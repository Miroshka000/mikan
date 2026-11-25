#!/usr/bin/env sh
# The core's egress guard on a stack of its own: the panel beside its own node as the installer
# runs them, on addresses the core takes for public, without MIKAN_ALLOW_PRIVATE. A client goes
# through the node to the panel, the subscription and the server's other ports. run.sh runs
# this after the slice, then payment.sh on the same stack; on its own it takes a minute or
# two. KEEP=1 leaves the stack running.
set -eu
cd "$(dirname "$0")"
. ./guard-stack.sh

guard_up
guard_phase guard-prepare
guard_client up -d
guard_phase guard
guard_down
