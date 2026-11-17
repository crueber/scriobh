#!/usr/bin/env bash
# Runs the shell's Rust tests with the real server in them. The tests in desktop/src-tauri/tests/real_server.rs ask
# the production server binary (not the stand-in) the questions of the launch contract, and do nothing unless
# SCRIOBH_REAL_SERVER names a binary. This sets it to the one desktop/scripts/sidecar.sh built for this machine, and
# refuses to run when that binary is not there, so a missing server fails the gate instead of skipping the real-server tests.
#
#   desktop/scripts/sidecar.sh && desktop/scripts/test.sh [arguments for cargo test]
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
triple="$(rustc -vV | sed -n 's/^host: //p')"
[ -n "$triple" ] || { echo "test: could not tell the host triple (is rustc installed?)" >&2; exit 1; }
ext=""
case "$triple" in *-windows-*) ext=".exe" ;; esac
server="$root/desktop/src-tauri/binaries/scriobh-desktop-server-$triple$ext"
if [ ! -f "$server" ] || [ ! -x "$server" ]; then
  echo "test: no server binary at $server; run desktop/scripts/sidecar.sh first" >&2
  exit 1
fi
cd "$root/desktop/src-tauri"
SCRIOBH_REAL_SERVER="$server" exec "${CARGO:-cargo}" test --locked "$@"
