#!/usr/bin/env node
// End-to-end test of the Compendium pages in a real Chromium: the library's Compendiums section, a Compendium's
// overview with its people and references, the project's Compendium panel and people page with the "also add to the
// Compendium" choice, the typed-name delete, a reader's read-only view, and a phone.
//
//   node scripts/e2e-compendium.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-compendium");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn(); // Wren Writer, who owns everything
kit.main = pg;
// Ada Admin and Rory Reader must have signed in once before anyone can add them by address.
const ada = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "admin@dev.scriobh");
const rory = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "reader@dev.scriobh");

const ADA = "admin@dev.scriobh", RORY = "reader@dev.scriobh";
const errBox = async (page, what) => {
  try { await page.locator("#comp-error").waitFor({ timeout: 8000 }); } catch { throw new Error(`no answer for ${what}; the page says: ` + (await page.locator("#main").innerText()).replace(/\s+/g, " ").slice(0, 300)); }
};
const alert = async (page) => (await page.locator("[role=alert]").first().innerText()).trim();
const project = async (title, ctx = main) => (await api(ctx, "POST", "/api/v1/projects", { title, kind: "novel" })).json.item;
const compendium = async (name, ctx = main) => (await api(ctx, "POST", "/api/v1/compendiums", { name })).json.item;
const dialogReady = async (page) => { await page.locator("dialog#dialog[open]").waitFor(); await sleep(300); }; // a press inside htmx's 20 ms settle gap would send the plain form
const status = async (page, path) => (await page.goto(base() + path, { waitUntil: "load" })).status();
const noSideways = async (page, label) => {
  const m = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: window.innerWidth }));
  ok(m.sw <= m.iw, `${label} scrolls sideways: ${m.sw} > ${m.iw}`);
};

let bible, salt, tides;

// Removing a person asks first (the shared confirmation): the button names the person, the panel's confirming button is off for half a second.
const removeAda = async (pg) => {
  await pg.getByRole("button", { name: "Remove Ada Admin from the project" }).click();
  await pg.locator(".arm .arm-ok").waitFor();
  await pg.waitForTimeout(700);
  await pg.locator(".arm .arm-ok").click();
};

await kit.test("standalone Compendium: the empty section, the dialog, the overview and the owner's words", async () => {
  await pg.goto(`${base()}/compendiums`);
  ok((await pg.locator("body").innerText()).includes("No Compendiums yet"), "the empty section says nothing");
  await pg.getByRole("link", { name: "New Compendium" }).first().click();
  await dialogReady(pg);
  await pg.locator("#cn-name").fill("The Salt Road Bible");
  await pg.getByRole("button", { name: "Create Compendium" }).click();
  await pg.waitForURL(/\/c\/the-salt-road-bible/);
  bible = (await api(main, "GET", "/api/v1/compendiums")).json.items.find((c) => c.name === "The Salt Road Bible");
  ok(bible, "the Compendium was not created");
  const body = await pg.locator("#comp-page").innerText();
  ok(body.includes("Owner.") && body.includes("You own this Compendium"), "the owner is not told the role in words: " + body.slice(0, 200));
  ok(body.includes("No project uses it yet"), "a standalone Compendium does not say that no project uses it");
  // A blank name in the dialog is answered in place and nothing is created.
  kit.allowed = [/status of 422/];
  await pg.goto(`${base()}/compendiums`);
  await pg.getByRole("link", { name: "New Compendium" }).first().click();
  await dialogReady(pg);
  await pg.locator("#cn-name").evaluate((i) => i.removeAttribute("required"));
  await pg.locator("#cn-name").fill("  ");
  await pg.getByRole("button", { name: "Create Compendium" }).click();
  await pg.locator("#compendium-new-form [role=alert]").first().waitFor();
  eq((await api(main, "GET", "/api/v1/compendiums")).json.items.length, 1, "a blank name made a Compendium");
});

await kit.test("people: one answer for an address that cannot be added, role words, keyboard-only add", async () => {
  kit.allowed = [/status of 422/];
  await pg.goto(`${base()}/c/${bible.slug}`);
  const hint = await pg.locator("#am-hint").innerText();
  ok(/Editor/.test(hint) && /Reader/.test(hint) && /separate from access to any project/.test(hint), "the role words are missing: " + hint);
  // By keyboard alone: the address, Tab to the role, Tab to Add, Enter. The instance reader cannot be an editor, so this is refused.
  await pg.locator("#am-email").focus();
  await pg.keyboard.type(RORY);
  await pg.keyboard.press("Tab");
  eq(await pg.evaluate(() => document.activeElement.id), "am-role", "Tab after the address does not reach the role");
  await pg.keyboard.press("Tab");
  eq(await pg.evaluate(() => document.activeElement.textContent.trim()), "Add", "Tab after the role does not reach Add");
  await pg.keyboard.press("Enter");
  await pg.locator("#comp-error").waitFor();
  const refused = await alert(pg);
  // An address nobody has gets the very same sentence.
  await pg.locator("#am-email").fill("nobody@example.invalid");
  await pg.getByRole("button", { name: "Add", exact: true }).click();
  await pg.locator("#comp-error").waitFor();
  eq(await alert(pg), refused, "an unknown address is answered differently");
  ok(!/nobody@|instance|reader/i.test(refused), "the answer says why: " + refused);
  // The same keystrokes with the role moved to Reader add Rory.
  await pg.locator("#am-email").fill(RORY);
  await pg.locator("#am-email").focus();
  await pg.keyboard.press("Tab");
  await pg.keyboard.press("r"); // type-ahead on the role list: a closed select takes letters without opening
  eq(await pg.locator("#am-role").inputValue(), "reader", "typing R did not choose Reader");
  await pg.keyboard.press("Tab");
  await pg.keyboard.press("Enter");
  await pg.getByRole("status").filter({ hasText: /now has access/ }).first().waitFor();
  ok((await pg.locator(".people").innerText()).includes("Rory Reader"), "Rory is not in the list");
});

await kit.test("references: reference another Compendium and see the one-level rule", async () => {
  tides = await compendium("Tides and Harbours");
  await pg.goto(`${base()}/c/${bible.slug}`);
  ok((await pg.locator("#refs").innerText()).includes("one level only"), "the one-level rule is not explained");
  await pg.locator("#ra-target").selectOption({ label: "Tides and Harbours" });
  await pg.getByRole("button", { name: "Reference it" }).click();
  await pg.getByRole("button", { name: "Stop referencing Tides and Harbours" }).waitFor();
  await pg.getByRole("button", { name: "Stop referencing Tides and Harbours" }).click();
  await pg.getByText("It references no other Compendium.").waitFor();
});

await kit.test("project: create it, preview, make the Compendium primary, change it, take it away", async () => {
  salt = await project("The Salt Road");
  await pg.goto(`${base()}/p/${salt.slug}/compendium`);
  ok((await pg.locator("#primary").innerText()).includes("no primary Compendium yet"), "no empty state");
  ok((await pg.locator("body").innerText()).includes("does not put anyone on the Compendium") || (await pg.locator("body").innerText()).includes("Being on this project does not put anyone on the Compendium"), "the page does not say that project access is not the bible");
  await pg.locator("#pc-set").selectOption({ label: "The Salt Road Bible" });
  await pg.getByRole("button", { name: "See what would change" }).click();
  await pg.locator("#preview").waitFor();
  const preview = await pg.locator("#preview").innerText();
  ok(/appear/.test(preview) && /override/.test(preview), "the preview does not count what enters and which overrides stay: " + preview);
  eq((await api(main, "GET", `/api/v1/projects/${salt.id}/compendium`)).json.item.has_primary, false, "the preview changed the project");
  await pg.getByRole("button", { name: "Make it the primary" }).click();
  await pg.getByRole("link", { name: "The Salt Road Bible" }).first().waitFor();
  eq((await api(main, "GET", `/api/v1/projects/${salt.id}/compendium`)).json.item.has_primary, true, "the primary was not set");
  // The name leads to the Compendium's page.
  await pg.locator("#primary").getByRole("link", { name: "The Salt Road Bible" }).click();
  await pg.waitForURL(new RegExp(`/c/${bible.slug}$`));
  ok((await pg.locator("#projects").innerText()).includes("The Salt Road"), "the Compendium does not list the project");
  // Reference a whole Compendium from the project, and see the page without the owner's controls when it is not the owner's.
  await pg.goto(`${base()}/p/${salt.slug}/compendium`);
  await pg.locator("#pr-target").selectOption({ label: "Tides and Harbours" });
  await pg.getByRole("button", { name: "Reference it" }).click();
  await pg.getByRole("button", { name: "Stop referencing Tides and Harbours" }).waitFor();
  await pg.getByRole("button", { name: "Stop referencing Tides and Harbours" }).click();
  await pg.getByText("No other Compendium is referenced.").waitFor();
  // A second project makes its own Compendium in one step.
  const second = await project("Hedgerow");
  await pg.goto(`${base()}/p/${second.slug}/compendium`);
  await pg.locator("summary", { hasText: "Make a new Compendium for this project" }).click();
  await pg.getByRole("button", { name: "Create it" }).click();
  await pg.locator("#primary .bigline a").waitFor();
  eq((await pg.locator("#primary .bigline a").innerText()).trim(), "Hedgerow", "the new Compendium is not the project's primary");
  // Taking it away shows a preview first.
  await pg.getByRole("button", { name: /Take the primary away/ }).click();
  await pg.locator("#preview").waitFor();
  ok(/stop/.test(await pg.locator("#preview").innerText()), "taking the primary away shows no preview");
  await pg.getByRole("button", { name: "Take it away" }).click();
  await pg.getByText("This project has no primary Compendium yet.").waitFor();
});

await kit.test("project people: the Compendium prompt both ways, and what was granted", async () => {
  kit.allowed = [/status of (403|404|422)/];
  await pg.goto(`${base()}/p/${salt.slug}/people`);
  // No: the person is on the project alone, and the page says so.
  await pg.locator("#pa-email").fill(ADA);
  await pg.getByLabel(/No, the project only/).check();
  await pg.getByRole("button", { name: "Add to the project" }).click();
  await pg.locator("#grant-result").waitFor();
  ok((await pg.locator("#grant-result").innerText()).includes("not added to the Compendium, because that was not asked for"), "the result does not say why: " + await pg.locator("#grant-result").innerText());
  eq(await status(ada.page, `/c/${bible.slug}`), 404, "a person added to the project alone can open the Compendium");
  await pg.goto(`${base()}/p/${salt.slug}/people`);
  await removeAda(pg);
  await pg.getByText("Ada Admin", { exact: false }).waitFor({ state: "detached" }).catch(() => {});
  // Ask by role (the default): an editor is added to the Compendium too, and the page says what was granted.
  await pg.locator("#pa-email").fill(ADA);
  eq(await pg.getByLabel(/Ask by role/).isChecked(), true, "the default is not 'ask by role'");
  await pg.getByRole("button", { name: "Add to the project" }).click();
  await pg.locator("#grant-result").waitFor();
  ok((await pg.locator("#grant-result").innerText()).includes("also added to the Compendium as an editor"), "the result does not say what was granted");
  eq(await status(ada.page, `/c/${bible.slug}`), 200, "the editor cannot open the Compendium she was added to");
  ok((await pg.locator(".people").innerText()).includes("On the Compendium: editor"), "the list does not show the Compendium role");
  // A reader by default is not added to the Compendium.
  await pg.locator("#pa-email").fill(RORY);
  await pg.locator("#pa-role").selectOption("reader");
  await pg.getByRole("button", { name: "Add to the project" }).click();
  await pg.locator("#grant-result").waitFor();
  ok((await pg.locator("#grant-result").innerText()).includes("not added to the Compendium"), "a reader by default was told something else");
  // An address that cannot be added gets the same sentence as on a Compendium.
  await pg.locator("#pa-email").fill("nobody@example.invalid");
  await pg.getByRole("button", { name: "Add to the project" }).click();
  await errBox(pg, "an unknown address on the project");
  const none = await alert(pg);
  await pg.goto(`${base()}/c/${bible.slug}`);
  await pg.locator("#am-email").fill("nobody@example.invalid");
  await pg.getByRole("button", { name: "Add", exact: true }).click();
  await errBox(pg, "an unknown address on the Compendium");
  eq(await alert(pg), none, "the project and the Compendium answer an unknown address differently");
  // A person who owns a project but only edits the Compendium cannot give access to it: asking outright is refused,
  // and the default is to add them to the project alone and say so. (An instance administrator counts as an owner of
  // every Compendium, so Ada owns the Compendium and Wren, who is not an administrator, edits it.)
  const adas = await compendium("Ada's World", ada.ctx);
  eq((await api(ada.ctx, "POST", `/api/v1/compendiums/${adas.id}/members`, { email: "writer@dev.scriobh", role: "editor" })).status, 201, "Wren could not be made an editor of Ada's World");
  const borrowed = await project("Borrowed book");
  eq((await api(main, "PUT", `/api/v1/projects/${borrowed.id}/compendium`, { compendium_id: adas.id })).status, 200, "an editor could not make it primary");
  await pg.goto(`${base()}/p/${borrowed.slug}/people`);
  await pg.locator("#pa-email").fill(RORY);
  await pg.locator("#pa-role").selectOption("reader");
  await pg.getByLabel(/Yes, also add them/).check();
  await pg.getByRole("button", { name: "Add to the project" }).click();
  await errBox(pg, "an explicit Compendium request by a non-owner");
  ok(/owner/i.test(await alert(pg)), "the refusal is not in the op's words: " + (await alert(pg)));
  ok(!(await pg.locator(".people").innerText()).includes("Rory Reader"), "the person was added although the request was refused");
  await pg.getByLabel(/Ask by role/).check();
  await pg.locator("#pa-role").selectOption("editor");
  await pg.locator("#pa-email").fill(ADA);
  await pg.getByRole("button", { name: "Add to the project" }).click();
  await pg.locator("#grant-result").waitFor();
  ok((await pg.locator("#grant-result").innerText()).includes("Only the Compendium's owner can give access to it"), "the default for a non-owner does not say why nothing was granted: " + await pg.locator("#grant-result").innerText());
  // Change a Compendium role from the Compendium page, then take the person off both.
  await pg.goto(`${base()}/c/${bible.slug}`);
  const row = pg.locator(".people li", { hasText: "Ada Admin" });
  await row.locator("select").selectOption("reader");
  await row.getByRole("button", { name: "Change the role of Ada Admin" }).click();
  await pg.waitForURL(/saved=/);
  eq(await pg.locator(".people li", { hasText: "Ada Admin" }).locator("select").inputValue(), "reader", "the role did not change");
  // Taking her off the project says which Compendium role she keeps.
  await pg.goto(`${base()}/p/${salt.slug}/people`);
  await removeAda(pg);
  await pg.locator("#grant-result").waitFor();
  ok((await pg.locator("#grant-result").innerText()).includes("still have access to the Compendium as a reader"), "removal does not say which Compendium role remains");
  await pg.goto(`${base()}/c/${bible.slug}`);
  await pg.getByRole("button", { name: "Take Ada Admin off this Compendium" }).click();
  await pg.waitForURL(/saved=/);
  eq(await status(ada.page, `/c/${bible.slug}`), 404, "a removed member still opens the Compendium");
});

await kit.test("states: archive, trash, restore and delete for good with the name typed", async () => {
  kit.allowed = [/status of 404/];
  const w = await compendium("Scratch World");
  await api(main, "PUT", `/api/v1/projects/${salt.id}/compendium`, { compendium_id: w.id });
  await pg.goto(`${base()}/compendiums`);
  await pg.getByRole("button", { name: "Archive Scratch World" }).click();
  await pg.waitForURL(/saved=archive/);
  ok(!(await pg.locator(".ccards").innerText()).includes("Scratch World"), "an archived Compendium is still in the active list");
  await pg.getByRole("link", { name: "Archived" }).last().click();
  await pg.waitForURL(/state=archived/);
  await pg.getByRole("button", { name: "Bring Scratch World back to the list" }).click();
  await pg.waitForURL(/saved=unarchive/);
  await pg.goto(`${base()}/compendiums`);
  await pg.getByRole("button", { name: "Move Scratch World to the trash" }).click();
  await pg.waitForURL(/saved=trash/);
  await pg.goto(`${base()}/compendiums?state=trashed`);
  await pg.getByRole("button", { name: "Restore Scratch World from the trash" }).click();
  await pg.waitForURL(/saved=restore/);
  await pg.goto(`${base()}/compendiums`);
  await pg.getByRole("button", { name: "Move Scratch World to the trash" }).click();
  await pg.waitForURL(/saved=trash/);
  await pg.goto(`${base()}/compendiums?state=trashed`);
  await pg.getByRole("link", { name: "Delete Scratch World for good" }).click();
  await dialogReady(pg);
  const dialog = pg.locator("dialog#dialog[open]");
  ok((await dialog.innerText()).includes("The Salt Road"), "the dialog does not name the project that loses it");
  const submit = dialog.getByRole("button", { name: "Delete forever" });
  eq(await submit.isDisabled(), true, "the delete button is on before the name is typed");
  await dialog.locator("#cp-name").fill("scratch world");
  eq(await submit.isDisabled(), true, "the delete button accepts a wrong name");
  await dialog.locator("#cp-name").fill("Scratch World");
  eq(await submit.isDisabled(), false, "the delete button stays off for the right name");
  await submit.click();
  await waitFor(async () => !(await api(main, "GET", "/api/v1/compendiums?state=trashed")).json.items.some((c) => c.id === w.id), "the Compendium to be deleted");
  eq((await api(main, "GET", `/api/v1/projects/${salt.id}/compendium`)).json.item.has_primary, false, "the project kept a deleted primary");
});

await kit.test("a reader sees everything and can change nothing", async () => {
  kit.allowed = [/status of (403|404)/];
  const r = rory.page;
  await r.goto(`${base()}/c/${bible.slug}`);
  const body = await r.locator("#comp-page").innerText();
  ok(body.includes("You cannot change anything in it"), "the reader is not told what they can do");
  for (const name of ["Give someone access", "Name and description", "Archive or delete"]) ok(!body.includes(name), `the reader sees "${name}"`);
  eq(await r.locator("#comp-page form").count(), 0, "the reader has a form to submit");
  eq(await r.getByRole("link", { name: /Export/ }).count(), 0, "the reader is offered the export");
  await r.goto(`${base()}/compendiums`);
  ok((await r.locator(".ccards").innerText()).includes("You can read"), "the list does not say the reader's role");
  eq(await r.locator(".ccard-actions").count(), 0, "the reader has card actions");
  // A forged request is refused as well.
  const forged = await r.evaluate(async (id) => {
    const csrf = document.querySelector('meta[name="csrf-token"]').content;
    const res = await fetch(`/ui/compendiums/${id}/archive`, { method: "POST", headers: { "X-CSRF-Token": csrf, "Content-Type": "application/x-www-form-urlencoded" }, body: "" });
    return res.status;
  }, bible.id);
  eq(forged, 403, "a reader's forged archive request");
});

await kit.test("phone: 390 px, nothing sideways, targets at least 44 px", async () => {
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  const page = phone.page;
  await compendium("Tides again");
  for (const path of ["/compendiums", `/c/${bible.slug}`, `/p/${salt.slug}/compendium`, `/p/${salt.slug}/people`]) {
    await page.goto(base() + path, { waitUntil: "load" });
    await noSideways(page, path);
    const small = await page.evaluate(() => [...document.querySelectorAll("#main .comp .btn, #main .comp input:not([type=hidden]):not([type=checkbox]):not([type=radio]), #main .comp select, #main .comp summary, #main .comp textarea")]
      .filter((e) => e.getClientRects().length)
      .map((e) => ({ n: e.tagName + "." + (e.className || e.id || e.name) + " " + (e.getAttribute("aria-label") || e.textContent || "").trim().slice(0, 24), h: Math.round(e.getBoundingClientRect().height), w: Math.round(e.getBoundingClientRect().width) }))
      .filter((t) => t.h < 44 || (t.w < 44)));
    ok(small.length === 0, `${path}: controls under 44 px: ` + small.map((t) => `${t.n} ${t.w}x${t.h}`).join("; "));
  }
  await phone.ctx.close();
});

await kit.finish("e2e-compendium");
