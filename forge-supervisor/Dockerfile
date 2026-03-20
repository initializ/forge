# Build stage
FROM golang:1.21-alpine AS builder

# Install certificates for TLS
RUN apk add --no-cache ca-certificates

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies (using replace directive, so this uses local forge-core)
RUN go mod download

# Copy source code
COPY . .

# Build static binary with netgo (no cgo)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -installsuffix netgo \
    -tags netgo \
    -o /usr/local/bin/forge-supervisor .

# Final stage - scratch image
FROM scratch

# Copy certificates and binary
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/local/bin/forge-supervisor /usr/local/bin/forge-supervisor

# Create non-root user for the agent
RUN adduser -D -u 1000 agent

# Useforge-supervisor as PID 1
ENTRYPOINT ["/usr/local/bin/forge-supervisor"]
