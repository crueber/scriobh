#!/usr/bin/env node
// End-to-end test of the selection bar (UX-FIXES U6): a bar of three buttons over a finished selection in the writing view
// (Add to Compendium, Look up, Comment), which reuses the Alt+Shift+C and Alt+Shift+M paths. It appears after a drag or a
// Shift+arrow, never while typing, never with the caret collapsed, and never on a phone; it takes no focus and moves no layout;
// the same three items are in the right-click menu; the chords still work.
//
//   SELBAR_SHOTS=<dir> node scripts/e2e-selbar.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { join } from "node:path";

const kit = await createKit("e2e-selbar");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const SHOTS = process.env.SELBAR_SHOTS || kit.tmp;
const NAMES = ["Add to Compendium", "Look up", "Comment"];
const { ctx: main, page } = await kit.signedIn();
kit.main = page;

let book, doc;
const P1 = "The harbour was quiet that morning.";
const P2 = "Mara counted the boats that had not come in, and said nothing to anyone.";
const PROSE = `${P1}\n\n${P2}\n`;
const text = (pg = page) => pg.evaluate(() => { const d = window.scriobh.write.editor.view.state.doc; return d.textBetween(0, d.content.size, "\n"); });
const barShown = (pg = page) => pg.evaluate(() => { const b = document.getElementById("selbar"); return !!b && !b.hidden; });
const barNames = (pg = page) => pg.evaluate(() => [...document.querySelectorAll("#selbar button")].map((b) => b.textContent.trim()));
const focusInText = (pg = page) => pg.evaluate(() => { const a = document.activeElement; return !!(a && a.closest && a.closest(".scriobh-prose")); });
const current = (pg = page) => pg.evaluate(() => window.scriobh.inspector.current);
const closeSidebar = (pg = page) => pg.evaluate(() => window.scriobh.inspector.close(false));
// The screen box of a word of the text, found in the DOM (a real mouse drag over it selects it).
const wordBox = (pg, s) => pg.evaluate((s) => {
  const w = document.createTreeWalker(document.querySelector(".scriobh-prose"), NodeFilter.SHOW_TEXT);
  for (let n; (n = w.nextNode());) {
    const i = n.nodeValue.indexOf(s);
    if (i >= 0) { const r = document.createRange(); r.setStart(n, i); r.setEnd(n, i + s.length); const b = r.getBoundingClientRect(); return { x1: b.left, x2: b.right, y: (b.top + b.bottom) / 2 }; }
  }
  throw new Error("not in the text: " + s);
}, s);
const drag = async (s, pg = page) => {
  const b = await wordBox(pg, s);
  await pg.mouse.move(b.x1 + 1, b.y);
  await pg.mouse.down();
  await pg.mouse.move(b.x2 - 1, b.y, { steps: 6 });
  await pg.mouse.up();
};
const shown = (pg = page) => waitFor(() => barShown(pg), "the selection bar to show", 8000);
const hidden = (pg = page) => waitFor(async () => !(await barShown(pg)), "the selection bar to hide", 8000);
const pick = (s, pg = page) => pg.evaluate((s) => {
  const v = window.scriobh.write.editor.view; let hit = null;
  v.state.doc.descendants((n, pos) => { if (hit || !n.isText) return; const i = n.text.indexOf(s); if (i >= 0) hit = [pos + i, pos + i + s.length]; });
  v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); v.focus();
}, s);

await kit.test("setup: a chapter of two paragraphs, the script not yet fetched", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: "Bar Book", kind: "novel" })).json.item;
  const comp = (await api(main, "POST", "/api/v1/compendiums", { name: "Harbour Notes", kind: "fiction" })).json.item;
  await api(main, "PUT", `/api/v1/projects/${book.id}/compendium`, { compendium_id: comp.id }); // the New entry and the search need a Compendium
  doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "Harbour", content: PROSE });
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.inspector)), "the writing view", 15000);
  // Fetched on the first finished selection, never on load (UX-FIXES U6).
  eq(await page.evaluate(() => document.querySelectorAll('script[src*="selbar"]').length), 0, "no selection-bar script on load");
  eq(await barShown(), false, "no bar on load");
});

await kit.test("a drag selection shows the bar with its three buttons, and focus stays in the text", async () => {
  await drag("harbour");
  await shown();
  eq(JSON.stringify(await barNames()), JSON.stringify(NAMES), "the three buttons, in order");
  // The accessible name is the visible text: no aria-label that would say something else.
  eq(await page.evaluate(() => [...document.querySelectorAll("#selbar button")].filter((b) => b.hasAttribute("aria-label")).length), 0, "no aria-label on the buttons");
  ok(await focusInText(), "focus is still in the text");
  await page.screenshot({ path: join(SHOTS, "selbar-1280-light.png"), caret: "initial" });
  await page.emulateMedia({ colorScheme: "dark" });
  await sleep(150);
  await page.screenshot({ path: join(SHOTS, "selbar-1280-dark.png"), caret: "initial" });
  await page.emulateMedia({ colorScheme: "light" });
  await closeSidebar().catch(() => {});
});

await kit.test("typing a letter hides the bar, and the letter types normally with the text intact", async () => {
  await drag("quiet");
  await shown();
  await page.keyboard.press("z");
  await hidden();
  eq(await text(), `The harbour was z that morning.\n${P2}`, "the letter replaced the selected word and nothing else changed");
});

await kit.test("each button does its job: Add to Compendium, Look up (its focus and selection held on press), Comment", async () => {
  await drag("boats");
  await shown();
  await page.getByRole("button", { name: "Add to Compendium", exact: true }).click();
  await waitFor(async () => (await current()) === "compendium", "the Compendium tab");
  await waitFor(() => page.evaluate(() => { const i = document.getElementById("lkn-name"); return !!i && i.value === "boats"; }), "the new entry named by the words", 12000);
  await hidden();
  await closeSidebar();

  await drag("Mara");
  await shown();
  const box = await page.locator("#selbar button", { hasText: "Look up" }).boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  eq(await page.evaluate(() => window.getSelection().toString()), "Mara", "the selection survives the press on the button");
  ok(await focusInText(), "the press leaves focus in the text");
  await page.mouse.up();
  await waitFor(async () => (await current()) === "compendium", "the Compendium tab for Look up");
  await waitFor(() => page.locator(".lk-quote").filter({ hasText: "Mara" }).count().then((n) => n > 0), "the search named by the words", 12000);
  await hidden();
  await closeSidebar();

  await drag("counted");
  await shown();
  await page.getByRole("button", { name: "Comment", exact: true }).click();
  await waitFor(async () => (await current()) === "comments", "the Comments tab");
  await waitFor(() => page.locator("#cw-sel").count().then((n) => n > 0), "the composer for the selected words", 12000);
  ok(await page.locator(".rv-quote").filter({ hasText: "counted" }).count().then((n) => n > 0), "the composer quotes the words");
  await closeSidebar();
});

await kit.test("Escape hides the bar", async () => {
  await drag("anyone");
  await shown();
  await page.keyboard.press("Escape");
  await hidden();
});

await kit.test("no layout shift while the bar shows and hides, and the focus stays in the text", async () => {
  eq(await page.evaluate(() => PerformanceObserver.supportedEntryTypes.includes("layout-shift")), true, "the browser reports layout shifts");
  await page.evaluate(() => { window.__ls = []; new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__ls.push(e.value); }).observe({ type: "layout-shift", buffered: false }); });
  await sleep(200);
  for (const w of ["nothing", "said", "Mara"]) {
    await drag(w);
    await shown();
    ok(await focusInText(), `focus stays in the text when the bar shows over "${w}"`);
    await page.keyboard.press("Escape");
    await hidden();
  }
  await drag("boats");
  await shown();
  await page.keyboard.press("x");
  await hidden();
  await sleep(400);
  eq(await page.evaluate(() => window.__ls.length), 0, "zero layout-shift entries");
});

await kit.test("the right-click menu has the same three items, and they work", async () => {
  await drag("nothing");
  await shown();
  const b = await wordBox(page, "nothing");
  await page.mouse.click(b.x1 + 4, b.y, { button: "right" });
  await page.locator("#alt-menu").waitFor();
  const items = await page.locator("#alt-menu [role=menuitem]").allInnerTexts();
  eq(JSON.stringify(items.slice(0, 3).map((x) => x.trim())), JSON.stringify(NAMES), "the three items lead the menu");
  await page.locator("#alt-menu [role=menuitem]", { hasText: "Look up" }).click();
  await waitFor(async () => (await current()) === "compendium", "the Compendium tab from the menu");
  await waitFor(() => page.locator(".lk-quote").filter({ hasText: "nothing" }).count().then((n) => n > 0), "the search named by the menu's words", 12000);
  await closeSidebar();
});

await kit.test("keyboard only: Shift+arrow shows the bar on release, and Alt+Shift+C and Alt+Shift+M still work", async () => {
  await page.evaluate(() => {
    const v = window.scriobh.write.editor.view; let at = -1;
    v.state.doc.descendants((n, pos) => { if (at < 0 && n.isText && n.text.indexOf("Mara") >= 0) at = pos + n.text.indexOf("Mara"); });
    v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.near(v.state.doc.resolve(at)))); v.focus();
  });
  await page.keyboard.down("Shift");
  for (let i = 0; i < 5; i++) await page.keyboard.press("ArrowRight");
  await page.keyboard.up("Shift");
  await shown();
  eq(await page.evaluate(() => window.getSelection().toString().trim()), "Mara", "the Shift+arrow selection");
  await page.keyboard.press("Alt+Shift+KeyC");
  await waitFor(async () => (await current()) === "compendium", "Alt+Shift+C to open the Compendium");
  await closeSidebar();
  await pick("counted");
  await page.keyboard.press("Alt+Shift+KeyM");
  await waitFor(async () => (await current()) === "comments", "Alt+Shift+M to open the Comments");
  await closeSidebar();
});

await kit.test("a phone (coarse pointer, 390 px, touch): the bar is not offered and its script is not fetched", async () => {
  await page.goto("about:blank"); // the chapter's lock is the first tab's: this one would be read-only, so the first tab lets go
  await sleep(400);
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  const ph = phone.page;
  kit.watch(ph, "phone");
  await ph.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ph.waitForSelector(".scriobh-prose");
  await waitFor(() => ph.evaluate(() => !!(window.scriobh && window.scriobh.write)), "the writing view on a phone", 15000);
  ok(await ph.evaluate(() => matchMedia("(pointer: coarse)").matches), "the emulation is a coarse pointer (the check is not vacuous)");
  await drag("harbour", ph);
  await sleep(800);
  eq(await barShown(ph), false, "no bar on a phone");
  eq(await ph.evaluate(() => document.querySelectorAll('script[src*="selbar"]').length), 0, "its script is not fetched on a phone");
  await ph.screenshot({ path: join(SHOTS, "selbar-390-touch-no-bar.png"), caret: "initial" });
  await phone.ctx.close();
});

await kit.test("390 px with a fine pointer: the bar shows inside the screen, light and dark", async () => {
  const narrow = await kit.signedIn({ viewport: { width: 390, height: 844 } });
  const np = narrow.page;
  kit.watch(np, "narrow");
  await np.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await np.waitForSelector(".scriobh-prose");
  await waitFor(() => np.evaluate(() => !!(window.scriobh && window.scriobh.write)), "the writing view at 390", 15000);
  await drag("harbour", np);
  await shown(np);
  const box = await np.locator("#selbar").boundingBox();
  ok(box.x >= 0 && box.x + box.width <= 390, `the bar fits the 390 px screen (${Math.round(box.x)}..${Math.round(box.x + box.width)})`);
  await np.screenshot({ path: join(SHOTS, "selbar-390-light.png"), caret: "initial" });
  await np.emulateMedia({ colorScheme: "dark" });
  await sleep(150);
  await np.screenshot({ path: join(SHOTS, "selbar-390-dark.png"), caret: "initial" });
  await narrow.ctx.close();
});

await kit.csp(page, "main");
await kit.finish("e2e-selbar");
