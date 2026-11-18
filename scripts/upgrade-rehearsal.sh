#!/usr/bin/env bash
# Upgrade rehearsal (docs/OPERATIONS.md, "Upgrades").
#
#   scripts/upgrade-rehearsal.sh               from v0.1, v0.5, v1.0, v1.1, v1.2, v2.0, v2.2.0 and v2.2.1
#   scripts/upgrade-rehearsal.sh v0.3 v0.5     from each tag named
#   scripts/upgrade-rehearsal.sh --all         from every release tag (v2.2.1 included)
#
# For each tag: `git archive` the tag into a temporary directory (nothing in
# this tree is touched), build its development server the way its Makefile's
# build-dev did, and run TestUpgradeRehearsal (cmd/scriobh/upgrade_rehearsal_test.go)
# with it. The old server writes projects, documents and saves (and snapshots
# and goals where the release has them) through its own API and signs in through
# its development issuer, and then a rich dataset (cmd/scriobh/upgrade_rich_test.go):
# three people with the three roles, a book with parts, chapters, scenes, notes,
# revisions and snapshots, comments and suggestions, share links, API keys, a
# webhook, a static-site configuration, a picture named in a chapter and in a
# research note, a Compendium with entities, relationships
# and mentions, goals, an import, a compile and an export, an AI provider with a
# sealed key, an idempotency key and a Git remote pushed to a development Git
# server and, from v1.1, a second remote with audience private (every operation
# the old release lacks is skipped, so the same code runs for every tag); the answers are recorded. Then the current build
# starts on the same data directory and must take the pre-migration copy, apply
# every migration, end with a fresh install's schema (columns, foreign keys,
# unique constraints, indexes, triggers), pass integrity and foreign-key checks,
# keep the old session, and answer every field the old release answered,
# unchanged. It then uses what the old release made: the API keys, the share
# links, the webhook (signed with its original secret, to its sealed address), the
# sealed provider key, an idempotent replay, the site; checks that the old Git
# remote reads as private and pushes exactly the files it pushed; that the cover
# migrations kept every attachment and attachment_refs row, left no attachment
# cover-clean and no project a layout, and gave a private remote include_cover 1
# and a public one 0; that mail, the
# semantic index, Pandoc, presence, passphrases and typography are all off; and
# that a second start applies nothing and takes no second copy. Last, it rolls back the way OPERATIONS.md says (the pre-migration
# copy over scriobh.db) and the old release must start and answer as before.
#
# UPGRADE_FIXTURE_DIR=internal/db/testdata writes the fixture of each tag
# (<tag>.db) from the old release's database, before the upgrade. An existing
# fixture is never overwritten: remove it first to replace it.
#
# No network (the Go module proxy is off; every address is loopback). Exits 0
# when every rehearsal passes.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

for v in HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy; do
  unset "$v"
done
export GOPROXY=off GOFLAGS=-mod=mod

tags=("$@")
if [ "${#tags[@]}" -eq 0 ]; then
  tags=(v0.1 v0.5 v1.0 v1.1 v1.2 v2.0 v2.2.0 v2.2.1)
elif [ "${tags[0]}" = "--all" ]; then
  tags=()
  while IFS= read -r t; do tags+=("$t"); done < <(git tag --list 'v[0-9]*' --sort=version:refname)
fi

work="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-upgrade-XXXXXX")"
trap 'rm -rf "$work"' EXIT

version="$(git describe --tags --always --dirty --match 'v[0-9]*' 2>/dev/null || echo dev)"
echo "== build the current development server ($version)"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$work/scriobh" ./cmd/scriobh

failed=0
for tag in "${tags[@]}"; do
  echo "== $tag: build its development server from git archive"
  src="$work/src-$tag"
  mkdir -p "$src"
  git archive "$tag" | tar -x -C "$src"
  (cd "$src" && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$tag" -o "$work/scriobh-$tag" ./cmd/scriobh)
  fixture=""
  if [ -n "${UPGRADE_FIXTURE_DIR:-}" ]; then
    fixture="$(cd "$UPGRADE_FIXTURE_DIR" && pwd)/$tag.db"
    if [ -e "$fixture" ]; then
      # A fixture can carry rows other tests count (v0.1 and v0.2 were made by
      # hand with more in them); remove it first to replace it on purpose.
      echo "== $tag: $fixture exists; not overwriting it" >&2
      fixture=""
    fi
  fi
  echo "== $tag: write with it, then upgrade with $version"
  start=$(date +%s)
  SCRIOBH_REHEARSAL_BIN="$work/scriobh" SCRIOBH_UPGRADE_OLD_BIN="$work/scriobh-$tag" SCRIOBH_UPGRADE_TAG="$tag" SCRIOBH_UPGRADE_FIXTURE="$fixture" \
    go test ./cmd/scriobh -run '^TestUpgradeRehearsal$' -count=1 -timeout 10m -v >"$work/log-$tag" 2>&1 || true
  sed -n 's/.*REHEARSAL /  /p' "$work/log-$tag"
  if grep -q '^ok' "$work/log-$tag"; then
    echo "== $tag: passed in $(( $(date +%s) - start ))s"
  else
    cat "$work/log-$tag" >&2
    echo "== $tag: FAILED" >&2
    failed=1
  fi
done
exit $failed
