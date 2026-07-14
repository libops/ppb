FROM ghcr.io/libops/go:1.26.5@sha256:ea764e85e42a243217c621891123b3fda9374674c29d59785414fc6b15815b3d AS builder

SHELL ["/bin/ash", "-o", "pipefail", "-ex", "-c"]

WORKDIR /app

COPY go.* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY *.go ./
COPY pkg ./pkg

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /app/binary .

FROM ghcr.io/libops/base:3.2.2.0@sha256:0c6bab5444d4758bb977a42c1e5199cba733c7776e1aa006f8021108a785c547

COPY --from=builder /app/binary /app/binary

USER goapp

ENTRYPOINT [ "/app/binary" ]

HEALTHCHECK CMD curl -sf -o /dev/null http://localhost:8080/healthcheck || exit 1
