#!/usr/bin/env node
// The Pandoc screens in Chromium, against a stand-in Pandoc (scripts/fake-pandoc.mjs): the administrator's page (switch on
// with the checks, a refused path or an old version shown on the field, the test and what it says, the kept path after
// a switch-off), the Compile page offering odt, rtf, latex, rst and mediawiki only while the bridge is on (and the file it
// downloads), the import page offering the Pandoc panel only while it is on (and the import it makes, and the plain
// sentence when the bridge goes off under an open page), who may see any of it, 390 px, the keyboard alone, a lost network
// on the import page and the right-to-left pseudo-locale.
//
//   node scripts/e2e-pandoc.mjs [--only substring] [--shots dir]
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { CANARY, installFakePandoc } from "./fake-pandoc.mjs";

const kit = await createKit("e2e-pandoc");
kit.deadline(6 * 60 * 1000);
const argv = process.argv.slice(2);
kit.only = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
const shots = argv.includes("--shots") ? resolve(argv[argv.indexOf("--shots") + 1]) : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, sleep } = kit;
const base = () => kit.base();
const good = installFakePandoc(join(kit.tmp, "pandoc-good"));
const old = installFakePandoc(join(kit.tmp, "pandoc-old"), { version: "2.14.2" });

const writer = await kit.signedIn({}, "writer@dev.scriobh");
const reader = await kit.signedIn({}, "reader@dev.scriobh");
const adm = await kit.signedIn({}, "admin@dev.scriobh");
const made = await api(writer.ctx, "POST", "/api/v1/projects", { title: "Salt Road", kind: "novel" });
const pid = made.json.item.id, slug = made.json.item.slug;
const first = (await api(writer.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
const g = (await api(writer.ctx, "GET", `/api/v1/docs/${first}`)).json.item;
await api(writer.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, content: "The tide came in over the quay.\n" });

const T = (page, sel) => page.locator(sel).innerText();
const noHScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);
const shot = async (page, name) => { if (shots) await page.screenshot({ path: join(shots, name + ".png"), fullPage: true, caret: "initial" }); };
const adminPage = async (page = adm.page) => { kit.main = page; await page.goto(base() + "/admin/pandoc", { waitUntil: "load" }); };
const save = async (page, { on, path, mem }) => {
  await page.locator("#pd-enabled").setChecked(!!on);
  await page.locator("#pd-path").fill(path);
  if (mem !== undefined) await page.locator("#pd-mem").fill(String(mem));
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.waitForLoadState("load");
};
const setBridge = (on, path) => api(adm.ctx, "PUT", "/api/v1/admin/pandoc", { enabled: on, path });
const formatOptions = async (page) => page.locator("#o-format option").evaluateAll((os) => os.map((o) => o.value));

await kit.test("only an administrator finds the page; the console's tabs name it", async () => {
  kit.allowed.push(/404/);
  for (const who of [writer, reader]) {
    const r = await who.page.goto(base() + "/admin/pandoc", { waitUntil: "load" });
    eq(r.status(), 404, "the Pandoc page for a non-administrator");
    // no form token here, so this is refused by the token check; the Go test posts with a token and gets the 404
    const p = await who.ctx.request.post(base() + "/ui/admin/pandoc/save", { form: { enabled: "1", path: good.bin } });
    ok(p.status() === 404 || p.status() === 403, "saving as a non-administrator: " + p.status());
  }
  await adminPage();
  eq(await adm.page.locator('.adm-tabs a[aria-current="page"]').innerText(), "Pandoc", "the tab is current");
  eq(await adm.page.locator('.adm-tabs a[href="/admin/mail"]').count(), 1, "the other tabs are there");
});

await kit.test("off: the page says so, and Compile and import do not offer Pandoc", async () => {
  await adminPage();
  ok((await T(adm.page, "#pd-state")).startsWith("Switched off"), "the state");
  eq(await adm.page.locator("#pd-restored").count(), 0, "no restore note when no path is kept");
  eq(await adm.page.getByRole("button", { name: "Test", exact: true }).count(), 0, "no test button while off");
  const page = writer.page; kit.main = page;
  await page.goto(base() + `/p/${slug}/compile`, { waitUntil: "load" });
  await page.waitForSelector("#o-format");
  const fmts = await formatOptions(page);
  ok(fmts.includes("docx") && fmts.includes("epub"), "the ordinary formats: " + fmts);
  for (const f of ["odt", "rtf", "latex", "rst", "mediawiki"]) ok(!fmts.includes(f), `${f} is offered while the bridge is off`);
  await page.goto(base() + "/import", { waitUntil: "load" });
  eq(await page.locator("#imp-f-pandoc").count(), 0, "no Pandoc choice on the import page");
  eq(await page.locator('[data-fmt="pandoc"]').count(), 0, "no Pandoc panel");
  await shot(adm.page, "pandoc-off");
});

await kit.test("a path that is not usable is refused on its field, and the typed values stay", async () => {
  kit.allowed.push(/422/);
  await adminPage();
  const page = adm.page;
  await save(page, { on: true, path: "pandoc-relative", mem: 256 });
  await page.waitForSelector("#pd-error");
  eq(await page.locator("#pd-path").getAttribute("aria-invalid"), "true", "the field is marked");
  ok((await page.locator("#pd-path").getAttribute("aria-describedby")).includes("pd-error"), "the error is tied to the field");
  eq(await page.locator("#pd-path").inputValue(), "pandoc-relative", "the typed path stays");
  eq(await page.locator("#pd-mem").inputValue(), "256", "and the memory limit");
  ok(await page.locator("#pd-enabled").isChecked(), "and the switch");
  ok((await T(page, "#pd-state")).startsWith("Switched off"), "nothing changed");
  // too old: the reason is shown
  await save(page, { on: true, path: old.bin });
  await page.waitForSelector("#pd-error");
  ok(/3\.1\.6|version|older/i.test(await T(page, "#pd-error")), "the old version is explained: " + (await T(page, "#pd-error")));
  ok((await T(page, "#pd-state")).startsWith("Switched off"), "still off");
  await shot(page, "pandoc-refused");
});

await kit.test("switching on checks the program, then the page says what it found", async () => {
  await adminPage();
  const page = adm.page;
  await save(page, { on: true, path: good.bin, mem: 512 });
  await page.waitForSelector("#pd-state");
  eq(await T(page, "#pd-state"), "Switched on", "the state");
  ok((await T(page, "#pd-program")).includes("Version 3.2"), "the version: " + (await T(page, "#pd-program")));
  ok((await T(page, "main")).includes("The settings are saved."), "the confirmation");
  ok((await T(page, "main")).includes(good.bin), "the program's path is shown");
  eq(await page.locator("#pd-restored").count(), 0, "no restore note while on");
  await shot(page, "pandoc-on");
});

await kit.test("the test shows both directions, only as sentences", async () => {
  await adminPage();
  const page = adm.page;
  await page.getByRole("button", { name: "Test", exact: true }).click();
  await page.waitForSelector(".pd-result");
  let txt = await T(page, ".pd-result");
  ok(txt.includes("Both directions worked.") && txt.includes("Reading rst into Markdown worked") && txt.includes("Writing rst from Markdown worked") && txt.includes("version 3.2"), txt);
  await shot(page, "pandoc-tested");
  // a program that fails: a short sentence, never the program's own words
  writeFileSync(join(good.dir, "mode"), "fail");
  await page.getByRole("button", { name: "Test", exact: true }).click();
  await page.waitForSelector(".pd-result");
  txt = await T(page, "main");
  ok(txt.includes("The test did not pass.") && txt.includes("did not work:"), txt.slice(0, 400));
  ok(!txt.includes(CANARY), "the program's error output reached the page");
  writeFileSync(join(good.dir, "mode"), "ok");
});

await kit.test("Compile offers the Pandoc formats while it is on, says who makes the file, and downloads it", async () => {
  const page = writer.page; kit.main = page;
  await page.goto(base() + `/p/${slug}/compile`, { waitUntil: "load" });
  await page.waitForSelector("#o-format");
  const fmts = await formatOptions(page);
  for (const f of ["odt", "rtf", "latex", "rst", "mediawiki"]) ok(fmts.includes(f), `${f} is missing: ${fmts}`);
  eq(await page.locator("#o-format-h").count(), 0, "no Pandoc note for an ordinary format");
  await page.selectOption("#o-format", "rst");
  ok((await T(page, "#o-format-h")).includes("made by Pandoc"), "the note names Pandoc");
  const dl = page.waitForEvent("download");
  await page.locator("#cmp-download").click();
  const d = await dl;
  ok(/\.rst$/.test(d.suggestedFilename()), "the file name: " + d.suggestedFilename());
  const body = readFileSync(await d.path(), "utf8");
  ok(body.startsWith("FAKE-PANDOC rst") && body.includes("The tide came in over the quay."), "the file holds the compile text: " + body.slice(0, 120));
  await page.waitForFunction(() => /Made /.test(document.querySelector("#cmp-status").textContent));
  await shot(page, "pandoc-compile");
});

await kit.test("import offers the Pandoc panel while it is on, converts a file, and lands in a project", async () => {
  const page = writer.page; kit.main = page;
  await page.goto(base() + "/import", { waitUntil: "load" });
  await page.waitForSelector("#imp-f-pandoc");
  ok(!(await page.locator('[data-fmt="pandoc"]').isVisible()), "the panel is hidden until chosen");
  await page.locator("#imp-f-pandoc").check();
  ok(await page.locator('[data-fmt="pandoc"]').isVisible(), "the panel opens");
  ok((await T(page, '[data-fmt="pandoc"]')).includes("converted by Pandoc"), "it says who converts");
  const rst = join(kit.tmp, "chapter.rst");
  writeFileSync(rst, "CANARY-IMPORT Title\n===================\n\nSome words.\n");
  await page.locator("#imp-pd").setInputFiles(rst);
  await page.waitForSelector("#imp-h", { timeout: 20000 });
  eq(await page.locator("#imp-pdf").inputValue(), "rst", "the format was read from the file's name");
  ok((await T(page, ".imp-sum")).includes("A document converted by Pandoc"), "the preview names the source: " + (await T(page, ".imp-sum")));
  ok((await T(page, "#imp-result")).includes("Chapter One"), "the converted chapter is listed");
  await shot(page, "pandoc-import");
  await page.locator("[data-go]").click();
  await page.waitForURL(/\/p\/[^/]+\/d\/doc_/, { timeout: 20000 });
  const s2 = new URL(page.url()).pathname.split("/")[2];
  const prj = (await api(writer.ctx, "GET", "/api/v1/projects")).json.items.find((p) => p.slug === s2);
  const order = (await api(writer.ctx, "GET", `/api/v1/projects/${prj.id}/docs`)).json.order;
  const doc = (await api(writer.ctx, "GET", `/api/v1/docs/${order[0]}`)).json.item;
  ok(doc.content.includes("A short test paragraph.") && doc.content.includes("FROM-THE-FILE: CANARY-IMPORT"), "the converted text: " + doc.content.slice(0, 200));
});

await kit.test("the format select follows the file; the keyboard reaches the panel", async () => {
  const page = writer.page; kit.main = page;
  await page.goto(base() + "/import", { waitUntil: "load" });
  await page.waitForSelector("#imp-fmt");
  await page.locator("#imp-f-md").focus();
  // ArrowDown walks the radio group (Markdown, Word, EPUB, PDF, Scrivener, WordPress, Ghost, Pandoc, ...): press it until Pandoc is
  // the chosen one, within a bound, so a format added to the group does not break the check
  for (let i = 0; i < 12 && !(await page.locator("#imp-f-pandoc").isChecked()); i++) await page.keyboard.press("ArrowDown");
  ok(await page.locator("#imp-f-pandoc").isChecked(), "the keyboard reaches the Pandoc radio");
  ok(await page.locator('[data-fmt="pandoc"]').isVisible(), "the Pandoc panel opens from the radio group by keyboard");
  eq(await page.locator("[data-fmt]:visible").count(), 1, "one panel at a time");
  await page.keyboard.press("Tab");
  eq(await page.evaluate(() => document.activeElement.id), "imp-pd", "focus goes to the file field");
  const labels = await page.locator("#imp-pdf option").allInnerTexts();
  eq(labels.length, 10, "ten formats");
});

await kit.test("the bridge going off under an open import page is a plain sentence, and nothing typed is lost", async () => {
  const page = writer.page; kit.main = page;
  kit.allowed.push(/409/);
  await page.goto(base() + "/import", { waitUntil: "load" });
  await page.locator("#imp-f-pandoc").check();
  await page.locator("#imp-pdf").selectOption("typst");
  eq((await setBridge(false, good.bin)).status, 200, "switching off");
  const odt = join(kit.tmp, "book.odt");
  writeFileSync(odt, "not really an odt");
  await page.locator("#imp-pd").setInputFiles(odt);
  await page.waitForFunction(() => /not switched on/i.test(document.querySelector("#imp-status").textContent), null, { timeout: 15000 });
  eq(await page.locator("#imp-status").getAttribute("data-bad") !== null, true, "the status is marked as a problem");
  eq(await page.locator("#imp-pdf").inputValue(), "odt", "the format follows the file");
  eq(await page.locator("#imp-f-pandoc").isChecked(), true, "the choice stays");
  // and the Compile page offers the formats no more
  await page.goto(base() + `/p/${slug}/compile`, { waitUntil: "load" });
  await page.waitForSelector("#o-format");
  ok(!(await formatOptions(page)).includes("odt"), "odt is offered while off");
});

// What a restore does to the settings, written the way the restore step writes it (the page reads this mark).
const haveSqlite = spawnSync("sqlite3", ["--version"]).status === 0;
const restoreMark = () => execFileSync("sqlite3", [join(kit.dataDir, "scriobh.db"), "PRAGMA busy_timeout=5000; UPDATE settings SET value = '0' WHERE key = 'pandoc.enabled'; INSERT INTO settings(key, value, updated_at) VALUES ('pandoc.restore_paused', '1', '2026-01-01T00:00:00Z') ON CONFLICT(key) DO UPDATE SET value = '1';"]);

await kit.test("switched off by hand with the path kept: no restore note, and the path stays in the field", async () => {
  eq((await setBridge(false, good.bin)).status, 200, "switching off");
  await adminPage();
  const page = adm.page;
  ok((await T(page, "#pd-state")).startsWith("Switched off"), "off");
  eq(await page.locator("#pd-restored").count(), 0, "an administrator's own switch-off is not called a restore");
  eq(await page.locator("#pd-path").inputValue(), good.bin, "the path is kept in the field");
  await page.locator("#pd-enabled").check();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.waitForSelector("#pd-state");
  eq(await T(page, "#pd-state"), "Switched on", "switched on again");
});

await kit.test("after a restore: the page says so, saving while off keeps the note, and switching on lifts it", async () => {
  if (!haveSqlite) { console.log("      (skipped: no sqlite3 program to write the restore mark)"); return; }
  restoreMark();
  await adminPage();
  const page = adm.page;
  ok((await T(page, "#pd-state")).startsWith("Switched off"), "off");
  ok((await T(page, "#pd-restored")).includes("restored from a backup"), "the note");
  await shot(page, "pandoc-restored");
  await page.getByRole("button", { name: "Save", exact: true }).click(); // saved while off
  await page.waitForURL(/saved=1/);
  eq(await page.locator("#pd-restored").count(), 1, "saving while off does not lift it");
  await page.locator("#pd-enabled").check();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.waitForSelector("#pd-state");
  eq(await T(page, "#pd-state"), "Switched on", "switched on");
  eq(await page.locator("#pd-restored").count(), 0, "the note is gone");
});

await kit.test("by keyboard alone: tab order, Space on the switch, Enter to save", async () => {
  await adminPage();
  const page = adm.page;
  await page.locator("#pd-enabled").focus();
  await page.keyboard.press("Space");
  ok(!(await page.locator("#pd-enabled").isChecked()), "Space toggles the switch");
  await page.keyboard.press("Tab");
  eq(await page.evaluate(() => document.activeElement.id), "pd-path", "then the path");
  await page.keyboard.press("Tab");
  eq(await page.evaluate(() => document.activeElement.id), "pd-mem", "then the memory limit");
  await page.keyboard.press("Tab");
  eq(await page.evaluate(() => document.activeElement.textContent.trim()), "Save", "then Save");
  const ring = await page.evaluate(() => { const c = getComputedStyle(document.activeElement); return c.outlineStyle !== "none" && parseFloat(c.outlineWidth) > 0 || c.boxShadow !== "none"; });
  ok(ring, "the focus is visible");
  await page.keyboard.press("Enter");
  await page.waitForURL(/saved=1/);
  ok((await T(page, "#pd-state")).startsWith("Switched off"), "saved from the keyboard");
  // back on, for what follows
  eq((await setBridge(true, good.bin)).status, 200, "back on");
});

await kit.test("390 px, light and dark: nothing scrolls sideways and every control is a full target", async () => {
  for (const scheme of ["light", "dark"]) {
    const ph = await kit.signedIn({ viewport: { width: 390, height: 844 }, colorScheme: scheme, hasTouch: true }, "admin@dev.scriobh");
    const page = ph.page; kit.main = page;
    await page.goto(base() + "/admin/pandoc", { waitUntil: "load" });
    ok(await noHScroll(page), `${scheme}: no sideways scroll`);
    for (const sel of ["#pd-path", "#pd-mem", "main button.btn"]) {
      const hgt = await page.locator(sel).first().evaluate((e) => e.getBoundingClientRect().height);
      ok(hgt >= 40, `${scheme}: ${sel} is ${hgt}px high`);
    }
    const lab = await page.locator("label[for=pd-enabled]").evaluate((e) => e.getBoundingClientRect().height);
    ok(lab >= 44, `${scheme}: the switch's row is ${lab}px high`);
    await page.getByRole("button", { name: "Test", exact: true }).click();
    await page.waitForSelector(".pd-result");
    ok(await noHScroll(page), `${scheme}: no sideways scroll with a result`);
    await shot(page, `pandoc-390-${scheme}`);
    await ph.ctx.close();
  }
  kit.main = null;
});

await kit.test("the right-to-left pseudo-locale: the page mirrors, its words come through the catalogue and nothing sticks out", async () => {
  await api(adm.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } });
  await api(writer.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } });
  const page = adm.page; kit.main = page;
  await adminPage();
  eq(await page.evaluate(() => document.documentElement.dir), "rtl", "the page is right to left");
  const text = await T(page, "#adm main, main");
  ok(/[⟦⟧]/.test(text), "the words come through the pseudo-locale");
  ok(await noHScroll(page), "no sideways scroll");
  const overflow = await page.evaluate(() => [...document.querySelectorAll("main *")].filter((e) => e.getBoundingClientRect().right > document.documentElement.clientWidth + 1 || e.getBoundingClientRect().left < -1).length);
  eq(overflow, 0, "nothing sticks out of the window");
  await page.locator('form[action^="/ui/admin/pandoc/test"] button').click();
  await page.waitForSelector(".pd-result");
  eq(await page.evaluate(() => getComputedStyle(document.querySelector(".pd-result")).direction), "rtl", "the result is mirrored too");
  await shot(page, "pandoc-rtl");
  const w = writer.page; kit.main = w;
  await w.goto(base() + "/import", { waitUntil: "load" });
  await w.locator("#imp-f-pandoc").check();
  await shot(w, "pandoc-import-rtl");
  await api(adm.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "en" } });
  await api(writer.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "en" } });
});

await kit.test("a lost network on the import page is said plainly and keeps the choices", async () => {
  const page = writer.page; kit.main = page;
  await page.goto(base() + "/import", { waitUntil: "load" });
  await page.locator("#imp-f-pandoc").check();
  await page.locator("#imp-pdf").selectOption("org");
  kit.allowed.push(/Failed to load resource|ERR_INTERNET_DISCONNECTED/);
  await writer.ctx.setOffline(true);
  const f = join(kit.tmp, "notes.org");
  writeFileSync(f, "* A heading\n");
  await page.locator("#imp-pd").setInputFiles(f);
  await page.waitForFunction(() => /Could not reach the server/.test(document.querySelector("#imp-status").textContent), null, { timeout: 15000 });
  eq(await page.locator("#imp-pdf").inputValue(), "org", "the format choice stays");
  await writer.ctx.setOffline(false);
  await sleep(100);
});

await kit.finish("e2e-pandoc");
