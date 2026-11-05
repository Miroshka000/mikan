#!/usr/bin/env sh
# The core's egress guard on a stack of its own: the panel beside its own node as the installer
# runs them, on addresses the core takes for public, without MIKAN_ALLOW_PRIVATE. A client goes
# through the node to the panel, the subscription and the server's other ports. run.sh runs
# this last; on its own it takes a minute or two. KEEP=1 leaves the stack running.
set -eu
cd "$(dirname "$0")"
export MSYS_NO_PATHCONV=1
export MIKAN_TEST_POSTGRES_PASSWORD=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')

docker volume create mikan-gomod >/dev/null
docker volume create mikan-gocache >/dev/null
docker compose --profile guard down -v --remove-orphans >/dev/null 2>&1 || true
docker compose build node

PW=$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 20)
echo "$PW" | docker compose --profile guard run --rm -T guard-panel admin bootstrap \
  --public-host guard.test --port 2053 --admin-path slice-admin-path-0000 --sub-path slicesub0000 --username admin --password-stdin >/dev/null
docker compose --profile guard up -d guard-node guard-panel guard-server guard-driver

status=0
docker compose --profile guard exec -T -e SLICE_PW="$PW" guard-driver go run ./test/slice/driver guard-prepare || status=$?
if [ "$status" = 0 ]; then
  docker compose --profile guard up -d guard-client
  sleep 3
  docker compose --profile guard exec -T -e SLICE_PW="$PW" guard-driver go run ./test/slice/driver guard || status=$?
fi
if [ "$status" != 0 ]; then
  docker compose --profile guard logs --tail 60 guard-panel guard-node guard-client
fi
if [ "${KEEP:-0}" != 1 ]; then
  docker compose --profile guard down -v --remove-orphans
fi
exit "$status"
