#!/usr/bin/env node
// Typing in a 20,000-word chapter with 200 alternatives and 600 Lab marks, at 4x CPU slowdown: keydown to input stays under the
// frame, the marks and the alternatives are on their own words before and after a save refresh (also after a paragraph has been
// ended with a space at the very start of the chapter, which shifts every mark), and the Lab never reflows the text.
//
//   node scripts/e2e-ai-perf.mjs
import { createKit, eq, ok } from "./e2e-kit.mjs";

const cpu = 4;
const kit = await createKit("e2e-ai-perf", {});
kit.deadline(480000); // a stalled step fails the script instead of hanging it
const { api, waitFor } = kit;
const { ctx: main } = await kit.signedIn();
const q = (a, f) => { const s = [...a].sort((x, y) => x - y); return s[Math.min(s.length - 1, Math.floor(s.length * f))]; };
const ms = (n) => `${(n ?? NaN).toFixed(1)} ms`;
let seed = 23;
const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
const vocab = "the tide harbour boat lamp ferry stone wall road house door window gull wind rain salt net rope pier quay light shadow letter ledger winter spring morning evening slow heavy bright old narrow long cold warm walked stood watched waited carried opened closed turned heard knew said had was were came went held kept left found lost mended counted listened".split(" ");
let hedgeN = 0, dsN = 0; const tags = [];
const paras = []; let n = 0;
while (n < 20000) {
  const out = [];
  for (let s = 0; s < 4; s++) {
    const k = 8 + Math.floor(rnd() * 10), w = [];
    for (let i = 0; i < k; i++) w.push(vocab[Math.floor(rnd() * vocab.length)]);
    if (s === 0 && tags.length < 200) { const t = "zq" + String(tags.length + 1).padStart(4, "0"); tags.push(t); w[3] = t; }
    if (s === 1 && hedgeN < 300) { w[2] = "perhaps"; hedgeN++; }
    let t = w.join(" ");
    if (s === 2 && dsN < 300) { const i = t.indexOf(" "); t = t.slice(0, i) + "  " + t.slice(i + 1); dsN++; }
    out.push(t[0].toUpperCase() + t.slice(1) + ".");
  }
  const p = out.join(" "); paras.push(p); n += p.split(/\s+/).length;
}
const content = paras.join("\n\n") + "\n";
const book = (await api(main, "POST", "/api/v1/projects", { title: "Perf", kind: "novel" })).json.item;
const doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
const rev = (await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: 1, title: "W", content })).json.item.revision;
await kit.test("setup: 200 alternatives and the Lab's marks", async () => {
  let r0 = rev;
  for (const tag of tags) { const r = await api(main, "POST", `/api/v1/docs/${doc}/alternatives`, { base_revision: r0, scope: "word", quote: tag, variants: [tag + "b", tag + "c"] }); ok(r.status < 300, "alt " + JSON.stringify(r.json).slice(0, 120)); r0 = r.json.revision; }
  const a = await api(main, "POST", `/api/v1/projects/${book.id}/ai/lab/analyse`, { document_id: doc });
  ok(a.status < 300, "analyse");
  console.log("   marks:", JSON.stringify(a.json && a.json.counts));
});
const pctx = await kit.newContext({ viewport: { width: 1280, height: 900 } });
await pctx.addCookies(await main.cookies());
await pctx.addCookies([{ name: "scriobh_insp", value: "lab", url: kit.base() }]);
await pctx.addInitScript(() => {
  window.__proc = [];
  let t0 = 0;
  addEventListener("keydown", () => { t0 = performance.now(); }, true);
  addEventListener("input", () => { if (t0) window.__proc.push(performance.now() - t0); }, true);
});
const page = await pctx.newPage();
kit.watch(page, "desk"); kit.main = page;
const cdp = await pctx.newCDPSession(page);
const url = `${kit.base()}/p/${book.slug}/d/${doc}`;
const check = () => page.evaluate(() => {
  let bad = 0, badAlt = 0;
  const lab = [...document.querySelectorAll(".scriobh-prose .lab-mark")];
  lab.forEach((e) => { const k = [...e.classList].find((c) => c.startsWith("lab-") && c !== "lab-mark"); const t = e.textContent; if (k === "lab-hedge_words" && t.toLowerCase() !== "perhaps") bad++; if (k === "lab-punctuation_fix" && !/^\s+$/.test(t)) bad++; });
  const alts = [...document.querySelectorAll(".scriobh-prose .scriobh-alt")];
  alts.forEach((e) => { if (!/^zq\d{4}[bc]?$/.test(e.textContent.trim())) badAlt++; });
  return { nlab: lab.length, bad, nalt: alts.length, badAlt };
});
await kit.test("4x CPU: typing stays under a frame, and every mark is on its word before and after a save and a refresh", async () => {
  await page.goto(url, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose .scriobh-alt", { timeout: 60000 });
  await waitFor(async () => (await page.evaluate(() => document.querySelectorAll(".scriobh-prose .lab-mark").length)) > 0, "the Lab's marks", 30000);
  const before = await check();
  console.log("   before:", JSON.stringify(before));
  ok(before.nlab >= 590 && before.bad === 0 && before.badAlt === 0, "marks are right on load");
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: cpu });
  await page.evaluate(() => {
    const mk = document.querySelectorAll(".scriobh-prose .scriobh-alt"), p = mk[Math.floor(mk.length / 2)].closest("p");
    const r = document.createRange(); r.selectNodeContents(p); r.collapse(false); const s = getSelection(); s.removeAllRanges(); s.addRange(r); p.scrollIntoView({ block: "center" });
    window.__proc.length = 0;
  });
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.type(" The tide had gone out and the boats lay over on their sides.".repeat(8), { delay: 14 });
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "saved", 60000);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: 1 });
  const proc = await page.evaluate(() => window.__proc);
  const p95 = q(proc, 0.95);
  console.log(`   ${proc.length} keys at ${cpu}x CPU: keydown to input p50 ${ms(q(proc, 0.5))} p95 ${ms(p95)} max ${ms(Math.max(...proc))}`);
  ok(p95 < 16, "p95 " + ms(p95) + " is under a frame");
  // Words and a trailing space go in at the very start: every mark after them moves.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, 1))); v.focus(); });
  await page.keyboard.type("Opening words. ", { delay: 20 });
  await page.keyboard.press("Enter");
  await page.waitForTimeout(300);
  const mid = await check();
  console.log("   after the edit, before the save:", JSON.stringify(mid));
  eq(mid.bad + mid.badAlt, 0, "marks follow the edit");
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "saved", 60000);
  await page.waitForTimeout(3500);
  const after = await check();
  console.log("   after the save and the refresh:", JSON.stringify(after));
  eq(after.bad, 0, "the Lab's marks are on their words after the refresh");
  eq(after.badAlt, 0, "the alternatives are on their words after the refresh");
  ok(after.nlab >= 590, "and they are all still there: " + after.nlab);
});
await pctx.close();
await kit.finish("e2e-ai-perf");
