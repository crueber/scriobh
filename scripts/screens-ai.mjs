#!/usr/bin/env node
// Screenshots of the Phase 4 screens for docs/screens/phase-4: the provider settings, a project's AI page, the context preview,
// a proposal with its word diff, the Lab with marks in the text, the chat and the reviews. Against a fake provider; desktop
// light, desktop dark and mobile light only.
//
//   node scripts/screens-ai.mjs [--out docs/screens/phase-4]
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { createKit } from "./e2e-kit.mjs";
import { startFake } from "./fake-ai.mjs";
import { repo } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-4");
mkdirSync(out, { recursive: true });
const kit = await createKit("screens-ai", { env: { SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000" } });
const { api, sleep, waitFor } = kit;
const fake = await startFake();
const { ctx: main, page: p0 } = await kit.signedIn();
const { ctx: adaCtx, page: p1 } = await kit.signedIn({ viewport: { width: 1000, height: 800 } }, "admin@dev.scriobh");
const para = (s) => s + "\n\n";
const BODY = para("The harbour was very quiet that morning. Marta counted the boats that had not come in, and then she counted them again.") +
  para("Nobody had slept. The lamp on the quay burned on, and the gulls sat along the wall like a jury.") +
  para("The tide turned twice before noon, and by the second turning she had stopped pretending, perhaps, that the number would change.  Then she saw the light, low and green, where no boat should be.") +
  para("By evening the whole quay knew.");
const pv = await api(adaCtx, "POST", "/api/v1/ai/providers", { kind: "openai_compatible", name: "Studio model", base_url: fake.base, local: true, default_model: "llama3.1:8b", scope: "instance" });
if (pv.status >= 300) throw new Error("provider " + JSON.stringify(pv.json));
const book = (await api(main, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000 })).json.item;
const doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
let rev = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item.revision;
await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, title: "The Quiet Harbour", content: BODY });
await api(main, "PUT", `/api/v1/projects/${book.id}/ai/policy`, { mode: "ask", providers: [pv.json.item.id], actions: ["selection", "alternatives", "chat", "critique", "summaries", "continuity", "extraction", "lab"], redaction: "entities" });
await api(main, "PUT", `/api/v1/ai/budget`, { scope: "project", subject: book.id, max_tokens: 200000, max_cost_micros: 0 }).catch(() => {});
await p0.close(); await p1.close();

const sizes = [
  ["desktop-light", { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, "light"],
  ["desktop-dark", { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, "dark"],
  ["mobile-light", { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 }, "light"],
];
const cookies = await main.cookies(), adaCookies = await adaCtx.cookies();
const select = (pg, s) => pg.evaluate((s) => {
  const v = window.scriobh.write.editor.view; let hit = null;
  v.state.doc.descendants((n, pos) => { if (hit || !n.isText) return; const i = n.text.indexOf(s); if (i >= 0) hit = [pos + i, pos + i + s.length]; });
  v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); v.focus();
}, s);
let reviewed = false;
for (const [tag, opts, scheme] of sizes) {
  const mobile = tag.startsWith("mobile");
  const ctx = await kit.newContext({ ...opts, colorScheme: scheme });
  await ctx.addCookies(cookies);
  const actx = await kit.newContext({ ...opts, colorScheme: scheme });
  await actx.addCookies(adaCookies);
  const page = await ctx.newPage(), apage = await actx.newPage();
  kit.watch(page, tag); kit.watch(apage, tag + "-admin");
  const shot = async (pg, name, full = false) => { await sleep(500); await pg.screenshot({ path: join(out, `${name}-${tag}.png`), fullPage: full }); console.log("shot", `${name}-${tag}`); };
  // 1. The provider settings, as the administrator.
  await apage.goto(`${kit.base()}/settings/ai`, { waitUntil: "load" });
  await apage.waitForSelector("#ai-root:not([data-loading]) .ai-card");
  await apage.evaluate(() => document.fonts.ready);
  await shot(apage, "ai-providers", true);
  // 2. The project's AI page.
  await page.goto(`${kit.base()}/p/${book.slug}/ai`, { waitUntil: "load" });
  await page.waitForSelector("#ai-root:not([data-loading]) form, #ai-root:not([data-loading]) h2");
  await page.evaluate(() => document.fonts.ready);
  await shot(page, "ai-project", true);
  // The writing view.
  const load = async () => {
    await page.goto(`${kit.base()}/p/${book.slug}/d/${doc}`, { waitUntil: "load" });
    await page.waitForSelector(".scriobh-prose");
    await waitFor(() => page.evaluate(() => !!(window.scriobh.alts && window.scriobh.write)), "the view", 15000);
    await page.evaluate(() => document.fonts.ready);
    await sleep(700);
  };
  await load();
  // 3. The context preview.
  await page.evaluate(() => window.scriobh.inspector.close());
  await select(page, "The harbour was very quiet that morning.");
  await page.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:ai", { detail: "menu" })));
  await page.locator("dialog.ai-menu").waitFor();
  await page.locator("dialog.ai-menu").getByRole("button", { name: "Tighten" }).click();
  const dlg = page.locator("dialog.ai-dlg:not(.ai-menu)");
  await dlg.waitFor();
  await waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload", 15000);
  await shot(page, "ai-preview");
  // 4. The proposal with its word diff.
  await dlg.getByRole("button", { name: /^Send/ }).click();
  await page.locator("#ai-pop .ai-diff").waitFor();
  await shot(page, "ai-proposal");
  await page.locator("#ai-pop").getByRole("button", { name: /^Reject/ }).click();
  await page.locator("#ai-pop").waitFor({ state: "detached" });
  // 5. The Lab: marks in the text and the list.
  await page.evaluate(() => window.scriobh.inspector.open("lab", true));
  await page.locator("#ipanel-lab .lab").waitFor();
  await page.getByRole("button", { name: "Check this chapter" }).click();
  await page.waitForSelector("#ipanel-lab .ai-rv", { timeout: 15000 });
  await page.waitForSelector(".scriobh-prose .lab-mark", { timeout: 15000 });
  await shot(page, "ai-lab");
  // 6. The chat, with one exchange.
  fake.say("The slow part is the first paragraph: it counts the boats twice before anything is at stake. If the light comes sooner, the counting reads as dread and not as inventory.");
  await page.evaluate(() => window.scriobh.inspector.open("ai", true));
  await page.locator("#ipanel-ai .ai-subtabs").waitFor();
  await page.locator("#aitab-chat").click();
  await page.locator("#aic-msg").fill("Is the opening too slow?");
  await page.locator("#ipanel-ai").getByRole("button", { name: /^Send/ }).click();
  const cd = page.locator("dialog.ai-dlg:not(.ai-menu)");
  await cd.waitFor();
  await waitFor(async () => (await cd.locator("pre.ai-pay").count()) > 0, "the payload", 15000);
  await cd.getByRole("button", { name: /^Send/ }).click();
  await page.locator('#ipanel-ai .ai-msg[data-role="assistant"] small:not(:empty)').waitFor({ timeout: 15000 }).catch(() => {});
  await waitFor(async () => /light comes sooner/.test(await page.locator("#ipanel-ai").innerText()), "the answer", 15000);
  await shot(page, "ai-chat");
  fake.say(null);
  // 7. The reviews: a critique, once.
  await page.locator("#aitab-reviews").click();
  if (!reviewed) {
    await page.getByRole("button", { name: "Critique this chapter" }).click();
    const rd = page.locator("dialog.ai-dlg:not(.ai-menu)");
    await rd.waitFor();
    await waitFor(async () => (await rd.locator("pre.ai-pay").count()) > 0, "the payload", 15000);
    await rd.getByRole("button", { name: /^Send/ }).click();
    await page.locator("#ipanel-ai .ai-rv").first().waitFor({ timeout: 15000 });
    reviewed = true;
  } else {
    await page.locator("#ipanel-ai .ai-rv").getByRole("button", { name: "Open" }).first().click();
    await page.locator("#ipanel-ai .ai-rv").first().waitFor();
  }
  await shot(page, "ai-reviews");
  await ctx.close(); await actx.close();
}
await fake.close();
await kit.finish("screens-ai");
