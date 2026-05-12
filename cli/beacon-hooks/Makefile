# Beacon Hooks Makefile

# Build variables
BINARY := beacon-hooks
LDFLAGS := -s -w

.PHONY: all build clean test build-all build-darwin build-linux build-windows compress

all: build

# Build for current platform
build:
	@echo "Building $(BINARY)..."
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# Build and compress for current platform
build-compressed: build
	@echo "Compressing $(BINARY) with UPX..."
	upx --best --lzma $(BINARY) || echo "UPX not available, skipping compression"

# Build for all platforms
build-all: build-darwin build-linux build-windows

# macOS builds
build-darwin: build-darwin-amd64 build-darwin-arm64

build-darwin-amd64:
	@echo "Building for macOS (amd64)..."
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-amd64 .

build-darwin-arm64:
	@echo "Building for macOS (arm64)..."
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-arm64 .

# Linux builds
build-linux: build-linux-amd64 build-linux-arm64

build-linux-amd64:
	@echo "Building for Linux (amd64)..."
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-amd64 .

build-linux-arm64:
	@echo "Building for Linux (arm64)..."
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-arm64 .

# Windows builds
build-windows: build-windows-amd64

build-windows-amd64:
	@echo "Building for Windows (amd64)..."
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-windows-amd64.exe .

# Compress all binaries with UPX
compress:
	@echo "Compressing binaries with UPX..."
	@for f in $(BINARY)-*; do \
		if [ -f "$$f" ]; then \
			echo "Compressing $$f..."; \
			upx --best --lzma "$$f" || echo "Failed to compress $$f"; \
		fi; \
	done

# Build all and compress
build-all-compressed: build-all compress

# Clean build artifacts
clean:
	rm -f $(BINARY) $(BINARY)-*

# Run tests
test:
	go test -v ./...

# Format code
fmt:
	go fmt ./...

# Tidy dependencies
tidy:
	go mod tidy
