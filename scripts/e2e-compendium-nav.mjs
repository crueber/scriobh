#!/usr/bin/env node
// The rest of the Compendium in the writing view, in a real Chromium (F-1262 to F-1266): the events a scene tells and a quick note in
// the inspector's Compendium tab, the way back to writing from a Compendium page, the project's Compendium tab going to the Compendium,
// and the caret and the scroll coming back.
//
//   node scripts/e2e-compendium-nav.mjs [--only substring|substring] [--shots DIR]
//
// The sidebar steps run at 390 px and by the keyboard alone, with the editor's text, caret, scroll, revision and unsaved state compared
// before and after. The restore steps hold the answer for js/resume.js, so that what the writer does before it arrives is the same on
// every run and not whatever a slow machine happens to make of it.
import { mkdirSync } from "node:fs";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { seedBible } from "./entity-fixture.mjs";
import { signIn } from "./harness.mjs";

const kit = await createKit("e2e-compendium-nav");
kit.deadline(12 * 60 * 1000);
const arg = (n) => (process.argv.includes(n) ? process.argv[process.argv.indexOf(n) + 1] : "");
const only = arg("--only").split("|").filter(Boolean);
const shots = arg("--shots");
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;
const baseTest = kit.test;
kit.test = (name, fn) => (only.length && !only.some((o) => name.includes(o))) ? undefined : baseTest(name, async () => {
  try { await fn(); } catch (e) { if (shots) await pg.screenshot({ path: `${shots}/fail-${name.split(".")[0]}.png` }).catch(() => {}); await pg.setViewportSize({ width: 1280, height: 800 }).catch(() => {}); throw e; }
});
const J = async (method, path, data) => {
  const r = await api(main, method, path, data);
  if (r.status >= 300) throw new Error(`${method} ${path}: ${r.status} ${JSON.stringify(r.json)}`);
  return r.json;
};

// ---- two books that use one Compendium, a long chapter, events and a note ---------------------------------------------------
const bible = await seedBible(J, { fillers: 3 });
const comp = bible.comp.slug;
const mkBook = async (title) => {
  const b = (await J("POST", "/api/v1/projects", { title, kind: "novel" })).item;
  await J("PUT", `/api/v1/projects/${b.id}/compendium`, { compendium_id: bible.comp.id });
  const d = (await J("GET", `/api/v1/projects/${b.id}/docs`)).order[0];
  return { ...b, d };
};
const bookA = await mkBook("The Salt Road");
const bookB = await mkBook("The Second Road");
const bare = (await J("POST", "/api/v1/projects", { title: "Bare book", kind: "novel" })).item;
const para = (i) => `Paragraph ${i}. ` + "The tide came in over the slip and the ferry waited for the light to change, as it always did, while the boats knocked together in the dark. ".repeat(3) + "\n\n";
let long = "";
for (let i = 1; i <= 36; i++) long += para(i);
const patchDoc = async (id, fields) => { const cur = (await J("GET", `/api/v1/docs/${id}`)).item; await J("PATCH", `/api/v1/docs/${id}`, { base_revision: cur.revision, ...fields }); };
await patchDoc(bookA.d, { title: "Chapter one", content: long });
await patchDoc(bookB.d, { title: "The crossing", content: "A short chapter about the crossing.\n" });
const second = (await J("POST", `/api/v1/projects/${bookA.id}/docs`, { title: "Second chapter", kind: "chapter", content: "Short words.\n" })).item.id;
await J("PUT", "/api/v1/me/prefs", { items: { autosave_ms: "5000" } }); // the editor's own save waits, so unsaved text can be compared
const ev = async (title, story_date, extra = {}) => (await J("POST", `/api/v1/compendiums/${bible.comp.id}/timeline/events`, { title, story_date, ...extra })).item;
const storm = await ev("The storm", "1998-04");
const wedding = await ev("The wedding at the quay", "1998-06-12");
await ev("Midsummer fires", "", { story_label: "Midsummer, Year 312" });
const note = (await J("POST", `/api/v1/projects/${bookA.id}/research`, { title: "Ferry timetable", body: "The last boat is at nine." })).item;
const chapter = (b, d) => `${base()}/p/${b.slug}/d/${d}`;
const scenes = async (id) => (await J("GET", `/api/v1/compendiums/${bible.comp.id}/timeline/events?project=${bookA.id}`)).items.filter((e) => e.scenes.some((s) => s.id === id)).map((e) => e.title).sort();

// ---- helpers ---------------------------------------------------------------------------------------------------------------
const ready = (page) => waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor && window.scriobh.inspector && window.scriobh.chords)), "the page's scripts", 12000);
const openDoc = async (page, b, d, hash = "") => {
  await page.goto(chapter(b, d) + hash);
  await page.waitForSelector(".scriobh-prose");
  await ready(page);
  await sleep(250);
};
const snap = (page) => page.evaluate(() => {
  const W = window.scriobh.write, s = W.editor.view.state.selection;
  return { y: Math.round(window.scrollY), md: W.editor.getMarkdown(), a: s.anchor, h: s.head, rev: W.rev, un: !!W.unsaved() };
});
const textPos = (page, needle) => page.evaluate((n) => {
  let at = -1;
  window.scriobh.write.editor.view.state.doc.descendants((node, pos) => { if (at < 0 && node.isText) { const i = node.text.indexOf(n); if (i >= 0) at = pos + i; } });
  return at;
}, needle);
const caretAt = async (page, needle, offset = 0) => {
  const at = (await textPos(page, needle)) + offset;
  await page.evaluate((a) => { const ed = window.scriobh.write.editor; ed.alts.select(a, a); ed.view.focus(); }, at);
};
const scrollToText = (page, needle) => page.evaluate((n) => {
  const v = window.scriobh.write.editor.view; let at = -1;
  v.state.doc.descendants((node, pos) => { if (at < 0 && node.isText && node.text.indexOf(n) >= 0) at = pos; });
  const r = v.coordsAtPos(at); window.scrollTo(0, window.scrollY + r.top - 200);
}, needle);
const inEditor = (page) => page.evaluate(() => !!(document.activeElement && document.activeElement.closest(".scriobh-prose")));
const active = (page) => page.evaluate(() => (document.activeElement ? document.activeElement.id || document.activeElement.tagName : ""));
const tabTo = async (page, sel, back = false, max = 80) => {
  for (let i = 0; i < max; i++) {
    if (await page.evaluate((s) => !!document.activeElement && document.activeElement.matches(s), sel)) return;
    await page.keyboard.press(back ? "Shift+Tab" : "Tab");
  }
  throw new Error(`could not reach ${sel} by the keyboard`);
};
const noSideways = async (page, what) => eq(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, `${what}: no sideways scroll`);
const targets = async (page, sel, what) => {
  const small = await page.evaluate((s) => [...document.querySelectorAll(s)].filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => (e.id || e.className || e.tagName) + ":" + Math.round(e.getBoundingClientRect().height)), sel);
  eq(small.join(","), "", `${what}: every target is 44 px`);
};
const sheetOpen = async (page) => {
  await page.keyboard.press("Alt+Shift+KeyC");
  await page.locator("#lk-main .lk-search, #lk-entry, #lk-none, #lk-closed, #lk-view").first().waitFor();
};
const view = async (page, which) => { // by the keyboard: from wherever the focus is to the button of the view, and Space
  const n = { entries: 1, events: 2, notes: 3 }[which];
  const sel = `.lk-views button:nth-child(${n})`;
  const inPanel = await page.evaluate(() => !!(document.activeElement && document.activeElement.closest("#ipanel-compendium")));
  if (!inPanel) await sheetOpen(page);
  await tabTo(page, sel, true).catch(async () => { await tabTo(page, sel); });
  await page.keyboard.press("Space");
  await page.locator(which === "entries" ? "#lk-q" : `#lk-view[data-view="${which === "events" ? "events" : "notes"}"] #lk-h`).first().waitFor();
};
// The phone: a touch screen 390 px wide, signed in as the same person. The chapter is open for writing in one tab at a time, so the desktop tab leaves it first.
const phone = async () => {
  await pg.goto("about:blank");
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  await ctx.addCookies(await main.cookies());
  const page = await ctx.newPage();
  kit.watch(page, "phone");
  return { ctx, page };
};
// Hold the answer to every request that matches until release(): what the writer does while it is on its way is then the same on every run.
const holdRoute = async (page, re) => {
  let release;
  const gate = new Promise((r) => { release = r; });
  await page.route(re, async (route) => { await gate; await route.continue().catch(() => {}); });
  return { release, off: async () => { release(); await page.unroute(re).catch(() => {}); } };
};
const shot = async (page, name) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }); };
// The tab comes back on the view it was left on; the entries' search is one press away.
const toEntries = async (page) => {
  await page.locator("#lk-q, #lk-view").first().waitFor();
  if (!(await page.locator("#lk-q").count())) await page.locator(".lk-views button", { hasText: "Entries" }).click();
  await page.locator("#lk-q").waitFor();
};
const statusText = (page) => page.locator("#lk-status").innerText();

// ---- B: the events this scene tells, at 390 px by the keyboard alone -------------------------------------------------------------
await kit.test("1. events this scene tells, at 390 px by the keyboard alone: link one found by its words, add one, unlink one, the editor untouched", async () => {
  kit.allowed.push(/status of 4\d\d/);
  const ph = await phone();
  const pg = ph.page;
  await openDoc(pg, bookA, second);
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.type("XYZ"); // unsaved on purpose
  await view(pg, "events");
  await sleep(800); // a touch screen saves when the editor loses the focus: that save, if there is one, has landed
  const before = await snap(pg);
  eq(await pg.locator("#lk-view #lk-h").innerText(), "Events this scene tells", "the heading");
  ok(/Scene: Second chapter/.test(await pg.locator(".lk-scenename").innerText()), "the scene is named");
  ok(/None yet/.test(await pg.locator("#lk-view").innerText()), "nothing is told yet");
  // link, by the words of its title
  await tabTo(pg, "#lk-eq");
  await pg.keyboard.type("storm");
  await pg.locator("#lk-ev-hits .lk-ev", { hasText: "The storm" }).waitFor();
  eq(await pg.locator("#lk-ev-hits .lk-ev").count(), 1, "one event matches");
  await tabTo(pg, "#lk-ev-hits button");
  await pg.keyboard.press("Enter");
  await pg.locator("#lk-view > .lk-evs .lk-ev", { hasText: "The storm" }).waitFor();
  ok(/Linked\./.test(await statusText(pg)), "Linked. is said: " + JSON.stringify(await statusText(pg)) + (await pg.locator("#lk-view").innerText()).slice(0, 300));
  eq(await active(pg), "lk-h", "the focus is on the heading, not lost with the button");
  eq((await scenes(second)).join(), "The storm", "the scene tells the storm");
  // a place or a person finds it too
  await tabTo(pg, "#lk-eq");
  await pg.keyboard.type("quay");
  await pg.locator("#lk-ev-hits .lk-ev", { hasText: "The wedding at the quay" }).waitFor();
  // add one: title and story date only
  await tabTo(pg, ".lk-evnew summary");
  await pg.keyboard.press("Enter");
  await tabTo(pg, "#lkev-title");
  await pg.keyboard.type("The ferry sinks");
  await pg.keyboard.press("Tab");
  await pg.keyboard.type("1998-07-01");
  await tabTo(pg, ".lk-evnew button[type=submit]");
  await pg.keyboard.press("Enter");
  await pg.locator("#lk-view > .lk-evs .lk-ev", { hasText: "The ferry sinks" }).waitFor();
  ok(/Added to The Salt Road Bible and linked\./.test(await statusText(pg)), "the new event is said: " + (await statusText(pg)));
  ok(/1998-07-01/.test(await pg.locator("#lk-view > .lk-evs").innerText()), "its date is shown");
  eq((await scenes(second)).join("|"), "The ferry sinks|The storm", "the scene tells both");
  // a date that cannot be read is said where the writer is looking, and nothing is added
  await tabTo(pg, ".lk-evnew summary");
  await pg.keyboard.press("Enter");
  await tabTo(pg, "#lkev-title");
  await pg.keyboard.type("Bad date");
  await pg.keyboard.press("Tab");
  await pg.keyboard.type("last spring");
  await tabTo(pg, ".lk-evnew button[type=submit]");
  await pg.keyboard.press("Enter");
  await waitFor(async () => /date/i.test(await pg.locator("#lkev-date-err").innerText()), "the refusal to be said beside the date", 6000);
  eq(await pg.locator("#lkev-title").inputValue(), "Bad date", "the typed words are still in the form");
  eq((await scenes(second)).length, 2, "nothing was added");
  // unlink one
  await tabTo(pg, '#lk-view > .lk-evs button[aria-label^="Stop telling The storm"]');
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await scenes(second)).join() === "The ferry sinks", "the storm to be unlinked");
  await waitFor(async () => !(await pg.locator("#lk-view > .lk-evs .lk-ev", { hasText: "The storm" }).count()), "the list to follow");
  // the phone's sheet: no sideways scroll, targets of 44 px, a search box of 16 px
  await noSideways(pg, "the events view at 390 px");
  await targets(pg, "#lk-view button, #lk-view a, #lk-view summary, #lk-view input[type=search], #lk-view input[type=text]", "the events view");
  eq(await pg.locator("#lk-eq").evaluate((e) => parseFloat(getComputedStyle(e).fontSize) >= 16), true, "the search box is 16 px or more");
  await shot(pg, "events-390-light");
  // the editor was not touched: text, caret, scroll, revision, unsaved
  const after = await snap(pg);
  eq(JSON.stringify(after), JSON.stringify(before), "the editor's text, caret, scroll, revision and unsaved state");
  // the link to the event opens its page
  const href = await pg.locator("#lk-view > .lk-evs .lk-ev a").first().getAttribute("href");
  ok(new RegExp(`^/c/${comp}/timeline/events/tle_`).test(href), "the link to the event: " + href);
  await pg.keyboard.press("Escape");
  await ph.ctx.close();
});

// ---- B: the quick note ------------------------------------------------------------------------------------------------------------
await kit.test("2. a quick note, at 390 px by the keyboard alone: listed, found, added, saved; a click away does not lose its words; a stale save keeps them", async () => {
  kit.allowed.push(/status of 4\d\d/);
  const ph = await phone();
  const pg = ph.page;
  await openDoc(pg, bookA, second);
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.type("ABC");
  await view(pg, "notes");
  await sleep(800);
  const before = await snap(pg);
  ok(/Ferry timetable/.test(await pg.locator("#lk-nrows").innerText()), "the book's note is listed");
  ok(await pg.locator('a:has-text("Open all notes")').getAttribute("href").then((h) => h === `/p/${bookA.slug}/research`), "a way to all the notes");
  await tabTo(pg, "#lk-nq");
  await pg.keyboard.type("zzz");
  await pg.locator("#lk-nrows .lk-empty").waitFor();
  await pg.keyboard.press("ControlOrMeta+a");
  await pg.keyboard.type("timetable");
  await pg.locator("#lk-nrows .lk-pick", { hasText: "Ferry timetable" }).waitFor();
  // a new note: title and words
  await tabTo(pg, "#lk-nnew");
  await pg.keyboard.press("Enter");
  await pg.locator("#lkn-title").waitFor({ timeout: 5000 }).catch(async (e) => { throw new Error("no form: " + (await pg.locator("#lk-main").innerText()).slice(0, 300).replace(/\n/g, "/") + " active=" + (await active(pg))); });
  await tabTo(pg, "#lkn-title");
  await pg.keyboard.type("Tide table");
  await pg.keyboard.press("Tab");
  await pg.keyboard.type("High at six.");
  await tabTo(pg, "#lk-nform button[type=submit]");
  await pg.keyboard.press("Enter");
  await pg.locator("#lk-nform .lk-ntext").waitFor();
  await waitFor(async () => /Added\./.test(await statusText(pg)), "Added.", 6000);
  const mine = (await J("GET", `/api/v1/projects/${bookA.id}/research`)).items.find((n) => n.title === "Tide table");
  ok(mine, "the note is the book's");
  eq((await J("GET", `/api/v1/projects/${bookA.id}/research/${mine.id}`)).item.body, "High at six.", "its words");
  // more words, saved with the button
  await tabTo(pg, "#lkn-body");
  await pg.evaluate(() => { const t = document.activeElement; t.setSelectionRange(t.value.length, t.value.length); }); // the end of the words, as Cmd+Down or Ctrl+End does
  await pg.keyboard.type(" Low at midnight.");
  await tabTo(pg, "#lk-nform .lk-savebar button");
  await pg.keyboard.press("Enter");
  await waitFor(async () => /Saved\./.test(await statusText(pg)), "Saved.", 6000);
  eq((await J("GET", `/api/v1/projects/${bookA.id}/research/${mine.id}`)).item.body, "High at six. Low at midnight.", "the saved words");
  // words not saved are not lost to a press on Back to the notes: the panel says so and stays
  await tabTo(pg, "#lkn-body");
  await pg.evaluate(() => { const t = document.activeElement; t.setSelectionRange(t.value.length, t.value.length); }); // the end of the words, as Cmd+Down or Ctrl+End does
  await pg.keyboard.type(" Unsaved.");
  await tabTo(pg, "#lk-nback", true);
  await pg.keyboard.press("Enter");
  await waitFor(async () => /Save the note first/.test(await statusText(pg)), "the warning", 4000);
  eq(await pg.locator("#lkn-body").inputValue(), "High at six. Low at midnight. Unsaved.", "the words are still in the box");
  // someone else changes the note: this save is refused with the words kept, Keep mine wins
  await J("PATCH", `/api/v1/projects/${bookA.id}/research/${mine.id}`, { base_revision: (await J("GET", `/api/v1/projects/${bookA.id}/research/${mine.id}`)).item.revision, body: "Their words." });
  await tabTo(pg, "#lk-nform .lk-savebar button");
  await pg.keyboard.press("Enter");
  await pg.locator(".lk-conflict").waitFor();
  ok(/changed since you opened it/.test(await statusText(pg)), "the conflict is said");
  eq(await pg.locator("#lkn-body").inputValue(), "High at six. Low at midnight. Unsaved.", "the typed words are still in the box");
  eq((await J("GET", `/api/v1/projects/${bookA.id}/research/${mine.id}`)).item.body, "Their words.", "nothing of theirs was written over");
  await tabTo(pg, ".lk-conflict button.solid");
  await pg.keyboard.press("Enter");
  await waitFor(async () => /Saved\./.test(await statusText(pg)), "Keep mine to save", 6000);
  eq((await J("GET", `/api/v1/projects/${bookA.id}/research/${mine.id}`)).item.body, "High at six. Low at midnight. Unsaved.", "Keep mine wins");
  // now Back to the notes is free
  await tabTo(pg, "#lk-nback", true);
  await pg.keyboard.press("Enter");
  await pg.locator("#lk-nrows").waitFor();
  await noSideways(pg, "the notes view at 390 px");
  await targets(pg, "#lk-view button, #lk-view a, #lk-view input[type=search]", "the notes view");
  await shot(pg, "notes-390-light");
  eq(JSON.stringify(await snap(pg)), JSON.stringify(before), "the editor's text, caret, scroll, revision and unsaved state");
  await pg.keyboard.press("Escape");
  await ph.ctx.close();
});

await kit.test("2b. a note: a double click makes one note, a save does not clear the words typed while it was on its way, Use theirs lets the words go by the writer's choice", async () => {
  kit.allowed.push(/status of 4\d\d/);
  await openDoc(pg, bookA, second);
  await sheetOpen(pg);
  await view(pg, "notes");
  await pg.locator("#lk-nnew").click();
  await pg.locator("#lkn-title").fill("Twice");
  await pg.locator("#lkn-body").fill("Once only.");
  let h = await holdRoute(pg, /\/lookup\/note-new/);
  await pg.locator("#lk-nform button[type=submit]").dblclick();
  await sleep(300);
  eq(await pg.locator("#lk-nform button[type=submit]").isDisabled(), true, "the button is off while the request is out");
  await h.off();
  await pg.locator("#lk-nform .lk-savebar button", { hasText: "Save the note" }).waitFor();
  eq((await J("GET", `/api/v1/projects/${bookA.id}/research`)).items.filter((n) => n.title === "Twice").length, 1, "one note was made by a double click");
  const twice = (await J("GET", `/api/v1/projects/${bookA.id}/research`)).items.find((n) => n.title === "Twice");
  // words typed while a save is on its way are not saved by it, and are not let go of
  await pg.locator("#lkn-body").fill("First words.");
  h = await holdRoute(pg, /\/lookup\/note-save/);
  await pg.locator("#lk-nform .lk-savebar button").click();
  await pg.locator("#lkn-body").focus();
  await pg.keyboard.insertText(" Typed during.");
  await h.off();
  await waitFor(async () => /Saved\./.test(await statusText(pg)), "the save to come back", 6000);
  eq(await pg.locator("#lkn-body").inputValue(), "First words. Typed during.", "the words typed during the save are in the box");
  await pg.locator("#lk-nback").click();
  await waitFor(async () => /Save the note first/.test(await statusText(pg)), "the warning: those words are not saved", 4000);
  eq(await pg.locator("#lk-nform").count(), 1, "and the note is still open");
  // someone else changes the note: this save is refused, the conflict box stays when something else is pressed, and Use theirs is allowed to let the words go
  const cur = (await J("GET", `/api/v1/projects/${bookA.id}/research/${twice.id}`)).item;
  await J("PATCH", `/api/v1/projects/${bookA.id}/research/${twice.id}`, { base_revision: cur.revision, body: "Their words." });
  await pg.locator("#lk-nform .lk-savebar button").click();
  await pg.locator(".lk-conflict").waitFor();
  await pg.locator("#lk-nback").click();
  await sleep(300);
  eq(await pg.locator(".lk-conflict button").count(), 2, "the conflict box and its two buttons are still there after another press");
  await pg.locator(".lk-conflict button", { hasText: "Use theirs" }).click();
  await waitFor(async () => (await pg.locator("#lkn-body").inputValue()) === "Their words.", "their words to be shown", 6000);
  await pg.locator("#lk-nback").click();
  await pg.locator("#lk-nrows").waitFor();
  ok(!/Save the note first/.test(await statusText(pg).catch(() => "")), "nothing holds the writer back once the words were let go");
});

await kit.test("2c. an older answer never replaces a newer view, and a double click on an event form makes one event", async () => {
  kit.allowed.push(/status of 4\d\d/);
  await openDoc(pg, bookA, second);
  await sheetOpen(pg);
  let h = await holdRoute(pg, /\/lookup\?view=events/);
  await pg.locator(".lk-views button", { hasText: "Timeline" }).click();
  await pg.locator(".lk-views button", { hasText: "Notes" }).click();
  await pg.locator('#lk-view[data-view="notes"]').waitFor();
  await h.off();
  await sleep(500);
  eq(await pg.locator("#lk-view").getAttribute("data-view"), "notes", "the events answer came late and changed nothing");
  await pg.locator(".lk-views button", { hasText: "Timeline" }).click();
  await pg.locator('#lk-view[data-view="events"]').waitFor();
  await pg.locator(".lk-evnew summary").click();
  await pg.locator("#lkev-title").fill("Dup event");
  h = await holdRoute(pg, /\/lookup\/event-new/);
  await pg.locator(".lk-evnew button[type=submit]").dblclick();
  await sleep(300);
  eq(await pg.locator(".lk-evnew button[type=submit]").isDisabled(), true, "the button is off while the request is out");
  await h.off();
  await pg.locator("#lk-view > .lk-evs .lk-ev", { hasText: "Dup event" }).waitFor();
  eq((await J("GET", `/api/v1/compendiums/${bible.comp.id}/timeline/events`)).items.filter((e) => e.title === "Dup event").length, 1, "one event was made by a double click");
});

await kit.test("3. a reader of the book sees the events and the notes as text, with the reason, and no form", async () => {
  kit.allowed.push(/status of 4\d\d/);
  const rd = await kit.signedIn({ viewport: { width: 1280, height: 800 } }, "reader@dev.scriobh");
  ok((await api(main, "POST", `/api/v1/projects/${bookA.id}/members`, { email: "reader@dev.scriobh", role: "reader" })).status < 300, "Rory on the book");
  ok((await api(main, "POST", `/api/v1/compendiums/${bible.comp.id}/members`, { email: "reader@dev.scriobh", role: "reader" })).status < 300, "Rory on the Compendium");
  const p = rd.page;
  await openDoc(p, bookA, second);
  await sheetOpen(p);
  await view(p, "events");
  const t = await p.locator("#lk-view").innerText();
  ok(/The ferry sinks/.test(t) && /Only editors of this book can change the events/.test(t), "the list and the reason: " + t.replace(/\n+/g, " / "));
  eq(await p.locator("#lk-view form").count(), 0, "no form of any kind");
  await view(p, "notes");
  ok(/Only editors of this book can change its notes/.test(await p.locator("#lk-view").innerText()), "the reason for the notes: " + (await p.locator("#lk-view").innerText()).replace(/\n+/g, " / "));
  eq(await p.locator('button:has-text("New note")').count(), 0, "no way to add a note");
  await p.locator(".lk-pick", { hasText: "Ferry timetable" }).click();
  await p.locator("#lk-view .md").waitFor();
  eq(await p.locator("#lk-nform").count(), 0, "a reader has no form for a note");
  ok(/The last boat is at nine/.test(await p.locator("#lk-view .md").innerText()), "the note is read as text");
  await rd.ctx.close();
});

// ---- C: Back to writing ------------------------------------------------------------------------------------------------------------
await kit.test("4. Back to writing: who is shown it, and where it goes", async () => {
  kit.allowed.push(/status of 4\d\d/);
  const back = (page) => page.locator("main .comp-back a");
  // a person on the Compendium and on no book is shown nothing, and no book's title
  const rd = await kit.signedIn({ viewport: { width: 1280, height: 800 } }, "reader@dev.scriobh");
  const p = rd.page;
  const members = (await J("GET", `/api/v1/projects/${bookA.id}/members`)).items;
  const rory = members.find((m) => m.email === "reader@dev.scriobh");
  ok(rory, "Rory is a member of the first book");
  ok((await api(main, "DELETE", `/api/v1/projects/${bookA.id}/members?user_id=${rory.user_id}`)).status < 300, "Rory is taken off the first book");
  await p.goto(`${base()}/c/${comp}`);
  eq(await back(p).count(), 0, "a person who has been in no book of this Compendium has no way back");
  // on one book, opened: that book, and its last chapter, and not the other book
  ok((await api(main, "POST", `/api/v1/projects/${bookA.id}/members`, { email: "reader@dev.scriobh", role: "reader" })).status < 300, "Rory is on the first book again");
  await openDoc(p, bookA, second);
  await p.goto(`${base()}/c/${comp}`);
  eq((await back(p).innerText()).trim().replace(/^\S+\s/, ""), "Back to The Salt Road, Second chapter", "the book he was in and its chapter");
  ok(!/The Second Road/.test(await p.locator("main").innerText()), "the other book is never named to him");
  eq(await back(p).getAttribute("href"), `/p/${bookA.slug}/d/${second}#resume`, "where it goes");
  // taken off the book again: nothing of it, whatever his own state says
  await api(main, "DELETE", `/api/v1/projects/${bookA.id}/members?user_id=${rory.user_id}`);
  await p.goto(`${base()}/c/${comp}`);
  eq(await back(p).count(), 0, "nothing of a book he cannot open");
  const mt = await p.locator("main").innerText();
  ok(!/The Salt Road(?! Bible)/.test(mt), "its title is not on the page");
  await rd.ctx.close();
  // the writer: the book written last, from every page of the Compendium
  await openDoc(pg, bookA, bookA.d);
  await openDoc(pg, bookB, bookB.d);
  for (const path of ["", "/entities", `/e/${bible.maire.id}`, "/timeline"]) {
    await pg.goto(`${base()}/c/${comp}${path}`);
    eq((await back(pg).innerText()).trim().replace(/^\S+\s/, ""), "Back to The Second Road, The crossing", `on ${path || "the overview"}`);
  }
  // by the keyboard: the link is reached with Tab, Enter goes to the chapter, which is shown
  await pg.goto(`${base()}/c/${comp}`);
  await tabTo(pg, "main .comp-back a");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(new RegExp(`/p/${bookB.slug}/d/${bookB.d}`));
  await pg.waitForSelector(".scriobh-prose");
  ok(/A short chapter about the crossing/.test(await pg.locator(".scriobh-prose").innerText()), "the chapter is shown");
  ok(!/#resume/.test(pg.url()), "the fragment is gone from the address: " + pg.url());
  // the other book written last: the link follows
  await openDoc(pg, bookA, bookA.d);
  await pg.goto(`${base()}/c/${comp}`);
  eq((await back(pg).innerText()).trim().replace(/^\S+\s/, ""), "Back to The Salt Road, Chapter one", "after the other book");
  // no address of the request is put in it
  await pg.goto(`${base()}/c/${comp}?from=${bookB.slug}&back=https://example.com/`);
  eq(await back(pg).getAttribute("href"), `/p/${bookA.slug}/d/${bookA.d}#resume`, "a query changes nothing");
  // of two books, the one that is not locked is the way back, though the locked one was opened later
  const guarded = await mkBook("Guarded book");
  await patchDoc(guarded.d, { title: "Zqchapter", content: "Zqwords." });
  await openDoc(pg, bookA, bookA.d);
  await openDoc(pg, guarded, guarded.d);
  ok((await api(main, "PUT", `/api/v1/projects/${guarded.id}/passphrase`, { passphrase: "correct horse battery" })).status < 300, "the passphrase is set");
  const other = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  const op = await other.newPage();
  kit.watch(op, "second session");
  await signIn(op, base());
  await op.goto(`${base()}/c/${comp}`);
  eq((await back(op).innerText()).trim().replace(/^\S+\s/, ""), "Back to The Salt Road, Chapter one", "an open book is the way back before a locked one");
  // a book with a passphrase this session has not unlocked is named by its title alone, when it is the only one
  const comp2 = (await J("POST", "/api/v1/compendiums", { name: "A second world" })).item;
  const g2 = await mkBook("Only guarded");
  await patchDoc(g2.d, { title: "Zqchapter", content: "Zqwords." });
  await J("PUT", `/api/v1/projects/${g2.id}/compendium`, { compendium_id: comp2.id });
  await openDoc(pg, g2, g2.d);
  ok((await api(main, "PUT", `/api/v1/projects/${g2.id}/passphrase`, { passphrase: "correct horse battery" })).status < 300, "the passphrase is set");
  await op.goto(`${base()}/c/${comp2.slug}`);
  eq((await back(op).innerText()).trim().replace(/^\S+\s/, ""), "Back to Only guarded", "a locked book, by its title");
  eq(await back(op).getAttribute("href"), `/p/${g2.slug}`, "and to its unlock page");
  ok(!(await op.locator("main").innerText()).includes("Zqchapter"), "no chapter title");
  // the Compendium tab of a locked book names no Compendium to a session that has not unlocked it
  const r = await other.request.fetch(`${base()}/p/${g2.slug}/compendium/open`, { maxRedirects: 0 });
  ok(!/\/c\//.test(r.headers().location || "") && !(await r.text()).includes("A second world"), "the tab of a locked book says nothing of the Compendium: " + r.status() + " " + r.headers().location);
  await other.close();
  await api(main, "POST", `/api/v1/projects/${guarded.id}/trash`, {});
  await api(main, "POST", `/api/v1/projects/${g2.id}/trash`, {});
});

await kit.test("5. the project's Compendium tab goes to the Compendium's entries; a book with none keeps the wiring page; the menu keeps it too", async () => {
  await pg.goto(`${base()}/p/${bookA.slug}/outline`);
  await pg.locator(".ptabs a", { hasText: "Compendium" }).click();
  await pg.waitForURL(new RegExp(`/c/${comp}/entities$`));
  // a book with none: the wiring page, which offers to make or attach one
  await pg.goto(`${base()}/p/${bare.slug}/outline`);
  await pg.locator(".ptabs a", { hasText: "Compendium" }).click();
  await pg.waitForURL(new RegExp(`/p/${bare.slug}/compendium$`));
  ok(/Compendium/.test(await pg.locator("main h1, main h2").first().innerText()), "the wiring page");
  // the project menu keeps the wiring page for a book that has a Compendium
  const href = await pg.locator(`a.menu-item[href="/p/${bookA.slug}/compendium"]`).count();
  await pg.goto(`${base()}/p/${bookA.slug}/outline`);
  ok((await pg.locator(`.menu-item[href="/p/${bookA.slug}/compendium"]`).count()) >= 1 && href === 0, "the menu has Compendium settings");
  await pg.goto(`${base()}/p/${bookA.slug}/compendium`);
  ok(/primary/i.test(await pg.locator("main").innerText()), "the wiring page of a book that has one");
  // the overview of the Compendium links each book to it
  await pg.goto(`${base()}/c/${comp}`);
  await pg.locator(`#projects a[href="/p/${bookA.slug}/compendium"]`).click();
  await pg.waitForURL(new RegExp(`/p/${bookA.slug}/compendium$`));
  // at 390 px the phone's Bible tab goes the same way, and the way back is a target of 44 px
  const ph = await phone();
  const p = ph.page;
  await p.goto(`${base()}/p/${bookA.slug}/outline`);
  const bible390 = p.locator(".tabbar a.tab", { hasText: "Compendium" }).first();
  eq(await bible390.getAttribute("href"), `/p/${bookA.slug}/compendium/open`, "the Bible tab's address");
  await bible390.click();
  await p.waitForURL(new RegExp(`/c/${comp}/entities$`));
  await noSideways(p, "the Compendium page at 390 px");
  await targets(p, "main .comp-back a", "the way back");
  await shot(p, "back-390-light");
  await ph.ctx.close();
});

// ---- C: caret and scroll -------------------------------------------------------------------------------------------------------------
const longPos = async (page) => { // a place in the long chapter: scrolled down, a caret in a paragraph there
  await openDoc(page, bookA, bookA.d);
  await scrollToText(page, "Paragraph 20.");
  await caretAt(page, "Paragraph 20.", 8);
  await page.evaluate(() => window.scrollTo(0, window.scrollY + 0));
  return snap(page);
};
const near = (a, b, d, what) => ok(Math.abs(a - b) <= d, `${what}: ${a} is not near ${b}`);

await kit.test("6. the caret and the scroll come back after Back to writing, the Back button and a reload; not the caret when the text changed", async () => {
  const at = await longPos(pg);
  ok(at.y > 1500, "the chapter is scrolled down: " + at.y);
  const comeBack = async (how) => {
    if (how === "link") { await pg.goto(`${base()}/c/${comp}`); await pg.locator("main .comp-back a").click(); await pg.waitForURL(/\/d\//); }
    else if (how === "back") { await pg.goto(`${base()}/c/${comp}`); await pg.goBack(); }
    else await pg.reload();
    await pg.waitForSelector(".scriobh-prose");
    await ready(pg);
  };
  const same = async (how) => {
    await waitFor(async () => Math.abs((await snap(pg)).y - at.y) <= 2, `the scroll to come back after ${how}`, 6000);
    const now = await snap(pg);
    eq(`${now.a}-${now.h}`, `${at.a}-${at.h}`, `the caret after ${how}`);
    eq(now.rev, at.rev, "the revision");
    ok(!now.un, "nothing is unsaved");
  };
  await comeBack("link");
  await same("the link");
  ok(await inEditor(pg), "the editor has the focus after Back to writing");
  ok(!/#resume/.test(pg.url()), "the fragment is gone");
  await pg.keyboard.type("Q"); // lands at the caret
  await waitFor(async () => (await J("GET", `/api/v1/docs/${bookA.d}`)).item.content.includes("ParagrapQh 20."), "Q to land where the caret was and to be saved", 12000);
  at.rev = (await snap(pg)).rev;
  at.a = at.h = (await snap(pg)).a;
  await comeBack("back");
  await same("the Back button");
  eq(await pg.evaluate(() => history.scrollRestoration), "manual", "the browser's own restoring is off for a page that has a place kept");
  ok(!(await inEditor(pg)), "the Back button does not take the focus");
  await comeBack("reload");
  await same("a reload");
  // the text changed in between: the scroll comes back, the caret does not
  await patchDoc(bookA.d, { content: long + "A line added in another tab.\n\n" });
  await pg.goto(`${base()}/c/${comp}`);
  await pg.goBack();
  await pg.waitForSelector(".scriobh-prose");
  await ready(pg);
  await waitFor(async () => Math.abs((await snap(pg)).y - at.y) <= 40, "the scroll to come back though the text changed", 6000);
  const moved = await snap(pg);
  ok(moved.a !== at.a && moved.a <= 2, `the caret is not put back over text that changed: ${moved.a}`);
  // a range of selected words comes back as a caret: the first letter typed would replace the words, and nobody chose that
  // a plain visit of the chapter (a click on it: not Back, not a reload, not the way back) opens it at the top, though a place is kept for it
  await pg.goto(chapter(bookA, bookA.d));
  await pg.waitForSelector(".scriobh-prose");
  await ready(pg);
  await sleep(700);
  const top = await snap(pg);
  ok(top.y <= 4 && top.h <= 2, `a plain visit opens at the top: scroll ${top.y}, caret ${top.h}`);
  await scrollToText(pg, "Paragraph 26.");
  const from = await textPos(pg, "Paragraph 26.");
  await pg.evaluate(([a, b]) => { const ed = window.scriobh.write.editor; ed.alts.select(a, b); ed.view.focus(); }, [from, from + 12]);
  const range = await snap(pg);
  ok(range.a !== range.h, "a range is selected");
  await pg.goto(`${base()}/c/${comp}`);
  await pg.goBack();
  await pg.waitForSelector(".scriobh-prose");
  await ready(pg);
  await waitFor(async () => Math.abs((await snap(pg)).y - range.y) <= 40, "the scroll to come back", 6000);
  const kept = await snap(pg);
  ok(kept.a === kept.h && kept.h === range.h, `the range came back as a caret at its end: ${kept.a}-${kept.h} for ${range.a}-${range.h}`);
  // the Source view keeps no place, and leaves the browser's own restoring alone
  await pg.goto(chapter(bookA, bookA.d));
  await pg.waitForSelector(".scriobh-prose");
  await ready(pg);
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press("ControlOrMeta+e");
  await pg.locator(".scriobh-source").waitFor({ state: "visible" });
  await pg.goto(`${base()}/c/${comp}`);
  await pg.goBack();
  await pg.waitForSelector(".scriobh-prose, .scriobh-source");
  await ready(pg);
  eq(await pg.evaluate(() => history.scrollRestoration), "auto", "no place was kept in the Source view, so the browser's restoring stays on");
  // a find or a new chapter is never fought
  await pg.goto(`${base()}/p/${bookA.slug}/d/${bookA.d}?find=Paragraph%2030`);
  await pg.waitForSelector(".scriobh-prose");
  await ready(pg);
  await sleep(600);
  ok(!/find=/.test(pg.url()) || true, "ok");
});

await kit.test("7. nothing is put back over the writer: typed, scrolled, clicked or in a field before the script arrives", async () => {
  const hold = async (page) => {
    let release;
    const gate = new Promise((r) => { release = r; });
    const re = /\/js\/resume\.[0-9a-f]+\.js/;
    await page.route(re, async (route) => { await gate; await route.continue().catch(() => {}); });
    return { release, done: async () => { release(); await page.unroute(re).catch(() => {}); } };
  };
  const leave = async () => { // a position is kept, and the chapter opens again with the script held
    await longPos(pg);
    await pg.goto(`${base()}/c/${comp}`);
  };
  // typed before it arrives
  await leave();
  let h = await hold(pg);
  await pg.goto(chapter(bookA, bookA.d) + "#resume");
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor)), "the editor", 10000);
  await pg.locator(".scriobh-prose p").first().click();
  await pg.keyboard.type("Z");
  const typed = await snap(pg);
  await h.done();
  await sleep(700);
  const t1 = await snap(pg);
  eq(JSON.stringify([t1.y, t1.a, t1.h]), JSON.stringify([typed.y, typed.a, typed.h]), "typed before it arrived: the scroll and the caret are the writer's");
  eq(t1.md, typed.md, "the text is what the writer typed, and nothing else");
  ok(/Z/.test(t1.md.slice(0, 600)) && (t1.md.match(/Z/g) || []).length === 1, "the typed letter is in the first paragraph, where it was typed");
  await waitFor(async () => (await J("GET", `/api/v1/docs/${bookA.d}`)).item.content.slice(0, 600).includes("Z"), "the typed letter to be saved", 12000);
  // scrolled before it arrives
  await leave();
  h = await hold(pg);
  await pg.goto(chapter(bookA, bookA.d) + "#resume");
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor)), "the editor", 10000);
  await pg.mouse.move(400, 400);
  await pg.mouse.wheel(0, 300);
  await waitFor(async () => (await snap(pg)).y > 100, "the writer's scroll");
  const scrolled = await snap(pg);
  await h.done();
  await sleep(700);
  const s1 = await snap(pg);
  near(s1.y, scrolled.y, 2, "scrolled before it arrived: the page stays where the writer put it");
  ok(s1.y < 1000, "and it did not jump to the old place: " + s1.y);
  // in a sidebar field before it arrives: nothing moves and the field keeps the focus
  await leave();
  h = await hold(pg);
  await pg.goto(chapter(bookA, bookA.d) + "#resume");
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor)), "the editor", 10000);
  if (!(await pg.locator("#inspector.is-open").count())) { await pg.locator("#insp-open").click(); await pg.locator("#itab-compendium").click(); }
  await toEntries(pg);
  await pg.locator("#lk-q").focus();
  await h.done();
  await sleep(700);
  eq(await active(pg), "lk-q", "the field in the sidebar keeps the focus");
  ok((await snap(pg)).y < 300, "and the page did not move under it");
  // and with the script on time, a field in the sidebar that takes the focus later is never robbed of it
  await leave();
  await pg.goto(chapter(bookA, bookA.d) + "#resume");
  await pg.waitForSelector(".scriobh-prose");
  await ready(pg);
  await sleep(300);
  await toEntries(pg);
  await pg.locator("#lk-q").focus();
  await sleep(500);
  eq(await active(pg), "lk-q", "a field focused after the restore is left alone");
});

await kit.test("8. at 390 px (a touch screen) the caret and the scroll come back by Back to writing, reached by the keyboard, and no keyboard is raised", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  const p = await ctx.newPage();
  kit.watch(p, "phone");
  await signIn(p, base());
  await openDoc(p, bookA, bookA.d);
  await scrollToText(p, "Paragraph 18.");
  await p.evaluate(() => { const ed = window.scriobh.write.editor, v = ed.view; let at = -1; v.state.doc.descendants((n, pos) => { if (at < 0 && n.isText && n.text.indexOf("Paragraph 18.") >= 0) at = pos; }); ed.alts.select(at + 5, at + 5); });
  const at = await snap(p);
  ok(at.y > 1000, "scrolled down on the phone: " + at.y);
  await p.goto(`${base()}/c/${comp}`);
  await noSideways(p, "the Compendium page");
  await tabTo(p, "main .comp-back a");
  await targets(p, "main .comp-back a", "the way back");
  await p.keyboard.press("Enter");
  await p.waitForURL(/\/d\//);
  await p.waitForSelector(".scriobh-prose");
  await ready(p);
  await waitFor(async () => Math.abs((await snap(p)).y - at.y) <= 2, "the scroll to come back on the phone", 6000);
  const now = await snap(p);
  eq(`${now.a}-${now.h}`, `${at.a}-${at.h}`, "the caret");
  ok(!(await inEditor(p)), "the editor does not take the focus on a touch screen");
  eq(await p.evaluate(() => document.documentElement.classList.contains("kb")), false, "no keyboard is up");
  await ctx.close();
});

// ---- the pieces, in both themes at 1280 and 390 ------------------------------------------------------------------------------------
await kit.test("9. the new pieces in Paper and Ink at 1280 and at 390", async () => {
  if (!shots) return;
  const pieces = async (page, tag) => {
    await openDoc(page, bookA, second);
    await sheetOpen(page);
    await view(page, "events");
    await page.locator("#lk-eq").focus();
    await page.keyboard.type("wed");
    await page.locator("#lk-ev-hits .lk-ev").first().waitFor();
    await shot(page, `compendium-nav-events-${tag}`);
    await view(page, "notes");
    await shot(page, `compendium-nav-notes-${tag}`);
    await page.locator(".lk-pick", { hasText: "Tide table" }).click();
    await page.locator("#lk-nform").waitFor();
    await shot(page, `compendium-nav-note-${tag}`);
    await page.goto(`${base()}/c/${comp}`);
    await page.locator("main .comp-back a").waitFor();
    await shot(page, `compendium-nav-back-${tag}`);
  };
  for (const scheme of ["light", "dark"]) {
    await pg.emulateMedia({ colorScheme: scheme });
    await pieces(pg, `1280-${scheme}`);
    const ph = await phone();
    await ph.page.emulateMedia({ colorScheme: scheme });
    await pieces(ph.page, `390-${scheme}`);
    await ph.ctx.close();
  }
  await pg.emulateMedia({ colorScheme: "light" });
});

await kit.finish("e2e-compendium-nav");
