#!/usr/bin/env node
// Screenshots of every theme for docs/screens/phase-2: the library, the writing view, Settings
// (with the picker), the palette and search, at 1280x800 and 390x844.
//
//   node scripts/screens-themes.mjs [--out docs/screens/phase-2] [--themes sepia,slate]
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const arg = (n, d) => (argv.includes(n) ? argv[argv.indexOf(n) + 1] : d);
const out = resolve(repo, arg("--out", "docs/screens/phase-2"));
const only = arg("--themes", "").split(",").filter(Boolean);
mkdirSync(out, { recursive: true });
const THEMES = [["paper", "light"], ["sepia", "light"], ["ink", "dark"], ["slate", "dark"], ["moss", "dark"]].filter(([n]) => !only.length || only.includes(n));
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-themeshots-");
const srv = await startServer(build(tmp), join(tmp, "data"));
const base = srv.base;
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const boot = await browser.newContext();
await signIn(await boot.newPage(), base);
const api = async (method, path, data) => {
  const r = await boot.request.fetch(base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return r.json().catch(() => null);
};
const chapter = [
  "# The harbour at low tide", "",
  "The tide had gone out and taken the light with it. By four o'clock the harbour was a bowl of black mud, and the boats lay over on their sides like animals that had surrendered.", "",
  "> Nobody on the quay said a word. They watched the gulls walk where the water had been.", "",
  "She had **always** known the sea would give things back, in its own time. A [[Lantern]] on the slip, a boot, once a *chair*. See [the chart](https://example.com/chart) for the soundings.", "",
  "%%Check the tide table for the date before this goes to a reader.%%", "",
  "- [x] Walk the quay", "- [ ] Find the lantern", "",
].join("\n");
const p = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000, cover_color: "#2F5D50", author_name: "Máire Nolan" })).item;
const d0 = (await api("GET", `/api/v1/projects/${p.id}/docs`)).order[0];
await api("PATCH", `/api/v1/docs/${d0}`, { base_revision: 1, title: "The harbour at low tide", content: chapter });
await api("POST", "/api/v1/projects", { title: "Notes on Quiet Software", kind: "essay", word_goal: 6000, cover_color: "#7A4B2A" });
await api("POST", "/api/v1/projects", { title: "Hedgerow", kind: "short", cover_color: "#6B3E4F" });
await api("POST", `/api/v1/projects/${p.id}/pin`, { pinned: true });
const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
for (const [name, scheme] of THEMES) {
  const items = scheme === "light" ? { theme: "light", theme_light: name } : { theme: "dark", theme_dark: name };
  await api("PUT", "/api/v1/me/prefs", { items });
  const cookies = await boot.cookies();
  for (const [size, opts] of Object.entries(sizes)) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    await ctx.addInitScript(() => sessionStorage.setItem("scriobh:tab", "themeshots"));
    const page = await ctx.newPage();
    const shot = async (what, o = {}) => { await page.screenshot({ path: join(out, `themes-${name}-${size}-${what}.png`), ...o }); console.log("shot", name, size, what); };
    const go = async (path) => { await page.goto(base + path, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); await sleep(300); };
    await go("/");
    await shot("library");
    await go(`/p/${p.slug}/d/${d0}`);
    await page.waitForSelector(".scriobh-prose");
    await page.locator(".scriobh-prose").click();
    await page.keyboard.press("ControlOrMeta+End");
    await sleep(200);
    await shot("write");
    await go("/settings");
    await shot("settings", { fullPage: size === "desktop" });
    await page.locator("#appearance").scrollIntoViewIfNeeded();
    await sleep(100);
    await shot("picker");
    await go(`/p/${p.slug}/search?q=tide`);
    await shot("search");
    await go("/");
    await page.keyboard.press("ControlOrMeta+k");
    await page.locator("#palette-input").waitFor();
    await sleep(350);
    await shot("palette");
    await ctx.close();
  }
}
await browser.close();
await srv.stop();
rm(tmp);
