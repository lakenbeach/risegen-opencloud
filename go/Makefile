.PHONY: build-lib test lint clean

# Build the Go CDK wrapper shared library (.so) for Python ctypes
build-lib:
	@echo "Building Go CDK wrapper shared library..."
	go build -v -buildmode=c-shared -o libcloudadapter.so ./cmd/
	@echo "Shared library built successfully: libcloudadapter.so"
	@echo "Header file generated: libcloudadapter.h"
	@echo "Exported symbols:"
	@nm libcloudadapter.so | grep " T CloudAdapter" | sed 's/^  */  /'

# Run tests
test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...

# Run linters
lint:
	@echo "Running golangci-lint..."
	golangci-lint run ./...
	@echo "Running gofmt check..."
	test -z $$(gofmt -l .) || (gofmt -l . && exit 1)

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -f coverage.out libcloudadapter.so libcloudadapter.h
	go clean -cache -testcache -modcache

# Default target
all: build-lib test lint
