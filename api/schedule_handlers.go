package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"s3migration/pkg/scheduler"
)

var scheduleManager *scheduler.Scheduler

// DefaultTaskExecutor executes scheduled tasks
type DefaultTaskExecutor struct{}

// Execute implements the TaskExecutor interface
func (e *DefaultTaskExecutor) Execute(ctx context.Context, schedule *scheduler.Schedule) error {
	// TODO: Implement actual migration execution
	// For now, just log that it would run
	return nil
}

// InitScheduler initializes the global scheduler
func InitScheduler(executor scheduler.TaskExecutor) {
	if scheduleManager != nil {
		return // Already initialized
	}
	scheduleManager = scheduler.NewScheduler(executor)
	scheduleManager.Start()
}

// EnsureSchedulerInitialized ensures the scheduler is initialized
func EnsureSchedulerInitialized() {
	if scheduleManager == nil {
		InitScheduler(&DefaultTaskExecutor{})
	}
}

// CreateScheduleRequest represents a request to create a schedule
type CreateScheduleRequest struct {
	Name             string                     `json:"name" binding:"required"`
	CronExpr         string                     `json:"cron_expr" binding:"required"`
	SourceBucket     string                     `json:"source_bucket" binding:"required"`
	DestBucket       string                     `json:"dest_bucket" binding:"required"`
	SourcePrefix     string                     `json:"source_prefix"`
	DestPrefix       string                     `json:"dest_prefix"`
	Incremental      bool                       `json:"incremental"`
	DeleteRemoved    bool                       `json:"delete_removed"`
	ConflictStrategy scheduler.ConflictStrategy `json:"conflict_strategy"`
}

// CreateSchedule handles POST /api/schedules
// @Summary Create a new schedule
// @Description Create a new scheduled migration task
// @Tags schedules
// @Accept json
// @Produce json
// @Param request body CreateScheduleRequest true "Schedule request"
// @Success 200 {object} scheduler.Schedule
// @Failure 400 {object} gin.H
// @Router /api/schedules [post]
func CreateSchedule(c *gin.Context) {
	EnsureSchedulerInitialized()
	
	var req CreateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Create schedule
	schedule := &scheduler.Schedule{
		ID:       uuid.New().String(),
		Name:     req.Name,
		CronExpr: req.CronExpr,
		Enabled:  true,
		Source: scheduler.SourceConfig{
			Bucket: req.SourceBucket,
			Prefix: req.SourcePrefix,
		},
		Destination: scheduler.DestConfig{
			Bucket: req.DestBucket,
			Prefix: req.DestPrefix,
		},
		Options: scheduler.SyncOptions{
			Incremental:      req.Incremental,
			DeleteRemoved:    req.DeleteRemoved,
			ConflictStrategy: req.ConflictStrategy,
		},
	}

	if err := scheduleManager.AddSchedule(schedule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, schedule)
}

// GetSchedule handles GET /api/schedules/:id
// @Summary Get a schedule
// @Description Get details of a specific schedule
// @Tags schedules
// @Produce json
// @Param id path string true "Schedule ID"
// @Success 200 {object} scheduler.Schedule
// @Failure 404 {object} gin.H
// @Router /api/schedules/{id} [get]
func GetSchedule(c *gin.Context) {
	if scheduleManager == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scheduler not initialized"})
		return
	}
	
	id := c.Param("id")

	schedule, err := scheduleManager.GetSchedule(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, schedule)
}

// ListSchedules handles GET /api/schedules
// @Summary List all schedules
// @Description Get a list of all scheduled tasks
// @Tags schedules
// @Produce json
// @Success 200 {array} scheduler.Schedule
// @Router /api/schedules [get]
func ListSchedules(c *gin.Context) {
	if scheduleManager == nil {
		c.JSON(http.StatusOK, []interface{}{}) // Return empty array if not initialized
		return
	}
	schedules := scheduleManager.ListSchedules()
	c.JSON(http.StatusOK, schedules)
}

// UpdateSchedule handles PUT /api/schedules/:id
// @Summary Update a schedule
// @Description Update an existing schedule
// @Tags schedules
// @Accept json
// @Produce json
// @Param id path string true "Schedule ID"
// @Param request body CreateScheduleRequest true "Updated schedule"
// @Success 200 {object} scheduler.Schedule
// @Failure 400 {object} gin.H
// @Router /api/schedules/{id} [put]
func UpdateSchedule(c *gin.Context) {
	id := c.Param("id")

	var req CreateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get existing schedule
	existingSchedule, err := scheduleManager.GetSchedule(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Update fields
	existingSchedule.Name = req.Name
	existingSchedule.CronExpr = req.CronExpr
	existingSchedule.Source.Bucket = req.SourceBucket
	existingSchedule.Source.Prefix = req.SourcePrefix
	existingSchedule.Destination.Bucket = req.DestBucket
	existingSchedule.Destination.Prefix = req.DestPrefix
	existingSchedule.Options.Incremental = req.Incremental
	existingSchedule.Options.DeleteRemoved = req.DeleteRemoved
	existingSchedule.Options.ConflictStrategy = req.ConflictStrategy

	if err := scheduleManager.UpdateSchedule(existingSchedule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, existingSchedule)
}

// DeleteSchedule handles DELETE /api/schedules/:id
// @Summary Delete a schedule
// @Description Delete a scheduled task
// @Tags schedules
// @Produce json
// @Param id path string true "Schedule ID"
// @Success 200 {object} gin.H
// @Failure 404 {object} gin.H
// @Router /api/schedules/{id} [delete]
func DeleteSchedule(c *gin.Context) {
	id := c.Param("id")

	if err := scheduleManager.RemoveSchedule(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// EnableSchedule handles POST /api/schedules/:id/enable
// @Summary Enable a schedule
// @Description Enable a disabled schedule
// @Tags schedules
// @Produce json
// @Param id path string true "Schedule ID"
// @Success 200 {object} gin.H
// @Failure 400 {object} gin.H
// @Router /api/schedules/{id}/enable [post]
func EnableSchedule(c *gin.Context) {
	id := c.Param("id")

	if err := scheduleManager.EnableSchedule(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "enabled"})
}

// DisableSchedule handles POST /api/schedules/:id/disable
// @Summary Disable a schedule
// @Description Disable an active schedule
// @Tags schedules
// @Produce json
// @Param id path string true "Schedule ID"
// @Success 200 {object} gin.H
// @Failure 400 {object} gin.H
// @Router /api/schedules/{id}/disable [post]
func DisableSchedule(c *gin.Context) {
	id := c.Param("id")

	if err := scheduleManager.DisableSchedule(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "disabled"})
}

// RunScheduleNow handles POST /api/schedules/:id/run
// @Summary Run schedule immediately
// @Description Execute a schedule immediately, outside of its cron schedule
// @Tags schedules
// @Produce json
// @Param id path string true "Schedule ID"
// @Success 200 {object} gin.H
// @Failure 400 {object} gin.H
// @Router /api/schedules/{id}/run [post]
func RunScheduleNow(c *gin.Context) {
	id := c.Param("id")

	if err := scheduleManager.RunNow(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "started"})
}

// GetSchedulerStats handles GET /api/schedules/stats
// @Summary Get scheduler statistics
// @Description Get overall scheduler statistics
// @Tags schedules
// @Produce json
// @Success 200 {object} scheduler.SchedulerStats
// @Router /api/schedules/stats [get]
func GetSchedulerStats(c *gin.Context) {
	if scheduleManager == nil {
		c.JSON(http.StatusOK, gin.H{
			"total_schedules": 0,
			"active_schedules": 0,
			"disabled_schedules": 0,
		})
		return
	}
	stats := scheduleManager.GetStats()
	c.JSON(http.StatusOK, stats)
}
