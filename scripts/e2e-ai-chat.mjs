#!/usr/bin/env node
// The writing partner's chat and reviews against a fake provider. A chat message is previewed before it is sent (Cancel sends
// nothing); the answer streams in and can be stopped; a chat is gone with the page unless it is kept, and a kept chat is private to
// the person who kept it (another member of the project, and an administrator, cannot read or list it) and can be exported and
// deleted. A critique becomes a review: notes that point at the text, shown, resolved or dismissed, never changing the chapter.
// An extraction's entries go into the bible one at a time, after a dry run. A summary is a proposal for the synopsis. A spent
// budget stops a chat message before it leaves.
//
//   node scripts/e2e-ai-chat.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startFake } from "./fake-ai.mjs";

const kit = await createKit("e2e-ai-chat", { env: { SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000" } });
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
const TEXT = "The harbour was very quiet. Mara Voss counted the boats twice.\n\nThe tide turned.\n";
const stored = async () => (await api(wren.ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
const open = async (pg = page) => { await pg.goto(`${base()}/p/${book.slug}/d/${doc}`); await pg.waitForSelector(".scriobh-prose"); await waitFor(() => pg.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 10000); };
const tab = async (name, pg = page) => { await pg.evaluate(() => window.scriobh.inspector.open("ai", true)); await pg.locator("#ipanel-ai .ai-subtabs").waitFor(); await pg.locator("#aitab-" + name).click(); };
const preview = (pg = page) => pg.locator("dialog.ai-dlg:not(.ai-menu)");
const policy = (extra = {}) => api(wren.ctx, "PUT", `/api/v1/projects/${book.id}/ai/policy`, Object.assign({ mode: "ask", providers: [pid], actions: ["selection", "alternatives", "chat", "critique", "summaries", "continuity", "extraction", "lab"], redaction: "off" }, extra));
const ready = async (dlg) => waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
const chats = (who) => api(who.ctx, "GET", `/api/v1/projects/${book.id}/ai/chats`);

await kit.test("setup: a provider that stays local, a project with AI on, a chapter", async () => {
  const pv = await api(ada.ctx, "POST", "/api/v1/ai/providers", { kind: "openai_compatible", name: "Fake", base_url: fake.base, local: true, default_model: "fake-model", scope: "instance" });
  ok(pv.status < 300, "provider: " + JSON.stringify(pv.json));
  pid = pv.json.item.id;
  book = (await api(wren.ctx, "POST", "/api/v1/projects", { title: "AI Chat", kind: "novel" })).json.item;
  doc = (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = await stored();
  await api(wren.ctx, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "Harbour", content: TEXT });
  ok((await policy()).status < 300, "policy");
  const mem = await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/members`, { email: "admin@dev.scriobh", role: "editor" });
  ok(mem.status < 300, "a second editor joins the project: " + JSON.stringify(mem.json));
  await open();
});

await kit.test("a chat message is previewed: Cancel sends nothing; Send streams the answer in", async () => {
  await tab("chat");
  await page.locator("#aic-msg").fill("Is the opening too slow?");
  await page.locator("#ipanel-ai").getByRole("button", { name: /^Send/ }).click();
  const dlg = preview();
  await dlg.waitFor();
  await ready(dlg);
  ok((await dlg.innerText()).includes("Is the opening too slow?"), "the message is in the payload");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  await dlg.waitFor({ state: "detached" });
  eq(fake.count(), 0, "Cancel sent nothing");
  eq(await page.locator("#ipanel-ai .ai-msg").count(), 0, "nothing was added to the conversation");
  await page.locator("#ipanel-ai").getByRole("button", { name: /^Send/ }).click();
  await dlg.waitFor();
  await ready(dlg);
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await dlg.waitFor({ state: "detached" });
  eq(await page.evaluate(() => document.activeElement.id), "aic-msg", "after Send, focus is back in the chat's message box, not in the manuscript");
  eq(await page.locator('#ipanel-ai input[aria-label="Chat title"]').count(), 0, "there is no hidden text field to tab through");
  await waitFor(async () => /fake model/.test(await page.locator('#ipanel-ai .ai-msg[data-role="assistant"] .ai-msg-text').innerText()), "the answer", 10000);
  eq(fake.count(), 1, "one request went out");
  eq((await stored()).content, TEXT, "the chapter is untouched");
  eq((await chats(wren)).json.items.length, 0, "not kept: nothing is stored on the server");
});

await kit.test("pressing Send twice opens one preview, not two", async () => {
  await page.locator("#aic-msg").fill("Twice.");
  await page.locator("#ipanel-ai").getByRole("button", { name: /^Send/ }).dblclick();
  const dlg = preview();
  await dlg.first().waitFor();
  await page.waitForTimeout(1000);
  eq(await dlg.count(), 1, "one preview");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  await dlg.first().waitFor({ state: "detached" });
  await page.locator("#aic-msg").fill("");
});

await kit.test("Stop ends a streaming answer: what arrived is shown as incomplete and is not kept", async () => {
  fake.say("word ".repeat(900));
  await page.locator("#aic-msg").fill("Go on at length.");
  await page.locator("#ipanel-ai").getByRole("button", { name: /^Send/ }).click();
  const dlg = preview();
  await dlg.waitFor();
  await ready(dlg);
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await waitFor(async () => fake.count() === 2, "the second request");
  const second = JSON.parse(fake.requests()[1]).messages.map((m) => m.role + ": " + m.content);
  ok(second.some((m) => m.startsWith("user: Is the opening too slow?")) && second.some((m) => m.startsWith("assistant: ") && /fake model/.test(m)), "the conversation so far goes with the second message: " + second.map((m) => m.slice(0, 40)).join(" | "));
  await page.locator("#ipanel-ai").getByRole("button", { name: "Stop" }).waitFor({ state: "visible" });
  await waitFor(async () => (await page.locator('#ipanel-ai .ai-msg[data-role="assistant"] .ai-msg-text').last().innerText()).length > 20, "some of the answer");
  await page.locator("#ipanel-ai").getByRole("button", { name: "Stop" }).click();
  eq(await page.evaluate(() => document.activeElement.id), "aic-msg", "after Stop, focus is in the message box, not on the page body");
  await waitFor(async () => /Incomplete|cancel/i.test(await page.locator("#ipanel-ai .ai-msg small").last().innerText()), "the stop to be shown");
  const t = await page.locator('#ipanel-ai .ai-msg[data-role="assistant"] .ai-msg-text').last().innerText();
  ok(t.length < 4500, "it did not run to the end");
  eq(await page.locator("#ipanel-ai").getByRole("button", { name: "Stop" }).isVisible(), false, "Stop is gone");
  fake.say(null);
});

await kit.test("a kept chat is stored, listed, private to its owner, exported and deleted", async () => {
  await page.locator("#aic-keep").check();
  await page.locator("#aic-msg").fill("Remember this: the ledger is wrong.");
  await page.locator("#ipanel-ai").getByRole("button", { name: /^Send/ }).click();
  const dlg = preview();
  await dlg.waitFor();
  await ready(dlg);
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await waitFor(async () => (await chats(wren)).json.items.length === 1, "the chat to be kept", 10000);
  const mine = (await chats(wren)).json.items[0];
  eq(mine.turn_count, 2, "one exchange is kept");
  ok((await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/ai/chat?chat_id=${mine.chat_id}`)).json.item.turns.some((x) => /the ledger is wrong/.test(x.content)), "the message is in it");
  eq((await chats(ada)).json.items.length, 0, "another editor of the project (an administrator too) sees no chats");
  eq((await api(ada.ctx, "GET", `/api/v1/projects/${book.id}/ai/chat?chat_id=${mine.chat_id}`)).status, 404, "another member cannot open it by its id");
  eq((await api(ada.ctx, "DELETE", `/api/v1/projects/${book.id}/ai/chat?chat_id=${mine.chat_id}`)).status, 404, "or delete it");
  eq((await chats(wren)).json.items.length, 1, "and it is still there");
  await page.reload();
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => !!window.scriobh.write), "the view");
  await tab("chat");
  await waitFor(async () => (await page.locator("#aic-sel option").count()) >= 2, "the chat list");
  await page.locator("#aic-sel").selectOption(mine.chat_id);
  await waitFor(async () => (await page.locator("#ipanel-ai .ai-msg").count()) === 2, "the kept chat to open");
  const [dl] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "Export" }).click()]);
  ok(/\.md$/.test(dl.suggestedFilename()), "the export is a Markdown file: " + dl.suggestedFilename());
  ok(/Only what is said after you turn this on is kept/.test(await page.locator("#ipanel-ai").innerText()), "the panel says plainly that only what comes after Keep is kept");
  await page.getByRole("button", { name: "Delete this chat" }).dblclick();
  await page.waitForTimeout(800);
  eq((await chats(wren)).json.items.length, 1, "a double-click does not delete the chat");
  ok(await page.getByRole("button", { name: "Really delete it?" }).count() === 1, "it is armed and asks");
  await page.keyboard.press("Escape");
  ok(await page.getByRole("button", { name: "Delete this chat" }).count() === 1, "Escape puts it back");
  await page.getByRole("button", { name: "Delete this chat" }).click();
  await page.getByRole("button", { name: "Really delete it?" }).click();
  await waitFor(async () => (await chats(wren)).json.items.length === 0, "the chat to be deleted");
  await waitFor(async () => (await page.locator("#ipanel-ai .ai-msg").count()) === 0, "the conversation to be cleared from the panel");
});

await kit.test("a spent budget stops a chat message before it leaves", async () => {
  ok((await policy({ budget_tokens: 1 })).status < 300, "a one-token cap");
  await open();
  const n = fake.count();
  await tab("chat");
  await page.locator("#aic-msg").fill("Anything.");
  await page.locator("#ipanel-ai").getByRole("button", { name: /^Send/ }).click();
  const dlg = preview();
  await waitFor(async () => /cannot be sent/i.test(await dlg.innerText()), "the refusal");
  eq(await dlg.getByRole("button", { name: /^Send/ }).isDisabled(), true, "Send is off");
  await dlg.getByRole("button", { name: "Cancel" }).click();
  eq(fake.count(), n, "nothing was sent");
  ok((await policy({ budget_tokens: 0 })).status < 300, "the cap is lifted");
});

await kit.test("a critique becomes a review of notes that point at the text; resolving and dismissing change no text", async () => {
  await open();
  await tab("reviews");
  await page.getByRole("button", { name: "Critique this chapter" }).click();
  const dlg = preview();
  await dlg.waitFor();
  await ready(dlg);
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await waitFor(async () => (await page.locator("#ipanel-ai .ai-rv").count()) >= 1 && /None of this changes the chapter/.test(await page.locator("#ipanel-ai").innerText()), "the review to open", 15000);
  ok(await page.evaluate(() => /Critique/.test(document.activeElement.textContent || "")), "focus lands on the new review's heading");
  const note = page.locator("#ipanel-ai .ai-rv").first();
  ok(/slow/.test(await note.innerText()), "the note is shown: " + (await note.innerText()).slice(0, 80));
  await note.getByRole("button", { name: "Show in the text" }).click();
  const sel = await page.evaluate(() => { const v = window.scriobh.write.editor.view; const s = v.state.selection; return v.state.doc.textBetween(s.from, s.to); });
  ok(/harbour was very quiet/.test(sel), "the passage is selected: " + sel);
  await note.getByRole("button", { name: "Resolve" }).click();
  await waitFor(async () => /dealt with/.test(await page.locator("#ipanel-ai").innerText()), "the note to be marked");
  eq((await stored()).content, TEXT, "the chapter is unchanged");
  // Three notes: Dismiss all asks first, and a double-click does not do it.
  fake.say(JSON.stringify({ notes: ["The harbour was very quiet.", "Mara Voss counted the boats twice.", "The tide turned."].map((q, i) => ({ quote: q, category: "pacing", severity: "low", note: "Note " + (i + 1) })) }));
  await page.getByRole("button", { name: "Back" }).click();
  await page.getByRole("button", { name: "Critique this chapter" }).click();
  const d2 = preview();
  await d2.waitFor();
  await ready(d2);
  await d2.getByRole("button", { name: /^Send/ }).click();
  await waitFor(async () => (await page.locator("#ipanel-ai .ai-rv").count()) === 3, "three notes", 15000);
  fake.say(null);
  await page.getByRole("button", { name: /^Dismiss all 3 open/ }).dblclick();
  await page.waitForTimeout(800);
  eq(await page.locator("#ipanel-ai .ai-rv").getByRole("button", { name: "Dismiss" }).count(), 3, "a double-click dismissed nothing");
  await page.getByRole("button", { name: /^Really dismiss all 3/ }).click();
  await waitFor(async () => (await page.locator("#ipanel-ai .ai-rv").getByRole("button", { name: "Dismiss" }).count()) === 0, "all three to be dismissed");
  // Deleting the review is guarded the same way.
  await page.getByRole("button", { name: "Delete this review" }).dblclick();
  await page.waitForTimeout(800);
  ok(await page.getByRole("button", { name: "Back" }).count() === 1 && await page.locator("#ipanel-ai .ai-rv").count() === 3, "a double-click did not delete the review");
  await page.keyboard.press("Escape");
  eq((await stored()).content, TEXT, "the chapter is unchanged");
});

await kit.test("a guarded button does not hand focus back to a writer who has moved on, and holding Enter confirms nothing", async () => {
  const reviews = async () => (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/ai/reviews?document_id=${doc}`)).json.items.length;
  const n0 = await reviews();
  ok(n0 >= 1, "a review is open");
  const del = page.locator("#ipanel-ai").getByRole("button", { name: "Delete this review" });
  await del.click();
  // The writer goes straight back to the text and types a space and Enter: neither may reach the button.
  await page.evaluate(() => window.scriobh.write.editor.view.focus());
  await page.waitForTimeout(900);
  ok(await page.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus stayed in the text after the half second");
  ok(await page.locator("#ipanel-ai").getByRole("button", { name: "Delete this review" }).count() === 1, "the button disarmed itself");
  await page.keyboard.type(" ");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Backspace");
  await page.keyboard.press("Backspace");
  eq(await reviews(), n0, "nothing was deleted by the keys typed in the text");
  // Holding Enter on the button: the press that arms it, and every repeat after it, confirms nothing.
  await del.focus();
  // A held Enter, made in the page (a flood of CDP key events to a button that disables itself wedged the browser): a keydown and a
  // click every 33 ms for a second, the repeats marked as such, then the key-up.
  await page.evaluate(async () => {
    const fire = (type, repeat) => { const el = document.activeElement || document.body; el.dispatchEvent(new KeyboardEvent(type, { key: "Enter", code: "Enter", bubbles: true, cancelable: true, repeat })); return el; };
    const press = (repeat) => { const el = fire("keydown", repeat); if (el.tagName === "BUTTON" && !el.disabled) el.click(); };
    press(false);
    for (let i = 0; i < 30; i++) { await new Promise((r) => setTimeout(r, 33)); press(true); }
    fire("keyup", false);
  });
  await page.waitForTimeout(800);
  eq(await reviews(), n0, "holding Enter deleted nothing");
  ok(await page.locator("#ipanel-ai").getByRole("button", { name: "Really delete it?" }).count() === 1, "it is armed and asks");
  await page.locator("#ipanel-ai").getByRole("button", { name: "Really delete it?" }).press("Enter");
  await waitFor(async () => (await reviews()) === n0 - 1, "a second, deliberate press to delete it");
});

await kit.test("a chat answer that fails does not take focus from the text", async () => {
  await open();
  await tab("chat");
  kit.allowed = [/ERR_FAILED|net::/, /Failed to load resource/];
  await page.route(/\/ai\/run\/stream/, (route) => route.abort());
  await page.locator("#aic-msg").fill("Will this fail?");
  await page.locator("#ipanel-ai").getByRole("button", { name: /^Send/ }).click();
  const dlg = preview();
  await dlg.waitFor();
  await ready(dlg);
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.evaluate(() => window.scriobh.write.editor.view.focus());
  await waitFor(async () => /Could not reach|did not arrive|cut off/i.test(await page.locator("#ipanel-ai .ai-msg small").last().innerText()), "the failure to be shown");
  await page.waitForTimeout(300);
  ok(await page.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "focus is still in the text");
  await page.unroute(/\/ai\/run\/stream/);
});

await kit.test("extraction: entries are proposed, a dry run says what would be made, and Create makes it through the entity ops", async () => {
  await open();
  await tab("reviews");
  await page.getByRole("button", { name: "Find names not in the bible" }).click();
  const dlg = preview();
  await dlg.waitFor();
  await ready(dlg);
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await waitFor(async () => (await page.locator('#ipanel-ai .ai-rv:has-text("Mara Voss")').count()) >= 1, "the proposed entry", 15000);
  const before = (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/entities`)).json;
  const li = page.locator('#ipanel-ai .ai-rv:has-text("Mara Voss")').first();
  await li.getByRole("button", { name: /^Add to the bible/ }).click();
  await waitFor(async () => /no bible of its own/.test(await li.innerText()), "the note that the project has no bible yet");
  eq(JSON.stringify((await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/entities`)).json), JSON.stringify(before), "nothing was made without a bible");
  ok((await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/compendium`, { name: "AI Chat Bible", kind: "fiction" })).status < 300, "the project gets a bible");
  await page.getByRole("button", { name: "Back" }).click();
  await page.locator("#ipanel-ai .ai-rv").getByRole("button", { name: "Open" }).first().click();
  await li.getByRole("button", { name: /^Add to the bible/ }).waitFor();
  await li.getByRole("button", { name: /^Add to the bible/ }).click();
  await waitFor(async () => /Dry run/.test(await li.innerText()), "the dry run");
  eq(JSON.stringify((await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/entities`)).json), JSON.stringify(before), "the dry run made nothing");
  await li.getByRole("button", { name: "Create it now" }).click();
  await waitFor(async () => JSON.stringify((await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/entities`)).json).includes("Mara Voss"), "the entity to exist", 10000);
});

await kit.test("a summary is a proposal for the synopsis: nothing is written until Accept", async () => {
  fake.say("A harbour pilot counts boats in a quiet harbour.");
  await tab("reviews");
  await page.getByRole("button", { name: "Back" }).click().catch(() => {});
  await page.getByRole("button", { name: "Summarise" }).click();
  const dlg = preview();
  await dlg.waitFor();
  await ready(dlg);
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.locator("#ai-pop").waitFor();
  eq((await stored()).synopsis_short || "", "", "the synopsis is empty while the proposal waits");
  ok(await page.evaluate(() => !document.activeElement.closest("#ai-pop")), "the card did not take focus");
  // Type, then Accept at once: the save goes first, the revision is the page's own, and nothing conflicts.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.focus(); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.atEnd(v.state.doc))); });
  await page.keyboard.type(" zz");
  await page.locator("#ai-pop").getByRole("button", { name: "Accept" }).click();
  await waitFor(async () => /harbour pilot/.test((await stored()).synopsis_short || (await stored()).synopsis_long || ""), "the synopsis to be written", 10000);
  await page.locator("#ai-pop").waitFor({ state: "detached" });
  ok((await stored()).content.includes("zz"), "what was typed just before is stored too");
  await page.keyboard.type("yy");
  await waitFor(async () => (await stored()).content.includes("yyzz") || (await stored()).content.includes("zzyy"), "typing after the accept saves too", 10000);
  eq(await page.locator(".notice-bar").count(), 0, "no conflict or error bar appeared");
  eq(await page.evaluate(() => window.scriobh.write.rev), (await stored()).revision, "the page holds the server's revision");
  fake.say(null);
});

await kit.test("accepting a synopsis keeps the editor's undo history: Ctrl+Z still removes what was typed", async () => {
  fake.say("A short summary of the chapter.");
  await open();
  await tab("reviews");
  await page.getByRole("button", { name: "Summarise" }).click();
  const dlg = preview();
  await dlg.waitFor();
  await ready(dlg);
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.locator("#ai-pop").waitFor();
  fake.say(null);
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.focus(); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.atEnd(v.state.doc))); });
  await page.keyboard.type(" kk");
  await page.waitForTimeout(1200);
  await waitFor(async () => !(await page.evaluate(() => window.scriobh.write.unsaved())), "saved", 15000);
  await page.locator("#ai-pop").getByRole("button", { name: "Accept" }).click();
  await page.locator("#ai-pop").waitFor({ state: "detached" });
  await page.keyboard.press("ControlOrMeta+KeyZ");
  await page.waitForTimeout(300);
  ok(!(await page.evaluate(() => { const d = window.scriobh.write.editor.view.state.doc; return d.textBetween(0, d.content.size, "\n"); })).includes("kk"), "Ctrl+Z removed what was typed before the accept");
});

await kit.test("Delete all my kept chats on the project page is guarded: a double-click does nothing", async () => {
  const made = await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/ai/chats`, { title: "Kept for the guard" });
  ok(made.status < 300, "a kept chat to delete");
  // "Delete all" is offered from two chats up (one chat has its own Delete).
  ok((await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/ai/chats`, { title: "Kept for the guard, too" })).status < 300, "a second kept chat");
  await page.goto(`${base()}/p/${book.slug}/ai`);
  await page.waitForSelector("#ai-root:not([data-loading])");
  await page.getByRole("button", { name: "Delete all my kept chats" }).dblclick();
  await page.waitForTimeout(800);
  eq((await chats(wren)).json.items.length, 2, "a double-click deleted nothing");
  await page.getByRole("button", { name: "Really delete them all?" }).click();
  await waitFor(async () => (await chats(wren)).json.items.length === 0, "the kept chats to go");
});

await kit.test("AI turned off for the whole server: the project page says so plainly, asks nothing that would be refused, and the header chip agrees", async () => {
  ok((await api(ada.ctx, "PUT", "/api/v1/ai/settings", { enabled: false })).status < 300, "the server's switch off");
  try {
    await page.goto(`${base()}/p/${book.slug}/ai`);
    await page.waitForSelector("#ai-root:not([data-loading])");
    const body = await page.locator("#ai-root").innerText();
    ok(/AI is turned off for this server/.test(body), "an honest note: " + body.slice(0, 120));
    ok(!/cannot see this project's AI settings/i.test(body), "it does not blame permissions");
    ok(/None of it is lost/.test(body), "it says what happens to what is kept");
    await open();
    eq((await page.locator(".ai-chip").innerText()).trim(), "AI off: server", "the header chip says AI is off for the server");
  } finally {
    await api(ada.ctx, "PUT", "/api/v1/ai/settings", { enabled: true });
  }
});

await kit.test("at 320 px the project's AI page does not scroll sideways, and at 390 px with a mouse every control is 44 px", async () => {
  for (const [w, h] of [[320, 700], [390, 844]]) {
    const ctx = await kit.newContext({ viewport: { width: w, height: h } });
    await ctx.addCookies(await wren.ctx.cookies());
    const pg = await ctx.newPage();
    kit.watch(pg, "narrow" + w);
    await pg.goto(`${base()}/p/${book.slug}/ai`);
    await pg.waitForSelector("#ai-root:not([data-loading])");
    eq(await pg.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), true, `no sideways scroll at ${w} px`);
    const small = await pg.locator("#ai-root button, #ai-root summary, #ai-root select, #ai-root input:not([type=checkbox]):not([type=radio])").evaluateAll((els) => els.filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => (e.textContent || e.id || e.name).trim().slice(0, 30)));
    eq(small.length, 0, `controls are 44 px tall at ${w}: ` + small.join(", "));
    await ctx.close();
  }
});

await kit.test("at 390 px the chat and reviews fit, and their controls are touch targets", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 });
  await ctx.addCookies(await wren.ctx.cookies());
  const pg = await ctx.newPage();
  kit.watch(pg, "phone");
  await open(pg);
  for (const name of ["chat", "reviews"]) {
    await tab(name, pg);
    await pg.waitForTimeout(400);
    const fits = await pg.evaluate(() => { const r = document.querySelector("#ipanel-ai").getBoundingClientRect(); return r.left >= 0 && r.right <= window.innerWidth + 1 && document.documentElement.scrollWidth <= window.innerWidth + 1; });
    ok(fits, name + " fits 390 px without a sideways scroll");
    const small = await pg.locator("#ipanel-ai button, #ipanel-ai select, #ipanel-ai textarea, #ipanel-ai summary").evaluateAll((els) => els.filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => (e.textContent || e.id).trim().slice(0, 30)));
    eq(small.length, 0, name + ": controls are 44 px tall: " + small.join(", "));
  }
  await ctx.close();
});

await fake.close();
await kit.finish("e2e-ai-chat");
