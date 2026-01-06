.PHONY: build build-collector build-panel build-release clean test lint generate

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
dev: build build-mock dev-stop
	@echo "Starting tracer demo..."
	@mkdir -p /tmp/tracer-data
	@rm -f /tmp/tracer-mock.log
	@touch /tmp/tracer-mock.log
	@echo "Starting panel on :8080..."
	@./bin/tracer-panel -config dev.panel.toml > /tmp/tracer-panel.log 2>&1 & echo $$! > /tmp/tracer-panel.pid
	@sleep 1
	@echo "Starting mock logger..."
	@./bin/mock-logger -output /tmp/tracer-mock.log -interval 1s > /tmp/tracer-mock-logger.log 2>&1 & echo $$! > /tmp/tracer-mock.pid
	@sleep 1
	@echo "Starting collector..."
	@./bin/tracer-collector -config dev.collector.toml > /tmp/tracer-collector.log 2>&1 & echo $$! > /tmp/tracer-collector.pid
	@sleep 1
	@echo ""
	@echo "====================================="
	@echo "Tracer demo is running!"
	@echo "  Panel:     http://localhost:8080"
	@echo "  Mock logs: /tmp/tracer-mock.log"
	@echo "====================================="
	@echo ""
	@echo "Run 'make dev-stop' to stop all components"
	@echo "Tailing mock logs (Ctrl+C to stop tailing, services keep running)..."
	@echo ""
	@tail -f /tmp/tracer-mock.log

# Stop all dev components
dev-stop:
	@if [ -f /tmp/tracer-panel.pid ]; then kill $$(cat /tmp/tracer-panel.pid) 2>/dev/null || true; rm -f /tmp/tracer-panel.pid; fi
	@if [ -f /tmp/tracer-collector.pid ]; then kill $$(cat /tmp/tracer-collector.pid) 2>/dev/null || true; rm -f /tmp/tracer-collector.pid; fi
	@if [ -f /tmp/tracer-mock.pid ]; then kill $$(cat /tmp/tracer-mock.pid) 2>/dev/null || true; rm -f /tmp/tracer-mock.pid; fi
	@pkill -x tracer-panel 2>/dev/null || true
	@pkill -x tracer-collector 2>/dev/null || true
	@pkill -x mock-logger 2>/dev/null || true
	@echo "All dev components stopped"

# View panel logs
dev-logs:
	@tail -f /tmp/tracer-panel.log
