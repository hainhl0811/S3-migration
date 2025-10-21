package pool

import (
	"context"
	"sync"
	"sync/atomic"
)

// Task represents a unit of work
type Task func(ctx context.Context) error

// WorkerPool manages a pool of workers
type WorkerPool struct {
	workers     int
	tasks       chan Task
	results     chan error
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	activeCount atomic.Int32
	totalTasks  atomic.Int64
	failedTasks atomic.Int64
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(ctx context.Context, workers int) *WorkerPool {
	poolCtx, cancel := context.WithCancel(ctx)

	wp := &WorkerPool{
		workers: workers,
		tasks:   make(chan Task, workers*2),
		results: make(chan error, workers*2),
		ctx:     poolCtx,
		cancel:  cancel,
	}

	// Start workers
	for i := 0; i < workers; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}

	return wp
}

// worker processes tasks from the queue
func (wp *WorkerPool) worker() {
	defer wp.wg.Done()

	for {
		select {
		case task, ok := <-wp.tasks:
			if !ok {
				return
			}

			wp.activeCount.Add(1)
			wp.totalTasks.Add(1)

			err := task(wp.ctx)

			if err != nil {
				wp.failedTasks.Add(1)
			}

			wp.activeCount.Add(-1)

			select {
			case wp.results <- err:
			case <-wp.ctx.Done():
				return
			}

		case <-wp.ctx.Done():
			return
		}
	}
}

// Submit submits a task to the pool
func (wp *WorkerPool) Submit(task Task) bool {
	select {
	case wp.tasks <- task:
		return true
	case <-wp.ctx.Done():
		return false
	}
}

// Results returns the results channel
func (wp *WorkerPool) Results() <-chan error {
	return wp.results
}

// Stop stops the worker pool gracefully
func (wp *WorkerPool) Stop() {
	close(wp.tasks)
	wp.wg.Wait()
	close(wp.results)
}

// Shutdown cancels all workers immediately
func (wp *WorkerPool) Shutdown() {
	wp.cancel()
	wp.wg.Wait()
	close(wp.results)
}

// ActiveWorkers returns the number of currently active workers
func (wp *WorkerPool) ActiveWorkers() int32 {
	return wp.activeCount.Load()
}

// WorkerPoolStats contains worker pool statistics
type WorkerPoolStats struct {
	TotalWorkers  int
	ActiveWorkers int32
	TotalTasks    int64
	FailedTasks   int64
	SuccessRate   float64
}

// Stats returns pool statistics
func (wp *WorkerPool) Stats() WorkerPoolStats {
	total := wp.totalTasks.Load()
	failed := wp.failedTasks.Load()

	successRate := 0.0
	if total > 0 {
		successRate = float64(total-failed) / float64(total) * 100
	}

	return WorkerPoolStats{
		TotalWorkers:  wp.workers,
		ActiveWorkers: wp.activeCount.Load(),
		TotalTasks:    total,
		FailedTasks:   failed,
		SuccessRate:   successRate,
	}
}

// DynamicWorkerPool is a worker pool that can adjust its size
type DynamicWorkerPool struct {
	mu          sync.RWMutex
	minWorkers  int
	maxWorkers  int
	workers     []*WorkerPool
	currentSize int
	tasks       chan Task
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewDynamicWorkerPool creates a dynamic worker pool
func NewDynamicWorkerPool(ctx context.Context, minWorkers, maxWorkers int) *DynamicWorkerPool {
	poolCtx, cancel := context.WithCancel(ctx)

	dwp := &DynamicWorkerPool{
		minWorkers:  minWorkers,
		maxWorkers:  maxWorkers,
		currentSize: minWorkers,
		tasks:       make(chan Task, maxWorkers*2),
		ctx:         poolCtx,
		cancel:      cancel,
	}

	// Start with minimum workers
	dwp.workers = make([]*WorkerPool, 0, maxWorkers)
	for i := 0; i < minWorkers; i++ {
		pool := NewWorkerPool(poolCtx, 1)
		dwp.workers = append(dwp.workers, pool)
	}

	// Start task distributor
	go dwp.distribute()

	return dwp
}

func (dwp *DynamicWorkerPool) distribute() {
	for {
		select {
		case task, ok := <-dwp.tasks:
			if !ok {
				return
			}

			// Find a worker with capacity
			submitted := false
			dwp.mu.RLock()
			for _, worker := range dwp.workers {
				if worker.Submit(task) {
					submitted = true
					break
				}
			}
			dwp.mu.RUnlock()

			// All workers busy, try to expand
			if !submitted && dwp.expandIfNeeded() {
				// Retry submission
				dwp.mu.RLock()
				for _, worker := range dwp.workers {
					if worker.Submit(task) {
						dwp.mu.RUnlock()
						break
					}
				}
				dwp.mu.RUnlock()
			}

		case <-dwp.ctx.Done():
			return
		}
	}
}

func (dwp *DynamicWorkerPool) expandIfNeeded() bool {
	dwp.mu.Lock()
	defer dwp.mu.Unlock()

	if dwp.currentSize >= dwp.maxWorkers {
		return false
	}

	// Add a new worker
	pool := NewWorkerPool(dwp.ctx, 1)
	dwp.workers = append(dwp.workers, pool)
	dwp.currentSize++

	return true
}

// Submit submits a task to the pool
func (dwp *DynamicWorkerPool) Submit(task Task) bool {
	select {
	case dwp.tasks <- task:
		return true
	case <-dwp.ctx.Done():
		return false
	}
}

// Stop stops all workers
func (dwp *DynamicWorkerPool) Stop() {
	close(dwp.tasks)

	dwp.mu.RLock()
	for _, worker := range dwp.workers {
		worker.Stop()
	}
	dwp.mu.RUnlock()
}

// GetSize returns current pool size
func (dwp *DynamicWorkerPool) GetSize() int {
	dwp.mu.RLock()
	defer dwp.mu.RUnlock()
	return dwp.currentSize
}

// GetActiveWorkers returns total active workers
func (dwp *DynamicWorkerPool) GetActiveWorkers() int32 {
	dwp.mu.RLock()
	defer dwp.mu.RUnlock()

	var total int32
	for _, worker := range dwp.workers {
		total += worker.ActiveWorkers()
	}
	return total
}
