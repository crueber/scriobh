#!/usr/bin/env node
// End-to-end test of the Goals page in a real Chromium: setting targets with the keyboard, words
// added against the target, a cut never counting against the day, the calendar's text, the
// project target and its pace, and the page at 390 px.
//
//   node scripts/e2e-goals.mjs [--only substring]
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-goals");
kit.only = process.argv.includes("--only") ? process.argv[process.argv.indexOf("--only") + 1] : "";
const { api, waitFor } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

const made = await api(main, "POST", "/api/v1/projects", { title: "Goals book", kind: "novel" });
const proj = made.json.item;
const docId = (await api(main, "GET", `/api/v1/projects/${proj.id}/docs`)).json.order[0];
const goalsUrl = `${base()}/p/${proj.slug}/goals`;
const text = (n) => Array.from({ length: n }, (_, i) => `word${i}`).join(" ") + "\n";
let rev = 1;
const save = async (content) => {
  const r = await api(main, "PATCH", `/api/v1/docs/${docId}`, { base_revision: rev, content });
  ok(r.status === 200, `saving answered ${r.status} ${JSON.stringify(r.json)}`);
  rev = r.json.item.revision;
};
const inTwoWeeks = new Date(Date.now() + 14 * 864e5).toISOString().slice(0, 10);

await kit.test("goals: the empty page invites a target, and the keyboard reaches it", async () => {
  await pg.goto(goalsUrl);
  await pg.getByRole("heading", { name: "Goals", exact: true }).waitFor();
  const body = await pg.locator("body").innerText();
  ok(body.includes("No daily target yet"), "no invitation to set a daily target");
  ok(body.includes("never counts against you"), "the promise about cutting is missing");
  ok(!/\bmissed\b|\bbehind\b|\bfailed\b/i.test(body), "the page uses shaming words");
  eq(await pg.locator(".ptabs a[aria-current=page]").innerText(), "Goals", "the current tab");
  // Tab through the page until the daily field has focus.
  let found = false;
  for (let i = 0; i < 40 && !found; i++) {
    await pg.keyboard.press("Tab");
    found = await pg.evaluate(() => document.activeElement && document.activeElement.id === "g-daily");
  }
  ok(found, "the daily field cannot be reached with Tab");
  await pg.keyboard.type("300");
  await pg.keyboard.press("Enter");
  await pg.getByText("Daily target saved.").waitFor();
  eq(await pg.locator("#g-daily").inputValue(), "300", "the saved target is shown in the field");
  await pg.reload();
  eq(await pg.locator("#g-daily").inputValue(), "300", "the target after a reload");
});

await kit.test("goals: words added meet the target, the calendar says so in words, and a cut never counts against the day", async () => {
  await save(text(350));
  await pg.goto(goalsUrl);
  const today = pg.locator("#today");
  await waitFor(async () => (await today.innerText()).includes("350 words added"), "today's words");
  const t = await today.innerText();
  ok(t.includes("Daily target of 300 met."), "target not marked met: " + t);
  ok(/Streak\s*1 day/.test(t.replace(/\n/g, " ")), "streak: " + t);
  const cell = pg.locator("td.day.today");
  ok((await cell.getAttribute("class")).includes("hit"), "today's cell lacks the tick class");
  const label = await cell.locator(".sr-only").textContent();
  ok(/350 words added, goal met, today/.test(label), "the cell's text: " + label);
  eq(await pg.locator("td.day").count(), 30, "thirty days in the calendar");
  // Cut almost everything: the day keeps its credit and says so calmly.
  await save(text(40));
  await pg.reload();
  const t2 = await pg.locator("#today").innerText();
  ok(t2.includes("350 words added"), "a cut took words away from the day: " + t2);
  ok(/Net change today: 40 words/.test(t2), "the net change is not shown: " + t2.split("\n").join(" "));
  ok(t2.includes("never counts against the day"), "no calm note about cutting");
  ok(t2.includes("Daily target of 300 met."), "a cut un-met the day");
});

await kit.test("goals: 90 days, and the project target with its pace", async () => {
  await pg.getByRole("link", { name: "90 days" }).click();
  await pg.waitForURL(/days=90/);
  eq(await pg.locator("td.day").count(), 90, "ninety days");
  eq(await pg.locator(".ranges a[aria-current=true]").innerText(), "90 days", "the current range");
  await pg.locator("#g-own").fill("1000");
  await pg.locator("#g-own-end").fill(inTwoWeeks);
  await pg.getByRole("button", { name: "Save my target" }).click();
  await pg.getByText("Your target saved.").waitFor();
  const t = (await pg.locator("#target").innerText()).replace(/\n/g, " ");
  ok(/40\s*of 1,000 words/.test(t), "progress: " + t);
  ok(/target you set for yourself/.test(t), "source line: " + t);
  ok(/To finish by .* the project needs about \d+ words a day/.test(t), "pace: " + t);
  ok(!/at that pace it would reach/i.test(t), "a projection was shown without enough writing days: " + t);
  // The owner can also set the project's shared target; the page says what each one is.
  await pg.locator(".shared summary").click();
  await pg.locator("#g-shared").fill("90000");
  await pg.getByRole("button", { name: "Save shared target" }).click();
  await pg.getByText("The project's shared target saved.").waitFor();
  const p = (await api(main, "GET", `/api/v1/projects/${proj.id}`)).json.item;
  eq(p.word_goal, 90000, "the shared goal on the project");
  // Remove my own target: the page falls back to the shared one.
  await pg.getByRole("button", { name: "Remove" }).last().click();
  await pg.getByText("Your target removed.").waitFor();
  ok((await pg.locator("#target").innerText()).includes("set for the whole project"), "did not fall back to the shared target");
});

await kit.test("goals: at 390 px nothing scrolls sideways and More marks this page", async () => {
  const phone = await kit.signedIn({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2 });
  const page = phone.page;
  await page.goto(goalsUrl);
  await page.locator("table.cal").waitFor();
  const m = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: window.innerWidth, cell: document.querySelector("td.day").getBoundingClientRect().width }));
  ok(m.sw <= m.iw, `the page scrolls sideways: ${m.sw} > ${m.iw}`);
  ok(m.cell >= 30, "the calendar cells are too small: " + m.cell);
  // Goals sits under More on a phone since the project pages grew past five tabs: the menu marks it as the current page.
  eq((await page.locator(".menu-item[aria-current=page]").first().innerText()).trim(), "Goals", "the More menu's current item");
  const targets = await page.evaluate(() => [...document.querySelectorAll(".goals .btn, .goals input:not([type=hidden]), .goals .ranges a, .goals summary")].map((e) => e.tagName + "." + e.className + ":" + Math.round(e.getBoundingClientRect().height)));
  const small = targets.filter((t) => +t.split(":").pop() < 40);
  ok(small.length === 0, "a control is under 40 px tall: " + small.join(", "));
  await phone.ctx.close();
});

// U5: the writing day follows the browser's zone for a person who has never chosen one (the first signed-in page sends it), and a zone
// they chose is kept when another browser has a different one.
await kit.test("zone: a person who chose no time zone gets the browser's, and a chosen one is kept", async () => {
  const who = "reader@dev.scriobh"; // a dev identity no other step of this script uses
  const a = await kit.signedIn({ timezoneId: "Pacific/Auckland" }, who);
  const me = async (ctx) => (await api(ctx.ctx, "GET", "/api/v1/me")).json.item.timezone;
  await waitFor(async () => (await me(a)) === "Pacific/Auckland", "the browser's zone to be stored for a person who chose none");
  await a.ctx.close();
  // A choice made on purpose is not written over by a browser in another zone.
  const b = await kit.signedIn({ timezoneId: "Pacific/Auckland" }, who);
  eq((await api(b.ctx, "PATCH", "/api/v1/me", { timezone: "Europe/Paris" })).status, 200, "a zone is chosen");
  const c = await kit.signedIn({ timezoneId: "Asia/Tokyo" }, who);
  // The page sent its zone once the browser recorded the answer (a page with live updates never goes idle, so wait for the record).
  await waitFor(() => c.page.evaluate(() => localStorage.getItem("scriobh:zone-sent")), "the page to send its zone");
  eq(await me(c), "Europe/Paris", "the chosen zone is kept, not the other browser's");
  await c.ctx.close();
  await b.ctx.close();
});

// The zone is sent after the page has loaded, never while it starts: a request in flight while the writing view mounts costs that mount
// about 25 ms at 4x CPU, most of it the request. The phase-2 budget guards the mount; this step checks only when the request starts. An
// init script records the page's readyState at each send, in localStorage so the record survives the redirects of sign-in.
await kit.test("zone: the browser's zone is sent after the page has loaded, not while it starts", async () => {
  // A fresh browser for the same person, signed in by copying the session of the script's own context: a sign-in here would spend the
  // per-address sign-in allowance (/auth/ requests, a burst of 10) that the steps above have already used, and the callback answers 429.
  const ctx = await kit.newContext({ viewport: { width: 1280, height: 800 }, timezoneId: "Europe/Lisbon" });
  await ctx.addCookies(await main.cookies());
  await ctx.addInitScript(() => {
    const send = window.fetch;
    window.fetch = function (u) {
      if (String(u).indexOf("/api/v1/me/zone-default") >= 0) {
        try { const a = JSON.parse(localStorage.getItem("__zt") || "[]"); a.push(document.readyState); localStorage.setItem("__zt", JSON.stringify(a)); } catch (e) { /* the record is a test's aid */ }
      }
      return send.apply(this, arguments);
    };
  });
  const page = await ctx.newPage();
  kit.watch(page, "zone");
  await page.goto(kit.base() + "/", { waitUntil: "load" });
  await page.locator("body.signed-in").waitFor();
  await waitFor(() => page.evaluate(() => localStorage.getItem("scriobh:zone-sent")), "the page to send its zone");
  const states = await page.evaluate(() => JSON.parse(localStorage.getItem("__zt") || "[]"));
  ok(states.length >= 1, "the zone was sent: " + JSON.stringify(states));
  eq(states.every((s) => s === "complete"), true, "every send began after the load event: " + JSON.stringify(states));
  await ctx.close();
});

await kit.finish("e2e-goals");
