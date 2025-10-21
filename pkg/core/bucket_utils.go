package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// BucketValidator handles bucket validation and creation
type BucketValidator struct {
	client *s3.Client
}

// NewBucketValidator creates a new bucket validator
func NewBucketValidator(client *s3.Client) *BucketValidator {
	return &BucketValidator{client: client}
}

// EnsureBucketExists checks if a bucket exists and creates it if necessary
func (bv *BucketValidator) EnsureBucketExists(ctx context.Context, bucketName string, region string, createIfMissing bool) error {
	// Check if bucket exists
	exists, err := bv.BucketExists(ctx, bucketName)
	if err != nil {
		return fmt.Errorf("failed to check bucket existence: %w", err)
	}

	if exists {
		return nil // Bucket already exists
	}

	// Bucket doesn't exist
	if !createIfMissing {
		return fmt.Errorf("destination bucket '%s' does not exist", bucketName)
	}

	// Create the bucket
	return bv.CreateBucket(ctx, bucketName, region)
}

// BucketExists checks if a bucket exists
func (bv *BucketValidator) BucketExists(ctx context.Context, bucketName string) (bool, error) {
	_, err := bv.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})

	if err != nil {
		// Check if it's a "not found" error (404)
		// In AWS SDK v2, we check the error type or message
		errMsg := err.Error()
		if contains(errMsg, "NotFound") || contains(errMsg, "NoSuchBucket") || contains(errMsg, "404") {
			return false, nil // Bucket doesn't exist
		}
		
		// Some other error (permissions, network, etc.)
		return false, err
	}

	return true, nil
}

// CreateBucket creates a new bucket
func (bv *BucketValidator) CreateBucket(ctx context.Context, bucketName, region string) error {
	input := &s3.CreateBucketInput{
		Bucket: aws.String(bucketName),
	}

	// For regions other than us-east-1, we need to specify LocationConstraint
	if region != "" && region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}

	_, err := bv.client.CreateBucket(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to create bucket '%s': %w", bucketName, err)
	}

	fmt.Printf("✓ Created destination bucket: %s\n", bucketName)
	return nil
}

// ValidateBucketAccess checks if we have required permissions on a bucket
func (bv *BucketValidator) ValidateBucketAccess(ctx context.Context, bucketName string, needWrite bool) error {
	// Check read access
	_, err := bv.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucketName),
		MaxKeys: aws.Int32(1),
	})

	if err != nil {
		return fmt.Errorf("no read access to bucket '%s': %w", bucketName, err)
	}

	// Check write access if needed
	if needWrite {
		// Try to get bucket ACL (requires write-like permissions)
		_, err := bv.client.GetBucketAcl(ctx, &s3.GetBucketAclInput{
			Bucket: aws.String(bucketName),
		})

		if err != nil {
			return fmt.Errorf("no write access to bucket '%s': %w", bucketName, err)
		}
	}

	return nil
}

// GetBucketRegion retrieves the region of a bucket
func (bv *BucketValidator) GetBucketRegion(ctx context.Context, bucketName string) (string, error) {
	resp, err := bv.client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{
		Bucket: aws.String(bucketName),
	})

	if err != nil {
		return "", err
	}

	// AWS returns empty string for us-east-1
	if resp.LocationConstraint == "" {
		return "us-east-1", nil
	}

	return string(resp.LocationConstraint), nil
}

// BucketInfo contains information about a bucket
type BucketInfo struct {
	Name         string
	Region       string
	Exists       bool
	Accessible   bool
	ObjectCount  int64
	TotalSize    int64
	ErrorMessage string
}

// GetBucketInfo retrieves comprehensive information about a bucket
func (bv *BucketValidator) GetBucketInfo(ctx context.Context, bucketName string) *BucketInfo {
	info := &BucketInfo{
		Name: bucketName,
	}

	// Check if exists
	exists, err := bv.BucketExists(ctx, bucketName)
	if err != nil {
		info.ErrorMessage = err.Error()
		return info
	}

	info.Exists = exists
	if !exists {
		return info
	}

	// Get region
	region, err := bv.GetBucketRegion(ctx, bucketName)
	if err == nil {
		info.Region = region
	}

	// Try to get object count
	paginator := s3.NewListObjectsV2Paginator(bv.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
	})

	var objectCount int64
	var totalSize int64

	for paginator.HasMorePages() && objectCount < 10000 { // Limit to first 10k for quick check
		page, err := paginator.NextPage(ctx)
		if err != nil {
			break
		}

		objectCount += int64(len(page.Contents))
		for _, obj := range page.Contents {
			totalSize += *obj.Size
		}
	}

	info.ObjectCount = objectCount
	info.TotalSize = totalSize
	info.Accessible = true

	return info
}

