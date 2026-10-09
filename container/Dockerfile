# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26

# The build runs on the builder's own platform and cross-compiles, so a multi-arch build needs no emulation.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG GIT_HASH=none
ARG RELEASE_VERSION=dev
ARG BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -o /out/kubrix \
    -ldflags="-s -w -X main.version=${RELEASE_VERSION} -X main.commit=${GIT_HASH} -X main.date=${BUILD_TIME}" .

# git is needed to bootstrap from a release and to upgrade; ca-certificates for GitHub and the registries.
FROM alpine:3 AS production
ARG GIT_HASH=none
ARG RELEASE_VERSION=dev
ARG BUILD_TIME=unknown
LABEL org.opencontainers.image.title="kubriX-cli" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${RELEASE_VERSION}" \
      org.opencontainers.image.revision="${GIT_HASH}" \
      org.opencontainers.image.created="${BUILD_TIME}"

RUN apk add --no-cache git ca-certificates \
    && adduser -D -u 10001 kubrix

COPY --from=build /out/kubrix /usr/local/bin/kubrix
USER kubrix
WORKDIR /home/kubrix
ENTRYPOINT ["kubrix"]
