package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"strings"
)

// BulkMigrator handles migration of all buckets in an account
type BulkMigrator struct {
	sourceEnhanced *EnhancedMigrator
	destEnhanced   *EnhancedMigrator
}

// NewBulkMigrator creates a new bulk migrator with enhanced migrators
func NewBulkMigrator(ctx context.Context, sourceRegion, sourceEndpoint, destRegion, destEndpoint string) (*BulkMigrator, error) {
	// Try to create enhanced migrators first
	sourceCfg := EnhancedMigratorConfig{
		Region:             sourceRegion,
		EndpointURL:        sourceEndpoint,
		ConnectionPoolSize: 20,
		EnableStreaming:    true,
		EnablePrefetch:     true,
		StreamChunkSize:    100 * 1024 * 1024, // 100MB chunks
		CacheTTL:           5 * time.Minute,
		CacheSize:          1000,
	}
	
	destCfg := EnhancedMigratorConfig{
		Region:             destRegion,
		EndpointURL:        destEndpoint,
		ConnectionPoolSize: 20,
		EnableStreaming:    true,
		EnablePrefetch:     true,
		StreamChunkSize:    100 * 1024 * 1024, // 100MB chunks
		CacheTTL:           5 * time.Minute,
		CacheSize:          1000,
	}

	sourceEnhanced, err := NewEnhancedMigrator(ctx, sourceCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create source enhanced migrator: %w", err)
	}

	destEnhanced, err := NewEnhancedMigrator(ctx, destCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create destination enhanced migrator: %w", err)
	}

	return &BulkMigrator{
		sourceEnhanced: sourceEnhanced,
		destEnhanced:   destEnhanced,
	}, nil
}

// BulkMigrateInput contains parameters for bulk migration
type BulkMigrateInput struct {
	ExcludeBuckets []string      // Buckets to skip
	IncludeBuckets []string      // Only migrate these buckets (if specified)
	DryRun         bool          // Simulate without copying
	Timeout        time.Duration // Timeout per object
	Concurrent     int           // Number of buckets to migrate concurrently
}

// BulkMigrateResult contains results from bulk migration
type BulkMigrateResult struct {
	BucketResults    map[string]*MigrateResult
	TotalBuckets     int64
	SuccessBuckets   int64
	FailedBuckets    int64
	TotalObjects     int64
	TotalSizeMB      float64
	ElapsedTime      string
	Errors           []string
}

// MigrateAllBuckets migrates all buckets from source to destination
func (bm *BulkMigrator) MigrateAllBuckets(ctx context.Context, input BulkMigrateInput) (*BulkMigrateResult, error) {
	startTime := time.Now()

	fmt.Println("\n=== Starting Bulk S3 Account Migration ===")
	
	// List all source buckets
	sourceClient := bm.sourceEnhanced.GetClient()
	
	sourceBuckets, err := bm.listBuckets(ctx, sourceClient)
	if err != nil {
		return nil, fmt.Errorf("failed to list source buckets: %w", err)
	}

	// Filter buckets
	bucketsToMigrate := bm.filterBuckets(sourceBuckets, input.IncludeBuckets, input.ExcludeBuckets)

	if len(bucketsToMigrate) == 0 {
		fmt.Println("No buckets to migrate")
		return &BulkMigrateResult{
			BucketResults: make(map[string]*MigrateResult),
		}, nil
	}

	fmt.Printf("Found %d buckets to migrate\n", len(bucketsToMigrate))
	for _, bucket := range bucketsToMigrate {
		fmt.Printf("  - %s\n", bucket)
	}

	if input.DryRun {
		fmt.Println("\n⚠️  DRY RUN MODE - No data will be copied")
	}

	// Initialize result
	result := &BulkMigrateResult{
		BucketResults: make(map[string]*MigrateResult),
		TotalBuckets:  int64(len(bucketsToMigrate)),
		Errors:        make([]string, 0),
	}

	// Set concurrency limit
	concurrent := input.Concurrent
	if concurrent == 0 {
		concurrent = 3 // Default: 3 buckets at a time
	}

	// Migrate buckets concurrently
	semaphore := make(chan struct{}, concurrent)
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	var totalObjects, successBuckets, failedBuckets atomic.Int64
	var totalSize atomic.Int64

	for _, bucketName := range bucketsToMigrate {
		wg.Add(1)
		go func(bucket string) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			fmt.Printf("\n📦 Starting migration of bucket: %s\n", bucket)

			// Ensure destination bucket exists (create if needed)
			if !input.DryRun {
				if err := bm.ensureBucketExists(ctx, bucket); err != nil {
					fmt.Printf("❌ Failed to create destination bucket %s: %v\n", bucket, err)
					failedBuckets.Add(1)
					resultMu.Lock()
					result.Errors = append(result.Errors, fmt.Sprintf("Bucket %s: %v", bucket, err))
					resultMu.Unlock()
					return
				}
			}

			// Migrate bucket contents
			migrateInput := MigrateInput{
				SourceBucket: bucket,
				DestBucket:   bucket, // Same bucket name in destination
				SourcePrefix: "",
				DestPrefix:   "",
				DryRun:       input.DryRun,
				Timeout:      input.Timeout,
			}

			bucketResult, err := bm.sourceEnhanced.Migrate(ctx, migrateInput)

			resultMu.Lock()
			if err != nil {
				fmt.Printf("❌ Failed to migrate bucket %s: %v\n", bucket, err)
				result.Errors = append(result.Errors, fmt.Sprintf("Bucket %s: %v", bucket, err))
				result.BucketResults[bucket] = &MigrateResult{
					Errors: []string{err.Error()},
				}
				failedBuckets.Add(1)
			} else {
				fmt.Printf("✅ Completed migration of bucket: %s (%d objects, %.1f MB)\n", 
					bucket, bucketResult.Copied, bucketResult.CopiedSizeMB)
				result.BucketResults[bucket] = bucketResult
				totalObjects.Add(bucketResult.Copied)
				totalSize.Add(int64(bucketResult.CopiedSizeMB * 1024 * 1024))
				
				if bucketResult.Failed == 0 {
					successBuckets.Add(1)
				} else {
					failedBuckets.Add(1)
				}
			}
			resultMu.Unlock()
		}(bucketName)
	}

	// Wait for all buckets to complete
	wg.Wait()

	// Finalize results
	result.SuccessBuckets = successBuckets.Load()
	result.FailedBuckets = failedBuckets.Load()
	result.TotalObjects = totalObjects.Load()
	result.TotalSizeMB = float64(totalSize.Load()) / (1024 * 1024)
	result.ElapsedTime = time.Since(startTime).String()

	// Print summary
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("BULK MIGRATION SUMMARY")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Total Buckets: %d\n", result.TotalBuckets)
	fmt.Printf("Successful: %d\n", result.SuccessBuckets)
	fmt.Printf("Failed: %d\n", result.FailedBuckets)
	fmt.Printf("Total Objects: %d\n", result.TotalObjects)
	fmt.Printf("Total Size: %.1f MB\n", result.TotalSizeMB)
	fmt.Printf("Total Time: %s\n", result.ElapsedTime)
	fmt.Println(strings.Repeat("=", 60))

	return result, nil
}

func (bm *BulkMigrator) listBuckets(ctx context.Context, client *s3.Client) ([]string, error) {
	resp, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, err
	}

	buckets := make([]string, 0, len(resp.Buckets))
	for _, bucket := range resp.Buckets {
		buckets = append(buckets, *bucket.Name)
	}

	return buckets, nil
}

func (bm *BulkMigrator) filterBuckets(allBuckets, includeBuckets, excludeBuckets []string) []string {
	// If include list specified, only use those
	if len(includeBuckets) > 0 {
		includeMap := make(map[string]bool)
		for _, b := range includeBuckets {
			includeMap[b] = true
		}

		filtered := make([]string, 0)
		for _, bucket := range allBuckets {
			if includeMap[bucket] {
				filtered = append(filtered, bucket)
			}
		}
		return filtered
	}

	// Otherwise, exclude specified buckets
	excludeMap := make(map[string]bool)
	for _, b := range excludeBuckets {
		excludeMap[b] = true
	}

	filtered := make([]string, 0, len(allBuckets))
	for _, bucket := range allBuckets {
		if !excludeMap[bucket] {
			filtered = append(filtered, bucket)
		}
	}

	return filtered
}

func (bm *BulkMigrator) ensureBucketExists(ctx context.Context, bucketName string) error {
	// Check if bucket exists in destination
	destClient := bm.destEnhanced.GetClient()
	_, err := destClient.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})

	if err == nil {
		// Bucket already exists
		return nil
	}

	// Create bucket in destination
	_, err = destClient.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	})

	if err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}

	fmt.Printf("📝 Created destination bucket: %s\n", bucketName)
	return nil
}

// Stop stops the bulk migration
func (bm *BulkMigrator) Stop() {
	bm.sourceEnhanced.Stop()
	bm.destEnhanced.Stop()
}

