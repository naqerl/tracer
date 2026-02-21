-include ../.env
export

.PHONY: build up down logs volumes-rm clean-volumes reset

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

build:
	podman build -t $(DEMO_IMAGE) -f $(DEMO_DIR)/Containerfile $(ROOT_DIR)

up: down build
	@podman volume exists $(DEMO_VOL_PANEL) || podman volume create $(DEMO_VOL_PANEL) >/dev/null
	@podman volume exists $(DEMO_VOL_SHARED) || podman volume create $(DEMO_VOL_SHARED) >/dev/null
	@podman volume exists $(DEMO_VOL_ALPHA) || podman volume create $(DEMO_VOL_ALPHA) >/dev/null
	@podman volume exists $(DEMO_VOL_BETA) || podman volume create $(DEMO_VOL_BETA) >/dev/null
	@podman pod create --name $(DEMO_POD) -p $(DEMO_PORT):8080 >/dev/null
	@podman run -d --name $(DEMO_POD)-panel --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_DIR)/config/panel.toml:/demo/panel.toml:ro \
		$(DEMO_IMAGE) tracer-panel -config /demo/panel.toml >/dev/null
	@podman run --rm --name $(DEMO_POD)-init --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared \
		$(DEMO_IMAGE) tracer-demo-init --data-dir /demo/data --username demo --password demo123 --key-file /demo/shared/api_key >/dev/null
	@podman run -d --name $(DEMO_POD)-alpha-json --pod $(DEMO_POD) \
		--init \
		--stop-timeout 1 \
		-v $(DEMO_VOL_ALPHA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/alpha-json.log; while true; do printf "{\"timestamp\":\"%s\",\"level\":\"error\",\"trace_id\":\"alpha-checkout\",\"message\":\"checkout request failed\",\"user_id\":\"user-%03d\",\"request_id\":\"req-alpha-%05d\",\"region\":\"us-east\",\"latency_ms\":%d}\n" "$$(date -Iseconds)" "$$((i%50))" "$$i" "$$((100 + (i%900)))" >> /logs/alpha-json.log; i=$$((i+1)); sleep 1; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-alpha-regex --pod $(DEMO_POD) \
		--init \
		--stop-timeout 1 \
		-v $(DEMO_VOL_ALPHA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/alpha-regex.log; while true; do printf "%s [WARN] trace=alpha-payment msg=\"payment authorization failed\" user=user-%03d request=req-pay-%05d amount=%.2f\n" "$$(date "+%Y-%m-%d %H:%M:%S")" "$$((i%40))" "$$i" "$$(awk "BEGIN { printf \"%.2f\", 10 + ($$i % 90) + 0.25 }")" >> /logs/alpha-regex.log; i=$$((i+1)); sleep 2; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-beta-json --pod $(DEMO_POD) \
		--init \
		--stop-timeout 1 \
		-v $(DEMO_VOL_BETA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/beta-json.log; while true; do printf "{\"time\":\"%s\",\"severity\":\"error\",\"trace\":\"beta-inventory\",\"msg\":\"inventory sync timeout\",\"warehouse\":\"wh-%02d\",\"batch_id\":\"batch-%05d\",\"attempt\":%d}\n" "$$(date -Iseconds)" "$$((i%7))" "$$i" "$$((1 + (i%3)))" >> /logs/beta-json.log; i=$$((i+1)); sleep 3; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-beta-regex --pod $(DEMO_POD) \
		--init \
		--stop-timeout 1 \
		-v $(DEMO_VOL_BETA):/logs \
		docker.io/library/alpine:3.20 sh -c 'i=0; touch /logs/beta-regex.log; while true; do printf "%s|INFO|trace=beta-notify|msg=notification delivery failed|channel=telegram|target=user-%03d|attempt=%d\n" "$$(date -Iseconds)" "$$((i%30))" "$$((1 + (i%4)))" >> /logs/beta-regex.log; i=$$((i+1)); sleep 2; done' >/dev/null
	@podman run -d --name $(DEMO_POD)-collector-alpha --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared:ro \
		-v $(DEMO_VOL_ALPHA):/demo/logs \
		-v $(DEMO_DIR)/config/collector-alpha.toml.tmpl:/demo/collector.toml.tmpl:ro \
		$(DEMO_IMAGE) bash -lc 'while [ ! -s /demo/shared/api_key ]; do sleep 1; done; key=$$(cat /demo/shared/api_key); sed "s|__API_KEY__|$${key}|g" /demo/collector.toml.tmpl > /tmp/collector.toml; exec tracer-collector -config /tmp/collector.toml' >/dev/null
	@podman run -d --name $(DEMO_POD)-collector-beta --pod $(DEMO_POD) \
		-v $(DEMO_VOL_PANEL):/demo/data \
		-v $(DEMO_VOL_SHARED):/demo/shared:ro \
		-v $(DEMO_VOL_BETA):/demo/logs \
		-v $(DEMO_DIR)/config/collector-beta.toml.tmpl:/demo/collector.toml.tmpl:ro \
		$(DEMO_IMAGE) bash -lc 'while [ ! -s /demo/shared/api_key ]; do sleep 1; done; key=$$(cat /demo/shared/api_key); sed "s|__API_KEY__|$${key}|g" /demo/collector.toml.tmpl > /tmp/collector.toml; exec tracer-collector -config /tmp/collector.toml' >/dev/null
	@echo "Demo is running:"
	@echo "  Panel URL:  $(DEMO_PANEL_URL)"
	@echo "  Credentials: demo / demo123"
	@echo "  Stop: make -C demo down"

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
