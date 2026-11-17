#!/usr/bin/env node
// End-to-end test of the Editions pages in Chromium: the empty state, the list of imported books, one edition with its
// chapters, the download of the original file (the bytes it was imported as), a file that is no longer on the server, what
// each role sees (an owner, an editor, a reader), 390 px, the right-to-left pseudo-locale and a reload with the network cut.
//
//   node scripts/e2e-editions.mjs [--only substring] [--shots dir]
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { makeEpub } from "./fixtures/books.mjs";

const kit = await createKit("e2e-editions");
kit.deadline(5 * 60 * 1000);
const argv = process.argv.slice(2);
kit.only = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
const shots = argv.includes("--shots") ? resolve(argv[argv.indexOf("--shots") + 1]) : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const owner = await kit.signedIn({}, "writer@dev.scriobh");   // Wren, owner
const editor = await kit.signedIn({}, "admin@dev.scriobh");   // Ada, an editor of the book (and an administrator of the server)
const reader = await kit.signedIn({}, "reader@dev.scriobh");  // Rory, a reader
kit.main = owner.page;
const pg = owner.page;
const shot = async (name, page = pg) => {
  if (!shots) return;
  const v = page.viewportSize(), hgt = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: v.width, height: Math.min(hgt, 4000) });
  await page.screenshot({ path: join(shots, name + ".png"), caret: "initial" });
  await page.setViewportSize(v);
};

const bookFile = makeEpub(join(kit.tmp, "marsh.epub"), "ok", kit.tmp);
const bytes = readFileSync(bookFile), b64 = bytes.toString("base64"), sha = createHash("sha256").update(bytes).digest("hex");
const empty = (await api(owner.ctx, "POST", "/api/v1/projects", { title: "No Books", kind: "novel" })).json.item;
const mk = (await api(owner.ctx, "POST", "/api/v1/projects", { title: "Two Editions", kind: "novel" })).json.item;
const importInto = async (label) => {
  const pv = await api(owner.ctx, "POST", `/api/v1/projects/${mk.id}/import/preview`, { name: "marsh", epub: b64 });
  ok(pv.status === 200, "preview " + JSON.stringify(pv.json));
  const ap = await api(owner.ctx, "POST", `/api/v1/projects/${mk.id}/import`, { name: "marsh", epub: b64, token: pv.json.token, edition_label: label });
  ok(ap.status === 201, "apply " + JSON.stringify(ap.json));
  return ap.json.edition_id;
};
const e1 = await importInto("First printing");
const e2 = await importInto("Second printing");
for (const [who, role] of [["admin@dev.scriobh", "editor"], ["reader@dev.scriobh", "reader"]]) ok((await api(owner.ctx, "POST", `/api/v1/projects/${mk.id}/members`, { email: who, role })).status < 300, "member " + role);
const list = `/p/${mk.slug}/editions`;

await kit.test("empty: a project with no imported book says so and points to adding one", async () => {
  await pg.goto(`${base()}/p/${empty.slug}/editions`);
  const t = await pg.locator("main").innerText();
  ok(/No imported books yet/.test(t) && /EPUB book into this project/.test(t), "the empty state: " + t);
  await pg.getByRole("link", { name: "Add a book to this project" }).click();
  await pg.waitForURL(/\/import$/);
});

await kit.test("the list: each edition with its label, kind, file name, date, size, chapters and a short checksum; Open and Download", async () => {
  await pg.goto(base() + list);
  eq(await pg.locator(".edn-item").count(), 2, "two editions");
  const t = await pg.locator("main").innerText();
  for (const w of [/First printing/, /Second printing/, /EPUB book, from the file marsh/, /Imported/, /Size/, /Checksum \(SHA-256, start\)/, new RegExp(sha.slice(0, 12)), /Chapters\s*\n?\s*6/, /A project keeps at most 50 editions, and a book can be up to 32 MB. This project has 2/, /never pushed to Git/]) ok(w.test(t), `the list does not say ${w}:\n${t}`);
  eq(await pg.getByRole("link", { name: /Download the original file of/ }).count(), 2, "download links");
  eq(await pg.locator("h1").innerText(), "Editions", "the heading");
  await shot("editions-list-1280");
});

await kit.test("what each role sees: the owner and the administrator-editor see who imported; a reader reads the list and cannot download", async () => {
  const text = async (p) => { await p.goto(base() + list); return p.locator("main").innerText(); };
  ok(/Imported by\s*\n?\s*Wren Writer/.test(await text(owner.page)), "the owner is not told who imported");
  const ed = await text(editor.page);
  ok(/Imported by/.test(ed), "an administrator is not told who imported");
  eq(await editor.page.getByRole("link", { name: /Download the original file of/ }).count(), 2, "an editor cannot download");
  const rd = await text(reader.page);
  ok(/First printing/.test(rd) && /Only owners and editors can download/.test(rd), "the reader's list: " + rd);
  ok(!/Imported by/.test(rd), "a reader is told who imported");
  eq(await reader.page.getByRole("link", { name: /Download/ }).count(), 0, "a reader is offered the download");
  eq((await api(reader.ctx, "GET", `/api/v1/projects/${mk.id}/editions/${e1}/download`)).status, 403, "the server lets a reader download");
  // the project menu and palette are for owners and editors
  await owner.page.goto(`${base()}/p/${mk.slug}/goals`);
  await owner.page.getByRole("link", { name: "Editions" }).first().waitFor({ state: "attached" });
});

await kit.test("one edition: its chapters as the book was read, nested, each with where it was and what it became", async () => {
  await pg.goto(base() + list);
  await pg.getByRole("link", { name: "First printing", exact: true }).click();
  await pg.waitForURL(new RegExp(`/editions/${e1}$`));
  eq(await pg.locator("h1").innerText(), "First printing", "the heading");
  const t = await pg.locator("main").innerText();
  ok(new RegExp(sha).test(t) && /urn:isbn:9780000000099/.test(t), "the full checksum and the book's identifier");
  const road = pg.locator('.edn-ch:has(.edn-title:text-is("The Road"))');
  eq(await road.getAttribute("data-depth"), "1", "a chapter is nested under its part");
  ok(/Chapter, \d+ words/.test(await road.innerText()) && /Where it was in the book: ch01\.xhtml/.test(await road.innerText()), "the chapter's line: " + (await road.innerText()));
  await road.getByRole("link", { name: "Open the document it became" }).click();
  await pg.waitForURL(/\/p\/[^/]+\/d\/doc_/);
});

await kit.test("download: the file is the book as it was imported, byte for byte", async () => {
  await pg.goto(`${base()}/p/${mk.slug}/editions/${e1}`);
  const [dl] = await Promise.all([pg.waitForEvent("download"), pg.getByRole("link", { name: "Download the original file" }).click()]);
  const path = await dl.path();
  eq(createHash("sha256").update(readFileSync(path)).digest("hex"), sha, "the downloaded bytes");
  ok(/First printing\.epub$/.test(dl.suggestedFilename()), "the file's name: " + dl.suggestedFilename());
});

await kit.test("the file is gone from the server: no link that would save a page, the sentence in its place, the record and the chapters still there", async () => {
  const dir = join(kit.dataDir, "files", "projects", mk.id, "editions");
  const edition = `${base()}/p/${mk.slug}/editions/${e1}`;
  await pg.goto(edition);
  const link = pg.getByRole("link", { name: "Download the original file" });
  eq(await link.count(), 1, "the link is there while the file is");
  // A page drawn before the file went still has its link. The link carries the download attribute, so what answers it is
  // saved: it must be a failed download that says why, never the edition page saved under the book's name.
  const kept = readdirSync(dir).map((f) => [f, readFileSync(join(dir, f))]); // put back at the end: the steps after this one still need the link
  for (const f of readdirSync(dir)) rmSync(join(dir, f));
  const saved = pg.waitForEvent("download", { timeout: 4000 }).catch(() => null);
  await link.click();
  const d = await saved;
  if (d) {
    ok(await d.failure(), "a link on a stale page saved a file: " + d.suggestedFilename() + " (a redirect to the page was saved as the book)");
  }
  eq(pg.url(), edition, "the page did not move");
  await pg.reload();
  eq(await link.count(), 0, "a link to a file that is not there");
  ok(/not on this server/.test(await pg.locator("#edn-file").locator("..").innerText()), "the sentence");
  ok(/The Road/.test(await pg.locator(".edn-chapters").innerText()), "the chapters are gone with the file");
  await pg.goto(`${base()}/p/${mk.slug}/editions`);
  eq(await pg.getByRole("link", { name: /Download the original file of/ }).count(), 0, "the list still links to it");
  ok(/not on this server/.test(await pg.locator("main").innerText()), "the list says it");
  // The file comes back (a restore, a copy put back by hand): the link comes back with it, so the steps after this still see a link.
  for (const [f, bytes] of kept) writeFileSync(join(dir, f), bytes);
  await pg.goto(edition);
  eq(await pg.getByRole("link", { name: "Download the original file" }).count(), 1, "the link did not come back with the file");
});

await kit.test("390 px: the list and one edition fit, with targets of 44 px", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 800 }, hasTouch: true, isMobile: true }, "writer@dev.scriobh");
  for (const path of [list, `/p/${mk.slug}/editions/${e2}`]) {
    await ph.page.goto(base() + path);
    const m = await ph.page.evaluate(() => {
      const small = [];
      for (const e of document.querySelectorAll("main a.btn, main .edn-name a")) { const r = e.getBoundingClientRect(); if (r.height < 43.5) small.push(`${e.textContent.slice(0, 30)} ${Math.round(r.width)}x${Math.round(r.height)}`); }
      return { over: document.documentElement.scrollWidth > document.documentElement.clientWidth, small };
    });
    eq(m.over, false, `${path} scrolls sideways at 390 px`);
    eq(m.small.join("; "), "", `${path} has targets under 44 px`);
  }
  await shot("editions-390", ph.page);
  await ph.ctx.close();
});

await kit.test("the right-to-left pseudo-locale: the list and an edition mirror and nothing overflows", async () => {
  eq((await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } })).status, 200, "set the pseudo-locale");
  try {
    for (const path of [list, `/p/${mk.slug}/editions/${e2}`]) {
      await pg.goto(base() + path);
      eq(await pg.locator("html").getAttribute("dir"), "rtl", `${path}: right to left`);
      eq((await pg.locator("main p, main dt, main h2, main a.btn").evaluateAll((l) => l.filter((e) => e.textContent.trim() && !e.textContent.includes("⟦") && !/^[\d .,:KMGB-]+$/.test(e.textContent.trim()) && !e.closest(".edn-name, .edn-title")).map((e) => e.textContent.slice(0, 40)))).join(" | "), "", `${path}: words that did not come from the catalogue`);
      eq(await pg.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, `${path}: scrolls sideways`);
    }
    await shot("edition-rtl-1280");
  } finally { await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "en" } }); }
});

await kit.test("with the network cut, a reload shows the offline notice and not a browser error page", async () => {
  const ctx = await kit.newContext({ serviceWorkers: "allow", viewport: { width: 1000, height: 700 } });
  await ctx.addCookies(await owner.ctx.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "offline");
  await p.goto(base() + list);
  await p.waitForFunction(async () => (await navigator.serviceWorker.getRegistration()) && navigator.serviceWorker.controller !== null || (await navigator.serviceWorker.ready, true), null, { timeout: 15000 }).catch(() => {});
  await p.reload();
  kit.allowed.push(/Failed to load resource|ERR_INTERNET_DISCONNECTED|net::/);
  await ctx.setOffline(true);
  try {
    await p.reload();
    ok(/cannot reach the server/.test(await p.locator("body").innerText()), "the offline notice");
  } finally { await ctx.setOffline(false); await ctx.close(); }
});

await kit.finish("e2e-editions");
