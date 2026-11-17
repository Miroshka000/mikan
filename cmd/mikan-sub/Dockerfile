# Build from the repository root: docker build -f cmd/mikan-sub/Dockerfile -t mikan-sub .
FROM golang:1.27@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/mikan-sub/ ./cmd/mikan-sub/
COPY internal/subrelay/ ./internal/subrelay/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /mikan-sub ./cmd/mikan-sub

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates libcap && \
    addgroup -S -g 65532 mikan && adduser -S -D -H -u 65532 -G mikan mikan && \
    mkdir -p /data/acme && chown -R 65532:65532 /data && chmod 700 /data/acme
COPY --chmod=755 --from=build /mikan-sub /usr/local/bin/mikan-sub
RUN setcap cap_net_bind_service=+ep /usr/local/bin/mikan-sub
USER 65532:65532
EXPOSE 80 443
ENTRYPOINT ["/usr/local/bin/mikan-sub"]
