#!/usr/bin/env node
// Screenshots of the cover page for docs/screens/covers-c1: the cover page of a book with a page-mode front and an image
// back, the page of a wrap with its drawing, the draft after a picture was added, a refusal, the writing view's tree with
// the Cover row, and the library tile and shelf, at 1280x800 (ratio 1) and 390x844 (ratio 2), light and dark.
//
//   node scripts/screens-covers.mjs --out docs/screens/covers-c1
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";
import { harbourPng, wrapPng, flatPng } from "./covers-pictures.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/covers-c1");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-covershots-");
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
const up = async (pid, part, png, q = "") => (await (await boot.request.fetch(`${base}/api/v1/projects/${pid}/cover-layout/images?part=${part}${q}`, { method: "POST", data: png, headers: { "Content-Type": "image/png" } })).json()).attachment_id;

// A tall flat picture for the front: dusk over a harbour. A second, wide one, to show the page keeping all of it.
const wideBoat = flatPng(1800, 1100, (x, y) => { const u = x / 1800, v = y / 1100; if ((u - 0.7) ** 2 * 2.7 + (v - 0.45) ** 2 < 0.02) return [226, 176, 74]; if (v > 0.62) return [59, 91, 122]; return [232, 217, 204]; });
const book = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan", word_goal: 80000, cover_color: "#3B5B7A" })).item;
const front = await up(book.id, "front", wideBoat), back = await up(book.id, "back", harbourPng(900, 1350));
const lay = await api("PUT", `/api/v1/projects/${book.id}/cover-layout`, { revision: 0, shape: "sides", trim: "6x9", alt: "A harbour at dusk, a small boat and a low sun",
  front: { mode: "page", attachment: front, fit: "contain", scale: 85, position: "top", background: "#3B5B7A", margin_in: 0.5 }, back: { mode: "image", attachment: back } });
if (!lay || !lay.shape) throw new Error("layout: " + JSON.stringify(lay));
const plain = (await api("POST", "/api/v1/projects", { title: "Fresh Page", kind: "novel" })).item;
const other = (await api("POST", "/api/v1/projects", { title: "Winter Ferry", kind: "novella", cover_color: "#7A4B2A" })).item;
const oa = await up(other.id, "front", harbourPng(1200, 1800));
await api("PUT", `/api/v1/projects/${other.id}/cover-layout`, { revision: 0, shape: "sides", alt: "A harbour at dusk", front: { mode: "image", attachment: oa } });
const wb = (await api("POST", "/api/v1/projects", { title: "Wrapped", kind: "novel" })).item;
const w = wrapPng();
const wa = await up(wb.id, "wrap", w.buffer, "&trim=6x9&bleed_in=0.125");
await api("PUT", `/api/v1/projects/${wb.id}/cover-layout`, { revision: 0, shape: "wrap", trim: "6x9", alt: "A wrap in dusk colours", wrap: { attachment: wa, bleed_in: 0.125 } });
const d0 = (await api("GET", `/api/v1/projects/${book.id}/docs`)).order[0];
const rev0 = (await api("GET", `/api/v1/docs/${d0}`)).item.revision;
await api("PATCH", `/api/v1/docs/${d0}`, { base_revision: rev0, title: "The harbour at low tide", content: "The tide had gone out and taken the light with it.\n\nBy four o'clock the harbour was a bowl of black mud.\n" });
await api("POST", `/api/v1/projects/${book.id}/docs`, { title: "The quay", kind: "chapter", content: "She counted the boats twice.\n" });

const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
async function run(scheme, themePrefs, tagPrefix) {
  await api("PUT", "/api/v1/me/prefs", { items: themePrefs });
  const cookies = await boot.cookies();
  for (const [size, opts] of Object.entries(sizes)) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const tag = `${size}-${tagPrefix}`;
    const shot = async (name, o = {}) => { if (o.fullPage) { await page.evaluate(() => window.scrollTo(0, 0)); await sleep(200); } await page.screenshot({ path: join(out, `cover-${name}-${tag}.png`), ...o }); console.log("shot", name, tag); };
    const go = async (path) => { await page.goto(base + path, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); await page.waitForFunction(() => [...document.images].every((i) => i.complete)); await sleep(300); };
    await go(`/p/${book.slug}/cover`);
    await shot("page", { fullPage: size === "desktop" });
    await go(`/p/${wb.slug}/cover`);
    await page.locator(".cv-art").scrollIntoViewIfNeeded();
    await sleep(200);
    await shot("wrap", { fullPage: size === "desktop" });
    // the draft after a picture was added, and a refusal
    await go(`/p/${plain.slug}/cover`);
    await page.locator("#cv-shape-sides").check();
    await page.locator("#cv-front-mode input[value=page]").check();
    const nav = page.waitForNavigation();
    await page.locator("#cv-front-file").setInputFiles({ name: "front.png", mimeType: "image/png", buffer: wideBoat });
    await nav;
    await page.locator("#cv-front-scale").fill("70");
    await page.locator("input[name=front_pos][value=bottom]").check();
    await sleep(300);
    await shot("draft", { fullPage: size === "desktop" });
    await page.locator("#cv-front-mode input[value=image]").check();
    const nav2 = page.waitForNavigation();
    await page.getByRole("button", { name: "Save the cover" }).click();
    await nav2;
    await page.locator("#cv-msg").scrollIntoViewIfNeeded();
    await sleep(200);
    await shot("refused", { fullPage: size === "desktop" });
    // a stale save: the writer's version stays in the form, the saved one is described beside it
    await go(`/p/${other.slug}/cover`);
    await page.locator("#cv-alt").fill("A harbour, a small boat, a low sun, and a gull");
    const cur = (await api("GET", `/api/v1/projects/${other.id}/cover-layout`)).layout;
    await api("PUT", `/api/v1/projects/${other.id}/cover-layout`, { revision: cur.revision, shape: "sides", trim: cur.trim, alt: "A harbour at dusk, as the other editor describes it", front: { mode: "image", attachment: cur.front.attachment } });
    const nav3 = page.waitForNavigation();
    await page.getByRole("button", { name: "Save the cover" }).click();
    await nav3;
    await sleep(200);
    await shot("conflict", { fullPage: size === "desktop" });
    // the tree, the library
    await go(`/p/${book.slug}/d/${d0}`);
    if (size === "mobile") { await page.locator("#tree-toggle").click(); await sleep(400); }
    await shot("tree");
    await go(`/p/${plain.slug}`);
    if (size === "mobile") { await page.locator("#tree-toggle").click(); await sleep(400); }
    await shot("tree-generated");
    await go("/");
    await shot("library");
    await ctx.close();
  }
}
await run("light", { theme: "light", theme_light: "paper" }, "light");
await run("dark", { theme: "dark", theme_dark: "ink" }, "dark");
await browser.close();
await srv.stop();
rm(tmp);
