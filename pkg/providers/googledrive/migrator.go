package googledrive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// GoogleDriveMigrator handles migration from Google Drive to S3
type GoogleDriveMigrator struct {
	driveClient *Client
	s3Client    *s3.Client
	ctx         context.Context
	
	// Performance monitoring
	startTime     time.Time
	totalBytes    int64
	bytesPerSecond float64
	lastUpdate    time.Time
}

// MigrationInput contains parameters for Google Drive to S3 migration
type MigrationInput struct {
	SourceFolderID   string // Google Drive folder ID (empty = root folder)
	DestBucket       string // S3 destination bucket
	DestPrefix       string // S3 destination prefix
	DryRun           bool   // If true, only simulate the migration
	IncludeSharedFiles bool  // If true, include files shared with me (default: false)
	ProgressCallback func(progress float64, copied, total int64, copiedSize, totalSize int64, speed float64, eta string)
}

// MigrationResult contains the result of a migration
type MigrationResult struct {
	TotalFiles    int64 `json:"total_files"`
	CopiedFiles   int64 `json:"copied_files"`
	SkippedFiles  int64 `json:"skipped_files"`
	FailedFiles   int64 `json:"failed_files"`
	TotalSize     int64 `json:"total_size"`
	CopiedSize    int64 `json:"copied_size"`
	StartTime     time.Time `json:"start_time"`
	EndTime       time.Time `json:"end_time"`
	Duration      time.Duration `json:"duration"`
}

// NewGoogleDriveMigrator creates a new Google Drive migrator
func NewGoogleDriveMigrator(ctx context.Context, driveClient *Client, s3Client *s3.Client) *GoogleDriveMigrator {
	return &GoogleDriveMigrator{
		driveClient: driveClient,
		s3Client:    s3Client,
		ctx:         ctx,
	}
}

// Migrate performs the migration from Google Drive to S3
func (m *GoogleDriveMigrator) Migrate(input MigrationInput) (*MigrationResult, error) {
	startTime := time.Now()
	result := &MigrationResult{
		StartTime: startTime,
	}

	fmt.Printf("Starting Google Drive to S3 migration...\n")
	fmt.Printf("Source Folder ID: %s\n", input.SourceFolderID)
	fmt.Printf("Destination: s3://%s/%s\n", input.DestBucket, input.DestPrefix)
	fmt.Printf("Dry Run: %v\n", input.DryRun)

	// Ensure destination bucket exists
	if !input.DryRun {
		if err := m.ensureDestinationBucketExists(input.DestBucket); err != nil {
			return nil, fmt.Errorf("failed to ensure destination bucket exists: %w", err)
		}
	}

	// Process files with streaming approach - optimized for 750 GB/day Google Drive limit
	// Target: 31.25 MB/s sustained (750 GB/day)
	fmt.Printf("⚡ Optimizing for Google Drive 750 GB/day limit (31.25 MB/s target)...\n")
	
	// Initialize performance monitoring
	m.startTime = time.Now()
	m.totalBytes = 0
	m.bytesPerSecond = 0
	m.lastUpdate = time.Now()
	
	// CRITICAL: Single worker mode - even 3 workers exceeded 2Gi limit
	// 50 workers → 25 → 10 → 3 all caused OOM
	// This is the absolute minimum - one file at a time
	numCopyWorkers := 1 // Single worker - absolute minimum
	
	fmt.Printf("📋 Phase 1: Discovering all files (fast discovery without upload throttling)...\n")
	
	// Phase 1: Discover all files first (no uploads yet)
	type FileToUpload struct {
		Info FileInfo
		Path string
	}
	var filesToUpload []FileToUpload
	var discoveryMu sync.Mutex
	totalFiles := int64(0)
	totalSize := int64(0)
	
	err := m.processFilesStreaming(input.SourceFolderID, input.IncludeSharedFiles, func(file FileInfo, filePath string) error {
		// Skip folders
		if file.IsFolder {
			return nil
		}
		
		// Just collect file metadata (no upload yet in Phase 1)
		discoveryMu.Lock()
		filesToUpload = append(filesToUpload, FileToUpload{Info: file, Path: filePath})
		totalFiles++
		totalSize += file.Size
		
		// Log discovery progress every 1000 files and send progress updates
		if totalFiles%1000 == 0 {
			fmt.Printf("🔍 Discovered %d files, total size: %.1f GB\n", 
				totalFiles, float64(totalSize)/(1024*1024*1024))
		}
		
		// Send discovery progress updates every 100 files for better UX
		if totalFiles%100 == 0 && input.ProgressCallback != nil {
			// Calculate discovery progress (assume we're discovering files)
			discoveryProgress := float64(totalFiles) / float64(totalFiles+1000) * 100 // Estimate progress
			eta := "discovering..."
			speed := 0.0 // No upload speed during discovery
			
			input.ProgressCallback(discoveryProgress, totalFiles, totalFiles+1000, totalSize, totalSize+1024*1024*1024, speed, eta)
		}
		
		discoveryMu.Unlock()
		
		return nil
	})
	
	if err != nil {
		return result, fmt.Errorf("failed to process files: %w", err)
	}
	
	// Update result with discovery totals
	result.TotalFiles = totalFiles
	result.TotalSize = totalSize
	
	fmt.Printf("✅ Discovery complete! Found %d files (%.2f GB)\n", totalFiles, float64(totalSize)/(1024*1024*1024))
	fmt.Printf("🚀 Phase 2: Uploading files with %d concurrent workers (maximum throughput)...\n", numCopyWorkers)
	
	// Send discovery completion update
	if input.ProgressCallback != nil {
		input.ProgressCallback(0.0, 0, totalFiles, 0, totalSize, 0.0, "starting upload...")
	}
	
	// Phase 2: Upload all discovered files with maximum throughput
	semaphore := make(chan struct{}, numCopyWorkers)
	var copyWg sync.WaitGroup
	var resultMu sync.Mutex
	
	for fileIndex, fileToUpload := range filesToUpload {
		copyWg.Add(1)
		go func(index int, f FileInfo, path string) {
			defer copyWg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			
			// Update progress
			resultMu.Lock()
			progress := float64(result.CopiedFiles+result.FailedFiles+result.SkippedFiles) / float64(result.TotalFiles) * 100
		eta := m.calculateETA(result.StartTime, int64(result.CopiedFiles+result.FailedFiles+result.SkippedFiles), result.TotalFiles)
		speed := m.calculateSpeed(result.StartTime, result.CopiedSize)
		currentCount := result.CopiedFiles + result.FailedFiles + result.SkippedFiles
		copiedSize := result.CopiedSize
		totalSize := result.TotalSize
		resultMu.Unlock()
		
		if input.ProgressCallback != nil {
			input.ProgressCallback(progress, int64(currentCount), result.TotalFiles, copiedSize, totalSize, speed, eta)
		}

			// Log every 100 files or first 50
			if index%100 == 0 || index <= 50 {
				fmt.Printf("Processing [%d/%d] %s (%.2f MB)\n", 
					index, result.TotalFiles, path, float64(f.Size)/(1024*1024))
			}

			// Generate S3 key with full path and proper extension
			s3Key := m.generateS3KeyWithPath(path, f.MimeType, input.DestPrefix)

			if input.DryRun {
				resultMu.Lock()
				result.CopiedFiles++
				result.CopiedSize += f.Size
				resultMu.Unlock()
				return
			}

			// Copy file to S3
			if err := m.copyFileToS3(f, input.DestBucket, s3Key); err != nil {
				if strings.Contains(err.Error(), "fileNotDownloadable") || 
				   strings.Contains(err.Error(), "Only files with binary content") {
					resultMu.Lock()
					result.SkippedFiles++
					resultMu.Unlock()
				} else {
					if index%100 == 0 || index <= 50 {
						fmt.Printf("  [ERROR] %s: %v\n", f.Name, err)
					}
					resultMu.Lock()
					result.FailedFiles++
					resultMu.Unlock()
				}
				return
			}

			resultMu.Lock()
			result.CopiedFiles++
			result.CopiedSize += f.Size
			if index%100 == 0 || index <= 50 {
				fmt.Printf("  [SUCCESS] %s\n", s3Key)
			}
			resultMu.Unlock()
		}(fileIndex, fileToUpload.Info, fileToUpload.Path)
	}
	
	// Wait for all uploads
	copyWg.Wait()
	
	fmt.Printf("Found %d files total\n", result.TotalFiles)
	fmt.Printf("Total size: %.2f MB\n", float64(result.TotalSize)/(1024*1024))
	
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	// Final progress update
	if input.ProgressCallback != nil {
		input.ProgressCallback(100.0, result.CopiedFiles, result.TotalFiles, 
			result.CopiedSize, result.TotalSize,
			m.calculateSpeed(result.StartTime, result.CopiedSize), "Completed")
	}

	fmt.Printf("\n")
	fmt.Printf("✅ ============================================\n")
	fmt.Printf("✅ MIGRATION COMPLETED!\n")
	fmt.Printf("✅ ============================================\n")
	fmt.Printf("⏱️  Total Time: %v\n", result.Duration)
	fmt.Printf("📊 Total files: %d\n", result.TotalFiles)
	fmt.Printf("✅ Copied files: %d\n", result.CopiedFiles)
	fmt.Printf("❌ Failed files: %d\n", result.FailedFiles)
	fmt.Printf("📦 Total size: %.2f GB\n", float64(result.TotalSize)/(1024*1024*1024))
	fmt.Printf("✅ Copied size: %.2f GB\n", float64(result.CopiedSize)/(1024*1024*1024))
	fmt.Printf("============================================\n\n")
	
	// Performance analysis for Google Drive 750 GB/day limit
	if result.Duration > 0 {
		avgSpeedMBps := float64(result.CopiedSize) / (1024*1024) / result.Duration.Seconds()
		dailyThroughput := avgSpeedMBps * 86400 / (1024 * 1024) // Convert to GB/day
		
		fmt.Printf("🚀 Performance Analysis:\n")
		fmt.Printf("   Average Speed: %.2f MB/s\n", avgSpeedMBps)
		fmt.Printf("   Daily Throughput: %.1f GB/day\n", dailyThroughput)
		
		if dailyThroughput >= 700 {
			fmt.Printf("   ✅ Excellent: Near Google Drive 750 GB/day limit!\n")
		} else if dailyThroughput >= 500 {
			fmt.Printf("   ✅ Good: 67%% of Google Drive limit\n")
		} else if dailyThroughput >= 250 {
			fmt.Printf("   ⚠️  Moderate: 33%% of Google Drive limit\n")
		} else {
			fmt.Printf("   ❌ Low: %.0f%% of Google Drive limit - consider optimizing\n", (dailyThroughput/750)*100)
		}
		
		// Calculate time to complete full migration at this speed
		if result.TotalSize > result.CopiedSize && avgSpeedMBps > 0 {
			remainingMB := float64(result.TotalSize - result.CopiedSize) / (1024 * 1024)
			remainingHours := remainingMB / avgSpeedMBps / 3600
			fmt.Printf("   📊 ETA for remaining %.1f GB: %.1f hours\n", 
				float64(result.TotalSize - result.CopiedSize)/(1024*1024*1024), remainingHours)
		}
	}

	return result, nil
}

// sanitizeMetadataValue cleans metadata values for S3-compatible storage compatibility
func sanitizeMetadataValue(value string) string {
	// Replace problematic characters that might cause S3-compatible storage issues
	// Limit length and remove non-ASCII characters that might cause UnknownError
	if len(value) > 1024 {
		value = value[:1024] // S3 metadata value limit
	}
	
	// Remove or replace characters that might cause issues with MinIO and other S3-compatible services
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\x00", "") // Remove null bytes
	
	// Remove non-printable characters
	var result strings.Builder
	for _, r := range value {
		if r >= 32 && r <= 126 { // Printable ASCII range
			result.WriteRune(r)
		} else {
			result.WriteString("?") // Replace non-printable with safe character
		}
	}
	
	return strings.TrimSpace(result.String())
}

// getAllFilesRecursively gets all files in a folder and its subfolders using concurrent workers
func (m *GoogleDriveMigrator) getAllFilesRecursively(folderID string, includeShared bool) ([]FileInfo, error) {
	fmt.Printf("🔍 Starting to list files from Google Drive...\n")
	if includeShared {
		fmt.Printf("   📂 Mode: Including files SHARED with you\n")
	} else {
		fmt.Printf("   📂 Mode: Only files YOU OWN (excluding shared files)\n")
	}
	startTime := time.Now()

	// Thread-safe data structures
	var (
		allFilesMu    sync.Mutex
		allFiles      []FileInfo
		visitedMu     sync.Mutex
		visited       = make(map[string]bool)
		folderNamesMu sync.Mutex
		folderNames   = make(map[string]string)
		queueMu       sync.Mutex
		queue         []string
		fileCount     int
		folderCount   int
		wg            sync.WaitGroup
	)

	// Initialize queue
	if folderID == "" {
		queue = []string{"root"}
	} else {
		queue = []string{folderID}
	}
	folderNames["root"] = "My Drive (Root)"

	// Smart worker count based on workload (similar to S3 implementation)
	// Start with 1 worker, then dynamically scale up based on queue size
	var workersMu sync.Mutex
	activeWorkers := 0
	maxWorkers := 10 // Google API rate limit friendly
	workerFunc := func(workerID int) {
		defer wg.Done()
		defer func() {
			workersMu.Lock()
			activeWorkers--
			workersMu.Unlock()
		}()
		
		for {
			// Get next folder from queue
			queueMu.Lock()
			if len(queue) == 0 {
				queueMu.Unlock()
				return
			}
			currentFolderID := queue[0]
			queue = queue[1:]
			currentQueueSize := len(queue)
			queueMu.Unlock()

			// Check if already visited
			visitedMu.Lock()
			if visited[currentFolderID] {
				visitedMu.Unlock()
				continue
			}
			visited[currentFolderID] = true
			folderCount++
			currentFolderCount := folderCount
			visitedMu.Unlock()

			// Get folder name
			folderNamesMu.Lock()
			folderName := folderNames[currentFolderID]
			if folderName == "" {
				folderName = currentFolderID[:8] + "..."
			}
			folderNamesMu.Unlock()

			// Log progress (only from worker 0 to avoid spam)
			if workerID == 0 || currentFolderCount%10 == 0 {
				fmt.Printf("📂 [%d/%d] Worker-%d: %s (Queue: %d, Files: %d)\n", 
					currentFolderCount, currentFolderCount+currentQueueSize, workerID, folderName, currentQueueSize, fileCount)
			}

			// List files in current folder with pagination
			pageToken := ""
			pageNum := 0
			for {
				pageNum++
				files, nextPageToken, err := m.driveClient.ListFilesWithTokenAndOptions(currentFolderID, 1000, pageToken, includeShared)
				if err != nil {
					fmt.Printf("⚠️  Worker-%d: Error listing folder %s: %v\n", workerID, folderName, err)
					break
				}

				// Process files
				var newFolders []string
				var newFiles []FileInfo
				for _, file := range files {
					if file.IsFolder {
						newFolders = append(newFolders, file.ID)
					} else {
						newFiles = append(newFiles, file)
					}
				}

				// Update shared data structures
				if len(files) > 0 {
					allFilesMu.Lock()
					allFiles = append(allFiles, files...)
					fileCount += len(newFiles)
					allFilesMu.Unlock()

					queueMu.Lock()
					queue = append(queue, newFolders...)
					queueMu.Unlock()

					folderNamesMu.Lock()
					for _, file := range files {
						if file.IsFolder {
							folderNames[file.ID] = file.Name
						}
					}
					folderNamesMu.Unlock()
				}

				// Check for more pages
				if nextPageToken == "" {
					break
				}
				pageToken = nextPageToken
			}

			// Progress summary every 50 folders (from any worker)
			if currentFolderCount%50 == 0 {
				elapsed := time.Since(startTime)
				rate := float64(currentFolderCount) / elapsed.Seconds()
				queueMu.Lock()
				estimatedTotal := currentFolderCount + len(queue)
				queueSize := len(queue)
				queueMu.Unlock()
				estimatedRemaining := float64(queueSize) / rate
				
				fmt.Printf("\n💡 Progress Summary (Worker-%d):\n", workerID)
				fmt.Printf("   Folders scanned: %d/%d (%.1f%%)\n", currentFolderCount, estimatedTotal, float64(currentFolderCount)/float64(estimatedTotal)*100)
				fmt.Printf("   Files found: %d\n", fileCount)
				fmt.Printf("   Scan rate: %.1f folders/sec\n", rate)
				fmt.Printf("   ETA: %.0f seconds remaining\n\n", estimatedRemaining)
			}
		}
	}

	// Dynamic worker spawning based on queue size (similar to S3 approach)
	// Start with 1 worker, spawn more as queue grows
	fmt.Printf("🚀 Starting with adaptive worker pool (max: %d workers)...\n", maxWorkers)
	
	// Start first worker
	wg.Add(1)
	workersMu.Lock()
	activeWorkers = 1
	workersMu.Unlock()
	go workerFunc(0)
	
	// Monitor queue and spawn additional workers as needed
	go func() {
		workerID := 1
		checkInterval := 100 * time.Millisecond
		
		for {
			time.Sleep(checkInterval)
			
			queueMu.Lock()
			queueSize := len(queue)
			queueMu.Unlock()
			
			// Exit if queue is empty and no workers active
			workersMu.Lock()
			if queueSize == 0 && activeWorkers == 0 {
				workersMu.Unlock()
				return
			}
			
			// Smart worker scaling based on queue size
			var desiredWorkers int
			if queueSize == 0 {
				desiredWorkers = 0
			} else if queueSize < 10 {
				desiredWorkers = 1 // Small queue: 1 worker
			} else if queueSize < 50 {
				desiredWorkers = 3 // Medium queue: 3 workers
			} else if queueSize < 200 {
				desiredWorkers = 5 // Large queue: 5 workers
			} else {
				desiredWorkers = maxWorkers // Very large queue: max workers
			}
			
			// Spawn new workers if needed
			if activeWorkers < desiredWorkers && workerID < maxWorkers {
				newWorkers := desiredWorkers - activeWorkers
				if activeWorkers+newWorkers > maxWorkers {
					newWorkers = maxWorkers - activeWorkers
				}
				
				for i := 0; i < newWorkers; i++ {
					if workerID >= maxWorkers {
						break
					}
					wg.Add(1)
					currentWorkerID := workerID
					activeWorkers++
					fmt.Printf("⚡ Scaling up: Spawned Worker-%d (Queue: %d, Active: %d/%d)\n", 
						currentWorkerID, queueSize, activeWorkers, maxWorkers)
					go workerFunc(currentWorkerID)
					workerID++
				}
			}
			workersMu.Unlock()
		}
	}()

	// Wait for all workers to complete
	wg.Wait()

	elapsed := time.Since(startTime)
	fmt.Printf("✅ File listing completed: %d files, %d folders scanned in %v (%.1f folders/sec)\n", 
		fileCount, folderCount, elapsed.Round(time.Second), float64(folderCount)/elapsed.Seconds())

	return allFiles, nil
}

// generateS3Key generates an S3 key for a Google Drive file
func (m *GoogleDriveMigrator) generateS3Key(file FileInfo, destPrefix string, allFiles []FileInfo) string {
	// Build the full path by traversing parents
	path := file.Name
	currentFile := file

	// Create a map for quick lookup
	fileMap := make(map[string]FileInfo)
	for _, f := range allFiles {
		fileMap[f.ID] = f
	}

	// Build path by following parents
	for len(currentFile.Parents) > 0 && currentFile.Parents[0] != "root" {
		parentID := currentFile.Parents[0]
		if parent, exists := fileMap[parentID]; exists {
			path = parent.Name + "/" + path
			currentFile = parent
		} else {
			break
		}
	}

	// Clean the path
	path = strings.TrimPrefix(path, "/")
	path = strings.ReplaceAll(path, "//", "/")

	// Combine with destination prefix
	if destPrefix != "" {
		path = strings.TrimSuffix(destPrefix, "/") + "/" + path
	}

	return path
}

// generateS3KeyWithExtension generates an S3 key with proper extension for Google Workspace files
func (m *GoogleDriveMigrator) generateS3KeyWithExtension(file FileInfo, destPrefix string, allFiles []FileInfo) string {
	path := m.generateS3Key(file, destPrefix, allFiles)
	
	// Add appropriate extension for Google Workspace files
	switch file.MimeType {
	case "application/vnd.google-apps.document":
		if !strings.HasSuffix(path, ".docx") {
			path += ".docx"
		}
	case "application/vnd.google-apps.spreadsheet":
		if !strings.HasSuffix(path, ".xlsx") {
			path += ".xlsx"
		}
	case "application/vnd.google-apps.presentation":
		if !strings.HasSuffix(path, ".pptx") {
			path += ".pptx"
		}
	case "application/vnd.google-apps.drawing":
		if !strings.HasSuffix(path, ".pdf") {
			path += ".pdf"
		}
	case "application/vnd.google-apps.script":
		if !strings.HasSuffix(path, ".json") {
			path += ".json"
		}
	}
	
	return path
}

// copyFileToS3 downloads a file from Google Drive and uploads it to S3 using streaming
func (m *GoogleDriveMigrator) copyFileToS3(file FileInfo, bucket, key string) error {
	// Special handling for 0-byte files (empty files)
	if file.Size == 0 {
		// Create an empty file directly without downloading
		emptyBody := bytes.NewReader([]byte{})
		
		putInput := &s3.PutObjectInput{
			Bucket: &bucket,
			Key:    &key,
			Body:   emptyBody,
			Metadata: map[string]string{
				"source":         "google-drive",
				"source-file-id": file.ID,
				"original-name":  sanitizeMetadataValue(file.Name),
				"mime-type":      sanitizeMetadataValue(file.MimeType),
				"migrated-at":    time.Now().Format(time.RFC3339),
			},
		}
		
		// For 0-byte files, explicitly do NOT set ContentLength
		// Some S3-compatible storage systems reject ContentLength: 0
		// They expect the header to be omitted entirely for empty files
		
		if file.MimeType != "" {
			putInput.ContentType = &file.MimeType
		}
		
		_, err := m.s3Client.PutObject(m.ctx, putInput)
		if err != nil {
			return fmt.Errorf("failed to upload empty file %s to S3 (bucket: %s, key: %s): %w", 
				file.Name, bucket, key, err)
		}
		
		return nil
	}
	
	// Download from Google Drive (returns io.ReadCloser)
	reader, err := m.driveClient.GetFile(file.ID)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer reader.Close()

	// EMERGENCY: Force streaming mode for ALL files to prevent OOM
	// Even with 5MB buffer and 10 workers, we hit 7GB memory usage
	// Disabling ALL buffering to use pure streaming mode
	var body io.Reader
	var actualSize int64
	
	// Force streaming for ALL files (no buffering at all)
	body = reader
	actualSize = file.Size
	
	// Note: This sacrifices retry capability for memory safety
	// If uploads fail, they'll need to be retried as new migrations

	// Prepare PutObject input with S3-compatible optimizations
	putInput := &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   body,
		Metadata: map[string]string{
			"source":         "google-drive",
			"source-file-id": file.ID,
			"original-name":  sanitizeMetadataValue(file.Name),
			"mime-type":      sanitizeMetadataValue(file.MimeType),
			"migrated-at":    time.Now().Format(time.RFC3339),
		},
	}
	
	// Set ContentLength with actual size (required by some S3 implementations)
	// This fixes 411 MissingContentLength errors
	if actualSize > 0 {
		putInput.ContentLength = &actualSize
	}
	
	// Set content type for better caching and performance
	if file.MimeType != "" {
		putInput.ContentType = &file.MimeType
	}
	
	// Note: StorageClass and ServerSideEncryption removed for S3-compatible storage compatibility
	// These parameters can cause UnknownError 400 with MinIO and other S3-compatible services

	// Upload to S3 with bandwidth monitoring
	uploadStart := time.Now()
	_, err = m.s3Client.PutObject(m.ctx, putInput)
	uploadDuration := time.Since(uploadStart)
	
	if err != nil {
		// Enhanced error reporting for S3-compatible storage debugging
		return fmt.Errorf("failed to upload %s to S3 (bucket: %s, key: %s, size: %d bytes): %w", 
			file.Name, bucket, key, file.Size, err)
	}

	// Update bandwidth monitoring with actual transferred size
	m.totalBytes += actualSize
	if uploadDuration > 0 {
		instantaneousSpeed := float64(actualSize) / uploadDuration.Seconds()
		m.bytesPerSecond = (m.bytesPerSecond + instantaneousSpeed) / 2 // Running average
		
		// Log performance every 100MB transferred
		if m.totalBytes%(100*1024*1024) < actualSize {
			currentSpeed := m.bytesPerSecond / (1024 * 1024) // Convert to MB/s
			
			// EMERGENCY: Log memory usage to debug OOM
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			memUsageMB := float64(memStats.Alloc) / (1024 * 1024)
			
			fmt.Printf("📊 Bandwidth: %.1f MB/s | Memory: %.1f MB | Total: %.1f GB transferred\n", 
				currentSpeed, memUsageMB, float64(m.totalBytes)/(1024*1024*1024))
			
			// Check if we're approaching the 750 GB/day limit
			if currentSpeed > 35.0 { // 35 MB/s = ~3TB/day (safety margin)
				fmt.Printf("⚠️  High bandwidth detected (%.1f MB/s) - approaching Google Drive limits\n", currentSpeed)
			}
			
			// Force garbage collection if memory usage is high
			if memUsageMB > 1000 { // Over 1GB
				runtime.GC()
				debug.FreeOSMemory()
				fmt.Printf("🗑️  Forced garbage collection (memory was %.1f MB)\n", memUsageMB)
			}
		}
	}

	return nil
}

// ensureDestinationBucketExists ensures the S3 bucket exists
func (m *GoogleDriveMigrator) ensureDestinationBucketExists(bucket string) error {
	_, err := m.s3Client.HeadBucket(m.ctx, &s3.HeadBucketInput{
		Bucket: &bucket,
	})
	if err != nil {
		// Try to create the bucket
		_, createErr := m.s3Client.CreateBucket(m.ctx, &s3.CreateBucketInput{
			Bucket: &bucket,
		})
		if createErr != nil {
			return fmt.Errorf("failed to create bucket: %w", createErr)
		}
		fmt.Printf("Created destination bucket: %s\n", bucket)
	}
	return nil
}

// calculateETA calculates estimated time to completion
func (m *GoogleDriveMigrator) calculateETA(startTime time.Time, completed, total int64) string {
	if completed == 0 || total == 0 {
		return "Unknown"
	}

	elapsed := time.Since(startTime)
	rate := float64(completed) / elapsed.Seconds()
	remaining := total - completed
	
	if rate <= 0 {
		return "Unknown"
	}

	etaSeconds := float64(remaining) / rate
	eta := time.Duration(etaSeconds) * time.Second
	
	if eta < time.Minute {
		return fmt.Sprintf("%.0fs", eta.Seconds())
	} else if eta < time.Hour {
		return fmt.Sprintf("%.1fm", eta.Minutes())
	} else {
		return fmt.Sprintf("%.1fh", eta.Hours())
	}
}

// calculateSpeed calculates the current transfer speed
func (m *GoogleDriveMigrator) calculateSpeed(startTime time.Time, bytesTransferred int64) float64 {
	elapsed := time.Since(startTime).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(bytesTransferred) / elapsed / (1024 * 1024) // MB/s
}

// processFilesStreaming processes files without loading all into memory
// Also builds folder paths as we go
func (m *GoogleDriveMigrator) processFilesStreaming(folderID string, includeShared bool, callback func(FileInfo, string) error) error {
	visited := &sync.Map{} // Thread-safe visited map
	folderPaths := &sync.Map{} // Thread-safe folder paths map
	
	// Initialize starting folder
	startFolderID := folderID
	if folderID == "" {
		startFolderID = "root"
		folderPaths.Store("root", "") // Root has empty path
	} else {
		folderPaths.Store(folderID, "")
	}

	// Concurrent folder processing with worker pool
	const maxConcurrentFolders = 2 // Reduced from 10 to minimize memory usage
	folderQueue := make(chan string, 100)
	var wg sync.WaitGroup
	var discoveryErr error
	var errMu sync.Mutex
	var activeWorkers sync.WaitGroup // Track active folder processing
	var foldersProcessed int64 = 0 // Track progress
	
	fmt.Printf("🚀 Starting concurrent folder discovery with %d workers...\n", maxConcurrentFolders)
	
	// Start worker goroutines
	for i := 0; i < maxConcurrentFolders; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			for currentFolderID := range folderQueue {
				// Check if already visited
				if _, loaded := visited.LoadOrStore(currentFolderID, true); loaded {
					activeWorkers.Done() // Mark this folder as done
					continue
				}
				
				// Get current path
				currentPathInterface, _ := folderPaths.Load(currentFolderID)
				currentPath := currentPathInterface.(string)

				// List files with pagination
				pageToken := ""
				for {
					files, nextPageToken, err := m.driveClient.ListFilesWithTokenAndOptions(currentFolderID, 1000, pageToken, includeShared)
					if err != nil {
						errMu.Lock()
						if discoveryErr == nil {
							discoveryErr = fmt.Errorf("failed to list files in folder %s: %w", currentFolderID, err)
						}
						errMu.Unlock()
						activeWorkers.Done()
						return
					}

					for _, file := range files {
						// Build file path
						var filePath string
						if currentPath == "" {
							filePath = file.Name
						} else {
							filePath = currentPath + "/" + file.Name
						}
						
						if file.IsFolder {
							// Store folder path and add to queue
							folderPaths.Store(file.ID, filePath)
							
							// Add folder to queue using goroutine to avoid deadlock
							// Track this new folder and queue it without blocking
							go func(folderID string) {
								activeWorkers.Add(1)
								folderQueue <- folderID
							}(file.ID)
						}
						
						// Process each file immediately (streaming) with its path
						if err := callback(file, filePath); err != nil {
							errMu.Lock()
							if discoveryErr == nil {
								discoveryErr = err
							}
							errMu.Unlock()
							activeWorkers.Done()
							return
						}
					}

					if nextPageToken == "" {
						break
					}
					pageToken = nextPageToken
				}
				
				// Mark this folder as fully processed
				processed := atomic.AddInt64(&foldersProcessed, 1)
				if processed%10 == 0 {
					fmt.Printf("   📁 Scanned %d folders concurrently...\n", processed)
				}
				activeWorkers.Done()
			}
		}(i)
	}
	
	// Seed the queue with the starting folder
	activeWorkers.Add(1)
	folderQueue <- startFolderID
	
	// Wait for all folders to be processed, then close the queue
	go func() {
		activeWorkers.Wait()
		close(folderQueue)
	}()
	
	// Wait for all workers to finish
	wg.Wait()
	
	// Return any error encountered during discovery
	if discoveryErr != nil {
		return discoveryErr
	}

	return nil
}

// generateS3KeyWithPath generates S3 key from full file path
func (m *GoogleDriveMigrator) generateS3KeyWithPath(filePath, mimeType, destPrefix string) string {
	// Add extension for Google Workspace files
	path := filePath
	switch mimeType {
	case "application/vnd.google-apps.document":
		if !strings.HasSuffix(path, ".docx") {
			path += ".docx"
		}
	case "application/vnd.google-apps.spreadsheet":
		if !strings.HasSuffix(path, ".xlsx") {
			path += ".xlsx"
		}
	case "application/vnd.google-apps.presentation":
		if !strings.HasSuffix(path, ".pptx") {
			path += ".pptx"
		}
	case "application/vnd.google-apps.drawing":
		if !strings.HasSuffix(path, ".pdf") {
			path += ".pdf"
		}
	case "application/vnd.google-apps.script":
		if !strings.HasSuffix(path, ".json") {
			path += ".json"
		}
	}
	
	// Combine with destination prefix
	if destPrefix != "" {
		return strings.TrimSuffix(destPrefix, "/") + "/" + path
	}
	return path
}
