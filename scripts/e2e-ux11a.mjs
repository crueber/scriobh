#!/usr/bin/env node
// End-to-end checks for U11.1 (a slimmer header and grouped tools) in the writing view:
// the revision tools sit in one labelled "Revise" menu that works by keyboard (Enter or Space opens it, the arrows move, Escape
// closes it and returns focus to its button) and by mouse (a press on an item keeps the editor's selection); the inspector's
// tab strip fits at 1280 px without clipping a tab; the Compendium button shows its label at 1280 px and up; "Saved" stays visible;
// the header's breadcrumb neither overlaps the page tabs nor clips its names; and the chapter title is readable at 390 px.
// When U11_SHOTS names a folder, the states checked here are saved there as PNG.
//
//   node scripts/e2e-ux11a.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { join } from "node:path";

const kit = await createKit("e2e-ux11a");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor } = kit;
const shots = process.env.U11_SHOTS || "";
const shot = (pg, name) => (shots ? pg.screenshot({ path: join(shots, `${name}.png`) }) : null);
const { ctx: main, page } = await kit.signedIn();
const LONG = "The Long Road Home Through the Northern Mountains: A Novel in Three Parts";
let book, chapter;
const only = kit.only;
kit.only = ""; // the setup always runs, whichever step is named
await kit.test("setup: a long project title, a chapter with words to select, and a Compendium", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: LONG, kind: "novel" })).json.item;
  chapter = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "The Chapter Title That Is Also Rather Long For A Header", kind: "chapter", content: "Opening words here, and more of them.\n\n## In the chapter\n\nMore words, and the end.\n" })).json.item;
  await api(main, "POST", "/api/v1/compendiums", { name: "Bible", project_id: book.id });
});
kit.only = only;

// A writer's page at a width and a colour scheme, the editor ready, the inspector open when asked.
const open = async (w, { dark = false, inspector = false, touch = false } = {}) => {
  const ctx = await kit.newContext({ viewport: { width: w, height: 860 }, isMobile: touch, hasTouch: touch, colorScheme: dark ? "dark" : "light" });
  await ctx.addCookies(await main.cookies());
  const pg = await ctx.newPage();
  kit.watch(pg, `w${w}`);
  await pg.goto(`${kit.base()}/p/${book.slug}/d/${chapter.id}`, { waitUntil: "load" });
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor && window.scriobh.write.editor.view)), "the editor", 15000);
  // revise.js arrives with the page's load event (data-ext); the menu's keys and presses are wired by it.
  await waitFor(() => pg.evaluate(() => performance.getEntriesByType("resource").some((r) => /\/js\/revise\./.test(r.name))), "the Revise script", 8000);
  if (inspector) {
    await waitFor(() => pg.evaluate(() => { const b = document.querySelector("#insp-open"); return !!b && !b.hidden; }), "the inspector button", 8000);
    await pg.evaluate(() => document.querySelector("#insp-open").click());
    await pg.waitForTimeout(300);
  }
  return { ctx, pg };
};
// Selects the words in the chapter's first paragraph, through the editor's own state, and focuses the text.
const select = (pg, a = 1, b = 8) => pg.evaluate(([a, b]) => {
  const v = window.scriobh.write.editor.view;
  v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, a, b)));
  v.focus();
}, [a, b]);
const pmSel = (pg) => pg.evaluate(() => { const s = window.scriobh.write.editor.view.state.selection; return [s.from, s.to]; });
const menuOpen = (pg) => pg.evaluate(() => { try { return !!document.querySelector("#revise-menu:popover-open"); } catch (e) { return false; } });

// ---- the Revise menu -------------------------------------------------------------------------------------------------
await kit.test("Revise: the four revision tools sit in one labelled menu, not loose in the format bar", async () => {
  const { ctx, pg } = await open(1280);
  eq(await pg.locator('#fmtbar [data-cmd="ghost"], #fmtbar [data-cmd="alt"], #fmtbar [data-cmd="stash"], #fmtbar [data-cmd="split"]').count(), 0, "no revision button is loose in the bar");
  const btn = pg.locator('#fmtbar [data-cmd="revise"]');
  eq(await btn.count(), 1, "one Revise button");
  ok(/Revise/.test(await btn.innerText()), "the button says Revise");
  eq(await btn.getAttribute("aria-haspopup"), "menu", "it opens a menu");
  eq(await pg.locator("#revise-menu [role=menuitem]").evaluateAll((xs) => xs.map((x) => x.dataset.cmd).join(",")), "alt,ghost,stash,split", "the four items, in order");
  const keys = await pg.locator("#revise-menu [role=menuitem] .menu-keys").allInnerTexts();
  ok(keys.length === 4 && keys.every((k) => k.trim().length > 0), "each item shows its shortcut");
  await ctx.close();
});

await kit.test("Revise by keyboard: Enter opens it, the arrows move, Enter runs Ghost, Escape closes and returns focus to Revise", async () => {
  const { ctx, pg } = await open(1280);
  await select(pg, 1, 8);
  await pg.locator('#fmtbar [data-cmd="revise"]').focus();
  await pg.keyboard.press("Enter");
  await waitFor(() => menuOpen(pg), "the Revise menu to open", 3000);
  await waitFor(() => pg.evaluate(() => document.activeElement.dataset.cmd === "alt"), "focus on the first item", 3000);
  await pg.keyboard.press("ArrowDown");
  eq(await pg.evaluate(() => document.activeElement.dataset.cmd), "ghost", "the arrow down moves to Ghost");
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await pg.locator(".scriobh-prose .scriobh-ghost").count()) >= 1, "the selection is ghosted", 8000);
  ok(!(await menuOpen(pg)), "the menu closes after an item runs");
  await pg.locator('#fmtbar [data-cmd="revise"]').focus();
  await pg.keyboard.press("Space");
  await waitFor(() => pg.evaluate(() => document.activeElement.dataset.cmd === "alt"), "the menu to open with Space, focus on its first item", 3000);
  await pg.keyboard.press("ArrowUp");
  eq(await pg.evaluate(() => document.activeElement.dataset.cmd), "split", "the arrow up wraps to the last item");
  await pg.keyboard.press("Escape");
  await waitFor(async () => !(await menuOpen(pg)), "Escape to close the menu", 3000);
  await waitFor(() => pg.evaluate(() => document.activeElement.dataset.cmd === "revise"), "Escape returns focus to the Revise button", 3000);
  await ctx.close();
});

await kit.test("Revise in Source: Alternative, Ghost and Stash are off, and the chapter break still works", async () => {
  const { ctx, pg } = await open(1280);
  await pg.locator('#fmtbar [data-cmd="source"]').click();
  await waitFor(() => pg.evaluate(() => window.scriobh.write.editor.mode() === "source"), "the Source view", 5000);
  await pg.locator('#fmtbar [data-cmd="revise"]').click();
  await waitFor(() => menuOpen(pg), "the Revise menu in Source", 3000);
  const off = await pg.evaluate(() => [...document.querySelectorAll("#revise-menu [role=menuitem]")].map((i) => [i.dataset.cmd, i.disabled]));
  eq(JSON.stringify(off), JSON.stringify([["alt", true], ["ghost", true], ["stash", true], ["split", false]]), "which items are off in Source");
  await pg.keyboard.press("Escape");
  await ctx.close();
});

await kit.test("Revise by mouse: a press on an item keeps the editor's selection and its focus", async () => {
  const { ctx, pg } = await open(1280);
  await select(pg, 1, 8);
  const before = await pmSel(pg);
  await pg.locator('#fmtbar [data-cmd="revise"]').click();
  await waitFor(() => menuOpen(pg), "the menu to open by mouse", 3000);
  eq(JSON.stringify(await pmSel(pg)), JSON.stringify(before), "opening the menu keeps the selection");
  const item = pg.locator('#revise-menu [data-cmd="ghost"]');
  const box = await item.boundingBox();
  await pg.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await pg.mouse.down();
  eq(JSON.stringify(await pmSel(pg)), JSON.stringify(before), "pressing an item keeps the selection");
  ok(await pg.evaluate(() => !!document.activeElement.closest(".ProseMirror")), "pressing an item keeps focus in the text");
  await pg.mouse.up();
  await waitFor(async () => (await pg.locator(".scriobh-prose .scriobh-ghost").count()) >= 1, "the item runs on the release", 8000);
  await ctx.close();
});

// ---- the header and the inspector's tabs -----------------------------------------------------------------------------
await kit.test("tabs: the inspector's tab strip fits at 1024, 1280 and 1440 px, with no tab clipped mid-word", async () => {
  for (const w of [1024, 1280, 1440]) {
    const { ctx, pg } = await open(w, { inspector: true });
    const m = await pg.evaluate(() => {
      const s = document.querySelector(".insp-tabs").getBoundingClientRect();
      const bad = [...document.querySelectorAll(".insp-tab")].filter((t) => {
        const r = t.getBoundingClientRect();
        return t.scrollWidth > t.clientWidth + 1 || r.left < s.left - 1 || r.right > s.right + 1;
      }).map((t) => t.textContent.trim());
      const strip = document.querySelector(".insp-tabs");
      return { overflow: strip.scrollWidth - strip.clientWidth, bad };
    });
    eq(m.overflow <= 1, true, `${w} px: the strip scrolls sideways by ${m.overflow}px`);
    eq(m.bad.join("|"), "", `${w} px: tabs clipped`);
    if (w === 1280) await shot(pg, "tabs-1280");
    await ctx.close();
  }
});

await kit.test("Compendium: its button shows the word at 1280 px and up, and its accessible name stays Compendium", async () => {
  for (const w of [1280, 1440]) {
    const { ctx, pg } = await open(w);
    const m = await pg.evaluate(() => {
      const b = document.querySelector("#comp-open");
      const l = b && b.querySelector(".comp-label");
      const r = l && l.getBoundingClientRect();
      return { shown: !!b && b.getBoundingClientRect().width > 0, label: !!r && r.width > 40 && getComputedStyle(l).display !== "none", name: b && b.getAttribute("aria-label") };
    });
    ok(m.shown && m.label, `${w} px: the Compendium button has a visible label`);
    eq(m.name, "Compendium", `${w} px: its accessible name`);
    await ctx.close();
  }
});

await kit.test("header: Saved stays visible, and the breadcrumb never overlaps the page tabs or clips a name", async () => {
  for (const w of [1024, 1280, 1440]) for (const insp of [false, true]) {
    const { ctx, pg } = await open(w, { inspector: insp, dark: w === 1024 });
    const m = await pg.evaluate(() => {
      const vis = (e) => !!e && e.getBoundingClientRect().width > 0;
      const save = document.querySelector("#save-word");
      const sr = save && save.getBoundingClientRect();
      // The text of the breadcrumb's last names, as drawn (a range covers the words, not the box).
            const ct = document.querySelector("#crumb-title"), pt = document.querySelector(".ptabs");
      const clipped = [...document.querySelectorAll(".crumb-write #crumb-title, .crumb-write .crumb-project > span, .crumb-write .crumb-part > span")].filter((e) => e.scrollWidth > e.clientWidth + 1 && vis(e)).map((e) => e.textContent.trim().slice(0, 30));
      const cb = vis(ct) ? ct.getBoundingClientRect() : null; const overlap = vis(pt) && cb ? cb.right > pt.getBoundingClientRect().left + 1 && cb.left < pt.getBoundingClientRect().right : false;
      return { savedShown: !!sr && sr.width > 0 && sr.right <= innerWidth && sr.left >= 0, clipped, overlap, h: document.querySelector(".topbar").getBoundingClientRect().height, docW: document.documentElement.scrollWidth, winW: innerWidth };
    });
    ok(m.savedShown, `${w} px${insp ? " with the inspector" : ""}: Saved is visible`);
    ok(m.h <= 56, `${w} px${insp ? " with the inspector" : ""}: the header is one line (${Math.round(m.h)} px)`);
    ok(!m.overlap, `${w} px${insp ? " with the inspector" : ""}: the breadcrumb overlaps the page tabs`);
    eq(m.docW <= m.winW, true, `${w} px: the page scrolls sideways`);
    if (!insp && w === 1280) await shot(pg, "header-1280");
    await ctx.close();
  }
});

await kit.test("phone: the header stays one line at 390 px", async () => {
  const { ctx, pg } = await open(390, { touch: true });
  const h = await pg.evaluate(() => document.querySelector(".topbar").getBoundingClientRect().height);
  ok(h <= 56, `the header is ${Math.round(h)} px tall`);
  await shot(pg, "header-390");
  await ctx.close();
});

await kit.finish("e2e-ux11a");
