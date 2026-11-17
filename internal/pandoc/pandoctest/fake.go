// Package pandoctest is a stand-in for the Pandoc program, for tests. No real
// Pandoc is used by any test of the bridge; this shell script answers
// --version, records the arguments, the environment and the input it was given
// in files beside it, and, when its "mode" file says so, misbehaves (hangs,
// floods standard output or standard error, fails, starts a child, writes
// links and pipes into the media folder). That proves how the bridge calls a
// program and what it does about one that misbehaves; it proves nothing about
// what a real Pandoc does with a real document.
package pandoctest

import (
	"os"
	"path/filepath"
	"testing"
)

// Script is the fake program.
const Script = `#!/bin/sh
D="$(dirname "$0")"
mode="ok"
[ -f "$D/mode" ] && mode="$(cat "$D/mode")"
case "$1" in
--version)
  echo run >> "$D/version.log" # every run of the program for its version, so a test can show there was none
  case "$mode" in
  old) printf 'pandoc 2.14.2\nFeatures: +server\n';;
  notpandoc) printf 'hello world\n';;
  *) printf 'pandoc 3.1.9\nFeatures: +server +lua\n';;
  esac
  exit 0;;
esac
# a conversion: record what was given
{ for a in "$@"; do printf '%s\n' "$a"; done; printf -- '--end--\n'; } >> "$D/args.log"
env | sort >> "$D/env.log"
printf -- '--end--\n' >> "$D/env.log"
for f in input input.md; do [ -f "$f" ] && cp "$f" "$D/input.log"; done
pwd > "$D/cwd.log"
extract=0
for a in "$@"; do case "$a" in --extract-media|--extract-media=*) extract=1;; esac; done
case "$mode" in
hang) sleep 60;;
slow) sleep 1;;
flood) yes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa | head -c 300000000; exit 0;;
stderrflood) yes SECRET_DOCUMENT_TEXT | head -c 30000000 >&2; exit 1;;
stderrwarn) yes "[WARNING] Could not fetch resource" | head -c 3000000 >&2; printf '# Chapter One\n\nHello world.\n'; exit 0;;
fail) echo "SECRET_DOCUMENT_TEXT the document says" >&2; exit 3;;
grandchild) sleep 60 & echo $! > "$D/child.pid"; wait;;
limits) { ulimit -t; ulimit -n; ulimit -c; ulimit -f; } > "$D/limits.log"; echo "# ok"; exit 0;;
escape)
  echo owned > "$D/escaped"
  if [ "$extract" = 1 ]; then
    mkdir -p media/deep/er/est/est/est/est/est/est/est/est/est
    echo deep > media/deep/er/est/est/est/est/est/est/est/est/est/too-deep.png
    ln -s /etc/hostname media/evil.png
    mkfifo media/pipe.png 2>/dev/null
    printf 'real image' > media/real.png
  fi
  echo "# Title"; exit 0;;
esac
# the ordinary conversions
out=""
prev=""
for a in "$@"; do
  [ "$prev" = "-o" ] && out="$a"
  prev="$a"
done
if [ -n "$out" ]; then
  if [ -f "$D/export.out" ]; then cp "$D/export.out" "$out"; else printf 'EXPORTED-FILE' > "$out"; fi
  exit 0
fi
if [ "$extract" = 1 ] && [ -f "$D/media.png" ]; then mkdir -p media/media; cp "$D/media.png" media/media/image1.png; fi
if [ -f "$D/md.out" ]; then cat "$D/md.out"; else printf '# Chapter One\n\nHello world.\n'; fi
exit 0
`

// Install writes the fake into a new directory and returns the directory and the program's path.
func Install(t testing.TB) (dir, bin string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "pandoc")
	if err := os.WriteFile(bin, []byte(Script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, bin
}

// SetMode tells the fake how to behave: ok (the default), old, notpandoc, hang, slow, flood,
// stderrflood, stderrwarn (a lot of warnings on standard error, then a good result), fail, grandchild, limits, escape.
func SetMode(t testing.TB, dir, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Put writes a file the fake reads: md.out (what an import returns), export.out
// (what an export writes), media.png (a file it extracts as media/media/image1.png).
func Put(t testing.TB, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Log reads a file the fake wrote: args.log, env.log, input.log, cwd.log, version.log (one line for each time it was run with --version).
func Log(t testing.TB, dir, name string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return string(b)
}
