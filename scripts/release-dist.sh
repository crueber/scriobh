#!/usr/bin/env bash
# Builds the release files of a version: the production server and the agent CLI for fifteen platforms, one
# archive each, and SHA256SUMS.txt. CI's release workflow runs this, and so can you; what you build here is
# what the workflow publishes.
#
#   scripts/release-dist.sh --version v2.1.0                 # into ./dist
#   make release-dist VERSION=v2.1.0
#   scripts/release-dist.sh --version v2.0 --src ../checkout-of-the-tag --out /tmp/dist
#
#   --version V   the version stamped into both programs (required; the tag, as it is written)
#   --src DIR     the source tree to build (default: the tree this script is in). The release workflow builds a
#                 tag's tree with the script of the commit it runs on, so a tag made before this script existed
#                 can still be released; the script needs nothing of the tree but its go.mod, its sources and
#                 its LICENSE, NOTICE and README.md.
#   --out DIR     where the files go (default: ./dist, emptied first)
#   --targets L   a space-separated list of GOOS/GOARCH (default: the shipped platforms below)
#
# Platforms shipped (each was built, server and CLI, before it went on the list):
#   linux/amd64 linux/arm64 linux/riscv64 linux/ppc64le darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
#   linux/arm linux/386 windows/386   32-bit. linux/arm is built with GOARM=7 (set below, not left to the toolchain),
#                                     so it is for ARMv7 boards and not ARMv6 ones (the first Raspberry Pi); the
#                                     archive is named "arm". A 32-bit process has at most 2 GiB of address space, so a
#                                     large library is not for it. internal/pdfread keeps every size, offset and limit
#                                     inside an int or in int64 for it (internal/pdfread/num.go).
#   freebsd/amd64 freebsd/arm64       the BSDs. The Pandoc bridge sets the same resource limits as on the other Unixes
#   openbsd/amd64 netbsd/amd64        (internal/pandoc/rlimit_unix.go: syscall.Rlimit is int64 on FreeBSD) and reads
#                                     free disk space with syscall on FreeBSD and golang.org/x/sys/unix on OpenBSD and
#                                     NetBSD (free_unix.go, free_openbsd.go, free_netbsd.go).
# Only the host's own binary has ever been run (darwin/arm64, by the maintainer's checks); every other platform is
# cross-compiled and unexecuted. Windows has no Pandoc bridge: it refuses (KindUnsupported) rather than run Pandoc
# without the process limits.
#
# Pairs that build with CGO off but are not shipped: linux/s390x and linux/loong64 (rare; ask with --targets).
# A pair whose build breaks comes off the list, with the compiler's reason written here. These seven were off it
# until the 32-bit and per-OS fixes (a 1<<40 constant overflowing int in internal/pdfread; syscall.Rlimit and Statfs
# field types in internal/pandoc). The fix is a source change, so a tree older than it (a tag made before the fix,
# built with --src) fails on these seven and the script stops at the first: for such a tree, pass --targets with the
# first eight platforms above.
#
# What is built is what the Dockerfile builds: CGO_ENABLED=0 (the SQLite driver is pure Go), -trimpath,
# -buildvcs=false, -s -w, the server with `-tags prod` (no development issuer, no stub sign-in: the tag that
# separates the production server from the development build) and the CLI without tags, and the version set
# with the same -X flags (main.version for the server; main.version and the cli package's Version for the CLI).
#
# Deterministic enough to verify: the same source and version give the same binaries, on any machine with the
# same Go toolchain, and archives whose bytes depend only on their contents (scripts/release-archive.py: fixed
# mtime, owner and modes; no gzip time). The mtime is SOURCE_DATE_EPOCH when set, else the time of the source
# tree's commit. The archives' compressed bytes also depend on the zlib behind Python's gzip, so compare the
# programs inside the archives when a checksum differs between two machines.
#
# Nothing is signed. SHA256SUMS.txt is a checksum list, not a signature.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
version="${VERSION:-}"
src=""
out="dist"
targets="linux/amd64 linux/arm64 linux/arm linux/386 linux/riscv64 linux/ppc64le darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 windows/386 freebsd/amd64 freebsd/arm64 openbsd/amd64 netbsd/amd64"

die() { echo "release-dist: $*" >&2; exit 1; }
while [ $# -gt 0 ]; do
  case "$1" in
    --version) version="${2:?--version needs a value}"; shift 2 ;;
    --src) src="${2:?--src needs a directory}"; shift 2 ;;
    --out) out="${2:?--out needs a directory}"; shift 2 ;;
    --targets) targets="${2:?--targets needs a list}"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
[ -n "$version" ] || die "--version (or VERSION) is required, for example --version v2.1.0"
case "$version" in
  *[!A-Za-z0-9._+-]*|"") die "the version may hold only letters, digits and . _ + -: $version" ;;
esac
src="$(cd "${src:-$here/..}" && pwd)"
[ -f "$src/go.mod" ] && [ -d "$src/cmd/scriobh" ] && [ -d "$src/cmd/scriobh-cli" ] || die "$src is not a Scríobh source tree"
for f in LICENSE NOTICE README.md; do [ -f "$src/$f" ] || die "$src/$f is missing"; done
command -v python3 >/dev/null 2>&1 || die "python3 is required (it writes the archives)"
GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || die "go is not on PATH"

epoch="${SOURCE_DATE_EPOCH:-}"
if [ -z "$epoch" ]; then
  epoch="$(git -C "$src" log -1 --format=%ct 2>/dev/null || true)"
fi
[ -n "$epoch" ] || die "cannot tell the source's time: set SOURCE_DATE_EPOCH (seconds since the epoch)"

# The module path comes from go.mod, so the same script is right in the development tree and in a copy whose
# module path was rewritten for publication.
module="$(cd "$src" && "$GO" list -m)"
[ -n "$module" ] || die "go list -m gave no module path"
server_ldflags="-s -w -X main.version=$version"
# main.version too: the CLI's main copies it over cli.Version at start, so the -X on the cli package alone leaves
# `scriobh-cli --version` at "dev" (the Dockerfile's CLI build had that flaw; it has both now).
cli_ldflags="-s -w -X main.version=$version -X $module/internal/cli.Version=$version"

mkdir -p "$out"
out="$(cd "$out" && pwd)"
# --out is emptied: never the source itself, any folder that holds it (its parents, up to /), or the home folder.
# A folder inside the source (the default ./dist) is the normal case.
case "${out%/}/" in "${src%/}"/|"${HOME%/}"/|/) die "refusing to empty $out: it is the source tree, the home folder or /" ;; esac
case "${src%/}/" in "${out%/}"/*) die "refusing to empty $out: it holds the source tree $src" ;; esac
[ ! -e "$out/go.mod" ] || die "refusing to empty $out: it holds a go.mod"
find "$out" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
stage="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-dist.XXXXXX")"
trap 'rm -rf "$stage"' EXIT

echo "release-dist: $version from $src ($module), $(date -u -r "$epoch" +%FT%TZ 2>/dev/null || date -u -d "@$epoch" +%FT%TZ), $("$GO" version)"
start=$SECONDS
for t in $targets; do
  goos="${t%/*}"; goarch="${t#*/}"
  ext=""; [ "$goos" = windows ] && ext=".exe"
  name="scriobh-$version-$goos-$goarch"
  dir="$stage/$goos-$goarch"
  mkdir -p "$dir"
  t0=$SECONDS
  (cd "$src" &&
    if [ "$goarch" = arm ]; then export GOARM=7; fi &&   # the toolchain's default depends on where it was built
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO" build -trimpath -buildvcs=false -tags prod \
      -ldflags "$server_ldflags" -o "$dir/scriobh$ext" ./cmd/scriobh &&
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO" build -trimpath -buildvcs=false \
      -ldflags "$cli_ldflags" -o "$dir/scriobh-cli$ext" ./cmd/scriobh-cli) || die "the build for $t failed"
  if [ "$goos" = windows ]; then archive="$name.zip"; else archive="$name.tar.gz"; fi
  python3 -I "$here/release-archive.py" "$out/$archive" "$name" "$epoch" \
    "$dir/scriobh$ext" "$dir/scriobh-cli$ext" "$src/LICENSE" "$src/NOTICE" "$src/README.md"
  echo "release-dist: $archive  ($((SECONDS - t0)) s)"
done

# The checksum list: sha256sum's format ("<hash>  <name>"), sorted by name, written from inside the folder so
# the names carry no path. Either tool: Linux has sha256sum, macOS has shasum.
cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$@"; }
else
  sum() { shasum -a 256 "$@"; }
fi
export LC_ALL=C   # the glob sorts by byte, the same on every machine
files=$(printf '%s\n' *)
# shellcheck disable=SC2086  # the names hold no spaces (checked above)
sum $files >SHA256SUMS.txt
echo "release-dist: done in $((SECONDS - start)) s; $(echo "$files" | wc -l | tr -d ' ') archives and SHA256SUMS.txt in $out"
