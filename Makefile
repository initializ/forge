BINARY    := forge
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
COVERFILE := coverage.out
MODULES   := forge-core forge-cli forge-plugins

.PHONY: build test test-integration vet fmt lint cover cover-html install clean release help

## build: Compile the forge binary
build:
	cd forge-cli && go build -ldflags "$(LDFLAGS)" -o ../$(BINARY) ./cmd/forge

## test: Run all unit tests with race detection across all modules
test:
	@for mod in $(MODULES); do echo "==> Testing $$mod"; (cd $$mod && go test -race ./...); done

## test-integration: Run integration tests (requires build tag)
test-integration:
	@for mod in $(MODULES); do echo "==> Integration testing $$mod"; (cd $$mod && go test -race -tags=integration ./...); done

## vet: Run go vet on all modules
vet:
	@for mod in $(MODULES); do echo "==> Vetting $$mod"; (cd $$mod && go vet ./...); done

## fmt: Check that all Go files are gofmt-compliant
fmt:
	@test -z "$$(gofmt -l .)" || (echo "Files not formatted:"; gofmt -l .; exit 1)

## lint: Run golangci-lint on all modules (must be installed separately)
lint:
	@for mod in $(MODULES); do echo "==> Linting $$mod"; (cd $$mod && golangci-lint run ./...); done

## cover: Generate test coverage report for all modules
cover:
	@for mod in $(MODULES); do echo "==> Coverage $$mod"; (cd $$mod && go test -race -coverprofile=$(COVERFILE) ./... && go tool cover -func=$(COVERFILE)); done

## cover-html: Open coverage report in browser (forge-cli)
cover-html:
	cd forge-cli && go test -race -coverprofile=$(COVERFILE) ./... && go tool cover -html=$(COVERFILE)

## install: Install forge to GOPATH/bin
install:
	cd forge-cli && go install -ldflags "$(LDFLAGS)" ./cmd/forge

## clean: Remove build artifacts and coverage files
clean:
	rm -f $(BINARY)
	@for mod in $(MODULES); do rm -f $$mod/$(COVERFILE); done

## release: Build a snapshot release using goreleaser
release:
	goreleaser release --snapshot --clean

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'
