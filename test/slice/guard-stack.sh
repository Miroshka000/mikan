# Sourced by guard.sh and payment.sh: the stack of the panel beside its own node, on
# addresses the core takes for public (the guard profile of compose.yaml), and the driver's
# phases each script runs on it. KEEP=1 leaves the stack running.
export MSYS_NO_PATHCONV=1
export MIKAN_TEST_POSTGRES_PASSWORD=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
GUARD="docker compose --profile guard"
status=0

# guard_up builds the node's image and the services named, bootstraps the panel as the
# installer does and starts the stack with those services.
guard_up() {
  docker volume create mikan-gomod >/dev/null
  docker volume create mikan-gocache >/dev/null
  $GUARD down -v --remove-orphans >/dev/null 2>&1 || true
  $GUARD build node "$@"
  PW=$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 20)
  echo "$PW" | $GUARD run --rm -T guard-panel admin bootstrap \
    --public-host guard.test --port 2053 --admin-path slice-admin-path-0000 --sub-path slicesub0000 --username admin --password-stdin >/dev/null
  $GUARD up -d guard-node guard-panel guard-server guard-driver "$@"
}

# guard_phase runs a phase of the driver, unless one before it failed.
guard_phase() {
  [ "$status" = 0 ] || return 0
  $GUARD exec -T -e SLICE_PW="$PW" guard-driver go run ./test/slice/driver "$1" || status=$?
}

# guard_client starts the client on the config a phase wrote ("up -d") or restarts it on a
# new one ("restart"), unless a phase failed.
guard_client() {
  [ "$status" = 0 ] || return 0
  $GUARD "$@" guard-client
  sleep 3
}

# guard_down shows the logs of a failure, takes the stack down and exits with the status.
guard_down() {
  if [ "$status" != 0 ]; then
    $GUARD logs --tail 60 guard-panel guard-node guard-client
  fi
  if [ "${KEEP:-0}" != 1 ]; then
    $GUARD down -v --remove-orphans
  fi
  exit "$status"
}
