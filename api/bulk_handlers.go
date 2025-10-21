package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"s3migration/pkg/core"
)

// BulkMigrationRequest represents a request to migrate all buckets
type BulkMigrationRequest struct {
	SourceRegion   string   `json:"source_region"`
	SourceEndpoint string   `json:"source_endpoint"`
	DestRegion     string   `json:"dest_region"`
	DestEndpoint   string   `json:"dest_endpoint"`
	ExcludeBuckets []string `json:"exclude_buckets"` // Buckets to skip
	IncludeBuckets []string `json:"include_buckets"` // Only these buckets (if specified)
	DryRun         bool     `json:"dry_run"`
	Timeout        int      `json:"timeout"`
	Concurrent     int      `json:"concurrent"` // Number of buckets to migrate concurrently
}

// StartBulkMigration handles POST /api/migrate/bulk
// @Summary Start bulk migration of all buckets
// @Description Migrate all buckets from source account to destination account
// @Tags migration
// @Accept json
// @Produce json
// @Param request body BulkMigrationRequest true "Bulk migration request"
// @Success 200 {object} gin.H
// @Failure 400 {object} gin.H
// @Router /api/migrate/bulk [post]
func StartBulkMigration(c *gin.Context) {
	var req BulkMigrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Generate task ID
	taskID := uuid.New().String()

	// Start bulk migration in background
	go runBulkMigration(taskID, req)

	c.JSON(http.StatusOK, gin.H{
		"task_id": taskID,
		"status":  "started",
		"message": "Bulk migration started. This will migrate all buckets in the source account.",
	})
}

func runBulkMigration(taskID string, req BulkMigrationRequest) {
	ctx := context.Background()

	// Create bulk migrator
	bulkMigrator, err := core.NewBulkMigrator(
		ctx,
		req.SourceRegion,
		req.SourceEndpoint,
		req.DestRegion,
		req.DestEndpoint,
	)
	if err != nil {
		fmt.Printf("Failed to create bulk migrator: %v\n", err)
		return
	}

	// Set defaults
	if req.Timeout == 0 {
		req.Timeout = 3600
	}
	if req.Concurrent == 0 {
		req.Concurrent = 3
	}

	// Execute bulk migration
	input := core.BulkMigrateInput{
		ExcludeBuckets: req.ExcludeBuckets,
		IncludeBuckets: req.IncludeBuckets,
		DryRun:         req.DryRun,
		Timeout:        time.Duration(req.Timeout) * time.Second,
		Concurrent:     req.Concurrent,
	}

	result, err := bulkMigrator.MigrateAllBuckets(ctx, input)
	if err != nil {
		fmt.Printf("Bulk migration failed: %v\n", err)
		return
	}

	fmt.Printf("\nBulk migration completed! Migrated %d buckets, %d objects, %.1f MB\n",
		result.SuccessBuckets, result.TotalObjects, result.TotalSizeMB)
}

