// Leaving the Source view: the text typed there becomes the document. The change is made as the places where the two documents
// differ, not as the whole document (nor, unless there are very many places, as the one span from the first difference to the
// last): the history step then covers what Source changed and nothing else, so an Undo cannot take back, or rebase over, a
// wording or a stored line that the alternatives wrote quietly (addToHistory:false) in a part Source did not touch. The step
// is closed to the typing before it (closeHistory), and the caller closes it to what follows. When the pieces do not make the
// new document, the whole document is replaced, as before.
//
// Blocks are matched by their whole content (type, every attribute, marks, text: the JSON of the node), so a change of a
// heading's level, a link's address, a task's state, a code block's language or a list's first number is a change. The match is
// patience-style, which stays linear-ish: the common start and end are cut away, the blocks that occur once on each side and
// keep their order anchor the rest, and what lies between anchors is cut and anchored again; a stretch with no anchor is
// matched with a longest-common-subsequence table when it is small, and is one place when it is not. More than MAX_PIECES
// places make the one span from the first difference to the last.

import {Slice, Fragment} from "prosemirror-model"
import {ReplaceStep} from "prosemirror-transform"
import {closeHistory} from "prosemirror-history"

const TABLE = 250000
const MAX_PIECES = 200

const kids = node => { const o = []; node.forEach(c => o.push(c)); return o }
const key = n => JSON.stringify(n.toJSON())

// The longest increasing run of `seq` (positions in the new list, in the old list's order), as indexes into seq.
function lis(seq) {
  const tails = [], prev = new Array(seq.length)
  for (let i = 0; i < seq.length; i++) {
    let lo = 0, hi = tails.length
    while (lo < hi) { const mid = (lo + hi) >> 1; if (seq[tails[mid]] < seq[i]) lo = mid + 1; else hi = mid }
    prev[i] = lo ? tails[lo - 1] : -1
    tails[lo] = i
  }
  const out = []
  for (let i = tails.length ? tails[tails.length - 1] : -1; i >= 0; i = prev[i]) out.push(i)
  return out.reverse()
}

// [from, to) of a and [from, to) of b that differ, as a list of places, over the keys ka and kb between lo/hi bounds.
function places(ka, kb, a0, a1, b0, b1, out) {
  const stack = [[a0, a1, b0, b1]]
  const done = []
  while (stack.length) {
    let [i0, i1, j0, j1] = stack.pop()
    while (i0 < i1 && j0 < j1 && ka[i0] === kb[j0]) { i0++; j0++ }
    while (i1 > i0 && j1 > j0 && ka[i1 - 1] === kb[j1 - 1]) { i1--; j1-- }
    const n = i1 - i0, m = j1 - j0
    if (!n && !m) continue
    if (!n || !m) { done.push([i0, i1, j0, j1]); continue }
    // blocks that are alone on their side, and on the other
    const inA = new Map(), inB = new Map()
    for (let i = i0; i < i1; i++) inA.set(ka[i], inA.has(ka[i]) ? -1 : i)
    for (let j = j0; j < j1; j++) inB.set(kb[j], inB.has(kb[j]) ? -1 : j)
    const as = [], bs = []
    for (let i = i0; i < i1; i++) { const j = inB.get(ka[i]); if (inA.get(ka[i]) === i && j !== undefined && j >= 0) { as.push(i); bs.push(j) } }
    if (as.length) {
      const keep = lis(bs)
      let pi = i0, pj = j0
      const gaps = []
      for (const k of keep) { gaps.push([pi, as[k], pj, bs[k]]); pi = as[k] + 1; pj = bs[k] + 1 }
      gaps.push([pi, i1, pj, j1])
      for (let g = gaps.length - 1; g >= 0; g--) stack.push(gaps[g])
      continue
    }
    if (n * m > TABLE) { done.push([i0, i1, j0, j1]); continue }
    // a longest-common-subsequence table for a small stretch with no anchor
    const w = m + 1, t = new Uint32Array((n + 1) * w)
    for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--)
      t[i * w + j] = ka[i0 + i] === kb[j0 + j] ? t[(i + 1) * w + j + 1] + 1 : Math.max(t[(i + 1) * w + j], t[i * w + j + 1])
    let i = 0, j = 0, open = null
    const close = () => { if (open) { done.push([open[0], i0 + i, open[1], j0 + j]); open = null } }
    while (i < n || j < m) {
      if (i < n && j < m && ka[i0 + i] === kb[j0 + j]) { close(); i++; j++ }
      else {
        if (!open) open = [i0 + i, j0 + j]
        if (j >= m || (i < n && t[(i + 1) * w + j] >= t[i * w + j + 1])) i++
        else j++
      }
    }
    close()
  }
  // The stack hands the places over in document order only when each gap is finished before the next: sort to be sure.
  done.sort((x, y) => x[0] - y[0] || x[2] - y[2])
  for (const d of done) out.push(d)
}

class TooMany extends Error {}

// The pieces {from, to, slice}, in order, in the positions of `old`; `pos` is where old's content starts.
function pieces(old, next, pos, out) {
  const a = kids(old), b = kids(next)
  const starts = []
  let at = pos
  for (const c of a) { starts.push(at); at += c.nodeSize }
  starts.push(at)
  const hs = []
  places(a.map(key), b.map(key), 0, a.length, 0, b.length, hs)
  for (const [i0, i1, j0, j1] of hs) {
    if (i1 - i0 === j1 - j0) for (let k = 0; k < i1 - i0; k++) pair(a[i0 + k], b[j0 + k], starts[i0 + k], out)
    else add(out, {from: starts[i0], to: starts[i1], slice: new Slice(Fragment.fromArray(b.slice(j0, j1)), 0, 0)})
  }
}

function add(out, p) {
  if (out.length >= MAX_PIECES) throw new TooMany()
  out.push(p)
}

function pair(o, n, pos, out) {
  const whole = () => add(out, {from: pos, to: pos + o.nodeSize, slice: new Slice(Fragment.from(n), 0, 0)})
  if (o.type !== n.type || !o.sameMarkup(n) || o.isLeaf) return whole()
  if (o.isTextblock) {
    const s = o.content.findDiffStart(n.content)
    if (s == null) return
    const e = o.content.findDiffEnd(n.content)
    let x = e.a, y = e.b
    const over = s - Math.min(x, y)
    if (over > 0) { x += over; y += over }
    add(out, {from: pos + 1 + s, to: pos + 1 + x, slice: new Slice(n.content.cut(s, y), 0, 0)})
    return
  }
  pieces(o, n, pos + 1, out)
}

// One span, from the first top-level block that differs to the last.
function span(old, next) {
  const a = kids(old), b = kids(next)
  let s = 0
  while (s < a.length && s < b.length && key(a[s]) === key(b[s])) s++
  let ea = a.length, eb = b.length
  while (ea > s && eb > s && key(a[ea - 1]) === key(b[eb - 1])) { ea--; eb-- }
  let from = 0
  for (let i = 0; i < s; i++) from += a[i].nodeSize
  let to = from
  for (let i = s; i < ea; i++) to += a[i].nodeSize
  return [{from, to, slice: new Slice(Fragment.fromArray(b.slice(s, eb)), 0, 0)}]
}

export function sourceExitStep(state, next) {
  const tr = state.tr, old = state.doc
  let ok = false
  try {
    let out = []
    try { pieces(old, next, 0, out) } catch (e) { if (!(e instanceof TooMany)) throw e; out = span(old, next) }
    // One step for each piece, last first, so that every piece is in the positions of the document it was found in.
    for (let i = out.length - 1; i >= 0; i--) tr.step(new ReplaceStep(out[i].from, out[i].to, out[i].slice))
    ok = tr.doc.eq(next)
  } catch (e) { /* the whole document, below */ }
  if (ok) return closeHistory(tr)
  return closeHistory(state.tr.replaceWith(0, old.content.size, next.content))
}
