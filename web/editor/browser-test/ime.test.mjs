// Composition (IME) checks in Chromium.
//
// What this covers: the real composition path of Chromium driven through the
// DevTools protocol (Input.imeSetComposition / Input.insertText), which makes the
// browser itself fire compositionstart/update/end and beforeinput/input, plus a
// hand-built event sequence that mutates the DOM the way a browser does.
//
// What it cannot cover (see docs/EDITOR-SPIKE.md for the manual device checklist):
// iOS Safari autocorrect and dictation, Android Gboard composition and suggestion
// strip, long-press accents, selection handles, and the virtual keyboard resizing
// the viewport. Those need a phone.

import assert from "node:assert/strict"
import {suite, md, sleep, mount} from "./harness.mjs"

const {bt, fresh} = suite()

// Log composition-related events on the editor DOM.
const watch = page => page.evaluate(() => {
  window.__ev = []
  for (const type of ["compositionstart", "compositionupdate", "compositionend", "beforeinput", "input"]) {
    h.ed.view.dom.addEventListener(type, e => window.__ev.push(type + (e.inputType ? ":" + e.inputType : "") + (e.data != null ? "=" + e.data : "")))
  }
})
const events = page => page.evaluate(() => window.__ev)

async function cdp(context, page) { return context.newCDPSession(page) }

// One composition: successive preedit strings, then the committed text.
async function compose(client, steps, commit) {
  for (const text of steps) await client.send("Input.imeSetComposition", {text, selectionStart: text.length, selectionEnd: text.length})
  await client.send("Input.insertText", {text: commit})
}

bt("a Japanese-style composition inserts the committed text once, in place", async () => {
  const {page, context} = await fresh("before  after\n")
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector("p").firstChild; getSelection().collapse(t, 7) })
  await sleep(30)
  await watch(page)
  const client = await cdp(context, page)
  await compose(client, ["n", "に", "にほ", "にほん"], "日本")
  await sleep(50)
  assert.equal(await md(page), "before 日本 after\n")
  const ev = await events(page)
  assert.ok(ev.some(e => e.startsWith("compositionstart")), ev.join(" "))
  assert.ok(ev.some(e => e.startsWith("compositionupdate")), ev.join(" "))
  assert.ok(ev.some(e => e.startsWith("compositionend")), ev.join(" "))
  assert.equal(await page.evaluate(() => h.ed.view.composing), false)
  await context.close()
})

bt("composition inside bold text stays bold; composition at the start and end of a paragraph", async () => {
  const {page, context} = await fresh("plain **bold** plain\n\nsecond\n")
  const client = await cdp(context, page)
  // Inside the bold run.
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector("strong").firstChild; getSelection().collapse(t, 2) })
  await sleep(30)
  await compose(client, ["あ", "あい"], "愛")
  await sleep(50)
  assert.equal(await md(page), "plain **bo愛ld** plain\n\nsecond\n")
  // Start of the second paragraph.
  await page.evaluate(() => { const t = h.ed.view.dom.querySelectorAll("p")[1].firstChild; getSelection().collapse(t, 0) })
  await sleep(30)
  await compose(client, ["ㅎ", "하", "한"], "한")
  await sleep(50)
  // End of the second paragraph.
  await page.evaluate(() => { const t = h.ed.view.dom.querySelectorAll("p")[1].lastChild; getSelection().collapse(t, t.textContent.length) })
  await sleep(30)
  await compose(client, ["k", "ke", "kei"], "Ké")
  await sleep(50)
  assert.equal(await md(page), "plain **bo愛ld** plain\n\n한secondKé\n")
  await context.close()
})

bt("composition inside headings, list items and private notes", async () => {
  const {page, context} = await fresh("# head\n\n- item\n\nx %%note%% y\n")
  const client = await cdp(context, page)
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector("h1").firstChild; getSelection().collapse(t, 4) })
  await sleep(30)
  await compose(client, ["#", "# "], "# ")   // committing "# " mid-heading is literal text, not a rule
  await sleep(50)
  assert.match(await md(page), /^# head# \n\n- item\n/)
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector("li p").firstChild; getSelection().collapse(t, 4) })
  await sleep(30)
  await compose(client, ["é"], "é")
  await sleep(50)
  assert.match(await md(page), /- itemé\n/)
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector(".private-note").firstChild; getSelection().collapse(t, 2) })
  await sleep(30)
  await compose(client, ["ü"], "ü")
  await sleep(50)
  assert.match(await md(page), /%%noüte%%/)
  await context.close()
})

bt("a cancelled composition leaves the text alone", async () => {
  const {page, context} = await fresh("keep this\n")
  const client = await cdp(context, page)
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector("p").firstChild; getSelection().collapse(t, 9) })
  await sleep(30)
  await client.send("Input.imeSetComposition", {text: "あ", selectionStart: 1, selectionEnd: 1})
  await client.send("Input.imeSetComposition", {text: "", selectionStart: 0, selectionEnd: 0})
  await sleep(50)
  assert.equal(await md(page), "keep this\n")
  await context.close()
})

bt("blur in the middle of a composition commits it once", async () => {
  const {page, context} = await fresh("keep this\n")
  const client = await cdp(context, page)
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector("p").firstChild; getSelection().collapse(t, 9) })
  await sleep(30)
  await client.send("Input.imeSetComposition", {text: "あ", selectionStart: 1, selectionEnd: 1})
  await page.evaluate(() => h.ed.view.dom.blur())
  await sleep(100)
  assert.equal(await md(page), "keep thisあ\n") // Chromium commits the preedit text on blur
  assert.equal(await page.evaluate(() => h.ed.view.composing), false)
  await context.close()
})

bt("setMarkdown from outside is refused while a composition is in flight", async () => {
  const {page, context} = await fresh("keep this\n")
  const client = await cdp(context, page)
  await page.evaluate(() => { const t = h.ed.view.dom.querySelector("p").firstChild; getSelection().collapse(t, 9) })
  await sleep(30)
  await client.send("Input.imeSetComposition", {text: "あい", selectionStart: 2, selectionEnd: 2})
  await sleep(30)
  assert.equal(await page.evaluate(() => h.ed.view.composing), true)
  assert.equal(await page.evaluate(() => h.ed.setMarkdown("server text\n")), false)
  await client.send("Input.insertText", {text: "愛"})
  await sleep(50)
  assert.equal(await md(page), "keep this愛\n")
  await context.close()
})

bt("a composition does not call onChange until idle, and the committed text is what is reported", async () => {
  const {page, context} = await fresh("")
  const client = await cdp(context, page)
  await compose(client, ["か", "かん", "かんじ"], "漢字")
  assert.equal(await page.evaluate(() => h.changes.length), 0)
  await sleep(900)
  const changes = await page.evaluate(() => h.changes.map(c => c.md))
  assert.deepEqual(changes.at(-1), "漢字\n")
  await context.close()
})

bt("hand-built composition events with DOM mutation, as a browser performs them", async () => {
  const {page, context} = await fresh("start end\n")
  await page.evaluate(() => {
    const dom = h.ed.view.dom
    const text = dom.querySelector("p").firstChild
    getSelection().collapse(text, 6)
    const fire = (type, init) => dom.dispatchEvent(new CompositionEvent(type, {bubbles: true, data: init.data}))
    fire("compositionstart", {data: ""})
    text.data = "start " + "あ" + "end"
    getSelection().collapse(text, 7)
    fire("compositionupdate", {data: "あ"})
    text.data = "start " + "あい" + "end"
    getSelection().collapse(text, 8)
    fire("compositionupdate", {data: "あい"})
    text.data = "start " + "愛" + "end"
    getSelection().collapse(text, 7)
    fire("compositionupdate", {data: "愛"})
    fire("compositionend", {data: "愛"})
  })
  await sleep(100)
  assert.equal(await md(page), "start 愛end\n")
  await context.close()
})

bt("typing keeps the caret and the text right when Markdown-significant characters are typed one by one", async () => {
  const {page, context} = await fresh()
  await page.keyboard.type("a*b_c [d] <e> & 1. # > - `f` ~g~ %h% \\i")
  assert.equal(await md(page), "a\\*b_c \\[d\\] \\<e> & 1. # > - `f` \\~g\\~ %h% \\\\i\n")
  await context.close()
})
