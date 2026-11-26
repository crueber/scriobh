#!/usr/bin/env node
// End-to-end test of the locked writing page (UX-FIXES U2) in a real Chromium, against the real binary.
//
//   node scripts/e2e-lock.mjs [--only substring] [--shots DIR]
//
// A tab that another tab holds reads and cannot edit: its title is refused, and it asks for the lease again
// on its own (every 30 s while the page is in front, and when it comes back). A tab that closed without giving
// the chapter back is taken back about 90 s later (the lease's time), and the page then shows the server's text
// before it allows typing. A live tab's lease is never taken from it. A title-only change survives a reload.
// With --shots DIR the locked page and the "can write again" page are photographed at 1280 and 390 px, light and dark.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";

const args = process.argv.slice(2);
const arg = (k) => (args.includes(k) ? args[args.indexOf(k) + 1] : "");
const shots = arg("--shots");
if (shots) mkdirSync(shots, { recursive: true });

const kit = await createKit("e2e-lock");
kit.deadline(12 * 60 * 1000); // the kill step waits for the lease to run out: about 2 minutes
kit.only = arg("--only");
const { api, waitFor, sleep, MOD } = kit;

const main = (await kit.signedIn()).ctx;
const seed = async (title) => {
  const made = (await api(main, "POST", "/api/v1/projects", { title, kind: "novel" })).json.item;
  const doc = (await api(main, "GET", `/api/v1/projects/${made.id}/docs`)).json.order[0];
  return { id: made.id, slug: made.slug, doc };
};
const serverDoc = async (id) => (await api(main, "GET", `/api/v1/docs/${id}`)).json.item;
const setServer = async (id, content) => {
  const cur = await serverDoc(id);
  const r = await api(main, "PATCH", `/api/v1/docs/${id}`, { base_revision: cur.revision, content });
  eq(r.status, 200, "the server's own save");
};
const open = async (page, p) => {
  await page.goto(`${kit.base()}/p/${p.slug}/d/${p.doc}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor)), "the writing view", 10000);
};
const state = (page) => page.locator("#save-state").getAttribute("data-state");
const waitState = (page, s, ms = 12000) => waitFor(async () => (await state(page)) === s, `state ${s}`, ms);
const waitSaved = (page) => waitFor(async () => (await state(page)) === "saved" && !(await page.evaluate(() => window.scriobh.write.unsaved())), "saved", 12000);
const editable = (page) => page.locator(".scriobh-prose").getAttribute("contenteditable");
const editorMd = async (page) => (await page.evaluate(() => window.scriobh.write.editor.getMarkdown())).trim();
const titleOf = (page) => page.locator("#doc-title").inputValue();
const live = (page) => page.locator("#live").textContent();
const typeInto = async (page, text) => {
  await page.locator(".scriobh-prose").click();
  await page.keyboard.press(`${MOD}+End`);
  await page.keyboard.type(text);
};
// Photographs for the design check: 1280 and 390 px, light and dark. Only with --shots.
const photo = async (page, name) => {
  if (!shots) return;
  const size = page.viewportSize();
  for (const scheme of ["light", "dark"]) {
    await page.emulateMedia({ colorScheme: scheme });
    await sleep(300);
    await page.screenshot({ path: join(shots, `${name}-${scheme}-${size.width}.png`), fullPage: false });
  }
  await page.emulateMedia({ colorScheme: null });
};
const phone = async (page, on) => page.setViewportSize(on ? { width: 390, height: 844 } : { width: 1280, height: 800 });

// Two tabs of one browser context have two tab ids (sessionStorage is per tab), which is what the lease tells apart.
const bodyOnServer = async (id) => (await serverDoc(id)).content.trim();

let P; // the project the lock tests share
await kit.test("setup: a chapter with text, held by tab A", async () => {
  P = await seed("Locked book");
  await setServer(P.doc, "Tab A text.");
  eq((await serverDoc(P.doc)).content, "Tab A text.", "server text");
});

// Tab A stays open for the two steps that follow; it holds the lease.
let A = null;
await kit.test("a tab that another live tab holds is read-only, with a banner, and stays so while that tab is live", async () => {
  A = await main.newPage();
  kit.watch(A, "tab A");
  await open(A, P);
  await waitSaved(A);
  eq(await editable(A), "true", "tab A is the writer");
  const B = await main.newPage();
  kit.B = B; // the next step closes tab A and reads tab B, whatever this step found
  kit.watch(B, "tab B");
  const requested = [];
  B.on("request", (r) => { if (/\/static\/js\/lock\./.test(r.url())) requested.push(r.url()); });
  await open(B, P);
  await waitState(B, "locked");
  await B.locator("#nb-lock").waitFor({ timeout: 8000 });
  ok((await B.locator("#nb-lock").innerText()).includes("Take over"), "no Take over in the banner");
  eq(requested.length, 1, "lock.js is fetched once, by the locked page");
  eq(await editable(B), "false", "tab B must be read-only");
  // A live tab keeps its lease: B asks again (every 30 s) and is refused each time.
  await sleep(35000);
  eq(await state(B), "locked", "tab B must stay locked while tab A is live");
  eq(await editable(B), "false", "tab B must stay read-only while tab A is live");
  eq(await editable(A), "true", "tab A keeps writing");
  await typeInto(A, " Still A.");
  await waitSaved(A);
  eq(await bodyOnServer(P.doc), "Tab A text. Still A.", "tab A's save reached the server");
  await phone(B, false);
  await photo(B, "locked");
  await phone(B, true);
  await photo(B, "locked");
  await phone(B, false);
});

await kit.test("a locked page refuses a title change and sends none", async () => {
  const B = kit.B;
  const before = await titleOf(B);
  let patchedTitle = 0;
  B.on("request", (r) => { if (r.method() === "PATCH" && /"title"/.test(r.postData() || "")) patchedTitle++; });
  await B.locator("#doc-title").click();
  await B.keyboard.type("Refused title");
  await sleep(1500);
  eq(await titleOf(B), before, "a locked title must not change");
  eq(patchedTitle, 0, "a locked title must not be sent");
  eq(await state(B), "locked", "still locked");
});

await kit.test("a tab that closed without giving the chapter back is taken back on its own, with the server's text", async () => {
  const B = kit.B;
  // Tab A goes away without letting go: its release request is refused, as if the browser had been killed.
  await main.route("**/api/v1/docs/*/lease", (route) => {
    const body = route.request().postDataJSON ? route.request().postDataJSON() : null;
    if (body && body.release) return route.abort();
    return route.continue();
  });
  // The server's copy moves on while tab B is locked (an API client, which the lease does not apply to).
  await setServer(P.doc, "Server text, written while B was locked.");
  await A.close();
  const gone = Date.now();
  await sleep(5000);
  eq(await state(B), "locked", "tab B must still be locked just after tab A closed: the lease was not given back");
  await waitFor(async () => (await editable(B)) === "true", "tab B to be writable on its own", 150000);
  const took = Math.round((Date.now() - gone) / 1000);
  console.log(`      tab B was writable again ${took} s after tab A closed`);
  ok(took <= 130, `writable after ${took} s, expected about 2 minutes at most`);
  await waitSaved(B);
  eq(await state(B), "saved", "tab B's state");
  await waitFor(async () => (await live(B)).includes("You can write here again"), "the live region to say that tab B can write", 5000);
  ok(!(await B.locator("#nb-live").count()), "the stale \"changed somewhere else\" bar is still shown");
  eq(await editorMd(B), "Server text, written while B was locked.", "tab B must show the server's text, never the old text");
  eq(await titleOf(B), (await serverDoc(P.doc)).title, "the title is the server's");
  await photo(B, "back");
  await phone(B, true);
  await photo(B, "back");
  await phone(B, false);
  await typeInto(B, " B again.");
  await waitSaved(B);
  eq(await bodyOnServer(P.doc), "Server text, written while B was locked. B again.", "tab B's save reached the server");
  await main.unroute("**/api/v1/docs/*/lease");
  await B.close();
});

await kit.test("a title-only change survives a reload before it is saved", async () => {
  const Q = await seed("Title book");
  const page = await main.newPage();
  kit.watch(page, "tab Q");
  let lockAsked = 0;
  page.on("request", (r) => { if (/\/static\/js\/lock\./.test(r.url())) lockAsked++; });
  await open(page, Q);
  await waitSaved(page);
  eq(lockAsked, 0, "an unlocked page never fetches lock.js");
  await page.locator("#doc-title").click();
  await page.keyboard.press(`${MOD}+A`);
  await page.keyboard.type("Retitled soon");
  await page.reload({ waitUntil: "load" }); // before the save delay has passed
  await page.waitForSelector(".scriobh-prose");
  await page.getByRole("button", { name: "Restore your unsaved text" }).waitFor({ timeout: 8000 });
  await page.getByRole("button", { name: "Restore your unsaved text" }).click();
  eq(await titleOf(page), "Retitled soon", "the title typed before the reload");
  await waitSaved(page);
  eq((await serverDoc(Q.doc)).title, "Retitled soon", "the restored title reached the server");
  await page.close();
});

// A hidden tab stops renewing its lease, and its typed words must never be lost or overwritten. Playwright cannot hide a
// headless page, so the test replaces document.visibilityState with a value it sets and dispatches the real event.
const hideable = (page) => page.addInitScript(() => {
  let v = "visible";
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => v });
  window.__setVisibility = (s) => { v = s; document.dispatchEvent(new Event("visibilitychange")); };
});
const hide = (page, s) => page.evaluate((x) => window.__setVisibility(x), s);
const draftHolds = (page, words) => page.evaluate((w) => Object.keys(localStorage).some((k) => k.startsWith("scriobh:draft:") && localStorage.getItem(k).includes(w)), words);

await kit.test("a tab that goes hidden keeps its typed words; the tab that takes its lease never overwrites them", async () => {
  kit.allowed = [/Failed to load resource|net::ERR/];
  const Q = await seed("Hidden book");
  const second = (await api(main, "POST", `/api/v1/projects/${Q.id}/docs`, { title: "Second", kind: "chapter", content: "Second saved text." })).json.item.id;
  await setServer(Q.doc, "First saved text.");
  const second2 = { slug: Q.slug, doc: second };
  // A1 has words the server does not have: its saves fail while it is hidden. A2 has nothing unsaved.
  const A1 = await main.newPage(); kit.watch(A1, "A1"); await hideable(A1); await open(A1, Q); await waitSaved(A1);
  const A2 = await main.newPage(); kit.watch(A2, "A2"); await hideable(A2); await open(A2, second2); await waitSaved(A2);
  await A1.route("**/api/v1/docs/*", (r) => (r.request().method() === "PATCH" ? r.abort() : r.continue()));
  await typeInto(A1, " Unsaved words.");
  await hide(A1, "hidden");
  await hide(A2, "hidden");
  await sleep(1500);
  ok(await draftHolds(A1, "Unsaved words."), "the words typed before hiding are in this browser's draft");
  // Tab B for each chapter: both locked while A holds them, then free once the hidden tabs' leases have run out.
  const B1 = await main.newPage(); kit.watch(B1, "B1"); await open(B1, Q); await waitState(B1, "locked");
  const B2 = await main.newPage(); kit.watch(B2, "B2"); await open(B2, second2); await waitState(B2, "locked");
  await waitFor(async () => (await editable(B1)) === "true" && (await editable(B2)) === "true", "both leases of the hidden tabs to be taken", 150000);
  eq(await editorMd(B1), "First saved text.", "B1 must show the server's text, not the hidden tab's unsaved words");
  await typeInto(B1, " B edit.");
  await waitSaved(B1);
  eq(await bodyOnServer(Q.doc), "First saved text. B edit.", "B's save");
  // A1 comes back: its lease is gone, so it offers the choices and keeps its words.
  await A1.unroute("**/api/v1/docs/*");
  await hide(A1, "visible");
  await waitFor(async () => (await A1.locator("#nb-conflict").count()) > 0, "A1 to offer the conflict choices", 20000);
  const bar = await A1.locator("#nb-conflict").innerText();
  ok(bar.includes("open for writing in another tab") && bar.includes("Keep mine"), "A1's bar: " + bar);
  ok((await editorMd(A1)).includes("Unsaved words."), "A1 must still hold its typed words");
  eq(await bodyOnServer(Q.doc), "First saved text. B edit.", "the hidden tab must not overwrite B's text");
  // A2 comes back with nothing unsaved: it is locked, and the server's text is untouched.
  await hide(A2, "visible");
  await waitState(A2, "locked");
  eq(await editable(A2), "false", "A2 is read-only while B2 holds the chapter");
  eq(await bodyOnServer(second), "Second saved text.", "A2 must not have written");
  for (const p of [A1, A2, B1, B2]) await p.close();
});

await kit.finish("e2e-lock");
