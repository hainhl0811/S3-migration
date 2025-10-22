package tuning

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"s3migration/pkg/adaptive"
	"s3migration/pkg/models"
	"s3migration/pkg/network"
)

// WorkerConfig defines worker count configuration for a pattern
type WorkerConfig struct {
	Min     int
	Max     int
	Default int
}

// PerformanceSample represents a performance measurement
type PerformanceSample struct {
	Speed     float64
	Workers   int
	FileSize  int64
	Timestamp time.Time
}

// Tuner dynamically adjusts performance parameters
type Tuner struct {
	mu                  sync.RWMutex
	currentPattern      models.WorkloadPattern
	currentWorkers      atomic.Int32
	minWorkers          int
	maxWorkers          int
	networkMonitor      *network.Monitor
	memoryManager       *adaptive.MemoryManager // Memory-aware worker management
	performanceSamples  []PerformanceSample
	sizeDistribution    []int64
	adjustmentThreshold int
	lastAdjustmentTime  time.Time
	adjustmentInterval  time.Duration
	totalBytes          int64
	totalFiles          int64
	avgFileSize         float64
	workerConfigs       map[models.WorkloadPattern]WorkerConfig
}

// NewTuner creates a new performance tuner with adaptive memory management
func NewTuner() *Tuner {
	// Create memory manager for dynamic worker adjustment
	memMgr := adaptive.NewMemoryManager()
	
	// Get memory-aware max workers
	maxWorkers := memMgr.GetMaxWorkers()
	
	// All patterns now use memory-aware limits (no hardcoded differences)
	// Start with aggressive defaults - memory manager will limit if needed
	defaultWorkers := maxWorkers / 2 // Start with 50% of max workers
	if defaultWorkers < 50 {
		defaultWorkers = min(50, maxWorkers) // At least 50 workers
	}
	
	configs := map[models.WorkloadPattern]WorkerConfig{
		models.PatternManySmall:  {Min: 10, Max: maxWorkers, Default: defaultWorkers},
		models.PatternMixed:      {Min: 10, Max: maxWorkers, Default: defaultWorkers},
		models.PatternLargeFiles: {Min: 5, Max: maxWorkers, Default: defaultWorkers / 2}, // Memory manager will limit appropriately
		models.PatternUnknown:    {Min: 10, Max: maxWorkers, Default: defaultWorkers},
	}

	t := &Tuner{
		currentPattern:      models.PatternUnknown,
		networkMonitor:      network.NewMonitor(),
		memoryManager:       memMgr, // Memory-aware management
		performanceSamples:  make([]PerformanceSample, 0),
		sizeDistribution:    make([]int64, 0),
		adjustmentThreshold: 5,
		adjustmentInterval:  30 * time.Second,
		workerConfigs:       configs,
	}

	config := configs[models.PatternUnknown]
	t.minWorkers = config.Min
	t.maxWorkers = config.Max
	t.currentWorkers.Store(int32(config.Default))
	
	fmt.Printf("📊 Tuner initialized with memory-aware limits (max: %d workers)\n", maxWorkers)

	return t
}

// AnalyzeWorkload analyzes the workload pattern
func (t *Tuner) AnalyzeWorkload(fileSizes []int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(fileSizes) == 0 {
		return
	}

	t.sizeDistribution = fileSizes
	t.totalFiles = int64(len(fileSizes))

	// Calculate total bytes and average
	var total int64
	for _, size := range fileSizes {
		total += size
	}
	t.totalBytes = total
	t.avgFileSize = float64(total) / float64(len(fileSizes))

	// Detect pattern based on BOTH file count AND total data size
	smallFiles := 0
	largeFiles := 0
	var smallFilesBytes int64
	var largeFilesBytes int64
	
	for _, size := range fileSizes {
		if size < 1024*1024 { // < 1MB
			smallFiles++
			smallFilesBytes += size
		} else if size > 100*1024*1024 { // > 100MB
			largeFiles++
			largeFilesBytes += size
		}
	}

	// Calculate ratios by both count and size
	smallFileCountRatio := float64(smallFiles) / float64(t.totalFiles)
	smallFileSizeRatio := float64(smallFilesBytes) / float64(total)
	largeFileSizeRatio := float64(largeFilesBytes) / float64(total)

	var newPattern models.WorkloadPattern
	
	// If large files account for >20% of total data, treat as large files
	// even if they're few in count
	if largeFileSizeRatio > 0.2 {
		newPattern = models.PatternLargeFiles
		fmt.Printf("Pattern detection: Large files (%.1f%% of data in %d large files)\n", 
			largeFileSizeRatio*100, largeFiles)
	} else if smallFileSizeRatio > 0.8 && smallFileCountRatio > 0.8 {
		// If >80% of files AND data are small, use many small pattern
		newPattern = models.PatternManySmall
		fmt.Printf("Pattern detection: Many small files (%.1f%% of data in %d small files)\n", 
			smallFileSizeRatio*100, smallFiles)
	} else {
		// Mixed workload
		newPattern = models.PatternMixed
		fmt.Printf("Pattern detection: Mixed sizes (small: %.1f%% data, large: %.1f%% data)\n", 
			smallFileSizeRatio*100, largeFileSizeRatio*100)
	}

	// Update configuration if pattern changed
	if newPattern != t.currentPattern {
		t.currentPattern = newPattern
		config := t.workerConfigs[newPattern]
		t.minWorkers = config.Min
		t.maxWorkers = config.Max
		t.currentWorkers.Store(int32(config.Default))

		fmt.Printf("\nWorkload pattern detected: %s\n", newPattern)
		fmt.Printf("Adjusting worker range to %d-%d\n", t.minWorkers, t.maxWorkers)
	}
}

// RecordPerformance records a performance sample
func (t *Tuner) RecordPerformance(transferSpeedMB float64, activeWorkers int, fileSize int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	sample := PerformanceSample{
		Speed:     transferSpeedMB,
		Workers:   activeWorkers,
		FileSize:  fileSize,
		Timestamp: time.Now(),
	}

	t.performanceSamples = append(t.performanceSamples, sample)

	// Keep only recent samples (last 5 minutes)
	cutoff := time.Now().Add(-5 * time.Minute)
	filtered := make([]PerformanceSample, 0)
	for _, s := range t.performanceSamples {
		if s.Timestamp.After(cutoff) {
			filtered = append(filtered, s)
		}
	}
	t.performanceSamples = filtered
}

// ShouldAdjust determines if it's time to adjust workers
func (t *Tuner) ShouldAdjust() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.performanceSamples) < t.adjustmentThreshold {
		return false
	}

	if time.Since(t.lastAdjustmentTime) < t.adjustmentInterval {
		return false
	}

	return true
}

// GetOptimalWorkers calculates optimal worker count
func (t *Tuner) GetOptimalWorkers() int {
	// PRIORITY 1: Check memory constraints first!
	current := int(t.currentWorkers.Load())
	t.memoryManager.RecordMemoryUsage(current)
	
	// Get memory-safe worker count
	memorySafeWorkers := t.memoryManager.GetOptimalWorkers()
	
	// Force GC if memory is high
	t.memoryManager.ForceGCIfNeeded()
	
	if !t.ShouldAdjust() {
		// Even if not adjusting for performance, respect memory limits
		if memorySafeWorkers < current {
			t.currentWorkers.Store(int32(memorySafeWorkers))
			fmt.Printf("⚠️  Reducing workers to %d due to memory constraints\n", memorySafeWorkers)
		}
		return int(t.currentWorkers.Load())
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Group samples by worker count
	workerSpeeds := make(map[int][]float64)
	for _, sample := range t.performanceSamples {
		workerSpeeds[sample.Workers] = append(workerSpeeds[sample.Workers], sample.Speed)
	}

	// Memory manager handles optimization - no pattern-specific logic needed
	// Start with current workers and let memory manager adjust
	optimalWorkers := int(t.currentWorkers.Load())

	// Apply network recommendations
	networkRecommended := t.networkMonitor.GetRecommendedWorkers(optimalWorkers)
	condition := t.networkMonitor.GetCurrentCondition()

	if condition == models.NetworkPoor || condition == models.NetworkFair {
		optimalWorkers = networkRecommended
	} else {
		optimalWorkers = (optimalWorkers + networkRecommended) / 2
	}

	// CRITICAL: Memory limits take priority over performance optimization!
	if optimalWorkers > memorySafeWorkers {
		optimalWorkers = memorySafeWorkers
		fmt.Printf("⚠️  Worker count capped at %d by memory manager\n", memorySafeWorkers)
	}

	// Apply bounds
	boundedWorkers := max(t.minWorkers, min(optimalWorkers, t.maxWorkers))

	// Fast adjustment - jump to optimal immediately for better performance
	t.currentWorkers.Store(int32(boundedWorkers))

	t.lastAdjustmentTime = time.Now()
	return int(t.currentWorkers.Load())
}

// Old optimization functions removed - memory manager handles all optimization

// GetCurrentWorkers returns current worker count
func (t *Tuner) GetCurrentWorkers() int {
	return int(t.currentWorkers.Load())
}

// GetMaxWorkers returns maximum worker count
func (t *Tuner) GetMaxWorkers() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.maxWorkers
}

func avg(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
