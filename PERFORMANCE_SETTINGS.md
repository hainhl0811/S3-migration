# Performance Settings Guide

## 🎯 **Current Configuration (Balanced)**

### **Memory Settings**
```yaml
GOMEMLIMIT: 1800MiB          # 90% of 2Gi limit
GOGC: 100                    # Default garbage collection
Memory Limit: 2Gi            # Pod memory limit
Memory per Worker: 5 MiB     # Estimated memory per worker
Safe Threshold: 80%          # Use 80% of available memory
```

### **Calculated Worker Capacity**
```
Available Memory: 1800 MiB × 0.80 = 1440 MiB
Workers: 1440 MiB ÷ 5 MiB = ~288 workers
```

---

## 📊 **Performance History**

### **Version 1: Too Conservative** ❌
```yaml
Memory per Worker: 50 MiB
Safe Threshold: 70%
Result: Only 20 workers → Too slow
```

### **Version 2: Too Aggressive** ❌
```yaml
GOMEMLIMIT: 3500MiB
Memory Limit: 4Gi
Memory per Worker: 2 MiB
Safe Threshold: 90%
Result: Network dropped from 200Mbps to KB/s → Nodes crashed
```

### **Version 3: Too Conservative (Revert)** ⚠️
```yaml
GOMEMLIMIT: 1800MiB
Memory per Worker: 10 MiB
Safe Threshold: 70%
Result: Only 126 workers → Still slow
```

### **Version 4: Balanced (Current)** ✅
```yaml
GOMEMLIMIT: 1800MiB
Memory per Worker: 5 MiB
Safe Threshold: 80%
Result: ~288 workers → Should be optimal
```

---

## 🔧 **Tuning Guidelines**

### **If Performance is Too Slow**
1. **Increase worker count**:
   - Reduce `estimatedPerWorker` (5 MiB → 3 MiB)
   - Increase `safeThresholdPct` (0.80 → 0.85)
   
2. **Increase memory allocation**:
   - Increase `GOMEMLIMIT` (1800MiB → 2400MiB)
   - Increase `memory.limits` (2Gi → 3Gi)

### **If Pods are Crashing/Unstable**
1. **Reduce worker count**:
   - Increase `estimatedPerWorker` (5 MiB → 10 MiB)
   - Decrease `safeThresholdPct` (0.80 → 0.70)

2. **Reduce memory pressure**:
   - Decrease `GOMEMLIMIT` (1800MiB → 1200MiB)
   - Reduce `safeThresholdPct` (0.80 → 0.70)

### **If Network is Bottleneck**
- **Fix network infrastructure first** - No amount of worker tuning will help
- Current symptoms: Discovery fast, but transfer speed terrible
- Network tests show: Download ~0.037 MB/s, Upload ~0.0001 MB/s

---

## 📈 **Expected Performance**

### **With Current Settings (288 workers)**
```
For 100KB objects:
- 288 workers × 10 objects/sec = 2,880 objects/sec
- 2,880 obj/s × 100KB = 288 MB/s network requirement

For 1M objects:
- 1,000,000 ÷ 2,880 = 347 seconds = 5.8 minutes
```

### **Network Bottleneck Impact**
```
With 0.037 MB/s download speed:
- 0.037 MB/s ÷ 0.1 MB/object = 0.37 objects/sec
- 1,000,000 ÷ 0.37 = 2,702,703 seconds = 31 days!
```

---

## ⚠️ **Current Issue: Network Bottleneck**

The migration is slow **NOT because of worker configuration** but because of network issues:

### **Symptoms**:
- Discovery phase: Fast ✅
- Transfer phase: Extremely slow ❌
- Network speed: KB/s instead of MB/s

### **Root Cause**:
Network infrastructure bottleneck within the Kubernetes cluster

### **Solution**:
Fix network infrastructure before tuning worker settings

---

## 🎯 **Recommended Actions**

1. **Fix Network Infrastructure** (Priority 1)
   - Check network bandwidth between pods and S3 endpoints
   - Verify no network throttling or QoS limitations
   - Test direct network speed from cluster nodes

2. **Monitor After Network Fix** (Priority 2)
   - Observe worker count and memory usage
   - Adjust settings based on actual performance
   - Gradually tune for optimal throughput

3. **Tune Worker Settings** (Priority 3)
   - Only after network is fixed
   - Start with current balanced settings
   - Adjust based on monitoring data

---

## 📋 **Quick Reference**

| Setting | Conservative | Balanced | Aggressive |
|---------|-------------|----------|-----------|
| **GOMEMLIMIT** | 1200MiB | 1800MiB | 3500MiB |
| **Memory Limit** | 2Gi | 2Gi | 4Gi |
| **Per Worker** | 10 MiB | 5 MiB | 2 MiB |
| **Threshold** | 70% | 80% | 90% |
| **Workers** | ~84 | ~288 | ~1575 |
| **Stability** | Very Stable | Stable | Unstable |
| **Performance** | Slow | Good | Very Fast* |

*If network can handle it

---

## 🚀 **Current Status**

**Configuration**: Balanced (Version 4)
**Workers**: ~288 workers possible
**Network**: **BOTTLENECK** - needs fixing
**Action Required**: Fix network infrastructure first
