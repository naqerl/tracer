-include ../.env
export

.PHONY: build up down logs shell reset

ROOT_DIR := $(abspath $(CURDIR)/..)
DEMO_DIR := $(ROOT_DIR)/newdemo

DEMO_IMAGE ?= tracer-newdemo:local
DEMO_CONTAINER ?= tracer-newdemo
DEMO_PORT ?= 18081
DEMO_PANEL_URL ?= http://localhost:$(DEMO_PORT)

build:
	podman build -t $(DEMO_IMAGE) -f $(DEMO_DIR)/Containerfile $(ROOT_DIR)

up: down build
	podman run -d --name $(DEMO_CONTAINER) \
		--privileged \
		-p $(DEMO_PORT):8080 \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		--tmpfs /run \
		--tmpfs /run/lock \
		--tmpfs /tmp \
		$(DEMO_IMAGE)
	@echo "New demo is running:"
	@echo "  Panel URL: $(DEMO_PANEL_URL)"
	@echo "  Container: $(DEMO_CONTAINER)"
	@echo "  Logs: make -C newdemo logs"

down:
	@podman container exists $(DEMO_CONTAINER) && podman rm -f -t 10 $(DEMO_CONTAINER) >/dev/null || true
	@echo "New demo stopped"

logs:
	podman logs -f $(DEMO_CONTAINER)

shell:
	podman exec -it $(DEMO_CONTAINER) bash

reset: down
