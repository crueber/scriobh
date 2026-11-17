#!/usr/bin/env node
// End-to-end test of the installable shell in a real Chromium: the service
// worker registers and takes control, keeps only the shell, serves it with the
// network cut and shows the offline notice instead of a browser error page, and
// never lets one person's pages reach another on the same browser profile.
//
//   node scripts/e2e-pwa.mjs
//
// About 20 seconds. The network is cut two ways: the context's offline switch,
// and by stopping the server (restarted on the same port afterwards).
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { signIn, startServer } from "./harness.mjs";

const kit = await createKit("e2e-pwa");
const { api, waitFor } = kit;
const base = () => kit.base();
const A = "writer@dev.scriobh", A_NAME = "Wren Writer";
const B = "reader@dev.scriobh", B_NAME = "Rory Reader";
const SECRET = "The Cormorant Ledger";

// A service worker needs to be allowed; the shared kit blocks them for every other script.
const ctx = await kit.newContext({ serviceWorkers: "allow", viewport: { width: 1000, height: 700 } });
const pg = await ctx.newPage();
kit.watch(pg, "main");
kit.main = pg;
// Cutting the network makes the browser log failed loads; those are the point.
const cut = [/ERR_INTERNET_DISCONNECTED/, /ERR_CONNECTION_REFUSED/, /Failed to load resource/, /net::ERR/];

const cacheDump = () => pg.evaluate(async () => {
  const out = [];
  for (const name of await caches.keys()) {
    const c = await caches.open(name);
    for (const req of await c.keys()) {
      const res = await c.match(req);
      const ct = res.headers.get("content-type") || "";
      const u = new URL(req.url);
      out.push({ cache: name, path: u.pathname, search: u.search, ct, status: res.status, text: /text|json|javascript|svg|xml/.test(ct) ? await res.clone().text() : "" });
    }
  }
  return out;
});
const controlled = () => waitFor(() => pg.evaluate(async () => {
  const r = await navigator.serviceWorker.ready;
  return r.active && r.active.state === "activated" && !!navigator.serviceWorker.controller;
}), "the worker to activate and take control", 20000);

let project, csrf;

await kit.test("the worker registers at the root, takes control and keeps only the shell", async () => {
  await signIn(pg, base(), A);
  const made = await api(ctx, "POST", "/api/v1/projects", { title: SECRET, kind: "novel" });
  ok(made.json && made.json.item, "creating the project: " + made.status);
  project = made.json.item;
  await pg.goto(`${base()}/`, { waitUntil: "load" });
  csrf = await pg.evaluate(() => (document.querySelector('meta[name="csrf-token"]') || {}).content);
  ok(csrf && csrf.length > 8, "no CSRF token on the signed-in page");
  await controlled();
  const reg = await pg.evaluate(async () => { const r = await navigator.serviceWorker.getRegistration(); return { scope: r.scope, script: r.active.scriptURL }; });
  eq(reg.scope, base() + "/", "the worker's scope is the whole origin");
  eq(reg.script, base() + "/sw.js", "the worker is served from the root");
  // Visit what a person visits, signed in, so there is something a bad cache could have kept.
  await pg.goto(`${base()}/p/${project.slug}`, { waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  await pg.goto(`${base()}/settings`, { waitUntil: "load" });
  await pg.goto(`${base()}/`, { waitUntil: "load" });

  const dump = await cacheDump();
  ok(dump.length > 15, `only ${dump.length} entries are kept`);
  const names = new Set(dump.map((d) => d.cache));
  eq(names.size, 1, "one cache");
  ok([...names][0].startsWith("scriobh-static-"), "the cache name: " + [...names]);
  for (const d of dump) {
    ok(d.path.startsWith("/static/") || d.path === "/offline", `the cache holds ${d.path}, which is neither a static file nor the offline page`);
    ok(d.status === 200, `${d.path} was stored with status ${d.status}`);
    for (const secret of [A_NAME, A, SECRET, csrf]) {
      ok(!d.text.includes(secret), `${d.path} holds "${secret}"`);
    }
  }
  ok(dump.some((d) => d.path === "/offline"), "the offline page is kept");
  ok(dump.some((d) => /\/static\/css\/app\.[0-9a-f]{8}\.css$/.test(d.path)), "the stylesheet is kept");
  for (const f of ["editor", "doc", "format", "alts", "altpanel", "stash", "inspector"]) ok(dump.some((d) => new RegExp("/static/js/" + f + "\\.[0-9a-f]{8}\\.js$").test(d.path)), `the writing view's ${f}.js is kept for offline`);
  ok(dump.some((d) => d.path === "/static/fonts/newsreader.woff2"), "the font is kept under the name the stylesheet uses");
  // Whatever a page is, the cache has no answer for it.
  for (const p of ["/", `/p/${project.slug}`, "/settings", "/api/v1/projects", `/api/v1/docs/${project.id}`]) {
    ok(await pg.evaluate(async (u) => (await caches.match(u)) === undefined, p), `the cache answers ${p}`);
  }
});

await kit.test("the network cut with the context's offline switch: the shell loads and says so", async () => {
  kit.allowed = cut;
  await ctx.setOffline(true);
  try {
    let res;
    try {
      res = await pg.goto(`${base()}/`, { waitUntil: "load", timeout: 15000 });
    } catch (e) {
      throw new Error("the browser's error page came up instead of the offline notice: " + e.message.split("\n")[0]);
    }
    const text = await pg.locator("main").innerText();
    // If the switch did not reach the worker's own requests the library would have loaded here; that is a failure
    // to prove anything, so it fails, and the server-stopped test below is the unambiguous cut.
    ok(/cannot reach the server/.test(text), "the offline notice did not appear: " + text.slice(0, 80));
    eq(res.status(), 200, "the offline page is a normal answer");
    ok(!text.includes(A_NAME) && !text.includes(SECRET), "the offline page holds a person's text");
    eq(await pg.title(), "Offline · Scríobh", "the offline page title");
  } finally {
    await ctx.setOffline(false);
  }
});

await kit.test("the server stopped: a reload shows the offline notice, styled, from the cache", async () => {
  kit.allowed = cut;
  const port = new URL(base()).port;
  await pg.goto(`${base()}/`, { waitUntil: "load" });
  await controlled();
  await kit.srv.stop();
  // A reload of the page we are on, then a deep link, then the start URL.
  for (const path of ["/", `/p/${project.slug}`, "/settings", "/no/such/page"]) {
    const res = await pg.goto(base() + path, { waitUntil: "load", timeout: 15000 });
    ok(res && res.status() === 200, `${path}: the browser did not get a page (${res && res.status()})`);
    const text = await pg.locator("main").innerText();
    ok(/cannot reach the server/.test(text), `${path}: not the offline notice: ${text.slice(0, 80)}`);
    ok(!text.includes(A_NAME) && !text.includes(SECRET), `${path}: shows a person's text offline`);
  }
  // The shell came from the cache: the stylesheet is applied and a script file answers.
  const shell = await pg.evaluate(async () => {
    const sheet = document.querySelector('link[rel="stylesheet"]');
    const rules = [...document.styleSheets].reduce((n, s) => { try { return n + s.cssRules.length; } catch { return n; } }, 0);
    const js = await fetch(document.querySelector("script[src]").src).then((r) => r.status).catch(() => 0);
    const h1 = getComputedStyle(document.querySelector("h1")).fontFamily;
    const bg = getComputedStyle(document.body).backgroundColor;
    return { hasSheet: !!sheet, rules, js, h1, bg };
  });
  ok(shell.hasSheet && shell.rules > 100, `the stylesheet is not applied offline: ${JSON.stringify(shell)}`);
  eq(shell.js, 200, "a script file offline");
  ok(/Newsreader|serif/.test(shell.h1), "the heading font " + shell.h1);
  // An API call is not answered from anywhere: it fails like any request with no network.
  const api2 = await pg.evaluate(() => fetch("/api/v1/projects", { credentials: "same-origin" }).then((r) => r.status).catch(() => "network error"));
  eq(api2, "network error", "an API call offline");
  // Back online on the same port: the same profile recovers without a reload of the worker.
  kit.srv = await startServer(kit.bin, kit.dataDir, { SCRIOBH_LISTEN: "127.0.0.1:" + port });
  eq(new URL(base()).port, port, "the server came back on the same port");
  await pg.goto(`${base()}/`, { waitUntil: "load" });
  ok((await pg.locator("main").innerText()).includes(SECRET), "online again, the library shows the project");
});

await kit.test("one person's pages never reach the next person on the same browser", async () => {
  // A signs out; B signs in on the same profile, with the worker and its cache still there.
  await pg.goto(`${base()}/`, { waitUntil: "load" });
  await pg.locator(".avatar-btn").click();
  await pg.getByRole("button", { name: "Sign out" }).click();
  await waitFor(async () => (await pg.locator("main").innerText()).includes("Sign in"), "the signed-out page");
  await controlled();
  await signIn(pg, base(), B);
  await pg.goto(`${base()}/`, { waitUntil: "load" });
  const lib = await pg.locator("body").innerText();
  ok(lib.includes(B_NAME) || !lib.includes(A_NAME), "B sees a page that is not A's");
  ok(!lib.includes(A_NAME) && !lib.includes(SECRET), "B's library shows A's name or project");
  const html = await pg.content();
  ok(!html.includes(A) && !html.includes(SECRET) && !html.includes(csrf), "B's page holds something of A's");
  // A's project page is a 404 for B, from the server, not a cached copy.
  kit.allowed = [/status of 404/];
  const res = await pg.goto(`${base()}/p/${project.slug}`, { waitUntil: "load" });
  eq(res.status(), 404, "B opening A's project");
  ok(!(await pg.content()).includes(SECRET), "A's project title on B's 404");
  // And the cache is still only the shell.
  for (const d of await cacheDump()) {
    ok(d.path.startsWith("/static/") || d.path === "/offline", `the cache holds ${d.path}`);
    for (const secret of [A_NAME, B_NAME, A, B, SECRET, csrf]) ok(!d.text.includes(secret), `${d.path} holds "${secret}"`);
  }
  // B's CSRF token is B's: the page carries one, and it is not A's.
  await pg.goto(`${base()}/`, { waitUntil: "load" });
  const csrfB = await pg.evaluate(() => (document.querySelector('meta[name="csrf-token"]') || {}).content);
  ok(csrfB && csrfB !== csrf, "B's CSRF token is A's");
});

await kit.test("a browser that blocks service workers works as before, without a console error", async () => {
  kit.allowed = [];
  const plain = await kit.newContext({ viewport: { width: 1000, height: 700 } }); // serviceWorkers: "block", as every other script
  const p2 = await plain.newPage();
  kit.watch(p2, "blocked");
  await signIn(p2, base(), A);
  await p2.goto(`${base()}/`, { waitUntil: "load" });
  await kit.sleep(500);
  ok((await p2.locator("main").innerText()).includes(SECRET), "the library without a worker");
  await plain.close();
});

await kit.finish("e2e-pwa");
