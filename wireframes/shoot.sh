#!/usr/bin/env sh
# Regenerate every wireframe screenshot. Needs Chromium and Python 3.
set -eu
cd "$(dirname "$0")"

PORT="${PORT:-8123}"
CHROME="${CHROME:-chromium}"
PROFILE="${TMPDIR:-/tmp}/scriobh-wireframe-chrome"

python3 -m http.server "$PORT" --bind 127.0.0.1 >/dev/null 2>&1 &
SERVER=$!
trap 'kill "$SERVER" 2>/dev/null || true' EXIT INT TERM
sleep 1

shot() { # $1 url, $2 out, $3 width, $4 height, $5 scale
  "$CHROME" --headless=new --no-sandbox --disable-gpu --hide-scrollbars \
    --force-device-scale-factor="$5" --window-size="$3,$4" \
    --user-data-dir="$PROFILE" --virtual-time-budget=4000 \
    --screenshot="$2" "$1" >/dev/null 2>&1
}

for d in a; do
  for v in editor library plan compendium compile studio; do
    shot "http://127.0.0.1:$PORT/$d.html?v=$v" "shots/$d-$v.png" 1440 900 1
  done
  shot "http://127.0.0.1:$PORT/$d.html?v=mobile" "shots/$d-mobile.png" 390 844 2
done

shot "http://127.0.0.1:$PORT/overview.html" "shots/00-overview.png" 1680 840 1
shot "http://127.0.0.1:$PORT/overview-mobile.html" "shots/00-mobile.png" 1100 820 1

echo "wrote $(find shots -name '*.png' | wc -l) screenshots to shots/"
