package models

import "time"

// MigrationRequest represents a migration request
type MigrationRequest struct {
	SourceBucket      string       `json:"source_bucket"` // Empty = migrate all buckets
	DestBucket        string       `json:"dest_bucket"`   // Empty = use source bucket names
	SourcePrefix      string       `json:"source_prefix"`
	DestPrefix        string       `json:"dest_prefix"`
	SourceCredentials *Credentials `json:"source_credentials,omitempty"` // Credentials for source bucket
	DestCredentials   *Credentials `json:"dest_credentials,omitempty"`   // Credentials for destination bucket (optional, uses source if not provided)
	Credentials       *Credentials `json:"credentials,omitempty"`        // Deprecated: for backward compatibility, use source_credentials instead
	DryRun            bool         `json:"dry_run"`
	MigrationMode     string       `json:"migration_mode"` // "full_rewrite" or "incremental" (default: full_rewrite)
	Timeout           int          `json:"timeout"`
}

// Credentials for S3 access
type Credentials struct {
	AccessKey    string `json:"access_key,omitempty"`
	SecretKey    string `json:"secret_key,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
	Region       string `json:"region"`
	EndpointURL  string `json:"endpoint_url,omitempty"`
}

// GoogleDriveCredentials for Google Drive access
type GoogleDriveCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	RedirectURL  string `json:"redirect_url"`
}

// GoogleDriveMigrationRequest represents a Google Drive to S3 migration request
type GoogleDriveMigrationRequest struct {
	SourceFolderID    string                  `json:"source_folder_id"`    // Google Drive folder ID (empty = root)
	DestBucket        string                  `json:"dest_bucket"`         // S3 destination bucket
	DestPrefix        string                  `json:"dest_prefix"`         // S3 destination prefix
	SourceCredentials *GoogleDriveCredentials `json:"source_credentials"`  // Google Drive credentials
	DestCredentials   *Credentials            `json:"dest_credentials"`    // S3 destination credentials
	DryRun            bool                    `json:"dry_run"`
	MigrationMode     string                  `json:"migration_mode"`      // "full_rewrite" or "incremental"
	Timeout           int                     `json:"timeout"`
	IncludeSharedFiles bool                   `json:"include_shared_files"` // Include files shared with me (default: false)
}

// MigrationStatus represents the current status of a migration task
type MigrationStatus struct {
	TaskID         string    `json:"task_id"`
	Status         string    `json:"status"` // pending, running, completed, failed, cancelled
	MigrationType  string    `json:"migration_type"` // "s3" or "google-drive"
	Progress       float64   `json:"progress"`
	CopiedObjects  int64     `json:"copied_objects"`
	TotalObjects   int64     `json:"total_objects"`
	CopiedSize     int64     `json:"copied_size"`
	TotalSize      int64     `json:"total_size"`
	CurrentSpeed   float64   `json:"current_speed"` // MB/s
	ETA            string    `json:"eta"`
	Errors         []string  `json:"errors"`
	StartTime      time.Time `json:"start_time"`
	EndTime        time.Time `json:"end_time"`
	Duration       string    `json:"duration"` // Human-readable duration
	LastUpdateTime time.Time `json:"last_update_time"`
	// Dry run specific information
	DryRun         bool      `json:"dry_run"`
	DryRunVerified []string  `json:"dry_run_verified,omitempty"` // What was verified during dry run
	SampleFiles    []string  `json:"sample_files,omitempty"`     // Sample files found
}

// MigrationResult represents the final result of a migration
type MigrationResult struct {
	TaskID       string   `json:"task_id"`
	Success      bool     `json:"success"`
	Copied       int64    `json:"copied"`
	Failed       int64    `json:"failed"`
	TotalSizeMB  float64  `json:"total_size_mb"`
	CopiedSizeMB float64  `json:"copied_size_mb"`
	ElapsedTime  string   `json:"elapsed_time"`
	AvgSpeedMB   float64  `json:"avg_speed_mb"`
	Errors       []string `json:"errors"`
}

// ObjectInfo represents information about an S3 object
type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

// NetworkCondition represents network quality
type NetworkCondition string

const (
	NetworkExcellent NetworkCondition = "excellent"
	NetworkGood      NetworkCondition = "good"
	NetworkFair      NetworkCondition = "fair"
	NetworkPoor      NetworkCondition = "poor"
)

// WorkloadPattern represents the type of workload
type WorkloadPattern string

const (
	PatternManySmall  WorkloadPattern = "many_small_files"
	PatternMixed      WorkloadPattern = "mixed_sizes"
	PatternLargeFiles WorkloadPattern = "large_files"
	PatternUnknown    WorkloadPattern = "unknown"
)
