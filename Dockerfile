FROM ghcr.io/libops/go:1.26.5@sha256:f952de0a7e29d3232292d98e2a9fe4855719d4179f0df35090b5a3c01a5167ba AS builder

SHELL ["/bin/ash", "-o", "pipefail", "-ex", "-c"]

WORKDIR /app

COPY go.* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY *.go ./
COPY pkg ./pkg

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /app/binary .

FROM ghcr.io/libops/base:3.2.2.0@sha256:d4da77a62c6dead3d52c0e0cc4f38647379d7edd8b2be7b17a2cce06d469b545

COPY --from=builder /app/binary /app/binary

USER goapp

ENTRYPOINT [ "/app/binary" ]

HEALTHCHECK CMD curl -sf -o /dev/null http://localhost:8080/healthcheck || exit 1
