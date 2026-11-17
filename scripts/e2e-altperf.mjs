#!/usr/bin/env node
// The alternatives budget: a 20,000-word chapter holding 200 alternatives, on a 390x844 touch phone with the CPU slowed 4x.
// Typing next to the marks stays under 16 ms (keydown to input, p95) and cycling a wording shows in the text in under 100 ms.
//
// A second chapter holds 1,000 alternatives (the server is started with room for them) and typing in it keeps the same budget:
// what a key costs does not grow with the number of marks.
//
//   node scripts/e2e-altperf.mjs [--cpu 4]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const cpu = Number(process.argv.includes("--cpu") ? process.argv[process.argv.indexOf("--cpu") + 1] : 4);
const kit = await createKit("e2e-altperf", { env: { SCRIOBH_MAX_ALTS_PER_DOC: "1000" } });
const { api, waitFor } = kit;
const { ctx: main } = await kit.signedIn();
const q = (a, f) => { const s = [...a].sort((x, y) => x - y); return s[Math.min(s.length - 1, Math.floor(s.length * f))]; };
const ms = (n) => `${n.toFixed(1)} ms`;

let seed = 23;
const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
const vocab = "the tide harbour boat lamp ferry stone wall road house door window gull wind rain salt net rope pier quay light shadow letter ledger winter spring morning evening slow quiet heavy bright old narrow long cold warm walked stood watched waited carried opened closed turned heard knew said had was were came went held kept left found lost mended counted listened".split(" ");
// Every second paragraph carries a word that is its own (zq0001, zq0002, ...) so each of the 200 alternatives anchors once.
function para(tags) {
  const out = [];
  for (let s = 0; s < 4; s++) {
    const n = 8 + Math.floor(rnd() * 10), w = [];
    for (let i = 0; i < n; i++) w.push(vocab[Math.floor(rnd() * vocab.length)]);
    if (tags[s]) w[3] = tags[s];
    const t = w.join(" ");
    out.push(t[0].toUpperCase() + t.slice(1) + ".");
  }
  return out.join(" ");
}
// A chapter of at least 20,000 words in which `perPara` sentences of every paragraph carry a word of their own.
function chapter(count, perPara) {
  const paras = [], tags = [];
  let n = 0;
  while (n < 20000 || tags.length < count) {
    const mine = [];
    for (let k = 0; k < 4; k++) if (k < perPara && tags.length < count) { const t = "zq" + String(tags.length + 1).padStart(4, "0"); tags.push(t); mine[k] = t; }
    const p = para(mine);
    paras.push(p);
    n += p.split(/\s+/).length;
  }
  return { content: paras.join("\n\n") + "\n", tags };
}
const small = chapter(200, 1), big = chapter(1000, 4);
const tags = small.tags, content = small.content;

const book = (await api(main, "POST", "/api/v1/projects", { title: "Alt Perf", kind: "novel" })).json.item;
const doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
let rev = (await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: 1, title: "The Whole Winter", content })).json.item.revision;
let made = 0;
await kit.test("setup: 200 alternatives in a 20,000-word chapter", async () => {
  for (const tag of tags) {
    const r = await api(main, "POST", `/api/v1/docs/${doc}/alternatives`, { base_revision: rev, scope: "word", quote: tag, variants: [tag + "b", tag + "c"] });
    ok(r.status < 300, `alternative ${made + 1}: ${r.status} ${JSON.stringify(r.json).slice(0, 160)}`);
    rev = r.json.revision;
    made++;
  }
  const d = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  console.log(`e2e-altperf: ${d.word_count} words, ${made} alternatives, comment line ${(d.content.split("\n").filter((l) => l.startsWith("<!-- scriobh:alts")).pop() || "").length} bytes, CPU throttle ${cpu}x`);
  ok(d.word_count >= 20000, "words: " + d.word_count);
});

const phone = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true, isMobile: true,
  userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1" });
await phone.addCookies(await main.cookies());
await phone.addInitScript(() => {
  window.__proc = []; window.__cycle = [];
  let t0 = 0;
  addEventListener("keydown", (e) => { t0 = performance.now(); if (e.altKey && e.shiftKey && (e.code === "KeyN" || e.code === "KeyP" || e.code === "KeyO")) window.__ck = t0; }, true);
  addEventListener("input", () => { if (t0) window.__proc.push(performance.now() - t0); }, true);
});
const page = await phone.newPage();
kit.watch(page, "phone");
kit.main = page;
const cdp = await phone.newCDPSession(page);
await cdp.send("Emulation.setCPUThrottlingRate", { rate: cpu });
const url = `${kit.base()}/p/${book.slug}/d/${doc}`;

await kit.test("phone: the chapter opens and the marks are drawn", async () => {
  await page.goto(url, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose");
  await page.waitForSelector(".scriobh-prose .scriobh-alt", { timeout: 40000 });
  const marks = await page.evaluate(() => window.scriobh.alts.items.length);
  eq(marks, 200, "the editor knows all 200");
  await waitFor(async () => (await page.locator(".scriobh-prose .scriobh-alt").count()) > 0, "marks drawn", 20000);
});

await kit.test("phone: typing among the marks stays under 16 ms (p95)", async () => {
  // Put the caret at the end of the paragraph holding the 100th alternative and type.
  await page.evaluate(() => {
    const ed = document.querySelector(".scriobh-prose");
    const mk = ed.querySelectorAll(".scriobh-alt");
    const p = mk[Math.floor(mk.length / 2)].closest("p");
    const r = document.createRange();
    r.selectNodeContents(p); r.collapse(false);
    const s = getSelection(); s.removeAllRanges(); s.addRange(r);
    p.scrollIntoView({ block: "center" });
    window.__proc.length = 0;
  });
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.type(" The tide had gone out and the boats lay over on their sides.".repeat(8), { delay: 14 });
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "saved", 40000);
  const proc = await page.evaluate(() => window.__proc);
  console.log(`      ${proc.length} keystrokes: keydown to input p50 ${ms(q(proc, 0.5))}, p95 ${ms(q(proc, 0.95))}, max ${ms(Math.max(...proc))}`);
  ok(proc.length >= 400, "keystrokes measured: " + proc.length);
  ok(q(proc, 0.95) < 16, `keydown to input p95 ${ms(q(proc, 0.95))} (budget 16)`);
});

await kit.test("phone: cycling a wording shows in the text in under 100 ms", async () => {
  const times = [];
  for (let i = 0; i < 12; i++) {
    // Select inside a mark, then cycle; the clock runs from the key to the text changing in the editor.
    await page.evaluate((i) => {
      const ed = document.querySelector(".scriobh-prose");
      const mk = ed.querySelectorAll(".scriobh-alt");
      const m = mk[20 + i * 7];
      m.scrollIntoView({ block: "center" });
      const v = window.scriobh.write.editor.view;
      const at = v.posAtDOM(m, 0);
      v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, at + 1)));
      v.focus();
      window.__seen = new Promise((res) => {
        const o = new MutationObserver(() => { if (window.__ck) { res(performance.now() - window.__ck); o.disconnect(); } });
        o.observe(ed, { childList: true, subtree: true, characterData: true });
      });
      window.__ck = 0;
    }, i);
    await new Promise((r) => setTimeout(r, 150));
    await page.keyboard.press("Alt+Shift+KeyN");
    const t = await page.evaluate(() => Promise.race([window.__seen, new Promise((r) => setTimeout(() => r(-1), 4000))]));
    ok(t >= 0, "the text changed after the cycle key");
    times.push(t);
    await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "saved", 20000);
  }
  console.log(`      ${times.length} cycles: p50 ${ms(q(times, 0.5))}, p95 ${ms(q(times, 0.95))}, max ${ms(Math.max(...times))}`);
  ok(q(times, 0.95) < 100, `cycle p95 ${ms(q(times, 0.95))} (budget 100)`);
});

let bigDoc = "";
await kit.test("setup: 1,000 alternatives in a second 20,000-word chapter", async () => {
  bigDoc = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "The Long Winter", kind: "chapter", content: big.content })).json.item.id;
  let r0 = (await api(main, "GET", `/api/v1/docs/${bigDoc}`)).json.item.revision, made2 = 0;
  for (const tag of big.tags) {
    const r = await api(main, "POST", `/api/v1/docs/${bigDoc}/alternatives`, { base_revision: r0, scope: "word", quote: tag, variants: [tag + "b", tag + "c"] });
    ok(r.status < 300, `alternative ${made2 + 1}: ${r.status} ${JSON.stringify(r.json).slice(0, 160)}`);
    r0 = r.json.revision; made2++;
  }
  eq(made2, 1000, "all made");
});

await kit.test("phone: 1,000 alternatives: typing among the marks is still under 16 ms (p95)", async () => {
  await page.goto("about:blank");
  await page.goto(`${kit.base()}/p/${book.slug}/d/${bigDoc}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose .scriobh-alt", { timeout: 60000 });
  await waitFor(async () => (await page.evaluate(() => window.scriobh.alts.items.length)) === 1000, "the editor to know all 1,000", 30000);
  await waitFor(async () => (await page.locator(".scriobh-prose .scriobh-alt").count()) > 0, "marks drawn", 20000);
  await page.evaluate(() => {
    const ed = document.querySelector(".scriobh-prose");
    const mk = ed.querySelectorAll(".scriobh-alt");
    const p = mk[Math.floor(mk.length / 2)].closest("p");
    const r = document.createRange();
    r.selectNodeContents(p); r.collapse(false);
    const s = getSelection(); s.removeAllRanges(); s.addRange(r);
    p.scrollIntoView({ block: "center" });
    window.__proc.length = 0;
    // What the editor spends on each edit (its state update, the decorations included), which the input event does not see.
    const v = window.scriobh.write.editor.view, orig = v.updateState.bind(v);
    window.__upd = [];
    v.updateState = (st) => { const t = performance.now(); orig(st); window.__upd.push(performance.now() - t); };
  });
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.type(" The tide had gone out and the boats lay over on their sides.".repeat(8), { delay: 14 });
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "saved", 60000);
  const proc = await page.evaluate(() => window.__proc), upd = (await page.evaluate(() => window.__upd)).filter((x) => x > 0);
  console.log(`      ${upd.length} edits with 1,000 alternatives: the editor's state update p50 ${ms(q(upd, 0.5))}, p95 ${ms(q(upd, 0.95))}, max ${ms(Math.max(...upd))}`);
  ok(upd.length >= 400, "edits measured: " + upd.length);
  ok(q(upd, 0.95) < 16, `the state update of an edit p95 ${ms(q(upd, 0.95))} with 1,000 alternatives (budget 16)`);
  console.log(`      ${proc.length} keystrokes with 1,000 alternatives: keydown to input p50 ${ms(q(proc, 0.5))}, p95 ${ms(q(proc, 0.95))}, max ${ms(Math.max(...proc))}`);
  ok(proc.length >= 400, "keystrokes measured: " + proc.length);
  ok(q(proc, 0.95) < 16, `keydown to input p95 ${ms(q(proc, 0.95))} with 1,000 alternatives (budget 16)`);
});

await phone.close();
await kit.finish("e2e-altperf");
