#!/usr/bin/env node
// End-to-end checks for the fixes of the Phase 3 UI review that need a real browser: private drafts never reach the
// next person on the same browser and are removed at sign-out; a live refresh keeps the focused row and catches a
// change that arrives while it is fetching; a name under the caret can be opened from the palette and is announced;
// the stream restarts when a page comes back from the back-forward cache; the PDF preview frame is locked down; the
// sheet's one-line fields are one line; a board move that fails puts the Move to box back.
//
//   node scripts/e2e-review3.mjs [--only substring]
import { armAnswer, armOk, createKit, eq, ok } from "./e2e-kit.mjs";
import { signIn } from "./harness.mjs";

const kit = await createKit("e2e-review3");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page: wren } = await kit.signedIn();
kit.main = wren;

let book, cmp, rory;
const keys = (page, re) => page.evaluate((s) => Object.keys(localStorage).filter((k) => new RegExp(s).test(k)), re.source);

await kit.test("setup: a book with a Compendium, a reader on the Compendium, one entity", async () => {
  (await kit.signedIn({}, "reader@dev.scriobh")).ctx.close(); // the reader has signed in once, so they can be added
  book = (await api(main, "POST", "/api/v1/projects", { title: "Review Book", kind: "novel" })).json.item;
  cmp = (await api(main, "POST", "/api/v1/compendiums", { name: "Review Bible", project_id: book.id })).json.item;
  const r = await api(main, "POST", `/api/v1/compendiums/${cmp.id}/members`, { email: "reader@dev.scriobh", role: "reader" });
  ok(r.status < 300, "adding the reader: " + r.status + " " + JSON.stringify(r.json));
});

await kit.test("a private draft carries its owner's id, never reaches the next person, and sign-out removes it", async () => {
  kit.allowed = [/Failed to load resource/, /ERR_FAILED/];
  const A = await kit.signedIn();
  const page = A.page;
  await page.route("**/scratchpad", (r) => (r.request().method() === "PUT" ? r.abort() : r.continue()));
  await page.goto(`${base()}/c/${cmp.slug}/scratch`);
  await page.locator("#sc-content").fill("Wren's private line, never saved.");
  await waitFor(async () => (await keys(page, /^scriobh:scratch:/)).length === 1, "the draft to be kept in the browser", 10000);
  const [key] = await keys(page, /^scriobh:scratch:/);
  ok(/^scriobh:scratch:usr_[^:]+:c:/.test(key), "the key names its owner: " + key);
  // The session ends without a clean sign-out (it expired); someone else signs in on the same browser profile.
  await page.unroute("**/scratchpad");
  await A.ctx.clearCookies();
  await signIn(page, base(), "reader@dev.scriobh");
  await page.goto(`${base()}/c/${cmp.slug}/scratch`);
  await page.locator("#sc-content").waitFor();
  await sleep(600);
  eq(await page.locator(".r-bar").count(), 0, "the next person is offered no copy of the previous person's text");
  ok(!(await page.locator("#sc-content").inputValue()).includes("Wren"), "and the text is not put in their pad");
  // Their own draft is under their own id; signing out removes every draft, theirs and the one left behind.
  await page.route("**/scratchpad", (r) => (r.request().method() === "PUT" ? r.abort() : r.continue()));
  await page.locator("#sc-content").fill("Rory's own unsaved line.");
  await waitFor(async () => (await keys(page, /^scriobh:scratch:/)).length === 2, "Rory's own draft", 10000);
  await page.unroute("**/scratchpad");
  await page.locator("#sc-content").fill("");
  await page.evaluate(() => document.querySelector("#signout").requestSubmit());
  await armOk(page);
  await page.waitForURL(`${base()}/`, { timeout: 15000 });
  await waitFor(async () => (await keys(page, /^scriobh:(scratch|research|read|draft|held):/)).length === 0, "every draft to be removed at sign-out", 10000);
  await A.ctx.close();
  kit.allowed = [];
});

// One signed-in browser for the rest: the login endpoint is rate limited, and a page of its own is all each step needs.
const L = { ctx: main }; // the first browser again, in new pages
const fresh = async () => { const p = await L.ctx.newPage(); kit.watch(p, "page"); return p; };

await kit.test("the entity list keeps the focused row and the scroll, and catches a change that arrives mid-fetch", async () => {
  for (let i = 1; i <= 4; i++) await api(main, "POST", `/api/v1/compendiums/${cmp.id}/entities`, { kind: "character", name: `Listed ${i}` });
  const page = await fresh();
  await page.goto(`${base()}/c/${cmp.slug}/entities`);
  await waitFor(() => page.evaluate(() => !!document.querySelector("[data-live]")), "the page", 10000);
  await sleep(1500);
  const row = page.locator("#ent-list .ent-name a").nth(1);
  const id = await row.getAttribute("id");
  ok(id && id.startsWith("en-"), "rows have ids: " + id);
  await row.focus();
  // Two changes close together: the second arrives while the first fetch is in flight and must not be dropped.
  await api(main, "POST", `/api/v1/compendiums/${cmp.id}/entities`, { kind: "character", name: "Burst one" });
  await api(main, "POST", `/api/v1/compendiums/${cmp.id}/entities`, { kind: "character", name: "Burst two" });
  await waitFor(async () => { const t = await page.locator("#ent-list").innerText(); return t.includes("Burst one") && t.includes("Burst two"); }, "both changes to be listed", 15000);
  eq(await page.evaluate(() => document.activeElement.id), id, "focus stayed on the same row");
  await page.close();
});

await kit.test("the stream starts again when a page comes back from the back-forward cache", async () => {
  const page = await fresh();
  const reqs = [];
  page.on("request", (r) => { if (/\/api\/v1\/events/.test(r.url())) reqs.push(r.url()); });
  await page.goto(`${base()}/p/${book.slug}/outline`);
  await waitFor(() => reqs.length >= 1, "the stream to open", 10000);
  const n = reqs.length;
  await page.evaluate(() => { window.dispatchEvent(new Event("pagehide")); });
  await sleep(500);
  await page.evaluate(() => { const e = new Event("pageshow"); e.persisted = true; window.dispatchEvent(e); });
  await waitFor(() => reqs.length > n, "the stream to open again after pageshow", 8000);
  await page.close();
});

await kit.test("a name under the caret is announced and opened from the palette", async () => {
  await api(main, "POST", `/api/v1/compendiums/${cmp.id}/entities`, { kind: "character", name: "Marta Quill", summary: "The ferry rower." });
  const doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, content: "Marta Quill rowed out.\n" });
  const page = await fresh();
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await page.waitForSelector(".scriobh-prose .mention", { timeout: 25000 });
  await page.evaluate(() => {
    const v = window.scriobh.write.editor.view, m = document.querySelector(".scriobh-prose .mention");
    v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.near(v.state.doc.resolve(v.posAtDOM(m, 1)))));
    v.focus();
  });
  await waitFor(async () => /Marta Quill, Character\. The ferry rower/.test(await page.locator("#live").innerText()), "the card to be spoken in the live region", 6000);
  await page.keyboard.press(`${MOD}+Shift+P`); // inside the editor Mod+K is the link key
  await page.locator("#palette[open]").waitFor({ timeout: 5000 });
  await page.locator("#palette-input").fill("entity under");
  await page.getByRole("option", { name: /Open the entity under the cursor/ }).click();
  await page.waitForURL(/\/p\/.+\/e\/ent_/, { timeout: 10000 });
  await page.close();
});

await kit.test("the PDF preview frame carries no referrer and no delegated features, and is shown only after a click", async () => {
  const pdf = Buffer.from("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n");
  const up = await main.request.post(`${base()}/api/v1/projects/${book.id}/attachments?filename=review.pdf`, { data: pdf, headers: { "Content-Type": "application/pdf" } });
  const att = (await up.json()).item;
  const page = await fresh();
  await page.goto(`${base()}/p/${book.slug}/files/${att.id}`);
  eq(await page.locator("#att-frame iframe").count(), 0, "no frame before the click");
  ok((await page.locator("a.btn", { hasText: "Download the PDF" }).getAttribute("href")).includes("download=1"), "download is the first link");
  await page.locator("#att-preview").click();
  const f = page.locator("#att-frame iframe");
  await f.waitFor();
  eq(await f.getAttribute("referrerpolicy"), "no-referrer");
  eq(await f.getAttribute("allow"), "");
  await page.close();
});

await kit.test("the sheet's one-line fields are one line; a paragraph field is a text area", async () => {
  const e = (await api(main, "POST", `/api/v1/compendiums/${cmp.id}/entities`, { kind: "character", name: "Fielded" })).json.item;
  const page = await fresh();
  await page.goto(`${base()}/c/${cmp.slug}/e/${e.id}`);
  eq(await page.locator("#ef-role").evaluate((n) => n.tagName), "INPUT", "role is one line");
  eq(await page.locator("#ef-age").evaluate((n) => n.tagName), "INPUT", "age is one line");
  eq(await page.locator("#ef-appearance").evaluate((n) => n.tagName), "TEXTAREA", "appearance is a paragraph");
  await page.close();
});

await kit.test("on a phone the sheet's Save is kept above the tab bar, where a tap reaches it", async () => {
  const e = (await api(main, "POST", `/api/v1/compendiums/${cmp.id}/entities`, { kind: "character", name: "Tall sheet" })).json.item;
  const page = await fresh();
  await page.setViewportSize({ width: 390, height: 700 });
  await page.goto(`${base()}/c/${cmp.slug}/e/${e.id}`);
  await page.locator(".sticky-save button").waitFor({ state: "attached" });
  const at = await page.evaluate(() => {
    window.scrollTo(0, 0);
    const b = document.querySelector(".sticky-save button"), r = b.getBoundingClientRect(), x = r.left + r.width / 2, y = r.top + r.height / 2;
    const top = document.elementFromPoint(x, y);
    return { inView: r.bottom <= window.innerHeight && r.top >= 0, hit: top === b || b.contains(top), tab: document.querySelector(".tabbar").getBoundingClientRect().top, bottom: r.bottom };
  });
  ok(at.inView, "Save is on screen without scrolling: " + JSON.stringify(at));
  ok(at.hit, "a tap on Save reaches it, not the tab bar: " + JSON.stringify(at));
  ok(at.bottom <= at.tab + 1, "it sits above the tab bar: " + JSON.stringify(at));
  await page.close();
});

await kit.test("a board move that fails puts the Move to box back where the card is", async () => {
  kit.allowed = [/Failed to load resource/, /ERR_FAILED/];
  const note = (await api(main, "POST", `/api/v1/projects/${book.id}/research`, { kind: "note", title: "Board note", body: "text" })).json.item;
  const page = await fresh();
  await page.goto(`${base()}/p/${book.slug}/research/board`);
  await page.waitForSelector(`#c-${note.id}`);
  await page.route("**/research/board-move", (r) => r.abort());
  const sel = page.locator(`#c-${note.id} select[name=to]`);
  const from = await sel.inputValue();
  await page.locator(`#c-${note.id} .r-name`).focus();
  await page.keyboard.press("ArrowRight");
  await waitFor(async () => /could not be reached/.test(await page.locator("#r-live").innerText()), "the failure to be said", 8000);
  eq(await sel.inputValue(), from, "the box is back where the card is");
  await page.close();
  kit.allowed = [];
});

await kit.finish("e2e-review3");
