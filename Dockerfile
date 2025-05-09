# Build controller
FROM ghcr.io/cybozu/golang:1.24-noble AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY ./ .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o imageprefetch-controller cmd/imageprefetch-controller/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o nodeimageset-controller cmd/nodeimageset-controller/main.go

FROM ghcr.io/cybozu/ubuntu:24.04
WORKDIR /
COPY --from=builder /workspace/imageprefetch-controller .
COPY --from=builder /workspace/nodeimageset-controller .
USER 10000:10000
