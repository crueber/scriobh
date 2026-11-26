#!/usr/bin/env node
// End-to-end test of the Phase 2 writing view: the inspector, the Versions panel (three clicks to restore
// yesterday's chapter, with a clock-shifted history), wiki links, front and back matter, and the
// "today" figure, in a real Chromium.
//
//   node scripts/e2e-write2.mjs [--only substring]
//
// "Yesterday" is made with the development build's clock hook: the first server runs with its clock a day
// back and writes the history through the ordinary ops; the second runs on the same data with the real clock.
import { armOk, createKit, eq, ok } from "./e2e-kit.mjs";
import { signIn, startServer } from "./harness.mjs";

const now = new Date();
const yesterdayNoon = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 12, 0, 0);
const offset = Math.round((yesterdayNoon.getTime() - now.getTime()) / 1000);
const kit = await createKit("e2e-write2", { env: { SCRIOBH_DEV_CLOCK_OFFSET_SECONDS: String(offset), SCRIOBH_SESSION_HOURS: "400", SCRIOBH_SESSION_IDLE_MINUTES: "100000" } });
const only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const V1 = "The harbour was quiet at dawn.\n\nMarta counted the boats twice.\n";
const V2 = "The harbour was quiet at dawn, and the tide had gone out.\n\nMarta counted the boats twice, then a third time.\n\nNobody came.\n";
const V3 = "Today's rewrite starts somewhere else entirely.\n";

let book, doc, harbour;
const text = () => page().evaluate(() => window.scriobh.write.editor.getMarkdown());
const stored = async (id) => (await api(main, "GET", `/api/v1/docs/${id}`)).json.item;
const panel = async (p, name) => {
  // Details has no toggle of its own since U5: the Sidebar toggle opens the inspector on the tab used last,
  // so the Details tab is chosen after it.
  if (name === "Details") {
    const side = p.getByRole("button", { name: "Sidebar", exact: true });
    if ((await side.getAttribute("aria-expanded")) !== "true") await side.click();
    const tab = p.getByRole("tab", { name: "Details", exact: true });
    if ((await tab.getAttribute("aria-selected")) !== "true") await tab.click();
    return;
  }
  const b = p.getByRole("button", { name, exact: true });
  if ((await b.getAttribute("aria-expanded")) !== "true") await b.click();
};
const open = async (path = `/p/${book.slug}/d/${doc}`) => {
  await page().goto(base() + path);
  await page().waitForSelector(".scriobh-prose");
  await waitFor(() => page().evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.inspector)), "the lazy parts to load", 10000);
};

await kit.test("setup: a history written yesterday, then a restart on the real clock", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "Versions book", kind: "novel" });
  ok(made.json && made.json.item, "project: " + made.status + JSON.stringify(made.json));
  book = made.json.item;
  const l = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json;
  doc = l.order[0];
  const h = await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Harbour", kind: "note", content: "A place." });
  harbour = h.json.item.id;
  let rev = (await stored(doc)).revision;
  eq((await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, title: "The Quiet Harbour", content: V1 })).status, 200, "save v1");
  eq((await api(main, "POST", `/api/v1/docs/${doc}/revisions`, { label: "First draft" })).status, 201, "snapshot 1");
  rev = (await stored(doc)).revision;
  eq((await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, content: V2 })).status, 200, "save v2");
  eq((await api(main, "POST", `/api/v1/docs/${doc}/revisions`, { label: "Second draft" })).status, 201, "snapshot 2");
  await pg.close();
  await kit.srv.stop();
  kit.srv = await startServer(kit.bin, kit.dataDir, { SCRIOBH_SESSION_HOURS: "400", SCRIOBH_SESSION_IDLE_MINUTES: "100000" });
  const page = await main.newPage();
  kit.watch(page, "main");
  kit.main = page;
  await pg.close().catch(() => {});
  globalThis.__pg = page;
  rev = (await stored(doc)).revision;
  eq((await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, content: V3 })).status, 200, "save v3 today");
  const days = (await api(main, "GET", `/api/v1/docs/${doc}/revisions/days?utc_offset=${-now.getTimezoneOffset()}`)).json.days;
  ok(days.length >= 1, "history has a day: " + JSON.stringify(days.map((d) => d.day)));
});
kit.only = only; // the setup always runs
const page = () => globalThis.__pg;

await kit.test("versions: three clicks restore yesterday's chapter", async () => {
  const p = page();
  await p.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "the inspector script", 10000);
  eq(await p.evaluate(() => window.scriobh.write.editor.getMarkdown()), V3.trimEnd() === V3 ? V3 : V3.replace(/\n$/, "") + "\n", "starts on today's text");
  const credit = async () => (await api(main, "GET", `/api/v1/projects/${book.id}/stats/today`)).json.words_added;
  const creditBefore = await credit();
  await p.getByRole("button", { name: "Versions", exact: true }).click();                              // click 1
  await p.getByRole("button", { name: /^Yesterday/ }).click();                            // click 2
  await p.getByRole("button", { name: /^Restore the version/ }).first().click();          // click 3
  await waitFor(async () => (await p.evaluate(() => window.scriobh.write.editor.getMarkdown())).includes("a third time"), "the editor to hold yesterday's text");
  await waitFor(async () => (await stored(doc)).content === V2, "the server to hold yesterday's text");
  eq(await credit(), creditBefore, "a restore is not counted as words added (V2 is longer than V3)");
  // History is append-only: the text it replaced is still there.
  const list = (await api(main, "GET", `/api/v1/docs/${doc}/revisions?limit=20`)).json.items;
  ok(list.some((r) => r.reason === "restore"), "a restore version was written: " + list.map((r) => r.reason).join(","));
  let found = false;
  for (const r of list) {
    const c = (await api(main, "GET", `/api/v1/docs/${doc}/revision?revision_id=${r.id}`)).json;
    if (c && c.content === V3) found = true;
  }
  ok(found, "today's replaced text is kept in the history");
});

await kit.test("versions: typing that has not been saved yet survives a restore", async () => {
  const p = page();
  await p.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "the inspector script", 10000);
  await p.locator(".scriobh-prose").click();
  await p.keyboard.press(`${MOD}+End`);
  await p.keyboard.type(" Unsaved tail.");
  await panel(p, "Versions");
  await p.getByRole("button", { name: /^Yesterday/ }).click();
  const first = p.getByRole("button", { name: /^Restore the version/ });
  await first.last().click(); // the oldest of yesterday's versions
  await waitFor(async () => !(await p.evaluate(() => window.scriobh.write.editor.getMarkdown())).includes("Unsaved tail"), "the editor to take the restored text");
  const items = (await api(main, "GET", `/api/v1/docs/${doc}/revisions?limit=30`)).json.items;
  let kept = false;
  for (const r of items) {
    const c = (await api(main, "GET", `/api/v1/docs/${doc}/revision?revision_id=${r.id}`)).json;
    if (c && c.content.includes("Unsaved tail")) kept = true;
  }
  ok(kept, "the unsaved text was written to the history before it was replaced");
});

await kit.test("versions: view, compare, name, rename and delete", async () => {
  const p = page();
  await p.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "the inspector script", 10000);
  await panel(p, "Versions");
  await p.getByRole("button", { name: /^Yesterday/ }).click();
  await p.getByRole("button", { name: /^View the version/ }).first().click();
  await p.locator(".ver-text").waitFor();
  ok((await p.locator(".ver-text").innerText()).length > 10, "the version text is shown");
  eq(await p.locator(".ver-text").getAttribute("aria-label"), "Text of this version, read only", "read-only label");
  await p.getByRole("button", { name: /All versions/ }).click();
  await p.getByRole("button", { name: /^Compare the version/ }).first().click();
  await p.locator(".ver-diff").waitFor();
  ok((await p.locator(".ver-diff ins, .ver-diff del").count()) > 0, "the comparison marks insertions or deletions");
  ok(/underlined/.test(await p.locator("#ipanel-versions").innerText()), "the legend says how changes are marked");
  await p.getByRole("button", { name: /All versions/ }).click();
  // Name the current text, then delete the named version.
  await p.getByLabel("Name for this version").fill("Before the rewrite");
  await p.getByRole("button", { name: "Keep a named version" }).click();
  await waitFor(async () => (await api(main, "GET", `/api/v1/docs/${doc}/revisions?reason=manual`)).json.items.some((r) => r.label === "Before the rewrite"), "the named version");
  await p.getByRole("button", { name: /^Today/ }).click();
  await p.getByRole("button", { name: /^Delete the named version/ }).first().click();
  await p.getByRole("button", { name: /^Confirm/ }).first().click();
  await waitFor(async () => !(await api(main, "GET", `/api/v1/docs/${doc}/revisions?reason=manual`)).json.items.some((r) => r.label === "Before the rewrite"), "the named version to go");
});

await kit.test("versions: a big cut keeps the text before it, and a restore gets it back", async () => {
  // Its own chapter, so the steps around it keep the text they expect.
  const long = Array.from({ length: 40 }, (_, i) => `Paragraph ${i + 1} holds a few ordinary words about the harbour and the boats that came in at dawn.`).join("\n\n") + "\n";
  const made = await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "The cut chapter", kind: "chapter", content: long });
  ok(made.json && made.json.item, "the chapter: " + made.status);
  const cut = made.json.item.id;
  const p = page();
  await p.goto(`${base()}/p/${book.slug}/d/${cut}`);
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "the inspector script", 10000);
  // Delete most of the chapter in the editor: select all and type one line.
  await p.locator(".scriobh-prose").click();
  await p.keyboard.press(`${MOD}+a`);
  await p.keyboard.type("A short line.");
  await waitFor(async () => (await stored(cut)).content.startsWith("A short line"), "the cut to be saved", 10000);
  await panel(p, "Versions");
  const row = p.locator("li.ver-item", { hasText: "Before a large cut" });
  await row.first().waitFor();
  ok((await p.getByText("Before a large cut", { exact: true }).count()) > 0, "the Versions list says Before a large cut");
  await row.getByRole("button", { name: /^Restore the version/ }).first().click();
  await waitFor(async () => (await stored(cut)).content.includes("Paragraph 40"), "the server to hold the text back", 10000);
  await waitFor(async () => (await p.evaluate(() => window.scriobh.write.editor.getMarkdown())).includes("Paragraph 40"), "the editor to hold the text back", 10000);
});

await kit.test("wiki links: completion, found and missing, create a note, follow", async () => {
  const p = page();
  await p.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.wiki)), "the wiki script", 10000);
  await sleep(500);
  await p.locator(".scriobh-prose").click();
  await p.keyboard.press(`${MOD}+End`);
  await p.keyboard.type(" See [[Har");
  const list = p.locator("#wiki-list");
  await list.waitFor({ state: "visible" });
  ok((await list.locator("[role=option]").count()) >= 1, "titles are offered");
  eq(await p.locator("#wiki-list [role=option]").first().getAttribute("aria-selected"), "true", "the first option is active");
  ok((await p.locator(".scriobh-prose").getAttribute("aria-activedescendant")) !== null, "the editor points at the active option");
  await p.keyboard.press("Enter");
  await list.waitFor({ state: "hidden" });
  ok((await p.evaluate(() => window.scriobh.write.editor.getMarkdown())).includes("[[Harbour]]"), "the link was inserted by Enter: " + (await text()));
  // Escape closes the list and inserts nothing.
  await p.keyboard.type(" and [[Zz");
  await list.waitFor({ state: "visible" });
  await p.keyboard.press("Escape");
  await list.waitFor({ state: "hidden" });
  ok(!(await text()).includes("[[Zz]]"), "Escape inserted nothing");
  // A missing note is marked, a found one is not.
  await p.keyboard.press("Backspace"); await p.keyboard.press("Backspace"); await p.keyboard.press("Backspace"); await p.keyboard.press("Backspace");
  await p.keyboard.type("[[Lighthouse]] ");
  await waitFor(() => p.locator(".wikilink-missing").count().then((n) => n === 1), "the missing link to be marked");
  eq(await p.locator(".wikilink-found").count(), 1, "one found link");
  ok((await p.evaluate(() => getComputedStyle(document.querySelector(".wikilink-missing"), "::after").content)).includes("?"), "the missing link carries a question mark, not just a colour");
  // Following the found link with Mod+Enter, from a selected link.
  await p.locator(".wikilink-found").click();
  await p.locator("#wiki-card").waitFor({ state: "visible" });
  ok(/Open note/.test(await p.locator("#wiki-card").innerText()), "the card offers Open note");
  await waitFor(async () => !(await p.evaluate(() => window.scriobh.write.unsaved())), "the text to be saved");
  await p.keyboard.press(`${MOD}+Enter`);
  await p.waitForURL(new RegExp(`/d/${harbour}$`));
  await p.waitForSelector(".scriobh-prose");
  // Back, then create the missing note from its card.
  await p.goBack();
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.wiki)), "the wiki script", 10000);
  await sleep(500);
  await p.locator(".wikilink-missing").click({ timeout: 4000 }).catch(async (e) => { throw new Error("no missing link after going back: " + (await p.evaluate(() => document.querySelector(".scriobh-prose").innerHTML)).slice(-400)); });
  await p.locator("#wiki-card").getByRole("button", { name: "Create note" }).click();
  await p.waitForURL((u) => /\/d\/doc_/.test(u.pathname) && !u.pathname.endsWith(doc) && !u.pathname.endsWith(harbour));
  await p.waitForSelector(".scriobh-prose");
  eq(await p.locator("#doc-title").inputValue(), "Lighthouse", "the new note has the link's title");
  const all = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.items.filter((d) => d.title === "Lighthouse");
  eq(all.length, 1, "exactly one Lighthouse note");
});

await kit.test("wiki links: Mod+Enter still inserts a scene break away from a link", async () => {
  const p = page();
  await p.goto(`${base()}/p/${book.slug}/d/${harbour}`);
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.wiki)), "the wiki script", 10000);
  await p.locator(".scriobh-prose").click();
  await p.keyboard.press(`${MOD}+End`);
  await p.keyboard.press(`${MOD}+Enter`);
  ok((await text()).includes("***") || (await text()).includes("---") || (await p.locator(".scriobh-prose hr, .scriobh-prose .scene-break").count()) > 0, "a scene break was inserted: " + (await text()));
});

await kit.test("inspector: details are saved with the revision, backlinks and links show, focus stays in the editor", async () => {
  kit.allowed = [/status of 409/]; // a details save may meet a text save in flight; it takes the new revision and retries
  const p = page();
  await p.goto(`${base()}/p/${book.slug}/d/${harbour}`);
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "the inspector script", 10000);
  await panel(p, "Details");
  ok(await p.locator("#inspector").isVisible(), "the inspector opened");
  const fromDoc = p.locator("#il-in li");
  await waitFor(async () => (await fromDoc.count()) >= 1, "a backlink from the chapter that links here");
  ok((await fromDoc.first().innerText()).includes("The Quiet Harbour"), "the backlink names the linking chapter: " + (await fromDoc.first().innerText()));
  await p.locator("#in-syn-s").fill("Where the boats come in.");
  await p.locator("#in-status").selectOption("revise");
  await p.locator("#in-target").fill("1200");
  await p.locator("#in-pov").fill("Marta");
  await p.locator("#in-loc").fill("The quay");
  await p.locator("#in-sd").fill("1923-04-02");
  await waitFor(async () => { const d = await stored(harbour); return d.synopsis_short === "Where the boats come in." && d.status === "revise" && d.target_words === 1200 && d.pov === "Marta" && d.location === "The quay" && d.story_date === "1923-04-02"; }, "the details to be saved");
  // The editor still saves after the inspector changed the revision: no conflict bar.
  await p.locator(".scriobh-prose").click();
  await p.keyboard.press(`${MOD}+End`);
  await p.keyboard.type(" More about the quay.");
  await waitFor(async () => (await stored(harbour)).content.includes("More about the quay"), "the text to save after the inspector's save");
  eq(await p.locator("#nb-conflict").count(), 0, "no conflict bar");
  // Typing in the editor never moves focus into the inspector, and a save never steals it.
  await p.keyboard.type(" Still typing.");
  await sleep(1800);
  eq(await p.evaluate(() => document.activeElement.closest(".scriobh-prose") !== null), true, "focus stayed in the editor");
  // A reload keeps the details.
  await open(`/p/${book.slug}/d/${harbour}`);
  await panel(p, "Details");
  eq(await p.locator("#in-syn-s").inputValue(), "Where the boats come in.", "synopsis after reload");
  // Escape closes it and returns focus to the button that opened it.
  await p.locator("#in-syn-s").focus();
  await p.keyboard.press("Escape");
  eq(await p.evaluate(() => document.activeElement.id), "insp-open", "focus returns to the opener");
  ok(!(await p.locator("#inspector").isVisible()), "the inspector closed");
});

await kit.test("inspector: one refused field does not hold back the others", async () => {
  kit.allowed = [/status of 422/];
  const p = page();
  await open(`/p/${book.slug}/d/${harbour}`);
  await panel(p, "Details");
  await p.locator("#in-target").fill("-5");
  await p.locator("#in-target").blur();
  await waitFor(async () => (await p.locator('[data-err="target_words"]').isVisible()), "the refusal to be shown");
  await p.locator("#in-pov").fill("Anselm");
  await waitFor(async () => (await stored(harbour)).pov === "Anselm", "the next field to save in spite of the refused one");
  await p.locator("#in-target").fill("900");
  await waitFor(async () => (await stored(harbour)).target_words === 900, "the corrected field to save");
  ok(!(await p.locator('[data-err="target_words"]').isVisible()), "the refusal clears");
});

await kit.test("focus dimming and the wiki marks are drawn together", async () => {
  const p = page();
  eq((await api(main, "PUT", "/api/v1/me/prefs", { items: { focus_dim: "true" } })).status, 200, "turn focus dimming on");
  await open(`/p/${book.slug}/d/${doc}`);
  await waitFor(() => p.evaluate(() => !!(window.scriobh.wiki)), "the wiki script", 10000);
  await sleep(600);
  await p.locator(".scriobh-prose").click();
  await waitFor(async () => (await p.locator(".scriobh-prose .is-active").count()) >= 1, "the active paragraph to be marked");
  ok((await p.locator(".wikilink-found, .wikilink-missing").count()) >= 1, "the wiki marks survive focus dimming");
  await api(main, "PUT", "/api/v1/me/prefs", { items: { focus_dim: "false" } });
});

await kit.test("front and back matter come from the add menu and are not numbered", async () => {
  const p = page();
  await p.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await p.waitForSelector(".scriobh-prose");
  await sleep(300);
  await p.locator("#add-btn").click();
  const before = p.url();
  await p.getByRole("menuitem", { name: "Add front matter" }).click();
  await p.waitForURL((u) => u.href !== before && /\/d\/doc_/.test(u.pathname));
  await p.waitForSelector(".scriobh-prose");
  await sleep(300);
  await p.locator("#add-btn").click();
  const before2 = p.url();
  await p.getByRole("menuitem", { name: "Add back matter" }).click();
  await p.waitForURL((u) => u.href !== before2 && /\/d\/doc_/.test(u.pathname));
  await p.waitForSelector(".scriobh-prose");
  const rows = await p.locator("#tree .tree-row").evaluateAll((l) => l.map((r) => r.dataset.kind + "|" + (r.querySelector(".tree-num") ? r.querySelector(".tree-num").textContent : "")));
  eq(rows[0].split("|")[0], "front_matter", "front matter first: " + rows.join(","));
  eq(rows[rows.length - 1].split("|")[0], "back_matter", "back matter last: " + rows.join(","));
  ok(rows.every((r) => r.split("|")[0].endsWith("matter") ? r.split("|")[1] === "" : true), "matter has no chapter number: " + rows.join(","));
  ok(/back matter/i.test(await p.locator("#doc-meta").innerText()), "the page labels it Back matter: " + (await p.locator("#doc-meta").innerText()));
});

await kit.test("the today figure follows the saves and the wiki list does not reflow the page", async () => {
  const p = page();
  await open(`/p/${book.slug}/d/${doc}`);
  await p.locator(".scriobh-prose").click();
  await p.keyboard.press(`${MOD}+End`);
  const box = await p.locator(".scriobh-prose").boundingBox();
  await p.keyboard.type(" Twenty brand new words to count toward the day: one two three four five six seven eight nine ten.");
  await waitFor(async () => (await p.locator("#st-today").isVisible()) && /Today \d+/.test(await p.locator("#st-today").innerText()), "the today figure", 12000);
  const after = await p.locator(".scriobh-prose").boundingBox();
  eq(Math.round(after.x), Math.round(box.x), "the page did not move sideways");
});

await kit.test("signing out first saves a detail typed a moment ago", async () => {
  const p = page();
  await open(`/p/${book.slug}/d/${harbour}`);
  await panel(p, "Details");
  await p.locator("#in-pov").fill("Typed just before signing out");
  // No blur, no pause: straight to the account menu.
  await p.getByRole("button", { name: /^Account/ }).click();
  await p.locator("#account").getByRole("button", { name: "Sign out" }).click();
  // Earlier steps left text the server never had in this browser (a held field and a draft), so the page asks before it discards it.
  // The browser's own confirm() is gone (a webview may have none); the question is the page's, and the answer is yes.
  ok((await armOk(p)) >= 1, "the page asked before discarding the text this browser held");
  await p.waitForURL((u) => !/^\/p\//.test(u.pathname), { timeout: 15000 });
  await signIn(p, base());
  eq((await stored(harbour)).pov, "Typed just before signing out", "the field was saved before the session ended");
});

await kit.test("phone: the inspector is a bottom sheet and the Versions panel fits", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  const p = await ctx.newPage();
  kit.watch(p, "phone");
  await p.goto(`${base()}/auth/login`);
  await p.goto(`${base()}/p/${book.slug}/d/${doc}`);
  if (/auth|dev-idp/.test(p.url())) {
    await p.getByRole("button", { name: /Sign in as writer/ }).click();
    await p.waitForURL((u) => !/dev-idp|auth/.test(u.pathname));
    await p.goto(`${base()}/p/${book.slug}/d/${doc}`);
  }
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "the inspector script", 10000);
  await p.getByRole("button", { name: "Versions", exact: true }).tap();
  const box = await p.locator("#inspector").boundingBox();
  ok(box && box.width >= 380 && box.y > 200, "the inspector is a sheet at the bottom: " + JSON.stringify(box));
  await p.getByRole("button", { name: /^Yesterday/ }).tap();
  const overflow = await p.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
  eq(overflow, false, "no sideways scroll");
  // One evaluation: the list may be redrawn by a live refresh between a locator's match and its measure, and a detached button measures 0 x 0.
  const small = await p.evaluate(() => [...document.querySelectorAll("#inspector button")].filter((b) => { const r = b.getBoundingClientRect(); return r.width > 0 && r.height > 0 && (r.height < 43.5 || r.width < 43.5); }).map((b) => b.textContent.trim().slice(0, 20)));
  ok(small.length === 0, "touch targets under 44 px: " + small.join(", "));
  await p.getByRole("tab", { name: "Details" }).tap();
  const small2 = await p.evaluate(() => [...document.querySelectorAll("#inspector button, #inspector input, #inspector select, #inspector textarea")].filter((b) => { const r = b.getBoundingClientRect(); return r.width > 0 && r.height > 0 && b.type !== "checkbox" && (r.height < 43.5 || (r.width < 43.5 && b.tagName === "BUTTON")); }).map((b) => (b.id || b.textContent.trim()).slice(0, 20)));
  ok(small2.length === 0, "details: touch targets under 44 px: " + small2.join(", "));
  eq(await p.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "details: no sideways scroll");
  await ctx.close();
});

await kit.finish("e2e-write2");
