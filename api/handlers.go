package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"s3migration/pkg/core"
	"s3migration/pkg/models"
	"s3migration/pkg/pool"
	"s3migration/pkg/providers/googledrive"
	"s3migration/pkg/state"
)

// TaskManager manages migration tasks (in-memory + RDS persistent state)
type TaskManager struct {
	mu           sync.RWMutex
	tasks        map[string]*TaskInfo
	stateManager state.StateManager
}

// TaskInfo contains task information
type TaskInfo struct {
	ID               string
	Status           *models.MigrationStatus
	Result           *models.MigrationResult
	EnhancedMigrator *core.EnhancedMigrator
	GoogleMigrator   *googledrive.GoogleDriveMigrator
	CancelFn         context.CancelFunc
	StartTime        time.Time
	OriginalRequest  models.MigrationRequest
}

var taskManager *TaskManager

// InitTaskManager initializes the task manager with RDS/database backend
func InitTaskManager(dbDriver, dbConnectionString string) error {
	var stateManager state.StateManager
	var err error

	// Create database-backed state manager
	stateManager, err = state.NewDBStateManager(dbDriver, dbConnectionString)
	if err != nil {
		return fmt.Errorf("failed to initialize database state manager: %w", err)
	}

	taskManager = &TaskManager{
		tasks:        make(map[string]*TaskInfo),
		stateManager: stateManager,
	}

	// Load existing tasks from database on startup (for pod restarts)
	if err := taskManager.loadExistingTasks(); err != nil {
		fmt.Printf("Warning: failed to load existing tasks: %v\n", err)
	}

	// Start background jobs
	go taskManager.cleanupOldTasks()
	go taskManager.periodicStateSave()

	fmt.Printf("✅ Task manager initialized with %s database backend\n", dbDriver)
	return nil
}

// loadExistingTasks loads tasks from database on startup
func (tm *TaskManager) loadExistingTasks() error {
	tasks, err := tm.stateManager.ListTasks()
	if err != nil {
		return err
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	for _, taskState := range tasks {
		// Only load running tasks (completed tasks are for history only)
		if taskState.Status == "running" {
			// Mark as failed since pod restarted mid-migration
			taskState.Status = "failed"
			taskState.Errors = append(taskState.Errors, "Migration interrupted by pod restart")
			now := time.Now()
			taskState.EndTime = &now
			
			// Save updated state
			tm.stateManager.SaveTask(taskState)
		}
		
		// Convert to MigrationStatus for in-memory storage
		status := &models.MigrationStatus{
			TaskID:        taskState.ID,
			Status:        taskState.Status,
			Progress:      taskState.Progress,
			CopiedObjects: taskState.CopiedObjects,
			TotalObjects:  taskState.TotalObjects,
			CopiedSize:    taskState.CopiedSize,
			TotalSize:     taskState.TotalSize,
			CurrentSpeed:  taskState.CurrentSpeed,
			ETA:           taskState.ETA,
			Duration:      taskState.Duration,
			Errors:        taskState.Errors,
			MigrationType: taskState.MigrationType,
			DryRun:        taskState.DryRun,
		}

		tm.tasks[taskState.ID] = &TaskInfo{
			ID:        taskState.ID,
			Status:    status,
			StartTime: taskState.StartTime,
		}

		fmt.Printf("Loaded task %s from database (status: %s)\n", taskState.ID, taskState.Status)
	}

	return nil
}

// cleanupOldTasks runs periodically to clean up old completed tasks
func (tm *TaskManager) cleanupOldTasks() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		// Clean up tasks older than 7 days
		if err := tm.stateManager.CleanupOldTasks(7 * 24 * time.Hour); err != nil {
			fmt.Printf("Error cleaning up old tasks: %v\n", err)
		}
	}
}

// periodicStateSave saves all task states to database periodically
func (tm *TaskManager) periodicStateSave() {
	ticker := time.NewTicker(5 * time.Second) // Save every 5 seconds for real-time updates
	defer ticker.Stop()

	for range ticker.C {
		tm.mu.RLock()
		tasks := make([]*TaskInfo, 0, len(tm.tasks))
		for _, task := range tm.tasks {
			tasks = append(tasks, task)
		}
		tm.mu.RUnlock()

		// Save each task (non-blocking)
		for _, task := range tasks {
			if err := tm.saveTaskState(task); err != nil {
				// Silently fail - don't spam logs
			}
		}
	}
}

// saveTaskState persists task state to database
func (tm *TaskManager) saveTaskState(taskInfo *TaskInfo) error {
	if tm.stateManager == nil {
		return fmt.Errorf("state manager not initialized")
	}

	taskState := &state.TaskState{
		ID:            taskInfo.ID,
		Status:        taskInfo.Status.Status,
		Progress:      taskInfo.Status.Progress,
		CopiedObjects: taskInfo.Status.CopiedObjects,
		TotalObjects:  taskInfo.Status.TotalObjects,
		CopiedSize:    taskInfo.Status.CopiedSize,
		TotalSize:     taskInfo.Status.TotalSize,
		CurrentSpeed:  taskInfo.Status.CurrentSpeed,
		ETA:           taskInfo.Status.ETA,
		Duration:      taskInfo.Status.Duration,
		Errors:        taskInfo.Status.Errors,
		StartTime:     taskInfo.StartTime,
		MigrationType: taskInfo.Status.MigrationType,
		DryRun:        taskInfo.Status.DryRun,
		SyncMode:      false, // Default to false
	}

	// Set end time for completed tasks
	if taskInfo.Status.Status == "completed" || taskInfo.Status.Status == "failed" || taskInfo.Status.Status == "cancelled" {
		now := time.Now()
		taskState.EndTime = &now
	}

	// Store original request (simplified version)
	taskState.OriginalRequest = map[string]interface{}{
		"source_bucket": taskInfo.OriginalRequest.SourceBucket,
		"dest_bucket":   taskInfo.OriginalRequest.DestBucket,
		"dry_run":       taskInfo.OriginalRequest.DryRun,
	}

	return tm.stateManager.SaveTask(taskState)
}

// Auto-generate encryption key with multiple fallback options
func getOrGenerateEncryptionKey() (string, error) {
	// Priority 1: Environment variable
	if envKey := os.Getenv("ENCRYPTION_KEY"); envKey != "" {
		return envKey, nil
	}
	
	// Priority 2: Key file in data directory
	keyFile := "/app/data/encryption.key"
	if key, err := loadKeyFromFile(keyFile); err == nil && key != "" {
		return key, nil
	}
	
	// Priority 3: Generate and save new key
	return generateAndSaveKey(keyFile)
}

// Load encryption key from file
func loadKeyFromFile(keyFile string) (string, error) {
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		return "", fmt.Errorf("key file does not exist")
	}
	
	data, err := ioutil.ReadFile(keyFile)
	if err != nil {
		return "", err
	}
	
	key := strings.TrimSpace(string(data))
	if len(key) < 16 {
		return "", fmt.Errorf("key too short")
	}
	
	return key, nil
}

// Generate and save new encryption key
func generateAndSaveKey(keyFile string) (string, error) {
	// Create data directory if it doesn't exist
	dataDir := filepath.Dir(keyFile)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create data directory: %v", err)
	}
	
	// Generate 32-byte random key
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", fmt.Errorf("failed to generate random key: %v", err)
	}
	
	// Convert to base64 string
	key := base64.StdEncoding.EncodeToString(keyBytes)
	
	// Save to file
	if err := ioutil.WriteFile(keyFile, []byte(key), 0600); err != nil {
		return "", fmt.Errorf("failed to save key file: %v", err)
	}
	
	return key, nil
}

// Security: Encrypt sensitive data before storing
func encryptCredentials(data string) (string, error) {
	if data == "" {
		return "", nil
	}
	
	keyStr, err := getOrGenerateEncryptionKey()
	if err != nil {
		return "", err
	}
	key := []byte(keyStr)
	
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	
	ciphertext := gcm.Seal(nonce, nonce, []byte(data), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Security: Decrypt sensitive data when needed
func decryptCredentials(encryptedData string) (string, error) {
	if encryptedData == "" {
		return "", nil
	}
	
	data, err := base64.StdEncoding.DecodeString(encryptedData)
	if err != nil {
		return "", err
	}
	
	keyStr, err := getOrGenerateEncryptionKey()
	if err != nil {
		return "", err
	}
	key := []byte(keyStr)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	
	return string(plaintext), nil
}

// Security: Create a sanitized request copy without sensitive data
func sanitizeRequestForStorage(req *models.MigrationRequest) *models.MigrationRequest {
	sanitized := *req
	
	// Encrypt source credentials if present
	if sanitized.SourceCredentials != nil {
		encrypted := *sanitized.SourceCredentials
		
		if encryptedAccessKey, err := encryptCredentials(encrypted.AccessKey); err == nil {
			encrypted.AccessKey = encryptedAccessKey
		}
		if encryptedSecretKey, err := encryptCredentials(encrypted.SecretKey); err == nil {
			encrypted.SecretKey = encryptedSecretKey
		}
		
		sanitized.SourceCredentials = &encrypted
	}
	
	// Encrypt destination credentials if present
	if sanitized.DestCredentials != nil {
		encrypted := *sanitized.DestCredentials
		
		if encryptedAccessKey, err := encryptCredentials(encrypted.AccessKey); err == nil {
			encrypted.AccessKey = encryptedAccessKey
		}
		if encryptedSecretKey, err := encryptCredentials(encrypted.SecretKey); err == nil {
			encrypted.SecretKey = encryptedSecretKey
		}
		
		sanitized.DestCredentials = &encrypted
	}
	
	// Backward compatibility: encrypt old Credentials field
	if sanitized.Credentials != nil {
		encrypted := *sanitized.Credentials
		
		if encryptedAccessKey, err := encryptCredentials(encrypted.AccessKey); err == nil {
			encrypted.AccessKey = encryptedAccessKey
		}
		if encryptedSecretKey, err := encryptCredentials(encrypted.SecretKey); err == nil {
			encrypted.SecretKey = encryptedSecretKey
		}
		
		sanitized.Credentials = &encrypted
	}
	
	return &sanitized
}

// Security: Restore sensitive data for retry
func restoreRequestForRetry(sanitizedReq *models.MigrationRequest) *models.MigrationRequest {
	restored := *sanitizedReq
	
	// Decrypt source credentials if present
	if restored.SourceCredentials != nil {
		decrypted := *restored.SourceCredentials
		
		if decryptedAccessKey, err := decryptCredentials(decrypted.AccessKey); err == nil {
			decrypted.AccessKey = decryptedAccessKey
		}
		if decryptedSecretKey, err := decryptCredentials(decrypted.SecretKey); err == nil {
			decrypted.SecretKey = decryptedSecretKey
		}
		
		restored.SourceCredentials = &decrypted
	}
	
	// Decrypt destination credentials if present
	if restored.DestCredentials != nil {
		decrypted := *restored.DestCredentials
		
		if decryptedAccessKey, err := decryptCredentials(decrypted.AccessKey); err == nil {
			decrypted.AccessKey = decryptedAccessKey
		}
		if decryptedSecretKey, err := decryptCredentials(decrypted.SecretKey); err == nil {
			decrypted.SecretKey = decryptedSecretKey
		}
		
		restored.DestCredentials = &decrypted
	}
	
	// Backward compatibility: decrypt old Credentials field
	if restored.Credentials != nil {
		decrypted := *restored.Credentials
		
		if decryptedAccessKey, err := decryptCredentials(decrypted.AccessKey); err == nil {
			decrypted.AccessKey = decryptedAccessKey
		}
		if decryptedSecretKey, err := decryptCredentials(decrypted.SecretKey); err == nil {
			decrypted.SecretKey = decryptedSecretKey
		}
		
		restored.Credentials = &decrypted
	}
	
	return &restored
}

// StartMigration handles POST /migrate
// @Summary Start a migration
// @Description Start a new S3 bucket migration task
// @Tags migration
// @Accept json
// @Produce json
// @Param request body models.MigrationRequest true "Migration request"
// @Success 200 {object} models.MigrationStatus
// @Failure 400 {object} gin.H
// @Router /api/migrate [post]
func StartMigration(c *gin.Context) {
	fmt.Printf("=== MIGRATION HANDLER CALLED ===\n")
	var req models.MigrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fmt.Printf("ERROR: Failed to bind JSON: %v\n", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fmt.Printf("Request received: %+v\n", req)
	
	// Validate bucket combinations
	if req.SourceBucket == "" && req.DestBucket != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "When source bucket is empty (all buckets), destination bucket must also be empty"})
		return
	}
	if req.SourceBucket != "" && req.DestBucket == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Destination bucket is required when source bucket is specified"})
		return
	}
	
	// Generate task ID
	taskID := uuid.New().String()
	
	// Check if this is an all-buckets migration
	if req.SourceBucket == "" {
		// Start all-buckets migration
		go runAllBucketsMigration(context.Background(), taskID, req)
		
		// Store task info
		status := &models.MigrationStatus{
			TaskID:    taskID,
			Status:    "running",
			StartTime: time.Now(),
		}
		taskInfo := TaskInfo{
			ID:             taskID,
			Status:         status,
			StartTime:      time.Now(),
			OriginalRequest: req,
		}
		taskManager.mu.Lock()
		taskManager.tasks[taskID] = &taskInfo
		taskManager.mu.Unlock()
		
		c.JSON(http.StatusOK, *status)
		return
	}

	// Create migrator with credentials
	ctx, cancel := context.WithCancel(context.Background())
	
	var enhancedMigrator *core.EnhancedMigrator
	var err error
	
	// Handle backward compatibility: if Credentials is provided, use it as SourceCredentials
	if req.Credentials != nil && req.SourceCredentials == nil {
		req.SourceCredentials = req.Credentials
	}
	
	// Determine region and endpoint from SOURCE credentials
	region := "us-east-1"
	endpointURL := ""
	
	if req.SourceCredentials != nil {
		if req.SourceCredentials.Region != "" {
			region = req.SourceCredentials.Region
		}
		endpointURL = req.SourceCredentials.EndpointURL
	}
	
	// Create enhanced migrator with optimal configuration
	cfg := core.EnhancedMigratorConfig{
		Region:             region,
		EndpointURL:        endpointURL,
		ConnectionPoolSize: 20, // Increased for better performance
		EnableStreaming:    false, // Disabled - use multipart copy for large files instead
		EnablePrefetch:     true,
		StreamChunkSize:    0, // Not used when streaming is disabled
		CacheTTL:           5 * time.Minute,
		CacheSize:          1000,
		AccessKey:          "", // Will be set below if provided
		SecretKey:          "", // Will be set below if provided
	}
	
	// Add explicit source credentials if provided
	if req.SourceCredentials != nil && req.SourceCredentials.AccessKey != "" && req.SourceCredentials.SecretKey != "" {
		cfg.AccessKey = req.SourceCredentials.AccessKey
		cfg.SecretKey = req.SourceCredentials.SecretKey
	}
	
	enhancedMigrator, err = core.NewEnhancedMigrator(ctx, cfg)
	
	if err != nil {
		cancel()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create task info
	status := &models.MigrationStatus{
		TaskID:         taskID,
		Status:         "pending",
		MigrationType:  "s3",
		Progress:       0,
		StartTime:      time.Now(),
		LastUpdateTime: time.Now(),
		DryRun:         req.DryRun,
		DryRunVerified: []string{},
		SampleFiles:    []string{},
	}

	taskInfo := &TaskInfo{
		ID:               taskID,
		Status:           status,
		EnhancedMigrator: enhancedMigrator,
		CancelFn:         cancel,
		StartTime:        time.Now(),
		OriginalRequest:  *sanitizeRequestForStorage(&req), // Encrypt sensitive data
	}

	taskManager.mu.Lock()
	taskManager.tasks[taskID] = taskInfo
	taskManager.mu.Unlock()

	// Start migration in background
	go runEnhancedMigration(ctx, taskID, enhancedMigrator, req)

	c.JSON(http.StatusOK, status)
}

func maskCredential(cred string) string {
	if cred == "" {
		return "***"
	}
	if len(cred) < 8 {
		return "***"
	}
	return cred[:4] + "***" + cred[len(cred)-4:]
}

func runEnhancedMigration(ctx context.Context, taskID string, enhancedMigrator *core.EnhancedMigrator, req models.MigrationRequest) {
	// Add panic recovery to prevent server crashes
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Panic in enhanced migration %s: %v\n", taskID, r)
			taskManager.mu.Lock()
			if task, exists := taskManager.tasks[taskID]; exists {
				task.Status.Status = "failed"
				task.Status.Errors = append(task.Status.Errors, fmt.Sprintf("Panic: %v", r))
			}
			taskManager.mu.Unlock()
		}
	}()
	
	fmt.Printf("=== ENHANCED MIGRATION DEBUG START ===\n")
	fmt.Printf("Task ID: %s\n", taskID)
	fmt.Printf("Request: %+v\n", req)
	
	// Update status to running
	taskManager.mu.Lock()
	if task, exists := taskManager.tasks[taskID]; exists {
		task.Status.Status = "running"
	}
	taskManager.mu.Unlock()

	// Execute migration
	timeout := time.Duration(req.Timeout) * time.Second
	if timeout == 0 {
		timeout = 1 * time.Hour
	}

	// Handle backward compatibility: if Credentials is provided, use it as SourceCredentials
	if req.Credentials != nil && req.SourceCredentials == nil {
		req.SourceCredentials = req.Credentials
	}

	// Get region from credentials
	destRegion := ""
	if req.DestCredentials != nil && req.DestCredentials.Region != "" {
		destRegion = req.DestCredentials.Region
	} else if req.SourceCredentials != nil && req.SourceCredentials.Region != "" {
		destRegion = req.SourceCredentials.Region
	}

	fmt.Printf("\n=== MIGRATION REQUEST DEBUG ===\n")
	fmt.Printf("Source Bucket: %s\n", req.SourceBucket)
	fmt.Printf("Source Prefix: '%s'\n", req.SourcePrefix)
	fmt.Printf("Dest Bucket: %s\n", req.DestBucket)
	fmt.Printf("Dest Prefix: '%s'\n", req.DestPrefix)
	fmt.Printf("Dry Run: %v\n", req.DryRun)
	if req.SourceCredentials != nil {
		maskedAccessKey := maskCredential(req.SourceCredentials.AccessKey)
		fmt.Printf("Source Access Key: %s\n", maskedAccessKey)
		fmt.Printf("Source Region: %s\n", req.SourceCredentials.Region)
		fmt.Printf("Source Endpoint: %s\n", req.SourceCredentials.EndpointURL)
	}
	if req.DestCredentials != nil {
		maskedAccessKey := maskCredential(req.DestCredentials.AccessKey)
		fmt.Printf("Dest Access Key: %s (CROSS-ACCOUNT COPY)\n", maskedAccessKey)
		fmt.Printf("Dest Region: %s\n", req.DestCredentials.Region)
		fmt.Printf("Dest Endpoint: %s\n", req.DestCredentials.EndpointURL)
	}
	fmt.Printf("================================\n\n")
	
	// Determine migration mode
	migrationMode := core.MigrationMode(req.MigrationMode)
	if migrationMode == "" {
		migrationMode = core.ModeFullRewrite // Default to full rewrite
	}
	
	input := core.MigrateInput{
		SourceBucket:  req.SourceBucket,
		DestBucket:    req.DestBucket,
		SourcePrefix:  req.SourcePrefix,
		DestPrefix:    req.DestPrefix,
		DestRegion:    destRegion, // Region for destination bucket creation (empty for custom providers)
		DryRun:        req.DryRun,
		MigrationMode: migrationMode,
		Timeout:       timeout,
		ProgressCallback: func(progress float64, copied, total int64, speed float64, eta string) {
			// Update task status in real-time
			taskManager.mu.Lock()
			if task, exists := taskManager.tasks[taskID]; exists {
				task.Status.Progress = progress
				task.Status.CopiedObjects = copied
				task.Status.TotalObjects = total
				task.Status.CurrentSpeed = speed
				task.Status.ETA = eta
				task.Status.LastUpdateTime = time.Now()
			}
			taskManager.mu.Unlock()
		},
	}

	// Add destination credentials if different from source
	if req.DestCredentials != nil {
		input.DestAccessKey = req.DestCredentials.AccessKey
		input.DestSecretKey = req.DestCredentials.SecretKey
		input.DestEndpointURL = req.DestCredentials.EndpointURL
	}

	fmt.Printf("Starting enhanced migration task %s: %s -> %s (DryRun: %v)\n", 
		taskID, input.SourceBucket, input.DestBucket, input.DryRun)
	fmt.Printf("Input: %+v\n", input)
	fmt.Printf("Using enhanced migrator with all optimizations\n")
	
	var result *core.MigrateResult
	var err error
	
	if enhancedMigrator == nil {
		// Create a new migrator for retry tasks using the original request credentials
		fmt.Printf("Creating new enhanced migrator for retry task\n")
		
		// Check if credentials are available
		if req.SourceCredentials == nil {
			err = fmt.Errorf("cannot retry task: source credentials not available (credentials are not persisted for security reasons)")
			fmt.Printf("ERROR: %v\n", err)
		} else {
			enhancedMigrator, err = core.NewEnhancedMigrator(ctx, core.EnhancedMigratorConfig{
				ConnectionPoolSize: 10,
				StreamChunkSize:    64 * 1024 * 1024, // 64MB
				AccessKey:          req.SourceCredentials.AccessKey,
				SecretKey:          req.SourceCredentials.SecretKey,
				Region:             destRegion,
				EndpointURL:        req.SourceCredentials.EndpointURL,
			})
			if err != nil {
				fmt.Printf("Failed to create enhanced migrator: %v\n", err)
			} else {
				result, err = enhancedMigrator.Migrate(ctx, input)
			}
		}
	} else {
		result, err = enhancedMigrator.Migrate(ctx, input)
	}
	
	fmt.Printf("=== ENHANCED MIGRATION DEBUG RESULT ===\n")
	fmt.Printf("Error: %v\n", err)
	fmt.Printf("Result: %+v\n", result)
	fmt.Printf("=== ENHANCED MIGRATION DEBUG END ===\n")

	// Update final status
	taskManager.mu.Lock()
	defer taskManager.mu.Unlock()

	if task, exists := taskManager.tasks[taskID]; exists {
		if err != nil {
			fmt.Printf("Enhanced migration %s failed: %v\n", taskID, err)
			task.Status.Status = "failed"
			task.Status.Errors = append(task.Status.Errors, err.Error())
			task.Status.Progress = 0
			// Don't try to access result if it's nil
			if result == nil {
				return
			}
		} else if result.Cancelled {
			task.Status.Status = "cancelled"
		} else if result.Failed > 0 {
			task.Status.Status = "completed_with_errors"
		} else {
			task.Status.Status = "completed"
		}
		
		// Set end time and duration
		task.Status.EndTime = time.Now()
		duration := task.Status.EndTime.Sub(task.Status.StartTime)
		task.Status.Duration = formatDuration(duration)

		task.Result = &models.MigrationResult{
			TaskID:       taskID,
			Success:      result.Failed == 0 && !result.Cancelled,
			Copied:       result.Copied,
			Failed:       result.Failed,
			TotalSizeMB:  result.TotalSizeMB,
			CopiedSizeMB: result.CopiedSizeMB,
			ElapsedTime:  result.ElapsedTime,
			AvgSpeedMB:   result.AvgSpeedMB,
			Errors:       result.Errors,
		}

		// Update progress metrics for all runs (dry run and actual)
		if result.DryRun {
			task.Status.DryRunVerified = result.DryRunVerified
			task.Status.SampleFiles = []string{} // Not showing sample files
			// Update progress metrics for dry run
			task.Status.Progress = 100.0
			task.Status.CopiedObjects = result.Copied
			task.Status.TotalObjects = result.Copied // For dry run, "copied" means "would be copied"
			task.Status.CopiedSize = int64(result.TotalSizeMB * 1024 * 1024)
			task.Status.TotalSize = int64(result.TotalSizeMB * 1024 * 1024)
			task.Status.CurrentSpeed = result.AvgSpeedMB
			task.Status.ETA = "0s"
		} else {
			// Update progress metrics for actual runs
			totalObjects := result.Copied + result.Failed
			if totalObjects > 0 {
				task.Status.Progress = float64(result.Copied) / float64(totalObjects) * 100.0
			} else {
				task.Status.Progress = 100.0 // Completed but no objects processed
			}
			task.Status.CopiedObjects = result.Copied
			task.Status.TotalObjects = totalObjects
			task.Status.CopiedSize = int64(result.CopiedSizeMB * 1024 * 1024)
			task.Status.TotalSize = int64(result.TotalSizeMB * 1024 * 1024)
			task.Status.CurrentSpeed = result.AvgSpeedMB
			task.Status.ETA = "0s" // Completed
		}
	}
}

// GetStatus handles GET /status/:taskID
// @Summary Get migration status
// @Description Get the status of a migration task
// @Tags migration
// @Produce json
// @Param taskID path string true "Task ID"
// @Success 200 {object} models.MigrationStatus
// @Failure 404 {object} gin.H
// @Router /api/status/{taskID} [get]
func GetStatus(c *gin.Context) {
	taskID := c.Param("taskID")

	taskManager.mu.RLock()
	task, exists := taskManager.tasks[taskID]
	taskManager.mu.RUnlock()

	if !exists {
		// Task not in memory, check database
		taskState, err := taskManager.stateManager.LoadTask(taskID)
		if err != nil || taskState == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
			return
		}
		
		// Convert database task state to migration status
		status := &models.MigrationStatus{
			TaskID:        taskState.ID,
			Status:        taskState.Status,
			Progress:      taskState.Progress,
			CopiedObjects: taskState.CopiedObjects,
			TotalObjects:  taskState.TotalObjects,
			CopiedSize:    taskState.CopiedSize,
			TotalSize:     taskState.TotalSize,
			CurrentSpeed:  taskState.CurrentSpeed,
			ETA:           taskState.ETA,
			Duration:      taskState.Duration,
			Errors:        taskState.Errors,
			StartTime:     taskState.StartTime,
			MigrationType: taskState.MigrationType,
			DryRun:        taskState.DryRun,
			LastUpdateTime: time.Now(), // Set to current time for database tasks
		}
		
		// Handle EndTime conversion from pointer to value
		if taskState.EndTime != nil {
			status.EndTime = *taskState.EndTime
		}
		
		c.JSON(http.StatusOK, status)
		return
	}

	c.JSON(http.StatusOK, task.Status)
}

// ListTasks handles GET /tasks
// @Summary List all tasks
// @Description Get a list of all migration tasks
// @Tags migration
// @Produce json
// @Success 200 {array} string
// @Router /api/tasks [get]
func ListTasks(c *gin.Context) {
	taskManager.mu.RLock()
	memoryTaskIDs := make([]string, 0, len(taskManager.tasks))
	for id := range taskManager.tasks {
		memoryTaskIDs = append(memoryTaskIDs, id)
	}
	taskManager.mu.RUnlock()

	// Get all tasks from database
	dbTasks, err := taskManager.stateManager.ListTasks()
	if err != nil {
		// If database fails, fall back to memory-only
		c.JSON(http.StatusOK, memoryTaskIDs)
		return
	}

	// Combine memory and database task IDs
	taskIDSet := make(map[string]bool)
	for _, id := range memoryTaskIDs {
		taskIDSet[id] = true
	}
	for _, taskState := range dbTasks {
		taskIDSet[taskState.ID] = true
	}

	// Convert set back to slice
	taskIDs := make([]string, 0, len(taskIDSet))
	for id := range taskIDSet {
		taskIDs = append(taskIDs, id)
	}

	c.JSON(http.StatusOK, taskIDs)
}

// CancelTask handles DELETE /tasks/:taskID
// @Summary Cancel a migration task
// @Description Cancel a running migration task
// @Tags migration
// @Produce json
// @Param taskID path string true "Task ID"
// @Success 200 {object} gin.H
// @Failure 404 {object} gin.H
// @Router /api/tasks/{taskID} [delete]
func CancelTask(c *gin.Context) {
	taskID := c.Param("taskID")

	taskManager.mu.Lock()
	defer taskManager.mu.Unlock()

	task, exists := taskManager.tasks[taskID]
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}

	if task.Status.Status == "pending" || task.Status.Status == "running" {
		// Stop S3 migrator if it exists (S3-to-S3 migration)
		if task.EnhancedMigrator != nil {
			task.EnhancedMigrator.Stop()
		}
		
		// Cancel the context (works for both S3 and Google Drive migrations)
		if task.CancelFn != nil {
			task.CancelFn()
		}
		
		task.Status.Status = "cancelled"
		fmt.Printf("Task %s cancelled by user\n", taskID)
		c.JSON(http.StatusOK, gin.H{"status": "cancelled", "message": "Task cancelled successfully"})
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("task cannot be cancelled (status: %s)", task.Status.Status)})
	}
}

// RetryTask removed - credentials are not persisted for security reasons
// Users should start a new migration which will automatically skip already copied files

// HealthCheck handles GET /health
// @Summary Health check
// @Description Check if the API is running
// @Tags system
// @Produce json
// @Success 200 {object} gin.H
// @Router /health [get]
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "healthy",
		"time":   time.Now(),
	})
}

// runAllBucketsMigration migrates all buckets from source to destination
func runAllBucketsMigration(ctx context.Context, taskID string, req models.MigrationRequest) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("All-buckets migration panic: %v\n", r)
		taskManager.mu.Lock()
		if task, exists := taskManager.tasks[taskID]; exists {
			task.Status.Status = "failed"
			task.Status.Errors = []string{fmt.Sprintf("Migration panic: %v", r)}
		}
		taskManager.mu.Unlock()
		}
	}()

	// Create S3 client for listing buckets
	region := "us-east-1"
	endpointURL := ""
	if req.SourceCredentials != nil {
		if req.SourceCredentials.Region != "" {
			region = req.SourceCredentials.Region
		}
		endpointURL = req.SourceCredentials.EndpointURL
	}

	cfg := pool.ConnectionPoolConfig{
		Region:      region,
		EndpointURL: endpointURL,
		Timeout:     time.Duration(req.Timeout) * time.Second,
		MaxRetries:  3,
	}

	if req.SourceCredentials != nil && req.SourceCredentials.AccessKey != "" && req.SourceCredentials.SecretKey != "" {
		cfg.AccessKey = req.SourceCredentials.AccessKey
		cfg.SecretKey = req.SourceCredentials.SecretKey
	}

	cp, err := pool.NewConnectionPool(ctx, cfg)
	if err != nil {
		taskManager.mu.Lock()
		if task, exists := taskManager.tasks[taskID]; exists {
			task.Status.Status = "failed"
			task.Status.Errors = []string{fmt.Sprintf("Failed to create connection pool: %v", err)}
		}
		taskManager.mu.Unlock()
		return
	}
	client := cp.GetClient()

	// List all buckets
	fmt.Printf("Listing all buckets...\n")
	listBucketsOutput, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		taskManager.mu.Lock()
		if task, exists := taskManager.tasks[taskID]; exists {
			task.Status.Status = "failed"
			task.Status.Errors = []string{fmt.Sprintf("Failed to list buckets: %v", err)}
		}
		taskManager.mu.Unlock()
		return
	}

	if len(listBucketsOutput.Buckets) == 0 {
		taskManager.mu.Lock()
		if task, exists := taskManager.tasks[taskID]; exists {
			task.Status.Status = "completed"
			task.Status.TotalObjects = 0
			task.Status.CopiedObjects = 0
		}
		taskManager.mu.Unlock()
		return
	}

	taskManager.mu.Lock()
	if task, exists := taskManager.tasks[taskID]; exists {
		task.Status.TotalObjects = int64(len(listBucketsOutput.Buckets))
		task.Status.CopiedObjects = 0
	}
	taskManager.mu.Unlock()

	// Create enhanced migrator
	enhancedMigrator, err := core.NewEnhancedMigrator(ctx, core.EnhancedMigratorConfig{
		ConnectionPoolSize: 10,
		StreamChunkSize:    64 * 1024 * 1024, // 64MB
		AccessKey:          cfg.AccessKey,
		SecretKey:          cfg.SecretKey,
		Region:             region,
		EndpointURL:        endpointURL,
	})
	if err != nil {
		taskManager.mu.Lock()
		if task, exists := taskManager.tasks[taskID]; exists {
			task.Status.Status = "failed"
			task.Status.Errors = []string{fmt.Sprintf("Failed to create enhanced migrator: %v", err)}
		}
		taskManager.mu.Unlock()
		return
	}

	var totalObjects, completedObjects int64
	var totalSize, completedSize int64

	// Migrate each bucket
	for i, bucket := range listBucketsOutput.Buckets {
		bucketName := *bucket.Name
		fmt.Printf("Migrating bucket %d/%d: %s\n", i+1, len(listBucketsOutput.Buckets), bucketName)

		// Create migration request for this bucket
		bucketReq := models.MigrationRequest{
			SourceBucket:      bucketName,
			DestBucket:        bucketName, // Use same name for destination
			SourcePrefix:      req.SourcePrefix,
			DestPrefix:        req.DestPrefix,
			SourceCredentials: req.SourceCredentials,
			DestCredentials:   req.DestCredentials,
			DryRun:            req.DryRun,
			Timeout:           req.Timeout,
		}

		// Create input for enhanced migrator
		// Determine migration mode
		migrationMode := core.MigrationMode(bucketReq.MigrationMode)
		if migrationMode == "" {
			migrationMode = core.ModeFullRewrite // Default to full rewrite
		}
		
		input := core.MigrateInput{
			SourceBucket:      bucketReq.SourceBucket,
			DestBucket:        bucketReq.DestBucket,
			SourcePrefix:      bucketReq.SourcePrefix,
			DestPrefix:        bucketReq.DestPrefix,
			MigrationMode:     migrationMode,
		}
		
		// Add destination credentials if provided
		if bucketReq.DestCredentials != nil {
			input.DestAccessKey = bucketReq.DestCredentials.AccessKey
			input.DestSecretKey = bucketReq.DestCredentials.SecretKey
			input.DestEndpointURL = bucketReq.DestCredentials.EndpointURL
		}

		// Run migration for this bucket
		result, err := enhancedMigrator.Migrate(ctx, input)
		if err != nil {
			fmt.Printf("Failed to migrate bucket %s: %v\n", bucketName, err)
			taskManager.mu.Lock()
			if task, exists := taskManager.tasks[taskID]; exists {
				task.Status.Errors = append(task.Status.Errors, fmt.Sprintf("Failed to migrate bucket %s: %v", bucketName, err))
			}
			taskManager.mu.Unlock()
			continue
		}

		// Update totals
		totalObjects += result.Copied + result.Failed
		completedObjects += result.Copied
		totalSize += int64(result.TotalSizeMB * 1024 * 1024) // Convert MB to bytes
		completedSize += int64(result.CopiedSizeMB * 1024 * 1024) // Convert MB to bytes

		// Update task progress
		taskManager.mu.Lock()
		if task, exists := taskManager.tasks[taskID]; exists {
			task.Status.CopiedObjects = int64(i + 1)
			task.Status.TotalObjects = int64(len(listBucketsOutput.Buckets))
		}
		taskManager.mu.Unlock()
	}

	// Mark as completed
	taskManager.mu.Lock()
	if task, exists := taskManager.tasks[taskID]; exists {
		task.Status.Status = "completed"
		task.Status.TotalObjects = totalObjects
		task.Status.CopiedObjects = completedObjects
		task.Status.TotalSize = totalSize
		task.Status.CopiedSize = completedSize
	}
	taskManager.mu.Unlock()

	fmt.Printf("All-buckets migration completed. Migrated %d buckets, %d objects, %d bytes\n", 
		len(listBucketsOutput.Buckets), totalObjects, completedSize)
}

// GoogleDriveQuickAuthURL handles token exchange for public OAuth app
func GoogleDriveQuickAuthURL(c *gin.Context) {
    var req struct {
        Code string `json:"code" binding:"required"`
    }

    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Authorization code is required"})
        return
    }

    // Use public OAuth app credentials from environment
    clientID := os.Getenv("GOOGLE_CLIENT_ID")
    clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
    if clientID == "" || clientSecret == "" {
        c.JSON(http.StatusServiceUnavailable, gin.H{
            "error": "Google OAuth not configured. Please set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET environment variables or use custom OAuth.",
        })
        return
    }
    redirectURL := fmt.Sprintf("%s://%s/auth/callback", 
        func() string {
            if c.Request.Header.Get("X-Forwarded-Proto") == "http" || 
               strings.HasPrefix(c.Request.Host, "localhost") || 
               strings.HasPrefix(c.Request.Host, "127.0.0.1") {
                return "http"
            }
            return "https"
        }(), c.Request.Host)

    // Create auth handler
    authHandler := googledrive.NewAuthHandler(c.Request.Context(), googledrive.OAuthConfig{
        ClientID:     clientID,
        ClientSecret: clientSecret,
        RedirectURL:  redirectURL,
    })

    // Exchange code for token
    tokenResponse, err := authHandler.ExchangeCodeForToken(req.Code)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to exchange token: %v", err)})
        return
    }

    c.JSON(http.StatusOK, tokenResponse)
}

// GoogleDriveAuthURL generates OAuth URL for Google Drive authentication
func GoogleDriveAuthURL(c *gin.Context) {
	var req struct {
		ClientID     string `json:"client_id" binding:"required"`
		ClientSecret string `json:"client_secret" binding:"required"`
		RedirectURL  string `json:"redirect_url" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create auth handler
	authHandler := googledrive.NewAuthHandler(c.Request.Context(), googledrive.OAuthConfig{
		ClientID:     req.ClientID,
		ClientSecret: req.ClientSecret,
		RedirectURL:  req.RedirectURL,
	})

	// Generate state for CSRF protection
	state := uuid.New().String()

	// Get auth URL
	authURL := authHandler.GetAuthURL(state)

	c.JSON(http.StatusOK, gin.H{
		"auth_url": authURL,
		"state":    state,
	})
}

// GoogleDriveExchangeToken exchanges authorization code for access token
func GoogleDriveExchangeToken(c *gin.Context) {
	var req struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RedirectURL  string `json:"redirect_url"`
		Code         string `json:"code" binding:"required"`
		QuickLogin   bool   `json:"quick_login"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var clientID, clientSecret, redirectURL string

	if req.QuickLogin {
		// Use OAuth credentials from environment for public use
		clientID = os.Getenv("GOOGLE_CLIENT_ID")
		clientSecret = os.Getenv("GOOGLE_CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Quick login not configured. Please set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET environment variables or use custom OAuth.",
			})
			return
		}
		
		// Get the current domain from the request to build redirect URL
		host := c.Request.Host
		scheme := "https"
		if c.Request.Header.Get("X-Forwarded-Proto") == "http" || 
		   strings.HasPrefix(host, "localhost") || 
		   strings.HasPrefix(host, "127.0.0.1") {
			scheme = "http"
		}
		redirectURL = fmt.Sprintf("%s://%s/auth/callback", scheme, host)
	} else {
		// Use user-provided credentials
		if req.ClientID == "" || req.ClientSecret == "" || req.RedirectURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "client_id, client_secret, and redirect_url are required for custom OAuth"})
			return
		}
		clientID = req.ClientID
		clientSecret = req.ClientSecret
		redirectURL = req.RedirectURL
	}

	// Create auth handler
	authHandler := googledrive.NewAuthHandler(c.Request.Context(), googledrive.OAuthConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
	})

	// Exchange code for token
	tokenResponse, err := authHandler.ExchangeCodeForToken(req.Code)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to exchange token: %v", err)})
		return
	}

	c.JSON(http.StatusOK, tokenResponse)
}

// GoogleDriveListFolders lists folders in Google Drive
func GoogleDriveListFolders(c *gin.Context) {
	var req struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		AccessToken  string `json:"access_token" binding:"required"`
		RefreshToken string `json:"refresh_token" binding:"required"`
		ParentID     string `json:"parent_id"` // Empty for root folder
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Determine if this is a public OAuth app or custom OAuth
	var clientID, clientSecret string
	if req.ClientID == "" || req.ClientSecret == "" {
		// Public OAuth app - use credentials from environment
		clientID = os.Getenv("GOOGLE_CLIENT_ID")
		clientSecret = os.Getenv("GOOGLE_CLIENT_SECRET")
		if clientID == "" || clientSecret == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Google OAuth not configured. Please set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET environment variables or provide client_id and client_secret in request.",
			})
			return
		}
	} else {
		// Custom OAuth - use user credentials
		clientID = req.ClientID
		clientSecret = req.ClientSecret
	}

	// Create Google Drive client
	client, err := googledrive.NewClient(c.Request.Context(), googledrive.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AccessToken:  req.AccessToken,
		RefreshToken: req.RefreshToken,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to create client: %v", err)})
		return
	}

	// List folders
	folders, err := client.ListFolders(req.ParentID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Failed to list folders: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"folders": folders})
}

// StartGoogleDriveMigration starts a Google Drive to S3 migration
func StartGoogleDriveMigration(c *gin.Context) {
	var req models.GoogleDriveMigrationRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate required fields
	if req.SourceCredentials == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_credentials is required"})
		return
	}
	if req.DestBucket == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dest_bucket is required"})
		return
	}

	// Generate task ID
	taskID := uuid.New().String()

	// Create context with timeout
	timeout := time.Duration(req.Timeout) * time.Second
	if timeout == 0 {
		timeout = 24 * time.Hour // Default timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	// Create task
	taskManager.mu.Lock()
	taskManager.tasks[taskID] = &TaskInfo{
		ID:        taskID,
		Status: &models.MigrationStatus{
			TaskID:        taskID,
			Status:        "pending",
			MigrationType: "google-drive",
			Progress:      0,
			StartTime:     time.Now(),
			DryRun:        req.DryRun,
		},
		CancelFn:        cancel,
		StartTime:       time.Now(),
		OriginalRequest: models.MigrationRequest{}, // Empty for Google Drive
	}
	taskManager.mu.Unlock()

	// Start migration in goroutine
	go runGoogleDriveMigration(ctx, taskID, req)

	c.JSON(http.StatusOK, gin.H{
		"task_id": taskID,
		"message": "Google Drive migration started",
	})
}

// runGoogleDriveMigration executes the Google Drive to S3 migration
func runGoogleDriveMigration(ctx context.Context, taskID string, req models.GoogleDriveMigrationRequest) {
	defer func() {
		if r := recover(); r != nil {
			taskManager.mu.Lock()
			if task, exists := taskManager.tasks[taskID]; exists {
				task.Status.Status = "failed"
				task.Status.Errors = append(task.Status.Errors, fmt.Sprintf("Panic: %v", r))
			}
			taskManager.mu.Unlock()
		}
	}()

	// Update status to running
	taskManager.mu.Lock()
	if task, exists := taskManager.tasks[taskID]; exists {
		task.Status.Status = "running"
	}
	taskManager.mu.Unlock()

	// Create Google Drive client
	driveClient, err := googledrive.NewClient(ctx, googledrive.Config{
		ClientID:     req.SourceCredentials.ClientID,
		ClientSecret: req.SourceCredentials.ClientSecret,
		AccessToken:  req.SourceCredentials.AccessToken,
		RefreshToken: req.SourceCredentials.RefreshToken,
	})
	if err != nil {
		taskManager.mu.Lock()
		if task, exists := taskManager.tasks[taskID]; exists {
			task.Status.Status = "failed"
			task.Status.Errors = append(task.Status.Errors, fmt.Sprintf("Failed to create Google Drive client: %v", err))
		}
		taskManager.mu.Unlock()
		return
	}

	// Create S3 client for destination
	destCredentials := req.DestCredentials
	if destCredentials == nil {
		destCredentials = &models.Credentials{
			AccessKey:   req.SourceCredentials.AccessToken, // Fallback - this is wrong, should use source S3 creds
			SecretKey:   req.SourceCredentials.RefreshToken, // This needs to be fixed
			Region:      "us-east-1",
			EndpointURL: "",
		}
	}

	cp, err := pool.NewConnectionPool(ctx, pool.ConnectionPoolConfig{
		AccessKey:   destCredentials.AccessKey,
		SecretKey:   destCredentials.SecretKey,
		Region:      destCredentials.Region,
		EndpointURL: destCredentials.EndpointURL,
		Timeout:     time.Hour,
	})
	if err != nil {
		taskManager.mu.Lock()
		if task, exists := taskManager.tasks[taskID]; exists {
			task.Status.Status = "failed"
			task.Status.Errors = append(task.Status.Errors, fmt.Sprintf("Failed to create connection pool: %v", err))
		}
		taskManager.mu.Unlock()
		return
	}
	s3Client := cp.GetClient()

	// Create Google Drive migrator
	migrator := googledrive.NewGoogleDriveMigrator(ctx, driveClient, s3Client)

	// Create migration input
	migrationInput := googledrive.MigrationInput{
		SourceFolderID:   req.SourceFolderID,
		DestBucket:       req.DestBucket,
		DestPrefix:       req.DestPrefix,
		DryRun:           req.DryRun,
		IncludeSharedFiles: req.IncludeSharedFiles,
		ProgressCallback: func(progress float64, copied, total int64, copiedSize, totalSize int64, speed float64, eta string) {
			// Update task status in real-time
			taskManager.mu.Lock()
			if task, exists := taskManager.tasks[taskID]; exists {
				task.Status.Progress = progress
				task.Status.CopiedObjects = copied
				task.Status.TotalObjects = total
				task.Status.CopiedSize = copiedSize
				task.Status.TotalSize = totalSize
				task.Status.CurrentSpeed = speed
				task.Status.ETA = eta
				task.Status.LastUpdateTime = time.Now()
			}
			taskManager.mu.Unlock()
		},
	}

	// Run migration
	result, err := migrator.Migrate(migrationInput)
	if err != nil {
		taskManager.mu.Lock()
		if task, exists := taskManager.tasks[taskID]; exists {
			task.Status.Status = "failed"
			task.Status.Errors = append(task.Status.Errors, fmt.Sprintf("Migration failed: %v", err))
		}
		taskManager.mu.Unlock()
		return
	}

	// Mark as completed
	taskManager.mu.Lock()
	if task, exists := taskManager.tasks[taskID]; exists {
		task.Status.Status = "completed"
		task.Status.TotalObjects = result.TotalFiles
		task.Status.CopiedObjects = result.CopiedFiles
		task.Status.TotalSize = result.TotalSize
		task.Status.CopiedSize = result.CopiedSize
		
		// Set end time and duration
		task.Status.EndTime = time.Now()
		duration := task.Status.EndTime.Sub(task.Status.StartTime)
		task.Status.Duration = formatDuration(duration)
		
		task.Result = &models.MigrationResult{
			TaskID:       taskID,
			Success:      result.FailedFiles == 0,
			Copied:       result.CopiedFiles,
			Failed:       result.FailedFiles,
			TotalSizeMB:  float64(result.TotalSize) / (1024 * 1024),
			CopiedSizeMB: float64(result.CopiedSize) / (1024 * 1024),
			ElapsedTime:  result.Duration.String(),
			AvgSpeedMB:   float64(result.CopiedSize) / result.Duration.Seconds() / (1024 * 1024),
		}
	}
	taskManager.mu.Unlock()

	fmt.Printf("Google Drive migration completed. Migrated %d files, %d bytes\n", 
		result.CopiedFiles, result.CopiedSize)
}

// formatDuration formats a duration into a human-readable string
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	} else if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	} else if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh %dm", hours, minutes)
	} else {
		days := int(d.Hours() / 24)
		hours := int(d.Hours()) % 24
		return fmt.Sprintf("%dd %dh", days, hours)
	}
}
