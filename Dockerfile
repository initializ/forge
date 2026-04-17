# ── Build stage ────────────────────────────────────────────────
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none

WORKDIR /src

# Copy go.work and all module go.mod/go.sum files first for layer caching
COPY go.work ./
COPY forge-core/go.mod forge-core/go.sum ./forge-core/
COPY forge-cli/go.mod forge-cli/go.sum ./forge-cli/
COPY forge-plugins/go.mod forge-plugins/go.sum ./forge-plugins/
COPY forge-skills/go.mod forge-skills/go.sum ./forge-skills/
COPY forge-ui/go.mod forge-ui/go.sum ./forge-ui/

# Download dependencies (cached unless go.mod/go.sum change)
RUN --mount=type=cache,target=/go/pkg/mod \
    go work sync && \
    cd forge-core && go mod download && \
    cd ../forge-cli && go mod download && \
    cd ../forge-plugins && go mod download && \
    cd ../forge-skills && go mod download && \
    cd ../forge-ui && go mod download

# Copy full source
COPY forge-core/ ./forge-core/
COPY forge-cli/ ./forge-cli/
COPY forge-plugins/ ./forge-plugins/
COPY forge-skills/ ./forge-skills/
COPY forge-ui/ ./forge-ui/

# Build static binary for target platform
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/forge ./forge-cli/cmd/forge

# ── Runtime stage ─────────────────────────────────────────────
FROM alpine:3.22.4

RUN apk add --no-cache ca-certificates git tzdata && \
    adduser -D -h /home/forge forge

COPY --from=builder /out/forge /usr/local/bin/forge

USER forge
WORKDIR /home/forge

ENTRYPOINT ["forge"]
