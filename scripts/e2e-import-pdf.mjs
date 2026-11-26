#!/usr/bin/env node
// End-to-end test of importing a PDF and a Word document with tracked changes in Chromium: the chooser and its words, the
// PDF's fidelity report (pages read, confidence, what could and could not be recovered), chapters from headings with the
// mapping editor, the refusals (a scan, an encrypted file, not a PDF), the server being busy (a 429, stubbed in the browser)
// and trying again, the three choices for tracked changes with what the preview counts, the author labels in Review,
// what the apply says, 390 px and the right-to-left pseudo-locale.
//
//   node scripts/e2e-import-pdf.mjs [--only substring] [--shots dir]
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { makeDocx, writePdf } from "./fixtures/books.mjs";

const kit = await createKit("e2e-import-pdf");
kit.deadline(8 * 60 * 1000);
const argv = process.argv.slice(2);
kit.only = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
const shots = argv.includes("--shots") ? resolve(argv[argv.indexOf("--shots") + 1]) : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const pdf = writePdf(join(kit.tmp, "harbour.pdf"));
const scan = writePdf(join(kit.tmp, "scan.pdf"), { scanned: true });
const enc = writePdf(join(kit.tmp, "locked.pdf"), { encrypted: true });
const notPdf = join(kit.tmp, "notes.pdf");
writeFileSync(notPdf, "plain text, not a PDF");
const docx = makeDocx(join(kit.tmp, "harbour.docx"), kit.tmp);

const open = async (path = "/import") => { await pg.goto(base() + path); await pg.waitForSelector("#imp-fmt"); };
// A change is a form whose answer is swapped into the review: it has settled when htmx has nothing in flight.
const settle = async () => { await sleep(200); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"), null, { timeout: 25000 }); await sleep(100); };
const titles = () => pg.locator("#imp-tree .imp-title").allInnerTexts();
const nodeFor = (title) => pg.locator(`#imp-tree > li.imp-node:has(> .imp-row > .imp-title:text-is("${title}"))`).first();
// Edit opens a row's panel: it is fetched when the row's details open, and its controls are separate small forms.
const openEdit = async (title, key = false) => {
  const n = nodeFor(title);
  if (!(await n.locator("details.imp-edit").first().evaluate((d) => d.open))) {
    if (key) { await n.locator("summary").first().focus(); await pg.keyboard.press("Enter"); } else await n.locator("summary").first().click();
  }
  await n.locator(".imp-edit-body form").first().waitFor();
  await settle(); // the panel was just swapped in: htmx marks it added for a moment
  return n;
};
// The same file chosen again for the same target is the draft that was already waiting (with its edits), not a second one: a step that
// wants the file as it came starts with none open.
const discardDrafts = async () => { for (const d of (await api(main, "GET", "/api/v1/imports/drafts")).json.items) eq((await api(main, "DELETE", `/api/v1/imports/drafts/${d.id}`)).status, 200, "discard a draft"); };
const shot = async (name, page = pg) => {
  if (!shots) return;
  const v = page.viewportSize(), hgt = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: v.width, height: Math.min(hgt, 4000) });
  await page.screenshot({ path: join(shots, name + ".png"), caret: "initial" });
  await page.setViewportSize(v);
};
const previewPdf = async (file = pdf) => { await discardDrafts(); await open(); await pg.check("#imp-f-pdf"); await pg.locator("#imp-pdffile").setInputFiles(file); await pg.locator("#imp-h").waitFor({ timeout: 25000 }); await pg.waitForLoadState("load"); await settle(); };
const previewDocx = async () => { await discardDrafts(); await open(); await pg.check("#imp-f-docx"); await pg.locator("#imp-docx").setInputFiles(docx); await pg.locator("#imp-h").waitFor({ timeout: 25000 }); await pg.waitForLoadState("load"); await settle(); };

await kit.test("PDF: the preview says what was read, what could not be recovered, and proposes chapters from headings", async () => {
  await previewPdf();
  const sum = await pg.locator(".imp-sum").innerText();
  ok(/A PDF/.test(sum) && /3 documents/.test(sum), "the summary: " + sum);
  const box = await pg.locator(".imp-book").innerText();
  for (const w of [/Pages read\s*4 \(4 with text\)/, /Confidence\s*90% on average; pages under 70%: 0/, /proposed from headings/, /Tables: none found/, /Pictures: none found/, /Links, underline, colour and type size: not kept/, /Columns: every page was read as one column/, /Scanned pages: none/, /Running head and page number lines removed: 6/, /The PDF itself is not kept/]) ok(w.test(box), `the PDF panel does not say ${w}:\n${box}`);
  eq((await titles()).join("|"), "Arrival|The Quay|Departure", "the chapters from headings");
  // a mark and words, never colour alone
  eq(await pg.locator('.imp-book li[data-ok="false"]').count() > 0, true, "a limit is marked");
  ok(/not kept/.test(await pg.locator('.imp-book li[data-ok="false"]').first().innerText()), "the mark has words with it");
  await pg.locator("#imp-review details", { hasText: "importer noted" }).locator("summary").click();
  ok(/Only the text layer is read/.test(await pg.locator("#imp-review details", { hasText: "importer noted" }).innerText()), "the server's own sentences are kept, folded");
  await shot("pdf-preview-1280");
});

await kit.test("PDF: the same mapping editor, by keyboard, and the apply makes the project; no edition, since the PDF is not kept", async () => {
  await previewPdf();
  await openEdit("The Quay", true);
  await pg.getByRole("button", { name: "Move The Quay up" }).focus();
  await pg.keyboard.press("Enter");
  await settle();
  eq((await titles()).join("|"), "The Quay|Arrival|Departure", "reorder");
  const n = await openEdit("Departure");
  const sel = n.getByRole("combobox", { name: "Join into", exact: true }); // the picker is in the panel: no opener, no Cancel
  await sel.selectOption(await sel.locator("option", { hasText: "Arrival" }).first().getAttribute("value"));
  await n.locator('button[data-act="merge"]').click();
  await settle();
  eq((await titles()).join("|"), "The Quay|Arrival", "the join");
  await pg.locator("#imp-t").fill("The Harbour Book");
  await pg.locator("[data-go]").click();
  await pg.waitForURL(/\/p\/[^/]+\/d\/doc_/, { timeout: 25000 });
  const slug = new URL(pg.url()).pathname.split("/")[2];
  const pr = (await api(main, "GET", "/api/v1/projects")).json.items.find((p) => p.slug === slug);
  eq((await api(main, "GET", `/api/v1/projects/${pr.id}/editions`)).json.items.length, 0, "a PDF made an edition");
  await pg.goto(`${base()}/p/${slug}/editions`);
  ok(/No imported books yet/.test(await pg.locator("main").innerText()), "the empty state");
});

await kit.test("PDF refusals: a scan, an encrypted file and a file that is not a PDF, in the op's words, with no promise of recognition", async () => {
  kit.allowed.push(/status of 422/);
  await discardDrafts();
  for (const [file, re] of [[scan, /No text layer: its pages are pictures|This PDF has no text layer/i], [enc, /encrypted \(password protected\)/], [notPdf, /not a PDF/]]) {
    await open();
    await pg.check("#imp-f-pdf");
    await pg.locator("#imp-pdffile").setInputFiles(file);
    // The words are looked for wherever the refusal is drawn (the chooser's message, or a page of its own): where it must be is the next step.
    await waitFor(async () => re.test(await pg.locator("main").innerText()), `the refusal ${re}`, 25000);
  }
  await open();
  await pg.check("#imp-f-pdf");
  await pg.locator("#imp-pdffile").setInputFiles(scan);
  // (the chooser's own words about scans also say "character recognition": the wait is for the refusal itself)
  await waitFor(async () => /this PDF has no text layer/i.test(await pg.locator("main").innerText()), "the scan refusal");
  ok(/does no character recognition/.test(await pg.locator("main").innerText()), "the refusal does not say there is no recognition");
  ok(!/we will|soon|coming/i.test(await pg.locator("main").innerText()), "a promise of recognition");
});

await kit.test("PDF refusals: a refused PDF leaves the person on the chooser with the sentence in its message, the file still chosen, and nothing kept", async () => {
  kit.allowed.push(/status of 422/);
  await discardDrafts();
  const bad = [];
  for (const [file, re] of [[scan, /No text layer: its pages are pictures|This PDF has no text layer/i], [enc, /encrypted \(password protected\)/], [notPdf, /not a PDF/i]]) {
    await open();
    await pg.check("#imp-f-pdf");
    await pg.locator("#imp-pdffile").setInputFiles(file);
    await waitFor(async () => re.test(await pg.locator("main").innerText()), `the refusal ${re}`, 25000);
    await pg.waitForLoadState("load");
    const name = file.split("/").pop();
    const sentence = (await pg.locator("main").innerText()).split("\n").find((l) => re.test(l)) || "";
    if (!/^[A-Z]/.test(sentence)) bad.push(`${name}: the refusal does not begin with a capital: ${sentence.slice(0, 40)}`);
    if ((await pg.locator("#imp-stage-err").count()) !== 1) { bad.push(`${name}: the sentence is not the chooser's message (#imp-stage-err); the page is ${new URL(pg.url()).pathname.replace(/imd_\w+/, "imd_…")}`); continue; }
    const t = await pg.locator("#imp-stage-err").innerText();
    if (!re.test(t) || !/^[A-Z]/.test(t)) bad.push(`${name}: the message is ${t}`);
    if ((await pg.locator("#imp-pdffile").evaluate((i) => i.files.length)) !== 1) bad.push(`${name}: the file is not still chosen`);
  }
  const kept = (await api(main, "GET", "/api/v1/imports/drafts")).json.items.filter((d) => d.status === "open");
  if (kept.length) bad.push(`a refused file is kept as an open draft: ${kept.map((d) => d.name).join(", ")}`);
  ok(bad.length === 0, bad.join("; "));
});

await kit.test("PDF busy: when the person is already sending a file, it is said calmly, the file stays chosen, and the button sends it again", async () => {
  // The server lets one upload at a time in for a person. One is held open here by hand (the headers of a form post whose body never
  // comes), so the page's own upload meets a real busy server, not a stub.
  kit.allowed.push(/status of 422/);
  await discardDrafts();
  const { connect } = await import("node:net");
  const u = new URL(base());
  const cookie = (await main.cookies()).map((c) => `${c.name}=${c.value}`).join("; ");
  const held = connect(Number(u.port), u.hostname);
  await new Promise((r) => held.once("connect", r));
  held.write(`POST /ui/import/stage HTTP/1.1\r\nHost: ${u.host}\r\nCookie: ${cookie}\r\nContent-Type: multipart/form-data; boundary=xx\r\nContent-Length: 5000000\r\n\r\n--xx\r\n`);
  await sleep(500);
  try {
    await open();
    await pg.check("#imp-f-pdf");
    await pg.locator("#imp-pdffile").setInputFiles(pdf);
    const err = pg.locator("#imp-stage-err");
    await err.waitFor({ timeout: 25000 });
    ok(/already sending a file/.test(await err.innerText()), "the busy sentence: " + (await err.innerText()));
    ok(/Wait for it to finish, then try again/.test(await err.innerText()), "the sentence does not say what to do");
    await waitFor(() => err.evaluate((n) => n === document.activeElement), "the sentence to take focus", 3000);
    eq(await pg.locator("#imp-pdffile").evaluate((i) => i.files.length), 1, "the file is no longer chosen");
    eq(await pg.locator("#imp-review").count(), 0, "a review is shown while busy");
    held.destroy();
    await sleep(800); // the server lets go of the held upload when its connection ends
    const retry = pg.locator('form[data-fmt="pdf"] button[type=submit]');
    eq((await retry.innerText()).trim(), "Review what would be created", "the button's words");
    await retry.click();
    await pg.locator("#imp-h").waitFor({ timeout: 25000 });
    ok(/A PDF/.test(await pg.locator(".imp-sum").innerText()), "the retry did not make the review");
  } finally { held.destroy(); }
});

await kit.test("Word: the three choices for tracked changes are each explained, with the keyboard alone", async () => {
  await open();
  await pg.locator("#imp-f-md").focus();
  await pg.keyboard.press("ArrowDown");
  const t = await pg.locator('[data-fmt="docx"]').innerText();
  for (const w of [/Suggest them\./, /Accept them\./, /Reject them\./, /each change a reviewer made waits as a suggestion/, /the later change is already in the text/, /whole paragraph a reviewer deleted is gone/]) ok(w.test(t), `the Word panel does not say ${w}`);
  eq(await pg.locator('input[name="opt.docx_changes"]:checked').getAttribute("value"), "suggest", "suggest is the default");
  ok(!/not brought in; the preview/.test(t) || /Pictures are not brought in/.test(t), "the hint still says comments and tracked changes are dropped");
  await pg.locator("#imp-dc-suggest").focus();
  await pg.keyboard.press("ArrowDown");
  eq(await pg.locator('input[name="opt.docx_changes"]:checked').getAttribute("value"), "accept", "arrow keys move through the three");
});

await kit.test("Word: the preview counts suggestions, comments and what was taken as accepted, names the two authors, and says what the chapters hold", async () => {
  await previewDocx();
  const box = await pg.locator(".imp-book").innerText();
  for (const w of [/The chapters hold the original text, as its author left it/, /Suggestions to be written: 3/, /Word comments to be written as comments: 1/, /Changes that cannot be suggestions and are taken as the reviewer left them: 2/, /In whole paragraphs a reviewer deleted \(that text is not kept\): 1/, /Too close to a change by another author: 1/, /Bo Reader \(from a Word file\) \/ Ann Editor \(from a Word file\)/, /Ann Editor \(from a Word file\), changes: 2/, /Bo Reader \(from a Word file\), changes: 2/, /later change is already in the text/]) ok(w.test(box), `the Word panel does not say ${w}:\n${box}`);
  await shot("docx-preview-1280");
  // accept: no suggestions
  await pg.check("#imp-dc-accept");
  await settle();
  const b2 = await pg.locator(".imp-book").innerText();
  ok(/final text, with every change accepted/.test(b2) && !/Suggestions to be written/.test(b2), "accept: " + b2);
  await pg.check("#imp-dc-reject");
  await settle();
  ok(/original text, with every change rejected/.test(await pg.locator(".imp-book").innerText()), "reject");
});

await kit.test("Word: the apply writes the suggestions, the chapter holds the original, Review names the author as a Word author, and the result is first", async () => {
  await previewDocx();
  await pg.locator("#imp-t").fill("The Harbour Notes");
  await pg.locator("[data-go]").click();
  // The apply wrote suggestions and comments, so the answer is the draft's own page, with the result first.
  await waitFor(async () => /Imported 2 documents/.test(await pg.locator("#imp-h").innerText()) && !(await pg.locator("[data-go]").count()), "the result");
  const focusedId = await pg.evaluate(() => document.activeElement && document.activeElement.id); // asserted last: what follows is still checked
  const text = await pg.locator("#imp-review").innerText();
  ok(/Suggestions waiting in Review, under the reviewers' Word names: 3/.test(text) && /Word comments written as comments: 1/.test(text), "the result: " + text);
  const top = await pg.locator("#imp-h").boundingBox();
  ok(top && top.y < 400, "the result is not at the top: " + (top && top.y));
  const made = (await api(main, "GET", "/api/v1/projects")).json.items.find((p) => p.title === "The Harbour Notes");
  const docs = (await api(main, "GET", `/api/v1/projects/${made.id}/docs`)).json.items;
  const one = docs.find((d) => d.title === "One");
  const body = (await api(main, "GET", `/api/v1/docs/${one.id}`)).json.item.content;
  ok(/came in at dawn and the harbour was quiet\. Nobody ever spoke/.test(body) && !/first light/.test(body), "the chapter does not hold the original (with the later author's change): " + body.slice(0, 200));
  await pg.getByRole("link", { name: "Review" }).click();
  await pg.waitForURL(/\/review$/);
  const rv = await pg.locator("main").innerText();
  ok(/Ann Editor \(from a Word file\)/.test(rv), "Review does not show the Word label: " + rv.slice(0, 600));
  kit.word = made;
  eq(focusedId, "imp-h", "the result does not take the focus");
});

await kit.test("390 px: the PDF and Word previews fit, with touch targets of 44 px", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 800 }, hasTouch: true, isMobile: true });
  await ctx.addCookies(await main.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "phone");
  for (const [radio, input, file, h] of [["#imp-f-pdf", "#imp-pdffile", pdf, ".imp-book dl"], ["#imp-f-docx", "#imp-docx", docx, ".imp-book h3"]]) {
    await discardDrafts();
    await p.goto(base() + "/import");
    await p.waitForSelector("#imp-fmt");
    await p.check(radio);
    await p.locator(input).setInputFiles(file);
    await p.locator("#imp-h").waitFor({ timeout: 25000 });
    await p.waitForLoadState("load");
    await sleep(600);
    const m = await p.evaluate(() => {
      const small = [];
      for (const e of document.querySelectorAll(".imp-book summary, .imp-book input, .imp-book a, .imp-radios input, .imp-radios .check")) {
        const r = e.getBoundingClientRect();
        if (r.width && (r.height < 43.5 && !e.matches("input"))) small.push(`${(e.textContent || e.id).slice(0, 30)} ${Math.round(r.width)}x${Math.round(r.height)}`);
      }
      return { over: document.documentElement.scrollWidth > document.documentElement.clientWidth, small };
    });
    eq(m.over, false, `${radio}: the page scrolls sideways at 390 px`);
    eq(m.small.join("; "), "", `${radio}: controls under 44 px`);
    await shot(radio.slice(7) + "-preview-390", p);
  }
  await ctx.close();
});

await kit.test("the right-to-left pseudo-locale: the PDF and Word panels come from the catalogue and nothing overflows", async () => {
  eq((await api(main, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } })).status, 200, "set the pseudo-locale");
  try {
    await open();
    eq(await pg.locator("html").getAttribute("dir"), "rtl", "right to left");
    await pg.check("#imp-f-pdf");
    ok((await pg.locator('[data-fmt="pdf"]').innerText()).includes("⟦"), "the chooser's PDF words are not from the catalogue");
    await pg.check("#imp-f-docx");
    ok((await pg.locator('[data-fmt="docx"]').innerText()).includes("⟦"), "the Word chooser's words are not from the catalogue");
    await previewPdf();
    ok((await pg.locator(".imp-book").innerText()).includes("⟦"), "the PDF panel's words are not from the catalogue");
    eq(await pg.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "the PDF preview scrolls sideways");
    eq((await pg.locator(".imp-book li, .imp-book p, .imp-book h3, .imp-book h4").evaluateAll((l) => l.filter((e) => !e.textContent.includes("⟦")).map((e) => e.textContent.slice(0, 40)))).join(" | "), "", "words of the PDF panel that did not come from the catalogue");
    await previewDocx();
    ok((await pg.locator(".imp-book").innerText()).includes("⟦"), "the Word panel's words are not from the catalogue");
    eq(await pg.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "the Word preview scrolls sideways");
    eq((await pg.locator(".imp-book li, .imp-book p, .imp-book h3, .imp-book h4").evaluateAll((l) => l.filter((e) => !e.textContent.includes("⟦")).map((e) => e.textContent.slice(0, 40)))).join(" | "), "", "words of the Word panel that did not come from the catalogue");
    await shot("docx-preview-rtl-1280");
  } finally { await api(main, "PUT", "/api/v1/me/prefs", { items: { language: "en" } }); }
});

await kit.finish("e2e-import-pdf");
