// The Scriobh document schema.
//
// Everything here must have a Markdown spelling (see markdown.js). Anything the
// authoring schema does not model is held verbatim in `source_block` (block) or
// `source_inline` (inline) and written back byte for byte. Raw HTML is never an
// authoring construct: pasted HTML is mapped onto these nodes by the parseDOM
// rules below and the markup itself is discarded.
//
// This module is pure data. It does not touch the DOM at load time, so Node can
// import it without jsdom.

import {Schema} from "prosemirror-model"

// Link and image destinations that may be stored. Everything else (javascript:,
// vbscript:, data:, file:, ...) is refused; the element's text is kept, the
// link is not. markdown-it applies its own validateLink on the Markdown path.
const SAFE_HREF = /^(?:https?:|mailto:|tel:|#|\/|\.{1,2}\/|[^:/?#]*(?:[/?#]|$))/i
export function safeHref(href) {
  if (typeof href !== "string") return null
  const h = href.trim()
  if (!h) return "" // an empty destination is a valid link
  // Strip control characters and whitespace that browsers ignore inside a scheme.
  const probe = h.replace(/[\u0000- \u007f-\u009f]/g, "")
  return SAFE_HREF.test(probe) ? h : null
}
// Pasted images may not point off the instance: no scheme, no protocol-relative host.
const SAFE_IMG = /^(?:\/(?!\/)|\.{1,2}\/|[^:/?#]+(?:[/?#]|$))/
export function safeSrc(src) {
  if (typeof src !== "string") return null
  const s = src.trim()
  if (!s) return null
  const probe = s.replace(/[\u0000- \u007f-\u009f]/g, "")
  return SAFE_IMG.test(probe) ? s : null
}

// Formatting that arrives as inline styles (Word, Google Docs, web pages) cannot be
// read from `element.style`: under the application's CSP (style-src 'self') the
// browser refuses to apply style attributes, even in the inert document used to
// parse a paste, and reports a violation. paste.js therefore turns the style
// attributes of pasted HTML into a `data-sx` token list on the *string*, before
// any DOM parse, and these rules read the tokens: b bold, i italic, s strike,
// nb font-weight normal, ni font-style normal.
const sx = (dom, token) => (dom.getAttribute("data-sx") || "").split(" ").includes(token)
const nodes = {
  doc: {content: "block+"},

  paragraph: {
    content: "inline*",
    group: "block",
    parseDOM: [{tag: "p"}],
    toDOM() { return ["p", 0] }
  },

  heading: {
    attrs: {level: {default: 1}},
    content: "inline*",
    group: "block",
    defining: true,
    parseDOM: [1, 2, 3, 4, 5, 6].map(level => ({tag: "h" + level, attrs: {level}})),
    toDOM(node) { return ["h" + node.attrs.level, 0] }
  },

  blockquote: {
    content: "block+",
    group: "block",
    defining: true,
    parseDOM: [{tag: "blockquote"}],
    toDOM() { return ["blockquote", 0] }
  },

  // `* * *` in Markdown. Every thematic break spelling parses to this node.
  scene_break: {
    group: "block",
    parseDOM: [{tag: "hr"}, {tag: "div.scene-break"}],
    toDOM() { return ["div", {class: "scene-break", role: "separator"}] }
  },

  bullet_list: {
    content: "list_item+",
    group: "block",
    attrs: {tight: {default: true}},
    parseDOM: [{tag: "ul", getAttrs: dom => ({tight: dom.getAttribute("data-tight") !== "false"})}],
    toDOM(node) { return ["ul", {"data-tight": String(!!node.attrs.tight)}, 0] }
  },

  ordered_list: {
    content: "list_item+",
    group: "block",
    attrs: {order: {default: 1}, tight: {default: true}},
    parseDOM: [{
      tag: "ol",
      getAttrs(dom) {
        const start = parseInt(dom.getAttribute("start") || "1", 10)
        return {order: start >= 0 && start < 1e9 ? start : 1, tight: dom.getAttribute("data-tight") !== "false"}
      }
    }],
    toDOM(node) {
      const attrs = {"data-tight": String(!!node.attrs.tight)}
      if (node.attrs.order !== 1) attrs.start = node.attrs.order
      return ["ol", attrs, 0]
    }
  },

  // `checked` is null for a plain item, true/false for a GFM task item.
  list_item: {
    content: "block+",
    defining: true,
    attrs: {checked: {default: null}},
    parseDOM: [{
      tag: "li",
      getAttrs(dom) {
        const c = dom.getAttribute("data-checked")
        return {checked: c === "true" ? true : c === "false" ? false : null}
      }
    }],
    toDOM(node) {
      const c = node.attrs.checked
      return c === null ? ["li", 0] : ["li", {class: "task", "data-checked": c ? "true" : "false"}, 0]
    }
  },

  // `[^label]: ...`. Placed where it was written, never moved to the end.
  footnote_def: {
    content: "block+",
    group: "block",
    defining: true,
    attrs: {label: {default: "1"}},
    parseDOM: [{tag: "div.footnote-def", getAttrs: dom => ({label: dom.getAttribute("data-label") || "1"})}],
    toDOM(node) { return ["div", {class: "footnote-def", "data-label": node.attrs.label}, 0] }
  },

  code_block: {
    content: "text*",
    marks: "",
    group: "block",
    code: true,
    defining: true,
    attrs: {params: {default: ""}},
    parseDOM: [{
      tag: "pre",
      preserveWhitespace: "full",
      getAttrs: dom => ({params: dom.getAttribute("data-params") || ""})
    }],
    toDOM(node) {
      return ["pre", node.attrs.params ? {"data-params": node.attrs.params} : {}, ["code", 0]]
    }
  },

  // Markdown the authoring schema does not model: HTML blocks and comments
  // (including the trailing `<!-- scriobh:alts ... -->`), tables, front matter,
  // `:::` containers, link reference definitions. Written back verbatim.
  // kind: html | alts | table | front_matter | directive | reference | unparsed
  source_block: {
    content: "text*",
    marks: "",
    group: "block",
    code: true,
    defining: true,
    attrs: {kind: {default: "html"}},
    parseDOM: [{
      tag: "pre.source-block",
      priority: 60,
      preserveWhitespace: "full",
      getAttrs: dom => ({kind: dom.getAttribute("data-kind") || "html"})
    }],
    toDOM(node) { return ["pre", {class: "source-block", "data-kind": node.attrs.kind}, ["code", 0]] }
  },

  text: {group: "inline"},

  image: {
    inline: true,
    group: "inline",
    draggable: true,
    attrs: {src: {}, alt: {default: ""}, title: {default: null}},
    parseDOM: [
      // Our own clipboard HTML (cut and paste inside the editor): an image whose
      // destination is off the instance was written with data-src, never src.
      {
        tag: "img[data-src]",
        priority: 60,
        getAttrs: dom => ({src: dom.getAttribute("data-src"), alt: dom.getAttribute("alt") || "", title: dom.getAttribute("title") || null})
      },
      {
        tag: "img[src]",
        getAttrs(dom) {
          const src = safeSrc(dom.getAttribute("src"))
          if (!src) return false
          return {src, alt: dom.getAttribute("alt") || "", title: dom.getAttribute("title") || null}
        }
      }
    ],
    // The editor never makes a request for a document's image unless it lives on
    // this instance: an absolute, protocol-relative or data: destination is kept in
    // the document (and in the Markdown) but drawn as its alt text.
    toDOM(node) {
      const {src, alt, title} = node.attrs
      const local = safeSrc(src)
      const attrs = local ? {src: local, alt} : {"data-src": src, alt, class: "remote-image"}
      if (title) attrs.title = title
      return ["img", attrs]
    }
  },

  hard_break: {
    inline: true,
    group: "inline",
    selectable: false,
    parseDOM: [{tag: "br"}],
    toDOM() { return ["br"] }
  },

  // `[[Title]]` and `[[Title|alias]]`. An atom: Backspace right after one turns
  // it back into its source text (see editor.js).
  wiki_link: {
    inline: true,
    group: "inline",
    atom: true,
    attrs: {target: {}, alias: {default: null}},
    parseDOM: [{
      tag: "span.wikilink",
      getAttrs: dom => {
        const target = dom.getAttribute("data-target")
        return target ? {target, alias: dom.getAttribute("data-alias") || null} : false
      }
    }],
    toDOM(node) {
      const {target, alias} = node.attrs
      const attrs = {class: "wikilink", "data-target": target}
      if (alias) attrs["data-alias"] = alias
      return ["span", attrs, alias || target]
    }
  },

  // `[^label]`
  footnote_ref: {
    inline: true,
    group: "inline",
    atom: true,
    attrs: {label: {}},
    parseDOM: [{tag: "sup.footnote-ref", getAttrs: dom => ({label: dom.getAttribute("data-label") || "1"})}],
    toDOM(node) { return ["sup", {class: "footnote-ref", "data-label": node.attrs.label}, node.attrs.label] }
  },

  // Inline HTML or anything else inline the schema does not model, held raw.
  source_inline: {
    inline: true,
    group: "inline",
    atom: true,
    attrs: {raw: {}},
    parseDOM: [{tag: "code.source-inline", priority: 60, getAttrs: dom => ({raw: dom.textContent || ""})}],
    toDOM(node) { return ["code", {class: "source-inline"}, node.attrs.raw] }
  }
}

// Mark order is nesting order, outermost first. `code` is last: a non-escaping
// mark must be the innermost one for the serializer.
const marks = {
  // `%%like this%%`: inline, excluded from word counts and exports.
  private_note: {
    inclusive: false,
    parseDOM: [{tag: "span.private-note"}],
    toDOM() { return ["span", {class: "private-note"}, 0] }
  },

  link: {
    attrs: {href: {}, title: {default: null}},
    inclusive: false,
    parseDOM: [{
      tag: "a[href]",
      getAttrs(dom) {
        const href = safeHref(dom.getAttribute("href"))
        if (href === null) return false
        return {href, title: dom.getAttribute("title") || null}
      }
    }],
    // Defence in depth: a stored destination with an unsafe scheme is never put in an href.
    toDOM(mark) {
      const {href, title} = mark.attrs
      const attrs = {}
      const safe = safeHref(href)
      if (safe !== null) attrs.href = safe
      if (title) attrs.title = title
      return ["a", attrs, 0]
    }
  },

  em: {
    parseDOM: [
      {tag: "i", getAttrs: dom => !sx(dom, "ni") && null},
      {tag: "em"},
      {tag: "[data-sx~=i]", consuming: false}
    ],
    toDOM() { return ["em", 0] }
  },

  strong: {
    parseDOM: [
      {tag: "strong"},
      // Google Docs wraps a whole paste in <b style="font-weight:normal">.
      {tag: "b", getAttrs: dom => !sx(dom, "nb") && null},
      {tag: "[data-sx~=b]", consuming: false}
    ],
    toDOM() { return ["strong", 0] }
  },

  strike: {
    parseDOM: [
      {tag: "s"},
      {tag: "del"},
      {tag: "strike"},
      {tag: "[data-sx~=s]", consuming: false}
    ],
    toDOM() { return ["s", 0] }
  },

  code: {
    parseDOM: [{tag: "code"}, {tag: "tt"}],
    toDOM() { return ["code", 0] }
  }
}

export const schema = new Schema({nodes, marks})
