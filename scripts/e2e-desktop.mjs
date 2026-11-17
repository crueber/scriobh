#!/usr/bin/env node
// Desktop mode in Chromium, against the production build (-tags prod: no development issuer in the binary) started the
// way the desktop shell starts it: SCRIOBH_DESKTOP=1, a launch token, loopback, no identity provider configured.
//
//   the window     the start address exchanges the token for the launch cookie once; a wrong or reused token is an unknown
//                  address; the first run shows the local profile form (keyboard only, 390 px, a refused form says why)
//                  and then signs in through the embedded issuer with no further page
//   strangers      a second browser context without the launch cookie cannot sign in; a page of another site, and a page
//                  on another port of this computer, cannot get the issuer to answer (fetch, frame, link); a request
//                  that names another Host is refused
//   the app        a project is made, written in, searched and exported; signing out shows the sign-in page and signing
//                  in again needs no form; the pages about this computer open from the menu
//   the worker     with the service worker active (as in the shell's window) signing in still works
//   a restart      the next launch binds the same port, so a draft the browser held for a chapter is offered again
//   privacy        the browser asks nothing of any other origin, and the server opened no connection to anywhere but
//                  this computer (desktop.info); the look for a local model asks only when its button is pressed
//   xx-rtl         the profile page in the right-to-left pseudo-locale, on a development build in desktop mode (a
//                  production build never serves the pseudo-locales, which is checked too)
//
//   node scripts/e2e-desktop.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { build, desktopEnv, launchToken, startServer } from "./harness.mjs";
import { createServer, request } from "node:http";
import { mkdirSync } from "node:fs";
import { join } from "node:path";

const token = launchToken();
const kit = await createKit("e2e-desktop", { tags: "prod", env: { ...desktopEnv(token), SCRIOBH_SECRET_KEY: "e2e-desktop-secret-key-0123456789abcdef" } });
kit.deadline(8 * 60 * 1000);
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const base = kit.base();
const port = new URL(base).port;
const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);

// A page of somebody else's, served from another port. Opened as http://localhost:<port> it is another site (the
// browser's strictest case for cookies); as http://127.0.0.1:<port> it is the same site and another origin, which is
// what any other program on this computer can be. It tries every way a page has of making a browser ask the issuer.
const authorize = `${base}/desktop-idp/authorize?` + new URLSearchParams({ client_id: "scriobh-desktop", redirect_uri: `${base}/auth/oidc/callback`, response_type: "code", scope: "openid",
  state: "evil", nonce: "evil", code_challenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", code_challenge_method: "S256" });
const evil = createServer((req, res) => {
  if (req.url === "/evil.js") {
    res.writeHead(200, { "Content-Type": "text/javascript" });
    res.end(`const u = document.documentElement.dataset.u; const out = {};
      const tries = [["cors", { mode: "cors", credentials: "include" }], ["no-cors", { mode: "no-cors", credentials: "include" }]];
      Promise.all(tries.map(([k, o]) => fetch(u, o).then((r) => { out[k] = r.type + " " + r.status; }, (e) => { out[k] = "error"; }))).then(() => { window.__evil = out; });`);
    return;
  }
  res.writeHead(200, { "Content-Type": "text/html" });
  res.end(`<!doctype html><html data-u="${authorize.replace(/&/g, "&amp;")}"><title>elsewhere</title><a id="go" href="${authorize.replace(/&/g, "&amp;")}">go</a><iframe id="fr" src="${authorize.replace(/&/g, "&amp;")}"></iframe><img src="${authorize.replace(/&/g, "&amp;")}"><script src="/evil.js"></script></html>`);
});
await new Promise((r) => evil.listen(0, "127.0.0.1", r));
const evilPort = evil.address().port;
kit.origins = [`http://localhost:${evilPort}`, `http://127.0.0.1:${evilPort}`];

const raw = (path, headers = {}) => new Promise((res, rej) => {
  const r = request({ host: "127.0.0.1", port, path, method: "GET", headers }, (m) => { m.resume(); m.on("end", () => res(m.statusCode)); });
  r.on("error", rej); r.end();
});
const callbacks = () => kit.srv.log.split("\n").filter((l) => l.includes('"route":"GET /auth/oidc/callback"')).length;
const refusals = (reason) => kit.srv.log.split("\n").filter((l) => l.includes("the embedded issuer refused a request") && l.includes(`"reason":"${reason}"`)).length;

const ctx = await kit.newContext({ viewport: { width: 1280, height: 800 } });
const page = await ctx.newPage();
kit.watch(page, "window");

await kit.test("before the window opens: a browser cannot sign in, a wrong token is an unknown address, another Host is refused", async () => {
  kit.allowed.push(/Failed to load resource/);
  const sctx = await kit.newContext();
  const sp = await sctx.newPage();
  kit.watch(sp, "stranger");
  let r = await sp.goto(base + "/", { waitUntil: "load" });
  eq(r.status(), 200, "the sign-in page");
  ok(await sp.getByRole("link", { name: "Sign in" }).count(), "the sign-in page is shown, not started");
  r = await sp.goto(base + "/auth/login", { waitUntil: "load" });
  eq(r.status(), 403, "sign-in without the launch cookie");
  ok((await sp.locator("main").innerText()).includes("its own window"), "and says why");
  r = await sp.goto(base + "/desktop/start?t=" + "0".repeat(64), { waitUntil: "load" });
  eq(r.status(), 404, "a wrong launch token");
  eq((await sctx.cookies()).filter((c) => c.name === "scriobh_desktop_launch").length, 0, "no launch cookie for a wrong token");
  r = await sp.goto(base + "/desktop/profile", { waitUntil: "load" });
  eq(r.status(), 404, "the profile form for a stranger");
  eq(await raw("/healthz", { Host: "evil.example:" + port }), 421, "a request that names another Host (DNS rebinding)");
  eq(await raw("/", { Host: "evil.example" }), 421, "another Host, no port");
  eq(await raw("/desktop-idp/jwks", { Host: `127.0.0.1.evil.example:${port}` }), 421, "a look-alike Host at the issuer");
  eq(await raw("/healthz", { Host: "localhost:" + port }), 421, "localhost is not this instance's name (it can resolve to ::1, where nothing listens)");
  eq(await raw("/healthz", { Host: "127.0.0.1:" + port }), 200, "the bound address is");
  eq(await raw("/dev-idp/.well-known/openid-configuration"), 404, "the development issuer is not in this build");
  await sctx.close();
});

await kit.test("the first run: the profile form by keyboard, at 390 px, and what a mistake shows", async () => {
  kit.main = page;
  await page.goto(`${base}/desktop/start?t=${token}`, { waitUntil: "load" });
  eq(new URL(page.url()).pathname, "/desktop/profile", "the first run lands on the profile form");
  ok(!page.url().includes(token), "the token is not in the address the window ends on");
  const launch = (await ctx.cookies()).find((c) => c.name === "scriobh_desktop_launch");
  ok(launch && launch.httpOnly && launch.sameSite === "Strict" && !launch.secure && launch.expires === -1, "the launch cookie is HttpOnly, SameSite=Strict, a session cookie: " + JSON.stringify(launch));
  ok(!launch.value.includes(token), "the cookie is not the token");
  const main = await page.locator("main").innerText();
  ok(/local profile/.test(main) && /no password/.test(main), "the page says this is a local profile with no password: " + main.slice(0, 200));
  eq(await page.locator("h1").count(), 1, "one heading");
  eq(await page.locator("html").getAttribute("lang"), "en", "a production build serves no pseudo-locale");
  // 390 px: nothing scrolls sideways, the fields are full targets
  await page.setViewportSize({ width: 390, height: 844 });
  ok(!(await sideways(page)), "the profile page scrolls sideways at 390 px");
  const box = await page.locator("#dp-name").boundingBox();
  ok(box.height >= 40 && box.width >= 280, "the name field at 390 px: " + JSON.stringify(box));
  if (shots) await page.screenshot({ path: join(shots, "desktop-profile-390.png"), fullPage: true });
  await page.setViewportSize({ width: 1280, height: 800 });
  if (shots) await page.screenshot({ path: join(shots, "desktop-profile-1280.png") });
  // keyboard only: Tab reaches the name, the address and the button in that order
  await page.locator("body").click({ position: { x: 5, y: 5 } });
  const seen = [];
  for (let i = 0; i < 12 && seen[seen.length - 1] !== "submit"; i++) {
    await page.keyboard.press("Tab");
    const at = await page.evaluate(() => { const e = document.activeElement; return e.id || (e.type === "submit" ? "submit" : e.className || e.tagName); });
    seen.push(at);
    if (at === "dp-name") await page.keyboard.type("Nuala Ní Dhomhnaill");
    if (at === "dp-email") await page.keyboard.type("not an address");
  }
  const order = seen.filter((s) => ["dp-name", "dp-email", "submit"].includes(s)).join(" ");
  eq(order, "dp-name dp-email submit", "the Tab order of the form (" + seen.join(", ") + ")");
  kit.allowed.push(/422/);
  await Promise.all([page.waitForLoadState("load"), page.keyboard.press("Enter")]);
  await page.waitForSelector("#dp-error");
  eq(await page.locator("#dp-email").getAttribute("aria-invalid"), "true", "the wrong field is marked");
  ok((await page.locator("#dp-email").getAttribute("aria-describedby")).includes("dp-error"), "and points at the message");
  eq(await page.locator("#dp-error").getAttribute("role"), "alert", "the message is announced");
  eq(await page.locator("#dp-name").inputValue(), "Nuala Ní Dhomhnaill", "what was typed is kept");
  await page.fill("#dp-email", "");
  await page.locator("#dp-name").focus();
  await Promise.all([page.waitForURL(base + "/"), page.keyboard.press("Enter")]);
  ok(await page.locator("body.signed-in").count(), "signed in after the profile, with no other page");
  ok((await page.locator("#account").innerText().catch(() => "")).includes("Nuala") || (await page.content()).includes("Nuala Ní Dhomhnaill"), "as the profile");
});

await kit.test("the token is good once, and a second browser without the launch cookie still cannot sign in", async () => {
  kit.allowed.push(/Failed to load resource/);
  const sctx = await kit.newContext();
  const sp = await sctx.newPage();
  kit.watch(sp, "stranger");
  let r = await sp.goto(`${base}/desktop/start?t=${token}`, { waitUntil: "load" });
  eq(r.status(), 404, "the launch token a second time");
  r = await sp.goto(base + "/auth/login", { waitUntil: "load" });
  eq(r.status(), 403, "sign-in in a second context");
  r = await sp.goto(authorize, { waitUntil: "load" });
  eq(r.status(), 403, "the issuer, asked directly by a browser without the cookie");
  r = await sp.goto(base + "/api/v1/desktop/info", { waitUntil: "load" });
  eq(r.status(), 401, "desktop.info signed out");
  ok(!(await sctx.cookies()).some((c) => c.name === "scriobh_session"), "no session for the stranger");
  await sctx.close();
});

await kit.test("another site's page, and a page on another port of this computer, cannot make the issuer answer", async () => {
  kit.allowed.push(/Failed to load resource/, /CORS/, /blocked/i, /Refused to (display|frame)/, /X-Frame-Options/, /ERR_/);
  for (const [name, origin] of [["another site", `http://localhost:${evilPort}`], ["another port of this computer", `http://127.0.0.1:${evilPort}`]]) {
    const before = refusals("cross-site") + refusals("no-launch-cookie"), cbBefore = callbacks();
    const ep = await ctx.newPage(); // the window's own browser: it holds the launch cookie and the session
    const answers = [];
    ep.on("response", (r) => { if (r.url().startsWith(base + "/desktop-idp/authorize")) answers.push(r.status()); });
    ep.on("request", (r) => { if (r.url().startsWith(base + "/auth/oidc/callback")) answers.push("CALLBACK"); });
    await ep.goto(origin + "/", { waitUntil: "load" });
    await ep.waitForFunction(() => window.__evil, null, { timeout: 10000 });
    const seen = await ep.evaluate(() => window.__evil);
    ok(!/basic 200|cors 200/.test(JSON.stringify(seen)), `${name}: a fetch read an answer: ${JSON.stringify(seen)}`);
    // a link the person is tricked into following: a top-level navigation from the other page
    await Promise.all([ep.waitForLoadState("load"), ep.click("#go")]);
    await kit.sleep(300);
    // What the server saw is the measure (the browser hides a blocked answer from the page, and from this script): two
    // fetches, the frame, the image and the link, each refused, and no callback.
    const refused = refusals("cross-site") + refusals("no-launch-cookie") - before;
    ok(refused >= 5, `${name}: the issuer refused ${refused} requests, want the two fetches, the frame, the image and the link`);
    ok(answers.length >= 1 && answers.every((s) => s === 403), `${name}: the issuer answered ${answers.join(", ")}, want 403 every time and never the callback`);
    eq(callbacks() - cbBefore, 0, `${name}: the application's callback was reached`);
    ok(!ep.url().includes("code="), `${name}: the navigation ended with a code: ${ep.url()}`);
    await ep.close();
  }
  // the control: the window's own navigation gets a code and is signed in
  const cp = await ctx.newPage();
  const r = await cp.goto(authorize, { waitUntil: "load" });
  ok(cp.url().startsWith(base + "/"), "the window's own navigation is answered: " + cp.url() + " " + (r && r.status()));
  await cp.close();
});

let pid = "", doc = "", slug = "";
await kit.test("a project is made, written in, searched and exported", async () => {
  kit.main = page;
  const made = await api(ctx, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel" });
  eq(made.status, 201, "project.create");
  pid = made.json.item.id; slug = made.json.item.slug;
  doc = (await api(ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
  const g = (await api(ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
  eq((await api(ctx, "PATCH", `/api/v1/docs/${doc}`, { base_revision: g.revision, content: "The tide came in over the grey sand.\n" })).status, 200, "doc.update");
  await page.goto(base + "/", { waitUntil: "load" });
  ok((await page.locator("main").innerText()).includes("The Salt Road"), "the library lists the project");
  await page.goto(`${base}/p/${made.json.item.slug}/d/${doc}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose", { timeout: 15000 });
  ok((await page.locator(".scriobh-prose").innerText()).includes("grey sand"), "the writing view shows the text");
  await page.locator(".scriobh-prose").click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type(" And went out again.");
  await kit.waitFor(async () => ((await api(ctx, "GET", `/api/v1/docs/${doc}`)).json.item.content || "").includes("went out again"), "the typed words to be saved", 15000);
  const found = await api(ctx, "GET", `/api/v1/projects/${pid}/search?q=tide`);
  ok(found.status === 200 && JSON.stringify(found.json).includes(doc), "search finds the chapter");
  const exp = await ctx.request.fetch(`${base}/api/v1/projects/${pid}/export`, { method: "POST", data: { mode: "single" }, headers: { "Content-Type": "application/json" } });
  eq(exp.status(), 200, "export.markdown");
  ok((await exp.text()).includes("grey sand"), "the export holds the text");
  ok(/^attachment/.test(exp.headers()["content-disposition"] || ""), "the export is sent as a download, which the shell hands to its save dialog: " + exp.headers()["content-disposition"]);
});

await kit.test("the pages about this computer: reached from the menu, and the look for a model asks only when pressed", async () => {
  kit.main = page;
  await page.goto(base + "/", { waitUntil: "load" });
  await page.locator(".avatar-btn").click();
  await page.getByRole("link", { name: "This computer" }).click();
  await page.waitForURL(/\/desktop\/identity/);
  eq(await page.locator("#di-running").getAttribute("data-mode"), "embedded", "the identity page says the local profile is in use");
  ok(!(await sideways(page)), "the identity page scrolls sideways");
  if (shots) await page.screenshot({ path: join(shots, "desktop-identity-1280.png"), fullPage: true });
  const before = (await api(ctx, "GET", "/api/v1/desktop/info")).json;
  await page.getByRole("link", { name: "A model on this computer" }).click();
  await page.waitForURL(/\/desktop\/local-model/);
  await page.reload({ waitUntil: "load" });
  const opened = (await api(ctx, "GET", "/api/v1/desktop/info")).json;
  eq(opened.outbound_connections, before.outbound_connections, "opening the page asked something of the network");
  eq(await page.locator("#lm-result").count(), 0, "nothing is shown as found before the button is pressed");
  await Promise.all([page.waitForURL(/\/desktop\/local-model/), page.getByRole("button", { name: "Look for Ollama on this computer" }).click()]);
  await page.waitForSelector("#lm-result");
  const after = (await api(ctx, "GET", "/api/v1/desktop/info")).json;
  eq(after.outbound_connections, before.outbound_connections + 1, "the button makes exactly one request");
  eq(after.outbound_elsewhere, 0, "and it stays on this computer");
  const result = await page.locator("#lm-result").innerText();
  ok(/No model server answered|Ollama is running/.test(result), "the result says what was found: " + result.slice(0, 120));
  if (shots) { await page.screenshot({ path: join(shots, "desktop-local-model-1280.png"), fullPage: true }); kit.main = null; } // a full-page shot leaves style attributes behind
  for (const w of [390]) {
    await page.setViewportSize({ width: w, height: 844 });
    for (const p of ["/desktop/identity", "/desktop/local-model"]) {
      await page.goto(base + p, { waitUntil: "load" });
      ok(!(await sideways(page)), `${p} scrolls sideways at ${w} px`);
      if (shots) await page.screenshot({ path: join(shots, `desktop${p.replace(/\//g, "-").replace("-desktop", "")}-${w}.png`), fullPage: true });
    }
    await page.setViewportSize({ width: 1280, height: 800 });
  }
});

await kit.test("signing out shows the sign-in page, and signing in again needs no form", async () => {
  kit.main = page;
  await page.goto(base + "/", { waitUntil: "load" });
  await page.locator(".avatar-btn").click();
  await page.getByRole("button", { name: "Sign out" }).click();
  await page.getByRole("link", { name: "Sign in" }).waitFor({ timeout: 10000 }); // the sign-in page is shown: signing out is not undone at once
  eq(await page.locator("body.signed-in").count(), 0, "signed out");
  await page.reload({ waitUntil: "load" });
  eq(await page.locator("body.signed-in").count(), 0, "still signed out after a reload");
  await page.getByRole("link", { name: "Sign in" }).click();
  await page.locator("body.signed-in").waitFor({ timeout: 10000 }); // signed in again, straight to the library
  eq(new URL(page.url()).pathname, "/", "back on the library");
  ok((await page.locator("main").innerText()).includes("The Salt Road"), "the same person: their project is there");
});

await kit.test("with the service worker active, signing in still works (the worker leaves the sign-in's navigations alone)", async () => {
  // Every other context here blocks service workers; the desktop window does not. Seen in WKWebView: once the worker was
  // active the issuer refused the window, because the worker re-sent the navigation and WebKit described it differently.
  const sctx = await kit.newContext({ serviceWorkers: "allow", viewport: { width: 1280, height: 800 }, storageState: await ctx.storageState() });
  const sp = await sctx.newPage();
  kit.watch(sp, "worker");
  await sp.goto(base + "/", { waitUntil: "load" });
  await kit.waitFor(() => sp.evaluate(async () => { const r = await navigator.serviceWorker.ready; return r.active && r.active.state === "activated"; }), "the service worker to be active", 15000);
  await sp.reload({ waitUntil: "load" });
  ok(await sp.evaluate(() => !!navigator.serviceWorker.controller), "the page is controlled by the worker");
  const src = await (await sctx.request.get(base + "/sw.js")).text();
  ok(/\(auth\|desktop\|desktop-idp\)/.test(src), "the worker's source names the paths it leaves alone");
  // The session ends (only in this window: the cookie is dropped, the session is not revoked). A page that needs sign-in
  // goes to /auth/login, to the issuer and back, every hop a navigation of a page the worker controls.
  const before = refusals("cross-site");
  await sctx.clearCookies({ name: "scriobh_session" });
  await sp.goto(base + "/settings", { waitUntil: "load" });
  await sp.locator("body.signed-in").waitFor({ timeout: 10000 });
  eq(new URL(sp.url()).pathname, "/settings", "the page asked for is reached, signed in again");
  eq(refusals("cross-site"), before, "the issuer refused the window's own sign-in");
  await sctx.close();
});

await kit.test("a restart keeps the port, so a draft this browser held is still offered", async () => {
  kit.main = page;
  await page.goto(`${base}/p/${slug}/d/${doc}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose", { timeout: 15000 });
  const saved = (await api(ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
  // what a window that was closed in the middle of a save leaves behind
  await page.evaluate(([id, md, rev]) => localStorage.setItem(`scriobh:draft:${document.querySelector('meta[name="scriobh-user"]').content}:${id}`, JSON.stringify({ md, title: "Chapter 1", rev, t: Date.now() })),
    [doc, saved.content.replace(/\n$/, "") + " Typed and never saved.\n", saved.revision]);
  await page.goto("about:blank");
  await kit.srv.stop();
  const token2 = launchToken();
  kit.srv = await startServer(kit.bin, kit.dataDir, { ...desktopEnv(token2), SCRIOBH_SECRET_KEY: "e2e-desktop-secret-key-0123456789abcdef" });
  eq(kit.srv.base, base, "the next launch listens on the port of the last (the origin, and so the browser's storage, is the same)");
  await page.goto(`${base}/desktop/start?t=${token2}`, { waitUntil: "load" });
  ok(await page.locator("body.signed-in").count(), "the next launch is signed in");
  await page.goto(`${base}/p/${slug}/d/${doc}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose", { timeout: 15000 });
  ok(!(await page.locator(".scriobh-prose").innerText()).includes("never saved"), "the draft is offered, not applied by itself");
  const restore = page.getByRole("button", { name: "Restore your unsaved text" });
  await restore.waitFor({ timeout: 10000 });
  await restore.click();
  await kit.waitFor(async () => (await page.locator(".scriobh-prose").innerText()).includes("Typed and never saved."), "the draft to be restored");
  await kit.waitFor(async () => ((await api(ctx, "GET", `/api/v1/docs/${doc}`)).json.item.content || "").includes("Typed and never saved."), "the restored text to be saved", 15000);
});

await kit.test("nothing left the machine: no request to another origin, no outbound connection elsewhere", async () => {
  const info = (await api(ctx, "GET", "/api/v1/desktop/info")).json;
  eq(info.mode, "embedded", "mode");
  eq(info.outbound_elsewhere, 0, "connections the server opened to anywhere but this computer");
  // (That the count is live was shown above: the look for a model raised it by exactly one, and this process has not signed anyone in since it started.)
  ok(!kit.srv.log.includes(token), "the launch token is in the server's log");
  ok(!/desktop\/start\?/.test(kit.srv.log), "the start address's query is in the log");
  ok(!kit.findings.some((f) => /off-origin/.test(f)), "the browser asked another origin: " + kit.findings.filter((f) => /off-origin/.test(f)).join("; "));
});

await kit.test("xx-rtl: the profile page mirrors on a development build in desktop mode", async () => {
  const devBin = build(kit.tmp, "", "scriobh-dev");
  const t2 = launchToken();
  const dev = await startServer(devBin, join(kit.tmp, "data-rtl"), desktopEnv(t2));
  kit.origins.push(new URL(dev.base).origin);
  try {
    const rctx = await kit.newContext({ viewport: { width: 390, height: 844 }, locale: "xx-rtl" });
    const rp = await rctx.newPage();
    kit.watch(rp, "rtl");
    await rp.goto(`${dev.base}/desktop/start?t=${t2}`, { waitUntil: "load" });
    eq(new URL(rp.url()).pathname, "/desktop/profile", "the first run on the development build");
    eq(await rp.locator("html").getAttribute("dir"), "rtl", "the page is right to left");
    eq(await rp.locator("html").getAttribute("lang"), "xx-rtl", "in the pseudo-locale");
    const label = await rp.locator('label[for="dp-name"]').innerText();
    ok(label !== "Your name" && label.length > 0, "the form's words come from the catalogue: " + label);
    const lb = await rp.locator('label[for="dp-name"]').boundingBox(), ib = await rp.locator("#dp-name").boundingBox();
    ok(Math.abs(lb.x + lb.width - (ib.x + ib.width)) < 3, "the label sits at the field's right edge: " + JSON.stringify([lb, ib]));
    eq(await rp.locator("#dp-email").getAttribute("dir"), "ltr", "an address is typed left to right");
    ok(!(await sideways(rp)), "the right-to-left profile page scrolls sideways at 390 px");
    if (shots) await rp.screenshot({ path: join(shots, "desktop-profile-rtl-390.png"), fullPage: true });
    await rp.fill("#dp-name", "كاتبة");
    await Promise.all([rp.waitForURL(dev.base + "/"), rp.keyboard.press("Enter")]);
    ok(await rp.locator("body.signed-in").count(), "signed in on the development build's embedded issuer");
    await rctx.close();
  } finally { await dev.stop(); }
});

await new Promise((r) => evil.close(r));
await kit.finish("e2e-desktop");
