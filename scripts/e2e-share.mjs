#!/usr/bin/env node
// Share links in Chromium: the owner makes a link on the management page (shown once), and a beta reader with the link and
// no account (a browser with no cookies) unlocks it, reads without ever seeing a setting, a navigation or the application,
// and leaves comments by name; a failed unlock is counted and slowed; a revoked link says nothing about why.
//
//   node scripts/e2e-share.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { mkdirSync } from "node:fs";

const kit = await createKit("e2e-share", { env: { SCRIOBH_SHARE_RATE_LIMIT: "0", SCRIOBH_SHARE_LINK_RATE: "0" } });
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const owner = await kit.signedIn({}, "writer@dev.scriobh");

const made = await api(owner.ctx, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Wren Writer" });
const P = made.json.item, pid = P.id, slug = P.slug;
const first = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
const sentence = (c, s) => `Sentence ${c}.${s} says the *tide* came in over the stones, and nobody on the quay counted the boats.`;
const body = (c) => Array.from({ length: 5 }, (_, p) => Array.from({ length: 3 }, (_, s) => sentence(c, p * 3 + s + 1)).join(" ")).join("\n\n") + "\n\n%%PRIVATE note for the writer only%%\n";
const chapters = [];
for (let c = 1; c <= 6; c++) {
  if (c === 1) { const g = (await api(owner.ctx, "GET", `/api/v1/docs/${first}`)).json.item; await api(owner.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, title: `Chapter ${c}`, content: body(c) }); chapters.push(first); }
  else chapters.push((await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: `Chapter ${c}`, kind: "chapter", content: body(c) })).json.item.id);
}
await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: "Plot notes SECRETPLOT", kind: "note", content: "SECRETPLOT is the twist.\n" });

const PASS = "correct horse battery";
let link = null;
const origin = () => new URL(kit.base()).origin;

await kit.test("the owner makes a link on the management page: shown once, with a copy button, never listed", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${kit.base()}/p/${slug}/share`, { waitUntil: "load" });
  await page.fill("#sl-label", "Beta reader Sam");
  await page.check('input[name="scope"][value="comment"]');
  await page.fill("#sl-pass", PASS);
  await page.check('input[name="download"]');
  await page.click('button[type="submit"]:has-text("Make the link")');
  await page.waitForSelector("#shl-once");
  eq(await page.evaluate(() => document.activeElement.id), "shl-url", "focus goes to the link that was made");
  eq(await page.locator("#shl-once-note").getAttribute("role"), "status", "and its note is announced");
  const url = await page.inputValue("#shl-url");
  ok(url.startsWith(origin() + "/s/shl_"), "the link carries this server's address: " + url);
  ok(await page.locator("#shl-copy").isVisible(), "a copy button");
  link = new URL(url).pathname;
  await page.goto(`${kit.base()}/p/${slug}/share`, { waitUntil: "load" });
  eq(await page.locator("#shl-once").count(), 0, "the link is not shown again");
  const html = await page.content();
  ok(!html.includes(link.split(".")[1]) && !html.includes(link), "the list holds no token");
  ok((await page.locator(".shl-link").first().innerText()).includes("Beta reader Sam"), "the link is listed by its label");
  if (shots) { await page.screenshot({ path: `${shots}/share-links-1280.png` }); kit.main = null; }
});

// a visitor with no account: a context with no cookies, and a record of every request
const visitor = async (viewport) => {
  const ctx = await kit.newContext({ viewport: viewport || { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  const requests = [];
  kit.watch(page, "visitor");
  page.on("request", (r) => requests.push({ url: r.url(), headers: r.headers() }));
  return { ctx, page, requests };
};
const text = (page) => page.evaluate(() => document.body.innerText);

await kit.test("a beta reader with the link and no account unlocks it and reads, and sees no settings", async () => {
  const v = await visitor(); const page = v.page; kit.main = page;
  kit.allowed.push(/401/); // the browser logs the probe of a locked link and the wrong passphrase: they are answers, not faults
  const shell = await page.goto(kit.base() + link, { waitUntil: "load" });
  eq(shell.status(), 200, "the page");
  eq(shell.headers()["referrer-policy"], "no-referrer", "no referrer");
  ok(/noindex/.test(shell.headers()["x-robots-tag"] || ""), "noindex");
  ok(!shell.headers()["set-cookie"], "no cookie");
  await page.waitForSelector("#sh-unlock-form", { state: "visible" });
  eq(await page.locator("#sh-main").isVisible(), false, "nothing of the book before the passphrase");
  const before = await text(page);
  ok(!before.includes("The Salt Road") && !before.includes("Sentence 1.1"), "the page before the passphrase holds no word of the book: " + before.slice(0, 80));
  await page.fill("#sh-pass", "not the passphrase");
  await page.press("#sh-pass", "Enter");
  await page.waitForSelector("#sh-unlock-err", { state: "visible" });
  ok((await page.locator("#sh-unlock-err").innerText()).includes("not right"), "a wrong passphrase says so");
  eq(await page.locator("#sh-pass").getAttribute("aria-invalid"), "true", "the field is marked invalid");
  eq(await page.locator("#sh-pass").getAttribute("aria-describedby"), "sh-unlock-err", "and points at the error");
  await page.fill("#sh-pass", PASS);
  eq(await page.locator("#sh-pass").getAttribute("aria-invalid"), null, "typing again clears the mark");
  await page.press("#sh-pass", "Enter");
  await page.waitForSelector("#sh-main", { state: "visible" });
  await kit.waitFor(async () => (await page.evaluate(() => document.activeElement.id)) === "sh-title", "focus is on the title once the text opens, not dropped with the form");
  eq(await page.locator("#sh-title").innerText(), "The Salt Road", "the title");
  eq(await page.locator("#sh-toc-list li").count(), 6, "six chapters in the contents, no note");
  await kit.waitFor(async () => (await page.locator("section.chapter").count()) >= 1, "the first chapter");
  const t = await text(page);
  ok(t.includes("Sentence 1.1 says the tide came"), "the text is there");
  ok(!/PRIVATE note|SECRETPLOT|Settings|Sign out|Library/.test(t), "nothing private and no settings or library: " + t.slice(0, 300));
  const links = await page.evaluate(() => [...document.querySelectorAll("a[href]")].map((a) => a.getAttribute("href")).filter((h) => !h.startsWith("#") && h !== "#"));
  eq(links.length, 0, "no link leaves the page: " + links.join(", "));
  eq(await page.evaluate(() => document.cookie), "", "no cookie");
  eq((await v.ctx.cookies()).length, 0, "no cookie in the browser");
  const proof = await page.evaluate(() => Object.keys(sessionStorage).concat(Object.keys(localStorage)));
  ok(proof.some((k) => k.startsWith("scriobh.share.")), "the proof is in this tab's session storage");
  eq(await page.evaluate(() => Object.keys(localStorage).length), 0, "nothing in local storage");
  for (const r of v.requests) {
    ok(r.url.startsWith(origin()), "an outside request " + r.url);
    ok(!r.headers.cookie, `a cookie went with ${r.url}`);
    ok(!r.headers.referer || (/\/static\//.test(r.headers.referer) && !/\/s\//.test(r.headers.referer)), `a referrer naming the link went with ${r.url}: ${r.headers.referer}`);
    ok(!/\/(settings|p\/|ui\/|auth\/|api\/v1\/)/.test(new URL(r.url).pathname.replace(/^\/s\/[^/]+/, "")), "a request into the application: " + r.url);
  }
  await page.close();
});

await kit.test("chapters load as they are reached; the whole text can be loaded; a keyboard can reach everything", async () => {
  const v = await visitor(); const page = v.page; kit.main = page;
  kit.allowed.push(/401/);
  await page.goto(kit.base() + link, { waitUntil: "load" });
  await page.fill("#sh-pass", PASS); await page.press("#sh-pass", "Enter");
  await page.waitForSelector("#sh-main", { state: "visible" });
  await kit.waitFor(async () => (await page.locator("section.chapter").count()) >= 1, "first chapter");
  ok((await page.locator("section.chapter").count()) < 6, "not every chapter at once");
  // follow a contents entry by keyboard: Tab to it, Enter, and the chapter is loaded and focused
  await page.locator("#sh-toc-list a").nth(4).focus();
  await page.keyboard.press("Enter");
  await kit.waitFor(async () => (await page.evaluate(() => document.activeElement && document.activeElement.id)) === "c-" + chapters[4], "focus on chapter 5");
  ok((await page.locator("section.chapter").count()) >= 5, "chapters before it were loaded too");
  if (await page.locator("#sh-all").isVisible()) await page.getByRole("button", { name: "Load the whole text" }).click();
  await kit.waitFor(async () => (await page.locator("section.chapter").count()) === 6, "all six");
  ok(await page.locator("#sh-dl").isVisible(), "the download is offered on this link");
  const [dl] = await Promise.all([page.waitForEvent("download"), page.locator("#sh-dl").click()]);
  const path = await dl.path();
  const { readFileSync } = await import("node:fs");
  const md = readFileSync(path, "utf8");
  ok(md.includes("## Chapter 1") && md.includes("Sentence 6.15") && !md.includes("PRIVATE note") && !md.includes("SECRETPLOT"), "the download is the shown text");
  await page.close();
});

await kit.test("a guest comments by name on a selection and by keyboard; the writer sees it as Guest: name", async () => {
  const v = await visitor(); const page = v.page; kit.main = page;
  kit.allowed.push(/401/);
  await page.goto(kit.base() + link, { waitUntil: "load" });
  await page.fill("#sh-pass", PASS); await page.press("#sh-pass", "Enter");
  await page.waitForSelector("#sh-name", { state: "visible" });
  await kit.waitFor(async () => (await page.locator("section.chapter").count()) >= 1, "chapter");
  // no name yet: the comment asks for one
  await page.evaluate(() => {
    const p = document.querySelector(".r-prose p"), t = p.firstChild, i = t.nodeValue.indexOf("Sentence 1.2");
    const r = document.createRange(); r.setStart(t, 0); r.setEnd(p.lastChild.nodeType === 3 ? p.lastChild : p.lastChild.firstChild, 12); getSelection().removeAllRanges(); getSelection().addRange(r);
    void i;
  });
  await page.waitForSelector("#sh-pop-b", { state: "visible" });
  await page.click("#sh-pop-b");
  await page.waitForSelector("#sh-new");
  await page.fill("#sh-new", "I stumbled over this opening.");
  await page.keyboard.press("ControlOrMeta+Enter");
  ok(await page.evaluate(() => document.activeElement.id) === "sh-name", "the missing name takes focus");
  await page.fill("#sh-name", "Sam B");
  await page.fill("#sh-new", "I stumbled over this opening.");
  await page.keyboard.press("ControlOrMeta+Enter");
  await kit.waitFor(async () => (await page.locator(".sh-thread").count()) === 1, "the thread is listed");
  ok((await page.locator(".sh-thread").innerText()).includes("Guest: Sam B (you): I stumbled"), "labelled as a guest of the link, and as this tab's own: " + await page.locator(".sh-thread").innerText());
  ok(await page.evaluate(() => (document.activeElement.id || "").startsWith("th-")), "focus is on the new comment's thread");
  ok((await page.locator("mark.sh-mark").count()) >= 1, "the words are marked");
  // by keyboard: place a cursor, select words, comment on them
  await page.locator("body").press("Escape");
  await page.evaluate(() => { document.activeElement && document.activeElement.blur(); });
  await page.keyboard.press("]");
  await page.keyboard.press("]");
  await page.keyboard.press("Alt+Shift+ArrowRight");
  await page.keyboard.press("Alt+Shift+ArrowRight");
  await page.keyboard.press("c");
  await page.waitForSelector("#sh-new");
  await page.keyboard.type("A second thought, by keyboard.");
  await page.keyboard.press("ControlOrMeta+Enter");
  await kit.waitFor(async () => (await page.locator(".sh-thread").count()) === 2, "the second thread");
  // the writer sees both, as a guest, anchored; the owner's reply comes back to the guest
  const all = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=20`)).json;
  eq(all.total, 2, "two threads in the writer's list");
  ok(all.items.every((t) => t.author.kind === "guest" && t.author.name.includes("Sam B")), "named as a guest: " + JSON.stringify(all.items.map((t) => t.author)));
  ok(all.items.every((t) => t.anchor && t.anchor.state === "active"), "anchored");
  const reply = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/comments`, { parent_id: all.items[0].id, body: "Thank you, I will rework it." });
  ok(reply.status === 201, "the writer replies " + reply.status);
  await page.reload({ waitUntil: "load" });
  await page.waitForSelector("#sh-main", { state: "visible" });
  await kit.waitFor(async () => (await page.locator(".sh-thread").count()) === 2, "threads after a reload (the proof was kept)");
  ok((await page.locator("#sh-threads").innerText()).includes("I will rework it"), "the writer's reply shows");
  eq((await v.ctx.cookies()).length, 0, "still no cookie");
  if (shots) {
    await page.screenshot({ path: `${shots}/share-1280.png` });
    await page.emulateMedia({ colorScheme: "dark" });
    await page.screenshot({ path: `${shots}/share-1280-dark.png` });
    kit.main = null; // a screenshot leaves empty style attributes of its own on the page
  }
  await page.close();
});

await kit.test("guests: another guest's comment is not 'You', a draft survives a reload, an offline open is an alert not a dead link", async () => {
  const mk = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/shares`, { label: "two guests", scope: "comment" });
  ok(mk.status === 201, "link " + mk.status);
  const path = mk.json.path;
  const a = await visitor(); const b = await visitor();
  for (const [v, name] of [[a, "Ann"], [b, "Bo"]]) {
    kit.main = v.page;
    await v.page.goto(kit.base() + path, { waitUntil: "load" });
    await v.page.waitForSelector("#sh-main", { state: "visible" });
    await v.page.fill("#sh-name", name);
  }
  // Ann comments on a selection
  const sel = (pg) => pg.evaluate(() => { const b = document.querySelector("#sh-book .r-prose p"); const r = document.createRange(); r.setStart(b.firstChild, 0); r.setEnd(b.firstChild, 12); const s = getSelection(); s.removeAllRanges(); s.addRange(r); b.scrollIntoView({ block: "center" }); });
  await kit.waitFor(async () => (await a.page.locator("#sh-book .r-prose p").count()) > 0, "text");
  await sel(a.page);
  await a.page.waitForSelector("#sh-pop-b", { state: "visible" });
  await a.page.click("#sh-pop-b");
  await a.page.fill("#sh-new", "Ann's draft, half written");
  // the page is reloaded before it is sent: the draft is in the box again
  await a.page.reload({ waitUntil: "load" });
  await a.page.waitForSelector("#sh-new");
  eq(await a.page.inputValue("#sh-new"), "Ann's draft, half written", "the unsent comment survives a reload");
  await a.page.keyboard.press("ControlOrMeta+Enter");
  await kit.waitFor(async () => (await a.page.locator(".sh-thread").count()) === 1, "Ann's thread");
  await b.page.reload({ waitUntil: "load" });
  await b.page.waitForSelector("#sh-main", { state: "visible" });
  await kit.waitFor(async () => (await b.page.locator(".sh-thread").count()) === 1, "Bo sees the thread made through the link");
  const bt = await b.page.locator(".sh-thread").innerText();
  ok(bt.includes("Guest: Ann") && !bt.includes("(you)"), "Bo does not see Ann's comment as his own: " + bt);
  await a.page.reload({ waitUntil: "load" });
  await kit.waitFor(async () => (await a.page.locator(".sh-thread").count()) === 1, "Ann's thread after reload");
  ok((await a.page.locator(".sh-thread").innerText()).includes("Guest: Ann (you)"), "Ann's own stays hers in her tab");
  // the sent draft is gone
  await a.page.reload({ waitUntil: "load" });
  await a.page.waitForSelector("#sh-main", { state: "visible" });
  eq(await a.page.locator("#sh-new").count(), 0, "a sent comment leaves no draft behind");
  // offline when the page opens: it says so where it is heard, with a way to try again; it does not call the link gone
  const c = await visitor(); kit.main = c.page;
  kit.allowed.push(/ERR_FAILED/); // the aborted request is the point of the step
  await c.ctx.route("**/api/manifest", (r) => r.abort());
  await c.page.goto(kit.base() + path, { waitUntil: "load" });
  await c.page.waitForSelector("#sh-loading[role=alert]");
  ok((await c.page.locator("#sh-loading").innerText()).includes("Could not reach the server"), "offline is named");
  eq(await c.page.locator("#sh-gone").isVisible(), false, "and the link is not called gone");
  await c.ctx.unroute("**/api/manifest");
  await c.page.getByRole("button", { name: "Try again" }).click();
  await c.page.waitForSelector("#sh-main", { state: "visible" });
  for (const v of [a, b, c]) await v.ctx.close();
});

await kit.test("a long contents list is folded, not cut off; the phone's passphrase box is a full target", async () => {
  const mkb = await api(owner.ctx, "POST", "/api/v1/projects", { title: "Long Book", kind: "novel" });
  const lp = mkb.json.item.id;
  for (let c = 1; c <= 14; c++) await api(owner.ctx, "POST", `/api/v1/projects/${lp}/docs`, { title: `Part ${c}`, kind: "chapter", content: c === 1 ? "Text of part 1.\n\n- a bullet\n- another\n\n1. one\n2. two\n" : `Text of part ${c}.\n` });
  const lk = await api(owner.ctx, "POST", `/api/v1/projects/${lp}/shares`, { label: "long", scope: "read", passphrase: PASS });
  const v = await visitor({ width: 390, height: 800 }); const page = v.page; kit.main = page;
  kit.allowed.push(/401/);
  await page.goto(kit.base() + lk.json.path, { waitUntil: "load" });
  await page.waitForSelector("#sh-pass", { state: "visible" });
  const box = await page.locator("#sh-pass").boundingBox();
  ok(box.height >= 44, "the passphrase box is at least 44 px: " + box.height);
  await page.fill("#sh-pass", PASS); await page.press("#sh-pass", "Enter");
  await page.waitForSelector("#sh-main", { state: "visible" });
  await kit.waitFor(async () => (await page.locator("#sh-book ul").count()) === 1, "the first part with its list");
  const kinds = await page.evaluate(() => [...document.querySelectorAll("#sh-book ul, #sh-book ol")].map((e) => getComputedStyle(e).listStyleType));
  eq(kinds.join(), "disc,decimal", "a list in the text has its markers on the shared page");
  eq(await page.locator("#sh-toc-d").evaluate((e) => e.open), false, "a long contents list is folded");
  await page.locator("#sh-toc-d summary").click();
  const ol = await page.locator("#sh-toc-list").evaluate((e) => ({ scroll: e.scrollHeight, client: e.clientHeight }));
  ok(ol.scroll <= ol.client + 1, "opened, it is all there, not behind an invisible scroll: " + JSON.stringify(ol));
  await v.ctx.close();
});

await kit.test("Ctrl+P on a link's page loads every chapter before it prints", async () => {
  const mk = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/shares`, { label: "print", scope: "read" });
  const v = await visitor(); const page = v.page; kit.main = page;
  await page.goto(kit.base() + mk.json.path, { waitUntil: "load" });
  await page.waitForSelector("#sh-main", { state: "visible" });
  await kit.waitFor(async () => (await page.locator("section.chapter").count()) >= 1, "the first chapter");
  const before = await page.locator("section.chapter").count();
  await page.evaluate(() => { window.__printed = 0; window.print = () => { window.__printed++; }; });
  await page.keyboard.press("Control+p");
  await kit.waitFor(async () => (await page.evaluate(() => window.__printed)) >= 1, "the print was asked for once the text is in");
  eq(await page.locator("section.chapter").count(), 6, "all six chapters were loaded first (it began with " + before + ")");
  // A window whose print request does nothing (the desktop application's webview; here, the stand-in above, which opens no
  // panel and sends no print event): the page says where printing is.
  await kit.waitFor(async () => /Printing did not start/.test(await page.locator("#sh-live").innerText()), "the page to say that printing did not start");
  await v.ctx.close();
});

await kit.test("a failed passphrase is counted and slowed, for that client only", async () => {
  const v = await visitor(); const page = v.page; kit.main = page;
  kit.allowed.push(/40[14]|429/);
  await page.goto(kit.base() + link, { waitUntil: "load" });
  for (let i = 0; i < 5; i++) { await page.fill("#sh-pass", "wrong " + i); await page.press("#sh-pass", "Enter"); await kit.waitFor(async () => (await page.locator("#sh-unlock-err").isVisible()), "an answer"); await kit.sleep(120); }
  await kit.waitFor(async () => /wait|Too many/.test(await page.locator("#sh-unlock-err").innerText()), "the wait is said");
  const err = await page.locator("#sh-unlock-err").innerText();
  ok(/Wait \d+ seconds/.test(err), "how long: " + err);
  // even the right passphrase waits, for this client
  await page.fill("#sh-pass", PASS); await page.press("#sh-pass", "Enter");
  await kit.waitFor(async () => /Wait \d+ seconds/.test(await page.locator("#sh-unlock-err").innerText()), "still waiting");
  eq(await page.locator("#sh-main").isVisible(), false, "the book stays closed");
  const links = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/shares`)).json.items;
  const usage = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/shares/${links.find((l) => l.label === "Beta reader Sam").id}/usage`)).json;
  ok(usage.failed_unlocks >= 5 && usage.locked_clients >= 1, "the owner sees it: " + JSON.stringify(usage));
  await page.close();
});

await kit.test("390 px: reads and comments without sideways scroll; print drops the controls", async () => {
  const v = await visitor({ width: 390, height: 800 }); const page = v.page; kit.main = page;
  await kit.sleep(100);
  // the earlier lock is for the client address: use a link with no passphrase for the phone
  const mk = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/shares`, { label: "phone", scope: "comment" });
  const plink = mk.json.path;
  await page.goto(kit.base() + plink, { waitUntil: "load" });
  await page.waitForSelector("#sh-main", { state: "visible" });
  await kit.waitFor(async () => (await page.locator("section.chapter").count()) >= 1, "chapter");
  ok(!(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)), "no sideways scroll at 390");
  if (!(await page.locator("#sh-toc-d").evaluate((e) => e.open))) await page.locator("#sh-toc-d summary").click(); // a long list starts folded on a phone
  let box = null;
  await kit.waitFor(async () => { box = await page.locator("#sh-toc-list a").first().boundingBox(); return box && box.height > 0; }, "a contents entry on the page");
  ok(box.height >= 30, "a contents entry is tall enough to touch: " + box.height);
  if (shots) { await page.screenshot({ path: `${shots}/share-390.png` }); }
  await page.emulateMedia({ media: "print" });
  eq(await page.locator(".sh-tools").isVisible(), false, "the controls are not printed");
  eq(await page.locator("#sh-toc").isVisible(), false, "nor the contents");
  ok(await page.locator("section.chapter").first().isVisible(), "the text is");
  kit.main = null;
  await page.close();
});

await kit.test("a revoked link, a wrong link and a link to nothing show the same page, which says nothing about why", async () => {
  const v = await visitor(); const page = v.page; kit.main = page;
  kit.allowed.push(/404/);
  const links = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/shares`)).json.items;
  const target = links.find((l) => l.label === "phone");
  const mk2 = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/shares`, { label: "to revoke" });
  const path2 = mk2.json.path;
  // revoke from the management page
  await owner.page.goto(`${kit.base()}/p/${slug}/share`, { waitUntil: "load" });
  await owner.page.locator(".shl-link", { hasText: "to revoke" }).locator("summary").click();
  const rev = owner.page.locator(".shl-link", { hasText: "to revoke" });
  await rev.getByRole("button", { name: "Revoke this link" }).click();
  await owner.page.waitForSelector(".shl-link .arm");
  eq(await owner.page.evaluate(() => document.activeElement.textContent), "Cancel", "the question opens on Cancel");
  await owner.page.keyboard.press("Escape");
  eq(await rev.locator(".arm").count(), 0, "Escape puts the question away");
  eq(await owner.page.evaluate(() => document.activeElement.textContent), "Revoke this link", "focus returns to the button");
  await rev.getByRole("button", { name: "Revoke this link" }).click();
  await owner.page.keyboard.press("Enter"); await kit.sleep(60); await owner.page.keyboard.press("Enter"); await kit.sleep(700);
  eq((await api(owner.ctx, "GET", `/api/v1/projects/${pid}/shares`)).json.items.find((x) => x.label === "to revoke").status, "active", "a double Enter does not revoke it");
  await rev.getByRole("button", { name: "Revoke this link" }).click();
  await owner.page.waitForSelector(".shl-link .arm-ok:not([disabled])");
  await rev.locator(".arm-ok").click();
  await owner.page.waitForSelector(".saved");
  void target;
  const gone = [];
  for (const p of [path2, "/s/shl_00000000000000000000000000.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "/s/garbage"]) {
    const r = await page.goto(kit.base() + p, { waitUntil: "load" });
    eq(r.status(), 404, p + " is a 404");
    gone.push(await page.evaluate(() => document.documentElement.outerHTML.replace(/\s+/g, " ")));
    ok((await text(page)).includes("This link is not available"), "the message");
  }
  eq(gone[0], gone[1], "revoked and unknown are the same page");
  eq(gone[1], gone[2], "unknown and garbage are the same page");
  ok(!/revoked|expired|passphrase|The Salt Road/.test(gone[0]), "the page explains nothing");
  // a link that is revoked while it is open: the next request says the same
  await page.close();
});

await kit.finish("e2e-share");
