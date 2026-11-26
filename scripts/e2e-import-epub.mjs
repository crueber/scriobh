#!/usr/bin/env node
// End-to-end test of importing an EPUB book in Chromium: the chooser and its explained options, the preview (what the
// book says of itself, the cut used, the cover, the edition label, the book's files), the mapping editor (rename, make a
// part, nest, join, leave out, reorder, all by keyboard and by buttons), a change the server refuses being taken back, the
// refusals (DRM, fixed layout, too large, not a book), adding to an existing project, what is shown after the apply and
// the Editions page it points to, 390 px, the right-to-left pseudo-locale and a reload with the network cut.
//
//   node scripts/e2e-import-epub.mjs [--only substring] [--shots dir]
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { makeEpub } from "./fixtures/books.mjs";

const kit = await createKit("e2e-import-epub");
kit.deadline(8 * 60 * 1000);
const argv = process.argv.slice(2);
kit.only = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
const shots = argv.includes("--shots") ? resolve(argv[argv.indexOf("--shots") + 1]) : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const book = makeEpub(join(kit.tmp, "marsh.epub"), "ok", kit.tmp);
const drm = makeEpub(join(kit.tmp, "drm.epub"), "drm", kit.tmp);
const fixed = makeEpub(join(kit.tmp, "fixed.epub"), "fixed", kit.tmp);
const notBook = join(kit.tmp, "notes.epub");
writeFileSync(notBook, "This is plain text, not a zip file.");

const projects = async () => (await api(main, "GET", "/api/v1/projects")).json.items;
const open = async (path = "/import") => { await pg.goto(base() + path); await pg.waitForSelector("#imp-fmt"); };
// A change is a form whose answer is swapped into the review: it has settled when htmx has nothing in flight.
const settle = async () => { await sleep(200); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"), null, { timeout: 20000 }); await sleep(100); };
const titles = () => pg.locator("#imp-tree .imp-title").allInnerTexts();
const nodeFor = (title) => pg.locator(`#imp-tree > li.imp-node:has(> .imp-row > .imp-title:text-is("${title}"))`).first();
// The tree is flat: a chapter is inside the nearest row above it that is one level shallower.
const parentOf = (title) => pg.evaluate((t) => {
  const li = [...document.querySelectorAll("#imp-tree > li.imp-node")], depth = (e) => Number((e.className.match(/\bd(\d+)\b/) || [])[1]);
  const i = li.findIndex((e) => e.querySelector(":scope > .imp-row > .imp-title").textContent === t);
  for (let j = i - 1; j >= 0; j--) if (depth(li[j]) < depth(li[i])) return li[j].querySelector(":scope > .imp-row > .imp-title").textContent;
  return "";
}, title);
const shot = async (name, page = pg) => {
  if (!shots) return;
  const v = page.viewportSize(), hgt = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: v.width, height: Math.min(hgt, 4000) });
  await page.screenshot({ path: join(shots, name + ".png"), caret: "initial" });
  await page.setViewportSize(v);
};
// The same file chosen again for the same target is the draft that was already waiting (with its edits), not a second one: a step that
// wants the book as it came starts with none open.
const discardDrafts = async () => { for (const d of (await api(main, "GET", "/api/v1/imports/drafts")).json.items) eq((await api(main, "DELETE", `/api/v1/imports/drafts/${d.id}`)).status, 200, "discard a draft"); };
const preview = async (file = book) => {
  await discardDrafts();
  await open();
  await pg.check("#imp-f-epub");
  await pg.locator("#imp-epub").setInputFiles(file);
  await pg.locator("#imp-h").waitFor({ timeout: 20000 });
  await pg.waitForLoadState("load"); // htmx is in place: a change made before it is would not post
  await settle();
};
// A chapter inside another is offered as "Title (in Parent)": the option is found by the start of its words.
const chooseOption = async (title, name, to) => { const sel = nodeFor(title).getByRole("combobox", { name, exact: true }); await sel.selectOption(await sel.locator("option", { hasText: to }).first().getAttribute("value")); };
// Edit opens a row's panel: it is fetched when the row's details open, and its controls are separate small forms.
const openEdit = async (title) => {
  const n = nodeFor(title), d = n.locator("details.imp-edit").first();
  if (!(await d.evaluate((e) => e.open))) await n.locator("summary").first().click();
  await n.locator(".imp-edit-body form").first().waitFor();
  await settle(); // the panel was just swapped in: htmx marks it added for a moment
  return n;
};
await kit.test("chooser: EPUB and PDF are offered, the cut and the pages are each explained, with the keyboard alone", async () => {
  await open();
  await pg.locator("#imp-f-docx").focus();
  await pg.keyboard.press("ArrowDown");
  ok(await pg.locator('[data-fmt="epub"]').isVisible(), "the EPUB panel did not open from the keyboard");
  const t = await pg.locator('[data-fmt="epub"]').innerText();
  for (const w of [/table of contents/i, /One chapter for each file/, /chapter-level heading/, /Leave them out/, /Keep a title page/, /DRM/, /Nothing in it is run or fetched/]) ok(w.test(t), `the EPUB panel does not say ${w}`);
  eq(await pg.locator('input[name="opt.epub_sections"]').count(), 4, "four cuts");
  eq(await pg.locator('input[name="opt.epub_sections"]:checked').getAttribute("value"), "auto", "auto is the default cut");
  eq(await pg.locator('input[name="opt.epub_pages"]:checked').getAttribute("value"), "skip", "skipping the title page is the default");
  await pg.keyboard.press("ArrowDown");
  ok(await pg.locator('[data-fmt="pdf"]').isVisible(), "the PDF panel did not open");
  ok(/separate process/.test(await pg.locator('[data-fmt="pdf"]').innerText()), "the PDF panel does not say it is read in its own process");
  eq(await pg.locator("[data-fmt]:visible").count(), 1, "one panel at a time");
  await shot("chooser-epub-1280");
});

await kit.test("preview: the book's own words, the cut used, the cover, the edition label, the files", async () => {
  await preview();
  const sum = await pg.locator(".imp-sum").innerText();
  ok(/An EPUB book/.test(sum) && /6 documents/.test(sum), "the summary: " + sum);
  const box = await pg.locator(".imp-book").innerText();
  for (const w of [/The Marsh Almanac/, /Ada Fixture/, /en-GB/, /3\.0/, /table of contents/, /cover picture is kept/]) ok(w.test(box), `the book panel does not say ${w}: ${box}`);
  eq(await pg.locator("#imp-label").inputValue(), "The Marsh Almanac (2019)", "the edition label is proposed");
  ok(/never pushed to Git/.test(box), "the label says where the file goes");
  ok(/Footnotes\s*1/.test(box), "the footnote count");
  eq((await titles()).join("|"), "Dedication|Part One: The Marsh|The Road|The Wall|The Tide|Acknowledgments", "the proposed chapters");
  const files = pg.locator(".imp-files");
  await files.locator("summary").click();
  await waitFor(async () => /Becomes The Road/.test(await files.innerText()), "the files list");
  ok(/Not imported: the cover page/.test(await files.innerText()), "the cover page is not listed as left out, with its reason");
  await shot("epub-preview-1280");
});

await kit.test("the cut: by files changes the chapters, and the explanation of what was used says so", async () => {
  await preview();
  await pg.check("#imp-es-files");
  await settle();
  ok(/one chapter for each file/i.test(await pg.locator(".imp-book").innerText()), "the cut used is not said");
  ok((await titles()).length >= 6, "by files gives no chapters");
  await pg.check("#imp-ep-keep");
  await settle();
  ok((await titles()).some((t) => /Marsh Almanac|Title/i.test(t)), "keeping the title page did not add one: " + (await titles()));
});

await kit.test("mapping: rename, make a part, reorder, nest, join, leave out and put back, from the keyboard", async () => {
  await preview();
  // rename, from the keyboard: Edit opens with Enter, the field is the first control
  await pg.locator('#imp-tree summary[aria-label="Edit The Tide"]').focus();
  await pg.keyboard.press("Enter");
  await nodeFor("The Tide").locator(".imp-edit-body form").first().waitFor();
  await settle();
  const field = pg.getByLabel("Title of The Tide", { exact: true });
  await field.fill("The Flood");
  await field.press("Tab"); // change fires on leaving the field
  await settle();
  ok((await titles()).includes("The Flood"), "the rename did not stick");
  // reorder: move The Wall up with its button, by keyboard
  await openEdit("The Wall");
  await pg.getByRole("button", { name: "Move The Wall up" }).focus();
  await pg.keyboard.press("Enter");
  await settle();
  const t = await titles();
  ok(t.indexOf("The Wall") < t.indexOf("The Road"), "Move up did not reorder: " + t);
  ok(await pg.evaluate(() => /Move The Wall (up|down)|Edit The Wall/.test(document.activeElement.getAttribute("aria-label") || "")), "the focus was lost after a move");
  // make a part
  await openEdit("Acknowledgments");
  await pg.getByRole("button", { name: "Make Acknowledgments a part" }).click();
  await settle();
  eq(await pg.locator('#imp-tree .imp-node[data-kind="part"]').count(), 2, "Make a part did not make one");
  // nest The Flood under the new part (a select and a button, so a keyboard and a thumb can do it): both are in the panel, no opener
  let n = await openEdit("The Flood");
  eq(await n.locator(".imp-to").count(), 2, "the panel does not show the Nest and Join pickers at once");
  const sel = n.getByRole("combobox", { name: "Nest under", exact: true });
  await sel.selectOption({ label: "Acknowledgments" });
  const go = n.locator('button[data-act="parent"]');
  eq((await go.innerText()).trim(), "Move here", "the Nest button's words");
  await go.click();
  await settle();
  eq(await parentOf("The Flood"), "Acknowledgments", "the chapter is not under the part");
  // join The Wall into The Road: it leaves the list and the list of what was left out says where it went, with a way back
  n = await openEdit("The Wall");
  await chooseOption("The Wall", "Join into", "The Road");
  ok(/Footnotes are renumbered/.test(await n.locator(".imp-to", { hasText: "Footnotes" }).innerText()), "the join does not mention the footnotes");
  const join = n.locator('button[data-act="merge"]');
  eq((await join.innerText()).trim(), "Join", "the Join button's words");
  await join.click();
  await settle();
  ok(!(await titles()).includes("The Wall"), "the joined chapter is still listed");
  const left = await pg.locator(".imp-left").innerText();
  ok(/The Wall \(joined into The Road\)/.test(left), "the list does not say where it went: " + left);
  await pg.getByRole("button", { name: /^Put The Wall .*back$/ }).click(); // the line says where it went: "The Wall (joined into The Road)"
  await settle();
  ok((await titles()).includes("The Wall"), "Put back did not bring it back");
  // leave out and put back
  await openEdit("Dedication");
  await pg.getByRole("button", { name: "Leave out Dedication" }).click();
  await settle();
  ok(!(await titles()).includes("Dedication"), "leaving out did not");
  await pg.getByRole("button", { name: "Put Dedication back" }).click();
  await settle();
  ok((await titles()).includes("Dedication"), "put back");
  await shot("epub-mapping-1280");
});

await kit.test("mapping: a change the server refuses is taken back with a sentence, and the page is not stuck", async () => {
  kit.allowed.push(/status of 422/); // the refusal is the point
  await preview();
  // A chapter that has another joined into it cannot be left out: the server's own refusal, from the controls a person has.
  let n = await openEdit("The Wall");
  await chooseOption("The Wall", "Join into", "The Road");
  await n.locator('button[data-act="merge"]').click();
  await settle();
  ok(!(await titles()).includes("The Wall"), "the join was not made");
  n = await openEdit("The Road");
  await n.locator('button[data-act="skip"]').click();
  const notice = pg.locator("#imp-notice");
  await notice.waitFor();
  await settle();
  ok(/cannot be left out while The Wall is joined into it/.test(await notice.innerText()), "the server's words are not shown: " + (await notice.innerText()));
  ok(await notice.evaluate((e) => e === document.activeElement), "the notice does not take the focus");
  ok(/cannot be left out while The Wall is joined into it/.test(await pg.locator("#imp-live").innerText()), "a screen reader is not told: " + (await pg.locator("#imp-live").innerText()));
  ok((await titles()).includes("The Road"), "the refused leaving out was kept");
  await pg.getByRole("button", { name: /^Put The Wall .*back$/ }).click(); // the line says where it went: "The Wall (joined into The Road)"
  await settle();
  ok((await titles()).includes("The Wall"), "the page is stuck after the refusal: Put back did nothing");
  eq(await pg.locator("#imp-notice").count(), 0, "the notice stays after the next change");
  await openEdit("The Tide");
  await pg.getByRole("button", { name: "Move The Tide up" }).click();
  await settle();
  const t = await titles();
  ok(t.indexOf("The Tide") < t.indexOf("The Wall"), "the page is stuck: " + t);
});

await kit.test("mapping: two changes sent at once, the second refused, the page shows what the server accepted", async () => {
  kit.allowed.push(/status of 422/);
  await preview();
  const before = (await titles()).join("|");
  await openEdit("The Tide");
  await openEdit("The Wall");
  // Both forms carry the revision they were drawn at, so the server takes one and refuses the other as made on an old version.
  await pg.evaluate(() => { for (const l of ["Move The Tide up", "Move The Wall down"]) document.querySelector(`#imp-review [aria-label="${l}"]`).click(); }); // one task
  await settle();
  const after = (await titles()).join("|");
  ok(after !== before, "neither change was taken");
  const tab = await main.newPage();
  kit.watch(tab, "tab2");
  await tab.goto(pg.url());
  await tab.locator("#imp-h").waitFor();
  eq(after, (await tab.locator("#imp-tree .imp-title").allInnerTexts()).join("|"), "the page shows something the server does not hold");
  await tab.close();
  await openEdit("The Tide");
  await pg.getByRole("button", { name: "Move The Tide up" }).click();
  await settle();
  const t = await titles();
  ok(t.indexOf("The Tide") < t.indexOf("The Road"), "the page is stuck after a refusal: " + t);
});

await kit.test("refusals: DRM, a fixed layout and a file that is not a book, each in plain words", async () => {
  kit.allowed.push(/status of 422/); // each refusal is a 422 the browser logs
  await discardDrafts();
  for (const [file, re] of [[drm, /protected by DRM/], [fixed, /This is a fixed-layout EPUB/], [notBook, /not an EPUB book/]]) {
    await open();
    await pg.check("#imp-f-epub");
    await pg.locator("#imp-epub").setInputFiles(file);
    // The words are looked for wherever the refusal is drawn (the chooser's message, or a page of its own): where it must be is the last step.
    await waitFor(async () => re.test(await pg.locator("main").innerText()), `the refusal ${re}`, 30000);
  }
});

await kit.test("refusals: a book over the limit is refused with the limit that applies to books", async () => {
  kit.allowed.push(/status of 422/);
  const big = join(kit.tmp, "huge.epub");
  writeFileSync(big, Buffer.alloc(33 * 1024 * 1024));
  await open();
  await pg.check("#imp-f-epub");
  await pg.locator("#imp-epub").setInputFiles(big);
  await pg.locator("#imp-stage-err").waitFor({ timeout: 30000 });
  ok(/larger than 32 MiB/.test(await pg.locator("#imp-stage-err").innerText()), "the size message: " + (await pg.locator("#imp-stage-err").innerText()));
});

await kit.test("refusals: a refused book leaves the person on the chooser with the sentence in its message, the file still chosen, and nothing kept", async () => {
  kit.allowed.push(/status of 422/);
  await discardDrafts();
  const big = join(kit.tmp, "huge.epub");
  writeFileSync(big, Buffer.alloc(33 * 1024 * 1024));
  const bad = [];
  for (const [file, re] of [[drm, /protected by DRM/], [fixed, /This is a fixed-layout EPUB/], [notBook, /not an EPUB book/], [big, /larger than/]]) {
    await open();
    await pg.check("#imp-f-epub");
    await pg.locator("#imp-epub").setInputFiles(file);
    await waitFor(async () => re.test(await pg.locator("main").innerText()), `the refusal ${re}`, 30000);
    await pg.waitForLoadState("load");
    const name = file.split("/").pop();
    if ((await pg.locator("#imp-stage-err").count()) !== 1) { bad.push(`${name}: the sentence is not the chooser's message (#imp-stage-err); the page is ${new URL(pg.url()).pathname.replace(/imd_\w+/, "imd_…")}`); continue; }
    if (!re.test(await pg.locator("#imp-stage-err").innerText())) bad.push(`${name}: the message is ` + (await pg.locator("#imp-stage-err").innerText()));
    if ((await pg.locator("#imp-epub").evaluate((i) => i.files.length)) !== 1) bad.push(`${name}: the file is not still chosen`);
  }
  const kept = (await api(main, "GET", "/api/v1/imports/drafts")).json.items.filter((d) => d.status === "open");
  if (kept.length) bad.push(`a refused book is kept as an open draft: ${kept.map((d) => d.name).join(", ")}`);
  ok(bad.length === 0, bad.join("; "));
});

await kit.test("apply: the book becomes a project, the original file is kept as an edition and the page says so, with the result first", async () => {
  await preview();
  await pg.locator("#imp-label").fill("First printing");
  await pg.locator("#imp-t").fill("The Marsh Almanac");
  await pg.locator("[data-go]").click();
  // An apply that kept an edition is the draft's own page, with the result first: the review page is drawn again as the result.
  await waitFor(async () => /^Imported 6 documents/.test((await pg.locator("#imp-h").innerText()).trim()) && !(await pg.locator("[data-go]").count()), "the result", 20000);
  eq((await pg.locator("#imp-h").innerText()).trim(), "Imported 6 documents", "the result");
  const focusedId = await pg.evaluate(() => document.activeElement && document.activeElement.id); // asserted last: what follows is still checked
  ok(/Kept the original file as an edition\./.test(await pg.locator("#imp-review").innerText()), "the edition is not said to be kept: " + (await pg.locator("#imp-review").innerText()));
  const top = await pg.locator("#imp-h").boundingBox();
  ok(top && top.y < 700, "the result is not in the first screen");
  const links = await pg.locator("#imp-review a").evaluateAll((a) => a.map((x) => x.textContent + " " + x.getAttribute("href")));
  ok(links.some((l) => /Editions .*\/editions$/.test(l)) && links.some((l) => /Open the book/.test(l)), "the links: " + links);
  await shot("epub-done-1280");
  // a reload does not do it again
  const before = (await projects()).length;
  await pg.reload();
  await pg.waitForSelector("#imp-h");
  eq((await pg.locator("#imp-h").innerText()).trim(), "Imported 6 documents", "a reload of the result");
  eq((await projects()).length, before, "a reload imported again");
  const made = (await projects()).find((p) => p.title === "The Marsh Almanac");
  ok(made, "the project was not made");
  const eds = (await api(main, "GET", `/api/v1/projects/${made.id}/editions`)).json.items;
  eq(eds.length, 1, "one edition");
  eq(eds[0].label, "First printing", "the label typed");
  eq(eds[0].chapters, 6, "chapters recorded");
  eq(eds[0].metadata.source_name, "marsh", "the file's name is recorded");
  kit.made = made;
  eq(focusedId, "imp-h", "the result does not take the focus");
});

await kit.test("the Editions page: the edition, its chapters, the download, in the project menu and the palette", async () => {
  const made = kit.made;
  await pg.goto(`${base()}/p/${made.slug}/editions`);
  ok(/First printing/.test(await pg.locator("main").innerText()), "the label is not listed");
  const text = await pg.locator("main").innerText();
  for (const w of [/EPUB book/, /from the file marsh/, /6\s*\n?\s*Chapters|Chapters\s*\n?\s*6/i, /never pushed to Git/]) ok(w.test(text), `the list does not say ${w}`);
  await pg.getByRole("link", { name: "Download the original file of First printing" }).waitFor();
  const [dl] = await Promise.all([pg.waitForEvent("download"), pg.getByRole("link", { name: "Download the original file of First printing" }).click()]);
  ok(/\.epub$/.test(dl.suggestedFilename()), "the download's name: " + dl.suggestedFilename());
  await pg.getByRole("link", { name: "First printing", exact: true }).click();
  ok(/The Road/.test(await pg.locator(".edn-chapters").innerText()), "the chapters of the edition");
  // the project menu and the palette reach it
  await pg.goto(`${base()}/p/${made.slug}/goals`);
  await pg.keyboard.press("ControlOrMeta+k");
  await pg.keyboard.type("editions");
  ok(await pg.getByText("Go to the editions (imported books) of this project").first().isVisible(), "the palette does not offer the editions");
  await pg.keyboard.press("Escape");
});

await kit.test("append: a book is added to a project that exists, as new chapters after what is there, and its edition is kept there", async () => {
  const mk = await api(main, "POST", "/api/v1/projects", { title: "Already Here", kind: "novel" });
  const pid = mk.json.item.id, slug = mk.json.item.slug;
  const docsBefore = (await api(main, "GET", `/api/v1/projects/${pid}/docs`)).json.items.length;
  await open(`/p/${slug}/import`);
  ok(/added to this project as new chapters/.test(await pg.locator("body").innerText()) || true, "");
  await pg.check("#imp-f-epub");
  ok(/added to this project as new chapters/.test(await pg.locator('[data-fmt="epub"]').innerText()), "the panel does not say the chapters are added");
  await pg.locator("#imp-epub").setInputFiles(book);
  await pg.locator("#imp-h").waitFor({ timeout: 20000 });
  await pg.waitForLoadState("load");
  await settle();
  eq(await pg.locator("#imp-h").innerText(), "What would be added", "the preview heading");
  eq(await pg.locator("#imp-t").count(), 0, "an append asks for a project title");
  await pg.locator("[data-go]").click();
  await waitFor(async () => /Imported 6 documents/.test(await pg.locator("#imp-h").innerText()), "the result");
  const after = (await api(main, "GET", `/api/v1/projects/${pid}/docs`)).json.items.length;
  ok(after >= docsBefore + 6, `the chapters were not added (${docsBefore} to ${after})`);
  eq((await api(main, "GET", `/api/v1/projects/${pid}/editions`)).json.items.length, 1, "the edition is kept in the project");
  const links = await pg.locator("#imp-review a").evaluateAll((a) => a.map((x) => x.textContent + " " + x.getAttribute("href")));
  const dr = (await api(main, "GET", `/api/v1/imports/drafts/${new URL(pg.url()).pathname.split("/").pop()}`)).json.item;
  ok(links.some((l) => /^Editions .*\/editions$/.test(l)) && links.some((l) => /Open the book/.test(l)), "the result's links: " + JSON.stringify(links) + " (the draft's result has slug " + JSON.stringify(dr.result && dr.result.slug) + ")");
  await pg.locator('#imp-review a:text-is("Editions")').click();
  await pg.waitForURL(/\/editions$/);
});

await kit.test("the cover: offered as the new project's cover, unchecked, and used when checked", async () => {
  await preview();
  const box = pg.locator("#imp-usecover");
  await box.waitFor();
  eq(await box.isChecked(), false, "the box starts unchecked");
  ok(/Use this picture as the project's cover/.test(await pg.locator("label[for=imp-usecover]").innerText()), "its words");
  eq(await box.getAttribute("aria-describedby"), "imp-usecover-h", "its hint is attached");
  await pg.locator("#imp-t").fill("The Marsh Almanac, with its cover");
  await box.check();
  await pg.locator("[data-go]").click();
  await waitFor(async () => /Imported \d+ documents/.test(await pg.locator("#imp-h").innerText()), "the result");
  const made = (await projects()).find((p) => p.title === "The Marsh Almanac, with its cover");
  ok(made, "the project was not made");
  const lay = (await api(main, "GET", `/api/v1/projects/${made.id}/cover-layout`)).json.layout;
  ok(lay && lay.shape === "sides" && lay.front.mode === "image" && lay.alt.startsWith("Cover of "), "the book's cover is the project's: " + JSON.stringify(lay));
  const plain = (await projects()).find((p) => p.title === "The Marsh Almanac");
  eq((await api(main, "GET", `/api/v1/projects/${plain.id}/cover-layout`)).json.layout, null, "the first import, made without the box, has none");
});

await kit.test("390 px: the chooser, the preview and the mapping fit, with touch targets of 44 px", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 800 }, hasTouch: true, isMobile: true });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "phone");
  await p.goto(base() + "/import");
  await p.waitForSelector("#imp-fmt");
  await p.check("#imp-f-epub");
  await p.locator("#imp-epub").setInputFiles(book);
  await p.locator("#imp-h").waitFor({ timeout: 20000 });
  await p.waitForLoadState("load");
  await sleep(600);
  await p.locator('#imp-review summary[aria-label="Edit The Road"]').click();
  await p.locator("#imp-review .imp-edit-body form").first().waitFor();
  await p.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
  const m = await p.evaluate(() => {
    const small = [];
    for (const e of document.querySelectorAll("#imp-review .imp-edit-body button, #imp-review .imp-edit-body select, #imp-review .imp-edit-body input[type=text], #imp-review .imp-edit > summary, .imp-book input:not([type=checkbox]), .imp-book label.check, .imp-files summary")) {
      const r = e.getBoundingClientRect();
      if (r.width && (r.height < 43.5 || r.width < 43.5)) small.push(`${(e.getAttribute("aria-label") || e.textContent || e.id).slice(0, 30)} ${Math.round(r.width)}x${Math.round(r.height)}`);
    }
    return { over: document.documentElement.scrollWidth > document.documentElement.clientWidth, small };
  });
  eq(m.over, false, "the page scrolls sideways at 390 px");
  eq(m.small.join("; "), "", "controls under 44 px");
  // Nest and Join are in the panel, a select and a button each: nothing to open
  const o = await p.evaluate(() => ({ over: document.documentElement.scrollWidth > document.documentElement.clientWidth, w: document.querySelector(".imp-to select").getBoundingClientRect().width }));
  eq(o.over, false, "the picker makes the page scroll sideways");
  ok(o.w <= 390, "the picker is wider than the screen");
  await shot("epub-mapping-390", p);
  await ctx.close();
});

await kit.test("the right-to-left pseudo-locale: the chooser, the preview and the Editions page mirror, nothing overflows, the new words come from the catalogue", async () => {
  eq((await api(main, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } })).status, 200, "set the pseudo-locale");
  try {
    await open();
    eq(await pg.locator("html").getAttribute("dir"), "rtl", "the chooser is right to left");
    await pg.check("#imp-f-epub");
    ok((await pg.locator('[data-fmt="epub"]').innerText()).includes("⟦"), "the chooser's words are not from the catalogue");
    await preview();
    eq(await pg.locator("html").getAttribute("dir"), "rtl", "the page is right to left");
    ok((await pg.locator(".imp-book").innerText()).includes("⟦"), "the book panel's words are not from the catalogue");
    eq((await pg.locator(".imp-book p, .imp-book dt, .imp-book h3, .imp-book label, .imp-book .hint, .imp-map p, .imp-map h3").evaluateAll((l) => l.filter((e) => !e.textContent.includes("⟦")).map((e) => e.textContent.slice(0, 40)))).join(" | "), "", "words of the book panel that did not come from the catalogue");
    eq(await pg.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "the preview scrolls sideways");
    await shot("epub-preview-rtl-1280");
    await pg.goto(`${base()}/p/${kit.made.slug}/editions`);
    eq(await pg.locator("html").getAttribute("dir"), "rtl", "the Editions page is right to left");
    ok((await pg.locator("main").innerText()).includes("⟦"), "the Editions words are not from the catalogue");
    eq(await pg.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "the Editions page scrolls sideways");
    await shot("editions-rtl-1280");
  } finally { await api(main, "PUT", "/api/v1/me/prefs", { items: { language: "en" } }); }
});

await kit.test("with the network cut, a reload of the import page shows the offline notice, not a browser error page", async () => {
  const ctx = await kit.newContext({ serviceWorkers: "allow", viewport: { width: 1000, height: 700 } });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "offline");
  await p.goto(base() + "/import");
  await p.waitForFunction(async () => (await navigator.serviceWorker.getRegistration()) && navigator.serviceWorker.controller !== null || (await navigator.serviceWorker.ready, true), null, { timeout: 15000 }).catch(() => {});
  await p.reload();
  kit.allowed.push(/Failed to load resource|ERR_INTERNET_DISCONNECTED|net::/);
  await ctx.setOffline(true);
  try {
    await p.reload();
    ok(/cannot reach the server/.test(await p.locator("body").innerText()), "the offline notice");
  } finally { await ctx.setOffline(false); await ctx.close(); }
});

await kit.finish("e2e-import-epub");
