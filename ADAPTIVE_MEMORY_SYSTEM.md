# Adaptive Memory Management System - v2.6.0

## Overview

Instead of hardcoding worker limits, the system now **automatically calculates** optimal worker count based on:
- Available memory (GOMEMLIMIT or Kubernetes limit)
- Current memory usage
- Estimated memory per worker
- Workload pattern

## How It Works

### 1. Memory Manager (`pkg/adaptive/memory_manager.go`)

**Initialization**:
```go
// Reads GOMEMLIMIT from environment (set by Kubernetes)
maxMemory = 1800 MiB  (from GOMEMLIMIT)
safeThreshold = 70% = 1260 MiB
estimatedPerWorker = 100 MiB (initial guess)

maxWorkers = 1260 / 100 = 12 workers (calculated, not hardcoded!)
```

**Runtime Adjustment**:
```
Every 30 seconds:
1. Check current memory usage
2. Calculate: availableMemory = safeThreshold - currentUsage
3. Calculate: possibleWorkers = availableMemory / estimatedPerWorker
4. Gradually adjust workers (max +2 or -1 per adjustment)
5. Update estimatedPerWorker based on actual usage
```

### 2. Integration with Tuner (`pkg/tuning/tuner.go`)

**Priority Order** (Most Important First):
1. **Memory Constraints** - Never exceed memory limits
2. Network Conditions - Adjust for poor network
3. Performance Optimization - Maximize throughput

```go
func GetOptimalWorkers() int {
    // Step 1: Check memory FIRST
    memorySafeWorkers := memoryManager.GetOptimalWorkers()
    
    // Step 2: Calculate performance-optimal workers
    performanceOptimal := calculateBasedOnPattern()
    
    // Step 3: Memory wins!
    if performanceOptimal > memorySafeWorkers {
        return memorySafeWorkers  // Capped by memory
    }
    
    return performanceOptimal
}
```

## Key Features

### 1. Self-Calibrating
- Starts with 1 worker
- Monitors actual memory usage per worker
- Gradually increases if memory allows
- Automatically reduces if memory is high

### 2. Proactive GC
- Triggers garbage collection at 60% of safe threshold
- Prevents memory from reaching critical levels
- Logs GC activity for debugging

### 3. Dynamic Limits
Worker limits adapt to environment:

**Scenario A: 2Gi Memory Limit**
```
GOMEMLIMIT = 1800 MiB
Safe threshold (70%) = 1260 MiB
Initial estimate: 100 MiB/worker
Max workers = 12
```

**Scenario B: 4Gi Memory Limit**
```
GOMEMLIMIT = 3600 MiB
Safe threshold (70%) = 2520 MiB
Initial estimate: 100 MiB/worker
Max workers = 25
```

**Scenario C: Actual Usage is Higher**
```
After running:
Observed: 200 MiB/worker (not 100)
Max workers recalculated = 1260 / 200 = 6 workers
System auto-adjusts down
```

### 4. Real-Time Monitoring
Logs show memory-aware decisions:
```
🧠 Memory Manager initialized:
   Max Memory: 1800 MiB
   Safe Threshold: 70% (1260 MiB)
   Estimated per worker: 100 MiB
   Max workers allowed: 12

🧠 Memory: 450 MiB / 1800 MiB (25.0%) | Workers: 2 | Est per worker: 120 MiB

⚠️  Worker count capped at 8 by memory manager
🧠 Memory: 980 MiB / 1800 MiB (54.4%) | Workers: 8 | Est per worker: 122 MiB

🗑️  GC triggered (high memory usage): freed 150 MiB (was 1100 MiB, now 950 MiB)
```

## Configuration

### Kubernetes (No Changes Needed!)
```yaml
env:
- name: GOMEMLIMIT
  value: "1800MiB"  # System reads this automatically

resources:
  limits:
    memory: "2Gi"  # Enforced by Kubernetes
```

### Tuning Parameters (Optional)

**Adjust Safe Threshold** (default 70%):
```go
memoryManager.SetSafeThreshold(0.80)  // Use 80% instead of 70%
```

**Why 70% is Good**:
- Leaves 30% buffer for:
  - Garbage collection overhead
  - Temporary spikes
  - OS and other processes

## Comparison: Before vs After

### Before (Hardcoded)
```go
// pkg/tuning/tuner.go
configs := map[models.WorkloadPattern]WorkerConfig{
    PatternManySmall: {Min: 1, Max: 2, Default: 1},  // HARDCODED!
}
```

**Problems**:
- ❌ Doesn't adapt to different node sizes
- ❌ Wastes resources on larger nodes
- ❌ Still OOMs if files are larger than expected
- ❌ Requires manual tuning for each environment

### After (Adaptive - v2.6.0)
```go
// Automatically calculated based on GOMEMLIMIT
maxWorkers := memoryManager.GetMaxWorkers()
configs := map[models.WorkloadPattern]WorkerConfig{
    PatternManySmall: {Min: 1, Max: maxWorkers, Default: min(maxWorkers, 2)},
}
```

**Benefits**:
- ✅ Adapts to any node size automatically
- ✅ Maximizes resource usage safely
- ✅ Self-corrects if memory usage is higher than estimated
- ✅ Works in any environment (dev, staging, prod)

## Memory Calculation Example

**Scenario: 100 files to migrate on 2Gi pod**

```
Initial State:
- GOMEMLIMIT: 1800 MiB
- Safe threshold: 1260 MiB
- Current usage: 300 MiB
- Available: 960 MiB
- Estimated per worker: 100 MiB
- Start with: 1 worker

After 10 files (actual usage observed):
- Current usage: 420 MiB (1 worker used 120 MiB, not 100)
- Updated estimate: 120 MiB/worker
- Available: 840 MiB
- Can add: 840 / 120 = 7 workers
- Increase to: 3 workers (gradual +2)

After 30 files:
- Current usage: 660 MiB (3 workers using 220 MiB/worker!)
- Updated estimate: 220 MiB/worker (files are larger)
- Available: 600 MiB
- Can only support: 600 / 220 = 2.7 ≈ 2 workers
- Reduce to: 2 workers (gradual -1)

Continue migration:
- System stabilizes at 2 workers
- Each using ~220 MiB
- Total: 440-500 MiB
- Safe within 1260 MiB threshold
```

## Benefits for Your Use Case

### 1. Node Flexibility
- **Current**: 7.7 GB nodes with 2Gi pods
- **Future**: Upgrade to 16 GB nodes → automatically uses more workers
- **Dev**: Smaller nodes → automatically uses fewer workers

### 2. Workload Adaptation
- **Small files**: More workers (low memory per worker)
- **Large files**: Fewer workers (high memory per worker)
- **Mixed**: Dynamically adjusts

### 3. Safety
- **Memory spike**: Workers automatically reduced
- **Low usage**: Workers gradually increased
- **OOM prevention**: GC triggered proactively

### 4. No Manual Tuning
- **Before**: Edit code, rebuild, redeploy for each environment
- **After**: One build works everywhere

## Monitoring

### Check Adaptive Behavior
```bash
# Watch workers adjust in real-time
kubectl logs -f -n s3-migration -l app=s3-migration | grep "🧠"

# Expected output:
🧠 Memory Manager initialized: Max Memory: 1800 MiB, Max workers: 12
🧠 Memory: 450 MiB / 1800 MiB (25.0%) | Workers: 2
🧠 Memory: 680 MiB / 1800 MiB (37.8%) | Workers: 3
⚠️  Worker count capped at 3 by memory manager
```

### Verify Memory Safety
```bash
kubectl top pods -n s3-migration

# Should stay well below limit:
NAME                     CPU   MEMORY
s3-migration-xxx         80m   950Mi   ✅ (below 1260Mi safe threshold)
```

## Summary

**Old Approach** (v2.5.0 and earlier):
- Hardcoded: `Max: 2 workers`
- One size fits all
- Manual tuning required

**New Approach** (v2.6.0):
- **Calculated**: `Max = (GOMEMLIMIT × 70%) / estimatedPerWorker`
- Adapts to environment
- Self-tuning

**Result**:
- ✅ Automatically safe on any node size
- ✅ Maximizes performance within memory limits
- ✅ No more manual worker configuration
- ✅ Works from dev (small nodes) to prod (large nodes)

The system is now **tactical and efficient** rather than hardcoded! 🎯

