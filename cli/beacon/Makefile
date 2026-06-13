# Beacon CLI Makefile

# Build variables
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Go build flags
LDFLAGS := -X github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version.Version=$(VERSION) \
           -X github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version.GitCommit=$(GIT_COMMIT) \
           -X github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version.BuildDate=$(BUILD_DATE)

# Hooks binary ldflags (embed version for upgrade display)
HOOKS_LDFLAGS := -s -w -X github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/version.Version=$(VERSION)

# Output binary name
BINARY := beacon
HOOKS_BINARY := beacon-hooks
HOOKS_DIR := ../beacon-hooks
EMBEDDED_DIR := internal/embedded

# Detect current platform
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

.PHONY: all build clean test install build-hooks build-hooks-current ensure-placeholder

all: build

# Ensure placeholder exists (for fresh clones where hooks.bin is gitignored)
ensure-placeholder:
	@if [ ! -f $(EMBEDDED_DIR)/hooks.bin ]; then \
		echo "Creating placeholder hooks.bin..."; \
		echo "PLACEHOLDER" > $(EMBEDDED_DIR)/hooks.bin; \
	fi

# Build hooks binary for current platform and copy to embedded directory
build-hooks-current: ensure-placeholder
	@echo "Building hooks binary for $(GOOS)/$(GOARCH)..."
	cd $(HOOKS_DIR) && GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags "$(HOOKS_LDFLAGS)" -o $(HOOKS_BINARY) .
	@echo "Copying hooks binary to embedded directory..."
	cp $(HOOKS_DIR)/$(HOOKS_BINARY) $(EMBEDDED_DIR)/hooks.bin
	@echo "Hooks binary ready."

# Build hooks binary for specific platform (used by goreleaser)
build-hooks-for-platform:
	@echo "Building hooks binary for $(TARGET_OS)/$(TARGET_ARCH)..."
	cd $(HOOKS_DIR) && GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) go build -ldflags "$(HOOKS_LDFLAGS)" -o $(HOOKS_BINARY)-$(TARGET_OS)-$(TARGET_ARCH)$(if $(filter windows,$(TARGET_OS)),.exe,) .
	@if command -v upx >/dev/null 2>&1; then \
		echo "Compressing with UPX..."; \
		upx --best --lzma $(HOOKS_DIR)/$(HOOKS_BINARY)-$(TARGET_OS)-$(TARGET_ARCH)$(if $(filter windows,$(TARGET_OS)),.exe,) || true; \
	fi
	cp $(HOOKS_DIR)/$(HOOKS_BINARY)-$(TARGET_OS)-$(TARGET_ARCH)$(if $(filter windows,$(TARGET_OS)),.exe,) $(EMBEDDED_DIR)/hooks.bin

# Build the CLI (with hooks for current platform)
build: build-hooks-current
	@echo "Building $(BINARY) $(VERSION)..."
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# Build CLI only (assumes hooks.bin is already in place)
build-cli-only:
	@echo "Building $(BINARY) $(VERSION)..."
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# Build for all platforms (each with matching hooks binary)
build-all: build-darwin build-linux build-windows

build-darwin: build-darwin-amd64 build-darwin-arm64

build-darwin-amd64:
	@echo "Building for macOS (amd64)..."
	$(MAKE) build-hooks-for-platform TARGET_OS=darwin TARGET_ARCH=amd64
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-amd64 .

build-darwin-arm64:
	@echo "Building for macOS (arm64)..."
	$(MAKE) build-hooks-for-platform TARGET_OS=darwin TARGET_ARCH=arm64
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-arm64 .

build-linux: build-linux-amd64 build-linux-arm64

build-linux-amd64:
	@echo "Building for Linux (amd64)..."
	$(MAKE) build-hooks-for-platform TARGET_OS=linux TARGET_ARCH=amd64
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-amd64 .

build-linux-arm64:
	@echo "Building for Linux (arm64)..."
	$(MAKE) build-hooks-for-platform TARGET_OS=linux TARGET_ARCH=arm64
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-arm64 .

build-windows: build-windows-amd64

build-windows-amd64:
	@echo "Building for Windows (amd64)..."
	$(MAKE) build-hooks-for-platform TARGET_OS=windows TARGET_ARCH=amd64
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-windows-amd64.exe .

# Clean build artifacts
clean:
	rm -f $(BINARY) $(BINARY)-*
	rm -f $(HOOKS_DIR)/$(HOOKS_BINARY) $(HOOKS_DIR)/$(HOOKS_BINARY)-*
	# Reset hooks.bin to placeholder
	echo "PLACEHOLDER" > $(EMBEDDED_DIR)/hooks.bin

# Run tests
test:
	go test -v ./...

# Install to GOPATH/bin
install: build
	@echo "Installing $(BINARY)..."
	go install -ldflags "$(LDFLAGS)" .

# Format code
fmt:
	go fmt ./...

# Lint code
lint:
	golangci-lint run

# Run go mod tidy
tidy:
	go mod tidy

# Package the external threat-rule pack (repo rules/, not embedded in the binary)
# into a tarball that `beacon rules pull` / `beacon rules add` can install. The
# release build produces the same artifact via the goreleaser before-hook.
rules-pack:
	mkdir -p release-rules
	COPYFILE_DISABLE=1 tar --exclude='._*' --exclude='.DS_Store' -czf release-rules/threat-rules.tar.gz -C ../.. rules
	@echo "wrote release-rules/threat-rules.tar.gz"
