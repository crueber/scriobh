#!/usr/bin/env node
// Screenshots of the names, the scene tab and the outline orders for docs/screens/phase-3: the writing view with names
// from the bible underlined and the hover card, the "In this scene" tab, the outline in the story timeline and
// point-of-view orders with their columns, and "Where this appears" on both entity sheets. 1280x900 (ratio 1) and
// 390x844 (ratio 2), light and dark, plus a spot check of the Sepia, Slate and Moss themes.
//
//   node scripts/screens-scenes.mjs --out docs/screens/phase-3
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-3");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-sceneshots-");
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

const p = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan" })).item;
const c = (await api("POST", "/api/v1/compendiums", { name: "The Salt Road bible", project_id: p.id })).item;
const ent = async (kind, name, summary, aliases) => (await api("POST", `/api/v1/compendiums/${c.id}/entities`, { kind, name, summary, aliases })).item;
const mara = await ent("character", "Mara Voss", "The harbour clerk who keeps the ledger and tells no one what it says.", ["Mara"]);
const tobias = await ent("character", "Tobias Hale", "The ferryman, who refuses the crossing.", ["Tobias"]);
const quay = await ent("place", "Saltmarsh Quay", "A grey quay, empty since the storm.", ["the quay"]);
await ent("item", "The Brass Lamp", "A lamp that is never lit.", ["the lamp"]);
const seeded = (await api("GET", `/api/v1/projects/${p.id}/docs`)).order[0];
const mk = async (b) => (await api("POST", `/api/v1/projects/${p.id}/docs`, b)).item.id;
await api("POST", `/api/v1/docs/${seeded}/trash`).catch(() => null);
const part = await mk({ title: "Part One: The Quay", kind: "part" });
const chap = await mk({ title: "Low Water", kind: "chapter", parent_id: part });
const body = (a, b) => `Mara Voss carried the brass lamp down to Saltmarsh Quay before the light went. She did not light it; ${a} had said not to.\n\nTobias stood at the end of the quay with his back to her, and for a long time neither of them spoke. The tide went out. ${b} watched the gulls.\n\n%%Mara knows what the ledger says%%\n\nWhen he turned, the lamp was in her hands and the quay was nearly dark.\n`;
const s1 = await mk({ title: "The ledger", kind: "scene", parent_id: chap, content: body("Tobias", "Mara") });
const s2 = await mk({ title: "The crossing refused", kind: "scene", parent_id: chap, content: body("Mara", "Tobias") });
const s3 = await mk({ title: "A letter from the mainland", kind: "scene", parent_id: chap, content: "The letter was addressed to Mara and had been opened. Nobody at the quay would say by whom.\n" });
const patch = async (id, b) => { const d = (await api("GET", `/api/v1/docs/${id}`)).item; return (await api("PATCH", `/api/v1/docs/${id}`, { base_revision: d.revision, ...b })).item; };
const setPov = async (id, who, where) => {
  let d = (await api("GET", `/api/v1/docs/${id}`)).item;
  if (who) { const r = await api("PUT", `/api/v1/docs/${id}/pov`, { entity: who.id, base_revision: d.revision }); d = r.item || r; }
  if (where) await api("PUT", `/api/v1/docs/${id}/location`, { entity: where.id, base_revision: d.revision });
};
await patch(s1, { story_date: "1998-04-12", status: "draft" });
await patch(s2, { story_date: "1998-04-10", status: "revise" });
await patch(s3, { story_date: "", status: "idea" });
await setPov(s1, mara, quay);
await setPov(s2, tobias, quay);
await sleep(1500);
const cookies = await boot.cookies();

const sizes = { desktop: { viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
async function run(size, scheme, tag, states) {
  const ctx = await browser.newContext({ ...sizes[size], colorScheme: scheme, serviceWorkers: "block" });
  await ctx.addCookies(cookies);
  const page = await ctx.newPage();
  const shot = async (name, o = {}) => { await page.screenshot({ path: join(out, `scenes-${name}-${tag}.png`), ...o }); console.log("shot", name, tag); };
  const wait = async (sel) => { await page.waitForSelector(sel); await page.evaluate(() => document.fonts.ready); await sleep(150); };
  await states({ page, shot, wait, ctx });
  await ctx.close();
}
async function write(page, wait, hover) {
  await page.goto(`${base}/p/${p.slug}/d/${s1}`, { waitUntil: "load" });
  await wait(".scriobh-prose");
  await page.waitForSelector(".scriobh-prose .mention", { timeout: 20000 });
  if (hover) { await page.locator(".scriobh-prose .mention").first().hover(); await page.locator("#men-card").waitFor({ state: "visible" }); await sleep(150); }
}
const states = (size) => async ({ page, shot, wait }) => {
  const full = { fullPage: size === "mobile" };
  await write(page, wait, size === "desktop");
  if (size === "mobile") { await page.locator(".scriobh-prose .mention").first().tap(); await page.locator("#men-card").waitFor({ state: "visible" }); await sleep(150); }
  await shot("names", { fullPage: false });
  await page.keyboard.press("Escape");
  await page.evaluate(() => window.scriobh.inspector.open("scene", false));
  await page.locator("#ipanel-scene .sc-name").first().waitFor();
  await sleep(200);
  await shot("tab", { fullPage: false });
  await page.locator("#ipanel-scene").getByRole("button", { name: "Ada" }).first().click().catch(() => null);
  await page.locator("#ipanel-scene .sc-name").first().click();
  await sleep(150);
  await shot("tab-card", { fullPage: false });
  await page.getByRole("button", { name: /Change the point of view/ }).click().catch(() => null);
  await page.locator("#sc-pick-pov-in").fill("to").catch(() => null);
  await sleep(500);
  await shot("tab-picker", { fullPage: false });
  await page.goto(`${base}/p/${p.slug}/outline?sort=timeline`, { waitUntil: "load" });
  await wait(".orow");
  await shot("outline-timeline", full);
  await page.goto(`${base}/p/${p.slug}/outline?sort=pov`, { waitUntil: "load" });
  await wait(".o-groups");
  await shot("outline-pov", full);
  await page.goto(`${base}/p/${p.slug}/e/${mara.id}`, { waitUntil: "load" });
  await wait("#where .where-row");
  await page.locator("#where").scrollIntoViewIfNeeded();
  await shot("where-book", full);
  await page.goto(`${base}/c/${c.slug}/e/${mara.id}`, { waitUntil: "load" });
  await wait("#where .where-row");
  await page.locator("#where").scrollIntoViewIfNeeded();
  await shot("where-compendium", full);
};
for (const size of Object.keys(sizes)) {
  for (const scheme of ["light", "dark"]) await run(size, scheme, `${size}-${scheme}`, states(size));
}
// Spot checks: the other themes, at desktop size only.
const spot = async ({ page, shot, wait }) => {
  await write(page, wait, true);
  await shot("theme-names", { fullPage: false });
  await page.goto(`${base}/p/${p.slug}/outline?sort=pov`, { waitUntil: "load" });
  await wait(".o-groups");
  await shot("theme-outline-pov");
};
await api("PUT", "/api/v1/me/prefs", { items: { theme_light: "sepia", theme_dark: "slate" } });
await run("desktop", "light", "sepia-light", spot);
await run("desktop", "dark", "slate-dark", spot);
await api("PUT", "/api/v1/me/prefs", { items: { theme_dark: "moss" } });
await run("desktop", "dark", "moss-dark", spot);
await browser.close();
await srv.stop();
