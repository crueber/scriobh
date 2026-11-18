#!/usr/bin/env bash
# The Linux steps that build, package and check the desktop app's .deb, one named step at a time. Two workflows
# call them: desktop.yml (on demand, with the checks) and release.yml (for a release tag, without the format, lint
# and test steps, which the push gate and the desktop run have already made). Debian or Ubuntu; root, or sudo for
# the apt steps.
#
#   desktop/scripts/linux-deb.sh deps toolchain sidecar cli package install launch
#
#   deps       apt: what the Tauri shell builds with
#   toolchain  rustup, the pinned toolchain (RUST_TOOLCHAIN), onto PATH and, in a workflow, GITHUB_PATH
#   versions   print the tool versions and the disk space
#   sidecar    the production server the bundle holds (desktop/scripts/sidecar.sh; VERSION is its version)
#   fmt        cargo fmt --check
#   lint       cargo clippy, warnings are errors
#   test       desktop/scripts/test.sh (the stand-in server's tests and the real server's)
#   cli        the pinned Tauri command line (npm ci in desktop/)
#   compile    build the app without packaging
#   package    build the .deb (TAURI_BUILD_CONFIG, when set, stamps the release's version and may ask for update artefacts)
#   sign       sign the .deb with the update key, so the manifest can list it (docs/UPDATES.md: the app only ever says that
#              a newer .deb exists); needs TAURI_SIGNING_PRIVATE_KEY, and does nothing when TAURI_BUILD_CONFIG asks for none
#   name       with VERSION a release tag: name the .deb and the AppImage for the tag (see below); package and appimage do
#              it for their own file, so this step is for a .deb made some other way (the GitHub release's Linux leg)
#   appimage   build the AppImage and open it (UNVERIFIED: never run on the runner); its packaging tools come from the
#              pinned, hash-checked downloads below, put where the Tauri bundler looks first; a result that does not
#              unpack, or lacks the app and its server, is deleted and fails the step
#   install    apt-get install the .deb and check what it put on disk (desktop/scripts/deb-install.sh)
#   launch     run the installed app headless under xvfb and a session bus and quit it (launch-check.sh)
#
# FILE NAMES. The Tauri bundler names its files for the app's own version (Scriobh_2.2.0_amd64.deb, from
# desktop/src-tauri/tauri.conf.json). With VERSION a release tag (v followed by a digit) the published files are named
# for the tag without its v (Scriobh_2.2.1_amd64.deb), as build-windows-cross.sh and build-macos.sh name theirs, and
# a WARNING says so when the tag is not the version the packages carry. A release stamps the tag's version into them
# (TAURI_BUILD_CONFIG), so they agree; without it what is inside the package (the .deb's control file) is the app's own.
# Without VERSION, or with something that is not a tag, the bundler's names are left alone.
#
# TAURI_BUILD_CONFIG is the absolute path of a JSON file that every `tauri build` here merges over tauri.conf.json with
# --config (`desktop/scripts/updater-manifest.py config` writes it): the release's version, so that the package, the file
# name and the update signature all carry the tag's, and for a release with update artefacts the update source. Without
# it the app is built as it is committed and makes no update artefacts.
#
# SRC_DIR is the tree to build (default: the tree this script is in). release.yml builds a tag's tree with the
# script of the commit it runs on, so the tag need not contain this script; the tree's own sidecar.sh,
# deb-install.sh and launch-check.sh are the ones that run.
set -euo pipefail

root="${SRC_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
cd "$root"
export RUST_TOOLCHAIN="${RUST_TOOLCHAIN:-1.100.0}"
export PATH="$HOME/.cargo/bin:$PATH"

# The update key reaches ONLY the bundler and the signing command: it is taken out of the environment here, before npm and Cargo
# run (their lifecycle and build scripts would otherwise see it), and handed to `tauri bundle` and `tauri signer sign` alone.
signing_key="${TAURI_SIGNING_PRIVATE_KEY:-}"
signing_password="${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}"
unset TAURI_SIGNING_PRIVATE_KEY TAURI_SIGNING_PRIVATE_KEY_PASSWORD

as_root() { if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi; }
tauri() { (cd desktop && node node_modules/@tauri-apps/cli/tauri.js "$@"); }

# Build and bundle with the release's configuration, when there is one: `tauri build --no-bundle` (compiles, no key in its
# environment) then `tauri bundle` (no Cargo build, no npm script; the key is given to this command alone, when update artefacts
# are asked for). Every build in this file goes through it, so the .deb and the AppImage of one release carry one version.
tauri_build() {
  local bundles=("$@") cfg=() key=()
  if [ -n "${TAURI_BUILD_CONFIG:-}" ]; then
    case "$TAURI_BUILD_CONFIG" in /*) ;; *) echo "linux-deb: TAURI_BUILD_CONFIG must be an absolute path: $TAURI_BUILD_CONFIG" >&2; exit 1 ;; esac
    [ -f "$TAURI_BUILD_CONFIG" ] || { echo "linux-deb: TAURI_BUILD_CONFIG names no file: $TAURI_BUILD_CONFIG" >&2; exit 1; }
    cfg=(--config "$TAURI_BUILD_CONFIG")
    if wants_updater_artefacts; then
      [ -n "$signing_key" ] || { echo "linux-deb: TAURI_BUILD_CONFIG asks for update artefacts and TAURI_SIGNING_PRIVATE_KEY is not set" >&2; exit 1; }
      key=("TAURI_SIGNING_PRIVATE_KEY=$signing_key" "TAURI_SIGNING_PRIVATE_KEY_PASSWORD=$signing_password")
    fi
  fi
  tauri build --no-bundle ${cfg[@]+"${cfg[@]}"}
  (cd desktop && env ${key[@]+"${key[@]}"} node node_modules/@tauri-apps/cli/tauri.js bundle "${bundles[@]}" ${cfg[@]+"${cfg[@]}"})
}

# True when the release configuration asks for signed update artefacts.
wants_updater_artefacts() {
  [ -n "${TAURI_BUILD_CONFIG:-}" ] && [ -f "$TAURI_BUILD_CONFIG" ] && grep -q '"createUpdaterArtifacts": *true' "$TAURI_BUILD_CONFIG"
}

# The version the packages carry: the release's, from TAURI_BUILD_CONFIG, else the app's own (tauri.conf.json).
app_version() {
  local v=""
  if [ -n "${TAURI_BUILD_CONFIG:-}" ] && [ -f "$TAURI_BUILD_CONFIG" ]; then
    v="$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$TAURI_BUILD_CONFIG" | head -1)"
  fi
  [ -n "$v" ] || v="$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' desktop/src-tauri/tauri.conf.json | head -1)"
  printf '%s' "$v"
}

step_deps() {
  export DEBIAN_FRONTEND=noninteractive
  as_root apt-get update
  as_root apt-get install -y --no-install-recommends \
    libwebkit2gtk-4.1-dev libgtk-3-dev libayatana-appindicator3-dev librsvg2-dev \
    build-essential curl file libssl-dev libxdo-dev libdbus-1-dev pkg-config \
    ca-certificates git xdg-utils
}

step_toolchain() {
  curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain "$RUST_TOOLCHAIN" --component clippy --component rustfmt
  [ -z "${GITHUB_PATH:-}" ] || echo "$HOME/.cargo/bin" >>"$GITHUB_PATH"
}

step_versions() { rustc -vV; cargo --version; go version; node --version; df -h . | tail -1; }
step_sidecar() { desktop/scripts/sidecar.sh; }
step_fmt() { (cd desktop/src-tauri && cargo fmt --check); }
step_lint() { (cd desktop/src-tauri && cargo clippy --locked --all-targets -- -D warnings); }
# The real-server tests ask the binary the sidecar step built (test.sh sets SCRIOBH_REAL_SERVER and refuses to
# run without it), and the stand-in's tests run beside them.
step_test() { desktop/scripts/test.sh; }
step_cli() { (cd desktop && npm ci --no-audit --no-fund); }
step_compile() { tauri build --no-bundle; }  # no packages, so no release configuration

step_package() {
  if wants_updater_artefacts; then
    echo "the release configuration asks for update artefacts; they are made and signed"
  else
    echo "building without update artefacts"
  fi
  tauri_build --bundles deb
  release_name desktop/src-tauri/target/release/bundle/deb deb
  ls -l desktop/src-tauri/target/release/bundle/deb/
}

# Signs the .deb(s) of the bundle folder with the update key, bound to the app's version (`--app-version`: the same
# trusted comment the bundler writes for the other packages, which the app checks against the manifest). The Tauri
# bundler makes update artefacts for the AppImage only; the manifest lists the .deb so that the app can say a newer
# one exists, and for that it needs the signature. The app never installs a .deb.
step_sign() {
  wants_updater_artefacts || { echo "linux-deb: no update artefacts are asked for; the .deb is not signed"; return 0; }
  local f n=0 v
  v="$(app_version)"
  for f in desktop/src-tauri/target/release/bundle/deb/*.deb; do
    [ -f "$f" ] || continue
    rm -f "$f.sig"
    [ -n "$signing_key" ] || { echo "linux-deb: update artefacts are asked for and TAURI_SIGNING_PRIVATE_KEY is not set" >&2; exit 1; }
    (cd desktop && env TAURI_SIGNING_PRIVATE_KEY="$signing_key" TAURI_SIGNING_PRIVATE_KEY_PASSWORD="$signing_password" node node_modules/@tauri-apps/cli/tauri.js signer sign --app-version "$v" "$root/$f")
    [ -s "$f.sig" ] || { echo "linux-deb: signing $f made no signature" >&2; exit 1; }
    n=$((n + 1))
    echo "linux-deb: $f is signed for version $v"
  done
  [ "$n" -gt 0 ] || { echo "linux-deb: no .deb to sign" >&2; exit 1; }
}

# release_name DIR EXT: with VERSION a release tag, rename the bundler's Scriobh_<app version>_amd64.EXT in DIR (and
# its updater signature, .EXT.sig, when there is one) to Scriobh_<tag without v>_amd64.EXT. A name that is already the
# tag's is left alone, so the step can run twice. A file in DIR named otherwise is a bundler change this script has
# not been taught, and fails the step instead of publishing under a name nobody chose.
release_name() {
  local dir="$1" ext="$2" version product name_version want f base new
  case "${VERSION:-}" in v[0-9]*) ;; *) return 0 ;; esac
  case "$VERSION" in *[!A-Za-z0-9._+-]*) echo "linux-deb: VERSION may hold only letters, digits and . _ + -: $VERSION" >&2; exit 1 ;; esac
  version="$(app_version)"
  product="$(sed -n 's/^ *"productName": *"\([^"]*\)".*/\1/p' desktop/src-tauri/tauri.conf.json | head -1)"
  [ -n "$version" ] && [ -n "$product" ] || { echo "linux-deb: could not read the version and productName from desktop/src-tauri/tauri.conf.json" >&2; exit 1; }
  name_version="${VERSION#v}"
  if [ "$name_version" != "$version" ]; then
    echo "linux-deb: WARNING: the release is $VERSION but the app inside says $version (desktop/src-tauri/tauri.conf.json, Cargo.toml and package.json). The files are named for the release; the version inside the package is $version."
  fi
  shopt -s nullglob
  for f in "$dir"/*."$ext"; do
    base="$(basename "$f")"
    want="${product}_${name_version}_amd64.$ext"
    [ "$base" != "$want" ] || continue
    [ "$base" = "${product}_${version}_amd64.$ext" ] || { echo "linux-deb: $f is not named ${product}_${version}_amd64.$ext, so it is not renamed for the release; the bundler's naming has changed" >&2; exit 1; }
    new="$dir/$want"
    mv -- "$f" "$new"
    [ ! -f "$f.sig" ] || mv -- "$f.sig" "$new.sig"
    echo "linux-deb: $base is published as $want"
  done
  shopt -u nullglob
}

# For a .deb that something else built (release.yml on GitHub runs the Tauri command line itself): the same names.
step_name() {
  release_name desktop/src-tauri/target/release/bundle/deb deb
  release_name desktop/src-tauri/target/release/bundle/appimage AppImage
}

# THE APPIMAGE. `tauri build --bundles appimage` downloads, at build time, into ~/.cache/tauri (tauri-bundler 2.10.5,
# which @tauri-apps/cli 2.12.5 uses; it skips a download when the file is already there):
#   AppRun-x86_64                           github.com/tauri-apps/binary-releases, tag apprun-old
#   linuxdeploy-07333c6-x86_64.AppImage     the same repository, tag linuxdeploy-07333c6
#   linuxdeploy-plugin-appimage.AppImage    github.com/linuxdeploy/linuxdeploy-plugin-appimage, tag `continuous`: a
#                                           MOVING tag, optional (the bundler falls back to a built-in copy)
# The bundler checks none of them against a hash, and the third moves. So they are fetched here and checked against
# the SHA-256 below before the bundler runs, and put in the cache under the names it looks for; a changed file
# fails the step instead of being bundled. The GTK and GStreamer plugin scripts are inside the bundler. It runs
# linuxdeploy with APPIMAGE_EXTRACT_AND_RUN=1, so no FUSE is needed. When @tauri-apps/cli is upgraded, read the
# bundler's src/bundle/linux/appimage/linuxdeploy.rs again: its tool names and commit hash are what these lines copy.
# x86_64 only (the runner's architecture). NO_STRIP: linuxdeploy's own strip chokes on newer binaries' sections.
appimage_tool() { # appimage_tool URL SHA256 CACHE_NAME
  local dir="${XDG_CACHE_HOME:-$HOME/.cache}/tauri" tmp
  mkdir -p "$dir"
  if [ -f "$dir/$3" ] && echo "$2  $dir/$3" | sha256sum -c --status - 2>/dev/null; then return 0; fi
  tmp="$(mktemp)"
  curl --proto '=https' --tlsv1.2 -fsSL --retry 3 -o "$tmp" "$1"
  echo "$2  $tmp" | sha256sum -c --status - || { echo "linux-deb: $1 is not the file this script pins (sha256 $2)" >&2; rm -f "$tmp"; exit 1; }
  install -m 0755 "$tmp" "$dir/$3"; rm -f "$tmp"
}

step_appimage() {
  [ "$(uname -m)" = x86_64 ] || { echo "linux-deb: the AppImage step pins x86_64 tools only" >&2; exit 1; }
  local gh=https://github.com dir=desktop/src-tauri/target/release/bundle/appimage
  rm -rf "$dir"   # nothing from an earlier run may be mistaken for this build's
  appimage_tool "$gh/tauri-apps/binary-releases/releases/download/apprun-old/AppRun-x86_64" \
    f30140a43a0a59e46db21bdefdf749b9e9f2c6946e92afabbacf98b8ae73fb4f AppRun-x86_64
  appimage_tool "$gh/tauri-apps/binary-releases/releases/download/linuxdeploy-07333c6/linuxdeploy-x86_64.AppImage" \
    36a2d7e274d12e1050d0e9ecfe11d339ed54720b2bec464c286d53f8b07f5c62 linuxdeploy-07333c6-x86_64.AppImage
  appimage_tool "$gh/linuxdeploy/linuxdeploy-plugin-appimage/releases/download/continuous/linuxdeploy-plugin-appimage-x86_64.AppImage" \
    49d6a17160675a6bd1781699aae6bdf7692d98552e02a3671d2183d10547842e linuxdeploy-plugin-appimage.AppImage
  NO_STRIP=true tauri_build --bundles appimage
  ls -l "$dir"
  appimage_examine "$dir"
  release_name "$dir" AppImage
}

# An AppImage is opened before it is published, not only built: `--appimage-extract` unpacks it without FUSE, and
# the unpacked tree must hold the app and the bundled server and a desktop entry; the server must run and report
# the version (VERSION, when set). A file that fails is deleted so that nothing after this step can pick it up.
appimage_examine() {
  local dir="$1" img work app server ver
  local imgs=()
  imgs=("$dir"/*.AppImage)
  [ -e "${imgs[0]}" ] && [ ${#imgs[@]} -eq 1 ] || { echo "linux-deb: expected exactly one AppImage in $dir, found ${#imgs[@]}" >&2; rm -f "$dir"/*.AppImage; exit 1; }
  img="$(cd "$dir" && pwd)/$(basename "${imgs[0]}")"
  work="$(mktemp -d)"
  ( cd "$work" && "$img" --appimage-extract >/dev/null ) || { echo "linux-deb: $img does not unpack" >&2; rm -f "$img"; rm -rf "$work"; exit 1; }
  app="$(find "$work/squashfs-root" -type f -name scriobh-desktop | head -1)"
  server="$(find "$work/squashfs-root" -type f -name scriobh-desktop-server | head -1)"
  if [ -z "$app" ] || [ -z "$server" ] || [ -z "$(find "$work/squashfs-root" -maxdepth 1 -name '*.desktop' | head -1)" ] || [ ! -x "$server" ]; then
    echo "linux-deb: the AppImage lacks the app ($app), its server ($server) or a desktop entry:" >&2
    ( cd "$work/squashfs-root" && find . -maxdepth 4 | sort | head -60 ) >&2
    rm -f "$img"; rm -rf "$work"; exit 1
  fi
  ver="$("$server" --version)" || { echo "linux-deb: the AppImage's server does not run" >&2; rm -f "$img"; rm -rf "$work"; exit 1; }
  echo "linux-deb: the AppImage unpacks; app $(basename "$app"), server $(basename "$server") reports '$ver'"
  if [ -n "${VERSION:-}" ] && [ "$ver" != "$VERSION" ]; then
    echo "linux-deb: the AppImage's server reports '$ver', not '$VERSION'" >&2; rm -f "$img"; rm -rf "$work"; exit 1
  fi
  rm -rf "$work"
}

# The package is installed and started, not only built. apt resolves its dependencies; the launcher, the bundled
# server and the desktop entry must be on disk. The -dev packages the build needed are still installed, so this
# does not prove the Depends line is enough on a clean machine.
step_install() { desktop/scripts/deb-install.sh desktop/src-tauri/target/release/bundle/deb; }

# A display and a session bus for the installed app; python3 and procps are what the launch check calls. It starts
# /usr/bin/scriobh-desktop under xvfb-run and dbus-run-session with a throwaway HOME (no keychain, so the first-run
# key file fallback), waits for the bundled server to bind and answer /healthz, then sends the shell SIGTERM and
# requires that the server is gone and the data directory's lock is released. Every wait is bounded.
step_launch() {
  export DEBIAN_FRONTEND=noninteractive
  as_root apt-get install -y --no-install-recommends xvfb xauth dbus procps python3 curl fonts-dejavu-core
  desktop/scripts/launch-check.sh /usr/bin/scriobh-desktop
}

[ $# -gt 0 ] || { sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//' >&2; exit 2; }
for s in "$@"; do
  case "$s" in
    deps|toolchain|versions|sidecar|fmt|lint|test|cli|compile|package|sign|name|appimage|install|launch)
      echo "linux-deb: $s"
      "step_$s" ;;
    *) echo "linux-deb: unknown step: $s" >&2; exit 2 ;;
  esac
done
