// Where a chapter may be cut (the editor's side of `doc.split`, docs/FEATURES.md F-1224 and F-1225).
//
// The op names the cut as an offset into the stored Markdown and cuts that text byte for byte (it takes the blank space off
// the end of the first part and the line breaks off the start of the second, and nothing else). So a place is offered only
// where both halves are still what the editor would have written for them: never between the `- ` of a list item and its
// words, inside `*emphasis*`, a link, an HTML tag pair, a math span or a ghost, in a code block, or in the middle of a word.
// A caret in one of those goes to the nearest place that is allowed, and the answer says where it went.
//
// Offsets come from the editor's own serializer (`offsetOf`) once the stored text is the serializer's spelling. For a text that
// cannot be written again (a chapter too long to be edited), `stored` is that text: the place is found in it, as it is, from
// where the parser says its blocks lie.

import {EditorState} from "prosemirror-state"
import {parse, serialize, blockSpans} from "./markdown.js"
import {schema} from "./schema.js"
import {cleanText} from "./paste.js"
import {offsetOf, plainOf} from "./alts.js"

// Scripts written without spaces between words: a cut may fall between any two of their characters. A character that may not
// begin a line (a closing bracket, a full stop, a combining mark) or end one (an opening bracket) is kept with its neighbour.
const NOSPACE = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Thai}\p{Script=Lao}\p{Script=Khmer}\p{Script=Myanmar}]/u
const NOSTART = /^[\p{Pe}\p{Pf}\p{Po}\p{M}‍️]$/u
const NOEND = /^[\p{Ps}\p{Pi}]$/u
const VOID = /^(br|hr|img|wbr|input|area|base|col|embed|link|meta|source|track)$/
const SP = /[ \t\r\n]/ // the blank space the op trims; a no-break space joins its words
const norm = s => s.replace(/\s+$/, "")
const wrap = n => schema.nodes.doc.create(null, n)

// The text before a cut as the op keeps it: the blank space at its end taken off, and with it the backslash of a hard line break
// the cut came right after (the break goes with the line break: it would be a stray character at the end of the first part).
// An escaped backslash (an even run) is text. The same rule as `trimHead` in internal/domain/docs.
export function headOf(raw) {
  let h = raw.replace(/[ \t\r\n]+$/, "")
  if (/\n/.test(raw.slice(h.length)) && (/\\+$/.exec(h) || [""])[0].length % 2 === 1) h = h.slice(0, -1).replace(/[ \t]+$/, "")
  return h
}

// The offsets inside which a cut would break something, for the inline content of paragraph `p` (`t` is its text, one
// character to a position): each pair of matching HTML tags with what they hold (a ghost's tags are such a pair), and each
// inline math span, `$...$`.
function heldSpans(p, t) {
  const spans = [], stack = []
  p.forEach((c, off) => {
    if (c.type.name !== "source_inline") return
    const m = /^<(\/?)([A-Za-z][\w:-]*)\b[^>]*?(\/?)>$/.exec(c.attrs.raw)
    if (!m || m[3] || VOID.test(m[2].toLowerCase())) return
    const name = m[2].toLowerCase()
    if (!m[1]) stack.push([name, off])
    else for (let i = stack.length - 1; i >= 0; i--) if (stack[i][0] === name) { spans.push([stack[i][1], off + c.nodeSize]); stack.length = i; break }
  })
  const re = /\$[^\s$](?:[^$]*[^\s$])?\$(?!\d)/g
  let m
  while ((m = re.exec(t))) spans.push([m.index, m.index + m[0].length])
  return spans
}

// Which side's formatting (1 or -1) puts the marker for a cut at offset `k` of paragraph `p` outside every mark, so that the
// stored text cut there reads as the two paragraphs the editor would write; 0 when no side does.
function cutSide(p, k) {
  const whole = EditorState.create({doc: wrap(p)}), plain = plainOf(whole)
  const head = norm(cleanText(serialize(wrap(p.cut(0, k))))), tail = norm(cleanText(serialize(wrap(p.cut(k)))))
  for (const side of [1, -1]) {
    const off = offsetOf(whole, 1 + k, side)
    if (off >= 0 && norm(headOf(plain.slice(0, off))) === head && norm(plain.slice(off).replace(/^[\r\n]+/, "")) === tail) return side
  }
  return 0
}

// The text before and after a cut between two top-level blocks, as the serializer writes them; or why there is none.
function around(state, cut) {
  const doc = state.doc, last = doc.lastChild
  const end = last && last.type.name === "source_block" && last.attrs.kind === "alts" ? doc.content.size - last.nodeSize : doc.content.size
  const head = norm(cleanText(serialize(doc.cut(0, cut)))), tail = cleanText(serialize(doc.cut(cut)))
  if (!head.trim()) return {why: "start"}
  if (cut >= end || !tail.trim()) return {why: "end"}
  return {head, tail}
}

// The offset of such a cut in the serializer's text: the end of the line that ends the block before it, line break included
// (a cut before that line break would be inside a code fence, a comment or front matter, which the op refuses to cut).
function plainBoundary(plain, a) {
  const at = a.head.length, rest = plain.slice(at)
  if (!plain.startsWith(a.head) || !/^(\r?\n|$)/.test(rest) || norm(rest.replace(/^[\r\n]+/, "")) !== norm(a.tail)) return -1
  return at + (plain[at] === "\n" ? 1 : 0)
}

// The same in the stored text, spelt as it was written: the end of the previous block, found from where the parser says the
// blocks lie, if the blocks are the editor's children one for one and the two beside the cut read as the editor's own.
function storedBoundary(doc, cut, stored) {
  const {text, blocks} = blockSpans(stored), i = doc.resolve(cut).index()
  if (blocks.length !== doc.childCount || i < 1 || i >= blocks.length) return -1
  for (const j of [i - 1, i]) if (cleanText(serialize(parse(text.slice(blocks[j].start, blocks[j].end)))) !== cleanText(serialize(wrap(doc.child(j))))) return -1
  return blocks[i - 1].end
}

// A cut inside paragraph `i` at offset `k` of its content, in the stored text: the offsets just after a space are tried, and
// the first at which the stored text cut there reads as the editor's two halves is the place.
function storedInside(doc, i, k, stored) {
  const {text, blocks} = blockSpans(stored), p = doc.child(i)
  if (blocks.length !== doc.childCount) return -1
  const b = blocks[i], src = text.slice(b.start, b.end).replace(/\n+$/, "")
  if (cleanText(serialize(parse(src))) !== cleanText(serialize(wrap(p)))) return -1
  const head = norm(cleanText(serialize(wrap(p.cut(0, k))))), tail = norm(cleanText(serialize(wrap(p.cut(k)))))
  for (let o = 1; o < src.length; o++) {
    if (/\s/.test(src[o]) || !/\s/.test(src[o - 1])) continue
    const h = norm(headOf(src.slice(0, o))), t = norm(src.slice(o).replace(/^[\r\n]+/, ""))
    if (h && cleanText(serialize(parse(h))).replace(/\n$/, "") === head && cleanText(serialize(parse(t))).replace(/\n$/, "") === tail) return b.start + o
  }
  return -1
}

// The start of what follows the cut, for telling the writer: its first words.
function words(s) {
  const w = s.replace(/\s+/g, " ").trim()
  return w.length > 40 ? w.slice(0, 40).replace(/\s+\S*$/, "") + "…" : w
}

// Where the selection says a chapter is cut. {ok: true, offset, pos, moved, at, what, dist, text}: `offset` into the text the
// op will cut (the serializer's, or `stored` when that is given), `pos` the document position, `moved` null, "block", "inline"
// (out of formatting) or "word" (to the other end of the word the caret was in, the near one having no text beside it) when the
// cut is not where the caret was (`at` "before" or "after" and `what` "list", "quote", "heading", "code" or
// "block" for a block; `dist` the distance in positions), `text` the first words of what follows. Or {ok: false, why}: "start"
// or "end" (no text on that side), "inside" (the caret is in something no edge of which has text beside it), "unplaced" (the
// text could not be matched exactly), "stored" (the same, for a stored text that cannot be written again). A selection cuts
// where it starts; a scene break chosen whole cuts after it.
export function splitAt(state, sel = state.selection, stored) {
  const doc = state.doc
  let pos = sel.from
  if (sel.node && sel.$from.depth === 0) pos = sel.to
  const $p = doc.resolve(pos)
  const miss = stored == null ? "unplaced" : "stored"
  const block = (cut, extra) => {
    const a = around(state, cut)
    if (a.why) return {ok: false, why: a.why}
    const offset = stored == null ? plainBoundary(plainOf(state), a) : storedBoundary(doc, cut, stored)
    if (offset < 0) return {ok: false, why: miss}
    let next = doc.resolve(cut).nodeAfter
    while (next && !next.isTextblock && next.firstChild) next = next.firstChild // the first line of whatever follows
    return {ok: true, offset, pos: cut, moved: null, dist: Math.abs(cut - pos), text: words(next ? next.textContent : ""), ...extra}
  }
  if ($p.depth === 0) return block(pos)
  const top = $p.node(1), n = top.type.name
  if ($p.depth > 1 || n !== "paragraph") {
    // A list, a quote, a heading, code, a note: never through the middle of one. A list or a quote with the caret at its very
    // end is cut after; anything else before.
    const what = /list$/.test(n) ? "list" : n === "blockquote" ? "quote" : n === "heading" ? "heading" : /^(code|source)_block$/.test(n) ? "code" : "block"
    const end = pos + $p.depth === $p.after(1) && (what === "list" || what === "quote")
    return block(end ? $p.after(1) : $p.before(1), {moved: "block", at: end ? "after" : "before", what})
  }
  const start = $p.start(1), size = top.content.size, index = $p.index(0), t = top.textBetween(0, size, "", "￼")
  if (t.length !== size) return {ok: false, why: miss}
  const spans = heldSpans(top, t), isGap = []
  for (let i = 0; i < size; i++) isGap[i] = SP.test(t[i])
  top.forEach((c, off) => { if (c.type.name === "hard_break") isGap[off] = true }) // a hard line break ends a word as a space does
  const prev = i => (i >= 2 && (t.charCodeAt(i - 1) & 0xfc00) === 0xdc00 && (t.charCodeAt(i - 2) & 0xfc00) === 0xd800 ? t.slice(i - 2, i) : t[i - 1]) // the character before i, whole
  const next = i => ((t.charCodeAt(i) & 0xfc00) === 0xd800 && i + 1 < size ? t.slice(i, i + 2) : t[i]) // and the one at i
  const skip = k => { // after the blank space, and after a hard line break and the space that follows it
    while (k < size && isGap[k]) { const hb = !SP.test(t[k]); k++; if (hb) while (k < size && SP.test(t[k])) k++ }
    return k
  }
  const edges = k => { // the ends of the word, number or address the place is in
    let a = k, b = k
    while (a > 0 && !isGap[a - 1]) a -= prev(a).length
    while (b < size && !isGap[b]) b += next(b).length
    return [a, b]
  }
  const inWord = k => k > 0 && k < size && !isGap[k] && !isGap[k - 1] && !((NOSPACE.test(prev(k)) || NOSPACE.test(next(k))) && !NOEND.test(prev(k)) && !NOSTART.test(next(k)))
  const settle = k => { // a cut sits between words (or characters, where there are no spaces), after a line break, and in no grapheme
    while (k > 0 && k < size && (/^[\p{M}\u200d\ufe0f]$/u.test(next(k)) || (NOSPACE.test(prev(k)) && NOSTART.test(next(k))))) k += next(k).length
    while (k > 0 && k < size && NOSPACE.test(next(k)) && NOEND.test(prev(k))) k -= prev(k).length
    k = skip(k)
    if (inWord(k)) { const [a, b] = edges(k); k = skip(k - a <= b - k ? a : b) } // the middle of a word: to the nearer end of it
    if (!t.slice(0, k).trim()) return 0
    return t.slice(k).trim() ? k : size
  }
  const raw = pos - start, trulyStart = !t.slice(0, raw).trim(), trulyEnd = !t.slice(raw).trim()
  const want = settle(pos - start)
  const cands = new Set([want, 0, size]), wordEnds = new Set()
  top.forEach((c, off) => { cands.add(off); cands.add(off + c.nodeSize) })
  spans.forEach(([a, b]) => { cands.add(a); cands.add(b) })
  if (inWord(Math.min(Math.max(raw, 0), size))) edges(raw).forEach(e => { const k = settle(e); cands.add(k); wordEnds.add(k) }) // both ends of the word the caret is in
  const order = [...new Set([...cands].map(settle))].sort((a, b) => Math.abs(a - want) - Math.abs(b - want) || a - b)
  let first = null
  for (const k of order) {
    const extra = {moved: k === want ? null : wordEnds.has(k) ? "word" : "inline"}
    let r
    if (k === 0 || k === size) r = block(k ? $p.after(1) : $p.before(1), extra)
    else {
      const side = spans.some(([a, b]) => k > a && k < b) ? 0 : cutSide(top, k)
      if (!side) continue // inside formatting or a held span: the next place
      const cut = start + k
      const offset = stored == null ? offsetOf(state, cut, side) : storedInside(doc, index, k, stored)
      if (offset < 0) continue
      r = {ok: true, offset, pos: cut, dist: Math.abs(cut - pos), text: words(t.slice(k).replace(/\ufffc/g, "")), ...extra}
    }
    // The place asked for answers for itself when the caret is truly at that end; a place moved to that has no text beside it is passed over.
    if (r.ok || (k === want && !((r.why === "start" && !trulyStart) || (r.why === "end" && !trulyEnd)))) return r
    first = first || r
  }
  return first ? {ok: false, why: first.why === "start" || first.why === "end" ? "inside" : first.why} : {ok: false, why: miss}
}
