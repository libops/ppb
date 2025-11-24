FROM ghcr.io/libops/go1.25:main@sha256:f43c9b34f888d2ac53e87c8e061554f826b8eb580863d7b21fd787b6f0378f8f AS builder

SHELL ["/bin/ash", "-o", "pipefail", "-ex", "-c"]

WORKDIR /app

COPY go.* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY *.go ./
COPY pkg ./pkg

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /app/binary .

FROM ghcr.io/libops/go1.25:main@sha256:f43c9b34f888d2ac53e87c8e061554f826b8eb580863d7b21fd787b6f0378f8f

COPY --from=builder /app/binary /app/binary

