# Security Guidelines

## 🔒 **Sensitive Data Management**

This project requires sensitive credentials. **NEVER commit secrets to git!**

## 📋 **Setup Instructions**

### **1. Kubernetes Secrets**

```bash
# Copy the template
cp k8s/secrets.template.yaml k8s/secrets.yaml

# Edit with your actual credentials
nano k8s/secrets.yaml

# Apply to cluster
kubectl apply -f k8s/secrets.yaml

# Verify (values will be base64 encoded)
kubectl get secret s3-migration-secrets -n s3-migration -o yaml
```

### **2. Environment Variables (Local Development)**

```bash
# Copy the template
cp env.template .env

# Edit with your actual credentials
nano .env

# Run locally
source .env
go run cmd/server/main.go
```

## 🔑 **Required Secrets**

### **Database Connection String**

Format:
```
postgres://username:password@host:5432/database?sslmode=disable
```

Example:
```
postgres://s3migrator:SecurePassword123@172.16.1.107:5432/s3migration?sslmode=disable
```

### **Encryption Key (Optional)**

Generate a secure key:
```bash
openssl rand -base64 32
```

## ⚠️ **Security Checklist**

Before pushing to GitHub:

- [ ] `.gitignore` is in place
- [ ] No `.env` files committed
- [ ] No `k8s/secrets.yaml` committed
- [ ] No hardcoded passwords in code
- [ ] Templates created (`.template` files)
- [ ] README updated with setup instructions

## 🚨 **If You Accidentally Commit Secrets**

1. **Immediately rotate all credentials**
2. **Remove from git history:**
   ```bash
   git filter-branch --force --index-filter \
     "git rm --cached --ignore-unmatch k8s/secrets.yaml" \
     --prune-empty --tag-name-filter cat -- --all
   ```
3. **Force push (⚠️ use with caution):**
   ```bash
   git push origin --force --all
   ```
4. **Update credentials everywhere**

## 🔐 **Best Practices**

1. **Use Kubernetes Secrets** for production
2. **Use environment variables** for local development
3. **Rotate credentials** regularly
4. **Use separate credentials** for dev/staging/production
5. **Enable SSL/TLS** for database connections
6. **Use strong passwords** (20+ characters, mixed case, numbers, symbols)
7. **Limit database user permissions** (principle of least privilege)

## 📞 **Reporting Security Issues**

If you discover a security vulnerability, please email security@yourcompany.com instead of creating a public issue.

