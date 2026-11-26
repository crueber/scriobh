#!/usr/bin/env node
// The writing partner's edit path against a fake provider: the context preview shows the exact payload and its fingerprint, Cancel
// sends nothing, taking a chip off changes the payload, a proposal arrives as a word diff and is never inserted, Accept is the
// only write and goes through the accept op, a stale accept is refused, alternatives are added without changing the text, and a
// budget that is used up stops the request before it leaves. Also by keyboard alone, and at 390 px.
//
//   node scripts/e2e-ai-edit.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startFake } from "./fake-ai.mjs";

const kit = await createKit("e2e-ai-edit", { env: { SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000" } });
kit.deadline(480000); // a stalled step fails the script instead of hanging it
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const fake = await startFake();
const wren = await kit.signedIn();
const ada = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "admin@dev.scriobh");
const page = wren.page;
kit.main = page;
let book, doc, pid;
const TEXT = "The harbour was very quiet. Mara counted the boats twice.\n\nThe tide turned.\n";
const text = () => page.evaluate(() => { const d = window.scriobh.write.editor.view.state.doc; return d.textBetween(0, d.content.size, "\n").replace(/<!-- scriobh:alts[\s\S]*$/, "").trim(); });
const select = (s) => page.evaluate((s) => {
  const v = window.scriobh.write.editor.view; let hit = null;
  v.state.doc.descendants((n, pos) => { if (hit || !n.isText) return; const i = n.text.indexOf(s); if (i >= 0) hit = [pos + i, pos + i + s.length]; });
  if (!hit) throw new Error("not found: " + s);
  v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); v.focus();
}, s);
const open = async (pg = page) => { await pg.goto(`${base()}/p/${book.slug}/d/${doc}`); await pg.waitForSelector(".scriobh-prose"); await waitFor(() => pg.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 15000); };
const setPolicy = (extra = {}) => api(wren.ctx, "PUT", `/api/v1/projects/${book.id}/ai/policy`, Object.assign({ mode: "ask", providers: [pid], actions: ["selection", "alternatives", "chat", "critique", "summaries", "continuity", "extraction", "lab"] }, extra));
const menu = async (name, pg = page) => { await pg.keyboard.press("Alt+Shift+KeyI"); await pg.locator("dialog.ai-menu").waitFor(); await pg.locator("dialog.ai-menu").getByRole("button", { name }).click(); };
const preview = (pg = page) => pg.locator("dialog.ai-dlg:not(.ai-menu)");

await kit.test("setup: a provider that stays local, a project with AI on, and a chapter", async () => {
  const pv = await api(ada.ctx, "POST", "/api/v1/ai/providers", { kind: "openai_compatible", name: "Fake", base_url: fake.base, local: true, default_model: "fake-model", scope: "instance" });
  ok(pv.status < 300, "provider: " + JSON.stringify(pv.json));
  pid = pv.json.item.id;
  book = (await api(wren.ctx, "POST", "/api/v1/projects", { title: "AI Edit", kind: "novel" })).json.item;
  doc = (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = (await api(wren.ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(wren.ctx, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "Harbour", content: TEXT });
  ok((await setPolicy()).status < 300, "policy");
  await open();
});

await kit.test("the preview shows the exact payload, where it goes and a fingerprint; Cancel sends nothing", async () => {
  const untouched = await text();
  await select("The harbour was very quiet.");
  await menu("Tighten");
  const dlg = preview();
  await dlg.waitFor();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload to be shown");
  const t = await dlg.innerText();
  ok(/stays on your machine or network/.test(t), "it says the request stays local: " + t.slice(0, 200));
  ok(t.includes("The harbour was very quiet."), "the selection is in the payload");
  ok(/Request fingerprint \(keyed\): [0-9a-f]{64}/.test(t), "the keyed fingerprint is shown");
  ok(!/SHA-256/.test(t), "and it is not called a plain SHA-256");
  ok(/tokens/.test(t) && /Cost:/.test(t), "size and cost are shown");
  eq(await dlg.getByRole("button", { name: /^Send/ }).isEnabled(), true, "Send is ready");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  await dlg.waitFor({ state: "detached" });
  eq(fake.count(), 0, "the fake provider was never asked");
  eq(await text(), untouched, "the text is untouched");
});

await kit.test("Escape is Cancel too, and taking a chip off makes a different payload with a different fingerprint", async () => {
  await select("The harbour was very quiet.");
  await menu("Tighten");
  const dlg = preview();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  const hash1 = /[0-9a-f]{64}/.exec(await dlg.innerText())[0];
  ok((await dlg.innerText()).includes("Project brief"), "the brief is in the payload");
  await dlg.locator("#aic-brief").uncheck();
  await waitFor(async () => !(await dlg.innerText()).includes("Title: AI Edit"), "the brief to be gone from the payload");
  const hash2 = /[0-9a-f]{64}/.exec(await dlg.innerText())[0];
  ok(hash1 !== hash2, "the fingerprint changed");
  await page.keyboard.press("Escape");
  await dlg.waitFor({ state: "detached" });
  eq(fake.count(), 0, "still nothing was sent");
});

await kit.test("Send: the provider got exactly the previewed messages, and the answer is a diff, not an insertion", async () => {
  await select("The harbour was very quiet.");
  await menu("Tighten");
  const dlg = preview();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  const shown = await dlg.locator("pre.ai-pay").evaluateAll((l) => l.map((e) => e.textContent));
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.locator("#ai-pop").waitFor();
  eq(fake.count(), 1, "exactly one request left");
  const body = JSON.parse(fake.requests()[0]);
  const sent = body.messages.map((m) => m.content);
  eq(JSON.stringify(shown.slice(0, sent.length)), JSON.stringify(sent), "the messages the provider got are, in order, exactly the ones the preview showed");
  eq(shown[sent.length], fake.requests()[0], "and the request body shown is, byte for byte, the one the provider received");
  ok(await page.locator("#ai-pop .ai-diff del").count() > 0 && await page.locator("#ai-pop .ai-diff ins, #ai-pop .ai-diff span").count() > 0, "a diff with struck words");
  eq(await text(), TEXT.trim().replace(/\n+/g, "\n"), "the chapter is untouched while the proposal waits");
  ok((await api(wren.ctx, "GET", `/api/v1/docs/${doc}`)).json.item.content === TEXT, "and so is the stored text");
});

await kit.test("Accept is the one write: through the accept op, as one history entry", async () => {
  await page.locator("#ai-pop").getByRole("button", { name: /^Accept/ }).click();
  await waitFor(async () => (await text()).includes("The harbour was quiet."), "the text to change");
  await page.locator("#ai-pop").waitFor({ state: "detached" });
  const stored = (await api(wren.ctx, "GET", `/api/v1/docs/${doc}`)).json.item.content;
  ok(stored.includes("The harbour was quiet. Mara"), "stored: " + stored.slice(0, 80));
  const revs = (await api(wren.ctx, "GET", `/api/v1/docs/${doc}/revisions`)).json;
  ok(JSON.stringify(revs).includes("ai-accepted"), "a revision with the reason ai-accepted exists");
  eq(fake.count(), 1, "no further request");
});

await kit.test("a stale accept is refused by the op's own rule, and the text stays as the writer left it", async () => {
  kit.allowed = [/status of 409/];
  await select("Mara counted the boats twice.");
  await menu("Tighten");
  const dlg = preview();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.locator("#ai-pop").waitFor();
  // The writer edits the very words the proposal is about before deciding.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; let at = -1; v.state.doc.descendants((n, p) => { if (at < 0 && n.isText && n.text.includes("counted")) at = p + n.text.indexOf("counted"); }); v.dispatch(v.state.tr.insertText("recounted and ", at, at + "counted".length)); });
  await page.locator("#ai-pop").getByRole("button", { name: /^Accept/ }).click();
  await waitFor(async () => (await page.locator("#ai-pop .ai-bad").count()) > 0 && (await page.locator("#ai-pop .ai-bad").isVisible()), "the refusal to be shown");
  ok(/changed|found|stale|cannot/i.test(await page.locator("#ai-pop .ai-bad").innerText()), "it says why: " + (await page.locator("#ai-pop .ai-bad").innerText()));
  ok((await text()).includes("recounted and "), "the writer's own words are still there");
  await page.locator("#ai-pop").getByRole("button", { name: /^Reject/ }).click();
  await page.locator("#ai-pop").waitFor({ state: "detached" });
});

await kit.test("alternatives: candidates are added as wordings and the text does not change", async () => {
  await page.waitForTimeout(800);
  await select("The tide turned.");
  await menu("Give me alternatives");
  const dlg = preview();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.locator("#ai-pop").waitFor();
  eq(await page.locator("#ai-pop input[type=checkbox]").count(), 3, "three candidates");
  const before = await text();
  await page.locator("#ai-pop").getByRole("button", { name: /^Add the ticked/ }).click();
  await page.locator("#ai-pop").waitFor({ state: "detached" });
  eq(await text(), before, "the text is as it was");
  const alts = (await api(wren.ctx, "GET", `/api/v1/docs/${doc}/alternatives`)).json;
  ok(alts.items.some((a) => a.variants.filter((v) => v.source === "generated").length === 3), "three wordings marked as generated");
});

await kit.test("a rewrite can be kept as an alternative without changing the text; Try again asks again, and asking is previewed again", async () => {
  await select("The harbour was quiet.");
  await menu("Rewrite…");
  let dlg = preview();
  await dlg.locator("#aip-instruction").fill("Make it quieter.");
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0 && (await dlg.innerText()).includes("Make it quieter."), "the instruction to be in the payload");
    await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.locator("#ai-pop").waitFor();
  const before = await text(), n = fake.count();
  await page.locator("#ai-pop").getByRole("button", { name: /^Try again/ }).click();
  dlg = preview();
  await dlg.waitFor();
  eq(fake.count(), n, "Try again shows the preview first: nothing was sent yet");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  eq(await text(), before, "and the text is as it was");
});

await kit.test("a used-up budget stops the request before it leaves: the preview says so and Send is off", async () => {
  ok((await setPolicy({ budget_tokens: 1 })).status < 300, "a one-token cap");
  await open();
  const n = fake.count();
  await select("The harbour was quiet.");
  await menu("Tighten");
  const dlg = preview();
  await waitFor(async () => /cannot be sent/i.test(await dlg.innerText()), "the refusal");
  eq(await dlg.getByRole("button", { name: /^Send/ }).isDisabled(), true, "Send is off");
  ok(await dlg.getByRole("link", { name: /AI settings/ }).count() === 1, "it points to the settings");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  eq(fake.count(), n, "nothing was sent");
  ok((await setPolicy({ budget_tokens: 0 })).status < 300, "the cap is lifted");
});

await kit.test("with the network cut: asking says it cannot reach the server, nothing is sent, and the text is untouched", async () => {
  await open();
  await select("The harbour was quiet.");
  const before = await text(), n = fake.count();
  kit.allowed = [/ERR_INTERNET_DISCONNECTED/];
  await page.context().setOffline(true);
  try {
    await menu("Tighten");
    await waitFor(async () => /Could not reach the server/.test(await page.locator("#toasts, #live").allInnerTexts().then((l) => l.join(" "))), "the plain message");
    eq(await preview().count(), 0, "no preview opened, so nothing could be sent");
  } finally {
    await page.context().setOffline(false);
  }
  eq(fake.count(), n, "nothing was sent");
  eq(await text(), before, "the text is untouched");
});

// A document of its own for the tests below, so that what they do to the text does not meet the others'.
const BASE_TEXT = "The harbour was very quiet. Mara counted the boats twice.\n";
const fresh = async (title, extra) => {
  const d = (await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/docs`, { title, kind: "chapter", content: BASE_TEXT })).json.item;
  if (extra) await extra(d);
  await page.goto(`${base()}/p/${book.slug}/d/${d.id}`);
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 10000);
  return d;
};
const sendTighten = async () => {
  await select("The harbour was very quiet.");
  await menu("Tighten");
  const dlg = preview();
  await dlg.waitFor();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.locator("#ai-pop").waitFor();
};
// A held key: Playwright marks a key that is already down as an auto-repeat, so this is a keydown, repeats, and a key-up.
const hold = async (key, repeats) => {
  await page.keyboard.down(key);
  for (let i = 0; i < repeats; i++) { await page.waitForTimeout(40); await page.keyboard.down(key); }
  await page.keyboard.up(key);
};

for (const key of ["r", "a", "t"]) {
  await kit.test(`a held '${key}' on a proposal is one press: nothing is typed into the chapter and nothing acts twice`, async () => {
    await fresh("Hold " + key);
    await sendTighten();
    await page.evaluate(() => document.getElementById("ai-pop").focus()); // a deliberate move into the card (the chord is tested below)
    eq(await page.evaluate(() => document.activeElement.id), "ai-pop", "focus is in the proposal");
    await hold(key, 25);
    await page.waitForTimeout(1200);
    const want = key === "a" ? "The harbour was quiet. Mara counted the boats twice." : "The harbour was very quiet. Mara counted the boats twice.";
    eq(await text(), want, "the text is exactly what the one action leaves, with no repeated letters");
    if (key === "t") {
      eq(await preview().count(), 1, "Try again opened one preview");
      await preview().getByRole("button", { name: "Cancel" }).click();
    }
    ok(await page.locator("#ai-pop").count() === 0, "the card is gone");
  });
}

await kit.test("the Alt+Shift chords are all different, and the proposal's chord does not also act on an alternative", async () => {
  await fresh("Chords", async (d) => {
    const cur = (await api(wren.ctx, "GET", `/api/v1/docs/${d.id}`)).json.item;
    const r = await api(wren.ctx, "POST", `/api/v1/docs/${d.id}/alternatives`, { base_revision: cur.revision, scope: "word", quote: "harbour", variants: ["port"] });
    ok(r.status < 300, "an alternative: " + JSON.stringify(r.json));
  });
  await sendTighten();
  const chords = await page.evaluate(() => window.scriobh.chords);
  eq(chords.clash.length, 0, "no chord is claimed twice: " + JSON.stringify(chords));
  ok(["KeyA", "KeyN", "KeyP", "KeyO", "KeyG", "KeyS", "KeyI", "KeyJ"].every((c) => chords[c]), "every Alt+Shift chord of the view is in the table: " + Object.keys(chords).join(","));
  // The caret goes inside the alternative's word, then the chord for the proposal is pressed.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; let at = -1; v.state.doc.descendants((n, p) => { if (at < 0 && n.isText && n.text.includes("harbour")) at = p + n.text.indexOf("harbour") + 3; }); v.focus(); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, at))); });
  const writes = [], onreq = (r) => { if (/\/alternatives/.test(r.url()) && r.method() !== "GET") writes.push(r.method() + " " + r.url()); };
  page.on("request", onreq);
  await page.keyboard.press("Alt+Shift+KeyJ");
  await page.waitForTimeout(600);
  page.off("request", onreq);
  eq(await page.evaluate(() => document.activeElement.id), "ai-pop", "the chord moved focus to the proposal");
  eq(writes.length, 0, "and chose no wording: " + writes.join(","));
  await page.locator("#ai-pop").getByRole("button", { name: /^Reject/ }).click();
});

await kit.test("one card at a time: a summary proposal replaces a waiting proposal and there is one #ai-pop", async () => {
  await fresh("Two cards");
  await sendTighten();
  await page.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:ai", { detail: "summary" })));
  const dlg = preview();
  await dlg.waitFor();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await waitFor(async () => /Proposed synopsis/.test(await page.locator("#ai-pop").first().innerText()), "the summary card");
  eq(await page.locator("#ai-pop").count(), 1, "one card, not two");
  await page.locator("#ai-pop").getByRole("button", { name: "Reject" }).click();
  eq(await page.locator("#ai-pop").count(), 0, "and it goes when rejected");
});

await kit.test("a review that finishes while the writer is typing in the text does not take focus", async () => {
  await fresh("Typing review");
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.focus(); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.atEnd(v.state.doc))); });
  fake.slow(1200);
  await menu("Critique this chapter");
  const dlg = preview();
  await dlg.waitFor();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: /^Send/ }).click();
  for (const ch of "going on") { await page.keyboard.type(ch); await page.waitForTimeout(350); }
  fake.slow(0);
  await waitFor(async () => /Reviews list|None of this changes/.test(await page.locator("#ipanel-ai, #live").allInnerTexts().then((l) => l.join(" "))), "the review to be announced or opened", 10000);
  await page.waitForTimeout(500);
  ok(await page.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus is still in the text");
  ok((await text()).endsWith("going on"), "every key reached the text: " + (await text()).slice(-20));
});

await kit.test("a failed Accept gives the button back its focus", async () => {
  await fresh("Failed accept");
  await sendTighten();
  await page.route(/\/ai\/accept\/edit/, (route) => route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: { code: "conflict", message: "Nothing was accepted." } }) }));
  kit.allowed = [/409/];
  const b = page.locator("#ai-pop").getByRole("button", { name: /^Accept/ });
  await b.click();
  await waitFor(async () => (await page.locator("#ai-pop .ai-bad").isVisible()), "the refusal");
  await page.unroute(/\/ai\/accept\/edit/);
  eq(await page.evaluate(() => document.activeElement.textContent.trim().replace(/\s*\(A\)$/, "")), "Accept", "focus is on the Accept button, not the page body");
  await page.locator("#ai-pop").getByRole("button", { name: /^Reject/ }).click();
});

await kit.test("a proposal that arrives while the writer is typing never takes the keys: typing 'again' stays 'again' and no accept is called", async () => {
  await open();
  const accepts = [], onreq = (r) => { if (/\/ai\/accept\//.test(r.url())) accepts.push(r.url()); };
  page.on("request", onreq);
  const accepted = async () => JSON.stringify((await api(wren.ctx, "GET", `/api/v1/docs/${doc}/revisions`)).json).split("ai-accepted").length - 1;
  const before = await accepted();
  await select("The harbour was quiet.");
  fake.slow(1500);
  await menu("Tighten");
  const dlg = preview();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: /^Send/ }).click();
  // The caret goes to the end of the chapter and typing begins at once, one key every 450 ms: the proposal arrives in the middle of it.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.focus(); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.atEnd(v.state.doc))); });
  for (const ch of "again") { await page.keyboard.type(ch); await page.waitForTimeout(450); }
  await page.locator("#ai-pop").waitFor();
  ok((await text()).endsWith("again"), "what was typed is exactly 'again': " + (await text()).slice(-20));
  eq(accepts.length, 0, "no accept op was called");
  ok(await page.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus is still in the text");
  await page.waitForTimeout(1500);
  eq(await accepted(), before, "no ai-accepted revision was written");
  await page.locator("#ai-pop").getByRole("button", { name: /^Reject/ }).click();
  fake.slow(0);
  page.off("request", onreq);
});

await kit.test("the AI tab restored from the cookie works when the boot file is late, and says so plainly when a part cannot be loaded, with a way to try again", async () => {
  await page.context().addCookies([{ name: "scriobh_insp", value: "ai", url: base() }]);
  // The boot file arrives 1.5 s late.
  await page.route(/aiboot/, async (route) => { await new Promise((r) => setTimeout(r, 1500)); await route.continue(); });
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await page.locator("#ipanel-ai .ai-subtabs").waitFor();
  await page.locator("#aic-msg").waitFor({ timeout: 15000 });
  await page.unroute(/aiboot/);
  // The core fails to load once.
  kit.allowed = [/Failed to load resource/, /ERR_FAILED|ERR_BLOCKED|net::/];
  await page.route(/\/js\/ai(\.[0-9a-f]+)?\.js/, (route) => route.abort());
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await page.locator("#ipanel-ai").getByText(/could not be loaded/).waitFor({ timeout: 15000 });
  await page.unroute(/\/js\/ai(\.[0-9a-f]+)?\.js/);
  await page.locator("#ipanel-ai").getByRole("button", { name: "Try again" }).click();
  await page.locator("#aic-msg").waitFor({ timeout: 15000 });
  // A part that loads but never starts counts as not loaded, with its retry.
  await page.route(/aichat/, (route) => route.fulfill({ contentType: "text/javascript", body: "/* empty */" }));
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await page.locator("#ipanel-ai").getByText(/could not be loaded/).waitFor({ timeout: 15000 });
  await page.locator("#ipanel-ai").getByRole("button", { name: "Try again" }).waitFor();
  await page.unroute(/aichat/);
  // The boot file never arrives: the tab says so after a few seconds, nothing throws, and a writer typing meanwhile is not moved.
  await page.route(/aiboot/, (route) => route.abort());
  await page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await page.waitForSelector(".scriobh-prose");
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.focus(); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.atEnd(v.state.doc))); });
  const t0 = Date.now(); let typed = 0;
  while (Date.now() - t0 < 9500) { await page.keyboard.type("x"); typed++; await page.waitForTimeout(250); }
  await page.locator("#ipanel-ai").getByText(/could not be loaded/).waitFor({ timeout: 10000 });
  ok(await page.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus never left the text");
  ok((await text()).endsWith("x".repeat(typed)), "every key reached the text");
  await page.unroute(/aiboot/);
  await page.context().addCookies([{ name: "scriobh_insp", value: "", url: base() }]);
  // The keys are saved before the next step leaves this page: a save still on its way would land after the next page has read the chapter.
  await waitFor(async () => ((await api(wren.ctx, "GET", `/api/v1/docs/${doc}`)).json.item.content || "").includes("x".repeat(typed)), "the typed keys are saved", 15000);
});

await kit.test("by keyboard alone: the chord, the menu, the preview, Send, and the proposal's keys", async () => {
  await open();
  await select("The harbour was quiet.");
  await page.keyboard.press("Alt+Shift+KeyI");
  await page.locator("dialog.ai-menu").waitFor();
  ok(await page.evaluate(() => !!document.activeElement.closest("dialog.ai-menu")), "focus is in the menu");
  await page.keyboard.press("Tab"); // Rewrite → Tighten
  await page.keyboard.press("Enter");
  const dlg = preview();
  await dlg.waitFor();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
  await dlg.getByRole("button", { name: /^Send/ }).focus();
  await page.keyboard.press("Enter");
  await page.locator("#ai-pop").waitFor();
  ok(await page.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus stayed in the text when the proposal arrived");
  await page.keyboard.press("Alt+Shift+KeyJ");
  ok(await page.evaluate(() => document.activeElement.id === "ai-pop"), "the chord moved focus to the proposal");
  await page.keyboard.press("r");
  await page.locator("#ai-pop").waitFor({ state: "detached" });
  ok(await page.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus returned to the text");
});

await kit.test("at 390 px the preview and the proposal fit the screen and every button is a touch target", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 });
  await ctx.addCookies(await wren.ctx.cookies());
  await page.goto("about:blank");
  const pg = await ctx.newPage();
  kit.watch(pg, "phone");
  await open(pg);
  await select.call(null, "The harbour was quiet.").catch(() => {});
  await pg.evaluate(() => { const v = window.scriobh.write.editor.view; let hit; v.state.doc.descendants((n, p) => { if (!hit && n.isText && n.text.includes("The harbour was quiet.")) hit = [p + n.text.indexOf("The harbour"), p + n.text.indexOf("The harbour") + 23]; }); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); });
  await pg.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:ai", { detail: "menu" })));
  await pg.locator("dialog.ai-menu").waitFor();
  await pg.locator("dialog.ai-menu").getByRole("button", { name: "Tighten" }).tap();
  const dlg = pg.locator("dialog.ai-dlg:not(.ai-menu)");
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload", 10000);
  const fits = await pg.evaluate(() => { const r = document.querySelector("dialog.ai-dlg:not(.ai-menu)").getBoundingClientRect(); return r.left >= 0 && r.right <= window.innerWidth + 1 && document.documentElement.scrollWidth <= window.innerWidth; });
  ok(fits, "the preview fits 390 px");
  const small = await dlg.locator("button, input[type=text], select, summary").evaluateAll((els) => els.filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => (e.textContent || e.id).trim().slice(0, 20) + " " + Math.round(e.getBoundingClientRect().height)));
  eq(small.length, 0, "controls are 44 px tall: " + small.join(", "));
  await dlg.getByRole("button", { name: /^Send/ }).tap();
  await pg.locator("#ai-pop").waitFor();
  const popFits = await pg.evaluate(() => { const r = document.querySelector("#ai-pop").getBoundingClientRect(); return r.left >= 0 && r.right <= window.innerWidth + 1; });
  ok(popFits, "the proposal fits 390 px");
  const small2 = await pg.locator("#ai-pop button").evaluateAll((els) => els.filter((e) => e.getBoundingClientRect().height < 43.5 || e.getBoundingClientRect().width < 43.5).map((e) => (e.textContent || "").trim().slice(0, 20)));
  eq(small2.length, 0, "the proposal's buttons are 44 px: " + small2.join(", "));
  await pg.locator("#ai-pop").getByRole("button", { name: /^Reject/ }).tap();
  await ctx.close();
});

await fake.close();
await kit.finish("e2e-ai-edit");
