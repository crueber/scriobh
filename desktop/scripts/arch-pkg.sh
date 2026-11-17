#!/usr/bin/env bash
# The Arch Linux steps for the desktop app, one named step at a time: make an Arch package from the .deb the Linux
# job already built, install it on a clean Arch, start the installed app headless, remove it, and open the AppImage
# on Arch too. release.yml and desktop.yml call them inside an archlinux container (the steps need pacman); root.
#
#   desktop/scripts/arch-pkg.sh build install namcap launch remove appimage
#
#   deps-build  pacman: namcap (the package checker); makepkg itself is in the base-devel image
#   build       makepkg from desktop/arch/PKGBUILD over $DEB, into $DIST: scriobh-desktop-<ver>-1-x86_64.pkg.tar.zst
#   install     pacman -U the package in $DIST, check what it put on disk, and list the libraries the shell links
#   namcap      namcap over the PKGBUILD and the installed package; a library no dependency covers fails the step
#   deps-run    pacman: Xvfb, a session bus and the other tools the launch check calls
#   launch      run /usr/bin/scriobh-desktop headless, see its bundled server answer, quit it (launch-check.sh);
#               with ARCH_LAUNCH_GDB=1 the app runs under gdb, which prints every thread's backtrace if it will not quit
#   remove      pacman -Rns the package and see that it is gone, desktop entry and all
#   appimage    open $APPIMAGE (if there is one) with --appimage-extract-and-run, as a machine without FUSE must,
#               see its bundled server answer, quit it
#
# Environment:
#   DEB        the .deb to make the package from (build)
#   DIST       where the package goes and is found (default ./dist)
#   APPIMAGE   the AppImage to open (appimage); the step is a note and a success when unset or missing
#   VERSION    the tag, which the bundled server reports as its version (default: not compared)
#   SRC_DIR    the tree LICENSE and NOTICE come from (default: the tree this script is in)
#
# WHAT THESE STEPS DO AND DO NOT PROVE. The base-devel and plain archlinux images carry no GTK or WebKit, so
# `install` is the first time those come from the package's `depends` alone: a dependency missing from the list shows
# as a library `ldd` cannot find or an app that does not start. The harness tools (`deps-run`) are installed AFTER the
# package for that reason. What is NOT proved: the window. The launch check forces GDK_BACKEND=x11 and software
# rendering under Xvfb, so nothing here says whether the app draws on Hyprland, on Wayland or on an NVIDIA driver
# (docs/DESKTOP.md, "Arch Linux and Omarchy").
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
src="${SRC_DIR:-$root}"
DIST="${DIST:-dist}"
mkdir -p "$DIST"
DIST="$(cd "$DIST" && pwd)"

# The launch checks here do NOT suppress the first-run keychain notice (there is no Secret Service in the container, so it
# is raised): a real dialog is open when the app is asked to quit, which is where the shell used to freeze on Linux.
export LAUNCH_CHECK_NO_NOTICE_MARKER=1

say() { echo "arch-pkg: $*"; }
fail() { echo "arch-pkg: FAIL: $*" >&2; exit 1; }
as_root() { if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi; }

# pacman with one retry for a container whose kernel lacks what pacman 7's download sandbox wants (Landlock,
# seccomp): the message names it, and --disable-sandbox is pacman's own answer.
pac() {
  local log rc=0
  log="$(mktemp)"
  # `|| rc=...`: under pipefail a failing pacman must not end the script before its output is looked at.
  as_root pacman "$@" 2>&1 | tee "$log" || rc="${PIPESTATUS[0]}"
  if [ "$rc" -ne 0 ] && grep -qiE 'landlock|sandbox|seccomp' "$log"; then
    say "pacman could not sandbox its downloads here; trying once more with --disable-sandbox"
    rc=0
    as_root pacman --disable-sandbox "$@" 2>&1 | tee "$log" || rc="${PIPESTATUS[0]}"
  fi
  rm -f "$log"
  return "$rc"
}

one_pkg() { # the one package in $DIST
  local f=("$DIST"/scriobh-desktop-*-x86_64.pkg.tar.zst)
  [ -e "${f[0]}" ] && [ "${#f[@]}" -eq 1 ] || fail "expected exactly one scriobh-desktop-*-x86_64.pkg.tar.zst in $DIST, found ${#f[@]}"
  printf '%s\n' "${f[0]}"
}

step_deps_build() { pac -S --needed --noconfirm namcap; }

step_build() {
  [ -n "${DEB:-}" ] && [ -f "$DEB" ] || fail "DEB must name the .deb to package (got '${DEB:-}')"
  command -v makepkg >/dev/null 2>&1 || fail "makepkg is not here: run this in an archlinux:base-devel container"
  [ -f "$src/LICENSE" ] && [ -f "$src/NOTICE" ] || fail "no LICENSE and NOTICE in $src"
  rm -f "$DIST"/scriobh-desktop-*.pkg.tar.zst
  local work deb_name ctl ver name_ver pkgver sum_deb sum_lic sum_not
  work="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-arch.XXXXXX")"
  mkdir -p "$work/build" "$work/out" "$work/ctl"

  # Two versions can be in play. The .deb's control file says the app's own (what the Tauri bundler stamped from
  # tauri.conf.json); the file's name says the release's (release.yml names the published .deb for the tag, as the
  # Windows and macOS files are: Scriobh_<tag without v>_amd64.deb, see linux-deb.sh). The package is made for the
  # release, so pkgver is the file name's version (with - as _; the .deb's own name keeps the hyphen, here as _deb, and
  # the AUR recipe keeps it apart as _debver for its source name); the
  # control file's is the fallback for a .deb with some other name (a branch build is named by the bundler, and then
  # the two are the same), and a difference is a warning, not a failure.
  deb_name="$(basename "$DEB")"
  bsdtar -xf "$DEB" -C "$work/ctl" --include 'control.tar.*'
  bsdtar -xf "$work"/ctl/control.tar.* -C "$work/ctl"
  ctl="$work/ctl/control"
  [ -f "$ctl" ] || ctl="$work/ctl/./control"
  [ -f "$ctl" ] || fail "the .deb has no control file"
  ver="$(sed -n 's/^Version: *//p' "$ctl" | head -1 || true)"
  [ -n "$ver" ] || fail "the .deb's control file has no Version"
  [ "$(sed -n 's/^Architecture: *//p' "$ctl" | head -1 || true)" = amd64 ] || fail "the .deb is not for amd64"
  name_ver=""
  case "$deb_name" in Scriobh_*_amd64.deb) name_ver="${deb_name#Scriobh_}"; name_ver="${name_ver%_amd64.deb}" ;; esac
  if [ -n "$name_ver" ] && [ "$name_ver" != "$ver" ]; then
    say "WARNING: the .deb is named for $name_ver but its control file says $ver (the app inside). The package carries $name_ver, the release's version."
  fi
  # pkgver may not hold a hyphen (it separates pkgver from pkgrel): 2.1.0-rc.1 becomes 2.1.0_rc.1.
  pkgver="${name_ver:-$ver}"
  pkgver="${pkgver//-/_}"
  case "$pkgver" in *[!A-Za-z0-9._+]* | '') fail "the .deb's version '${name_ver:-$ver}' cannot be a pkgver" ;; esac
  say "the .deb is $deb_name, version ${name_ver:-$ver} (pkgver $pkgver); Depends: $(sed -n 's/^Depends: *//p' "$ctl" | head -1 || true)"

  cp -- "$DEB" "$work/build/$deb_name"
  cp -- "$src/LICENSE" "$src/NOTICE" "$work/build/"
  sum_deb="$(sha256sum "$work/build/$deb_name" | cut -d' ' -f1)"
  sum_lic="$(sha256sum "$work/build/LICENSE" | cut -d' ' -f1)"
  sum_not="$(sha256sum "$work/build/NOTICE" | cut -d' ' -f1)"
  sed -e "s|^pkgver=.*|pkgver=$pkgver|" \
      -e "s|^_deb=.*|_deb=$deb_name|" \
      -e "s|^sha256sums=.*|sha256sums=('$sum_deb' '$sum_lic' '$sum_not')|" \
      "$root/desktop/arch/PKGBUILD" >"$work/build/PKGBUILD"
  grep -q "^pkgver=$pkgver\$" "$work/build/PKGBUILD" && grep -q "^_deb=$deb_name\$" "$work/build/PKGBUILD" \
    && grep -q "^sha256sums=('$sum_deb'" "$work/build/PKGBUILD" || fail "the staged PKGBUILD was not edited as intended"
  bash -n "$work/build/PKGBUILD" || fail "the staged PKGBUILD does not parse"

  # makepkg refuses root. --nodeps: the package's own dependencies must NOT be installed yet (the install step is
  # what proves the list), and nothing is compiled.
  id builder >/dev/null 2>&1 || as_root useradd -m builder
  as_root chown -R builder "$work"
  say "makepkg as builder"
  as_root runuser -u builder -- env "HOME=$(getent passwd builder | cut -d: -f6)" "PKGDEST=$work/out" "BUILDDIR=$work/tmp" \
    bash -c 'cd "$1" && makepkg --nodeps --force --noconfirm --nocheck' _ "$work/build"
  local made=("$work"/out/*.pkg.tar.zst)
  [ -e "${made[0]}" ] && [ "${#made[@]}" -eq 1 ] || fail "makepkg left ${#made[@]} packages"
  [ "$(basename "${made[0]}")" = "scriobh-desktop-$pkgver-1-x86_64.pkg.tar.zst" ] || fail "makepkg made $(basename "${made[0]}"), not scriobh-desktop-$pkgver-1-x86_64.pkg.tar.zst"
  cp -- "${made[0]}" "$DIST/"
  as_root rm -rf "$work"

  local pkg listing
  pkg="$(one_pkg)"
  say "built $(basename "$pkg") ($(du -k "$pkg" | cut -f1) KiB)"
  listing="$(bsdtar -tf "$pkg")"
  for f in usr/bin/scriobh-desktop usr/bin/scriobh-desktop-server usr/share/licenses/scriobh-desktop/LICENSE usr/share/licenses/scriobh-desktop/NOTICE; do
    grep -qxF "$f" <<<"$listing" || fail "the package lacks $f"
  done
  grep -qE '^usr/share/applications/.*\.desktop$' <<<"$listing" || fail "the package has no desktop entry"
  grep -qE '^usr/share/icons/hicolor/.*/apps/scriobh-desktop\.png$' <<<"$listing" || fail "the package has no icon"
  say "the package's metadata:"
  bsdtar -xOf "$pkg" .PKGINFO | grep -E '^(pkgname|pkgver|pkgdesc|url|arch|license|depend|optdepend|size) ' | sed 's/^/arch-pkg:   /'
}

step_install() {
  local pkg f missing lib own deps resolved
  pkg="$(one_pkg)"
  # The test is only worth something on an image that does not already have what the shell links.
  if pacman -Q webkit2gtk-4.1 >/dev/null 2>&1 || pacman -Q gtk3 >/dev/null 2>&1; then
    say "note: webkit2gtk-4.1 or gtk3 is already installed here, so this install does not prove the dependency list"
  else
    say "webkit2gtk-4.1 and gtk3 are not installed: the dependencies come from the package alone"
  fi
  say "fuse2: $(pacman -Q fuse2 2>/dev/null || echo 'not installed')"
  pac -U --noconfirm "$pkg"

  pacman -Q scriobh-desktop >/dev/null 2>&1 || fail "pacman does not list scriobh-desktop as installed"
  for f in /usr/bin/scriobh-desktop /usr/bin/scriobh-desktop-server; do
    [ -f "$f" ] && [ -x "$f" ] || fail "$f is not an executable file"
    pacman -Qo "$f" 2>/dev/null | grep -q scriobh-desktop || fail "pacman says $f is not the package's"
    say "present: $f ($(stat -c '%A %s bytes' "$f"))"
  done
  [ "$(dirname "$(readlink -f /usr/bin/scriobh-desktop)")" = "$(dirname "$(readlink -f /usr/bin/scriobh-desktop-server)")" ] || fail "the server is not in the shell's folder (the shell looks beside its own executable)"
  local entry
  entry="$(pacman -Qlq scriobh-desktop | grep -E '^/usr/share/applications/.*\.desktop$' | head -1 || true)"
  [ -n "$entry" ] || fail "the package installs no desktop entry"
  say "desktop entry: $entry"
  sed -n 's/^\(Exec\|Icon\|MimeType\|Categories\)=/arch-pkg:   \1=/p' "$entry"
  command -v "$(sed -n 's/^Exec=//p' "$entry" | head -1 | cut -d' ' -f1)" >/dev/null 2>&1 || fail "the entry's Exec command is not on PATH"
  grep -q '^MimeType=.*x-scheme-handler/scriobh' "$entry" || fail "the entry does not register the scriobh scheme"
  grep -q '^MimeType=.*text/markdown' "$entry" && grep -q '^MimeType=.*application/epub+zip' "$entry" || fail "the entry does not register Markdown and EPUB"
  # pacman's own hook (desktop-file-utils) has indexed the entry; a note, not a failure, because it depends on the
  # hook running in this very transaction.
  if grep -q 'x-scheme-handler/scriobh=' /usr/share/applications/mimeinfo.cache 2>/dev/null; then say "mimeinfo.cache lists the scriobh scheme handler"
  else say "note: /usr/share/applications/mimeinfo.cache does not list the scriobh scheme handler (the pacman hook may not have run in this container)"; fi
  if command -v desktop-file-validate >/dev/null 2>&1; then
    desktop-file-validate "$entry" && say "desktop-file-validate: ok" || say "note: desktop-file-validate has remarks (above); not a failure"
  fi

  missing="$(ldd /usr/bin/scriobh-desktop 2>&1 | grep 'not found' || true)"
  [ -z "$missing" ] || fail "the installed shell has libraries nothing installed provides (the package's depends is not enough):
$missing"
  say "ldd: every library of /usr/bin/scriobh-desktop resolves"

  # What the shell links, and which package gives each: the evidence for the depends list.
  if command -v readelf >/dev/null 2>&1; then
    say "libraries the shell asks for (readelf -d NEEDED), and the package that owns each:"
    deps="$(pacman -Qi scriobh-desktop | sed -n 's/^Depends On *: *//p')"
    resolved="$(ldd /usr/bin/scriobh-desktop 2>&1 || true)"   # once, into a variable: awk below exits early
    for lib in $(readelf -d /usr/bin/scriobh-desktop | sed -n 's/.*Shared library: \[\(.*\)\]/\1/p'); do
      f="$(awk -v l="$lib" '$1 == l { print $3; exit }' <<<"$resolved")"
      own="$( [ -n "$f" ] && pacman -Qoq "$f" 2>/dev/null || echo '?')"
      case " $deps " in *" $own "*) echo "arch-pkg:   $lib -> $own (listed)" ;; *) echo "arch-pkg:   $lib -> $own (brought by another dependency)" ;; esac
    done
  else
    say "readelf is not on this image (it is in binutils, with base-devel); the NEEDED list is printed by the build job"
  fi
  ! command -v file >/dev/null 2>&1 || say "the bundled server: $(file -b /usr/bin/scriobh-desktop-server | cut -c1-90)"
  local ver
  ver="$(/usr/bin/scriobh-desktop-server --version 2>&1)" || fail "/usr/bin/scriobh-desktop-server --version failed: $ver"
  say "the bundled server reports '$ver'"
  if [ -n "${VERSION:-}" ] && [ "$ver" != "$VERSION" ]; then fail "the bundled server reports '$ver', not '$VERSION'"; fi
  say "install ok"
}

# namcap reads the installed libraries, so it runs after `install`. Its "Dependency X detected and not included" is the
# failure this step exists to catch; every other line is shown and not enforced (the list is explicit on purpose, so
# "included but already satisfied" is expected).
step_namcap() {
  command -v namcap >/dev/null 2>&1 || fail "namcap is not installed (deps-build)"
  local pkg out
  pkg="$(one_pkg)"
  out="$(mktemp)"
  say "namcap on the PKGBUILD"
  namcap "$root/desktop/arch/PKGBUILD" 2>&1 | tee "$out" || true
  say "namcap on the package"
  namcap "$pkg" 2>&1 | tee -a "$out" || true
  if grep -E ' E: Dependency .* detected and not included' "$out"; then rm -f "$out"; fail "namcap found a library no listed dependency covers (above)"; fi
  rm -f "$out"
  say "namcap: no missing dependency"
}

step_deps_run() {
  pac -S --needed --noconfirm xorg-server-xvfb xorg-xauth dbus procps-ng python curl ttf-dejavu mesa file desktop-file-utils elfutils xorg-xwininfo
}

step_launch() {
  [ -x /usr/bin/scriobh-desktop ] || fail "the package is not installed (install first)"
  if [ "${ARCH_LAUNCH_GDB:-}" != 1 ]; then
    "$root/desktop/scripts/launch-check.sh" /usr/bin/scriobh-desktop
    return
  fi
  # Opt-in diagnosis of a shell that does not quit: it runs as gdb's child, so that gdb, its parent, may ptrace it
  # (Yama allows only an ancestor), and launch-check.sh has gdb print every thread's backtrace when the quit fails.
  # The limits and the failure are launch-check.sh's, unchanged. debuginfod is off: in web run #178 fetching debug
  # info for every library held the start past launch-check's 90 s, and gdb 18.1 then aborted on a libstdc++
  # assertion while loading it. The libraries' exported names (GLib, GTK, WebKit) are enough to read where a
  # thread waits; the shell's own frames are addresses (it is stripped).
  say "ARCH_LAUNCH_GDB=1: the app runs under gdb; a hang will be stopped and every thread's backtrace printed"
  pac -S --needed --noconfirm gdb
  local dir
  dir="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-gdb.XXXXXX")"
  # Breakpoints on the GTK calls that make a dialog print which dialog it was and who asked (rfd's GTK backend calls
  # gtk_message_dialog_new with the title as the first %s argument, r9 on x86_64), so that a dialog nobody expected
  # is named. They only print and continue.
  cat >"$dir/dialogs.gdb" <<'GDBS'
set breakpoint pending on
break gtk_message_dialog_new
commands
silent
printf "GDB DIALOG: gtk_message_dialog_new, title: %s\n", (char*)$r9
bt 14
continue
end
break gtk_file_chooser_dialog_new
commands
silent
printf "GDB DIALOG: gtk_file_chooser_dialog_new, title: %s\n", (char*)$rdi
bt 14
continue
end
GDBS
  cat >"$dir/gdb-launch.sh" <<'WRAP'
#!/bin/sh
unset DEBUGINFOD_URLS
exec gdb -q -batch -x "$(dirname "$0")/dialogs.gdb" \
  -ex 'set pagination off' -ex 'set confirm off' -ex 'set startup-with-shell off' -ex 'set disable-randomization off' \
  -ex 'set debuginfod enabled off' \
  -ex 'handle SIGTERM nostop noprint pass' -ex 'handle SIG32 SIG33 SIG34 SIG35 nostop noprint pass' \
  -ex run \
  -ex 'echo \n=== GDB: the shell was interrupted or exited ===\n' \
  -ex 'info threads' -ex 'thread apply all bt 40' \
  -ex 'echo \n=== GDB: end ===\n' \
  -ex kill \
  --args /usr/bin/scriobh-desktop "$@"
WRAP
  chmod +x "$dir/gdb-launch.sh"
  LAUNCH_CHECK_GDB=1 LAUNCH_CHECK_SHELL_NAME=scriobh-desktop \
    "$root/desktop/scripts/launch-check.sh" "$dir/gdb-launch.sh" /usr/bin/scriobh-desktop-server
}

step_remove() {
  local owned entry
  owned="$(pacman -Qlq scriobh-desktop | grep -v '/$' || true)"
  [ -n "$owned" ] || fail "scriobh-desktop is not installed, or owns no files"
  entry="$(grep -E '^/usr/share/applications/.*\.desktop$' <<<"$owned" | head -1 || true)"
  [ -n "$entry" ] || fail "the package owned no desktop entry"
  pac -Rns --noconfirm scriobh-desktop
  pacman -Q scriobh-desktop >/dev/null 2>&1 && fail "pacman still lists scriobh-desktop"
  local f left=""
  while IFS= read -r f; do [ -e "$f" ] && left="$left $f"; done <<<"$owned"
  [ -z "$left" ] || fail "files of the package are still on disk after the removal:$left"
  [ ! -e "$entry" ] || fail "the desktop entry is still there"
  say "removed: $(printf '%s\n' "$owned" | wc -l | tr -d ' ') files gone, desktop entry gone"
  # The person's data is not the package's: nothing here may have been under the package's name.
  say "remove ok"
}

step_appimage() {
  if [ -z "${APPIMAGE:-}" ] || [ ! -f "${APPIMAGE:-}" ]; then say "no AppImage to open (APPIMAGE='${APPIMAGE:-}'); nothing to check"; return 0; fi
  local img work server wrap
  img="$(cd "$(dirname "$APPIMAGE")" && pwd)/$(basename "$APPIMAGE")"
  chmod +x "$img"
  work="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-appimage.XXXXXX")"
  # shellcheck disable=SC2064
  trap "rm -rf '$work'" RETURN
  say "fuse2: $(pacman -Q fuse2 2>/dev/null || echo 'not installed'); /dev/fuse: $([ -e /dev/fuse ] && echo present || echo absent). The AppImage is run with --appimage-extract-and-run, as a machine without FUSE must."
  say "webkit2gtk-4.1 on this machine: $(pacman -Q webkit2gtk-4.1 2>/dev/null || echo 'not installed (the AppImage must bring its own)')"
  ( cd "$work" && "$img" --appimage-extract >/dev/null ) || fail "the AppImage does not unpack with --appimage-extract"
  server="$(find "$work/squashfs-root" -type f -name scriobh-desktop-server | head -1 || true)"
  [ -n "$server" ] && [ -x "$server" ] || fail "the AppImage holds no executable scriobh-desktop-server"
  # The over-bundling that breaks newer Mesa on Arch (tauri-apps/tauri#15665): a bundled Wayland client library,
  # GStreamer or GLib is taken over the system's. A warning: this Xvfb run cannot show the failure, only a window
  # on Wayland with a current Mesa can.
  local libs
  libs="$(find "$work/squashfs-root" \( -name 'libwayland-*' -o -name 'libgst*' -o -name 'libglib-2.0*' -o -name 'libEGL*' -o -name 'libGL.so*' \) 2>/dev/null | sed "s|$work/squashfs-root/||" | sort || true)"
  if [ -n "$libs" ]; then
    say "WARNING: the AppImage bundles libraries that a newer Mesa on Wayland does not always tolerate (tauri-apps/tauri#15665); the headless check below cannot see that:"
    printf '%s\n' "$libs" | sed 's/^/arch-pkg:   /'
  else
    say "the AppImage bundles no libwayland, GStreamer, GLib or EGL libraries"
  fi
  wrap="$work/run-appimage.sh"
  printf '#!/bin/sh\nexec "%s" --appimage-extract-and-run "$@"\n' "$img" >"$wrap"
  chmod +x "$wrap"
  # The runtime stays as the parent of the app, so launch-check.sh finds the shell by name below it.
  LAUNCH_CHECK_SHELL_NAME=scriobh-desktop "$root/desktop/scripts/launch-check.sh" "$wrap" "$server"
  say "appimage ok"
}

[ $# -gt 0 ] || { sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//' >&2; exit 2; }
for s in "$@"; do
  case "$s" in
    deps-build|build|install|namcap|deps-run|launch|remove|appimage)
      echo "arch-pkg: $s"
      "step_${s//-/_}" ;;
    *) echo "arch-pkg: unknown step: $s" >&2; exit 2 ;;
  esac
done
