.PHONY: build build-collector build-panel clean test lint generate

# Build all binaries
build: build-collector build-panel

# Build collector
build-collector:
	go build -o bin/tracer-collector ./cmd/collector

# Build panel
build-panel:
	go build -o bin/tracer-panel ./cmd/panel

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

# Generate SQLC code
generate:
	sqlc generate

# Development: run collector with example config
run-collector:
	go run ./cmd/collector -config config.example.toml

# Development: run panel with example config
run-panel:
	go run ./cmd/panel -config panel.example.toml

# Build mock logger
build-mock:
	go build -o bin/mock-logger ./cmd/mock-logger

# Development: run full demo (panel + collector + mock logger)
# Runs all components in background and shows logs
dev: build build-mock
	@echo "Starting tracer demo..."
	@mkdir -p /tmp/tracer-data
	@rm -f /tmp/tracer-mock.log
	@touch /tmp/tracer-mock.log
	@echo "Starting panel on :8080..."
	@./bin/tracer-panel -config dev.panel.toml &
	@sleep 1
	@echo "Starting mock logger..."
	@./bin/mock-logger -output /tmp/tracer-mock.log -interval 1s &
	@sleep 1
	@echo "Starting collector..."
	@./bin/tracer-collector -config dev.collector.toml &
	@echo ""
	@echo "====================================="
	@echo "Tracer demo is running!"
	@echo "  Panel:     http://localhost:8080"
	@echo "  Mock logs: /tmp/tracer-mock.log"
	@echo "====================================="
	@echo ""
	@echo "Press Ctrl+C to stop all components"
	@trap 'pkill -f tracer-panel; pkill -f tracer-collector; pkill -f mock-logger; exit 0' INT; \
	tail -f /tmp/tracer-mock.log

# Stop all dev components
dev-stop:
	@pkill -f tracer-panel || true
	@pkill -f tracer-collector || true
	@pkill -f mock-logger || true
	@echo "All components stopped"
