# Build configuration (overridable via environment)
WITH_SYSTEMD ?= auto
BINDIR ?= /usr/local/bin
PANEL_USER ?= tracer-panel
COLLECTOR_USER ?= tracer-collector
TRACER_GROUP ?= tracer
CGO_ENABLED ?= 0

# Git version info for embedding
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_REF := $(shell git symbolic-ref --short HEAD 2>/dev/null || git describe --tags --exact-match 2>/dev/null || echo "unknown")

LDFLAGS := -s -w \
	-X tracer/build.Commit=$(GIT_COMMIT) \
	-X tracer/build.Ref=$(GIT_REF) \
	-X tracer/pkg/setup.WithSystemd=$(WITH_SYSTEMD) \
	-X tracer/pkg/setup.Bindir=$(BINDIR) \
	-X tracer/pkg/setup.PanelUser=$(PANEL_USER) \
	-X tracer/pkg/setup.CollectorUser=$(COLLECTOR_USER) \
	-X tracer/pkg/setup.TracerGroup=$(TRACER_GROUP)

.PHONY: build build-collector build-panel clean test vet dev-setup generate sqlc install

build: build-collector build-panel

build-collector:
	CGO_ENABLED=$(CGO_ENABLED) go build -ldflags="$(LDFLAGS)" -o bin/tracer-collector ./apps/collector/cmd/tracer-collector

build-panel:
	CGO_ENABLED=$(CGO_ENABLED) go build -ldflags="$(LDFLAGS)" -o bin/tracer-panel ./apps/panel/cmd/tracer-panel

clean:
	rm -rf bin/

test: vet
	go test ./...

vet:
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
