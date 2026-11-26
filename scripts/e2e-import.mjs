#!/usr/bin/env node
// End-to-end test of the import page (a folder and a zip, preview, edits, apply, adding to a project),
// the mirror line and Rebuild in the project menu, and the administrators' Backups section, in Chromium.
//
//   node scripts/e2e-import.mjs [--only substring]
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-import");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const words = (n, seed = "tide") => (`${seed} came in over the grey stones and the gulls wheeled `.repeat(Math.ceil(n / 9))).split(" ").slice(0, n).join(" ");
const write = (dir, rel, text) => { const p = join(dir, rel); mkdirSync(join(p, ".."), { recursive: true }); writeFileSync(p, text); };

// A small book on disk: three chapters (one with ## headings), back matter by name, a folder, a hidden folder and an image.
const book = join(kit.tmp, "salt-road");
write(book, "01-the-start.md", `# The Start\n\n${words(120)}\n\n## First light\n\n${words(60, "dawn")}\n\n## Second light\n\n${words(40, "noon")}\n`);
write(book, "02-the-middle.md", `# The Middle\n\n${words(200, "reef")}\n`);
write(book, "appendix.md", `# Notes on tides\n\n${words(30, "moon")}\n`);
write(book, "part-two/01-crossing.md", `# The Crossing\n\n${words(80, "ferry")}\n`);
write(book, "part-two/02-landfall.md", `# Landfall\n\n${words(70, "shore")}\n`);
write(book, ".git/config", "[core]\n");
write(book, "cover.png", "not really a png");

const longTasks = () => pg.evaluate(() => window.__long || []);
const watchLong = () => pg.addInitScript(() => {
  window.__long = [];
  try { new PerformanceObserver((l) => l.getEntries().forEach((e) => window.__long.push(Math.round(e.duration)))).observe({ entryTypes: ["longtask"] }); } catch (e) { /* not supported */ }
});
await watchLong();

const docs = async (pid) => (await api(main, "GET", `/api/v1/projects/${pid}/docs`)).json;
const open = async (path = "/import") => { await pg.goto(base() + path); await pg.waitForSelector("#imp-folder"); };
const status = () => pg.locator("#imp-status").innerText();
const treeTitles = () => pg.locator("#imp-review .imp-node > .imp-row .imp-title").allInnerTexts();
const nodeFor = (title) => pg.locator(`#imp-review .imp-node:has(> .imp-row > .imp-title:text-is("${title}"))`).first();
const openEdit = async (title) => {
  const n = nodeFor(title);
  if (!(await n.locator("details.imp-edit").first().evaluate((d) => d.open))) {
    await n.locator("summary").first().focus();
    await pg.keyboard.press("Enter"); // keyboard only: the summary toggles on Enter
  }
  ok(await n.locator("details.imp-edit").first().evaluate((d) => d.open), "the Edit panel did not open");
  await n.locator(".imp-edit-body form").first().waitFor();
  await settle(); // the panel was just swapped in: htmx marks it added for a moment
  return n;
};
// A change is a form whose answer is swapped into the review: it has settled when htmx has nothing in flight.
const settle = async () => { await sleep(200); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"), null, { timeout: 20000 }); await sleep(100); };

await kit.test("import: a folder is previewed as a tree, edited with the keyboard, and created", async () => {
  await open();
  await pg.locator("#imp-folder").setInputFiles(book);
  await pg.locator("#imp-h").waitFor();
  const sum = await pg.locator(".imp-sum").innerText();
  ok(/6 documents|5 documents|7 documents/.test(sum) && /words/.test(sum), "the summary: " + sum);
  eq(await pg.locator("#imp-t").inputValue(), "Salt Road", "the project title starts as the folder's name");
  // What was left out, and why; hidden files are counted, not listed.
  await pg.locator(".imp-skipped summary").click();
  const skipped = await pg.locator(".imp-skipped").innerText();
  ok(/cover\.png/.test(skipped) && /not a Markdown file/.test(skipped), "skipped files: " + skipped);
  ok(/hidden file/.test(await pg.locator(".imp-note", { hasText: "hidden file" }).first().innerText()), "the hidden-files note is missing");
  const t0 = await treeTitles();
  ok(t0.includes("The Start") && t0.includes("The Crossing"), "the tree: " + t0);
  // Rename.
  let n = await openEdit("The Start");
  const title = n.locator('input[data-act="title"]').first();
  await title.fill("Opening");
  await pg.keyboard.press("Tab"); // change fires on leaving the field
  await settle();
  ok((await treeTitles()).includes("Opening"), "the rename is not in the preview");
  // Retype: the appendix is back matter; make it a note.
  n = await openEdit("Notes on tides");
  await n.locator('select[data-act="kind"]').first().selectOption("note");
  await settle();
  ok(/Note/.test(await nodeFor("Notes on tides").locator(".imp-meta").first().innerText()), "the kind change is not in the preview");
  // Split the file with headings: two scenes appear under it.
  n = await openEdit("Opening");
  await n.locator('input[data-act="split"]').first().check();
  await settle();
  const t1 = await treeTitles();
  ok(t1.includes("First light") && t1.includes("Second light"), "no scenes after splitting: " + t1);
  // Reorder: the middle chapter moves above the opening.
  n = await openEdit("The Middle");
  await n.locator('button[data-act="up"]').first().click();
  await settle();
  const t2 = await treeTitles();
  ok(t2.indexOf("The Middle") < t2.indexOf("Opening"), "The Middle did not move up: " + t2);
  // Leave out, then put back, then leave out again: the server stops listing it, the page remembers it.
  n = await openEdit("Notes on tides");
  await n.locator('button[data-act="skip"]').first().click();
  await settle();
  ok(!(await treeTitles()).includes("Notes on tides"), "the left-out document is still in the tree");
  ok(/Left out of the import \(1\)/.test(await pg.locator(".imp-left").innerText()), "no left-out list");
  await pg.getByRole("button", { name: "Put Notes on tides back" }).click();
  await settle();
  ok((await treeTitles()).includes("Notes on tides"), "Put back did not bring it back");
  n = await openEdit("Notes on tides");
  await n.locator('button[data-act="skip"]').first().click();
  await settle();
  // Apply.
  await pg.locator("#imp-t").fill("The Salt Road");
  const go = pg.locator("[data-go]");
  ok(/Import \d+ documents?/.test(await go.innerText()), "the import button: " + (await go.innerText()));
  await go.click();
  await pg.waitForURL(/\/p\/the-salt-road(-\d+)?\/d\/doc_/, { timeout: 20000 });
  const made = (await api(main, "GET", "/api/v1/projects")).json.items.find((p) => p.title === "The Salt Road");
  ok(made, "the project was not created");
  const d = await docs(made.id);
  const byId = Object.fromEntries(d.items.map((i) => [i.id, i]));
  const order = d.order.map((id) => byId[id].title);
  ok(order.indexOf("The Middle") < order.indexOf("Opening"), "the reorder was not applied: " + order);
  ok(!order.includes("Notes on tides"), "the left-out document was created");
  ok(order.includes("First light") && order.includes("Second light"), "the scenes were not created: " + order);
  const kinds = Object.fromEntries(d.items.map((i) => [i.title, i.kind]));
  eq(kinds["First light"], "scene", "a split heading becomes a scene");
  ok(d.items.some((i) => i.kind === "part"), "the folder did not become a part");
  // The tree on the page the person lands on shows what was made.
  await pg.waitForSelector(".tree-row");
  ok((await pg.locator(".tree-row").count()) >= 5, "the writing view shows too few documents");
});

await kit.test("import: a zip is read, previewed and created", async () => {
  const zip = join(kit.tmp, "salt-road.zip");
  execFileSync("zip", ["-qr", zip, "salt-road", "-x", "salt-road/.git/*"], { cwd: kit.tmp });
  await open();
  await pg.locator("#imp-zip").setInputFiles(zip);
  await pg.locator("#imp-h").waitFor();
  ok(/documents/.test(await pg.locator(".imp-sum").innerText()), "no summary for a zip");
  eq(await pg.locator("#imp-t").inputValue(), "Salt Road", "the project title starts as the zip's name without .zip, made readable");
  await pg.locator("#imp-t").fill("From a zip");
  await pg.locator("[data-go]").click();
  await pg.waitForURL(/\/p\/from-a-zip(-\d+)?\/d\/doc_/, { timeout: 20000 });
  const made = (await api(main, "GET", "/api/v1/projects")).json.items.find((p) => p.title === "From a zip");
  ok(made && (await docs(made.id)).items.length >= 5, "the zip import made too few documents");
});

await kit.test("import: a 25 MB zip does not freeze the page and the preview answers", async () => {
  const big = join(kit.tmp, "big");
  const chunk = words(1_900_000 / 6, "alpha beta gamma").slice(0, 1_900_000);
  for (let i = 1; i <= 13; i++) write(big, `${String(i).padStart(2, "0")}-chapter.md`, `# Chapter ${i}\n\n${chunk.replace(/alpha/g, "alpha" + i)}\n`);
  const zip = join(kit.tmp, "big.zip");
  execFileSync("zip", ["-q0r", zip, "big"], { cwd: kit.tmp });
  await open();
  await pg.evaluate(() => { window.__long.length = 0; });
  const t0 = Date.now();
  await pg.locator("#imp-zip").setInputFiles(zip);
  await pg.locator("#imp-h").waitFor({ timeout: 90000 });
  const ms = Date.now() - t0, tasks = await longTasks();
  const worst = Math.max(0, ...tasks);
  console.log(`      25 MB zip: preview shown after ${ms} ms; main-thread tasks over 50 ms: ${tasks.length}, longest ${worst} ms`);
  ok(/13 documents/.test(await pg.locator(".imp-sum").innerText()), "the big zip preview: " + (await pg.locator(".imp-sum").innerText()));
  ok(worst < 700, `the page froze for ${worst} ms while reading the zip`);
});

await kit.test("import: plain-words errors for a zip that is too big, not a zip, and a folder with nothing to import", async () => {
  kit.allowed = [/status of 422/, /status of 413/];
  await open();
  const huge = join(kit.tmp, "huge.zip");
  writeFileSync(huge, Buffer.concat([Buffer.from("PK\x03\x04"), Buffer.alloc(35 * 1048576)])); // looks like a zip, and is too big
  await pg.locator("#imp-zip").setInputFiles(huge);
  const stageErr = () => pg.locator("#imp-stage-err").innerText();
  await waitFor(async () => /larger than 34 MiB/.test(await stageErr()), "the size message", 30000);
  ok(/choose a folder/.test(await stageErr()), "the size message does not say what to do: " + (await stageErr()));
  const notzip = join(kit.tmp, "notes.zip");
  writeFileSync(notzip, "this is not a zip file");
  await pg.locator("#imp-zip").setInputFiles(notzip);
  await waitFor(async () => /not a readable zip file/.test(await stageErr()), "the not-a-zip message");
  eq(await pg.locator("#imp-review").count(), 0, "a review is shown for something that is not a zip");
  const empty = join(kit.tmp, "empty-folder");
  write(empty, "readme.txt", "Only a text file.");
  await pg.locator("#imp-folder").setInputFiles(empty);
  await pg.locator("#imp-h").waitFor();
  eq(await pg.locator("[data-go]").isDisabled(), true, "Import is offered with nothing to import");
  await pg.locator(".imp-skipped summary").click();
  ok(/not a Markdown file/.test(await pg.locator(".imp-skipped").innerText()), "the skipped file is not explained");
});

await kit.test("import: a folder is added to an existing project after what is there", async () => {
  const made = (await api(main, "POST", "/api/v1/projects", { title: "Existing book", kind: "novel" })).json.item;
  const first = (await docs(made.id)).order[0];
  const before = (await api(main, "GET", `/api/v1/docs/${first}`)).json.item;
  await open(`/p/${made.slug}/import`);
  ok(/Add files to Existing book/.test(await pg.locator("h1").innerText()), "the page does not name the project");
  eq(await pg.locator("#imp-t").count(), 0, "a title box is offered when adding to a project");
  await pg.locator("#imp-folder").setInputFiles(book);
  await pg.locator("#imp-h").waitFor();
  await pg.locator("[data-go]").click();
  await pg.waitForURL(new RegExp(`/p/${made.slug}/d/doc_`), { timeout: 20000 });
  const d = await docs(made.id), byId = Object.fromEntries(d.items.map((i) => [i.id, i]));
  eq(d.order[0], first, "an existing document moved");
  ok(d.items.length >= 6, "too few documents after adding: " + d.items.length);
  const after = (await api(main, "GET", `/api/v1/docs/${first}`)).json.item;
  eq(after.revision, before.revision, "an existing document was changed");
  ok(byId[d.order[1]], "the added documents are not after the existing one");
  // A reader of the project cannot open the page.
  const rctx = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  const rp = await rctx.newPage();
  const { signIn } = await import("./harness.mjs");
  await signIn(rp, base(), "reader@dev.scriobh");
  const r = await rp.goto(`${base()}/p/${made.slug}/import`);
  eq(r.status(), 404, "a stranger learns the project exists");
  await rctx.close();
});

await kit.test("mirror: the project menu says whether the Markdown copy is current, and Rebuild works", async () => {
  const p = (await api(main, "POST", "/api/v1/projects", { title: "Mirrored book", kind: "novel" })).json.item;
  await pg.goto(base() + "/");
  await pg.getByRole("link", { name: "Actions for Mirrored book" }).focus();
  await pg.keyboard.press("Enter");
  await pg.locator("dialog#dialog[open]").waitFor();
  await sleep(300);
  const line = pg.locator("#dialog .mirror-line");
  await line.waitFor();
  const before = await line.innerText();
  ok(/Markdown copy/.test(before), "the mirror line: " + before);
  await pg.getByRole("button", { name: "Rebuild the copy" }).focus();
  await pg.keyboard.press("Enter");
  await waitFor(async () => /The Markdown copy is up to date\./.test(await pg.locator("#dialog .mirror-line").innerText()), "the line to say it is current after Rebuild");
  ok(/files?, last changed/.test(await pg.locator("#dialog .mirror-line").innerText()), "no file count or time: " + (await pg.locator("#dialog .mirror-line").innerText()));
  const st = (await api(main, "GET", `/api/v1/projects/${p.id}/mirror`)).json.item;
  ok(st.in_sync && st.files > 0, "the API disagrees: " + JSON.stringify(st));
  // Focus stays inside the dialog after the swap.
  // htmx swaps the dialog's body and the shell puts the focus back when the swap has settled (htmx's 20 ms settle delay): the check waits for that, as a person's next key would.
  await waitFor(() => pg.evaluate(() => !!document.activeElement && document.activeElement.closest("dialog#dialog") !== null), "focus to be back in the dialog after Rebuild", 3000);
  await pg.keyboard.press("Escape");
});

await kit.test("backups: an administrator takes a backup, sees it listed and downloads it; a writer has no section", async () => {
  const { ctx, page } = await kit.signedIn({}, "admin@dev.scriobh");
  try {
    await page.goto(base() + "/settings");
    await page.locator("#backups").waitFor();
    const text = await page.locator("#backups").innerText();
    ok(/There are no backups yet|Scheduled|Manual/.test(text) && /secret\.key/.test(text) && /scriobh restore/.test(text), "the section: " + text.slice(0, 300));
    await page.getByRole("button", { name: "Back up now" }).click();
    await page.waitForURL(/saved=backup/);
    await page.locator("#backups .saved").waitFor();
    const row = page.locator("#backups .sessions li").first();
    ok(/Manual backup/.test(await row.innerText()), "the new backup is not listed: " + (await row.innerText()));
    const dl = page.waitForEvent("download");
    await row.getByRole("link", { name: /Download/ }).click();
    const d = await dl;
    ok(/\.zip$/.test(d.suggestedFilename()), "the download is not a zip: " + d.suggestedFilename());
    await page.close();
  } finally { await ctx.close(); }
  await pg.goto(base() + "/settings");
  await pg.locator("#sessions").waitFor();
  eq(await pg.locator("#backups").count(), 0, "a writer sees the Backups section");
});

// A stored zip written by hand (no dependency): entries are named files with their bytes.
import { crc32 } from "node:zlib";
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

await kit.test("import: a Word document is previewed with what was left out, and created", async () => {
  // a .docx made by this program itself: a small book compiled as a reading copy
  const src = (await api(main, "POST", "/api/v1/projects", { title: "Word Source", kind: "novel", author_name: "Wren Writer" })).json.item;
  const first = (await docs(src.id)).order[0];
  await api(main, "PATCH", `/api/v1/docs/${first}`, { base_revision: 1, title: "One", content: `${words(80)}[^1]\n\n[^1]: A note.\n\n* * *\n\n${words(40, "after")}\n` });
  await api(main, "POST", `/api/v1/projects/${src.id}/docs`, { title: "Two", kind: "chapter", content: `${words(90, "two")}\n` });
  const r = await main.request.fetch(`${base()}/api/v1/projects/${src.id}/export/compile`, { method: "POST", data: { preset: "styled-docx" }, headers: { "Content-Type": "application/json" } });
  eq(r.status(), 200, "the compile");
  const docx = join(kit.tmp, "book.docx");
  writeFileSync(docx, await r.body());
  await open();
  await pg.check("#imp-f-docx"); // the Word panel of the chooser
  await pg.locator("#imp-docx").setInputFiles(docx);
  await pg.locator("#imp-h").waitFor();
  const sum = await pg.locator(".imp-sum").innerText();
  ok(/documents/.test(sum) && /words/.test(sum), "the summary: " + sum);
  ok(/A Word document/.test(sum) && !/folder of Markdown/.test(sum), "the preview says what it is: " + sum);
  const titles = await treeTitles();
  ok(titles.includes("One") && titles.includes("Two"), "the chapters come from the headings: " + titles.join(", "));
  eq(await pg.locator("#imp-t").inputValue(), "Word Source", "the project's title comes from the document");
  // another way to cut: the preview is made again
  await pg.locator('#imp-cutform input[value="none"]').check();
  await pg.locator("#imp-cut-apply").click();
  await settle();
  ok((await status()) === "" || !/could not/i.test(await status()), "one document: " + await status());
  await pg.locator('#imp-cutform input[value="auto"]').check();
  await pg.locator("#imp-cut-apply").click();
  await settle();
  await pg.locator("[data-go]").click();
  await pg.waitForURL(/\/p\/word-source[^/]*\/d\//, { timeout: 20000 });
  const created = (await api(main, "GET", "/api/v1/projects")).json.items.filter((p) => p.title === "Word Source");
  ok(created.length === 2, "the import is a new project beside the source: " + created.length);
});

await kit.test("import: an Obsidian vault is converted, with what was dropped listed in the preview", async () => {
  const vault = zipStore({
    "Vault/.obsidian/app.json": "{}",
    "Vault/01 Start.md": `# Start\n\nSee [[Other#Heading|the other page]] and ![[Embedded]] ^block1\n\n> [!note] A callout\n> inside\n\n%%a private comment%%\n\n\`\`\`dataview\nTABLE x\n\`\`\`\n\n${words(60)}\n`,
    "Vault/02 Other.md": `# Other\n\n${words(50, "other")}\n\n![[picture.png]]\n`,
    "Vault/board.canvas": "{}",
  });
  const zipPath = join(kit.tmp, "vault.zip");
  writeFileSync(zipPath, vault);
  await open();
  await pg.check("#imp-obs");
  await pg.locator("#imp-zip").setInputFiles(zipPath);
  await pg.locator("#imp-h").waitFor();
  const notes = await pg.locator("#imp-review .imp-note, #imp-review .imp-warn, #imp-review .imp-skipped").allInnerTexts();
  const all = notes.join(" | ");
  ok(/canvas/i.test(all), "the canvas is listed as skipped: " + all);
  ok(/dataview|Templater|image|picture|attachment/i.test(all + (await pg.locator("#imp-review").innerText())), "what was dropped is reported");
  ok((await treeTitles()).length >= 2, "two notes become two chapters");
  ok(/An Obsidian vault/.test(await pg.locator(".imp-sum").innerText()), "the preview says it is a vault: " + await pg.locator(".imp-sum").innerText());
  await pg.locator("[data-go]").click();
  await pg.waitForURL(/\/p\/[^/]+\/d\//, { timeout: 20000 });
  const slug = new URL(pg.url()).pathname.split("/")[2];
  const pid = (await api(main, "GET", "/api/v1/projects")).json.items.find((p) => p.slug === slug).id;
  const order = (await docs(pid)).order;
  const text = (await Promise.all(order.map(async (id) => (await api(main, "GET", `/api/v1/docs/${id}`)).json.item.content))).join("\n");
  ok(text.includes("[[Other|Other > Heading]]") || text.includes("the other page"), "the link to a heading points at the page");
  ok(text.includes("**Note: A callout**") && !text.includes("dataview") && text.includes("%%a private comment%%"), "callout, dataview and the private comment are handled as documented");
});

await kit.finish("e2e-import");
