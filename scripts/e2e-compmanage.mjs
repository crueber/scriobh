#!/usr/bin/env node
// Merging, splitting and changing the primary Compendium in Chromium (F-478): each step is a preview first; a name
// clash blocks the confirmation until it is answered; the confirmation is the Compendium's name, typed; a change made
// between the preview and the confirmation is a fresh preview, not a merge; the record says there is no undo button;
// the previews read as sections at 390 px and mirrored; an editor of the Compendium is shown none of it.
//
//   node scripts/e2e-compmanage.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { mkdirSync } from "node:fs";

const kit = await createKit("e2e-compmanage");
kit.deadline(5 * 60 * 1000);
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor } = kit;
const base = kit.base();
const owner = await kit.signedIn({}, "writer@dev.scriobh");
const editor = await kit.signedIn({}, "admin@dev.scriobh");
const mk = async (path, data) => { const r = await api(owner.ctx, "POST", path, data); ok(r.status < 300, `${path}: ${r.status} ${JSON.stringify(r.json)}`); return r.json.item || r.json; };
const src = await mk("/api/v1/compendiums", { name: "Salt Bible" });
const dst = await mk("/api/v1/compendiums", { name: "The Wide World" });
await mk(`/api/v1/compendiums/${src.id}/members`, { email: "admin@dev.scriobh", role: "editor" });
const ent = async (c, kind, name, summary = "") => (await mk(`/api/v1/compendiums/${c.id}/entities`, { kind, name, summary })).id;
const mara = await ent(src, "character", "Mara Voss", "A harbour pilot.");
const tom = await ent(src, "character", "Tom Hale", "The ferryman.");
const quay = await ent(src, "place", "The quay", "Where the boats come in.");
const lamp = await ent(src, "item", "Spare lantern", "To be split off.");
await ent(dst, "character", "Mara Voss", "Another pilot, in another book.");
const book = await mk("/api/v1/projects", { title: "The Salt Road", kind: "novel" });
const text = (page) => page.evaluate(() => document.body.innerText.replace(/[\u2068\u2069]/g, "").replace(/\s+/g, " ")); // names are isolated in the page; a reader hears the name
const hscroll = (page) => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
const names = async (c) => ((await api(owner.ctx, "GET", `/api/v1/compendiums/${c.id}/entities?limit=500`)).json.items || []).map((e) => e.name);

await kit.test("the owner opens Merge and split from the Compendium and previews a merge: the clash is named and the merge cannot be confirmed", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${base}/c/${src.slug}`, { waitUntil: "load" });
  await page.getByRole("link", { name: "Merge and split" }).click();
  await page.waitForURL(/\/manage$/);
  const t0 = await text(page);
  ok(t0.includes("There is no undo button"), "the page says there is no undo button");
  await page.selectOption("#mg-into", dst.id);
  await page.getByRole("button", { name: "Preview" }).first().click();
  await page.waitForSelector("#preview");
  const t = await text(page);
  ok(t.includes("Names already used in the target"), "the clash section");
  ok(/“Mara Voss” is already the name of “Mara Voss”/.test(t), "the clash is named: " + t.slice(t.indexOf("Names already"), t.indexOf("Names already") + 200));
  ok(/1 entity needs an answer before the move/.test(t), "the unanswered clash is counted");
  ok(await page.locator(".cmg-confirm button[type=submit]").isDisabled(), "the merge button is disabled while a clash is unanswered");
  ok(!(await page.locator("#cmg-typed").count()), "and no typed confirmation is offered yet");
  eq((await names(src)).length, 4, "nothing moved");
  if (shots) await page.screenshot({ path: `${shots}/cmanage-preview-1280.png`, fullPage: true });
  kit.main = null;
});

await kit.test("answer the clash, update the preview, type the name: the merge happens and the record says there is no undo", async () => {
  const page = owner.page; kit.main = page;
  await page.getByLabel("New name").fill("Mara of the Salt");
  await page.getByRole("radio", { name: "Rename it" }).check();
  await page.getByRole("button", { name: "Update the preview" }).click();
  await page.waitForSelector("#cmg-typed");
  ok(await page.locator(".cmg-confirm button[type=submit]").isDisabled(), "the button still waits for the name");
  await page.fill("#cmg-typed", "Salt Bible");
  ok(!(await page.locator(".cmg-confirm button[type=submit]").isDisabled()), "the typed name enables it");
  await page.fill("#cmg-typed", "Salt Bibl");
  ok(await page.locator(".cmg-confirm button[type=submit]").isDisabled(), "a wrong name disables it again");
  await page.fill("#cmg-typed", "Salt Bible");
  await page.locator(".cmg-confirm button[type=submit]").click();
  await page.waitForURL(/\/changes\//);
  const t = await text(page);
  ok(t.includes("There is no undo button") && t.includes("Record of a change"), "the record");
  ok(/Mara of the Salt/.test(t) && /Tom Hale/.test(t), "what moved is on it: " + t.slice(0, 300));
  ok(await names(dst).then((n) => n.includes("Tom Hale") && n.includes("Mara of the Salt") && n.includes("Mara Voss")), "the entities are in the target");
  if (shots) await page.screenshot({ path: `${shots}/cmanage-record-1280.png`, fullPage: true });
  kit.main = null;
});

await kit.test("a change made between the preview and the confirmation is a fresh preview, and nothing is merged", async () => {
  const a = await mk("/api/v1/compendiums", { name: "Stale A" });
  const b = await mk("/api/v1/compendiums", { name: "Stale B" });
  await ent(a, "character", "Ivo", "Waits.");
  const page = owner.page; kit.main = page;
  await page.goto(`${base}/c/${a.slug}/manage`, { waitUntil: "load" });
  await page.selectOption("#mg-into", b.id);
  await page.getByRole("button", { name: "Preview" }).first().click();
  await page.waitForSelector("#cmg-typed");
  await ent(a, "character", "Late arrival", "Added in another tab.");
  await page.fill("#cmg-typed", "Stale A");
  await page.locator(".cmg-confirm button[type=submit]").click();
  await page.waitForSelector(".notice-line");
  ok((await page.locator(".notice-line").innerText()).includes("Something changed since the preview"), "said");
  eq((await names(a)).length, 2, "nothing moved");
  await page.fill("#cmg-typed", "Stale A");
  await page.locator(".cmg-confirm button[type=submit]").click();
  await page.waitForURL(/\/changes\//);
  eq((await names(b)).length, 2, "after the fresh preview the merge is made");
  kit.main = null;
});

await kit.test("split: narrow the list, choose entities by keyboard, name the new Compendium, preview, confirm", async () => {
  const page = owner.page; kit.main = page;
  const sp = await mk("/api/v1/compendiums", { name: "Split Source" });
  await ent(sp, "place", "The quay", "Where the boats come in.");
  await ent(sp, "item", "Spare lantern", "To be split off.");
  await ent(sp, "character", "Keeper", "Stays behind.");
  await page.goto(`${base}/c/${sp.slug}/manage`, { waitUntil: "load" });
  ok(!(await page.locator("#sp-q").isDisabled()), "the filter is there");
  await page.fill("#sp-q", "quay");
  await page.getByRole("button", { name: "Narrow the list" }).click();
  await page.waitForSelector("#sp-q");
  ok(!(await text(page)).includes("Spare lantern"), "the list is narrowed");
  await page.fill("#sp-q", "");
  await page.getByRole("button", { name: "Narrow the list" }).click();
  await page.waitForSelector(".cmg-ents");
  // by keyboard: focus the checkbox and press Space
  await page.getByRole("checkbox", { name: /Spare lantern/ }).focus();
  await page.keyboard.press("Space");
  ok(await page.getByRole("checkbox", { name: /Spare lantern/ }).isChecked(), "ticked with the keyboard");
  await page.fill("#sp-name", "Lanterns");
  await page.locator('form[action$="/manage/split-preview"] button[type=submit]').click();
  await page.waitForSelector("#preview");
  ok((await text(page)).includes("into a new Compendium, “Lanterns”"), "the preview names the new Compendium");
  await page.fill("#cmg-typed", "Lanterns");
  await page.locator(".cmg-confirm button[type=submit]").click();
  await page.waitForURL(/\/changes\//);
  ok(!(await names(sp)).includes("Spare lantern") && (await names(sp)).includes("Keeper"), "the lantern left and the keeper stayed");
  const list = (await api(owner.ctx, "GET", "/api/v1/compendiums")).json.items.map((c) => c.name);
  ok(list.includes("Lanterns"), "the new Compendium exists");
  await page.goto(`${base}/c/${sp.slug}/changes`, { waitUntil: "load" });
  ok((await page.locator(".cmg-log li").count()) >= 1, "the change is listed");
  kit.main = null;
});

await kit.test("the project's primary Compendium: the preview names what changes, the change carries its fingerprint", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${base}/p/${book.slug}/compendium`, { waitUntil: "load" });
  await page.selectOption("#pc-set", src.id);
  await page.getByRole("button", { name: "See what would change" }).click();
  await page.waitForSelector("#preview");
  ok(/appear in this project/.test(await text(page)), "the preview");
  ok(await page.locator('#preview input[name="fingerprint"]').count(), "it carries a fingerprint");
  await page.getByRole("button", { name: "Make it the primary" }).click();
  await page.waitForSelector(".saved");
  ok((await text(page)).includes("The primary Compendium is changed."), "changed");
  kit.main = null;
});

await kit.test("at 390 px the preview is sections, not a table: no sideways scroll, controls 44 px", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, storageState: await owner.ctx.storageState() });
  const page = await ctx.newPage(); kit.watch(page, "phone"); kit.main = page;
  const a = await mk("/api/v1/compendiums", { name: "Phone A" });
  const b = await mk("/api/v1/compendiums", { name: "Phone B" });
  await ent(a, "character", "Dara", "x"); await ent(b, "character", "Dara", "y");
  await page.goto(`${base}/c/${a.slug}/manage`, { waitUntil: "load" });
  eq(await hscroll(page) <= 0, true, "no sideways scroll on the manage page");
  await page.selectOption("#mg-into", b.id);
  await page.getByRole("button", { name: "Preview" }).first().click();
  await page.waitForSelector("#preview");
  eq(await hscroll(page) <= 0, true, "no sideways scroll on the preview");
  eq(await page.locator("table").count(), 0, "no table");
  const small = await page.evaluate(() => [...document.querySelectorAll("main .check, main button, main a.btn, main select, main input[type=text]")].filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => e.outerHTML.slice(0, 70)));
  eq(small.length, 0, "controls under 44 px: " + small.join(" | "));
  if (shots) await page.screenshot({ path: `${shots}/cmanage-preview-390.png`, fullPage: true });
  await ctx.close();
  kit.main = null;
});

await kit.test("mirrored, and without script", async () => {
  await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } });
  const page = owner.page; kit.main = page;
  await page.goto(`${base}/c/${src.slug}/manage`, { waitUntil: "load" });
  eq(await page.evaluate(() => document.documentElement.dir), "rtl", "dir=rtl");
  eq(await hscroll(page) <= 0, true, "no sideways scroll");
  if (shots) await page.screenshot({ path: `${shots}/cmanage-rtl.png`, fullPage: true });
  await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "en" } });
  const c = await mk("/api/v1/compendiums", { name: "Plain A" });
  const d = await mk("/api/v1/compendiums", { name: "Plain B" });
  await ent(c, "character", "Zed", "z");
  const js = await kit.browser.newContext({ storageState: await owner.ctx.storageState(), javaScriptEnabled: false, serviceWorkers: "block" });
  const p = await js.newPage();
  await p.goto(`${base}/c/${c.slug}/manage`, { waitUntil: "load" });
  await p.selectOption("#mg-into", d.id);
  await p.locator('form[action$="/manage/merge-preview"] button[type=submit]').click();
  await p.fill("#cmg-typed", "Plain A");
  await p.locator(".cmg-confirm button[type=submit]").click();
  await p.waitForURL(/\/changes\//);
  ok((await names(d)).includes("Zed"), "a merge without script");
  await js.close();
  kit.main = null;
});

await kit.test("an editor of the Compendium is shown none of it, and the pages refuse them", async () => {
  const page = editor.page; kit.main = page;
  kit.allowed.push(/403/);
  await page.goto(`${base}/c/${src.slug}`, { waitUntil: "load" });
  eq(await page.getByRole("link", { name: "Merge and split" }).count(), 0, "no link");
  const r = await page.goto(`${base}/c/${src.slug}/manage`, { waitUntil: "load" });
  eq(r.status(), 403, "the page");
  kit.main = null;
});

await kit.finish("e2e-compmanage");
