// Shared by the end-to-end scripts: build the development binary, run it on an
// ephemeral loopback port with the stub identity provider, find a Chromium, and
// sign in. Build-time only; nothing here ships.
import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync, mkdtempSync, readdirSync, rmSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createInterface } from "node:readline";

export const repo = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export function findChromium() {
  if (process.env.CHROME) return process.env.CHROME;
  if (process.env.CSP_SMOKE_CHROMIUM) return process.env.CSP_SMOKE_CHROMIUM;
  const dirs = [process.env.PLAYWRIGHT_BROWSERS_PATH, join(homedir(), "Library", "Caches", "ms-playwright"), join(homedir(), ".cache", "ms-playwright")].filter((d) => d && existsSync(d));
  const rel = [
    ["chrome-mac-arm64", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"],
    ["chrome-mac-x64", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"],
    ["chrome-linux64", "chrome"],
    ["chrome-linux", "chrome"],
    ["chrome-win64", "chrome.exe"],
  ];
  for (const dir of dirs) {
    const revs = readdirSync(dir).filter((n) => /^chromium-\d+$/.test(n)).sort((a, b) => Number(b.split("-")[1]) - Number(a.split("-")[1]));
    for (const r of revs) for (const parts of rel) { const p = join(dir, r, ...parts); if (existsSync(p)) return p; }
  }
  return "";
}

// tags are Go build tags ("prod" for the production build, which has no development issuer); name is the file's name.
export function build(tmp, tags = "", name = "scriobh") {
  const bin = join(tmp, name);
  const b = spawnSync("go", ["build", ...(tags ? ["-tags", tags] : []), "-o", bin, "./cmd/scriobh"], { cwd: repo, encoding: "utf8" });
  if (b.status !== 0) throw new Error(`go build failed:\n${b.stdout}${b.stderr}`);
  return bin;
}

// Start the server on 127.0.0.1:0 with a data directory; resolves with { base, stop, log }.
export async function startServer(bin, dataDir, extraEnv = {}) {
  const env = { PATH: process.env.PATH, HOME: process.env.HOME ?? dataDir, SCRIOBH_LISTEN: "127.0.0.1:0", SCRIOBH_DATA_DIR: dataDir, SCRIOBH_OIDC_STUB: "1", SCRIOBH_RATE_LIMIT: "20000", ...extraEnv }; // polling test scripts would trip the default per-person limit
  for (const k of Object.keys(env)) if (env[k] === undefined || env[k] === null) delete env[k]; // a name given as undefined is left out (desktop mode has no development issuer)
  const child = spawn(bin, [], { env, stdio: ["ignore", "pipe", "pipe"] });
  let log = "";
  child.stderr.on("data", (d) => (log += d));
  const base = await new Promise((res, rej) => {
    const timer = setTimeout(() => rej(new Error(`server did not print LISTEN within 30s\n${log}`)), 30000);
    child.once("exit", (code) => { clearTimeout(timer); rej(new Error(`server exited early (${code})\n${log}`)); });
    createInterface({ input: child.stdout }).on("line", (line) => {
      const m = /^LISTEN (\S+)/.exec(line);
      if (m) { clearTimeout(timer); res(m[1]); }
    });
  });
  child.removeAllListeners("exit");
  return {
    base,
    child,
    get log() { return log; },
    async stop(signal = "SIGTERM") {
      if (child.exitCode !== null || child.signalCode !== null) return;
      const gone = new Promise((r) => child.once("exit", r));
      child.kill(signal);
      const t = setTimeout(() => child.kill("SIGKILL"), 5000);
      await gone;
      clearTimeout(t);
    },
  };
}

// The environment of a desktop instance, as the shell gives it to its sidecar (docs/p8-notes/desktop-contract.md): desktop
// mode, a launch token, and no development issuer. Spread it into startServer's extraEnv.
export function desktopEnv(token) {
  return { SCRIOBH_OIDC_STUB: undefined, SCRIOBH_DESKTOP: "1", SCRIOBH_LAUNCH_TOKEN: token };
}
export function launchToken() { return randomBytes(32).toString("hex"); }

export function tempDir(prefix = "scriobh-e2e-") { return mkdtempSync(join(tmpdir(), prefix)); }
export function rm(dir) { rmSync(dir, { recursive: true, force: true }); }

// Sign in through the development issuer's picker.
export async function signIn(page, base, who = "writer@dev.scriobh") {
  await page.goto(base + "/auth/login", { waitUntil: "load" });
  const nav = page.waitForURL((u) => u.origin === new URL(base).origin && !u.pathname.startsWith("/dev-idp") && !u.pathname.startsWith("/auth/"), { timeout: 15000 });
  await page.getByRole("button", { name: `Sign in as ${who}` }).click();
  await nav;
}
