#!/usr/bin/env node
// The keyboard shortcuts window (F-1250 to F-1252), in a real Chromium.
//
//   node scripts/e2e-shortcuts.mjs [--only substring] [--shots DIR]
//
// It opens from the header icon, from ? and from the palette; it does not open from a ? typed in the editor, the Markdown source or
// a text field; the focus stays inside it and goes back to where it came from; a phone reaches it through the account menu with the
// keyboard alone, in one column that scrolls inside the dialog; the caps are the Mac's signs or Ctrl, Shift and Alt by the page's own
// navigator.platform; both themes read well; and what the window says is what the page does: every Alt+Shift key in it is claimed in
// scriobh.chords and every claim is in it.
import { mkdirSync } from "node:fs";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startFake } from "./fake-ai.mjs";

const kit = await createKit("e2e-shortcuts", { env: { SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000" } });
kit.deadline(8 * 60 * 1000);
const arg = (n) => (process.argv.includes(n) ? process.argv[process.argv.indexOf(n) + 1] : "");
kit.only = arg("--only");
const shots = arg("--shots");
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const fake = await startFake();
const wren = await kit.signedIn();
const ada = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "admin@dev.scriobh");
const main = wren.ctx, pg = wren.page;
kit.main = pg;
let book, bookAI, doc, docAI;
const TEXT = "The harbour was very quiet. Mara counted the boats twice.\n\nThe tide turned.\n";

const dialog = (page) => page.locator("#dialog");
const opened = (page) => page.evaluate(() => !!document.querySelector("#dialog[open] .kb"));
const waitOpen = (page, what) => waitFor(() => opened(page), what);
const inDialog = (page) => page.evaluate(() => { const a = document.activeElement; return !!(a && a.closest && a.closest("#dialog[open]")); });
const activeDesc = (page) => page.evaluate(() => { const a = document.activeElement; return a ? (a.id ? "#" + a.id : "") + "|" + a.tagName + "|" + (a.className || "") + "|" + (a.getAttribute("aria-label") || a.dataset.cmd || a.textContent.slice(0, 30)) : "none"; });
const openWrite = async (page, p, d) => {
  await page.goto(`${base()}/p/${p.slug}/d/${d}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.tools && window.scriobh.alts)), "the page's scripts to load", 10000);
  await sleep(150);
};
const noSideways = async (page, what) => eq(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, `${what}: no sideways scroll`);
// A context for another platform, signed in as the same person; the page reads navigator.platform.
const platformContext = async (platform, opts = {}) => {
  const c = await kit.newContext({ viewport: { width: 1280, height: 800 }, ...opts });
  await c.addCookies(await main.cookies());
  await c.addInitScript((p) => { Object.defineProperty(navigator, "platform", { get: () => p }); }, platform);
  const page = await c.newPage();
  kit.watch(page, platform);
  return { c, page };
};
const caps = (page, row) => page.evaluate((r) => [...document.querySelectorAll(`#dialog .kb-row[data-row="${r}"] .kb-alt:not([hidden])`)].map((a) => [...a.querySelectorAll("kbd")].map((k) => (k.querySelector("[aria-hidden]") || k).textContent).join("|")), row);
// WCAG contrast of an element's text on what is behind it.
const contrast = (page, sel) => page.evaluate((s) => {
  const parse = (c) => { const m = c.match(/rgba?\(([^)]+)\)/); const v = m[1].split(/[ ,\/]+/).filter(Boolean).map(Number); return { r: v[0], g: v[1], b: v[2], a: v.length > 3 ? v[3] : 1 }; };
  const lum = (c) => { const f = (x) => { x /= 255; return x <= 0.03928 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4); }; return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b); };
  const el = document.querySelector(s);
  if (!el) return -1;
  let bg = null;
  for (let e = el; e && !bg; e = e.parentElement) { const c = parse(getComputedStyle(e).backgroundColor); if (c.a > 0.99) bg = c; }
  const fg = parse(getComputedStyle(el).color);
  const a = lum(fg), b = lum(bg || { r: 255, g: 255, b: 255 });
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}, sel);

await kit.test("setup: a provider that stays local, a book with AI off and a book with AI on, and a chapter in each", async () => {
  const pv = await api(ada.ctx, "POST", "/api/v1/ai/providers", { kind: "openai_compatible", name: "Fake", base_url: fake.base, local: true, default_model: "fake-model", scope: "instance" });
  ok(pv.status < 300, "provider: " + JSON.stringify(pv.json));
  const mk = async (title) => {
    const p = (await api(main, "POST", "/api/v1/projects", { title, kind: "novel" })).json.item;
    const d = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.order[0];
    const cur = (await api(main, "GET", `/api/v1/docs/${d}`)).json.item;
    const put = await api(main, "PATCH", `/api/v1/docs/${d}`, { base_revision: cur.revision, title: "Harbour", content: TEXT });
    ok(put.status === 200, "seed: " + put.status);
    return [p, d];
  };
  [book, doc] = await mk("Keys plain");
  ok((await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Second", kind: "chapter", content: "More words.\n" })).status < 300, "a second chapter, so that there is a next one");
  [bookAI, docAI] = await mk("Keys with AI");
  const pol = await api(main, "PUT", `/api/v1/projects/${bookAI.id}/ai/policy`, { mode: "ask", providers: [pv.json.item.id], actions: ["selection", "alternatives", "chat", "critique", "summaries", "continuity", "extraction", "lab"] });
  ok(pol.status < 300, "policy: " + JSON.stringify(pol.json));
});

await kit.test("the header icon is there at 1280 px, named, and opens the window with the page's own sections first; Esc closes it and focus goes back to the icon", async () => {
  await pg.goto(`${base()}/`);
  await pg.waitForSelector("#palette", { state: "attached" });
  await waitFor(() => pg.evaluate(() => !!window.scriobh && !!window.scriobh.palette), "the palette script");
  const icon = pg.locator(".topbar .kb-open");
  eq(await icon.isVisible(), true, "the icon is visible");
  eq(await icon.getAttribute("aria-label"), "Keyboard shortcuts", "its name");
  eq(await icon.getAttribute("aria-keyshortcuts"), "Shift+/", "its key, in ARIA's words (a ? is Shift and the slash)");
  eq(await icon.evaluate((e) => e.nextElementSibling.matches("[data-palette-open]")), true, "it sits before the palette hint");
  eq(await pg.locator("#dialog").evaluate((d) => d.open), false, "closed to begin with");
  // nothing is fetched until it is asked for
  const asked = [];
  const code = [];
  pg.on("request", (r) => { if (r.url().includes("/ui/shortcuts")) asked.push(r.url()); else if (/shortcuts[^/]*\.js/.test(r.url())) code.push(r.url()); });
  eq(code.length, 0, "the window's script has not been fetched with the page");
  await icon.focus();
  await pg.keyboard.press("Enter");
  await waitOpen(pg, "the window to open from the icon");
  eq(asked.length, 1, "one request, made on first use");
  eq(code.length, 1, "and the window's script was fetched then, once");
  eq(await pg.locator("#dialog").getAttribute("aria-labelledby"), "dialog-title", "the dialog is named by its title");
  eq(await pg.locator("#dialog-title").innerText(), "Keyboard shortcuts", "its title");
  eq(await activeDesc(pg), "#dialog-title|H2||Keyboard shortcuts", "focus is on the title");
  const heads = await pg.locator("#dialog .kb-group h3").evaluateAll((h) => h.map((x) => x.textContent.replace(/\s+/g, " ").trim()));
  ok(/^Palette and help On this page$/.test(heads[0]), "the library's own section is first, marked: " + heads[0]);
  ok(heads.length >= 5 && !heads.some((h, i) => i > 0 && /On this page/.test(h)), "and only that one is marked: " + heads.join(" / "));
  ok((await pg.locator("#dialog .kb-lede").innerText()).includes("Ctrl on Windows and Linux, Command on a Mac."), "the lede says which key is which");
  await pg.keyboard.press("Escape");
  await waitFor(async () => !(await opened(pg)), "Esc to close it");
  eq(await activeDesc(pg), await icon.evaluate((e) => (e.id ? "#" + e.id : "") + "|" + e.tagName + "|" + e.className + "|" + e.getAttribute("aria-label")), "focus is back on the icon");
  // the second time it comes from the page's memory
  await icon.click();
  await waitOpen(pg, "the window again");
  eq(asked.length, 1, "the second opening asks for nothing");
  eq(code.length, 1, "nor for the script");
  await pg.keyboard.press("Escape");
});

await kit.test("? opens it from the library and from the outline (the outline's own keys first), and not while typing", async () => {
  await pg.goto(`${base()}/`);
  await waitFor(() => pg.evaluate(() => !!window.scriobh && !!window.scriobh.palette), "the palette script");
  await pg.locator("main").click({ position: { x: 5, y: 5 } });
  await pg.keyboard.press("Shift+Slash");
  await waitOpen(pg, "? to open it in the library");
  await pg.keyboard.press("Escape");
  await waitFor(async () => !(await opened(pg)), "close");
  await pg.goto(`${base()}/p/${book.slug}/outline`);
  await waitFor(() => pg.evaluate(() => !!window.scriobh && !!window.scriobh.palette), "the palette script");
  await pg.keyboard.press("Shift+Slash");
  await waitOpen(pg, "? to open it in the outline");
  const first = await pg.locator("#dialog .kb-group h3").first().innerText();
  ok(/^Outline/.test(first) && /On this page/.test(first), "the outline's keys come first: " + first);
  ok((await pg.locator("#dialog .kb-row").count()) > 40, "and the rest follow");
  await pg.keyboard.press("Escape");
  await waitFor(async () => !(await opened(pg)), "close");
  // a text field: the search page's box takes the question mark
  await pg.goto(`${base()}/search`);
  await waitFor(() => pg.evaluate(() => !!window.scriobh && !!window.scriobh.palette), "the palette script");
  const box = pg.locator("main input[type=search], main input[name=q], main input[type=text]").first();
  await box.click();
  await pg.keyboard.type("why?");
  eq(await box.inputValue(), "why?", "the ? is typed into a text box");
  eq(await opened(pg), false, "and does not open the window");
  // the palette's own box
  await pg.keyboard.press(`${MOD}+k`);
  await pg.locator("#palette-input").waitFor();
  await pg.keyboard.type("?");
  eq(await pg.locator("#palette-input").inputValue(), "?", "the palette's box takes it too");
  eq(await opened(pg), false, "the window stays shut");
  await pg.keyboard.press("Escape");
});

await kit.test("in the editor and in the Markdown source a ? is a question mark; the palette and the icon are the way in", async () => {
  await openWrite(pg, book, doc);
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${MOD}+End`);
  await pg.keyboard.type(" Why?");
  await sleep(150);
  eq(await opened(pg), false, "the window did not open from the editor");
  eq(await pg.evaluate(() => window.scriobh.write.editor.getMarkdown().includes("Why?")), true, "the ? is in the text");
  await pg.evaluate(() => window.scriobh.write.editor.toggleSource());
  const src = pg.locator(".scriobh-source:not([hidden])");
  await src.waitFor();
  await src.click();
  await pg.keyboard.type("Really?");
  eq(await opened(pg), false, "nor from the Markdown source");
  eq(await src.inputValue().then((v) => v.includes("Really?")), true, "where it is typed");
  await pg.evaluate(() => window.scriobh.write.editor.toggleSource());
  await pg.locator(".scriobh-prose").click();
  // the palette: Mod+Shift+P inside the editor
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("keyboard");
  const row = pg.locator("#palette-list [role=option]").first();
  const t = await row.innerText();
  ok(t.includes("Keyboard shortcuts") && t.trim().endsWith("?"), "the palette lists it, with its key: " + t);
  await pg.keyboard.press("Enter");
  await waitOpen(pg, "the palette command to open it");
  const heads = await pg.locator("#dialog .kb-group h3").evaluateAll((h) => h.map((x) => x.textContent.replace(/\s+/g, " ").trim()));
  ok(/^Writing and format On this page$/.test(heads[0]), "the writing view's keys come first: " + heads.join(" / "));
  ok(!heads.some((h) => /Writing partner/.test(h)), "no writing partner while AI is off");
  await pg.keyboard.press("Escape");
  await waitFor(async () => !(await opened(pg)), "close");
  eq(await pg.evaluate(() => document.activeElement.classList.contains("scriobh-prose")), true, "focus is back in the text");
  // the icon, with the caret in the text
  await pg.locator(".topbar .kb-open").click();
  await waitOpen(pg, "the icon to open it on the writing page");
  await pg.keyboard.press("Escape");
  await waitFor(async () => !(await opened(pg)), "close");
  eq(await pg.evaluate(() => window.scriobh.write.editor.getMarkdown().includes("Why?")), true, "the text is as it was");
});

await kit.test("focus stays inside it, Tab and Shift+Tab wrap, and the background cannot be reached", async () => {
  await pg.goto(`${base()}/`);
  await waitFor(() => pg.evaluate(() => !!window.scriobh && !!window.scriobh.palette), "the palette script");
  await pg.locator(".topbar .kb-open").click();
  await waitOpen(pg, "open");
  for (let i = 0; i < 6; i++) { await pg.keyboard.press("Tab"); ok(await inDialog(pg), `Tab ${i + 1} left the dialog: ${await activeDesc(pg)}`); }
  for (let i = 0; i < 8; i++) { await pg.keyboard.press("Shift+Tab"); ok(await inDialog(pg), `Shift+Tab ${i + 1} left the dialog: ${await activeDesc(pg)}`); }
  eq(await pg.evaluate(() => { const b = document.querySelector("main"); return b.closest("[inert]") !== null || !!document.querySelector("dialog[open]:modal"); }), true, "the page behind is out of reach");
  eq(await pg.locator("#dialog .dialog-close").evaluate((b) => b.getAttribute("aria-label")), "Close", "the Close button is named");
  await pg.locator("#dialog .dialog-close").click();
  await waitFor(async () => !(await opened(pg)), "Close to close it");
  eq(await pg.evaluate(() => document.activeElement.classList.contains("kb-open")), true, "focus returns to the icon after a click on Close");
  // a click on the backdrop closes it too
  await pg.locator(".topbar .kb-open").click();
  await waitOpen(pg, "open again");
  await pg.mouse.click(5, 400);
  await waitFor(async () => !(await opened(pg)), "a click outside to close it");
});

await kit.test("the words come from the catalogue: every row has its words, and no key appears that the registry does not have", async () => {
  await openWrite(pg, bookAI, docAI);
  await pg.keyboard.press("Escape");
  await pg.locator(".topbar .kb-open").click();
  await waitOpen(pg, "open with AI on");
  const rows = await pg.locator("#dialog .kb-row").evaluateAll((r) => r.map((x) => ({ id: x.dataset.row, words: x.querySelector("dt").textContent.trim(), keys: x.querySelectorAll("kbd").length })));
  ok(rows.length >= 55, "rows: " + rows.length);
  ok(rows.every((r) => r.words && !/^kb\./.test(r.words) && r.keys > 0), "a row has no words or no keys: " + JSON.stringify(rows.find((r) => !r.words || /^kb\./.test(r.words) || !r.keys)));
  const heads = await pg.locator("#dialog .kb-group h3").evaluateAll((h) => h.map((x) => x.textContent.replace(/\s+/g, " ").trim()));
  ok(heads.some((h) => /^Writing partner On this page$/.test(h)), "the writing partner's keys are there when AI is on: " + heads.join(" / "));
  eq(heads.indexOf("Palette and help"), heads.length - 1, "the application's keys come last in the writing view");
  const none = await pg.evaluate(() => [...document.querySelectorAll("#dialog *")].filter((e) => e.hasAttribute("style")).length);
  eq(none, 0, "no style attribute in the dialog");
  // what it says is what the page does: every Alt+Shift key in it is claimed, and every claim is in it
  const said = await pg.evaluate(() => [...document.querySelectorAll("#dialog .kb-alt")].map((a) => [...a.querySelectorAll("kbd")]).filter((k) => k.length === 3 && k[0].dataset.k === "Alt" && k[1].dataset.k === "Shift").map((k) => "Key" + k[2].textContent));
  // the writing partner's second chord is claimed when its script arrives, on first use: use it, as a writer does
  await pg.keyboard.press("Escape");
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press("Alt+Shift+KeyI");
  await pg.locator("dialog.ai-menu").waitFor();
  await waitFor(() => pg.evaluate(() => !!window.scriobh.chords.KeyJ), "ai.js to claim its chord");
  await pg.keyboard.press("Escape");
  await pg.locator(".topbar .kb-open").click();
  await waitOpen(pg, "the window again");
  const claimed = await pg.evaluate(() => Object.keys(window.scriobh.chords).filter((k) => k !== "clash"));
  eq(await pg.evaluate(() => window.scriobh.chords.clash.length), 0, "no chord is claimed twice");
  eq(said.slice().sort().join(","), claimed.slice().sort().join(","), "the Alt+Shift keys of the window and the keys the page claims are the same set (AI on)");
  ok(said.length === 11, "eleven of them (the Compendium tab's and the Comments tab's are two): " + said.join(","));
  await pg.keyboard.press("Escape");
  await openWrite(pg, book, doc);
  await pg.locator(".topbar .kb-open").click();
  await waitOpen(pg, "open with AI off");
  const said0 = await pg.evaluate(() => [...document.querySelectorAll("#dialog .kb-alt")].map((a) => [...a.querySelectorAll("kbd")]).filter((k) => k.length === 3 && k[0].dataset.k === "Alt" && k[1].dataset.k === "Shift").map((k) => "Key" + k[2].textContent));
  const claimed0 = await pg.evaluate(() => Object.keys(window.scriobh.chords).filter((k) => k !== "clash"));
  eq(said0.slice().sort().join(","), claimed0.slice().sort().join(","), "and the same with AI off, where the writing partner has no claim and no row");
  await pg.keyboard.press("Escape");
});

await kit.test("a Mac sees the command, shift and option signs; every other computer Ctrl, Shift and Alt; the palette and the bar follow", async () => {
  for (const [platform, mod, shift, alt] of [["MacIntel", "⌘", "⇧", "⌥"], ["Win32", "Ctrl", "Shift", "Alt"], ["Linux x86_64", "Ctrl", "Shift", "Alt"]]) {
    const { c, page } = await platformContext(platform);
    await openWrite(page, book, doc);
    await page.locator(".topbar .kb-open").click();
    await waitOpen(page, `the window on ${platform}`);
    const mac = platform === "MacIntel";
    eq((await caps(page, "bold")).join(","), `${mod}|B`, `${platform}: bold`);
    eq((await caps(page, "redo")).join(","), `${mod}|${shift}|Z,${mod}|Y`, `${platform}: redo has both of its keys`);
    eq((await caps(page, "split")).join(","), `${alt}|${shift}|B`, `${platform}: split chapter`);
    eq((await caps(page, "h2")).join(","), `${mod}|${alt}|2`, `${platform}: heading 2`);
    eq((await caps(page, "chapter_next")).join(","), `${alt}|Page Down`, `${platform}: next chapter is Alt and Page Down (words, not a sign)`);
    eq((await caps(page, "tree_move")).join(","), "↑,↓", `${platform}: arrows`);
    eq((await caps(page, "tree_trash")).join(","), mac ? "Delete,Backspace" : "Delete", `${platform}: the trash key, with Backspace on a Mac only`);
    eq((await caps(page, "palette")).join(","), `${mod}|${shift}|P,${mod}|/`, `${platform}: the palette's keys, which work in the editor too`);
    eq((await caps(page, "palette_k")).join(","), `${mod}|K`, `${platform}: Mod+K, which the editor keeps for a link`);
    eq((await caps(page, "link")).join(","), `${mod}|K`, `${platform}: and the link is the same key`);
    eq((await caps(page, "help")).join(","), "?", `${platform}: the help key`);
    await page.keyboard.press("Escape");
    await waitFor(async () => !(await opened(page)), "close");
    // the tooltips and the palette
    const tip = (cmd) => page.locator(`#fmtbar [data-cmd="${cmd}"]`).getAttribute("title");
    eq(await tip("bold"), mac ? "Bold (⌘B)" : "Bold (Ctrl+B)", `${platform}: Bold's tooltip`);
    eq(await page.locator('#revise-menu [data-cmd="split"] .menu-keys').textContent(), mac ? "⌥⇧B" : "Alt+Shift+B", `${platform}: split's key in its menu item`);
    eq(await tip("redo"), mac ? "Redo (⌘⇧Z / ⌘Y)" : "Redo (Ctrl+Shift+Z / Ctrl+Y)", `${platform}: Redo's tooltip`);
    eq(await page.locator("#st-source kbd").innerText(), mac ? "⌘E" : "Ctrl+E", `${platform}: the status bar's Source key`);
    await page.locator(".scriobh-prose").click();
    await page.keyboard.press(`${MOD}+Shift+p`);
    await page.locator("#palette-input").fill("go to chapter");
    ok((await page.locator("#palette-list [role=option]").first().innerText()).trim().endsWith(mac ? "⌘O" : "Ctrl+O"), `${platform}: the palette's hint for the switcher`);
    await page.locator("#palette-input").fill("split");
    ok((await page.locator("#palette-list [role=option]").first().innerText()).trim().endsWith(mac ? "⌥⇧B" : "Alt+Shift+B"), `${platform}: the palette's hint for the split`);
    await page.keyboard.press("Escape");
    await c.close();
  }
});

await kit.test("it is the window's job to say the same on every page family: every signed-in page has the icon and the account line, and the reading view puts its keys first", async () => {
  const seen = [];
  for (const [path, first] of [[`/`, "Palette and help"], [`/settings`, "Palette and help"], [`/studio`, "Palette and help"], [`/p/${book.slug}/read`, "Reading and review"], [`/p/${book.slug}/cards`, "Corkboard"], [`/p/${book.slug}/board`, "Status board"]]) {
    await pg.goto(base() + path);
    await waitFor(() => pg.evaluate(() => !!window.scriobh && !!window.scriobh.palette), `the palette script on ${path}`);
    ok((await pg.locator(".topbar .kb-open").count()) === 1 && (await pg.locator("#account [data-kb-open]").count()) === 1, `${path}: the icon and the account line`);
    if (path.endsWith("/read")) await pg.locator("body").click({ position: { x: 3, y: 300 } });
    await pg.keyboard.press("Shift+Slash");
    await waitOpen(pg, `? on ${path}`);
    const h = await pg.locator("#dialog .kb-group h3").first().innerText();
    ok(h.startsWith(first) && /On this page/.test(h), `${path}: starts with ${first}, got ${h}`);
    seen.push(path);
    await pg.keyboard.press("Escape");
    await waitFor(async () => !(await opened(pg)), "close");
  }
  eq(seen.length, 6, "all of them");
});

await kit.test("the reading view's keys are all there (F-701) and the review keys match the handler's", async () => {
  await pg.goto(`${base()}/p/${book.slug}/read`);
  await waitFor(() => pg.evaluate(() => !!window.scriobh && !!window.scriobh.palette), "the palette script");
  await pg.keyboard.press("Shift+Slash");
  await waitOpen(pg, "open in the reading view");
  const rows = await pg.locator("#dialog .kb-group.is-here .kb-row").evaluateAll((r) => r.map((x) => x.dataset.row));
  for (const want of ["read_next", "read_prev", "review_caret", "review_select", "review_comment", "review_suggest", "review_margin", "review_thread", "review_show", "review_reply", "review_resolve", "review_next", "review_leave"]) ok(rows.includes(want), `the reading section lacks ${want}: ${rows.join(",")}`);
  eq((await caps(pg, "review_caret")).join(","), "]," + "[", "the paragraph keys");
  eq((await caps(pg, "read_next")).join(","), "→,J", "the page-turning keys");
  await pg.keyboard.press("Escape");
});

await kit.test("when the window cannot be fetched a toast says so, nothing opens, and the page is left alone", async () => {
  const { c, page } = await platformContext("Win32");
  kit.allowed.push(/ERR_FAILED/); // the request this step cuts
  await page.route("**/ui/shortcuts*", (r) => r.abort());
  await openWrite(page, book, doc);
  await page.locator(".scriobh-prose").click();
  await page.keyboard.press(`${MOD}+End`);
  await page.keyboard.type(" Text kept.");
  await page.locator(".topbar .kb-open").click();
  await waitFor(async () => /could not be loaded/.test(await page.locator(".toast").last().innerText().catch(() => "")), "the toast");
  eq(await opened(page), false, "no window");
  eq(await page.url().includes("/d/"), true, "still on the chapter");
  eq(await page.evaluate(() => window.scriobh.write.editor.getMarkdown().includes("Text kept.")), true, "the text is there");
  await page.unroute("**/ui/shortcuts*");
  // a redirect to the sign-in page (an expired session) is not put into the dialog either
  await page.route("**/ui/shortcuts*", (r) => r.fulfill({ status: 303, headers: { Location: "/auth/login" } }));
  await page.locator(".topbar .kb-open").click();
  await sleep(600);
  eq(await opened(page), false, "a sign-in redirect opens nothing");
  eq(await page.locator("#dialog-body").innerHTML(), "", "and puts nothing in the dialog");
  eq(page.url().includes("/auth/login"), false, "and does not navigate away from the chapter");
  await c.close();
});

await kit.test("at 390 px on a phone: no icon, and with the keyboard alone the account menu leads to a one-column window that scrolls inside itself", async () => {
  const { c, page } = await platformContext("iPhone", { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
  await openWrite(page, book, doc);
  eq(await page.locator(".topbar .kb-open").isVisible(), false, "the icon is not shown");
  // Tab to the account button and open its menu with Enter; the menu focuses its first line; Tab to the shortcuts line
  await page.locator(".scriobh-prose").blur().catch(() => {});
  await page.evaluate(() => document.activeElement && document.activeElement.blur());
  let at = "";
  for (let i = 0; i < 60 && !/popovertarget/.test(at); i++) { await page.keyboard.press("Tab"); at = await page.evaluate(() => { const a = document.activeElement; return a && a.hasAttribute("popovertarget") ? "popovertarget" + (a.offsetParent ? "" : "-hidden") : ""; }); }
  ok(/popovertarget/.test(at), "Tab reaches the account button");
  const opener = await activeDesc(page);
  await page.keyboard.press("Enter");
  await waitFor(() => page.evaluate(() => document.querySelector("#account:popover-open") !== null), "the menu");
  let item = "";
  const path = [];
  for (let i = 0; i < 40 && item !== "kb"; i++) { item = await page.evaluate(() => (document.activeElement && document.activeElement.hasAttribute("data-kb-open") ? "kb" : "")); if (item !== "kb") { path.push(await activeDesc(page)); await page.keyboard.press("Tab"); } }
  eq(item, "kb", "Tab reaches the Keyboard shortcuts line, through " + path.join(" > "));
  await page.keyboard.press("Enter");
  await waitOpen(page, "the window from the account menu");
  eq(await page.evaluate(() => document.querySelector("#account:popover-open") === null), true, "the menu closed");
  const lay = await page.evaluate(() => {
    const d = document.querySelector("#dialog"), g = [...d.querySelectorAll(".kb-group")], r = d.getBoundingClientRect();
    const x = g.map((s) => Math.round(s.getBoundingClientRect().left));
    return { cols: new Set(x).size, left: x[0], dw: d.clientWidth, dsw: d.scrollWidth, dh: d.clientHeight, dsh: d.scrollHeight, w: Math.round(r.width), bodyW: document.documentElement.scrollWidth, vw: document.documentElement.clientWidth, closeW: document.querySelector("#dialog .dialog-close").getBoundingClientRect().width, closeH: document.querySelector("#dialog .dialog-close").getBoundingClientRect().height };
  });
  eq(lay.cols, 1, "one column");
  ok(lay.dsw <= lay.dw, `the dialog does not scroll sideways: ${lay.dsw} in ${lay.dw}`);
  ok(lay.dsh > lay.dh, `it scrolls inside itself: ${lay.dsh} in ${lay.dh}`);
  ok(lay.bodyW <= lay.vw, "the page does not scroll sideways");
  ok(lay.w <= 390 - 8, "and it fits the screen: " + lay.w);
  ok(lay.closeW >= 44 && lay.closeH >= 44, `the Close button is a 44 px target: ${lay.closeW} by ${lay.closeH}`);
  const rowBox = await page.evaluate(() => { const r = document.querySelector("#dialog .kb-row"); return { w: r.getBoundingClientRect().width, sw: r.scrollWidth }; });
  ok(rowBox.sw <= Math.ceil(rowBox.w), "a row does not overflow its box");
  // the keyboard scrolls it (End), and Esc closes it with focus back on the account button
  await page.keyboard.press("Tab");
  await page.evaluate(() => { document.querySelector("#dialog").scrollTop = 0; });
  await page.locator("#dialog .kb-group").last().scrollIntoViewIfNeeded();
  ok(await page.evaluate(() => document.querySelector("#dialog").scrollTop > 0), "the dialog scrolled");
  if (shots) { await sleep(500); await page.screenshot({ path: `${shots}/phone-light-bottom.png` }); }
  await page.keyboard.press("Escape");
  await waitFor(async () => !(await opened(page)), "Esc to close it");
  eq(await activeDesc(page), opener, "focus is back on the account button that opened the menu");
  await c.close();
});

await kit.test("both themes: the window reads well in Paper and Ink at 1280 and 390 px (contrast of its words, keys and marks; screenshots with --shots)", async () => {
  for (const scheme of ["light", "dark"]) {
    for (const vp of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
      const { c, page } = await platformContext("Win32", { viewport: vp, colorScheme: scheme, hasTouch: vp.width < 600, isMobile: vp.width < 600 });
      await openWrite(page, bookAI, docAI);
      await page.evaluate(() => document.activeElement && document.activeElement.blur());
      await page.keyboard.press("Shift+Slash");
      await waitOpen(page, `${scheme} ${vp.width}`);
      await sleep(450); // the dialog fades in: a colour read during it is a blend
      const theme = await page.evaluate(() => getComputedStyle(document.querySelector("#dialog")).backgroundColor);
      for (const [sel, min, what] of [[".kb-row dt", 4.5, "the words"], [".kb kbd", 4.5, "a key cap"], [".kb-lede", 4.5, "the lede"], [".kb-here", 4.5, "the mark of this page"], [".kb-or", 4.5, "the word or"], [".kb-group h3", 4.5, "a heading"]]) {
        const r = await contrast(page, sel);
        ok(r >= min, `${scheme} ${vp.width}: ${what} has contrast ${r.toFixed(2)} (want ${min}) on ${theme}`);
      }
      eq(await page.evaluate(() => /gradient/.test(getComputedStyle(document.querySelector("#dialog")).backgroundImage + getComputedStyle(document.querySelector(".kb kbd")).backgroundImage)), false, "no gradient");
      await noSideways(page, `${scheme} ${vp.width}`);
      if (vp.width > 1000) {
        const cols = await page.evaluate(() => new Set([...document.querySelectorAll("#dialog .kb-group")].map((s) => Math.round(s.getBoundingClientRect().left))).size);
        eq(cols, 2, "two columns on a wide screen");
      }
      if (shots) { await sleep(500); await page.screenshot({ path: `${shots}/${scheme}-${vp.width}.png` }); } // after the dialog's fade
      await c.close();
    }
  }
});

await kit.test("another keyboard layout: the Alt+Shift keys are drawn with the letters under them, in the window, the tooltips and the palette; with no layout map the US letters", async () => {
  for (const platform of ["Win32", "MacIntel"]) {
    const c = await kit.newContext({ viewport: { width: 1280, height: 800 } });
    await c.addCookies(await main.cookies());
    // a layout on which the key that is B on a US keyboard makes X, and the A key makes Q
    await c.addInitScript((p) => {
      Object.defineProperty(navigator, "platform", { get: () => p });
      Object.defineProperty(navigator, "keyboard", { get: () => ({ getLayoutMap: () => Promise.resolve(new Map([["KeyB", "x"], ["KeyA", "q"]])) }) });
    }, platform);
    const page = await c.newPage();
    kit.watch(page, "layout " + platform);
    await openWrite(page, book, doc);
    await waitFor(() => page.evaluate(() => /X/.test(document.querySelector('#revise-menu [data-cmd="split"] .menu-keys').textContent)), "the key to follow the layout");
    const mac = platform === "MacIntel";
    eq(await page.locator('#revise-menu [data-cmd="split"] .menu-keys').textContent(), mac ? "⌥⇧X" : "Alt+Shift+X", `${platform}: the split key in its menu item`);
    eq(await page.locator('#revise-menu [data-cmd="alt"] .menu-keys').textContent(), mac ? "⌥⇧Q" : "Alt+Shift+Q", `${platform}: the alternative's key in its menu item`);
    eq(await page.locator('#revise-menu [data-cmd="ghost"] .menu-keys').textContent(), mac ? "⌥⇧G" : "Alt+Shift+G", `${platform}: a key the layout map does not change keeps its letter`);
    await page.locator(".topbar .kb-open").click();
    await waitOpen(page, "the window");
    eq((await caps(page, "split")).join(","), mac ? "⌥|⇧|X" : "Alt|Shift|X", `${platform}: split in the window`);
    eq((await caps(page, "alt_add")).join(","), mac ? "⌥|⇧|Q" : "Alt|Shift|Q", `${platform}: the alternative in the window`);
    eq((await caps(page, "stash")).join(","), mac ? "⌥|⇧|S" : "Alt|Shift|S", `${platform}: an unmapped letter stays`);
    await page.keyboard.press("Escape");
    await waitFor(async () => !(await opened(page)), "close");
    await page.locator(".scriobh-prose").click();
    await page.keyboard.press(`${MOD}+Shift+p`);
    await page.locator("#palette-input").fill("split");
    ok((await page.locator("#palette-list [role=option]").first().innerText()).trim().endsWith(mac ? "⌥⇧X" : "Alt+Shift+X"), `${platform}: the palette's hint`);
    await page.keyboard.press("Escape");
    await c.close();
  }
});

await kit.test("a Mac names its signs in a tooltip and keeps a sign apart from a word key", async () => {
  const { c, page } = await platformContext("MacIntel");
  await openWrite(page, book, doc);
  await page.locator(".topbar .kb-open").click();
  await waitOpen(page, "the window");
  eq(await page.locator('#dialog .kb-row[data-row="split"] kbd[data-k]').evaluateAll((k) => k.map((x) => x.title).join(",")), "Option,Shift", "the option and shift signs are named");
  eq(await page.locator('#dialog .kb-row[data-row="bold"] kbd[data-k]').first().getAttribute("title"), "Command", "the command sign is named");
  await page.keyboard.press("Escape");
  await waitFor(async () => !(await opened(page)), "close");
  await page.locator(".scriobh-prose").click();
  await page.keyboard.press(`${MOD}+Shift+p`);
  await page.locator("#palette-input").fill("next chapter");
  const t = (await page.locator("#palette-list [role=option]").first().innerText()).trim();
  ok(t.endsWith("⌥ Page Down"), "the option sign is apart from the word: " + t);
  await page.keyboard.press("Escape");
  await c.close();
});

await kit.test("? made with AltGr (Control and Alt together on Windows) opens it; Control and Alt without AltGr do not", async () => {
  await pg.goto(`${base()}/`);
  await waitFor(() => pg.evaluate(() => !!window.scriobh && !!window.scriobh.palette), "the palette script");
  await pg.locator("main").click({ position: { x: 5, y: 5 } });
  await pg.evaluate(() => document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "?", ctrlKey: true, altKey: true, bubbles: true, cancelable: true })));
  await sleep(300);
  eq(await opened(pg), false, "Control and Alt, with no AltGr, are a chord of someone else's");
  await pg.evaluate(() => document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "?", ctrlKey: true, altKey: true, modifierAltGraph: true, bubbles: true, cancelable: true })));
  await waitOpen(pg, "AltGr and ? to open it");
  await pg.keyboard.press("Escape");
});

await kit.test("a browser with no :popover-open selector still opens it", async () => {
  const c = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  await c.addCookies(await main.cookies());
  await c.addInitScript(() => {
    const q = Document.prototype.querySelectorAll;
    Document.prototype.querySelectorAll = function (s) { if (/popover-open/.test(s)) throw new SyntaxError("not a selector"); return q.call(this, s); };
  });
  const page = await c.newPage();
  kit.watch(page, "no popover selector");
  await page.goto(`${base()}/`);
  await waitFor(() => page.evaluate(() => !!window.scriobh && !!window.scriobh.palette), "the palette script");
  await page.locator(".topbar .kb-open").click();
  await waitOpen(page, "the window with no :popover-open");
  await page.keyboard.press("Escape");
  await c.close();
});

await kit.test("the window and the script do not wake the editor: nothing moves while it opens over a chapter", async () => {
  await openWrite(pg, book, doc);
  await pg.locator(".scriobh-prose").click();
  const box = () => pg.evaluate(() => { const r = document.querySelector(".scriobh-prose").getBoundingClientRect(); return [r.x, r.y, r.width, r.height].map(Math.round).join(","); });
  const before = await box();
  await pg.locator(".topbar .kb-open").click();
  await waitOpen(pg, "open");
  await pg.keyboard.press("Escape");
  await waitFor(async () => !(await opened(pg)), "close");
  eq(await box(), before, "the paper did not move");
});

await kit.finish("e2e-shortcuts");
