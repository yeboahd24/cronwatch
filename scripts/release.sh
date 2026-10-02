#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
version=${1:?Usage: scripts/release.sh VERSION}
dist_dir=${CRONWATCH_DIST_DIR:-dist}
mkdir -p "$dist_dir"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
  target_os=${target%_*}
  target_arch=${target#*_}
  archive="cronwatch_${target}.tar.gz"
  CGO_ENABLED=0 GOOS=$target_os GOARCH=$target_arch go build -trimpath \
    -ldflags="-s -w -X github.com/yeboahd24/cronwatch/internal/app.Version=$version" \
    -o "$tmp_dir/cronwatch" ./cmd/cronwatch
  tar -czf "$dist_dir/$archive" -C "$tmp_dir" cronwatch
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$dist_dir" && sha256sum cronwatch_*.tar.gz > checksums.sha256)
else
  (cd "$dist_dir" && shasum -a 256 cronwatch_*.tar.gz > checksums.sha256)
fi

cp install.sh "$dist_dir/install.sh"
chmod 0755 "$dist_dir/install.sh"
echo "Release artifacts are in $dist_dir/"
