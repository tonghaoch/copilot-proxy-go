# Stage 1: Build
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /copilot-proxy-go .

# Stage 2: Runtime
FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget

COPY --from=builder /copilot-proxy-go /usr/local/bin/copilot-proxy-go
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Create data directory
RUN mkdir -p /root/.local/share/copilot-api

EXPOSE 4141

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --spider -q http://localhost:4141/ || exit 1

ENTRYPOINT ["/entrypoint.sh"]
