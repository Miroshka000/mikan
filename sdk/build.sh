#!/bin/sh
# Builds an SDK from the panel's OpenAPI spec (web/src/api/openapi.json) and checks it.
#
#   sh sdk/build.sh typescript [version]
#   sh sdk/build.sh python [version]
#
# The version is the panel release the SDK is built for, as its tag says it without the v
# (0.5.2.0). Without one the build is only a check, and says 0.0.0. The packages land in
# sdk/dist; nothing here publishes, release.yml does that from those files.
#
# It needs node (the spec step), and for typescript pnpm, for python python 3.11 or later.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(dirname "$here")
build="$here/.build"
dist="$here/dist"

target="${1:-}"
version="${2:-0.0.0}"

case "$target" in
  typescript | python) ;;
  *)
    echo "usage: sh sdk/build.sh typescript|python [version]" >&2
    exit 2
    ;;
esac
# A panel release: 0.5.2.0, or 0.4.5 on the older tags with three numbers.
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(\.[0-9]+)?$'; then
  echo "sdk: $version is not a release version (like 0.5.2.0)" >&2
  exit 2
fi

# The files of a package that a registry shows beside the code.
stage_docs() {
  cp "$1/README.md" "$root/LICENSE" "$root/NOTICE.md" "$2/"
}

build_typescript() {
  # npm versions have three numbers, a panel release four: the last two make npm's third
  # (0.5.2.1 is 0.5.201 there, 0.5.3.0 is 0.5.300), a plain version that orders as the
  # panel's releases do and that npm update follows. A hyphen would make every version a
  # pre-release to npm. The fourth number must stay under 100 for this.
  case "$version" in
    *.*.*.*)
      head="${version%.*}"
      patch="${head##*.}"
      rev="${version##*.}"
      if [ "$rev" -ge 100 ]; then
        echo "sdk: $version: npm takes the fourth number into the third, it must be under 100" >&2
        exit 1
      fi
      npm_version="${head%.*}.$((patch * 100 + rev))"
      ;;
    *)
      # Only the check build (0.0.0) has three numbers: a release with three would take the
      # npm version of the four-number one after it (0.5.2 and 0.5.2.0 both make 0.5.200),
      # and npm never takes a version twice.
      if [ "$version" != 0.0.0 ]; then
        echo "sdk: $version: npm takes releases with four numbers only (like 0.5.2.0)" >&2
        exit 1
      fi
      npm_version=0.0.0
      ;;
  esac

  cd "$here/typescript"
  pnpm install --frozen-lockfile
  pnpm run generate
  pnpm run build
  pnpm test

  stage="$build/typescript"
  rm -rf "$stage"
  mkdir -p "$stage"
  cp package.json "$stage/"
  cp -r dist "$stage/dist"
  stage_docs . "$stage"
  (
    cd "$stage"
    node -e '
      const { readFileSync, writeFileSync } = require("node:fs");
      const pkg = JSON.parse(readFileSync("package.json", "utf8"));
      pkg.version = process.argv[1];
      writeFileSync("package.json", JSON.stringify(pkg, null, 2) + "\n");
    ' "$npm_version"
    npm pack --pack-destination "$dist"
  )
}

build_python() {
  stage="$build/python"
  rm -rf "$stage" "$build/venv"
  mkdir -p "$stage"

  # The generator and the build tools, from the pinned file with its hashes.
  python3 -m venv "$build/venv"
  # shellcheck disable=SC1091 # made just above
  . "$build/venv/bin/activate"
  pip install --quiet --disable-pip-version-check --require-hashes -r "$here/python/requirements.txt"

  # A warning is a piece of the API the client would be missing: stop on it.
  openapi-python-client generate --path "$build/openapi.json" --config "$here/python/generator.yml" \
    --meta none --output-path "$stage/mikan_sdk" --fail-on-warning
  # The types are inline: this tells type checkers to read them (PEP 561).
  touch "$stage/mikan_sdk/py.typed"

  cp "$here/python/pyproject.toml" "$stage/"
  stage_docs "$here/python" "$stage"
  sed "s/^version = \"0.0.0\"\$/version = \"$version\"/" "$stage/pyproject.toml" > "$stage/pyproject.new"
  mv "$stage/pyproject.new" "$stage/pyproject.toml"
  grep -q "^version = \"$version\"\$" "$stage/pyproject.toml"

  python -m build --no-isolation --outdir "$dist" "$stage"

  # The wheel as a user gets it, not the files beside it: its dependencies are in the venv.
  pip install --quiet --disable-pip-version-check --no-deps --force-reinstall "$dist/mikan_sdk-$version-py3-none-any.whl"
  MIKAN_SDK_SPEC="$build/openapi.json" python -I -m unittest discover -s "$here/python/tests" -v
}

node "$here/spec.mjs" "$version" "$build/openapi.json"
mkdir -p "$dist"
case "$target" in
  typescript) build_typescript ;;
  python) build_python ;;
esac
