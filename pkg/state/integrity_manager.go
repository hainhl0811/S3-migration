package state

import (
	"database/sql"
	"fmt"
	"time"

	"s3migration/pkg/integrity"
)

// IntegrityManager handles database operations for integrity verification
type IntegrityManager struct {
	db *sql.DB
}

// NewIntegrityManager creates a new integrity manager
func NewIntegrityManager(db *sql.DB) *IntegrityManager {
	return &IntegrityManager{db: db}
}

// IntegrityRecord represents a database record for integrity verification
type IntegrityRecord struct {
	ID              int64
	TaskID          string
	ObjectKey       string
	SourceETag      string
	SourceSize      int64
	SourceProvider  string
	DestETag        string
	DestSize        int64
	DestProvider    string
	CalculatedMD5   string
	CalculatedSHA1  string
	CalculatedSHA256 string
	CalculatedCRC32 string
	ETagMatch       bool
	SizeMatch       bool
	MD5Match        bool
	SHA1Match       bool
	IsValid         bool
	ErrorMessage    string
	CreatedAt       time.Time
}

// IntegritySummary represents aggregated integrity metrics
type IntegritySummary struct {
	TaskID          string    `json:"task_id"`
	TotalObjects    int64     `json:"total_objects"`
	VerifiedObjects int64     `json:"verified_objects"`
	FailedObjects   int64     `json:"failed_objects"`
	IntegrityRate   float64   `json:"integrity_rate"`
	LastVerified    time.Time `json:"last_verified"`
}

// StoreIntegrityResult stores an integrity verification result
func (im *IntegrityManager) StoreIntegrityResult(
	taskID, objectKey string,
	result *integrity.IntegrityResult,
	sourceProvider, destProvider string,
) error {
	query := `
		INSERT INTO integrity_results 
		(task_id, object_key, 
		 source_etag, source_size, source_provider,
		 dest_etag, dest_size, dest_provider,
		 calculated_md5, calculated_sha1, calculated_sha256, calculated_crc32,
		 etag_match, size_match, md5_match, sha1_match,
		 is_valid, error_message, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		RETURNING id
	`

	var id int64
	err := im.db.QueryRow(query,
		taskID, objectKey,
		result.SourceETag, result.SourceSize, sourceProvider,
		result.DestETag, result.DestSize, destProvider,
		result.CalculatedMD5, result.CalculatedSHA1, result.CalculatedSHA256, result.CalculatedCRC32,
		result.ETagMatch, result.SizeMatch, result.MD5Match, result.SHA1Match,
		result.IsValid, result.ErrorMessage, time.Now(),
	).Scan(&id)

	if err != nil {
		return fmt.Errorf("failed to store integrity result: %w", err)
	}

	return nil
}

// GetIntegritySummary retrieves integrity summary for a task
func (im *IntegrityManager) GetIntegritySummary(taskID string) (*IntegritySummary, error) {
	query := `
		SELECT 
			task_id,
			total_objects,
			verified_objects,
			failed_objects,
			integrity_rate,
			last_verified
		FROM integrity_summary
		WHERE task_id = $1
	`

	var summary IntegritySummary
	err := im.db.QueryRow(query, taskID).Scan(
		&summary.TaskID,
		&summary.TotalObjects,
		&summary.VerifiedObjects,
		&summary.FailedObjects,
		&summary.IntegrityRate,
		&summary.LastVerified,
	)

	if err == sql.ErrNoRows {
		// No integrity results yet
		return &IntegritySummary{
			TaskID:          taskID,
			TotalObjects:    0,
			VerifiedObjects: 0,
			FailedObjects:   0,
			IntegrityRate:   0.0,
		}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get integrity summary: %w", err)
	}

	return &summary, nil
}

// GetFailedIntegrityObjects retrieves objects that failed integrity verification
func (im *IntegrityManager) GetFailedIntegrityObjects(taskID string, limit int) ([]IntegrityRecord, error) {
	query := `
		SELECT 
			id, task_id, object_key,
			source_etag, source_size, source_provider,
			dest_etag, dest_size, dest_provider,
			calculated_md5, calculated_sha1, calculated_sha256, calculated_crc32,
			etag_match, size_match, md5_match, sha1_match,
			is_valid, error_message, created_at
		FROM integrity_results
		WHERE task_id = $1 AND is_valid = FALSE
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := im.db.Query(query, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get failed integrity objects: %w", err)
	}
	defer rows.Close()

	var records []IntegrityRecord
	for rows.Next() {
		var record IntegrityRecord
		err := rows.Scan(
			&record.ID, &record.TaskID, &record.ObjectKey,
			&record.SourceETag, &record.SourceSize, &record.SourceProvider,
			&record.DestETag, &record.DestSize, &record.DestProvider,
			&record.CalculatedMD5, &record.CalculatedSHA1, &record.CalculatedSHA256, &record.CalculatedCRC32,
			&record.ETagMatch, &record.SizeMatch, &record.MD5Match, &record.SHA1Match,
			&record.IsValid, &record.ErrorMessage, &record.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan integrity record: %w", err)
		}
		records = append(records, record)
	}

	return records, nil
}

// UpdateTaskIntegrityStatus updates the integrity status for a task
func (im *IntegrityManager) UpdateTaskIntegrityStatus(taskID string) error {
	query := `
		UPDATE migration_tasks
		SET 
			verified_objects = (
				SELECT COUNT(*) FROM integrity_results 
				WHERE task_id = $1 AND is_valid = TRUE
			),
			failed_objects = (
				SELECT COUNT(*) FROM integrity_results 
				WHERE task_id = $1 AND is_valid = FALSE
			),
			integrity_rate = (
				SELECT COALESCE(
					ROUND(100.0 * SUM(CASE WHEN is_valid THEN 1 ELSE 0 END) / COUNT(*), 2),
					0.0
				)
				FROM integrity_results 
				WHERE task_id = $1
			),
			integrity_verified = (
				SELECT COUNT(*) > 0 FROM integrity_results WHERE task_id = $1
			),
			integrity_errors = (
				SELECT STRING_AGG(error_message, '; ')
				FROM (
					SELECT DISTINCT error_message 
					FROM integrity_results 
					WHERE task_id = $1 AND is_valid = FALSE
					LIMIT 10
				) AS errors
			)
		WHERE id = $1
	`

	_, err := im.db.Exec(query, taskID)
	if err != nil {
		return fmt.Errorf("failed to update task integrity status: %w", err)
	}

	return nil
}

// GetIntegrityReport generates a detailed integrity report
func (im *IntegrityManager) GetIntegrityReport(taskID string) (map[string]interface{}, error) {
	summary, err := im.GetIntegritySummary(taskID)
	if err != nil {
		return nil, err
	}

	failedObjects, err := im.GetFailedIntegrityObjects(taskID, 100)
	if err != nil {
		return nil, err
	}

	report := map[string]interface{}{
		"summary":         summary,
		"failed_objects":  failedObjects,
		"failed_count":    len(failedObjects),
		"has_failures":    summary.FailedObjects > 0,
		"integrity_passed": summary.IntegrityRate >= 99.9,
	}

	return report, nil
}

// DeleteIntegrityResults deletes integrity results for a task
func (im *IntegrityManager) DeleteIntegrityResults(taskID string) error {
	query := `DELETE FROM integrity_results WHERE task_id = $1`
	_, err := im.db.Exec(query, taskID)
	if err != nil {
		return fmt.Errorf("failed to delete integrity results: %w", err)
	}
	return nil
}

