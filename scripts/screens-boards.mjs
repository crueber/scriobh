#!/usr/bin/env node
// Screenshots of the corkboard, the status board and reading mode for docs/screens/phase-3: every state at
// 1280x900 (ratio 1) and 390x844 (ratio 2), light and dark, plus a spot check of the Sepia, Slate and Moss themes.
//
//   node scripts/screens-boards.mjs --out docs/screens/phase-3
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-3");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-boardshots-");
const srv = await startServer(build(tmp), join(tmp, "data"));
const base = srv.base;
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const boot = await browser.newContext();
const bp = await boot.newPage();
await signIn(bp, base);
const api = async (method, path, data) => {
  const r = await boot.request.fetch(base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return r.json().catch(() => null);
};
const sentence = "The tide came in over the grey stones and the gulls wheeled above the quay, crying for the boats that were not there. ";
const para = (n) => (sentence.repeat(Math.ceil(n / 22))).split(" ").slice(0, n).join(" ").trim() + ".";
const chapter = (paras, n = 90) => Array.from({ length: paras }, (_, i) => (i === 2 ? "> Nobody counts the ones who stay.\n\n" : "") + para(n + (i % 3) * 14)).join("\n\n");

const p = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000, author_name: "Máire Nolan" })).item;
const seeded = (await api("GET", `/api/v1/projects/${p.id}/docs`)).order[0];
const mk = async (b) => (await api("POST", `/api/v1/projects/${p.id}/docs`, b)).item.id;
await mk({ title: "placeholder", kind: "note" });
await api("POST", `/api/v1/docs/${seeded}/trash`);
const ids = {};
ids.ded = await mk({ title: "Dedication", kind: "front_matter", content: "For the ones who stayed.", synopsis_short: "For the ones who stayed." });
ids.p1 = await mk({ title: "Part One: The Quay", kind: "part" });
ids.c1 = await mk({ title: "Low Water", kind: "chapter", parent_id: ids.p1, content: chapter(14) + "\n\nShe *counted* them twice, and then once more, in pencil.", synopsis_short: "Mara counts the boats that did not come in." });
ids.s1 = await mk({ title: "The ledger", kind: "scene", parent_id: ids.c1, content: chapter(5), synopsis_short: "A careful reckoning, done in pencil." });
ids.c2 = await mk({ title: "The ferryman's daughter", kind: "chapter", parent_id: ids.p1, content: chapter(10), synopsis_short: "Tobias refuses the crossing and will not say why." });
ids.c3 = await mk({ title: "A letter from the mainland", kind: "chapter", parent_id: ids.p1, content: chapter(8), synopsis_short: "" });
ids.p2 = await mk({ title: "Part Two: The Salt Road", kind: "part" });
ids.c4 = await mk({ title: "Nobody walks it in winter", kind: "chapter", parent_id: ids.p2, content: chapter(12), synopsis_short: "Two days on the causeway with the wrong boots." });
ids.c5 = await mk({ title: "A very long chapter title that goes on to test how a card wraps a heading at narrow widths", kind: "chapter", parent_id: ids.p2, content: chapter(3), synopsis_short: "" });
ids.back = await mk({ title: "Acknowledgements", kind: "back_matter", content: chapter(2) });
for (const m of (await api("GET", `/api/v1/projects/${p.id}/docs`)).items) if (m.title === "placeholder") await api("POST", `/api/v1/docs/${m.id}/trash`);
const patch = async (id, b) => { const d = (await api("GET", `/api/v1/docs/${id}`)).item; await api("PATCH", `/api/v1/docs/${id}`, { base_revision: d.revision, ...b }); };
await patch(ids.ded, { status: "done" });
await patch(ids.c1, { target_words: 1500, status: "revise" });
await patch(ids.s1, { status: "outline" });
await patch(ids.c2, { target_words: 1200, status: "draft" });
await patch(ids.c3, { target_words: 1200, status: "idea" });
await patch(ids.c4, { target_words: 900, status: "draft" });
await patch(ids.c5, { status: "idea" });
await patch(ids.back, { status: "done" });

const empty = (await api("POST", "/api/v1/projects", { title: "Fresh Page", kind: "novel" })).item;
const eseed = (await api("GET", `/api/v1/projects/${empty.id}/docs`)).order[0];
await api("POST", `/api/v1/projects/${empty.id}/docs`, { title: "x", kind: "note" });
await api("POST", `/api/v1/docs/${eseed}/trash`);
for (const m of (await api("GET", `/api/v1/projects/${empty.id}/docs`)).items) await api("POST", `/api/v1/docs/${m.id}/trash`);
const cookies = await boot.cookies();

const sizes = { desktop: { viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
async function run(size, scheme, tag, states) {
  const ctx = await browser.newContext({ ...sizes[size], colorScheme: scheme, serviceWorkers: "block" });
  await ctx.addCookies(cookies);
  const page = await ctx.newPage();
  const shot = async (name, o = {}) => { await page.screenshot({ path: join(out, `boards-${name}-${tag}.png`), ...o }); console.log("shot", name, tag); };
  const wait = async (sel) => { await page.waitForSelector(sel); await page.evaluate(() => document.fonts.ready); await sleep(120); };
  await states({ page, shot, wait, ctx });
  await ctx.close();
}
for (const size of Object.keys(sizes)) {
  for (const scheme of ["light", "dark"]) {
    await run(size, scheme, `${size}-${scheme}`, async ({ page, shot, wait }) => {
      const full = { fullPage: size === "mobile" };
      await page.goto(`${base}/p/${p.slug}/cards`, { waitUntil: "load" });
      await wait(".ccard");
      await page.waitForFunction(() => window.__boardsReady === true);
      await shot("corkboard", full);
      await page.locator(`#c-${ids.c2}`).focus();
      await sleep(150);
      await shot("corkboard-tools", { fullPage: false });
      await page.goto(`${base}/p/${p.slug}/cards?status=draft`, { waitUntil: "load" });
      await wait(".ccard");
      await shot("corkboard-filtered", full);
      await page.goto(`${base}/p/${p.slug}/board`, { waitUntil: "load" });
      await wait(".bcard");
      await page.waitForFunction(() => window.__boardsReady === true);
      await shot("board", full);
      await page.locator(`#c-${ids.c2}`).focus();
      await sleep(150);
      await shot("board-tools", { fullPage: false });
      await page.goto(`${base}/p/${empty.slug}/cards`, { waitUntil: "load" });
      await wait("#b-empty");
      await shot("empty-corkboard");
      await page.goto(`${base}/p/${empty.slug}/board`, { waitUntil: "load" });
      await wait("#b-empty");
      await shot("empty-board");
      await page.goto(`${base}/p/${empty.slug}/read`, { waitUntil: "load" });
      await wait(".reader-empty");
      await shot("empty-read");
      await page.goto(`${base}/p/${p.slug}/read/${ids.c1}`, { waitUntil: "load" });
      await wait("#read-stream");
      await shot("read");
      await page.getByRole("button", { name: "Contents" }).click();
      await sleep(150);
      await shot("read-contents");
      await page.keyboard.press("Escape");
      await page.evaluate(([pid, d]) => localStorage.setItem("scriobh:read:" + document.getElementById("reader").dataset.user + ":" + pid, JSON.stringify({ doc: d, f: 0.42, title: "Low Water", at: Date.now() })), [p.id, ids.c1]);
      await page.goto(`${base}/p/${p.slug}/read/${ids.c2}`, { waitUntil: "load" });
      await wait("#read-continue:not([hidden])");
      await shot("read-continue");
    });
  }
}
// Spot checks: the other themes, at desktop size only.
await api("PUT", "/api/v1/me/prefs", { items: { theme_light: "sepia", theme_dark: "slate", editor_font: "serif" } });
await run("desktop", "light", "sepia-light", async ({ page, shot, wait }) => {
  await page.goto(`${base}/p/${p.slug}/cards`, { waitUntil: "load" }); await wait(".ccard"); await page.locator(`#c-${ids.c2}`).focus(); await sleep(150); await shot("theme-corkboard");
  await page.goto(`${base}/p/${p.slug}/board`, { waitUntil: "load" }); await wait(".bcard"); await shot("theme-board");
  await page.goto(`${base}/p/${p.slug}/read/${ids.c1}`, { waitUntil: "load" }); await wait("#read-stream"); await shot("theme-read");
});
await run("desktop", "dark", "slate-dark", async ({ page, shot, wait }) => {
  await page.goto(`${base}/p/${p.slug}/cards`, { waitUntil: "load" }); await wait(".ccard"); await page.locator(`#c-${ids.c2}`).focus(); await sleep(150); await shot("theme-corkboard");
  await page.goto(`${base}/p/${p.slug}/board`, { waitUntil: "load" }); await wait(".bcard"); await shot("theme-board");
  await page.goto(`${base}/p/${p.slug}/read/${ids.c1}`, { waitUntil: "load" }); await wait("#read-stream"); await shot("theme-read");
});
await api("PUT", "/api/v1/me/prefs", { items: { theme_dark: "moss", editor_size: "21", editor_font: "sans" } });
await run("desktop", "dark", "moss-dark", async ({ page, shot, wait }) => {
  await page.goto(`${base}/p/${p.slug}/cards`, { waitUntil: "load" }); await wait(".ccard"); await shot("theme-corkboard");
  await page.goto(`${base}/p/${p.slug}/board`, { waitUntil: "load" }); await wait(".bcard"); await shot("theme-board");
  await page.goto(`${base}/p/${p.slug}/read/${ids.c1}`, { waitUntil: "load" }); await wait("#read-stream"); await shot("theme-read");
});
await browser.close();
await srv.stop();
