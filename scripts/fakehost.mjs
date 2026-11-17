// A fake code host for the browser tests: GitHub Enterprise Server (/api/v3, /api/uploads) and Forgejo (/api/v1) REST
// subsets over one self-signed HTTPS server on 127.0.0.1, and the repositories it "creates" served over Git smart HTTP
// by `git http-backend`, so a push lands in a real bare repository. Written from the providers' documentation, like the
// Go fake in internal/gitmirror/fakehost_test.go: it does what the documentation was read to say, not what a real host
// was seen to do. Test tooling only; nothing here ships. The development server trusts its certificate through
// SCRIOBH_DEV_TRUST_PEM (a build without -tags prod). The key's curve is written by name: macOS's own LibreSSL (/usr/bin/openssl)
// otherwise writes the curve's parameters in full, which Go refuses ("invalid ECDSA parameters"), and the host "could not be reached".
//
//   const host = await startFakeHost({ dir });
//   host.host            // 127.0.0.1:PORT, what an account is connected with
//   host.pemPath         // pass as SCRIOBH_DEV_TRUST_PEM
//   host.token           // the token the fake accepts
//   host.addRepo("mairead", "taken", { private: true })
//   host.mode = "ratelimit"  // or "unauth", "5xx", "slow": every API call answers that way until set back to ""
//   host.head("mairead", "salt-road", "main")
//   await host.stop();
import { spawn, spawnSync } from "node:child_process";
import { mkdirSync, readFileSync } from "node:fs";
import { createServer } from "node:https";
import { join } from "node:path";

const TOKEN = "tok_Zq7Vn3xK9wLm2Rt5Yp8Bd4Hc6Jf1Ga0S";

function execPath() {
  const r = spawnSync("git", ["--exec-path"], { encoding: "utf8" });
  if (r.status !== 0) throw new Error("git is not available");
  return join(r.stdout.trim(), "git-http-backend");
}

export function available() {
  return spawnSync("git", ["--version"], { encoding: "utf8" }).status === 0 && spawnSync("openssl", ["version"], { encoding: "utf8" }).status === 0;
}

function body(req) {
  return new Promise((res, rej) => {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => res(Buffer.concat(chunks)));
    req.on("error", rej);
  });
}

function json(res, code, v, headers = {}) {
  const b = Buffer.from(JSON.stringify(v));
  res.writeHead(code, { "Content-Type": "application/json", "Content-Length": b.length, ...headers });
  res.end(b);
}

export async function startFakeHost({ dir, token = TOKEN, login = "mairead", orgs = [] } = {}) {
  if (!dir) throw new Error("startFakeHost needs dir");
  mkdirSync(dir, { recursive: true });
  const root = join(dir, "repos");
  mkdirSync(root, { recursive: true });
  const keyPath = join(dir, "host.key"), pemPath = join(dir, "host.pem");
  const g = spawnSync("openssl", ["req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-pkeyopt", "ec_param_enc:named_curve", "-nodes", "-keyout", keyPath, "-out", pemPath, "-days", "2", "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1,DNS:localhost"], { encoding: "utf8" });
  if (g.status !== 0) throw new Error(`openssl failed: ${g.stderr}`);
  const backend = execPath();
  const state = { mode: "", repos: new Map(), rels: new Map(), reqs: [], nextId: 100, uploads: 0, orgs: new Set(orgs) };
  const repoDir = (o, n) => join(root, o, `${n}.git`);
  const git = (args, opts = {}) => spawnSync("git", args, { encoding: "utf8", ...opts });
  const isEmpty = (o, n) => git(["-C", repoDir(o, n), "for-each-ref", "--count=1"]).stdout.trim() === "";
  const head = (o, n, branch) => { const r = git(["-C", repoDir(o, n), "rev-parse", "--verify", "--quiet", `refs/heads/${branch}`]); return r.status === 0 ? r.stdout.trim() : ""; };
  function addRepo(owner, name, { private: priv = true, description = "", archived = false, noPush = false } = {}) {
    mkdirSync(join(root, owner), { recursive: true });
    const d = repoDir(owner, name);
    if (git(["init", "--bare", "--quiet", "--initial-branch=trunk", d]).status !== 0) throw new Error("git init failed");
    git(["-C", d, "config", "http.receivepack", "true"]);
    const r = { owner, name, private: priv, description, branch: "trunk", archived, noPush };
    state.repos.set(`${owner}/${name}`, r);
    return r;
  }
  const repoJSON = (flavor, r) => {
    const m = { name: r.name, full_name: `${r.owner}/${r.name}`, private: r.private, description: r.description, default_branch: r.branch, archived: r.archived, fork: false,
      owner: { login: r.owner }, permissions: { push: !r.noPush, admin: !r.noPush, pull: true } };
    m.size = isEmpty(r.owner, r.name) ? 0 : 12; // both hosts report a size; Forgejo also says whether the repository is empty
    if (flavor === "forgejo") m.empty = isEmpty(r.owner, r.name);
    return m;
  };
  const authorized = (flavor, req) => req.headers.authorization === (flavor === "github" ? `Bearer ${token}` : `token ${token}`);
  const relJSON = (flavor, rel) => ({ id: rel.id, tag_name: rel.tag, body: rel.body, draft: false,
    assets: rel.assets.map((a) => (flavor === "github" ? { id: a.id, name: a.name, size: a.size, state: "uploaded" } : { id: a.id, name: a.name, size: a.size })),
    html_url: "https://127.0.0.1:1/never-followed", upload_url: "https://127.0.0.1:1/never-followed/upload{?name,label}" });
  const nid = () => ++state.nextId;

  async function api(flavor, req, res, url, rest) {
    if (!authorized(flavor, req)) return json(res, 401, { message: "Bad credentials" });
    switch (state.mode) {
      case "ratelimit": return json(res, 429, { message: "slow down" }, { "Retry-After": "7" });
      case "unauth": return json(res, 401, { message: "Bad credentials" });
      case "5xx": res.writeHead(502); return res.end("<html>Bad gateway</html>");
      case "slow": await new Promise((r) => setTimeout(r, 2500)); break;
      default: break;
    }
    const parts = rest.replace(/^\/|\/$/g, "").split("/");
    const m = req.method;
    if (m === "GET" && rest === "/user") return json(res, 200, { login });
    if (m === "GET" && rest === "/user/repos") {
      const page = Math.max(1, Number(url.searchParams.get("page") || 1)), per = Number(url.searchParams.get("per_page") || url.searchParams.get("limit") || 50);
      const all = [...state.repos.values()].sort((a, b) => a.name.localeCompare(b.name));
      return json(res, 200, all.slice((page - 1) * per, page * per).map((r) => repoJSON(flavor, r)), all.length > page * per ? { Link: `<https://127.0.0.1:1/never-followed?page=${page + 1}>; rel="next"` } : {});
    }
    if (m === "POST" && (rest === "/user/repos" || (parts.length === 3 && parts[0] === "orgs" && parts[2] === "repos"))) {
      let owner = login;
      if (parts[0] === "orgs") { owner = parts[1]; if (!state.orgs.has(owner)) return json(res, 404, { message: "Not Found" }); }
      let inp = {};
      try { inp = JSON.parse((await body(req)).toString() || "{}"); } catch { return json(res, 422, { message: "bad body" }); }
      if (!inp.name) return json(res, 422, { message: "bad body" });
      if (inp.auto_init) return json(res, 422, { message: "auto_init is not accepted by this fake: the first push is meant to be ours" });
      if (state.repos.has(`${owner}/${inp.name}`)) {
        return flavor === "github"
          ? json(res, 422, { message: "Repository creation failed.", errors: [{ resource: "Repository", code: "custom", field: "name", message: "name already exists on this account" }] })
          : json(res, 409, { message: "The repository with the same name already exists." });
      }
      const r = addRepo(owner, inp.name, { private: !!inp.private, description: inp.description || "" });
      return json(res, 201, repoJSON(flavor, r));
    }
    if (parts[0] === "repos" && parts.length === 3 && (m === "GET" || m === "PATCH")) {
      const r = state.repos.get(`${parts[1]}/${parts[2]}`);
      if (!r) return json(res, 404, { message: "Not Found" });
      if (m === "PATCH") {
        let inp = {};
        try { inp = JSON.parse((await body(req)).toString() || "{}"); } catch { /* none */ }
        if (inp.default_branch && !head(r.owner, r.name, inp.default_branch)) return json(res, 422, { message: "branch does not exist" });
        if (inp.default_branch) r.branch = inp.default_branch;
      }
      return json(res, 200, repoJSON(flavor, r));
    }
    if (parts[0] === "repos" && parts[3] === "releases") return releases(flavor, req, res, url, parts);
    return json(res, 404, { message: `no such route ${rest}` });
  }

  async function releases(flavor, req, res, url, parts) {
    const [, owner, name] = parts, key = (t) => `${owner}/${name}/${t}`, m = req.method;
    if (m === "POST" && parts.length === 4) {
      if (!state.repos.has(`${owner}/${name}`)) return json(res, 404, { message: "Not Found" });
      let inp = {};
      try { inp = JSON.parse((await body(req)).toString() || "{}"); } catch { /* none */ }
      if (state.rels.has(key(inp.tag_name))) return json(res, 422, { message: "already_exists", errors: [{ resource: "Release", code: "already_exists", field: "tag_name" }] });
      const rel = { id: nid(), tag: inp.tag_name, body: inp.body || "", target: inp.target_commitish, assets: [] };
      state.rels.set(key(rel.tag), rel);
      return json(res, 201, relJSON(flavor, rel));
    }
    if (m === "GET" && parts.length === 6 && parts[4] === "tags") {
      const rel = state.rels.get(key(parts[5]));
      return rel ? json(res, 200, relJSON(flavor, rel)) : json(res, 404, { message: "Not Found" });
    }
    if (m === "POST" && parts.length === 6 && parts[5] === "assets" && flavor === "forgejo") return addAsset(flavor, req, res, url, owner, name, parts[4]);
    const del = (id) => {
      for (const [k, rel] of state.rels) {
        if (!k.startsWith(`${owner}/${name}/`)) continue;
        const i = rel.assets.findIndex((a) => String(a.id) === id);
        if (i >= 0) { rel.assets.splice(i, 1); res.writeHead(204); return res.end(); }
      }
      return json(res, 404, { message: "Not Found" });
    };
    if (m === "DELETE" && flavor === "github" && parts.length === 6 && parts[4] === "assets") return del(parts[5]);
    if (m === "DELETE" && flavor === "forgejo" && parts.length === 7 && parts[5] === "assets") return del(parts[6]);
    return json(res, 404, { message: "no such release route" });
  }

  async function addAsset(flavor, req, res, url, owner, name, relId) {
    let rel = null;
    for (const [k, r] of state.rels) if (k.startsWith(`${owner}/${name}/`) && String(r.id) === relId) rel = r;
    if (!rel) return json(res, 404, { message: "Not Found" });
    let data = await body(req);
    const aname = url.searchParams.get("name") || "";
    const ct = /boundary=(?:"([^"]+)"|([^;]+))/.exec(req.headers["content-type"] || "");
    if (ct) { // Forgejo takes multipart/form-data: the asset is the part named attachment
      const delim = Buffer.from("--" + (ct[1] || ct[2]));
      let at = data.indexOf(delim);
      while (at >= 0) {
        const next = data.indexOf(delim, at + delim.length);
        if (next < 0) break;
        const part = data.subarray(at + delim.length + 2, next - 2); // minus the line breaks around the boundary
        const split = part.indexOf("\r\n\r\n");
        if (split >= 0 && /name="attachment"/.test(part.subarray(0, split).toString())) { data = part.subarray(split + 4); break; }
        at = next;
      }
    }
    if (rel.assets.some((a) => a.name === aname)) return json(res, 422, { message: "already_exists", errors: [{ resource: "ReleaseAsset", code: "already_exists", field: "name" }] });
    const a = { id: nid(), name: aname, size: data.length, state: "uploaded" };
    rel.assets.push(a);
    state.uploads++;
    return json(res, 201, { id: a.id, name: a.name, size: a.size });
  }

  async function serveGit(req, res, url) {
    const auth = /^Basic (.+)$/.exec(req.headers.authorization || "");
    const pw = auth ? Buffer.from(auth[1], "base64").toString().split(":").slice(1).join(":") : null;
    if (pw !== token) { res.writeHead(401, { "WWW-Authenticate": 'Basic realm="git"' }); return res.end("authentication required"); }
    const data = await body(req);
    const env = { PATH: process.env.PATH, HOME: process.env.HOME || root, GIT_PROJECT_ROOT: root, GIT_HTTP_EXPORT_ALL: "1", GIT_CONFIG_NOSYSTEM: "1", REQUEST_METHOD: req.method, PATH_INFO: url.pathname,
      QUERY_STRING: url.search.replace(/^\?/, ""), CONTENT_TYPE: req.headers["content-type"] || "", CONTENT_LENGTH: String(data.length), REMOTE_USER: "git", REMOTE_ADDR: "127.0.0.1",
      GATEWAY_INTERFACE: "CGI/1.1", SERVER_PROTOCOL: "HTTP/1.1" };
    if (req.headers["content-encoding"]) env.HTTP_CONTENT_ENCODING = req.headers["content-encoding"];
    if (req.headers["git-protocol"]) env.GIT_PROTOCOL = req.headers["git-protocol"];
    const child = spawn(backend, [], { env, stdio: ["pipe", "pipe", "inherit"] });
    const out = [];
    child.stdout.on("data", (c) => out.push(c));
    child.stdin.on("error", () => {});
    child.stdin.end(data);
    await new Promise((r) => child.on("close", r));
    const all = Buffer.concat(out);
    let i = all.indexOf("\r\n\r\n"), skip = 4;
    if (i < 0) { i = all.indexOf("\n\n"); skip = 2; }
    if (i < 0) { res.writeHead(500); return res.end("bad CGI answer"); }
    const headers = {};
    let code = 200;
    for (const line of all.subarray(0, i).toString().split(/\r?\n/)) {
      const k = line.indexOf(":");
      if (k < 0) continue;
      const name = line.slice(0, k).trim(), value = line.slice(k + 1).trim();
      if (name.toLowerCase() === "status") code = Number(value.split(" ")[0]) || 200; else headers[name] = value;
    }
    res.writeHead(code, headers);
    res.end(all.subarray(i + skip));
  }

  const server = createServer({ key: readFileSync(keyPath), cert: readFileSync(pemPath) }, async (req, res) => {
    const url = new URL(req.url, "https://127.0.0.1");
    state.reqs.push({ method: req.method, path: url.pathname });
    try {
      if (url.pathname.startsWith("/api/v3/")) return await api("github", req, res, url, url.pathname.slice("/api/v3".length));
      if (url.pathname.startsWith("/api/v1/")) return await api("forgejo", req, res, url, url.pathname.slice("/api/v1".length));
      if (url.pathname.startsWith("/api/uploads/") && req.method === "POST") {
        if (!authorized("github", req)) return json(res, 401, { message: "Bad credentials" });
        const p = url.pathname.slice("/api/uploads/".length).split("/"); // repos/o/r/releases/id/assets
        if (p.length === 6 && p[0] === "repos" && p[3] === "releases" && p[5] === "assets") return await addAsset("github", req, res, url, p[1], p[2], p[4]);
        return json(res, 404, { message: "no such upload route" });
      }
      return await serveGit(req, res, url);
    } catch (e) {
      try { res.writeHead(500); res.end(String(e)); } catch { /* gone */ }
    }
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  const port = server.address().port;
  return {
    host: `127.0.0.1:${port}`, port, token, login, pemPath,
    get mode() { return state.mode; }, set mode(v) { state.mode = v; },
    addRepo, head, isEmpty, orgs: state.orgs, dir: repoDir,
    // give a repository some content, so the host says it is not empty
    addCommit(o, n, branch = "main") {
      const d = repoDir(o, n);
      const tree = spawnSync("git", ["--git-dir", d, "hash-object", "-t", "tree", "-w", "--stdin"], { input: "", encoding: "utf8" }).stdout.trim();
      const c = spawnSync("git", ["--git-dir", d, "-c", "user.name=x", "-c", "user.email=x@example.org", "commit-tree", tree, "-m", "already here"], { encoding: "utf8" }).stdout.trim();
      spawnSync("git", ["--git-dir", d, "update-ref", `refs/heads/${branch}`, c]);
    },
    requests: () => state.reqs.slice(),
    repo: (o, n) => state.repos.get(`${o}/${n}`) || null,
    release: (o, n, tag) => state.rels.get(`${o}/${n}/${tag}`) || null,
    uploads: () => state.uploads,
    tree: (o, n, branch) => git(["-C", repoDir(o, n), "ls-tree", "-r", "--name-only", branch]).stdout.split("\n").filter(Boolean),
    show: (o, n, branch, path) => git(["-C", repoDir(o, n), "show", `${branch}:${path}`]).stdout,
    gitOut: (o, n, ...args) => git(["-C", repoDir(o, n), ...args]).stdout.trim(),
    stop: () => new Promise((r) => { server.closeAllConnections?.(); server.close(() => r()); }),
  };
}
