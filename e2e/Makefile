-include ../.env
export

.PHONY: build up down logs shell reset check live-reload mock-logs-high mock-logs-normal mock-logs-status

ROOT_DIR := $(abspath $(CURDIR)/..)
DEMO_DIR := $(ROOT_DIR)/e2e

DEMO_IMAGE ?= tracer-e2e:local
DEMO_CONTAINER ?= tracer-e2e
DEMO_PORT ?= 18081
DEMO_PANEL_URL ?= http://localhost:$(DEMO_PORT)
DEMO_UNITS := demo-api.service demo-worker.service demo-cron.service

build:
	cd .. && ./configure --make-config --with-systemd=yes
	podman build -t $(DEMO_IMAGE) -f $(DEMO_DIR)/Containerfile $(ROOT_DIR)

up: down build
	podman run -d --name $(DEMO_CONTAINER) \
		--privileged \
		-p $(DEMO_PORT):8080 \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		-v $(ROOT_DIR)/landing/html:/opt/tracer/landing/html:ro \
		--tmpfs /run \
		--tmpfs /run/lock \
		--tmpfs /tmp \
		$(DEMO_IMAGE)
	@echo "E2E demo is running:"
	@echo "  Panel URL: $(DEMO_PANEL_URL)"
	@echo "  Container: $(DEMO_CONTAINER)"
	@echo "  Logs: make -C e2e logs"

down:
	@podman container exists $(DEMO_CONTAINER) && podman rm -f -t 10 $(DEMO_CONTAINER) >/dev/null || true
	@echo "E2E demo stopped"

logs:
	podman logs -f $(DEMO_CONTAINER)

shell:
	podman exec -it $(DEMO_CONTAINER) bash

reset: down

check:
	./check.sh

live-reload:
	./live-reload.sh

mock-logs-high:
	@podman container exists $(DEMO_CONTAINER) >/dev/null 2>&1 || { echo "container $(DEMO_CONTAINER) is not running"; exit 1; }
	podman exec $(DEMO_CONTAINER) systemctl set-environment TRACER_DEMO_LOG_PROFILE=high
	podman exec $(DEMO_CONTAINER) systemctl restart $(DEMO_UNITS)
	@echo "Mock service logging profile set to high"

mock-logs-normal:
	@podman container exists $(DEMO_CONTAINER) >/dev/null 2>&1 || { echo "container $(DEMO_CONTAINER) is not running"; exit 1; }
	podman exec $(DEMO_CONTAINER) systemctl unset-environment TRACER_DEMO_LOG_PROFILE
	podman exec $(DEMO_CONTAINER) systemctl restart $(DEMO_UNITS)
	@echo "Mock service logging profile set to normal"

mock-logs-status:
	@podman container exists $(DEMO_CONTAINER) >/dev/null 2>&1 || { echo "container $(DEMO_CONTAINER) is not running"; exit 1; }
	@profile="$$(podman exec $(DEMO_CONTAINER) systemctl show-environment | sed -n 's/^TRACER_DEMO_LOG_PROFILE=//p')"; \
	if [ -z "$$profile" ]; then \
		profile=normal; \
	fi; \
	echo "Mock service logging profile: $$profile"
