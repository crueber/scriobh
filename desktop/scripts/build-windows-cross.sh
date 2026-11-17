#!/usr/bin/env bash
# Builds the Windows installer of the desktop app on Linux (or macOS): the Rust shell cross-compiled with
# cargo-xwin, the Go server cross-compiled with GOOS=windows, and an NSIS installer made by makensis. No Windows
# machine is involved, and nothing here ever runs a Windows program. The installer is not signed.
#
#   desktop/scripts/build-windows-cross.sh                       # installer into ./dist
#   desktop/scripts/build-windows-cross.sh --version v2.1.0      # also names the file after the tag, stamps the server
#   desktop/scripts/build-windows-cross.sh --out /tmp/out --no-lint
#
#   --version V   the release tag, as written. It is stamped into the server (main.version) and into the file's
#                 name. Without it the server says what `git describe` says and the file carries the app's own
#                 version (the one in tauri.conf.json).
#   --out DIR     where the installer, its .sha256 and the line in SHA256SUMS.txt go (default: ./dist). Nothing in
#                 DIR is deleted: scripts/release-dist.sh empties it first, then this script adds to it, so a release
#                 workflow runs this after that one and uploads dist/*.
#   --build-nsis  build the pinned NSIS even when the NSIS on this machine is new enough (a way to try that path)
#   --no-lint     skip `cargo clippy` for the Windows target (it is the only place the Windows-only Rust code is
#                 compiled with lints; leave it on)
#
# Environment: SRC_DIR builds another tree (a tag's checkout) with this script; SCRIOBH_WIN_CACHE, below.
#
# What it installs, and only when it is missing (a warm runner or a developer machine installs nothing):
#   apt (Linux, as root):   nsis clang lld llvm p7zip-full       (the Tauri documentation's list, plus 7z to look inside)
#   NSIS 3.13, pinned:      built from source when the machine's makensis cannot compile Tauri's installer script.
#                           Debian 12's `nsis` is 3.08, which has no Include/Win/RestartManager.nsh, and the script fails
#                           with `!include: could not find: "Win\RestartManager.nsh"`. The source and the release
#                           archive (Stubs, Plugins, Include) are downloaded from SourceForge and checked against
#                           the SHA-256 pinned below before anything is built (apt: scons g++ zlib1g-dev bzip2)
#   rustup toolchain:       $RUST_TOOLCHAIN (default below) with clippy, and the target x86_64-pc-windows-msvc
#   cargo-xwin:             pinned, below (`cargo install --locked`)
#   the Windows SDK and C runtime: downloaded by cargo-xwin on first use into $XWIN_CACHE_DIR (below)
#   the Tauri command line: `npm ci` in desktop/ (pinned by package-lock.json)
#
# LICENCE. The xwin step downloads the Microsoft Visual C++ runtime and the Windows SDK from Microsoft and uses
# them to compile and link. They are Microsoft's, under Microsoft's licence terms
# (https://go.microsoft.com/fwlink/?LinkId=2086102), not under this project's. cargo-xwin says: "By using this
# software you are consented to accept the license at https://go.microsoft.com/fwlink/?LinkId=2086102". Running
# this script is that acceptance. The files stay in the cache and are never copied into the installer. What the
# app carries of Microsoft's is the compiler runtime linked into it (vcruntime, through the static_vcruntime
# crate, so the Visual C++ Redistributable is not needed); the rest of the C runtime is the Universal CRT that
# Windows 10 and 11 ship, which the program imports by name (api-ms-win-crt-*.dll).
#
# CACHES. One directory, SCRIOBH_WIN_CACHE, holds everything worth keeping between runs, the way the runner's Go
# cache is kept: it is not configured by the workflow, it is wherever the runner already persists. When the
# variable is not set the directory is `scriobh-win-cross` beside Go's build cache (so a runner that mounts
# GOCACHE somewhere persistent gets this too; the default container has ~/.cache/go-build, so ~/.cache/scriobh-win-cross).
#   $CACHE/xwin    the Windows SDK and C runtime (about 600 MB for x86_64 only)
#   $CACHE/nsis-3.13  the pinned NSIS, when it had to be built (a minute or two)
#   $CACHE/xdg     XDG_CACHE_HOME, where Tauri keeps the NSIS plugin it downloads from GitHub (Linux)
#   $CACHE/cargo   CARGO_HOME (the crate registry and the cargo-xwin binary) and $CACHE/rustup RUSTUP_HOME, but only
#                  on a machine with no ~/.cargo and no CARGO_HOME set (a fresh CI container); a developer's own
#                  Rust installation is left alone
# The first line of the log says which directory it is and whether it is warm. If the second run on a runner says
# "cold" again, the directory is not persisted and every run downloads the SDK again.
#
# Outbound requests (build time only; the installer and the app make none of these): static.rust-lang.org and
# crates.io, aka.ms and download.visualstudio.microsoft.com (the SDK and runtime), github.com (the NSIS plugin),
# registry.npmjs.org, proxy.golang.org, downloads.sourceforge.net (NSIS, only when it must be built), and apt's mirrors.
set -euo pipefail

# SRC_DIR is the tree to build (default: the tree this script is in), as in linux-deb.sh: release.yml builds a tag's
# tree with the script of the commit it runs on, so the tag need not contain this script. `here` stays the folder
# of this script (verify-windows-installer.sh is its neighbour); the tree's own sidecar.sh builds the server.
root="${SRC_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---- Pins. Change them here and nowhere else. -------------------------------------------------------------------
target="x86_64-pc-windows-msvc"
RUST_TOOLCHAIN="${RUST_TOOLCHAIN:-1.100.0}"   # keep equal to RUST_TOOLCHAIN in .forgejo/workflows/desktop.yml
XWIN_VERSION_PIN="0.23.1"                      # cargo-xwin
NSIS_VERSION="3.13"
NSIS_SRC_SHA256="a8ffe024602d46b6d766f9e1ce30c324ad2a24daeacd3efc2642d436a0c157ac"   # nsis-3.13-src.tar.bz2
NSIS_ZIP_SHA256="ba63dffc4410ee89193e1cb5a41989991bd77c61068da17e3156d136b7b0b3d8"   # nsis-3.13.zip
export XWIN_ARCH="${XWIN_ARCH:-x86_64}"        # not aarch64: it would double the download
export XWIN_SDK_VERSION="${XWIN_SDK_VERSION:-10.0.26100}"
export XWIN_CRT_VERSION="${XWIN_CRT_VERSION:-14.44.17.14}"

version=""
out="dist"
lint=1
force_nsis=0
die() { echo "build-windows-cross: $*" >&2; exit 1; }
say() { echo "build-windows-cross: $*"; }
while [ $# -gt 0 ]; do
  case "$1" in
    --version) version="${2:?--version needs a value}"; shift 2 ;;
    --out) out="${2:?--out needs a directory}"; shift 2 ;;
    --build-nsis) force_nsis=1; shift ;;
    --no-lint) lint=0; shift ;;
    -h|--help) sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
case "$version" in
  *[!A-Za-z0-9._+-]*) die "the version may hold only letters, digits and . _ + -: $version" ;;
esac
start=$SECONDS

# ---- The cache directory -------------------------------------------------------------------------------------------
if [ -z "${SCRIOBH_WIN_CACHE:-}" ]; then
  gocache=""
  if command -v go >/dev/null 2>&1; then gocache="$(go env GOCACHE 2>/dev/null || true)"; fi
  if [ -n "$gocache" ] && [ "$gocache" != "off" ]; then
    SCRIOBH_WIN_CACHE="$(dirname "$gocache")/scriobh-win-cross"
  else
    SCRIOBH_WIN_CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/scriobh-win-cross"
  fi
fi
cache="$SCRIOBH_WIN_CACHE"
mkdir -p "$cache"
export XWIN_CACHE_DIR="${XWIN_CACHE_DIR:-$cache/xwin}"
# Go's cache is found through XDG_CACHE_HOME on Linux, and the next line moves that for Tauri's sake: pin Go's
# own cache (and module cache) where they were, so the server build keeps using the runner's persistent ones.
if command -v go >/dev/null 2>&1; then
  GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
  GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
  export GOCACHE GOMODCACHE
fi
if [ "$(uname -s)" = Linux ]; then export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$cache/xdg}"; fi
if [ -z "${CARGO_HOME:-}" ] && [ -z "${RUSTUP_HOME:-}" ] && [ ! -d "$HOME/.cargo" ]; then
  export CARGO_HOME="$cache/cargo" RUSTUP_HOME="$cache/rustup"
fi
export PATH="${CARGO_HOME:-$HOME/.cargo}/bin:$PATH"
if [ -f "$XWIN_CACHE_DIR/xwin/DONE" ]; then warm=warm; else warm=cold; fi
say "cache $cache ($warm: the Windows SDK $( [ "$warm" = warm ] && echo is || echo is not ) there)"

# ---- Tools -------------------------------------------------------------------------------------------------------
if [ "$(uname -s)" = Darwin ]; then
  # Homebrew's llvm and lld are keg-only.
  for p in /opt/homebrew/opt/llvm/bin /opt/homebrew/opt/lld/bin /usr/local/opt/llvm/bin /usr/local/opt/lld/bin; do
    [ -d "$p" ] && PATH="$p:$PATH"
  done
  export PATH
fi

sevenz() { command -v 7zz 2>/dev/null || command -v 7z 2>/dev/null || true; }
missing_tools() {
  local m=() c
  for c in makensis clang lld-link llvm-ar llvm-rc; do command -v "$c" >/dev/null 2>&1 || m+=("$c"); done
  [ -n "$(sevenz)" ] || m+=("7z")
  echo "${m[*]:-}"
}

# Debian ships the LLVM tools versioned (llvm-rc-14) and puts the plain names in the `llvm` package; if a plain name
# is still missing after the install, link the versioned one from /usr/lib/llvm-*/bin into a directory of our own.
link_llvm_tools() {
  local shim="$cache/llvm-shim" d t
  for t in llvm-ar llvm-rc clang lld-link; do
    command -v "$t" >/dev/null 2>&1 && continue
    for d in /usr/lib/llvm-*/bin; do
      if [ -x "$d/$t" ]; then mkdir -p "$shim"; ln -sf "$d/$t" "$shim/$t"; break; fi
    done
  done
  [ -d "$shim" ] && PATH="$shim:$PATH" && export PATH
  return 0
}

need="$(missing_tools)"
if [ -n "$need" ]; then
  if [ "$(uname -s)" = Linux ] && command -v apt-get >/dev/null 2>&1 && [ "$(id -u)" = 0 ]; then
    say "installing with apt: nsis clang lld llvm p7zip-full (missing: $need)"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y --no-install-recommends nsis clang lld llvm p7zip-full ca-certificates curl file
    link_llvm_tools
    need="$(missing_tools)"
  fi
  [ -z "$need" ] || die "missing: $need. On Debian or Ubuntu: apt install nsis clang lld llvm p7zip-full. On macOS: brew install nsis llvm lld sevenzip"
fi
for c in go node npm python3 curl git; do command -v "$c" >/dev/null 2>&1 || die "$c is not on PATH"; done

# NSIS. Tauri's installer script includes Win\RestartManager.nsh, which NSIS has only since 3.09 or later; ask the
# makensis on this machine to compile that one include (it fails exactly as the real build would), and when it cannot,
# build the pinned release.
nsis_adequate() {
  local d rc=0
  d="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-nsis-probe.XXXXXX")"
  printf '!include "Win\\RestartManager.nsh"\nOutFile "%s/probe.exe"\nSection\nSectionEnd\n' "$d" >"$d/probe.nsi"
  makensis -V1 "$d/probe.nsi" >"$d/log" 2>&1 || rc=$?
  [ "$rc" = 0 ] || sed 's/^/build-windows-cross: nsis probe: /' "$d/log" >&2
  rm -rf "$d"
  return "$rc"
}

check_sha256() { # file hash
  local got
  if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$1" | cut -d' ' -f1)"; else got="$(shasum -a 256 "$1" | cut -d' ' -f1)"; fi
  [ "$got" = "$2" ] || die "$1: SHA-256 is $got, expected $2; not using it"
}

build_pinned_nsis() {
  local home="$cache/nsis-$NSIS_VERSION" work="$cache/nsis-build" base c m=()
  if [ -f "$home/.complete" ]; then return 0; fi
  for c in scons g++ bzip2; do command -v "$c" >/dev/null 2>&1 || m+=("$c"); done
  if [ "$(uname -s)" = Linux ] && [ ! -f /usr/include/zlib.h ]; then m+=("zlib1g-dev"); fi
  if [ "${#m[@]}" -gt 0 ]; then
    if [ "$(uname -s)" = Linux ] && command -v apt-get >/dev/null 2>&1 && [ "$(id -u)" = 0 ]; then
      say "installing with apt to build NSIS: scons g++ zlib1g-dev bzip2 (missing: ${m[*]})"
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -qq
      apt-get install -y --no-install-recommends scons g++ zlib1g-dev bzip2 ca-certificates curl
    else
      die "to build NSIS $NSIS_VERSION these are needed: ${m[*]}"
    fi
  fi
  say "building NSIS $NSIS_VERSION from source (the NSIS here cannot compile Tauri's installer script)"
  # The install prefix is compiled into makensis as the place its stubs and plugins are (and Tauri starts it in a way
  # that does not honour NSISDIR), so it is built for its final place; a build that stops half way has no .complete.
  rm -rf "$work" "$home"
  mkdir -p "$work" "$home/share" "$home/bin"
  base="https://downloads.sourceforge.net/project/nsis/NSIS%203/$NSIS_VERSION"
  curl -fsSL --retry 5 --retry-delay 5 -o "$work/src.tar.bz2" "$base/nsis-$NSIS_VERSION-src.tar.bz2"
  curl -fsSL --retry 5 --retry-delay 5 -o "$work/release.zip" "$base/nsis-$NSIS_VERSION.zip"
  check_sha256 "$work/src.tar.bz2" "$NSIS_SRC_SHA256"
  check_sha256 "$work/release.zip" "$NSIS_ZIP_SHA256"
  tar -xjf "$work/src.tar.bz2" -C "$work"
  # The compiler only; the stubs, plugins and include files come from the release archive (the same recipe Homebrew's
  # makensis formula uses).
  (cd "$work/nsis-$NSIS_VERSION-src" && scons -j"$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)" makensis \
    PREFIX="$home" SKIPMISC=all SKIPPLUGINS=all SKIPSTUBS=all SKIPUTILS=all STRIP=0 VERSION="$NSIS_VERSION")
  cp "$work/nsis-$NSIS_VERSION-src/build/urelease/makensis/makensis" "$home/bin/makensis"
  "$sevenz_cmd" x -y -o"$work/release" "$work/release.zip" >/dev/null
  mv "$work/release/nsis-$NSIS_VERSION" "$home/share/nsis"
  touch "$home/.complete"
  rm -rf "$work"
}

sevenz_cmd="$(sevenz)"
if [ "$force_nsis" = 1 ] || ! nsis_adequate; then
  build_pinned_nsis
  # No NSISDIR: Tauri starts makensis in a way that ignores it, so the probe below must not rely on it either.
  unset NSISDIR
  export PATH="$cache/nsis-$NSIS_VERSION/bin:$PATH"
  nsis_adequate || die "the pinned NSIS $NSIS_VERSION cannot compile Win\\RestartManager.nsh either"
fi

# rustup, the pinned toolchain with clippy, and the Windows target.
if ! command -v rustup >/dev/null 2>&1; then
  say "installing rustup with toolchain $RUST_TOOLCHAIN"
  curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --no-modify-path --profile minimal --default-toolchain "$RUST_TOOLCHAIN"
fi
rustup toolchain install "$RUST_TOOLCHAIN" --profile minimal --component clippy --target "$target" --no-self-update
export RUSTUP_TOOLCHAIN="$RUST_TOOLCHAIN"

# cargo-xwin, pinned. `cargo xwin --version` prints "cargo-xwin-xwin <version>".
if ! cargo xwin --version 2>/dev/null | grep -q " $XWIN_VERSION_PIN\$"; then
  say "installing cargo-xwin $XWIN_VERSION_PIN"
  cargo install --locked cargo-xwin --version "$XWIN_VERSION_PIN"
fi

say "Microsoft's C runtime and Windows SDK are downloaded by cargo-xwin and are under Microsoft's licence: https://go.microsoft.com/fwlink/?LinkId=2086102 (using them is accepting it)"
for t in rustc cargo cargo-xwin makensis clang llvm-ar llvm-rc go node; do
  printf 'build-windows-cross: %-11s %s\n' "$t" "$(command -v "$t")"
done
rustc -vV | sed -n '1p;2p'
makensis -VERSION 2>/dev/null | sed 's/^/build-windows-cross: makensis /' || true
go version
node --version
df -h "$root" | tail -1

# ---- Build -------------------------------------------------------------------------------------------------------
# The server the bundler puts inside the installer: desktop/src-tauri/binaries/scriobh-desktop-server-<triple>.exe
# (the name is the one Tauri's externalBin wants, with the target triple and the extension).
say "server for Windows (GOOS=windows, no cgo)"
VERSION="${version:-${VERSION:-}}" "$root/desktop/scripts/sidecar.sh" "$target"
sidecar="$root/desktop/src-tauri/binaries/scriobh-desktop-server-$target.exe"
[ -f "$sidecar" ] || die "the server was not built: $sidecar"

export CARGO_TERM_COLOR=never CARGO_INCREMENTAL=0
# The C runtime's import libraries name .pdb files from Microsoft's build machines; the linker warns about each (LNK4099,
# hundreds of lines) and the warning means nothing here. cargo-xwin passes RUSTFLAGS on to the target.
export RUSTFLAGS="${RUSTFLAGS:+$RUSTFLAGS }-C link-arg=/IGNORE:4099"
if [ "$lint" = 1 ]; then
  say "clippy for $target (the Windows-only code is compiled here)"
  (cd "$root/desktop/src-tauri" && cargo xwin clippy --locked --release --target "$target" --all-targets -- -D warnings)
fi

say "Tauri command line"
(cd "$root/desktop" && npm ci --no-audit --no-fund)

say "tauri build for $target with cargo-xwin (NSIS only: an .msi needs WiX, which only runs on Windows)"
(cd "$root/desktop" && node node_modules/@tauri-apps/cli/tauri.js build --runner cargo-xwin --target "$target" --bundles nsis)

targetdir="${CARGO_TARGET_DIR:-$root/desktop/src-tauri/target}"
bundle="$targetdir/$target/release/bundle/nsis"
shopt -s nullglob
installers=("$bundle"/*-setup.exe)
[ "${#installers[@]}" -eq 1 ] || die "expected one installer in $bundle, found ${#installers[@]}"
installer="${installers[0]}"
app="$targetdir/$target/release/scriobh-desktop.exe"
[ -f "$app" ] || die "the app was not built: $app"

# ---- Look inside, without Windows ----------------------------------------------------------------------------------
"$here/verify-windows-installer.sh" "$installer" "$app" "$sidecar"

# ---- Output --------------------------------------------------------------------------------------------------------
appversion="$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$root/desktop/src-tauri/tauri.conf.json" | head -1)"
if [ -n "$version" ] && [ "${version#v}" != "$appversion" ]; then
  say "WARNING: the release is $version but the app inside says $appversion (desktop/src-tauri/tauri.conf.json, Cargo.toml and package.json). The file is named for the release; the installer's own version, shown in Windows' installed apps list, is $appversion."
fi
name="scriobh-desktop-${version:-$appversion}-windows-amd64-setup.exe"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
cp "$installer" "$out/$name"
if command -v sha256sum >/dev/null 2>&1; then sum() { sha256sum "$@"; }; else sum() { shasum -a 256 "$@"; }; fi
(cd "$out" && sum "$name" >"$name.sha256")
# Join the release's checksum list when there is one (scripts/release-dist.sh writes it), keeping its order: by name.
if [ -f "$out/SHA256SUMS.txt" ]; then
  (cd "$out" && { grep -v -F "  $name" SHA256SUMS.txt || true; cat "$name.sha256"; } | LC_ALL=C sort -k2 >SHA256SUMS.txt.new && mv SHA256SUMS.txt.new SHA256SUMS.txt)
fi
say "done in $((SECONDS - start)) s"
ls -l "$out/$name"
cat "$out/$name.sha256"
