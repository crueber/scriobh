#!/usr/bin/env bash
# Backup and restore rehearsal (docs/OPERATIONS.md, "Restore drill").
#
# Builds the development server the way `make build-dev` does, then drives it
# through TestRestoreRehearsal (cmd/scriobh/restore_rehearsal_test.go):
#
#   1. start it on 127.0.0.1:0 and fill an instance through the API: two
#      projects, documents, revisions and a snapshot, an image, an alternative,
#      comments, share links and API keys (one of each revoked), a revoked
#      session, a Git remote with a token, an AI provider with a key, a webhook
#      with a sealed address, site options and a goal;
#   2. `scriobh backup` beside the running server, then `scriobh backup --out`
#      with it stopped;
#   3. `scriobh restore` into a directory that does not exist yet, with the same
#      SCRIOBH_SECRET_KEY; compare every table row for row, the stored files,
#      44 API answers and the attachment's bytes; remove mirror/ and let the
#      server write it again, byte for byte; use each credential for real
#      against loopback stand-ins (no outside address is contacted). A
#      restore pauses every webhook and parks the feeder (so a restored copy
#      never sends the old outbox); the rehearsal checks that, switches the
#      webhook on again through the API and compares the answers after that;
#   4. restore again with a different key: the writing is all there, the
#      output and the API say plainly which credentials cannot be read.
#
#   5. the Phase 7 rehearsal (TestRestoreRehearsalEveryFeature): an instance with
#      every feature that sends anything switched on and something waiting at
#      each outside party (a Git remote with a token, a signing key and a
#      trigger; a webhook with a delivery waiting; mail on with a Kindle edition
#      queued and a digest opt-in; a semantic index with embeddings, one of them
#      stale; Pandoc enabled; a project with a passphrase; an imported EPUB with
#      its stored source file; the presence preference; typography), each
#      outside party a loopback stand-in that counts what reaches it. It is
#      restored on a different path and every table but the ones a restore
#      changes on purpose must be identical; a server started on the copy must
#      reach no stand-in in a quiet window and switch nothing on; a restore
#      without the secret key followed by a start with the original put back
#      must not switch a remote on; and a restore whose mail and index pauses
#      fail must exit non-zero saying "NOT paused", pause everything else, keep
#      its markers and keep both silent at every start until the cause is fixed.
#      The steps are also run in the opposite order and twice (the order is the
#      order of package initialisation) and must leave the same database.
#
# No network: the Go module proxy is switched off and every address is
# loopback. Everything is written under a temporary directory that is removed.
# Exits 0 when the rehearsal passes.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

for v in HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy; do
  unset "$v"
done
export GOPROXY=off GOFLAGS=-mod=mod

work="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-restore-XXXXXX")"
trap 'rm -rf "$work"' EXIT

version="$(git describe --tags --always --dirty --match 'v[0-9]*' 2>/dev/null || echo dev)"
echo "== build the development server ($version)"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$work/scriobh" ./cmd/scriobh

echo "== rehearse backup and restore"
start=$(date +%s)
SCRIOBH_REHEARSAL_BIN="$work/scriobh" go test ./cmd/scriobh -run '^(TestRestoreRehearsal|TestRestoreRehearsalEveryFeature|TestRestoreStepsLeaveTheSameDatabaseInAnyOrder)$' -count=1 -timeout 15m -v >"$work/log" 2>&1 || true
# The numbers the rehearsal checked: each REHEARSAL line and its continuation.
awk '/REHEARSAL /{sub(/.*REHEARSAL /,"  "); print; more=1; next} more && /^            /{print; next} {more=0}' "$work/log"
if grep -q '^ok' "$work/log"; then
  echo "== restore rehearsal passed in $(( $(date +%s) - start ))s"
  exit 0
fi
cat "$work/log" >&2
echo "== restore rehearsal FAILED" >&2
exit 1
