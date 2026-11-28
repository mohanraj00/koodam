# Build the manager binary
# Override BASE_IMAGE to build from another registry, e.g. docker.io/library/golang:1.26
ARG BASE_IMAGE=golang:1.26
FROM ${BASE_IMAGE} AS source
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# The controller image builds all the binaries except spend-api.
FROM source AS builder
ARG TARGETOS
ARG TARGETARCH
# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o harness-install ./cmd/harness-install
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o egress-proxy ./cmd/egress-proxy
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o agent-admission ./cmd/agent-admission
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o output-digest ./cmd/output-digest
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o app-claim-check ./cmd/app-claim-check
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o conversion ./cmd/conversion

# The spend-api image (#406) has the spend-api binary only. The binary embeds
# docs/spend-api.openapi.json, internal/opspage/static/ and internal/opspage/mugham.pin.json.
# .dockerignore keeps these files in the build context, so the build fails if one is missing.
FROM source AS spend-api-builder
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o spend-api ./cmd/spend-api

# Build this image with --target spend-api.
FROM gcr.io/distroless/static:nonroot AS spend-api
WORKDIR /
COPY --from=spend-api-builder /workspace/spend-api .
USER 65532:65532

ENTRYPOINT ["/spend-api"]

# The controller image is the last stage, so a build with no --target gives this image.
# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/harness-install .
COPY --from=builder /workspace/egress-proxy .
COPY --from=builder /workspace/agent-admission .
COPY --from=builder /workspace/output-digest .
COPY --from=builder /workspace/app-claim-check .
COPY --from=builder /workspace/conversion .
USER 65532:65532

ENTRYPOINT ["/manager"]
