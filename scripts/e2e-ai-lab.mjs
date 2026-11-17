#!/usr/bin/env node
// The Lab in the writing view. With AI off (the default) the local checks work, draw marks in the text with a line per kind, and
// list them; accepting one writes through the accept op as one history entry, dismissing one keeps it dismissed, accepting a
// chosen set is previewed as one diff and applied as one change; no AI control and no AI script exists and no provider is asked.
// With AI on the model checks go through the preview (Cancel sends nothing) and come back as marks, never as rewrites. At 390 px
// the tab fits and its controls are touch targets.
//
//   node scripts/e2e-ai-lab.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startFake } from "./fake-ai.mjs";

const kit = await createKit("e2e-ai-lab", { env: { SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000" } });
kit.deadline(480000); // a stalled step fails the script instead of hanging it
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor } = kit;
const base = () => kit.base();
const fake = await startFake();
const wren = await kit.signedIn();
const ada = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "admin@dev.scriobh");
const page = wren.page;
kit.main = page;
let book, doc, pid;
const LONG = "The harbour master, who had kept the same ledger since the winter the old pier collapsed and who trusted nothing that was not written in his own hand, walked down to the water and looked at the boats and counted them again and then counted them a third time because the number would not come out the way the ledger said it should and nobody else was there to ask.";
const TEXT = "The harbour was  very quiet.  Mara, perhaps, counted the boats twice.\n\n" + LONG + "\n\nThe tide turned .\n";
const text = () => page.evaluate(() => { const d = window.scriobh.write.editor.view.state.doc; return d.textBetween(0, d.content.size, "\n").replace(/<!-- scriobh:alts[\s\S]*$/, "").trim(); });
const open = async (pg = page) => { await pg.goto(`${base()}/p/${book.slug}/d/${doc}`); await pg.waitForSelector(".scriobh-prose"); await waitFor(() => pg.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 10000); };
const lab = async (pg = page) => { await pg.evaluate(() => window.scriobh.inspector.open("lab", true)); await pg.locator("#ipanel-lab .lab").waitFor(); };
const stored = async () => (await api(wren.ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
const accepted = async () => JSON.stringify((await api(wren.ctx, "GET", `/api/v1/docs/${doc}/revisions`)).json).split("ai-accepted").length - 1;
const marks = () => page.locator("#ipanel-lab .ai-rv");

await kit.test("setup: a chapter with things the local checks find; AI is off", async () => {
  book = (await api(wren.ctx, "POST", "/api/v1/projects", { title: "Lab", kind: "novel" })).json.item;
  doc = (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = (await api(wren.ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(wren.ctx, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "Harbour", content: TEXT });
  await open();
  eq(await page.evaluate(() => document.getElementById("write").dataset.ai), "off", "AI is off by default");
});

await kit.test("AI off: no AI control, no AI tab, no AI script; the Lab tab is there", async () => {
  eq(await page.locator("#itab-ai").count(), 0, "no AI tab");
  eq(await page.locator('[data-cmd="ai"]').count(), 0, "no AI toolbar button");
  eq(await page.evaluate(() => typeof window.scriobh.aiLoad + typeof window.scriobh.ai), "undefinedundefined", "no AI code is loaded");
  eq(await page.locator("#itab-lab").count(), 1, "the Lab tab");
  ok(/AI off/.test(await page.locator(".ai-chip").innerText()), "the header says AI is off");
  eq(fake.count(), 0, "no provider was asked");
});

await kit.test("the local check marks the text and lists the marks; nothing is rewritten and no AI is involved", async () => {
  const before = await text();
  await lab();
  await page.getByRole("button", { name: "Check this chapter" }).click();
  await waitFor(async () => (await marks().count()) >= 3, "the marks to be listed", 10000);
  const kinds = await marks().evaluateAll((l) => l.map((x) => x.dataset.kind));
  ok(kinds.includes("punctuation_fix") && kinds.includes("hedge_words") && kinds.includes("long_sentence"), "kinds found: " + kinds.join(","));
  await waitFor(async () => (await page.locator(".scriobh-prose .lab-mark").count()) >= 3, "marks to be drawn in the text");
  const lines = await page.locator(".scriobh-prose .lab-mark").evaluateAll((l) => [...new Set(l.map((e) => getComputedStyle(e).textDecorationStyle))]);
  ok(lines.length >= 2, "kinds differ by line style, not colour alone: " + lines.join(","));
  const pseudo = await page.locator(".scriobh-prose .lab-mark").evaluateAll((l) => l.map((e) => getComputedStyle(e, "::after").content));
  ok(pseudo.every((c) => c === "none" || c === "normal"), "a mark adds nothing to the text, no letter and no width: " + pseudo.join(","));
  const shapes = await page.evaluate(() => ["long_sentence", "hedge_words", "punctuation_fix", "convoluted_sentence", "tone_mismatch", "trim_filler", "trim_adverbs", "trim_repetition", "trim_dialogue_tags"].map((k) => {
    const e = document.createElement("span"); e.className = "lab-mark lab-" + k; document.querySelector(".scriobh-prose").appendChild(e); const cs = getComputedStyle(e); const s = cs.textDecorationStyle + " " + cs.textDecorationThickness; e.remove(); return s;
  }));
  eq(new Set(shapes).size, 9, "nine kinds, nine different lines (shape and weight): " + shapes.join(" | "));
  ok((await page.locator("#ipanel-lab [role=status]").count()) <= 2, "the list has one live region, not one for each mark");
  const sum = await page.locator("#ipanel-lab .ai-result").first().innerText();
  ok(!/\b1 \w+ words\b|\b1 punctuation fixes\b|\b1 hedge words\b/.test(sum), "the summary is pluralised properly: " + sum);
  const gapText = await page.locator('#ipanel-lab .ai-rv[data-kind="punctuation_fix"] .ai-quote').first().innerText();
  ok(!/^[\s\u201C\u201D"]*$/.test(gapText), "a whitespace-only passage is described in words, not shown as an empty quote: " + gapText);
  ok(await page.locator("#ipanel-lab .insp-tag").evaluateAll((l) => l.every((e) => e.getBoundingClientRect().width < 200)), "the kind labels are chips, not bars");
  eq(await text(), before, "the text is unchanged");
  eq((await stored()).content, TEXT, "and so is the stored text");
  eq(fake.count(), 0, "no provider was asked");
  ok(await page.locator('#ipanel-lab :text("Mark tangled sentences")').count() === 0, "the model checks are not offered with AI off");
});

await kit.test("Show puts the selection on the marked words, and the next/previous mark keys step through them", async () => {
  const li = page.locator('#ipanel-lab .ai-rv[data-kind="hedge_words"]').first();
  await li.getByRole("button", { name: "Show" }).click();
  const sel = await page.evaluate(() => { const v = window.scriobh.write.editor.view; const s = v.state.selection; return v.state.doc.textBetween(s.from, s.to); });
  ok(sel.length > 0, "something is selected: " + sel);
  await page.evaluate(() => window.scriobh.lab.step(1));
  const sel2 = await page.evaluate(() => { const v = window.scriobh.write.editor.view; const s = v.state.selection; return v.state.doc.textBetween(s.from, s.to); });
  ok(sel2 !== sel, "the next mark is a different one: " + sel2);
});

await kit.test("Show and next/previous select the right words after text is typed in front of the marks", async () => {
  const li = page.locator('#ipanel-lab .ai-rv[data-kind="hedge_words"]').first();
  const quote = (await li.locator("q").first().innerText()).trim();
  // Words go in at the very start, and Show is pressed at once, before the marks have been asked for again.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.dispatch(v.state.tr.insertText("Some new words in front of everything. ", 1)); });
  await li.getByRole("button", { name: "Show" }).click();
  const sel = await page.evaluate(() => { const v = window.scriobh.write.editor.view; const s = v.state.selection; return v.state.doc.textBetween(s.from, s.to); });
  eq(sel.trim(), quote, "Show selects the marked words, not words shifted by the new text");
  await page.evaluate(() => window.scriobh.write.editor.view.dispatch(window.scriobh.write.editor.view.state.tr.setSelection(window.scriobh.write.editor.view.state.selection.constructor.atStart(window.scriobh.write.editor.view.state.doc))));
  await page.evaluate(() => window.scriobh.lab.step(1));
  const nx = await page.evaluate(() => { const v = window.scriobh.write.editor.view; const s = v.state.selection; return v.state.doc.textBetween(s.from, s.to); });
  ok(nx.length > 0 && !/^Some new/.test(nx), "the next mark is a mark, not the new words: " + JSON.stringify(nx));
  // Put the text back as it was, so the marks and the stored text agree for what follows.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.dispatch(v.state.tr.delete(1, 1 + "Some new words in front of everything. ".length)); });
  await waitFor(async () => (await stored()).content === TEXT, "the text to be saved as it was", 15000);
});

await kit.test("dismissing a mark removes it, and it stays dismissed when the check is run again", async () => {
  const li = page.locator('#ipanel-lab .ai-rv[data-kind="hedge_words"]').first();
  const quote = await li.locator("q").first().innerText();
  await li.getByRole("button", { name: "Dismiss" }).click();
  await waitFor(async () => (await page.locator('#ipanel-lab .ai-rv[data-kind="hedge_words"]').count()) === 0, "the mark to go");
  await page.getByRole("button", { name: "Check this chapter" }).click();
  await waitFor(async () => (await page.locator("#ipanel-lab .lab-list").innerText()).length > 0, "the list");
  await page.waitForTimeout(600);
  ok(!(await page.locator("#ipanel-lab .lab-list").innerText()).includes(quote) || (await page.locator('#ipanel-lab .ai-rv[data-kind="hedge_words"]').count()) === 0, "the dismissed mark did not come back");
});

await kit.test("accepting one mark with a fix writes through the accept op, as one history entry that earns no words", async () => {
  const li = page.locator('#ipanel-lab .ai-rv[data-kind="punctuation_fix"]').first();
  const quote = await li.locator(".ai-quote").first().innerText();
  const n = await accepted();
  await li.getByRole("button", { name: "Accept" }).click();
  await waitFor(async () => (await stored()).content !== TEXT, "the stored text to change", 10000);
  const revs = JSON.stringify((await api(wren.ctx, "GET", `/api/v1/docs/${doc}/revisions`)).json);
  ok(revs.includes("ai-accepted"), "a revision with the reason ai-accepted: " + quote);
  eq(await accepted(), n + 1, "exactly one new entry");
  eq(fake.count(), 0, "still no provider asked");
});

await kit.test("accept all shows the whole change first; Cancel changes nothing; Apply is one change", async () => {
  const withFix = await page.locator('#ipanel-lab .ai-rv:has(button:text-is("Accept"))').count();
  ok(withFix >= 1, "marks with a fix: " + withFix);
  const before = (await stored()).content;
  await page.locator("#ipanel-lab .lab-all").click();
  const dlg = page.locator("dialog.ai-dlg");
  await dlg.waitFor();
  ok(/one change/i.test(await dlg.innerText()) && (await dlg.locator(".ai-diff").count()) === 1, "the diff of the whole change");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  await dlg.waitFor({ state: "detached" });
  eq((await stored()).content, before, "Cancel changed nothing");
  const n = await accepted();
  await page.locator("#ipanel-lab .lab-all").click();
  await dlg.waitFor();
  await dlg.getByRole("button", { name: "Apply as one change" }).click();
  await dlg.waitFor({ state: "detached" });
  await waitFor(async () => (await stored()).content !== before, "the text to change", 10000);
  eq(await accepted(), n + 1, "one history entry for the set");
  eq(fake.count(), 0, "no provider asked");
});

await kit.test("marks stay on their words after a save when a paragraph ends in a space; showing and hiding them moves nothing", async () => {
  const body = "Stood by the harbour and perhaps looked.  Gulls wheeled.\n\nThe tide perhaps turned here.  More gulls.\n\nA third paragraph, perhaps one more.  End.\n";
  const d2 = (await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Spaces", kind: "chapter", content: body })).json.item;
  await page.goto(`${base()}/p/${book.slug}/d/${d2.id}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 10000);
  await lab();
  await page.getByRole("button", { name: "Check this chapter" }).click();
  await waitFor(async () => (await page.locator(".scriobh-prose .lab-hedge_words").count()) === 3, "three hedge marks", 10000);
  const words = () => page.evaluate(() => [...document.querySelectorAll(".scriobh-prose .lab-hedge_words")].map((e) => e.textContent.toLowerCase()));
  // Words and a trailing space go in at the very start, then Enter: the first paragraph now ends in a space.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, 1))); v.focus(); });
  await page.keyboard.type("Opening words. ", { delay: 15 });
  await page.keyboard.press("Enter");
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "saved", 15000);
  await page.waitForTimeout(3000); // the marks are asked for again after a save
  eq((await words()).join(","), "perhaps,perhaps,perhaps", "after the save and the refresh every mark is on its word");
  const li = page.locator('#ipanel-lab .ai-rv[data-kind="hedge_words"]').nth(1);
  await li.getByRole("button", { name: "Show" }).click();
  eq((await page.evaluate(() => { const v = window.scriobh.write.editor.view; const s = v.state.selection; return v.state.doc.textBetween(s.from, s.to); })).toLowerCase(), "perhaps", "Show selects exactly the word");
  const selected = () => page.evaluate(() => { const v = window.scriobh.write.editor.view; const s = v.state.selection; return v.state.doc.textBetween(s.from, s.to); });
  await page.evaluate(() => window.scriobh.lab.step(1));
  eq(await selected(), "  ", "next selects exactly the next mark (the double space after it)");
  await page.evaluate(() => window.scriobh.lab.step(1));
  eq((await selected()).toLowerCase(), "perhaps", "and the one after it is the third word");
  await page.evaluate(() => window.scriobh.lab.step(-1));
  eq(await selected(), "  ", "previous goes back one");
  // No reflow: where the last line of the first paragraphs ends is the same with the marks showing and hidden.
  const where = () => page.evaluate(() => [...document.querySelectorAll(".scriobh-prose p")].map((p) => { const r = document.createRange(); r.selectNodeContents(p); r.collapse(false); const b = r.getBoundingClientRect(); return Math.round(b.left) + "," + Math.round(b.top); }).join(" "));
  const withMarks = await where();
  await page.locator("#lab-vis").uncheck();
  await page.waitForTimeout(200);
  eq(await where(), withMarks, "hiding the marks moves nothing");
  await page.locator("#lab-vis").check();
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 10000);
});

await kit.test("setup: a provider that stays local and AI turned on for the project", async () => {
  const pv = await api(ada.ctx, "POST", "/api/v1/ai/providers", { kind: "openai_compatible", name: "Fake", base_url: fake.base, local: true, default_model: "fake-model", scope: "instance" });
  ok(pv.status < 300, "provider: " + JSON.stringify(pv.json));
  pid = pv.json.item.id;
  const cur = await stored();
  await api(wren.ctx, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, content: "The harbour was very quiet. Mara counted the boats twice.\n" });
  const r = await api(wren.ctx, "PUT", `/api/v1/projects/${book.id}/ai/policy`, { mode: "ask", providers: [pid], actions: ["selection", "alternatives", "chat", "critique", "summaries", "continuity", "extraction", "lab"], redaction: "off" });
  ok(r.status < 300, "policy: " + JSON.stringify(r.json));
  await open();
  eq(await page.evaluate(() => document.getElementById("write").dataset.ai), "on", "AI is on");
});

await kit.test("model checks go through the preview: Cancel sends nothing, Send brings back marks and no rewrite", async () => {
  await lab();
  await page.getByRole("button", { name: "Mark words to trim" }).click();
  const dlg = page.locator("dialog.ai-dlg:not(.ai-menu)");
  await dlg.waitFor();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  await dlg.waitFor({ state: "detached" });
  eq(fake.count(), 0, "Cancel sent nothing");
  const before = (await stored()).content;
  await page.getByRole("button", { name: "Mark words to trim" }).click();
  await dlg.waitFor();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await waitFor(async () => (await page.locator('#ipanel-lab .ai-rv[data-kind^="trim_"]').count()) >= 1, "a trim mark to be listed", 15000);
  eq(fake.count(), 1, "one request went out");
  eq((await stored()).content, before, "nothing was rewritten");
  ok(await page.locator('#ipanel-lab .ai-rv[data-kind^="trim_"]').first().innerText().then((t) => /Model/.test(t)), "it says the model made it");
});

await kit.test("accept all above 300 marks: the check says it stops at 300, the dialog says one change takes at most 300, and the rest stay open", async () => {
  const body = Array.from({ length: 340 }, (_, i) => "Word" + i + "  here.").join(" ") + " It was very quiet.\n";
  const big = (await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Many", kind: "chapter", content: body })).json.item;
  await page.goto(`${base()}/p/${book.slug}/d/${big.id}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 10000);
  await lab();
  await page.getByRole("button", { name: "Check this chapter" }).click();
  await waitFor(async () => /stops at 300 marks/.test(await page.locator("#ipanel-lab .ai-result").first().innerText()), "the check, with its note", 20000);
  // One mark from a model check takes the list past 300.
  await page.getByRole("button", { name: "Mark words to trim" }).click();
  const pv = page.locator("dialog.ai-dlg:not(.ai-menu)");
  await pv.waitFor();
  await waitFor(async () => (await pv.locator("pre.ai-pay").count()) > 0, "the payload");
  await pv.getByRole("button", { name: /^Send/ }).click();
  await waitFor(async () => (await page.locator('#ipanel-lab .ai-rv[data-kind^="trim_"]').count()) >= 1, "the trim mark", 20000);
  await waitFor(async () => /Accept all 301/.test(await page.locator("#ipanel-lab .lab-all").innerText()), "Accept all over 300", 20000);
  await page.locator("#ipanel-lab .lab-all").click();
  const dlg = page.locator("dialog.ai-dlg:not(.ai-menu)");
  await dlg.waitFor();
  ok(/at most 300 marks/.test(await dlg.innerText()), "the dialog says one change takes at most 300: " + (await dlg.innerText()).slice(0, 200));
  await dlg.getByRole("button", { name: "Apply as one change" }).click();
  await dlg.waitFor({ state: "detached" });
  await waitFor(async () => { const r = await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/ai/lab/marks?document_id=${big.id}&status=open`); return r.status === 200 && r.json.marks.length > 0 && r.json.marks.length < 301; }, "the rest to stay open", 20000);
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
});

await kit.test("at 390 px the Lab tab fits and its controls are touch targets", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 });
  await ctx.addCookies(await wren.ctx.cookies());
  const pg = await ctx.newPage();
  kit.watch(pg, "phone");
  await open(pg);
  await lab(pg);
  await pg.getByRole("button", { name: "Check this chapter" }).tap();
  await pg.waitForTimeout(800);
  const fits = await pg.evaluate(() => { const r = document.querySelector("#ipanel-lab").getBoundingClientRect(); return r.left >= 0 && r.right <= window.innerWidth + 1 && document.documentElement.scrollWidth <= window.innerWidth + 1; });
  ok(fits, "the tab fits 390 px without a sideways scroll");
  const small = await pg.locator("#ipanel-lab button, #ipanel-lab select, #ipanel-lab input:not([type=checkbox])").evaluateAll((els) => els.filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => (e.textContent || e.id).trim().slice(0, 30)));
  eq(small.length, 0, "controls are 44 px tall: " + small.join(", "));
  await ctx.close();
});

await fake.close();
await kit.finish("e2e-ai-lab");
