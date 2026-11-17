// A small SMTP relay for the mail end-to-end script: EHLO, STARTTLS (or TLS from the first byte), AUTH PLAIN and LOGIN,
// MAIL, RCPT, DATA, QUIT, with a self-signed certificate made by the `openssl` command for 127.0.0.1. It keeps what it was
// given so the script can look at it. Test tooling only; nothing here ships.
//
//   const smtp = await startFakeSmtp({ dir, mode: "tls", user: "mailer", pass: "secret" });
//   smtp.port; smtp.caPem; smtp.mails   // each: { from, to, subject, tls, authed, authUser, raw, attachments: [{ name, type, bytes, head }] }
//   await smtp.close();
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import net from "node:net";
import tls from "node:tls";

// A certificate that is its own authority, for 127.0.0.1 and localhost: pasted into the settings page's authority box.
// The key's curve is written by name: macOS's own LibreSSL (/usr/bin/openssl) writes the parameters in full, and Go refuses
// that certificate ("invalid ECDSA parameters"), as it did for scripts/fakehost.mjs.
export function makeCert(dir) {
  const key = join(dir, "relay-key.pem"), cert = join(dir, "relay-cert.pem");
  const r = spawnSync("openssl", ["req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-pkeyopt", "ec_param_enc:named_curve", "-nodes", "-keyout", key, "-out", cert, "-days", "3", "-subj", "/CN=localhost",
    "-addext", "subjectAltName=IP:127.0.0.1,DNS:localhost", "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=digitalSignature,keyCertSign"], { encoding: "utf8" });
  if (r.status !== 0) throw new Error(`openssl could not make the certificate:\n${r.stdout}${r.stderr}`);
  return { key: readFileSync(key, "utf8"), cert: readFileSync(cert, "utf8") };
}

function header(raw, name) {
  const m = new RegExp(`^${name}:[ \\t]*(.*(?:\\r\\n[ \\t].*)*)`, "im").exec(raw);
  return m ? m[1].replace(/\r\n[ \t]+/g, " ").trim() : "";
}

function attachments(raw) {
  const out = [];
  const bm = /boundary="?([^";\r\n]+)"?/i.exec(raw);
  if (!bm) return out;
  for (const part of raw.split("--" + bm[1])) {
    const i = part.indexOf("\r\n\r\n");
    if (i < 0) continue;
    const head = part.slice(0, i), body = part.slice(i + 4);
    const f = /filename\*?="?([^";\r\n]+)"?/i.exec(head);
    if (!f) continue;
    const data = Buffer.from(body.replace(/\s+/g, ""), "base64");
    out.push({ name: f[1], type: (/Content-Type:\s*([^;\r\n]+)/i.exec(head) || [])[1] || "", bytes: data.length, head: data.subarray(0, 2).toString("latin1"), data });
  }
  return out;
}

export async function startFakeSmtp({ dir, mode = "tls", user = "mailer", pass = "relay-pass" }) {
  const { key, cert } = makeCert(dir);
  const ctx = tls.createSecureContext({ key, cert });
  const mails = [];
  const st = { connections: 0, authFailures: 0, refuse: "" }; // refuse: an SMTP reply to give MAIL FROM (a failure the script wants to see)

  function serve(raw, implicit) {
    st.connections++;
    let sock = raw, usedTLS = false, buf = "", state = "cmd", cur = {}, step = "";
    const say = (l) => { try { sock.write(l + "\r\n"); } catch { /* gone */ } };
    const wire = (s) => { s.on("data", (d) => { buf += d.toString("latin1"); pump(); }); s.on("error", () => {}); };
    if (implicit) { sock = new tls.TLSSocket(raw, { isServer: true, secureContext: ctx }); usedTLS = true; }
    wire(sock);
    say("220 fake.test ESMTP ready");
    function auth(b64) {
      const parts = Buffer.from(b64, "base64").toString().split("\0");
      if (parts.length === 3 && parts[1] === user && parts[2] === pass) { cur.authed = true; cur.authUser = parts[1]; say("235 ok"); }
      else { st.authFailures++; say("535 5.7.8 bad credentials"); }
    }
    function pump() {
      for (;;) {
        if (state === "data") {
          const i = buf.indexOf("\r\n.\r\n");
          const empty = buf.startsWith(".\r\n");
          if (i < 0 && !empty) return;
          const body = empty ? "" : buf.slice(0, i + 2);
          buf = empty ? buf.slice(3) : buf.slice(i + 5);
          state = "cmd";
          const raw = body.replace(/\r\n\./g, "\r\n").replace(/^\./, "");
          mails.push({ from: cur.from, to: cur.to, tls: usedTLS, authed: !!cur.authed, authUser: cur.authUser || "", raw, subject: header(raw, "Subject"), attachments: attachments(raw) });
          cur = { authed: cur.authed, authUser: cur.authUser };
          say("250 queued");
          continue;
        }
        const i = buf.indexOf("\r\n");
        if (i < 0) return;
        const line = buf.slice(0, i);
        buf = buf.slice(i + 2);
        if (step === "plain") { step = ""; auth(line.trim()); continue; }
        if (step === "login-user") { step = "login-pass"; cur.loginUser = Buffer.from(line.trim(), "base64").toString(); say("334 " + Buffer.from("Password:").toString("base64")); continue; }
        if (step === "login-pass") {
          step = "";
          const p = Buffer.from(line.trim(), "base64").toString();
          if (cur.loginUser === user && p === pass) { cur.authed = true; cur.authUser = cur.loginUser; say("235 ok"); } else { st.authFailures++; say("535 5.7.8 bad credentials"); }
          continue;
        }
        const up = line.toUpperCase();
        if (up.startsWith("EHLO") || up.startsWith("HELO")) {
          const l = ["fake.test greets you"];
          if (mode === "starttls" && !usedTLS) l.push("STARTTLS");
          if (usedTLS) l.push("AUTH PLAIN LOGIN");
          l.push("SIZE 52428800", "8BITMIME");
          l.forEach((x, n) => say("250" + (n === l.length - 1 ? " " : "-") + x));
        } else if (up === "STARTTLS" && mode === "starttls" && !usedTLS) {
          say("220 go ahead");
          sock.removeAllListeners("data");
          buf = "";
          sock = new tls.TLSSocket(sock, { isServer: true, secureContext: ctx });
          usedTLS = true;
          wire(sock);
        } else if (up.startsWith("AUTH PLAIN")) {
          const arg = line.slice(10).trim();
          if (arg) auth(arg); else { step = "plain"; say("334 "); }
        } else if (up.startsWith("AUTH LOGIN")) { step = "login-user"; say("334 " + Buffer.from("Username:").toString("base64")); }
        else if (up.startsWith("MAIL FROM:")) {
          if (st.refuse) { say(st.refuse); continue; }
          cur.from = line.slice(10).split(" ")[0].replace(/[<>]/g, ""); say("250 ok");
        } else if (up.startsWith("RCPT TO:")) { cur.to = line.slice(8).split(" ")[0].replace(/[<>]/g, ""); say("250 ok"); }
        else if (up === "DATA") { state = "data"; say("354 go"); }
        else if (up === "RSET" || up === "NOOP") say("250 ok");
        else if (up === "QUIT") { say("221 bye"); sock.end(); return; }
        else say("502 not implemented");
      }
    }
  }

  const server = net.createServer((raw) => serve(raw, mode === "tls"));
  await new Promise((res) => server.listen(0, "127.0.0.1", res));
  return {
    port: server.address().port,
    caPem: cert,
    mails,
    state: st,
    user,
    pass,
    async close() { await new Promise((res) => server.close(res)); },
  };
}
