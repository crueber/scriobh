#!/usr/bin/env node
// End-to-end test of the staged import and its server-rendered review, in Chromium: a Word document is chosen once and held on the
// server, the review is a page drawn from the draft (a reload, the Back button and a second tab all see the draft as it is), every
// change is a small form that swaps the review body, nothing of the review is kept in the browser's storage, the whole walk works with
// the keyboard alone and with no script, at 390 px and in both themes, and a reload with the network cut shows the offline page while
// the draft survives.
//
//   node scripts/e2e-import-review.mjs [--only substring] [--shots dir]
import { mkdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { crc32 } from "node:zlib";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { signIn } from "./harness.mjs";

const kit = await createKit("e2e-import-review");
kit.deadline(8 * 60 * 1000);
const argv = process.argv.slice(2);
kit.only = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
const shots = argv.includes("--shots") ? resolve(argv[argv.indexOf("--shots") + 1]) : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

// A stored zip written by hand (no dependency): entries are named files with their bytes.
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
const docxOf = (chapters, tag = "") => zipStore({
  "[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
  "word/document.xml": `<?xml version="1.0" encoding="UTF-8"?><w:document ${W}><w:body>${chapters.map((c) => `<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>${c}</w:t></w:r></w:p><w:p><w:r><w:t xml:space="preserve">${(c + " over the grey stones the gulls wheeled. " + tag).repeat(14)}</w:t></w:r></w:p>`).join("")}</w:body></w:document>`,
  "word/styles.xml": `<?xml version="1.0"?><w:styles ${W}><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>`,
});
const docxFile = (name, chapters, tag) => { const p = join(kit.tmp, name); writeFileSync(p, docxOf(chapters, tag)); return p; };

const shot = async (name, page = pg) => {
  if (!shots) return;
  const v = page.viewportSize(), hgt = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: v.width, height: Math.min(hgt, 3000) });
  await page.screenshot({ path: join(shots, name + ".png"), caret: "initial" });
  await page.setViewportSize(v);
};
const idle = async (page = pg) => { await sleep(150); await page.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added")); await sleep(80); };
const open = async (page = pg, path = "/import") => { await page.goto(base() + path); await page.waitForSelector("#imp-fmt"); };
const titles = (page = pg) => page.locator("#imp-review > .imp-tree > .imp-node > .imp-row .imp-title").allInnerTexts();
const rowOf = (title, page = pg) => page.locator(`#imp-review > .imp-tree > .imp-node:has(> .imp-row > .imp-title:text-is("${title}"))`).first();
const draftId = (page = pg) => new URL(page.url()).pathname.split("/").pop();
const storage = (page = pg) => page.evaluate(() => ({ local: Object.keys(localStorage).filter((k) => k !== "scriobh:zone-sent").map((k) => k + "=" + localStorage.getItem(k).slice(0, 80)), session: Object.keys(sessionStorage) }));
const stage = async (file, page = pg) => {
  await open(page);
  await page.check("#imp-f-docx");
  await page.locator("#imp-docx").setInputFiles(file);
  await page.waitForURL(/\/import\/d\/imd_/, { timeout: 20000 });
  await page.locator("#imp-h").waitFor();
};
const keyOpen = async (title, page = pg) => {
  const r = rowOf(title, page);
  const s = r.locator("summary").first();
  if (!(await r.locator("details.imp-edit").first().evaluate((d) => d.open))) { await s.focus(); await page.keyboard.press("Enter"); }
  await r.locator(".imp-edit-body form").first().waitFor();
  await idle(page); // the panel was just swapped in: htmx marks it added for a moment, and a press in that moment is a plain post
  return r;
};

// The script's one person may hold five open imports (the most at once): each step that leaves a draft open discards it at the end.
const discardDraft = async () => {
  await pg.locator("form.imp-discard button").first().click();
  await pg.waitForURL(/\/import\/?(\?.*)?$/, { timeout: 20000 });
};
const book = docxFile("salt-road.docx", ["The Road", "The Mill", "Snow", "The End"]);

await kit.test("a Word file is chosen once, held on the server, and the review is a page of its own", async () => {
  await stage(book);
  const sum = await pg.locator(".imp-sum").innerText();
  ok(/4 documents/.test(sum) && /A Word document/.test(sum), "the summary: " + sum);
  eq((await titles()).join("|"), "The Road|The Mill|Snow|The End", "the chapters");
  eq(await pg.locator("#imp-t").inputValue(), "Salt Road", "the project's title starts as the file's name");
  ok(/^\/import\/d\/imd_[0-9A-Za-z]+$/.test(new URL(pg.url()).pathname), "the review has an address: " + pg.url());
  // the staged file is on the server, outside what a backup holds
  const list = (await api(main, "GET", "/api/v1/imports/drafts")).json.items;
  eq(list.length, 1, "one draft is open");
  eq(list[0].kind + "/" + list[0].status, "docx/open", "the draft");
  await shot("review-word-light-1280");
});

await kit.test("every change is a small form that swaps the review body, with the keyboard alone", async () => {
  let r = await keyOpen("The Mill");
  await r.locator('button[data-act="up"]').first().focus();
  await pg.keyboard.press("Enter");
  await idle();
  eq((await titles()).join("|"), "The Mill|The Road|Snow|The End", "The Mill moved up");
  ok(/Moved The Mill above The Road: now 1 of 4\./.test(await pg.locator("#imp-live").innerText()), "the words for a screen reader: " + (await pg.locator("#imp-live").innerText()));
  // The button that can go no further is off, and focus has gone where a key press can go on from.
  const active = await pg.evaluate(() => document.activeElement && (document.activeElement.id || document.activeElement.tagName));
  ok(/^imp-s-/.test(active), "focus after the last move up: " + active);
  ok(await rowOf("The Mill").locator('button[data-act="up"]').first().isDisabled(), "Move up is off for the first row");
  // Rename with the keyboard: the change fires on leaving the field.
  r = rowOf("Snow");
  await r.locator("summary").first().focus();
  await pg.keyboard.press("Enter");
  await r.locator('input[data-act="title"]').waitFor();
  await idle();
  await r.locator('input[data-act="title"]').fill("First Snow");
  await pg.keyboard.press("Tab");
  await idle();
  ok((await titles()).includes("First Snow"), "the rename: " + (await titles()));
  // Retype, leave out, put back.
  r = await keyOpen("The End");
  await r.locator('select[data-act="kind"]').selectOption("back_matter");
  await idle();
  ok(/Back matter/.test(await rowOf("The End").locator(".imp-meta").innerText()), "the kind");
  r = await keyOpen("The End");
  await r.locator('button[data-act="skip"]').focus();
  await pg.keyboard.press("Enter");
  await idle();
  ok(!(await titles()).includes("The End"), "left out");
  ok(/Left out of the import \(1\)/.test(await pg.locator(".imp-left").innerText()), "the left-out list");
  const onPutBack = await pg.evaluate(() => document.activeElement && document.activeElement.getAttribute("aria-label"));
  eq(onPutBack, "Put The End back", "focus after Leave out is on the Put back of what left");
  await pg.getByRole("button", { name: "Put The End back" }).focus();
  await pg.keyboard.press("Enter");
  await idle();
  ok((await titles()).includes("The End"), "put back");
  // Undo goes back one step (the put back), the chapter is out again.
  await pg.locator("#imp-undo").focus();
  await pg.keyboard.press("Enter");
  await idle();
  ok(!(await titles()).includes("The End"), "undo");
});

await kit.test("a reload, the Back button and the list of imports in progress all find the draft as it was", async () => {
  const id = draftId(), t1 = (await titles()).join("|");
  await pg.reload();
  await pg.locator("#imp-h").waitFor();
  eq((await titles()).join("|"), t1, "a reload shows the same review");
  ok(/Left out of the import \(1\)/.test(await pg.locator(".imp-left").innerText()), "the left-out list survives a reload");
  // nothing of the review is in the browser's storage
  const st = await storage();
  // htmx remembers the current path (an address, no text) for its history; nothing else, and nothing of the review's words
  eq(st.local.length, 0, "localStorage holds: " + JSON.stringify(st.local));
  ok(st.session.every((k) => k === "htmx-current-path-for-history"), "sessionStorage holds: " + JSON.stringify(st.session));
  ok(!JSON.stringify(await pg.evaluate(() => ({ ...sessionStorage }))).includes("grey stones"), "the review's text is in sessionStorage");
  // Back is the chooser, which lists the draft
  await pg.goBack();
  await pg.waitForSelector("#imp-fmt");
  const listed = await pg.locator("#imp-drafts").innerText();
  ok(/salt-road/.test(listed) && /Resume/.test(listed) && /Discard/.test(listed), "the chooser's list: " + listed);
  await pg.getByRole("link", { name: /Resume the import of salt-road/ }).click();
  await pg.locator("#imp-h").waitFor();
  eq(draftId(), id, "Resume opens the same draft");
  eq((await titles()).join("|"), t1, "and the same review");
  // edits do not step through history: one Back from here is the chooser
  await pg.goBack();
  await pg.waitForSelector("#imp-fmt");
  await pg.goForward();
  await pg.locator("#imp-h").waitFor();
});

await kit.test("a second tab sees the same draft, and its stale change is refused with the current version", async () => {
  const tab2 = await main.newPage();
  kit.watch(tab2, "tab2");
  await tab2.goto(pg.url());
  await tab2.locator("#imp-h").waitFor();
  eq((await titles(tab2)).join("|"), (await titles()).join("|"), "both tabs show one state");
  // tab 2 opens a row's panel now, so its forms carry this revision
  const r2 = await keyOpen("The Road", tab2);
  // change in tab 1
  let r = await keyOpen("First Snow");
  await r.locator('button[data-act="up"]').focus();
  await pg.keyboard.press("Enter");
  await idle();
  // the same draft, a change in tab 2 made on the revision before: refused, the current version drawn, a notice that takes focus
  kit.allowed = [/status of 422/];
  await r2.locator('button[data-act="down"]').focus();
  await tab2.keyboard.press("Enter");
  await idle(tab2);
  const notice = tab2.locator("#imp-notice");
  await notice.waitFor();
  ok(/changed in another tab/.test(await notice.innerText()), "the notice: " + (await notice.innerText()));
  ok(await notice.evaluate((n) => n === document.activeElement), "the notice has focus");
  eq((await titles(tab2)).join("|"), (await titles()).join("|"), "the stale tab now shows the current version");
  await tab2.close();
});

await kit.test("another way to cut is read again on the server, the edits of chapters that are gone are listed, and Undo brings them back", async () => {
  const before = (await titles()).join("|");
  await pg.locator('#imp-cutform input[value="none"]').check();
  await pg.locator("#imp-cut-apply").click();
  await idle();
  ok((await titles()).join("|") !== before, "one document reads the file again: " + (await titles()));
  ok(/no longer apply|no longer applies/.test(await pg.locator("#imp-notice").innerText()), "what was lost is said: " + (await pg.locator("#imp-notice").innerText()));
  await pg.locator("#imp-undo").click();
  await idle();
  eq((await titles()).join("|"), before, "Undo restores the edits");
  eq(await pg.locator('#imp-cutform input:checked').inputValue(), "auto", "and the way it was cut");
});

await kit.test("the review at 390 px has no sideways scroll and big targets, in both themes", async () => {
  await pg.setViewportSize({ width: 390, height: 844 });
  await pg.reload();
  await pg.locator("#imp-h").waitFor();
  const r = await keyOpen("The Mill");
  const over = await pg.evaluate(() => document.documentElement.scrollWidth - innerWidth);
  ok(over <= 0, `the page scrolls sideways by ${over}px at 390 px`);
  const small = await pg.evaluate(() => [...document.querySelectorAll("#imp-review button:not(.sr-only), #imp-review summary, #imp-review select, #imp-review input[type=text]")].filter((e) => e.getBoundingClientRect().height > 0 && e.getBoundingClientRect().height < 40).map((e) => (e.getAttribute("aria-label") || e.textContent || e.id).trim().slice(0, 30)));
  ok(small.length === 0, "controls under 40 px high: " + small.join(", "));
  await shot("review-word-light-390");
  await pg.emulateMedia({ colorScheme: "dark" });
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await shot("review-word-dark-390");
  await pg.setViewportSize({ width: 1280, height: 900 });
  await shot("review-word-dark-1280");
  await pg.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  await pg.emulateMedia({ colorScheme: "light" });
  void r;
});

await kit.test("the Import button creates the book, and the finished address says so; the stage is deleted", async () => {
  await pg.locator("#imp-t").fill("Salt Road, imported");
  await pg.locator("#imp-t").press("Tab");
  await idle();
  const id = draftId();
  await pg.locator("[data-go]").focus();
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/p\/salt-road-imported\/d\/doc_/, { timeout: 20000 });
  const made = (await api(main, "GET", "/api/v1/projects")).json.items.find((p) => p.title === "Salt Road, imported");
  ok(made, "the project was not created");
  const d = (await api(main, "GET", `/api/v1/projects/${made.id}/docs`)).json;
  ok(d.items.length === 3, "documents: " + d.items.map((i) => i.title));
  // the finished draft: counts and a link, no review
  await pg.goto(`${base()}/import/d/${id}`);
  const text = await pg.locator("#imp-review").innerText();
  ok(/Imported 3 documents/.test(text), "the result page: " + text.slice(0, 120));
  eq(await pg.locator("[data-go]").count(), 0, "a finished import offers no Import button");
  const row = (await api(main, "GET", `/api/v1/imports/drafts/${id}`)).json.item;
  eq(row.status, "done", "the draft is done");
  ok(row.result && row.result.documents === 3 && !JSON.stringify(row).includes("grey stones"), "the finished row holds counts only");
});

await kit.test("with no script: choose, review, change, apply, all as plain forms", async () => {
  const ctx = await kit.newContext({ javaScriptEnabled: false, viewport: { width: 1280, height: 800 } });
  const p = await ctx.newPage();
  kit.watch(p, "noscript");
  await signIn(p, base());
  await p.goto(base() + "/import");
  await p.check("#imp-f-docx");
  await p.locator("#imp-docx").setInputFiles(docxFile("plain.docx", ["Alpha", "Beta", "Gamma"], "plain"));
  await p.locator('form[data-fmt="docx"] button[type=submit]').click();
  await p.waitForURL(/\/import\/d\/imd_/);
  eq((await titles(p)).join("|"), "Alpha|Beta|Gamma", "the review with no script");
  // the Edit link opens a row's panel on the page, and its buttons are real buttons
  await p.locator(`#imp-review > .imp-tree > .imp-node:has(.imp-title:text-is("Beta")) a`).click();
  await p.waitForURL(/edit=/);
  await p.locator('button[data-act="up"]').first().click();
  await p.waitForURL(/\/import\/d\/imd_/);
  eq((await titles(p)).join("|"), "Beta|Alpha|Gamma", "a move with no script");
  await p.locator("[data-go]").click();
  await p.waitForURL(/\/p\/[^/]+\/d\/doc_/);
  await ctx.close();
});

await kit.test("a refused file says why, in words, and keeps nothing", async () => {
  kit.allowed = [/status of 422/];
  const bad = join(kit.tmp, "notes.docx");
  writeFileSync(bad, "only words, not a zip");
  await open();
  await pg.check("#imp-f-docx");
  await pg.locator("#imp-docx").setInputFiles(bad);
  const err = pg.locator("#imp-stage-err");
  await err.waitFor();
  ok(/not a Word document/.test(await err.innerText()), "the reason: " + (await err.innerText()));
  await waitFor(() => err.evaluate((n) => n === document.activeElement), "the reason to take focus", 3000);
  const list = (await api(main, "GET", "/api/v1/imports/drafts")).json.items.filter((d) => d.status === "open");
  eq(list.length, 0, "nothing was kept");
});

await kit.test("discarding goes back to the chooser, which no longer lists it", async () => {
  await stage(docxFile("gone.docx", ["Only"], "gone"));
  const id = draftId();
  await pg.getByRole("button", { name: "Discard this import" }).click();
  await pg.waitForURL(/\/import$/);
  ok(!/gone/.test(await pg.locator("body").innerText().then((t) => (t.match(/Imports in progress[\s\S]*/) || [""])[0])), "the list still shows it");
  eq((await api(main, "GET", `/api/v1/imports/drafts/${id}`)).status, 404, "the draft is gone");
});

await kit.test("a reload with the network cut shows the offline page, and the draft is intact when the network returns", async () => {
  const ctx = await kit.newContext({ serviceWorkers: "allow", viewport: { width: 1000, height: 700 } });
  const p = await ctx.newPage();
  kit.watch(p, "offline");
  kit.allowed = [/ERR_INTERNET_DISCONNECTED/, /Failed to load resource/, /net::ERR/];
  await signIn(p, base());
  await stage(docxFile("offline.docx", ["Tide", "Salt"], "offline"), p);
  const url = p.url(), t1 = (await titles(p)).join("|");
  await waitFor(() => p.evaluate(async () => { const r = await navigator.serviceWorker.ready; return r.active && r.active.state === "activated" && !!navigator.serviceWorker.controller; }), "the worker", 20000);
  await ctx.setOffline(true);
  try {
    const res = await p.reload({ waitUntil: "load" });
    const text = await p.locator("body").innerText();
    ok(/cannot reach the server/.test(text), "the offline notice: " + text.slice(0, 100));
    ok(!text.includes("Tide"), "the offline page holds the manuscript");
    eq(res.status(), 200, "the offline page is a normal answer");
  } finally { await ctx.setOffline(false); }
  await p.goto(url);
  await p.locator("#imp-h").waitFor();
  eq((await titles(p)).join("|"), t1, "the draft survived");
  await ctx.close();
});

await kit.test("a folder: only Markdown is sent, the rest is listed by name and size", async () => {
  const dir = join(kit.tmp, "salt-folder");
  const w = (rel, text) => { const f = join(dir, rel); mkdirSync(join(f, ".."), { recursive: true }); writeFileSync(f, text); };
  w("01-start.md", "# The Start\n\n" + "tide came in. ".repeat(40));
  w("part/02-end.md", "# The End\n\n" + "ferry went out. ".repeat(40));
  w("cover.png", "not a png".repeat(40000)); // 360 KB
  w(".git/config", "[core]\n");
  await open();
  const sent = [];
  pg.on("requestfinished", async (r) => { if (r.url().endsWith("/ui/import/stage") && r.method() === "POST") sent.push((await r.sizes()).requestBodySize); });
  await pg.locator("#imp-folder").setInputFiles(dir);
  await pg.waitForURL(/\/import\/d\/imd_/, { timeout: 20000 });
  await pg.locator("#imp-h").waitFor();
  eq((await titles()).join("|"), "The Start|Part|The End", "the Markdown became documents (a subfolder is a part)");
  await pg.locator(".imp-skipped summary").click();
  const sk = await pg.locator(".imp-skipped").innerText();
  ok(/cover\.png/.test(sk) && /not a Markdown file/.test(sk), "the picture is listed with the reason: " + sk);
  ok(/hidden file/.test(await pg.locator("#imp-review").innerText()), "the hidden file is counted");
  await waitFor(() => sent.length > 0, "the request to finish");
  ok(sent.every((n) => n < 60000), `the picture was sent to the server: ${sent} bytes`);
});

await kit.test("joining a chapter hands focus to its Put back, and the Join keeps the book's order", async () => {
  await stage(docxFile("join.docx", ["Alpha", "Beta", "Gamma"], "join"));
  await pg.waitForLoadState("load");
  const r = await keyOpen("Beta");
  await r.locator("select[name=to]").last().selectOption({ label: "Alpha" });
  await r.locator('button[data-act="merge"]').focus();
  await pg.keyboard.press("Enter");
  await idle();
  eq((await titles()).join("|"), "Alpha|Gamma", "Beta joined into Alpha");
  ok(/^Put Beta .*back$/.test(await pg.evaluate(() => document.activeElement && document.activeElement.getAttribute("aria-label"))), "focus after Join is on the Put back of what left");
  await pg.getByRole("button", { name: "Discard this import" }).click();
  await pg.waitForURL(/\/import$/);
});

await kit.test("adding to a project: editing a row, then Import, adds to that project (the review never turns into a new project)", async () => {
  const proj = (await api(main, "POST", "/api/v1/projects", { title: "Host book", kind: "novel" })).json.item;
  await open(pg, `/p/${proj.slug}/import`);
  await pg.check("#imp-f-docx");
  await pg.locator("#imp-docx").setInputFiles(docxFile("into.docx", ["Added One", "Added Two"], "into"));
  await pg.waitForURL(new RegExp(`/p/${proj.slug}/import/d/imd_`), { timeout: 20000 });
  await pg.locator("#imp-h").waitFor();
  await pg.waitForLoadState("load");
  eq(await pg.locator("#imp-h").innerText(), "What would be added", "an addition");
  const r = await keyOpen("Added Two");
  await r.locator('input[data-act="title"]').fill("Added Second");
  await pg.keyboard.press("Tab");
  await idle();
  eq(await pg.locator("#imp-h").innerText(), "What would be added", "still an addition after an edit");
  eq(await pg.locator("#imp-t").count(), 0, "no new project's title field after an edit");
  eq(await pg.getByRole("button", { name: /^Add \d+ documents?$/ }).count(), 1, "the button adds");
  await pg.locator("[data-go]").focus();
  await pg.keyboard.press("Enter");
  await pg.waitForURL(new RegExp(`/p/${proj.slug}/d/doc_`), { timeout: 20000 });
  const d = (await api(main, "GET", `/api/v1/projects/${proj.id}/docs`)).json;
  ok(d.items.some((i) => i.title === "Added Second"), "the renamed chapter was added to the project: " + d.items.map((i) => i.title));
});

// U1: Enter in a field of the review is one change. The browser's own submit used to send the same revision a second time.
await kit.test("Enter in a row's title is one change: one request, the page stays, the title changes, no notice", async () => {
  await stage(docxFile("enter-row.docx", ["Enter One", "Enter Two"], "enter"));
  const url0 = pg.url();
  const posts = [];
  const onReq = (q) => { if (q.method() === "POST" && /\/ui\/import\/d\/[^/]+\/update$/.test(q.url())) posts.push(q.postData() || ""); };
  pg.on("request", onReq);
  const r = await keyOpen("Enter One");
  await r.locator('input[data-act="title"]').fill("Enter Renamed");
  await pg.keyboard.press("Enter");
  await idle();
  pg.off("request", onReq);
  eq(posts.length, 1, "update requests sent for one Enter");
  const revs = posts.map((b) => new URLSearchParams(b).get("rev"));
  eq(new Set(revs).size, revs.length, "no two requests with the same revision: " + revs.join(","));
  eq(pg.url(), url0, "the page stays where it was");
  eq(await pg.locator("#imp-notice").count(), 0, "no notice of a change that was not made");
  const focus = await pg.evaluate(() => (document.activeElement && document.activeElement.id) || "");
  ok(/-title$/.test(focus), "focus stays on the title: " + focus);
  ok((await titles()).includes("Enter Renamed"), "the title changed: " + (await titles()));
  await discardDraft();
});

await kit.test("a double press of a change is sent once, and the review moves once", async () => {
  await stage(docxFile("double.docx", ["Twice One", "Twice Two", "Twice Three"], "twice"));
  const posts = [];
  const onReq = (q) => { if (q.method() === "POST" && /\/ui\/import\/d\/[^/]+\/update$/.test(q.url())) posts.push(q.postData() || ""); };
  pg.on("request", onReq);
  const r = await keyOpen("Twice One");
  await r.locator('button[data-act="down"]').dblclick();
  await idle();
  pg.off("request", onReq);
  eq(posts.length, 1, "update requests sent for two presses");
  eq((await titles()).join("|"), "Twice Two|Twice One|Twice Three", "the row moved down once");
  await discardDraft();
});

await kit.test("Enter in Project title saves the title and imports nothing: no project is made, and the draft keeps the title", async () => {
  await stage(docxFile("enter-proj.docx", ["Plain One", "Plain Two"], "plain"));
  const url0 = pg.url();
  await pg.locator("#imp-t").fill("Typed Title");
  await pg.keyboard.press("Enter");
  await idle();
  eq(pg.url(), url0, "still the review, not a new project's address");
  const list = (await api(main, "GET", "/api/v1/projects")).json.items;
  ok(!list.some((p) => p.title === "Typed Title"), "no project was made from the Enter");
  await pg.reload();
  await pg.locator("#imp-h").waitFor();
  eq(await pg.locator("#imp-t").inputValue(), "Typed Title", "the draft keeps the title after a reload");
  await discardDraft();
});

// U1 3(a): an import into a new project: its result offers Undo, and the undo moves what it added to the trash.
await kit.test("an import as a new project: its result offers Undo, and Undo moves its documents to the trash", async () => {
  await stage(docxFile("undo-new.docx", ["Undo One", "Undo Two"], "undo"));
  const id = draftId();
  await pg.locator("[data-go]").focus();
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/p\/[^/]+\/d\/doc_/, { timeout: 20000 });
  const slug = new URL(pg.url()).pathname.split("/")[2];
  await pg.goto(base() + "/import/d/" + id);
  await pg.locator("#imp-h").waitFor();
  await pg.locator("details.imp-undo-d > summary").first().click();
  await pg.getByRole("button", { name: "Move 2 documents to the trash" }).click();
  await pg.waitForURL(new RegExp(`/p/${slug}/import\\?`), { timeout: 20000 });
  await pg.locator("#imp-undone").waitFor();
  ok(/Moved all 2 documents of that import to the trash\./.test(await pg.locator("#imp-undone").innerText()), "the undo says what it moved");
  const proj = (await api(main, "GET", "/api/v1/projects")).json.items.find((p) => p.slug === slug);
  const d = (await api(main, "GET", `/api/v1/projects/${proj.id}/docs`)).json;
  ok(!d.items.some((i) => /^Undo /.test(i.title)), "the two documents are not in the project: " + d.items.map((i) => i.title));
});

await kit.finish("e2e-import-review");
