#!/usr/bin/env node
// Phase 1 acceptance walk: a 10,000-word, 12-chapter project, driven on a 390x844
// touch phone with the CPU slowed 4x. It measures what the exit criteria name
// (editor interactive, keystroke handling, chapter switching, the tree) and checks
// that every save survives a hard reload and a server restart.
//
//   node scripts/e2e-accept.mjs [--cpu 4]
//
// Roughly two minutes. Prints one line per measurement; fails on a missed budget:
//   editor interactive < 250 ms after the editor chunk arrives (at the given throttle)
//   keydown -> input event p95 < 16 ms
//   chapter switch (tap to an editable page) p95 under 1.5 s at 4x
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startServer } from "./harness.mjs";

const cpu = Number(process.argv.includes("--cpu") ? process.argv[process.argv.indexOf("--cpu") + 1] : 4);
const kit = await createKit("e2e-accept");
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;
const q = (a, f) => { const s = [...a].sort((x, y) => x - y); return s[Math.min(s.length - 1, Math.floor(s.length * f))]; };
const ms = (n) => `${n.toFixed(0)} ms`;

// ---- a manuscript: 3 parts, 12 chapters, about 10,000 words, deterministic -------
let seed = 7;
const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
const vocab = "the tide harbour boat lamp ferry stone wall road house door window gull wind rain salt net rope pier quay light shadow letter ledger winter spring morning evening slow quiet heavy bright old narrow long cold warm walked stood watched waited carried opened closed turned heard knew said had was were came went held kept left found lost mended counted listened".split(" ");
function sentence() {
  const n = 7 + Math.floor(rnd() * 12), w = [];
  for (let i = 0; i < n; i++) w.push(vocab[Math.floor(rnd() * vocab.length)]);
  let s = w.join(" ");
  if (rnd() < 0.12) s = s.replace(/ (\w+) (\w+)$/, " *$1 $2*");
  return s[0].toUpperCase() + s.slice(1) + ".";
}
function chapterText(words) {
  const paras = [];
  let n = 0;
  while (n < words) {
    const k = 3 + Math.floor(rnd() * 4), s = [];
    for (let i = 0; i < k; i++) s.push(sentence());
    const p = s.join(" ");
    paras.push(p);
    n += p.split(/\s+/).length;
  }
  return paras.join("\n\n") + "\n";
}

const book = (await api(main, "POST", "/api/v1/projects", { title: "Acceptance Book", kind: "novel", word_goal: 80000 })).json.item;
const seeded = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
const chapters = [];
let first = true;
for (let part = 1; part <= 3; part++) {
  const pd = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: `Part ${part}`, kind: "part" })).json.item;
  for (let c = 1; c <= 4; c++) {
    const n = chapters.length + 1;
    const body = { title: `Chapter ${n}`, kind: "chapter", parent_id: pd.id, content: chapterText(833) };
    if (first) { // the seeded first page becomes chapter 1
      const r = await api(main, "PATCH", `/api/v1/docs/${seeded}`, { base_revision: 1, title: body.title, content: body.content });
      await api(main, "POST", `/api/v1/docs/${seeded}/move`, { parent_id: pd.id });
      chapters.push(seeded);
      first = false;
      ok(r.status === 200, "seed chapter 1");
    } else chapters.push((await api(main, "POST", `/api/v1/projects/${book.id}/docs`, body)).json.item.id);
  }
}
const docs = async () => (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.items.filter((d) => d.kind === "chapter");
const words = (await docs()).reduce((a, d) => a + d.word_count, 0);
console.log(`e2e-accept: ${chapters.length} chapters, ${words} words, CPU throttle ${cpu}x`);

// ---- the phone ----------------------------------------------------------------------
const phone = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true, isMobile: true,
  userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1" });
await phone.addCookies(await main.cookies());
await phone.addInitScript(() => {
  window.__mount = 0; window.__lat = []; window.__proc = [];
  new MutationObserver((_, o) => { if (document.querySelector(".scriobh-prose")) { window.__mount = performance.now(); o.disconnect(); } }).observe(document, { childList: true, subtree: true });
  let t0 = 0;
  addEventListener("keydown", () => { t0 = performance.now(); requestAnimationFrame(() => setTimeout(() => window.__lat.push(performance.now() - t0), 0)); }, true);
  addEventListener("input", () => { if (t0) window.__proc.push(performance.now() - t0); }, true);
});
const page = await phone.newPage();
kit.watch(page, "phone");
kit.main = page; // the per-step DOM and CSP scan looks at the phone page
const cdp = await phone.newCDPSession(page);
await cdp.send("Emulation.setCPUThrottlingRate", { rate: cpu });
const url = (id) => `${base()}/p/${book.slug}/d/${id}`;
const mounted = () => page.evaluate(() => ({ mount: window.__mount, nav: performance.timeOrigin, now: performance.now(), ed: (performance.getEntriesByType("resource").find((r) => /\/js\/editor\./.test(r.name)) || {}).responseEnd || 0 }));
const interactive = async () => { await page.waitForSelector(".scriobh-prose"); await page.waitForFunction(() => document.querySelector("#save-state")); };

await kit.test("phone: first load, editor interactive", async () => {
  const t0 = Date.now();
  await page.goto(url(chapters[0]), { waitUntil: "load" });
  await interactive();
  const m = await mounted();
  const sinceChunk = m.mount - m.ed;
  console.log(`      editor mounted ${ms(m.mount)} after navigation start, ${ms(sinceChunk)} after the editor bundle arrived (page load ${Date.now() - t0} ms wall)`);
  ok(sinceChunk < 250, `editor interactive ${ms(sinceChunk)} after the chunk (budget 250)`);
});

await kit.test("phone: switching chapters through the chapter sheet", async () => {
  const times = [];
  for (let i = 1; i < chapters.length; i++) {
    const want = `Chapter ${i + 1}`;
    const t0 = Date.now();
    await page.locator("#jump-open").tap();
    await page.locator("#jump-input").fill(want);
    await page.locator(`#jump-list li:not([hidden])`).first().tap();
    await page.waitForURL(new RegExp(`/d/${chapters[i]}$`));
    await interactive();
    await page.waitForFunction((t) => document.querySelector("#doc-title")?.value === t, want);
    times.push(Date.now() - t0);
  }
  console.log(`      ${times.length} switches (tap, filter, tap, page load, editor ready): p50 ${ms(q(times, 0.5))}, p95 ${ms(q(times, 0.95))}, max ${ms(Math.max(...times))}`);
  ok(q(times, 0.95) < 1500, `chapter switch p95 ${ms(q(times, 0.95))}`);
});

await kit.test("phone: the tree drawer and prev/next", async () => {
  await page.goto(url(chapters[5]), { waitUntil: "load" });
  await interactive();
  const t0 = Date.now();
  await page.locator("#tree-toggle").tap();
  await page.waitForFunction(() => document.querySelector("#rail")?.classList.contains("is-open") && document.querySelectorAll("#tree .tree-row:not([hidden])").length >= 12);
  const open = Date.now() - t0;
  const rows = await page.locator("#tree .tree-row:not([hidden])").count();
  const total = Number((await page.locator("#tree-total").innerText()).replace(/,/g, ""));
  const sum = (await docs()).reduce((a, d) => a + d.word_count, 0);
  eq(total, sum, "the project total at the foot of the tree vs the server's sum");
  // Collapse and expand a part (tap on its twist), timed.
  const c0 = Date.now();
  await page.locator("#tree .tree-row[data-kind=part]").first().locator(".twist").tap();
  await page.waitForFunction(() => document.querySelectorAll("#tree .tree-row:not([hidden])").length < 15);
  await page.locator("#tree .tree-row[data-kind=part]").first().locator(".twist").tap();
  await page.waitForFunction(() => document.querySelectorAll("#tree .tree-row:not([hidden])").length >= 15);
  console.log(`      drawer open to ${rows} rows: ${open} ms; collapse and expand a part: ${Date.now() - c0} ms; project total ${total} words (server ${sum})`);
  await page.keyboard.press("Escape");
  const n0 = Date.now();
  await page.getByRole("link", { name: /^Next/ }).tap();
  await page.waitForURL(new RegExp(`/d/${chapters[6]}$`));
  await interactive();
  console.log(`      next chapter link to an editable page: ${Date.now() - n0} ms`);
});

await kit.test("phone: typing 2,000 characters in a 10,000-word project; saved, reloaded, and after a restart", async () => {
  kit.allowed = [/ERR_CONNECTION_REFUSED/]; // the tab outlives the server it was loaded from, for a moment, on purpose
  await page.goto(url(chapters[2]), { waitUntil: "load" });
  await interactive();
  await page.locator(".scriobh-prose").tap();
  await page.keyboard.press("ControlOrMeta+End");
  const text = " " + "The tide had gone out and taken the light with it, and the boats lay over on their sides. ".repeat(23).slice(0, 1999);
  await page.keyboard.type(text, { delay: 14 });
  await waitFor(async () => (await page.locator("#save-state").getAttribute("data-state")) === "saved" && !(await page.evaluate(() => window.scriobh.write.unsaved())), "Saved", 30000);
  const { lat, proc } = await page.evaluate(() => ({ lat: window.__lat, proc: window.__proc }));
  console.log(`      ${lat.length} keystrokes at ${cpu}x CPU: keydown to input p50 ${q(proc, 0.5).toFixed(1)} ms, p95 ${q(proc, 0.95).toFixed(1)} ms; to next frame p50 ${q(lat, 0.5).toFixed(1)} ms, p95 ${q(lat, 0.95).toFixed(1)} ms, max ${Math.max(...lat).toFixed(1)} ms`);
  ok(lat.length >= 1900, "keystrokes measured: " + lat.length);
  ok(q(proc, 0.95) < 16, `keydown to input p95 ${q(proc, 0.95).toFixed(1)} ms (budget 16)`);
  const mine = await page.evaluate(() => window.scriobh.write.editor.getMarkdown());
  const body = async () => (await api(main, "GET", `/api/v1/docs/${chapters[2]}`)).json.item.content;
  eq(await body(), mine, "the server body vs the editor body (bytes)");
  // A hard reload.
  await page.reload({ waitUntil: "load" });
  await interactive();
  eq(await page.evaluate(() => window.scriobh.write.editor.getMarkdown()), mine, "text after a hard reload");
  // A server restart on the same data directory.
  await kit.srv.stop();
  kit.srv = await startServer(kit.bin, kit.dataDir);
  await phone.addCookies(await main.cookies());
  await main.addCookies([]);
  await page.goto(url(chapters[2]), { waitUntil: "load" });
  await interactive();
  eq(await page.evaluate(() => window.scriobh.write.editor.getMarkdown()), mine, "text after a server restart");
  ok(kit.srv.base, "restarted");
});

await kit.test("phone: every chapter opens, shows its words, and the totals add up", async () => {
  const list = await docs();
  const sum = list.reduce((a, d) => a + d.word_count, 0);
  ok(list.length === 12, "12 chapters: " + list.length);
  ok(sum > words + 300 && sum < words + 600, `the 2,000 typed characters add roughly 400 words to ${words}: ${sum}`);
  const ctx = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  await ctx.addCookies(await main.cookies());
  const d = await ctx.newPage();
  kit.watch(d, "desktop");
  await d.goto(`${base()}/`);
  ok((await d.locator("body").innerText()).includes("Acceptance Book"), "the library shows the project");
  await d.goto(`${base()}/p/${book.slug}`);
  await d.waitForSelector(".scriobh-prose");
  const t0 = Date.now();
  const shown = await d.evaluate(() => [...document.querySelectorAll("#tree .tree-row[data-kind=chapter] .tree-count")].map((e) => Number(e.textContent.replace(/[^0-9]/g, "") || 0)));
  eq(shown.reduce((a, b) => a + b, 0), sum, "chapter counts shown in the tree vs the server");
  console.log(`      desktop project open (page, tree, editor): ${Date.now() - t0} ms after load; ${shown.length} chapters, ${sum} words`);
  await kit.csp(d, "desktop");
  await ctx.close();
});

await phone.close();
await kit.finish("e2e-accept");
