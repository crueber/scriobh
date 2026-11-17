#!/usr/bin/env node
// Screenshots of the outline page for docs/screens/phase-2: the list, a row with its tools, the
// inline editor and the empty project. 1280x800 (ratio 1) and 390x844 (ratio 2), light and dark.
//
//   node scripts/screens-outline.mjs --out docs/screens/phase-2
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-2");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-outshots-");
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

const p = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000, author_name: "Máire Nolan" })).item;
const seeded = (await api("GET", `/api/v1/projects/${p.id}/docs`)).order[0];
const mk = async (b) => (await api("POST", `/api/v1/projects/${p.id}/docs`, b)).item.id;
await mk({ title: "placeholder", kind: "note" });
await api("POST", `/api/v1/docs/${seeded}/trash`);
const ids = {};
ids.ded = await mk({ title: "Dedication", kind: "front_matter", content: words(60), synopsis_short: "For the ones who stayed." });
ids.p1 = await mk({ title: "Part One: The Quay", kind: "part", synopsis_short: "The harbour, before the storm; Mara keeps the ledger and tells no one what it says." });
ids.c1 = await mk({ title: "Low Water", kind: "chapter", parent_id: ids.p1, content: words(5200), synopsis_short: "Mara counts the boats that did not come in." });
ids.s1 = await mk({ title: "The ledger", kind: "scene", parent_id: ids.c1, content: words(900), synopsis_short: "A careful reckoning, done in pencil." });
ids.c2 = await mk({ title: "The ferryman's daughter", kind: "chapter", parent_id: ids.p1, content: words(4100), synopsis_short: "Tobias refuses the crossing and will not say why." });
ids.c3 = await mk({ title: "A letter from the mainland", kind: "chapter", parent_id: ids.p1, content: words(3300), synopsis_short: "" });
ids.p2 = await mk({ title: "Part Two: The Salt Road", kind: "part", synopsis_short: "The walk inland." });
ids.c4 = await mk({ title: "Nobody walks it in winter", kind: "chapter", parent_id: ids.p2, content: words(6100), synopsis_short: "Two days on the causeway with the wrong boots." });
ids.c5 = await mk({ title: "A very long chapter title that goes on to test how the outline wraps a heading at narrow widths", kind: "chapter", parent_id: ids.p2, content: words(150), synopsis_short: "" });
ids.back = await mk({ title: "Acknowledgements", kind: "back_matter", content: words(220) });
for (const m of (await api("GET", `/api/v1/projects/${p.id}/docs`)).items) if (m.title === "placeholder") await api("POST", `/api/v1/docs/${m.id}/trash`);
const patch = async (id, b) => { const d = (await api("GET", `/api/v1/docs/${id}`)).item; await api("PATCH", `/api/v1/docs/${id}`, { base_revision: d.revision, ...b }); };
await patch(ids.ded, { status: "done" });
await patch(ids.p1, { target_words: 15000, status: "draft" });
await patch(ids.c1, { target_words: 6000, status: "revise" });
await patch(ids.s1, { status: "outline" });
await patch(ids.c2, { target_words: 4000, status: "draft" });
await patch(ids.c3, { target_words: 5000, status: "idea" });
await patch(ids.p2, { target_words: 20000, status: "outline" });
await patch(ids.c4, { target_words: 5000, status: "draft" });
await patch(ids.c5, { status: "idea" });
const empty = (await api("POST", "/api/v1/projects", { title: "Fresh Page", kind: "novel" })).item;
const eseed = (await api("GET", `/api/v1/projects/${empty.id}/docs`)).order[0];
await api("POST", `/api/v1/projects/${empty.id}/docs`, { title: "x", kind: "note" });
await api("POST", `/api/v1/docs/${eseed}/trash`);
for (const m of (await api("GET", `/api/v1/projects/${empty.id}/docs`)).items) await api("POST", `/api/v1/docs/${m.id}/trash`);
const cookies = await boot.cookies();

const sizes = { desktop: { viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const tag = `${size}-${scheme}`;
    const shot = async (name, o = {}) => { await page.screenshot({ path: join(out, `outline-${name}-${tag}.png`), ...o }); console.log("shot", name, tag); };
    await page.goto(`${base}/p/${p.slug}/outline`, { waitUntil: "load" });
    await page.evaluate(() => document.fonts.ready);
    await sleep(250);
    await shot("list", { fullPage: size === "desktop" });
    await page.locator(`#o-${ids.c1} .orow-syn`).click();
    await sleep(150);
    await page.locator(`#o-${ids.c1}`).scrollIntoViewIfNeeded();
    await shot("tools");
    await page.getByRole("button", { name: "Edit Low Water" }).click();
    await sleep(150);
    await page.locator(`#o-${ids.c1}`).scrollIntoViewIfNeeded();
    await shot("edit");
    await page.goto(`${base}/p/${empty.slug}/outline`, { waitUntil: "load" });
    await sleep(200);
    await shot("empty");
    await ctx.close();
  }
}
await browser.close();
await srv.stop();
rm(tmp);
