// Node-only tests of the parser and serializer (no DOM, no browser). The Go test
// in internal/editor is the gate; these run the same corpus from `npm test` so the
// editor can be worked on without Go, and add a few focused checks.

import {test} from "node:test"
import assert from "node:assert/strict"
import {readdirSync, readFileSync, existsSync} from "node:fs"
import {execFileSync} from "node:child_process"
import {dirname, join, resolve} from "node:path"
import {fileURLToPath} from "node:url"
import {parse, parseDetailed, serialize, normalize, schema} from "../src/markdown.js"
import {countWords} from "../src/count.js"

const here = dirname(fileURLToPath(import.meta.url))
const corpus = resolve(here, "..", "..", "..", "internal", "editor", "testdata", "corpus")

test("corpus: canonical inputs are fixed points, others match their goldens, and the fallback is never used", () => {
  const files = readdirSync(corpus).filter(f => f.endsWith(".md") && !f.endsWith(".golden.md"))
  assert.ok(files.length >= 100)
  for (const name of files) {
    const input = readFileSync(join(corpus, name), "utf8")
    const goldenPath = join(corpus, name.replace(/\.md$/, ".golden.md"))
    const want = existsSync(goldenPath) ? readFileSync(goldenPath, "utf8") : input
    const first = parseDetailed(input)
    assert.equal(first.fallback, false, name + ": fallback used: " + first.error)
    const out = serialize(first.doc)
    assert.equal(out, want, name)
    assert.equal(normalize(out), out, name + ": not idempotent")
  }
})

test("literal-text and mark properties", () => {
  const out = execFileSync(process.execPath, [join(here, "..", "props.mjs"), "--impl", "src"], {encoding: "utf8"})
  assert.match(out, /^ok:/)
})

test("the alts comment survives byte for byte at the end of a document", () => {
  const alts = `<!-- scriobh:alts {"version":1,"items":[{"scope":"word","quote":"a_b *c* [d]","variants":["x","y"],"chosen":"x"}]} -->`
  const input = `Some text with *emphasis*.\n\n${alts}\n`
  const doc = parse(input)
  assert.equal(doc.lastChild.type.name, "source_block")
  assert.equal(doc.lastChild.attrs.kind, "alts")
  assert.equal(doc.lastChild.textContent, alts)
  assert.equal(serialize(doc), input)
  // After a rich edit the comment is still the last block, unchanged.
  const edited = schema.topNodeType.create(null, [schema.nodes.paragraph.create(null, schema.text("Changed text.")), doc.lastChild])
  assert.equal(serialize(edited), "Changed text.\n\n" + alts + "\n")
})

test("unsupported Markdown becomes a visible source block and is never dropped", () => {
  const cases = {
    html: ["<div class=\"x\">\n  raw\n</div>\n", "html"],
    comment: ["<!-- a comment -->\n", "html"],
    table: ["| a | b |\n| - | - |\n| 1 | 2 |\n", "table"],
    frontMatter: ["---\ntitle: x\n---\n\ntext\n", "front_matter"],
    directive: ["::: note\nbody\n:::\n", "directive"],
    reference: ["[ref]: http://example.com/\n", "reference"]
  }
  for (const [name, [md, kind]] of Object.entries(cases)) {
    const doc = parse(md)
    assert.equal(doc.firstChild.type.name, "source_block", name)
    assert.equal(doc.firstChild.attrs.kind, kind, name)
    assert.equal(serialize(doc), md, name)
  }
})

test("every thematic break spelling is one scene break, written as '* * *'", () => {
  for (const spelling of ["---", "***", "___", "- - -", "* * *", "_ _ _", "-----", "  ***  "]) {
    const doc = parse(`a\n\n${spelling}\n\nb\n`)
    assert.equal(doc.child(1).type.name, "scene_break", spelling)
    assert.equal(serialize(doc), "a\n\n* * *\n\nb\n", spelling)
  }
})

test("wiki links, private notes, footnotes and task items map to their own nodes and marks", () => {
  const doc = parse("A [[Title|alias]] and [[Plain]] with %%a *note*%% and a footnote[^1].\n\n- [x] done\n\n[^1]: Body.\n")
  const names = new Set()
  doc.descendants(n => { names.add(n.type.name); n.marks.forEach(m => names.add("mark:" + m.type.name)) })
  for (const want of ["wiki_link", "footnote_ref", "footnote_def", "mark:private_note", "mark:em", "list_item"]) assert.ok(names.has(want), want)
  let link
  doc.descendants(n => { if (n.type.name === "wiki_link" && !link) link = n })
  assert.deepEqual({...link.attrs}, {target: "Title", alias: "alias"})
  let item
  doc.descendants(n => { if (n.type.name === "list_item") item = n })
  assert.equal(item.attrs.checked, true)
})

test("word counts leave out private notes, code and source blocks", () => {
  const doc = parse("One two three %%secret words here%% four.\n\n```\ncode words here\n```\n\n<!-- scriobh:alts {\"a\":1} -->\n\n[[Wiki Link]] five\n")
  assert.equal(countWords(doc), 4 + 3)
})

test("hard breaks, trailing backslash and escapes", () => {
  assert.equal(normalize("a  \nb\n"), "a\\\nb\n")
  assert.equal(normalize("a\\\nb\n"), "a\\\nb\n")
  assert.equal(normalize("end\\\n"), "end\\\\\n")
})

test("ordered lists are renumbered from their first number", () => {
  assert.equal(normalize("3. a\n9. b\n1. c\n"), "3. a\n4. b\n5. c\n")
})

test("empty input is an empty document with no bytes", () => {
  assert.equal(serialize(parse("")), "")
  assert.equal(serialize(parse("\n\n  \n")), "")
  assert.equal(parse("").childCount, 1)
})

test("a document serializes identically however it was built", () => {
  const a = parse("# T\n\n*x* and **y**\n")
  const b = schema.topNodeType.create(null, [
    schema.nodes.heading.create({level: 1}, schema.text("T")),
    schema.nodes.paragraph.create(null, [schema.text("x", [schema.marks.em.create()]), schema.text(" and "), schema.text("y", [schema.marks.strong.create()])])
  ])
  assert.ok(a.eq(b))
  assert.equal(serialize(a), serialize(b))
})
