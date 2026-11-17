// Node helper for the development Git server (cmd/scriobh-dev-gitserver): an SSH Git remote over bare
// repositories on disk, plus a loopback JSON control API. Test tooling only; nothing here ships.
//
//   const bin = buildGitServer(tmp);
//   const g = await startGitServer({ bin, root: join(tmp, "git") });
//   await g.control("POST", "/repos", { name: "book" });
//   g.sshUrl("book")            // ssh://git@127.0.0.1:PORT/book.git
//   await g.stop();
import { spawn, spawnSync } from "node:child_process";
import { join } from "node:path";
import { createInterface } from "node:readline";
import { repo } from "./harness.mjs";

// The server side of a push is the system git program; without it scripts should skip.
export function available() {
  return spawnSync("git", ["--version"], { encoding: "utf8" }).status === 0;
}

export function buildGitServer(tmp) {
  const bin = join(tmp, "scriobh-dev-gitserver");
  const b = spawnSync("go", ["build", "-o", bin, "./cmd/scriobh-dev-gitserver"], { cwd: repo, encoding: "utf8" });
  if (b.status !== 0) throw new Error(`go build of the dev git server failed:\n${b.stdout}${b.stderr}`);
  return bin;
}

// Start the built server over `root` (a directory for the bare repositories).
// Options: bin (from buildGitServer, required), root (required), anyUser, rsa.
export async function startGitServer({ bin, root, anyUser = false, rsa = false }) {
  if (!bin || !root) throw new Error("startGitServer needs bin and root");
  const args = ["--root", root];
  if (anyUser) args.push("--allow-any-user");
  if (rsa) args.push("--rsa");
  const child = spawn(bin, args, { stdio: ["ignore", "pipe", "pipe"] });
  let log = "";
  child.stderr.on("data", (d) => (log += d));
  const found = await new Promise((res, rej) => {
    const got = {};
    const timer = setTimeout(() => rej(new Error(`the git server did not print both LISTEN lines within 20s\n${log}`)), 20000);
    child.once("exit", (code) => { clearTimeout(timer); rej(new Error(`the git server exited early (${code})\n${log}`)); });
    createInterface({ input: child.stdout }).on("line", (line) => {
      const m = /^LISTEN (ssh|control) (\S+)/.exec(line);
      if (m) got[m[1]] = m[2];
      if (got.ssh && got.control) { clearTimeout(timer); res(got); }
    });
  });
  child.removeAllListeners("exit");
  const control = async (method, path, body) => {
    const r = await fetch(found.control + path, { method, headers: body === undefined ? {} : { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
    let json = null;
    try { json = await r.json(); } catch { /* no body */ }
    if (!r.ok) { const e = new Error(`${method} ${path} answered ${r.status} ${JSON.stringify(json)}`); e.status = r.status; e.json = json; throw e; }
    return json;
  };
  return {
    sshAddr: found.ssh,
    controlUrl: found.control,
    sshUrl: (name) => `ssh://git@${found.ssh}/${name.replace(/\.git$/, "")}.git`,
    control,
    info: () => control("GET", "/info"),
    get log() { return log; },
    async stop() {
      if (child.exitCode !== null || child.signalCode !== null) return;
      const gone = new Promise((r) => child.once("exit", r));
      child.kill("SIGTERM");
      const t = setTimeout(() => child.kill("SIGKILL"), 5000);
      await gone;
      clearTimeout(t);
    },
  };
}
