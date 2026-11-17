// Word-count oracle for the Go counter (internal/text).
//
//   node wordcount.mjs [--impl source|bundle] label=dir [label=dir ...]
//
// For every .md file in each directory prints one JSON object
// {"label/name.md": countWords(parse(markdown))}, keys sorted. The count is the
// one the editor's status bar shows (src/count.js); the Go counter must agree
// with it for every file. The default implementation is the source when
// node_modules is installed and the committed bundle otherwise; both are the
// same code (the bundle test fails when they drift).

import {existsSync, readdirSync, readFileSync} from "node:fs"
import {dirname, join, resolve} from "node:path"
import {fileURLToPath, pathToFileURL} from "node:url"

const here = dirname(fileURLToPath(import.meta.url))

async function load(impl) {
  if (impl === "bundle") {
    await import(pathToFileURL(resolve(here, "..", "static", "js", "editor.js")))
    const api = globalThis.scriobhEditor && globalThis.scriobhEditor.markdown
    if (!api) throw new Error("bundle does not export scriobhEditor.markdown")
    return {parse: api.parse, countWords: api.countWords}
  }
  const [{parse}, {countWords}] = await Promise.all([
    import(pathToFileURL(join(here, "src", "markdown.js"))),
    import(pathToFileURL(join(here, "src", "count.js")))
  ])
  return {parse, countWords}
}

async function main() {
  const args = process.argv.slice(2)
  let impl = existsSync(join(here, "node_modules", "prosemirror-model")) ? "source" : "bundle"
  const dirs = []
  let stdin = false
  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--impl") impl = args[++i]
    else if (args[i] === "--stdin") stdin = true
    else {
      const eq = args[i].indexOf("=")
      if (eq < 1) throw new Error("usage: wordcount.mjs [--impl source|bundle] label=dir ...")
      dirs.push([args[i].slice(0, eq), args[i].slice(eq + 1)])
    }
  }
  const {parse, countWords} = await load(impl)
  if (stdin) {
    // Batch mode for the differential test: a JSON array of Markdown strings in,
    // a JSON array of counts out.
    const chunks = []
    for await (const chunk of process.stdin) chunks.push(chunk)
    const inputs = JSON.parse(Buffer.concat(chunks).toString("utf8"))
    process.stdout.write(JSON.stringify(inputs.map(md => countWords(parse(md)))))
    return
  }
  if (!dirs.length) throw new Error("usage: wordcount.mjs [--impl source|bundle] [--stdin] label=dir ...")
  const out = {}
  for (const [label, dir] of dirs) {
    for (const name of readdirSync(dir).sort()) {
      if (!name.endsWith(".md")) continue
      out[label + "/" + name] = countWords(parse(readFileSync(join(dir, name), "utf8")))
    }
  }
  const sorted = {}
  for (const key of Object.keys(out).sort()) sorted[key] = out[key]
  process.stdout.write(JSON.stringify(sorted, null, 1) + "\n")
}

main().catch(err => { console.error(err && err.stack || err); process.exit(2) })
