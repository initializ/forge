BINARY    := forge
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
COVERFILE := coverage.out
MODULES   := forge-core forge-cli forge-plugins

.PHONY: build build-brain test test-brain test-integration vet fmt lint cover cover-html install clean release help

## build: Compile the forge binary
build:
	cd forge-cli && go build -ldflags "$(LDFLAGS)" -o ../$(BINARY) ./cmd/forge

## build-brain: Compile forge with local brain inference (requires CGo + C++ compiler)
LLAMA_GO_MOD := $(shell cd forge-core && go list -m -f '{{.Dir}}' github.com/tcpipuk/llama-go 2>/dev/null)

build-brain: build-brain-deps
	cd forge-cli && CGO_ENABLED=1 \
		CGO_CXXFLAGS="-std=c++17 -I$(LLAMA_GO_MOD) -I$(LLAMA_GO_MOD)/llama.cpp/include -I$(LLAMA_GO_MOD)/llama.cpp/ggml/include -I$(LLAMA_GO_MOD)/llama.cpp/common -I$(LLAMA_GO_MOD)/llama.cpp/vendor" \
		CGO_CFLAGS="-I$(LLAMA_GO_MOD) -I$(LLAMA_GO_MOD)/llama.cpp/include -I$(LLAMA_GO_MOD)/llama.cpp/ggml/include" \
		CGO_LDFLAGS="-L$(LLAMA_GO_MOD) -L$(LLAMA_GO_MOD)/build/bin -lbinding -lcommon -lllama -lggml -lggml-cpu -lggml-base -lggml-metal -lggml-blas -lstdc++ -lm -lomp -framework Accelerate -framework Metal -framework Foundation -framework MetalKit -Wl,-rpath,$(LLAMA_GO_MOD) -Wl,-rpath,$(LLAMA_GO_MOD)/build/bin" \
		LIBRARY_PATH="$(LLAMA_GO_MOD):$(LLAMA_GO_MOD)/build/bin" \
		go build -tags brain -ldflags "$(LDFLAGS)" -o ../$(BINARY) ./cmd/forge

## build-brain-deps: Clone llama.cpp submodule and compile static libs for llama-go
build-brain-deps:
	@if [ ! -d "$(LLAMA_GO_MOD)/llama.cpp/include" ]; then \
		echo "==> Cloning llama.cpp into llama-go module cache..."; \
		chmod -R u+w "$(LLAMA_GO_MOD)" 2>/dev/null || true; \
		TMPDIR=$$(mktemp -d) && \
		git clone --recurse-submodules https://github.com/tcpipuk/llama-go "$$TMPDIR/repo" && \
		COMMIT=$$(echo "$(LLAMA_GO_MOD)" | grep -o '[0-9a-f]\{12\}$$') && \
		cd "$$TMPDIR/repo" && git checkout "$$COMMIT" && git submodule update --init --recursive && \
		cp -r "$$TMPDIR/repo/llama.cpp" "$(LLAMA_GO_MOD)/llama.cpp" && \
		rm -rf "$$TMPDIR"; \
	fi
	@if [ ! -f "$(LLAMA_GO_MOD)/libbinding.a" ]; then \
		echo "==> Building llama.cpp static libraries..."; \
		chmod -R u+w "$(LLAMA_GO_MOD)" 2>/dev/null || true; \
		cd "$(LLAMA_GO_MOD)" && $(MAKE) libbinding.a || true; \
		cd "$(LLAMA_GO_MOD)" && for f in build/bin/*.dylib; do cp "$$f" "./$$(basename $$f)" 2>/dev/null; done; \
		cd "$(LLAMA_GO_MOD)" && ln -sf libllama.0.0.0.dylib libllama.dylib 2>/dev/null; \
		cd "$(LLAMA_GO_MOD)" && ln -sf libggml.0.9.5.dylib libggml.dylib 2>/dev/null; \
		cd "$(LLAMA_GO_MOD)" && ln -sf libggml-base.0.9.5.dylib libggml-base.dylib 2>/dev/null; \
		cd "$(LLAMA_GO_MOD)" && ln -sf libggml-cpu.0.9.5.dylib libggml-cpu.dylib 2>/dev/null; \
		cd "$(LLAMA_GO_MOD)" && ln -sf libggml-metal.0.9.5.dylib libggml-metal.dylib 2>/dev/null; \
		cd "$(LLAMA_GO_MOD)" && ln -sf libggml-blas.0.9.5.dylib libggml-blas.dylib 2>/dev/null; \
		cd "$(LLAMA_GO_MOD)" && sed -i '' 's/-lgomp/-lomp/g' model.go 2>/dev/null; \
	else \
		echo "==> llama.cpp libraries already built"; \
	fi

## test: Run all unit tests with race detection across all modules
test:
	@for mod in $(MODULES); do echo "==> Testing $$mod"; (cd $$mod && go test -race ./...); done

## test-brain: Run brain tests with CGo enabled
test-brain:
	cd forge-core && CGO_ENABLED=1 go test -tags brain -race ./brain/...

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
