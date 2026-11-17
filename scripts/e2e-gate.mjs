#!/usr/bin/env node
// The project passphrase in Chromium (F-252): the owner sets one on the page and by keyboard; a member who has not
// unlocked it is sent to the unlock page; a page that is open when the project locks (another tab, Lock now, the idle
// limit) covers itself, shows no word of the manuscript, keeps what was typed and saves it when the person unlocks, with
// the ordinary conflict choice if the chapter changed meanwhile; the wait after wrong tries is a time in words; the cover
// and both pages work at 390 px, by keyboard, offline, without script and mirrored.
//
//   node scripts/e2e-gate.mjs [--shots DIR] [--only NAME] [--no-idle]
import { armAnswer, armOk, createKit, eq, ok } from "./e2e-kit.mjs";
import { mkdirSync } from "node:fs";

const kit = await createKit("e2e-gate");
kit.deadline(8 * 60 * 1000);
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
if (process.argv.includes("--only")) kit.only = process.argv[process.argv.indexOf("--only") + 1];
const { api, waitFor } = kit;
const MOD = kit.MOD;

const owner = await kit.signedIn({}, "writer@dev.scriobh");          // Wren, owner
const editor = await kit.signedIn({}, "admin@dev.scriobh");          // Ada, an editor
const made = await api(owner.ctx, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Wren Writer" });
const P = made.json.item, pid = P.id, slug = P.slug;
const first = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
const g = (await api(owner.ctx, "GET", `/api/v1/docs/${first}`)).json.item;
const OPENING = "Zebra harbour opens the first chapter with a quiet tide.";
await api(owner.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, title: "Chapter One Zebra", content: OPENING + "\n" });
const second = (await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: "Chapter Two", kind: "chapter", content: "Second chapter words.\n" })).json.item.id;
const mem = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/members`, { email: "admin@dev.scriobh", role: "editor" });
ok(mem.status < 300, "add the editor: " + mem.status);

const PASS = "correct horse battery";
const base = kit.base();
const doc = (ctx, id = first) => api(ctx, "GET", `/api/v1/docs/${id}`).then((r) => r.json.item);
const text = (page) => page.evaluate(() => document.body.innerText);
const state = (page) => page.locator("#save-state").getAttribute("data-state");
const coverOpen = (page) => page.evaluate(() => !!document.getElementById("gate-cover")?.open);
const hscroll = (page) => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
async function openChapter(page, id = first) {
  await page.goto(`${base}/p/${slug}/d/${id}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose", { timeout: 10000 });
  await kit.sleep(250);
  // Another person's tab holds the chapter (the lease): take it over, as a person would.
  if (await page.locator("#nb-lock button").count()) { await page.click("#nb-lock button"); await page.waitForSelector("#nb-lock", { state: "detached" }); }
}
async function typeAtEnd(page, s) {
  await page.locator(".scriobh-prose").click();
  await page.keyboard.press(`${MOD}+End`);
  await page.keyboard.type(s, { delay: 3 });
}
const onUnlockPage = (page) => new URL(page.url()).pathname.endsWith("/unlock");
// A page that locks leaves for the unlock page (its text has gone from it), or, where that page cannot be reached, shows the cover.
async function waitLocked(page, ms = 15000) { await waitFor(async () => onUnlockPage(page), "the unlock page", ms); await page.waitForLoadState("load"); }
async function waitCover(page, ms = 15000) { await waitFor(() => coverOpen(page).catch(() => false), "the cover", ms); }
// The way to the unlock page cannot be reached (the network is cut, as far as that page goes): the cover stays.
const holdCover = (page) => page.route("**/unlock?next=*", (r) => r.abort());
const freeCover = (page) => page.unroute("**/unlock?next=*");
async function backAtChapter(page) {
  await page.waitForSelector(".scriobh-prose", { timeout: 15000 });
  await kit.sleep(400);
  // A tab that was closed under the lock still holds the chapter for a minute and a half (its release was refused while locked): take it over, as a person would.
  if (await page.locator("#nb-lock button").count()) { await page.click("#nb-lock button"); await page.waitForSelector("#nb-lock", { state: "detached" }); }
}
async function restoreDraft(page) {
  await page.waitForSelector("#nb-draft", { timeout: 10000 });
  await page.click('#nb-draft button:has-text("Restore your unsaved text")');
  await kit.sleep(400); // the restore hands the text over; Saved can be read just before it does
}
const lockElsewhere = () => api(owner.ctx, "POST", `/api/v1/projects/${pid}/lock`);
const chapterURL = `${base}/p/${slug}/d/${first}`;
// The page's text is not in it: nothing of these needles in the document the browser holds (what DevTools and Save page as read).
async function assertNoText(page, needles, what) {
  const html = await page.content();
  for (const n of needles) ok(!html.includes(n), `"${n}" is still in the page (${what})`);
}
async function waitSaved(page, ms = 10000) {
  await waitFor(async () => (await state(page)) === "saved" && !(await page.evaluate(() => window.scriobh.write.unsaved())), "Saved with nothing unsaved", ms);
}
async function unlockOnCover(page, pass) {
  await page.fill("#gate-cover-pass", pass);
  await page.press("#gate-cover-pass", "Enter");
}
async function unlockAny(page, pass) {
  if (onUnlockPage(page)) { await page.fill("#gate-pass", pass); await page.keyboard.press("Enter"); } else await unlockOnCover(page, pass);
}
// What a person at the screen could read: the text of everything that is not hidden.
async function visibleText(page) { return (await text(page)).replace(/\s+/g, " "); }

// Tests after the first need the passphrase; when one is run alone (--only) it is set here, through the op.
async function ensure() {
  const st = await api(owner.ctx, "GET", `/api/v1/projects/${pid}/passphrase`);
  if (!st.json.item.protected) { const r = await api(owner.ctx, "PUT", `/api/v1/projects/${pid}/passphrase`, { passphrase: PASS, idle_minutes: 30 }); ok(r.status === 200, "set: " + r.status); }
}

await kit.test("the owner sets a passphrase on its page, by keyboard, and reads what it does and does not protect", async () => {
  const page = owner.page; kit.main = page;
  kit.allowed.push(/422/); // the mismatch is answered 422 by the form
  await page.goto(`${base}/p/${slug}/passphrase`, { waitUntil: "load" });
  const t = await visibleText(page);
  for (const w of ["It is not encryption", "It keeps out", "It does not keep out", "the server's administrator", "Git pushes", "backups", "a borrowed laptop", "the drafts this browser keeps", "developer tools", "not encrypted"]) ok(t.includes(w), `the page says "${w}"`);
  eq(await page.locator("h1").count(), 1, "one h1");
  const levels = await page.evaluate(() => [...document.querySelectorAll("main h1, main h2, main h3")].map((h) => +h.tagName[1]));
  ok(levels.every((l, i) => i === 0 || l - levels[i - 1] <= 1), "headings do not skip a level: " + levels);
  ok(!(await page.locator('form[action$="/passphrase/clear"]').count()), "no remove form while there is no passphrase");
  // the two boxes must agree
  await page.fill("#ps-new", PASS);
  await page.fill("#ps-rep", PASS + "x");
  await page.fill("#ps-idle", "30");
  await page.press("#ps-idle", "Enter");
  await page.waitForSelector("#pass-err-set");
  ok((await page.locator("#pass-err-set").innerText()).includes("not the same"), "the mismatch is said");
  eq(await page.locator("#ps-rep").getAttribute("aria-invalid"), "true", "and tied to its field");
  eq(await page.inputValue("#ps-new"), "", "a typed passphrase does not come back");
  // by keyboard: the new field, the repeat, enter
  await page.focus("#ps-new");
  await page.keyboard.type(PASS);
  await page.keyboard.press("Tab");
  await page.keyboard.type(PASS);
  await page.keyboard.press("Enter");
  await page.waitForSelector(".saved");
  ok((await page.locator(".saved").innerText()).includes("saved"), "saved");
  ok((await visibleText(page)).includes("It is open in this browser"), "this browser stays open");
  ok(await page.locator('form[action$="/passphrase/clear"]').count(), "the remove form is there now");
  const st = await api(owner.ctx, "GET", `/api/v1/projects/${pid}/passphrase`);
  eq(st.json.item.protected, true, "the server says it is protected");
  ok(!JSON.stringify(st.json).includes(PASS), "the passphrase is nowhere in the answer");
  if (shots) await page.screenshot({ path: `${shots}/gate-settings-1280.png`, fullPage: true });
  kit.main = null;
});

await kit.test("a member who has not unlocked is sent to the unlock page: the title, no text, a wrong passphrase said in words", async () => {
  await ensure();
  const page = editor.page; kit.main = page;
  kit.allowed.push(/422/); // a wrong passphrase is answered 422
  await page.goto(`${base}/p/${slug}/d/${first}`, { waitUntil: "load" });
  ok(new URL(page.url()).pathname.endsWith("/unlock"), "sent to the unlock page: " + page.url());
  const t = await visibleText(page);
  ok(t.includes("The Salt Road"), "the title");
  ok(!t.includes("Zebra") && !t.includes("Chapter"), "no word of the manuscript: " + t.slice(0, 200));
  eq(await page.evaluate(() => document.activeElement && document.activeElement.id), "gate-pass", "focus is in the passphrase box");
  await page.fill("#gate-pass", "not the passphrase");
  await page.keyboard.press("Enter");
  await page.waitForSelector("#gate-err");
  eq(await page.locator("#gate-err").getAttribute("role"), "alert", "the error is announced");
  ok((await page.locator("#gate-err").innerText()).includes("not right"), "in words");
  ok((await page.locator("#gate-pass").getAttribute("aria-describedby")).includes("gate-err"), "and tied to the box");
  if (shots) await page.screenshot({ path: `${shots}/gate-unlock-1280.png` });
  await page.fill("#gate-pass", PASS);
  await page.keyboard.press("Enter");
  await page.waitForSelector(".scriobh-prose", { timeout: 10000 });
  ok(new URL(page.url()).pathname === `/p/${slug}/d/${first}`, "back where the person was going: " + page.url());
  kit.main = null;
});


await kit.test("another tab locks the project: the page puts what was typed in its draft, has no text left in it, goes to the unlock page, and the unlock brings it back", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/, /409/, /422/);
  await openChapter(page);
  await typeAtEnd(page, " Held words kept through the lock.");
  eq((await lockElsewhere()).status, 200, "the lock");
  await waitLocked(page);
  ok(onUnlockPage(page), "the page went to the unlock page: " + page.url());
  ok(!decodeURIComponent(page.url()).includes("Zebra") && !page.url().includes("find="), "no manuscript text in the address: " + page.url());
  await assertNoText(page, ["Zebra", "Chapter One", "Held words", "Chapter Two", "harbour"], "the unlock page");
  const t = await visibleText(page);
  ok(t.includes("The Salt Road"), "the project's title");
  const draft = await page.evaluate(() => { const u = document.querySelector('meta[name="scriobh-user"]').content; const k = Object.keys(localStorage).find((x) => x.startsWith(`scriobh:draft:${u}:`)); return k ? localStorage.getItem(k) : ""; });
  ok(draft.includes("Held words kept through the lock."), "the typed text is in the draft store: " + draft.slice(0, 80));
  if (shots) await page.screenshot({ path: `${shots}/gate-locked-unlock-1280.png` });
  await page.fill("#gate-pass", "wrong wrong wrong");
  await page.keyboard.press("Enter");
  await page.waitForSelector("#gate-err");
  await page.fill("#gate-pass", PASS);
  await page.keyboard.press("Enter");
  await backAtChapter(page);
  eq(new URL(page.url()).pathname, `/p/${slug}/d/${first}`, "back at the chapter");
  await restoreDraft(page);
  await waitSaved(page);
  ok((await doc(owner.ctx)).content.includes("Held words kept through the lock."), "the held text was saved through the normal path");
  kit.main = null;
});

await kit.test("the manuscript is not in the page's document while it is locked: the editor's page and the reading view", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/);
  await openChapter(page);
  const before = await page.content();
  ok(before.includes("Zebra harbour") || (await page.evaluate(() => window.scriobh.write.editor.getMarkdown())).includes("Zebra harbour"), "the canary is in the open chapter");
  await lockElsewhere();
  await waitLocked(page);
  await assertNoText(page, ["Zebra harbour", "quiet tide"], "the locked editor's page");
  await unlockAny(page, PASS);
  await backAtChapter(page);
  // the reading view
  await page.goto(`${base}/p/${slug}/read/${first}`, { waitUntil: "load" });
  ok((await page.content()).includes("Zebra harbour"), "the reading view shows the text");
  await lockElsewhere();
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await waitLocked(page, 10000);
  await assertNoText(page, ["Zebra harbour", "quiet tide"], "the locked reading view");
  await unlockAny(page, PASS);
  await page.waitForSelector("#main");
  kit.main = null;
});

await kit.test("the cover (the unlock page cannot be reached): no text in the document, the draft is kept, Back to the library leaves, a closed tab loses nothing", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/, /ERR_FAILED/, /Failed to load resource/, /net::ERR/);
  await holdCover(page);
  await openChapter(page);
  await typeAtEnd(page, " Typed under a cover.");
  await lockElsewhere();
  await waitCover(page);
  await assertNoText(page, ["Zebra harbour", "Typed under", "Chapter One"], "the cover");
  const t = await visibleText(page);
  ok(t.includes("The Salt Road") && t.includes("is locked"), "the cover names the project: " + t.slice(0, 100));
  ok(await page.locator("#gate-cover-held").isVisible(), "the cover says what is kept");
  ok((await page.locator("#gate-cover-held").innerText()).includes("kept in this browser"), "in words");
  eq(await page.title(), "Locked · Scríobh", "the tab's title does not carry the chapter's");
  eq(await page.evaluate(() => document.activeElement.id), "gate-cover-pass", "focus is in the passphrase box");
  await page.keyboard.press("Escape");
  ok(await coverOpen(page), "Escape does not dismiss the cover");
  for (let i = 0; i < 6; i++) { await page.keyboard.press("Tab"); ok(await page.evaluate(() => !!document.activeElement.closest("#gate-cover")), "Tab stayed inside the cover"); }
  if (shots) await page.screenshot({ path: `${shots}/gate-cover-1280.png` });
  await unlockOnCover(page, "wrong wrong wrong");
  await page.waitForSelector("#gate-cover-err:not([hidden])");
  ok((await page.locator("#gate-cover-err").innerText()).includes("not right"), "the wrong passphrase is said");
  eq(await page.inputValue("#gate-cover-pass"), "", "the box is emptied");
  // the way out: the draft is already kept, so the link must leave (it did nothing in an earlier version)
  await page.click("#gate-cover a.btn.quiet");
  await page.waitForURL((u) => u.pathname === "/", { timeout: 8000 });
  await freeCover(page);
  // a closed tab: a new one at the chapter is sent to the unlock page, and the unlock offers the text back
  await page.close();
  const p2 = await owner.ctx.newPage(); kit.watch(p2, "tab2");
  owner.page = p2; kit.main = p2;
  await p2.goto(chapterURL, { waitUntil: "load" });
  ok(onUnlockPage(p2), "a new tab at a locked chapter is the unlock page");
  await unlockAny(p2, PASS);
  await backAtChapter(p2);
  await restoreDraft(p2);
  await waitSaved(p2);
  ok((await doc(owner.ctx)).content.includes("Typed under a cover."), "the text typed under the cover was saved after a closed tab");
  kit.main = null;
});

await kit.test("REVIEW: the cover's Back to the library link leaves while unsaved text is held (the draft is already kept)", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/, /401/, /422/, /ERR_FAILED/, /Failed to load resource/, /net::ERR/);
  await openChapter(page);
  await typeAtEnd(page, " Held words and a way out.");
  eq((await api(owner.ctx, "POST", `/api/v1/projects/${pid}/lock`)).status, 200, "the lock");
  await waitLocked(page);
  // 1. the way out must leave: the draft is already kept, so a held draft is no reason to refuse (the unlock page's own link, or the cover's)
  await page.click(onUnlockPage(page) ? "main a.btn.quiet" : "#gate-cover a.btn.quiet");
  await kit.sleep(1500);
  ok(new URL(page.url()).pathname === "/", "the link to the library did nothing: " + page.url());
  await page.goto(chapterURL, { waitUntil: "load" });
  await unlockAny(page, PASS);
  await backAtChapter(page);
  await restoreDraft(page);
  await waitSaved(page);
  kit.main = null;
});

await kit.test("a signed-out session on the cover is told so, with a way to sign in, not 'try again' for ever", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/, /401/, /ERR_FAILED/, /Failed to load resource/, /net::ERR/);
  await holdCover(page);
  await openChapter(page);
  await lockElsewhere();
  await waitCover(page);
  await page.route("**/api/v1/projects/*/unlock", (r) => r.fulfill({ status: 401, contentType: "application/json", body: JSON.stringify({ error: { code: "unauthenticated", message: "Sign in." } }) }));
  await unlockOnCover(page, PASS);
  await page.waitForSelector("#gate-cover-err:not([hidden])");
  ok((await page.locator("#gate-cover-err").innerText()).includes("signed out"), "said: " + (await page.locator("#gate-cover-err").innerText()));
  ok(await page.locator("#gate-cover-signin").isVisible(), "a link to sign in");
  ok((await page.locator("#gate-cover-signin").getAttribute("href")).startsWith("/auth/login?next="), "which comes back");
  await page.unroute("**/api/v1/projects/*/unlock");
  await freeCover(page);
  await page.goto(`${base}/`, { waitUntil: "load" });
  await unlockAny(await (async () => { await page.goto(chapterURL, { waitUntil: "load" }); return page; })(), PASS);
  await backAtChapter(page);
  kit.main = null;
});

await kit.test("Lock now from the account menu and from the palette: the page saves, then leaves for the unlock page", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/);
  await openChapter(page);
  await page.click(".avatar-btn");
  const lock = page.locator("#account [data-gate-lock]");
  ok(await lock.isVisible(), "the menu offers Lock this project now");
  await lock.click();
  await waitLocked(page);
  await assertNoText(page, ["Zebra harbour"], "after Lock now");
  await unlockAny(page, PASS);
  await backAtChapter(page);
  await page.keyboard.press(`${MOD}+k`);
  await page.fill("#palette-input", "Lock this");
  await page.waitForSelector('#palette-list li:has-text("Lock this project now")');
  await page.keyboard.press("Enter");
  await waitLocked(page);
  await unlockAny(page, PASS);
  await backAtChapter(page);
  kit.main = null;
});

await kit.test("a chapter changed by another session while locked: the held text comes back and meets the usual conflict choice, with both texts kept", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/, /409/);
  await openChapter(page);
  await typeAtEnd(page, " Mine, typed before the lock.");
  await lockElsewhere();
  await waitLocked(page);
  const other = await kit.signedIn({}, "writer@dev.scriobh");
  const u = await api(other.ctx, "POST", `/api/v1/projects/${pid}/unlock`, { passphrase: PASS });
  eq(u.status, 200, "the other session unlocks");
  const cur = await doc(other.ctx);
  let w = await api(other.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: cur.revision, content: "Theirs, written meanwhile.\n" });
  if (w.status === 409) { await api(other.ctx, "POST", `/api/v1/docs/${first}/lease`, { tab_id: "other", take: true }); w = await api(other.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: cur.revision, tab_id: "other", content: "Theirs, written meanwhile.\n" }); }
  ok(w.status === 200, "the other session writes: " + w.status);
  await unlockAny(page, PASS);
  await backAtChapter(page);
  await page.waitForSelector("#nb-draft", { timeout: 10000 });
  ok((await page.locator("#nb-draft").innerText()).includes("changed elsewhere"), "the bar says the saved chapter changed meanwhile: " + (await page.locator("#nb-draft").innerText()).replace(/\s+/g, " "));
  ok((await doc(owner.ctx)).content.includes("Theirs, written meanwhile."), "theirs is on the server, untouched, until the choice is made");
  await page.click('#nb-draft button:has-text("Restore your unsaved text")');
  await waitFor(async () => (await doc(owner.ctx)).content.includes("Mine, typed before the lock."), "mine to be saved", 10000);
  await waitSaved(page);
  // the text it replaced was copied first, as the restore always does
  const { readdirSync, readFileSync, existsSync } = await import("node:fs");
  const dir = `${kit.dataDir}/conflicts/${pid}`;
  ok(existsSync(dir) && readdirSync(dir).some((f) => readFileSync(`${dir}/${f}`, "utf8").includes("Theirs, written meanwhile.")), "a copy of theirs was stored before it was replaced");
  await other.ctx.close();
  kit.main = null;
});

await kit.test("the wait after wrong tries is a time in words on the unlock page", async () => {
  await ensure();
  const page = editor.page; kit.main = page;
  kit.allowed.push(/403/, /429/, /422/);
  await api(editor.ctx, "POST", `/api/v1/projects/${pid}/lock`);
  await page.goto(chapterURL, { waitUntil: "load" });
  ok(onUnlockPage(page), "the unlock page");
  for (let i = 0; i < 8; i++) {
    await page.fill("#gate-pass", "guess number " + i);
    await page.keyboard.press("Enter");
    await page.waitForSelector("#gate-err");
    if (/try again in/.test(await page.locator("#gate-err").innerText())) break;
  }
  const msg = await page.locator("#gate-err").innerText();
  ok(/try again in (about )?\d+ (second|minute)/.test(msg), "a wait in words: " + msg);
  ok(!/429|rate/.test(msg), "no raw code");
  ok((await visibleText(page)).includes("right passphrase will work again"), "and that the right passphrase waits too");
  kit.main = null;
});

await kit.test("offline: the cover holds, says so when an unlock cannot be sent, and the page comes back by itself when the network does", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/, /ERR_INTERNET_DISCONNECTED/, /Failed to load resource/, /net::ERR/);
  await openChapter(page);
  await typeAtEnd(page, " Offline words.");
  await owner.ctx.setOffline(true);
  await page.evaluate(() => window.scriobh.gate.lock());
  await waitCover(page);
  await assertNoText(page, ["Zebra harbour", "Offline words", "Chapter One"], "the offline cover");
  ok(await page.locator("#gate-cover-held").isVisible(), "the cover says what is kept");
  await unlockOnCover(page, PASS);
  await page.waitForSelector("#gate-cover-err:not([hidden])");
  ok((await page.locator("#gate-cover-err").innerText()).includes("Could not reach the server"), "said");
  eq(await page.inputValue("#gate-cover-pass"), "", "the passphrase is not kept in the box");
  if (shots) await page.screenshot({ path: `${shots}/gate-cover-offline-1280.png` });
  await owner.ctx.setOffline(false); // the server never locked it: the page finds that out, and comes back with the draft offered
  await backAtChapter(page);
  await restoreDraft(page);
  await waitSaved(page);
  ok((await doc(owner.ctx)).content.includes("Offline words."), "the text typed before the cover was saved");
  kit.main = null;
});

await kit.test("the page's own idle timer: a clock a test sets locks it with the network cut, and nothing of the text is left in it", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/ERR_INTERNET_DISCONNECTED/, /Failed to load resource/, /net::ERR/);
  await openChapter(page);
  await typeAtEnd(page, " Idle words.");
  await waitSaved(page);
  await owner.ctx.setOffline(true);
  await page.evaluate(() => { const t = Date.now(); window.scriobh.gate.clock = () => t + 31 * 60000; window.scriobh.gate.idleCheck(); });
  await waitCover(page, 3000);
  await assertNoText(page, ["Zebra harbour", "Idle words"], "the idle cover");
  await owner.ctx.setOffline(false);
  await backAtChapter(page);
  kit.main = null;
});

await kit.test("an htmx request refused because the project locked is not followed to the unlock page before the draft is written", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/);
  await openChapter(page);
  await typeAtEnd(page, " Typed beside an htmx call.");
  await lockElsewhere();
  await page.evaluate((id) => window.htmx.ajax("GET", `/ui/projects/${id}/tree`, { target: "#dialog-body", swap: "innerHTML" }), pid);
  await waitLocked(page, 8000);
  const draft = await page.evaluate(() => Object.keys(localStorage).filter((k) => /^scriobh:(draft|held):/.test(k)).map((k) => localStorage.getItem(k)).join("|"));
  ok(draft.includes("Typed beside an htmx call."), "what was typed is in the drafts");
  await unlockAny(page, PASS);
  await backAtChapter(page);
  await restoreDraft(page);
  await waitSaved(page);
  ok((await doc(owner.ctx)).content.includes("Typed beside an htmx call."), "and was saved after the unlock");
  kit.main = null;
});

await kit.test("every field keeps a draft: the inspector's text, choice and tick, a form, survive a lock and a closed tab, and come back with a note", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/, /ERR_FAILED/, /Failed to load resource/, /net::ERR/);
  await openChapter(page);
  await page.click("#insp-open");
  await page.waitForSelector("#in-syn-s", { state: "visible" });
  await waitFor(() => page.evaluate(() => !!window.scriobh.inspector), "the inspector", 8000);
  await page.route("**/api/v1/docs/*", (r) => (r.request().method() === "PATCH" ? r.abort() : r.continue())); // none of it can be saved: it is all unsaved, as in a bad moment
  await page.fill("#in-syn-s", "A synopsis typed before the lock.");
  await page.fill("#in-pov", "Wren, close third");
  await page.selectOption("#in-status", "revise");
  await page.locator("#in-compile").uncheck();
  await kit.sleep(900);
  await lockElsewhere();
  await waitLocked(page);
  const kept = await page.evaluate(() => Object.keys(localStorage).filter((k) => /^scriobh:fields:/.test(k)).map((k) => localStorage.getItem(k)).join("|"));
  ok(kept.includes("A synopsis typed before the lock.") && kept.includes("Wren, close third") && kept.includes("revise"), "the fields are in this browser: " + kept.slice(0, 120));
  await assertNoText(page, ["A synopsis typed", "Wren, close third"], "the unlock page");
  await page.close();
  const p2 = await owner.ctx.newPage(); kit.watch(p2, "tab2");
  owner.page = p2; kit.main = p2;
  await p2.goto(chapterURL, { waitUntil: "load" });
  await unlockAny(p2, PASS);
  await backAtChapter(p2);
  await p2.waitForSelector(".held-note", { timeout: 10000 });
  ok((await p2.locator(".held-note").innerText()).includes("Your unsaved changes were kept in this browser and are back."), "the note says so");
  await waitFor(async () => { const d = await doc(owner.ctx); return d.synopsis_short === "A synopsis typed before the lock." && d.pov === "Wren, close third" && d.status === "revise" && d.include_in_compile === false; }, "the fields saved through the normal path", 15000);
  eq(await p2.inputValue("#in-syn-s"), "A synopsis typed before the lock.", "and are in the boxes");
  ok(await p2.evaluate(() => !Object.keys(localStorage).some((k) => /^scriobh:fields:/.test(k))), "the drafts are gone once saved");
  kit.main = null;
});

await kit.test("a form's field is kept too; a draft with no field to go into is shown, to copy or discard", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/);
  await page.goto(`${base}/p/${slug}/goals`, { waitUntil: "load" });
  await page.fill("#g-daily", "777");
  await lockElsewhere();
  await page.evaluate(() => window.dispatchEvent(new Event("focus"))); // a page that asks nothing finds out when asked to look
  await waitLocked(page);
  await unlockAny(page, PASS);
  await page.waitForSelector(".held-note", { timeout: 10000 });
  eq(await page.inputValue("#g-daily"), "777", "a form's number is back in its box");
  await kit.sleep(900); // what is back and not saved is kept again until it is
  // an unmatched draft: a field the page no longer has (put in from a page that does not watch fields, so that this page's own writes do not replace it)
  await page.goto(`${base}/`, { waitUntil: "load" });
  await page.evaluate((path) => { const u = document.querySelector('meta[name="scriobh-user"]').content; localStorage.setItem(`scriobh:fields:${u}:${path}`, JSON.stringify({ f: [{ s: "#gone", v: "Text with nowhere to go", l: "A comment", y: "textarea" }], t: 1 })); }, `/p/${slug}/goals`);
  const url = `/p/${slug}/goals`;
  await page.goto(`${base}${url}`, { waitUntil: "load" });
  await page.waitForSelector(".held-note textarea.held-copy", { timeout: 10000 });
  eq(await page.inputValue(".held-copy"), "Text with nowhere to go", "it is shown, to copy");
  await page.click('.held-note button:has-text("Discard")');
  eq(await page.locator(".held-note").count(), 0, "and can be discarded: " + url);
  kit.main = null;
});

await kit.test("an outline's inline edit is kept through a lock: its field is not on the page that comes back, so the text is shown to copy", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/, /ERR_FAILED/, /Failed to load resource/, /net::ERR/);
  await page.goto(`${base}/p/${slug}/outline`, { waitUntil: "load" });
  await page.waitForSelector("#o-live");
  await page.locator("li[data-id]").first().click();
  await page.locator("[data-act=edit]").first().click();
  await page.route("**/api/v1/docs/*", (r) => (r.request().method() === "PATCH" ? r.abort() : r.continue()));
  await page.locator(".orow-edit textarea").fill("Outline words typed before the lock.");
  await kit.sleep(700);
  await lockElsewhere();
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await waitLocked(page);
  await assertNoText(page, ["Outline words typed"], "the unlock page");
  await unlockAny(page, PASS);
  await page.waitForSelector(".held-note textarea.held-copy", { timeout: 10000 });
  eq(await page.inputValue(".held-copy"), "Outline words typed before the lock.", "the text is shown, to copy");
  await page.click('.held-note button:has-text("Discard")');
  await page.unroute("**/api/v1/docs/*");
  kit.main = null;
});

await kit.test("signing out removes every draft, including a locked project's, and one that could not be removed is removed at the next load", async () => {
  await ensure();
  const w = await kit.signedIn({}, "writer@dev.scriobh");
  const page = w.page; kit.main = page;
  kit.allowed.push(/403/, /ERR_FAILED/, /Failed to load resource/, /net::ERR/);
  await api(owner.ctx, "POST", `/api/v1/projects/${pid}/lock`);
  await api(w.ctx, "POST", `/api/v1/projects/${pid}/unlock`, { passphrase: PASS });
  await openChapter(page);
  await page.route("**/api/v1/docs/*", (r) => (r.request().method() === "PATCH" ? r.abort() : r.continue()));
  await typeAtEnd(page, " A draft that must not outlive the sign-out.");
  await api(w.ctx, "POST", `/api/v1/projects/${pid}/lock`);
  await waitLocked(page);
  await page.goto(`${base}/`, { waitUntil: "load" });
  const has = () => page.evaluate(() => Object.keys(localStorage).filter((k) => /^scriobh:(draft|held|fields|scratch|research|read):/.test(k)).length);
  ok((await has()) > 0, "the locked project's draft is in this browser");
  await page.unroute("**/api/v1/docs/*");
  await page.click(".avatar-btn");
  await page.locator("#signout button").click();
  const asked = await armAnswer(page, "ok"); // asked in the page: the browser's own panel is never used
  ok(/locked/.test(asked) && /discard/.test(asked), "it asked first, in words: " + asked.slice(0, 120));
  await waitFor(async () => (await has()) === 0, "every draft to be gone after the sign-out", 8000);
  // the removal that could not run: the marker makes the next page do it before anything is drawn
  await page.evaluate(() => { const u = "usr_x"; localStorage.setItem(`scriobh:draft:${u}:doc_1`, JSON.stringify({ md: "left behind" })); localStorage.setItem("scriobh:purge", "1"); });
  await page.reload({ waitUntil: "load" });
  eq(await has(), 0, "a marked removal is finished at the next load");
  await w.ctx.close();
  await api(owner.ctx, "POST", `/api/v1/projects/${pid}/unlock`, { passphrase: PASS }); // this test locked the owner's own session to make a second one
  owner.page.bringToFront().catch(() => {});
  kit.main = null;
});

await kit.test("the page checks again when the tab comes back: a lock made while it was hidden takes the text off the screen", async () => {
  await ensure();
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/);
  await openChapter(page);
  await lockElsewhere();
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await waitLocked(page, 8000);
  await unlockAny(page, PASS);
  await backAtChapter(page);
  kit.main = null;
});

await kit.test("at 390 px: the unlock page, the cover and the settings page have no sideways scroll and touch-sized controls", async () => {
  await ensure();
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  const page = await ctx.newPage(); kit.watch(page, "phone"); kit.main = page;
  kit.allowed.push(/403/, /ERR_ABORTED/, /ERR_INTERNET_DISCONNECTED/, /Failed to load resource/, /net::ERR/);
  const { signIn } = await import("./harness.mjs");
  await signIn(page, base, "writer@dev.scriobh");
  await page.goto(`${base}/p/${slug}`, { waitUntil: "load" });
  ok(onUnlockPage(page), "this session has not unlocked");
  eq(await hscroll(page) <= 0, true, "no sideways scroll on the unlock page");
  const small = await page.evaluate(() => [...document.querySelectorAll("main a.btn, main button, main input")].filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => e.outerHTML.slice(0, 60)));
  eq(small.length, 0, "every control on the unlock page is at least 44 px: " + small.join(" | "));
  if (shots) await page.screenshot({ path: `${shots}/gate-unlock-390.png` });
  await page.fill("#gate-pass", PASS);
  await page.keyboard.press("Enter");
  await page.waitForSelector(".scriobh-prose", { timeout: 10000 });
  await ctx.setOffline(true);
  await page.evaluate(() => window.scriobh.gate.lock());
  await waitCover(page);
  eq(await hscroll(page) <= 0, true, "no sideways scroll on the cover");
  const cs = await page.evaluate(() => [...document.querySelectorAll("#gate-cover a:not([hidden]), #gate-cover button, #gate-cover input:not([type=hidden])")].filter((e) => e.getBoundingClientRect().height < 43.5).map((e) => e.outerHTML.slice(0, 60)));
  eq(cs.length, 0, "every control on the cover is at least 44 px: " + cs.join(" | "));
  ok(await page.evaluate(() => { const r = document.getElementById("gate-cover-pass").getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight; }), "the passphrase box is on the screen");
  if (shots) await page.screenshot({ path: `${shots}/gate-cover-390.png` });
  await ctx.setOffline(false);
  await kit.sleep(1500);
  await page.goto(`${base}/p/${slug}/passphrase`, { waitUntil: "load" });
  eq(await hscroll(page) <= 0, true, "no sideways scroll on the settings page");
  const ps = await page.evaluate(() => [...document.querySelectorAll("main a.btn, main button, main input:not([type=hidden]):not([type=checkbox])")].filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => e.outerHTML.slice(0, 70)));
  eq(ps.length, 0, "every control on the settings page is at least 44 px: " + ps.join(" | "));
  if (shots) await page.screenshot({ path: `${shots}/gate-settings-390.png`, fullPage: true });
  await ctx.close();
  kit.main = null;
});

await kit.test("without script: the unlock page and the settings forms work as plain forms", async () => {
  await ensure();
  const ctx = await kit.browser.newContext({ storageState: await owner.ctx.storageState(), javaScriptEnabled: false, serviceWorkers: "block" });
  const page = await ctx.newPage();
  await api(owner.ctx, "POST", `/api/v1/projects/${pid}/lock`);
  await page.goto(`${base}/p/${slug}`, { waitUntil: "load" });
  ok(new URL(page.url()).pathname.endsWith("/unlock"), "the unlock page");
  await page.fill("#gate-pass", PASS);
  await page.click('button[type="submit"]');
  await page.waitForURL((u) => u.pathname.startsWith(`/p/${slug}`) && !u.pathname.endsWith("/unlock"), { timeout: 10000 });
  ok((await page.content()).includes("Chapter"), "the project opened");
  await ctx.close();
  const o2 = await kit.browser.newContext({ storageState: await owner.ctx.storageState(), javaScriptEnabled: false, serviceWorkers: "block" });
  const p2 = await o2.newPage();
  await p2.goto(`${base}/p/${slug}/passphrase`, { waitUntil: "load" });
  await p2.fill("#po-idle", "40");
  await p2.fill("#po-cur", PASS);
  await p2.click('form[action$="/passphrase/options"] button[type="submit"]');
  await p2.waitForSelector(".saved");
  eq(await p2.inputValue("#po-idle"), "40", "the idle limit was saved by a plain form");
  await p2.fill("#po-idle", "30");
  await p2.fill("#po-cur", PASS);
  await p2.click('form[action$="/passphrase/options"] button[type="submit"]');
  await p2.waitForSelector(".saved");
  await o2.close();
});

await kit.test("the library shows a locked project as its title and the words Needs a passphrase, once", async () => {
  await ensure();
  const ctx = await kit.newContext({ viewport: { width: 1000, height: 700 } });
  const page = await ctx.newPage(); kit.watch(page, "library"); kit.main = page;
  const { signIn } = await import("./harness.mjs");
  await signIn(page, base, "writer@dev.scriobh");
  await page.goto(`${base}/`, { waitUntil: "load" });
  const tile = page.locator("li.tile", { hasText: "The Salt Road" });
  const t = (await tile.innerText()).replace(/\s+/g, " ");
  ok(t.includes("Needs a passphrase"), "the lock is named: " + t);
  eq(t.split("Needs a passphrase").length - 1, 1, "once: " + t);
  ok(!/\d+ words?/.test(t) && !t.includes("Wren Writer"), "no count and no author: " + t);
  eq(await tile.locator(".pin-btn, a[aria-haspopup=dialog]").count(), 0, "no action that would ask the project something");
  if (shots) await page.screenshot({ path: `${shots}/gate-library-1000.png` });
  await ctx.close();
  kit.main = null;
});

await kit.test("mirrored: the settings page, the unlock page and the cover read right to left without sideways scroll", async () => {
  await ensure();
  await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } });
  const page = owner.page; kit.main = page;
  kit.allowed.push(/403/, /ERR_INTERNET_DISCONNECTED/, /Failed to load resource/, /net::ERR/);
  await page.goto(`${base}/p/${slug}/passphrase`, { waitUntil: "load" });
  eq(await page.evaluate(() => document.documentElement.dir), "rtl", "dir=rtl");
  eq(await hscroll(page) <= 0, true, "no sideways scroll");
  if (shots) await page.screenshot({ path: `${shots}/gate-settings-rtl.png`, fullPage: true });
  await openChapter(page);
  await owner.ctx.setOffline(true);
  await page.evaluate(() => window.scriobh.gate.lock());
  await waitCover(page);
  eq(await hscroll(page) <= 0, true, "no sideways scroll on the cover");
  eq(await page.evaluate(() => /[a-z]{4,}/.test(document.getElementById("gate-cover").innerText.replace(/The Salt Road/g, ""))), false, "no plain English left on the cover");
  if (shots) await page.screenshot({ path: `${shots}/gate-cover-rtl.png` });
  await owner.ctx.setOffline(false);
  await backAtChapter(page);
  await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "en" } });
  kit.main = null;
});

if (!process.argv.includes("--no-idle")) {
  await kit.test("the idle limit locks an unattended editor (a minute): the page's own upkeep does not keep it open, and with the network cut the page's own timer does it", async () => {
    await ensure();
    const page = owner.page; kit.main = page;
    kit.allowed.push(/403/, /ERR_INTERNET_DISCONNECTED/, /Failed to load resource/, /net::ERR/);
    const s = await api(owner.ctx, "PUT", `/api/v1/projects/${pid}/passphrase`, { current: PASS, idle_minutes: 1 });
    eq(s.status, 200, "idle limit of one minute: " + JSON.stringify(s.json));
    await openChapter(page);
    await typeAtEnd(page, " Typed just before leaving the desk.");
    await waitSaved(page);
    const t0 = Date.now();
    await waitLocked(page, 130000);
    ok(Date.now() - t0 >= 20000, "it locked by idle, not at once");
    await assertNoText(page, ["Typed just before", "Zebra harbour"], "after the idle lock");
    await unlockAny(page, PASS);
    await backAtChapter(page);
    // the same with the network cut: the server cannot say, the page's own timer does
    await typeAtEnd(page, " More.");
    await waitSaved(page);
    await owner.ctx.setOffline(true);
    const t1 = Date.now();
    await waitCover(page, 120000);
    ok(Date.now() - t1 >= 20000, "the page locked itself by its own timer (" + Math.round((Date.now() - t1) / 1000) + " s)");
    await assertNoText(page, ["Typed just before", "Zebra harbour"], "the offline idle cover");
    await owner.ctx.setOffline(false);
    // the server's own limit has run out as well by now: the page finds its way to the unlock page, and the unlock brings the chapter back
    await waitFor(async () => onUnlockPage(page) || (await page.locator(".scriobh-prose").count()) > 0, "the unlock page or the chapter", 20000);
    if (onUnlockPage(page)) await unlockAny(page, PASS);
    await backAtChapter(page);
    await api(owner.ctx, "PUT", `/api/v1/projects/${pid}/passphrase`, { current: PASS, idle_minutes: 30 });
    kit.main = null;
  });
}

await kit.finish("e2e-gate");
