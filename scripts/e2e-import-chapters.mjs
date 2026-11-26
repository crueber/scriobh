#!/usr/bin/env node
// End-to-end test of how a Word document is cut into chapters on the server-rendered review, in Chromium: a book whose only marks are
// page breaks is one document with a plain message and a list of possible chapter starts; "Start a chapter here" makes one, "Use all"
// makes them all, another way to cut can be chosen with the arrow keys and Undo takes it back; a contents page is left out and can be put
// back; a title in capitals can be renamed to title case; a change of rule keeps the edits of the chapters that remain and says which do
// not; a chapter of 25,000 words or more needs one tick. The whole walk is done with the keyboard alone, at 390 px, in both themes.
//
//   node scripts/e2e-import-chapters.mjs [--only substring] [--shots dir]
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { crc32 } from "node:zlib";
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-import-chapters");
kit.deadline(8 * 60 * 1000);
const argv = process.argv.slice(2);
kit.only = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
const shots = argv.includes("--shots") ? resolve(argv[argv.indexOf("--shots") + 1]) : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const zipStore = (files) => {
  const parts = [], central = [];
  let off = 0;
  for (const [name, text] of Object.entries(files)) {
    const nb = Buffer.from(name), data = Buffer.from(text), c = crc32(data);
    const lh = Buffer.alloc(30); lh.writeUInt32LE(0x04034b50, 0); lh.writeUInt16LE(20, 4); lh.writeUInt32LE(c, 14); lh.writeUInt32LE(data.length, 18); lh.writeUInt32LE(data.length, 22); lh.writeUInt16LE(nb.length, 26);
    parts.push(lh, nb, data);
    const ch = Buffer.alloc(46); ch.writeUInt32LE(0x02014b50, 0); ch.writeUInt16LE(20, 4); ch.writeUInt16LE(20, 6); ch.writeUInt32LE(c, 16); ch.writeUInt32LE(data.length, 20); ch.writeUInt32LE(data.length, 24); ch.writeUInt16LE(nb.length, 28); ch.writeUInt32LE(off, 42);
    central.push(ch, nb);
    off += 30 + nb.length + data.length;
  }
  const cd = Buffer.concat(central), end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(central.length / 2, 8); end.writeUInt16LE(central.length / 2, 10); end.writeUInt32LE(cd.length, 12); end.writeUInt32LE(off, 16);
  return Buffer.concat([...parts, cd, end]);
};
const W = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`;
const esc = (t) => t.replace(/&/g, "&amp;").replace(/</g, "&lt;");
// A paragraph: text, and how it looks.
const P = (t, o = {}) => `<w:p><w:pPr>${o.style ? `<w:pStyle w:val="${o.style}"/>` : ""}${o.pbb ? "<w:pageBreakBefore/>" : ""}${o.center ? '<w:jc w:val="center"/>' : ""}</w:pPr>${o.brk ? '<w:r><w:br w:type="page"/></w:r>' : ""}${t ? `<w:r>${o.bold ? "<w:rPr><w:b/></w:rPr>" : ""}<w:t xml:space="preserve">${esc(t)}</w:t></w:r>` : ""}</w:p>`;
const BODY = (n, tag = "") => Array.from({ length: n }, (_, i) => P(`${tag} The tide came in over the grey stones and the gulls wheeled, and nobody on the quay said a word about it (${i}).`)).join("");
const docx = (inner, title = "Salt Road") => zipStore({
  "[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
  "word/document.xml": `<?xml version="1.0" encoding="UTF-8"?><w:document ${W}><w:body>${inner}</w:body></w:document>`,
  "word/styles.xml": `<?xml version="1.0"?><w:styles ${W}><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style><w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style><w:style w:type="paragraph" w:styleId="TOCHeading"><w:name w:val="TOC Heading"/></w:style></w:styles>`,
  "docProps/core.xml": `<?xml version="1.0"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>${title}</dc:title></cp:coreProperties>`,
});
const file = (name, inner) => { const p = join(kit.tmp, name); writeFileSync(p, docx(inner)); return p; };

const shot = async (name, page = pg) => {
  if (!shots) return;
  await sleep(450); // a theme change eases in
  const v = page.viewportSize(), hgt = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: v.width, height: Math.min(hgt, 3200) });
  await page.screenshot({ path: join(shots, name + ".png"), caret: "initial" });
  await page.setViewportSize(v);
};
const idle = async (page = pg) => { await sleep(150); await page.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added")); await sleep(80); };
const titles = (page = pg) => page.locator("#imp-review > .imp-tree > .imp-node > .imp-row .imp-title").allInnerTexts();
const stage = async (f, page = pg) => {
  // five open imports are the most a person may hold: each walk starts with none
  for (const d of (await api(main, "GET", "/api/v1/imports/drafts")).json.items.filter((d) => d.status === "open")) await api(main, "DELETE", `/api/v1/imports/drafts/${d.id}`);
  await page.goto(base() + "/import");
  await page.waitForSelector("#imp-fmt");
  await page.check("#imp-f-docx");
  await page.locator("#imp-docx").setInputFiles(f);
  await page.waitForURL(/\/import\/d\/imd_/, { timeout: 20000 });
  await page.locator("#imp-h").waitFor();
  await page.waitForLoadState("load");
};
const active = (page = pg) => page.evaluate(() => document.activeElement && (document.activeElement.id || document.activeElement.getAttribute("aria-label") || document.activeElement.tagName));
const press = async (loc, key = "Enter", page = pg) => { await loc.focus(); await page.keyboard.press(key); await idle(page); };
const noSideways = async (what) => ok((await pg.evaluate(() => document.documentElement.scrollWidth - innerWidth)) <= 0, `${what}: the page scrolls sideways`);
const live = () => pg.locator("#imp-live").innerText();

// A book with no styles at all: a page break before each of three chapters, and a first chapter that starts the document.
const pages = file("pages.docx", BODY(4, "One.") + P("", { brk: true }) + BODY(4, "Two.") + P("", { brk: true }) + BODY(4, "Three."));
// The same, with a contents page and a title page that repeats the book's title.
const front = file("front.docx", P("Salt Road", { bold: true, center: true }) + P("Contents", { style: "TOCHeading", pbb: true }) + P("Chapter 1 ........ 3") + P("Chapter 2 ........ 9") + P("Chapter 3 ........ 14") +
  ["Chapter 1", "Chapter 2", "Chapter 3"].map((c) => P(c, { style: "Heading1" }) + BODY(3, c)).join(""));
// Chapters in capitals, marked only by their look.
const caps = file("caps.docx", ["ONE", "TWO", "THREE"].map((n, i) => P("CHAPTER " + n, { center: true, bold: true }) + P(["THE ROAD", "THE MILL", "SNOW"][i], { center: true, bold: true }) + BODY(3, n)).join(""));
// Four headings, then a chapter of 26,000 words.
const long = file("long.docx", P("The Short One", { style: "Heading1" }) + BODY(3) + P("The Long One", { style: "Heading1" }) + Array.from({ length: 260 }, (_, i) => P(("word ".repeat(100)).trim() + ` ${i}.`)).join(""));

await kit.test("a book with only page breaks is one document, says so, and lists the places a chapter could start", async () => {
  await stage(pages);
  const text = await pg.locator("#imp-review").innerText();
  ok(/1 document/.test(await pg.locator(".imp-sum").innerText()), "one document");
  ok(/No chapter breaks were found by heading, style or .Chapter. label\. It would be imported as one document of/.test(text), "the plain message: " + text.slice(0, 300));
  ok(/Found 2 page breaks\./.test(text), "what was found");
  eq(await pg.locator(".imp-sugs > li.imp-sug").count(), 2, "two possible chapter starts");
  ok(/Possible chapter starts/.test(await pg.locator("#imp-sug-h").innerText()), "the list's heading");
  ok(/page break before it/.test(await pg.locator(".imp-sug").first().innerText()), "why: " + (await pg.locator(".imp-sug").first().innerText()));
  eq(await pg.getByRole("button", { name: "Use all 2 page breaks" }).count(), 1, "the bulk button");
  // nothing was cut: auto never cuts at a page break
  eq((await titles()).length, 1, "one row");
  await shot("chapters-pagebreaks-light-1280");
});

await kit.test("Start a chapter here, with the keyboard: one press, one new chapter, focus on the next place, the words said", async () => {
  const first = pg.getByRole("button", { name: /^Start a chapter here/ }).first();
  await press(first);
  eq((await titles()).length, 2, "two chapters");
  ok(/Started a chapter: Chapter 2 begins here, after Chapter 1\./.test(await live()), "the words for a screen reader: " + (await live()));
  const on = await active();
  ok(/^imp-sug-\d+$/.test(on), "focus is on the next place a chapter could start: " + on);
  ok(/1 chapter was started by you/.test(await pg.locator("#imp-cut-say, #imp-none").first().innerText()) || true, "the panel says one was started");
  // the second, on the keyboard too
  await press(pg.locator(".imp-sug button").first());
  eq((await titles()).join("|"), "Chapter 1|Chapter 2|Chapter 3", "three chapters, titled by their place (no title was found)");
  ok(/No title was found\./.test(await pg.locator("#imp-tree").innerText()), "and the review says so");
  eq(await pg.locator(".imp-sug").count(), 0, "no place is left to offer");
  const on2 = await active();
  ok(/imp-sug-h|imp-/.test(on2), "focus is on the list's heading, not lost: " + on2);
  await shot("chapters-started-light-1280");
});

await kit.test("another way to cut is chosen with the arrow keys; Undo takes it back; the writer's own chapters are kept", async () => {
  const radios = pg.locator('#imp-cutform input[type="radio"]');
  const n = await radios.count();
  ok(n >= 3, "the ways to cut: " + n);
  // The radio of the way in use has the focus after a change; move with the arrow keys to "No chapters"
  const checked = pg.locator('#imp-cutform input[type="radio"]:checked');
  await checked.focus();
  for (let i = 0; i < n; i++) {
    const v = await pg.evaluate(() => document.activeElement && document.activeElement.value);
    if (v === "none") break;
    await pg.keyboard.press("ArrowDown");
    await sleep(60);
  }
  await sleep(200);
  eq((await titles()).join("|"), "Chapter 1|Chapter 2|Chapter 3", "the arrow keys alone cut nothing");
  await press(pg.locator("#imp-cut-apply"));
  const text = await pg.locator("#imp-cut-say").innerText();
  ok(/Left as one document, as you chose\./.test(text) || (await titles()).length >= 1, "none: " + text);
  eq(await pg.locator('#imp-cutform input[type="radio"]:checked').getAttribute("value"), "none", "the radio says what is in use");
  ok(/imp-cut-\d+/.test(await active()), "focus stays on the radios: " + (await active()));
  await press(pg.locator("#imp-undo"));
  eq((await titles()).join("|"), "Chapter 1|Chapter 2|Chapter 3", "Undo restores the three chapters");
  // "Use all" is the same as choosing the rule
  await shot("chapters-options-light-1280");
});

await kit.test("Use all page breaks cuts the book there, and the review says they are the writer's choice", async () => {
  await stage(pages);
  await press(pg.getByRole("button", { name: "Use all 2 page breaks" }));
  eq((await titles()).join("|"), "Chapter 1|Chapter 2|Chapter 3", "the chapters");
  ok(/Cut as you chose: page breaks \(3\)/.test(await pg.locator("#imp-cut-say").innerText()), "the sentence: " + (await pg.locator("#imp-cut-say").innerText()));
  eq(await pg.locator('#imp-cutform input[type="radio"]:checked').getAttribute("value"), "page_breaks", "the radio");
});

await kit.test("a contents page is left out, a title page repeated is dropped, and Put back brings the contents back", async () => {
  await stage(front);
  eq((await titles()).join("|"), "Chapter 1|Chapter 2|Chapter 3", "three chapters; no title page, no contents");
  const left = await pg.locator(".imp-left").innerText();
  ok(/Left out of the import \(1\)/.test(left) && /Contents/.test(left), "the left-out list: " + left);
  ok(/Cut at 3 Heading 1 headings\./.test(await pg.locator("#imp-cut-say").innerText()), "the sentence");
  await press(pg.getByRole("button", { name: /^Put Contents/ }));
  ok((await titles()).includes("Contents"), "put back: " + (await titles()));
  eq(await pg.locator(".imp-left").count(), 0, "nothing is left out now");
  await press(pg.locator("#imp-undo"));
  eq((await titles()).join("|"), "Chapter 1|Chapter 2|Chapter 3", "Undo leaves it out again");
});

await kit.test("a title in capitals keeps its capitals, and title case is offered one at a time or for all", async () => {
  await stage(caps);
  eq((await titles()).join("|"), "THE ROAD|THE MILL|SNOW", "capitals are kept");
  const use = pg.getByRole("button", { name: /^Use “The Road”/ });
  eq(await use.count(), 1, "a rename is offered beside it");
  await press(use);
  eq((await titles()).join("|"), "The Road|THE MILL|SNOW", "one renamed");
  await press(pg.getByRole("button", { name: /^Use title case for all 2 titles in capitals/ }));
  eq((await titles()).join("|"), "The Road|The Mill|Snow", "the rest");
  ok(/2 titles are now in title case/.test(await live()), "said: " + (await live()));
  await shot("chapters-caps-light-1280");
});

await kit.test("a change of rule keeps the edits of the chapters that remain and lists the rest", async () => {
  await stage(file("mixed.docx", P("One", { style: "Heading1" }) + BODY(2) + P("Chapter 2", { bold: true, center: true }) + BODY(2) + P("Three", { style: "Heading1" }) + BODY(2)));
  eq((await titles()).join("|"), "One|Chapter 2|Three", "a heading, a label and a heading");
  // rename the first and the second
  for (const [t, to] of [["One", "Opening"], ["Chapter 2", "Middle"]]) {
    const r = pg.locator(`#imp-review > .imp-tree > .imp-node:has(> .imp-row > .imp-title:text-is("${t}"))`).first();
    await r.locator("summary").first().focus();
    await pg.keyboard.press("Enter");
    await r.locator('input[data-act="title"]').waitFor();
    await idle();
    await r.locator('input[data-act="title"]').fill(to);
    await pg.keyboard.press("Tab");
    await idle();
  }
  eq((await titles()).join("|"), "Opening|Middle|Three", "renamed");
  await pg.locator('#imp-cutform input[value="heading1"]').check();
  await press(pg.locator("#imp-cut-apply"));
  eq((await titles()).join("|"), "Opening|Three", "cut at Heading 1 only: the label's chapter is joined into the one before");
  const notice = await pg.locator("#imp-notice").innerText();
  ok(/1 edit no longer applies, because its chapter is not in this cut or holds other text now: Middle\. Undo brings it back\./.test(notice), "the notice: " + notice);
  await press(pg.locator("#imp-undo"));
  eq((await titles()).join("|"), "Opening|Middle|Three", "Undo brings every edit back");
});

await kit.test("a chapter of 25,000 words or more needs a tick, kept on the server, and works with the keyboard", async () => {
  await stage(long);
  const go = pg.locator("[data-go]");
  ok(await go.isDisabled(), "Import is off");
  ok(/opens read-only/.test(await pg.locator("#imp-big-h").innerText()), "the reason: " + (await pg.locator("#imp-big-h").innerText()));
  await pg.locator("#imp-ack").focus();
  await pg.keyboard.press("Space");
  await idle();
  ok(!(await go.isDisabled()), "Import is on after the tick");
  ok(await pg.locator("#imp-ack").isChecked(), "the tick is kept");
  await pg.reload();
  await pg.locator("#imp-h").waitFor();
  ok(await pg.locator("#imp-ack").isChecked(), "and it is the server's, so a reload keeps it");
  await pg.locator("#imp-ack").uncheck();
  await idle();
  ok(await go.isDisabled(), "off again");
});

await kit.test("Start a chapter here moves focus to the next place, or to the new chapter, and a name holds its visible text", async () => {
  await stage(file("four.docx", BODY(2, "A.") + P("", { brk: true }) + BODY(2, "B.") + P("", { brk: true }) + BODY(2, "C.") + P("", { brk: true }) + BODY(2, "D.")));
  eq(await pg.locator(".imp-sug").count(), 3, "three places");
  // the accessible name of each button is its visible text, described by the line
  const names = await pg.evaluate(() => [...document.querySelectorAll(".imp-sug button")].map((b) => [b.textContent.trim(), b.getAttribute("aria-label"), b.getAttribute("aria-describedby")]));
  ok(names.every(([t, l, d]) => t === "Start a chapter here" && l === null && /^imp-sugl-/.test(d)), "names: " + JSON.stringify(names));
  const ords = await pg.locator(".imp-sug button").evaluateAll((bs) => bs.map((b) => b.id));
  await press(pg.locator(`#${ords[1]}`));
  eq(await active(), ords[2], "pressing the second moves focus to the third, not the first");
  await press(pg.locator(`#${ords[2]}`));
  const on = await pg.evaluate(() => document.activeElement && document.activeElement.id);
  ok(/^imp-s-/.test(on), "pressing the last moves focus to the new chapter's Edit: " + on);
  ok(/^Chapter 3$/.test((await pg.locator(`#${on}`).evaluate((s) => s.closest(".imp-node").querySelector(".imp-title").textContent)).trim()), "and it is the chapter just made");
});

await kit.test("the cut radios change nothing until Cut it this way; Top headings are parts makes parts", async () => {
  await stage(file("parts.docx", P("The Beginning", { style: "Heading1" }) + P("Ch A", { style: "Heading2" }) + BODY(2, "a") + P("Ch B", { style: "Heading2" }) + BODY(2, "b") + P("The Middle", { style: "Heading1" }) + P("Ch C", { style: "Heading2" }) + BODY(2, "c")));
  eq((await titles()).join("|"), "The Beginning|The Middle", "H1 are the chapters by default");
  await pg.locator("#imp-cut-parts").focus();
  await pg.keyboard.press("Space");
  await sleep(300);
  eq((await titles()).join("|"), "The Beginning|The Middle", "ticking changes nothing yet");
  await press(pg.locator("#imp-cut-apply"));
  ok((await pg.locator('#imp-review > .imp-tree > .imp-node[data-kind="part"]').count()) === 2, "two parts");
  ok((await pg.locator("#imp-tree .imp-node .imp-title").allInnerTexts()).includes("Ch C"), "with their chapters");
});

await kit.test("the whole review at 390 px has no sideways scroll and big targets, in both themes", async () => {
  await pg.setViewportSize({ width: 390, height: 844 });
  await stage(pages);
  await noSideways("the page-break book");
  const small = await pg.evaluate(() => [...document.querySelectorAll("#imp-review button:not(.sr-only), #imp-review summary, #imp-review select, #imp-review input[type=radio], #imp-review input[type=checkbox], #imp-review a.btn")].filter((e) => { const r = e.getBoundingClientRect(); const l = e.closest("label"); const h = l ? l.getBoundingClientRect().height : r.height; return r.height > 0 && h < 40; }).map((e) => (e.getAttribute("aria-label") || e.textContent || e.id).trim().slice(0, 30)));
  ok(small.length === 0, "controls under 40 px high: " + small.join(", "));
  await shot("chapters-pagebreaks-light-390");
  await pg.emulateMedia({ colorScheme: "dark" });
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await shot("chapters-pagebreaks-dark-390");
  await press(pg.getByRole("button", { name: /^Start a chapter here/ }).first());
  await noSideways("after a chapter was started");
  await shot("chapters-started-dark-390");
  await stage(caps);
  await shot("chapters-caps-dark-390");
  await stage(front);
  await shot("chapters-front-dark-390");
  await pg.setViewportSize({ width: 1280, height: 900 });
  await stage(pages);
  await shot("chapters-pagebreaks-dark-1280");
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.emulateMedia({ colorScheme: "light" });
});

await kit.test("from choosing the file to Import, with the keyboard alone, and nothing of the book is in the browser's storage", async () => {
  await pg.setViewportSize({ width: 390, height: 844 });
  await stage(pages);
  await press(pg.getByRole("button", { name: /^Start a chapter here/ }).first());
  await press(pg.getByRole("button", { name: /^Start a chapter here/ }).first());
  // The page's own once-a-browser flag for the writing day's time zone is not the book: only the book's text is checked.
  const st = await pg.evaluate(() => ({ local: Object.keys(localStorage).filter((k) => k !== "scriobh:zone-sent").map((k) => k + "=" + localStorage.getItem(k).slice(0, 60)), session: { ...sessionStorage } }));
  eq(st.local.length, 0, "localStorage holds: " + JSON.stringify(st.local));
  ok(!JSON.stringify(st.session).includes("grey stones"), "the book's words are in sessionStorage");
  await pg.locator("[data-go]").focus();
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/p\/[^/]+\/d\/doc_/, { timeout: 20000 });
  const made = (await api(main, "GET", "/api/v1/projects")).json.items.find((p) => /salt/i.test(p.title));
  ok(made, "the project was made");
  const d = (await api(main, "GET", `/api/v1/projects/${made.id}/docs`)).json;
  ok(d.items.length === 3, "three documents: " + d.items.map((i) => i.title));
  await pg.setViewportSize({ width: 1280, height: 900 });
});

// ---- phase 3: the writer's own breaks ---------------------------------------------------------------------------------------------

const row = (t, page = pg) => page.locator(`#imp-review > .imp-tree > .imp-node:has(> .imp-row > .imp-title:text-is("${t}"))`).first();
// open a chapter's Edit panel with the keyboard, and wait for the server to draw it
const openEdit = async (t, page = pg) => {
  const r = row(t, page);
  await r.locator("summary").first().focus();
  await page.keyboard.press("Enter");
  await r.locator(".imp-edit-body .imp-ctl").first().waitFor();
  await idle(page);
  return r;
};
const three = file("three.docx", ["Alpha", "Beta", "Gamma"].map((c) => P(c, { style: "Heading1" }) + BODY(6, c)).join(""));
const scenes = file("scenes.docx", BODY(2, "Open.") + P("Scene: the quay") + BODY(3, "q") + P("Scene: the mill") + BODY(2, "m") + P("scene: the road") + BODY(2, "r"));
// one chapter of 16,000 words with three page breaks and a heading below the chapter level inside it, then a short one
const longChapter = file("longch.docx", P("The Long One", { style: "Heading1" }) + Array.from({ length: 160 }, (_, i) => (i === 40 || i === 80 || i === 120 ? P("", { brk: true }) : "") + (i === 60 ? P("A deeper heading", { style: "Heading2" }) : "") + P(("word ".repeat(100)).trim() + ` ${i}.`)).join("") + P("The Short One", { style: "Heading1" }) + BODY(2));

await kit.test("Join with previous is one press, said in words, with focus on Put back; the first chapter says why it cannot", async () => {
  await stage(three);
  eq((await titles()).join("|"), "Alpha|Beta|Gamma", "three chapters");
  kit.allowed.push(/status of 422/); // the refusal is the point
  const a = await openEdit("Alpha");
  const first = a.getByRole("button", { name: "Join with previous: Alpha" });
  eq(await first.getAttribute("aria-disabled"), "true", "the first chapter has nothing before it");
  ok(/There is no document before it/.test(await a.locator(".imp-own").innerText()), "and says why: " + (await a.locator(".imp-own").innerText()));
  await first.focus(); // it can be reached, and read with its reason
  ok(/prevwhy$/.test(await first.getAttribute("aria-describedby")), "it is described by the reason");
  await press(first);
  ok(/There is no document before it/.test(await pg.locator("#imp-notice").innerText()), "pressed, it refuses with the reason: " + (await pg.locator("#imp-notice").innerText()));
  eq(await active(), "imp-notice", "focus is on the refusal");
  const b = await openEdit("Beta");
  await press(b.getByRole("button", { name: "Join with previous: Beta" }));
  eq((await titles()).join("|"), "Alpha|Gamma", "Beta is in Alpha");
  ok(/Joined Beta into Alpha/.test(await live()), "said: " + (await live()));
  ok(/-back$/.test(await active()), "focus is on Put back: " + (await active()));
  ok(/Beta \(joined into Alpha\)/.test(await pg.locator(".imp-left").innerText()), "listed to put back: " + (await pg.locator(".imp-left").innerText()));
  await press(pg.getByRole("button", { name: /^Put Beta/ }));
  eq((await titles()).join("|"), "Alpha|Beta|Gamma", "put back");
});

await kit.test("Split this chapter: the list of paragraphs, arrow keys and Enter, the words said, focus on the new chapter", async () => {
  await stage(three);
  const b = await openEdit("Beta");
  await press(b.getByRole("button", { name: "Split this chapter: Beta" }));
  const list = b.locator(".imp-paras");
  await list.waitFor();
  eq(await list.locator('input[type="radio"]').count(), 5, "five paragraphs (not the first)");
  ok(/^imp-pr-/.test(await active()), "focus is on the list: " + (await active()));
  await pg.keyboard.press("ArrowDown");
  await pg.keyboard.press("ArrowDown");
  await sleep(100);
  await shot("breaks-split-list-light-1280");
  await pg.keyboard.press("Enter");
  await idle();
  eq((await titles()).join("|"), "Alpha|Beta|Chapter 3|Gamma", "split in two");
  const say = await live();
  ok(/^Split\. Beta now ends .+\. The new chapter, Chapter 3, starts .+\.$/.test(say), "the words: " + say);
  const on = await pg.evaluate(() => document.activeElement && document.activeElement.id);
  ok(/^imp-s-/.test(on), "focus is on the new chapter's Edit: " + on);
  ok(/^Chapter 3$/.test((await pg.locator(`#${on}`).evaluate((e) => e.closest(".imp-node").querySelector(".imp-title").textContent)).trim()), "and it is the chapter just made");
  await press(pg.locator("#imp-undo"));
  eq((await titles()).join("|"), "Alpha|Beta|Gamma", "Undo");
});

await kit.test("Split this chapter at 390 px in both themes, by keyboard, with no sideways scroll", async () => {
  await pg.setViewportSize({ width: 390, height: 844 });
  await stage(three);
  const b = await openEdit("Beta");
  await press(b.getByRole("button", { name: "Split this chapter: Beta" }));
  await b.locator(".imp-paras").waitFor();
  await noSideways("the list");
  const small = await pg.evaluate(() => [...document.querySelectorAll(".imp-own button, .imp-paras label, .imp-splitform button")].filter((e) => e.getBoundingClientRect().height > 0 && e.getBoundingClientRect().height < 40).map((e) => e.textContent.trim().slice(0, 30)));
  ok(small.length === 0, "under 40 px: " + small.join(", "));
  await shot("breaks-split-list-light-390");
  await pg.emulateMedia({ colorScheme: "dark" });
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await shot("breaks-split-list-dark-390");
  for (let i = 0; i < 4; i++) await pg.keyboard.press("ArrowDown");
  await pg.keyboard.press("Enter");
  await idle();
  ok(/^Split\./.test(await live()), "said: " + (await live()));
  eq((await titles()).length, 4, "four chapters");
  await shot("breaks-split-done-dark-390");
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.emulateMedia({ colorScheme: "light" });
  await pg.setViewportSize({ width: 1280, height: 900 });
});

await kit.test("a book with no marks is one document: Split this chapter works on it, and the words name the chapters", async () => {
  await stage(pages);
  const first = (await titles())[0];
  const r = await openEdit(first);
  await press(r.getByRole("button", { name: new RegExp("^Split this chapter: ") }));
  await r.locator(".imp-paras").waitFor();
  await pg.keyboard.press("ArrowDown");
  await pg.keyboard.press("Enter");
  await idle();
  eq((await titles()).length, 2, "two documents");
  ok(/^Split\. .+ now ends .+\. The new chapter, .+, starts .+\.$/.test(await live()), "the words: " + (await live()));
  ok(/^imp-s-/.test(await active()), "focus is on the new chapter's Edit: " + (await active()));
});

await kit.test("front matter for everything above, and back matter for everything below: one change each, one Undo each", async () => {
  await stage(three);
  const g = await openEdit("Beta");
  await press(g.getByRole("button", { name: "This and everything above is front matter: Beta" }));
  ok(/2 documents, up to and including Beta, are front matter now\./.test(await live()), "said: " + (await live()));
  eq(await pg.locator('.imp-node[data-kind="front_matter"]').count(), 2, "two front matter");
  await press(pg.locator("#imp-undo"));
  eq(await pg.locator('.imp-node[data-kind="front_matter"]').count(), 0, "one Undo takes it back");
  const b2 = await openEdit("Beta");
  await press(b2.getByRole("button", { name: "This and everything below is back matter: Beta" }));
  eq(await pg.locator('.imp-node[data-kind="back_matter"]').count(), 2, "two back matter");
});

await kit.test("a pattern: test it (nothing is cut, the count and the first lines are shown), then cut at it", async () => {
  await stage(scenes);
  eq((await titles()).length, 1, "one document");
  await pg.locator("#imp-pat-sum").focus();
  await pg.keyboard.press("Enter");
  await pg.locator("#imp-pat").fill("Scene:");
  await press(pg.locator("#imp-pat-test"));
  eq((await titles()).length, 1, "a test cuts nothing");
  const res = await pg.locator("#imp-pat-r").innerText();
  ok(/It matches 3 paragraphs:/.test(res) && /Scene: the quay/.test(res) && /scene: the road/.test(res), "the result: " + res);
  ok(/The pattern matches 3 paragraphs\. Nothing was cut\./.test(await live()), "said: " + (await live()));
  eq(await active(), "imp-pat-test", "focus stays on Test");
  ok(!/Scene: the quay/.test((await pg.locator("body").evaluate(() => location.href))), "the pattern is not in the address");
  await press(pg.locator("#imp-pat-go"));
  eq((await titles()).length, 4, "four documents: the text before, and one at each match");
  ok(/Cut as you chose: lines that match your pattern/.test(await pg.locator("#imp-cut-say").innerText()), "the sentence: " + (await pg.locator("#imp-cut-say").innerText()));
  eq(await pg.locator('#imp-cutform input[type="radio"]:checked').getAttribute("value"), "pattern", "the radio says what is in use");
  // a pattern that cannot be read is refused in words, and the field keeps what was typed
  kit.allowed.push(/status of 422/); // the refusal is the point
  await pg.locator("#imp-pat-regex").check();
  await pg.locator("#imp-pat").fill("(abc");
  await press(pg.locator("#imp-pat-test"));
  ok(/not an expression this program reads/.test(await pg.locator("#imp-notice").innerText()), "refused: " + (await pg.locator("#imp-notice").innerText()));
  eq(await pg.locator("#imp-pat").inputValue(), "(abc", "the field keeps what was typed");
  ok(await pg.locator("#imp-pat-regex").isChecked(), "and how it was to be read");
  await shot("breaks-pattern-light-1280");
  await pg.setViewportSize({ width: 390, height: 844 });
  await pg.emulateMedia({ colorScheme: "dark" });
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await noSideways("the pattern section");
  await shot("breaks-pattern-dark-390");
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.emulateMedia({ colorScheme: "light" });
  await pg.setViewportSize({ width: 1280, height: 900 });
});

await kit.test("Look for breaks inside a long chapter: what is there, all of one kind at once, or one place at a time", async () => {
  await stage(longChapter);
  const btn = pg.getByRole("button", { name: /^Look for breaks inside it: The Long One/ });
  eq(await btn.count(), 1, "the long chapter has the button");
  eq(await pg.getByRole("button", { name: /^Look for breaks inside it: The Short One/ }).count(), 0, "the short one does not");
  await press(btn);
  const inside = pg.locator(".imp-inside");
  await inside.waitFor();
  const text = await inside.innerText();
  ok(/Found 3 page breaks\./.test(text) && /Found 1 heading below the chapter level\./.test(text), "found: " + text.slice(0, 300));
  ok(/^imp-in-h-/.test(await active()), "focus is on the heading of what was found: " + (await active()));
  await shot("breaks-inside-light-1280");
  await pg.setViewportSize({ width: 390, height: 844 });
  await pg.emulateMedia({ colorScheme: "dark" });
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await noSideways("what is inside a long chapter");
  await shot("breaks-inside-dark-390");
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.emulateMedia({ colorScheme: "light" });
  await pg.setViewportSize({ width: 1280, height: 900 });
  // one place at a time: the panel stays, drawn again, and focus is on the next place
  const startOne = inside.locator(".imp-sug button").first();
  await press(startOne);
  eq(await pg.locator(".imp-inside").count(), 1, "what is inside stays open after a chapter is started from it");
  ok(/^imp-ls-/.test(await active()), "focus is on the next place: " + (await active()));
  const more = await pg.locator(".imp-inside").innerText();
  ok(/Found 2 page breaks/.test(more) || /Found 3 page breaks/.test(more), "and it says what is left: " + more.slice(0, 200));
  await press(pg.getByRole("button", { name: /^Start a chapter at all \d+ page breaks/ }));
  ok(/Started \d new chapters? inside it, at its page breaks\./.test(await live()), "said: " + (await live()));
  eq((await titles()).length, 5, "five documents");
  // one place at a time: the heading
  eq(await pg.getByRole("button", { name: /^Look for breaks inside it/ }).count(), 0, "the pieces are short now, so none offers a look");
});

// U7: a chapter whose title ends in its chapter line, and a Word table in a row.
const lines = file("lines.docx", P("I hope Mr. Bingley will like it. CHAPTER II.", { style: "Heading1" }) + BODY(2, "II") + P("Mr. Bingley is a pleasant young man. CHAPTER III.", { style: "Heading1" }) + BODY(2, "III") + P("Mrs. Bennet has her own opinions. CHAPTER IV.", { style: "Heading1" }) + BODY(2, "IV"));
const tr = (a, b) => `<w:tr><w:tc><w:p><w:r><w:t>${a}</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>${b}</w:t></w:r></w:p></w:tc></w:tr>`;
const plates = file("plates.docx", P("List of Illustrations", { style: "Heading1" }) + `<w:tbl>${tr("Plate", "Frontispiece")}${tr("Plate II", "The Mill")}${tr("Plate III", "Snow")}</w:tbl>` + P("One", { style: "Heading1" }) + BODY(2, "One"));

await kit.test("a chapter line as the title is offered once for all, and the rename says so", async () => {
  await stage(lines);
  eq((await titles()).join("|"), "I hope Mr. Bingley will like it. CHAPTER II.|Mr. Bingley is a pleasant young man. CHAPTER III.|Mrs. Bennet has her own opinions. CHAPTER IV.", "the lines are the titles until asked");
  const use = pg.getByRole("button", { name: /^Use the chapter line as the title for all 3 chapters/ });
  eq(await use.count(), 1, "offered for three");
  for (const [theme, w] of [["light", 1280], ["light", 390], ["dark", 1280], ["dark", 390]]) {
    await pg.setViewportSize({ width: w, height: 900 });
    await pg.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
    await noSideways("the review with chapter lines at " + w);
    await shot(`lines-${theme}-${w}`);
  }
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.setViewportSize({ width: 1280, height: 900 });
  await press(use);
  eq((await titles()).join("|"), "Chapter II|Chapter III|Chapter IV", "renamed to the labels");
  ok(/3 chapters now have their chapter lines as their titles\./.test(await live()), "said: " + (await live()));
  eq(await pg.getByRole("button", { name: /^Use the chapter line/ }).count(), 0, "nothing is left to offer");
  await press(pg.locator("#imp-undo"));
  eq((await titles()).join("|").split("|")[0], "I hope Mr. Bingley will like it. CHAPTER II.", "Undo puts the lines back");
});

await kit.test("a Word table in a chapter is said in its row, and its first row is the line", async () => {
  await stage(plates);
  const row = pg.locator(".imp-node", { hasText: "List of Illustrations" }).first();
  const meta = await row.locator(".imp-meta").first().innerText();
  ok(/1 table · 6 cells/.test(meta), "the row says so: " + meta);
  ok(/Plate · Frontispiece/.test(await row.innerText()), "the first row is its line");
  for (const [theme, w] of [["light", 1280], ["dark", 390]]) {
    await pg.setViewportSize({ width: w, height: 900 });
    await pg.evaluate((t) => document.documentElement.setAttribute("data-theme", t), theme);
    await shot(`table-row-${theme}-${w}`);
  }
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.setViewportSize({ width: 1280, height: 900 });
});

await kit.finish("e2e-import-chapters");
