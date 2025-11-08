FROM golang:1.25-alpine3.22@sha256:d3f0cf7723f3429e3f9ed846243970b20a2de7bae6a5b66fc5914e228d831bbb

WORKDIR /app

COPY . ./

RUN go mod download \
    && go build -o /app/ppb  \
    && go clean -cache -modcache

ENTRYPOINT ["/app/ppb"]
