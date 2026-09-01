FROM ghcr.io/libops/go:1.26.6@sha256:132e5632829827a10da523e64a2adaed4c47362e60ac26f081181d7c7a279bb8 AS builder

SHELL ["/bin/ash", "-o", "pipefail", "-ex", "-c"]

WORKDIR /app

COPY go.* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY *.go ./
COPY pkg ./pkg

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /app/binary .

FROM ghcr.io/libops/base:3.2.2.0@sha256:57b396fd8b681d626c89359e3f68618d4822f46c9364854c9d4747c25ab9d705

COPY --from=builder /app/binary /app/binary

USER goapp

ENTRYPOINT [ "/app/binary" ]

HEALTHCHECK CMD curl -sf -o /dev/null http://localhost:8080/healthcheck || exit 1
