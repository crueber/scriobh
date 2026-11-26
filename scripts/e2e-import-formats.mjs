#!/usr/bin/env node
// End-to-end test of the import page's other formats in Chromium: the chooser, a Scrivener project (zip and .scriv
// folder) with its research notes, pictures and character and place sheets, a WordPress export (the file, and a zip of
// it found by itself in the Markdown panel), a Ghost export with its pictures in a zip, the converter's report, the apply,
// adding to a project, the keyboard alone, 390 px, both themes, and that nothing is fetched from anywhere.
//
//   node scripts/e2e-import-formats.mjs [--only substring] [--shots dir]
import { execFileSync } from "node:child_process";
import { cpSync, mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { repo } from "./harness.mjs";

const kit = await createKit("e2e-import-formats");
kit.deadline(6 * 60 * 1000);
const argv = process.argv.slice(2);
kit.only = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
const shots = argv.includes("--shots") ? resolve(argv[argv.indexOf("--shots") + 1]) : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

// The fixtures are small hand-made exports, tracked in the repository (scripts/fixtures/import-formats).
const fx = join(repo, "scripts", "fixtures", "import-formats");
const scrivDir = join(kit.tmp, "Tiny.scriv");
cpSync(join(fx, "Tiny.scriv"), scrivDir, { recursive: true });
const scrivZip = join(kit.tmp, "Tiny.scriv.zip");
execFileSync("zip", ["-qr", scrivZip, "Tiny.scriv"], { cwd: kit.tmp });
const wxr = join(fx, "harbour-notes.wordpress.xml");
const wxrZip = join(kit.tmp, "harbour-notes.zip");
execFileSync("zip", ["-qj", wxrZip, wxr]);
const ghostDir = join(kit.tmp, "ghost");
cpSync(join(fx, "ghost"), ghostDir, { recursive: true });
const ghostZip = join(kit.tmp, "lighthouse-ghost.zip");
execFileSync("zip", ["-qr", ghostZip, "."], { cwd: ghostDir });
const ghostJSON = join(ghostDir, "lighthouse.ghost.2024-03-01-10-00-00.json");

// Nothing on this page may reach anywhere but the server: the watcher in the kit fails a step on any other origin.
const projects = async () => (await api(main, "GET", "/api/v1/projects")).json.items;
const docs = async (pid) => (await api(main, "GET", `/api/v1/projects/${pid}/docs`)).json;
const open = async (path = "/import") => { await pg.goto(base() + path); await pg.waitForSelector("#imp-fmt"); };
const status = () => pg.locator("#imp-status").innerText();
// A change is a form whose answer is swapped into the review: it has settled when htmx has nothing in flight.
const settle = async () => { await sleep(200); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"), null, { timeout: 20000 }); await sleep(100); };
const treeTitles = () => pg.locator("#imp-review > .imp-tree > .imp-node > .imp-row .imp-title").allInnerTexts();
// A tall page is shot by growing the viewport to it.
const shot = async (name, page = pg) => {
  if (!shots) return;
  const v = page.viewportSize(), hgt = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: v.width, height: Math.min(hgt, 4000) });
  await page.screenshot({ path: join(shots, name + ".png"), caret: "initial" }); // a hidden caret leaves an empty style attribute on each field
  await page.setViewportSize(v);
};
const applyAndLand = async (title) => {
  if (title) await pg.locator("#imp-t").fill(title);
  await pg.locator("[data-go]").click();
  await pg.waitForURL(/\/p\/[^/]+\/d\/doc_/, { timeout: 20000 });
  const slug = new URL(pg.url()).pathname.split("/")[2];
  return (await projects()).find((p) => p.slug === slug);
};

await kit.test("chooser: each format shows its own words and file field, with the keyboard alone", async () => {
  await open();
  ok(await pg.locator('[data-fmt="md"]').isVisible(), "Markdown is the panel shown first");
  ok(!(await pg.locator('[data-fmt="scrivener"]').isVisible()), "the Scrivener panel is hidden at first");
  // Keyboard: focus the first choice, arrow down through them; each shows its panel.
  await pg.locator("#imp-f-md").focus();
  for (const [fmt, words] of [["docx", /headings, chapter styles/], ["epub", /table of contents/i], ["pdf", /separate process/], ["text", /UTF-8 text/], ["scrivener", /\.scriv/], ["wordpress", /Tools, then Export/], ["ghost", /Ghost Admin/]]) {
    await pg.keyboard.press("ArrowDown");
    const panel = pg.locator(`[data-fmt="${fmt}"]`);
    ok(await panel.isVisible(), `${fmt}: its panel did not open from the keyboard`);
    ok(words.test(await panel.innerText()), `${fmt}: the panel does not say where the file comes from`);
    eq(await pg.locator("[data-fmt]:visible").count(), 1, `${fmt}: one panel at a time`);
  }
  await shot("chooser-ghost-1280");
});

await kit.test("scrivener: a zipped project is previewed with its research, sheets and report, and created", async () => {
  await open();
  await pg.check("#imp-f-scrivener");
  await pg.locator("#imp-scriv").setInputFiles(scrivZip);
  await pg.locator("#imp-h").waitFor({ timeout: 20000 });
  await pg.waitForLoadState("load"); // htmx is in place: a change made before it is would not post
  await settle();
  const sum = await pg.locator(".imp-sum").innerText();
  ok(/A Scrivener project/.test(sum), "the summary names the source: " + sum);
  eq(await pg.locator("#imp-t").inputValue(), "The Tiny Book", "the title comes from the project");
  const t = await treeTitles();
  ok(t.includes("Arrival") && t.includes("Departure"), "the Draft is in the tree: " + t);
  // The research notes, with the sheets marked.
  const res = pg.locator(".imp-more details", { hasText: "research note" });
  if (!(await res.evaluate((d) => d.open))) { // a short list of notes is open already; a long one is opened with the keyboard
    await res.locator("summary").focus();
    await pg.keyboard.press("Enter");
  }
  const rtext = await res.innerText();
  ok(/Mara/.test(rtext) && /character sheet/.test(rtext) && /Saltmouth/.test(rtext) && /place sheet/.test(rtext), "the sheets are marked: " + rtext);
  // The report of what was read and not kept.
  const rep = pg.locator(".imp-more details", { hasText: "did not keep" });
  await rep.locator("summary").click();
  ok(/Trashed|Labels|Keywords/i.test(await rep.innerText()), "the report lists what was not kept: " + (await rep.innerText()));
  ok(/Pictures\s*2/.test(await rep.innerText()), "the report counts the two pictures: " + (await rep.innerText()));
  ok(/2 picture\(s\) will be imported/.test(await pg.locator("#imp-review").innerText()), "the preview says the pictures will be imported");
  // The sheets become entities when asked: the preview says so.
  await pg.selectOption("#imp-ss", "entities");
  await settle();
  ok(/will be made entities/.test(await pg.locator("#imp-review").innerText()), "the preview does not say the sheets become entities");
  // Leave a research note out from the keyboard, then put it back.
  await pg.getByRole("button", { name: "Leave out Harbour lore" }).focus();
  await pg.keyboard.press("Enter");
  await settle();
  ok(!/Harbour lore/.test(await pg.locator(".imp-more").innerText()), "the note left out is still listed");
  await pg.getByRole("button", { name: "Put Harbour lore back" }).click();
  await settle();
  ok(/Harbour lore/.test(await pg.locator(".imp-more").innerText()), "Put back did not bring the note back");
  await shot("scrivener-preview-1280");
  const made = await applyAndLand("The Tiny Book");
  ok(made, "the project was not created");
  const order = (await docs(made.id)).items.map((d) => d.title);
  ok(order.includes("Arrival") && order.includes("Departure"), "the chapters were not created: " + order);
  const notes = (await api(main, "GET", `/api/v1/projects/${made.id}/research`)).json;
  ok(JSON.stringify(notes).includes("Harbour lore"), "the research notes were not written");
  // The picture in the text is an attachment of the project, served cleaned.
  const d = await docs(made.id);
  const arrival = (await api(main, "GET", `/api/v1/docs/${d.items.find((i) => i.title === "Arrival").id}`)).json.item.content;
  const m = arrival.match(/\]\((\/files\/att_[0-9A-Z]+)\)/);
  ok(m, "the picture in the text is not linked to an attachment: " + arrival);
  const img = await main.request.get(base() + m[1]);
  eq(img.status(), 200, "the picture is served");
  ok(/image\/jpeg/.test(img.headers()["content-type"]), "the picture's type: " + img.headers()["content-type"]);
  ok(!(await img.body()).includes("GPS-SECRET"), "the stored picture still says where it was taken");
  // The sheets are entities of the new project's Compendium.
  const cmp = (await api(main, "GET", "/api/v1/compendiums")).json.items.find((c) => c.name === "The Tiny Book Compendium");
  ok(cmp, "no Compendium was made for the project");
  const ents = (await api(main, "GET", `/api/v1/compendiums/${cmp.id}/entities`)).json.items.map((x) => x.name + ":" + x.kind).sort();
  eq(ents.join(","), "Mara:character,Saltmouth:place", "the sheets became entities");
});

await kit.test("scrivener: the .scriv folder itself can be chosen", async () => {
  await open();
  await pg.check("#imp-f-scrivener");
  await pg.locator("#imp-scrivdir").setInputFiles(scrivDir);
  await pg.locator("#imp-h").waitFor({ timeout: 20000 });
  await pg.waitForLoadState("load"); // htmx is in place: a change made before it is would not post
  ok(/A Scrivener project/.test(await pg.locator(".imp-sum").innerText()), "the folder was not read as a Scrivener project");
  ok((await treeTitles()).includes("Arrival"), "the folder's Draft is not in the tree");
});

await kit.test("wordpress: the export file is previewed, an option re-previews, and a blog is created", async () => {
  await open();
  await pg.check("#imp-f-wordpress");
  await pg.locator("#imp-wp").setInputFiles(wxr);
  await pg.locator("#imp-h").waitFor({ timeout: 20000 });
  await pg.waitForLoadState("load"); // htmx is in place: a change made before it is would not post
  ok(/A WordPress export/.test(await pg.locator(".imp-sum").innerText()), "the summary names WordPress");
  const before = (await treeTitles()).length;
  await pg.selectOption("#imp-wps", "all");
  await settle();
  ok((await treeTitles()).length > before, "drafts were not added when asked for");
  await pg.selectOption("#imp-wps", "published");
  await settle();
  const rep = pg.locator(".imp-more details", { hasText: "did not keep" });
  await rep.locator("summary").click();
  ok(/Comments|Attachment/i.test(await rep.innerText()), "the report lists comments or attachments: " + (await rep.innerText()));
  const made = await applyAndLand("Harbour Notes");
  eq(made.kind, "blog", "a WordPress export is a blog");
  // The picture named by an address on the site was not fetched (the watcher fails any other origin from the page; the
  // server's side is TestExportPicturesAreNeverFetched): it is a mark in the text.
  const d = await docs(made.id);
  const low = (await api(main, "GET", `/api/v1/docs/${d.items.find((i) => i.title === "Low Tide").id}`)).json.item.content;
  ok(/\\\[image: A boat on the mud\\\]/.test(low) && !low.includes("/files/"), "the picture by address: " + low);
});

await kit.test("detection: a zip of a WordPress export in the Markdown panel is read as WordPress", async () => {
  await open();
  await pg.locator("#imp-zip").setInputFiles(wxrZip);
  await pg.locator("#imp-h").waitFor({ timeout: 20000 });
  await pg.waitForLoadState("load"); // htmx is in place: a change made before it is would not post
  ok(/A WordPress export/.test(await pg.locator(".imp-sum").innerText()), "the zip was not recognised: " + (await pg.locator(".imp-sum").innerText()));
});

await kit.test("ghost: a zip with the images folder brings the pictures, once", async () => {
  await open();
  await pg.check("#imp-f-ghost");
  await pg.locator("#imp-gh").setInputFiles(ghostZip);
  await pg.locator("#imp-h").waitFor({ timeout: 20000 });
  await pg.waitForLoadState("load"); // htmx is in place: a change made before it is would not post
  ok(/1 picture\(s\) will be imported/.test(await pg.locator("#imp-review").innerText()), "the preview does not count the picture");
  const made = await applyAndLand("Lighthouse");
  const files = (await api(main, "GET", `/api/v1/projects/${made.id}/attachments`)).json.items;
  eq(files.length, 1, "the picture and its resized copy are one attachment");
});

await kit.test("ghost: the export file is previewed and added to a project that exists", async () => {
  const host = (await api(main, "POST", "/api/v1/projects", { title: "Lighthouse Blog", kind: "blog" })).json.item;
  await api(main, "POST", `/api/v1/projects/${host.id}/docs`, { title: "Already here", kind: "chapter", content: "Old words.\n" });
  await open(`/p/${host.slug}/import`);
  await pg.check("#imp-f-ghost");
  await pg.locator("#imp-gh").setInputFiles(ghostJSON);
  await pg.locator("#imp-h").waitFor({ timeout: 20000 });
  await pg.waitForLoadState("load"); // htmx is in place: a change made before it is would not post
  ok(/A Ghost export/.test(await pg.locator(".imp-sum").innerText()), "the summary names Ghost");
  ok(/Add \d+ documents?/.test(await pg.locator("[data-go]").innerText()), "the button adds");
  await pg.locator("[data-go]").click();
  // The import page is itself /p/lighthouse-blog/import, so that prefix is already the URL before anything is applied:
  // the page this lands on is the one that is not the import page.
  await pg.waitForURL((u) => /^\/p\/lighthouse-blog\/d\/doc_/.test(u.pathname), { timeout: 20000 });
  const d = await docs(host.id);
  const titles = d.order.map((id) => d.items.find((i) => i.id === id).title);
  ok(titles.includes("The Lamp") && titles.indexOf("Already here") < titles.indexOf("The Lamp"), "the posts were not added after what was there: " + titles);
});

await kit.test("phone: 390 px in both themes, no sideways scroll, the preview readable", async () => {
  for (const scheme of ["light", "dark"]) {
    const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, colorScheme: scheme, storageState: await main.storageState() });
    const page = await ctx.newPage();
    kit.watch(page, "phone-" + scheme);
    await page.goto(base() + "/import");
    for (const fmt of ["wordpress", "ghost", "docx", "scrivener"]) {
      await page.check(`#imp-f-${fmt}`);
      const wide = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      ok(wide <= 0, `${scheme} ${fmt}: the chooser scrolls sideways by ${wide} px`);
      await shot(`chooser-${fmt}-390-${scheme}`, page);
    }
    await page.locator("#imp-scriv").setInputFiles(scrivZip);
    await page.locator("#imp-h").waitFor({ timeout: 20000 });
    await page.locator(".imp-more details summary").first().click();
    await sleep(200);
    const wide = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    ok(wide <= 0, `${scheme}: the page scrolls sideways by ${wide} px`);
    await shot(`scrivener-preview-390-${scheme}`, page);
    await kit.csp(page, "phone-" + scheme);
    await ctx.close();
  }
});

await kit.finish("e2e-import-formats");
