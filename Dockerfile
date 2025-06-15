# Multi-stage Dockerfile for Harbor Replicator
# Build stage
FROM golang:1.21-alpine AS builder

# Install dependencies for building
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o harbor-replicator ./cmd/harbor-replicator

# Final stage
FROM alpine:3.19

# Install ca-certificates and timezone data
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 harbor && \
    adduser -D -u 1000 -G harbor harbor

# Create necessary directories
WORKDIR /app
RUN mkdir -p /app/configs /app/state /app/logs && \
    chown -R harbor:harbor /app

# Copy binary from builder stage
COPY --from=builder /app/harbor-replicator .

# Copy default configuration
COPY --chown=harbor:harbor configs/config.yaml /app/configs/config.yaml

# Expose ports
EXPOSE 8080 9090

# Switch to non-root user
USER harbor:harbor

# Set default entrypoint and command
ENTRYPOINT ["./harbor-replicator"]
CMD ["--config", "/app/configs/config.yaml"]