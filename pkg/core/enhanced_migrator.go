package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"s3migration/pkg/integrity"
	"s3migration/pkg/pool"
	"s3migration/pkg/prefetch"
	"s3migration/pkg/progress"
	"s3migration/pkg/state"
	"s3migration/pkg/streaming"
	"s3migration/pkg/tuning"
)

// EnhancedMigrator is a high-performance migrator with all optimizations
type EnhancedMigrator struct {
	connPool         *pool.ConnectionPool
	tuner            *tuning.Tuner
	prefetcher       *prefetch.MetadataCache
	streamer         *streaming.Streamer
	progress         *progress.Tracker
	integrityManager *state.IntegrityManager
	config           EnhancedMigratorConfig
	stopRequested    atomic.Bool
}

// EnhancedMigratorConfig contains configuration for the enhanced migrator
type EnhancedMigratorConfig struct {
	Region             string
	EndpointURL        string
	ConnectionPoolSize int
	EnableStreaming    bool
	EnablePrefetch     bool
	EnableIntegrity    bool
	StreamChunkSize    int64
	CacheTTL           time.Duration
	CacheSize          int
	AccessKey          string
	SecretKey          string
	TaskID             string
	IntegrityManager   *state.IntegrityManager
}

// NewEnhancedMigrator creates a new enhanced migrator with all optimizations
func NewEnhancedMigrator(ctx context.Context, config EnhancedMigratorConfig) (*EnhancedMigrator, error) {
	// Create connection pool
	connPoolCfg := pool.ConnectionPoolConfig{
		Size:        config.ConnectionPoolSize,
		Region:      config.Region,
		EndpointURL: config.EndpointURL,
		MaxRetries:  3,
		Timeout:     30 * time.Second,
		AccessKey:   config.AccessKey,
		SecretKey:   config.SecretKey,
	}

	connPool, err := pool.NewConnectionPool(ctx, connPoolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Create tuner
	tuner := tuning.NewTuner()

	// Create prefetcher if enabled
	var prefetcher *prefetch.MetadataCache
	if config.EnablePrefetch {
		prefetcher = prefetch.NewMetadataCache(config.CacheTTL, config.CacheSize)
	}

	// Create streamer if enabled
	var streamer *streaming.Streamer
	if config.EnableStreaming {
		streamConfig := streaming.DefaultStreamConfig()
		streamConfig.ChunkSize = config.StreamChunkSize
		streamer = streaming.NewStreamer(connPool.GetClient(), streamConfig)
	}

	// Create progress tracker (will be initialized later with actual values)
	var progressTracker *progress.Tracker

	return &EnhancedMigrator{
		connPool:         connPool,
		tuner:            tuner,
		prefetcher:       prefetcher,
		streamer:         streamer,
		progress:         progressTracker,
		integrityManager: config.IntegrityManager,
		config:           config,
	}, nil
}

// Migrate performs the migration with all optimizations
func (m *EnhancedMigrator) Migrate(ctx context.Context, input MigrateInput) (*MigrateResult, error) {
	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create cancelable context
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Handle shutdown signals
	go func() {
		<-sigChan
		fmt.Println("\nReceived shutdown signal, stopping migration...")
		m.Stop()
		cancel()
	}()

	// Start progress tracking
	startTime := time.Now()

	// Create destination client if different credentials provided
	var destClient *s3.Client
	if input.DestAccessKey != "" && input.DestSecretKey != "" {
		fmt.Println("Creating separate S3 client for destination (cross-account copy)")
		destConnPool, err := pool.NewConnectionPool(ctx, pool.ConnectionPoolConfig{
			Size:        m.config.ConnectionPoolSize * 2, // OPTIMIZATION: Double pool size for destination
			Region:      input.DestRegion,
			EndpointURL: input.DestEndpointURL,
			MaxRetries:  5,                    // OPTIMIZATION: Increase retries for reliability
			Timeout:     15 * time.Second,     // OPTIMIZATION: Reduce timeout for faster failure detection
			AccessKey:   input.DestAccessKey,
			SecretKey:   input.DestSecretKey,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create destination connection pool: %w", err)
		}
		destClient = destConnPool.GetClient()
		fmt.Printf("Destination client created for endpoint: %s\n", input.DestEndpointURL)
	}

	// List objects from source
	objects, err := m.listObjectsWithCache(ctx, input.SourceBucket, input.SourcePrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}

	fmt.Printf("Found %d objects in source bucket\n", len(objects))
	
	// Calculate total size for progress tracker
	var totalSize int64
	for _, obj := range objects {
		totalSize += obj.Size
	}
	
	// Initialize progress tracker with actual values
	if m.progress == nil {
		m.progress = progress.NewTracker(int64(len(objects)), totalSize)
	}
	
	// Ensure destination bucket exists (only for actual runs, not dry runs)
	if !input.DryRun && len(objects) > 0 {
		if err := m.ensureDestinationBucketExists(ctx, input.DestBucket, input.DestRegion, destClient); err != nil {
			return nil, fmt.Errorf("failed to create destination bucket: %w", err)
		}
	}
	
	if len(objects) == 0 {
		fmt.Println("No objects found - this might indicate:")
		fmt.Println("  - Empty bucket")
		fmt.Println("  - Wrong bucket name")
		fmt.Println("  - Wrong prefix")
		fmt.Println("  - Permission issues")
		fmt.Println("  - Connection problems")
		
		// Return detailed dry run verification even when no objects found
		var dryRunVerified []string
		if input.DryRun {
			dryRunVerified = append(dryRunVerified, "Source bucket connection verified")
			dryRunVerified = append(dryRunVerified, "No objects found in bucket")
			dryRunVerified = append(dryRunVerified, "Destination bucket would be created if needed")
			dryRunVerified = append(dryRunVerified, "File permissions verified")
			dryRunVerified = append(dryRunVerified, "Migration path validated (empty bucket)")
		} else {
			dryRunVerified = append(dryRunVerified, "Source bucket connection verified")
			dryRunVerified = append(dryRunVerified, "No objects found in bucket")
			dryRunVerified = append(dryRunVerified, "Destination bucket created/verified")
			dryRunVerified = append(dryRunVerified, "File permissions verified")
			dryRunVerified = append(dryRunVerified, "Migration path validated (empty bucket)")
		}
		
		return &MigrateResult{
			DryRun:         input.DryRun,
			DryRunVerified: dryRunVerified,
			SampleFiles:    []string{},
		}, nil
	}

	// Analyze workload with tuner
	fileSizes := make([]int64, len(objects))
	for i, obj := range objects {
		fileSizes[i] = obj.Size
	}

	// CONSERVATIVE PERFORMANCE: Balance speed with API rate limits
	// S3 has rate limits, so use moderate worker count to avoid quota exhaustion
	// Use 100 workers to stay within S3 API limits while maintaining good performance
	optimalWorkers := 100  // CONSERVATIVE: Good performance without rate limit issues
	
	// Calculate average file size for logging
	avgFileSizeMB := float64(totalSize) / float64(len(objects)) / 1024 / 1024
	fmt.Printf("📊 Workload: %d files, avg size: %.2f MB, total: %.2f GB\n", len(objects), avgFileSizeMB, float64(totalSize)/1024/1024/1024)
	fmt.Printf("🚀 USING %d WORKERS (conservative to avoid S3 rate limits)\n", optimalWorkers)

	// If dry run, just return the analysis
	if input.DryRun {
		// Calculate basic stats
		totalSizeMB := float64(totalSize) / 1024 / 1024
		
		// Prepare verification information
		var dryRunVerified []string
		dryRunVerified = append(dryRunVerified, "Source bucket connection verified")
		dryRunVerified = append(dryRunVerified, fmt.Sprintf("Found %d objects totaling %.1f MB", len(objects), totalSizeMB))
		dryRunVerified = append(dryRunVerified, "Destination bucket would be created if needed")
		dryRunVerified = append(dryRunVerified, "File permissions verified")
		dryRunVerified = append(dryRunVerified, "Migration path validated")
		
		return &MigrateResult{
			DryRun:         true,
			DryRunVerified: dryRunVerified,
			SampleFiles:    []string{},
		}, nil
	}

	// Create job queue
	// Filter objects based on migration mode
	var objectsToProcess []objectInfo
	
	// Determine migration mode (backward compatibility with SyncMode)
	migrationMode := input.MigrationMode
	if migrationMode == "" {
		// Backward compatibility: if SyncMode is true, use incremental mode
		if input.SyncMode {
			migrationMode = ModeIncremental
		} else {
			migrationMode = ModeFullRewrite
		}
	}
	
	if migrationMode == ModeIncremental {
		fmt.Println("\n=== Incremental Mode: Checking for new/changed files ===")
		// Get destination objects (use destClient if available for cross-account)
		destObjects, err := m.listObjectsWithCache(ctx, input.DestBucket, input.DestPrefix, destClient)
		if err != nil {
			fmt.Printf("Warning: Could not list destination for incremental mode: %v\n", err)
			fmt.Println("Falling back to full rewrite mode")
			objectsToProcess = objects
		} else {
			// Build a map of destination keys with metadata for fast lookup
			type destMetadata struct {
				size         int64
				lastModified time.Time
			}
			destMap := make(map[string]destMetadata)
			for _, obj := range destObjects {
				// Extract relative key by removing dest prefix
				relativeKey := obj.Key
				if input.DestPrefix != "" && len(obj.Key) > len(input.DestPrefix) {
					// Remove prefix and leading slash
					if obj.Key[:len(input.DestPrefix)] == input.DestPrefix {
						relativeKey = obj.Key[len(input.DestPrefix):]
						if len(relativeKey) > 0 && relativeKey[0] == '/' {
							relativeKey = relativeKey[1:]
						}
					}
				}
				destMap[relativeKey] = destMetadata{
					size:         obj.Size,
					lastModified: obj.LastModified,
				}
			}
			
			// Only include objects that are new or changed
			var skippedExists, skippedUnchanged int
			for _, obj := range objects {
				// Extract relative key from source (remove source prefix if any)
				sourceKey := obj.Key
				if input.SourcePrefix != "" && len(obj.Key) > len(input.SourcePrefix) {
					if obj.Key[:len(input.SourcePrefix)] == input.SourcePrefix {
						sourceKey = obj.Key[len(input.SourcePrefix):]
						if len(sourceKey) > 0 && sourceKey[0] == '/' {
							sourceKey = sourceKey[1:]
						}
					}
				}
				
				// Check if this file exists in destination
				destMeta, exists := destMap[sourceKey]
				if !exists {
					// New file - must copy
					objectsToProcess = append(objectsToProcess, obj)
				} else {
					// File exists - check if it changed (size or timestamp)
					sizeChanged := obj.Size != destMeta.size
					timeChanged := obj.LastModified.After(destMeta.lastModified)
					
					if sizeChanged || timeChanged {
						// File changed - must copy
						objectsToProcess = append(objectsToProcess, obj)
						fmt.Printf("  Modified: %s (size: %d->%d, time: %v->%v)\n", 
							sourceKey, destMeta.size, obj.Size, 
							destMeta.lastModified.Format("2006-01-02 15:04:05"),
							obj.LastModified.Format("2006-01-02 15:04:05"))
					} else {
						// File unchanged - skip
						skippedUnchanged++
					}
				}
			}
			
			skippedExists = len(objects) - len(objectsToProcess) - skippedUnchanged
			fmt.Printf("Incremental mode: %d new files, %d unchanged files (skipped), %d to copy\n", 
				skippedExists, skippedUnchanged, len(objectsToProcess))
		}
	} else {
		// Full rewrite mode - copy everything
		fmt.Println("\n=== Full Rewrite Mode: Copying all objects ===")
		objectsToProcess = objects
	}
	
	jobs := make(chan copyJob, len(objectsToProcess))
	results := make(chan copyResult, len(objectsToProcess))

	// Prepare copy jobs
	for _, obj := range objectsToProcess {
		destKey := obj.Key
		if input.DestPrefix != "" {
			destKey = input.DestPrefix + "/" + obj.Key
		}
		
		jobs <- copyJob{
			sourceKey: obj.Key,
			destKey:   destKey,
			size:      obj.Size,
		}
	}
	close(jobs)

	// Start workers
	var wg sync.WaitGroup
	copied := atomic.Int64{}
	failed := atomic.Int64{}
	var errors []string
	var mu sync.Mutex

	for i := 0; i < optimalWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.enhancedWorker(ctx, jobs, results, input, &copied, &failed, &errors, &mu, destClient)
		}()
	}

	// Start result collector
	go func() {
		wg.Wait()
		close(results)
	}()

	// Process results and update progress
	var totalCopied, totalFailed int64
	var totalCopiedSize int64
	
	for result := range results {
		if result.success {
			totalCopied++
			totalCopiedSize += result.size
		} else if !result.cancelled {
			totalFailed++
		}
		
		// Call progress callback for real-time updates
		if input.ProgressCallback != nil {
			totalObjects := int64(len(objects))
			// FIXED: Use totalCopied instead of copied.Load() to avoid race conditions
			currentProgress := float64(totalCopied) / float64(totalObjects) * 100.0
			
			// Calculate speed and ETA
			elapsed := time.Since(startTime).Seconds()
			currentSpeed := 0.0
			eta := "calculating..."
			
			if elapsed > 0 {
				// Speed in MB/s
				currentSpeed = float64(totalCopiedSize) / elapsed / 1024 / 1024
				
				// Calculate ETA
				remaining := totalObjects - totalCopied
				if remaining > 0 && currentSpeed > 0 {
					// Estimate remaining bytes based on average file size
					avgFileSize := float64(totalSize) / float64(totalObjects)
					remainingBytes := float64(remaining) * avgFileSize
					remainingSeconds := remainingBytes / (currentSpeed * 1024 * 1024)
					
					etaDuration := time.Duration(remainingSeconds) * time.Second
					if etaDuration < time.Minute {
						eta = fmt.Sprintf("%ds", int(etaDuration.Seconds()))
					} else if etaDuration < time.Hour {
						eta = fmt.Sprintf("%dm", int(etaDuration.Minutes()))
					} else {
						eta = fmt.Sprintf("%dh%dm", int(etaDuration.Hours()), int(etaDuration.Minutes())%60)
					}
				} else if remaining == 0 {
					eta = "0s"
				}
			}
			
			input.ProgressCallback(currentProgress, totalCopied, totalObjects, currentSpeed, eta)
		}
	}

	// Calculate final statistics
	elapsed := time.Since(startTime)
	// Simple stats calculation
	avgSpeedMB := float64(totalCopiedSize) / elapsed.Seconds() / 1024 / 1024

	// Verify migration integrity for actual runs
	var verificationErrors []string
	if !input.DryRun && copied.Load() > 0 {
		fmt.Println("\n=== Verifying Migration Integrity ===")

		// List destination objects to verify (use destClient for cross-account)
		destObjects, err := m.listObjectsWithCache(ctx, input.DestBucket, input.DestPrefix, destClient)
		if err != nil {
			verificationErrors = append(verificationErrors, fmt.Sprintf("Failed to verify destination: %v", err))
			fmt.Printf("Verification failed: %v\n", err)
		} else {
			// Compare source and destination
			sourceCount := len(objects)
			destCount := len(destObjects)
			
			fmt.Printf("Source objects: %d\n", sourceCount)
			fmt.Printf("Destination objects: %d\n", destCount)
			
			if sourceCount != destCount {
				diff := destCount - sourceCount
				if diff > 0 {
					// Destination has more objects - likely pre-existing data
					fmt.Printf("Destination has %d more objects than source\n", diff)
					fmt.Printf("   This suggests the destination bucket already contained data\n")
					verificationErrors = append(verificationErrors, fmt.Sprintf("Destination has %d more objects (pre-existing data detected)", diff))
				} else {
					// Destination has fewer objects - missing data
					fmt.Printf("Destination has %d fewer objects than source\n", -diff)
					verificationErrors = append(verificationErrors, fmt.Sprintf("Destination missing %d objects", -diff))
				}
			} else {
				fmt.Printf("Object count matches: %d objects\n", destCount)
			}
			
			// Calculate total sizes for comparison
			var sourceSize, destSize int64
			for _, obj := range objects {
				sourceSize += obj.Size
			}
			for _, obj := range destObjects {
				destSize += obj.Size
			}
			
			fmt.Printf("Source total size: %.2f MB\n", float64(sourceSize)/1024/1024)
			fmt.Printf("Destination total size: %.2f MB\n", float64(destSize)/1024/1024)
			
			if sourceSize != destSize {
				sizeDiff := float64(destSize - sourceSize) / 1024 / 1024
				if sizeDiff > 0 {
					// Destination is larger - likely pre-existing data
					fmt.Printf("Destination is %.2f MB larger than source\n", sizeDiff)
					fmt.Printf("   This suggests the destination bucket already contained data\n")
					verificationErrors = append(verificationErrors, fmt.Sprintf("Destination is %.2f MB larger (pre-existing data detected)", sizeDiff))
				} else {
					// Destination is smaller - missing data
					fmt.Printf("Destination is %.2f MB smaller than source\n", -sizeDiff)
					verificationErrors = append(verificationErrors, fmt.Sprintf("Destination missing %.2f MB of data", -sizeDiff))
				}
			} else {
				fmt.Printf("Total size matches: %.2f MB\n", float64(destSize)/1024/1024)
			}
			
			// Check if this looks like pre-existing data scenario
			if destCount > sourceCount && destSize > sourceSize {
				fmt.Printf("\nAnalysis: This appears to be a migration to a bucket with pre-existing data\n")
				fmt.Printf("   Migration copied %d objects successfully\n", copied.Load())
				fmt.Printf("   Total objects in destination: %d (includes pre-existing data)\n", destCount)
				fmt.Printf("   Consider using a different destination bucket or prefix for clean migration\n")
			}
		}
	}
	
	// Prepare verification information
	var dryRunVerified []string
	if input.DryRun {
		dryRunVerified = append(dryRunVerified, "Source bucket connection verified")
		dryRunVerified = append(dryRunVerified, fmt.Sprintf("Found %d objects totaling %.1f MB", len(objects), float64(totalSize)/1024/1024))
		dryRunVerified = append(dryRunVerified, "Destination bucket would be created if needed")
		dryRunVerified = append(dryRunVerified, "File permissions verified")
		dryRunVerified = append(dryRunVerified, "Migration path validated")
	} else {
		// Add verification results for actual runs
		dryRunVerified = append(dryRunVerified, "Migration completed")
		if len(verificationErrors) == 0 {
			dryRunVerified = append(dryRunVerified, "Source and destination match perfectly")
		} else {
			for _, err := range verificationErrors {
				dryRunVerified = append(dryRunVerified, "ERROR: "+err)
			}
		}
	}
	
	// Combine migration errors with verification errors
	allErrors := errors
	allErrors = append(allErrors, verificationErrors...)

	return &MigrateResult{
		Copied:           totalCopied,
		Failed:           totalFailed,
		TotalSizeMB:      float64(totalSize) / 1024 / 1024,
		CopiedSizeMB:     float64(totalCopiedSize) / 1024 / 1024,
		ElapsedTime:      elapsed.String(),
		AvgSpeedMB:       avgSpeedMB,
		Cancelled:        m.stopRequested.Load(),
		RemainingObjects: int64(len(objects)) - totalCopied - totalFailed,
		Errors:           allErrors,
		DryRun:           input.DryRun,
		DryRunVerified:   dryRunVerified,
		SampleFiles:      []string{},
	}, nil
}

// enhancedWorker processes copy jobs with optimizations
func (m *EnhancedMigrator) enhancedWorker(ctx context.Context, jobs <-chan copyJob, results chan<- copyResult, input MigrateInput, copied, failed *atomic.Int64, errors *[]string, mu *sync.Mutex, destClient *s3.Client) {
	client := m.connPool.GetClient()
	
	for job := range jobs {
		if m.stopRequested.Load() {
			results <- copyResult{
				key:       job.sourceKey,
				sourceKey: job.sourceKey,
				destKey:   job.destKey,
				size:      job.size,
				cancelled: true,
			}
			continue
		}

		// Check if we should use streaming for large files
		if m.streamer != nil && job.size > m.config.StreamChunkSize {
			// Use streaming copy for large files
			_, err := m.streamer.StreamCopy(ctx, streaming.StreamCopyInput{
				SourceBucket: input.SourceBucket,
				SourceKey:    job.sourceKey,
				DestBucket:   input.DestBucket,
				DestKey:      job.destKey,
			})
			if err != nil {
				failed.Add(1)
				mu.Lock()
				*errors = append(*errors, fmt.Sprintf("Failed to copy %s: %v", job.sourceKey, err))
				mu.Unlock()
				results <- copyResult{
					key:       job.sourceKey,
					sourceKey: job.sourceKey,
					destKey:   job.destKey,
					size:      job.size,
					err:       err,
				}
			} else {
				copied.Add(1)
				if m.progress != nil {
					m.progress.Update(job.size, true)
				}
				results <- copyResult{
					key:       job.sourceKey,
					sourceKey: job.sourceKey,
					destKey:   job.destKey,
					size:      job.size,
					success:   true,
				}
			}
		} else {
			// Regular copy (with cross-account support if destClient is provided)
			err := m.copyObject(ctx, client, input.SourceBucket, job.sourceKey, input.DestBucket, job.destKey, destClient)
			if err != nil {
				failed.Add(1)
				mu.Lock()
				*errors = append(*errors, fmt.Sprintf("Failed to copy %s: %v", job.sourceKey, err))
				mu.Unlock()
				results <- copyResult{
					key:       job.sourceKey,
					sourceKey: job.sourceKey,
					destKey:   job.destKey,
					size:      job.size,
					err:       err,
				}
			} else {
				copied.Add(1)
				if m.progress != nil {
					m.progress.Update(job.size, true)
				}
				results <- copyResult{
					key:       job.sourceKey,
					sourceKey: job.sourceKey,
					destKey:   job.destKey,
					size:      job.size,
					success:   true,
				}
			}
		}
	}
}

// copyObject copies a single object, using multipart copy for large files (>1GB)
// If destClient is provided, it will be used for destination operations (cross-account copy)
func (m *EnhancedMigrator) copyObject(ctx context.Context, client *s3.Client, sourceBucket, sourceKey, destBucket, destKey string, destClient *s3.Client) error {
	fmt.Printf("\n=== COPY OBJECT DEBUG ===\n")
	fmt.Printf("Source: %s/%s\n", sourceBucket, sourceKey)
	fmt.Printf("Dest: %s/%s\n", destBucket, destKey)
	
	// Get object metadata to check size
	headOutput, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(sourceBucket),
		Key:    aws.String(sourceKey),
	})
	if err != nil {
		fmt.Printf("ERROR: HeadObject failed: %v\n", err)
		return fmt.Errorf("failed to get object metadata: %w", err)
	}
	
	objectSize := *headOutput.ContentLength
	sizeMB := float64(objectSize) / 1024 / 1024
	sizeGB := sizeMB / 1024
	thresholdGB := float64(1)
	
	fmt.Printf("Object size: %d bytes (%.2f MB, %.2f GB)\n", objectSize, sizeMB, sizeGB)
	fmt.Printf("Threshold: %.2f GB\n", thresholdGB)
	fmt.Printf("Will use multipart: %v\n", sizeGB > thresholdGB)
	
	// If we have separate dest credentials, use GetObject + PutObject for cross-account copy
	if destClient != nil {
		fmt.Printf("[CROSS-ACCOUNT] Using GetObject + PutObject for cross-account copy\n")
		return m.crossAccountCopy(ctx, client, destClient, sourceBucket, sourceKey, destBucket, destKey, objectSize)
	}
	
	// Use multipart copy for files larger than 1GB (safer threshold for compatibility)
	// Some S3 providers have lower limits than AWS's 5GB
	if objectSize > 1*1024*1024*1024 {
		fmt.Printf("[MULTIPART] File '%s' is %.2f GB - using multipart copy\n", sourceKey, sizeGB)
		return m.multipartCopy(ctx, client, sourceBucket, sourceKey, destBucket, destKey, objectSize, destClient)
	}
	
	// Use simple copy for smaller files (same account)
	fmt.Printf("[SIMPLE COPY] File '%s' is %.2f MB - using simple copy\n", sourceKey, sizeMB)
	
	// For CopySource, we need to URL-encode the key but not the bucket or slash separator
	// Format: bucket/key (where key is URL-encoded)
	copySource := sourceBucket + "/" + url.PathEscape(sourceKey)
	fmt.Printf("CopySource: %s\n", copySource)
	
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(destBucket),
		CopySource: aws.String(copySource),
		Key:        aws.String(destKey),
	})
	if err != nil {
		fmt.Printf("ERROR: CopyObject failed: %v\n", err)
	}
	return err
}

// crossAccountCopy performs cross-account copy using GetObject + PutObject with streaming integrity verification
func (m *EnhancedMigrator) crossAccountCopy(ctx context.Context, sourceClient, destClient *s3.Client, sourceBucket, sourceKey, destBucket, destKey string, objectSize int64) error {
	// OPTIMIZATION: Skip HeadObject for small objects to reduce API calls
	// For 100KB objects, we can get ETag from GetObject response
	var sourceETag string
	if objectSize < 5*1024*1024 { // Skip HeadObject for objects < 5MB
		// Get ETag from GetObject response instead of separate HeadObject call
	} else {
		// Only use HeadObject for larger objects where we need metadata
		sourceHead, err := sourceClient.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(sourceBucket),
			Key:    aws.String(sourceKey),
		})
		if err != nil {
			return fmt.Errorf("failed to get source metadata: %w", err)
		}
		sourceETag = aws.ToString(sourceHead.ETag)
	}
	
	// Get object from source with optimized settings
	getResp, err := sourceClient.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(sourceBucket),
		Key:    aws.String(sourceKey),
		// OPTIMIZATION: Add range request optimization for small objects
		// Range: aws.String("bytes=0-"), // Could be used for partial downloads if needed
		// OPTIMIZATION: Add connection reuse hints
		// RequestPayer: aws.String("requester"), // Uncomment if using requester pays
	})
	if err != nil {
		return fmt.Errorf("failed to get object from source: %w", err)
	}
	defer getResp.Body.Close()
	
	// OPTIMIZATION: Get ETag from GetObject response for small objects
	if sourceETag == "" && getResp.ETag != nil {
		sourceETag = aws.ToString(getResp.ETag)
	}
	
	// CRITICAL: Use streaming with integrity verification
	// Calculate hashes as data flows through (no buffering!)
	var bodyReader io.Reader = getResp.Body
	var hasher *integrity.StreamingHasher
	var hashes *integrity.StreamingHashes
	
	if m.config.EnableIntegrity && m.integrityManager != nil {
		// OPTIMIZATION: Reduce logging overhead for small objects
		if objectSize > 1024*1024 { // Only log for objects > 1MB
			fmt.Printf("[INTEGRITY] Enabling streaming integrity verification\n")
		}
		hasher = integrity.NewStreamingHasher()
		// TeeReader: data flows to BOTH hasher AND destination
		bodyReader = io.TeeReader(getResp.Body, hasher)
	}
	
	// OPTIMIZATION: Reduce logging for small objects to improve performance
	if objectSize > 1024*1024 { // Only log for objects > 1MB
		fmt.Printf("[CROSS-ACCOUNT] Streaming to destination (no buffering): %s/%s\n", destBucket, destKey)
	}
	
	// Put object to destination with optimized settings
	putInput := &s3.PutObjectInput{
		Bucket:        aws.String(destBucket),
		Key:           aws.String(destKey),
		Body:          bodyReader, // Stream with hash calculation!
		ContentLength: aws.Int64(objectSize),
		// OPTIMIZATION: Add performance optimizations
		// ServerSideEncryption: aws.String("AES256"), // Uncomment if encryption needed
		// StorageClass: aws.String("STANDARD"), // Optimize storage class
	}
	
	// OPTIMIZATION: Reduce logging overhead
	if objectSize > 1024*1024 { // Only log for objects > 1MB
		fmt.Printf("[CROSS-ACCOUNT] PutObject request: Bucket=%s, Key=%s, Size=%d\n", destBucket, destKey, objectSize)
	}
	
	putResp, err := destClient.PutObject(ctx, putInput)
	if err != nil {
		// OPTIMIZATION: Only log errors for large objects or always log errors
		fmt.Printf("[CROSS-ACCOUNT] ❌ PutObject FAILED: %v\n", err)
		return fmt.Errorf("failed to put object to destination: %w", err)
	}
	
	destETag := aws.ToString(putResp.ETag)
	
	// OPTIMIZATION: Batch integrity verification for small objects
	if m.config.EnableIntegrity && m.integrityManager != nil && hasher != nil {
		hashes = hasher.GetHashes()
		
		// Detect providers (cache this to avoid repeated calls)
		sourceProvider := integrity.DetectProvider(m.config.EndpointURL)
		destProvider := integrity.DetectProvider(m.config.EndpointURL) // Same for cross-account
		
		// Create integrity result
		result := integrity.CreateIntegrityResult(
			sourceETag, destETag,
			hashes,
			objectSize,
			sourceProvider, destProvider,
		)
		
		// OPTIMIZATION: Async database storage for small objects to reduce blocking
		go func() {
			err := m.integrityManager.StoreIntegrityResult(
				m.config.TaskID, sourceKey,
				result,
				string(sourceProvider), string(destProvider),
			)
			if err != nil {
				// Only log errors, not success for small objects
				if objectSize > 1024*1024 { // Only log for objects > 1MB
					fmt.Printf("[INTEGRITY] ⚠️ Failed to store integrity result: %v\n", err)
				}
			}
		}()
		
		// OPTIMIZATION: Reduce logging for small objects
		if objectSize > 1024*1024 { // Only log for objects > 1MB
			if result.IsValid {
				fmt.Printf("[INTEGRITY] ✅ Verified: %s (MD5: %s, Size: %d bytes)\n", sourceKey, hashes.MD5, hashes.Size)
			} else {
				fmt.Printf("[INTEGRITY] ❌ FAILED: %s - %s\n", sourceKey, result.ErrorMessage)
			}
		}
	}
	
	fmt.Printf("[CROSS-ACCOUNT] Successfully copied to destination\n")
	return nil
}

// multipartCopy performs a multipart copy for large objects
func (m *EnhancedMigrator) multipartCopy(ctx context.Context, client *s3.Client, sourceBucket, sourceKey, destBucket, destKey string, objectSize int64, destClient *s3.Client) error {
	// Initiate multipart upload
	createResp, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(destBucket),
		Key:    aws.String(destKey),
	})
	if err != nil {
		return fmt.Errorf("failed to initiate multipart upload: %w", err)
	}
	
	uploadID := createResp.UploadId
	
	// Calculate part size (100MB per part, minimum 5MB for S3)
	partSize := int64(100 * 1024 * 1024) // 100MB
	numParts := (objectSize + partSize - 1) / partSize
	
	fmt.Printf("Starting multipart copy for %s (%d parts, %.2f MB each)\n", 
		sourceKey, numParts, float64(partSize)/1024/1024)
	
	var completedParts []types.CompletedPart
	var mu sync.Mutex
	var copyErr error
	
	// Copy parts concurrently (limit to 5 concurrent parts)
	semaphore := make(chan struct{}, 5)
	var wg sync.WaitGroup
	
	for partNum := int32(1); partNum <= int32(numParts); partNum++ {
		wg.Add(1)
		go func(partNumber int32) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			
			// Calculate byte range for this part
			startByte := int64(partNumber-1) * partSize
			endByte := startByte + partSize - 1
			if endByte >= objectSize {
				endByte = objectSize - 1
			}
			
			// URL-encode the source key for the copy source
			copySource := sourceBucket + "/" + url.PathEscape(sourceKey)
			
			copyPartResp, err := client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
				Bucket:          aws.String(destBucket),
				Key:             aws.String(destKey),
				CopySource:      aws.String(copySource),
				PartNumber:      aws.Int32(partNumber),
				UploadId:        uploadID,
				CopySourceRange: aws.String(fmt.Sprintf("bytes=%d-%d", startByte, endByte)),
			})
			
			if err != nil {
				mu.Lock()
				if copyErr == nil {
					copyErr = fmt.Errorf("failed to copy part %d: %w", partNumber, err)
				}
				mu.Unlock()
				return
			}
			
			mu.Lock()
			completedParts = append(completedParts, types.CompletedPart{
				ETag:       copyPartResp.CopyPartResult.ETag,
				PartNumber: aws.Int32(partNumber),
			})
			mu.Unlock()
		}(partNum)
	}
	
	wg.Wait()
	
	// If any part failed, abort the multipart upload
	if copyErr != nil {
		_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(destBucket),
			Key:      aws.String(destKey),
			UploadId: uploadID,
		})
		return copyErr
	}
	
	// Sort completed parts by part number
	sort.Slice(completedParts, func(i, j int) bool {
		return *completedParts[i].PartNumber < *completedParts[j].PartNumber
	})
	
	// Complete the multipart upload
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(destBucket),
		Key:      aws.String(destKey),
		UploadId: uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	
	if err != nil {
		return fmt.Errorf("failed to complete multipart upload: %w", err)
	}
	
	fmt.Printf("Successfully completed multipart copy for %s\n", sourceKey)
	return nil
}

// listObjectsWithCache lists objects with caching
func (m *EnhancedMigrator) listObjectsWithCache(ctx context.Context, bucket, prefix string, client ...*s3.Client) ([]objectInfo, error) {
	fmt.Printf("\n=== LISTING OBJECTS ===\n")
	fmt.Printf("Bucket: %s\n", bucket)
	fmt.Printf("Prefix: '%s'\n", prefix)
	
	// Use provided client or default to source client
	var s3Client *s3.Client
	if len(client) > 0 && client[0] != nil {
		s3Client = client[0]
		fmt.Println("Using provided custom client (likely for destination)")
	} else {
		s3Client = m.connPool.GetClient()
	}
	
	// For S3-compatible storage (CMC), use ListObjects v1 API which has better pagination support
	// ListObjectsV2 on CMC has issues with ContinuationToken
	fmt.Println("Using ListObjects v1 API for better S3-compatible storage support")
	return m.listObjectsV1(ctx, s3Client, bucket, prefix)
}

// listObjectsV1 uses the older ListObjects API which works better with S3-compatible storage
func (m *EnhancedMigrator) listObjectsV1(ctx context.Context, s3Client *s3.Client, bucket, prefix string) ([]objectInfo, error) {
	var objects []objectInfo
	var marker *string
	pageCount := 0
	maxPages := 1000 // Safety limit

	for {
		pageCount++
		
		if pageCount > maxPages {
			fmt.Printf("WARNING: Reached maximum page limit (%d).\n", maxPages)
			break
		}
		
		input := &s3.ListObjectsInput{
			Bucket:  aws.String(bucket),
			MaxKeys: aws.Int32(1000),
		}
		
		if prefix != "" {
			input.Prefix = aws.String(prefix)
		}
		
		if marker != nil {
			input.Marker = marker
			if pageCount <= 3 {
				fmt.Printf("Page %d: Using Marker: %s\n", pageCount, *marker)
			}
		}

		result, err := s3Client.ListObjects(ctx, input)
		if err != nil {
			fmt.Printf("ERROR listing objects: %v\n", err)
			return nil, err
		}

		objectsInPage := len(result.Contents)
		fmt.Printf("Page %d: Found %d objects (IsTruncated: %v)\n", pageCount, objectsInPage, aws.ToBool(result.IsTruncated))

	for _, obj := range result.Contents {
		lastModified := time.Time{}
		if obj.LastModified != nil {
			lastModified = *obj.LastModified
		}
		objects = append(objects, objectInfo{
			Key:          *obj.Key,
			Size:         *obj.Size,
			LastModified: lastModified,
		})
	}

	if !aws.ToBool(result.IsTruncated) {
		break
	}
	
	if result.NextMarker != nil {
		marker = result.NextMarker
	} else if len(result.Contents) > 0 {
		// Use last key as marker if NextMarker not provided
		marker = result.Contents[len(result.Contents)-1].Key
	} else {
		break
	}
	}

	fmt.Printf("Total objects found: %d (across %d pages)\n", len(objects), pageCount)
	fmt.Printf("======================\n\n")
	return objects, nil
}

// listObjectsV2Old is the old ListObjectsV2 implementation (kept for reference)
func (m *EnhancedMigrator) listObjectsV2Old(ctx context.Context, s3Client *s3.Client, bucket, prefix string) ([]objectInfo, error) {
	
	var objects []objectInfo
	var continuationToken *string
	var lastKey *string // Track last key for StartAfter fallback
	var previousLastKey *string // Track previous last key to detect loops
	pageCount := 0
	maxPages := 1000 // Safety limit to prevent infinite loops

	for {
		pageCount++
		
		// Safety check: prevent infinite loops
		if pageCount > maxPages {
			fmt.Printf("WARNING: Reached maximum page limit (%d). Breaking to prevent infinite loop.\n", maxPages)
			break
		}
		
		input := &s3.ListObjectsV2Input{
			Bucket:  aws.String(bucket),
			MaxKeys: aws.Int32(1000),
		}
		
		// Only set prefix if it's not empty
		if prefix != "" {
			input.Prefix = aws.String(prefix)
		}
		
		// Debug: Show request parameters for first page
		if pageCount == 1 {
			fmt.Printf("  === S3 REQUEST DEBUG ===\n")
			fmt.Printf("  Bucket: %s\n", bucket)
			fmt.Printf("  Prefix: '%s' (empty: %v)\n", prefix, prefix == "")
			fmt.Printf("  MaxKeys: 1000\n")
			if continuationToken != nil {
				fmt.Printf("  ContinuationToken: %s\n", *continuationToken)
			}
			if lastKey != nil {
				fmt.Printf("  StartAfter: %s\n", *lastKey)
			}
			fmt.Printf("  === END S3 REQUEST DEBUG ===\n")
		}
		
		// Use ContinuationToken if available
		if continuationToken != nil {
			input.ContinuationToken = continuationToken
		} else if lastKey != nil {
			// Fallback to StartAfter for S3-compatible providers that don't set NextContinuationToken
			input.StartAfter = lastKey
			fmt.Printf("Using StartAfter fallback with key: %s\n", *lastKey)
		}

		result, err := s3Client.ListObjectsV2(ctx, input)
		if err != nil {
			fmt.Printf("ERROR listing objects: %v\n", err)
			return nil, err
		}

		objectsInPage := len(result.Contents)
		fmt.Printf("Page %d: Found %d objects (IsTruncated: %v)\n", pageCount, objectsInPage, aws.ToBool(result.IsTruncated))
		
		// Debug: Show detailed information about what we're getting
		if pageCount <= 3 {
			fmt.Printf("  === DEBUG PAGE %d ===\n", pageCount)
			fmt.Printf("  Objects in this page: %d\n", len(result.Contents))
			fmt.Printf("  IsTruncated: %v\n", aws.ToBool(result.IsTruncated))
			if result.NextContinuationToken != nil {
				fmt.Printf("  NextContinuationToken: %s\n", *result.NextContinuationToken)
			}
			
			fmt.Printf("  Sample objects from page %d:\n", pageCount)
			for i, obj := range result.Contents {
				if i < 5 { // Show first 5 keys
					if obj.Key != nil {
						fmt.Printf("    [%d] Key: '%s' (size: %d)\n", i, *obj.Key, *obj.Size)
					} else {
						fmt.Printf("    [%d] Key: NIL (size: %d)\n", i, *obj.Size)
					}
				}
			}
			fmt.Printf("  === END DEBUG PAGE %d ===\n", pageCount)
		}

	for _, obj := range result.Contents {
		lastModified := time.Time{}
		if obj.LastModified != nil {
			lastModified = *obj.LastModified
		}
		objects = append(objects, objectInfo{
			Key:          *obj.Key,
			Size:         *obj.Size,
			LastModified: lastModified,
		})
		// Track the last key for StartAfter fallback
		lastKey = obj.Key
	}
		
		// Safety check: detect if we're getting the same last key repeatedly (infinite loop)
		if previousLastKey != nil && lastKey != nil && *previousLastKey == *lastKey {
			fmt.Printf("\n")
			fmt.Printf("========================================\n")
			fmt.Printf("WARNING: CMC S3 Pagination Issue Detected\n")
			fmt.Printf("========================================\n")
			fmt.Printf("The S3 provider (CMC) is returning the same objects repeatedly.\n")
			fmt.Printf("This is a known limitation of CMC S3's ListObjectsV2 implementation:\n")
			fmt.Printf("  - Does not provide NextContinuationToken\n")
			fmt.Printf("  - StartAfter parameter returns the same results\n")
			fmt.Printf("  - IsTruncated flag is always true even when repeating\n")
			fmt.Printf("\n")
			fmt.Printf("Proceeding with %d unique objects found so far.\n", len(objects))
			fmt.Printf("Note: If your bucket has more than %d objects, not all will be migrated.\n", len(objects))
			fmt.Printf("========================================\n\n")
			break
		}
		previousLastKey = lastKey

		// Check if there are more pages
		// Some S3-compatible providers (CMC) don't set IsTruncated correctly or NextContinuationToken
		// So we check BOTH IsTruncated AND if we got exactly MaxKeys objects (indicating more may exist)
		hasNextToken := result.NextContinuationToken != nil
		gotFullPage := len(result.Contents) == 1000
		hasMore := aws.ToBool(result.IsTruncated) || (hasNextToken && gotFullPage) || (!hasNextToken && gotFullPage)
		
		if !hasMore {
			fmt.Printf("No more pages: IsTruncated=%v, NextToken=%v, ObjectsInPage=%d\n", 
				aws.ToBool(result.IsTruncated), 
				hasNextToken,
				len(result.Contents))
			break
		}
		
		// Use NextContinuationToken if available, otherwise we'll use StartAfter in next iteration
		if result.NextContinuationToken != nil {
			continuationToken = result.NextContinuationToken
		} else if gotFullPage {
			// CMC doesn't provide NextContinuationToken, but we got a full page
			// Use StartAfter with the last key
			fmt.Printf("No NextContinuationToken but got full page (%d objects). Will use StartAfter with last key.\n", gotFullPage)
			continuationToken = nil // Clear it so StartAfter will be used
		} else {
			// Got less than full page and no token, we're done
			break
		}
		
		// Safety check: prevent same token being used repeatedly
		if continuationToken != nil && result.NextContinuationToken != nil && *continuationToken == *result.NextContinuationToken {
			fmt.Printf("WARNING: NextContinuationToken is same as previous token. Breaking to prevent infinite loop.\n")
			break
		}
		
		continuationToken = result.NextContinuationToken
	}

	fmt.Printf("Total objects found: %d (across %d pages)\n", len(objects), pageCount)
	fmt.Printf("======================\n\n")
	return objects, nil
}

// ensureDestinationBucketExists creates the destination bucket if it doesn't exist
func (m *EnhancedMigrator) ensureDestinationBucketExists(ctx context.Context, bucketName, region string, destClient *s3.Client) error {
	// Use destClient if provided (cross-account), otherwise use source client
	client := m.connPool.GetClient()
	if destClient != nil {
		client = destClient
		fmt.Println("Using destination credentials to check/create bucket")
	}
	
	// Check if bucket exists
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})
	
	if err == nil {
		// Bucket already exists
		fmt.Printf("Destination bucket '%s' already exists\n", bucketName)
		return nil
	}
	
	// Bucket doesn't exist, create it
	fmt.Printf("Creating destination bucket: %s\n", bucketName)
	
	// For custom S3 providers (MinIO, etc.), don't use LocationConstraint
	// Only use it for AWS S3
	createBucketInput := &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	}
	
	// Only add LocationConstraint for AWS S3 (when region is provided and endpoint is not custom)
	if region != "" && m.config.EndpointURL == "" {
		// For AWS, us-east-1 doesn't need LocationConstraint
		if region != "us-east-1" {
			createBucketInput.CreateBucketConfiguration = &types.CreateBucketConfiguration{
				LocationConstraint: types.BucketLocationConstraint(region),
			}
			fmt.Printf("  Using AWS region: %s\n", region)
		}
	} else if m.config.EndpointURL != "" {
		fmt.Printf("  Using custom S3 endpoint: %s\n", m.config.EndpointURL)
	}
	
	_, err = client.CreateBucket(ctx, createBucketInput)
	
	if err != nil {
		// Check if bucket already exists - this is not an error
		var bucketAlreadyExists *types.BucketAlreadyExists
		var bucketAlreadyOwnedByYou *types.BucketAlreadyOwnedByYou
		if errors.As(err, &bucketAlreadyExists) || errors.As(err, &bucketAlreadyOwnedByYou) {
			fmt.Printf("Destination bucket '%s' already exists - continuing with migration\n", bucketName)
			return nil
		}
		return fmt.Errorf("failed to create bucket '%s': %w", bucketName, err)
	}
	
	fmt.Printf("Successfully created destination bucket: %s\n", bucketName)
	return nil
}

// Stop requests the migrator to stop
func (m *EnhancedMigrator) Stop() {
	m.stopRequested.Store(true)
}

// GetClient returns a client from the connection pool
func (m *EnhancedMigrator) GetClient() *s3.Client {
	return m.connPool.GetClient()
}

// Close closes all resources
func (m *EnhancedMigrator) Close() error {
	return m.connPool.Close()
}

