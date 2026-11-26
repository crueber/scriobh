#!/usr/bin/env node
// Screenshots of the entity bible for docs/screens/phase-3: every state, light and dark, at 1280 and 390 px, and
// a few with the other themes (Sepia, Slate, Moss) set in the preferences.
//
//   node scripts/screens-entities.mjs [--out docs/screens/phase-3] [--which list,sheet,more,merge,project] [--no-themes]
//
// The acceptance set of the denser entity pages is `--out docs/screens/entities-before --which list,sheet --no-themes` on the
// old pages and the same with entities-after on the new ones; both write numbers.json (the page's height and the rows on screen).
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { writeFileSync } from "node:fs";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";
import { seedBible, seedExtras } from "./entity-fixture.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-3");
const only = argv.includes("--which") ? argv[argv.indexOf("--which") + 1].split(",") : ["list", "more", "sheet", "states", "merge", "project"];
const themes = !argv.includes("--no-themes");
const numbers = {};
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-entshots-");
const bin = build(tmp);
const srv = await startServer(bin, join(tmp, "data"));
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const boot = await browser.newContext();
const bp = await boot.newPage();
await signIn(bp, srv.base);
const api = async (method, path, data) => {
  const r = await boot.request.fetch(srv.base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  const j = await r.json().catch(() => null);
  if (r.status() >= 300) throw new Error(`${method} ${path}: ${r.status()} ${JSON.stringify(j)}`);
  return j;
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// A bible worth looking at.
const bible = await seedBible(api);
const { comp, empty, maire, quay, tadhg, lantern } = bible;
const { dupA, gone, book } = await seedExtras(api, bible);
const bookDoc = (await api("GET", `/api/v1/projects/${book.id}/docs`)).order[0];
await api("PATCH", `/api/v1/docs/${bookDoc}`, { base_revision: 1, content: "Máire Nolan walked the length of the slip before it was light.\n\nTadhg Ó Néill was waiting at Salt Quay, as he always was.\n" });

const sizes = { "1280": { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, "390": { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
const cu = (p) => `${srv.base}/c/${comp.slug}${p}`;

async function states(page, size, tag, which) {
  const shot = async (name, o = {}) => { await page.evaluate(() => document.fonts.ready); await sleep(150); await page.screenshot({ path: join(out, `entities-${name}-${size}-${tag}.png`), fullPage: true, ...o }); console.log("shot", name, size, tag); };
  const go = async (u) => { await page.goto(u, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); };
  if (which.includes("list")) {
    await go(cu("/entities"));
    // The rows whose whole box is on the first screen.
    numbers[`list-${size}-${tag}`] = await page.evaluate(() => { const h = innerHeight; return { rows: [...document.querySelectorAll(".ent-row")].filter((r) => { const b = r.getBoundingClientRect(); return b.top >= 0 && b.bottom <= h; }).length, total: document.querySelectorAll(".ent-row").length, scrollHeight: document.documentElement.scrollHeight }; });
    await shot("list");
  }
  if (which.includes("more")) {
    await go(cu("/entities?q=ferry"));
    await shot("search");
    await go(`${srv.base}/c/${empty.slug}/entities`);
    await shot("empty");
    await go(cu("/entities?state=trashed"));
    await shot("trash");
    await go(cu("/fields"));
    await shot("fields");
  }
  if (which.includes("sheet")) {
    await go(cu("/e/" + maire.id));
    numbers[`sheet-${size}-${tag}`] = await page.evaluate(() => ({ scrollHeight: document.documentElement.scrollHeight, width: innerWidth }));
    await shot("sheet");
    await page.locator("#relations").scrollIntoViewIfNeeded();
    await shot("relations", { fullPage: false });
  }
  if (which.includes("states")) {
    await go(cu("/e/" + maire.id));
    await page.locator("#ea-alias").fill("the quay");
    await page.getByRole("button", { name: "Add the name" }).click();
    await page.locator("#ent-error").waitFor();
    await shot("collision");
    await go(cu("/e/" + gone.id));
    await shot("redirect");
    await go(cu("/e/" + lantern.id));
    await shot("sheet-trashed");
    // A stale save.
    await go(cu("/e/" + tadhg.id));
    await api("PATCH", `/api/v1/entities/${tadhg.id}`, { summary: "Harbour master, and nobody's friend." });
    await page.locator("#es-summary").fill("Harbour master and Máire's oldest rival.");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.locator("#ent-error").waitFor();
    await shot("conflict");
    await api("PATCH", `/api/v1/entities/${tadhg.id}`, { summary: "Harbour master, and Máire's rival." });
    // The preview.
    await go(cu("/e/" + quay.id));
    await page.getByRole("button", { name: "Preview" }).click();
    await page.locator("#es-prev").waitFor({ state: "visible" });
    await shot("preview");
  }
  if (which.includes("sidebar")) {
    // The writing view's Compendium tab, on the reference entry and on the list: a column at 1280 and a sheet at 390.
    await go(`${srv.base}/p/${book.slug}/d/${bookDoc}`);
    await page.waitForSelector(".scriobh-prose");
    await sleep(600);
    if (size === "390") await page.locator("#insp-open").click();
    for (const [name, want] of [["sidebar-list", "{}"], ["sidebar-entry", `{ entity: ${JSON.stringify(maire.id)} }`], ["sidebar-book", `{ entity: ${JSON.stringify(maire.id)} }`]]) {
      await page.evaluate(`window.scriobh.lookupWant = ${want}; window.scriobh.inspector.open("compendium", false)`);
      await page.waitForSelector(name === "sidebar-list" ? "#lk-q" : "#lk-entry");
      if (name === "sidebar-book") { await page.getByRole("button", { name: "This book", exact: true }).click(); await page.locator("#ov-name").waitFor(); }
      await sleep(400);
      await shot(name, { fullPage: false });
    }
  }
  if (which.includes("merge")) {
    await go(cu("/e/" + dupA.id + "/merge?q=ma"));
    await shot("merge-pick");
    await go(cu("/e/" + dupA.id + "/merge?into=" + maire.id));
    await shot("merge-preview");
  }
  if (which.includes("project")) {
    await go(`${srv.base}/p/${book.slug}/entities`);
    await shot("project-list");
    await go(`${srv.base}/p/${book.slug}/e/${maire.id}`);
    await shot("project-sheet");
  }
}

for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(await boot.cookies());
    const page = await ctx.newPage();
    await states(page, size, scheme, only);
    await ctx.close();
  }
}
// The other themes, at both sizes.
for (const [name, scheme] of themes ? [["sepia", "light"], ["slate", "dark"], ["moss", "dark"]] : []) {
  await api("PUT", "/api/v1/me/prefs", { items: scheme === "light" ? { theme: "light", theme_light: name } : { theme: "dark", theme_dark: name } });
  for (const [size, opts] of Object.entries(sizes)) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(await boot.cookies());
    const page = await ctx.newPage();
    await states(page, size, name, only.filter((w) => w !== "more" && w !== "states" && w !== "project"));
    await ctx.close();
  }
}
await browser.close();
await srv.stop();
rm(tmp);
writeFileSync(join(out, "numbers.json"), JSON.stringify(numbers, null, 2) + "\n");
console.log(numbers);
