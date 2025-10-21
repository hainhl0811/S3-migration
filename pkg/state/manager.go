package state

import (
	"time"
)

// TaskState represents the persisted state of a migration task
type TaskState struct {
	ID              string                 `json:"id"`
	Status          string                 `json:"status"`
	Progress        float64                `json:"progress"`
	CopiedObjects   int64                  `json:"copied_objects"`
	TotalObjects    int64                  `json:"total_objects"`
	CopiedSize      int64                  `json:"copied_size"`
	TotalSize       int64                  `json:"total_size"`
	CurrentSpeed    float64                `json:"current_speed"`
	ETA             string                 `json:"eta"`
	Duration        string                 `json:"duration"`
	Errors          []string               `json:"errors"`
	StartTime       time.Time              `json:"start_time"`
	EndTime         *time.Time             `json:"end_time,omitempty"`
	MigrationType   string                 `json:"migration_type"`
	DryRun          bool                   `json:"dry_run"`
	SyncMode        bool                   `json:"sync_mode"`
	OriginalRequest map[string]interface{} `json:"original_request"`
}

// StateManager interface for state persistence
type StateManager interface {
	SaveTask(task *TaskState) error
	LoadTask(taskID string) (*TaskState, error)
	ListTasks() ([]*TaskState, error)
	DeleteTask(taskID string) error
	CleanupOldTasks(olderThan time.Duration) error
}

