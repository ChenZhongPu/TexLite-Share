VERSION ?= 0.1.7
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
BUILD_DATE ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
PKG := texlite-share/internal/version

LDFLAGS := -s -w \
	-X $(PKG).Version=$(VERSION) \
	-X $(PKG).GitCommit=$(GIT_COMMIT) \
	-X $(PKG).BuildDate=$(BUILD_DATE)

BIN_DIR := bin
DIST_DIR := dist

.PHONY: all build server client test clean release version

all: build

## build: Build both server and client binaries for current platform
build: server client

## server: Build texlite-share-server
server:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/texlite-share-server ./cmd/texlite-share-server
	@echo "Built $(BIN_DIR)/texlite-share-server"

## client: Build texlite-tunnel-client
client:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/texlite-tunnel-client ./cmd/texlite-tunnel-client
	@echo "Built $(BIN_DIR)/texlite-tunnel-client"

## test: Run all unit and integration tests with race detector
test:
	go test -v -race ./...

## version: Print current version info
version:
	@echo "TexLite Share Version: $(VERSION)"
	@echo "Git Commit:            $(GIT_COMMIT)"
	@echo "Build Date:            $(BUILD_DATE)"

## release: Cross-compile static CGO-free binaries for all supported platforms
release: clean
	@mkdir -p $(DIST_DIR)
	@echo "Building release binaries for version $(VERSION) ($(GIT_COMMIT))..."
	# Linux amd64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/texlite-share-server-linux-amd64 ./cmd/texlite-share-server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/texlite-tunnel-client-linux-amd64 ./cmd/texlite-tunnel-client
	# Linux arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/texlite-share-server-linux-arm64 ./cmd/texlite-share-server
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/texlite-tunnel-client-linux-arm64 ./cmd/texlite-tunnel-client
	# macOS (Darwin) amd64 & arm64
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/texlite-tunnel-client-darwin-amd64 ./cmd/texlite-tunnel-client
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/texlite-tunnel-client-darwin-arm64 ./cmd/texlite-tunnel-client
	# Windows amd64
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/texlite-tunnel-client-windows-amd64.exe ./cmd/texlite-tunnel-client
	@echo "All release binaries successfully built in $(DIST_DIR)/"
	@ls -lh $(DIST_DIR)

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)
