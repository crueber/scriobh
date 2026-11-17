#!/usr/bin/env node
// End-to-end test of the names from the bible that the writing view underlines: adding a character and seeing
// every mention takes well under a minute; marks stand on the right words (not in private notes, not on a longer
// word), never change the text, follow edits, are never drawn anew while typing, show a card, can be turned off,
// and the ambiguous ones read differently by line style.
//
//   node scripts/e2e-mentions.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-mentions");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page } = await kit.signedIn();
kit.main = page;

let book, doc, cmp, other;
const marks = () => page.locator(".scriobh-prose .mention");
const texts = () => marks().allInnerTexts();
const md = () => page.evaluate(() => window.scriobh.write.editor.getMarkdown());
const ready = async () => {
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.mentions)), "the highlighter", 10000);
};
const mk = async (id, kind, name, extra = {}) => (await api(main, "POST", `/api/v1/compendiums/${id}/entities`, { kind, name, ...extra })).json.item;
const PROSE = "Marta rowed out past Marta's pier. Madam Quill, who said Martaville was dull, kept %%Marta is hiding something%% quiet.\n\nThe harbourmaster Brenna waved. Brenna waved again.\n\nAda said hello. Ada was kind; Adam was not.\n";

await kit.test("setup: a chapter that names people before the bible has them", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: "Mention Book", kind: "novel" })).json.item;
  doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "Harbour", content: PROSE });
  cmp = (await api(main, "POST", "/api/v1/compendiums", { name: "Mention Bible", project_id: book.id })).json.item;
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready();
  await sleep(2500);
  eq(await marks().count(), 0, "nothing is underlined while the bible is empty");
});

await kit.test("adding a character, then opening the chapter: every mention is underlined within a minute", async () => {
  const t0 = Date.now();
  await mk(cmp.id, "character", "Marta Quill", { summary: "The ferry rower.", aliases: ["Marta"] });
  await mk(cmp.id, "character", "Brenna Hale", { summary: "The harbourmaster." });
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready();
  await waitFor(async () => (await marks().count()) >= 2, "the marks", 30000);
  const took = Date.now() - t0;
  ok(took < 60000, `under a minute: ${took} ms`);
  const t = await texts();
  eq(t.filter((x) => x === "Marta").length, 2, "both Martas in the prose (not Martaville, not the private note): " + JSON.stringify(t));
  eq(t.filter((x) => x === "Brenna").length, 0, "Brenna is not a name or alias of Brenna Hale");
  console.log(`      add, open, see every mention: ${took} ms`);
});

await kit.test("marks never stand on the private note, on a longer word, or on a name's possessive stem wrongly", async () => {
  const bad = await page.evaluate(() => [...document.querySelectorAll(".scriobh-prose .mention")].filter((m) => m.closest(".private-note")).length);
  eq(bad, 0, "no mark inside a private note");
  ok(!(await texts()).some((x) => /Martaville/.test(x)), "no mark on Martaville");
});

await kit.test("the marks change nothing in the text, and the saved Markdown is the same", async () => {
  const before = await md();
  eq(before, PROSE.replace(/\n+$/, "\n"), "the editor's Markdown is the stored text");
  const s = await marks().first().evaluate((e) => getComputedStyle(e).textDecorationStyle + " " + getComputedStyle(e).textDecorationLine);
  ok(/solid/.test(s) && /underline/.test(s), "a solid underline: " + s);
});

await kit.test("hovering a mark shows the card with the book's summary; Escape closes it", async () => {
  await marks().first().hover();
  await page.locator("#men-card").waitFor({ state: "visible", timeout: 5000 });
  const c = await page.locator("#men-card").innerText();
  ok(/Marta Quill/.test(c) && /The ferry rower/.test(c) && /Character/.test(c), "name, summary and kind: " + c);
  ok(/\/p\/.+\/e\//.test(await page.locator("#men-card a").getAttribute("href")), "it links to the sheet");
  await page.keyboard.press("Escape");
  await page.locator("#men-card").waitFor({ state: "hidden" });
});

await kit.test("typing draws nothing anew; the marks beside the edit stay and the edited name loses its own", async () => {
  await page.locator(".scriobh-prose").click();
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.atStart(v.state.doc))); v.focus(); });
  const n = await marks().count();
  await page.keyboard.type("A new first line.\n");
  eq(await marks().count(), n, "the marks moved with the text, none vanished");
  const first = await marks().first().evaluate((e) => e.textContent);
  eq(first, "Marta");
  // Typing into a name takes that mark away at once rather than leaving it on the wrong letters.
  await page.keyboard.press("ArrowRight");
  await page.keyboard.type("x");
  await waitFor(async () => (await marks().count()) === n - 1, "the edited name to lose its mark");
});

await kit.test("after the save, the marks are asked for again and match the text", async () => {
  await waitFor(async () => (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item.content.includes("A new first line."), "the save", 10000);
  await waitFor(async () => (await texts()).filter((x) => x === "Marta").length === 1, "the new answer", 15000);
});

await kit.test("a name that could mean two entities is wavy and the card says so", async () => {
  other = (await api(main, "POST", "/api/v1/compendiums", { name: "Other Bible" })).json.item;
  await mk(other.id, "character", "Ada");
  await mk(cmp.id, "character", "Ada Quill", { aliases: ["Ada"] });
  await api(main, "POST", `/api/v1/projects/${book.id}/compendium/refs`, { compendium_id: other.id });
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready();
  await waitFor(async () => (await texts()).includes("Ada"), "Ada to be marked", 30000);
  const amb = page.locator(".scriobh-prose .mention-amb").first();
  eq(await amb.evaluate((e) => getComputedStyle(e).textDecorationStyle), "wavy", "a wavy underline");
  await amb.hover();
  await page.locator("#men-card").waitFor({ state: "visible", timeout: 5000 });
  ok(/could also mean/.test(await page.locator("#men-card").innerText()), "the card names the other");
  ok(!(await texts()).some((x) => x === "Adam"), "Adam is not Ada");
  await page.keyboard.press("Escape");
});

await kit.test("the palette turns the marks off and on, and the choice is remembered", async () => {
  await page.keyboard.press(`${MOD}+K`);
  await page.locator("#palette-input").fill("underline");
  await page.getByRole("option", { name: /underlined names from the bible/ }).click();
  await waitFor(async () => (await marks().count()) === 0, "the marks to go");
  await page.reload();
  await ready();
  await sleep(2500);
  eq(await marks().count(), 0, "still off after a reload");
  await page.keyboard.press(`${MOD}+K`);
  await page.locator("#palette-input").fill("underline");
  await page.getByRole("option", { name: /underlined names from the bible/ }).click();
  await waitFor(async () => (await marks().count()) > 0, "the marks to return", 15000);
});

await kit.test("the caret at rest inside a name shows the card; Ctrl or Command with a click goes to the sheet", async () => {
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready();
  await page.waitForSelector(".scriobh-prose .mention", { timeout: 20000 });
  // Put the caret inside the first mark through the view, then leave it alone.
  await page.evaluate(() => {
    const v = window.scriobh.write.editor.view, m = document.querySelector(".scriobh-prose .mention");
    const pos = v.posAtDOM(m, 1);
    v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.near(v.state.doc.resolve(pos))));
    v.focus();
  });
  await page.locator("#men-card").waitFor({ state: "visible", timeout: 5000 });
  ok(/Marta Quill/.test(await page.locator("#men-card").innerText()), "the card shows for the name under the caret");
  await page.keyboard.press("Escape");
  await page.locator("#men-card").waitFor({ state: "hidden" });
  await marks().first().click({ modifiers: [process.platform === "darwin" ? "Meta" : "Control"] });
  await page.waitForURL(/\/p\/.+\/e\/ent_/, { timeout: 10000 });
});

await kit.test("the whole path through the screens: add an entity, see where it appears, open the chapter and see every mention, in under a minute", async () => {
  const mkdoc = await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Selma arrives", kind: "chapter", content: "Selma Reyes walked in. Selma Reyes sat down, and Selma Reyes said nothing.\n" });
  const chapter = mkdoc.json.item.id;
  const t0 = Date.now();
  await page.goto(`${base()}/c/${cmp.slug}/entities`);
  await page.locator("#ea-name").fill("Selma Reyes");
  await page.keyboard.press("Enter");
  await page.getByText("The entity is added.").waitFor();
  const entity = page.url().split("/e/")[1].split(/[?#]/)[0];
  // The Compendium's own sheet says which books name it.
  await page.goto(`${base()}/c/${cmp.slug}/e/${entity}`);
  await page.locator("#where").waitFor();
  await waitFor(async () => /Mention Book/.test(await page.locator("#where").innerText()), "the book to be listed on the Compendium's sheet, filled in without a reload", 20000);
  ok(/3 mentions in 1 document/.test(await page.locator("#where").innerText().then((t) => t.replace(/\s+/g, " "))), "counts: " + (await page.locator("#where").innerText()));
  // The book's sheet lists the document with its snippet and marks the first appearance.
  await page.goto(`${base()}/p/${book.slug}/e/${entity}`);
  await waitFor(async () => (await page.locator("#where .where-row").count()) === 1, "the document to be listed", 20000);
  const w = (await page.locator("#where").innerText()).replace(/\s+/g, " ");
  ok(/Selma arrives/.test(w) && /First appearance/.test(w) && /named 3 times/.test(w) && /Selma Reyes walked in/.test(w), "the row: " + w);
  await page.locator("#where .where-row a").first().click();
  await page.waitForSelector(".scriobh-prose");
  await waitFor(async () => (await page.locator(".scriobh-prose .mention", { hasText: "Selma Reyes" }).count()) === 3, "all three mentions underlined", 20000);
  const took = Date.now() - t0;
  ok(took < 60000, `under a minute: ${took} ms`);
  console.log(`      add through the screens, see where it appears, see every mention in the chapter: ${took} ms`);
  ok(chapter, "chapter");
});

await kit.test("on a phone a tap shows the card, and nothing overflows at 390 px", async () => {
  const { page: ph } = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  await ph.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ph.waitForSelector(".scriobh-prose .mention", { timeout: 30000 });
  await ph.locator(".scriobh-prose .mention").first().tap();
  await ph.locator("#men-card").waitFor({ state: "visible", timeout: 5000 });
  ok(await ph.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "no horizontal scroll");
  const r = await ph.locator("#men-card").boundingBox();
  ok(r.x >= 0 && r.x + r.width <= 390, "the card sits inside the screen: " + JSON.stringify(r));
});

await kit.finish("e2e-mentions");
