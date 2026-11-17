#!/usr/bin/env node
// Screenshots of the entity bible for docs/screens/phase-3: every state, light and dark, at 1280 and 390 px, and
// a few with the other themes (Sepia, Slate, Moss) set in the preferences.
//
//   node scripts/screens-entities.mjs [--out docs/screens/phase-3]
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-3");
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
const comp = (await api("POST", "/api/v1/compendiums", { name: "The Salt Road Bible", kind: "fiction", description: "Everything the Salt Road books agree on." })).item;
const empty = (await api("POST", "/api/v1/compendiums", { name: "Hedgerow Notes" })).item;
const mk = async (kind, name, extra = {}) => (await api("POST", `/api/v1/compendiums/${comp.id}/entities`, { kind, name, ...extra })).item;
await api("PUT", `/api/v1/compendiums/${comp.id}/entity-fields`, { kind: "character", key: "fare", label: "Ferry fare", type: "number" });
await api("PUT", `/api/v1/compendiums/${comp.id}/entity-fields`, { kind: "character", key: "side", label: "Allegiance", type: "choice", choices: ["Crown", "Quay", "Neither"] });
await api("PUT", `/api/v1/compendiums/${comp.id}/entity-fields`, { kind: "character", key: "born", label: "Born", type: "date" });
const maire = await mk("character", "Máire Nolan", {
  sort_name: "Nolan, Máire", summary: "A ferrywoman with a long memory and a short temper.", color: "#2F5D50", aliases: ["The Ferrywoman", "Mam"],
  fields: { fare: "3", side: "Quay", born: "1790-03", age: "34", role: "Ferry keeper" },
  body: "She has kept the **Salt Quay** ferry since the flood took her father.\n\n## What she knows\n\n- Every tide of the estuary, by smell.\n- Who owes the Guild, and how much.\n\n> The sea gives things back, in its own time.\n",
});
const quay = await mk("place", "Salt Quay", { summary: "Where the road meets the water.", color: "#4A6FA5", aliases: ["The Quay"], body: "The last stone quay before the marsh." });
const tadhg = await mk("character", "Tadhg Ó Néill", { summary: "Harbour master, and Máire's rival.", color: "#7A4B2A", fields: { side: "Crown", fare: "5" } });
const lantern = await mk("item", "The brass lantern", { summary: "Found on the slip after the storm." });
const guild = await mk("org", "The Lamplighters' Guild", { summary: "Keeps the lights from Salt Quay to the bar.", aliases: ["The Guild"] });
await mk("concept", "The crossing", { summary: "What the ferry is for, and what it costs." });
for (const [i, n] of ["Cathal Brennan", "Sorcha Quinn", "Donal Fox", "Orla Byrne", "Colm Tobin", "Eithne Moran"].entries()) await mk("character", n, { summary: `A name on the quay, ${i + 1}.` });
for (const n of ["Glen Hollow", "Marsh Cross", "Ridge Lough"]) await mk("place", n, { summary: "A day's walk from the quay." });
await api("POST", `/api/v1/entities/${maire.id}/relations`, { to_entity_id: quay.id, kind: "custom", label: "keeps the ferry at", inverse_label: "ferry kept by" });
await api("POST", `/api/v1/entities/${maire.id}/relations`, { to_entity_id: tadhg.id, kind: "rival", symmetric: true });
await api("POST", `/api/v1/entities/${maire.id}/relations`, { to_entity_id: guild.id, kind: "member", label: "owes money to", notes: "Since the flood." });
await api("POST", `/api/v1/entities/${lantern.id}/relations`, { to_entity_id: maire.id, kind: "custom", label: "was found by", inverse_label: "found" });
// Two near-duplicates to merge, with a field conflict and a project that changed both.
const dupA = await mk("character", "Maire N.", { summary: "A second note on the ferrywoman.", fields: { fare: "4", side: "Quay" }, aliases: ["Mairín"] });
const gone = await mk("character", "Old Tadhg", { summary: "Merged long ago." });
const book = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan", word_goal: 90000 })).item;
await api("PUT", `/api/v1/projects/${book.id}/compendium`, { compendium_id: comp.id });
await api("PUT", `/api/v1/projects/${book.id}/entity-overrides`, { entity_id: maire.id, name: "Mairín Nolan", fields: { fare: "4" }, notes: "In book two she is older, and the fare has gone up." });
await api("PUT", `/api/v1/projects/${book.id}/entity-overrides`, { entity_id: dupA.id, summary: "This book's note on her." });
await api("POST", `/api/v1/entities/${gone.id}/merge`, { into_id: tadhg.id });
await api("POST", `/api/v1/entities/${lantern.id}/trash`, {});

const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
const cu = (p) => `${srv.base}/c/${comp.slug}${p}`;

async function states(page, size, tag, which) {
  const shot = async (name, o = {}) => { await page.evaluate(() => document.fonts.ready); await sleep(150); await page.screenshot({ path: join(out, `entities-${name}-${size}-${tag}.png`), fullPage: true, ...o }); console.log("shot", name, size, tag); };
  const go = async (u) => { await page.goto(u, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); };
  if (which.includes("list")) {
    await go(cu("/entities"));
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
    await shot("sheet");
    await page.locator("#relations").scrollIntoViewIfNeeded();
    await shot("relations", { fullPage: false });
    await go(cu("/e/" + maire.id));
    await page.locator("#ea-alias").fill("the quay");
    await page.getByRole("button", { name: "Add the alias" }).click();
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
    await states(page, size, scheme, ["list", "more", "sheet", "merge", "project"]);
    await ctx.close();
  }
}
// The other themes, at both sizes.
for (const [name, scheme] of [["sepia", "light"], ["slate", "dark"], ["moss", "dark"]]) {
  await api("PUT", "/api/v1/me/prefs", { items: scheme === "light" ? { theme: "light", theme_light: name } : { theme: "dark", theme_dark: name } });
  for (const [size, opts] of Object.entries(sizes)) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(await boot.cookies());
    const page = await ctx.newPage();
    await states(page, size, name, ["list", "sheet", "merge"]);
    await ctx.close();
  }
}
await browser.close();
await srv.stop();
rm(tmp);
