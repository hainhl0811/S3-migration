package adaptive

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

// MemoryManager dynamically adjusts concurrency based on available memory
type MemoryManager struct {
	mu                  sync.RWMutex
	maxMemoryMiB        int64   // Maximum memory limit (from GOMEMLIMIT or K8s)
	safeThresholdPct    float64 // Safe threshold percentage (e.g., 0.7 = 70%)
	currentWorkers      int
	minWorkers          int
	maxWorkers          int
	lastAdjustment      time.Time
	adjustmentInterval  time.Duration
	memoryHistory       []int64 // Recent memory samples
	historySamples      int
	estimatedPerWorker  int64 // Estimated memory per worker in MiB
}

// MemoryStats represents current memory statistics
type MemoryStats struct {
	AllocMiB      int64
	TotalAllocMiB int64
	SysMiB        int64
	UsagePercent  float64
	AvailableMiB  int64
}

// NewMemoryManager creates a new adaptive memory manager
func NewMemoryManager() *MemoryManager {
	// Get GOMEMLIMIT from environment
	var maxMemory int64 = 2048 // Default 2GiB if not set
	
	if limit := debug.SetMemoryLimit(-1); limit > 0 {
		maxMemory = limit / (1024 * 1024) // Convert to MiB
	}
	
	mm := &MemoryManager{
		maxMemoryMiB:       maxMemory,
		safeThresholdPct:   0.85, // Use max 85% of available memory (optimized)
		currentWorkers:     1,
		minWorkers:         1,
		maxWorkers:         100, // Will be adjusted based on memory
		adjustmentInterval: 30 * time.Second,
		lastAdjustment:     time.Now(),
		memoryHistory:      make([]int64, 0, 10),
		historySamples:     10,
		estimatedPerWorker: 100, // Initial estimate: 100 MiB per worker
	}
	
	// Calculate realistic max workers based on memory
	safeMemory := int64(float64(mm.maxMemoryMiB) * mm.safeThresholdPct)
	mm.maxWorkers = int(safeMemory / mm.estimatedPerWorker)
	if mm.maxWorkers < mm.minWorkers {
		mm.maxWorkers = mm.minWorkers
	}
	
	fmt.Printf("🧠 Memory Manager initialized:\n")
	fmt.Printf("   Max Memory: %d MiB\n", mm.maxMemoryMiB)
	fmt.Printf("   Safe Threshold: %.0f%% (%d MiB)\n", mm.safeThresholdPct*100, safeMemory)
	fmt.Printf("   Estimated per worker: %d MiB\n", mm.estimatedPerWorker)
	fmt.Printf("   Max workers allowed: %d\n", mm.maxWorkers)
	
	return mm
}

// GetCurrentStats returns current memory statistics
func (mm *MemoryManager) GetCurrentStats() MemoryStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	
	allocMiB := int64(m.Alloc / 1024 / 1024)
	sysMiB := int64(m.Sys / 1024 / 1024)
	totalAllocMiB := int64(m.TotalAlloc / 1024 / 1024)
	
	usagePercent := float64(allocMiB) / float64(mm.maxMemoryMiB) * 100
	availableMiB := mm.maxMemoryMiB - allocMiB
	
	return MemoryStats{
		AllocMiB:      allocMiB,
		TotalAllocMiB: totalAllocMiB,
		SysMiB:        sysMiB,
		UsagePercent:  usagePercent,
		AvailableMiB:  availableMiB,
	}
}

// RecordMemoryUsage records current memory usage for analysis
func (mm *MemoryManager) RecordMemoryUsage(workers int) {
	stats := mm.GetCurrentStats()
	
	mm.mu.Lock()
	defer mm.mu.Unlock()
	
	// Add to history
	mm.memoryHistory = append(mm.memoryHistory, stats.AllocMiB)
	if len(mm.memoryHistory) > mm.historySamples {
		mm.memoryHistory = mm.memoryHistory[1:]
	}
	
	// Update estimated memory per worker
	if workers > 0 && len(mm.memoryHistory) >= 3 {
		avgMemory := average(mm.memoryHistory)
		mm.estimatedPerWorker = avgMemory / int64(workers)
		if mm.estimatedPerWorker < 50 {
			mm.estimatedPerWorker = 3  // Optimized for small objects (100KB) - 3 MiB per worker
		}
	}
}

// GetOptimalWorkers calculates optimal worker count based on current memory
func (mm *MemoryManager) GetOptimalWorkers() int {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	
	stats := mm.GetCurrentStats()
	
	// Calculate safe memory available
	safeMemory := int64(float64(mm.maxMemoryMiB) * mm.safeThresholdPct)
	availableForWorkers := safeMemory - stats.AllocMiB
	
	// If we're over safe threshold, reduce workers
	if stats.AllocMiB > safeMemory {
		if mm.currentWorkers > mm.minWorkers {
			mm.currentWorkers--
			mm.triggerGC("over safe threshold")
		}
		return mm.currentWorkers
	}
	
	// Calculate how many workers we can safely add
	if availableForWorkers > mm.estimatedPerWorker {
		potentialWorkers := int(availableForWorkers / mm.estimatedPerWorker)
		
		// Don't increase too aggressively
		if potentialWorkers > mm.currentWorkers+2 {
			potentialWorkers = mm.currentWorkers + 2
		}
		
		// Apply bounds
		if potentialWorkers > mm.maxWorkers {
			potentialWorkers = mm.maxWorkers
		}
		if potentialWorkers < mm.minWorkers {
			potentialWorkers = mm.minWorkers
		}
		
		mm.currentWorkers = potentialWorkers
	}
	
	return mm.currentWorkers
}

// ShouldAdjustWorkers determines if workers should be adjusted
func (mm *MemoryManager) ShouldAdjustWorkers() bool {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	
	// Don't adjust too frequently
	if time.Since(mm.lastAdjustment) < mm.adjustmentInterval {
		return false
	}
	
	stats := mm.GetCurrentStats()
	
	// Adjust if memory usage is high (> 60%) or low (< 30%)
	return stats.UsagePercent > 60 || stats.UsagePercent < 30
}

// AdjustWorkers adjusts worker count based on current memory
func (mm *MemoryManager) AdjustWorkers() int {
	mm.mu.Lock()
	mm.lastAdjustment = time.Now()
	mm.mu.Unlock()
	
	newWorkers := mm.GetOptimalWorkers()
	
	stats := mm.GetCurrentStats()
	fmt.Printf("🧠 Memory: %d MiB / %d MiB (%.1f%%) | Workers: %d | Est per worker: %d MiB\n",
		stats.AllocMiB, mm.maxMemoryMiB, stats.UsagePercent, newWorkers, mm.estimatedPerWorker)
	
	return newWorkers
}

// triggerGC forces garbage collection
func (mm *MemoryManager) triggerGC(reason string) {
	before := mm.GetCurrentStats()
	runtime.GC()
	debug.FreeOSMemory()
	after := mm.GetCurrentStats()
	
	freed := before.AllocMiB - after.AllocMiB
	if freed > 0 {
		fmt.Printf("🗑️  GC triggered (%s): freed %d MiB (was %d MiB, now %d MiB)\n",
			reason, freed, before.AllocMiB, after.AllocMiB)
	}
}

// ForceGCIfNeeded triggers GC if memory usage is high
func (mm *MemoryManager) ForceGCIfNeeded() bool {
	stats := mm.GetCurrentStats()
	
	// Trigger GC if over 60% of safe threshold
	threshold := int64(float64(mm.maxMemoryMiB) * mm.safeThresholdPct * 0.6)
	if stats.AllocMiB > threshold {
		mm.triggerGC("high memory usage")
		return true
	}
	
	return false
}

// GetCurrentWorkers returns current worker count
func (mm *MemoryManager) GetCurrentWorkers() int {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.currentWorkers
}

// GetMaxWorkers returns maximum worker count
func (mm *MemoryManager) GetMaxWorkers() int {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.maxWorkers
}

// SetSafeThreshold sets the safe memory threshold percentage
func (mm *MemoryManager) SetSafeThreshold(percent float64) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	
	if percent > 0 && percent <= 1.0 {
		mm.safeThresholdPct = percent
		
		// Recalculate max workers
		safeMemory := int64(float64(mm.maxMemoryMiB) * mm.safeThresholdPct)
		mm.maxWorkers = int(safeMemory / mm.estimatedPerWorker)
		if mm.maxWorkers < mm.minWorkers {
			mm.maxWorkers = mm.minWorkers
		}
		
		fmt.Printf("🧠 Safe threshold updated to %.0f%% (%d MiB), max workers: %d\n",
			percent*100, safeMemory, mm.maxWorkers)
	}
}

// LogMemoryStats logs detailed memory statistics
func (mm *MemoryManager) LogMemoryStats() {
	stats := mm.GetCurrentStats()
	
	mm.mu.RLock()
	workers := mm.currentWorkers
	maxWorkers := mm.maxWorkers
	mm.mu.RUnlock()
	
	fmt.Printf("📊 Memory Stats:\n")
	fmt.Printf("   Current: %d MiB / %d MiB (%.1f%%)\n", stats.AllocMiB, mm.maxMemoryMiB, stats.UsagePercent)
	fmt.Printf("   Available: %d MiB\n", stats.AvailableMiB)
	fmt.Printf("   Workers: %d / %d max\n", workers, maxWorkers)
	fmt.Printf("   Estimated per worker: %d MiB\n", mm.estimatedPerWorker)
}

func average(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	var sum int64
	for _, v := range values {
		sum += v
	}
	return sum / int64(len(values))
}

