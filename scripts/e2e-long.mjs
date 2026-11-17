#!/usr/bin/env node
// Long-document check: a single 20,000-word chapter on a 390x844 touch phone with
// the CPU slowed 4x (the exit criterion "a 20,000-word chapter remains smooth").
// It types at the start, the middle and the end of the chapter, then checks the text
// is saved and survives a hard reload.
//
//   node scripts/e2e-long.mjs [--cpu 4]
//
// Budgets (at the given throttle): editor interactive < 400 ms after the editor
// bundle arrives; keydown -> input p95 < 16 ms at every position; the typed text
// reaches the server byte for byte.
import { createKit, eq, ok } from "./e2e-kit.mjs";

const cpu = Number(process.argv.includes("--cpu") ? process.argv[process.argv.indexOf("--cpu") + 1] : 4);
const kit = await createKit("e2e-long");
const { api, waitFor } = kit;
const { ctx: main } = await kit.signedIn();
const q = (a, f) => { const s = [...a].sort((x, y) => x - y); return s[Math.min(s.length - 1, Math.floor(s.length * f))]; };
const ms = (n) => `${n.toFixed(0)} ms`;

let seed = 11;
const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
const vocab = "the tide harbour boat lamp ferry stone wall road house door window gull wind rain salt net rope pier quay light shadow letter ledger winter spring morning evening slow quiet heavy bright old narrow long cold warm walked stood watched waited carried opened closed turned heard knew said had was were came went held kept left found lost mended counted listened".split(" ");
function para() {
  const out = [];
  for (let s = 0; s < 4; s++) {
    const n = 8 + Math.floor(rnd() * 10), w = [];
    for (let i = 0; i < n; i++) w.push(vocab[Math.floor(rnd() * vocab.length)]);
    let t = w.join(" ");
    if (rnd() < 0.1) t = t.replace(/ (\w+) (\w+)$/, " **$1 $2**");
    out.push(t[0].toUpperCase() + t.slice(1) + ".");
  }
  return out.join(" ");
}
const paras = [];
let n = 0;
while (n < 20000) { const p = para(); paras.push(p); n += p.split(/\s+/).length; }
const content = paras.join("\n\n") + "\n";

const book = (await api(main, "POST", "/api/v1/projects", { title: "Long Chapter", kind: "novel" })).json.item;
const seeded = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
const put = await api(main, "PATCH", `/api/v1/docs/${seeded}`, { base_revision: 1, title: "The Whole Winter", content });
ok(put.status === 200, "seed the long chapter: " + put.status);
console.log(`e2e-long: ${put.json.item.word_count} words in one chapter, guard ${put.json.item.guard || "none"}, CPU throttle ${cpu}x`);

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
kit.main = page;
const cdp = await phone.newCDPSession(page);
await cdp.send("Emulation.setCPUThrottlingRate", { rate: cpu });
const url = `${kit.base()}/p/${book.slug}/d/${seeded}`;
const interactive = async () => { await page.waitForSelector(".scriobh-prose"); await page.waitForFunction(() => document.querySelector("#save-state")); };

await kit.test("phone: a 20,000-word chapter becomes editable quickly", async () => {
  await page.goto(url, { waitUntil: "load" });
  await interactive();
  const m = await page.evaluate(() => ({ mount: window.__mount, ed: (performance.getEntriesByType("resource").find((r) => /\/js\/editor\./.test(r.name)) || {}).responseEnd || 0 }));
  const since = m.mount - m.ed;
  console.log(`      editor mounted ${ms(m.mount)} after navigation start, ${ms(since)} after the editor bundle arrived`);
  ok(since < 400, `editor interactive ${ms(since)} after the chunk with 20,000 words (budget 400)`);
});

const typed = " The tide had gone out and the boats lay over on their sides.";
for (const where of ["start", "middle", "end"]) {
  await kit.test(`phone: typing at the ${where} of the chapter`, async () => {
    await page.evaluate(() => { window.__lat.length = 0; window.__proc.length = 0; });
    await page.locator(".scriobh-prose").tap();
    if (where === "start") await page.keyboard.press("ControlOrMeta+Home");
    else if (where === "end") await page.keyboard.press("ControlOrMeta+End");
    else {
      await page.evaluate(() => {
        const ed = document.querySelector(".scriobh-prose");
        const ps = ed.querySelectorAll("p");
        const p = ps[Math.floor(ps.length / 2)];
        const r = document.createRange();
        r.selectNodeContents(p);
        r.collapse(false);
        const s = getSelection();
        s.removeAllRanges();
        s.addRange(r);
        p.scrollIntoView({ block: "center" });
      });
    }
    await page.keyboard.type(typed.repeat(8), { delay: 14 });
    await waitFor(async () => (await page.locator("#save-state").getAttribute("data-state")) === "saved" && !(await page.evaluate(() => window.scriobh.write.unsaved())), "Saved", 40000);
    const { lat, proc } = await page.evaluate(() => ({ lat: window.__lat, proc: window.__proc }));
    console.log(`      ${lat.length} keystrokes at the ${where}: keydown to input p50 ${q(proc, 0.5).toFixed(1)} ms, p95 ${q(proc, 0.95).toFixed(1)} ms; to next frame p50 ${q(lat, 0.5).toFixed(1)} ms, p95 ${q(lat, 0.95).toFixed(1)} ms, max ${Math.max(...lat).toFixed(1)} ms`);
    ok(lat.length >= 400, "keystrokes measured: " + lat.length);
    ok(q(proc, 0.95) < 16, `keydown to input p95 ${q(proc, 0.95).toFixed(1)} ms at the ${where} (budget 16)`);
  });
}

await kit.test("phone: everything typed is on the server and survives a reload", async () => {
  const mine = await page.evaluate(() => window.scriobh.write.editor.getMarkdown());
  const body = async () => (await api(main, "GET", `/api/v1/docs/${seeded}`)).json.item.content;
  eq(await body(), mine, "the server body vs the editor body (bytes)");
  ok((mine.match(/The tide had gone out and the boats lay over on their sides\./g) || []).length >= 24, "all three typed runs are in the text");
  await page.reload({ waitUntil: "load" });
  await interactive();
  eq(await page.evaluate(() => window.scriobh.write.editor.getMarkdown()), mine, "text after a hard reload");
});

await phone.close();
await kit.finish("e2e-long");
