include build/config.mk
export

# Set build tags based on configure options
ifeq ($(WITH_SYSTEMD),yes)
    BUILD_TAGS := systemd
else
    BUILD_TAGS :=
endif

TAGS_FLAG := $(if $(BUILD_TAGS),-tags $(BUILD_TAGS))

# Git version info for embedding
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_REF := $(shell git symbolic-ref --short HEAD 2>/dev/null || git describe --tags --exact-match 2>/dev/null || echo "unknown")

LDFLAGS := -s -w \
	-X tracer/build.Commit=$(GIT_COMMIT) \
	-X tracer/build.Ref=$(GIT_REF)

.PHONY: build build-collector build-panel clean test vet dev-setup sqlc

build: build-collector build-panel

build-collector:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o bin/tracer-collector ./apps/collector/cmd/tracer-collector

build-panel:
	CGO_ENABLED=0 go build $(TAGS_FLAG) -ldflags="$(LDFLAGS)" -o bin/tracer-panel ./apps/panel/cmd/tracer-panel

clean:
	rm -rf bin/
	rm -f build/config.mk

test: vet
	go test ./...

vet:
	go fmt ./...
	go vet ./...
	staticcheck ./...

dev-setup:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
	go install honnef.co/go/tools/cmd/staticcheck@v0.7.0
	go mod download

sqlc:
	cd apps/panel && sqlc generate
