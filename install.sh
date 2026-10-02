#!/bin/sh
set -eu

base_url=${CRONWATCH_RELEASE_BASE_URL:-https://github.com/yeboahd24/cronwatch/releases/latest/download}
base_url=${base_url%/}

case "$(uname -s)" in
  Linux) target_os=linux ;;
  Darwin) target_os=darwin ;;
  *) echo 'CronWatch installer supports Linux and macOS.' >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) target_arch=amd64 ;;
  aarch64|arm64) target_arch=arm64 ;;
  *) echo 'Unsupported CPU architecture.' >&2; exit 1 ;;
esac

if ! command -v curl >/dev/null 2>&1; then
  echo 'curl is required.' >&2
  exit 1
fi
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  echo 'sha256sum or shasum is required.' >&2
  exit 1
fi

archive="cronwatch_${target_os}_${target_arch}.tar.gz"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
curl -fsSL "$base_url/$archive" -o "$tmp_dir/$archive"
curl -fsSL "$base_url/checksums.sha256" -o "$tmp_dir/checksums.sha256"
expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp_dir/checksums.sha256")
if [ -z "$expected" ]; then
  echo "Missing checksum for $archive." >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp_dir/$archive" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$tmp_dir/$archive" | awk '{ print $1 }')
fi
if [ "$actual" != "$expected" ]; then
  echo "Checksum mismatch for $archive." >&2
  exit 1
fi

tar -xzf "$tmp_dir/$archive" -C "$tmp_dir" cronwatch
install_dir=${CRONWATCH_INSTALL_DIR:-"$HOME/.local/bin"}
mkdir -p "$install_dir"
install -m 0755 "$tmp_dir/cronwatch" "$install_dir/cronwatch"
echo "Installed CronWatch to $install_dir/cronwatch"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to PATH to use cronwatch from any directory." ;;
esac
