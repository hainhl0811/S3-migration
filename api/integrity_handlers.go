package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"s3migration/pkg/state"
)

// IntegrityHandlers contains handlers for integrity verification endpoints
type IntegrityHandlers struct {
	integrityManager *state.IntegrityManager
}

// NewIntegrityHandlers creates a new integrity handlers instance
func NewIntegrityHandlers(integrityManager *state.IntegrityManager) *IntegrityHandlers {
	return &IntegrityHandlers{
		integrityManager: integrityManager,
	}
}

// GetIntegritySummary returns integrity summary for a task
// GET /api/tasks/:taskId/integrity
func (h *IntegrityHandlers) GetIntegritySummary(c *gin.Context) {
	taskID := c.Param("taskId")
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task_id is required"})
		return
	}

	summary, err := h.integrityManager.GetIntegritySummary(taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, summary)
}

// GetIntegrityReport returns detailed integrity report for a task
// GET /api/tasks/:taskId/integrity/report
func (h *IntegrityHandlers) GetIntegrityReport(c *gin.Context) {
	taskID := c.Param("taskId")
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task_id is required"})
		return
	}

	report, err := h.integrityManager.GetIntegrityReport(taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, report)
}

// GetFailedIntegrityObjects returns objects that failed integrity verification
// GET /api/tasks/:taskId/integrity/failures
func (h *IntegrityHandlers) GetFailedIntegrityObjects(c *gin.Context) {
	taskID := c.Param("taskId")
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task_id is required"})
		return
	}

	// Get limit from query parameter (default: 100)
	limit := 100
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsedLimit, err := parseInt(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}

	failures, err := h.integrityManager.GetFailedIntegrityObjects(taskID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id":  taskID,
		"count":    len(failures),
		"failures": failures,
	})
}

// Helper function to parse int
func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

