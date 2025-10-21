# S3 Migration Tool v2.3.0

A production-ready, high-performance migration tool for S3-compatible storage and Google Drive with PostgreSQL-backed multi-pod architecture.

## ✨ Features

- 🚀 **Multi-pod horizontal scaling** (3+ replicas)
- 💾 **PostgreSQL RDS backend** for shared state
- 📦 **S3 to S3 migration** (AWS, MinIO, Wasabi, etc.)
- 🔄 **Google Drive to S3 migration**
- 🎯 **High availability** with automatic failover
- 💪 **Concurrent uploads** (50+ workers)
- 🔒 **Zero-byte file support**
- 🏗️ **Folder structure preservation**
- 📊 **Real-time progress tracking**
- 🌐 **Web UI** with live updates

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

## 🔧 Configuration

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DB_DRIVER` | No | `postgres` | Database driver |
| `DB_CONNECTION_STRING` | **Yes** | - | PostgreSQL connection |
| `PORT` | No | `8000` | API server port |
| `GIN_MODE` | No | `release` | Gin framework mode |

### Scaling

```bash
# Scale to 5 pods
kubectl scale deployment s3-migration --replicas=5 -n s3-migration

# Auto-scaling (HPA)
kubectl autoscale deployment s3-migration --min=3 --max=10 --cpu-percent=70 -n s3-migration
```

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

- **50 concurrent workers** for maximum throughput
- **Streaming uploads** to prevent OOM
- **0-byte file handling** with proper headers
- **Bandwidth monitoring** and throttling
- **Auto-retry** on transient failures

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

### v2.3.0 (Latest)
- ✅ PostgreSQL RDS backend
- ✅ Multi-pod horizontal scaling
- ✅ Removed PVC dependencies
- ✅ 0-byte file fix
- ✅ State persistence across restarts

### v2.2.50
- ✅ Fixed 0-byte file uploads
- ✅ Improved error handling

### v2.2.49
- ✅ Concurrent folder discovery
- ✅ Two-phase migration (discovery → upload)
- ✅ 50 concurrent workers

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
