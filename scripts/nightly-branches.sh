#!/usr/bin/env bash
# Print the branches of a repository that have a commit in the last 24 hours,
# one per line as "<sha> <branch>", for the nightly workflow. Prints nothing and
# exits 0 when there is none (the nightly run then does nothing).
#
#   scripts/nightly-branches.sh [--remote URL|PATH] [--since EPOCH] [--branch NAME] [--max N]
#
#   --remote  where to look (default: $NIGHTLY_REMOTE, else this checkout's origin)
#   --since   the cutoff as seconds since the epoch (default: now minus 86400);
#             a branch is listed when its tip's committer date is at or after it
#   --branch  list this branch whatever its date (a manual run); it must exist
#   --max     at most N branches (default 4), in the order below; the ones left
#             out are named on stderr
#
# Branches only: tags and other refs are never looked at. The default branch (the
# remote's HEAD, else main) is listed first, then phase-* branches, then the rest,
# each group in name order, and the cap is applied to the branches in that order
# after the date and merge filters. A branch whose tip is already an ancestor of
# the default branch's tip is left out: it was merged, and the default branch is
# tested with it in (a recent merge commit is that branch's test). A manual run
# with --branch lists the branch it names whatever its history. Two branches at
# the same commit are listed once (the stages test commits), under the first
# name, and so are two with the same tree (a squash merge). The remote is read
# into a throwaway repository, so the checkout the script runs in is not touched. With NIGHTLY_TOKEN set the fetch sends it as an
# Authorization header (the workflow's token).
#
# Date arithmetic is epoch seconds and `git for-each-ref`, not `date -d`, so the
# script runs the same on Debian and macOS.
set -euo pipefail

remote="${NIGHTLY_REMOTE:-}"
since=""
only=""
max=4
while [ $# -gt 0 ]; do
  case "$1" in
  --remote) remote="$2"; shift 2 ;;
  --since) since="$2"; shift 2 ;;
  --branch) only="$2"; shift 2 ;;
  --max) max="$2"; shift 2 ;;
  *) echo "nightly-branches: unknown argument $1" >&2; exit 2 ;;
  esac
done
[ -n "$remote" ] || remote="$(git remote get-url origin)"
[ -n "$since" ] || since=$(( $(date +%s) - 86400 ))
case "$since$max" in *[!0-9]*) echo "nightly-branches: --since and --max must be whole numbers" >&2; exit 2 ;; esac

git_remote=(git)
[ -z "${NIGHTLY_TOKEN:-}" ] || git_remote=(git -c "http.extraHeader=Authorization: token ${NIGHTLY_TOKEN}")

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
git init -q --bare "$work/r"

head_ref="$("${git_remote[@]}" ls-remote --symref "$remote" HEAD 2>/dev/null | awk '$1 == "ref:" { sub("^refs/heads/", "", $2); print $2; exit }')" || head_ref=""

heads="$("${git_remote[@]}" ls-remote --heads "$remote")" || { echo "nightly-branches: git ls-remote failed" >&2; exit 1; }
names=()
while IFS=$'\t' read -r _ ref; do
  [ -n "$ref" ] && names+=("${ref#refs/heads/}")
done <<<"$heads"
if [ "${#names[@]}" -eq 0 ]; then
  echo "nightly-branches: the remote has no branches" >&2
  exit 1
fi
# The default branch: where the remote's HEAD points, else main, else master, when it exists.
default=""
for cand in "$head_ref" main master; do
  [ -n "$cand" ] || continue
  for n in "${names[@]}"; do [ "$n" = "$cand" ] && default="$cand"; done
  [ -z "$default" ] || break
done
if [ -n "$only" ]; then
  found=""
  for n in "${names[@]}"; do [ "$n" = "$only" ] && found=1; done
  [ -n "$found" ] || { echo "nightly-branches: the remote has no branch $only" >&2; exit 1; }
  names=("$only")
fi

# Whether a branch is merged needs the history between the tips, so the fetch is not shallow; commits
# only (the trees and blobs are skipped where the server supports a filter, and the fetch falls back to a plain one where it does not).
refspecs=()
for n in "${names[@]}"; do refspecs+=("+refs/heads/$n:refs/nightly/$n"); done
"${git_remote[@]}" -C "$work/r" fetch -q --no-tags --filter=tree:0 "$remote" "${refspecs[@]}" 2>/dev/null \
  || "${git_remote[@]}" -C "$work/r" fetch -q --no-tags "$remote" "${refspecs[@]}" \
  || { echo "nightly-branches: git fetch failed" >&2; exit 1; }

# "<committer epoch> <sha> <tree> <branch>", ordered default branch, phase-*, others, then by name.
# Branches are deduplicated by tree: a branch with the default branch's tree tests the same and
# does not take a slot (the default branch comes first, so it is kept).
rows="$(git -C "$work/r" for-each-ref --format='%(committerdate:unix) %(objectname) %(tree) %(refname:strip=2)' refs/nightly/ | awk -v def="$default" '
  { rank = ($4 == def) ? 0 : ($4 ~ /^phase-/ ? 1 : 2); printf "%d %s\n", rank, $0 }' | sort -k1,1n -k5,5 | cut -d' ' -f2-)"

default_sha=""
[ -z "$default" ] || [ -n "$only" ] || default_sha="$(git -C "$work/r" rev-parse "refs/nightly/$default")"

seen=" "
count=0
skipped=()
merged=()
while read -r when sha tree branch; do
  [ -n "$sha" ] || continue
  if [ -z "$only" ] && [ "$when" -lt "$since" ]; then continue; fi
  # Already part of the default branch: tested there, with everything merged since.
  if [ -z "$only" ] && [ -n "$default_sha" ] && [ "$branch" != "$default" ] &&
    git -C "$work/r" merge-base --is-ancestor "$sha" "$default_sha"; then
    merged+=("$branch")
    continue
  fi
  case "$seen" in *" $tree "*) continue ;; esac
  seen="$seen$tree "
  if [ "$count" -ge "$max" ]; then skipped+=("$branch"); continue; fi
  count=$((count + 1))
  echo "$sha $branch"
done <<<"$rows"

if [ "$count" -eq 0 ]; then
  echo "nightly-branches: nothing to do: no branch has a commit since $since" >&2
fi
if [ "${#merged[@]}" -gt 0 ]; then
  echo "nightly-branches: already merged into $default, tested there: ${merged[*]}" >&2
fi
if [ "${#skipped[@]}" -gt 0 ]; then
  echo "nightly-branches: over the cap of $max, not tested tonight: ${skipped[*]}" >&2
fi
