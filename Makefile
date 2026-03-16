-include .env
export

.PHONY: build build-collector build-panel build-release build-release-collector build-release-panel clean test vet deps sqlc build-mock check-boundaries pre-commit dev-setup install-pre-commit-hook

build: build-collector build-panel

build-collector:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/tracer-collector ./apps/collector/cmd/tracer-collector

build-panel:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/tracer-panel ./apps/panel/cmd/tracer-panel

clean:
	rm -rf bin/

test: vet
	go test ./...

pre-commit:
	test -z "$$(go fmt ./...)"
	go vet ./...
	staticcheck ./...

vet:
	go fmt ./...
	go vet ./...
	staticcheck ./...

dev-setup: install-pre-commit-hook
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
	go install honnef.co/go/tools/cmd/staticcheck@v0.7.0
	go mod download

install-pre-commit-hook:
	@mkdir -p .git/hooks
	@printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail' 'make pre-commit' > .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@echo 'Installed .git/hooks/pre-commit'

sqlc:
	cd apps/panel && sqlc generate
