#!/usr/bin/env node
// Screenshots of research and the scratchpad for docs/screens/phase-3: the tree, the empty state, the board,
// a note, a save conflict, a clipping with the click-through warning, the scratchpad and the tags manager.
// 1280x900 (ratio 1) and 390x844 (ratio 2), light and dark, for a project; then the list, board and note in
// sepia (light) and slate and moss (dark) at 1280 as a spot check of the other themes.
//
//   node scripts/screens-research.mjs --out docs/screens/phase-3
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-3");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-resshots-");
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
const para = (s) => s.join("\n\n");

const p = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000, author_name: "Máire Nolan" })).item;
const q = `/api/v1/projects/${p.id}`;
const fresh = (await api("POST", "/api/v1/projects", { title: "Fresh Page", kind: "novel" })).item;
const tags = [["harbour", "teal"], ["weather", "blue"], ["people", "plum"], ["ledgers", "ochre"], ["songs", "rose"], ["law", "slate"]];
for (const [name, color] of tags) await api("POST", `${q}/tags`, { name, color });
const mk = async (b) => (await api("POST", `${q}/research`, b)).item;
const tides = await mk({ kind: "folder", title: "Tides and weather" });
const people = await mk({ kind: "folder", title: "People of the quay" });
const n1 = await mk({ title: "Tide tables for the autumn", parent_id: tides.id, status: "useful", tags: ["harbour", "weather"], body: para(["Low water at **six**, high water at noon, and the spring tides run an hour late after the equinox.", "The harbour master keeps the tables in pencil because the figures change.", "See the [chart](/files/att_example) for the soundings."]) });
await mk({ title: "Fog, and how the gulls know", parent_id: tides.id, status: "reading", tags: ["weather"], body: "Notes from the quay, October." });
await mk({ title: "A harbour master's diary", kind: "clipping", parent_id: tides.id, status: "inbox", tags: ["harbour", "people"], source_url: "https://example.org/archive/diary-1871", retrieved_on: "2026-03-14", quote: "The sea was quiet for nine days, and nobody trusted it.", body: "Useful for Mara's father. The nine days could be the nine days before the storm." });
await mk({ title: "Who keeps the ledger", parent_id: people.id, status: "reading", tags: ["people", "ledgers"], body: "The ferryman's daughter, and her pencil." });
await mk({ title: "Songs sung at the slip", parent_id: people.id, status: "inbox", tags: ["songs"], body: "Three verses, none of them finished." });
await mk({ title: "Salvage law, short version", status: "useful", tags: ["law", "harbour"], body: "A wreck belongs to the finder for a year and a day." });
await mk({ title: "Old rope and tar", status: "archived", body: "Not for the book." });
await mk({ title: "A note with no tags or words yet", status: "inbox" });
const clip = (await api("GET", `${q}/research`)).items.find((i) => i.kind === "clipping");
await api("PUT", `${q}/scratchpad`, { content: "Ask Mara what she saw on the ninth day.\n\nCheck whether the ferry runs on a Sunday.\n\nThe ending: the ledger is blank.", base_revision: 0 });
const cookies = await boot.cookies();

const sizes = { desktop: { viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
const go = async (page, path) => { await page.goto(base + path, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); await sleep(250); };

async function states(page, shot) {
  const r = `/p/${p.slug}/research`;
  await go(page, r);
  await shot("tree", { fullPage: true });
  await go(page, `/p/${fresh.slug}/research`);
  await shot("empty");
  await go(page, `${r}/board`);
  await shot("board", { fullPage: true });
  await go(page, `${r}/${n1.id}`);
  await shot("note", { fullPage: true });
  await go(page, `${r}/${clip.id}`);
  await page.getByRole("button", { name: /Open the source/ }).click();
  await sleep(100);
  await shot("clipping-warning", { fullPage: true });
  await go(page, `/p/${p.slug}/scratch`);
  await shot("scratch");
  await go(page, `${r}/tags`);
  await shot("tags", { fullPage: true });
  // The conflict bar: another save lands while this page is open.
  await go(page, `${r}/${n1.id}`);
  const cur = (await api("GET", `${q}/research/${n1.id}`)).item;
  await api("PATCH", `${q}/research/${n1.id}`, { base_revision: cur.revision, body: cur.body + "\n\nAdded from another tab." });
  await page.locator("#n-body").press("End");
  await page.keyboard.type(" And this from here.");
  await page.locator(".r-bar").waitFor({ timeout: 8000 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await sleep(150);
  await shot("conflict");
}

for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const tag = `${size}-${scheme}`;
    await states(page, async (name, o = {}) => { await page.screenshot({ path: join(out, `research-${name}-${tag}.png`), ...o }); console.log("shot", name, tag); });
    await ctx.close();
  }
}

// Three of the five themes: the list, the board and a note.
for (const [name, scheme] of [["sepia", "light"], ["slate", "dark"], ["moss", "dark"]]) {
  await api("PUT", "/api/v1/me/prefs", { items: scheme === "light" ? { theme: "light", theme_light: name } : { theme: "dark", theme_dark: name } });
  const ctx = await browser.newContext({ ...sizes.desktop, colorScheme: scheme, serviceWorkers: "block" });
  await ctx.addCookies(await boot.cookies());
  const page = await ctx.newPage();
  for (const [what, path] of [["tree", `/p/${p.slug}/research`], ["board", `/p/${p.slug}/research/board`], ["note", `/p/${p.slug}/research/${clip.id}`]]) {
    await go(page, path);
    await page.screenshot({ path: join(out, `research-${what}-theme-${name}.png`), fullPage: true });
    console.log("shot", what, name);
  }
  await ctx.close();
}
await api("PUT", "/api/v1/me/prefs", { items: { theme: "system" } });
await browser.close();
await srv.stop();
