# ============================================
# Multi-stage Dockerfile for S3 Migration Tool
# ============================================
# Stage 1: Build the Go application
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /build

# Copy go mod files first (for better caching)
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -o s3migration \
    ./cmd/server

# ============================================
# Stage 2: Create minimal runtime image
FROM alpine:latest

# Install ca-certificates for HTTPS
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 s3migration && \
    adduser -D -u 1000 -G s3migration s3migration

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/s3migration .

# Copy web files (static assets and UI)
COPY web/ /app/web/

# Create directories for state and config
RUN mkdir -p /app/data/state /app/config && \
    chown -R s3migration:s3migration /app

# Copy configuration example (optional)
# COPY config.example.yaml /app/config/

# Switch to non-root user
USER s3migration

# Expose API port
EXPOSE 8000

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8000/health || exit 1

# Set environment variables
# Note: Set ENCRYPTION_KEY in docker-compose.yml or at runtime
ENV PORT=8000 \
    GIN_MODE=release \
    ENCRYPTION_KEY=""

# Run the application
CMD ["./s3migration"]

