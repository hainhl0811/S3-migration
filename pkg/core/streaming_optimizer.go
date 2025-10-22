package core

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"s3migration/pkg/integrity"
)

// StreamingOptimizer implements rclone-inspired optimizations for streaming transfers
type StreamingOptimizer struct {
	// Connection pooling for multipart uploads
	connectionPool chan *s3.Client
	// Parallel chunk processing
	chunkWorkers int
	// Chunk size optimization based on object size
	chunkSize int64
	// Retry configuration
	maxRetries int
	retryDelay time.Duration
}

// ChunkInfo represents a multipart upload chunk
type ChunkInfo struct {
	PartNumber int
	Offset     int64
	Size       int64
	Data       io.Reader
}

// MultipartUploadResult contains the result of a multipart upload
type MultipartUploadResult struct {
	UploadID string
	ETag     string
	Parts    []s3.CompletedPart
}

// NewStreamingOptimizer creates a new streaming optimizer
func NewStreamingOptimizer(connectionPool chan *s3.Client, chunkWorkers int) *StreamingOptimizer {
	return &StreamingOptimizer{
		connectionPool: connectionPool,
		chunkWorkers:   chunkWorkers,
		chunkSize:      5 * 1024 * 1024, // 5MB default chunk size
		maxRetries:     5,
		retryDelay:     time.Second,
	}
}

// OptimizeChunkSize calculates optimal chunk size based on object size
// Based on rclone's chunk size optimization
func (so *StreamingOptimizer) OptimizeChunkSize(objectSize int64) int64 {
	switch {
	case objectSize < 100*1024*1024: // < 100MB
		return 5 * 1024 * 1024 // 5MB chunks
	case objectSize < 1024*1024*1024: // < 1GB
		return 10 * 1024 * 1024 // 10MB chunks
	case objectSize < 10*1024*1024*1024: // < 10GB
		return 25 * 1024 * 1024 // 25MB chunks
	default:
		return 50 * 1024 * 1024 // 50MB chunks for very large files
	}
}

// StreamingMultipartUpload performs optimized multipart upload with parallel chunk processing
// Inspired by rclone's multithreaded approach but adapted for streaming
func (so *StreamingOptimizer) StreamingMultipartUpload(
	ctx context.Context,
	client *s3.Client,
	bucket, key string,
	objectSize int64,
	bodyReader io.Reader,
	hasher *integrity.StreamingHasher,
) (*MultipartUploadResult, error) {
	
	// Optimize chunk size based on object size
	chunkSize := so.OptimizeChunkSize(objectSize)
	
	// Create multipart upload
	createResp, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart upload: %w", err)
	}
	
	uploadID := aws.ToString(createResp.UploadID)
	
	// Calculate number of parts
	numParts := (objectSize + chunkSize - 1) / chunkSize
	
	// Channel for chunk processing results
	chunkResults := make(chan ChunkResult, numParts)
	
	// Channel for chunk work
	chunkWork := make(chan ChunkInfo, numParts)
	
	// Start parallel chunk workers (inspired by rclone's multithreading)
	var wg sync.WaitGroup
	for i := 0; i < so.chunkWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			so.processChunkWorker(ctx, client, bucket, key, uploadID, chunkWork, chunkResults)
		}()
	}
	
	// Stream data and create chunks
	go func() {
		defer close(chunkWork)
		so.streamAndCreateChunks(bodyReader, objectSize, chunkSize, chunkWork, hasher)
	}()
	
	// Wait for all workers to complete
	go func() {
		wg.Wait()
		close(chunkResults)
	}()
	
	// Collect results
	var completedParts []s3.CompletedPart
	var lastError error
	
	for result := range chunkResults {
		if result.Error != nil {
			lastError = result.Error
			continue
		}
		completedParts = append(completedParts, result.CompletedPart)
	}
	
	if lastError != nil {
		// Abort multipart upload on error
		client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(bucket),
			Key:      aws.String(key),
			UploadId: aws.String(uploadID),
		})
		return nil, lastError
	}
	
	// Complete multipart upload
	completeResp, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
		MultipartUpload: &s3.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to complete multipart upload: %w", err)
	}
	
	return &MultipartUploadResult{
		UploadID: uploadID,
		ETag:     aws.ToString(completeResp.ETag),
		Parts:    completedParts,
	}, nil
}

// ChunkResult represents the result of processing a chunk
type ChunkResult struct {
	CompletedPart s3.CompletedPart
	Error         error
}

// streamAndCreateChunks streams data and creates chunks for parallel processing
// This is the key optimization - streaming while creating chunks
func (so *StreamingOptimizer) streamAndCreateChunks(
	bodyReader io.Reader,
	objectSize int64,
	chunkSize int64,
	chunkWork chan<- ChunkInfo,
	hasher *integrity.StreamingHasher,
) {
	partNumber := 1
	offset := int64(0)
	
	// Create a limited reader for each chunk
	for offset < objectSize {
		remainingSize := objectSize - offset
		currentChunkSize := chunkSize
		if remainingSize < chunkSize {
			currentChunkSize = remainingSize
		}
		
		// Create limited reader for this chunk
		chunkReader := io.LimitReader(bodyReader, currentChunkSize)
		
		// Send chunk for parallel processing
		chunkWork <- ChunkInfo{
			PartNumber: partNumber,
			Offset:     offset,
			Size:       currentChunkSize,
			Data:       chunkReader,
		}
		
		offset += currentChunkSize
		partNumber++
	}
}

// processChunkWorker processes chunks in parallel (rclone-inspired multithreading)
func (so *StreamingOptimizer) processChunkWorker(
	ctx context.Context,
	client *s3.Client,
	bucket, key, uploadID string,
	chunkWork <-chan ChunkInfo,
	results chan<- ChunkResult,
) {
	for chunk := range chunkWork {
		result := so.processChunk(ctx, client, bucket, key, uploadID, chunk)
		results <- result
	}
}

// processChunk processes a single chunk with retry logic
func (so *StreamingOptimizer) processChunk(
	ctx context.Context,
	client *s3.Client,
	bucket, key, uploadID string,
	chunk ChunkInfo,
) ChunkResult {
	
	var lastError error
	
	// Retry logic inspired by rclone's robust error handling
	for attempt := 0; attempt < so.maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(so.retryDelay * time.Duration(attempt))
		}
		
		// Upload part
		uploadResp, err := client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String(key),
			PartNumber: aws.Int32(int32(chunk.PartNumber)),
			UploadId:   aws.String(uploadID),
			Body:       chunk.Data,
		})
		
		if err != nil {
			lastError = err
			continue
		}
		
		// Success
		return ChunkResult{
			CompletedPart: s3.CompletedPart{
				ETag:       uploadResp.ETag,
				PartNumber: aws.Int32(int32(chunk.PartNumber)),
			},
			Error: nil,
		}
	}
	
	return ChunkResult{
		Error: fmt.Errorf("failed to upload part %d after %d attempts: %w", 
			chunk.PartNumber, so.maxRetries, lastError),
	}
}

// AdaptiveChunkSize calculates chunk size based on network conditions
// Inspired by rclone's adaptive behavior
func (so *StreamingOptimizer) AdaptiveChunkSize(objectSize int64, networkQuality string) int64 {
	baseChunkSize := so.OptimizeChunkSize(objectSize)
	
	switch networkQuality {
	case "excellent":
		return baseChunkSize * 2 // Larger chunks for good network
	case "good":
		return baseChunkSize
	case "fair":
		return baseChunkSize / 2 // Smaller chunks for poor network
	case "poor":
		return baseChunkSize / 4 // Much smaller chunks for very poor network
	default:
		return baseChunkSize
	}
}

// ResumeMultipartUpload resumes an interrupted multipart upload
// Based on rclone's resume functionality
func (so *StreamingOptimizer) ResumeMultipartUpload(
	ctx context.Context,
	client *s3.Client,
	bucket, key, uploadID string,
	existingParts []s3.CompletedPart,
) (*MultipartUploadResult, error) {
	
	// List existing parts
	listResp, err := client.ListParts(ctx, &s3.ListPartsInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list existing parts: %w", err)
	}
	
	// Create map of existing parts for quick lookup
	existingPartsMap := make(map[int32]bool)
	for _, part := range listResp.Parts {
		existingPartsMap[aws.ToInt32(part.PartNumber)] = true
	}
	
	// This would need to be implemented based on the specific resume logic
	// For now, return error indicating resume not fully implemented
	return nil, fmt.Errorf("resume functionality not fully implemented yet")
}

// GetOptimalWorkers calculates optimal number of workers based on object size
// Inspired by rclone's adaptive worker calculation
func (so *StreamingOptimizer) GetOptimalWorkers(objectSize int64) int {
	switch {
	case objectSize < 10*1024*1024: // < 10MB
		return 2 // Minimal workers for small objects
	case objectSize < 100*1024*1024: // < 100MB
		return 4 // Moderate workers for medium objects
	case objectSize < 1024*1024*1024: // < 1GB
		return 8 // More workers for large objects
	default:
		return 16 // Maximum workers for very large objects
	}
}
