#!/usr/bin/env node
// The static site page in Chromium: the status, the posts and what is left out with its reason, the warnings, an
// options save by keyboard alone (only what changed), a per-post address, the zip download, and a publish to the
// server's folder that asks first (Escape cancels, the confirming button waits half a second) and then says where it
// went and that nothing changed since. 390 px with no sideways scroll, both themes. Nothing is published on page load.
//
//   node scripts/e2e-site.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { existsSync, mkdirSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const kit = await createKit("e2e-site", { env: { SCRIOBH_SITE_PUBLISH: "1" } });
kit.deadline(150000);
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const owner = await kit.signedIn({}, "writer@dev.scriobh");

const made = await api(owner.ctx, "POST", "/api/v1/projects", { title: "Harbour Notes", kind: "blog", author_name: "Wren Writer" });
const P = made.json.item, pid = P.id, slug = P.slug;
const chapter = async (title, status, content) => {
  const d = (await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title, kind: "chapter", content })).json.item;
  await api(owner.ctx, "PATCH", `/api/v1/docs/${d.id}`, { base_revision: d.revision, status });
  return d.id;
};
const first = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
{ const g = (await api(owner.ctx, "GET", `/api/v1/docs/${first}`)).json.item; await api(owner.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, title: "Low water", status: "done", content: "The tide had gone out and taken the light with it.\n\nBy four the harbour was mud.\n" }); }
const second = await chapter("The quay", "done", "Nobody on the quay counted the boats.\n");
const draft = await chapter("Unfinished ending", "draft", "Not yet.\n");
const sitePage = `${kit.base()}/p/${slug}/site`;
const noHScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);
const T = (page, sel) => page.locator(sel).innerText();

await kit.test("the page shows the status, the posts, what is left out and why, and publishes nothing by itself", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(sitePage, { waitUntil: "load" });
  ok((await T(page, "#site-pubstate")).includes("Never published"), "never published");
  const posts = await T(page, "#site-posts");
  ok(posts.includes("Low water") && posts.includes("The quay"), "both done chapters are posts");
  ok(!posts.includes("Unfinished ending"), "the draft is not a post");
  const out = await T(page, "#site-out");
  ok(out.includes("Unfinished ending"), "the draft is listed as left out");
  eq(await page.locator(`#doc-${draft} button:has-text("Publish as a post")`).count(), 0, "a draft is not offered a post role that would not publish it");
  ok(await page.locator(`#doc-${draft} a[href="/p/${slug}/outline"]`).count() === 1, "it is pointed at the outline, where its status changes");
  ok(await page.locator(`#doc-${draft} button:has-text("Always leave out")`).count() === 1, "and can be left out for good in one click");
  ok((await T(page, "#site-warnings")).length > 0, "warnings are shown");
  ok(!existsSync(join(kit.dataDir, "sites")) || readdirSync(join(kit.dataDir, "sites")).length === 0, "nothing was published by opening the page");
  if (shots) { await page.screenshot({ path: `${shots}/site-1280.png`, fullPage: true }); kit.main = null; }
});

await kit.test("keyboard alone: the base address is saved, and only what changed is sent", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(sitePage, { waitUntil: "load" });
  await page.locator("#so-base").focus();
  await page.keyboard.type("https://harbour.example.org/notes/");
  await Promise.all([page.waitForNavigation(), page.keyboard.press("Enter")]); // submits the options form
  ok((await page.locator("p.saved[role=status]").innerText()).includes("saved"), "a saved line is announced");
  ok((await T(page, "#site-status")).includes("https://harbour.example.org/notes/"), "the address is shown as a link");
  const o = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/site/options`)).json.options;
  eq(o.base_url, "https://harbour.example.org/notes/", "the base address");
  eq(o.title, "", "an untouched field stays at its default");
});

await kit.test("a post's address by keyboard: open its form, change the address, save", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(sitePage, { waitUntil: "load" });
  const sum = page.locator(`#doc-${second} summary`);
  await sum.focus();
  await page.keyboard.press("Enter");
  await page.locator(`#d-s-${second}`).focus();
  await page.keyboard.type("on-the-quay");
  await Promise.all([page.waitForNavigation(), page.keyboard.press("Enter")]);
  ok((await T(page, `#doc-${second}`)).includes("posts/on-the-quay.html"), "the new address is listed");
});

await kit.test("download: the zip is the site", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(sitePage, { waitUntil: "load" });
  const [dl] = await Promise.all([page.waitForEvent("download"), page.locator("#site-build").click()]);
  const b = readFileSync(await dl.path());
  ok(b.length > 500 && b[0] === 0x50 && b[1] === 0x4b, "a zip file");
  ok(dl.suggestedFilename().endsWith("-site.zip"), "named for the site: " + dl.suggestedFilename());
});

await kit.test("publish asks first: Escape cancels, the confirm waits, then it says where it went", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(sitePage, { waitUntil: "load" });
  await page.locator("#site-publish").focus();
  await page.keyboard.press("Enter");
  await page.waitForSelector("#site-publish-form .arm");
  ok((await page.locator("#site-publish-form .arm-msg").innerText()).includes("sites folder"), "the question names the folder");
  eq(await page.evaluate(() => document.activeElement.textContent), "Cancel", "focus is on Cancel");
  ok(await page.locator("#site-publish-form .arm-ok").isDisabled(), "the confirming button waits");
  await page.keyboard.press("Escape");
  eq(await page.locator("#site-publish-form .arm").count(), 0, "Escape put it away");
  eq(await page.evaluate(() => document.activeElement.id), "site-publish", "focus is back on Publish");
  ok(!existsSync(join(kit.dataDir, "sites")) || readdirSync(join(kit.dataDir, "sites")).length === 0, "nothing published on Escape");
  await page.keyboard.press("Enter");
  await page.waitForSelector("#site-publish-form .arm");
  await kit.sleep(600);
  await page.keyboard.press("Tab");
  await Promise.all([page.waitForNavigation(), page.keyboard.press("Enter")]);
  await page.waitForSelector("#site-done");
  ok((await T(page, "#site-done")).includes("Published"), "it says it was published");
  ok((await T(page, "#site-status")).includes("/public"), "and where, relative to the sites folder");
  ok((await T(page, "#site-pubstate")).includes("Up to date"), "nothing changed since");
  const dirs = readdirSync(join(kit.dataDir, "sites"));
  eq(dirs.length, 1, "one folder");
  ok(existsSync(join(kit.dataDir, "sites", dirs[0], "public", "index.html")), "with the site in public/");
  // a change in the book: the page says the site is behind
  const g = (await api(owner.ctx, "GET", `/api/v1/docs/${second}`)).json.item;
  await api(owner.ctx, "PATCH", `/api/v1/docs/${second}`, { base_revision: g.revision, content: "Nobody on the quay counted the boats. One more line.\n" });
  await page.goto(sitePage, { waitUntil: "load" });
  ok((await T(page, "#site-pubstate")).includes("Changed since"), "changed since the publish");
  // a second publish names the folder it replaces
  await page.locator("#site-publish").click();
  ok((await page.locator("#site-publish-form .arm-msg").innerText()).includes(dirs[0]), "the question names the folder it replaces");
  await page.keyboard.press("Escape");
});

await kit.test("390 px, dark: cards, full-width buttons, nothing scrolls sideways", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 844 }, colorScheme: "dark", hasTouch: true }, "writer@dev.scriobh");
  const page = ph.page; kit.main = page;
  await page.goto(sitePage, { waitUntil: "load" });
  ok(await noHScroll(page), "no sideways scroll");
  const w = await page.locator("#site-build").evaluate((e) => e.getBoundingClientRect().width);
  ok(w > 300, "the download button is full width: " + w);
  if (shots) await page.screenshot({ path: `${shots}/site-390-dark.png` });
  await page.locator(`#doc-${first} summary`).click();
  ok(await noHScroll(page), "no sideways scroll with a form open");
  if (shots) await page.locator("#site-options").screenshot({ path: `${shots}/site-options-390-dark.png` });
  await ph.ctx.close();
  kit.main = null;
});

await kit.finish("e2e-site");
