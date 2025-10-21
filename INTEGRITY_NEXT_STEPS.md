# Integrity Verification - Next Steps

## ✅ Database Schema Created!

Great! Your PostgreSQL database now has all the tables needed for integrity verification.

## 🚀 What to Do Next

### **Option 1: Test Locally First** (Recommended)

Before deploying to Kubernetes, test the integrity verification locally:

#### **Step 1: Build the application**
```bash
go build -o s3migration.exe cmd/server/main.go
```

#### **Step 2: Set environment variables**
```powershell
# Database connection
$env:DB_DRIVER="postgres"
$env:DB_CONNECTION_STRING="host=172.16.1.107 port=5432 user=s3migrator password=Hainhl@1q2w3e dbname=s3migration sslmode=disable"

# Google OAuth (if using Google Drive)
$env:GOOGLE_CLIENT_ID="your-client-id"
$env:GOOGLE_CLIENT_SECRET="your-client-secret"

# Port
$env:PORT="8000"
$env:GIN_MODE="release"
```

#### **Step 3: Run the application**
```powershell
.\s3migration.exe
```

#### **Step 4: Open browser**
```
http://localhost:8000
```

#### **Step 5: Test a small S3 migration**
1. Go to the migration tab
2. Enter source and destination S3 credentials
3. Start a migration with a few files
4. Check the logs for `[INTEGRITY]` messages

#### **Step 6: Check integrity results**
```powershell
# Check integrity summary
$env:PGPASSWORD="Hainhl@1q2w3e"; psql -h 172.16.1.107 -U s3migrator -d s3migration -c "SELECT * FROM integrity_summary;"

# Check detailed results
$env:PGPASSWORD="Hainhl@1q2w3e"; psql -h 172.16.1.107 -U s3migrator -d s3migration -c "SELECT * FROM integrity_results LIMIT 10;"
```

### **Option 2: Deploy to Kubernetes** (Production)

If you're ready to deploy:

#### **Step 1: Build and push Docker image**
```bash
# Build with new integrity features
docker build --platform linux/amd64 -t registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.7.0-integrity .

# Push to registry
docker push registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.7.0-integrity
```

#### **Step 2: Update Kubernetes deployment**
```bash
# Update deployment.yaml image version
# Change: image: registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.7.0-integrity

# Apply changes
kubectl apply -f k8s/deployment.yaml -n s3-migration

# Or use set image
kubectl set image deployment/s3-migration s3-migration=registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.7.0-integrity -n s3-migration
```

#### **Step 3: Verify deployment**
```bash
# Check pods
kubectl get pods -n s3-migration

# Check logs for integrity messages
kubectl logs -n s3-migration -l app=s3-migration --tail=100 | grep INTEGRITY
```

## 🔧 How to Enable Integrity Verification

Currently, integrity verification is implemented but **NOT enabled by default**. You need to enable it in the code:

### **Method 1: Enable in handlers.go** (Quick way)

Find this section in `api/handlers.go` (around line 400-500 in StartMigration function):

```go
// Create enhanced migrator
config := core.EnhancedMigratorConfig{
    Region:             sourceRegion,
    EndpointURL:        sourceEndpoint,
    ConnectionPoolSize: 10,
    EnableStreaming:    true,
    EnablePrefetch:     false,
    // ADD THESE LINES:
    EnableIntegrity:    true,  // ← Enable integrity verification
    TaskID:             taskID,
    IntegrityManager:   integrityManager,
    // ... rest of config
}
```

You'll also need to create the integrity manager:

```go
// In StartMigration function, before creating EnhancedMigrator:

// Get database connection for integrity manager
dbManager, ok := taskManager.stateManager.(*state.DBStateManager)
if !ok {
    c.JSON(http.StatusInternalServerError, gin.H{"error": "database not available"})
    return
}

// Create integrity manager
integrityManager := state.NewIntegrityManager(dbManager.GetDB())
```

### **Method 2: Add UI Toggle** (Better way)

Add a checkbox in the web UI to enable/disable integrity:

```html
<!-- In web/index.html, add to S3 migration form: -->
<div class="form-group">
    <label>
        <input type="checkbox" id="enableIntegrity" checked>
        Enable Integrity Verification (MD5, SHA256, ETag)
    </label>
</div>
```

Then pass it in the API request and use it in the handler.

## 📊 How to Check Integrity Results

### **Via API**

```bash
# Get integrity summary for a task
curl http://localhost:8000/api/tasks/{taskId}/integrity

# Get detailed integrity report
curl http://localhost:8000/api/tasks/{taskId}/integrity/report

# Get failed objects
curl http://localhost:8000/api/tasks/{taskId}/integrity/failures?limit=100
```

### **Via Database**

```sql
-- Get integrity summary
SELECT * FROM integrity_summary;

-- Get failed verifications
SELECT * FROM integrity_results WHERE is_valid = false;

-- Get statistics
SELECT 
    source_provider,
    dest_provider,
    COUNT(*) as total,
    SUM(CASE WHEN is_valid THEN 1 ELSE 0 END) as verified,
    SUM(CASE WHEN NOT is_valid THEN 1 ELSE 0 END) as failed,
    ROUND(100.0 * SUM(CASE WHEN is_valid THEN 1 ELSE 0 END) / COUNT(*), 2) as success_rate
FROM integrity_results
GROUP BY source_provider, dest_provider;
```

## 🎯 What to Expect in Logs

When integrity is enabled, you'll see logs like:

```
[CROSS-ACCOUNT] Downloading from source: my-bucket/file.txt (size: 1048576 bytes)
[INTEGRITY] Enabling streaming integrity verification
[CROSS-ACCOUNT] Streaming to destination (no buffering): dest-bucket/file.txt
[CROSS-ACCOUNT] PutObject request: Bucket=dest-bucket, Key=file.txt, Size=1048576
[INTEGRITY] ✅ Verified: file.txt (MD5: abc123def456, Size: 1048576 bytes)
[CROSS-ACCOUNT] Successfully copied to destination
```

Or if integrity fails:

```
[INTEGRITY] ❌ FAILED: file.txt - ETag mismatch; Size mismatch: source=1048576, dest=1048500
```

## 🔍 Testing Checklist

### **1. Small File Test** (< 5MB)
- Upload a small file
- Check logs for `[INTEGRITY] ✅ Verified`
- Verify in database: `SELECT * FROM integrity_results WHERE task_id = 'your-task-id';`

### **2. Large File Test** (> 100MB)
- Upload a large file
- Verify streaming works (no OOM)
- Check integrity results

### **3. Multiple Files Test**
- Upload 10-20 files
- Check integrity summary: `SELECT * FROM integrity_summary;`
- Verify integrity_rate is 100%

### **4. Different Providers Test**
- Test with different S3-compatible providers:
  - AWS S3 → AWS S3
  - MinIO → MinIO
  - Wasabi → Wasabi
  - AWS S3 → MinIO (cross-provider)

## 📝 Quick Test Script

```powershell
# 1. Check schema exists
$env:PGPASSWORD="Hainhl@1q2w3e"; psql -h 172.16.1.107 -U s3migrator -d s3migration -c "\dt"

# Expected: migration_tasks, integrity_results

# 2. Check columns
$env:PGPASSWORD="Hainhl@1q2w3e"; psql -h 172.16.1.107 -U s3migrator -d s3migration -c "SELECT column_name FROM information_schema.columns WHERE table_name='migration_tasks' AND column_name LIKE 'integrity%';"

# Expected: integrity_verified, integrity_errors, integrity_rate

# 3. Check view
$env:PGPASSWORD="Hainhl@1q2w3e"; psql -h 172.16.1.107 -U s3migrator -d s3migration -c "\dv"

# Expected: integrity_summary

# 4. Check if data exists (after running a migration)
$env:PGPASSWORD="Hainhl@1q2w3e"; psql -h 172.16.1.107 -U s3migrator -d s3migration -c "SELECT COUNT(*) FROM integrity_results;"
```

## 🚦 Current Status

### ✅ Completed
- [x] Streaming hash calculator (MD5, SHA1, SHA256, CRC32)
- [x] Database schema (migration_tasks + integrity_results)
- [x] Integrity manager (store/retrieve results)
- [x] Enhanced migrator integration (TeeReader streaming)
- [x] API endpoints (/api/tasks/:id/integrity)
- [x] Provider detection (AWS, MinIO, Wasabi, B2, R2, DO)

### ⏳ Pending
- [ ] Enable integrity by default (currently disabled)
- [ ] Web UI integration (display integrity metrics)
- [ ] Testing with real S3 providers
- [ ] Documentation update

## 🎯 Recommended Next Steps

1. **Test locally** with a small S3 migration (Option 1 above)
2. **Enable integrity** in the code (Method 1 above)
3. **Verify it works** by checking logs and database
4. **Deploy to Kubernetes** when confident (Option 2 above)
5. **Monitor** integrity results in production

## ❓ Need Help?

If you encounter any issues:

1. **Check logs** for `[INTEGRITY]` messages
2. **Check database**: `SELECT * FROM integrity_results;`
3. **Verify schema**: Run the verification queries above
4. **Test API**: `curl http://localhost:8000/api/tasks/{id}/integrity`

---

**You're all set! The foundation is complete. Now you just need to enable and test it!** ✅

