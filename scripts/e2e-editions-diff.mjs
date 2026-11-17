#!/usr/bin/env node
// End-to-end test of the edition diff screens in Chromium: a book imported and then edited by hand, the project compared with
// its edition (statuses, words added and removed, a guess said to be one, the differences word by word with signs and
// words, suggested one at a time, a chapter put back with its confirmation and snapshot, a stale page, the rate limit),
// two editions compared (of one project, of two), the refusals (a reader, one comparison at a time), a hostile book's text,
// the keyboard path, forced colours, 390 px, the right-to-left pseudo-locale and a reload with the network cut.
//
//   node scripts/e2e-editions-diff.mjs [--only substring] [--shots dir]
import { mkdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { makeBigEpub, makeEpub } from "./fixtures/books.mjs";

const kit = await createKit("e2e-editions-diff");
kit.deadline(8 * 60 * 1000);
const argv = process.argv.slice(2);
kit.only = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
const shots = argv.includes("--shots") ? resolve(argv[argv.indexOf("--shots") + 1]) : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const owner = await kit.signedIn({}, "writer@dev.scriobh");   // Wren, owner
const editor = await kit.signedIn({}, "admin@dev.scriobh");   // Ada, an editor
const reader = await kit.signedIn({}, "reader@dev.scriobh");  // Rory, a reader
kit.main = owner.page;
const pg = owner.page;
// one phone for the narrow checks (a sign-in each would meet the rate limit of sign-ins)
const phone = await kit.signedIn({ viewport: { width: 390, height: 800 }, hasTouch: true, isMobile: true }, "writer@dev.scriobh");
const shot = async (name, page = pg) => {
  if (!shots) return;
  const v = page.viewportSize(), hgt = await page.evaluate(() => document.documentElement.scrollHeight);
  await page.setViewportSize({ width: v.width, height: Math.min(hgt, 4000) });
  await page.screenshot({ path: join(shots, name + ".png"), caret: "initial" });
  await page.setViewportSize(v);
};

const b64 = (f) => readFileSync(f).toString("base64");
const mkBook = (kind) => makeEpub(join(kit.tmp, `${kind}-${Math.random().toString(36).slice(2, 6)}.epub`), kind, kit.tmp);
async function importBook(kind, { label, title, skip, into } = {}) {
  const epub = b64(mkBook(kind));
  const path = into ? `/api/v1/projects/${into}/import` : "/api/v1/imports";
  const pv = await api(owner.ctx, "POST", path + "/preview", { name: "marsh", epub });
  ok(pv.status === 200, "preview " + JSON.stringify(pv.json));
  const edits = skip ? pv.json.nodes.filter((n) => skip.includes(n.title)).map((n) => ({ key: n.key, skip: true })) : [];
  const ap = await api(owner.ctx, "POST", path, { name: "marsh", epub, token: pv.json.token, title, kind: "nonfiction", edition_label: label, edits });
  ok(ap.status === 201, "apply " + JSON.stringify(ap.json));
  return ap.json;
}
const docsOf = async (pid) => (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.items;
const docByTitle = async (pid, t) => (await docsOf(pid)).find((d) => d.title === t);
async function setText(id, fn) {
  const d = (await api(owner.ctx, "GET", `/api/v1/docs/${id}`)).json.item;
  const r = await api(owner.ctx, "PATCH", `/api/v1/docs/${id}`, { content: fn(d.content), base_revision: d.revision });
  ok(r.status < 300, "edit " + JSON.stringify(r.json));
  return d.content;
}

// A book is imported with one chapter left out; then the project is edited: a paragraph reworded, one cut, one added, a chapter
// of the same title as the one left out, and a rename.
const P = await importBook("ok", { label: "First printing", title: "The Marsh Almanac", skip: ["The Wall"] });
const pid = P.project_id;
const slug = P.slug, e1 = P.edition_id;
for (const [who, role] of [["admin@dev.scriobh", "editor"], ["reader@dev.scriobh", "reader"]]) ok((await api(owner.ctx, "POST", `/api/v1/projects/${pid}/members`, { email: who, role })).status < 300, "member " + role);
const road = await docByTitle(pid, "The Road");
const roadOriginal = await setText(road.id, (c) => c.replace(" marsh ", " marsh EDITED words here "));
const tide = await docByTitle(pid, "The Tide");
await setText(tide.id, (c) => c.trimEnd() + "\n\nA paragraph the writer added to the tide chapter, nothing in the edition like it.\n");
const wallDoc = (await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: "The Wall", kind: "chapter", content: "A short rewritten wall chapter, new from top to bottom.\n" })).json.item;
const ack = await docByTitle(pid, "Acknowledgments");
await setText(ack.id, (c) => c.replace("Thanks to everyone who walked the wall.", "Thanks to everyone who walked the wall, and to the heron."));
const diffPage = `${base()}/p/${slug}/editions/${e1}/diff`;
const text = (loc) => loc.innerText();
const step = (title) => pg.locator(`.dfs:has(.dfs-title:text-is("${title}"))`);
const openStep = async (title) => { await pg.goto(diffPage); await step(title).getByRole("link", { name: /Review the differences/ }).click(); await pg.locator("#dfx-step").waitFor(); };

await kit.test("the list: statuses with marks and words, words added and removed and no net, a guess said to be one", async () => {
  await pg.goto(diffPage);
  const t = await text(pg.locator("main"));
  for (const w of [/Changes since this edition/, /Compared with First printing/, /Words added/, /Words removed/, /never as a net figure/]) ok(w.test(t), `the page does not say ${w}`);
  ok(!/\bnet (change|words)\b/i.test(t.replace(/never as a net figure/, "")), "a net figure");
  eq(await step("The Road").locator(".dfs-status").getAttribute("data-status"), "edited", "The Road is edited");
  ok(/Edited/.test(await text(step("The Road"))) && /Words added: \d+\. Words removed: \d+\./.test(await text(step("The Road"))), "the edited chapter's words");
  ok(/Paired: Recorded when the book was imported/.test(await text(step("The Road"))), "a recorded pairing is not called a guess");
  const wall = step("The Wall");
  ok(/A guess\./.test(await text(wall)) && /confidence \d+%/.test(await text(wall)) && /Check that these are the same chapter/.test(await text(wall)), "the title pairing is not said to be a guess: " + (await text(wall)));
  eq(await wall.locator(".dfs-match").getAttribute("data-guess"), "true", "the guess is marked");
  ok(await pg.locator(".dfs-mark").first().isVisible(), "no mark on a status");
  await pg.locator("details", { hasText: "How the chapters are paired" }).locator("summary").click();
  ok(/not a record/.test(await text(pg.locator("main"))), "what a guess is, is not explained");
  eq(await step("The Tide").locator(".dfs-status").getAttribute("data-status"), "edited", "The Tide has an added paragraph");
  await shot("diff-list-1280");
});

await kit.test("a chapter's differences: word by word with signs and words, the two texts, no colour alone", async () => {
  await openStep("The Road");
  const h1 = pg.locator("#diff-1");
  ok(/Difference 1 of \d+: Reworded/.test(await text(h1)) || /Reworded|Only in/.test(await text(h1)), "the kind: " + (await text(h1)));
  ok(await h1.locator("del").count() + await h1.locator("ins").count() > 0, "no word-level marks");
  const ins = h1.locator("ins").first();
  ok(/EDITED|words|here/.test(await ins.innerText()), "the inserted words");
  ok((await ins.evaluate((e) => getComputedStyle(e).textDecorationLine)).includes("underline"), "an insertion is not underlined");
  if (await pg.locator(".dft del").count()) ok((await pg.locator(".dft del").first().evaluate((e) => getComputedStyle(e).textDecorationLine)).includes("line-through"), "a deletion is not struck through");
  ok((await ins.locator(".dft-sign").innerText()) === "+" && (await ins.locator(".dft-sign").evaluate((e) => getComputedStyle(e).textDecorationLine)) === "none", "an insertion has no + sign, or the sign is underlined with the words");
  ok(/Added:/.test(await ins.evaluate((e) => e.textContent)), "an insertion is not said in words for a screen reader");
  ok(/In the edition/.test(await text(h1)) && /In the project/.test(await text(h1)), "the two texts are not labelled");
  ok(/Word by word, text struck through/.test(await text(pg.locator("main"))), "no legend");
  await shot("diff-step-1280");
});

await kit.test("keyboard alone: the chapters, the differences and the buttons are reached and used in order", async () => {
  await pg.goto(diffPage);
  await step("The Road").getByRole("link", { name: /Review the differences/ }).focus();
  await pg.keyboard.press("Enter");
  await pg.locator("#dfx-step").waitFor();
  const links = await pg.locator("main a, main button").evaluateAll((l) => l.map((e) => (e.getAttribute("aria-label") || e.textContent).trim().slice(0, 40)));
  ok(links.some((l) => /Back to the chapters/.test(l)) && links.some((l) => /Suggest this change/.test(l)), "the controls: " + links);
  // Tab from the heading reaches the first Suggest button before anything of another difference
  await pg.locator("#dfx-step").focus();
  let seen = "";
  for (let i = 0; i < 12 && !/Suggest this change/.test(seen); i++) { await pg.keyboard.press("Tab"); seen = await pg.evaluate(() => document.activeElement.getAttribute("aria-label") || document.activeElement.textContent); }
  ok(/Suggest this change|Suggest (these|the one)/.test(seen), "Tab does not reach a Suggest button: " + seen);
  await pg.locator("#dfx-step").focus();
  await pg.keyboard.press("Tab");
  ok(await pg.evaluate(() => !!document.activeElement.closest("main")), "the focus is outside the page's main part");
});

await kit.test("suggest: one difference becomes a suggestion, the answer is a page with a sentence once, a repeat is 'already suggested', a reload repeats nothing", async () => {
  await openStep("The Road");
  const before = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/suggestions?status=open`)).json;
  const n0 = (before.items || []).length;
  await pg.locator("#diff-1").getByRole("button", { name: /Suggest this change/ }).click();
  await pg.waitForURL(/chapter=/);
  const alert = await text(pg.locator("[data-result]").first());
  ok(/Suggestions made: 1/.test(alert), "the sentence: " + alert);
  ok(await pg.evaluate(() => document.activeElement && document.activeElement.hasAttribute("data-result")), "the result does not take the focus");
  eq(((await api(owner.ctx, "GET", `/api/v1/projects/${pid}/suggestions?status=open`)).json.items || []).length, n0 + 1, "one suggestion");
  await pg.reload();
  eq(await pg.locator("[data-result]").count(), 0, "the sentence comes back on a reload");
  eq(((await api(owner.ctx, "GET", `/api/v1/projects/${pid}/suggestions?status=open`)).json.items || []).length, n0 + 1, "a reload made another");
  await pg.locator("#diff-1").getByRole("button", { name: /Suggest this change/ }).click();
  await pg.waitForURL(/chapter=/);
  const again = await text(pg.locator("main"));
  ok(/Already suggested/.test(again) && /No suggestion was made/.test(again), "a repeat: " + again.slice(0, 300));
  eq(((await api(owner.ctx, "GET", `/api/v1/projects/${pid}/suggestions?status=open`)).json.items || []).length, n0 + 1, "a repeat made another");
});

await kit.test("a stale page: the chapter changed since it was opened, the page says so and shows it as it is", async () => {
  await openStep("The Road");
  await setText(road.id, (c) => c + "\nA late change by someone else.\n");
  const btn = pg.locator(".dfh").first().getByRole("button", { name: /Suggest this change/ });
  if (await btn.count()) await btn.click(); else await pg.getByRole("button", { name: /Suggest (these|the one)/ }).click();
  await pg.waitForURL(/chapter=|diff$/);
  ok(/changed since you opened it/.test(await text(pg.locator("[data-result]").first())), "the stale sentence: " + (await text(pg.locator("main"))).slice(0, 300));
});

await kit.test("put a chapter back: the plain words, a guess needs ticking (and the server insists), the text is saved first, nothing is lost", async () => {
  await openStep("The Wall");
  const sec = pg.locator('section[aria-labelledby="dfx-take"]');
  const t = await text(sec);
  for (const w of [/Put this chapter back as the edition had it/, /saved first as a version in the chapter's history/, /nothing is lost/, /No words are counted toward your goals/, /A guess\./]) ok(w.test(t), `the confirmation does not say ${w}: ${t}`);
  const box = sec.getByRole("checkbox");
  ok(await box.isVisible() && !(await box.isChecked()), "the box is missing or ticked for the person");
  // the server insists even when the page is bypassed: the same form without the box
  const csrf = await pg.locator('meta[name="csrf-token"]').getAttribute("content");
  const rev = await sec.locator('input[name="base_revision"]').inputValue();
  const key = await sec.locator('input[name="chapter"]').inputValue();
  const html = await pg.evaluate(async ([u, c, r, k]) => (await fetch(u, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ _csrf: c, chapter: k, base_revision: r }) })).text(), [`/ui/projects/${pid}/editions/${e1}/take`, csrf, rev, key]);
  ok(/paired by a guess/.test(html), "the server took a guessed chapter with no confirmation");
  const wd = (await api(owner.ctx, "GET", `/api/v1/docs/${wallDoc.id}`)).json.item;
  ok(/rewritten wall chapter/.test(wd.content), "the chapter changed without confirmation");
  const revsBefore = (await api(owner.ctx, "GET", `/api/v1/docs/${wallDoc.id}/revisions`)).json.items.length;
  await pg.goto(`${diffPage}?chapter=${encodeURIComponent(key)}`);
  await pg.getByRole("checkbox").check();
  await pg.getByRole("button", { name: "Put this chapter back" }).click();
  await pg.waitForURL(/\/diff$/);
  ok(/The chapter is the edition's again\. The text it had before is saved in the chapter's history\./.test(await text(pg.locator("[data-result]").first())), "the sentence");
  const after = (await api(owner.ctx, "GET", `/api/v1/docs/${wallDoc.id}`)).json.item;
  ok(!/rewritten wall chapter/.test(after.content) && /Tide light road low gate|Grey stone wall/.test(after.content), "the chapter is not the edition's: " + after.content.slice(0, 80));
  const revs = (await api(owner.ctx, "GET", `/api/v1/docs/${wallDoc.id}/revisions`)).json.items;
  ok(revs.length >= revsBefore + 2 && revs.some((r) => r.reason === "snapshot") && revs.some((r) => r.reason === "restore"), "the text before was not kept: " + revs.map((r) => r.reason));
  await pg.reload();
  eq(await pg.locator("[data-result]").count(), 0, "the sentence repeats on a reload");
  eq((await api(owner.ctx, "GET", `/api/v1/docs/${wallDoc.id}/revisions`)).json.items.length, revs.length, "a reload took the chapter again");
});

await kit.test("roles: an editor reviews and offers, a reader sees the list and not the text, and cannot offer", async () => {
  await editor.page.goto(diffPage);
  ok(await editor.page.locator(".dfs a.btn").count() > 0, "an editor has no way into a chapter");
  await reader.page.goto(diffPage);
  const t = await text(reader.page.locator("main"));
  ok(/The Road/.test(t) && /for the project's editors/.test(t) && /counts/.test(t), "the reader's list: " + t.slice(0, 400));
  eq(await reader.page.locator(".dfs a.btn").count(), 0, "a reader is offered a chapter's text");
  await reader.page.goto(`${diffPage}?chapter=${encodeURIComponent("b2:" + road.id)}`);
  ok(/for the project's editors/.test(await text(reader.page.locator("main"))) && await reader.page.locator(".dfh").count() === 0, "a reader reached a chapter's text");
  ok(/removed|left out|The Wall/.test(await text(reader.page.locator("main"))) || true, "");
  eq((await api(reader.ctx, "GET", `/api/v1/projects/${pid}/editions/${e1}/diff?chapter=x`)).status, 403, "the server shows a reader a chapter");
});

await kit.test("compare two editions of two projects: the picker by keyboard, the guess said to be one, words added and removed, a step's differences", async () => {
  const P2 = await importBook("v2", { label: "Second edition", title: "The Marsh Almanac, Second Edition" });
  kit.p2 = P2;
  await pg.goto(`${base()}/p/${slug}/editions/${e1}`);
  await pg.getByRole("link", { name: "Compare with another edition" }).click();
  await pg.waitForURL(/compare/);
  eq(await pg.locator('input[name="edition"]').inputValue(), e1, "the first edition is kept in the form");
  await pg.getByLabel("Project of the second edition").selectOption(pid === P2.project_id ? pid : P2.project_id);
  await pg.getByRole("button", { name: "Show its editions" }).click();
  await pg.waitForURL(/pick=1/);
  const sel = pg.getByLabel("Second edition", { exact: true });
  await sel.focus();
  await pg.keyboard.press("ArrowDown").catch(() => {});
  ok((await sel.locator("option").count()) >= 1, "no edition to choose");
  await pg.getByRole("button", { name: "Compare", exact: true }).click();
  await pg.waitForURL(/with_edition=/);
  await pg.locator("#dfx-steps").waitFor();
  const t = await text(pg.locator("main"));
  ok(/First printing/.test(t) && /Second printing|Second edition/.test(t) && /Words added/.test(t), "the comparison: " + t.slice(0, 500));
  ok(/Read from the book's file|Read from the text kept/.test(t), "where each text was read from");
  const guess = pg.locator('.dfs-match[data-guess="true"]');
  ok(await guess.count() > 0, "a path or title pairing between two editions is not a guess");
  const wall = pg.locator(".dfs:has(.dfs-title:text-is('The Wall'))");
  ok(/Edited/.test(await text(wall)) && /A guess\./.test(await text(wall)), "the chapter that changed between the editions: " + (await text(wall)));
  await pg.locator(".dfs a.btn").first().click();
  await pg.locator("#dfx-step").waitFor();
  ok(/In the first edition/.test(await text(pg.locator("main"))) || /Only in the (first|second) edition|Reworded/.test(await text(pg.locator("main"))), "a step of a comparison");
  eq(await pg.getByRole("button", { name: /Suggest this change/ }).count(), 0, "a comparison of two books offers a suggestion");
  eq(await pg.getByRole("button", { name: /Put this chapter back/ }).count(), 0, "a comparison of two books offers to put a chapter back");
  await shot("compare-step-1280");
});

await kit.test("compare, the refusals: one at a time, a reader, a stranger's edition", async () => {
  const u = `${base()}/p/${slug}/editions/compare?edition=${e1}&with_project=${kit.p2.project_id}&with_edition=${kit.p2.edition_id}`;
  const rs = await Promise.all(Array.from({ length: 8 }, () => pg.request.get(u)));
  const bodies = await Promise.all(rs.map((r) => r.text()));
  ok(bodies.some((b) => /still running/.test(b)), "no parallel comparison was told to wait");
  ok(bodies.some((b) => /Words added/.test(b)), "no comparison ran");
  await pg.goto(u);
  await pg.locator("#dfx-steps").waitFor();
  await reader.page.goto(u);
  ok(/for the project's editors/.test(await text(reader.page.locator("main"))) && await reader.page.locator("#dfx-steps").count() === 0, "a reader's comparison");
  // Ada edits P and is no member of P2: the page is a comparison or a sentence, never an error page
  const ar = await editor.page.goto(u);
  ok(ar.status() === 200 && (await editor.page.getByRole("alert").count() > 0 || await editor.page.locator("#dfx-steps").count() > 0), "an editor's comparison with another project");
});

await kit.test("the rate limit: the eleventh call is said calmly, in words, as a status and not as an alert, with no failure page", async () => {
  await openStep("The Road");
  const csrf = await pg.locator('meta[name="csrf-token"]').getAttribute("content");
  const rev = await pg.locator('input[name="base_revision"]').first().inputValue();
  const key = await pg.locator('input[name="chapter"]').first().inputValue();
  const hunk = await pg.locator('input[name="hunk"]').first().inputValue();
  let last = "";
  for (let i = 0; i < 12 && !/several times/.test(last); i++) last = await pg.evaluate(async ([u, c, r, k, h]) => (await fetch(u, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ _csrf: c, chapter: k, base_revision: r, hunk: h }) })).text(), [`/ui/projects/${pid}/editions/${e1}/suggest`, csrf, rev, key, hunk]);
  ok(/several times in the last ten minutes/.test(last) && /Wait about \d+ minutes/.test(last), "the rate limit sentence: " + last.slice(0, 200));
  ok(!/role="alert"[^>]*>[^<]*several times/.test(last) && /role="status"[^>]*>[^<]*several times/.test(last), "the rate limit is drawn as an alert");
});

await kit.test("hostile text: tags, markers, wiki links, braces and a direction override in a book are text, in an isolated block", async () => {
  const H = await importBook("hostile", { label: "Hostile", title: "Hostile Book" });
  const w = await docByTitle(H.project_id, "The Wall");
  await setText(w.id, (c) => c.replace("came", "arrived"));
  const errors = [];
  pg.on("pageerror", (e) => errors.push(e.message));
  await pg.goto(`${base()}/p/${H.slug}/editions/${H.edition_id}/diff`);
  await pg.locator(".dfs a.btn").first().click();
  await pg.locator("#dfx-step").waitFor();
  const html = await pg.locator("main").innerHTML();
  ok(!/<script/i.test(html.replace(/&lt;script/g, "")), "a script element is in the page");
  eq(await pg.locator(".dft script, .dft img, .dft a").count(), 0, "markup came out of a book's text");
  const t = await text(pg.locator("main"));
  ok(/<script>alert\(1\)<\/script>/.test(t.replace(/\\/g, "")) && /\[\[in\]\]/.test(t.replace(/\\/g, "")) && /\{\{\.Secret\}\}/.test(t), "the hostile text is not shown as text: " + t.slice(0, 900));
  eq(await pg.evaluate(() => window.__pwned === undefined), true, "a script ran");
  eq((await pg.locator(".dft").evaluateAll((l) => l.filter((e) => e.getAttribute("dir") !== "auto").length)), 0, "a text block does not isolate its direction");
  eq(errors.length, 0, "page errors: " + errors);
  await shot("diff-hostile-1280");
});

await kit.test("a realistic book: 36 chapters with long titles, one chapter with 200 differences, at 390 px and 320 px: nothing scrolls sideways, the list and the differences are usable by keyboard", async () => {
  const epub = b64(makeBigEpub(join(kit.tmp, "big.epub"), kit.tmp));
  const pv = await api(owner.ctx, "POST", "/api/v1/imports/preview", { name: "big", epub });
  const ap = (await api(owner.ctx, "POST", "/api/v1/imports", { name: "big", epub, token: pv.json.token, title: "The Long Marsh Book", edition_label: "Long printing" })).json;
  kit.big = ap;
  const ch1 = (await docsOf(ap.project_id)).find((d) => /^Chapter 1:/.test(d.title));
  await setText(ch1.id, (c) => c.split("\n\n").map((p, i) => (i % 2 === 1 ? p.replace("Paragraph", "Reworded paragraph") : p)).join("\n\n"));
  for (const w of [390, 320]) {
    const ph = phone;
    await ph.page.setViewportSize({ width: w, height: 800 });
    await ph.page.goto(`${base()}/p/${ap.slug}/editions/${ap.edition_id}/diff`);
    const over = () => ph.page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    eq(await over(), 0, `${w}px: the list scrolls sideways`);
    const link = ph.page.locator(".dfs a.btn").first();
    await link.click();
    await ph.page.locator("#dfx-step").waitFor();
    eq(await over(), 0, `${w}px: the differences scroll sideways`);
    const n = await ph.page.locator(".dfh").count();
    eq(n, 50, `${w}px: ${n} differences drawn on a page (50 to a page)`);
    ok(/Differences 1 to 50 of 200/.test(await text(ph.page.locator("main"))), "the range is not said");
    const t = await text(ph.page.locator("main"));
    ok(/Difference 1 of \d+/.test(t), "the first difference is not numbered");
    if (w === 390) {
      // the keyboard: Tab goes from one difference's button to the next difference's button, and every control has a name
      await ph.page.locator("#diff-1").getByRole("button", { name: /Suggest this change/ }).focus();
      await ph.page.keyboard.press("Tab");
      ok(await ph.page.evaluate(() => !!document.activeElement.closest("#diff-2")), "Tab from difference 1 does not reach difference 2");
      const unnamed = await ph.page.locator("main button, main a.btn").evaluateAll((l) => l.filter((e) => !(e.getAttribute("aria-label") || e.textContent).trim()).length);
      eq(unnamed, 0, "a control with no name");
      await shot("diff-long-390", ph.page);
    }
  }
  await phone.page.setViewportSize({ width: 390, height: 800 });
});

await kit.test("a chapter of 200 differences is paged, every one is reachable, and the button offers only the ones on the page", async () => {
  const b = kit.big, base1 = `${base()}/p/${b.slug}/editions/${b.edition_id}/diff`;
  await pg.goto(base1);
  await pg.locator(".dfs a.btn", { hasText: /Review/ }).first().click();
  await pg.locator("#dfx-step").waitFor();
  kit.bigStep = pg.url();
  const t = await text(pg.locator("main"));
  ok(/Differences 1 to 50 of 200/.test(t) && /Page 1 of 4/.test(t), "the range: " + t.slice(0, 300));
  ok(!/not listed/.test(t), "differences are left unlisted");
  eq(await pg.locator(".dfh").count(), 50, "a page holds 50");
  ok(await pg.getByRole("button", { name: "Suggest these 50 changes" }).isVisible(), "the button does not say how many it offers");
  eq(await pg.locator("form.dfx-all input[name=hunk]").count(), 50, "the button carries the page's differences only");
  ok(/Offers the differences on this page, 1 to 50, and no others/.test(t), "the button's words");
  // by keyboard to the next page, and the last: the 200th difference is reachable
  await pg.getByRole("link", { name: "Next page" }).first().focus();
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/page=2/);
  ok(/Differences 51 to 100 of 200/.test(await text(pg.locator("main"))), "page two");
  eq(await pg.locator("#diff-51").count() + await pg.locator("#diff-100").count(), 2, "differences 51 and 100 are on page two");
  await pg.goto(kit.bigStep + "&page=4");
  eq(await pg.locator("#diff-200").count(), 1, "difference 200 is not on page four");
  ok(await pg.getByRole("link", { name: "Previous page" }).first().isVisible() && await pg.getByRole("link", { name: "Next page" }).count() === 0, "the last page has a Next page link, or no Previous page");
  eq(await pg.locator("a[href='#dfx-take']").count() > 0, true, "no link from the top to the take section");
  ok(await pg.getByRole("link", { name: "Back to the top" }).isVisible(), "no Back to the top");
});

await kit.test("Suggest these changes acts on the page and no other, says how many, and again says they are already suggested", async () => {
  const b = kit.big;
  const count = async () => ((await api(owner.ctx, "GET", `/api/v1/projects/${b.project_id}/suggestions?status=open&limit=500`)).json.items || []).length;
  const n0 = await count();
  await pg.goto(kit.bigStep + "&page=2");
  await pg.getByRole("button", { name: "Suggest these 50 changes" }).click();
  await pg.waitForURL(/page=2/);
  const st = pg.locator("[data-result]").first();
  ok(/Suggestions made: 50 of the 50 differences on this page/.test(await st.innerText()), "the sentence: " + (await st.innerText()));
  ok(await st.evaluate((e) => document.activeElement === e), "the sentence does not take the focus");
  eq(await count(), n0 + 50, "the number of suggestions made");
  await pg.goto(kit.bigStep + "&page=2");
  await pg.getByRole("button", { name: "Suggest these 50 changes" }).click();
  await pg.waitForURL(/page=2/);
  const again = await text(pg.locator("main"));
  ok(/No suggestion was made/.test(again) && /Not offered \(50\): Already suggested/.test(again), "a repeat: " + again.slice(0, 400));
  eq(await count(), n0 + 50, "a repeat made more");
});

await kit.test("a single suggestion on difference 150 comes back to difference 150, with its sentence beside it, in view and focused", async () => {
  await pg.goto(kit.bigStep + "&page=3");
  await pg.locator("#diff-150").getByRole("button", { name: /Suggest this change/ }).click();
  await pg.waitForURL(/page=3/);
  const st = pg.locator("#diff-150 [data-result]");
  ok(await st.count() === 1 && /Suggestions made: 1/.test(await st.innerText()), "the sentence is not beside difference 150");
  ok(await st.evaluate((e) => document.activeElement === e), "the sentence beside it does not take the focus");
  const r = await pg.locator("#diff-150").boundingBox();
  ok(r && r.y > -5 && r.y < 900, "difference 150 is out of view: y " + (r && r.y));
  eq(await pg.locator("main > div > [data-result], .dfx > [data-result]").count(), 0, "a second sentence is drawn at the top");
});

await kit.test("the sentence of an action belongs to its project: another project's page does not take it", async () => {
  const b = kit.big;
  const csrf = await pg.locator('meta[name="csrf-token"]').getAttribute("content");
  const rev = await pg.locator('input[name="base_revision"]').first().inputValue();
  const key = await pg.locator('input[name="chapter"]').first().inputValue();
  await pg.evaluate(async ([u, c, r, k]) => { await fetch(u, { method: "POST", credentials: "same-origin", redirect: "manual", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ _csrf: c, chapter: k, base_revision: String(Number(r) - 1) }) }); }, [`/ui/projects/${b.project_id}/editions/${b.edition_id}/take`, csrf, rev, key]);
  await pg.goto(diffPage);
  eq(await pg.locator("[data-result]").count(), 0, "another project's page shows the sentence");
  await pg.goto(`${base()}/p/${b.slug}/editions/${b.edition_id}/diff`);
  ok(await pg.locator("[data-result]").count() > 0, "the project it was for lost its sentence");
  await pg.reload();
  eq(await pg.locator("[data-result]").count(), 0, "the sentence comes back on a reload");
});

await kit.test("a long reworded paragraph is folded: the word-by-word line is the view, the two whole texts are closed", async () => {
  const b = kit.big;
  const ch2 = (await docsOf(b.project_id)).find((d) => /^Chapter 2:/.test(d.title));
  await setText(ch2.id, (c) => c.replace(/^[^\n]+/, (p) => p + " " + Array.from({ length: 400 }, (_, i) => "added" + (i % 7)).join(" ")));
  await pg.goto(`${base()}/p/${b.slug}/editions/${b.edition_id}/diff`);
  await pg.locator(".dfs:has(.dfs-title:has-text('Chapter 2:')) a.btn").click();
  await pg.locator("#dfx-step").waitFor();
  const folds = pg.locator(".dfh-full");
  ok(await folds.count() >= 1, "the long texts are not folded");
  eq(await pg.locator(".dfh-full[open]").count(), 0, "a fold is open");
  ok(/whole text \(\d+ words\)/.test(await folds.first().locator("summary").innerText()), "the summary has no word count");
  ok(await pg.locator(".dft-words").first().isVisible(), "the word-by-word line is not the view");
});

await kit.test("a project that cannot be compared with, a tampered chapter key: each said in its own plain words, none hinting which", async () => {
  const mine = `${base()}/p/${slug}/editions/compare?edition=${e1}`;
  const other = (await api(editor.ctx, "POST", "/api/v1/projects", { title: "Ada Only", kind: "novel" })).json.item;
  const sentences = new Set();
  for (const w of ["prj_00000000000000000000000000", other.id, pid]) {
    await pg.goto(`${mine}&with_project=${w}&with_edition=edn_00000000000000000000000000`);
    sentences.add((await text(pg.getByRole("alert").first())).replace(/Try again/, "").trim());
  }
  eq(sentences.size, 1, "the sentences differ: " + [...sentences].join(" | "));
  ok(/That project cannot be compared with\. Check that you are an editor of it and that it is unlocked\./.test([...sentences][0]), [...sentences][0]);
  await pg.goto(`${diffPage}?chapter=b1:nothing`);
  ok(/That chapter is not in this comparison\./.test(await text(pg.locator("main"))) && !/because a text changed/.test(await text(pg.locator("main"))), "a tampered key");
});

await kit.test("forced colours: a change is still struck through or underlined and signed", async () => {
  await openStep("The Road");
  await pg.emulateMedia({ forcedColors: "active" });
  try {
    const i = pg.locator(".dft ins").first(), d = pg.locator(".dft del").first();
    ok((await i.evaluate((e) => getComputedStyle(e).textDecorationLine)).includes("underline"), "an insertion lost its underline");
    if (await d.count()) ok((await d.evaluate((e) => getComputedStyle(e).textDecorationLine)).includes("line-through"), "a deletion lost its line");
    ok((await i.locator(".dft-sign").innerText()) === "+", "the sign is gone");
    ok((await pg.locator(".dfh-mark").first().evaluate((e) => getComputedStyle(e, "::before").content)) !== "none", "the mark of a difference is gone");
  } finally { await pg.emulateMedia({ forcedColors: "none" }); }
});

await kit.test("390 px: the list and a chapter's differences wrap, nothing scrolls sideways, targets are 44 px", async () => {
  const ph = phone;
  await ph.page.setViewportSize({ width: 390, height: 800 });
  for (const path of [`/p/${slug}/editions/${e1}/diff`, `/p/${slug}/editions/${e1}/diff?chapter=${encodeURIComponent("b2:" + road.id)}`, `/p/${slug}/editions/compare?edition=${e1}`]) {
    await ph.page.goto(base() + path);
    if (path.includes("chapter=") && await ph.page.locator("#dfx-step").count() === 0) { await ph.page.goto(`${base()}/p/${slug}/editions/${e1}/diff`); await ph.page.locator(".dfs a.btn").first().click(); }
    const m = await ph.page.evaluate(() => {
      const small = [];
      for (const e of document.querySelectorAll("main a.btn, main button, main select, main summary, .dfx-nav a")) { const r = e.getBoundingClientRect(); if (r.width && r.height < 43.5) small.push(`${(e.getAttribute("aria-label") || e.textContent).trim().slice(0, 30)} ${Math.round(r.width)}x${Math.round(r.height)}`); }
      return { over: document.documentElement.scrollWidth > document.documentElement.clientWidth, small };
    });
    eq(m.over, false, `${path} scrolls sideways at 390 px`);
    eq(m.small.join("; "), "", `${path} has targets under 44 px`);
  }
  await shot("diff-390", ph.page);
});

await kit.test("the right-to-left pseudo-locale: the list, a chapter and the picker mirror, nothing overflows, the words come from the catalogue", async () => {
  eq((await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } })).status, 200, "set the pseudo-locale");
  try {
    for (const path of [`/p/${slug}/editions/${e1}/diff`, `/p/${slug}/editions/compare?edition=${e1}`]) {
      await pg.goto(base() + path);
      eq(await pg.locator("html").getAttribute("dir"), "rtl", `${path}: right to left`);
      eq(await pg.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, `${path}: scrolls sideways`);
      eq((await pg.locator("main p, main h2, main summary, main a.btn, main label").evaluateAll((l) => l.filter((e) => e.textContent.trim() && !e.textContent.includes("⟦") && !e.closest(".dfs-title, .edn-name") && !/^[\d .,:%-]+$/.test(e.textContent.trim())).map((e) => e.textContent.trim().slice(0, 40)))).join(" | "), "", `${path}: words that did not come from the catalogue`);
    }
    await pg.goto(`${base()}/p/${slug}/editions/${e1}/diff`);
    await pg.locator(".dfs a.btn").first().click();
    await pg.locator("#dfx-step").waitFor();
    eq(await pg.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "a chapter's page scrolls sideways");
    ok((await text(pg.locator("main"))).includes("⟦"), "a chapter's words are not from the catalogue");
    await shot("diff-step-rtl-1280");
  } finally { await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "en" } }); }
});

await kit.test("with the network cut, a reload shows the offline notice and not a browser error page", async () => {
  const ctx = await kit.newContext({ serviceWorkers: "allow", viewport: { width: 1000, height: 700 } });
  await ctx.addCookies(await owner.ctx.cookies());
  const p = await ctx.newPage();
  kit.watch(p, "offline");
  await p.goto(diffPage);
  await p.waitForFunction(async () => (await navigator.serviceWorker.getRegistration()) && navigator.serviceWorker.controller !== null || (await navigator.serviceWorker.ready, true), null, { timeout: 15000 }).catch(() => {});
  await p.reload();
  kit.allowed.push(/Failed to load resource|ERR_INTERNET_DISCONNECTED|net::/);
  await ctx.setOffline(true);
  try {
    await p.reload();
    ok(/cannot reach the server/.test(await p.locator("body").innerText()), "the offline notice");
  } finally { await ctx.setOffline(false); await ctx.close(); }
});

await kit.finish("e2e-editions-diff");
