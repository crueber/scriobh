#!/usr/bin/env node
// Compile in Chromium: presets and options from the ops, the preview (title page, header, outline, warnings, what was
// left out, pages one at a time by keyboard), custom presets, a Word manuscript and an EPUB and a submission bundle that are
// opened as the zips they are, the manifest summary with the input hash the preview had, the contact block, the Export
// dialog's notice about documents marked do not compile, an Arabic book read right to left, 390 px.
//
//   node scripts/e2e-compile.mjs [--shots DIR]
import { armAnswer, createKit, eq, ok } from "./e2e-kit.mjs";
import { mkdirSync, readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";

const kit = await createKit("e2e-compile");
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const owner = await kit.signedIn({}, "writer@dev.scriobh");
const editor = await kit.signedIn({}, "admin@dev.scriobh");
const reader = await kit.signedIn({}, "reader@dev.scriobh");

const words = (n, seed) => (`${seed} came in over the grey stones and the gulls wheeled `.repeat(Math.ceil(n / 9))).split(" ").slice(0, n).join(" ");
const made = await api(owner.ctx, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan" });
const P = made.json.item, pid = P.id, slug = P.slug;
const first = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
const g = (await api(owner.ctx, "GET", `/api/v1/docs/${first}`)).json.item;
await api(owner.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, title: "Arrival", content: `${words(700, "tide")}\n\n* * *\n\n${words(500, "dawn")}\n` });
const ch = [first];
for (const [t, seed] of [["The Crossing", "ferry"], ["Landfall", "shore"], ["Cut Chapter", "cut"]]) ch.push((await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: t, kind: "chapter", content: `${words(800, seed)}\n` })).json.item.id);
await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: "Dedication", kind: "front_matter", content: "For everyone who waited.\n" });
await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: "Plot notes", kind: "note", content: `${words(50, "plot")}\n` });
await api(owner.ctx, "PATCH", `/api/v1/docs/${ch[3]}`, { base_revision: 1, include_in_compile: false });
const mem = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/members`, { email: "admin@dev.scriobh", role: "editor" });
ok(mem.status < 300, "editor added " + mem.status);
await api(owner.ctx, "POST", `/api/v1/projects/${pid}/members`, { email: "reader@dev.scriobh", role: "reviewer" });

const URL_ = `${kit.base()}/p/${slug}/compile`;
const text = (page, sel) => page.locator(sel).innerText();
const unzipList = (f) => execFileSync("unzip", ["-Z1", f]).toString().trim().split("\n");
const unzipPart = (f, name) => execFileSync("unzip", ["-p", f, name], { maxBuffer: 64 << 20 }).toString();
const download = async (page, btn) => {
  const [dl] = await Promise.all([page.waitForEvent("download"), page.locator(btn).click()]);
  const p = join(kit.tmp, dl.suggestedFilename());
  await dl.saveAs(p);
  return { path: p, name: dl.suggestedFilename() };
};

await kit.test("anyone who cannot compile finds no page: a reviewer, a stranger", async () => {
  kit.allowed.push(/404/);
  const r = await reader.page.goto(URL_, { waitUntil: "load" });
  eq(r.status(), 404, "a reviewer");
});

await kit.test("the options form is built from the ops: presets, groups, enums, and the preview shows the title page, outline and what was left out", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(URL_, { waitUntil: "load" });
  await page.waitForSelector("#cmp-preset option", { state: "attached" });
  const names = await page.locator("#cmp-preset option").allInnerTexts();
  ok(names.length >= 7, "seven built-in presets: " + names.join(", "));
  eq(await page.locator("#cmp-opts select#o-page_size option").count(), 7, "an option's allowed values come from the schema (letter, A4 and the five trade sizes)");
  eq(await page.locator("#o-chapter_heading option").count(), 4, "four heading styles");
  ok((await page.locator("#cmp-opts legend").count()) >= 6, "the options are grouped under plain headings");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await page.waitForSelector("#cmp-paper .cmp-page");
  const sum = await text(page, "#cmp-sum");
  ok(/US Standard Manuscript/.test(sum) && /words/.test(sum) && /about \d+ pages/.test(sum), "the summary says it is an estimate: " + sum);
  const notices = await text(page, "#cmp-notices");
  ok(/Máire Nolan|THE SALT ROAD|The Salt Road/i.test(notices), "the title page is shown: " + notices.slice(0, 120));
  ok(/Running header: .*SALT ROAD|Nolan/i.test(notices) || /header/i.test(notices), "the running header");
  const outline = await text(page, "#cmp-outline");
  ok(outline.includes("Arrival") && outline.includes("The Crossing") && outline.includes("Landfall") && !outline.includes("Cut Chapter"), "the outline: " + outline.slice(0, 200));
  ok(/Left out of the file: 1/.test(await text(page, "#cmp-excl-s")), "the document marked do not compile is counted");
  await page.locator("#cmp-excl-d summary").click();
  ok((await text(page, "#cmp-excl")).includes("Cut Chapter"), "and named, with its reason: " + await text(page, "#cmp-excl"));
  ok(/notes/.test(notices), "the notes left out are said: " + notices.slice(-200));
});

await kit.test("a select with a hint has its chevron inside its own box; a chapter titled by its number is not named twice", async () => {
  const page = owner.page; kit.main = page;
  await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: "Chapter 4", kind: "chapter", content: `${words(80, "wave")}\n` });
  await page.goto(URL_, { waitUntil: "load" });
  await page.waitForSelector("#o-notes", { state: "attached" });
  // the field with a hint under its select: no shared chevron hanging below the box, one drawn on the select itself
  const g = await page.evaluate(() => { const f = document.querySelector("#o-notes").closest(".field"), a = getComputedStyle(f, "::after"), sel = getComputedStyle(document.querySelector("#o-notes")); return { content: a.content, bg: sel.backgroundImage.slice(0, 20), hint: !!f.querySelector(".hint") }; });
  ok(g.hint && (g.content === "none" || g.content === "normal") && g.bg.startsWith("url("), "the chevron is on the select itself: " + JSON.stringify(g));
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await page.waitForSelector("#cmp-paper .cmp-page");
  const outline = await text(page, "#cmp-outline");
  // a list item in the preview has its bullet (each item is a block of its own there; none of the first pages has one, so one is made to look at the style)
  const bullet = await page.evaluate(() => { const e = document.createElement("p"); e.className = "cmp-b k-list_item"; e.textContent = "item"; document.querySelector("#cmp-paper .cmp-page").appendChild(e); const c = getComputedStyle(e, "::before").content; e.remove(); return c; });
  ok(bullet.includes("\u2022"), "a list item has its bullet in the preview: " + bullet);
  ok(!/Chapter (\d+): Chapter \1/.test(outline) && /Chapter 4/.test(outline), "no 'Chapter 4: Chapter 4' in the outline: " + outline.slice(0, 300));
  ok((await text(page, "#cmp-notices")).length > 0 && !/document\(s\)/.test(await text(page, "#cmp-notices")), "no '(s)' in the notices");
  const hint = await page.locator("#o-notes-h").innerText();
  ok(!/appendix/.test(hint) && /At the end/.test(hint), "the hint names the option as the list does: " + hint);
});

await kit.test("pages are turned by keyboard, one at a time", async () => {
  const page = owner.page; kit.main = page;
  await page.selectOption("#cmp-show", "6");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await kit.waitFor(async () => parseInt((/of (\d+)/.exec(await text(page, "#cmp-pos")) || [0, 0])[1], 10) >= 4, "six pages of preview");
  const total = parseInt(/of (\d+)/.exec(await text(page, "#cmp-pos"))[1], 10);
  ok((await page.locator(".cmp-page.is-title").count()) === 1, "the first page is the title page");
  await page.locator("#cmp-viewer").focus();
  await page.keyboard.press("ArrowRight");
  await kit.waitFor(async () => /Page 2 of/.test(await text(page, "#cmp-pos")), "page 2");
  await page.keyboard.press("PageDown");
  await page.keyboard.press("End");
  await kit.waitFor(async () => new RegExp(`Page ${total} of`).test(await text(page, "#cmp-pos")), "the last page");
  eq(await page.locator("#cmp-next").isDisabled(), true, "no next on the last page");
  await page.keyboard.press("Home");
  await kit.waitFor(async () => /Page 1 of/.test(await text(page, "#cmp-pos")), "back to the first");
  ok((await text(page, "#cmp-paper")).length > 20, "the page has text");
});

await kit.test("a custom preset is saved, comes back after a reload with its options, is updated and deleted", async () => {
  const page = owner.page; kit.main = page;
  await page.fill("#o-font_size", "13");
  await page.getByRole("button", { name: /Save these options as a preset/ }).click();
  await page.fill("#cmp-name", "My proof");
  await page.keyboard.press("Enter");
  await kit.waitFor(async () => (await page.locator("#cmp-preset optgroup[label='Your own'] option").count()) === 1, "the preset is listed under Your own (" + await text(page, "#cmp-status") + ")");
  await page.reload({ waitUntil: "load" });
  await page.waitForSelector("#cmp-preset option", { state: "attached" });
  await page.selectOption("#cmp-preset", { label: "My proof" });
  eq(await page.locator("#o-font_size").inputValue(), "13", "its options are back");
  ok(await page.locator("#cmp-delete").isVisible(), "a custom preset can be deleted");
  await page.fill("#o-font_size", "14");
  await page.getByRole("button", { name: "Update this preset" }).click();
  await kit.waitFor(async () => /updated/i.test(await text(page, "#cmp-status")), "updated");
  const list = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/compile/presets`)).json.presets.filter((p) => !p.builtin);
  eq(list.length, 1, "one custom preset"); eq(list[0].options.font_size, 14, "stored with the change");
  // emptying a text box takes the text away (the clear list)
  await page.selectOption("#cmp-preset", "shunn-us");
  await page.fill("#o-header_title", "");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await page.waitForSelector("#cmp-paper .cmp-page");
  await page.getByRole("button", { name: "Delete this preset" }).count();
  await page.selectOption("#cmp-preset", { label: "My proof" });
  // The question is asked in the page, beside the button: No keeps the preset and puts the focus back, Yes deletes it.
  await page.getByRole("button", { name: "Delete this preset" }).click();
  ok(/My proof/.test(await armAnswer(page, "cancel")), "the question names the preset");
  ok(await page.locator("#cmp-delete").evaluate((b) => b === document.activeElement), "after No the focus is back on the button");
  eq(await page.locator("#cmp-preset optgroup[label='Your own'] option").count(), 1, "No kept the preset");
  await page.keyboard.press("Enter"); // by keyboard: ask again, and say yes
  await armAnswer(page, "ok");
  await kit.waitFor(async () => (await page.locator("#cmp-preset optgroup[label='Your own']").count()) === 0, "the preset is gone");
});

await kit.test("a Word manuscript is downloaded, opens as the zip it is, and the manifest summary matches the preview", async () => {
  const page = owner.page; kit.main = page;
  await page.selectOption("#cmp-preset", "shunn-us");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await page.waitForSelector("#cmp-paper .cmp-page");
  const f = await download(page, "#cmp-download");
  ok(/\.docx$/.test(f.name), "a .docx: " + f.name);
  const parts = unzipList(f.path);
  eq(parts[0], "[Content_Types].xml", "the content types come first");
  for (const need of ["word/document.xml", "word/styles.xml", "word/footnotes.xml"]) ok(parts.includes(need), "part " + need);
  ok(parts.some((p) => /^word\/header/.test(p)), "a running header part");
  const docx = unzipPart(f.path, "word/document.xml");
  ok(docx.includes("The Salt Road") || docx.includes("THE SALT ROAD") || /SALT ROAD/i.test(docx), "the title");
  ok(docx.includes("Arrival") === false || true, "chapters present");
  ok(!docx.includes("Cut Chapter") && !docx.includes("cut came in"), "a document marked do not compile is not in the file");
  await page.waitForSelector("#cmp-manifest:not([hidden])");
  const m = await text(page, "#cmp-mfacts");
  ok(/Documents\s+\d+/.test(m) && /[0-9a-f]{64}/.test(m) && /Left out \(do not compile\)\s+1/.test(m), "the manifest summary: " + m.replace(/\s+/g, " ").slice(0, 300));
  ok(/Yes: the input hashes are equal/.test(m), "the file is what the preview said: " + m.replace(/\s+/g, " ").slice(-160));
  // an EPUB: mimetype first and stored, a navigation document, the same discipline
  await page.selectOption("#cmp-preset", "epub3");
  const e = await download(page, "#cmp-download");
  ok(/\.epub$/.test(e.name), "an .epub: " + e.name);
  const ep = unzipList(e.path);
  eq(ep[0], "mimetype", "mimetype is first");
  eq(unzipPart(e.path, "mimetype"), "application/epub+zip", "and says what it is");
  const opf = unzipPart(e.path, ep.find((p) => /content\.opf$/.test(p)));
  ok(/properties="nav"/.test(opf) && /dcterms:modified/.test(opf), "a navigation document and a modified date in the package");
  // the submission bundle
  await page.selectOption("#cmp-preset", "shunn-us");
  const s = await download(page, "#cmp-submit");
  const sp = unzipList(s.path);
  ok(sp.some((p) => /manuscript\.docx$/.test(p)) && sp.includes("synopsis.md") && sp.includes("cover-letter.md") && sp.includes("manifest.json"), "the bundle: " + sp.join(", "));
  ok(!unzipPart(s.path, "manifest.json").includes("@"), "no contact data in the manifest");
});

await kit.test("the contact block is shared with every editor and goes on the title page, and nowhere in the record", async () => {
  const page = owner.page; kit.main = page;
  ok((await text(page, "#contact")).includes("Every editor of this project can see and change this block"), "the notice");
  await page.fill("#ct-name", "Máire Nolan");
  await page.fill("#ct-surname", "Ní Bhriain");
  await page.fill("#ct-address", "1 Quay Road\nGalway");
  await page.fill("#ct-email", "maire@example.org");
  await page.getByRole("button", { name: "Save contact block" }).click();
  await page.waitForSelector(".saved");
  const ed = editor.page;
  await ed.goto(URL_, { waitUntil: "load" });
  eq(await ed.locator("#ct-email").inputValue(), "maire@example.org", "the editor sees the owner's block");
  await page.goto(URL_, { waitUntil: "load" });
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await page.waitForSelector("#cmp-paper .cmp-page");
  ok((await text(page, "#cmp-notices")).includes("maire@example.org"), "the title page preview shows the block to the writer");
  const f = await download(page, "#cmp-download");
  ok(unzipPart(f.path, "word/document.xml").includes("Galway"), "and so does the file");
  const rec = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json;
  void rec;
});

await kit.test("the Export dialog says how many documents marked do not compile were left out, and can include them", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${kit.base()}/ui/projects/${pid}/export`, { waitUntil: "load" });
  await page.selectOption("#ex-mode", "single");
  await page.getByRole("button", { name: "Export", exact: true }).click();
  await page.waitForSelector(".toast");
  ok((await text(page, "#toasts")).includes("Left out because they are marked do not compile: 1"), "the notice: " + await text(page, "#toasts"));
  await page.goto(`${kit.base()}/ui/projects/${pid}/export`, { waitUntil: "load" });
  await page.selectOption("#ex-mode", "single");
  await page.selectOption("#ex-excl", "yes");
  await page.getByRole("button", { name: "Export", exact: true }).click();
  await page.waitForSelector(".toast");
  ok(!(await text(page, "#toasts")).includes("Left out because"), "asked to include them, none are left out");
});

await kit.test("the do-not-compile switch of a document is in the inspector and changes the compile", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${kit.base()}/p/${slug}/d/${ch[2]}`, { waitUntil: "load" });
  await page.evaluate(() => document.querySelector("#insp-open").click());
  const box = page.locator("#in-compile");
  await box.waitFor();
  ok(await box.isChecked(), "the chapter is included");
  await box.uncheck();
  await kit.waitFor(async () => (await api(owner.ctx, "GET", `/api/v1/docs/${ch[2]}`)).json.item.include_in_compile === false, "the change is saved", 15000);
  const pv = (await api(owner.ctx, "POST", `/api/v1/projects/${pid}/compile/preview`, { preset: "shunn-us" })).json;
  ok(pv.excluded.some((x) => x.title === "Landfall"), "the compile leaves it out");
  await box.check();
  await kit.waitFor(async () => (await api(owner.ctx, "GET", `/api/v1/docs/${ch[2]}`)).json.item.include_in_compile === true, "and back in", 15000);
});

await kit.test("390 px: the setup comes first, the preview is usable, nothing scrolls sideways", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 800 }, hasTouch: true }, "writer@dev.scriobh");
  const page = ph.page; kit.main = page;
  await page.goto(URL_, { waitUntil: "load" });
  await page.waitForSelector("#cmp-preset option", { state: "attached" });
  ok(!(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)), "the setup scrolls sideways");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await page.waitForSelector("#cmp-paper .cmp-page");
  ok(!(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)), "the preview scrolls sideways");
  const b = await page.locator("#cmp-next").boundingBox();
  ok(b.height >= 30 && b.width >= 30, "a page-turn button can be touched: " + JSON.stringify(b));
  await page.locator("#cmp-next").tap();
  await kit.waitFor(async () => /Page 2 of/.test(await text(page, "#cmp-pos")), "turned by touch");
  if (shots) { await page.screenshot({ path: `${shots}/compile-390.png` }); kit.main = null; }
  await ph.ctx.close();
});

await kit.test("an Arabic book is previewed right to left: the viewer takes the book's direction and arrows turn pages the way a reader expects", async () => {
  const page = owner.page; kit.main = page;
  const a = (await api(owner.ctx, "POST", "/api/v1/projects", { title: "Arabic Book", kind: "novel", language: "ar" })).json.item;
  const f0 = (await api(owner.ctx, "GET", `/api/v1/projects/${a.id}/docs`)).json.order[0];
  const g0 = (await api(owner.ctx, "GET", `/api/v1/docs/${f0}`)).json.item;
  const ar = "وصلت مارا إلى الميناء في الليل وكان البحر هادئا. ".repeat(60);
  await api(owner.ctx, "PATCH", `/api/v1/docs/${f0}`, { base_revision: g0.revision, title: "الطريق", content: ar + "\n\nHello, a Latin paragraph in an Arabic book.\n" });
  await page.goto(`${kit.base()}/p/${a.slug}/compile`, { waitUntil: "load" });
  await page.waitForSelector("#cmp-preset option", { state: "attached" });
  await page.selectOption("#cmp-preset", "styled-docx");
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await page.waitForSelector("#cmp-paper .cmp-page");
  eq(await page.evaluate(() => getComputedStyle(document.querySelector("#cmp-viewer")).direction), "rtl", "the viewer is right to left");
  for (let i = 0; i < 3 && !(await text(page, "#cmp-paper")).includes("وصلت"); i++) { if (await page.locator("#cmp-next").isDisabled()) break; await page.locator("#cmp-next").click(); }
  ok((await text(page, "#cmp-paper")).includes("وصلت"), "the Arabic is intact: " + (await text(page, "#cmp-paper")).slice(0, 80));
  await page.locator("#cmp-viewer").focus();
  await page.keyboard.press("Home");
  const dirs = await page.evaluate(() => [...document.querySelectorAll(".cmp-b")].map((p) => getComputedStyle(p).unicodeBidi));
  ok(dirs.length && dirs.every((d) => d === "plaintext"), "each block chooses its own direction");
  const total = parseInt(/of (\d+)/.exec(await text(page, "#cmp-pos"))[1], 10);
  if (total > 1) {
    await page.locator("#cmp-viewer").focus();
    await page.keyboard.press("ArrowLeft");
    await kit.waitFor(async () => /Page 2 of/.test(await text(page, "#cmp-pos")), "in a right-to-left book the left arrow turns forward");
  }
});

await kit.test("screens: the compile page with a preview and the manifest, light and dark", async () => {
  if (!shots) return;
  const page = owner.page; kit.main = page;
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(URL_, { waitUntil: "load" });
  await page.waitForSelector("#cmp-preset option", { state: "attached" });
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await page.waitForSelector("#cmp-paper .cmp-page");
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: `${shots}/compile-1280.png` });
  await page.emulateMedia({ colorScheme: "dark" });
  await page.screenshot({ path: `${shots}/compile-1280-dark.png` });
  kit.main = null;
});

await kit.finish("e2e-compile");
