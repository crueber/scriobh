#!/usr/bin/env node
// End-to-end test of the writing view in a real Chromium, against the real binary.
//
//   node scripts/e2e-write.mjs [--only substring] [--keep] [--no-phone]
//
// It builds the development binary, starts it on 127.0.0.1:0 with the stub
// identity provider, and drives the writing view: typing and saving, a hard
// reload, a server restart, an expired session, a cut network, two tabs, the
// three ways out of a conflict, a body over the limit, rapid typing, the
// manuscript tree (keyboard and pointer), the page with JavaScript off, and a
// phone-sized run with the CPU slowed 4x. Every step is also watched for console
// errors, CSP violations and requests to any other origin.
//
// Needs playwright (a devDependency of scripts/) and a Chromium: see harness.mjs.
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { build, findChromium, rm, signIn, startServer, tempDir } from "./harness.mjs";
import { armOk } from "./e2e-kit.mjs";

const args = process.argv.slice(2);
const only = args.includes("--only") ? args[args.indexOf("--only") + 1] : "";
const noPhone = args.includes("--no-phone");
const { chromium } = await import("playwright");

const tmp = tempDir();
const dataDir = join(tmp, "data");
const bin = build(tmp);
let srv = await startServer(bin, dataDir);
let base = srv.base;
const exe = findChromium();
const browser = await chromium.launch(exe ? { executablePath: exe } : {});
console.log(`e2e-write: ${browser.version()} against ${base}`);

// ------------------------------------------------------------------ plumbing

const findings = [];      // problems seen by the watchers, outside any expected failure
let step = "start";
let allowed = [];         // regexes for console messages the current step expects
const results = [];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const MOD = "ControlOrMeta";

function watch(page, label) {
  const note = (m) => findings.push(`[${step}] (${label}) ${m}`);
  page.on("console", (m) => {
    if (m.type() !== "error" && m.type() !== "warning") return;
    const t = m.text();
    if (/favicon\.ico/.test(m.location().url || "")) return; // the development issuer's own page
    if (/Service Worker registration blocked/.test(t)) return; // this script blocks workers; e2e-pwa.mjs runs with one
    if (allowed.some((re) => re.test(t))) return;
    note(`console ${m.type()}: ${t.slice(0, 200)} ${m.location().url || ""}`);
  });
  page.on("pageerror", (e) => note(`uncaught: ${e.message}`));
  const served = new Set(); // origins this run served (a restart moves to a new port)
  page.on("request", (r) => {
    served.add(new URL(base).origin);
    if (/^(https?|wss?):/.test(r.url()) && !served.has(new URL(r.url()).origin)) note(`off-origin request ${r.url()}`);
  });
  page.on("dialog", (d) => d.accept().catch(() => {}));
}
async function csp(page, label) {
  const v = await page.evaluate(() => window.__csp || []).catch(() => []);
  for (const x of v) findings.push(`[${step}] (${label}) securitypolicyviolation: ${x}`);
  const dom = await page.evaluate(() => ({ style: document.querySelectorAll("[style]").length, inline: document.querySelectorAll("script:not([src])").length })).catch(() => null);
  if (dom && (dom.style || dom.inline)) findings.push(`[${step}] (${label}) live DOM has ${dom.style} style attribute(s), ${dom.inline} inline script(s)`);
}
const initScript = () => {
  window.__csp = [];
  document.addEventListener("securitypolicyviolation", (e) => window.__csp.push(`${e.violatedDirective} ${e.blockedURI || "inline"}`));
};

async function newContext(opts = {}) {
  const ctx = await browser.newContext({ serviceWorkers: "block", ...opts });
  await ctx.addInitScript(initScript);
  return ctx;
}
async function newSignedIn(opts = {}, who) {
  const ctx = await newContext({ viewport: { width: 1280, height: 800 }, ...opts });
  const page = await ctx.newPage();
  watch(page, "main");
  await signIn(page, base, who);
  return { ctx, page };
}

async function test(name, fn) {
  if (only && !name.includes(only)) return;
  step = name;
  allowed = [];
  const t0 = Date.now();
  const before = findings.length;
  try {
    await fn();
    await main.setOffline(false);
    await csp(pg, "main"); // the live DOM, after every step: JS-built bars, sheets and dialogs included
    const bad = findings.length - before;
    results.push({ name, ok: bad === 0, ms: Date.now() - t0, err: bad ? `${bad} watcher finding(s), see below` : "" });
  } catch (e) {
    await main.setOffline(false).catch(() => {});
    results.push({ name, ok: false, ms: Date.now() - t0, err: e.message.split("\n").slice(0, 6).join(" | ") });
  }
  const r = results[results.length - 1];
  console.log(`${r.ok ? "PASS" : "FAIL"}  ${name}  (${(r.ms / 1000).toFixed(1)}s)${r.err ? "\n      " + r.err : ""}`);
}
function ok(cond, msg) { if (!cond) throw new Error(msg); }
function eq(a, b, msg) { if (a !== b) throw new Error(`${msg}: got ${JSON.stringify(String(a).slice(0, 120))}, want ${JSON.stringify(String(b).slice(0, 120))}`); }

// Server-side helpers (the API, as a client would use it).
async function api(ctx, method, path, data) {
  const r = await ctx.request.fetch(base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  let json = null;
  try { json = await r.json(); } catch { /* no body */ }
  return { status: r.status(), json };
}
async function newProject(ctx, title = "The Salt Road", kind = "novel") {
  const p = (await api(ctx, "POST", "/api/v1/projects", { title, kind })).json.item;
  const list = (await api(ctx, "GET", `/api/v1/projects/${p.id}/docs`)).json;
  return { ...p, doc: list.order[0] };
}
async function serverDoc(ctx, id) { return (await api(ctx, "GET", `/api/v1/docs/${id}`)).json.item; }
async function createDoc(ctx, pid, body) { return (await api(ctx, "POST", `/api/v1/projects/${pid}/docs`, body)).json.item; }
async function waitFor(fn, what, ms = 8000) {
  const t0 = Date.now();
  let last;
  while (Date.now() - t0 < ms) { try { last = await fn(); if (last) return last; } catch { /* retry */ } await sleep(60); }
  throw new Error(`timed out waiting for ${what}`);
}
// The editor serializes with a closing newline; literal expectations may omit it.
const trimNl = (s) => s.replace(/\n$/, "");
const body = async (ctx, id) => trimNl((await serverDoc(ctx, id)).content);
async function waitBody(ctx, id, md, ms = 8000) {
  let got = "";
  try { await waitFor(async () => (got = (await serverDoc(ctx, id)).content) === md || got === md + "\n", "the server body", ms); }
  catch { throw new Error(`the server body is ${JSON.stringify(got.slice(-60))}, want ${JSON.stringify(md.slice(-60))}`); }
}
const state = (page) => page.locator("#save-state").getAttribute("data-state");
// Saved, and nothing typed since: the header alone can lag the debounce by 700 ms.
async function waitSaved(page, ms = 8000) {
  await waitFor(async () => (await state(page)) === "saved" && !(await page.evaluate(() => window.scriobh.write.unsaved())), "Saved with nothing unsaved", ms);
}
async function waitState(page, want, ms = 8000) { await waitFor(async () => (await state(page)) === want, `state ${want} (is ${await state(page).catch(() => "?")})`, ms); }
const rawMd = (page) => page.evaluate(() => window.scriobh.write.editor.getMarkdown());
const editorMd = async (page) => trimNl(await rawMd(page));
async function open(page, p, docId) {
  await page.goto(`${base}/p/${p.slug}${docId ? "/d/" + docId : ""}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose", { timeout: 10000 });
}
async function typeText(page, text, delay = 4) {
  await page.locator(".scriobh-prose").click();
  await page.keyboard.press(`${MOD}+End`);
  await page.keyboard.type(text, { delay });
}
const unsavedKeys = (page, id) => page.evaluate((id) => localStorage.getItem(`scriobh:draft:${document.querySelector('meta[name="scriobh-user"]').content}:${id}`), id);

// ====================================================================== tests

const { ctx: main, page: pg } = await newSignedIn();

await test("definition of done: type, Saved, hard reload, server restart", async () => {
  allowed = [/ERR_CONNECTION_REFUSED/, /ERR_INCOMPLETE_CHUNKED_ENCODING/, /Failed to load resource/]; // the server is stopped on purpose; the live stream and the tree meet it
  const p = await newProject(main);
  await open(pg, p);
  eq(await pg.locator("#doc-title").inputValue(), "Chapter 1", "title field");
  const text = "The tide had gone out and taken the light with it.\n\nMáire stood at the end of the pier.";
  await typeText(pg, "The tide had gone out and taken the light with it.");
  await pg.keyboard.press("Enter");
  await pg.keyboard.type("Máire stood at the end of the pier.");
  await waitSaved(pg);
  await waitBody(main, p.doc, text);
  await waitFor(async () => (await pg.locator("#save-word").innerText()) === "Saved", "the word Saved");
  ok(!(await unsavedKeys(pg, p.doc)), "the local draft was not cleared after a confirmed save");
  await pg.reload({ waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  eq(await editorMd(pg), text, "text after a hard reload");
  // Type right after the reload: the lease is still ours (same tab id), no Locked.
  await typeText(pg, " Rain.");
  await waitSaved(pg);
  await waitBody(main, p.doc, text + " Rain.");
  await csp(pg, "main");
  // Restart the server on the same data directory.
  await srv.stop();
  srv = await startServer(bin, dataDir);
  base = srv.base;
  await open(pg, p);
  eq(await editorMd(pg), text + " Rain.", "text after a server restart");
  eq(await state(pg), "saved", "state after restart");
  // And after a hard kill in the middle of nothing (no clean shutdown).
  await typeText(pg, " Wind.");
  await waitSaved(pg);
  await srv.stop("SIGKILL");
  srv = await startServer(bin, dataDir);
  base = srv.base;
  await open(pg, p);
  eq(await editorMd(pg), text + " Rain. Wind.", "text after SIGKILL and restart");
});

await test("session expiry mid-typing keeps the text and resumes by itself", async () => {
  allowed = [/status of 401/, /ERR_/];
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "First line saved.");
  await waitSaved(pg);
  const cookies = await main.cookies();
  await main.clearCookies();
  await typeText(pg, " Typed after the session ended.");
  await waitState(pg, "signedout");
  const bar = await pg.locator("#notices").innerText();
  ok(bar.includes("Signed out") && bar.includes("your text is kept here") && bar.includes("Sign in in another tab"), "the signed-out message is wrong: " + bar);
  ok(pg.url().includes("/p/"), "the page navigated away on 401: " + pg.url());
  eq(await editorMd(pg), "First line saved. Typed after the session ended.", "editor text while signed out");
  ok(await unsavedKeys(pg, p.doc), "no local draft while signed out");
  await typeText(pg, " More text while signed out.");
  await sleep(1500);
  eq(await state(pg), "signedout", "still signed out");
  // Sign in again elsewhere (cookie back), return to this tab.
  await main.addCookies(cookies);
  await pg.evaluate(() => window.dispatchEvent(new Event("focus")));
  await waitSaved(pg, 10000);
  const want = "First line saved. Typed after the session ended. More text while signed out.";
  await waitBody(main, p.doc, want);
  ok(!(await unsavedKeys(pg, p.doc)), "the draft was not cleared after the save resumed");
});

await test("a signed-out tab retries on its own timer", async () => {
  allowed = [/status of 401/, /ERR_/];
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Alpha.");
  await waitSaved(pg);
  const cookies = await main.cookies();
  await main.clearCookies();
  await typeText(pg, " Beta.");
  await waitState(pg, "signedout");
  await main.addCookies(cookies);
  const t0 = Date.now();
  await waitSaved(pg, 30000);
  console.log(`      (the 20 s retry resumed saving after ${((Date.now() - t0) / 1000).toFixed(0)} s)`);
  await waitBody(main, p.doc, "Alpha. Beta.");
});

await test("network cut: Offline, keep editing, back online saves everything", async () => {
  allowed = [/ERR_INTERNET_DISCONNECTED/, /Failed to load resource/];
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Online text.");
  await waitSaved(pg);
  await main.setOffline(true);
  await typeText(pg, " Offline one.");
  await waitState(pg, "offline");
  eq(await pg.locator("#live").evaluate((e) => e.getAttribute("role") + "/" + e.getAttribute("aria-live")), "status/polite", "the live region");
  await waitFor(async () => (await pg.locator("#live").innerText()).includes("Offline"), "the live region to announce Offline", 6000);
  await typeText(pg, " Offline two.");
  await sleep(800);
  eq(await state(pg), "offline", "still offline");
  ok((await pg.locator("#notices").innerText()).includes("Your text is kept here"), "no offline message");
  await main.setOffline(false);
  await waitSaved(pg, 12000);
  await waitBody(main, p.doc, "Online text. Offline one. Offline two.");
  // Reload with the network cut and come back: the draft protects text typed offline.
  await main.setOffline(true);
  await typeText(pg, " Cut before reload.");
  await waitState(pg, "offline");
  await pg.evaluate(() => window.scriobh.write.editor.flush());
  const draft = JSON.parse(await unsavedKeys(pg, p.doc));
  ok(trimNl(draft.md).endsWith("Cut before reload."), "the draft is missing the last text: " + JSON.stringify(draft.md.slice(-40)));
  await main.setOffline(false);
  await waitSaved(pg, 12000);
});

await test("a crashed tab's draft is offered, never applied silently", async () => {
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Saved text.");
  await waitSaved(pg);
  // Plant a newer draft, as a tab that died mid-save would leave behind.
  await pg.evaluate(([id, rev]) => localStorage.setItem(`scriobh:draft:${document.querySelector('meta[name="scriobh-user"]').content}:${id}`, JSON.stringify({ md: "Saved text. Unsaved tail.", title: "Chapter 1", rev, t: Date.now() })), [p.doc, (await serverDoc(main, p.doc)).revision]);
  await pg.reload({ waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  eq(await editorMd(pg), "Saved text.", "the draft must not be applied by itself");
  const restore = pg.getByRole("button", { name: "Restore your unsaved text" });
  await restore.waitFor();
  await restore.click();
  eq(await editorMd(pg), "Saved text. Unsaved tail.", "text after restore");
  await waitSaved(pg);
  await waitBody(main, p.doc, "Saved text. Unsaved tail.");
  // Discard.
  await pg.evaluate(([id]) => localStorage.setItem(`scriobh:draft:${document.querySelector('meta[name="scriobh-user"]').content}:${id}`, JSON.stringify({ md: "junk", title: "Chapter 1", rev: 1, t: Date.now() })), [p.doc]);
  await pg.reload({ waitUntil: "load" });
  await pg.getByRole("button", { name: "Discard" }).click();
  ok(!(await unsavedKeys(pg, p.doc)), "discard left the draft behind");
  eq(await editorMd(pg), "Saved text. Unsaved tail.", "text after discard");
});

await test("two tabs: the second is read-only, take over works, the old tab is refused", async () => {
  allowed = [/status of 409/];
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Tab A text.");
  await waitSaved(pg);
  const b = await main.newPage();
  watch(b, "tab B");
  // A second tab id: a fresh page in the same context has its own sessionStorage.
  await open(b, p);
  await waitState(b, "locked");
  ok((await b.locator("#notices").innerText()).includes("Open in another tab"), "no banner in tab B");
  eq(await b.locator(".scriobh-prose").getAttribute("contenteditable"), "false", "tab B must be read-only");
  const takeOver = b.getByRole("button", { name: "Take over" });
  await takeOver.click();
  await waitSaved(b);
  eq(await b.locator(".scriobh-prose").getAttribute("contenteditable"), "true", "tab B after take over");
  await typeText(b, " From B.");
  await waitSaved(b);
  await waitBody(main, p.doc, "Tab A text. From B.");
  // Tab A is now the stale one.
  await typeText(pg, " Late from A.");
  await waitState(pg, "conflict");
  const bar = await pg.locator("#notices").innerText();
  ok(/Keep mine/.test(bar) && /Keep theirs/.test(bar) && /Compare/.test(bar), "the old tab has no conflict path: " + bar);
  eq(await editorMd(pg), "Tab A text. Late from A.", "tab A kept its text");
  eq((await body(main, p.doc)), "Tab A text. From B.", "tab A must not have written");
  // Keep mine from the lease conflict takes the lease back and saves.
  await pg.getByRole("button", { name: "Keep mine" }).click();
  await waitSaved(pg);
  await waitBody(main, p.doc, "Tab A text. Late from A.");
  await b.close();
});

for (const choice of ["Keep mine", "Keep theirs", "Compare"]) {
  await test(`conflict: ${choice}`, async () => {
    allowed = [/status of 409/];
    const p = await newProject(main);
    await open(pg, p);
    await typeText(pg, "The harbour was quiet that morning.");
    await waitSaved(pg);
    // Someone else (the API, no tab) changes the document.
    const cur = await serverDoc(main, p.doc);
    const r = await api(main, "PATCH", `/api/v1/docs/${p.doc}`, { base_revision: cur.revision, content: "The harbour was loud that evening.\n\nA second paragraph from elsewhere." });
    eq(r.status, 200, "the other writer's save");
    await typeText(pg, " Gulls argued.");
    await waitState(pg, "conflict");
    const mine = "The harbour was quiet that morning. Gulls argued.";
    const theirs = "The harbour was loud that evening.\n\nA second paragraph from elsewhere.";
    eq(await editorMd(pg), mine, "the text was kept while the conflict bar is up");
    eq((await body(main, p.doc)), theirs, "no write before a choice");
    await waitFor(() => existsConflictCopy(p.id, mine), "a conflict copy of the local text");
    if (choice === "Keep mine") {
      await pg.getByRole("button", { name: "Keep mine" }).click();
      await waitSaved(pg);
      await waitBody(main, p.doc, mine);
    } else if (choice === "Keep theirs") {
      await pg.getByRole("button", { name: "Keep theirs" }).click();
      await waitSaved(pg);
      await waitFor(async () => (await editorMd(pg)) === theirs, "the editor to show their text");
      eq((await body(main, p.doc)), theirs, "their text must stay");
    } else {
      await pg.getByRole("button", { name: "Compare" }).click();
      const panel = pg.locator("#nb-compare");
      await panel.waitFor();
      const ins = await panel.locator("ins").allInnerTexts();
      const del = await panel.locator("del").allInnerTexts();
      ok(ins.join("").includes("quiet") && ins.join("").includes("morning"), `compare: insertions ${JSON.stringify(ins)}`);
      ok(del.join("").includes("loud") && del.join("").includes("evening"), `compare: deletions ${JSON.stringify(del)}`);
      eq(await editorMd(pg), mine, "compare must not change the editor");
      eq(await state(pg), "conflict", "still in conflict after Compare");
      await pg.getByRole("button", { name: "Keep theirs" }).click();
      await waitSaved(pg);
    }
    ok(existsConflictCopy(p.id, mine), "the conflict copy is missing after " + choice);
  });
}

await test("a save whose answer was lost is adopted, not reported as a conflict", async () => {
  allowed = [/status of 409/, /ERR_/, /Failed to load resource/];
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Landed.");
  await waitSaved(pg);
  // Let the PATCH reach the server, but cut the response.
  let cut = true;
  await pg.route("**/api/v1/docs/*", async (route) => {
    if (cut && route.request().method() === "PATCH") {
      await route.fetch();
      cut = false;
      return route.abort("connectionreset");
    }
    return route.continue();
  });
  await typeText(pg, " More.");
  await waitState(pg, "offline");
  await typeText(pg, " Extra typed before the retry."); // the first save landed; this is newer than what the retry will carry
  await waitSaved(pg, 15000);
  await pg.unroute("**/api/v1/docs/*");
  await waitBody(main, p.doc, "Landed. More. Extra typed before the retry.");
  ok(!(await pg.locator("#notices").innerText()).includes("Keep mine"), "a landed save became a conflict");
});

await test("413: a body over 2 MiB is kept locally and explained", async () => {
  allowed = [/status of 413/, /Failed to load resource/];
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Small first.");
  await waitSaved(pg);
  const big = "word ".repeat(450000);
  await pg.keyboard.press(`${MOD}+e`);
  const src = pg.locator(".scriobh-source");
  await src.waitFor();
  await src.evaluate((t, v) => { t.value = v; t.dispatchEvent(new Event("input", { bubbles: true })); }, big);
  await pg.evaluate(() => window.scriobh.write.editor.flush());
  await waitState(pg, "toolarge", 15000);
  const bar = await pg.locator("#notices").innerText();
  ok(bar.includes("2 MiB") && bar.includes("kept"), "413 message: " + bar);
  eq((await body(main, p.doc)), "Small first.", "the server body is untouched");
  ok((await editorMd(pg)).length >= big.length - 1, "the editor still holds the whole text");
  await src.evaluate((t, v) => { t.value = v; t.dispatchEvent(new Event("input", { bubbles: true })); }, "Small again.");
  await pg.evaluate(() => window.scriobh.write.editor.flush());
  await waitSaved(pg, 10000);
  await waitBody(main, p.doc, "Small again.");
});

await test("429 and 5xx keep the text and retry", async () => {
  allowed = [/status of (429|500|503)/, /Failed to load resource/];
  const p = await newProject(main);
  await open(pg, p);
  let n = 0;
  await pg.route("**/api/v1/docs/*", (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    n++;
    if (n === 1) return route.fulfill({ status: 429, headers: { "Retry-After": "2", "Content-Type": "application/json" }, body: JSON.stringify({ error: { code: "rate_limited", message: "Slow down" } }) });
    if (n === 2) return route.fulfill({ status: 503, headers: { "Content-Type": "application/json" }, body: JSON.stringify({ error: { code: "internal", message: "later" } }) });
    return route.continue();
  });
  await typeText(pg, "Resilient text.");
  await waitState(pg, "paused");
  eq(await editorMd(pg), "Resilient text.", "text during 429");
  await waitState(pg, "error", 8000);
  await waitSaved(pg, 15000);
  await waitBody(main, p.doc, "Resilient text.");
  await pg.unroute("**/api/v1/docs/*");
});

await test("422 on the title keeps the body saving", async () => {
  allowed = [/status of 422/, /Failed to load resource/];
  const p = await newProject(main);
  await open(pg, p);
  const title = pg.locator("#doc-title");
  await title.click();
  await title.evaluate((t) => { t.value = "x".repeat(301); t.dispatchEvent(new Event("input", { bubbles: true })); });
  await typeText(pg, "Body survives a bad title.");
  await waitBody(main, p.doc, "Body survives a bad title.");
  await waitFor(async () => (await pg.locator("#title-error").isVisible()), "the title message");
  ok((await pg.locator("#title-error").innerText()).length > 5, "no title message");
  await title.fill("A fine title");
  await waitSaved(pg);
  await waitFor(async () => (await serverDoc(main, p.doc)).title === "A fine title", "the corrected title");
});

await test("title changes save, and update the tree row and breadcrumb in place", async () => {
  const p = await newProject(main);
  await open(pg, p);
  const title = pg.locator("#doc-title");
  await title.click();
  await title.fill("The Harbour at Low Tide");
  await pg.keyboard.press("Enter");
  eq(await pg.evaluate(() => document.activeElement.classList.contains("scriobh-prose")), true, "Enter in the title moves into the body");
  await waitSaved(pg);
  eq((await pg.locator(`#tr-${p.doc} .tree-title`).innerText()), "The Harbour at Low Tide", "tree row");
  eq((await pg.locator("#crumb-title").innerText()), "The Harbour at Low Tide", "breadcrumb");
  eq((await serverDoc(main, p.doc)).title, "The Harbour at Low Tide", "server title");
  ok((await pg.title()).startsWith("The Harbour at Low Tide"), "document title: " + (await pg.title()));
});

await test("rapid typing: one save in flight, final body byte for byte", async () => {
  const p = await newProject(main);
  await open(pg, p);
  let inflight = 0, max = 0, count = 0;
  await pg.route("**/api/v1/docs/*", async (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    inflight++; count++; max = Math.max(max, inflight);
    await sleep(180); // a slow server, so saves would overlap if the loop let them
    try { await route.continue(); } finally { inflight--; }
  });
  const words = Array.from({ length: 160 }, (_, i) => `word${i}`).join(" ");
  await pg.locator(".scriobh-prose").click();
  for (let i = 0; i < 6; i++) { await pg.keyboard.type(words.slice(i * 160, i * 160 + 160), { delay: 0 }); await pg.keyboard.press(`${MOD}+s`); }
  await pg.keyboard.type(words.slice(960), { delay: 0 });
  await waitSaved(pg, 15000);
  const md = await rawMd(pg);
  await waitBody(main, p.doc, md);
  eq(max, 1, "maximum concurrent saves");
  ok(count >= 2, `only ${count} save(s); the test did not exercise overlap`);
  ok((await serverDoc(main, p.doc)).content === md, "the final server body is not byte-for-byte the editor body");
  await pg.unroute("**/api/v1/docs/*");
});

await test("long-document guard: warn bar and read-only", async () => {
  const p = await newProject(main);
  const long = Array.from({ length: 15200 }, (_, i) => "alpha").join(" ");
  const warn = await createDoc(main, p.id, { title: "Long chapter", content: long });
  await open(pg, p, warn.id);
  const bar = await pg.locator("#notices").innerText();
  ok(/words/.test(bar) && /splitting it at a scene break/.test(bar), "warn bar: " + bar);
  eq(await pg.locator(".scriobh-prose").getAttribute("contenteditable"), "true", "warn level stays editable");
  const huge = await createDoc(main, p.id, { title: "Huge chapter", content: Array.from({ length: 25100 }, () => "beta").join(" ") });
  await open(pg, p, huge.id);
  eq(await pg.locator(".scriobh-prose").getAttribute("contenteditable"), "false", "read-only level");
  ok(/read-only/.test(await pg.locator("#notices").innerText()), "no read-only reason");
});

await test("page without JavaScript is readable", async () => {
  const p = await newProject(main);
  const d2 = await createDoc(main, p.id, { title: "Second chapter", content: "Second body & <b>not bold</b>." });
  await api(main, "PATCH", `/api/v1/docs/${p.doc}`, { base_revision: 1, content: "# Heading\n\nFirst body text.\n\n- a\n- b" });
  const ctx = await newContext({ javaScriptEnabled: false, viewport: { width: 1280, height: 800 } });
  await ctx.addCookies(await main.cookies());
  const page = await ctx.newPage();
  await page.goto(`${base}/p/${p.slug}/d/${p.doc}`);
  const text = await page.locator("body").innerText();
  ok(text.includes("Chapter 1") && text.includes("First body text.") && text.includes("# Heading") && text.includes("- b"), "body text is missing without JS: " + text.slice(0, 300));
  ok(await page.locator("#doc-plain").isVisible(), "the plain body is not shown");
  ok(await page.getByRole("link", { name: "Second chapter" }).first().isVisible(), "the tree links to the other chapter");
  await page.goto(`${base}/p/${p.slug}/d/${d2.id}`);
  ok((await page.locator("body").innerText()).includes("Second body & <b>not bold</b>."), "escaped body text");
  eq(await page.locator("#doc-plain b").count(), 0, "markdown became markup");
  await ctx.close();
});

// ------------------------------------------------------------------- the tree

const rowTitles = (page) => page.locator("#tree .tree-row:not([hidden]) .tree-title").allInnerTexts();
async function rowFor(page, title) { return page.locator("#tree .tree-row", { has: page.locator(".tree-title", { hasText: new RegExp(`^${title}$`) }) }).first(); }
// The tree is re-rendered after a change; wait until the row is back, at its level, with focus.
async function settleTree(page, title, level, order) {
  await waitFor(() => page.evaluate(([t, l, o]) => {
    const a = document.activeElement, rows = [...document.querySelectorAll("#tree .tree-row:not([hidden]) .tree-title")].map((x) => x.textContent).join("|");
    return a && a.classList.contains("tree-row") && a.querySelector(".tree-title").textContent === t && a.getAttribute("aria-level") === l && (!o || rows === o);
  }, [title, level, order]), `focus on ${title} at level ${level}${order ? " with rows " + order : ""}`);
}
async function treeSetup(titles) {
  const p = await newProject(main, "Tree Book");
  await api(main, "PATCH", `/api/v1/docs/${p.doc}`, { base_revision: 1, title: "One" });
  const ids = { One: p.doc };
  for (const t of titles) ids[t] = (await createDoc(main, p.id, { title: t })).id;
  return { p, ids };
}
const serverOrder = async (p) => {
  const l = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json;
  const by = Object.fromEntries(l.items.map((i) => [i.id, i]));
  return l.order.map((id) => `${"-".repeat(depth(by, id))}${by[id].title}`);
};
function depth(by, id) { let d = 0; for (let p = by[id].parent_id; p; p = by[p]?.parent_id) d++; return d; }

await test("tree: keyboard reorder, indent, outdent, rename, trash and undo", async () => {
  const { p, ids } = await treeSetup(["Two", "Three"]);
  await open(pg, p, ids.One);
  eq((await rowTitles(pg)).join("|"), "One|Two|Three", "initial rows");
  const rows = pg.locator("#tree");
  ok((await rows.getAttribute("role")) === "tree", "role=tree");
  // Roving focus with arrows.
  await (await rowFor(pg, "One")).focus();
  await pg.keyboard.press("ArrowDown");
  eq(await pg.evaluate(() => document.activeElement.querySelector(".tree-title").textContent), "Two", "ArrowDown");
  await pg.keyboard.press("End");
  eq(await pg.evaluate(() => document.activeElement.querySelector(".tree-title").textContent), "Three", "End");
  await pg.keyboard.press("Home");
  // Alt+Down moves One below Two.
  await pg.keyboard.press("Alt+ArrowDown");
  await waitFor(async () => (await serverOrder(p)).join("|") === "Two|One|Three", "reorder down");
  await settleTree(pg, "One", "1", "Two|One|Three");
  eq(await pg.evaluate(() => document.activeElement.querySelector(".tree-title")?.textContent), "One", "focus stays on the moved row");
  // The move also reaches this tab as a live event, and the tree is drawn again a moment later: the row keeps its focus through that.
  await sleep(2500);
  eq(await pg.evaluate(() => document.activeElement && document.activeElement.querySelector(".tree-title")?.textContent), "One", "focus stays on the moved row after the tree is drawn again");
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => (await serverOrder(p)).join("|") === "One|Two|Three", "reorder up");
  await settleTree(pg, "One", "1", "One|Two|Three");
  // Indent One's next sibling Two into One; outdent back.
  await (await rowFor(pg, "Two")).focus();
  await pg.keyboard.press("Alt+ArrowRight");
  await waitFor(async () => (await serverOrder(p)).join("|") === "One|-Two|Three", "indent");
  await settleTree(pg, "Two", "2");
  eq(await (await rowFor(pg, "Two")).getAttribute("aria-level"), "2", "aria-level after indent");
  eq(await (await rowFor(pg, "One")).getAttribute("aria-expanded"), "true", "folder is expanded");
  await pg.keyboard.press("Alt+ArrowLeft");
  await waitFor(async () => (await serverOrder(p)).join("|") === "One|Two|Three", "outdent");
  await settleTree(pg, "Two", "1");
  // Collapse state is remembered.
  await pg.keyboard.press("Alt+ArrowRight");
  await waitFor(async () => (await serverOrder(p)).join("|") === "One|-Two|Three", "indent again");
  await settleTree(pg, "Two", "2");
  await (await rowFor(pg, "One")).focus();
  await pg.keyboard.press("ArrowLeft");
  eq(await (await rowFor(pg, "One")).getAttribute("aria-expanded"), "false", "collapsed");
  eq((await rowTitles(pg)).join("|"), "One|Three", "collapsed rows hide children");
  await pg.reload({ waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  eq((await rowTitles(pg)).join("|"), "One|Three", "collapse state after reload");
  await (await rowFor(pg, "One")).focus();
  await pg.keyboard.press("ArrowRight");
  eq((await rowTitles(pg)).join("|"), "One|Two|Three", "expanded again");
  // Rename with F2 (another document, then the open one).
  await (await rowFor(pg, "Three")).focus();
  await pg.keyboard.press("F2");
  await pg.keyboard.type("Three and a half");
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await serverOrder(p)).includes("Three and a half"), "rename of another document");
  await waitFor(async () => (await rowTitles(pg)).includes("Three and a half"), "the tree to show the new name");
  await (await rowFor(pg, "One")).focus();
  await pg.keyboard.press("F2");
  await pg.keyboard.press(`${MOD}+a`);
  await pg.keyboard.type("Opening");
  await pg.keyboard.press("Enter");
  await waitSaved(pg);
  await waitFor(async () => (await serverDoc(main, ids.One)).title === "Opening", "rename of the open document through its save loop");
  eq(await pg.locator("#doc-title").inputValue(), "Opening", "title field follows the tree rename");
  // Delete trashes with an Undo toast.
  await (await rowFor(pg, "Three and a half")).focus();
  await pg.keyboard.press("Delete");
  await waitFor(async () => !(await serverOrder(p)).includes("Three and a half"), "trash");
  const undo = pg.locator("#toasts").getByRole("button", { name: "Undo" });
  await undo.click();
  await waitFor(async () => (await serverOrder(p)).includes("Three and a half"), "restore through Undo");
});

await test("tree: pointer drag moves a document, with an insertion indicator", async () => {
  const { p, ids } = await treeSetup(["Two", "Three", "Four"]);
  await open(pg, p, ids.One);
  const from = await rowFor(pg, "Four");
  const to = await rowFor(pg, "One");
  const fb = await from.boundingBox(), tb = await to.boundingBox();
  await pg.mouse.move(fb.x + 90, fb.y + fb.height / 2);
  await pg.mouse.down();
  await pg.mouse.move(fb.x + 90, fb.y + fb.height / 2 - 12, { steps: 4 });
  await pg.mouse.move(tb.x + 90, tb.y + 3, { steps: 8 });
  await waitFor(async () => (await pg.locator(".tree-row.drop-before").count()) === 1, "the insertion indicator");
  await pg.mouse.up();
  await waitFor(async () => (await serverOrder(p)).join("|") === "Four|One|Two|Three", "drag to the top");
  await waitFor(async () => (await rowTitles(pg)).join("|") === "Four|One|Two|Three", "the tree to redraw");
  // Drag Three into Two (middle of the row).
  const t3 = await (await rowFor(pg, "Three")).boundingBox();
  const t2 = await (await rowFor(pg, "Two")).boundingBox();
  await pg.mouse.move(t3.x + 90, t3.y + t3.height / 2);
  await pg.mouse.down();
  await pg.mouse.move(t3.x + 90, t3.y + 4, { steps: 3 });
  await pg.mouse.move(t2.x + 90, t2.y + t2.height / 2, { steps: 6 });
  await waitFor(async () => (await pg.locator(".tree-row.drop-into").count()) === 1, "the into indicator");
  await pg.mouse.up();
  await waitFor(async () => (await serverOrder(p)).join("|") === "Four|One|Two|-Three", "drag into");
  ok(page_urlStays(pg, p, ids.One), "a drag must not navigate");
});
function page_urlStays(page, p, id) { return page.url().endsWith(`/p/${p.slug}/d/${id}`); }

await test("tree: add chapter, scene and part; creating focuses the title", async () => {
  const { p, ids } = await treeSetup([]);
  await open(pg, p, ids.One);
  await (await rowFor(pg, "One")).focus();
  await pg.locator("#add-btn").click();
  await pg.getByRole("menuitem", { name: "Add scene" }).click();
  await pg.waitForURL(/\?new=1|\/d\//);
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(async () => (await pg.evaluate(() => document.activeElement?.id)) === "doc-title", "the title to take focus");
  eq(await pg.locator("#doc-title").inputValue(), "", "the new scene is untitled");
  await pg.keyboard.type("A scene");
  await pg.keyboard.press("Enter");
  await waitSaved(pg);
  await waitFor(async () => (await serverOrder(p)).join("|") === "One|-A scene", "a scene lands inside the chapter");
  await pg.locator("#add-btn").click();
  await pg.getByRole("menuitem", { name: "Add part" }).click();
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(async () => (await serverOrder(p)).length === 3 && (await serverOrder(p))[2] === "", "a top-level part");
  const rows = await api(main, "GET", `/api/v1/projects/${p.id}/docs`);
  eq(rows.json.items.filter((i) => i.kind === "part").length, 1, "part created");
  // Adding a part opens it. The page's own navigation comes after the server's answer, so the step waits for it:
  // otherwise the next step's first navigation races it (ERR_ABORTED, or a document that never arrives).
  const newPart = rows.json.items.find((i) => i.kind === "part");
  await pg.waitForURL((u) => u.pathname.includes(newPart.id), { timeout: 10000 });
  await pg.waitForLoadState("load");
});

await test("tree: word counts shown equal the server's", async () => {
  const { p, ids } = await treeSetup([]);
  const part = await createDoc(main, p.id, { title: "Part A", kind: "part" });
  const c1 = await createDoc(main, p.id, { title: "C1", parent_id: part.id, content: "one two three four five\n\nsix seven" });
  await createDoc(main, p.id, { title: "C2", parent_id: part.id, content: "alpha beta **gamma** [[Link|delta]] `skip`" });
  await open(pg, p, c1.id);
  await typeText(pg, " eight nine ten.");
  await waitSaved(pg);
  const list = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.items;
  const sum = (id) => list.filter((i) => i.id === id || i.parent_id === id).reduce((a, i) => a + i.word_count, 0);
  const shown = await pg.evaluate(() => Object.fromEntries([...document.querySelectorAll("#tree .tree-row")].map((r) => [r.dataset.id, +r.querySelector(".tree-count").textContent.replace(/[^0-9]/g, "") || 0])));
  for (const i of list) {
    const want = list.some((x) => x.parent_id === i.id) ? sum(i.id) : i.word_count;
    eq(shown[i.id], want, `tree count for ${i.title}`);
  }
  const total = list.reduce((a, i) => a + i.word_count, 0);
  eq(+(await pg.locator("#tree-total").innerText()).replace(/,/g, ""), total, "project total");
  // The status bar (the editor's count) agrees with the server's for the open document.
  const bar = await pg.locator("#st-words").innerText();
  eq(+bar.replace(/[^0-9]/g, ""), list.find((i) => i.id === c1.id).word_count, "status bar words vs server");
  await csp(pg, "main");
});

await test("prev/next, jump list and Alt+PageDown flush before navigating", async () => {
  const { p, ids } = await treeSetup(["Two", "Three"]);
  await open(pg, p, ids.One);
  await typeText(pg, "Typed then navigated at once.");
  await pg.keyboard.press("Alt+PageDown");
  await pg.waitForURL(new RegExp(`/d/${ids.Two}$`));
  eq((await body(main, ids.One)), "Typed then navigated at once.", "text saved before leaving");
  await pg.waitForSelector(".scriobh-prose");
  await pg.locator("#jump-open").click();
  await pg.locator("#jump-input").fill("thr");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(new RegExp(`/d/${ids.Three}$`));
  await pg.getByRole("link", { name: /Previous/ }).click();
  await pg.waitForURL(new RegExp(`/d/${ids.Two}$`));
});

await test("toolbar and shortcuts: bold, heading, scene break, Source, a11y names", async () => {
  const p = await newProject(main);
  await open(pg, p);
  const names = await pg.locator("#fmtbar .tb").evaluateAll((bs) => bs.map((b) => [b.getAttribute("aria-label"), b.title]));
  ok(names.length >= 14 && names.every(([a, t]) => a && t), "every button needs an accessible name and a tooltip: " + JSON.stringify(names));
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.type("plain ");
  await pg.getByRole("button", { name: "Bold" }).click();
  await waitFor(async () => (await pg.getByRole("button", { name: "Bold" }).getAttribute("aria-pressed")) === "true", "bold to show as pressed");
  await pg.keyboard.type("strong");
  await pg.keyboard.press(`${MOD}+b`);
  await pg.keyboard.type(" tail");
  await pg.keyboard.press("Enter");
  await pg.getByRole("button", { name: "Heading 2" }).click();
  await pg.keyboard.type("Title");
  await pg.keyboard.press("Enter");
  await pg.keyboard.press(`${MOD}+Enter`);
  await pg.keyboard.type("After the break.");
  await pg.keyboard.press(`${MOD}+s`);
  await waitSaved(pg);
  const md = (await serverDoc(main, p.doc)).content;
  ok(md.includes("plain **strong** tail") && md.includes("## Title") && /\n\* \* \*\n/.test(md) && md.includes("After the break."), "markdown: " + JSON.stringify(md));
  await pg.keyboard.press(`${MOD}+e`);
  await waitFor(async () => (await pg.getByRole("button", { name: "Markdown source" }).first().getAttribute("aria-pressed")) === "true", "Source to show as pressed");
  ok(await pg.locator(".scriobh-source").isVisible(), "source textarea visible");
  eq(await pg.locator(".scriobh-source").inputValue(), md, "source shows the exact Markdown");
  await pg.keyboard.press(`${MOD}+e`);
  // Toolbar is a roving-tabindex toolbar.
  const tab0 = await pg.locator("#fmtbar .tb[tabindex='0']").count();
  eq(tab0, 1, "exactly one toolbar tab stop");
  // The editing surface has a name and the document language.
  eq(await pg.locator(".scriobh-prose").getAttribute("aria-label"), "Document text", "editor name");
  eq(await pg.locator("#paper").getAttribute("lang"), "en", "language");
});

await test("a lease answer that arrives late never strands typed text", async () => {
  allowed = [/status of 409/];
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Holder text.");
  await waitSaved(pg);
  // A second tab id, with the lease answer held back for 1.5 s while the person types.
  const b = await main.newPage();
  watch(b, "late lease");
  await b.route("**/api/v1/docs/*/lease", async (route) => { await sleep(1500); await route.continue(); });
  await b.goto(`${base}/p/${p.slug}`, { waitUntil: "load" });
  await b.waitForSelector(".scriobh-prose");
  await b.locator(".scriobh-prose").click();
  await b.keyboard.press(`${MOD}+End`);
  await b.keyboard.type(" Typed during the wait.", { delay: 5 });
  await waitState(b, "conflict", 10000);
  ok((await b.locator("#notices").innerText()).includes("Keep mine"), "no conflict choices for text typed before the lease answer");
  eq(await editorMd(b), "Holder text. Typed during the wait.", "the late tab keeps its text");
  ok(await b.evaluate((id) => !!localStorage.getItem(`scriobh:draft:${document.querySelector('meta[name="scriobh-user"]').content}:${id}`), p.doc), "no local draft in the late tab");
  // Keep theirs must store a copy first, then show theirs.
  await b.getByRole("button", { name: "Keep theirs" }).click();
  await waitFor(() => existsConflictCopy(p.id, "Holder text. Typed during the wait."), "a copy of the typed text");
  await b.close();
});

await test("link: dialog adds it, the button removes it", async () => {
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Visit the harbour office today.");
  await pg.keyboard.press("Home");
  for (let i = 0; i < 10; i++) await pg.keyboard.press("ArrowRight");
  for (let i = 0; i < 7; i++) await pg.keyboard.press("Shift+ArrowRight");
  await pg.getByRole("button", { name: "Link" }).click();
  const url = pg.locator("#link-url");
  await url.waitFor();
  await url.fill("https://example.com/harbour");
  await pg.keyboard.press("Enter");
  await waitSaved(pg);
  await waitBody(main, p.doc, "Visit the [harbour](https://example.com/harbour) office today.");
  // Caret inside the link: the button now unlinks.
  await pg.locator(".scriobh-prose a").click();
  await waitFor(async () => (await pg.getByRole("button", { name: "Link" }).getAttribute("aria-pressed")) === "true", "Link to show as active");
  await pg.getByRole("button", { name: "Link" }).click();
  await waitSaved(pg);
  await waitFor(async () => !(await body(main, p.doc)).includes("]("), "the link to be removed");
});

await test("trashing the open document: flush, move on, Undo brings it back", async () => {
  const { p, ids } = await treeSetup(["Two"]);
  await open(pg, p, ids.One);
  await typeText(pg, "Unsaved when trashed.");
  await (await rowFor(pg, "One")).focus();
  await pg.keyboard.press("Delete");
  await pg.waitForURL(new RegExp(`/d/${ids.Two}`), { timeout: 10000 });
  await pg.waitForSelector(".scriobh-prose");
  eq((await api(main, "GET", `/api/v1/docs/${ids.One}`)).json.item.trashed, true, "trashed on the server");
  eq((await api(main, "GET", `/api/v1/docs/${ids.One}`)).json.item.content, "Unsaved when trashed.\n", "the text was saved before the trash");
  await pg.locator("#toasts").getByRole("button", { name: "Undo" }).click();
  await pg.waitForURL(new RegExp(`/d/${ids.One}$`), { timeout: 10000 });
  await pg.waitForSelector(".scriobh-prose");
  eq(await editorMd(pg), "Unsaved when trashed.", "text after Undo");
  eq((await api(main, "GET", `/api/v1/docs/${ids.One}`)).json.item.trashed, false, "restored on the server");
});

await test("row menu (the touch route): indent, move to, trash, on a phone", async () => {
  const { p, ids } = await treeSetup(["Two", "Three"]);
  const part = await createDoc(main, p.id, { title: "A Part", kind: "part" });
  const ctx = await newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true, isMobile: true });
  await ctx.addCookies(await main.cookies());
  const page = await ctx.newPage();
  watch(page, "phone menu");
  await open(page, p, ids.One);
  await page.locator("#tree-toggle").tap();
  const moreOf = (t) => page.locator("#tree .tree-row", { has: page.locator(".tree-title", { hasText: new RegExp(`^${t}$`) }) }).locator(".tree-more");
  await moreOf("Two").tap();
  await page.locator("#row-menu").waitFor({ state: "visible" });
  await page.getByRole("button", { name: "Indent" }).tap();
  await waitFor(async () => (await serverOrder(p)).includes("-Two"), "Indent from the menu");
  await waitFor(async () => (await rowTitles(page)).length === 4 && (await page.locator("#row-menu").evaluate((d) => !d.open)), "the tree to redraw");
  await moreOf("Three").tap();
  await page.getByRole("button", { name: "Move to…" }).tap();
  await page.locator("#row-menu").getByRole("button", { name: /A Part/ }).tap();
  await waitFor(async () => (await serverOrder(p)).join("|").endsWith("A Part|-Three"), "Move to a part");
  await waitFor(async () => (await page.locator("#row-menu").evaluate((d) => !d.open)), "the menu to close");
  await moreOf("Three").tap();
  await page.getByRole("button", { name: "Move to trash" }).tap();
  await waitFor(async () => !(await serverOrder(p)).some((x) => x.endsWith("Three")), "trash from the menu");
  await csp(page, "phone menu");
  await ctx.close();
});

// ---- findings from the independent review of the writing view: each has a check here -------------------------------

await test("review: a 413 and then a title-only save never marks the oversize text saved", async () => {
  allowed = [/status of 413/, /Failed to load resource/];
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Small first.");
  await waitSaved(pg);
  const big = "word ".repeat(450000);
  await pg.keyboard.press(`${MOD}+e`);
  const src = pg.locator(".scriobh-source");
  await src.waitFor();
  await src.evaluate((t, v) => { t.value = v; t.dispatchEvent(new Event("input", { bubbles: true })); }, big);
  await pg.evaluate(() => window.scriobh.write.editor.flush());
  await waitState(pg, "toolarge", 15000);
  // A title change goes through; the oversize text is still unsaved, still in the editor, still in the draft.
  const title = pg.locator("#doc-title");
  await title.fill("A new title while too large");
  await title.press("Tab");
  await waitFor(async () => (await serverDoc(main, p.doc)).title === "A new title while too large", "the title save");
  await sleep(600);
  eq(await state(pg), "toolarge", "the state after a title-only save");
  ok(await pg.evaluate(() => window.scriobh.write.unsaved()), "the oversize text must still count as unsaved");
  ok(await unsavedKeys(pg, p.doc), "the draft was deleted although the text was never saved");
  eq((await body(main, p.doc)), "Small first.", "the server body");
  const nav = await pg.evaluate(() => window.scriobh.write.flushAll());
  eq(nav, false, "leaving is refused while the text is unsaved");
});

await test("review: a held draft survives the first keystroke; take over offers it; quota is said aloud; sign-out clears", async () => {
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Server text.");
  await waitSaved(pg);
  const rev = (await serverDoc(main, p.doc)).revision;
  const plant = (md) => pg.evaluate(([id, md, rev]) => localStorage.setItem(`scriobh:draft:${document.querySelector('meta[name="scriobh-user"]').content}:${id}`, JSON.stringify({ md, title: "Chapter 1", rev, t: Date.now() })), [p.doc, md, rev]);
  await plant("Server text. Held tail.");
  await pg.reload({ waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  // Type before answering the bar: the offered draft must not be overwritten.
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${MOD}+End`);
  await pg.keyboard.type(" Typed first.");
  await waitSaved(pg);
  const restore = pg.getByRole("button", { name: "Restore your unsaved text" });
  await restore.click();
  await waitFor(async () => (await editorMd(pg)) === "Server text. Held tail.", "the held draft (not the new typing) to come back");
  await waitSaved(pg);
  await waitFor(() => existsConflictCopy(p.id, "Server text. Typed first."), "a copy of what was typed meanwhile");
  // A second tab id is locked; after Take over the held draft is offered there.
  await plant("Server text. Held tail. Again.");
  const b = await main.newPage();
  watch(b, "tab B");
  await b.addInitScript(() => sessionStorage.setItem("scriobh:tab", "tab-b-held"));
  await b.goto(`${base}/p/${p.slug}`, { waitUntil: "load" });
  await b.waitForSelector(".scriobh-prose");
  await waitState(b, "locked");
  eq(await b.getByRole("button", { name: "Restore your unsaved text" }).count(), 0, "a locked tab must not offer a draft");
  await b.getByRole("button", { name: "Take over" }).click();
  await b.getByRole("button", { name: "Restore your unsaved text" }).waitFor();
  await b.close();
  // A full or blocked store is said aloud, and leaves no stale draft.
  const c = await main.newPage();
  watch(c, "quota");
  await c.addInitScript(() => { const f = Storage.prototype.setItem; Storage.prototype.setItem = function (k, v) { if (/^scriobh:draft:/.test(k)) throw new DOMException("full", "QuotaExceededError"); return f.call(this, k, v); }; });
  await c.goto(`${base}/p/${p.slug}`, { waitUntil: "load" });
  await c.waitForSelector(".scriobh-prose");
  await c.locator(".scriobh-prose").click();
  await c.keyboard.press(`${MOD}+End`);
  await c.keyboard.type(" Quota.");
  await c.waitForSelector("#nb-quota", { timeout: 5000 });
  await c.close();
  // Signing out saves first, then leaves nothing of the manuscript in the browser.
  await plant("Left behind.");
  await pg.goto(`${base}/p/${p.slug}`, { waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  await pg.locator("#nb-draft").getByRole("button", { name: "Discard" }).click().catch(() => {});
  await plant("Left behind.");
  const keys = () => pg.evaluate(() => Object.keys(localStorage).filter((k) => /^scriobh:(draft|held):/.test(k)));
  await pg.getByRole("button", { name: /^Account/ }).click();
  await pg.getByRole("button", { name: "Sign out", exact: true }).click();
  await armOk(pg); // text the server never had: the page asks first
  await pg.waitForURL((u) => !u.pathname.startsWith("/p/"), { timeout: 10000 });
  eq((await keys()).length, 0, "drafts left in the browser after sign-out");
  // Sign back in for what follows.
  const again = await signIn(pg, base).catch(() => null);
  void again;
});

await test("review: trashing the last document is undoable on the page it lands on", async () => {
  const p = await newProject(main);
  await open(pg, p);
  await (await rowFor(pg, "Chapter 1")).focus();
  await pg.keyboard.press("Delete");
  // The page it lands on is the project page, which is also the page the walk started on, so the URL cannot say the
  // landing happened; the line the server renders for a trashed document can. The format bar has its own button named
  // "Undo" (the editor's history), so the trash's Undo is found in its own line, not by name alone.
  const undoLine = pg.locator(".undo-line");
  await undoLine.waitFor({ timeout: 10000 });
  const undo = undoLine.getByRole("button", { name: "Undo" });
  ok((await pg.locator("body").innerText()).includes("Moved “Chapter 1” to the trash."), "no message");
  await undo.click();
  await pg.waitForURL(new RegExp(`/d/${p.doc}`), { timeout: 10000 });
  await pg.waitForSelector(".scriobh-prose");
  // A crafted link is ignored, and the page still works.
  await pg.goto(`${base}/p/${p.slug}/d/${p.doc}?trashed=%&tt=%E0%A4%A`, { waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  ok(await pg.evaluate(() => !!(window.scriobh.tree && window.scriobh.write)), "tree.js stopped on a crafted ?trashed= link");
  await pg.goto(`${base}/p/${p.slug}?trashed=doc_../../x`, { waitUntil: "load" });
  ok(!(await pg.locator(".undo-line, #toasts").getByRole("button", { name: "Undo" }).count()), "an Undo for something that is not a trashed document of this project");
});

await test("review: the palette never lets an expired session navigate the writing page away", async () => {
  allowed = [/status of 401/, /ERR_/, /Failed to load resource/];
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "Typed before the session ended.");
  await waitSaved(pg);
  const cookies = await main.cookies();
  await main.clearCookies();
  await typeText(pg, " And after.");
  await waitState(pg, "signedout");
  const url = pg.url();
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("export");
  await pg.keyboard.press("Enter");
  await sleep(1200);
  eq(pg.url(), url, "the page navigated while signed out");
  eq(await editorMd(pg), "Typed before the session ended. And after.", "the text");
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("new project");
  await pg.keyboard.press("Enter");
  await sleep(800);
  eq(pg.url(), url, "New project navigated while signed out");
  await main.addCookies(cookies);
  await pg.evaluate(() => window.dispatchEvent(new Event("focus")));
  await waitSaved(pg, 12000);
  await waitBody(main, p.doc, "Typed before the session ended. And after.");
});

await test("review: a locked or reader tab does not move the writing tab's draft", async () => {
  const p = await newProject(main);
  await open(pg, p);
  await typeText(pg, "First.");
  await waitSaved(pg);
  // Hold the next save so the draft is live while a second tab opens.
  let release = () => {};
  const gate = new Promise((r) => { release = r; });
  await pg.route("**/api/v1/docs/*", async (route) => { if (route.request().method() === "PATCH") await gate; return route.continue(); });
  await typeText(pg, " Second.");
  await waitFor(() => unsavedKeys(pg, p.doc), "the draft to be written");
  const b = await main.newPage();
  watch(b, "locked tab");
  await b.addInitScript(() => sessionStorage.setItem("scriobh:tab", "tab-other-hold"));
  await b.goto(`${base}/p/${p.slug}`, { waitUntil: "load" });
  await b.waitForSelector(".scriobh-prose");
  await waitState(b, "locked");
  await b.close();
  release();
  await waitSaved(pg);
  await typeText(pg, " Third.");
  await waitSaved(pg);
  await pg.unroute("**/api/v1/docs/*");
  await pg.reload({ waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  await sleep(600);
  eq(await pg.getByRole("button", { name: "Restore your unsaved text" }).count(), 0, "an already-saved draft is offered back");
  eq(await editorMd(pg), "First. Second. Third.", "the text");
});

await test("review: a collapsed folder never hides the current row; there is always one tab stop", async () => {
  const { p, ids } = await treeSetup(["Two"]);
  const part = await createDoc(main, p.id, { title: "A Part", kind: "part" });
  await api(main, "POST", `/api/v1/docs/${ids.Two}/move`, { parent_id: part.id });
  await pg.evaluate(([k, id]) => localStorage.setItem(k, JSON.stringify([id])), [`scriobh:collapsed:${p.id}`, part.id]);
  await open(pg, p, ids.Two);
  const cur = pg.locator(`#tr-${ids.Two}`);
  ok(await cur.isVisible(), "the current row is hidden by a collapsed ancestor");
  eq(await pg.locator("#tree .tree-row[tabindex='0']:not([hidden])").count(), 1, "exactly one visible tab stop");
});

await test("review: a slow tree answer does not take focus from the editor; the page's navigation follows the tree", async () => {
  const { p, ids } = await treeSetup(["Two", "Three"]);
  await open(pg, p, ids.One);
  await pg.route("**/ui/docs/*/move", async (route) => { await sleep(700); await route.continue(); });
  await (await rowFor(pg, "Three")).focus();
  await pg.keyboard.press("Alt+ArrowUp");
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.type("Typing while the tree redraws.");
  await sleep(1200);
  eq(await pg.evaluate(() => document.activeElement.classList.contains("scriobh-prose")), true, "focus stayed in the editor");
  await waitFor(async () => (await serverOrder(p)).join("|") === "One|Three|Two", "the move");
  await pg.unroute("**/ui/docs/*/move");
  // Previous, next, the position and the jump list were rebuilt from the tree.
  await waitFor(async () => (await pg.locator("#jump-list li .j-title").allInnerTexts()).join("|") === "One|Three|Two", "the jump list to follow the tree");
  eq(await pg.locator("#write").getAttribute("data-next"), `/p/${p.slug}/d/${ids.Three}`, "data-next");
  ok((await pg.locator("#st-pos").innerText()).startsWith("Document 1 of 3"), "position: " + (await pg.locator("#st-pos").innerText()));
  await waitSaved(pg);
});

await test("review: a title the server normalises is saved once, not in a loop", async () => {
  const p = await newProject(main);
  await open(pg, p);
  let patches = 0;
  await pg.route("**/api/v1/docs/*", (route) => { if (route.request().method() === "PATCH") patches++; return route.continue(); });
  const title = pg.locator("#doc-title");
  await title.click();
  await title.evaluate((t) => { t.value = "Edge title\u0085"; t.dispatchEvent(new Event("input", { bubbles: true })); });
  await title.press("Tab");
  await waitSaved(pg);
  await sleep(2500);
  ok(patches <= 2, `${patches} saves for one title change`);
  await pg.unroute("**/api/v1/docs/*");
});

await test("review: Mod+K that removes a link does not also open the palette", async () => {
  const p = await newProject(main);
  await open(pg, p);
  await pg.locator(".scriobh-prose").click();
  await pg.evaluate(() => window.scriobh.write.editor.pasteMarkdown("A [link](https://example.com/x) here."));
  await pg.evaluate(() => { const a = document.querySelector(".scriobh-prose a"); getSelection().selectAllChildren(a); }); // the whole link is selected
  await sleep(150);
  await pg.keyboard.press(`${MOD}+k`);
  await sleep(300);
  eq(await pg.locator(".scriobh-prose a").count(), 0, "the link was not removed");
  eq(await pg.locator("dialog#palette").evaluate((d) => d.open), false, "the palette opened as well");
  await pg.keyboard.press(`${MOD}+/`);
  await waitFor(() => pg.locator("dialog#palette").evaluate((d) => d.open), "the palette from Mod+/").catch(async (e) => {
    throw new Error(e.message + " " + JSON.stringify(await pg.evaluate(() => ({ dialogs: [...document.querySelectorAll("dialog[open]")].map((d) => d.id), active: document.activeElement.tagName + "." + document.activeElement.className, url: location.pathname, scripts: [...document.scripts].map((s) => s.src.split("/").pop()) }))));
  });
  await pg.keyboard.press("Escape");
});

await test("a non-member gets the same Not found (the role read-only editor is covered by the Go page test)", async () => {
  allowed = [/status of 404/];
  const p = await newProject(main);
  const reader = await newSignedIn({}, "reader@dev.scriobh");
  // The reader is not a member: 404. Make one a member through the API is not available yet, so check the 404.
  const r = await reader.page.goto(`${base}/p/${p.slug}`);
  eq(r.status(), 404, "a stranger gets the same Not found page");
  ok(!(await reader.page.locator("body").innerText()).includes(p.title), "the 404 leaks the title");
  await reader.ctx.close();
});

// ---------------------------------------------------------------- the phone

if (!noPhone) await test("phone 390x844, 4x CPU: typing latency, drawer, sheet, formatting bar", async () => {
  const ctx = await newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true, isMobile: true,
    userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1" });
  await ctx.addCookies(await main.cookies());
  const page = await ctx.newPage();
  watch(page, "phone");
  await page.addInitScript(() => {
    window.__mount = 0;
    new MutationObserver((_, o) => { if (document.querySelector(".scriobh-prose")) { window.__mount = performance.now(); o.disconnect(); } }).observe(document, { childList: true, subtree: true });
    window.__lat = []; window.__proc = []; window.__slow = [];
    try { new PerformanceObserver((l) => { for (const e of l.getEntries()) if (/^(keydown|keypress|keyup|beforeinput|input)$/.test(e.name)) window.__slow.push([e.name, e.duration]); }).observe({ type: "event", durationThreshold: 16 }); } catch { /* not supported */ }
    let t0 = 0;
    addEventListener("keydown", () => { t0 = performance.now(); requestAnimationFrame(() => setTimeout(() => window.__lat.push(performance.now() - t0), 0)); }, true);
    addEventListener("input", () => { if (t0) window.__proc.push(performance.now() - t0); }, true);
  });
  const p = await newProject(main, "Phone Book");
  const cdp = await ctx.newCDPSession(page);
  await cdp.send("Emulation.setCPUThrottlingRate", { rate: Number(process.env.E2E_CPU || 4) });
  const t0 = Date.now();
  await page.goto(`${base}/p/${p.slug}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose");
  const interactive = await page.evaluate(() => {
    const nav = performance.getEntriesByType("navigation")[0];
    const ed = performance.getEntriesByType("resource").find((r) => /\/js\/editor\./.test(r.name));
    return { mount: window.__mount, editorEnd: ed ? ed.responseEnd : 0, load: nav.loadEventEnd };
  });
  console.log(`      interactive: editor mounted ${interactive.mount.toFixed(0)} ms after navigation start, ${(interactive.mount - interactive.editorEnd).toFixed(0)} ms after the editor bundle arrived (${process.env.E2E_CPU || 4}x CPU throttle)`);
  // The drawer, the chapter sheet and the formatting bar.
  ok(await page.locator("#fmtbar").isVisible(), "formatting bar visible on a phone");
  const box = await page.locator("#fmtbar").boundingBox();
  ok(box.height >= 44 && box.y + box.height <= 844 - 56 + 1, `formatting bar geometry ${JSON.stringify(box)}`);
  // The buttons the bar shows (the strip is behind Aa on a phone, U11.3: closed, Undo, Redo and Aa; open, all of them), 44 px each way.
  const smallShown = () => page.locator("#fmtbar .tb").evaluateAll((bs) => bs.filter((b) => b.getClientRects().length > 0).filter((b) => b.getBoundingClientRect().width < 43.5 || b.getBoundingClientRect().height < 43.5).length);
  eq(await smallShown(), 0, "touch targets below 44 px in the formatting bar");
  await page.locator("#fmt-aa").tap();
  eq(await smallShown(), 0, "touch targets below 44 px in the formatting bar with Aa open");
  await page.locator("#fmt-aa").tap();
  await page.locator("#tree-toggle").tap();
  await waitFor(async () => (await page.locator("#rail").evaluate((r) => r.classList.contains("is-open"))), "drawer to open");
  ok(await page.locator("#tree-filter").isVisible(), "search field at the top of the drawer");
  await page.locator("#tree-filter").fill("zzz");
  eq(await page.locator("#tree .tree-row:not([hidden])").count(), 0, "filter hides rows");
  await page.locator("#tree-filter").fill("");
  await page.keyboard.press("Escape");
  await waitFor(async () => !(await page.locator("#rail").evaluate((r) => r.classList.contains("is-open"))), "drawer to close");
  eq(await page.evaluate(() => document.activeElement.id), "tree-toggle", "focus returns to the opener");
  await page.locator("#jump-open").tap();
  ok(await page.locator("#jump").evaluate((d) => d.open), "chapter sheet opens");
  const sheet = await page.locator("#jump").boundingBox();
  ok(Math.abs(sheet.y + sheet.height - 844) < 2, `the sheet is a bottom sheet: ${JSON.stringify(sheet)}`);
  await page.keyboard.press("Escape");
  // Typing 2,000 characters.
  await page.locator(".scriobh-prose").tap();
  const sentence = "The tide had gone out and taken the light with it, and the boats lay over on their sides. ";
  const text = sentence.repeat(23).slice(0, 2000);
  await page.keyboard.type(text, { delay: 14 });
  await waitSaved(page, 30000);
  const { lat, proc, slow } = await page.evaluate(() => ({ lat: window.__lat, proc: window.__proc, slow: window.__slow }));
  const q = (a, f) => { const s = [...a].sort((x, y) => x - y); return s[Math.min(s.length - 1, Math.floor(s.length * f))]; };
  console.log(`      ${lat.length} keystrokes at ${process.env.E2E_CPU || 4}x CPU: keydown to input event p50 ${q(proc, 0.5).toFixed(1)} ms, p95 ${q(proc, 0.95).toFixed(1)} ms; keydown to next frame p50 ${q(lat, 0.5).toFixed(1)} ms, p95 ${q(lat, 0.95).toFixed(1)} ms, max ${Math.max(...lat).toFixed(1)} ms`);
  console.log(`      Event Timing API: ${slow.length} key event(s) of ${lat.length} took 16 ms or more from input to next paint${slow.length ? ` (max ${Math.max(...slow.map((x) => x[1]))} ms)` : ""}`);
  eq(lat.length >= 1900, true, "keystrokes measured");
  await waitBody(main, p.doc, trimNl(await page.evaluate(() => window.scriobh.write.editor.getMarkdown())));
  await csp(page, "phone");
  await ctx.close();
});

// -------------------------------------------------------------------- report

await browser.close();
await srv.stop();
const conflicts = existsSync(join(dataDir, "conflicts"));
rm(tmp);
console.log("");
for (const f of findings) console.log("FINDING " + f);
const failed = results.filter((r) => !r.ok).length;
console.log(`e2e-write: ${results.length - failed} of ${results.length} steps passed${findings.length ? `, ${findings.length} watcher finding(s)` : ", no console, CSP or off-origin findings"}`);
process.exit(failed || findings.length ? 1 : 0);

function existsConflictCopy(projectId, text) {
  const dir = join(dataDir, "conflicts", projectId);
  if (!existsSync(dir)) return false;
  return readdirSync(dir).some((f) => trimNl(readFileSync(join(dir, f), "utf8")) === text);
}
