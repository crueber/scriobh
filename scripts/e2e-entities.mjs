#!/usr/bin/env node
// End-to-end test of the entity bible in a real Chromium: the list and its empty state, adding with the keyboard,
// custom fields, aliases and their collision message, relationships, search by alias, the Markdown preview, a stale
// save, a project's override, a merge with its preview and a conflict choice, the export download, a reader's
// read-only view, live events that never touch a form, and the pages at 390 px.
//
//   node scripts/e2e-entities.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-entities");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;
const rd = await kit.signedIn({}, "reader@dev.scriobh"); // signs in first, so the owner can add them by email

const comp = (await api(main, "POST", "/api/v1/compendiums", { name: "Salt Bible" })).json.item;
const cu = (p = "") => `${base()}/c/${comp.slug}${p}`;
ok((await api(main, "POST", `/api/v1/compendiums/${comp.id}/members`, { email: "reader@dev.scriobh", role: "reader" })).status < 300, "adding the reader");
const proj = (await api(main, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel" })).json.item;
ok((await api(main, "PUT", `/api/v1/projects/${proj.id}/compendium`, { compendium_id: comp.id })).status < 300, "setting the primary Compendium");
const mk = async (kind, name, extra = {}) => (await api(main, "POST", `/api/v1/compendiums/${comp.id}/entities`, { kind, name, ...extra })).json.item;
const getE = async (id) => (await api(main, "GET", `/api/v1/entities/${id}`)).json.item;
const idOf = (url) => /\/e\/(ent_\w+)/.exec(url)[1];
let maire, quay;

await kit.test("entities: the empty list invites the first one, and the keyboard adds it", async () => {
  await pg.goto(cu("/entities"));
  await pg.getByRole("heading", { name: "Entities", exact: true }).waitFor();
  const body = await pg.locator("body").innerText();
  ok(body.includes("No entities yet"), "no invitation");
  eq(await pg.locator(".ptabs a[aria-current=page]").innerText(), "Entities", "the current Compendium tab");
  let found = false;
  for (let i = 0; i < 60 && !found; i++) {
    await pg.keyboard.press("Tab");
    found = await pg.evaluate(() => document.activeElement && document.activeElement.id === "ea-name");
  }
  ok(found, "the name field cannot be reached with Tab");
  await pg.keyboard.type("Máire Nolan");
  await pg.keyboard.press("Enter");
  await pg.getByText("The entity is added.").waitFor();
  maire = idOf(pg.url());
  eq(await pg.locator("h1").innerText(), "Máire Nolan", "the sheet's heading");
  eq((await getE(maire)).kind, "character", "the default kind");
});

await kit.test("entities: custom fields are defined on their own page and appear on every sheet of the kind", async () => {
  kit.allowed.push(/status of 422/); // the refusals this step provokes are answered 422
  await pg.goto(cu("/fields"));
  await pg.getByRole("heading", { name: "Custom fields" }).waitFor();
  await pg.locator("#nf-kind").selectOption("character");
  await pg.locator("#nf-label").fill("Ferry fare");
  await pg.locator("#nf-key").fill("fare");
  await pg.locator("#nf-type").selectOption("number");
  await pg.getByRole("button", { name: "Add the field" }).click();
  await pg.getByText("The field is saved.").waitFor();
  ok((await pg.locator("#character").innerText()).includes("Ferry fare"), "the field is not listed under Characters");
  // A second definition with another type is refused in plain words, next to the form.
  await pg.locator("#nf-kind").selectOption("character");
  await pg.locator("#nf-label").fill("Fare again");
  await pg.locator("#nf-key").fill("fare");
  await pg.locator("#nf-type").selectOption("text");
  await pg.getByRole("button", { name: "Add the field" }).click();
  const err = await pg.locator("[role=alert]").first().innerText();
  ok(/type cannot change/i.test(err), "the refusal: " + err);
  eq(await pg.locator("#nf-key").inputValue(), "fare", "the typed key is kept");
  // On the sheet.
  await pg.goto(cu("/e/" + maire));
  // an empty field is a quiet slot in its place and takes the words as it is; with more than eight empty the rest wait behind one row
  if (!(await pg.locator("#ef-fare").isVisible())) await pg.getByText(/Show \d+ more fields/).click();
  await pg.locator("#ef-fare").fill("34");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await pg.getByText("Saved.", { exact: true }).waitFor();
  eq(await pg.locator("#ef-fare").inputValue(), "34", "the value after saving");
  eq((await getE(maire)).fields.fare, "34", "the stored value");
  // A value of the wrong type is explained and kept in its box.
  await pg.locator("#ef-fare").fill("a lot");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await pg.locator("#ent-error").waitFor();
  eq(await pg.locator("#ef-fare").inputValue(), "a lot", "the refused text stays");
  eq((await getE(maire)).fields.fare, "34", "a refused value changed the entity");
});

await kit.test("entities: the sheet, by keyboard alone, to a saved summary", async () => {
  await pg.goto(cu("/e/" + maire));
  let found = false;
  for (let i = 0; i < 80 && !found; i++) {
    await pg.keyboard.press("Tab");
    found = await pg.evaluate(() => document.activeElement && document.activeElement.id === "es-summary");
  }
  ok(found, "the summary cannot be reached with Tab");
  await pg.keyboard.type("A ferrywoman.");
  let saveFocused = false;
  for (let i = 0; i < 60 && !saveFocused; i++) {
    await pg.keyboard.press("Tab");
    saveFocused = await pg.evaluate(() => { const a = document.activeElement; return a && a.tagName === "BUTTON" && a.textContent.trim() === "Save"; });
  }
  ok(saveFocused, "the Save button cannot be reached with Tab");
  await pg.keyboard.press("Enter");
  await pg.getByText("Saved.", { exact: true }).waitFor();
  eq((await getE(maire)).summary, "A ferrywoman.", "the stored summary");
});

await kit.test("entities: an alias, and the message when another entity already uses the word", async () => {
  kit.allowed.push(/status of 422/); // the refusals this step provokes are answered 422
  quay = (await mk("place", "Salt Quay", { summary: "Where the road meets the water." })).id;
  await pg.goto(cu("/e/" + maire));
  await pg.locator("#ea-alias").fill("Mam");
  await pg.getByRole("button", { name: "Add the name" }).click();
  await pg.getByText("Another name added.").waitFor();
  ok((await pg.locator("#aliases").innerText()).includes("Mam"), "the alias is not listed");
  await pg.locator("#ea-alias").fill("salt  quay");
  await pg.getByRole("button", { name: "Add the name" }).click();
  const box = pg.locator("#ent-error");
  await box.waitFor();
  const t = await box.innerText();
  ok(t.includes("Salt Quay"), "the message does not name the other entity: " + t);
  eq(await box.locator("a").first().getAttribute("href"), `/c/${comp.slug}/e/${quay}`, "the link to the other entity");
  eq(await pg.locator("#ea-alias").inputValue(), "salt  quay", "the typed alias is kept");
  eq(await pg.locator("#ea-alias").getAttribute("aria-invalid"), "true", "the field is marked invalid");
  eq((await getE(maire)).aliases.length, 1, "a refused alias was added");
});

await kit.test("entities: a relationship reads from each side", async () => {
  kit.allowed.push(/status of 422/); // the refusals this step provokes are answered 422
  await pg.goto(cu("/e/" + maire));
  await pg.getByText("Add a relationship", { exact: true }).click();
  await pg.locator("#er-other").fill("Salt Quay");
  await pg.locator("#er-label").fill("keeps the ferry at");
  await pg.locator("#er-inv").fill("ferry kept by");
  await pg.getByRole("button", { name: "Add the relationship" }).click();
  await pg.getByText("Relationship added.").waitFor();
  const t = await pg.locator("#relations").innerText();
  ok(t.includes("keeps the ferry at") && t.includes("Salt Quay") && t.includes("from this entity"), "the relationship: " + t);
  await pg.goto(cu("/e/" + quay));
  const t2 = await pg.locator("#relations").innerText();
  ok(t2.includes("ferry kept by") && t2.includes("Máire Nolan") && t2.includes("from the other"), "the other side: " + t2);
  // A name that is not here is explained.
  await pg.goto(cu("/e/" + maire));
  await pg.getByText("Add a relationship", { exact: true }).click();
  await pg.locator("#er-other").fill("Nobody at all");
  await pg.getByRole("button", { name: "Add the relationship" }).click();
  ok((await pg.locator("#ent-error").innerText()).includes("No entity in this Compendium has that name"), "no explanation for an unknown name");
});

await kit.test("entities: search finds an entity by an alias and says where it matched", async () => {
  await pg.goto(cu("/entities"));
  await pg.locator("#ent-q").fill("mam");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/q=mam/);
  const t = await pg.locator("#ent-list").innerText();
  ok(t.includes("Máire Nolan") && /alias/i.test(t), "an alias search: " + t);
  ok(!t.includes("Salt Quay"), "the search matched something unrelated");
  await pg.locator("#ent-q").fill("zzzz");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/q=zzzz/);
  ok((await pg.locator("#ent-list").innerText()).includes("Nothing matches"), "no honest empty state for a search");
  await pg.getByRole("link", { name: "Places" }).click();
  await pg.waitForURL(/kind=place/);
  await pg.getByRole("link", { name: "Clear" }).click();
  ok((await pg.locator("#ent-list").innerText()).includes("Salt Quay"), "the kind filter lost the place");
});

await kit.test("entities: the Markdown preview is rendered by the server, and hidden again", async () => {
  await pg.goto(cu("/e/" + maire));
  const btn = pg.getByRole("button", { name: "Preview" });
  await btn.waitFor();
  await pg.locator("#es-body").fill("She knows **every** tide.\n\n<script>alert(1)</script>");
  await btn.click();
  const prev = pg.locator("#es-prev");
  await prev.waitFor({ state: "visible" });
  eq(await prev.locator("strong").innerText(), "every", "bold in the preview");
  eq(await prev.locator("script").count(), 0, "raw HTML became a script");
  eq(await pg.locator("#es-preview").getAttribute("aria-expanded"), "true", "aria-expanded");
  await pg.locator("#es-preview").click();
  await prev.waitFor({ state: "hidden" });
});

await kit.test("entities: a stale save keeps the person's text and offers Keep mine and Use theirs", async () => {
  kit.allowed.push(/status of 422/); // the refusals this step provokes are answered 422
  await pg.goto(cu("/e/" + maire));
  const r1 = await api(main, "PATCH", `/api/v1/entities/${maire}`, { summary: "Changed by someone else." });
  ok(r1.status === 200, "the other change: " + r1.status);
  await pg.locator("#es-summary").fill("My careful summary");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await pg.locator("#ent-error").waitFor();
  const t = await pg.locator("#ent-error").innerText();
  ok(/changed since you opened it/.test(t), "the explanation: " + t);
  eq(await pg.locator("#es-summary").inputValue(), "My careful summary", "the person's text is kept");
  ok((await pg.locator("details.more[open]").innerText()).includes("Changed by someone else."), "their version is shown");
  eq((await getE(maire)).summary, "Changed by someone else.", "a stale save went through");
  await pg.getByRole("button", { name: "Keep mine" }).click();
  await pg.getByText("Saved.", { exact: true }).waitFor();
  eq((await getE(maire)).summary, "My careful summary", "Keep mine did not save");
});

await kit.test("entities: a project's override is its own, and clearing returns the Compendium's", async () => {
  await pg.goto(`${base()}/p/${proj.slug}/entities`);
  await pg.getByRole("heading", { name: "Entities", exact: true }).waitFor();
  const list = await pg.locator("#ent-list").innerText();
  ok(list.includes("Máire Nolan") && list.includes("Primary Compendium"), "the project's list: " + list);
  await pg.getByRole("link", { name: "Máire Nolan" }).click();
  await pg.locator("#ov-name").fill("Mairín");
  await pg.locator("#ov-notes").fill("Older in this book.");
  await pg.getByRole("button", { name: /Save this book/ }).click();
  await pg.getByText("override is saved").waitFor();
  eq(await pg.locator("h1").innerText(), "Mairín", "the book's name");
  eq((await getE(maire)).name, "Máire Nolan", "an override changed the Compendium");
  await pg.goto(`${base()}/p/${proj.slug}/entities`);
  ok((await pg.locator("#ent-list").innerText()).includes("This book changes it"), "the list does not mark the override");
  await pg.getByRole("link", { name: "Mairín" }).click();
  await pg.getByRole("button", { name: /Clear this book/ }).click();
  await pg.getByText("The override is cleared").waitFor();
  eq(await pg.locator("h1").innerText(), "Máire Nolan", "the name after clearing");
});

await kit.test("entities: merge shows the plan first, asks about each difference, and the old address leads to the survivor", async () => {
  const dup = await mk("character", "Maire", { summary: "second", fields: { fare: "35" }, aliases: ["Mairín"] });
  await pg.goto(cu("/e/" + dup.id + "/merge"));
  await pg.locator("#mg-q").fill("Máire");
  await pg.getByRole("button", { name: "Find" }).click();
  await pg.getByRole("link", { name: "Máire Nolan" }).click();
  await pg.getByRole("heading", { name: "What will happen" }).waitFor();
  const plan = await pg.locator(".plan").innerText();
  ok(plan.includes("Máire Nolan") && plan.includes("Mairín"), "the plan: " + plan);
  ok((await pg.locator("#merge-form").innerText()).includes("Fields that differ"), "the conflict is not shown");
  eq(await pg.getByRole("button", { name: /^Merge Maire into/ }).count(), 0, "a merge button is offered while a difference is unanswered");
  eq((await getE(dup.id)).name, "Maire", "the preview changed something");
  await pg.locator('input[name="fc.fare"][value="source"]').check();
  await pg.getByRole("button", { name: "Update the preview" }).click();
  await pg.getByRole("button", { name: /^Merge Maire into/ }).waitFor();
  await pg.getByRole("button", { name: /^Merge Maire into/ }).click();
  await pg.getByText("The merge is done").waitFor();
  eq(idOf(pg.url()), maire, "the survivor");
  eq(await pg.locator("#ef-fare").inputValue(), "35", "the chosen field value");
  await pg.goto(cu("/e/" + dup.id));
  await pg.getByText("was merged into this one").waitFor();
  eq(idOf(pg.url()), maire, "the old address led to the survivor");
});

await kit.test("entities: the export downloads a zip", async () => {
  await pg.goto(cu("/entities"));
  const dl = pg.waitForEvent("download");
  await pg.getByRole("button", { name: /Export as Markdown/ }).click();
  const d = await dl;
  ok(/\.zip$/.test(d.suggestedFilename()), "the file name: " + d.suggestedFilename());
});

await kit.test("entities: a reader sees everything and can change nothing", async () => {
  const p = rd.page;
  await p.goto(cu("/entities"));
  ok((await p.locator("body").innerText()).includes("read only"), "no read-only note");
  eq(await p.locator("#add").count(), 0, "an add form for a reader");
  eq(await p.getByRole("button", { name: /Export as Markdown/ }).count(), 0, "an export button for a reader");
  await p.goto(cu("/e/" + maire));
  eq(await p.locator("#details").count(), 0, "an edit form for a reader");
  eq(await p.locator("form[action*='/ui/entities/']").count(), 0, "a form for a reader");
  ok((await p.locator("body").innerText()).includes("Máire Nolan"), "the sheet is not readable");
  await p.goto(cu("/fields"));
  eq(await p.locator("#fields form").count(), 0, "a field form for a reader");
});

await kit.test("entities: a live change refreshes the list without touching a form, and the sheet only says it changed", async () => {
  await pg.goto(cu("/entities"));
  await pg.locator("#ea-name").fill("half typed");
  await mk("place", "Salt Marsh");
  const fire = (type) => pg.evaluate((t) => document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: t, seq: 1, by_you: false, payload: {} } })), type);
  await fire("entity.created");
  await waitFor(async () => (await pg.locator("#ent-list").innerText()).includes("Salt Marsh"), "the new row to appear");
  eq(await pg.locator("#ea-name").inputValue(), "half typed", "the form being typed in was touched");
  // An event that is mine also refreshes the list: it may come from another tab of mine, and nothing typed is touched.
  await mk("place", "Salt Flats");
  await pg.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: "entity.created", by_you: true } })));
  await waitFor(async () => (await pg.locator("#ent-list").innerText()).includes("Salt Flats"), "my own event to refresh the list");
  eq(await pg.locator("#ea-name").inputValue(), "half typed", "the form being typed in was touched by my own event");
  // On a sheet.
  await pg.goto(cu("/e/" + maire));
  await pg.locator("#es-summary").fill("typing now");
  await fire("entity.updated");
  await pg.locator("#ent-changed button").waitFor();
  eq(await pg.locator("#es-summary").inputValue(), "typing now", "the sheet's text was replaced");
  ok((await pg.locator("#ent-changed").innerText()).includes("changed elsewhere"), "no note");
  eq(await pg.locator("#ent-changed").getAttribute("aria-live"), "polite", "the note is not announced");
  await fire("document.updated");
});

await kit.test("entities: at 390 px nothing scrolls sideways, controls are 44 px, and the tab bar leads here", async () => {
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  const p = phone.page;
  const dup = await mk("character", "Ciara Phone");
  for (const [path, ready] of [["/entities", "#ent-list"], ["/e/" + maire, "#sheet"], ["/e/" + dup.id + "/merge?into=" + maire, "#merge-form"], ["/fields", "#fields"]]) {
    await p.goto(cu(path));
    await p.locator(ready).waitFor();
    const m = await p.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: window.innerWidth }));
    ok(m.sw <= m.iw, `${path} scrolls sideways: ${m.sw} > ${m.iw}`);
    const small = await p.evaluate(() => [...document.querySelectorAll(".ents .btn, .ents input:not([type=hidden]):not([type=radio]):not([type=checkbox]), .ents select, .ents textarea, .ents summary, .ent-kinds a, .ents .radio, .ents .check, .ent-name a")]
      .filter((e) => e.offsetParent !== null)
      .map((e) => ({ t: e.tagName + "." + e.className + " " + (e.textContent || e.name || "").trim().slice(0, 24), h: e.getBoundingClientRect().height }))
      .filter((x) => x.h < 43.5).map((x) => `${x.t}:${Math.round(x.h)}`));
    eq(small.length, 0, `${path}: controls under 44 px: ${small.join(", ")}`);
  }
  await p.goto(cu("/entities"));
  eq(await p.locator(".tabbar .tab[aria-current=page]").count(), 1, "the tab bar's current tab");
  await phone.ctx.close();
});

await kit.finish("e2e-entities");
