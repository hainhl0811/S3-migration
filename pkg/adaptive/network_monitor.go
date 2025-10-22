package adaptive

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// NetworkMonitor monitors network quality and provides adaptive recommendations
// Inspired by rclone's network adaptation
type NetworkMonitor struct {
	// Network quality metrics
	latency     time.Duration
	throughput  float64 // MB/s
	errorRate   float64 // 0.0 to 1.0
	lastUpdate  time.Time
	
	// Quality thresholds
	excellentThreshold time.Duration // < 50ms
	goodThreshold      time.Duration // < 100ms
	fairThreshold      time.Duration // < 500ms
	// > 500ms is poor
	
	mu sync.RWMutex
}

// NewNetworkMonitor creates a new network monitor
func NewNetworkMonitor() *NetworkMonitor {
	return &NetworkMonitor{
		excellentThreshold: 50 * time.Millisecond,
		goodThreshold:      100 * time.Millisecond,
		fairThreshold:      500 * time.Millisecond,
	}
}

// GetCurrentCondition returns the current network condition
func (nm *NetworkMonitor) GetCurrentCondition() string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	
	if nm.latency < nm.excellentThreshold {
		return "excellent"
	} else if nm.latency < nm.goodThreshold {
		return "good"
	} else if nm.latency < nm.fairThreshold {
		return "fair"
	} else {
		return "poor"
	}
}

// GetQuality returns the network quality (same as GetCurrentCondition)
func (nm *NetworkMonitor) GetQuality() string {
	return nm.GetCurrentCondition()
}

// UpdateMetrics updates network metrics
func (nm *NetworkMonitor) UpdateMetrics(latency time.Duration, throughput float64, errorRate float64) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	
	nm.latency = latency
	nm.throughput = throughput
	nm.errorRate = errorRate
	nm.lastUpdate = time.Now()
}

// GetLatency returns current latency
func (nm *NetworkMonitor) GetLatency() time.Duration {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.latency
}

// GetThroughput returns current throughput in MB/s
func (nm *NetworkMonitor) GetThroughput() float64 {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.throughput
}

// GetErrorRate returns current error rate (0.0 to 1.0)
func (nm *NetworkMonitor) GetErrorRate() float64 {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.errorRate
}

// IsStale returns true if the metrics are stale (older than 30 seconds)
func (nm *NetworkMonitor) IsStale() bool {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return time.Since(nm.lastUpdate) > 30*time.Second
}

// TestNetworkQuality performs a network quality test
func (nm *NetworkMonitor) TestNetworkQuality(ctx context.Context, testURL string) error {
	// Simple latency test
	start := time.Now()
	resp, err := http.Get(testURL)
	if err != nil {
		return fmt.Errorf("network test failed: %w", err)
	}
	defer resp.Body.Close()
	
	latency := time.Since(start)
	
	// Calculate throughput (simplified)
	contentLength := resp.ContentLength
	if contentLength > 0 {
		throughput := float64(contentLength) / latency.Seconds() / (1024 * 1024) // MB/s
		nm.UpdateMetrics(latency, throughput, 0.0)
	} else {
		nm.UpdateMetrics(latency, 0.0, 0.0)
	}
	
	return nil
}

// GetOptimalConcurrency returns optimal concurrency based on network quality
func (nm *NetworkMonitor) GetOptimalConcurrency(baseConcurrency int) int {
	condition := nm.GetCurrentCondition()
	
	switch condition {
	case "excellent":
		return baseConcurrency * 2 // Double concurrency for excellent network
	case "good":
		return int(float64(baseConcurrency) * 1.5) // 50% more for good network
	case "fair":
		return baseConcurrency // No change for fair network
	case "poor":
		return baseConcurrency / 2 // Half concurrency for poor network
	default:
		return baseConcurrency
	}
}

// GetOptimalChunkSize returns optimal chunk size based on network quality
func (nm *NetworkMonitor) GetOptimalChunkSize(baseChunkSize int64) int64 {
	condition := nm.GetCurrentCondition()
	
	switch condition {
	case "excellent":
		return baseChunkSize * 2 // Larger chunks for excellent network
	case "good":
		return int64(float64(baseChunkSize) * 1.5) // 50% larger for good network
	case "fair":
		return baseChunkSize // No change for fair network
	case "poor":
		return baseChunkSize / 2 // Smaller chunks for poor network
	default:
		return baseChunkSize
	}
}

// GetRetryDelay returns retry delay based on network quality
func (nm *NetworkMonitor) GetRetryDelay(baseDelay time.Duration) time.Duration {
	condition := nm.GetCurrentCondition()
	
	switch condition {
	case "excellent":
		return baseDelay / 2 // Faster retries for excellent network
	case "good":
		return baseDelay // Normal retries for good network
	case "fair":
		return baseDelay * 2 // Slower retries for fair network
	case "poor":
		return baseDelay * 4 // Much slower retries for poor network
	default:
		return baseDelay
	}
}

// GetRecommendations returns network optimization recommendations
func (nm *NetworkMonitor) GetRecommendations() []string {
	var recommendations []string
	
	condition := nm.GetCurrentCondition()
	
	switch condition {
	case "excellent":
		recommendations = append(recommendations, "Network quality is excellent - can use maximum concurrency")
		recommendations = append(recommendations, "Consider using larger chunk sizes for better throughput")
	case "good":
		recommendations = append(recommendations, "Network quality is good - can use high concurrency")
		recommendations = append(recommendations, "Consider using larger chunk sizes")
	case "fair":
		recommendations = append(recommendations, "Network quality is fair - use moderate concurrency")
		recommendations = append(recommendations, "Consider using standard chunk sizes")
	case "poor":
		recommendations = append(recommendations, "Network quality is poor - reduce concurrency")
		recommendations = append(recommendations, "Consider using smaller chunk sizes")
		recommendations = append(recommendations, "Consider implementing retry with exponential backoff")
	default:
		recommendations = append(recommendations, "Network quality unknown - use conservative settings")
	}
	
	if nm.IsStale() {
		recommendations = append(recommendations, "Network metrics are stale - consider running network test")
	}
	
	return recommendations
}
