package prefetch

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ObjectMetadata contains cached object metadata
type ObjectMetadata struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
	StorageClass string
	CachedAt     time.Time
}

// MetadataCache manages cached object metadata
type MetadataCache struct {
	mu        sync.RWMutex
	cache     map[string]*ObjectMetadata
	ttl       time.Duration
	maxSize   int
	hits      uint64
	misses    uint64
	evictions uint64
}

// NewMetadataCache creates a new metadata cache
func NewMetadataCache(ttl time.Duration, maxSize int) *MetadataCache {
	return &MetadataCache{
		cache:   make(map[string]*ObjectMetadata),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

// Get retrieves metadata from cache
func (mc *MetadataCache) Get(key string) (*ObjectMetadata, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	metadata, exists := mc.cache[key]
	if !exists {
		mc.misses++
		return nil, false
	}

	// Check if expired
	if time.Since(metadata.CachedAt) > mc.ttl {
		mc.misses++
		return nil, false
	}

	mc.hits++
	return metadata, true
}

// Set stores metadata in cache
func (mc *MetadataCache) Set(key string, metadata *ObjectMetadata) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Check if we need to evict
	if len(mc.cache) >= mc.maxSize {
		mc.evictOldest()
	}

	metadata.CachedAt = time.Now()
	mc.cache[key] = metadata
}

// evictOldest removes the oldest entry (LRU-like)
func (mc *MetadataCache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for key, metadata := range mc.cache {
		if oldestKey == "" || metadata.CachedAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = metadata.CachedAt
		}
	}

	if oldestKey != "" {
		delete(mc.cache, oldestKey)
		mc.evictions++
	}
}

// Clear clears the cache
func (mc *MetadataCache) Clear() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.cache = make(map[string]*ObjectMetadata)
}

// Stats returns cache statistics
type CacheStats struct {
	Size      int
	MaxSize   int
	Hits      uint64
	Misses    uint64
	Evictions uint64
	HitRate   float64
}

func (mc *MetadataCache) Stats() CacheStats {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	total := mc.hits + mc.misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(mc.hits) / float64(total) * 100
	}

	return CacheStats{
		Size:      len(mc.cache),
		MaxSize:   mc.maxSize,
		Hits:      mc.hits,
		Misses:    mc.misses,
		Evictions: mc.evictions,
		HitRate:   hitRate,
	}
}

// Prefetcher prefetches object metadata
type Prefetcher struct {
	client       *s3.Client
	cache        *MetadataCache
	concurrency  int
	prefetchSize int
}

// PrefetchConfig holds prefetcher configuration
type PrefetchConfig struct {
	CacheTTL     time.Duration
	CacheSize    int
	Concurrency  int
	PrefetchSize int
}

// DefaultPrefetchConfig returns default configuration
func DefaultPrefetchConfig() PrefetchConfig {
	return PrefetchConfig{
		CacheTTL:     5 * time.Minute,
		CacheSize:    10000,
		Concurrency:  10,
		PrefetchSize: 100,
	}
}

// NewPrefetcher creates a new metadata prefetcher
func NewPrefetcher(client *s3.Client, config PrefetchConfig) *Prefetcher {
	return &Prefetcher{
		client:       client,
		cache:        NewMetadataCache(config.CacheTTL, config.CacheSize),
		concurrency:  config.Concurrency,
		prefetchSize: config.PrefetchSize,
	}
}

// GetMetadata retrieves metadata, using cache if available
func (p *Prefetcher) GetMetadata(ctx context.Context, bucket, key string) (*ObjectMetadata, error) {
	// Try cache first
	if metadata, ok := p.cache.Get(key); ok {
		return metadata, nil
	}

	// Fetch from S3
	resp, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}

	metadata := &ObjectMetadata{
		Key:          key,
		Size:         *resp.ContentLength,
		LastModified: *resp.LastModified,
		ETag:         *resp.ETag,
		CachedAt:     time.Now(),
	}

	if resp.StorageClass != "" {
		metadata.StorageClass = string(resp.StorageClass)
	}

	// Cache the result
	p.cache.Set(key, metadata)

	return metadata, nil
}

// PrefetchBatch prefetches metadata for a batch of objects
func (p *Prefetcher) PrefetchBatch(ctx context.Context, bucket string, keys []string) error {
	// Split into chunks
	chunks := chunkKeys(keys, p.prefetchSize)

	for _, chunk := range chunks {
		if err := p.prefetchChunk(ctx, bucket, chunk); err != nil {
			return err
		}
	}

	return nil
}

func (p *Prefetcher) prefetchChunk(ctx context.Context, bucket string, keys []string) error {
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, p.concurrency)
	errChan := make(chan error, len(keys))

	for _, key := range keys {
		// Skip if already cached
		if _, ok := p.cache.Get(key); ok {
			continue
		}

		wg.Add(1)
		go func(k string) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			resp, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(k),
			})
			if err != nil {
				errChan <- err
				return
			}

			metadata := &ObjectMetadata{
				Key:          k,
				Size:         *resp.ContentLength,
				LastModified: *resp.LastModified,
				ETag:         *resp.ETag,
				CachedAt:     time.Now(),
			}

			if resp.StorageClass != "" {
				metadata.StorageClass = string(resp.StorageClass)
			}

			p.cache.Set(k, metadata)
		}(key)
	}

	wg.Wait()
	close(errChan)

	// Return first error if any
	if err := <-errChan; err != nil {
		return err
	}

	return nil
}

// PrefetchFromList prefetches metadata from S3 list operation
func (p *Prefetcher) PrefetchFromList(ctx context.Context, bucket, prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(p.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}

		// Cache metadata from list results
		for _, obj := range page.Contents {
			metadata := &ObjectMetadata{
				Key:          *obj.Key,
				Size:         *obj.Size,
				LastModified: *obj.LastModified,
				ETag:         *obj.ETag,
				StorageClass: string(obj.StorageClass),
				CachedAt:     time.Now(),
			}
			p.cache.Set(*obj.Key, metadata)
		}
	}

	return nil
}

// GetCacheStats returns cache statistics
func (p *Prefetcher) GetCacheStats() CacheStats {
	return p.cache.Stats()
}

// ClearCache clears the metadata cache
func (p *Prefetcher) ClearCache() {
	p.cache.Clear()
}

func chunkKeys(keys []string, chunkSize int) [][]string {
	var chunks [][]string
	for i := 0; i < len(keys); i += chunkSize {
		end := i + chunkSize
		if end > len(keys) {
			end = len(keys)
		}
		chunks = append(chunks, keys[i:end])
	}
	return chunks
}
