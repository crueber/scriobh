// Cut and paste inside the editor goes through the DOM: ProseMirror serializes the
// selection to HTML (toDOM) and re-parses its own clipboard HTML (parseDOM). This
// runs the whole corpus through that path and requires the same Markdown back:
// nothing may be lost or changed by moving a document through the clipboard.

import assert from "node:assert/strict"
import {readdirSync, readFileSync} from "node:fs"
import {dirname, join, resolve} from "node:path"
import {fileURLToPath} from "node:url"
import {suite, md, paste} from "./harness.mjs"

const {bt, fresh} = suite()
const corpus = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..", "..", "internal", "editor", "testdata", "corpus")

bt("select all, copy, paste into an empty editor: every corpus document comes back the same", async () => {
  const names = readdirSync(corpus).filter(f => f.endsWith(".md") && !f.endsWith(".golden.md")).sort()
  const inputs = names.map(n => readFileSync(join(corpus, n), "utf8"))
  const {page, context} = await fresh()
  const results = []
  for (let i = 0; i < names.length; i++) {
    const copied = await page.evaluate(input => {
      h.mount(input)
      h.ed.focus()
      h.ed.view.dom.dispatchEvent(new KeyboardEvent("keydown", {key: "a", ctrlKey: !navigator.platform.includes("Mac"), metaKey: navigator.platform.includes("Mac"), bubbles: true}))
      const dt = new DataTransfer()
      h.ed.view.dom.dispatchEvent(new ClipboardEvent("copy", {clipboardData: dt, bubbles: true, cancelable: true}))
      return {html: dt.getData("text/html"), text: dt.getData("text/plain"), expected: scriobhEditor.markdown.normalize(input)}
    }, inputs[i])
    await page.evaluate(() => { h.mount(""); h.ed.focus() })
    await paste(page, {html: copied.html, text: copied.text})
    results.push({name: names[i], expected: copied.expected, got: await md(page), htmlLength: copied.html.length})
  }
  const bad = results.filter(r => r.got !== r.expected)
  assert.deepEqual(bad.map(r => ({name: r.name, expected: r.expected.slice(0, 200), got: r.got.slice(0, 200)})), [])
  assert.ok(results.length >= 100)
  await context.close()
})
