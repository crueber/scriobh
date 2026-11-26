#!/usr/bin/env node
// End-to-end test of the repository import page in a real Chromium.
//
//   node scripts/e2e-importgit.mjs [--only substring]
//
// The page is a plain form. A fetch starts a draft whose page (/import/d/ID, the same review as any other import) follows the clone
// by asking /ui/import/d/ID/progress every 2 s while it runs, then shows the tree. An address that cannot be used at all is
// refused on the form itself.
//
// WHAT IS REAL: the servers, the pages, the forms, the import.git_* ops, the clones. Two servers are run. The first has the default
// network rule (nothing private or loopback may be connected to), so an address the server may not connect to really fails, a name that
// does not resolve really fails, and the token goes nowhere. The second is told it may connect to loopback and to trust the certificate
// of the fake code host (scripts/fakehost.mjs: a self-signed HTTPS server whose repositories are served by `git http-backend`), so a
// repository is cloned for real over HTTPS, for the ready state, the edits, the apply and the failures a host can cause (a wrong
// token, a repository that is not there, an empty one, one with no Markdown).
//
// WHAT IS NOT: nothing is stubbed in the browser. (The earlier version of this script stubbed the job and preview JSON with page.route,
// because the page was built in the browser from those answers; the page now holds no state and is drawn on the server.) A slow
// host is made by a small HTTPS proxy in this script that holds the first answer for a few seconds.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { request as httpsRequest, createServer as createHttpsServer } from "node:https";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { available, startFakeHost } from "./fakehost.mjs";
import { rm, signIn, startServer } from "./harness.mjs";

if (!available()) {
  console.log("e2e-importgit: SKIPPED (git or openssl is not on PATH; the fake code host needs both)");
  process.exit(0);
}
const hostDir = join((await import("node:os")).tmpdir(), "e2e-importgit-host-" + process.pid);
const host = await startFakeHost({ dir: hostDir });
const kit = await createKit("e2e-importgit");
kit.deadline(8 * 60 * 1000);
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
// The second server may connect to loopback and trusts the fake host's certificate.
const srv2 = await startServer(kit.bin, join(kit.tmp, "data2"), { SCRIOBH_GIT_ALLOW_PRIVATE: "1", SCRIOBH_DEV_TRUST_PEM: host.pemPath });
kit.origins = [new URL(srv2.base).origin];
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;
const ctx2 = await kit.newContext({ viewport: { width: 1280, height: 800 } }); // its own cookies: both servers are 127.0.0.1
const pg2 = await ctx2.newPage();
kit.watch(pg2, "server2");
await signIn(pg2, srv2.base);
const api2 = async (method, path, data) => {
  const r = await ctx2.request.fetch(srv2.base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  let json = null;
  try { json = await r.json(); } catch { /* no body */ }
  return { status: r.status(), json };
};
const finish = kit.finish;
kit.finish = async (n) => { await slow.stop().catch(() => {}); await srv2.stop().catch(() => {}); await host.stop().catch(() => {}); rm(hostDir); return finish(n); };

const open = async (page = pg, q = "", b = base()) => { await page.goto(`${b}/import/git${q}`); await page.waitForSelector("#rg-form"); };
const failedPage = (page) => page.locator("#imp-h", { hasText: "This import cannot go on" });
const failureText = async (page) => (await page.locator("#imp-review").innerText());

await kit.test("the page: reached from the folder import, a form with a write-only token", async () => {
  await pg.goto(`${base()}/import`);
  await pg.getByRole("link", { name: "A Git repository" }).click();
  await pg.waitForURL(/\/import\/git$/);
  await pg.waitForSelector("#rg-form");
  eq(await pg.locator("#rg-token").getAttribute("type"), "password", "the token field is masked");
  eq(await pg.locator("#rg-token").getAttribute("autocomplete"), "off", "autocomplete is off for the token");
  ok(/not saved/.test(await pg.locator("#rg-token-h").innerText()), "the page says the token is not kept");
  ok(await pg.locator("#rg-src-remote").isDisabled(), "with no remote the project choice is off");
  ok(/None of your projects/.test(await pg.locator(".rg-src").innerText()), "and says why");
  // Keyboard only: the address field is reached by Tab from the heading, the button submits with Enter.
  await pg.locator("#imp-title").focus();
  for (let i = 0; i < 6; i++) await pg.keyboard.press("Tab");
  ok(await pg.evaluate(() => /^rg-/.test(document.activeElement.id)), "Tab does not walk into the form: " + (await pg.evaluate(() => document.activeElement.id)));
});

await kit.test("real fetch: an address the server may not connect to fails in words, and the token goes nowhere", async () => {
  await open();
  const requests = [];
  pg.on("request", (r) => requests.push(r.url()));
  await pg.locator("#rg-url").fill("https://127.0.0.1:9/private/book.git");
  await pg.locator("#rg-user").fill("wren");
  await pg.locator("#rg-token").fill("tok_never_shown_again_424242");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  // the fetch is a draft of its own: its page follows it, and shows how it ended
  await pg.waitForURL(/\/import\/d\/imd_/);
  await failedPage(pg).waitFor({ timeout: 30000 });
  const text = await failureText(pg);
  ok(/not one this server may connect to/.test(text), "the failure in words: " + text);
  ok(/SCRIOBH_GIT_ALLOW_PRIVATE=1/.test(text) && /administrator|whoever runs this server/.test(text), "the plain explanation of the address rule: " + text);
  ok(!(await pg.content()).includes("tok_never_shown_again"), "the token is in the page");
  ok(!requests.some((u) => u.includes("tok_never")), "the token is in a URL");
  ok(!kit.srv.log.includes("tok_never_shown_again"), "the token is in the server's log");
  // The list holds the failed fetch; throwing it away is a real POST that comes back to the form.
  await open();
  eq(await pg.locator("#rg-token").inputValue(), "", "the token field is empty again");
  const li = pg.locator("#rg-jobs li").first();
  ok(/Failed/.test(await li.innerText()), "the list shows the failure: " + (await li.innerText()));
  ok(!(await pg.content()).includes("tok_never"), "the token is in the list");
  await li.getByRole("button", { name: /Throw away/ }).click();
  await pg.waitForURL(/\/import\/git$/);
  await waitFor(async () => (await pg.locator("#rg-jobs li").count()) === 0, "the failed fetch to leave the list");
  const jobs = (await api(main, "GET", "/api/v1/imports/git/jobs")).json.items;
  eq(jobs.length, 0, "the job is gone on the server");
  pg.removeAllListeners("request");
});

await kit.test("the end of a fetch is announced: focus moves to the result and a live region says it failed", async () => {
  await open();
  await pg.locator("#rg-url").fill("https://127.0.0.1:9/private/book.git");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await pg.waitForURL(/\/import\/d\/imd_/);
  await failedPage(pg).waitFor({ timeout: 30000 });
  await sleep(300);
  const bad = [];
  const active = await pg.evaluate(() => document.activeElement.id);
  if (active !== "imp-h") bad.push(`focus does not move to the result (it is on "${active}")`);
  const live = await pg.locator("#imp-live").innerText();
  if (!/cannot go on|failed/i.test(live)) bad.push(`the live region does not say it failed ("${live}"); the failure paragraph has role=alert only if it is added by a swap`);
  await pg.getByRole("button", { name: "Discard this import" }).click();
  await pg.waitForURL(/\/import\/git$/);
  ok(bad.length === 0, bad.join("; "));
});

await kit.test("real fetch: an ssh address and an empty form are refused on the form, in words", async () => {
  kit.allowed = [/status of 422/];
  await open();
  await pg.locator("#rg-url").fill("ssh://git@example.org/wren/x.git");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await pg.locator("#rg-job[data-bad]").waitFor();
  const t2 = await pg.locator("#rg-job").innerText();
  ok(/deploy key/.test(t2) && /https address/.test(t2), "an ssh address says what to do instead: " + t2);
  await pg.locator("#rg-url").fill("");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await pg.locator("#rg-job[data-bad]").waitFor();
  ok(/Send the repository's https address/.test(await pg.locator("#rg-job .rg-line").innerText()), "an empty address is asked for: " + (await pg.locator("#rg-job").innerText()));
  const act = await pg.evaluate(() => document.activeElement.id);
  ok(["rg-url", "rg-job-h"].includes(act), "focus does not go to the message or the address (it is on \"" + act + "\")");
});

await kit.test("real fetch: a name that does not resolve fails in words, with what to do", async () => {
  await open();
  await pg.locator("#rg-url").fill("https://no-such-host.invalid/x/y.git");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await pg.waitForURL(/\/import\/d\/imd_/);
  await failedPage(pg).waitFor({ timeout: 60000 });
  const t1 = await failureText(pg);
  ok(/could not be reached/.test(t1), "unreachable in words: " + t1);
  ok(/try again|Check the address/i.test(t1), "unreachable says what to do: " + t1);
  // Clean up the failed fetches left by these.
  for (const j of (await api(main, "GET", "/api/v1/imports/git/jobs")).json.items) await api(main, "DELETE", `/api/v1/imports/git?job_id=${j.id}`);
});

await kit.test("through a project's remote: the Git page's link preselects it, and the fetch uses it", async () => {
  const p = (await api(main, "POST", "/api/v1/projects", { title: "Diverged Book", kind: "novel" })).json.item;
  const made = await api(main, "POST", `/api/v1/projects/${p.id}/git/remotes`, { name: "Backup", url: "https://127.0.0.1:9/wren/diverged.git", auth_kind: "https_token", token: "tok_remote_secret_777" });
  eq(made.status, 201, "create the remote: " + JSON.stringify(made.json));
  await open(pg, `?project=${p.slug}`);
  ok(await pg.locator("#rg-src-remote").isChecked(), "the remote choice is selected");
  ok(await pg.locator("#rg-b-url").isHidden(), "the address block is hidden");
  ok(/Diverged Book · Backup/.test(await pg.locator("#rg-remote option:checked").innerText()), "the remote is named");
  ok(/whose remote has commits/.test(await pg.locator("#rg-b-remote").innerText()), "the page says why you came");
  ok(!(await pg.content()).includes("tok_remote_secret"), "the remote's token is in the page");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await pg.waitForURL(/\/import\/d\/imd_/);
  await failedPage(pg).waitFor({ timeout: 30000 });
  ok(/not one this server may connect to/.test(await failureText(pg)), "the stored remote's address is held to the same rule");
  ok(!(await pg.content()).includes("tok_remote_secret"), "the remote's token is in the failed page");
  for (const j of (await api(main, "GET", "/api/v1/imports/git/jobs")).json.items) await api(main, "DELETE", `/api/v1/imports/git?job_id=${j.id}`);
  // Someone who is not the owner cannot ask for it.
  eq((await api(main, "GET", "/import/git?project=no-such-project")).status, 404, "an unknown project is not found");
});

// ---- the ready state: repositories on the fake host, cloned for real by the second server -------------------------------------
const repoUrl = (name, port = host.port) => `https://127.0.0.1:${port}/mairead/${name}.git`;
const gitIn = (dir, ...a) => { const r = spawnSync("git", ["-C", dir, "-c", "user.name=x", "-c", "user.email=x@example.org", ...a], { encoding: "utf8" }); if (r.status) throw new Error(`git ${a.join(" ")}: ${r.stderr}`); };
const makeRepo = (name, files) => {
  host.addRepo("mairead", name);
  const wt = join(kit.tmp, "wt-" + name);
  mkdirSync(wt, { recursive: true });
  for (const [p, t] of Object.entries(files)) { mkdirSync(join(wt, p, ".."), { recursive: true }); writeFileSync(join(wt, p), t); }
  gitIn(wt, "init", "-q", "--initial-branch=main");
  gitIn(wt, "add", ".");
  gitIn(wt, "commit", "-qm", "first");
  gitIn(wt, "push", "-q", host.dir("mairead", name), "main");
};
// A small HTTPS proxy to the fake host that holds the first answer of a clone (the list of refs) for `delay` ms.
let delay = 0;
const proxy = createHttpsServer({ key: readFileSync(join(hostDir, "host.key")), cert: readFileSync(join(hostDir, "host.pem")) }, (req, res) => {
  const go = () => {
    const up = httpsRequest({ host: "127.0.0.1", port: host.port, path: req.url, method: req.method, headers: req.headers, rejectUnauthorized: false }, (r) => { res.writeHead(r.statusCode, r.headers); r.pipe(res); });
    up.on("error", () => { try { res.writeHead(502); res.end(); } catch { /* gone */ } });
    req.pipe(up);
  };
  if (delay && /\/info\/refs/.test(req.url)) setTimeout(go, delay); else go();
});
await new Promise((r) => proxy.listen(0, "127.0.0.1", r));
const slow = { port: proxy.address().port, stop: () => new Promise((r) => { proxy.closeAllConnections?.(); proxy.close(() => r()); }) };

await kit.test("setup: repositories on the fake host, one with a manuscript folder", async () => {
  makeRepo("salt-road", {
    "manuscript/one.md": "# The Start\n\nThe tide came in and the gulls wheeled over the quay.\n\n## First light\n\nGrey.\n\n## Second light\n\nWhite.\n",
    "manuscript/two.md": "# The Middle\n\nSalt on the wind, and a lamp that nobody lit.\n",
    "notes/elsewhere.md": "# Not in the folder\n\nThis is outside the folder that is imported.\n",
  });
  makeRepo("no-markdown", { "readme.txt": "Only text, nothing in Markdown." });
  host.addRepo("mairead", "empty-one");
  ok(host.head("mairead", "salt-road", "main"), "the repository has a commit");
});

const polls = [];
await kit.test("a running fetch: its page asks every 2 s, says it cannot be stopped, and the list shows it running with nothing to throw away", async () => {
  delay = 11000;
  pg2.on("request", (r) => { if (/\/progress$/.test(r.url())) polls.push(Date.now()); });
  await open(pg2, "", srv2.base);
  await pg2.locator("#rg-url").fill(repoUrl("salt-road", slow.port));
  await pg2.locator("#rg-user").fill("mairead");
  await pg2.locator("#rg-token").fill(host.token);
  await pg2.locator("#rg-branch").fill("main");
  await pg2.locator("#rg-path").fill("manuscript");
  await pg2.getByRole("button", { name: "Fetch the repository" }).click();
  await pg2.waitForURL(/\/import\/d\/imd_/);
  kit.draft = new URL(pg2.url()).pathname.split("/").pop();
  await pg2.locator("#imp-h").waitFor();
  ok(/The fetch/.test(await pg2.locator("#imp-h").innerText()), "the heading");
  ok(/Fetching the repository/.test(await pg2.locator("#imp-review").innerText()), "the progress words");
  ok(/cannot be stopped/.test(await pg2.locator("#imp-review").innerText()), "a running fetch says it cannot be stopped");
  // the list, in a second tab of the same person
  const other = await ctx2.newPage();
  kit.watch(other, "server2-list");
  await other.goto(`${srv2.base}/import/git`);
  const li = other.locator("#rg-jobs li").first();
  ok(/Being fetched/.test(await li.innerText()), "the list shows it running: " + (await li.innerText()));
  eq(await li.getByRole("button", { name: /Throw away/ }).count(), 0, "no Throw away while it runs");
  ok(await li.getByRole("link", { name: "Check progress" }).isVisible(), "a way back to its page");
  await other.close();
  // visible: a poll about every 2 s
  await waitFor(() => polls.length >= 3, "three polls", 9000);
  const gaps = polls.slice(1).map((t, i) => t - polls[i]);
  ok(gaps.every((g) => g > 1500 && g < 3500), "the polls are not about 2 s apart: " + gaps);
  kit.hiddenFrom = polls.length;
});

await kit.test("a hidden tab asks nothing while the fetch runs", async () => {
  const before = polls.length;
  await pg2.evaluate(() => { Object.defineProperty(document, "hidden", { configurable: true, get: () => true }); Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" }); document.dispatchEvent(new Event("visibilitychange")); });
  await sleep(5000);
  const during = polls.length - before;
  await pg2.evaluate(() => { Object.defineProperty(document, "hidden", { configurable: true, get: () => false }); Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" }); document.dispatchEvent(new Event("visibilitychange")); });
  eq(during, 0, "a hidden tab kept asking");
});

await kit.test("the ready preview: the tree of the folder only, an edit, and the apply makes the project", async () => {
  await pg2.locator("#imp-tree").waitFor({ timeout: 30000 });
  await pg2.waitForLoadState("load");
  const after = polls.length;
  await sleep(4500);
  eq(polls.length, after, "the page keeps asking after the fetch ended");
  const titles = () => pg2.locator("#imp-tree .imp-title").allInnerTexts();
  eq((await titles()).join("|"), "The Start|The Middle", "only the folder that was asked for is the tree: " + (await titles()));
  ok(/A folder of Markdown files/.test(await pg2.locator(".imp-sum").innerText()), "the summary names the source: " + (await pg2.locator(".imp-sum").innerText()));
  // an edit: a rename, which the server keeps with the draft
  const settle = async () => { await sleep(200); await pg2.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"), null, { timeout: 20000 }); await sleep(100); };
  const node = pg2.locator('#imp-tree > li.imp-node:has(> .imp-row > .imp-title:text-is("The Start"))').first();
  await node.locator("summary").click();
  await node.locator(".imp-edit-body form").first().waitFor();
  await settle();
  await node.locator('input[data-act="title"]').first().fill("Opening");
  await pg2.keyboard.press("Tab");
  await settle();
  ok((await titles()).includes("Opening"), "the rename: " + (await titles()));
  await pg2.locator("#imp-t").fill("From the repository");
  await pg2.locator("[data-go]").click();
  await pg2.waitForURL(/\/p\/from-the-repository[^/]*\/d\/doc_/, { timeout: 30000 });
  const made = (await api2("GET", "/api/v1/projects")).json.items.find((p) => p.title === "From the repository");
  ok(made, "the project was not made");
  const d = (await api2("GET", `/api/v1/projects/${made.id}/docs`)).json;
  const names = d.order.map((id) => d.items.find((i) => i.id === id).title);
  ok(names.includes("Opening") && names.includes("The Middle") && !names.includes("Not in the folder"), "the documents: " + names);
  // the list says it was imported, with a way to the project
  await open(pg2, "", srv2.base);
  const li = pg2.locator('#rg-jobs li[data-status="done"]').first();
  ok(/Imported/.test(await li.innerText()) && (await li.getByRole("link", { name: "Open the project" }).isVisible()), "the list: " + (await li.innerText()));
});

await kit.test("each failure has its words and its explanation; the list resumes a ready job and throws one away", async () => {
  delay = 0;
  const bad = [];
  const cases = [
    ["a wrong token", repoUrl("salt-road"), "wrong-token", /refused the credentials/, /token that can read it/],
    ["a repository that is not there", repoUrl("not-here"), host.token, /does not exist/, /Check the spelling/],
    ["an empty repository", repoUrl("empty-one"), host.token, /repository is empty/, /Push something/],
    ["no Markdown", repoUrl("no-markdown"), host.token, /no Markdown text/i, /Folder field/],
  ];
  for (const [what, url, tok, words, hint] of cases) {
    await open(pg2, "", srv2.base);
    await pg2.locator("#rg-url").fill(url);
    await pg2.locator("#rg-token").fill(tok);
    await pg2.locator("#rg-branch").fill("main");
    await pg2.getByRole("button", { name: "Fetch the repository" }).click();
    await pg2.waitForURL(/\/import\/d\/imd_/);
    await failedPage(pg2).waitFor({ timeout: 30000 });
    const t = await failureText(pg2);
    if (!words.test(t)) bad.push(`${what}: the words are not there: ${t.slice(0, 200)}`);
    if (!hint.test(t)) bad.push(`${what}: no explanation (${hint}) on the page that says it failed`);
  }
  // the list holds the four failures and the one that was imported; a ready job is resumed from it
  await open(pg2, "", srv2.base);
  eq(await pg2.locator('#rg-jobs li[data-status="failed"]').count(), 4, "four failed fetches are listed");
  await pg2.locator("#rg-url").fill(repoUrl("salt-road"));
  await pg2.locator("#rg-token").fill(host.token);
  await pg2.locator("#rg-branch").fill("main");
  await pg2.locator("#rg-path").fill("manuscript");
  await pg2.getByRole("button", { name: "Fetch the repository" }).click();
  await pg2.locator("#imp-tree").waitFor({ timeout: 30000 });
  await open(pg2, "", srv2.base);
  await pg2.locator('#rg-jobs li[data-status="preview"]').getByRole("link", { name: "Continue" }).click();
  await pg2.locator("#imp-tree").waitFor();
  ok((await pg2.locator("#imp-tree .imp-title").allInnerTexts()).includes("The Start"), "the ready job resumes from the list");
  // throw one away: the row leaves the list
  await open(pg2, "", srv2.base);
  const row = pg2.locator('#rg-jobs li[data-status="failed"]').first();
  const id = await row.getAttribute("data-id");
  await row.getByRole("button", { name: /Throw away/ }).click();
  await pg2.waitForURL(/\/import\/git$/);
  eq(await pg2.locator(`#rg-jobs li[data-id="${id}"]`).count(), 0, "the row did not leave the list");
  eq((await api2("GET", "/api/v1/imports/git/jobs")).json.items.some((j) => j.id === id), false, "the job is still on the server");
  ok(bad.length === 0, bad.join("; "));
});

await kit.test("phone: 390 px, no sideways scroll, targets of 44 px", async () => {
  const ph = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  await ph.addCookies(await main.cookies());
  const p = await ph.newPage();
  kit.watch(p, "phone");
  await open(p);
  eq(await p.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "the page scrolls sideways");
  const small = await p.locator("#import button:visible, #import input:visible, #import select:visible, #import .imp-tabs a:visible").evaluateAll((l) => l.filter((b) => { const r = b.getBoundingClientRect(); return b.type !== "radio" && b.type !== "checkbox" && r.height < 43.5; }).map((b) => (b.id || b.textContent.trim()).slice(0, 24)));
  eq(small.length, 0, "controls under 44 px: " + small.join(", "));
  await ph.close();
});

await kit.finish("e2e-importgit");
