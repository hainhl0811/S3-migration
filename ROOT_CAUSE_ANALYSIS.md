# Root Cause Analysis - OOM Issue

## THE REAL PROBLEM

We were fixing the **WRONG code**! The system has TWO migration engines:

1. **Google Drive → S3** (`pkg/providers/googledrive/migrator.go`) - We were fixing this ✅
2. **S3 → S3** (`pkg/core/enhanced_migrator.go`) - **This was the actual problem! ❌**

## What We Discovered

### Issue #1: Wrong Migration Type
- User is doing **S3 → S3** migration
- We spent hours optimizing **Google Drive → S3** code
- The S3 → S3 code was never touched until now

### Issue #2: S3-to-S3 Used 100 Workers!
**File**: `pkg/tuning/tuner.go` line 50

**Before**:
```go
configs := map[models.WorkloadPattern]WorkerConfig{
    models.PatternManySmall:  {Min: 20, Max: 100, Default: 30},  // UP TO 100!
    models.PatternMixed:      {Min: 10, Max: 50, Default: 20},
    models.PatternLargeFiles: {Min: 3, Max: 15, Default: 5},
    models.PatternUnknown:    {Min: 5, Max: 50, Default: 10},
}
```

**After (v2.5.0)**:
```go
configs := map[models.WorkloadPattern]WorkerConfig{
    models.PatternManySmall:  {Min: 1, Max: 2, Default: 1},  // REDUCED!
    models.PatternMixed:      {Min: 1, Max: 2, Default: 1},
    models.PatternLargeFiles: {Min: 1, Max: 1, Default: 1},
    models.PatternUnknown:    {Min: 1, Max: 2, Default: 1},
}
```

### Issue #3: Buffering Entire Files in Memory!
**File**: `pkg/core/enhanced_migrator.go` line 696

**Before**:
```go
// Read the entire body into memory to ensure we can retry if needed
// The body stream can only be read once, so we buffer it
bodyBytes, err := io.ReadAll(getResp.Body)  // LOADS ENTIRE FILE!
...
Body: bytes.NewReader(bodyBytes),
```

**With 100 workers × average 50MB files = 5GB just for file buffers!**

**After (v2.5.0)**:
```go
// CRITICAL: Use streaming instead of buffering to prevent OOM
// DO NOT use io.ReadAll - stream directly from source to destination
putInput := &s3.PutObjectInput{
    Body: getResp.Body,  // Stream directly - no buffering!
    ContentLength: aws.Int64(objectSize),
}
```

### Issue #4: Memory Limit Larger Than Node
**File**: `k8s/deployment.yaml`

**Before**:
```yaml
memory: "8Gi"  # Pod limit
# BUT node only has 7.7GB total!
```

**After (v2.5.0)**:
```yaml
memory: "2Gi"  # Fits in node capacity
GOMEMLIMIT: "1800MiB"  # Go runtime enforcement
GOGC: "50"  # Aggressive garbage collection
```

## Memory Calculation

### Before (Why it Failed)
```
100 workers × 50MB average file = 5000 MB (file buffers)
+ HTTP connections overhead       = 500 MB
+ Application memory               = 500 MB
+ Metadata for discovery           = 500 MB
─────────────────────────────────────────────
Total estimated                    = 6500 MB (~6.5 GB)

BUT with spikes, peaks, GC delays = 7-9 GB
Node capacity                      = 7.7 GB
Result: NODE CRASH ❌
```

### After (Why it Should Work)
```
1-2 workers (no buffering)         = 200-400 MB
+ Minimal HTTP connections         = 100 MB
+ Application memory                = 300 MB
+ Metadata (limited)                = 200 MB
─────────────────────────────────────────────
Total estimated                     = 800-1000 MB

Peak with GC                        = 1.2-1.5 GB
GOMEMLIMIT enforcement              = 1.8 GB (hard stop)
Kubernetes limit                    = 2 GB (enforced)
Node capacity                       = 7.7 GB
Result: SAFE ✅
```

## All Changes Made (v2.5.0-s3-fix)

### 1. S3-to-S3 Worker Limits
**File**: `pkg/tuning/tuner.go`
- Max workers: 100 → 2
- Default workers: 30 → 1
- **Impact**: 98% reduction in concurrent operations

### 2. S3-to-S3 Streaming
**File**: `pkg/core/enhanced_migrator.go`
- Removed `io.ReadAll()`
- Stream directly from source to destination
- **Impact**: Zero file buffering

### 3. Memory Limits
**File**: `k8s/deployment.yaml`
- Kubernetes limit: 8Gi → 2Gi
- Added GOMEMLIMIT: 1800MiB
- Added GOGC: 50
- **Impact**: Limit now enforced, node protected

### 4. Google Drive Workers (Already Done)
**File**: `pkg/providers/googledrive/migrator.go`
- Workers: 50 → 1
- File buffer: 10MB → 0 (streaming)
- **Impact**: Consistent with S3-to-S3

## Why It Failed Before

| Problem | Impact | Fix |
|---------|--------|-----|
| **Wrong code fixed** | Optimized Google Drive, not S3 | Fixed S3 tuner |
| **100 workers** | 5GB+ file buffers | Reduced to 1-2 workers |
| **io.ReadAll()** | Entire files in memory | Stream directly |
| **8Gi limit > 7.7GB node** | Limit not enforced | 2Gi limit enforced |
| **No GOMEMLIMIT** | Go didn't know limit | 1800MiB hard stop |

## Current Configuration (v2.5.0-s3-fix)

### S3 → S3 Migration
- **Workers**: 1-2 (auto-tuned, max 2)
- **Buffering**: None (pure streaming)
- **Memory**: ~800MB-1.5GB expected

### Google Drive → S3 Migration
- **Workers**: 1
- **Buffering**: None (pure streaming)
- **Memory**: ~600MB-1GB expected

### Kubernetes
- **Memory Limit**: 2Gi (fits in 7.7GB node)
- **GOMEMLIMIT**: 1800MiB (Go enforcement)
- **GOGC**: 50 (aggressive GC)

## Expected Behavior

### Monitoring
```bash
kubectl top pods -n s3-migration --watch
```

**Expected output**:
```
NAME                            CPU   MEMORY
s3-migration-xxx                100m  850Mi   ✅ Normal
s3-migration-yyy                80m   920Mi   ✅ Normal
s3-migration-zzz                90m   780Mi   ✅ Normal
```

**If you see > 1.8Gi**: Something is still wrong, investigate logs

### Logs
```bash
kubectl logs -f -n s3-migration -l app=s3-migration
```

**Should show**:
```
Using 1 workers (auto-tuned for optimal performance)
[CROSS-ACCOUNT] Streaming to destination (no buffering)
```

## Performance Impact

### Throughput
- **Before**: 100 workers = fast (but crashes)
- **After**: 1-2 workers = slower but **STABLE**
- **Trade-off**: ~98% slower but migrations actually complete

### For 100GB Migration
- **Before**: ~30 minutes (then crash at 25% = wasted)
- **After**: ~3-6 hours (completes successfully ✅)

## Files Changed

1. **pkg/tuning/tuner.go** - Worker limits (100 → 2)
2. **pkg/core/enhanced_migrator.go** - Streaming instead of buffering
3. **k8s/deployment.yaml** - 2Gi limit + GOMEMLIMIT + GOGC
4. **pkg/providers/googledrive/migrator.go** - Already fixed (1 worker, streaming)
5. **pkg/providers/googledrive/client.go** - Already fixed (minimal connections)

## Summary

**Root Causes**:
1. ❌ Fixed wrong migration type (Google Drive vs S3)
2. ❌ 100 concurrent workers
3. ❌ Buffering entire files in memory
4. ❌ Memory limit > node capacity

**Fixes**:
1. ✅ Fixed S3-to-S3 tuner (1-2 workers max)
2. ✅ Streaming (no buffering)
3. ✅ 2Gi limit (< 7.7GB node)
4. ✅ GOMEMLIMIT enforcement

**Result**: Should now work with both S3 and Google Drive migrations within 2Gi limit.

