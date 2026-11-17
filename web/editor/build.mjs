// Builds web/static/js/editor.js: one minified IIFE, ES2020, no source map.
//
//   node build.mjs                 write web/static/js/editor.js
//   node build.mjs --out <file>    write somewhere else (the Go test uses this)
//   node build.mjs --analyze       also print the biggest inputs
//
// The output is committed. `go test ./internal/editor` rebuilds it into a
// temporary file and fails if the bytes differ from the committed bundle.

import {build} from "esbuild"
import {gzipSync} from "node:zlib"
import {readFileSync} from "node:fs"
import {dirname, join, resolve} from "node:path"
import {fileURLToPath} from "node:url"

const here = dirname(fileURLToPath(import.meta.url))
const args = process.argv.slice(2)
const outIndex = args.indexOf("--out")
const outfile = outIndex >= 0 ? resolve(args[outIndex + 1]) : resolve(here, "..", "static", "js", "editor.js")
const analyze = args.includes("--analyze")

const result = await build({
  entryPoints: [join(here, "src", "index.js")],
  outfile,
  bundle: true,
  minify: true,
  format: "iife",
  target: "es2020",
  platform: "browser",
  legalComments: "none",
  charset: "utf8",
  sourcemap: false,
  metafile: analyze,
  logLevel: "warning"
})

const bytes = readFileSync(outfile)
console.log(`${outfile}: ${bytes.length} bytes, ${gzipSync(bytes, {level: 9}).length} bytes gzip`)

if (analyze) {
  const inputs = Object.entries(result.metafile.outputs[Object.keys(result.metafile.outputs)[0]].inputs)
  inputs.sort((a, b) => b[1].bytesInOutput - a[1].bytesInOutput)
  for (const [name, info] of inputs.slice(0, 25)) console.log(String(info.bytesInOutput).padStart(8), name)
}
