-include .env
export

.PHONY: install build build-collector build-panel build-release build-release-collector build-release-panel clean test vet deps sqlc build-mock dev dev-panel dev-collector dev-genlog dev-stop dev-logs demo-build demo-up demo-down demo-logs demo-reset test-endpoints

DEV_SESSION ?= tracer-dev
TMUXP_CONFIG ?= tmuxp.dev.yaml
TMUXP_CMD ?= uvx tmuxp
DEV_PANEL_PORT ?= $(shell awk -F'"' '/^port[[:space:]]*=[[:space:]]*"/ {print $$2; exit}' dev.panel.toml)
DEV_PANEL_URL ?= http://localhost:$(DEV_PANEL_PORT)

DEMO_IMAGE ?= tracer-demo:local
DEMO_POD ?= tracer-demo
DEMO_PORT ?= 18081
DEMO_PANEL_URL ?= http://localhost:$(DEMO_PORT)
DEMO_VOL_PANEL ?= tracer_demo_panel_data
DEMO_VOL_SHARED ?= tracer_demo_shared
DEMO_VOL_ALPHA ?= tracer_demo_logs_alpha
DEMO_VOL_BETA ?= tracer_demo_logs_beta

install:
	@echo "uv/uvx is used to run tmuxp (tmux session manager)"
	command -v uv >/dev/null 2>&1 || curl -LsSf https://astral.sh/uv/install.sh | sh
	command -v tmux >/dev/null 2>&1 || (echo "tmux is required"; exit 1)

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

# Development: run panel + collector + log generator in tmuxp
dev: dev-stop
	@echo "Starting tracer dev session via tmuxp..."
	@$(TMUXP_CMD) load -d $(TMUXP_CONFIG)
	@echo ""
	@echo "====================================="
	@echo "Tracer demo is running!"
	@echo "  Panel:     $(DEV_PANEL_URL)"
	@echo "  Mock logs: /tmp/tracer-mock.log"
	@echo "  Session:   $(DEV_SESSION)"
	@echo "====================================="
	@echo ""
	@echo "Attach with: tmux attach -t $(DEV_SESSION)"
	@echo "Stop with:   make dev-stop"

# Runner used by tmuxp pane: panel
dev-panel:
	@mkdir -p /tmp/tracer-data
	@exec go run ./cmd/panel -config dev.panel.toml 2>&1 | tee /tmp/tracer-panel.log

# Runner used by tmuxp pane: collector
dev-collector:
	@exec go run ./cmd/collector -config dev.collector.toml 2>&1 | tee /tmp/tracer-collector.log

# Runner used by tmuxp pane: synthetic log generator
dev-genlog:
	@truncate -s 0 /tmp/tracer-mock.log
	@exec go run ./cmd/mock-logger -output /tmp/tracer-mock.log -interval 1s 2>&1 | tee /tmp/tracer-mock-logger.log

# Stop dev tmux session
dev-stop:
	@tmux has-session -t $(DEV_SESSION) 2>/dev/null && tmux kill-session -t $(DEV_SESSION) || true
	@echo "Dev session stopped"

# View panel logs
dev-logs:
	@tmux has-session -t $(DEV_SESSION) 2>/dev/null && tmux attach -t $(DEV_SESSION) || tail -f /tmp/tracer-panel.log

demo-build:
	podman build -t $(DEMO_IMAGE) -f demo/Dockerfile .

demo-up: demo-down demo-build
	@podman volume exists $(DEMO_VOL_PANEL) || podman volume create $(DEMO_VOL_PANEL) >/dev/null
	@podman volume exists $(DEMO_VOL_SHARED) || podman volume create $(DEMO_VOL_SHARED) >/dev/null
	@podman volume exists $(DEMO_VOL_ALPHA) || podman volume create $(DEMO_VOL_ALPHA) >/dev/null
	@podman volume exists $(DEMO_VOL_BETA) || podman volume create $(DEMO_VOL_BETA) >/dev/null
	@podman pod create --name $(DEMO_POD) -p $(DEMO_PORT):8080 >/dev/null
	@podman run -d --name $(DEMO_POD)-panel --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(CURDIR)/demo/config/panel.toml:/demo/panel.toml:ro \
		$(DEMO_IMAGE) tracer-panel -config /demo/panel.toml >/dev/null
	@podman run --rm --name $(DEMO_POD)-init --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared \
		$(DEMO_IMAGE) tracer-demo-init --data-dir /demo/data --username demo --password demo123 --key-file /demo/shared/api_key >/dev/null
	@podman run -d --name $(DEMO_POD)-alpha-json --pod $(DEMO_POD) \
		-v $(DEMO_VOL_ALPHA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/alpha-json.log; while true; do printf "{\"timestamp\":\"%s\",\"level\":\"info\",\"trace_id\":\"alpha-%04d\",\"message\":\"alpha json event %d\"}\n" "$$(date -Iseconds)" "$$i" "$$i" >> /logs/alpha-json.log; i=$$((i+1)); sleep 1; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-alpha-regex --pod $(DEMO_POD) \
		-v $(DEMO_VOL_ALPHA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/alpha-regex.log; while true; do printf "%s [WARN] trace=alpha-rx-%04d msg=alpha regex event %d\n" "$$(date "+%Y-%m-%d %H:%M:%S")" "$$i" "$$i" >> /logs/alpha-regex.log; i=$$((i+1)); sleep 2; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-beta-json --pod $(DEMO_POD) \
		-v $(DEMO_VOL_BETA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/beta-json.log; while true; do printf "{\"time\":\"%s\",\"severity\":\"ERROR\",\"trace\":\"beta-%04d\",\"msg\":\"beta json failure %d\"}\n" "$$(date -Iseconds)" "$$i" "$$i" >> /logs/beta-json.log; i=$$((i+1)); sleep 3; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-beta-regex --pod $(DEMO_POD) \
		-v $(DEMO_VOL_BETA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/beta-regex.log; while true; do printf "%s|INFO|trace=beta-rx-%04d|beta regex event %d\n" "$$(date -Iseconds)" "$$i" "$$i" >> /logs/beta-regex.log; i=$$((i+1)); sleep 2; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-collector-alpha --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared:ro \
		-v $(DEMO_VOL_ALPHA):/demo/logs \
		-v $(CURDIR)/demo/config/collector-alpha.toml.tmpl:/demo/collector.toml.tmpl:ro \
		$(DEMO_IMAGE) bash -lc 'while [ ! -s /demo/shared/api_key ]; do sleep 1; done; key=$$(cat /demo/shared/api_key); sed "s|__API_KEY__|$${key}|g" /demo/collector.toml.tmpl > /tmp/collector.toml; exec tracer-collector -config /tmp/collector.toml' >/dev/null
	@podman run -d --name $(DEMO_POD)-collector-beta --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared:ro \
		-v $(DEMO_VOL_BETA):/demo/logs \
		-v $(CURDIR)/demo/config/collector-beta.toml.tmpl:/demo/collector.toml.tmpl:ro \
		$(DEMO_IMAGE) bash -lc 'while [ ! -s /demo/shared/api_key ]; do sleep 1; done; key=$$(cat /demo/shared/api_key); sed "s|__API_KEY__|$${key}|g" /demo/collector.toml.tmpl > /tmp/collector.toml; exec tracer-collector -config /tmp/collector.toml' >/dev/null
	@echo "Demo is running:"
	@echo "  Panel URL:  $(DEMO_PANEL_URL)"
	@echo "  Credentials: demo / demo123"
	@echo "  Stop: make demo-down"

demo-down:
	@podman pod exists $(DEMO_POD) && podman pod rm -f $(DEMO_POD) >/dev/null || true
	@echo "Demo stopped"

demo-logs:
	@podman logs --tail=80 $(DEMO_POD)-panel || true
	@echo "---"
	@podman logs --tail=80 $(DEMO_POD)-collector-alpha || true
	@echo "---"
	@podman logs --tail=80 $(DEMO_POD)-collector-beta || true

demo-reset: demo-down
	@podman volume rm -f $(DEMO_VOL_PANEL) $(DEMO_VOL_SHARED) $(DEMO_VOL_ALPHA) $(DEMO_VOL_BETA) >/dev/null || true
	@echo "Demo data removed"

test-endpoints:
	go run cmd/crawler/main.go
