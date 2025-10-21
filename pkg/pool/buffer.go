package pool

import (
	"sync"
)

// BufferPool manages a pool of reusable byte buffers
type BufferPool struct {
	pool      sync.Pool
	size      int
	maxAlloc  int64
	allocated int64
	mu        sync.Mutex
}

// NewBufferPool creates a new buffer pool
func NewBufferPool(bufferSize int, maxAlloc int64) *BufferPool {
	return &BufferPool{
		size:     bufferSize,
		maxAlloc: maxAlloc,
		pool: sync.Pool{
			New: func() interface{} {
				return make([]byte, bufferSize)
			},
		},
	}
}

// Get retrieves a buffer from the pool
func (bp *BufferPool) Get() []byte {
	bp.mu.Lock()
	if bp.maxAlloc > 0 && bp.allocated >= bp.maxAlloc {
		bp.mu.Unlock()
		// Return a new buffer without tracking if we're at limit
		return make([]byte, bp.size)
	}
	bp.allocated += int64(bp.size)
	bp.mu.Unlock()

	buf := bp.pool.Get().([]byte)
	return buf[:bp.size] // Reset capacity
}

// Put returns a buffer to the pool
func (bp *BufferPool) Put(buf []byte) {
	if buf == nil || cap(buf) != bp.size {
		return
	}

	bp.mu.Lock()
	bp.allocated -= int64(bp.size)
	bp.mu.Unlock()

	// Only clear sensitive data if needed, otherwise skip for performance
	// Most use cases don't need clearing for S3 transfers
	// Uncomment below if security requirements mandate clearing
	// for i := range buf {
	// 	buf[i] = 0
	// }

	bp.pool.Put(buf[:cap(buf)]) // Reset to full capacity
}

// Stats returns pool statistics
type BufferPoolStats struct {
	Size      int
	Allocated int64
	MaxAlloc  int64
}

func (bp *BufferPool) Stats() BufferPoolStats {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	return BufferPoolStats{
		Size:      bp.size,
		Allocated: bp.allocated,
		MaxAlloc:  bp.maxAlloc,
	}
}

// MultiSizeBufferPool manages multiple buffer pools of different sizes
type MultiSizeBufferPool struct {
	pools map[int]*BufferPool
	mu    sync.RWMutex
}

// NewMultiSizeBufferPool creates a pool with multiple buffer sizes
func NewMultiSizeBufferPool(sizes []int, maxAllocPerSize int64) *MultiSizeBufferPool {
	mp := &MultiSizeBufferPool{
		pools: make(map[int]*BufferPool),
	}

	for _, size := range sizes {
		mp.pools[size] = NewBufferPool(size, maxAllocPerSize)
	}

	return mp
}

// Get retrieves a buffer of the appropriate size
func (mp *MultiSizeBufferPool) Get(size int) []byte {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	// Find the smallest pool that can accommodate the size
	bestSize := -1
	for poolSize := range mp.pools {
		if poolSize >= size {
			if bestSize == -1 || poolSize < bestSize {
				bestSize = poolSize
			}
		}
	}

	if bestSize != -1 {
		return mp.pools[bestSize].Get()[:size]
	}

	// No suitable pool found, allocate directly
	return make([]byte, size)
}

// Put returns a buffer to the appropriate pool
func (mp *MultiSizeBufferPool) Put(buf []byte) {
	if buf == nil {
		return
	}

	size := cap(buf)
	mp.mu.RLock()
	pool, exists := mp.pools[size]
	mp.mu.RUnlock()

	if exists {
		pool.Put(buf)
	}
	// If pool doesn't exist, let GC handle it
}

// GetAllStats returns statistics for all pools
func (mp *MultiSizeBufferPool) GetAllStats() map[int]BufferPoolStats {
	mp.mu.RLock()
	defer mp.mu.RUnlock()

	stats := make(map[int]BufferPoolStats)
	for size, pool := range mp.pools {
		stats[size] = pool.Stats()
	}
	return stats
}
