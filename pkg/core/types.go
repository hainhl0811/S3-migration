package core

import (
	"time"
)

// MigrationMode defines the migration behavior
type MigrationMode string

const (
	// ModeFullRewrite copies all objects regardless of destination state
	ModeFullRewrite MigrationMode = "full_rewrite"
	// ModeIncremental only copies new or changed objects (based on size/timestamp)
	ModeIncremental MigrationMode = "incremental"
)

// MigrateInput contains parameters for a migration operation
type MigrateInput struct {
	SourceBucket      string
	DestBucket        string
	SourcePrefix      string
	DestPrefix        string
	DestRegion        string
	DryRun            bool
	SyncMode          bool          // Deprecated: use MigrationMode instead
	MigrationMode     MigrationMode // Migration mode: full_rewrite or incremental
	Timeout           time.Duration
	// Destination credentials (optional, if different from source)
	DestAccessKey     string
	DestSecretKey     string
	DestEndpointURL   string
	// Progress callback for real-time updates
	ProgressCallback  func(progress float64, copied, total int64, speed float64, eta string)
}

// MigrateResult contains the result of a migration operation
type MigrateResult struct {
	Copied           int64
	Failed           int64
	TotalSizeMB      float64
	CopiedSizeMB     float64
	ElapsedTime      string
	AvgSpeedMB       float64
	Cancelled        bool
	RemainingObjects int64
	Errors           []string
	// Dry run specific information
	DryRun           bool
	DryRunVerified   []string
	SampleFiles      []string
}

// objectInfo represents basic object information
type objectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// copyJob represents a copy job for the worker pool
type copyJob struct {
	sourceKey string
	destKey   string
	size      int64
}

// copyResult represents the result of a copy operation
type copyResult struct {
	key       string
	sourceKey string
	destKey   string
	size      int64
	err       error
	success   bool
	cancelled bool
}

