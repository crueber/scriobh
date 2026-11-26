#!/usr/bin/env node
// CSP smoke: build the development server, sign in through the stub issuer in
// a real Chromium, walk the main pages, and fail on anything the content
// security policy and the privacy rules forbid:
//
//   - a console error or warning, or an uncaught page error
//   - a securitypolicyviolation event
//   - any request to an origin other than the app's (zero outbound requests)
//   - a style="" attribute, an on*="" attribute, an inline <script>, or an
//     inline <style>, in the served HTML and in the live DOM
//   - a non-2xx response for an expected page or any of its resources
//
// It finds elements by text and role, never by class name, so pages can be
// restyled underneath it.
//
// Usage:  node csp-smoke.mjs [--probe] [--keep] [--base URL]
//   --probe      exit 0 when a Chromium is available, 1 otherwise; runs nothing
//   --keep       leave the temp data directory in place and print its path
//   --base URL   test an already-running development server instead of
//                building and starting one (it must have SCRIOBH_OIDC_STUB=1)
// Environment:
//   CSP_SMOKE_CHROMIUM   path to a Chromium/Chrome executable
//   CSP_SMOKE_VIEWPORT   e.g. 390x844 (default 1280x900)

import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, rmSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createInterface } from "node:readline";
import { writeFileSync } from "node:fs";
import { crc32 } from "node:zlib";
import { flatPng } from "./covers-pictures.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, "..");
const args = new Set(process.argv.slice(2));
const baseArg = (() => {
  const i = process.argv.indexOf("--base");
  return i > 0 ? process.argv[i + 1] : "";
})();

// ---------------------------------------------------------------- browser

function browserDirs() {
  const dirs = [];
  if (process.env.PLAYWRIGHT_BROWSERS_PATH) dirs.push(process.env.PLAYWRIGHT_BROWSERS_PATH);
  dirs.push(join(homedir(), "Library", "Caches", "ms-playwright"));
  dirs.push(join(homedir(), ".cache", "ms-playwright"));
  return dirs.filter((d) => existsSync(d));
}

// Candidate executables, newest revision first. The full browser is preferred
// over the headless shell: it is what a person would run.
function findChromium() {
  if (process.env.CSP_SMOKE_CHROMIUM) {
    return existsSync(process.env.CSP_SMOKE_CHROMIUM) ? process.env.CSP_SMOKE_CHROMIUM : "";
  }
  const rel = [
    ["chrome-mac-arm64", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"],
    ["chrome-mac-x64", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"],
    ["chrome-linux64", "chrome"],
    ["chrome-linux", "chrome"],
    ["chrome-win64", "chrome.exe"],
  ];
  for (const dir of browserDirs()) {
    const revs = readdirSync(dir)
      .filter((n) => /^chromium-\d+$/.test(n))
      .sort((a, b) => Number(b.split("-")[1]) - Number(a.split("-")[1]));
    for (const r of revs) {
      for (const parts of rel) {
        const p = join(dir, r, ...parts);
        if (existsSync(p)) return p;
      }
    }
  }
  return "";
}

if (args.has("--probe")) {
  const p = findChromium();
  if (p) console.log(p);
  process.exit(p ? 0 : 1);
}

// ----------------------------------------------------------------- server

let server = null;
let tmp = "";
let serverLog = "";

// Stop the server (SIGTERM, then SIGKILL after 5s) and remove the temp dir.
// Safe to call more than once.
let cleaned = false;
async function cleanup() {
  if (cleaned) return;
  cleaned = true;
  if (server && server.exitCode === null && server.signalCode === null) {
    const gone = new Promise((res) => server.once("exit", res));
    server.kill("SIGTERM");
    const timer = setTimeout(() => server.kill("SIGKILL"), 5000);
    await gone;
    clearTimeout(timer);
  }
  if (tmp) {
    if (args.has("--keep")) console.log(`csp-smoke: kept ${tmp}`);
    else rmSync(tmp, { recursive: true, force: true });
  }
}
for (const sig of ["SIGINT", "SIGTERM"]) {
  process.on(sig, async () => {
    await cleanup();
    process.exit(130);
  });
}

async function startServer() {
  tmp = mkdtempSync(join(tmpdir(), "scriobh-csp-"));
  const bin = join(tmp, "scriobh");
  const build = spawnSync("go", ["build", "-o", bin, "./cmd/scriobh"], { cwd: repo, encoding: "utf8" });
  if (build.status !== 0) {
    throw new Error(`go build failed:\n${build.stdout}${build.stderr}`);
  }
  const env = {
    PATH: process.env.PATH,
    HOME: process.env.HOME ?? tmp,
    SCRIOBH_LISTEN: "127.0.0.1:0",
    SCRIOBH_DATA_DIR: join(tmp, "data"),
    SCRIOBH_OIDC_STUB: "1",
  };
  server = spawn(bin, [], { env, stdio: ["ignore", "pipe", "pipe"] });
  server.stderr.on("data", (d) => (serverLog += d));
  const base = await new Promise((res, rej) => {
    const timer = setTimeout(() => rej(new Error(`server did not print LISTEN within 30s\n${serverLog}`)), 30000);
    server.on("exit", (code) => {
      clearTimeout(timer);
      rej(new Error(`server exited early (code ${code})\n${serverLog}`));
    });
    createInterface({ input: server.stdout }).on("line", (line) => {
      const m = /^LISTEN (\S+)/.exec(line);
      if (m) {
        clearTimeout(timer);
        res(m[1]);
      }
    });
  });
  server.removeAllListeners("exit");
  return base;
}

// ---------------------------------------------------------------- checking

const results = [];
let current = null;

function begin(name, expectStatus) {
  current = { name, expectStatus, problems: [], notes: [], status: null, url: "" };
  results.push(current);
}
const problem = (msg) => current && !current.problems.includes(msg) && current.problems.push(msg);

// What a served page must not contain. Applied to the raw HTML and to the live DOM.
function scanHTML(html) {
  const found = [];
  const strip = html.replace(/<!--[\s\S]*?-->/g, "");
  if (/<[a-z][^>]*\sstyle\s*=/i.test(strip)) found.push("style= attribute");
  if (/<[a-z][^>]*\son[a-z]+\s*=/i.test(strip)) found.push("on*= event-handler attribute");
  if (/<script\b(?![^>]*\ssrc\s*=)[^>]*>/i.test(strip)) found.push("inline <script>");
  if (/<style\b/i.test(strip)) found.push("inline <style>");
  if (/javascript:/i.test(strip)) found.push("javascript: URL");
  return found;
}

async function scanDOM(page) {
  return page.evaluate(() => {
    const out = [];
    const q = (sel) => document.querySelectorAll(sel).length;
    if (q("[style]")) out.push(`style= attribute on ${q("[style]")} element(s)`);
    if (q("script:not([src])")) out.push(`${q("script:not([src])")} inline <script> element(s)`);
    if (q("style")) out.push(`${q("style")} <style> element(s)`);
    const handlers = [...document.querySelectorAll("*")].filter((el) =>
      [...el.attributes].some((a) => /^on/i.test(a.name)),
    );
    if (handlers.length) out.push(`on*= attribute on ${handlers.length} element(s)`);
    return out;
  });
}

// Visit a URL as a top-level navigation and check the result.
async function visit(page, base, path, expectStatus, label) {
  begin(label ?? path, expectStatus);
  const resp = await page.goto(base + path, { waitUntil: "networkidle" });
  await settle(page, resp);
}

async function settle(page, resp) {
  current.url = page.url();
  current.status = resp ? resp.status() : null;
  if (resp) {
    if (current.expectStatus === "2xx" ? !(current.status >= 200 && current.status < 300) : current.status !== current.expectStatus) {
      problem(`expected status ${current.expectStatus}, got ${current.status}`);
    }
    const headers = resp.headers();
    const ctype = headers["content-type"] ?? "";
    if (ctype.includes("text/html")) {
      if (!headers["content-security-policy"]) problem("no Content-Security-Policy header on an HTML page");
      for (const f of scanHTML(await resp.text())) problem(`served HTML contains ${f}`);
    }
  }
  for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
  const csp = await page.evaluate(() => window.__cspViolations ?? []);
  for (const v of csp) problem(`securitypolicyviolation: ${v}`);
}

// --------------------------------------------------------------------- main

async function main() {
  let playwright;
  try {
    playwright = await import("playwright");
  } catch {
    throw new Error("playwright is not installed; run `npm ci` in scripts/ first");
  }
  const { chromium } = playwright;

  const base = baseArg || (await startServer());
  const origin = new URL(base).origin;
  console.log(`csp-smoke: testing ${origin}${baseArg ? " (existing server)" : ""}`);

  const exe = findChromium();
  let browser;
  try {
    browser = await chromium.launch(process.env.CSP_SMOKE_CHROMIUM && exe ? { executablePath: exe } : {});
  } catch (e) {
    if (!exe) throw new Error(`no Chromium available (${e.message.split("\n")[0]}); set CSP_SMOKE_CHROMIUM`);
    browser = await chromium.launch({ executablePath: exe });
  }
  console.log(`csp-smoke: browser ${browser.version()}`);

  const [w, h] = (process.env.CSP_SMOKE_VIEWPORT ?? "1280x900").split("x").map(Number);
  const context = await browser.newContext({ viewport: { width: w, height: h }, serviceWorkers: "block" });

  // Every securitypolicyviolation is recorded before any page script runs.
  await context.addInitScript(() => {
    window.__cspViolations = [];
    document.addEventListener("securitypolicyviolation", (e) => {
      window.__cspViolations.push(`${e.violatedDirective} blocked ${e.blockedURI || "inline"} (${e.sourceFile || "page"}:${e.lineNumber || 0})`);
    });
  });

  // Zero outbound requests: record and refuse anything that is not the app.
  const outbound = new Set();
  await context.route(
    (u) => /^(https?|wss?|ftp):/.test(u.protocol) && u.origin !== origin,
    (route) => {
      outbound.add(route.request().url());
      problem(`outbound request to ${route.request().url()}`);
      return route.abort();
    },
  );

  const page = await context.newPage();
  // Pages that follow live updates hold an event stream open, so the network is never idle there. "networkidle" means
  // "loaded, then quiet for a moment, or three seconds at most".
  for (const name of ["goto", "reload", "waitForNavigation"]) {
    const real = page[name].bind(page);
    page[name] = async (...a) => {
      const o = a[a.length - 1];
      const idle = o && typeof o === "object" && o.waitUntil === "networkidle";
      if (idle) a[a.length - 1] = { ...o, waitUntil: "load" };
      const out = await real(...a);
      if (idle) await page.waitForLoadState("networkidle", { timeout: 3000 }).catch(() => {});
      return out;
    };
  }
  const realLoadState = page.waitForLoadState.bind(page);
  page.waitForLoadState = (state, o) => realLoadState(state === "networkidle" ? "load" : state, o).then(() => (state === "networkidle" ? realLoadState("networkidle", { timeout: 3000 }).catch(() => {}) : undefined));
  page.on("console", (m) => {
    if (m.type() === "error" || m.type() === "warning") {
      // This walk blocks service workers; the page's registration is refused and Chromium says so
      // (scripts/e2e-pwa.mjs is the test that runs with the worker).
      if (m.text().includes("Service Worker registration blocked")) return;
      const loc = m.location();
      // Chromium logs "Failed to load resource" for the document itself when a
      // page is deliberately a 404; that is the status the step expects.
      if (
        typeof current?.expectStatus === "number" &&
        current.expectStatus >= 400 &&
        loc?.url === page.url() &&
        m.text().includes(`status of ${current.expectStatus}`)
      ) {
        return;
      }
      problem(`console ${m.type()}: ${m.text()}${loc?.url ? ` (${loc.url}:${loc.lineNumber})` : ""}`);
    }
  });
  page.on("pageerror", (e) => {
    // Chromium 159's built-in PDF viewer, inside the sandboxed attachment frame, reads navigator.serviceWorker from
    // its own injected script and is refused. None of the app's files appears in that stack, so it is not ours.
    if (/Access to service workers is denied/.test(e.message) && !/\/(static|assets?)\//.test(e.stack || "")) return;
    problem(`uncaught page error: ${e.message}`);
  });
  page.on("request", (r) => {
    if (/^(https?|wss?|ftp):/.test(r.url()) && new URL(r.url()).origin !== origin) {
      problem(`outbound request to ${r.url()}`);
    }
  });
  page.on("response", (r) => {
    const u = r.url();
    if (!u.startsWith(origin)) return;
    const s = r.status();
    const isDoc = r.request().resourceType() === "document" && r.request().frame() === page.mainFrame();
    if (!isDoc && (s >= 400 || s < 200)) problem(`resource ${new URL(u).pathname} returned ${s}`);
  });
  page.on("requestfailed", (r) => {
    if (r.failure()?.errorText !== "net::ERR_ABORTED") problem(`request failed: ${r.url()} (${r.failure()?.errorText})`);
  });

  // 1. Signed-out landing.
  await visit(page, origin, "/", 200, "/ (signed out)");

  // 2. Sign in through the development issuer's picker.
  begin("sign-in via the development issuer", "2xx");
  const r = await page.goto(origin + "/auth/login", { waitUntil: "networkidle" });
  await settle(page, r);
  const button = page.getByRole("button", { name: "Sign in as writer@dev.scriobh" });
  if ((await button.count()) === 0) {
    problem("the issuer picker has no button named 'Sign in as writer@dev.scriobh'");
    throw new Error("cannot sign in; aborting the walk\n" + report());
  }
  const nav = page.waitForNavigation({ waitUntil: "networkidle" });
  await button.click();
  const after = await nav;
  results.push((current = { name: "after sign-in (callback destination)", expectStatus: "2xx", problems: [], notes: [], status: null, url: "" }));
  await settle(page, after);
  if (new URL(page.url()).origin !== origin) problem(`ended on ${page.url()}, not the app`);

  // 3. Library, new-project page.
  await visit(page, origin, "/", 200, "/ (signed in, library)");

  // 3b. With the network cut, a submit says so inside the dialog and loses nothing.
  begin("offline submit keeps the form and says so", "none");
  {
    const open = page.getByRole("link", { name: /new project/i }).first();
    await open.click();
    await page.locator("dialog#dialog[open]").waitFor({ timeout: 5000 }).catch(() => problem("the new-project dialog did not open"));
    // htmx opens the dialog on afterSwap, before it has processed the form that
    // was just swapped in. Enter then submits the form natively: the offline
    // error never lands in the dialog and the page navigates away. Process the
    // new form here, so the submit is htmx's whatever the machine's speed.
    await page.evaluate(() => {
      const f = document.querySelector("#new-project-form");
      if (f && window.htmx) window.htmx.process(f);
    });
    const t = page.getByLabel(/title/i).first();
    await t.fill("Typed while offline");
    await context.setOffline(true);
    await page.keyboard.press("Enter");
    const msg = page.locator("dialog [data-dialog-error]");
    await msg.waitFor({ timeout: 5000 }).catch(() => problem("an offline submit showed no message in the dialog"));
    if ((await t.inputValue()) !== "Typed while offline") problem("an offline submit lost the typed text");
    await context.setOffline(false);
    await page.keyboard.press("Escape");
    // The cut request itself is logged by the browser and by htmx; those lines
    // are the expected cause, not a finding. Anything else still fails the step.
    current.problems = current.problems.filter((m) => !/ERR_INTERNET_DISCONNECTED|htmx:(afterRequest|sendError)|request failed: .*\/ui\/projects/.test(m));
  }
  await visit(page, origin, "/ui/projects/new", 200);

  // 4. Create a project through the UI form, as a person would.
  begin("create a project through the form", "2xx");
  const title = `CSP smoke ${Date.now().toString(36)}`;
  const field = page.getByLabel(/title/i).first();
  await field.fill(title);
  const submit = page.getByRole("button", { name: /create|start|save/i }).first();
  const arrive = page.waitForURL(/\/p\/[^/?#]+$/, { timeout: 15000 }).catch(() => null);
  await submit.click();
  const arrived = await arrive;
  await page.waitForLoadState("networkidle");
  current.url = page.url();
  current.status = null;
  if (arrived === null) {
    problem(`did not arrive at /p/<slug>; ended at ${page.url()}`);
  }
  for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
  for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);

  // 5. The project page, fresh.
  const m = /\/p\/([^/?#]+)$/.exec(new URL(page.url()).pathname);
  if (m) {
    await visit(page, origin, `/p/${m[1]}`, 200);
    // On a phone the project crumb is folded away; the title is still in the breadcrumb markup.
    const text = (await page.locator("body").innerText()) + (await page.locator(".crumb").textContent());
    if (!text.includes(title)) problem(`the project page does not show the title "${title}"`);
  } else {
    begin("/p/<slug>", 200);
    problem("no project slug to visit");
  }

  // 5a. The import pages, and settings (which holds the administrators' Backups section).
  await visit(page, origin, "/import", 200);
  await visit(page, origin, "/import/git", 200);
  if (m) await visit(page, origin, `/p/${m[1]}/import`, 200);
  // 5a2. The staged import: a Word file chosen on the chooser, the review page drawn from its draft, and a change made on it (a form
  // that swaps the review body): no inline script, no style attribute, no violation, nothing off-origin.
  {
    begin("import review: choose a file, review it, move a chapter", 200);
    const W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"';
    const chapters = ["One", "Two"].map((c) => `<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>${c}</w:t></w:r></w:p><w:p><w:r><w:t>${(c + " tide came in. ").repeat(20)}</w:t></w:r></w:p>`).join("");
    const files = { "[Content_Types].xml": '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>',
      "word/document.xml": `<?xml version="1.0"?><w:document ${W}><w:body>${chapters}</w:body></w:document>`,
      "word/styles.xml": `<?xml version="1.0"?><w:styles ${W}><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>` };
    const parts = [], central = [];
    let off = 0;
    for (const [name, text] of Object.entries(files)) {
      const nb = Buffer.from(name), d = Buffer.from(text), c = crc32(d);
      const lh = Buffer.alloc(30); lh.writeUInt32LE(0x04034b50, 0); lh.writeUInt16LE(20, 4); lh.writeUInt32LE(c, 14); lh.writeUInt32LE(d.length, 18); lh.writeUInt32LE(d.length, 22); lh.writeUInt16LE(nb.length, 26);
      parts.push(lh, nb, d);
      const ch = Buffer.alloc(46); ch.writeUInt32LE(0x02014b50, 0); ch.writeUInt16LE(20, 4); ch.writeUInt16LE(20, 6); ch.writeUInt32LE(c, 16); ch.writeUInt32LE(d.length, 20); ch.writeUInt32LE(d.length, 24); ch.writeUInt16LE(nb.length, 28); ch.writeUInt32LE(off, 42);
      central.push(ch, nb);
      off += 30 + nb.length + d.length;
    }
    const cd = Buffer.concat(central), end = Buffer.alloc(22);
    end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(central.length / 2, 8); end.writeUInt16LE(central.length / 2, 10); end.writeUInt32LE(cd.length, 12); end.writeUInt32LE(off, 16);
    const docx = join(tmp, "smoke.docx");
    writeFileSync(docx, Buffer.concat([...parts, cd, end]));
    await page.goto(origin + "/import", { waitUntil: "networkidle" });
    await page.check("#imp-f-docx");
    await page.locator("#imp-docx").setInputFiles(docx);
    await page.waitForURL(/\/import\/d\/imd_/, { timeout: 15000 }).catch(() => problem("the file was not staged: " + page.url()));
    await page.waitForLoadState("networkidle");
    await settle(page, null);
    await page.locator('#imp-review summary[aria-label="Edit One"]').click();
    await page.locator('#imp-review button[data-act="down"]').first().waitFor({ timeout: 10000 }).catch(() => problem("the edit panel did not open"));
    await page.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"), null, { timeout: 5000 });
    await page.locator('#imp-review button[data-act="down"]').first().click();
    await page.waitForFunction(() => document.querySelector("#imp-review .imp-title")?.textContent === "Two", null, { timeout: 10000 }).catch(() => problem("the change was not swapped in"));
    await page.waitForLoadState("networkidle");
    await settle(page, null);
    await page.getByRole("button", { name: "Discard this import" }).click();
    await page.waitForURL(/\/import$/, { timeout: 10000 }).catch(() => problem("discard did not go back to the chooser"));
  }
  await visit(page, origin, "/settings", 200);
  if (m) await visit(page, origin, `/p/${m[1]}`, 200, "project page again, for the writing view");

  // 5b. The writing view: type, see it saved, reload and find it, add a chapter, jump, and a foreign id.
  if (m) {
    begin("writing view: type, save, reload", "none");
    const wait = (fn) => page.waitForFunction(fn, null, { timeout: 10000 });
    const saved = () => wait(() => document.querySelector("#save-state")?.dataset.state === "saved" && !window.scriobh.write.unsaved());
    await page.waitForSelector(".scriobh-prose", { timeout: 10000 }).catch(() => problem("the editor did not mount"));
    await page.locator(".scriobh-prose").click();
    await page.keyboard.type("A line typed by the smoke test.");
    await saved().catch(() => problem("the page never reached Saved"));
    await page.reload({ waitUntil: "networkidle" });
    await page.waitForSelector(".scriobh-prose");
    if (!(await page.locator(".scriobh-prose").innerText()).includes("A line typed by the smoke test.")) problem("the text is gone after a reload");
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);

    begin("writing view: add a chapter, jump list, source mode", "none");
    const first = page.url();
    await page.evaluate(async () => {
      // The mobile drawer and the desktop rail share one tree; add through the same route the buttons use.
      const csrf = document.querySelector('meta[name="csrf-token"]').content;
      const r = await fetch("/ui/projects/" + document.querySelector("#write").dataset.project + "/docs", { method: "POST", headers: { "X-CSRF-Token": csrf, "X-Scriobh-Fetch": "1", "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ kind: "chapter", title: "Second chapter" }) });
      if (r.status !== 201) throw new Error("add chapter: " + r.status);
    });
    await page.goto(first, { waitUntil: "networkidle" });
    await page.waitForSelector(".scriobh-prose");
    await page.locator("#jump-open").click();
    await page.locator("#jump-input").fill("second");
    await page.keyboard.press("Enter");
    await page.waitForURL(/\/d\//, { timeout: 10000 }).catch(() => problem("the jump list did not open the chapter"));
    await page.waitForSelector(".scriobh-prose");
    await page.locator(".scriobh-prose").click();
    await page.keyboard.press("ControlOrMeta+e");
    if (!(await page.locator(".scriobh-source").isVisible())) problem("Source mode did not open");
    await page.keyboard.press("ControlOrMeta+e");
    await page.evaluate(() => document.fonts.ready);
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    await visit(page, origin, `/p/${m[1]}/d/doc_00000000000000000000000000`, 404, "foreign or missing document");
  }

  // 5b. The Git sync page: the empty state of a project with no remote, and its push and force pages refusing quietly.
  if (m) {
    await visit(page, origin, `/p/${m[1]}/git`, 200);
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
  }

  // 5c. The goals page: look, set a daily target, see it saved, look at 90 days.
  if (m) {
    await visit(page, origin, `/p/${m[1]}/goals`, 200);
    begin("goals: set a daily target", "none");
    await page.locator("#g-daily").fill("500");
    await page.getByRole("button", { name: "Save daily target" }).click();
    await page.waitForSelector(".saved", { timeout: 10000 }).catch(() => problem("the goals page did not confirm the target"));
    if (!(await page.locator("body").innerText()).includes("Daily target saved.")) problem("no saved confirmation on the goals page");
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    await visit(page, origin, `/p/${m[1]}/goals?days=90`, 200);
  }

  // 5c0. The publish and connect pages: the static site page, the webhooks page and connect an agent.
  if (m) {
    for (const path of [`/p/${m[1]}/site`, `/p/${m[1]}/webhooks`, "/settings/mcp"]) {
      await visit(page, origin, path, 200);
      for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
      for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    }
  }

  // 5c1. The AI pages: AI is off on a fresh instance, so both pages say so, and the project's page and the account page
  // load their script and make no request to anything but this server.
  if (m) {
    await visit(page, origin, "/settings/ai", 200);
    await page.waitForSelector("#ai-root:not([data-loading])", { timeout: 10000 }).catch(() => problem("the AI settings page did not draw"));
    if (!/Nothing is configured|not configured/i.test(await page.locator("body").innerText())) problem("the AI settings page does not say that nothing is configured");
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    await visit(page, origin, `/p/${m[1]}/ai`, 200);
    await page.waitForSelector("#ai-root:not([data-loading])", { timeout: 10000 }).catch(() => problem("the project's AI page did not draw"));
    if (!/AI is off for this project/i.test(await page.locator("body").innerText())) problem("the project's AI page does not say AI is off");
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
  }

  // 5c2. The Compendium pages: the empty library section, a new Compendium through the dialog's plain form, its
  // overview, the project's Compendium panel and its people page.
  if (m) {
    await visit(page, origin, "/compendiums", 200);
    if (!(await page.locator("body").innerText()).includes("No Compendiums yet")) problem("the empty Compendiums list says nothing");
    begin("compendium: create, overview, panel, people", "none");
    const made = await page.evaluate(async () => {
      const csrf = document.querySelector('meta[name="csrf-token"]').content;
      const r = await fetch("/ui/compendiums", { method: "POST", redirect: "manual", headers: { "X-CSRF-Token": csrf, "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ name: "Smoke Bible", kind: "fiction" }) });
      return r.type === "opaqueredirect" || r.ok || r.status === 303;
    });
    if (!made) problem("creating a Compendium failed");
    await visit(page, origin, "/compendiums", 200);
    await visit(page, origin, "/c/smoke-bible", 200);
    await visit(page, origin, `/p/${m[1]}/compendium`, 200);
    await visit(page, origin, `/p/${m[1]}/people`, 200);
    await visit(page, origin, "/c/no-such-compendium-csp-smoke", 404);
  }

  // 5d. The outline: server-rendered, with its script, no CSP findings.
  if (m) {
    await visit(page, origin, `/p/${m[1]}/outline`, 200);
    if (!(await page.locator(".o-list, .o-empty").count())) problem("the outline page shows neither a list nor its empty state");
  }

  // 5e. The corkboard, the status board and reading mode: server-rendered, each with its script, no CSP findings.
  if (m) {
    await visit(page, origin, `/p/${m[1]}/cards`, 200);
    if (!(await page.locator("li").filter({ has: page.getByRole("link") }).count())) problem("the corkboard shows no card for the seeded chapter");
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    await visit(page, origin, `/p/${m[1]}/cards?status=done`, 200, "corkboard, filtered");
    await visit(page, origin, `/p/${m[1]}/board`, 200);
    if (!(await page.getByRole("heading", { name: /^Draft/ }).count())) problem("the status board shows no Draft column");
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    await visit(page, origin, `/p/${m[1]}/read`, 200, "reading mode (landing, then the first page)");
    if (!(await page.getByRole("link", { name: /back to writing/i }).count())) problem("the reading page has no way back to writing");
    await page.getByRole("button", { name: /contents/i }).click();
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    await visit(page, origin, `/p/${m[1]}/read/doc_00000000000000000000000000`, 404, "reading page of a missing document");
  }

  // 5f. The entity bible: the list, a sheet with the preview open, the merge page with a plan, the fields page, and the project's view.
  if (m) {
    begin("entities: set up a Compendium", "none");
    const made = await page.evaluate(async (slug) => {
      const csrf = document.querySelector('meta[name="csrf-token"]').content;
      const call = async (method, path, body) => {
        const r = await fetch(path, { method, headers: { "X-CSRF-Token": csrf, "Content-Type": "application/json" }, body: JSON.stringify(body) });
        return { status: r.status, json: await r.json().catch(() => null) };
      };
      const c = await call("POST", "/api/v1/compendiums", { name: "Smoke Bible" });
      if (c.status !== 201) throw new Error("compendium: " + c.status);
      const id = c.json.item.id;
      const a = await call("POST", `/api/v1/compendiums/${id}/entities`, { kind: "character", name: "Smoke One", aliases: ["Uno"] });
      const b = await call("POST", `/api/v1/compendiums/${id}/entities`, { kind: "character", name: "Smoke Two" });
      const pr = await call("GET", "/api/v1/projects", undefined);
      const p = pr.json.items.find((x) => x.slug === slug);
      const set = await call("PUT", `/api/v1/projects/${p.id}/compendium`, { compendium_id: id });
      if (a.status !== 201 || b.status !== 201 || set.status >= 300) throw new Error("setup: " + [a.status, b.status, set.status]);
      return { slug: c.json.item.slug, a: a.json.item.id, b: b.json.item.id };
    }, m[1]);
    await visit(page, origin, `/c/${made.slug}/entities`, 200);
    await visit(page, origin, `/c/${made.slug}/entities?q=uno`, 200, "entities: search by alias");
    await visit(page, origin, `/c/${made.slug}/e/${made.a}`, 200);
    begin("entities: preview the notes", "none");
    await page.locator("#es-body").fill("A **bold** line.");
    await page.getByRole("button", { name: "Preview" }).click();
    await page.waitForSelector("#es-prev strong", { timeout: 10000 }).catch(() => problem("the preview did not appear"));
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    await visit(page, origin, `/c/${made.slug}/e/${made.a}/merge?into=${made.b}`, 200, "entities: merge preview");
    await visit(page, origin, `/c/${made.slug}/fields`, 200);
    await visit(page, origin, `/p/${m[1]}/entities`, 200);
    await visit(page, origin, `/p/${m[1]}/e/${made.a}`, 200);
    await visit(page, origin, `/c/${made.slug}/e/ent_00000000000000000000000000`, 404, "entities: a missing entity");
  }

  // 5g. Research and the scratchpad, for the project and for a Compendium: every page and its script.
  if (m) {
    const owners = [{ base: `/p/${m[1]}`, name: "project" }];
    const made = await page.evaluate(async () => {
      const r = await fetch("/api/v1/compendiums", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify({ name: "Research Smoke Bible" }) });
      const j = await r.json().catch(() => null);
      return j && j.item ? j.item.slug : "";
    });
    if (made) owners.push({ base: `/c/${made}`, name: "Compendium" });
    for (const o of owners) {
      await visit(page, origin, `${o.base}/research`, 200, `${o.name} research (empty)`);
      begin(`${o.name} research: add a clipping, tag it, open it`, "none");
      await page.locator("#r-add-kind").selectOption("clipping");
      await page.locator("#r-add-title").fill("A diary");
      await page.getByRole("button", { name: "Add", exact: true }).click();
      await page.waitForURL(/\/research\/[^/?]+/, { timeout: 10000 }).catch(() => problem("a new clipping did not open"));
      await page.waitForSelector("#pad-state", { timeout: 10000 }).catch(() => problem("the note page has no save state"));
      await page.locator("#n-url").fill("https://example.org/diary");
      await page.locator("#n-tags").fill("harbour");
      await page.locator("#n-body").fill("A line **kept** by the smoke test.");
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForSelector(".saved", { timeout: 10000 }).catch(() => problem("the note did not confirm the save"));
      await page.getByRole("button", { name: "Preview" }).click();
      await page.waitForSelector("#n-preview strong", { timeout: 5000 }).catch(() => problem("the preview did not render the Markdown"));
      await page.getByRole("button", { name: "Open the source" }).click();
      if (!(await page.locator("#n-warn").isVisible())) problem("the click-through warning did not show");
      await page.getByRole("button", { name: "Cancel" }).click();
      for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
      for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
      await visit(page, origin, `${o.base}/research`, 200, `${o.name} research (with a clipping)`);
      await visit(page, origin, `${o.base}/research/board`, 200, `${o.name} research board`);
      await visit(page, origin, `${o.base}/research/board?by=tag`, 200, `${o.name} research board by tag`);
      await visit(page, origin, `${o.base}/research/tags`, 200, `${o.name} research tags`);
      await visit(page, origin, `${o.base}/research?q=kept`, 200, `${o.name} research search`);
      await visit(page, origin, `${o.base}/scratch`, 200, `${o.name} scratchpad`);
      begin(`${o.name} scratchpad: type and see it saved`, "none");
      await page.locator("#sc-content").fill("A private line.");
      await page.waitForFunction(() => /^Saved at/.test(document.querySelector("#pad-state")?.textContent ?? ""), null, { timeout: 10000 }).catch(() => problem("the scratchpad never said Saved"));
    }
  }

  // 5h. The attachment pages: the empty list, then one image and one PDF uploaded through the API, the list and file
  // pages, and the PDF panel (the one place a frame is allowed, by frame-src 'self' on these pages alone).
  if (m) {
    await visit(page, origin, `/p/${m[1]}/files`, 200);
    if (!(await page.locator("#att-drop").count())) problem("the attachments page has no drop zone for its owner");
    begin("attachments: upload an image and a PDF, list them", "none");
    const up = await page.evaluate(async (slug) => {
      const pid = (await (await fetch(`/api/v1/projects`)).json()).items.find((p) => p.slug === slug).id;
      const png = Uint8Array.from(atob("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="), (c) => c.charCodeAt(0));
      const pdf = new TextEncoder().encode("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n");
      const send = async (name, body, type) => (await (await fetch(`/api/v1/projects/${pid}/attachments?filename=${name}`, { method: "POST", headers: { "Content-Type": type }, body })).json()).item;
      return { img: await send("smoke.png", png, "image/png"), pdf: await send("smoke.pdf", pdf, "application/pdf") };
    }, m[1]);
    if (!up.img || !up.pdf) problem("the upload through the API failed");
    else {
      await visit(page, origin, `/p/${m[1]}/files`, 200, "attachments list with files");
      if ((await page.locator(".att-card").count()) !== 2) problem("the list does not show both files");
      await visit(page, origin, `/p/${m[1]}/files/${up.img.id}`, 200, "attachment page (image)");
      await visit(page, origin, `/p/${m[1]}/files/${up.pdf.id}`, 200, "attachment page (PDF) and its panel");
      await page.locator("#att-preview").click();
      await page.waitForSelector("#att-frame iframe", { timeout: 5000 }).catch(() => problem("the PDF panel did not open"));
      await page.waitForTimeout(400);
      for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
      for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    }
  }

  // 5h2. The cover page: empty, a picture chosen (the form answers with the draft and its SVG preview), page mode's live
  // preview, saved, the saved page, the tree's Cover row on the writing view and the library's tile with the picture.
  if (m) {
    await visit(page, origin, `/p/${m[1]}/cover`, 200, "cover page: empty");
    begin("cover page: choose a picture, change the page mode, save", "none");
    const png = flatPng(300, 450, () => [200, 120, 60]); // a picture the page can place: a one-pixel one is under a pixel at 60 percent, which the preview and Save refuse
    await page.locator("#cv-shape-sides").check();
    await page.locator("#cv-front-mode input[value=image]").check();
    const nav = page.waitForNavigation();
    await page.locator("#cv-front-file").setInputFiles({ name: "smoke.png", mimeType: "image/png", buffer: png });
    await nav;
    if (!(await page.locator("#cv-pv-page .cv-svg image").count())) problem("the draft is not drawn on its page");
    await page.locator("#cv-front-mode input[value=page]").check();
    await page.locator("#cv-front-scale").fill("60");
    if (!(await page.locator("#cv-pv-page .cv-svg clipPath").count())) problem("the live preview did not draw the page");
    for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
    for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
    await page.locator("#cv-alt").fill("A flat brown picture");
    const nav2 = page.waitForNavigation();
    await page.getByRole("button", { name: "Save the cover" }).click();
    await nav2;
    await visit(page, origin, `/p/${m[1]}/cover`, 200, "cover page: saved");
    await visit(page, origin, `/p/${m[1]}`, 200, "writing view with the tree's Cover row");
    if (!(await page.locator("#tree-cover img").count())) problem("the tree's Cover row has no thumbnail");
    await visit(page, origin, `/`, 200, "library with a cover picture");
    if (!(await page.locator(".tile .cover-image img").count())) problem("the library tile has no picture");
  }

  // 5i. The timeline pages: a Compendium with two events, its page, the event form, the check in place, a book's two orders.
  if (m) {
    begin("timeline: seed a Compendium with two events through the API", "none");
    const seeded = await page.evaluate(async (slug) => {
      const csrf = document.querySelector('meta[name="csrf-token"]').content;
      const call = async (method, path, body) => {
        const r = await fetch(path, { method, headers: { "X-CSRF-Token": csrf, "Content-Type": "application/json" }, body: body ? JSON.stringify(body) : undefined });
        return { status: r.status, json: await r.json().catch(() => null) };
      };
      const c = await call("POST", "/api/v1/compendiums", { name: "Smoke Bible" });
      if (c.status !== 201) return "compendium " + c.status;
      const cid = c.json.item.id;
      const list = await call("GET", "/api/v1/projects");
      const proj = list.json.items.find((p) => p.slug === slug);
      const put = await call("PUT", "/api/v1/projects/" + proj.id + "/compendium", { compendium_id: cid });
      if (put.status !== 200) return "set compendium " + put.status;
      const ent = await call("POST", "/api/v1/compendiums/" + cid + "/entities", { kind: "character", name: "Ana" });
      if (ent.status !== 201) return "entity " + ent.status;
      for (const [t, d] of [["First event", "1998-04"], ["Second event", "1999"]]) {
        const e = await call("POST", "/api/v1/compendiums/" + cid + "/timeline/events", { title: t, story_date: d, participants: [{ entity: "Ana", role: "there" }] });
        if (e.status !== 201) return "event " + e.status;
      }
      return c.json.item.slug;
    }, m[1]);
    if (!/^[a-z0-9-]+$/.test(String(seeded))) problem("could not seed a timeline: " + seeded);
    else {
      await visit(page, origin, `/c/${seeded}/timeline`, 200);
      if (!(await page.locator("#tl-events tbody tr").count())) problem("the Compendium timeline lists no events");
      await visit(page, origin, `/c/${seeded}/timeline/events/new`, 200);
      await page.getByLabel("Starts").fill("1998-02-30");
      await page.getByLabel("Starts").blur();
      if (!/February has 28 days/.test(await page.locator("#ev-start-h").innerText())) problem("the live date hint did not say what is wrong");
      await page.locator("[name=person_name]").first().pressSequentially("An");
      await page.locator("ul[role=listbox]:not([hidden])").first().waitFor({ timeout: 5000 }).catch(() => problem("the entity picker showed no matches"));
      for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
      for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
      await visit(page, origin, `/c/${seeded}/timeline`, 200, "timeline: the check, loaded in place");
      await page.getByRole("link", { name: "Check timeline" }).click();
      await page.locator("#tl-check-body .tl-read").waitFor({ timeout: 5000 }).catch(() => problem("the check panel did not load"));
      for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
      for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
      await visit(page, origin, `/p/${m[1]}/timeline`, 200);
      await visit(page, origin, `/p/${m[1]}/timeline?order=narrative&check=1`, 200);
      if (!(await page.locator(".tl-mf").count())) problem("the book's own order has no move buttons for an editor");
      await visit(page, origin, `/c/${seeded}/timeline?from=1998-4`, 422, "timeline: a refused filter date is a plain 422 page");
    }
  }

  // 5j. Review and sharing: the review page, the reading view with its margin and a comment, the writing view's
  // Comments tab, the share links page, and the public page of a link (and the one page for a link that opens nothing).
  if (m) {
    begin("review: seed a comment, a suggestion and a share link through the API", "none");
    const seeded = await page.evaluate(async (slug) => {
      const csrf = document.querySelector('meta[name="csrf-token"]').content;
      const call = async (method, path, body) => {
        const r = await fetch(path, { method, headers: { "X-CSRF-Token": csrf, "Content-Type": "application/json" }, body: body ? JSON.stringify(body) : undefined });
        return { status: r.status, json: await r.json().catch(() => null) };
      };
      const list = await call("GET", "/api/v1/projects");
      const proj = list.json.items.find((p) => p.slug === slug);
      const docs = await call("GET", `/api/v1/projects/${proj.id}/docs`);
      const id = docs.json.order[0];
      const d = (await call("GET", `/api/v1/docs/${id}`)).json.item;
      const text = "The tide came in over the stones, and nobody on the quay counted the boats.\n";
      const up = await call("PATCH", `/api/v1/docs/${id}`, { base_revision: d.revision, content: text });
      if (up.status !== 200) return "text " + up.status;
      const c = await call("POST", `/api/v1/projects/${proj.id}/comments`, { document_id: id, quote: "tide came in", plain: true, body: "Smoke comment." });
      if (c.status !== 201) return "comment " + c.status;
      const s = await call("POST", `/api/v1/projects/${proj.id}/suggestions`, { document_id: id, base_revision: up.json.item.revision, plain: true, changes: [{ quote: "nobody on the quay", text: "no one on the quay" }] });
      if (s.status !== 201) return "suggestion " + s.status;
      const sh = await call("POST", `/api/v1/projects/${proj.id}/shares`, { label: "smoke", scope: "comment" });
      if (sh.status !== 201) return "share " + sh.status;
      return JSON.stringify({ doc: id, path: sh.json.path });
    }, m[1]);
    let info = null;
    try { info = JSON.parse(seeded); } catch { problem("could not seed review: " + seeded); }
    if (info) {
      await visit(page, origin, `/p/${m[1]}/review`, 200);
      await visit(page, origin, `/p/${m[1]}/read/${info.doc}`, 200, "reading view with the comment margin");
      await page.waitForSelector("mark.rv-mark", { timeout: 10000 }).catch(() => problem("the comment is not marked on the page"));
      await page.waitForSelector("del.rv-del", { timeout: 10000 }).catch(() => problem("the suggestion is not marked on the page"));
      // on a phone the margin is a sheet that starts closed: the Comments button opens it
      if (await page.locator("#rv-margin").evaluate((e) => getComputedStyle(e).visibility === "hidden")) await page.locator("#rv-toggle").click();
      await page.getByRole("button", { name: /^Suggestions/ }).click();
      for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
      for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
      await visit(page, origin, `/p/${m[1]}/d/${info.doc}`, 200, "writing view, to open the Comments tab");
      await page.evaluate(() => document.querySelector("#insp-open").click());
      await page.locator("#itab-comments").click();
      await page.waitForSelector("#ipanel-comments .rv-card", { timeout: 10000 }).catch(() => problem("the Comments tab shows no thread"));
      for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
      for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
      await visit(page, origin, `/p/${m[1]}/compile`, 200, "the Compile page");
      await page.getByRole("button", { name: "Preview", exact: true }).click();
      await page.waitForSelector("#cmp-paper .cmp-page", { timeout: 10000 }).catch(() => problem("the compile preview drew no page"));
      for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
      for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
      await visit(page, origin, "/admin/users", 404, "the console, to a person who is not an administrator");
      await visit(page, origin, `/p/${m[1]}/share`, 200);
      await visit(page, origin, info.path, 200, "the page of a share link");
      await page.waitForSelector("#sh-main:not([hidden])", { timeout: 10000 }).catch(() => problem("the share page did not open the text"));
      await page.waitForSelector("section.chapter", { timeout: 10000 }).catch(() => problem("the share page shows no chapter"));
      for (const f of await scanDOM(page)) problem(`live DOM has ${f}`);
      for (const v of await page.evaluate(() => window.__cspViolations ?? [])) problem(`securitypolicyviolation: ${v}`);
      await visit(page, origin, "/s/shl_00000000000000000000000000.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 404, "a share link that opens nothing");
    }
  }

  // 6. Not found.
  await visit(page, origin, "/no-such-page-csp-smoke", 404);
  await visit(page, origin, "/p/no-such-project-csp-smoke", 404);

  // 7. Sign out, and the signed-out landing again.
  begin("sign out", "2xx");
  // Sign out lives in the account menu (a native popover); open it first.
  const menu = page.getByRole("button", { name: /^account/i }).first();
  if ((await menu.count()) > 0) await menu.click();
  const out = page.getByRole("button", { name: /sign out/i }).first();
  if ((await out.count()) === 0) {
    problem("no 'Sign out' button on the page");
  } else {
    const n = page.waitForNavigation({ waitUntil: "networkidle" });
    await out.click();
    await settle(page, await n);
  }
  await visit(page, origin, "/", 200, "/ (signed out, after sign-out)");

  await browser.close();
  if (outbound.size) console.log(`csp-smoke: ${outbound.size} outbound request(s) were refused`);
}

function report() {
  let bad = 0;
  const lines = [];
  for (const r of results) {
    const ok = r.problems.length === 0;
    if (!ok) bad++;
    lines.push(`${ok ? "PASS" : "FAIL"}  ${r.name}${r.status ? `  [${r.status}]` : ""}`);
    for (const p of r.problems) lines.push(`        - ${p}`);
  }
  lines.push("", bad ? `csp-smoke: ${bad} of ${results.length} checks FAILED` : `csp-smoke: all ${results.length} checks passed`);
  return lines.join("\n");
}

let failed = false;
try {
  await main();
} catch (e) {
  failed = true;
  console.error(`csp-smoke: ${e.message}`);
  if (serverLog) console.error(`--- server log ---\n${serverLog.split("\n").slice(-15).join("\n")}`);
}
await cleanup();
console.log(report());
process.exit(failed || results.some((r) => r.problems.length) ? 1 : 0);
