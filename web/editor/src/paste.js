// Paste and copy.
//
// HTML paste is mapped onto the schema by the parseDOM rules in schema.js:
// unknown elements are unwrapped, <script>/<style>/<head> are ignored, styles
// become marks only for bold/italic/strike, unsafe link and image destinations
// are refused. No markup is ever stored.
//
// Plain-text paste is literal: it is never interpreted as Markdown. Typing
// Mod+Alt+Shift+V (or calling pasteMarkdown) pastes the clipboard text as
// Markdown instead.

import {Slice, Fragment} from "prosemirror-model"
import {schema} from "./schema.js"
import {parse, serialize} from "./markdown.js"

const {nodes, marks} = schema

// A surrogate half with no partner is not text: the server stores it as U+FFFD, the saved copy then differs from what was
// sent, and the page would send it again for ever. It is replaced the moment it comes in, and again on the way out.
const LONE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g
export function cleanText(s) { return typeof s === "string" ? s.replace(LONE, "\uFFFD") : s }

const BLANK = /^[\s ​]*$/

// Word and Google Docs paste empty paragraphs full of NBSP. They carry no text
// and have no Markdown spelling.
function dropBlankParagraphs(fragment) {
  const kept = []
  fragment.forEach(node => {
    if (node.type === nodes.paragraph && BLANK.test(node.textContent) && !hasNonText(node)) return
    kept.push(node.content.size && !node.isTextblock ? node.copy(dropBlankParagraphs(node.content)) : node)
  })
  // Never turn a paste into nothing: keep the original if everything was blank.
  return kept.length ? Fragment.from(kept) : fragment
}
function hasNonText(node) {
  let found = false
  node.descendants(child => { if (!child.isText && child.type !== nodes.hard_break) found = true })
  return found
}

// ------------------------------------------------------------ HTML sanitising
//
// Pasted HTML is rewritten as a string before any DOM sees it. This matters under
// the application's CSP (style-src 'self'): the browser refuses style attributes,
// even when parsing into an inert document, reports a violation for each, and
// leaves `element.style` empty, so the formatting that Word and Google Docs carry
// only in styles would be lost. Here the style attributes are removed and the
// three formatting facts the schema can hold are kept as data-sx tokens that the
// parseDOM rules in schema.js read: b bold, i italic, s strike, nb/ni "normal".
// Event-handler attributes are dropped, table cells become blocks.

const TAG = /<(\/?)([a-zA-Z][a-zA-Z0-9:-]*)((?:\s+[^\s"'<>\/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>`]+))?)*)\s*(\/?)>/g
const ATTR = /([^\s"'<>\/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?/g
const BLOCKISH = {td: 1, th: 1, tr: 1, caption: 1}

function styleTokens(style) {
  const tokens = []
  const decl = name => {
    const m = new RegExp("(?:^|;)\\s*" + name + "\\s*:\\s*([^;]+)", "i").exec(style)
    return m ? m[1].trim().toLowerCase() : ""
  }
  const weight = decl("font-weight")
  if (/^(?:bold(?:er)?|[6-9]\d\d)$/.test(weight)) tokens.push("b")
  else if (/^(?:normal|lighter|[1-5]\d\d)$/.test(weight)) tokens.push("nb")
  const fontStyle = decl("font-style")
  if (/^(?:italic|oblique)/.test(fontStyle)) tokens.push("i")
  else if (fontStyle === "normal") tokens.push("ni")
  if (/line-through/.test(decl("text-decoration") + " " + decl("text-decoration-line"))) tokens.push("s")
  return tokens.join(" ")
}

export function sanitizePastedHTML(html) {
  html = cleanText(html)
  // ProseMirror marks its own clipboard HTML with data-pm-slice. Only that may
  // carry data-src (an image kept in the document but not drawn); in foreign HTML
  // it is a lazy-loading attribute and is dropped like any other. The same goes for the classes that mark held source
  // (source-inline, source-block): foreign HTML must not be able to make a ghost tag or an alternatives comment.
  const internal = /\bdata-pm-slice=/.test(html)
  html = html
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<(script|style|head|title|xml|noscript|template|iframe|object|embed)\b[\s\S]*?<\/\1\s*>/gi, "")
  return html.replace(TAG, (whole, close, name, attrs, selfClose) => {
    const lower = name.toLowerCase()
    const outName = BLOCKISH[lower] ? "div" : name
    if (close) return "</" + outName + ">"
    let kept = "", style = ""
    for (const m of attrs.matchAll(ATTR)) {
      const attr = m[1].toLowerCase()
      if (attr === "style") style = m[2] ?? m[3] ?? m[4] ?? ""
      else if (attr === "data-sx" || attr.startsWith("on") || (attr === "data-src" && !internal)) continue
      else if (!internal && (attr === "data-kind" || (attr === "class" && /\bsource-(?:inline|block)\b/.test(m[2] ?? m[3] ?? m[4] ?? "")))) continue
      else kept += " " + m[0]
    }
    const tokens = style ? styleTokens(style) : ""
    return "<" + outName + kept + (tokens ? ` data-sx="${tokens}"` : "") + (selfClose ? "/" : "") + ">"
  })
}

export function insertMarkdown(view, text) {
  const doc = parse(cleanText(text))
  const single = doc.childCount === 1 && doc.firstChild.isTextblock
  const slice = new Slice(doc.content, single ? 1 : 0, single ? 1 : 0)
  view.dispatch(view.state.tr.replaceSelection(slice).scrollIntoView().setMeta("paste", true).setMeta("uiEvent", "paste"))
}

export function pasteProps() {
  let markdownUntil = 0
  return {
    transformPastedHTML: sanitizePastedHTML,
    transformPastedText: cleanText,
    transformPasted(slice) {
      return new Slice(dropBlankParagraphs(slice.content), slice.openStart, slice.openEnd)
    },
    handleKeyDown(view, event) {
      if (event.key && event.key.toLowerCase() === "v" && (event.metaKey || event.ctrlKey) && event.altKey && event.shiftKey) {
        markdownUntil = Date.now() + 2000
      }
      return false
    },
    handlePaste(view, event) {
      const data = event.clipboardData
      const text = data ? data.getData("text/plain") : ""
      if (markdownUntil > Date.now()) {
        markdownUntil = 0
        if (text) { insertMarkdown(view, text); return true }
      }
      // A bare URL pasted over selected text becomes a link on that text.
      const {from, to, empty} = view.state.selection
      if (!empty && /^https?:\/\/\S+$/i.test(text.trim()) && !view.state.doc.rangeHasMark(from, to, marks.link)) {
        view.dispatch(view.state.tr.addMark(from, to, marks.link.create({href: text.trim()})))
        return true
      }
      return false
    },
    // What other applications, and the Source textarea, receive on copy.
    clipboardTextSerializer(slice) {
      try {
        const doc = schema.topNodeType.create(null, slice.content)
        return serialize(doc).replace(/\n$/, "")
      } catch (e) {
        return slice.content.textBetween(0, slice.content.size, "\n\n")
      }
    }
  }
}
