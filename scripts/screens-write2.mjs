#!/usr/bin/env node
// Screenshots of the Phase 2 writing view for docs/screens/phase-2: the inspector (details and links),
// the Versions panel and a comparison, the wiki-link list and card, front and back matter in the tree,
// and the status bar's today figure. 1280x800 and 390x844, light and dark.
//
//   node scripts/screens-write2.mjs [--out docs/screens/phase-2]
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-2");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-w2shots-");
const bin = build(tmp);
const dataDir = join(tmp, "data");
const now = new Date();
const yesterdayNoon = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 12, 0, 0);
const offset = Math.round((yesterdayNoon.getTime() - now.getTime()) / 1000);
const env = { SCRIOBH_SESSION_HOURS: "400", SCRIOBH_SESSION_IDLE_MINUTES: "100000" };
let srv = await startServer(bin, dataDir, { ...env, SCRIOBH_DEV_CLOCK_OFFSET_SECONDS: String(offset) });
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const boot = await browser.newContext();
const bp = await boot.newPage();
await signIn(bp, srv.base);
const api = async (method, path, data) => {
  const r = await boot.request.fetch(srv.base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return { status: r.status(), json: await r.json().catch(() => null) };
};
const para = (s) => s + "\n\n";
const V1 = para("The harbour was quiet at dawn. Marta counted the boats twice, because the first count had come out wrong.") + para("Nobody had slept. The lamp on the quay burned on, and the gulls sat along the wall like a jury.");
const V2 = para("The harbour was quiet at dawn, and the tide had gone out. Marta counted the boats twice, then a third time.") + para("Nobody had slept. The lamp on the quay burned on, and the gulls sat along the wall like a jury.") + para("Then she saw the light, low and green, where no boat should be.");
const V3 = para("The harbour was quiet at dawn, and the tide had gone out. Marta counted the boats twice, then a third time, and wrote the number on her hand.") + para("Nobody had slept. The lamp on the quay burned on, and the gulls sat along the wall like a jury. See [[The Lighthouse]] and [[Marta Voss]].") + para("Then she saw the light, low and green, where no boat should be. [[Old Pier]] was a mile away.");

const book = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000 })).json.item;
const first = (await api("GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
const mk = async (body) => (await api("POST", `/api/v1/projects/${book.id}/docs`, body)).json.item.id;
const lighthouse = await mk({ title: "The Lighthouse", kind: "note", content: "Built in 1861. Marta's grandfather kept the lamp." });
await mk({ title: "Marta Voss", kind: "note", content: "Harbour pilot. Counts everything twice." });
let rev = (await api("GET", `/api/v1/docs/${first}`)).json.item.revision;
await api("PATCH", `/api/v1/docs/${first}`, { base_revision: rev, title: "The Quiet Harbour", content: V1, synopsis_short: "Marta counts the boats and finds one too many." });
await api("POST", `/api/v1/docs/${first}/revisions`, { label: "First draft" });
rev = (await api("GET", `/api/v1/docs/${first}`)).json.item.revision;
await api("PATCH", `/api/v1/docs/${first}`, { base_revision: rev, content: V2, synopsis_long: "Opens on the quay. The green light is the first sign that the story is not about boats.", status: "draft", target_words: 1800, pov: "Marta", location: "The quay", story_date: "1923-04-02" });
await api("POST", `/api/v1/docs/${first}/revisions`, { label: "After the editor call" });
await api("POST", `/api/v1/projects/${book.id}/docs`, { title: "Title page", kind: "front_matter", after: "", content: "THE SALT ROAD\n\nA novel." });
await api("POST", `/api/v1/projects/${book.id}/docs`, { title: "Acknowledgements", kind: "back_matter", content: "Thanks to the harbour." });
await api("PUT", `/api/v1/projects/${book.id}/goals`, { kind: "daily", target_words: 500 });
await bp.close();
await boot.close();
await srv.stop();
srv = await startServer(bin, dataDir, env);
const ctx0 = await browser.newContext();
const p0 = await ctx0.newPage();
await signIn(p0, srv.base);
const cookies = await ctx0.cookies();
const api2 = async (method, path, data) => {
  const r = await ctx0.request.fetch(srv.base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return { status: r.status(), json: await r.json().catch(() => null) };
};
rev = (await api2("GET", `/api/v1/docs/${first}`)).json.item.revision;
await api2("PATCH", `/api/v1/docs/${first}`, { base_revision: rev, content: V3 });
await ctx0.close();

const shot = async (page, name) => { await page.screenshot({ path: join(out, name + ".png") }); console.log("shot", name); };
const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const tag = `${size}-${scheme}`;
    const load = async () => {
      await page.goto(`${srv.base}/p/${book.slug}/d/${first}`, { waitUntil: "load" });
      await page.waitForSelector(".scriobh-prose");
      await page.evaluate(() => document.fonts.ready);
      await page.waitForFunction(() => window.scriobh && window.scriobh.inspector && window.scriobh.wiki, null, { timeout: 10000 });
      await sleep(900);
    };
    const tap = async (loc) => (size === "mobile" ? loc.tap() : loc.click());
    await page.goto(srv.base + "/");
    await page.evaluate(() => { document.cookie = "scriobh_insp=; max-age=0; path=/"; });
    await load();
    await shot(page, `write-today-${tag}`);
    // details and links
    await tap(page.getByRole("button", { name: "Sidebar", exact: true }));
    await tap(page.getByRole("tab", { name: "Details", exact: true }));
    await sleep(900);
    await shot(page, `inspector-details-${tag}`);
    await tap(page.getByRole("button", { name: "Sidebar", exact: true }));
    // versions
    await tap(page.getByRole("button", { name: "Versions", exact: true }));
    await page.getByRole("button", { name: /^Yesterday/ }).waitFor();
    await tap(page.getByRole("button", { name: /^Yesterday/ }));
    await sleep(400);
    await shot(page, `versions-${tag}`);
    await tap(page.getByRole("button", { name: /^Compare the version/ }).first());
    await page.locator(".ver-diff").waitFor();
    await sleep(300);
    await shot(page, `versions-compare-${tag}`);
    await tap(page.getByRole("button", { name: "Versions", exact: true }));
    // wiki list and card
    await load();
    await page.locator(".scriobh-prose").click();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.type(" The [[Ma");
    await page.locator("#wiki-list").waitFor({ state: "visible" });
    await sleep(250);
    await shot(page, `wiki-list-${tag}`);
    await page.keyboard.press("Escape");
    // Put the chapter back as it was and forget the unsaved typing, so the next shots show the same page.
    await page.waitForFunction(() => !window.scriobh.write.unsaved(), null, { timeout: 6000 }).catch(() => {});
    await page.evaluate(() => { Object.keys(localStorage).filter((k) => /^scriobh:(draft|held):/.test(k)).forEach((k) => localStorage.removeItem(k)); });
    const cur = (await (await ctx.request.get(`${srv.base}/api/v1/docs/${first}`)).json()).item;
    await ctx.request.patch(`${srv.base}/api/v1/docs/${first}`, { data: { base_revision: cur.revision, content: V3 } });
    await load();
    await tap(page.locator(".wikilink-missing").first());
    await page.locator("#wiki-card").waitFor({ state: "visible" });
    await sleep(250);
    await shot(page, `wiki-missing-${tag}`);
    // front and back matter in the tree
    await load();
    if (size === "mobile") { await page.locator("#tree-toggle").tap(); await sleep(400); }
    await shot(page, `tree-matter-${tag}`);
    if (size === "desktop") {
      await page.locator("#add-btn").click();
      await sleep(250);
      await shot(page, `add-menu-${tag}`);
    }
    await ctx.close();
  }
}
await browser.close();
await srv.stop();
await sleep(1500);
rm(tmp);
process.exit(0);
