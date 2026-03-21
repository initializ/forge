# Build stage
FROM golang:1.21-alpine AS builder

# Install certificates for TLS and useradd
RUN apk add --no-cache ca-certificates

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Create agent user (UID 1000) in builder — we'll copy its entry to scratch
RUN adduser -D -u 1000 -G 1000 agent

# Build static binary with netgo (no cgo)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -installsuffix netgo \
    -tags netgo \
    -o /usr/local/bin/forge-supervisor .

# Final stage — scratch image (no shell, minimal)
FROM scratch

# Copy certificates
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy supervisor binary
COPY --from=builder /usr/local/bin/forge-supervisor /usr/local/bin/forge-supervisor

# Copy passwd/group for UID 1000 (so 'id' and 'groups' work for the agent)
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

# Create /etc/forge/ directory for policy file (mounted at runtime)
COPY --from=builder /etc/group /etc/group
RUN mkdir -p /etc/forge && touch /etc/forge/egress_allowlist.json

# forge-supervisor runs as PID 1 (UID 0), agent child runs as UID 1000 via exec.go
ENTRYPOINT ["/usr/local/bin/forge-supervisor"]
