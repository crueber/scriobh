#!/usr/bin/env bash
# Looks inside a Windows installer built by build-windows-cross.sh and fails when it is not what the app needs.
# It never runs a Windows program: it reads the files. What it proves is the make-up of the package (NSIS, the two
# programs inside, the right machine and subsystem, the version), not that the installer installs or the app runs.
#
#   desktop/scripts/verify-windows-installer.sh <setup.exe> <scriobh-desktop.exe> <scriobh-desktop-server-...exe>
#
# Needs 7z (or 7zz) and python3.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# SRC_DIR: the tree that was built (build-windows-cross.sh passes it on), whose tauri.conf.json holds the version to expect.
conf="${SRC_DIR:-$here/../..}/desktop/src-tauri/tauri.conf.json"
installer="${1:?usage: verify-windows-installer.sh <setup.exe> <app.exe> <server.exe>}"
app="${2:?missing the app}"
server="${3:?missing the server}"
fail() { echo "verify-windows-installer: FAIL: $*" >&2; exit 1; }
ok() { echo "verify-windows-installer: ok: $*"; }
sevenz="$(command -v 7zz 2>/dev/null || command -v 7z 2>/dev/null || true)"
[ -n "$sevenz" ] || fail "7z is not installed"
command -v python3 >/dev/null 2>&1 || fail "python3 is not installed"
if command -v sha256sum >/dev/null 2>&1; then hash() { sha256sum "$1" | cut -d' ' -f1; }; else hash() { shasum -a 256 "$1" | cut -d' ' -f1; }; fi
want="$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$conf" | head -1)"
[ -n "$want" ] || fail "no version in $conf"

if command -v file >/dev/null 2>&1; then
  for f in "$installer" "$app" "$server"; do echo "verify-windows-installer: file: $(file -b "$f")"; done
fi

# 1. The installer is an NSIS installer and holds both programs.
listing="$("$sevenz" l "$installer")"
grep -q '^Type = Nsis' <<<"$listing" || fail "$installer is not an NSIS installer according to 7z"
grep -Eq ' scriobh-desktop\.exe$' <<<"$listing" || fail "the installer does not hold scriobh-desktop.exe"
grep -Eq ' scriobh-desktop-server\.exe$' <<<"$listing" || fail "the installer does not hold scriobh-desktop-server.exe (the sidecar, installed beside the app)"
ok "NSIS installer holding scriobh-desktop.exe and scriobh-desktop-server.exe"
echo "$listing" | sed -n '/^-----/,$p' | sed 's/^/    /'

# 2. What is inside is what was built: extract both and compare hashes.
tmp="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-verify.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
"$sevenz" x -y -o"$tmp/x" "$installer" scriobh-desktop.exe scriobh-desktop-server.exe >/dev/null
[ "$(hash "$tmp/x/scriobh-desktop-server.exe")" = "$(hash "$server")" ] || fail "scriobh-desktop-server.exe in the installer is not the server built"
# The bundler writes the package kind into the app it packs (a 3-byte marker, __TAURI_BUNDLE_TYPE_VAR_NSS) and puts the
# unmarked copy back on disk afterwards, so the two app files are equal except for that marker.
python3 -I - "$tmp/x/scriobh-desktop.exe" "$app" <<'PY' || fail "scriobh-desktop.exe in the installer is not the one built"
import sys
a, b = (open(p, "rb").read() for p in sys.argv[1:3])
m = b"__TAURI_BUNDLE_TYPE_VAR_"
if len(a) != len(b): raise SystemExit("the sizes differ")
diff = [i for i in range(len(a)) if a[i] != b[i]]
if not diff or len(diff) > 3: raise SystemExit(f"{len(diff)} bytes differ, want only the 3 of the marker")
start = a.rfind(m, 0, diff[0] + 1) + len(m)
if start < len(m) or diff[-1] >= start + 3: raise SystemExit("the bytes that differ are not the marker's")
if a[start:start + 3] != b"NSS": raise SystemExit("the app in the installer is not marked as an NSIS package")
PY
ok "both programs in the installer are the ones built (the app differs only in the bundler's NSIS marker)"

# 3. PE headers: x86-64, the app a GUI program (no console window), the server a console program (the shell starts it
#    with CREATE_NO_WINDOW), and the app's imports do not name the shared Visual C++ runtime (it is linked in).
python3 -I - "$tmp/x/scriobh-desktop.exe" "$tmp/x/scriobh-desktop-server.exe" <<'PY' || fail "PE header check"
import struct, sys
def pe(path):
    d = open(path, "rb").read()
    if d[:2] != b"MZ": raise SystemExit(f"{path}: not a PE file")
    off = struct.unpack_from("<I", d, 0x3C)[0]
    if d[off:off+4] != b"PE\0\0": raise SystemExit(f"{path}: no PE signature")
    machine = struct.unpack_from("<H", d, off+4)[0]
    magic = struct.unpack_from("<H", d, off+24)[0]
    subsystem = struct.unpack_from("<H", d, off+24+68)[0]
    return d, machine, magic, subsystem
names = {2: "WINDOWS_GUI", 3: "WINDOWS_CUI"}
want = {sys.argv[1]: 2, sys.argv[2]: 3}
for path, sub in want.items():
    d, machine, magic, subsystem = pe(path)
    label = path.rsplit("/", 1)[-1]
    if machine != 0x8664: raise SystemExit(f"{label}: machine {machine:#x}, want 0x8664 (x86-64)")
    if magic != 0x20B: raise SystemExit(f"{label}: not a PE32+ image")
    if subsystem != sub: raise SystemExit(f"{label}: subsystem {names.get(subsystem, subsystem)}, want {names[sub]}")
    print(f"verify-windows-installer: ok: {label}: PE32+ x86-64, subsystem {names[subsystem]}")
d = pe(sys.argv[1])[0]
for dll in (b"vcruntime140.dll", b"VCRUNTIME140.dll", b"msvcp140.dll", b"MSVCP140.dll"):
    if dll in d: raise SystemExit(f"scriobh-desktop.exe imports {dll.decode()}: the C runtime was not linked in")
print("verify-windows-installer: ok: scriobh-desktop.exe imports no vcruntime140.dll or msvcp140.dll")
PY

# 4. The version resource of the app says the version of tauri.conf.json.
"$sevenz" x -y -o"$tmp/r" "$tmp/x/scriobh-desktop.exe" '.rsrc/version.txt' >/dev/null || fail "no version resource in scriobh-desktop.exe"
res="$(python3 -I -c 'import sys; sys.stdout.write(open(sys.argv[1], "rb").read().decode("utf-16-le", "replace"))' "$tmp/r/.rsrc/version.txt" | tr -d '\r')"
echo "$res" | grep -E 'FILEVERSION|PRODUCTVERSION|"(FileVersion|ProductVersion|ProductName|FileDescription)"' | sed 's/^ */    /'
echo "$res" | grep -q "\"FileVersion\", *\"$want\"" || fail "FileVersion is not $want"
echo "$res" | grep -q "\"ProductVersion\", *\"$want\"" || fail "ProductVersion is not $want"
ok "the version resource says $want"
