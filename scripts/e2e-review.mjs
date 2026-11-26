#!/usr/bin/env node
// Review in Chromium: an editor reads a whole book in the reading view and leaves 40 anchored comments (by mouse
// and by keyboard), a reviewer replies and mentions, the writer works through the queue and resolves them in one
// pass; suggestions are made in suggestion mode and decided; a reader sees none of it; 390 px and keyboard only.
//
//   node scripts/e2e-review.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { mkdirSync } from "node:fs";

const kit = await createKit("e2e-review");
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const owner = await kit.signedIn({}, "writer@dev.scriobh");        // Wren, owner
const editor = await kit.signedIn({}, "admin@dev.scriobh");        // Ada, an editor of this book
const reviewer = await kit.signedIn({}, "reader@dev.scriobh");     // Rory, a reviewer
const CH = 12;

// ---- a book with unique, findable sentences --------------------------------------------------------------------
const made = await api(owner.ctx, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Wren Writer" });
const P = made.json.item, pid = P.id, slug = P.slug;
const sentence = (c, s) => `Sentence ${c}.${s} says the *tide* came in over the stones, and nobody on the quay counted the boats.`;
const first = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
const chapters = [];
for (let c = 1; c <= CH; c++) {
  const body = Array.from({ length: 6 }, (_, p) => Array.from({ length: 3 }, (_, s) => sentence(c, p * 3 + s + 1)).join(" ")).join("\n\n") + "\n";
  if (c === 1) { const g = (await api(owner.ctx, "GET", `/api/v1/docs/${first}`)).json.item; await api(owner.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, title: `Chapter ${c}`, content: body }); chapters.push(first); }
  else chapters.push((await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: `Chapter ${c}`, kind: "chapter", content: body })).json.item.id);
}
const mem = async (email, role) => { const r = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/members`, { email, role }); ok(r.status < 300, `add member ${role}: ${r.status} ${JSON.stringify(r.json)}`); };
await mem("admin@dev.scriobh", "editor");
await mem("reader@dev.scriobh", "reviewer");

const readURL = (id) => `${kit.base()}/p/${slug}/read/${id}`;

// select the words by their text, as a mouse drag would leave the page
const select = (page, needle) => page.evaluate((needle) => {
  for (const b of document.querySelectorAll(".r-prose p")) {
    const txt = b.textContent, i = txt.indexOf(needle);
    if (i < 0) continue;
    const tw = document.createTreeWalker(b, NodeFilter.SHOW_TEXT);
    let n, pos = 0, r = document.createRange(), started = false;
    while ((n = tw.nextNode())) {
      const len = n.nodeValue.length;
      if (!started && i < pos + len) { r.setStart(n, i - pos); started = true; }
      if (started && i + needle.length <= pos + len) { r.setEnd(n, i + needle.length - pos); break; }
      pos += len;
    }
    b.scrollIntoView({ block: "center" });
    const sel = getSelection(); sel.removeAllRanges(); sel.addRange(r);
    return true;
  }
  return false;
}, needle);
const comment = async (page, body, how) => {
  if (how === "button") { await page.waitForSelector(".rv-pop-c", { state: "visible" }); await page.click(".rv-pop-c"); }
  else await page.keyboard.press("c");
  await page.waitForSelector("#rv-new");
  await page.fill("#rv-new", body);
  const before = await page.locator(".rv-card").count();
  await page.keyboard.press("ControlOrMeta+Enter");
  await kit.waitFor(async () => (await page.locator(".rv-card").count()) > before, "the new card");
};

await kit.test("the reading view for an editor has the margin and an empty list", async () => {
  const page = editor.page; kit.main = page;
  await page.goto(readURL(chapters[0]), { waitUntil: "load" });
  await page.waitForSelector("#rv-margin");
  ok(await page.locator("#rv-toggle").isVisible(), "the Comments button");
  eq(await page.locator("mark.rv-mark").count(), 0, "no comment yet, no marks");
  await kit.waitFor(async () => (await page.locator("#rv-empty").innerText()).includes("No open comments"), "the empty margin says so");
});

await kit.test("an editor reads the book and leaves 40 anchored comments", async () => {
  const page = editor.page; kit.main = page;
  let made_ = 0;
  for (let c = 1; c <= CH; c++) {
    await page.goto(readURL(chapters[c - 1]), { waitUntil: "load" });
    await page.waitForSelector("#rv-margin");
    await kit.waitFor(async () => (await page.locator("#rv-empty").innerText()) !== "" || (await page.locator(".rv-card").count()) > 0, "the margin loads");
    const n = c <= 4 ? 4 : 3;
    for (let k = 0; k < n; k++) {
      const needle = `Sentence ${c}.${k * 4 + 2} says the tide came`;
      ok(await select(page, needle), "found " + needle);
      await comment(page, `Comment ${c}.${k}: this sentence says too much about the tide.`, "button");
      made_++;
    }
    eq((await page.locator("mark.rv-mark").count()) >= n, true, "marks for the new comments in chapter " + c);
  }
  eq(made_, 40, "40 made");
  const all = await api(editor.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=200`);
  eq(all.json.total, 40, "40 open threads");
  ok(all.json.items.every((t) => t.anchor && t.anchor.state === "active"), "every anchor is active");
});

await kit.test("a reviewer replies with a mention and the writer finds it under Mentions me", async () => {
  const page = reviewer.page; kit.main = page;
  await page.goto(readURL(chapters[1]), { waitUntil: "load" });
  await page.waitForSelector(".rv-card");
  const card = page.locator(".rv-card").first();
  await card.getByRole("button", { name: "Reply" }).click();
  await page.waitForSelector('textarea[id^="rv-r-"]');
  const ta = page.locator('textarea[id^="rv-r-"]');
  await ta.fill("Agreed, @Wr");
  await page.waitForSelector(".rv-pick li");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  ok((await ta.inputValue()).startsWith("Agreed, @Wren Writer "), "the picked name: " + await ta.inputValue());
  await ta.press("End");
  await page.keyboard.type("please cut it.");
  await page.keyboard.press("ControlOrMeta+Enter");
  await kit.waitFor(async () => (await page.locator(".rv-replies li").count()) === 1, "the reply is in the thread");
  // the writer's margin, filtered to the threads that mention them
  const o = owner.page; kit.main = o;
  await o.goto(readURL(chapters[1]), { waitUntil: "load" });
  await o.waitForSelector(".rv-card");
  await o.getByRole("button", { name: "Mentions me", exact: true }).click();
  eq(await o.locator(".rv-card").count(), 1, "one thread mentions the writer");
  ok((await o.locator(".rv-card").innerText()).includes("please cut it"), "it is the reviewer's reply");
});

await kit.test("keyboard only: place the cursor, select words, comment, walk the list", async () => {
  const page = reviewer.page; kit.main = page;
  await page.goto(readURL(chapters[4]), { waitUntil: "load" });
  await page.waitForSelector("#rv-margin");
  await kit.waitFor(async () => (await page.locator(".rv-card").count()) > 0, "the margin loads");
  const before = await api(reviewer.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=1`);
  await page.keyboard.press("]");
  await page.keyboard.press("Alt+Shift+ArrowRight");
  await page.keyboard.press("Alt+Shift+ArrowRight");
  await page.keyboard.press("Alt+Shift+ArrowRight");
  await page.keyboard.press("c");
  await page.waitForSelector("#rv-new");
  await page.keyboard.type("Keyboard comment on the opening words.");
  await page.keyboard.press("ControlOrMeta+Enter");
  await kit.waitFor(async () => (await api(reviewer.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=1`)).json.total === before.json.total + 1, "the comment was made");
  const mine = (await api(reviewer.ctx, "GET", `/api/v1/projects/${pid}/comments?author=me&status=open&limit=5`)).json.items.find((t) => t.body.startsWith("Keyboard comment"));
  ok(mine && mine.anchor.quote.startsWith("Sentence 5.1"), "anchored on the opening words: " + (mine && mine.anchor.quote));
  // into the list and along it
  await page.keyboard.press("m");
  const focused = () => page.evaluate(() => (document.activeElement.closest(".rv-card") || {}).id || "");
  await kit.waitFor(async () => (await focused()).startsWith("rv-t-"), "focus in the list");
  const a = await focused();
  await page.keyboard.press("j");
  const b = await focused();
  ok(b && b !== a, "j moves to the next card");
  await page.keyboard.press("k");
  eq(await focused(), a, "k moves back");
  await page.keyboard.press("Enter"); // show in the text
  eq(await page.evaluate(() => document.activeElement.tagName), "MARK", "Enter puts focus on the words in the text");
});

await kit.test("the writer resolves every open comment in one pass, with a preview and an expected count", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${kit.base()}/p/${slug}/review`, { waitUntil: "load" });
  const total = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=1`)).json.total;
  ok(total >= 41, "at least 41 open: " + total);
  ok((await page.locator("#rq-sum").innerText()).includes(`${total} open comments`), "the page counts them: " + await page.locator("#rq-sum").innerText());
  eq(await page.locator(".rq-chapters a").count(), CH + 1, "the chapter strip: all and twelve chapters");
  await page.getByRole("button", { name: /^Resolve all/ }).click();
  await page.waitForSelector("#rq-plan");
  ok((await page.locator("#rq-plan").innerText()).includes(`resolves ${total} comments`), "the preview says how many");
  eq((await api(owner.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=1`)).json.total, total, "nothing changed yet");
  // a comment arrives between the preview and the confirmation: the set changed, so nothing is resolved
  const extra = await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { body: "A late comment on the book." });
  ok(extra.status === 201, "late comment " + extra.status);
  await page.getByRole("button", { name: /^Resolve \d+$/ }).click();
  await page.waitForSelector("[role=alert]");
  ok((await page.locator("[role=alert]").innerText()).includes("changed"), "the changed set is refused, in words");
  eq((await api(owner.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=1`)).json.total, total + 1, "still open");
  // look again, confirm
  await page.getByRole("button", { name: /^Resolve all/ }).click();
  await page.waitForSelector("#rq-plan");
  await page.getByRole("button", { name: /^Resolve \d+$/ }).click();
  await page.waitForSelector(".saved");
  ok((await page.locator(".saved").innerText()).includes(`${total + 1} comments resolved`), "done: " + await page.locator(".saved").innerText());
  eq((await api(owner.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=1`)).json.total, 0, "none open");
  ok(await page.locator("#rq-none").isVisible(), "the page says everything has been looked at");
});

const docText = async (id) => (await api(owner.ctx, "GET", `/api/v1/docs/${id}`)).json.item.content;
const suggestOn = async (page) => { await page.getByRole("button", { name: "Suggesting", exact: true }).click(); await kit.waitFor(async () => (await page.getByRole("button", { name: "Suggesting", exact: true }).getAttribute("aria-pressed")) === "true", "Suggesting on"); };
const suggest = async (page, needle, kind, text) => {
  await page.locator("#reader").focus(); // after a suggestion is sent focus is in the margin; the keys of the page are the page's
  ok(await select(page, needle), "found " + needle);
  await page.keyboard.press("s");
  await page.waitForSelector("#rv-sw");
  await page.selectOption("#rv-sw", kind);
  if (text != null) await page.fill("#rv-st", text);
  const resp = page.waitForResponse((r) => r.url().includes("/suggestions") && r.request().method() === "POST", { timeout: 4000 }).catch(() => null);
  await page.keyboard.press("ControlOrMeta+Enter");
  const rr = await resp;
  if (rr && rr.status() >= 300) throw new Error("suggestion.create answered " + rr.status() + " " + (await rr.text()).slice(0, 300));
  if (!rr) throw new Error("the form sent nothing");
  await kit.waitFor(async () => !(await page.locator("#rv-sw").count()), "the composer closes (" + (await page.locator("#rv-compose").innerText()).replace(/\s+/g, " ").slice(0, 300) + ")");
};

await kit.test("suggestion mode: a reviewer replaces, deletes and inserts; the marks show and nothing changes", async () => {
  const page = reviewer.page; kit.main = page;
  const c = chapters[2], before = await docText(c);
  await page.goto(readURL(c), { waitUntil: "load" });
  await page.waitForSelector("#rv-margin");
  await suggestOn(page);
  await suggest(page, "Sentence 3.2 says the tide came", "replace", "Sentence 3.2 says the sea came");
  await kit.waitFor(async () => (await page.locator("del.rv-del").count()) >= 1, "the struck words");
  await suggest(page, "Sentence 3.5 says the tide came", "delete", null);
  await suggest(page, "Sentence 3.8 says", "after", " (so it is said)");
  await kit.waitFor(async () => (await page.locator("ins.rv-ins").count()) >= 3, "the new words");
  eq(await docText(c), before, "the chapter text did not change");
  const list = (await api(reviewer.ctx, "GET", `/api/v1/projects/${pid}/suggestions?document_id=${c}&status=open`)).json;
  eq(list.total, 3, "three open suggestions");
  ok(await page.locator("#rv-suggest-n").innerText() === "3", "the tab counts them");
  // the struck words are still readable as the old text
  ok((await page.locator("del.rv-del").allInnerTexts()).join("").includes("Sentence 3.2 says the tide came"), "the old words stay, struck through");
  const css = await page.locator("del.rv-del").first().evaluate((e) => getComputedStyle(e).textDecorationLine);
  ok(css.includes("line-through"), "a strikethrough");
  // A screenshot leaves empty style attributes of its own on the page (the screenshot's caret hiding), so the live-DOM check skips the page after one.
  if (shots) { await page.screenshot({ path: `${shots}/review-suggestions-1280.png` }); kit.main = null; }
});

await kit.test("the writer sees them in context and in a list, accepts one, and all the rest with a plan", async () => {
  const page = owner.page; kit.main = page;
  const c = chapters[2];
  await page.goto(readURL(c), { waitUntil: "load" });
  await page.waitForSelector("#rv-suggest-n");
  await kit.waitFor(async () => (await page.locator("del.rv-del").count()) >= 2, "marks for the writer");
  await page.getByRole("button", { name: /^Suggestions/ }).click();
  eq(await page.locator("#rv-sugg .rv-card").count(), 3, "three cards in the list");
  // accept the replacement
  const card = page.locator("#rv-sugg .rv-card").filter({ hasText: "the sea came" });
  await card.getByRole("button", { name: "Accept", exact: true }).click();
  await kit.waitFor(async () => (await docText(c)).includes("Sentence 3.2 says the sea came"), "the text is changed by the accept");
  await kit.waitFor(async () => (await page.evaluate(() => document.activeElement && document.activeElement.tagName)) !== "BODY", "focus is not dropped after Accept");
  ok(await page.evaluate(() => !!document.activeElement.closest("#rv-sugg, .rv-tabs")), "focus stays in the Suggestions list");
  await kit.waitFor(async () => (await page.locator("#read-stream").innerText()).includes("Sentence 3.2 says the sea came"), "the page shows the new words");
  eq(await page.locator("#rv-sugg .rv-card").count(), 2, "two left");
  // the rest, as a plan: an overlap is made first (a reviewer suggests over the words of another)
  const rev = (await api(owner.ctx, "GET", `/api/v1/docs/${c}`)).json.item.revision;
  const o1 = await api(reviewer.ctx, "POST", `/api/v1/projects/${pid}/suggestions`, { document_id: c, base_revision: rev, plain: true, changes: [{ quote: "Sentence 3.5 says", text: "Sentence 3.5 insists" }] });
  ok(o1.status === 201, "an overlapping suggestion: " + JSON.stringify(o1.json));
  await page.reload({ waitUntil: "load" });
  await page.getByRole("button", { name: /^Suggestions/ }).click();
  await page.getByRole("button", { name: /^Accept all/ }).click();
  await page.waitForSelector("#rv-sugg .rv-compose");
  eq(await page.evaluate(() => document.activeElement.textContent), "Cancel", "the plan opens with focus on Cancel, not on the button that applies it");
  ok(await page.getByRole("button", { name: /^Accept \d+$/ }).isDisabled(), "and the button that applies it waits");
  await page.keyboard.press("Escape");
  await kit.waitFor(async () => (await page.locator("#rv-sugg .rv-compose").count()) === 0, "Escape cancels the plan");
  eq(await page.evaluate(() => document.activeElement.textContent), "Accept all…", "focus goes back to where the plan was asked for");
  await page.getByRole("button", { name: /^Accept all/ }).click();
  await page.waitForSelector("#rv-sugg .rv-compose");
  const plan = await page.locator("#rv-sugg .rv-compose").innerText();
  ok(/would be applied/.test(plan) && /would be left out/.test(plan) && /overlaps/.test(plan), "the plan lists what is skipped and why: " + plan);
  eq((plan.match(/comes before/g) || []).length, 1, "the overlap is said once: " + plan);
  await page.getByRole("button", { name: /^Accept \d+$/ }).click();
  await kit.waitFor(async () => (await docText(c)).includes("(so it is said)"), "the insertion was applied");
  const now = await docText(c);
  ok(!now.includes("Sentence 3.5 says the tide came") || !now.includes("Sentence 3.5 insists"), "one of the overlapping pair applied at most");
  const rv = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/revisions?document_id=${c}`));
  void rv;
});

await kit.test("a suggestion made on words that then changed is refused in words, never applied", async () => {
  kit.allowed.push(/409/);
  const c = chapters[3], page = owner.page; kit.main = page;
  const d = (await api(owner.ctx, "GET", `/api/v1/docs/${c}`)).json.item;
  const s = await api(reviewer.ctx, "POST", `/api/v1/projects/${pid}/suggestions`, { document_id: c, base_revision: d.revision, plain: true, changes: [{ quote: "Sentence 4.2 says the tide came", text: "Sentence 4.2 says the sea came" }] });
  ok(s.status === 201, "made " + s.status);
  await api(owner.ctx, "PATCH", `/api/v1/docs/${c}`, { base_revision: d.revision, content: d.content.replace("Sentence 4.2 says the *tide* came", "Sentence 4.2 says the *water* came") });
  await page.goto(readURL(c), { waitUntil: "load" });
  await page.getByRole("button", { name: /^Suggestions/ }).click();
  const card = page.locator("#rv-sugg .rv-card");
  await kit.waitFor(async () => (await card.count()) === 1, "the card");
  ok((await card.innerText()).includes("changed since this was suggested"), "stale says so");
  await card.getByRole("button", { name: "Accept", exact: true }).click();
  await page.waitForSelector("#rv-sugg .rv-card .field-error");
  ok((await card.innerText()).includes("Reject it"), "the refusal says what to do: " + await card.innerText());
  ok(!(await docText(c)).includes("the sea came"), "nothing was applied");
  await card.getByRole("button", { name: "Reject", exact: true }).click();
  await kit.waitFor(async () => (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/suggestions?document_id=${c}&status=open`)).json.total === 0, "rejected");
});

await kit.test("390 px: the margin is a sheet, comment from a selection, no sideways scroll", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 800 }, hasTouch: true, isMobile: true }, "reader@dev.scriobh");
  const page = ph.page; kit.main = page;
  await page.goto(readURL(chapters[5]), { waitUntil: "load" });
  await page.waitForSelector("#rv-margin", { state: "attached" });
  const sheet = () => page.locator("#rv-margin").evaluate((e) => ({ vis: getComputedStyle(e).visibility, top: Math.round(e.getBoundingClientRect().top), h: window.innerHeight }));
  eq((await sheet()).vis, "hidden", "the sheet starts closed");
  ok(!(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)), "no sideways scroll");
  ok(await page.getByRole("button", { name: /^Comments/ }).first().isVisible(), "the Comments button is in reach");
  // one tap opens the sheet and the next closes it (the first tap must not be wasted on a state that says it is already open)
  await page.tap("#rv-toggle");
  eq((await sheet()).vis, "visible", "the Comments button opens the sheet");
  eq(await page.locator("#rv-toggle").getAttribute("aria-pressed"), "true", "and says so");
  eq(await page.locator("#rv-toggle").getAttribute("aria-expanded"), null, "one state, not two");
  const tb = await page.locator("#rv-toggle").boundingBox();
  await page.touchscreen.tap(tb.x + tb.width / 2, tb.y + tb.height / 2); // the page behind an open sheet is inert: a tap on it is a tap outside, and closes it
  await kit.waitFor(async () => (await sheet()).vis === "hidden", "the next tap closes it (after its short slide)");
  ok(await select(page, "Sentence 6.2 says the tide came"), "selected");
  await page.waitForSelector(".rv-pop-c", { state: "visible" });
  const pop = await page.locator(".rv-pop").boundingBox();
  ok(pop.y > 400 && pop.height >= 40, "the button sits at the bottom, large enough to tap: " + JSON.stringify(pop));
  await page.tap(".rv-pop-c");
  await page.waitForSelector("#rv-new");
  eq((await sheet()).vis, "visible", "the sheet opened for the composer");
  const box = await page.locator("#rv-new").boundingBox();
  ok(box.width > 250, "the box is wide enough: " + box.width);
  await page.fill("#rv-new", "Phone comment.");
  await page.tap(".rv-form button[type=submit]");
  await kit.waitFor(async () => (await page.locator(".rv-card").count()) >= 1, "the card");
  if (shots) await page.screenshot({ path: `${shots}/review-sheet-390.png` });
  ok(!(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)), "still no sideways scroll");
  await page.keyboard.press("Escape");
  await ph.ctx.close();
});

await kit.test("a reader (no review role) sees the book and none of the comments; the review page is refused", async () => {
  const members = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/members`)).json.items;
  const rory = members.find((m) => m.email === "reader@dev.scriobh");
  const setRole = (role) => api(owner.ctx, "PATCH", `/api/v1/projects/${pid}/members`, { user_id: rory.user_id, role });
  eq((await setRole("reader")).status, 200, "role change");
  const page = reviewer.page; kit.main = page;
  await page.goto(readURL(chapters[0]), { waitUntil: "load" });
  eq(await page.locator("#rv-margin").count(), 0, "no margin");
  eq(await page.locator("#rv-toggle").count(), 0, "no comments button");
  const api403 = await api(reviewer.ctx, "GET", `/api/v1/projects/${pid}/comments`);
  eq(api403.status, 403, "the comments op refuses a reader");
  kit.allowed.push(/403/);
  const r = await page.goto(`${kit.base()}/p/${slug}/review`, { waitUntil: "load" });
  eq(r.status(), 403, "the review page says no to a reader");
  await setRole("reviewer");
});

await kit.test("the writing view's Comments tab lists the chapter's threads; resolve and reply there; the text is untouched", async () => {
  const c = chapters[6], page = owner.page; kit.main = page;
  for (const k of [3, 7]) {
    const r = await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: c, quote: `Sentence 7.${k} says the tide came`, plain: true, body: `Panel comment ${k}.` });
    ok(r.status === 201, "comment " + r.status);
  }
  const before = await docText(c);
  await page.goto(`${kit.base()}/p/${slug}/d/${c}`, { waitUntil: "load" });
  await page.waitForSelector("#itab-comments", { state: "attached" });
  await page.evaluate(() => document.querySelector("#insp-open").click());
  await page.click("#itab-comments");
  await kit.waitFor(async () => (await page.locator("#ipanel-comments .rv-card").count()) === 2, "two cards in the panel");
  ok((await page.locator("#ipanel-comments").innerText()).includes("can be marked in the text"), "the panel says where comments are drawn");
  const href = await page.locator("#ipanel-comments .rv-actions a").first().getAttribute("href");
  ok(href.startsWith(`/p/${slug}/read/${c}#t=`), "a link to the reading view: " + href);
  await page.locator("#ipanel-comments .rv-card").first().getByRole("button", { name: "Resolve" }).click();
  await kit.waitFor(async () => (await page.locator("#ipanel-comments .rv-card").count()) === 1, "resolved ones leave the open list");
  await page.locator("#ipanel-comments").getByRole("button", { name: "Resolved", exact: true }).click();
  ok((await page.locator("#ipanel-comments .rv-card").allInnerTexts()).some((t) => t.includes("Panel comment")), "and are under Resolved, with the three the writer resolved earlier");
  eq(await docText(c), before, "the chapter text is untouched");
});

await kit.test("the contents show open counts, and Next unresolved (button and N) goes across chapters", async () => {
  const page = owner.page; kit.main = page;
  for (const [c, k] of [[0, 10], [7, 10]]) {
    const r = await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: chapters[c], quote: `Sentence ${c + 1}.${k} says the tide came`, plain: true, body: `Open thread in chapter ${c + 1}.` });
    ok(r.status === 201, "comment " + r.status);
  }
  await page.goto(readURL(chapters[0]), { waitUntil: "load" });
  const open = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=1`)).json.total;
  await kit.waitFor(async () => (await page.locator("#rv-count").innerText()) === String(open), "the total on the button (" + open + ")");
  await page.getByRole("button", { name: /^Contents/ }).click();
  const badge = page.locator(`#read-toc a[href$="/${chapters[7]}"] .rv-n`);
  await kit.waitFor(async () => (await badge.count()) === 1 && (await badge.innerText()) === "1 open", "the chapter's count in the contents");
  await page.keyboard.press("Escape");
  const onMark = () => page.evaluate(() => (document.activeElement.tagName === "MARK" ? document.activeElement.dataset.t : ""));
  const cardText = (id) => page.locator("#rv-t-" + id).innerText();
  await page.getByRole("button", { name: "Next unresolved" }).click();
  await kit.waitFor(async () => (await onMark()) !== "", "the first open thread's words are in focus");
  ok((await cardText(await onMark())).includes("chapter 1"), "it is the one in this chapter");
  await page.keyboard.press("Escape");
  await page.locator("#reader").focus();
  await page.keyboard.press("n");
  // the queue's next thread is in chapter 6 (an earlier step left one open there): N goes to its page, and the words are in focus
  await page.waitForURL(new RegExp(`/read/${chapters[5]}#t=`), { timeout: 10000 });
  await kit.waitFor(async () => (await onMark()) !== "", "the thread in the other chapter is shown");
  ok((await cardText(await onMark())).includes("Phone comment"), "it is the other chapter's");
  await page.keyboard.press("Escape");
  await page.locator("#reader").focus();
  await page.keyboard.press("n");
  await page.waitForURL(new RegExp(`/read/${chapters[6]}#t=`), { timeout: 10000 });
});

await kit.test("scroll continuously: a chapter that arrives is marked, comments and suggestions", async () => {
  const page = owner.page; kit.main = page;
  const c = chapters[4];
  const r1 = await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: c, quote: "Sentence 5.14 says the tide came", plain: true, body: "Late chapter comment." });
  ok(r1.status === 201, "comment " + r1.status);
  const d = (await api(owner.ctx, "GET", `/api/v1/docs/${c}`)).json.item;
  const r2 = await api(reviewer.ctx, "POST", `/api/v1/projects/${pid}/suggestions`, { document_id: c, base_revision: d.revision, plain: true, changes: [{ quote: "Sentence 5.11 says the tide came", text: "Sentence 5.11 says the sea came" }] });
  ok(r2.status === 201, "suggestion " + r2.status);
  await page.goto(readURL(chapters[3]), { waitUntil: "load" });
  await page.getByRole("button", { name: "Scroll continuously" }).click();
  const sec = page.locator(`section.chapter[data-doc="${c}"]`);
  await kit.waitFor(async () => { await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight)); return (await sec.count()) === 1; }, "the next chapter is appended");
  await kit.waitFor(async () => (await sec.locator("mark.rv-mark").count()) >= 1, "its comment is marked");
  await kit.waitFor(async () => (await sec.locator("del.rv-del").count()) >= 1, "its suggestion is drawn");
});

// ---- the fix round: confirmations, drafts, the sheet, focus, keys ---------------------------------------------------
await kit.test("a bulk confirmation cannot be got through by a held Enter or a double tap, and Escape leaves it", async () => {
  const page = owner.page; kit.main = page;
  for (const k of [1, 2]) await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: chapters[0], quote: `Sentence 1.${12 + k} says the tide came`, plain: true, body: `Bulk ${k}.` });
  const open = async () => (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/comments?status=open&limit=1`)).json.total;
  const before = await open();
  ok(before >= 2, "comments to resolve: " + before);
  await page.goto(`${kit.base()}/p/${slug}/review`, { waitUntil: "load" });
  await page.getByRole("button", { name: /^Resolve all/ }).focus();
  await page.keyboard.press("Enter");
  await page.waitForSelector("#rq-plan");
  eq(await page.evaluate(() => document.activeElement.textContent.trim()), "Cancel", "the plan opens with focus on Cancel");
  ok(await page.locator("[data-hold]").isDisabled(), "the confirming button is off at first");
  // two Enters, 60 ms apart, as a held key or a double tap would send them
  await page.keyboard.press("Enter"); await kit.sleep(60); await page.keyboard.press("Enter");
  await kit.sleep(900);
  eq(await open(), before, "nothing was resolved");
  await page.getByRole("button", { name: /^Resolve all/ }).click();
  await page.waitForSelector("#rq-plan");
  await page.keyboard.press("Escape");
  await page.waitForURL(new RegExp(`/p/${slug}/review$`));
  eq(await page.locator("#rq-plan").count(), 0, "Escape leaves the plan");
  eq(await open(), before, "and nothing was resolved");
});

await kit.test("an unsent reply is kept by thread through a live update, a redraw and a change of filter", async () => {
  const page = editor.page; kit.main = page;
  const mk = async (n) => (await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: chapters[8], quote: `Sentence 9.${n} says the tide came`, plain: true, body: `Draft thread ${n}.` })).json.item.thread_id;
  const t1 = await mk(2), t2 = await mk(5);
  await page.goto(readURL(chapters[8]), { waitUntil: "load" });
  await page.waitForSelector(`#rv-t-${t1}`);
  await page.locator(`#rv-t-${t1}`).getByRole("button", { name: "Reply" }).click();
  await page.fill(`#rv-r-${t1}`, "half a reply, still being thought");
  // focus leaves the box (a click on the page), then another person replies to the other thread: the margin is drawn again
  await page.locator("#read-stream h2").first().click();
  await api(owner.ctx, "POST", `/api/v1/projects/${pid}/comments`, { parent_id: t2, body: "A reply from the writer." });
  await kit.waitFor(async () => (await page.locator(`#rv-t-${t2} .rv-replies li`).count()) === 1, "the margin shows the other person's reply");
  eq(await page.locator(`#rv-r-${t1}`).inputValue(), "half a reply, still being thought", "the unsent reply is intact after the live update");
  // typing while a live update arrives: nothing is redrawn under the caret
  await page.locator(`#rv-r-${t1}`).click(); await page.keyboard.press("End"); await page.keyboard.type(" and more");
  await api(owner.ctx, "POST", `/api/v1/projects/${pid}/comments`, { parent_id: t2, body: "A second reply from the writer." });
  await kit.sleep(1500);
  eq(await page.evaluate(() => document.activeElement.id), `rv-r-${t1}`, "the caret is still in the box");
  ok((await page.locator(`#rv-r-${t1}`).inputValue()).endsWith("and more"), "and what was typed is there");
  // a filter and back
  await page.getByRole("button", { name: "Resolved", exact: true }).click();
  await page.getByRole("button", { name: "Open", exact: true }).click();
  eq(await page.locator(`#rv-r-${t1}`).inputValue(), "half a reply, still being thought and more", "the reply survives a change of filter");
  await page.keyboard.press("Escape");
});

await kit.test("deleting a comment asks in the card, not in a browser dialog, and a double Enter does not delete it", async () => {
  const page = editor.page; kit.main = page;
  let dialogs = 0; const count = () => { dialogs++; }; page.on("dialog", count);
  const t = (await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: chapters[8], quote: "Sentence 9.8 says the tide came", plain: true, body: "To be deleted." })).json.item.thread_id;
  await page.goto(readURL(chapters[8]), { waitUntil: "load" });
  await page.waitForSelector(`#rv-t-${t}`);
  const card = page.locator(`#rv-t-${t}`), del = card.locator(".rv-actions").getByRole("button", { name: "Delete", exact: true });
  await del.click();
  await page.waitForSelector(`#rv-t-${t} .arm`);
  eq(await page.evaluate(() => document.activeElement.textContent), "Cancel", "focus is on Cancel");
  ok(await card.locator(".arm-ok").isDisabled(), "the confirming button waits");
  await page.keyboard.press("Escape");
  eq(await card.locator(".arm").count(), 0, "Escape puts the question away");
  eq(await page.evaluate(() => document.activeElement.textContent), "Delete", "focus returns to Delete");
  await del.click();
  await page.keyboard.press("Enter"); await kit.sleep(60); await page.keyboard.press("Enter");
  await kit.sleep(700);
  eq((await api(editor.ctx, "GET", `/api/v1/projects/${pid}/comments/${t}`)).status, 200, "the comment is still there");
  await del.click();
  await page.waitForSelector(`#rv-t-${t} .arm-ok:not([disabled])`);
  await card.locator(".arm-ok").click();
  await kit.waitFor(async () => (await page.locator(`#rv-t-${t}`).count()) === 0, "deleted once confirmed");
  eq(dialogs, 0, "no native dialog was used");
  ok(await page.evaluate(() => document.activeElement.tagName !== "BODY"), "focus was not dropped");
  page.off("dialog", count);
});

await kit.test("the margin keys: N from inside the margin, Escape back to the words, the card named by its author", async () => {
  const page = editor.page; kit.main = page;
  await page.goto(readURL(chapters[8]), { waitUntil: "load" });
  await page.waitForSelector(".rv-card");
  const first = page.locator(".rv-card").first();
  const labelled = await first.getAttribute("aria-labelledby");
  ok(labelled && (await page.locator("#" + labelled).innerText()).includes("Ada"), "the card is named by its author and time");
  eq(await first.getAttribute("aria-label"), null, "and its own words are not replaced by a label");
  await page.keyboard.press("m");
  await kit.waitFor(async () => (await page.evaluate(() => (document.activeElement.closest(".rv-card") || {}).id || "")).startsWith("rv-t-"), "focus in the list");
  await page.keyboard.press("n");
  await kit.waitFor(async () => (await page.evaluate(() => document.activeElement.tagName)) === "MARK", "N in the margin goes to the next thread's words");
  eq(await page.locator("mark.rv-mark").first().getAttribute("tabindex"), "-1", "marks can be focused by script and are not in the tab order");
  ok(!!(await page.locator("mark.rv-mark").first().getAttribute("aria-details")), "and point at their card");
  await page.keyboard.press("m");
  await kit.waitFor(async () => (await page.evaluate(() => (document.activeElement.closest(".rv-card") || {}).id || "")).startsWith("rv-t-"), "back in the list");
  await page.keyboard.press("Escape");
  eq(await page.evaluate(() => document.activeElement.tagName), "MARK", "Escape returns focus to the words");
});

await kit.test("a comment in Arabic keeps its order in the margin; times read the same on the review page and the margin; lists have markers", async () => {
  const page = editor.page; kit.main = page;
  const ar = await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: chapters[9], quote: "Sentence 10.2 says the tide came", plain: true, body: "هذا تعليق بالعربية with Latin words" });
  ok(ar.status === 201, "arabic comment " + ar.status);
  await page.goto(readURL(chapters[9]), { waitUntil: "load" });
  await page.waitForSelector(".rv-card");
  const bidi = await page.locator(".rv-body").last().evaluate((e) => getComputedStyle(e).unicodeBidi);
  eq(bidi, "plaintext", "the comment sets its own direction");
  const stamp = await page.locator(`#rv-t-${ar.json.item.thread_id} time`).first().innerText();
  const full = await page.locator(`#rv-t-${ar.json.item.thread_id} time`).first().getAttribute("title");
  ok(full && full.length > stamp.length, "the full time is in the title: " + full);
  await owner.page.goto(`${kit.base()}/p/${slug}/review?doc=${chapters[9]}`, { waitUntil: "load" });
  await kit.waitFor(async () => (await owner.page.locator(`#t-${ar.json.item.thread_id} time`).innerText()) === stamp, "the review page shows the same time in the same form (" + stamp + ")");
  // a list in the chapter has markers in the reading view and the writing view
  const L = (await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: "A list", kind: "chapter", content: "Intro.\n\n- first item\n- second item\n\n1. one\n2. two\n" })).json.item.id;
  await owner.page.goto(readURL(L), { waitUntil: "load" });
  const kinds = async (pg, sel) => pg.evaluate((sel) => [...document.querySelectorAll(sel)].map((e) => getComputedStyle(e).listStyleType + "|" + parseFloat(getComputedStyle(e).paddingInlineStart)), sel);
  const r = await kinds(owner.page, ".r-prose ul, .r-prose ol");
  ok(r[0].startsWith("disc|") && r[1].startsWith("decimal|") && parseFloat(r[0].split("|")[1]) > 10, "markers in the reading view: " + r);
  await owner.page.goto(`${kit.base()}/p/${slug}/d/${L}`, { waitUntil: "load" });
  await owner.page.waitForSelector(".scriobh-prose ul");
  const w = await kinds(owner.page, ".scriobh-prose ul, .scriobh-prose ol");
  ok(w[0].startsWith("disc|") && w[1].startsWith("decimal|"), "markers in the writing view: " + w);
});

await kit.test("the review page lists suggestions as the book runs, not newest first", async () => {
  const page = owner.page; kit.main = page;
  for (const [c, n] of [[10, 4], [2, 4], [6, 4]]) {
    const d = (await api(owner.ctx, "GET", `/api/v1/docs/${chapters[c - 1]}`)).json.item;
    const r = await api(reviewer.ctx, "POST", `/api/v1/projects/${pid}/suggestions`, { document_id: chapters[c - 1], base_revision: d.revision, plain: true, changes: [{ quote: `Sentence ${c}.${n} says`, text: `Sentence ${c}.${n} said` }] });
    ok(r.status === 201, `suggestion ${c}: ${r.status} ${JSON.stringify(r.json)}`);
  }
  await page.goto(`${kit.base()}/p/${slug}/review`, { waitUntil: "load" });
  const heads = (await page.locator("#rq-sug .rq-group h3").allInnerTexts()).map((t) => parseInt(/Chapter (\d+)/.exec(t)[1], 10));
  ok(heads.length >= 3 && heads.every((n, i) => i === 0 || heads[i - 1] < n), "chapters in book order: " + heads);
});

await kit.test("the keyboard-selection popover is beside the words; the mention picker is under the box, not over it", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(readURL(chapters[3]), { waitUntil: "load" });
  await page.waitForSelector("#rv-margin");
  await page.locator("#reader").focus();
  await page.keyboard.press("]");
  for (let i = 0; i < 3; i++) await page.keyboard.press("Shift+ArrowRight");
  await page.waitForSelector(".rv-pop", { state: "visible" });
  const sel = await page.evaluate(() => { const r = getSelection().getRangeAt(0).getBoundingClientRect(); return { top: r.top, bottom: r.bottom }; });
  const pop = await page.locator(".rv-pop .btn").first().boundingBox();
  ok(Math.abs(pop.y - sel.bottom) < 60 || Math.abs(pop.y + pop.height - sel.top) < 60, `the button is at the words: selection ${JSON.stringify(sel)}, button ${JSON.stringify(pop)}`);
  await page.keyboard.press("c");
  await page.waitForSelector("#rv-new");
  await page.keyboard.type("Hello @Ad");
  await page.waitForSelector(".rv-pick li");
  const ta = await page.locator("#rv-new").boundingBox(), pick = await page.locator(".rv-pick").boundingBox();
  ok(pick.y >= ta.y + ta.height - 2, `the picker is below the box: box ${JSON.stringify(ta)}, picker ${JSON.stringify(pick)}`);
  await page.keyboard.press("Escape"); await page.keyboard.press("Escape");
});

await kit.test("390 px: the sheet is a dialog with a Close button, Escape, a tap outside, a trap and a way back; the mark is never under it", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 800 }, hasTouch: true, isMobile: true }, "admin@dev.scriobh");
  const page = ph.page; kit.main = page;
  const t = (await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: chapters[10], quote: "Sentence 11.16 says the tide came", plain: true, body: "A comment far down the page." })).json.item.thread_id;
  await page.goto(readURL(chapters[10]), { waitUntil: "load" });
  await page.waitForSelector(`mark[data-t="${t}"]`);
  const root = page.locator("#rv-margin");
  await page.tap("#rv-toggle");
  await kit.waitFor(async () => (await root.getAttribute("role")) === "dialog", "the open sheet is a dialog");
  eq(await root.getAttribute("aria-modal"), "true", "a modal one");
  ok(await page.locator("#rv-close").isVisible(), "with a Close button");
  eq(await page.evaluate(() => document.querySelector("#read-stream").inert), true, "the page behind it is inert");
  for (let i = 0; i < 14; i++) { await page.keyboard.press("Tab"); ok(await page.evaluate(() => !!document.activeElement.closest("#rv-margin")), "Tab stays in the sheet (press " + (i + 1) + ")"); }
  for (let i = 0; i < 14; i++) { await page.keyboard.press("Shift+Tab"); ok(await page.evaluate(() => !!document.activeElement.closest("#rv-margin")), "Shift+Tab stays in the sheet"); }
  await page.keyboard.press("Escape");
  await kit.waitFor(async () => (await root.getAttribute("role")) === null, "Escape closes it");
  eq(await page.evaluate(() => document.activeElement.id), "rv-toggle", "focus returns to the button that opened it");
  await page.tap("#rv-toggle");
  await page.tap("#rv-close");
  await kit.waitFor(async () => (await root.getAttribute("role")) === null, "Close closes it");
  await page.tap("#rv-toggle");
  await page.touchscreen.tap(195, 60);
  await kit.waitFor(async () => (await root.getAttribute("role")) === null, "a tap outside closes it");
  // the words a card is about are in the free part of the window
  await page.locator(`mark[data-t="${t}"]`).first().evaluate((e) => e.scrollIntoView({ block: "end" }));
  await page.tap(`mark[data-t="${t}"]`);
  await kit.waitFor(async () => (await root.getAttribute("role")) === "dialog", "tapping words opens the sheet at their card");
  await kit.sleep(500);
  const geo = await page.evaluate((t) => ({ mark: document.querySelector(`mark[data-t="${t}"]`).getBoundingClientRect().bottom, sheet: document.querySelector("#rv-margin").getBoundingClientRect().top }), t);
  ok(geo.mark <= geo.sheet + 1, `the words are above the sheet: ${JSON.stringify(geo)}`);
  ok(await page.locator(".rv-pop").isHidden(), "the Comment button does not sit over the sheet");
  await page.tap("#rv-close");
  // the bar with the Comments button stays in reach down the page
  await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
  const bar = await page.locator("#rv-toggle").boundingBox();
  ok(bar && bar.y >= 0 && bar.y < 200, "the Comments button is still in reach after scrolling: " + JSON.stringify(bar));
  // a window zoomed to 200%: the sheet is the whole window
  await page.setViewportSize({ width: 195, height: 400 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.tap("#rv-toggle");
  await kit.sleep(500);
  const z = await page.evaluate(() => { const r = document.querySelector("#rv-margin").getBoundingClientRect(); return { top: r.top, h: r.height, vh: innerHeight }; });
  ok(z.top <= 1 && z.h >= z.vh - 2, "the sheet is full-screen when the window is small: " + JSON.stringify(z));
  ok(await page.locator("#rv-close").isVisible(), "with its way back");
  await ph.ctx.close();
});

await kit.test("withdrawing a suggestion asks first, and focus lands somewhere sensible after each decision", async () => {
  const page = reviewer.page; kit.main = page;
  const c = chapters[1], d = (await api(owner.ctx, "GET", `/api/v1/docs/${c}`)).json.item;
  const mk = async (q, t) => (await api(reviewer.ctx, "POST", `/api/v1/projects/${pid}/suggestions`, { document_id: c, base_revision: d.revision, plain: true, changes: [{ quote: q, text: t }] })).json.item.id;
  const a = await mk("Sentence 2.7 says", "Sentence 2.7 insists");
  await page.goto(readURL(c), { waitUntil: "load" });
  await page.getByRole("button", { name: /^Suggestions/ }).click();
  const card = page.locator(`#rv-s-${a}`);
  await card.waitFor();
  await card.getByRole("button", { name: "Withdraw", exact: true }).click();
  await page.waitForSelector(`#rv-s-${a} .arm`);
  eq(await page.evaluate(() => document.activeElement.textContent), "Cancel", "the question opens on Cancel");
  await page.keyboard.press("Escape");
  eq(await card.locator(".arm").count(), 0, "Escape puts it away");
  eq(await page.evaluate(() => document.activeElement.textContent), "Withdraw", "focus is back on Withdraw");
  await card.getByRole("button", { name: "Withdraw", exact: true }).click();
  await page.waitForSelector(`#rv-s-${a} .arm-ok:not([disabled])`);
  await card.locator(".arm-ok").click();
  await kit.waitFor(async () => (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/suggestions/${a}`)).json.item.status === "withdrawn", "withdrawn once confirmed");
  await kit.waitFor(async () => (await page.evaluate(() => document.activeElement && document.activeElement.tagName)) !== "BODY", "focus is not dropped after Withdraw");
  // sending one: focus goes to the new card
  await page.getByRole("button", { name: "Suggesting", exact: true }).click();
  await page.locator("#reader").focus();
  ok(await select(page, "Sentence 2.9 says the tide came"), "selected");
  await page.keyboard.press("s");
  await page.waitForSelector("#rv-sw");
  await page.fill("#rv-st", "Sentence 2.9 says the sea came");
  await page.keyboard.press("ControlOrMeta+Enter");
  await kit.waitFor(async () => (await page.evaluate(() => (document.activeElement.closest(".rv-card") || {}).id || "")).startsWith("rv-s-"), "focus is on the card of the suggestion just sent");
});

await kit.test("screens: the margin and the writer's review page, light and dark", async () => {
  if (!shots) return;
  const page = editor.page; kit.main = page;
  await api(editor.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: chapters[0], quote: "Sentence 1.2 says the tide came", before: "", after: "", plain: true, body: "This sentence says too much about the tide. Cut it back to what Mara can see." });
  await page.goto(readURL(chapters[0]), { waitUntil: "load" });
  await page.waitForSelector(".rv-card");
  await page.screenshot({ path: `${shots}/review-read-1280.png` });
  await page.emulateMedia({ colorScheme: "dark" });
  await page.screenshot({ path: `${shots}/review-read-1280-dark.png` });
  await page.emulateMedia({ colorScheme: "light" });
  await owner.page.goto(`${kit.base()}/p/${slug}/review`, { waitUntil: "load" });
  await owner.page.screenshot({ path: `${shots}/review-queue-1280.png` });
  kit.main = null;
});

await kit.finish("e2e-review");
