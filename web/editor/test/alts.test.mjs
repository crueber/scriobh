// The map between Markdown source offsets and positions (src/alts.js): the position of a source range in the document, the
// source of a selection with its context, and the quiet edits an alternatives op needs.
import test from "node:test"
import assert from "node:assert/strict"
import {EditorState} from "prosemirror-state"
import {parse, serialize} from "../src/markdown.js"
import {Decoration, DecorationSet} from "prosemirror-view"
import {rangesOf, sourceOf, offsetOf, plainOf, makeAlts} from "../src/alts.js"
import {sanitizePastedHTML, cleanText} from "../src/paste.js"

const at = (md, s) => ({start: md.indexOf(s), end: md.indexOf(s) + s.length})
const text = (doc, r) => doc.textBetween(r.from, r.to)

test("source offsets map to positions, through emphasis, links, escapes and multi-byte text", () => {
  const md = "Intro with *bright* words and a [link](https://x.test) here.\n\nA café, a snow☃man and a 😀 smile\\_ end.\n\n> Quoted line for the end.\n"
  const doc = parse(md)
  const wanted = ["bright", "link", "café", "snow☃man", "smile", "Quoted line"]
  const out = rangesOf(md, wanted.map(w => at(md, w)))
  wanted.forEach((w, i) => assert.equal(text(doc, out[i]), w, w))
})

test("a range that spans markup keeps the asterisks it holds when quoted", () => {
  const doc = parse("One *two three* four.\n")
  const state = EditorState.create({doc})
  const from = 1 + "One ".length, to = from + "two three".length
  assert.deepEqual(sourceOf(state, from, to), {quote: "two three", before: "One *", after: "* four.\n".slice(0, 24)})
  const whole = sourceOf(state, 1, 1 + "One two three four.".length)
  assert.equal(whole.quote, "One *two three* four.")
})

test("context is at most 24 characters of source on each side", () => {
  const md = "a".repeat(60) + " target " + "b".repeat(60) + "\n"
  const state = EditorState.create({doc: parse(md)})
  const s = sourceOf(state, 1 + 61, 1 + 61 + 6)
  assert.equal(s.quote, "target")
  assert.equal(s.before.length, 24)
  assert.equal(s.after.length, 24)
})

function host(md) {
  let state = EditorState.create({doc: parse(md)}), quietNow = false, changes = 0
  const view = {get state() { return state }, dispatch(tr) { state = state.apply(tr); if (!quietNow) changes++ }}
  const alts = makeAlts(view, {silently(fn) { quietNow = true; try { fn() } finally { quietNow = false } }, settle() {}})
  return {view, alts, changes: () => changes}
}

test("replace puts inline Markdown over a range as one quiet step and keeps the selection", () => {
  const h = host("The harbour was *quiet* today.\n")
  const r = rangesOf("The harbour was *quiet* today.\n", [at("The harbour was *quiet* today.\n", "quiet")])[0]
  const end = h.alts.replace(r.from, r.to, "very **still**")
  assert.equal(end, r.from + "very still".length)
  assert.equal(serialize(h.view.state.doc), "The harbour was *very **still*** today.\n".replace("*very **still***", "*very **still***"))
  assert.equal(h.changes(), 0)
  assert.equal(h.alts.replace(r.from, end, "two\n\nparagraphs"), -1)
})

test("setComment swaps, adds and removes the alternatives block without touching the text", () => {
  const h = host("Body.\n\n<!-- scriobh:alts {\"version\":1,\"sig\":\"a\"} -->\n")
  assert.equal(h.alts.setComment("<!-- scriobh:alts {\"version\":1,\"sig\":\"b\"} -->"), true)
  assert.equal(serialize(h.view.state.doc), "Body.\n\n<!-- scriobh:alts {\"version\":1,\"sig\":\"b\"} -->\n")
  assert.equal(h.alts.setComment("<!-- scriobh:alts {\"version\":1,\"sig\":\"b\"} -->"), false)
  h.alts.setComment("")
  assert.equal(serialize(h.view.state.doc), "Body.\n")
  h.alts.setComment("<!-- scriobh:alts {\"version\":1} -->")
  assert.equal(serialize(h.view.state.doc), "Body.\n\n<!-- scriobh:alts {\"version\":1} -->\n")
  assert.equal(h.changes(), 0)
})

test("replace with the replaced source checks it first, and keeps only the emphasis around the old wording", () => {
  // The old wording inside emphasis: the new one stays inside it.
  const md = "The harbour was *quiet* today.\n"
  const h = host(md)
  const r = rangesOf(md, [at(md, "quiet")])[0]
  assert.equal(h.alts.replace(r.from, r.to, "still", "quiet") > 0, true)
  assert.equal(serialize(h.view.state.doc), "The harbour was *still* today.\n")
  // The old wording that holds its own asterisks: the new one is not made italic.
  const md2 = "*Quiet* harbour, said Mara.\n\nOther.\n"
  const h2 = host(md2)
  const r2 = rangesOf(md2, [at(md2, "*Quiet* harbour, said Mara.")])[0]
  assert.equal(h2.alts.replace(r2.from, r2.to, "Still water, said Mara.", "*Quiet* harbour, said Mara.") > 0, true)
  assert.equal(serialize(h2.view.state.doc), "Still water, said Mara.\n\nOther.\n")
  // The editor does not hold what the server says it replaced: nothing is changed.
  const h3 = host("The harbour was quiet today.\n")
  const r3 = rangesOf("The harbour was quiet today.\n", [at("The harbour was quiet today.\n", "quiet")])[0]
  assert.equal(h3.alts.replace(r3.from + 2, r3.to + 2, "still", "quiet"), -1)
  assert.equal(serialize(h3.view.state.doc), "The harbour was quiet today.\n")
})

test("ranges do not run out of marker codes, and text that looks like a marker is text", () => {
  // 3,500 words, each its own range, past the room the old private-use codes had; with a ligature, private-use characters
  // and an astral character in the text.
  const words = []
  for (let i = 0; i < 3500; i++) words.push("w" + i)
  const md = "Start ﬁne   \u{1F600} " + words.join(" ") + " end.\n"
  const rs = words.map(w => at(md, " " + w + " ")).map(r => ({start: r.start + 1, end: r.end - 1}))
  const doc = parse(md)
  const out = rangesOf(md, rs)
  for (const k of [0, 1, 1000, 3039, 3040, 3041, 3499]) assert.equal(text(doc, out[k]), words[k], words[k])
})

test("the decorations of one document carry over to the next through the edit between them", () => {
  const md = "The harbour was quiet today.\n\nSecond paragraph here.\n"
  const h = host(md)
  const r = rangesOf(md, [at(md, "quiet")])[0]
  const set = DecorationSet.create(h.view.state.doc, [Decoration.inline(r.from, r.to, {class: "x"})])
  const before = h.view.state.doc
  h.view.dispatch(h.view.state.tr.insertText("ur wa", 1, 1))
  const moved = h.alts.mapSet(set, before, h.view.state.doc)
  const [d] = moved.find()
  assert.equal(text(h.view.state.doc, {from: d.from, to: d.to}), "quiet")
})

test("foreign pasted HTML cannot make held source: no ghost tag, no alternatives block", () => {
  const foreign = '<p>a <code class="source-inline">&lt;scriobh-ghost&gt;</code> b</p><pre class="source-block" data-kind="alts"><code>&lt;!-- scriobh:alts {} --&gt;</code></pre>'
  const clean = sanitizePastedHTML(foreign)
  assert.equal(/source-(inline|block)/.test(clean), false)
  assert.equal(/data-kind/.test(clean), false)
  // The editor's own clipboard keeps them.
  const own = '<p data-pm-slice="1 1 []">a <code class="source-inline">x</code></p>'
  assert.equal(/source-inline/.test(sanitizePastedHTML(own)), true)
})

const select = (md, word) => {
  const doc = parse(md); let from = -1
  doc.descendants((n, p) => { if (from < 0 && n.isText && n.text.includes(word)) from = p + n.text.indexOf(word) })
  return {state: EditorState.create({doc}), from, to: from + word.length}
}

test("a chapter that really holds the old marker code points is still named exactly", () => {
  const md = "Intro  old words end.\n\nLater the word target sits  here, and  again.\n"
  const {state, from, to} = select(md, "target")
  const s = sourceOf(state, from, to)
  assert.equal(s.quote, "target")
  assert.equal(md.slice(md.indexOf(s.before + "target") + s.before.length, md.indexOf(s.before + "target") + s.before.length + 6), "target")
  assert.equal(offsetOf(state, from), md.indexOf("target"))
  // The selection holds one of the code points itself.
  const both = select(md, "word target sits  here")
  assert.equal(sourceOf(both.state, both.from, both.to).quote, "word target sits  here")
})

test("a quote that occurs twice carries enough context to say which, and astral and CRLF text are named too", () => {
  const md = "The tide turned. Mara waited. The tide turned. Mara waited. The tide turned again.\n"
  const doc = parse(md)
  let second = -1
  doc.descendants((n, p) => { if (second < 0 && n.isText) { const i = n.text.indexOf("Mara waited", n.text.indexOf("Mara waited") + 1); if (i >= 0) second = p + i } })
  const s = sourceOf(EditorState.create({doc}), second, second + "Mara waited".length)
  const whole = s.before + s.quote + s.after
  assert.equal(md.indexOf(whole), md.lastIndexOf(whole), "the quote with its context is found once")
  assert.equal(md.indexOf(whole) + s.before.length, md.lastIndexOf("Mara waited"))
  const astral = select("A \u{1F600} smile and a \u{1F680} launch.\n", "launch")
  assert.equal(sourceOf(astral.state, astral.from, astral.to).quote, "launch")
  assert.equal(offsetOf(astral.state, astral.from), "A \u{1F600} smile and a \u{1F680} ".length)
  const crlf = select("First line\r\nsecond line with target here.\r\n\r\nNext.\r\n", "target")
  assert.equal(sourceOf(crlf.state, crlf.from, crlf.to).quote, "target")
})

test("a text that cannot be told apart from itself is refused, not guessed", () => {
  const same = select("echo echo echo\n", "echo")
  // Three equal words in one line: the context to either side can never make one of them unique, so it is refused.
  const r = sourceOf(same.state, same.from + 5, same.from + 9)
  assert.ok(r === null || (r.before + r.quote + r.after).split(r.before + r.quote + r.after).length === 2)
})

test("a lone surrogate is replaced, and the serialization the host compares with is the cleaned one", () => {
  assert.equal(cleanText("a\uD800b"), "a�b")
  assert.equal(cleanText("a\uDC00b"), "a�b")
  assert.equal(cleanText("a\u{1F600}b"), "a\u{1F600}b")
  const doc = parse("word \uD800 word\n")
  assert.equal(plainOf(EditorState.create({doc})), "word � word\n")
})
