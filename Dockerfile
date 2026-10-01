FROM ghcr.io/libops/go:1.26.6@sha256:a9928eb225e42659e251261cb603e103f67053a1feab3f76a6383affc40fda35 AS builder

SHELL ["/bin/ash", "-o", "pipefail", "-ex", "-c"]

WORKDIR /app

COPY go.* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY *.go ./
COPY pkg ./pkg

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /app/binary .

FROM ghcr.io/libops/base:3.2.2.0@sha256:dcbc45a6f61b41d6e849c8920ebca58f26cd65414b3efafbaabc5d04fa95af9d

COPY --from=builder /app/binary /app/binary

USER goapp

ENTRYPOINT [ "/app/binary" ]

HEALTHCHECK CMD curl -sf -o /dev/null http://localhost:8080/healthcheck || exit 1
