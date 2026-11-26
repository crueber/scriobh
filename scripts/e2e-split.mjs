#!/usr/bin/env node
// The chapter break of the writing view (F-1224, F-1225): "Split chapter here" in one action from the rich editor, on the format bar,
// in the palette, from the keyboard (Alt+Shift+B) and from the guard bar of a long chapter, in a real Chromium.
//
//   node scripts/e2e-split.mjs [--only substring] [--shots DIR]
//
// The split takes the caret of the rich editor, has the editor's own serializer name the Markdown place, saves what is typed
// first, and opens the new chapter (to be named) in the rich editor. The Source view is never entered: a watcher in every page
// records the moment the Source textarea is shown. The text of both chapters is compared exactly with what the op makes from the
// text (the head loses the blank lines before the cut, the tail the ones after it).
import { mkdirSync } from "node:fs";
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-split");
kit.deadline(10 * 60 * 1000);
const arg = (n) => (process.argv.includes(n) ? process.argv[process.argv.indexOf(n) + 1] : "");
kit.only = arg("--only");
const shots = arg("--shots");
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep, MOD } = kit;
const mac = process.platform === "darwin", TOP = mac ? "Meta+ArrowUp" : "Control+Home", END = mac ? "Meta+ArrowDown" : "Control+End"; // the start and the end of the text
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

// Every page of the context reports when the Source textarea has been shown (sessionStorage outlives the navigation a split ends with).
await main.addInitScript(() => {
  const arm = () => {
    const t = document.querySelector(".scriobh-source");
    if (!t) return false;
    new MutationObserver(() => { if (!t.hidden) sessionStorage.setItem("__source", "1"); }).observe(t, { attributes: true, attributeFilter: ["hidden"] });
    return true;
  };
  new MutationObserver((_, o) => { if (arm()) o.disconnect(); }).observe(document, { childList: true, subtree: true });
});

const mk = async (title, content) => {
  const made = await api(main, "POST", "/api/v1/projects", { title, kind: "novel" });
  ok(made.json && made.json.item, `project ${title}: ${made.status}`);
  const p = made.json.item, doc = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.order[0];
  const put = await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: 1, title: "Whole", content });
  ok(put.status === 200, `seed ${title}: ${put.status}`);
  return { ...p, doc };
};
const list = async (p) => (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json;
const text = async (id) => (await api(main, "GET", `/api/v1/docs/${id}`)).json.item.content;
const open = async (page, p, id = p.doc) => {
  await page.goto(`${base()}/p/${p.slug}/d/${id}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.split && window.scriobh.alts)), "the split tool to load", 10000);
};
// The caret before `needle` in the document (a position of the editor's own), then focus.
const place = (page, needle, delta = 0) => page.evaluate(([n, d]) => {
  const ed = window.scriobh.write.editor;
  let at = -1;
  ed.view.state.doc.descendants((node, pos) => { if (at < 0 && node.isText) { const i = node.text.indexOf(n); if (i >= 0) at = pos + i + d; } });
  if (at < 0) throw new Error("no such text: " + n);
  ed.alts.select(at, at);
  ed.view.focus();
  return at;
}, [needle, delta]);
const clickSplit = async (page) => { await page.locator('#fmtbar [data-cmd="revise"]').click(); await page.locator('#revise-menu [data-cmd="split"]').click(); };
const toastText = (page) => page.locator(".toast").last().innerText();
const sourceShown = (page) => page.evaluate(() => sessionStorage.getItem("__source") === "1");
// After a split: the page moved to the new chapter, in the rich editor, with its title to be given.
const landed = async (page, p, was, o = {}) => {
  await page.waitForURL((u) => /\/d\//.test(u.pathname) && !u.pathname.endsWith(was), { timeout: 15000 });
  await page.waitForSelector(".scriobh-prose");
  const id = page.url().split("/d/")[1].split("?")[0];
  eq(await page.locator(".scriobh-source").isHidden(), true, "the new chapter opens in the rich editor");
  if (!o.source) eq(await sourceShown(page), false, "the Source view was never shown");
  await waitFor(async () => (await page.evaluate(() => document.activeElement && document.activeElement.id)) === "doc-title", "the new chapter's title to take focus");
  return id;
};
const noCut = async (p, n = 1) => eq((await list(p)).items.length, n, "nothing was split");
const clear = (page) => page.evaluate(() => sessionStorage.removeItem("__source"));

await kit.test("one click on the format bar, from the rich editor, in the middle of a paragraph", async () => {
  const p = await mk("Split one", "Alpha opening paragraph of the chapter.\n\nThe tide had gone out and the boats lay over on their sides in the mud.\n\nOmega closing paragraph.\n");
  await open(pg, p);
  await clear(pg);
  // A real click puts the caret where the writer's pointer is.
  const box = await pg.evaluate(() => { const v = window.scriobh.write.editor.view; let at = -1; v.state.doc.descendants((n, pos) => { if (at < 0 && n.isText && n.text.includes("and the boats")) at = pos + n.text.indexOf("and the boats"); }); const c = v.coordsAtPos(at); return { x: c.left + 1, y: (c.top + c.bottom) / 2 }; });
  await pg.mouse.click(box.x, box.y);
  await pg.getByRole("button", { name: "Revise", exact: true }).click();
  const btn = pg.locator("#revise-menu [data-cmd=\"split\"]");
  eq(await btn.isEnabled(), true, "the item is enabled under 15,000 words");
  const macPage = await pg.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform));
  eq(await btn.locator(".menu-keys").textContent(), macPage ? "⌥⇧B" : "Alt+Shift+B", "the item shows the key, drawn for this platform");
  await btn.click();
  const tail = await landed(pg, p, p.doc);
  eq(await text(p.doc), "Alpha opening paragraph of the chapter.\n\nThe tide had gone out\n", "the first chapter ends where the caret was");
  eq(await text(tail), "and the boats lay over on their sides in the mud.\n\nOmega closing paragraph.\n", "the new chapter begins there");
  const l = await list(p);
  eq(l.items.length, 2, "two chapters");
  eq(l.order[1], tail, "the new chapter follows the first");
});

await kit.test("what is typed and not yet saved is in the first chapter: no word is lost", async () => {
  const p = await mk("Split typed", "Start of it.\n\nMiddle part goes here.\n");
  await open(pg, p);
  await clear(pg);
  await place(pg, "Middle");
  await pg.keyboard.type("Newly typed words. ");
  // No wait: the save loop has not run. The split saves first.
  await clickSplit(pg);
  const tail = await landed(pg, p, p.doc);
  eq(await text(p.doc), "Start of it.\n\nNewly typed words.\n", "the head holds what was typed, the caret was after it");
  eq(await text(tail), "Middle part goes here.\n", "the tail begins at the caret");
});

await kit.test("the palette and Alt+Shift+B do the same, and a held key is one press", async () => {
  const p = await mk("Split palette", "First part.\n\nSecond part begins here and goes on.\n");
  await open(pg, p);
  await clear(pg);
  await place(pg, "begins");
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("split chapter");
  eq((await pg.locator("#palette-list [role=option]").first().innerText()).includes("Split chapter here"), true, "the palette lists the command by the button's words");
  await pg.keyboard.press("Enter");
  const tail = await landed(pg, p, p.doc);
  eq(await text(p.doc), "First part.\n\nSecond part\n", "the palette: the first chapter");
  eq(await text(tail), "begins here and goes on.\n", "the palette split at the caret");
  // The chord, with auto-repeat.
  const q = await mk("Split chord", "One.\n\nTwo is here.\n\nThree is here.\n");
  await open(pg, q);
  await clear(pg);
  await place(pg, "Two");
  await pg.keyboard.down("Alt"); await pg.keyboard.down("Shift");
  await pg.keyboard.press("KeyB"); await pg.keyboard.press("KeyB"); await pg.keyboard.press("KeyB");
  await pg.keyboard.up("Shift"); await pg.keyboard.up("Alt");
  const t2 = await landed(pg, q, q.doc);
  eq(await text(t2), "Two is here.\n\nThree is here.\n", "the chord split at the caret");
  await sleep(500);
  eq((await list(q)).items.length, 2, "one split, not three");
  eq(await pg.evaluate(() => window.scriobh.chords.clash.length), 0, "no chord is claimed twice");
});

await kit.test("edges: the start, the end, a list, a heading, a scene break, a selection", async () => {
  // the very start and the very end: said, and nothing cut
  kit.allowed = [/status of 4\d\d/];
  const a = await mk("Split ends", "Only paragraph one.\n\nOnly paragraph two.\n");
  await open(pg, a);
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(TOP);
  await clickSplit(pg);
  await waitFor(async () => /no text before the cursor/.test(await toastText(pg)), "the start to be said");
  await pg.keyboard.press(END);
  await clickSplit(pg);
  await waitFor(async () => /no text after the cursor/.test(await toastText(pg)), "the end to be said");
  await noCut(a);
  eq(await text(a.doc), "Only paragraph one.\n\nOnly paragraph two.\n", "the text is untouched");
  eq(await pg.locator(".scriobh-source").isHidden(), true, "still the rich editor");
  // a list: before the whole list, never through its markers
  const l = await mk("Split list", "Intro line.\n\n- apples\n- pears\n\nOutro line.\n");
  await open(pg, l);
  await place(pg, "pears", 2);
  await clickSplit(pg);
  // a cut that moves across a whole list is asked about first, and nothing is held or cut while the question is open
  const q1 = pg.locator("#nb-split");
  await q1.waitFor();
  eq(await q1.innerText().then((x) => /The cursor is inside something that cannot be cut through/.test(x) && /just before it/.test(x) && /“apples”/.test(x)), true, "the question says where the cut would go");
  eq(await pg.getByRole("button", { name: "Cancel" }).evaluate((b) => document.activeElement === b), true, "focus is on Cancel");
  await pg.keyboard.press("Escape");
  await q1.waitFor({ state: "detached" });
  await noCut(l);
  await clickSplit(pg);
  await pg.getByRole("button", { name: "Split there" }).click();
  let t = await landed(pg, l, l.doc);
  eq(await text(l.doc), "Intro line.\n", "a list: the first chapter ends before it");
  eq(await text(t), "- apples\n- pears\n\nOutro line.\n", "a list: it begins the new chapter whole");
  await waitFor(async () => (await toastText(pg)) === "Split before the list, which cannot be cut through. This chapter begins with “apples”.", "the new chapter to say where the cut went");
  // a heading
  const h = await mk("Split heading", "Some prose.\n\n## The second part\n\nMore prose.\n");
  await open(pg, h);
  await place(pg, "second", 3);
  await clickSplit(pg);
  t = await landed(pg, h, h.doc);
  await waitFor(async () => (await toastText(pg)) === "Split before the heading. This chapter begins with “The second part”.", "the heading notice");
  eq(await text(h.doc), "Some prose.\n", "a heading: before it");
  eq(await text(t), "## The second part\n\nMore prose.\n", "a heading: it opens the new chapter");
  // a scene break chosen whole: after it
  const s = await mk("Split break", "Closing words.\n\n* * *\n\nOpening words.\n");
  await open(pg, s);
  await pg.locator(".scene-break").click();
  eq(await pg.evaluate(() => !!window.scriobh.write.editor.view.state.selection.node), true, "the scene break is selected whole");
  await clickSplit(pg);
  t = await landed(pg, s, s.doc);
  eq(await text(s.doc), "Closing words.\n\n* * *\n", "a scene break: it stays at the end of the first chapter");
  eq(await text(t), "Opening words.\n", "a scene break: the new chapter begins with prose");
  // a selection: the cut is where it starts and nothing is deleted
  const m = await mk("Split selection", "First.\n\nThe tide had gone out and the boats lay over.\n");
  await open(pg, m);
  await place(pg, "tide");
  for (let i = 0; i < 13; i++) await pg.keyboard.press("Shift+ArrowRight");
  await waitFor(() => pg.evaluate(() => { const s = window.scriobh.write.editor.view.state; return s.doc.textBetween(s.selection.from, s.selection.to) === "tide had gone"; }), "the words to be selected");
  await clickSplit(pg);
  t = await landed(pg, m, m.doc);
  eq(await text(m.doc), "First.\n\nThe\n", "a selection: the cut is at its start (nearest word edge)");
  eq(await text(t), "tide had gone out and the boats lay over.\n", "a selection: nothing deleted");
});

await kit.test("edges: formatting, a ghost, an alternative, and a chapter written elsewhere (Markdown spelt the other way)", async () => {
  kit.allowed = [/status of 422/];
  // inside emphasis: to the edge of it, no stray asterisk
  const e = await mk("Split emphasis", "He said *very quiet words* and went on talking about nothing in particular.\n\nSecond paragraph.\n");
  await open(pg, e);
  await place(pg, "quiet");
  await clickSplit(pg);
  let t = await landed(pg, e, e.doc);
  eq(await text(e.doc), "He said\n", "emphasis: the first chapter ends before the emphasised run");
  eq(await text(t), "*very quiet words* and went on talking about nothing in particular.\n\nSecond paragraph.\n", "emphasis: the run is whole in the new chapter");
  await waitFor(async () => /^Split moved out of the formatting the cursor was in/.test(await toastText(pg)), "the emphasis notice");
  // a ghost: not through it
  const g = await mk("Split ghost", "Alpha <scriobh-ghost>hidden words here</scriobh-ghost> omega and the rest of it.\n\nTail paragraph.\n");
  await open(pg, g);
  await place(pg, "words here");
  await clickSplit(pg);
  t = await landed(pg, g, g.doc);
  eq(await text(g.doc), "Alpha\n", "a ghost: the first chapter ends before it");
  eq(await text(t), "<scriobh-ghost>hidden words here</scriobh-ghost> omega and the rest of it.\n\nTail paragraph.\n", "a ghost: whole in the new chapter");
  // an alternative: the cut inside it is refused in the server's words; nothing changes; outside it, the alternative stays with its text
  const sentence = "The tide had gone out and the boats lay over on their sides.";
  const a = await mk("Split alternative", `Opening line.\n\n${sentence}\n\nClosing line.\n`);
  const made = await api(main, "POST", `/api/v1/docs/${a.doc}/alternatives`, { base_revision: (await api(main, "GET", `/api/v1/docs/${a.doc}`)).json.item.revision, scope: "sentence", quote: sentence, before: "Opening line.\n\n", after: "\n\nClosing line.", variants: ["The water had drawn back and the hulls rested on the mud."] });
  ok(made.status === 201 || made.status === 200, "an alternative was made: " + made.status + JSON.stringify(made.json).slice(0, 200));
  await open(pg, a);
  await place(pg, "and the boats");
  await clickSplit(pg);
  await waitFor(async () => /inside an alternative wording/.test(await toastText(pg)), "the server's refusal to be shown");
  await noCut(a);
  await place(pg, "Closing");
  await clickSplit(pg);
  t = await landed(pg, a, a.doc);
  eq(await text(t), "Closing line.\n", "outside the alternative the cut is made");
  eq(((await api(main, "GET", `/api/v1/docs/${a.doc}/alternatives`)).json.items || []).length, 1, "the alternative stays with its sentence");
  // a chapter written elsewhere (underscores, ---, * bullets): the editor's spelling is stored first, then it is cut
  const w = await mk("Split spelt", "Start _emphasised_ words.\n\n---\n\n* bullet one\n* bullet two\n\nFinal paragraph here.\n");
  await open(pg, w);
  await place(pg, "Final");
  await clickSplit(pg);
  t = await landed(pg, w, w.doc);
  eq(await text(w.doc), "Start *emphasised* words.\n\n* * *\n\n- bullet one\n- bullet two\n", "the first chapter, in the editor's spelling");
  eq(await text(t), "Final paragraph here.\n", "the new chapter");
});

await kit.test("another tab holds the chapter, and the network is gone: nothing is cut, the text is kept, the words say why", async () => {
  kit.allowed = [/status of 409/, /ERR_INTERNET_DISCONNECTED/];
  const p = await mk("Split refused", "Alpha is first.\n\nBravo is second.\n");
  await open(pg, p);
  await place(pg, "Bravo");
  // another tab takes the chapter (the lease is the server's)
  const took = await api(main, "POST", `/api/v1/docs/${p.doc}/lease`, { tab_id: "another-tab-of-mine", take: true });
  ok(took.status === 200, "lease taken: " + took.status);
  await clickSplit(pg);
  await waitFor(async () => /another tab/.test(await toastText(pg)), "the lease to be named");
  await noCut(p);
  eq(await pg.locator(".scriobh-source").isHidden(), true, "still the rich editor");
  // offline: typed words stay, nothing is sent
  const q = await mk("Split offline", "Alpha is first.\n\nBravo is second.\n");
  await open(pg, q);
  await place(pg, "Bravo");
  await pg.keyboard.type("Kept. ");
  await main.setOffline(true);
  await clickSplit(pg);
  await waitFor(async () => (await toastText(pg)) === "Not saved yet. Staying here so nothing is lost.", "the unsaved words to be named");
  await main.setOffline(false);
  await noCut(q);
  eq(await pg.evaluate(() => window.scriobh.write.editor.getMarkdown()), "Alpha is first.\n\nKept. Bravo is second.\n", "the typed words are still in the editor");
  eq(await pg.locator(".scriobh-source").isHidden(), true, "still the rich editor");
  await waitFor(async () => (await text(q.doc)) === "Alpha is first.\n\nKept. Bravo is second.\n", "the words to be saved once the network is back", 20000);
  // nothing waiting to be saved: the request itself fails, and the words say the chapter may or may not have been cut
  await place(pg, "Bravo");
  await main.setOffline(true);
  await clickSplit(pg);
  await waitFor(async () => (await toastText(pg)) === "Could not reach the server, so the chapter may not have been split. Look at the manuscript before trying again.", "the unreachable server to be named");
  await main.setOffline(false);
  await noCut(q);
});

await kit.test("a paragraph after a code fence or an HTML comment: the op takes the cut (it refused one before the line break that ends them)", async () => {
  for (const [name, md, head] of [
    ["fence", "One.\n\n```\ncode\n```\n\nAfter para and more.\n", "One.\n\n```\ncode\n```\n"],
    ["comment", "One.\n\n<!-- c -->\n\nAfter para and more.\n", "One.\n\n<!-- c -->\n"]]) {
    const p = await mk("Split after " + name, md);
    await open(pg, p);
    await place(pg, "After");
    await clickSplit(pg);
    const t = await landed(pg, p, p.doc);
    eq(await text(p.doc), head, `after a ${name}: the first chapter`);
    eq(await text(t), "After para and more.\n", `after a ${name}: the new chapter`);
  }
});

await kit.test("a number, an address, inline math, inline HTML, a hard line break and a script without spaces are cut where they can be", async () => {
  const cases = [
    ["number", "The value was 3.14159 or so, and then more words follow here.\n\nNext.\n", "14159", 0, "The value was\n", "3.14159 or so, and then more words follow here.\n\nNext.\n"],
    ["address", "See https://example.test/a/b_c for the whole story told over again.\n\nNext.\n", "example", 0, "See\n", "https://example.test/a/b_c for the whole story told over again.\n\nNext.\n"],
    ["math", "Let $x +y$ be the sum of both parts here, and then go on talking.\n\nNext.\n", "+y", 0, "Let\n", "$x +y$ be the sum of both parts here, and then go on talking.\n\nNext.\n"],
    ["html", "It was <b>bold words</b> that he said, and then he went on his way.\n\nNext.\n", "words", 0, "It was\n", "<b>bold words</b> that he said, and then he went on his way.\n\nNext.\n"],
    ["hard break", "Line one\\\nline two words here.\n\nEnd.\n", "line two", 0, "Line one\n", "line two words here.\n\nEnd.\n"],
    ["astral", "𝐀𝐁𝐂 words and more follow here.\n\nNext.\n", "𝐀𝐁𝐂", 1, "𝐀𝐁𝐂\n", "words and more follow here.\n\nNext.\n"],
    ["cjk", "这是一个很长的中文段落没有空格而且还在继续。\n\nNext.\n", "段落", 0, "这是一个很长的中文\n", "段落没有空格而且还在继续。\n\nNext.\n"],
    ["cjk, the second character", "这是一个很长的中文段落没有空格而且还在继续。\n\nNext.\n", "是一个", 0, "这\n", "是一个很长的中文段落没有空格而且还在继续。\n\nNext.\n"]];
  for (const [name, md, needle, delta, head, tail] of cases) {
    const p = await mk("Split " + name, md);
    await open(pg, p);
    await place(pg, needle, delta);
    await clickSplit(pg);
    const t = await landed(pg, p, p.doc);
    eq(await text(p.doc), head, `${name}: the first chapter`);
    eq(await text(t), tail, `${name}: the new chapter`);
  }
});

await kit.test("Source view and the chord: the caret of the textarea is the place; a toolbar press before the split tool has arrived says so", async () => {
  const p = await mk("Split source", "Alpha is first.\n\nBravo is second and goes on.\n");
  await open(pg, p);
  await pg.evaluate(() => window.scriobh.write.editor.toggleSource(true));
  await pg.locator(".scriobh-source").evaluate((t) => { const n = t.value.indexOf("second"); t.focus(); t.setSelectionRange(n, n); });
  await pg.keyboard.press("Alt+Shift+KeyB");
  const t = await landed(pg, p, p.doc, { source: true });
  eq(await text(p.doc), "Alpha is first.\n\nBravo is\n", "Source: the first chapter");
  eq(await text(t), "second and goes on.\n", "Source: the new chapter");
  // the split tool is not there yet (its file is held back): the bar says so in words and cuts nothing
  kit.allowed = [/Failed to load resource/];
  const q = await mk("Split loading", "Alpha is first.\n\nBravo is second.\n");
  await pg.route(/\/static\/js\/split[.-][^/]*\.js/, (r) => r.abort());
  await pg.goto(`${base()}/p/${q.slug}/d/${q.doc}`);
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh.write && window.scriobh.alts)), "the writing view to load");
  eq(await pg.evaluate(() => !!window.scriobh.split), false, "the split tool has not arrived");
  await clickSplit(pg);
  await waitFor(async () => (await toastText(pg)) === "The split tool is still loading. Try again in a moment.", "the wait to be said");
  await pg.unroute(/\/static\/js\/split[.-][^/]*\.js/);
  await noCut(q);
});

await kit.test("typing while the split is being made is refused, and lands in neither chapter", async () => {
  kit.allowed = [/Failed to load resource|ERR_FAILED|ERR_ABORTED/];
  const p = await mk("Split held", "Alpha is first.\n\nBravo is second.\n");
  await open(pg, p);
  await clear(pg);
  await place(pg, "Bravo");
  await pg.route("**/api/v1/docs/*/split", async (r) => { await sleep(1500); await r.continue(); });
  await clickSplit(pg);
  await pg.keyboard.type("XYZ");
  await waitFor(async () => /^Hold on: the chapter is being saved/.test(await toastText(pg)), "the held keys to be refused in words");
  const tail = await landed(pg, p, p.doc);
  eq(await text(p.doc), "Alpha is first.\n", "the keys typed during the hold are in neither chapter: the first");
  eq(await text(tail), "Bravo is second.\n", "the keys typed during the hold are in neither chapter: the second");
  await pg.unroute("**/api/v1/docs/*/split");
});

// ---- a long chapter ------------------------------------------------------------------------------------------------------
const words = (n, tag) => { const out = []; let i = 0, c = 0; while (c < n) { const k = Math.min(40, n - c); out.push(Array.from({ length: k }, () => `${tag}${i++}`).join(" ")); c += k; } return out; };
const join = (ps) => ps.join("\n\n") + "\n";

await kit.test("a chapter over 15,000 words: the bar's button splits from the rich editor in one go, and so does the guard", async () => {
  const ps = words(15200, "w");
  const p = await mk("Split long", join(ps));
  await open(pg, p);
  await clear(pg);
  await pg.locator("#nb-guard").waitFor();
  const cut = 190;
  await place(pg, `w${cut * 40} `);
  await pg.locator("#nb-guard").getByRole("button", { name: "Split chapter here" }).click();
  const tail = await landed(pg, p, p.doc);
  eq(await text(p.doc), join(ps.slice(0, cut)), "the first chapter, word for word");
  eq(await text(tail), join(ps.slice(cut)), "the new chapter, word for word");
  // and the format bar's button does the same in the new chapter, which is 600 words under the limit
  ok((await list(p)).items.every((d) => d.word_count < 15000), "both are under the warning");
});

await kit.test("a chapter over 25,000 words opens read-only: a click puts the caret, one go cuts it", async () => {
  const ps = words(25200, "r");
  const p = await mk("Split read-only", join(ps));
  await open(pg, p);
  await clear(pg);
  eq(await pg.locator(".scriobh-prose").getAttribute("contenteditable"), "false", "read-only past the limit");
  const cut = 300;
  const at = await pg.evaluate((n) => { const v = window.scriobh.write.editor.view; let at = -1; v.state.doc.descendants((nd, pos) => { if (at < 0 && nd.isText && nd.text.startsWith("r" + n + " ")) at = pos; }); return at; }, cut * 40);
  await pg.evaluate((pos) => { const c = window.scriobh.write.editor.view.coordsAtPos(pos); window.scrollTo(0, window.scrollY + c.top - 300); }, at);
  const c = await pg.evaluate((pos) => { const c = window.scriobh.write.editor.view.coordsAtPos(pos); return { x: c.left + 1, y: (c.top + c.bottom) / 2 }; }, at);
  await pg.mouse.click(c.x, c.y);
  await waitFor(() => pg.evaluate((pos) => window.scriobh.write.editor.view.state.selection.from === pos, at), "a click in a read-only chapter to move the editor's selection");
  await pg.locator("#nb-guard").getByRole("button", { name: "Split chapter here" }).click();
  const tail = await landed(pg, p, p.doc);
  eq(await text(p.doc), join(ps.slice(0, cut)), "the first chapter, word for word");
  eq(await text(tail), join(ps.slice(cut)), "the new chapter, word for word");
  ok((await list(p)).items.every((d) => d.word_count < 25000), "both halves are under the limit");
});

await kit.test("a read-only chapter whose saved text is not the editor's spelling is cut from the saved text and not written again", async () => {
  const ps = words(25200, "s");
  ps[299] = "Before the list _emphasised_ words and " + ps[299];
  ps[300] = "* bullet one\n* bullet two";
  const p = await mk("Split stored", join(ps));
  const rev = (await api(main, "GET", `/api/v1/docs/${p.doc}`)).json.item.revision;
  const writes = [];
  pg.on("request", (r) => { if (r.method() === "PATCH" && /\/api\/v1\/docs\//.test(r.url())) writes.push(r.url()); });
  await open(pg, p);
  await clear(pg);
  eq(await pg.locator(".scriobh-prose").getAttribute("contenteditable"), "false", "read-only past the limit");
  eq(await pg.evaluate(() => window.scriobh.alts.placed()), false, "the saved text is not in the editor's spelling");
  // a cut between two blocks, after a list the file spells with *
  await place(pg, "s12040 ");
  await clickSplit(pg);
  let tail = await landed(pg, p, p.doc);
  eq(await text(p.doc), join(ps.slice(0, 301)), "the first chapter is the saved text, spelt as it was (the list with *, the emphasis with _)");
  eq(await text(tail), join(ps.slice(301)), "the new chapter");
  eq((await api(main, "GET", `/api/v1/docs/${p.doc}`)).json.item.revision, rev + 1, "the chapter was changed once, by the split: no rewrite before it");
  eq(writes.length, 0, "the page sent no save: " + writes.join(","));
  // the same in the middle of a paragraph
  const q = await mk("Split stored mid", join(ps));
  await open(pg, q);
  await place(pg, "s16010 ");
  await clickSplit(pg);
  tail = await landed(pg, q, q.doc);
  const para = ps[400].split(" ");
  eq(await text(q.doc), join([...ps.slice(0, 400), para.slice(0, 10).join(" ")]), "mid-paragraph: the first chapter");
  eq(await text(tail), join([para.slice(10).join(" "), ...ps.slice(401)]), "mid-paragraph: the new chapter");
  eq(writes.length, 0, "no save was sent");
});

// ---- the keyboard alone, at 390 px; the bar in both themes ------------------------------------------------------------------
await kit.test("keyboard only at 390 px: the button is reached with the arrow keys and pressed with Enter", async () => {
  const phone = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  await phone.addCookies(await main.cookies());
  await phone.addInitScript(() => { sessionStorage.removeItem("__source"); });
  const page = await phone.newPage();
  kit.watch(page, "phone");
  const p = await mk("Split phone", "Alpha is the first paragraph.\n\nBravo is the second paragraph and it goes on.\n");
  await open(page, p);
  await page.locator(".scriobh-prose").focus();
  await place(page, "Bravo");
  // the format bar is one Tab stop: Shift+Tab from the text reaches it, the arrows move along it
  for (let i = 0; i < 6 && !(await page.evaluate(() => !!document.activeElement.closest("#fmtbar"))); i++) await page.keyboard.press("Shift+Tab");
  eq(await page.evaluate(() => !!document.activeElement.closest("#fmtbar")), true, "Shift+Tab from the text reaches the format bar");
  // On a phone the strip sits behind Aa (U11.3): Enter on Aa opens it, then the arrows walk it.
  eq(await page.evaluate(() => document.activeElement.id), "fmt-aa", "on a phone the Tab stop is Aa");
  await page.keyboard.press("Enter");
  eq(await page.evaluate(() => document.getElementById("fmt-aa").getAttribute("aria-expanded")), "true", "Enter on Aa opens the strip");
  await page.keyboard.press("Tab"); // from Aa into the open strip: its one stop, then the arrows walk it
  let n = 0;
  while (n++ < 40 && (await page.evaluate(() => document.activeElement.dataset.cmd)) !== "revise") await page.keyboard.press("ArrowRight");
  eq(await page.evaluate(() => document.activeElement.dataset.cmd), "revise", "the arrows reach Revise");
  const r = await page.evaluate(() => { const b = document.activeElement.getBoundingClientRect(), bar = document.querySelector("#fmtbar").getBoundingClientRect(); return { l: b.left, r: b.right, bl: bar.left, br: bar.right, w: b.width, h: b.height }; });
  ok(r.l >= r.bl - 1 && r.r <= r.br + 1, `the focused button is inside the bar's visible part: ${JSON.stringify(r)}`);
  ok(r.w >= 44 && r.h >= 44, `a touch target of 44 px: ${r.w} x ${r.h}`);
  if (shots) await page.screenshot({ path: `${shots}/bar-390-light.png`, clip: { x: 0, y: 700, width: 390, height: 144 } });
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => document.activeElement && document.activeElement.dataset.cmd === "alt", null, { timeout: 5000 });
  for (let i = 0; i < 6 && (await page.evaluate(() => document.activeElement.dataset.cmd)) !== "split"; i++) await page.keyboard.press("ArrowDown");
  eq(await page.evaluate(() => document.activeElement.dataset.cmd), "split", "the arrows in the menu reach the chapter break");
  await page.keyboard.press("Enter");
  const tail = await landed(page, p, p.doc);
  eq(await text(p.doc), "Alpha is the first paragraph.\n", "the first chapter");
  eq(await text(tail), "Bravo is the second paragraph and it goes on.\n", "the new chapter");
  eq(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "no sideways scroll");
  // and the chord, on the same screen
  const q = await mk("Split phone chord", "One here.\n\nTwo here.\n");
  await open(page, q);
  await place(page, "Two");
  await page.keyboard.press("Alt+Shift+KeyB");
  const t2 = await landed(page, q, q.doc);
  eq(await text(t2), "Two here.\n", "the chord at 390 px");
  if (shots) {
    await open(page, p);
    await page.screenshot({ path: `${shots}/page-390-light.png` });
    await phone.close();
    for (const [scheme, vp, name] of [["light", { width: 1280, height: 800 }, "bar-1280-light"], ["dark", { width: 1280, height: 800 }, "bar-1280-dark"], ["dark", { width: 390, height: 844 }, "bar-390-dark"]]) {
      const c2 = await kit.newContext({ viewport: vp, deviceScaleFactor: 2, colorScheme: scheme });
      await c2.addCookies(await main.cookies());
      const pp = await c2.newPage();
      await open(pp, p);
      const sb = pp.getByRole("button", { name: "Revise", exact: true });
      if (vp.width < 600) await sb.focus(); else await sb.hover();
      await sleep(500);
      await pp.screenshot({ path: `${shots}/${name}.png`, clip: vp.width < 600 ? { x: 0, y: 700, width: 390, height: 144 } : { x: 0, y: 0, width: vp.width, height: 260 } });
      await c2.close();
    }
  } else await phone.close();
});

await kit.test("a page that does not move on to the new chapter takes its revision, and is read again after ten seconds", async () => {
  kit.allowed = [/Failed to load resource|ERR_FAILED|ERR_ABORTED/];
  const q = await mk("Split stays", "Alpha is first.\n\nBravo is second.\n");
  await open(pg, q);
  await place(pg, "Bravo");
  const held = /\/d\/[^/?]+\?new=1/; // the request for the new chapter is held: the page never moves
  await pg.route(held, async (r) => { await sleep(25000); await r.abort().catch(() => {}); });
  await clickSplit(pg);
  await waitFor(async () => (await list(q)).items.length === 2, "the split to be made");
  const head = (await api(main, "GET", `/api/v1/docs/${q.doc}`)).json.item;
  eq(await pg.evaluate(() => window.scriobh.write.rev), head.revision, "the page took the head's new revision (no 'changed somewhere else')");
  await waitFor(async () => (await toastText(pg).catch(() => "")) === "The chapter was split. The new chapter follows this one in the manuscript.", "the reloaded page to say what was done", 25000);
  eq(await pg.evaluate(() => window.scriobh.write.editor.getMarkdown()), "Alpha is first.\n", "the page shows the first chapter as it is now");
  await pg.unroute(held, { behavior: "ignoreErrors" });
});

await kit.finish("e2e-split");
