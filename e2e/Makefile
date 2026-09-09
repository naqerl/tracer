-include ../.env
export

.PHONY: build build-release up up-release down logs shell reset check check-install live-reload mock-logs-high mock-logs-normal mock-logs-status

ROOT_DIR := $(abspath $(CURDIR)/..)
DEMO_DIR := $(ROOT_DIR)/e2e

DEMO_IMAGE ?= tracer-e2e:local
DEMO_CONTAINER ?= tracer-e2e
DEMO_PORT ?= 18081
DEMO_PANEL_URL ?= http://localhost:$(DEMO_PORT)
DEMO_UNITS := demo-api.service demo-worker.service demo-cron.service demo-conflict.service

# The public demo panel is gated by nginx (port 8081), which enforces read-only
# when E2E_PANEL_READONLY is non-zero. Set E2E_PANEL_READONLY=0 to allow
# state-changing requests (e.g. notifier management) through the proxy.
E2E_PANEL_READONLY ?= 1

build:
	docker build -t $(DEMO_IMAGE) -f $(DEMO_DIR)/Dockerfile $(ROOT_DIR)

build-release:
	docker build -t $(DEMO_IMAGE) --build-arg E2E_INSTALL_MODE=release -f $(DEMO_DIR)/Dockerfile $(ROOT_DIR)

up: down build
	docker run -d --name $(DEMO_CONTAINER) \
		--privileged \
		--cgroupns=host \
		-p $(DEMO_PORT):8081 \
		-e E2E_PANEL_READONLY=$(E2E_PANEL_READONLY) \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		--tmpfs /run \
		--tmpfs /run/lock \
		--tmpfs /tmp \
		$(DEMO_IMAGE)
	@echo "E2E demo is running:"
	@echo "  Panel URL: $(DEMO_PANEL_URL)"
	@echo "  Container: $(DEMO_CONTAINER)"
	@echo "  Logs: make -C e2e logs"

up-release: down build-release
	docker run -d --name $(DEMO_CONTAINER) \
		--privileged \
		--cgroupns=host \
		-p $(DEMO_PORT):8081 \
		-e E2E_PANEL_READONLY=$(E2E_PANEL_READONLY) \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		--tmpfs /run \
		--tmpfs /run/lock \
		--tmpfs /tmp \
		$(DEMO_IMAGE)
	@echo "E2E demo (release install) is running:"
	@echo "  Panel URL: $(DEMO_PANEL_URL)"
	@echo "  Container: $(DEMO_CONTAINER)"
	@echo "  Logs: make -C e2e logs"

down:
	@docker inspect $(DEMO_CONTAINER) >/dev/null 2>&1 && docker stop -t 10 $(DEMO_CONTAINER) >/dev/null && docker rm -f $(DEMO_CONTAINER) >/dev/null || true
	@echo "E2E demo stopped"

logs:
	docker logs -f $(DEMO_CONTAINER)

shell:
	docker exec -it $(DEMO_CONTAINER) bash

reset: down

check:
	./check.sh

check-install:
	./check.sh

live-reload:
	./live-reload.sh

mock-logs-high:
	@docker inspect $(DEMO_CONTAINER) >/dev/null 2>&1 || { echo "container $(DEMO_CONTAINER) is not running"; exit 1; }
	docker exec $(DEMO_CONTAINER) systemctl set-environment TRACER_DEMO_LOG_PROFILE=high
	docker exec $(DEMO_CONTAINER) systemctl restart $(DEMO_UNITS)
	@echo "Mock service logging profile set to high"

mock-logs-normal:
	@docker inspect $(DEMO_CONTAINER) >/dev/null 2>&1 || { echo "container $(DEMO_CONTAINER) is not running"; exit 1; }
	docker exec $(DEMO_CONTAINER) systemctl unset-environment TRACER_DEMO_LOG_PROFILE
	docker exec $(DEMO_CONTAINER) systemctl restart $(DEMO_UNITS)
	@echo "Mock service logging profile set to normal"

mock-logs-status:
	@docker inspect $(DEMO_CONTAINER) >/dev/null 2>&1 || { echo "container $(DEMO_CONTAINER) is not running"; exit 1; }
	@profile="$$(docker exec $(DEMO_CONTAINER) systemctl show-environment | sed -n 's/^TRACER_DEMO_LOG_PROFILE=//p')"; \
	if [ -z "$$profile" ]; then \
		profile=normal; \
	fi; \
	echo "Mock service logging profile: $$profile"
