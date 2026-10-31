#!/bin/sh
# Installs the rvis binary into $HELM_PLUGIN_DIR/bin.
# Builds from source when Go is available, otherwise downloads a release.
set -eu

cd "${HELM_PLUGIN_DIR:-$(dirname "$0")/..}"
version="$(sed -n 's/^version: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' plugin.yaml)"
repo="mohammed-ehsan/rvis"

if [ -x bin/rvis ] && [ "${1:-}" != "--force" ]; then
  echo "rvis: bin/rvis already present"
  exit 0
fi
mkdir -p bin

if command -v go >/dev/null 2>&1 && [ -f go.mod ]; then
  echo "rvis: building v${version} from source with $(go version | cut -d' ' -f3)"
  go build -trimpath -ldflags "-s -w -X github.com/mohammed-ehsan/rvis/cmd.Version=${version}" -o bin/rvis .
  exit 0
fi

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "rvis: unsupported architecture $arch" >&2; exit 1 ;;
esac
url="https://github.com/${repo}/releases/download/v${version}/rvis_${version}_${os}_${arch}.tar.gz"
echo "rvis: downloading ${url}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$url" -o "$tmp/rvis.tgz"
else
  wget -qO "$tmp/rvis.tgz" "$url"
fi
tar -xzf "$tmp/rvis.tgz" -C "$tmp"
mv "$tmp/rvis" bin/rvis
chmod +x bin/rvis
echo "rvis: installed bin/rvis"
