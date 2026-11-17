#!/usr/bin/env node
// Screenshots of the Compendium pages for docs/screens/phase-3: light and dark, 1280 and 390 px, plus three of the
// other themes at 1280 px.
//
//   node scripts/screens-compendium.mjs --out docs/screens/phase-3
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-3");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-compshots-");
const bin = build(tmp);
const srv = await startServer(bin, join(tmp, "data"));
const base = srv.base;
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});

const as = async (who) => {
  const ctx = await browser.newContext({ serviceWorkers: "block" });
  const p = await ctx.newPage();
  await signIn(p, base, who);
  const api = async (method, path, data) => {
    const r = await ctx.request.fetch(base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
    const json = await r.json().catch(() => null);
    if (r.status() >= 400) throw new Error(`${method} ${path} answered ${r.status()} ${JSON.stringify(json)}`);
    return json;
  };
  return { ctx, api };
};
const wren = await as("writer@dev.scriobh");
const ada = await as("admin@dev.scriobh");
const rory = await as("reader@dev.scriobh");
const api = wren.api;

const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
const session = async (opts, extraCookies) => {
  const ctx = await browser.newContext({ serviceWorkers: "block", ...opts });
  await ctx.addCookies(await wren.ctx.cookies());
  const page = await ctx.newPage();
  return { ctx, page };
};
const each = async (fn, only) => {
  for (const [size, opts] of Object.entries(sizes)) {
    for (const scheme of ["light", "dark"]) {
      const s = await session({ ...opts, colorScheme: scheme });
      const shot = async (name, o = {}) => {
        await s.page.evaluate(() => document.fonts.ready);
        await s.page.screenshot({ path: join(out, `compendium-${name}-${size}-${scheme}.png`), fullPage: true, ...o });
        console.log("shot", name, size, scheme);
      };
      await fn(s.page, shot, size, scheme);
      await s.ctx.close();
    }
  }
};
const go = (page, path) => page.goto(base + path, { waitUntil: "load" });

// 1. The empty library section, before anything exists.
await each(async (page, shot) => { await go(page, "/compendiums"); await shot("empty"); });

// Seed: three Compendiums with entities, a reference, people, two projects.
const salt = (await api("POST", "/api/v1/compendiums", { name: "The Salt Road Bible", kind: "fiction", description: "Everyone and everywhere in the Salt Road books: the harbour towns, the families who run them, and the roads between." })).item;
const tides = (await api("POST", "/api/v1/compendiums", { name: "Tides and Harbours", kind: "nonfiction", description: "Research on tides, weather and small ports." })).item;
for (const [kind, name] of [["character", "Orla Brennan"], ["character", "Tomas Hale"], ["place", "Carrick Harbour"], ["place", "The Salt Road"], ["item", "The brass compass"], ["org", "The Pilots' Guild"]]) {
  await api("POST", `/api/v1/compendiums/${salt.id}/entities`, { kind, name, summary: `${name}, as the books have them.` });
}
for (const name of ["Spring tides", "Fog"]) await api("POST", `/api/v1/compendiums/${tides.id}/entities`, { kind: "concept", name });
await api("POST", `/api/v1/compendiums/${salt.id}/refs`, { target_id: tides.id });
await api("POST", `/api/v1/compendiums/${salt.id}/members`, { email: "admin@dev.scriobh", role: "editor" });
await api("POST", `/api/v1/compendiums/${salt.id}/members`, { email: "reader@dev.scriobh", role: "reader" });
const cosmos = (await ada.api("POST", "/api/v1/compendiums", { name: "A Shared Cosmology", kind: "fiction", description: "The older world behind several authors' books." })).item;
await ada.api("POST", `/api/v1/compendiums/${cosmos.id}/members`, { email: "writer@dev.scriobh", role: "reader" });
const scratch = (await api("POST", "/api/v1/compendiums", { name: "Scratch World", kind: "fiction" })).item;
const book = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan" })).item;
const hedge = (await api("POST", "/api/v1/projects", { title: "Hedgerow", kind: "short" })).item;
await api("PUT", `/api/v1/projects/${book.id}/compendium`, { compendium_id: salt.id });
await api("POST", `/api/v1/projects/${book.id}/compendium/refs`, { compendium_id: tides.id });
const series = (await api("POST", "/api/v1/compendiums", { name: "The Salt Road Series Bible", kind: "fiction" })).item;
for (const name of ["Orla Brennan", "Dara Kell", "Inis Mór"]) await api("POST", `/api/v1/compendiums/${series.id}/entities`, { kind: name === "Inis Mór" ? "place" : "character", name });
await api("PUT", `/api/v1/projects/${hedge.id}/compendium`, { compendium_id: scratch.id });
await api("POST", `/api/v1/projects/${book.id}/members`, { email: "admin@dev.scriobh", role: "editor", add_to_compendium: false }).catch(() => {});
await api("POST", `/api/v1/projects/${book.id}/members`, { email: "reader@dev.scriobh", role: "reader" }).catch(() => {});
const archived = (await api("POST", "/api/v1/compendiums", { name: "Old Notes", kind: "nonfiction" })).item;
await api("POST", `/api/v1/compendiums/${archived.id}/archive`);
await api("POST", `/api/v1/compendiums/${scratch.id}/trash`).catch(() => {});

// 2. The pages.
await each(async (page, shot, size) => {
  await go(page, "/compendiums"); await shot("library");
  await go(page, "/compendiums?state=archived"); await shot("library-archived");
  await go(page, "/compendiums?state=trashed"); await shot("library-trash");
  await go(page, "/compendiums"); await page.getByRole("link", { name: "New Compendium" }).first().click();
  await page.locator("dialog#dialog[open]").waitFor(); await new Promise((r) => setTimeout(r, 400));
  await shot("new-dialog", { fullPage: false });
  await go(page, `/c/${salt.slug}`); await shot("overview");
  await page.locator("#people").scrollIntoViewIfNeeded(); await shot("members", { fullPage: false });
  await page.locator("#refs").scrollIntoViewIfNeeded(); await shot("references", { fullPage: false });
  await go(page, `/p/${book.slug}/compendium`); await shot("project-panel");
  await go(page, `/p/${hedge.slug}/compendium`); await shot("project-panel-trashed-primary");
  await go(page, `/p/${book.slug}/compendium`);
  await page.locator("#pc-set").selectOption({ label: "The Salt Road Series Bible" });
  await page.getByRole("button", { name: "See what would change" }).click();
  await page.locator("#preview").waitFor();
  await shot("preview");
  await go(page, `/p/${book.slug}/people?saved=added&n=Ada%20Admin&g=granted&cr=editor`); await shot("people");
  await go(page, `/c/${cosmos.slug}`); await shot("overview-reader");
  await go(page, "/compendiums?state=trashed");
  await page.getByRole("link", { name: "Delete Scratch World for good" }).click();
  await page.locator("dialog#dialog[open]").waitFor(); await new Promise((r) => setTimeout(r, 400));
  await shot("purge-dialog", { fullPage: false });
});
// A project with no primary Compendium.
const bare = (await api("POST", "/api/v1/projects", { title: "Hedgerow, second draft", kind: "short" })).item;
await each(async (page, shot) => { await go(page, `/p/${bare.slug}/compendium`); await shot("project-panel-none"); await go(page, `/p/${bare.slug}/people`); await shot("people-none"); });
// A project reader sees the page without the owner's controls.
{
  const ctx = await browser.newContext({ ...sizes.desktop, serviceWorkers: "block" });
  await ctx.addCookies(await rory.ctx.cookies());
  const page = await ctx.newPage();
  await go(page, `/p/${book.slug}/compendium`); await page.screenshot({ path: join(out, "compendium-project-panel-reader-desktop-light.png"), fullPage: true });
  await go(page, `/c/${salt.slug}`); await page.screenshot({ path: join(out, "compendium-overview-reader-role-desktop-light.png"), fullPage: true });
  await ctx.close();
}

// 3. Three other themes.
for (const [key, theme, scheme] of [["theme_light", "sepia", "light"], ["theme_dark", "slate", "dark"], ["theme_dark", "moss", "dark"]]) {
  await api("PUT", "/api/v1/me/prefs", { items: { [key]: theme } });
  const s = await session({ ...sizes.desktop, colorScheme: scheme });
  for (const [name, path] of [["overview", `/c/${salt.slug}`], ["project-panel", `/p/${book.slug}/compendium`], ["library", "/compendiums"]]) {
    await go(s.page, path);
    await s.page.evaluate(() => document.fonts.ready);
    await s.page.screenshot({ path: join(out, `compendium-${name}-desktop-${theme}.png`), fullPage: true });
    console.log("shot", name, theme);
  }
  await s.ctx.close();
}
await browser.close();
await srv.stop();
rm(tmp);
