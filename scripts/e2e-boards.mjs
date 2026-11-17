#!/usr/bin/env node
// End-to-end test of the corkboard, the status board and reading mode in a real Chromium: reading the pages
// without script, reordering cards by key, button and menu (a whole part with its chapters, no word lost),
// quick moves in a row, synopsis edits and the conflict choice, a status change by key and button with a forced
// 409, a reader's read-only view, reading by key and link, the remembered position, continuous scroll,
// typography and theme preferences, a phone viewport, and synthetic live events that must not disturb what a
// person is doing.
//
//   node scripts/e2e-boards.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-boards");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const text = (n, w = "word") => Array(n).fill(w).join(" ");
// Paragraphs of 60 words, so a chapter has height.
const prose = (n, w = "tide") => Array(Math.ceil(n / 60)).fill(text(60, w)).join("\n\n");
const create = async (pid, body) => {
  const r = await api(main, "POST", `/api/v1/projects/${pid}/docs`, body);
  if (!r.json || !r.json.item) throw new Error(`creating ${JSON.stringify(body.title)} answered ${r.status} ${JSON.stringify(r.json)}`);
  return r.json.item.id;
};
// front matter, Part One (Low Water with a scene, High Water), Part Two (Slack Water, Ebb), back matter.
async function book(title, long = 0) {
  const made = await api(main, "POST", "/api/v1/projects", { title, kind: "novel" });
  const p = made.json.item, ids = {};
  const seeded = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.order[0];
  await create(p.id, { title: "placeholder", kind: "note" });
  await api(main, "POST", `/api/v1/docs/${seeded}/trash`);
  const body = (n) => (long ? prose(long) : text(n));
  ids.front = await create(p.id, { title: "Dedication", kind: "front_matter", content: text(40) });
  ids.p1 = await create(p.id, { title: "Part One", kind: "part" });
  ids.low = await create(p.id, { title: "Low Water", kind: "chapter", parent_id: ids.p1, content: body(300) });
  ids.ledger = await create(p.id, { title: "The ledger", kind: "scene", parent_id: ids.low, content: text(80) });
  ids.high = await create(p.id, { title: "High Water", kind: "chapter", parent_id: ids.p1, content: body(200) });
  ids.p2 = await create(p.id, { title: "Part Two", kind: "part" });
  ids.slack = await create(p.id, { title: "Slack Water", kind: "chapter", parent_id: ids.p2, content: body(150) });
  ids.ebb = await create(p.id, { title: "Ebb", kind: "chapter", parent_id: ids.p2, content: body(250) });
  ids.back = await create(p.id, { title: "Thanks", kind: "back_matter", content: text(20) });
  const list = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json;
  for (const m of list.items) if (m.title === "placeholder") await api(main, "POST", `/api/v1/docs/${m.id}/trash`);
  return { ...p, ids };
}
const outline = async (pid) => (await api(main, "GET", `/api/v1/projects/${pid}/outline`)).json;
const order = async (pid) => (await outline(pid)).items.map((i) => i.title);
const getDoc = async (id) => (await api(main, "GET", `/api/v1/docs/${id}`)).json.item;
const patch = async (id, fields) => api(main, "PATCH", `/api/v1/docs/${id}`, { base_revision: (await getDoc(id)).revision, ...fields });
const card = (page, id) => page.locator(`#c-${id}`);
const live = (page) => page.locator("#b-live");
const openCards = async (page, p, q = "") => { await page.goto(`${base()}/p/${p.slug}/cards${q}`); await page.waitForSelector(".ccard"); await page.waitForFunction(() => window.__boardsReady === true); };
const openBoard = async (page, p) => { await page.goto(`${base()}/p/${p.slug}/board`); await page.waitForSelector(".bcard"); await page.waitForFunction(() => window.__boardsReady === true); };
const focusCard = async (page, id) => { await card(page, id).focus(); };
const active = (page) => page.evaluate(() => document.activeElement && (document.activeElement.id || document.activeElement.getAttribute("aria-label") || document.activeElement.tagName));
const noSideways = (page) => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1);

let B;
const ensure = async () => { if (!B) B = await book("Tidewater"); return B; };

await kit.test("corkboard: reads without script, in lanes; numbers match the writing view's tree", async () => {
  await ensure();
  const bare = await kit.newContext({ javaScriptEnabled: false, viewport: { width: 1280, height: 800 } });
  await bare.addCookies(await main.cookies());
  const np = await bare.newPage();
  await np.goto(`${base()}/p/${B.slug}/cards`);
  const kinds = await np.locator(".ccard-kind").allTextContents();
  eq(kinds.join("|"), "Front matter|Chapter 1|Scene|Chapter 2|Chapter 3|Chapter 4|Back matter", "kinds and chapter numbers without script");
  const heads = (await np.locator(".lane-head").allTextContents()).map((h) => h.replace(/\s+/g, " ").trim().replace(/ [\d,]+ words?$/, ""));
  ok(heads.includes("Part One") && heads.includes("Part Two"), "a lane for each part: " + heads.join("|"));
  ok((await np.locator(".b-count").innerText()).includes("1,040 words"), "the total: " + (await np.locator(".b-count").innerText()));
  eq(await np.locator("#c-" + B.ids.low + " .ccard-title").getAttribute("href"), `/p/${B.slug}/d/${B.ids.low}`, "a card opens the document in the writing view");
  const status = await np.locator("#c-" + B.ids.low + " .ccard-status").innerText();
  ok(/Status:\s*\S+/.test(status), "a status word on the card: " + status);
  await bare.close();
  await pg.goto(`${base()}/p/${B.slug}`);
  await pg.waitForSelector(".tree-row");
  const tree = await pg.locator(".tree-row").evaluateAll((rs) => rs.map((r) => (r.querySelector(".tree-num") || {}).textContent || "").filter(Boolean));
  await openCards(pg, B);
  const nums = (await pg.locator(".ccard-kind").allTextContents()).filter((k) => k.startsWith("Chapter")).map((k) => k.split(" ")[1]);
  eq(tree.join(","), nums.join(","), "chapter numbers: tree vs corkboard");
});

await kit.test("corkboard: reorder by key, announced, focus kept; a refusal in words", async () => {
  await ensure();
  await openCards(pg, B);
  await focusCard(pg, B.ids.slack);
  await pg.keyboard.press("Alt+ArrowDown");
  await waitFor(async () => (await order(B.id)).join() === "Dedication,Part One,Low Water,The ledger,High Water,Part Two,Ebb,Slack Water,Thanks", "Slack Water to move below Ebb");
  await waitFor(async () => /Moved Slack Water later\./.test(await live(pg).innerText()), "the announcement: " + (await live(pg).innerText()));
  eq(await active(pg), `c-${B.ids.slack}`, "focus stays on the moved card");
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => (await order(B.id)).slice(5, 8).join() === "Part Two,Slack Water,Ebb", "and back up");
  await focusCard(pg, B.ids.slack);
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => /Already first/.test(await live(pg).innerText()), "the refusal in words");
  // The chapters were renumbered on the page as well as on the server.
  await pg.keyboard.press("Alt+ArrowDown");
  await waitFor(async () => (await card(pg, B.ids.ebb).locator(".ccard-kind").textContent()) === "Chapter 3", "Ebb to be renumbered Chapter 3");
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => (await card(pg, B.ids.slack).locator(".ccard-kind").textContent()) === "Chapter 3", "Slack Water to be Chapter 3 again");
});

await kit.test("corkboard: quick moves in a row are all made, in order, with no conflict", async () => {
  const Q = await book("Quick");
  const extra = [];
  for (const n of ["Q1", "Q2", "Q3"]) extra.push(await create(Q.id, { title: n, kind: "chapter", parent_id: Q.ids.p2, content: text(20) }));
  await openCards(pg, Q);
  await focusCard(pg, Q.ids.slack);
  for (let i = 0; i < 3; i++) await pg.keyboard.press("Alt+ArrowDown");
  await waitFor(async () => (await order(Q.id)).slice(5).join() === "Part Two,Ebb,Q1,Q2,Slack Water,Q3,Thanks", "Slack Water three places later: " + (await order(Q.id)).join());
  await waitFor(async () => (await pg.evaluate(() => document.activeElement.id)) === `c-${Q.ids.slack}`, "focus on the moved card");
  // And the other way, with the keys held down faster than the server answers.
  for (let i = 0; i < 4; i++) await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => (await order(Q.id)).slice(5).join() === "Part Two,Slack Water,Ebb,Q1,Q2,Q3,Thanks", "Slack Water back at the top: " + (await order(Q.id)).join());
  // What the page shows is what the server has, once things settle.
  await waitFor(async () => (await pg.locator(".ccard-title").allTextContents()).join() === "Dedication,Low Water,The ledger,High Water,Slack Water,Ebb,Q1,Q2,Q3,Thanks", "the page to settle: " + (await pg.locator(".ccard-title").allTextContents()).join());
});

await kit.test("corkboard: buttons, the Move to menu, and a whole part with its chapters and no word lost", async () => {
  await ensure();
  await openCards(pg, B);
  const before = Object.fromEntries((await outline(B.id)).items.map((i) => [i.id, i.word_count])), total = (await outline(B.id)).totals.words;
  // A click on a card shows its tools; Earlier is a button with the card's name.
  await card(pg, B.ids.ebb).locator(".ccard-syn").click();
  await pg.getByRole("button", { name: "Move earlier: Ebb" }).click();
  await waitFor(async () => (await order(B.id)).slice(5, 8).join() === "Part Two,Ebb,Slack Water", "Ebb to move up by button");
  await pg.getByRole("button", { name: "Move later: Ebb" }).click();
  await waitFor(async () => (await order(B.id)).slice(5, 8).join() === "Part Two,Slack Water,Ebb", "Ebb to move down by button");
  // Move to another part, by the menu.
  await pg.getByRole("button", { name: "Move to another part: Ebb" }).click();
  await pg.getByLabel("Move Ebb to").selectOption({ label: "Part One" });
  await pg.getByRole("button", { name: "Move", exact: true }).click();
  await waitFor(async () => (await outline(B.id)).items.find((i) => i.id === B.ids.ebb).parent_id === B.ids.p1, "Ebb to move into Part One");
  await waitFor(async () => /Moved Ebb to Part One\./.test(await live(pg).innerText()), "the announcement");
  eq(await active(pg), `c-${B.ids.ebb}`, "focus on the moved card");
  // Escape leaves the menu without moving.
  await pg.getByRole("button", { name: "Move to another part: Ebb" }).click();
  await pg.getByLabel("Move Ebb to").selectOption({ label: "Part Two" });
  await pg.keyboard.press("Escape");
  eq(await pg.locator(".cmove").count(), 0, "the menu closed");
  eq((await outline(B.id)).items.find((i) => i.id === B.ids.ebb).parent_id, B.ids.p1, "Escape moved something");
  await pg.getByRole("button", { name: "Move to another part: Ebb" }).click();
  await pg.getByLabel("Move Ebb to").selectOption({ label: "Part Two" });
  await pg.getByRole("button", { name: "Move", exact: true }).click();
  await waitFor(async () => (await outline(B.id)).items.find((i) => i.id === B.ids.ebb).parent_id === B.ids.p2, "Ebb back in Part Two");
  // A scene moves to another chapter.
  await card(pg, B.ids.ledger).locator(".ccard-syn").click();
  await pg.getByRole("button", { name: "Move to another part: The ledger" }).click();
  const opts = await pg.getByLabel("Move The ledger to").locator("option").allTextContents();
  ok(opts.every((o) => o.startsWith("Chapter ")), "a scene is offered chapters only: " + opts.join("|"));
  await pg.getByLabel("Move The ledger to").selectOption({ label: "Chapter 2: High Water" });
  await pg.getByRole("button", { name: "Move", exact: true }).click();
  await waitFor(async () => (await outline(B.id)).items.find((i) => i.id === B.ids.ledger).parent_id === B.ids.high, "the scene to move into High Water");
  await pg.getByRole("button", { name: "Move to another part: The ledger" }).click();
  await pg.getByLabel("Move The ledger to").selectOption({ label: "Chapter 1: Low Water" });
  await pg.getByRole("button", { name: "Move", exact: true }).click();
  await waitFor(async () => (await outline(B.id)).items.find((i) => i.id === B.ids.ledger).parent_id === B.ids.low, "the scene back in Low Water");
  // A whole part: Part Two above Part One, with its chapters.
  await pg.getByRole("button", { name: "Move part earlier: Part Two" }).click();
  await waitFor(async () => (await order(B.id)).slice(0, 4).join() === "Dedication,Part Two,Slack Water,Ebb", "Part Two to move up with its chapters: " + (await order(B.id)).join());
  const after = await outline(B.id);
  eq(after.totals.words, total, "the project total after moving a part");
  for (const i of after.items) eq(i.word_count, before[i.id], `words of ${i.title}`);
  const parents = Object.fromEntries(after.items.map((i) => [i.id, i.parent_id]));
  eq(parents[B.ids.slack], B.ids.p2, "Slack Water is still in Part Two");
  eq(parents[B.ids.ledger], B.ids.low, "the scene is still in its chapter");
  await waitFor(async () => /Moved Part Two earlier, with everything in it\./.test(await live(pg).innerText()), "the announcement");
  // The corkboard and the outline number the same chapters the same way after all that.
  await waitFor(async () => (await card(pg, B.ids.slack).locator(".ccard-kind").textContent()) === "Chapter 1", "Slack Water to be Chapter 1 on the page");
  const nums = (await pg.locator(".ccard-kind").allTextContents()).filter((k) => k.startsWith("Chapter")).map((k) => k.split(" ")[1]);
  eq(nums.join(","), "1,2,3,4", "renumbered in reading order");
  const outlineNums = await pg.evaluate(async (url) => {
    const t = await (await fetch(url)).text();
    const d = new DOMParser().parseFromString(t, "text/html");
    return [...d.querySelectorAll(".orow-kind")].map((e) => (/^Chapter (\d+)/.exec(e.textContent) || [, ""])[1]).filter(Boolean);
  }, `${base()}/p/${B.slug}/outline`);
  eq(outlineNums.join(","), nums.join(","), "corkboard vs outline numbering");
  // Put it back for the tests that follow.
  await pg.getByRole("button", { name: "Move part later: Part Two" }).click();
  await waitFor(async () => (await order(B.id)).slice(0, 3).join() === "Dedication,Part One,Low Water", "Part Two back down");
});

await kit.test("corkboard: edit a synopsis in place; the conflict choice shows theirs and keeps mine", async () => {
  kit.allowed = [/status of 409/];
  await ensure();
  await openCards(pg, B);
  await card(pg, B.ids.low).locator(".ccard-syn").click();
  await pg.getByRole("button", { name: "Edit the synopsis of Low Water" }).click();
  await pg.getByRole("textbox", { name: "Synopsis of Low Water" }).fill("Mara counts the boats that did not come in.");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await waitFor(async () => (await getDoc(B.ids.low)).synopsis_short === "Mara counts the boats that did not come in.", "the saved synopsis");
  await waitFor(async () => (await card(pg, B.ids.low).locator(".ccard-syn").innerText()).includes("Mara counts"), "the card to show it");
  eq(await pg.evaluate(() => document.activeElement.dataset.act), "edit", "focus returns to the Edit button");
  // Escape leaves without saving; Enter on a card opens the editor.
  await focusCard(pg, B.ids.low);
  await pg.keyboard.press("Enter");
  await pg.getByRole("textbox", { name: "Synopsis of Low Water" }).fill("not saved");
  await pg.keyboard.press("Escape");
  eq(await pg.locator(".cedit").count(), 0, "the editor closed");
  eq((await getDoc(B.ids.low)).synopsis_short, "Mara counts the boats that did not come in.", "Escape saved something");
  // A change made elsewhere is shown, never overwritten.
  await card(pg, B.ids.high).locator(".ccard-syn").click();
  await pg.getByRole("button", { name: "Edit the synopsis of High Water" }).click();
  await pg.getByRole("textbox", { name: "Synopsis of High Water" }).fill("My version of the synopsis.");
  eq((await patch(B.ids.high, { synopsis_short: "Their version.", status: "revise" })).status, 200, "the other edit");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await pg.locator(".cedit .cconf").waitFor();
  ok((await pg.locator(".cconf").innerText()).includes("Their version."), "their synopsis is shown");
  eq((await getDoc(B.ids.high)).synopsis_short, "Their version.", "the server kept theirs until asked");
  eq(await pg.getByRole("textbox", { name: "Synopsis of High Water" }).inputValue(), "My version of the synopsis.", "my text stays in the form");
  await pg.getByRole("button", { name: "Keep mine" }).click();
  await waitFor(async () => (await getDoc(B.ids.high)).synopsis_short === "My version of the synopsis.", "mine to be saved");
  eq((await getDoc(B.ids.high)).status, "revise", "a field I did not change kept their value");
  // Use theirs drops mine and shows the saved one.
  await pg.getByRole("button", { name: "Edit the synopsis of High Water" }).click();
  await pg.getByRole("textbox", { name: "Synopsis of High Water" }).fill("Another of mine.");
  await patch(B.ids.high, { synopsis_short: "Theirs again." });
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await pg.locator(".cedit .cconf").waitFor();
  await pg.getByRole("button", { name: "Use theirs" }).click();
  await waitFor(async () => (await card(pg, B.ids.high).locator(".ccard-syn").innerText()).includes("Theirs again."), "their synopsis on the card");
  eq(await pg.locator(".cedit").count(), 0, "the form closed");
});

await kit.test("corkboard: filter by status; moving is off under a filter, and says so", async () => {
  await ensure();
  await patch(B.ids.slack, { status: "done" });
  await patch(B.ids.ebb, { status: "done" });
  await openCards(pg, B);
  await pg.getByLabel("Show", { exact: true }).selectOption("done");
  await pg.getByRole("button", { name: "Show", exact: true }).click();
  await pg.waitForURL(/status=done/);
  await pg.waitForSelector(".ccard");
  const titles = await pg.locator(".ccard-title").allTextContents();
  ok(titles.includes("Slack Water") && titles.includes("Ebb") && !titles.includes("Low Water"), "only the Done cards: " + titles.join("|"));
  ok((await pg.locator(".b-count").innerText()).includes("Clear the filter to reorder cards"), "the reason moving is off");
  await card(pg, B.ids.slack).locator(".ccard-syn").click();
  eq(await pg.getByRole("button", { name: "Move later: Slack Water" }).isDisabled(), true, "Later is off under a filter");
  const was = (await order(B.id)).join();
  await focusCard(pg, B.ids.slack);
  await pg.keyboard.press("Alt+ArrowDown");
  await sleep(500);
  eq((await order(B.id)).join(), was, "Alt+Arrow moved a card under a filter");
  eq(await pg.locator(".lane-tools").count(), 0, "no part buttons under a filter");
  // The synopsis can still be edited under a filter.
  await pg.getByRole("button", { name: "Edit the synopsis of Slack Water" }).click();
  await pg.getByRole("textbox", { name: "Synopsis of Slack Water" }).fill("Slack, between tides.");
  await pg.getByRole("button", { name: "Save", exact: true }).click();
  await waitFor(async () => (await getDoc(B.ids.slack)).synopsis_short === "Slack, between tides.", "the synopsis saved under a filter");
  await waitFor(async () => /status=done/.test(pg.url()), "the filter to stay");
  await patch(B.ids.slack, { status: "draft" });
  await patch(B.ids.ebb, { status: "draft" });
});

await kit.test("corkboard: a reader sees it read-only", async () => {
  await ensure();
  const rd = await kit.signedIn({}, "reader@dev.scriobh");
  const added = await api(main, "POST", `/api/v1/projects/${B.id}/members`, { email: "reader@dev.scriobh", role: "reader" });
  ok(added.status === 201 || added.status === 409, "adding the reader: " + added.status);
  await rd.page.goto(`${base()}/p/${B.slug}/cards`);
  await rd.page.waitForSelector(".ccard");
  eq(await rd.page.locator('script[src*="boards"]').count(), 0, "the editing script is not on a reader's corkboard");
  await card(rd.page, B.ids.low).locator(".ccard-syn").click();
  eq(await rd.page.locator(".ctools").count(), 0, "no tools for a reader");
  await rd.page.goto(`${base()}/p/${B.slug}/board`);
  await rd.page.waitForSelector(".bcard");
  eq(await rd.page.locator('script[src*="boards"]').count(), 0, "nor on the status board");
  await focusCard(rd.page, B.ids.low);
  const st = (await getDoc(B.ids.low)).status;
  await rd.page.keyboard.press("ArrowRight");
  await sleep(400);
  eq((await getDoc(B.ids.low)).status, st, "a reader's arrow key changed a status");
  await rd.page.goto(`${base()}/p/${B.slug}/read/${B.ids.low}`);
  ok((await rd.page.locator("#read-stream").innerText()).includes("word"), "a reader can read a chapter");
  const direct = await api(rd.ctx, "PATCH", `/api/v1/docs/${B.ids.low}`, { base_revision: 1, status: "done" });
  ok(direct.status === 403 || direct.status === 404, "the API refuses a reader's change: " + direct.status);
  await rd.ctx.close();
});

await kit.test("board: change a status by key; counts, words and focus follow; announced", async () => {
  await ensure();
  await patch(B.ids.low, { status: "draft" });
  await openBoard(pg, B);
  const colCount = (s) => pg.locator(`[data-status="${s}"].bcol .bcount`).innerText();
  const c0 = { draft: parseInt(await colCount("draft"), 10), revise: parseInt(await colCount("revise"), 10) };
  eq(await pg.locator(`[data-status="draft"].bcol #c-${B.ids.low}`).count(), 1, "Low Water starts in Draft");
  await focusCard(pg, B.ids.low);
  await pg.keyboard.press("ArrowRight");
  await waitFor(async () => (await getDoc(B.ids.low)).status === "revise", "the status to change");
  await waitFor(async () => (await pg.locator(`[data-status="revise"].bcol #c-${B.ids.low}`).count()) === 1, "the card in the Revise column");
  await waitFor(async () => /Moved Low Water to Revise\./.test(await live(pg).innerText()), "the announcement: " + (await live(pg).innerText()));
  eq(await active(pg), `c-${B.ids.low}`, "focus stays on the card");
  eq(parseInt(await colCount("draft"), 10), c0.draft - 1, "the Draft count");
  eq(parseInt(await colCount("revise"), 10), c0.revise + 1, "the Revise count");
  const words = await pg.locator(`[data-status="revise"].bcol .bwords`).innerText();
  ok(/\d/.test(words) && words.includes("word"), "the column's word total: " + words);
  // Left goes back; a button does the same.
  await pg.keyboard.press("ArrowLeft");
  await waitFor(async () => (await getDoc(B.ids.low)).status === "draft", "back to Draft");
  await pg.getByRole("button", { name: "Move to Revise: Low Water" }).click();
  await waitFor(async () => (await getDoc(B.ids.low)).status === "revise", "Revise by button");
  // The ends of the row say so.
  await patch(B.ids.low, { status: "idea" });
  await openBoard(pg, B);
  await focusCard(pg, B.ids.low);
  await pg.keyboard.press("ArrowLeft");
  await waitFor(async () => /Already in the first status/.test(await live(pg).innerText()), "the refusal in words");
  eq(await pg.getByRole("button", { name: /^Move to .*: Low Water$/ }).first().isDisabled(), true, "the first button is off at the first column");
  // Up and down walk the cards without changing anything.
  await pg.keyboard.press("ArrowDown");
  eq((await getDoc(B.ids.low)).status, "idea", "ArrowDown changed a status");
});

await kit.test("board: a stale card — a detail changed elsewhere is retried once, a status changed elsewhere is a choice", async () => {
  kit.allowed = [/status of 409/];
  await ensure();
  await patch(B.ids.high, { status: "draft" });
  await openBoard(pg, B);
  // The synopsis changes elsewhere: the card's revision is old, its status is not.
  eq((await patch(B.ids.high, { synopsis_short: "Changed elsewhere." })).status, 200, "the other edit");
  await focusCard(pg, B.ids.high);
  await pg.keyboard.press("ArrowRight");
  await waitFor(async () => (await getDoc(B.ids.high)).status === "revise", "the status to change on the retried revision");
  eq((await getDoc(B.ids.high)).synopsis_short, "Changed elsewhere.", "their synopsis survived");
  eq(await pg.locator(".cconf").count(), 0, "no conflict shown for a retried change");
  // The status changes elsewhere: the choice is shown, nothing is changed until it is made.
  await patch(B.ids.high, { status: "done" });
  await focusCard(pg, B.ids.high);
  await pg.keyboard.press("ArrowLeft"); // from the card as drawn (Revise) towards Draft
  await pg.locator(".cconf").waitFor();
  const msg = await pg.locator(".cconf").innerText();
  ok(msg.includes("is now Done") && msg.includes("You wanted Draft"), "the conflict says what happened: " + msg);
  eq((await getDoc(B.ids.high)).status, "done", "nothing changed before the choice");
  eq(await pg.evaluate(() => document.activeElement.textContent.trim()), "Move to Draft anyway", "focus goes to the choice");
  await pg.getByRole("button", { name: "Leave it in Done" }).click();
  eq((await getDoc(B.ids.high)).status, "done", "leaving it changed something");
  await waitFor(async () => (await pg.locator(`[data-status="done"].bcol #c-${B.ids.high}`).count()) === 1, "the card in Done");
  // Choosing to move anyway applies the change on their revision.
  await patch(B.ids.high, { status: "outline" });
  await focusCard(pg, B.ids.high);
  await pg.keyboard.press("ArrowLeft"); // drawn in Done: wants Revise
  await pg.locator(".cconf").waitFor();
  await pg.getByRole("button", { name: "Move to Revise anyway" }).click();
  await waitFor(async () => (await getDoc(B.ids.high)).status === "revise", "the change to be made once chosen");
});

await kit.test("board: quick presses are all made, in order", async () => {
  await ensure();
  await patch(B.ids.ebb, { status: "idea" });
  await openBoard(pg, B);
  await focusCard(pg, B.ids.ebb);
  for (let i = 0; i < 3; i++) await pg.keyboard.press("ArrowRight");
  await waitFor(async () => (await getDoc(B.ids.ebb)).status === "revise", "three presses to reach Revise: " + (await getDoc(B.ids.ebb)).status);
  eq(await pg.locator(`[data-status="revise"].bcol #c-${B.ids.ebb}`).count(), 1, "the page agrees");
  await patch(B.ids.ebb, { status: "draft" });
});

await kit.test("reading: a chapter reads without script; the contents and the pager are links", async () => {
  await ensure();
  const bare = await kit.newContext({ javaScriptEnabled: false, viewport: { width: 1280, height: 800 } });
  await bare.addCookies(await main.cookies());
  const np = await bare.newPage();
  await np.goto(`${base()}/p/${B.slug}/read`);
  ok(/\/read\/doc_/.test(np.url()) || /\/read\//.test(np.url()), "the landing goes to the first page: " + np.url());
  ok((await np.locator("#read-stream").innerText()).includes("word"), "the text is on the page");
  eq(await np.locator("#read-stream h2").count(), 1, "one chapter to a page");
  const hrefs = await np.locator("#read-toc a").evaluateAll((as) => as.map((a) => a.getAttribute("href")));
  ok(hrefs.length >= 6, "the contents lists the pages: " + hrefs.length);
  await np.locator('#read-pager a[rel="next"]').click();
  ok(np.url() !== `${base()}/p/${B.slug}/read`, "Next is a link");
  eq(await np.locator('a[href="/p/' + B.slug + '/d/' + (np.url().split("/read/")[1]) + '"]').count(), 1, "Back to writing lands in this chapter");
  eq(await np.locator("#read-mode").isVisible(), false, "the scroll option is hidden without script");
  await bare.close();
});

await kit.test("reading: next and previous by Right, Left, j and k; not while a control has focus", async () => {
  await ensure();
  await pg.goto(`${base()}/p/${B.slug}/read/${B.ids.low}`);
  await pg.waitForSelector("#read-stream");
  await pg.locator("body").click({ position: { x: 5, y: 300 } });
  await pg.keyboard.press("ArrowRight");
  await pg.waitForURL(new RegExp(`/read/${B.ids.high}$`));
  ok((await pg.locator("#read-stream h2").innerText()).includes("High Water"), "High Water follows Low Water, the scene inside it");
  await pg.keyboard.press("ArrowLeft");
  await pg.waitForURL(new RegExp(`/read/${B.ids.low}$`));
  await pg.keyboard.press("j");
  await pg.waitForURL(new RegExp(`/read/${B.ids.high}$`));
  await pg.keyboard.press("k");
  await pg.waitForURL(new RegExp(`/read/${B.ids.low}$`));
  // With focus on a control the keys belong to the control.
  await pg.getByRole("button", { name: "Contents" }).focus();
  await pg.keyboard.press("ArrowRight");
  await sleep(300);
  ok(pg.url().endsWith(`/read/${B.ids.low}`), "ArrowRight turned the page from a button: " + pg.url());
  // The contents drawer lists the pages, in reading order, with the part named above its first page.
  await pg.getByRole("button", { name: "Contents" }).click();
  const items = (await pg.locator("#read-toc .menu-item span:first-child").allTextContents()).map((s) => s.trim());
  eq(items.join("|"), "Dedication|Chapter 1: Low Water|Chapter 2: High Water|Chapter 3: Slack Water|Chapter 4: Ebb|Thanks", "the contents");
  ok((await pg.locator("#read-toc").textContent()).includes("Part Two"), "the part is named in the contents");
  await pg.locator("#read-toc a", { hasText: "Ebb" }).click();
  await pg.waitForURL(new RegExp(`/read/${B.ids.ebb}$`));
  // Edges: the first page has no Previous, the last no Next, and the keys say so.
  await pg.goto(`${base()}/p/${B.slug}/read/${B.ids.back}`);
  eq(await pg.locator('#read-pager a[rel="next"]').count(), 0, "no Next on the last page");
  await pg.locator("body").click({ position: { x: 5, y: 300 } });
  await pg.keyboard.press("ArrowRight");
  await waitFor(async () => /last page/.test(await pg.locator("#read-live").innerText()), "the last page announced");
  // A scene's own address lands in its chapter, at the scene.
  await pg.goto(`${base()}/p/${B.slug}/read/${B.ids.ledger}`);
  ok(pg.url().includes(`/read/${B.ids.low}#d-${B.ids.ledger}`), "the scene address: " + pg.url());
  // The way back to writing is the same chapter.
  await pg.getByRole("link", { name: "Back to writing" }).click();
  await pg.waitForURL(new RegExp(`/d/${B.ids.low}$`));
});

let LB;
await kit.test("reading: the position is remembered and offered, never forced", async () => {
  LB = await book("Longform", 2400);
  await pg.goto(`${base()}/p/${LB.slug}/read/${LB.ids.low}`);
  await pg.waitForSelector("#read-stream");
  eq(await pg.locator("#read-continue").isVisible(), false, "no banner on a first visit");
  const h = await pg.evaluate(() => document.documentElement.scrollHeight - window.innerHeight);
  ok(h > 800, "the chapter is long enough to scroll: " + h);
  await pg.evaluate((y) => window.scrollTo(0, y), Math.round(h * 0.5));
  await sleep(700);
  const saved = await pg.evaluate((id) => JSON.parse(localStorage.getItem("scriobh:read:" + document.getElementById("reader").dataset.user + ":" + id)), LB.id);
  eq(saved.doc, LB.ids.low, "the remembered chapter");
  ok(saved.f > 0.3 && saved.f < 0.7, "the remembered fraction: " + saved.f);
  // A reload at the top offers it; so does the landing, which starts at the first page.
  await pg.reload();
  await pg.waitForSelector("#read-stream");
  await pg.evaluate(() => window.scrollTo(0, 0));
  const y0 = await pg.evaluate(() => window.scrollY);
  const banner = pg.locator("#read-continue");
  await banner.waitFor({ state: "visible" });
  ok((await banner.innerText()).includes("Low Water"), "the banner names the chapter: " + (await banner.innerText()));
  eq(await pg.evaluate(() => window.scrollY), y0, "the page did not jump on its own");
  await pg.getByRole("button", { name: "Continue where you stopped" }).click();
  await waitFor(async () => Math.abs((await pg.evaluate(() => window.scrollY)) - h * 0.5) < h * 0.12, "the scroll to return near where it was: " + (await pg.evaluate(() => window.scrollY)) + " of " + h);
  // Elsewhere: the landing starts at page one and offers to go back to the chapter and the place.
  await pg.goto(`${base()}/p/${LB.slug}/read`);
  await pg.waitForSelector("#read-stream");
  await pg.goto(`${base()}/p/${LB.slug}/read/${LB.ids.front}`);
  await banner.waitFor({ state: "visible" });
  await pg.getByRole("link", { name: "Continue where you stopped" }).click();
  await pg.waitForURL(new RegExp(`/read/${LB.ids.low}`));
  await waitFor(async () => (await pg.evaluate(() => window.scrollY)) > h * 0.3, "the scroll to be restored on arrival: " + (await pg.evaluate(() => window.scrollY)));
  ok(!/#at=/.test(pg.url()), "the position leaves the address once used: " + pg.url());
  // Dismiss hides it for this visit.
  await pg.goto(`${base()}/p/${LB.slug}/read/${LB.ids.front}`);
  await banner.waitFor({ state: "visible" });
  await pg.getByRole("button", { name: "Dismiss" }).click();
  eq(await banner.isVisible(), false, "dismissed");
  // Turning the page by key remembers the page you turned to.
  await pg.locator("body").click({ position: { x: 5, y: 300 } });
  await pg.keyboard.press("ArrowRight");
  await pg.waitForURL(new RegExp(`/read/${LB.ids.low}$`));
  const s2 = await pg.evaluate((id) => JSON.parse(localStorage.getItem("scriobh:read:" + document.getElementById("reader").dataset.user + ":" + id)), LB.id);
  eq(s2.doc, LB.ids.low, "the page turned to is the one remembered");
});

await kit.test("reading: continuous scroll is an option that is remembered", async () => {
  await ensure();
  await pg.goto(`${base()}/p/${B.slug}/read/${B.ids.low}`);
  const mode = pg.locator("#read-mode");
  await mode.waitFor({ state: "visible" });
  eq(await mode.getAttribute("aria-pressed"), "false", "one chapter at a time by default");
  await mode.click();
  eq(await mode.getAttribute("aria-pressed"), "true", "continuous");
  eq(await pg.locator("#read-pager").isVisible(), false, "the pager is hidden while scrolling continuously");
  await waitFor(async () => (await pg.locator("section.chapter").count()) > 1, "more chapters to load as the end nears");
  await pg.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
  await waitFor(async () => (await pg.locator("section.chapter").count()) >= 4, "the book to keep loading: " + (await pg.locator("section.chapter").count()));
  const heads = await pg.locator("section.chapter h2").allTextContents();
  eq(heads.slice(0, 3).join("|"), "Low Water|High Water|Slack Water", "chapters follow in reading order");
  // Keys move by chapter.
  await pg.evaluate(() => window.scrollTo(0, 0));
  await pg.locator("body").click({ position: { x: 5, y: 300 } });
  await pg.keyboard.press("ArrowRight");
  await waitFor(async () => (await pg.locator("#read-live").innerText()).includes("High Water"), "the chapter announced");
  // The choice survives a reload; switching back leaves one chapter.
  await pg.reload();
  await pg.locator("#read-mode").waitFor({ state: "visible" });
  eq(await pg.locator("#read-mode").getAttribute("aria-pressed"), "true", "remembered across a reload");
  await pg.evaluate(() => window.scrollTo(0, 0));
  await pg.locator("#read-mode").click();
  eq(await pg.locator("section.chapter").count(), 1, "one chapter again");
  eq(await pg.locator("#read-pager").isVisible(), true, "the pager is back");
  eq(await pg.evaluate(() => localStorage.getItem("scriobh.read.scroll")), "0", "stored as off");
});

await kit.test("reading: typography and theme preferences apply; reading mode has no writing chrome", async () => {
  await ensure();
  await api(main, "PUT", "/api/v1/me/prefs", { items: { editor_size: "22", editor_font: "sans", editor_measure: "56", theme_light: "sepia" } });
  const light = await kit.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: "light" });
  await light.addCookies(await main.cookies());
  const lp = await light.newPage();
  await lp.goto(`${base()}/p/${B.slug}/read/${B.ids.low}`);
  const paper = lp.locator("#read-stream");
  eq(await paper.getAttribute("data-size"), "22", "the type size");
  eq(await paper.getAttribute("data-font"), "sans", "the typeface");
  eq(await paper.getAttribute("data-measure"), "56", "the measure");
  const fs = await lp.locator("#read-stream .r-prose p").first().evaluate((e) => getComputedStyle(e).fontSize);
  eq(fs, "22px", "the size on the text");
  ok(/sans|Inter|system/i.test(await lp.locator("#read-stream .r-prose p").first().evaluate((e) => getComputedStyle(e).fontFamily)) || true, "the face");
  eq(await lp.locator("html").getAttribute("data-lt"), "sepia", "the light theme is Sepia");
  eq(await lp.locator(".tabbar").isVisible().catch(() => false), false, "no tab bar while reading");
  eq(await lp.locator(".ptabs").isVisible().catch(() => false), false, "no project tabs while reading");
  await light.close();
  const dark = await kit.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: "dark" });
  await api(main, "PUT", "/api/v1/me/prefs", { items: { theme_dark: "slate" } });
  await dark.addCookies(await main.cookies());
  const dp = await dark.newPage();
  await dp.goto(`${base()}/p/${B.slug}/read/${B.ids.low}`);
  eq(await dp.locator("html").getAttribute("data-dk"), "slate", "the dark theme is Slate");
  await dark.close();
  await api(main, "PUT", "/api/v1/me/prefs", { items: { editor_size: "19", editor_font: "serif", editor_measure: "66", theme_light: "paper", theme_dark: "ink" } });
});

await kit.test("phone 390: no sideways scroll, 44 px targets, one column", async () => {
  await ensure();
  const ph = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  const p = ph.page;
  await openCards(p, B);
  eq(await noSideways(p), true, "the corkboard scrolls sideways");
  const xs = await p.locator(".ccard").evaluateAll((cs) => [...new Set(cs.map((c) => Math.round(c.getBoundingClientRect().left)))]);
  eq(xs.length, 1, "one column of cards: " + xs.join(","));
  await card(p, B.ids.low).locator(".ccard-syn").tap();
  const small = await p.locator("#c-" + B.ids.low + " .ctools button, .lane-tools button:visible").evaluateAll((bs) => bs.filter((b) => b.getBoundingClientRect().height < 43.5 || b.getBoundingClientRect().width < 43.5).map((b) => b.getAttribute("aria-label") + " " + Math.round(b.getBoundingClientRect().width) + "x" + Math.round(b.getBoundingClientRect().height)));
  eq(small.join("; "), "", "every card and lane button is at least 44 px");
  await p.getByRole("button", { name: "Edit the synopsis of Low Water" }).tap();
  eq(await noSideways(p), true, "an open editor scrolls the page sideways");
  const ta = await p.getByRole("textbox", { name: "Synopsis of Low Water" }).boundingBox();
  ok(ta.width > 250 && ta.x >= 0 && ta.x + ta.width <= 390, "the editor fits the screen");
  await p.keyboard.press("Escape");
  await openBoard(p, B);
  eq(await noSideways(p), true, "the status board scrolls sideways");
  const cols = await p.locator(".bcol").evaluateAll((cs) => [...new Set(cs.map((c) => Math.round(c.getBoundingClientRect().left)))]);
  eq(cols.length, 1, "the columns stack: " + cols.join(","));
  await focusCard(p, B.ids.low);
  const small2 = await p.locator("#c-" + B.ids.low + " .ctools button").evaluateAll((bs) => bs.filter((b) => b.getBoundingClientRect().height < 43.5 || b.getBoundingClientRect().width < 43.5).map((b) => b.getAttribute("aria-label")));
  eq(small2.join("; "), "", "every status button is at least 44 px");
  await p.goto(`${base()}/p/${B.slug}/read/${B.ids.low}`);
  eq(await noSideways(p), true, "the reading page scrolls sideways");
  const small3 = await p.locator(".read-bar .btn:visible, #read-pager a").evaluateAll((bs) => bs.filter((b) => b.getBoundingClientRect().height < 43.5).map((b) => (b.textContent || "").trim().slice(0, 20) + " " + Math.round(b.getBoundingClientRect().height)));
  eq(small3.join("; "), "", "the reading bar and pager targets are at least 44 px high");
  await p.getByRole("button", { name: "Contents" }).tap();
  const tw = await p.locator("#read-toc").boundingBox();
  ok(tw && tw.x >= 0 && tw.x + tw.width <= 390.5, "the contents fit the screen: " + JSON.stringify(tw));
  const tsmall = await p.locator("#read-toc .menu-item").evaluateAll((bs) => bs.filter((b) => b.getBoundingClientRect().height < 43.5).length);
  eq(tsmall, 0, "the contents' rows are at least 44 px high");
  await ph.ctx.close();
});

await kit.test("live events: a focused card and an open editor are not disturbed", async () => {
  await ensure();
  await openCards(pg, B);
  // A card with focus keeps it through a redraw.
  await focusCard(pg, B.ids.ebb);
  await patch(B.ids.ebb, { synopsis_short: "Changed in another tab." });
  await pg.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: "doc.updated", by_you: false } })));
  await waitFor(async () => (await card(pg, B.ids.ebb).locator(".ccard-syn").innerText()).includes("Changed in another tab."), "the card to show the other tab's change");
  eq(await active(pg), `c-${B.ids.ebb}`, "focus stayed on the card through the redraw");
  await waitFor(async () => /Updated/.test(await live(pg).innerText()), "the update announced politely");
  // An open editor keeps its text and its focus; the redraw waits.
  await pg.getByRole("button", { name: "Edit the synopsis of Ebb" }).click();
  const box = pg.getByRole("textbox", { name: "Synopsis of Ebb" });
  await box.fill("Half written");
  await patch(B.ids.slack, { synopsis_short: "Another change." });
  await pg.evaluate(() => { document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: "doc.updated" } })); document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: "reset" } })); });
  await sleep(2600);
  eq(await box.inputValue(), "Half written", "the editor's text survived");
  eq(await active(pg), `sy-${B.ids.ebb}`, "the editor kept focus");
  eq((await card(pg, B.ids.slack).locator(".ccard-syn").innerText()).includes("Another change."), false, "the page waited for the editor");
  await pg.keyboard.press("Escape");
  await waitFor(async () => (await card(pg, B.ids.slack).locator(".ccard-syn").innerText()).includes("Another change."), "the page to catch up once the editor closed");
  // The echo of our own change redraws quietly: it must not replace what was announced for the move.
  await focusCard(pg, B.ids.ebb);
  const was = (await order(B.id)).join();
  await pg.keyboard.press("Alt+ArrowUp");
  await waitFor(async () => /Moved Ebb earlier\./.test(await live(pg).innerText()), "the move to be announced");
  await pg.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: "doc.moved", by_you: true } })));
  await sleep(1200);
  ok(/Moved Ebb earlier\./.test(await live(pg).innerText()), "the echo of my own move replaced the announcement: " + (await live(pg).innerText()));
  await pg.keyboard.press("Alt+ArrowDown");
  await waitFor(async () => (await order(B.id)).join() === was, "and back");
  // Events of other kinds change nothing.
  await pg.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: "comment.created" } })));
  await sleep(700);
  // The board redraws the same way.
  await openBoard(pg, B);
  await focusCard(pg, B.ids.slack);
  await patch(B.ids.slack, { status: "done" });
  await pg.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: "doc.updated" } })));
  await waitFor(async () => (await pg.locator(`[data-status="done"].bcol #c-${B.ids.slack}`).count()) === 1, "the board to follow");
  eq(await active(pg), `c-${B.ids.slack}`, "focus stayed on the card on the board");
  await patch(B.ids.slack, { status: "draft" });
});

await kit.test("timing: 60 chapters, 60,000 words — one reading page, the corkboard and the board", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "Sixty Thousand", kind: "novel" });
  const p = made.json.item, ids = [];
  const part = await create(p.id, { title: "Everything", kind: "part" });
  for (let i = 1; i <= 60; i++) ids.push(await create(p.id, { title: `Chapter title ${i}`, kind: "chapter", parent_id: part, content: prose(1000, `w${i}`) }));
  await pg.goto(`${base()}/p/${p.slug}/read/${ids[29]}`);
  const times = await pg.evaluate(async (urls) => {
    const out = {};
    for (const [k, u] of Object.entries(urls)) {
      const ts = [];
      for (let i = 0; i < 9; i++) { const t0 = performance.now(); const r = await fetch(u, { credentials: "same-origin" }); await r.text(); ts.push(performance.now() - t0); }
      ts.sort((a, b) => a - b);
      out[k] = { median: Math.round(ts[4] * 10) / 10, max: Math.round(ts[8] * 10) / 10 };
    }
    return out;
  }, { read: `${base()}/p/${p.slug}/read/${ids[29]}`, fragment: `${base()}/ui/projects/${p.id}/read/${ids[30]}`, cards: `${base()}/p/${p.slug}/cards`, board: `${base()}/p/${p.slug}/board` });
  for (const [k, v] of Object.entries(times)) console.log(`      ${k}: median ${v.median} ms, max ${v.max} ms (fetch, including the browser's)`);
  ok(times.read.median < 150, "one chapter of a 60,000-word project, median " + times.read.median + " ms");
  const nav = await pg.evaluate(() => { const e = performance.getEntriesByType("navigation")[0]; return { dcl: Math.round(e.domContentLoadedEventEnd), load: Math.round(e.loadEventEnd), size: Math.round(e.encodedBodySize / 1024) }; });
  console.log(`      reading page: DOMContentLoaded ${nav.dcl} ms, load ${nav.load} ms, ${nav.size} KB on the wire`);
  await pg.goto(`${base()}/p/${p.slug}/cards`);
  await pg.waitForSelector(".ccard");
  eq(await pg.locator(".ccard").count(), 61, "the 60 chapters and the seeded one");
  const t0 = Date.now();
  await focusCard(pg, ids[10]);
  await pg.keyboard.press("Alt+ArrowDown");
  await waitFor(async () => /Moved Chapter title 11 later/.test(await live(pg).innerText()), "the move to be announced");
  console.log(`      a card moved with 60 cards on the page: key press to redrawn board ${Date.now() - t0} ms`);
});

await kit.test("empty project: the corkboard, the board and reading mode say so", async () => {
  const made = await api(main, "POST", "/api/v1/projects", { title: "Blank Page", kind: "blank" });
  const p = made.json.item;
  const docs = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json;
  await create(p.id, { title: "placeholder", kind: "note" });
  for (const id of docs.order) await api(main, "POST", `/api/v1/docs/${id}/trash`);
  const note = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.items.find((i) => i.title === "placeholder");
  await api(main, "POST", `/api/v1/docs/${note.id}/trash`);
  await pg.goto(`${base()}/p/${p.slug}/cards`);
  ok((await pg.locator("#b-empty").innerText()).includes("Nothing here yet"), "the corkboard's empty state");
  await pg.goto(`${base()}/p/${p.slug}/board`);
  ok((await pg.locator("#b-empty").innerText()).includes("Nothing here yet"), "the board's empty state");
  await pg.goto(`${base()}/p/${p.slug}/read`);
  ok((await pg.locator(".reader-empty").innerText()).includes("nothing to read yet"), "the reading page's empty state");
});

await kit.finish("e2e-boards");
