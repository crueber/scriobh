// Scaffolding for the stage 2 end-to-end scripts: one server, one signed-in
// browser context, step runner, console/CSP/off-origin watchers, API helpers.
import { build, findChromium, rm, signIn, startServer, tempDir } from "./harness.mjs";
import { join } from "node:path";

export async function createKit(title, opts = {}) {
  const { chromium } = await import("playwright");
  const tmp = tempDir();
  const dataDir = join(tmp, "data");
  const bin = build(tmp, opts.tags || "");
  const kit = { tmp, dataDir, bin, findings: [], results: [], allowed: [], step: "start", sleep: (ms) => new Promise((r) => setTimeout(r, ms)), MOD: "ControlOrMeta" };
  kit.srv = await startServer(bin, dataDir, opts.env || {});
  kit.base = () => kit.srv.base;
  const exe = findChromium();
  kit.browser = await chromium.launch(exe ? { executablePath: exe } : {});
  console.log(`${title}: ${kit.browser.version()} against ${kit.base()}`);

  // A hard limit on the whole script: if it has not finished by then, its own browser and server are killed and it exits with 2.
  kit.deadline = (ms) => {
    const t = setTimeout(() => {
      console.error(`\n${title}: not finished after ${Math.round(ms / 1000)} s: stopped (a step stalled)`);
      try { kit.browser.process()?.kill("SIGKILL"); } catch { /* gone */ }
      try { kit.srv.child.kill("SIGKILL"); } catch { /* gone */ }
      process.exit(2);
    }, ms);
    t.unref();
    return t;
  };

  kit.watch = (page, label) => {
    const note = (m) => kit.findings.push(`[${kit.step}] (${label}) ${m}`);
    page.on("console", (m) => {
      if (m.type() !== "error" && m.type() !== "warning") return;
      const t = m.text();
      if (/favicon\.ico/.test(m.location().url || "")) return;
      // The kit blocks service workers (a worker would answer requests the scripts count); the registration is refused and Chromium says so.
      if (/Service Worker registration blocked/.test(t)) return;
      if (kit.allowed.some((re) => re.test(t))) return;
      note(`console ${m.type()}: ${t.slice(0, 200)} ${m.location().url || ""}`);
    });
    page.on("pageerror", (e) => note(`uncaught: ${e.message}`));
    // A page being unloaded after a server restart (new port) may still send its
    // keepalive lease release to the origin it was served from; that origin is
    // ours. Only an origin this run never served is off-origin.
    const served = new Set();
    page.on("request", (r) => {
      served.add(new URL(kit.base()).origin);
      for (const o of kit.origins || []) served.add(o); // a second server this script started itself
      if (/^(https?|wss?):/.test(r.url()) && !served.has(new URL(r.url()).origin)) note(`off-origin request ${r.url()}`);
    });
    page.on("dialog", (d) => d.accept().catch(() => {}));
  };
  kit.csp = async (page, label) => {
    const v = await page.evaluate(() => window.__csp || []).catch(() => []);
    for (const x of v) kit.findings.push(`[${kit.step}] (${label}) securitypolicyviolation: ${x}`);
    const dom = await page.evaluate(() => ({ style: document.querySelectorAll("[style]").length, inline: document.querySelectorAll("script:not([src])").length })).catch(() => null);
    if (dom && (dom.style || dom.inline)) kit.findings.push(`[${kit.step}] (${label}) live DOM has ${dom.style} style attribute(s), ${dom.inline} inline script(s)`);
  };
  kit.newContext = async (opts = {}) => {
    const ctx = await kit.browser.newContext({ serviceWorkers: "block", ...opts });
    await ctx.addInitScript(() => {
      window.__csp = [];
      document.addEventListener("securitypolicyviolation", (e) => window.__csp.push(`${e.violatedDirective} ${e.blockedURI || "inline"}`));
    });
    return ctx;
  };
  kit.signedIn = async (opts = {}, who) => {
    const ctx = await kit.newContext({ viewport: { width: 1280, height: 800 }, ...opts });
    const page = await ctx.newPage();
    kit.watch(page, "main");
    await signIn(page, kit.base(), who);
    return { ctx, page };
  };
  kit.test = async (name, fn) => {
    if (kit.only && !name.includes(kit.only)) return;
    kit.step = name;
    kit.allowed = [];
    const t0 = Date.now(), before = kit.findings.length;
    try {
      await fn();
      if (kit.main) await kit.csp(kit.main, "main");
      const bad = kit.findings.length - before;
      kit.results.push({ name, ok: bad === 0, ms: Date.now() - t0, err: bad ? `${bad} watcher finding(s)` : "" });
    } catch (e) {
      kit.results.push({ name, ok: false, ms: Date.now() - t0, err: e.message.split("\n").slice(0, 6).join(" | ") });
    }
    const r = kit.results[kit.results.length - 1];
    console.log(`${r.ok ? "PASS" : "FAIL"}  ${name}  (${(r.ms / 1000).toFixed(1)}s)${r.err ? "\n      " + r.err : ""}`);
  };
  kit.api = async (ctx, method, path, data) => {
    const r = await ctx.request.fetch(kit.base() + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
    let json = null;
    try { json = await r.json(); } catch { /* no body */ }
    return { status: r.status(), json };
  };
  kit.waitFor = async (fn, what, ms = 8000) => {
    const t0 = Date.now();
    while (Date.now() - t0 < ms) { try { const v = await fn(); if (v) return v; } catch { /* retry */ } await kit.sleep(60); }
    throw new Error(`timed out waiting for ${what}`);
  };
  kit.finish = async (name) => {
    await kit.browser.close();
    await kit.srv.stop();
    rm(kit.tmp);
    console.log("");
    for (const f of kit.findings) console.log("FINDING " + f);
    const failed = kit.results.filter((r) => !r.ok).length;
    console.log(`${name}: ${kit.results.length - failed} of ${kit.results.length} steps passed${kit.findings.length ? `, ${kit.findings.length} watcher finding(s)` : ", no console, CSP or off-origin findings"}`);
    process.exit(failed || kit.findings.length ? 1 : 0);
  };
  return kit;
}
// The in-page question before something that cannot be undone (js/arm.js; the browser's own confirm() is never used).
// armAnswer waits for it, answers "ok" or "cancel", and returns its words. armOk says yes to every question that comes
// within ms (a sign-out may ask one, two or none), and returns how many there were.
export async function armAnswer(page, answer = "ok", timeout = 8000) {
  const box = page.locator(".arm").last();
  await box.waitFor({ state: "visible", timeout });
  const msg = await box.locator(".arm-msg").innerText();
  if (answer === "ok") {
    const yes = box.locator(".arm-ok");
    await page.waitForFunction((b) => b && !b.disabled, await yes.elementHandle(), { timeout: 5000 }); // off for half a second, against a double tap
    await yes.click();
  } else await box.locator("button:not(.arm-ok)").click();
  return msg;
}
export async function armOk(page, ms = 1500) {
  let n = 0;
  for (;;) {
    try { await armAnswer(page, "ok", ms); n++; } catch { return n; }
  }
}
export const ok = (c, m) => { if (!c) throw new Error(m); };
export const eq = (a, b, m) => { if (a !== b) throw new Error(`${m}: got ${JSON.stringify(String(a).slice(0, 120))}, want ${JSON.stringify(String(b).slice(0, 120))}`); };
