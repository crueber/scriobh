#!/usr/bin/env node
// Screenshots of the repository import page for docs/screens/phase-2: choosing a source, through a
// project's remote, a fetch under way, a failed fetch explained, the preview, and the list of imports.
// 1280x800 and 390x844, light and dark. The failed fetch is real (an address the server may not connect
// to); a fetch under way, the preview and the list are drawn from stubbed answers of the ops (see
// e2e-importgit.mjs for why a real clone cannot be driven from here).
//
//   node scripts/screens-importgit.mjs [--out docs/screens/phase-2]
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-2");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-igshots-");
const srv = await startServer(build(tmp), join(tmp, "data"));
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const boot = await browser.newContext();
const bp = await boot.newPage();
await signIn(bp, srv.base);
const api = async (method, path, data) => {
  const r = await boot.request.fetch(srv.base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return { status: r.status(), json: await r.json().catch(() => null) };
};
const b64 = (s) => Buffer.from(s).toString("base64");
const pv = (await api("POST", "/api/v1/imports/preview", { name: "Salt Road", files: [
  { path: "manuscript/01-the-harbour.md", data: b64("# The Harbour\n\nThe tide came in and the gulls wheeled over the quay, and nobody on the boats looked up.\n\n## First light\n\nGrey.\n\n## Second light\n\nWhite.\n"), size: 200 },
  { path: "manuscript/02-the-road.md", data: b64("# The Road\n\nSalt on the wind, and a lamp that nobody lit.\n"), size: 70 },
  { path: "manuscript/part-two/03-the-crossing.md", data: b64("# The Crossing\n\nThe ferry came late.\n"), size: 60 },
  { path: "notes/ideas.md", data: b64("# Ideas\n\nA storm, maybe.\n"), size: 40 },
  { path: "cover.png", size: 9000 }, { path: "LICENSE", size: 1000 }] })).json;
const proj = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel" })).json.item;
await api("POST", `/api/v1/projects/${proj.id}/git/remotes`, { name: "Backup", url: "https://git.example.org/wren/salt-road.git", auth_kind: "https_token", token: "tok_for_the_screens_only_123456" });
const cookies = await boot.cookies();
const now = () => new Date().toISOString();
const job = (id, status, extra = {}) => ({ id, status, source_url: `https://git.example.org/wren/${id === "imp_ready" ? "salt-road" : "essays"}.git`, branch: "main", created_at: now(), ...extra });
const ready = job("imp_ready", "preview", { commit_sha: "9f3c2a1b77e0", summary: { documents: pv.documents, words: pv.words, layout: pv.layout, token: pv.token } });

const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const tag = `${size}-${scheme}`;
    for (const j of (await api("GET", "/api/v1/imports/git/jobs")).json.items) await api("DELETE", `/api/v1/imports/git?job_id=${j.id}`); // start each set with an empty list
    const shot = async (name, el) => { await sleep(250); await page.screenshot({ path: join(out, `importgit-${name}-${tag}.png`) }); console.log("shot", name, tag); };
    const go = async (q = "") => { await page.goto(`${srv.base}/import/git${q}`, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); await page.waitForSelector("#rg-form"); };
    await go();
    await shot("choose");
    await go(`?project=${proj.slug}`);
    await shot("remote");
    // a failed fetch, for real
    await go();
    await page.locator("#rg-url").fill("https://127.0.0.1:9/private/book.git");
    await page.locator("#rg-user").fill("wren");
    await page.locator("#rg-token").fill("tok_screen_only");
    await page.getByRole("button", { name: "Fetch the repository" }).click();
    await page.locator("#rg-job[data-bad]").waitFor({ timeout: 30000 });
    await page.locator("#rg-job").scrollIntoViewIfNeeded();
    await shot("failed");
    // stubbed: a fetch under way, then the preview, then the list
    let phase = "running";
    await page.route("**/api/v1/imports/git/preview", async (route) => {
      const b = route.request().postDataJSON();
      if (b.job_id) return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ job: ready, preview: pv }) });
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ job: job("imp_run", "running") }) });
    });
    await page.route("**/api/v1/imports/git?job_id=*", async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ item: phase === "running" ? job("imp_run", "running") : ready }) });
    });
    await go();
    await page.locator("#rg-url").fill("https://git.example.org/wren/essays.git");
    await page.getByRole("button", { name: "Fetch the repository" }).click();
    await page.locator("#rg-job-h").waitFor();
    await page.locator("#rg-job").scrollIntoViewIfNeeded();
    await shot("fetching");
    // the list: the ready one and the running one beside whatever else is there
    phase = "preview";
    await page.locator("#rg-url").fill("https://git.example.org/wren/salt-road.git");
    await page.getByRole("button", { name: "Fetch the repository" }).click();
    await page.locator(".imp-tree").waitFor({ timeout: 10000 });
    await sleep(300);
    await page.locator("#rg-job").scrollIntoViewIfNeeded();
    await shot("preview");
    await page.locator("#rg-jobs-sec").scrollIntoViewIfNeeded();
    await shot("list");
    await ctx.close();
  }
}
await browser.close();
await srv.stop();
await sleep(1500);
rm(tmp);
process.exit(0);
