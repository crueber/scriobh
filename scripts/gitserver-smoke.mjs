#!/usr/bin/env node
// Proves the Node side of the development Git server works: start it, then exercise every control call.
//
//   node scripts/gitserver-smoke.mjs
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { rm, tempDir } from "./harness.mjs";
import { available, buildGitServer, startGitServer } from "./gitserver.mjs";

if (!available() || spawnSync("ssh-keygen", ["-V"], { encoding: "utf8" }).error) {
  console.log("gitserver-smoke: SKIPPED (git or ssh-keygen is not on PATH)");
  process.exit(0);
}
const tmp = tempDir("scriobh-gitsmoke-");
let g;
const fail = (m) => { console.error("gitserver-smoke: FAIL " + m); throw new Error(m); };
const eq = (a, b, m) => { if (JSON.stringify(a) !== JSON.stringify(b)) fail(`${m}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`); };
const run = (cwd, env, cmd, args) => spawnSync(cmd, args, { cwd, env: { ...process.env, ...env }, encoding: "utf8" });
try {
  g = await startGitServer({ bin: buildGitServer(tmp), root: join(tmp, "repos") });
  const info = await g.info();
  eq(info.ssh_addr, g.sshAddr, "ssh_addr");
  if (!info.host_keys.length || !/^SHA256:/.test(info.host_keys[0].fingerprint) || !/^ssh-ed25519 /.test(info.host_keys[0].authorized_keys_line)) fail("host key info");
  eq(g.sshUrl("book"), `ssh://git@${g.sshAddr}/book.git`, "sshUrl");

  eq((await g.control("POST", "/repos", { name: "book" })).path, "/book.git", "repo path");
  eq((await g.info()).repos, ["book"], "repos");
  let status = 0;
  await g.control("POST", "/repos", { name: "book" }).catch((e) => { status = e.status; });
  eq(status, 409, "creating twice");

  // A real client key, authorised through the control API.
  const key = join(tmp, "id");
  if (run(tmp, {}, "ssh-keygen", ["-q", "-t", "ed25519", "-N", "", "-f", key]).status !== 0) fail("ssh-keygen");
  const pub = readFileSync(key + ".pub", "utf8");
  const fp = (await g.control("POST", "/authorize", { public_key: pub })).fingerprint;
  eq((await g.info()).authorized, [fp], "authorized");
  const known = join(tmp, "known_hosts");
  const [host, port] = g.sshAddr.split(":");
  const env = { GIT_SSH_COMMAND: `ssh -i ${key} -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=${known} -o BatchMode=yes -o ConnectTimeout=10 -p ${port}`,
    GIT_AUTHOR_NAME: "Me", GIT_AUTHOR_EMAIL: "me@example.com", GIT_COMMITTER_NAME: "Me", GIT_COMMITTER_EMAIL: "me@example.com" };
  const url = g.sshUrl("book");
  if (run(tmp, env, "git", ["ls-remote", url]).status !== 0) fail("ls-remote with the authorised key");

  // From outside: an empty repository gets its branch; head and log see it.
  const first = await g.control("POST", "/foreign-commit", { repo: "book", branch: "main", message: "From outside", files: { "manuscript/one.md": "One.\n" } });
  eq((await g.control("GET", "/head?repo=book&branch=main")).sha, first.sha, "head");
  let missing = 0;
  await g.control("GET", "/head?repo=book&branch=nope").catch((e) => { missing = e.status; });
  eq(missing, 404, "missing branch");

  // Push through SSH from a clone, then show the rejected-push case.
  const work = join(tmp, "work");
  if (run(tmp, env, "git", ["clone", "-q", url, work]).status !== 0) fail("clone");
  writeFileSync(join(work, "mine.md"), "mine\n");
  for (const a of [["add", "-A"], ["commit", "-q", "-m", "Mine"], ["push", "-q", "origin", "main"]]) if (run(work, env, "git", a).status !== 0) fail("git " + a[0]);
  await g.control("POST", "/foreign-commit", { repo: "book", branch: "main", message: "Someone pushed" });
  const log = await g.control("GET", "/log?repo=book&branch=main");
  eq(log.items.map((c) => c.subject), ["Someone pushed", "Mine", "From outside"], "log subjects");
  eq(log.items[0].author, "Someone Else", "foreign author");
  writeFileSync(join(work, "again.md"), "x\n");
  for (const a of [["add", "-A"], ["commit", "-q", "-m", "Stale"]]) run(work, env, "git", a);
  if (run(work, env, "git", ["push", "-q", "origin", "main"]).status === 0) fail("a stale push was accepted");

  // Host key rotation and restore.
  const rot = await g.control("POST", "/rotate-host-key", {});
  if (rot.host_keys[0].fingerprint === info.host_keys[0].fingerprint) fail("rotation kept the key");
  const changed = run(tmp, env, "git", ["ls-remote", url]);
  if (changed.status === 0 || !/HOST IDENTIFICATION HAS CHANGED/.test(changed.stderr + changed.stdout)) fail("a changed host key was not noticed");
  eq((await g.control("POST", "/restore-host-key")).host_keys[0].fingerprint, info.host_keys[0].fingerprint, "restored");
  if (run(tmp, env, "git", ["ls-remote", url]).status !== 0) fail("ls-remote after restore");

  // A bundle made by git verifies and lists its head; a text file does not.
  const bundle = join(tmp, "b.bundle");
  if (run(work, env, "git", ["bundle", "create", bundle, "main"]).status !== 0) fail("bundle create");
  const vb = await g.control("POST", "/verify-bundle", { path: bundle });
  if (!vb.ok || vb.heads[0].ref !== "refs/heads/main") fail("verify-bundle " + JSON.stringify(vb));
  writeFileSync(join(tmp, "junk"), "not a bundle");
  if ((await g.control("POST", "/verify-bundle", { path: join(tmp, "junk") })).ok) fail("a text file verified as a bundle");

  // Deauthorise, then shut down through the control API.
  await g.control("POST", "/deauthorize", { fingerprint: fp });
  if (run(tmp, env, "git", ["ls-remote", url]).status === 0) fail("a deauthorised key still works");
  await g.control("POST", "/shutdown");
  await new Promise((r) => setTimeout(r, 300));
  console.log("gitserver-smoke: ok");
} catch (e) {
  console.error(e.stack || e);
  process.exitCode = 1;
} finally {
  if (g) await g.stop();
  rm(tmp);
}
