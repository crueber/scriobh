#!/usr/bin/env node
// The Undo and Redo buttons of the writing view's format bar (F-1226), in a real Chromium.
//
//   node scripts/e2e-undo.mjs [--only substring] [--shots DIR]
//
// They are the first group of the bar. They do what Mod+Z and Mod+Y do (the editor's own history), follow its depth (disabled
// at the start of a chapter, Redo disabled until something was undone), keep the editor's focus and selection when they are
// pressed, change nothing about layout while typing, are off in the Source view (which keeps its own undo), in a chapter held
// by another tab and in one too long to edit, and are in the palette.
import { mkdirSync } from "node:fs";
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-undo");
kit.deadline(8 * 60 * 1000);
const arg = (n) => (process.argv.includes(n) ? process.argv[process.argv.indexOf(n) + 1] : "");
kit.only = arg("--only");
const shots = arg("--shots");
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep, MOD } = kit;
const mac = process.platform === "darwin";
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const mk = async (title, content) => {
  const made = await api(main, "POST", "/api/v1/projects", { title, kind: "novel" });
  ok(made.json && made.json.item, `project ${title}: ${made.status}`);
  const p = made.json.item, doc = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.order[0];
  const put = await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: 1, title: "Whole", content });
  ok(put.status === 200, `seed ${title}: ${put.status}`);
  return { ...p, doc };
};
const stored = async (id) => (await api(main, "GET", `/api/v1/docs/${id}`)).json.item.content;
const open = async (page, p) => {
  await page.goto(`${base()}/p/${p.slug}/d/${p.doc}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.tools && window.scriobh.alts)), "the page's scripts to load", 10000);
  await sleep(150);
};
const btn = (page, cmd) => page.locator(`#fmtbar [data-cmd="${cmd}"]`);
// {undo, redo}: true when the button is enabled. Read after the bar's own short pause (it follows the editor a moment after a change).
const state = (page) => page.evaluate(() => ({ undo: !document.querySelector('#fmtbar [data-cmd="undo"]').disabled, redo: !document.querySelector('#fmtbar [data-cmd="redo"]').disabled }));
const wantState = async (page, undo, redo, what) => {
  try { await waitFor(async () => { const s = await state(page); return s.undo === undo && s.redo === redo; }, what); } catch (e) {
    throw new Error(`${what}: wanted Undo ${undo ? "on" : "off"} and Redo ${redo ? "on" : "off"}, found ${JSON.stringify(await state(page))}`);
  }
};
const md = (page) => page.evaluate(() => window.scriobh.write.editor.getMarkdown());
const caretEnd = (page) => page.evaluate(() => {
  const ed = window.scriobh.write.editor, at = ed.view.state.doc.content.size - 1;
  ed.alts.select(at, at);
  ed.view.focus();
});
const selectText = (page, needle) => page.evaluate((n) => {
  const ed = window.scriobh.write.editor;
  let at = -1;
  ed.view.state.doc.descendants((node, pos) => { if (at < 0 && node.isText) { const i = node.text.indexOf(n); if (i >= 0) at = pos + i; } });
  if (at < 0) throw new Error("no such text: " + n);
  ed.alts.select(at, at + n.length);
  ed.view.focus();
}, needle);
const selected = (page) => page.evaluate(() => { const v = window.scriobh.write.editor.view, s = v.state.selection; return v.state.doc.textBetween(s.from, s.to, " "); });
const inEditor = (page) => page.evaluate(() => document.activeElement && document.activeElement.classList.contains("scriobh-prose"));
const noSideways = async (page, what) => eq(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, `${what}: no sideways scroll`);
const toastText = (page) => page.locator(".toast").last().innerText().catch(() => "");
const tip = (page, cmd) => btn(page, cmd).getAttribute("title");

const A = "Alpha paragraph.\n\nBeta paragraph.\n";

await kit.test("at the start of a chapter both buttons are off; they are the first group, with words and keys in the tooltips", async () => {
  const p = await mk("Undo start", A);
  await open(pg, p);
  await wantState(pg, false, false, "a chapter just opened");
  const order = await pg.evaluate(() => [...document.querySelectorAll("#fmtbar .tb[data-cmd]")].slice(0, 3).map((b) => b.dataset.cmd));
  eq(order.join(","), "undo,redo,bold", "Undo and Redo come first, before Bold");
  eq(await pg.evaluate(() => document.querySelector('#fmtbar [data-cmd="undo"]').closest(".fmt-group").getAttribute("aria-label")), "Undo and redo", "their group is named");
  eq(await btn(pg, "undo").getAttribute("aria-label"), "Undo", "Undo's name");
  eq(await btn(pg, "redo").getAttribute("aria-label"), "Redo", "Redo's name");
  eq(await pg.evaluate(() => document.querySelectorAll('#fmtbar [data-cmd="undo"] svg use, #fmtbar [data-cmd="redo"] svg use').length), 2, "each has a sprite icon");
  eq(await pg.evaluate(() => [...document.querySelectorAll("#fmtbar .tb[data-cmd]")].filter((b) => b.tabIndex === 0).map((b) => b.dataset.cmd).join(",")), "bold", "the one Tab stop of the strip is still on Bold while Undo is off (Aa is a stop of its own, U11.3)");
  eq(await tip(pg, "undo"), mac ? "Undo (⌘Z)" : "Undo (Ctrl+Z)", "Undo's tooltip says the key of this platform");
  eq(await tip(pg, "redo"), mac ? "Redo (⌘⇧Z / ⌘Y)" : "Redo (Ctrl+Shift+Z / Ctrl+Y)", "Redo's tooltip says both keys of the registry's row, drawn for this platform");
  // The other platform, as the page reads it from navigator.
  const other = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  await other.addCookies(await main.cookies());
  await other.addInitScript((m) => { Object.defineProperty(navigator, "platform", { get: () => (m ? "Win32" : "MacIntel") }); }, mac);
  const op = await other.newPage();
  kit.watch(op, "other platform");
  await open(op, p);
  eq(await tip(op, "undo"), mac ? "Undo (Ctrl+Z)" : "Undo (⌘Z)", "Undo's tooltip on the other platform");
  eq(await tip(op, "redo"), mac ? "Redo (Ctrl+Shift+Z / Ctrl+Y)" : "Redo (⌘⇧Z / ⌘Y)", "Redo's tooltip on the other platform");
  await other.close();
});

await kit.test("type, bold, Undo by the button returns the exact text, Redo brings it back; both follow the history; focus and selection stay", async () => {
  const p = await mk("Undo walk", A);
  await open(pg, p);
  await caretEnd(pg);
  await pg.keyboard.type(" More words.");
  await wantState(pg, true, false, "after typing");
  const typed = await md(pg);
  eq(typed, "Alpha paragraph.\n\nBeta paragraph. More words.\n", "the typed text");
  await sleep(700); // a new history step, not the same typing run
  await selectText(pg, "Beta");
  await btn(pg, "bold").click();
  const bold = await md(pg);
  ok(bold.includes("**Beta** paragraph."), "bold applied: " + bold);
  await wantState(pg, true, false, "after bold");
  // A real click: the editor keeps the focus, and the selection is the one the history gives back.
  await btn(pg, "undo").click();
  eq(await md(pg), typed, "Undo by the button returns the exact text from before the bold");
  eq(await inEditor(pg), true, "the editor still has the focus after a click on Undo");
  eq(await selected(pg), "Beta", "the selection is the one from before the bold");
  await wantState(pg, true, true, "after one Undo");
  await btn(pg, "redo").click();
  eq(await md(pg), bold, "Redo brings the bold back");
  eq(await inEditor(pg), true, "the editor still has the focus after a click on Redo");
  await btn(pg, "undo").click();
  await btn(pg, "undo").click();
  eq(await md(pg), A, "two Undos return the text as it was opened, exactly");
  await wantState(pg, false, true, "at the start of the history again");
  // Typing goes on at the caret the history left, and is a new history that drops the Redo.
  await pg.keyboard.type("Z");
  await wantState(pg, true, false, "typing after undo drops what could be redone");
  // An undo is an ordinary edit: it saves, and the chapter is clean.
  await btn(pg, "undo").click();
  await waitFor(async () => (await stored(p.doc)) === A, "the undone text to be stored");
  await waitFor(() => pg.evaluate(() => !window.scriobh.write.editor.isDirty()), "the editor to be saved");
});

await kit.test("a native undo or redo that asks the page for it (beforeinput historyUndo and historyRedo: the Edit menu of WebKit, a swipe) is the editor's own history, not the browser's", async () => {
  const p = await mk("Undo native", A);
  await open(pg, p);
  await caretEnd(pg);
  await pg.keyboard.type(" X."); // the browser's own undo stack has this
  await sleep(700);
  await selectText(pg, "Beta");
  await btn(pg, "bold").click(); // a step of the editor's own history that the browser's stack never saw
  const bold = await md(pg);
  ok(bold.includes("**Beta**") && bold.includes(" X."), "typed and bold: " + bold);
  // The event a WebKit page gets from the Edit menu's Undo (sent here by hand: Chromium does not send it for execCommand).
  const ask = (type) => pg.evaluate((t) => { const e = new InputEvent("beforeinput", { inputType: t, cancelable: true, bubbles: true }); window.scriobh.write.editor.view.dom.dispatchEvent(e); return e.defaultPrevented; }, type);
  eq(await ask("historyUndo"), true, "the editor answers it itself (the browser's own undo is stopped)");
  eq(await md(pg), "Alpha paragraph.\n\nBeta paragraph. X.\n", "it took back the bold, the editor's last step, and left the typing");
  await wantState(pg, true, true, "after the native undo");
  eq(await ask("historyRedo"), true, "and the redo");
  eq(await md(pg), bold, "it brought the bold back");
  eq(await ask("insertText"), false, "any other input is left alone");
  eq(await inEditor(pg), true, "the editor kept the focus");
});

await kit.test("the keys do what the buttons do, and the buttons follow the keys", async () => {
  const p = await mk("Undo keys", A);
  await open(pg, p);
  await caretEnd(pg);
  await pg.keyboard.type(" Keys.");
  await wantState(pg, true, false, "typed");
  await pg.keyboard.press(`${MOD}+z`);
  await wantState(pg, false, true, "after the key");
  eq(await md(pg), A, "Mod+Z");
  await pg.keyboard.press(`${MOD}+y`);
  await wantState(pg, true, false, "after Mod+Y");
  eq(await md(pg), "Alpha paragraph.\n\nBeta paragraph. Keys.\n", "Mod+Y");
  await pg.keyboard.press(`${MOD}+z`);
  await pg.keyboard.press(`${MOD}+Shift+z`);
  eq(await md(pg), "Alpha paragraph.\n\nBeta paragraph. Keys.\n", "Mod+Shift+Z");
  await wantState(pg, true, false, "after Mod+Shift+Z");
});

await kit.test("the first key typed changes nothing in the layout, and a long run of typing toggles each button once", async () => {
  const p = await mk("Undo layout", A);
  await open(pg, p);
  await caretEnd(pg);
  const rects = () => pg.evaluate(() => {
    const r = (s) => { const b = document.querySelector(s).getBoundingClientRect(); return [b.x, b.y, b.width, b.height].map((v) => Math.round(v * 10) / 10).join(","); };
    return { bar: r("#fmtbar"), undo: r('#fmtbar [data-cmd="undo"]'), redo: r('#fmtbar [data-cmd="redo"]'), bold: r('#fmtbar [data-cmd="bold"]'), prose: r(".scriobh-prose") };
  });
  const before = await rects();
  // Count attribute writes on the two buttons while typing.
  await pg.evaluate(() => {
    window.__writes = 0;
    for (const c of ["undo", "redo"]) new MutationObserver((l) => { window.__writes += l.length; (window.__w = window.__w || []).push(...l.map((x) => x.attributeName + "=" + x.target.getAttribute(x.attributeName))); }).observe(document.querySelector(`#fmtbar [data-cmd="${c}"]`), { attributes: true });
  });
  await pg.keyboard.type("Some words typed in one run, with no pause worth the name.", { delay: 8 });
  await wantState(pg, true, false, "after the run");
  await sleep(200);
  const after = await rects();
  eq(JSON.stringify(after.bar), JSON.stringify(before.bar), "the bar did not move or change size");
  eq(JSON.stringify(after.undo), JSON.stringify(before.undo), "Undo did not move");
  eq(JSON.stringify(after.bold), JSON.stringify(before.bold), "Bold did not move");
  eq(after.prose.split(",")[1], before.prose.split(",")[1], "the text did not move down or up");
  eq(await pg.evaluate(() => window.__writes), 1, "the buttons changed once in all (Undo came on; nothing else was written): " + JSON.stringify(await pg.evaluate(() => window.__w)));
});

await kit.test("a step kept out of the history (the stored alternatives line) does not turn Undo on, and Undo leaves it alone", async () => {
  const p = await mk("Undo quiet", A);
  await open(pg, p);
  await pg.evaluate(() => window.scriobh.write.editor.alts.setComment("<!-- quiet line -->"));
  await sleep(300);
  await wantState(pg, false, false, "after a stored line was written quietly");
  await caretEnd(pg);
  await selectText(pg, "Alpha");
  await pg.keyboard.type("Omega");
  await wantState(pg, true, false, "after typing");
  await btn(pg, "undo").click();
  await wantState(pg, false, true, "after Undo");
  ok((await md(pg)).includes("<!-- quiet line -->"), "the stored line is still there after Undo");
  ok((await md(pg)).startsWith("Alpha paragraph."), "the typed word is gone");
});

await kit.test("a text that arrives from outside starts a new history: both buttons go off", async () => {
  const p = await mk("Undo fresh", A);
  await open(pg, p);
  await caretEnd(pg);
  await pg.keyboard.type(" Typed.");
  await wantState(pg, true, false, "typed");
  await waitFor(async () => (await stored(p.doc)).includes("Typed."), "the save");
  await pg.evaluate(() => window.scriobh.write.editor.setMarkdown("Replaced text.\n", { force: true }));
  await wantState(pg, false, false, "after the text was replaced");
  // And a reload: the history does not survive it (F-071 is a separate row), so both are off, and nothing is broken.
  await pg.reload();
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.tools)), "scripts");
  await sleep(200);
  await wantState(pg, false, false, "after a reload");
});

await kit.test("Source view: both buttons are off and say why; the source keeps its own undo; leaving it is one step in the rich history", async () => {
  const p = await mk("Undo source", A);
  await open(pg, p);
  await caretEnd(pg);
  await pg.keyboard.type(" Rich.");
  await wantState(pg, true, false, "typed in the rich editor");
  const rich = await md(pg);
  await btn(pg, "source").click();
  await pg.locator(".scriobh-source").waitFor({ state: "visible" });
  await wantState(pg, false, false, "in Source");
  eq(await tip(pg, "undo"), "The Markdown source keeps its own undo and redo. Use the keyboard there.", "Undo says why it is off");
  eq(await tip(pg, "redo"), "The Markdown source keeps its own undo and redo. Use the keyboard there.", "Redo says why it is off");
  // A disabled button does nothing when pressed.
  await btn(pg, "undo").click({ force: true, trial: false }).catch(() => {});
  eq(await md(pg), rich, "a press on a disabled Undo changes nothing");
  // The textarea's own undo, by the keyboard.
  const ta = pg.locator(".scriobh-source");
  await ta.focus();
  await pg.keyboard.press(`${MOD}+End`);
  await pg.keyboard.type(" Source one.");
  const typed = await ta.inputValue();
  ok(typed.includes("Source one."), "typed in the source");
  await pg.keyboard.press(`${MOD}+z`);
  ok(!(await ta.inputValue()).includes("Source one."), "the keyboard's undo works on the textarea's own stack");
  await pg.keyboard.press(`${MOD}+y`).catch(() => {});
  await pg.keyboard.press(`${MOD}+Shift+z`);
  // What the page could have done instead (the reason the buttons are off): the textarea's stack cannot be read, and entering Source sets its value, which empties it.
  const probe = await pg.evaluate(() => { const t = document.querySelector(".scriobh-source"); t.focus(); return { enabled: document.queryCommandEnabled("undo") }; });
  console.log(`      info: in Source, queryCommandEnabled("undo") = ${probe.enabled}; the textarea's depth is not readable`);
  // Leave Source: the edits made there are one step in the rich history.
  await ta.focus();
  await pg.keyboard.type(" Two.");
  const withSource = (await ta.inputValue());
  await btn(pg, "source").click();
  await pg.locator(".scriobh-prose").waitFor({ state: "visible" });
  await wantState(pg, true, false, "back in the rich editor, the edits made in Source are one step");
  ok((await md(pg)).includes("Two."), "the source edits are in the rich text");
  eq(await inEditor(pg), true, "the editor has the focus when Source is left");
  eq(await tip(pg, "undo"), mac ? "Undo (⌘Z)" : "Undo (Ctrl+Z)", "the tooltip is back to the key");
  await btn(pg, "undo").click();
  eq(await md(pg), rich, "one Undo takes back everything done in Source, and the typing before it stays");
  await btn(pg, "redo").click();
  ok((await md(pg)).includes("Two."), "Redo brings the Source edits back");
  await waitFor(async () => (await stored(p.doc)).includes("Two."), "the save");
  ok(withSource.includes("Two."), "(the textarea held what the rich editor was given)");
  // Going into Source again: the buttons are off, and the rich history is kept for when it is left.
  await btn(pg, "source").click();
  await pg.locator(".scriobh-source").waitFor({ state: "visible" });
  await wantState(pg, false, false, "in Source again");
  await btn(pg, "source").click();
  await pg.locator(".scriobh-prose").waitFor({ state: "visible" });
  await wantState(pg, true, false, "the rich history is as it was after Source is left with no change");
});

await kit.test("the palette lists Undo and Redo by the buttons' words, runs them, and leaves the focus in the editor", async () => {
  const p = await mk("Undo palette", A);
  await open(pg, p);
  await caretEnd(pg);
  await pg.keyboard.type(" Palette.");
  await wantState(pg, true, false, "typed");
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("undo");
  const row = pg.locator("#palette-list [role=option]").first();
  const t = await row.innerText();
  ok(t.includes("Undo") && t.includes(mac ? "⌘Z" : "Ctrl+Z"), "the palette's row: " + t);
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await md(pg)) === A, "the palette's Undo to take the typing back");
  eq(await inEditor(pg), true, "the focus is in the editor after the palette's Undo");
  await wantState(pg, false, true, "after the palette's Undo");
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("redo");
  ok((await pg.locator("#palette-list [role=option]").first().innerText()).includes("Redo"), "Redo is listed");
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await md(pg)).includes("Palette."), "the palette's Redo");
  eq(await inEditor(pg), true, "the focus is in the editor after the palette's Redo");
  // Where the buttons are off, the palette's commands do nothing and say nothing wrong.
  await btn(pg, "source").click();
  await pg.locator(".scriobh-source").waitFor({ state: "visible" });
  const inSource = await ta_value(pg);
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("undo");
  await pg.keyboard.press("Enter");
  await sleep(200);
  eq(await ta_value(pg), inSource, "the palette's Undo does nothing in Source");
  eq(await toastText(pg), "The Markdown source keeps its own undo and redo. Use the keyboard there.", "and it says why: the button's own words");
  // Nothing to undo in a fresh chapter: the palette says so.
  const q = await mk("Undo palette none", A);
  await open(pg, q);
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("undo");
  await pg.keyboard.press("Enter");
  eq(await toastText(pg), "There is nothing to undo here.", "the palette's Undo with nothing to undo says so");
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("redo");
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await toastText(pg)) === "There is nothing to redo here.", "the palette's Redo to say so");
});
async function ta_value(page) { return page.locator(".scriobh-source").inputValue(); }

await kit.test("an operation holds the editor: both buttons go off at once and come back when it ends; a press during it is refused with the same note as the key", async () => {
  const p = await mk("Undo hold", A);
  await open(pg, p);
  await caretEnd(pg);
  await pg.keyboard.type(" Held.");
  await wantState(pg, true, false, "typed");
  const before = await md(pg);
  await pg.evaluate(() => { const S = window.scriobh; window.__done = S.alts.exclusive(() => new Promise((r) => { window.__release = () => r({ ok: true }); })); });
  await waitFor(() => pg.evaluate(() => window.scriobh.write.editor.view.dom.getAttribute("aria-busy") === "true"), "the hold to begin");
  await wantState(pg, false, false, "during the hold");
  await pg.evaluate(() => window.scriobh.tools.run("undo")); // what a click on the button runs, in the gap before it is shown off
  await waitFor(async () => (await toastText(pg)).startsWith("Hold on"), "the refusal note");
  eq(await md(pg), before, "Undo changed nothing during the hold");
  await pg.evaluate(() => window.__release());
  await pg.evaluate(() => window.__done);
  await wantState(pg, true, false, "after the hold ended, with no key typed and no selection moved");
});

await kit.test("a wording written after Source is left survives an Undo of the Source edit; the buttons agree with the history", async () => {
  const p = await mk("Undo source alt", "One.\n\nTwo words here.\n");
  await open(pg, p);
  await btn(pg, "source").click();
  await pg.locator(".scriobh-source").waitFor({ state: "visible" });
  await pg.locator(".scriobh-source").focus();
  await pg.keyboard.press(`${MOD}+End`);
  await pg.keyboard.type("\nSource added.\n");
  await btn(pg, "source").click();
  await pg.locator(".scriobh-prose").waitFor({ state: "visible" });
  await wantState(pg, true, false, "after Source");
  await pg.evaluate(() => { const ed = window.scriobh.write.editor; let at = -1; ed.view.state.doc.descendants((n, pos) => { if (n.isText && n.text.includes("words")) at = pos + n.text.indexOf("words"); }); ed.alts.replace(at, at + 5, "ALTWORD"); });
  await wantState(pg, true, false, "a quiet wording does not change the buttons");
  await btn(pg, "undo").click();
  const after = await md(pg);
  ok(after.includes("Two ALTWORD here."), "the wording survives the Undo: " + JSON.stringify(after));
  ok(!after.includes("Source added."), "what Source added is gone: " + JSON.stringify(after));
  await wantState(pg, false, true, "after the Undo");
  eq(JSON.stringify(await state(pg)), JSON.stringify(await pg.evaluate(() => { const h = window.scriobh.write.editor.history(); return { undo: h.undo, redo: h.redo }; })), "the buttons say what the history says");
});

await kit.test("another tab holds the chapter: both buttons are off, and work again after a take over", async () => {
  kit.allowed = [/status of 409/];
  const p = await mk("Undo locked", A);
  await open(pg, p);
  await caretEnd(pg);
  await pg.keyboard.type(" Held.");
  await wantState(pg, true, false, "typed in the first tab");
  await waitFor(async () => (await stored(p.doc)).includes("Held."), "the save");
  const b = await main.newPage();
  kit.watch(b, "tab B");
  await open(b, p);
  await waitFor(() => b.locator("#notices").innerText().then((t) => t.includes("Open in another tab")), "tab B to say it is read-only");
  await wantState(b, false, false, "in the tab that cannot write");
  // Pressing a disabled button changes nothing, and the keys do not write either.
  await btn(b, "undo").click({ force: true }).catch(() => {});
  eq(await md(b), await stored(p.doc), "nothing changed in the held tab");
  await b.keyboard.press(`${MOD}+Shift+p`);
  await b.locator("#palette-input").fill("undo");
  await b.keyboard.press("Enter");
  await waitFor(async () => (await toastText(b)).includes("open for writing in another tab"), "the palette to say the chapter is held elsewhere");
  await b.getByRole("button", { name: "Take over" }).click();
  await waitFor(() => b.locator(".scriobh-prose").getAttribute("contenteditable").then((v) => v === "true"), "tab B to be editable after take over");
  await wantState(b, false, false, "after take over, with nothing typed in this tab yet");
  await caretEnd(b);
  await b.keyboard.type(" Mine.");
  await wantState(b, true, false, "typing after take over");
  await b.close();
});

await kit.test("a chapter over 25,000 words opens read-only: both buttons are off", async () => {
  const words = Array.from({ length: 26000 }, (_, i) => "w" + (i % 90)).join(" ");
  const body = words.match(/(\S+ ){1,200}/g).join("\n\n") + "\n";
  const p = await mk("Undo long", body);
  await open(pg, p);
  await sleep(300);
  await wantState(pg, false, false, "a chapter too long to edit");
});

await kit.test("keyboard only at 390 px: the arrows reach Undo, Enter presses it, the caret stays and typing goes on; no sideways scroll", async () => {
  const phone = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  await phone.addCookies(await main.cookies());
  const page = await phone.newPage();
  kit.watch(page, "phone");
  const p = await mk("Undo phone", A);
  await open(page, p);
  await page.locator(".scriobh-prose").focus();
  await caretEnd(page);
  await page.keyboard.type(" Phone.");
  await wantState(page, true, false, "typed on the phone");
  const typed = await md(page);
  for (let i = 0; i < 6 && !(await page.evaluate(() => !!document.activeElement.closest("#fmtbar"))); i++) await page.keyboard.press("Shift+Tab");
  eq(await page.evaluate(() => !!document.activeElement.closest("#fmtbar")), true, "Shift+Tab from the text reaches the format bar");
  // On a phone the strip sits behind Aa (U11.3): Aa is the stop, Enter opens the strip, and the arrows walk it to Bold.
  eq(await page.evaluate(() => document.activeElement.id), "fmt-aa", "on a phone the Tab stop is Aa");
  await page.keyboard.press("Enter");
  eq(await page.evaluate(() => document.getElementById("fmt-aa").getAttribute("aria-expanded")), "true", "Enter on Aa opens the strip");
  await page.keyboard.press("Tab"); // from Aa into the open strip: its one stop is Bold
  eq(await page.evaluate(() => document.activeElement.dataset.cmd), "bold", "the arrows reach Bold in the open strip");
  await page.keyboard.press("ArrowLeft");
  eq(await page.evaluate(() => document.activeElement.dataset.cmd), "undo", "ArrowLeft from Bold reaches Undo (Redo is off, so it is passed)");
  const r = await page.evaluate(() => { const b = document.activeElement.getBoundingClientRect(), bar = document.querySelector("#fmtbar").getBoundingClientRect(); return { l: b.left, r: b.right, bl: bar.left, br: bar.right, w: b.width, h: b.height }; });
  ok(r.l >= r.bl - 1 && r.r <= r.br + 1, `Undo is inside the bar's visible part: ${JSON.stringify(r)}`);
  ok(r.w >= 44 && r.h >= 44, `a touch target of 44 px: ${r.w} x ${r.h}`);
  if (shots) await page.screenshot({ path: `${shots}/bar-390-light.png`, clip: { x: 0, y: 700, width: 390, height: 144 } });
  await page.keyboard.press("Enter");
  eq(await md(page), A, "Enter on Undo takes the typing back");
  eq(await inEditor(page), true, "the focus went back to the editor");
  await wantState(page, false, true, "after Undo");
  await page.keyboard.type("Q");
  ok((await md(page)).includes("paragraph.Q") || (await md(page)).includes("Q"), "typing goes on in the text: " + (await md(page)));
  await wantState(page, true, false, "typing after Undo");
  // Redo by the keyboard on the bar too.
  await page.keyboard.press(`${MOD}+z`);
  await wantState(page, false, true, "Redo is on");
  for (let i = 0; i < 6 && !(await page.evaluate(() => !!document.activeElement.closest("#fmtbar"))); i++) await page.keyboard.press("Shift+Tab");
  await page.keyboard.press("Home"); // the first button that is on: Undo is off now, so Redo
  eq(await page.evaluate(() => document.activeElement.dataset.cmd), "redo", "Home reaches Redo, the first button that is on");
  await page.keyboard.press("Space");
  ok((await md(page)).includes("Q"), "Space on Redo brings the typing back: " + (await md(page)));
  eq(typed.includes("Phone."), true, "(the earlier text)");
  await noSideways(page, "the phone");
  if (shots) {
    await open(page, p);
    await page.locator(".scriobh-prose").focus();
    await caretEnd(page);
    await page.keyboard.type(" Again.");
    await wantState(page, true, false, "typed again");
    await page.screenshot({ path: `${shots}/page-390-light.png` });
  }
  await phone.close();
  if (shots) {
    for (const [scheme, vp, name] of [["light", { width: 1280, height: 800 }, "bar-1280-light"], ["dark", { width: 1280, height: 800 }, "bar-1280-dark"], ["dark", { width: 390, height: 844 }, "bar-390-dark"]]) {
      const c2 = await kit.newContext({ viewport: vp, deviceScaleFactor: 2, colorScheme: scheme });
      await c2.addCookies(await main.cookies());
      const pp = await c2.newPage();
      await open(pp, await mk("Undo shot " + name, A)); // a chapter of its own: the phone's tab may not have given its lease back yet
      await pp.locator(".scriobh-prose").focus();
      await caretEnd(pp);
      await pp.keyboard.type(" Shot.");
      await sleep(700);
      await pp.keyboard.type(" More.");
      await wantState(pp, true, false, "shot, typed");
      await sleep(700);
      await pp.keyboard.press(`${MOD}+z`);
      await wantState(pp, true, true, "shot, both on");
      if (vp.width < 600) await btn(pp, "undo").focus(); else await btn(pp, "redo").hover();
      await sleep(500);
      await pp.screenshot({ path: `${shots}/${name}.png`, clip: vp.width < 600 ? { x: 0, y: 700, width: 390, height: 144 } : { x: 0, y: 0, width: vp.width, height: 260 } });
      await c2.close();
    }
  }
});

await kit.finish("e2e-undo");
