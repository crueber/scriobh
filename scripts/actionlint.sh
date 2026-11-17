#!/usr/bin/env bash
# Lints every workflow with actionlint, a pinned version installed into a temporary directory. `make actionlint`.
# Needs Go and, the first time, the network (go install); it is not part of `make ci`.
#
#   scripts/actionlint.sh              the development workflows (.forgejo/workflows), the GitHub templates in
#                                      scripts/publish (filled in with an example owner and repository, as the
#                                      publish does), and .github/workflows when the tree has it
#   scripts/actionlint.sh FILE...      only those files
#   ACTIONLINT=/path/to/actionlint     use that binary instead of installing the pinned one (its version is printed)
#
# The runner labels this forge uses (docker, macos, windows) are declared to actionlint. Two findings on the
# development workflows are known and ignored, only there and only by these exact messages:
#   container "init"   Forgejo's runner accepts `init: true` on a job container; GitHub's schema has no such key.
#                      The GitHub workflows (templates) do not use it, and are checked without any ignore.
#   download-artifact@v3 the same, for the download side (desktop.yml's arch-branch job takes the .deb the linux job uploaded).
#   upload-artifact@v3 this forge's actions/upload-artifact stops at v3 (docs/OPERATIONS.md, "Continuous
#                      integration"); actionlint knows v3 is too old for GitHub. The GitHub templates use v4.
# Shellcheck and pyflakes are run on `run:` blocks and scripts only when they are on PATH, so the same files can
# lint differently on two machines; the script says which it found.
set -euo pipefail

ACTIONLINT_VERSION="v1.7.12"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/actionlint.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

bin="${ACTIONLINT:-}"
if [ -z "$bin" ]; then
  command -v go >/dev/null 2>&1 || { echo "actionlint: go is not on PATH (it installs actionlint $ACTIONLINT_VERSION)" >&2; exit 1; }
  GOBIN="$tmp/bin" GOFLAGS=-mod=mod go install "github.com/rhysd/actionlint/cmd/actionlint@$ACTIONLINT_VERSION" || { echo "actionlint: go install failed (the network?)" >&2; exit 1; }
  bin="$tmp/bin/actionlint"
fi
echo "actionlint: $("$bin" -version | head -1) ($bin)"
for t in shellcheck pyflakes; do
  if command -v "$t" >/dev/null 2>&1; then echo "actionlint: $t found, its checks run"; else echo "actionlint: $t not found, its checks are skipped"; fi
done

cat >"$tmp/config.yaml" <<'YAML'
self-hosted-runner:
  labels: [docker, macos, windows]
paths:
  .forgejo/workflows/*.yml:
    ignore:
      - 'unexpected key "init" for "container" section'
      - 'the runner of "actions/upload-artifact@v3" action is too old to run on GitHub Actions'
      - 'the runner of "actions/download-artifact@v3" action is too old to run on GitHub Actions'
YAML

files=("$@")
if [ ${#files[@]} -eq 0 ]; then
  shopt -s nullglob
  files=(.forgejo/workflows/*.yml .github/workflows/*.yml)
  # The templates, rendered the way scripts/publish-to-github.sh renders them, so what is linted is what ships.
  if [ -d scripts/publish ]; then
    mkdir -p "$tmp/render/.github/workflows"
    for tpl in scripts/publish/*.yml; do
      sed "s|{{OWNER}}|example|g; s|{{REPO}}|scriobh|g" "$tpl" >"$tmp/render/.github/workflows/$(basename "$tpl")"
      files+=("$tmp/render/.github/workflows/$(basename "$tpl")")
    done
  fi
fi
[ ${#files[@]} -gt 0 ] || { echo "actionlint: no workflow files" >&2; exit 1; }
status=0
"$bin" -config-file "$tmp/config.yaml" -oneline "${files[@]}" || status=$?
if [ "$status" = 0 ]; then echo "actionlint: ${#files[@]} workflow files, no findings"; fi
exit $status
