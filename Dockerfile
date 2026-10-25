# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.26-alpine
ARG ALPINE_IMAGE=alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build-base

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates git
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ ./cmd/
COPY deploy/ ./deploy/
COPY internal/ ./internal/

ENV CGO_ENABLED=0

FROM build-base AS build-healthcheck
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/telesrv-healthcheck ./cmd/telesrv-healthcheck

FROM build-base AS build-migrate
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/telesrv-migrate ./cmd/telesrv-migrate

FROM build-base AS build-edge
ARG VCS_REF=unknown
ARG VCS_BRANCH=unknown
ARG VCS_TREE_STATE=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X main.gitCommit=${VCS_REF} -X main.gitBranch=${VCS_BRANCH} -X main.gitTreeState=${VCS_TREE_STATE} -X main.buildTime=${BUILD_DATE}" \
      -o /out/telesrv-edge ./cmd/telesrv-edge

FROM build-base AS build-core
ARG VCS_REF=unknown
ARG VCS_BRANCH=unknown
ARG VCS_TREE_STATE=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X main.gitCommit=${VCS_REF} -X main.gitBranch=${VCS_BRANCH} -X main.gitTreeState=${VCS_TREE_STATE} -X main.buildTime=${BUILD_DATE}" \
      -o /out/telesrv-core ./cmd/telesrv-core

FROM build-base AS build-egress
ARG VCS_REF=unknown
ARG VCS_BRANCH=unknown
ARG VCS_TREE_STATE=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X main.gitCommit=${VCS_REF} -X main.gitBranch=${VCS_BRANCH} -X main.gitTreeState=${VCS_TREE_STATE} -X main.buildTime=${BUILD_DATE}" \
      -o /out/telesrv-egress ./cmd/telesrv-egress

FROM build-base AS build-file
ARG VCS_REF=unknown
ARG VCS_BRANCH=unknown
ARG VCS_TREE_STATE=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X main.gitCommit=${VCS_REF} -X main.gitBranch=${VCS_BRANCH} -X main.gitTreeState=${VCS_TREE_STATE} -X main.buildTime=${BUILD_DATE}" \
      -o /out/telesrv-file ./cmd/telesrv-file

FROM build-base AS build-sfu
ARG VCS_REF=unknown
ARG VCS_BRANCH=unknown
ARG VCS_TREE_STATE=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X main.gitCommit=${VCS_REF} -X main.gitBranch=${VCS_BRANCH} -X main.gitTreeState=${VCS_TREE_STATE} -X main.buildTime=${BUILD_DATE}" \
      -o /out/telesrv-sfu ./cmd/telesrv-sfu

FROM build-base AS build-admin
RUN apk add --no-cache nodejs npm
WORKDIR /src/cmd/telesrv-admin/web
RUN --mount=type=cache,target=/root/.npm \
    npm ci && npm run build
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/telesrv-admin ./cmd/telesrv-admin

FROM ${ALPINE_IMAGE} AS langpack-bundle
WORKDIR /usr/share/telesrv/langpack
COPY data/langpack/ ./
RUN set -eu; \
    find . -type f -name '*.strings' -print | LC_ALL=C sort > /tmp/langpack-files; \
    test -s /tmp/langpack-files; \
    while IFS= read -r file; do sha256sum "$file"; done < /tmp/langpack-files \
      | sha256sum | cut -d ' ' -f 1 > .seed-fingerprint; \
    grep -Eq '^[0-9a-f]{64}$' .seed-fingerprint

FROM ${ALPINE_IMAGE} AS runtime-base

ARG VCS_REF=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="telesrv" \
      org.opencontainers.image.description="Telegram-like MTProto server" \
      org.opencontainers.image.source="https://github.com/iamxvbaba/gramsrv" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.created="${BUILD_DATE}"

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 telesrv \
    && adduser -S -D -H -u 10001 -G telesrv telesrv \
    && install -d -o telesrv -g telesrv -m 0750 /app /etc/telesrv /var/lib/telesrv /var/lib/telesrv-identity

COPY --from=build-healthcheck /out/telesrv-healthcheck /usr/local/bin/telesrv-healthcheck
COPY --chmod=0555 deploy/docker/docker-entrypoint.sh /usr/local/bin/telesrv-container-entrypoint

WORKDIR /app
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/telesrv-container-entrypoint"]

FROM runtime-base AS runtime-media
USER root
RUN apk add --no-cache ffmpeg
USER 10001:10001

FROM runtime-base AS migrate
COPY --from=build-migrate /out/telesrv-migrate /usr/local/bin/telesrv-migrate
CMD ["telesrv-migrate"]

FROM runtime-media AS file
USER root
RUN install -d -o telesrv -g telesrv -m 0750 \
      /var/lib/telesrv-file \
      /var/lib/telesrv-file/blobs \
      /var/lib/telesrv-file/blob-staging \
      /var/lib/telesrv-file/maptiles \
      /var/tmp/telesrv-file
COPY --from=build-file /out/telesrv-file /usr/local/bin/telesrv-file
COPY --chown=telesrv:telesrv deploy/docker/config/file.yaml /etc/telesrv/file.yaml
USER 10001:10001
EXPOSE 2520
CMD ["telesrv-file", "--config", "/etc/telesrv/file.yaml"]

FROM runtime-media AS core
USER root
RUN install -d -o telesrv -g telesrv -m 0750 \
      /var/lib/telesrv-core \
      /var/lib/telesrv-core/maptiles \
      /var/lib/telesrv-core/telegram-login \
      /var/tmp/telesrv-core
COPY --from=build-core /out/telesrv-core /usr/local/bin/telesrv-core
COPY --from=langpack-bundle --chown=telesrv:telesrv /usr/share/telesrv/langpack/ /usr/share/telesrv/langpack/
COPY --chown=telesrv:telesrv deploy/docker/assets/seed-manifest.json /usr/share/telesrv/seed-manifest.json
COPY --chown=telesrv:telesrv deploy/docker/config/core.yaml /etc/telesrv/core.yaml
USER 10001:10001
EXPOSE 2400 2401 2420 2440
CMD ["telesrv-core", "--config", "/etc/telesrv/core.yaml"]

FROM runtime-base AS egress
COPY --from=build-egress /out/telesrv-egress /usr/local/bin/telesrv-egress
COPY --chown=telesrv:telesrv deploy/docker/config/egress.yaml /etc/telesrv/egress.yaml
EXPOSE 2510
CMD ["telesrv-egress", "--config", "/etc/telesrv/egress.yaml"]

FROM runtime-base AS sfu
COPY --from=build-sfu /out/telesrv-sfu /usr/local/bin/telesrv-sfu
COPY --chown=telesrv:telesrv deploy/docker/config/sfu.yaml /etc/telesrv/sfu.yaml
# Host-network SFU binds relay ports directly; the bridge fallback publishes a
# bounded configurable 1:1 range.
EXPOSE 2450 12399/udp 12400/udp
CMD ["telesrv-sfu", "--config", "/etc/telesrv/sfu.yaml"]

FROM runtime-base AS admin
COPY --from=build-admin /out/telesrv-admin /usr/local/bin/telesrv-admin
COPY --chown=telesrv:telesrv deploy/docker/config/admin.yaml /etc/telesrv/admin.yaml
EXPOSE 2600
CMD ["telesrv-admin", "--config", "/etc/telesrv/admin.yaml"]

FROM runtime-base AS edge
USER root
RUN apk add --no-cache openssl \
    && install -d -o telesrv -g telesrv -m 0700 /var/lib/telesrv-edge
COPY --from=build-edge /out/telesrv-edge /usr/local/bin/telesrv-edge
COPY --chown=telesrv:telesrv deploy/docker/config/edge.yaml /etc/telesrv/edge.yaml
USER 10001:10001
EXPOSE 2398
CMD ["telesrv-edge", "--config", "/etc/telesrv/edge.yaml"]

# Public v2 test image. The embedded pair is deliberately non-secret and makes
# patched client builds and fresh one-click deployments share one fingerprint.
# Build target "edge" above remains free of the published test private key.
FROM edge AS edge-test
USER root
RUN install -d -o telesrv -g telesrv -m 0755 /usr/share/telesrv/keys
COPY --chown=telesrv:telesrv --chmod=0444 deploy/docker/assets/test-server-rsa.pub /usr/share/telesrv/keys/test-server-rsa.pub
COPY --chown=telesrv:telesrv --chmod=0444 deploy/docker/assets/test-server-rsa.pem.b64 /usr/share/telesrv/keys/test-server-rsa.pem.b64
USER 10001:10001
