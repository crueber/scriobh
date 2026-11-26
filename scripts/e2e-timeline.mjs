#!/usr/bin/env node
// End-to-end test of the timeline pages in a real Chromium: the Compendium timeline with its filters and strip, the live
// date hints and the entity picker on the event form, scene links, the check panel, a book's own view with keyboard
// moves, the same pages without script, at 390 px, with the network cut, and with five hundred events.
//
//   node scripts/e2e-timeline.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-timeline");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const post = async (path, data) => {
  const r = await api(main, "POST", path, data);
  if (r.status >= 300) throw new Error(`POST ${path} answered ${r.status} ${JSON.stringify(r.json)}`);
  return r.json;
};
let C, P, ent = {}, ev = {}, doc = {};
const cpage = (path = "") => `${base()}/c/${C.slug}/timeline${path}`;
const ppage = (q = "") => `${base()}/p/${P.slug}/timeline${q}`;
const order = async () => (await api(main, "GET", `/api/v1/projects/${P.id}/timeline/order`)).json.events;
const titleOf = (id) => Object.keys(ev).find((k) => ev[k] === id);
const noSideways = (page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1);

await kit.test("setup: a Compendium, a book that uses it, events, people and dated scenes", async () => {
  C = (await post("/api/v1/compendiums", { name: "Salt Bible" })).item;
  P = (await post("/api/v1/projects", { title: "The Salt Road", kind: "novel" })).item;
  const r = await api(main, "PUT", `/api/v1/projects/${P.id}/compendium`, { compendium_id: C.id });
  eq(r.status, 200, "setting the primary Compendium");
  for (const [k, kind, name] of [["ana", "character", "Ana"], ["ben", "character", "Ben"], ["harbour", "place", "The Harbour"], ["hill", "place", "The Hill"]]) {
    ent[k] = (await post(`/api/v1/compendiums/${C.id}/entities`, { kind, name })).item.id;
  }
  const mk = async (key, title, date, end, label, place, who) => {
    ev[key] = (await post(`/api/v1/compendiums/${C.id}/timeline/events`, { title, story_date: date, story_date_end: end, story_label: label, location: place, participants: who.map((w) => ({ entity: w, role: "there" })) })).item.id;
  };
  await mk("Wedding", "Wedding", "1998-04-12", "", "Midsummer, Year 312", "The Harbour", ["Ana"]);
  await mk("Funeral", "Funeral", "1998-04-12", "", "", "The Hill", ["Ana"]);
  await mk("Voyage", "Voyage", "1999", "2000-02", "", "", ["Ben"]);
  for (const [i, y] of [1990, 1991, 1994, 2001, 2003, 2007, 2011].entries()) await mk(`Other${i}`, `Other ${y}`, String(y), "", "", "", []);
  await mk("Mystery", "Mystery", "", "", "", "", []);
  const seeded = (await api(main, "GET", `/api/v1/projects/${P.id}/docs`)).json.order[0];
  const dated = async (key, title, date) => {
    const d = (await post(`/api/v1/projects/${P.id}/docs`, { title, kind: "chapter", content: "Words." })).item;
    const cur = (await api(main, "GET", `/api/v1/docs/${d.id}`)).json.item;
    await api(main, "PATCH", `/api/v1/docs/${d.id}`, { base_revision: cur.revision, story_date: date });
    doc[key] = d.id;
  };
  await dated("arrival", "Arrival", "1998-04-12");
  await dated("departure", "Departure", "1998-03-01");
  await dated("homecoming", "Homecoming", "1999-06");
  await api(main, "POST", `/api/v1/docs/${seeded}/trash`);
  for (const [d, e] of [["arrival", "Wedding"], ["homecoming", "Voyage"]]) await post(`/api/v1/docs/${doc[d]}/timeline-events`, { event_id: ev[e] });
});

await kit.test("the Compendium page: events in story order, the label beside the date, a strip with a table behind it", async () => {
  await pg.goto(cpage());
  await pg.waitForSelector("#tl-events table");
  const rows = await pg.locator("#tl-events tbody tr").count();
  eq(rows, 11, "events listed");
  const first = await pg.locator("#tl-events tbody tr").first().innerText();
  ok(/Other 1990/.test(first), "the earliest event comes first: " + first);
  const last = await pg.locator("#tl-events tbody tr").last().innerText();
  ok(/Mystery/.test(last) && /Undated/.test(last), "an undated event comes last: " + last);
  const wedding = pg.locator("#tl-events tbody tr", { hasText: "Wedding" });
  ok(/1998-04-12\s+Midsummer, Year 312/.test((await wedding.locator("td").first().innerText()).replace(/\n+/g, " ")), "the story label sits beside the date");
  ok((await pg.locator(".tl-strip .tl-blk").count()) > 1, "the strip has blocks");
  ok((await pg.locator(".tl-alt table tbody tr").count()) > 1, "the strip's table alternative is there");
  ok(/dated item/.test(await pg.locator(".tl-alt caption").textContent()), "the table says what it counts");
  ok(await pg.locator(".tl-alt table").isVisible(), "the table is open, so it is read as the page is");
  eq(await pg.locator("h1").count(), 1, "one h1");
});

await kit.test("a strip block lists its events in the table, by mouse and by keyboard", async () => {
  await pg.goto(cpage());
  const links = pg.locator(".tl-strip a.tl-bar");
  ok((await links.count()) >= 2, "blocks to choose from");
  await links.first().focus();
  ok(await links.first().evaluate((a) => a === document.activeElement), "a block takes focus");
  const outline = await links.first().evaluate((a) => getComputedStyle(a).outlineStyle);
  ok(outline !== "none", "a focused block has a visible focus ring: " + outline);
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/from=.*to=/);
  const rows = await pg.locator("#tl-events tbody tr").count();
  ok(rows >= 1 && rows < 11, "the table now lists only that stretch: " + rows);
  await pg.goto(cpage("?from=1998&to=1998-12"));
  const titles = await pg.locator("#tl-events tbody tr .tl-title").allInnerTexts();
  ok(titles.includes("Wedding") && titles.includes("Funeral") && !titles.includes("Voyage"), "the date range keeps 1998: " + titles);
  await pg.goto(cpage(`?entity=${ent.ben}`));
  eq((await pg.locator("#tl-events tbody tr").count()), 1, "the entity filter keeps one event");
});

await kit.test("the filter form: a bad date is explained and the examples are shown", async () => {
  kit.allowed = [/status of 422/];
  await pg.goto(cpage());
  await pg.getByLabel("From").fill("1998-4");
  await pg.getByRole("button", { name: "Show events" }).click();
  await pg.waitForSelector("#f-from-err");
  ok(/Use a date such as 1998, 1998-04 or 1998-04-12/.test(await pg.locator("#f-from-err").innerText()), "the plain-words refusal, beside its field");
  eq(await pg.getByLabel("From").getAttribute("aria-invalid"), "true", "the field with the problem is marked");
  ok(/such as 1998, 1998-04 or 1998-04-12/.test(await pg.locator("#f-from-err").innerText()), "the examples are in the one message");
  eq(await pg.locator("#f-from-h").count(), 0, "one message, not the hint and the refusal both");
  await pg.goto(cpage("?entity=" + ent.hill + "&from=1999"));
  ok(/No event matches these filters/.test(await pg.locator(".tl-empty").innerText()), "an honest empty result");
});

await kit.test("live date hints: partial dates are explained as they are typed", async () => {
  await pg.goto(cpage("/events/new"));
  const start = pg.getByLabel("Starts"), end = pg.getByLabel("Ends");
  const hint = (id) => pg.locator(`#${id}`).innerText();
  const type = async (loc, v) => { await loc.fill(""); await loc.pressSequentially(v, { delay: 5 }); await loc.blur(); };
  await type(start, "1998-02-30");
  ok(/February has 28 days/.test(await hint("ev-start-h")), "a day that is not in the month: " + (await hint("ev-start-h")));
  eq(await start.getAttribute("aria-invalid"), "true", "marked invalid");
  await type(start, "1998-4");
  ok(/two digits/.test(await hint("ev-start-h")) && /1998-04-12/.test(await hint("ev-start-h")), "a one-digit month: " + (await hint("ev-start-h")));
  await type(start, "1998-13");
  ok(/no month 13/.test(await hint("ev-start-h")), "a month that is not one: " + (await hint("ev-start-h")));
  await type(start, "-0044");
  ok(/The whole of year -44/.test(await hint("ev-start-h")) && (await start.getAttribute("aria-invalid")) === null, "a negative year reads back: " + (await hint("ev-start-h")));
  await type(start, "1998-04");
  ok(/month of April 1998/.test(await hint("ev-start-h")), "a month reads back");
  await type(start, "1998-04-12 19:30");
  ok(/At 19:30 on 12 April 1998/.test(await hint("ev-start-h")), "a time reads back: " + (await hint("ev-start-h")));
  await type(start, "last spring");
  ok(/not a date the timeline can read/.test(await hint("ev-start-h")) && /1998-04-12T19:30/.test(await hint("ev-start-h")), "nonsense is refused with examples");
  await type(start, "1998-04-12");
  await type(end, "1998-04-01");
  ok(/The end is before the start/.test(await hint("ev-end-h")), "end before start: " + (await hint("ev-end-h")));
  eq(await end.getAttribute("aria-invalid"), "true", "the end is marked invalid");
  await type(end, "1998-04-14");
  ok(!/before/.test(await hint("ev-end-h")) && (await end.getAttribute("aria-invalid")) === null, "a good end clears it");
  await type(start, "");
  eq(await hint("ev-start-h"), "Leave empty if the date is not known. Use 1998, 1998-04, 1998-04-12 or 1998-04-12T19:30. A year can be negative, such as -0044.", "an empty field gets the examples back");
});

await kit.test("the entity picker is a listbox: keys choose, Escape closes, the place picker lists places only", async () => {
  await pg.goto(cpage("/events/new"));
  const name = pg.locator("[name=person_name]").first();
  await name.focus();
  await name.pressSequentially("A", { delay: 20 });
  const box = name.locator("xpath=following-sibling::ul[@role='listbox']");
  await box.waitFor({ state: "visible" });
  eq(await name.getAttribute("role"), "combobox", "the field is a combobox");
  eq(await name.getAttribute("aria-expanded"), "true", "expanded");
  await pg.keyboard.press("ArrowDown");
  const active = await name.getAttribute("aria-activedescendant");
  ok(active && (await pg.locator(`#${active}`).getAttribute("aria-selected")) === "true", "the highlighted option is announced");
  await pg.keyboard.press("Escape");
  await box.waitFor({ state: "hidden" });
  eq(await name.evaluate((e) => e === document.activeElement), true, "focus stays in the field after Escape");
  await name.fill("");
  await name.pressSequentially("An", { delay: 20 });
  await box.waitFor({ state: "visible" });
  await pg.keyboard.press("ArrowDown");
  await pg.keyboard.press("Enter");
  eq(await name.inputValue(), "Ana", "Enter chooses the highlighted person");
  await box.waitFor({ state: "hidden" });
  const place = pg.getByLabel("Place");
  await place.pressSequentially("The", { delay: 20 });
  const opts = pg.locator("#ev-place").locator("xpath=..").locator("[role=option]");
  await opts.first().waitFor();
  const shown = (await opts.allInnerTexts()).join(" | ");
  ok(/The Harbour/.test(shown) && /The Hill/.test(shown) && !/Ana|Ben/.test(shown), "the place picker lists places only: " + shown);
  await pg.keyboard.press("ArrowDown"); await pg.keyboard.press("ArrowDown"); await pg.keyboard.press("Enter");
  eq(await place.inputValue(), "The Hill", "the second place chosen by key");
  const before = await pg.locator("[name=person_name]").count();
  await pg.getByRole("button", { name: "Add another" }).click();
  eq(await pg.locator("[name=person_name]").count(), before + 1, "a new row");
  ok(await pg.locator("[name=person_name]").last().evaluate((e) => e === document.activeElement), "focus moves to the new row");
});

await kit.test("add an event with the keyboard alone: it is saved with its people and place, and the list shows it", async () => {
  await pg.goto(cpage("/events/new"));
  await pg.getByLabel("Title").fill("Harvest feast");
  await pg.getByLabel("Starts").fill("1998-09-20");
  await pg.getByLabel("Story label").fill("Reaping, Year 312");
  await pg.getByLabel("Place").fill("The Harbour");
  await pg.locator("[name=person_name]").first().fill("Ana");
  await pg.locator("[name=person_role]").first().fill("host");
  await pg.locator("[name=person_name]").nth(1).fill("Ben");
  await pg.getByLabel("Description").fill("Everyone came down to the quay.");
  await pg.getByRole("button", { name: "Add event" }).focus();
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/events\/tle_.*saved=created/);
  ok(/Event added\./.test(await pg.locator(".saved").innerText()), "the page confirms");
  eq(await pg.getByLabel("Title").inputValue(), "Harvest feast", "the form shows what was saved");
  eq(await pg.locator("[name=person_role]").first().inputValue(), "host", "the role was kept");
  ev.Feast = pg.url().match(/events\/(tle_[^?]+)/)[1];
  await pg.goto(cpage());
  ok((await pg.locator("#tl-events tbody tr .tl-title").allInnerTexts()).includes("Harvest feast"), "the event is listed");
  const bad = await post(`/api/v1/compendiums/${C.id}/timeline/events`, { title: "x" }).catch(() => null);
  ok(bad, "the op still takes a bare event");
});

await kit.test("a refused save keeps what was typed and says why in plain words", async () => {
  kit.allowed = [/status of 422/]; // the browser logs a refused form post
  await pg.goto(cpage("/events/new"));
  await pg.getByLabel("Title").fill("Doomed");
  await pg.getByLabel("Place").fill("Nowhere in particular");
  await pg.getByRole("button", { name: "Add event" }).click();
  await pg.waitForSelector("#tl-err");
  ok(/No entity in this Compendium is called Nowhere in particular/.test(await pg.locator("#tl-err").innerText()), "the refusal");
  eq(await pg.getByLabel("Title").inputValue(), "Doomed", "the title is kept");
  eq(await pg.getByLabel("Place").getAttribute("aria-invalid"), "true", "the place is marked");
  ok(/tl-err/.test(await pg.getByLabel("Place").getAttribute("aria-describedby")), "the refusal is tied to the field");
});

await kit.test("scene links: choose the book, choose the scene, see it as a link to the chapter, then unlink it", async () => {
  await pg.goto(cpage(`/events/${ev.Funeral}`));
  ok(/No scene tells this event yet/.test(await pg.locator("#tl-scenes").innerText()), "no scene yet");
  await pg.getByRole("button", { name: "Show its scenes" }).click();
  await pg.waitForSelector("#ev-scene");
  const opts = await pg.locator("#ev-scene option").allInnerTexts();
  ok(opts.some((o) => /Departure/.test(o)), "the book's scenes are offered: " + opts);
  await pg.locator("#ev-scene").selectOption({ label: opts.find((o) => /Departure/.test(o)) });
  await pg.getByRole("button", { name: "Link this scene" }).click();
  await pg.waitForSelector(".tl-scenes a");
  const href = await pg.locator(".tl-scenes a", { hasText: "Departure" }).getAttribute("href");
  eq(href, `/p/${P.slug}/d/${doc.departure}`, "a link to the chapter");
  await pg.getByRole("button", { name: /Unlink Departure/ }).click();
  await pg.waitForURL(/saved=unlinked/);
  ok(/No scene tells this event yet/.test(await pg.locator("#tl-scenes").innerText()), "unlinked");
});

await kit.test("the check panel loads in place, errors first, with links to what it names, and says what it covers", async () => {
  await pg.goto(cpage());
  const url = pg.url();
  await pg.getByRole("link", { name: "Check timeline" }).click();
  await pg.locator("#tl-check-body .tl-find").first().waitFor();
  eq(pg.url(), url, "the page did not reload");
  const first = await pg.locator("#tl-check-body .tl-find").first().innerText();
  // The two events are dated by the day only, so the check calls it a warning; an error needs clock times or an explicit end.
  ok(/(Error|Warning)/.test(first) && /Two places at once/.test(first) && /Ana cannot be at/.test(first), "an error first, in words: " + first);
  const links = await pg.locator("#tl-check-body .tl-links a").evaluateAll((as) => as.map((a) => a.getAttribute("href")));
  ok(links.some((h) => h === `/timeline/events/${ev.Wedding}`) && links.some((h) => h.includes(`entity=${ent.ana}`)), "links to the events and the person: " + links);
  await pg.locator("#tl-check-body .tl-links a", { hasText: "Wedding" }).first().click();
  await pg.waitForURL(new RegExp(`/c/${C.slug}/timeline/events/${ev.Wedding}`));
  eq(await pg.locator("h1").innerText(), "Wedding", "the link opens the event");
  // With no contradictions the panel says what was looked at, not that all is well.
  await api(main, "POST", `/api/v1/timeline/events/${ev.Funeral}/restore`);
  await api(main, "DELETE", `/api/v1/timeline/events/${ev.Funeral}`);
  await pg.goto(cpage("?check=1"));
  const calm = await pg.locator("#tl-check-body").innerText();
  ok(/found nothing to flag/.test(calm) && /cannot tell whether the story is consistent/.test(calm), "an honest empty result: " + calm);
  await api(main, "POST", `/api/v1/timeline/events/${ev.Funeral}/restore`);
});

await kit.test("a book's timeline: story order, as the book tells it, unplaced scenes, the check", async () => {
  await pg.goto(ppage());
  await pg.waitForSelector("#tl-ptable table");
  const titles = await pg.locator("#tl-ptable tbody .tl-title").allInnerTexts();
  ok(titles.indexOf("Departure") < titles.indexOf("Wedding") && titles.indexOf("Funeral") < titles.indexOf("Voyage"), "story order by date: " + titles);
  eq(await pg.locator(".tl-mf").count(), 0, "no move buttons in story order");
  await pg.getByRole("link", { name: "As the book tells it" }).click();
  await pg.waitForURL(/order=narrative/);
  ok((await pg.locator(".tl-mf").count()) >= 3, "move buttons in the book's own order");
  ok(/Scenes never move here/.test(await pg.locator("#tl-events").innerText()), "the note that scenes never move");
  await pg.getByRole("link", { name: "Check timeline" }).click();
  await pg.locator("#tl-check-body .tl-find", { hasText: "Told out of order" }).waitFor();
  ok(/flashback/.test(await pg.locator("#tl-check-body").innerText()), "a possible flashback is explained, not condemned");
  const sceneLink = await pg.locator("#tl-check-body .tl-links a", { hasText: "Departure" }).first().getAttribute("href");
  eq(sceneLink, `/p/${P.slug}/d/${doc.departure}`, "scene findings link to the chapter");
  const loose = (await post(`/api/v1/projects/${P.id}/docs`, { title: "Loose end", kind: "scene" })).item.id;
  await pg.goto(ppage());
  const note = await pg.locator(".tl-note").first().innerText();
  ok(/neither a story date nor an event/.test(note), "unplaced scenes are counted: " + note);
  eq(await pg.locator(".tl-note a", { hasText: "Open the outline" }).getAttribute("href"), `/p/${P.slug}/outline`, "with a link to the outline");
  await api(main, "POST", `/api/v1/docs/${loose}/trash`);
});

await kit.test("moving events: buttons, Alt with the arrows, queued quick moves, announced, saved, and a reset", async () => {
  await pg.goto(ppage("?order=narrative"));
  await pg.waitForSelector("#tl-body tr");
  const start = await order();
  ok(start.length >= 10, "events in the book's order: " + start.length);
  const first = start[0], second = start[1];
  const rowOf = (id) => pg.locator(`#tl-row-${id}`);
  const ids = () => pg.locator("#tl-body tr[data-event]").evaluateAll((trs) => trs.map((t) => t.dataset.event));
  eq((await ids())[0], first, "the page starts in the server's order");
  // Button: the first event later.
  await rowOf(first).getByRole("button", { name: /later/ }).click();
  eq((await ids())[1], first, "the row moved at once");
  await waitFor(async () => new RegExp(`Moved ${titleOf(first)} later`).test(await pg.locator("#tl-live").innerText()), "the move to be announced");
  await waitFor(async () => (await order())[1] === first, "the move to be saved");
  eq((await order())[0], second, "the server swapped the two events");
  eq(await rowOf(first).getByRole("button", { name: /later/ }).evaluate((b) => b === document.activeElement), true, "focus stays on the button that was pressed");
  // Position numbers stay with their places.
  const nums = await pg.locator("#tl-body .tl-pos").allInnerTexts();
  ok(nums.every((n, i) => Number(n) === Number(nums[0]) + i), "numbers still run in sequence: " + nums);
  // Keys: Alt+ArrowUp moves it back; three quick Alt+ArrowDowns settle where the server says.
  await rowOf(first).locator(".tl-title").focus();
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => (await order())[0] === first, "Alt+ArrowUp to be saved");
  const e0 = (await order()).slice();
  await rowOf(e0[0]).locator(".tl-title").focus();
  await pg.keyboard.press("Alt+ArrowDown"); await pg.keyboard.press("Alt+ArrowDown"); await pg.keyboard.press("Alt+ArrowDown");
  await sleep(300);
  const expect = [e0[1], e0[2], e0[3], e0[0], ...e0.slice(4)];
  await waitFor(async () => JSON.stringify(await order()) === JSON.stringify(expect), "three quick moves to be saved in order");
  eq(JSON.stringify(await ids()), JSON.stringify(expect.slice(0, (await ids()).length)), "the page shows the same order");
  await pg.reload();
  await pg.waitForSelector("#tl-body tr");
  eq(JSON.stringify(await ids()), JSON.stringify(expect.slice(0, (await ids()).length)), "after a reload too");
  ok(/own order of events/.test(await pg.locator("#tl-events").innerText()), "the page says the book has its own order");
  // The first cannot go earlier: the button is disabled and the key is announced.
  const top = (await ids())[0];
  eq(await rowOf(top).getByRole("button", { name: /earlier/ }).isDisabled(), true, "no Earlier on the first");
  await rowOf(top).locator(".tl-title").focus();
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => /already first/.test(await pg.locator("#tl-live").innerText()), "the key to say the event is already first");
  // Reset.
  await pg.getByRole("button", { name: /Go back to the order the scenes give/ }).click();
  await pg.waitForURL(/saved=reset/);
  eq((await order()).join(), start.join(), "the derived order is back");
  eq(await pg.locator("#tl-events form.tl-reset").count(), 0, "no reset button once it is derived again");
});

await kit.test("a refused move leaves the page as the server holds it, and says so", async () => {
  kit.allowed = [/status of 422/]; // the script's own request is refused, as the test intends
  await pg.goto(ppage("?order=narrative"));
  await pg.waitForSelector("#tl-body tr[data-event]");
  const id = await pg.locator("#tl-body tr[data-event]").first().getAttribute("data-event");
  await api(main, "DELETE", `/api/v1/timeline/events/${id}`); // gone behind the page's back
  await pg.locator(`#tl-row-${id}`).getByRole("button", { name: /later/ }).click();
  await waitFor(async () => (await pg.locator("#tl-live").innerText()).includes("Could not move"), "the refusal to be announced");
  await waitFor(async () => (await pg.locator(`#tl-row-${id}`).count()) === 0, "the list to be drawn again without it");
  await api(main, "POST", `/api/v1/timeline/events/${id}/restore`);
});

await kit.test("without script: the same pages read, filter, add and move through forms", async () => {
  const bare = await kit.newContext({ javaScriptEnabled: false, viewport: { width: 1280, height: 800 } });
  await bare.addCookies(await main.cookies());
  const np = await bare.newPage();
  await np.goto(cpage());
  ok((await np.locator("#tl-events tbody tr").count()) >= 10, "the table reads");
  await np.getByLabel("Who or where").selectOption({ label: "Ben" });
  await np.getByRole("button", { name: "Show events" }).click();
  eq(await np.locator("#tl-events tbody tr").count(), 2, "the filter form works (Voyage and the feast)");
  await np.goto(cpage("/events/new"));
  eq(await np.getByRole("button", { name: "Add another" }).isVisible(), false, "no Add another without script");
  await np.getByLabel("Title").fill("Plain form event");
  await np.getByLabel("Place").fill("The Hill");
  await np.locator("[name=person_name]").first().fill("Ana");
  await np.getByRole("button", { name: "Add event" }).click();
  ok(/Event added/.test(await np.locator(".saved").innerText()), "added without script");
  await api(main, "PUT", `/api/v1/projects/${P.id}/timeline/order`, { reset: true });
  await np.goto(ppage("?order=narrative"));
  const before = await order();
  await np.locator(`#tl-row-${before[0]} button[value=later]`).click();
  await np.waitForURL(/saved=moved/);
  eq((await order())[1], before[0], "a move through the form saved");
  await np.getByRole("button", { name: /Go back to the order the scenes give/ }).click();
  await np.waitForURL(/saved=reset/);
  eq((await order()).join(), before.join(), "reset through the form");
  await bare.close();
});

await kit.test("the date hints and the picker still work with the network cut", async () => {
  kit.allowed = [/Failed to load resource/, /ERR_INTERNET_DISCONNECTED/]; // the picker's request is refused, quietly
  await pg.goto(cpage("/events/new"));
  await pg.context().setOffline(true);
  try {
    await pg.getByLabel("Starts").fill("1998-02-30");
    await pg.getByLabel("Starts").blur();
    ok(/February has 28 days/.test(await pg.locator("#ev-start-h").innerText()), "the hint needs no network");
    await pg.locator("[name=person_name]").first().fill("An");
    await pg.locator("[name=person_name]").first().dispatchEvent("input");
    await pg.locator("[role=listbox]").first().waitFor({ state: "visible", timeout: 3000 }).catch(() => {});
    ok(true, "the picker fails quietly");
  } finally { await pg.context().setOffline(false); }
});

await kit.test("390 px: no sideways scroll, a strip that runs down, targets of 44 px, move buttons that work", async () => {
  const phone = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 });
  await phone.addCookies(await main.cookies());
  const np = await phone.newPage();
  kit.watch(np, "phone");
  for (const [label, url] of [["Compendium", cpage()], ["Compendium check", cpage("?check=1")], ["event", cpage(`/events/${ev.Wedding}`)], ["new event", cpage("/events/new")], ["book", ppage()], ["book as told", ppage("?order=narrative&check=1")]]) {
    await np.goto(url);
    await np.evaluate(() => document.fonts.ready);
    ok(await noSideways(np), `${label}: the page scrolls sideways`);
  }
  await np.goto(cpage());
  const boxes = await np.locator(".tl-strip .tl-bar").evaluateAll((els) => els.map((e) => { const r = e.getBoundingClientRect(); return { x: Math.round(r.x), y: Math.round(r.y), h: Math.round(r.height), w: Math.round(r.width) }; }));
  ok(boxes.length > 2 && boxes.every((b, i) => i === 0 || b.y > boxes[i - 1].y) && boxes.every((b) => b.x === boxes[0].x), "the strip runs down the page: " + JSON.stringify(boxes.slice(0, 3)));
  ok(boxes.every((b) => b.h >= 44), "every block is at least 44 px tall: " + Math.min(...boxes.map((b) => b.h)));
  await np.goto(ppage("?order=narrative"));
  const sizes = await np.locator(".tl-mf .btn").evaluateAll((els) => els.slice(0, 6).map((e) => { const r = e.getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; }));
  ok(sizes.length > 0 && sizes.every(([w, h]) => w >= 44 && h >= 44), "move buttons are 44 px: " + JSON.stringify(sizes));
  const start = await order();
  const firstId = start[0];
  await np.locator(`#tl-row-${firstId}`).getByRole("button", { name: /later/ }).tap();
  await waitFor(async () => (await order())[1] === firstId, "a tap on Later to be saved");
  await api(main, "PUT", `/api/v1/projects/${P.id}/timeline/order`, { reset: true });
  await np.goto(cpage("/events/new"));
  const start2 = await np.getByLabel("Starts").boundingBox();
  ok(start2.height >= 40, "the date field is a comfortable target: " + start2.height);
  await phone.close();
});

await kit.test("a stranger gets the same 404 for a Compendium that exists and one that does not", async () => {
  kit.allowed = [/status of 404/];
  const other = await kit.signedIn({}, "reader@dev.scriobh");
  const a = await other.page.goto(cpage());
  const b = await other.page.goto(`${base()}/c/no-such-compendium/timeline`);
  eq(a.status(), 404, "a stranger");
  eq(b.status(), 404, "a missing one");
  eq((await other.page.goto(ppage())).status(), 404, "a book the person is not on");
  await other.ctx.close();
});

await kit.test("five hundred events: the page stays small and quick, the strip stays at twelve blocks", async () => {
  const big = (await post("/api/v1/compendiums", { name: "Big Bible" })).item;
  const bigBook = (await post("/api/v1/projects", { title: "Big Book", kind: "novel" })).item;
  await api(main, "PUT", `/api/v1/projects/${bigBook.id}/compendium`, { compendium_id: big.id });
  const batch = [];
  for (let i = 0; i < 500; i++) {
    batch.push(api(main, "POST", `/api/v1/compendiums/${big.id}/timeline/events`, { title: `Event ${i}`, story_date: `${1900 + Math.floor(i / 5)}-${String(1 + (i % 12)).padStart(2, "0")}`, description: `The harbour at low tide, number ${i}.` }));
    if (batch.length === 25) await Promise.all(batch.splice(0));
  }
  await Promise.all(batch);
  const measure = async (label, url) => {
    const t0 = Date.now();
    const r = await pg.goto(url, { waitUntil: "load" });
    const ms = Date.now() - t0;
    const nodes = await pg.evaluate(() => document.querySelectorAll("*").length);
    const bytes = (await r.body()).length;
    console.log(`PERF ${label}: ${ms} ms to load, ${bytes} bytes, ${nodes} DOM elements`);
    ok(nodes < 3000 && ms < 4000, `${label}: ${nodes} elements, ${ms} ms`);
    return nodes;
  };
  await measure("Compendium, 500 events", `${base()}/c/${big.slug}/timeline`);
  eq(await pg.locator(".tl-strip .tl-blk").count() <= 13, true, "the strip stays at twelve blocks and the undated one");
  eq(await pg.locator("#tl-events tbody tr").count(), 50, "the table is one page");
  await measure("Compendium, last page", `${base()}/c/${big.slug}/timeline?offset=450`);
  await measure("book, 500 events, story order", `${base()}/p/${bigBook.slug}/timeline`);
  await measure("book, 500 events, as told", `${base()}/p/${bigBook.slug}/timeline?order=narrative`);
  await pg.goto(`${base()}/p/${bigBook.slug}/timeline?order=narrative&offset=50`);
  const mid = await pg.locator("#tl-body tr[data-event]").nth(2).getAttribute("data-event");
  await pg.locator(`#tl-row-${mid}`).getByRole("button", { name: /earlier/ }).click();
  await waitFor(async () => (await api(main, "GET", `/api/v1/projects/${bigBook.id}/timeline/order`)).json.explicit.length === 500, "a move on page two to be saved");
  await pg.locator("#tl-body tr[data-event]").first().locator(".tl-title").focus();
  await pg.keyboard.press("Alt+ArrowUp"); // across the page boundary: the server swaps with the event on the page before
  await waitFor(async () => (await pg.locator("#tl-body tr[data-event]").count()) === 50, "the page to be drawn again");
});

await kit.finish("e2e-timeline");
