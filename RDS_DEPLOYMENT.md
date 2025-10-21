# S3 Migration Tool - RDS Deployment Guide

## 🎯 **Version 2.3.0 - Multi-Pod Scaling with RDS Backend**

This version adds **RDS/PostgreSQL support** for state persistence, enabling **true horizontal scaling** with **3+ pod replicas**.

---

## 📋 **Prerequisites**

### 1. **Create RDS PostgreSQL Instance**

**AWS RDS:**
```bash
aws rds create-db-instance \
  --db-instance-identifier s3-migration-db \
  --db-instance-class db.t3.micro \
  --engine postgres \
  --master-username admin \
  --master-user-password YOUR_SECURE_PASSWORD \
  --allocated-storage 20 \
  --vpc-security-group-ids sg-xxxxx \
  --db-subnet-group-name your-subnet-group \
  --publicly-accessible false
```

**Or use any PostgreSQL/MySQL database:**
- AWS RDS PostgreSQL
- AWS RDS MySQL
- Azure Database for PostgreSQL
- Google Cloud SQL
- Self-hosted PostgreSQL/MySQL

### 2. **Get Database Connection String**

**PostgreSQL:**
```
postgres://username:password@host:5432/dbname?sslmode=require
```

**MySQL:**
```
username:password@tcp(host:3306)/dbname?parseTime=true
```

---

## 🚀 **Deployment Steps**

### **Step 1: Create Kubernetes Secret**

```bash
# Create the secret with your RDS connection string
kubectl create secret generic s3-migration-secrets \
  --from-literal=db-connection-string='postgres://admin:PASSWORD@your-rds-endpoint.rds.amazonaws.com:5432/s3migration?sslmode=require' \
  --namespace=s3-migration
```

### **Step 2: Build and Push Docker Image**

```bash
# Build v2.3.0 with RDS support
docker build -t registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.3.0 .

# Push to registry
docker push registry.gitlab.com/hainhl0811/cmc-example-deploy/s3-migration:v2.3.0
```

### **Step 3: Deploy to Kubernetes**

```bash
# Apply updated deployment (3 replicas with RDS)
kubectl apply -f k8s/deployment.yaml

# Watch deployment rollout
kubectl rollout status deployment/s3-migration -n s3-migration

# Verify all 3 pods are running
kubectl get pods -n s3-migration
```

Expected output:
```
NAME                            READY   STATUS    RESTARTS   AGE
s3-migration-xxxxxxxxx-xxxxx    1/1     Running   0          30s
s3-migration-xxxxxxxxx-xxxxx    1/1     Running   0          30s
s3-migration-xxxxxxxxx-xxxxx    1/1     Running   0          30s
```

---

## 🏗️ **Architecture**

### **Before (v2.2.x) - Single Pod**
```
┌─────────────────┐
│   Pod 1         │
│  (In-Memory)    │  ❌ Single point of failure
│                 │  ❌ Lost state on restart
└─────────────────┘
```

### **After (v2.3.0) - Multi-Pod with RDS**
```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│   Pod 1         │     │   Pod 2         │     │   Pod 3         │
│                 │     │                 │     │                 │
└────────┬────────┘     └────────┬────────┘     └────────┬────────┘
         │                       │                       │
         └───────────────────────┼───────────────────────┘
                                 │
                                 ▼
                    ┌────────────────────────┐
                    │   RDS PostgreSQL       │
                    │   (Shared State)       │
                    │                        │
                    │  ✅ High Availability  │
                    │  ✅ Shared State       │
                    │  ✅ Auto Backup        │
                    │  ✅ Horizontal Scale   │
                    └────────────────────────┘
```

---

## 🎯 **Key Benefits**

| Feature | Before (v2.2.x) | After (v2.3.0) |
|---------|-----------------|----------------|
| **Scalability** | ❌ 1 pod only | ✅ 3+ pods |
| **High Availability** | ❌ Single point of failure | ✅ Multiple replicas |
| **State Persistence** | ❌ Lost on pod restart | ✅ Persisted in RDS |
| **Load Balancing** | ❌ N/A | ✅ Automatic across pods |
| **Pod Restart** | ❌ All migrations lost | ✅ Automatic recovery |
| **Concurrent Migrations** | ⚠️ Limited to 1 pod | ✅ Distributed across 3 pods |

---

## 🔧 **Configuration**

### **Environment Variables**

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DB_DRIVER` | No | `postgres` | Database driver (`postgres` or `mysql`) |
| `DB_CONNECTION_STRING` | **Yes** | - | Database connection string |
| `PORT` | No | `8000` | API server port |
| `GIN_MODE` | No | `release` | Gin framework mode |

### **Database Schema**

The application **automatically creates** the following table on startup:

```sql
CREATE TABLE migration_tasks (
    id VARCHAR(255) PRIMARY KEY,
    status VARCHAR(50) NOT NULL,
    progress FLOAT NOT NULL DEFAULT 0,
    copied_objects BIGINT NOT NULL DEFAULT 0,
    total_objects BIGINT NOT NULL DEFAULT 0,
    copied_size BIGINT NOT NULL DEFAULT 0,
    total_size BIGINT NOT NULL DEFAULT 0,
    current_speed FLOAT NOT NULL DEFAULT 0,
    eta VARCHAR(255),
    duration VARCHAR(255),
    errors TEXT,
    start_time TIMESTAMP NOT NULL,
    end_time TIMESTAMP,
    migration_type VARCHAR(50),
    dry_run BOOLEAN DEFAULT FALSE,
    sync_mode BOOLEAN DEFAULT FALSE,
    original_request TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

---

## 📊 **Monitoring**

### **Check Pod Status**
```bash
kubectl get pods -n s3-migration -l app=s3-migration
```

### **View Pod Logs**
```bash
# View logs from all pods
kubectl logs -n s3-migration -l app=s3-migration --tail=100

# View logs from specific pod
kubectl logs -n s3-migration s3-migration-xxxxxxxxx-xxxxx
```

### **Database Connection Health**
```bash
# Check logs for database initialization
kubectl logs -n s3-migration -l app=s3-migration | grep "Database state manager"
```

Expected output:
```
✅ Database state manager initialized successfully
✅ Task manager initialized with postgres database backend
```

---

## 🔒 **Security Best Practices**

1. **Use SSL/TLS** for database connections:
   ```
   ?sslmode=require
   ```

2. **Store credentials in Kubernetes Secrets**:
   ```bash
   kubectl create secret generic s3-migration-secrets \
     --from-literal=db-connection-string='postgres://...' \
     --namespace=s3-migration
   ```

3. **Use RDS IAM Authentication** (AWS only):
   ```go
   // Connection string with IAM auth
   postgres://username@host:5432/dbname?sslmode=require&aws_iam_auth=true
   ```

4. **Enable RDS Encryption** at rest and in transit

---

## 🐛 **Troubleshooting**

### **Pods stuck in "Pending" state**
```bash
# Check pod events
kubectl describe pod -n s3-migration s3-migration-xxxxxxxxx-xxxxx
```

**Solution**: Verify RDS security group allows connections from EKS cluster

### **"failed to initialize database state manager" error**
- Check DB_CONNECTION_STRING format
- Verify database is accessible from pods
- Check security group / firewall rules
- Test connection manually:
  ```bash
  kubectl run -it --rm debug --image=postgres:15 --restart=Never -- \
    psql "postgres://admin:password@your-rds.amazonaws.com:5432/s3migration"
  ```

### **Migrations not visible after pod restart**
- Check database connectivity
- Verify periodic state save is working (logs every 5 seconds)
- Query database directly:
  ```sql
  SELECT id, status, progress FROM migration_tasks;
  ```

---

## 📈 **Scaling**

### **Scale to 5 replicas**
```bash
kubectl scale deployment s3-migration --replicas=5 -n s3-migration
```

### **Auto-scaling** (HPA)
```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: s3-migration-hpa
  namespace: s3-migration
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: s3-migration
  minReplicas: 3
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
```

---

## ✅ **Success Criteria**

After deployment, verify:

1. **3 pods running**:
   ```bash
   kubectl get pods -n s3-migration
   ```

2. **Database connected**:
   ```bash
   kubectl logs -n s3-migration -l app=s3-migration | grep "Task manager initialized"
   ```

3. **Load balancer working**:
   ```bash
   # Multiple requests hit different pods
   for i in {1..10}; do
     curl http://your-service/api/health
   done
   ```

4. **State persistence**:
   - Start a migration
   - Delete a pod: `kubectl delete pod -n s3-migration s3-migration-xxxxxxxxx-xxxxx`
   - Migration still visible in UI ✅

---

## 🎉 **You're Ready!**

Your S3 Migration Tool is now running with:
- ✅ **3 pod replicas** for high availability
- ✅ **RDS PostgreSQL** for shared state
- ✅ **Auto-recovery** from pod failures
- ✅ **Horizontal scaling** support
- ✅ **Production-ready** architecture

**Need help?** Check the logs or database for debugging!

