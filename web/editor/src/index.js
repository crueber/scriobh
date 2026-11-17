// Bundle entry. Exposes one global, `scriobhEditor`, and nothing else.
//
//   scriobhEditor.mount(el, {markdown, onChange, readOnly, onLimit, ...})
//   scriobhEditor.markdown.{parse, parseDetailed, serialize, normalize, schema, countWords}
//
// Loading this file touches no DOM, so it also loads in bare Node (the Go tests
// use that to check the committed bundle against the corpus).

import {mount, limits} from "./editor.js"
import {parse, parseDetailed, serialize, normalize, schema} from "./markdown.js"
import {countWords} from "./count.js"
import {Decoration, DecorationSet} from "prosemirror-view"

globalThis.scriobhEditor = {
  version: "0.1.0",
  mount,
  limits,
  // For decorations: alternatives, ghosting, comments and the Lab will build
  // DecorationSets and hand them to editor.setDecorations(fn).
  Decoration,
  DecorationSet,
  markdown: {parse, parseDetailed, serialize, normalize, schema, countWords}
}
