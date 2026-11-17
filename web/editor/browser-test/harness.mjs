// Shared harness for the Chromium tests. Test tooling only: nothing here is on
// the runtime path, and the committed bundle does not import it.
//
// A loopback HTTP server serves one page, the committed bundle and stylesheet,
// and a small glue script, with the application's CSP header exactly as the plan
// gives it (script-src 'self'; style-src 'self'), or with a stricter one when a
// test passes strictCsp. Violations are recorded in the page, and every request
// the page attempts, to any origin, is recorded by Playwright.
//
// Chromium: SCRIOBH_CHROMIUM=/path/to/binary, else the Playwright cache
// ($PLAYWRIGHT_BROWSERS_PATH, ~/Library/Caches/ms-playwright or
// ~/.cache/ms-playwright, chromium-*), else the test is skipped
// (and fails when CI is set).

import {createServer} from "node:http"
import {test, before, after} from "node:test"
import {existsSync, readFileSync, readdirSync} from "node:fs"
import {homedir} from "node:os"
import {dirname, join, resolve} from "node:path"
import {fileURLToPath} from "node:url"

const here = dirname(fileURLToPath(import.meta.url))
const staticDir = resolve(here, "..", "..", "static")

// The application's header as the brief gives it, and a stricter one for the
// network assertions. Tests run under the application's header unless they ask.
export const CSP = "script-src 'self'; style-src 'self'"
export const CSP_STRICT = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'none'; img-src 'self'; base-uri 'none'; form-action 'none'"

// Same search as scripts/harness.mjs: the newest Playwright revision wins, and
// Playwright's own PLAYWRIGHT_BROWSERS_PATH is honoured. Current Playwright
// unpacks Chromium for Linux into chrome-linux64 (older ones: chrome-linux).
export function findChromium() {
  if (process.env.SCRIOBH_CHROMIUM) return process.env.SCRIOBH_CHROMIUM
  const mac = ["Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"]
  const rel = [
    ["chrome-mac-arm64", ...mac],
    ["chrome-mac-x64", ...mac],
    ["chrome-linux64", "chrome"],
    ["chrome-linux", "chrome"],
  ]
  const roots = [process.env.PLAYWRIGHT_BROWSERS_PATH, join(homedir(), "Library", "Caches", "ms-playwright"), join(homedir(), ".cache", "ms-playwright")]
  for (const root of roots.filter(r => r && existsSync(r))) {
    const revs = readdirSync(root).filter(d => /^chromium-\d+$/.test(d)).sort((a, b) => Number(b.split("-")[1]) - Number(a.split("-")[1]))
    for (const rev of revs) for (const parts of rel) {
      const p = join(root, rev, ...parts)
      if (existsSync(p)) return p
    }
  }
  return null
}

const PAGE = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>editor harness</title>
<link rel="stylesheet" href="/tokens.css"><link rel="stylesheet" href="/editor.css"><link rel="stylesheet" href="/harness.css">
</head><body><main><div id="ed"></div></main>
<script src="/harness.js"></script><script src="/editor.js"></script>
</body></html>`

const HARNESS_JS = `
window.h = {changes: [], limits: [], warnings: [], violations: [], links: [], ed: null,
  mount(markdown, opts) {
    if (h.ed) h.ed.destroy()
    h.changes = []; h.limits = []; h.warnings = []
    h.ed = scriobhEditor.mount(document.getElementById("ed"), Object.assign({
      markdown,
      onChange: (md, info) => h.changes.push({md, version: info.version, t: performance.now()}),
      onLimit: l => h.limits.push(l),
      onWarning: w => h.warnings.push(w),
      onLink: i => h.links.push(i)
    }, opts || {}))
    return true
  }
}
document.addEventListener("securitypolicyviolation", e => h.violations.push({directive: e.violatedDirective, blocked: e.blockedURI, sample: e.sample}))
`
const HARNESS_CSS = `body { margin: 0; padding: 2rem 1rem; background: #fbfaf7; } main { max-width: 52rem; margin: 0 auto; }`

export function startServer() {
  const requests = []
  const server = createServer((req, res) => {
    const [url, query] = req.url.split("?")
    requests.push(url)
    const send = (type, body) => {
      res.writeHead(200, {"content-type": type, "content-security-policy": query === "csp=strict" ? CSP_STRICT : CSP, "cache-control": "no-store"})
      res.end(body)
    }
    if (url === "/") return send("text/html; charset=utf-8", PAGE)
    if (url === "/harness.js") return send("text/javascript", HARNESS_JS)
    if (url === "/harness.css") return send("text/css", HARNESS_CSS)
    if (url === "/editor.js") return send("text/javascript", readFileSync(join(staticDir, "js", "editor.js")))
    // The real design tokens (the :root and theme blocks of app.css, without fonts),
    // so the editor styles are exercised against the values the app ships.
    if (url === "/tokens.css") {
      const css = readFileSync(join(staticDir, "css", "app.css"), "utf8").replace(/@font-face\s*\{[^}]*\}/g, "")
      return send("text/css", css)
    }
    if (url === "/editor.css") return send("text/css", readFileSync(join(staticDir, "css", "editor.css")))
    res.writeHead(404, {"content-type": "text/plain"})
    res.end("not found")
  })
  return new Promise(resolveStart => {
    server.listen(0, "127.0.0.1", () => resolveStart({
      url: `http://127.0.0.1:${server.address().port}/`,
      requests,
      close: () => new Promise(r => server.close(r))
    }))
  })
}

export async function launch() {
  const executablePath = findChromium()
  if (!executablePath) return null
  const {chromium} = await import("playwright-core")
  return chromium.launch({executablePath, headless: true, args: ["--no-sandbox"]})
}

// Skip (or fail under CI) when there is no browser.
export async function withBrowser(t, fn) {
  const browser = await launch().catch(err => { if (process.env.CI) throw err; return null })
  if (!browser) {
    if (process.env.CI) throw new Error("Chromium is required for the browser tests in CI (set SCRIOBH_CHROMIUM)")
    t.skip("no Chromium found (set SCRIOBH_CHROMIUM or install the Playwright browser)")
    return
  }
  const server = await startServer()
  try {
    await fn({browser, server})
  } finally {
    await browser.close()
    await server.close()
  }
}

export async function openPage(browser, server, {viewport = {width: 900, height: 900}, hasTouch = false, strictCsp = false} = {}) {
  const context = await browser.newContext({viewport, hasTouch})
  const page = await context.newPage()
  const errors = []
  // Every request the page attempts, to any origin (the server log only sees its own).
  const requests = []
  page.on("request", req => requests.push(req.url()))
  page.on("console", msg => { if (msg.type() === "error" || msg.type() === "warning") errors.push(msg.text()) })
  page.on("pageerror", err => errors.push("pageerror: " + err.message))
  await page.goto(server.url + (strictCsp ? "?csp=strict" : ""))
  await page.waitForFunction(() => window.scriobhEditor && window.h)
  return {page, context, errors, requests}
}

export const mount = (page, markdown, opts) => page.evaluate(([m, o]) => h.mount(m, o), [markdown, opts || null])
export const md = page => page.evaluate(() => h.ed.getMarkdown())
export const focus = page => page.evaluate(() => h.ed.focus())
export const sleep = ms => new Promise(r => setTimeout(r, ms))

// A paste event carrying the given clipboard data.
export function paste(page, {html, text}) {
  return page.evaluate(([html, text]) => {
    const dt = new DataTransfer()
    if (html != null) dt.setData("text/html", html)
    if (text != null) dt.setData("text/plain", text)
    const ev = new ClipboardEvent("paste", {clipboardData: dt, bubbles: true, cancelable: true})
    h.ed.view.dom.dispatchEvent(ev)
    return ev.defaultPrevented
  }, [html ?? null, text ?? null])
}

// Per-file setup: one browser and one server for the file, and a test wrapper that
// skips (never silently passes) without a browser. CI fails in before().
export function suite() {
  const state = {browser: null, server: null}
  before(async () => {
    state.browser = await launch().catch(err => { if (process.env.CI) throw err; return null })
    if (!state.browser && process.env.CI) throw new Error("Chromium is required in CI (set SCRIOBH_CHROMIUM)")
    if (state.browser) state.server = await startServer()
  })
  after(async () => {
    if (state.browser) await state.browser.close()
    if (state.server) await state.server.close()
  })
  const bt = (name, fn) => test(name, async t => {
    if (!state.browser) { t.skip("no Chromium found (set SCRIOBH_CHROMIUM)"); return }
    await fn(t)
  })
  const fresh = async (markdown = "", opts, pageOpts) => {
    const ctx = await openPage(state.browser, state.server, pageOpts)
    await mount(ctx.page, markdown, opts)
    await focus(ctx.page)
    return ctx
  }
  return {bt, fresh, state}
}
