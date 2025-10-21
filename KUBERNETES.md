# 🚀 Kubernetes Deployment Guide

Complete guide for deploying S3 Migration Tool on Kubernetes.

## 📋 Table of Contents

1. [Quick Start](#quick-start)
2. [Prerequisites](#prerequisites)
3. [Deployment Options](#deployment-options)
4. [Configuration](#configuration)
5. [Monitoring](#monitoring)
6. [Troubleshooting](#troubleshooting)

---

## ⚡ Quick Start

### Option 1: Deploy with kubectl (5 minutes)

```bash
# 1. Generate encryption key (optional)
kubectl create secret generic s3-migration-secrets \
  --from-literal=encryption-key="$(openssl rand -base64 32)" \
  -n default

# 2. Deploy everything
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/ingress.yaml
kubectl apply -f k8s/configmap.yaml

# 3. Check status
kubectl get pods -l app=s3-migration
kubectl logs -f deployment/s3-migration
```

### Option 2: Deploy with Helm (Recommended)

```bash
# 1. Install
helm install s3-migration ./helm/s3-migration \
  --namespace s3-migration \
  --create-namespace

# 2. Access
kubectl port-forward -n s3-migration svc/s3-migration 8000:80
```

---

## 📦 Prerequisites

- **Kubernetes**: v1.19+ cluster
- **kubectl**: Configured to access your cluster
- **Helm**: v3.x (for Helm deployment)
- **Docker Image**: Available in GitLab Container Registry

```bash
# Verify cluster access
kubectl cluster-info
kubectl version

# Verify Helm (if using)
helm version
```

---

## 🎯 Deployment Options

### 1. Raw Kubernetes Manifests

**Location**: `k8s/`

**Files**:
- `deployment.yaml` - Main deployment, service, and PVCs
- `ingress.yaml` - External access configuration
- `secrets.yaml` - Encryption key storage
- `configmap.yaml` - Application configuration

**Deploy**:
```bash
cd k8s
./deploy.sh [namespace]
```

### 2. Helm Chart

**Location**: `helm/s3-migration/`

**Benefits**:
- Templated configuration
- Easy upgrades
- Value overrides
- Release management

**Deploy**:
```bash
cd k8s
./helm-deploy.sh [release-name] [namespace] [values-file]
```

### 3. Production Examples

```bash
# Development
helm install s3-migration ./helm/s3-migration \
  -f k8s/examples/development.yaml \
  -n dev

# Production
helm install s3-migration ./helm/s3-migration \
  -f k8s/examples/production.yaml \
  -n production
```

---

## ⚙️ Configuration

### Storage Classes

Edit PVC storage classes for your cluster:

```yaml
# k8s/deployment.yaml
spec:
  storageClassName: fast-ssd  # Change this
```

Or in Helm values:

```yaml
# helm/s3-migration/values.yaml
persistence:
  data:
    storageClass: "fast-ssd"
    size: 50Gi
```

### Resource Allocation

**Small migrations** (< 1TB):
```yaml
resources:
  limits:
    cpu: 2000m
    memory: 4Gi
  requests:
    cpu: 500m
    memory: 512Mi
```

**Large migrations** (> 10TB):
```yaml
resources:
  limits:
    cpu: 8000m
    memory: 16Gi
  requests:
    cpu: 2000m
    memory: 4Gi
```

### Ingress Configuration

**1. Update hostname**:
```yaml
# k8s/ingress.yaml
spec:
  rules:
  - host: s3-migration.your-domain.com  # Change this
```

**2. Enable TLS** (with cert-manager):
```yaml
annotations:
  cert-manager.io/cluster-issuer: "letsencrypt-prod"

tls:
  - hosts:
    - s3-migration.your-domain.com
    secretName: s3-migration-tls
```

**3. Apply**:
```bash
kubectl apply -f k8s/ingress.yaml
```

### Encryption Key

**Auto-generate** (recommended):
```yaml
# Leave empty in secrets.yaml
data:
  encryption-key: ""
```

**Manual**:
```bash
# Generate
KEY=$(openssl rand -base64 32)

# Create secret
kubectl create secret generic s3-migration-secrets \
  --from-literal=encryption-key="$KEY" \
  -n s3-migration
```

---

## 📊 Monitoring

### Health Checks

```bash
# Port forward
kubectl port-forward -n s3-migration svc/s3-migration 8000:80

# Check health
curl http://localhost:8000/api/health
```

### Logs

```bash
# Real-time logs
kubectl logs -f deployment/s3-migration -n s3-migration

# Last 100 lines
kubectl logs --tail=100 deployment/s3-migration -n s3-migration

# Previous pod (after crash)
kubectl logs --previous deployment/s3-migration -n s3-migration
```

### Resource Usage

```bash
# CPU/Memory
kubectl top pods -n s3-migration

# Describe pod
kubectl describe pod <pod-name> -n s3-migration

# Events
kubectl get events -n s3-migration --sort-by='.lastTimestamp'
```

---

## 🔍 Troubleshooting

### Pod Not Starting

```bash
# Check pod status
kubectl get pods -n s3-migration
kubectl describe pod <pod-name> -n s3-migration

# Common issues:
# - ImagePullBackOff: Check image name/credentials
# - CrashLoopBackOff: Check logs
# - Pending: Check PVC status
```

### PVC Issues

```bash
# Check PVC
kubectl get pvc -n s3-migration
kubectl describe pvc s3-migration-data -n s3-migration

# Common issues:
# - Pending: No storage class or insufficient space
# - Lost: Underlying volume deleted
```

### Ingress Not Working

```bash
# Check ingress
kubectl get ingress -n s3-migration
kubectl describe ingress s3-migration -n s3-migration

# Check ingress controller
kubectl get pods -n ingress-nginx

# Test DNS
nslookup s3-migration.your-domain.com

# Test connection
curl -v http://s3-migration.your-domain.com
```

### High Memory Usage

```bash
# Check current usage
kubectl top pods -n s3-migration

# Increase limits
kubectl set resources deployment s3-migration \
  --limits=memory=8Gi \
  -n s3-migration
```

### Migration Failures

```bash
# Check logs
kubectl logs -f deployment/s3-migration -n s3-migration

# Access pod shell
kubectl exec -it deployment/s3-migration -n s3-migration -- /bin/sh

# Check disk space
kubectl exec deployment/s3-migration -n s3-migration -- df -h
```

---

## 🔄 Operations

### Scaling

```bash
# Manual scale
kubectl scale deployment s3-migration --replicas=3 -n s3-migration

# Auto-scaling (Helm)
helm upgrade s3-migration ./helm/s3-migration \
  -n s3-migration \
  --set autoscaling.enabled=true \
  --set autoscaling.maxReplicas=5
```

### Updating

```bash
# Update image (kubectl)
kubectl set image deployment/s3-migration \
  s3-migration=registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.1.0 \
  -n s3-migration

# Update with Helm
helm upgrade s3-migration ./helm/s3-migration \
  -n s3-migration \
  --set image.tag=v2.1.0
```

### Backup

```bash
# Export all manifests
kubectl get all -n s3-migration -o yaml > backup.yaml

# Backup PVC data
kubectl cp s3-migration/<pod-name>:/app/data ./backup-data -n s3-migration
```

### Uninstall

```bash
# kubectl
cd k8s
./uninstall.sh s3-migration

# Helm
helm uninstall s3-migration -n s3-migration
kubectl delete namespace s3-migration
```

---

## 🔐 Security

### Network Policies

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
    ports:
    - protocol: TCP
      port: 8000
```

### RBAC

```bash
# Create service account with limited permissions
kubectl create serviceaccount s3-migration -n s3-migration

# Create role
kubectl create role s3-migration \
  --verb=get,list,watch \
  --resource=pods,services \
  -n s3-migration

# Bind role
kubectl create rolebinding s3-migration \
  --role=s3-migration \
  --serviceaccount=s3-migration:s3-migration \
  -n s3-migration
```

### Pod Security

```yaml
# helm/s3-migration/values.yaml
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 1000
  fsGroup: 2000

securityContext:
  allowPrivilegeEscalation: false
  capabilities:
    drop:
      - ALL
```

---

## 📖 Additional Resources

- **Kubernetes Documentation**: https://kubernetes.io/docs/
- **Helm Documentation**: https://helm.sh/docs/
- **Ingress Controllers**: https://kubernetes.io/docs/concepts/services-networking/ingress-controllers/
- **cert-manager**: https://cert-manager.io/

---

## 🆘 Support

**Logs**: `kubectl logs -f deployment/s3-migration -n s3-migration`  
**Events**: `kubectl get events -n s3-migration`  
**Describe**: `kubectl describe pod <pod-name> -n s3-migration`

For more help, check the logs or contact support.

