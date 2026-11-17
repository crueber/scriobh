#!/usr/bin/env bash
# Installs the .deb the Linux job just built, the way a person would (`apt-get install ./file.deb`, so that its
# dependencies are resolved), and confirms what it put on disk: the launcher, the bundled server beside it, and
# the desktop entry. Debian or Ubuntu, as root. Nothing is started here: desktop/scripts/launch-check.sh does that.
#
#   desktop/scripts/deb-install.sh <the .deb, or the folder that holds exactly one>
#
# What this does NOT prove: that the package's Depends line is enough on a clean machine. The job has the
# `-dev` packages of WebKitGTK and GTK installed (it built the app with them), so every library the app links
# is already present. The Depends line and the libraries the installed binary resolves are printed so a change
# in either is visible in the log; a clean-machine check needs a fresh container.
set -euo pipefail
# Under pipefail, `cmd | grep -q` fails when grep exits at its first match and cmd then dies of SIGPIPE;
# has() reads all of its input, so a match is a match whatever the size of cmd's output.
has() { grep "$@" >/dev/null; }

arg="${1:?usage: deb-install.sh <file.deb or folder>}"
if [ -d "$arg" ]; then
  shopt -s nullglob
  debs=("$arg"/*.deb)
  [ "${#debs[@]}" -eq 1 ] || { echo "deb-install: FAIL: expected exactly one .deb in $arg, found ${#debs[@]}: ${debs[*]:-none}" >&2; exit 1; }
  deb="${debs[0]}"
else
  deb="$arg"
fi
[ -f "$deb" ] || { echo "deb-install: FAIL: no such file: $deb" >&2; exit 1; }
deb="$(cd "$(dirname "$deb")" && pwd)/$(basename "$deb")"

fail() { echo "deb-install: FAIL: $*" >&2; exit 1; }
say() { echo "deb-install: $*"; }

pkg="$(dpkg-deb -f "$deb" Package)"
say "package: $pkg $(dpkg-deb -f "$deb" Version) $(dpkg-deb -f "$deb" Architecture), $(du -k "$deb" | cut -f1) KiB"
say "Depends: $(dpkg-deb -f "$deb" Depends)"
[ "$(dpkg-deb -f "$deb" Architecture)" = "$(dpkg --print-architecture)" ] || fail "the package is for $(dpkg-deb -f "$deb" Architecture), this machine is $(dpkg --print-architecture)"

export DEBIAN_FRONTEND=noninteractive
# An absolute path, so apt reads it as a file and resolves its dependencies from the archive.
apt-get install -y --no-install-recommends "$deb" || fail "apt-get could not install $deb"

status="$(dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null || true)"
[ "$status" = "install ok installed" ] || fail "dpkg says '$pkg' is '$status', not 'install ok installed'"
say "dpkg: $pkg is installed"

listing="$(dpkg -L "$pkg")"
launcher=/usr/bin/scriobh-desktop
server=/usr/bin/scriobh-desktop-server
for f in "$launcher" "$server"; do
  printf '%s\n' "$listing" | has -xF "$f" || fail "$f is not in the package's file list (dpkg -L $pkg)"
  [ -f "$f" ] && [ -x "$f" ] || fail "$f is not an executable file on disk"
  say "present: $f ($(stat -c '%A %s bytes' "$f"))"
done
# The shell looks for the server next to its own executable (sidecar_path in desktop/src-tauri/src/app.rs).
[ "$(dirname "$(readlink -f "$launcher")")" = "$(dirname "$(readlink -f "$server")")" ] || fail "the server is not in the launcher's folder"

desktop_entry="$(printf '%s\n' "$listing" | grep -E '^/usr/share/applications/.*\.desktop$' | head -1 || true)"
[ -n "$desktop_entry" ] || fail "the package installs no desktop entry in /usr/share/applications"
exec_line="$(sed -n 's/^Exec=//p' "$desktop_entry" | head -1)"
say "desktop entry: $desktop_entry (Exec=$exec_line)"
command -v "${exec_line%% *}" >/dev/null 2>&1 || fail "the desktop entry's Exec command '${exec_line%% *}' is not on PATH"

# Every shared library the installed shell links must resolve (the server is static).
missing="$(ldd "$launcher" 2>&1 | grep -E 'not found' || true)"
[ -z "$missing" ] || fail "the installed shell has unresolved libraries:
$missing"
say "ldd: every library of $launcher resolves ($(ldd "$launcher" | wc -l) entries)"

# The bundled server starts and answers (a static binary: nothing to resolve; this shows it is the right architecture).
ver="$("$server" version 2>&1)" || fail "$server version failed: $ver"
say "bundled server: $server version -> $ver"
say "ok"
