#!/usr/bin/env node
// Every key the shortcuts window lists does something on the page that lists it (F-1250, F-1251), in a real Chromium.
//
//   node scripts/e2e-shortcuts-keys.mjs [--only substring]
//
// For each kind of page (the writing view, the outline, the corkboard, the status board, the research board, the timeline, the
// reading view and the library) and for an editor and a reader, the window is opened, the rows of the sections about the page are
// read from it (with the keys it prints), and each of their keys is pressed on the page, in the state its row describes. A key
// "does something" when the page reacts: the document changes, focus moves, a request goes out, the address changes, or the handler
// takes the key (the keydown is default-prevented). A control key that nothing handles is pressed first each time, and the page
// must be quiet for it, or the probe would be proving nothing. A row with no probe fails, so a new key cannot be added to the
// registry without saying how to press it here. A reader's window lists no key of a script that is not loaded for a reader.
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startFake } from "./fake-ai.mjs";

const kit = await createKit("e2e-shortcuts-keys", { env: { SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000" } });
kit.deadline(12 * 60 * 1000);
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const onlyRows = process.argv.includes("--rows") ? process.argv[process.argv.indexOf("--rows") + 1].split(",") : null;
const base = () => kit.base();
const fake = await startFake();
const wren = await kit.signedIn();
const ada = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "admin@dev.scriobh");
const main = wren.ctx;
kit.main = wren.page;
let reader; // a person who can only read the book

const post = async (path, data) => {
  const r = await api(main, "POST", path, data);
  if (r.status >= 300 || !r.json) throw new Error(`POST ${path} answered ${r.status} ${JSON.stringify(r.json)}`);
  return r.json;
};
const TEXT = "The harbour was very quiet. Mara counted the boats twice.\n\nThe tide turned.\n\n- one\n- two\n";
let B, ids = {}, T, C, tev = {}, tdoc = {};

// ---- the keys as the window prints them, as Playwright's names -----------------------------------------------------------------
const CAP = { Ctrl: "ControlOrMeta", Shift: "Shift", Alt: "Alt", "↑": "ArrowUp", "↓": "ArrowDown", "←": "ArrowLeft", "→": "ArrowRight", "Page Up": "PageUp", "Page Down": "PageDown", Esc: "Escape", Menu: "ContextMenu", "/": "Slash", "\\": "Backslash", "[": "BracketLeft", "]": "BracketRight", "?": "Shift+Slash", "A to Z": "h" };
const chordOf = (caps) => {
  const alt = caps.includes("Alt") && caps.includes("Shift");
  return caps.map((c) => CAP[c] || (c.length === 1 && /[A-Z]/.test(c) ? (alt ? "Key" + c : c.toLowerCase()) : c)).join("+");
};

// ---- seeing whether the page reacted -------------------------------------------------------------------------------------
const arm = async (page) => {
  await page.evaluate(() => {
    if (window.__pr) return;
    const pr = (window.__pr = { n: 0, taken: 0, log: [] });
    new MutationObserver((l) => { pr.n += l.length; for (const r of l) if (pr.log.length < 8) pr.log.push(r.type + " " + (r.target.id || r.target.className || r.target.nodeName) + (r.attributeName ? " @" + r.attributeName : "")); }).observe(document, { subtree: true, childList: true, attributes: true, characterData: true });
    document.addEventListener("focusin", (e) => { pr.n++; if (pr.log.length < 8) pr.log.push("focusin " + (e.target.id || e.target.nodeName)); }, true);
    // after the whole dispatch: did anything take the key?
    window.addEventListener("keydown", (e) => { setTimeout(() => { if (e.defaultPrevented) { pr.taken++; if (pr.log.length < 8) pr.log.push("took " + e.key); } }); }, true);
    try { new PerformanceObserver((l) => { pr.n += l.getEntries().length; for (const x of l.getEntries()) if (pr.log.length < 8) pr.log.push("res " + x.name); }).observe({ type: "resource" }); } catch { /* no resource timing */ }
  });
};
const count = (page) => page.evaluate(() => (window.__pr ? window.__pr.n + window.__pr.taken : -1)).catch(() => -2);
// Press a key, and say whether the page reacted: the counters moved, the address changed, or the page was replaced.
const stable = async (page) => {
  // wait for the page's own noise (a save, a live refresh, a toast going away) to stop: a window of 400 ms with nothing in it
  for (let i = 0; i < 20; i++) {
    const a = await count(page);
    await sleep(400);
    if ((await count(page)) === a) return;
  }
  throw new Error("the page never went quiet");
};
const reacts = async (page, chord) => {
  await arm(page);
  const url = page.url();
  await stable(page);
  const before = await count(page);
  await page.keyboard.press(chord);
  await sleep(350);
  const after = await count(page);
  return after !== before || page.url() !== url || after < 0;
};
// A key nothing handles must not make the page react, or the probe measures the page's own noise.
const quiet = async (page, what) => {
  const r = await reacts(page, "F7");
  if (r) throw new Error(`${what}: the page reacts to a key nothing handles (F7), so a probe here would prove nothing (${JSON.stringify(await page.evaluate(() => window.__pr && window.__pr.log))})`);
};

// ---- the writing view's fixtures -----------------------------------------------------------------------------------------
const settle = (page) => waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.tools && window.scriobh.alts && window.scriobh.palette)), "the page's scripts", 12000);
const reset = async (id, content) => {
  const cur = (await api(main, "GET", `/api/v1/docs/${id}`)).json.item;
  if (cur.content !== content) await api(main, "PATCH", `/api/v1/docs/${id}`, { base_revision: cur.revision, content });
};
const openDoc = async (page, id) => {
  // The last probe may have changed the chapter in a page still open: it saves first, or the reset below moves the revision under
  // it and the page's own save on leaving is refused (a 409 the watcher reports), which is right for the app and wrong for the walk.
  await page.evaluate(() => window.scriobh && window.scriobh.write
    ? Promise.race([window.scriobh.write.flushAll().then(() => 1, () => 0), new Promise((r) => setTimeout(() => r(0), 5000))])
    : 0).catch(() => 0);
  if (id === ids.main) await reset(id, TEXT); // the probes change the chapter: each starts from the same words
  await page.goto(`${base()}/p/${B.slug}/d/${id}`);
  await page.evaluate(() => { try { localStorage.removeItem("scriobh:tree"); } catch (e) { /* none */ } document.body.classList.remove("tree-hidden"); }); // Mod+\\ was the last probe's
  await page.waitForSelector(".scriobh-prose");
  await settle(page);
  await sleep(200);
};
const selectText = (page, needle) => page.evaluate((n) => {
  const ed = window.scriobh.write.editor;
  let at = -1;
  ed.view.state.doc.descendants((node, pos) => { if (at < 0 && node.isText) { const i = node.text.indexOf(n); if (i >= 0) at = pos + i; } });
  if (at < 0) throw new Error("no such text: " + n);
  ed.alts.select(at, at + n.length);
  ed.view.focus();
}, needle);
const treeRow = (page, title) => page.locator(`#tree .tree-row[aria-label*="${title}"]`).first();
const unfocus = (page) => page.evaluate(() => { document.activeElement && document.activeElement.blur(); });

// Each row of the window: how to put the page in the state the row describes. The prep is run again for every key of the row.
// prep(page, key) leaves the page ready; the key is then pressed. A row listed here without a prep for a kind of person is
// pressed in the state of the first.
const ed = (opts = {}) => async (page) => {
  await openDoc(page, ids.main);
  await page.locator(".scriobh-prose").click();
  await selectText(page, opts.select || "harbour");
  if (opts.type) { await page.keyboard.press("End"); await page.keyboard.type(" x"); await sleep(150); }
  if (opts.undo) { await page.keyboard.press("ControlOrMeta+z"); await sleep(150); }
};
const inTree = (title) => async (page) => { await openDoc(page, ids.main); await treeRow(page, title).focus(); await sleep(300); };
const aiProposal = async (page) => {
  await openDoc(page, ids.main);
  await page.locator(".scriobh-prose").click();
  await selectText(page, "The harbour was very quiet.");
  await page.keyboard.press("Alt+Shift+KeyI");
  await page.locator("dialog.ai-menu").waitFor();
  await page.locator("dialog.ai-menu").getByRole("button", { name: "Tighten" }).click();
  const dlg = page.locator("dialog.ai-dlg:not(.ai-menu)");
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.locator("#ai-pop").waitFor();
  await page.keyboard.press("Alt+Shift+KeyJ"); // focus goes to the card
  await sleep(200);
};

const SPEC = {
  // writing and format
  bold: ed(), italic: ed(), strike: ed(), h1: ed(), h2: ed(), h3: ed(), paragraph: ed(), link: ed(), scene_break: ed(), line_break: ed(), source: ed(), save: ed({ type: true }),
  list_indent: ed({ select: "two" }), list_outdent: ed({ select: "two" }),
  undo: ed({ type: true }), redo: ed({ type: true, undo: true }),
  // alternatives and ghosts
  alt_add: ed(), alt_next: ed(), alt_prev: ed(), alt_orig: ed(), ghost: ed(), stash: ed(),
  // the writing partner
  ai_menu: ed(), ai_proposal: async (page) => { await ed()(page); await page.keyboard.press("Alt+Shift+KeyI"); await page.locator("dialog.ai-menu").waitFor(); await page.keyboard.press("Escape"); await waitFor(() => page.evaluate(() => !!window.scriobh.chords.KeyJ), "ai.js"); await page.locator(".scriobh-prose").click(); },
  ai_accept: aiProposal, ai_keep: aiProposal, ai_reject: aiProposal, ai_retry: aiProposal,
  ai_chat_send: async (page) => {
    await ed()(page);
    await page.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:ai", { detail: "chat" })));
    await page.locator("#aic-msg").waitFor();
    await page.locator("#aic-msg").fill("Is the opening too slow?");
  },
  // the Compendium tab: the chord opens it on the selected words, Enter opens the first result of the search, Esc goes back to the caret
  compendium_panel: ed(),
  // the Comments tab: the chord opens it on the selected words, with the composer for them
  comments_panel: ed(),
  compendium_open: async (page) => { await compendiumSearch(page, "Captain"); },
  compendium_esc: async (page) => { await compendiumSearch(page, ""); },
  // chapters
  split: async (page) => { await ed()(page); await page.keyboard.press("ArrowRight"); },
  chapter_prev: async (page) => { await openDoc(page, ids.mid); await page.locator(".scriobh-prose").click(); },
  chapter_next: async (page) => { await openDoc(page, ids.mid); await page.locator(".scriobh-prose").click(); },
  switcher: async (page) => { await openDoc(page, ids.main); await unfocus(page); },
  manuscript: async (page) => { await openDoc(page, ids.main); await unfocus(page); },
  // the manuscript tree
  tree_move: inTree("High Water"), tree_ends: inTree("High Water"), tree_fold: inTree("Part One"), tree_open: inTree("High Water"),
  tree_rename: inTree("High Water"), tree_trash: inTree("Thanks"), tree_reorder: inTree("High Water"), tree_nest: inTree("High Water"),
  tree_jump: inTree("Dedication"), tree_menu: inTree("High Water"),
  // the outline
  outline_move: async (page) => { await page.goto(`${base()}/p/${B.slug}/outline`); await page.waitForSelector("#o-live"); await page.locator("li.orow").nth(2).focus(); },
  outline_edit: async (page) => { await page.goto(`${base()}/p/${B.slug}/outline`); await page.waitForSelector("#o-live"); await page.locator("li.orow").nth(2).focus(); },
  outline_reorder: async (page) => { await page.goto(`${base()}/p/${B.slug}/outline`); await page.waitForSelector("#o-live"); await page.locator("li.orow").nth(3).focus(); },
  outline_nest: async (page) => { await page.goto(`${base()}/p/${B.slug}/outline`); await page.waitForSelector("#o-live"); await page.locator("li.orow").nth(3).focus(); },
  outline_save: async (page) => {
    await page.goto(`${base()}/p/${B.slug}/outline`); await page.waitForSelector("#o-live");
    await page.locator("li.orow").nth(2).focus(); await page.keyboard.press("Enter");
    await page.locator("li.orow form input").first().waitFor();
    await page.locator("li.orow form input").first().fill("A new title");
  },
  // the corkboard
  cards_move: async (page) => { await page.goto(`${base()}/p/${B.slug}/cards`); await page.locator(".ccard").nth(1).focus(); },
  cards_edit: async (page) => { await page.goto(`${base()}/p/${B.slug}/cards`); await page.locator(".ccard").nth(1).focus(); },
  cards_earlier: async (page) => { await page.goto(`${base()}/p/${B.slug}/cards`); await page.locator(".ccard").nth(2).focus(); },
  cards_later: async (page) => { await page.goto(`${base()}/p/${B.slug}/cards`); await page.locator(".ccard").nth(1).focus(); },
  cards_save: async (page) => {
    await page.goto(`${base()}/p/${B.slug}/cards`); await page.locator(".ccard").nth(1).focus(); await page.keyboard.press("Enter");
    await page.locator(".ccard form textarea, .ccard form input").first().waitFor();
    await page.locator(".ccard form textarea").first().fill("A new synopsis.");
  },
  // the status board
  status_move: async (page) => { await page.goto(`${base()}/p/${B.slug}/board`); await page.locator(".bcard").nth(1).focus(); },
  status_column: async (page) => { await page.goto(`${base()}/p/${B.slug}/board`); await page.locator(".bcard").nth(1).focus(); },
  // the research board
  research_column: async (page) => {
    await post(`/api/v1/projects/${B.id}/research`, { kind: "note", title: `Card ${Date.now()}`, body: "x", status: "reading" });
    await page.goto(`${base()}/p/${B.slug}/research/board`); await page.locator('.r-col[data-key="reading"] .r-name').first().focus(); },
  // the timeline of a book that has a Compendium
  event_move: async (page) => { await page.goto(`${base()}/p/${T.slug}/timeline?order=narrative`); await page.waitForSelector("tr[data-event]"); await page.locator("tr[data-event] a").nth(1).focus(); },
  // reading and review
  read_next: async (page) => { await page.goto(`${base()}/p/${B.slug}/read/${ids.main}`); await page.waitForSelector("#reader"); await page.locator("body").click({ position: { x: 3, y: 300 } }); },
  read_prev: async (page) => { await page.goto(`${base()}/p/${B.slug}/read/${ids.main}`); await page.waitForSelector("#reader"); await page.locator("body").click({ position: { x: 3, y: 300 } }); },
  review_caret: async (page) => { await openRead(page); },
  review_select: async (page) => { await openRead(page); await page.keyboard.press("]"); await sleep(200); },
  review_comment: async (page) => { await openRead(page); await page.keyboard.press("]"); await page.keyboard.press("Alt+Shift+ArrowRight"); await page.keyboard.press("Alt+Shift+ArrowRight"); },
  review_suggest: async (page) => {
    await openRead(page);
    await page.getByRole("button", { name: "Suggesting", exact: true }).click();
    await page.locator("body").click({ position: { x: 3, y: 300 } });
    await page.keyboard.press("]"); await page.keyboard.press("Alt+Shift+ArrowRight"); await page.keyboard.press("Alt+Shift+ArrowRight");
  },
  review_margin: async (page) => { await openRead(page); },
  review_thread: async (page) => { await inMargin(page); },
  review_show: async (page) => { await inMargin(page); },
  review_reply: async (page) => { await inMargin(page); },
  review_resolve: async (page) => { await inMargin(page); },
  review_next: async (page) => { await openRead(page); },
  review_leave: async (page) => { await inMargin(page); },
  review_send: async (page) => {
    await openRead(page); await page.keyboard.press("]"); await page.keyboard.press("Alt+Shift+ArrowRight"); await page.keyboard.press("Alt+Shift+ArrowRight");
    await page.keyboard.press("c"); await page.waitForSelector("#rv-new"); await page.fill("#rv-new", "A comment from the keys test.");
  },
  // the application
  palette: async (page) => { await openDoc(page, ids.main); await unfocus(page); },
  palette_k: async (page) => { await openDoc(page, ids.main); await unfocus(page); },
  help: async (page) => { await openDoc(page, ids.main); await unfocus(page); },
  esc: async (page) => { await openDoc(page, ids.main); await unfocus(page); await page.keyboard.press("Shift+Slash"); await page.locator("#dialog[open] .kb").waitFor(); },
};
// The Compendium tab open with its search focused (the chord with nothing selected), and a word typed in it.
const compendiumSearch = async (page, word) => {
  await openDoc(page, ids.main);
  await page.locator(".scriobh-prose").click();
  await page.keyboard.press("Alt+Shift+KeyC");
  await page.locator("#lk-q").waitFor();
  await waitFor(() => page.evaluate(() => document.activeElement && document.activeElement.id === "lk-q"), "the search to take the focus");
  if (word) { await page.keyboard.type(word); await page.locator(".lk-pick").first().waitFor(); }
  // leaving the editor saves it, the format bar catches up on the next key event and the names are asked for 2.5 s after a save: let all of it
  // happen, so that the page is quiet when the key under test is pressed
  await page.keyboard.press("F7");
  await sleep(3200);
};
let threadN = 0;
const openRead = async (page) => {
  // every probe has a thread of its own: the keys resolve and reply, and the next probe needs one open
  ok((await api(main, "POST", `/api/v1/projects/${B.id}/comments`, { document_id: ids.main, quote: "The tide turned", plain: true, body: `Thread ${++threadN} to walk along.` })).status < 300, "a comment");
  await page.goto(`${base()}/p/${B.slug}/read/${ids.main}`);
  await page.waitForSelector("#rv-margin");
  await waitFor(async () => (await page.locator(".rv-card").count()) > 0, "the margin loads");
  await page.locator("body").click({ position: { x: 3, y: 300 } });
};
const inMargin = async (page) => {
  await openRead(page);
  await page.keyboard.press("m");
  await waitFor(() => page.evaluate(() => !!document.activeElement.closest(".rv-card")), "focus in the list");
};

// ---- the window, read ---------------------------------------------------------------------------------------------------------
const windowRows = async (page, only) => {
  await unfocus(page);
  await page.keyboard.press("Shift+Slash");
  await page.locator("#dialog[open] .kb").waitFor();
  const rows = await page.evaluate((onlyHere) => [...document.querySelectorAll("#dialog .kb-group")].filter((g) => !onlyHere || g.classList.contains("is-here") || g.getAttribute("aria-labelledby") === "kb-g-app").flatMap((g) => [...g.querySelectorAll(".kb-row")].map((r) => ({
    group: g.getAttribute("aria-labelledby").slice(5), id: r.dataset.row,
    chords: [...r.querySelectorAll(".kb-alt:not([hidden])")].map((a) => [...a.querySelectorAll("kbd")].map((k) => (k.querySelector("[aria-hidden]") || k).textContent)),
  }))), only);
  await page.keyboard.press("Escape");
  await waitFor(async () => !(await page.evaluate(() => !!document.querySelector("#dialog[open]"))), "the window to close");
  return rows;
};

const platform = async (opts = {}) => {
  const c = await kit.newContext({ viewport: { width: 1280, height: 900 }, ...opts.ctx });
  await c.addCookies(await (opts.from || main).cookies());
  await c.addInitScript(() => { Object.defineProperty(navigator, "platform", { get: () => "Win32" }); });
  const page = await c.newPage();
  kit.watch(page, opts.label || "keys");
  return { c, page };
};

// One kind of page, for one kind of person: read the window, then press every key it lists.
const walk = async (page, name, url, expectHere, opts = {}) => {
  await page.goto(url);
  await settleAny(page);
  const rows = await windowRows(page, true);
  const here = rows.filter((r) => r.group !== "app");
  const groups = new Set(here.map((r) => r.group));
  eq([...groups].sort().join(","), expectHere.slice().sort().join(","), `${name}: the sections about this page`);
  const missing = rows.filter((r) => !SPEC[r.id] && !(opts.skip || []).includes(r.id)).map((r) => r.id);
  if (missing.length) throw new Error(`${name}: the window lists keys this script has no way to press: ${missing.join(", ")}`);
  const failed = [];
  let n = 0;
  for (const r of rows) {
    if ((opts.skip || []).includes(r.id) || (onlyRows && !onlyRows.includes(r.id))) continue;
    let first = true;
    for (const caps of r.chords) {
      const key = r.id === "tree_jump" ? "h" : chordOf(caps); // a letter: High Water starts with it, and the row in focus does not
      n++;
      try {
        await SPEC[r.id](page);
        await arm(page);
        await stable(page);
        if (first) await quiet(page, `${name} ${r.id}`); // a key nothing handles must leave the page as it was
        first = false;
        const where = await page.evaluate(() => { const a = document.activeElement; return a ? a.tagName + "." + a.className + "#" + a.id : "none"; });
        if (!(await reacts(page, key))) failed.push(`${r.id} (${caps.join(" ")} as ${key}) [focus was ${where}]`);
      } catch (e) {
        failed.push(`${r.id} (${caps.join(" ")} as ${key}): ${e.message.split("\n")[0].slice(0, 160)}`);
      }
    }
  }
  if (failed.length) throw new Error(`${name}: a key the window lists did nothing: ${failed.join("; ")}`);
  return { rows, n };
};
const settleAny = async (page) => { await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.palette)), "the palette script", 12000); await sleep(300); };

// ---- setup ---------------------------------------------------------------------------------------------------------------------
const wanted = kit.only;
kit.only = ""; // the setup always runs
await kit.test("setup: a book with parts and chapters and AI on, a reader, a comment, research, and a book with a timeline", async () => {
  const pv = await api(ada.ctx, "POST", "/api/v1/ai/providers", { kind: "openai_compatible", name: "Fake", base_url: fake.base, local: true, default_model: "fake-model", scope: "instance" });
  ok(pv.status < 300, "provider: " + JSON.stringify(pv.json));
  B = (await post("/api/v1/projects", { title: "Keys book", kind: "novel" })).item;
  const seeded = (await api(main, "GET", `/api/v1/projects/${B.id}/docs`)).json.order[0];
  const create = async (body) => (await post(`/api/v1/projects/${B.id}/docs`, body)).item.id;
  await create({ title: "placeholder", kind: "note" });
  await api(main, "POST", `/api/v1/docs/${seeded}/trash`);
  ids.front = await create({ title: "Dedication", kind: "front_matter", content: "For everyone.\n" });
  ids.p1 = await create({ title: "Part One", kind: "part" });
  ids.main = await create({ title: "Low Water", kind: "chapter", parent_id: ids.p1, content: TEXT });
  ids.ledger = await create({ title: "The ledger", kind: "scene", parent_id: ids.main, content: "Ledger words.\n" });
  ids.mid = await create({ title: "High Water", kind: "chapter", parent_id: ids.p1, content: "Middle words here.\n\nMore of them.\n" });
  ids.p2 = await create({ title: "Part Two", kind: "part" });
  ids.last = await create({ title: "Slack Water", kind: "chapter", parent_id: ids.p2, content: "Last words.\n" });
  ids.back = await create({ title: "Thanks", kind: "back_matter", content: "Thank you.\n" });
  for (const m of (await api(main, "GET", `/api/v1/projects/${B.id}/docs`)).json.items) if (m.title === "placeholder") await api(main, "POST", `/api/v1/docs/${m.id}/trash`);
  ok((await api(main, "PUT", `/api/v1/projects/${B.id}/ai/policy`, { mode: "ask", providers: [pv.json.item.id], actions: ["selection", "alternatives", "chat", "critique", "summaries", "continuity", "extraction", "lab"] })).status < 300, "policy");
  const rctx = await kit.newContext({ viewport: { width: 1280, height: 900 } });
  const rp = await rctx.newPage();
  const { signIn } = await import("./harness.mjs");
  await signIn(rp, base(), "reader@dev.scriobh");
  reader = { ctx: rctx, page: rp };
  const m = await api(main, "POST", `/api/v1/projects/${B.id}/members`, { email: "reader@dev.scriobh", role: "reader" });
  ok(m.status < 300, "reader: " + m.status + JSON.stringify(m.json));
  // the book's own Compendium, with one entry the chapter does not name, for the Compendium tab's keys (the reader is on it)
  const bc = (await post("/api/v1/compendiums", { name: "Keys book Bible", project_id: B.id })).item;
  await post(`/api/v1/compendiums/${bc.id}/entities`, { kind: "character", name: "Captain Vell", summary: "Keeps the lamp." });
  ok((await api(main, "POST", `/api/v1/compendiums/${bc.id}/members`, { email: "reader@dev.scriobh", role: "reader" })).status < 300, "the reader joins the Compendium");
  const cm = await api(main, "POST", `/api/v1/projects/${B.id}/comments`, { document_id: ids.main, quote: "The tide turned", plain: true, body: "A comment to walk along." });
  ok(cm.status < 300, "comment: " + cm.status + JSON.stringify(cm.json));
  // research notes in two columns, for the research board
  for (const [title, status] of [["Tides", "inbox"], ["Boats", "reading"], ["Reeds", "useful"]]) await api(main, "POST", `/api/v1/projects/${B.id}/research`, { kind: "note", title, body: "x", status });
  // a book with a Compendium, events and dated chapters, for the timeline
  C = (await post("/api/v1/compendiums", { name: "Keys Bible" })).item;
  T = (await post("/api/v1/projects", { title: "Keys timeline", kind: "novel" })).item;
  ok((await api(main, "PUT", `/api/v1/projects/${T.id}/compendium`, { compendium_id: C.id })).status === 200, "compendium");
  for (const [k, title, date] of [["a", "First", "1990"], ["b", "Second", "1991"], ["c", "Third", "1992"]]) tev[k] = (await post(`/api/v1/compendiums/${C.id}/timeline/events`, { title, story_date: date })).item.id;
  const tseed = (await api(main, "GET", `/api/v1/projects/${T.id}/docs`)).json.order[0];
  for (const [k, title, date, e] of [["a", "Arrival", "1990", "a"], ["b", "Departure", "1991", "b"], ["c", "Homecoming", "1992", "c"]]) {
    const d = (await post(`/api/v1/projects/${T.id}/docs`, { title, kind: "chapter", content: "Words." })).item;
    const cur = (await api(main, "GET", `/api/v1/docs/${d.id}`)).json.item;
    await api(main, "PATCH", `/api/v1/docs/${d.id}`, { base_revision: cur.revision, story_date: date });
    await post(`/api/v1/docs/${d.id}/timeline-events`, { event_id: tev[e] });
    tdoc[k] = d.id;
  }
  await api(main, "POST", `/api/v1/docs/${tseed}/trash`);
  await api(main, "POST", `/api/v1/projects/${T.id}/members`, { email: "reader@dev.scriobh", role: "reader" });
});

kit.only = wanted;

const WRITE = ["write", "undo", "alts", "chapters", "compendium", "comments", "tree"];
const WRITE_AI = [...WRITE, "ai"];
const R_NOEDIT = ["write", "chapters", "compendium", "tree"]; // write: the Markdown source, which a reader can look at

await kit.test("the writing view, for an editor: every key it lists does something", async () => {
  kit.allowed.push(/422/); // Split chapter here at a place that cannot be cut answers 422, and says so
  const { c, page } = await platform({ label: "write" });
  const { rows, n } = await walk(page, "write", `${base()}/p/${B.slug}/d/${ids.main}`, WRITE_AI, {});
  console.log(`      ${rows.length} rows, ${n} keys pressed`);
  await c.close();
});

await kit.test("the writing view, for a reader: what it lists works, and the keys for editing are not listed", async () => {
  const { c, page } = await platform({ label: "write reader", from: reader.ctx });
  const { rows } = await walk(page, "write (reader)", `${base()}/p/${B.slug}/d/${ids.main}`, R_NOEDIT, {});
  eq(rows.some((r) => ["bold", "tree_rename", "tree_trash", "split", "undo", "ai_menu"].includes(r.id)), false, "no key for editing is listed to a reader");
  eq(rows.filter((r) => r.group === "write").map((r) => r.id).join(","), "source", "of the writing keys a reader has the Markdown source and nothing else");
  await c.close();
});

for (const [name, path, here, spec] of [
  ["the outline", (s) => `/p/${s}/outline`, ["outline"], "outline"],
  ["the corkboard", (s) => `/p/${s}/cards`, ["cards"], "cards"],
  ["the status board", (s) => `/p/${s}/board`, ["status"], "status"],
  ["the research board", (s) => `/p/${s}/research/board`, ["research"], "research"],
]) {
  await kit.test(`${name}, for an editor: every key it lists does something`, async () => {
    const { c, page } = await platform({ label: name });
    const { rows, n } = await walk(page, name, base() + path(B.slug), here, {});
    console.log(`      ${rows.length} rows, ${n} keys pressed`);
    await c.close();
  });
  await kit.test(`${name}, for a reader: none of the keys for editing is listed`, async () => {
    const { c, page } = await platform({ label: name + " reader", from: reader.ctx });
    await page.goto(base() + path(B.slug));
    await settleAny(page);
    const rows = await windowRows(page, true);
    eq(rows.filter((r) => r.group === spec).length, 0, `${name}: a reader is shown the ${spec} keys`);
    await c.close();
  });
}

await kit.test("the timeline: the keys it lists work where the buttons are, and a reader is not offered them", async () => {
  const { c, page } = await platform({ label: "timeline" });
  // the keys are for the book's own order: in story order, or filtered, there are no buttons to move with, and no keys listed
  await page.goto(`${base()}/p/${T.slug}/timeline`);
  await settleAny(page);
  eq((await windowRows(page, true)).filter((r) => r.group === "timeline").length, 0, "in story order the timeline lists no move key");
  const { rows, n } = await walk(page, "timeline", `${base()}/p/${T.slug}/timeline?order=narrative`, ["timeline"], {});
  console.log(`      ${rows.length} rows, ${n} keys pressed`);
  const rp = await platform({ label: "timeline reader", from: reader.ctx });
  await rp.page.goto(`${base()}/p/${T.slug}/timeline?order=narrative`);
  await settleAny(rp.page);
  eq((await windowRows(rp.page, true)).filter((r) => r.group === "timeline").length, 0, "a reader is shown the timeline keys");
  await rp.c.close();
  // the Compendium's own timeline cannot be moved, so it lists none
  const cp = await platform({ label: "compendium timeline" });
  await cp.page.goto(`${base()}/c/${C.slug}/timeline`);
  await settleAny(cp.page);
  eq((await windowRows(cp.page, true)).filter((r) => r.group === "timeline").length, 0, "the Compendium's timeline lists no move key");
  await cp.c.close();
  await c.close();
});

await kit.test("the reading view: every key it lists does something, for the owner", async () => {
  const { c, page } = await platform({ label: "read" });
  const { rows, n } = await walk(page, "read", `${base()}/p/${B.slug}/read/${ids.main}`, ["read"], {});
  console.log(`      ${rows.length} rows, ${n} keys pressed`);
  await c.close();
});

await kit.test("the library: the palette, the help key and Esc do what they say, and Mod+K outside the editor opens the palette", async () => {
  const { c, page } = await platform({ label: "library" });
  const { rows } = await walk(page, "library", `${base()}/`, [], {});
  eq(rows.map((r) => r.id).sort().join(","), "esc,help,palette,palette_k", "the library lists only the application's keys");
  await c.close();
});

await kit.finish("e2e-shortcuts-keys");
