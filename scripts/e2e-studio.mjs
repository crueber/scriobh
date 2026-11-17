#!/usr/bin/env node
// The Writing Studio in a real Chromium (F-1100 and up): the dashboard for a person with nothing, with a hand-built
// history and with five years of days; every number on the page against the API's and against a hand count; goals made,
// changed and archived with the keyboard; a sprint started from the dashboard and from the writing view with typing
// uninterrupted; nudges; the export; the private progress card (made, shown once, gone on a reload, revoked, opened with
// no cookie, drawn as a PNG); the progress email and nudge switches; a locked project that contributes nothing; 390 px;
// the keyboard; a reload with the network cut; right to left; forced colours.
//
//   node scripts/e2e-studio.mjs [--only substring] [--shots DIR]
//
// No op writes into the past, so the history is written into the running server's database by a Go helper
// (scripts/seed-studio.mjs); it needs only the Go toolchain the server is built with.
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { dayAgo, fiveYears, seedDays } from "./seed-studio.mjs";
import { mkdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";

const argv = process.argv.slice(2);
const SHOTS = argv.includes("--shots") ? resolve(argv[argv.indexOf("--shots") + 1]) : "";
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const kit = await createKit("e2e-studio");
kit.only = argv.includes("--only") ? argv[argv.indexOf("--only") + 1] : "";
kit.deadline(8 * 60 * 1000);
const { api, waitFor } = kit;
const base = () => kit.base();
// A full-page capture is taken in a second tab of the same person (Chromium's full-page capture leaves style attributes on the
// page it measures, which the kit would report); a page that shows something once is captured as it stands, the viewport alone.
const shot = async (page, name, opts = {}) => {
  if (!SHOTS) return;
  if (opts.full === false) { await page.screenshot({ path: join(SHOTS, name + ".png") }); return; }
  const tab = await page.context().newPage();
  await tab.emulateMedia(opts.media || {});
  await tab.goto(page.url(), { waitUntil: "load" });
  await tab.screenshot({ path: join(SHOTS, name + ".png"), fullPage: true });
  await tab.close();
};
const num = (n) => String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
const SHAME = /\bmissed\b|\bfailed\b|\bbehind\b|\bonly\b|\byou have not\b|\byou haven't\b|\bbroke\b|\blost\b|\b\d+ of 7\b|\bwarning\b/i;
const noShame = (text, where) => { const m = SHAME.exec(text); ok(!m, `${where} says "${m && m[0]}": ${text.slice(Math.max(0, (m ? m.index : 0) - 40), (m ? m.index : 0) + 60)}`); };
const noSideScroll = async (page, where) => {
  const m = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: window.innerWidth }));
  ok(m.sw <= m.iw, `${where} scrolls sideways: ${m.sw} > ${m.iw}`);
};

// ---- three people -------------------------------------------------------------------------------------------------
const { ctx: wren, page: pg } = await kit.signedIn();                       // the hand-built history, mid-streak
kit.main = pg;
const { ctx: ada, page: adaPg } = await kit.signedIn({}, "admin@dev.scriobh"); // five years of days
const { ctx: rory, page: roryPg } = await kit.signedIn({}, "reader@dev.scriobh"); // nothing at all
const me = async (ctx) => (await api(ctx, "GET", "/api/v1/me")).json.item;
const wrenId = (await me(wren)).id, adaId = (await me(ada)).id;
const mk = async (ctx, title) => (await api(ctx, "POST", "/api/v1/projects", { title, kind: "novel" })).json.item;
const book = await mk(wren, "The Salt Road");
const locked = await mk(wren, "Locked Ledger");
const adaBook = await mk(ada, "Five Winters");
const docOf = async (ctx, p) => (await api(ctx, "GET", `/api/v1/projects/${p.id}/docs`)).json.order[0];
const bookDoc = await docOf(wren, book);
const text = (n, w = "word") => Array.from({ length: n }, (_, i) => `${w}${i}`).join(" ") + "\n";
let rev = 1;
const save = async (content) => {
  const r = await api(wren, "PATCH", `/api/v1/docs/${bookDoc}`, { base_revision: rev, content });
  ok(r.status === 200, `saving answered ${r.status} ${JSON.stringify(r.json)}`);
  rev = r.json.item.revision;
};

// The hand count. Days before today, words added. The project's own daily goal is 100 (set on its Goals page), so the days
// of 100 or more are goal days, as the save loop would have frozen them.
//   the last 7 days (today and six before): 120 + 300 + 80 + 500 + 200         = 1,200 on 5 days
//   the 7 before:                            100 + 250                           =   350
//   the last 30 days:                        1,200 + 350 + 400                   = 1,950 on 8 days
//   the 30 before:                           1,000
//   the last 365 days:                       1,950 + 1,000 + 700                 = 3,650 on 10 days
//   the calendar's 24 weeks hold every day but the one 200 days ago: 9 writing days; 10 in all
//   goal days: 1 and 2 days ago (a run of 2 that today can continue), 5 and 6 (2), 8, 10, 20, 40, 200; the longest run is 2
const HAND = [{ ago: 1, words: 120 }, { ago: 2, words: 300 }, { ago: 4, words: 80 }, { ago: 5, words: 500, net: 420 }, { ago: 6, words: 200 }, { ago: 8, words: 100 },
  { ago: 10, words: 250 }, { ago: 20, words: 400 }, { ago: 40, words: 1000 }, { ago: 200, words: 700 }].map((r) => ({ ...r, target: 100, hit: r.words >= 100, hour: r.ago % 2 ? 7 : 21 }));
await api(wren, "PUT", `/api/v1/projects/${book.id}/goals`, { kind: "daily", target_words: 100 });
seedDays(kit.dataDir, wrenId, book.id, HAND);
await api(ada, "PUT", `/api/v1/projects/${adaBook.id}/goals`, { kind: "daily", target_words: 300 });
seedDays(kit.dataDir, adaId, adaBook.id, fiveYears());
const dash = async (ctx, q = "") => (await api(ctx, "GET", "/api/v1/me/studio" + q)).json;

await kit.test("a new person: the page is warm, says how it will fill, and shames nothing", async () => {
  await roryPg.goto(base() + "/studio");
  await roryPg.getByRole("heading", { name: "Writing studio", level: 1 }).waitFor();
  const body = await roryPg.locator("main").innerText();
  noShame(body, "the empty dashboard");
  ok(body.includes("This page fills in as you write"), "no welcome line");
  ok(body.includes("A goal is a number you choose"), "no invitation to set a goal");
  ok(body.includes("Nothing written in this range yet"), "the chart's empty state");
  eq(await roryPg.locator(".st-card").count(), 4, "four period cards");
  eq(await roryPg.locator(".st-card [data-num]").first().innerText(), "0", "today's words");
  eq(await roryPg.locator(".st-delta").count(), 0, "a person with nothing is shown no comparison");
  eq(await roryPg.locator(".st-cal tbody tr").count(), 7, "seven rows of days");
  ok(!(await roryPg.locator("#st-quick").count()), "a quick action with nowhere to write");
  await shot(roryPg, "new-1280-light");
  for (const p of ["/studio/goals", "/studio/sprints", "/studio/data", "/studio/card"]) {
    await roryPg.goto(base() + p);
    noShame(await roryPg.locator("main").innerText(), p);
  }
});

await kit.test("the dashboard: every number is the API's, and the hand count's", async () => {
  await pg.goto(base() + "/studio");
  const d = await dash(wren);
  const cards = Object.fromEntries(d.cards.map((c) => [c.key, c]));
  eq(cards["7d"].words, 1200, "the API's 7 days"); eq(cards["7d"].previous.words, 350, "the API's 7 days before");
  eq(cards["30d"].words, 1950, "the API's 30 days"); eq(cards["365d"].words, 3650, "the API's 365 days");
  for (const c of d.cards) eq(await pg.locator(`.st-card[data-card="${c.key}"] [data-num]`).innerText(), num(c.words), `the ${c.key} card`);
  const delta = (k) => pg.locator(`.st-card[data-card="${k}"] .st-delta`).first().innerText();
  eq(await delta("7d"), "850 more than the 7 days before (+243%)", "the 7-day comparison");
  eq(cards["7d"].delta.percent, 243, "the API's percentage");
  eq(await delta("30d"), "950 more than the 30 days before (+95%)", "the 30-day comparison");
  eq(await pg.locator('.st-card[data-card="365d"] [data-delta]').count(), 0, "a year with nothing before it is not told so, and has no percentage");
  eq(await pg.locator('.st-card[data-card="today"] [data-delta]').count(), 0, "a day that is not over is never set below yesterday");
  // a smaller period is not coloured: its text is the colour of every other comparison
  const cols = await pg.evaluate(() => [...document.querySelectorAll(".st-delta")].map((e) => getComputedStyle(e).color));
  ok(new Set(cols).size === 1, "the comparisons are not all one colour: " + [...new Set(cols)].join(" "));
  eq(await pg.locator(".st-spark polyline").count(), 4, "four sparklines");
  ok((await pg.locator('.st-card[data-card="7d"] .st-spark title').textContent()).includes("200, 500, 80, 0, 300, 120, 0"), "the 7-day sparkline's numbers as text");
  // the streak card
  eq(d.streak.current, 2, "the API's streak"); eq(d.streak.longest, 2, "the API's longest");
  eq(await pg.locator("[data-streak]").innerText(), "2", "the streak");
  eq(await pg.locator("[data-longest]").innerText(), "Longest run: 2 days", "the longest run");
  eq(await pg.locator("[data-today]").innerText(), "0 / 100 words today in The Salt Road", "today against the target, named for the project the goal belongs to");
  ok((await pg.locator("#streak").innerText()).includes("2 days in a row so far. 100 words keep it going today"), "the streak's message");
  // the calendar: 24 columns, Monday first, Mon/Wed/Fri labelled, each day named with its date and words
  eq(d.calendar.writing_days, 9, "the API's writing days in 24 weeks"); eq(d.calendar.all_time_writing_days, 10, "the API's writing days in all");
  eq(await pg.locator("[data-cal-sum]").innerText(), "9 writing days in the last 24 weeks", "the calendar's count");
  eq(await pg.locator(".st-cal").getAttribute("data-weeks"), "24", "24 weeks");
  eq((await pg.locator(".st-cal tbody th span[aria-hidden]").allInnerTexts()).join(" "), "Mon Wed Fri", "the row labels");
  eq((await pg.locator(".st-cal tbody th .sr-only").allInnerTexts()).join(" "), "Monday Tuesday Wednesday Thursday Friday Saturday Sunday", "every row is named for a screen reader");
  const cells = d.calendar.weeks.flatMap((w) => w.cells).filter((c) => !c.future);
  eq(await pg.locator(".st-cal tbody td .sr-only").count(), cells.length, "one named cell for each day so far");
  const names = await pg.locator(".st-cal tbody td .sr-only").allInnerTexts();
  const written = names.filter((n) => !/no words$/.test(n));
  eq(written.length, 9, "nine days with words");
  ok(written.some((n) => /: 500 words, daily goal met$/.test(n)) && written.some((n) => /: 80 words$/.test(n)), "the cells' names: " + written.join(" | "));
  eq(await pg.locator(".st-cal td.hit").count(), 8, "eight goal days ringed in the 24 weeks");
  ok(!(await pg.locator(".st-cal td[class*=miss], .st-cal td[class*=bad]").count()), "an empty day is marked");
  // each level is a size as well as a tint
  const sizes = await pg.evaluate(() => [0, 1, 2, 3, 4].map((l) => { const e = document.querySelector(`.st-legend .l${l}`); return parseFloat(getComputedStyle(e).borderTopWidth); }));
  ok(sizes[1] > sizes[2] && sizes[2] > sizes[3] && sizes[3] > sizes[4], "the legend's squares do not grow with the level: " + sizes.join(" "));
  // goals, chart, patterns, projects
  eq(await pg.locator("[data-goal-counts]").innerText(), `${d.goals.active} active, ${d.goals.completed} completed`, "the goal counts");
  eq(await pg.locator("[data-chart-desc]").innerText(), `${num(d.series.figures.words)} words added from ${fmtDate(d.series.from)} to ${fmtDate(d.series.to)}`, "the chart's sentence");
  eq(d.series.figures.words, 1950, "the chart's 30 days");
  eq(await pg.locator("#words .st-table tbody tr").count(), 30, "thirty rows in the chart's table");
  ok((await pg.locator("#words .st-pace").innerText()).includes(`Pace: ${d.series.avg_per_day} words a day over the whole range`), "the pace");
  ok((await pg.locator("#patterns").innerText()).includes("Most words on " + ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"][d.series.best_weekday] + "s"), "the fullest weekday");
  const row = (await pg.locator("#projects tbody tr").first().innerText()).replace(/\s+/g, " ");
  const p0 = d.projects[0];
  eq(row, `The Salt Road ${num(p0.today)} words ${num(p0.week)} words ${num(p0.previous_week)} words`, "the project's row");
  noShame(await pg.locator("main").innerText(), "the dashboard");
  await shot(pg, "streak-1280-light");
});

function fmtDate(iso) { const d = new Date(iso + "T00:00:00Z"); return `${d.getUTCDate()} ${["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"][d.getUTCMonth()]} ${d.getUTCFullYear()}`; }

await kit.test("ranges are links: the chart to all time, the calendar to all time, a custom range, and a refused one keeps what was typed", async () => {
  await pg.getByRole("link", { name: "All time", exact: true }).nth(1).click();
  await pg.waitForURL(/range=all/);
  const d = await dash(wren, "?range=all");
  eq(await pg.locator("[data-chart-desc]").innerText(), `${num(d.series.figures.words)} words added from ${fmtDate(d.series.from)} to ${fmtDate(d.series.to)}`, "the all-time chart");
  eq(d.series.figures.words, 3650, "all time by hand");
  eq(await pg.locator('#words .st-ranges a[aria-current="true"]').innerText(), "All time", "the current range");
  await pg.locator("#activity .st-ranges a", { hasText: "All time" }).click();
  await pg.waitForURL(/cal=all/);
  eq(await pg.locator("[data-cal-sum]").innerText(), "10 writing days in all", "the all-time calendar");
  ok(+(await pg.locator(".st-cal").getAttribute("data-weeks")) >= 29, "the all-time calendar is longer than 24 weeks");
  await noSideScroll(pg, "the all-time calendar");
  await pg.goto(`${base()}/studio?from=${dayAgo(6)}&to=${dayAgo(0)}#words`);
  eq(await pg.locator("[data-chart-desc]").innerText(), `1,200 words added from ${fmtDate(dayAgo(6))} to ${fmtDate(dayAgo(0))}`, "a custom range");
  eq(await pg.locator("#st-from").inputValue(), dayAgo(6), "the dates stay in the form");
  await pg.goto(`${base()}/studio?from=${dayAgo(0)}&to=${dayAgo(6)}#words`);
  ok((await pg.locator("#st-range-error").innerText()).includes("Give both dates"), "a refused range says why");
  eq(await pg.locator("#st-from").getAttribute("aria-invalid"), "true", "the field is marked");
  eq(await pg.locator("#st-from").inputValue(), dayAgo(0), "what was typed is kept");
});

await kit.test("five years of days: the page holds, and its numbers are the API's", async () => {
  const t0 = Date.now();
  await adaPg.goto(base() + "/studio?range=all&cal=all");
  const ms = Date.now() - t0;
  const d = await dash(ada, "?range=all");
  for (const c of d.cards) eq(await adaPg.locator(`.st-card[data-card="${c.key}"] [data-num]`).innerText(), num(c.words), `the ${c.key} card`);
  eq(await adaPg.locator("[data-streak]").innerText(), String(d.streak.current), "the streak");
  eq(await adaPg.locator("[data-chart-desc]").innerText(), `${num(d.series.figures.words)} words added from ${fmtDate(d.series.from)} to ${fmtDate(d.series.to)}`, "the chart");
  ok(+(await adaPg.locator(".st-cal").getAttribute("data-weeks")) >= 260, "five years of weeks");
  await noSideScroll(adaPg, "five years of calendar");
  ok(ms < 4000, `the all-time page took ${ms} ms`);
  noShame(await adaPg.locator("main").innerText(), "the long dashboard");
  await shot(adaPg, "five-years-all-1280-light");
  await adaPg.goto(base() + "/studio");
  await shot(adaPg, "five-years-1280-light");
});

await kit.test("goals: made with the keyboard, a duplicate and a bad number refused beside the field with what was typed kept", async () => {
  await pg.goto(base() + "/studio/goals");
  await pg.locator("#ng-measure").focus();
  await pg.locator("#ng-period").selectOption("week");
  await pg.locator("#ng-target").focus();
  await pg.keyboard.type("2000");
  await pg.keyboard.press("Enter");
  await pg.getByText("Goal added.").waitFor();
  ok(await pg.evaluate(() => document.activeElement && document.activeElement.hasAttribute("data-result")), "focus is not on the result");
  const top = await pg.evaluate(() => document.activeElement.getBoundingClientRect().top);
  ok(top >= 0 && top < 800, "the result is not in the first screen: " + top);
  ok((await pg.locator("#goals").innerText()).includes("Words a week"), "the new goal is listed");
  const wk = (await api(wren, "GET", "/api/v1/me/studio/goals")).json.items.find((g) => g.period === "week");
  ok((await pg.locator("#goals").innerText()).includes(`${num(wk.value)} / 2,000 words`), "this week's words against it: " + (await pg.locator("#goals").innerText()).replace(/\s+/g, " ").slice(0, 300));
  await pg.reload();
  ok(!(await pg.getByText("Goal added.").count()), "the note is shown again after a reload");
  eq(await pg.locator("#goals .st-goal").count(), 2, "a reload made no second goal");
  // the same again: refused, with a link to the one that exists
  await pg.locator("#ng-period").selectOption("week");
  await pg.locator("#ng-target").fill("900");
  await pg.getByRole("button", { name: "Add this goal" }).click();
  const err = pg.locator("#st-error");
  await err.waitFor();
  ok((await err.innerText()).includes("You already have that goal"), "the duplicate's words: " + (await err.innerText()));
  eq(await pg.locator("#ng-target").inputValue(), "900", "the typed target is kept");
  eq(await pg.locator("#ng-period").inputValue(), "week", "the chosen period is kept");
  ok((await pg.locator("#ng-period").getAttribute("aria-describedby") || "").includes("st-error"), "the error is not tied to the field");
  ok(/\/studio\/goals\/sgl_/.test(await err.locator("a").getAttribute("href")), "no link to the existing goal");
  // a number that is no target
  await pg.locator("#ng-period").selectOption("month");
  await pg.locator("#ng-target").evaluate((e) => { e.removeAttribute("min"); e.value = "0"; }); // past the browser's own check: the server's
  await pg.getByRole("button", { name: "Add this goal" }).click();
  await pg.locator("#st-error").waitFor();
  eq(await pg.locator("#ng-target").getAttribute("aria-invalid"), "true", "the target is marked");
  ok((await pg.locator("#ng-target").getAttribute("aria-describedby")).includes("st-error"), "the target names its error");
});

let totalGoal = "";
await kit.test("goals: a total with a deadline gives a forecast called an estimate; a change is a version; archiving asks first", async () => {
  await save(text(150));
  await pg.goto(base() + "/studio/goals");
  await pg.locator("#ng-period").selectOption("total");
  await pg.locator("#ng-target").fill("5000");
  await pg.locator("#ng-project").selectOption({ label: "The Salt Road" });
  await pg.locator("#ng-deadline").fill(dayAgo(-60));
  await pg.getByRole("button", { name: "Add this goal" }).click();
  await pg.getByText("Goal added.").waitFor();
  const item = pg.locator(".st-goal", { hasText: "Words in total" });
  const t = (await item.innerText()).replace(/\s+/g, " ");
  ok(t.includes("150 / 5,000 words"), "the manuscript against the total: " + t);
  ok(/About \d+ net words a day finishes by/.test(t), "the pace needed: " + t);
  ok(/net words a day/.test(t) && !/after the deadline|behind/.test(t), "the forecast does not say its units, or counts days after the deadline: " + t);
  ok(/An estimate|No date can be estimated/.test(t), "the forecast does not say it is an estimate: " + t);
  ok(/From your last 28 days/.test(t), "the forecast's basis: " + t);
  noShame(t, "the forecast");
  await item.getByRole("link", { name: "Words in total" }).click();
  await pg.waitForURL(/\/studio\/goals\/sgl_/);
  totalGoal = new URL(pg.url()).pathname.split("/").pop();
  await pg.locator("#eg-target").fill("6000");
  await pg.locator("#eg-target").press("Enter");
  await pg.getByText("Goal changed.").waitFor();
  eq(await pg.locator("#versions tbody tr").count(), 2, "two versions");
  ok((await pg.locator("#versions tbody").innerText()).includes("6,000") && (await pg.locator("#versions tbody").innerText()).includes("5,000"), "both targets are listed");
  await pg.reload();
  eq(await pg.locator("#versions tbody tr").count(), 2, "a reload made no third version");
  // archive: the first press asks, Escape puts it back, the second confirms
  await pg.getByRole("button", { name: "Archive this goal" }).click();
  await pg.getByText("Archive this goal? Its record stays").waitFor();
  await pg.keyboard.press("Escape");
  eq((await api(wren, "GET", "/api/v1/me/studio/goals")).json.items.filter((g) => g.id === totalGoal).length, 1, "Escape archived it");
  await pg.getByRole("button", { name: "Archive this goal" }).click();
  await pg.getByRole("button", { name: "Archive", exact: true }).click({ delay: 700 });
  await pg.waitForURL(/\/studio\/goals$/);
  await pg.getByText("Goal archived. Its record stays.").waitFor();
  ok((await pg.locator("#archived").innerText()).includes("Words in total"), "the archived goal is not listed");
  // an archived goal cannot be changed, even by a form that was left open
  const r = await wren.request.post(`${base()}/ui/studio/goals/${totalGoal}/update`, { form: { _csrf: await pg.locator('meta[name="csrf-token"]').getAttribute("content"), target: "7000" }, maxRedirects: 0 });
  eq(r.status(), 303, "a refused change answers with a redirect");
  await pg.goto(`${base()}/studio/goals/${totalGoal}`);
  ok((await pg.locator("#st-error").innerText()).includes("archived"), "the refusal is said on the goal's page");
  ok(!(await pg.locator("#change").count()), "an archived goal still offers a change form");
});

await kit.test("nudges: at most the server's, dismissed for the day, and switched off in Settings", async () => {
  await pg.goto(base() + "/studio");
  const d = await dash(wren);
  ok(d.today.hit && d.streak.current === 3, "today's 150 words meet the goal and continue the run: " + JSON.stringify(d.streak));
  eq(await pg.locator("[data-streak]").innerText(), "3", "the streak after today's words");
  const n = pg.locator(".st-nudges li");
  eq(await n.count(), d.nudges.length, "as many nudges as the server offers");
  ok(d.nudges.length >= 1 && d.nudges.length <= 3, "the server offered " + d.nudges.length);
  const first = await n.first().locator("span").first().innerText();
  await n.first().getByRole("button").click();
  await pg.waitForURL(/\/studio$/);
  ok(!(await pg.locator(".st-nudges").innerText().catch(() => "")).includes(first), "a dismissed nudge is still shown");
  await pg.reload();
  ok(!(await pg.locator(".st-nudges").innerText().catch(() => "")).includes(first), "a dismissed nudge came back on a reload");
  await pg.goto(base() + "/settings#studio");
  await pg.locator("#st-nudges").uncheck();
  await pg.locator("#studio").getByRole("button", { name: "Save" }).click();
  await pg.getByText("Notes are off.").waitFor();
  await pg.goto(base() + "/studio");
  eq(await pg.locator(".st-nudges li").count(), 0, "nudges with the switch off");
  eq((await dash(wren)).nudges_enabled, false, "the preference");
  await api(wren, "PUT", "/api/v1/me/prefs", { items: { "studio.nudges": "true" } });
});

await kit.test("a sprint from the dashboard opens the writing view with a quiet line; typing is not disturbed; it stops from the menu", async () => {
  await pg.goto(base() + "/studio");
  await pg.locator("#sprint").getByLabel("15 minutes").check();
  await pg.locator("#sprint").getByRole("button", { name: "Start the sprint" }).click();
  await pg.waitForURL(/\/p\/the-salt-road/);
  await pg.waitForSelector(".scriobh-prose");
  const line = pg.locator("#st-sprint");
  await waitFor(async () => /Sprint: 1[45] minutes left/.test(await line.innerText()), "the sprint line");
  ok(await pg.evaluate(() => !!document.querySelector('script[src*="sprint."]') || performance.getEntriesByType("resource").some((e) => /\/js\/sprint\./.test(e.name))), "the sprint script was not fetched for a running sprint");
  // type while the line is rewritten: the editor's text does not move and the caret stays in it
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${kit.MOD}+End`);
  await pg.keyboard.type(" typing while the clock runs", { delay: 15 });
  await kit.sleep(400);
  const before = await pg.evaluate(() => document.querySelector(".scriobh-prose").getBoundingClientRect().top);
  // six minutes pass: the script reads the sprint again and rewrites its line (its own path, not a stand-in), while a layout-shift
  // observer watches the page of text
  await pg.evaluate(() => {
    window.__shift = [];
    new PerformanceObserver((l) => { for (const e of l.getEntries()) for (const s of e.sources || []) if (s.node && s.node.nodeType === 1 && s.node.closest("#paper")) window.__shift.push(e.value); }).observe({ type: "layout-shift" });
    const n = Date.now; Date.now = () => n() + 6 * 60000;
  });
  await waitFor(async () => /Sprint: [89] minutes left/.test(await line.innerText()), "the line six minutes on", 30000);
  await pg.keyboard.type(" and on", { delay: 15 });
  await kit.sleep(300);
  eq((await pg.evaluate(() => window.__shift)).length, 0, "the text shifted while the sprint line changed");
  eq(await pg.evaluate(() => document.querySelector(".scriobh-prose").getBoundingClientRect().top), before, "the text moved");
  ok(await pg.evaluate(() => document.activeElement && document.activeElement.closest(".scriobh-prose") !== null), "the caret left the text");
  ok((await pg.locator(".scriobh-prose").innerText()).includes("typing while the clock runs and on"), "typed text is missing");
  await waitFor(async () => (await pg.locator("#save-word").innerText()) === "Saved", "the save");
  await shot(pg, "write-sprint-1280-light");
  // a second start is refused in words, and nothing navigates
  const url = pg.url();
  await line.click();
  ok(await pg.locator("#sprint-menu [data-sprint-start]").isHidden(), "a running sprint still offers a start");
  await pg.locator("#sprint-menu").getByRole("button", { name: "Stop the sprint" }).click();
  await waitFor(async () => /Sprint stopped: \d+ words/.test(await pg.locator("#toasts").innerText()), "the stop's notice");
  eq(pg.url(), url, "stopping left the page");
  ok(await line.isHidden(), "the line is still shown after the stop");
  eq((await api(wren, "GET", "/api/v1/me/studio/sprint")).json.sprint, null, "the sprint still runs");
});

await kit.test("a sprint from the writing view: the script is fetched only then, the start does not navigate, a custom length, set aside", async () => {
  await pg.goto(`${base()}/p/the-salt-road/d/${bookDoc}`);
  await pg.waitForSelector(".scriobh-prose");
  await kit.sleep(900); // the page's extensions load a moment after it
  ok(!(await pg.evaluate(() => performance.getEntriesByType("resource").some((e) => /\/js\/sprint\./.test(e.name)))), "the sprint script rides with a page that has no sprint");
  await pg.locator(".scriobh-prose").click();
  await pg.keyboard.press(`${kit.MOD}+End`);
  await pg.keyboard.type(" unsaved words before the sprint");
  const url = pg.url();
  ok(await pg.locator("#st-sprint").isHidden(), "an idle sprint line takes room in the status bar");
  await pg.locator(".avatar-btn").click();
  await pg.locator("#sprint-open").click();
  await pg.locator('#sprint-menu input[name="custom"]').fill("7");
  await pg.locator("#sprint-menu").getByRole("button", { name: "Start", exact: true }).click();
  await waitFor(async () => (await pg.locator("#st-sprint").innerText()) === "Sprint: 7 minutes left", "the line of a 7-minute sprint");
  eq(pg.url(), url, "starting a sprint left the page");
  ok((await pg.locator(".scriobh-prose").innerText()).includes("unsaved words before the sprint"), "what was typed is gone");
  ok((await pg.locator("#toasts").getAttribute("aria-live")) === "polite", "the notices are not a polite live region");
  const cur = (await api(wren, "GET", "/api/v1/me/studio/sprint")).json.sprint;
  eq(cur.planned_minutes, 7, "the sprint the server holds");
  // the end is said without taking the caret: move the clock past the end
  await pg.locator(".scriobh-prose").click();
  await pg.evaluate(() => { const n = Date.now; Date.now = () => n() + 7 * 60000 + 1000; });
  await waitFor(async () => (await pg.locator("#toasts").innerText()).includes("Time is up. Finish your sentence"), "the end's notice", 30000);
  ok(await pg.evaluate(() => document.activeElement && document.activeElement.closest(".scriobh-prose") !== null), "the end's notice took the caret");
  eq(await pg.locator("#st-sprint").innerText(), "Time is up. Finish your sentence", "the line at the end");
  await pg.locator("#st-sprint").click();
  await pg.locator("#sprint-menu").getByRole("button", { name: "Set it aside" }).click();
  await waitFor(async () => (await pg.locator("#toasts").innerText()).includes("Sprint set aside. What you wrote stays"), "the set-aside notice");
  // the statistics hold the stopped sprint and not the one set aside
  await pg.goto(base() + "/studio/sprints");
  const st = (await api(wren, "GET", "/api/v1/me/studio/sprints?range=90d")).json;
  eq(st.sprints, 1, "one sprint in the statistics");
  eq(await pg.locator(".st-facts dd").first().innerText(), "1", "the page's count");
  eq(await pg.locator(".st-facts dd").nth(1).innerText(), num(st.words), "the page's words");
  eq(await pg.locator("#stats tbody tr").count(), 1, "one recent sprint");
  ok((await pg.locator("#stats tbody").innerText()).includes("Stopped"), "its ending");
  noShame(await pg.locator("main").innerText(), "the sprints page");
  // "Write N words today" for a person with words to go
  await api(wren, "POST", "/api/v1/me/studio/goals", { period: "day", measure: "words", target: 100000 });
  await pg.goto(base() + "/studio");
  const d = await dash(wren);
  const quick = pg.locator("#st-quick");
  eq(await quick.innerText(), `Write ${num(d.today.remaining)} words today`, "the quick action");
  await quick.click();
  await pg.waitForURL(/\/p\/the-salt-road/);
  await waitFor(async () => /^Session timer: \d+ minutes?$/.test(await pg.locator("#st-sprint").innerText()), "the session timer's line");
  const s2 = (await api(wren, "GET", "/api/v1/me/studio/sprint")).json.sprint;
  eq(s2.target_words, d.today.remaining, "the timer's target");
  // a second start from the dashboard is refused in words, with a way to stop
  await pg.goto(base() + "/studio/sprints");
  ok((await pg.locator("#sprint").innerText()).includes("Session timer"), "the running timer is not shown");
  await pg.getByRole("button", { name: "Stop the sprint" }).click();
  await pg.getByText(/Sprint stopped: \d+ words/).waitFor();
  const g = (await api(wren, "GET", "/api/v1/me/studio/goals")).json.items.find((x) => x.target === 100000);
  await api(wren, "POST", `/api/v1/me/studio/goals/${g.id}/archive`);
});

await kit.test("export: CSV and Markdown are downloads with the server's own names", async () => {
  await pg.goto(base() + "/studio/data");
  const [csv] = await Promise.all([pg.waitForEvent("download"), pg.getByRole("button", { name: "Download CSV" }).click()]);
  ok(/^scriobh-writing-daily-\d{4}-\d\d-\d\d-\d{4}-\d\d-\d\d\.csv$/.test(csv.suggestedFilename()), "the CSV's name: " + csv.suggestedFilename());
  const body = readFileSync(await csv.path(), "utf8");
  ok(body.split("\r\n")[0].startsWith("day,"), "the CSV's header: " + body.slice(0, 80));
  ok(body.includes(`${dayAgo(5)},500,420`), "the day of 500 added and 420 net is not in the file");
  const [md] = await Promise.all([pg.waitForEvent("download"), pg.getByRole("button", { name: "Download Markdown" }).click()]);
  ok(/^scriobh-writing-.*\.md$/.test(md.suggestedFilename()), "the Markdown's name: " + md.suggestedFilename());
  // compare: this week and last, and other dates
  const c = (await api(wren, "GET", "/api/v1/me/studio/compare?preset=last_30")).json;
  await pg.getByRole("link", { name: "The last 30 days and the 30 before" }).click();
  await pg.waitForURL(/preset=last_30/);
  const row = (await pg.locator("#st-compare tbody tr").first().innerText()).replace(/\s+/g, " ");
  eq(row, `Words added ${num(c.a.words)} ${num(c.b.words)} ${c.words.change > 0 ? "+" : ""}${num(c.words.change)}`, "the comparison's first row");
  noShame(await pg.locator("main").innerText(), "the compare page");
});

await kit.test("the progress card: made, shown once with this server's address, a PNG, gone on a reload, opened with no cookie, revoked", async () => {
  await pg.goto(base() + "/studio/card");
  await pg.locator("#cd-label").fill("For Sam");
  await pg.locator("#cd-days").evaluate((e) => { e.removeAttribute("max"); e.value = "400"; }); // past the browser's own check: the server's
  await pg.getByRole("button", { name: "Make the card" }).click();
  await pg.locator("#st-error").waitFor();
  eq(await pg.locator("#cd-days").getAttribute("aria-invalid"), "true", "the days are marked");
  eq(await pg.locator("#cd-label").inputValue(), "For Sam", "the typed note is kept");
  ok(!(await pg.locator("#cd-proj").isChecked()), "project names are ticked without being asked");
  await pg.locator("#cd-days").fill("7");
  await pg.getByRole("button", { name: "Make the card" }).click();
  const box = pg.locator("#card-url");
  await box.waitFor();
  const link = await box.inputValue();
  ok(link.startsWith(base() + "/card/crd_"), "the link: " + link.slice(0, 60));
  ok(await box.evaluate((e) => e === document.activeElement), "focus is not on the link");
  ok(!pg.url().includes("crd_"), "the token is in the address");
  ok(await pg.evaluate((l) => !Object.keys(localStorage).some((k) => (localStorage[k] || "").includes(l)) && !Object.keys(sessionStorage).some((k) => (sessionStorage[k] || "").includes(l)), link.split("/card/")[1]), "the token is in the browser's storage");
  await shot(pg, "card-once-1280-light", { full: false });
  const [png] = await Promise.all([pg.waitForEvent("download"), pg.getByRole("button", { name: "Download as PNG" }).click()]);
  eq(png.suggestedFilename(), "writing-progress.png", "the PNG's name");
  const bytes = readFileSync(await png.path());
  ok(bytes.length > 3000 && bytes.subarray(1, 4).toString() === "PNG", "the PNG is not a picture: " + bytes.length + " bytes");
  eq(bytes.readUInt32BE(16), 1200, "the PNG's width");
  await pg.reload();
  ok(!(await pg.locator("#card-url").count()), "the link is shown again after a reload");
  eq(await pg.locator(".st-cardlist > li").count(), 1, "a reload made a second card");
  ok(!(await pg.content()).includes(link.split(".").pop()), "the secret is in the page after a reload");
  // a stranger: no cookie sent, none set, no script, no way into the application
  const anon = await kit.newContext({ viewport: { width: 390, height: 844 } });
  const ap = await anon.newPage();
  kit.watch(ap, "card");
  const resp = await ap.goto(link);
  eq(resp.status(), 200, "the card's page");
  ok(!(await resp.allHeaders())["set-cookie"], "the card's page set a cookie");
  eq((await anon.cookies()).length, 0, "a cookie after opening the card");
  const pub = await ap.content();
  ok(!/<script/i.test(pub), "the card's page has a script");
  ok(!/href="\/(?!card\/|static\/)/.test(pub), "the card's page links into the application");
  ok(!pub.includes("The Salt Road") && !pub.includes("Wren") && !pub.includes("For Sam"), "the card names a project, the person or the note");
  const d = await dash(wren);
  ok((await ap.locator("main").innerText()).includes(`${d.streak.current} days in a row`), "the card's streak: " + (await ap.locator("main").innerText()).slice(0, 200));
  await noSideScroll(ap, "the card at 390 px");
  await shot(ap, "card-public-390-light", { full: false });
  await ap.emulateMedia({ colorScheme: "dark" });
  await shot(ap, "card-public-390-dark", { full: false });
  eq((await ap.goto(link + "/card.svg")).status(), 200, "the card's picture");
  await pg.reload();
  ok((await pg.locator("[data-views]").first().innerText()).startsWith("Opened 1 time"), "the view count: " + (await pg.locator("[data-views]").first().innerText()));
  // revoke: asks first, then the link opens nothing
  await pg.getByRole("button", { name: "Revoke" }).click();
  await pg.getByText("Revoke this card? Its link stops working at once.").waitFor();
  await pg.getByRole("button", { name: "Revoke", exact: true }).last().click({ delay: 700 });
  await pg.getByText("Card revoked. Its link opens nothing now.").waitFor();
  kit.allowed = [/404/];
  eq((await ap.goto(link)).status(), 404, "a revoked card");
  await anon.close();
});

await kit.test("the progress email: off by default, said plainly when mail is off, turned on and off when it is on", async () => {
  await pg.goto(base() + "/settings#progress");
  const sec = pg.locator("#progress");
  ok((await sec.innerText()).includes("Mail is not switched on for this server"), "mail off: " + (await sec.innerText()).slice(0, 200));
  ok(!(await sec.locator("form").count()), "a switch with nothing behind it");
  eq((await api(wren, "GET", "/api/v1/me/mail/progress")).json.enabled, false, "the progress email is on by default");
  const mc = await api(ada, "PUT", "/api/v1/admin/mail", { host: "127.0.0.1", port: 2525, security: "none", allow_private: true, from_address: "scriobh@example.org", enabled: true });
  ok(mc.status === 200, "switching mail on: " + mc.status + " " + JSON.stringify(mc.json));
  await pg.reload();
  ok((await sec.innerText()).includes("Off. Nothing is sent"), "off by default: " + (await sec.innerText()).slice(0, 300));
  ok(!(await sec.locator('input[name="frequency"]:checked').count()), "a frequency is chosen for the person");
  ok((await sec.innerText()).includes("writer@dev.scriobh"), "the address is not shown");
  await sec.getByRole("button", { name: "Turn it on" }).click();
  await pg.getByText("Choose weekly or monthly.").waitFor();
  await sec.getByLabel("Monthly").check();
  await sec.getByRole("button", { name: "Turn it on" }).click();
  await pg.getByText("The progress email is on.").waitFor();
  ok((await sec.innerText()).includes("On: on the first of each month"), "the state: " + (await sec.innerText()).slice(0, 300));
  eq((await api(wren, "GET", "/api/v1/me/mail/progress")).json.frequency, "monthly", "the stored frequency");
  await sec.getByRole("button", { name: "Turn it off" }).click();
  await pg.getByText("The progress email is off. Nothing will be sent.").waitFor();
  eq((await api(wren, "GET", "/api/v1/me/mail/progress")).json.enabled, false, "still on");
  await api(ada, "PUT", "/api/v1/admin/mail", { enabled: false });
});

await kit.test("roles and a locked project: a reader starts no sprint, and a locked project gives no word, day or title", async () => {
  const week = async () => (await dash(wren)).cards.find((c) => c.key === "7d").words;
  const mine = await week();
  seedDays(kit.dataDir, wrenId, locked.id, [{ ago: 1, words: 5000 }, { ago: 3, words: 7000 }]);
  eq(await week(), mine + 12000, "the second project counts while it is not locked");
  const r = await api(wren, "PUT", `/api/v1/projects/${locked.id}/passphrase`, { passphrase: "correct horse battery staple", idle_minutes: 30 });
  ok(r.status === 200, "setting the passphrase: " + r.status + " " + JSON.stringify(r.json));
  await api(wren, "POST", `/api/v1/projects/${locked.id}/lock`);
  await pg.goto(base() + "/studio?range=all&cal=all");
  const d = await dash(wren, "?range=all");
  eq(d.hidden_projects, 1, "the API's count of locked projects");
  eq(await pg.locator('.st-card[data-card="7d"] [data-num]').innerText(), num(mine), "the 7 days without the locked project");
  const body = await pg.locator("body").innerText();
  ok(!body.includes("Locked Ledger"), "the locked project is named on the dashboard");
  const leak = /5,000|7,000|12,000|5,120/.exec(body);
  ok(!leak, "the locked project's words are on the dashboard: ..." + (leak && body.slice(Math.max(0, leak.index - 60), leak.index + 40).replace(/\s+/g, " ")));
  ok(body.includes("1 project is locked and not counted."), "the note about what is left out");
  const csv = await (await wren.request.get(`${base()}/api/v1/me/studio/export?format=csv&table=daily&range=30d`)).text();
  ok(csv.includes(`${dayAgo(1)},120,`) && !/,7000,|,5120,/.test(csv), "the export holds the locked project's days");
  for (const p of ["/studio/goals", "/studio/sprints", "/studio/data"]) {
    await pg.goto(base() + p);
    const html = await pg.locator("main").innerHTML();
    ok(html.includes("The Salt Road") && !html.includes("Locked Ledger"), p + " offers or names the locked project");
  }
  // a sprint on it is refused in words and nothing starts
  const csrf = await pg.locator('meta[name="csrf-token"]').getAttribute("content");
  const res = await wren.request.post(`${base()}/ui/studio/sprint/start`, { form: { _csrf: csrf, project_id: locked.id, minutes: "15", back: "sprints" }, maxRedirects: 0 });
  eq(res.status(), 303, "the refused start");
  await pg.goto(base() + "/studio/sprints");
  ok((await pg.locator("#st-error").innerText()).length > 10, "the refusal is not said");
  eq((await api(wren, "GET", "/api/v1/me/studio/sprint")).json.sprint, null, "a sprint started on a locked project");
  // a reader: the pages open, no sprint form, and a posted start is refused
  await roryPg.goto(base() + "/studio/sprints");
  ok(!(await roryPg.locator("#sp-project").count()), "a reader is offered a sprint");
  ok((await roryPg.locator("#sprint").innerText()).includes("A sprint runs on a project you can write in"), "the reader's line");
});

await kit.test("the outline and the tree show a document's word target in words", async () => {
  const r = await api(wren, "PATCH", `/api/v1/docs/${bookDoc}`, { base_revision: (await api(wren, "GET", `/api/v1/docs/${bookDoc}`)).json.item.revision, target_words: 2000 });
  ok(r.status === 200, "setting the target: " + r.status);
  const meta = (await api(wren, "GET", `/api/v1/projects/${book.id}/docs`)).json.items.find((m) => m.id === bookDoc);
  ok(meta.progress > 0 && meta.progress < 1, "the API's progress: " + meta.progress);
  await pg.goto(`${base()}/p/the-salt-road/d/${bookDoc}`);
  const rowEl = pg.locator(`#tr-${bookDoc}`);
  ok((await rowEl.innerText()).replace(/\s+/g, "").includes("/2,000"), "the tree row: " + (await rowEl.innerText()));
  const desc = await rowEl.getAttribute("aria-describedby");
  ok((await pg.locator("#" + desc).innerText()).includes("target 2,000 words") || (await pg.locator("#" + desc).textContent()).includes("target 2,000 words"), "the tree row's description");
  await pg.goto(base() + "/p/the-salt-road/outline");
  const o = (await pg.locator(`#o-${bookDoc}`).innerText()).replace(/\s+/g, " ");
  ok(/words of 2,000/.test(o), "the outline row: " + o);
  eq(await pg.locator(`#o-${bookDoc} progress`).count(), 1, "the outline's bar");
});

await kit.test("390 px: nothing scrolls sideways, the calendar fits or scrolls in its own region, controls are 44 px", async () => {
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  const page = phone.page;
  for (const p of ["/studio", "/studio?range=all&cal=all", "/studio/goals", "/studio/sprints", "/studio/data", "/studio/card", "/settings"]) {
    await page.goto(base() + p);
    await noSideScroll(page, p + " at 390 px");
    const small = await page.evaluate(() => [...document.querySelectorAll("main .btn, main input:not([type=hidden]):not([type=checkbox]):not([type=radio]), main select, main .st-tabs a, main .st-ranges a, main summary")]
      .filter((e) => e.offsetParent !== null && e.getBoundingClientRect().height < 44).map((e) => e.tagName + "." + e.className + ":" + Math.round(e.getBoundingClientRect().height)));
    ok(small.length === 0, p + ": a control is under 44 px tall: " + small.slice(0, 6).join(", "));
  }
  await page.goto(base() + "/studio");
  const m = await page.evaluate(() => { const s = document.querySelector("#activity .st-scroll"), c = document.querySelector(".st-cal td i").getBoundingClientRect(); return { sw: s.scrollWidth, cw: s.clientWidth, cell: c.width, tab: s.tabIndex }; });
  ok(m.sw <= m.cw + 1, `24 weeks do not fit a phone: ${m.sw} in ${m.cw}`);
  ok(m.cell >= 10, "the calendar's squares are too small: " + m.cell);
  eq(m.tab, 0, "the calendar's region is not focusable");
  eq((await page.locator('.tabbar .tab[aria-current="page"]').innerText()).trim(), "Studio", "the phone's tab");
  await shot(page, "streak-390-light");
  await page.goto(base() + "/studio?cal=all#activity");
  const all = await page.evaluate(() => { const s = document.querySelector("#activity .st-scroll"); return { sw: s.scrollWidth, cw: s.clientWidth }; });
  ok(all.sw > all.cw, "the all-time calendar should scroll inside its region");
  // the result of an action is focused and inside the first screen
  await page.goto(base() + "/studio/goals");
  await page.locator("#ng-period").selectOption("month");
  await page.locator("#ng-target").fill("20000");
  await page.getByRole("button", { name: "Add this goal" }).tap();
  await page.getByText("Goal added.").waitFor();
  const r = await page.evaluate(() => { const e = document.activeElement; const b = e.getBoundingClientRect(); return { result: e.hasAttribute("data-result"), top: b.top, bottom: b.bottom, h: window.innerHeight }; });
  ok(r.result && r.top >= 0 && r.bottom <= r.h, "the result is not focused inside the first screen: " + JSON.stringify(r));
  await shot(page, "goals-390-light");
  await phone.ctx.close();
});

await kit.test("a phone has no status bar: the sprint is started and stopped from More, the minutes are in the menu, and the page stays", async () => {
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  const page = phone.page;
  await page.goto(`${base()}/p/the-salt-road/d/${bookDoc}`);
  await page.waitForSelector(".scriobh-prose");
  const url = page.url();
  await page.locator('.tabbar button[popovertarget="account"]').tap();
  await page.locator("#sprint-open").tap();
  const small = await page.evaluate(() => [...document.querySelectorAll("#sprint-menu .menu-item, #sprint-menu .btn, #sprint-menu input[type=number]")].filter((e) => e.offsetParent !== null && e.getBoundingClientRect().height < 44).map((e) => e.textContent.trim() + ":" + Math.round(e.getBoundingClientRect().height)));
  ok(small.length === 0, "a control of the sprint menu is under 44 px: " + small.join(", "));
  await shot(page, "write-sprint-menu-390-light", { full: false });
  await page.locator("#sprint-menu").getByRole("button", { name: "15 minutes" }).tap();
  await waitFor(async () => (await page.locator("#toasts").innerText()).includes("A 15-minute sprint has started"), "the start's notice");
  eq(page.url(), url, "starting a sprint left the page");
  ok(await page.locator("#sprint-menu").isHidden() && await page.locator("#account").isHidden(), "the menus stay open over the text");
  ok(await page.evaluate(() => !document.activeElement.closest(".scriobh-prose")), "a touch screen was given the keyboard after a start from More");
  ok(await page.evaluate(() => document.activeElement !== document.body), "focus fell to the page after a start from More");
  await noSideScroll(page, "the writing view with a sprint at 390 px");
  await page.locator('.tabbar button[popovertarget="account"]').tap();
  await page.locator("#sprint-open").tap();
  eq(await page.locator("#sprint-menu [data-sprint-line]").textContent(), "Sprint: 15 minutes left", "the minutes in the menu");
  await page.locator("#sprint-menu").getByRole("button", { name: "Set it aside" }).tap();
  await waitFor(async () => (await page.locator("#toasts").innerText()).includes("Sprint set aside"), "the set-aside notice");
  eq((await api(wren, "GET", "/api/v1/me/studio/sprint")).json.sprint, null, "the sprint still runs");
  await phone.ctx.close();
});

await kit.test("the keyboard: tabs, ranges, the calendar's region, the table of numbers, a goal's form", async () => {
  await pg.goto(base() + "/studio");
  const seen = new Set();
  for (let i = 0; i < 90; i++) {
    await pg.keyboard.press("Tab");
    const k = await pg.evaluate(() => { const e = document.activeElement; if (!e || e === document.body) return ""; const r = e.getBoundingClientRect(); const o = getComputedStyle(e); return (e.closest("#activity .st-scroll") === e ? "calendar" : e.closest(".st-tabs") ? "tabs" : e.closest("#words .st-ranges") ? "ranges" : e.closest("#sprint") ? "sprint" : e.closest("#goals") ? "goals" : e.closest(".st-more") ? "more" : "") + "|" + (o.outlineStyle !== "none" || o.boxShadow !== "none" ? "ring" : "noring") + "|" + (r.width > 0); });
    if (k) seen.add(k.split("|")[0] + (k.includes("noring") ? "!" : ""));
  }
  for (const want of ["tabs", "calendar", "ranges", "goals", "sprint", "more"]) ok(seen.has(want), `Tab does not reach ${want} with a visible focus: ${[...seen].join(" ")}`);
  await pg.locator("#words .st-more summary").first().focus();
  await pg.keyboard.press("Enter");
  ok(await pg.locator("#words .st-table").isVisible(), "Enter does not open the table of numbers");
});

await kit.test("offline: a reload with the network cut says so, and comes back", async () => {
  const c = await kit.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: "allow" });
  const page = await c.newPage();
  const { signIn } = await import("./harness.mjs");
  await signIn(page, base());
  await page.goto(base() + "/studio");
  await kit.waitFor(() => page.evaluate(async () => { const r = await navigator.serviceWorker.ready; return r.active && r.active.state === "activated" && !!navigator.serviceWorker.controller; }), "the worker", 20000).catch(() => {});
  await page.reload({ waitUntil: "load" });
  await c.setOffline(true);
  try {
    await page.reload({ waitUntil: "load", timeout: 15000 }).catch(() => {});
    const t = await page.locator("body").innerText();
    ok(/cannot reach the server|offline/i.test(t), "the offline notice: " + t.slice(0, 120));
    ok(!/\d+ words/.test(t), "an offline page shows numbers it cannot have");
  } finally { await c.setOffline(false); }
  await page.reload({ waitUntil: "load" });
  await page.getByRole("heading", { name: "Writing studio", level: 1 }).waitFor();
  await c.close();
});

await kit.test("right to left, the dark themes and forced colours", async () => {
  await api(wren, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } });
  for (const p of ["/studio", "/studio/goals", "/studio/sprints", "/studio/data", "/studio/card", "/settings"]) {
    await pg.goto(base() + p);
    eq(await pg.locator("html").getAttribute("dir"), "rtl", p + " is not right to left");
    await noSideScroll(pg, p + " right to left");
    if (p === "/studio") {
      await shot(pg, "streak-1280-rtl");
      const stray = await pg.evaluate(() => [...document.querySelectorAll("main *")].filter((e) => e.children.length === 0 && /^[A-Za-z][a-z]{3,}( [a-z]{3,})+$/.test(e.textContent.trim()) && !e.closest("bdi") && !e.closest("option")).map((e) => e.textContent.trim()).slice(0, 5));
      ok(stray.length === 0, "plain English on the right-to-left dashboard: " + stray.join(" | "));
    }
  }
  await api(wren, "PUT", "/api/v1/me/prefs", { items: { language: "en" } });
  for (const [name, items] of [["slate", { theme: "dark", theme_dark: "slate" }], ["moss", { theme: "dark", theme_dark: "moss" }], ["sepia", { theme: "light", theme_light: "sepia" }]]) {
    await api(wren, "PUT", "/api/v1/me/prefs", { items });
    await pg.goto(base() + "/studio");
    // the calendar's fullest square and the chart's line stand out from the surface in every theme
    const c = await pg.evaluate(() => { const lum = (s) => { const m = s.match(/[\d.]+/g).map(Number); const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(m[0]) + 0.7152 * f(m[1]) + 0.0722 * f(m[2]); };
      const a = lum(getComputedStyle(document.querySelector(".st-legend .l4")).backgroundColor), b = lum(getComputedStyle(document.querySelector(".st-panel")).backgroundColor); return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05); });
    ok(c >= 3, `${name}: the accent on the panel is ${c.toFixed(2)}:1`);
    await shot(pg, `streak-1280-${name}`);
  }
  await api(wren, "PUT", "/api/v1/me/prefs", { items: { theme: "system", theme_dark: "ink", theme_light: "paper" } });
  await pg.emulateMedia({ forcedColors: "active" });
  await pg.goto(base() + "/studio");
  const f = await pg.evaluate(() => { const g = (s) => getComputedStyle(document.querySelector(s)); return { l4: g(".st-legend .l4").backgroundColor, l0: g(".st-legend .l0").backgroundColor, w1: g(".st-legend .l1").borderTopWidth, w3: g(".st-legend .l3").borderTopWidth, line: g(".st-plot .ln") ? g(".st-plot .ln").stroke : "", page: g("body").backgroundColor }; });
  ok(f.l4 !== f.l0, "in forced colours a full day looks like an empty one: " + JSON.stringify(f));
  ok(parseFloat(f.w1) > parseFloat(f.w3), "in forced colours the levels are not sizes: " + JSON.stringify(f));
  ok(f.line && f.line !== f.page, "in forced colours the chart's line is the page's colour");
  await shot(pg, "streak-1280-forced", { media: { forcedColors: "active" } });
  await pg.emulateMedia({ forcedColors: "none" });
});

await kit.test("the fix round: a sprint ended elsewhere is told as it ended, focus goes to the text, the bar's neighbours stay put at 1024 px", async () => {
  const page = pg;
  await page.setViewportSize({ width: 1024, height: 800 });
  await page.goto(`${base()}/p/the-salt-road/d/${bookDoc}`);
  await page.waitForSelector(".scriobh-prose");
  // enough words today that the figure in the bar has four digits (the widest it gets at 1024)
  const cur = (await api(wren, "GET", `/api/v1/docs/${bookDoc}`)).json.item;
  const w1 = await api(wren, "PATCH", `/api/v1/docs/${bookDoc}`, { base_revision: cur.revision, content: text(2100, "fx") });
  ok(w1.status === 200, "writing the day's words: " + w1.status); rev = w1.json.item.revision;
  await page.reload();
  await page.waitForSelector(".scriobh-prose");
  await page.locator(".scriobh-prose").click();
  await page.keyboard.type(" ");
  await waitFor(async () => (await page.locator("#st-today").isVisible()) && /Today [\d,]{5,} of/.test(await page.locator("#st-today").innerText()), "the day's figure in the status bar");
  // start a one-minute sprint from the account menu: focus lands in the text, not on the page
  await page.locator(".avatar-btn").click();
  await page.locator("#sprint-open").click();
  await page.locator('#sprint-menu input[name="custom"]').fill("1");
  await page.locator("#sprint-menu").getByRole("button", { name: "Start", exact: true }).click();
  await waitFor(async () => /Sprint: 1 minute left/.test(await page.locator("#st-sprint").innerText()), "the sprint line");
  ok(await page.evaluate(() => document.activeElement && document.activeElement.closest(".scriobh-prose") !== null), "focus after starting from the menu is not in the text: " + (await page.evaluate(() => document.activeElement && document.activeElement.tagName + "#" + document.activeElement.id)));
  // the bar's neighbours do not move when the line says "Time is up" (and do not touch each other)
  const rects = () => page.evaluate(() => Object.fromEntries(["st-pos", "st-counts", "st-sprint", "st-today", "st-source"].map((id) => { const e = document.getElementById(id); if (!e || e.hidden || !e.offsetParent) return [id, null]; const r = e.getBoundingClientRect(); return [id, [Math.round(r.left * 10) / 10, Math.round(r.right * 10) / 10]]; })));
  const before = await rects();
  await page.evaluate(() => { const n = Date.now; Date.now = () => n() + 62000; });
  await waitFor(async () => (await page.locator("#st-sprint").innerText()) === "Time is up. Finish your sentence", "the line at the end", 30000);
  const after = await rects();
  for (const k of ["st-sprint", "st-today", "st-source"]) eq(JSON.stringify(after[k]), JSON.stringify(before[k]), `${k} moved when the sprint line changed`);
  const seq = ["st-pos", "st-counts", "st-sprint", "st-today", "st-source"].map((k) => after[k]).filter(Boolean);
  for (let i = 1; i < seq.length; i++) ok(seq[i][0] >= seq[i - 1][1] - 0.5, `the status bar's items overlap: ${JSON.stringify(after)}`);
  await shot(page, "write-sprint-1024-over", { full: false });
  if (SHOTS) await page.reload(); // a capture leaves style attributes on the page it was taken of; the page the kit watches is a fresh one
  // ended in another tab (stopped through the API): the toast names that sprint and says stopped, not finished
  await page.evaluate(() => document.querySelector("#toasts").replaceChildren());
  const stopped = (await api(wren, "POST", "/api/v1/me/studio/sprint/stop")).json.item;
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  await waitFor(async () => (await page.locator("#toasts").innerText()).includes(`Sprint stopped: ${num(stopped.words_added)} words`), `the toast of the stopped sprint (${stopped.words_added} words): ` + (await page.locator("#toasts").innerText()));
  eq(stopped.state, "stopped", "the sprint the API stopped");
  // set aside in another tab: nothing is told, and certainly not another sprint's words
  await page.locator(".avatar-btn").click();
  await page.locator("#sprint-open").click();
  await page.locator('#sprint-menu input[name="custom"]').fill("5");
  await page.locator("#sprint-menu").getByRole("button", { name: "Start", exact: true }).click();
  await waitFor(async () => /Sprint: 5 minutes left/.test(await page.locator("#st-sprint").innerText()), "the second sprint's line");
  await page.evaluate(() => document.querySelector("#toasts").replaceChildren());
  await api(wren, "POST", "/api/v1/me/studio/sprint/abandon");
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  await waitFor(async () => await page.locator("#st-sprint").isHidden(), "the line after another tab set the sprint aside");
  await kit.sleep(1200);
  const told = await page.locator("#toasts").innerText();
  ok(!/Sprint (stopped|finished)/.test(told), "a sprint that was set aside was told as finished: " + told);
  await page.setViewportSize({ width: 1280, height: 800 });
});

await kit.test("the fix round: a day's figure by click and by key, the all-time calendar at its newest week, the chart's labels right to left, no negative difference between unequal spans", async () => {
  await pg.goto(base() + "/studio");
  await pg.locator(".st-cal td:has(.sr-only) i").first().scrollIntoViewIfNeeded();
  const cell = pg.locator(".st-cal td.l4, .st-cal td.l3").first();
  await cell.click();
  const named = (await cell.locator(".sr-only").innerText()).trim();
  eq((await pg.locator(".st-day").innerText()).trim(), named, "the day under the calendar after a click");
  await pg.locator('#activity .st-scroll').focus();
  const was = (await pg.locator(".st-day").innerText()).trim();
  await pg.keyboard.press("ArrowLeft");
  const now = (await pg.locator(".st-day").innerText()).trim();
  ok(now !== was && /\d/.test(now), `the arrow key did not move to another day: "${was}" then "${now}"`);
  await pg.keyboard.press("ArrowDown");
  ok(/\d/.test(await pg.locator(".st-day").innerText()), "the down arrow lost the day");
  // the all-time calendar on a phone opens at the newest week
  await adaPg.setViewportSize({ width: 390, height: 844 });
  await adaPg.goto(base() + "/studio?cal=all#activity");
  const sc = await adaPg.evaluate(() => { const s = document.querySelector("#activity .st-scroll"); return { left: s.scrollLeft, sw: s.scrollWidth, cw: s.clientWidth }; });
  ok(sc.sw > sc.cw && sc.left + sc.cw >= sc.sw - 2, "the all-time calendar does not open at its newest week: " + JSON.stringify(sc));
  await adaPg.setViewportSize({ width: 1280, height: 800 });
  // the default compare is the finished days of the week against the same days of last week, and says so; on a Monday there is
  // nothing finished yet: one column, no difference, and a note that it starts tomorrow
  await pg.goto(base() + "/studio/data");
  const wk = (await api(wren, "GET", "/api/v1/me/studio/compare?preset=this_week")).json;
  const heads = (await pg.locator("#st-compare thead th").allInnerTexts()).join(" | ");
  const cmpText = await pg.locator("#compare").innerText();
  if (wk.compared) {
    ok(/, so far/.test(heads) && /, the same days/.test(heads), "the compare heads do not say what the sides are: " + heads);
    ok(/finished day/.test(cmpText), "no note that the sides are finished days");
  } else {
    eq(await pg.locator("#st-compare thead th").count(), 1, "a week with no finished day has more than one column: " + heads);
    ok(cmpText.includes("The comparison starts tomorrow") && !/Difference/.test(cmpText) && !cmpText.includes("\u2212"), "a Monday's compare: " + cmpText);
  }
  // the month, whichever day it is, is compared over finished days (or starts tomorrow on the 1st)
  await pg.goto(base() + "/studio/data?preset=this_month");
  const mo = (await api(wren, "GET", "/api/v1/me/studio/compare?preset=this_month")).json;
  const mt = await pg.locator("#compare").innerText();
  ok(mo.compared ? /finished day/.test(mt) : mt.includes("The comparison starts tomorrow"), "the month's compare note: " + mt);
  // two dates of different lengths have no difference worked out
  await pg.goto(base() + `/studio/data?a_from=${dayAgo(6)}&a_to=${dayAgo(0)}&b_from=${dayAgo(20)}&b_to=${dayAgo(18)}`);
  const diff = await pg.locator("#st-compare tbody tr td:last-child").allInnerTexts();
  ok(diff.length === 5 && diff.every((x) => x.trim() === "—"), "a difference between 7 days and 3: " + diff.join(" "));
  // right to left: the chart's labels sit under the line they belong to, inside the picture
  await api(wren, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } });
  await pg.goto(base() + "/studio");
  const g = await pg.evaluate(() => {
    const svg = document.querySelector(".st-plot svg").getBoundingClientRect();
    const labels = [...document.querySelectorAll(".st-x span")].map((e) => e.getBoundingClientRect());
    const d = document.querySelector(".st-plot .ln").getAttribute("d");
    const pts = d.match(/[ML]\s*([\d.]+)/g).map((m) => parseFloat(m.replace(/[ML\s]/g, "")));
    const flipped = getComputedStyle(document.querySelector(".st-plot svg")).transform !== "none";
    return { svg: [svg.left, svg.right], labels: labels.map((r) => [r.left, r.right]), first: pts[0], last: pts[pts.length - 1], flipped };
  });
  ok(g.flipped, "the chart is not mirrored in a right-to-left language");
  ok(g.labels.every((r) => r[0] >= g.svg[0] - 2 && r[1] <= g.svg[1] + 2), "a label overhangs the picture: " + JSON.stringify(g));
  ok(g.labels[0][0] > g.labels[g.labels.length - 1][0], "the oldest label is not at the right: " + JSON.stringify(g));
  await api(wren, "PUT", "/api/v1/me/prefs", { items: { language: "en" } });
});

if (SHOTS) {
  await kit.test("screenshots: goals, sprints, compare, the card page, settings, dark", async () => {
    for (const [p, n] of [["/studio/goals", "goals"], ["/studio/sprints", "sprints"], ["/studio/data", "data"], ["/studio/card", "card"], ["/settings#progress", "settings"], [`/p/the-salt-road/goals`, "project-goals"]]) {
      await pg.goto(base() + p);
      await shot(pg, n + "-1280-light");
    }
    await pg.goto(base() + "/studio");
    await shot(pg, "streak-1280-dark", { media: { colorScheme: "dark" } });
    await adaPg.goto(base() + "/studio?range=365d");
    await shot(adaPg, "five-years-1280-dark", { media: { colorScheme: "dark" } });
    const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 }, "admin@dev.scriobh");
    await phone.page.goto(base() + "/studio");
    await shot(phone.page, "five-years-390-light");
    await shot(phone.page, "five-years-390-dark", { media: { colorScheme: "dark" } });
    const np = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 }, "reader@dev.scriobh");
    await np.page.goto(base() + "/studio");
    await shot(np.page, "new-390-light");
    await phone.ctx.close(); await np.ctx.close();
  });
}

await kit.finish("e2e-studio");
