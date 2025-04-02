# Build the manager binary
FROM ghcr.io/cybozu/golang:1.24-noble AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

FROM scratch
WORKDIR /
COPY --from=builder /workspace/manager .
USER 10000:10000

ENTRYPOINT ["/manager"]
