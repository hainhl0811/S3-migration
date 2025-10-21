package structures

import (
	"errors"
	"sync"
	"sync/atomic"
)

var (
	ErrBufferFull  = errors.New("ring buffer is full")
	ErrBufferEmpty = errors.New("ring buffer is empty")
)

// RingBuffer is a lock-free ring buffer for efficient data passing
type RingBuffer struct {
	buffer []interface{}
	size   uint64
	mask   uint64
	head   atomic.Uint64
	tail   atomic.Uint64
}

// NewRingBuffer creates a new ring buffer (size must be power of 2)
func NewRingBuffer(size uint64) *RingBuffer {
	if size&(size-1) != 0 {
		// Round up to next power of 2
		size = nextPowerOf2(size)
	}

	return &RingBuffer{
		buffer: make([]interface{}, size),
		size:   size,
		mask:   size - 1,
	}
}

func nextPowerOf2(n uint64) uint64 {
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	n++
	return n
}

// Push adds an item to the buffer
func (rb *RingBuffer) Push(item interface{}) error {
	for {
		head := rb.head.Load()
		tail := rb.tail.Load()

		if (tail+1)&rb.mask == head&rb.mask {
			return ErrBufferFull
		}

		if rb.tail.CompareAndSwap(tail, tail+1) {
			rb.buffer[tail&rb.mask] = item
			return nil
		}
	}
}

// Pop removes and returns an item from the buffer
func (rb *RingBuffer) Pop() (interface{}, error) {
	for {
		head := rb.head.Load()
		tail := rb.tail.Load()

		if head == tail {
			return nil, ErrBufferEmpty
		}

		item := rb.buffer[head&rb.mask]
		if rb.head.CompareAndSwap(head, head+1) {
			return item, nil
		}
	}
}

// Len returns the current number of items in the buffer
func (rb *RingBuffer) Len() uint64 {
	head := rb.head.Load()
	tail := rb.tail.Load()
	return (tail - head) & rb.mask
}

// Cap returns the capacity of the buffer
func (rb *RingBuffer) Cap() uint64 {
	return rb.size
}

// ObjectPool is a type-safe object pool
type ObjectPool[T any] struct {
	pool sync.Pool
	new  func() T
}

// NewObjectPool creates a new typed object pool
func NewObjectPool[T any](newFunc func() T) *ObjectPool[T] {
	return &ObjectPool[T]{
		pool: sync.Pool{
			New: func() interface{} {
				return newFunc()
			},
		},
		new: newFunc,
	}
}

// Get retrieves an object from the pool
func (op *ObjectPool[T]) Get() T {
	return op.pool.Get().(T)
}

// Put returns an object to the pool
func (op *ObjectPool[T]) Put(obj T) {
	op.pool.Put(obj)
}

// SlicePool manages pools of slices of different capacities
type SlicePool struct {
	pools map[int]*sync.Pool
	mu    sync.RWMutex
}

// NewSlicePool creates a new slice pool
func NewSlicePool() *SlicePool {
	return &SlicePool{
		pools: make(map[int]*sync.Pool),
	}
}

// GetSlice gets a slice with at least the requested capacity
func (sp *SlicePool) GetSlice(capacity int) []byte {
	// Round up to nearest power of 2
	size := int(nextPowerOf2(uint64(capacity)))

	sp.mu.RLock()
	pool, exists := sp.pools[size]
	sp.mu.RUnlock()

	if !exists {
		sp.mu.Lock()
		// Double-check
		pool, exists = sp.pools[size]
		if !exists {
			pool = &sync.Pool{
				New: func() interface{} {
					return make([]byte, size)
				},
			}
			sp.pools[size] = pool
		}
		sp.mu.Unlock()
	}

	slice := pool.Get().([]byte)
	return slice[:capacity] // Return slice with requested capacity
}

// PutSlice returns a slice to the pool
func (sp *SlicePool) PutSlice(slice []byte) {
	capacity := cap(slice)
	size := int(nextPowerOf2(uint64(capacity)))

	sp.mu.RLock()
	pool, exists := sp.pools[size]
	sp.mu.RUnlock()

	if exists {
		// Clear the slice for security (optional)
		for i := range slice {
			slice[i] = 0
		}
		pool.Put(slice[:size]) // Reset to full capacity
	}
}

// CompactMap is a memory-efficient map for string keys
type CompactMap struct {
	mu    sync.RWMutex
	data  map[uint64]interface{}
	keys  []string
	limit int
}

// NewCompactMap creates a new compact map with size limit
func NewCompactMap(limit int) *CompactMap {
	return &CompactMap{
		data:  make(map[uint64]interface{}),
		keys:  make([]string, 0, limit),
		limit: limit,
	}
}

// Set stores a value for a key
func (cm *CompactMap) Set(key string, value interface{}) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	hash := hashKey(key)

	// Check if we need to evict
	if len(cm.data) >= cm.limit {
		if _, exists := cm.data[hash]; !exists {
			// Remove oldest entry
			if len(cm.keys) > 0 {
				oldKey := cm.keys[0]
				oldHash := hashKey(oldKey)
				delete(cm.data, oldHash)
				cm.keys = cm.keys[1:]
			}
		}
	}

	cm.data[hash] = value
	cm.keys = append(cm.keys, key)
}

// Get retrieves a value for a key
func (cm *CompactMap) Get(key string) (interface{}, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	hash := hashKey(key)
	value, exists := cm.data[hash]
	return value, exists
}

// Len returns the number of entries
func (cm *CompactMap) Len() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.data)
}

// hashKey creates a hash for a string key
func hashKey(s string) uint64 {
	h := uint64(5381)
	for i := 0; i < len(s); i++ {
		h = ((h << 5) + h) + uint64(s[i])
	}
	return h
}

// Stats returns statistics about the map
func (cm *CompactMap) Stats() struct {
	Size       int
	Limit      int
	LoadFactor float64
} {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	return struct {
		Size       int
		Limit      int
		LoadFactor float64
	}{
		Size:       len(cm.data),
		Limit:      cm.limit,
		LoadFactor: float64(len(cm.data)) / float64(cm.limit),
	}
}
