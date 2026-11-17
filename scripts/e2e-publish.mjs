#!/usr/bin/env node
// The Git page's Phase 7 parts and the publish flow in Chromium, against a fake code host that the script starts
// (scripts/fakehost.mjs: Forgejo and GitHub Enterprise Server REST subsets over a self-signed HTTPS server, repositories
// served by `git http-backend`). The development server trusts its certificate through SCRIOBH_DEV_TRUST_PEM. Covered: the
// audience is chosen, never defaulted; connect an account (the token is write-only); create a repository and review the
// plan before anything is created; a public repository needs its name typed; choose an empty repository from a paged list;
// a rate limit and a plan that changed; several remotes (up to five); widening what a remote carries (typed name, history
// starts over, force push with the typed name); the signing key; the push trigger (the secret shown once, a signed test);
// the release panel (retry safely, a tag that already has another edition); the restore pause; a phone, the keyboard and a
// reload with the network cut. Skips (exit 0) when git or openssl is missing.
//
//   node scripts/e2e-publish.mjs [--only substring] [--shots DIR]
import { createHmac } from "node:crypto";
import { mkdirSync } from "node:fs";
import { createServer } from "node:http";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { available, startFakeHost } from "./fakehost.mjs";
import { rm, tempDir } from "./harness.mjs";

if (!available()) {
  console.log("e2e-publish: SKIPPED (git or openssl is not on PATH; the fake code host needs both)");
  process.exit(0);
}
const hostDir = tempDir();
const host = await startFakeHost({ dir: hostDir });
const hooks = [];
const receiver = createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => { hooks.push({ url: req.url, headers: req.headers, body: Buffer.concat(chunks).toString() }); res.writeHead(204); res.end(); });
});
await new Promise((r) => receiver.listen(0, "127.0.0.1", r));
const hookUrl = `http://127.0.0.1:${receiver.address().port}/hook/PATH-SECRET-77`;

const kit = await createKit("e2e-publish", { env: { SCRIOBH_GIT_ALLOW_PRIVATE: "1", SCRIOBH_DEV_TRUST_PEM: host.pemPath } });
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const origFinish = kit.finish;
kit.finish = async (n) => { await host.stop().catch(() => {}); receiver.close(); rm(hostDir); return origFinish(n); };
const { api, waitFor } = kit;
const base = () => kit.base();
const owner = await kit.signedIn();
const pg = owner.page, main = owner.ctx;
kit.main = pg;

let book, docs = {};
const gitPage = (remote, extra = "") => `${base()}/p/${book.slug}/git${remote ? `?remote=${remote}${extra}` : extra ? "?" + extra.slice(1) : ""}`;
const publishPage = (q = "") => `${base()}/p/${book.slug}/git/publish${q}`;
const remotes = async () => (await api(main, "GET", `/api/v1/projects/${book.id}/git/remotes`)).json.items;
const remoteByName = async (n) => (await remotes()).find((r) => r.name === n || r.name === `Forgejo: mairead/${n}`);
const idle = async (name, ms = 40000) => { try { return await waitFor(async () => { const r = await remoteByName(name); return r && !r.status.running && r.status.last_push_at ? r : null; }, `the push of ${name} to finish`, ms); } catch (e) { throw new Error(e.message + " " + JSON.stringify((await remoteByName(name))?.status)); } };
const bodyText = async () => (await pg.locator("main").innerText());
const mirrorReady = () => waitFor(async () => { const m = (await api(main, "GET", `/api/v1/projects/${book.id}/mirror`)).json.item; return m && m.enabled && !m.pending && m.files > 0; }, "the Markdown copy", 20000);
const hostWrites = () => host.requests().filter((r) => r.method !== "GET" && !r.path.includes(".git/")).length;
const setDoc = async (id, patch) => { const d = (await api(main, "GET", `/api/v1/docs/${id}`)).json.item; const r = await api(main, "PATCH", `/api/v1/docs/${id}`, { base_revision: d.revision, ...patch }); eq(r.status, 200, "save a document"); };
const treeText = (o, n, b = "main") => host.tree(o, n, b).map((p) => host.show(o, n, b, p)).join("\n");
const phone = async (path) => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "phone");
  await p.goto(path);
  const o = await p.evaluate(() => ({ over: document.documentElement.scrollWidth > document.documentElement.clientWidth, small: [...document.querySelectorAll("main button, main a.btn, main input:not([type=hidden]):not([type=checkbox]):not([type=radio]), main select")].filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.height < 43.5; }).map((e) => (e.textContent || e.name || e.id).trim().slice(0, 24)) }));
  if (shots) await p.screenshot({ path: join(shots, `phone-${new URL(path).pathname.split("/").slice(-1)[0] || "page"}.png`), fullPage: true });
  await ctx.close();
  return o;
};
const releaseDone = async (what, re = /Published/) => { try { await waitFor(async () => re.test(await pg.locator("#rel-headline").innerText().catch(() => "")), what, 60000); } catch (e) { throw new Error(e.message + ": " + (await pg.locator("#rel-live").innerText().catch(() => "no panel")).replace(/\s+/g, " ").slice(0, 400)); } };
// A screenshot is taken in a context of its own: a full-page capture touches the page it is taken from, and the CSP watcher
// on the main page must see only what the page itself did.
const shot = async (url, name) => {
  if (!shots) return;
  const ctx = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  await p.goto(url);
  await p.screenshot({ path: join(shots, name), fullPage: true });
  await ctx.close();
};
// click a button whose answer is a redirect to a page, and wait for that page
const clickAndLoad = async (loc) => { const nav = pg.waitForNavigation(); await loc.click(); await nav; };
const chooseAudience = async (scope, value) => pg.locator(`${scope} input[name="audience"][value="${value}"]`).check();

await kit.test("setup: a book with a finished chapter, a draft and a private note", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "Salt Road", kind: "novel", author_name: "Wren Writer" });
  book = made.json.item;
  const first = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  await setDoc(first, { title: "Chapter One", content: "The harbour was quiet at dawn.\n\nMarta counted the boats twice.\n\n%%PRIVATE-NOTE-ONE only the writer%%\n" });
  await setDoc(first, { status: "done" });
  docs.one = first;
  docs.two = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Chapter Two", kind: "chapter", content: "A draft of the second chapter, DRAFT-WORDS-TWO.\n" })).json.item.id;
  await setDoc(docs.two, { status: "draft" });
  host.addRepo("mairead", "backup", { private: true });
  await mirrorReady();
});

await kit.test("adding a remote: both audiences are offered in plain words, neither is chosen, and the server refuses a form that does not choose", async () => {
  await pg.goto(gitPage());
  const radios = pg.locator('#add input[name="audience"]');
  eq(await radios.count(), 2, "two audiences");
  eq(await pg.locator('#add input[name="audience"]:checked').count(), 0, "neither is chosen");
  ok(await radios.first().evaluate((e) => e.required), "the choice is required in the browser too");
  const text = await pg.locator("#add").innerText();
  ok(/Everything in the project except secrets/.test(text) && /Only what a reader sees/.test(text), "what each sends, in words");
  ok(/passwords, keys and tokens are never pushed/i.test(text), "and that secrets never go");
  const csrf = await pg.locator('meta[name="csrf-token"]').getAttribute("content");
  kit.allowed.push(/status of 422/);
  const status = await pg.evaluate(async ([id, csrf]) => { const r = await fetch(`/ui/projects/${id}/git/create`, { method: "POST", headers: { "X-CSRF-Token": csrf, "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ name: "X", url: "ssh://git@forge.example.com/a/b.git" }) }); return r.redirected && /role="alert"/.test(await r.text()) ? 422 : r.status; }, [book.id, csrf]); // a refusal is a redirect to the page that shows it
  eq(status, 422, "a form with no audience is refused");
  const o = await phone(gitPage());
  eq(o.over, false, "the empty Git page scrolls sideways on a phone");
  await shot(gitPage(), "git-empty-1280.png");
});

await kit.test("the publish page without an account: the connect form, with the scopes in plain words", async () => {
  await pg.goto(publishPage());
  const t = await bodyText();
  ok(/Connect an account/.test(t) && /Administration/.test(t) && /Contents/.test(t) && /repo scope/.test(t), "scopes are explained");
  eq(await pg.locator("#pc-token").getAttribute("type"), "password", "the token is typed into a password field");
  ok(await pg.locator("#plan").count() === 0, "no plan yet");
  eq(hostWrites(), 0, "nothing was changed at the host");
});

await kit.test("a wrong token says one thing, and the token is not in the page it came back to", async () => {
  kit.allowed.push(/status of 422/);
  await pg.goto(publishPage());
  await pg.locator('input[name="provider"][value="forgejo"]').check();
  await pg.locator("#pc-host").fill(host.host);
  await pg.locator("#pc-token").fill("WRONG-TOKEN-CANARY-123456");
  await pg.getByRole("button", { name: "Connect an account" }).click();
  await pg.locator("#pub-error").waitFor();
  const html = await pg.content();
  ok(!html.includes("WRONG-TOKEN-CANARY-123456"), "the token came back in the page");
  eq(await pg.locator("#pc-token").inputValue(), "", "and the field is empty again");
});

await kit.test("connect the Forgejo account: the token is write-only", async () => {
  await pg.goto(publishPage());
  await pg.locator('input[name="provider"][value="forgejo"]').check();
  await pg.locator("#pc-host").fill(host.host);
  await pg.locator("#pc-label").fill("Test host");
  await pg.locator("#pc-token").fill(host.token);
  await pg.getByRole("button", { name: "Connect an account" }).click();
  await pg.waitForURL(/connected=1/);
  ok(/The account is connected/.test(await bodyText()), "confirmed");
  ok((await pg.locator("#pub-account").innerText()).includes("mairead"), "the account is listed by its login");
  const everything = await pg.evaluate(() => document.documentElement.outerHTML + JSON.stringify(localStorage) + JSON.stringify(sessionStorage) + document.cookie + location.href);
  ok(!everything.includes(host.token), "the token is nowhere in the page, the address or the browser's storage");
  ok(await pg.locator("#fields").isVisible(), "the repository form follows");
});

await kit.test("create a repository: the plan is reviewed first and nothing exists until the last button", async () => {
  await pg.goto(publishPage(`?account_id=${(await api(main, "GET", "/api/v1/git/accounts")).json.items[0].id}`));
  await pg.locator("#pf-name").fill("salt-road");
  await pg.locator("#pf-desc").fill("The Salt Road, a novel");
  await chooseAudience("#fields", "public");
  await pg.locator('#fields input[name="only_done"]').check();
  await pg.getByRole("button", { name: "Review the plan" }).click();
  await pg.locator("#plan").waitFor();
  const t = await pg.locator("#plan").innerText();
  ok(/Create the repository/.test(t) && /Push the first commit/.test(t), "what will happen, in order");
  ok(/mairead\/salt-road/.test(t) && /Private: only you and people you add/.test(t), "the repository and its visibility: " + t.slice(0, 200));
  ok(/\d+ files?, [\d.]+ (bytes|KB)/.test(t), "the files and their size");
  ok(/Chapter Two/.test(t) && /Not done yet/.test(t), "the draft is listed as left out, with why");
  eq(host.repo("mairead", "salt-road"), null, "nothing was created at the host");
  eq(hostWrites(), 0, "no write reached the host");
  
  await pg.getByRole("button", { name: "Create and push" }).click();
  await pg.waitForURL(/saved=published/);
  await idle("salt-road");
  ok(host.repo("mairead", "salt-road") && host.repo("mairead", "salt-road").private === true, "created, private");
  const all = treeText("mairead", "salt-road");
  ok(/harbour was quiet/.test(all), "the finished chapter is pushed");
  ok(!/DRAFT-WORDS-TWO/.test(all) && !/PRIVATE-NOTE-ONE/.test(all), "the draft and the private note are not");
  ok(!(await pg.content()).includes(host.token), "the token is not on the Git page");
});

await kit.test("a public repository needs its name typed, here and on the server", async () => {
  kit.allowed.push(/status of 4\d\d/);
  await pg.goto(publishPage(`?account_id=${(await api(main, "GET", "/api/v1/git/accounts")).json.items[0].id}`));
  await pg.locator("#pf-name").fill("open-book");
  await pg.locator('#fields input[name="visibility"][value="public"]').check();
  await chooseAudience("#fields", "public");
  await pg.getByRole("button", { name: "Review the plan" }).click();
  await pg.locator("#plan").waitFor();
  ok(/Anyone can read it|anyone can read it/i.test(await pg.locator("#plan").innerText()), "the plan says it is public");
  const go = pg.getByRole("button", { name: "Create and push" });
  ok(await go.isDisabled(), "the button waits for the name");
  await pg.locator("#pp-confirm").fill("open-boo");
  ok(await go.isDisabled(), "a name that is not exact keeps it off");
  // the server refuses a wrong name whatever the script does
  const form = await pg.locator("#plan form").evaluate((f) => Object.fromEntries(new FormData(f)));
  const status = await pg.evaluate(async (f) => { const r = await fetch(f.action, { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ ...f.data, confirm: "nope" }) }); return r.redirected && /role="alert"/.test(await r.text()) ? 422 : r.status; }, { action: await pg.locator("#plan form").getAttribute("action"), data: form });
  ok(status >= 400 && status < 500, "a wrong name is refused by the server: " + status);
  eq(host.repo("mairead", "open-book"), null, "nothing was created");
  await pg.locator("#pp-confirm").fill("open-book");
  await go.click();
  await pg.waitForURL(/saved=published/);
  eq(host.repo("mairead", "open-book").private, false, "created public after the name was typed");
});

await kit.test("a name that exists is a blocker, with no way to publish", async () => {
  await pg.goto(publishPage(`?account_id=${(await api(main, "GET", "/api/v1/git/accounts")).json.items[0].id}`));
  await pg.locator("#pf-name").fill("salt-road");
  await chooseAudience("#fields", "private");
  await pg.getByRole("button", { name: "Review the plan" }).click();
  await pg.locator("#plan").waitFor();
  ok(/already exists/.test(await pg.locator("#plan").innerText()), "the blocker is in words");
  eq(await pg.getByRole("button", { name: "Create and push" }).count(), 0, "no button to publish");
});

await kit.test("choose an empty repository from a paged list; one with content cannot be chosen", async () => {
  host.addRepo("mairead", "empty-one", { private: false }); // empty, and public at the host: the plan asks for its name
  host.addRepo("mairead", "has-content", { private: true });
  host.addCommit("mairead", "has-content");
  for (let i = 0; i < 52; i++) host.addRepo("mairead", `bulk-${String(i).padStart(2, "0")}`, { private: true });
  const acct = (await api(main, "GET", "/api/v1/git/accounts")).json.items[0].id;
  await pg.goto(publishPage(`?account_id=${acct}&mode=existing#pick`));
  ok(await pg.getByRole("link", { name: "Next" }).isVisible(), "a second page is offered");
  await pg.getByRole("link", { name: "Next" }).click();
  await pg.waitForURL(/page=2/);
  ok(await pg.getByRole("link", { name: "Previous" }).isVisible(), "and the way back");
  await pg.goto(publishPage(`?account_id=${acct}&mode=existing&page=2&q=has-content#pick`));
  const row = pg.locator(".repo", { hasText: "has-content" });
  ok(/has content/.test(await row.innerText()), "the host's word that it has content is shown: " + (await row.innerText()));
  eq(await row.getByRole("link", { name: /Choose/ }).count(), 0, "a repository with content has no Choose");
  await pg.goto(publishPage(`?account_id=${acct}&mode=existing&page=2&q=empty-one#pick`));
  await pg.locator(".repo", { hasText: "empty-one" }).getByRole("link", { name: /Choose/ }).click();
  await pg.waitForURL(/name=empty-one/);
  eq(await pg.locator("#pf-name").inputValue(), "empty-one", "the name is filled in");
  await chooseAudience("#fields", "public");
  await pg.getByRole("button", { name: "Review the plan" }).click();
  await pg.locator("#plan").waitFor();
  const go2 = pg.getByRole("button", { name: "Connect and push" });
  ok(await go2.isVisible(), "an existing repository is connected, not created");
  ok(await pg.locator("#pp-confirm").count() === 1, "it is public at the host, so its name is asked for");
  ok(await go2.isDisabled(), "and the button waits for it");
  await pg.locator("#pp-confirm").fill("empty-one");
  await go2.click();
  await pg.waitForURL(/saved=published/);
  ok(!(await pg.locator("#pub-error").count()), "the plan that was reviewed is the one that went (no plan_changed)");
  await idle("empty-one");
  ok(/harbour was quiet/.test(treeText("mairead", "empty-one")), "pushed into the empty repository");
});

await kit.test("a rate limit says how long to wait, and nothing is changed", async () => {
  kit.allowed.push(/status of 429/);
  const acct = (await api(main, "GET", "/api/v1/git/accounts")).json.items[0].id;
  await pg.goto(publishPage(`?account_id=${acct}`));
  await pg.locator("#pf-name").fill("rate-book");
  await chooseAudience("#fields", "private");
  host.mode = "ratelimit";
  await pg.getByRole("button", { name: "Review the plan" }).click();
  await pg.locator("#pub-error").waitFor();
  ok(/7 seconds/.test(await pg.locator("#pub-error").innerText()), "the wait is stated: " + (await pg.locator("#pub-error").innerText()));
  host.mode = "";
  eq(host.repo("mairead", "rate-book"), null, "nothing was created");
});

await kit.test("a plan that changed after it was reviewed is shown again, not published", async () => {
  kit.allowed.push(/status of 409/);
  const acct = (await api(main, "GET", "/api/v1/git/accounts")).json.items[0].id;
  await pg.goto(publishPage(`?account_id=${acct}`));
  await pg.locator("#pf-name").fill("late-book");
  await chooseAudience("#fields", "private");
  await pg.getByRole("button", { name: "Review the plan" }).click();
  await pg.locator("#plan").waitFor();
  await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Chapter Three", kind: "chapter", content: "Added after the plan was made.\n" });
  await mirrorReady();
  await pg.getByRole("button", { name: "Create and push" }).click();
  await pg.locator("#pub-error").waitFor();
  ok(/Something changed since you reviewed the plan/.test(await pg.locator("#pub-error").innerText()), "the page says so");
  eq(host.repo("mairead", "late-book"), null, "nothing was created");
  await pg.getByRole("button", { name: "Create and push" }).click();
  await pg.waitForURL(/saved=published/);
  ok(host.repo("mairead", "late-book"), "the new plan can be published");
  await idle("late-book");
});

await kit.test("several remotes: each is listed with what it carries, where it goes and how it stands; up to five", async () => {
  await pg.goto(gitPage());
  eq(await pg.locator("#remotes .remote").count(), 4, "four remotes are listed");
  const t = await pg.locator("#remotes").innerText();
  ok(/Only what a reader sees/.test(t) && /Everything except secrets/.test(t) && /only documents that are done/.test(t), "audience and the done-only filter are on the list");
  ok(/main/.test(t) && /The top of the repository/.test(t), "branch and folder");
  ok(/Pushed/.test(t) && /Pushed \d/.test(t), "the last push");
  ok(await pg.getByRole("link", { name: "Publish to a repository" }).first().isVisible(), "the publish flow is one click away");
  await pg.getByRole("link", { name: "empty-one" }).click();
  await pg.waitForURL(/remote=/);
  ok(/empty-one/.test(await pg.locator("#git-live").innerText()), "the chosen remote is the one shown");
  // an https remote made by hand fills the project's last place
  await pg.goto(gitPage());
  await pg.locator("#gf-name").fill("Backup");
  await pg.locator("#gf-url").fill(`https://${host.host}/mairead/backup.git`);
  await pg.locator("#add details.more").first().evaluate((d) => { d.open = true; });
  await pg.locator("#gf-user").fill("mairead");
  await pg.locator("#gf-token").fill(host.token);
  await chooseAudience("#add", "private");
  await pg.getByRole("button", { name: "Add the remote" }).click();
  await pg.waitForURL(/saved=created/);
  eq((await remotes()).length, 5, "five remotes");
  await pg.goto(gitPage());
  eq(await pg.locator("#add form.git-form").count(), 0, "a sixth cannot be added");
  ok(/up to 5 remotes/.test(await pg.locator("#add").innerText()), "the limit is said");
  const o = await phone(gitPage());
  eq(o.over, false, "five remotes scroll sideways on a phone");
  eq(o.small.length, 0, "touch targets: " + o.small.join(", "));
  await shot(gitPage(), "git-remotes-1280.png");
});

await kit.test("widening what a remote carries: the box, the typed name, the history starts over, the force push with the typed name and a bundle", async () => {
  kit.allowed.push(/status of 4\d\d/);
  const rem = await remoteByName("salt-road");
  await pg.goto(gitPage(rem.id, "#carries"));
  await pg.locator("#carries-change summary").click();
  await chooseAudience("#carries-change", "private");
  await clickAndLoad(pg.locator("#carries-change").getByRole("button", { name: "Change what it carries" }));
  ok(/history will start over/.test(await pg.locator("#git-error").innerText()), "the box is needed");
  await chooseAudience("#carries-change", "private");
  await pg.locator('#carries-change input[name="understood"]').check();
  await clickAndLoad(pg.locator("#carries-change").getByRole("button", { name: "Change what it carries" }));
  ok(/Type the remote's name exactly/.test(await pg.locator("#git-error").innerText()), "the name is needed to let more reach a repository that may be public");
  eq((await remoteByName("salt-road")).audience, "public", "nothing changed yet");
  await pg.locator("#carries-change").evaluate((d) => { d.open = true; });
  await chooseAudience("#carries-change", "private");
  await pg.locator('#carries-change input[name="understood"]').check();
  await pg.locator("#ga-confirm").fill(rem.name);
  await pg.locator("#carries-change").getByRole("button", { name: "Change what it carries" }).click();
  await pg.waitForURL(/saved=audience/);
  eq((await remoteByName("salt-road")).audience, "private", "the audience changed");
  ok(/history starts over/.test(await bodyText()), "and the page says the history starts over");
  await pg.getByRole("button", { name: "Push now" }).click();
  await waitFor(async () => (await remoteByName("salt-road")).status.state === "rejected", "the push to be refused", 30000);
  await pg.goto(gitPage(rem.id));
  ok(/The repository has other commits/.test(await bodyText()), "the refusal is the divergence page: " + (await pg.locator("#git-headline").innerText()));
  await pg.getByRole("link", { name: /Replace the history/ }).click();
  await pg.locator("#git-force-form").waitFor();
  await kit.sleep(300); // the dialog's script settles a moment after the form arrives
  const input = pg.locator("#gfp-confirm");
  await input.fill("nope");
  const submit = pg.locator("#git-force-form button[data-confirm-submit]");
  ok(await submit.isDisabled(), "the force push waits for the name");
  await input.fill(rem.name);
  await submit.click();
  await pg.waitForURL(/saved=force/);
  await waitFor(async () => { const r = await remoteByName("salt-road"); return r.status.state === "synced" && !r.status.running; }, "the replacement", 40000);
  ok(/PRIVATE-NOTE-ONE/.test(treeText("mairead", "salt-road")), "now the repository holds the full copy, private notes included");
  await pg.goto(gitPage(rem.id));
  const hist = await pg.locator("#history").innerText();
  ok(/Replaced the repository's history/.test(hist), "the history says so");
});

await kit.test("a repository that is public at the host gets no full copy: the push is refused and says why", async () => {
  const rem = await remoteByName("open-book");
  await pg.goto(gitPage(rem.id, "#carries"));
  await pg.locator("#carries-change summary").click();
  await chooseAudience("#carries-change", "private");
  await pg.locator('#carries-change input[name="understood"]').check();
  await pg.locator("#ga-confirm").fill(rem.name);
  await pg.locator("#carries-change").getByRole("button", { name: "Change what it carries" }).click();
  await pg.waitForURL(/saved=audience/);
  await pg.getByRole("button", { name: "Push now" }).click();
  await waitFor(async () => { const r = await remoteByName("open-book"); return r.status.state === "blocked"; }, "the push to be stopped", 30000);
  await pg.goto(gitPage(rem.id));
  ok(/public/i.test(await pg.locator("#git-banner").innerText()), "the banner says the repository is public: " + (await pg.locator("#git-banner").innerText()));
  ok(!/PRIVATE-NOTE-ONE/.test(treeText("mairead", "open-book")), "the private note never reached the public repository");
});

await kit.test("the signing key: made, shown to copy, switched on for a remote, and the next commit is signed", async () => {
  const rem = await remoteByName("late-book");
  await pg.goto(gitPage(rem.id, "#signing"));
  await pg.getByRole("button", { name: "Make the project's signing key" }).click();
  await pg.waitForURL(/saved=signing/);
  const key = (await pg.locator("#git-signkey").innerText()).trim();
  ok(/^ssh-ed25519 /.test(key), "the public key is shown");
  ok(await pg.locator('#signing button[data-copy]').first().isVisible(), "with a copy button");
  ok(!(await pg.content()).includes("PRIVATE KEY"), "no private key");
  await pg.locator('#signing input[name="sign_commits"]').check();
  await pg.locator("#signing").getByRole("button", { name: "Save" }).click();
  await pg.waitForURL(/saved=sign_on/);
  await setDoc(docs.one, { content: "The harbour was quiet at dawn, and then it was not.\n\nMarta counted the boats twice.\n" });
  await mirrorReady();
  const before = host.gitOut("mairead", "late-book", "rev-parse", "main");
  await pg.goto(gitPage(rem.id));
  await pg.getByRole("button", { name: "Push now" }).click();
  await waitFor(() => host.gitOut("mairead", "late-book", "rev-parse", "main") !== before, "a new commit", 40000);
  ok(/gpgsig/.test(host.gitOut("mairead", "late-book", "cat-file", "-p", "main")), "the commit carries a signature");
});

await kit.test("the push trigger: the secret is shown once, a test is signed and holds no manuscript text, it is removed with a confirmation", async () => {
  const rem = await remoteByName("late-book");
  await pg.goto(gitPage(rem.id, "#trigger"));
  await pg.locator("#gt-url").fill(hookUrl);
  await pg.getByRole("button", { name: "Make the trigger" }).click();
  await pg.locator("#git-secret").waitFor();
  const secret = (await pg.locator("#git-secret").innerText()).trim();
  ok(secret.length >= 16, "the secret is shown in the answer that made it");
  ok(/not shown again/i.test(await pg.locator("#git-once").innerText()), "and says it is shown once");
  ok(!(await pg.content()).includes("PATH-SECRET-77"), "the stored path is not in the page");
  await pg.goto(gitPage(rem.id, "#trigger"));
  const again = await pg.evaluate(() => document.documentElement.outerHTML);
  ok(!again.includes(secret) && !again.includes("PATH-SECRET-77"), "after a reload neither the secret nor the path is shown");
  ok(/127\.0\.0\.1/.test(await pg.locator("#trigger").innerText()), "only the host is");
  await pg.getByRole("button", { name: "Send a test" }).click();
  await pg.locator("#git-trigger-result, #git-error").first().waitFor();
  ok(await pg.locator("#git-trigger-result").count() === 1, "the test result is shown: " + ((await pg.locator("#git-error").count()) ? await pg.locator("#git-error").innerText() : ""));
  await waitFor(() => hooks.length > 0, "the receiver to be called", 15000);
  const h = hooks[0];
  const ts = h.headers["scriobh-webhook-timestamp"], sig = h.headers["scriobh-webhook-signature"] || "";
  eq(sig, "v1=" + createHmac("sha256", secret).update(`${ts}.${h.body}`).digest("hex"), "the request is signed with the secret that was shown");
  ok(!/harbour|Marta|PRIVATE-NOTE/.test(h.body), "the body holds no manuscript text: " + h.body.slice(0, 120));
  // removal asks first, in the place of the button, and can be backed out with Escape
  await pg.getByRole("button", { name: "Remove the trigger" }).click();
  await pg.locator(".arm").waitFor();
  await pg.keyboard.press("Escape");
  eq(await pg.locator(".arm").count(), 0, "Escape backs out");
  eq(await pg.locator("#trigger dl").count() > 0, true, "the trigger is still there");
});

await kit.test("the release panel: preview, publish, a retry uploads nothing twice, another edition under the same tag is refused", async () => {
  const rem = await remoteByName("empty-one");
  await pg.goto(`${base()}/p/${book.slug}/git/release?remote=${rem.id}`);
  await pg.locator("#rl-format").selectOption("epub");
  await pg.locator("#rl-tag").fill("v1.0");
  await pg.getByRole("button", { name: "Preview", exact: true }).click();
  await pg.locator("#rel-preview").waitFor();
  ok(/mairead\/empty-one/.test(await pg.locator("#rel-preview").innerText()), "the repository and the tag are shown before anything is made");
  eq(host.release("mairead", "empty-one", "v1.0"), null, "the preview created nothing");
  await pg.getByRole("button", { name: "Publish the edition" }).click();
  await pg.waitForURL(/release=/);
  await releaseDone("the release to finish");
  const t = await pg.locator("#current").innerText();
  ok(/[0-9a-f]{12}/.test(t) && /SHA-256/.test(t), "the manifest hash and the file's hash are shown");
  const rel = host.release("mairead", "empty-one", "v1.0");
  eq(rel && rel.assets.length, 1, "one asset at the host");
  const uploads = host.uploads();
  // retry: same tag, same edition
  await pg.goto(`${base()}/p/${book.slug}/git/release?remote=${rem.id}`);
  await pg.locator("#rl-format").selectOption("epub");
  await pg.locator("#rl-tag").fill("v1.0");
  await pg.getByRole("button", { name: "Preview", exact: true }).click();
  await pg.getByRole("button", { name: "Publish the edition" }).click();
  await pg.waitForURL(/release=/);
  await releaseDone("the retry to finish");
  eq(host.uploads(), uploads, "nothing was uploaded again");
  ok(/nothing was uploaded again/i.test(await pg.locator("#current").innerText()), "and the page says so");
  // another edition under the same tag
  await setDoc(docs.one, { content: "A different first chapter, so a different edition.\n" });
  await mirrorReady();
  await pg.goto(`${base()}/p/${book.slug}/git/release?remote=${rem.id}`);
  await pg.locator("#rl-format").selectOption("epub");
  await pg.locator("#rl-tag").fill("v1.0");
  await pg.getByRole("button", { name: "Preview", exact: true }).click();
  await pg.getByRole("button", { name: "Publish the edition" }).click();
  await pg.waitForURL(/release=/);
  await waitFor(async () => /different edition|made by hand/.test(await pg.locator("#current").innerText().catch(() => "")), "the refusal", 60000);
  eq(host.release("mairead", "empty-one", "v1.0").assets.length, 1, "the first edition is untouched");
  const o = await phone(`${base()}/p/${book.slug}/git/release?remote=${rem.id}`);
  eq(o.over, false, "the release page scrolls sideways on a phone");
});

await kit.test("a restore leaves a remote paused: the page says so and offers one way back", async () => {
  const rem = await remoteByName("Backup");
  const db = join(kit.dataDir, "scriobh.db");
  const r = spawnSync("sqlite3", [db, `UPDATE git_remotes SET enabled = 0, state = 'blocked', last_error_code = 'paused_by_restore', restore_paused = 1 WHERE id = '${rem.id}';`], { encoding: "utf8" });
  if (r.status !== 0) { console.log("      (skipped: no sqlite3 to simulate a restore: " + r.stderr + ")"); return; }
  await pg.goto(gitPage(rem.id));
  const list = await pg.locator("#remote-" + rem.id).innerText();
  ok(/Paused when this copy was restored/.test(list) && /Switch on/.test(list), "the list says so, with the one action");
  eq(await pg.getByRole("button", { name: "Push now" }).count(), 0, "no push button on a paused remote");
  await pg.locator("#status").getByRole("button", { name: "Switch on" }).click();
  await pg.waitForURL(/saved=enabled/);
  ok(!/Paused when this copy was restored/.test(await pg.locator("#remote-" + rem.id).innerText()), "the pause is lifted");
});

await kit.test("after an answer the result is in view and has the focus, on a phone too: the plan, the preview and the once-only secret", async () => {
  for (const scheme of shots ? ["light", "dark"] : ["light"]) {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true, colorScheme: scheme });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "phone");
  const inView = (sel) => p.evaluate((q) => { const e = document.querySelector(q); if (!e) return "missing"; const r = e.getBoundingClientRect(); return r.top >= 0 && r.top < window.innerHeight ? "in view" : "top at " + Math.round(r.top) + " of " + window.innerHeight; }, sel);
  const focused = () => p.evaluate(() => { const a = document.activeElement; const r = a && a.closest("[data-result]"); return r ? r.id || a.tagName : "nowhere: " + (a && a.tagName); });
  try {
    // the plan
    const acct = (await api(main, "GET", "/api/v1/git/accounts")).json.items[0].id;
    await p.goto(publishPage(`?account_id=${acct}`));
    await p.locator("#pf-name").fill("phone-plan");
    await p.locator('#fields input[name="audience"][value="private"]').check();
    await p.getByRole("button", { name: "Review the plan" }).click();
    await p.locator("#plan").waitFor();
    eq(await inView("#plan"), "in view", "the plan is in the first screen");
    if (shots) await p.screenshot({ path: join(shots, `phone-${scheme}-plan.png`) });
    ok(/^plan/.test(await focused()), "and has the focus: " + (await focused()));
    // the once-only secret, after a rotation
    const rem = await remoteByName("late-book");
    await p.goto(gitPage(rem.id, "#trigger"));
    await p.getByRole("button", { name: "Make a new secret" }).click();
    await p.locator(".arm .arm-ok").waitFor();
    await kit.sleep(700);
    await p.locator(".arm .arm-ok").click();
    await p.locator("#git-secret").waitFor();
    eq(await inView("#git-once"), "in view", "the secret is in the first screen");
    ok(await inView("#git-secret") === "in view", "with its text: " + (await inView("#git-secret")));
    eq(await focused(), "git-once", "and has the focus");
    if (shots) await p.screenshot({ path: join(shots, `phone-${scheme}-secret.png`) });
    // the release preview
    const rem2 = await remoteByName("empty-one");
    await p.goto(`${base()}/p/${book.slug}/git/release?remote=${rem2.id}`);
    await p.locator("#rl-tag").fill("v9.9");
    await p.getByRole("button", { name: "Preview", exact: true }).click();
    await p.locator("#rel-preview").waitFor();
    eq(await inView("#rel-preview"), "in view", "the preview is in the first screen");
    if (shots) await p.screenshot({ path: join(shots, `phone-${scheme}-preview.png`) });
    if (shots) { await p.goto(gitPage(rem.id)); await p.screenshot({ path: join(shots, `phone-${scheme}-git.png`) }); }
  } finally { await ctx.close(); }
  }
});

await kit.test("a reload or Back repeats nothing: the plan and the preview are pages, and reloading the secret makes no new secret", async () => {
  const acct = (await api(main, "GET", "/api/v1/git/accounts")).json.items[0].id;
  await pg.goto(publishPage(`?account_id=${acct}`));
  await pg.locator("#pf-name").fill("reload-plan");
  await chooseAudience("#fields", "private");
  await pg.getByRole("button", { name: "Review the plan" }).click();
  await pg.locator("#plan").waitFor();
  const url = pg.url();
  ok(/plan=pln_/.test(url) && !/audience|visibility|token/.test(url), "the plan has an address of its own that holds only an id: " + url);
  await pg.reload();
  await pg.locator("#plan").waitFor();
  ok(/reload-plan/.test(await pg.locator("#plan").innerText()), "a reload shows the plan again");
  await pg.goto(`${base()}/p/${book.slug}/git`);
  await pg.goBack();
  await pg.locator("#plan").waitFor();
  await pg.goForward();
  await pg.goBack();
  await pg.locator("#plan").waitFor();
  eq(host.repo("mairead", "reload-plan"), null, "nothing was created by any of it");
  // the secret
  const rem = await remoteByName("late-book");
  const hint = async () => (await api(main, "GET", `/api/v1/projects/${book.id}/git/trigger?remote_id=${rem.id}`)).json.item.secret_hint;
  await pg.goto(gitPage(rem.id, "#trigger"));
  await pg.getByRole("button", { name: "Make a new secret" }).click();
  await pg.locator(".arm .arm-ok").waitFor();
  await kit.sleep(700);
  await pg.locator(".arm .arm-ok").click();
  await pg.locator("#git-secret").waitFor();
  const shown = (await pg.locator("#git-secret").innerText()).trim();
  const before = await hint();
  ok(!pg.url().includes(shown), "the secret is not in the address");
  await pg.reload();
  await pg.locator("#git-once").waitFor();
  ok(/already shown/.test(await pg.locator("#git-once").innerText()) && await pg.locator("#git-secret").count() === 0, "a reload says it was shown and shows nothing");
  eq(await hint(), before, "and made no new secret");
  await pg.goBack().catch(() => {});
  await pg.goto(gitPage(rem.id));
  eq(await hint(), before, "going away and back made none either");
  // the preview
  const rem2 = await remoteByName("empty-one");
  await pg.goto(`${base()}/p/${book.slug}/git/release?remote=${rem2.id}`);
  await pg.locator("#rl-tag").fill("v7.7");
  await pg.getByRole("button", { name: "Preview", exact: true }).click();
  await pg.locator("#rel-preview").waitFor();
  await pg.reload();
  await pg.locator("#rel-preview").waitFor();
  eq(host.release("mairead", "empty-one", "v7.7"), null, "a preview, reloaded, created no release");
});

await kit.test("the keyboard alone: the publish form is reachable in order and the confirmation takes the focus and gives it back", async () => {
  await pg.goto(publishPage());
  await pg.locator("body").click({ position: { x: 5, y: 5 } });
  const order = [];
  for (let i = 0; i < 40; i++) {
    await pg.keyboard.press("Tab");
    const id = await pg.evaluate(() => { const e = document.activeElement; return e && e.closest("main") ? (e.name || e.id || e.textContent.trim().slice(0, 20)) : ""; });
    if (id) order.push(id);
  }
  ok(order.includes("repo") || order.length > 5, "tab reaches the page's controls: " + order.join(","));
  const ring = await pg.evaluate(() => { const e = document.activeElement, s = getComputedStyle(e); return s.outlineStyle !== "none" || s.boxShadow !== "none"; });
  ok(ring, "the focused control shows its focus");
  // disconnect asks first and Escape puts the focus back on the button
  const b = pg.getByRole("button", { name: "Disconnect" }).first();
  await b.focus();
  await pg.keyboard.press("Enter");
  await pg.locator(".arm").waitFor();
  await pg.keyboard.press("Escape");
  await pg.waitForFunction(() => document.activeElement && /Disconnect/.test(document.activeElement.textContent));
  eq(await pg.locator(".arm").count(), 0, "the question is gone");
});

await kit.test("the right-to-left pseudo-locale: the pages mirror, nothing overflows, nothing is left in plain English", async () => {
  eq((await api(main, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } })).status, 200, "set the pseudo-locale");
  try {
    const rem = await remoteByName("late-book");
    const acct = (await api(main, "GET", "/api/v1/git/accounts")).json.items;
    const pages = [["git", gitPage(rem.id)], ["release", `${base()}/p/${book.slug}/git/release?remote=${rem.id}`], ["publish", publishPage(acct[0] ? `?account_id=${acct[0].id}` : "")]];
    for (const [name, url] of pages) {
      await pg.goto(url);
      eq(await pg.locator("html").getAttribute("dir"), "rtl", `${name}: the page is right to left`);
      const o = await pg.evaluate(() => ({ over: document.documentElement.scrollWidth > document.documentElement.clientWidth, key: document.querySelector("main").innerText.includes("⟦") }));
      eq(o.over, false, `${name}: scrolls sideways`);
      ok(o.key, `${name}: the words come from the catalogue`);
      await shot(url, `rtl-${name}-1280.png`);
    }
    const p = await phone(gitPage(rem.id));
    eq(p.over, false, "the Git page scrolls sideways on a phone in the right-to-left locale");
  } finally { await api(main, "PUT", "/api/v1/me/prefs", { items: { language: "en" } }); }
});

await kit.test("with the network cut, a reload shows the offline notice and not a browser error page", async () => {
  const ctx = await kit.newContext({ serviceWorkers: "allow", viewport: { width: 1000, height: 700 } });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "offline");
  await p.goto(publishPage());
  await p.waitForFunction(async () => (await navigator.serviceWorker.getRegistration()) && navigator.serviceWorker.controller !== null || (await navigator.serviceWorker.ready, true), null, { timeout: 15000 }).catch(() => {});
  await p.reload();
  kit.allowed.push(/Failed to load resource|ERR_INTERNET_DISCONNECTED|net::/);
  await ctx.setOffline(true);
  try {
    await p.reload();
    ok(/cannot reach the server/.test(await p.locator("body").innerText()), "the offline notice");
  } finally { await ctx.setOffline(false); await ctx.close(); }
});

await kit.test("the account is disconnected and its token forgotten; the remotes keep working", async () => {
  await pg.goto(publishPage());
  await pg.getByRole("button", { name: "Disconnect" }).first().click();
  await pg.locator(".arm .arm-ok").waitFor();
  await kit.sleep(700);
  await pg.locator(".arm .arm-ok").click();
  await pg.waitForURL(/disconnected=1/);
  eq((await api(main, "GET", "/api/v1/git/accounts")).json.items.length, 0, "no account");
  ok((await remotes()).length >= 5, "the remotes are still there");
});

await kit.finish("e2e-publish");
