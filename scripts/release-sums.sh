#!/usr/bin/env bash
# Writes a SHA-256 list of the files in a folder, in sha256sum's format: the checksum step of the GitHub release
# workflow, kept in a script so publish-check can run it on a folder shaped like a real download.
#
#   scripts/release-sums.sh DIR NAME      writes DIR/NAME; writes nothing when DIR holds no files
#
# DIR must be flat: a sub-folder (or anything else that is not a regular file) is an error naming it. That is the
# shape upload-artifact gives a download when an artifact was uploaded from several folders (each file keeps its
# path below the common parent), and `sha256sum -- *` over it fails with "Is a directory" after the long builds.
set -euo pipefail
dir="${1:?usage: release-sums.sh DIR NAME}"; name="${2:?usage: release-sums.sh DIR NAME}"
[ -d "$dir" ] || { echo "release-sums: no such folder: $dir" >&2; exit 1; }
cd "$dir"
export LC_ALL=C
shopt -s nullglob dotglob
files=()
for f in *; do
  [ "$f" = "$name" ] && continue
  [ -f "$f" ] && [ ! -L "$f" ] || { echo "release-sums: $dir/$f is not a plain file: the folder must be flat (stage the packages into one folder before uploading them)" >&2; exit 1; }
  files+=("$f")
done
[ ${#files[@]} -gt 0 ] || { echo "release-sums: $dir holds no files; no $name written"; exit 0; }
if command -v sha256sum >/dev/null 2>&1; then sha256sum -- "${files[@]}" >"$name"; else shasum -a 256 -- "${files[@]}" >"$name"; fi
echo "release-sums: $name lists ${#files[@]} files"
