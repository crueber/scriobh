// The editor's side of alternatives, ghosts and the stash (docs/EDITOR-SPIKE.md, "Alternatives, ghosts and the stash").
// The server names text by offsets into the Markdown source of the body; the editor holds a tree. This is the map
// between the two, built the way the Source toggle's cursor map is: private-use marker characters put into a scratch copy
// of the text (a document to serialise, or Markdown to parse), found again, and taken out. Nothing here changes the schema
// or what is stored.

import {TextSelection} from "prosemirror-state"
import {Fragment} from "prosemirror-model"
import {Mapping, StepMap} from "prosemirror-transform"
import {parse, serialize} from "./markdown.js"
import {schema} from "./schema.js"
import {cleanText} from "./paste.js"

const BASE = 0xE100          // the fallback markers for rangesOf: BASE + 2k opens range k, BASE + 2k + 1 closes it
const CODED_MAX = 3000       // ... which only has room for this many ranges inside the private-use area
const CONTEXT = 24

// Characters that are not in `text`, to stand for places in it: the private-use area is large, and a text that really holds one
// of these code points must not be able to be mistaken for a marker. [] when there are not enough.
function absent(text, n) {
  const out = []
  for (let c = 0xE000; c < 0xF8FF && out.length < n; c++) if (text.indexOf(String.fromCharCode(c)) < 0) out.push(String.fromCharCode(c))
  return out.length < n ? [] : out
}

// The serialization of the document, as the host compares it with the stored text.
export function plainOf(state) { return cleanText(serialize(state.doc)) }

// The Markdown source of the text between two positions, with source on each side, exactly as the serializer writes it (so a
// selection across `*emphasis*` quotes the asterisks that are inside it). The context grows until quote and context together
// are found once in the text: a quote that occurs twice must still say which one. Null when the text cannot be named exactly:
// the markers did not come back once each, or they changed the text around them.
export function sourceOf(state, from, to, ctx = CONTEXT) {
  ctx = Math.max(1, ctx)
  const plain = plainOf(state)
  const [a, b] = absent(plain, 2)
  if (!a) return null
  const tr = state.tr
  // Each marker takes the marks of the text it touches (the opener those of the text after it, the closer those of the
  // text before it), so it lands inside emphasis rather than beside its asterisks.
  const marker = (pos, ch, side) => {
    const $p = state.doc.resolve(pos), n = side > 0 ? $p.nodeAfter : $p.nodeBefore
    return schema.text(ch, n && n.isText ? n.marks : [])
  }
  tr.insert(to, marker(to, b, -1))
  tr.insert(from, marker(from, a, 1))
  const md = cleanText(serialize(tr.doc))
  const i = md.indexOf(a), j = md.indexOf(b)
  if (i < 0 || j < i || md.indexOf(a, i + 1) >= 0 || md.indexOf(b, j + 1) >= 0) return null
  if (md.slice(0, i) + md.slice(i + 1, j) + md.slice(j + 1) !== plain) return null
  const quote = md.slice(i + 1, j)
  for (let c = ctx; ; c *= 2) {
    const before = md.slice(Math.max(0, i - c), i), after = md.slice(j + 1, j + 1 + c), whole = before + quote + after
    if (plain.indexOf(whole) === plain.lastIndexOf(whole)) return {quote, before, after}
    if (c >= 4096 || c >= plain.length) return null
  }
}

// Where a position falls in the Markdown source (an offset into the text the serializer writes), for putting something
// in at the cursor. -1 when it cannot be told exactly.
export function offsetOf(state, pos) {
  const plain = plainOf(state)
  const [a] = absent(plain, 1)
  if (!a) return -1
  const $p = state.doc.resolve(pos), n = $p.nodeAfter
  const md = cleanText(serialize(state.tr.insert(pos, schema.text(a, n && n.isText ? n.marks : [])).doc))
  const i = md.indexOf(a)
  if (i < 0 || md.indexOf(a, i + 1) >= 0 || md.slice(0, i) + md.slice(i + 1) !== plain) return -1
  return i
}

// A character that is not in `text`, to stand for every marker at once (the private-use area is large, and a text that
// holds all of it cannot be told apart from itself).
function sentinel(text) {
  for (let c = 0xE000; c < 0xF8FF; c++) if (text.indexOf(String.fromCharCode(c)) < 0) return String.fromCharCode(c)
  return null
}

// Positions of the ranges [{start, end}] (UTF-16 offsets into `body`, the stored Markdown before the comment) in the
// document that `body` parses to, which is the editor's own while nothing has been typed since it was loaded or saved.
// An entry is null when its markers could not be found. One marker character stands for every end of every range; which
// is which follows from the order they come back in, so no number of ranges can run out of codes or be mistaken for
// text. If a marker is swallowed by the parse (inside a link address, say) the ranges are asked for again with a code of
// their own each, as far as the private-use area goes.
export function rangesOf(body, ranges) {
  const tagged = []
  ranges.forEach((r, k) => { tagged.push([r.start, 2 * k], [r.end, 2 * k + 1]) })
  tagged.sort((a, b) => a[0] - b[0] || a[1] - b[1])
  const S = sentinel(body)
  if (S) {
    let text = "", last = 0
    for (const [at] of tagged) { text += body.slice(last, at) + S; last = at }
    text += body.slice(last)
    const found = findAll(parse(text), c => c === S.charCodeAt(0))
    if (found.length === tagged.length) {
      const where = new Map()
      tagged.forEach(([, tag], rank) => where.set(tag, found[rank][0] - rank))
      return ranges.map((_, k) => {
        const a = where.get(2 * k), b = where.get(2 * k + 1)
        return a == null || b == null || b < a ? null : {from: a, to: b}
      })
    }
  }
  return ranges.length <= CODED_MAX ? rangesOfCoded(body, ranges) : ranges.map(() => null)
}

function findAll(doc, test) {
  const found = []
  doc.descendants((node, pos) => {
    if (!node.isText) return true
    const t = node.text
    for (let i = 0; i < t.length; i++) { const c = t.charCodeAt(i); if (test(c)) found.push([pos + i, c]) }
    return true
  })
  return found
}

function rangesOfCoded(body, ranges) {
  const marks = []
  ranges.forEach((r, k) => { marks.push([r.start, BASE + 2 * k], [r.end, BASE + 2 * k + 1]) })
  // Markers at one offset keep their order (an empty range cannot happen), and later offsets are inserted first.
  marks.sort((a, b) => b[0] - a[0] || b[1] - a[1])
  let text = body
  for (const [at, code] of marks) text = text.slice(0, at) + String.fromCharCode(code) + text.slice(at)
  const found = findAll(parse(text), c => c >= BASE && c < BASE + 2 * ranges.length)
  found.sort((a, b) => a[0] - b[0])
  const at = new Map()
  found.forEach(([pos, code], rank) => at.set(code, pos - rank))
  return ranges.map((_, k) => {
    const a = at.get(BASE + 2 * k), b = at.get(BASE + 2 * k + 1)
    return a == null || b == null || b < a ? null : {from: a, to: b}
  })
}

// The inline content of one line of Markdown, or null when it is not a single paragraph of text.
function inlineOf(md) {
  const d = parse(md)
  if (d.childCount !== 1 || d.firstChild.type.name !== "paragraph") return null
  return d.firstChild.content
}

export function makeAlts(view, hooks) {
  return {
    sourceOf: (from, to, ctx) => sourceOf(view.state, from, to, ctx),
    rangesOf,
    // How many positions the inline Markdown `md` takes once parsed, or -1 when it is not one paragraph of text.
    measure: md => { const c = inlineOf(md); return c ? c.size : -1 },
    offsetOf: pos => offsetOf(view.state, pos),
    // The serialization of the document: the stored text is this when the editor and the server agree on where things are.
    plain: () => plainOf(view.state),
    // Put new wording in place of [from, to) in one transaction, leaving the selection where it was (mapped), without
    // counting it as typing: the server has already stored it. `was` is the source text the server says it replaced; when
    // the editor does not hold those words between the two positions the editor is out of step with the server and nothing
    // is changed. Returns the new end, or -1.
    replace(from, to, md, was) {
      let content = inlineOf(md)
      if (!content) return -1
      const mine = typeof was === "string" ? inlineOf(was) : null
      if (typeof was === "string") {
        // The words, not the markup: where a range starts or ends at a run of emphasis the source names the asterisks and
        // the editor's positions do not.
        const here = view.state.doc.textBetween(from, to, "", "\ufffc"), said = mine ? mine.textBetween(0, mine.size, "", "\ufffc") : null
        if (said === null || here !== said) return -1
      }
      // The new wording stands where the old one stood, so it keeps the formatting around it (the emphasis it was in) but
      // not the formatting the old wording carried inside itself: a quote that includes its own asterisks is replaced by
      // text that says what it wants, and one that sits inside emphasis stays inside it.
      const old = view.state.doc.resolve(from).nodeAfter
      let base = old && old.isText ? old.marks : []
      if (base.length && typeof was === "string") {
        const first = mine && mine.firstChild && mine.firstChild.isText ? mine.firstChild.marks : []
        base = base.filter(m => !m.isInSet(first))
      }
      if (base.length) {
        const kids = []
        content.forEach(n => kids.push(n.isText ? base.reduce((nn, m) => nn.mark(m.addToSet(nn.marks)), n) : n))
        content = Fragment.fromArray(kids)
      }
      const sel = view.state.selection
      const tr = view.state.tr.replaceWith(from, to, content)
      tr.setMeta("addToHistory", false)
      tr.setSelection(sel.map(tr.doc, tr.mapping))
      hooks.silently(() => view.dispatch(tr))
      return from + content.size
    },
    // The decorations `set` made for `oldDoc`, carried over to `newDoc` through the one edit between them (the span the two
    // differ in), without drawing anything again.
    mapSet(set, oldDoc, newDoc) {
      if (oldDoc === newDoc) return set
      const s = oldDoc.content.findDiffStart(newDoc.content)
      if (s == null) return set
      const e = oldDoc.content.findDiffEnd(newDoc.content)
      let a = e.a, b = e.b
      const over = s - Math.min(a, b)
      if (over > 0) { a += over; b += over }
      return set.map(new Mapping([new StepMap([s, a - s, b - s])]), newDoc)
    },
    // The version the editor is at (it goes up with every edit), and holding it still for the host: while locked no edit of
    // the text goes through.
    version: hooks.version,
    lock: hooks.lock,
    // The last line of the document is the alternatives comment, held as one source block. Replace its text with the
    // stored line ("" removes the block), without touching the rest, the selection or the history.
    setComment(line) {
      const doc = view.state.doc, last = doc.lastChild
      const has = last && last.type.name === "source_block" && last.attrs.kind === "alts"
      if (has && last.textContent === line) return false
      const tr = view.state.tr
      const at = doc.content.size - last.nodeSize
      if (has && !line) tr.delete(at, doc.content.size)
      else if (has) tr.replaceWith(at, doc.content.size, schema.nodes.source_block.create({kind: "alts"}, schema.text(line)))
      else if (line) tr.insert(doc.content.size, schema.nodes.source_block.create({kind: "alts"}, schema.text(line)))
      else return false
      tr.setMeta("addToHistory", false)
      hooks.silently(() => view.dispatch(tr))
      return true
    },
    // The host has stored the text as of version `v` (what the editor holds, when none is named).
    settle: hooks.settle,
    // Select a range (positions) and scroll it into view.
    select(from, to) {
      view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, from, to)).scrollIntoView())
    }
  }
}
