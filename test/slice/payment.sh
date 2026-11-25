#!/usr/bin/env sh
# Payment access after expiry on the stack of guard.sh: the panel beside its own node, on
# addresses the core takes for public. An expired subscription gets the renewal profile; a
# real client on it reaches the subscription page and the listed site and nothing else,
# within the renewal quota. After the renewal the regular profile is back and the renewal
# key is gone. The payment is the admin's extension: a provider's checkout needs its account.
# KEEP=1 leaves the stack running.
set -eu
cd "$(dirname "$0")"
. ./guard-stack.sh

guard_up pay-site pay-other
guard_phase payment-prepare
guard_client up -d
guard_phase payment
# The client restarts on the key of the new window.
guard_client restart
guard_phase payment-renewed
guard_down
