FROM docker.io/library/golang:1.25-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/tracer-panel ./cmd/panel && \
    CGO_ENABLED=0 go build -o /out/tracer-collector ./cmd/collector && \
    CGO_ENABLED=0 go build -o /out/tracer-demo-init ./cmd/demo-init

FROM docker.io/library/debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    bash \
    curl \
    sed \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/tracer-panel /usr/local/bin/tracer-panel
COPY --from=build /out/tracer-collector /usr/local/bin/tracer-collector
COPY --from=build /out/tracer-demo-init /usr/local/bin/tracer-demo-init
