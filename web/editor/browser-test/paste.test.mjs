// Paste sanitising, run in Chromium with the application's CSP header. Pasted HTML
// must end up as schema nodes only and clean Markdown; plain text stays literal.
// The paste events are synthetic (ClipboardEvent + DataTransfer dispatched on the
// editor): the OS clipboard is not touched.

import assert from "node:assert/strict"
import {suite, md, paste, sleep, mount} from "./harness.mjs"

const {bt, fresh, state} = suite()

// Every element the editing surface may contain after any paste.
const ALLOWED = ["P", "H1", "H2", "H3", "H4", "H5", "H6", "BLOCKQUOTE", "UL", "OL", "LI", "PRE", "CODE", "EM", "STRONG", "S", "A", "BR", "IMG", "DIV", "SPAN", "SUP"]

const audit = page => page.evaluate(() => {
  const dom = h.ed.view.dom
  const bad = []
  for (const el of dom.querySelectorAll("*")) {
    if (el.hasAttribute("style")) bad.push("style attribute on " + el.tagName)
    for (const a of el.attributes) if (/^on/i.test(a.name)) bad.push("event handler " + a.name)
    if (el.tagName === "A" && /^\s*(javascript|vbscript|data):/i.test(el.getAttribute("href") || "")) bad.push("unsafe href")
    if (el.tagName === "IMG" && !/^(\/(?!\/)|\.\.?\/|[^:/?#]+([/?#]|$))/.test(el.getAttribute("src") || "")) bad.push("off-instance img src " + el.getAttribute("src"))
    if (el.tagName === "SPAN" && !el.classList.contains("wikilink") && !el.classList.contains("private-note")) bad.push("stray span")
    if (el.tagName === "DIV" && !el.classList.contains("scene-break") && !el.classList.contains("footnote-def")) bad.push("stray div")
  }
  // Pasted style attributes must be removed before any DOM parse: under style-src 'self'
  // each one would raise a violation.
  for (const v of h.violations) bad.push("CSP violation " + v.directive)
  return {bad, tags: [...new Set([...dom.querySelectorAll("*")].map(e => e.tagName))]}
})

const pasteAndRead = async (page, html, text) => {
  await paste(page, {html, text})
  await sleep(30)
  return md(page)
}

bt("Word paste: styled spans, Mso classes, o:p and NBSP-only paragraphs become clean paragraphs", async () => {
  const {page, context} = await fresh()
  const html = `<html xmlns:o="urn:schemas-microsoft-com:office:office"><head><meta charset="utf-8"><style>p.MsoNormal{margin:0cm}</style></head><body lang=EN-IE><!--StartFragment--><p class=MsoNormal style='margin:0cm'><span style='font-size:12.0pt;font-family:"Times New Roman",serif'>Hello <b>bold</b> and <i>italic</i> text</span><o:p></o:p></p><p class=MsoNormal><o:p>&nbsp;</o:p></p><p class=MsoNormal><span style='mso-bidi-font-weight:bold'>Second paragraph</span></p><!--EndFragment--></body></html>`
  assert.equal(await pasteAndRead(page, html, "Hello bold and italic text\n\nSecond paragraph"), "Hello **bold** and *italic* text\n\nSecond paragraph\n")
  const a = await audit(page)
  assert.deepEqual(a.bad, [])
  await context.close()
})

bt("Google Docs paste: the b font-weight:normal wrapper is not bold; weight-700 spans are", async () => {
  const {page, context} = await fresh()
  const html = `<meta charset='utf-8'><meta charset="utf-8"><b style="font-weight:normal;" id="docs-internal-guid-1234-5678"><p dir="ltr" style="line-height:1.38;margin-top:0pt;margin-bottom:0pt;"><span style="font-size:11pt;font-family:Arial,sans-serif;color:#000000;font-weight:400;font-style:normal;text-decoration:none;">Plain </span><span style="font-size:11pt;font-family:Arial,sans-serif;font-weight:700;">bold span</span><span style="font-size:11pt;font-weight:400;font-style:italic;"> italic span</span></p><br /><p dir="ltr"><span style="font-weight:400;text-decoration-line:line-through;">gone</span></p></b>`
  assert.equal(await pasteAndRead(page, html, "x"), "Plain **bold span** *italic span*\n\n~~gone~~\n")
  assert.deepEqual((await audit(page)).bad, [])
  await context.close()
})

bt("web page paste: nested divs, font, script, onclick, inline styles", async () => {
  const {page, context} = await fresh()
  const html = `<div class="outer" style="color:red"><div id="inner"><h2 style="margin:0">Heading</h2><p>Para with <a href="http://example.com/x" onclick="evil()" style="color:blue" target="_blank">a link</a><script>alert(1)</script> and <font color="red" face="Arial" size="4">font text</font> and <span style="background:yellow">highlighted</span>.</p><div>loose div text</div></div></div>`
  const out = await pasteAndRead(page, html, "x")
  assert.equal(out, "## Heading\n\nPara with [a link](http://example.com/x) and font text and highlighted.\n\nloose div text\n")
  assert.deepEqual((await audit(page)).bad, [])
  await context.close()
})

bt("dangerous markup: javascript: links, onerror images, data: and remote images, iframes, style tags", async () => {
  const {page, context} = await fresh()
  const html = `<p><a href="javascript:alert(1)">click me</a> <a href="  JaVaScRiPt:alert(2)">spaced</a> <a href="vbscript:x">vb</a> <a href="data:text/html,<script>alert(3)</script>">data link</a> <img src="local.png" onerror="alert(4)" alt="local"> <img src="data:image/png;base64,AAAA" alt="inline data"> <img src="https://tracker.example/p.png" alt="remote"> <img src="//tracker.example/q.png" alt="proto-relative"> <iframe src="http://evil.example"></iframe><object data="x"></object><style>p{color:red}</style><svg onload="alert(5)"><text>svg text</text></svg> <a href="mailto:me@example.com">mail</a> <a href="/relative/path">rel</a> <a href="#frag">frag</a></p>`
  state.server.requests.length = 0
  const out = await pasteAndRead(page, html, "x")
  assert.ok(!/javascript|vbscript|data:|onerror|onload|<|iframe|evil|tracker/i.test(out), out)
  assert.match(out, /click me/)
  assert.match(out, /\[mail\]\(mailto:me@example\.com\)/)
  assert.match(out, /\[rel\]\(\/relative\/path\)/)
  assert.match(out, /\[frag\]\(#frag\)/)
  assert.match(out, /!\[local\]\(local\.png\)/)
  const a = await audit(page)
  assert.deepEqual(a.bad, [])
  assert.deepEqual(await page.evaluate(() => h.violations), [])
  // No request left the page for the remote or data images.
  await sleep(200)
  assert.ok(!state.server.requests.some(r => /tracker|evil/.test(r)))
  await context.close()
})

bt("lists, quotes, code and tables paste as schema nodes", async () => {
  const {page, context} = await fresh()
  const html = `<ul><li>one</li><li>two<ul><li>nested</li></ul></li></ul><blockquote><p>quoted</p></blockquote><pre><code>let a = 1\nlet b = 2</code></pre><table><tbody><tr><td>cell a</td><td>cell b</td></tr></tbody></table><ol start="3"><li>three</li></ol>`
  const out = await pasteAndRead(page, html, "x")
  assert.match(out, /^- one\n- two\n  - nested\n\n> quoted\n\n```\nlet a = 1\nlet b = 2\n```\n\ncell a\n\ncell b\n\n3\. three\n$/)
  assert.deepEqual((await audit(page)).bad, [])
  await context.close()
})

bt("plain-text paste is literal: it never turns into headings, lists, emphasis or links", async () => {
  const {page, context} = await fresh()
  const text = "Line one\nLine two\n\n# not a heading\n*not em* [[not wiki]] and %%not private%%\n- not a list\n1. not numbered\n> not a quote\n```\nnot code\n```"
  const out = await pasteAndRead(page, null, text)
  assert.equal(out, [
    "Line one", "", "Line two", "", "\\# not a heading", "",
    "\\*not em\\* \\[\\[not wiki\\]\\] and \\%\\%not private\\%\\%", "",
    "\\- not a list", "", "1\\. not numbered", "", "\\> not a quote", "", "\\`\\`\\`", "", "not code", "", "\\`\\`\\`", ""
  ].join("\n"))
  assert.deepEqual((await audit(page)).bad, [])
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll("h1,ul,ol,blockquote,pre,em,strong,.wikilink,.private-note").length), 0)
  await context.close()
})

bt("plain text pasted mid-paragraph stays inline and literal", async () => {
  const {page, context} = await fresh("before after\n")
  await page.evaluate(() => {
    const t = h.ed.view.dom.querySelector("p").firstChild
    getSelection().collapse(t, 7)
  })
  await sleep(30)
  assert.equal(await pasteAndRead(page, null, "*star* and _under_ "), "before \\*star\\* and \\_under\\_ after\n")
  await context.close()
})

bt("paste as Markdown: the shortcut and the API parse the clipboard text", async () => {
  const {page, context} = await fresh()
  await page.keyboard.press("ControlOrMeta+Alt+Shift+V")
  await paste(page, {text: "# Heading\n\nSome *em* with [[Wiki]] and %%a note%%.\n\n- a\n- b"})
  await sleep(30)
  assert.equal(await md(page), "# Heading\n\nSome *em* with [[Wiki]] and %%a note%%.\n\n- a\n- b\n")
  // Without the shortcut the same text stays literal.
  await mount(page, "")
  await paste(page, {text: "*literal*"})
  assert.equal(await md(page), "\\*literal\\*\n")
  // The API does the same job for a toolbar button.
  await mount(page, "x\n")
  await page.evaluate(() => h.ed.pasteMarkdown("**bold**"))
  assert.match(await md(page), /\*\*bold\*\*/)
  await context.close()
})

bt("a bare URL pasted over selected text makes a link on it", async () => {
  const {page, context} = await fresh("read the docs now\n")
  await page.evaluate(() => {
    const t = h.ed.view.dom.querySelector("p").firstChild
    getSelection().setBaseAndExtent(t, 9, t, 13)
  })
  await sleep(30)
  assert.equal(await pasteAndRead(page, null, "https://example.com/docs"), "read the [docs](https://example.com/docs) now\n")
  await context.close()
})

bt("copy puts Markdown in text/plain", async () => {
  const {page, context} = await fresh("# Title\n\nSome *em* and **strong** with [[Wiki]].\n\n- a\n- b\n")
  await page.keyboard.press("ControlOrMeta+a")
  const text = await page.evaluate(() => {
    const dt = new DataTransfer()
    const ev = new ClipboardEvent("copy", {clipboardData: dt, bubbles: true, cancelable: true})
    h.ed.view.dom.dispatchEvent(ev)
    return {plain: dt.getData("text/plain"), html: dt.getData("text/html")}
  })
  assert.equal(text.plain, "# Title\n\nSome *em* and **strong** with [[Wiki]].\n\n- a\n- b")
  assert.match(text.html, /<h1/)
  await context.close()
})
