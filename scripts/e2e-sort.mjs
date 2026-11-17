#!/usr/bin/env node
// End-to-end test of the outline's orders and columns: manuscript, story timeline and point of view, each a real
// link that swaps the list in place; the choice and the columns remembered per person; no move tools in the flat
// orders; live changes still arriving; the keyboard alone; 390 px.
//
//   node scripts/e2e-sort.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-sort");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page } = await kit.signedIn();
kit.main = page;

let book, cmp, mara, joss, ch;
// The project starts with an empty chapter of its own; it is not part of what is being tested.
const titles = async () => (await page.locator(".orow-title").allInnerTexts()).filter((t) => t !== "Chapter 1");
const link = (name) => page.locator(`#o-tools [data-sort-link="${name}"]`);

await kit.test("setup: a book whose scenes have story dates and points of view", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: "Sort Book", kind: "novel" })).json.item;
  cmp = (await api(main, "POST", "/api/v1/compendiums", { name: "Sort Bible", project_id: book.id })).json.item;
  const mk = async (name) => (await api(main, "POST", `/api/v1/compendiums/${cmp.id}/entities`, { kind: "character", name })).json.item;
  mara = await mk("Mara Voss");
  joss = await mk("Joss Hale");
  const part = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Part One", kind: "part" })).json.item.id;
  ch = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Low Water", kind: "chapter", parent_id: part })).json.item.id;
  const scene = async (title, date, pov) => {
    const id = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title, kind: "scene", parent_id: ch, content: title + " text." })).json.item.id;
    const cur = (await api(main, "GET", `/api/v1/docs/${id}`)).json.item;
    const r = await api(main, "PATCH", `/api/v1/docs/${id}`, { base_revision: cur.revision, story_date: date });
    if (pov) await api(main, "PUT", `/api/v1/docs/${id}/pov`, { entity: pov, base_revision: r.json.item.revision });
    return id;
  };
  await scene("Third in the book", "1999-05-01", mara.id);
  await scene("Second in the book", "1998-04-12", joss.id);
  await scene("First in the book, undated", "", mara.id);
  await scene("Last in the book", "1997-01-02", null);
});

await kit.test("the manuscript order is the default and shows the tree", async () => {
  await page.goto(`${base()}/p/${book.slug}/outline`);
  await page.waitForSelector(".orow");
  eq(await link("order").getAttribute("aria-current"), "true");
  eq((await titles()).slice(0, 2).join("|"), "Part One|Low Water");
  eq(await page.locator(".o-groups").count(), 0);
});

await kit.test("Story timeline orders scenes by date, undated last, in place, with the address and the remembered choice", async () => {
  await link("timeline").click();
  await waitFor(async () => (await link("timeline").getAttribute("aria-current")) === "true", "the order to change");
  eq((await titles()).join("|"), "Last in the book|Second in the book|Third in the book|First in the book, undated");
  ok(/sort=timeline/.test(page.url()), "the address names the order: " + page.url());
  ok(await page.evaluate(() => document.activeElement && document.activeElement.dataset.sortLink === "timeline"), "focus stays on the control");
  ok(/Part One \/ Low Water/.test(await page.locator(".orow-crumb").first().innerText()), "each scene says where it sits");
  await waitFor(async () => /story timeline/.test(await page.locator("#o-live").innerText()), "the change to be announced");
  // Leaving and coming back to the plain address brings the remembered order.
  await page.goto(`${base()}/p/${book.slug}/outline`);
  await waitFor(async () => (await link("timeline").getAttribute("aria-current")) === "true", "the saved order to return", 8000);
  eq((await titles())[0], "Last in the book");
});

await kit.test("columns: story date shows in the timeline, can be turned off, and the choice is remembered", async () => {
  ok(await page.locator(".orow-date").first().isVisible(), "the date column is on by default in the timeline order");
  eq((await page.locator(".orow-date").allInnerTexts()).map((t) => t.replace(/Story date:\s*/, "").trim()).slice(0, 4).join("|"), "1997-01-02|1998-04-12|1999-05-01|Undated");
  await page.locator("#o-colsel summary").click();
  await page.getByLabel("Story date").uncheck();
  await waitFor(async () => !(await page.locator(".orow-date").first().isVisible()), "the date column to go");
  await page.getByLabel("Point of view").check();
  await waitFor(async () => await page.locator(".orow-pov").first().isVisible(), "the point-of-view column to appear");
  ok(/Mara Voss|None|Joss Hale/.test(await page.locator(".orow-pov").first().innerText()));
  await page.goto(`${base()}/p/${book.slug}/outline`);
  await waitFor(async () => (await page.locator(".orow-pov").count()) > 0 && (await page.locator(".orow-pov").first().isVisible()), "the choice to return", 8000);
  ok(!(await page.locator(".orow-date").first().isVisible()), "dates stay off");
  await page.locator("#o-colsel summary").click();
  await page.getByLabel("Words").uncheck();
  await waitFor(async () => !(await page.locator(".orow-nums").first().isVisible()), "the words column to go");
  await page.getByLabel("Words").check();
});

await kit.test("Point of view groups the scenes under the character, with counts, and none last", async () => {
  await link("pov").click();
  await waitFor(async () => (await page.locator(".o-groups").count()) === 1, "the groups");
  const heads = (await page.locator(".o-gh").allInnerTexts()).map((t) => t.replace(/\s+/g, " ").trim());
  ok(heads.some((h) => /^Mara Voss 2 scenes/.test(h)), "Mara's group: " + heads);
  ok(heads.some((h) => /^Joss Hale 1 scene/.test(h)), "Joss's group: " + heads);
  ok(/^No point of view \d scenes?/.test(heads[heads.length - 1]), heads[heads.length - 1]);
  const mara = page.locator(".o-grp", { hasText: "Mara Voss" });
  eq((await mara.locator(".orow-title").allInnerTexts()).join("|"), "Third in the book|First in the book, undated", "manuscript order inside a group");
  ok((await page.locator("ol.o-sub[aria-labelledby]").count()) >= 3, "each group is a labelled list");
});

await kit.test("the flat orders offer no move tools, and say so", async () => {
  await page.locator(".orow").first().click();
  await page.locator(".orow.is-active .orow-tools").waitFor();
  eq(await page.locator(".orow.is-active .orow-tools button[data-act=up], .orow.is-active .orow-tools [data-act=grip]").count(), 0, "no move or drag tools");
  ok(await page.locator(".orow.is-active .orow-tools [data-act=edit]").count() === 1, "editing is still there");
  await page.locator(".orow.is-active").focus();
  await page.keyboard.press("Alt+ArrowDown");
  await waitFor(async () => /only possible in manuscript order/.test(await page.locator("#o-live").innerText()), "the explanation");
});

await kit.test("a scene added elsewhere arrives in the sorted list, and an open editor keeps its text", async () => {
  await link("timeline").click();
  await waitFor(async () => (await link("timeline").getAttribute("aria-current")) === "true", "timeline");
  await page.locator(".orow").first().click();
  await page.locator(".orow.is-active [data-act=edit]").click();
  await page.locator(".orow-edit textarea").fill("Words I am in the middle of");
  const n = await page.locator(".orow").count();
  const id = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Added in another tab", kind: "scene", parent_id: ch })).json.item.id;
  await sleep(2500);
  eq(await page.locator(".orow-edit textarea").inputValue(), "Words I am in the middle of", "the draft is untouched");
  await page.getByRole("button", { name: "Cancel" }).click();
  await waitFor(async () => (await page.locator(".orow").count()) === n + 1 || (await titles()).includes("Added in another tab"), "the new scene to appear", 12000);
  ok(id, "created");
});

await kit.test("back to the manuscript order: the tree and its move tools return", async () => {
  await link("order").click();
  await waitFor(async () => (await page.locator(".o-groups").count()) === 0 && (await titles())[0] === "Part One", "the tree");
  await page.locator(".orow").nth(1).click();
  await page.locator(".orow.is-active .orow-tools [data-act=up]").waitFor();
});

await kit.test("with the keyboard alone: the order links and the columns are reachable and work", async () => {
  await link("pov").focus();
  await page.keyboard.press("Enter");
  await waitFor(async () => (await page.locator(".o-groups").count()) === 1, "the groups, from the keyboard");
  await link("timeline").focus();
  await page.keyboard.press("Enter");
  await waitFor(async () => (await link("timeline").getAttribute("aria-current")) === "true", "timeline from the keyboard");
  await page.locator("#o-colsel summary").focus();
  await page.keyboard.press("Enter");
  await page.getByLabel("Story date").focus();
  await page.keyboard.press("Space");
  ok(await page.getByLabel("Story date").isChecked() === (await page.locator(".orow-date").first().isVisible()), "the checkbox and the column agree");
});

await kit.test("on a phone the orders fit at 390 px with 44 px targets", async () => {
  const { page: ph } = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  await ph.goto(`${base()}/p/${book.slug}/outline?sort=pov`);
  await ph.waitForSelector(".o-groups");
  ok(await ph.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "no horizontal scroll");
  const small = await ph.locator(".o-sort a, .o-colsel summary").evaluateAll((els) => els.filter((e) => e.getBoundingClientRect().height < 43.5).map((e) => e.textContent.trim()));
  eq(small.length, 0, "every control is 44 px tall: " + small.join(", "));
});

await kit.finish("e2e-sort");
