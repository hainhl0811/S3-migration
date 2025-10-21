package streaming

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3Types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"s3migration/pkg/pool"
)

// StreamConfig holds configuration for streaming operations
type StreamConfig struct {
	ChunkSize      int64
	MaxConcurrent  int
	BufferPoolSize int64
}

// DefaultStreamConfig returns default streaming configuration
func DefaultStreamConfig() StreamConfig {
	return StreamConfig{
		ChunkSize:      8 * 1024 * 1024, // 8MB chunks
		MaxConcurrent:  5,
		BufferPoolSize: 100 * 1024 * 1024, // 100MB max buffer pool
	}
}

// Streamer handles streaming large object transfers
type Streamer struct {
	client     *s3.Client
	bufferPool *pool.BufferPool
	config     StreamConfig
}

// NewStreamer creates a new streamer
func NewStreamer(client *s3.Client, config StreamConfig) *Streamer {
	return &Streamer{
		client:     client,
		bufferPool: pool.NewBufferPool(int(config.ChunkSize), config.BufferPoolSize),
		config:     config,
	}
}

// StreamCopyInput contains input parameters for stream copy
type StreamCopyInput struct {
	SourceBucket string
	SourceKey    string
	DestBucket   string
	DestKey      string
	ObjectSize   int64
}

// StreamCopyResult contains the result of a stream copy
type StreamCopyResult struct {
	BytesCopied int64
	PartCount   int
	Err         error
}

// StreamCopy performs a streaming copy for large objects
func (s *Streamer) StreamCopy(ctx context.Context, input StreamCopyInput) (*StreamCopyResult, error) {
	// For small objects, use regular copy
	if input.ObjectSize < s.config.ChunkSize {
		return s.simpleCopy(ctx, input)
	}

	// For large objects, use multipart upload
	return s.multipartCopy(ctx, input)
}

func (s *Streamer) simpleCopy(ctx context.Context, input StreamCopyInput) (*StreamCopyResult, error) {
	copySource := fmt.Sprintf("%s/%s", input.SourceBucket, input.SourceKey)

	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(input.DestBucket),
		Key:        aws.String(input.DestKey),
		CopySource: aws.String(copySource),
	})

	if err != nil {
		return nil, err
	}

	return &StreamCopyResult{
		BytesCopied: input.ObjectSize,
		PartCount:   1,
	}, nil
}

func (s *Streamer) multipartCopy(ctx context.Context, input StreamCopyInput) (*StreamCopyResult, error) {
	// Initiate multipart upload
	createResp, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(input.DestBucket),
		Key:    aws.String(input.DestKey),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initiate multipart upload: %w", err)
	}

	uploadID := *createResp.UploadId
	copySource := fmt.Sprintf("%s/%s", input.SourceBucket, input.SourceKey)

	// Calculate number of parts
	partCount := int((input.ObjectSize + s.config.ChunkSize - 1) / s.config.ChunkSize)

	// Upload parts concurrently
	type partResult struct {
		partNum int
		etag    string
		err     error
	}

	results := make(chan partResult, partCount)
	semaphore := make(chan struct{}, s.config.MaxConcurrent)
	var wg sync.WaitGroup

	for partNum := 1; partNum <= partCount; partNum++ {
		wg.Add(1)
		go func(pn int) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Calculate byte range for this part
			startByte := int64(pn-1) * s.config.ChunkSize
			endByte := startByte + s.config.ChunkSize - 1
			if endByte >= input.ObjectSize {
				endByte = input.ObjectSize - 1
			}

			copyRange := fmt.Sprintf("bytes=%d-%d", startByte, endByte)

			uploadResp, err := s.client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
				Bucket:          aws.String(input.DestBucket),
				Key:             aws.String(input.DestKey),
				CopySource:      aws.String(copySource),
				CopySourceRange: aws.String(copyRange),
				PartNumber:      aws.Int32(int32(pn)),
				UploadId:        aws.String(uploadID),
			})

			if err != nil {
				results <- partResult{partNum: pn, err: err}
				return
			}

			results <- partResult{
				partNum: pn,
				etag:    *uploadResp.CopyPartResult.ETag,
			}
		}(partNum)
	}

	// Wait for all parts to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	completedParts := make(map[int]string)
	var copyErr error

	for result := range results {
		if result.err != nil {
			copyErr = result.err
			break
		}
		completedParts[result.partNum] = result.etag
	}

	// If any part failed, abort the upload
	if copyErr != nil {
		s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(input.DestBucket),
			Key:      aws.String(input.DestKey),
			UploadId: aws.String(uploadID),
		})
		return nil, fmt.Errorf("multipart copy failed: %w", copyErr)
	}

	// Complete the multipart upload
	parts := make([]s3Types.CompletedPart, 0, len(completedParts))
	for i := 1; i <= partCount; i++ {
		parts = append(parts, s3Types.CompletedPart{
			ETag:       aws.String(completedParts[i]),
			PartNumber: aws.Int32(int32(i)),
		})
	}

	_, err = s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(input.DestBucket),
		Key:      aws.String(input.DestKey),
		UploadId: aws.String(uploadID),
		MultipartUpload: &s3Types.CompletedMultipartUpload{
			Parts: parts,
		},
	})

	if err != nil {
		return nil, fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	return &StreamCopyResult{
		BytesCopied: input.ObjectSize,
		PartCount:   partCount,
	}, nil
}

// StreamDownload streams an object download with buffering
func (s *Streamer) StreamDownload(ctx context.Context, bucket, key string, writer io.Writer) (int64, error) {
	resp, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	buffer := s.bufferPool.Get()
	defer s.bufferPool.Put(buffer)

	totalBytes := int64(0)
	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			written, writeErr := writer.Write(buffer[:n])
			totalBytes += int64(written)
			if writeErr != nil {
				return totalBytes, writeErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return totalBytes, err
		}
	}

	return totalBytes, nil
}

// GetBufferPoolStats returns buffer pool statistics
func (s *Streamer) GetBufferPoolStats() pool.BufferPoolStats {
	return s.bufferPool.Stats()
}
