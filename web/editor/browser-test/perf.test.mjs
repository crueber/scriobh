// Keystroke latency on a 20,000-word document, measured in Chromium with real key
// events (CDP Input.dispatchKeyEvent through Playwright's keyboard).
//
// Two numbers per keystroke, reported separately because they answer different
// questions:
//
//   work     from the input event (capture, before ProseMirror sees it) to the end
//            of view.updateState for that keystroke: ProseMirror reading the DOM
//            change, applying the transaction, and updating the DOM. The most
//            direct measure of what the editor itself costs.
//   handled  keydown (capture) -> a setTimeout(0) queued from the input event of
//            that key. The browser inserts the character in the input phase, and
//            ProseMirror's MutationObserver and transaction run before the next
//            task, so the timeout fires after the editor has applied the text
//            (the test checks every keystroke reached the document). This is the
//            main-thread cost of the keystroke at the editor level and the
//            figure compared with the 16 ms target.
//   painted  keydown -> the setTimeout(0) queued from the first requestAnimationFrame
//            after it. Includes waiting for the frame (up to one 16.7 ms frame
//            in headless), so it is an upper bound with that noise in it.
//
// Also reported: parse + mount time, serialization time (which happens off the
// keystroke path, at idle) and the same keystroke numbers with the CPU throttled
// 4x, a rough stand-in for a mid-range phone.
//
// Run: npm run test:browser   (prints a table; fails only past a loose guard)

import assert from "node:assert/strict"
import {suite, sleep} from "./harness.mjs"

const {bt, fresh, state} = suite()

function makeDoc(words) {
  const vocab = "the quiet house stood beyond river where morning light fell across slate roofs and she remembered father saying that every road leads home eventually even those that circle back through rain and silence".split(" ")
  const out = []
  let count = 0, p = 0
  while (count < words) {
    p++
    if (p % 25 === 1) { out.push(`## Chapter ${p}`); out.push(""); continue }
    if (p % 40 === 0) { out.push("- first point in a list", "- second point with *emphasis*", "- third point", ""); count += 12; continue }
    const n = 40 + (p % 25)
    const para = []
    for (let i = 0; i < n; i++) {
      let w = vocab[(p * 7 + i * 3) % vocab.length]
      if (i === 5) w = `*${w}*`
      if (i === 17 && p % 3 === 0) w = `**${w}**`
      if (i === 29 && p % 5 === 0) w = `[[${w}]]`
      para.push(w)
    }
    out.push(para.join(" "), "")
    count += n
  }
  return out.join("\n")
}

const pct = (arr, q) => { const s = [...arr].sort((a, b) => a - b); return s[Math.min(s.length - 1, Math.floor(q * s.length))] }
const fmt = x => x.toFixed(1).padStart(6)

async function measure(page, keys) {
  const before = await page.evaluate(() => h.ed.view.state.doc.textContent.length)
  await page.evaluate(() => {
    window.__samples = []
    window.__t0 = 0
    window.__onKey = () => { window.__t0 = performance.now() }
    // The character is inserted by the browser in the input phase of the same key
    // event; queue the timeout from there so it cannot fire before the insertion.
    window.__tEnd = 0
    const view = h.ed.view, orig = view.updateState.bind(view)
    window.__restore = () => { view.updateState = orig }
    // The end of ProseMirror's work for the keystroke: state applied, DOM updated.
    view.updateState = state => { const r = orig(state); window.__tEnd = performance.now(); return r }
    window.__onInput = () => {
      const t0 = window.__t0, tIn = performance.now()
      setTimeout(() => {
        const t1 = performance.now(), work = window.__tEnd - tIn
        requestAnimationFrame(() => setTimeout(() => {
          window.__samples.push({work, handled: t1 - t0, painted: performance.now() - t0})
        }, 0))
      }, 0)
    }
    document.addEventListener("keydown", window.__onKey, true)
    document.addEventListener("input", window.__onInput, true)
  })
  const letters = "etaoinshrdlucmfwypvbgkjqxz"
  for (let i = 0; i < keys; i++) {
    await page.keyboard.press(i % 7 === 6 ? "Space" : letters[i % letters.length])
    await sleep(25)
  }
  await sleep(200)
  const samples = await page.evaluate(() => {
    document.removeEventListener("keydown", window.__onKey, true)
    document.removeEventListener("input", window.__onInput, true)
    window.__restore()
    return window.__samples
  })
  assert.equal(samples.length, keys, "one input event per keystroke")
  const after = await page.evaluate(() => h.ed.view.state.doc.textContent.length)
  assert.equal(after - before, keys, "every keystroke reached the document")
  assert.ok(samples.every(s => s.work >= 0), "updateState ran after the input event for every keystroke")
  return {
    work: samples.map(s => s.work),
    handled: samples.map(s => s.handled),
    painted: samples.map(s => s.painted)
  }
}

bt("typing on a 20,000-word document: keystroke latency", async t => {
  const markdown = makeDoc(20000)
  const words = markdown.split(/\s+/).filter(w => /\w/.test(w)).length
  const {page, context} = await fresh("", null, {viewport: {width: 900, height: 900}})
  const timings = await page.evaluate(md => {
    const t0 = performance.now()
    h.mount(md)
    const mounted = performance.now() - t0
    const t1 = performance.now()
    const dom = h.ed.view.dom
    void dom.offsetHeight // force layout
    return {mounted, layout: performance.now() - t1, paragraphs: dom.children.length, words: h.ed.wordCount()}
  }, markdown)

  // Put the caret at the end of a paragraph near the middle, as a writer would.
  await page.evaluate(() => {
    const ps = [...h.ed.view.dom.querySelectorAll("p")]
    const p = ps[Math.floor(ps.length / 2)]
    p.scrollIntoView({block: "center"})
    h.ed.focus()
    const last = p.lastChild
    getSelection().collapse(last, last.textContent ? last.textContent.length : 0)
  })
  await sleep(100)

  const plain = await measure(page, 300)

  // Serialization is off the keystroke path (700 ms idle); how long does it block then?
  const ser = await page.evaluate(() => {
    const runs = []
    for (let i = 0; i < 5; i++) {
      h.ed.view.dispatch(h.ed.view.state.tr.insertText("x", 1))
      const t = performance.now(); h.ed.getMarkdown(); runs.push(performance.now() - t)
    }
    return runs
  })

  const client = await context.newCDPSession(page)
  await client.send("Emulation.setCPUThrottlingRate", {rate: 4})
  await page.evaluate(() => { const p = h.ed.view.dom.querySelectorAll("p")[100]; h.ed.focus(); const l = p.lastChild; getSelection().collapse(l, l.textContent.length) })
  await sleep(100)
  const throttled = await measure(page, 150)
  await client.send("Emulation.setCPUThrottlingRate", {rate: 1})

  const rows = [
    ["editor work", plain.work], ["handled", plain.handled], ["painted", plain.painted],
    ["editor work (4x CPU throttle)", throttled.work], ["handled (4x CPU throttle)", throttled.handled], ["painted (4x CPU throttle)", throttled.painted]
  ]
  const lines = [
    `document: ${timings.words} words, ${timings.paragraphs} top-level blocks`,
    `parse + build + render: ${timings.mounted.toFixed(0)} ms (+${timings.layout.toFixed(0)} ms first layout)`,
    `serialize at idle: ${ser.map(x => x.toFixed(0)).join(", ")} ms (5 runs)`,
    "keystroke latency in ms            p50    p95    max",
    ...rows.map(([name, arr]) => `${name.padEnd(30)} ${fmt(pct(arr, .5))} ${fmt(pct(arr, .95))} ${fmt(Math.max(...arr))}`)
  ]
  const worst = (name, arr) => `${name}: slowest at keystroke #${arr.map((v, i) => [v, i]).sort((a, b) => b[0] - a[0]).slice(0, 3).map(([v, i]) => i + " (" + v.toFixed(0) + " ms)").join(", #")}`
  lines.push(worst("editor work", plain.work), worst("handled", plain.handled))
  for (const l of lines) t.diagnostic(l)
  console.log("\n" + lines.join("\n"))

  // A loose regression guard, not the target: the target (p95 < 16 ms) is reported above.
  assert.ok(pct(plain.work, .95) < 16, "p95 editor work per keystroke is above the 16 ms target")
  await context.close()
})
