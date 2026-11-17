#!/usr/bin/env node
// The AI screens with the keyboard alone, and with reduced motion: the chat, the reviews, the Lab and both settings pages can be
// reached, used and left with Tab, Shift+Tab, Enter, Space, the arrows and Escape; every stop shows where focus is; focus never
// lands on the page body after something is done; nothing animates or transitions with reduced motion; and every theme draws the
// panels and the Lab's marks (a screenshot of each is kept in the scratch folder named by SCREENS_OUT, never in the repository).
//
//   node scripts/e2e-ai-keys.mjs [--only substring]
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startFake } from "./fake-ai.mjs";

const kit = await createKit("e2e-ai-keys", { env: { SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000" } });
kit.deadline(300000); // a stalled step fails the script instead of hanging it
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor } = kit;
const base = () => kit.base();
const fake = await startFake();
const wren = await kit.signedIn();
const ada = await kit.signedIn({ viewport: { width: 1280, height: 900 } }, "admin@dev.scriobh");
const page = wren.page;
kit.main = page;
let book, doc, pid;
const TEXT = "The harbour was  very quiet. Mara counted the boats twice, perhaps.\n\nThe tide turned .\n";
const open = async () => { await page.goto(`${base()}/p/${book.slug}/d/${doc}`); await page.waitForSelector(".scriobh-prose"); await waitFor(() => page.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 10000); };
const preview = () => page.locator("dialog.ai-dlg:not(.ai-menu)");
const ready = async (dlg) => waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload");
// Press Tab until `match(descriptor)` holds, and say where focus has been; fail if it is lost to the page or never arrives.
const here = (pg) => pg.evaluate(() => {
  const e = document.activeElement, cs = getComputedStyle(e);
  return { tag: e.tagName, id: e.id, text: (e.getAttribute("aria-label") || e.textContent || e.value || "").trim().slice(0, 40), ring: (cs.outlineStyle !== "none" && parseFloat(cs.outlineWidth) > 0) || cs.boxShadow !== "none", body: e === document.body };
});
async function tabTo(pg, match, label, max = 60, key = "Tab") {
  const seen = [];
  for (let i = 0; i < max; i++) {
    await pg.keyboard.press(key);
    const d = await here(pg);
    seen.push(d.id || d.text);
    ok(!d.body, `${label}: focus was lost to the page body after ${seen.join(" > ")}`);
    if (match(d)) { ok(d.ring, `${label}: a visible focus indicator on ${d.id || d.text}`); return d; }
  }
  throw new Error(`${label}: never reached it by ${key} (${seen.join(" > ")})`);
}

await kit.test("setup: a provider that stays local, a project with AI on, a chapter with marks to find", async () => {
  const pv = await api(ada.ctx, "POST", "/api/v1/ai/providers", { kind: "openai_compatible", name: "Fake", base_url: fake.base, local: true, default_model: "fake-model", scope: "instance" });
  ok(pv.status < 300, "provider");
  pid = pv.json.item.id;
  book = (await api(wren.ctx, "POST", "/api/v1/projects", { title: "AI Keys", kind: "novel" })).json.item;
  doc = (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = (await api(wren.ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(wren.ctx, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "Harbour", content: TEXT });
  ok((await api(wren.ctx, "PUT", `/api/v1/projects/${book.id}/ai/policy`, { mode: "ask", providers: [pid], actions: ["selection", "alternatives", "chat", "critique", "summaries", "continuity", "extraction", "lab"], redaction: "off" })).status < 300, "policy");
  await open();
});

await kit.test("the chat by keyboard alone: reach it, write, preview, send, and focus comes back to the message box", async () => {
  await page.evaluate(() => window.scriobh.inspector.open("ai", true));
  await page.locator("#ipanel-ai .ai-subtabs").waitFor();
  eq((await here(page)).id, "itab-ai", "the AI tab is focused when it is opened from the keyboard");
  await page.locator("#aic-msg").waitFor();
  await tabTo(page, (d) => d.id === "aitab-chat", "to the Chat view");
  await tabTo(page, (d) => d.id === "aic-sel", "to the kept chats");
  await tabTo(page, (d) => d.id === "aic-keep", "to Keep");
  await tabTo(page, (d) => d.id === "aic-msg", "to the message box");
  await page.keyboard.type("Is the opening slow?");
  await page.keyboard.press("Control+Enter");
  const dlg = preview();
  await dlg.waitFor();
  eq(await page.evaluate(() => document.activeElement.tagName), "H2", "the preview opens on its heading");
  await ready(dlg);
  await tabTo(page, (d) => /^Send/.test(d.text), "to Send in the preview");
  await page.keyboard.press("Enter");
  await dlg.waitFor({ state: "detached" });
  eq((await here(page)).id, "aic-msg", "focus is back in the message box");
  await waitFor(async () => /fake model/.test(await page.locator("#ipanel-ai").innerText()), "the answer", 10000);
  await page.keyboard.press("Shift+Tab");
  ok((await here(page)).id !== "", "Shift+Tab moves backwards");
});

await kit.test("the reviews by keyboard alone: arrows between the views, Enter on a button, the review gets focus", async () => {
  await page.evaluate(() => document.getElementById("aitab-chat").focus());
  await page.keyboard.press("ArrowRight");
  eq((await here(page)).id, "aitab-reviews", "the right arrow moves to Reviews");
  await page.getByRole("button", { name: "Critique this chapter" }).waitFor();
  await tabTo(page, (d) => /Critique this chapter/.test(d.text), "to Critique this chapter");
  await page.keyboard.press("Enter");
  const dlg = preview();
  await dlg.waitFor();
  await ready(dlg);
  await tabTo(page, (d) => /^Send/.test(d.text), "to Send");
  await page.keyboard.press("Enter");
  await waitFor(async () => (await page.locator("#ipanel-ai .ai-rv").count()) >= 1 && /None of this changes the chapter/.test(await page.locator("#ipanel-ai").innerText()), "the review to open", 15000);
  ok(/Critique/.test((await here(page)).text), "the new review's heading has focus");
  await tabTo(page, (d) => /^Show in the text/.test(d.text), "to Show in the text");
  await page.keyboard.press("Enter");
  ok(await page.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "Show moves focus into the text");
  await page.evaluate(() => window.scriobh.inspector.open("lab", true));
});

await kit.test("the Lab by keyboard alone: check, then the marks one by one", async () => {
  await page.evaluate(() => window.scriobh.inspector.open("lab", true));
  await page.locator("#ipanel-lab .lab").waitFor();
  await tabTo(page, (d) => /^Check this chapter/.test(d.text), "to Check this chapter");
  await page.keyboard.press("Enter");
  await waitFor(async () => (await page.locator("#ipanel-lab .ai-rv").count()) >= 2, "the marks", 10000);
  await tabTo(page, (d) => d.text === "Show", "to Show", 80);
  await page.keyboard.press("Enter");
  ok(await page.evaluate(() => !!document.activeElement.closest(".scriobh-prose")), "Show moves focus into the text, on the marked words");
  await page.keyboard.press("Alt+Shift+KeyJ");
});

await kit.test("the settings pages by keyboard alone: every control is reached in order and Escape leaves nothing armed", async () => {
  const pg = ada.page;
  await pg.goto(`${base()}/settings/ai`);
  await pg.waitForSelector("#ai-root:not([data-loading])");
  await pg.evaluate(() => document.querySelector("#ai-root").scrollIntoView());
  await pg.evaluate(() => { const s = document.querySelector("#providers details summary"); s.focus(); });
  await pg.keyboard.press("Enter");
  eq(await pg.locator("#providers details").getAttribute("open"), "", "Enter opens the add-a-provider form");
  await tabTo(pg, (d) => d.id === "pf-kind", "to Kind", 10);
  await tabTo(pg, (d) => d.id === "pf-name", "to Name", 10);
  await tabTo(pg, (d) => d.id === "pf-key", "to API key", 10);
  await tabTo(pg, (d) => /^Add the provider/.test(d.text), "to Add the provider", 20);
  // A provider whose Remove is armed with the keyboard is disarmed by Escape.
  await pg.locator("#pf-name").fill("Throwaway");
  await pg.locator("#pf-url").fill(fake.base);
  await pg.locator("#pf-local").check();
  await pg.getByRole("button", { name: "Add the provider" }).press("Enter");
  const card = pg.locator(".ai-card", { has: pg.locator("h3", { hasText: "Throwaway" }) });
  await card.waitFor();
  await card.getByRole("button", { name: "Remove" }).focus();
  await pg.keyboard.press("Enter");
  await pg.waitForTimeout(700);
  ok(await card.getByRole("button", { name: /Really remove/ }).count() === 1, "Enter arms it");
  await pg.keyboard.press("Escape");
  ok(await card.getByRole("button", { name: "Remove", exact: true }).count() === 1, "Escape disarms it, and focus is still on it");
  eq((await here(pg)).text, "Remove", "focus stays on the button");
  await card.getByRole("button", { name: "Remove" }).focus();
  await pg.keyboard.press("Enter");
  await pg.keyboard.press("Enter"); // at once: the half second has not passed, so this confirms nothing
  await pg.waitForTimeout(100);
  eq(await card.count(), 1, "a second Enter at once does not remove it");
  await pg.waitForTimeout(700);
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await card.count()) === 0, "the provider to be removed");
});

// Every guarded button in the app: holding Enter on it, or double-clicking it, confirms nothing; typing elsewhere does not reach it.
// What a browser does for a held Enter: a keydown to the focused element and, on a button, a click; repeated while the key is down
// (the repeats say so); then a key-up. The events are made in the page: a long run of CDP key events (Input.dispatchKeyEvent) to a
// button that disables itself left the browser process spinning, and the click made by a key is the thing the guards look at.
const holdEnter = async (pg, ms = 1000) => pg.evaluate(async (ms) => {
  const fire = (type, repeat) => { const el = document.activeElement || document.body; el.dispatchEvent(new KeyboardEvent(type, { key: "Enter", code: "Enter", bubbles: true, cancelable: true, repeat })); return el; };
  const press = (repeat) => { const el = fire("keydown", repeat); if (el.tagName === "BUTTON" && !el.disabled) el.click(); };
  press(false);
  for (let i = 0; i < Math.round(ms / 33); i++) { await new Promise((r) => setTimeout(r, 33)); press(true); }
  fire("keyup", false);
}, ms);
await kit.test("Delete the named version: holding Enter, a double-click and typing elsewhere delete nothing; a new press does", async () => {
  const snap = await api(wren.ctx, "POST", `/api/v1/docs/${doc}/revisions`, { label: "Guarded" });
  ok(snap.status === 201, "a named version");
  const have = async () => (await api(wren.ctx, "GET", `/api/v1/docs/${doc}/revisions?reason=manual`)).json.items.some((r) => r.label === "Guarded");
  await open();
  await page.evaluate(() => window.scriobh.inspector.open("versions", true));
  const del = () => page.getByRole("button", { name: /^(Delete|Confirm deleting) the named version/ }).first();
  await page.getByRole("button", { name: /^Today/ }).click();
  await del().waitFor();
  await del().dblclick();
  await page.waitForTimeout(800);
  ok(await have(), "a double-click deleted nothing");
  await page.evaluate(() => window.scriobh.write.editor.view.focus()); // leaving it disarms it
  await del().click();
  await page.evaluate(() => window.scriobh.write.editor.view.focus());
  await page.waitForTimeout(800);
  await page.keyboard.type(" ");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Backspace");
  await page.keyboard.press("Backspace");
  ok(await have(), "keys typed in the text did not reach it");
  for (let i = 0; ; i++) { // the list redraws when it has loaded: focus the button once it stays
    await del().focus();
    if (await page.evaluate(() => /named version/.test(document.activeElement.getAttribute("aria-label") || ""))) break;
    if (i > 6) throw new Error("the button cannot be focused");
    await page.waitForTimeout(300);
  }
  await holdEnter(page, 800);
  await page.waitForTimeout(1500); // the held key's last events have been handled by now
  ok(await have(), "holding Enter deleted nothing");
  // A new press: it may have to arm the button again if the held one left it disarmed, and then needs one more.
  for (let i = 0; i < 4 && (await have()); i++) {
    await page.getByRole("button", { name: /^(Delete|Confirm deleting) the named version/ }).first().press("Enter");
    await page.waitForTimeout(800);
  }
  ok(!(await have()), "a new press deletes it");
});

await kit.test("with reduced motion nothing on the new surfaces animates or transitions", async () => {
  const ctx = await kit.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
  await ctx.addCookies(await wren.ctx.cookies());
  const pg = await ctx.newPage();
  kit.watch(pg, "reduced");
  await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 10000);
  await pg.evaluate(() => window.scriobh.inspector.open("lab", true));
  await pg.getByRole("button", { name: "Check this chapter" }).click();
  await waitFor(async () => (await pg.locator(".scriobh-prose .lab-mark").count()) >= 2, "marks drawn", 10000);
  const moving = await pg.evaluate(() => [...document.querySelectorAll("#ipanel-lab *, .lab-mark, .ai-chip, .ai-chip-box")].filter((e) => { const cs = getComputedStyle(e); return (cs.animationName !== "none" && parseFloat(cs.animationDuration) > 0.01) || parseFloat(cs.transitionDuration) > 0.01; }).map((e) => e.className));
  eq(moving.length, 0, "nothing animates or transitions: " + moving.slice(0, 5).join(" | "));
  await ctx.close();
});

// Every theme draws the panels, the chip and the marks. The screenshots are for a person to look at; the checks are that marks
// have a line of their own colour (not the accent) and the panels have no horizontal overflow.
for (const [name, attrs] of [["paper", { theme: "light" }], ["sepia", { theme: "light", lt: "sepia" }], ["ink", { theme: "dark" }], ["slate", { theme: "dark", dk: "slate" }], ["moss", { theme: "dark", dk: "moss" }]]) {
  await kit.test(`theme ${name}: the Lab's marks, the chat and the chip are drawn and the marks are not in the accent`, async () => {
    const ctx = await kit.newContext({ viewport: { width: 1280, height: 900 } });
    await ctx.addCookies(await wren.ctx.cookies());
    const pg = await ctx.newPage();
    kit.watch(pg, name);
    await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
    await pg.waitForSelector(".scriobh-prose");
    await waitFor(() => pg.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the writing view", 10000);
    await pg.evaluate((a) => { const r = document.documentElement; r.dataset.theme = a.theme; if (a.lt) r.dataset.lt = a.lt; else delete r.dataset.lt; if (a.dk) r.dataset.dk = a.dk; else delete r.dataset.dk; }, attrs);
    await pg.evaluate(() => window.scriobh.inspector.open("lab", true));
    await pg.getByRole("button", { name: "Check this chapter" }).click();
    await waitFor(async () => (await pg.locator(".scriobh-prose .lab-mark").count()) >= 2, "marks drawn", 10000);
    const colours = await pg.evaluate(() => { const cs = (e) => getComputedStyle(e); const m = document.querySelector(".scriobh-prose .lab-mark"); return { line: cs(m).textDecorationColor, accent: cs(document.documentElement).getPropertyValue("--accent").trim(), lab: cs(document.documentElement).getPropertyValue("--lab").trim() }; });
    ok(colours.lab && colours.lab.toLowerCase() !== colours.accent.toLowerCase(), `the Lab's colour ${colours.lab} is not the accent ${colours.accent}`);
    eq(await pg.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), true, "no sideways scroll");
    if (process.env.SCREENS_OUT) { mkdirSync(process.env.SCREENS_OUT, { recursive: true }); await pg.screenshot({ path: join(process.env.SCREENS_OUT, `lab-${name}.png`) }); }
    await ctx.close();
  });
}

await fake.close();
await kit.finish("e2e-ai-keys");
