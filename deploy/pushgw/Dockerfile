# syntax=docker/dockerfile:1

ARG GO_VERSION=1.25-bookworm
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=
ARG DATE=
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags "-s -w \
      -X github.com/Nathandela/swarm/internal/version.Version=${VERSION} \
      -X github.com/Nathandela/swarm/internal/version.Commit=${COMMIT} \
      -X github.com/Nathandela/swarm/internal/version.Date=${DATE}" \
    -o /out/swarm-pushgw ./cmd/swarm-pushgw

FROM gcr.io/distroless/static-debian12:nonroot AS final

LABEL org.opencontainers.image.title="swarm-pushgw" \
      org.opencontainers.image.description="Swarm public FCM push gateway" \
      org.opencontainers.image.source="https://github.com/Nathandela/swarm" \
      org.opencontainers.image.documentation="https://github.com/Nathandela/swarm/blob/main/docs/operations/push-gateway-deploy.md" \
      org.opencontainers.image.licenses="MIT"

COPY --from=builder /out/swarm-pushgw /swarm-pushgw
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/swarm-pushgw"]
