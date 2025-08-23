FROM golang:1.24-alpine3.21@sha256:ba0e119509b381457e9bb434319f2ef7996ad7ca7ec11a66e4ecdd8da49b8ddf

WORKDIR /app

COPY . ./

RUN go mod download \
    && go build -o /app/ppb  \
    && go clean -cache -modcache

ENTRYPOINT ["/app/ppb"]
