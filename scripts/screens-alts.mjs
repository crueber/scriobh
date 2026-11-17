#!/usr/bin/env node
// Screenshots of the Phase 3.5 writing view for docs/screens/phase-3.5: marks in the text with the cycler, the Alternatives
// panel, the add form, ghosted text beside the Stash panel, and the export options. Desktop light, desktop dark and mobile
// light only.
//
//   node scripts/screens-alts.mjs [--out docs/screens/phase-3.5]
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { createKit } from "./e2e-kit.mjs";
import { repo } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-3.5");
mkdirSync(out, { recursive: true });
const kit = await createKit("screens-alts");
const { api, sleep } = kit;
const { ctx: main, page: p0 } = await kit.signedIn();
const para = (s) => s + "\n\n";
const BODY = para("The harbour was quiet that morning. Marta counted the boats that had not come in, and then she counted them again.") +
  para("Nobody had slept. The lamp on the quay burned on, and the gulls sat along the wall like a jury.") +
  para("The tide turned twice before noon, and by the second turning she had stopped pretending that the number would change.") +
  para("Then she saw the light, low and green, where no boat should be. She did not tell her father what she had seen.") +
  para("By evening the whole quay knew.");

const book = (await api(main, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000 })).json.item;
const doc = (await api(main, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
let rev = (await api(main, "GET", `/api/v1/docs/${doc}`)).json.item.revision;
rev = (await api(main, "PATCH", `/api/v1/docs/${doc}`, { base_revision: rev, title: "The Quiet Harbour", content: BODY })).json.item.revision;
const alt = async (body) => { const r = await api(main, "POST", `/api/v1/docs/${doc}/alternatives`, { base_revision: rev, ...body }); if (r.status >= 300) throw new Error(JSON.stringify(r.json)); rev = r.json.revision; return r.json; };
await alt({ scope: "word", quote: "quiet", before: "was ", after: " that", variants: ["still", "hushed"] });
await alt({ scope: "sentence", quote: "Nobody had slept.", variants: ["Not one of them had slept.", "No one had slept a minute."] });
await alt({ scope: "word", quote: "turned", variants: ["swung", "came round"] });
await alt({ scope: "paragraph", quote: "By evening the whole quay knew.", variants: ["By evening everyone on the quay knew.", "The quay knew by evening."] });
const g = await api(main, "POST", `/api/v1/docs/${doc}/ghosts`, { base_revision: rev, ranges: [{ quote: " She did not tell her father what she had seen.", before: "where no boat should be." }] });
if (g.status >= 300) throw new Error("ghost " + JSON.stringify(g.json));
await api(main, "POST", `/api/v1/projects/${book.id}/stash`, { title: "The father's silence", kind: "paragraph", content: "Her father said nothing for a long while. When he did speak, it was about the weather.", tags: ["father", "cut"] });
await api(main, "POST", `/api/v1/projects/${book.id}/stash`, { title: "Opening, first try", kind: "paragraph", content: "Before the sun was up Marta was already on the quay with her book of numbers.", tags: ["opening"] });
await api(main, "POST", `/api/v1/projects/${book.id}/stash`, { title: "Chapter notes", kind: "note", content: "The green light returns in chapter nine. Do not explain it here.", tags: [] });
await p0.close();

const sizes = [
  ["desktop-light", { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, "light"],
  ["desktop-dark", { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, "dark"],
  ["mobile-light", { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 }, "light"],
];
const cookies = await main.cookies();
for (const [tag, opts, scheme] of sizes) {
  const ctx = await kit.newContext({ ...opts, colorScheme: scheme });
  await ctx.addCookies(cookies);
  const page = await ctx.newPage();
  kit.watch(page, tag);
  const mobile = tag.startsWith("mobile");
  const shot = async (name) => { await sleep(500); await page.screenshot({ path: join(out, `${name}-${tag}.png`) }); console.log("shot", `${name}-${tag}`); };
  const load = async () => {
    await page.goto(`${kit.base()}/p/${book.slug}/d/${doc}`, { waitUntil: "load" });
    await page.waitForSelector(".scriobh-prose .scriobh-alt", { timeout: 20000 });
    await page.evaluate(() => document.fonts.ready);
    await sleep(900);
  };
  await load();
  // 1. The marks in the text, and the cycler over one of them.
  await page.evaluate(() => window.scriobh.inspector.close());
  if (mobile) await page.locator(".scriobh-prose .scriobh-alt").first().tap(); else await page.locator(".scriobh-prose .scriobh-alt").first().click();
  await page.locator("#alt-cyc").waitFor({ timeout: 5000 });
  await shot("alts-cycler");
  // 2. The Alternatives panel.
  await page.keyboard.press("Escape");
  await page.evaluate(() => window.scriobh.inspector.open("alts", false));
  await page.locator("#ipanel-alts .ap-card").first().waitFor();
  await shot("alts-panel");
  await page.evaluate(() => window.scriobh.inspector.close());
  // 3. The add form on a selected word.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; let hit; v.state.doc.descendants((n, p) => { if (!hit && n.isText && n.text.includes("lamp")) hit = [p + n.text.indexOf("lamp"), p + n.text.indexOf("lamp") + 4]; }); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); v.focus(); });
  await sleep(200);
  await page.evaluate(() => window.scriobh.alts.cmd("alt"));
  await page.locator("#alt-new").waitFor({ timeout: 5000 });
  await page.keyboard.type("lantern");
  await shot("alts-add");
  await page.keyboard.press("Escape");
  // 4. The ghosted sentence beside the Stash panel.
  await page.evaluate(() => { document.querySelector(".scriobh-ghost") && document.querySelector(".scriobh-ghost").scrollIntoView({ block: "center" }); window.scriobh.inspector.open("stash", false); });
  await page.locator("#ipanel-stash .ap-card").first().waitFor();
  await shot("stash-ghost");
  await ctx.close();
}
// 5. The export options (desktop light only matters for the page, but all three sizes are taken).
for (const [tag, opts, scheme] of sizes) {
  const ctx = await kit.newContext({ ...opts, colorScheme: scheme });
  await ctx.addCookies(cookies);
  const page = await ctx.newPage();
  kit.watch(page, tag);
  await page.goto(`${kit.base()}/ui/projects/${book.id}/export`, { waitUntil: "load" });
  await page.waitForSelector("#ex-ghosts");
  await page.evaluate(() => document.fonts.ready);
  await sleep(500);
  await page.screenshot({ path: join(out, `export-options-${tag}.png`), fullPage: true });
  console.log("shot", `export-options-${tag}`);
  await ctx.close();
}
await kit.finish("screens-alts");
