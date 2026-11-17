#!/usr/bin/env node
// Screenshots of the stage 2 screens for docs/screens/phase-1: the library and its views,
// the project menu and the typed-title delete, settings, the palette and the switcher.
// 1280x800 (device pixel ratio 1) and 390x844 (ratio 2), light and dark.
//
//   node scripts/screens-library.mjs --out docs/screens/phase-1
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-1");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-libshots-");
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
const words = (n) => ("the tide came in over the grey stones and the gulls wheeled ".repeat(Math.ceil(n / 11))).split(" ").slice(0, n).join(" ") + "\n";
const colours = ["#2F5D50", "#7A4B2A", "#3B5B7A", "#6B3E4F", "#4B6B3A", "#5A4E7A", "#8A6420", "#4A4740"];
const specs = [
  ["The Salt Road", "novel", 80000, 54300, 0, "Máire Nolan"], ["Notes on Quiet Software", "essay", 6000, 2150, 1, "Máire Nolan"],
  ["The Winter Letters", "novella", 30000, 21800, 2, ""], ["Hedgerow", "short", 0, 3900, 3, "Ciara Ní Bhriain"],
  ["A Short History of Tides, Harbours, and the People Who Waited for Them", "nonfiction", 90000, 12400, 4, "C. Rueber"], ["Field Notes", "collection", 0, 0, 5, ""],
  ["Old Draft", "novel", 0, 800, 6, ""], ["Abandoned Plan", "report", 0, 120, 7, ""],
];
const made = {};
for (const [title, kind, goal, wc, ci, author] of specs) {
  const p = (await api("POST", "/api/v1/projects", { title, kind, word_goal: goal, cover_color: colours[ci], author_name: author })).item;
  if (wc) {
    const d = (await api("GET", `/api/v1/projects/${p.id}/docs`)).order[0];
    await api("PATCH", `/api/v1/docs/${d}`, { base_revision: 1, content: words(Math.min(wc, 20000)) });
  }
  made[title] = p;
}
await api("POST", `/api/v1/projects/${made["The Salt Road"].id}/pin`, { pinned: true });
await api("POST", `/api/v1/projects/${made["Hedgerow"].id}/pin`, { pinned: true });
await api("POST", `/api/v1/projects/${made["The Winter Letters"].id}/open`, {});
await api("POST", `/api/v1/projects/${made["Old Draft"].id}/archive`);
await api("POST", `/api/v1/projects/${made["Abandoned Plan"].id}/trash`);
const cookies = await boot.cookies();

const shot = async (page, name, o = {}) => { await page.screenshot({ path: join(out, name + ".png"), ...o }); console.log("shot", name); };
const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const tag = `${size}-${scheme}`;
    const go = async (path) => { await page.goto(base + path, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); await sleep(250); };
    await go("/");
    await shot(page, `library-${tag}`);
    {
      await go("/?state=archived");
      await shot(page, `library-archived-${tag}`);
      await go("/?state=trashed");
      await shot(page, `library-trash-${tag}`);
      await page.getByRole("link", { name: "Delete forever" }).click();
      await page.locator("#pp-title").fill("Abandoned");
      await sleep(300);
      await shot(page, `purge-dialog-${tag}`);
      await page.keyboard.press("Escape");
      await go("/");
      await page.getByRole("link", { name: "Actions for Hedgerow" }).click();
      await page.locator("dialog#dialog[open]").waitFor();
      await sleep(400);
      await shot(page, `project-menu-${tag}`);
      await page.keyboard.press("Escape");
    }
    await go("/settings");
    await shot(page, `settings-${tag}`, size === "mobile" ? {} : { fullPage: true });
    await go("/");
    await page.keyboard.press("ControlOrMeta+k");
    await page.locator("#palette-input").waitFor();
    await sleep(350);
    await shot(page, `palette-${tag}`);
    await page.keyboard.type("set");
    await sleep(200);
    await shot(page, `palette-typing-${tag}`);
    await page.keyboard.press("Escape");
    {
      await page.keyboard.press("ControlOrMeta+o");
      await page.locator("#palette-input").waitFor();
      await sleep(600);
      await shot(page, `switcher-${tag}`);
    }
    await ctx.close();
  }
}
// Search, export, split, focus mode and the empty views.
const salt = made["The Salt Road"];
const saltDoc = (await api("GET", `/api/v1/projects/${salt.id}/docs`)).order[0];
const long = (await api("POST", `/api/v1/projects/${salt.id}/docs`, { title: "The Whole Winter", content: Array.from({ length: 25300 }, (_, i) => (i % 40 === 39 ? "tide\n\ntide" : "tide")).join(" ") })).item;
const para = (await api("POST", `/api/v1/projects/${salt.id}/docs`, { title: "Low Water", content: Array.from({ length: 14 }, (_, i) => `Paragraph ${i + 1}. The tide had gone out and taken the light with it, and the boats lay over on their sides like animals that had surrendered.`).join("\n\n") })).item;
for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    await ctx.addInitScript(() => sessionStorage.setItem("scriobh:tab", "libshots"));
    const page = await ctx.newPage();
    const tag = `${size}-${scheme}`;
    const go = async (path) => { await page.goto(base + path, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); await sleep(300); };
    await go(`/p/${salt.slug}/search?q=gulls`);
    await shot(page, `search-${tag}`);
    {
      await go(`/search?q=tide`);
      await shot(page, `search-all-${tag}`);
      await go(`/p/${salt.slug}/search?q=zzzzqq`);
      await shot(page, `search-none-${tag}`);
      await go("/");
      await page.getByRole("link", { name: "Actions for The Salt Road" }).click();
      await page.locator("dialog#dialog[open]").waitFor();
      await sleep(300); // htmx settles the dialog's content
      await page.getByRole("link", { name: /Export as Markdown/ }).click();
      await page.locator("#project-export-form").waitFor();
      await sleep(300);
      await shot(page, `export-dialog-${tag}`);
      await page.keyboard.press("Escape");
      // The read-only chapter and the way out of it.
      await go(`/p/${salt.slug}/d/${long.id}`);
      await page.waitForSelector(".scriobh-prose");
      await page.locator("#nb-guard").getByRole("button", { name: "Split at cursor" }).click();
      await page.locator(".scriobh-source").evaluate((t) => { const n = Math.floor(t.value.length / 2); t.focus(); t.setSelectionRange(n, n); });
      await sleep(300);
      await shot(page, `split-guard-${tag}`);
    }
    {
      await api("PUT", "/api/v1/me/prefs", { items: { focus_dim: "true" } });
      await ctx.clearCookies();
      await ctx.addCookies(await boot.cookies());
      await go(`/p/${salt.slug}/d/${para.id}`);
      await page.locator(".scriobh-prose p").nth(4).click();
      await sleep(300);
      await shot(page, `focus-dim-${tag}`);
      await api("PUT", "/api/v1/me/prefs", { items: { focus_dim: "false" } });
    }
    await ctx.close();
  }
}
{
  const ctx = await browser.newContext({ ...sizes.desktop, serviceWorkers: "block" });
  const page = await ctx.newPage();
  await signIn(page, base, "reader@dev.scriobh");
  for (const [state, name] of [["archived", "archived-empty"], ["trashed", "trash-empty"]]) {
    await page.goto(base + `/?state=${state}`, { waitUntil: "load" });
    await sleep(250);
    await shot(page, `${name}-desktop-light`);
  }
  await ctx.close();
  const m = await browser.newContext({ ...sizes.mobile, colorScheme: "dark", serviceWorkers: "block" });
  const mp = await m.newPage();
  await signIn(mp, base, "reader@dev.scriobh");
  await mp.goto(base + "/?state=archived", { waitUntil: "load" });
  await sleep(250);
  await shot(mp, "archived-empty-mobile-dark");
  await m.close();
}
// The settings page is the one place the theme is chosen: show the dark choice made and saved.
await browser.close();
await srv.stop();
rm(tmp);
