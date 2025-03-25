# Build the manager binary
FROM ghcr.io/cybozu/golang:1.24-noble AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY ./ .
# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Build nodeimageset-controller
FROM ghcr.io/cybozu/golang:1.24-noble AS builder-controller
ARG TARGETOS
ARG TARGETARCH
WORKDIR /workspace
COPY ./ .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o nodeimageset-controller cmd/nodeimageset-controller/main.go

FROM ghcr.io/cybozu/ubuntu:24.04
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=builder-controller /workspace/nodeimageset-controller .
USER 10000:10000

ENTRYPOINT ["/manager"]
