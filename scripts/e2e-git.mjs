#!/usr/bin/env node
// End-to-end test of the Git sync page against a real SSH Git server (the development one under
// cmd/scriobh-dev-gitserver, which hands the Git protocol to the system git): add a remote, copy the deploy key,
// compare and accept the host key, test the connection, push, read the history, provoke a rejection with a commit
// from outside, force push and verify the bundle, meet a changed host key, meet a secret in the text, remove the
// remote. Skips (exit 0) when git is not installed.
//
//   node scripts/e2e-git.mjs [--only substring]
import { existsSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { available, buildGitServer, startGitServer } from "./gitserver.mjs";

if (!available()) {
  console.log("e2e-git: SKIPPED (the git program is not on PATH; the dev Git server needs it)");
  process.exit(0);
}
const kit = await createKit("e2e-git", { env: { SCRIOBH_GIT_ALLOW_PRIVATE: "1" } });
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;
const server = await startGitServer({ bin: buildGitServer(kit.tmp), root: join(kit.tmp, "gitroot") });
const origFinish = kit.finish;
kit.finish = async (n) => { await server.stop().catch(() => {}); return origFinish(n); };

let book, doc, remoteId;
const page = () => `${base()}/p/${book.slug}/git`;
const live = () => pg.locator("#git-live");
const headline = async () => (await pg.locator("#git-headline").innerText()).trim();
const mirrorReady = async () => {
  await waitFor(async () => { const m = (await api(main, "GET", `/api/v1/projects/${book.id}/mirror`)).json.item; return m && m.enabled && !m.pending && m.files > 0; }, "the Markdown copy to be written", 15000);
};
const setText = async (md) => {
  const d = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  const r = await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: d.revision, content: md });
  eq(r.status, 200, "save the text");
};
const remote = async () => (await api(main, "GET", `/api/v1/projects/${book.id}/git/remote`)).json.item;
const pushNow = async () => {
  await pg.goto(page());
  await pg.getByRole("button", { name: "Push now" }).click();
};
const waitIdle = async (what = "the push to finish") => {
  await waitFor(async () => { const r = await remote(); return !r.status.running; }, what, 30000);
};
const phoneOverflow = async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "phone");
  await p.goto(page());
  const o = await p.evaluate(() => ({ over: document.documentElement.scrollWidth > document.documentElement.clientWidth, small: [...document.querySelectorAll("main button, main a.btn, main input:not([type=hidden]):not([type=checkbox]):not([type=radio]), main select")].filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && r.height < 43.5; }).map((e) => (e.textContent || e.name || e.id).trim().slice(0, 24)) }));
  await ctx.close();
  return o;
};

const pushFromPalette = async () => {
  await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.write)), "the writing view", 10000);
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press("ControlOrMeta+Shift+p");
  await pg.locator("#palette-input").fill("Push to Git");
  await pg.keyboard.press("Enter");
};

await kit.test("setup: a project with text, and an empty repository on the dev server", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "Git Book", kind: "novel" });
  book = made.json.item;
  doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  await setText("The harbour was quiet at dawn.\n\nMarta counted the boats twice.\n");
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: 2, title: "The Quiet Harbour" });
  await server.control("POST", "/repos", { name: "book" });
  await mirrorReady();
});

await kit.test("the palette's Push to Git now says plainly when there is no remote", async () => {
  kit.allowed = [/status of 409/, /status of 404/, /status of 422/];
  await pushFromPalette();
  await pg.locator("#toasts .toast").first().waitFor();
  const t = await pg.locator("#toasts").innerText();
  ok(/remote|Git sync/i.test(t), "a toast says why nothing was pushed: " + t);
});

await kit.test("the empty state offers the form, with presets, and nothing else", async () => {
  await pg.goto(page());
  ok(await pg.getByRole("heading", { name: "Add a remote" }).isVisible(), "the add form is offered");
  await pg.getByRole("link", { name: "GitHub" }).click();
  await pg.waitForURL(/preset=github/);
  eq(await pg.locator("#gf-url").inputValue(), "ssh://git@github.com/", "GitHub fills in its host");
  ok(/write access/i.test(await pg.locator("main").innerText()), "the preset says where the deploy key goes");
  await pg.getByRole("link", { name: "Any Git server" }).click();
  ok(!(await pg.getByRole("button", { name: "Push now" }).count()), "no push button without a remote");
  const o = await phoneOverflow();
  eq(o.over, false, "the empty state scrolls sideways on a phone");
});

await kit.test("add a remote: the deploy key is shown with a copy button and the checklist is honest", async () => {
  await pg.goto(page());
  await pg.getByLabel("Name", { exact: true }).fill("Backup");
  await pg.locator("#gf-url").fill(server.sshUrl("book"));
  await pg.locator("#gf-an").fill("");
  await pg.locator('#add input[name="audience"][value="private"]').check(); // what a remote carries is chosen, never defaulted
  await pg.getByRole("button", { name: "Add the remote" }).click();
  await pg.waitForURL(/saved=created/);
  ok(await pg.getByText("The remote is added. Finish the steps under Setup.").isVisible(), "the confirmation");
  ok(/Not ready yet/.test(await headline()), "the banner says not ready: " + (await headline()));
  const key = (await pg.locator("#git-pubkey").innerText()).trim();
  ok(/^ssh-ed25519 /.test(key), "the public key is shown: " + key.slice(0, 30));
  ok(await pg.getByRole("button", { name: "Copy the key" }).isVisible(), "the copy button appears with script");
  ok(!(await pg.content()).includes("PRIVATE KEY"), "no private key anywhere");
  const items = await pg.locator(".steps li").evaluateAll((l) => l.map((x) => x.dataset.done));
  eq(items.join(), "true,false", "the key is made, the host key is not accepted");
  remoteId = (await remote()).id;
  await server.control("POST", "/authorize", { public_key: key });
});

await kit.test("scan the host key, compare it, accept it; the box must be ticked", async () => {
  kit.allowed = [/status of 422/];
  await pg.goto(page());
  await pg.getByRole("button", { name: "Scan the host key" }).click();
  const fp = (await server.info()).host_keys[0].fingerprint;
  await waitFor(async () => (await pg.locator(".fingerprints").innerText()).includes(fp), "the scan to show the host's fingerprint");
  const accept = pg.getByRole("button", { name: "Accept this host key" });
  const compared = pg.getByLabel("I compared this fingerprint with the one the host publishes");
  eq(await compared.getAttribute("required"), "", "the comparison box is required");
  // Without the box the server still refuses.
  const bad = await pg.evaluate(async ([id, fingerprint, csrf]) => {
    const r = await fetch(`/ui/projects/${id}/git/hostkey-accept`, { method: "POST", headers: { "X-CSRF-Token": csrf, "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ remote_id: "", fingerprint }) });
    return r.redirected && /role="alert"/.test(await r.text()) ? 422 : r.status; // a refusal is a redirect to the page that shows it
  }, [book.id, fp, await pg.locator('meta[name="csrf-token"]').getAttribute("content")]);
  eq(bad, 422, "accepting without comparing is refused");
  await compared.check();
  await accept.click();
  await pg.waitForURL(/saved=hostkey/);
  const r = await remote();
  eq(r.host_key && r.host_key.fingerprint, fp, "the pinned key is the one scanned");
  eq((await pg.locator(".steps li").evaluateAll((l) => l.map((x) => x.dataset.done))).join(), "true,true", "both steps are done");
});

await kit.test("test the connection: it works, and the repository is empty", async () => {
  await pg.goto(page());
  await pg.getByRole("button", { name: "Test the connection" }).click();
  await pg.getByText("The connection works.").waitFor();
  ok(/empty/i.test(await pg.locator("#test").innerText()), "it says the repository is empty");
});

await kit.test("push now: it runs, finishes, is listed, and says what it sent", async () => {
  await pg.goto(page());
  await pg.getByRole("button", { name: "Push now" }).click();
  await pg.waitForURL(/saved=pushed/);
  await waitFor(async () => (await remote()).status.state === "synced", "the remote to be synced", 30000);
  await pg.goto(page());
  ok(/Up to date|Pushed/.test(await headline()), "the banner: " + (await headline()));
  ok(await pg.locator("#history li").first().innerText().then((t) => /Pushed/.test(t)), "the history lists it");
  await pg.getByRole("link", { name: "What was sent" }).first().click();
  await pg.getByRole("heading", { name: "Everything the commit holds" }).waitFor();
  const files = await pg.locator(".manifest tbody td:first-child").allInnerTexts();
  ok(files.includes("scriobh.json") && files.some((f) => f.startsWith("manuscript/")), "the manifest lists the project file and the chapter: " + files.join(","));
  const log = (await server.control("GET", `/log?repo=book&branch=main`)).items;
  eq(log.length, 1, "one commit on the server");
  ok(/first push/.test(log[0].subject), "the first commit says so: " + log[0].subject);
  eq(log[0].author, "Scríobh", "the neutral identity");
});

await kit.test("the palette's Push to Git now starts a push and says where to follow it", async () => {
  await setText("The harbour was quiet at dawn.\n\nMarta counted the boats twice, and then once more.\n");
  await mirrorReady();
  await pushFromPalette();
  await pg.locator("#toasts .toast").first().waitFor();
  ok(/push was started|already running/i.test(await pg.locator("#toasts").innerText()), "the toast: " + (await pg.locator("#toasts").innerText()));
  await waitFor(async () => (await remote()).status.state === "synced" && !(await remote()).status.running, "the push to finish", 30000);
});

await kit.test("someone else pushes: the next push is refused, with their commits listed, and nothing is overwritten", async () => {
  const theirs = await server.control("POST", "/foreign-commit", { repo: "book", branch: "main", message: "Fix a typo in the README", files: { "README.md": "hello" } });
  await setText("The harbour was quiet at dawn, and the tide had gone out.\n\nMarta counted the boats twice.\n");
  await mirrorReady();
  await pushNow();
  await waitFor(async () => (await remote()).status.state === "rejected", "the push to be refused", 30000);
  await pg.goto(page());
  ok(/changes this project did not make/.test(await headline()), "the banner: " + (await headline()));
  await pg.locator("#diverged").waitFor();
  ok((await pg.locator("#diverged .commits").innerText()).includes("Fix a typo in the README"), "their commit is listed");
  ok(/nothing was merged/i.test(await pg.locator("#diverged").innerText()), "it says nothing was merged");
  eq((await server.control("GET", "/head?repo=book&branch=main")).sha, theirs.sha, "the server still has their commit as head");
  ok(await pg.getByRole("link", { name: /^Import it/ }).isVisible(), "the import exit is offered");
  const o = await phoneOverflow();
  eq(o.over, false, "the rejected state scrolls sideways on a phone");
  kit.theirs = theirs.sha;
});

await kit.test("force push: the name must be typed, a bundle of the old history is written and is a real bundle", async () => {
  await pg.goto(page());
  await pg.getByRole("link", { name: /^Replace the history/ }).click();
  await pg.locator("#git-force-form").waitFor();
  await sleep(300); // the dialog's script settles a moment after the form arrives
  const go = pg.getByRole("button", { name: "Replace the history", exact: true });
  ok(await go.isDisabled(), "the button waits for the typed name");
  await pg.locator("#gfp-confirm").fill("backup");
  ok(await go.isDisabled(), "a name in the wrong case is not enough");
  await pg.locator("#gfp-confirm").fill("Backup");
  ok(await go.isEnabled(), "the exact name enables it");
  ok(/data\/backups\/git/.test(await pg.locator("#git-force-form").innerText()), "it says where the bundle goes");
  await go.click();
  await pg.waitForURL(/saved=force/);
  await waitFor(async () => (await remote()).status.state === "synced", "the replacement to finish", 40000);
  const pushes = (await api(main, "GET", `/api/v1/projects/${book.id}/git/pushes`)).json.items;
  const forced = pushes.find((p) => p.forced);
  ok(forced, "a forced push is recorded");
  const dir = join(kit.dataDir, "backups", "git");
  const found = [];
  const walk = (d) => { for (const n of readdirSync(d)) { const p = join(d, n); if (statSync(p).isDirectory()) walk(p); else if (n.endsWith(".bundle")) found.push(p); } };
  ok(existsSync(dir), "backups/git exists");
  walk(dir);
  ok(found.length >= 1, "a bundle file was written");
  const v = await server.control("POST", "/verify-bundle", { path: found[0] });
  ok(v.ok, "git accepts the bundle: " + JSON.stringify(v));
  ok(v.heads.some((h) => h.sha === kit.theirs), "the bundle holds the commit that was overwritten");
  await pg.goto(`${base()}/p/${book.slug}/git/pushes/${forced.id}`);
  ok(/bundle/i.test(await pg.locator("main").innerText()), "the push page mentions the bundle");
});

await kit.test("import the repository through the remote as a new project (a real clone over SSH)", async () => {
  await pg.goto(`${base()}/import/git?project=${book.slug}`);
  ok(await pg.locator("#rg-src-remote").isChecked(), "the remote is preselected");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await pg.getByRole("button", { name: /^Import \d+ document/ }).waitFor({ timeout: 60000 });
  const preview = await pg.locator("main").innerText();
  ok(/Quiet Harbour|Chapter|chapter/.test(preview) || /document/.test(preview), "a preview of the repository's chapters is shown");
  await pg.getByRole("button", { name: /^Import \d+ document/ }).click();
  await pg.waitForURL((u) => /^\/p\//.test(u.pathname) && u.pathname.split("/")[2] !== book.slug, { timeout: 30000 });
  const all = (await api(main, "GET", "/api/v1/projects")).json.items;
  ok(all.length === 2, "a second project exists: " + all.map((x) => x.title).join(","));
  const copy = all.find((x) => x.id !== book.id);
  const docs = (await api(main, "GET", `/api/v1/projects/${copy.id}/docs`)).json.items;
  ok(docs.some((d) => d.word_count > 5), "the imported project holds the text");
  const sources = (await api(main, "GET", `/api/v1/projects/${copy.id}`)).json.item;
  ok(sources, "the new project opens");
});

await kit.test("the remote's settings can be saved as they are, and changed", async () => {
  await pg.goto(page());
  await pg.locator("#settings").getByRole("button", { name: "Save", exact: true }).click();
  await pg.waitForURL(/saved=settings/);
  await waitIdle();
  eq((await remote()).status.state, "synced", "saving the settings unchanged leaves the remote synced");
  await pg.locator("#settings").getByLabel("Only when I press Push now").check();
  await pg.locator("#gs-an").fill("Wren Writer");
  await pg.locator("#gs-ae").fill("wren@example.com");
  await pg.locator("#settings").getByRole("button", { name: "Save", exact: true }).click();
  await pg.waitForURL(/saved=settings/);
  // what the remote carries is its own form, so a routine save cannot change it
  await pg.locator("#carries-change summary").click();
  await pg.locator('#carries-change input[name="include_notes"]').check();
  await pg.locator("#carries-change").getByRole("button", { name: "Change what it carries" }).click();
  await pg.waitForURL(/saved=audience/);
  const r = await remote();
  eq(r.push_mode, "manual", "the push mode");
  eq(r.include_notes, true, "notes are on");
  eq(r.author, "Wren Writer <wren@example.com>", "the commit identity");
  await pg.locator("#settings").getByLabel("After I stop writing (30 seconds later, and never more than five minutes behind)").check();
  await pg.locator("#gs-an").fill("");
  await pg.locator("#gs-ae").fill("");
  await pg.locator("#settings").getByRole("button", { name: "Save", exact: true }).click();
  await pg.waitForURL(/saved=settings/);
  eq((await remote()).author, "Scríobh <noreply@scriobh>", "the neutral identity is back");
});

await kit.test("a failed connection test is a result with advice, and the key can be authorised again", async () => {
  const key = (await pg.goto(page()), (await pg.locator("#git-pubkey").innerText()).trim());
  const fp = (await remote()).key_fingerprint;
  await server.control("POST", "/deauthorize", { fingerprint: fp });
  await pg.getByRole("button", { name: "Test the connection" }).click();
  await pg.getByText("The connection failed.").waitFor();
  ok(/deploy key|write access/i.test(await pg.locator("#test").innerText()), "the advice names the deploy key: " + (await pg.locator("#test").innerText()));
  await server.control("POST", "/authorize", { public_key: key });
  await pg.getByRole("button", { name: "Test the connection" }).click();
  await pg.getByText("The connection works.").waitFor();
});

await kit.test("a changed host key stops the push and is shown as a hard warning; it can be replaced on purpose", async () => {
  await server.control("POST", "/rotate-host-key", {});
  await setText("The harbour was quiet at dawn, and the tide had gone out. Nobody came.\n\nMarta counted the boats twice.\n");
  await mirrorReady();
  await pushNow();
  await waitFor(async () => { const s = (await remote()).status; return s.state === "blocked" && s.last_error_code === "host_key_changed"; }, "the push to be blocked", 30000);
  await pg.goto(page());
  ok(/host key of .* has changed/.test(await headline()), "the banner: " + (await headline()));
  await pg.getByRole("button", { name: "Scan again" }).click();
  await pg.getByText("The host key has changed.").waitFor();
  ok((await pg.locator(".banner-danger").getAttribute("role")) === "alert", "the warning is an alert");
  const replace = pg.getByRole("button", { name: "Replace the host key" });
  await pg.getByLabel("I know why the host key changed and have checked this new one").check();
  await pg.getByLabel("I compared this fingerprint with the one the host publishes").check();
  await replace.click();
  await pg.waitForURL(/saved=hostkey/);
  await pushNow();
  await waitFor(async () => (await remote()).status.state === "synced", "the push to go through again", 15000).catch(async (e) => { throw new Error(e.message + " " + JSON.stringify((await remote()).status).slice(0, 500)); });
});

await kit.test("a secret in the text blocks the push, names the file, and the owner can mark it as fine", async () => {
  const key = "-----BEGIN RSA PRIVATE KEY-----\n" + "MIIEowIBAAKCAQEA" + "x7Qk9".repeat(30) + "\n-----END RSA PRIVATE KEY-----\n";
  await setText("Marta found a letter.\n\n" + key + "\nShe did not read it.\n");
  await mirrorReady();
  await pushNow();
  await waitFor(async () => { const s = (await remote()).status; return s.state === "blocked" && s.last_error_code === "secret_in_tree"; }, "the push to be blocked for the secret", 30000);
  await pg.goto(page());
  ok(/something looks like a secret/.test(await headline()), "the banner: " + (await headline()));
  const item = pg.locator("#secrets li").first();
  ok(/manuscript\//.test(await item.innerText()), "the file is named: " + (await item.innerText()));
  ok(!(await pg.content()).includes("MIIEowIBAAKCAQEA"), "the text of the secret is never shown");
  ok(/covers exactly the text it has now/.test(await pg.locator("#secrets").innerText()), "the acknowledgement is explained");
  await pg.getByRole("button", { name: "It is not a secret, push this file" }).click();
  await pg.waitForURL(/saved=acked/);
  ok(await pg.locator("#acked").isVisible(), "the mark is listed");
  await pushNow();
  await waitFor(async () => (await remote()).status.state === "synced", "the marked file to be pushed", 15000).catch(async (e) => { throw new Error(e.message + " " + JSON.stringify((await remote()).status).slice(0, 500)); });
});

await kit.test("while a push runs the page asks every few seconds, redraws in place, announces the change, and stops when hidden", async () => {
  // Pushes on a loopback server are over in milliseconds, so the answers of the status route are staged.
  await waitIdle("any automatic push to finish");
  await pg.goto(page());
  const html = await (await pg.request.get(`${base()}/ui/projects/${book.id}/git/status`)).text();
  const running = html.replace('data-poll="false"', 'data-poll="true"').replace(/<strong id="git-headline">[^<]*<\/strong>/, '<strong id="git-headline">Pushing to 127.0.0.1 now.</strong>');
  let calls = 0;
  await pg.route("**/git/status*", async (route) => {
    calls++;
    await route.fulfill({ status: 200, contentType: "text/html", body: calls < 3 ? running : html });
  });
  await pg.evaluate(() => { document.getElementById("git-live").dataset.poll = "true"; document.dispatchEvent(new Event("visibilitychange")); });
  await waitFor(async () => calls >= 3, "three asks while the push runs", 12000);
  await waitFor(async () => (await headline()) !== "Pushing to 127.0.0.1 now.", "the redraw to show the end", 8000).catch(async (e) => { throw new Error(e.message + `; calls ${calls}; headline ${await headline()}; staged ${running.slice(0, 300).replace(/\s+/g, " ")}`); });
  ok(/pushed|Up to date/i.test(await headline()), "the banner is the finished one: " + (await headline()));
  const said = await pg.locator("#git-announce").innerText();
  ok(/Up to date|Pushed/i.test(said), "the change is announced in the live region: " + said);
  const before = calls;
  await sleep(4500);
  eq(calls, before, "no more asks once the push is over");
  // A hidden tab does not ask.
  await pg.evaluate(() => { document.getElementById("git-live").dataset.poll = "true"; Object.defineProperty(document, "hidden", { configurable: true, get: () => true }); document.dispatchEvent(new Event("visibilitychange")); });
  await sleep(4500);
  eq(calls, before, "a hidden page asks nothing");
  await pg.unroute("**/git/status*");
});

await kit.test("changing the address says what it removes, then walks the owner through setting up again", async () => {
  kit.allowed = [/status of 422/];
  await server.control("POST", "/repos", { name: "book2" });
  await pg.goto(page());
  await pg.locator("#address summary").click();
  // Without the tick the browser refuses, and the server does too.
  const none = await pg.evaluate(async ([id, rid, url, csrf]) => { const r = await fetch(`/ui/projects/${id}/git/address`, { method: "POST", headers: { "X-CSRF-Token": csrf, "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ remote_id: rid, url }) }); return r.redirected && /role="alert"/.test(await r.text()) ? 422 : r.status; }, [book.id, (await remote()).id, server.sshUrl("book2"), await pg.locator('meta[name="csrf-token"]').getAttribute("content")]);
  eq(none, 422, "the server refuses an address change that was not confirmed");
  await pg.locator("#ga-url").fill(server.sshUrl("book2"));
  await pg.getByLabel("I understand the saved key or token and the host key will be removed if the address changes").check();
  await pg.getByRole("button", { name: "Change the address" }).click();
  await pg.waitForURL(/saved=address/);
  const r = await remote();
  ok(!r.host_key && r.setup.includes("deploy_key") && r.setup.includes("host_key"), "the key and the host key were removed: " + JSON.stringify(r.setup));
  ok(/set them up again/.test(await pg.locator("main").innerText()), "the page walks the owner back through setup");
  await pg.getByRole("button", { name: "Make a deploy key" }).click();
  await pg.waitForURL(/saved=keygen/);
  const key = (await pg.locator("#git-pubkey").innerText()).trim();
  await server.control("POST", "/authorize", { public_key: key });
  await pg.getByRole("button", { name: "Scan the host key" }).click();
  await pg.getByLabel("I compared this fingerprint with the one the host publishes").check();
  await pg.getByRole("button", { name: "Accept this host key" }).click();
  await pg.waitForURL(/saved=hostkey/);
  await pg.getByRole("button", { name: "Test the connection" }).click();
  await pg.getByText("The connection works.").waitFor();
  await pushNow();
  await waitFor(async () => (await remote()).status.state === "synced", "the first push to the new address", 30000);
  eq((await server.control("GET", "/log?repo=book2&branch=main")).items.length, 1, "the new repository has the commit");
});

await kit.test("switch pushing off and on, change the address (with the warning), and remove the remote by typing its name", async () => {
  await pg.goto(page());
  await pg.getByRole("button", { name: "Switch pushing off" }).click();
  await pg.waitForURL(/saved=disabled/);
  ok(/switched off/.test(await headline()), "the banner says so: " + (await headline()));
  await pg.getByRole("button", { name: "Switch pushing on" }).click();
  await pg.waitForURL(/saved=enabled/);
  // The address form says what it will remove before it is saved, and refuses without the tick.
  await pg.locator("#address summary").click();
  ok(/removed/.test(await pg.locator("#address").innerText()), "the warning is on the form");
  // Remove the remote.
  const del = pg.getByRole("button", { name: "Remove the remote" });
  ok(await del.isDisabled(), "removal waits for the typed name");
  await pg.locator("#gd-confirm").fill("Backup");
  await del.click();
  await pg.waitForURL(/saved=deleted/);
  ok(await pg.getByRole("heading", { name: "Add a remote" }).isVisible(), "back to the empty state");
});

await kit.finish("e2e-git");
