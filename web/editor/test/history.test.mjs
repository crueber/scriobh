// What the Undo and Redo buttons of the format bar follow: the history's depths, as two booleans (historyState), through typing,
// undo, redo, a step kept out of the history (an alternative's, the stored line's) and a state made fresh (a reload).
import test from "node:test"
import assert from "node:assert/strict"
import {EditorState, TextSelection} from "prosemirror-state"
import {history, closeHistory} from "prosemirror-history"
import {parse, serialize} from "../src/markdown.js"
import {commandTable, historyState} from "../src/rules.js"
import {sourceExitStep} from "../src/sourcestep.js"

const fresh = md => EditorState.create({doc: parse(md), plugins: [history()]})
const type = (state, text, extra) => {
  const tr = state.tr.insertText(text, state.doc.content.size - 1)
  if (extra) extra(tr)
  return state.apply(tr)
}
// A command is run against a state and its result is the new state (the view is not needed by undo and redo).
const run = (state, name) => {
  let next = state
  assert.ok(commandTable[name](state, tr => { next = state.apply(tr) }), `${name} had nothing to do`)
  return next
}

test("a new document has nothing to undo or redo", () => {
  assert.deepEqual(historyState(fresh("One line.\n")), {undo: false, redo: false})
})

test("typing enables Undo; Undo enables Redo and may disable Undo; Redo goes back", () => {
  let s = type(fresh("One line.\n"), " More.")
  assert.deepEqual(historyState(s), {undo: true, redo: false})
  s = run(s, "undo")
  assert.equal(serialize(s.doc).trim(), "One line.", "undo returns the exact text")
  assert.deepEqual(historyState(s), {undo: false, redo: true})
  s = run(s, "redo")
  assert.equal(serialize(s.doc).trim(), "One line. More.")
  assert.deepEqual(historyState(s), {undo: true, redo: false})
})

test("a new edit after an undo drops what could be redone", () => {
  let s = run(type(fresh("A.\n"), " B."), "undo")
  assert.equal(historyState(s).redo, true)
  s = type(s, " C.")
  assert.deepEqual(historyState(s), {undo: true, redo: false})
})

test("a bold from the command table is one undoable step", () => {
  let s = fresh("Hello world.\n")
  s = s.apply(s.tr.setSelection(TextSelection.create(s.doc, 1, 6)))
  assert.deepEqual(historyState(s), {undo: false, redo: false}, "a selection alone is not a step")
  s = run(s, "bold")
  assert.match(serialize(s.doc), /\*\*Hello\*\*/)
  assert.equal(historyState(s).undo, true)
  s = run(s, "undo")
  assert.equal(serialize(s.doc).trim(), "Hello world.")
})

test("a step made with addToHistory:false is never counted", () => {
  const quiet = tr => tr.setMeta("addToHistory", false)
  let s = type(fresh("A.\n"), " B.", quiet)
  assert.deepEqual(historyState(s), {undo: false, redo: false}, "the first quiet step leaves the history empty")
  s = type(s, " C.")
  s = type(s, " D.", quiet)
  assert.equal(historyState(s).undo, true)
  s = run(s, "undo") // takes back the typed step, not the quiet ones
  assert.deepEqual(historyState(s), {undo: false, redo: true})
  assert.match(serialize(s.doc), /B\./, "the quiet step stays")
})

test("a state made fresh (a reload, a new text from outside) starts with an empty history", () => {
  let s = type(fresh("A.\n"), " B.")
  assert.equal(historyState(s).undo, true)
  s = fresh("New text.\n")
  assert.deepEqual(historyState(s), {undo: false, redo: false})
})

// ---- leaving the Source view (editor.js toggleSource calls sourceExitStep with the text parsed from the textarea)
const quietReplace = (state, needle, wording) => {
  let at = -1
  state.doc.descendants((n, pos) => { if (at < 0 && n.isText && n.text.includes(needle)) at = pos + n.text.indexOf(needle) })
  assert.ok(at >= 0, needle)
  return state.apply(closeHistory(state.tr.insertText(wording, at, at + needle.length).setMeta("addToHistory", false)))
}

test("an alternative's wording written after Source is left survives Undo of the Source edit (the whole-document step wiped it)", () => {
  const start = fresh("One line.\n\nTwo words here.\n")
  const next = parse("One Source line.\n\nTwo words here.\n\nAdded in Source.\n")
  let s = start.apply(sourceExitStep(start, next))
  assert.equal(serialize(s.doc), serialize(next))
  s = quietReplace(s, "words", "ALTWORD")
  s = run(s, "undo")
  assert.equal(serialize(s.doc), "One line.\n\nTwo ALTWORD here.\n", "Undo took back what Source did, and only that")
  assert.deepEqual(historyState(s), {undo: false, redo: true})
  // The control: a step over the whole document is rebased over the quiet one and puts the old words back.
  let w = start.apply(closeHistory(start.tr.replaceWith(0, start.doc.content.size, next.content)))
  w = quietReplace(w, "words", "ALTWORD")
  w = run(w, "undo")
  assert.equal(serialize(w.doc), "One line.\n\nTwo words here.\n", "(what the whole-document step did)")
})

test("the stored alternatives line (a source block at the end) survives it too", () => {
  const start = fresh("One.\n\nTwo.\n\n<!-- alts: stored -->\n")
  const next = parse("One edited.\n\nTwo.\n\n<!-- alts: stored -->\n")
  let s = start.apply(sourceExitStep(start, next))
  const last = s.doc.lastChild
  s = s.apply(closeHistory(s.tr.insertText("<!-- alts: later -->", s.doc.content.size - last.nodeSize + 1, s.doc.content.size - 1).setMeta("addToHistory", false)))
  const before = serialize(s.doc)
  s = run(s, "undo")
  assert.ok(serialize(s.doc).includes("alts: later"), "the stored line is kept: " + serialize(s.doc))
  assert.ok(serialize(s.doc).startsWith("One."), "the Source edit is gone")
  assert.notEqual(serialize(s.doc), before)
})

test("the Source step is its own history event, before and after, even inside the 500 ms grouping window", () => {
  let s = type(fresh("A.\n"), " B.")
  const t = s.tr.time
  // Source edit right next to the typing, in the same instant.
  const next = parse("A. B. Src.\n")
  s = s.apply(sourceExitStep(s, next))
  s = s.apply(closeHistory(s.tr.setSelection(TextSelection.atEnd(s.doc)))) // the selection step toggleSource makes after it
  s = type(s, "!")
  assert.equal(serialize(s.doc), "A. B. Src.!\n")
  s = run(s, "undo")
  assert.equal(serialize(s.doc), "A. B. Src.\n", "typing after Source is a step of its own")
  s = run(s, "undo")
  assert.equal(serialize(s.doc), "A. B.\n", "the Source step is alone (not joined to the typing before it)")
  s = run(s, "undo")
  assert.equal(serialize(s.doc), "A.\n")
  assert.ok(t > 0)
})

test("a Source text that changes only whitespace or nothing makes no step", () => {
  const start = fresh("Same.\n")
  const s = start.apply(sourceExitStep(start, parse("Same.\n")))
  assert.equal(s.doc.eq(start.doc), true)
  assert.deepEqual(historyState(s), {undo: false, redo: false})
})

test("the pieces make the new document for edits of every kind, in lists and quotes too, and leave the rest alone", () => {
  const docs = [
    "# Title\n\nOne.\n\nTwo.\n\nThree.\n",
    "# Title\n\n- a\n- b\n- c\n\n> quoted words\n\nEnd.\n",
    "# Heading\n\n1. first\n2. second\n\n***\n\nTail text.\n",
    "Only.\n",
    "\n",
    "- [ ] task\n- [x] done\n\n> a\n>\n> b\n\n```\ncode\n```\n\nafter\n",
    "Mixed **bold** and *italic* and `code`.\n\nSecond line\\\nbroken.\n"
  ]
  let n = 0
  for (const a of docs) for (const b of docs) {
    const start = fresh(a), next = parse(b)
    const s = start.apply(sourceExitStep(start, next))
    assert.equal(serialize(s.doc), serialize(next), `${JSON.stringify(a)} -> ${JSON.stringify(b)}`)
    n++
  }
  assert.equal(n, docs.length * docs.length)
  // A middle list item edited in Source; a wording written quietly in the first item survives the Undo.
  const start = fresh("- alpha words\n- beta\n- gamma\n")
  let s = start.apply(sourceExitStep(start, parse("- alpha words\n- beta changed\n- gamma\n")))
  s = quietReplace(s, "words", "ALT")
  s = run(s, "undo")
  assert.equal(serialize(s.doc), "- alpha ALT\n- beta\n- gamma\n")
  // And an edit at the top and one at the bottom leave the middle to the quiet step.
  const two = fresh("Top.\n\nMiddle words here.\n\nBottom.\n")
  s = two.apply(sourceExitStep(two, parse("Top changed.\n\nMiddle words here.\n\nBottom changed.\n")))
  s = quietReplace(s, "words", "ALT")
  s = run(s, "undo")
  assert.equal(serialize(s.doc), "Top.\n\nMiddle ALT here.\n\nBottom.\n")
})

// ---- blocks that differ only in an attribute are different blocks (the key is the node's whole content)
test("a change of a heading level, a link's address, a task's state, a code block's language or a list's first number is found as a change", () => {
  const cases = [
    ["heading level", "# One\n\nA.\n\nB words.\n\nC.\n", "## One\n\nA.\n\nB words.\n\nC changed.\n"],
    ["link address", "A.\n\n[x](http://a)\n\nB words.\n\nC.\n", "A.\n\n[x](http://b)\n\nB words.\n\nC changed.\n"],
    ["task state", "- [ ] t\n\nA.\n\nB words.\n\nC.\n", "- [x] t\n\nA.\n\nB words.\n\nC changed.\n"],
    ["code language", "A.\n\n```js\nx\n```\n\nB words.\n\nC.\n", "A.\n\n```go\nx\n```\n\nB words.\n\nC changed.\n"],
    ["list start", "1. a\n\nA.\n\nB words.\n\nC.\n", "5. a\n\nA.\n\nB words.\n\nC changed.\n"]
  ]
  for (const [what, a, b] of cases) {
    const start = fresh(a), next = parse(b)
    const tr = sourceExitStep(start, next)
    assert.ok(tr.steps.length >= 2, `${what}: the attribute and the text are two places (${tr.steps.length})`)
    assert.ok(!tr.steps.some(st => st.from === 0 && st.to === start.doc.content.size), `${what}: not the whole document`)
    let s = start.apply(tr)
    assert.equal(serialize(s.doc), serialize(next), what)
    s = quietReplace(s, "words", "ALT")
    s = run(s, "undo")
    assert.equal(serialize(s.doc), serialize(start.doc).replace("B words.", "B ALT."), `${what}: the quiet wording survives the Undo`)
  }
})

// ---- many blocks: matched by what they hold, not by place
const para = i => "Paragraph number " + i + " words"
test("a shift of hundreds of blocks (one removed first, one added last) is two places, and a quiet wording in the middle survives Undo", () => {
  for (const N of [400, 600, 1000, 3000]) {
    const L = Array.from({length: N}, (_, i) => para(i))
    const start = fresh(L.join("\n\n") + "\n"), next = parse([...L.slice(1), "A new last paragraph"].join("\n\n") + "\n")
    const tr = sourceExitStep(start, next)
    assert.equal(tr.steps.length, 2, `${N} blocks`)
    let s = start.apply(tr)
    assert.ok(s.doc.eq(next))
    s = quietReplace(s, "number " + (N >> 1) + " words", "number " + (N >> 1) + " ALT")
    s = run(s, "undo")
    assert.ok(serialize(s.doc).includes("number " + (N >> 1) + " ALT"), `${N}: the wording survives`)
    assert.ok(serialize(s.doc).includes("Paragraph number 0 words"), `${N}: the first block is back`)
  }
})

test("12,000 changed short blocks: a bounded number of steps, fast, and little memory", () => {
  const L = Array.from({length: 12000}, (_, i) => "w" + i)
  const start = fresh(L.join("\n\n") + "\n"), next = parse(L.map(x => x + "x").join("\n\n") + "\n")
  globalThis.gc && globalThis.gc()
  const heap = process.memoryUsage().heapUsed, t0 = performance.now()
  const tr = sourceExitStep(start, next)
  const ms = performance.now() - t0, grown = (process.memoryUsage().heapUsed - heap) / 1e6
  assert.ok(tr.steps.length <= 200, `${tr.steps.length} steps`)
  assert.ok(ms < 200, `${ms.toFixed(0)} ms`)
  assert.ok(grown < 200, `${grown.toFixed(0)} MB of heap`)
  const s = start.apply(tr)
  assert.ok(s.doc.eq(next))
  assert.ok(run(s, "undo").doc.eq(start.doc))
})

test("25,000 one-line paragraphs, every one changed, is one step and nothing runs out of memory", () => {
  const L = Array.from({length: 25000}, (_, i) => "w" + i)
  const start = fresh(L.join("\n\n") + "\n"), next = parse(L.map(x => x + "x").join("\n\n") + "\n")
  const tr = sourceExitStep(start, next)
  assert.ok(tr.steps.length <= 200, `${tr.steps.length} steps`)
  const s = start.apply(tr)
  assert.ok(s.doc.eq(next))
  assert.ok(run(s, "undo").doc.eq(start.doc))
})

test("more than 200 places make the one span from the first difference to the last, not the whole document", () => {
  const L = Array.from({length: 1000}, (_, i) => "Line " + i)
  const start = fresh(L.join("\n\n") + "\n"), next = parse(L.map((x, i) => i >= 100 && i < 500 ? x + "!" : x).join("\n\n") + "\n")
  const tr = sourceExitStep(start, next)
  assert.equal(tr.steps.length, 1)
  assert.ok(tr.steps[0].from > 0 && tr.steps[0].to < start.doc.content.size, "the blocks before and after are outside it")
  assert.ok(start.apply(tr).doc.eq(next))
})
