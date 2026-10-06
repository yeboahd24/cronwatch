#!/bin/sh
# Regenerates the README screenshots in docs/images from made-up demo data
# (scripts/demo). Needs Go and Chromium or Google Chrome.
#
#   sh scripts/screenshots.sh
set -eu

cd "$(dirname "$0")/.."
browser=$(command -v chromium || command -v chromium-browser || command -v google-chrome || command -v google-chrome-stable) || {
  echo "screenshots.sh: needs chromium or google-chrome on PATH" >&2
  exit 1
}
tmp=$(mktemp -d)
pid=
trap '[ -n "$pid" ] && kill "$pid" 2>/dev/null; rm -rf "$tmp"' EXIT HUP INT TERM

# Fixed time zone so the pages read the same wherever this runs.
export TZ=UTC
# Stamp the latest release, as the Servers page shows each server's version.
version=$(git describe --tags --abbrev=0 2>/dev/null || echo dev)
go build -ldflags "-X github.com/yeboahd24/cronwatch/internal/app.Version=$version" -o "$tmp/cronwatch" ./cmd/cronwatch
eval "$(go run ./scripts/demo -data-dir "$tmp/data")" # sets FAILED_RUN, EXPORT_JOB, INGEST_JOB, LIVE_RUN

port=8790
# The Servers page names this machine; give it a made-up name where a user
# namespace allows, so the screenshots do not show the real one.
serve="$tmp/cronwatch serve --data-dir $tmp/data --addr 127.0.0.1:$port --sync-crontab=false"
if unshare --uts --map-root-user true 2>/dev/null; then
  unshare --uts --map-root-user sh -c "hostname ops-hub && exec $serve" >"$tmp/serve.log" 2>&1 &
else
  echo "screenshots.sh: warning: cannot set a hostname; the Servers page shows this machine's" >&2
  $serve >"$tmp/serve.log" 2>&1 &
fi
pid=$!
tries=0
until curl -fs "http://localhost:$port/healthz" >/dev/null 2>&1; do
  tries=$((tries + 1))
  if [ "$tries" -gt 50 ]; then
    cat "$tmp/serve.log" >&2
    exit 1
  fi
  sleep 0.2
done

# shot FILE WIDTH HEIGHT PATH captures one page at twice the pixel density.
shot() {
  "$browser" --headless --disable-gpu --no-sandbox --hide-scrollbars --force-device-scale-factor=2 \
    --window-size="$2,$3" --screenshot="docs/images/$1" "http://localhost:$port$4" >/dev/null 2>&1
  echo "docs/images/$1"
}

shot jobs.png 1200 860 /
shot timeline.png 1200 600 /timeline
shot run-failed.png 1200 1370 "/runs/$FAILED_RUN?stream=compare"
shot job-durations.png 1200 920 "/jobs/$EXPORT_JOB"
shot servers.png 1200 1530 /servers
shot run-live.png 1200 880 "/runs/$LIVE_RUN"
