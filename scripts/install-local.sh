#!/bin/sh
# Builds rvis and installs it as a Helm plugin straight from this checkout,
# without GitHub or a network connection.
#
#   scripts/install-local.sh            build, stage and install
#   scripts/install-local.sh --package  also write dist/rvis_<version>_<os>_<arch>.tar.gz
#
# The plugin is copied (not symlinked) into $(helm env HELM_PLUGINS)/rvis, so
# it keeps working after `make clean` or moving the repository.
# GOOS/GOARCH may be set to package for another platform (implies no install).
set -eu

cd "$(dirname "$0")/.."
helm="${HELM_BIN:-helm}"
version="$(sed -n 's/^version: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' plugin.yaml)"
goos="${GOOS:-$(go env GOOS)}"
goarch="${GOARCH:-$(go env GOARCH)}"
package=false
[ "${1:-}" = "--package" ] && package=true

stage="dist/plugin/rvis"
rm -rf "$stage"
mkdir -p "$stage/bin" "$stage/scripts"

ext=""
[ "$goos" = "windows" ] && ext=".exe"
echo "rvis: building v${version} for ${goos}/${goarch}"
GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X github.com/mohammed-ehsan/rvis/cmd.Version=${version}" \
  -o "$stage/bin/rvis${ext}" .
cp plugin.yaml README.md LICENSE "$stage/"
# The install hook finds bin/rvis already present and does nothing.
cp scripts/install.sh "$stage/scripts/"

if $package; then
  archive="dist/rvis_${version}_${goos}_${goarch}.tar.gz"
  tar -czf "$archive" -C dist/plugin rvis
  echo "rvis: packaged ${archive}"
  echo "      install elsewhere with: tar -xzf $(basename "$archive") && helm plugin install ./rvis"
fi

if [ "$goos" != "$(go env GOHOSTOS)" ] || [ "$goarch" != "$(go env GOHOSTARCH)" ]; then
  echo "rvis: cross-compiled build, skipping local install"
  exit 0
fi

plugins="$("$helm" env HELM_PLUGINS)"
target="${plugins%%:*}/rvis"
if "$helm" plugin list 2>/dev/null | awk 'NR>1 {print $1}' | grep -qx rvis; then
  echo "rvis: replacing the installed plugin"
  "$helm" plugin uninstall rvis >/dev/null
fi
rm -rf "$target"
mkdir -p "$(dirname "$target")"
cp -R "$stage" "$target"
echo "rvis: installed v${version} to ${target}"
"$helm" rvis --rvis-version
