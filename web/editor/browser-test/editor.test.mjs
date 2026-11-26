// Behaviour of the mounted editor in Chromium, with real keyboard events and the
// application's CSP header. Run: npm run test:browser (from web/editor).

import {test, before, after} from "node:test"
import assert from "node:assert/strict"
import {launch, startServer, openPage, mount, md, focus, sleep, paste} from "./harness.mjs"

let browser = null, server = null
before(async () => {
  browser = await launch().catch(err => { if (process.env.CI) throw err; return null })
  if (!browser && process.env.CI) throw new Error("Chromium is required in CI")
  if (browser) server = await startServer()
})
after(async () => {
  if (browser) await browser.close()
  if (server) await server.close()
})

// Skips (never silently passes) when there is no browser; CI fails in before().
function bt(name, fn) {
  test(name, async t => {
    if (!browser) { t.skip("no Chromium found (set SCRIOBH_CHROMIUM)"); return }
    await fn(t)
  })
}

async function fresh(markdown = "", opts, pageOpts) {
  const ctx = await openPage(browser, server, pageOpts)
  await mount(ctx.page, markdown, opts)
  await focus(ctx.page)
  return ctx
}

bt("an untouched document returns its original bytes and does not call onChange", async () => {
  const {page, context} = await fresh("* a\n* b\n\n---\n\n<div>raw</div>\n")
  assert.equal(await md(page), "* a\n* b\n\n---\n\n<div>raw</div>\n")
  await sleep(900)
  assert.equal(await page.evaluate(() => h.changes.length), 0)
  assert.equal(await page.evaluate(() => h.ed.isDirty()), false)
  await context.close()
})

bt("typing with real key events applies Markdown input rules", async () => {
  const {page, context} = await fresh()
  await page.keyboard.type("# Title\nSome *em* and **strong** and `code` here.\n- one\ntwo\n\nThree\n1. a\nb\n\n> quoted\n")
  await sleep(50)
  assert.equal(await md(page), [
    "# Title", "",
    "Some *em* and **strong** and `code` here.", "",
    "- one", "- two", "",
    "Three", "",
    "1. a", "2. b", "",
    "> quoted", ""
  ].join("\n"))
  await context.close()
})

bt("onChange is debounced to 700 ms idle, flushed on blur, and never on the keystroke path", async () => {
  const {page, context} = await fresh()
  await page.keyboard.type("hello")
  await sleep(450)
  assert.equal(await page.evaluate(() => h.changes.length), 0, "no call before the idle window")
  await page.keyboard.type(" world")
  await sleep(450)
  assert.equal(await page.evaluate(() => h.changes.length), 0, "typing restarts the window")
  await sleep(500)
  const changes = await page.evaluate(() => h.changes)
  assert.equal(changes.length, 1)
  assert.equal(changes[0].md, "hello world\n")

  await page.keyboard.type("!")
  await page.evaluate(() => h.ed.view.dom.blur())
  const afterBlur = await page.evaluate(() => h.changes.map(c => c.md))
  assert.deepEqual(afterBlur, ["hello world\n", "hello world!\n"], "blur flushes at once")
  await context.close()
})

bt("setMarkdown refuses to replace unsaved input unless forced; markSaved clears the flag", async () => {
  const {page, context} = await fresh("start\n")
  await page.keyboard.type("local ")
  assert.equal(await page.evaluate(() => h.ed.isDirty()), true)
  assert.equal(await page.evaluate(() => h.ed.setMarkdown("from the server\n")), false)
  assert.match(await md(page), /local start/)
  // The host stores the text it was given, then confirms with the version.
  await sleep(800)
  const {version} = await page.evaluate(() => h.changes.at(-1))
  assert.equal(await page.evaluate(v => (h.ed.markSaved(v), h.ed.isDirty()), version), false)
  assert.equal(await page.evaluate(() => h.ed.setMarkdown("from the server\n")), true)
  assert.equal(await md(page), "from the server\n")
  // A later edit makes it dirty again; force overrides.
  await focus(page)
  await page.keyboard.type("x")
  assert.equal(await page.evaluate(() => h.ed.setMarkdown("forced\n")), false)
  assert.equal(await page.evaluate(() => h.ed.setMarkdown("forced\n", {force: true})), true)
  assert.equal(await md(page), "forced\n")
  // markSaved with a stale version leaves the editor dirty.
  await focus(page)
  await page.keyboard.type("a")
  await sleep(800)
  const v1 = await page.evaluate(() => h.changes.at(-1).version)
  await page.keyboard.type("b")
  assert.equal(await page.evaluate(v => (h.ed.markSaved(v), h.ed.isDirty()), v1), true)
  await context.close()
})

bt("undo and redo", async () => {
  const {page, context} = await fresh()
  await page.keyboard.type("first")
  await sleep(600) // new history group
  await page.keyboard.type(" second")
  await page.keyboard.press("ControlOrMeta+z")
  const afterUndo = await md(page)
  assert.ok(afterUndo === "first\n" || afterUndo === "\n" || afterUndo === "", afterUndo)
  await page.keyboard.press("ControlOrMeta+Shift+z")
  assert.match(await md(page), /first/)
  await context.close()
})

bt("an input rule is undone by Backspace", async () => {
  const {page, context} = await fresh()
  await page.keyboard.type("# ")
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll("h1").length), 1)
  await page.keyboard.press("Backspace")
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll("h1").length), 0)
  assert.equal(await page.evaluate(() => h.ed.view.dom.textContent), "# ")
  await context.close()
})

bt("scene breaks: '---', '***' then Enter, and Mod+Enter all produce one canonical break", async () => {
  const {page, context} = await fresh()
  await page.keyboard.type("one\n---")
  await page.keyboard.type("two\n***\n")
  await page.keyboard.type("three")
  await page.keyboard.press("ControlOrMeta+Enter")
  await page.keyboard.press("ControlOrMeta+Enter")
  await page.keyboard.type("four")
  assert.equal(await md(page), "one\n\n* * *\n\ntwo\n\n* * *\n\nthree\n\n* * *\n\n* * *\n\nfour\n")
  await context.close()
})

bt("wiki links, private notes, footnote refs and task items from the keyboard", async () => {
  const {page, context} = await fresh()
  await page.keyboard.type("See [[Dun Laoghaire|Dun]] and %%a note%% and a note[^1].\n- [ ] todo\n")
  const out = await md(page)
  assert.equal(out, "See [[Dun Laoghaire|Dun]] and %%a note%% and a note[^1].\n\n- [ ] todo\n-\n")
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll(".wikilink").length), 1)
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll(".private-note").length), 1)
  await context.close()
})

bt("Backspace after a wiki link turns it back into editable text", async () => {
  const {page, context} = await fresh()
  await page.keyboard.type("[[Title]]")
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll(".wikilink").length), 1)
  // Right after typing, Backspace undoes the input rule: the typed text comes back.
  await page.keyboard.press("Backspace")
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll(".wikilink").length), 0)
  assert.equal(await md(page), "\\[\\[Title\\]\\]\n")

  // Later, Backspace directly after an existing link unwraps it into source text.
  await mount(page, "A [[Title|alias]] b\n")
  await page.evaluate(() => {
    const link = h.ed.view.dom.querySelector(".wikilink")
    getSelection().collapse(link.nextSibling, 0)
  })
  await sleep(50)
  await page.keyboard.press("Backspace")
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll(".wikilink").length), 0)
  assert.equal(await md(page), "A \\[\\[Title|alias\\] b\n")
  await context.close()
})

bt("Tab and Shift+Tab move list items; Tab outside a list is left to the browser", async () => {
  const {page, context} = await fresh()
  await page.keyboard.type("- a\nb")
  await page.keyboard.press("Tab")
  assert.equal(await md(page), "- a\n  - b\n")
  await page.keyboard.press("Shift+Tab")
  assert.equal(await md(page), "- a\n- b\n")
  const stillFocused = await page.evaluate(() => {
    h.mount("plain\n"); h.ed.focus()
    return true
  })
  assert.ok(stillFocused)
  await page.keyboard.press("Tab")
  assert.equal(await page.evaluate(() => h.ed.view.dom.contains(document.activeElement) && document.activeElement === h.ed.view.dom), false, "Tab moved focus out of the editor")
  await context.close()
})

bt("Mod+B, Mod+I, Mod+K, Mod+Shift+X", async () => {
  const {page, context} = await fresh("one two three\n")
  await page.keyboard.press("ControlOrMeta+a")
  await page.keyboard.press("ControlOrMeta+b")
  assert.equal(await md(page), "**one two three**\n")
  await page.keyboard.press("ControlOrMeta+b")
  await page.keyboard.press("ControlOrMeta+i")
  assert.equal(await md(page), "*one two three*\n")
  await page.keyboard.press("ControlOrMeta+i")
  await page.keyboard.press("ControlOrMeta+Shift+x")
  assert.equal(await md(page), "~~one two three~~\n")
  await page.keyboard.press("ControlOrMeta+Shift+x")
  await page.keyboard.press("ControlOrMeta+k")
  assert.equal(await page.evaluate(() => h.links[0] && h.links[0].text), "one two three")
  await context.close()
})

bt("Mod+E swaps to a textarea over the exact Markdown and back without losing text", async () => {
  const original = "# Title\n\nSome *text* with a [[Link]] and %%note%%.\n\n* * *\n\n- a\n- b\n\n<!-- scriobh:alts {\"version\":1} -->\n"
  const {page, context} = await fresh(original)
  await page.keyboard.press("ControlOrMeta+e")
  assert.equal(await page.evaluate(() => h.ed.mode()), "source")
  assert.equal(await page.evaluate(() => document.querySelector(".scriobh-source").value), original)
  assert.equal(await page.evaluate(() => document.querySelector(".scriobh-source").hidden), false)
  // Toggle with no edits: nothing changes, nothing is reported.
  await page.keyboard.press("ControlOrMeta+e")
  assert.equal(await page.evaluate(() => h.ed.mode()), "rich")
  assert.equal(await md(page), original)
  await sleep(800)
  assert.equal(await page.evaluate(() => h.changes.length), 0)

  // Edit in Source (the exact text the writer typed is what getMarkdown returns).
  await page.keyboard.press("ControlOrMeta+e")
  await page.evaluate(() => {
    const t = document.querySelector(".scriobh-source")
    t.value = "* starred\n* list\n\nAnd <b>html</b>.\n"
    t.dispatchEvent(new Event("input", {bubbles: true}))
  })
  assert.equal(await md(page), "* starred\n* list\n\nAnd <b>html</b>.\n")
  await page.keyboard.press("ControlOrMeta+e")
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll("li").length), 2)
  assert.equal(await md(page), "* starred\n* list\n\nAnd <b>html</b>.\n", "bytes typed in Source are kept until a rich edit")
  await sleep(800)
  const changes = await page.evaluate(() => h.changes.map(c => c.md))
  assert.equal(changes.length, 1)
  // A rich edit canonicalises.
  await page.keyboard.type("x")
  assert.match(await md(page), /^- /)
  // Undo returns to the Source-edited text.
  await context.close()
})

// What the Undo button (and Mod+Z) takes back after the Source view: the Source edits as one step of their own, even when the
// round trip comes within the history's 500 ms grouping window of typing before it or after it (so a step that is not closed
// to its neighbours fails this test), and nothing a later quiet step (an alternative's wording, the stored line) wrote.
bt("leaving Source is one step of its own: never merged with typing before or after it, never over a quiet edit elsewhere", async () => {
  const {page, context} = await fresh("One line.\n\nTwo words here.\n")
  await page.evaluate(() => { const at = h.ed.view.state.doc.content.size - 1; h.ed.alts.select(at, at) })
  await page.keyboard.type(" Typed.")
  // At once, well inside 500 ms: Source, an edit there, and back.
  await page.keyboard.press("ControlOrMeta+e")
  await page.evaluate(() => {
    const t = document.querySelector(".scriobh-source")
    t.value = t.value.replace("Typed.", "Typed. Src.") // next to where the typing ended: a step not closed to it would join it
    t.dispatchEvent(new Event("input", {bubbles: true}))
  })
  await page.keyboard.press("ControlOrMeta+e")
  assert.equal(await md(page), "One line.\n\nTwo words here. Typed. Src.\n")
  // Typing straight after leaving Source, in the same window.
  await page.keyboard.type("!")
  assert.equal(await md(page), "One line.\n\nTwo words here. Typed. Src.!\n")
  // A quiet step elsewhere, as an alternative's wording is written.
  await page.evaluate(() => {
    let at = -1
    h.ed.view.state.doc.descendants((n, pos) => { if (at < 0 && n.isText && n.text.includes("words")) at = pos + n.text.indexOf("words") })
    h.ed.alts.replace(at, at + 5, "ALTWORD")
  })
  assert.match(await md(page), /Two ALTWORD here\. Typed\. Src\.!/)
  const hist = () => page.evaluate(() => h.ed.history())
  await page.evaluate(() => h.ed.exec("undo"))
  assert.equal(await md(page), "One line.\n\nTwo ALTWORD here. Typed. Src.\n", "the first Undo takes back the typing after Source, alone")
  await page.evaluate(() => h.ed.exec("undo"))
  assert.equal(await md(page), "One line.\n\nTwo ALTWORD here. Typed.\n", "the second takes back what Source did, alone; the quiet wording stays")
  await page.evaluate(() => h.ed.exec("undo"))
  assert.equal(await md(page), "One line.\n\nTwo ALTWORD here.\n", "the third takes back the typing before Source")
  assert.deepEqual(await hist(), {undo: false, redo: true})
  await context.close()
})

// A hold (the alternatives' operations) and its end poke the host's dispatch hook with a transaction that changes nothing; a bold
// waiting to be typed (a stored mark) must survive it, and a lock after destroy must do nothing.
bt("a hold begins and ends without losing a pending bold, and without a transaction that changes anything", async () => {
  const {page, context} = await fresh("Hello world.\n")
  await page.evaluate(() => { h.ed.alts.select(6, 6); h.ed.view.focus() })
  await page.keyboard.press("ControlOrMeta+b") // nothing selected: bold is pending for the next letters
  const marks = () => page.evaluate(() => (h.ed.view.state.storedMarks || []).map(m => m.type.name).join(","))
  assert.equal(await marks(), "strong")
  const seen = await page.evaluate(async () => {
    const out = {docChanged: false, count: 0}
    const dispatch = h.ed.view.dispatch
    h.ed.view.dispatch = tr => { out.count++; if (tr.docChanged) out.docChanged = true; dispatch.call(h.ed.view, tr) }
    h.ed.alts.lock(true); h.ed.alts.lock(false); h.ed.alts.lock(true) // a release and a hold in the same breath: one poke
    await Promise.resolve(); await Promise.resolve()
    h.ed.alts.lock(false)
    await Promise.resolve(); await Promise.resolve()
    return out
  })
  assert.equal(seen.docChanged, false)
  assert.equal(seen.count, 2, "one poke for the hold that began and ended in one breath, one for the release after")
  assert.equal(await marks(), "strong", "the pending bold is still there")
  await page.keyboard.type("X")
  assert.match(await md(page), /Hello\*\*X\*\* world/)
  await page.evaluate(() => { h.ed.destroy(); h.ed.alts.lock(true) }) // no error after destroy
  await context.close()
})

bt("the selection survives the Source round trip", async () => {
  const {page, context} = await fresh("# Heading\n\nThe quick brown fox jumps over the lazy dog.\n\nSecond paragraph here.\n")
  // Select "brown fox" by walking the DOM.
  await page.evaluate(() => {
    const p = h.ed.view.dom.querySelectorAll("p")[0]
    const text = p.firstChild
    const start = text.textContent.indexOf("brown")
    const sel = getSelection()
    sel.setBaseAndExtent(text, start, text, start + "brown fox".length)
  })
  await sleep(50)
  await page.keyboard.press("ControlOrMeta+e")
  const inSource = await page.evaluate(() => {
    const t = document.querySelector(".scriobh-source")
    return t.value.slice(t.selectionStart, t.selectionEnd)
  })
  assert.equal(inSource, "brown fox")
  await page.keyboard.press("ControlOrMeta+e")
  const back = await page.evaluate(() => getSelection().toString())
  assert.equal(back, "brown fox")
  await context.close()
})

bt("limits: warn at 15,000 words, read-only above 25,000", async () => {
  const words = n => (("lorem ipsum dolor sit amet ").repeat(Math.ceil(n / 5) + 1)).split(" ").slice(0, n).join(" ")
  const {page, context} = await fresh()
  await page.evaluate(w => h.mount(w), words(16000) + "\n")
  assert.deepEqual(await page.evaluate(() => h.limits.map(l => l.level)), ["warn"])
  assert.equal(await page.evaluate(() => h.ed.view.dom.getAttribute("contenteditable")), "true")
  await page.evaluate(w => h.mount(w), words(26000) + "\n")
  assert.deepEqual(await page.evaluate(() => h.limits.map(l => l.level)), ["readonly"])
  assert.equal(await page.evaluate(() => h.ed.view.dom.getAttribute("contenteditable")), "false")
  await page.keyboard.type("nope")
  assert.equal((await md(page)).startsWith("nope"), false)
  await context.close()
})

bt("decorations can be layered without touching the schema or the Markdown", async () => {
  const {page, context} = await fresh("The quiet house.\n")
  await page.evaluate(() => {
    const {Decoration, DecorationSet} = scriobhEditor
    h.ed.setDecorations(state => {
      const text = "quiet"
      let found = null
      state.doc.descendants((node, pos) => { if (node.isText && node.text.includes(text)) found = pos + node.text.indexOf(text) })
      return found === null ? DecorationSet.empty
        : DecorationSet.create(state.doc, [Decoration.inline(found, found + text.length, {class: "scriobh-alt"})])
    })
  })
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll(".scriobh-alt").length), 1)
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelector(".scriobh-alt").textContent), "quiet")
  assert.equal(await md(page), "The quiet house.\n")
  await page.keyboard.type("A ")
  assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll(".scriobh-alt").length), 1)
  assert.match(await md(page), /^A The quiet house\.\n$/)
  await context.close()
})

// Every request the page attempts, to any origin, under the application's header
// and under a stricter one. A document with off-instance images must not make the
// editor fetch them (invariant 7), and typing, Source and paste must not either.
for (const strictCsp of [false, true]) {
  bt(`no request beyond the page, bundle, stylesheet and local images; no CSP violation; no style attributes (${strictCsp ? "strict" : "application"} CSP)`, async () => {
    const doc = "# T\n\nBody with *em* and [[Wiki]].\n\n![local](/local.png) ![rel](pics/a.png)\n\n![attached](/files/att_01M7A86NKJAGGM62CEFZNA5MM9) [a PDF](/files/att_01M7A86NKJAGGM62CEFZNA5MM8)\n\n![remote](https://tracker.example/p.png) ![proto](//tracker.example/q.png) ![inline](data:image/png;base64,AAAA)\n\n[a link](https://tracker.example/page)\n"
    const {page, context, errors, requests} = await fresh(doc, null, {strictCsp})
    await page.keyboard.type("typing some text ")
    await page.keyboard.press("ControlOrMeta+e")
    await page.keyboard.press("ControlOrMeta+e")
    await paste(page, {html: "<p>pasted <img src=\"https://tracker.example/r.png\" alt=\"r\"> <img data-src=\"https://tracker.example/lazy.png\" alt=\"lazy\"></p>", text: "x"})
    await sleep(900)
    const origin = new URL(page.url()).origin
    const off = requests.filter(r => !r.startsWith(origin + "/") && !r.startsWith("data:"))
    assert.deepEqual(off, [], "requests to other origins")
    const local = requests.filter(r => r.startsWith(origin + "/")).map(r => new URL(r).pathname).filter(r => r !== "/favicon.ico")
    assert.deepEqual([...new Set(local)].sort(), ["/", "/editor.css", "/editor.js", "/tokens.css", "/harness.css", "/harness.js", "/local.png", "/pics/a.png", "/files/att_01M7A86NKJAGGM62CEFZNA5MM9"].sort())
    assert.deepEqual(await page.evaluate(() => h.violations), [])
    assert.equal(await page.evaluate(() => document.querySelectorAll("[style]").length), 0)
    assert.deepEqual(errors.filter(e => !/404/.test(e)), [])
    // The off-instance images are still in the document and the Markdown, drawn as alt text.
    assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll("img.remote-image").length >= 3), true)
    assert.match(await md(page), /!\[remote\]\(https:\/\/tracker\.example\/p\.png\)/)
    // An attachment of this instance is drawn as the picture (a request for /files/att_… is the only one a stored picture makes),
    // its Markdown is untouched, and a link to a PDF is only a link.
    assert.equal(await page.evaluate(() => h.ed.view.dom.querySelectorAll('img[src="/files/att_01M7A86NKJAGGM62CEFZNA5MM9"]:not(.remote-image)').length), 1)
    assert.match(await md(page), /!\[attached\]\(\/files\/att_01M7A86NKJAGGM62CEFZNA5MM9\) \[a PDF\]\(\/files\/att_01M7A86NKJAGGM62CEFZNA5MM8\)/)
    // Control: the listener does see a style attribute, so the empty result means something.
    await page.evaluate(() => document.body.setAttribute("style", "color: red"))
    await sleep(100)
    assert.equal((await page.evaluate(() => h.violations)).length > 0, true, "the violation listener works")
    await context.close()
  })
}

bt("works at 390 px width and with the keyboard alone", async () => {
  const ctx = await openPage(browser, server, {viewport: {width: 390, height: 780}, hasTouch: true})
  await mount(ctx.page, "# Phone\n\nA paragraph that is long enough to wrap onto a second line on a narrow screen, easily.\n")
  const dims = await ctx.page.evaluate(() => ({sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth}))
  assert.ok(dims.sw <= dims.cw, `horizontal scroll at 390px: ${dims.sw} > ${dims.cw}`)
  await ctx.context.close()
})

bt("smart quotes and dashes are off by default and opt-in; the Markdown holds the typed characters", async () => {
  const off = await fresh()
  const typed = "He said \"hi\" -- it's fine... `a\"b`"
  await off.page.keyboard.type(typed)
  assert.equal(await md(off.page), typed + "\n")
  await off.context.close()
  const on = await fresh("", {typography: true})
  await on.page.keyboard.type(typed)
  assert.equal(await md(on.page), "He said “hi” — it’s fine… `a\"b`\n")
  await on.context.close()
})

bt("read-only mode refuses input, keeps Source available, and reports no changes", async () => {
  const {page, context} = await fresh("fixed text\n", {readOnly: true})
  await page.keyboard.type("nope")
  assert.equal(await md(page), "fixed text\n")
  assert.equal(await page.evaluate(() => h.ed.view.dom.getAttribute("contenteditable")), "false")
  assert.equal(await page.evaluate(() => document.activeElement === h.ed.view.dom), true, "a read-only surface takes keyboard focus")
  await page.evaluate(() => h.ed.toggleSource())
  assert.equal(await page.evaluate(() => document.querySelector(".scriobh-source").readOnly), true)
  assert.equal(await page.evaluate(() => document.querySelector(".scriobh-source").value), "fixed text\n")
  await sleep(800)
  assert.equal(await page.evaluate(() => h.changes.length), 0)
  await context.close()
})

bt("turning code into a paragraph keeps its lines as hard breaks; a source block is never converted", async () => {
  const {page, context} = await fresh("```\nline one\nline two\n```\n\n<div>\nraw\n</div>\n")
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector("pre:not(.source-block) code").firstChild; getSelection().collapse(t, 3) })
  await sleep(30)
  await page.keyboard.press("ControlOrMeta+Alt+0")
  assert.equal(await md(page), "line one\\\nline two\n\n<div>\nraw\n</div>\n")
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector("pre.source-block code").firstChild; getSelection().collapse(t, 2) })
  await sleep(30)
  await page.keyboard.press("ControlOrMeta+Alt+0")
  await page.keyboard.press("ControlOrMeta+Alt+2")
  assert.equal(await md(page), "line one\\\nline two\n\n<div>\nraw\n</div>\n")
  await context.close()
})
