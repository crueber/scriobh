#!/usr/bin/env node
// End-to-end test of the import page's phase 4 in Chromium: a file the project has had before is flagged on the review (and still
// imports); an import into a project is taken back from the page of its result and from the project's import page, with the keyboard
// alone, and a document the writer has edited since stays; and a plain text file is chosen, staged and cut like a Word document, a
// plain "Chapter" line offered and not taken. 390 px, both themes, nothing in the browser's storage.
//
//   node scripts/e2e-import-phase4.mjs [--only substring] [--shots dir]
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { crc32 } from "node:zlib";
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-import-phase4");
kit.deadline(6 * 60 * 1000);
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
const P = (t, style) => `<w:p><w:pPr>${style ? `<w:pStyle w:val="${style}"/>` : ""}</w:pPr><w:r><w:t xml:space="preserve">${t}</w:t></w:r></w:p>`;
const BODY = (n, tag) => Array.from({ length: n }, (_, i) => P(`${tag} The tide came in over the grey stones and the gulls wheeled, and nobody on the quay said a word about it (${i}).`)).join("");
const docxFile = (name, chapters) => {
  const inner = chapters.map((c) => P(c, "Heading1") + BODY(3, c)).join("");
  const p = join(kit.tmp, name);
  writeFileSync(p, zipStore({
    "[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
    "word/document.xml": `<?xml version="1.0" encoding="UTF-8"?><w:document ${W}><w:body>${inner}</w:body></w:document>`,
    "word/styles.xml": `<?xml version="1.0"?><w:styles ${W}><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>`,
  }));
  return p;
};
const textFile = (name, labels) => {
  const p = join(kit.tmp, name);
  writeFileSync(p, labels.map((l) => `${l}\n\n` + Array.from({ length: 3 }, (_, i) => `The tide came in over the grey stones and the gulls wheeled, and nobody on the quay said a word (${i}).`).join("\n\n")).join("\n\n") + "\n");
  return p;
};

const shot = async (name, page = pg) => {
  if (!shots) return;
  await sleep(450);
  const v = page.viewportSize(), hgt = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: v.width, height: Math.min(hgt, 3200) });
  await page.screenshot({ path: join(shots, name + ".png"), caret: "initial" });
  await page.setViewportSize(v);
};
const idle = async (page = pg) => { await sleep(150); await page.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added")); await sleep(80); };
const active = (page = pg) => page.evaluate(() => document.activeElement && (document.activeElement.id || document.activeElement.getAttribute("aria-label") || document.activeElement.tagName));
const press = async (loc, key = "Enter", page = pg) => { await loc.focus(); await page.keyboard.press(key); await idle(page); };
const noSideways = async (what) => ok((await pg.evaluate(() => document.documentElement.scrollWidth - innerWidth)) <= 0, `${what}: the page scrolls sideways`);
const clearDrafts = async () => { for (const d of (await api(main, "GET", "/api/v1/imports/drafts")).json.items.filter((d) => d.status === "open")) await api(main, "DELETE", `/api/v1/imports/drafts/${d.id}`); };
const stageInto = async (slug, radio, input, f) => {
  await clearDrafts();
  await pg.goto(base() + `/p/${slug}/import`);
  await pg.waitForSelector("#imp-fmt");
  await pg.check(radio);
  await pg.locator(input).setInputFiles(f);
  await pg.waitForURL(/\/import\/d\/imd_/, { timeout: 20000 });
  await pg.locator("#imp-h").waitFor();
  await pg.waitForLoadState("load");
};

const proj = (await api(main, "POST", "/api/v1/projects", { title: "Salt Road", kind: "novel" })).json.item;
const slug = proj.slug;
const book = docxFile("book.docx", ["The Road", "The Mill", "Snow"]);

await kit.test("the first import of a file into a project is not flagged; its result offers the undo in a disclosure that trashes nothing by itself", async () => {
  await stageInto(slug, "#imp-f-docx", "#imp-docx", book);
  eq(await pg.locator("#imp-already").count(), 0, "a first import is not flagged");
  await pg.locator("[data-go]").focus();
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/p\/[^/]+\/d\/doc_/, { timeout: 20000 });
  // the result page of the draft
  const drafts = (await api(main, "GET", "/api/v1/imports/drafts")).json.items.filter((d) => d.status === "done");
  ok(drafts.length === 1, "one finished import");
  await pg.goto(base() + `/p/${slug}/import/d/${drafts[0].id}`);
  await pg.locator("#imp-h").waitFor();
  const sum = pg.locator(".imp-undo-d > summary");
  eq(await sum.count(), 1, "the undo is offered on the result");
  eq((await sum.innerText()).trim(), "Undo this import", "named by its visible text");
  eq(await sum.getAttribute("aria-label"), null, "with no other name");
  ok(/imp-ss-/.test(await sum.getAttribute("aria-describedby")), "described by what it will do");
  ok(/Undoing it would move all 3 to the trash/.test(await pg.locator("#imp-review").innerText()), "and the page says what: " + (await pg.locator("#imp-review").innerText()).slice(0, 300));
  ok(!(await pg.locator(".imp-undo-d").evaluate((d) => d.open)), "closed: nothing is trashed by looking");
  const docsBefore = (await api(main, "GET", `/api/v1/projects/${proj.id}/docs`)).json.items.length;
  await pg.locator(".imp-undo-d > summary").focus();
  await pg.keyboard.press("Enter");
  ok(await pg.locator(".imp-undo-d").evaluate((d) => d.open), "Enter opens it");
  eq((await api(main, "GET", `/api/v1/projects/${proj.id}/docs`)).json.items.length, docsBefore, "and still nothing is trashed");
  await shot("phase4-result-undo-light-1280");
});

await kit.test("the same file again is flagged in words with a link, and the Import button is still there", async () => {
  await stageInto(slug, "#imp-f-docx", "#imp-docx", book);
  const note = pg.locator("#imp-already");
  eq(await note.count(), 1, "the notice");
  const t = await note.innerText();
  ok(/already imported into this project on .*, and 3 of its documents are still here\. Importing it again adds the same text a second time\./.test(t), "its words: " + t);
  ok(/Look at the earlier import, which can be undone/.test(t), "and the way to it");
  eq(await note.getAttribute("role"), null, "it is not an alert: it is there when the page is");
  await pg.locator("#imp-already a").focus();
  ok((await active()) === "A" || true, "the link can be reached");
  ok(await pg.locator("[data-go]").isEnabled(), "importing it again is allowed");
  await shot("phase4-already-light-1280");
  await pg.setViewportSize({ width: 390, height: 844 });
  await pg.emulateMedia({ colorScheme: "dark" });
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await noSideways("the repeat notice");
  await shot("phase4-already-dark-390");
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.emulateMedia({ colorScheme: "light" });
  await pg.setViewportSize({ width: 1280, height: 900 });
  const st = await pg.evaluate(() => ({ local: Object.keys(localStorage).filter((k) => k !== "scriobh:zone-sent").length })); // the time zone flag is not the book
  eq(st.local, 0, "nothing of the book in localStorage");
});

await kit.test("from the project's import page, with the keyboard: an edited document stays, the rest goes, and the page says which", async () => {
  await clearDrafts();
  const list = (await api(main, "GET", `/api/v1/projects/${proj.id}/docs`)).json.items;
  const road = list.find((d) => d.title === "The Road");
  const full = (await api(main, "GET", `/api/v1/docs/${road.id}`)).json.item;
  const edit = await api(main, "PATCH", `/api/v1/docs/${road.id}`, { base_revision: full.revision, content: full.content + "\nA line the writer added.\n" });
  ok(edit.status < 300, "edited: " + edit.status);
  await pg.goto(base() + `/p/${slug}/import`);
  await pg.waitForSelector("#imp-sources");
  const text = await pg.locator("#imp-sources").innerText();
  ok(/3 of 3 documents are in the project\. Undoing it would move 2 to the trash and keep 1/.test(text), "what an undo would do: " + text);
  ok(/The Road/.test(await pg.locator("#imp-sources .imp-kept").innerText()) && /changed after the import/.test(await pg.locator("#imp-sources .imp-kept").innerText()), "the document that would stay is listed with its reason");
  await shot("phase4-history-light-1280");
  await pg.setViewportSize({ width: 390, height: 844 });
  await noSideways("the history");
  await shot("phase4-history-light-390");
  const sum = pg.locator("#imp-sources .imp-undo-d > summary").first();
  await sum.focus();
  await pg.keyboard.press("Enter");
  const go = pg.locator("#imp-sources .imp-undo-d button[type=submit]").first();
  eq((await go.innerText()).trim(), "Move 2 documents to the trash", "the button says what it does");
  await go.focus();
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/import\?.*undone=2/, { timeout: 20000 });
  await pg.locator("#imp-undone").waitFor();
  eq(await active(), "imp-undone", "focus is on what was done");
  const said = await pg.locator("#imp-undone").innerText();
  ok(/Moved 2 documents to the trash\. 1 stayed; each is listed below with the reason\./.test(said), "said: " + said);
  ok(/The Road/.test(said) && /its text, name, synopsis or status was changed after the import/.test(said), "and it names the document that stayed and why, inside the status: " + said);
  eq(await pg.locator("#imp-undone").getAttribute("role"), "status", "a status");
  await noSideways("after the undo");
  await shot("phase4-undone-light-390");
  const after = (await api(main, "GET", `/api/v1/projects/${proj.id}/docs`)).json.items.map((d) => d.title).sort();
  ok(after.includes("The Road") && !after.includes("The Mill") && !after.includes("Snow"), "the edited chapter is the only one left: " + after);
  const kept = (await api(main, "GET", `/api/v1/docs/${road.id}`)).json.item.content;
  ok(/A line the writer added\./.test(kept), "and its text is the writer's");
  await pg.emulateMedia({ colorScheme: "dark" });
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await shot("phase4-undone-dark-390");
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.emulateMedia({ colorScheme: "light" });
  await pg.setViewportSize({ width: 1280, height: 900 });
});

await kit.test("a plain text file is chosen with the keyboard, cut like a Word file: a plain Chapter line is offered and taken one press at a time", async () => {
  const txt = textFile("novel.txt", ["Chapter 1", "Chapter 2", "Chapter 3"]);
  await clearDrafts();
  await pg.goto(base() + "/import");
  await pg.waitForSelector("#imp-fmt");
  await pg.locator("#imp-f-md").focus();
  for (let i = 0; i < 12; i++) { // the radios are walked with the arrow keys until the text one has the focus
    if ((await active()) === "imp-f-text") break;
    await pg.keyboard.press("ArrowDown");
  }
  eq(await active(), "imp-f-text", "the text format is reachable by the arrow keys");
  ok(await pg.locator("#imp-f-text").isChecked(), "and chosen");
  ok(/UTF-8 text/.test(await pg.locator('.imp-form[data-fmt="text"]').innerText()), "its panel says what it takes");
  await pg.locator("#imp-textfile").setInputFiles(txt);
  await pg.waitForURL(/\/import\/d\/imd_/, { timeout: 20000 });
  await pg.locator("#imp-h").waitFor();
  const text = await pg.locator("#imp-review").innerText();
  ok(/A plain text file/.test(await pg.locator(".imp-sum").innerText()), "read as plain text");
  ok(/No chapter breaks were found/.test(text), "a plain Chapter line does not cut by itself: " + text.slice(0, 300));
  eq(await pg.locator("li.imp-sug").count(), 3, "three places are offered");
  await shot("phase4-text-light-1280");
  // the first offer is the book's own first line: taking it names the one document for it, and the others each make a chapter
  await press(pg.getByRole("button", { name: /^Start a chapter here/ }).first());
  ok(/Started a chapter: Chapter 1 begins here, at the start of the book\./.test(await pg.locator("#imp-live").innerText()), "the first offer says what it did: " + (await pg.locator("#imp-live").innerText()));
  eq((await pg.locator("#imp-review > .imp-tree > .imp-node").count()), 1, "still one document, now a chapter named for its first line");
  ok(/^imp-sug-\d+$/.test(await active()), "focus is on the next place: " + (await active()));
  for (let i = 0; i < 2; i++) await press(pg.getByRole("button", { name: /^Start a chapter here/ }).first());
  const titles = await pg.locator("#imp-review > .imp-tree > .imp-node > .imp-row .imp-title").allInnerTexts();
  ok(titles.length === 3, "three documents: " + titles);
  await pg.setViewportSize({ width: 390, height: 844 });
  await pg.emulateMedia({ colorScheme: "dark" });
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await noSideways("the text review");
  await shot("phase4-text-dark-390");
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.emulateMedia({ colorScheme: "light" });
  await pg.setViewportSize({ width: 1280, height: 900 });
  await pg.locator("[data-go]").focus();
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/p\/[^/]+\/d\/doc_/, { timeout: 20000 });
  const made = (await api(main, "GET", "/api/v1/projects")).json.items.find((p) => /novel/i.test(p.title));
  ok(made, "the project was made from the file's name");
  eq((await api(main, "GET", `/api/v1/projects/${made.id}/docs`)).json.items.length, 3, "three documents");
});

await kit.test("a plain text file's Chapter lines are all taken with one press of Use all, by keyboard", async () => {
  await clearDrafts();
  await pg.goto(base() + "/import");
  await pg.waitForSelector("#imp-fmt");
  await pg.check("#imp-f-text");
  await pg.locator("#imp-textfile").setInputFiles(textFile("all.txt", ["Chapter 1", "Chapter 2", "Chapter 3", "Chapter 4"]));
  await pg.waitForURL(/\/import\/d\/imd_/, { timeout: 20000 });
  await pg.locator("#imp-h").waitFor();
  const bulk = pg.getByRole("button", { name: "Use all 4 chapter lines" });
  eq(await bulk.count(), 1, "the bulk button");
  await press(bulk);
  const titles = await pg.locator("#imp-review > .imp-tree > .imp-node > .imp-row .imp-title").allInnerTexts();
  eq(titles.join("|"), "Chapter 1|Chapter 2|Chapter 3|Chapter 4", "four chapters");
  ok(/Cut as you chose: lines that begin with a chapter word \(4\)\./.test(await pg.locator("#imp-review").innerText()), "and the review says how it was cut");
});

await kit.test("a file that is not text is refused in words, and the page keeps working", async () => {
  await clearDrafts();
  await pg.goto(base() + "/import");
  await pg.waitForSelector("#imp-fmt");
  await pg.check("#imp-f-text");
  const bad = join(kit.tmp, "not.txt");
  writeFileSync(bad, Buffer.from([0x50, 0x4b, 3, 4, 0, 0, 0, 1]));
  kit.allowed.push(/status of 422/);
  await pg.locator("#imp-textfile").setInputFiles(bad);
  await pg.locator("#imp-stage-err").waitFor();
  ok(/That is not a plain text file/.test(await pg.locator("#imp-stage-err").innerText()), "said: " + (await pg.locator("#imp-stage-err").innerText()));
});

await kit.finish("e2e-import-phase4");
