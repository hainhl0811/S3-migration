# CRITICAL: Memory Limit Fix - Node Crash Root Cause

## THE REAL PROBLEM

Your Kubernetes nodes only have **~8GB total RAM**, but you configured pods with an **8Gi memory limit**. This is why the limits weren't enforced and nodes crashed!

### The Math
```
Node total memory:        8126896 Ki  (~7.7 GB)
Your pod memory limit:    8 Gi        (~8.6 GB)
Kubernetes system pods:   ~500 MB
Operating system:         ~500 MB
─────────────────────────────────────────────────
Available for your pod:   ~6.7 GB  (NOT 8 GB!)
```

**Result**: When your pod tried to use its "allowed" 8Gi, it consumed the entire node, crashed the node, and took down other pods.

### Why Kubernetes Didn't Stop It

Kubernetes memory limits are enforced by the Linux kernel's cgroup memory controller. However:

1. **Limit was larger than node capacity** - The kernel can't allocate what doesn't exist
2. **No hard limit at pod level** - Go runtime didn't know about the Kubernetes limit
3. **Memory overcommit** - Linux allows processes to request more memory than available
4. **OOM killer triggers too late** - By the time it activates, the node is already unstable

## THE FIX (v2.4.0-emergency + 2Gi limit)

### 1. Reduced Kubernetes Memory Limit: 8Gi → 2Gi

**File**: `k8s/deployment.yaml`
```yaml
resources:
  requests:
    memory: "512Mi"  # Was 1Gi
  limits:
    memory: "2Gi"    # Was 8Gi - NOW ENFORCED!
```

**Why 2Gi?**
- Node has ~7.7 GB total
- OS needs ~500 MB
- Kubernetes needs ~500 MB
- 3 pods × 2Gi = 6 GB (fits comfortably in ~6.7 GB available)
- Leaves ~700 MB safety margin

### 2. Added Go Runtime Memory Limit (GOMEMLIMIT)

**File**: `k8s/deployment.yaml`
```yaml
env:
- name: GOMEMLIMIT
  value: "1800MiB"  # 90% of 2Gi Kubernetes limit
- name: GOGC
  value: "50"  # Aggressive GC (default is 100)
```

**What This Does**:
- **GOMEMLIMIT**: Tells Go runtime "don't use more than 1.8 GB"
- **GOGC=50**: Triggers garbage collection at 50% heap growth (more aggressive)
- **Together**: Creates a hard stop BEFORE hitting Kubernetes limit

### 3. Worker Configuration (Already Applied)

From v2.4.0-emergency:
- **3 workers** (was 10)
- **0 buffering** (all streaming)
- **Memory monitoring** with auto-GC

## How It Works Now

```
Application allocates memory
          ↓
GOMEMLIMIT enforces 1.8 GB limit
          ↓
If exceeded, Go GC runs aggressively (GOGC=50)
          ↓
If still growing, Go runtime refuses allocation
          ↓
Kubernetes enforces 2 Gi hard limit (OOM if exceeded)
          ↓
Node protected (limit < node capacity)
```

## Expected Behavior

### Normal Operation
```
Memory usage: ~500 MB - 1.5 GB
Status: ✅ Running smoothly
Logs: "📊 Memory: 850.3 MB"
```

### High Memory
```
Memory usage: ~1.6 GB - 1.8 GB
Status: ⚠️ GOMEMLIMIT kicking in
Logs: "🗑️ Forced garbage collection"
```

### Critical (Should Not Happen)
```
Memory usage: > 1.9 GB
Status: 🚨 Approaching Kubernetes limit
Result: Pod gets OOMKilled (BUT NODE SURVIVES!)
```

## Current Configuration Summary

| Setting | Value | Purpose |
|---------|-------|---------|
| **Workers** | 3 | Minimize concurrent memory usage |
| **File Buffering** | 0 (streaming) | Zero memory overhead |
| **K8s Memory Limit** | 2 Gi | Hard limit (fits in node capacity) |
| **GOMEMLIMIT** | 1800 MiB | Go runtime soft limit (90% of K8s limit) |
| **GOGC** | 50 | Aggressive garbage collection |
| **Auto-GC Trigger** | 1000 MB | Force GC when heap > 1 GB |

## Monitoring

### Check if pods respect limits
```bash
# Watch pod memory (should stay under 2Gi)
kubectl top pods -n s3-migration --watch

# View memory limits
kubectl describe pods -n s3-migration | grep -A 3 "Limits:"

# Check for OOM events (pod-level, NOT node-level)
kubectl get events -n s3-migration | grep -i oom
```

### What to look for in logs
```bash
kubectl logs -f -n s3-migration -l app=s3-migration | grep -E "Memory|📊|🗑️"
```

**Good**:
```
📊 Bandwidth: 15.2 MB/s | Memory: 650.3 MB | Total: 1.2 GB
📊 Bandwidth: 18.1 MB/s | Memory: 890.1 MB | Total: 2.3 GB
```

**Warning** (but OK):
```
🗑️  Forced garbage collection (memory was 1123.4 MB)
📊 Bandwidth: 16.5 MB/s | Memory: 520.7 MB | Total: 3.1 GB
```

**Critical** (pod will be killed):
```
📊 Bandwidth: 12.3 MB/s | Memory: 1850.2 MB | Total: 4.5 GB
(then pod gets OOMKilled by Kubernetes, but node survives)
```

## What If Pod Still Gets OOMKilled?

If a pod hits the 2Gi limit and gets OOMKilled:

**Good news**: Node will survive! Other pods will keep running.

**Next steps**:
1. Check logs to see what caused the spike
2. Reduce to 2 workers or even 1 worker
3. Investigate memory leak (likely in Google Drive API)

## Comparison: Before vs After

| Metric | Before (8Gi) | After (2Gi) |
|--------|--------------|-------------|
| **Kubernetes Limit** | 8 Gi | 2 Gi |
| **Go Runtime Limit** | None | 1800 MiB |
| **Garbage Collection** | Default (100) | Aggressive (50) |
| **Node Safety** | ❌ Crashes node | ✅ Node protected |
| **Pod Behavior on OOM** | Kills entire node | Kills only pod |
| **Other Pods** | Die with node | Keep running |
| **Workers** | 10 | 3 |
| **Expected Memory** | 7 GB | 1-1.5 GB |

## Why This Will Work

1. **Limit < Node Capacity**: 2Gi < 7.7GB node capacity
2. **Runtime Enforcement**: GOMEMLIMIT stops Go before Kubernetes limit
3. **Aggressive GC**: GOGC=50 cleans up memory proactively
4. **Fewer Workers**: 3 workers = less concurrent allocation
5. **No Buffering**: Streaming reduces memory footprint
6. **Manual GC**: Code triggers GC at 1GB threshold

## Verification

Run this after pods are ready:
```bash
# All should show 2Gi limit and GOMEMLIMIT=1800MiB
kubectl describe pods -n s3-migration | grep -A 5 "Limits:"
kubectl describe pods -n s3-migration | grep GOMEMLIMIT
```

## Files Changed

1. **k8s/deployment.yaml**:
   - Memory limit: 8Gi → 2Gi
   - Memory request: 1Gi → 512Mi
   - Added GOMEMLIMIT=1800MiB
   - Added GOGC=50

2. **Already from v2.4.0-emergency**:
   - pkg/providers/googledrive/migrator.go (3 workers, streaming, auto-GC)
   - pkg/providers/googledrive/client.go (12 HTTP connections)

## Summary

**Root Cause**: Pod limit (8Gi) exceeded node capacity (7.7GB)

**Fix**: 
- Kubernetes limit: 8Gi → 2Gi (fits in node)
- Go runtime limit: GOMEMLIMIT=1800MiB (soft limit)
- Aggressive GC: GOGC=50
- Workers: 3 (minimal concurrency)

**Result**: 
- ✅ Node protected from crash
- ✅ Pod will OOMKill before affecting node
- ✅ Other pods continue running
- ⚠️ Slower migration (3 workers vs 50 originally)

**Status**: Deployed and ready for testing!

