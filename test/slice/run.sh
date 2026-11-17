#!/usr/bin/env sh
# Vertical slice: real image, bootstrap, panel + node, a mihomo client configured
# from the panel's own subscription. KEEP=1 leaves the stack running.
set -eu
cd "$(dirname "$0")"
export MSYS_NO_PATHCONV=1
export MIKAN_TEST_POSTGRES_PASSWORD=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')

docker volume create mikan-gomod >/dev/null
docker volume create mikan-gocache >/dev/null
docker compose down -v --remove-orphans >/dev/null 2>&1 || true
docker compose build node target

PW=$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 20)
echo "$PW" | docker compose run --rm -T panel admin bootstrap \
  --public-host node.test --port 2053 --admin-path slice-admin-path-0000 --sub-path slicesub0000 --username admin --password-stdin >/dev/null
docker compose up -d node panel target driver

status=0
docker compose exec -T -e SLICE_PW="$PW" driver go run ./test/slice/driver nodes || status=$?
if [ "$status" = 0 ]; then
  docker compose --profile node2 up -d node2
  docker compose exec -T -e SLICE_PW="$PW" driver go run ./test/slice/driver prepare || status=$?
fi
if [ "$status" = 0 ]; then
  docker compose --profile client up -d client
  sleep 3
  docker compose exec -T -e SLICE_PW="$PW" driver go run ./test/slice/driver verify || status=$?
fi
# Pools: VLESS Vision counts to a small traffic pool of its own.
if [ "$status" = 0 ]; then
  docker compose exec -T -e SLICE_PW="$PW" driver go run ./test/slice/driver pools || status=$?
fi
# Cascade: the panel's node sends VLESS Vision out through node2.
if [ "$status" = 0 ]; then
  docker compose exec -T -e SLICE_PW="$PW" driver go run ./test/slice/driver cascade || status=$?
fi
# Devices: the client also gets a device's own keys, then the admin unbinds the device.
if [ "$status" = 0 ]; then
  docker compose exec -T -e SLICE_PW="$PW" driver go run ./test/slice/driver devices || status=$?
fi
if [ "$status" = 0 ]; then
  docker compose --profile client restart client
  sleep 3
  docker compose exec -T -e SLICE_PW="$PW" driver go run ./test/slice/driver devices-check || status=$?
fi
# Automatic moves: two clients on two networks lose the node's 443/tcp; the panel must
# move XHTTP.
if [ "$status" = 0 ]; then
  docker compose --profile client2 up -d client2
  docker compose --profile client --profile blocker run --rm blocker || status=$?
fi
if [ "$status" = 0 ]; then
  docker compose --profile client2 --profile blocker2 run --rm blocker2 || status=$?
fi
if [ "$status" = 0 ]; then
  docker compose exec -T -e SLICE_PW="$PW" driver go run ./test/slice/driver autotune || status=$?
fi
if [ "$status" = 0 ]; then
  # The client restarts on the new profile; its 443/tcp stays dropped.
  docker compose --profile client restart client
  docker compose --profile client --profile blocker run --rm blocker || status=$?
  sleep 2
fi
if [ "$status" = 0 ]; then
  docker compose exec -T -e SLICE_PW="$PW" driver go run ./test/slice/driver autotune-check || status=$?
fi
if [ "$status" != 0 ]; then
  docker compose --profile node2 logs --tail 60 panel node node2
  docker compose --profile client --profile client2 logs --tail 30 client client2
fi
if [ "${KEEP:-0}" != 1 ]; then
  docker compose --profile client --profile client2 --profile node2 --profile blocker --profile blocker2 down -v --remove-orphans
  # The core's egress guard, which MIKAN_ALLOW_PRIVATE keeps off on the stack above.
  if [ "$status" = 0 ]; then
    sh ./guard.sh || status=$?
  fi
fi
exit "$status"
