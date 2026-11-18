#!/usr/bin/env node
// Screenshots of the Compile page's paperback cover for docs/screens/covers-c2: a book with a page-mode front and an image back
// measured for KDP, and a wrap whose spine does not fit the page count (the book's folds drawn beside the picture's), at
// 1280x1500 (ratio 1) and 390x844 (ratio 2), light and dark. Judge them from the pictures, not from the CSS.
//
//   node scripts/screens-compile-cover.mjs --out /tmp/shots
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";
import { harbourPng, wrapPng, flatPng } from "./covers-pictures.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/covers-c2");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-cmpcover-");
const srv = await startServer(build(tmp), join(tmp, "data"));
const base = srv.base;
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const boot = await browser.newContext();
await signIn(await boot.newPage(), base);
const api = async (method, path, data) => { const r = await boot.request.fetch(base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} }); return r.json().catch(() => null); };
const up = async (pid, part, png, q = "") => (await (await boot.request.fetch(`${base}/api/v1/projects/${pid}/cover-layout/images?part=${part}${q}`, { method: "POST", data: png, headers: { "Content-Type": "image/png" } })).json()).attachment_id;

const wideBoat = flatPng(1800, 1100, (x, y) => { const u = x / 1800, v = y / 1100; if ((u - 0.7) ** 2 * 2.7 + (v - 0.45) ** 2 < 0.02) return [226, 176, 74]; if (v > 0.62) return [59, 91, 122]; return [232, 217, 204]; });
const sides = (await api("POST", "/api/v1/projects", { title: "The Salt Road", subtitle: "A novel", kind: "novel", author_name: "Máire Nolan", cover_color: "#3B5B7A" })).item;
const front = await up(sides.id, "front", wideBoat), back = await up(sides.id, "back", harbourPng(900, 1350));
await api("PUT", `/api/v1/projects/${sides.id}/cover-layout`, { revision: 0, shape: "sides", trim: "6x9", alt: "A harbour at dusk, a small boat and a low sun",
  front: { mode: "page", attachment: front, fit: "contain", scale: 85, position: "top", background: "#3B5B7A", margin_in: 0.5 }, back: { mode: "image", attachment: back } });
const wb = (await api("POST", "/api/v1/projects", { title: "Wrapped", kind: "novel" })).item;
const wa = await up(wb.id, "wrap", wrapPng().buffer, "&trim=6x9&bleed_in=0.125");
await api("PUT", `/api/v1/projects/${wb.id}/cover-layout`, { revision: 0, shape: "wrap", trim: "6x9", alt: "A wrap in dusk colours", wrap: { attachment: wa, bleed_in: 0.125 } });
const plain = (await api("POST", "/api/v1/projects", { title: "Fresh Page", kind: "novel", cover_color: "#7A4B2A" })).item;

const sizes = { desktop: { viewport: { width: 1280, height: 1500 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
async function run(scheme, prefs, tagPrefix) {
  await api("PUT", "/api/v1/me/prefs", { items: prefs });
  const cookies = await boot.cookies();
  for (const [size, opts] of Object.entries(sizes)) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const tag = `${size}-${tagPrefix}`;
    const measure = async (proj, profile, pages, barcode) => {
      await page.goto(`${base}/p/${proj.slug}/compile`, { waitUntil: "load" });
      await page.waitForSelector("#cmp-preset option", { state: "attached" });
      await page.selectOption("#cmp-preset", "print-6x9");
      await page.fill("#cv-thick", "0.0025"); await page.fill("#cv-pages", pages);
      await page.selectOption("#cv-profile", profile);
      if (barcode) await page.check("#cv-barcode");
      await page.locator("#cmp-cv-form button[type=submit]").click();
      await page.waitForSelector("#cv-art svg");
      await page.waitForFunction(() => [...document.querySelectorAll("#cv-art image")].every((i) => { const im = new Image(); im.src = i.getAttribute("href"); return im.complete; }));
      await sleep(500);
    };
    const shot = async (name, sel) => {
      await page.locator(sel).evaluate((e, top) => { e.scrollIntoView({ block: top ? "start" : "center" }); if (top) window.scrollBy(0, -80); }, size === "desktop"); // the drawing near the top, the facts below it
      await sleep(200);
      const file = join(out, `compile-cover-${name}-${tag}.png`);
      await page.screenshot({ path: file, fullPage: false });
      console.log("shot", name, tag);
    };
    await measure(sides, "kdp", "200", true);
    await shot("sides", "#cv-art");
    await measure(wb, "kdp", "300", false);
    await shot("wrap-stale", "#cv-art");
    await measure(plain, "ingram", "200", true);
    await shot("generated", "#cv-art");
    await ctx.close();
  }
}
await run("light", { theme: "light", theme_light: "paper" }, "light");
await run("dark", { theme: "dark", theme_dark: "ink" }, "dark");
await browser.close();
await srv.stop();
rm(tmp);
