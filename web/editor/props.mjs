// Property tests for the serializer, run by the Go test (against the committed
// bundle, so Node alone is enough) and by `npm test` (against either).
//
//   node props.mjs --impl bundle|source [--cases N]
//
// 1. Literal text. Any punctuation-heavy string placed in a paragraph, heading,
//    list item, quote, footnote, or after a hard break must come back unchanged:
//    parse(serialize(doc)) equals doc. This is the class of bug that makes text
//    silently turn into formatting after a save.
// 2. Marks. Random runs of text with random marks must reach a fixed point after
//    one serialization (serialize(parse(serialize(doc))) == serialize(doc)) and
//    must keep all their characters.
//
// The generator is seeded, so a failure is reproducible.

import {loadImpl} from "./roundtrip.mjs"

const args = process.argv.slice(2)
const impl = args.includes("--impl") ? args[args.indexOf("--impl") + 1] : "bundle"
const cases = args.includes("--cases") ? +args[args.indexOf("--cases") + 1] : 4000

let api
if (impl === "bundle") {
  await loadImpl("bundle")
  api = globalThis.scriobhEditor.markdown
} else {
  api = await import("./src/markdown.js")
}
const {schema, parse, serialize} = api
const n = schema.nodes, m = schema.marks

function rng(seed) {
  return () => {
    seed |= 0; seed = (seed + 0x6D2B79F5) | 0
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}
const rand = rng(20260101)
const pick = arr => arr[Math.floor(rand() * arr.length)]

const ALPHABET = ["*", "_", "~", "\\", "[", "]", "(", ")", "<", ">", "&", "%", "#", "-", "+", "=", ".", "1", "2", "!", "`", "|", ":", "^", "\"", "'", " ", "a", "b", "é", "$", "{", "}", "@", "/", "x", "amp;", "&amp;", "[^1]", "[[", "]]", "%%", "```", "---", "* * *", "1.", "- ", "> ", "# ", ":::", "<b>", "<!--"]
const HAND = [
  "*", "**", "***", "_", "__", "~~", "`", "``", "[", "]", "[]", "[a]", "[a](b)", "![a](b)", "<a>", "<http://x.y>", "&amp;", "&copy;", "&#35;",
  "%%", "%%%", "%%a%%", "50%", "100%%", "- a", "+ a", "* a", "1. a", "1) a", "# a", "## a", "#", "a #", "a ##", "> a", "---", "===", "***", "* * *",
  "```", "~~~", ":::", "::: a", "[^1]", "[^1]:", "[[a]]", "[[a|b]]", "\\", "\\\\", "a\\", "\\*", "<!-- c -->", "<div>", "</div>", "|a|b|", "|-|-|",
  "http://x.y/a_b_c", "snake_case", "2*3*4", "a_b", "_a_", "*a*", "**a**", "~a~", "a  b", "a b", "café", "مرحبا"
]

function randomText() {
  let s = ""
  const len = 1 + Math.floor(rand() * 6)
  for (let i = 0; i < len; i++) s += pick(ALPHABET)
  return s.trim()
}
const CASES = []
for (const h of HAND) CASES.push(h.trim())
while (CASES.length < cases) CASES.push(randomText())

const text = s => schema.text(s)
const para = (...c) => n.paragraph.create(null, c)
const contexts = [
  ["paragraph", t => [para(text(t))]],
  ["second paragraph", t => [para(text("first")), para(text(t))]],
  ["heading", t => [n.heading.create({level: 2}, text(t))]],
  ["bullet item", t => [n.bullet_list.create({tight: true}, [n.list_item.create(null, para(text(t)))])]],
  ["loose item", t => [n.bullet_list.create({tight: false}, [n.list_item.create(null, para(text(t))), n.list_item.create(null, para(text("two")))])]],
  ["ordered item", t => [n.ordered_list.create({order: 3, tight: true}, [n.list_item.create(null, para(text(t)))])]],
  ["task item", t => [n.bullet_list.create({tight: true}, [n.list_item.create({checked: false}, para(text(t)))])]],
  ["quote", t => [n.blockquote.create(null, para(text(t)))]],
  ["quote list", t => [n.blockquote.create(null, n.bullet_list.create({tight: true}, [n.list_item.create(null, para(text(t)))]))]],
  ["footnote", t => [para(text("x")), n.footnote_def.create({label: "1"}, para(text(t)))]],
  ["after hard break", t => [para(text("a"), n.hard_break.create(), text(t))]],
  ["between hard breaks", t => [para(text("a"), n.hard_break.create(), text(t), n.hard_break.create(), text("b"))]],
  ["emphasised", t => [para(text("x "), schema.text(t, [m.em.create()]), text(" y"))]],
  ["strong", t => [para(text("x "), schema.text(t, [m.strong.create()]), text(" y"))]],
  ["link text", t => [para(schema.text(t, [m.link.create({href: "http://x.y/"})]))]],
  ["private note", t => [para(text("x "), schema.text(t, [m.private_note.create()]), text(" y"))]],
  ["after private note", t => [para(schema.text("a", [m.private_note.create()]), text(t))]],
  ["before private note", t => [para(text(t), schema.text("a", [m.private_note.create()]))]]
]

const failures = []
let literal = 0
const same = (a, b) => a.eq(b)
for (const t of CASES) {
  if (!t) continue
  for (const [name, make] of contexts) {
    let doc
    try { doc = schema.topNodeType.createChecked(null, make(t)) } catch (e) { continue }
    // Whitespace at the edge of marked text is the serializer's to move; skip those inputs.
    if (/emphasised|strong|link|private/.test(name) && (t !== t.trim())) continue
    literal++
    let back, md
    try {
      md = serialize(doc)
      back = parse(md)
    } catch (e) { failures.push({kind: "literal", context: name, text: t, error: String(e)}); continue }
    if (!same(doc, back)) failures.push({kind: "literal", context: name, text: t, markdown: md, got: back.toString()})
  }
}

// ---- marks
const MARKS = ["em", "strong", "strike", "code", "private_note", "link"]
const WORDS = ["alpha", "beta", "gamma", "d3lta", "épsilon", "a*b", "x_y", "50%", "(paren)", "[sq]", "<tag>", "&amp;", "a b", "one two three"]
let marksCases = 0
for (let i = 0; i < Math.max(500, cases / 4); i++) {
  // Marked runs are separated by unmarked spaces: emphasis next to punctuation
  // without whitespace is a CommonMark limitation (see docs/EDITOR-SPIKE.md).
  const runs = []
  const count = 1 + Math.floor(rand() * 4)
  for (let r = 0; r < count; r++) {
    const set = []
    for (const name of MARKS) if (rand() < 0.25) set.push(name === "link" ? m.link.create({href: "http://x.y/" + pick(["a", "b(c)", "d e"])}) : m[name].create())
    if (r > 0) runs.push(schema.text(" "))
    runs.push(schema.text(pick(WORDS), set))
  }
  let doc
  try { doc = schema.topNodeType.createChecked(null, [para(...runs)]) } catch (e) { continue }
  marksCases++
  try {
    // doc -> markdown -> doc normalises (whitespace moves outside emphasis). From the
    // normalised document on, the output must be a fixed point.
    const d1 = parse(serialize(doc))
    const md2 = serialize(d1)
    const md3 = serialize(parse(md2))
    if (md2 !== md3) failures.push({kind: "marks", markdown: md2, again: md3, doc: doc.toString()})
    else if (d1.textContent.trim() !== doc.textContent.trim()) failures.push({kind: "marks-text", markdown: md2, want: doc.textContent, got: d1.textContent})
    else if (!d1.eq(parse(md2))) failures.push({kind: "marks-doc", markdown: md2, doc: d1.toString(), again: parse(md2).toString()})
  } catch (e) { failures.push({kind: "marks", error: String(e), doc: doc.toString()}) }
}

if (failures.length) {
  console.log(`FAIL: ${failures.length} failures (${literal} literal-text cases, ${marksCases} mark cases, impl ${impl})`)
  for (const f of failures.slice(0, 12)) console.log(JSON.stringify(f))
  process.exit(1)
}
console.log(`ok: ${literal} literal-text cases and ${marksCases} mark cases passed (impl ${impl})`)
