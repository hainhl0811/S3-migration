# Worker Configuration Guide

## Current Configuration (v2.3.9)

### Upload Workers
- **Location**: `pkg/providers/googledrive/migrator.go` line 95
- **Current Value**: `10` workers
- **Previous Values**: `50` workers (OOM), then `25` workers (still OOM)
- **Memory Impact**: Each worker buffers file data in memory during upload

### Discovery Workers
- **Location**: `pkg/providers/googledrive/migrator.go` line 805
- **Current Value**: `10` workers (concurrent folder processing)
- **Memory Impact**: Lower than upload workers, primarily metadata

### HTTP Connection Pool
- **Location**: `pkg/providers/googledrive/client.go` lines 90-91
- **MaxIdleConns**: `40` (adjusted for 10 workers)
- **MaxIdleConnsPerHost**: `20` (supports 10 concurrent downloads)

### File Buffer Size
- **Location**: `pkg/providers/googledrive/migrator.go` line 660
- **Current Value**: `5 MB` (files under 5MB buffered in memory)
- **Previous Value**: `10 MB` (contributed to OOM)
- **Memory Impact**: `10 workers × 5 MB = 50 MB` buffer usage (vs 250 MB at 10MB)

### Kubernetes Resources
- **Memory Request**: `1Gi`
- **Memory Limit**: `8Gi`
- **CPU Request**: `500m`
- **CPU Limit**: `2000m` (2 cores)

## Why the Change?

### Problem
Pod was **OOMKilled** (Out Of Memory) during migration with 50 workers and 4Gi memory limit:
- Task `b52aad1c-f924-430b-ac2a-4bbd00bda254` crashed at 25% progress
- Pod restart caused migration interruption
- High memory consumption from concurrent file buffering

### Solution
1. **Increased memory limit**: 4Gi → 8Gi
2. **Reduced worker count**: 50 → 25 workers
3. **Adjusted HTTP pool**: Connections scaled proportionally

## Performance Guidelines

### Memory Calculation
```
Estimated Memory per Worker with 5MB buffer: ~100-200 MB
10 workers × 200 MB = ~2 GB peak usage
+ File buffers: 10 × 5 MB = 50 MB
+ Application overhead: ~500 MB
≈ 2.5 GB total (well within 8Gi limit)
```

### Tuning Workers

#### To INCREASE Throughput (if you have more memory):
```go
// In pkg/providers/googledrive/migrator.go line 95
numCopyWorkers := 40 // For 12-16Gi memory pods

// In pkg/providers/googledrive/client.go lines 90-91
MaxIdleConns:        160  // 40 workers × 4
MaxIdleConnsPerHost: 80   // 40 workers × 2
```

#### To DECREASE Memory Usage (if OOM occurs):
```go
// In pkg/providers/googledrive/migrator.go line 95
numCopyWorkers := 15 // For 4-6Gi memory pods

// In pkg/providers/googledrive/client.go lines 90-91
MaxIdleConns:        60   // 15 workers × 4
MaxIdleConnsPerHost: 30   // 15 workers × 2
```

## Monitoring

### Check Memory Usage
```bash
# Real-time pod memory monitoring
kubectl top pods -n s3-migration

# Check for OOMKilled events
kubectl get events -n s3-migration | grep OOM

# View pod resource limits
kubectl get pods -n s3-migration -o custom-columns=NAME:.metadata.name,MEMORY:.spec.containers[0].resources.limits.memory
```

### Signs You Need to Reduce Workers
- Pods show "OOMKilled" in events
- Pods restart during migrations
- `kubectl top pods` shows memory near limit (>90%)

### Signs You Can Increase Workers
- `kubectl top pods` shows low memory usage (<50%)
- You have more memory available in your cluster
- Migration speed is slower than network bandwidth

## Deployment Process

After changing worker configuration:

```bash
# 1. Build new image
docker build --platform linux/amd64 -t registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.3.X .

# 2. Push image
docker push registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.3.X

# 3. Update deployment.yaml
# Change image tag to v2.3.X

# 4. Apply changes
kubectl apply -f k8s/deployment.yaml

# 5. Monitor rollout
kubectl rollout status deployment/s3-migration -n s3-migration

# 6. Verify new configuration
kubectl get pods -n s3-migration -o custom-columns=NAME:.metadata.name,IMAGE:.spec.containers[0].image,MEMORY:.spec.containers[0].resources.limits.memory
```

## Best Practices

1. **Start Conservative**: Begin with lower worker counts and increase gradually
2. **Monitor Memory**: Use `kubectl top pods` during migrations
3. **Test with Small Migrations**: Test configuration changes with small migrations first
4. **Leave Headroom**: Don't configure workers to use 100% of available memory
5. **Consider File Sizes**: Larger average file sizes require fewer workers

## Current Status (v2.3.9)

✅ **Optimized for 8Gi Memory - No More OOM**
- 10 upload workers (reduced from 50 → 25 → 10)
- 10 discovery workers
- 5MB file buffer (reduced from 10MB)
- Fixed cleanup button (pods no longer reload deleted tasks)
- Balanced for throughput and stability
- Successfully deployed to all 3 pods

### Recent Changes (v2.3.9)
1. **Reduced workers from 25 to 10** - Even 25 workers caused OOM
2. **Reduced file buffer from 10MB to 5MB** - Lower memory footprint per worker
3. **Fixed cleanup functionality** - Pods no longer reload failed/completed tasks from DB on startup

## Related Files

- `pkg/providers/googledrive/migrator.go` - Worker pool configuration
- `pkg/providers/googledrive/client.go` - HTTP connection pool
- `k8s/deployment.yaml` - Kubernetes resource limits

