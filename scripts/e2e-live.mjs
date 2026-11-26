#!/usr/bin/env node
// End-to-end test of live updates with two tabs of one person: a change made in one appears in the other's outline
// and tree without a reload, a change to the chapter open in the other tab shows a "changed elsewhere" bar and never
// replaces what is being typed, the stream resumes from the last id after the tab was hidden, and it stops while
// hidden.
//
//   node scripts/e2e-live.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-live");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page: tabA } = await kit.signedIn();
kit.main = tabA;
const tabB = await main.newPage();
kit.watch(tabB, "tab B");

let book, doc, second;
const text = (page) => page.evaluate(() => window.scriobh.write.editor.getMarkdown());
const requests = [];
tabB.on("request", (r) => { if (/\/api\/v1\/events/.test(r.url())) requests.push({ url: r.url(), id: r.headers()["last-event-id"] || "", at: Date.now() }); });

await kit.test("setup", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: "Live Book", kind: "novel" })).json.item;
  doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: 1, title: "Chapter one", content: "The harbour was quiet.\n" });
  second = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Chapter two", kind: "chapter", content: "Two." })).json.item.id;
});

await kit.test("a chapter added in one tab appears in the other tab's outline, with no reload", async () => {
  await tabB.goto(`${base()}/p/${book.slug}/outline`);
  await tabB.waitForSelector(".orow");
  await waitFor(() => requests.length >= 1, "the stream to open", 10000);
  const before = await tabB.locator(".orow").count();
  await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Chapter three, from elsewhere", kind: "chapter", content: "Three." });
  await waitFor(async () => (await tabB.locator(".orow").count()) === before + 1, "the new row to appear", 10000);
  ok((await tabB.locator(".orow-title").allInnerTexts()).includes("Chapter three, from elsewhere"), "the new chapter is listed");
});

await kit.test("a change to the open chapter shows a bar; what is being typed is never replaced", async () => {
  kit.allowed = [/status of 409/]; // tab B's own autosave meets the other tab's save: the ordinary conflict bar
  await tabB.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await tabB.waitForSelector(".scriobh-prose");
  await waitFor(() => tabB.evaluate(() => !!(window.scriobh && window.scriobh.write && document.querySelector("[data-live]"))), "the page", 10000);
  await sleep(1500);
  await tabB.locator(".scriobh-prose").click();
  await tabB.keyboard.press(`${MOD}+End`);
  await tabB.keyboard.type(" My own words, typed here.");
  // Another tab (the API, as the same person) saves new text for this chapter while those words are unsaved.
  const cur = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  eq((await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, content: "The harbour was quiet. Someone else wrote this.\n" })).status, 200, "the other tab's save");
  await tabB.keyboard.type(" And more of my words.");
  await tabB.locator("#nb-live").waitFor({ timeout: 15000 });
  const mine = await text(tabB);
  ok(mine.includes("My own words, typed here.") && mine.includes("And more of my words.") && !mine.includes("Someone else wrote this"), "the typed text is untouched: " + mine);
  ok(await tabB.evaluate(() => document.activeElement.closest(".scriobh-prose") !== null), "focus stayed in the editor");
  ok(/unsaved text here is kept/.test(await tabB.locator("#nb-live").innerText()), "the bar says the unsaved text is kept: " + (await tabB.locator("#nb-live").innerText()));
  eq(await tabB.locator("#nb-live button", { hasText: "Reload" }).count(), 0, "no Reload while there are unsaved words");
});

await kit.test("with nothing unsaved the bar offers Reload, which brings the other version in", async () => {
  await tabB.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await tabB.waitForSelector(".scriobh-prose");
  await waitFor(() => tabB.evaluate(() => !!(window.scriobh && window.scriobh.write && document.querySelector("[data-live]"))), "the page", 10000);
  await sleep(2000);
  const cur = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, content: "Version from another tab, entirely.\n" });
  await tabB.locator("#nb-live").waitFor({ timeout: 15000 });
  await tabB.locator("#nb-live").getByRole("button", { name: "Reload" }).click();
  await waitFor(async () => (await text(tabB)).includes("Version from another tab"), "the editor to show the other version");
  eq(await tabB.locator("#nb-live").count(), 0, "the bar closes");
  // Typing afterwards saves normally on top of the new revision.
  await tabB.locator(".scriobh-prose").click();
  await tabB.keyboard.press(`${MOD}+End`);
  await tabB.keyboard.type(" Then mine.");
  await waitFor(async () => (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item.content.includes("Then mine."), "my words to save", 10000);
});

await kit.test("the tree follows another tab's changes and keeps its place", async () => {
  const before = await tabB.locator("#tree .tree-row").count();
  await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Added in the tree's absence", kind: "chapter" });
  await waitFor(async () => (await tabB.locator("#tree .tree-row").count()) === before + 1, "the tree to gain a row", 12000);
});

await kit.test("the stream stops while the page is hidden and resumes from the last id", async () => {
  const n = requests.length;
  await tabB.evaluate(() => { Object.defineProperty(document, "hidden", { configurable: true, get: () => true }); document.dispatchEvent(new Event("visibilitychange")); });
  await sleep(1500);
  await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Added while hidden", kind: "chapter" });
  await sleep(3000);
  eq(requests.length, n, "a hidden page opened no new stream");
  await tabB.evaluate(() => { Object.defineProperty(document, "hidden", { configurable: true, get: () => false }); document.dispatchEvent(new Event("visibilitychange")); });
  await waitFor(() => requests.length > n, "the stream to reopen", 8000);
  ok(requests[requests.length - 1].id !== "", "it resumed with a Last-Event-ID: " + JSON.stringify(requests[requests.length - 1]));
  await waitFor(async () => (await tabB.locator("#tree .tree-row .tree-title").allInnerTexts()).includes("Added while hidden"), "the missed change to arrive after the resume", 12000);
});

await kit.test("the entity list, the corkboard and the research board follow another tab of the same person", async () => {
  const cmp = (await api(main, "POST", "/api/v1/compendiums", { name: "Live Bible", project_id: book.id })).json.item;
  await tabB.goto(`${base()}/c/${cmp.slug}/entities`);
  await waitFor(() => tabB.evaluate(() => !!document.querySelector("[data-live]")), "the entity page", 10000);
  await sleep(1500);
  await api(main, "POST", `/api/v1/compendiums/${cmp.id}/entities`, { kind: "character", name: "Added from the other tab" });
  await waitFor(async () => /Added from the other tab/.test(await tabB.locator("#ent-list").innerText()), "the entity list to follow", 12000);
  await tabB.goto(`${base()}/p/${book.slug}/cards`);
  await tabB.waitForSelector(".ccard");
  await sleep(1500);
  const before = await tabB.locator(".ccard").count();
  await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "A card from the other tab", kind: "chapter" });
  await waitFor(async () => (await tabB.locator(".ccard").count()) === before + 1, "the corkboard to gain a card", 12000);
  await tabB.goto(`${base()}/p/${book.slug}/research`);
  await sleep(2000);
  await api(main, "POST", `/api/v1/projects/${book.id}/research`, { kind: "note", title: "A note from the other tab", body: "text" });
  await waitFor(async () => /A note from the other tab/.test(await tabB.locator("body").innerText()), "the research list to follow", 12000);
});

await kit.test("31. a chapter moved above the open one is not a change to it: no bar, and its number in the eyebrow follows", async () => {
  await tabB.goto(`${base()}/p/${book.slug}/d/${second}`);
  await tabB.waitForSelector(".scriobh-prose");
  await waitFor(() => tabB.evaluate(() => !!(window.scriobh && window.scriobh.write && document.querySelector("[data-live]"))), "the page", 10000);
  await sleep(1500);
  const meta = () => tabB.locator("#doc-meta").innerText();
  ok(/chapter 2\b/i.test(await meta()), "the eyebrow says chapter 2 before the move: " + (await meta()));
  // the open chapter goes to the top of the book: it becomes chapter 1, and the move is of this chapter itself
  eq((await api(main, "POST", `/api/v1/docs/${second}/move`, { after: "" })).status, 200, "the move");
  await sleep(3000); // past the wait a live change takes before it is said (1500 ms) and the tree's refresh
  eq(await tabB.locator("#nb-live").count(), 0, "a move is not a change to the chapter: no bar");
  await waitFor(async () => /chapter 1\b/i.test(await meta()), "the eyebrow to follow the new number: " + (await meta()), 10000);
  if (process.env.SHOTS) await tabB.locator("#doc-meta").screenshot({ path: `${process.env.SHOTS}/u3-eyebrow-1280-light.png` }); // a look, not a check
});

await kit.finish("e2e-live");
