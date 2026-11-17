#!/usr/bin/env node
// End-to-end test of the outline page in a real Chromium: reading it without script, inline
// edits and the conflict choice, reordering by key, button and drag (a whole part with its
// chapters, no word lost), adding, numbering that matches the writing view, a phone
// viewport, and a 60-chapter, 60,000-word project for timing.
//
//   node scripts/e2e-outline.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-outline");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const text = (n, w = "word") => Array(n).fill(w).join(" ");
const create = async (pid, body) => {
  const r = await api(main, "POST", `/api/v1/projects/${pid}/docs`, body);
  if (!r.json || !r.json.item) throw new Error(`creating ${JSON.stringify(body.title)} answered ${r.status} ${JSON.stringify(r.json)}`);
  return r.json.item.id;
};
// front matter, Part One (Low Water with a scene, High Water), Part Two (Slack Water, Ebb), back matter: 1,040 words.
async function book(title) {
  const made = await api(main, "POST", "/api/v1/projects", { title, kind: "novel" });
  const p = made.json.item, ids = {};
  const seeded = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.order[0];
  await create(p.id, { title: "placeholder", kind: "note" }); // keeps the project alive while the seeded chapter goes
  await api(main, "POST", `/api/v1/docs/${seeded}/trash`);
  ids.front = await create(p.id, { title: "Dedication", kind: "front_matter", content: text(40) });
  ids.p1 = await create(p.id, { title: "Part One", kind: "part" });
  ids.low = await create(p.id, { title: "Low Water", kind: "chapter", parent_id: ids.p1, content: text(300) });
  ids.ledger = await create(p.id, { title: "The ledger", kind: "scene", parent_id: ids.low, content: text(80) });
  ids.high = await create(p.id, { title: "High Water", kind: "chapter", parent_id: ids.p1, content: text(200) });
  ids.p2 = await create(p.id, { title: "Part Two", kind: "part" });
  ids.slack = await create(p.id, { title: "Slack Water", kind: "chapter", parent_id: ids.p2, content: text(150) });
  ids.ebb = await create(p.id, { title: "Ebb", kind: "chapter", parent_id: ids.p2, content: text(250) });
  ids.back = await create(p.id, { title: "Thanks", kind: "back_matter", content: text(20) });
  const list = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json;
  for (const m of list.items) if (m.title === "placeholder") await api(main, "POST", `/api/v1/docs/${m.id}/trash`);
  return { ...p, ids };
}
const outline = async (pid) => (await api(main, "GET", `/api/v1/projects/${pid}/outline`)).json;
const order = async (pid) => (await outline(pid)).items.map((i) => i.title);
const words = async (pid) => Object.fromEntries((await outline(pid)).items.map((i) => [i.id, i.word_count]));
const row = (page, id) => page.locator(`#o-${id}`);
const live = (page) => page.locator("#o-live");
const open = async (page, p) => { await page.goto(`${base()}/p/${p.slug}/outline`); await page.waitForSelector(".orow"); };
// Press a key on a row, with the row as the focused element.
const focusRow = async (page, id) => { await row(page, id).focus(); };

let B;
const ensure = async () => { if (!B) B = await book("Tidewater"); return B; };
await kit.test("outline: reads without script; numbers match the writing view's tree", async () => {
  await ensure();
  const bare = await kit.newContext({ javaScriptEnabled: false, viewport: { width: 1280, height: 800 } });
  await bare.addCookies(await main.cookies());
  const np = await bare.newPage();
  await np.goto(`${base()}/p/${B.slug}/outline`);
  const kinds = await np.locator(".orow-kind").allTextContents();
  eq(kinds.join("|"), "Front matter|Part|Chapter 1|Scene|Chapter 2|Part|Chapter 3|Chapter 4|Back matter", "kinds and chapter numbers without script");
  ok((await np.locator("#o-summary").innerText()).includes("1,040 words"), "the project total: " + (await np.locator("#o-summary").innerText()));
  eq(await np.locator("#o-add").count(), 1, "the add form is there for an editor even without script");
  await bare.close();
  // The tree in the writing view numbers the same documents the same way.
  await pg.goto(`${base()}/p/${B.slug}`);
  await pg.waitForSelector(".tree-row");
  const tree = await pg.locator(".tree-row").evaluateAll((rs) => rs.map((r) => (r.querySelector(".tree-num") || {}).textContent || ""));
  const out = await pg.evaluate(async (url) => {
    const t = await (await fetch(url)).text();
    const d = new DOMParser().parseFromString(t, "text/html");
    return [...d.querySelectorAll(".orow-kind")].map((e) => (/^Chapter (\d+)/.exec(e.textContent) || [, ""])[1]);
  }, `${base()}/p/${B.slug}/outline`);
  eq(tree.join(","), out.join(","), "chapter numbers: tree vs outline");
});

await kit.test("outline: edit synopsis, status and target in place; the totals follow", async () => {
  await ensure();
  await open(pg, B);
  await row(pg, B.ids.low).locator(".orow-syn").click();
  await pg.getByRole("button", { name: "Edit Low Water" }).click();
  await pg.getByLabel("Synopsis").fill("Mara counts the boats that did not come in.");
  await pg.locator(".orow-edit").getByLabel("Status").selectOption("done");
  await pg.getByLabel("Target words").fill("6,000");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await waitFor(async () => (await outline(B.id)).items.find((i) => i.id === B.ids.low).target_words === 6000, "the saved target");
  const d = (await outline(B.id)).items.find((i) => i.id === B.ids.low);
  eq(d.status, "done", "status");
  eq(d.synopsis_short, "Mara counts the boats that did not come in.", "synopsis");
  await waitFor(async () => (await row(pg, B.ids.low).innerText()).includes("of 6,000"), "the row to show the target");
  ok((await row(pg, B.ids.low).innerText()).includes("Mara counts"), "the synopsis on the row");
  ok((await pg.locator("#o-summary").innerText()).includes("of 6,000"), "the project target in the summary: " + (await pg.locator("#o-summary").innerText()));
  eq(await row(pg, B.ids.low).locator("progress").count(), 1, "a progress bar for a target");
  // Focus returned to the row's Edit button, so the next edit is one key away.
  eq(await pg.evaluate(() => document.activeElement.dataset.act), "edit", "focus after saving");
  // Escape leaves an edit without saving.
  await pg.keyboard.press("Enter");
  await pg.getByLabel("Synopsis").fill("not saved");
  await pg.keyboard.press("Escape");
  eq(await pg.locator(".orow-edit").count(), 0, "the editor closed");
  eq((await outline(B.id)).items.find((i) => i.id === B.ids.low).synopsis_short, "Mara counts the boats that did not come in.", "Escape saved something");
  // A target that is not a number is refused in words, and nothing is sent.
  await pg.keyboard.press("Enter");
  await pg.getByLabel("Target words").fill("lots");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  ok((await pg.locator(".orow-edit .field-error").innerText()).includes("whole number"), "the message for a bad target");
  await pg.keyboard.press("Escape");
});

await kit.test("outline: a change made elsewhere is shown, never overwritten", async () => {
  kit.allowed = [/status of 409/];
  await ensure();
  await open(pg, B);
  await row(pg, B.ids.high).locator(".orow-syn").click();
  await pg.getByRole("button", { name: "Edit High Water" }).click();
  await pg.getByLabel("Synopsis").fill("My version of the synopsis.");
  const cur = (await api(main, "GET", `/api/v1/docs/${B.ids.high}`)).json.item;
  const r = await api(main, "PATCH", `/api/v1/docs/${B.ids.high}`, { base_revision: cur.revision, status: "revise", synopsis_short: "Their version." });
  eq(r.status, 200, "the other edit");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await pg.locator(".orow-edit .conflict").waitFor();
  ok((await pg.locator(".conflict").innerText()).includes("Their version."), "their synopsis is shown");
  eq((await api(main, "GET", `/api/v1/docs/${B.ids.high}`)).json.item.synopsis_short, "Their version.", "the server kept theirs until asked");
  eq(await pg.getByLabel("Synopsis").inputValue(), "My version of the synopsis.", "my text stays in the form");
  await pg.getByRole("button", { name: "Keep mine" }).click();
  await waitFor(async () => (await api(main, "GET", `/api/v1/docs/${B.ids.high}`)).json.item.synopsis_short === "My version of the synopsis.", "mine to be saved");
  eq((await api(main, "GET", `/api/v1/docs/${B.ids.high}`)).json.item.status, "revise", "a field I did not change kept their value");
});

await kit.test("outline: two quick moves are applied one after the other, each on the list as the last left it", async () => {
  const Q = await book("Queue");
  await open(pg, Q);
  await focusRow(pg, Q.ids.p1);
  await pg.keyboard.press("Alt+ArrowDown");
  await pg.keyboard.press("Alt+ArrowDown"); // pressed before the first answer came back
  await waitFor(async () => (await order(Q.id)).join() === "Dedication,Part Two,Slack Water,Ebb,Thanks,Part One,Low Water,The ledger,High Water", "Part One to go below both Part Two and the back matter");
  await sleep(400);
  eq((await pg.locator(".orow-title").allInnerTexts()).join(), (await order(Q.id)).join(), "the page shows the order the server holds");
});

await kit.test("outline: reorder by key, announced; a whole part moves with its chapters and no word is lost", async () => {
  await ensure();
  await open(pg, B);
  const before = await words(B.id), total = (await outline(B.id)).totals.words;
  eq(total, 1040, "the starting total");
  // Move a chapter down by key.
  await focusRow(pg, B.ids.slack);
  await pg.keyboard.press("Alt+ArrowDown");
  await waitFor(async () => (await order(B.id)).join() === "Dedication,Part One,Low Water,The ledger,High Water,Part Two,Ebb,Slack Water,Thanks", "Slack Water to move below Ebb");
  await waitFor(async () => /Moved Slack Water\. Now 2 of 2 in Part Two/.test(await live(pg).innerText()), "the move to be announced: " + (await live(pg).innerText()));
  eq(await pg.evaluate(() => document.activeElement.id), `o-${B.ids.slack}`, "focus stays on the moved row");
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => (await order(B.id)).slice(5, 8).join() === "Part Two,Slack Water,Ebb", "and back up");
  // The first one cannot go further up.
  await focusRow(pg, B.ids.slack);
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => /Already first/.test(await live(pg).innerText()), "the refusal in words");
  // A whole part: Part Two moves above Part One, with Slack Water and Ebb.
  await focusRow(pg, B.ids.p2);
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => (await order(B.id)).join() === "Dedication,Part Two,Slack Water,Ebb,Part One,Low Water,The ledger,High Water,Thanks", "Part Two to move up with its chapters");
  const after = await outline(B.id);
  eq(after.totals.words, total, "the project word total after moving a part");
  const now = Object.fromEntries(after.items.map((i) => [i.id, i.word_count]));
  for (const [id, w] of Object.entries(before)) eq(now[id], w, `words of ${id}`);
  const parents = Object.fromEntries(after.items.map((i) => [i.id, i.parent_id]));
  eq(parents[B.ids.slack], B.ids.p2, "Slack Water is still in Part Two");
  eq(parents[B.ids.ledger], B.ids.low, "the scene is still in its chapter");
  // The page shows the new numbers: chapters were renumbered in reading order.
  await waitFor(async () => (await row(pg, B.ids.slack).locator(".orow-kind").textContent()) === "Chapter 1", "Slack Water to be renumbered Chapter 1 on the page");
  const kinds = await pg.locator(".orow-kind").allTextContents();
  eq(kinds.filter((k) => k.startsWith("Chapter")).join("|"), "Chapter 1|Chapter 2|Chapter 3|Chapter 4", "renumbered");
  // Indent and outdent: the back matter goes inside Part One, then out again.
  await focusRow(pg, B.ids.back);
  await pg.keyboard.press("Alt+ArrowRight");
  await waitFor(async () => (await outline(B.id)).items.find((i) => i.id === B.ids.back).parent_id === B.ids.p1, "Thanks to move into Part One");
  await waitFor(async () => (await row(pg, B.ids.back).getAttribute("data-parent")) === B.ids.p1 && (await pg.evaluate(() => document.activeElement.id)) === `o-${B.ids.back}`, "the page to settle on the moved row");
  await pg.keyboard.press("Alt+ArrowLeft");
  await waitFor(async () => (await outline(B.id)).items.find((i) => i.id === B.ids.back).parent_id === "", "Thanks to move out again");
  // The page settles on the moved row after the server answers; wait for that, or it takes focus back from the next step.
  await waitFor(async () => (await row(pg, B.ids.back).getAttribute("data-parent")) === "" && (await pg.evaluate(() => document.activeElement.id)) === `o-${B.ids.back}`, "the page to settle on the row moved out");
  eq((await outline(B.id)).totals.words, total, "the total after indent and outdent");
  // The same actions are buttons, reachable by Tab from the row.
  await focusRow(pg, B.ids.ebb);
  await pg.keyboard.press("Tab"); // the title link
  await pg.keyboard.press("Tab"); // Edit
  await pg.keyboard.press("Tab"); // Move up
  eq(await pg.evaluate(() => document.activeElement.getAttribute("aria-label")), "Move up Ebb", "Tab reaches the row's buttons");
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await order(B.id)).slice(1, 4).join() === "Part Two,Ebb,Slack Water", "Ebb to move up by button");
});

await kit.test("outline: add a chapter, a scene inside a chapter, and a chapter inside a part", async () => {
  await ensure();
  await open(pg, B);
  await pg.locator("#o-add-kind").selectOption("chapter");
  await pg.locator("#o-add-title").fill("Flotsam");
  await pg.getByRole("button", { name: "Add to the end" }).click();
  const id = await waitFor(async () => (await outline(B.id)).items.find((i) => i.title === "Flotsam"), "the new chapter").then((i) => i.id);
  await waitFor(async () => await row(pg, id).count(), "the new row on the page");
  eq(await pg.evaluate(() => document.activeElement.id), `o-${id}`, "focus goes to the new row");
  eq(await pg.locator("#o-add-title").inputValue(), "", "the title field is cleared");
  await row(pg, B.ids.low).locator(".orow-syn").click();
  await pg.getByRole("button", { name: "Add scene to Low Water" }).click();
  await waitFor(async () => (await outline(B.id)).items.filter((i) => i.parent_id === B.ids.low && i.kind === "scene").length === 2, "a second scene in Low Water");
  await row(pg, B.ids.p2).locator(".orow-syn").click();
  await pg.getByRole("button", { name: "Add chapter to Part Two" }).click();
  await waitFor(async () => (await outline(B.id)).items.filter((i) => i.parent_id === B.ids.p2 && i.kind === "chapter").length === 3, "a third chapter in Part Two");
  // The server's numbers and the page's agree after all that.
  await pg.reload();
  const kinds = await pg.locator(".orow-kind").allTextContents();
  const nums = kinds.filter((k) => k.startsWith("Chapter")).map((k) => k.split(" ")[1]);
  eq(nums.join(","), nums.map((_, i) => i + 1).join(","), "chapter numbers run 1..n");
});

await kit.test("outline: drag a chapter before another and into a part; Escape cancels", async () => {
  await ensure();
  await pg.setViewportSize({ width: 1280, height: 1400 }); // both rows on screen, as a person dragging would have them
  await open(pg, B);
  const drag = async (fromId, toId, where) => {
    await row(pg, fromId).locator(".orow-syn").click();
    const g = await pg.getByRole("button", { name: new RegExp("^Drag ") }).boundingBox();
    const t = await row(pg, toId).boundingBox();
    await pg.mouse.move(g.x + g.width / 2, g.y + g.height / 2);
    await pg.mouse.down();
    const y = where === "top" ? t.y + 4 : t.y + t.height - 4;
    await pg.mouse.move(t.x + 60, y, { steps: 8 });
    return y;
  };
  // Dropped on the top half of High Water: Ebb goes in front of it, inside Part One.
  await drag(B.ids.ebb, B.ids.high, "top");
  await pg.mouse.up();
  await waitFor(async () => { const i = (await outline(B.id)).items; return i.find((x) => x.id === B.ids.ebb).parent_id === B.ids.p1 && i.map((x) => x.title).indexOf("Ebb") < i.map((x) => x.title).indexOf("High Water"); }, "Ebb to be dropped before High Water");
  // Dropped on the lower half of a part: first child of that part.
  await open(pg, B);
  await drag(B.ids.high, B.ids.p2, "bottom");
  await pg.mouse.up();
  await waitFor(async () => { const i = (await outline(B.id)).items; return i.find((x) => x.id === B.ids.high).parent_id === B.ids.p2; }, "High Water to be dropped into Part Two");
  // Escape cancels a drag: nothing moves.
  await open(pg, B);
  const was = (await order(B.id)).join();
  await drag(B.ids.back, B.ids.low, "top");
  await pg.keyboard.press("Escape");
  await pg.mouse.up();
  await sleep(300);
  eq((await order(B.id)).join(), was, "a cancelled drag moved something");
  ok(/cancelled/.test(await live(pg).innerText()), "the cancel is announced");
  // A part cannot be dropped into itself.
  await drag(B.ids.p2, B.ids.slack, "top");
  await pg.mouse.up();
  await sleep(300);
  eq((await order(B.id)).join(), was, "a part was dropped inside itself");
  await pg.setViewportSize({ width: 1280, height: 800 });
});

await kit.test("outline: phone, 390 px: no sideways scroll, targets of 44 px, buttons move rows", async () => {
  const ph = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  await ph.addCookies(await main.cookies());
  const p = await ph.newPage();
  kit.watch(p, "phone");
  await p.goto(`${base()}/p/${B.slug}/outline`);
  await p.waitForSelector(".orow");
  eq(await p.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "the page scrolls sideways");
  const ids = (await outline(B.id)).items.filter((i) => i.kind === "chapter");
  const last = ids[ids.length - 1];
  await row(p, last.id).locator(".orow-syn").tap();
  const small = await row(p, last.id).locator(".orow-tools button").evaluateAll((bs) => bs.filter((b) => { const r = b.getBoundingClientRect(); return r.height < 43.5 || r.width < 43.5; }).map((b) => b.getAttribute("aria-label")));
  eq(small.length, 0, "tools under 44 px: " + small.join(", "));
  const was = (await order(B.id)).join();
  const up = row(p, last.id).getByRole("button", { name: /^Move up / });
  if (await up.isEnabled()) await up.tap();
  await waitFor(async () => (await order(B.id)).join() !== was, "the tap on Move up to move the row");
  await p.screenshot({ path: `${kit.tmp}/outline-phone.png` });
  await ph.close();
});

await kit.test("outline: a 60-chapter, 60,000-word project loads fast and moves a part", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "Sixty", kind: "novel", word_goal: 90000 });
  const p = made.json.item;
  const seeded = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.order[0];
  const parts = [];
  for (let i = 1; i <= 6; i++) {
    const part = await create(p.id, { title: `Part ${["One", "Two", "Three", "Four", "Five", "Six"][i - 1]}`, kind: "part" });
    parts.push(part);
    for (let c = 1; c <= 10; c++) await create(p.id, { title: `Chapter title ${i}-${c}`, kind: "chapter", parent_id: part, content: text(1000, "tide"), synopsis_short: `What happens in ${i}-${c}.` });
  }
  await api(main, "POST", `/api/v1/docs/${seeded}/trash`);
  const o = await outline(p.id);
  eq(o.totals.words, 60000, "the generated total");
  const times = [];
  for (let i = 0; i < 7; i++) {
    const t0 = Date.now();
    const r = await main.request.get(`${base()}/p/${p.slug}/outline`);
    ok(r.status() === 200, "status " + r.status());
    await r.body();
    times.push(Date.now() - t0);
  }
  times.sort((a, b) => a - b);
  console.log(`      outline page, 60 chapters / 60,000 words, server render: median ${times[3]} ms, max ${times[6]} ms`);
  ok(times[6] < 150, `server render max ${times[6]} ms is over the 150 ms page budget`);
  const t0 = Date.now();
  await pg.goto(`${base()}/p/${p.slug}/outline`, { waitUntil: "load" });
  await pg.waitForSelector(".orow");
  const nav = await pg.evaluate(() => { const n = performance.getEntriesByType("navigation")[0]; return { dcl: Math.round(n.domContentLoadedEventEnd), load: Math.round(n.loadEventEnd), rows: document.querySelectorAll(".orow").length }; });
  console.log(`      browser: DOMContentLoaded ${nav.dcl} ms, load ${nav.load} ms, ${nav.rows} rows (wall ${Date.now() - t0} ms)`);
  eq(nav.rows, 66, "rows");
  // Move Part Two above Part One by key, timed from the key press to the new order on the page.
  await focusRow(pg, parts[1]);
  const k0 = Date.now();
  await pg.keyboard.press("Alt+ArrowUp");
  await pg.waitForFunction((id) => { const rs = [...document.querySelectorAll(".orow")]; return rs[0].dataset.id === id; }, parts[1], { timeout: 8000 });
  console.log(`      move of a 10-chapter part: key press to redrawn list ${Date.now() - k0} ms`);
  const after = await outline(p.id);
  eq(after.totals.words, 60000, "the total after moving a part");
  eq(after.items.length, 66, "documents after moving a part");
  eq((await pg.locator("#o-summary").innerText()).includes("60,000"), true, "the summary on the page");
});

await kit.finish("e2e-outline");
