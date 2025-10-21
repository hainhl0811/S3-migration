package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// StateStore handles persistent storage of sync state
type StateStore struct {
	mu       sync.RWMutex
	filePath string
	autoSave bool
	saveChan chan struct{}
	stopChan chan struct{}
}

// NewStateStore creates a new state store
func NewStateStore(filePath string, autoSave bool) *StateStore {
	store := &StateStore{
		filePath: filePath,
		autoSave: autoSave,
		saveChan: make(chan struct{}, 1),
		stopChan: make(chan struct{}),
	}

	if autoSave {
		go store.autoSaveWorker()
	}

	return store
}

func (store *StateStore) autoSaveWorker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Periodic save
		case <-store.saveChan:
			// Triggered save
		case <-store.stopChan:
			return
		}
	}
}

// SaveState saves sync state to disk
func (store *StateStore) SaveState(state *SyncState) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	// Create directory if it doesn't exist
	dir := filepath.Dir(store.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Prepare state for serialization
	stateData := struct {
		LastSync  time.Time             `json:"last_sync"`
		SyncCount int                   `json:"sync_count"`
		Files     map[string]*FileState `json:"files"`
	}{
		LastSync:  state.lastSync,
		SyncCount: state.syncCount,
		Files:     state.files,
	}

	// Write to temporary file first
	tempFile := store.filePath + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create state file: %w", err)
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(stateData); err != nil {
		file.Close()
		os.Remove(tempFile)
		return fmt.Errorf("failed to encode state: %w", err)
	}

	file.Close()

	// Atomic rename
	if err := os.Rename(tempFile, store.filePath); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to save state file: %w", err)
	}

	return nil
}

// LoadState loads sync state from disk
func (store *StateStore) LoadState() (*SyncState, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	file, err := os.Open(store.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// No state file yet, return new state
			return NewSyncState(store.filePath), nil
		}
		return nil, fmt.Errorf("failed to open state file: %w", err)
	}
	defer file.Close()

	var stateData struct {
		LastSync  time.Time             `json:"last_sync"`
		SyncCount int                   `json:"sync_count"`
		Files     map[string]*FileState `json:"files"`
	}

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&stateData); err != nil {
		return nil, fmt.Errorf("failed to decode state: %w", err)
	}

	state := NewSyncState(store.filePath)
	state.lastSync = stateData.LastSync
	state.syncCount = stateData.SyncCount
	state.files = stateData.Files

	return state, nil
}

// TriggerSave triggers an async save (if auto-save enabled)
func (store *StateStore) TriggerSave() {
	if store.autoSave {
		select {
		case store.saveChan <- struct{}{}:
		default:
			// Save already pending
		}
	}
}

// Stop stops the auto-save worker
func (store *StateStore) Stop() {
	close(store.stopChan)
}

// DeltaCalculator calculates differences between source and destination
type DeltaCalculator struct {
	sourceClient *s3.Client
	destClient   *s3.Client
}

// NewDeltaCalculator creates a new delta calculator
func NewDeltaCalculator(sourceClient, destClient *s3.Client) *DeltaCalculator {
	return &DeltaCalculator{
		sourceClient: sourceClient,
		destClient:   destClient,
	}
}

// Delta represents changes between source and destination
type Delta struct {
	NewFiles     []*ObjectInfo
	UpdatedFiles []*ObjectInfo
	DeletedFiles []*ObjectInfo
	Unchanged    []*ObjectInfo
}

// CalculateDelta determines what needs to be synced
func (dc *DeltaCalculator) CalculateDelta(ctx context.Context, input SyncInput, state *SyncState) (*Delta, error) {
	delta := &Delta{
		NewFiles:     make([]*ObjectInfo, 0),
		UpdatedFiles: make([]*ObjectInfo, 0),
		DeletedFiles: make([]*ObjectInfo, 0),
		Unchanged:    make([]*ObjectInfo, 0),
	}

	// List source objects
	sourceObjects, err := dc.listObjects(ctx, dc.sourceClient, input.SourceBucket, input.SourcePrefix)
	if err != nil {
		return nil, err
	}

	// List destination objects
	destObjects, err := dc.listObjects(ctx, dc.destClient, input.DestBucket, input.DestPrefix)
	if err != nil {
		return nil, err
	}

	// Build maps for comparison
	sourceMap := make(map[string]*ObjectInfo)
	destMap := make(map[string]*ObjectInfo)

	for _, obj := range sourceObjects {
		sourceMap[obj.Key] = obj
	}

	for _, obj := range destObjects {
		destMap[obj.Key] = obj
	}

	// Find new and updated files
	for key, sourceObj := range sourceMap {
		_, existsInDest := destMap[key]

		if !existsInDest {
			delta.NewFiles = append(delta.NewFiles, sourceObj)
		} else {
			// Check if changed
			if state.HasChanged(key, sourceObj.Size, sourceObj.LastModified, sourceObj.ETag) {
				delta.UpdatedFiles = append(delta.UpdatedFiles, sourceObj)
			} else {
				delta.Unchanged = append(delta.Unchanged, sourceObj)
			}
		}
	}

	// Find deleted files
	for key := range destMap {
		if _, existsInSource := sourceMap[key]; !existsInSource {
			delta.DeletedFiles = append(delta.DeletedFiles, destMap[key])
		}
	}

	return delta, nil
}

func (dc *DeltaCalculator) listObjects(ctx context.Context, client *s3.Client, bucket, prefix string) ([]*ObjectInfo, error) {
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

// SyncStateSummary provides a summary of sync state
type SyncStateSummary struct {
	TotalFiles      int
	TotalSize       int64
	LastSyncTime    time.Time
	OldestFile      time.Time
	NewestFile      time.Time
	AverageFileSize int64
}

// GetSummary returns a summary of the sync state
func (ss *SyncState) GetSummary() SyncStateSummary {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	summary := SyncStateSummary{
		TotalFiles:   len(ss.files),
		LastSyncTime: ss.lastSync,
	}

	var totalSize int64
	var oldestTime, newestTime time.Time

	for _, state := range ss.files {
		totalSize += state.Size

		if oldestTime.IsZero() || state.LastModified.Before(oldestTime) {
			oldestTime = state.LastModified
		}

		if newestTime.IsZero() || state.LastModified.After(newestTime) {
			newestTime = state.LastModified
		}
	}

	summary.TotalSize = totalSize
	summary.OldestFile = oldestTime
	summary.NewestFile = newestTime

	if summary.TotalFiles > 0 {
		summary.AverageFileSize = totalSize / int64(summary.TotalFiles)
	}

	return summary
}
