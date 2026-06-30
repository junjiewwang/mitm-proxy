.PHONY: build test clean release lint vet

# 变量
BINARY_NAME := mitm-proxy
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)"
GOFLAGS := -trimpath

# === 构建 ===

build:
	go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME) .

build-linux:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME)-linux-amd64 .

build-darwin:
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME)-darwin-arm64 .

build-all: build-linux build-darwin

# === 测试 ===

test:
	go test -v -race -count=1 ./...

test-short:
	go test -short ./...

bench:
	go test -bench=. -benchmem -run=^$$ ./rule/ ./processor/

# === 代码质量 ===

vet:
	go vet ./...

lint: vet
	@echo "=== Build check ==="
	go build ./...
	@echo "=== Sensitive info scan ==="
	@! grep -rEn "(cloud\.tencent\.com|1500000|700001|251005942|9\.223)" --include="*.go" --include="*.yaml" \
		| grep -v ".rules" | grep -v "docs/" | grep -v "examples/" | grep -v "Makefile" \
		&& echo "PASS: No sensitive info found" || echo "WARNING: Sensitive info detected"

# === 清理 ===

clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-linux-* $(BINARY_NAME)-darwin-*
	rm -rf dist/

# === 发布 ===

release: lint test build-all
	@echo "Release artifacts:"
	@ls -la $(BINARY_NAME)-*

# === 帮助 ===

help:
	@echo "Available targets:"
	@echo "  build       - Build binary for current platform"
	@echo "  build-all   - Build for linux/amd64 + darwin/arm64"
	@echo "  test        - Run all tests with race detector"
	@echo "  bench       - Run benchmarks (rule matching, processor)"
	@echo "  vet         - Run go vet"
	@echo "  lint        - Full lint (build + vet + sensitive scan)"
	@echo "  clean       - Remove build artifacts"
	@echo "  release     - Lint + test + build-all"
