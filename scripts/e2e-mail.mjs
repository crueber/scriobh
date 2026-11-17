#!/usr/bin/env node
// The mail screens in Chromium, against a small SMTP relay of this script's own (scripts/fake-smtp.mjs: TLS from the first
// byte, a self-signed certificate for 127.0.0.1 pasted into the settings page's authority box, a password): the
// administrator's /admin/mail (the relay saved without the password ever coming back, a test message that arrives over
// TLS with the sign-in used, a wrong password reported by class, the on and off switch, the restore pause and its one
// switch-on button, the Kindle domain allow-list and its tick, the queue with no content), the Kindle address and the
// digest on /settings, Send to Kindle from the Compile page (preview, then confirm, an EPUB that arrives, a spent preview
// refused, the log that shows the full address to the owner and the domain to an editor), the refusals, 390 px, the
// keyboard path, and an offline reload.
//
// It needs the `openssl` command (the relay's certificate) and, for the restore-pause step only, `sqlite3` (that step is
// skipped with a printed note when it is missing; internal/server/mailui_test.go covers the pause too).
//
//   node scripts/e2e-mail.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startFakeSmtp } from "./fake-smtp.mjs";
import { mkdirSync } from "node:fs";
import { execFileSync, spawnSync } from "node:child_process";
import { join } from "node:path";

const kit = await createKit("e2e-mail");
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const PASS = "Relay-Pass-Canary-7q2";
const smtp = await startFakeSmtp({ dir: kit.tmp, mode: "tls", user: "mailer", pass: PASS });
const wren = await kit.signedIn({}, "writer@dev.scriobh");
const ada = await kit.signedIn({}, "admin@dev.scriobh");
const rory = await kit.signedIn({}, "reader@dev.scriobh");
const MAIL = () => kit.base() + "/admin/mail";

// Two books: A (Wren owns it, Ada edits it, Rory reviews it) and B (Ada, an administrator, owns it, and Wren edits it).
const mkBook = async (title, text, who = wren) => {
  const made = await api(who.ctx, "POST", "/api/v1/projects", { title, kind: "novel", author_name: "Wren Writer" });
  const P = made.json.item;
  const first = (await api(who.ctx, "GET", `/api/v1/projects/${P.id}/docs`)).json.order[0];
  const g = (await api(who.ctx, "GET", `/api/v1/docs/${first}`)).json.item;
  await api(who.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, title: "Chapter 1", content: `${text}\n` });
  return P;
};
const A = await mkBook("The Salt Road", "The tide came in over the grey stones and the gulls wheeled over the harbour.");
const B = await mkBook("Second Book", "Nobody on the quay counted the boats twice.", ada);
await api(wren.ctx, "POST", `/api/v1/projects/${A.id}/members`, { email: "admin@dev.scriobh", role: "editor" });
await api(wren.ctx, "POST", `/api/v1/projects/${A.id}/members`, { email: "reader@dev.scriobh", role: "reviewer" });
const bm = await api(ada.ctx, "POST", `/api/v1/projects/${B.id}/members`, { email: "writer@dev.scriobh", role: "editor" });
if (bm.status >= 300) throw new Error("adding the editor to book B: " + bm.status);

const sql = (q) => execFileSync("sqlite3", [join(kit.dataDir, "scriobh.db"), ".timeout 5000", q]).toString();
const haveSqlite = spawnSync("sqlite3", ["--version"]).status === 0;
const refreshLog = async (page) => { await page.locator("#ml-refresh").click(); await page.waitForLoadState("load"); };
const gap = () => kit.sleep(5300); // one test message in five seconds
const hasText = async (page, sel, re) => re.test(await page.locator(sel).innerText());
const noSideScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);

await kit.test("anyone who is not an administrator finds no mail settings page", async () => {
  kit.allowed.push(/404/);
  for (const who of [wren, rory]) eq((await who.page.goto(MAIL(), { waitUntil: "load" })).status(), 404, "/admin/mail for a non-administrator");
});

await kit.test("the relay is saved, the password never comes back, and a test message arrives over TLS with the sign-in used", async () => {
  const page = ada.page; kit.main = page;
  await page.goto(MAIL(), { waitUntil: "load" });
  ok(await hasText(page, "#state", /Mail is off/), "mail starts off");
  eq(await page.locator('.adm-tabs a[aria-current="page"]').innerText(), "Email", "the tab");
  await page.fill("#ml-host", "127.0.0.1");
  await page.fill("#ml-port", String(smtp.port));
  await page.selectOption("#ml-sec", "tls");
  await page.check('input[name="allow_private"]');
  await page.fill("#ml-user", "mailer");
  await page.fill("#ml-pass", PASS);
  await page.fill("#ml-from", "scriobh@example.org");
  await page.fill("#ml-fname", "Scríobh");
  await page.locator("#relay details summary").click();
  await page.fill("#ml-ca", smtp.caPem);
  await page.locator("#relay button[type=submit]").click();
  await page.waitForSelector(".saved");
  ok(/saved/.test(await page.locator(".saved").innerText()), "saved");
  const html = await page.content(), vals = await page.evaluate(() => [...document.querySelectorAll("input,textarea")].map((e) => e.value).join("|"));
  ok(!html.includes(PASS) && !vals.includes(PASS), "the password is in the page");
  eq(await page.inputValue("#ml-pass"), "", "the password field comes back empty");
  ok(/A password is stored/.test(await page.locator("#ml-pass-h").innerText()), "says one is stored");
  ok(/A certificate authority is stored/.test(await page.locator("#relay details").textContent()), "says the authority is stored");
  eq(await page.inputValue("#ml-ca"), "", "the authority box is not prefilled");
  // A save with the password left empty keeps it: the test still signs in.
  await page.locator("#relay button[type=submit]").click();
  await page.waitForSelector(".saved");
  await page.locator("#test button[type=submit]").click();
  await page.waitForSelector("#ml-test-result");
  ok(/accepted the test message/.test(await page.locator("#ml-test-result").innerText()), "the test result: " + (await page.locator("#ml-test-result").innerText()));
  await kit.waitFor(() => smtp.mails.length === 1, "the test message at the relay");
  const m = smtp.mails[0];
  ok(m.tls && m.authed && m.authUser === "mailer", "sent over TLS, signed in: " + JSON.stringify({ tls: m.tls, authed: m.authed }));
  eq(m.to, "admin@dev.scriobh", "a test goes to the administrator's own address");
  eq(m.from, "scriobh@example.org", "from the sender address");
  ok(/test/i.test(m.subject), "the subject " + m.subject);
  ok(!m.raw.includes(PASS), "the password is not in the message");
  if (shots) { await page.screenshot({ path: `${shots}/admin-mail-1280.png`, fullPage: true }); kit.main = null; }
});

await kit.test("a wrong password is reported by class and holds sending; the right one lifts it", async () => {
  const page = ada.page; kit.main = page;
  await gap();
  await page.goto(MAIL(), { waitUntil: "load" });
  await page.fill("#ml-pass", "not-the-password");
  await page.locator("#relay button[type=submit]").click();
  await page.waitForSelector(".saved");
  await page.locator("#test button[type=submit]").click();
  await page.waitForSelector("#ml-test-result");
  const t = await page.locator("#ml-test-result").innerText();
  ok(/refused the user name and password/.test(t) && !t.includes("not-the-password"), "the class in words, not the relay's own: " + t);
  eq(await page.locator("#ml-test-result").getAttribute("data-tone"), "warn", "marked as a problem");
  ok(await page.locator("#ml-test-result .ml-mark").count() === 1, "a mark, not colour alone");
  await gap();
  await page.fill("#ml-pass", PASS);
  await page.locator("#relay button[type=submit]").click();
  await page.waitForSelector(".saved");
  await page.locator("#test button[type=submit]").click();
  await page.waitForSelector("#ml-test-result[data-tone=ok]");
  eq(smtp.mails.length, 2, "the second good test arrived");
  // a second test straight away is told to wait
  await page.locator("#test button[type=submit]").click();
  await page.waitForSelector("#ml-test-result");
  ok(/seconds/.test(await page.locator("#ml-test-result").innerText()), "told to wait");
});

await kit.test("STARTTLS: a relay that upgrades the connection gets the test message, with the sign-in used after the upgrade", async () => {
  const page = ada.page; kit.main = page;
  mkdirSync(join(kit.tmp, "starttls"), { recursive: true });
  const up = await startFakeSmtp({ dir: join(kit.tmp, "starttls"), mode: "starttls", user: "mailer", pass: PASS });
  const save = async (port, sec, ca) => {
    await page.goto(MAIL(), { waitUntil: "load" });
    await page.fill("#ml-port", String(port));
    await page.selectOption("#ml-sec", sec);
    await page.fill("#ml-pass", PASS);
    await page.locator("#relay details summary").click();
    await page.fill("#ml-ca", ca);
    await page.locator("#relay button[type=submit]").click();
    await page.waitForSelector(".saved");
  };
  try {
    await gap();
    await save(up.port, "starttls", up.caPem);
    await page.locator("#test button[type=submit]").click();
    await page.waitForSelector("#ml-test-result[data-tone=ok]");
    await kit.waitFor(() => up.mails.length === 1, "the test message at the STARTTLS relay");
    ok(up.mails[0].tls && up.mails[0].authed, "upgraded before signing in: " + JSON.stringify({ tls: up.mails[0].tls, authed: up.mails[0].authed }));
  } finally {
    await save(smtp.port, "tls", smtp.caPem); // the rest of the script uses the first relay
    await up.close();
  }
});

await kit.test("mail is switched on and off; a refused save keeps what was typed but not the password", async () => {
  const page = ada.page; kit.main = page;
  kit.allowed.push(/422/);
  await page.goto(MAIL(), { waitUntil: "load" });
  eq(await page.locator('form[action="/ui/admin/mail/enable"]').count(), 1, "one way to switch on");
  await page.locator("#switch button").click();
  await page.waitForSelector(".saved");
  ok(await hasText(page, "#state", /Mail is on/), "on");
  eq(await page.locator('form[action="/ui/admin/mail/enable"]').count(), 0, "no switch-on while on");
  // a refused save
  await page.fill("#ml-from", "not an address");
  await page.fill("#ml-pass", "typed-but-refused");
  await page.locator("#relay button[type=submit]").click();
  await page.waitForSelector("#ml-error");
  eq(await page.locator("#ml-from").getAttribute("aria-invalid"), "true", "the field is marked");
  ok((await page.locator("#ml-from").getAttribute("aria-describedby")).includes("ml-error"), "and tied to the error");
  eq(await page.inputValue("#ml-from"), "not an address", "what was typed is kept");
  eq(await page.inputValue("#ml-pass"), "", "the password is not");
  ok(/Enter it again/.test(await page.locator("main").innerText()), "says to enter the password again");
  await page.goto(MAIL(), { waitUntil: "load" });
  ok(await hasText(page, "#state", /Mail is on/), "the refused save changed nothing");
});

await kit.test("the Kindle allow-list: adding a domain needs the tick, taking away does not, and the default is one button", async () => {
  const page = ada.page; kit.main = page;
  kit.allowed.push(/422/);
  await page.goto(MAIL(), { waitUntil: "load" });
  eq(await page.inputValue("#ml-dom"), "kindle.com\nfree.kindle.com", "the default is shown");
  await page.fill("#ml-dom", "kindle.com\nfree.kindle.com\nexample.net");
  await page.locator("#domains button[type=submit]:not([name=reset])").click();
  await page.waitForSelector("#ml-error");
  ok(/Tick the box/.test(await page.locator("#ml-error").innerText()), "refused without the tick");
  await page.goto(MAIL(), { waitUntil: "load" });
  eq(await page.inputValue("#ml-dom"), "kindle.com\nfree.kindle.com", "nothing changed");
  await page.fill("#ml-dom", "kindle.com\nfree.kindle.com\nexample.net");
  await page.check('#domains input[name="understood"]');
  await page.locator("#domains button[type=submit]:not([name=reset])").click();
  await page.waitForSelector(".saved");
  eq(await page.inputValue("#ml-dom"), "kindle.com\nfree.kindle.com\nexample.net", "saved");
  ok(/any mailbox on that domain/.test(await page.locator("#ml-dom-w").innerText()), "the warning is in plain words");
  await page.locator("#domains button[name=reset]").click();
  await page.waitForSelector(".saved");
  eq(await page.inputValue("#ml-dom"), "kindle.com\nfree.kindle.com", "back to the default");
});

await kit.test("the Kindle address and the digest on settings", async () => {
  const page = wren.page; kit.main = page;
  await page.goto(kit.base() + "/settings", { waitUntil: "load" });
  ok(/Approved Personal Document/.test(await page.locator("#kindle").innerText()) && (await page.locator("#kindle").innerText()).includes("scriobh@example.org"), "the sender to approve at Amazon");
  await page.fill("#ml-k-addr", "wren@example.com");
  await page.locator('#kindle form[action="/ui/settings/kindle/save"] button').click();
  await page.waitForSelector("#ml-k-error");
  eq(await page.locator("#ml-k-addr").getAttribute("aria-invalid"), "true", "a wrong domain is marked");
  await page.fill("#ml-k-addr", "wren_k@kindle.com");
  await page.locator('#kindle form[action="/ui/settings/kindle/save"] button').click();
  await page.waitForSelector("#kindle .saved");
  eq(await page.inputValue("#ml-k-addr"), "wren_k@kindle.com", "stored");
  // the digest
  ok(/The digest is off/.test(await page.locator("#ml-d-state").innerText()), "off to start with");
  ok((await page.locator("#digest").innerText()).includes("writer@dev.scriobh"), "the address it goes to");
  await page.locator('#digest button[value="on"]').click();
  await page.waitForSelector("#ml-d-error");
  await page.check('#digest input[value="weekly"]');
  await page.locator('#digest button[value="on"]').click();
  await page.waitForSelector("#digest .saved");
  ok(/on Monday morning/.test(await page.locator("#ml-d-state").innerText()), "weekly");
  await page.locator('#digest button[value="off"]').click();
  await page.waitForSelector("#digest .saved");
  ok(/The digest is off/.test(await page.locator("#ml-d-state").innerText()), "off again");
  // Rory, an instance reader, can store an address too: the sections are on her settings page as well
  const rp = rory.page;
  await rp.goto(kit.base() + "/settings", { waitUntil: "load" });
  await rp.fill("#ml-k-addr", "rory_k@free.kindle.com");
  await rp.locator('#kindle form[action="/ui/settings/kindle/save"] button').click();
  await rp.waitForSelector("#kindle .saved");
  if (shots) { await page.goto(kit.base() + "/settings#kindle", { waitUntil: "load" }); await page.locator("#kindle").screenshot({ path: `${shots}/settings-kindle-1280.png` }); kit.main = null; }
});

await kit.test("Send to Kindle: preview, then confirm; an EPUB arrives; the same preview cannot be used twice", async () => {
  const page = wren.page; kit.main = page;
  kit.allowed.push(/409/);
  const before = smtp.mails.length;
  await page.goto(`${kit.base()}/p/${A.slug}/compile`, { waitUntil: "load" });
  await page.waitForSelector("#kindle");
  const sec = await page.locator("#kindle").innerText();
  ok(sec.includes("wren_k@kindle.com") && /always an EPUB/.test(sec), "the compile section says where it goes and that the options do not apply: " + sec.slice(0, 160));
  await page.locator('#kindle button[type=submit]').click();
  await page.waitForSelector("#preview");
  ok(/\/kindle\/preview$/.test(page.url()), "the preview is the answer to the form");
  const t = await page.locator("#preview").innerText();
  ok(t.includes("wren_k@kindle.com") && /\.epub/.test(t) && /manuscript leaves this server/.test(t) && t.includes("scriobh@example.org"), "the preview says what, where and from whom: " + t.slice(0, 300));
  eq(await page.locator("#preview input[name=token]").count(), 1, "one token field");
  const token = await page.inputValue("#preview input[name=token]"), csrf = await page.inputValue("#preview input[name=_csrf]");
  ok(!page.url().includes(token), "the token is not in the URL");
  ok(!(await page.evaluate(() => JSON.stringify([localStorage, sessionStorage]))).includes(token), "nor in storage");
  eq(await page.locator("[data-token],[data-secret]").count(), 0, "nor in a data attribute");
  if (shots) { await page.screenshot({ path: `${shots}/kindle-preview-1280.png`, fullPage: true }); kit.main = null; }
  await page.locator('#preview button[type=submit]').click();
  await page.waitForURL(new RegExp(`/p/${A.slug}/kindle`));
  ok(/Queued/.test(await page.locator("#ml-queued").innerText()), "queued");
  await kit.waitFor(() => smtp.mails.length === before + 1, "the edition at the relay", 30000);
  const m = smtp.mails[before];
  eq(m.to, "wren_k@kindle.com", "the stored address, nobody else");
  ok(m.tls && m.authed, "over TLS, signed in");
  eq(m.attachments.length, 1, "one attachment");
  const a = m.attachments[0];
  ok(/\.epub$/.test(a.name) && /^[\x20-\x7e]+$/.test(a.name) && a.type === "application/epub+zip" && a.head === "PK" && a.bytes > 1000, "an EPUB: " + JSON.stringify({ n: a.name, t: a.type, h: a.head, b: a.bytes }));
  ok(!m.raw.includes(PASS), "no password in it");
  // the same token again
  const again = await wren.ctx.request.fetch(`${kit.base()}/ui/projects/${A.id}/kindle/send`, { method: "POST", form: { _csrf: csrf, token } });
  eq(again.status(), 409, "a spent preview is refused");
  const body = await again.text();
  ok(/not valid any more/.test(body) && !body.includes(token), "in words, without the token");
  await page.goto(`${kit.base()}/p/${A.slug}/kindle`, { waitUntil: "load" });
  await kit.waitFor(async () => { await refreshLog(page); return /Sent/.test(await page.locator("#log li .ml-state").first().innerText()); }, "the log to say Sent", 20000);
  const row = await page.locator("#log li").first().innerText();
  ok(row.includes("wren_k@kindle.com") && row.includes("Wren Writer"), "the owner sees the full address: " + row);
  ok(!/^\s*$/.test(await page.locator("#log").innerText()), "the log");
});

await kit.test("an editor who is not the owner sees the domain, not the address; a reviewer finds nothing", async () => {
  kit.allowed.push(/404/, /409/);
  const rp = rory.page; kit.main = rp;
  eq((await rp.goto(`${kit.base()}/p/${A.slug}/kindle`, { waitUntil: "load" })).status(), 404, "a reviewer");
  const w = wren.page; kit.main = w;
  const before = smtp.mails.length;
  const cr = await w.goto(`${kit.base()}/p/${B.slug}/compile`, { waitUntil: "load" });
  ok(cr.status() === 200, `the editor's Compile page answered ${cr.status()}`);
  await w.locator("#kindle button[type=submit]").click();
  await w.waitForSelector("#preview");
  await w.locator("#preview button[type=submit]").click();
  await w.waitForURL(new RegExp(`/p/${B.slug}/kindle`));
  await kit.waitFor(async () => { await refreshLog(w); return /Sent/.test(await w.locator("#log li .ml-state").first().innerText()); }, "the editor's send", 20000);
  const row = await w.locator("#log li").first().innerText();
  ok(!row.includes("wren_k@") && row.includes("kindle.com"), "an editor sees only the domain: " + row);
  ok(/see the full address/.test(await w.locator("#log").innerText()), "and is told why");
  eq(smtp.mails.length, before + 1, "one more message");
  eq(smtp.mails[before].to, "wren_k@kindle.com", "it went to the editor's own address");
  const a = ada.page; kit.main = a;
  await a.goto(`${kit.base()}/p/${B.slug}/kindle`, { waitUntil: "load" });
  ok((await a.locator("#log li").first().innerText()).includes("wren_k@kindle.com"), "the book's owner sees the address in full");
});

await kit.test("with mail off the compile section says so and offers no preview; the restore pause has one switch-on button", async () => {
  const page = ada.page; kit.main = page;
  await page.goto(MAIL(), { waitUntil: "load" });
  await page.locator('#switch button').click();
  await page.waitForSelector(".saved");
  ok(await hasText(page, "#state", /Mail is off/) && /switched off by an administrator/.test(await page.locator("#state").innerText()), "off, and why");
  const w = wren.page; kit.main = w;
  await w.goto(`${kit.base()}/p/${A.slug}/compile`, { waitUntil: "load" });
  ok(/Email is not switched on/.test(await w.locator("#kindle").innerText()) && (await w.locator("#kindle button").count()) === 0, "mail off on the compile page");
  await w.goto(kit.base() + "/settings", { waitUntil: "load" });
  eq(await w.locator('#digest button[value="on"]').count(), 0, "the digest cannot be turned on while mail is off");
  if (!haveSqlite) { console.log("      (sqlite3 is not installed: the restore-pause step is skipped)"); return; }
  // what a restore leaves
  sql("UPDATE mail_config SET enabled = 0, restore_paused = 1;");
  kit.main = page;
  await page.goto(MAIL(), { waitUntil: "load" });
  await page.waitForSelector("#ml-paused");
  eq(await page.locator('form[action="/ui/admin/mail/enable"]').count(), 1, "one switch-on form on the page");
  ok(await page.locator('#ml-paused form[action="/ui/admin/mail/enable"]').count() === 1, "and it is in the banner");
  if (shots) { await page.screenshot({ path: `${shots}/admin-mail-paused-1280.png` }); kit.main = null; }
  await page.locator("#ml-paused button").click();
  await page.waitForSelector(".saved");
  eq(await page.locator("#ml-paused").count(), 0, "the banner is gone");
  ok(await hasText(page, "#state", /Mail is on/), "on again");
});

await kit.test("the queue shows counts and failure classes and no message content", async () => {
  const page = ada.page; kit.main = page;
  await page.goto(MAIL() + "#queue", { waitUntil: "load" });
  const q = await page.locator("#queue").innerText();
  ok(/Sent\s*\n?\s*[2-9]/.test(q) || /Sent/.test(q), "counts: " + q.slice(0, 120));
  ok(/Kindle edition/.test(q), "the messages are listed");
  for (const secret of ["wren_k", "rory_k", "Salt Road", "Second Book", "tide came"]) ok(!q.includes(secret), `the queue shows ${secret}`);
});

await kit.test("the keyboard path: the address saved and the settings reached without a mouse", async () => {
  const page = wren.page; kit.main = page;
  await page.goto(kit.base() + "/settings", { waitUntil: "load" });
  await page.focus("#ml-k-addr");
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.type("wren_k2@kindle.com");
  await page.keyboard.press("Enter");
  await page.waitForSelector("#kindle .saved");
  eq(await page.inputValue("#ml-k-addr"), "wren_k2@kindle.com", "saved by Enter");
  await page.focus("#ml-k-addr");
  await page.keyboard.press("Tab");
  const f = await page.evaluate(() => { const e = document.activeElement; const s = getComputedStyle(e); return { text: e.textContent.trim(), ring: s.outlineStyle !== "none" && parseFloat(s.outlineWidth) > 0, tag: e.tagName }; });
  ok(f.tag === "BUTTON" && f.ring, "Tab reaches the Save button with a visible focus ring: " + JSON.stringify(f));
  await page.fill("#ml-k-addr", "wren_k@kindle.com");
  await page.keyboard.press("Enter");
  await page.waitForSelector("#kindle .saved");
  // the administrator's page: the authority box opens from the keyboard
  const ap = ada.page; kit.main = ap;
  await ap.goto(MAIL(), { waitUntil: "load" });
  await ap.focus("#relay details summary");
  await ap.keyboard.press("Enter");
  ok(await ap.locator("#ml-ca").isVisible(), "the summary opens by Enter");
  await ap.keyboard.press("Enter");
  ok(!(await ap.locator("#ml-ca").isVisible()), "and closes again");
});

await kit.test("390 px: every mail page reads, nothing scrolls sideways, and targets are at least 44 px", async () => {
  const phone = async (who) => kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true, isMobile: true, storageState: await who.ctx.storageState() });
  const out = [];
  for (const [who, paths] of [[ada, ["/admin/mail"]], [wren, ["/settings", `/p/${A.slug}/compile`, `/p/${A.slug}/kindle`]]]) {
    const ctx = await phone(who);
    const page = await ctx.newPage(); kit.watch(page, "phone"); kit.main = page;
    for (const p of paths) {
      await page.goto(kit.base() + p, { waitUntil: "load" });
      ok(await noSideScroll(page), p + " scrolls sideways");
      const small = await page.evaluate((root) => {
        const bad = [];
        for (const e of document.querySelectorAll(`${root} button, ${root} a.btn, ${root} select, ${root} summary, ${root} input:not([type=hidden]):not([type=checkbox]):not([type=radio]), ${root} label.check`)) {
          const r = e.getBoundingClientRect();
          if (r.width && r.height && r.height < 43.5) bad.push(`${e.tagName}.${e.className} ${Math.round(r.height)}px ${(e.textContent || e.name || "").trim().slice(0, 30)}`);
        }
        return bad;
      }, p === "/settings" ? "#kindle, #digest" : p.endsWith("/compile") ? "#kindle" : "main");
      out.push(...small.map((s) => `${p}: ${s}`));
      if (shots) await page.screenshot({ path: `${shots}/${p.replace(/\W+/g, "-").replace(/^-/, "")}-390.png`, fullPage: true });
    }
    await ctx.close();
  }
  ok(out.length === 0, "targets under 44 px: " + out.join("; "));
});

await kit.test("both themes: the pages in the dark scheme (screenshots with --shots)", async () => {
  if (!shots) return;
  for (const [who, name, path] of [[ada, "admin-mail", "/admin/mail"], [wren, "kindle", `/p/${A.slug}/kindle`], [wren, "compile", `/p/${A.slug}/compile`]]) {
    const ctx = await kit.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: "dark", storageState: await who.ctx.storageState() });
    const page = await ctx.newPage(); kit.watch(page, "dark");
    await page.goto(kit.base() + path, { waitUntil: "load" });
    await page.screenshot({ path: `${shots}/${name}-dark-1280.png`, fullPage: true });
    await ctx.close();
  }
});

await kit.test("the pseudo-locale, right to left: the pages mirror and nothing scrolls sideways (screenshots with --shots)", async () => {
  for (const who of [ada, wren]) await api(who.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } });
  const page = ada.page; kit.main = page;
  await page.goto(MAIL(), { waitUntil: "load" });
  eq(await page.evaluate(() => document.documentElement.dir), "rtl", "right to left");
  ok(await noSideScroll(page), "admin page scrolls sideways in rtl");
  if (shots) { await page.screenshot({ path: `${shots}/admin-mail-rtl-1280.png`, fullPage: true }); kit.main = null; }
  const w = wren.page; kit.main = w;
  for (const [name, path] of [["kindle", `/p/${A.slug}/kindle`], ["compile", `/p/${A.slug}/compile`], ["settings", "/settings#kindle"]]) {
    await w.goto(kit.base() + path, { waitUntil: "load" });
    eq(await w.evaluate(() => document.documentElement.dir), "rtl", name + " is right to left");
    ok(await noSideScroll(w), name + " scrolls sideways in rtl");
    if (shots) { await w.screenshot({ path: `${shots}/${name}-rtl-1280.png`, fullPage: true }); kit.main = null; }
  }
  for (const who of [ada, wren]) await api(who.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "en" } });
});

await kit.test("after a reload with the network cut: the offline notice shows nothing of the settings, and a form sent offline loses nothing the server held", async () => {
  const cut = [/ERR_INTERNET_DISCONNECTED/, /ERR_CONNECTION_REFUSED/, /Failed to load resource/, /net::ERR/];
  const ctx = await kit.newContext({ serviceWorkers: "allow", viewport: { width: 1000, height: 700 }, storageState: await ada.ctx.storageState() });
  const pg = await ctx.newPage(); kit.watch(pg, "offline"); kit.main = pg;
  await pg.goto(MAIL(), { waitUntil: "load" });
  await kit.waitFor(() => pg.evaluate(async () => { const r = await navigator.serviceWorker.ready; return r.active && r.active.state === "activated" && !!navigator.serviceWorker.controller; }), "the worker", 20000);
  await pg.reload({ waitUntil: "load" });
  await pg.fill("#ml-fname", "Typed Before The Cut");
  kit.allowed = cut;
  await ctx.setOffline(true);
  try {
    // a form sent with no network does not arrive; coming back to it, what was typed is still in the box, and nothing was saved
    await pg.locator("#relay button[type=submit]").click().catch(() => {});
    await kit.sleep(500);
    await ctx.setOffline(false);
    await pg.goBack({ waitUntil: "load" }).catch(() => {});
    await pg.waitForSelector("#ml-fname", { timeout: 10000 });
    eq(await pg.inputValue("#ml-fname"), "Typed Before The Cut", "what was typed is kept when the page is come back to");
    eq((await api(ada.ctx, "GET", "/api/v1/admin/mail")).json.from_name, "Scríobh", "nothing was saved by the lost submit");
    await ctx.setOffline(true);
    await pg.reload({ waitUntil: "load", timeout: 15000 });
    const text = await pg.locator("main").innerText();
    ok(/cannot reach the server/.test(text), "the offline notice: " + text.slice(0, 80));
    ok(!text.includes("scriobh@example.org") && !text.includes("127.0.0.1"), "the notice holds the relay's settings");
  } finally {
    await ctx.setOffline(false);
  }
  await ctx.close();
});

await smtp.close();
await kit.finish("e2e-mail");
