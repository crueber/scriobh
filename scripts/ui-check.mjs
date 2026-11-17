#!/usr/bin/env node
// Drives the real binary in a real browser: screenshots of every Phase 0
// page in both themes and at 1280x800 and 390x844, plus the accessibility and
// containment checks that CSS alone cannot prove.
//
//   node scripts/ui-check.mjs --out docs/screens/phase-0
//
// Needs playwright-core (a devDependency of scripts/, never a runtime one) and
// a Chromium. Environment:
//   PW_CORE   path or URL of playwright-core's entry file, when it is not installed here
//   CHROME    path to the Chromium executable, when Playwright's own download is absent
//
// Fails on: a console error, a CSP violation, a request to any host other than
// the server, a page that scrolls sideways, a focused control with no visible
// indicator, a dialog that leaks focus or does not give it back.
import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const argv = process.argv.slice(2);
const arg = (n, d) => (argv.includes(n) ? argv[argv.indexOf(n) + 1] : d);
const outDir = resolve(arg("--out", join(tmpdir(), "scriobh-screens")));
const { chromium } = await import(process.env.PW_CORE || "playwright-core");

const work = mkdtempSync(join(tmpdir(), "scriobh-ui-"));
const bin = join(work, "scriobh");
execFileSync("go", ["build", "-o", bin, "./cmd/scriobh"], { cwd: root, stdio: "inherit" });
mkdirSync(outDir, { recursive: true });

const server = spawn(bin, [], {
  env: { PATH: process.env.PATH, SCRIOBH_OIDC_STUB: "1", SCRIOBH_LISTEN: "127.0.0.1:0", SCRIOBH_DATA_DIR: join(work, "data") },
  stdio: ["ignore", "pipe", "ignore"],
});
const base = await new Promise((ok, no) => {
  let buf = "";
  server.stdout.on("data", (d) => {
    buf += d;
    const m = buf.match(/LISTEN (http:\/\/127\.0\.0\.1:\d+)/);
    if (m) ok(m[1]);
  });
  server.on("exit", () => no(new Error("server exited")));
  setTimeout(() => no(new Error("server did not start")), 20000);
});
const origin = new URL(base).origin;

const problems = [];
let step = "start";
const note = (m) => { problems.push(`[${step}] ${m}`); console.error(`PROBLEM [${step}]:`, m.slice(0, 220)); };
const browser = await chromium.launch({ executablePath: process.env.CHROME || undefined });

const projects = [
  { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan", cover_color: "#2F5D50" },
  { title: "Notes on Quiet Software", kind: "essay", author_name: "Máire Nolan", cover_color: "#7A4B2A" },
  { title: "The Winter Letters", kind: "novella", author_name: "Máire Nolan", cover_color: "#3B5B7A" },
  { title: "Field Notes", kind: "collection", cover_color: "#6B3E4F" },
  { title: "A Short History of Tides, Harbours, and the People Who Waited for Them", kind: "nonfiction", author_name: "C. Rueber", cover_color: "#5A4E7A" },
  { title: "Hedgerow", kind: "short", author_name: "Ciara Ní Bhriain", cover_color: "#4B6B3A" },
  { title: "Q3 Report", kind: "report", cover_color: "#8A6420" },
  { title: "x".repeat(200), kind: "blank", cover_color: "#4A4740" },
];

async function newPage(ctx) {
  const page = await ctx.newPage();
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (e) => console.error(`CSP ${e.violatedDirective} blocked ${e.blockedURI} sample=${JSON.stringify(e.sample)} at ${e.sourceFile}:${e.lineNumber}`));
  });
  page.on("console", (m) => { if ((m.type() === "error" || m.type() === "warning") && !m.text().startsWith("Failed to load resource") && !/Service Worker registration blocked/.test(m.text()) && !/Applying inline style/.test(m.text()) && !/^Response Status Error Code 403 from \/ui\/projects/.test(m.text())) note(`console ${m.type()}: ${m.text()} (${page.url()})`); });
  page.on("pageerror", (e) => note(`page error: ${e.message}`));
  page.on("requestfailed", (r) => r.failure()?.errorText !== "net::ERR_ABORTED" && note(`request failed: ${r.url()} ${r.failure()?.errorText}`));
  page.on("request", (r) => { if (!r.url().startsWith("data:") && new URL(r.url()).origin !== origin) note(`off-origin request: ${r.url()}`); });
  page.on("response", (r) => { if (r.status() >= 400 && !r.url().includes("/p/does-not-exist") && !([403, 422].includes(r.status()) && r.url().endsWith("/ui/projects")) && !r.url().includes("/dev-idp/") && !(r.status() === 401 && r.url().endsWith("/ui/projects/new"))) note(`HTTP ${r.status()}: ${r.url()}`); });
  return page;
}

const sideways = async (page, label) => {
  const [sw, cw] = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  if (sw > cw) note(`${label}: scrolls sideways (${sw} > ${cw})`);
};

async function signIn(page) {
  await page.goto(base + "/auth/login");
  await page.getByRole("button", { name: "Sign in as writer@dev.scriobh" }).click();
  await page.waitForURL(base + "/");
}

// Focus indicator: an outline, or the ring box-shadow the swatches use, on the
// focused element or the chip beside it.
const focusVisible = (page) => page.evaluate(() => {
  const el = document.activeElement;
  if (!el || el === document.body) return { ok: false, what: "body" };
  const probe = (e) => { const c = getComputedStyle(e); return (c.outlineStyle !== "none" && parseFloat(c.outlineWidth) >= 2) || (c.boxShadow !== "none" && c.boxShadow.includes("rgb")); };
  const sib = el.nextElementSibling;
  const name = el.tagName.toLowerCase() + (el.id ? "#" + el.id : "") + (el.getAttribute("aria-label") ? `[${el.getAttribute("aria-label")}]` : "") + (el.textContent.trim() ? ` "${el.textContent.trim().slice(0, 24)}"` : "");
  return { ok: probe(el) || (sib && probe(sib)), what: name };
});

async function tabOrder(page, label, max = 40) {
  await page.evaluate(() => { document.activeElement?.blur(); window.scrollTo(0, 0); });
  const seen = [];
  for (let i = 0; i < max; i++) {
    await page.keyboard.press("Tab");
    const f = await focusVisible(page);
    if (f.what === "body") break;
    if (!f.ok) note(`${label}: no visible focus indicator on ${f.what}`);
    if (seen.includes(f.what)) break;
    seen.push(f.what);
  }
  console.log(`tab order ${label}: ${seen.join(" > ")}`);
  return seen;
}

const shot = (page, name) => { step = "shot " + name; return page.screenshot({ path: join(outDir, name + ".png"), animations: "disabled" }); };

const viewports = {
  desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 },
  mobile: { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true },
};

let state; // one sign-in, reused: the login route is rate limited on purpose
try {
  {
    const ctx = await browser.newContext(viewports.desktop);
    await signIn(await newPage(ctx));
    state = await ctx.storageState();
    await ctx.close();
  }
  // Pass 1: the signed-out landing and the empty library, before anything is seeded.
  for (const [vp, vopts] of Object.entries(viewports)) {
    for (const scheme of ["light", "dark"]) {
      const tag = `${vp}-${scheme}`;
      const ctx = await browser.newContext({ ...vopts, colorScheme: scheme, reducedMotion: "reduce" });
      const page = await newPage(ctx);
      await page.goto(base + "/");
      await page.evaluate(() => document.fonts.ready);
      await shot(page, `landing-${tag}`);
      await sideways(page, `landing ${tag}`);
      if (tag === "desktop-light") await tabOrder(page, "landing");
      await ctx.addCookies(state.cookies);
      await page.goto(base + "/");
      await page.evaluate(() => document.fonts.ready);
      await shot(page, `library-empty-${tag}`);
      await sideways(page, `empty library ${tag}`);
      await ctx.close();
    }
  }
  // Seed the library through the real API, as the signed-in browser.
  {
    const ctx = await browser.newContext({ ...viewports.desktop, storageState: state });
    const page = await newPage(ctx);
    await page.goto(base + "/");
    for (const p of projects) {
      const r = await page.evaluate(async (p) => {
        const t = document.querySelector('meta[name="csrf-token"]').content;
        const res = await fetch("/api/v1/projects", { method: "POST", headers: { "Content-Type": "application/json", "X-CSRF-Token": t }, body: JSON.stringify(p) });
        return res.status;
      }, p);
      if (r !== 201) note(`seeding ${p.title.slice(0, 20)} returned ${r}`);
    }
    await ctx.close();
  }
  // Pass 2: everything else, with the library full.
  for (const [vp, vopts] of Object.entries(viewports)) {
    for (const scheme of ["light", "dark"]) {
      const tag = `${vp}-${scheme}`;
      const ctx = await browser.newContext({ ...vopts, colorScheme: scheme, reducedMotion: "reduce", storageState: state });
      const page = await newPage(ctx);
      const fresh = tag === "desktop-light";

      await page.goto(base + "/");
      await page.evaluate(() => document.fonts.ready);
      await shot(page, `library-${tag}`);
      await sideways(page, `library ${tag}`);
      if (fresh) await tabOrder(page, "library");

      // Focus rings on the solid button and on a cover, in this theme.
      if (vp === "desktop") {
        await page.goto(base + "/");
        await page.keyboard.press("Tab");
        await page.getByRole("link", { name: "New project" }).focus();
        await shot(page, `focus-button-${tag}`);
        await page.locator(".tile-link").first().focus();
        await shot(page, `focus-cover-${tag}`);
      }

      // New-project dialog: opens, traps focus, restores it.
      const trigger = page.getByRole("link", { name: "New project" });
      await trigger.focus();
      await trigger.press("Enter");
      await page.locator("dialog#dialog[open]").waitFor();
      const titleFocused = await page.evaluate(() => document.activeElement?.id);
      if (titleFocused !== "np-title") note(`dialog ${tag}: focus landed on ${titleFocused}, not the title field`);
      await shot(page, `dialog-${tag}`);
      if (fresh) {
        const inside = [];
        for (let i = 0; i < 24; i++) {
          await page.keyboard.press("Tab");
          const f = await focusVisible(page);
          inside.push(f.what);
          if (f.what === "body") continue; // past the last control, a modal hands focus to the browser's own UI
          if (!f.ok) note(`dialog: no visible focus indicator on ${f.what}`);
          const within = await page.evaluate(() => !!document.activeElement?.closest("dialog") || document.activeElement === document.body);
          if (!within) note(`dialog: focus escaped to ${f.what}`);
        }
        console.log("dialog tab loop:", [...new Set(inside)].join(" > "));
        // A rejected submit re-renders the form and must keep focus inside it.
        await page.fill("#np-title", "   ");
        await page.evaluate(() => { document.querySelector("#np-title").removeAttribute("required"); });
        await page.keyboard.press("Enter");
        await page.locator(".field-error").first().waitFor();
        const after = await page.evaluate(() => document.activeElement?.id);
        if (after !== "np-title") note(`dialog 422: focus is on ${after}, expected np-title`);
        await shot(page, `dialog-error-${tag}`);
      }
      await page.keyboard.press("Escape");
      await page.locator("dialog#dialog[open]").waitFor({ state: "detached" }).catch(() => {});
      await page.waitForFunction(() => document.activeElement?.textContent?.trim() === "New project", null, { timeout: 2000 })
        .catch(async () => note(`dialog ${tag}: focus returned to ${JSON.stringify(await page.evaluate(() => document.activeElement?.textContent?.trim()))}, expected the New project link`));

      // Palette
      if (vp === "desktop") {
        await page.keyboard.press("Control+k");
        await page.locator("dialog#palette[open]").waitFor();
        await page.keyboard.type("nw");
        await shot(page, `palette-${tag}`);
        const sel = await page.evaluate(() => document.querySelector('#palette [aria-selected="true"]')?.textContent);
        if (!/New project/.test(sel || "")) note(`palette fuzzy match selected ${JSON.stringify(sel)}`);
        await page.keyboard.press("Escape");
        await page.locator("dialog#palette[open]").waitFor({ state: "detached" }).catch(() => {});
        const pb = await page.evaluate(() => document.activeElement?.textContent?.trim());
        if (pb !== "New project") note(`palette closed: focus is on ${JSON.stringify(pb)}`);
      }

      // Account menu
      const acct = vp === "desktop" ? page.getByRole("button", { name: /^Account/ }) : page.getByRole("button", { name: "More" });
      await acct.click();
      await page.locator("#account:popover-open").waitFor();
      await shot(page, `menu-${tag}`);
      await page.keyboard.press("Escape");

      if (fresh) {
        // Keyboard only: the palette opens a dialog and gives focus back; the account menu takes and returns focus.
        await page.getByRole("link", { name: "New project" }).focus();
        await page.keyboard.press("Control+k");
        await page.keyboard.type("new");
        await page.keyboard.press("Enter");
        await page.locator("dialog#dialog[open]").waitFor();
        await page.keyboard.press("Escape");
        await page.locator("dialog#dialog[open]").waitFor({ state: "detached" }).catch(() => {});
        await page.waitForFunction(() => document.activeElement?.textContent?.trim() === "New project", null, { timeout: 2000 })
          .catch(async () => note(`palette > New project > Escape: focus is on ${JSON.stringify(await page.evaluate(() => document.activeElement?.textContent?.trim()))}`));
        const btn = page.getByRole("button", { name: /^Account/ });
        await btn.focus();
        await page.keyboard.press("Enter");
        await page.locator("#account:popover-open").waitFor();
        // Focus moves into the popover on the next frame, not synchronously.
        await page.waitForFunction(() => !!document.activeElement?.closest("#account"), null, { timeout: 2000 })
          .catch(() => note("account menu opened by keyboard but focus stayed outside it"));
        await page.keyboard.press("Escape");
        await page.waitForFunction(() => /^Account/.test(document.activeElement?.getAttribute("aria-label") || ""), null, { timeout: 2000 })
          .catch(async () => note(`account menu closed: focus is on ${await page.evaluate(() => document.activeElement?.tagName)}, expected the avatar button`));
        // Escape closes the palette without running anything.
        await page.keyboard.press("Control+k");
        await page.keyboard.press("Escape");
        if (await page.locator("dialog[open]").count()) note("Escape left a dialog open");
      }

      // Toast
      await page.evaluate(() => window.scriobh.toast("Project archived", { undo: () => {} }));
      await shot(page, `toast-${tag}`);

      // Project page
      await page.goto(base + "/p/the-salt-road");
      await page.evaluate(() => document.fonts.ready);
      await shot(page, `write-${tag}`);
      await sideways(page, `project ${tag}`);
      if (fresh) await tabOrder(page, "project");
      if (fresh) {
        // The writing view from its top: the skip link, header, tree, toolbar, title, editor, status bar.
        await page.goto(base + "/p/the-salt-road");
        await page.waitForSelector(".scriobh-prose");
        await page.mouse.click(1, 1);
        await tabOrder(page, "write view from the top", 60);
      }

      // Error page
      await page.goto(base + "/p/does-not-exist");
      await shot(page, `error-${tag}`);

      // Long titles must not break the shelf.
      await page.goto(base + "/");
      await sideways(page, `library with a 200-character title ${tag}`);
      await ctx.close();
    }
  }

  // Pass 3: behaviours that change data or session state.
  {
    const ctx = await browser.newContext({ ...viewports.desktop, storageState: state });
    const page = await newPage(ctx);
    await page.goto(base + "/");
    await page.getByRole("link", { name: "New project" }).click();
    await page.locator("dialog#dialog[open]").waitFor();
    // A rejected word goal puts focus on the word goal, not the title.
    await page.fill("#np-title", "Goal Test");
    await page.fill("#np-goal", "20000000");
    await page.keyboard.press("Enter");
    await page.locator("#np-goal[aria-invalid=true]").waitFor();
    await page.waitForFunction(() => document.activeElement?.id === "np-goal", null, { timeout: 2000 })
      .catch(async () => note(`word goal error: focus is on ${await page.evaluate(() => document.activeElement?.id)}`));
    // A refused submit (403) is reported inside the dialog and leaves the form alone.
    await page.route("**/ui/projects", (route) => route.request().method() === "POST" ? route.fulfill({ status: 403, contentType: "text/html", body: "no" }) : route.continue());
    await page.fill("#np-goal", "");
    await page.keyboard.press("Enter");
    const inline = page.locator("dialog [data-dialog-error]");
    await inline.waitFor({ timeout: 3000 }).catch(() => note("a 403 on submit was not reported inside the dialog"));
    if (await page.locator("#np-title").inputValue() !== "Goal Test") note("a 403 on submit cleared the form");
    await shot(page, "dialog-refused-desktop-light");
    await page.unroute("**/ui/projects");
    // Creating through the dialog ends on the new project (204 + HX-Redirect).
    await page.fill("#np-title", "Created in a Dialog");
    await page.keyboard.press("Enter");
    await page.waitForURL(/\/p\/created-in-a-dialog/, { timeout: 5000 }).catch(() => note("creating through the dialog did not reach the project page"));
    await ctx.close();
  }
  {
    // An expired session must send the browser to sign in, not swap the sign-in page into the dialog.
    const ctx = await browser.newContext({ ...viewports.desktop, storageState: state });
    const page = await newPage(ctx);
    await page.goto(base + "/");
    await ctx.clearCookies();
    await page.getByRole("link", { name: "New project" }).click();
    await page.waitForURL(/dev-idp|auth\/login/, { timeout: 5000 }).catch(() => note(`expired session: still on ${page.url()}`));
    if (await page.locator("dialog[open]").count()) note("expired session: a dialog is open with the sign-in page in it");
    await ctx.close();
  }
  // data-theme overrides the system preference in both directions.
  for (const scheme of ["light", "dark"]) {
    const other = scheme === "light" ? "dark" : "light";
    const bgOf = async (page) => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    const a = await browser.newContext({ ...viewports.desktop, colorScheme: scheme, storageState: state });
    const b = await browser.newContext({ ...viewports.desktop, colorScheme: other, storageState: state });
    const pa = await newPage(a), pb = await newPage(b);
    await pa.goto(base + "/");
    await pb.goto(base + "/");
    const natural = await bgOf(pa), wanted = await bgOf(pb);
    await pa.evaluate((t) => { document.documentElement.dataset.theme = t; }, other);
    const forced = await bgOf(pa);
    if (natural === wanted || forced !== wanted) note(`data-theme="${other}" on a ${scheme} system: background ${natural} became ${forced}, expected ${wanted}`);
    await a.close();
    await b.close();
  }

  // 200% zoom on a 1280 screen is a 640 px viewport.
  {
    const ctx = await browser.newContext({ viewport: { width: 640, height: 400 }, deviceScaleFactor: 2, storageState: state });
    const page = await newPage(ctx);
    for (const path of ["/", "/p/the-salt-road", "/ui/projects/new"]) { await page.goto(base + path); await sideways(page, `200% zoom ${path}`); }
    await ctx.close();
  }
  // Full-page no-JS fallback for the new-project form.
  {
    const ctx = await browser.newContext({ ...viewports.desktop, javaScriptEnabled: false, colorScheme: "light", storageState: state });
    const page = await newPage(ctx);
    await page.goto(base + "/ui/projects/new");
    await shot(page, "new-project-page-desktop-light");
    await page.fill("#np-title", "A Page Without Scripts");
    await page.getByRole("button", { name: "Create project" }).click();
    await page.waitForURL(/\/p\/a-page-without-scripts/);
    await ctx.close();
  }
} finally {
  await browser.close();
  server.kill("SIGTERM");
  await new Promise((r) => setTimeout(r, 1500)); // the server flushes its mirror while it shuts down
  rmSync(work, { recursive: true, force: true, maxRetries: 10, retryDelay: 300 });
}

console.log(problems.length ? `\n${problems.length} problem(s)` : "\nno problems");
process.exit(problems.length ? 1 : 0);
