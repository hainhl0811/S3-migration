-- Complete Database Schema for S3 Migration Tool
-- Run this to set up all required tables, indexes, and views

-- ============================================================================
-- MAIN MIGRATION TASKS TABLE
-- ============================================================================

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
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    
    -- Integrity verification columns
    integrity_verified BOOLEAN DEFAULT FALSE,
    integrity_errors TEXT,
    verified_objects BIGINT DEFAULT 0,
    failed_objects BIGINT DEFAULT 0,
    integrity_rate DECIMAL(5,2) DEFAULT 0.00
);

-- Indexes for migration_tasks
CREATE INDEX IF NOT EXISTS idx_tasks_status ON migration_tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON migration_tasks(created_at);
CREATE INDEX IF NOT EXISTS idx_tasks_updated_at ON migration_tasks(updated_at);

-- ============================================================================
-- INTEGRITY VERIFICATION TABLE
-- ============================================================================

CREATE TABLE IF NOT EXISTS integrity_results (
    id SERIAL PRIMARY KEY,
    task_id VARCHAR(255) NOT NULL,
    object_key VARCHAR(1024) NOT NULL,
    
    -- Source information
    source_etag VARCHAR(255),
    source_size BIGINT,
    source_provider VARCHAR(50),
    
    -- Destination information
    dest_etag VARCHAR(255),
    dest_size BIGINT,
    dest_provider VARCHAR(50),
    
    -- Calculated hashes
    calculated_md5 VARCHAR(64),
    calculated_sha1 VARCHAR(64),
    calculated_sha256 VARCHAR(64),
    calculated_crc32 VARCHAR(16),
    
    -- Verification results
    etag_match BOOLEAN NOT NULL,
    size_match BOOLEAN NOT NULL,
    md5_match BOOLEAN DEFAULT FALSE,
    sha1_match BOOLEAN DEFAULT FALSE,
    is_valid BOOLEAN NOT NULL,
    error_message TEXT,
    
    -- Timestamps
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    
    -- Foreign key
    CONSTRAINT fk_task FOREIGN KEY (task_id) REFERENCES migration_tasks(id) ON DELETE CASCADE
);

-- Indexes for integrity_results
CREATE INDEX IF NOT EXISTS idx_integrity_task_id ON integrity_results(task_id);
CREATE INDEX IF NOT EXISTS idx_integrity_is_valid ON integrity_results(is_valid);
CREATE INDEX IF NOT EXISTS idx_integrity_created_at ON integrity_results(created_at);
CREATE INDEX IF NOT EXISTS idx_integrity_task_valid ON integrity_results(task_id, is_valid);

-- ============================================================================
-- INTEGRITY SUMMARY VIEW
-- ============================================================================

CREATE OR REPLACE VIEW integrity_summary AS
SELECT 
    task_id,
    COUNT(*) as total_objects,
    SUM(CASE WHEN is_valid THEN 1 ELSE 0 END) as verified_objects,
    SUM(CASE WHEN NOT is_valid THEN 1 ELSE 0 END) as failed_objects,
    ROUND(100.0 * SUM(CASE WHEN is_valid THEN 1 ELSE 0 END) / COUNT(*), 2) as integrity_rate,
    MAX(created_at) as last_verified
FROM integrity_results
GROUP BY task_id;

-- ============================================================================
-- VERIFICATION
-- ============================================================================

-- Check that all tables were created
SELECT 
    'migration_tasks' as table_name,
    EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'migration_tasks') as exists
UNION ALL
SELECT 
    'integrity_results' as table_name,
    EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'integrity_results') as exists;

-- Check that all indexes were created
SELECT schemaname, tablename, indexname 
FROM pg_indexes 
WHERE tablename IN ('migration_tasks', 'integrity_results')
ORDER BY tablename, indexname;

-- Check that view was created
SELECT table_name 
FROM information_schema.views 
WHERE table_name = 'integrity_summary';

-- Success message
SELECT '✅ Database schema created successfully!' as status;

