package pool

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ConnectionPool manages a pool of S3 client connections
type ConnectionPool struct {
	clients     []*s3.Client
	mu          sync.RWMutex
	size        int
	currentIdx  atomic.Int32
	region      string
	endpointURL string
	created     time.Time
	requests    atomic.Int64
	errors      atomic.Int64
}

// ConnectionPoolConfig holds configuration for the connection pool
type ConnectionPoolConfig struct {
	Size        int
	Region      string
	EndpointURL string
	MaxRetries  int
	Timeout     time.Duration
	// Explicit credentials for custom S3 providers
	AccessKey string
	SecretKey string
}

// DefaultConnectionPoolConfig returns default pool configuration
func DefaultConnectionPoolConfig() ConnectionPoolConfig {
	return ConnectionPoolConfig{
		Size:       10,
		Region:     "us-east-1",
		MaxRetries: 3,
		Timeout:    30 * time.Second,
	}
}

// NewConnectionPool creates a new connection pool
func NewConnectionPool(ctx context.Context, cfg ConnectionPoolConfig) (*ConnectionPool, error) {
	if cfg.Size <= 0 {
		cfg.Size = 10
	}

	pool := &ConnectionPool{
		clients:     make([]*s3.Client, cfg.Size),
		size:        cfg.Size,
		region:      cfg.Region,
		endpointURL: cfg.EndpointURL,
		created:     time.Now(),
	}

	// Create all clients upfront
	for i := 0; i < cfg.Size; i++ {
		client, err := pool.createClient(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create client %d: %w", i, err)
		}
		pool.clients[i] = client
	}

	return pool, nil
}

func (cp *ConnectionPool) createClient(ctx context.Context, cfg ConnectionPoolConfig) (*s3.Client, error) {
	var awsCfg aws.Config
	var err error
	
	// For S3-compatible storage with custom endpoint and no region, use a dummy region
	// AWS SDK requires a region for signature calculation, but S3-compatible storage ignores it
	region := cfg.Region
	if region == "" && cfg.EndpointURL != "" {
		region = "us-east-1" // Dummy region for S3-compatible storage
	}
	
	// For S3-compatible storage, use a custom HTTP client that doesn't follow redirects
	var httpClient *http.Client
	if cfg.EndpointURL != "" {
		httpClient = &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Don't follow redirects for S3-compatible storage
				// This prevents 301 PermanentRedirect issues
				return http.ErrUseLastResponse
			},
		}
	}
	
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		// Use explicit credentials for custom S3 providers
		configOptions := []func(*config.LoadOptions) error{
			config.WithRegion(region),
			config.WithRetryMaxAttempts(cfg.MaxRetries),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
				cfg.AccessKey,
				cfg.SecretKey,
				"", // session token (empty for static credentials)
			)),
		}
		
		if httpClient != nil {
			configOptions = append(configOptions, config.WithHTTPClient(httpClient))
		}
		
		awsCfg, err = config.LoadDefaultConfig(ctx, configOptions...)
	} else {
		// Use default credential chain (environment variables, IAM role, etc.)
		configOptions := []func(*config.LoadOptions) error{
			config.WithRegion(region),
			config.WithRetryMaxAttempts(cfg.MaxRetries),
		}
		
		if httpClient != nil {
			configOptions = append(configOptions, config.WithHTTPClient(httpClient))
		}
		
		awsCfg, err = config.LoadDefaultConfig(ctx, configOptions...)
	}
	
	if err != nil {
		return nil, err
	}

	clientOptions := []func(*s3.Options){
		func(o *s3.Options) {
			o.RetryMaxAttempts = cfg.MaxRetries
		},
	}

	if cfg.EndpointURL != "" {
		// Create custom endpoint resolver to force our endpoint and prevent redirects
		customResolver := s3.EndpointResolverFunc(func(region string, options s3.EndpointResolverOptions) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:               cfg.EndpointURL,
				HostnameImmutable: true, // Prevent SDK from modifying the hostname
				Source:            aws.EndpointSourceCustom,
			}, nil
		})
		
		clientOptions = append(clientOptions, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.EndpointURL)
			o.EndpointResolver = customResolver
			// Force path-style addressing for S3-compatible storage
			// This is required for most non-AWS S3 providers (MinIO, CMC Telecom, etc.)
			o.UsePathStyle = true
			// Disable endpoint discovery to prevent 301 redirects
			o.EndpointOptions.DisableHTTPS = false
			o.EndpointOptions.UseFIPSEndpoint = aws.FIPSEndpointStateDisabled
			// Disable dual-stack endpoints
			o.EndpointOptions.UseDualStackEndpoint = aws.DualStackEndpointStateDisabled
			fmt.Printf("S3 Client Config: Endpoint=%s, UsePathStyle=true, HostnameImmutable=true, Region=%s\n", cfg.EndpointURL, region)
		})
	} else {
		fmt.Printf("S3 Client Config: AWS Default, Region=%s\n", region)
	}

	return s3.NewFromConfig(awsCfg, clientOptions...), nil
}

// GetClient retrieves a client from the pool using round-robin
func (cp *ConnectionPool) GetClient() *s3.Client {
	cp.requests.Add(1)

	idx := cp.currentIdx.Add(1)
	if idx < 0 {
		idx = -idx
	}

	cp.mu.RLock()
	defer cp.mu.RUnlock()

	return cp.clients[int(idx)%cp.size]
}

// GetClientByKey retrieves a client using consistent hashing based on a key
func (cp *ConnectionPool) GetClientByKey(key string) *s3.Client {
	cp.requests.Add(1)

	hash := hashString(key)

	cp.mu.RLock()
	defer cp.mu.RUnlock()

	return cp.clients[int(hash)%cp.size]
}

// RecordError records an error for statistics
func (cp *ConnectionPool) RecordError() {
	cp.errors.Add(1)
}

// Stats returns connection pool statistics
type ConnectionPoolStats struct {
	Size          int
	TotalRequests int64
	TotalErrors   int64
	Uptime        time.Duration
	ErrorRate     float64
}

func (cp *ConnectionPool) Stats() ConnectionPoolStats {
	requests := cp.requests.Load()
	errors := cp.errors.Load()

	errorRate := 0.0
	if requests > 0 {
		errorRate = float64(errors) / float64(requests) * 100
	}

	return ConnectionPoolStats{
		Size:          cp.size,
		TotalRequests: requests,
		TotalErrors:   errors,
		Uptime:        time.Since(cp.created),
		ErrorRate:     errorRate,
	}
}

// Close closes all clients in the pool
func (cp *ConnectionPool) Close() error {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	// AWS SDK v2 clients don't need explicit closing
	// Just clear the references
	cp.clients = nil

	return nil
}

// Resize resizes the connection pool (creates new clients or removes excess)
func (cp *ConnectionPool) Resize(ctx context.Context, newSize int) error {
	if newSize <= 0 {
		return fmt.Errorf("invalid pool size: %d", newSize)
	}

	cp.mu.Lock()
	defer cp.mu.Unlock()

	if newSize == cp.size {
		return nil
	}

	cfg := ConnectionPoolConfig{
		Size:        newSize,
		Region:      cp.region,
		EndpointURL: cp.endpointURL,
		MaxRetries:  3,
		Timeout:     30 * time.Second,
	}

	if newSize > cp.size {
		// Add more clients
		for i := cp.size; i < newSize; i++ {
			client, err := cp.createClient(ctx, cfg)
			if err != nil {
				return fmt.Errorf("failed to create client %d: %w", i, err)
			}
			cp.clients = append(cp.clients, client)
		}
	} else {
		// Remove excess clients
		cp.clients = cp.clients[:newSize]
	}

	cp.size = newSize
	return nil
}

// Simple hash function for consistent hashing
func hashString(s string) uint32 {
	h := uint32(0)
	for i := 0; i < len(s); i++ {
		h = 31*h + uint32(s[i])
	}
	return h
}

// HealthCheck performs a health check on all clients
func (cp *ConnectionPool) HealthCheck(ctx context.Context) map[int]error {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	results := make(map[int]error)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, client := range cp.clients {
		wg.Add(1)
		go func(idx int, c *s3.Client) {
			defer wg.Done()

			// Try to list buckets as a health check
			_, err := c.ListBuckets(ctx, &s3.ListBucketsInput{})

			mu.Lock()
			results[idx] = err
			mu.Unlock()
		}(i, client)
	}

	wg.Wait()
	return results
}
