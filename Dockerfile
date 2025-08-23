FROM golang:1.25-alpine3.22@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440

WORKDIR /app

COPY . ./

RUN go mod download \
    && go build -o /app/ppb  \
    && go clean -cache -modcache

ENTRYPOINT ["/app/ppb"]
