#!/usr/bin/env node
// End-to-end test of alternatives, ghosts and the stash in the writing view: the Phase 3.5 exit criteria. Three versions of a
// word, a sentence and a paragraph are kept and cycled without leaving the page; the original is always one action away; the
// export uses the chosen wordings and importing it brings the alternatives back; ghosted text is revived, not counted, and
// exported by default; a stashed paragraph is dragged back; and it all works by keyboard, at 390 px, and with reduced motion.
//
//   node scripts/e2e-alts.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-alts");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page } = await kit.signedIn();
kit.main = page;

let book, doc;
const P1 = "The harbour was quiet that morning.";
const P2 = "Mara counted the boats that had not come in, and said nothing to anyone.";
const S3A = "The tide turned twice before noon.";
const S3B = "It left the quay slick with weed.";
const PROSE = `${P1}\n\n${P2}\n\n${S3A} ${S3B}\n`;
const text = () => page.evaluate(() => { const d = window.scriobh.write.editor.view.state.doc; return d.textBetween(0, d.content.size, "\n").replace(/<!-- scriobh:alts[\s\S]*$/, "").trim(); }); // the chapter, without the comment that ends it
const stored = async (id = doc) => (await api(main, "GET", `/api/v1/docs/${id}`)).json.item;
const list = async (id = doc) => (await api(main, "GET", `/api/v1/docs/${id}/alternatives`)).json;
const select = (s, pg = page) => pg.evaluate((s) => {
  const v = window.scriobh.write.editor.view; let hit = null;
  v.state.doc.descendants((n, pos) => { if (hit || !n.isText) return; const i = n.text.indexOf(s); if (i >= 0) hit = [pos + i, pos + i + s.length]; });
  if (!hit) throw new Error("not found: " + s);
  v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); v.focus();
}, s);
const caretIn = (s, off = 1, pg = page) => pg.evaluate(([s, off]) => {
  const v = window.scriobh.write.editor.view; let hit = null;
  v.state.doc.descendants((n, pos) => { if (hit || !n.isText) return; const i = n.text.indexOf(s); if (i >= 0) hit = pos + i + off; });
  v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.near(v.state.doc.resolve(hit)))); v.focus();
}, [s, off]);
const settled = (pg = page) => waitFor(async () => !(await pg.evaluate(() => window.scriobh.write.unsaved())), "the page to be saved", 10000);
const ready = async (pg = page, mark = true) => {
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.alts)), "alts.js", 15000);
  if (mark) await pg.waitForSelector(".scriobh-prose .scriobh-alt", { timeout: 20000 });
};
// Make an alternative through the add form (as the keyboard would), with its first other wording.
const addAlt = async (selection, scope, wording, pg = page) => {
  await select(selection, pg);
  await pg.keyboard.press("Alt+Shift+KeyA");
  const pop = pg.locator("#alt-pop");
  await pop.waitFor();
  await pop.locator(`input[value="${scope}"]`).check();
  await pop.locator("textarea").fill(wording);
  await pg.keyboard.press("Enter");
  await pop.waitFor({ state: "detached", timeout: 10000 });
};
// More wordings through the panel's own form.
const panel = async (pg = page) => { await pg.evaluate(() => window.scriobh.inspector.open("alts", false)); await pg.locator("#ipanel-alts .ap-card").first().waitFor(); };
const addWording = async (aid, wording, pg = page) => {
  await panel(pg);
  const card = pg.locator(`#ipanel-alts .ap-card[data-a="${aid}"]`);
  await card.locator(".ap-add input").fill(wording);
  await card.locator(".ap-add input").press("Enter");
  await waitFor(async () => (await card.locator(".ap-var .ap-text").allInnerTexts()).includes(wording), "the wording to be listed");
};

await kit.test("setup: a chapter of three paragraphs", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: "Alt Book", kind: "novel" })).json.item;
  doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "Harbour", content: PROSE });
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready(page, false);
});

await kit.test("a word, a sentence and a paragraph each keep three wordings, drawn with wavy underlines and end dots", async () => {
  await addAlt("quiet", "word", "still");
  await addAlt(S3A, "sentence", "The tide came and went twice by noon.");
  await addAlt(P2, "paragraph", "Mara counted the missing boats and told no one.");
  const l = await list();
  eq(l.active, 3, "three alternatives");
  const byScope = Object.fromEntries(l.items.map((a) => [a.scope, a]));
  await addWording(byScope.word.id, "calm");
  await addWording(byScope.sentence.id, "Twice before noon the tide turned.");
  await addWording(byScope.paragraph.id, "Nobody heard Mara say a word about the boats.");
  await waitFor(async () => (await page.locator(".scriobh-prose .scriobh-alt").count()) >= 3, "the marks");
  eq(await page.locator(".scriobh-prose .alt-dot").count(), 6, "two end dots for each");
  const after = await list();
  for (const a of after.items) eq(a.variants.length, 3, `${a.scope} keeps three wordings`);
  ok((await text()).includes(P1) && (await text()).includes(P2) && (await text()).includes(S3A), "the text still holds the originals");
});

await kit.test("cycling in place with the keyboard, in order, for every scope, and the original is one key away", async () => {
  const cycleTo = async (anchor, key, expected) => {
    await caretIn(anchor, 2);
    await page.keyboard.press(key);
    await waitFor(async () => (await text()).includes(expected), `“${expected}”`);
  };
  await cycleTo("quiet", "Alt+Shift+KeyN", "still that morning");
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("calm that morning"), "the third word");
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("quiet that morning"), "it wraps to the original");
  await page.keyboard.press("Alt+Shift+KeyP");
  await waitFor(async () => (await text()).includes("calm that morning"), "back one");
  await page.keyboard.press("Alt+Shift+KeyO");
  await waitFor(async () => (await text()).includes("quiet that morning"), "the original in one action");
  await waitFor(async () => /Wording 1 of 3, the original/.test(await page.locator("#live").innerText()), "the live region to announce the original", 5000); // the announcement follows the text by a moment
  await cycleTo("The tide", "Alt+Shift+KeyN", "The tide came and went twice by noon.");
  await cycleTo("The tide", "Alt+Shift+KeyN", "Twice before noon the tide turned.");
  await page.keyboard.press("Alt+Shift+KeyO");
  await waitFor(async () => (await text()).includes(S3A), "the sentence original");
  await cycleTo("Mara counted", "Alt+Shift+KeyN", "Mara counted the missing boats");
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("Nobody heard Mara"), "the third paragraph");
  await page.keyboard.press("Alt+Shift+KeyO");
  await waitFor(async () => (await text()).includes(P2), "the paragraph original");
  await settled();
  eq((await stored()).content.includes(P2) && (await stored()).content.includes("quiet that morning"), true, "the stored text is the chosen wordings (the originals)");
  eq(await page.evaluate(() => window.scriobh.write.unsaved()), false, "nothing is waiting to be saved");
});

await kit.test("rapid presses are applied in order and the server and editor agree afterwards", async () => {
  await caretIn("quiet", 2);
  for (let i = 0; i < 4; i++) await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await list()).items.find((a) => a.scope === "word").chosen_position === 2, "four steps through three wordings land on the second", 10000);
  await waitFor(async () => (await text()).includes("still that morning"), "the editor shows it");
  await page.keyboard.press("Alt+Shift+KeyO");
  await waitFor(async () => (await text()).includes("quiet that morning") && (await list()).items.find((a) => a.scope === "word").chosen_position === 1, "back to the original");
  await settled();
  const s = await stored();
  eq(await page.evaluate(() => window.scriobh.write.serverMd), s.content, "what the editor believes is stored is what is stored");
});

await kit.test("the panel: use, edit, reorder, remove with undo, show original, in a list that reads for a screen reader", async () => {
  const w = (await list()).items.find((a) => a.scope === "word");
  await panel();
  const card = () => page.locator(`#ipanel-alts .ap-card[data-a="${w.id}"]`);
  await card().getByRole("button", { name: /Use .still./ }).click();
  await waitFor(async () => (await text()).includes("still that morning"), "Use puts the wording in the text");
  await card().getByRole("button", { name: /Edit .calm./ }).click();
  await page.locator("#ipanel-alts textarea").fill("hushed");
  await page.keyboard.press("Enter");
  await waitFor(async () => (await card().locator(".ap-text").allInnerTexts()).includes("hushed"), "the edited wording is listed");
  await card().getByRole("button", { name: /Move .hushed. up/ }).click();
  await waitFor(async () => (await card().locator(".ap-text").allInnerTexts()).join("|") === "quiet|hushed|still", "reordered");
  await card().getByRole("button", { name: /Remove .hushed./ }).click();
  await waitFor(async () => (await card().locator(".ap-text").allInnerTexts()).join("|") === "quiet|still", "removed");
  await page.locator("#ipanel-alts .ap-undo").getByRole("button", { name: "Undo" }).click();
  await waitFor(async () => (await card().locator(".ap-text").allInnerTexts()).join("|") === "quiet|hushed|still", "the undo puts it back where it was");
  eq(await card().locator(".ap-var").filter({ hasText: "Original" }).getByRole("button", { name: /Remove|Edit/ }).count(), 0, "the original has no Remove or Edit");
  await card().getByRole("button", { name: "Show original" }).click();
  await waitFor(async () => (await text()).includes("quiet that morning"), "Show original");
  await settled();
});

await kit.test("a/an follows the wording that is put in place", async () => {
  const cur = await stored();
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, content: cur.content.replace("The harbour was quiet that morning.", "She saw a boat that morning.\n\nThe harbour was quiet that morning.") });
  await page.reload(); await ready();
  await addAlt("boat", "word", "owl");
  await caretIn("boat", 2);
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("She saw an owl that morning"), "“a boat” becomes “an owl”");
  await page.keyboard.press("Alt+Shift+KeyO");
  await waitFor(async () => (await text()).includes("She saw a boat that morning"), "and back to “a boat”");
  await settled();
});

await kit.test("ghosting: dimmed with a strike, not counted, kept in the chapter, revived from the panel, exported by default", async () => {
  const before = (await stored()).word_count;
  await select(S3B);
  await page.keyboard.press("Alt+Shift+KeyG");
  await waitFor(async () => (await page.locator(".scriobh-prose .scriobh-ghost").count()) >= 1, "the ghost is drawn");
  ok(/line-through/.test(await page.locator(".scriobh-prose .scriobh-ghost").first().evaluate((e) => getComputedStyle(e).textDecorationLine)), "struck through, so it is not colour alone");
  await settled();
  const g = await stored();
  eq(g.word_count, before - 7, "the ghosted words are not counted");
  ok(g.content.includes("<scriobh-ghost>It left the quay slick with weed.</scriobh-ghost>"), "the words stay in the chapter");
  ok((await text()).includes(S3B), "and in the editor");
  const exp = await main.request.post(`${base()}/api/v1/projects/${book.id}/export`, { data: {}, headers: { "Content-Type": "application/json" } });
  ok((await exp.text()).includes("<scriobh-ghost>"), "an export keeps ghosts by default");
  const omit = await main.request.post(`${base()}/api/v1/projects/${book.id}/export`, { data: { ghosts: "omit" }, headers: { "Content-Type": "application/json" } });
  ok(!(await omit.text()).includes("slick with weed"), "ghosts: omit leaves the text out");
  await panel();
  await page.locator("#ipanel-alts").getByRole("button", { name: /Revive/ }).first().click();
  await waitFor(async () => (await page.locator(".scriobh-prose .scriobh-ghost").count()) === 0, "the ghost is gone");
  await settled();
  eq((await stored()).word_count, before, "the words count again");
  ok(!(await stored()).content.includes("<scriobh-ghost>"), "the tags are gone");
});

await kit.test("export uses the chosen wordings; importing the file brings the alternatives back", async () => {
  await caretIn("quiet", 2);
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("hushed that morning"), "the second wording (reordered earlier)");
  await settled();
  const exp = await main.request.post(`${base()}/api/v1/projects/${book.id}/export`, { data: { mode: "single" }, headers: { "Content-Type": "application/json" } });
  const md = await exp.text();
  ok(md.includes("hushed that morning"), "the export holds the chosen wording");
  const files = [{ path: "Imported/chapter-one.md", content: md }];
  const pv = (await api(main, "POST", "/api/v1/imports/preview", { files, name: "Imported" })).json;
  const ap = await api(main, "POST", "/api/v1/imports", { files, name: "Imported", token: pv.token });
  ok(ap.status < 300, "import: " + ap.status + " " + JSON.stringify(ap.json).slice(0, 200));
  let imported = null;
  for (const id of (await api(main, "GET", `/api/v1/projects/${ap.json.project_id}/docs`)).json.order) if ((await list(id)).active > 0) imported = id;
  ok(imported, "one of the imported documents carries the alternatives");
  const l2 = await list(imported);
  eq(l2.active, 4, "the alternatives came back");
  eq(l2.items.find((a) => a.scope === "word" && a.variants.some((v) => v.text === "hushed")).variants.length, 3, "with their wordings");
  eq((await stored(imported)).content.includes("hushed that morning"), true, "and the chosen text");
  await page.keyboard.press("Alt+Shift+KeyO");
  await settled();
});

await kit.test("the context menu offers what the selection or the alternative can do, by keyboard", async () => {
  // A one-word alternative, whatever wording it is showing now: its own marked text is what gets selected.
  const word = await page.evaluate(() => {
    const v = window.scriobh.write.editor.view, el = [...document.querySelectorAll(".scriobh-prose .scriobh-alt")].find((e) => e.textContent.trim() && !/\s/.test(e.textContent));
    const a = v.posAtDOM(el, 0), b = a + el.textContent.length;
    v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, a, b))); v.focus();
    return el.textContent;
  });
  // The word is selected on screen (a person sees that before clicking); right-click the middle of it.
  await page.waitForFunction((w) => String(getSelection()) === w && document.activeElement.closest(".scriobh-prose"), word, { timeout: 4000 });
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  const c = await page.evaluate(() => { const r = getSelection().getRangeAt(0).getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; });
  await page.mouse.click(c.x, c.y, { button: "right" });
  const menu = page.locator("#alt-menu");
  await menu.waitFor();
  const items = await menu.getByRole("menuitem").allInnerTexts();
  ok(items.includes("Next wording") && items.includes("Show the original"), "alternative actions: " + items);
  ok(items.some((t) => /Ghost/.test(t)) && items.some((t) => /stash/i.test(t)), "selection actions: " + items);
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Escape");
  await menu.waitFor({ state: "detached" });
  ok(await page.evaluate(() => document.activeElement.closest(".scriobh-prose") !== null), "focus returned to the text");
});

await kit.test("the palette and the toolbar reach the same commands", async () => {
  await select("harbour");
  await page.keyboard.press(`${MOD}+Shift+P`);
  await page.locator("#palette-input").fill("alternative to the selection");
  await page.getByRole("option", { name: /Add an alternative to the selection/ }).click();
  await page.locator("#alt-pop").waitFor();
  await page.keyboard.press("Escape");
  await page.locator("#alt-pop").waitFor({ state: "detached" });
  await select("harbour");
  await page.locator('#fmtbar [data-cmd="revise"]').click();
  await page.locator('#revise-menu [data-cmd="alt"]').click();
  await page.locator("#alt-pop").waitFor();
  await page.keyboard.press("Escape");
  await page.locator("#alt-pop").waitFor({ state: "detached" });
});

await kit.test("a stashed paragraph is sent from the selection, listed, and dragged back only when asked", async () => {
  await select(P2.slice(0, 30));
  await page.keyboard.press("Alt+Shift+KeyS");
  await waitFor(async () => (await api(main, "GET", `/api/v1/projects/${book.id}/stash`)).json.items.length === 1, "the item in the stash", 10000);
  await settled();
  await page.evaluate(() => window.scriobh.inspector.open("stash", false));
  const card = page.locator("#ipanel-stash .ap-card").first();
  await card.waitFor();
  ok(/not part of any export/i.test(await page.locator("#ipanel-stash").innerText()), "the panel says the stash is not exported");
  const stashText = (await api(main, "GET", `/api/v1/projects/${book.id}/stash`)).json.items[0].content;
  const exp = await main.request.post(`${base()}/api/v1/projects/${book.id}/export`, { data: {}, headers: { "Content-Type": "application/json" } });
  ok(stashText.length > 10 && !(await exp.text()).includes("☃stash"), "the item holds the text");
  await card.getByRole("button", { name: /Put .* in the chapter/ }).click();
  await page.locator("#ipanel-stash form.ap-put select[name=where]").selectOption("end");
  const before = await text();
  await page.locator("#ipanel-stash form.ap-put").getByRole("button", { name: "Put it in the chapter" }).click();
  await waitFor(async () => (await text()).length > before.length, "the text arrives", 10000);
  ok((await text()).trim().endsWith(stashText.replace(/[*_`]/g, "").trim().slice(-20)), "at the end of the chapter, as a paragraph of its own");
  eq((await api(main, "GET", `/api/v1/projects/${book.id}/stash`)).json.items.length, 1, "a copy stays in the stash");
  await settled();
});

await kit.finish("e2e-alts");
