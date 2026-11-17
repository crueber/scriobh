#!/usr/bin/env node
// End-to-end test of the repository import page in a real Chromium.
//
//   node scripts/e2e-importgit.mjs [--only substring]
//
// WHAT IS REAL: the server, the page, the form, the import.git_* ops for starting a fetch, following
// it, failing it, listing and throwing it away. A fetch that fails (an address the server may not
// connect to, a name that does not resolve) really runs and really fails, and the page follows it.
//
// WHAT IS NOT: a successful clone. The server only fetches https addresses, a local test server would
// need a certificate this process cannot make the server trust, and the file:// seam exists only in Go
// tests. So the ready state is stubbed in the browser (page.route): the job and preview JSON are the
// shapes the real ops return (the preview is a real answer of the folder importer's op, wrapped the
// way import.git_preview wraps it). The real clone, preview and apply over the ops are covered by
// TestImportRepoFlowAndList in internal/server and by the tests in internal/gitmirror.
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-importgit");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const open = async (page = pg, q = "") => { await page.goto(`${base()}/import/git${q}`); await page.waitForSelector("#rg-form"); };
const b64 = (s) => Buffer.from(s).toString("base64");
const status = () => pg.locator("#imp-status").innerText();

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
  eq(await pg.locator("#rg-token").inputValue(), "", "the token field is emptied as soon as it is sent");
  await waitFor(async () => (await pg.locator("#rg-job[data-bad]").count()) === 1, "the fetch to fail", 30000);
  const text = await pg.locator("#rg-job").innerText();
  ok(/not one this server may connect to/.test(text), "the failure in words: " + text);
  ok(/SCRIOBH_GIT_ALLOW_PRIVATE=1/.test(text) && /administrator|whoever runs this server/.test(text), "the plain explanation of the address rule: " + text);
  ok(!(await pg.content()).includes("tok_never_shown_again"), "the token is in the page");
  ok(!requests.some((u) => u.includes("tok_never")), "the token is in a URL");
  eq(await pg.evaluate(() => document.activeElement.id), "rg-job-h", "focus moves to the result");
  ok(/The fetch failed/.test(await status()), "the live region says it failed");
  // The list holds the failed fetch; throwing it away is a real DELETE.
  const li = pg.locator("#rg-jobs li").first();
  ok(/Failed/.test(await li.innerText()), "the list shows the failure: " + (await li.innerText()));
  ok(!(await pg.content()).includes("tok_never"), "the token is in the list");
  await li.getByRole("button", { name: /Throw away/ }).click();
  await waitFor(async () => (await pg.locator("#rg-jobs li").count()) === 0, "the failed fetch to leave the list");
  const jobs = (await api(main, "GET", "/api/v1/imports/git/jobs")).json.items;
  eq(jobs.length, 0, "the job is gone on the server");
  pg.removeAllListeners("request");
});

await kit.test("real fetch: a name that does not resolve, an ssh address, an empty form", async () => {
  kit.allowed = [/status of 422/];
  await open();
  await pg.locator("#rg-url").fill("https://no-such-host.invalid/x/y.git");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await waitFor(async () => (await pg.locator("#rg-job[data-bad]").count()) === 1, "the fetch to fail", 40000);
  const t1 = await pg.locator("#rg-job").innerText();
  ok(/could not be reached/.test(t1) && /try again|Check the address/i.test(t1), "unreachable in words: " + t1);
  await open();
  await pg.locator("#rg-url").fill("ssh://git@example.org/wren/x.git");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await pg.locator("#rg-job[data-bad]").waitFor();
  const t2 = await pg.locator("#rg-job").innerText();
  ok(/deploy key/.test(t2) && /https address/.test(t2), "an ssh address says what to do instead: " + t2);
  await pg.locator("#rg-url").fill("");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  ok(/Enter the repository/.test(await status()), "an empty address is asked for");
  eq(await pg.evaluate(() => document.activeElement.id), "rg-url", "focus returns to the address");
  // Clean up the failed fetch left by the first of these.
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
  await waitFor(async () => (await pg.locator("#rg-job[data-bad]").count()) === 1, "the fetch to fail", 30000);
  ok(/not one this server may connect to/.test(await pg.locator("#rg-job").innerText()), "the stored remote's address is held to the same rule");
  for (const j of (await api(main, "GET", "/api/v1/imports/git/jobs")).json.items) await api(main, "DELETE", `/api/v1/imports/git?job_id=${j.id}`);
  // Someone who is not the owner cannot ask for it.
  eq((await api(main, "GET", "/import/git?project=no-such-project")).status, 404, "an unknown project is not found");
});

// ---- the ready state, with the answers of the ops stubbed ---------------------------------------------------------------------
let preview = null;
await kit.test("stub: a real folder preview, wrapped as import.git_preview wraps it", async () => {
  const r = await api(main, "POST", "/api/v1/imports/preview", { name: "Salt Road", files: [
    { path: "one.md", data: b64("# The Start\n\nThe tide came in and the gulls wheeled over the quay.\n\n## First light\n\nGrey.\n\n## Second light\n\nWhite.\n"), size: 120 },
    { path: "two.md", data: b64("# The Middle\n\nSalt on the wind, and a lamp that nobody lit.\n"), size: 70 },
    { path: "cover.png", size: 300 }] });
  eq(r.status, 200, "folder preview: " + JSON.stringify(r.json));
  preview = r.json;
  ok(preview.nodes.length >= 2 && preview.token, "the preview has nodes and a token");
});

await kit.test("stub: following a running job asks every 2.5 s while the tab shows, nothing while it is hidden", async () => {
  kit.allowed = [/status of 404/]; // the stubbed project does not exist on the real server
  const job = (status, extra = {}) => ({ id: "imp_stub1", status, source_url: "https://git.example.org/wren/salt-road.git", branch: "main", created_at: new Date().toISOString(), ...extra });
  const seen = { polls: 0, starts: 0, previews: [], applies: [] };
  let phase = "running";
  await pg.route("**/api/v1/imports/git/preview", async (route) => {
    const b = route.request().postDataJSON();
    if (b.job_id) { seen.previews.push(b); return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ job: job("preview", { commit_sha: "9f3c2a1b77", summary: { documents: preview.documents, words: preview.words, layout: preview.layout, token: preview.token } }), preview }) }); }
    seen.starts++; seen.start = b;
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ job: job("running") }) });
  });
  await pg.route("**/api/v1/imports/git?job_id=*", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    seen.polls++;
    const j = phase === "running" ? job("running") : job("preview", { commit_sha: "9f3c2a1b77", summary: { documents: preview.documents, words: preview.words, layout: preview.layout, token: preview.token } });
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ item: j }) });
  });
  await pg.route("**/api/v1/imports/git", async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    seen.applies.push(route.request().postDataJSON());
    return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ project_id: "prj_stub", slug: "from-the-repo", title: "Imported", documents: preview.documents, words: preview.words, source_id: "src_1", first_document_id: "doc_stub", skipped_total: 1, warnings: [] }) });
  });
  await open();
  await pg.locator("#rg-url").fill("https://git.example.org/wren/salt-road.git");
  await pg.locator("#rg-branch").fill("main");
  await pg.locator("#rg-path").fill("manuscript");
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await pg.locator("#rg-job-h").waitFor();
  eq(seen.start.url, "https://git.example.org/wren/salt-road.git", "the address that was sent");
  eq(seen.start.branch, "main", "branch");
  eq(seen.start.path, "manuscript", "folder");
  ok(!("token" in seen.start), "no token was sent when none was typed");
  ok(/Fetching the repository/.test(await pg.locator("#rg-job").innerText()), "the progress words");
  ok(/cannot be stopped/.test(await pg.locator("#rg-job").innerText()), "a running fetch says it cannot be stopped");
  ok(/Being fetched/.test(await pg.locator("#rg-jobs li").first().innerText()), "the list shows it running");
  eq(await pg.locator("#rg-jobs li").first().getByRole("button", { name: /Throw away/ }).count(), 0, "no Throw away while it runs");
  // Visible: a poll arrives within about 3 seconds.
  await waitFor(() => seen.polls >= 1, "the first poll", 6000);
  // Hidden: nothing is asked for 6 seconds.
  const before = seen.polls;
  await pg.evaluate(() => { Object.defineProperty(document, "hidden", { configurable: true, get: () => true }); document.dispatchEvent(new Event("visibilitychange")); });
  await sleep(6000);
  eq(seen.polls, before, "a hidden tab kept asking");
  // Shown again: it asks at once, and when the job is ready the preview appears.
  phase = "preview";
  await pg.evaluate(() => { Object.defineProperty(document, "hidden", { configurable: true, get: () => false }); document.dispatchEvent(new Event("visibilitychange")); });
  await pg.locator(".imp-tree").waitFor({ timeout: 8000 });
  ok(seen.polls > before, "it did not resume");
  ok(seen.previews.length >= 1 && seen.previews[0].job_id === "imp_stub1", "the preview was asked for by job id");
  ok(/Fetched\./.test(await pg.locator("#rg-job").innerText()) && /commit 9f3c2a1b/.test(await pg.locator("#rg-job").innerText()), "the job panel says what was fetched");
  ok(/address, the commit/.test(await pg.locator(".rg-record").innerText()), "what is kept is explained");

  // The tree, an edit (a rename: the page asks again with the job id and the edit), and the apply.
  const titles = () => pg.locator(".imp-tree .imp-title").allInnerTexts();
  ok((await titles()).includes("The Start"), "the tree: " + (await titles()));
  const node = pg.locator('.imp-node[data-key]', { has: pg.locator('.imp-title', { hasText: /^The Start$/ }) }).first();
  await node.locator("summary").click();
  await node.locator('input[data-act="title"]').first().fill("Opening");
  await pg.keyboard.press("Tab");
  await waitFor(() => seen.previews.some((b) => (b.edits || []).some((e) => e.title === "Opening")), "the edit to be sent with the job id", 5000);
  ok(seen.previews.every((b) => b.job_id === "imp_stub1"), "every preview names the job");
  await pg.locator("#imp-t").fill("From the repository");
  await pg.locator("[data-go]").click();
  await pg.waitForURL(/\/p\/from-the-repo/, { timeout: 8000 }).catch(() => {});
  ok(seen.applies.length === 1, "apply was sent once");
  const ap = seen.applies[0];
  eq(ap.job_id, "imp_stub1", "apply names the job");
  eq(ap.token, preview.token, "apply carries the preview token");
  eq(ap.title, "From the repository", "apply carries the title");
  ok((ap.edits || []).some((e) => e.title === "Opening"), "apply carries the edit");
  ok(/\/p\/from-the-repo\/d\/doc_stub/.test(pg.url()), "the page goes to the new project: " + pg.url());
  await pg.unroute("**/api/v1/imports/git/preview");
  await pg.unroute("**/api/v1/imports/git?job_id=*");
  await pg.unroute("**/api/v1/imports/git");
});

await kit.test("stub: each failure has its words; the list resumes a ready job and throws one away", async () => {
  const mk = (id, status, extra) => ({ id, status, source_url: `https://git.example.org/wren/${id}.git`, branch: "main", created_at: new Date().toISOString(), ...extra });
  const jobs = [
    mk("imp_a", "preview", { commit_sha: "abc12345ffff", summary: { documents: preview.documents, words: preview.words, layout: "markdown-folder", token: preview.token } }),
    mk("imp_b", "failed", { error: "The repository is larger than the 512 MB limit (SCRIOBH_GIT_MAX_REPO_MB). Nothing was imported." }),
    mk("imp_c", "failed", { error: "git.example.org refused the credentials of \"this import\". Check that the deploy key is added to the repository with write access, or that the token is valid and may push." }),
    mk("imp_d", "failed", { error: "git.example.org says the repository does not exist, or that \"this import\" has no access to it. Check the address and that the key can write to it." }),
    mk("imp_e", "failed", { error: "There is no Markdown text in the repository to import (looked for .md files and a Scríobh mirror)." }),
    mk("imp_f", "failed", { error: "The repository is empty: there is nothing to import." }),
    mk("imp_g", "failed", { error: "git.example.org did not answer in time." })];
  const hints = { imp_b: /Folder field/, imp_c: /token that can read it/, imp_d: /Check the spelling/, imp_e: /Folder field/, imp_f: /Push something/, imp_g: /Try again/ };
  const gone = [];
  let next = null;
  await pg.route("**/api/v1/imports/git?job_id=*", async (route) => {
    const id = new URL(route.request().url()).searchParams.get("job_id");
    if (route.request().method() === "DELETE") { gone.push(id); return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ discarded: true }) }); }
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ item: jobs.find((j) => j.id === id) }) });
  });
  await pg.route("**/api/v1/imports/git/preview", async (route) => {
    const b = route.request().postDataJSON();
    const j = b.job_id ? jobs.find((x) => x.id === b.job_id) : next;
    return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ job: j, ...(b.job_id ? { preview } : {}) }) });
  });
  await open();
  for (const j of jobs.filter((x) => x.status === "failed")) {
    next = j;
    await pg.locator("#rg-url").fill(j.source_url);
    await pg.getByRole("button", { name: "Fetch the repository" }).click();
    await waitFor(async () => (await pg.locator("#rg-job[data-bad]").count()) === 1 && (await pg.locator("#rg-job").innerText()).includes(j.error.slice(0, 30)), "the failure of " + j.id);
    const t = await pg.locator("#rg-job").innerText();
    ok(hints[j.id].test(t), j.id + " has its explanation: " + t);
  }
  ok((await pg.locator("#rg-jobs li").count()) === 6, "six failed fetches are listed");
  // A ready job: started, then resumed from the list after the preview was cleared by another start.
  next = jobs[0];
  await pg.locator("#rg-url").fill(next.source_url);
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await pg.locator(".imp-tree").waitFor();
  next = jobs[1];
  await pg.locator("#rg-url").fill(next.source_url);
  await pg.getByRole("button", { name: "Fetch the repository" }).click();
  await waitFor(async () => (await pg.locator(".imp-tree").count()) === 0 || (await pg.locator("#imp-result").isHidden()), "the old preview to go");
  await pg.locator('#rg-jobs li[data-id="imp_a"]').getByRole("button", { name: "Continue" }).click();
  await pg.locator(".imp-tree").waitFor();
  ok(/Fetched\./.test(await pg.locator("#rg-job").innerText()), "the ready job resumes from the list");
  // Throw one away: a DELETE for that job, the row leaves, focus lands on the heading.
  await pg.locator('#rg-jobs li[data-id="imp_b"]').getByRole("button", { name: /Throw away/ }).click();
  await waitFor(() => gone.includes("imp_b"), "the discard to reach the server");
  await waitFor(async () => (await pg.locator('#rg-jobs li[data-id="imp_b"]').count()) === 0, "the row to leave the list");
  eq(await pg.evaluate(() => document.activeElement.id), "imp-title", "focus returns to the page heading");
  await pg.unroute("**/api/v1/imports/git?job_id=*");
  await pg.unroute("**/api/v1/imports/git/preview");
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
