-include ../.env
export

.PHONY: build up up-live-reload down logs volumes-rm clean-volumes reset start-loggers stop-loggers restart-loggers

ROOT_DIR := $(abspath $(CURDIR)/..)
DEMO_DIR := $(ROOT_DIR)/demo

DEMO_IMAGE ?= tracer-demo:local
DEMO_POD ?= tracer-demo
DEMO_PORT ?= 18081
DEMO_PANEL_URL ?= http://localhost:$(DEMO_PORT)
DEMO_VOL_PANEL ?= tracer_demo_panel_data
DEMO_VOL_SHARED ?= tracer_demo_shared
DEMO_VOL_ALPHA ?= tracer_demo_logs_alpha
DEMO_VOL_BETA ?= tracer_demo_logs_beta
DEMO_VOL_GOMOD ?= tracer_demo_gomod
DEMO_VOL_GOCACHE ?= tracer_demo_gocache
DEMO_DEV_IMAGE ?= docker.io/library/golang:1.25-bookworm
LOGGER_CONTAINERS := alpha-json alpha-regex beta-json beta-regex

build:
	podman build -t $(DEMO_IMAGE) -f $(DEMO_DIR)/Containerfile $(ROOT_DIR)

up: down build
	@podman volume exists $(DEMO_VOL_PANEL) || podman volume create $(DEMO_VOL_PANEL) >/dev/null
	@podman volume exists $(DEMO_VOL_SHARED) || podman volume create $(DEMO_VOL_SHARED) >/dev/null
	@podman volume exists $(DEMO_VOL_ALPHA) || podman volume create $(DEMO_VOL_ALPHA) >/dev/null
	@podman volume exists $(DEMO_VOL_BETA) || podman volume create $(DEMO_VOL_BETA) >/dev/null
	@podman pod create --name $(DEMO_POD) -p $(DEMO_PORT):8080 >/dev/null
	@# Initialize panel first (creates DB, admin user, API key) before starting panel
	@podman run --rm --name $(DEMO_POD)-init --pod $(DEMO_POD) \
		-e TRACER_DATA_DIR=/demo/data \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared \
		-v $(DEMO_DIR)/config/panel.yaml:/demo/panel.yaml:ro \
		$(DEMO_IMAGE) bash -lc 'tracer-panel -init -username demo -password demo123 2>/dev/null | grep "^collector_api_key=" | cut -d= -f2 > /demo/shared/api_key'
	@# Start panel after initialization
	@podman run -d --name $(DEMO_POD)-panel --pod $(DEMO_POD) \
		-e TRACER_DATA_DIR=/demo/data \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_DIR)/config/panel.yaml:/demo/panel.yaml:ro \
		$(DEMO_IMAGE) tracer-panel -config /demo/panel.yaml >/dev/null
	@$(MAKE) start-loggers
	@podman run -d --name $(DEMO_POD)-collector-alpha --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared:ro \
		-v $(DEMO_VOL_ALPHA):/demo/logs \
		-v $(DEMO_DIR)/config/collector-alpha.yaml.tmpl:/demo/collector.yaml.tmpl:ro \
		$(DEMO_IMAGE) bash -lc 'while [ ! -s /demo/shared/api_key ]; do sleep 1; done; key=$$(cat /demo/shared/api_key); sed "s|__API_KEY__|$${key}|g" /demo/collector.yaml.tmpl > /tmp/collector.yaml; exec tracer-collector -config /tmp/collector.yaml' >/dev/null
	@podman run -d --name $(DEMO_POD)-collector-beta --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared:ro \
		-v $(DEMO_VOL_BETA):/demo/logs \
		-v $(DEMO_DIR)/config/collector-beta.yaml.tmpl:/demo/collector.yaml.tmpl:ro \
		$(DEMO_IMAGE) bash -lc 'while [ ! -s /demo/shared/api_key ]; do sleep 1; done; key=$$(cat /demo/shared/api_key); sed "s|__API_KEY__|$${key}|g" /demo/collector.yaml.tmpl > /tmp/collector.yaml; exec tracer-collector -config /tmp/collector.yaml' >/dev/null
	@echo "Demo is running:"
	@echo "  Panel URL:  $(DEMO_PANEL_URL)"
	@echo "  Credentials: demo / demo123"
	@echo "  Stop: make -C demo down"

up-live-reload: down
	@podman volume exists $(DEMO_VOL_PANEL) || podman volume create $(DEMO_VOL_PANEL) >/dev/null
	@podman volume exists $(DEMO_VOL_SHARED) || podman volume create $(DEMO_VOL_SHARED) >/dev/null
	@podman volume exists $(DEMO_VOL_ALPHA) || podman volume create $(DEMO_VOL_ALPHA) >/dev/null
	@podman volume exists $(DEMO_VOL_BETA) || podman volume create $(DEMO_VOL_BETA) >/dev/null
	@podman volume exists $(DEMO_VOL_GOMOD) || podman volume create $(DEMO_VOL_GOMOD) >/dev/null
	@podman volume exists $(DEMO_VOL_GOCACHE) || podman volume create $(DEMO_VOL_GOCACHE) >/dev/null
	@podman pod create --name $(DEMO_POD) -p $(DEMO_PORT):8080 >/dev/null
	@podman run --rm --name $(DEMO_POD)-init --pod $(DEMO_POD) \
		-e TRACER_DATA_DIR=/demo/data \
		-v $(ROOT_DIR):/workspace \
		-v $(DEMO_VOL_GOMOD):/go/pkg/mod \
		-v $(DEMO_VOL_GOCACHE):/root/.cache/go-build \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared \
		$(DEMO_DEV_IMAGE) bash -lc 'export PATH="/usr/local/go/bin:$$PATH"; cd /workspace && go run ./apps/panel/cmd/tracer-panel/main.go -init -username demo -password demo123 2>/dev/null | grep "^collector_api_key=" | cut -d= -f2 > /demo/shared/api_key' >/dev/null
	@podman run -d --name $(DEMO_POD)-panel --pod $(DEMO_POD) \
		-e TRACER_DATA_DIR=/demo/data \
		-e TRACER_PANEL_CONFIG=/demo/panel.yaml \
		-v $(ROOT_DIR):/workspace \
		-v $(DEMO_VOL_GOMOD):/go/pkg/mod \
		-v $(DEMO_VOL_GOCACHE):/root/.cache/go-build \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_DIR)/config/panel.yaml:/demo/panel.yaml:ro \
		$(DEMO_DEV_IMAGE) bash -lc 'export PATH="/usr/local/go/bin:/go/bin:$$PATH"; command -v air >/dev/null 2>&1 || go install github.com/air-verse/air@latest; cd /workspace; exec air --build.cmd "go build -o /tmp/tracer-panel ./apps/panel/cmd/tracer-panel" --build.bin "/tmp/tracer-panel" --build.include_ext "go,html,tmpl,yaml" --build.exclude_dir ".git,tmp,vendor,node_modules" --build.delay "500" --build.stop_on_error "true" --log.time "true"' >/dev/null
	@$(MAKE) start-loggers
	@podman run -d --name $(DEMO_POD)-collector-alpha --pod $(DEMO_POD) \
		-v $(ROOT_DIR):/workspace \
		-v $(DEMO_VOL_GOMOD):/go/pkg/mod \
		-v $(DEMO_VOL_GOCACHE):/root/.cache/go-build \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared:ro \
		-v $(DEMO_VOL_ALPHA):/demo/logs \
		-v $(DEMO_DIR)/config/collector-alpha.yaml.tmpl:/demo/collector.yaml.tmpl:ro \
		$(DEMO_DEV_IMAGE) bash -lc 'export PATH="/usr/local/go/bin:/go/bin:$$PATH"; command -v air >/dev/null 2>&1 || go install github.com/air-verse/air@latest; while [ ! -s /demo/shared/api_key ]; do sleep 1; done; key=$$(cat /demo/shared/api_key); sed "s|__API_KEY__|$${key}|g" /demo/collector.yaml.tmpl > /demo/collector.yaml; export TRACER_CONFIG=/demo/collector.yaml; cd /workspace; exec air --build.cmd "go build -o /tmp/tracer-collector ./apps/collector/cmd/tracer-collector" --build.bin "/tmp/tracer-collector" --build.include_ext "go,yaml,tmpl" --build.exclude_dir ".git,tmp,vendor,node_modules" --build.delay "500" --build.stop_on_error "true" --log.time "true"' >/dev/null
	@podman run -d --name $(DEMO_POD)-collector-beta --pod $(DEMO_POD) \
		-v $(ROOT_DIR):/workspace \
		-v $(DEMO_VOL_GOMOD):/go/pkg/mod \
		-v $(DEMO_VOL_GOCACHE):/root/.cache/go-build \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared:ro \
		-v $(DEMO_VOL_BETA):/demo/logs \
		-v $(DEMO_DIR)/config/collector-beta.yaml.tmpl:/demo/collector.yaml.tmpl:ro \
		$(DEMO_DEV_IMAGE) bash -lc 'export PATH="/usr/local/go/bin:/go/bin:$$PATH"; command -v air >/dev/null 2>&1 || go install github.com/air-verse/air@latest; while [ ! -s /demo/shared/api_key ]; do sleep 1; done; key=$$(cat /demo/shared/api_key); sed "s|__API_KEY__|$${key}|g" /demo/collector.yaml.tmpl > /demo/collector.yaml; export TRACER_CONFIG=/demo/collector.yaml; cd /workspace; exec air --build.cmd "go build -o /tmp/tracer-collector ./apps/collector/cmd/tracer-collector" --build.bin "/tmp/tracer-collector" --build.include_ext "go,yaml,tmpl" --build.exclude_dir ".git,tmp,vendor,node_modules" --build.delay "500" --build.stop_on_error "true" --log.time "true"' >/dev/null
	@echo "Demo is running in live-reload mode:"
	@echo "  Panel URL:  $(DEMO_PANEL_URL)"
	@echo "  Credentials: demo / demo123"
	@echo "  Source mounted: $(ROOT_DIR) -> /workspace"
	@echo "  Stop: make -C demo down"

start-loggers:
	@podman pod exists $(DEMO_POD) || (echo "Demo pod '$(DEMO_POD)' is not running. Start it first with 'make -C demo up'."; exit 1)
	@$(MAKE) stop-loggers >/dev/null
	@# Build and copy mock-logger to shared volume
	@podman run --rm \
		-v $(ROOT_DIR):/workspace \
		-v $(DEMO_VOL_SHARED):/shared \
		-v $(DEMO_VOL_GOMOD):/go/pkg/mod \
		-v $(DEMO_VOL_GOCACHE):/root/.cache/go-build \
		$(DEMO_DEV_IMAGE) bash -lc 'export PATH="/usr/local/go/bin:$$PATH"; mkdir -p /shared/bin; go build -o /shared/bin/mock-logger /workspace/demo/mock-logger/main.go' 2>/dev/null
	@podman run -d --name $(DEMO_POD)-alpha-json --pod $(DEMO_POD) \
		--init --stop-timeout 1 \
		-v $(DEMO_VOL_ALPHA):/logs \
		-v $(DEMO_VOL_SHARED):/shared \
		docker.io/library/alpine:3.20 sh -c '/shared/bin/mock-logger -output /logs/alpha-json.log -per-message 100; while true; do sleep 3600; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-alpha-regex --pod $(DEMO_POD) \
		--init --stop-timeout 1 \
		-v $(DEMO_VOL_ALPHA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/alpha-regex.log; while true; do if [ $$((i%100)) -lt 70 ]; then lvl="INFO"; elif [ $$((i%100)) -lt 90 ]; then lvl="WARN"; else lvl="ERROR"; fi; printf "%s [%s] trace=alpha-service msg=\"log entry %d\" user=user-%03d request=req-%05d\n" "$$(date "+%Y-%m-%d %H:%M:%S")" "$$lvl" "$$i" "$$((i%100))" "$$i" >> /logs/alpha-regex.log; i=$$((i+1)); sleep 2; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-beta-json --pod $(DEMO_POD) \
		--init --stop-timeout 1 \
		-v $(DEMO_VOL_BETA):/logs \
		-v $(DEMO_VOL_SHARED):/shared \
		docker.io/library/alpine:3.20 sh -c '/shared/bin/mock-logger -output /logs/beta-json.log -per-message 100; while true; do sleep 3600; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-beta-regex --pod $(DEMO_POD) \
		--init --stop-timeout 1 \
		-v $(DEMO_VOL_BETA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/beta-regex.log; while true; do if [ $$((i%100)) -lt 70 ]; then lvl="INFO"; elif [ $$((i%100)) -lt 90 ]; then lvl="WARN"; else lvl="ERROR"; fi; printf "%s|%s|trace=beta-service|msg=log entry %d|user=user-%03d|request=req-%05d\n" "$$(date -Iseconds)" "$$lvl" "$$i" "$$((i%100))" "$$i" >> /logs/beta-regex.log; i=$$((i+1)); sleep 2; done' >/dev/null
	@echo "Demo loggers started"

stop-loggers:
	@for c in $(LOGGER_CONTAINERS); do \
		podman container exists $(DEMO_POD)-$$c && podman rm -f -t 1 $(DEMO_POD)-$$c >/dev/null || true; \
	done
	@echo "Demo loggers stopped"

restart-loggers: stop-loggers start-loggers

down:
	@podman pod exists $(DEMO_POD) && podman pod rm -f -t 1 $(DEMO_POD) >/dev/null || true
	@echo "Demo stopped"

logs:
	@podman logs --tail=80 $(DEMO_POD)-panel || true
	@echo "---"
	@podman logs --tail=80 $(DEMO_POD)-collector-alpha || true
	@echo "---"
	@podman logs --tail=80 $(DEMO_POD)-collector-beta || true

reset: down
	@$(MAKE) clean-volumes
	@echo "Demo reset complete"

volumes-rm:
	@podman volume rm -f $(DEMO_VOL_PANEL) $(DEMO_VOL_SHARED) $(DEMO_VOL_ALPHA) $(DEMO_VOL_BETA) >/dev/null || true
	@echo "Demo data removed"

clean-volumes: volumes-rm
