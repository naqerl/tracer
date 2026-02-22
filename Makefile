-include .env
export

.PHONY: build build-collector build-panel build-release build-release-collector build-release-panel clean test vet deps sqlc build-mock test-endpoints check-boundaries

# Build all binaries
build: build-collector build-panel

# Build collector
build-collector:
	go build -o bin/tracer-collector ./apps/collector/cmd/tracer-collector

# Build panel
build-panel:
	go build -o bin/tracer-panel ./apps/panel/cmd/tracer-panel

# Build release binaries (static, stripped)
build-release: build-release-collector build-release-panel

build-release-collector:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/tracer-collector ./apps/collector/cmd/tracer-collector

build-release-panel:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/tracer-panel ./apps/panel/cmd/tracer-panel

# Clean build artifacts
clean:
	rm -rf bin/

# Run tests
test:
	go test ./...

# Run vet and staticcheck
vet: fmt
	go vet ./...
	staticcheck ./...

fmt:
	go fmt ./...

# Install dependencies
deps:
	go mod tidy

# Generate SQLC code (panel only)
sqlc:
	cd apps/panel && sqlc generate

# Build mock logger (kept in cmd/ for now)
build-mock:
	go build -o bin/mock-logger ./demo/mock-logger

test-endpoints:
	go run apps/panel/cmd/crawler/main.go

# Check import boundaries
check-boundaries:
	./scripts/check_boundaries.sh
