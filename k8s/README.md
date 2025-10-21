# Kubernetes Deployment Guide

This guide explains how to deploy the S3 Migration tool on Kubernetes using either raw manifests or Helm.

## Prerequisites

- Kubernetes cluster (v1.19+)
- `kubectl` configured to access your cluster
- (Optional) Helm 3.x for Helm-based deployment
- Docker image: `registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.0.0`

## Deployment Options

### Option 1: Deploy with kubectl (Raw Manifests)

#### 1. Create Namespace (Optional)
```bash
kubectl create namespace s3-migration
```

#### 2. Generate Encryption Key
```bash
# Generate a secure 32-byte encryption key
ENCRYPTION_KEY=$(openssl rand -base64 32)
echo $ENCRYPTION_KEY

# Create the secret
kubectl create secret generic s3-migration-secrets \
  --from-literal=encryption-key="$ENCRYPTION_KEY" \
  -n s3-migration
```

Or leave it empty to auto-generate:
```bash
kubectl apply -f k8s/secrets.yaml -n s3-migration
```

#### 3. Deploy the Application
```bash
# Apply all manifests
kubectl apply -f k8s/deployment.yaml -n s3-migration
kubectl apply -f k8s/ingress.yaml -n s3-migration
kubectl apply -f k8s/configmap.yaml -n s3-migration
```

#### 4. Configure Ingress
Edit `k8s/ingress.yaml` to set your domain:
```yaml
spec:
  rules:
  - host: s3-migration.your-domain.com  # Change this
```

Then apply:
```bash
kubectl apply -f k8s/ingress.yaml -n s3-migration
```

#### 5. Verify Deployment
```bash
# Check pods
kubectl get pods -n s3-migration

# Check service
kubectl get svc -n s3-migration

# Check ingress
kubectl get ingress -n s3-migration

# View logs
kubectl logs -f deployment/s3-migration -n s3-migration
```

---

### Option 2: Deploy with Helm (Recommended)

#### 1. Configure Values
Edit `helm/s3-migration/values.yaml`:
```yaml
image:
  tag: "v2.0.0"

ingress:
  enabled: true
  hosts:
    - host: s3-migration.your-domain.com  # Change this
      paths:
        - path: /
          pathType: Prefix

persistence:
  data:
    size: 10Gi
    storageClass: ""  # Set your storage class

resources:
  limits:
    cpu: 2000m
    memory: 4Gi
```

#### 2. Install with Helm
```bash
# Install from local chart
helm install s3-migration ./helm/s3-migration \
  --namespace s3-migration \
  --create-namespace

# Or with custom values
helm install s3-migration ./helm/s3-migration \
  --namespace s3-migration \
  --create-namespace \
  -f custom-values.yaml
```

#### 3. Upgrade Deployment
```bash
# Upgrade to new version
helm upgrade s3-migration ./helm/s3-migration \
  --namespace s3-migration

# Upgrade with new image tag
helm upgrade s3-migration ./helm/s3-migration \
  --namespace s3-migration \
  --set image.tag=v2.1.0
```

#### 4. Verify Installation
```bash
# Check release status
helm status s3-migration -n s3-migration

# List all resources
helm get manifest s3-migration -n s3-migration

# View values
helm get values s3-migration -n s3-migration
```

#### 5. Uninstall
```bash
helm uninstall s3-migration -n s3-migration
```

---

## Configuration Options

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT` | HTTP server port | `8000` |
| `GIN_MODE` | Gin mode (debug/release) | `release` |
| `ENCRYPTION_KEY` | Encryption key for credentials | Auto-generated |

### Persistent Volumes

The application uses three PVCs:

| PVC | Purpose | Default Size | Path |
|-----|---------|--------------|------|
| `s3-migration-data` | Application data | 10Gi | `/app/data` |
| `s3-migration-logs` | Log files | 5Gi | `/app/logs` |
| `s3-migration-state` | State files | 1Gi | `/app/data/state` |

### Resource Requirements

**Minimum:**
- CPU: 500m
- Memory: 512Mi

**Recommended:**
- CPU: 2000m
- Memory: 4Gi

**For Large Migrations:**
- CPU: 4000m+
- Memory: 8Gi+

---

## Ingress Configuration

### Nginx Ingress Controller

```yaml
annotations:
  nginx.ingress.kubernetes.io/proxy-body-size: "0"
  nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
  nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
```

### Traefik

```yaml
annotations:
  traefik.ingress.kubernetes.io/router.middlewares: default-redirect-https@kubernetescrd
```

### TLS/HTTPS with cert-manager

```yaml
annotations:
  cert-manager.io/cluster-issuer: "letsencrypt-prod"

tls:
  - hosts:
    - s3-migration.your-domain.com
    secretName: s3-migration-tls
```

---

## Accessing the Application

### Via Ingress (Production)
```
https://s3-migration.your-domain.com
```

### Via Port Forward (Development)
```bash
kubectl port-forward -n s3-migration svc/s3-migration 8000:80

# Access at http://localhost:8000
```

### Via LoadBalancer (Cloud)
Change service type in values.yaml:
```yaml
service:
  type: LoadBalancer
```

---

## Monitoring and Troubleshooting

### Check Pod Status
```bash
kubectl get pods -n s3-migration
kubectl describe pod <pod-name> -n s3-migration
```

### View Logs
```bash
# Real-time logs
kubectl logs -f deployment/s3-migration -n s3-migration

# Last 100 lines
kubectl logs --tail=100 deployment/s3-migration -n s3-migration

# Previous pod logs (after restart)
kubectl logs --previous deployment/s3-migration -n s3-migration
```

### Check Health
```bash
# Port forward
kubectl port-forward -n s3-migration svc/s3-migration 8000:80

# Check health endpoint
curl http://localhost:8000/api/health
```

### Check PVC Status
```bash
kubectl get pvc -n s3-migration
kubectl describe pvc s3-migration-data -n s3-migration
```

### Common Issues

#### Pod CrashLoopBackOff
```bash
# Check logs
kubectl logs <pod-name> -n s3-migration

# Check events
kubectl get events -n s3-migration --sort-by='.lastTimestamp'
```

#### PVC Pending
```bash
# Check storage class
kubectl get storageclass

# Check PVC events
kubectl describe pvc s3-migration-data -n s3-migration
```

#### Ingress Not Working
```bash
# Check ingress controller
kubectl get pods -n ingress-nginx

# Check ingress
kubectl describe ingress s3-migration -n s3-migration

# Check DNS resolution
nslookup s3-migration.your-domain.com
```

---

## Scaling

### Manual Scaling
```bash
# Scale to 3 replicas
kubectl scale deployment s3-migration --replicas=3 -n s3-migration
```

### Auto-scaling (HPA)
Enable in `values.yaml`:
```yaml
autoscaling:
  enabled: true
  minReplicas: 1
  maxReplicas: 5
  targetCPUUtilizationPercentage: 80
```

---

## Backup and Restore

### Backup PVCs
```bash
# Create snapshots using your storage provider's tools
# Or copy data manually
kubectl cp s3-migration/<pod-name>:/app/data ./backup-data -n s3-migration
```

### Restore PVCs
```bash
# Copy data back
kubectl cp ./backup-data s3-migration/<pod-name>:/app/data -n s3-migration
```

---

## Security Best Practices

1. **Use Secrets for Sensitive Data**
   ```bash
   kubectl create secret generic s3-migration-secrets \
     --from-literal=encryption-key="$(openssl rand -base64 32)" \
     -n s3-migration
   ```

2. **Enable Network Policies**
   ```yaml
   apiVersion: networking.k8s.io/v1
   kind: NetworkPolicy
   metadata:
     name: s3-migration-netpol
   spec:
     podSelector:
       matchLabels:
         app: s3-migration
     policyTypes:
     - Ingress
     ingress:
     - from:
       - namespaceSelector:
           matchLabels:
             name: ingress-nginx
   ```

3. **Use RBAC**
   - Grant minimal permissions
   - Use ServiceAccount with limited scope

4. **Enable Pod Security Policies**
   ```yaml
   podSecurityContext:
     runAsNonRoot: true
     runAsUser: 1000
     fsGroup: 2000
   ```

---

## Production Checklist

- [ ] Configure custom domain in Ingress
- [ ] Enable TLS/HTTPS
- [ ] Set appropriate resource limits
- [ ] Configure persistent storage
- [ ] Set up encryption key
- [ ] Enable health checks
- [ ] Configure monitoring/alerting
- [ ] Set up backup strategy
- [ ] Enable autoscaling (if needed)
- [ ] Review security settings
- [ ] Test disaster recovery
- [ ] Document runbooks

---

## Support

For issues or questions:
- Check logs: `kubectl logs -f deployment/s3-migration -n s3-migration`
- Review events: `kubectl get events -n s3-migration`
- Contact: admin@example.com

