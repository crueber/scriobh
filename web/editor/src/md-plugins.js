// markdown-it configuration for Scriobh: CommonMark + strikethrough, plus
// wiki links, private notes, footnotes, task items, and "source block" capture
// for everything the authoring schema does not model.
//
// Pure JavaScript, no DOM. Runs in Node and in the browser bundle.

import MarkdownIt from "markdown-it"

const SOURCE_KINDS = {html_block: "html", table: "table", reference: "reference"}

// Wrap a block rule so that, when it matches, its tokens are replaced by one
// `source_block` token carrying the exact source lines (container prefixes
// such as "> " and list indentation are already stripped by getLines).
function sourceBlockRule(original, kind) {
  return function (state, startLine, endLine, silent) {
    if (silent) return original(state, startLine, endLine, silent)
    const before = state.tokens.length
    if (!original(state, startLine, endLine, false)) return false
    state.tokens.length = before
    pushSource(state, startLine, state.line, kind)
    return true
  }
}

function pushSource(state, startLine, endLine, kind) {
  let content = state.getLines(startLine, endLine, state.blkIndent, false)
  content = content.replace(/\n+$/, "")
  if (kind === "html" && /^<!--\s*scriobh:alts\b/.test(content)) kind = "alts"
  const token = state.push("source_block", "", 0)
  token.content = content
  token.info = kind
  token.map = [startLine, endLine]
  token.block = true
  return token
}

// `---` ... `---` (or `...`) on the very first lines of the document.
function frontMatterRule(state, startLine, endLine, silent) {
  if (startLine !== 0 || state.blkIndent !== 0 || state.parentType !== "root") return false
  const first = state.getLines(0, 1, 0, false)
  if (!/^---[ \t]*$/.test(first)) return false
  if (endLine < 3) return false
  // The first inner line must look like YAML (key: value, comment, list item),
  // otherwise this is a thematic break that happens to open the document.
  const second = state.getLines(1, 2, 0, false)
  if (!/^(?:[A-Za-z0-9_][\w .-]*:(?:\s|$)|#|- )/.test(second)) return false
  let close = -1
  for (let line = 2; line < endLine; line++) {
    const text = state.getLines(line, line + 1, 0, false)
    if (/^(?:---|\.\.\.)[ \t]*$/.test(text)) { close = line; break }
  }
  if (close < 0) return false
  if (silent) return true
  pushSource(state, 0, close + 1, "front_matter")
  state.line = close + 1
  return true
}

// `:::name` ... `:::`: held verbatim. Only matched when closed.
function directiveRule(state, startLine, endLine, silent) {
  const start = state.bMarks[startLine] + state.tShift[startLine]
  const max = state.eMarks[startLine]
  if (state.sCount[startLine] - state.blkIndent >= 4) return false
  const first = state.src.slice(start, max)
  const open = /^(:{3,})(?:[ \t]*[^\s:].*)?$/.exec(first)
  if (!open) return false
  const need = open[1].length
  const closeRe = new RegExp("^:{" + need + ",}[ \\t]*$")
  let close = -1
  for (let line = startLine + 1; line < endLine; line++) {
    const text = state.src.slice(state.bMarks[line] + state.tShift[line], state.eMarks[line])
    if (state.sCount[line] - state.blkIndent < 0) break
    if (closeRe.test(text)) { close = line; break }
  }
  if (close < 0) return false
  if (silent) return true
  pushSource(state, startLine, close + 1, "directive")
  state.line = close + 1
  return true
}

// `[^label]: text`, with indented continuation blocks.
function footnoteDefRule(state, startLine, endLine, silent) {
  const start = state.bMarks[startLine] + state.tShift[startLine]
  const max = state.eMarks[startLine]
  if (state.sCount[startLine] - state.blkIndent >= 4) return false
  if (start + 4 > max) return false
  if (state.src.charCodeAt(start) !== 0x5B || state.src.charCodeAt(start + 1) !== 0x5E) return false
  let pos
  for (pos = start + 2; pos < max; pos++) {
    const ch = state.src.charCodeAt(pos)
    if (ch === 0x20 || ch === 0x09 || ch === 0x5B) return false
    if (ch === 0x5D) break
  }
  if (pos === start + 2) return false
  if (pos + 1 >= max || state.src.charCodeAt(++pos) !== 0x3A) return false
  if (silent) return true
  pos++

  const label = state.src.slice(start + 2, pos - 2)
  const open = state.push("footnote_def_open", "", 1)
  open.meta = {label}
  open.map = [startLine, 0]

  const oldBMark = state.bMarks[startLine]
  const oldTShift = state.tShift[startLine]
  const oldSCount = state.sCount[startLine]
  const oldParentType = state.parentType
  const posAfterColon = pos
  const initial = state.sCount[startLine] + pos - (state.bMarks[startLine] + state.tShift[startLine])
  let offset = initial
  while (pos < max) {
    const ch = state.src.charCodeAt(pos)
    if (ch === 0x09) offset += 4 - offset % 4
    else if (ch === 0x20) offset++
    else break
    pos++
  }
  state.tShift[startLine] = pos - posAfterColon
  state.sCount[startLine] = offset - initial
  state.bMarks[startLine] = posAfterColon
  state.blkIndent += 4
  state.parentType = "footnote"
  if (state.sCount[startLine] < state.blkIndent) state.sCount[startLine] += state.blkIndent
  state.md.block.tokenize(state, startLine, endLine, true)
  state.parentType = oldParentType
  state.blkIndent -= 4
  state.tShift[startLine] = oldTShift
  state.sCount[startLine] = oldSCount
  state.bMarks[startLine] = oldBMark

  state.push("footnote_def_close", "", -1)
  open.map[1] = state.line
  return true
}

// `[^label]`
function footnoteRefRule(state, silent) {
  const start = state.pos
  const max = state.posMax
  if (state.src.charCodeAt(start) !== 0x5B || state.src.charCodeAt(start + 1) !== 0x5E) return false
  let pos
  for (pos = start + 2; pos < max; pos++) {
    const ch = state.src.charCodeAt(pos)
    if (ch === 0x20 || ch === 0x09 || ch === 0x0A || ch === 0x5B) return false
    if (ch === 0x5D) break
  }
  if (pos >= max || pos === start + 2) return false
  if (!silent) {
    const token = state.push("footnote_ref", "", 0)
    token.meta = {label: state.src.slice(start + 2, pos)}
  }
  state.pos = pos + 1
  return true
}

// `[[Title]]` and `[[Title|alias]]`
function wikiLinkRule(state, silent) {
  const start = state.pos
  const max = state.posMax
  if (state.src.charCodeAt(start) !== 0x5B || state.src.charCodeAt(start + 1) !== 0x5B) return false
  let pos
  for (pos = start + 2; pos < max - 1; pos++) {
    const ch = state.src.charCodeAt(pos)
    if (ch === 0x0A || ch === 0x5B) return false
    if (ch === 0x5D) break
  }
  if (pos >= max - 1 || state.src.charCodeAt(pos) !== 0x5D || state.src.charCodeAt(pos + 1) !== 0x5D) return false
  const inner = state.src.slice(start + 2, pos)
  const bar = inner.indexOf("|")
  const target = (bar < 0 ? inner : inner.slice(0, bar)).trim()
  const alias = bar < 0 ? null : inner.slice(bar + 1).trim() || null
  if (!target) return false
  if (!silent) {
    const token = state.push("wiki_link", "", 0)
    token.meta = {target, alias}
  }
  state.pos = pos + 2
  return true
}

// Inline HTML (`<b>`, `<br>`, ...): held raw.
function inlineHtmlToSource(state) {
  for (const block of state.tokens) {
    if (block.type !== "inline" || !block.children) continue
    for (const child of block.children) {
      if (child.type === "html_inline") child.type = "source_inline"
    }
  }
}

// `%%private note%%`, evaluated after inline parsing so that code spans and
// backslash escapes (which are not "text" tokens yet) are never touched.
// Opening and closing runs of `%` longer than two must be escaped by the
// serializer; here the first two characters of a run delimit.
function privateNotes(state) {
  for (const block of state.tokens) {
    if (block.type !== "inline" || !block.children) continue
    const children = block.children
    // Find every delimiter occurrence.
    const hits = []
    for (let i = 0; i < children.length; i++) {
      const tok = children[i]
      if (tok.type !== "text" || tok.content.indexOf("%%") < 0) continue
      let from = 0
      for (;;) {
        const at = tok.content.indexOf("%%", from)
        if (at < 0) break
        hits.push({tok: i, at})
        from = at + 2
      }
    }
    if (hits.length < 2) continue
    // Pair them in order, refusing an empty note.
    const used = []
    for (let h = 0; h + 1 < hits.length;) {
      const a = hits[h], b = hits[h + 1]
      const empty = a.tok === b.tok && b.at === a.at + 2
      if (empty) { h += 2; continue }
      used.push(a, b)
      h += 2
    }
    if (!used.length) continue
    const byTok = new Map()
    for (let k = 0; k < used.length; k++) {
      const hit = used[k]
      if (!byTok.has(hit.tok)) byTok.set(hit.tok, [])
      byTok.get(hit.tok).push({at: hit.at, open: k % 2 === 0})
    }
    const out = []
    const Token = state.Token
    for (let i = 0; i < children.length; i++) {
      const tok = children[i]
      const list = byTok.get(i)
      if (!list) { out.push(tok); continue }
      let pos = 0
      const push = text => {
        if (!text) return
        const t = new Token("text", "", 0)
        t.content = text
        t.level = tok.level
        out.push(t)
      }
      for (const {at, open} of list) {
        push(tok.content.slice(pos, at))
        const t = new Token(open ? "private_open" : "private_close", "", open ? 1 : -1)
        t.level = tok.level
        out.push(t)
        pos = at + 2
      }
      push(tok.content.slice(pos))
    }
    block.children = out
  }
}

// GFM task items: `- [ ] text` / `- [x] text`.
function taskItems(state) {
  const tokens = state.tokens
  for (let i = 0; i < tokens.length - 2; i++) {
    if (tokens[i].type !== "list_item_open") continue
    const para = tokens[i + 1], inline = tokens[i + 2]
    if (para.type !== "paragraph_open" || inline.type !== "inline" || !inline.children || !inline.children.length) continue
    const first = inline.children[0]
    if (first.type !== "text") continue
    const m = /^\[([ xX])\][ \t]+(?=\S)|^\[([ xX])\][ \t]*$/.exec(first.content)
    if (!m) continue
    // A bare "[ ]" with nothing after it stays literal text.
    if (!/\S/.test(first.content.slice(m[0].length)) && inline.children.length === 1) continue
    tokens[i].meta = {checked: (m[1] || m[2]).toLowerCase() === "x"}
    first.content = first.content.slice(m[0].length)
    if (!first.content) inline.children.shift()
  }
}

export function createTokenizer() {
  const md = new MarkdownIt("commonmark", {html: true, linkify: false, typographer: false, breaks: false})
  md.enable(["strikethrough", "table"])

  // Destinations are kept as written. markdown-it's default percent-encodes
  // and punycodes them, which would rewrite the writer's links.
  md.normalizeLink = url => url
  md.normalizeLinkText = text => text

  // markdown-it's validateLink trims and lowercases but does not strip the control
  // characters browsers ignore inside a scheme ("java&Tab;script:" reaches the
  // href as "java<TAB>script:"). Without normalizeLink's percent-encoding that is
  // ours to catch: test the destination as the browser would read it.
  const baseValidate = md.validateLink
  md.validateLink = url => baseValidate(url) && baseValidate(String(url).replace(/[\u0000-\u0020\u007f-\u009f]/g, ""))

  const block = md.block.ruler
  const orig = name => block.__rules__[block.__find__(name)].fn
  block.at("table", sourceBlockRule(orig("table"), "table"), {alt: ["paragraph", "reference"]})
  block.at("html_block", sourceBlockRule(orig("html_block"), "html"), {alt: ["paragraph", "reference", "blockquote"]})
  block.at("reference", sourceBlockRule(orig("reference"), "reference"))
  block.before("table", "front_matter", frontMatterRule)
  block.before("table", "directive", directiveRule, {alt: ["paragraph", "reference", "blockquote", "list"]})
  block.before("reference", "footnote_def", footnoteDefRule, {alt: ["paragraph", "reference"]})

  md.inline.ruler.before("link", "footnote_ref", footnoteRefRule)
  md.inline.ruler.before("link", "wiki_link", wikiLinkRule)

  md.core.ruler.after("inline", "source_inline", inlineHtmlToSource)
  md.core.ruler.after("source_inline", "private_notes", privateNotes)
  md.core.ruler.after("private_notes", "task_items", taskItems)
  return md
}

export {SOURCE_KINDS}
