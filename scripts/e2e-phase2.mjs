#!/usr/bin/env node
// Phase 2 acceptance walk: a 60-chapter, 60,000-word project, driven on a 390x844 touch phone with the CPU
// slowed 4x. It measures what the v0.2 exit criteria name: open, tree, outline, search, versions list and
// restore, reorder of a whole part (the word total must not move), wiki-link creation, and daily goal
// progress after typing. Server-side budgets from docs/ARCHITECTURE.md are checked on the API.
//
//   node scripts/e2e-phase2.mjs [--cpu 4]
//
// "Yesterday's" history uses the development build's clock hook (see e2e-write2.mjs).
//
// Budgets that fail this script (at the given throttle where the phone is involved):
//   editor interactive < 250 ms after the editor chunk arrives
//   open a chapter (server) < 150 ms p95; save a chapter (server) < 120 ms p95; search (server) < 80 ms p95
//   outline page (server) < 150 ms p95; the outline is on screen within 2.0 s on the throttled phone
//   the whole-part move leaves every word where it was
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startServer } from "./harness.mjs";

const cpu = Number(process.argv.includes("--cpu") ? process.argv[process.argv.indexOf("--cpu") + 1] : 4);
const now = new Date();
const yesterdayNoon = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 12, 0, 0);
const offset = Math.round((yesterdayNoon.getTime() - now.getTime()) / 1000);
const env = { SCRIOBH_SESSION_HOURS: "400", SCRIOBH_SESSION_IDLE_MINUTES: "100000" };
const kit = await createKit("e2e-phase2", { env: { ...env, SCRIOBH_DEV_CLOCK_OFFSET_SECONDS: String(offset) } });
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
const q = (a, f) => { const s = [...a].sort((x, y) => x - y); return s[Math.min(s.length - 1, Math.floor(s.length * f))]; };
const ms = (n) => `${n.toFixed(0)} ms`;

// ---- a manuscript: 6 parts, 60 chapters of 1,000 words, deterministic -------------------------
let seed = 11;
const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
const vocab = "the tide harbour boat lamp ferry stone wall road house door window gull wind rain salt net rope pier quay light shadow letter ledger winter spring morning evening slow quiet heavy bright old narrow long cold warm walked stood watched waited carried opened closed turned heard knew said had was were came went held kept left found lost mended counted listened".split(" ");
function sentence() {
  const n = 7 + Math.floor(rnd() * 12), w = [];
  for (let i = 0; i < n; i++) w.push(vocab[Math.floor(rnd() * vocab.length)]);
  return w.join(" ").replace(/^./, (c) => c.toUpperCase()) + ".";
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

let book, chapters = [], parts = [];
await kit.test("setup: 60 chapters, 60,000 words, yesterday's versions, a restart on the real clock", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "Sixty Chapters", kind: "novel", word_goal: 90000 });
  book = made.json.item;
  const seeded = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  for (let part = 1; part <= 6; part++) {
    const pd = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: `Part ${part}`, kind: "part" })).json.item;
    parts.push(pd.id);
    for (let c = 1; c <= 10; c++) {
      const n = chapters.length + 1;
      const body = { title: `Chapter ${n}`, kind: "chapter", parent_id: pd.id, content: chapterText(1000), synopsis_short: `What happens in chapter ${n}.` };
      if (n === 1) {
        await api(main, "PATCH", `/api/v1/docs/${seeded}`, { base_revision: 1, title: body.title, content: body.content, synopsis_short: body.synopsis_short });
        await api(main, "POST", `/api/v1/docs/${seeded}/move`, { parent_id: pd.id });
        chapters.push(seeded);
      } else chapters.push((await api(main, "POST", `/api/v1/projects/${book.id}/docs`, body)).json.item.id);
    }
  }
  // Chapter 1 gets a history written "yesterday": two named versions of two different texts.
  const c1 = chapters[0];
  let rev = (await api(main, "GET", `/api/v1/docs/${c1}`)).json.item.revision;
  eq((await api(main, "POST", `/api/v1/docs/${c1}/revisions`, { label: "Draft one" })).status, 201, "snapshot one");
  eq((await api(main, "PATCH", `/api/v1/docs/${c1}`, { base_revision: rev, content: chapterText(1000) })).status, 200, "second text");
  eq((await api(main, "POST", `/api/v1/docs/${c1}/revisions`, { label: "Draft two" })).status, 201, "snapshot two");
  await pg.close();
  await kit.srv.stop();
  kit.srv = await startServer(kit.bin, kit.dataDir, env);
  rev = (await api(main, "GET", `/api/v1/docs/${c1}`)).json.item.revision;
  eq((await api(main, "PATCH", `/api/v1/docs/${c1}`, { base_revision: rev, content: chapterText(1000) })).status, 200, "today's text");
  const items = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.items;
  const total = items.reduce((a, d) => a + d.word_count, 0);
  console.log(`e2e-phase2: ${chapters.length} chapters, ${parts.length} parts, ${total} words, CPU throttle ${cpu}x`);
  ok(total >= 58000, "about 60,000 words: " + total);
});

// ---- server budgets, on the API ----------------------------------------------------------------
const timed = async (n, fn) => { const t = []; for (let i = 0; i < n; i++) { const t0 = performance.now(); await fn(i); t.push(performance.now() - t0); } return t; };
await kit.test("server: open a chapter, save a chapter, search, the outline page", async () => {
  const open = await timed(20, (i) => api(main, "GET", `/api/v1/docs/${chapters[i * 2]}`));
  const page = await timed(20, async (i) => { const r = await main.request.fetch(`${base()}/p/${book.slug}/d/${chapters[i * 2 + 1]}`); await r.text(); });
  const outline = await timed(20, async () => { const r = await main.request.fetch(`${base()}/p/${book.slug}/outline`); await r.text(); });
  const search = await timed(20, (i) => api(main, "GET", `/api/v1/projects/${book.id}/search?q=${["lamp", "quiet harbour", "ledger", "winter"][i % 4]}`));
  const target = chapters[30];
  const save = await timed(15, async (i) => {
    const d = (await api(main, "GET", `/api/v1/docs/${target}`)).json.item;
    const r = await api(main, "PATCH", `/api/v1/docs/${target}`, { base_revision: d.revision, content: d.content + `\nA line added ${i}.\n` });
    if (r.status !== 200) throw new Error("save " + r.status);
  });
  console.log(`      GET doc p95 ${ms(q(open, 0.95))}; chapter page p95 ${ms(q(page, 0.95))}; outline page p95 ${ms(q(outline, 0.95))}; search p95 ${ms(q(search, 0.95))}; save (with a read) p95 ${ms(q(save, 0.95))}`);
  ok(q(page, 0.95) < 150, `chapter page p95 ${ms(q(page, 0.95))} (budget 150 ms)`);
  ok(q(outline, 0.95) < 150, `outline page p95 ${ms(q(outline, 0.95))} (budget 150 ms)`);
  ok(q(search, 0.95) < 80, `search p95 ${ms(q(search, 0.95))} (budget 80 ms)`);
  ok(q(save, 0.95) < 120 + 40, `save p95 ${ms(q(save, 0.95))} (budget 120 ms plus the read in this loop)`);
});

// ---- the phone ---------------------------------------------------------------------------------------
const phone = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true, isMobile: true,
  userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1" });
await phone.addCookies(await main.cookies());
await phone.addInitScript(() => {
  window.__mount = 0;
  new MutationObserver((_, o) => { if (document.querySelector(".scriobh-prose")) { window.__mount = performance.now(); o.disconnect(); } }).observe(document, { childList: true, subtree: true });
});
const page = await phone.newPage();
kit.watch(page, "phone");
kit.main = page;
const cdp = await phone.newCDPSession(page);
await cdp.send("Emulation.setCPUThrottlingRate", { rate: cpu });
const url = (id) => `${base()}/p/${book.slug}/d/${id}`;
const editorText = () => page.evaluate(() => window.scriobh.write.editor.getMarkdown());
const ready = async () => { await page.waitForSelector(".scriobh-prose"); await page.waitForFunction(() => window.scriobh && window.scriobh.write && window.scriobh.inspector, null, { timeout: 15000 }); };

await kit.test("phone: open a chapter of a 60-chapter book, the editor is interactive", async () => {
  await page.goto(url(chapters[0]), { waitUntil: "load" });
  await ready();
  const m = await page.evaluate(() => ({ mount: window.__mount, ed: (performance.getEntriesByType("resource").find((r) => /\/js\/editor\./.test(r.name)) || {}).responseEnd || 0 }));
  console.log(`      editor mounted ${ms(m.mount)} after navigation start, ${ms(m.mount - m.ed)} after the editor bundle arrived`);
  ok(m.mount - m.ed < 250, `editor interactive ${ms(m.mount - m.ed)} after the chunk (budget 250)`);
});

await kit.test("phone: the manuscript drawer lists 66 documents, with the word total", async () => {
  const t0 = Date.now();
  await page.locator("#tree-toggle").tap();
  await page.locator("#rail.is-open").waitFor();
  const rows = await page.locator("#tree .tree-row").count();
  console.log(`      drawer open to ${rows} rows in ${Date.now() - t0} ms`);
  eq(rows, 66, "tree rows");
  const total = Number((await page.locator("#tree-total").innerText()).replace(/,/g, ""));
  const items = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.items;
  eq(total, items.reduce((a, d) => a + d.word_count, 0), "the tree's total matches the server");
  await page.locator("#rail-close").tap();
});

await kit.test("phone: the outline loads fast and shows every row and the total", async () => {
  const t0 = Date.now();
  await page.goto(`${base()}/p/${book.slug}/outline`, { waitUntil: "load" });
  await page.waitForSelector(".orow");
  const t = Date.now() - t0;
  const n = await page.locator(".orow").count();
  console.log(`      outline: ${n} rows on screen ${t} ms after navigation (wall, throttled ${cpu}x)`);
  eq(n, 66, "outline rows");
  ok(t < 2000, `outline took ${t} ms at ${cpu}x (budget 2000)`);
});

await kit.test("phone: search finds a phrase in 60,000 words", async () => {
  await page.goto(`${base()}/p/${book.slug}/search`, { waitUntil: "load" });
  const t0 = Date.now();
  await page.locator("#search-q").fill("quiet harbour");
  await page.locator("#search-q").press("Enter");
  await page.waitForSelector(".result");
  const t = Date.now() - t0;
  console.log(`      search page answered in ${t} ms (wall, throttled ${cpu}x), ${await page.locator(".result").count()} results`);
  ok(t < 1500, `search took ${t} ms at ${cpu}x`);
});

await kit.test("phone: versions list and restore of yesterday's text", async () => {
  await page.goto(url(chapters[0]), { waitUntil: "load" });
  await ready();
  const was = await editorText();
  const t0 = Date.now();
  await page.getByRole("button", { name: "Versions", exact: true }).tap();
  await page.getByRole("button", { name: /^Yesterday/ }).waitFor();
  console.log(`      versions list ${Date.now() - t0} ms after the tap`);
  await page.getByRole("button", { name: /^Yesterday/ }).tap();
  const t1 = Date.now();
  await page.getByRole("button", { name: /^Restore the version/ }).first().tap();
  await waitFor(async () => (await editorText()) !== was, "the editor to hold the restored text", 15000);
  console.log(`      restore ${Date.now() - t1} ms from the tap to the text on the page`);
  const kept = (await api(main, "GET", `/api/v1/docs/${chapters[0]}/revisions?limit=30`)).json.items;
  ok(kept.some((r) => r.reason === "restore"), "a restore version was written");
});

await kit.test("phone: reorder a whole part from the outline; no word moves", async () => {
  const before = (await api(main, "GET", `/api/v1/projects/${book.id}/outline`)).json;
  const words = Object.fromEntries(before.items.map((i) => [i.id, i.word_count]));
  await page.goto(`${base()}/p/${book.slug}/outline`, { waitUntil: "load" });
  await page.waitForSelector(".orow");
  const partRow = page.locator(`#o-${parts[0]}`);
  await partRow.locator(".orow-syn").tap();
  const down = partRow.getByRole("button", { name: /^Move down / });
  const t0 = Date.now();
  await down.tap();
  await waitFor(async () => (await api(main, "GET", `/api/v1/projects/${book.id}/outline`)).json.items.find((i) => i.kind === "part").title !== "Part 1", "the part to move");
  console.log(`      part moved ${Date.now() - t0} ms after the tap (throttled ${cpu}x)`);
  const after = (await api(main, "GET", `/api/v1/projects/${book.id}/outline`)).json;
  eq(after.totals.words, before.totals.words, "the project's word total after the move");
  for (const i of after.items) eq(i.word_count, words[i.id], `words of ${i.title}`);
  const kids = after.items.filter((i) => i.parent_id === parts[0]).length;
  eq(kids, 10, "Part 1 still holds its ten chapters");
});

await kit.test("phone: a wiki link creates its note and goes there", async () => {
  await page.goto(url(chapters[5]), { waitUntil: "load" });
  await ready();
  await page.locator(".scriobh-prose").tap();
  await page.keyboard.press(`${MOD}+End`);
  await page.keyboard.type(" [[The Lamp Keeper]] ");
  await page.locator(".wikilink-missing").waitFor();
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "the text to save", 15000);
  await page.locator(".wikilink-missing").tap();
  await page.locator("#wiki-card").getByRole("button", { name: "Create note" }).tap();
  await page.waitForURL((u) => /\/d\/doc_/.test(u.pathname) && !u.pathname.endsWith(chapters[5]), { timeout: 20000 });
  await page.waitForSelector(".scriobh-prose");
  eq(await page.locator("#doc-title").inputValue(), "The Lamp Keeper", "the note's title");
  const notes = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.items.filter((d) => d.title === "The Lamp Keeper");
  eq(notes.length, 1, "one note");
});

await kit.test("phone: the daily goal fills as words are typed", async () => {
  eq((await api(main, "PUT", `/api/v1/projects/${book.id}/goals`, { kind: "daily", target_words: 60 })).status, 200, "set the daily goal");
  const before = (await api(main, "GET", `/api/v1/projects/${book.id}/stats/today`)).json;
  await page.goto(url(chapters[7]), { waitUntil: "load" });
  await ready();
  await page.locator(".scriobh-prose").tap();
  await page.keyboard.press(`${MOD}+End`);
  const typed = Array.from({ length: 80 }, (_, i) => "word" + i).join(" ");
  await page.keyboard.type(" " + typed, { delay: 4 });
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "the text to save", 20000);
  const today = await waitFor(async () => { const t = (await api(main, "GET", `/api/v1/projects/${book.id}/stats/today`)).json; return t.words_added >= before.words_added + 70 ? t : null; }, "the words added today", 12000);
  ok(today.has_goal && today.hit, "the daily goal is met: " + JSON.stringify(today));
  await page.goto(`${base()}/p/${book.slug}/goals`, { waitUntil: "load" });
  ok(/goal met|met/i.test(await page.locator("body").innerText()), "the goals page says it is met");
});

await kit.finish("e2e-phase2");
