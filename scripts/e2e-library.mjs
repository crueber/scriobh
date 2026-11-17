#!/usr/bin/env node
// End-to-end test of the library, settings, preferences, palette and switcher,
// lease release and the reload-with-the-network-cut guarantee, in a real Chromium.
//
//   node scripts/e2e-library.mjs [--only substring]
import { armAnswer, armOk, createKit, eq, ok } from "./e2e-kit.mjs";
import { signIn } from "./harness.mjs";

const kit = await createKit("e2e-library");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const project = async (title, extra = {}) => {
  const made = await api(main, "POST", "/api/v1/projects", { title, kind: "novel", ...extra });
  if (!made.json || !made.json.item) throw new Error(`creating "${title}" answered ${made.status} ${JSON.stringify(made.json)}`);
  const p = made.json.item;
  const list = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json;
  return { ...p, doc: list.order[0] };
};
const list = async (state = "active") => (await api(main, "GET", `/api/v1/projects?state=${state}`)).json.items;
const body = async (ctx, id) => (await api(ctx, "GET", `/api/v1/docs/${id}`)).json.item.content.replace(/\n$/, "");
const tile = (page, title) => page.locator("#shelf-wrap .tile", { has: page.locator(".tile-title", { hasText: new RegExp(`^${title}$`) }) });
const titles = (page) => page.locator("#shelf-wrap .tile .tile-title").allInnerTexts();

await kit.test("library: continue where I left off, pin, archive and undo, trash, restore, purge", async () => {
  kit.allowed = [/status of 404/];
  const a = await project("Alpha", { word_goal: 50000 });
  await project("Beta");
  await project("Gamma");
  await pg.goto(`${base()}/p/${a.slug}`, { waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  await waitFor(async () => (await list()).find((p) => p.title === "Alpha").last_document_id === a.doc, "project.open to record the document");
  await pg.goto(`${base()}/`);
  const cont = pg.locator(".continue");
  ok(await cont.isVisible(), "no Continue card");
  ok((await cont.innerText()).includes("Alpha"), "the Continue card is not Alpha: " + (await cont.innerText()));
  await cont.click();
  await pg.waitForURL(new RegExp(`/p/${a.slug}/d/${a.doc}$`));
  await pg.goto(`${base()}/`);

  // Pin: Gamma moves to the front.
  await pg.getByRole("button", { name: "Pin Gamma" }).click();
  await waitFor(async () => (await titles(pg))[0] === "Gamma", "the pinned project to come first");
  eq(await pg.getByRole("button", { name: "Unpin Gamma" }).getAttribute("aria-pressed"), "true", "pin state");
  await sleep(150); // htmx settles a redrawn shelf for 20 ms; a link pressed inside that gap would navigate instead

  // Progress: a project with a goal shows a progress bar.
  eq(await tile(pg, "Alpha").locator("progress").count(), 1, "a progress bar for a project with a word goal");
  eq(await tile(pg, "Beta").locator("progress").count(), 0, "no bar without a goal");

  // Archive by keyboard, then Undo from the toast.
  await pg.getByRole("link", { name: "Actions for Beta" }).focus();
  await pg.keyboard.press("Enter");
  await pg.locator("dialog#dialog[open]").waitFor();
  await sleep(300); // a button pressed within htmx's 20 ms settle gap submits the plain form instead, which has no toast
  await pg.getByRole("button", { name: "Archive", exact: true }).click();
  await waitFor(async () => !(await titles(pg)).includes("Beta"), "Beta to leave the shelf");
  await pg.locator("#toasts").getByRole("button", { name: "Undo" }).click();
  await waitFor(async () => (await titles(pg)).includes("Beta"), "Beta back after Undo");
  eq((await list("archived")).length, 0, "nothing archived after Undo");

  // Archive for real and look at the Archived view.
  await pg.getByRole("link", { name: "Actions for Beta" }).click();
  await pg.getByRole("button", { name: "Archive", exact: true }).click();
  await waitFor(async () => (await list("archived")).length === 1, "Beta to be archived");
  await pg.getByRole("link", { name: "Archived" }).click();
  await pg.waitForURL(/state=archived/);
  ok((await titles(pg)).includes("Beta"), "Beta is not in the Archived view");

  // Trash Alpha, restore it, trash it again, delete it for good with the typed title.
  await pg.goto(`${base()}/`);
  await pg.getByRole("link", { name: "Actions for Alpha" }).click();
  await pg.getByRole("button", { name: "Move to the trash" }).click();
  await waitFor(async () => (await list("trashed")).length === 1, "Alpha in the trash");
  await pg.goto(`${base()}/?state=trashed`);
  await pg.getByRole("button", { name: "Restore" }).click();
  await waitFor(async () => (await list("trashed")).length === 0, "Alpha restored");
  await pg.goto(`${base()}/`);
  await pg.getByRole("link", { name: "Actions for Alpha" }).click();
  await pg.getByRole("button", { name: "Move to the trash" }).click();
  await waitFor(async () => (await list("trashed")).length === 1, "Alpha in the trash again");
  await pg.goto(`${base()}/?state=trashed`);
  await pg.getByRole("link", { name: "Delete forever" }).click();
  const confirm = pg.locator("#pp-title");
  await confirm.waitFor();
  const del = pg.getByRole("button", { name: "Delete forever" });
  await waitFor(() => del.isDisabled(), "Delete forever to be off until the title is typed");
  await confirm.fill("alpha");
  ok(await del.isDisabled(), "a wrong title enables the button");
  await confirm.fill("Alpha");
  ok(await del.isEnabled(), "the exact title does not enable the button");
  await del.click();
  await waitFor(async () => (await list("all")).every((p) => p.title !== "Alpha"), "Alpha to be gone");
  await waitFor(async () => (await pg.locator("body").innerText()).includes("The trash is empty."), "the trash empty state");
});

await kit.test("library without JavaScript: pin posts a form and comes back", async () => {
  const ctx = await kit.newContext({ javaScriptEnabled: false, viewport: { width: 1280, height: 800 } });
  await ctx.addCookies(await main.cookies());
  const page = await ctx.newPage();
  await page.goto(`${base()}/`);
  await page.getByRole("button", { name: "Unpin Gamma" }).click();
  await page.waitForURL(`${base()}/`);
  await page.getByRole("button", { name: "Pin Gamma" }).waitFor();
  await ctx.close();
});

await kit.test("library at 390 px: no sideways scroll, targets reachable, empty states", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 });
  await ctx.addCookies(await main.cookies());
  const page = await ctx.newPage();
  kit.watch(page, "phone");
  for (const q of ["/", "/?state=archived", "/?state=trashed", "/settings"]) {
    await page.goto(`${base()}${q}`);
    const w = await page.evaluate(() => ({ s: document.documentElement.scrollWidth, c: document.documentElement.clientWidth }));
    ok(w.s <= w.c, `${q} scrolls sideways: ${w.s} > ${w.c}`);
  }
  await page.goto(`${base()}/`);
  await page.getByRole("link", { name: "Actions for Gamma" }).tap();
  await page.locator("dialog#dialog[open]").waitFor();
  const small = await page.locator("#dialog .action-item").evaluateAll((els) => els.filter((e) => e.getBoundingClientRect().height < 43.5).length);
  eq(small, 0, "touch targets below 44 px in the project menu");
  await page.keyboard.press("Escape");
  await kit.csp(page, "phone");
  await ctx.close();
});

await kit.test("settings: theme before first paint, reduced motion, writing prefs, profile, sessions", async () => {
  kit.allowed = [/status of 401/, /status of 422/];
  await pg.goto(`${base()}/settings`);
  // The choice shows at once (no save), and the saved choice is rendered by the server on <html>.
  await pg.selectOption("#sf-theme", "dark");
  eq(await pg.locator("html").getAttribute("data-theme"), "dark", "live preview of the theme");
  await pg.selectOption("#sf-reduced_motion", "on");
  await pg.selectOption("#sf-editor_size", "22");
  await pg.selectOption("#sf-editor_measure", "60");
  await pg.selectOption("#sf-editor_font", "sans");
  await pg.selectOption("#sf-autosave_ms", "1000");
  await pg.locator("#ck-show_word_count").uncheck();
  await pg.getByRole("button", { name: "Save settings" }).click();
  await pg.waitForURL(/saved=prefs/);
  ok(await pg.getByText("Settings saved.").isVisible(), "no confirmation");
  // Served with the attributes: check the raw HTML, before any script could have run.
  const raw = await (await main.request.get(`${base()}/settings`)).text();
  ok(/<html[^>]*data-theme="dark"[^>]*data-motion="reduce"/.test(raw), "the theme and motion are not in the served <html>: " + raw.slice(0, 200));
  const bg = await pg.evaluate(() => getComputedStyle(document.body).backgroundColor);
  ok(bg === "rgb(19, 19, 17)", "dark theme background " + bg);
  const dur = await pg.evaluate(() => parseFloat(getComputedStyle(document.querySelector(".btn")).transitionDuration));
  ok(dur < 0.001, "reduced motion left a transition of " + dur + " s");
  // The writing view takes the font, size, line length and autosave.
  const p = await project("Prefs book");
  await pg.goto(`${base()}/p/${p.slug}`);
  await pg.waitForSelector(".scriobh-prose");
  const m = await pg.evaluate(() => ({ size: getComputedStyle(document.querySelector(".scriobh-prose")).fontSize, font: getComputedStyle(document.querySelector(".scriobh-prose")).fontFamily, counts: document.querySelector("#st-counts").getAttribute("aria-pressed"), auto: document.querySelector("#write").dataset.autosave }));
  eq(m.size, "22px", "editor font size");
  ok(/Instrument Sans/.test(m.font), "editor font " + m.font);
  eq(m.counts, "false", "word counts off by default");
  eq(m.auto, "1000", "autosave interval");
  // Profile.
  await pg.goto(`${base()}/settings`);
  await pg.fill("#pf-name", "Máire Nolan");
  await pg.getByRole("button", { name: "Save profile" }).click();
  await pg.waitForURL(/saved=profile/);
  ok(await pg.getByRole("button", { name: /Account, Máire Nolan/ }).count() > 0, "the account button does not show the new name");
  await pg.fill("#pf-name", "   ");
  await pg.evaluate(() => document.querySelector("#pf-name").removeAttribute("required"));
  await pg.getByRole("button", { name: "Save profile" }).click();
  await pg.getByText("Give yourself a name.").waitFor();
  // Sessions: a second device appears, and can be signed out from here.
  const other = await kit.signedIn({ viewport: { width: 800, height: 600 } });
  await pg.goto(`${base()}/settings#sessions`);
  ok((await pg.locator(".sessions li").count()) >= 2, "the second session is not listed");
  ok(await pg.locator(".this-device").count() === 1, "exactly one session is marked as this device");
  await pg.getByRole("button", { name: /Sign out the other \d+ device/ }).click();
  await pg.waitForURL(/saved=sessions/);
  const gone = await other.page.goto(`${base()}/settings`);
  ok(/auth\/login|dev-idp/.test(other.page.url()) || gone.status() !== 200, "the other device still has a session: " + other.page.url());
  await other.ctx.close();
  eq(await pg.locator(".sessions li").count(), 1, "only this device is left");
  // Signing this device out from Settings leaves no draft of a manuscript behind (and is not a writing page).
  const q = await project("Drafts book");
  await pg.evaluate(([a, b]) => { localStorage.setItem(`scriobh:draft:${document.querySelector('meta[name="scriobh-user"]').content}:${a}`, "{}"); localStorage.setItem(`scriobh:held:${document.querySelector('meta[name="scriobh-user"]').content}:${b}`, "[]"); }, [q.doc, q.doc]);
  const other2 = await kit.signedIn({ viewport: { width: 800, height: 600 } });
  await other2.ctx.close();
  await pg.goto(`${base()}/settings#sessions`);
  await pg.locator(".sessions li", { has: pg.locator(".this-device") }).getByRole("button", { name: "Sign out here" }).click();
  await pg.waitForURL((u) => u.pathname === "/", { timeout: 10000 });
  eq(await pg.evaluate(() => Object.keys(localStorage).filter((k) => /^scriobh:(draft|held):/.test(k)).length), 0, "drafts left after signing this device out");
  await signIn(pg, base());
  // Put the preferences back for the steps that follow.
  await api(main, "PUT", "/api/v1/me/prefs", { items: { theme: "system", reduced_motion: "system", editor_size: "19", editor_measure: "66", editor_font: "serif", autosave_ms: "700", show_word_count: "true" } });
  await api(main, "PATCH", "/api/v1/me", { display_name: "Wren Writer" });
});

await kit.test("palette and switcher: commands, chapters, projects, focus returns", async () => {
  const p = await project("Palette book");
  for (const t of ["Second chapter", "Third chapter"]) await api(main, "POST", `/api/v1/projects/${p.id}/docs`, { title: t });
  await pg.goto(`${base()}/`);
  await pg.keyboard.press(`${MOD}+k`);
  const input = pg.locator("#palette-input");
  await input.waitFor();
  await input.fill("sett");
  eq(await pg.locator("#palette-list [aria-selected=true] .cmd-label").innerText(), "Settings", "the best match is selected");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/settings$/);
  // The switcher on a project page lists its chapters; typing narrows; Enter opens one.
  await pg.goto(`${base()}/p/${p.slug}`);
  await pg.waitForSelector(".scriobh-prose");
  await pg.keyboard.press(`${MOD}+o`);
  await input.waitFor();
  const opts = await pg.locator("#palette-list [role=option]").allInnerTexts();
  ok(opts.some((t) => t.includes("Third chapter")) && !opts.some((t) => /Go to library/.test(t)), "the switcher lists chapters, not commands: " + JSON.stringify(opts));
  await input.fill("third");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/d\//);
  await pg.waitForSelector(".scriobh-prose");
  eq(await pg.locator("#doc-title").inputValue(), "Third chapter", "the chapter opened");
  // A command on the writing view: Source, from the palette, then back.
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await input.fill("source");
  await pg.keyboard.press("Enter");
  await waitFor(async () => pg.locator(".scriobh-source").isVisible(), "Source mode from the palette");
  await pg.keyboard.press(`${MOD}+e`);
  // Projects through the switcher from anywhere; Escape returns focus to where it was.
  await pg.goto(`${base()}/settings`);
  await pg.locator("#pf-name").focus();
  await pg.keyboard.press(`${MOD}+o`);
  await input.fill("palette b");
  await waitFor(async () => (await pg.locator("#palette-list [role=option]").count()) > 0, "projects in the switcher");
  await pg.keyboard.press("Escape");
  eq(await pg.evaluate(() => document.activeElement.id), "pf-name", "focus returns after Escape");
  await pg.keyboard.press(`${MOD}+o`);
  await input.fill("palette b");
  await waitFor(async () => (await pg.locator("#palette-list [role=option]").count()) > 0, "the project");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(new RegExp(`/p/${p.slug}`));
});

await kit.test("writing modes: dimmed paragraphs and typewriter scrolling follow the settings", async () => {
  const p = await project("Modes book");
  const paras = Array.from({ length: 60 }, (_, i) => `Paragraph ${i + 1} of the chapter, with enough words in it to take a line or two on the page.`).join("\n\n");
  await api(main, "PATCH", `/api/v1/docs/${p.doc}`, { base_revision: 1, content: paras });
  await pg.goto(`${base()}/p/${p.slug}`);
  await pg.waitForSelector(".scriobh-prose");
  eq(await pg.locator(".paper.focus-dim").count(), 0, "no dimming by default");
  await api(main, "PUT", "/api/v1/me/prefs", { items: { focus_dim: "true", typewriter: "true" } });
  await pg.goto(`${base()}/p/${p.slug}`);
  await pg.waitForSelector(".scriobh-prose");
  await pg.locator(".scriobh-prose p").nth(40).click();
  // The mark is on the first paragraph before the click (the caret starts there), so "one paragraph is marked" is already true
  // and says nothing about the click: wait for the mark to be on the paragraph that was clicked, and read nothing before that.
  const marked = () => pg.evaluate(() => [...document.querySelectorAll(".scriobh-prose > *")].map((e, i) => (e.classList.contains("is-active") ? i : -1)).filter((i) => i >= 0));
  await waitFor(async () => (await marked()).join() === "40", "the clicked paragraph (block 40) to be marked");
  const op = (n) => pg.locator(".scriobh-prose > p").nth(n).evaluate((e) => getComputedStyle(e).opacity);
  const act = await marked();
  eq(await op(act[0]), "1", `the paragraph with the caret (block ${act}) is at full strength`);
  eq(await op(10), "0.55", "the other paragraphs are dimmed");
  // Dragging a selection never recentres the page under the pointer; only typing does.
  const y0 = await pg.evaluate(() => scrollY);
  const b1 = await pg.locator(".scriobh-prose > p").nth(act[0]).boundingBox(), b2 = await pg.locator(".scriobh-prose > p").nth(act[0] + 1).boundingBox();
  await pg.mouse.move(b1.x + 40, b1.y + 10);
  await pg.mouse.down();
  await pg.mouse.move(b2.x + 200, b2.y + 10, { steps: 6 });
  await pg.mouse.up();
  await sleep(300);
  ok(Math.abs((await pg.evaluate(() => scrollY)) - y0) < 4, "a drag-select moved the page");
  await pg.locator(".scriobh-prose > p").nth(act[0]).click();
  await pg.keyboard.type(" more");
  await sleep(200);
  const top = await pg.evaluate(() => { const v = scriobh.write.editor.view; return v.coordsAtPos(v.state.selection.head).top / innerHeight; });
  ok(top > 0.25 && top < 0.65, `the caret line sits near the middle of the window: ${top.toFixed(2)}`);
  await waitFor(async () => (await pg.locator("#save-state").getAttribute("data-state")) === "saved" && !(await pg.evaluate(() => window.scriobh.write.unsaved())), "Saved");
  await api(main, "PUT", "/api/v1/me/prefs", { items: { focus_dim: "false", typewriter: "false" } });
});

await kit.test("closing a tab frees its chapter at once (lease release)", async () => {
  const p = await project("Lease book");
  const ctxA = await kit.newContext({ viewport: { width: 1000, height: 700 } });
  await ctxA.addCookies(await main.cookies());
  const a = await ctxA.newPage();
  await a.goto(`${base()}/p/${p.slug}`);
  await a.waitForSelector(".scriobh-prose");
  await a.locator(".scriobh-prose").click();
  await a.keyboard.type("Written in the first tab.");
  await waitFor(async () => (await api(main, "GET", `/api/v1/docs/${p.doc}`)).json.item.content.startsWith("Written in the first tab."), "the first tab's text to be saved");
  // A second tab opens while the first still holds the lease: read-only.
  await pg.goto(`${base()}/p/${p.slug}`);
  await waitFor(async () => (await pg.locator("#save-state").getAttribute("data-state")) === "locked", "the second tab to be locked");
  await a.close({ runBeforeUnload: false });
  await sleep(600);
  await pg.reload({ waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  await sleep(500);
  eq(await pg.locator("#save-state").getAttribute("data-state"), "saved", "after the first tab closed, the chapter is free");
  eq(await pg.locator(".scriobh-prose").getAttribute("contenteditable"), "true", "editable once the lease is released");
  await ctxA.close();
});

await kit.test("reload with the network cut: nothing typed is lost; the draft comes back", async () => {
  kit.allowed = [/ERR_/, /Failed to load resource/];
  const p = await project("Offline book");
  const off = await main.newPage();
  kit.watch(off, "offline");
  await off.goto(`${base()}/p/${p.slug}`);
  await off.waitForSelector(".scriobh-prose");
  await off.locator(".scriobh-prose").click();
  await off.keyboard.type("Saved before the cut.");
  await waitFor(async () => (await api(main, "GET", `/api/v1/docs/${p.doc}`)).json.item.content.startsWith("Saved before the cut."), "the first save");
  await main.setOffline(true);
  await off.keyboard.type(" Typed with the network cut.");
  await waitFor(async () => (await off.locator("#save-state").getAttribute("data-state")) === "offline", "Offline");
  // A reload cannot load the page (there is no service worker). The tab is still alive, so the text waits in the editor.
  let failed = false;
  await off.reload({ waitUntil: "load" }).catch(() => { failed = true; });
  ok(failed, "a reload with the network cut unexpectedly loaded the page");
  // The tab is now the browser's error page, so the page script is gone: the draft is all that is left of the text.
  // The chapter is still leased to the dead tab (its release could not be sent).
  await off.close();
  await main.setOffline(false);
  const again = await main.newPage();
  kit.watch(again, "reopened");
  await again.goto(`${base()}/p/${p.slug}`, { waitUntil: "load" });
  await again.waitForSelector(".scriobh-prose");
  await sleep(800);
  // Either the release got out (the chapter is free) or the old lease holds (Locked, then Take over).
  if ((await again.locator("#save-state").getAttribute("data-state")) === "locked") await again.getByRole("button", { name: "Take over" }).click();
  // Nothing may be lost, by one of two routes: the old tab got its save out when the network came back,
  // or this browser still holds the draft and offers it (never applied silently).
  const restore = again.getByRole("button", { name: "Restore your unsaved text" });
  const has = async () => (await api(main, "GET", `/api/v1/docs/${p.doc}`)).json.item.content.includes("Typed with the network cut.");
  await waitFor(async () => (await has()) || (await restore.isVisible()), "the text to be saved, or offered back");
  if (!(await has())) {
    eq((await api(main, "GET", `/api/v1/docs/${p.doc}`)).json.item.content, "Saved before the cut.\n", "the draft must not be applied by itself");
    await restore.click();
    await waitFor(has, "the restored text to be saved");
  }
  await again.close();
});

await kit.test("search: a project and every project; highlighted, escaped, opens the document", async () => {
  const p = await project("Search book");
  await api(main, "PATCH", `/api/v1/docs/${p.doc}`, { base_revision: 1, title: "The Harbour", content: "The harbour was a bowl of <script>window.hacked=1</script> black mud, and the gulls argued.\n" });
  await api(main, "POST", `/api/v1/projects/${p.id}/docs`, { title: "Elsewhere", content: "Nothing here but weather.\n" });
  await pg.goto(`${base()}/`);
  await pg.locator("#lib-q").fill("harb");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\/search\?q=harb/);
  const group = pg.locator(".result-group", { hasText: "Search book" });
  await group.waitFor();
  eq(await group.locator("mark").first().innerText(), "harbour", "the match is highlighted");
  eq(await pg.evaluate(() => window.hacked), undefined, "a script in a document ran");
  eq(await pg.locator(".snippet script").count(), 0, "markup from a document in the snippet");
  await group.getByRole("link", { name: "The Harbour" }).click();
  await pg.waitForURL(new RegExp(`/p/${p.slug}/d/${p.doc}$`));
  // The palette goes to the project's search, and the page has a Search tab on a phone.
  await pg.waitForSelector(".scriobh-prose");
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("search this");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(new RegExp(`/p/${p.slug}/search$`));
  await pg.locator("#search-q").fill("gulls");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/q=gulls/);
  ok((await pg.locator(".search-count").innerText()).includes("1 document matches"), "the count: " + (await pg.locator(".search-count").innerText()));
  await pg.locator("#search-q").fill("zzzzqq");
  await pg.keyboard.press("Enter");
  await pg.getByText("Nothing matches").waitFor();
  await pg.locator("#search-q").fill("\"'");
  await pg.keyboard.press("Enter");
  await pg.getByText("Type a word to search for.").waitFor();
});

await kit.test("export: the dialog downloads a file and says what was recorded", async () => {
  const p = await project("Export book");
  await api(main, "PATCH", `/api/v1/docs/${p.doc}`, { base_revision: 1, title: "Chapter one", content: "A line that must be in the export.\n" });
  await pg.goto(`${base()}/`);
  await pg.getByRole("link", { name: "Actions for Export book" }).click();
  await pg.getByRole("link", { name: /Export as Markdown/ }).click();
  await pg.locator("#project-export-form").waitFor();
  ok((await pg.locator("#project-export-form").innerText()).includes("recorded with a manifest"), "the manifest note");
  let [dl] = await Promise.all([pg.waitForEvent("download"), pg.getByRole("button", { name: "Export", exact: true }).click()]);
  ok(/\.md$/.test(dl.suggestedFilename()), "single file: " + dl.suggestedFilename());
  const fs = await import("node:fs");
  ok(fs.readFileSync(await dl.path(), "utf8").includes("A line that must be in the export."), "the export holds the text");
  await pg.locator("#toasts").getByText(/Exported 1 document\. Recorded with a manifest \(exp_/).waitFor();
  // The zip, from the writing view's palette.
  await pg.goto(`${base()}/p/${p.slug}`);
  await pg.waitForSelector(".scriobh-prose");
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("export");
  await pg.keyboard.press("Enter");
  await pg.locator("#ex-mode").selectOption("bundle");
  [dl] = await Promise.all([pg.waitForEvent("download"), pg.getByRole("button", { name: "Export", exact: true }).click()]);
  ok(/\.zip$/.test(dl.suggestedFilename()), "bundle: " + dl.suggestedFilename());
  // Without script the same form posts and the browser downloads.
  const ctx = await kit.newContext({ javaScriptEnabled: false, viewport: { width: 1000, height: 700 } });
  await ctx.addCookies(await main.cookies());
  const plain = await ctx.newPage();
  await plain.goto(`${base()}/ui/projects/${p.id}/export`);
  [dl] = await Promise.all([plain.waitForEvent("download"), plain.getByRole("button", { name: "Export", exact: true }).click()]);
  ok(/\.md$/.test(dl.suggestedFilename()), "no script: " + dl.suggestedFilename());
  await ctx.close();
});

await kit.test("split at cursor: from Source mode, from the palette, and in a read-only chapter", async () => {
  const p = await project("Split book");
  const text = "Para one.\n\nPara two.\n\nPara three.\n";
  await api(main, "PATCH", `/api/v1/docs/${p.doc}`, { base_revision: 1, title: "Whole", content: text });
  await pg.goto(`${base()}/p/${p.slug}`);
  await pg.waitForSelector(".scriobh-prose");
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("split");
  await pg.keyboard.press("Enter");
  await pg.getByText("Put the cursor where the new document should begin").waitFor();
  const at = text.indexOf("Para two.");
  await pg.locator(".scriobh-source").evaluate((t, n) => { t.focus(); t.setSelectionRange(n, n); }, at);
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("split");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\?new=1|\/d\//, { timeout: 10000 });
  await pg.waitForSelector(".scriobh-prose");
  const list = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json;
  eq(list.items.length, 2, "two documents after the split");
  const head = await body(main, p.doc);
  const tailId = list.order[1];
  eq(head, "Para one.", "the head keeps the text before the cut");
  eq(await body(main, tailId), "Para two.\n\nPara three.", "the tail takes the rest");
  await waitFor(async () => (await pg.evaluate(() => document.activeElement && document.activeElement.id)) === "doc-title", "the new document's title to take focus");

  // A chapter past 25,000 words opens read-only; Split at cursor is how it gets cut down.
  const big = await api(main, "POST", `/api/v1/projects/${p.id}/docs`, { title: "Too long", content: Array.from({ length: 25200 }, (_, i) => (i % 40 === 39 ? "beta\n\nbeta" : "beta")).join(" ") });
  const bid = big.json.item.id;
  await pg.goto(`${base()}/p/${p.slug}/d/${bid}`);
  await pg.waitForSelector(".scriobh-prose");
  eq(await pg.locator(".scriobh-prose").getAttribute("contenteditable"), "false", "read-only past the limit");
  await pg.locator("#nb-guard").getByRole("button", { name: "Split at cursor" }).click();
  await pg.getByText("Put the cursor where the new document should begin").waitFor();
  await pg.locator(".scriobh-source").evaluate((t) => { const n = Math.floor(t.value.length / 2); t.focus(); t.setSelectionRange(n, n); });
  await pg.locator("#nb-guard").getByRole("button", { name: "Split at cursor" }).click();
  await waitFor(async () => (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.items.length === 4, "the long chapter to be split", 20000);
  const after = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.items;
  ok(after.length === 4, "the long chapter became two: " + after.length);
  ok(after.every((d) => d.word_count < 25000), "both halves are under the limit: " + after.map((d) => d.word_count));
});

await kit.test("palette: add chapter and sign out from the keyboard; the status bar switch is a saved setting", async () => {
  const p = await project("Palette actions");
  await pg.goto(`${base()}/p/${p.slug}`);
  await pg.waitForSelector(".scriobh-prose");
  eq((await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.items.length, 1, "one chapter");
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("add chapter");
  await pg.keyboard.press("Enter");
  await pg.waitForURL(/\?new=1|\/d\//, { timeout: 10000 });
  eq((await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.items.length, 2, "the palette added a chapter");
  // The word-count switch in the status bar is the account's setting, not a browser leftover.
  await pg.waitForSelector(".scriobh-prose");
  await pg.locator("#st-counts").click();
  await waitFor(async () => (await api(main, "GET", "/api/v1/me/prefs")).json.items.show_word_count === "false", "the switch to be saved to the account");
  await api(main, "PUT", "/api/v1/me/prefs", { items: { show_word_count: "true" } });
  await pg.reload({ waitUntil: "load" });
  await pg.waitForSelector(".scriobh-prose");
  eq(await pg.locator("#st-counts").getAttribute("aria-pressed"), "true", "Settings win over an earlier click");
  // Sign out through the palette (and sign back in for the steps after).
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("sign out");
  await pg.keyboard.press("Enter");
  await pg.waitForURL((u) => !u.pathname.startsWith("/p/"), { timeout: 10000 });
  await signIn(pg, base());
});

await kit.test("review 2: focus after a shelf redraw, Undo by keyboard, dialogs never leave focus on the page", async () => {
  await project("Focus one");
  await project("Focus two");
  await pg.goto(`${base()}/`);
  const active = () => pg.evaluate(() => { const a = document.activeElement; return a ? (a.getAttribute("aria-label") || a.textContent || a.id || a.tagName).trim().slice(0, 40) : ""; });
  // Pin by keyboard: focus comes back to the (redrawn) pin button of the same project.
  await pg.getByRole("button", { name: "Pin Focus two" }).focus();
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await active()) === "Unpin Focus two", "focus on the same control after a pin");
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await active()) === "Pin Focus two", "and after the unpin");
  // Archive from the keyboard: the dialog closes, the toast's Undo has focus and works.
  await pg.getByRole("link", { name: "Actions for Focus one" }).focus();
  await pg.keyboard.press("Enter");
  await pg.locator("dialog#dialog[open]").waitFor();
  ok(await pg.evaluate(() => !!(document.activeElement && document.activeElement.closest("dialog"))), "a dialog opened from the keyboard keeps focus inside it");
  await sleep(300);
  await pg.getByRole("button", { name: "Archive", exact: true }).focus();
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await active()) === "Undo", "Undo to take focus");
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await titles(pg)).includes("Focus one"), "the project to come back after Undo by keyboard");
  ok((await active()) !== "BODY" && (await active()) !== "", "focus is lost after Undo: " + (await active()));
  // Dialogs reached through the menu: the export dialog and the purge confirmation.
  await sleep(300); // htmx settles a redrawn shelf for 20 ms
  await pg.getByRole("link", { name: "Actions for Focus two" }).focus();
  await pg.keyboard.press("Enter");
  await pg.locator("dialog#dialog[open]").waitFor();
  await sleep(300);
  await pg.getByRole("link", { name: /Export as Markdown/ }).focus();
  await pg.keyboard.press("Enter");
  await pg.locator("#project-export-form").waitFor();
  await waitFor(async () => (await pg.evaluate(() => document.activeElement && document.activeElement.id)) === "dialog-title", "focus on the export dialog's heading");
  await pg.keyboard.press("Escape");
});

await kit.test("review 3: palette ids are unique however often it opens; Tab skips the list; aria-expanded is the real state; all projects, fresh", async () => {
  const p = await project("Ids book");
  for (const t of ["Two", "Three"]) await api(main, "POST", `/api/v1/projects/${p.id}/docs`, { title: t });
  await pg.goto(`${base()}/p/${p.slug}`);
  await pg.waitForSelector(".scriobh-prose");
  const input = pg.locator("#palette-input");
  eq(await input.getAttribute("aria-expanded"), "false", "closed");
  for (let i = 0; i < 3; i++) {
    await pg.keyboard.press(`${MOD}+o`);
    await input.waitFor();
    eq(await input.getAttribute("aria-expanded"), "true", "open");
    const ids = await pg.locator("#palette-list [role=option]").evaluateAll((l) => l.map((x) => x.id));
    eq(new Set(ids).size, ids.length, "duplicate option ids on open " + (i + 1) + ": " + ids.join(","));
    await pg.keyboard.press("Escape");
    await waitFor(async () => (await input.getAttribute("aria-expanded")) === "false", "aria-expanded to say closed");
  }
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await input.waitFor();
  await pg.keyboard.press("Tab");
  ok(!(await pg.evaluate(() => document.activeElement.id === "palette-list")), "Tab landed on the scrollable list");
  await pg.keyboard.press("Escape");
  // A project created after this page loaded, and an archived one, are both found on the next open.
  const fresh = await project("Brand new after load");
  await api(main, "POST", `/api/v1/projects/${fresh.id}/archive`);
  await pg.keyboard.press(`${MOD}+o`);
  await input.waitFor();
  await input.fill("brand new");
  const opt = pg.locator("#palette-list [role=option]", { hasText: "Brand new after load" });
  await opt.waitFor();
  ok((await opt.innerText()).includes("Archived project"), "archived projects are marked: " + (await opt.innerText()));
  // Matching is contiguous or by word starts; scattered letters never match a long title.
  await input.fill("bnal");
  eq(await pg.locator("#palette-list [role=option]", { hasText: "Brand new after load" }).count(), 0, "scattered letters matched a long title");
  await input.fill("ids bo");
  ok((await pg.locator("#palette-list [role=option]").count()) >= 1, "word starts do not match");
  await pg.keyboard.press("Escape");
});

await kit.test("review 5: Delete forever reads as danger", async () => {
  const p = await project("Danger book");
  await api(main, "POST", `/api/v1/projects/${p.id}/trash`);
  await pg.goto(`${base()}/?state=trashed`);
  const link = pg.getByRole("link", { name: "Delete forever" }).first();
  const color = await link.evaluate((e) => getComputedStyle(e).color);
  const danger = await pg.evaluate(() => { const d = document.createElement("i"); d.style.color = "var(--danger)"; document.body.appendChild(d); const c = getComputedStyle(d).color; d.remove(); return c; });
  eq(color, danger, "Delete forever is drawn in the danger colour");
  await api(main, "POST", `/api/v1/projects/${p.id}/restore`);
});

await kit.test("review 1: a failed sign-out keeps the drafts and the session; unsaved text is asked about", async () => {
  kit.allowed = [/status of 403/, /Failed to load resource/];
  const p = await project("Signout book");
  // A page with no automatic dialog handler: a confirm is dismissed, as a cautious person would.
  const page = await main.newPage();
  page.on("pageerror", () => {});
  await page.goto(`${base()}/settings`);
  await page.evaluate(([id]) => localStorage.setItem(`scriobh:draft:${document.querySelector('meta[name="scriobh-user"]').content}:${id}`, JSON.stringify({ md: "Only here.", title: "Chapter 1", rev: 1, t: Date.now() })), [p.doc]);
  const keys = () => page.evaluate(() => Object.keys(localStorage).filter((k) => /^scriobh:(draft|held):/.test(k)));
  // 1. The server refuses (a stale token): still signed in, draft kept.
  await page.route("**/auth/logout", (route) => route.fulfill({ status: 403, contentType: "application/json", body: JSON.stringify({ error: { code: "forbidden", message: "expired" } }) }));
  await page.getByRole("button", { name: /^Account/ }).click();
  await page.locator("#account").getByRole("button", { name: "Sign out", exact: true }).click();
  ok(/never saved/.test(await armAnswer(page, "ok")), "the question is asked in the page");
  await page.getByText("Could not sign out").waitFor();
  eq((await keys()).length, 1, "the draft was deleted by a sign-out that failed");
  ok((await page.goto(`${base()}/settings`)).status() === 200 && !/auth\/login|dev-idp/.test(page.url()), "the person was signed out although the server refused");
  await page.unroute("**/auth/logout");
  // 2. Text the server does not have: the person is asked, and a no keeps everything.
  if (!(await page.locator("#account").evaluate((e) => e.matches(":popover-open")))) await page.getByRole("button", { name: /^Account/ }).click();
  await page.locator("#account").getByRole("button", { name: "Sign out", exact: true }).click();
  await armAnswer(page, "cancel");
  ok(await page.locator("#account").getByRole("button", { name: "Sign out", exact: true }).evaluate((b) => b === document.activeElement), "after No the focus is back on Sign out");
  await sleep(500);
  eq((await keys()).length, 1, "the draft is gone after the person said no");
  ok(!/auth\/login/.test(page.url()) && (await page.goto(`${base()}/settings`)).status() === 200, "signed out after saying no");
  await page.close();
});

await kit.test("split: a chapter stored with CRLF line endings splits where the caret is", async () => {
  const p = await project("Crlf book");
  const text = "Alpha line.\r\n\r\nBravo line.\r\n\r\nCharlie line.\r\n";
  await api(main, "PATCH", `/api/v1/docs/${p.doc}`, { base_revision: 1, title: "Windows", content: text });
  await pg.goto(`${base()}/p/${p.slug}`);
  await pg.waitForSelector(".scriobh-prose");
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${MOD}+e`);
  await pg.locator(".scriobh-source").waitFor();
  await pg.locator(".scriobh-source").evaluate((t) => { const n = t.value.indexOf("Charlie"); t.focus(); t.setSelectionRange(n, n); });
  await pg.keyboard.press(`${MOD}+Shift+p`);
  await pg.locator("#palette-input").fill("split");
  await pg.keyboard.press("Enter");
  await waitFor(async () => (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json.items.length === 2, "the split");
  const list = (await api(main, "GET", `/api/v1/projects/${p.id}/docs`)).json;
  const head = (await api(main, "GET", `/api/v1/docs/${p.doc}`)).json.item.content.replace(/\r/g, "");
  const tail = (await api(main, "GET", `/api/v1/docs/${list.order[1]}`)).json.item.content.replace(/\r/g, "");
  ok(head.includes("Bravo line.") && !head.includes("Charlie"), "the head: " + JSON.stringify(head));
  ok(tail.startsWith("Charlie line."), "the tail: " + JSON.stringify(tail));
});

await kit.finish("e2e-library");
