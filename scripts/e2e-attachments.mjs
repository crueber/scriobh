#!/usr/bin/env node
// End-to-end test of attachments in a real Chromium: adding a file from the writing view (picker, drop, paste), the
// alt-text bar and its explicit Insert, cancel, refusals in plain words, remote pictures staying alt text, the
// manager pages of a project and of a Compendium, the delete rule, and a phone viewport.
//
//   node scripts/e2e-attachments.mjs [--only substring]
import { deflateSync } from "node:zlib";
import { randomBytes } from "node:crypto";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";

// Small limits, so the server's own answers (too large, over quota) can be seen: one file up to 1 MB, a project up to 1 MB.
const kit = await createKit("e2e-attachments", { env: { SCRIOBH_UPLOAD_MAX_MB: "1", SCRIOBH_QUOTA_PROJECT_MB: "1" } });
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;
await main.grantPermissions(["clipboard-read", "clipboard-write"], { origin: base() }).catch(() => {});

// A valid PNG of a flat colour; two seeds are two different files.
function crc32(buf) {
  let c, crc = ~0;
  for (let n = 0; n < buf.length; n++) { c = (crc ^ buf[n]) & 0xff; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; crc = (crc >>> 8) ^ c; }
  return ~crc >>> 0;
}
function png(w, h, seed) {
  const chunk = (type, data) => {
    const len = Buffer.alloc(4); len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type), data]);
    const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td));
    return Buffer.concat([len, td, crc]);
  };
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr[8] = 8; ihdr[9] = 2;
  const row = Buffer.concat([Buffer.from([0]), Buffer.alloc(w * 3, seed)]);
  const raw = Buffer.concat(Array.from({ length: h }, () => row));
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}
// An incompressible picture of about the given size.
function bigPng(kb) {
  const w = 300, h = Math.ceil((kb * 1024) / (w * 3));
  const chunk = (type, data) => { const len = Buffer.alloc(4); len.writeUInt32BE(data.length); const td = Buffer.concat([Buffer.from(type), data]); const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td)); return Buffer.concat([len, td, crc]); };
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr[8] = 8; ihdr[9] = 2;
  const raw = Buffer.concat(Array.from({ length: h }, () => Buffer.concat([Buffer.from([0]), randomBytes(w * 3)])));
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(raw, { level: 0 })), chunk("IEND", Buffer.alloc(0))]);
}
const PDF = Buffer.from("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n");
const file = (name, buffer, mimeType) => ({ name, mimeType, buffer });

let book, doc, comp;
const md = (p) => p.evaluate(() => window.scriobh.write.editor.getMarkdown());
const stored = async (id) => (await api(main, "GET", `/api/v1/docs/${id}`)).json.item;
const list = async (owner = "projects", id = book.id) => (await api(main, "GET", `/api/v1/${owner}/${id}/attachments`)).json.items;
const openDoc = async (page = pg) => {
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh && window.scriobh.attach && window.scriobh.write)), "the uploader to load", 10000);
};
const choose = async (page, ...files) => page.locator('input[type="file"]').setInputFiles(files);
const bars = (page) => page.locator("#notices .att-row");
const live = (page) => page.locator("#att-live");

await kit.test("setup: a project with one chapter, and a Compendium", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "Attached", kind: "novel" });
  ok(made.json && made.json.item, "project: " + made.status);
  book = made.json.item;
  doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const rev = (await stored(doc)).revision;
  eq((await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, title: "The Harbour", content: "The harbour was quiet at dawn.\n\nMarta counted the boats.\n" })).status, 200, "seed the chapter");
  const c = await api(main, "POST", "/api/v1/compendiums", { name: "Attached Bible" });
  ok(c.json && c.json.item, "compendium: " + c.status + JSON.stringify(c.json));
  comp = c.json.item;
});

await kit.test("writing view: add a picture, nothing is inserted until Insert, then one undo takes it out", async () => {
  await openDoc();
  const before = await md(pg);
  await pg.locator("#fmtbar [data-cmd=image]").waitFor();
  await choose(pg, file("harbour map.png", png(40, 30, 10), "image/png"));
  await bars(pg).first().waitFor();
  const inp = pg.locator('#notices input[type="text"]');
  await inp.waitFor({ timeout: 8000 });
  eq(await inp.inputValue(), "harbour map", "alt text starts from the file name");
  eq(await md(pg), before, "nothing is in the chapter before Insert");
  await waitFor(async () => (await live(pg).innerText()).includes("added"), "the live region to say it was added");
  await pg.locator('#notices').getByRole("button", { name: "Insert image" }).waitFor();
  await inp.fill("The harbour at low tide");
  await inp.press("Enter");
  await waitFor(async () => (await md(pg)).includes("![The harbour at low tide](/files/att_"), "the image Markdown in the chapter");
  eq(await bars(pg).count(), 0, "the bar closes");
  ok(await pg.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus is back in the chapter");
  // It is drawn.
  await waitFor(() => pg.evaluate(() => { const i = document.querySelector('.scriobh-prose img[src^="/files/att_"]'); return i && i.complete && i.naturalWidth > 0; }), "the picture to load");
  // One undo removes the whole insertion.
  await pg.keyboard.press(`${MOD}+z`);
  eq(await md(pg), before, "one undo restores the chapter");
  await pg.keyboard.press(`${MOD}+Shift+z`);
  ok((await md(pg)).includes("![The harbour at low tide]"), "redo brings it back");
  // It saves, and the file knows where it is used.
  await waitFor(async () => (await stored(doc)).content.includes("![The harbour at low tide](/files/att_"), "the save");
  const a = (await list())[0];
  await waitFor(async () => (await list())[0].refs === 1, "the attachment to count its use");
  await waitFor(async () => (await list()).find((i) => i.filename === "harbour map.png").alt === "The harbour at low tide", "the typed alt text to be kept on the file");
  void a;
});

await kit.test("writing view: Not now leaves the chapter alone; the file stays in the project", async () => {
  await openDoc();
  const before = await md(pg);
  const n = (await list()).length;
  await choose(pg, file("second.png", png(20, 20, 40), "image/png"));
  await pg.locator('#notices').getByRole("button", { name: "Not now" }).click({ timeout: 8000 });
  eq(await md(pg), before, "the chapter is unchanged");
  eq((await list()).length, n + 1, "the file is kept");
  eq(await bars(pg).count(), 0, "the bar closes");
});

await kit.test("writing view: typing is not interrupted by an upload finishing", async () => {
  await openDoc();
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${MOD}+End`);
  await pg.keyboard.type("Typing before ");
  await choose(pg, file("third.png", png(16, 16, 70), "image/png"));
  await pg.keyboard.type("and after the upload. ");
  await pg.locator('#notices input[type="text"]').waitFor({ timeout: 8000 });
  await pg.keyboard.type("still typing.");
  const t = await md(pg);
  ok(t.includes("Typing before and after the upload. still typing."), "all of it landed in the chapter: " + t);
  ok(await pg.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus stayed in the chapter");
  await pg.locator('#notices').getByRole("button", { name: "Not now" }).click();
});

await kit.test("writing view: an added file never replaces what is selected", async () => {
  const keep = (await list()).find((i) => i.kind === "image");
  const rev = (await stored(doc)).revision;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, content: `Before the picture.\n\n![kept](${keep.url})\n\nAfter the picture.\n` });
  await openDoc();
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${MOD}+a`);
  await choose(pg, file("selected.png", png(26, 26, 200), "image/png"));
  await pg.locator('#notices input[type="text"]').waitFor({ timeout: 8000 });
  await pg.locator('#notices').getByRole("button", { name: "Insert image" }).click();
  await waitFor(async () => (await md(pg)).includes("![selected]"), "the new picture");
  const t = await md(pg);
  ok(t.includes("Before the picture.") && t.includes("After the picture.") && t.includes(`![kept](${keep.url})`), "everything that was selected is still there: " + t);
});

await kit.test("writing view: in Source mode an added file goes after the selection, and one undo takes it out", async () => {
  const rev = (await stored(doc)).revision;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, content: "Alpha beta gamma.\n" });
  await openDoc();
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${MOD}+e`);
  const ta = pg.locator(".scriobh-source");
  await ta.waitFor();
  await ta.evaluate((t) => { t.focus(); t.setSelectionRange(0, 5); });
  await choose(pg, file("source.png", png(14, 14, 210), "image/png"));
  await pg.locator('#notices input[type="text"]').waitFor({ timeout: 8000 });
  await pg.locator('#notices').getByRole("button", { name: "Insert image" }).click();
  await waitFor(async () => (await ta.inputValue()).includes("![source](/files/att_"), "the Markdown in the source (bars: " + (await bars(pg).allInnerTexts()).join(" | ") + "; mode: " + (await pg.evaluate(() => window.scriobh.write.editor.mode())) + ")");
  const v = await ta.inputValue();
  ok(v.startsWith("Alpha![source](/files/att_") && v.includes(") beta gamma."), "the selected word is still there and the picture follows it: " + v);
  await pg.keyboard.press(`${MOD}+z`);
  eq(await ta.inputValue(), "Alpha beta gamma.\n", "one undo in Source takes the insertion out");
  await pg.keyboard.press(`${MOD}+e`);
});

await kit.test("writing view: after a paste, the keyboard reaches the alt field and Enter inserts", async () => {
  const rev = (await stored(doc)).revision;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, content: "Pasting here.\n" });
  await openDoc();
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${MOD}+End`);
  await pg.evaluate(async (b64) => {
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const dt = new DataTransfer();
    dt.items.add(new File([bytes], "image.png", { type: "image/png" }));
    document.querySelector(".scriobh-prose").dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
  }, png(15, 15, 220).toString("base64"));
  const inp = pg.locator('#notices input[type="text"]');
  await inp.waitFor({ timeout: 8000 });
  ok(await pg.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus stays in the chapter while the bar appears");
  let reached = false, presses = 0;
  for (; presses < 10 && !reached; presses++) { await pg.keyboard.press("Shift+Tab"); reached = await inp.evaluate((e) => document.activeElement === e); }
  ok(reached, "Shift+Tab from the chapter reaches the alt field");
  ok(presses <= 6, "and within a few presses: " + presses);
  await inp.fill("A pasted screenshot");
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await md(pg)).includes("![A pasted screenshot](/files/att_"), "the picture");
});

await kit.test("writing view: the server's own answers: too large, then over the project's limit", async () => {
  // Without the client's own size check (the page is told there is no limit) the server answers 413 in plain words.
  kit.allowed.push(/status of 413/); // the server's refusals are 413 responses, by design
  await pg.route("**/api/v1/files/usage", (r) => r.fulfill({ json: {} }));
  await openDoc();
  await pg.evaluate(() => { document.querySelector(".scriobh-prose").focus(); });
  const bigPath = join(kit.tmp, "toolarge.png");
  writeFileSync(bigPath, Buffer.alloc(2 * 1024 * 1024, 7));
  await pg.locator('input[type="file"]').setInputFiles(bigPath);
  await bars(pg).first().waitFor({ timeout: 10000 });
  await waitFor(async () => /larger than 1(\.0)? MB|larger than the server accepts/.test(await bars(pg).first().innerText()), "the too-large message: " + (await bars(pg).first().innerText().catch(() => "")));
  await bars(pg).first().getByRole("button", { name: "Dismiss" }).click();
  await pg.unroute("**/api/v1/files/usage");
  // Fill the project: about 400 KB pictures until the server refuses.
  await openDoc();
  let said = "";
  for (let i = 0; i < 4 && !/storage limit/.test(said); i++) {
    await choose(pg, file(`fill-${i}.png`, bigPng(400), "image/png"));
    await waitFor(async () => (await bars(pg).count()) === 1 && /added|storage limit/.test(await bars(pg).first().innerText()), "an answer");
    said = await bars(pg).first().innerText();
    await bars(pg).first().getByRole("button", { name: /Not now|Dismiss/ }).click();
  }
  ok(/(This project has reached its storage limit|That file would take this project over its storage limit)\. Delete files you no longer need, then try again\./.test(said), "the quota message: " + said);
  ok(!said.includes("!"), "no exclamation mark");
  // The manager shows it too.
  await pg.goto(`${base()}/p/${book.slug}/files`);
  ok(/in \d+ files? of the 1\.0 MB/.test(await pg.locator(".usage-line").innerText()), "usage against the limit: " + (await pg.locator(".usage-line").innerText()));
  // Free the room again for the steps that follow.
  for (const it of (await list()).filter((i) => /^fill-/.test(i.filename))) await api(main, "DELETE", `/api/v1/projects/${book.id}/attachments/${it.id}`);
});

await kit.test("writing view: the same file twice says so and makes no second copy", async () => {
  await openDoc();
  const n = (await list()).length;
  await choose(pg, file("again.png", png(40, 30, 10), "image/png"));
  await pg.locator('#notices input[type="text"]').waitFor({ timeout: 8000 });
  ok((await pg.locator("#notices .att-msg").innerText()).includes("already here"), "said it was already here");
  eq((await list()).length, n, "no second copy");
  await pg.locator('#notices').getByRole("button", { name: "Not now" }).click();
});

await kit.test("writing view: refusals are in plain words, and an SVG is never sent", async () => {
  await openDoc();
  let sent = 0;
  pg.on("request", (r) => { if (/\/attachments\?/.test(r.url()) && r.method() === "POST") sent++; });
  await choose(pg, file("logo.svg", Buffer.from("<svg xmlns='http://www.w3.org/2000/svg'/>"), "image/svg+xml"));
  await bars(pg).first().waitFor();
  const t1 = await bars(pg).first().innerText();
  ok(/SVG pictures are not accepted/.test(t1) && !t1.includes("!"), "svg message: " + t1);
  await choose(pg, file("notes.docx", Buffer.from("x"), "application/vnd.openxmlformats-officedocument.wordprocessingml.document"));
  await waitFor(async () => (await bars(pg).count()) === 2, "a second bar");
  ok(/not a kind of file that can be added/.test(await bars(pg).nth(1).innerText()), "type message");
  await choose(pg, file("empty.png", Buffer.alloc(0), "image/png"));
  await waitFor(async () => (await bars(pg).count()) === 3, "a third bar");
  ok(/is empty/.test(await bars(pg).nth(2).innerText()), "empty message");
  const bigPath = join(kit.tmp, "huge.png");
  writeFileSync(bigPath, Buffer.alloc(40 * 1024 * 1024, 1));
  await pg.locator('input[type="file"]').setInputFiles(bigPath);
  await waitFor(async () => (await bars(pg).count()) === 4, "a fourth bar");
  ok(/more than the/.test(await bars(pg).nth(3).innerText()), "size message: " + (await bars(pg).nth(3).innerText()));
  eq(sent, 0, "none of these was sent");
  for (let i = 0; i < 4; i++) await bars(pg).first().getByRole("button", { name: "Dismiss" }).click();
  eq(await bars(pg).count(), 0, "all dismissed");
});

await kit.test("writing view: a pasted screenshot and a dropped PDF are added; a paste with text is left alone", async () => {
  await openDoc();
  const n = (await list()).length;
  await pg.locator(".scriobh-prose").click();
  const b64 = png(24, 24, 90).toString("base64");
  await pg.evaluate(async (b64) => {
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const dt = new DataTransfer();
    dt.items.add(new File([bytes], "image.png", { type: "image/png" }));
    document.querySelector(".scriobh-prose").dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
  }, b64);
  await pg.locator('#notices input[type="text"]').waitFor({ timeout: 8000 });
  eq((await list()).length, n + 1, "the pasted picture was stored");
  await pg.locator('#notices').getByRole("button", { name: "Not now" }).click();
  // Dropped PDF.
  await pg.evaluate(async (pdf) => {
    const dt = new DataTransfer();
    dt.items.add(new File([pdf], "tide tables.pdf", { type: "application/pdf" }));
    const paper = document.querySelector("#paper");
    for (const t of ["dragenter", "dragover", "drop"]) paper.dispatchEvent(new DragEvent(t, { dataTransfer: dt, bubbles: true, cancelable: true }));
  }, PDF.toString("latin1"));
  await pg.locator('#notices').getByRole("button", { name: "Insert link" }).waitFor({ timeout: 8000 });
  const before = await md(pg);
  await pg.locator('#notices').getByRole("button", { name: "Insert link" }).click();
  await waitFor(async () => /\[tide tables\.pdf\]\(\/files\/att_/.test(await md(pg)), "the PDF link");
  ok((await md(pg)).length > before.length, "inserted");
  // Text pasted together with a picture is the person's paste: nothing is uploaded.
  const m = (await list()).length;
  await pg.evaluate(async (b64) => {
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const dt = new DataTransfer();
    dt.setData("text/plain", "words");
    dt.items.add(new File([bytes], "image.png", { type: "image/png" }));
    document.querySelector(".scriobh-prose").dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
  }, b64);
  await sleep(700);
  eq(await bars(pg).count(), 0, "no bar for a paste that carries text");
  eq((await list()).length, m, "nothing uploaded");
});

await kit.test("writing view: Cancel stops an upload that is still going", async () => {
  await openDoc();
  const n = (await list()).length;
  await pg.route("**/attachments?filename=slow*", async (route) => { await sleep(2500); await route.continue().catch(() => {}); });
  await choose(pg, file("slow.png", png(30, 30, 120), "image/png"));
  await pg.locator("#notices progress").waitFor({ timeout: 5000 });
  await pg.locator('#notices').getByRole("button", { name: "Cancel" }).click();
  await waitFor(async () => (await bars(pg).count()) === 0, "the bar to go");
  await waitFor(async () => (await live(pg).innerText()).includes("Cancelled"), "the live region to say it was cancelled");
  await sleep(2800);
  await pg.unroute("**/attachments?filename=slow*");
  eq((await list()).length, n, "nothing was stored");
});

await kit.test("writing view: a remote picture is alt text and makes no request; an attached one is drawn", async () => {
  const rev = (await stored(doc)).revision;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, content: `![remote tracker](https://tracker.example/p.png) and ![local](${(await list()).find((i) => i.kind === "image").url})\n` });
  let off = 0;
  pg.on("request", (r) => { if (/tracker\.example/.test(r.url())) off++; });
  await openDoc();
  eq(await pg.locator(".scriobh-prose img.remote-image").count(), 1, "one remote picture drawn as alt text");
  await waitFor(() => pg.evaluate(() => { const i = document.querySelector('.scriobh-prose img[src^="/files/att_"]'); return i && i.complete && i.naturalWidth > 0; }), "the attached picture to load");
  eq(off, 0, "no request for the remote picture");
  ok((await md(pg)).includes("![remote tracker](https://tracker.example/p.png)"), "the Markdown still names it");
});

await kit.test("keyboard: the toolbar button opens the chooser; Escape in the alt field closes the bar", async () => {
  await openDoc();
  await pg.locator(".scriobh-prose").click();
  // The toolbar is off while the chapter is locked (a lease still held by the page just left); wait for it.
  await waitFor(() => pg.locator("#fmtbar [data-cmd=image]").isEnabled(), "the toolbar to be enabled", 10000);
  await pg.locator("#fmtbar [data-cmd=image]").focus();
  const chooser = pg.waitForEvent("filechooser", { timeout: 5000 });
  await pg.keyboard.press("Enter");
  const fc = await chooser;
  await fc.setFiles([file("keys.png", png(18, 18, 130), "image/png")]);
  const inp = pg.locator('#notices input[type="text"]');
  await inp.waitFor({ timeout: 8000 });
  ok(await inp.evaluate((e) => document.activeElement === e), "focus is in the alt field when it came from the toolbar");
  await pg.keyboard.press("Escape");
  eq(await bars(pg).count(), 0, "Escape closes the bar");
  ok(await pg.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus is back in the chapter");
});

await kit.test("manager: the grid, the space used, and a file's page with where it is used", async () => {
  await pg.goto(`${base()}/p/${book.slug}/files`);
  await pg.waitForSelector(".att-card");
  ok((await pg.locator(".att-card").count()) >= 4, "cards for the files");
  ok(await pg.locator(".att-thumb img").first().evaluate((i) => i.complete && i.naturalWidth > 0), "thumbnails load");
  const t = await pg.locator(".usage-line").innerText();
  ok(/in \d+ files? of the/.test(t), "usage line: " + t);
  await pg.locator(".att-card:has(img) .att-info h3 a").first().click();
  await pg.waitForSelector("#att-md");
  ok(/^!\[.*\]\(\/files\/att_/.test(await pg.locator("#att-md").innerText()), "the Markdown to copy");
  await pg.locator("[data-copy]").first().click();
  const clip = await pg.evaluate(() => navigator.clipboard.readText()).catch(() => "");
  if (clip) ok(/^!\[.*\]\(\/files\/att_/.test(clip), "copied: " + clip);
});

await kit.test("manager: rename and describe; the delete rule asks for the name when it is in use", async () => {
  const items = await list();
  const used = items.find((i) => i.refs > 0 && i.kind === "image");
  ok(used, "a file in use");
  await pg.goto(`${base()}/p/${book.slug}/files/${used.id}`);
  ok((await pg.locator("body").innerText()).includes("The Harbour") || (await pg.locator("body").innerText()).includes("Where it is used"), "where it is used");
  await pg.getByLabel("Name", { exact: true }).fill("harbour at dawn.png");
  await pg.getByLabel("Alt text").fill("Dawn over the harbour");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await pg.waitForSelector(".saved");
  ok((await pg.locator("h1").innerText()).includes("harbour at dawn.png"), "renamed");
  kit.allowed.push(/status of 422/); // the refused delete is a 422 page, by design
  await pg.getByRole("button", { name: "Delete the file" }).click();
  await pg.waitForSelector("#at-confirm", { timeout: 5000 }).catch(async () => { throw new Error("no typed confirmation; the page says: " + (await pg.locator("#attach").innerText()).replace(/\s+/g, " ").slice(0, 500)); });
  ok((await pg.locator(".banner-line").innerText()).includes("still in use"), "refused with a reason: " + (await pg.locator(".banner-line").innerText()));
  eq((await list()).length, items.length, "still there");
  await pg.locator("#at-confirm").fill("wrong");
  ok(await pg.getByRole("button", { name: "Delete it anyway" }).isDisabled(), "the button waits for the exact name");
  eq((await list()).length, items.length, "a wrong name deletes nothing");
  await pg.locator("#at-confirm").fill("harbour at dawn.png");
  await pg.getByRole("button", { name: "Delete it anyway" }).click();
  await pg.waitForURL(/\/files\?saved=deleted/);
  eq((await list()).length, items.length - 1, "deleted after the name was typed");
});

await kit.test("manager: add from the page, and the list follows without a reload", async () => {
  await pg.goto(`${base()}/p/${book.slug}/files`);
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.attach && window.scriobh.attach.add)), "the uploader", 8000);
  const n = await pg.locator(".att-card").count();
  await choose(pg, file("fresh.png", png(33, 21, 150), "image/png"), file("sheet.pdf", Buffer.concat([PDF, Buffer.from("% a different file\n")]), "application/pdf"));
  await waitFor(async () => (await pg.locator(".att-card").count()) === n + 2, "both cards to appear", 10000);
  await waitFor(async () => (await live(pg).innerText()).length > 0, "the live region to speak");
  // A drop onto the zone.
  await pg.evaluate((b64) => {
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const dt = new DataTransfer();
    dt.items.add(new File([bytes], "dropped.png", { type: "image/png" }));
    const z = document.querySelector("#att-drop");
    for (const t of ["dragenter", "dragover", "drop"]) z.dispatchEvent(new DragEvent(t, { dataTransfer: dt, bubbles: true, cancelable: true }));
  }, png(12, 12, 160).toString("base64"));
  await waitFor(async () => (await pg.locator(".att-card").count()) === n + 3, "the dropped card", 10000);
});

await kit.test("manager: a PDF has a download and a panel that opens on request", async () => {
  const pdf = (await list()).find((i) => i.kind === "pdf");
  await pg.goto(`${base()}/p/${book.slug}/files/${pdf.id}`);
  ok((await pg.getByRole("link", { name: "Download the PDF" }).getAttribute("href")).includes("download=1"), "download link");
  eq(await pg.locator("#att-frame iframe").count(), 0, "no frame until asked");
  await pg.getByRole("button", { name: "Preview here" }).click();
  await pg.waitForSelector("#att-frame iframe");
  eq(await pg.locator("#att-preview").getAttribute("aria-expanded"), "true", "expanded state");
  await pg.getByRole("button", { name: "Hide the preview" }).click();
  eq(await pg.locator("#att-frame iframe").count(), 0, "closed again");
  // The writing view's policy still forbids frames.
  const r = await pg.request.get(`${base()}/p/${book.slug}/d/${doc}`);
  ok(!/frame-src/.test(r.headers()["content-security-policy"] || ""), "the writing view allows no frames");
});

await kit.test("compendium: its own files, uploaded from its page", async () => {
  await pg.goto(`${base()}/c/${comp.slug}/files`);
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.attach && window.scriobh.attach.add)), "the uploader", 8000);
  await choose(pg, file("portrait.png", png(22, 28, 170), "image/png"));
  await pg.waitForSelector(".att-card", { timeout: 10000 });
  eq((await list("compendiums", comp.id)).length, 1, "stored on the Compendium");
  eq((await list()).some((i) => i.filename === "portrait.png"), false, "and not on the project");
  await pg.locator(".att-info h3 a").first().click();
  await pg.waitForSelector("#att-md");
  ok((await pg.locator("#att-md").innerText()).includes("/files/att_"), "its Markdown");
});

await kit.test("phone: the manager and a file's page fit 390 px, and every control is at least 44 px", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "phone");
  for (const path of [`/p/${book.slug}/files`, `/c/${comp.slug}/files`]) {
    await p.goto(base() + path);
    await p.waitForSelector(".att-card");
    const w = await p.evaluate(() => ({ sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth }));
    ok(w.sw <= w.cw, `${path} scrolls sideways: ${w.sw} > ${w.cw}`);
    const small = await p.evaluate(() => [...document.querySelectorAll("#attach .btn, #attach button, #attach .filters a")].filter((e) => !e.hidden && e.offsetParent).map((e) => ({ t: (e.textContent || "").trim().slice(0, 24), h: Math.round(e.getBoundingClientRect().height) })).filter((x) => x.h < 44));
    ok(small.length === 0, `${path} small targets: ${JSON.stringify(small)}`);
  }
  const one = (await list()).find((i) => i.kind === "image");
  await p.goto(`${base()}/p/${book.slug}/files/${one.id}`);
  await p.waitForSelector("#att-md");
  const w = await p.evaluate(() => ({ sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth }));
  ok(w.sw <= w.cw, `file page scrolls sideways: ${w.sw} > ${w.cw}`);
  await ctx.close();
});

await kit.test("without script: the lists and a file's page still read, and the forms still work", async () => {
  const ctx = await kit.newContext({ javaScriptEnabled: false, viewport: { width: 1280, height: 800 } });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  await p.goto(`${base()}/p/${book.slug}/files`);
  ok((await p.locator(".att-card").count()) > 0, "cards without script");
  ok((await p.locator("#att-drop").innerText()).includes("needs script"), "says that adding needs script");
  await p.locator(".att-info h3 a").first().click();
  await p.getByLabel("Name", { exact: true }).fill("no-script rename.png");
  await p.getByRole("button", { name: "Save", exact: true }).click();
  ok((await p.locator("h1").innerText()).includes("no-script rename.png"), "the rename form works without script");
  await ctx.close();
});

await kit.finish("e2e-attachments");
