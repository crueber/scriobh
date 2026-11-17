#!/usr/bin/env node
// Interface language and text direction in Chromium: the pseudo-locale shows every string of the new screens bracketed
// (so a string that is not in the catalogue, or that a script built itself, shows at once), the right-to-left pseudo-locale
// mirrors the page without a sideways scroll at 390 px, and an Arabic book is read right to left in the reading view and the
// writing view with a Latin quotation inside it set by its own direction.
//
//   node scripts/e2e-i18n.mjs
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-i18n", { env: { SCRIOBH_SHARE_RATE_LIMIT: "0", SCRIOBH_SHARE_LINK_RATE: "0" } });
const { api } = kit;
const owner = await kit.signedIn({}, "writer@dev.scriobh");
const made = await api(owner.ctx, "POST", "/api/v1/projects", { title: "Arabic Book", kind: "novel", language: "ar" });
const P = made.json.item, pid = P.id, slug = P.slug;
const first = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
const ar = "وصلت مارا إلى الميناء في الليل.\n\nHello world, the second paragraph is in English.\n\nשלום עולם וברכה.\n";
const g = (await api(owner.ctx, "GET", `/api/v1/docs/${first}`)).json.item;
await api(owner.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, title: "الطريق", content: ar });
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only")+1] : "";
const setLang = (l) => api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: l } });

await kit.test("the pseudo-locale brackets every string of the review, share and people screens and the strings a script builds", async () => {
  const page = owner.page; kit.main = page;
  eq((await setLang("xx")).status, 200, "setting the language");
  // the People page is an older screen: only its new part (what each role can do) is in the catalogue so far
  await page.goto(`${kit.base()}/p/${slug}/people`, { waitUntil: "load" });
  ok((await page.locator("#roles dt").allInnerTexts()).every((t) => t.startsWith("\u27e6")) && (await page.locator("#roles dd").allInnerTexts()).every((t) => t.startsWith("\u27e6")), "the role descriptions are in the language");
  for (const path of [`/p/${slug}/review`, `/p/${slug}/share`]) {
    await page.goto(kit.base() + path, { waitUntil: "load" });
    eq(await page.evaluate(() => document.documentElement.lang), "xx", path + " lang");
    const h1 = await page.locator("h1").first().innerText();
    ok(h1.startsWith("⟦"), path + " heading is bracketed: " + h1);
    const unbracketed = await page.evaluate(() => [...document.querySelectorAll("main h2, main h3, main label:not(.shl-docs *), main legend, main button")].map((e) => e.innerText.trim()).filter((t) => t && !t.startsWith("⟦") && !/^[0-9]/.test(t)).slice(0, 5));
    ok(unbracketed.length === 0, `${path} has strings that did not come from the catalogue: ${unbracketed.join(" | ")}`);
  }
  // the reading view: the button, the margin and the strings its script builds are in the language
  await page.goto(`${kit.base()}/p/${slug}/read/${first}`, { waitUntil: "load" });
  await page.waitForSelector(".rv-card, #rv-empty:not([hidden])");
  const built = await page.evaluate(() => [...document.querySelectorAll("#rv-margin button, #rv-margin h2, #rv-empty")].map((e) => e.innerText.trim()).filter((t) => t));
  ok(built.length >= 5 && built.every((t) => t.startsWith("⟦") || /^[0-9]+$/.test(t)), "the margin's own strings: " + built.join(" | "));
  await setLang("en");
});

await kit.test("the right-to-left pseudo-locale mirrors the page and nothing scrolls sideways at 390 px", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 800 } }, "writer@dev.scriobh");
  const page = ph.page; kit.main = page;
  eq((await api(ph.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } })).status, 200, "language");
  for (const path of [`/p/${slug}/review`, `/p/${slug}/share`, `/p/${slug}/read/${first}`, `/settings`]) {
    await page.goto(kit.base() + path, { waitUntil: "load" });
    eq(await page.evaluate(() => document.documentElement.dir), "rtl", path + " dir");
    eq(await page.evaluate(() => getComputedStyle(document.body).direction), "rtl", path + " computed direction");
    ok(!(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)), path + " scrolls sideways");
  }
  // the margin's cards put their accent on the start edge, which is the right in a mirrored page
  await page.goto(`${kit.base()}/p/${slug}/share`, { waitUntil: "load" });
  await api(ph.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "en" } });
  await ph.ctx.close();
});

await kit.test("an Arabic book is read right to left, paragraph by paragraph, in the reading and writing views", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${kit.base()}/p/${slug}/read/${first}`, { waitUntil: "load" });
  const probe = await page.evaluate(() => {
    const art = document.querySelector("#read-stream"), ps = [...document.querySelectorAll(".r-prose p")];
    return { dir: getComputedStyle(art).direction, lang: art.lang, para: ps.map((p) => ({ d: getComputedStyle(p).direction, bidi: getComputedStyle(p).unicodeBidi, align: getComputedStyle(p).textAlign })) };
  });
  eq(probe.lang, "ar", "lang");
  eq(probe.dir, "rtl", "the page is right to left");
  ok(probe.para.every((p) => p.bidi === "plaintext"), "each paragraph chooses its own direction: " + JSON.stringify(probe.para));
  const x = await page.evaluate(() => [...document.querySelectorAll(".r-prose p")].map((p) => Math.round(p.firstChild.parentElement.getBoundingClientRect().right)));
  ok(x.length === 3, "three paragraphs");
  // the writing view: the page and the editor take the direction too
  await page.goto(`${kit.base()}/p/${slug}/d/${first}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose");
  const w = await page.evaluate(() => ({ paper: getComputedStyle(document.querySelector("#paper")).direction, text: document.querySelector(".scriobh-prose").innerText }));
  eq(w.paper, "rtl", "the writing page is right to left");
  ok(w.text.includes("وصلت") && w.text.includes("שלום"), "the text is intact in the editor");
  // typing Arabic into the editor and saving round-trips it
  await page.locator(".scriobh-prose").click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type(" مرحبا");
  await kit.waitFor(async () => (await api(owner.ctx, "GET", `/api/v1/docs/${first}`)).json.item.content.includes("مرحبا"), "the typed Arabic was saved", 15000);
});

// Typing must not move anything: in an empty new paragraph the first keystroke, in either script, in a book of either
// direction, leaves the paragraph where it was (no reflow) and the caret beside the character just typed.
await kit.test("the first keystroke in an empty paragraph moves nothing, in an Arabic book and an English one", async () => {
  const page = owner.page; kit.main = page;
  const en = (await api(owner.ctx, "POST", "/api/v1/projects", { title: "English Book", kind: "novel", language: "en" })).json.item;
  const enFirst = (await api(owner.ctx, "GET", `/api/v1/projects/${en.id}/docs`)).json.order[0];
  const probe = () => page.evaluate(() => {
    const sel = getSelection(), pm = document.querySelector(".scriobh-prose"), p = sel.anchorNode && (sel.anchorNode.nodeType === 1 ? sel.anchorNode : sel.anchorNode.parentElement).closest("p");
    const r = p.getBoundingClientRect(), range = sel.getRangeAt(0).cloneRange();
    const rects = range.getClientRects(), c = rects.length ? rects[0] : p.getBoundingClientRect();
    return { top: Math.round(r.top), height: Math.round(r.height), left: Math.round(r.left), right: Math.round(r.right), dir: getComputedStyle(p).direction, caretX: Math.round(c.left), n: pm.querySelectorAll("p").length };
  });
  const FIRST = first;
  for (const [label, slugX, doc, typed, dirWanted] of [["Arabic book, Arabic letter", slug, FIRST, "م", "rtl"], ["Arabic book, Latin letter", slug, FIRST, "a", "ltr"], ["English book, Latin letter", en.slug, enFirst, "a", "ltr"], ["English book, Arabic letter", en.slug, enFirst, "م", "rtl"]]) {
    await page.goto(`${kit.base()}/p/${slugX}/d/${doc}`, { waitUntil: "load" });
    await page.waitForSelector(".scriobh-prose");
    await page.locator(".scriobh-prose").click();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.press("Enter");
    const before = await probe();
    await page.keyboard.type(typed);
    const after = await probe();
    if (process.env.E2E_DEBUG) console.log(label, JSON.stringify(before), JSON.stringify(after));
    ok(before.n === after.n, label + ": paragraphs changed");
    ok(before.top === after.top && Math.abs(before.height - after.height) <= 1, `${label}: the paragraph moved: ${JSON.stringify(before)} -> ${JSON.stringify(after)}`);
    // a left-to-right book keeps its direction whatever script is typed: the line does not turn round under the caret
    if (slugX === en.slug) { eq(after.dir, "ltr", label + ": the paragraph turned round"); ok(after.caretX < after.left + 80, label + ": the text left its edge: " + after.caretX); }
    // the caret sits at the edge of the typed character, on the side the text runs from, and a second key goes beyond it
    await page.keyboard.type(typed);
    const next = await probe();
    ok(slugX === en.slug ? next.caretX >= after.caretX : dirWanted === "rtl" ? next.caretX < after.caretX : next.caretX > after.caretX, `${label}: the caret did not follow the text: ${after.caretX} -> ${next.caretX}`);
  }
});

await kit.test("people, the reading toolbar and the margin of an Arabic comment: the pseudo-locale covers them, the right-to-left header is spaced, the quote is whole", async () => {
  const page = owner.page; kit.main = page;
  const c = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/comments`, { document_id: first, quote: "الميناء", plain: true, body: "هذا تعليق with Latin words" });
  ok(c.status === 201, "comment " + c.status);
  eq((await setLang("xx")).status, 200, "language");
  const bracketed = async (sel, what) => {
    const t = (await page.locator(sel).allInnerTexts()).map((x) => x.trim()).filter((x) => x);
    ok(t.length > 0 && t.every((x) => x.startsWith("⟦") || /^[0-9]+$/.test(x)), `${what} are in the language: ${t.join(" | ")}`);
  };
  await page.goto(`${kit.base()}/p/${slug}/people`, { waitUntil: "load" });
  await bracketed("main h1, main h2, main label, main legend, main button", "the People page's headings, labels and buttons");
  await page.goto(`${kit.base()}/p/${slug}/read/${first}`, { waitUntil: "load" });
  await bracketed(".read-bar .btn span:not(.rv-count), .read-pos, #read-mode", "the reading toolbar");
  await page.waitForSelector(".rv-card");
  // the quote in the margin is the whole word, not cut in the middle of a letter
  ok((await page.locator(".rv-card .rv-quote").first().innerText()).includes("الميناء"), "the quote is whole: " + await page.locator(".rv-card .rv-quote").first().innerText());
  // the time in the margin is a time, in the form the browser knows, whatever the interface's test language
  const when = await page.locator(".rv-card time").first().innerText();
  ok(!/^\d{4}-\d\d-\d\dT/.test(when), "a readable time: " + when);
  eq((await setLang("xx-rtl")).status, 200, "language");
  await page.goto(`${kit.base()}/p/${slug}/read/${first}`, { waitUntil: "load" });
  await page.waitForSelector(".rv-card");
  const gap = await page.evaluate(() => { const w = document.querySelector(".wordmark").getBoundingClientRect(), c = document.querySelector(".crumb").getBoundingClientRect(); return w.left - c.right; });
  ok(gap >= 8, "the wordmark and the breadcrumb are apart in a right-to-left page: " + gap);
  eq(await page.locator(".rv-card .rv-body").first().evaluate((e) => getComputedStyle(e).unicodeBidi), "plaintext", "the comment keeps its own order in the margin");
  await setLang("en");
});

await kit.test("removing a person asks first, and the button's name holds its visible word", async () => {
  const rory = await kit.signedIn({}, "reader@dev.scriobh");
  await rory.ctx.close();
  const add = await api(owner.ctx, "POST", `/api/v1/projects/${pid}/members`, { email: "reader@dev.scriobh", role: "reviewer" });
  ok(add.status < 300, "add " + add.status);
  const page = owner.page; kit.main = page;
  await page.goto(`${kit.base()}/p/${slug}/people`, { waitUntil: "load" });
  const row = page.locator(".people li", { hasText: "reader@dev.scriobh" });
  const btn = row.getByRole("button", { name: /^Remove/ });
  const label = await btn.getAttribute("aria-label");
  ok(/^Remove /.test(label) && label.includes("from the project"), "the accessible name starts with the visible word: " + label);
  await btn.click();
  await page.waitForSelector(".people .arm");
  eq(await page.evaluate(() => document.activeElement.textContent), "Cancel", "the question opens on Cancel");
  await page.keyboard.press("Escape");
  eq(await row.locator(".arm").count(), 0, "Escape puts it away");
  await btn.click();
  await page.keyboard.press("Enter"); await kit.sleep(60); await page.keyboard.press("Enter"); await kit.sleep(700);
  eq((await api(owner.ctx, "GET", `/api/v1/projects/${pid}/members`)).json.items.filter((m) => m.email === "reader@dev.scriobh").length, 1, "a double Enter did not remove them");
  if (await row.locator(".arm").count()) await page.keyboard.press("Escape"); // the second Enter, on the button again, only asks again
  await btn.click();
  await page.waitForSelector(".people .arm-ok:not([disabled])");
  await page.locator(".people .arm-ok").click();
  await page.waitForURL(/saved=out/);
  eq((await api(owner.ctx, "GET", `/api/v1/projects/${pid}/members`)).json.items.filter((m) => m.email === "reader@dev.scriobh").length, 0, "removed once confirmed");
});

await kit.finish("e2e-i18n");
