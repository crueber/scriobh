#!/usr/bin/env node
// Screenshots of the Git sync page for docs/screens/phase-2, in every state it can reach against the dev Git
// server: empty, a new remote with its checklist, the host key prompt, a changed host key, synced, rejected with
// commits, blocked by a secret, the force dialog, behind (the server gone), and a push's detail.
// 1280x800 and 390x844, light and dark.
//
//   node scripts/screens-git.mjs [--out docs/screens/phase-2]
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";
import { available, buildGitServer, startGitServer } from "./gitserver.mjs";

if (!available()) { console.log("screens-git: the git program is not on PATH"); process.exit(0); }
const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-2");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-gitshots-");
const srv = await startServer(build(tmp), join(tmp, "data"), { SCRIOBH_GIT_ALLOW_PRIVATE: "1" });
const gs = await startGitServer({ bin: buildGitServer(tmp), root: join(tmp, "gitroot") });
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const boot = await browser.newContext();
const bp = await boot.newPage();
await signIn(bp, srv.base);
const api = async (method, path, data) => {
  const r = await boot.request.fetch(srv.base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return { status: r.status(), json: await r.json().catch(() => null) };
};
const book = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel" })).json.item;
const doc = (await api("GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
const setText = async (md) => { const d = (await api("GET", `/api/v1/docs/${doc}`)).json.item; await api("PATCH", `/api/v1/docs/${doc}`, { base_revision: d.revision, content: md, title: "The Quiet Harbour" }); };
const mirrorReady = async () => { for (let i = 0; i < 100; i++) { const m = (await api("GET", `/api/v1/projects/${book.id}/mirror`)).json.item; if (m && m.enabled && !m.pending && m.files > 0) return; await sleep(200); } };
await setText("The harbour was quiet at dawn.\n\nMarta counted the boats twice, because the first count had come out wrong.\n");
await mirrorReady();
await gs.control("POST", "/repos", { name: "salt-road" });
const cookies = await boot.cookies();
const remote = async () => (await api("GET", `/api/v1/projects/${book.id}/git/remote`)).json.item;
const waitState = async (st) => { for (let i = 0; i < 150; i++) { const r = await remote(); if (r.status.state === st && !r.status.running) return r; await sleep(200); } throw new Error("never reached " + st); };
const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
const contexts = {};
for (const [size, opts] of Object.entries(sizes)) for (const scheme of ["light", "dark"]) {
  const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
  await ctx.addCookies(cookies);
  contexts[`${size}-${scheme}`] = await ctx.newPage();
}
const shoot = async (state, fn) => {
  for (const [tag, page] of Object.entries(contexts)) {
    await (fn ? fn(page) : page.goto(`${srv.base}/p/${book.slug}/git`));
    await page.evaluate(() => document.fonts.ready);
    await sleep(250);
    await page.screenshot({ path: join(out, `git-${state}-${tag}.png`), fullPage: !/force/.test(state) });
    console.log("shot", `git-${state}-${tag}`);
  }
};
const full = (page) => page.goto(`${srv.base}/p/${book.slug}/git`);

await shoot("empty", full);
// A new remote: the checklist, the key, the host key step.
await bp.goto(`${srv.base}/p/${book.slug}/git`);
await bp.locator("#gf-url").fill(gs.sshUrl("salt-road"));
await bp.getByRole("button", { name: "Add the remote" }).click();
await bp.waitForURL(/saved=created/);
const key = (await bp.locator("#git-pubkey").innerText()).trim();
await gs.control("POST", "/authorize", { public_key: key });
await shoot("new", (p) => p.goto(`${srv.base}/p/${book.slug}/git?saved=created#setup`));
// The host key prompt.
await shoot("hostkey", async (p) => { await p.goto(`${srv.base}/p/${book.slug}/git`); await p.getByRole("button", { name: "Scan the host key" }).click(); await p.locator(".fingerprints").waitFor(); await p.locator("#hostkey").scrollIntoViewIfNeeded(); });
await bp.goto(`${srv.base}/p/${book.slug}/git`);
await bp.getByRole("button", { name: "Scan the host key" }).click();
await bp.getByLabel("I compared this fingerprint with the one the host publishes").check();
await bp.getByRole("button", { name: "Accept this host key" }).click();
await bp.waitForURL(/saved=hostkey/);
await bp.getByRole("button", { name: "Test the connection" }).click();
await shoot("test", async (p) => { await p.goto(`${srv.base}/p/${book.slug}/git`); await p.getByRole("button", { name: "Test the connection" }).click(); await p.getByText("The connection works.").waitFor(); await p.locator("#test").scrollIntoViewIfNeeded(); });
// Synced, and what was sent.
await bp.goto(`${srv.base}/p/${book.slug}/git`);
await bp.getByRole("button", { name: "Push now" }).click();
await waitState("synced");
await shoot("synced", full);
const pushes = (await api("GET", `/api/v1/projects/${book.id}/git/pushes`)).json.items;
await shoot("push", (p) => p.goto(`${srv.base}/p/${book.slug}/git/pushes/${pushes[0].id}`));
// Rejected, with the other commits, and the force dialog.
await gs.control("POST", "/foreign-commit", { repo: "salt-road", branch: "main", message: "Fix a typo in the README", files: { "README.md": "hello" } });
await setText("The harbour was quiet at dawn, and the tide had gone out.\n\nMarta counted the boats twice, because the first count had come out wrong.\n");
await mirrorReady();
await bp.goto(`${srv.base}/p/${book.slug}/git`);
await bp.getByRole("button", { name: "Push now" }).click();
await waitState("rejected");
await shoot("rejected", full);
await shoot("force", async (p) => { await p.goto(`${srv.base}/p/${book.slug}/git`); await p.getByRole("link", { name: /^Replace the history/ }).click(); await p.locator("#git-force-form").waitFor(); await sleep(300); await p.locator("#gfp-confirm").fill("Bac"); });
// Force it through, then a changed host key.
await bp.goto(`${srv.base}/p/${book.slug}/git/force`);
await bp.locator("#gfp-confirm").fill("Backup");
await sleep(300);
await bp.getByRole("button", { name: "Replace the history" }).click();
await waitState("synced");
await gs.control("POST", "/rotate-host-key", {});
await setText("The harbour was quiet at dawn, and the tide had gone out. Nobody came.\n\nMarta counted the boats twice.\n");
await mirrorReady();
await bp.goto(`${srv.base}/p/${book.slug}/git`);
await bp.getByRole("button", { name: "Push now" }).click();
await waitState("blocked");
await shoot("hostkey-changed", async (p) => { await p.goto(`${srv.base}/p/${book.slug}/git`); await p.getByRole("button", { name: "Scan again" }).click(); await p.locator(".banner-danger").waitFor(); await p.locator(".banner-danger").scrollIntoViewIfNeeded(); });
await bp.goto(`${srv.base}/p/${book.slug}/git`);
await bp.getByRole("button", { name: "Scan again" }).click();
await bp.getByLabel("I know why the host key changed and have checked this new one").check();
await bp.getByLabel("I compared this fingerprint with the one the host publishes").check();
await bp.getByRole("button", { name: "Replace the host key" }).click();
await bp.waitForURL(/saved=hostkey/);
// A secret in the text.
const secret = "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA" + "x7Qk9".repeat(30) + "\n-----END RSA PRIVATE KEY-----\n";
await setText("Marta found a letter.\n\n" + secret + "\nShe did not read it.\n");
await mirrorReady();
await bp.goto(`${srv.base}/p/${book.slug}/git`);
await bp.getByRole("button", { name: "Push now" }).click();
await waitState("blocked");
await shoot("secret", full);
// Behind: the server goes away.
await bp.goto(`${srv.base}/p/${book.slug}/git`);
await bp.locator("#secrets button").first().click();
await bp.waitForURL(/saved=acked/);
await gs.stop();
await bp.goto(`${srv.base}/p/${book.slug}/git`);
await bp.getByRole("button", { name: "Push now" }).click();
await waitState("behind");
await shoot("behind", full);
await browser.close();
await srv.stop();
await sleep(1500);
rm(tmp);
process.exit(0);
