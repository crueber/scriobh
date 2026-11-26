// Where a chapter may be cut (src/alts.js, splitAt): the offset the split op is given, found through the editor's own
// serializer, and the halves the op makes from it. The op cuts the stored text byte for byte (it trims blank lines at the cut
// and nothing else), so each half must read back as what the editor wrote and the two must hold every word.
import test from "node:test"
import assert from "node:assert/strict"
import {EditorState, TextSelection, NodeSelection} from "prosemirror-state"
import {parse, serialize} from "../src/markdown.js"
import {countWords} from "../src/count.js"
import {plainOf} from "../src/alts.js"
import {splitAt} from "../src/split.js"

// What `doc.split` does with the offset (internal/domain/docs): the head loses the blank space at its end, and the backslash of a
// hard line break the cut came right after (`trimHead`); the tail loses leading line breaks.
function trimHead(head) {
  let t = head.replace(/[ \t\r\n]+$/, "")
  if (head.slice(t.length).includes("\n") && (/\\+$/.exec(t) || [""])[0].length % 2 === 1) t = t.slice(0, -1).replace(/[ \t]+$/, "")
  return t
}
const halves = (md, offset) => { const h = trimHead(md.slice(0, offset)); return {head: h ? h + "\n" : "", tail: md.slice(offset).replace(/^[\r\n]+/, "")} }

// A port of `insideSourceBlock` (internal/domain/docs/docs.go), which makes the op refuse a cut inside a code fence, an HTML
// comment or front matter. Written from the Go, line by line, so that the test applies the server's own rule.
function insideSourceBlock(content, cut) {
  const regs = [], lines = content.split(/(?<=\n)/)
  let pos = 0, fence = "", fenceStart = 0, comment = -1
  lines.forEach((ln, i) => {
    const start = pos, end = pos + ln.length
    pos = end
    const t = ln.replace(/[\r\n]+$/, ""), trim = t.replace(/^ +/, "")
    if (fence !== "") {
      if (trim.length >= fence.length && trim.startsWith(fence) && [...trim].every(c => c === fence[0]) && t.length - trim.length < 4) { regs.push([fenceStart, end]); fence = "" }
    } else if (comment >= 0) {
      if (t.includes("-->")) { regs.push([comment, end]); comment = -1 }
    } else if (i === 0 && t === "---") {
      let off = end
      for (const nx of lines.slice(1)) { off += nx.length; if (nx.replace(/[\r\n]+$/, "") === "---") { regs.push([0, off]); break } }
    } else if (t.length - trim.length < 4 && (trim.startsWith("```") || trim.startsWith("~~~"))) {
      const m = trim[0], n = trim.length - trim.replace(new RegExp("^\\" + m + "+"), "").length
      fence = m.repeat(n); fenceStart = start
    } else if (trim.startsWith("<!--")) {
      if (t.slice(t.indexOf("<!--") + 4).includes("-->")) regs.push([start, end]); else comment = start
    }
  })
  if (fence !== "") regs.push([fenceStart, content.length])
  if (comment >= 0) regs.push([comment, content.length])
  return regs.some(([a, b]) => cut > a && cut < b)
}
const midToken = (plain, offset) => /\S/.test(plain[offset - 1] || " ") && /\S/.test(plain[offset] || " ") && !/[\p{Script=Han}\p{Script=Thai}]/u.test(plain[offset])

// A state whose text is `md` as the serializer writes it, with the caret before the first `at` in the text (or at `n`).
function at(md, word, nth = 0) {
  const state = EditorState.create({doc: parse(md)})
  let from = -1, seen = 0
  state.doc.descendants((n, p) => {
    if (from >= 0 || !n.isText) return
    let i = -1
    while ((i = n.text.indexOf(word, i + 1)) >= 0) if (seen++ === nth) { from = p + i; return }
  })
  assert.ok(from >= 0, "the text has " + word)
  return {state, from, sel: TextSelection.create(state.doc, from)}
}
const plan = (md, word, nth) => { const c = at(md, word, nth); return {...splitAt(c.state, c.sel), plain: plainOf(c.state), state: c.state} }

// Both halves read back as themselves, and nothing was lost.
function exact(md, r) {
  assert.equal(r.ok, true, JSON.stringify(r))
  assert.equal(r.plain, md)
  assert.equal(insideSourceBlock(md, r.offset), false, "the op would refuse a cut inside a code fence, a comment or front matter")
  const {head, tail} = halves(md, r.offset)
  assert.equal(serialize(parse(head)), head, "the head reads back unchanged")
  assert.equal(serialize(parse(tail)), tail, "the tail reads back unchanged")
  assert.equal(countWords(parse(head)) + countWords(parse(tail)), countWords(parse(md)), "no word is lost or made")
  return {head, tail}
}

const prose = "First paragraph here.\n\nThe tide had gone out and the boats lay over on their sides.\n\nLast paragraph.\n"

test("a cursor in the middle of a paragraph cuts exactly there", () => {
  const r = plan(prose, "and the boats")
  const {head, tail} = exact(prose, r)
  assert.equal(r.moved, null)
  assert.equal(head, "First paragraph here.\n\nThe tide had gone out\n")
  assert.equal(tail, "and the boats lay over on their sides.\n\nLast paragraph.\n")
})

test("a cursor inside a word cuts at the edge of the word, and one in the spaces before a word cuts before the word", () => {
  const inside = exact(prose, plan(prose, "oats", 0)) // the cursor three letters into "boats": the word is not cut
  assert.equal(inside.tail.startsWith("boats lay"), true)
  const c = at(prose, "out and")
  const sp = TextSelection.create(c.state.doc, c.from + 3) // between "out" and the space
  const r = {...splitAt(c.state, sp), plain: prose}
  assert.equal(exact(prose, r).tail.startsWith("and the boats"), true)
})

test("the start of a paragraph cuts between the paragraphs", () => {
  const r = plan(prose, "The tide")
  assert.equal(r.moved, null)
  const {head, tail} = exact(prose, r)
  assert.equal(head, "First paragraph here.\n")
  assert.equal(tail.startsWith("The tide"), true)
  // ... and so does the end of the one before
  const e = at(prose, "here.")
  const end = splitAt(e.state, TextSelection.create(e.state.doc, e.from + 5))
  assert.equal(end.offset, r.offset)
})

test("no text before the cursor, or none after it, is said and not cut", () => {
  const s = EditorState.create({doc: parse(prose)})
  assert.deepEqual(splitAt(s, TextSelection.atStart(s.doc)), {ok: false, why: "start"})
  assert.deepEqual(splitAt(s, TextSelection.atEnd(s.doc)), {ok: false, why: "end"})
  const spaces = EditorState.create({doc: parse("Hello.\n\nWorld.\n")})
  assert.equal(splitAt(spaces, TextSelection.create(spaces.doc, 1)).why, "start")
})

test("a selection cuts where it starts, and changes nothing", () => {
  const c = at(prose, "tide had gone")
  const sel = TextSelection.create(c.state.doc, c.from, c.from + "tide had gone out".length)
  const r = {...splitAt(c.state, sel), plain: prose}
  assert.equal(exact(prose, r).tail.startsWith("tide had gone"), true)
  const back = TextSelection.create(c.state.doc, c.from + "tide had gone out".length, c.from) // made backwards
  assert.equal(splitAt(c.state, back).offset, r.offset)
})

test("inside a list or a quote the cut goes to the edge of the block, never through its markers", () => {
  const md = "Before.\n\n- one apple\n- two pears\n\nAfter.\n\n> A quoted line.\n\nEnd.\n"
  const l = plan(md, "pears")
  const a = exact(md, l)
  assert.equal(l.moved, "block")
  assert.equal(a.head, "Before.\n")
  assert.equal(a.tail.startsWith("- one apple"), true)
  const q = plan(md, "quoted")
  const b = exact(md, q)
  assert.equal(q.moved, "block")
  assert.equal(b.tail.startsWith("> A quoted line."), true)
  // The very end of the last item cuts after the list.
  const e = at(md, "pears")
  const end = splitAt(e.state, TextSelection.create(e.state.doc, e.from + 5))
  assert.equal(end.moved, "block")
  const c = exact(md, {...end, plain: md})
  assert.equal(c.tail.startsWith("After."), true)
  assert.equal(c.head.endsWith("- two pears\n"), true)
})

test("a heading or a code block is not cut through; the cut is before it", () => {
  const md = "Some prose first.\n\n## The second part\n\nMore prose.\n\n```js\nlet a = 1\n```\n\nOutro.\n"
  const h = plan(md, "second")
  assert.equal(h.moved, "block")
  assert.equal(exact(md, h).tail.startsWith("## The second part"), true)
  const code = plan(md, "let a")
  assert.equal(code.moved, "block")
  assert.equal(exact(md, code).tail.startsWith("```js"), true)
})

test("a scene break chosen whole cuts after it, and a gap beside one cuts there", () => {
  const md = "Closing words.\n\n* * *\n\nOpening words.\n"
  const state = EditorState.create({doc: parse(md)})
  let pos = -1
  state.doc.forEach((n, p) => { if (n.type.name === "scene_break") pos = p })
  const r = splitAt(state, NodeSelection.create(state.doc, pos))
  assert.equal(r.ok, true)
  const {head, tail} = halves(plainOf(state), r.offset)
  assert.equal(head, "Closing words.\n\n* * *\n")
  assert.equal(tail, "Opening words.\n")
  assert.equal(serialize(parse(head)), head)
})

test("the cursor in emphasis or a link goes to the edge of it, so no asterisk or bracket is left behind", () => {
  const md = "He said *very quiet words* and a [long link text](https://x.test/a) after.\n"
  for (const word of ["quiet", "link text"]) {
    const r = plan(md, word)
    assert.equal(r.moved, "inline", word)
    const {head, tail} = exact(md, r) // a stray `*` or `[` in either half would not read back as itself
    assert.equal(head.trim().length > 0 && tail.trim().length > 0, true, JSON.stringify({head, tail}))
  }
})

test("the start of an emphasised run is a place it can be cut, and the end of one", () => {
  const md = "Plain words then *emphasised words* and plain again.\n"
  const a = plan(md, "emphasised")
  assert.equal(a.moved, null)
  assert.equal(exact(md, a).tail.startsWith("*emphasised words*"), true)
  const e = at(md, "emphasised words")
  const end = {...splitAt(e.state, TextSelection.create(e.state.doc, e.from + "emphasised words".length)), plain: md}
  assert.equal(end.moved, null)
  assert.equal(exact(md, end).tail.startsWith("and plain again."), true)
})

test("a tail that would be read as a list is not offered where it would change", () => {
  const md = "The plan was 1. first, 2. second, and more words follow it here.\n"
  const one = plan(md, "1. first")
  assert.equal(one.ok, false) // the only other places are the ends of the one paragraph, which have no text beside them
  assert.equal(one.why, "inside")
  const two = "The plan was 1. first, 2. second, and more words follow it here.\n\nAnother paragraph.\n"
  const r = plan(two, "1. first")
  assert.equal(r.ok, true)
  assert.equal(r.moved, "inline")
  assert.equal(exact(two, r).tail, "Another paragraph.\n")
})

test("a cursor inside ghosted text goes to the edge of the ghost", () => {
  const md = "Alpha <scriobh-ghost>hidden words here</scriobh-ghost> omega and the rest.\n"
  const r = plan(md, "words here")
  assert.equal(r.ok, true)
  assert.equal(r.moved, "inline")
  const {head, tail} = halves(md, r.offset)
  const open = md.indexOf("<scriobh-ghost>"), close = md.indexOf("</scriobh-ghost>") + "</scriobh-ghost>".length
  assert.equal(r.offset === open || r.offset === close || halves(md, r.offset).head.trim().length === "Alpha".length, true, JSON.stringify({head, tail}))
  assert.equal(head.includes("<scriobh-ghost>") === head.includes("</scriobh-ghost>"), true, "both tags, or neither")
  assert.equal(tail.includes("<scriobh-ghost>") === tail.includes("</scriobh-ghost>"), true)
})

test("the alternatives comment is not text: a cut before only it is 'nothing after'", () => {
  const md = "Body of the chapter.\n\n<!-- scriobh:alts {\"version\":1,\"sig\":\"a\"} -->\n"
  const state = EditorState.create({doc: parse(md)})
  assert.deepEqual(splitAt(state, TextSelection.atEnd(state.doc)), {ok: false, why: "end"})
  const mid = "One two three.\n\n<!-- scriobh:alts {\"version\":1,\"sig\":\"a\"} -->\n"
  const m = plan(mid, "two")
  assert.equal(m.ok, true)
  assert.equal(halves(mid, m.offset).tail.startsWith("two three."), true)
  // The comment stays in the tail, after the text; the op divides it.
  assert.equal(halves(mid, m.offset).tail.includes("scriobh:alts"), true)
})

const everyPlace = (md, label, o = {}) => {
  const doc = parse(md), plain = serialize(doc)
  const state = EditorState.create({doc: parse(plain)})
  let ok = 0, moved = 0
  for (let pos = 1; pos < state.doc.content.size; pos++) {
    if (!state.doc.resolve(pos).parent.inlineContent) continue
    const sel = TextSelection.create(state.doc, pos)
    const r = splitAt(state, sel)
    if (!r.ok) { assert.equal(["start", "end", "inside", "unplaced"].includes(r.why), true, `${label} at ${pos}: ${JSON.stringify(r)}`); continue }
    const at = `${label} at ${pos} (offset ${r.offset})`
    assert.equal(insideSourceBlock(plain, r.offset), false, `the op refuses ${at}`)
    if (o.words !== false) assert.equal(midToken(plain, r.offset), false, `a word, number or address is cut in the middle: ${at}: ${JSON.stringify(plain.slice(r.offset - 12, r.offset) + "|" + plain.slice(r.offset, r.offset + 12))}`)
    const {head, tail} = halves(plain, r.offset)
    assert.equal(serialize(parse(head)), head, `head of ${at}: ${JSON.stringify(head.slice(-40))}`)
    assert.equal(serialize(parse(tail)), tail, `tail of ${at}: ${JSON.stringify(tail.slice(0, 40))}`)
    if (o.words !== false) assert.equal(countWords(parse(head)) + countWords(parse(tail)), countWords(state.doc), `words of ${at}`)
    assert.equal(/[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/.test(head + tail), false, `a surrogate pair is cut at ${at}`)
    assert.equal(head.includes("<scriobh-ghost>") === head.includes("</scriobh-ghost>"), true, `ghost tags at ${at}`)
    assert.equal((head.match(/<b>/g) || []).length, (head.match(/<\/b>/g) || []).length, `html tags in the head at ${at}`)
    assert.equal((tail.match(/<b>/g) || []).length, (tail.match(/<\/b>/g) || []).length, `html tags in the tail at ${at}`)
    ok++
    if (r.moved) moved++
  }
  return {ok, moved}
}

test("every place in a chapter of every kind of block gives two halves that read back, that the op accepts, and that hold every word", () => {
  const md = [
    "# Title", "",
    "Opening *emphasis* and **strong text** with `code` and a [link](https://x.test) and ~~struck~~ words.", "",
    "- item *one*", "- item two with [[Wiki Link]]", "  - nested item", "", "1. first", "2. second", "",
    "> A quote with *emphasis* inside.", "", "* * *", "", "Café ☃ snow 😀 smile\\_ end, 1. not a list, # not a heading.", "",
    "```", "fenced", "```", "", "After the fence words.", "", "<!-- a comment -->", "", "After the comment words.", "",
    "Alpha <scriobh-ghost>ghost words</scriobh-ghost> omega.", "",
    "- [ ] a task", "- [x] done task", "", "A hard  ", "break line.", "", "Pi is 3.14159 at https://x.test/a_b and a well-known road, with $x +y$ and <b>bold words</b> and *em <b>both here</b> more* end.", "", "Last paragraph of it all.", ""].join("\n")
  const r = everyPlace(md, "mix")
  assert.equal(r.ok > 100, true, "places found: " + r.ok)
  assert.equal(r.moved > 10, true, "places moved: " + r.moved)
})

test("a cut right after a code fence, an HTML comment or front matter is after the line break that ends it", () => {
  for (const [md, word] of [["One.\n\n```\ncode\n```\n\nAfter para.\n", "After"], ["One.\n\n<!-- c -->\n\nAfter para.\n", "After"], ["---\nt: x\n---\n\nAfter para.\n", "After"]]) {
    const r = plan(md, word)
    assert.equal(r.ok, true, JSON.stringify(r))
    const {tail} = exact(md, r)
    assert.equal(tail, "After para.\n")
    assert.equal(md[r.offset - 1], "\n", "the cut is after a line break")
  }
})

test("an address, a number, a word with a hyphen, inline math and inline HTML are not cut in the middle", () => {
  const cases = [
    ["The value was 3.14159 or so, and then more words follow here.\n\nNext.\n", "14159"],
    ["See https://example.test/a/b_c for the whole story told over again.\n\nNext.\n", "example"],
    ["A well-known road led to the harbour and then on to the town.\n\nNext.\n", "known"],
    ["Let $x +y$ be the sum of both parts here, and then go on talking.\n\nNext.\n", "+y"],
    ["It was <b>bold words</b> that he said, and then he went on his way.\n\nNext.\n", "words"],
    ["It was <b>bold</b>words that he said, and then he went on his way.\n\nNext.\n", "words"]]
  for (const [md, word] of cases) {
    const r = plan(md, word)
    assert.equal(r.ok, true, md + JSON.stringify(r))
    const {head, tail} = exact(md, r)
    assert.equal(midToken(r.plain, r.offset), false, `cut in the middle of a token: ${JSON.stringify(head.slice(-14))} | ${JSON.stringify(tail.slice(0, 14))}`)
    assert.equal((head.match(/<b>/g) || []).length, (head.match(/<\/b>/g) || []).length, "html tags balance in the head")
  }
  const html = "It was <b>bold words</b> that he said.\n\nNext.\n"
  const r = plan(html, "words")
  assert.equal(r.moved === "inline" || r.offset > html.indexOf("</b>"), true, "the caret inside a tag pair moves to its edge")
})

test("a letter outside the basic plane is one character: a word of them is not cut", () => {
  const md = "𝐀𝐁𝐂 words 😀😀 and 𝒳𝒴 more 👍🏽 text.\n\nSecond para.\n"
  const r = everyPlace(md, "astral")
  assert.equal(r.ok > 10, true)
})

test("a caret after a hard line break cuts after it, and the break's backslash goes with the line break", () => {
  const md = "Line one\\\nline two words here.\n\nEnd.\n"
  for (const word of ["line two", "ne two"]) {
    const r = plan(md, word)
    assert.equal(r.ok, true, JSON.stringify(r))
    const {head, tail} = exact(md, r)
    assert.equal(head, "Line one\n", "no stray backslash at the end of the first part")
    assert.equal(tail.startsWith("line two"), true)
  }
  // the end of the first line, before the break, cuts in the same place
  const c = at(md, "one")
  const end = splitAt(c.state, TextSelection.create(c.state.doc, c.from + 3))
  const {head} = halves(plainOf(c.state), end.offset)
  assert.equal(head, "Line one\n")
  everyPlace("Poem line one  \nline two goes on  \nline three ends here.\n\nAnother paragraph.\n", "poem")
})

test("a text written without spaces is cut between characters, and 'start' is said only at the start", () => {
  for (const md of ["这是一个很长的中文段落没有空格而且还在继续。\n\nNext paragraph.\n", "ข้อความภาษาไทยที่ยาวมากและไม่มีช่องว่างเลย\n\nNext paragraph.\n", "吾輩は猫である。名前はまだ無い。どこで生れたかとんと見当がつかぬ。\n\nNext.\n"]) {
    const state = EditorState.create({doc: parse(md)})
    const first = state.doc.child(0), size = first.content.size
    assert.deepEqual(splitAt(state, TextSelection.create(state.doc, 1)), {ok: false, why: "start"})
    const mid = splitAt(state, TextSelection.create(state.doc, 1 + Math.floor(size / 2)))
    assert.equal(mid.ok, true, JSON.stringify(mid))
    const {head, tail} = halves(plainOf(state), mid.offset)
    assert.equal(head.trim().length > 3 && tail.trim().length > 3 && tail.includes("\n\n") === true, true, JSON.stringify({head, tail}))
    assert.equal(mid.moved, null, "the cut is where the caret was")
    const second = splitAt(state, TextSelection.create(state.doc, 2))
    assert.equal(second.ok, true, "a cut after the first character is a cut: " + JSON.stringify(second))
    assert.equal(everyPlace(md, "nospace", {words: false}).ok > 5, true)
  }
})

test("the answer says where a moved cut went, and how far", () => {
  const md = "Before.\n\n- one apple\n- two pears\n\nAfter.\n\n## A heading\n\nMore.\n"
  const l = plan(md, "pears")
  assert.deepEqual([l.moved, l.at, l.what, l.text], ["block", "before", "list", "one apple"])
  const h = plan(md, "heading")
  assert.deepEqual([h.moved, h.at, h.what, h.text], ["block", "before", "heading", "A heading"])
  const w = plan(md, "pears")
  assert.equal(w.dist > 0, true)
  const m = plan("He said *very quiet words* and went on talking about nothing in particular.\n\nNext.\n", "quiet")
  assert.deepEqual([m.moved, m.at], ["inline", undefined])
  assert.equal(typeof m.text, "string")
  assert.equal(plan(prose, "and the boats").moved, null)
})

test("in a text that cannot be written again, the place is found in the text as stored, and it is not changed", () => {
  const stored = "Start _emphasised_ words.\n\n---\n\n* bullet one\n* bullet two\n\nFinal paragraph here, with more words in it.\n\nLast one.\n"
  const state = EditorState.create({doc: parse(stored)})
  const find = word => { let p = -1; state.doc.descendants((n, pos) => { if (p < 0 && n.isText && n.text.includes(word)) p = pos + n.text.indexOf(word) }); return p }
  // between paragraphs, after a list the file spells with *
  let r = splitAt(state, TextSelection.create(state.doc, find("Final")), stored)
  assert.equal(r.ok, true, JSON.stringify(r))
  let h = halves(stored, r.offset)
  assert.equal(h.head, "Start _emphasised_ words.\n\n---\n\n* bullet one\n* bullet two\n", "the first part is the stored text, spelt as it was")
  assert.equal(h.tail, "Final paragraph here, with more words in it.\n\nLast one.\n")
  // in the middle of a paragraph
  r = splitAt(state, TextSelection.create(state.doc, find("with more")), stored)
  assert.equal(r.ok, true, JSON.stringify(r))
  h = halves(stored, r.offset)
  assert.equal(h.head.endsWith("Final paragraph here,\n"), true, JSON.stringify(h))
  assert.equal(h.tail.startsWith("with more words in it."), true)
  // in a list, before it
  r = splitAt(state, TextSelection.create(state.doc, find("bullet two")), stored)
  assert.equal(r.moved, "block")
  assert.equal(halves(stored, r.offset).tail.startsWith("* bullet one"), true)
  // the editor and the stored text are not the same document: it is said, not guessed
  const other = EditorState.create({doc: parse("Only one paragraph.\n\nTwo.\n\nThree.\n")})
  assert.deepEqual(splitAt(other, TextSelection.create(other.doc, 22), stored).why, "stored")
})

test("a caret in the first word of a chapter is not told there is nothing before it: the word's other end is the place", () => {
  const md = "Hello world and more words follow.\n\nNext paragraph.\n"
  const c = at(md, "ello")
  const r = splitAt(c.state, TextSelection.create(c.state.doc, c.from + 1)) // between "e" and "l": nearer the start of the word
  assert.equal(r.ok, true, JSON.stringify(r))
  assert.equal(r.moved, "word")
  assert.equal(halves(plainOf(c.state), r.offset).tail.startsWith("world and more"), true)
  // truly at the start of the chapter it is said
  assert.deepEqual(splitAt(c.state, TextSelection.create(c.state.doc, 1)), {ok: false, why: "start"})
  // the only word of the only paragraph: no place has text on both sides
  const one = EditorState.create({doc: parse("Hello\n")})
  assert.equal(splitAt(one, TextSelection.create(one.doc, 3)).why, "inside")
})
