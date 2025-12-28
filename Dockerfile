FROM ghcr.io/libops/base:main@sha256:1185f74227c1c935b811e24971cc1b2d5deb615b9028779387a575027ef84d9d AS builder

SHELL ["/bin/ash", "-o", "pipefail", "-ex", "-c"]

WORKDIR /app

COPY go.* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY *.go ./
COPY pkg ./pkg

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /app/binary .

FROM ghcr.io/libops/base:main@sha256:1185f74227c1c935b811e24971cc1b2d5deb615b9028779387a575027ef84d9d

COPY --from=builder /app/binary /app/binary

USER goapp

ENTRYPOINT [ "/app/binary" ]

HEALTHCHECK CMD curl -sf -o /dev/null http://localhost:8080/healthcheck || exit 1
