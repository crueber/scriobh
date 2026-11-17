#!/usr/bin/env node
// End-to-end test of research and the scratchpad in a real Chromium, for a project and for a Compendium:
// folders, notes and clippings; moves by button; trash and restore; tags and the tag filter; the board by
// keyboard; a save that meets newer text (both choices); a draft that survives a failed save; the clipping
// click-through; search; the private pad (autosave, reload, conflict, sign-out flush); a reader's read-only
// view; and the phone viewport.
//
//   node scripts/e2e-research.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { signIn } from "./harness.mjs";

const kit = await createKit("e2e-research");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const P = (await api(main, "POST", "/api/v1/projects", { title: "Tidewater research", kind: "novel" })).json.item;
const C = (await api(main, "POST", "/api/v1/compendiums", { name: "Tidewater Bible" })).json.item;
const owners = [
  { name: "project", page: `/p/${P.slug}`, api: `/api/v1/projects/${P.id}`, plural: "projects", id: P.id },
  { name: "Compendium", page: `/c/${C.slug}`, api: `/api/v1/compendiums/${C.id}`, plural: "compendiums", id: C.id },
];

// A reader on both, who has signed in once so that they exist.
const rctx = await kit.newContext({ viewport: { width: 1280, height: 800 } });
const rpage = await rctx.newPage();
kit.watch(rpage, "reader");
await signIn(rpage, base(), "reader@dev.scriobh");
for (const o of owners) {
  const r = await api(main, "POST", `${o.api}/members`, { email: "reader@dev.scriobh", role: "reader" });
  ok(r.status < 300, `adding a reader to the ${o.name}: ${r.status} ${JSON.stringify(r.json)}`);
}

// A failure says where the page was and what it said, so a selector that missed can be read at a glance.
const test = (name, fn) => kit.test(name, async () => {
  try { await fn(); } catch (e) {
    const t = await pg.locator("body").innerText().catch(() => "");
    const st = await pg.locator("#pad-state").innerText({ timeout: 500 }).catch(() => "");
    e.message += ` || URL ${pg.url()} || STATE ${st} || PAGE ${t.slice(0, 300).replace(/\s+/g, " ")}`;
    throw e;
  }
});
const note = async (o, body) => {
  const r = await api(main, "POST", `${o.api}/research`, body);
  if (!r.json || !r.json.item) throw new Error(`creating ${JSON.stringify(body)}: ${r.status} ${JSON.stringify(r.json)}`);
  return r.json.item;
};
const get = async (o, id) => (await api(main, "GET", `${o.api}/research/${id}`)).json.item;
const state = (page) => page.locator("#pad-state");
const saved = (page) => waitFor(async () => /^Saved at/.test(await state(page).innerText()), "the page to say Saved");
const add = async (page, kind, title, o) => {
  if (!/\/research$/.test(new URL(page.url()).pathname)) await page.goto(`${base()}${o.page}/research`);
  await page.locator("#r-add-kind").selectOption(kind);
  await page.locator("#r-add-title").fill(title);
  await page.getByRole("button", { name: "Add", exact: true }).click();
};

for (const o of owners) {
  await test(`${o.name}: folders, notes and clippings; moves by button keep focus; trash and restore`, async () => {
    await pg.goto(`${base()}${o.page}/research`);
    ok(/No research in this/.test(await pg.locator("#research").innerText()), "no honest empty state");
    await add(pg, "folder", "Tides", o);
    await pg.waitForSelector(".saved");
    await add(pg, "note", "Tide tables", o);
    await add(pg, "clipping", "A harbour master's diary", o);
    // Creating a note or a clipping opens it; go back to the list.
    await pg.goto(`${base()}${o.page}/research`);
    eq(await pg.locator(".r-row").count(), 3, "rows after three adds");
    await pg.getByRole("button", { name: "Move into the folder above Tide tables" }).click();
    await pg.waitForSelector(".r-row.lv1");
    ok(/1 inside/.test(await pg.locator("#r-tree").innerText()), "the folder does not say what is in it");
    ok(await pg.evaluate(() => document.activeElement && document.activeElement.id.startsWith("n-")), "focus did not return to the moved row");
    await pg.getByRole("button", { name: "Move out of its folder Tide tables" }).click();
    await pg.waitForSelector(".r-row.lv0");
    await pg.getByRole("button", { name: "Move into the folder above Tide tables" }).click();
    await pg.waitForSelector(".r-row.lv1");
    await pg.getByRole("button", { name: "Move Tides to the trash" }).click();
    await pg.waitForSelector(".saved");
    ok(/trash/i.test(await pg.locator(".saved").innerText()), "the trash message: " + (await pg.locator(".saved").innerText()));
    await pg.getByRole("link", { name: "Trash", exact: true }).first().click();
    await pg.waitForSelector(".r-row");
    ok(/Tides/.test(await pg.locator("#r-tree").innerText()) && /Tide tables/.test(await pg.locator("#r-tree").innerText()), "the trash lacks the folder and what was in it");
    await pg.getByRole("button", { name: "Restore Tides" }).click();
    await pg.waitForSelector(".saved");
    await pg.goto(`${base()}${o.page}/research`);
    ok(/Tide tables/.test(await pg.locator("#r-tree").innerText()), "restore did not bring the note back");
  });

  await test(`${o.name}: tags with colours, the tag filter, rename, merge and delete`, async () => {
    kit.allowed = [/status of 422/]; // the duplicate name below is refused on purpose
    await pg.goto(`${base()}${o.page}/research/tags`);
    await pg.locator("#t-name").fill("harbour");
    await pg.getByLabel("Teal").check();
    await pg.getByRole("button", { name: "Add the tag" }).click();
    await pg.waitForSelector(".saved");
    await pg.locator("#t-name").fill("harbour");
    await pg.getByRole("button", { name: "Add the tag" }).click();
    ok(/already|exists|used/i.test(await pg.locator("#r-error").innerText()), "the duplicate tag message: " + (await pg.locator("#r-error").innerText()));
    await pg.locator("#t-name").fill("weather");
    await pg.getByLabel("Plum").check();
    await pg.getByRole("button", { name: "Add the tag" }).click();
    await pg.waitForSelector(".saved");
    const body = await pg.locator("#research").innerText();
    ok(/harbour/.test(body) && /Teal/.test(body) && /weather/.test(body) && /Plum/.test(body), "a tag does not show its name beside its colour word");
    // Tag a note on its own page, then filter the list by that tag.
    const [folder] = (await api(main, "GET", `${o.api}/research`)).json.items.filter((i) => i.title === "Tide tables");
    await pg.goto(`${base()}${o.page}/research/${folder.id}`);
    await pg.locator("#n-tags").fill("harbour, weather");
    await pg.getByRole("button", { name: "Save", exact: true }).click();
    await pg.waitForSelector(".saved");
    await pg.goto(`${base()}${o.page}/research`);
    await pg.locator("#r-tag").selectOption({ label: "harbour (1)" });
    await pg.getByRole("button", { name: "Show", exact: true }).click();
    await pg.waitForSelector(".r-row");
    eq(await pg.locator(".r-row").count(), 1, "rows with the harbour tag");
    ok(await pg.locator(".r-row .tag", { hasText: "harbour" }).count() > 0, "the tag chip is missing in the list");
    // Rename and recolour, merge, delete.
    await pg.goto(`${base()}${o.page}/research/tags`);
    await pg.locator("summary", { hasText: "Change weather" }).click();
    await pg.locator('input[id^="tn-"]').last().fill("storms");
    await pg.getByRole("button", { name: "Save", exact: true }).last().click();
    await pg.waitForSelector(".saved");
    ok(/storms/.test(await pg.locator("#research").innerText()), "the rename did not show");
    await pg.locator("summary", { hasText: "Change storms" }).click();
    await pg.locator('select[id^="tm-"]').last().selectOption({ label: "harbour" });
    await pg.getByRole("button", { name: "Merge" }).last().click();
    await pg.waitForSelector(".saved");
    ok(!/storms/.test(await pg.locator(".t-list").innerText()), "the merged tag is still listed");
    await pg.locator("summary", { hasText: "Change harbour" }).click();
    await pg.getByRole("button", { name: "Delete the tag harbour" }).click();
    await pg.waitForSelector(".saved");
    ok(/Tide tables/.test(await (await pg.goto(`${base()}${o.page}/research`)).text()), "deleting a tag removed a note");
  });

  await test(`${o.name}: a note saves by itself and after a reload; the preview comes from the server`, async () => {
    const n = await note(o, { title: "Autosaved", body: "start" });
    await pg.goto(`${base()}${o.page}/research/${n.id}`);
    await pg.locator("#n-body").fill("First line.\n\nA **kept** line.");
    await saved(pg);
    await pg.reload();
    eq(await pg.locator("#n-body").inputValue(), "First line.\n\nA **kept** line.", "the text after a reload");
    eq((await get(o, n.id)).body, "First line.\n\nA **kept** line.", "the server's text");
    await pg.getByRole("button", { name: "Preview" }).click();
    await pg.waitForSelector("#n-preview strong");
    ok(!(await pg.locator("#n-body").isVisible()), "the box stays while the preview shows");
    await pg.getByRole("button", { name: "Edit" }).click();
    ok(await pg.locator("#n-body").isVisible(), "Edit did not bring the box back");
    // Saving does not move the caret or the focus while typing.
    await pg.locator("#n-body").focus();
    await pg.keyboard.press("End");
    await pg.keyboard.type(" And more.");
    await saved(pg);
    ok(await pg.evaluate(() => document.activeElement.id === "n-body"), "focus left the box during an autosave");
  });

  await test(`${o.name}: a save that meets newer text keeps the person's words; Keep mine and Use theirs`, async () => {
    kit.allowed = [/status of 409/];
    const n = await note(o, { title: "Contested", body: "base" });
    for (const choice of ["Keep mine", "Use theirs"]) {
      const cur = await get(o, n.id);
      await pg.goto(`${base()}${o.page}/research/${n.id}`);
      // Someone else saves while this page is open.
      const r = await api(main, "PATCH", `${o.api}/research/${n.id}`, { base_revision: cur.revision, body: `theirs for ${choice}` });
      eq(r.status, 200, "the other save");
      await pg.locator("#n-body").fill(`mine for ${choice}`);
      await pg.locator(".r-bar").waitFor({ timeout: 8000 });
      ok(/changed somewhere else/.test(await pg.locator(".r-bar").innerText()), "the conflict bar: " + (await pg.locator(".r-bar").innerText()));
      eq(await pg.locator("#n-body").inputValue(), `mine for ${choice}`, "the person's text while the bar is up");
      eq((await get(o, n.id)).body, `theirs for ${choice}`, "nothing was overwritten");
      await pg.getByRole("button", { name: choice }).click();
      if (choice === "Keep mine") {
        await saved(pg);
        eq((await get(o, n.id)).body, "mine for Keep mine", "Keep mine saved");
      } else {
        await waitFor(async () => (await pg.locator("#n-body").inputValue()) === "theirs for Use theirs", "the other version to fill the box");
        eq((await get(o, n.id)).body, "theirs for Use theirs", "Use theirs left the server alone");
      }
    }
    // Without script the same thing is a plain page: both versions shown, the person's text in the form.
    const cur = await get(o, n.id);
    const bare = await kit.newContext({ javaScriptEnabled: false, viewport: { width: 1280, height: 800 } });
    await bare.addCookies(await main.cookies());
    const np = await bare.newPage();
    await np.goto(`${base()}${o.page}/research/${n.id}`);
    await api(main, "PATCH", `${o.api}/research/${n.id}`, { base_revision: cur.revision, body: "another change" });
    await np.locator("#n-body").fill("typed without script");
    await np.getByRole("button", { name: "Save", exact: true }).click();
    await np.locator("#r-conflict").waitFor();
    eq(await np.locator("#n-body").inputValue(), "typed without script", "the person's text without script");
    ok(/another change/.test(await np.locator("#r-conflict").innerText()), "the other version without script");
    await np.getByRole("button", { name: "Save", exact: true }).click();
    await np.waitForSelector(".saved");
    eq((await get(o, n.id)).body, "typed without script", "Keep mine without script");
    await bare.close();
  });

  await test(`${o.name}: a failed save keeps the text in this browser and offers it back after a reload`, async () => {
    const n = await note(o, { title: "Offline-ish", body: "server text" });
    await pg.goto(`${base()}${o.page}/research/${n.id}`);
    await pg.evaluate(() => { window.fetch = () => Promise.reject(new TypeError("Failed to fetch")); });
    await pg.locator("#n-body").fill("typed while the server was out of reach");
    await waitFor(async () => /kept in this browser/.test(await state(pg).innerText()), "the page to say Not saved and where the text is");
    ok(/kept in this browser/.test(await state(pg).innerText()), "the state does not say where the text is: " + (await state(pg).innerText()));
    const kept = await pg.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("scriobh:research:")).length);
    eq(kept, 1, "local drafts");
    await pg.reload();
    eq(await pg.locator("#n-body").inputValue(), "server text", "a reload never silently replaces the server's text");
    await pg.locator(".r-bar").waitFor();
    await pg.getByRole("button", { name: "Put it back" }).click();
    eq(await pg.locator("#n-body").inputValue(), "typed while the server was out of reach", "the draft after Put it back");
    await pg.locator("#n-body").press("End");
    await pg.keyboard.type(".");
    await saved(pg);
    eq((await get(o, n.id)).body, "typed while the server was out of reach.", "the saved text");
    eq(await pg.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("scriobh:research:")).length), 0, "the draft is cleared once the server has the text");
  });

  await test(`${o.name}: a clipping's address is text; opening it takes a warning and a second click`, async () => {
    const n = await note(o, { kind: "clipping", title: "Diary", source_url: "https://example.org/diary", retrieved_on: "2026-03-14", quote: "The sea was quiet.", body: "my notes" });
    const seen = [];
    await main.route("https://example.org/**", (r) => { seen.push(r.request().url()); return r.fulfill({ status: 200, contentType: "text/html", body: "<title>stub</title>" }); });
    await pg.goto(`${base()}${o.page}/research/${n.id}`);
    eq(await pg.locator("#n-src").innerText(), "https://example.org/diary", "the address shown as text");
    eq(await pg.locator('#research a[href^="http"]').count(), 0, "a link off the site on the note page");
    await pg.getByRole("button", { name: "Copy the address" }).waitFor();
    await pg.getByRole("button", { name: /Open the source/ }).click();
    ok(await pg.locator("#n-warn").isVisible(), "no warning");
    ok(/This opens another website\. Scríobh never fetches it, and the site will see your visit\./.test(await pg.locator("#n-warn").innerText()), "the warning words");
    eq(seen.length, 0, "a request to the source before the second click");
    await pg.getByRole("button", { name: "Cancel" }).click();
    ok(!(await pg.locator("#n-warn").isVisible()), "Cancel left the warning up");
    ok(await pg.evaluate(() => document.activeElement.hasAttribute("data-open")), "focus did not return to the Open button");
    await pg.getByRole("button", { name: /Open the source/ }).click();
    await pg.keyboard.press("Escape");
    ok(!(await pg.locator("#n-warn").isVisible()), "Escape left the warning up");
    await pg.getByRole("button", { name: /Open the source/ }).click();
    const popup = pg.waitForEvent("popup");
    await pg.getByRole("button", { name: "Open it in a new tab" }).click();
    const tab = await popup;
    await tab.waitForLoadState("domcontentloaded").catch(() => {});
    eq(tab.url(), "https://example.org/diary", "the opened address");
    eq(await tab.evaluate(() => window.opener), null, "the opened page can reach this one");
    await tab.close();
    await main.unroute("https://example.org/**");
  });

  await test(`${o.name}: the board moves a card by keyboard and by the Move to box, and says so`, async () => {
    const a = await note(o, { title: "Board A", status: "inbox" });
    const b = await note(o, { title: "Board B", status: "inbox" });
    await note(o, { title: "Board C", status: "useful" });
    await pg.goto(`${base()}${o.page}/research/board`);
    const col = (key) => pg.locator(`.r-col[data-key="${key}"]`);
    ok(await col("inbox").getByRole("link", { name: "Board A" }).count(), "Board A is not in Inbox");
    await col("inbox").getByRole("link", { name: "Board A" }).focus();
    await pg.keyboard.press("ArrowRight");
    await waitFor(async () => /Board A moved to Reading/.test(await pg.locator("#r-live").innerText()), "the live region to announce the move");
    ok(await col("reading").getByRole("link", { name: "Board A" }).count(), "Board A did not reach Reading");
    ok(await pg.evaluate(() => document.activeElement.textContent.trim() === "Board A"), "focus did not stay on the card");
    eq((await get(o, a.id)).status, "reading", "the server's status");
    await pg.keyboard.press("ArrowLeft");
    await waitFor(async () => /Board A moved to Inbox/.test(await pg.locator("#r-live").innerText()), "the move back");
    await pg.keyboard.press("ArrowLeft");
    await waitFor(async () => /first column/.test(await pg.locator("#r-live").innerText()), "the edge message");
    // The Move to box, with the keyboard.
    await pg.locator(`#c-${b.id} select`).selectOption("archived");
    await pg.locator(`#c-${b.id}`).getByRole("button", { name: "Move to" }).click();
    await waitFor(async () => /Board B moved to Archived/.test(await pg.locator("#r-live").innerText()), "the Move to announcement");
    eq((await get(o, b.id)).status, "archived", "the server's status for B");
    // A card that changed elsewhere is not moved, and the board says so. (The live stream would redraw the board with the
    // new revision at once, which is its job; it is held back here so the stale answer itself is what is tested.)
    await pg.evaluate(() => document.addEventListener("scriobh:event", (e) => e.stopImmediatePropagation(), true));
    await api(main, "PATCH", `${o.api}/research/${a.id}`, { base_revision: (await get(o, a.id)).revision, title: "Board A, renamed" });
    await pg.locator(`#c-${a.id} a`).focus();
    await pg.keyboard.press("ArrowRight");
    await waitFor(async () => /Not moved/.test(await pg.locator("#r-live").innerText()), "the stale message");
    eq((await get(o, a.id)).status, "inbox", "a stale move changed the note");
    // By tag.
    await api(main, "PATCH", `${o.api}/research/${a.id}`, { base_revision: (await get(o, a.id)).revision, tags: ["boats"] });
    await pg.goto(`${base()}${o.page}/research/board?by=tag`);
    ok(await pg.locator(".r-col .tagdot").count() > 0, "no swatch on a tag column");
    ok(/boats/.test(await pg.locator(".r-cols").innerText()), "no boats column");
  });

  await test(`${o.name}: search finds a phrase and says plainly when nothing matches`, async () => {
    await note(o, { title: "Rope walk", body: "Hemp and tar, a cable's length." });
    await pg.goto(`${base()}${o.page}/research`);
    await pg.locator("#r-q").fill("hemp");
    await pg.getByRole("button", { name: "Search", exact: true }).click();
    await pg.waitForSelector("#r-results");
    ok(/Rope walk/.test(await pg.locator("#r-results").innerText()) && await pg.locator("#r-results mark").count() > 0, "the hit or its highlight is missing");
    await pg.locator("#r-q").fill("qqzzxx");
    await pg.getByRole("button", { name: "Search", exact: true }).click();
    ok(/Nothing matches/.test(await pg.locator("#r-results").innerText()), "no honest empty result");
  });

  await test(`${o.name}: the scratchpad saves by itself, survives a reload and a failed save, and asks about a conflict`, async () => {
    kit.allowed = [/status of 409/, /status of 422/];
    await pg.goto(`${base()}${o.page}/scratch`);
    ok(/Private to you/.test(await pg.locator("#research").innerText()) && /not encrypted/.test(await pg.locator("#research").innerText()), "the privacy sentence");
    await pg.locator("#sc-content").fill("Ask Mara about the ledger.");
    await saved(pg);
    await pg.reload();
    eq(await pg.locator("#sc-content").inputValue(), "Ask Mara about the ledger.", "the pad after a reload");
    // A failed save keeps a copy; after a reload it is offered, never forced.
    await pg.evaluate(() => { window.fetch = () => Promise.reject(new TypeError("Failed to fetch")); });
    await pg.locator("#sc-content").fill("Ask Mara about the ledger. And the rope.");
    await waitFor(async () => /kept in this browser/.test(await state(pg).innerText()), "Not saved");
    await pg.reload();
    eq(await pg.locator("#sc-content").inputValue(), "Ask Mara about the ledger.", "the reload did not replace the server's text");
    await pg.getByRole("button", { name: "Put it back" }).click();
    eq(await pg.locator("#sc-content").inputValue(), "Ask Mara about the ledger. And the rope.", "the draft");
    await saved(pg);
    // Another tab of the same person saves; this one meets it.
    const cur = (await api(main, "GET", `${o.api}/scratchpad`)).json.item;
    await api(main, "PUT", `${o.api}/scratchpad`, { content: "From the other tab.", base_revision: cur.revision });
    await pg.locator("#sc-content").fill("Typed here instead.");
    await pg.locator(".r-bar").waitFor({ timeout: 8000 });
    await pg.getByRole("button", { name: "Keep mine" }).click();
    await saved(pg);
    eq((await api(main, "GET", `${o.api}/scratchpad`)).json.item.content, "Typed here instead.", "Keep mine on the pad");
    // Over the limit: the words, and the text is kept.
    await pg.locator("#sc-content").fill("é".repeat(140000)); // under the box's own length limit, over the server's 256 KB of bytes
    await waitFor(async () => /kept in this browser/.test(await state(pg).innerText()) && /256/.test(await state(pg).innerText()), "the 256 KB message");
    await pg.locator("#sc-content").fill("short again");
    await saved(pg);
  });

  await test(`${o.name}: the pad is private, and a reader has one of their own`, async () => {
    const mine = (await api(main, "GET", `${o.api}/scratchpad`)).json.item;
    ok(mine.content.length > 0, "the owner's pad is empty");
    await rpage.goto(`${base()}${o.page}/scratch`);
    eq(await rpage.locator("#sc-content").inputValue(), "", "the reader sees the owner's pad");
    await rpage.locator("#sc-content").fill("A reader's own note.");
    await saved(rpage);
    eq((await api(main, "GET", `${o.api}/scratchpad`)).json.item.content, "short again", "the owner's pad changed");
  });

  await test(`${o.name}: a reader sees the research and can change nothing`, async () => {
    await rpage.goto(`${base()}${o.page}/research`);
    await rpage.waitForSelector(".r-row");
    eq(await rpage.locator("#r-add-kind").count(), 0, "a reader is offered the add form");
    eq(await rpage.locator(".r-tools").count(), 0, "a reader is offered move or trash buttons");
    const first = await rpage.locator(".r-row:not(.is-folder) a.r-name").first().getAttribute("href");
    await rpage.goto(`${base()}${first}`);
    ok(/read this research but not change it/.test(await rpage.locator("#research").innerText()), "no read-only sentence");
    eq(await rpage.getByRole("button", { name: "Save", exact: true }).count(), 0, "a reader has Save");
    ok(await rpage.locator("#n-title").evaluate((e) => e.readOnly), "the title is editable for a reader");
    await rpage.goto(`${base()}${o.page}/research/board`);
    eq(await rpage.locator(".r-moveto").count(), 0, "a reader is offered the Move to box");
    await rpage.goto(`${base()}${o.page}/research/tags`);
    eq(await rpage.locator("#t-name").count(), 0, "a reader is offered the new tag form");
  });
}

await test("the pad is flushed on sign-out, and a page left with unsaved text keeps it", async () => {
  const o = owners[0];
  await pg.goto(`${base()}${o.page}/scratch`);
  await pg.locator("#sc-content").fill("Typed just before signing out.");
  await pg.getByRole("button", { name: /^account/i }).first().click();
  await pg.getByRole("button", { name: /sign out/i }).first().click();
  await pg.waitForURL((u) => !u.pathname.startsWith("/p/"), { timeout: 15000 });
  await signIn(pg, base());
  eq((await api(main, "GET", `${o.api}/scratchpad`)).json.item.content, "Typed just before signing out.", "the pad after sign-out");
  // Leaving the page by navigation right after typing.
  await pg.goto(`${base()}${o.page}/scratch`);
  await pg.locator("#sc-content").fill("Typed just before leaving.");
  await pg.goto(`${base()}${o.page}/research`);
  await waitFor(async () => (await api(main, "GET", `${o.api}/scratchpad`)).json.item.content === "Typed just before leaving.", "the pad to be saved on leaving");
});

await test("live: a change made elsewhere shows on the open list and board without touching typed text", async () => {
  const o = owners[0];
  await pg.goto(`${base()}${o.page}/research`);
  await pg.locator("#r-add-title").fill("half typed");
  await pg.locator("#r-add-title").focus();
  await pg.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: "research.created" } })));
  await sleep(300);
  eq(await pg.locator("#r-add-title").inputValue(), "half typed", "typed text was clobbered by a live refresh");
  await note(o, { title: "Arrived live" });
  await pg.evaluate(() => document.dispatchEvent(new CustomEvent("scriobh:event", { detail: { type: "research.created" } })));
  await waitFor(async () => /Arrived live/.test(await pg.locator("#r-tree").innerText()), "the new note to appear");
  eq(await pg.locator("#r-add-title").inputValue(), "half typed", "typed text after the refresh");
});

await test("phone, 390 px: no sideways scroll and targets of 44 px on every research page", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  for (const o of owners) {
    const first = (await api(main, "GET", `${o.api}/research`)).json.items.find((i) => i.kind !== "folder");
    const clip = (await api(main, "GET", `${o.api}/research`)).json.items.find((i) => i.kind === "clipping");
    for (const [label, path] of [["list", "/research"], ["board", "/research/board"], ["tags", "/research/tags"], ["note", `/research/${first.id}`], ["clipping", `/research/${clip.id}`], ["scratch", "/scratch"]]) {
      await ph.page.goto(`${base()}${o.page}${path}`);
      await ph.page.waitForSelector("#research");
      const m = await ph.page.evaluate(() => {
        const small = [...document.querySelectorAll("#research a.r-name, #research .btn, #research .icon-btn, #research input:not([type=hidden]):not([type=radio]):not([type=checkbox]), #research select, #research textarea, #research summary, #research .r-sub a, #research .r-by a")]
          .filter((e) => e.getClientRects().length && getComputedStyle(e).visibility !== "hidden")
          .filter((e) => Math.round(e.getBoundingClientRect().height) < 44 && !(e.closest("p") && e.tagName === "A" && !e.classList.contains("r-name")))
          .map((e) => `${e.tagName}.${e.className}:${Math.round(e.getBoundingClientRect().height)} ${(e.textContent || e.getAttribute("aria-label") || "").trim().slice(0, 24)}`);
        return { sw: document.documentElement.scrollWidth, iw: window.innerWidth, small };
      });
      ok(m.sw <= m.iw, `${o.name} ${label}: the page scrolls sideways (${m.sw} > ${m.iw})`);
      eq(m.small.length, 0, `${o.name} ${label}: controls under 44 px: ${m.small.slice(0, 6).join(" | ")}`);
    }
  }
  await ph.ctx.close();
});

await kit.finish("e2e-research");
