package sync

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ConflictStrategy defines how to handle file conflicts
type ConflictStrategy string

const (
	ConflictNewest ConflictStrategy = "newest" // Keep newest file
	ConflictSource ConflictStrategy = "source" // Always use source
	ConflictDest   ConflictStrategy = "dest"   // Keep destination
	ConflictSkip   ConflictStrategy = "skip"   // Skip conflicting files
	ConflictRename ConflictStrategy = "rename" // Rename conflicting files
)

// SyncOptions defines sync behavior
type SyncOptions struct {
	Incremental      bool             // Only sync changed files
	DeleteRemoved    bool             // Delete files removed from source
	Overwrite        bool             // Overwrite existing files
	SyncMetadata     bool             // Sync object metadata
	ConflictStrategy ConflictStrategy // How to handle conflicts
	Filters          []string         // File patterns to include/exclude
	MaxConcurrent    int              // Max concurrent transfers
}

// FileState represents the state of a synced file
type FileState struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
	LastSynced   time.Time
	Checksum     string
}

// SyncState manages the state of synchronized files
type SyncState struct {
	mu        sync.RWMutex
	files     map[string]*FileState
	lastSync  time.Time
	syncCount int
	stateFile string
}

// NewSyncState creates a new sync state manager
func NewSyncState(stateFile string) *SyncState {
	return &SyncState{
		files:     make(map[string]*FileState),
		stateFile: stateFile,
	}
}

// RecordFile records the state of a synced file
func (ss *SyncState) RecordFile(state *FileState) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	state.LastSynced = time.Now()
	ss.files[state.Key] = state
	ss.syncCount++
}

// GetFile retrieves the state of a file
func (ss *SyncState) GetFile(key string) (*FileState, bool) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	state, exists := ss.files[key]
	return state, exists
}

// HasChanged determines if a file has changed since last sync
func (ss *SyncState) HasChanged(key string, size int64, lastModified time.Time, etag string) bool {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	state, exists := ss.files[key]
	if !exists {
		return true // New file
	}

	// Check if any attributes changed
	if state.Size != size {
		return true
	}

	if state.ETag != etag {
		return true
	}

	if state.LastModified.Before(lastModified) {
		return true
	}

	return false
}

// GetChangedFiles returns files that need to be synced
func (ss *SyncState) GetChangedFiles() []*FileState {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	changed := make([]*FileState, 0)
	for _, state := range ss.files {
		changed = append(changed, state)
	}

	return changed
}

// Clear clears all state
func (ss *SyncState) Clear() {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ss.files = make(map[string]*FileState)
	ss.syncCount = 0
}

// Stats returns sync state statistics
type SyncStateStats struct {
	TotalFiles    int
	LastSync      time.Time
	TotalSyncs    int
	StateFileSize int64
}

func (ss *SyncState) Stats() SyncStateStats {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	return SyncStateStats{
		TotalFiles: len(ss.files),
		LastSync:   ss.lastSync,
		TotalSyncs: ss.syncCount,
	}
}

// IncrementalSyncer handles incremental synchronization
type IncrementalSyncer struct {
	sourceClient *s3.Client
	destClient   *s3.Client
	state        *SyncState
	options      SyncOptions
}

// NewIncrementalSyncer creates a new incremental syncer
func NewIncrementalSyncer(sourceClient, destClient *s3.Client, stateFile string, options SyncOptions) *IncrementalSyncer {
	return &IncrementalSyncer{
		sourceClient: sourceClient,
		destClient:   destClient,
		state:        NewSyncState(stateFile),
		options:      options,
	}
}

// SyncInput holds input parameters for sync operation
type SyncInput struct {
	SourceBucket string
	SourcePrefix string
	DestBucket   string
	DestPrefix   string
}

// SyncResult holds the result of a sync operation
type SyncResult struct {
	NewFiles       int64
	UpdatedFiles   int64
	DeletedFiles   int64
	SkippedFiles   int64
	UnchangedFiles int64
	TotalBytes     int64
	Duration       time.Duration
	Errors         []string
}

// Sync performs incremental synchronization
func (is *IncrementalSyncer) Sync(ctx context.Context, input SyncInput) (*SyncResult, error) {
	startTime := time.Now()
	result := &SyncResult{
		Errors: make([]string, 0),
	}

	// Get source objects
	sourceObjects, err := is.listObjects(ctx, is.sourceClient, input.SourceBucket, input.SourcePrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list source objects: %w", err)
	}

	// Get destination objects
	destObjects, err := is.listObjects(ctx, is.destClient, input.DestBucket, input.DestPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list destination objects: %w", err)
	}

	// Build destination map for quick lookup
	destMap := make(map[string]*ObjectInfo)
	for _, obj := range destObjects {
		destMap[obj.Key] = obj
	}

	// Process source objects
	for _, sourceObj := range sourceObjects {
		// Calculate relative key
		relativeKey := sourceObj.Key
		if input.SourcePrefix != "" {
			relativeKey = relativeKey[len(input.SourcePrefix):]
		}

		destKey := relativeKey
		if input.DestPrefix != "" {
			destKey = input.DestPrefix + relativeKey
		}

		// Check if file needs sync
		destObj, existsInDest := destMap[destKey]

		if !existsInDest {
			// New file
			if err := is.copyFile(ctx, input, sourceObj.Key, destKey); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("Failed to copy %s: %v", sourceObj.Key, err))
				continue
			}
			result.NewFiles++
			result.TotalBytes += sourceObj.Size
		} else {
			// File exists - check if changed
			if is.state.HasChanged(sourceObj.Key, sourceObj.Size, sourceObj.LastModified, sourceObj.ETag) {
				// Handle conflict
				if is.shouldSync(sourceObj, destObj) {
					if err := is.copyFile(ctx, input, sourceObj.Key, destKey); err != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("Failed to update %s: %v", sourceObj.Key, err))
						continue
					}
					result.UpdatedFiles++
					result.TotalBytes += sourceObj.Size
				} else {
					result.SkippedFiles++
				}
			} else {
				result.UnchangedFiles++
			}
		}

		// Record file state
		is.state.RecordFile(&FileState{
			Key:          sourceObj.Key,
			Size:         sourceObj.Size,
			ETag:         sourceObj.ETag,
			LastModified: sourceObj.LastModified,
		})
	}

	// Handle deletions
	if is.options.DeleteRemoved {
		sourceMap := make(map[string]bool)
		for _, obj := range sourceObjects {
			sourceMap[obj.Key] = true
		}

		for _, destObj := range destObjects {
			if !sourceMap[destObj.Key] {
				if err := is.deleteFile(ctx, input.DestBucket, destObj.Key); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("Failed to delete %s: %v", destObj.Key, err))
					continue
				}
				result.DeletedFiles++
			}
		}
	}

	result.Duration = time.Since(startTime)
	is.state.lastSync = time.Now()

	return result, nil
}

func (is *IncrementalSyncer) shouldSync(source, dest *ObjectInfo) bool {
	if is.options.Overwrite {
		return true
	}

	switch is.options.ConflictStrategy {
	case ConflictSource:
		return true
	case ConflictDest:
		return false
	case ConflictNewest:
		return source.LastModified.After(dest.LastModified)
	case ConflictSkip:
		return false
	default:
		return true
	}
}

func (is *IncrementalSyncer) copyFile(ctx context.Context, input SyncInput, sourceKey, destKey string) error {
	copySource := fmt.Sprintf("%s/%s", input.SourceBucket, sourceKey)

	_, err := is.destClient.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(input.DestBucket),
		Key:        aws.String(destKey),
		CopySource: aws.String(copySource),
	})

	return err
}

func (is *IncrementalSyncer) deleteFile(ctx context.Context, bucket, key string) error {
	_, err := is.destClient.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	return err
}

// ObjectInfo holds S3 object information
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

func (is *IncrementalSyncer) listObjects(ctx context.Context, client *s3.Client, bucket, prefix string) ([]*ObjectInfo, error) {
	var objects []*ObjectInfo

	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}

		for _, obj := range page.Contents {
			objects = append(objects, &ObjectInfo{
				Key:          *obj.Key,
				Size:         *obj.Size,
				ETag:         *obj.ETag,
				LastModified: *obj.LastModified,
			})
		}
	}

	return objects, nil
}

// GetSyncState returns the current sync state
func (is *IncrementalSyncer) GetSyncState() *SyncState {
	return is.state
}

// computeChecksum computes MD5 checksum for a file
func computeChecksum(data []byte) string {
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:])
}
