#!/usr/bin/env bash
# Builds the macOS desktop app (.app inside a .dmg) and puts the packages in dist/ with clear names.
# Runs on a Mac, by hand or from a CI job; it is the one place the macOS build is described, so the Forgejo job
# (.forgejo/workflows/desktop.yml), a release workflow and the GitHub template all call it.
#
#   desktop/scripts/build-macos.sh              # for this Mac's own architecture
#   desktop/scripts/build-macos.sh arm64        # Apple silicon   (also: aarch64, aarch64-apple-darwin)
#   desktop/scripts/build-macos.sh x86_64       # Intel           (also: x64, intel, x86_64-apple-darwin)
#   desktop/scripts/build-macos.sh universal    # one app with both (also: universal-apple-darwin)
#   desktop/scripts/build-macos.sh all          # arm64, then x86_64 (two separate packages)
#
# Output, for version 2.2.0 (the version is read from desktop/src-tauri/tauri.conf.json):
#   dist/Scriobh_2.2.0_aarch64.dmg   dist/Scriobh_2.2.0_x86_64.dmg   dist/Scriobh_2.2.0_universal.dmg
# With VERSION set to a release tag (v2.2.1) the files are named for it instead (Scriobh_2.2.1_aarch64.dmg), and a
# WARNING says so when the tag is not the app's version.
# and, only when update artefacts are being made (TAURI_BUILD_CONFIG asks for them and the update key is present),
# dist/Scriobh_2.2.0_<arch>.app.tar.gz and its .sig.
#
# What it does: makes sure the Rust target is installed (user-level, `rustup target add`), installs the pinned
# Tauri command line with npm ci, builds the production server for the target (desktop/scripts/sidecar.sh;
# Go cross-compiles, so an Apple-silicon Mac builds the Intel server and the reverse), runs the Tauri bundler,
# then checks what it made (bundle identifier, version, architectures of the app and its bundled server, the
# signature, and that the .dmg mounts and holds the app) and copies the .dmg into dist/.
#
# Signing, decided by the environment and never by a flag:
#   - APPLE_SIGNING_IDENTITY set to a Developer ID name: the bundler signs with that identity, which is already in
#     the keychain (the runbook's way) or is imported from APPLE_CERTIFICATE (+ APPLE_CERTIFICATE_PASSWORD) into a
#     temporary keychain; with APPLE_API_ISSUER, APPLE_API_KEY and APPLE_API_KEY_PATH (or APPLE_API_KEY_P8, the
#     key's text, which this script writes to a temporary file) it also notarises. Untested: no certificate exists
#     yet (docs/p8-notes/release-runbook.md).
#   - APPLE_SIGNING_IDENTITY empty or "-": the bundle is signed AD HOC (identity "-"), whatever else is set. This is not optional decoration: the compiler's own
#     signature covers only the main binary, so the bundle as a whole does not verify ("code has no resources
#     but signature indicates they must be present") and a downloaded copy would be reported as damaged rather
#     than as from an unidentified developer. An ad-hoc signature makes the bundle consistent; Gatekeeper still
#     asks the person to allow it (docs/DESKTOP.md, "macOS: building and Gatekeeper").
#   - The update key (TAURI_SIGNING_PRIVATE_KEY and its password) is read at the top of this script, removed from the
#     environment, and given to the bundling command only.
#   - TAURI_BUILD_CONFIG, the path of a JSON file for `tauri build --config` (written by `updater-manifest.py config`,
#     docs/UPDATES.md): it stamps the release's version into the app and, when it asks for update artefacts, carries
#     the update source. Those need TAURI_SIGNING_PRIVATE_KEY (+ _PASSWORD) and are signed with it.
#
# Environment:
#   SRC_DIR            the tree to build, when it is not the one this script is in (a tag's checkout)
#   TAURI_BUILD_CONFIG an absolute path to the JSON file `tauri build --config` merges over tauri.conf.json: the version
#                      of the tag, and for a release with update artefacts the update source; without it the app's own
#                      configuration is built as it is (and no update artefacts are made)
#   DIST_DIR           where the .dmg files go (default: dist/ in that tree)
#   VERSION            the release tag, as written: stamped into the bundled server (sidecar.sh; default `git describe`)
#                      and used, without its leading v, in the names of the files in dist/
#   CARGO_TARGET_DIR   where Cargo puts its build output (default desktop/src-tauri/target; a release build of
#                      the shell is about 1 GB per architecture)
#   SKIP_VERIFY=1      do not run the checks after the build
#   CI                 set to "true" here unless already set: the .dmg tool otherwise tells Finder (AppleScript)
#                      to lay out the window, which waits for a permission prompt nobody can answer on a
#                      runner or over ssh and times out after minutes; with CI set the .dmg is plain (no
#                      custom window layout, nothing else differs)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# SRC_DIR is the tree to build (default: the tree this script is in), as in linux-deb.sh: release.yml builds a tag's
# tree with the script of the commit it runs on, so the tag need not contain this script. The tree's own sidecar.sh
# builds the server; dist/ is then the tree's (DIST_DIR moves it).
root="${SRC_DIR:-$(cd "$here/../.." && pwd)}"
desktop="$root/desktop"
dist="${DIST_DIR:-$root/dist}"
target_dir="${CARGO_TARGET_DIR:-$desktop/src-tauri/target}"
case "$target_dir" in /*) ;; *) target_dir="$PWD/$target_dir" ;; esac

say() { echo "build-macos: $*"; }
die() { echo "build-macos: FAIL: $*" >&2; exit 1; }

# The update key reaches ONE command: the bundler's. It is taken out of the environment here, before npm, Go and Cargo run
# (their lifecycle and build scripts would otherwise see it), and handed to `tauri bundle` alone.
signing_key="${TAURI_SIGNING_PRIVATE_KEY:-}"
signing_password="${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}"
unset TAURI_SIGNING_PRIVATE_KEY TAURI_SIGNING_PRIVATE_KEY_PASSWORD

[ "$(uname -s)" = "Darwin" ] || die "this builds the macOS app and has to run on a Mac (this is $(uname -s))"
for tool in node npm go rustc cargo lipo codesign hdiutil plutil; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not on PATH"
done

version="$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$desktop/src-tauri/tauri.conf.json" | head -1)"
[ -n "$version" ] || die "could not read the version from tauri.conf.json"
# A release stamps its tag's version into the app with TAURI_BUILD_CONFIG (docs/UPDATES.md): it is the version the
# bundle, the checks below and the update signatures carry, whatever tauri.conf.json says.
if [ -n "${TAURI_BUILD_CONFIG:-}" ]; then
  case "$TAURI_BUILD_CONFIG" in /*) ;; *) die "TAURI_BUILD_CONFIG must be an absolute path: $TAURI_BUILD_CONFIG" ;; esac
  [ -f "$TAURI_BUILD_CONFIG" ] || die "TAURI_BUILD_CONFIG names no file: $TAURI_BUILD_CONFIG"
  stamped="$(plutil -extract version raw -o - "$TAURI_BUILD_CONFIG" 2>/dev/null || true)"
  [ -z "$stamped" ] || version="$stamped"
fi
product="$(sed -n 's/^ *"productName": *"\([^"]*\)".*/\1/p' "$desktop/src-tauri/tauri.conf.json" | head -1)"
identifier="$(sed -n 's/^ *"identifier": *"\([^"]*\)".*/\1/p' "$desktop/src-tauri/tauri.conf.json" | head -1)"
main_bin="$(sed -n 's/^ *"mainBinaryName": *"\([^"]*\)".*/\1/p' "$desktop/src-tauri/tauri.conf.json" | head -1)"
sidecar_name="scriobh-desktop-server"

# The version in the file names: the release (VERSION, a tag as written, without its leading v) when one is given, else
# the app's own version. A tag that is not the app's version (v2.2.1 on a tree whose tauri.conf.json says 2.2.0) must
# not publish Scriobh_2.2.0_*.dmg under it, so the name follows the tag and this says so, as build-windows-cross.sh does.
# What is inside the app (the bundle's version, which the checks below compare) is the stamped version when
# TAURI_BUILD_CONFIG carries one (a release), and otherwise the app's own: tauri.conf.json's.
name_version="$version"
if [ -n "${VERSION:-}" ]; then
  case "$VERSION" in *[!A-Za-z0-9._+-]*) die "VERSION may hold only letters, digits and . _ + -: $VERSION" ;; esac
  case "$VERSION" in v[0-9]*) name_version="${VERSION#v}" ;; *) name_version="$VERSION" ;; esac
  case "$VERSION" in
    v[0-9]*) [ "$name_version" = "$version" ] || say "WARNING: the release is $VERSION but the app inside says $version (desktop/src-tauri/tauri.conf.json, Cargo.toml and package.json). The file is named for the release; the version shown in the app's bundle is $version." ;;
  esac
fi

host_arch="$(uname -m)"
case "${1:-}" in
  "")                                         req="$host_arch" ;;
  arm64|aarch64|aarch64-apple-darwin)         req=arm64 ;;
  x86_64|x64|intel|amd64|x86_64-apple-darwin) req=x86_64 ;;
  universal|universal-apple-darwin)           req=universal ;;
  all)                                        req=all ;;
  *) die "unknown architecture '$1' (use arm64, x86_64, universal or all)" ;;
esac
[ "$req" = "all" ] && { SRC_DIR="$root" "$0" arm64 && SRC_DIR="$root" "$0" x86_64; exit $?; }

case "$req" in
  arm64)     triple=aarch64-apple-darwin; label=aarch64; want_archs="arm64" ;;
  x86_64)    triple=x86_64-apple-darwin;  label=x86_64;  want_archs="x86_64" ;;
  universal) triple=universal-apple-darwin; label=universal; want_archs="x86_64 arm64" ;;
esac
[ "$triple" = universal-apple-darwin ] && parts="aarch64-apple-darwin x86_64-apple-darwin" || parts="$triple"

export CI="${CI:-true}"
started=$(date +%s)

# The Rust standard library for each target. A user-level install into ~/.rustup; skipped when rustup is not
# what manages the toolchain (a distro or Homebrew Rust has to be given the targets another way).
if command -v rustup >/dev/null 2>&1; then
  for t in $parts; do
    rustup target list --installed 2>/dev/null | grep -qx "$t" || { say "rustup target add $t"; rustup target add "$t"; }
  done
fi

say "Tauri command line (npm ci)"
(cd "$desktop" && npm ci --no-audit --no-fund)

# The server that goes inside the app. For a universal app Tauri looks for a sidecar named for the universal
# triple, so the two builds are joined with lipo.
for t in $parts; do "$desktop/scripts/sidecar.sh" "$t"; done
if [ "$triple" = universal-apple-darwin ]; then
  bins="$desktop/src-tauri/binaries"
  lipo -create -output "$bins/$sidecar_name-universal-apple-darwin" \
    "$bins/$sidecar_name-aarch64-apple-darwin" "$bins/$sidecar_name-x86_64-apple-darwin"
  say "sidecar: $bins/$sidecar_name-universal-apple-darwin ($(lipo -archs "$bins/$sidecar_name-universal-apple-darwin"))"
fi

# Signing: a Developer ID identity when one is given, ad hoc otherwise (see the header).
keydir=""
cleanup() { [ -z "$keydir" ] || rm -rf "$keydir"; }
trap cleanup EXIT
if [ -n "${APPLE_SIGNING_IDENTITY:-}" ] && [ "${APPLE_SIGNING_IDENTITY}" != "-" ]; then
  # An identity alone is honoured: the certificate may already be in the keychain (docs/p8-notes/release-runbook.md),
  # or APPLE_CERTIFICATE (+ _PASSWORD) lets the bundler import it into a temporary one.
  say "signing with '${APPLE_SIGNING_IDENTITY}'"
  if [ -n "${APPLE_API_KEY_P8:-}" ] && [ -n "${APPLE_API_KEY:-}" ]; then
    keydir="$(mktemp -d)"
    printf '%s' "$APPLE_API_KEY_P8" > "$keydir/AuthKey_${APPLE_API_KEY}.p8"
    chmod 600 "$keydir/AuthKey_${APPLE_API_KEY}.p8"
    export APPLE_API_KEY_PATH="$keydir/AuthKey_${APPLE_API_KEY}.p8"
  fi
else
  say "no Developer ID certificate: signing ad hoc (identity '-'), no notarisation"
  unset APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_API_ISSUER APPLE_API_KEY APPLE_API_KEY_PATH APPLE_ID APPLE_PASSWORD APPLE_TEAM_ID
  export APPLE_SIGNING_IDENTITY="-"
fi
unset APPLE_API_KEY_P8

extra=()
bundle_env=()
if [ -n "${TAURI_BUILD_CONFIG:-}" ]; then
  extra=(--config "$TAURI_BUILD_CONFIG")
  if [ "$(plutil -extract bundle.createUpdaterArtifacts raw -o - "$TAURI_BUILD_CONFIG" 2>/dev/null || true)" = "true" ]; then
    [ -n "$signing_key" ] || die "TAURI_BUILD_CONFIG asks for update artefacts and TAURI_SIGNING_PRIVATE_KEY is not set"
    bundle_env=("TAURI_SIGNING_PRIVATE_KEY=$signing_key" "TAURI_SIGNING_PRIVATE_KEY_PASSWORD=$signing_password")
    say "update artefacts will be made and signed (version $version)"
  fi
fi

say "building $product $version for $triple"
export CARGO_TARGET_DIR="$target_dir"
# Compiled without the key, then bundled (and signed) with it: `tauri bundle` runs no Cargo build and no npm script.
(cd "$desktop" && node node_modules/@tauri-apps/cli/tauri.js build --target "$triple" --no-bundle ${extra[@]+"${extra[@]}"})
(cd "$desktop" && env ${bundle_env[@]+"${bundle_env[@]}"} node node_modules/@tauri-apps/cli/tauri.js bundle --target "$triple" --bundles app,dmg ${extra[@]+"${extra[@]}"})

bundle="$target_dir/$triple/release/bundle"
app="$bundle/macos/$product.app"
# The bundler names the file for its own spelling of the architecture (aarch64, x64, universal); dist/ always
# uses the target triple's architecture, so an Intel package is ..._x86_64.dmg.
dmg=""
for f in "$bundle/dmg/${product}_${version}_"*.dmg; do [ -f "$f" ] && dmg="$f"; done
[ -d "$app" ] || die "no $app after the build"
[ -f "$dmg" ] || die "no .dmg in $bundle/dmg after the build"

verify() {
  local plist="$app/Contents/Info.plist" got a
  say "checking $app"
  got="$(plutil -extract CFBundleIdentifier raw "$plist")"
  [ "$got" = "$identifier" ] || die "CFBundleIdentifier is $got, expected $identifier"
  got="$(plutil -extract CFBundleShortVersionString raw "$plist")"
  [ "$got" = "$version" ] || die "CFBundleShortVersionString is $got, expected $version"
  for f in "$app/Contents/MacOS/$main_bin" "$app/Contents/MacOS/$sidecar_name"; do
    [ -x "$f" ] || die "$f is missing or not executable"
    got="$(lipo -archs "$f" | tr ' ' '\n' | sort | tr '\n' ' ')"
    a="$(echo "$want_archs" | tr ' ' '\n' | sort | tr '\n' ' ')"
    [ "$got" = "$a" ] || die "$f has architectures [$got], expected [$a]"
  done
  codesign --verify --deep --strict "$app" || die "the signature of $app does not verify"
  say "  identifier $identifier, version $version, architectures: $(lipo -archs "$app/Contents/MacOS/$main_bin"), signature verifies"
  say "checking $dmg"
  local mnt out
  mnt="$(mktemp -d)"
  # The .dmg carries the licence text (bundle.licenseFile), which hdiutil asks to have agreed to; answer it.
  out="$(printf 'Y\n' | hdiutil attach -nobrowse -readonly -noautoopen -mountpoint "$mnt" "$dmg" 2>&1)" || { rmdir "$mnt"; die "the .dmg does not mount: $out"; }
  if [ ! -d "$mnt/$product.app" ]; then hdiutil detach -quiet "$mnt" || true; die "the .dmg holds no $product.app"; fi
  codesign --verify --deep --strict "$mnt/$product.app" || { hdiutil detach -quiet "$mnt" || true; die "the app inside the .dmg does not verify"; }
  hdiutil detach -quiet "$mnt" || hdiutil detach -force -quiet "$mnt" || true
  rmdir "$mnt" 2>/dev/null || true
  say "  the .dmg mounts and holds a signed $product.app"
}
[ "${SKIP_VERIFY:-}" = "1" ] || verify

mkdir -p "$dist"
cp "$dmg" "$dist/${product}_${name_version}_${label}.dmg"
for f in "$bundle/macos/$product.app.tar.gz" "$bundle/macos/$product.app.tar.gz.sig"; do
  [ -f "$f" ] || continue
  cp "$f" "$dist/${product}_${name_version}_${label}.${f#"$bundle/macos/$product."}"
done
say "done in $(( $(date +%s) - started )) s:"
for f in "$dist/${product}_${name_version}_${label}".*; do
  say "  $f ($(du -h "$f" | cut -f1), sha256 $(shasum -a 256 "$f" | cut -d' ' -f1))"
done
