#!/usr/bin/env node
// End-to-end checks for the U10 small fixes in the writing view and the library:
// the phone formatting bar's "›" covers no button; each Kind radio in the Compendium's New entry form sits on its own label's line;
// "Continue where you left off" puts the caret back where the writer left it (js/resume.js); and the screenshots the batch was
// reviewed from, when U10_SHOTS names a folder.
//
//   node scripts/e2e-ux10.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { join } from "node:path";

const kit = await createKit("e2e-ux10");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const shots = process.env.U10_SHOTS || "";
const shot = (pg, name) => (shots ? pg.screenshot({ path: join(shots, `${name}.png`) }) : null);
const { ctx: main, page } = await kit.signedIn();
kit.main = page;
let book, chapter;
// A fresh browser for the same person, signed in by copying the script's own session: a sign-in spends the per-address /auth/
// allowance (a burst of 10), and the steps below would use it up.
const session = async (opts, who) => {
  const ctx = await kit.newContext(opts);
  await ctx.addCookies(await main.cookies());
  const pg = await ctx.newPage();
  kit.watch(pg, who);
  await pg.goto(`${kit.base()}/`, { waitUntil: "load" });
  await pg.locator("body.signed-in").waitFor();
  return { ctx, pg };
};

const only = kit.only;
kit.only = ""; // the setup always runs, whichever step is named
await kit.test("setup: a chapter with an H2 and an H3 in its text, and a Compendium", async () => {
  book = (await api(main, "POST", "/api/v1/projects", { title: "UX Ten", kind: "novel" })).json.item;
  chapter = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "The Road", kind: "chapter", content: "Opening words here, and more of them.\n\n## In the chapter\n\nMore words.\n\n### Smaller\n\nEnd.\n" })).json.item;
  await api(main, "POST", "/api/v1/compendiums", { name: "Bible", project_id: book.id });
});
kit.only = only;

// The phone's formatting bar scrolls under its "›" button; no button may sit under it at rest, at any phone width.
for (const w of [360, 390]) {
  await kit.test(`phone formatting bar: the "›" covers no button at ${w} px`, async () => {
    const { ctx, pg } = await session({ viewport: { width: w, height: 844 }, isMobile: true, hasTouch: true }, "phone");
    await pg.goto(`${kit.base()}/p/${book.slug}/d/${chapter.id}`);
    await pg.waitForSelector(".scriobh-prose");
    // The strip is behind Aa on a phone (U11.3): closed, only Undo and Redo show and there is no "›"; Aa opens the whole strip, which is what this checks.
    eq(await pg.evaluate(() => { const m = document.querySelector(".fmt-more"); return !!m && !m.hidden; }), false, "the closed strip has no more button");
    await waitFor(() => pg.evaluate(() => performance.getEntriesByType("resource").some((r) => /\/js\/phone\./.test(r.name))), "the phone script", 8000);
    await pg.locator("#fmt-aa").tap();
    await waitFor(() => pg.evaluate(() => { const m = document.querySelector(".fmt-more"); return !!m && !m.hidden; }), "the more button to show", 8000);
    // The part of each button the bar shows (the track clips the rest) must not reach under the "›".
    const hits = await pg.evaluate(() => {
      const m = document.querySelector(".fmt-more").getBoundingClientRect();
      const track = (document.querySelector(".fmt-track") || document.getElementById("fmtbar")).getBoundingClientRect();
      return [...document.querySelectorAll("#fmtbar .tb")].filter((b) => b.getBoundingClientRect().width > 0).filter((b) => {
        const r = b.getBoundingClientRect();
        const left = Math.max(r.left, track.left), right = Math.min(r.right, track.right);
        return right > left && right > m.left + 0.5 && left < m.right - 0.5;
      }).map((b) => b.textContent.trim() || b.getAttribute("aria-label"));
    });
    eq(hits.length, 0, `buttons under the "›": ${hits.join(", ")}`);
    if (w === 390) { await shot(pg, "phone-390-light"); await pg.emulateMedia({ colorScheme: "dark" }); await shot(pg, "phone-390-dark"); }
    await ctx.close();
  });
}

// Each Kind is a radio with its own label on one line: the input is before its word, on the same row.
for (const w of [1280, 390]) {
  await kit.test(`New entry form: each Kind radio sits on its own label's line at ${w} px`, async () => {
    const { ctx, pg } = await session({ viewport: { width: w, height: 800 } }, "entry");
    await pg.goto(`${kit.base()}/p/${book.slug}/d/${chapter.id}`);
    await pg.waitForSelector(".scriobh-prose");
    await pg.evaluate(() => window.scriobh.inspector.open("compendium", true));
    await pg.locator("#ipanel-compendium [hx-get*='new='], #lk-main [hx-get*='new=']").first().waitFor({ timeout: 10000 });
    await pg.locator("#ipanel-compendium [hx-get*='new='], #lk-main [hx-get*='new=']").first().click();
    await pg.locator(".lk-newform .radio").first().waitFor({ timeout: 8000 });
    await sleep(300);
    const rows = await pg.locator(".lk-newform .radio").evaluateAll((els) => els.map((e) => {
      const i = e.querySelector("input").getBoundingClientRect(), s = e.querySelector("span").getBoundingClientRect();
      return { word: e.textContent.trim(), sameRow: Math.abs(i.top + i.height / 2 - (s.top + s.height / 2)) < 10, before: i.right <= s.left + 1 };
    }));
    eq(rows.length, 5, "one radio per kind");
    const split = rows.filter((r) => !r.sameRow || !r.before).map((r) => r.word);
    eq(split.length, 0, `radios apart from their words: ${split.join(", ")}`);
    await shot(pg, `new-entry-${w}-light`);
    if (w === 1280) { await pg.emulateMedia({ colorScheme: "dark" }); await shot(pg, `new-entry-${w}-dark`); }
    await ctx.close();
  });
}

// "Continue where you left off" on the library goes through the caret restore: the writer's caret comes back, not the top.
await kit.test("Continue where you left off puts the caret back where it was left", async () => {
  const pg = page;
  await pg.goto(`${kit.base()}/p/${book.slug}/d/${chapter.id}`);
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor && window.scriobh.write.editor.view)), "the editor", 10000);
  const at = 12;
  await pg.evaluate((p) => {
    const v = window.scriobh.write.editor.view;
    v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.near(v.state.doc.resolve(p))));
    v.focus();
  }, at);
  await sleep(300);
  await pg.goto(`${kit.base()}/`);
  await pg.locator("a.continue").waitFor({ timeout: 10000 });
  await pg.locator("a.continue").click();
  await pg.waitForURL(/\/d\//);
  await waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.write.editor && window.scriobh.write.editor.view)), "the editor again", 10000);
  const head = await waitFor(async () => {
    const h = await pg.evaluate(() => window.scriobh.write.editor.view.state.selection.head);
    return h === at ? h : null;
  }, `the caret at ${at}`, 6000).catch(async () => pg.evaluate(() => window.scriobh.write.editor.view.state.selection.head));
  eq(head, at, "the caret after Continue");
});

await kit.finish("e2e-ux10");
