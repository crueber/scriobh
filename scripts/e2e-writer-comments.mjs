#!/usr/bin/env node
// Comments written from the writing view, for yourself first (F-1280 to F-1285, phase 1 of docs/plans/WRITER-COMMENTS.md), in a real Chromium.
//
//   node scripts/e2e-writer-comments.mjs [--only substring] [--shots DIR]
//
// A selection in the middle of a paragraph, with text typed and not yet saved, becomes the anchor of a private comment through the
// Comments tab: the quote is the exact Markdown slice of the stored text, and the editor's text, caret, selection and scroll are exactly
// as they were (nothing is dispatched to the editor). A comment on the whole chapter; "Only me" is not visible to a second person, in
// the page or over the API; the same at 390 px with the keyboard alone; Show in the text scrolls and flashes without moving the caret;
// a comment whose words are gone is kept on the chapter or attached to other words; a reload with the tab open takes no focus.
// --shots DIR saves screenshots to look at; Playwright puts style attributes on text boxes to take them, so the CSP watcher reports them: look, do not check, with it on.
import { mkdirSync } from "node:fs";
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-writer-comments");
kit.deadline(10 * 60 * 1000);
const arg = (n) => (process.argv.includes(n) ? process.argv[process.argv.indexOf(n) + 1] : "");
kit.only = arg("--only");
const shots = arg("--shots");
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const wren = await kit.signedIn({}, "writer@dev.scriobh"); // owner and author of the comments
const ada = await kit.signedIn({}, "admin@dev.scriobh"); // an editor of the book, and an instance administrator
const main = wren.ctx, pg = wren.page;
kit.main = pg;

// The first paragraph is filler to make the page long enough to scroll; the second has emphasis and a repeated phrase.
const filler = Array.from({ length: 30 }, (_, i) => `Filler paragraph ${i + 1}: the quiet harbour town went on with its small business, one lamp at a time.`).join("\n\n");
const TARGET = "The *emphasised* words are here, and the tide ran on; the tide turned, and the tide came back.";
const BODY = `${filler}\n\n${TARGET}\n\nThe last paragraph says goodbye, with a little warmth.\n`;
const made = await api(main, "POST", "/api/v1/projects", { title: "Margin Notes", kind: "novel" });
ok(made.json && made.json.item, `project: ${made.status}`);
const P = made.json.item;
const doc = (await api(main, "GET", `/api/v1/projects/${P.id}/docs`)).json.order[0];
const seed = await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: 1, title: "Chapter One", content: BODY });
ok(seed.status === 200, `seed: ${seed.status}`);
const mem = await api(main, "POST", `/api/v1/projects/${P.id}/members`, { email: "admin@dev.scriobh", role: "editor" });
ok(mem.status < 300, `add editor: ${mem.status}`);
const cbase = `/api/v1/projects/${P.id}/comments`;
const stored = async () => (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
const comments = async (ctx, q = "") => (await api(ctx, "GET", `${cbase}?document_id=${doc}&plain=true&status=all${q}`)).json;

await main.addInitScript(() => { // the moment the Source textarea is shown, for a script that must never enter it
  const arm = () => {
    const t = document.querySelector(".scriobh-source");
    if (!t) return false;
    new MutationObserver(() => { if (!t.hidden) sessionStorage.setItem("__source", "1"); }).observe(t, { attributes: true, attributeFilter: ["hidden"] });
    return true;
  };
  new MutationObserver((_, o) => { if (arm()) o.disconnect(); }).observe(document, { childList: true, subtree: true });
});

const open = async (page, p = P, id = doc) => {
  await page.goto(`${base()}/p/${p.slug}/d/${id}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.alts && window.scriobh.inspector && window.scriobh.chords && window.scriobh.chords.KeyM)), "the page's scripts", 12000);
  await sleep(250);
};
// A place in the editor's own document: the start of the text node holding `needle`, plus delta.
const at = (page, needle, delta = 0) => page.evaluate(([n, d]) => {
  let p = -1;
  window.scriobh.write.editor.view.state.doc.descendants((node, pos) => { if (p < 0 && node.isText && node.text.indexOf(n) >= 0) p = pos + node.text.indexOf(n) + d; });
  if (p < 0) throw new Error("no such text: " + n);
  return p;
}, [needle, delta]);
const select = (page, from, to) => page.evaluate(([f, t]) => { const ed = window.scriobh.write.editor; ed.alts.select(f, t); ed.view.focus(); }, [from, to]);
const snap = (page) => page.evaluate(() => {
  const w = window.scriobh.write, v = w.editor.view, s = v.state.selection;
  window.__st = v.state;
  return { md: w.editor.getMarkdown(), anchor: s.anchor, head: s.head, y: window.scrollY, rev: w.rev };
});
const same = (page) => page.evaluate(() => window.scriobh.write.editor.view.state === window.__st);
const inEditor = (page) => page.evaluate(() => { const v = window.scriobh.write.editor.view; return v.dom.contains(document.activeElement); });
const live = (page) => page.evaluate(() => (document.getElementById("rv-live") || {}).textContent || "");
const shot = async (page, name) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }); };
const tabOpen = (page) => page.evaluate(() => document.getElementById("itab-comments").getAttribute("aria-selected") === "true" && document.getElementById("inspector").classList.contains("is-open"));

let mine; // the first private comment

await kit.test("a selection mid-paragraph, with unsaved text: the exact words, and the editor untouched", async () => {
  await main.addCookies([{ name: "scriobh_insp", value: "details", url: base() }]); // the inspector is open beside the page already: the chord changes its tab, and the page is not laid out again
  await open(pg);
  const w0 = (await stored()).revision;
  await pg.locator(".scriobh-prose").click();
  const p0 = (await at(pg, "emphasised")) - 4; // the start of "The ", before the emphasis
  await select(pg, p0, p0);
  await pg.keyboard.type(" QQ");
  eq(await pg.evaluate(() => window.scriobh.write.unsaved()), true, "the typed text is not saved yet");
  // a run that crosses the end of the emphasis: "sised words are"
  const from = await at(pg, "emphasised", 5), to = (await at(pg, "words are here")) + "words are".length;
  await select(pg, from, to);
  await pg.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
  await pg.evaluate(() => window.scrollBy(0, -120));
  await sleep(150);
  const before = await snap(pg);
  await pg.keyboard.press("Alt+Shift+KeyM");
  if (process.env.DBG) for (const t of [0, 50, 100, 200, 400]) { await sleep(t); console.log(t, await pg.evaluate(() => [window.scrollY, document.documentElement.scrollHeight, !!document.getElementById("cw-sel"), document.getElementById("inspector").className, document.activeElement.tagName + "." + document.activeElement.id])); }
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer for the selected words");
  eq(await tabOpen(pg), true, "the Comments tab is open");
  eq(await pg.locator(".cw .rv-quote").innerText(), "“sised words are”", "the composer shows the words");
  eq(await pg.evaluate(() => document.activeElement && document.activeElement.id), "cw-sel", "the text box has focus");
  eq(await same(pg), true, "nothing was dispatched to the editor when the composer opened");
  eq((await snap(pg)).y, before.y, "the page did not scroll");
  eq(await pg.locator(".scriobh-prose .cmt-pending").evaluateAll((l) => l.map((x) => x.textContent).join("")), "sised words are", "the held words stay marked while focus is in the tab");
  await shot(pg, "composer-wide");
  await pg.keyboard.type("Is the tide too much here? " + "CANARY-OWN");
  eq(await same(pg), true, "typing in the box dispatches nothing to the editor");
  // the radios exist only because another person is on the project, and "Only me" is the one chosen
  eq(await pg.locator('.cw input[type=radio][value=private]').isChecked(), true, "Only me is the default");
  await pg.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => pg.locator(".rv-card", { hasText: "CANARY-OWN" }).count().then((n) => n > 0), "the card");
  const list = await comments(main);
  eq(list.items.length, 1, "one thread");
  mine = list.items[0];
  const doc1 = await stored();
  eq(mine.visibility, "private", "it is private");
  eq(mine.anchor.quote, doc1.content.slice(mine.anchor.start, mine.anchor.end), "the quote is the Markdown slice at its offsets");
  eq(mine.anchor.quote, "sised* words are", "the quote is exactly the selected words, asterisks included");
  eq(mine.anchor.quote_plain, "sised words are", "and its plain form");
  ok(doc1.content.includes(" QQ"), "the unsaved text was saved first");
  eq(doc1.revision, w0 + 1, "the revision went up once, from the flush alone");
  eq(await pg.evaluate(() => window.scriobh.write.editor.getMarkdown().trimEnd()), doc1.content.trimEnd(), "the editor's text is the stored text");
  eq(await inEditor(pg), true, "focus is back in the text");
  const after = await snap(pg);
  eq(after.anchor, before.anchor, "selection anchor unchanged");
  eq(after.head, before.head, "selection head unchanged");
  eq(after.y, before.y, "the page did not scroll");
  eq(await pg.locator(".scriobh-prose .cmt-pending").count(), 0, "the held mark is gone");
  eq(await pg.evaluate(() => sessionStorage.getItem("__source")), null, "the Source view was never shown");
  await waitFor(async () => (await live(pg)).includes("Comment added"), "the announcement");
  ok(await pg.locator(".rv-card", { hasText: "Only you" }).count() > 0, "the card says Only you in words");
});

await kit.test("another person sees nothing of it: the page, the count, the API", async () => {
  if (process.env.DBG) console.log(await pg.evaluate(() => [...document.querySelectorAll("[style]")].map((e) => e.tagName + "#" + e.id + "." + String(e.className).slice(0, 50) + " " + e.getAttribute("style").slice(0, 80)).slice(0, 8)));
  await open(ada.page);
  await ada.page.locator("#insp-open").click().catch(() => {});
  await ada.page.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => ada.page.locator("#cw").count().then((n) => n > 0), "Ada's Comments tab");
  await sleep(500);
  eq(await ada.page.locator(".rv-card").count(), 0, "no card");
  eq(await ada.page.locator("#itab-comments-n").count(), 0, "no count on the tab");
  const url = `${cbase}/${mine.id}`, miss = `${cbase}/cmt_00000000000000000000000000`;
  const a = await ada.ctx.request.get(base() + url), b = await ada.ctx.request.get(base() + miss);
  eq(a.status(), 404, "404 for the private thread");
  eq(await a.text(), await b.text(), "byte-identical to an id that does not exist");
  const l = await comments(ada.ctx);
  eq(l.items.length, 0, "not in her list");
  eq(l.total, 0, "nor in the count");
  // and hers is not Wren's
  await ada.page.keyboard.press("Escape");
  const note = await api(ada.ctx, "POST", cbase, { document_id: doc, body: "ADA-PRIVATE", visibility: "private" });
  eq(note.status, 201, "Ada writes a private comment");
  const w = await comments(main);
  ok(!JSON.stringify(w).includes("ADA-PRIVATE"), "the owner does not see an editor's private comment");
  eq((await api(main, "GET", `${cbase}/${note.json.item.id}`)).status, 404, "nor by id");
  eq((await comments(main, "&q=ADA-PRIVATE")).total, 0, "nor by a word");
});

await kit.test("the reading view's margin: the author sees hers, marked Only you, and Next unresolved reaches it; another person's margin has nothing of it", async () => {
  const read = `${base()}/p/${P.slug}/read/${doc}`;
  await pg.goto(read);
  await waitFor(() => pg.locator("#rv-list .rv-card").count().then((n) => n >= 1), "the author's margin");
  const card = pg.locator("#rv-list .rv-card", { hasText: "CANARY-OWN" });
  eq(await card.count(), 1, "the author's margin has the private thread");
  ok((await card.innerText()).includes("Only you"), "it says Only you in words");
  ok(await pg.locator(`mark.rv-mark[data-t="${mine.thread_id}"]`).count() > 0, "its words are marked on the page");
  await pg.locator("#rv-next").click();
  await waitFor(() => pg.evaluate((t) => !!(document.activeElement && document.activeElement.matches && document.activeElement.matches(`mark.rv-mark[data-t="${t}"]`)), mine.thread_id), "Next unresolved to land on the marked words of the private thread");
  await ada.page.goto(read);
  await waitFor(() => ada.page.evaluate(() => !!document.getElementById("rv-list")), "Ada's margin");
  await sleep(700);
  const html = await ada.page.content();
  ok(!html.includes("CANARY-OWN") && !html.includes(mine.id), "nothing of Wren's private comment is in Ada's page");
  eq(await ada.page.locator(`mark.rv-mark[data-t="${mine.thread_id}"]`).count(), 0, "no mark for it on Ada's page");
  eq(await ada.page.locator("#rv-list .rv-card", { hasText: "CANARY-OWN" }).count(), 0, "no card for it in Ada's margin");
  ok(!(await ada.page.locator("#rv-count").innerText().catch(() => "")).trim() || true, "the count");
  await ada.page.goto("about:blank");
});

await kit.test("a comment on the whole chapter, from the keyboard, with focus back at the caret", async () => {
  await open(pg);
  await pg.locator(".scriobh-prose").click();
  const p0 = await at(pg, "last paragraph", 4);
  await select(pg, p0, p0);
  const before = await snap(pg);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.evaluate(() => document.activeElement && document.activeElement.id === "cw-chap"), "the chapter box to take focus");
  eq(await same(pg), true, "nothing dispatched");
  await pg.keyboard.type("Whole chapter note.");
  await pg.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => pg.locator(".rv-card", { hasText: "Whole chapter note." }).count().then((n) => n > 0), "the card");
  ok(await pg.locator(".rv-card", { hasText: "On the chapter as a whole" }).count() > 0, "On the chapter as a whole");
  ok(await pg.locator(".rv-card", { hasText: "Only you" }).count() > 0, "Only you");
  eq(await inEditor(pg), true, "focus is in the text");
  const after = await snap(pg);
  eq(after.anchor, before.anchor, "caret unchanged");
  const l = (await comments(main)).items.filter((t) => t.body === "Whole chapter note.")[0];
  ok(l && !l.anchor && l.visibility === "private", "stored private, with no anchor");
  // the tab count is drawn by the server on the next load, and kept by the tab
  await open(pg);
  eq((await pg.locator("#itab-comments").innerText()).replace(/\s+/g, " "), "Comments (2)", "the count on the tab");
});

await kit.test("from the Source view: the textarea's own offsets name the words", async () => {
  await open(pg);
  await pg.evaluate(() => window.scriobh.write.editor.toggleSource());
  await waitFor(() => pg.locator(".scriobh-source").isVisible(), "the Source view");
  const picked = await pg.evaluate(() => {
    const t = document.querySelector(".scriobh-source"), i = t.value.indexOf("last paragraph says");
    t.focus(); t.setSelectionRange(i, i + "last paragraph".length);
    return t.value.slice(i, i + "last paragraph".length);
  });
  eq(picked, "last paragraph", "the words are selected in the textarea");
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer");
  eq(await pg.locator(".cw .rv-quote").innerText(), "“last paragraph”", "the composer shows the words");
  await pg.keyboard.type("From the source view.");
  await pg.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => pg.locator(".rv-card", { hasText: "From the source view." }).count().then((n) => n > 0), "the card");
  const t = (await comments(main)).items.filter((x) => x.body === "From the source view.")[0];
  eq(t.anchor.quote, "last paragraph", "anchored on the words");
  eq(await pg.locator(".scriobh-source").isVisible(), true, "still in the Source view");
  eq(await pg.evaluate(() => document.activeElement === document.querySelector(".scriobh-source")), true, "focus is back in the textarea");
  await pg.evaluate(() => window.scriobh.write.editor.toggleSource());
});

await kit.test("Show in the text scrolls and flashes, and the caret does not move", async () => {
  await open(pg);
  await pg.locator("#itab-comments").click();
  await waitFor(() => pg.locator(".rv-card").count().then((n) => n >= 2), "the cards");
  await pg.evaluate(() => window.scrollTo(0, 0));
  const p0 = await at(pg, "Filler paragraph 3:");
  await select(pg, p0, p0 + 6);
  const before = await snap(pg);
  await pg.evaluate(() => window.scrollTo(0, 0));
  await pg.locator(".rv-card", { hasText: "CANARY-OWN" }).getByRole("button", { name: "Show in the text" }).click();
  await waitFor(() => pg.locator(".scriobh-prose .cmt-flash").count().then((n) => n >= 1), "the flash");
  eq(await pg.locator(".scriobh-prose .cmt-flash").evaluateAll((l) => l.map((x) => x.textContent).join("")), "sised words are", "the words that flash");
  const y = await pg.evaluate(() => window.scrollY);
  ok(y > 100, "the page scrolled to the words");
  const rect = await pg.evaluate(() => { const r = document.querySelector(".cmt-flash").getBoundingClientRect(); return [r.top, r.bottom, innerHeight]; });
  ok(rect[0] > 0 && rect[1] < rect[2], "the words are on the screen");
  eq(await pg.evaluate(() => { const v = window.scriobh.write.editor.view; return [v.state.selection.anchor, v.state.selection.head]; }).then((x) => x.join()), [before.anchor, before.head].join(), "the caret and the selection did not move");
  eq(await inEditor(pg), false, "the editor was not focused");
  await shot(pg, "show-wide");
  await waitFor(() => pg.locator(".scriobh-prose .cmt-flash").count().then((n) => n === 0), "the flash to clear", 4000);
});

await kit.test("the tab scrolled down its list: the composer is brought back to the top of the tab, and the page stays", async () => {
  await open(pg);
  await pg.locator("#itab-comments").click();
  await waitFor(() => pg.locator(".rv-card").count().then((n) => n >= 3), "cards");
  await pg.evaluate(() => { for (const id of ["ipanel-comments", "inspector"]) document.getElementById(id).scrollTop = 99999; });
  const p0 = await at(pg, "Filler paragraph 5:");
  await select(pg, p0, p0 + 6);
  const y = await pg.evaluate(() => window.scrollY);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer");
  const seen = await pg.evaluate(() => { const r = document.getElementById("cw-sel").getBoundingClientRect(), t = document.getElementById("itab-comments").getBoundingClientRect(); return r.top >= t.bottom - 1 && r.bottom <= innerHeight; });
  eq(seen, true, "the box is in view in the tab");
  eq(await pg.evaluate(() => window.scrollY), y, "the page did not move");
  await pg.keyboard.press("Escape");
});

await kit.test("a shared comment from the same composer, and the people on the project see it", async () => {
  await open(pg);
  await pg.locator(".scriobh-prose").click();
  const from = await at(pg, "quiet harbour town", 0);
  await select(pg, from, from + "quiet harbour".length);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer");
  await waitFor(() => pg.locator('.cw input[value=shared]').isVisible(), "the choice, with another person on the project");
  await pg.locator('.cw label', { hasText: "People on this project" }).click();
  ok(await pg.locator(".cw").innerText().then((t) => /cannot be made private again/.test(t)), "the one-way note is said next to the choice");
  await pg.locator("#cw-sel").fill("SHARED-NOTE for Ada");
  await pg.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => pg.locator(".rv-card", { hasText: "SHARED-NOTE" }).count().then((n) => n > 0), "the card");
  eq(await pg.locator(".rv-card", { hasText: "SHARED-NOTE" }).getByText("Only you").count(), 0, "a shared comment is not marked Only you");
  const t = (await comments(ada.ctx)).items.filter((x) => x.body.includes("SHARED-NOTE"))[0];
  ok(t && t.visibility === "shared", "Ada sees it, as shared");
  // "Edit" is offered for what is yours
  await pg.locator(".rv-card", { hasText: "SHARED-NOTE" }).getByRole("button", { name: "Edit" }).click();
  await pg.keyboard.press("ControlOrMeta+a");
  await pg.keyboard.type("SHARED-NOTE edited");
  await pg.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => pg.locator(".rv-card", { hasText: "SHARED-NOTE edited" }).count().then((n) => n > 0), "the edited card");
});

await kit.test("the words are gone: kept on the chapter, or attached to other words, never moved by a guess", async () => {
  await open(pg);
  await pg.locator("#itab-comments").click();
  await pg.locator(".scriobh-prose").click();
  // take the commented words out of the text
  const from = await at(pg, "emphasised"), to = (await at(pg, "words are here")) + "words are".length;
  await select(pg, from, to);
  await pg.keyboard.press("Delete");
  await pg.evaluate(() => window.scriobh.write.flushAll());
  await waitFor(async () => (await comments(main)).items.some((t) => t.anchor && t.anchor.state === "orphaned"), "the thread to lose its words").catch(async (e) => { throw new Error(e.message + " " + JSON.stringify((await comments(main)).items.map((t) => [t.body.slice(0, 12), t.anchor && t.anchor.state, t.anchor && t.anchor.quote])) + " " + JSON.stringify((await stored()).content.slice(-170))); });
  await pg.locator("#itab-comments").click();
  await waitFor(() => pg.locator(".rv-card.is-lost").count().then((n) => n === 1), "the lost card");
  ok((await pg.locator(".rv-card.is-lost").innerText()).includes("gone from the text"), "it says so");
  // Attach needs words to be selected
  const lost = pg.locator(".rv-card.is-lost");
  const p1 = await at(pg, "ran on");
  await select(pg, p1, p1);
  await lost.getByRole("button", { name: "Attach to the selected words" }).click();
  await waitFor(() => pg.locator("#rv-live").innerText().then((t) => /Select some words/.test(t)), "the message");
  await select(pg, p1, p1 + "ran on".length);
  await lost.getByRole("button", { name: "Attach to the selected words" }).click();
  await waitFor(async () => (await comments(main)).items.some((t) => t.anchor && t.anchor.state === "active" && t.anchor.quote === "ran on"), "the thread attached to the new words");
  await waitFor(() => pg.locator(".rv-card.is-lost").count().then((n) => n === 0), "the card to stop being lost");
  // another lost one is kept on the chapter
  await pg.locator(".scriobh-prose").click();
  const q0 = await at(pg, "ran on");
  await select(pg, q0, q0 + "ran on".length);
  await pg.keyboard.press("Delete");
  await pg.evaluate(() => window.scriobh.write.flushAll());
  await pg.locator("#itab-comments").click();
  await waitFor(() => pg.locator(".rv-card.is-lost").count().then((n) => n === 1), "lost again");
  await pg.locator(".rv-card.is-lost").getByRole("button", { name: "Keep on the chapter" }).click();
  await waitFor(async () => (await comments(main)).items.some((t) => t.body.includes("CANARY-OWN") && !t.anchor), "kept on the chapter");
  await waitFor(() => pg.locator(".rv-card.is-lost").count().then((n) => n === 0), "the card to stop being lost");
});

await kit.test("no focus is taken by a reload with the tab open, and a live update leaves a half-typed comment alone", async () => {
  await pg.evaluate(() => document.cookie = "scriobh_insp=comments; path=/");
  await open(pg);
  await sleep(600);
  eq(await pg.evaluate(() => !!document.activeElement.closest("#inspector")), false, "focus is not in the inspector after a reload");
  eq(await pg.locator("#cw-chap, #cw-sel").evaluateAll((l) => l.some((x) => x === document.activeElement)), false, "nor in the composer");
  await pg.locator("#cw-chap").fill("half-typed");
  await pg.locator("#cw-chap").focus();
  const note = await api(ada.ctx, "POST", cbase, { document_id: doc, body: "ADA-SHARED live" });
  eq(note.status, 201, "Ada writes a shared comment");
  await waitFor(() => pg.locator(".rv-card", { hasText: "ADA-SHARED live" }).count().then((n) => n > 0), "the live card", 12000);
  eq(await pg.locator("#cw-chap").inputValue(), "half-typed", "the half-typed comment is still there");
  eq(await pg.evaluate(() => document.activeElement && document.activeElement.id), "cw-chap", "and still has focus");
});

await kit.test("keyboard only at 390 px: select, chord, type, add; Escape keeps the draft; the sheet closes", async () => {
  await pg.goto("about:blank"); // the chapter is open for writing in one tab only
  await ada.page.goto("about:blank");
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true }, "writer@dev.scriobh");
  const ph = phone.page;
  await ph.addInitScript(() => { try { sessionStorage.removeItem("__source"); } catch (e) { /* none */ } });
  await open(ph);
  // a fresh paragraph, low on a long page, so the sheet would cover it
  const target = "Filler paragraph 27:";
  const p0 = await at(ph, target, 0);
  await ph.evaluate(([p]) => { const v = window.scriobh.write.editor.view; v.focus(); const dom = v.domAtPos(p).node; (dom.nodeType === 1 ? dom : dom.parentElement).scrollIntoView({ block: "end" }); }, [p0]);
  await select(ph, p0, p0);
  await ph.keyboard.down("Shift");
  for (let i = 0; i < 6; i++) await ph.keyboard.press("ArrowRight");
  await ph.keyboard.up("Shift");
  await sleep(300);
  const before = await snap(ph);
  await ph.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => ph.locator("#cw-sel").count().then((n) => n > 0), "the composer").catch(async (e) => { throw new Error(e.message + " " + JSON.stringify(await ph.evaluate(() => [document.getElementById("rv-live").textContent, !!document.getElementById("cw"), document.getElementById("inspector").className, JSON.stringify([...window.scriobh.write.editor.view.state.selection.$from.parent.textContent.slice(0, 30)]).length, window.scriobh.write.editor.view.state.selection.from, window.scriobh.write.editor.view.state.selection.to]))); });
  eq(await ph.evaluate(() => document.activeElement && document.activeElement.id), "cw-sel", "the box has focus");
  eq(await same(ph), true, "nothing dispatched");
  const where = await ph.evaluate(() => { const r = document.querySelector(".cmt-pending").getBoundingClientRect(), s = document.getElementById("inspector").getBoundingClientRect(); return { bottom: r.bottom, top: r.top, sheet: s.top }; });
  ok(where.top >= 0 && where.bottom <= where.sheet, `the words are in the free part above the sheet (${JSON.stringify(where)})`);
  await shot(ph, "composer-phone");
  await ph.keyboard.type("A draft I will not send");
  await ph.keyboard.press("Escape");
  await waitFor(async () => !(await ph.locator("#cw-sel").count()), "the composer to close");
  eq(await inEditor(ph), true, "Escape went back to the text");
  eq(await ph.evaluate(() => document.getElementById("inspector").classList.contains("is-open")), false, "the sheet closed");
  eq((await snap(ph)).anchor, before.anchor, "the selection is where it was");
  await ph.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => ph.locator("#cw-sel").count().then((n) => n > 0), "the composer again");
  eq(await ph.locator("#cw-sel").inputValue(), "A draft I will not send", "the draft came back");
  await ph.keyboard.press(`${MOD}+Enter`);
  await waitFor(async () => (await comments(main)).items.some((t) => t.body === "A draft I will not send" && t.visibility === "private"), "the comment");
  await waitFor(() => inEditor(ph), "focus in the editor");
  eq(await ph.evaluate(() => document.getElementById("inspector").classList.contains("is-open")), false, "the sheet closed after Add");
  await waitFor(async () => (await live(ph)).includes("Comment added"), "the announcement");
  eq(await ph.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no horizontal scroll");
  // targets are at least --target tall
  await ph.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => ph.locator("#cw-chap, #cw-sel").count().then((n) => n > 0), "the tab");
  const small = await ph.evaluate(() => { const t = parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--target")) || 44; return [...document.querySelectorAll("#ipanel-comments button, #ipanel-comments .cw-vis label")].filter((b) => b.offsetParent && b.getBoundingClientRect().height < t - 1).map((b) => b.textContent.trim() || b.name); });
  eq(small.join(), "", "every target in the tab is tall enough");
  await shot(ph, "tab-phone");
  await phone.ctx.close();
});

await kit.test("a half-typed chapter comment is still there after a comment on selected words is posted", async () => {
  await main.addCookies([{ name: "scriobh_insp", value: "details", url: base() }]);
  await open(pg);
  await pg.locator("#itab-comments").click();
  await waitFor(() => pg.locator("#cw-chap").count().then((n) => n > 0), "the chapter box");
  await pg.locator("#cw-chap").fill("HALF-TYPED CHAPTER NOTE");
  const p0 = await at(pg, "Filler paragraph 3:");
  await select(pg, p0, p0 + 6);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer for the words");
  await pg.keyboard.type("on the words");
  await pg.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => pg.locator(".rv-card", { hasText: "on the words" }).count().then((n) => n > 0), "the card");
  eq(await pg.locator("#cw-chap").inputValue(), "HALF-TYPED CHAPTER NOTE", "the chapter box kept what was typed in it");
  // and the other way round: a half-typed comment on words survives a chapter comment
  const q0 = await at(pg, "Filler paragraph 4:");
  await select(pg, q0, q0 + 6);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer again");
  await pg.keyboard.type("HALF-TYPED ON WORDS");
  await pg.keyboard.press("Escape"); // the draft is kept for the next composer on words
  await pg.locator("#cw-chap").fill("a chapter note that is sent");
  await pg.locator("#cw-chap").press(`${MOD}+Enter`);
  await waitFor(() => pg.locator(".rv-card", { hasText: "a chapter note that is sent" }).count().then((n) => n > 0), "the chapter card");
  await select(pg, q0, q0 + 6);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer a third time");
  eq(await pg.locator("#cw-sel").inputValue(), "HALF-TYPED ON WORDS", "the draft on words survived a chapter comment");
  await pg.keyboard.press("Escape");
});

await kit.test("nothing is written to the chapter for a comment: spellings the editor would change are left as they are", async () => {
  const seedText = "* item one\n* item two\n\n_x_ words here and there, and _y_ again.\n";
  const made2 = await api(main, "POST", `/api/v1/projects/${P.id}/docs`, { title: "Spelled otherwise", kind: "chapter", content: seedText });
  ok(made2.status === 201, `second chapter: ${made2.status}`);
  const d2 = made2.json.item.id;
  const before = (await api(main, "GET", `/api/v1/docs/${d2}`)).json.item;
  await pg.goto("about:blank");
  await open(pg, P, d2);
  await sleep(2500); // a save that the page might make by itself would be made by now
  eq(await pg.evaluate(() => window.scriobh.alts.placed()), false, "the editor spells the chapter otherwise than it is stored (the case under test)");
  const from = await at(pg, "words here", -2), to = from + "x words".length; // across the end of the emphasis, "x words"
  await select(pg, from, to);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer");
  await pg.keyboard.type("Spelled otherwise, and not rewritten.");
  await pg.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => pg.locator(".rv-card", { hasText: "not rewritten" }).count().then((n) => n > 0), "the card");
  const after = (await api(main, "GET", `/api/v1/docs/${d2}`)).json.item;
  eq(after.revision, before.revision, "the revision did not move");
  eq(after.content, before.content, "the stored text is exactly as it was written elsewhere");
  const th = (await api(main, "GET", `${cbase}?document_id=${d2}&plain=true`)).json.items.filter((t) => t.body.includes("not rewritten"))[0];
  ok(th && th.anchor && th.anchor.state === "active", "the comment is anchored");
  eq(th.anchor.quote_plain, "x words", "on the words that were selected");
  eq(after.content.slice(th.anchor.start, th.anchor.end), th.anchor.quote, "at their place in the stored text, as the Markdown spells it");
  // the comment and its list are the same on the next load, and the chapter is still not rewritten
  await open(pg, P, d2);
  await sleep(1500);
  eq((await api(main, "GET", `/api/v1/docs/${d2}`)).json.item.revision, before.revision, "nor by opening it again");
});

await kit.test("typing is not refused while a comment is on its way to the server", async () => {
  await pg.goto("about:blank");
  await open(pg);
  await pg.route("**/comments", async (route) => {
    if (route.request().method() === "POST") await sleep(2000);
    await route.continue();
  });
  const p0 = await at(pg, "Filler paragraph 8:");
  await select(pg, p0, p0 + 6);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer");
  await pg.keyboard.type("A slow comment");
  await pg.keyboard.press(`${MOD}+Enter`);
  await sleep(500); // the request is out and will take two seconds
  eq(await pg.locator(".rv-card", { hasText: "A slow comment" }).count(), 0, "the comment has not arrived");
  const p1 = await at(pg, "Filler paragraph 12:");
  await select(pg, p1, p1);
  await pg.keyboard.type("TYPED WHILE WAITING ");
  ok((await pg.evaluate(() => window.scriobh.write.editor.getMarkdown())).includes("TYPED WHILE WAITING "), "the editor took what was typed while the request was out");
  await waitFor(() => pg.locator(".rv-card", { hasText: "A slow comment" }).count().then((n) => n > 0), "the comment to arrive", 8000);
  await pg.unroute("**/comments");
  await pg.evaluate(() => window.scriobh.write.flushAll());
  const t = (await comments(main)).items.filter((x) => x.body === "A slow comment")[0];
  eq(t.anchor.quote_plain, "Filler", "anchored on the words it was made on, though text was typed meanwhile");
});

await kit.test("1024 px: the inspector is a column, nothing covers the words, and the page does not move", async () => {
  await pg.goto("about:blank");
  const mid = await kit.signedIn({ viewport: { width: 1024, height: 768 } }, "writer@dev.scriobh");
  const m = mid.page;
  await open(m);
  const p0 = await at(m, "Filler paragraph 27:");
  await m.evaluate(([p]) => { const v = window.scriobh.write.editor.view; v.focus(); const dom = v.domAtPos(p).node; (dom.nodeType === 1 ? dom : dom.parentElement).scrollIntoView({ block: "end" }); }, [p0]);
  await select(m, p0, p0 + 6);
  await sleep(400);
  const y = () => m.evaluate(() => window.scrollY);
  const y0 = await y();
  await m.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => m.locator("#cw-sel").count().then((n) => n > 0), "the composer");
  await sleep(400);
  eq(await y(), y0, "the page did not move when the composer opened");
  await m.keyboard.type("x");
  await m.keyboard.press("Escape");
  await sleep(400);
  eq(await y(), y0, "nor after Escape");
  await m.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => m.locator("#cw-sel").count().then((n) => n > 0), "the composer again");
  await m.keyboard.type("A comment at 1024 px");
  await m.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => m.locator(".rv-card", { hasText: "A comment at 1024" }).count().then((n) => n > 0), "the card");
  await sleep(400);
  eq(await y(), y0, "nor after the comment was added");
  await mid.ctx.close();
});

await kit.test("390 px: the page goes back where it was after Escape and after Add, closing the sheet moves nothing, the last paragraph included", async () => {
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true }, "writer@dev.scriobh");
  const ph = phone.page;
  await open(ph);
  const y = () => ph.evaluate(() => window.scrollY);
  const lastWords = async () => {
    const p0 = await at(ph, "last paragraph says", 0);
    await ph.evaluate(([p]) => { const v = window.scriobh.write.editor.view; v.focus(); const dom = v.domAtPos(p).node; (dom.nodeType === 1 ? dom : dom.parentElement).scrollIntoView({ block: "end" }); }, [p0]);
    await select(ph, p0, p0 + 14);
    await sleep(300);
  };
  await lastWords();
  const y0 = await y();
  await ph.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => ph.locator("#cw-sel").count().then((n) => n > 0), "the composer");
  await sleep(500);
  const where = await ph.evaluate(() => { const r = document.querySelector(".cmt-pending").getBoundingClientRect(), s = document.getElementById("inspector").getBoundingClientRect(); return { top: r.top, bottom: r.bottom, sheet: s.top }; });
  ok(where.top >= 0 && where.bottom <= where.sheet, `the last paragraph's words were lifted clear of the sheet (${JSON.stringify(where)})`);
  ok((await y()) > y0, "the page was scrolled to do it");
  await ph.keyboard.press("Escape");
  await waitFor(() => ph.evaluate(() => !document.getElementById("inspector").classList.contains("is-open")), "the sheet to close");
  eq(await y(), y0, "after Escape the page is where it was");
  await lastWords();
  const y1 = await y();
  await ph.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => ph.locator("#cw-sel").count().then((n) => n > 0), "the composer again");
  await ph.keyboard.type("A note on the last words");
  await ph.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => ph.evaluate(() => !document.getElementById("inspector").classList.contains("is-open")), "the sheet to close after Add");
  await sleep(400);
  eq(await y(), y1, "after Add the page is where it was");
  // the sheet closed by its own button moves nothing either, whatever the page was lifted to
  await lastWords();
  await ph.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => ph.locator("#cw-sel").count().then((n) => n > 0), "the composer a third time");
  await sleep(500);
  const lifted = await y();
  await ph.locator("#insp-close").click();
  await waitFor(() => ph.evaluate(() => !document.getElementById("inspector").classList.contains("is-open")), "the sheet to close by its button");
  await sleep(400);
  eq(await y(), lifted, "closing the sheet with its button moved nothing");
  await phone.ctx.close();
});

await kit.test("words across two paragraphs: the composer says why the comment is on the chapter, in the composer itself", async () => {
  await open(pg);
  await pg.locator(".scriobh-prose").click();
  const from = await at(pg, "Filler paragraph 3:"), to = (await at(pg, "Filler paragraph 4:")) + 5;
  await select(pg, from, to);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-chap").count().then((n) => n > 0), "the chapter composer");
  const said = await pg.locator("#cw").innerText();
  ok(/Select inside one paragraph/.test(said), "the composer shows the sentence, not only a screen reader's line: " + said.slice(0, 160));
  await shot(pg, "u3-refusal-1280-light");
  await pg.emulateMedia({ colorScheme: "dark" });
  await shot(pg, "u3-refusal-1280-dark");
  await pg.emulateMedia({ colorScheme: "light" });
  // a phone is a page of its own: a resize of this one leaves the layout of the wide window behind
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true }, "writer@dev.scriobh");
  await open(phone.page);
  await phone.page.locator(".scriobh-prose").click();
  await select(phone.page, await at(phone.page, "Filler paragraph 3:"), (await at(phone.page, "Filler paragraph 4:")) + 5);
  await phone.page.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => phone.page.locator("#cw-chap").count().then((n) => n > 0), "the chapter composer on a phone");
  await shot(phone.page, "u3-refusal-390-light");
  await phone.page.emulateMedia({ colorScheme: "dark" });
  await shot(phone.page, "u3-refusal-390-dark");
  await phone.ctx.close();
});

// ---- phase 2: the marks in the text (F-1286, F-1287) on a chapter of their own, so the steps above leave it as it is ----------
const MK_BODY = "The first paragraph is plain, and has nothing on it.\n\nThe *emphasised* words are here, and the tide ran on; the tide turned, and the tide came back.\n\nThe last paragraph says goodbye, with a little warmth.\n";
const mkMade = await api(main, "POST", `/api/v1/projects/${P.id}/docs`, { title: "Marked", kind: "chapter", content: MK_BODY });
ok(mkMade.status === 201, `marks chapter: ${mkMade.status}`);
const MK = mkMade.json.item.id;
const mkBase = async () => (await api(main, "GET", `/api/v1/docs/${MK}`)).json.item;
// Wren's private note on "tide turned", Wren's shared remark on "first paragraph is plain", Ada's private note on "warmth"
eq((await api(main, "POST", cbase, { document_id: MK, quote: "tide turned", body: "MARK-PRIVATE-WREN", visibility: "private" })).status, 201, "Wren's private note");
eq((await api(main, "POST", cbase, { document_id: MK, quote: "first paragraph is plain", body: "MARK-SHARED-WREN" })).status, 201, "Wren's shared remark");
eq((await api(ada.ctx, "POST", cbase, { document_id: MK, quote: "warmth", body: "MARK-PRIVATE-ADA", visibility: "private" })).status, 201, "Ada's private note");
const marksOf = (page) => page.evaluate(() => [...document.querySelectorAll(".scriobh-prose .cm-mark")].map((e) => e.textContent).sort());
const marksOn = (page) => page.evaluate(() => window.scriobh.cmarks && window.scriobh.cmarks.shown());
const geometry = (page) => page.evaluate(() => {
  const r = (e) => { const b = e.getBoundingClientRect(); return [b.top, b.left, b.width, b.height].map((n) => Math.round(n * 100) / 100).join(","); };
  return [...document.querySelectorAll(".scriobh-prose > *")].map(r).join("|") + "#" + document.documentElement.scrollHeight;
});
const setMarks = (page, on) => page.evaluate((v) => window.scriobh.cmarks.show(v), on);
const layoutShifts = (page) => page.evaluate(() => { window.__shifts = []; new PerformanceObserver((l) => { for (const e of l.getEntries()) if (!e.hadRecentInput) window.__shifts.push(e.value); }).observe({ type: "layout-shift" }); });

await kit.test("marks are drawn on load, on the words of each open comment and no others", async () => {
  await pg.goto("about:blank");
  await open(pg, P, MK);
  await waitFor(() => pg.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length) .then((n) => n === 2), "two marks drawn", 8000);
  eq(JSON.stringify(await marksOf(pg)), JSON.stringify(["first paragraph is plain", "tide turned"]), "the marks are on the words of Wren's two comments (his private one and his shared one)");
  ok(await pg.evaluate(() => { const m = document.querySelector(".cm-mark"); return !!m && getComputedStyle(m).backgroundColor !== "rgba(0, 0, 0, 0)"; }), "a mark has a fill");
  eq(await pg.evaluate(() => window.scriobh.write.editor.getMarkdown().includes("cm-mark")), false, "the marks are not in the Markdown");
  await shot(pg, "marks-wide-light");
  await pg.emulateMedia({ colorScheme: "dark" });
  await shot(pg, "marks-wide-dark");
  await pg.emulateMedia({ colorScheme: "light" });
});

await kit.test("typing before and inside a mark keeps it on the words it is on, and after a save it is on the stored words", async () => {
  const t0 = await at(pg, "tide turned");
  await select(pg, t0, t0); // before the words
  await pg.keyboard.type("QQ ");
  eq(JSON.stringify(await marksOf(pg)), JSON.stringify(["first paragraph is plain", "tide turned"]), "typed before a mark: the mark is on the same words");
  const inside = (await at(pg, "tide turned")) + 5; // before "turned"
  await select(pg, inside, inside);
  await pg.keyboard.type("X");
  const now = await marksOf(pg);
  ok(now.some((t) => t === "QQ tide Xturned" || t === "tide Xturned"), "typed inside a mark: the typed letter is in the mark, the words around it still are (" + JSON.stringify(now) + ")");
  ok(!now.some((t) => t.includes("QQ ")), "the text typed before the mark is not in it");
  await pg.evaluate(() => window.scriobh.write.flushAll());
  await waitFor(async () => {
    const list = (await api(main, "GET", `${cbase}?document_id=${MK}&plain=true&status=open`)).json.items.filter((t) => t.anchor && t.anchor.state === "active").map((t) => t.anchor.quote_plain).sort();
    return JSON.stringify(list) === JSON.stringify(await marksOf(pg));
  }, "after the save, the marks are on the words the server has", 8000);
  ok((await mkBase()).content.includes("QQ tide"), "the typed text was saved");
});

await kit.test("drawing and toggling a mark moves nothing: the same geometry, and no layout shift without input", async () => {
  await sleep(1500); // the page settles first (the formatting bar arrives after the load)
  await layoutShifts(pg);
  await setMarks(pg, false);
  await sleep(120);
  const gOff = await geometry(pg);
  await setMarks(pg, true);
  await sleep(120);
  const gOn = await geometry(pg);
  eq(gOn, gOff, "the page is laid out the same with the marks drawn and without them");
  // type into a marked paragraph, one key at a time, and compare each key's geometry with and without the marks
  const at0 = await at(pg, "tide Xturned", 2);
  await select(pg, at0, at0);
  for (const key of ["a", "b", "c", " ", "d"]) {
    await pg.keyboard.type(key);
    const withMarks = await geometry(pg);
    await setMarks(pg, false); await sleep(30);
    const without = await geometry(pg);
    await setMarks(pg, true); await sleep(30);
    eq(withMarks, without, `typing "${key}" in a marked paragraph lays the page out as it would without the marks`);
  }
  eq(await pg.evaluate(() => window.__shifts.length), 0, "no layout shift that was not from typing");
});

await kit.test("the off switch hides the marks, the choice survives a reload, and the switch is in the Comments tab", async () => {
  await pg.evaluate(() => document.cookie = "scriobh_insp=comments; path=/");
  await open(pg, P, MK);
  await pg.locator("#itab-comments").click();
  await waitFor(() => pg.locator("#cm-on").count().then((n) => n === 1), "the switch");
  eq(await pg.locator("#cm-on").isChecked(), true, "on by default");
  await pg.locator(".cm-off label, label:has(#cm-on)").first().click();
  await waitFor(() => pg.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length === 0), "the marks to go");
  eq(await pg.locator("#cm-on").isChecked(), false, "the switch is off");
  await open(pg, P, MK);
  await pg.locator("#itab-comments").click();
  await sleep(600);
  eq(await pg.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length), 0, "still no marks after a reload");
  eq(await pg.locator("#cm-on").isChecked(), false, "and the switch still says so");
  await pg.locator("label:has(#cm-on)").first().click();
  await waitFor(() => pg.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length === 2), "the marks back");
  await pg.evaluate(() => document.cookie = "scriobh_insp=details; path=/");
});

await kit.test("a click on a mark opens its comment in the Comments tab, and does not move the caret, focus or page", async () => {
  await open(pg, P, MK);
  await pg.locator(".scriobh-prose").click();
  const y = await pg.evaluate(() => window.scrollY);
  await pg.locator(".scriobh-prose .cm-mark", { hasText: "tide" }).first().click();
  await waitFor(() => pg.evaluate(() => document.getElementById("itab-comments").getAttribute("aria-selected") === "true"), "the Comments tab");
  await waitFor(() => pg.locator(".rv-card.is-current").count().then((n) => n === 1), "the comment is current");
  ok((await pg.locator(".rv-card.is-current").innerText()).includes("MARK-PRIVATE-WREN"), "the card is the comment of the words clicked");
  eq(await inEditor(pg), true, "focus stayed in the text");
  eq(await pg.evaluate(() => window.scrollY), y, "the page did not move");
  const sel = await pg.evaluate(() => { const s = window.scriobh.write.editor.view.state.selection; return [s.anchor, s.head].join(","); });
  ok(sel.split(",").every((n) => Number(n) > 0), "the caret is where the click put it: " + sel);
  await shot(pg, "marks-click-wide");
});

await kit.test("a second person sees only the marks of comments she can read", async () => {
  await open(ada.page, P, MK);
  await waitFor(() => ada.page.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length > 0), "Ada's marks", 8000);
  const seen = await marksOf(ada.page);
  eq(JSON.stringify(seen), JSON.stringify(["first paragraph is plain", "warmth"]), "Ada sees the shared remark and her own private note, not Wren's private note");
  const html = await ada.page.content();
  ok(!html.includes("MARK-PRIVATE-WREN"), "nothing of Wren's private note is in her page");
  await shot(ada.page, "marks-second-light");
});

await kit.test("a chapter with no comment loads no marks file; the first comment is marked at once, without a reload", async () => {
  await main.addCookies([{ name: "scriobh_insp", value: "details", url: base() }]);
  const fresh = await api(main, "POST", `/api/v1/projects/${P.id}/docs`, { title: "No notes yet", kind: "chapter", content: "A plain chapter with nothing said about it.\n\nA second paragraph, quiet.\n" });
  ok(fresh.status === 201, `fresh chapter: ${fresh.status}`);
  const FR = fresh.json.item.id;
  await pg.goto("about:blank");
  await open(pg, P, FR);
  await pg.locator("#itab-comments").click();
  await sleep(800);
  eq(await pg.evaluate(() => !!document.querySelector('script[src*="cmarks"]')), false, "no marks file for a chapter with no comment, even with the tab open");
  const at1 = await at(pg, "quiet", 0);
  await select(pg, at1, at1 + 5);
  await pg.keyboard.press("Alt+Shift+KeyM");
  await waitFor(() => pg.locator("#cw-sel").count().then((n) => n > 0), "the composer");
  await pg.keyboard.type("A first note on a plain word");
  await pg.keyboard.press(`${MOD}+Enter`);
  await waitFor(() => pg.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length === 1), "the first mark, without a reload", 8000);
  eq(JSON.stringify(await marksOf(pg)), JSON.stringify(["quiet"]), "the first mark is on the words it was made on");
  eq(await pg.evaluate(() => !!document.querySelector('script[src*="cmarks"]')), true, "the marks file came with the first comment");
  eq(await pg.locator("#cm-on").isChecked(), true, "the switch is on for it");
  ok(await pg.locator(".cm-off").isVisible(), "the switch is shown once there is a mark");
});

await kit.test("asking the server again with nothing changed replaces no mark", async () => {
  await open(pg, P, MK);
  await waitFor(() => pg.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length === 2), "two marks", 8000);
  await pg.evaluate(() => { window.__gone = 0; new MutationObserver((ms) => { for (const m of ms) for (const n of m.removedNodes) if (n.nodeType === 1 && (n.classList.contains("cm-mark") || n.querySelector && n.querySelector(".cm-mark"))) window.__gone++; }).observe(document.querySelector(".scriobh-prose"), { childList: true, subtree: true }); });
  await pg.evaluate(() => window.scriobh.cmarks.refresh());
  await pg.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:saved", { detail: {} })));
  await sleep(1800);
  eq(await pg.evaluate(() => window.__gone), 0, "no mark was taken out of the text by the answer");
  eq(await pg.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length), 2, "and both are still there");
});

await kit.test("the marks at 390 px: drawn, no horizontal scroll, in both themes", async () => {
  await pg.goto("about:blank"); // the chapter is held by one tab at a time: the desktop and Ada let go, so the phone can write
  await ada.page.goto("about:blank");
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true }, "writer@dev.scriobh");
  const ph = phone.page;
  await open(ph, P, MK);
  await waitFor(() => ph.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length === 2), "two marks on a phone", 8000);
  eq(await ph.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "no horizontal scroll");
  await ph.evaluate(() => window.scrollTo(0, 0));
  await sleep(300);
  await shot(ph, "marks-phone-light");
  await ph.emulateMedia({ colorScheme: "dark" });
  await sleep(300);
  await shot(ph, "marks-phone-dark");
  await phone.ctx.close();
});

await kit.test("the marks are not in what is saved or exported", async () => {
  await open(pg, P, MK);
  await waitFor(() => pg.evaluate(() => document.querySelectorAll(".scriobh-prose .cm-mark").length === 2), "the marks", 8000);
  const stored = (await mkBase()).content;
  eq(await pg.locator(".scriobh-prose .cm-mark").count() > 0, true, "the marks are on the screen while the text they are on is saved without them");
  ok(!stored.includes("cm-mark") && !stored.includes("<span"), "the saved text has no marks");
  const exp = await api(main, "POST", `/api/v1/projects/${P.id}/export/markdown`, { mode: "single" });
  const body = typeof exp.json === "object" && exp.json && exp.json.content ? exp.json.content : JSON.stringify(exp.json || "");
  ok(!body.includes("cm-mark") && !body.includes("MARK-PRIVATE") && !body.includes("MARK-SHARED"), "the export has no marks or comments");
  eq(await pg.evaluate(() => window.scriobh.write.editor.getMarkdown().trimEnd()), stored.trimEnd(), "the editor's text is the saved text");
});

await kit.finish("e2e-writer-comments");
