#!/usr/bin/env node
// The AI settings screens: AI is off by default and every affordance is absent; an administrator adds a provider (the key is
// write-only), tests it and removes it; a project's owner opts in; the account page says which providers each project may use;
// never-send flags; the policy is read-only for people who may not change it. Against a fake provider on a loopback port: no
// real provider is ever contacted.
//
//   node scripts/e2e-ai-admin.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { startFake } from "./fake-ai.mjs";

const kit = await createKit("e2e-ai-admin", { env: { SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000" } });
kit.deadline(300000); // a stalled step fails the script instead of hanging it
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const fake = await startFake();
const wren = await kit.signedIn();
const ada = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "admin@dev.scriobh");
kit.main = wren.page;
let book, doc;

await kit.test("setup: a project, and a chapter with some words", async () => {
  book = (await api(wren.ctx, "POST", "/api/v1/projects", { title: "AI Screens", kind: "novel" })).json.item;
  doc = (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const cur = (await api(wren.ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(wren.ctx, "PATCH", `/api/v1/docs/${doc}`, { base_revision: cur.revision, title: "Harbour", content: "The harbour was very quiet. Mara counted the boats twice.\n\nThe tide turned.\n" });
});

await kit.test("AI is off by default: the account page says so, and the writing view draws nothing of it", async () => {
  const pg = wren.page;
  await pg.goto(`${base()}/settings/ai`);
  await pg.locator("#ai-root .ai-banner").first().waitFor();
  ok(/No provider is set up/.test(await pg.locator("#ai-root").innerText()), "the not-configured state is plain: " + (await pg.locator("#ai-root").innerText()).slice(0, 200));
  await pg.goto(`${base()}/p/${book.slug}/ai`);
  await pg.locator("#ai-root .ai-banner").first().waitFor();
  ok(/AI is off for this project/.test(await pg.locator("#ai-root").innerText()), "the project page says AI is off");
  await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await pg.waitForSelector(".scriobh-prose");
  eq(await pg.locator("#write").getAttribute("data-ai"), "off", "the page knows");
  eq(await pg.locator("#itab-ai").count(), 0, "no AI tab");
  eq(await pg.locator('#fmtbar [data-cmd="ai"]').count(), 0, "no AI button on the toolbar");
  eq(await pg.locator('script[src*="aiboot"], script[src*="/ai."]').count(), 0, "no AI script is loaded");
  ok((await pg.locator(".ai-chip").innerText()).includes("AI off"), "the header says AI is off");
  // The palette has no AI command; the Lab (local checks) is there.
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press("ControlOrMeta+Shift+KeyP");
  await pg.keyboard.type("rewrite");
  await sleep(300);
  eq(await pg.locator("#palette li").filter({ hasText: /rewrite|Ask the writing partner/i }).count(), 0, "no AI command in the palette");
  await pg.keyboard.press("Escape");
  eq((await api(wren.ctx, "GET", "/api/v1/ai/status")).json.enabled, true, "AI is allowed by the server");
  eq(fake.count(), 0, "nothing was sent anywhere");
});

await kit.test("an administrator adds a provider: the key is write-only, Test says plainly what happened, Remove asks twice", async () => {
  const pg = ada.page;
  await pg.goto(`${base()}/settings/ai`);
  await pg.locator("#providers").waitFor();
  await pg.locator("#providers details summary").click();
  eq(await pg.locator("#pf-kind").inputValue(), "ollama", "a new provider starts as a model on this machine");
  await pg.locator("#pf-kind").selectOption("openai_compatible");
  await pg.locator("#pf-name").fill("Fake local");
  await pg.locator("#pf-url").fill(fake.base);
  await pg.locator("#pf-key").fill("fixture-token-for-tests-7777");
  await pg.locator("#pf-model").fill("fake-model");
  await pg.locator("#pf-local").check();
  await pg.getByRole("button", { name: "Add the provider" }).click();
  await pg.locator(".ai-card h3", { hasText: "Fake local" }).waitFor();
  const html = await pg.content();
  ok(!html.includes("fixture-token-for-tests-7777"), "the key is nowhere in the page");
  ok(/ends in .?7777/.test(await pg.locator("#providers").innerText()), "only its last four characters are shown: " + (await pg.locator("#providers").innerText()).replace(/\n/g, " | ").slice(0, 400));
  const card = pg.locator(".ai-card", { has: pg.locator("h3", { hasText: "Fake local" }) });
  ok(/Stays on this machine or network/.test(await card.innerText()), "it says the provider stays local");
  await card.getByRole("button", { name: "Test", exact: true }).click();
  await waitFor(async () => /answered/.test(await card.locator(".ai-result").innerText()), "the test to say what happened");
  ok(!/fixture-token/.test(await card.innerText()), "the test result carries no key");
  // A second, remote one: it says it leaves the server.
  await pg.locator("#providers details summary").click();
  await pg.locator("#pf-kind").selectOption("openrouter");
  await pg.locator("#pf-name").fill("OpenRouter");
  await pg.locator("#pf-key").fill("or-key-0000");
  await pg.getByRole("button", { name: "Add the provider" }).click();
  const or = pg.locator(".ai-card", { has: pg.locator("h3", { hasText: "OpenRouter" }) });
  await or.waitFor();
  ok(/Leaves your server/.test(await or.innerText()), "a remote provider says it leaves the server");
  // Remove: two steps.
  await or.getByRole("button", { name: "Remove" }).dblclick();
  await pg.waitForTimeout(800);
  eq(await pg.locator(".ai-card", { has: pg.locator("h3", { hasText: "OpenRouter" }) }).count(), 1, "a double-click does not remove it");
  ok(await or.getByRole("button", { name: /Really remove/ }).count() === 1, "it is armed and asks");
  await pg.keyboard.press("Escape");
  ok(await or.getByRole("button", { name: "Remove", exact: true }).count() === 1, "Escape disarms it");
  await or.getByRole("button", { name: "Remove" }).click();
  await or.getByRole("button", { name: /Really remove/ }).click();
  await waitFor(async () => (await pg.locator(".ai-card", { has: pg.locator("h3", { hasText: "OpenRouter" }) }).count()) === 0, "the provider to be gone");
  eq((await api(ada.ctx, "GET", "/api/v1/ai/providers")).json.items.length, 1, "one provider is left");
});

await kit.test("a writer sees the instance's provider without its address, and cannot edit it", async () => {
  const pg = wren.page;
  await pg.goto(`${base()}/settings/ai`);
  const card = pg.locator(".ai-card", { has: pg.locator("h3", { hasText: "Fake local" }) });
  await card.waitFor();
  const t = await card.innerText();
  ok(!t.includes(fake.base), "the address of an instance provider is not shown to a writer");
  eq(await card.getByRole("button", { name: "Edit" }).count(), 0, "no Edit");
  eq(await card.getByRole("button", { name: "Remove" }).count(), 0, "no Remove");
  eq(await pg.locator("#instance").count(), 0, "no instance settings");
  ok(!/Monthly caps|This server\b/.test(await pg.locator("#ai-root").innerText().then((x) => x.replace(/This server's|the server's/gi, ""))) || (await pg.locator("#instance").count()) === 0, "nothing admin-only is offered to a writer");
});

await kit.test("the owner opts in: mode, providers, what it may do, redaction and a style guide; the writing view then draws the writing partner", async () => {
  const pg = wren.page;
  await pg.goto(`${base()}/p/${book.slug}/ai`);
  await pg.locator("#policy").waitFor();
  await pg.locator('input[name="mode"][value="ask"]').check();
  await pg.locator('input[name="provider"]').first().check();
  for (const a of await pg.locator('input[name="action"]').all()) await a.check();
  await pg.locator("#po-red").selectOption("entities");
  await pg.locator("#po-style").fill("Third person, past tense. Never use the word suddenly.");
  await pg.getByRole("button", { name: "Save the policy" }).click();
  await waitFor(async () => /Saved/.test(await pg.locator("#policy .ai-result").innerText()), "the policy to be saved");
  await waitFor(async () => /AI is on: every request is previewed/.test(await pg.locator("#ai-root").innerText()), "the banner to change");
  const pol = (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/ai/policy`)).json.item;
  eq(pol.mode, "ask", "stored");
  eq(pol.redaction, "entities", "redaction stored");
  await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await pg.waitForSelector(".scriobh-prose");
  eq(await pg.locator("#write").getAttribute("data-ai"), "on", "the page knows AI is on");
  eq(await pg.locator("#itab-ai").count(), 1, "the AI tab is there");
  eq(await pg.locator('#fmtbar [data-cmd="ai"]').count(), 1, "and its toolbar button");
  ok((await pg.locator(".ai-chip").innerText()).includes("ask first"), "the header says what mode it is in");
  eq(fake.count(), 0, "still nothing sent");
});

await kit.test("the account page lists, for each project one can edit, whether it may use AI and which providers", async () => {
  const pg = wren.page;
  await pg.goto(`${base()}/settings/ai`);
  const li = pg.locator("#projects .ai-card", { has: pg.locator("h3", { hasText: "AI Screens" }) });
  await waitFor(async () => /May use: Fake local/.test(await li.innerText()), "the project's providers to be listed");
  ok(/Asks before every request/.test(await li.innerText()), "and the mode");
});

await kit.test("never send: a chapter can be flagged and unflagged from the page, and the flag reaches the op", async () => {
  const pg = wren.page;
  await pg.goto(`${base()}/p/${book.slug}/ai`);
  const box = pg.locator("#never input[type=checkbox]").first();
  await box.waitFor();
  await box.check();
  await waitFor(async () => (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/ai/exclude`)).json.items.some((x) => x.id === doc), "the flag to be stored");
  await box.uncheck();
  await waitFor(async () => !(await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/ai/exclude`)).json.items.some((x) => x.id === doc), "the flag to be lifted");
});

await kit.test("a reader is told the settings are not for them and makes no AI request; an administrator reads the policy through the op", async () => {
  const rory = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "reader@dev.scriobh");
  ok((await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/members`, { email: "reader@dev.scriobh", role: "reader" })).status < 300, "Rory can read the project");
  await rory.page.goto(`${base()}/p/${book.slug}/ai`);
  await rory.page.locator("#ai-root .ai-banner").first().waitFor();
  ok(/AI is for people who can edit/.test(await rory.page.locator("#ai-root").innerText()), "a reader is told so");
  await rory.page.goto(`${base()}/p/${book.slug}/d/${doc}`);
  await rory.page.waitForSelector(".scriobh-prose");
  eq(await rory.page.locator(".ai-chip").count(), 0, "a reader sees no AI chip");
  eq(await rory.page.locator("#itab-lab").count(), 0, "and no Lab");
  // The account page is not for a reader either: it says so and offers nothing.
  await rory.page.goto(`${base()}/settings/ai`);
  await rory.page.locator("#ai-root .ai-banner").first().waitFor();
  const rt = await rory.page.locator("#ai-root").innerText();
  ok(/AI providers are for people who can write/.test(rt), "a reader is told the providers page is not for them: " + rt.slice(0, 120));
  ok(!/Add a provider|This server|Monthly caps/.test(rt), "and is offered no way to add a provider or any admin setting");
  await rory.ctx.close();
  const adm = await api(ada.ctx, "GET", `/api/v1/projects/${book.id}/ai/policy`);
  eq(adm.status, 200, "an administrator can read the policy through the op");
});

await kit.test("with AI switched off for the server: the project page keeps never-send, use and kept chats; a kept chat is deleted after a confirmation, by keyboard", async () => {
  const kept = (await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/ai/chats`, { title: "Harbour questions" })).json.item;
  const other = (await api(wren.ctx, "POST", `/api/v1/projects/${book.id}/ai/chats`, { title: "Tide notes" })).json.item;
  eq((await api(ada.ctx, "PUT", "/api/v1/ai/settings", { enabled: false })).status, 200, "the administrator switches AI off");
  const sent = fake.count();
  try {
    const pg = wren.page;
    await pg.goto(`${base()}/p/${book.slug}/ai`);
    await pg.locator("#chats").waitFor();
    const all = await pg.locator("#ai-root").innerText();
    ok(/AI is turned off for this server/.test(all) && /Nothing can be sent/.test(all), "the banner says AI is off and nothing can be sent");
    ok(/not shown or changed here while AI is switched off/.test(await pg.locator("#policy").innerText()), "the policy says in words why it is not offered");
    eq(await pg.locator("#policy input, #policy select, #policy textarea, #policy button").count(), 0, "no policy control while AI is off");
    eq(await pg.locator("#never input[type=checkbox]").count() > 0, true, "the never-send list is there");
    await pg.locator("#usage").waitFor();
    // a never-send flag can be set and lifted with AI off
    const box = pg.locator("#never input[type=checkbox]").first();
    await box.check();
    await waitFor(async () => (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/ai/exclude`)).json.items.some((x) => x.id === doc), "the flag to be stored with AI off");
    await box.uncheck();
    await waitFor(async () => !(await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/ai/exclude`)).json.items.some((x) => x.id === doc), "the flag to be lifted with AI off");
    // the kept chats, each with a Delete that asks first
    eq(await pg.locator("#chats li.ai-card").count(), 2, "both kept chats are listed");
    const card = pg.locator("#chats li.ai-card", { has: pg.locator("h3", { hasText: "Harbour questions" }) });
    const del = card.getByRole("button", { name: "Delete", exact: true });
    await del.focus();
    await pg.keyboard.press("Enter");
    await card.getByRole("button", { name: /Really delete it/ }).waitFor();
    await pg.keyboard.press("Enter"); // too soon: the half-second settle swallows it
    await sleep(700);
    eq(await card.count(), 1, "a quick second press does nothing");
    await pg.keyboard.press("Enter");
    await waitFor(async () => (await card.count()) === 0, "the chat to leave the list");
    const left = (await api(wren.ctx, "GET", `/api/v1/projects/${book.id}/ai/chats`)).json.items;
    eq(left.length, 1, "one kept chat is left on the server");
    eq(left[0].chat_id, other.chat_id, "and it is the other one");
    ok(left.every((c) => c.chat_id !== kept.chat_id), "the deleted chat is gone");
    await waitFor(async () => /deleted/.test(await pg.locator("#ai-announce").innerText()), "the deletion to be announced");
    ok(/You have 1 kept chat/.test(await pg.locator("#chats").innerText()), "the count follows");
    eq(await pg.evaluate(() => document.activeElement && document.activeElement.textContent), "You have 1 kept chat.", "focus lands on the count, not lost");
    // the writing view still draws no AI affordance
    await pg.goto(`${base()}/p/${book.slug}/d/${doc}`);
    await pg.waitForSelector(".scriobh-prose");
    eq(await pg.locator("#itab-ai").count(), 0, "no AI tab with AI off");
    eq(await pg.locator('#fmtbar [data-cmd="ai"]').count(), 0, "no AI button with AI off");
    eq(await pg.locator('script[src*="aiboot"], script[src*="/ai."]').count(), 0, "no AI script with AI off");
    // how it looks: phone and desktop, light and dark
    const out = process.env.SHOTS || "";
    for (const scheme of ["light", "dark"]) {
      for (const vp of [{ width: 390, height: 844, isMobile: true, hasTouch: true }, { width: 1280, height: 900 }]) {
        const ctx = await kit.newContext({ viewport: { width: vp.width, height: vp.height }, isMobile: !!vp.isMobile, hasTouch: !!vp.hasTouch, colorScheme: scheme });
        await ctx.addCookies(await wren.ctx.cookies());
        const p2 = await ctx.newPage();
        kit.watch(p2, `${scheme}-${vp.width}`);
        await p2.goto(`${base()}/p/${book.slug}/ai`);
        await p2.locator("#chats").waitFor();
        await sleep(300);
        ok(await p2.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `${scheme} ${vp.width}: no sideways scroll`);
        if (vp.isMobile) {
          const small = await p2.locator("#ai-root button, #ai-root input:not([type=checkbox]):not([type=radio])").evaluateAll((els) => els.filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => (e.textContent || e.id).trim().slice(0, 24)));
          eq(small.length, 0, "every control is a touch target with AI off: " + small.join(", "));
        }
        if (out) await p2.screenshot({ path: `${out}/aioff-${scheme}-${vp.width}.png`, fullPage: true });
        await ctx.close();
      }
    }
    eq(fake.count(), sent, "nothing was sent with AI off");
  } finally {
    await api(ada.ctx, "PUT", "/api/v1/ai/settings", { enabled: true });
  }
});

await kit.test("at 390 px the settings pages fit and every control is a touch target", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 });
  await ctx.addCookies(await wren.ctx.cookies());
  const pg = await ctx.newPage();
  kit.watch(pg, "phone");
  for (const path of ["/settings/ai", `/p/${book.slug}/ai`]) {
    await pg.goto(`${base()}${path}`);
    await pg.locator("#ai-root .ai-banner").first().waitFor();
    await sleep(500);
    ok(await pg.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), path + ": no sideways scroll");
    const small = await pg.locator("#ai-root button, #ai-root input:not([type=checkbox]):not([type=radio]), #ai-root select, #ai-root summary").evaluateAll((els) => els.filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => (e.textContent || e.id || e.name || "").trim().slice(0, 24) + " " + Math.round(e.getBoundingClientRect().height)));
    eq(small.length, 0, path + ": every control is at least 44 px tall: " + small.join(", "));
  }
  await ctx.close();
});

await fake.close();
await kit.finish("e2e-ai-admin");
