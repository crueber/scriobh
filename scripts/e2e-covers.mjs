#!/usr/bin/env node
// End-to-end test of the cover page in a real Chromium (docs/COVERS.md, phase C1's cover page): a front picture chosen
// and saved, the thumbnail in the manuscript tree and the library, the page mode and its live preview against the
// server's own drawing, a wrap and its measured spine, a refused SVG as a sentence, the way back to the generated cover,
// a stale save, the page with no script, the keyboard alone at 390 px, and what a reload with the network cut shows.
//
//   node scripts/e2e-covers.mjs [--only substring]
import { createKit, eq, ok, armAnswer } from "./e2e-kit.mjs";
import { picture, wrap, SVG, wrapPng, harbourPng } from "./covers-pictures.mjs";
import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const kit = await createKit("e2e-covers");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

let book, front, wide, wrapPic;
const cover = () => `${base()}/p/${book.slug}/cover`;
const save = () => pg.getByRole("button", { name: "Save the cover" }).click();
const msg = (p = pg) => p.locator("#cv-msg");
const layout = async () => (await api(main, "GET", `/api/v1/projects/${book.id}/cover-layout`)).json.layout;
// the live preview's picture and the server's own drawing of the same draft: the image and clip attributes
const placed = (p = pg) => p.evaluate(() => {
  const svg = document.querySelector("#cv-pv-page .cv-svg");
  if (!svg) return null;
  const im = svg.querySelector("image"), cl = svg.querySelector("clipPath rect");
  const a = (e, ...k) => (e ? k.map((n) => e.getAttribute(n)).join(",") : "");
  return { view: svg.getAttribute("viewBox"), image: a(im, "x", "y", "width", "height", "preserveAspectRatio"), clip: a(cl, "x", "y", "width", "height"), bg: svg.getAttribute("class") };
});

await kit.test("setup: a project, and the cover page for a project with no layout", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan" });
  ok(made.json && made.json.item, "project: " + made.status);
  book = made.json.item;
  await pg.goto(cover());
  eq(await pg.locator("h1").innerText(), "Cover", "the page's heading");
  ok(await pg.locator("#cv-shape-generated").isChecked(), "the generated cover is the chosen shape");
  ok(!(await pg.locator("#cv-front").isVisible()), "the sides are not offered while the cover is generated");
  ok(await pg.locator("#cv-pv-page .cv-generated").isVisible(), "the preview is the generated cover");
  ok((await pg.locator(".tree-cover").count()) === 0, "no tree on this page");
  front = await picture(pg, { w: 1200, h: 1800 });
  wide = await picture(pg, { w: 1800, h: 1100, palette: "dusk" });
  const w = await wrap(pg, { dpi: 150 });
  wrapPic = w.buffer;
});

await kit.test("a front picture: chosen, added at once, described, saved; the thumbnail is in the tree and the library", async () => {
  kit.allowed = [/status of 422/]; // the refusal below is a 422 page, as it should be
  await pg.goto(cover());
  await pg.locator("#cv-shape-sides").check();
  await pg.locator("#cv-front-mode input[value=image]").check();
  const nav = pg.waitForNavigation();
  await pg.locator("#cv-front-file").setInputFiles({ name: "front.jpg", mimeType: "image/jpeg", buffer: front });
  await nav;
  ok(/Added front\.jpg as the front picture/.test(await pg.locator(".cv-note").innerText()), "the page says the picture was added");
  eq(await pg.locator("#cv-front-mode input[value=image]").isChecked(), true, "image mode stays chosen");
  ok(await pg.locator("#cv-pv-page .cv-svg image").count(), "the draft is drawn on its page");
  eq(await layout(), null, "nothing is saved yet");
  // no description: refused in words, the picture kept
  await save();
  await msg().waitFor();
  ok(/Describe the cover in a sentence/.test(await msg().innerText()), "the refusal names what to do: " + (await msg().innerText()));
  eq(await pg.evaluate(() => document.activeElement && document.activeElement.id), "cv-msg", "focus moves to the sentence");
  eq(await pg.locator("#cv-alt").getAttribute("aria-invalid"), "true", "the field is marked");
  await pg.locator("#cv-alt").fill("A sailing boat at dusk");
  await save();
  await pg.waitForSelector("#cv-flash");
  eq(await pg.locator("#cv-flash").innerText(), "The cover is saved.", "saved");
  const l = await layout();
  eq(l.front.mode, "image", "the saved mode");
  eq(l.alt, "A sailing boat at dusk", "the saved description");
  // the saved page shows the server's own picture at the library's size and at a page's size
  await waitFor(() => pg.locator(".cv-preview img").evaluateAll((a) => a.length === 2 && a.every((i) => i.complete && i.naturalWidth > 0)), "both previews to load");
  // the tree's fixed Cover row, above the first document
  await pg.goto(`${base()}/p/${book.slug}`);
  const row = pg.locator("#tree-cover");
  await row.waitFor();
  ok(await row.locator("img").evaluate((i) => i.complete && i.naturalWidth > 0), "the tree row has the thumbnail");
  ok((await row.innerText()).includes("Cover") && (await row.innerText()).includes("Picture"), "the row says Cover and Picture");
  const order = await pg.evaluate(() => { const c = document.querySelector("#tree-cover"), t = document.querySelector("#tree"); return !!(c.compareDocumentPosition(t) & Node.DOCUMENT_POSITION_FOLLOWING) && !c.closest("#tree") && !c.matches(".tree-row"); });
  ok(order, "the Cover row is above the tree, outside it, and is not a document row");
  eq(await pg.locator("#tree .tree-row").count(), 1, "the tree still counts only documents");
  ok(/Manuscript, 1 document/.test(await pg.locator("#tree").getAttribute("aria-label")), "the tree's name counts documents only");
  // the keyboard model skips it: ArrowUp from the first document stays on the document, Home too
  await pg.locator("#tree .tree-row").first().focus();
  await pg.keyboard.press("ArrowUp");
  ok(await pg.evaluate(() => document.activeElement.classList.contains("tree-row")), "ArrowUp does not leave the tree for the Cover row");
  await pg.keyboard.press("Home");
  ok(await pg.evaluate(() => document.activeElement.classList.contains("tree-row")), "Home stays on a document");
  // the row opens the cover page, by click
  await row.click();
  await pg.waitForURL(`**/p/${book.slug}/cover`);
  // the library: the picture inside the tile's 3:4 box, on the cover colour, with its description first
  await pg.goto(`${base()}/`);
  const tile = pg.locator(".tile .cover-image img");
  await tile.waitFor();
  eq(await tile.getAttribute("alt"), "Cover: A sailing boat at dusk", "the tile's alt text");
  ok(await tile.evaluate((i) => i.complete && i.naturalWidth > 0), "the tile's picture loads");
  const box = await pg.locator(".tile .cover-image").boundingBox();
  ok(Math.abs(box.width / box.height - 0.75) < 0.01, "the tile keeps its 3:4 box: " + box.width / box.height);
  ok((await pg.locator(".continue .cover-image img").count()) === 1, "the Continue line has it too");
});

await kit.test("page mode: the live preview is the server's own placing, to the pixel", async () => {
  await pg.goto(cover());
  await pg.locator("#cv-front-file").setInputFiles({ name: "wide.jpg", mimeType: "image/jpeg", buffer: wide });
  await pg.waitForURL(/\/ui\/projects\//);
  await pg.locator("#cv-front-mode input[value=page]").check();
  await pg.locator("#cv-front-scale").fill("70");
  await pg.locator("#cv-front-margin").fill("0.5");
  await pg.locator("input[name=front_pos][value=top-left]").check();
  await pg.locator("input[name=front_fit][value=cover]").check();
  await pg.locator("input[name=front_bg]").nth(2).check();
  const live = await placed();
  ok(live && live.image.endsWith(",none") && live.clip, "the page is drawn live: " + JSON.stringify(live));
  ok(/cover-2/.test(live.bg), "the background is the swatch chosen");
  // the server draws the same draft (the form posted with no file and the upload action)
  const nav = pg.waitForNavigation();
  await pg.evaluate(() => document.getElementById("cv-form").requestSubmit(document.getElementById("cv-upload")));
  await nav;
  const server = await placed();
  eq(JSON.stringify(live), JSON.stringify(server), "live and server placing");
  // change the fit and the position: the preview moves, and matches again
  await pg.locator("input[name=front_fit][value=contain]").check();
  await pg.locator("input[name=front_pos][value=bottom-right]").check();
  await pg.locator("#cv-front-scale").fill("45");
  const live2 = await placed();
  ok(live2.image !== live.image, "the preview moved");
  const nav2 = pg.waitForNavigation();
  await pg.evaluate(() => document.getElementById("cv-form").requestSubmit(document.getElementById("cv-upload")));
  await nav2;
  eq(JSON.stringify(live2), JSON.stringify(await placed()), "live and server placing after the change");
  // actual size, and the arrows on the position grid
  await pg.locator("input[name=front_fit][value=actual]").check();
  await pg.locator("input[name=front_pos][value=top]").focus();
  await pg.keyboard.press("ArrowDown");
  eq(await pg.evaluate(() => document.activeElement.value), "center", "ArrowDown moves down a row of the grid");
  ok(await pg.locator("input[name=front_pos][value=center]").isChecked(), "and chooses it");
  await pg.keyboard.press("ArrowUp");
  eq(await pg.evaluate(() => document.activeElement.value), "top", "ArrowUp moves up");
  const live3 = await placed();
  const nav3 = pg.waitForNavigation();
  await pg.evaluate(() => document.getElementById("cv-form").requestSubmit(document.getElementById("cv-upload")));
  await nav3;
  eq(JSON.stringify(live3), JSON.stringify(await placed()), "live and server placing at actual size");
  await pg.locator("#cv-alt").fill("The harbour at dusk");
  await save();
  await pg.waitForSelector("#cv-flash");
  const l = await layout();
  eq(l.front.mode, "page", "saved as a page");
  eq(l.front.fit, "actual", "the fit");
  eq(l.front.position, "top", "the position");
  eq(l.front.margin_in, 0.5, "the margin");
  eq(l.front.background, "#3B5B7A", "the background is from the palette");
  await waitFor(() => pg.locator("#cv-pv-page img").evaluate((i) => i.complete && i.naturalWidth > 0), "Save to show the server's rendering");
});

await kit.test("the live preview never draws what Save would refuse; a drop is guarded; more than one dropped file is said not to be used", async () => {
  kit.allowed = [/status of 422/]; // the save of the extreme picture below is refused, as it should be
  await pg.goto(cover());
  const svgs = () => pg.locator("#cv-pv-page .cv-svg").count();
  const caption = () => pg.locator("#cv-pv-cap").innerText();
  eq(await pg.locator("#cv-front-mode input[value=page]").isChecked(), true, "the saved front is a page");
  eq(await svgs(), 0, "a saved layout shows its own picture, not a draft");
  await pg.locator("#cv-front-scale").fill("70");
  eq(await svgs(), 1, "a scale of 70 is drawn");
  for (const [id, v, why] of [["#cv-front-scale", "5", "a scale under 10"], ["#cv-front-scale", "", "no scale (it is not read as 100)"], ["#cv-front-scale", "0", "a scale of 0 (not read as 100)"], ["#cv-front-scale", "120", "a scale over 100"], ["#cv-front-scale", "55.5", "a scale that is not whole"]]) {
    await pg.locator(id).fill(v);
    eq(await svgs(), 0, "nothing is drawn for " + why);
    ok(/cannot be saved/.test(await caption()), "the caption says so for " + why + ": " + (await caption()));
  }
  await pg.locator("#cv-front-scale").fill("70");
  eq(await svgs(), 1, "drawn again with a good scale");
  ok(/as it will be/.test(await caption()), "and the caption is back: " + (await caption()));
  for (const [v, why] of [["-0.125", "a negative margin"], ["2", "a margin over 1.5"], ["", "no margin"]]) {
    await pg.locator("#cv-front-margin").fill(v);
    eq(await svgs(), 0, "nothing is drawn for " + why);
  }
  await pg.locator("#cv-front-margin").fill("0.5");
  eq(await svgs(), 1, "drawn again with a good margin");
  ok(!/null/.test(await pg.locator("#cv-pv-page .cv-svg").getAttribute("class")), "the picture's class has no 'null'");
  // a picture far wider than the page, at actual size: more than eight times the page, which the server refuses
  const extreme = await picture(pg, { w: 16000, h: 300, marks: false });
  await pg.locator("#cv-front-file").setInputFiles({ name: "extreme.jpg", mimeType: "image/jpeg", buffer: extreme });
  await pg.waitForURL(/\/ui\/projects\//);
  await pg.locator("input[name=front_fit][value=actual]").check();
  await pg.locator("#cv-front-scale").fill("100");
  await pg.locator("#cv-front-margin").fill("0");
  eq(await svgs(), 0, "nothing is drawn for a picture more than eight times the page");
  await pg.locator("#cv-front-scale").fill("40");
  eq(await svgs(), 1, "drawn at a scale that keeps it within eight times the page");
  await pg.locator("#cv-front-scale").fill("100");
  await pg.locator("#cv-alt").fill("A very wide strip");
  await save();
  await msg().waitFor();
  ok(/proportions are too far from the page/.test(await msg().innerText()), "Save refuses what the preview did not draw: " + (await msg().innerText()));
  // a picture one pixel wide, shown at a tenth of its size, would be less than a pixel
  const thin = await picture(pg, { w: 1, h: 1500, marks: false });
  await pg.locator("#cv-front-file").setInputFiles({ name: "thin.jpg", mimeType: "image/jpeg", buffer: thin });
  await pg.waitForURL(/\/ui\/projects\//);
  await pg.locator("input[name=front_fit][value=contain]").check();
  await pg.locator("input[name=front_pos][value=left]").check();
  await pg.locator("#cv-front-scale").fill("100");
  eq(await svgs(), 1, "a thin picture at full scale is drawn");
  await pg.locator("#cv-front-scale").fill("10");
  eq(await svgs(), 0, "nothing is drawn for a picture under a pixel");

  // dropped files: only the first is used, and the page says so; a browser with no DataTransfer is told to use the button
  await pg.goto(cover());
  const drop = (target, n, noDT) => pg.evaluate(({ target, n, noDT }) => {
    window.__sent = 0;
    HTMLFormElement.prototype.requestSubmit = function () { window.__sent++; };
    const dt = new DataTransfer();
    for (let i = 0; i < n; i++) dt.items.add(new File([new Uint8Array([1, 2, 3])], "drop" + i + ".jpg", { type: "image/jpeg" }));
    if (noDT) window.DataTransfer = undefined;
    const el = document.querySelector(target);
    const over = new DragEvent("dragover", { dataTransfer: dt, bubbles: true, cancelable: true });
    el.dispatchEvent(over);
    const ev = new DragEvent("drop", { dataTransfer: dt, bubbles: true, cancelable: true });
    el.dispatchEvent(ev);
    const inp = document.querySelector("#cv-front-file");
    return { dragover: over.defaultPrevented, drop: ev.defaultPrevented, effect: over.dataTransfer.dropEffect, files: inp.files.length, name: inp.files[0] && inp.files[0].name, sent: window.__sent };
  }, { target, n, noDT });
  const outside = await drop("h1", 1, false);
  ok(outside.dragover && outside.drop, "a file dropped outside a zone does not navigate away: " + JSON.stringify(outside));
  const many = await drop("#cv-front [data-drop]", 3, false);
  eq(many.files, 1, "one file is taken");
  eq(many.name, "drop0.jpg", "the first");
  eq(many.sent, 1, "and sent once");
  await waitFor(async () => /Only one picture/.test(await pg.locator("#cv-live").innerText()), "the page to say that only one picture is used");
  ok(/drop0\.jpg/.test(await pg.locator("#cv-live").innerText()), "and which one");
  await pg.goto(cover());
  const nodt = await drop("#cv-front [data-drop]", 1, true);
  eq(nodt.sent, 0, "with no DataTransfer nothing is sent");
  await waitFor(async () => /file button/.test(await pg.locator("#cv-live").innerText()), "the page to say to use the file button");
});

await kit.test("a wrap: the measured resolution and spine, and the drawing with its cut lines", async () => {
  kit.allowed = [/status of 422/]; // the wrong trim at the end is refused
  await pg.goto(cover());
  await pg.locator("#cv-shape-wrap").check();
  ok(!(await pg.locator("#cv-front").isVisible()) && (await pg.locator("#cv-wrap-file").isVisible()), "the wrap's controls replace the sides'");
  await pg.locator("#cv-wrap-file").setInputFiles({ name: "wrap.jpg", mimeType: "image/jpeg", buffer: wrapPic });
  await pg.waitForURL(/\/ui\/projects\//);
  const facts = await pg.locator(".cv-facts").innerText();
  ok(/150 dpi at \u2068?6 × 9 in\u2069?/.test(facts), "the resolution: " + facts);
  ok(/0\.49\d in: about \d+ pages of KDP white paper \(0\.002252 in a page\)/.test(facts), "the spine in inches and pages: " + facts);
  ok(/the right/.test(facts), "which side the front is on");
  eq(await pg.locator(".cv-wrapsvg").count(), 1, "the drawing");
  eq(await pg.locator(".cv-line .cv-g-cut").count(), 1, "the cut line");
  eq(await pg.locator(".cv-line .cv-g-fold").count(), 1, "the spine folds");
  eq(await pg.locator(".cv-line .cv-g-margin").count(), 2, "the text margins");
  ok(await pg.locator("figure.cv-art figcaption").innerText().then((t) => /Cut line/.test(t) && /Spine folds/.test(t) && /Text margin/.test(t)), "the key says what the lines are");
  await save();
  await pg.waitForSelector("#cv-flash");
  const l = await layout();
  eq(l.shape, "wrap", "saved as a wrap");
  ok(Math.abs(l.measured.spine_in - 0.5) < 0.01, "the saved spine: " + l.measured.spine_in);
  await waitFor(() => pg.locator("#cv-pv-page img").evaluate((i) => i.complete && i.naturalWidth > 0), "the front cut from the wrap to load");
  // a wrap for the wrong trim: refused in a sentence that says what to do
  await pg.locator("#cv-trim").selectOption("a4");
  const nav = pg.waitForNavigation();
  await pg.locator("#cv-wrap-file").setInputFiles({ name: "wrap2.jpg", mimeType: "image/jpeg", buffer: wrapPic });
  await nav;
  const m = await msg().innerText();
  ok(/not a wrap for this size/.test(m) && /Check the page size/.test(m), "the wrong trim is a sentence: " + m);
});

await kit.test("a refusal: an SVG is a sentence, nothing is added, and the page is as it was", async () => {
  kit.allowed = [/status of 422/];
  await pg.goto(cover());
  await pg.locator("#cv-shape-sides").check();
  await pg.locator("#cv-front-mode input[value=image]").check();
  const before = (await api(main, "GET", `/api/v1/projects/${book.id}/attachments`)).json.items.length;
  const nav = pg.waitForNavigation();
  await pg.locator("#cv-front-file").setInputFiles({ name: "logo.svg", mimeType: "image/svg+xml", buffer: SVG });
  await nav;
  const m = await msg().innerText();
  ok(/An SVG is not accepted as a cover/.test(m) && /PNG or a JPEG/.test(m), "the sentence: " + m);
  eq(await pg.locator("#cv-front-file").getAttribute("aria-invalid"), "true", "the field is marked");
  eq((await api(main, "GET", `/api/v1/projects/${book.id}/attachments`)).json.items.length, before, "nothing was stored");
  // a PNG with the wrong kind of file inside, and an empty one
  const nav2 = pg.waitForNavigation();
  await pg.locator("#cv-front-file").setInputFiles({ name: "notes.jpg", mimeType: "image/jpeg", buffer: Buffer.from("this is not a picture at all") });
  await nav2;
  ok(/neither/.test(await msg().innerText()), "a file that is no picture: " + (await msg().innerText()));
});

await kit.test("a stale save: the writer's version stays in the form, the saved one is shown beside it, both named, with a way to reload", async () => {
  kit.allowed = [/status of 409/]; // the stale save is a 409 page, as it should be
  await pg.goto(cover());
  await pg.locator("#cv-alt").fill("Mine, from a page that is out of date");
  // someone else saves in the meantime
  const l = await layout();
  const r = await api(main, "PUT", `/api/v1/projects/${book.id}/cover-layout`, { revision: l.revision, shape: "wrap", wrap: { attachment: l.wrap.attachment, bleed_in: l.wrap.bleed_in }, alt: "Theirs", trim: l.trim });
  eq(r.status, 200, "the other save");
  await save();
  await msg().waitFor();
  ok(/Someone else changed the cover/.test(await msg().innerText()), "the sentence: " + (await msg().innerText()));
  eq(await pg.locator("#cv-alt").inputValue(), "Mine, from a page that is out of date", "the form keeps what the writer typed");
  const vers = await pg.locator(".cv-versions").innerText();
  ok(/Your version/.test(vers) && /Saved version/.test(vers) && /Theirs/.test(vers) && /Mine, from a page/.test(vers), "both versions are named and shown: " + vers);
  eq(await pg.locator("input[name=revision]").inputValue(), String(l.revision + 1), "a save from this page carries the revision it met");
  await pg.getByRole("link", { name: "Reload the cover" }).click();
  await pg.waitForURL(cover());
});

await kit.test("use the generated cover: asks first, then the CSS cover is back in the page, the tree and the library", async () => {
  await pg.goto(cover());
  await pg.getByRole("button", { name: "Use the generated cover" }).last().click();
  const asked = await armAnswer(pg, "cancel");
  ok(/Use the generated cover\?/.test(asked), "the question: " + asked);
  ok(await layout(), "cancel changes nothing");
  await pg.getByRole("button", { name: "Use the generated cover" }).last().click();
  await armAnswer(pg, "ok");
  await pg.waitForSelector("#cv-flash");
  ok(/generated cover again/.test(await pg.locator("#cv-flash").innerText()), "the page says so");
  eq(await layout(), null, "no layout");
  ok(await pg.locator("#cv-pv-page .cv-generated").isVisible(), "the preview is the generated cover");
  ok((await pg.locator("#cv-pv-page img").count()) === 0, "no picture in the preview");
  await pg.goto(`${base()}/p/${book.slug}`);
  ok((await pg.locator("#tree-cover img").count()) === 0 && /Generated/.test(await pg.locator("#tree-cover").innerText()), "the tree row is the swatch");
  await pg.goto(`${base()}/`);
  ok((await pg.locator(".tile .cover-image").count()) === 0 && (await pg.locator(".tile .cover-title").count()) === 1, "the library tile is the CSS cover");
  ok((await api(main, "GET", `/api/v1/projects/${book.id}/attachments`)).json.items.length > 0, "the pictures stay as attachments");
});

await kit.test("without script: a picture and the rest in one form, saved with one button", async () => {
  kit.allowed = [/status of 422/];
  const bare = await kit.newContext({ viewport: { width: 1280, height: 800 }, javaScriptEnabled: false, storageState: await main.storageState() });
  const p = await bare.newPage();
  kit.watch(p, "no-script");
  await p.goto(cover());
  ok((await p.locator("#cv-form").count()) === 1, "the form is there");
  await p.locator("#cv-shape-sides").check();
  await p.locator("#cv-front-mode input[value=image]").check();
  await p.locator("#cv-front-file").setInputFiles({ name: "front.jpg", mimeType: "image/jpeg", buffer: front });
  await p.locator("#cv-alt").fill("A sailing boat, with no script");
  await p.getByRole("button", { name: "Save the cover" }).click();
  await p.waitForSelector("#cv-flash");
  eq((await layout()).front.mode, "image", "saved by one press");
  await waitFor(() => p.locator("#cv-pv-page img").evaluate((i) => i.complete && i.naturalWidth > 0), "the server's picture");
  // a refusal still reads as words, and the draft keeps what was typed
  await p.locator("#cv-alt").fill("");
  await p.getByRole("button", { name: "Save the cover" }).click();
  ok(/Describe the cover/.test(await p.locator("#cv-msg").innerText()), "the refusal, with no script");
  await bare.close();
});

await kit.test("the keyboard alone at 390 px: shape, mode, file, description, save", async () => {
  await api(main, "DELETE", `/api/v1/projects/${book.id}/cover-layout`);
  const phone = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, storageState: await main.storageState() });
  const p = await phone.newPage();
  kit.watch(p, "phone");
  await p.goto(cover());
  const tab = async (n = 1) => { for (let i = 0; i < n; i++) await p.keyboard.press("Tab"); };
  const active = () => p.evaluate(() => { const a = document.activeElement; return a ? (a.id || a.name || a.tagName) + ":" + (a.value || "") : ""; });
  // the shape group takes arrow keys; the sides appear
  await p.locator("#cv-shape-generated").focus();
  await p.keyboard.press("ArrowDown");
  eq(await p.locator("#cv-shape-sides").isChecked(), true, "ArrowDown chooses front and back");
  ok(await p.locator("#cv-front").isVisible(), "and shows the sides");
  await tab(); // the trim
  eq((await active()).split(":")[0], "cv-trim", "the next stop is the page size");
  await tab(); // front: the mode group
  await p.keyboard.press("ArrowRight");
  eq(await p.locator("#cv-front-mode input[value=image]").isChecked(), true, "an arrow key chooses Image");
  await tab();
  eq((await active()).split(":")[0], "cv-front-file", "the file input is the next stop");
  const chooser = p.waitForEvent("filechooser");
  await p.keyboard.press("Enter");
  const fc = await chooser;
  const nav = p.waitForNavigation();
  await fc.setFiles({ name: "front.jpg", mimeType: "image/jpeg", buffer: front });
  await nav;
  ok(/Added front\.jpg/.test(await p.locator(".cv-note").innerText()), "the picture is added");
  // from the file input to the description and Save, by Tab alone
  await p.locator("#cv-front-file").focus();
  let reached = "";
  for (let i = 0; i < 30 && reached !== "cv-alt"; i++) { await tab(); reached = (await active()).split(":")[0]; }
  eq(reached, "cv-alt", "the description is reachable by Tab");
  await p.keyboard.type("A sailing boat at dusk");
  await tab();
  eq((await active()).startsWith("BUTTON") || (await active()).includes("save"), true, "then Save: " + (await active()));
  await p.keyboard.press("Enter");
  await p.waitForSelector("#cv-flash");
  eq((await layout()).front.mode, "image", "saved from the keyboard");
  // nothing scrolls sideways, and every control is a touch target
  const over = await p.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  ok(over <= 0, "sideways scroll at 390 px: " + over);
  const small = await p.evaluate(() => [...document.querySelectorAll("#cv-form button, #cv-form select, #cv-form input:not([type=hidden]):not([type=file]), #cv-form textarea")].filter((e) => e.offsetParent && !e.matches("input[type=radio], input[type=checkbox]")).map((e) => { const r = e.getBoundingClientRect(); return [e.id || e.name, r.width, r.height]; }).filter((x) => x[2] < 44));
  ok(small.length === 0, "controls under 44 px: " + JSON.stringify(small));
  await phone.close();
});

await kit.test("a reload with the network cut: the cover page is not kept, the offline notice is (as for every project page)", async () => {
  const sw = await kit.newContext({ serviceWorkers: "allow", viewport: { width: 1000, height: 700 }, storageState: await main.storageState() });
  const p = await sw.newPage();
  kit.watch(p, "offline");
  const cut = [/ERR_INTERNET_DISCONNECTED/, /Failed to load resource/, /net::ERR/];
  await p.goto(cover());
  await waitFor(() => p.evaluate(async () => { const r = await navigator.serviceWorker.ready; return r.active && r.active.state === "activated" && !!navigator.serviceWorker.controller; }), "the worker", 20000);
  await p.goto(cover());
  const dump = await p.evaluate(async () => { const out = []; for (const n of await caches.keys()) { const c = await caches.open(n); for (const r of await c.keys()) out.push(new URL(r.url).pathname); } return out; });
  ok(dump.every((d) => d.startsWith("/static/") || d === "/offline"), "the cache holds only the shell: " + dump.filter((d) => !d.startsWith("/static/")).join(" "));
  ok(!dump.some((d) => !d.startsWith("/static/") && /cover|files/.test(d)), "no cover picture or cover page is kept");
  kit.allowed = cut;
  await sw.setOffline(true);
  try {
    const res = await p.goto(cover(), { waitUntil: "load", timeout: 15000 });
    eq(res.status(), 200, "the offline page is a normal answer");
    const text = await p.locator("main").innerText();
    ok(/cannot reach the server/.test(text), "the offline notice: " + text.slice(0, 80));
    ok(!/Cover|The Salt Road|sailing/.test(text), "nothing of the cover or the book is shown offline");
  } finally {
    await sw.setOffline(false);
  }
  await sw.close();
});

const text0 = (sel) => pg.locator(sel).first().innerText();

// ---- the Compile page's paperback cover (phase C2): measured and drawn from the layout --------------------------------------
// --shots DIR writes the screenshots docs/screens/covers-c2 keeps.
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
let pb, wrapAtt;
const cmpUrl = () => `${base()}/p/${pb.slug}/compile`;
const say = (p = pg) => p.locator("#cmp-status").innerText();
const measured = (p = pg) => waitFor(async () => /The cover is measured|spine is/.test(await say(p)) && (await p.locator("#cv-art svg").count()) === 1, "the cover measured", 30000);
const pdfFacts = (buf) => { const t = buf.toString("latin1"); return { pages: (t.match(/\/Type \/Page(?![s\w])/g) || []).length, trim: (t.match(/\/TrimBox/g) || []).length, head: t.slice(0, 5) }; };
async function download(p, button) {
  const [dl] = await Promise.all([p.waitForEvent("download"), p.locator(button).click()]);
  const f = join(kit.tmp, dl.suggestedFilename());
  await dl.saveAs(f);
  return { name: dl.suggestedFilename(), ...pdfFacts(readFileSync(f)) };
}
async function openCompile(p = pg, preset = "print-6x9") {
  await p.goto(cmpUrl(), { waitUntil: "load" });
  await p.waitForSelector("#cmp-preset option", { state: "attached" });
  await p.selectOption("#cmp-preset", preset);
  await p.waitForSelector("#cmp-cover:not([hidden])");
}
async function fillCover(p = pg, { pages = "200", profile = "generic" } = {}) {
  await p.fill("#cv-thick", "0.0025");
  await p.fill("#cv-pages", pages);
  await p.selectOption("#cv-profile", profile);
}

await kit.test("the Compile page's cover: KDP chosen, the layout's wrap drawn with the folds, the margin and the barcode box", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "Print Book", kind: "novel", author_name: "Máire Nolan", cover_color: "#3B5B7A" });
  pb = made.json.item;
  const w = wrapPng();
  const upl = await main.request.fetch(`${base()}/api/v1/projects/${pb.id}/cover-layout/images?part=wrap&trim=6x9&bleed_in=0.125`, { method: "POST", data: w.buffer, headers: { "Content-Type": "image/png" } });
  wrapAtt = (await upl.json()).attachment_id;
  const set = await api(main, "PUT", `/api/v1/projects/${pb.id}/cover-layout`, { revision: 0, shape: "wrap", trim: "6x9", alt: "A wrap in dusk colours", wrap: { attachment: wrapAtt, bleed_in: 0.125 } });
  ok(set.status < 300, "the wrap layout is saved: " + set.status);
  await openCompile();
  ok(await pg.locator("#cmp-coverpage").isHidden(), "the ebook's cover section gives way to the paperback's: one link to the Cover page, not two");
  ok((await pg.locator("#cmp-cover a[href$='/cover']").count()) === 1, "the link to change the cover");
  ok(/three|proof/i.test(await text0("#cmp-cover > p.fine")), "the note says what the files are: " + (await text0("#cmp-cover > p.fine")).slice(0, 80));
  // the profile's one line follows the choice
  await pg.selectOption("#cv-profile", "kdp");
  ok(await pg.locator("#cv-profile-h [data-pf=kdp]").isVisible() && !(await pg.locator("#cv-profile-h [data-pf=ingram]").isVisible()), "only the chosen profile's line shows");
  ok(/0\.125 in inside the trim/.test(await pg.locator("#cv-profile-h").innerText()), "the line gives KDP's margin: " + (await pg.locator("#cv-profile-h").innerText()));
  await fillCover(pg, { profile: "kdp" });
  await pg.locator("#cmp-cv-form button[type=submit]").click();
  await measured();
  const art = await pg.evaluate(() => {
    const svg = document.querySelector("#cv-art svg"), vb = svg.getAttribute("viewBox").split(" ").map(Number);
    const g = (c) => [...svg.querySelectorAll(`.cv-line .${c}`)];
    const im = svg.querySelector("image");
    return { vb, href: im && im.getAttribute("href"), fold: (g("cv-g-fold")[0] || { getAttribute: () => "" }).getAttribute("d"), margin: g("cv-g-margin").map((r) => [r.getAttribute("x"), r.getAttribute("y"), r.getAttribute("width"), r.getAttribute("height")]), cut: g("cv-g-cut").map((r) => r.getAttribute("x") + "," + r.getAttribute("width")), bar: g("cv-g-bar").length, book: g("cv-g-book").length };
  });
  const near = (a, b, m) => ok(Math.abs(a - b) < 0.6, `${m}: ${a} is not ${b}`);
  near(art.vb[2], 918, "the drawing is the wrap's size in points (12.75 in)"); near(art.vb[3], 666, "and 9.25 in high");
  eq(art.href, `/files/${wrapAtt}`, "it draws the layout's own picture");
  const fold = art.fold.match(/[\d.]+/g).map(Number);
  near(fold[0], 441, "the spine's first fold"); near(fold[3], 477, "the spine's second fold: 0.5 in apart, 200 pages of 0.0025 in paper");
  eq(art.margin.length, 2, "a text margin on the back and on the front");
  near(+art.margin[0][0], 18, "KDP's 0.125 in margin, after the 0.125 in bleed"); near(+art.margin[0][2], 414, "the margin's width: 6 in less twice 0.125");
  near(+art.cut[0].split(",")[0], 9, "the trim starts after the bleed"); near(+art.cut[0].split(",")[1], 900, "and is the wrap less the bleed");
  eq(art.bar, 1, "the barcode box"); eq(art.book, 0, "a wrap that fits shows no second set of folds");
  ok(await pg.evaluate(() => new Promise((r) => { const i = new Image(); i.onload = () => r(i.naturalWidth > 0); i.onerror = () => r(false); i.src = document.querySelector("#cv-art image").getAttribute("href"); })), "the picture loads");
  ok(await pg.locator("#cv-download.solid").count() === 1 && await pg.locator("#cv-measure.solid").count() === 0, "once measured the file for the printer is the main action");
  const facts = await pg.locator("#cv-facts").innerText();
  ok(/KDP/.test(facts) && /Your wrap/.test(facts) && /0\.5 in/.test(facts), "the facts: " + facts.replace(/\s+/g, " "));
  const warn = (await pg.locator("#cv-warn").count()) ? await pg.locator("#cv-warn").innerText() : "";
  ok(/Leave the lower right of the back clear/.test(warn), "the barcode corner is said in words, with no wrap to read: " + warn);
  ok(!/\{|cover_|spine_/.test(warn + facts), "no key, code or placeholder is shown");
  // another profile measures again by itself: IngramSpark's margin is 0.25 in
  await pg.selectOption("#cv-profile", "ingram");
  await waitFor(async () => Math.abs(+(await pg.evaluate(() => (document.querySelector("#cv-art .cv-line .cv-g-margin") || { getAttribute: () => "0" }).getAttribute("x"))) - 27) < 0.6, "the margin to follow the profile: IngramSpark's 0.25 in");
  ok(/IngramSpark/.test(await pg.locator("#cv-facts").innerText()), "and the facts");
});

await kit.test("the cover files: the one for the printer, the front alone and the proof, each a PDF with a TrimBox", async () => {
  await pg.selectOption("#cv-profile", "kdp");
  await measured();
  const printer = await download(pg, "#cv-download");
  ok(/-cover\.pdf$/.test(printer.name) && printer.head === "%PDF-", "the printer's file: " + printer.name);
  eq(printer.pages, 1, "the printer's file is one page");
  ok(printer.trim >= 1, "with a TrimBox");
  const front = await download(pg, "#cv-front");
  ok(/-cover-front\.pdf$/.test(front.name), "the front's own file: " + front.name);
  eq(front.pages, 1, "one page"); ok(front.trim >= 1, "with a TrimBox");
  const proof = await download(pg, "#cv-proof");
  ok(/-cover-proof\.pdf$/.test(proof.name), "the proof's own file: " + proof.name);
  eq(proof.pages, 1, "one page");
  ok(/Made .*-cover-proof\.pdf/.test(await say()), "the status says it was made");
});

await kit.test("a stale wrap: another page count says the spine does not fit, and what to do; the book's folds are drawn too", async () => {
  await pg.fill("#cv-pages", "300");
  await pg.locator("#cv-pages").dispatchEvent("change");
  await waitFor(async () => (await pg.locator("#cv-art .cv-line .cv-g-book").count()) === 1, "the book's folds", 20000);
  const warn = await pg.locator("#cv-warn").innerText();
  ok(/The spine in your wrap is 0\.49\d in wide\. With 300 pages on this paper your spine is 0\.75 in/.test(warn), "the figures: " + warn);
  ok(/Make the wrap again for this spine/.test(warn), "and what to do");
  ok(/Where your book's spine folds/.test(await pg.locator(".cv-key").innerText()), "the legend names the second set");
  ok(/doesn't fit/.test(await say()), "the status line says so: " + (await say()));
  const foldBook = await pg.locator("#cv-art .cv-line .cv-g-book").getAttribute("d");
  const bk = foldBook.match(/[\d.]+/g).map(Number);
  ok(Math.abs(bk[0] - 432) < 0.6 && Math.abs(bk[3] - 486) < 0.6, "the book's folds, centred on the picture and 0.75 in apart: " + foldBook);
});

await kit.test("a trim the layout was not made for: said in words, with what to do, and the link; the generated cover is the way out", async () => {
  kit.allowed = [/status of 422/]; // the refusal is a 422 answer to the form, shown as a sentence
  await pg.selectOption("#cmp-preset", "print-5x8");
  await waitFor(async () => /Your cover is made for 6 × 9 in, and this preset is 5 × 8 in/.test(await pg.locator("#cv-out").innerText()), "the refusal", 20000);
  ok(/Make the cover again for 5 × 8 in on the Cover page/.test(await pg.locator("#cv-out").innerText()), "what to do");
  ok(/switch off “The book's own cover”/.test(await pg.locator("#cv-out").innerText()), "and the way out");
  eq(await pg.locator("#cv-out [role=alert]").count(), 1, "an alert");
  ok((await pg.locator("#cv-out a[href$='/cover']").count()) === 1, "a link to the Cover page");
  eq(await say(), "", "the alert says it, so the status line is empty: it is said once");
  eq(await pg.locator("#cv-art").count(), 0, "nothing is drawn");
  ok(await pg.locator("#cv-measure.solid").count() === 1, "and Measure is the main action again");
  // the download says it in the same words
  await pg.locator("#cv-download").click();
  await waitFor(async () => /Your cover is made for 6 × 9 in/.test(await say()), "the download's refusal is in words too");
  // switch the layout off: the generated cover is drawn for the 5 x 8 page
  await pg.selectOption("#cmp-preset", "print-6x9");
  await measured();
  await pg.locator("#o-own_cover").uncheck();
  await waitFor(async () => (await pg.locator("#cv-art svg image").count()) === 0 && (await pg.locator("#cv-art svg text").count()) > 0, "the generated cover", 20000);
  ok(/The generated cover/.test(await pg.locator("#cv-facts").innerText()), "the facts say so");
  ok(await pg.locator("#cv-proof").isHidden() && await pg.locator("#cv-proof-h").isHidden(), "a generated cover has no proof to offer, and the note goes with the button");
  await pg.locator("#o-own_cover").check();
  await measured();
  await waitFor(() => pg.locator("#cv-proof").isVisible(), "the proof to be back with the layout", 20000);
});

await kit.test("a refused field is marked and unmarked; another control's change does not move the focus; a picture that will not load is said", async () => {
  kit.allowed = [/status of 422/, /Failed to load resource|net::ERR_FAILED/];
  await openCompile();
  await fillCover(pg, { profile: "kdp" });
  await pg.fill("#cv-bleed", "9");
  await pg.locator("#cmp-cv-form button[type=submit]").click();
  await waitFor(async () => /between 0 and 0\.5/.test(await pg.locator("#cv-out").innerText()), "the bleed refused in words", 20000);
  eq(await pg.locator("#cv-bleed").getAttribute("aria-invalid"), "true", "the bleed is marked");
  eq(await pg.evaluate(() => document.activeElement.id), "cv-bleed", "after the person's own Measure the focus is on it");
  // another control changes: measured again by itself, and the focus stays where the person is
  await pg.locator("#cv-profile").focus();
  await pg.selectOption("#cv-profile", "ingram");
  await kit.sleep(600);
  eq(await pg.evaluate(() => document.activeElement.id), "cv-profile", "an automatic measure leaves the focus alone");
  eq(await pg.locator("#cv-bleed").getAttribute("aria-invalid"), "true", "the field is still marked while the answer is a refusal");
  await pg.fill("#cv-bleed", "");
  await pg.locator("#cv-bleed").dispatchEvent("change");
  await waitFor(async () => (await pg.locator("#cv-art svg").count()) === 1, "the cover measured again", 20000);
  eq(await pg.locator("#cmp-cover [aria-invalid]").count(), 0, "no field is left marked");
  // the picture cannot be loaded: said, as an alert, and once
  await pg.route("**/files/*", (r) => r.abort());
  await pg.locator("#cmp-cv-form button[type=submit]").click();
  await waitFor(async () => (await pg.locator("#cv-out .cmp-gone[role=alert]").count()) === 1, "the missing picture to be said", 20000);
  ok(/can't be loaded/.test(await pg.locator(".cmp-gone").innerText()), "in words");
  await pg.unroute("**/files/*");
});

await kit.test("the Compile page's cover by the keyboard alone, and the Cover page's print notes", async () => {
  await openCompile();
  await pg.evaluate(() => document.activeElement && document.activeElement.blur());
  const act = () => pg.evaluate(() => document.activeElement && (document.activeElement.id || document.activeElement.tagName));
  for (let i = 0; i < 120 && (await act()) !== "cv-profile"; i++) await pg.keyboard.press("Tab");
  eq(await act(), "cv-profile", "Tab reaches the profile");
  await pg.keyboard.type("Ingram"); // type-ahead on the closed select, as for the preset
  eq(await pg.locator("#cv-profile").inputValue(), "ingram", "the keyboard chooses a profile");
  await pg.keyboard.press("Tab");
  await pg.keyboard.type("0.0025");
  await pg.keyboard.press("Enter");
  await measured();
  const ring = await pg.evaluate(() => { const e = document.activeElement, st = getComputedStyle(e); return st.outlineStyle !== "none" || st.boxShadow !== "none"; });
  ok(ring, "the focus shows");
  for (let i = 0; i < 12 && (await act()) !== "cv-download"; i++) await pg.keyboard.press("Tab");
  eq(await act(), "cv-download", "Tab reaches the cover download");
  await pg.keyboard.press("Tab");
  eq(await act(), "cv-front", "then the front"); await pg.keyboard.press("Tab");
  eq(await act(), "cv-proof", "then the proof");
  // the Cover page tells the print findings of the saved wrap
  await pg.goto(`${base()}/p/${pb.slug}/cover`);
  const notes = await pg.locator(".cv-print").innerText();
  ok(/If you print this cover/.test(notes) && /Leave the lower right of the back clear/.test(notes), "the print notes: " + notes.slice(0, 160));
  ok(await pg.locator(".cv-print a[href$='/compile']").count() === 1, "with a way to the Compile page");
});

await kit.test("the Compile page's cover at 390 px: nothing scrolls sideways, every control is 44 px, the drawing fits", async () => {
  const phone = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, storageState: await main.storageState() });
  const p = await phone.newPage();
  kit.watch(p, "phone");
  await openCompile(p);
  await fillCover(p, { profile: "kdp" });
  await p.locator("#cmp-cv-form button[type=submit]").tap();
  await measured(p);
  eq(await p.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1), false, "no sideways scroll");
  const small = await p.evaluate(() => [...document.querySelectorAll("#cmp-cover button, #cmp-cover select, #cmp-cover input:not([type=checkbox]), #cmp-cover a.btn, #cmp-cover label.check")].filter((e) => e.offsetParent !== null && !e.disabled).map((e) => { const r = e.getBoundingClientRect(); return { id: e.id || e.textContent.trim().slice(0, 24), w: Math.round(r.width), h: Math.round(r.height) }; }).filter((r) => r.h < 44));
  eq(small.length, 0, "targets under 44 px: " + JSON.stringify(small));
  const box = await p.locator("#cv-art svg").boundingBox();
  ok(box.width <= 390 && box.width > 250, "the drawing fits the phone: " + JSON.stringify(box));
  if (shots) { await p.locator("#cmp-cover").scrollIntoViewIfNeeded(); await p.screenshot({ path: `${shots}/compile-cover-390.png`, caret: "initial" }); }
  await phone.close();
});

await kit.test("a person who is not a member finds nothing", async () => {
  const rd = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  const rp = await rd.newPage();
  kit.watch(rp, "reader");
  const { signIn } = await import("./harness.mjs");
  await signIn(rp, base(), "reader@dev.scriobh");
  kit.allowed = [/status of 404/];
  const res = await rp.goto(cover(), { waitUntil: "load" });
  eq(res.status(), 404, "a person who is not a member");
  await rd.close();
});

await kit.finish("e2e-covers");
