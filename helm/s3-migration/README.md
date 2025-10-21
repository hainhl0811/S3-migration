# S3 Migration Tool - Helm Chart

Helm chart for deploying S3 Migration Tool on Kubernetes.

## TL;DR

```bash
helm install s3-migration ./s3-migration \
  --namespace s3-migration \
  --create-namespace
```

## Introduction

This chart deploys S3 Migration Tool on a Kubernetes cluster using the Helm package manager.

## Prerequisites

- Kubernetes 1.19+
- Helm 3.x
- PV provisioner support in the underlying infrastructure

## Installing the Chart

```bash
# Basic installation
helm install s3-migration ./s3-migration

# With custom namespace
helm install s3-migration ./s3-migration \
  --namespace s3-migration \
  --create-namespace

# With custom values
helm install s3-migration ./s3-migration \
  -f custom-values.yaml
```

## Uninstalling the Chart

```bash
helm uninstall s3-migration -n s3-migration
```

## Configuration

See [values.yaml](values.yaml) for all configuration options.

### Common Configurations

#### Custom Domain
```yaml
ingress:
  enabled: true
  hosts:
    - host: s3-migration.your-domain.com
      paths:
        - path: /
          pathType: Prefix
```

#### Resource Limits
```yaml
resources:
  limits:
    cpu: 4000m
    memory: 8Gi
  requests:
    cpu: 1000m
    memory: 2Gi
```

#### Storage
```yaml
persistence:
  data:
    enabled: true
    storageClass: "fast-ssd"
    size: 50Gi
```

#### Auto-scaling
```yaml
autoscaling:
  enabled: true
  minReplicas: 1
  maxReplicas: 5
  targetCPUUtilizationPercentage: 70
```

## Examples

See [k8s/examples/](../../k8s/examples/) for complete examples:
- `development.yaml` - Dev environment
- `production.yaml` - Production with HA
- `custom-values.yaml` - Customization template

## Support

For issues and questions, see the main [KUBERNETES.md](../../KUBERNETES.md) documentation.

