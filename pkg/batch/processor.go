package batch

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Object represents a file to be copied
type Object struct {
	SourceBucket string
	SourceKey    string
	DestBucket   string
	DestKey      string
	Size         int64
}

// BatchConfig holds batch processing configuration
type BatchConfig struct {
	MaxBatchSize  int           // Maximum number of objects per batch
	MaxBatchBytes int64         // Maximum total bytes per batch
	FlushInterval time.Duration // Auto-flush interval
	WorkerCount   int           // Number of batch workers
}

// DefaultBatchConfig returns default configuration
func DefaultBatchConfig() BatchConfig {
	return BatchConfig{
		MaxBatchSize:  100,
		MaxBatchBytes: 10 * 1024 * 1024, // 10MB
		FlushInterval: 1 * time.Second,
		WorkerCount:   5,
	}
}

// Processor handles batch processing of small objects
type Processor struct {
	client    *s3.Client
	config    BatchConfig
	batches   chan *Batch
	results   chan Result
	wg        sync.WaitGroup
	stopChan  chan struct{}
	processed atomic.Int64
	failed    atomic.Int64
}

// Batch represents a group of objects to process together
type Batch struct {
	Objects   []Object
	TotalSize int64
	CreatedAt time.Time
}

// Result represents the result of processing a batch
type Result struct {
	Success bool
	Object  Object
	Error   error
}

// NewProcessor creates a new batch processor
func NewProcessor(client *s3.Client, config BatchConfig) *Processor {
	p := &Processor{
		client:   client,
		config:   config,
		batches:  make(chan *Batch, config.WorkerCount*2),
		results:  make(chan Result, config.WorkerCount*10),
		stopChan: make(chan struct{}),
	}

	// Start workers
	for i := 0; i < config.WorkerCount; i++ {
		p.wg.Add(1)
		go p.worker()
	}

	return p
}

// ProcessBatch submits a batch for processing
func (p *Processor) ProcessBatch(batch *Batch) {
	select {
	case p.batches <- batch:
	case <-p.stopChan:
	}
}

// worker processes batches
func (p *Processor) worker() {
	defer p.wg.Done()

	for {
		select {
		case batch := <-p.batches:
			p.processBatch(batch)
		case <-p.stopChan:
			return
		}
	}
}

func (p *Processor) processBatch(batch *Batch) {
	ctx := context.Background()

	// Process objects in batch concurrently with limited parallelism
	sem := make(chan struct{}, 10) // Limit to 10 concurrent ops per batch
	var batchWg sync.WaitGroup

	for _, obj := range batch.Objects {
		batchWg.Add(1)
		go func(o Object) {
			defer batchWg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			err := p.copyObject(ctx, o)

			result := Result{
				Success: err == nil,
				Object:  o,
				Error:   err,
			}

			if err == nil {
				p.processed.Add(1)
			} else {
				p.failed.Add(1)
			}

			select {
			case p.results <- result:
			case <-p.stopChan:
			}
		}(obj)
	}

	batchWg.Wait()
}

func (p *Processor) copyObject(ctx context.Context, obj Object) error {
	copySource := fmt.Sprintf("%s/%s", obj.SourceBucket, obj.SourceKey)

	_, err := p.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     &obj.DestBucket,
		Key:        &obj.DestKey,
		CopySource: &copySource,
	})

	return err
}

// Results returns the results channel
func (p *Processor) Results() <-chan Result {
	return p.results
}

// Stop stops the processor
func (p *Processor) Stop() {
	close(p.stopChan)
	p.wg.Wait()
	close(p.batches)
	close(p.results)
}

// Stats returns processing statistics
type Stats struct {
	Processed   int64
	Failed      int64
	SuccessRate float64
}

func (p *Processor) Stats() Stats {
	processed := p.processed.Load()
	failed := p.failed.Load()
	total := processed + failed

	successRate := 0.0
	if total > 0 {
		successRate = float64(processed) / float64(total) * 100
	}

	return Stats{
		Processed:   processed,
		Failed:      failed,
		SuccessRate: successRate,
	}
}

// BatchBuilder helps build batches efficiently
type BatchBuilder struct {
	mu           sync.Mutex
	currentBatch *Batch
	config       BatchConfig
	processor    *Processor
	autoFlush    *time.Timer
}

// NewBatchBuilder creates a new batch builder
func NewBatchBuilder(processor *Processor, config BatchConfig) *BatchBuilder {
	bb := &BatchBuilder{
		currentBatch: &Batch{
			Objects:   make([]Object, 0, config.MaxBatchSize),
			CreatedAt: time.Now(),
		},
		config:    config,
		processor: processor,
	}

	// Start auto-flush timer
	bb.autoFlush = time.AfterFunc(config.FlushInterval, func() {
		bb.Flush()
		bb.autoFlush.Reset(config.FlushInterval)
	})

	return bb
}

// Add adds an object to the current batch
func (bb *BatchBuilder) Add(obj Object) {
	bb.mu.Lock()
	defer bb.mu.Unlock()

	// Check if adding this object would exceed limits
	if len(bb.currentBatch.Objects) >= bb.config.MaxBatchSize ||
		bb.currentBatch.TotalSize+obj.Size > bb.config.MaxBatchBytes {
		// Flush current batch
		bb.flushUnlocked()
	}

	// Add to current batch
	bb.currentBatch.Objects = append(bb.currentBatch.Objects, obj)
	bb.currentBatch.TotalSize += obj.Size
}

// Flush flushes the current batch
func (bb *BatchBuilder) Flush() {
	bb.mu.Lock()
	defer bb.mu.Unlock()
	bb.flushUnlocked()
}

func (bb *BatchBuilder) flushUnlocked() {
	if len(bb.currentBatch.Objects) == 0 {
		return
	}

	// Submit current batch
	bb.processor.ProcessBatch(bb.currentBatch)

	// Create new batch
	bb.currentBatch = &Batch{
		Objects:   make([]Object, 0, bb.config.MaxBatchSize),
		CreatedAt: time.Now(),
	}
}

// Stop stops the batch builder
func (bb *BatchBuilder) Stop() {
	bb.autoFlush.Stop()
	bb.Flush()
}
