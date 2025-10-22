package core

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"s3migration/pkg/adaptive"
)

// DynamicMemoryEstimator implements rclone-inspired dynamic memory estimation
type DynamicMemoryEstimator struct {
	// Historical data for different object sizes
	sizeMemoryMap map[string]MemoryProfile
	mu            sync.RWMutex
	
	// Network quality monitoring
	networkMonitor *adaptive.NetworkMonitor
	
	// Memory profiles for different object sizes
	profiles map[string]MemoryProfile
}

// MemoryProfile contains memory usage data for a specific object size range
type MemoryProfile struct {
	SizeRange     string  // e.g., "small", "medium", "large"
	MinSize       int64   // Minimum object size in bytes
	MaxSize       int64   // Maximum object size in bytes
	BaseMemory    int64   // Base memory in MiB
	PerMBMemory   float64 // Additional memory per MB
	WorkerOverhead int64  // Overhead per worker in MiB
	LastUpdated   time.Time
	SampleCount   int
}

// NewDynamicMemoryEstimator creates a new dynamic memory estimator
func NewDynamicMemoryEstimator(networkMonitor *adaptive.NetworkMonitor) *DynamicMemoryEstimator {
	return &DynamicMemoryEstimator{
		sizeMemoryMap:  make(map[string]MemoryProfile),
		networkMonitor: networkMonitor,
		profiles: map[string]MemoryProfile{
			"tiny": {
				SizeRange:     "tiny",
				MinSize:       0,
				MaxSize:       1024 * 1024, // < 1MB
				BaseMemory:    1,           // 1 MiB base
				PerMBMemory:   0.5,         // 0.5 MiB per MB
				WorkerOverhead: 1,          // 1 MiB per worker
				LastUpdated:   time.Now(),
				SampleCount:   0,
			},
			"small": {
				SizeRange:     "small",
				MinSize:       1024 * 1024,        // 1MB
				MaxSize:       10 * 1024 * 1024,   // 10MB
				BaseMemory:    2,                  // 2 MiB base
				PerMBMemory:   0.3,                // 0.3 MiB per MB
				WorkerOverhead: 2,                 // 2 MiB per worker
				LastUpdated:   time.Now(),
				SampleCount:   0,
			},
			"medium": {
				SizeRange:     "medium",
				MinSize:       10 * 1024 * 1024,   // 10MB
				MaxSize:       100 * 1024 * 1024,  // 100MB
				BaseMemory:    5,                  // 5 MiB base
				PerMBMemory:   0.2,                // 0.2 MiB per MB
				WorkerOverhead: 5,                 // 5 MiB per worker
				LastUpdated:   time.Now(),
				SampleCount:   0,
			},
			"large": {
				SizeRange:     "large",
				MinSize:       100 * 1024 * 1024,  // 100MB
				MaxSize:       1024 * 1024 * 1024, // 1GB
				BaseMemory:    10,                 // 10 MiB base
				PerMBMemory:   0.1,                // 0.1 MiB per MB
				WorkerOverhead: 10,                // 10 MiB per worker
				LastUpdated:   time.Now(),
				SampleCount:   0,
			},
			"xlarge": {
				SizeRange:     "xlarge",
				MinSize:       1024 * 1024 * 1024, // 1GB
				MaxSize:       10 * 1024 * 1024 * 1024, // 10GB
				BaseMemory:    20,                 // 20 MiB base
				PerMBMemory:   0.05,               // 0.05 MiB per MB
				WorkerOverhead: 20,                // 20 MiB per worker
				LastUpdated:   time.Now(),
				SampleCount:   0,
			},
		},
	}
}

// EstimateMemoryPerWorker calculates memory per worker based on object size
// Inspired by rclone's adaptive memory management
func (dme *DynamicMemoryEstimator) EstimateMemoryPerWorker(objectSize int64) int64 {
	dme.mu.RLock()
	defer dme.mu.RUnlock()
	
	// Get the appropriate profile
	profile := dme.getProfileForSize(objectSize)
	
	// Calculate memory based on object size
	objectSizeMB := float64(objectSize) / (1024 * 1024)
	memoryPerWorker := profile.BaseMemory + int64(profile.PerMBMemory*objectSizeMB)
	
	// Add network quality adjustment
	networkQuality := dme.networkMonitor.GetCurrentCondition()
	switch networkQuality {
	case "excellent":
		memoryPerWorker = int64(float64(memoryPerWorker) * 0.8) // 20% less for excellent network
	case "good":
		memoryPerWorker = int64(float64(memoryPerWorker) * 0.9) // 10% less for good network
	case "fair":
		// No adjustment for fair network
	case "poor":
		memoryPerWorker = int64(float64(memoryPerWorker) * 1.2) // 20% more for poor network
	}
	
	// Ensure minimum memory
	if memoryPerWorker < 1 {
		memoryPerWorker = 1
	}
	
	return memoryPerWorker
}

// getProfileForSize returns the appropriate profile for the given object size
func (dme *DynamicMemoryEstimator) getProfileForSize(objectSize int64) MemoryProfile {
	for _, profile := range dme.profiles {
		if objectSize >= profile.MinSize && objectSize < profile.MaxSize {
			return profile
		}
	}
	
	// Default to large profile for very large objects
	return dme.profiles["xlarge"]
}

// UpdateMemoryProfile updates the memory profile based on actual usage
// This allows the estimator to learn and improve over time
func (dme *DynamicMemoryEstimator) UpdateMemoryProfile(objectSize int64, actualMemory int64, workerCount int) {
	dme.mu.Lock()
	defer dme.mu.Unlock()
	
	profile := dme.getProfileForSize(objectSize)
	
	// Calculate memory per worker
	memoryPerWorker := actualMemory / int64(workerCount)
	
	// Update the profile with exponential moving average
	alpha := 0.1 // Learning rate
	objectSizeMB := float64(objectSize) / (1024 * 1024)
	
	// Update base memory
	newBaseMemory := int64(float64(profile.BaseMemory)*(1-alpha) + float64(memoryPerWorker)*alpha)
	profile.BaseMemory = newBaseMemory
	
	// Update per-MB memory
	if objectSizeMB > 0 {
		perMBMemory := float64(memoryPerWorker-profile.BaseMemory) / objectSizeMB
		profile.PerMBMemory = profile.PerMBMemory*(1-alpha) + perMBMemory*alpha
	}
	
	// Update worker overhead
	workerOverhead := memoryPerWorker - profile.BaseMemory - int64(profile.PerMBMemory*objectSizeMB)
	if workerOverhead > 0 {
		profile.WorkerOverhead = int64(float64(profile.WorkerOverhead)*(1-alpha) + float64(workerOverhead)*alpha)
	}
	
	profile.LastUpdated = time.Now()
	profile.SampleCount++
	
	dme.profiles[profile.SizeRange] = profile
}

// GetOptimalWorkers calculates optimal number of workers based on object size and available memory
// Based on rclone's adaptive worker calculation
func (dme *DynamicMemoryEstimator) GetOptimalWorkers(objectSize int64, availableMemory int64) int {
	memoryPerWorker := dme.EstimateMemoryPerWorker(objectSize)
	
	// Calculate maximum possible workers
	maxWorkers := availableMemory / memoryPerWorker
	
	// Apply rclone-inspired worker limits based on object size
	objectSizeMB := float64(objectSize) / (1024 * 1024)
	
	var recommendedWorkers int
	switch {
	case objectSizeMB < 1: // < 1MB
		recommendedWorkers = 2
	case objectSizeMB < 10: // < 10MB
		recommendedWorkers = 4
	case objectSizeMB < 100: // < 100MB
		recommendedWorkers = 8
	case objectSizeMB < 1000: // < 1GB
		recommendedWorkers = 16
	default: // >= 1GB
		recommendedWorkers = 32
	}
	
	// Use the smaller of max possible and recommended
	if maxWorkers < recommendedWorkers {
		return int(maxWorkers)
	}
	return recommendedWorkers
}

// GetMemoryProfile returns the current memory profile for a size range
func (dme *DynamicMemoryEstimator) GetMemoryProfile(sizeRange string) (MemoryProfile, error) {
	dme.mu.RLock()
	defer dme.mu.RUnlock()
	
	profile, exists := dme.profiles[sizeRange]
	if !exists {
		return MemoryProfile{}, fmt.Errorf("unknown size range: %s", sizeRange)
	}
	
	return profile, nil
}

// GetAllProfiles returns all memory profiles
func (dme *DynamicMemoryEstimator) GetAllProfiles() map[string]MemoryProfile {
	dme.mu.RLock()
	defer dme.mu.RUnlock()
	
	// Return a copy to avoid race conditions
	profiles := make(map[string]MemoryProfile)
	for k, v := range dme.profiles {
		profiles[k] = v
	}
	
	return profiles
}

// ResetProfiles resets all profiles to default values
func (dme *DynamicMemoryEstimator) ResetProfiles() {
	dme.mu.Lock()
	defer dme.mu.Unlock()
	
	// Reset to default profiles
	dme.profiles = map[string]MemoryProfile{
		"tiny": {
			SizeRange:     "tiny",
			MinSize:       0,
			MaxSize:       1024 * 1024,
			BaseMemory:    1,
			PerMBMemory:   0.5,
			WorkerOverhead: 1,
			LastUpdated:   time.Now(),
			SampleCount:   0,
		},
		"small": {
			SizeRange:     "small",
			MinSize:       1024 * 1024,
			MaxSize:       10 * 1024 * 1024,
			BaseMemory:    2,
			PerMBMemory:   0.3,
			WorkerOverhead: 2,
			LastUpdated:   time.Now(),
			SampleCount:   0,
		},
		"medium": {
			SizeRange:     "medium",
			MinSize:       10 * 1024 * 1024,
			MaxSize:       100 * 1024 * 1024,
			BaseMemory:    5,
			PerMBMemory:   0.2,
			WorkerOverhead: 5,
			LastUpdated:   time.Now(),
			SampleCount:   0,
		},
		"large": {
			SizeRange:     "large",
			MinSize:       100 * 1024 * 1024,
			MaxSize:       1024 * 1024 * 1024,
			BaseMemory:    10,
			PerMBMemory:   0.1,
			WorkerOverhead: 10,
			LastUpdated:   time.Now(),
			SampleCount:   0,
		},
		"xlarge": {
			SizeRange:     "xlarge",
			MinSize:       1024 * 1024 * 1024,
			MaxSize:       10 * 1024 * 1024 * 1024,
			BaseMemory:    20,
			PerMBMemory:   0.05,
			WorkerOverhead: 20,
			LastUpdated:   time.Now(),
			SampleCount:   0,
		},
	}
}

// CalculateMemoryEfficiency calculates the memory efficiency of the current configuration
func (dme *DynamicMemoryEstimator) CalculateMemoryEfficiency(objectSize int64, actualMemory int64, workerCount int) float64 {
	estimatedMemory := dme.EstimateMemoryPerWorker(objectSize) * int64(workerCount)
	
	if estimatedMemory == 0 {
		return 0
	}
	
	// Efficiency is how close actual memory is to estimated memory
	efficiency := float64(estimatedMemory) / float64(actualMemory)
	
	// Clamp between 0.5 and 2.0 for reasonable bounds
	if efficiency < 0.5 {
		efficiency = 0.5
	} else if efficiency > 2.0 {
		efficiency = 2.0
	}
	
	return efficiency
}

// GetMemoryRecommendations returns recommendations for memory optimization
func (dme *DynamicMemoryEstimator) GetMemoryRecommendations(objectSize int64, currentMemory int64, workerCount int) []string {
	var recommendations []string
	
	efficiency := dme.CalculateMemoryEfficiency(objectSize, currentMemory, workerCount)
	
	if efficiency < 0.8 {
		recommendations = append(recommendations, "Memory usage is higher than estimated - consider reducing worker count")
	} else if efficiency > 1.2 {
		recommendations = append(recommendations, "Memory usage is lower than estimated - consider increasing worker count")
	}
	
	profile := dme.getProfileForSize(objectSize)
	if profile.SampleCount < 10 {
		recommendations = append(recommendations, "Insufficient data for accurate estimation - collect more samples")
	}
	
	networkQuality := dme.networkMonitor.GetCurrentCondition()
	if networkQuality == "poor" {
		recommendations = append(recommendations, "Poor network quality detected - consider reducing concurrency")
	}
	
	return recommendations
}
