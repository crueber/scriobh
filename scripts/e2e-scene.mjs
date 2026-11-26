#!/usr/bin/env node
// End-to-end test of the "In this scene" tab of the inspector: the entities a scene names, with kind and the
// book's summary; the point of view and place chosen from the bible with the keyboard alone; free text kept as
// it was; the calm "still looking" state; the wording when some names are left out; and the 390 px phone.
//
//   node scripts/e2e-scene.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-scene");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page } = await kit.signedIn();
kit.main = page;

let book, doc, cmp, ada, harbour;
const panel = () => page.locator("#ipanel-scene");
// The inspector remembers whether it was open, so the tab is opened through the same hook the palette uses.
const open = async (pg = page, url = true) => {
  if (url) await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.inspector)), "the inspector", 10000);
  await pg.evaluate(() => window.scriobh.inspector.open("scene", false));
  await pg.locator("#ipanel-scene").waitFor({ state: "visible" });
};

await kit.test("setup: a book with a bible and a scene that names three of its entities", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: "Scene Book", kind: "novel" })).json.item;
  doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  cmp = (await api(main, "POST", "/api/v1/compendiums", { name: "Scene Bible", project_id: book.id })).json.item;
  const mk = async (kind, name, summary, aliases) => (await api(main, "POST", `/api/v1/compendiums/${cmp.id}/entities`, { kind, name, summary, aliases })).json.item;
  ada = await mk("character", "Ada Quill", "A ferry clerk who keeps the ledger.", ["Ada"]);
  await mk("character", "Brenna Hale", "The harbourmaster.");
  harbour = await mk("place", "Saltmarsh Harbour", "A grey harbour town.");
  await mk("item", "The Brass Lamp", "A lamp that is never lit.", ["the lamp"]);
  const cur = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "The ledger", content: "Ada carried the lamp down to Saltmarsh Harbour. Ada did not light it.\n" });
});

await kit.test("the tab lists what the scene names, with kind and the book's summary, and says nothing is linked yet", async () => {
  await open();
  await waitFor(async () => (await panel().locator(".sc-name").count()) >= 3, "the names to appear", 20000);
  const names = await panel().locator(".sc-name").allInnerTexts();
  for (const n of ["Ada Quill", "Saltmarsh Harbour", "The Brass Lamp"]) ok(names.includes(n), `${n} is listed: ${names}`);
  ok(!names.includes("Brenna Hale"), "an entity the text does not name is not listed");
  ok(/Character/.test(await panel().innerText()) && /Place/.test(await panel().innerText()) && /Item/.test(await panel().innerText()), "each shows its kind");
  await panel().getByRole("button", { name: "Ada Quill" }).click();
  ok(/ferry clerk/.test(await panel().innerText()), "a name opens a quick card with the summary");
  eq(await panel().getByRole("button", { name: "Ada Quill" }).getAttribute("aria-expanded"), "true");
  ok(await panel().getByRole("link", { name: "Open the sheet" }).first().getAttribute("href") !== null, "the card links to the sheet");
});

await kit.test("the point of view is chosen from the bible with the keyboard alone", async () => {
  await panel().getByRole("button", { name: "Choose the point of view" }).focus();
  await page.keyboard.press("Enter");
  const box = panel().getByRole("combobox");
  await box.waitFor();
  ok(await box.evaluate((e) => document.activeElement === e), "the search box has focus");
  await page.keyboard.type("ad");
  await waitFor(async () => (await panel().getByRole("option").count()) === 1, "the list to narrow to one character");
  await panel().getByRole("option", { name: "Ada Quill" }).waitFor();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await waitFor(async () => (await api(main, "GET", `/api/v1/docs/${doc}/scene-entities`)).json.pov?.id === ada.id, "the point of view to be linked");
  await waitFor(async () => (await panel().innerText()).includes("point of view"), "the panel to redraw");
  const f = await page.evaluate(() => { const i = document.querySelector("#ipanel-details input[name=pov], #ipanel-details #f-pov, #ipanel-details [data-field=pov]"); return i ? i.value : null; });
  ok(f !== null, "the Details tab has a point-of-view field");
  eq(f, "Ada Quill", "the Details field shows the linked name");
  ok(await page.evaluate(() => document.activeElement.closest("#ipanel-scene") !== null), "focus stayed in the tab");
});

await kit.test("the place is chosen the same way, only places are offered, and Escape cancels", async () => {
  await panel().getByRole("button", { name: "Choose the location" }).click();
  await page.keyboard.type("ha");
  await waitFor(async () => (await panel().getByRole("option").count()) === 1, "the list to narrow to one place");
  await page.keyboard.press("Escape");
  eq(await panel().getByRole("combobox").count(), 0, "Escape closes the picker");
  ok(await page.locator("#inspector.is-open").count() === 1, "and leaves the inspector open");
  await panel().getByRole("button", { name: "Choose the location" }).click();
  await page.keyboard.type("harbour");
  await panel().getByRole("option", { name: "Saltmarsh Harbour" }).click();
  await waitFor(async () => (await api(main, "GET", `/api/v1/docs/${doc}/scene-entities`)).json.location?.id === harbour.id, "the place to be linked");
});

await kit.test("unlinking keeps the text, and free text that matches nothing is kept and marked", async () => {
  await panel().getByRole("button", { name: "Unlink the point of view from the Compendium (the text stays)" }).click();
  await waitFor(async () => (await api(main, "GET", `/api/v1/docs/${doc}/scene-entities`)).json.pov == null, "the point of view to be unlinked");
  const d = (await api(main, "GET", `/api/v1/docs/${doc}/scene-entities`)).json;
  ok(d.pov_text, "the free text stayed: " + JSON.stringify(d.pov_text));
  await waitFor(async () => /Not linked to the Compendium/.test(await panel().innerText()), "the panel to say the text is not linked");
});

await kit.test("while the index is behind the panel says so calmly and fills in; hidden names get one plain sentence", async () => {
  const t0 = Date.now() + 2500; // behind for the first moments, whichever way the tab asks
  await page.route("**/scene-entities", async (route) => {
    const r = await route.fetch();
    const j = await r.json();
    if (Date.now() < t0) { j.indexing = true; j.items = []; }
    else { j.hidden = true; }
    await route.fulfill({ response: r, json: j });
  });
  await open();
  await waitFor(async () => /Still looking through the book/.test(await panel().innerText()), "the indexing wording");
  ok(await panel().locator(".field-error").count() === 0, "it is not drawn as an error");
  await waitFor(async () => /left out because they belong to a Compendium you are not a member of/.test(await panel().innerText()), "the hidden wording after the next load", 12000);
  await page.unroute("**/scene-entities");
});

await kit.test("on a phone the tab fits at 390 px and its targets are 44 px", async () => {
  const { page: ph } = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  await open(ph);
  await waitFor(async () => (await ph.locator("#ipanel-scene .sc-name").count()) >= 3, "the names to appear", 20000);
  ok(await ph.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "no horizontal scroll");
  // A height is bottom minus top in single precision: 512 to 1024 px down the page that loses 2^-14 px (44 reads 43.99993896), so the
  // check allows a hundredth of a pixel and no more.
  const small = await ph.locator("#ipanel-scene button, #ipanel-scene a.btn, #ipanel-scene input").evaluateAll((els) => els.filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.99).map((e) => (e.textContent.trim() || e.placeholder) + " " + e.getBoundingClientRect().height));
  eq(small.length, 0, "every control is 44 px tall: " + small.join(", "));
});

await kit.finish("e2e-scene");
