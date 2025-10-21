package progress

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Tracker tracks migration progress
type Tracker struct {
	totalObjects   int64
	totalSize      int64
	copiedObjects  atomic.Int64
	copiedSize     atomic.Int64
	failedObjects  atomic.Int64
	startTime      time.Time
	lastUpdateTime time.Time
	transferSpeeds []float64
	mu             sync.RWMutex
}

// NewTracker creates a new progress tracker
func NewTracker(totalObjects int64, totalSize int64) *Tracker {
	return &Tracker{
		totalObjects:   totalObjects,
		totalSize:      totalSize,
		startTime:      time.Now(),
		lastUpdateTime: time.Now(),
		transferSpeeds: make([]float64, 0, 10),
	}
}

// Update updates progress with a new transfer result
func (t *Tracker) Update(objectSize int64, success bool) {
	now := time.Now()

	if success {
		t.copiedObjects.Add(1)
		t.copiedSize.Add(objectSize)
	} else {
		t.failedObjects.Add(1)
	}

	// Calculate transfer speed
	t.mu.Lock()
	elapsed := now.Sub(t.lastUpdateTime).Seconds()
	if elapsed > 0 && objectSize > 0 {
		speed := float64(objectSize) / elapsed
		t.transferSpeeds = append(t.transferSpeeds, speed)
		if len(t.transferSpeeds) > 10 {
			t.transferSpeeds = t.transferSpeeds[1:]
		}
	}
	t.lastUpdateTime = now
	t.mu.Unlock()
}

// Stats returns current progress statistics
type Stats struct {
	ProgressPct     float64
	CopiedObjects   int64
	TotalObjects    int64
	CopiedSizeMB    float64
	TotalSizeMB     float64
	FailedObjects   int64
	ElapsedTime     string
	TransferSpeedMB float64
	ETA             string
}

// GetStats returns current progress statistics
func (t *Tracker) GetStats() Stats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	copiedObjects := t.copiedObjects.Load()
	copiedSize := t.copiedSize.Load()
	failedObjects := t.failedObjects.Load()

	elapsed := time.Since(t.startTime)

	// Calculate average speed
	var avgSpeed float64
	if len(t.transferSpeeds) > 0 {
		var sum float64
		for _, speed := range t.transferSpeeds {
			sum += speed
		}
		avgSpeed = sum / float64(len(t.transferSpeeds))
	}

	// Calculate ETA
	remainingSize := t.totalSize - copiedSize
	var eta string
	if avgSpeed > 0 {
		etaSeconds := float64(remainingSize) / avgSpeed
		eta = time.Duration(etaSeconds * float64(time.Second)).String()
	} else {
		eta = "calculating..."
	}

	progressPct := 0.0
	if t.totalObjects > 0 {
		progressPct = float64(copiedObjects) / float64(t.totalObjects) * 100
	}

	return Stats{
		ProgressPct:     progressPct,
		CopiedObjects:   copiedObjects,
		TotalObjects:    t.totalObjects,
		CopiedSizeMB:    float64(copiedSize) / (1024 * 1024),
		TotalSizeMB:     float64(t.totalSize) / (1024 * 1024),
		FailedObjects:   failedObjects,
		ElapsedTime:     elapsed.String(),
		TransferSpeedMB: avgSpeed / (1024 * 1024),
		ETA:             eta,
	}
}

// FormatProgress formats current progress as a string
func (t *Tracker) FormatProgress() string {
	stats := t.GetStats()
	return fmt.Sprintf(
		"\rProgress: %.1f%% (%d/%d objects, %.1f/%.1f MB) | Speed: %.1f MB/s | ETA: %s | Failed: %d",
		stats.ProgressPct,
		stats.CopiedObjects,
		stats.TotalObjects,
		stats.CopiedSizeMB,
		stats.TotalSizeMB,
		stats.TransferSpeedMB,
		stats.ETA,
		stats.FailedObjects,
	)
}
