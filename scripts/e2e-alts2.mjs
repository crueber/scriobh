#!/usr/bin/env node
// The rest of the alternatives checks: the save contract (the server rewrites the comment line and the page follows without a
// false conflict), the keyboard alone, reduced motion, the 390 px phone, and the person's id in the draft keys.
//
//   node scripts/e2e-alts2.mjs [--only substring]
import { armAnswer, armOk, createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-alts2");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page } = await kit.signedIn();
kit.main = page;

let book, doc;
const PROSE = "The harbour was quiet that morning.\n\nMara counted the boats that had not come in.\n\nThe tide turned twice before noon.\n";
const text = (pg = page) => pg.evaluate(() => { const d = window.scriobh.write.editor.view.state.doc; return d.textBetween(0, d.content.size, "\n").replace(/<!-- scriobh:alts[\s\S]*$/, "").trim(); });
const stored = async () => (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
const list = async () => (await api(main, "GET", `/api/v1/docs/${doc}/alternatives`)).json;
const settled = (pg = page) => waitFor(async () => !(await pg.evaluate(() => window.scriobh.write.unsaved())), "the page to be saved", 10000);
const ready = async (pg = page, marks = true) => {
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.alts)), "alts.js", 15000);
  if (marks) await pg.waitForSelector(".scriobh-prose .scriobh-alt", { timeout: 20000 });
};
const lastLine = (md) => md.replace(/\n+$/, "").split("\n").pop();

await kit.test("setup: a chapter with an alternative on a word", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: "Alt Two", kind: "novel" })).json.item;
  doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "Harbour", content: PROSE });
  const c = await api(main, "POST", `/api/v1/docs/${doc}/alternatives`, { base_revision: cur.revision + 1, scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "calm"] });
  ok(c.status < 300, "create: " + c.status + JSON.stringify(c.json).slice(0, 200));
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready();
});

await kit.test("the save contract: a comment another tab rewrote is taken from the server, and a later change of details is not a conflict", async () => {
  kit.allowed = [/status of 409/]; // the stale-revision refusals this step provokes are the ordinary kind
  // Another tab adds a wording (the comment line changes; the text does not) while this page, which does not know, types.
  const a = (await list()).items[0];
  const add = await api(main, "POST", `/api/v1/docs/${doc}/alternatives/variants`, { base_revision: (await stored()).revision, alternative_id: a.id, text: "hushed" });
  ok(add.status < 300, "another tab's wording: " + add.status);
  // This page still holds the older revision: its first save is refused as stale, and the page settles it (only the comment differs).
  await page.locator(".scriobh-prose").click();
  await page.keyboard.press(`${MOD}+End`);
  await page.keyboard.type(" And it stayed so.");
  await settled();
  const s = await stored();
  ok(s.content.includes("And it stayed so."), "the typed text was saved");
  eq(await page.evaluate(() => window.scriobh.write.serverMd), s.content, "the page's idea of the stored text is the stored text, comment line included");
  eq(await page.locator("#nb-conflict").count(), 0, "no conflict bar");
  eq((await list()).items[0].variants.length, 4, "the other tab's wording was not lost");
  // A change of details elsewhere (a rename) and then more typing: no false 'changed somewhere else'.
  await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: s.revision, title: "Harbour, renamed" });
  await page.keyboard.type(" More.");
  await settled();
  eq(await page.locator("#nb-conflict").count(), 0, "still no conflict bar after a rename elsewhere");
  ok((await stored()).content.includes("More."), "saved");
});

await kit.test("drafts carry the person's id; a draft kept under the old key is taken over, never dropped", async () => {
  const keys = await page.evaluate(() => Object.keys(localStorage).filter((k) => /^scriobh:(draft|held):/.test(k)));
  ok(keys.every((k) => /^scriobh:(draft|held):usr_[^:]+:doc_/.test(k)), "every draft key names its owner: " + keys);
  // An old key for this chapter (text the server never had) and one for a chapter not opened.
  await page.evaluate((d) => {
    localStorage.setItem("scriobh:draft:" + d, JSON.stringify({ md: "Old unsaved words.\n", title: "T", rev: 1, t: Date.now() }));
    localStorage.setItem("scriobh:draft:doc_elsewhere", JSON.stringify({ md: "Other chapter words.\n", title: "T", rev: 1, t: Date.now() }));
  }, doc);
  await page.reload(); await ready(page, false);
  const after = await page.evaluate((d) => ({ legacy: localStorage.getItem("scriobh:draft:" + d), elsewhere: localStorage.getItem("scriobh:draft:doc_elsewhere"), held: Object.keys(localStorage).filter((k) => /^scriobh:held:usr_/.test(k)).map((k) => localStorage.getItem(k)).join("") }), doc);
  eq(after.legacy, null, "the old key for this chapter was taken over");
  ok(after.held.includes("Old unsaved words."), "its text is kept under the person's id and offered by the bar");
  ok(after.elsewhere && after.elsewhere.includes("Other chapter words."), "the key of a chapter that was not opened is left alone");
  await page.waitForSelector("#nb-draft");
  await page.evaluate(() => { for (const k of Object.keys(localStorage)) if (/^scriobh:(draft|held):/.test(k)) localStorage.removeItem(k); });
});

await kit.test("the keyboard alone: select, add an alternative, cycle, panel, ghost, revive, with no pointer", async () => {
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`); await ready(page, false);
  await page.locator(".scriobh-prose").focus();
  const mac = process.platform === "darwin", toStart = mac ? "Meta+ArrowUp" : "Control+Home";
  await page.keyboard.press(toStart);
  // The second paragraph's word "boats": move there with the arrows and select the word with Shift+Arrow.
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Home");
  for (let i = 0; i < 17; i++) await page.keyboard.press("ArrowRight"); // the start of "boats"
  for (let i = 0; i < 5; i++) await page.keyboard.press("Shift+ArrowRight");
  await page.keyboard.press("Alt+Shift+KeyA");
  const pop = page.locator("#alt-pop");
  await pop.waitFor();
  ok(await page.evaluate(() => document.activeElement.id === "alt-new"), "the form takes focus on its text box");
  await page.keyboard.type("vessels");
  await page.keyboard.press("Enter");
  await pop.waitFor({ state: "detached" });
  ok(await page.evaluate(() => document.activeElement.closest(".scriobh-prose") !== null), "focus returns to the text");
  await waitFor(async () => (await list()).active === 2, "the alternative exists");
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("vessels"), "cycled to the new wording");
  await page.keyboard.press("Alt+Shift+KeyO");
  await waitFor(async () => !(await text()).includes("vessels") || (await text()).includes("boats"), "back to the original");
  // The panel by the palette, and a button by Tab and Enter.
  await page.keyboard.press(`${MOD}+Shift+P`);
  await page.keyboard.type("alternatives of this");
  await page.keyboard.press("Enter");
  await page.locator("#ipanel-alts .ap-card").first().waitFor();
  ok(await page.evaluate(() => document.activeElement.id === "itab-alts"), "focus moves to the tab");
  let reached = false; // the close button comes after the tabs, then the panel
  for (let i = 0; i < 4 && !reached; i++) { await page.keyboard.press("Tab"); reached = await page.evaluate(() => !!document.activeElement.closest("#ipanel-alts")); }
  ok(reached, "Tab goes on into the panel");
  // Ghost the third paragraph (the others carry alternatives, which a ghost may not overlap) and revive it, by keys: the caret
  // goes back to where it was, which is inside the ghost.
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.press(toStart);
  await page.keyboard.press("ArrowDown"); await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Home");
  await page.keyboard.press("Shift+End");
  await page.keyboard.press("Alt+Shift+KeyG");
  await waitFor(async () => (await page.locator(".scriobh-ghost").count()) >= 1, "ghosted");
  await page.keyboard.press("ArrowLeft"); await page.keyboard.press("ArrowRight");
  await page.keyboard.press("Alt+Shift+KeyG");
  await waitFor(async () => (await page.locator(".scriobh-ghost").count()) === 0, "revived with the same key");
});

await kit.test("reduced motion: the removal fade and transitions are off, and cycling is unaffected", async () => {
  const ctx = await kit.newContext({ viewport: { width: 1280, height: 800 }, reducedMotion: "reduce" });
  await ctx.addCookies(await main.cookies());
  const pg = await ctx.newPage();
  kit.watch(pg, "reduced");
  await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready(pg);
  await pg.evaluate(() => window.scriobh.inspector.open("alts", false));
  await pg.locator("#ipanel-alts .ap-var").first().waitFor();
  const dur = await pg.locator("#ipanel-alts .ap-var").first().evaluate((e) => getComputedStyle(e).transitionDuration);
  ok(parseFloat(dur) < 0.001, "no transition on a wording: " + dur);
  await ctx.close();
});

await kit.test("the 390 px phone: the Alternatives tab is a sheet that fits, its buttons are 44 px, no sideways scroll", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 });
  await ctx.addCookies(await main.cookies());
  await page.goto("about:blank"); // one tab holds the writing lease: this one must not be locked out
  const pg = await ctx.newPage();
  kit.watch(pg, "phone");
  await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready(pg);
  await pg.evaluate(() => window.scriobh.inspector.open("alts", false));
  await pg.locator("#ipanel-alts .ap-card").first().waitFor();
  ok(await pg.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "no horizontal scroll");
  const small = await pg.locator("#ipanel-alts button, #ipanel-alts input").evaluateAll((els) => els.filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => (e.textContent || e.placeholder || "").trim() + " " + Math.round(e.getBoundingClientRect().height)));
  eq(small.length, 0, "every control is 44 px tall: " + small.join(", "));
  const box = await pg.locator("#inspector").boundingBox();
  ok(box && box.x >= 0 && box.x + box.width <= 390 + 1, "the sheet is inside the screen: " + JSON.stringify(box));
  await pg.evaluate(() => window.scriobh.inspector.close());
  // Add an alternative by the toolbar's button on a touch screen.
  await pg.evaluate(() => { const v = window.scriobh.write.editor.view; let hit; v.state.doc.descendants((n, p) => { if (!hit && n.isText && n.text.includes("tide")) hit = [p + n.text.indexOf("tide"), p + n.text.indexOf("tide") + 4]; }); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); });
  await pg.locator("#fmt-aa").tap(); // the strip is behind Aa on a phone (U11.3)
  await pg.locator('#fmtbar [data-cmd="revise"]').tap();
  await pg.locator('#revise-menu [data-cmd="alt"]').tap();
  await pg.locator("#alt-pop").waitFor();
  const pb = await pg.locator("#alt-pop").boundingBox();
  ok(pb.x >= 0 && pb.x + pb.width <= 391, "the add form is inside the screen: " + JSON.stringify(pb));
  await pg.keyboard.press("Escape");
  await ctx.close();
});


// ---- regression checks from the independent review: each of these lost or corrupted text before it was fixed ----------------
const stripAlt = (md) => md.replace(/\n*<!-- scriobh:alts[\s\S]*$/, "").replace(/\n+$/, "");
const editorMd = (pg = page) => pg.evaluate(() => window.scriobh.write.editor.getMarkdown());
const select = (s, pg = page) => pg.evaluate((s) => {
  const v = window.scriobh.write.editor.view; let hit = null;
  v.state.doc.descendants((n, pos) => { if (hit || !n.isText) return; const i = n.text.indexOf(s); if (i >= 0) hit = [pos + i, pos + i + s.length]; });
  if (!hit) throw new Error("not found: " + s);
  v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); v.focus();
}, s);
const caretIn = (s, off = 1, pg = page) => pg.evaluate(([s, off]) => {
  const v = window.scriobh.write.editor.view; let hit = null;
  v.state.doc.descendants((n, pos) => { if (hit || !n.isText) return; const i = n.text.indexOf(s); if (i >= 0) hit = pos + i + off; });
  if (hit == null) throw new Error("not found: " + s);
  v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.near(v.state.doc.resolve(hit)))); v.focus();
}, [s, off]);
// A new chapter with alternatives, opened in the main page.
const fresh = async (content, alts, title = "Review") => {
  book = (await api(main, "POST", "/api/v1/projects", { title, kind: "novel" })).json.item;
  doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  let rev = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item.revision;
  rev = (await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, title: "Harbour", content })).json.item.revision;
  for (const a of alts) {
    const r = await api(main, "POST", `/api/v1/docs/${doc}/alternatives`, { base_revision: rev, ...a });
    ok(r.status < 300, "alternative: " + r.status + JSON.stringify(r.json).slice(0, 160));
    rev = r.json.revision;
  }
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready(page, alts.length > 0);
};
// The page and the server hold the same text (the comment line aside), and the page does not say it is unsaved.
const agree = async (what) => {
  await settled();
  await waitFor(async () => stripAlt(await editorMd()) === stripAlt((await stored()).content), what + ": the editor and the server hold the same text", 8000);
};

await kit.test("review 1: an answer about the marks that arrives after typing is not drawn on the wrong words", async () => {
  await fresh(PROSE, [{ scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "calm"] }]);
  const slow = async (route) => { if (route.request().method() === "GET") await sleep(450); await route.continue().catch(() => {}); };
  await page.route("**/api/v1/docs/*/alternatives", slow);
  await page.evaluate(() => { window.scriobh.alts.refresh(); }); // the request leaves now and the answer comes 450 ms later
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+ArrowUp" : "Control+Home");
  await page.keyboard.type("ur wa  ", { delay: 10 });
  // Sample the marks while the late answer lands and after: they are on quiet or not drawn, never on the typed text.
  const seen = new Set();
  for (let i = 0; i < 25; i++) { seen.add((await page.locator(".scriobh-alt").allTextContents()).join("|")); await sleep(60); }
  await page.unroute("**/api/v1/docs/*/alternatives", slow);
  ok([...seen].every((x) => x === "" || x === "quiet"), "the marks were drawn on: " + JSON.stringify([...seen]));
  await waitFor(async () => (await page.locator(".scriobh-alt").allTextContents()).join("|") === "quiet", "the mark to come back on quiet: " + (await page.locator(".scriobh-alt").allTextContents()).join("|"), 8000);
  await caretIn("quiet", 2);
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("still that morning"), "the cycle put the new wording on quiet: " + (await text()));
  await agree("after the cycle");
});

await kit.test("review 2: text typed while a change waits for the save is kept or refused, never shown and lost", async () => {
  await fresh(PROSE, [{ scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "calm"] }]);
  const slow = async (route) => { if (route.request().method() === "PATCH") await sleep(1500); await route.continue().catch(() => {}); };
  await page.route("**/api/v1/docs/*", slow);
  // Which keys did the page refuse? (Seen after every handler has run.)
  await page.evaluate(() => { window.__keys = []; document.addEventListener("keydown", (e) => window.__keys.push([e.key, e.defaultPrevented])); });
  const accepted = async (ch) => (await page.evaluate((ch) => window.__keys.filter(([k, p]) => k === ch && !p).length, ch));
  const count = async (ch) => (stripAlt((await stored()).content).match(new RegExp(ch, "g")) || []).length;
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+ArrowUp" : "Control+Home");
  await page.keyboard.type("AAA");
  await caretIn("quiet", 2);
  await page.keyboard.press("Alt+Shift+KeyN"); // flushes AAA (the save takes 1.5 s), then cycles
  await sleep(1100);
  await page.keyboard.type("BBB");
  await sleep(2500);
  await page.unroute("**/api/v1/docs/*", slow);
  await agree("cycle");
  ok(!(await page.evaluate(() => window.scriobh.write.unsaved())), "nothing is left unsaved");
  ok((await stored()).content.includes("AAA"), "what was typed before the change is saved");
  eq(await count("B"), await accepted("B"), "every B the page took is on the server, and none it refused is");

  // The same with an op that takes the stored text whole (ghost): the typing is not wiped by the reload.
  await page.route("**/api/v1/docs/*", slow);
  await page.keyboard.type("CCC");
  await select("tide");
  await page.keyboard.press("Alt+Shift+KeyG");
  await sleep(1100);
  await page.keyboard.type("DDD");
  await sleep(2500);
  await page.unroute("**/api/v1/docs/*", slow);
  await agree("ghost");
  ok((await stored()).content.includes("CCC"), "what was typed before the ghost is saved");
  eq(await count("D"), await accepted("D"), "every D the page took is on the server, and none it refused is");
});

await kit.test("review 3: typing at the edge of a mark, the server adopts it, and the next cycle still agrees", async () => {
  await fresh(PROSE, [{ scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "calm"] }]);
  await caretIn("quiet", 5);
  await page.keyboard.type("s");
  await settled();
  await caretIn("quiet", 2); // straight away: the marks have not had time to be asked for again
  await page.keyboard.press("Alt+Shift+KeyN");
  await sleep(900);
  await agree("after the cycle at an edited mark");
});

await kit.test("review 4: Send to the stash removes the text that was stashed, not the text selected later", async () => {
  await fresh(PROSE, []);
  const slow = async (route) => { if (route.request().method() === "POST") await sleep(800); await route.continue().catch(() => {}); };
  await page.route("**/api/v1/projects/*/stash", slow);
  await select("Mara counted the boats that had not come in.");
  await page.keyboard.press("Alt+Shift+KeyS");
  await sleep(200);
  await select("The tide turned twice before noon.");
  await sleep(1500);
  await page.unroute("**/api/v1/projects/*/stash", slow);
  const t = await text();
  ok(t.includes("The tide turned twice before noon."), "the later selection is still in the chapter: " + t);
  ok(!t.includes("Mara counted"), "the stashed text left the chapter: " + t);
  const items = (await api(main, "GET", `/api/v1/projects/${book.id}/stash`)).json.items;
  ok(items.length === 1 && items[0].content.includes("Mara counted"), "the stash holds what was selected when the key was pressed");
  await agree("after the stash");
});

await kit.test("review 5: a wording does not take on the emphasis of the text it replaces", async () => {
  await fresh("*Quiet* harbour, said Mara.\n\nThe tide turned twice before noon.\n", [{ scope: "paragraph", quote: "*Quiet* harbour, said Mara.", variants: ["Still water, said Mara."] }]);
  await caretIn("harbour", 2);
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("Still water, said Mara."), "the paragraph cycled");
  await agree("after cycling the paragraph");
  ok(!(await editorMd()).includes("*Still"), "no emphasis was added to the new wording");
});


await kit.test("review 9: the palette opens the project it names when two readable projects share a slug", async () => {
  const ada = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "admin@dev.scriobh");
  const theirs = (await api(ada.ctx, "POST", "/api/v1/projects", { title: "Shared Name Book", kind: "novel" })).json.item;
  const mine = (await api(main, "POST", "/api/v1/projects", { title: "Shared Name Book", kind: "novel" })).json.item;
  eq(theirs.slug, mine.slug, "the two projects have one slug");
  ok((await api(ada.ctx, "POST", `/api/v1/projects/${theirs.id}/members`, { email: "writer@dev.scriobh", role: "reader" })).status < 300, "Wren can read Ada's");
  const list = (await api(main, "GET", "/api/v1/projects?state=all")).json.items.filter((p) => p.title === "Shared Name Book");
  eq(new Set(list.map((p) => p.address)).size, 2, "the two have different addresses: " + list.map((p) => p.address));
  const landed = [];
  for (let i = 0; i < 2; i++) {
    await page.goto(`${base()}/`);
    await page.keyboard.press("ControlOrMeta+KeyK");
    await page.keyboard.type("Shared Name Book");
    await sleep(500);
    for (let k = 0; k < i; k++) await page.keyboard.press("ArrowDown");
    await Promise.all([page.waitForURL((u) => u.pathname.startsWith("/p/"), { timeout: 10000 }), page.keyboard.press("Enter")]);
    landed.push(new URL(page.url()).pathname.split("/")[2]);
  }
  eq(new Set(landed).size, 2, "the two entries open two different projects: " + landed);
  ok(landed.every((a) => list.some((p) => p.address === a)), "each by its address: " + landed);
  await ada.ctx.close();
});


await kit.test("review 11: a chord pressed while an input method is composing is left to it", async () => {
  await fresh(PROSE, []);
  await select("quiet");
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.dom.dispatchEvent(new KeyboardEvent("keydown", { code: "KeyA", key: "Å", altKey: true, shiftKey: true, isComposing: true, keyCode: 229, bubbles: true, cancelable: true })); });
  await sleep(300);
  eq(await page.locator("#alt-pop").count(), 0, "no add form while composing");
  await page.keyboard.press("Alt+Shift+KeyA");
  await page.locator("#alt-pop").waitFor();
  await page.keyboard.press("Escape");
});

await kit.test("review 12: the cycler stays off the line being read, and the panel keeps what is typed in it", async () => {
  await fresh(PROSE + "\nA line below, so there is room.\n\nAnd another, and another.\n", [{ scope: "word", quote: "turned", before: "tide ", after: " twice", variants: ["swung", "came round"] }]);
  await page.locator(".scriobh-alt").first().scrollIntoViewIfNeeded();
  await page.locator(".scriobh-alt").first().click();
  await page.locator("#alt-cyc").waitFor();
  const r = await page.evaluate(() => { const c = document.querySelector("#alt-cyc").getBoundingClientRect(), m = document.querySelector(".scriobh-alt").getBoundingClientRect(); return { cb: c.bottom, ct: c.top, mt: m.top, mb: m.bottom }; });
  ok(r.cb <= r.mt + 1 || r.ct >= r.mb - 1, "the cycler is clear of the mark's line: " + JSON.stringify(r));
  ok(r.cb <= r.mt + 1, "and it is above, not over the next lines: " + JSON.stringify(r));
  await page.keyboard.press("Escape");
  // The panel: type in the "Another wording" box, let the marks be asked for again, and the box still has its words and focus.
  await page.evaluate(() => window.scriobh.inspector.open("alts", false));
  const box = page.locator("#ipanel-alts .ap-add input").first();
  await box.waitFor();
  await box.focus();
  await page.keyboard.type("half a wor");
  await page.evaluate(() => window.scriobh.alts.refresh());
  await sleep(1200);
  eq(await page.locator("#ipanel-alts .ap-add input").first().inputValue(), "half a wor", "what was typed is still in the box");
  ok(await page.evaluate(() => document.activeElement.closest(".ap-add") !== null), "and the box still has the focus");
  // One announcement, not two.
  await page.evaluate(() => { document.getElementById("live").textContent = ""; window.scriobh.alts.note("Say this once."); });
  await sleep(300);
  eq(await page.locator("#live").innerText(), "", "the status line is quiet when the toast speaks");
  ok((await page.locator("#toasts").innerText()).includes("Say this once."), "the toast says it");
});

await kit.test("review 12: the formatting bar says when it is wider than the screen, and the 44 px targets are wide too", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 });
  await ctx.addCookies(await main.cookies());
  await page.goto("about:blank");
  const pg = await ctx.newPage();
  kit.watch(pg, "phone");
  await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await ready(pg);
  // On a phone the strip is behind Aa (U11.3): open it, then the "more" button is there to scroll the bar.
  await pg.waitForFunction(() => performance.getEntriesByType("resource").some((r) => /\/js\/phone\./.test(r.name)), null, { timeout: 8000 });
  await pg.locator("#fmt-aa").tap();
  await pg.waitForSelector(".fmt-more:not([hidden])", { timeout: 5000 });
  const w0 = await pg.evaluate(() => document.querySelector(".fmt-track").scrollLeft);
  await pg.locator(".fmt-more").tap();
  await sleep(700);
  ok((await pg.evaluate(() => document.querySelector(".fmt-track").scrollLeft)) > w0, "the button scrolled the bar");
  for (let i = 0; i < 5 && (await pg.locator(".fmt-more").innerText()) === "\u203a"; i++) { await pg.locator(".fmt-more").tap(); await sleep(700); }
  const hidden = await pg.evaluate(() => { const bar = document.querySelector(".fmt-track").getBoundingClientRect(), more = document.querySelector(".fmt-more").getBoundingClientRect(); return [...document.querySelectorAll('#fmtbar [data-cmd="alt"], #fmtbar [data-cmd="ghost"], #fmtbar [data-cmd="stash"]')].filter((b) => b.getBoundingClientRect().right > more.left + 1 || b.getBoundingClientRect().left < bar.left - 1).length; });
  eq(hidden, 0, "after scrolling to the end, the new buttons are on screen and not under the arrow");
  // Targets: wide as well as tall.
  await pg.evaluate(() => window.scriobh.inspector.open("alts", false));
  await pg.locator("#ipanel-alts .ap-card").first().waitFor();
  const small = await pg.locator("#ipanel-alts button").evaluateAll((els) => els.filter((e) => e.offsetParent && (e.getBoundingClientRect().width < 43.5 || e.getBoundingClientRect().height < 43.5)).map((e) => (e.textContent || "").trim() + " " + Math.round(e.getBoundingClientRect().width) + "x" + Math.round(e.getBoundingClientRect().height)));
  eq(small.length, 0, "every button of the panel is 44 px both ways: " + small.join(", "));
  await pg.evaluate(() => window.scriobh.inspector.close());
  await pg.locator(".scriobh-alt").first().tap();
  await pg.locator("#alt-cyc").waitFor();
  const cs = await pg.locator("#alt-cyc button").evaluateAll((els) => els.filter((e) => e.getBoundingClientRect().width < 43.5 || e.getBoundingClientRect().height < 43.5).map((e) => (e.textContent || "").trim() + " " + Math.round(e.getBoundingClientRect().width) + "x" + Math.round(e.getBoundingClientRect().height)));
  eq(cs.length, 0, "the cycler's buttons are 44 px both ways: " + cs.join(", "));
  await ctx.close();
});

await kit.test("review 13: if the formatting bar's script does not arrive the bar says so and the text still saves", async () => {
  kit.allowed = [/Failed to load resource/];
  await page.route("**/static/js/format.*.js", (route) => route.abort());
  await fresh(PROSE, []);
  await page.waitForSelector("#fmtbar .fmt-gone", { timeout: 8000 });
  ok(/could not be loaded/.test(await page.locator("#fmtbar").innerText()), "the bar says what happened");
  eq(await page.locator("#fmtbar button.tb").count(), 0, "no dead buttons are left");
  await page.locator(".scriobh-prose").click();
  await page.keyboard.type(" Typed anyway.");
  await settled();
  ok((await stored()).content.includes("Typed anyway."), "the text saved");
  await page.unroute("**/static/js/format.*.js");
});

await kit.test("review 13: a passage that runs across a line break of the saved text is named once the editor's spelling is stored", async () => {
  kit.allowed = [/status of 4\d\d/];
  await fresh("Mara counted the\nboats that had not come in.\n\nThe tide turned twice before noon.\n", []);
  await select("counted");
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; const s = v.state.selection; let hit; v.state.doc.descendants((n, p) => { if (!hit && n.isText && n.text.includes("counted")) hit = p + n.text.indexOf("counted"); }); v.dispatch(v.state.tr.setSelection(s.constructor.create(v.state.doc, hit, hit + "counted the boats".length))); v.focus(); });
  await page.keyboard.press("Alt+Shift+KeyA");
  const pop = page.locator("#alt-pop");
  await pop.waitFor();
  await pop.locator("textarea").fill("tallied the boats");
  await page.keyboard.press("Enter");
  await sleep(1500);
  const shown = (await page.locator("#alt-pop .field-error").allInnerTexts()).join(" ");
  ok(!/not in the document at this revision/.test(shown), "the server's own sentence is not shown: " + shown);
  const made = (await list()).active;
  eq(made, 1, "the alternative was made (the editor's spelling is stored and the passage asked for again): " + shown);
});


await kit.test("review 2: while a change is held for its save, undo, bold, the toolbar, paste and drop do not change the text either", async () => {
  await fresh(PROSE, [{ scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "calm"] }]);
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+ArrowUp" : "Control+Home");
  await page.keyboard.type("AAA");
  const slow = async (route) => { if (route.request().method() === "PATCH") await sleep(1500); await route.continue().catch(() => {}); };
  await page.route("**/api/v1/docs/*", slow);
  await select("tide");
  await page.keyboard.press("Alt+Shift+KeyG"); // held until AAA is saved and the ghost is made
  const before = await editorMd();
  await sleep(300);
  await page.keyboard.press("ControlOrMeta+KeyB");
  await page.keyboard.press("ControlOrMeta+KeyZ");
  await page.locator('#fmtbar [data-cmd="italic"]').click({ force: true }).catch(() => {});
  await page.evaluate(() => {
    const v = window.scriobh.write.editor.view, dt = new DataTransfer();
    dt.setData("text/plain", "PASTED"); dt.setData("text/html", "<p>PASTED</p>");
    v.dom.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
    const d2 = new DataTransfer(); d2.setData("text/plain", "DROPPED");
    v.dom.dispatchEvent(new DragEvent("drop", { dataTransfer: d2, bubbles: true, cancelable: true, clientX: 200, clientY: 200 }));
  });
  eq(await editorMd(), before, "none of those changed the text while it was held");
  await sleep(2600);
  await page.unroute("**/api/v1/docs/*", slow);
  await agree("after the hold");
  const md = (await stored()).content;
  ok(!md.includes("PASTED") && !md.includes("DROPPED") && md.includes("AAA"), "nothing was pasted or dropped, and the typed text is saved");
});


await kit.test("review 2: a character typed and deleted leaves nothing to save, and a ghost made at once is still taken whole", async () => {
  await fresh(PROSE, []);
  await caretIn("tide", 4);
  await page.keyboard.type("x");
  await page.keyboard.press("Backspace");
  await select("Mara counted");
  await page.keyboard.press("Alt+Shift+KeyG");
  await waitFor(async () => (await page.locator(".scriobh-ghost").count()) >= 1, "ghosted");
  eq(await page.locator("#nb-conflict").count(), 0, "no conflict bar");
  ok(!(await page.locator("#toasts").innerText()).includes("same moment"), "no 'changed at the same moment' message");
  await agree("after the ghost");
});

await kit.test("review 2: the hold lets go when the save fails, and when it never answers", async () => {
  kit.allowed = [/Failed to load resource|net::ERR/];
  await fresh(PROSE, []);
  const refuse = async (route) => { if (route.request().method() === "PATCH") await route.abort().catch(() => {}); else await route.continue().catch(() => {}); };
  const never = async (route) => { if (route.request().method() === "PATCH") await new Promise(() => {}); else await route.continue().catch(() => {}); };
  const busy = () => page.evaluate(() => window.scriobh.alts.busy);
  try {
    await page.route("**/api/v1/docs/*", refuse);
    await page.locator(".scriobh-prose").focus();
    await page.keyboard.press(process.platform === "darwin" ? "Meta+ArrowUp" : "Control+Home");
    await page.keyboard.type("AAA");
    await select("tide");
    await page.keyboard.press("Alt+Shift+KeyG");
    await waitFor(async () => !(await busy()), "the hold to end after a refused save", 15000);
    await page.keyboard.type("ZZZ");
    ok((await text()).includes("ZZZ"), "the editor takes keys again");
  } finally { await page.unroute("**/api/v1/docs/*", refuse); }
  await settled();
  // A save that is never answered: the page gives up on it after its own time limit (30 s), and the hold ends then.
  try {
    await page.route("**/api/v1/docs/*", never);
    await page.keyboard.type("BBB");
    await select("Mara");
    await page.keyboard.press("Alt+Shift+KeyG");
    await sleep(1500);
    ok(await busy(), "held while the save is outstanding");
    await waitFor(async () => !(await busy()), "the hold to end after a save that was never answered", 45000);
    await page.keyboard.type("YYY");
    ok((await text()).includes("YYY"), "and the editor takes keys again");
  } finally { await page.unroute("**/api/v1/docs/*", never); }
});

await kit.test("review 2: from the Source view nothing is changed in the text behind the box", async () => {
  await fresh(PROSE, [{ scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "calm"] }]);
  await caretIn("quiet", 2);
  await page.evaluate(() => window.scriobh.write.editor.toggleSource(true));
  const before = await editorMd();
  const r = await page.evaluate(() => window.scriobh.alts.cycle("next"));
  await sleep(500);
  eq(await editorMd(), before, "the text is as it was");
  ok(/Source view/.test(await page.locator("#toasts").innerText()), "it says why");
  await page.evaluate(() => window.scriobh.write.editor.toggleSource(false));
});

await kit.test("review 13: the ghost under the caret is revived by where it is, even when the list is out of date", async () => {
  await fresh(PROSE, []);
  let rev = (await stored()).revision;
  for (const [q2, b] of [["Mara counted", ""], ["The tide turned", "Mara"]]) {
    const g = await api(main, "POST", `/api/v1/docs/${doc}/ghosts`, { base_revision: rev, ranges: [{ quote: q2, before: b }] });
    ok(g.status < 300, "ghost: " + g.status + JSON.stringify(g.json).slice(0, 120));
    rev = g.json.revision;
  }
  await page.reload(); await ready(page, false);
  await sleep(800); // the page asks for its ghosts once, now
  // The first ghost is deleted by hand (tags and all) and the second is revived straight away: the page's list still has the first.
  await page.evaluate(() => {
    const v = window.scriobh.write.editor.view; let first = null, last = null;
    v.state.doc.descendants((n, p) => { if (n.type.name === "source_inline" && /scriobh-ghost>$/.test(n.attrs.raw) && !n.attrs.raw.startsWith("</")) { if (first == null) first = p; } if (n.type.name === "source_inline" && n.attrs.raw === "</scriobh-ghost>" && last == null) last = p + n.nodeSize; });
    v.dispatch(v.state.tr.delete(first, last));
  });
  await caretIn("tide turned", 2);
  await page.keyboard.press("Alt+Shift+KeyG");
  await waitFor(async () => (await page.locator(".scriobh-ghost").count()) === 0, "the second ghost revived");
  eq((await api(main, "GET", `/api/v1/docs/${doc}/ghosts`)).json.items.length, 0, "and the server has none left");
});

await kit.test("review 13: an 'a' or 'an' before a cycled word is mended even when another change is waiting behind it", async () => {
  await fresh("She walked a quiet mile.\n\nThe tide turned twice before noon.\n", [{ scope: "word", quote: "quiet", before: "a ", after: " mile", variants: ["old"] }]);
  const aid = (await list()).items[0].id;
  await caretIn("quiet", 2);
  await page.keyboard.press("Alt+Shift+KeyN");
  await page.evaluate((aid) => { window.scriobh.alts.addVariant(aid, "tired"); }, aid); // queued straight behind it
  await waitFor(async () => (await text()).includes("an old mile"), "the article was mended: " + (await text()));
  await agree("after the cycle and the added wording");
});

await kit.test("review 8: signing out asks about an old-format draft for a chapter that was not opened, and keeps it when the answer is no", async () => {
  const other = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Other chapter", kind: "chapter", content: "Saved words.\n" })).json.item.id;
  await page.goto(`${base()}/`);
  await page.evaluate(() => { for (const k of Object.keys(localStorage)) if (/^scriobh:(draft|held):/.test(k)) localStorage.removeItem(k); });
  await page.evaluate((d) => localStorage.setItem("scriobh:draft:" + d, JSON.stringify({ md: "Words the server never had.\n", title: "Other chapter", rev: 1, t: Date.now() })), other);
  await page.evaluate(() => document.querySelector("form[data-signs-out]").requestSubmit());
  const asked = await armAnswer(page, "cancel"); // asked in the page, in the account menu: the browser's own panel is never used
  ok(/unsaved text/.test(asked) && /: 1\)/.test(asked), "it names the unsaved text: " + asked);
  await sleep(500);
  ok(await page.evaluate((d) => localStorage.getItem("scriobh:draft:" + d) !== null, other), "the draft is still there after No");
  ok(await page.locator("body.signed-in").count(), "still signed in after No");
  await page.evaluate((d) => localStorage.removeItem("scriobh:draft:" + d), other);
});


// ---- the second round of review fixes --------------------------------------------------------------------------------------
const patches = () => { const n = { count: 0 }; page.on("request", (r) => { if (r.method() === "PATCH" && /\/api\/v1\/docs\//.test(r.url())) n.count++; }); return n; };
const bodyOf = (md) => stripAlt(md);

await kit.test("round 2, 1: a pasted lone surrogate is repaired, the save settles without a storm, and an op afterwards works", async () => {
  await fresh(PROSE, [{ scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "calm"] }]);
  const sent = patches();
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+ArrowDown" : "Control+End");
  await page.evaluate(() => {
    const v = window.scriobh.write.editor.view, dt = new DataTransfer();
    dt.setData("text/plain", "lone \uD800 half");
    v.dom.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
  });
  await settled();
  await sleep(1500);
  ok(sent.count <= 3, "the page did not send the text over and over: " + sent.count + " saves");
  ok((await stored()).content.includes("lone � half"), "the half is stored as the replacement character");
  await caretIn("quiet", 2);
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("still that morning"), "the change after it works");
  await agree("after the change");
  ok(!(await page.evaluate(() => window.scriobh.alts.busy)), "and the editor is free");
});

await kit.test("round 2, 1: if the server stores something other than was sent, the page takes what it holds as saved", async () => {
  await fresh(PROSE, [{ scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "calm"] }]);
  const echo = async (route) => {
    if (route.request().method() !== "PATCH") return route.continue().catch(() => {});
    const r = await route.fetch(), j = await r.json();
    if (j.item && typeof j.item.content === "string") j.item.content = j.item.content.replace("Mara counted", "Mara  counted"); // the server "repairs" the text
    await route.fulfill({ response: r, json: j }).catch(() => {});
  };
  const sent = patches();
  await page.route("**/api/v1/docs/*", echo);
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+ArrowUp" : "Control+Home");
  await page.keyboard.type("Typed. ");
  await sleep(4000);
  await page.unroute("**/api/v1/docs/*", echo);
  ok(sent.count <= 3, "the page did not storm: " + sent.count + " saves");
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "it to be settled", 5000);
  const user = await page.evaluate(() => document.querySelector('meta[name="scriobh-user"]').content);
  eq(await page.evaluate(([d, u]) => localStorage.getItem(`scriobh:draft:${u}:${d}`), [doc, user]), null, "no draft is left behind for the text the server repaired");
  await page.reload(); await ready(page);
  eq(await page.locator("#nb-draft").count(), 0, "and the bar does not come back on the next load");
});

await kit.test("round 2, 2: a chapter that holds the old marker characters, an astral character and a repeated sentence is named at the right place", async () => {
  const body = "Intro  old words  end \u{1F600}.\n\nLater the word target sits here. The tide turned. Mara waited. The tide turned. Mara waited. Done.\n";
  await fresh(body, []);
  const add = async (picked, word) => {
    await select(picked);
    await page.keyboard.press("Alt+Shift+KeyA");
    const pop = page.locator("#alt-pop");
    await pop.waitFor();
    await pop.locator('input[value="word"]').check();
    await pop.locator("textarea").fill(word);
    await page.keyboard.press("Enter");
    await pop.waitFor({ state: "detached", timeout: 10000 });
  };
  await add("target", "goal");
  const first = (await list()).items[0], md = bodyOf((await stored()).content);
  eq(md.slice(first.start, first.end), "target", "the alternative is on the word that was selected");
  // The second of two equal sentences.
  await page.evaluate(() => {
    const v = window.scriobh.write.editor.view; let hit = null, n2 = 0;
    v.state.doc.descendants((n, p) => { if (!hit && n.isText) { let i = -1; while ((i = n.text.indexOf("Mara waited", i + 1)) >= 0) { if (++n2 === 2) { hit = [p + i, p + i + 11]; break; } } } });
    v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); v.focus();
  });
  await page.keyboard.press("Alt+Shift+KeyA");
  const pop = page.locator("#alt-pop");
  await pop.waitFor();
  await pop.locator('input[value="sentence"]').check().catch(() => {});
  await pop.locator("textarea").fill("Mara stood by");
  await page.keyboard.press("Enter");
  await pop.waitFor({ state: "detached", timeout: 10000 });
  const items = (await list()).items, md2 = bodyOf((await stored()).content);
  const second = items.find((a) => a.variants.some((v) => v.text === "Mara stood by"));
  eq(second.start, md2.lastIndexOf("Mara waited"), "the second occurrence, not the first: " + second.start + " vs " + md2.lastIndexOf("Mara waited"));
  eq(md2.slice(first.start, first.end), "target", "the first alternative is where it was");
});

await kit.test("round 2, 2: a mark that points at the same words in another place is not replaced", async () => {
  await fresh("The tide turned twice. Then the tide turned back.\n\nSecond paragraph here.\n", [{ scope: "word", quote: "turned", before: "tide ", after: " back", variants: ["swung", "came round"] }]);
  await sleep(1500); // the page's own first ask for the marks has come and gone
  // The page's idea of where the mark is drifts onto the first "turned" (same word, another place).
  await page.evaluate(() => {
    const m = window.scriobh.alts.marksOf()[0], v = window.scriobh.write.editor.view; let hit = null;
    v.state.doc.descendants((n, p) => { if (!hit && n.isText && n.text.includes("turned")) hit = p + n.text.indexOf("turned"); });
    m.from = hit; m.to = hit + 6;
  });
  await caretIn("tide turned twice", 8); // inside the first "turned", where the drifted mark now is
  await page.keyboard.press("Alt+Shift+KeyN");
  await sleep(900);
  await agree("after the cycle with a mark that had drifted");
  ok((await text()).startsWith("The tide turned twice."), "the first 'turned' was not touched: " + (await text()));
});

await kit.test("round 2, 3: legacy drafts are adopted for a writer without losing any, and a Discard removes only the copy it names", async () => {
  await fresh(PROSE, []);
  const user = await page.evaluate(() => document.querySelector('meta[name="scriobh-user"]').content);
  const mk = (md, t) => ({ md, title: "T", rev: 1, t });
  await page.evaluate(([d, user, mine, legacyDraft, legacyHeld]) => {
    for (const k of Object.keys(localStorage)) if (/^scriobh:(draft|held):/.test(k)) localStorage.removeItem(k);
    localStorage.setItem(`scriobh:held:${user}:${d}`, JSON.stringify(mine));
    localStorage.setItem("scriobh:draft:" + d, JSON.stringify(legacyDraft));
    localStorage.setItem("scriobh:held:" + d, JSON.stringify(legacyHeld));
  }, [doc, user, [mk("Mine one.\n", 5), mk("Mine two.\n", 4)], mk("Legacy draft.\n", 3), [mk("Legacy held a.\n", 2), mk("Legacy held b.\n", 1), mk("Legacy held c.\n", 0)]]);
  await page.reload(); await ready(page, false);
  const kept = await page.evaluate(([d, user]) => ({ held: JSON.parse(localStorage.getItem(`scriobh:held:${user}:${d}`) || "[]").map((x) => x.md), ld: localStorage.getItem("scriobh:draft:" + d), lh: localStorage.getItem("scriobh:held:" + d) }), [doc, user]);
  for (const w of ["Mine one.", "Mine two.", "Legacy draft.", "Legacy held a.", "Legacy held b.", "Legacy held c."]) ok(kept.held.some((m) => m.includes(w)), `"${w}" is kept: ` + JSON.stringify(kept.held));
  eq(kept.ld, null, "the old draft key is gone once its text is kept");
  eq(kept.lh, null, "and so is the old held key");
  // The bar describes the newest copy; Discard removes that one only.
  await page.waitForSelector("#nb-draft");
  ok(/older cop/.test(await page.locator("#nb-draft").innerText()), "the bar says there are older copies");
  const n0 = (await page.evaluate(([d, user]) => JSON.parse(localStorage.getItem(`scriobh:held:${user}:${d}`)).length, [doc, user]));
  await page.locator("#nb-draft button", { hasText: "Discard" }).click();
  await sleep(300);
  const n1 = (await page.evaluate(([d, user]) => JSON.parse(localStorage.getItem(`scriobh:held:${user}:${d}`) || "[]").length, [doc, user]));
  eq(n1, n0 - 1, "Discard removed one copy and left the others");
  await page.evaluate(() => { for (const k of Object.keys(localStorage)) if (/^scriobh:(draft|held):/.test(k)) localStorage.removeItem(k); });
});

await kit.test("round 2, 3: a reader's page leaves a legacy draft where it is", async () => {
  await fresh(PROSE, []);
  const rory = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "reader@dev.scriobh");
  ok((await api(main, "POST", `/api/v1/projects/${book.id}/members`, { email: "reader@dev.scriobh", role: "reader" })).status < 300, "Rory can read the chapter");
  await rory.page.goto(`${base()}/`);
  await rory.page.evaluate((d) => localStorage.setItem("scriobh:draft:" + d, JSON.stringify({ md: "A writer's words.\n", title: "T", rev: 1, t: 1 })), doc);
  await rory.page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await rory.page.waitForSelector(".scriobh-prose");
  await sleep(800);
  const kept = await rory.page.evaluate((d) => ({ legacy: localStorage.getItem("scriobh:draft:" + d), held: Object.keys(localStorage).filter((k) => /^scriobh:held:/.test(k)) }), doc);
  ok(kept.legacy && kept.legacy.includes("A writer's words."), "the old draft is still where it was");
  eq(kept.held.length, 0, "and nothing was filed under the reader");
  await rory.ctx.close();
});

await kit.test("round 2, 4: during a held change the Source view cannot be entered and nothing is typed behind the box", async () => {
  await fresh(PROSE, [{ scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "calm"] }]);
  await page.locator(".scriobh-prose").focus();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+ArrowUp" : "Control+Home");
  await page.keyboard.type("AAA");
  const slow = async (route) => { if (route.request().method() === "PATCH") await sleep(1500); await route.continue().catch(() => {}); };
  await page.route("**/api/v1/docs/*", slow);
  await caretIn("quiet", 2);
  await page.keyboard.press("Alt+Shift+KeyN"); // held while AAA is saved
  await sleep(300);
  await page.keyboard.press("ControlOrMeta+KeyE");
  await page.evaluate(() => window.scriobh.write.editor.toggleSource(true));
  eq(await page.evaluate(() => window.scriobh.write.editor.mode()), "rich", "still the text view");
  await sleep(2600);
  await page.unroute("**/api/v1/docs/*", slow);
  await waitFor(async () => (await text()).includes("still that morning"), "the choice stood: " + (await text()));
  await agree("after the held choice");
});

await kit.test("round 2, 5: a chapter spelled differently from the editor still puts a stash item exactly at the cursor", async () => {
  const cases = [
    ["a * b here.\n\nSecond paragraph.\n", "here"],
    ["line one  \nline two here.\n\nSecond paragraph.\n", "here"],
    ["# Title #\n\nbody here.\n", "here"],
    ["a\\_b here.\n\nSecond paragraph.\n", "here"],
    ["[link]( http://x.test ) here.\n\nSecond paragraph.\n", "here"],
  ];
  for (const [md, word] of cases) {
    await fresh(md, []);
    await api(main, "POST", `/api/v1/projects/${book.id}/stash`, { title: "X", kind: "paragraph", content: "XX" });
    await caretIn(word, 2);
    await page.evaluate(() => window.scriobh.inspector.open("stash", false));
    const card = page.locator("#ipanel-stash .ap-card").first();
    await card.waitFor();
    await card.getByRole("button", { name: /Put .* in the chapter/ }).click();
    const form = page.locator("#ipanel-stash form.ap-put");
    await form.locator("select[name=where]").selectOption("cursor");
    await form.locator("select[name=placement]").selectOption("inline");
    await form.locator("select[name=mode]").selectOption("copy");
    await form.getByRole("button", { name: "Put it in the chapter" }).click();
    await waitFor(async () => (await text()).includes("XX"), "the item to be put in: " + JSON.stringify(md));
    ok((await text()).includes("heXXre"), `exactly at the cursor, in ${JSON.stringify(md)}: ` + (await text()));
    await agree("after the put-back of " + JSON.stringify(md));
  }
});

await kit.test("round 2, 6: the hold ends once the change has landed, and Tab is never trapped by it", async () => {
  await fresh(PROSE, []);
  const slow = async (route) => { if (route.request().method() === "GET") await sleep(3000); await route.continue().catch(() => {}); };
  await page.route("**/api/v1/docs/*/alternatives", slow);
  await page.route("**/api/v1/docs/*/ghosts", slow);
  await select("tide");
  await page.keyboard.press("Alt+Shift+KeyG");
  await waitFor(async () => (await page.locator(".scriobh-ghost").count()) >= 1, "ghosted");
  await sleep(300);
  ok(!(await page.evaluate(() => window.scriobh.alts.busy)), "the hold is over although the marks and ghosts are still being asked for");
  await page.keyboard.type("Q");
  ok((await text()).includes("Q"), "and a key is taken");
  await page.unroute("**/api/v1/docs/*/alternatives", slow);
  await page.unroute("**/api/v1/docs/*/ghosts", slow);
  // Tab during a hold moves on.
  const slow2 = async (route) => { if (route.request().method() === "PATCH") await sleep(1200); await route.continue().catch(() => {}); };
  await page.route("**/api/v1/docs/*", slow2);
  await page.keyboard.type("R");
  await select("Mara");
  await page.keyboard.press("Alt+Shift+KeyG");
  await sleep(200);
  await page.keyboard.press("Tab");
  ok(!(await page.evaluate(() => document.activeElement.classList.contains("scriobh-prose"))), "focus moved out of the text");
  await sleep(2200);
  await page.unroute("**/api/v1/docs/*", slow2);
  await agree("after the held ghost");
});


await kit.test("round 2, 5: a sentence that holds a character the stored text spells differently is named once the editor's spelling is stored", async () => {
  kit.allowed = [/status of 4\d\d/];
  await fresh("a * b here.\n\nSecond paragraph.\n", []);
  await select("a * b here");
  await page.keyboard.press("Alt+Shift+KeyA");
  const pop = page.locator("#alt-pop");
  await pop.waitFor();
  await pop.locator('input[value="sentence"]').check();
  await pop.locator("textarea").fill("a star here");
  await page.keyboard.press("Enter");
  await pop.waitFor({ state: "detached", timeout: 10000 });
  eq((await list()).active, 1, "the alternative exists");
  const md = bodyOf((await stored()).content), a = (await list()).items[0];
  eq(md.slice(a.start, a.end), "a \\* b here.", "and it is on that sentence as the editor spells it");
  await agree("after it was made");
});


await kit.test("round 2, 6: the hold does not start inside a composition: it is taken once the composition has ended", async () => {
  await fresh(PROSE, []);
  await select("tide");
  await page.evaluate(() => { window.scriobh.write.editor.view.input.composing = true; });
  await page.evaluate(() => { window.scriobh.alts.ghostSelection(); }); // asked for by the menu, say, while an input method is mid-word
  await sleep(400);
  ok(!(await page.evaluate(() => window.scriobh.alts.busy)), "not held while the input method is composing");
  eq(await page.locator(".scriobh-ghost").count(), 0, "and nothing has been done yet");
  await page.evaluate(() => { window.scriobh.write.editor.view.input.composing = false; });
  await waitFor(async () => (await page.locator(".scriobh-ghost").count()) >= 1, "the change after the composition ended", 8000);
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.alts.busy)), "the hold to end", 8000);
  await agree("after the composition");
});


await kit.test("round 2, 3: a Discard leaves the draft this page has written, and the oldest ordinary copy goes before an adopted one", async () => {
  await fresh(PROSE, []);
  const user = await page.evaluate(() => document.querySelector('meta[name="scriobh-user"]').content);
  const mk = (md, t, extra) => Object.assign({ md, title: "T", rev: 1, t }, extra || {});
  const key = (k) => `scriobh:${k}:${user}:${doc}`;
  await page.evaluate(([d, user, held]) => {
    for (const k of Object.keys(localStorage)) if (/^scriobh:(draft|held):/.test(k)) localStorage.removeItem(k);
    localStorage.setItem(`scriobh:held:${user}:${d}`, JSON.stringify(held));
  }, [doc, user, [mk("Held one.\n", 3), mk("Held two.\n", 2)]]);
  await page.reload(); await ready(page, false);
  await page.waitForSelector("#nb-draft");
  await page.locator(".scriobh-prose").click();
  await page.keyboard.type(" Live words.");
  await sleep(1200);
  const live = await page.evaluate((k) => localStorage.getItem(k), key("draft"));
  await page.locator("#nb-draft button", { hasText: "Discard" }).click();
  await sleep(300);
  const after = await page.evaluate(([d, h]) => ({ draft: localStorage.getItem(d), held: JSON.parse(localStorage.getItem(h) || "[]").map((x) => x.md) }), [key("draft"), key("held")]);
  ok(live === null || after.draft !== null || (await stored()).content.includes("Live words."), "the page's own draft was not touched by Discard (or it was saved)");
  ok(!after.held.some((m) => m.includes("Live words")), "and it was not filed as an old copy: " + JSON.stringify(after.held));
  eq(after.held.length, 1, "one old copy is left");
  await settled();
  // More legacy copies than there is room for: what does not fit stays in the old key, and the old key is not removed.
  const legacy = [];
  for (let i = 0; i < 9; i++) legacy.push(mk(`Legacy ${i}.\n`, i));
  await page.evaluate(([d, user, legacy]) => {
    for (const k of Object.keys(localStorage)) if (/^scriobh:(draft|held):/.test(k)) localStorage.removeItem(k);
    localStorage.setItem("scriobh:draft:" + d, JSON.stringify(legacy[0]));
    localStorage.setItem("scriobh:held:" + d, JSON.stringify(legacy.slice(1)));
  }, [doc, user, legacy]);
  await page.reload(); await ready(page, false);
  const o = await page.evaluate(([d, user]) => ({ held: JSON.parse(localStorage.getItem(`scriobh:held:${user}:${d}`) || "[]"), lh: JSON.parse(localStorage.getItem("scriobh:held:" + d) || "[]"), ld: localStorage.getItem("scriobh:draft:" + d) }), [doc, user]);
  eq(o.held.length, 8, "eight copies were taken over");
  eq(o.lh.length + (o.ld ? 1 : 0), 1, "the one that did not fit is still in the old key: " + JSON.stringify([o.lh.length, !!o.ld]));
  // A session of ordinary typing that is cut short adds a copy: an adopted one is not the one that goes.
  await page.evaluate(([d, user]) => {
    const h = JSON.parse(localStorage.getItem(`scriobh:held:${user}:${d}`)); h.push({ md: "Ordinary old.\n", title: "T", rev: 1, t: 0 }); h.splice(h.length - 2, 0, { md: "Ordinary old 2.\n", title: "T", rev: 1, t: 0 });
    localStorage.setItem(`scriobh:held:${user}:${d}`, JSON.stringify(h));
    localStorage.setItem(`scriobh:draft:${user}:${d}`, JSON.stringify({ md: "Cut short.\n", title: "T", rev: 1, t: 9 }));
  }, [doc, user]);
  await page.reload(); await ready(page, false);
  await page.waitForSelector("#nb-draft"); // the page has taken hold of the copies once it offers one
  await page.locator(".scriobh-prose").click();
  await page.keyboard.type("x");
  const final = await page.evaluate(([d, user]) => JSON.parse(localStorage.getItem(`scriobh:held:${user}:${d}`) || "[]").map((x) => [x.md, !!x.legacy]), [doc, user]);
  ok(final.filter(([, l]) => l).length === 8, "every adopted copy is still there: " + JSON.stringify(final));
  ok(final.some(([m]) => m.includes("Cut short")), "and the cut-short draft was filed");
  await settled();
  await page.evaluate(() => { for (const k of Object.keys(localStorage)) if (/^scriobh:(draft|held):/.test(k)) localStorage.removeItem(k); });
});

await kit.test("round 2, 5: a chapter with Windows line endings is stored one way and named, cycled and put into at the right place", async () => {
  await fresh("First line of it.\r\nSecond line with target here.\r\n\r\nThird paragraph.\r\n", [{ scope: "word", quote: "target", before: "with ", after: " here", variants: ["goal", "aim"] }]);
  const md = (await stored()).content;
  ok(!md.includes("\r"), "the server stores line endings as LF: " + JSON.stringify(md.slice(0, 60)));
  await caretIn("target", 3);
  await page.keyboard.press("Alt+Shift+KeyN");
  await waitFor(async () => (await text()).includes("goal here"), "cycled after the line break: " + (await text()));
  // The server keeps the soft line break the editor spells as a space, until the editor's spelling is stored (below).
  await waitFor(async () => (await stored()).content.includes("with goal here"), "the server has the new wording", 5000);
  await api(main, "POST", `/api/v1/projects/${book.id}/stash`, { title: "X", kind: "paragraph", content: "XX" });
  await caretIn("Third paragraph", 3);
  await page.evaluate(() => window.scriobh.inspector.open("stash", false));
  const card = page.locator("#ipanel-stash .ap-card").first();
  await card.waitFor();
  await card.getByRole("button", { name: /Put .* in the chapter/ }).click();
  const form = page.locator("#ipanel-stash form.ap-put");
  await form.locator("select[name=where]").selectOption("cursor");
  await form.locator("select[name=placement]").selectOption("inline");
  await form.locator("select[name=mode]").selectOption("copy");
  await form.getByRole("button", { name: "Put it in the chapter" }).click();
  await waitFor(async () => (await text()).includes("ThiXXrd paragraph"), "exactly at the cursor after the line breaks: " + (await text()));
  await agree("after the put-back");
});

await kit.finish("e2e-alts2");
