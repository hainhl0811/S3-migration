package api

import (
	"context"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	"s3migration/pkg/core"
)

// TestConnectionRequest represents the test connection request
type TestConnectionRequest struct {
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	Region     string `json:"region"`
	EndpointURL string `json:"endpoint_url"`
}

// TestConnection handles POST /api/test-connection
// @Summary Test S3 connection
// @Description Test if S3 credentials and configuration are correct
// @Tags debug
// @Accept json
// @Produce json
// @Param request body TestConnectionRequest true "Connection test parameters"
// @Success 200 {object} gin.H
// @Failure 400 {object} gin.H
// @Failure 500 {object} gin.H
// @Router /api/test-connection [post]
func TestConnection(c *gin.Context) {
	ctx := context.Background()

	var req TestConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "Invalid request format",
			"error":   err.Error(),
		})
		return
	}

	// Default region if not provided
	if req.Region == "" {
		req.Region = "us-east-1"
	}

	var client *s3.Client
	var err error

	// Create enhanced migrator with provided credentials or use default
	cfg := core.EnhancedMigratorConfig{
		Region:             req.Region,
		EndpointURL:        req.EndpointURL,
		ConnectionPoolSize: 5,
		EnableStreaming:    false,
		EnablePrefetch:     false,
		AccessKey:          req.AccessKey,
		SecretKey:          req.SecretKey,
	}
	
	enhancedMigrator, err := core.NewEnhancedMigrator(ctx, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "Failed to create S3 client",
			"error":   err.Error(),
			"hint":    "Check your credentials, region, and endpoint URL",
		})
		return
	}
	client = enhancedMigrator.GetClient()

	// Test connection by listing buckets
	_, err = client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "Failed to list buckets",
			"error":   err.Error(),
			"hint":    "Check your credentials, region, and endpoint URL",
		})
		return
	}

	// Success response
	response := gin.H{
		"status":  "connected",
		"message": "S3 connection successful",
		"region":  req.Region,
	}

	if req.EndpointURL != "" {
		response["endpoint"] = req.EndpointURL
	}

	if req.AccessKey != "" {
		response["credentials_source"] = "provided"
		response["has_access_key"] = true
	} else {
		response["credentials_source"] = "environment/default"
	}

	c.JSON(http.StatusOK, response)
}

// TestBucketListingRequest represents the test bucket listing request
type TestBucketListingRequest struct {
	AccessKey   string `json:"access_key"`
	SecretKey   string `json:"secret_key"`
	Region      string `json:"region"`
	EndpointURL string `json:"endpoint_url"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
}

// TestBucketListing handles POST /api/test-bucket-listing
// @Summary Test bucket listing
// @Description Test if we can list objects in a specific bucket
// @Tags debug
// @Accept json
// @Produce json
// @Param request body TestBucketListingRequest true "Bucket listing test parameters"
// @Success 200 {object} gin.H
// @Failure 400 {object} gin.H
// @Failure 500 {object} gin.H
// @Router /api/test-bucket-listing [post]
func TestBucketListing(c *gin.Context) {
	ctx := context.Background()

	var req TestBucketListingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "error",
			"message": "Invalid request format",
			"error":   err.Error(),
		})
		return
	}

	// Default region if not provided
	if req.Region == "" {
		req.Region = "us-east-1"
	}

	var client *s3.Client
	var err error

	// Create enhanced migrator with provided credentials or use default
	cfg := core.EnhancedMigratorConfig{
		Region:             req.Region,
		EndpointURL:        req.EndpointURL,
		ConnectionPoolSize: 5,
		EnableStreaming:    false,
		EnablePrefetch:     false,
		AccessKey:          req.AccessKey,
		SecretKey:          req.SecretKey,
	}
	
	enhancedMigrator, err := core.NewEnhancedMigrator(ctx, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": "Failed to create S3 client",
			"error":   err.Error(),
		})
		return
	}
	
	// Test bucket listing
	client = enhancedMigrator.GetClient()
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: &req.Bucket,
		Prefix: &req.Prefix,
	})

	var objects []map[string]interface{}
	pageCount := 0
	totalObjects := 0

	for paginator.HasMorePages() {
		pageCount++
		page, err := paginator.NextPage(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status":  "error",
				"message": "Failed to list objects",
				"error":   err.Error(),
				"page":    pageCount,
			})
			return
		}

		for _, obj := range page.Contents {
			objects = append(objects, map[string]interface{}{
				"key":  *obj.Key,
				"size": *obj.Size,
			})
			totalObjects++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":         "success",
		"message":        "Bucket listing successful",
		"bucket":         req.Bucket,
		"prefix":         req.Prefix,
		"total_objects":  totalObjects,
		"pages":          pageCount,
		"sample_objects": objects[:min(10, len(objects))], // First 10 objects
	})
}

// min helper function
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GetTaskErrors handles GET /api/debug/task/:taskID/errors
// @Summary Get detailed error information for a task
// @Description Get all error details for a specific migration task
// @Tags debug
// @Produce json
// @Param taskID path string true "Task ID"
// @Success 200 {object} gin.H
// @Failure 404 {object} gin.H
// @Router /api/debug/task/{taskID}/errors [get]
func GetTaskErrors(c *gin.Context) {
	taskID := c.Param("taskID")

	taskManager.mu.RLock()
	task, exists := taskManager.tasks[taskID]
	taskManager.mu.RUnlock()

	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}

	response := gin.H{
		"task_id":   taskID,
		"status":    task.Status.Status,
		"errors":    task.Status.Errors,
		"start_time": task.Status.StartTime,
		"end_time":   task.Status.LastUpdateTime,
	}

	// Add result details if available
	if task.Result != nil {
		response["result"] = gin.H{
			"success":       task.Result.Success,
			"copied":        task.Result.Copied,
			"failed":        task.Result.Failed,
			"total_size_mb": task.Result.TotalSizeMB,
			"copied_size_mb": task.Result.CopiedSizeMB,
			"elapsed_time":  task.Result.ElapsedTime,
			"avg_speed_mb":  task.Result.AvgSpeedMB,
			"errors":        task.Result.Errors,
		}
	}

	c.JSON(http.StatusOK, response)
}

