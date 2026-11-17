// Round-trip driver for the Go test (internal/editor) and for `npm test`.
//
//   node roundtrip.mjs --impl source|bundle <corpus-dir>
//
// Prints one JSON document: {impl, files: [{name, out, out2, fallback, error}]}
// where out = serialize(parse(input)) and out2 = serialize(parse(out)).
// All assertions live in the Go test; this file only runs the code under test.
//
// `--impl source` loads src/markdown.js (needs node_modules).
// `--impl bundle` loads the committed web/static/js/editor.js in bare Node.

import {readdirSync, readFileSync} from "node:fs"
import {dirname, join, resolve} from "node:path"
import {fileURLToPath, pathToFileURL} from "node:url"

const here = dirname(fileURLToPath(import.meta.url))

export async function loadImpl(impl) {
  if (impl === "source" || impl === "src") {
    const mod = await import(pathToFileURL(join(here, "src", "markdown.js")))
    return {parseDetailed: mod.parseDetailed, serialize: mod.serialize}
  }
  if (impl === "bundle") {
    await import(pathToFileURL(resolve(here, "..", "static", "js", "editor.js")))
    const api = globalThis.scriobhEditor && globalThis.scriobhEditor.markdown
    if (!api) throw new Error("bundle does not export scriobhEditor.markdown")
    return {parseDetailed: api.parseDetailed, serialize: api.serialize}
  }
  throw new Error("unknown --impl " + impl)
}

async function main() {
  const args = process.argv.slice(2)
  let impl = "source", dir = null
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--impl") impl = args[++i]
    else dir = args[i]
  }
  if (!dir) throw new Error("usage: roundtrip.mjs --impl source|bundle <corpus-dir>")
  const {parseDetailed, serialize} = await loadImpl(impl)
  const files = []
  for (const name of readdirSync(dir).sort()) {
    if (!name.endsWith(".md") || name.endsWith(".golden.md")) continue
    const input = readFileSync(join(dir, name), "utf8")
    const first = parseDetailed(input)
    const out = serialize(first.doc)
    const second = parseDetailed(out)
    files.push({name, out, out2: serialize(second.doc), fallback: first.fallback || second.fallback, error: first.error || second.error})
  }
  process.stdout.write(JSON.stringify({impl, files}))
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(err => { console.error(err && err.stack || err); process.exit(2) })
}
