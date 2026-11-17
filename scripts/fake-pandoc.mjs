// A stand-in for the Pandoc program, for the browser scripts. It is a shell script that answers --version and does
// a "conversion" the way the Go tests' stand-in does (internal/pandoc/pandoctest): an import prints Markdown that holds
// a known paragraph and the input file's text, an export writes the compile Markdown behind a first line that names the
// writer. It proves how the pages call the bridge, nothing about what a real Pandoc does.
//
//   const fp = installFakePandoc(join(tmp, "pandoc-bin"));          // { dir, bin }
//   const old = installFakePandoc(join(tmp, "pandoc-old"), { version: "2.14.2" });
//
// The folder is outside the data directory, mode 755, and the program is mode 755 and the caller's own: what the
// bridge's checks ask of a binary. Writing "fail" into <dir>/mode makes every conversion fail with an error text that
// holds a canary, so a script can check that the text never reaches a page.
import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

export const CANARY = "SECRET_DOCUMENT_TEXT";

export function installFakePandoc(dir, { version = "3.2" } = {}) {
  mkdirSync(dir, { recursive: true });
  chmodSync(dir, 0o755);
  const bin = join(dir, "pandoc");
  writeFileSync(bin, `#!/bin/sh
D="$(dirname "$0")"
mode="ok"
[ -f "$D/mode" ] && mode="$(cat "$D/mode")"
case "$1" in
--version) printf 'pandoc ${version}\\nFeatures: +server +lua\\n'; exit 0;;
esac
{ for a in "$@"; do printf '%s\\n' "$a"; done; printf -- '--end--\\n'; } >> "$D/args.log"
[ "$mode" = fail ] && { echo "${CANARY} the document says" >&2; exit 3; }
out=""; prev=""; writer=""
for a in "$@"; do
  [ "$prev" = "-o" ] && out="$a"
  [ "$prev" = "-t" ] && writer="$a"
  prev="$a"
done
if [ -n "$out" ]; then
  { printf 'FAKE-PANDOC %s\\n' "$writer"; cat input.md; printf 'A short test paragraph.\\n'; } > "$out"
  exit 0
fi
printf '# Chapter One\\n\\nA short test paragraph.\\n\\nFROM-THE-FILE: '
head -c 400 input | tr -d '\\000-\\010\\013-\\037' | head -n 1
printf '\\n'
exit 0
`);
  chmodSync(bin, 0o755);
  return { dir, bin };
}
