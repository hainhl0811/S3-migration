# Single Worker Mode - v2.4.1

## Status: ABSOLUTE MINIMUM CONFIGURATION

Even with 3 workers, the pod exceeded the 2Gi memory limit. This is the **last resort** configuration.

## Configuration (v2.4.1-single-worker)

### Upload Workers: 1
**File**: `pkg/providers/googledrive/migrator.go` line 95
```go
numCopyWorkers := 1 // Single worker - absolute minimum
```

**Progression**:
- v2.3.6: 50 workers → OOM (node crashed)
- v2.3.7: 25 workers → OOM (node crashed)
- v2.3.8: 10 workers → OOM (node crashed)
- v2.4.0: 3 workers → OOM (pod killed, node survived ✅)
- **v2.4.1: 1 worker** → Testing now

### Discovery Workers: 2
**File**: `pkg/providers/googledrive/migrator.go` line 810
```go
const maxConcurrentFolders = 2 // Reduced from 10
```

### HTTP Connections: 4
**File**: `pkg/providers/googledrive/client.go` lines 90-91
```go
MaxIdleConns:        4  // Absolute minimum
MaxIdleConnsPerHost: 2  // Absolute minimum
```

### Other Settings
- **File Buffering**: 0 (all streaming)
- **Memory Limit**: 2 Gi
- **GOMEMLIMIT**: 1800 MiB
- **GOGC**: 50 (aggressive GC)
- **Auto-GC Trigger**: 1000 MB

## Expected Performance

### Throughput
```
1 worker = processes 1 file at a time
Estimated speed: 5-15 MB/s (depends on file size and network)
For 100 GB migration: ~2-6 hours
```

### Memory Usage
```
Single worker:           ~200-400 MB
Discovery (2 workers):   ~100 MB
Application overhead:    ~300 MB
Headroom for GC:         ~900 MB
────────────────────────────────────
Estimated total:         ~600-800 MB
Memory limit:            2048 MB (2 Gi)
Safety margin:           ~1.2-1.4 GB (60-70%)
```

## Why This Should Work

1. **One file at a time** - Minimal concurrent memory allocation
2. **Pure streaming** - No file buffering in memory
3. **Minimal HTTP pool** - Only 4 connections max
4. **Aggressive GC** - GOGC=50 + manual GC at 1GB
5. **Runtime limit** - GOMEMLIMIT=1800MiB stops Go before K8s limit
6. **K8s limit enforced** - 2Gi fits in node capacity

## If This Still Fails

If even 1 worker causes OOM, the issue is NOT worker count. It would indicate:

### 1. Memory Leak in Google Drive API
**Symptom**: Memory climbs steadily over time
**Solution**: Need to investigate Drive client library

### 2. Extremely Large Files
**Symptom**: OOM on specific files
**Solution**: Need to add file size filtering

### 3. Metadata Accumulation
**Symptom**: Discovery phase uses excessive memory
**Solution**: Need to stream discovery results to disk

### 4. HTTP Response Buffering
**Symptom**: Memory spikes during large file transfers
**Solution**: Need custom HTTP transport with no buffering

## Monitoring

### Watch Memory Usage
```bash
# Real-time pod memory
kubectl top pods -n s3-migration --watch

# Memory in logs (should show ~600-800MB)
kubectl logs -f -n s3-migration -l app=s3-migration | grep "Memory:"
```

### Expected Log Pattern
```
📊 Bandwidth: 8.5 MB/s | Memory: 450.3 MB | Total: 0.5 GB transferred
📊 Bandwidth: 10.2 MB/s | Memory: 580.1 MB | Total: 1.2 GB transferred
📊 Bandwidth: 9.8 MB/s | Memory: 620.5 MB | Total: 2.3 GB transferred
```

### Warning Signs
```
📊 Memory: 1100.2 MB  ⚠️ Getting high, but GC should kick in
🗑️  Forced garbage collection (memory was 1123.4 MB)  ✅ GC working
📊 Memory: 1650.8 MB  🚨 Approaching limit
📊 Memory: 1850.3 MB  🚨🚨 Near GOMEMLIMIT
```

## Alternative Solutions (If Single Worker Fails)

### Option 1: Chunk Discovery
Instead of loading all files into memory during discovery, write them to disk:
```go
// Write discovered files to temp file
// Read and process one at a time
```

### Option 2: Increase Node RAM
Upgrade Kubernetes nodes from 8GB to 16GB or 32GB

### Option 3: External Queue
Use Redis/RabbitMQ to queue files instead of in-memory

### Option 4: Different Approach
- Use `rclone` (written in Go, proven memory efficiency)
- Chunked/resumable uploads (process large files in pieces)
- Separate discovery and upload into different pods

## Summary

**Current Configuration**:
- ✅ 1 upload worker (absolute minimum)
- ✅ 2 discovery workers
- ✅ Pure streaming (no buffering)
- ✅ 2Gi memory limit (enforced)
- ✅ GOMEMLIMIT=1800MiB (runtime enforcement)
- ✅ Aggressive GC

**Expected Result**:
- Memory usage: ~600-800 MB (well within 2Gi)
- Throughput: 5-15 MB/s (slow but stable)
- Node safety: ✅ Protected

**If this fails**: The problem is a memory leak or architectural issue, not worker count.

## Version History

| Version | Workers | Memory Limit | Result |
|---------|---------|--------------|--------|
| v2.3.6 | 50 | 4 Gi | Node crash |
| v2.3.7 | 25 | 8 Gi | Node crash |
| v2.3.8 | 10 | 8 Gi | Node crash |
| v2.4.0 | 3 | 8 Gi | Node crash |
| v2.4.0-emergency | 3 | 2 Gi | Pod OOM (node OK ✅) |
| **v2.4.1-single-worker** | **1** | **2 Gi** | **Testing** |

This is the last line of defense. Monitor closely.

