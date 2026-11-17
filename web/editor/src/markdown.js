// Markdown <-> ProseMirror document. Pure functions, no DOM, no View.
//
//   parse(markdown)      -> Node          (never throws; see parseDetailed)
//   serialize(doc)       -> string        (deterministic, one canonical style)
//   normalize(markdown)  -> string        serialize(parse(markdown))
//
// Canonical style (docs/EDITOR-SPIKE.md has the reasoning):
//   *emphasis*  **strong**  ~~strike~~  `code`  %%private note%%
//   ATX headings, "-" bullets (alternating "*" next to another bullet list),
//   ordered lists numbered from their start with "." (alternating ")"),
//   "> " quotes, fenced code, "* * *" scene breaks, "\" + newline hard breaks,
//   [[Title|alias]] wiki links, [^label] footnotes defined in place,
//   one blank line between blocks, LF, exactly one final newline.

import {Mark} from "prosemirror-model"
import {MarkdownSerializerState} from "prosemirror-markdown"
import {schema} from "./schema.js"
import {createTokenizer} from "./md-plugins.js"

export {schema}

// ---------------------------------------------------------------------------
// Parsing

const tokenizer = createTokenizer()

class ParseState {
  constructor() {
    this.stack = [{type: schema.topNodeType, attrs: null, content: [], marks: Mark.none, depth: {}}]
  }
  top() { return this.stack[this.stack.length - 1] }
  addText(text) {
    if (!text) return
    const top = this.top(), nodes = top.content, last = nodes[nodes.length - 1]
    const node = schema.text(text, top.marks)
    if (last && last.isText && Mark.sameSet(last.marks, node.marks)) nodes[nodes.length - 1] = last.withText(last.text + text)
    else nodes.push(node)
  }
  // Marks nest by depth: `*a *b* c*` must stay emphasised after the inner close.
  openMark(mark) {
    const top = this.top()
    top.depth[mark.type.name] = (top.depth[mark.type.name] || 0) + 1
    top.marks = mark.addToSet(top.marks)
  }
  closeMark(type) {
    const top = this.top()
    if ((top.depth[type.name] = (top.depth[type.name] || 1) - 1) <= 0) top.marks = type.removeFromSet(top.marks)
  }
  addNode(type, attrs, content) {
    const top = this.top()
    const node = type.createAndFill(attrs, content, top ? top.marks : Mark.none)
    // A silent drop here would lose text. Fail loudly; parse() falls back to
    // holding the whole document as source.
    if (!node) throw new Error("cannot place " + type.name + " here")
    if (top) top.content.push(node)
    return node
  }
  openNode(type, attrs) { this.stack.push({type, attrs, content: [], marks: Mark.none, depth: {}}) }
  closeNode() {
    const info = this.stack.pop()
    return this.addNode(info.type, info.attrs, info.content)
  }
}

const noTrailingNewline = s => (s.endsWith("\n") ? s.slice(0, -1) : s)

function listTight(tokens, i) {
  while (++i < tokens.length) if (tokens[i].type !== "list_item_open") return !!tokens[i].hidden
  return false
}

function plainText(tokens) {
  let out = ""
  for (const t of tokens || []) {
    if (t.type === "text" || t.type === "text_special" || t.type === "code_inline") out += t.content
    else if (t.type === "softbreak" || t.type === "hardbreak") out += " "
    // Alt text is parsed with the same inline rules; keep the source of what is not text.
    else if (t.type === "wiki_link") out += "[[" + t.meta.target + (t.meta.alias ? "|" + t.meta.alias : "") + "]]"
    else if (t.type === "footnote_ref") out += "[^" + t.meta.label + "]"
    else if (t.type === "html_inline") out += t.content
    else if (t.type === "image") out += plainText(t.children)
    else if (t.children) out += plainText(t.children)
  }
  return out
}

const N = n => schema.nodes[n]
const M = n => schema.marks[n]

const handlers = {
  paragraph_open: s => s.openNode(N("paragraph"), null),
  paragraph_close: s => s.closeNode(),
  heading_open: (s, t) => s.openNode(N("heading"), {level: +t.tag.slice(1)}),
  heading_close: s => s.closeNode(),
  blockquote_open: s => s.openNode(N("blockquote"), null),
  blockquote_close: s => s.closeNode(),
  bullet_list_open: (s, t, ts, i) => s.openNode(N("bullet_list"), {tight: listTight(ts, i)}),
  bullet_list_close: s => s.closeNode(),
  ordered_list_open: (s, t, ts, i) => {
    const start = t.attrGet("start")
    s.openNode(N("ordered_list"), {order: start == null ? 1 : parseInt(start, 10), tight: listTight(ts, i)})
  },
  ordered_list_close: s => s.closeNode(),
  list_item_open: (s, t) => s.openNode(N("list_item"), {checked: t.meta && typeof t.meta.checked === "boolean" ? t.meta.checked : null}),
  list_item_close: s => s.closeNode(),
  footnote_def_open: (s, t) => s.openNode(N("footnote_def"), {label: t.meta.label}),
  footnote_def_close: s => s.closeNode(),
  hr: s => s.addNode(N("scene_break"), null),
  code_block: (s, t) => { s.openNode(N("code_block"), {params: ""}); s.addText(noTrailingNewline(t.content)); s.closeNode() },
  fence: (s, t) => { s.openNode(N("code_block"), {params: (t.info || "").trim()}); s.addText(noTrailingNewline(t.content)); s.closeNode() },
  source_block: (s, t) => { s.openNode(N("source_block"), {kind: t.info || "html"}); s.addText(t.content); s.closeNode() },
  image: (s, t) => s.addNode(N("image"), {src: t.attrGet("src"), alt: plainText(t.children), title: t.attrGet("title") || null}),
  hardbreak: s => s.addNode(N("hard_break"), null),
  softbreak: s => s.addText(" "),
  text: (s, t) => s.addText(t.content),
  inline: (s, t) => s.parseTokens(t.children),
  wiki_link: (s, t) => s.addNode(N("wiki_link"), {target: t.meta.target, alias: t.meta.alias}),
  footnote_ref: (s, t) => s.addNode(N("footnote_ref"), {label: t.meta.label}),
  source_inline: (s, t) => s.addNode(N("source_inline"), {raw: t.content}),
  em_open: s => s.openMark(M("em").create()),
  em_close: s => s.closeMark(M("em")),
  strong_open: s => s.openMark(M("strong").create()),
  strong_close: s => s.closeMark(M("strong")),
  s_open: s => s.openMark(M("strike").create()),
  s_close: s => s.closeMark(M("strike")),
  private_open: s => s.openMark(M("private_note").create()),
  private_close: s => s.closeMark(M("private_note")),
  link_open: (s, t) => s.openMark(M("link").create({href: t.attrGet("href"), title: t.attrGet("title") || null})),
  link_close: s => s.closeMark(M("link")),
  code_inline: (s, t) => {
    s.openMark(M("code").create())
    s.addText(t.content)
    s.closeMark(M("code"))
  }
}
ParseState.prototype.parseTokens = function (tokens) {
  for (let i = 0; i < tokens.length; i++) {
    const handler = handlers[tokens[i].type]
    if (!handler) throw new Error("unsupported token " + tokens[i].type)
    handler(this, tokens[i], tokens, i)
  }
}

function emptyDoc() { return schema.topNodeType.createAndFill() }

// Parse, reporting whether the whole-document fallback was used.
export function parseDetailed(markdown) {
  const text = typeof markdown === "string" ? markdown : ""
  if (!text.trim()) return {doc: emptyDoc(), fallback: false, error: null}
  try {
    const state = new ParseState()
    state.parseTokens(tokenizer.parse(text, {}))
    let doc
    do { doc = state.closeNode() } while (state.stack.length)
    return {doc, fallback: false, error: null}
  } catch (error) {
    // Never lose the text: hold the entire input as one source block.
    const body = text.replace(/\r\n?/g, "\n").replace(/\n+$/, "")
    const doc = schema.topNodeType.create(null, [
      schema.nodes.source_block.create({kind: "unparsed"}, body ? schema.text(body) : null)
    ])
    return {doc, fallback: true, error: String(error && error.message || error)}
  }
}

export function parse(markdown) { return parseDetailed(markdown).doc }

// ---------------------------------------------------------------------------
// Serializing

const repeat = (s, n) => s.repeat(Math.max(0, n))

// Destination in the form that parses back to the same string.
function formatDest(url) {
  url = String(url == null ? "" : url).replace(/&(?=#?[A-Za-z0-9]+;)/g, "\\&")
  if (url === "" || /[\s<>]/.test(url) || !balancedParens(url)) {
    return "<" + url.replace(/[\\<>]/g, "\\$&") + ">"
  }
  return url.replace(/[\\()]/g, "\\$&")
}
function balancedParens(url) {
  let depth = 0
  for (let i = 0; i < url.length; i++) {
    const c = url[i]
    if (c === "\\") { i++; continue }
    if (c === "(") depth++
    else if (c === ")" && --depth < 0) return false
  }
  return depth === 0
}
function formatTitle(title) {
  if (!title) return ""
  return ' "' + String(title).replace(/&(?=#?[A-Za-z0-9]+;)/g, "\\&").replace(/[\\"]/g, "\\$&") + '"'
}

function isPlainURL(link, parent, index) {
  const href = link.attrs.href
  if (link.attrs.title || /[\s<>]/.test(href) || /&(?=#?[A-Za-z0-9]+;)/.test(href)) return false
  if (!/^[A-Za-z][A-Za-z0-9+.-]{1,31}:/.test(href)) return false
  const content = parent.child(index)
  if (!content.isText || content.marks[content.marks.length - 1] !== link) return false
  const label = content.text
  if (label !== href && !(/^mailto:/i.test(href) && label === href.slice(7))) return false
  return index === parent.childCount - 1 || !link.isInSet(parent.child(index + 1).marks)
}

function trailingEscaped(out) {
  let n = 0
  for (let i = out.length - 2; i >= 0 && out[i] === "\\"; i--) n++
  return n % 2 === 1
}

class State extends MarkdownSerializerState {
  markers = new WeakMap()

  esc(str, startOfLine = false) {
    const afterBreak = this.out.endsWith("\\\n" + this.delim)
    startOfLine = startOfLine || afterBreak
    str = str.replace(/[`*\\~\[\]_]/g, (m, i) =>
      m === "_" && i > 0 && i + 1 < str.length && /\w/.test(str[i - 1]) && /\w/.test(str[i + 1]) ? m : "\\" + m)
    // Things that would be read as HTML, entities, or private notes.
    str = str.replace(/<(?=[A-Za-z\/!?])/g, "\\<")
    str = str.replace(/&(?=#?[A-Za-z0-9]+;)/g, "\\&")
    str = str.replace(/%%+/g, m => m.replace(/%/g, "\\%"))
    // "[^x](url)" is a link, but "[^x]" alone is a footnote reference.
    if (str[0] === "^" && this.out.endsWith("[") && !trailingEscaped(this.out)) str = "\\" + str
    if (this.escLeadingPercent) {
      if (str[0] === "%") str = "\\" + str
      this.escLeadingPercent = false
    }
    if (startOfLine) {
      str = str
        .replace(/^(\s*)(\+(?=\s|$)|-(?=\s|$)|-+\s*$|=+\s*$|>)/, "$1\\$2")
        .replace(/^(\s*)(#{1,6})(\s|$)/, "$1\\$2$3")
        .replace(/^(\s*\d{1,9})([.)])(\s|$)/, "$1\\$2$3")
        .replace(/^(\s*):{3,}/, m => m.replace(":", "\\:"))
    }
    return str
  }

  // Blank-line separation is one newline inside a tight list.
  flushClose(size = 2) {
    if (size === 2 && this.inTightList) size = 1
    super.flushClose(size)
  }

  renderList(node, delim, firstDelim) {
    this.flushClose(this.inTightList ? 1 : 2)
    const tight = listIsTight(node)
    const prev = this.inTightList
    this.inTightList = tight
    node.forEach((child, _, i) => {
      if (i && tight) super.flushClose(1)
      this.wrapBlock(delim, firstDelim(i), node, () => this.render(child, node, i))
    })
    this.inTightList = prev
  }

  // Containers whose children are separated by blank lines even inside a tight list.
  isolated(fn) {
    const prev = this.inTightList
    this.inTightList = false
    fn()
    this.inTightList = prev
  }

  // Source blocks: the container prefix on every line, none trailing on blank ones.
  literal(text) {
    text.split("\n").forEach((line, i) => {
      if (i === 0) this.write()
      else {
        this.out += "\n"
        if (this.delim) this.out += line === "" ? this.delim.replace(/\s+$/, "") : this.delim
      }
      this.out += line
    })
  }

  // Code body: every line starts a fresh output line.
  codeBody(text) {
    if (text === "") return
    for (const line of text.split("\n")) {
      if (this.delim) this.out += line === "" ? this.delim.replace(/\s+$/, "") : this.delim
      this.out += line + "\n"
    }
  }
}

const emptyItem = item => item.childCount === 1 && item.firstChild.type.name === "paragraph" && item.firstChild.content.size === 0

function listIsTight(list) {
  if (!list.attrs.tight) return false
  let loose = false
  list.forEach(item => item.forEach((child, _, i) => {
    if (i > 0 && /^(?:paragraph|source_block|footnote_def)$/.test(child.type.name)) loose = true
  }))
  return !loose
}

const nodes = {
  paragraph(state, node) {
    if (node.content.size === 0) return // an empty paragraph has no Markdown spelling
    state.renderInline(node)
    state.closeBlock(node)
  },
  heading(state, node) {
    state.write(repeat("#", node.attrs.level) + (node.content.size ? " " : ""))
    const from = state.out.length - 1
    state.renderInline(node, false)
    // A trailing run of "#" would read as an ATX closing sequence.
    if (node.content.size) state.out = state.out.slice(0, from) + state.out.slice(from).replace(/(\s)(#+)$/, "$1\\$2")
    state.closeBlock(node)
  },
  blockquote(state, node) {
    state.isolated(() => state.wrapBlock("> ", null, node, () => state.renderContent(node)))
  },
  scene_break(state, node) {
    state.write("* * *")
    state.closeBlock(node)
  },
  bullet_list(state, node) {
    // Two adjacent bullet lists would re-parse as one, so the second switches marker.
    const prev = state.closed && state.closed.type === node.type ? state.markers.get(state.closed) : null
    const marker = prev === "-" ? "*" : "-"
    state.markers.set(node, marker)
    state.renderList(node, "  ", i => (emptyItem(node.child(i)) ? marker : marker + " "))
  },
  ordered_list(state, node) {
    const start = node.attrs.order
    const prev = state.closed && state.closed.type === node.type ? state.markers.get(state.closed) : null
    const mark = prev === "." ? ")" : "."
    state.markers.set(node, mark)
    const width = String(start + node.childCount - 1).length + 2
    state.renderList(node, repeat(" ", width), i => (start + i) + (emptyItem(node.child(i)) ? mark : mark + " "))
  },
  list_item(state, node) {
    if (node.attrs.checked !== null && node.firstChild.type.name === "paragraph" && node.firstChild.content.size)
      state.write(node.attrs.checked ? "[x] " : "[ ] ")
    state.renderContent(node)
  },
  footnote_def(state, node) {
    state.isolated(() => state.wrapBlock("    ", "[^" + node.attrs.label + "]: ", node, () => state.renderContent(node)))
  },
  code_block(state, node) {
    const text = node.textContent
    const params = node.attrs.params || ""
    let ch = params.includes("`") ? "~" : "`"
    const re = ch === "`" ? /`{3,}/g : /~{3,}/g
    let longest = 2
    for (const m of text.match(re) || []) longest = Math.max(longest, m.length)
    const fence = repeat(ch, longest + 1)
    state.write(fence + params + "\n")
    state.codeBody(text)
    state.write(fence)
    state.closeBlock(node)
  },
  source_block(state, node) {
    state.literal(node.textContent)
    state.closeBlock(node)
  },
  image(state, node) {
    const {src, alt, title} = node.attrs
    state.text("![" + state.esc(alt || "") + "](" + formatDest(src) + formatTitle(title) + ")", false)
  },
  hard_break(state, node, parent, index) {
    for (let i = index + 1; i < parent.childCount; i++) {
      if (parent.child(i).type !== node.type) {
        state.write(parent.type.name === "heading" ? " " : "\\\n")
        return
      }
    }
  },
  text(state, node) { state.text(node.text, !state.inAutolink) },
  wiki_link(state, node) {
    const {target, alias} = node.attrs
    state.text("[[" + target + (alias ? "|" + alias : "") + "]]", false)
  },
  footnote_ref(state, node) { state.text("[^" + node.attrs.label + "]", false) },
  source_inline(state, node) { state.text(node.attrs.raw, false) }
}

const marks = {
  private_note: {
    open(state) {
      // A "%" just before the opener would merge into it.
      if (state.out.endsWith("%") && !trailingEscaped(state.out)) state.out = state.out.slice(0, -1) + "\\%"
      state.escLeadingPercent = true
      return "%%"
    },
    close(state) {
      if (state.out.endsWith("%") && !trailingEscaped(state.out)) state.out = state.out.slice(0, -1) + "\\%"
      state.escLeadingPercent = true
      return "%%"
    }
  },
  link: {
    open(state, mark, parent, index) {
      state.inAutolink = isPlainURL(mark, parent, index)
      return state.inAutolink ? "<" : "["
    },
    close(state, mark) {
      const {inAutolink} = state
      state.inAutolink = undefined
      return inAutolink ? ">" : "](" + formatDest(mark.attrs.href) + formatTitle(mark.attrs.title) + ")"
    },
    mixable: true
  },
  em: {open: "*", close: "*", mixable: true, expelEnclosingWhitespace: true},
  strong: {open: "**", close: "**", mixable: true, expelEnclosingWhitespace: true},
  strike: {open: "~~", close: "~~", mixable: true, expelEnclosingWhitespace: true},
  code: {
    open(_state, _mark, parent, index) { return backticksFor(parent.child(index), -1) },
    close(_state, _mark, parent, index) { return backticksFor(parent.child(index - 1), 1) },
    escape: false
  }
}

// The code-span delimiter on one side of `node`. A span is padded with one space
// when its text contains a backtick or has a space at both ends (a parser strips
// exactly one space from each end in that case), so the text survives.
function backticksFor(node, side) {
  const ticks = /`+/g
  let m, len = 0
  const text = node.isText ? node.text : ""
  while ((m = ticks.exec(text))) len = Math.max(len, m[0].length)
  const pad = len > 0 || (text.startsWith(" ") && text.endsWith(" ") && /[^ ]/.test(text)) ? " " : ""
  const ticksStr = "`".repeat(len + 1)
  return side > 0 ? pad + ticksStr : ticksStr + pad
}

export function serialize(doc) {
  const state = new State(nodes, marks, {strict: true, tightLists: true})
  state.renderContent(doc)
  const out = state.out.replace(/\n+$/, "")
  return out ? out + "\n" : ""
}

export function normalize(markdown) { return serialize(parse(markdown)) }
