#!/usr/bin/env node
// Screenshots of the import page (chooser, a preview with an open edit panel, an error, adding to a
// project), the project menu's mirror line and the administrators' Backups section, for docs/screens/phase-2.
// 1280x800 (device pixel ratio 1) and 390x844 (ratio 2), light and dark.
//
//   node scripts/screens-import.mjs --out docs/screens/phase-2
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-2");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-impshots-");
const srv = await startServer(build(tmp), join(tmp, "data"));
const base = srv.base;
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const boot = await browser.newContext();
const bp = await boot.newPage();
await signIn(bp, base, "admin@dev.scriobh");
const api = async (method, path, data) => {
  const r = await boot.request.fetch(base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return r.json().catch(() => null);
};
const words = (n, seed = "tide") => (`${seed} came in over the grey stones and the gulls wheeled `.repeat(Math.ceil(n / 9))).split(" ").slice(0, n).join(" ");
const write = (dir, rel, text) => { const p = join(dir, rel); mkdirSync(join(p, ".."), { recursive: true }); writeFileSync(p, text); };
const book = join(tmp, "The Salt Road");
write(book, "00-title-page.md", `# Title page\n\n${words(12)}\n`);
write(book, "01-the-start.md", `# The Start\n\n${words(420)}\n\n## First light\n\n${words(160, "dawn")}\n\n## Second light\n\n${words(140, "noon")}\n`);
write(book, "02-the-middle.md", `# The Middle\n\n${words(900, "reef")}\n`);
write(book, "part-two/01-the-crossing.md", `# The Crossing\n\n${words(780, "ferry")}\n`);
write(book, "part-two/02-landfall.md", `# Landfall\n\n${words(640, "shore")}\n`);
write(book, "appendix.md", `# Notes on tides\n\n${words(220, "moon")}\n`);
write(book, ".git/config", "[core]\n");
write(book, "cover.png", "not really a png");
write(book, "notes/ideas.txt", "ideas");
const huge = join(tmp, "huge.zip");
execFileSync("dd", ["if=/dev/zero", `of=${huge}`, "bs=1048576", "count=35"], { stdio: "ignore" });

const host = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000, cover_color: "#2F5D50" })).item;
await api("POST", "/api/v1/projects", { title: "Hedgerow", kind: "short", cover_color: "#6B3E4F" });
await api("POST", "/api/v1/admin/backups");
await sleep(2500); // the mirror writes a few seconds after a change
const cookies = await boot.cookies();

// A tall page is shot by growing the viewport to it, so the sticky header and tab bar sit where they do at the top.
const shot = async (page, name, o = {}) => {
  if (o.fullPage) {
    const w = page.viewportSize().width, h = await page.evaluate(() => document.documentElement.scrollHeight);
    await page.setViewportSize({ width: w, height: Math.min(h, 3000) });
    await sleep(200);
    await page.screenshot({ path: join(out, name + ".png") });
    await page.setViewportSize({ width: w, height: size0(page) });
  } else await page.screenshot({ path: join(out, name + ".png"), ...o });
  console.log("shot", name);
};
const heights = new WeakMap();
const size0 = (page) => heights.get(page);
const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    heights.set(page, opts.viewport.height);
    const tag = `${size}-${scheme}`;
    const go = async (path) => { await page.goto(base + path, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); await sleep(300); };
    const full = size === "mobile" ? { fullPage: true } : { fullPage: true };
    await go("/import");
    await shot(page, `import-choose-${tag}`);
    await page.locator("#imp-folder").setInputFiles(book);
    await page.locator("#imp-h").waitFor();
    await sleep(400);
    // Open the edit panel of the first chapter so the controls are on the page.
    await page.locator('#imp-result .imp-node:has(> .imp-row > .imp-title:text-is("The Start")) summary').first().click();
    await page.locator('#imp-result .imp-skipped summary').click();
    await sleep(200);
    await shot(page, `import-preview-${tag}`, full);
    await go("/import");
    await page.locator("#imp-zip").setInputFiles(huge);
    await page.waitForFunction(() => /larger than 34 MiB/.test(document.querySelector("#imp-status").textContent));
    await shot(page, `import-error-${tag}`);
    await go(`/p/${host.slug}/import`);
    await page.locator("#imp-folder").setInputFiles(book);
    await page.locator("#imp-h").waitFor();
    await sleep(400);
    await shot(page, `import-into-${tag}`, full);
    await go("/");
    await page.getByRole("link", { name: "Actions for The Salt Road" }).click();
    await page.locator("dialog#dialog[open]").waitFor();
    await sleep(500);
    await shot(page, `project-menu-mirror-${tag}`);
    await page.keyboard.press("Escape");
    await go("/settings#backups");
    if (size === "mobile") await shot(page, `backups-${tag}`, { fullPage: true });
    else await page.locator("#backups").screenshot({ path: join(out, `backups-${tag}.png`) }), console.log("shot", `backups-${tag}`);
    await ctx.close();
  }
}
await browser.close();
await srv.stop();
rm(tmp);
