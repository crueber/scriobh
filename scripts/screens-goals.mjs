#!/usr/bin/env node
// Screenshots of the Goals page for docs/screens/phase-2: light and dark, 1280 and 390 px.
// History is written back-dated by a Go helper (TestSeedGoalsHistory) on the stopped server's
// data directory, because no op writes into the past.
//
//   node scripts/screens-goals.mjs --out docs/screens/phase-2
import { mkdirSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-2");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-goalshots-");
const dataDir = join(tmp, "data");
const bin = build(tmp);
let srv = await startServer(bin, dataDir);
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const boot = await browser.newContext();
const bp = await boot.newPage();
await signIn(bp, srv.base);
const api = async (method, path, data) => {
  const r = await boot.request.fetch(srv.base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return r.json().catch(() => null);
};
const words = (n, p = "w") => Array.from({ length: n }, (_, i) => `${p}${i}`).join(" ") + "\n";
const iso = (d) => new Date(Date.now() + d * 864e5).toISOString().slice(0, 10);

const me = (await api("GET", "/api/v1/me")).item;
const book = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan", word_goal: 90000 })).item;
const bookDoc = (await api("GET", `/api/v1/projects/${book.id}/docs`)).order[0];
const fresh = (await api("POST", "/api/v1/projects", { title: "Hedgerow", kind: "short" })).item;
await api("PUT", `/api/v1/projects/${book.id}/goals`, { kind: "daily", target_words: 500 });
await api("PUT", `/api/v1/projects/${book.id}/goals`, { kind: "project", target_words: 80000, end_date: iso(200) });
await api("PATCH", `/api/v1/docs/${bookDoc}`, { base_revision: 1, content: words(2600, "salt") });
const cookies0 = await boot.cookies();
await srv.stop();
execFileSync("go", ["test", "./internal/server", "-run", "TestSeedGoalsHistory", "-count=1"], {
  cwd: repo, stdio: "inherit",
  env: { ...process.env, SCRIOBH_SEED_DIR: dataDir, SCRIOBH_SEED_USER: me.id, SCRIOBH_SEED_PROJECT: book.id, SCRIOBH_SEED_DOC: bookDoc },
});
srv = await startServer(bin, dataDir);
await boot.addCookies(cookies0);
// Today: a few words on top of the history.
const cur = (await api("GET", `/api/v1/docs/${bookDoc}`)).item;
await api("PATCH", `/api/v1/docs/${cur.id}`, { base_revision: cur.revision, content: cur.content + "\n" + words(220, "today") });
const cookies = await boot.cookies();

const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const shot = async (name, o = {}) => { await page.evaluate(() => document.fonts.ready); await page.screenshot({ path: join(out, `${name}-${size}-${scheme}.png`), fullPage: true, ...o }); console.log("shot", name, size, scheme); };
    await page.goto(`${srv.base}/p/${book.slug}/goals`, { waitUntil: "load" });
    await shot("goals");
    await page.goto(`${srv.base}/p/${book.slug}/goals?days=90#history`, { waitUntil: "load" });
    await page.locator("#history").scrollIntoViewIfNeeded();
    await shot("goals-90");
    await page.goto(`${srv.base}/p/${fresh.slug}/goals`, { waitUntil: "load" });
    await shot("goals-empty");
    if (scheme === "light") {
      await page.goto(`${srv.base}/p/${book.slug}/goals`, { waitUntil: "load" });
      await page.locator("#g-daily").fill("0");
      await page.locator("#g-daily").evaluate((e) => { e.min = "0"; });
      await page.getByRole("button", { name: "Save daily target" }).click();
      await page.locator("#goals-error").waitFor();
      await shot("goals-error");
    }
    await ctx.close();
  }
}
await browser.close();
await srv.stop();
rm(tmp);
