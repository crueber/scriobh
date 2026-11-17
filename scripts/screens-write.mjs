#!/usr/bin/env node
// Screenshots of the writing view for docs/screens/phase-1: 1280x800 and 390x844
// (device pixel ratio 2), light and dark, with a real-looking chapter, the tree,
// Source mode, and each notice. Judge the look from these, not from the CSS.
//
//   node scripts/screens-write.mjs --out docs/screens/phase-1
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const out = resolve(repo, argv.includes("--out") ? argv[argv.indexOf("--out") + 1] : "docs/screens/phase-1");
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-shots-");
const srv = await startServer(build(tmp), join(tmp, "data"));
const base = srv.base;
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const prose = `The tide had gone out and taken the light with it. By four o'clock the harbour was a bowl of black mud, and the boats lay over on their sides like animals that had surrendered.

Máire stood at the end of the pier with the letter in her coat pocket and watched the gulls argue over something in the weed. She had read it three times on the train and once more on the walk down, and each time the same sentence turned over: *we cannot keep the house after the spring.*

## What the letter said

It was short, and it was kind, which made it worse. Conn had written it the way he mended nets, with **no wasted motion**, and signed it with the initial he used for the bank.

> The house was never ours to keep. It was ours to stay in.

Three things she knew for certain:

- the ferry stopped running on the first of March
- the Nolans had already taken the long field
- the [[Harbour Office]] kept the only copy of the deed

***

Behind her the town was putting its lights on, one window at a time. Conn would be at the pub by now, or at the harbour office with the ledgers open and a glass he had forgotten to drink.

<aside class="margin">Check the tide table for March before this scene is final.</aside>

The harbourmaster had left the gate unlocked again. She let herself through and listened to the boats settle.`;

const italicPara = `She had not said it aloud, not once, in all the years since: *I would rather have stayed on the island than be forgiven for leaving it.* The words sat in her like a stone in a shoe, small and constant and not quite impossible to walk on.`;

async function api(ctx, method, path, data) {
  const r = await ctx.request.fetch(base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return r.json();
}

async function newPage(ctx, who = "writer@dev.scriobh") {
  const p = await ctx.newPage();
  await signIn(p, base, who);
  return p;
}

const boot = await browser.newContext();
const bootPage = await boot.newPage();
await signIn(bootPage, base);
const book = (await api(boot, "POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan" })).item;
const first = (await api(boot, "GET", `/api/v1/projects/${book.id}/docs`)).order[0];
await api(boot, "PATCH", `/api/v1/docs/${first}`, { base_revision: 1, title: "The Last Ferry", content: "The ferry came in late, as it always did in March, with its one lamp swinging.\n" });
const mk = async (body) => (await api(boot, "POST", `/api/v1/projects/${book.id}/docs`, body)).item;
const p1 = await mk({ title: "The Shore", kind: "part" });
await api(boot, "POST", `/api/v1/docs/${first}/move`, { parent_id: p1.id });
const harbour = await mk({ title: "The Harbour at Low Tide", kind: "chapter", parent_id: p1.id, content: prose });
await mk({ title: "A House With Two Doors", kind: "chapter", parent_id: p1.id, content: "word ".repeat(2884) });
await mk({ title: "The kitchen", kind: "scene", parent_id: harbour.id, content: "word ".repeat(412) });
await mk({ title: "The pier", kind: "scene", parent_id: harbour.id, content: "word ".repeat(380) });
const p2 = await mk({ title: "The Crossing", kind: "part" });
await mk({ title: "Low Water", parent_id: p2.id, content: "word ".repeat(2206) });
await mk({ title: "The Ledger", parent_id: p2.id, content: "word ".repeat(1988) });
await mk({ title: "A Gate Left Open", parent_id: p2.id });
const ital = await mk({ title: "Italics, honestly", content: italicPara });
const empty = await mk({ title: "", kind: "chapter" });
const long = await mk({ title: "The Whole Winter", kind: "chapter", content: Array.from({ length: 15400 }, (_, i) => (i % 17 === 16 ? "tide." : "tide")).join(" ") });

const cookies = await boot.cookies();
const sizes = { desktop: { viewport: { width: 1280, height: 800 } }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true } };

async function ready(page, url) {
  await page.goto(base + url, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose");
  await page.evaluate(() => document.fonts.ready);
  await sleep(250);
}
// Only these are committed (the whole set is ~8 MB; the docs budget is about 3 MB). The rest go to a scratch folder.
const keep = new Set(["write-desktop-light", "write-desktop-dark", "source-desktop-light", "tree-collapsed-desktop-light", "conflict-desktop-light", "signedout-desktop-dark", "italic-desktop-light",
  "write-mobile-light", "write-mobile-dark", "drawer-mobile-light", "sheet-mobile-light", "fmtbar-keyboard-mobile-dark", "conflict-mobile-light", "locked-mobile-dark", "empty-mobile-light", "guard-mobile-light", "compare-desktop-light"]);
const extra = join(tmp, "extra");
mkdirSync(extra, { recursive: true });
const shot = async (page, name) => { await page.screenshot({ path: join(keep.has(name) || argv.includes("--all") ? out : extra, name + ".png") }); console.log("shot", name); };
const waitState = async (page, s) => { for (let i = 0; i < 100; i++) { if ((await page.locator("#save-state").getAttribute("data-state")) === s) return; await sleep(80); } throw new Error("never reached " + s); };

for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, deviceScaleFactor: 2, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    await ctx.addInitScript(() => {
      window.__csp = [];
      document.addEventListener("securitypolicyviolation", (e) => window.__csp.push(e.violatedDirective));
    });
    if (!opts.hasTouch === false || size === "mobile") {
      // No real keyboard in an emulator: a stand-in visual viewport the script can shrink.
      await ctx.addInitScript(() => {
        const vv = new EventTarget();
        Object.defineProperties(vv, { height: { get: () => window.innerHeight - (window.__kb || 0) }, width: { get: () => window.innerWidth }, offsetTop: { value: 0 } });
        Object.defineProperty(window, "visualViewport", { value: vv });
      });
    }
    await ctx.addInitScript(() => sessionStorage.setItem("scriobh:tab", "shots-main"));
    const page = await ctx.newPage();
    const tag = `${size}-${scheme}`;
    const doc = (id) => `/p/${book.slug}/d/${id}`;
    const desk = size === "desktop";

    // The chapter, with the caret in the prose.
    await ready(page, doc(harbour.id));
    if (desk) { await page.locator(".scriobh-prose p").nth(1).click(); }
    await shot(page, `write-${tag}`);

    // Selection counts and toolbar state (desktop).
    if (desk) {
      await page.locator(".scriobh-prose p").first().click({ clickCount: 3 });
      await sleep(200);
      await shot(page, `selection-${tag}`);
    }

    // Source mode.
    await page.keyboard.press("ControlOrMeta+e");
    await sleep(250);
    await shot(page, `source-${tag}`);
    await page.keyboard.press("ControlOrMeta+e");

    // The tree: expanded (default), collapsed.
    if (desk) {
      await page.locator("#tree .tree-row[data-kind=part]").first().focus();
      await page.keyboard.press("ArrowLeft");
      await page.keyboard.press("ArrowDown");
      await page.keyboard.press("ArrowDown");
      await sleep(150);
      await shot(page, `tree-collapsed-${tag}`);
      await page.keyboard.press("Home");
      await page.keyboard.press("ArrowRight");
    }

    // Empty chapter, italic paragraph.
    await ready(page, doc(empty.id));
    if (desk) { await page.locator("#doc-title").click(); }
    await shot(page, `empty-${tag}`);
    await ready(page, doc(ital.id));
    await shot(page, `italic-${tag}`);

    // Guard bar.
    await ready(page, doc(long.id));
    await sleep(300);
    await shot(page, `guard-${tag}`);

    // Mobile drawer, chapter sheet, formatting bar over the keyboard.
    if (!desk) {
      await ready(page, doc(harbour.id));
      await page.locator("#tree-toggle").tap();
      await sleep(400);
      await shot(page, `drawer-${tag}`);
      await page.keyboard.press("Escape");
      await sleep(300);
      await page.locator("#jump-open").tap();
      await sleep(400);
      await shot(page, `sheet-${tag}`);
      await page.keyboard.press("Escape");
      await sleep(200);
      // No real keyboard in an emulator: stand in for the visual viewport shrinking.
      await page.locator(".scriobh-prose").tap();
      await page.evaluate(() => { window.__kb = 300; window.visualViewport.dispatchEvent(new Event("resize")); });
      console.log("csp violations while following the keyboard:", JSON.stringify(await page.evaluate(() => window.__csp)));
      await sleep(200);
      await shot(page, `fmtbar-keyboard-${tag}`);
    }

    // Locked banner (a second tab holds the lease), conflict bar, signed-out message.
    const other = await ctx.newPage();
    await other.addInitScript((t) => sessionStorage.setItem("scriobh:tab", "other-" + t), tag);
    await ready(other, doc(first));
    await other.evaluate(() => scriobh.write.editor.focus()); // stays open: a closing tab releases its lease
    const me = await ctx.newPage();
    await me.addInitScript((t) => sessionStorage.setItem("scriobh:tab", "mine-" + t), tag);
    await ready(me, doc(first));
    await waitState(me, "locked").catch(() => {});
    await shot(me, `locked-${tag}`);
    await me.getByRole("button", { name: "Take over" }).click();
    await other.close();
    await waitState(me, "saved");
    const cur = await api(ctx, "GET", `/api/v1/docs/${first}`);
    await api(ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: cur.item.revision, content: "Someone changed this somewhere else " + tag + ".\n" });
    await me.locator(".scriobh-prose").click();
    await me.keyboard.press("ControlOrMeta+End");
    await me.keyboard.type(" A line typed here.");
    await waitState(me, "conflict");
    await shot(me, `conflict-${tag}`);
    await me.getByRole("button", { name: "Compare" }).click();
    await sleep(300);
    await shot(me, `compare-${tag}`);
    await me.getByRole("button", { name: "Keep theirs" }).click();
    await waitState(me, "saved");
    await me.locator(".scriobh-prose").click();
    await me.keyboard.type("Typed after the session ended.");
    await ctx.clearCookies();
    await me.locator(".scriobh-prose").click();
    await me.keyboard.type(" And more.");
    await waitState(me, "signedout");
    await sleep(300);
    await shot(me, `signedout-${tag}`);
    await ctx.close();
  }
}
await browser.close();
await srv.stop();
rm(tmp);
