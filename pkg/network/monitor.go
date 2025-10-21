package network

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"s3migration/pkg/models"
)

// Monitor monitors network conditions
type Monitor struct {
	mu               sync.RWMutex
	speedSamples     []float64
	latencySamples   []float64
	errorCounts      map[string]int
	lastCheck        time.Time
	checkInterval    time.Duration
	maxSamples       int
	currentCondition models.NetworkCondition
}

// NewMonitor creates a new network monitor
func NewMonitor() *Monitor {
	m := &Monitor{
		speedSamples:     make([]float64, 0),
		latencySamples:   make([]float64, 0),
		errorCounts:      make(map[string]int),
		checkInterval:    30 * time.Second,
		maxSamples:       50,
		currentCondition: models.NetworkFair,
	}

	// Initial network check
	go m.CheckNetworkCondition(context.Background())

	return m
}

// CheckNetworkCondition checks current network conditions
func (m *Monitor) CheckNetworkCondition(ctx context.Context) models.NetworkCondition {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if now.Sub(m.lastCheck) < m.checkInterval {
		return m.currentCondition
	}

	// Test latency to S3
	start := time.Now()
	_, err := net.LookupHost("s3.amazonaws.com")
	latency := time.Since(start).Seconds()

	if err == nil {
		m.latencySamples = append(m.latencySamples, latency)
		if len(m.latencySamples) > m.maxSamples {
			m.latencySamples = m.latencySamples[1:]
		}
	}

	// Test download speed
	go m.testDownloadSpeed(ctx)

	m.lastCheck = now
	m.currentCondition = m.classifyCondition()

	return m.currentCondition
}

func (m *Monitor) testDownloadSpeed(ctx context.Context) {
	testURL := "https://aws.amazon.com/robots.txt"

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		m.mu.Lock()
		m.errorCounts["connection"]++
		m.mu.Unlock()
		return
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	downloadTime := time.Since(start).Seconds()
	if downloadTime > 0 {
		speedMBps := float64(len(data)) / downloadTime / (1024 * 1024)

		m.mu.Lock()
		m.speedSamples = append(m.speedSamples, speedMBps)
		if len(m.speedSamples) > m.maxSamples {
			m.speedSamples = m.speedSamples[1:]
		}
		m.mu.Unlock()
	}
}

func (m *Monitor) classifyCondition() models.NetworkCondition {
	if len(m.speedSamples) == 0 {
		return models.NetworkFair
	}

	// Calculate average speed
	var sum float64
	for _, speed := range m.speedSamples {
		sum += speed
	}
	avgSpeed := sum / float64(len(m.speedSamples))

	// Classify based on thresholds
	switch {
	case avgSpeed >= 50.0:
		return models.NetworkExcellent
	case avgSpeed >= 20.0:
		return models.NetworkGood
	case avgSpeed >= 5.0:
		return models.NetworkFair
	default:
		return models.NetworkPoor
	}
}

// GetRecommendedChunkSize returns recommended chunk size based on network
func (m *Monitor) GetRecommendedChunkSize() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	baseChunk := int64(8 * 1024 * 1024) // 8MB

	switch m.currentCondition {
	case models.NetworkExcellent:
		return baseChunk * 4 // 32MB
	case models.NetworkGood:
		return baseChunk * 2 // 16MB
	case models.NetworkFair:
		return baseChunk // 8MB
	default:
		return baseChunk / 2 // 4MB
	}
}

// GetRecommendedWorkers returns recommended number of workers
func (m *Monitor) GetRecommendedWorkers(currentWorkers int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	switch m.currentCondition {
	case models.NetworkPoor:
		return max(3, currentWorkers/2)
	case models.NetworkFair:
		return max(5, int(float64(currentWorkers)*0.7))
	case models.NetworkExcellent:
		return min(currentWorkers*2, 100)
	default: // NetworkGood
		return currentWorkers
	}
}

// ShouldPause determines if transfers should be paused
func (m *Monitor) ShouldPause() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.errorCounts["timeout"] > 5 ||
		m.errorCounts["connection"] > 5 ||
		m.errorCounts["other"] > 10
}

// GetCurrentCondition returns the current network condition
func (m *Monitor) GetCurrentCondition() models.NetworkCondition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentCondition
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
