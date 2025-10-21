package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/lib/pq" // PostgreSQL driver
)

// DBStateManager manages persistent state using a database (PostgreSQL/MySQL)
type DBStateManager struct {
	db *sql.DB
}

// NewDBStateManager creates a new database-backed state manager
// connectionString examples:
//   PostgreSQL: "postgres://user:password@host:5432/dbname?sslmode=require"
//   MySQL: "user:password@tcp(host:3306)/dbname?parseTime=true"
func NewDBStateManager(driverName, connectionString string) (*DBStateManager, error) {
	db, err := sql.Open(driverName, connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Configure connection pool for high concurrency
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	manager := &DBStateManager{db: db}

	// Initialize schema
	if err := manager.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	fmt.Println("✅ Database state manager initialized successfully")
	return manager, nil
}

// initSchema creates the necessary tables if they don't exist
func (m *DBStateManager) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS migration_tasks (
		id VARCHAR(255) PRIMARY KEY,
		status VARCHAR(50) NOT NULL,
		progress FLOAT NOT NULL DEFAULT 0,
		copied_objects BIGINT NOT NULL DEFAULT 0,
		total_objects BIGINT NOT NULL DEFAULT 0,
		copied_size BIGINT NOT NULL DEFAULT 0,
		total_size BIGINT NOT NULL DEFAULT 0,
		current_speed FLOAT NOT NULL DEFAULT 0,
		eta VARCHAR(255),
		duration VARCHAR(255),
		errors TEXT,
		start_time TIMESTAMP NOT NULL,
		end_time TIMESTAMP,
		migration_type VARCHAR(50),
		dry_run BOOLEAN DEFAULT FALSE,
		sync_mode BOOLEAN DEFAULT FALSE,
		original_request TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_tasks_status ON migration_tasks(status);
	CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON migration_tasks(created_at);
	CREATE INDEX IF NOT EXISTS idx_tasks_updated_at ON migration_tasks(updated_at);
	`

	_, err := m.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	return nil
}

// SaveTask saves task state to database
func (m *DBStateManager) SaveTask(task *TaskState) error {
	errorsJSON, _ := json.Marshal(task.Errors)
	requestJSON, _ := json.Marshal(task.OriginalRequest)

	query := `
		INSERT INTO migration_tasks (
			id, status, progress, copied_objects, total_objects, copied_size, total_size,
			current_speed, eta, duration, errors, start_time, end_time, migration_type,
			dry_run, sync_mode, original_request, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			progress = EXCLUDED.progress,
			copied_objects = EXCLUDED.copied_objects,
			total_objects = EXCLUDED.total_objects,
			copied_size = EXCLUDED.copied_size,
			total_size = EXCLUDED.total_size,
			current_speed = EXCLUDED.current_speed,
			eta = EXCLUDED.eta,
			duration = EXCLUDED.duration,
			errors = EXCLUDED.errors,
			end_time = EXCLUDED.end_time,
			updated_at = EXCLUDED.updated_at
	`

	_, err := m.db.Exec(query,
		task.ID,
		task.Status,
		task.Progress,
		task.CopiedObjects,
		task.TotalObjects,
		task.CopiedSize,
		task.TotalSize,
		task.CurrentSpeed,
		task.ETA,
		task.Duration,
		string(errorsJSON),
		task.StartTime,
		task.EndTime,
		task.MigrationType,
		task.DryRun,
		task.SyncMode,
		string(requestJSON),
		time.Now(),
	)

	if err != nil {
		return fmt.Errorf("failed to save task: %w", err)
	}

	return nil
}

// LoadTask loads task state from database
func (m *DBStateManager) LoadTask(taskID string) (*TaskState, error) {
	query := `
		SELECT id, status, progress, copied_objects, total_objects, copied_size, total_size,
			   current_speed, eta, duration, errors, start_time, end_time, migration_type,
			   dry_run, sync_mode, original_request
		FROM migration_tasks
		WHERE id = $1
	`

	var task TaskState
	var errorsJSON, requestJSON string
	var endTime sql.NullTime

	err := m.db.QueryRow(query, taskID).Scan(
		&task.ID,
		&task.Status,
		&task.Progress,
		&task.CopiedObjects,
		&task.TotalObjects,
		&task.CopiedSize,
		&task.TotalSize,
		&task.CurrentSpeed,
		&task.ETA,
		&task.Duration,
		&errorsJSON,
		&task.StartTime,
		&endTime,
		&task.MigrationType,
		&task.DryRun,
		&task.SyncMode,
		&requestJSON,
	)

	if err == sql.ErrNoRows {
		return nil, nil // Task not found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load task: %w", err)
	}

	if endTime.Valid {
		task.EndTime = &endTime.Time
	}

	json.Unmarshal([]byte(errorsJSON), &task.Errors)
	json.Unmarshal([]byte(requestJSON), &task.OriginalRequest)

	return &task, nil
}

// ListTasks lists all tasks
func (m *DBStateManager) ListTasks() ([]*TaskState, error) {
	query := `
		SELECT id, status, progress, copied_objects, total_objects, copied_size, total_size,
			   current_speed, eta, duration, errors, start_time, end_time, migration_type,
			   dry_run, sync_mode, original_request
		FROM migration_tasks
		ORDER BY created_at DESC
		LIMIT 1000
	`

	rows, err := m.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*TaskState
	for rows.Next() {
		var task TaskState
		var errorsJSON, requestJSON string
		var endTime sql.NullTime

		err := rows.Scan(
			&task.ID,
			&task.Status,
			&task.Progress,
			&task.CopiedObjects,
			&task.TotalObjects,
			&task.CopiedSize,
			&task.TotalSize,
			&task.CurrentSpeed,
			&task.ETA,
			&task.Duration,
			&errorsJSON,
			&task.StartTime,
			&endTime,
			&task.MigrationType,
			&task.DryRun,
			&task.SyncMode,
			&requestJSON,
		)
		if err != nil {
			fmt.Printf("Warning: failed to scan task: %v\n", err)
			continue
		}

		if endTime.Valid {
			task.EndTime = &endTime.Time
		}

		json.Unmarshal([]byte(errorsJSON), &task.Errors)
		json.Unmarshal([]byte(requestJSON), &task.OriginalRequest)

		tasks = append(tasks, &task)
	}

	return tasks, nil
}

// DeleteTask deletes task state from database
func (m *DBStateManager) DeleteTask(taskID string) error {
	query := `DELETE FROM migration_tasks WHERE id = $1`

	_, err := m.db.Exec(query, taskID)
	if err != nil {
		return fmt.Errorf("failed to delete task: %w", err)
	}

	return nil
}

// CleanupOldTasks removes task states older than the specified duration
func (m *DBStateManager) CleanupOldTasks(olderThan time.Duration) error {
	cutoffTime := time.Now().Add(-olderThan)

	query := `DELETE FROM migration_tasks WHERE created_at < $1 AND status IN ('completed', 'failed', 'cancelled')`

	result, err := m.db.Exec(query, cutoffTime)
	if err != nil {
		return fmt.Errorf("failed to cleanup old tasks: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		fmt.Printf("Cleaned up %d old task records\n", rowsAffected)
	}

	return nil
}

// Close closes the database connection
func (m *DBStateManager) Close() error {
	return m.db.Close()
}

