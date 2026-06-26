# Build configuration (overridable via environment)
WITH_SYSTEMD ?= auto
BINDIR ?= /usr/local/bin
PANEL_USER ?= tracer-panel
COLLECTOR_USER ?= tracer-collector
TRACER_GROUP ?= tracer
CGO_ENABLED ?= 0

# Container registry (Forgejo) — format: {registry}/{owner}/{image}
REGISTRY ?= git.naqerl.com
OWNER   ?= naqerl
IMAGE_PANEL  ?= $(REGISTRY)/$(OWNER)/panel

# Build version (set VERSION env var to inject, e.g. `make build VERSION=v1.2.3`)
# Defaults to "unknown" when not set (CI passes github.ref_name via VERSION).
VERSION ?= unknown

LDFLAGS := -s -w \
	-X tracer/build.Commit=$(VERSION) \
	-X tracer/build.Ref=$(VERSION) \
	-X tracer/pkg/setup.WithSystemd=$(WITH_SYSTEMD) \
	-X tracer/pkg/setup.Bindir=$(BINDIR) \
	-X tracer/pkg/setup.PanelUser=$(PANEL_USER) \
	-X tracer/pkg/setup.CollectorUser=$(COLLECTOR_USER) \
	-X tracer/pkg/setup.TracerGroup=$(TRACER_GROUP)

.PHONY: build build-collector build-panel clean test vet dev-setup generate sqlc install \
        docker-build-panel docker-tag-panel docker-push-panel

build: build-collector build-panel

build-collector:
	CGO_ENABLED=$(CGO_ENABLED) go build -ldflags="$(LDFLAGS)" -o bin/tracer-collector ./apps/collector/cmd/tracer-collector

build-panel:
	CGO_ENABLED=$(CGO_ENABLED) go build -ldflags="$(LDFLAGS)" -o bin/tracer-panel ./apps/panel/cmd/tracer-panel

clean:
	rm -rf bin/

test: vet
	go test ./...

vet: generate
	go fmt ./...
	go vet ./...
	go tool staticcheck ./...

dev-setup:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
	go mod download
	go mod tidy

generate:
	go tool templ generate

sqlc:
	cd apps/panel && sqlc generate

install: build
	@echo "Installing binaries to $(BINDIR)..."
	@install -d $(BINDIR)
	@install -m 0755 bin/tracer-collector $(BINDIR)/
	@install -m 0755 bin/tracer-panel $(BINDIR)/
	@echo "Installed: $(BINDIR)/tracer-collector"
	@echo "Installed: $(BINDIR)/tracer-panel"
	@echo ""
	@echo "Setting up tracer-collector..."
	@tracer-collector install
	@echo ""
	@echo "Setting up tracer-panel..."
	@tracer-panel install
	@echo ""
	@echo "Initializing panel..."
	@api_key=$$(tracer-panel -init 2>/dev/null | grep '^collector_api_key=' | cut -d= -f2); \
	if [ -n "$$api_key" ]; then \
		echo "Configuring collector with API key..."; \
		tracer-collector set api-key "$$api_key"; \
	else \
		echo "Panel already initialized, skipping API key setup"; \
	fi
	@echo ""
	@echo "========================================"
	@echo "Installation complete!"
	@echo "========================================"
	@echo ""
	@echo "Start services with:"
	@echo "  sudo systemctl start tracer-collector"
	@echo "  sudo systemctl start tracer-panel"

# ── Container images ──────────────────────────────────────────────────────────
# Build the panel image using the multi-stage Dockerfile.
docker-build-panel:
	docker build \
		-t $(IMAGE_PANEL):latest \
		-f apps/panel/Dockerfile \
		.

# Tag the panel image with a specific version (usage: make docker-tag-panel TAG=v1.2.3).
docker-tag-panel:
ifndef TAG
	$(error TAG is required. Usage: make docker-tag-panel TAG=v1.2.3)
endif
	docker tag $(IMAGE_PANEL):latest $(IMAGE_PANEL):$(TAG)

# Push all tags of the panel image to the registry.
docker-push-panel:
	docker push $(IMAGE_PANEL):latest
ifneq ($(TAG),)
	docker push $(IMAGE_PANEL):$(TAG)
endif
