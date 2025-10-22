package core

import (
	"context"
	"fmt"
	"sync"
	"time"

	"s3migration/pkg/adaptive"
	"s3migration/pkg/integrity"
	"s3migration/pkg/state"
)

// PriorityWorkloadManager manages workloads with different priorities based on object size
// Inspired by rclone's workload management
type PriorityWorkloadManager struct {
	// Priority queues for different object sizes
	highPriorityQueue   chan ObjectInfo   // Small objects (< 1MB) - high concurrency
	mediumPriorityQueue chan ObjectInfo   // Medium objects (1MB - 100MB) - medium concurrency
	lowPriorityQueue    chan ObjectInfo   // Large objects (> 100MB) - low concurrency
	
	// Worker pools for each priority
	highPriorityWorkers   int
	mediumPriorityWorkers int
	lowPriorityWorkers    int
	
	// Memory estimator
	memoryEstimator *DynamicMemoryEstimator
	
	// Network monitor
	networkMonitor *adaptive.NetworkMonitor
	
	// State management
	mu sync.RWMutex
}

// ObjectInfo represents an object to be migrated
type ObjectInfo struct {
	Key        string
	Size       int64
	Priority   Priority
	QueuedAt   time.Time
	RetryCount int
}

// Priority represents the processing priority
type Priority int

const (
	HighPriority   Priority = 1 // Small objects - process first
	MediumPriority Priority = 2 // Medium objects - process second
	LowPriority    Priority = 3 // Large objects - process last
)

// WorkloadStats contains statistics about the workload
type WorkloadStats struct {
	HighPriorityCount   int
	MediumPriorityCount int
	LowPriorityCount    int
	TotalObjects        int
	TotalSize           int64
	AverageSize         float64
}

// NewPriorityWorkloadManager creates a new priority workload manager
func NewPriorityWorkloadManager(
	memoryEstimator *DynamicMemoryEstimator,
	networkMonitor *adaptive.NetworkMonitor,
) *PriorityWorkloadManager {
	return &PriorityWorkloadManager{
		highPriorityQueue:   make(chan ObjectInfo, 10000),   // Large buffer for small objects
		mediumPriorityQueue: make(chan ObjectInfo, 1000),    // Medium buffer for medium objects
		lowPriorityQueue:    make(chan ObjectInfo, 100),     // Small buffer for large objects
		
		highPriorityWorkers:   0, // Will be calculated dynamically
		mediumPriorityWorkers: 0, // Will be calculated dynamically
		lowPriorityWorkers:    0, // Will be calculated dynamically
		
		memoryEstimator: memoryEstimator,
		networkMonitor:  networkMonitor,
	}
}

// AddObject adds an object to the appropriate priority queue
func (pwm *PriorityWorkloadManager) AddObject(key string, size int64) {
	priority := pwm.determinePriority(size)
	
	objectInfo := ObjectInfo{
		Key:      key,
		Size:     size,
		Priority: priority,
		QueuedAt: time.Now(),
	}
	
	switch priority {
	case HighPriority:
		select {
		case pwm.highPriorityQueue <- objectInfo:
		default:
			// Queue full - could implement backpressure here
			fmt.Printf("High priority queue full, dropping object: %s\n", key)
		}
	case MediumPriority:
		select {
		case pwm.mediumPriorityQueue <- objectInfo:
		default:
			fmt.Printf("Medium priority queue full, dropping object: %s\n", key)
		}
	case LowPriority:
		select {
		case pwm.lowPriorityQueue <- objectInfo:
		default:
			fmt.Printf("Low priority queue full, dropping object: %s\n", key)
		}
	}
}

// determinePriority determines the priority based on object size
// Based on rclone's size-based prioritization
func (pwm *PriorityWorkloadManager) determinePriority(size int64) Priority {
	switch {
	case size < 1024*1024: // < 1MB
		return HighPriority
	case size < 100*1024*1024: // < 100MB
		return MediumPriority
	default: // >= 100MB
		return LowPriority
	}
}

// CalculateOptimalWorkers calculates optimal worker counts for each priority
// Based on available memory and object size distribution
func (pwm *PriorityWorkloadManager) CalculateOptimalWorkers(availableMemory int64) {
	pwm.mu.Lock()
	defer pwm.mu.Unlock()
	
	// Get current workload stats
	stats := pwm.getWorkloadStats()
	
	// Calculate memory allocation for each priority
	// High priority gets 60% of memory (for many small objects)
	// Medium priority gets 30% of memory (for medium objects)
	// Low priority gets 10% of memory (for few large objects)
	highMemory := int64(float64(availableMemory) * 0.6)
	mediumMemory := int64(float64(availableMemory) * 0.3)
	lowMemory := int64(float64(availableMemory) * 0.1)
	
	// Calculate workers for each priority
	if stats.HighPriorityCount > 0 {
		avgSize := pwm.calculateAverageSize(HighPriority)
		pwm.highPriorityWorkers = pwm.memoryEstimator.GetOptimalWorkers(avgSize, highMemory)
	}
	
	if stats.MediumPriorityCount > 0 {
		avgSize := pwm.calculateAverageSize(MediumPriority)
		pwm.mediumPriorityWorkers = pwm.memoryEstimator.GetOptimalWorkers(avgSize, mediumMemory)
	}
	
	if stats.LowPriorityCount > 0 {
		avgSize := pwm.calculateAverageSize(LowPriority)
		pwm.lowPriorityWorkers = pwm.memoryEstimator.GetOptimalWorkers(avgSize, lowMemory)
	}
}

// calculateAverageSize calculates the average size for objects in a priority queue
func (pwm *PriorityWorkloadManager) calculateAverageSize(priority Priority) int64 {
	// This is a simplified calculation - in practice, you'd want to sample the actual queue
	switch priority {
	case HighPriority:
		return 100 * 1024 // 100KB average for small objects
	case MediumPriority:
		return 10 * 1024 * 1024 // 10MB average for medium objects
	case LowPriority:
		return 500 * 1024 * 1024 // 500MB average for large objects
	default:
		return 1024 * 1024 // 1MB default
	}
}

// getWorkloadStats returns current workload statistics
func (pwm *PriorityWorkloadManager) getWorkloadStats() WorkloadStats {
	return WorkloadStats{
		HighPriorityCount:   len(pwm.highPriorityQueue),
		MediumPriorityCount: len(pwm.mediumPriorityQueue),
		LowPriorityCount:    len(pwm.lowPriorityQueue),
		TotalObjects:        len(pwm.highPriorityQueue) + len(pwm.mediumPriorityQueue) + len(pwm.lowPriorityQueue),
	}
}

// ProcessWorkloads processes objects from all priority queues
// This is the main processing loop inspired by rclone's multithreaded approach
func (pwm *PriorityWorkloadManager) ProcessWorkloads(
	ctx context.Context,
	migrator *EnhancedMigrator,
	integrityManager *state.IntegrityManager,
) error {
	
	// Calculate optimal workers
	pwm.CalculateOptimalWorkers(3500) // 3.5GB available memory
	
	var wg sync.WaitGroup
	
	// Start high priority workers (small objects)
	for i := 0; i < pwm.highPriorityWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			pwm.processHighPriorityWorker(ctx, workerID, migrator, integrityManager)
		}(i)
	}
	
	// Start medium priority workers (medium objects)
	for i := 0; i < pwm.mediumPriorityWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			pwm.processMediumPriorityWorker(ctx, workerID, migrator, integrityManager)
		}(i)
	}
	
	// Start low priority workers (large objects)
	for i := 0; i < pwm.lowPriorityWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			pwm.processLowPriorityWorker(ctx, workerID, migrator, integrityManager)
		}(i)
	}
	
	// Wait for all workers to complete
	wg.Wait()
	
	return nil
}

// processHighPriorityWorker processes high priority objects (small objects)
func (pwm *PriorityWorkloadManager) processHighPriorityWorker(
	ctx context.Context,
	workerID int,
	migrator *EnhancedMigrator,
	integrityManager *state.IntegrityManager,
) {
	
	for {
		select {
		case objectInfo := <-pwm.highPriorityQueue:
			// Process small object with high concurrency
			pwm.processObject(ctx, objectInfo, migrator, integrityManager, "high-priority")
		case <-ctx.Done():
			return
		}
	}
}

// processMediumPriorityWorker processes medium priority objects (medium objects)
func (pwm *PriorityWorkloadManager) processMediumPriorityWorker(
	ctx context.Context,
	workerID int,
	migrator *EnhancedMigrator,
	integrityManager *state.IntegrityManager,
) {
	
	for {
		select {
		case objectInfo := <-pwm.mediumPriorityQueue:
			// Process medium object with medium concurrency
			pwm.processObject(ctx, objectInfo, migrator, integrityManager, "medium-priority")
		case <-ctx.Done():
			return
		}
	}
}

// processLowPriorityWorker processes low priority objects (large objects)
func (pwm *PriorityWorkloadManager) processLowPriorityWorker(
	ctx context.Context,
	workerID int,
	migrator *EnhancedMigrator,
	integrityManager *state.IntegrityManager,
) {
	
	for {
		select {
		case objectInfo := <-pwm.lowPriorityQueue:
			// Process large object with low concurrency
			pwm.processObject(ctx, objectInfo, migrator, integrityManager, "low-priority")
		case <-ctx.Done():
			return
		}
	}
}

// processObject processes a single object
func (pwm *PriorityWorkloadManager) processObject(
	ctx context.Context,
	objectInfo ObjectInfo,
	migrator *EnhancedMigrator,
	integrityManager *state.IntegrityManager,
	workerType string,
) {
	
	// Add retry logic inspired by rclone
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff
			backoff := time.Duration(attempt) * time.Second
			time.Sleep(backoff)
		}
		
		// Process the object
		err := pwm.processSingleObject(ctx, objectInfo, migrator, integrityManager)
		if err == nil {
			// Success
			return
		}
		
		// Log error and retry
		fmt.Printf("[%s] Error processing object %s (attempt %d/%d): %v\n", 
			workerType, objectInfo.Key, attempt+1, maxRetries, err)
	}
	
	// All retries failed
	fmt.Printf("[%s] Failed to process object %s after %d attempts\n", 
		workerType, objectInfo.Key, maxRetries)
}

// processSingleObject processes a single object (placeholder for actual implementation)
func (pwm *PriorityWorkloadManager) processSingleObject(
	ctx context.Context,
	objectInfo ObjectInfo,
	migrator *EnhancedMigrator,
	integrityManager *state.IntegrityManager,
) error {
	
	// This would call the actual migration logic
	// For now, just simulate processing
	processingTime := pwm.calculateProcessingTime(objectInfo.Size)
	time.Sleep(processingTime)
	
	// Update memory estimator with actual usage
	// This helps the estimator learn and improve
	estimatedMemory := pwm.memoryEstimator.EstimateMemoryPerWorker(objectInfo.Size)
	pwm.memoryEstimator.UpdateMemoryProfile(objectInfo.Size, estimatedMemory, 1)
	
	return nil
}

// calculateProcessingTime calculates estimated processing time based on object size
// This is used for simulation - in practice, you'd measure actual processing time
func (pwm *PriorityWorkloadManager) calculateProcessingTime(size int64) time.Duration {
	// Base processing time
	baseTime := 100 * time.Millisecond
	
	// Add time based on size
	sizeTime := time.Duration(size/1024) * time.Microsecond // 1 microsecond per KB
	
	// Add network quality adjustment
	networkQuality := pwm.networkMonitor.GetCurrentCondition()
	switch networkQuality {
	case "excellent":
		return baseTime + sizeTime/2
	case "good":
		return baseTime + sizeTime
	case "fair":
		return baseTime + sizeTime*2
	case "poor":
		return baseTime + sizeTime*4
	default:
		return baseTime + sizeTime
	}
}

// GetQueueStats returns current queue statistics
func (pwm *PriorityWorkloadManager) GetQueueStats() WorkloadStats {
	pwm.mu.RLock()
	defer pwm.mu.RUnlock()
	
	return pwm.getWorkloadStats()
}

// GetWorkerStats returns current worker statistics
func (pwm *PriorityWorkloadManager) GetWorkerStats() map[string]int {
	pwm.mu.RLock()
	defer pwm.mu.RUnlock()
	
	return map[string]int{
		"high_priority_workers":   pwm.highPriorityWorkers,
		"medium_priority_workers": pwm.mediumPriorityWorkers,
		"low_priority_workers":    pwm.lowPriorityWorkers,
	}
}

// Close closes all queues
func (pwm *PriorityWorkloadManager) Close() {
	close(pwm.highPriorityQueue)
	close(pwm.mediumPriorityQueue)
	close(pwm.lowPriorityQueue)
}
