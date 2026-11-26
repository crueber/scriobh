#!/usr/bin/env node
// The Compendium tab of the writing view's inspector, in a real Chromium (F-1253 to F-1261).
//
//   node scripts/e2e-sidebar.mjs [--only substring] [--shots DIR]
//
// The acceptance test is the first case: the editor's text, caret, scroll, revision and unsaved state, and the box of the page,
// are the same after everything the sidebar can do, and the editor never took the focus. Then lookup, edit with a stale
// revision (Keep mine, Use theirs), this book's version, another name typed and from the selection (the marks in the text follow),
// a new entry from the selection, point of view and location, what a reader, a stranger to the Compendium and a book with no
// Compendium are shown, the tab kept from chapter to chapter, the phone's sheet by keyboard alone, the density of the pages
// (numbers measured on the old pages, below), the session that ends while the panel is open, and the network cut.
import { mkdirSync } from "node:fs";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { seedBible, wordCount } from "./entity-fixture.mjs";

const kit = await createKit("e2e-sidebar");
kit.deadline(14 * 60 * 1000);
const arg = (n) => (process.argv.includes(n) ? process.argv[process.argv.indexOf(n) + 1] : "");
const only = arg("--only").split("|").filter(Boolean); // substrings of the cases to run, "|" between them
kit.only = "";
const shots = arg("--shots");
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;
const baseTest = kit.test;
kit.test = (name, fn) => (only.length && !only.some((o) => name.includes(o))) ? undefined : baseTest(name, async () => {
  try { await fn(); } catch (e) { if (shots) await pg.screenshot({ path: `${shots}/fail-${name.split(".")[0]}.png` }).catch(() => {}); await pg.context().setOffline(false).catch(() => {}); throw e; }
});
const J = async (method, path, data) => {
  const r = await api(main, method, path, data);
  if (r.status >= 300) throw new Error(`${method} ${path}: ${r.status} ${JSON.stringify(r.json)}`);
  return r.json;
};
// --slow MS holds every answer of the panel's list and entry requests for MS before the page sees it, as a busy machine does: the steps must hold on a
// slow answer as on a quick one (the root cause of the one failure of step 2 in a sequential run was a step that only held on a quick one).
const slow = Number(arg("--slow")) || 0;
if (slow) await pg.route(/\/ui\/projects\/[^/]+\/lookup(\?|$)/, async (route) => { if (route.request().method() === "GET") await new Promise((r) => setTimeout(r, slow)); await route.continue(); });
// LKLOG=1 prints every request of the panel with the time it was sent and answered: what to read when a step waits for a box that does not come.
if (process.env.LKLOG) { const t0 = Date.now(); pg.on("request", (r) => { if (/lookup/.test(r.url())) console.log("  >", Date.now() - t0, r.method(), r.url().replace(/^.*lookup/, "lookup")); }); pg.on("response", (r) => { if (/lookup/.test(r.url())) console.log("  <", Date.now() - t0, r.status(), r.url().replace(/^.*lookup/, "lookup")); }); }
const shot = async (page, name) => { if (shots) await page.screenshot({ path: `${shots}/${name}.png` }); };

// The numbers measured on the old pages with scripts/screens-entities.mjs (docs/screens/entities-before/numbers.json).
const BEFORE = { sheetHeight1280: 3306, rows1280: 6, rows390: 4 };

// ---- a book, a Compendium, and a chapter that is long enough to scroll --------------------------------------------------------
const bible = await seedBible(J, { fillers: 15 });
const maire = bible.maire;
const book = (await J("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel" })).item;
await J("PUT", `/api/v1/projects/${book.id}/compendium`, { compendium_id: bible.comp.id });
const docs0 = (await J("GET", `/api/v1/projects/${book.id}/docs`)).order[0];
const para = (i) => `Paragraph ${i}. ` + "The tide came in over the slip and the ferry waited for the light to change, as it always did, while the boats knocked together in the dark. ".repeat(3) + "\n\n";
let long = "";
for (let i = 1; i <= 30; i++) long += para(i);
long += "Mrs Riviera said hello to Máire Nolan, and Captain Vell stood by the slip.\n\n";
for (let i = 31; i <= 34; i++) long += para(i);
const docId = async (title, content, kind = "chapter") => (await J("POST", `/api/v1/projects/${book.id}/docs`, { title, kind, content })).item.id;
const putDoc = async (id, content) => { const cur = (await J("GET", `/api/v1/docs/${id}`)).item; await J("PATCH", `/api/v1/docs/${id}`, { base_revision: cur.revision, content }); };
await putDoc(docs0, long);
const second = await docId("Second chapter", "Short words.\n");
await J("PUT", "/api/v1/me/prefs", { items: { autosave_ms: "5000" } }); // the editor's own save waits, so unsaved text can be compared
const getE = async (id) => (await J("GET", `/api/v1/entities/${id}`)).item;
const chapter = (d = docs0) => `${base()}/p/${book.slug}/d/${d}`;

const openDoc = async (page, d = docs0) => {
  await page.goto(chapter(d));
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor && window.scriobh.alts && window.scriobh.inspector && window.scriobh.chords)), "the page's scripts", 12000);
  await sleep(250);
};
const snap = (page) => page.evaluate(() => {
  const W = window.scriobh.write, v = W.editor.view, s = v.state.selection;
  return { y: Math.round(window.scrollY), md: W.editor.getMarkdown(), a: s.anchor, h: s.head, rev: W.rev, un: !!W.unsaved() };
});
const watch = (page) => page.evaluate(() => {
  window.__box = []; window.__focus = [];
  const p = document.querySelector("#paper");
  let last = "";
  const tick = () => { const r = p.getBoundingClientRect(), k = [r.x, r.y + window.scrollY, r.width, r.height].map(Math.round).join(","); if (k !== last) { if (last) window.__box.push(k); last = k; } requestAnimationFrame(tick); };
  tick();
  document.addEventListener("focusin", (e) => { if (e.target.closest(".scriobh-prose, .scriobh-editor")) window.__focus.push("editor"); }, true);
  window.__scroll = [];
  window.addEventListener("scroll", () => { const a = document.activeElement; const i = document.querySelector("#inspector"), r = i.getBoundingClientRect(), b = a && a.getBoundingClientRect(); window.__scroll.push(Math.round(scrollY) + "@" + Math.round(performance.now()) + ":" + (a ? (a.id || a.tagName) : "") + " insp[" + Math.round(r.top) + "," + Math.round(r.bottom) + "," + i.scrollTop + "/" + i.scrollHeight + "] el[" + (b ? Math.round(b.top) + "," + Math.round(b.bottom) : "") + "]"); });
});
const textPos = (page, needle) => page.evaluate((n) => {
  let at = -1;
  window.scriobh.write.editor.view.state.doc.descendants((node, pos) => { if (at < 0 && node.isText) { const i = node.text.indexOf(n); if (i >= 0) at = pos + i; } });
  return at;
}, needle);
const selectText = async (page, needle) => {
  const at = await textPos(page, needle);
  if (at < 0) throw new Error("no such text: " + needle);
  await page.evaluate(([a, b]) => { const ed = window.scriobh.write.editor; ed.alts.select(a, b); ed.view.focus(); }, [at, at + needle.length]);
};
const caretAt = async (page, needle, offset = 0) => {
  const at = (await textPos(page, needle)) + offset;
  await page.evaluate((a) => { const ed = window.scriobh.write.editor; ed.alts.select(a, a); ed.view.focus(); }, at);
};
const scrollToText = (page, needle) => page.evaluate((n) => {
  const v = window.scriobh.write.editor.view; let at = -1;
  v.state.doc.descendants((node, pos) => { if (at < 0 && node.isText && node.text.indexOf(n) >= 0) at = pos; });
  const r = v.coordsAtPos(at); window.scrollTo(0, window.scrollY + r.top - 300);
}, needle);
const inEditor = (page) => page.evaluate(() => !!(document.activeElement && document.activeElement.closest(".scriobh-prose")));
// Fill a box the way a person does, by focusing it (the browser scrolls what it must, as it would for Tab) and typing: Playwright's own
// fill() scrolls every ancestor, the page included, to put the box in the middle, which no person's keystroke does.
const fillIn = async (page, sel, text) => { await page.locator(sel).focus(); await page.keyboard.press("ControlOrMeta+a"); if (text === "") await page.keyboard.press("Delete"); else await page.keyboard.insertText(text); };
const status = (page) => page.locator("#lk-status").innerText();
const openTab = async (page, how = "chord") => {
  if (how === "chord") await page.keyboard.press("Alt+Shift+KeyC");
  else {
    if (!(await page.locator("#inspector.is-open").count())) await page.locator("#insp-open").click();
    await page.locator("#itab-compendium").click();
  }
  await page.locator("#lk-main .lk-search, #lk-entry, #lk-none, #lk-closed").first().waitFor();
};
const openEntry = async (page, name) => {
  await page.locator("#lk-q").fill(name);
  const row = page.locator(".lk-pick", { hasText: name }).first();
  await row.waitFor();
  await row.click();
  await page.locator("#lk-entry").waitFor();
};
const savedStatus = (page) => waitFor(async () => /Saved at/.test(await status(page)), "the status line to say Saved", 6000);
const noSideways = async (page, what) => eq(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, `${what}: no sideways scroll`);
const marks = (page) => page.evaluate(() => [...document.querySelectorAll(".scriobh-prose .mention")].map((m) => m.textContent));

// ===================================================================================================================================
await kit.test("1. the editor is untouched while the sidebar is edited, and the Esc and the chord come back to the caret", async () => {
  await openDoc(pg);
  await scrollToText(pg, "Paragraph 15.");
  await caretAt(pg, "Paragraph 15.", 4);
  await pg.keyboard.type("XYZ");
  ok((await snap(pg)).un, "XYZ is unsaved while the editor has the focus");
  await openTab(pg, "chord"); // the chord opens the tab (the inspector opens now, which is the one layout change)
  await pg.locator("#lk-q").waitFor();
  await waitFor(() => inEditor(pg).then((x) => !x), "the focus to be in the panel");
  // leaving the editor saved XYZ (its own rule); now words that are unsaved while the focus is in the panel: a change the editor
  // has not saved yet, which is what the sidebar must never lose
  await waitFor(async () => !(await snap(pg)).un, "the editor to finish its own save", 8000);
  await pg.evaluate(() => { const v = window.scriobh.write.editor.view; v.dispatch(v.state.tr.insertText("ABC")); });
  const before = await snap(pg);
  ok(before.un && before.md.includes("XYZ") && before.md.includes("ABC"), "ABC is unsaved, XYZ is in the text: " + before.un);
  const saves = await pg.evaluate(() => performance.getEntriesByType("resource").filter((e) => /\/api\/v1\/docs\//.test(e.name)).length);
  await watch(pg);
  // search, open the entry, and edit the summary, a field, the description, add a name, each until Saved
  const Y = async (what) => { const y = await pg.evaluate(() => Math.round(scrollY)); if (y !== before.y) console.log("      scroll moved at", what, y, "from", before.y, await pg.evaluate(() => JSON.stringify({ sc: window.__scroll, box: window.__box, h: document.documentElement.scrollHeight, w: innerWidth }))); };
  await fillIn(pg, "#lk-q", "Máire");
  await Y("search");
  await pg.locator(".lk-pick", { hasText: "Máire Nolan" }).first().click();
  await pg.locator("#lk-entry").waitFor();
  await Y("open");
  await fillIn(pg, "#es-summary", "A ferrywoman with a long memory, and a hat.");
  await Y("summary");
  await savedStatus(pg);
  eq((await getE(maire.id)).summary, "A ferrywoman with a long memory, and a hat.", "the stored summary");
  await fillIn(pg, "#ef-appearance", "Grey eyes, a weathered coat.");
  await waitFor(async () => (await getE(maire.id)).fields.appearance === "Grey eyes, a weathered coat.", "the field to be stored", 6000);
  const body = (await getE(maire.id)).body;
  await fillIn(pg, "#es-body", body + "\n\nShe never sleeps before the last boat is in.");
  await waitFor(async () => (await getE(maire.id)).body.includes("never sleeps"), "the description to be stored", 6000);
  await fillIn(pg, "#lk-aka-in", "Old Mam");
  await pg.keyboard.press("Enter");
  await pg.locator(".en-chip", { hasText: "Old Mam" }).waitFor();
  const after = await snap(pg);
  eq(after.md, before.md, "the editor's text");
  eq(after.a + "/" + after.h, before.a + "/" + before.h, "the editor's selection");
  eq(after.y, before.y, "the page's scroll " + (await pg.evaluate(() => JSON.stringify(window.__scroll))));
  eq(after.rev, before.rev, "the document's revision");
  eq(after.un, before.un, "the editor's unsaved state");
  eq(await pg.evaluate(() => performance.getEntriesByType("resource").filter((e) => /\/api\/v1\/docs\//.test(e.name)).length), saves, "the sidebar made no request about the document");
  eq(await pg.evaluate(() => window.__focus.length), 0, "the editor never took the focus while the panel was edited");
  eq(await pg.evaluate(() => window.__box.length), 0, "the page's box never moved: " + (await pg.evaluate(() => window.__box.join(" | "))));
  ok((await getE(maire.id)).aliases.some((a) => a.alias === "Old Mam"), "the name was added");
  // Esc: the caret is where it was, and typing lands there
  await pg.locator("#es-summary").focus();
  await pg.keyboard.press("Escape");
  ok(await inEditor(pg), "Esc returns to the editor");
  const esc = await snap(pg);
  eq(esc.a + "/" + esc.h, before.a + "/" + before.h, "the caret after Esc");
  eq(esc.y, before.y, "the scroll after Esc");
  await pg.keyboard.type("Q");
  ok((await snap(pg)).md.includes("ABCQ"), "typing after Esc lands at the caret");
  // the chord from inside the panel does the same
  await pg.keyboard.press("Alt+Shift+KeyC");
  await waitFor(async () => !(await inEditor(pg)), "the chord to open the panel");
  await pg.keyboard.press("Alt+Shift+KeyC");
  ok(await inEditor(pg), "the chord inside the panel returns to the editor");
  // the unsaved XYZQ then saves by itself with the right content
  await waitFor(async () => (await J("GET", `/api/v1/docs/${docs0}`)).item.content.includes("ABCQ"), "the editor's own autosave", 12000);
  eq(await pg.evaluate(() => window.__box.length > 0 ? "moved" : "still"), "still", "the box stayed put through the chord, too");
});

await kit.test("2. look up by name and by other name; the entry shows its fields, names and description; the links open the right pages", async () => {
  await openDoc(pg);
  await openTab(pg, "tab");
  await fillIn(pg, "#lk-q", "Mam");
  await waitFor(async () => (await pg.locator(".lk-pick").count()) === 1, "a search by another name to find the entry only");
  ok((await pg.locator(".lk-pick").innerText()).includes("Máire Nolan"), "and it is Máire");
  await fillIn(pg, "#lk-q", "quay");
  await pg.locator(".lk-pick", { hasText: "Salt Quay" }).first().waitFor();
  await fillIn(pg, "#lk-q", "");
  await pg.locator("#lk-kind").selectOption("place");
  const places = (await J("GET", `/api/v1/compendiums/${bible.comp.id}/entities?kind=place&limit=100`)).items.length;
  await waitFor(async () => (await pg.locator(".lk-pick").count()) === places, "the kind filter");
  // Enter opens the first result of what was typed, not of the answer that happens to land next. The kind just cleared has a list on its way;
  // it is held here for longer than it takes to type a name and press Enter (a busy machine does the same), so Enter is pressed while it is
  // still out. The step used to pass only when that list came back first: it opened the first row of the wrong list.
  const hold = /\/ui\/projects\/[^/]+\/lookup\?(?!.*q=Tadhg)/;
  await pg.route(hold, async (route) => { await sleep(900); await route.fallback(); });
  try {
    await pg.locator("#lk-kind").selectOption("");
    await fillIn(pg, "#lk-q", "Tadhg");
    await pg.keyboard.press("Enter"); // Enter opens the first result
    await pg.locator("#lk-entry").waitFor();
    eq(await pg.locator("#lk-h").innerText(), "Tadhg Ó Néill", "the entry's heading, with the older list still on its way");
    await sleep(1100); // the held answer lands now and replaces nothing that is open
    eq(await pg.locator("#lk-h").innerText(), "Tadhg Ó Néill", "and the late list changes nothing");
  } finally { await pg.unroute(hold); }
  ok(await pg.evaluate(() => document.activeElement && document.activeElement.id === "lk-h"), "the heading has the focus after an explicit open");
  await pg.locator("#lk-back").click();
  await pg.locator("#lk-q").waitFor();
  await openEntry(pg, "Máire");
  const t = await pg.locator("#lk-entry").innerText();
  ok(/Ferry fare/.test(t) && /Mrs Nolan/.test(t) && /The Ferrywoman/.test(t), "fields and names: " + t.slice(0, 400));
  eq(await pg.locator("#es-body").inputValue().then((v) => wordCount(v) > 150), true, "the description is in the panel");
  ok(/Fields \(4 of 11 filled\)/.test(t) || /Fields \(\d of 11 filled\)/.test(t), "the count of filled fields: " + t.match(/Fields \([^)]*\)/));
  // the links go through the page's own click handler: unsaved text is flushed first
  await caretAt(pg, "Paragraph 3.", 4);
  await pg.keyboard.type("LINK");
  await pg.locator('a:has-text("Open in the Compendium")').click();
  await pg.waitForURL(new RegExp(`/c/${bible.comp.slug}/e/${maire.id}`));
  await pg.goBack();
  await pg.waitForSelector(".scriobh-prose");
  ok((await J("GET", `/api/v1/docs/${docs0}`)).item.content.includes("LINK"), "the text typed before the link was saved");
});

await kit.test("3. two tabs: a stale save shows the conflict box and keeps the words; Keep mine wins; Use theirs replaces the form only on that click", async () => {
  kit.allowed.push(/status of 422/);
  await openDoc(pg);
  await openTab(pg);
  await openEntry(pg, "Tadhg");
  const tid = bible.tadhg.id;
  // someone else changes the summary, the description and another field, in the other tab
  await J("PATCH", `/api/v1/entities/${tid}`, { summary: "Changed in the other tab.", body: "Their description.", fields: { appearance: "Remote appearance" } });
  await fillIn(pg, "#es-summary", "My careful summary");
  await waitFor(async () => (await pg.locator(".lk-conflict").count()) > 0, "the conflict box", 8000);
  const box = await pg.locator(".lk-conflict").innerText();
  ok(box.includes("Summary") && box.includes("Changed in the other tab."), "the saved words of the field that differs are shown: " + box);
  ok(!box.includes("Their description."), "a field the writer did not change is not in the box: " + box);
  eq(await pg.locator("#es-summary").inputValue(), "My careful summary", "the typed words are kept");
  eq((await getE(tid)).summary, "Changed in the other tab.", "a stale save went through");
  await pg.getByRole("button", { name: "Keep mine" }).click();
  await savedStatus(pg);
  eq((await getE(tid)).summary, "My careful summary", "Keep mine did not save");
  const after = await getE(tid);
  eq(after.fields.appearance, "Remote appearance", "a field changed elsewhere survives Keep mine");
  eq(after.body, "Their description.", "so does the description");
  // the description is protected in the same way, and Use theirs replaces the form on that click only
  await J("PATCH", `/api/v1/entities/${tid}`, { body: "Changed again, elsewhere." });
  await fillIn(pg, "#es-body", "A long paragraph typed here, in the sidebar.");
  await waitFor(async () => (await pg.locator(".lk-conflict").count()) > 0, "the conflict box for the description", 8000);
  eq(await pg.locator("#es-body").inputValue(), "A long paragraph typed here, in the sidebar.", "the typed description is kept");
  eq((await getE(tid)).body, "Changed again, elsewhere.", "the stale description did not go through");
  await pg.getByRole("button", { name: "Use theirs" }).click();
  await waitFor(async () => (await pg.locator("#es-body").inputValue()) === "Changed again, elsewhere.", "the form to be replaced by what is saved", 6000);
  eq(await pg.locator(".lk-conflict").count(), 0, "the conflict box is gone");
  // a form with words in it is not replaced when the tab becomes visible again
  await fillIn(pg, "#es-summary", "Typed and not yet saved");
  await pg.evaluate(() => { Object.defineProperty(document, "hidden", { configurable: true, get: () => false }); document.dispatchEvent(new Event("visibilitychange")); });
  await sleep(500);
  eq(await pg.locator("#es-summary").inputValue(), "Typed and not yet saved", "a dirty form is not replaced");
  await savedStatus(pg);
});

await kit.test("4. this book's version: set, shown as the book's, the Compendium's own record unchanged; cleared", async () => {
  await openDoc(pg);
  await openTab(pg);
  await openEntry(pg, "Máire");
  await pg.getByRole("button", { name: "This book", exact: true }).click();
  await pg.locator("#ov-name").waitFor();
  await pg.locator("#ef-age").fill("41");
  await pg.locator("#ov-notes").fill("Older in book two.");
  await waitFor(async () => /Saved for this book/.test(await status(pg)), "the book's version to be saved", 6000);
  eq((await getE(maire.id)).fields.age, "34", "the Compendium's record changed");
  const pe = (await J("GET", `/api/v1/projects/${book.id}/entity?entity=${maire.id}`)).item;
  eq(pe.fields.age, "41", "the book reads its own age");
  const page2 = await (await kit.newContext({ viewport: { width: 1280, height: 800 } })).newPage();
  await page2.context().addCookies(await main.cookies());
  await page2.goto(`${base()}/c/${bible.comp.slug}/e/${maire.id}`);
  eq(await page2.locator("#ef-age").inputValue(), "34", "the Compendium's own page shows the old age");
  await page2.close();
  await pg.getByRole("button", { name: "Clear this book's version" }).click();
  await pg.locator("#lk-entry").waitFor();
  await waitFor(async () => !(await J("GET", `/api/v1/projects/${book.id}/entity?entity=${maire.id}`)).item.overridden, "the override to be cleared", 6000);
});

await kit.test("5. Also known as, typed: added with Enter, a word another entry holds names that entry, remove", async () => {
  kit.allowed.push(/status of 422/);
  await openDoc(pg);
  await openTab(pg);
  await openEntry(pg, "Máire");
  await fillIn(pg, "#lk-aka-in", "Mrs Riviera");
  await pg.keyboard.press("Enter");
  await pg.locator(".en-chip", { hasText: "Mrs Riviera" }).waitFor();
  ok((await getE(maire.id)).aliases.some((a) => a.alias === "Mrs Riviera"), "stored");
  await fillIn(pg, "#lk-aka-in", "salt  quay");
  await pg.keyboard.press("Enter");
  await waitFor(async () => /Salt Quay/.test(await pg.locator("#lk-aka-status").innerText()), "the collision message, by the names", 6000);
  ok(await pg.getByRole("button", { name: /Open Salt Quay/ }).count(), "a button that opens the entry that holds the word");
  eq((await getE(maire.id)).aliases.filter((a) => /quay/i.test(a.alias)).length, 0, "a refused name was added");
  // the field itself stops at 200 characters; the server refuses more, and a name with a line break, in the same words
  eq(await pg.locator("#lk-aka-in").getAttribute("maxlength"), "200", "the field stops at 200 characters");
  const csrf = await pg.evaluate(() => document.querySelector('meta[name="csrf-token"]').content);
  for (const name of ["x".repeat(201), "two\nlines"]) {
    const r = await pg.request.post(`${base()}/ui/projects/${book.id}/lookup/alias`, { form: { entity_id: maire.id, alias: name }, headers: { "X-CSRF-Token": csrf, "X-Scriobh-Fetch": "1" } });
    eq(r.status(), 422, "a name that is too long or has a line break is refused");
    ok(/too long|runs over a line/.test(await r.text()), "in words");
  }
  eq((await getE(maire.id)).aliases.filter((a) => /^x+$|lines/.test(a.alias)).length, 0, "neither was added");
  await pg.getByRole("button", { name: "Remove the name Mrs Riviera" }).click();
  await waitFor(async () => !(await getE(maire.id)).aliases.some((a) => a.alias === "Mrs Riviera"), "the name to be removed", 6000);
});

await kit.test("6. Also known as, from the selected text: the chord, Add as another name for, the mark appears and the text is unchanged", async () => {
  await openDoc(pg);
  await scrollToText(pg, "Mrs Riviera said");
  await selectText(pg, "Mrs Riviera");
  const before = (await snap(pg)).md;
  ok(!(await marks(pg)).includes("Mrs Riviera"), "not a mark yet");
  await pg.keyboard.press("Alt+Shift+KeyC");
  await pg.locator("#lk-first").waitFor();
  ok(/Mrs Riviera/.test(await pg.locator(".lk-quote").innerText()), "the selected words are quoted");
  ok(await pg.evaluate(() => document.activeElement && document.activeElement.id === "lk-first"), "the focus is on the first action");
  await pg.locator("#lk-first").click();
  await pg.locator(".lk-target button").first().waitFor();
  await pg.locator(".lk-row", { hasText: "Máire Nolan" }).locator(".lk-target button").click();
  await pg.locator("#lk-entry").waitFor();
  await pg.locator(".en-chip", { hasText: "Mrs Riviera" }).waitFor();
  ok(/underlined/.test(await status(pg)), "the panel says the name will be underlined: " + (await status(pg)));
  await waitFor(async () => (await marks(pg)).includes("Mrs Riviera"), "the mark under Mrs Riviera", 20000);
  eq((await snap(pg)).md, before, "the text itself is unchanged");
  await J("DELETE", `/api/v1/entities/${maire.id}/aliases?alias_id=${(await getE(maire.id)).aliases.find((a) => a.alias === "Mrs Riviera").id}`);
});

await kit.test("7. a new entry from the selected text: kind required, created in the primary Compendium, underlined; a name that exists shows that entry", async () => {
  kit.allowed.push(/status of 422/);
  await openDoc(pg);
  await scrollToText(pg, "Captain Vell stood");
  await selectText(pg, "Captain Vell");
  await pg.keyboard.press("Alt+Shift+KeyC");
  await pg.locator("#lk-first").waitFor();
  await pg.getByRole("button", { name: "New entry", exact: true }).first().click();
  await pg.locator("#lkn-name").waitFor();
  eq(await pg.locator("#lkn-name").inputValue(), "Captain Vell", "the name is prefilled");
  eq(await pg.locator('input[name="kind"]:checked').count(), 0, "no kind is chosen for the writer");
  await pg.getByRole("button", { name: "Create" }).click();
  await pg.locator("#lk-new-err").waitFor();
  eq(await pg.locator("#lkn-name").inputValue(), "Captain Vell", "the words are kept");
  await pg.getByLabel("Character").check();
  await pg.getByRole("button", { name: "Create" }).click();
  await pg.locator("#lk-entry").waitFor();
  eq(await pg.locator("#lk-h").innerText(), "Captain Vell", "the new entry opens");
  ok(/Added to The Salt Road Bible/.test(await status(pg)), "the status: " + (await status(pg)));
  const list = (await J("GET", `/api/v1/compendiums/${bible.comp.id}/entities?name=Captain%20Vell`)).items;
  eq(list.length, 1, "created in the primary Compendium");
  await waitFor(async () => (await marks(pg)).includes("Captain Vell"), "the new name to be underlined", 20000);
  // a name that exists shows the existing entry instead
  await selectText(pg, "Máire Nolan");
  await pg.keyboard.press("Alt+Shift+KeyC");
  await pg.locator("#lk-first").waitFor();
  ok(/Already a name of Máire Nolan/.test(await pg.locator(".lk-already").innerText()), "the exact match is said");
  await pg.getByRole("button", { name: "New entry", exact: true }).first().click();
  await pg.locator("#lkn-name").waitFor();
  await pg.getByLabel("Character").check();
  await pg.getByRole("button", { name: "Create" }).click();
  await pg.getByRole("button", { name: /Open Máire Nolan/ }).waitFor();
  eq(await pg.locator("#lkn-name").inputValue(), "Máire Nolan", "the typed name is kept");
});

await kit.test("8. scene links: Use as location, the scene tab's card and an underlined name open the entry in the tab", async () => {
  await openDoc(pg);
  await openTab(pg);
  await openEntry(pg, "Salt Quay");
  const rev = await pg.evaluate(() => window.scriobh.write.rev);
  await pg.getByRole("button", { name: "Use as location" }).click();
  await waitFor(async () => /Linked as the location/.test(await status(pg)), "the link to be made", 6000);
  ok((await pg.evaluate(() => window.scriobh.write.rev)) > rev, "the document's revision rose");
  await pg.locator("#itab-scene").click();
  await pg.locator("#ipanel-scene .sc-refname", { hasText: "Salt Quay" }).first().waitFor();
  const card = pg.locator("#ipanel-scene .sc-item", { has: pg.locator(".sc-name", { hasText: "Máire Nolan" }) }).first();
  await card.locator(".sc-name").click();
  await card.getByRole("button", { name: "Open in the Compendium tab" }).click();
  await waitFor(async () => (await pg.locator("#lk-h").innerText()) === "Máire Nolan", "the scene card to open the entry in the tab", 6000);
  // Ctrl or Command click on an underlined name opens it in the tab and does not navigate
  await waitFor(async () => (await marks(pg)).includes("Máire Nolan"), "a mark", 20000);
  const url = pg.url();
  await pg.locator("#lk-back").click();
  await pg.locator("#lk-q").waitFor();
  await pg.locator("#itab-details").click();
  await pg.locator(".scriobh-prose .mention", { hasText: "Máire Nolan" }).first().click({ modifiers: ["ControlOrMeta"] });
  await waitFor(async () => (await pg.locator("#lk-h").count()) > 0 && (await pg.locator("#lk-h").innerText()) === "Máire Nolan", "the underlined name to open its entry in the tab", 6000);
  eq(pg.url(), url, "the page did not navigate");
  ok(await pg.locator("#itab-compendium[aria-selected=true]").count(), "the Compendium tab is shown");
  await J("PUT", `/api/v1/docs/${docs0}/location`, { entity: "", base_revision: (await J("GET", `/api/v1/docs/${docs0}`)).item.revision });
});

await kit.test("9. permissions follow the Compendium: the reason for a reader, not shared with a stranger (no names), no Compendium, and Make one", async () => {
  kit.allowed.push(/status of 4\d\d/);
  const rd = await kit.signedIn({ viewport: { width: 1280, height: 800 } }, "reader@dev.scriobh");
  // Rory is an instance reader, so he can only ever be a reader of the book and of the Compendium (the Go tests cover an editor of the
  // book who reads the Compendium). He is on the book and not on its Compendium first.
  const m1 = await api(main, "POST", `/api/v1/projects/${book.id}/members`, { email: "reader@dev.scriobh", role: "reader" });
  ok(m1.status < 300, "Rory on the book as a reader: " + m1.status + JSON.stringify(m1.json));
  const p = rd.page;
  await p.goto(chapter());
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "scripts", 10000);
  await p.locator("#insp-open").click();
  await p.locator("#itab-compendium").click();
  await p.locator("#lk-closed").waitFor();
  const t = await p.locator("#lk-main").innerText();
  ok(/not shared with you/.test(t), "the state: " + t);
  for (const word of ["Máire", "Salt Quay", "Salt Road Bible", "Tadhg", "Captain Vell", "30"]) ok(!t.includes(word), `the closed panel names ${word}`);
  const direct = await rd.ctx.request.fetch(`${base()}/ui/projects/${book.id}/lookup?entity=${maire.id}`, { headers: { "X-Scriobh-Fetch": "1" } });
  const nope = await rd.ctx.request.fetch(`${base()}/ui/projects/${book.id}/lookup?entity=ent_00000000000000000000000000`, { headers: { "X-Scriobh-Fetch": "1" } });
  eq(direct.status() + ":" + (await direct.text()), nope.status() + ":" + (await nope.text()), "an entry of a Compendium the caller is not on answers as a missing one");
  // a reader of the Compendium, and of the book: the fields are text, with the reasons, and there is no form at all
  ok((await api(main, "POST", `/api/v1/compendiums/${bible.comp.id}/members`, { email: "reader@dev.scriobh", role: "reader" })).status < 300, "Rory joins the Compendium as a reader");
  await p.reload();
  await p.waitForSelector(".scriobh-prose");
  await waitFor(() => p.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "scripts", 10000);
  await p.locator("#itab-compendium").click();
  await p.locator("#lk-q").waitFor();
  await openEntry(p, "Máire");
  const ro = await p.locator("#lk-entry").innerText();
  ok(/only its editors can change it/.test(ro), "the reason for the Compendium: " + ro.slice(0, 400));
  ok(/Only editors of this book can set its version/.test(ro), "the reason for the book's version");
  eq(await p.locator("#lk-form").count(), 0, "a reader has no form");
  eq(await p.locator("#lk-entry #es-body, #lk-entry #es-summary, #lk-entry #lk-addname").count(), 0, "no input for a reader");
  ok(/Ferry fare/.test(ro), "the fields are read as text");
  await shot(p, "sidebar-reader-1280-light");
  // a book with no Compendium: the invitation, and Make one for its owner
  const bare = (await J("POST", "/api/v1/projects", { title: "Bare book", kind: "novel" })).item;
  const bdoc = (await J("GET", `/api/v1/projects/${bare.id}/docs`)).order[0];
  await pg.goto(`${base()}/p/${bare.slug}/d/${bdoc}`);
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "scripts", 10000);
  await pg.locator("#itab-compendium").click();
  await pg.locator("#lk-none").waitFor();
  ok(/no Compendium yet/.test(await pg.locator("#lk-none").innerText()), "the state");
  await pg.getByRole("button", { name: "Make one for this book" }).click();
  await pg.locator("#lk-q").waitFor();
  const w2 = (await J("GET", `/api/v1/projects/${bare.id}/compendium`)).item;
  ok(w2.primary && w2.has_primary, "the book has a primary Compendium now");
  await rd.ctx.close();
});

await kit.test("10. the tab stays open from chapter to chapter, restores the last entry, and nothing in the editor moves", async () => {
  await openDoc(pg);
  await openTab(pg);
  await openEntry(pg, "Salt Quay");
  await caretAt(pg, "Paragraph 1.", 4);
  await pg.keyboard.press("Alt+PageDown");
  await pg.waitForURL(new RegExp(`/d/${second}`));
  await pg.waitForSelector(".scriobh-prose");
  const box0 = await pg.evaluate(() => { const r = document.querySelector("#paper").getBoundingClientRect(); return [r.x, r.width].map(Math.round).join(","); });
  ok(await pg.locator("#itab-compendium[aria-selected=true]").count(), "the panel is drawn open on the Compendium tab");
  await pg.locator("#lk-entry").waitFor({ timeout: 10000 });
  eq(await pg.locator("#lk-h").innerText(), "Salt Quay", "the last entry is restored");
  const box1 = await pg.evaluate(() => { const r = document.querySelector("#paper").getBoundingClientRect(); return [r.x, r.width].map(Math.round).join(","); });
  eq(box1, box0, "the editor's box did not move when the first fragment arrived");
});

await kit.test("11. at 390 px the tab is a sheet: keyboard only, 44 px targets, 16 px search, Esc closes it and returns to the editor", async () => {
  await pg.goto("about:blank"); // the chapter is open for writing in one tab at a time
  const c = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  await c.addCookies(await main.cookies());
  const p = await c.newPage();
  kit.watch(p, "phone");
  await openDoc(p);
  await p.locator("#insp-open").click();
  // the tab strip: Compendium is reachable by the arrow keys from the first tab
  await p.locator("#itab-details").focus();
  for (let i = 0; i < 12 && !(await p.evaluate(() => document.activeElement.id === "itab-compendium")); i++) await p.keyboard.press("ArrowRight");
  eq(await p.evaluate(() => document.activeElement.id), "itab-compendium", "the arrow keys reach the Compendium tab");
  await p.locator("#lk-q").waitFor();
  const sz = await p.evaluate(() => { const q = document.querySelector("#lk-q"), r = q.getBoundingClientRect(); return { h: r.height, f: parseFloat(getComputedStyle(q).fontSize) }; });
  ok(sz.h >= 43.9 && sz.f >= 16, "the search is 44 px high and 16 px: " + JSON.stringify(sz));
  await p.keyboard.press("Tab"); // the kind filter, then the rows
  await p.locator("#lk-q").focus();
  await p.keyboard.type("Máire");
  await p.locator(".lk-pick").first().waitFor();
  await p.keyboard.press("Enter");
  await p.locator("#lk-entry").waitFor();
  const small = await p.evaluate(() => [...document.querySelectorAll("#lk-entry button, #lk-entry summary, #lk-entry a")].filter((b) => b.offsetParent && b.getBoundingClientRect().height < 28).map((b) => (b.textContent || b.id).trim().slice(0, 20)));
  ok(small.length === 0, "targets under 28 px: " + small.join(", "));
  await noSideways(p, "the sheet");
  // every control is reached with Tab, in order, from the heading to the description
  await p.locator("#lk-h").focus();
  const seen = [];
  for (let i = 0; i < 90 && !seen.includes("ef-role"); i++) { await p.keyboard.press("Tab"); seen.push(await p.evaluate(() => document.activeElement.id || document.activeElement.textContent.trim().slice(0, 12))); }
  ok(["lk-aka-in", "es-summary", "ef-role", "es-body"].every((id) => seen.includes(id)), "Tab reaches the names, the summary, the fields and the description: " + seen.join(" > "));
  await shot(p, "sidebar-entry-390-light");
  // the sheet scrolls inside; Esc closes it and the editor has the focus
  await p.locator("#es-summary").focus();
  await p.keyboard.press("Escape");
  await waitFor(() => inEditor(p), "Esc to return to the editor", 3000);
  eq(await p.locator("#inspector.is-open").count(), 0, "the sheet is closed");
  await c.close();
});

await kit.test("12. density: the sheet and the lists, against the numbers measured on the old pages", async () => {
  const c = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  await c.addCookies(await main.cookies());
  const p = await c.newPage();
  const cu = (path) => `${base()}/c/${bible.comp.slug}${path}`;
  await p.goto(cu("/e/" + maire.id));
  const h = await p.evaluate(() => document.documentElement.scrollHeight);
  console.log(`      sheet height at 1280: ${h} (before ${BEFORE.sheetHeight1280}, limit ${Math.floor(BEFORE.sheetHeight1280 * 0.6)})`);
  ok(h <= BEFORE.sheetHeight1280 * 0.6, `the sheet is ${h} px tall, more than 60% of ${BEFORE.sheetHeight1280}`);
  const labels = await p.evaluate(() => [...document.querySelectorAll("#fields .en-label")].map((l) => ({ t: l.textContent.trim(), v: !!l.offsetParent })));
  eq(labels.length, 11, "all eleven fields are on the sheet: " + labels.map((l) => l.t));
  ok(labels.every((l) => l.v), "every label is visible without opening anything");
  const filled = Object.values((await getE(maire.id)).fields).filter(Boolean).length;
  eq(await p.locator("#fields h2").innerText(), `Fields (${filled} of 11 filled)`, "the count of filled fields");
  eq(await p.locator("details.en-more").count(), 0, "seven empty fields are all shown (no more than eight)");
  eq(await p.locator(".en-index a").count() >= 4, true, "the section index");
  await p.locator('.en-index a:has-text("Description")').focus();
  await p.keyboard.press("Enter");
  ok(await p.evaluate(() => location.hash === "#description"), "an index link reaches its section by keyboard");
  await shot(p, "entity-sheet-1280-light");
  // a kind with more than eight empty fields: the first six, and "Show N more fields"
  const heavy = (await J("POST", "/api/v1/compendiums", { name: "Heavy bible" })).item;
  for (let i = 0; i < 6; i++) await J("PUT", `/api/v1/compendiums/${heavy.id}/entity-fields`, { kind: "character", key: `extra${i}`, label: `Extra ${i}`, type: "text" });
  const he = (await J("POST", `/api/v1/compendiums/${heavy.id}/entities`, { kind: "character", name: "Heavy Hanna", fields: { role: "Cook", age: "50", extra0: "x" } })).item;
  await p.goto(`${base()}/c/${heavy.slug}/e/${he.id}`);
  const defs = await p.locator("#fields .en-label").count();
  const empty = defs - 3;
  ok(empty > 8, "the fixture has more than eight empty fields: " + empty);
  eq(await p.locator("details.en-more > summary").innerText(), `Show ${empty - 6} more fields`, "the count of the rest");
  eq(await p.locator("details.en-more .en-row").count(), empty - 6, "and that many rows behind it");
  eq(await p.locator("#fields > .en-grid > .en-row").count(), defs - (empty - 6), "the first six empty fields are shown in their places");
  await p.locator("details.en-more > summary").click();
  await p.locator("#ef-extra5").fill("opened");
  await p.getByRole("button", { name: "Save", exact: true }).click();
  await p.getByText("Saved.", { exact: true }).waitFor();
  eq((await getE(he.id)).fields.extra5, "opened", "a field filled from behind the row is saved");
  // the lists
  await p.goto(cu("/entities"));
  const rows = await p.evaluate(() => { const h = innerHeight; return [...document.querySelectorAll(".ent-row")].filter((r) => { const b = r.getBoundingClientRect(); return b.top >= 0 && b.bottom <= h; }).length; });
  console.log(`      list rows at 1280 by 800: ${rows} (before ${BEFORE.rows1280})`);
  ok(rows >= 15, `${rows} rows fit at 1280 by 800, want at least 15`);
  await shot(p, "entities-list-1280-light");
  const ph = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  await ph.addCookies(await main.cookies());
  const q = await ph.newPage();
  await q.goto(cu("/entities"));
  const fit = () => { const h = innerHeight; return [...document.querySelectorAll(".ent-row")].filter((r) => { const b = r.getBoundingClientRect(); return b.top >= 0 && b.bottom <= h; }).length; };
  // The way back to writing (a line of its own above the page, 44 px on a touch screen and 8 px of margin) takes a row of this list at 390 px. The list's
  // own density is what is measured, with that line set aside, and what the line costs is measured too: one row, no more.
  const withBack = await q.evaluate(fit);
  const backH = await q.evaluate(() => { const b = document.querySelector(".comp-back"); if (!b) return 0; const h = b.getBoundingClientRect().height + parseFloat(getComputedStyle(b).marginBottom); b.hidden = true; return h; });
  const prow = await q.evaluate(fit);
  console.log(`      list rows at 390 by 844: ${prow} (before ${BEFORE.rows390}), ${withBack} with the way back (${Math.round(backH)} px)`);
  ok(prow >= 8, `${prow} rows fit at 390 by 844, want at least 8`);
  ok(withBack >= prow - 1 && backH <= 56, `the way back costs at most one row: ${withBack} rows with it, ${prow} without, ${backH} px`);
  await noSideways(q, "the list at 390");
  await q.goto(cu("/e/" + maire.id));
  await noSideways(q, "the sheet at 390");
  await ph.close();
  await c.close();
});

await kit.test("13. the session ends while the panel is open: no sign-in page in the panel, the words stay, and Save now works once signed in again", async () => {
  kit.allowed.push(/status of 40[13]/, /Failed to load resource/, /htmx:|Response Status Error/); // htmx logs a refused request
  await pg.goto("about:blank");
  const c = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  const p = await c.newPage();
  kit.watch(p, "signed out");
  const { signIn } = await import("./harness.mjs");
  await signIn(p, base());
  await openDoc(p);
  await openTab(p);
  await openEntry(p, "Tadhg");
  await c.clearCookies(); // the session is gone
  await p.locator("#es-summary").fill("Words typed after the session ended");
  await waitFor(async () => /signed out/i.test(await status(p)), "the signed-out message", 8000);
  eq(await p.locator("#lk-entry").count(), 1, "the panel still shows the entry (no sign-in page)");
  eq(await p.locator("#es-summary").inputValue(), "Words typed after the session ended", "the typed words stay");
  eq(await p.locator("#lk-main input[name=password], #lk-main form[action*='/auth/']").count(), 0, "no sign-in form in the panel");
  // signed in again, in another tab of the same browser
  const q = await c.newPage();
  await signIn(q, base());
  await q.close();
  await p.getByRole("button", { name: "Save now" }).click();
  await waitFor(async () => (await getE(bible.tadhg.id)).summary === "Words typed after the session ended", "Save now to save with a fresh token", 10000);
  await c.close();
});

await kit.test("14. the network cut: the panel says so, keeps the typed words, and the editor keeps working", async () => {
  kit.allowed.push(/ERR_INTERNET_DISCONNECTED|Failed to load resource|net::/, /htmx:|Response Status Error/);
  await openDoc(pg, second); // (the first chapter may still be held by the tab a moment ago)
  await openTab(pg);
  await openEntry(pg, "Tadhg");
  await pg.context().setOffline(true);
  await fillIn(pg, "#es-summary", "Typed while offline");
  await waitFor(async () => /Could not reach/.test(await status(pg)), "the offline message", 8000);
  eq(await pg.locator("#es-summary").inputValue(), "Typed while offline", "the words stay in the field");
  await caretAt(pg, "Short", 2);
  await pg.keyboard.type("ok");
  ok((await snap(pg)).md.includes("Shokort"), "the editor still takes text: " + (await snap(pg)).md);
  await pg.context().setOffline(false);
  await pg.getByRole("button", { name: "Save now" }).click();
  await waitFor(async () => (await getE(bible.tadhg.id)).summary === "Typed while offline", "the words to be saved once the network is back", 10000);
});

await kit.test("15. one save at a time: typing during a save is not a conflict with itself", async () => {
  await openDoc(pg, second);
  await openTab(pg);
  await openEntry(pg, "Tadhg");
  const seen = [];
  pg.on("response", (r) => { if (r.url().includes("/lookup/save")) seen.push(r.status()); });
  for (let i = 0; i < 8; i++) { await fillIn(pg, "#es-summary", "Typing along " + i); await pg.locator("#es-summary").blur(); await pg.locator("#es-summary").focus(); }
  await waitFor(async () => (await getE(bible.tadhg.id)).summary === "Typing along 7", "the last words to be stored", 10000);
  ok(seen.length >= 1 && seen.every((s) => s === 200), "every save was answered 200: " + seen.join(","));
});

await kit.test("16. the page goes while a sidebar edit is pending: the last save goes with it", async () => {
  await openDoc(pg, second);
  await openTab(pg);
  await openEntry(pg, "Tadhg");
  await fillIn(pg, "#es-summary", "Left in a hurry"); // the page is gone before the 800 ms are up
  await pg.goto("about:blank");
  await waitFor(async () => (await getE(bible.tadhg.id)).summary === "Left in a hurry", "the keepalive save", 8000);
});

await kit.test("17. Tab through every control of the panel, on a tall and on a short window: the page's scroll never moves, and the last box takes words and saves", async () => {
  for (const [w, h] of [[1280, 800], [1280, 560], [1100, 640]]) {
    await pg.setViewportSize({ width: w, height: h });
    await openDoc(pg);
    await scrollToText(pg, "Paragraph 20.");
    await caretAt(pg, "Paragraph 20.", 4);
    await pg.keyboard.press("Alt+Shift+KeyC");
    await pg.locator("#lk-q").waitFor();
    await fillIn(pg, "#lk-q", "Máire");
    await waitFor(async () => (await pg.locator(".lk-pick").count()) === 1, "the search");
    await pg.locator(".lk-pick").click();
    await pg.locator("#lk-entry").waitFor();
    await waitFor(() => inEditor(pg).then((x) => !x), "the focus to be in the panel");
    await pg.evaluate(() => document.querySelector("#lk-h").focus({ preventScroll: true }));
    await sleep(300);
    const before = await snap(pg);
    await watch(pg);
    const stops = [];
    for (let i = 0; i < 120; i++) {
      await pg.keyboard.press("Tab");
      const at = await pg.evaluate(() => { const a = document.activeElement; return { in: !!(a && a.closest("#inspector")), id: a ? a.id || a.textContent.trim().slice(0, 14) : "", y: Math.round(scrollY) }; });
      if (!at.in) break;
      stops.push(at.id);
      if (at.y !== before.y) throw new Error(`${w}x${h}: Tab to ${at.id} moved the page from ${before.y} to ${at.y} (stops: ${stops.join(" > ")})`);
    }
    ok(stops.includes("es-body") && stops.length > 15, `${w}x${h}: Tab reached the description and ${stops.length} stops in all: ${stops.join(" > ")}`);
    // the last box takes words and they save, with the page where it was
    // the last Tab left the panel for whatever comes next in the page, which may be far away and takes the page with it: put the page back
    await pg.evaluate((y) => window.scrollTo(0, y), before.y);
    // back to the description by keyboard, a word typed, Tab to Save now and Enter: as a person does it
    for (let i = 0; i < 60 && !(await pg.evaluate(() => document.activeElement && document.activeElement.id === "es-body")); i++) await pg.keyboard.press("Shift+Tab");
    eq(await pg.evaluate(() => document.activeElement.id), "es-body", `${w}x${h}: Shift+Tab came back to the description`);
    await pg.keyboard.press("ControlOrMeta+End");
    await pg.keyboard.insertText(" ");
    for (let i = 0; i < 40 && (await pg.evaluate(() => document.activeElement.textContent.trim())) !== "Save now"; i++) await pg.keyboard.press("Tab");
    eq(await pg.evaluate(() => document.activeElement.textContent.trim()), "Save now", `${w}x${h}: Tab reaches Save now`);
    await pg.keyboard.press("Enter");
    await savedStatus(pg);
    const after = await snap(pg);
    eq(after.y, before.y, `${w}x${h}: the page's scroll after the last save`);
    eq(after.md, before.md, `${w}x${h}: the text`);
    eq(await pg.evaluate(() => window.__box.length), 0, `${w}x${h}: the page's box never moved`);
  }
  await pg.setViewportSize({ width: 1280, height: 800 });
});

await kit.test("18. a request of the panel that fails never takes the page away: a lost session, an entry that is gone, a server that is not there", async () => {
  kit.allowed.push(/status of 40[14]/, /Failed to load resource/, /htmx:|Response Status Error|ERR_FAILED/);
  // a lost session, found when the tab comes back: the entry stays, with the words, and the page does not move
  await pg.goto("about:blank");
  const c = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  const p = await c.newPage();
  kit.watch(p, "stale tab");
  const { signIn } = await import("./harness.mjs");
  await signIn(p, base());
  await openDoc(p, second);
  await openTab(p);
  await openEntry(p, "Tadhg");
  const url = p.url();
  await c.clearCookies();
  await p.evaluate(() => { document.activeElement.blur(); Object.defineProperty(document, "hidden", { configurable: true, get: () => false }); document.dispatchEvent(new Event("visibilitychange")); });
  await waitFor(async () => /signed out/i.test(await status(p)), "the signed-out message", 6000);
  eq(p.url(), url, "the page did not go to the sign-in page");
  eq(await p.locator("#lk-entry").count(), 1, "the entry is still there");
  await c.close();
  // an entry remembered for this tab that is gone: the list, not a panel stuck on Loading
  await openDoc(pg, second);
  // the panel, drawn open, is restoring the entry it had; its answer writes the tab's memory again, so the memory is changed after that, not before
  await waitFor(async () => !/^Loading/.test(await pg.locator("#lk-main").innerText({ timeout: 500 }).catch(() => "")), "the panel's own restore to land", 8000);
  const key = await pg.evaluate(() => `scriobh:lk:${document.querySelector("#write").dataset.user}:${window.scriobh.write.cfg.project}`);
  await pg.evaluate(([k]) => sessionStorage.setItem(k, JSON.stringify({ e: "ent_00000000000000000000000001", s: "record", q: "" })), [key]);
  await pg.reload();
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "scripts", 10000);
  if (!(await pg.locator("#inspector.is-open").count())) await pg.locator("#insp-open").click();
  await pg.locator("#itab-compendium").click();
  await pg.locator("#lk-q").waitFor({ timeout: 8000 });
  // the first open with the server not answering: a message in place of Loading
  await pg.evaluate((k) => sessionStorage.removeItem(k), key);
  await pg.route("**/ui/projects/*/lookup*", (r) => r.abort());
  await pg.goto(chapter(second)); // the inspector is drawn open on the tab, and the first fragment cannot arrive
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(async () => /Could not reach/.test(await pg.locator("#lk-main").innerText()), "a message in place of Loading", 8000);
  await pg.unroute("**/ui/projects/*/lookup*");
});

await kit.test("19. the sections of an entry stay as they were left, from one entry to the next in this tab", async () => {
  await openDoc(pg, second);
  await openTab(pg);
  await openEntry(pg, "Tadhg");
  const rel = pg.locator("#lk-sec-rel > summary");
  await rel.click();
  ok(await pg.locator("#lk-sec-rel").evaluate((d) => d.open), "the relationships open");
  await pg.locator("#lk-back").click();
  await openEntry(pg, "Salt Quay");
  ok(await pg.locator("#lk-sec-rel").evaluate((d) => d.open), "and stay open on the next entry");
  await pg.locator("#lk-sec-rel > summary").click();
  await pg.locator("#lk-back").click();
  await openEntry(pg, "Tadhg");
  ok(!(await pg.locator("#lk-sec-rel").evaluate((d) => d.open)), "closed again, and remembered closed");
});

// ---- review round: the words that must survive, what is said where the writer is looking, what must not take the focus -------------------------
const inView = (page, sel) => page.evaluate((q) => {
  const e = document.querySelector(q), i = document.querySelector("#inspector");
  if (!e || !e.offsetParent) return "missing";
  const r = e.getBoundingClientRect(), b = i.getBoundingClientRect();
  return r.top >= b.top - 1 && r.top < Math.min(b.bottom, innerHeight) && r.bottom > b.top ? "in view" : `out of view: ${Math.round(r.top)}..${Math.round(r.bottom)} in ${Math.round(b.top)}..${Math.round(b.bottom)}`;
}, sel);

await kit.test("20. the last words go with the page while a save is still on its way, and nothing of anyone else's is written over", async () => {
  await openDoc(pg, second);
  await openTab(pg);
  await openEntry(pg, "Tadhg");
  const first = (await getE(bible.tadhg.id)).summary;
  let n = 0, release = () => {};
  await pg.route("**/ui/projects/*/lookup/save", async (route) => {
    if (n++ > 0) return route.continue();
    const resp = await route.fetch(); // the server has it; the page is not told yet
    await new Promise((r) => { release = r; setTimeout(r, 4000); });
    try { await route.fulfill({ response: resp }); } catch { /* the page is gone */ }
  });
  await fillIn(pg, "#es-summary", "First words, in flight");
  await waitFor(async () => (await getE(bible.tadhg.id)).summary === "First words, in flight", "the first save to reach the server", 6000);
  await fillIn(pg, "#es-summary", "First words, in flight, and the last ones");
  await pg.goto("about:blank"); // the page goes before the first answer, and before the debounce of the second save
  release();
  await waitFor(async () => (await getE(bible.tadhg.id)).summary === "First words, in flight, and the last ones", "the last words to be saved by the keepalive request", 8000);
  await pg.unroute("**/ui/projects/*/lookup/save");
  void first;
});

await kit.test("21. words too many for a request that outlives the page wait in this browser and come back, to be saved", async () => {
  await openDoc(pg, second);
  await openTab(pg);
  await openEntry(pg, "Tadhg");
  const big = "Long words. ".repeat(6000);
  await pg.locator("#es-body").focus();
  await pg.keyboard.press("ControlOrMeta+a");
  await pg.keyboard.insertText(big);
  await pg.goto("about:blank");
  await sleep(500);
  ok((await getE(bible.tadhg.id)).body !== big.replace(/\s+$/, ""), "nothing was sent (it was too long for that)");
  await openDoc(pg, second);
  await pg.locator("#lk-entry").waitFor();
  await waitFor(async () => (await getE(bible.tadhg.id)).body === big.replace(/\s+$/, "") || (await getE(bible.tadhg.id)).body === big, "the words to come back and be saved", 10000);
});

await kit.test("22. a failed open takes nothing: a refresh that follows leaves the focus in the editor", async () => {
  kit.allowed.push(/ERR_FAILED|Failed to load resource|htmx:|Response Status Error/);
  await openDoc(pg, second);
  await openTab(pg);
  await openEntry(pg, "Tadhg");
  await pg.locator(".scriobh-prose").click();
  await pg.route("**/ui/projects/*/lookup?entity=*", (r) => r.abort());
  await pg.evaluate((id) => { window.scriobh.lookupWant = { entity: id }; window.scriobh.inspector.open("compendium", false); }, bible.quay.id);
  await waitFor(async () => /Could not reach/.test(await status(pg)), "the failed open to be said", 6000);
  await pg.unroute("**/ui/projects/*/lookup?entity=*");
  ok(await inEditor(pg), "the focus is still in the editor");
  await pg.evaluate(() => { document.activeElement.blur(); });
  await pg.locator(".scriobh-prose").click();
  await pg.evaluate(() => { Object.defineProperty(document, "hidden", { configurable: true, get: () => false }); document.dispatchEvent(new Event("visibilitychange")); });
  await sleep(800);
  ok(await inEditor(pg), "the refresh that follows does not take the focus into the panel");
});

await kit.test("23. Esc and the chord go back to the Source view, at the caret and with the page where it was", async () => {
  await openDoc(pg);
  await scrollToText(pg, "Paragraph 20.");
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press("ControlOrMeta+e"); // the Markdown source
  await pg.locator(".scriobh-source:not([hidden])").waitFor();
  await pg.locator(".scriobh-source").focus({ preventScroll: true });
  await sleep(200);
  const y = await pg.evaluate(() => Math.round(scrollY));
  await pg.keyboard.press("Alt+Shift+KeyC");
  await pg.locator("#lk-q").waitFor();
  await waitFor(async () => !(await pg.evaluate(() => document.activeElement.classList.contains("scriobh-source"))), "the focus to be in the panel");
  await pg.keyboard.press("Escape");
  await waitFor(() => pg.evaluate(() => document.activeElement && document.activeElement.classList.contains("scriobh-source")), "Esc to return to the Source view", 3000);
  eq(await pg.evaluate(() => Math.round(scrollY)), y, "the page did not move");
  await pg.keyboard.press("Alt+Shift+KeyC");
  await pg.locator("#lk-q").waitFor();
  await pg.keyboard.press("Alt+Shift+KeyC");
  await waitFor(() => pg.evaluate(() => document.activeElement && document.activeElement.classList.contains("scriobh-source")), "the chord to return to the Source view", 3000);
  await pg.keyboard.press("ControlOrMeta+e");
  await pg.locator(".scriobh-prose").waitFor();
});

await kit.test("24. a name that another entry holds, chosen from the selection's list, is said in words and throws nothing", async () => {
  kit.allowed.push(/status of 422/);
  await openDoc(pg);
  await scrollToText(pg, "Captain Vell stood");
  await selectText(pg, "Captain Vell");
  await pg.keyboard.press("Alt+Shift+KeyC");
  await pg.locator("#lk-first").waitFor();
  await pg.locator("#lk-first").click();
  await pg.getByText("Choose the entry that the words").waitFor();
  await fillIn(pg, "#lk-q", "Máire"); // the rows are targets while the words are being given to an entry
  await pg.locator(".lk-row", { hasText: "Máire Nolan" }).locator(".lk-target button").waitFor();
  await pg.locator(".lk-row", { hasText: "Máire Nolan" }).locator(".lk-target button").click();
  await waitFor(async () => /already used by Captain Vell/.test(await status(pg)), "the collision to be said by the list", 6000);
  ok(await pg.getByRole("button", { name: /Open Captain Vell/ }).count(), "with a button that opens the entry that holds the word");
  eq((await getE(maire.id)).aliases.filter((a) => a.alias === "Captain Vell").length, 0, "nothing was added");
});

await kit.test("25. what goes wrong while typing is said where the writer is looking: a conflict, no network, a lost session", async () => {
  kit.allowed.push(/ERR_INTERNET_DISCONNECTED|Failed to load resource|net::|htmx:|Response Status Error|status of 422/);
  await openDoc(pg);
  await openTab(pg);
  await openEntry(pg, "Máire");
  await pg.locator("#es-body").focus();
  await pg.keyboard.press("End");
  // a conflict, while typing in the description
  await J("PATCH", `/api/v1/entities/${maire.id}`, { body: ((await getE(maire.id)).body) + "\n\nSomeone else's line." });
  await pg.keyboard.insertText(" My line.");
  await waitFor(async () => (await pg.locator(".lk-conflict").count()) > 0, "the conflict box", 8000);
  eq(await inView(pg, ".lk-conflict"), "in view", "the conflict box is in view while typing in the description");
  await pg.getByRole("button", { name: "Use theirs" }).click();
  await waitFor(async () => (await pg.locator(".lk-conflict").count()) === 0, "the form to be replaced", 6000);
  // no network
  await pg.locator("#es-body").focus();
  await pg.context().setOffline(true);
  await pg.keyboard.press("End");
  await pg.keyboard.insertText(" More.");
  await waitFor(async () => /Could not reach/.test(await status(pg)), "the offline message", 8000);
  eq(await inView(pg, "#lk-status"), "in view", "the offline message is in view");
  await pg.context().setOffline(false);
  await pg.getByRole("button", { name: "Save now" }).click();
  await savedStatus(pg);
});

await kit.test("26. a token that went stale is renewed for every change of the panel, and a person who may not is told so, not that the page expired", async () => {
  kit.allowed.push(/status of 40[13]/, /Failed to load resource|htmx:|Response Status Error/);
  await pg.goto("about:blank");
  const c = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  const p = await c.newPage();
  kit.watch(p, "stale token");
  const { signIn } = await import("./harness.mjs");
  await signIn(p, base());
  await openDoc(p, second);
  await openTab(p);
  await openEntry(p, "Tadhg");
  await c.clearCookies();
  const q = await c.newPage();
  await signIn(q, base()); // another session of the same person: this page's token is no longer one
  await q.close();
  await fillIn(p, "#lk-aka-in", "Old Tadhg Two");
  await p.keyboard.press("Enter");
  await waitFor(async () => /expired/.test(await p.locator("#lk-aka-status").innerText()), "the expired message, by the names", 6000);
  await p.keyboard.press("Enter");
  await p.locator(".en-chip", { hasText: "Old Tadhg Two" }).waitFor();
  ok((await getE(bible.tadhg.id)).aliases.some((a) => a.alias === "Old Tadhg Two"), "the second try went through");
  await p.getByRole("button", { name: "Remove the name Old Tadhg Two" }).click();
  await waitFor(async () => !(await getE(bible.tadhg.id)).aliases.some((a) => a.alias === "Old Tadhg Two"), "the name to be removed", 6000);
  // a person who may not: the reason is the server's, and the words are not "expired"
  const rc = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  const rp = await rc.newPage();
  await signIn(rp, base(), "reader@dev.scriobh");
  const csrf = await (await rc.request.get(`${base()}/ui/csrf`, { headers: { "X-Scriobh-Fetch": "1" } })).json();
  const r = await rc.request.post(`${base()}/ui/projects/${book.id}/lookup/alias`, { form: { entity_id: bible.tadhg.id, alias: "Nope" }, headers: { "X-CSRF-Token": csrf.token, "X-Scriobh-Fetch": "1" } });
  ok(r.status() === 403 || r.status() === 404, "a reader of the Compendium is refused: " + r.status());
  ok(!/"reason":"csrf"/.test(await r.text()), "and it is not said to be a stale token");
  await rc.close();
  await c.close();
});

await kit.test("27. an input method's Esc and Enter are not the panel's, an answer that comes late does not replace what is typed, and a conflict does not hang a navigation", async () => {
  kit.allowed.push(/status of 422/, /htmx:|Response Status Error|Failed to load resource/);
  await openDoc(pg, second);
  await openTab(pg);
  await openEntry(pg, "Tadhg");
  // Esc during composition stays in the panel
  await pg.locator("#es-summary").focus();
  await pg.evaluate(() => document.activeElement.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", isComposing: true, bubbles: true, cancelable: true })));
  ok(!(await inEditor(pg)), "Esc inside an input method does not leave the panel");
  // a late answer: another entry is asked for, the writer types before it comes
  await pg.route("**/ui/projects/*/lookup?entity=*", async (route) => { await sleep(600); await route.continue(); });
  await pg.evaluate((id) => { window.scriobh.lookupWant = { entity: id }; window.scriobh.inspector.open("compendium", false); }, bible.quay.id);
  await fillIn(pg, "#es-summary", "Typed while the other entry was coming");
  await sleep(1500);
  await pg.unroute("**/ui/projects/*/lookup?entity=*");
  eq(await pg.locator("#lk-entry").getAttribute("data-entity"), bible.tadhg.id, "the late answer did not replace the entry");
  eq(await pg.locator("#es-summary").inputValue(), "Typed while the other entry was coming", "and what was typed is there");
  await savedStatus(pg);
  // a conflict does not hang Back to results
  await J("PATCH", `/api/v1/entities/${bible.tadhg.id}`, { summary: "Elsewhere again" });
  await fillIn(pg, "#es-summary", "Mine, against it");
  await waitFor(async () => (await pg.locator(".lk-conflict").count()) > 0, "the conflict box", 8000);
  await pg.locator("#lk-back").click();
  await waitFor(async () => /Keep mine or Use theirs/.test(await pg.locator(".lk-conflict").innerText()), "the box to say what is needed", 4000);
  eq(await pg.locator("#lk-entry").count(), 1, "the entry stays");
  await pg.getByRole("button", { name: "Use theirs" }).click();
});

await kit.test("28. a name an entry is given for this book is underlined in the text once the index has caught up", async () => {
  const cur = (await J("GET", `/api/v1/docs/${second}`)).item;
  await J("PATCH", `/api/v1/docs/${second}`, { base_revision: cur.revision, content: cur.content.replace(/\n*$/, "") + "\n\nOld Mairín waved from the slip.\n" });
  await openDoc(pg, second);
  await openTab(pg);
  await openEntry(pg, "Máire");
  await pg.getByRole("button", { name: "This book", exact: true }).click();
  await pg.locator("#ov-name").waitFor();
  ok(!(await marks(pg)).includes("Old Mairín"), "not a name yet");
  await fillIn(pg, "#ov-name", "Old Mairín");
  await waitFor(async () => /Saved for this book/.test(await status(pg)), "the book's version to be saved", 6000);
  await waitFor(async () => (await marks(pg)).includes("Old Mairín"), "the book's name to be underlined", 25000);
  await fillIn(pg, "#ov-name", "");
  await waitFor(async () => /Saved for this book/.test(await status(pg)), "the name to be cleared", 6000);
});

await kit.test("29. the inspector's tabs wrap onto rows, none cut off, reached by arrow keys, at 1280 and at 390", async () => {
  for (const [w, h] of [[1280, 800], [390, 844]]) {
    await pg.setViewportSize({ width: w, height: h });
    await openDoc(pg);
    if (w < 1150 || !(await pg.locator("#inspector.is-open").count())) await pg.locator("#insp-open").click();
    await pg.locator("#itab-details").click();
    const rows = await pg.evaluate(() => new Set([...document.querySelectorAll(".insp-tab")].map((t) => t.offsetTop)).size);
    ok(rows >= 1, `${w}: the tabs wrap onto ${rows} row(s)`);
    const more = await pg.evaluate(() => { const s = document.querySelector(".insp-tabs"); return s.scrollWidth > s.clientWidth; });
    ok(!more, `${w}: the tabs fit the strip, so nothing scrolls sideways`);
    ok(await pg.locator("#insp-tr").isHidden(), `${w}: no scroll button is needed`);
    const y = await pg.evaluate(() => Math.round(scrollY));
    await pg.locator("#itab-details").focus();
    const seen = [];
    for (let i = 0; i < 12; i++) {
      await pg.keyboard.press("ArrowRight");
      const at = await pg.evaluate(() => { const a = document.activeElement, s = document.querySelector(".insp-tabs"), r = a.getBoundingClientRect(), b = s.getBoundingClientRect(); return { id: a.id, sel: a.getAttribute("aria-selected"), ti: a.tabIndex, vis: r.left >= b.left - 1 && r.right <= b.right + 1, y: Math.round(scrollY) }; });
      seen.push(at.id);
      ok(at.vis, `${w}: ${at.id} is inside the strip once it has the focus`);
      eq(at.sel, "true", `${w}: ${at.id} is the selected tab`);
      eq(at.ti, 0, `${w}: and the one stop of the strip`);
      eq(at.y, y, `${w}: the page did not move`);
    }
    ok(new Set(seen).size >= 7, `${w}: the arrow keys went through every tab: ${seen.join(" > ")}`);
    ok(!(await pg.locator("#insp-tl").isVisible()) && !(await pg.locator("#insp-tr").isVisible()), `${w}: no end button, because the tabs fit`);
    await pg.locator("#itab-details").click();
    await shot(pg, `inspector-tabs-${w}`);
  }
  await pg.setViewportSize({ width: 1280, height: 800 });
  await openDoc(pg); // a fresh page: a resize leaves the editor's own measured heights on its boxes (set through the CSSOM, which the policy allows)
});

await kit.test("30. a field saved in the sidebar redraws the Fields count: the heading says what the save left", async () => {
  // a page of its own: the panel is left as the cases before it left it (an entry, a route held), which is not what this case is about
  const c = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  await c.addCookies(await main.cookies());
  const p = await c.newPage();
  kit.watch(p, "fields");
  await openDoc(p);
  await openTab(p, "tab");
  await fillIn(p, "#lk-q", "Máire");
  await p.locator(".lk-pick", { hasText: "Máire Nolan" }).first().click();
  await p.locator("#lk-entry").waitFor();
  const empty = await p.evaluate(() => { const i = [...document.querySelectorAll("#lk-form [id^='ef-']")].find((x) => x.value === "" && !["ef-fare", "ef-born", "ef-side"].includes(x.id)); return i ? i.id : ""; }); // a text field: the number, the date and the choice have their own values
  ok(empty !== "", "an empty field to fill");
  const count = () => p.locator("#lk-form #fields h2").innerText();
  const before = await count();
  await fillIn(p, "#" + empty, "filled in the sidebar");
  await savedStatus(p);
  const filled = Object.values((await getE(maire.id)).fields).filter(Boolean).length;
  eq(await count(), `Fields (${filled} of 11 filled)`, "the count after the save, as the store has it");
  ok((await count()) !== before, "and it is not the count from before the save: " + before);
  if (shots) await p.locator("#lk-form #fields h2").screenshot({ path: `${shots}/u3-fields-count-1280-light.png` }); // a look, not a check
});

// U5: the header's Compendium button opens the sidebar on its Compendium tab, and the sidebar's own toggle opens it on the tab it was last on
// (remembered in this browser). A fresh browser context, so nothing the cases before it left is in the way.
await kit.test("31. the Compendium button opens the sidebar on its tab, and the sidebar toggle reopens it on the last tab", async () => {
  const c = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  await c.addCookies(await main.cookies());
  const p = await c.newPage();
  kit.watch(p, "u5-tabs");
  await openDoc(p);
  const cb = p.locator("#comp-open");
  ok((await cb.count()) === 1, "the writing header has a Compendium button");
  eq(await cb.getAttribute("aria-label"), "Compendium", "the button's name is the word of its tab");
  await cb.click();
  ok(await p.locator("#inspector.is-open").count(), "the button opens the sidebar");
  eq(await p.locator("#itab-compendium").getAttribute("aria-selected"), "true", "on the Compendium tab");
  await p.locator("#itab-versions").click();
  await p.locator("#insp-close").click();
  await p.locator("#insp-open").click();
  eq(await p.locator("#itab-versions").getAttribute("aria-selected"), "true", "the sidebar toggle reopens on the tab it was last on");
  await p.reload();
  await p.locator("#insp-close").click();
  await p.locator("#insp-open").click();
  eq(await p.locator("#itab-versions").getAttribute("aria-selected"), "true", "and after a reload, in this browser");
  await c.close();
});

// ---- U9: the focus after Create, Escape back to the text, and the first-name offer (the plan's U9.2 and U9.4) ---------------------
await kit.test("9. an entry made from the panel leaves the focus on its empty summary, Escape goes back to the text, and the first-name offer adds the name once", async () => {
  await openDoc(pg);
  await openTab(pg);
  await pg.getByRole("button", { name: "New entry", exact: true }).first().click();
  await fillIn(pg, "#lkn-name", "Lark Alder");
  await pg.locator('input[name="kind"][value="character"]').check({ force: true });
  await pg.getByRole("button", { name: "Create" }).click();
  await pg.locator("#lk-entry").waitFor();
  await waitFor(async () => (await pg.evaluate(() => document.activeElement && document.activeElement.id)) === "es-summary", "the focus on the empty summary of the new entry", 6000);
  await pg.keyboard.press("Escape");
  ok(await inEditor(pg), "Escape returns the focus to the text");
  await openTab(pg);
  await openEntry(pg, "Máire");
  await pg.locator(".en-offer button").click();
  await waitFor(async () => (await getE(maire.id)).aliases.some((a) => a.alias === "Máire"), "the offered name to be stored", 6000);
  await pg.locator(".en-chip", { hasText: "Máire" }).first().waitFor();
  eq(await pg.locator(".en-offer").count(), 0, "the offer is gone once the name is there");
});

await kit.finish("e2e-sidebar");
