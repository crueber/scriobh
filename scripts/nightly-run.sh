#!/usr/bin/env bash
# Run one stage of the nightly workflow over a list of commits, one after the
# other, and fail at the end if any of them failed (a failure does not stop the
# rest: tonight's other branches still get their run).
#
#   scripts/nightly-run.sh STAGE "SHA:BRANCH SHA:BRANCH ..."
#
# STAGE is one of:
#   race   make test-race-core (the race detector on the concurrent packages)
#   plain  the whole plain `go test ./...` with the timing tests ON
#          (SCRIOBH_TIMING unset, TEST_PARALLEL=1, -count=1), internal/text first
#   bench  the performance gate (go run ./cmd/scriobh bench, 150k, 500k, 1M words)
#
# Each commit is fetched into its own directory under $NIGHTLY_WORK (default: a
# temporary one) and tested there with that commit's own Makefile and tests;
# only this driver, the Go toolchain and the job's tools come from the commit
# the workflow itself was started on (main, for the schedule). Reports go to
# $NIGHTLY_REPORTS (bench only).
#
# Environment:
#   NIGHTLY_REMOTE   the repository to fetch from (default: this checkout's origin)
#   NIGHTLY_TOKEN    sent as an Authorization header when set
#   NIGHTLY_TIMEOUT  minutes allowed per commit (default 35 for race and plain, 25 for bench)
#   NIGHTLY_DRY_RUN  1: fetch and check out, print the commands, run nothing
set -uo pipefail

stage="${1:-}"
list="${2:-}"
case "$stage" in
race | plain | bench) ;;
*) echo "usage: nightly-run.sh race|plain|bench \"SHA:BRANCH ...\"" >&2; exit 2 ;;
esac
if [ -z "$list" ]; then
  echo "nightly-run.sh: no branches to test; nothing to do"
  exit 0
fi

remote="${NIGHTLY_REMOTE:-$(git remote get-url origin)}"
git_remote=(git)
[ -z "${NIGHTLY_TOKEN:-}" ] || git_remote=(git -c "http.extraHeader=Authorization: token ${NIGHTLY_TOKEN}")
work="${NIGHTLY_WORK:-$(mktemp -d)}"
reports="${NIGHTLY_REPORTS:-$PWD/nightly-reports}"
dry="${NIGHTLY_DRY_RUN:-}"
case "$stage" in bench) default_timeout=25 ;; *) default_timeout=35 ;; esac
minutes="${NIGHTLY_TIMEOUT:-$default_timeout}"
mkdir -p "$reports"

# GNU timeout is on the runner's Debian image; macOS has it as gtimeout or not at all.
if command -v timeout >/dev/null 2>&1; then
  with_timeout() { timeout --signal=TERM --kill-after=60s "${minutes}m" "$@"; }
elif command -v gtimeout >/dev/null 2>&1; then
  with_timeout() { gtimeout --signal=TERM --kill-after=60s "${minutes}m" "$@"; }
else
  with_timeout() { "$@"; }
fi

run_stage() { # in the commit's directory
  local tag="$1"
  case "$stage" in
  race)
    # A commit that predates the target has nothing to run: say so and carry on.
    if ! grep -q '^test-race-core:' Makefile; then
      echo "NOTICE: this commit's Makefile has no test-race-core target; the race stage is skipped for it"
      return 0
    fi
    # Two packages at a time and two threads each: these tests do not need more, and a runner with four
    # vCPUs would otherwise run each package on four (GOMAXPROCS defaults to every CPU).
    GOMAXPROCS=2 TEST_PARALLEL=2 with_timeout make test-race-core
    ;;
  plain)
    # internal/text first, on a machine that has not been busy for this job: it
    # holds the tightest budget (a 2 MiB file of reference definitions in 1.5 s,
    # 0.85 s cool and 1.7 s warm). Then every other package. SCRIOBH_TIMING is
    # unset so the timing tests run.
    (
      unset SCRIOBH_TIMING
      # One package at a time on two threads: the whole pass takes 11 minutes that way on a laptop with the
      # timing tests on and every budget held (internal/text 16 s), so nothing here needs a fourth core.
      export CI=true TEST_PARALLEL=1 GOMAXPROCS=2
      # Both passes run whatever the first one does: a miss in internal/text must
      # not hide the other packages. The exit status is the worse of the two.
      with_timeout bash -c '
        rc=0
        make editor-deps || exit 1
        go test -p 1 -timeout 30m -count=1 ./internal/text || rc=1
        pkgs="$(go list ./... | grep -v "/internal/text$")" || { echo "go list failed" >&2; exit 1; }
        test -n "$pkgs" || { echo "go list returned no packages" >&2; exit 1; }
        go test -p 1 -timeout 30m -count=1 $pkgs || rc=1
        exit $rc'
    )
    ;;
  bench)
    with_timeout go run ./cmd/scriobh bench --sizes 150k,500k,1m \
      --out "$reports/bench-report-$tag.json" --candidate "$reports/bench-baseline-candidate-$tag.json"
    ;;
  esac
}

failed=()
for item in $list; do
  sha="${item%%:*}"
  branch="${item#*:}"
  tag="$(printf '%s' "$branch" | tr -c 'A-Za-z0-9._-' '_')-${sha:0:7}"
  echo "::group::$stage: $branch ($sha)"
  dir="$work/$sha"
  if [ ! -d "$dir/.git" ]; then
    mkdir -p "$dir" && git init -q "$dir" &&
      git -C "$dir" remote add origin "$remote" &&
      "${git_remote[@]}" -C "$dir" fetch -q --depth 1 origin "$sha" &&
      git -C "$dir" checkout -q FETCH_HEAD || {
      echo "FAILED: could not fetch $sha of $branch"
      failed+=("$branch(fetch)")
      echo "::endgroup::"
      continue
    }
  fi
  started=$(date +%s)
  if [ -n "$dry" ]; then
    echo "DRY RUN: would run stage $stage in $dir for $branch ($(git -C "$dir" log -1 --format=%h' '%s))"
    (cd "$dir" && grep -c '^test-race-core:' Makefile >/dev/null && echo "  Makefile has test-race-core" || echo "  Makefile has no test-race-core")
    rc=0
  else
    (cd "$dir" && run_stage "$tag")
    rc=$?
  fi
  echo "::endgroup::"
  echo "$stage $branch ($sha): exit $rc after $(( $(date +%s) - started )) s"
  if [ "$rc" -ne 0 ]; then
    [ "$rc" -eq 124 ] && echo "FAILED: $stage for $branch ran past ${minutes} minutes"
    failed+=("$branch")
  fi
  # Keep the disk: the next commit has its own directory and caches are shared.
  [ -n "$dry" ] || rm -rf "$dir"
done

if [ "${#failed[@]}" -gt 0 ]; then
  echo "nightly $stage: FAILED for: ${failed[*]}"
  exit 1
fi
echo "nightly $stage: all passed"
