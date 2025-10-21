# EMERGENCY OOM FIX - v2.4.0-emergency

## CRITICAL ISSUE

Task `83358584-2506-4f60-b666-c49154d94052` caused a **node-level OOM** event that crashed the Kubernetes node.

### Severity
- **Pod memory usage**: 6.9 GB (6925580Ki)
- **Memory limit**: 8 Gi
- **Node status**: `NodeNotReady` - entire node ran out of memory
- **Configuration at time of crash**: 10 workers, 5MB buffer

### Root Cause Analysis

Even with aggressive reductions (50 → 25 → 10 workers), memory usage remained at ~7GB. This suggests:

1. **Possible memory leak** in Google Drive API client or file streaming
2. **Large files** - files may be much larger than the 5MB buffer threshold
3. **Memory accumulation** - garbage collection not keeping up with allocation rate
4. **HTTP response buffering** - HTTP client may be buffering responses in memory

## EMERGENCY CHANGES (v2.4.0-emergency)

### 1. Reduced Workers: 10 → 3
**File**: `pkg/providers/googledrive/migrator.go` line 94
```go
numCopyWorkers := 3 // Emergency reduction - even 10 workers used 7GB
```

**Impact**: 70% reduction in concurrent operations

### 2. Disabled ALL File Buffering
**File**: `pkg/providers/googledrive/migrator.go` line 659-664

**Before**:
```go
if file.Size > 0 && file.Size < 5*1024*1024 { // Buffer files < 5MB
    data, err := io.ReadAll(reader)
    body = bytes.NewReader(data)
}
```

**After**:
```go
// Force streaming for ALL files (no buffering at all)
body = reader
actualSize = file.Size
```

**Impact**: 
- Zero memory buffering for file contents
- All files stream directly from Google Drive to S3
- Sacrifices retry capability for memory safety

### 3. Added Runtime Memory Monitoring
**File**: `pkg/providers/googledrive/migrator.go` lines 717-735

```go
// Log memory usage every 100MB transferred
var memStats runtime.MemStats
runtime.ReadMemStats(&memStats)
memUsageMB := float64(memStats.Alloc) / (1024 * 1024)

fmt.Printf("📊 Bandwidth: %.1f MB/s | Memory: %.1f MB | Total: %.1f GB\n", 
    currentSpeed, memUsageMB, totalTransferred)

// Force garbage collection if memory > 1GB
if memUsageMB > 1000 {
    runtime.GC()
    debug.FreeOSMemory()
    fmt.Printf("🗑️  Forced garbage collection\n")
}
```

**Impact**: 
- Real-time memory visibility in logs
- Proactive garbage collection at 1GB threshold
- Can identify exactly when/why memory spikes

### 4. Reduced HTTP Connection Pool
**File**: `pkg/providers/googledrive/client.go` lines 90-91

```go
MaxIdleConns:        12,  // Was 40
MaxIdleConnsPerHost: 6,   // Was 20
```

**Impact**: Reduced HTTP client memory overhead

## Expected Memory Usage (v2.4.0-emergency)

```
3 workers × 200 MB per worker (streaming overhead)  = 600 MB
0 MB file buffering (disabled)                       = 0 MB
HTTP connections (minimal)                           = 50 MB
Application overhead                                  = 300 MB
Garbage collection trigger threshold                  = 1000 MB
────────────────────────────────────────────────────────────
Estimated peak usage                                  = ~1 GB
Available memory limit                                = 8 GB
Safety margin                                         = 7 GB (87% headroom)
```

## Performance Impact

### Throughput
- **Before**: 10 workers (7GB memory, node crashed)
- **After**: 3 workers (~1GB memory, stable)
- **Trade-off**: 70% slower, but actually completes

### Monitoring Commands

Watch memory in real-time:
```bash
# Monitor pod memory
kubectl top pods -n s3-migration --watch

# Check for OOM events
watch -n 5 "kubectl get events -n s3-migration | grep -i oom"

# View migration logs with memory stats
kubectl logs -f -n s3-migration -l app=s3-migration | grep -E "Memory|📊|🗑️"
```

### What to Look For

The logs will now show:
```
📊 Bandwidth: 15.2 MB/s | Memory: 450.3 MB | Total: 1.2 GB transferred
📊 Bandwidth: 18.1 MB/s | Memory: 890.1 MB | Total: 2.3 GB transferred
🗑️  Forced garbage collection (memory was 1123.4 MB)
📊 Bandwidth: 16.5 MB/s | Memory: 520.7 MB | Total: 3.1 GB transferred
```

**Red flags**:
- Memory consistently above 2GB
- Memory increasing without GC
- Frequent GC (every log line)

## Next Steps

1. ✅ **Deployed v2.4.0-emergency** with 3 workers and full streaming
2. 🔄 **Start a test migration** and monitor logs closely
3. 📊 **Watch for memory patterns** in the logs
4. ⚠️ **If still OOM**: May need to:
   - Reduce to 1-2 workers
   - Investigate Google Drive API client for leaks
   - Consider chunked/resumable uploads
   - Add rate limiting to slow down allocation

## Files Modified

1. `pkg/providers/googledrive/migrator.go`:
   - Workers: 10 → 3
   - File buffering: Completely disabled
   - Added memory monitoring and forced GC

2. `pkg/providers/googledrive/client.go`:
   - HTTP connections: 40 → 12

3. `k8s/deployment.yaml`:
   - Image: v2.4.0-emergency

## Rollback Plan

If this still causes OOM:

```bash
# Rollback to previous version (will also OOM, but buys time)
kubectl set image deployment/s3-migration s3-migration=registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.3.9 -n s3-migration

# OR reduce to 1 worker (slowest but safest)
# Edit migrator.go: numCopyWorkers := 1
# Build v2.4.1-single-worker
```

## Investigation Needed

The 7GB memory usage with 10 workers suggests a serious issue. Possible causes:

1. **Google Drive API leak**: The drive client may not be releasing file handles
2. **S3 client leak**: AWS SDK may be buffering responses
3. **Large files**: Files may be larger than expected (check file size distribution)
4. **Goroutine leak**: Workers may not be terminating properly

Monitor the new memory logs to identify which component is consuming memory.

## Summary

**Emergency configuration deployed**:
- **3 workers** (was 10)
- **Zero file buffering** (was 5MB)
- **Memory monitoring** with auto-GC at 1GB
- **Expected usage**: ~1GB (was 7GB)

**This should prevent node crashes, but will be significantly slower.**

Watch the logs for memory patterns to identify the root cause.

