# Setup Integrity Schema - Local PostgreSQL

## 🎯 How to Run the Integrity Schema in Local PostgreSQL

### **Method 1: Using psql Command Line** (Recommended)

#### **Step 1: Connect to your PostgreSQL database**
```bash
# Using your existing connection details from RDS_DEPLOYMENT.md
psql -h 172.16.1.107 -U s3migrator -d s3migration
```

When prompted, enter your password: `Hainhl@1q2w3e`

#### **Step 2: Run the schema file**
```sql
-- Once connected, run:
\i pkg/state/integrity_schema.sql
```

Or run it directly from command line:
```bash
psql -h 172.16.1.107 -U s3migrator -d s3migration -f pkg/state/integrity_schema.sql
```

#### **Step 3: Verify tables were created**
```sql
-- Check tables
\dt

-- Should see:
-- integrity_results
-- migration_tasks (already exists)

-- Check view
\dv

-- Should see:
-- integrity_summary
```

### **Method 2: Copy-Paste SQL** (If psql not available)

If you don't have `psql` command-line tool, you can use any PostgreSQL client:

#### **Using pgAdmin, DBeaver, or similar**:

1. **Connect to database**:
   - Host: `172.16.1.107`
   - Port: `5432`
   - Database: `s3migration`
   - User: `s3migrator`
   - Password: `Hainhl@1q2w3e`

2. **Open SQL Query window**

3. **Copy and paste this SQL**:

```sql
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
```

4. **Execute the SQL**

### **Method 3: Using Windows PowerShell** (Your current shell)

#### **Step 1: Install psql (if not installed)**
```powershell
# Check if psql is available
psql --version

# If not available, install PostgreSQL client tools:
# Download from: https://www.postgresql.org/download/windows/
# Or use chocolatey:
choco install postgresql
```

#### **Step 2: Run schema from PowerShell**
```powershell
# Navigate to your project directory (already there)
cd "C:\Users\Admin\Desktop\S3 migration"

# Run the schema
psql -h 172.16.1.107 -U s3migrator -d s3migration -f pkg\state\integrity_schema.sql
```

### **Method 4: From Go Application** (Automatic on startup)

You can also run the schema automatically when the application starts:

```go
// In cmd/server/main.go or during initialization

import (
    "database/sql"
    "os"
    "io/ioutil"
)

func initIntegritySchema(db *sql.DB) error {
    // Read schema file
    schemaSQL, err := ioutil.ReadFile("pkg/state/integrity_schema.sql")
    if err != nil {
        return fmt.Errorf("failed to read schema file: %w", err)
    }
    
    // Execute schema
    _, err = db.Exec(string(schemaSQL))
    if err != nil {
        return fmt.Errorf("failed to execute schema: %w", err)
    }
    
    fmt.Println("✅ Integrity schema initialized")
    return nil
}
```

## 📋 Verification Steps

### **After running the schema, verify it worked:**

```sql
-- 1. Check if columns were added to migration_tasks
SELECT column_name, data_type 
FROM information_schema.columns 
WHERE table_name = 'migration_tasks' 
AND column_name IN ('integrity_verified', 'verified_objects', 'failed_objects', 'integrity_rate');

-- Expected output:
-- integrity_verified | boolean
-- verified_objects   | bigint
-- failed_objects     | bigint
-- integrity_rate     | numeric

-- 2. Check if integrity_results table exists
SELECT table_name 
FROM information_schema.tables 
WHERE table_name = 'integrity_results';

-- Expected output:
-- integrity_results

-- 3. Check if indexes were created
SELECT indexname 
FROM pg_indexes 
WHERE tablename = 'integrity_results';

-- Expected output:
-- integrity_results_pkey
-- idx_integrity_task_id
-- idx_integrity_is_valid
-- idx_integrity_created_at
-- idx_integrity_task_valid

-- 4. Check if view was created
SELECT table_name 
FROM information_schema.views 
WHERE table_name = 'integrity_summary';

-- Expected output:
-- integrity_summary
```

## 🔧 Troubleshooting

### **Error: "relation migration_tasks does not exist"**

**Problem**: The migration_tasks table hasn't been created yet.

**Solution**: Run the main application schema first:
```sql
-- This should be created by pkg/state/db_manager.go automatically
-- But if not, ensure your application has run at least once
```

### **Error: "permission denied"**

**Problem**: User doesn't have permission to create tables.

**Solution**: Grant permissions:
```sql
-- Run as postgres superuser:
GRANT ALL PRIVILEGES ON DATABASE s3migration TO s3migrator;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO s3migrator;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO s3migrator;
```

### **Error: "column already exists"**

**Problem**: Schema already run before.

**Solution**: This is fine! The `IF NOT EXISTS` clauses prevent errors. You can safely re-run the schema.

### **Error: "connection refused"**

**Problem**: Can't connect to PostgreSQL.

**Solution**: Check:
```bash
# 1. PostgreSQL is running
# 2. Firewall allows connection
# 3. pg_hba.conf allows remote connections
# 4. postgresql.conf has listen_addresses = '*'
```

## ✅ Quick One-Line Setup

### **From PowerShell** (Windows):
```powershell
$env:PGPASSWORD="Hainhl@1q2w3e"; psql -h 172.16.1.107 -U s3migrator -d s3migration -f pkg\state\integrity_schema.sql
```

### **From Bash** (Linux/Mac):
```bash
PGPASSWORD="Hainhl@1q2w3e" psql -h 172.16.1.107 -U s3migrator -d s3migration -f pkg/state/integrity_schema.sql
```

## 📊 Expected Output

When successful, you should see:
```
ALTER TABLE
CREATE TABLE
CREATE INDEX
CREATE INDEX
CREATE INDEX
CREATE INDEX
CREATE VIEW
```

## 🎯 Next Steps

After running the schema:

1. **Verify tables exist** (see verification steps above)
2. **Enable integrity in code** (set `EnableIntegrity: true`)
3. **Run a test migration**
4. **Check integrity results**:
   ```sql
   SELECT * FROM integrity_summary;
   SELECT * FROM integrity_results LIMIT 10;
   ```

## 📝 Schema Summary

**What gets created:**

1. **New columns in migration_tasks**:
   - `integrity_verified`: Boolean flag
   - `verified_objects`: Count of verified objects
   - `failed_objects`: Count of failed objects
   - `integrity_rate`: Percentage of successful verifications

2. **New table: integrity_results**:
   - Detailed per-object verification tracking
   - Source and destination metadata
   - Calculated hashes (MD5, SHA1, SHA256, CRC32)
   - Verification results (etag_match, size_match, etc.)

3. **New view: integrity_summary**:
   - Aggregated metrics per task
   - Total/verified/failed object counts
   - Integrity rate percentage

4. **4 Indexes** for fast queries

---

**That's it! Your local PostgreSQL database is now ready for integrity verification!** ✅

