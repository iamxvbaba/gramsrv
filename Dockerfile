# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59
ARG ALPINE_IMAGE=alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build-base

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache ca-certificates git
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd/ ./cmd/
COPY deploy/ ./deploy/
COPY internal/ ./internal/

ENV CGO_ENABLED=0

FROM build-base AS build-server
ARG VCS_REF=unknown
ARG VCS_BRANCH=unknown
ARG VCS_TREE_STATE=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X main.gitCommit=${VCS_REF} -X main.gitBranch=${VCS_BRANCH} -X main.gitTreeState=${VCS_TREE_STATE} -X main.buildTime=${BUILD_DATE}" \
      -o /out/telesrv ./cmd/telesrv

FROM build-base AS build-admin
RUN apk add --no-cache nodejs npm
WORKDIR /src/cmd/telesrv-admin/web
RUN --mount=type=cache,target=/root/.npm npm ci && npm run build
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/telesrv-admin ./cmd/telesrv-admin

FROM ${ALPINE_IMAGE} AS runtime-base

ARG VCS_REF=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="gramsrv" \
      org.opencontainers.image.description="Telegram-like MTProto server" \
      org.opencontainers.image.source="https://github.com/iamxvbaba/gramsrv" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.created="${BUILD_DATE}"

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 telesrv \
    && adduser -S -D -H -u 10001 -G telesrv telesrv \
    && install -d -o telesrv -g telesrv -m 0750 /app /var/lib/telesrv

COPY --chmod=0555 deploy/docker/docker-entrypoint.sh /usr/local/bin/telesrv-container-entrypoint

WORKDIR /app
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/telesrv-container-entrypoint"]

FROM runtime-base AS server
USER root
RUN apk add --no-cache ffmpeg openssl \
    && install -d -o telesrv -g telesrv -m 0750 \
      /var/lib/telesrv/blobs \
      /var/lib/telesrv/blob-staging \
      /var/lib/telesrv/maptiles \
      /var/lib/telesrv/livestream
COPY --from=build-server /out/telesrv /usr/local/bin/telesrv
COPY --chown=telesrv:telesrv data/langpack/ /usr/share/telesrv/langpack/
COPY --chown=telesrv:telesrv data/sticker-seed/ /app/data/sticker-seed/
USER 10001:10001
EXPOSE 2398 2400 2401 2599 12399/udp 12400/udp
CMD ["telesrv"]

FROM server AS server-test
USER root
RUN install -d -o telesrv -g telesrv -m 0755 /usr/share/telesrv/keys
COPY --chown=telesrv:telesrv --chmod=0444 deploy/docker/assets/test-server-rsa.pub /usr/share/telesrv/keys/test-server-rsa.pub
COPY --chown=telesrv:telesrv --chmod=0444 deploy/docker/assets/test-server-rsa.pem.b64 /usr/share/telesrv/keys/test-server-rsa.pem.b64
USER 10001:10001

FROM runtime-base AS admin
COPY --from=build-admin /out/telesrv-admin /usr/local/bin/telesrv-admin
EXPOSE 2600
CMD ["telesrv-admin"]
