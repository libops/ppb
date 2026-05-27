FROM ghcr.io/libops/base:main@sha256:389706a359c6ba0a4ffb9c3d21c0d909a1c2e5e0c4ab79cb5779129267207049 AS builder

SHELL ["/bin/ash", "-o", "pipefail", "-ex", "-c"]

WORKDIR /app

COPY go.* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY *.go ./
COPY pkg ./pkg

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /app/binary .

FROM ghcr.io/libops/base:main@sha256:389706a359c6ba0a4ffb9c3d21c0d909a1c2e5e0c4ab79cb5779129267207049

COPY --from=builder /app/binary /app/binary

USER goapp

ENTRYPOINT [ "/app/binary" ]

HEALTHCHECK CMD curl -sf -o /dev/null http://localhost:8080/healthcheck || exit 1
