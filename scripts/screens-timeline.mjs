#!/usr/bin/env node
// Screenshots of the timeline pages for docs/screens/phase-3: the Compendium timeline (list, check, filtered, a bad date,
// empty, trash, reader), the event page (new with hints and the picker, edit with scenes, read-only), a book's timeline
// (story order, as told with a moved row, check, empty). 1280x800 (ratio 1) and 390x844 (ratio 2), light and dark, and a
// spot-check of three other themes.
//
//   node scripts/screens-timeline.mjs --out docs/screens/phase-3
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { build, findChromium, repo, rm, signIn, startServer, tempDir } from "./harness.mjs";

const argv = process.argv.slice(2);
const arg = (n, d) => (argv.includes(n) ? argv[argv.indexOf(n) + 1] : d);
const out = resolve(repo, arg("--out", "docs/screens/phase-3"));
mkdirSync(out, { recursive: true });
const { chromium } = await import("playwright");
const tmp = tempDir("scriobh-tlshots-");
const srv = await startServer(build(tmp), join(tmp, "data"));
const base = srv.base;
const browser = await chromium.launch(findChromium() ? { executablePath: findChromium() } : {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const boot = await browser.newContext();
await signIn(await boot.newPage(), base);
const api = async (ctx, method, path, data) => {
  const r = await ctx.request.fetch(base + path, { method, data, headers: data ? { "Content-Type": "application/json" } : {} });
  return r.json().catch(() => null);
};
const A = (m, p, d) => api(boot, m, p, d);

// ---- the story world ----------------------------------------------------------------------------------------------
const C = (await A("POST", "/api/v1/compendiums", { name: "The Salt Road Bible" })).item;
const ent = {};
for (const [k, kind, name] of [["maire", "character", "Máire Nolan"], ["tobias", "character", "Tobias Reeve"], ["ciara", "character", "Ciara Ní Bhriain"], ["harbour", "place", "The Harbour"], ["causeway", "place", "The Causeway"], ["quay", "place", "The Quay House"]]) {
  ent[k] = (await A("POST", `/api/v1/compendiums/${C.id}/entities`, { kind, name })).item.id;
}
const ev = {};
const mk = async (key, title, date, end, label, place, who, description = "") => {
  ev[key] = (await A("POST", `/api/v1/compendiums/${C.id}/timeline/events`, { title, story_date: date, story_date_end: end, story_label: label, location: place, description, participants: who.map(([e, r]) => ({ entity: e, role: r })) })).item.id;
};
await mk("founded", "The harbour wall is finished", "1990-06", "", "Spring, Year 1", "The Harbour", [["Tobias Reeve", "mason"]], "Forty years of work, and the first boat ties up the day after.");
await mk("born", "Máire is born at the Quay House", "1991-02-14", "", "", "The Quay House", [["Máire Nolan", "newborn"]]);
await mk("storm", "The great storm", "1994-11", "1994-12", "Winter, Year 5", "The Harbour", [["Tobias Reeve", "keeps the light"], ["Máire Nolan", "child"]], "Three weeks of weather, and the wall holds.");
await mk("ledger", "The ledger is begun", "1998-03-01", "", "", "The Quay House", [["Máire Nolan", "clerk"]]);
await mk("wedding", "Tobias and Ciara marry", "1998-04-12", "", "Midsummer, Year 9", "The Harbour", [["Tobias Reeve", "groom"], ["Ciara Ní Bhriain", "bride"], ["Máire Nolan", "witness"]]);
await mk("walk", "Máire walks the causeway", "1998-04-12", "", "", "The Causeway", [["Máire Nolan", "alone"]], "She leaves before the feast and does not say why.");
await mk("letter", "The letter from the mainland", "1999-05", "", "", "", [["Ciara Ní Bhriain", "reader"]]);
await mk("ferry", "The ferry stops running", "2001", "2003", "The Quiet Years", "The Harbour", []);
await mk("return", "Máire comes back", "2004-09-02", "", "", "The Harbour", [["Máire Nolan", "returning"], ["Tobias Reeve", "waiting"]]);
await mk("lantern", "The lantern is found on the slip", "2007-10", "", "", "The Harbour", [["Máire Nolan", "finder"]]);
await mk("fire", "The Quay House burns", "2011-01-23", "", "", "The Quay House", [["Tobias Reeve", ""]]);
await mk("rumour", "A rumour of the old road", "", "", "Before anyone counted", "", []);
await mk("legend", "Why the lantern is lit", "", "", "", "", [], "Told differently by everyone who tells it.");

const P = (await A("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", word_goal: 80000, author_name: "Máire Nolan" })).item;
await A("PUT", `/api/v1/projects/${P.id}/compendium`, { compendium_id: C.id });
const seeded = (await A("GET", `/api/v1/projects/${P.id}/docs`)).order[0];
const doc = {};
const chapter = async (key, title, date, end = "") => {
  const d = (await A("POST", `/api/v1/projects/${P.id}/docs`, { title, kind: "chapter", content: "The tide had gone out and taken the light with it.\n" })).item;
  const cur = (await A("GET", `/api/v1/docs/${d.id}`)).item;
  await A("PATCH", `/api/v1/docs/${d.id}`, { base_revision: cur.revision, story_date: date, story_date_end: end });
  doc[key] = d.id;
};
await chapter("one", "Low water", "1998-04-12");
await chapter("two", "The ledger", "1998-03-01");
await chapter("three", "A letter from the mainland", "1999-05");
await chapter("four", "Nobody walks it in winter", "last spring");
await chapter("five", "The lantern", "2007-10");
await chapter("six", "An undated aside", "");
await A("POST", `/api/v1/docs/${seeded}/trash`);
for (const [d, e] of [["one", "wedding"], ["two", "ledger"], ["three", "letter"], ["five", "lantern"]]) await A("POST", `/api/v1/docs/${doc[d]}/timeline-events`, { event_id: ev[e] });
const empty = (await A("POST", "/api/v1/compendiums", { name: "A Fresh Bible" })).item;
const bare = (await A("POST", "/api/v1/projects", { title: "Fresh Page", kind: "novel" })).item;
const trashedEvent = ev.legend;
await A("DELETE", `/api/v1/timeline/events/${trashedEvent}`);

// A reader of the Compendium: sign them in once, then an administrator adds them.
const readerCtx = await browser.newContext();
await signIn(await readerCtx.newPage(), base, "reader@dev.scriobh");
const adminCtx = await browser.newContext();
await signIn(await adminCtx.newPage(), base, "admin@dev.scriobh");
await api(adminCtx, "POST", `/api/v1/compendiums/${C.id}/members`, { email: "reader@dev.scriobh", role: "reader" });
const cookies = await boot.cookies();
const readerCookies = await readerCtx.cookies();

const sizes = { desktop: { viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1 }, mobile: { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } };
const cpage = (path = "", slug = C.slug) => `${base}/c/${slug}/timeline${path}`;
const ppage = (q = "", slug = P.slug) => `${base}/p/${slug}/timeline${q}`;

async function states(page, shot, size) {
  const go = async (url) => { await page.goto(url, { waitUntil: "load" }); await page.evaluate(() => document.fonts.ready); await sleep(200); };
  await go(cpage());
  await shot("comp-list", { fullPage: true, at: [null, "#tl-strip", "#tl-events"] });
  await page.getByRole("link", { name: "Check timeline" }).click();
  await page.locator("#tl-check-body .tl-find").first().waitFor();
  await sleep(150);
  await page.locator("#tl-check").scrollIntoViewIfNeeded();
  await shot("comp-check", { fullPage: true, at: ["#tl-check"] });
  await go(cpage("?from=1998&to=1999"));
  await shot("comp-range", { fullPage: size === "desktop" });
  await go(cpage(`?entity=${ent.tobias}&from=2012`));
  await shot("comp-filtered-empty", { fullPage: true, at: ["#tl-events"] });
  await go(cpage("?from=1998-4"));
  await shot("comp-bad-date", { fullPage: true, at: ["#tl-events"] });
  await go(cpage("?trash=1"));
  await shot("comp-trash", { fullPage: true, at: ["#tl-events"] });
  await go(cpage("", empty.slug));
  await shot("comp-empty", { fullPage: true, at: ["#tl-events"] });
  await go(cpage("/events/new"));
  await page.getByLabel("Title").fill("The feast");
  await page.getByLabel("Starts").fill("1998-02-30");
  await page.getByLabel("Ends").fill("1998-04-01");
  await page.getByLabel("Starts").blur();
  await page.getByLabel("Ends").blur();
  await sleep(150);
  await shot("event-new-hints", { fullPage: true, at: [null, "#tl-people"] });
  const person = page.locator("[name=person_name]").first();
  await person.scrollIntoViewIfNeeded();
  await person.fill("");
  await person.pressSequentially("To", { delay: 30 });
  await page.locator("ul.tl-lb:not([hidden]) [role=option]").first().waitFor();
  await page.keyboard.press("ArrowDown");
  await sleep(100);
  await shot("event-new-picker");
  await go(cpage(`/events/${ev.wedding}`));
  await shot("event-edit", { fullPage: true, at: [null, "#tl-scenes"] });
  await page.getByRole("button", { name: "Show its scenes" }).click();
  await page.waitForSelector("#ev-scene");
  await page.locator("#tl-scenes").scrollIntoViewIfNeeded();
  await shot("event-scenes", { fullPage: true, at: ["#tl-scenes"] });
  await go(cpage(`/events/${trashedEvent}`));
  await shot("event-trashed", { fullPage: true, at: [null] });
  await go(ppage());
  await shot("book-story", { fullPage: true });
  await go(ppage("?order=narrative"));
  await page.locator(`#tl-row-${ev.wedding}`).getByRole("button", { name: /later/ }).click();
  await sleep(500);
  await page.locator(`#tl-row-${ev.wedding}`).scrollIntoViewIfNeeded();
  await shot("book-told-moved", { fullPage: true, at: ["#tl-events"] });
  await page.getByRole("button", { name: /Go back to the order the scenes give/ }).click().catch(() => {});
  await page.goto(ppage("?order=narrative"), { waitUntil: "load" });
  await api(boot, "PUT", `/api/v1/projects/${P.id}/timeline/order`, { reset: true });
  await go(ppage("?order=narrative&check=1"));
  await page.locator("#tl-check").scrollIntoViewIfNeeded();
  await shot("book-check", { fullPage: true, at: ["#tl-check"] });
  await go(ppage(`?entity=${ent.maire}&from=1998&to=1998-12`));
  await shot("book-filtered", { fullPage: true, at: ["#tl-events"] });
  await go(ppage("", bare.slug));
  await shot("book-empty", { fullPage: true, at: ["#tl-events"] });
}

for (const [size, opts] of Object.entries(sizes)) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    const tag = `${size}-${scheme}`;
    // A phone page is too long for one picture: the top, then the part that matters, each as a real viewport.
    const shot = async (name, o = {}) => {
      if (size === "mobile" && o.fullPage) {
        let i = 0;
        for (const sel of o.at || [null, "#tl-events"]) {
          const l = sel && page.locator(sel).first();
          if (sel && !(await l.count())) continue;
          if (sel) await l.evaluate((e) => e.scrollIntoView({ block: "start" })); else await page.evaluate(() => window.scrollTo(0, 0));
          await sleep(120);
          await page.screenshot({ path: join(out, `timeline-${name}${i ? "-" + (i + 1) : ""}-${tag}.png`) });
          i++;
        }
        console.log("shot", name, tag);
        return;
      }
      await page.screenshot({ path: join(out, `timeline-${name}-${tag}.png`), ...o });
      console.log("shot", name, tag);
    };
    await states(page, shot, size);
    await ctx.close();
    // The same Compendium as one of its readers.
    const rc = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await rc.addCookies(readerCookies);
    const rp = await rc.newPage();
    await rp.goto(cpage(), { waitUntil: "load" });
    await rp.evaluate(() => document.fonts.ready);
    await sleep(200);
    await rp.screenshot({ path: join(out, `timeline-comp-reader-${tag}.png`), fullPage: size === "desktop" });
    console.log("shot comp-reader", tag);
    await rc.close();
  }
}

// Three other themes, as a spot-check.
for (const [name, scheme] of [["sepia", "light"], ["slate", "dark"], ["moss", "dark"]]) {
  await A("PUT", "/api/v1/me/prefs", { items: scheme === "light" ? { theme: "light", theme_light: name } : { theme: "dark", theme_dark: name } });
  const tc = await boot.cookies();
  for (const [size, opts] of Object.entries(sizes)) {
    const ctx = await browser.newContext({ ...opts, colorScheme: scheme, serviceWorkers: "block" });
    await ctx.addCookies(tc);
    const page = await ctx.newPage();
    const shot = async (n, o = {}) => { await page.screenshot({ path: join(out, `timeline-theme-${name}-${n}-${size}.png`), ...o }); console.log("shot", name, n, size); };
    await page.goto(cpage(), { waitUntil: "load" });
    await page.evaluate(() => document.fonts.ready);
    await sleep(200);
    await shot("comp-list", { fullPage: size === "desktop" });
    await page.goto(ppage("?order=narrative&check=1"), { waitUntil: "load" });
    await sleep(300);
    await shot("book-check", { fullPage: size === "desktop" });
    await ctx.close();
  }
}
await A("PUT", "/api/v1/me/prefs", { items: { theme: "", theme_light: "paper", theme_dark: "ink" } });
await browser.close();
await srv.stop();
rm(tmp);
