// The editor entry point. Loaded lazily on document routes; the page shell does
// not need it. It makes no network requests and touches the DOM only inside
// mount(), so importing it in Node (for the round-trip tests) is safe.
//
//   const ed = scriobhEditor.mount(el, {markdown, onChange, readOnly, onLimit})
//
// onChange(markdown, {version}) is called 700 ms after the last edit, or at once
// on blur / hide / Mod+S, never on the keystroke path. The host confirms a save
// with ed.markSaved(version). Until then the editor is dirty and refuses
// setMarkdown() (unless {force: true}), so nothing arriving from outside can
// replace unsaved local input.

import {EditorState, Selection, TextSelection} from "prosemirror-state"
import {EditorView} from "prosemirror-view"
import {history, closeHistory} from "prosemirror-history"
import {gapCursor} from "prosemirror-gapcursor"
import {schema} from "./schema.js"
import {parseDetailed, serialize, parse} from "./markdown.js"
import {countWords} from "./count.js"
import {buildInputRules, buildKeymaps, commandTable, historyState} from "./rules.js"
import {sourceExitStep} from "./sourcestep.js"
import {pasteProps, insertMarkdown, cleanText} from "./paste.js"
import {makeAlts} from "./alts.js"
import {splitAt} from "./split.js"

export const limits = {warnWords: 15000, readOnlyWords: 25000, debounceMs: 700}

const S_ANCHOR = "", S_HEAD = ""

// ------------------------------------------------- selection <-> Markdown offset

// Offset in the serialized Markdown of the selection ends, found by serializing a
// scratch copy of the document with private-use marker characters at those
// positions. Best effort: a marker can land next to an escape and shift by a
// character or two, never more.
function markdownSelection(state) {
  const {anchor, head} = state.selection
  const points = [[head, S_HEAD]]
  if (anchor !== head) points.push([anchor, S_ANCHOR])
  points.sort((a, b) => b[0] - a[0])
  try {
    const tr = state.tr
    for (const [pos, ch] of points) tr.insertText(ch, pos, pos)
    let md = serialize(tr.doc)
    const find = ch => {
      const i = md.indexOf(ch)
      return i
    }
    let h = find(S_HEAD), a = anchor === head ? h : find(S_ANCHOR)
    md = md.replace(/[]/g, "")
    if (h < 0) return null
    if (anchor !== head) {
      // Removing the earlier marker shifts the later one.
      if (a < h) h -= 1
      else if (h < a) a -= 1
    } else a = h
    return {anchor: a, head: h}
  } catch (e) {
    return null
  }
}

function textOffset(doc, marker) {
  let offset = 0, found = -1
  doc.descendants(node => {
    if (found >= 0) return false
    if (!node.isText) return true
    const i = node.text.indexOf(marker)
    if (i >= 0) {
      found = offset + node.text.slice(0, i).replace(/[]/g, "").length
      return false
    }
    offset += node.text.replace(/[]/g, "").length
    return true
  })
  return found
}

function posFromTextOffset(doc, n) {
  let pos = -1
  doc.descendants((node, p) => {
    if (pos >= 0) return false
    if (!node.isText) return true
    if (n <= node.text.length) { pos = p + n; return false }
    n -= node.text.length
    return true
  })
  return pos
}

function selectionFromMarkdown(text, anchor, head) {
  try {
    const marked = [[head, S_HEAD]]
    if (anchor !== head) marked.push([anchor, S_ANCHOR])
    marked.sort((a, b) => b[0] - a[0])
    let md = text
    for (const [at, ch] of marked) md = md.slice(0, at) + ch + md.slice(at)
    const doc = parse(md)
    const h = textOffset(doc, S_HEAD)
    const a = anchor === head ? h : textOffset(doc, S_ANCHOR)
    return {anchor: a, head: h}
  } catch (e) {
    return null
  }
}

// ----------------------------------------------------------------------- mount

export function mount(el, options = {}) {
  const opts = Object.assign({markdown: "", readOnly: false, typography: false}, options)
  const lim = Object.assign({}, limits, opts.limits)
  const win = el.ownerDocument.defaultView
  const doc = el.ownerDocument

  const root = doc.createElement("div")
  root.className = "scriobh-editor"
  const host = doc.createElement("div")
  host.className = "scriobh-surface"
  const source = doc.createElement("textarea")
  source.className = "scriobh-source"
  source.hidden = true
  source.spellcheck = false
  source.setAttribute("autocapitalize", "off")
  source.setAttribute("autocorrect", "off")
  source.setAttribute("aria-label", opts.sourceLabel || "Markdown source")
  source.setAttribute("wrap", "soft")
  root.append(host, source)
  el.append(root)

  let readOnly = !!opts.readOnly
  let version = 0, savedVersion = 0, flushedVersion = 0, timer = 0, destroyed = false
  let mode = "rich"
  let warned = false
  let quiet = false
  let locked = false // the host holds the editor for an operation: no edit of the text goes through

  const initial = parseDetailed(opts.markdown)
  if (initial.fallback && opts.onWarning) opts.onWarning({kind: "unparsed", error: initial.error})

  // The document as loaded, and the exact bytes it came from. While the document
  // is still that very object, getMarkdown() returns those bytes untouched: an
  // unchanged file is never rewritten into canonical style behind the writer's back.
  let baseline = {doc: initial.doc, markdown: typeof opts.markdown === "string" ? opts.markdown : ""}

  function checkLimits(words) {
    if (words >= lim.readOnlyWords) {
      if (!warned || warned === "warn") opts.onLimit && opts.onLimit({level: "readonly", words, limit: lim.readOnlyWords})
      warned = "readonly"
    } else if (words >= lim.warnWords && !warned) {
      warned = "warn"
      opts.onLimit && opts.onLimit({level: "warn", words, limit: lim.warnWords})
    }
  }

  const openWords = countWords(initial.doc)
  if (openWords >= lim.readOnlyWords) readOnly = true
  checkLimits(openWords)

  function makeState(docNode) {
    return EditorState.create({
      doc: docNode,
      plugins: [
        buildInputRules({typography: opts.typography}),
        ...buildKeymaps({onSource: () => toggleSource(), onFlush: () => flush(), onLink: opts.onLink}),
        history(),
        gapCursor()
      ]
    })
  }

  const paste = pasteProps()
  const view = new EditorView(host, {
    state: makeState(initial.doc),
    editable: () => !readOnly && mode === "rich",
    // A read-only surface must still take keyboard focus (to scroll, select, and reach Mod+E).
    attributes: () => ({
      class: "scriobh-prose",
      role: "textbox",
      "aria-multiline": "true",
      "aria-readonly": !readOnly ? "false" : "true",
      "aria-label": opts.label || "Document",
      spellcheck: "true",
      autocapitalize: "sentences",
      ...(readOnly ? {tabindex: "0"} : {})
    }),
    dispatchTransaction(tr) {
      // While the host holds the editor for an operation, no edit of the text goes through, whatever made it (a key, the
      // toolbar, undo, a paste or a drop): the text the host is about to compare and store must not move under it.
      if (locked && tr.docChanged && !quiet) {
        try { view.docView.markDirty(0, view.state.doc.content.size); view.updateState(view.state) } catch (e) { /* the surface is as it was */ }
        return
      }
      const next = view.state.apply(tr)
      view.updateState(next)
      if (tr.docChanged && !quiet) changed()
    },
    handleDOMEvents: {
      blur() { if (mode === "rich") flush(); return false },
      // A native Undo or Redo that did not come through the keymap (the operating system's Edit menu, a context menu, a shake or
      // a swipe on a tablet) asks the page for it as a beforeinput of this type. The surface's own history answers it, never the
      // browser's, whose undo stack knows nothing of what the editor did.
      beforeinput(v, e) {
        const name = e.inputType === "historyUndo" ? "undo" : e.inputType === "historyRedo" ? "redo" : ""
        if (!name) return false
        e.preventDefault()
        commandTable[name](v.state, v.dispatch, v)
        return true
      }
    },
    ...paste
  })

  // ------------------------------------------------------------------ changes

  // A transaction that changes nothing (not the text, the selection or the pending marks), for a host that follows the editor
  // through its dispatch: a stored mark (a bold waiting to be typed) is carried over, which a selection step would clear.
  let poking = false
  function poke() { view.dispatch(view.state.tr.setStoredMarks(view.state.storedMarks)) }

  function changed() {
    version++
    win.clearTimeout(timer)
    timer = win.setTimeout(flush, lim.debounceMs)
  }

  // `canonical`: the serialization of the document, even when it has not been touched since it was loaded (then the loaded text,
  // which may be spelled differently, is what is normally given back).
  function currentMarkdown(canonical) {
    if (mode === "source") return cleanText(source.value)
    const state = view.state
    return cleanText(state.doc === baseline.doc && !canonical ? baseline.markdown : serialize(state.doc))
  }

  // `force`: hand the text over even if nothing changed, as the editor spells it (the host asks this before an operation that
  // names places by offset, so the stored text and the editor's agree on where they are).
  function flush(force) {
    win.clearTimeout(timer)
    timer = 0
    if (destroyed || (version === flushedVersion && force !== true)) return
    flushedVersion = version
    const markdown = currentMarkdown(force === true)
    // Handed over in the editor's own spelling: that is now the text of the document as far as the host is concerned.
    if (force === true && mode === "rich" && view.state.doc === baseline.doc) baseline = {doc: baseline.doc, markdown}
    if (mode === "rich") checkLimits(countWords(view.state.doc))
    if (opts.onChange) opts.onChange(markdown, {version})
  }
  source.addEventListener("input", changed)
  source.addEventListener("blur", () => { if (mode === "source") flush() })
  source.addEventListener("keydown", event => {
    if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey) {
      const k = event.key.toLowerCase()
      if (k === "e") { event.preventDefault(); toggleSource() }
      else if (k === "s") { event.preventDefault(); flush() }
    }
  })
  const onHide = () => { if (doc.visibilityState === "hidden") flush() }
  doc.addEventListener("visibilitychange", onHide)
  win.addEventListener("pagehide", flush)

  // -------------------------------------------------------------- source mode

  // What the textarea was loaded with. If it comes back unchanged the document
  // is not touched at all (no parse, no history step, no onChange).
  let sourceLoaded = ""

  function toggleSource(force) {
    const want = force === undefined ? (mode === "rich" ? "source" : "rich") : force ? "source" : "rich"
    if (want === mode || destroyed || locked) return mode // held by the host: the text does not change under it
    if (want === "source") {
      const sel = markdownSelection(view.state)
      const text = currentMarkdown()
      source.value = sourceLoaded = text
      source.readOnly = readOnly
      mode = "source"
      host.hidden = true
      source.hidden = false
      view.setProps({}) // re-read `editable`
      source.focus({preventScroll: true})
      if (sel) {
        source.setSelectionRange(Math.min(sel.anchor, sel.head), Math.max(sel.anchor, sel.head), sel.anchor > sel.head ? "backward" : "forward")
        const ratio = text.length ? sel.head / text.length : 0
        source.scrollTop = Math.max(0, ratio * source.scrollHeight - source.clientHeight / 2)
      }
    } else {
      const text = cleanText(source.value)
      const back = source.selectionDirection === "backward"
      const sel = selectionFromMarkdown(text, back ? source.selectionEnd : source.selectionStart, back ? source.selectionStart : source.selectionEnd)
      mode = "rich"
      source.hidden = true
      host.hidden = false
      if (text !== sourceLoaded) {
        const parsed = parseDetailed(text)
        if (parsed.fallback && opts.onWarning) opts.onWarning({kind: "unparsed", error: parsed.error})
        // One undoable step, never merged into the typing before it (an Undo takes back the Source edits and nothing else). The
        // edit was already counted by the textarea's input events, so it does not bump the version again.
        quiet = true
        view.dispatch(sourceExitStep(view.state, parsed.doc))
        quiet = false
        baseline = {doc: view.state.doc, markdown: text}
      }
      view.setProps({})
      const d = view.state.doc
      let tr = view.state.tr
      if (sel && sel.anchor >= 0 && sel.head >= 0) {
        const pa = posFromTextOffset(d, sel.anchor), ph = posFromTextOffset(d, sel.head)
        if (pa >= 0 && ph >= 0) tr = tr.setSelection(TextSelection.create(d, pa, ph))
      }
      quiet = true
      view.dispatch(closeHistory(tr.scrollIntoView())) // typing right after Source is left is a step of its own
      quiet = false
      focusSurface()
    }
    return mode
  }

  // --------------------------------------------------------------------- API

  function focusSurface() {
    if (mode === "source") source.focus()
    else if (readOnly) view.dom.focus()
    else view.focus()
  }

  const api = {
    view,
    getMarkdown: () => currentMarkdown(),

    // Replace the document from outside (a reload, a server revision). Refuses,
    // returning false, while there is unsaved local input unless {force: true}.
    setMarkdown(markdown, o = {}) {
      if (destroyed) return false
      // Text mid-composition is not in the document yet, so dirtiness alone cannot see it.
      if ((api.isDirty() || view.composing) && !o.force) return false
      win.clearTimeout(timer)
      const parsed = parseDetailed(markdown)
      if (parsed.fallback && opts.onWarning) opts.onWarning({kind: "unparsed", error: parsed.error})
      baseline = {doc: parsed.doc, markdown: String(markdown == null ? "" : markdown)}
      view.updateState(makeState(parsed.doc))
      if (mode === "source") source.value = baseline.markdown
      flushedVersion = savedVersion = version
      const words = countWords(parsed.doc)
      warned = false
      readOnly = !!opts.readOnly || words >= lim.readOnlyWords
      checkLimits(words)
      view.setProps({})
      // The history began again: a transaction that changes nothing lets the host's own dispatch hook (the format bar) see it.
      poke()
      return true
    },

    focus: focusSurface,
    isDirty: () => version !== savedVersion,
    // The host has stored `version` (from onChange). Clean only if nothing changed since.
    markSaved(v) { savedVersion = Math.min(version, Math.max(savedVersion, v === undefined ? flushedVersion : v)) },
    flush,
    toggleSource,
    mode: () => mode,
    setReadOnly(value) { readOnly = !!value; source.readOnly = readOnly; view.setProps({}) },
    pasteMarkdown(text) { insertMarkdown(view, text) },
    // What Undo and Redo can do now, for the format bar's buttons. Only the rich surface's own history is counted: the Source
    // textarea keeps its own, which the page cannot read, so in Source (and when the text is read-only or held) both are false.
    history: () => (mode === "rich" && !readOnly && !locked ? historyState(view.state) : {undo: false, redo: false}),
    exec(name) {
      const cmd = commandTable[name]
      if (!cmd || mode !== "rich" || readOnly) return false
      const ok = cmd(view.state, view.dispatch, view)
      view.focus()
      return ok
    },
    // Alternatives, ghosting, comments and the Lab are drawn as decorations:
    // `fn(state)` returns a DecorationSet. The schema is untouched.
    setDecorations(fn) { view.setProps({decorations: fn || undefined}) },
    wordCount: () => countWords(view.state.doc),
    // Where a chapter is cut for the selection (the current one, or the one given): see split.js. `stored` is the saved text, for a
    // chapter that cannot be written again (too long to edit).
    splitAt: (sel, stored) => splitAt(view.state, sel, stored),
    // The map between Markdown source offsets and positions, and the quiet edits alternatives need (alts.js).
    alts: makeAlts(view, {
      silently(fn) { const was = quiet; quiet = true; try { fn() } finally { quiet = was } },
      // The host has stored the text as of `v` (the version it sent; the current one when it names none). Anything typed
      // since stays unsaved and is handed over again.
      settle(v) {
        win.clearTimeout(timer); timer = 0
        const upto = v === undefined ? version : Math.min(version, v)
        flushedVersion = savedVersion = upto
        if (upto !== version) timer = win.setTimeout(flush, lim.debounceMs)
      },
      version: () => version,
      lock(on) {
        if (destroyed) return
        const was = locked
        locked = !!on; source.readOnly = locked || readOnly
        // The host's hook (the format bar) sees the hold begin and end. A release and a hold in the same breath (the stash's own
        // edit) are one poke, after both.
        if (was !== locked && !poking) { poking = true; Promise.resolve().then(() => { poking = false; if (!destroyed) poke() }) }
      }
    }),
    destroy() {
      if (destroyed) return
      destroyed = true
      win.clearTimeout(timer)
      doc.removeEventListener("visibilitychange", onHide)
      win.removeEventListener("pagehide", flush)
      view.destroy()
      root.remove()
    }
  }

  return api
}
