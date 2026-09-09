FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG E2E_INSTALL_MODE=build
ARG VERSION=unknown

RUN WITH_SYSTEMD=yes VERSION=${VERSION} make build && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/tracer-demo-loggen ./e2e/mocklog/main.go

FROM debian:bookworm

LABEL org.opencontainers.image.title="tracer-demo"
LABEL org.opencontainers.image.description="Full-stack tracer demo with systemd, mock services, and pre-configured collector/panel"
LABEL org.opencontainers.image.source="https://git.naqerl.com/eco/tracer"
LABEL org.opencontainers.image.licenses="MIT"

ENV container=docker
ARG E2E_INSTALL_MODE=build
ENV E2E_INSTALL_MODE=${E2E_INSTALL_MODE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    systemd \
    systemd-sysv \
    nginx \
    ca-certificates \
    curl \
    tar \
    gawk \
    passwd \
    procps \
    iproute2 \
    bash \
    git \
    make && \
    rm -rf /var/lib/apt/lists/*

COPY --from=build /out/tracer-demo-loggen /usr/local/bin/tracer-demo-loggen
COPY --from=build /src/bin/tracer-panel /usr/local/bin/tracer-panel
COPY --from=build /src/bin/tracer-collector /usr/local/bin/tracer-collector

COPY e2e/install-on-boot.sh /opt/tracer/e2e/install-on-boot.sh
COPY e2e/nginx/ /opt/tracer/e2e/nginx/
COPY e2e/systemd/ /etc/systemd/system/

RUN mkdir -p /etc/tracer && echo "E2E_INSTALL_MODE=${E2E_INSTALL_MODE}" > /etc/tracer/e2e.env && \
    chmod 0755 /opt/tracer/e2e/install-on-boot.sh /usr/local/bin/tracer-demo-loggen /etc/systemd/system/demo-conflict-loggen.sh /etc/systemd/system/tracer-idle-collector.sh && \
    mkdir -p /etc/systemd/system/multi-user.target.wants && \
    ln -sf /etc/systemd/system/install-on-boot.service /etc/systemd/system/multi-user.target.wants/install-on-boot.service && \
    ln -sf /etc/systemd/system/demo-api.service /etc/systemd/system/multi-user.target.wants/demo-api.service && \
    ln -sf /etc/systemd/system/demo-worker.service /etc/systemd/system/multi-user.target.wants/demo-worker.service && \
    ln -sf /etc/systemd/system/demo-cron.service /etc/systemd/system/multi-user.target.wants/demo-cron.service && \
    ln -sf /etc/systemd/system/demo-conflict.service /etc/systemd/system/multi-user.target.wants/demo-conflict.service

STOPSIGNAL SIGRTMIN+3
EXPOSE 8080
CMD ["/lib/systemd/systemd"]
