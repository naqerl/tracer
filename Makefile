.PHONY: build build-collector clean test lint

# Build all binaries
build: build-collector

# Build collector
build-collector:
	go build -o bin/tracer-collector ./cmd/collector

# Clean build artifacts
clean:
	rm -rf bin/

# Run tests
test:
	go test ./...

# Run linter
lint:
	golangci-lint run

# Install dependencies
deps:
	go mod tidy

# Development: run collector with example config
run-collector:
	go run ./cmd/collector -config config.example.toml
