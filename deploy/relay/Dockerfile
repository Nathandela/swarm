# syntax=docker/dockerfile:1
#
# swarm-relay OCI image (deploy/relay bundle, playbook 6.5). Multi-stage:
# a Go builder produces a static CGO_ENABLED=0 binary, and the final stage ships
# nothing else -- no shell, no package manager, no libc for a compromise to pivot on.
#
# BUILD FROM THE REPO ROOT (this Dockerfile needs go.mod and every package under
# internal/ and cmd/ in its context), pointing -f at this file:
#
#   docker build -f deploy/relay/Dockerfile -t swarm-relay:v0.13.25 .
#
# Multi-arch release build (documented here; NOT run automatically by anything in this
# repo -- docker buildx may not be installed in every environment that builds this
# image, and CI wires its own invocation):
#
#   docker buildx build \
#     --platform linux/amd64,linux/arm64 \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     --build-arg COMMIT=$(git rev-parse HEAD) \
#     --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -f deploy/relay/Dockerfile \
#     -t ghcr.io/nathandela/swarm-relay:v0.13.25 \
#     --push .
#
# The three --build-arg values feed the exact same internal/version.{Version,Commit,Date}
# ldflags stamps .goreleaser.yaml applies to every other release binary (see its
# swarm-relay build block) -- `swarm-relay version`-shaped output stays consistent whether
# the binary came from goreleaser or this image.

ARG GO_VERSION=1.25-bookworm

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS builder
WORKDIR /src

# Cache module downloads independent of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=
ARG DATE=

# CGO_ENABLED=0 + pure Go already guarantees a statically linked binary with zero
# third-party runtime dependencies (the same property .goreleaser.yaml documents for
# this exact binary) -- exactly what the scratch-adjacent distroless final stage below
# needs. GOOS/GOARCH come from buildx's own TARGETOS/TARGETARCH (set per --platform
# entry; a plain `docker build` with no buildx leaves them at the daemon's native
# platform). -s -w strips debug info.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags "-s -w \
      -X github.com/Nathandela/swarm/internal/version.Version=${VERSION} \
      -X github.com/Nathandela/swarm/internal/version.Commit=${COMMIT} \
      -X github.com/Nathandela/swarm/internal/version.Date=${DATE}" \
    -o /out/swarm-relay ./cmd/swarm-relay

FROM gcr.io/distroless/static-debian12:nonroot AS final

# OCI labels (playbook 6.5).
LABEL org.opencontainers.image.title="swarm-relay" \
      org.opencontainers.image.description="Untrusted rendezvous/mailbox/push relay for Swarm remote control (self-hosted)" \
      org.opencontainers.image.source="https://github.com/Nathandela/swarm" \
      org.opencontainers.image.documentation="https://github.com/Nathandela/swarm/blob/main/docs/operations/relay-vps-deploy.md" \
      org.opencontainers.image.licenses="MIT"

# distroless/static:nonroot already runs as uid/gid 65532 (nonroot:nonroot) and ships no
# shell, no package manager, and no libc; USER below is redundant with the base image's
# own default and kept for the reader who is skimming this Dockerfile without knowing
# that.
COPY --from=builder /out/swarm-relay /swarm-relay

# The relay's public websocket listener ("listen" in relay.config.example) and its
# loopback-only admin health/readiness surface ("admin_listen") are both config-file
# choices, not baked into this image -- EXPOSE documents the port a Compose/Kubernetes
# network is expected to reach, it does not itself open anything.
EXPOSE 9440

USER nonroot:nonroot
ENTRYPOINT ["/swarm-relay"]
CMD ["--config", "/etc/swarm-relay/relay.config"]
