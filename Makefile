-include .env
export

.PHONY: build build-collector build-panel build-release build-release-collector build-release-panel clean test vet deps sqlc build-mock test-endpoints

# Build all binaries
build: build-collector build-panel

# Build collector
build-collector:
	go build -o bin/tracer-collector ./cmd/collector

# Build panel
build-panel:
	go build -o bin/tracer-panel ./cmd/panel

# Build release binaries (static, stripped)
build-release: build-release-collector build-release-panel

build-release-collector:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/tracer-collector ./cmd/collector

build-release-panel:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/tracer-panel ./cmd/panel

# Clean build artifacts
clean:
	rm -rf bin/

# Run tests
test:
	go test ./...

# Run vet and staticcheck
vet:
	go vet ./...
	staticcheck ./...

# Install dependencies
deps:
	go mod tidy


# Generate SQLC code
sqlc:
	sqlc generate

# Build mock logger
build-mock:
	go build -o bin/mock-logger ./cmd/mock-logger

test-endpoints:
	go run cmd/crawler/main.go
