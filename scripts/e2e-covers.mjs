#!/usr/bin/env node
// End-to-end test of the cover page in a real Chromium (docs/COVERS.md, phase C1's cover page): a front picture chosen
// and saved, the thumbnail in the manuscript tree and the library, the page mode and its live preview against the
// server's own drawing, a wrap and its measured spine, a refused SVG as a sentence, the way back to the generated cover,
// a stale save, the page with no script, the keyboard alone at 390 px, and what a reload with the network cut shows.
//
//   node scripts/e2e-covers.mjs [--only substring]
import { createKit, eq, ok, armAnswer } from "./e2e-kit.mjs";
import { picture, wrap, SVG } from "./covers-pictures.mjs";

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
