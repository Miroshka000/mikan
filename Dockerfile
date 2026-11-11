# syntax=docker/dockerfile:1.7
# The web bundle and the Go binaries are built on the build platform (Go cross-compiles
# for arm64 natively); only the small final stage runs under the target architecture.
# Base images are pinned by digest next to their tag, so a rebuild takes the same bytes;
# dependabot (.github/dependabot.yml) proposes the new digest when the tag moves.
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:a34ab6d7f6f4813f4d0c5469d74685604b962fbdbf5793a7bc7b4998ecb867c8 AS web
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27@sha256:01aa404fa56b604b6f48e90c1d6e2c79fe5cd78f9592f78d80303fbe171cdcc0 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
ARG VERSION=dev
# Filled in by BuildKit for each platform. A default here would win over it and put
# amd64 binaries into the arm64 image (the 0.4.1 image did).
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/mikan ./cmd/mikan && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/mikan-node ./cmd/mikan-node

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates tzdata libcap postgresql18-client && \
    addgroup -S -g 65532 mikan && adduser -S -D -H -u 65532 -G mikan mikan
COPY --from=build /out/ /usr/local/bin/
# The binaries must be the image's own architecture: the ELF machine field says so.
ARG TARGETARCH
RUN case "$TARGETARCH" in amd64) want="3e 00" ;; arm64) want="b7 00" ;; *) want="" ;; esac; \
    for f in /usr/local/bin/mikan /usr/local/bin/mikan-node; do \
      got=$(od -A n -t x1 -j 18 -N 2 "$f" | tr -s " " | sed "s/^ //"); \
      if [ -n "$want" ] && [ "$got" != "$want" ]; then echo "$f is not built for $TARGETARCH (ELF machine $got)"; exit 1; fi; \
    done
# Non-root processes may bind 443 (node) and 80 (panel, ACME challenges) only through
# file capabilities; compose keeps NET_BIND_SERVICE in the bounding set (S-04).
RUN setcap cap_net_bind_service=+ep /usr/local/bin/mikan-node && \
    setcap cap_net_bind_service=+ep /usr/local/bin/mikan && \
    mkdir -p /data /run/mikan && chown -R 65532:65532 /data /run/mikan && chmod 700 /data
USER 65532:65532
ENV MIKAN_DATA_DIR=/data MIKAN_NODE_SOCKET=/run/mikan/node.sock
ENTRYPOINT ["/usr/local/bin/mikan"]
CMD ["serve"]
