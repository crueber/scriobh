#!/usr/bin/env node
// Review of ui-7-import: steps that FAIL on the branch and pass once the defect is fixed.
//   node scripts/e2e-review-ui7.mjs
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { makeBigEpub, makeDocx, makeEpub } from "./fixtures/books.mjs";

const kit = await createKit("e2e-review-ui7");
kit.deadline(9 * 60 * 1000);
const { sleep } = kit;
const owner = await kit.signedIn({}, "writer@dev.scriobh");
kit.main = owner.page;
const pg = owner.page;
const base = () => kit.base();
const openDocx = async () => {
  await pg.goto(base() + "/import");
  await pg.locator("#imp-f-docx").check();
  await pg.setInputFiles("#imp-docx", makeDocx(join(kit.tmp, "d" + Math.random().toString(36).slice(2, 6) + ".docx"), kit.tmp));
  await pg.waitForSelector("#imp-h");
  await sleep(500);
};
// Nest chapters ten levels deep through the page itself (what Nest under... allows): Level 2 in Level 1, ... Level 9 in Level 8, a chapter in Level 9.
async function deepen(p) {
  const title = (t) => p.locator(".imp-node").filter({ has: p.locator("> .imp-row > .imp-title", { hasText: t }) }).last();
  const nest = async (t, into) => {
    const row = title(t);
    await row.locator("summary").first().click();
    await row.getByRole("button", { name: /^Nest .* under another$/ }).first().click();
    const sel = p.getByRole("combobox", { name: "Nest under" });
    await sel.selectOption(await sel.locator("option", { hasText: into }).first().getAttribute("value"));
    await p.getByRole("button", { name: "Move here" }).click();
    await sleep(700);
  };
  for (let d = 2; d <= 9; d++) await nest(new RegExp(`^Level ${d} part`), new RegExp(`^Level ${d - 1} part`));
  await nest(/^Chapter 36/, /^Level 9 part/);
  await sleep(1500); // the last preview has drawn the list again
}
const edit = async (title) => { const d = pg.locator(`.imp-node:has(> .imp-row > .imp-title:text-is("${title}"))`).first().locator("details.imp-edit").first(); if ((await d.getAttribute("open")) === null) await d.locator("summary").click(); return d; };

await kit.test("Join into... with nothing to join into is disabled, with a sentence, and nothing is listed as left out", async () => {
  await openDocx();
  await (await edit("Two")).locator('[data-act="skip"]').click();
  await sleep(900);
  const d = await edit("One");
  const join = d.locator('[data-bk="merge"]');
  ok(!(await join.isEnabled()), "the Join control is enabled with no chapter to join into");
  ok(/There is no other chapter to join this into/.test(await d.innerText()), "no sentence says why: " + (await d.innerText()));
  eq(await join.getAttribute("aria-describedby") !== null, true, "the sentence is not tied to the control");
  const left = (await pg.locator(".imp-left").innerText().catch(() => "")).includes("One");
  ok(!left, "'One' is listed under Left out of the import and is still in the tree");
});

await kit.test("after Put back by keyboard the focus is not thrown to the top of the page", async () => {
  await openDocx();
  await (await edit("Two")).locator('[data-act="skip"]').click();
  await sleep(900);
  const back = pg.locator('.imp-left [data-act="back"]');
  await back.focus();
  await pg.keyboard.press("Enter");
  await sleep(1200);
  ok(await pg.evaluate(() => document.activeElement && document.activeElement !== document.body), "focus is on <body> after Put back");
});

await kit.test("a part cannot be joined, and the page says what to do instead; a chapter's join says nothing about chapters inside it", async () => {
  await pg.goto(base() + "/import");
  await pg.locator("#imp-f-epub").check();
  await pg.setInputFiles("#imp-epub", makeEpub(join(kit.tmp, "m.epub"), "ok", kit.tmp));
  await pg.waitForSelector("#imp-h");
  await sleep(600);
  const d = await edit("Part One: The Marsh");
  ok(!(await d.locator('[data-bk="merge"]').isEnabled()) && /cannot be joined/.test(await d.innerText()), "a part can be joined: " + (await d.innerText()));
  const c = await edit("The Wall");
  await c.locator('[data-bk="merge"]').click();
  await pg.locator(".imp-to select").selectOption({ label: "The Road (in Part One: The Marsh)" });
  await pg.locator('.imp-to [data-act="merge"]').click();
  await sleep(1300);
  const left = await pg.locator(".imp-left").innerText();
  ok(/The Wall \(joined into The Road\)/.test(left) && !/inside it/.test(left), "the left-out list: " + left.replace(/\n/g, " "));
});

await kit.test("Word authors in the pairs line carry the same '(from a Word file)' label as the authors list", async () => {
  await openDocx();
  const t = await pg.locator("#imp-result").innerText();
  const pairs = (t.match(/Reviewers whose changes met[^\n]*/) || [""])[0];
  ok(pairs, "the fixture has no pair of authors that met");
  ok(/from a Word file/.test(pairs), "the pairs line names authors with no label: " + pairs);
});

await kit.test("the server refuses a merge into a chapter that is left out, in whichever order the edits arrive", async () => {
  const b64 = readFileSync(makeEpub(join(kit.tmp, "o.epub"), "ok", kit.tmp)).toString("base64");
  const first = await kit.api(owner.ctx, "POST", "/api/v1/imports/preview", { name: "o", epub: b64 });
  eq(first.status, 200, "preview");
  const key = (t) => first.json.nodes.find((n) => n.title === t).key;
  const road = key("The Road"), wall = key("The Wall");
  const a = await kit.api(owner.ctx, "POST", "/api/v1/imports/preview", { name: "o", epub: b64, edits: [{ key: wall, skip: true }, { key: road, merge_into: wall }] });
  const b = await kit.api(owner.ctx, "POST", "/api/v1/imports/preview", { name: "o", epub: b64, edits: [{ key: road, merge_into: wall }, { key: wall, skip: true }] });
  eq(a.status, 422, "skip first, then merge into it");
  eq(b.status, 422, "merge first, then skip the target (accepted: the text of The Road would be in no document, " + b.json?.documents + " documents)");
});

await kit.test("a chapter that others are joined into cannot be left out until they are put back, and a chapter cannot be joined into one that is left out", async () => {
  await pg.goto(base() + "/import");
  await pg.locator("#imp-f-epub").check();
  await pg.setInputFiles("#imp-epub", makeEpub(join(kit.tmp, "m2.epub"), "ok", kit.tmp));
  await pg.waitForSelector("#imp-h");
  await sleep(600);
  const c = await edit("The Wall");
  await c.locator('[data-bk="merge"]').click();
  await pg.locator(".imp-to select").selectOption({ label: "The Road (in Part One: The Marsh)" });
  await pg.locator('.imp-to [data-act="merge"]').click();
  await sleep(1300);
  await (await edit("The Road")).locator('[data-act="skip"]').click();
  await sleep(500);
  ok(/Put The Wall back first/.test(await pg.locator("#imp-status").innerText()), "no sentence: " + (await pg.locator("#imp-status").innerText()));
  ok((await pg.locator(".imp-tree .imp-title", { hasText: "The Road" }).count()) > 0, "the chapter was left out");
  // and the part that holds it
  await (await edit("Part One: The Marsh")).locator('[data-act="skip"]').click();
  await sleep(500);
  ok(/back first/.test(await pg.locator("#imp-status").innerText()), "a part that holds the target was left out");
  await pg.locator('.imp-left [data-act="back"]').click();
  await sleep(1000);
  ok(await pg.evaluate(() => document.activeElement && document.activeElement.tagName !== "BODY"), "focus is lost after undoing a join");
  await (await edit("The Road")).locator('[data-act="skip"]').click();
  await sleep(1000);
  ok(/The Road/.test(await pg.locator(".imp-left").innerText()), "the chapter could not be left out once nothing was joined into it");
});

await kit.test("a move is announced where it went, in a region the status line does not clear, for long enough to be heard", async () => {
  await pg.goto(base() + "/import");
  await pg.locator("#imp-f-epub").check();
  await pg.setInputFiles("#imp-epub", makeEpub(join(kit.tmp, "m3.epub"), "ok", kit.tmp));
  await pg.waitForSelector("#imp-h");
  await sleep(600);
  const d = await edit("The Wall");
  await d.getByRole("button", { name: "Move The Wall up" }).click();
  await sleep(1500);
  const said = await pg.locator("#imp-live").innerText();
  ok(/Moved The Wall above The Road: now 1 of 3/.test(said), "the announcement: " + said);
  await sleep(2000);
  ok(/Moved The Wall/.test(await pg.locator("#imp-live").innerText()), "the announcement was gone after 3.5 s");
  eq(await pg.locator("#imp-status").getAttribute("aria-live"), "polite", "the status line is not polite");
});

await kit.test("Move to top and Move to bottom reach the ends of a long list", async () => {
  await pg.goto(base() + "/import");
  await pg.locator("#imp-f-epub").check();
  await pg.setInputFiles("#imp-epub", makeBigEpub(join(kit.tmp, "big.epub"), kit.tmp));
  await pg.waitForSelector("#imp-h");
  await sleep(1200);
  const row = pg.locator(".imp-node:has(> .imp-row > .imp-title:text-is(\"Part 1: a part with a long name that has to wrap on a phone\")) > ul > .imp-node").nth(4);
  const title = await row.locator("> .imp-row > .imp-title").innerText();
  await row.locator("summary").first().click();
  await row.getByRole("button", { name: /to the top$/ }).first().focus();
  await pg.keyboard.press("Enter");
  await sleep(1500);
  const first = await pg.locator(".imp-node:has(> .imp-row > .imp-title:text-is(\"Part 1: a part with a long name that has to wrap on a phone\")) > ul > .imp-node").first().locator("> .imp-row > .imp-title").innerText();
  eq(first, title, "Move to top did not put it first");
  ok(/to the top|above/.test(await pg.locator("#imp-live").innerText()) || /now 1 of 7/.test(await pg.locator("#imp-live").innerText()), "not announced: " + (await pg.locator("#imp-live").innerText()));
});

await kit.test("390 px with a realistic book (36 chapters, long titles, ten parts deep): nothing scrolls sideways, open pickers and the deepest Edit panel fit, down to 195 px", async () => {
  for (const [w, h] of [[390, 800], [320, 700], [195, 600]]) {
    const ctx = await kit.newContext({ viewport: { width: w, height: h }, hasTouch: true, isMobile: true });
    await ctx.addCookies(await owner.ctx.cookies());
    const p = await ctx.newPage();
    kit.watch(p, "phone" + w);
    await p.goto(base() + "/import");
    await p.locator("#imp-f-epub").check();
    await p.setInputFiles("#imp-epub", makeBigEpub(join(kit.tmp, "big2.epub"), kit.tmp));
    await p.waitForSelector("#imp-h");
    await sleep(1500);
    await deepen(p);
    const over = () => p.evaluate((w) => Math.max(0, Math.round(Math.max(...[...document.querySelectorAll("#import, #import *")].map((e) => e.getBoundingClientRect().right)) - w)), w); // the page itself; the application's own tab bar is not this page's
    eq(await over(), 0, `${w}px: the preview scrolls sideways`);
    // the deepest chapter: open Edit and the Join picker
    const deepest = p.locator(".imp-node").filter({ has: p.locator("> .imp-row > .imp-title", { hasText: /^Chapter 36/ }) }).first();
    if ((await deepest.locator("details.imp-edit").first().getAttribute("open")) === null) await deepest.locator("summary").first().click(); // it may be open already: it was edited
    await deepest.getByRole("button", { name: /^Join .* into another$/ }).first().click();
    eq(await over(), 0, `${w}px: the Join picker at the deepest level scrolls sideways`);
    ok(/level 10/.test(await deepest.locator("> .imp-row > .imp-meta").innerText()), "the level is not said in words: " + (await deepest.locator("> .imp-row > .imp-meta").innerText()));
    const r = await deepest.locator(".imp-edit-body").boundingBox();
    ok(r && r.x >= 0 && r.x + r.width <= w + 0.5, `${w}px: the Edit panel runs off the screen (x ${r && r.x}, width ${r && r.width})`);
    for (const b of await deepest.locator(".imp-edit-body button").all()) { const bb = await b.boundingBox(); if (bb) ok(bb.x >= -0.5 && bb.x + bb.width <= w + 0.5, `${w}px: a button runs off the screen: ${await b.innerText()}`); }
    if (w === 390) {
      const small = await deepest.locator(".imp-edit-body button, .imp-edit-body select").evaluateAll((l) => l.filter((e) => e.getBoundingClientRect().height < 43.5 || e.getBoundingClientRect().width < 43.5).map((e) => `${(e.textContent || "").trim().slice(0, 20)} ${Math.round(e.getBoundingClientRect().width)}x${Math.round(e.getBoundingClientRect().height)}`));
      eq(small.join("; "), "", "touch targets under 44 px");
    }
    await ctx.close();
  }
});

await kit.test("the edition's chapter list marks a joined chapter, and shows a file's own name", async () => {
  const b64 = readFileSync(makeEpub(join(kit.tmp, "j.epub"), "ok", kit.tmp)).toString("base64");
  const pv = await kit.api(owner.ctx, "POST", "/api/v1/imports/preview", { name: "j", epub: b64 });
  const road = pv.json.nodes.find((n) => n.title === "The Road").key, wall = pv.json.nodes.find((n) => n.title === "The Wall").key;
  const ap = await kit.api(owner.ctx, "POST", "/api/v1/imports", { name: "j", epub: b64, token: pv.json.token, edits: [{ key: wall, merge_into: road }] });
  eq(ap.status, 201, "apply");
  await pg.goto(`${base()}/p/${ap.json.slug}/editions/${ap.json.edition_id}`);
  const t = await pg.locator(".edn-chapters").innerText();
  ok(/Joined into “The Road”/.test(t) && /Open the document it was joined into/.test(t), "a joined chapter is not marked: " + t.slice(0, 500));
  ok(!/OEBPS\//.test(t), "a path inside the book is in the rows");
});

await kit.test("a coverless book says it has no cover, and a label with a direction override is cleaned (the page and the server)", async () => {
  await pg.goto(base() + "/import");
  await pg.locator("#imp-f-epub").check();
  await pg.setInputFiles("#imp-epub", makeEpub(join(kit.tmp, "c.epub"), "nocover", kit.tmp));
  await pg.waitForSelector("#imp-h");
  await sleep(600);
  const box = await pg.locator(".imp-book").innerText();
  ok(/This book has no cover picture\./.test(box) && !/not kept/.test(box), "the cover sentence: " + box);
  await pg.locator("#imp-label").fill("Plain \u202eesrever label");
  await pg.locator("#imp-t").fill("Coverless");
  await pg.locator("[data-go]").click();
  await pg.locator("#imp-h").waitFor();
  await sleep(800);
  const made = (await kit.api(owner.ctx, "GET", "/api/v1/projects")).json.items.find((p) => p.title === "Coverless");
  const ed = (await kit.api(owner.ctx, "GET", `/api/v1/projects/${made.id}/editions`)).json.items[0];
  ok(!/[\u202a-\u202e\u2066-\u2069]/.test(ed.label) && /Plain/.test(ed.label), "the label keeps a direction override: " + JSON.stringify(ed.label));
  // the server on its own: a label sent straight to the API
  const b64 = readFileSync(makeEpub(join(kit.tmp, "c2.epub"), "ok", kit.tmp)).toString("base64");
  const pv = await kit.api(owner.ctx, "POST", "/api/v1/imports/preview", { name: "c2", epub: b64 });
  const ap = await kit.api(owner.ctx, "POST", "/api/v1/imports", { name: "c2", epub: b64, token: pv.json.token, edition_label: "A\u202eB\u2067C" });
  const e2 = (await kit.api(owner.ctx, "GET", `/api/v1/projects/${ap.json.project_id}/editions`)).json.items[0];
  ok(!/[\u202a-\u202e\u2066-\u2069]/.test(e2.label), "the server kept a direction override: " + JSON.stringify(e2.label));
});

await kit.test("Editions is offered to every role that can read it, in the menu, the More list and the palette", async () => {
  const rd = await kit.signedIn({}, "reader@dev.scriobh");
  const mk = await kit.api(owner.ctx, "POST", "/api/v1/projects", { title: "Roles", kind: "novel" });
  await kit.api(owner.ctx, "POST", `/api/v1/projects/${mk.json.item.id}/members`, { email: "reader@dev.scriobh", role: "reader" });
  await rd.page.goto(`${base()}/p/${mk.json.item.slug}`);
  const has = await rd.page.evaluate(() => !!document.querySelector('a[href$="/editions"]'));
  ok(has, "the reader's project page has no way to the editions");
  await rd.page.goto(`${base()}/p/${mk.json.item.slug}/goals`);
  await rd.page.keyboard.press("ControlOrMeta+k");
  await rd.page.keyboard.type("editions");
  ok(await rd.page.getByText("Go to the editions (imported books) of this project").first().isVisible(), "the palette does not offer it to a reader");
});

await kit.finish("e2e-review-ui7");
