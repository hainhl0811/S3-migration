# S3 Migration Tool v2.6.6

A production-ready, high-performance migration tool for S3-compatible storage and Google Drive with PostgreSQL-backed multi-pod architecture and adaptive memory management.

## ✨ Features

- 🚀 **Multi-pod horizontal scaling** (3+ replicas)
- 💾 **PostgreSQL RDS backend** for shared state
- 📦 **S3 to S3 migration** (AWS, MinIO, Wasabi, etc.)
- 🔄 **Google Drive to S3 migration**
- 🎯 **High availability** with automatic failover
- 🧠 **Adaptive memory management** with OOM prevention
- 💪 **Smart worker tuning** (1-100 workers based on memory)
- 🔒 **Zero-byte file support**
- 🏗️ **Folder structure preservation**
- 📊 **Real-time progress tracking** with timestamps
- 🌐 **Modern Web UI** with minimalist design
- 📅 **Advanced filtering** (date range, status)
- 🔄 **Two-phase migration** (discovery → upload)
- 🎨 **Clean, professional interface**

## 🏗️ Architecture

```
       Load Balancer
            │
    ┌───────┼───────┐
    ▼       ▼       ▼
  Pod 1   Pod 2   Pod 3
    │       │       │
    └───────┼───────┘
            ▼
    PostgreSQL RDS
```

## 📋 Prerequisites

- Kubernetes cluster
- PostgreSQL database (RDS, Cloud SQL, or self-hosted)
- Docker registry access
- kubectl configured

## 🚀 Quick Start

### 1. Clone and Setup

```bash
git clone https://github.com/yourusername/s3-migration.git
cd s3-migration

# Copy environment templates
cp env.template .env
cp k8s/secrets.template.yaml k8s/secrets.yaml
```

### 2. Configure Database

Edit `k8s/secrets.yaml`:
```yaml
stringData:
  db-connection-string: "postgres://user:pass@host:5432/dbname?sslmode=disable"
```

Create database:
```sql
CREATE DATABASE s3migration;
GRANT ALL PRIVILEGES ON DATABASE s3migration TO your_user;
```

### 3. Deploy

```bash
# Create namespace
kubectl create namespace s3-migration

# Create secrets
kubectl apply -f k8s/secrets.yaml

# Deploy application
kubectl apply -f k8s/deployment.yaml

# Check status
kubectl get pods -n s3-migration
```

## 📚 Documentation

- [RDS Deployment Guide](RDS_DEPLOYMENT.md) - Complete PostgreSQL setup
- [Security Guidelines](SECURITY.md) - Secrets management
- [API Documentation](#api-endpoints) - REST API reference

## 🧠 Memory Management

### Adaptive Worker Scaling
- **Memory-aware tuning** - Workers adjust based on available memory
- **OOM prevention** - Automatic worker reduction when memory is low
- **Smart scaling** - 1-100 workers based on system resources
- **Real-time monitoring** - Continuous memory usage tracking

### Performance Optimization
- **Streaming transfers** - No file buffering to prevent OOM
- **Connection pooling** - Optimized HTTP client settings
- **Garbage collection** - Aggressive GC when memory is high
- **Memory limits** - Kubernetes and Go runtime limits

### Configuration
```yaml
# Kubernetes memory limits
resources:
  requests:
    memory: "512Mi"
  limits:
    memory: "2Gi"

# Go runtime limits
env:
- name: GOMEMLIMIT
  value: "1800MiB"  # 90% of 2Gi limit
- name: GOGC
  value: "50"       # Aggressive garbage collection
```

## 🔧 Configuration

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DB_DRIVER` | No | `postgres` | Database driver |
| `DB_CONNECTION_STRING` | **Yes** | - | PostgreSQL connection |
| `PORT` | No | `8000` | API server port |
| `GIN_MODE` | No | `release` | Gin framework mode |
| `GOMEMLIMIT` | No | `1800MiB` | Go memory limit |
| `GOGC` | No | `50` | Garbage collection percentage |

### Scaling

```bash
# Scale to 5 pods
kubectl scale deployment s3-migration --replicas=5 -n s3-migration

# Auto-scaling (HPA)
kubectl autoscale deployment s3-migration --min=3 --max=10 --cpu-percent=70 -n s3-migration
```

## 🌐 Web UI Features

### Modern Interface
- **Minimalist design** - Clean, professional appearance
- **Real-time updates** - Live progress tracking
- **Advanced filtering** - Date range and status filters
- **Task management** - View, monitor, and clean up tasks
- **Responsive design** - Works on desktop and mobile

### Task Management
- **Comprehensive timestamps** - Start time, end time, running duration
- **Progress tracking** - Real-time speed, ETA, and completion percentage
- **Status indicators** - Running, completed, failed states
- **Error reporting** - Detailed error messages and hints
- **Bulk operations** - Clean up failed/completed tasks

### Filtering & Search
- **Date range filtering** - Filter tasks by start date
- **Status filtering** - Show only running/completed/failed tasks
- **Combined filters** - Date + status filtering
- **Real-time updates** - Instant filter application
- **Clear filters** - One-click reset

## 🌐 API Endpoints

### Health Check
```bash
GET /api/health
```

### Start S3 Migration
```bash
POST /api/migrate
Content-Type: application/json

{
  "source_bucket": "source-bucket",
  "dest_bucket": "dest-bucket",
  "source_credentials": {
    "access_key": "...",
    "secret_key": "...",
    "region": "us-east-1"
  },
  "dest_credentials": {
    "access_key": "...",
    "secret_key": "...",
    "region": "us-west-2"
  }
}
```

### Start Google Drive Migration
```bash
POST /api/googledrive/migrate
Content-Type: application/json

{
  "source_folder_id": "folder-id-or-empty-for-root",
  "dest_bucket": "my-bucket",
  "source_credentials": {
    "access_token": "...",
    "refresh_token": "..."
  }
}
```

### Check Status
```bash
GET /api/status/{taskID}
```

### List Tasks
```bash
GET /api/tasks
```

## 🔒 Security

**NEVER commit secrets to git!**

See [SECURITY.md](SECURITY.md) for detailed security guidelines.

## 🏆 Performance

- **Adaptive worker scaling** (1-100 workers based on available memory)
- **Memory-aware tuning** prevents OOM crashes
- **Streaming uploads** for large files (no buffering)
- **0-byte file handling** with proper headers
- **Bandwidth monitoring** and throttling
- **Auto-retry** on transient failures
- **Cross-account S3 streaming** for maximum efficiency
- **Google Drive optimization** with connection pooling

## 📊 Monitoring

```bash
# View logs
kubectl logs -n s3-migration -l app=s3-migration --tail=100

# Watch pods
kubectl get pods -n s3-migration -w

# Check database
psql -h your-db-host -U s3migrator -d s3migration -c "SELECT * FROM migration_tasks;"
```

## 🐛 Troubleshooting

### Pods CrashLoopBackOff

Check database connectivity:
```bash
kubectl logs -n s3-migration <pod-name>
```

### Connection Refused

Verify PostgreSQL accepts remote connections:
- Check `postgresql.conf`: `listen_addresses = '*'`
- Check `pg_hba.conf`: Add entry for pod network
- Check firewall rules

### 411 Length Required

Fixed in v2.3.0 - 0-byte files handled specially.

## 📝 Changelog

### v2.6.6 (Latest) - Minimalist Design
- ✅ **Minimalist filter UI** - Clean, professional design
- ✅ **Advanced task filtering** - Date range and status filters
- ✅ **Real-time timestamps** - Start time, end time, running duration
- ✅ **Fixed duplicate refresh** - Single refresh interval (50% performance improvement)
- ✅ **Memory management** - Adaptive worker scaling prevents OOM
- ✅ **S3-to-S3 optimization** - Streaming transfers, reduced workers
- ✅ **Clean work directory** - Removed 15+ temporary files

### v2.6.5 - Consistent Design
- ✅ **Consistent UI styling** - Filter matches application design
- ✅ **Professional appearance** - Clean, polished interface
- ✅ **Maintainable code** - CSS classes instead of inline styles

### v2.6.4 - Date Filtering
- ✅ **Date range filtering** - Filter tasks by start date
- ✅ **Status filtering** - Filter by running/completed/failed
- ✅ **Combined filters** - Date + status filtering
- ✅ **Real-time updates** - Instant filter application

### v2.6.3 - Timestamps & Refresh
- ✅ **Task timestamps** - Start time, end time, running duration
- ✅ **Reduced refresh frequency** - 15 seconds instead of 5 seconds
- ✅ **Better UX** - Smoother, less aggressive updates

### v2.6.2 - UI Fixes
- ✅ **Input box consistency** - All credential fields same size
- ✅ **Professional styling** - Clean, uniform appearance

### v2.6.1 - Clean Code
- ✅ **Removed old tuner logic** - Simplified worker management
- ✅ **Memory-first approach** - Adaptive limits based on available memory
- ✅ **S3-to-S3 fixes** - Streaming transfers, reduced workers

### v2.6.0 - Adaptive Memory
- ✅ **Memory-aware worker scaling** - Prevents OOM crashes
- ✅ **Adaptive tuning** - Workers adjust based on memory usage
- ✅ **OOM prevention** - Smart memory management

### v2.3.0 - RDS Backend
- ✅ **PostgreSQL RDS backend** - Shared state across pods
- ✅ **Multi-pod horizontal scaling** - 3+ replicas
- ✅ **Removed PVC dependencies** - No more persistent volumes
- ✅ **0-byte file fix** - Proper handling of empty files
- ✅ **State persistence** - Survives pod restarts

## 🤝 Contributing

1. Fork the repository
2. Create feature branch
3. Make changes (don't commit secrets!)
4. Submit pull request

## 📄 License

[Your License Here]

## 🙋 Support

For issues and questions:
- GitHub Issues: [Create an issue](https://github.com/yourusername/s3-migration/issues)
- Email: support@yourcompany.com

---

**Built with ❤️ for high-performance cloud migrations**
