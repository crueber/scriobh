#!/usr/bin/env node
// End-to-end checks for U11.3 (phone) in the writing view, at 390 and 360 px with touch:
// the sheet (the inspector) takes at most half the screen, the text above it stays visible and the sheet scrolls inside itself;
// Escape, the close button and a tap on the text each leave the caret where the writer had it (or where they tapped), and the page
// does not move; with the on-screen keyboard up (simulated through visualViewport) the caret line stays above the sheet;
// the format strip sits behind one "Aa" button, Bold works from it, and the editor's selection is kept; "Saved" is a word
// in the header, which is 52 px; and at 1280 px nothing of this changes.
// When UX11C_SHOTS names a folder, the states are saved there as PNG.
//
//   node scripts/e2e-ux11c.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { join } from "node:path";

const kit = await createKit("e2e-ux11c");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor } = kit;
const shots = process.env.UX11C_SHOTS || "";
const shot = (pg, name) => (shots ? pg.screenshot({ path: join(shots, `${name}.png`) }) : null);
const { ctx: main, page } = await kit.signedIn();
const only = kit.only;
let book, chapter, body;
const PARAS = 14;
kit.only = ""; // the setup always runs, whichever step is named
await kit.test("setup: a book and a chapter of fourteen paragraphs, so the text scrolls under the sheet", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: "The Sheet Road", kind: "novel" })).json.item;
  body = Array.from({ length: PARAS }, (_, i) => `Paragraph ${i + 1} of the chapter, with some ordinary words to fill the line and wrap it on a phone.`).join("\n\n");
});
kit.only = only;
await page.goto("about:blank"); // the chapter is open for writing in one tab at a time
const made = [];
const test = (name, fn) => kit.test(name, async () => {
  try { await fn(); } finally { await Promise.all(made.splice(0).map((c) => c.close().catch(() => {}))); }
});

// A writer's page: a phone (touch, coarse pointer) or a desktop, the editor ready, optionally with a fake on-screen keyboard.
const open = async (w, { dark = false, touch = true, kb = false, h = 844 } = {}) => {
  // A chapter of its own for each tab: a chapter that a closed tab still holds reads "Locked" until its lease runs out.
  const ch = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "A Chapter For The Sheet", kind: "chapter", content: body + "\n" })).json.item;
  const ctx = await kit.newContext({ viewport: { width: w, height: h }, isMobile: touch, hasTouch: touch, colorScheme: dark ? "dark" : "light" });
  await ctx.addCookies(await main.cookies());
  if (kb) await ctx.addInitScript(() => {
    // A stand-in for the on-screen keyboard: the visual viewport shrinks by window.__setKb(px) and says so with a resize event.
    const vv = new EventTarget();
    window.__kb = 0;
    Object.defineProperties(vv, { height: { get: () => window.innerHeight - window.__kb }, width: { get: () => window.innerWidth }, offsetTop: { value: 0 }, pageTop: { value: 0 }, scale: { value: 1 } });
    Object.defineProperty(window, "visualViewport", { value: vv, configurable: true });
    window.__setKb = (k) => { window.__kb = k; vv.dispatchEvent(new Event("resize")); };
  });
  made.push(ctx);
  const pg = await ctx.newPage();
  kit.watch(pg, `w${w}${touch ? " touch" : ""}${kb ? " kb" : ""}`);
  await pg.goto(`${kit.base()}/p/${book.slug}/d/${ch.id}`, { waitUntil: "load" });
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor && window.scriobh.write.editor.view)), "the editor", 15000);
  await waitFor(() => pg.evaluate(() => { const b = document.getElementById("insp-open"); return !!b && !b.hidden; }), "the Sidebar button", 8000);
  // js/phone.js arrives after the page's load event (data-ext), like js/revise.js; the steps press keys only once it has run.
  await waitFor(() => pg.evaluate(() => performance.getEntriesByType("resource").some((r) => /\/js\/phone\./.test(r.name))), "the phone script", 8000);
  await pg.waitForTimeout(250);
  return { ctx, pg };
};
// The start of paragraph i's text, by the editor's own document.
const at = (pg, i, off = 3) => pg.evaluate(([i, off]) => { const d = window.scriobh.write.editor.view.state.doc; let p = 0; for (let k = 0; k < i; k++) p += d.child(k).nodeSize; return p + 1 + off; }, [i, off]);
// Sets the editor's selection and focuses the text, through the editor's own state.
const sel = (pg, a, b = a) => pg.evaluate(([a, b]) => { const v = window.scriobh.write.editor.view; v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, a, b))); v.focus(); }, [a, b]);
const selNow = (pg) => pg.evaluate(() => { const s = window.scriobh.write.editor.view.state.selection; return [s.from, s.to]; });
const sheetOpen = (pg) => pg.evaluate(() => document.getElementById("inspector").classList.contains("is-open"));
const waitClosed = (pg) => waitFor(async () => !(await sheetOpen(pg)), "the sheet to close", 4000);
const waitOpen = (pg) => waitFor(() => sheetOpen(pg), "the sheet to open", 4000);
const focusInEditor = (pg) => pg.evaluate(() => { const v = window.scriobh.write.editor.view; return v.dom.contains(document.activeElement); });
const scrollY = (pg) => pg.evaluate(() => Math.round(window.scrollY));
const tapSidebar = async (pg) => { await pg.locator("#insp-open").tap(); await waitOpen(pg); await pg.waitForTimeout(350); };

// ---- the sheet: height, the text above it, and its own scrolling ----------------------------------------------------------------
await test("sheet at 390 by 844: at most half the screen, the text above it stays visible, and the sheet scrolls inside itself", async () => {
  const { ctx, pg } = await open(390);
  await tapSidebar(pg);
  const m = await pg.evaluate(() => {
    const s = document.getElementById("inspector"), r = s.getBoundingClientRect();
    const head = document.querySelector(".topbar").getBoundingClientRect().bottom;
    const paras = [...document.querySelectorAll("#editor .ProseMirror p, #editor p")].map((p) => p.getBoundingClientRect()).filter((b) => b.top >= head && b.bottom <= r.top && b.height > 0);
    const before = window.scrollY;
    window.scrollBy(0, 120);
    const scrolled = window.scrollY - before;
    s.scrollTop = 0;
    s.scrollTop = 150;
    return { ratio: r.height / innerHeight, band: r.top - head, bandRatio: (r.top - head) / innerHeight, visibleLines: paras.length, overflowY: getComputedStyle(s).overflowY, canScroll: s.scrollHeight > s.clientHeight, innerScroll: s.scrollTop, scrolled, sheetBottom: Math.round(r.bottom), vh: innerHeight };
  });
  console.log(`      sheet ${Math.round(m.ratio * 100)}% of the screen; text above it ${Math.round(m.bandRatio * 100)}%, ${m.visibleLines} paragraph(s) whole; scrollable inside: ${m.canScroll} (${m.overflowY})`);
  ok(m.ratio <= 0.5 + 0.01, `the sheet takes ${Math.round(m.ratio * 100)}% of the height, want at most about half`);
  ok(m.bandRatio >= 0.3, `the text above the sheet is ${Math.round(m.bandRatio * 100)}% of the height, want at least about a third`);
  ok(m.visibleLines >= 1, "no paragraph of the text shows above the sheet");
  ok(m.overflowY === "auto" || m.overflowY === "scroll", "the sheet scrolls (overflow " + m.overflowY + ")");
  ok(m.canScroll && m.innerScroll > 0, "the sheet's own content scrolls inside it");
  ok(m.scrolled > 0, "the text above the sheet scrolls while the sheet is open");
  await shot(pg, "sheet-390-light");
  await pg.emulateMedia({ colorScheme: "dark" });
  await shot(pg, "sheet-390-dark");
  await ctx.close();
});

await test("sheet at 360 by 740: at most half the screen, and a third of it is text above", async () => {
  const { ctx, pg } = await open(360, { h: 740, dark: true });
  await tapSidebar(pg);
  const m = await pg.evaluate(() => {
    const s = document.getElementById("inspector"), r = s.getBoundingClientRect(), head = document.querySelector(".topbar").getBoundingClientRect().bottom;
    return { ratio: r.height / innerHeight, bandRatio: (r.top - head) / innerHeight };
  });
  console.log(`      sheet at 360: ${Math.round(m.ratio * 100)}% of the screen; text above it ${Math.round(m.bandRatio * 100)}%`);
  ok(m.ratio <= 0.5 + 0.01, `the sheet takes ${Math.round(m.ratio * 100)}% of the height, want at most about half`);
  ok(m.bandRatio >= 0.3, `the text above the sheet is ${Math.round(m.bandRatio * 100)}% of the height, want at least about a third`);
  await shot(pg, "sheet-360-dark");
  await ctx.close();
});

// ---- closing: the caret and the page -------------------------------------------------------------------------------------------
await test("sheet at 390: Escape from the Sidebar button closes it and the caret is where it was, the page not moved", async () => {
  const { ctx, pg } = await open(390);
  const p = await at(pg, 5);
  await sel(pg, p);
  await tapSidebar(pg);
  await pg.evaluate(() => window.scrollTo(0, window.scrollY)); // a settled page before the press
  const y = await scrollY(pg);
  await pg.keyboard.press("Escape");
  await waitClosed(pg);
  await pg.waitForTimeout(250);
  eq((await selNow(pg))[0], p, "the caret after Escape");
  ok(await focusInEditor(pg), "focus is in the text after Escape");
  eq(await scrollY(pg), y, "the page after Escape");
  await ctx.close();
});

await test("sheet at 390: the close button closes it and the caret is where it was", async () => {
  const { ctx, pg } = await open(390);
  const p = await at(pg, 8, 10);
  await sel(pg, p);
  await tapSidebar(pg);
  const y = await scrollY(pg);
  await pg.locator("#insp-close").tap();
  await waitClosed(pg);
  await pg.waitForTimeout(250);
  eq((await selNow(pg))[0], p, "the caret after the close button");
  ok(await focusInEditor(pg), "focus is in the text after the close button");
  eq(await scrollY(pg), y, "the page after the close button");
  await ctx.close();
});

await test("sheet at 390: a tap on the text above the sheet closes it, and the caret goes where the writer tapped", async () => {
  const { ctx, pg } = await open(390);
  await sel(pg, await at(pg, 2));
  await tapSidebar(pg);
  const target = await pg.evaluate(() => {
    const s = document.getElementById("inspector").getBoundingClientRect(), head = document.querySelector(".topbar").getBoundingClientRect().bottom;
    const ps = [...document.querySelectorAll("#editor p")].map((p, i) => ({ i, r: p.getBoundingClientRect() }));
    const hit = ps.find((x) => x.r.top > head + 20 && x.r.bottom < s.top - 20);
    return hit ? { i: hit.i, x: Math.round(hit.r.left + hit.r.width / 2), y: Math.round(hit.r.top + hit.r.height / 2) } : null;
  });
  ok(!!target, "a paragraph shows above the sheet to tap");
  await pg.touchscreen.tap(target.x, target.y);
  await waitClosed(pg);
  const lo = await at(pg, target.i, 0), hi = lo + (await pg.evaluate((i) => window.scriobh.write.editor.view.state.doc.child(i).content.size, target.i));
  // The browser's selection reaches the editor's state a moment after the tap (selectionchange), so the caret is waited for.
  await waitFor(async () => { const [f] = await selNow(pg); return f >= lo - 1 && f <= hi; }, "the caret to move to the tapped paragraph", 3000).catch(() => {});
  const [from] = await selNow(pg);
  ok(from >= lo - 1 && from <= hi, `the caret (${from}) is in the paragraph the writer tapped (${lo}..${hi})`);
  await ctx.close();
});

// ---- the keyboard: the caret line stays above the sheet ----------------------------------------------------------------------
await test("sheet with the on-screen keyboard up (simulated): the caret line stays above the sheet and the keyboard", async () => {
  const { ctx, pg } = await open(390, { kb: true });
  await sel(pg, await at(pg, 6));
  await tapSidebar(pg);
  // a field of the sheet takes the keyboard (the Details form), and the keyboard comes up above it
  await pg.locator("#insp-form input, #insp-form textarea").first().focus();
  await pg.evaluate(() => window.__setKb(330));
  await pg.waitForTimeout(400);
  const m = await pg.evaluate(() => {
    const s = document.getElementById("inspector").getBoundingClientRect(), head = document.querySelector(".topbar").getBoundingClientRect().bottom;
    const v = window.scriobh.write.editor.view, c = v.coordsAtPos(v.state.selection.head);
    return { caretTop: c.top, caretBottom: c.bottom, sheetTop: s.top, sheetBottom: s.bottom, head, vvBottom: innerHeight - window.__kb };
  });
  console.log(`      with the keyboard: caret ${Math.round(m.caretTop)}..${Math.round(m.caretBottom)}, sheet from ${Math.round(m.sheetTop)} to ${Math.round(m.sheetBottom)}, keyboard from ${Math.round(m.vvBottom)}`);
  ok(m.caretBottom <= m.sheetTop, "the caret line is above the sheet");
  ok(m.caretTop >= m.head, "the caret line is below the header");
  ok(Math.abs(m.sheetBottom - m.vvBottom) < 2, "the sheet sits on the keyboard");
  await shot(pg, "sheet-390-keyboard");
  await pg.evaluate(() => window.__setKb(0));
  await ctx.close();
});

// ---- the Aa button ------------------------------------------------------------------------------------------------------------
for (const w of [390, 360]) {
  await test(`format strip at ${w} px: one Aa button; Bold works from it and the selection is kept`, async () => {
    const { ctx, pg } = await open(w);
    const visible = (sel2) => pg.evaluate((s) => [...document.querySelectorAll(s)].filter((e) => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== "hidden").map((e) => e.dataset.cmd || e.id), sel2);
    const aa = pg.locator("#fmt-aa");
    ok((await aa.count()) === 1, "there is an Aa button");
    ok(await aa.isVisible(), "the Aa button is visible");
    const closed = await visible("#fmtbar .tb[data-cmd]");
    eq(closed.sort().join(","), "redo,undo", "with the strip closed, only Undo and Redo show beside Aa");
    await sel(pg, await at(pg, 1, 0), await at(pg, 1, 8));
    const before = await selNow(pg);
    await aa.tap();
    eq(await aa.getAttribute("aria-expanded"), "true", "Aa says the strip is open");
    const open2 = await visible("#fmtbar .tb[data-cmd]");
    ok(open2.includes("bold"), "the strip shows Bold once Aa is tapped");
    eq((await selNow(pg)).join(","), before.join(","), "tapping Aa keeps the selection");
    await pg.locator('#fmtbar [data-cmd="bold"]').tap();
    const marked = await pg.evaluate(([a, b]) => { const v = window.scriobh.write.editor.view; return v.state.doc.rangeHasMark(a, b, v.state.schema.marks.strong); }, before);
    ok(marked, "Bold made the selected words bold");
    eq((await selNow(pg)).join(","), before.join(","), "Bold keeps the selection");
    await shot(pg, `strip-${w}-light`);
    await pg.emulateMedia({ colorScheme: "dark" });
    await shot(pg, `strip-${w}-dark`);
    await ctx.close();
  });
}

await test("format strip: Aa works from the keyboard (Enter opens it; the editor keeps focus)", async () => {
  const { ctx, pg } = await open(390); // a coarse pointer, as a phone has; the keys are a real keyboard
  await sel(pg, await at(pg, 1, 0), await at(pg, 1, 4));
  await pg.locator("#fmt-aa").focus();
  await pg.keyboard.press("Enter");
  eq(await pg.locator("#fmt-aa").getAttribute("aria-expanded"), "true", "Enter opens the strip");
  ok(await pg.locator('#fmtbar [data-cmd="italic"]').isVisible(), "the italic button shows");
  await ctx.close();
});

// ---- Saved, and the header -------------------------------------------------------------------------------------------------------
for (const w of [390, 360]) {
  await test(`header at ${w} px: Saved is a word, and the header is one line of 52 px`, async () => {
    const { ctx, pg } = await open(w, { dark: w === 360 });
    const m = await pg.evaluate(() => {
      const sw = document.getElementById("save-word"), r = sw.getBoundingClientRect(), cs = getComputedStyle(sw);
      const top = document.querySelector(".topbar"), tr = top.getBoundingClientRect();
      const others = [...top.querySelectorAll("a, button, .crumb-write, .topbar-right > *")].filter((e) => e !== sw && !sw.contains(e) && !e.contains(sw) && e.getClientRects().length > 0).map((e) => e.getBoundingClientRect());
      const hit = others.some((o) => r.left < o.right - 1 && r.right > o.left + 1 && r.top < o.bottom - 1 && r.bottom > o.top + 1);
      return { cut: sw.scrollWidth > sw.clientWidth + 1, text: sw.textContent, w: r.width, h: r.height, clip: cs.clipPath, pos: cs.position, shown: sw.getClientRects().length > 0, inside: r.right <= innerWidth && r.left >= 0, hit, header: tr.height, docW: document.documentElement.scrollWidth, winW: innerWidth, lines: Math.round(tr.height / Math.max(1, parseFloat(getComputedStyle(top).lineHeight) || 16)) };
    });
    console.log(`      header ${m.header} px at ${w}; Saved ${Math.round(m.w)}x${Math.round(m.h)} px, clip ${m.clip}, position ${m.pos}`);
    eq(m.text, "Saved", "the save state's word");
    ok(m.shown && m.w >= 30 && m.clip === "none" && m.pos !== "absolute" && !m.cut, `"Saved" is drawn as a word (${Math.round(m.w)} px wide, not clipped)`);
    ok(m.inside && !m.hit, '"Saved" sits inside the header, over nothing');
    eq(Math.round(m.header), 52, "the header height");
    ok(m.docW <= m.winW, "the page does not scroll sideways");
    // The longest words the save state can say (js/doc.js LABEL) also stay on one line over nothing.
    for (const word of ["Signed out", "Too large", "Not saved"]) {
      const long = await pg.evaluate((word) => {
        const sw = document.getElementById("save-word"), top = document.querySelector(".topbar");
        sw.textContent = word;
        const r = sw.getBoundingClientRect(), tr = top.getBoundingClientRect();
        const others = [...top.querySelectorAll("a, button, .crumb-write, .topbar-right > *")].filter((e) => e !== sw && !sw.contains(e) && !e.contains(sw) && e.getClientRects().length > 0).map((e) => e.getBoundingClientRect());
        return { h: tr.height, inside: r.right <= innerWidth && r.left >= 0, hit: others.some((o) => r.left < o.right - 1 && r.right > o.left + 1 && r.top < o.bottom - 1 && r.bottom > o.top + 1), w: r.width };
      }, word);
      ok(Math.round(long.h) === 52 && long.inside && !long.hit, `"${word}" (${Math.round(long.w)} px) keeps the header at 52 px and sits over nothing (header ${Math.round(long.h)} px)`);
    }
    await shot(pg, `header-${w}-light`);
    await ctx.close();
  });
}

// ---- desktop is unchanged ---------------------------------------------------------------------------------------------------------------
await test("desktop at 1280 px: no Aa button, the whole format strip shows, Saved is a word, and Escape returns focus to the Sidebar button", async () => {
  const { ctx, pg } = await open(1280, { touch: false, h: 800 });
  const aaShown = await pg.evaluate(() => { const a = document.getElementById("fmt-aa"); return !!a && a.getClientRects().length > 0; });
  ok(!aaShown, "no Aa button on the desktop");
  const tools = await pg.evaluate(() => [...document.querySelectorAll('#fmtbar .tb[data-cmd]')].filter((e) => e.getClientRects().length > 0).map((e) => e.dataset.cmd));
  ok(tools.includes("bold") && tools.includes("h2") && tools.includes("undo"), "the whole strip shows: " + tools.join(","));
  eq(await pg.evaluate(() => document.getElementById("save-word").textContent), "Saved", "the save word on the desktop");
  await pg.locator("#insp-open").click();
  await pg.locator("#itab-details").focus();
  await pg.keyboard.press("Escape");
  await waitClosed(pg);
  eq(await pg.evaluate(() => document.activeElement && document.activeElement.id), "insp-open", "focus back on the Sidebar button");
  await shot(pg, "desktop-1280");
  await ctx.close();
});

await kit.finish("e2e-ux11c");
