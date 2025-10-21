-- Integrity verification schema for S3 Migration Tool

-- Add integrity columns to migration_tasks table
ALTER TABLE migration_tasks 
ADD COLUMN IF NOT EXISTS integrity_verified BOOLEAN DEFAULT FALSE,
ADD COLUMN IF NOT EXISTS integrity_errors TEXT,
ADD COLUMN IF NOT EXISTS verified_objects BIGINT DEFAULT 0,
ADD COLUMN IF NOT EXISTS failed_objects BIGINT DEFAULT 0,
ADD COLUMN IF NOT EXISTS integrity_rate DECIMAL(5,2) DEFAULT 0.00;

-- Create integrity_results table for detailed tracking
CREATE TABLE IF NOT EXISTS integrity_results (
    id SERIAL PRIMARY KEY,
    task_id UUID NOT NULL,
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
    
    -- Indexes
    CONSTRAINT fk_task FOREIGN KEY (task_id) REFERENCES migration_tasks(id) ON DELETE CASCADE
);

-- Create indexes for faster queries
CREATE INDEX IF NOT EXISTS idx_integrity_task_id ON integrity_results(task_id);
CREATE INDEX IF NOT EXISTS idx_integrity_is_valid ON integrity_results(is_valid);
CREATE INDEX IF NOT EXISTS idx_integrity_created_at ON integrity_results(created_at);
CREATE INDEX IF NOT EXISTS idx_integrity_task_valid ON integrity_results(task_id, is_valid);

-- Create view for integrity summary
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

