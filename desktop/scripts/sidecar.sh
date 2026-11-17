#!/usr/bin/env bash
# Builds the production server and puts it where the bundler looks for it:
# desktop/src-tauri/binaries/scriobh-desktop-server-<target triple>[.exe]. That folder is not committed.
#
#   desktop/scripts/sidecar.sh                      # for this machine
#   desktop/scripts/sidecar.sh x86_64-apple-darwin  # for another target (Go cross-compiles; the shell does not)
#
# The server is the same binary a server install runs: `-tags prod`, no cgo, built from this tree.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
triple="${1:-$(rustc -vV | sed -n 's/^host: //p')}"
[ -n "$triple" ] || { echo "sidecar: could not tell the target triple (is rustc installed?)" >&2; exit 1; }

case "$triple" in
  aarch64-apple-darwin)        goos=darwin  goarch=arm64 ext="" ;;
  x86_64-apple-darwin)         goos=darwin  goarch=amd64 ext="" ;;
  x86_64-unknown-linux-gnu)    goos=linux   goarch=amd64 ext="" ;;
  aarch64-unknown-linux-gnu)   goos=linux   goarch=arm64 ext="" ;;
  x86_64-pc-windows-msvc)      goos=windows goarch=amd64 ext=".exe" ;;
  aarch64-pc-windows-msvc)     goos=windows goarch=arm64 ext=".exe" ;;
  *) echo "sidecar: no Go target is known for $triple" >&2; exit 1 ;;
esac

version="${VERSION:-$(git -C "$root" describe --tags --always --dirty --match 'v[0-9]*' 2>/dev/null || echo dev)}"
out="$root/desktop/src-tauri/binaries/scriobh-desktop-server-$triple$ext"
mkdir -p "$(dirname "$out")"
(cd "$root" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "${GO:-go}" build -trimpath -tags prod -ldflags "-s -w -X main.version=$version" -o "$out" ./cmd/scriobh)
echo "sidecar: $out ($version)"
