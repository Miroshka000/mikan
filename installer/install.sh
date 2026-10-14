#!/bin/sh
# mikan: one-line install.
#
#   curl -fsSL https://github.com/o0hhs/mikanaddons/releases/latest/download/install.sh | sudo bash
#
# Finds the newest stable release in the signed release index (an asset of the "updates"
# pre-release, which does not depend on what GitHub calls the latest release), or takes
# the latest release when the index cannot be had or believed. Downloads that release's
# manifest and its signature, checks the signature against the
# release key built into this script, then downloads the installer for this server's
# architecture from the address the manifest names and checks its sha256 against the
# manifest, and starts it with the arguments given after "-s --":
# "... | sudo bash -s -- --join KEY" installs a node of an existing panel.
#
# What the check proves: the manifest and the binary come from whoever holds the release
# key, even if they were served by a mirror, a cache or a stolen GitHub account. What it
# cannot prove: this script is as trustworthy as the place it came from (it carries the
# key), so the first install rests on TLS and on GitHub for the script itself. Every update
# after that is checked by the installer against the key it carries.
set -eu

REPO="o0hhs/mikanaddons"
BASE="https://github.com/$REPO/releases/latest/download"
INDEX="https://github.com/$REPO/releases/download/updates/index.json"

# The public half of the key that signs every release's manifest.json: the same key as
# internal/release.PublicKey (installer/src/release.rs checks that this text matches it).
PUBKEY='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAH+GnL17JW6e7qw4qsYOC2YNMFPLp1mTjJmUqops3z7o=
-----END PUBLIC KEY-----'

fail() {
  echo "mikan: $*" >&2
  exit 1
}

[ "$(id -u)" = 0 ] || fail "run as root: curl -fsSL $BASE/install.sh | sudo bash"
case "$(uname -m)" in
  x86_64 | amd64) arch=x86_64 ;;
  aarch64 | arm64) arch=aarch64 ;;
  *) fail "unsupported architecture $(uname -m): mikan runs on x86_64 and aarch64" ;;
esac
command -v curl >/dev/null || fail "curl is missing"
command -v sha256sum >/dev/null || fail "sha256sum is missing"
command -v base64 >/dev/null || fail "base64 is missing"

# The signature is Ed25519: openssl 3.0 or newer checks it (Debian 12, Ubuntu 22.04 and
# 24.04, Alpine 3.17+ all have it). A fresh server may not have the command at all.
if ! command -v openssl >/dev/null 2>&1; then
  echo "mikan: openssl is needed to check the release's signature: installing it" >&2
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y openssl >&2 || true
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache openssl >&2 || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y openssl >&2 || true
  fi
fi
command -v openssl >/dev/null 2>&1 || fail "openssl is missing and could not be installed: install it and run again"
case "$(openssl version 2>/dev/null)" in
  "OpenSSL 3."* | "OpenSSL 4."*) ;;
  *) fail "this openssl ($(openssl version 2>/dev/null)) cannot check Ed25519 signatures: openssl 3.0 or newer is needed" ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
# A dead route must end in an error, not in silence: a connection that does not open in
# 15 s or a transfer slower than 1 KB/s for 30 s is retried, then given up. curl before
# 7.71 (Ubuntu 20.04) retries only some errors; newer ones retry every failure.
retry="--retry 3"
if { curl --help all || curl --help; } 2>/dev/null | grep -q -- '--retry-all-errors'; then
  retry="$retry --retry-all-errors"
fi
# get URL FILE WHAT [bar]: says what it downloads; "bar" shows curl's progress bar on a
# terminal (the installer is the only big file).
get() {
  echo "mikan: downloading $3: $1" >&2
  shown=-s
  if [ "${4:-}" = bar ] && [ -t 2 ]; then
    shown=--progress-bar
  fi
  # shellcheck disable=SC2086 # $retry is a list of flags
  curl -fSL $shown --proto '=https' --tlsv1.2 --connect-timeout 15 --speed-limit 1024 --speed-time 30 $retry "$1" -o "$2" || {
    code=$?
    case $code in
      22) hint="the address answered with an HTTP error" ;;
      *) hint="GitHub release downloads are unreachable from this server; check IPv6/proxy" ;;
    esac
    fail "cannot download $3 from $1 (curl exit code $code): $hint"
  }
}
# try URL FILE: a download whose failure is an answer, not the end.
try() {
  # shellcheck disable=SC2086 # $retry is a list of flags
  curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout 15 --speed-limit 1024 --speed-time 30 $retry "$1" -o "$2" 2>/dev/null
}
printf '%s\n' "$PUBKEY" >"$tmp/key.pem"
# signed FILE SIG: whether SIG is the release key's signature over FILE's exact bytes.
signed() {
  tr -d ' \n\r' <"$2" | base64 -d >"$tmp/check.bin" 2>/dev/null || return 1
  [ "$(wc -c <"$tmp/check.bin" | tr -d ' ')" = 64 ] || return 1
  openssl pkeyutl -verify -pubin -inkey "$tmp/key.pem" -rawin -in "$1" -sigfile "$tmp/check.bin" >/dev/null 2>&1
}
# The newest stable release the index lists (internal/release/index.go): entries are flat
# objects, so each is one line once split at "{"; one this script cannot read is skipped.
# Only "stable" release versions count, and the manifest comes from that version's own
# release in this project, whatever else the entry says.
newest_stable() {
  { tr -d ' \t\r\n' <"$1" | tr '{' '\n' && echo; } | while IFS= read -r e; do
    v=$(printf '%s' "$e" | sed -n 's/.*"version":"\([0-9.]*\)".*/\1/p')
    c=$(printf '%s' "$e" | sed -n 's/.*"channel":"\([a-z]*\)".*/\1/p')
    if [ "$c" = stable ] && printf '%s\n' "$v" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+(\.[0-9]+)?'; then
      printf '%s\n' "$v"
    fi
  done | sort -t. -k1,1n -k2,2n -k3,3n -k4,4n | tail -n 1
}

# The index first; whatever goes wrong with it, the latest release.
release=""
echo "mikan: reading the release index: $INDEX" >&2
if try "$INDEX" "$tmp/index.json" && try "$INDEX.sig" "$tmp/index.json.sig" && signed "$tmp/index.json" "$tmp/index.json.sig"; then
  newest=$(newest_stable "$tmp/index.json")
  at="https://github.com/$REPO/releases/download/v$newest/manifest.json"
  if [ -z "$newest" ]; then
    echo "mikan: the release index lists no stable release: using the latest release" >&2
  elif try "$at" "$tmp/manifest.json" && try "$at.sig" "$tmp/manifest.json.sig" && signed "$tmp/manifest.json" "$tmp/manifest.json.sig" &&
    tr -d '\n ' <"$tmp/manifest.json" | grep -qF "\"version\":\"$newest\""; then
    release=$newest
    echo "mikan: mikan $newest, the newest stable release" >&2
  else
    echo "mikan: the manifest of mikan $newest is unavailable: using the latest release" >&2
  fi
else
  echo "mikan: the release index is unavailable: using the latest release" >&2
fi
if [ -z "$release" ]; then
  get "$BASE/manifest.json" "$tmp/manifest.json" "the release manifest"
  get "$BASE/manifest.json.sig" "$tmp/manifest.json.sig" "the manifest's signature"
fi

# The signature first: nothing in the manifest is read before it is known to be the release's.
tr -d ' \n\r' <"$tmp/manifest.json.sig" | base64 -d >"$tmp/sig.bin" 2>/dev/null || fail "the manifest's signature is not base64"
[ "$(wc -c <"$tmp/sig.bin" | tr -d ' ')" = 64 ] || fail "the manifest's signature has the wrong length"
openssl pkeyutl -verify -pubin -inkey "$tmp/key.pem" -rawin -in "$tmp/manifest.json" -sigfile "$tmp/sig.bin" >/dev/null 2>&1 ||
  fail "the release manifest's signature does not match the release key: nothing is installed"

# The manifest lists "<arch>": { "url": …, "sha256": "…" } (no jq on a fresh server): the
# arch's object, then its two fields, in whatever order and with whatever else it holds.
entry=$(tr -d '\n ' <"$tmp/manifest.json" | sed -n "s/.*\"$arch\":{\([^}]*\)}.*/\1/p")
url=$(printf '%s' "$entry" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')
want=$(printf '%s' "$entry" | sed -n 's/.*"sha256":"\([0-9a-f]\{64\}\)".*/\1/p')
[ -n "$url" ] && [ -n "$want" ] || fail "the release has no installer for $arch"
# The binary comes from the release the manifest is of, not from whatever "latest" is now.
case "$url" in
  "https://github.com/$REPO/releases/download/v"*"/mikan-$arch") ;;
  *) fail "the manifest names an installer at an address outside this project's releases: $url" ;;
esac
get "$url" "$tmp/mikan" "the installer for $arch" bar
got=$(sha256sum "$tmp/mikan" | cut -d' ' -f1)
[ "$want" = "$got" ] || fail "the installer does not match the signed release manifest"

# Whole or not at all: the old command stays until the new one is on the disk.
install -m 755 "$tmp/mikan" /usr/local/bin/.mikan.new
mv -f /usr/local/bin/.mikan.new /usr/local/bin/mikan
# The installer is interactive; curl | bash leaves stdin on the script, so it reads the
# terminal. Without one (a script over ssh) it runs on its flags alone.
if [ -t 0 ] || ! (exec </dev/tty) 2>/dev/null; then
  exec /usr/local/bin/mikan install "$@"
fi
exec /usr/local/bin/mikan install "$@" </dev/tty
