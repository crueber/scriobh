#!/usr/bin/env node
// Screenshots of what the Phase 3 review changed, for docs/screens/phase-3: the banners of a Compendium in the trash
// and of an archived one, the scene tab after its spacing was fixed, and an entity sheet with one-line fields and its
// Save kept in reach. Only desktop light, desktop dark and mobile light, as the screenshots rule says.
//
//   node scripts/screens-review3.mjs --out docs/screens/phase-3
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-3");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-review3shots-");
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
const p = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel" })).item;
const c = (await api("POST", "/api/v1/compendiums", { name: "The Salt Road bible", project_id: p.id })).item;
const ent = async (cid, kind, name, summary, fields) => (await api("POST", `/api/v1/compendiums/${cid}/entities`, { kind, name, summary, fields })).item;
const mara = await ent(c.id, "character", "Mara Voss", "The harbour clerk who keeps the ledger.", { role: "Harbour clerk", age: "34", appearance: "Tall, with ink on her cuffs and a way of standing very still when she is counting." });
await ent(c.id, "place", "Saltmarsh Quay", "A grey quay.");
const doc = (await api("GET", `/api/v1/projects/${p.id}/docs`)).order[0];
const cur = (await api("GET", `/api/v1/docs/${doc}`)).item;
await api("PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "The ledger", content: "Mara Voss carried the lamp down to Saltmarsh Quay.\n" });
const old = (await api("POST", "/api/v1/compendiums", { name: "The old bible" })).item;
await ent(old.id, "character", "Old Hand", "Left in the trash.");
const shelf = (await api("POST", "/api/v1/compendiums", { name: "The shelved bible" })).item;
await ent(shelf.id, "character", "Shelf Hand", "Kept on the shelf.");
await api("POST", `/api/v1/compendiums/${old.id}/trash`);
await api("POST", `/api/v1/compendiums/${shelf.id}/archive`);
await sleep(1500);
const cookies = await boot.cookies();

const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
async function run(size, scheme) {
  const ctx = await browser.newContext({ ...sizes[size], colorScheme: scheme, serviceWorkers: "block" });
  await ctx.addCookies(cookies);
  const page = await ctx.newPage();
  const tag = `${size}-${scheme}`;
  const shot = async (name, o = {}) => { await page.screenshot({ path: join(out, `review3-${name}-${tag}.png`), ...o }); console.log("shot", name, tag); };
  const go = async (path, sel) => { await page.goto(base + path, { waitUntil: "load" }); await page.waitForSelector(sel); await page.evaluate(() => document.fonts.ready); await sleep(150); };
  await go(`/c/${old.slug}/entities`, ".comp-banner");
  await shot("trashed");
  await go(`/c/${shelf.slug}/entities`, ".comp-banner");
  await shot("archived");
  await go(`/c/${c.slug}/e/${mara.id}`, "#ef-role");
  await shot("sheet");
  await go(`/p/${p.slug}/d/${doc}`, ".scriobh-prose .mention");
  await page.evaluate(() => window.scriobh.inspector.open("scene", false));
  await page.locator("#ipanel-scene .sc-name").first().waitFor();
  await sleep(250);
  await shot("scene-tab");
  await ctx.close();
}
await run("desktop", "light");
await run("desktop", "dark");
await run("mobile", "light");
await browser.close();
await srv.stop();
