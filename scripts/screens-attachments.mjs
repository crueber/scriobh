#!/usr/bin/env node
// Screenshots of attachments for docs/screens/phase-3: the writing view with the alt-text bar and an inserted
// picture, the manager list, a file's page, the refused delete, the empty state and a Compendium's page.
// 1280x800 (ratio 1) and 390x844 (ratio 2), light and dark, plus three more themes on the list.
//
//   node scripts/screens-attachments.mjs --out docs/screens/phase-3
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { deflateSync } from "node:zlib";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-3");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-attshots-");
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

// Flat-colour pictures: a field and two blocks, no gradients.
function crc32(buf) { let c, crc = ~0; for (let n = 0; n < buf.length; n++) { c = (crc ^ buf[n]) & 0xff; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; crc = (crc >>> 8) ^ c; } return ~crc >>> 0; }
function png(w, h, field, a, b) {
  const chunk = (type, data) => { const len = Buffer.alloc(4); len.writeUInt32BE(data.length); const td = Buffer.concat([Buffer.from(type), data]); const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td)); return Buffer.concat([len, td, crc]); };
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr[8] = 8; ihdr[9] = 2;
  const rows = [];
  for (let y = 0; y < h; y++) {
    const row = Buffer.alloc(1 + w * 3);
    for (let x = 0; x < w; x++) {
      const inA = x > w * 0.12 && x < w * 0.46 && y > h * 0.5 && y < h * 0.88;
      const inB = x > w * 0.55 && x < w * 0.9 && y > h * 0.25 && y < h * 0.88;
      const c = inA ? a : inB ? b : field;
      row[1 + x * 3] = c[0]; row[2 + x * 3] = c[1]; row[3 + x * 3] = c[2];
    }
    rows.push(row);
  }
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(Buffer.concat(rows))), chunk("IEND", Buffer.alloc(0))]);
}
const upload = async (owner, id, name, buf, type, alt = "") => {
  const r = await boot.request.fetch(`${base}/api/v1/${owner}/${id}/attachments?filename=${encodeURIComponent(name)}${alt ? "&alt=" + encodeURIComponent(alt) : ""}`, { method: "POST", data: buf, headers: { "Content-Type": type } });
  return (await r.json()).item;
};
const pdf = (n) => Buffer.from(`%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\n% ${n}\ntrailer<</Root 1 0 R>>\n%%EOF\n`);

const p = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000, author_name: "Máire Nolan" })).item;
const d0 = (await api("GET", `/api/v1/projects/${p.id}/docs`)).order[0];
const pics = [
  ["harbour map.png", 640, 420, [233, 228, 214], [47, 93, 80], [181, 120, 70], "Hand-drawn map of the harbour, the quay in green"],
  ["causeway at low tide.png", 800, 500, [214, 222, 228], [74, 98, 120], [150, 160, 140], "The causeway at low tide, grey water on both sides"],
  ["Mara's ledger, page 3.png", 480, 640, [236, 230, 218], [120, 110, 100], [190, 170, 130], ""],
  ["lantern.png", 600, 600, [40, 44, 52], [226, 178, 80], [140, 120, 90], "A lantern on the slip"],
  ["ferry timetable.png", 720, 400, [225, 232, 222], [90, 130, 100], [200, 190, 150], "The winter ferry timetable"],
  ["a very long file name that keeps going to see how the card wraps it at narrow widths.png", 500, 320, [230, 220, 226], [120, 80, 100], [190, 150, 170], ""],
];
const made = [];
for (const [n, w, h, f, a, b, alt] of pics) made.push(await upload("projects", p.id, n, png(w, h, f, a, b), "image/png", alt));
await upload("projects", p.id, "tide tables 2026.pdf", pdf("tide tables"), "application/pdf", "Tide tables for the whole year");
await upload("projects", p.id, "research notes.md", Buffer.from("# Notes\n\nThe ferry stops in winter.\n"), "text/markdown");
const chapter = `# The harbour at low tide\n\nThe tide had gone out and taken the light with it. By four o'clock the harbour was a bowl of black mud.\n\n![${pics[0][6]}](${made[0].url})\n\nShe had always known the sea would give things back, in its own time.\n`;
const rev0 = (await api("GET", `/api/v1/docs/${d0}`)).item.revision;
await api("PATCH", `/api/v1/docs/${d0}`, { base_revision: rev0, title: "The harbour at low tide", content: chapter });
await api("POST", `/api/v1/projects/${p.id}/docs`, { title: "Notes on the ferry", kind: "note", content: `The timetable: ![timetable](${made[4].url})` });

const empty = (await api("POST", "/api/v1/projects", { title: "Fresh Page", kind: "novel" })).item;
const comp = (await api("POST", "/api/v1/compendiums", { name: "The Salt Road Bible" })).item;
await upload("compendiums", comp.id, "queen portrait.png", png(480, 600, [232, 226, 236], [110, 70, 120], [200, 170, 100]), "image/png", "The queen, in profile");
await upload("compendiums", comp.id, "coastline.png", png(800, 400, [214, 224, 230], [60, 100, 130], [160, 170, 140]), "image/png", "The coast, drawn from the air");

const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
async function run(scheme, themePrefs, tagPrefix, only) {
  await api("PUT", "/api/v1/me/prefs", { items: themePrefs });
  const cookies = await boot.cookies();
  for (const [size, opts] of Object.entries(sizes)) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const tag = `${size}-${tagPrefix}`;
    const shot = async (name, o = {}) => { await page.screenshot({ path: join(out, `attachments-${name}-${tag}.png`), ...o }); console.log("shot", name, tag); };
    const go = async (path) => { await page.goto(base + path, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); await sleep(300); };
    await go(`/p/${p.slug}/files`);
    await page.evaluate(() => document.querySelectorAll(".att-thumb img").forEach((i) => { i.loading = "eager"; }));
    await page.waitForFunction(() => document.querySelectorAll(".att-thumb img").length === 0 || [...document.querySelectorAll(".att-thumb img")].every((i) => i.complete));
    await page.evaluate(() => Promise.all([...document.querySelectorAll(".att-thumb img")].map((i) => i.decode().catch(() => {}))));
    await shot("list", { fullPage: size === "desktop" });
    if (size === "mobile") { await page.locator(".att-grid").scrollIntoViewIfNeeded(); await page.evaluate(() => window.scrollBy(0, -70)); await sleep(200); await shot("grid"); }
    if (only) { await ctx.close(); continue; }
    await go(`/p/${p.slug}/files/${made[0].id}`);
    await shot("file", { fullPage: size === "desktop" });
    await page.getByRole("button", { name: "Delete the file" }).click();
    await page.waitForSelector("#at-confirm");
    await page.locator("#at-confirm").scrollIntoViewIfNeeded();
    await shot("refused");
    await go(`/c/${comp.slug}/files`);
    await shot("compendium", { fullPage: size === "desktop" });
    await go(`/p/${empty.slug}/files`);
    await page.locator(".empty-line").scrollIntoViewIfNeeded();
    await shot("empty");
    // The writing view: a picture added from the toolbar waits for its alt text; then it is inserted.
    await go(`/p/${p.slug}/d/${d0}`);
    await page.waitForFunction(() => window.scriobh && window.scriobh.attach && window.scriobh.write);
    await page.locator(".scriobh-prose").click();
    await page.keyboard.press("ControlOrMeta+End");
    await page.locator('input[type="file"]').setInputFiles([{ name: "ferry quay.png", mimeType: "image/png", buffer: png(600, 360, [226, 232, 226], [70, 110, 90], [190, 160, 120]) }]);
    await page.waitForSelector('#notices input[type="text"]');
    await sleep(300);
    await shot("write-bar");
    await page.locator('#notices input[type="text"]').fill("The ferry quay in the morning");
    await page.keyboard.press("Enter");
    await sleep(600);
    await page.evaluate(() => { const i = document.querySelector('.scriobh-prose img[src^="/files/att_"]'); if (i) i.scrollIntoView({ block: "center" }); });
    await sleep(300);
    await shot("write-inserted");
    await ctx.close();
    // Put the chapter back, and remove the picture this run added, so every run starts from the same project.
    const cur = (await api("GET", `/api/v1/docs/${d0}`)).item;
    await api("PATCH", `/api/v1/docs/${d0}`, { base_revision: cur.revision, content: chapter });
    for (const it of (await api("GET", `/api/v1/projects/${p.id}/attachments`)).items) if (it.filename === "ferry quay.png") await api("DELETE", `/api/v1/projects/${p.id}/attachments/${it.id}`);
  }
}
await run("light", { theme: "light", theme_light: "paper" }, "light");
await run("dark", { theme: "dark", theme_dark: "ink" }, "dark");
for (const [name, scheme] of [["sepia", "light"], ["slate", "dark"], ["moss", "dark"]]) {
  await run(scheme, scheme === "light" ? { theme: "light", theme_light: name } : { theme: "dark", theme_dark: name }, `theme-${name}`, true);
}
await browser.close();
await srv.stop();
rm(tmp);
