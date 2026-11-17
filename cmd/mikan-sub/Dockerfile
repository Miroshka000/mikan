# Build from the repository root: docker build -f cmd/mikan-sub/Dockerfile -t mikan-sub .
FROM golang:1.27@sha256:01aa404fa56b604b6f48e90c1d6e2c79fe5cd78f9592f78d80303fbe171cdcc0 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/mikan-sub/ ./cmd/mikan-sub/
COPY internal/subrelay/ ./internal/subrelay/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /mikan-sub ./cmd/mikan-sub

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache ca-certificates libcap && \
    addgroup -S -g 65532 mikan && adduser -S -D -H -u 65532 -G mikan mikan && \
    mkdir -p /data/acme && chown -R 65532:65532 /data && chmod 700 /data/acme
COPY --chmod=755 --from=build /mikan-sub /usr/local/bin/mikan-sub
RUN setcap cap_net_bind_service=+ep /usr/local/bin/mikan-sub
USER 65532:65532
EXPOSE 80 443
ENTRYPOINT ["/usr/local/bin/mikan-sub"]
