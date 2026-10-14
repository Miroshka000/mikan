#!/bin/sh
# Tests of the one-line install (install.sh): the release index, and the latest release
# when the index cannot be had or believed. Run ONLY inside a disposable Linux container
# or CI runner: it writes /usr/local/bin/mikan. curl is replaced by a stand-in that serves
# files from a directory, and the script's release key by a key made here.
set -eu
[ "${MIKAN_INSTALLER_TEST:-}" = 1 ] || { echo 'Set MIKAN_INSTALLER_TEST=1 inside a disposable container' >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo 'Run as root: install.sh installs to /usr/local/bin' >&2; exit 1; }
[ ! -e /usr/local/bin/mikan ] || { echo '/usr/local/bin/mikan already exists; refusing to touch it' >&2; exit 1; }
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp" /usr/local/bin/mikan' EXIT INT TERM
mkdir -p "$tmp/bin" "$tmp/www"
export TEST_STATE="$tmp"
export PATH="$tmp/bin:$PATH"
case "$(uname -m)" in
  x86_64 | amd64) arch=x86_64 ;;
  aarch64 | arm64) arch=aarch64 ;;
esac

# The stand-in: https://HOST/PATH is $tmp/www/HOST/PATH, a missing file is an HTTP error.
cat >"$tmp/bin/curl" <<'CURL'
#!/bin/sh
out='' url=''
while [ $# -gt 0 ]; do
  case "$1" in
    --help) exit 0 ;;
    -o) out=$2; shift ;;
    https://*) url=$1 ;;
  esac
  shift
done
printf '%s\n' "$url" >>"$TEST_STATE/urls"
file="$TEST_STATE/www/${url#https://}"
[ -f "$file" ] || exit 22
cp "$file" "$out"
CURL
chmod +x "$tmp/bin/curl"

# The release key of this test, in the script instead of the real one.
openssl genpkey -algorithm ed25519 -out "$tmp/key.pem" 2>/dev/null
pub=$(openssl pkey -in "$tmp/key.pem" -pubout | sed -n 2p)
sed "s|^MCowBQYDK2VwAyEAZ3wSIPBSaJxh5CsGO8eINI0aM0kyrQ46EcJSNeH85W8=\$|$pub|" "$here/install.sh" >"$tmp/install.sh"
grep -q "^$pub\$" "$tmp/install.sh"

releases="$tmp/www/github.com/o0hhs/mikanaddons/releases"
sign() { openssl pkeyutl -sign -inkey "$tmp/key.pem" -rawin -in "$1" | base64 | tr -d '\n' >"$1.sig"; }
# release VERSION [MANIFEST-VERSION]: the installer and its signed manifest under vVERSION.
release() {
  d="$releases/download/v$1"
  mkdir -p "$d"
  printf '#!/bin/sh\necho "mikan %s $*" >"$TEST_STATE/ran"\n' "$1" >"$d/mikan-$arch"
  sum=$(sha256sum "$d/mikan-$arch" | cut -d' ' -f1)
  printf '{\n  "version": "%s",\n  "installer": {\n    "%s": {\n      "url": "https://github.com/o0hhs/mikanaddons/releases/download/v%s/mikan-%s",\n      "sha256": "%s"\n    }\n  }\n}\n' \
    "${2:-$1}" "$arch" "$1" "$arch" "$sum" >"$d/manifest.json"
  sign "$d/manifest.json"
}
latest() {
  mkdir -p "$releases/latest/download"
  cp "$releases/download/v$1/manifest.json" "$releases/download/v$1/manifest.json.sig" "$releases/latest/download/"
}
index() {
  mkdir -p "$releases/download/updates"
  printf '%s\n' "$1" >"$releases/download/updates/index.json"
  sign "$releases/download/updates/index.json"
}

# run WANT: install.sh installs and starts mikan WANT.
run() {
  rm -f /usr/local/bin/mikan "$tmp/ran" "$tmp/urls"
  if ! sh "$tmp/install.sh" --join KEY >"$tmp/output" 2>&1; then
    cat "$tmp/output" >&2
    echo "install.sh failed, want mikan $1" >&2
    exit 1
  fi
  [ "$(cat "$tmp/ran")" = "mikan $1 install --join KEY" ] || { cat "$tmp/output" >&2; echo "ran $(cat "$tmp/ran"), want mikan $1" >&2; exit 1; }
}

for v in 0.4.5 0.4.9 0.4.10 0.5.0.0 0.5.0.1 0.5.0.9 0.5.0.10 0.5.0.2-rc.1; do release "$v"; done
latest 0.5.0.0
entry() { printf '{"version":"%s","channel":"%s","from":"0.4.5","manifest":"https://x/m.json"}' "$1" "$2"; }

# The newest stable release of the index; pre-releases and what it cannot read are skipped.
index "{\"schema\":1,\"x\":{\"y\":1},\"releases\":[$(entry 0.4.5 stable),$(entry 0.5.0.1 stable),$(entry 0.5.0.2-rc.1 beta),$(entry 0.9.0 nightly),$(entry 0.9.1-rc.1 stable),{\"version\":5}]}"
run 0.5.0.1
grep -q 'the newest stable release' "$tmp/output"
if grep -q 'latest/download' "$tmp/urls"; then echo 'the latest release was read' >&2; exit 1; fi
# Versions are numbers, part by part.
index "{\"releases\":[$(entry 0.4.9 stable),$(entry 0.4.10 stable)]}"
run 0.4.10
index "{\"releases\":[$(entry 0.5.0.9 stable),$(entry 0.5.0.10 stable),$(entry 0.4.10 stable)]}"
run 0.5.0.10
index "$(printf '{\n  "releases": [\n    %s,\n    %s\n  ]\n}' "$(entry 0.5.0.1 stable)" "$(entry 0.5.0.0 stable)")"
run 0.5.0.1

# Whatever is wrong with the index, the latest release.
index "{\"releases\":[$(entry 0.5.0.1 stable)]}"
openssl genpkey -algorithm ed25519 -out "$tmp/other.pem" 2>/dev/null
openssl pkeyutl -sign -inkey "$tmp/other.pem" -rawin -in "$releases/download/updates/index.json" | base64 | tr -d '\n' >"$releases/download/updates/index.json.sig"
run 0.5.0.0
grep -q 'the release index is unavailable' "$tmp/output"
index "{\"releases\":[$(entry 0.5.0.2-rc.1 beta)]}"
run 0.5.0.0
grep -q 'lists no stable release' "$tmp/output"
index "{\"releases\":[$(entry 0.6.0 stable)]}"
run 0.5.0.0
grep -q 'the manifest of mikan 0.6.0 is unavailable' "$tmp/output"
release 0.6.0 0.6.1
run 0.5.0.0
grep -q 'the manifest of mikan 0.6.0 is unavailable' "$tmp/output"
rm -rf "$releases/download/updates"
run 0.5.0.0
grep -q 'the release index is unavailable' "$tmp/output"

# The latest release is checked as before: a bad signature installs nothing.
latest 0.4.5
cp "$releases/download/v0.5.0.0/manifest.json.sig" "$releases/latest/download/manifest.json.sig"
rm -f /usr/local/bin/mikan "$tmp/ran"
if sh "$tmp/install.sh" >"$tmp/output" 2>&1; then echo 'a bad signature must fail' >&2; exit 1; fi
grep -q 'nothing is installed' "$tmp/output"
[ ! -e /usr/local/bin/mikan ] && [ ! -e "$tmp/ran" ]
echo 'install.sh: ok'
