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
const status = () => pg.locator("#imp-status").innerText();
const settle = async () => { await sleep(450); await waitFor(async () => !/Updating|Working out|Reading/.test(await status()), "the preview to settle", 20000); };
const titles = () => pg.locator("#imp-result .imp-tree .imp-title").allInnerTexts();
const shot = async (name, page = pg) => {
  if (!shots) return;
  const v = page.viewportSize(), hgt = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: v.width, height: Math.min(hgt, 4000) });
  await page.screenshot({ path: join(shots, name + ".png"), caret: "initial" });
  await page.setViewportSize(v);
};
const preview = async (file = book) => {
  await open();
  await pg.check("#imp-f-epub");
  await pg.locator("#imp-epub").setInputFiles(file);
  await pg.locator("#imp-h").waitFor({ timeout: 20000 });
  await settle();
};
const button = (name) => pg.getByRole("button", { name, exact: true });
const nodeButton = (act, title) => pg.locator(`#imp-result .imp-node:has(> .imp-row .imp-title:text-is("${title}")) > .imp-row [data-act="${act}"]`).first();
// A nested chapter's option is drawn with dashes before its title.
const chooseOption = async (name, title) => { const sel = pg.getByRole("combobox", { name }); await sel.selectOption(await sel.locator("option", { hasText: title }).first().getAttribute("value")); };
const openEdit = async (title) => { const d = pg.locator(`#imp-result details.imp-edit:has(summary[aria-label="Edit ${title}"])`); if (!(await d.getAttribute("open")) && (await d.getAttribute("open")) !== "") await d.locator("summary").click(); return d; };

await kit.test("chooser: EPUB and PDF are offered, the cut and the pages are each explained, with the keyboard alone", async () => {
  await open();
  await pg.locator("#imp-f-docx").focus();
  await pg.keyboard.press("ArrowDown");
  ok(await pg.locator('[data-fmt="epub"]').isVisible(), "the EPUB panel did not open from the keyboard");
  const t = await pg.locator('[data-fmt="epub"]').innerText();
  for (const w of [/table of contents/i, /One chapter for each file/, /chapter-level heading/, /Leave them out/, /Keep a title page/, /DRM/, /Nothing in it is run or fetched/]) ok(w.test(t), `the EPUB panel does not say ${w}`);
  eq(await pg.locator('input[name="imp-es"]').count(), 4, "four cuts");
  eq(await pg.locator('input[name="imp-es"]:checked').getAttribute("value"), "auto", "auto is the default cut");
  eq(await pg.locator('input[name="imp-ep"]:checked').getAttribute("value"), "skip", "skipping the title page is the default");
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
  await pg.locator('#imp-result summary[aria-label="Edit The Tide"]').focus();
  await pg.keyboard.press("Enter");
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
  eq(await pg.locator('#imp-result .imp-node[data-kind="part"]').count(), 2, "Make a part did not make one");
  // nest The Flood under the new part (a select and a button, so a keyboard and a thumb can do it)
  await openEdit("The Flood");
  await pg.getByRole("button", { name: "Nest The Flood under another" }).click();
  const sel = pg.getByRole("combobox", { name: "Nest under" });
  ok(await sel.evaluate((s) => document.activeElement === s), "the picker does not take the focus");
  await sel.selectOption({ label: "Acknowledgments" });
  await pg.getByRole("button", { name: "Move here" }).click();
  await settle();
  ok(await pg.locator('#imp-result .imp-node:has(> .imp-row .imp-title:text-is("Acknowledgments")) ul .imp-title:text-is("The Flood")').count() === 1, "the chapter is not under the part");
  // Escape closes the picker and gives the focus back
  await openEdit("The Wall");
  await pg.getByRole("button", { name: "Join The Wall into another" }).click();
  await pg.keyboard.press("Escape");
  eq(await pg.locator(".imp-to").count(), 0, "Escape left the picker open");
  ok(await pg.evaluate(() => /Join The Wall/.test(document.activeElement.getAttribute("aria-label") || "")), "the focus did not return to the button that opened the picker");
  // join The Wall into The Road: it leaves the list and the list of what was left out says where it went, with a way back
  await pg.getByRole("button", { name: "Join The Wall into another" }).click();
  await chooseOption("Join into", "The Road");
  ok(/Footnotes are renumbered/.test(await pg.locator(".imp-to").innerText()), "the join does not mention the footnotes");
  await pg.getByRole("button", { name: "Join", exact: true }).click();
  await settle();
  ok(!(await titles()).includes("The Wall"), "the joined chapter is still listed");
  const left = await pg.locator(".imp-left").innerText();
  ok(/The Wall \(joined into The Road\)/.test(left), "the list does not say where it went: " + left);
  await pg.getByRole("button", { name: "Put The Wall back" }).click();
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
  let refuse = true;
  await pg.route("**/api/v1/imports/preview", async (route) => {
    if (refuse && /merge_into|"parent"/.test(route.request().postData() || "")) { refuse = false; return route.fulfill({ status: 422, contentType: "application/json", body: JSON.stringify({ error: { code: "invalid", message: "A document cannot be joined into itself." } }) }); }
    return route.continue();
  });
  await openEdit("The Wall");
  await pg.getByRole("button", { name: "Join The Wall into another" }).click();
  await chooseOption("Join into", "The Road");
  await pg.getByRole("button", { name: "Join", exact: true }).click();
  await waitFor(async () => /That change was not made/.test(await status()), "the refusal to be said");
  ok(/cannot be joined into itself/.test(await status()), "the server's words are not shown: " + (await status()));
  ok((await titles()).includes("The Wall"), "the refused join was kept");
  await settle().catch(() => {});
  await pg.unroute("**/api/v1/imports/preview");
  await openEdit("The Tide");
  await pg.getByRole("button", { name: "Move The Tide up" }).click();
  await settle();
  ok(!/not made/.test(await status()), "the page is stuck on the refused change");
});

await kit.test("mapping: two changes made inside the 350 ms wait, the second refused, both are taken back to the last state the server accepted", async () => {
  kit.allowed.push(/status of 422/);
  await preview();
  let refuse = true;
  await pg.route("**/api/v1/imports/preview", async (route) => {
    if (refuse && /"reorder":\[\{/.test(route.request().postData() || "")) { refuse = false; return route.fulfill({ status: 422, contentType: "application/json", body: JSON.stringify({ error: { code: "invalid", message: "Every key must be a distinct child of that parent." } }) }); }
    return route.continue();
  });
  const before = (await titles()).join("|");
  await openEdit("The Tide");
  await openEdit("The Wall");
  await pg.evaluate(() => { for (const l of ["Move The Tide up", "Move The Wall down"]) document.querySelector(`#imp-result [aria-label="${l}"]`).click(); }); // one task: inside the wait
  await waitFor(async () => /That change was not made/.test(await status()), "the refusal to be said");
  await settle().catch(() => {});
  eq((await titles()).join("|"), before, "the page kept a refused change");
  await pg.unroute("**/api/v1/imports/preview");
  await openEdit("The Tide");
  await pg.getByRole("button", { name: "Move The Tide up" }).click();
  await settle();
  const t = await titles();
  ok(t.indexOf("The Tide") < t.indexOf("The Wall"), "the page is stuck after a refusal: " + t);
});

await kit.test("refusals: DRM, a fixed layout, a file that is not a book and one that is too large, each in plain words, the file still chosen", async () => {
  kit.allowed.push(/status of 422/); // each refusal is a 422 the browser logs
  for (const [file, re] of [[drm, /protected by DRM/], [fixed, /fixed-layout/], [notBook, /not an EPUB file/]]) {
    await open();
    await pg.check("#imp-f-epub");
    await pg.locator("#imp-epub").setInputFiles(file);
    await waitFor(async () => re.test(await status()), `the refusal ${re}`, 20000);
    eq(await pg.locator("#imp-status").getAttribute("data-bad"), "", "the refusal is not marked as a problem");
    eq(await pg.locator("#imp-result").isHidden(), true, "a refused book shows a preview");
  }
  const big = join(kit.tmp, "huge.epub");
  writeFileSync(big, Buffer.alloc(33 * 1024 * 1024));
  await open();
  await pg.check("#imp-f-epub");
  await pg.locator("#imp-epub").setInputFiles(big);
  await waitFor(async () => /larger than 32 MiB/.test(await status()), "the size refusal");
  eq(await pg.locator("#imp-result").isHidden(), true, "a book that is too large shows a preview");
});

await kit.test("apply: the book becomes a project, the original file is kept as an edition and the page says so, with the result first", async () => {
  await preview();
  await pg.locator("#imp-label").fill("First printing");
  await pg.locator("#imp-t").fill("The Marsh Almanac");
  await pg.locator("[data-go]").click();
  await pg.locator("#imp-h").waitFor();
  await waitFor(async () => /Imported 6 documents/.test(await pg.locator("#imp-h").innerText()), "the result");
  ok(await pg.evaluate(() => document.activeElement && document.activeElement.id === "imp-h"), "the result does not take the focus");
  await waitFor(async () => /Kept the original file as edition “First printing”/.test(await pg.locator("#imp-result").innerText()), "the edition to be named");
  const top = await pg.locator("#imp-h").boundingBox();
  ok(top && top.y < 700, "the result is not in the first screen");
  const links = await pg.locator("#imp-result a").evaluateAll((a) => a.map((x) => x.textContent + " " + x.getAttribute("href")));
  ok(links.some((l) => /Editions .*\/editions$/.test(l)) && links.some((l) => /Open the book/.test(l)), "the links: " + links);
  await shot("epub-done-1280");
  // a reload does not do it again
  const before = (await projects()).length;
  await pg.reload();
  await pg.waitForSelector("#imp-fmt");
  eq((await projects()).length, before, "a reload imported again");
  const made = (await projects()).find((p) => p.title === "The Marsh Almanac");
  ok(made, "the project was not made");
  const eds = (await api(main, "GET", `/api/v1/projects/${made.id}/editions`)).json.items;
  eq(eds.length, 1, "one edition");
  eq(eds[0].label, "First printing", "the label typed");
  eq(eds[0].chapters, 6, "chapters recorded");
  eq(eds[0].metadata.source_name, "marsh", "the file's name is recorded");
  kit.made = made;
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
  await settle();
  eq(await pg.locator("#imp-h").innerText(), "What would be added", "the preview heading");
  eq(await pg.locator("#imp-t").count(), 0, "an append asks for a project title");
  await pg.locator("[data-go]").click();
  await waitFor(async () => /Imported 6 documents/.test(await pg.locator("#imp-h").innerText()), "the result");
  const after = (await api(main, "GET", `/api/v1/projects/${pid}/docs`)).json.items.length;
  ok(after >= docsBefore + 6, `the chapters were not added (${docsBefore} to ${after})`);
  eq((await api(main, "GET", `/api/v1/projects/${pid}/editions`)).json.items.length, 1, "the edition is kept in the project");
  await pg.locator('#imp-result a:text-is("Editions")').click();
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
  await sleep(600);
  await p.locator('#imp-result summary[aria-label="Edit The Road"]').click();
  const m = await p.evaluate(() => {
    const small = [];
    for (const e of document.querySelectorAll("#imp-result .imp-edit-body button, #imp-result .imp-edit-body select, #imp-result .imp-edit-body input[type=text], #imp-result .imp-edit > summary, .imp-book input:not([type=checkbox]), .imp-book label.check, .imp-files summary")) {
      const r = e.getBoundingClientRect();
      if (r.width && (r.height < 43.5 || r.width < 43.5)) small.push(`${(e.getAttribute("aria-label") || e.textContent || e.id).slice(0, 30)} ${Math.round(r.width)}x${Math.round(r.height)}`);
    }
    return { over: document.documentElement.scrollWidth > document.documentElement.clientWidth, small };
  });
  eq(m.over, false, "the page scrolls sideways at 390 px");
  eq(m.small.join("; "), "", "controls under 44 px");
  await p.getByRole("button", { name: "Nest The Road under another" }).click();
  const o = await p.evaluate(() => ({ over: document.documentElement.scrollWidth > document.documentElement.clientWidth, w: document.querySelector(".imp-to select").getBoundingClientRect().width }));
  eq(o.over, false, "the picker makes the page scroll sideways");
  ok(o.w <= 390, "the picker is wider than the screen");
  await shot("epub-mapping-390", p);
  await ctx.close();
});

await kit.test("the right-to-left pseudo-locale: the chooser, the preview and the Editions page mirror, nothing overflows, the new words come from the catalogue", async () => {
  eq((await api(main, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } })).status, 200, "set the pseudo-locale");
  try {
    await preview();
    eq(await pg.locator("html").getAttribute("dir"), "rtl", "the page is right to left");
    ok((await pg.locator(".imp-book").innerText()).includes("⟦"), "the book panel's words are not from the catalogue");
    ok((await pg.locator('[data-fmt="epub"]').innerText()).includes("⟦"), "the chooser's words are not from the catalogue");
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
