#!/usr/bin/env node
// Phase 3 acceptance walk: a Compendium of 5,000 entities, 600 research notes in a project and 200 in its Compendium,
// and a 60-scene book whose scenes name a few dozen of those entities, driven on a 390x844 touch phone with the CPU
// slowed 4x. It measures the screens the phase added:
//
//   node scripts/e2e-phase3.mjs [--cpu 4] [--entities 5000] [--notes 600]
//
// Budgets that fail this script (server times are medians of 7 on the API's own render, phone times are at the
// given throttle):
//   entity list, first page, server < 150 ms; alias search < 150 ms; entity sheet < 150 ms
//   entity list on screen within 2.5 s on the phone; "More" adds a page within 1.5 s
//   research list and board (600 notes), server < 150 ms; on screen within 2.5 s on the phone
//   mention.list with spans, one scene, < 50 ms; scene.entities < 50 ms
//   the names are underlined within 3 s of the editor on the phone, and typing stays under 16 ms a key (median)
//   the outline in the point-of-view order, server < 150 ms
//   "Where this appears" for an entity named in 60 scenes: sheet server < 150 ms
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { seedResearch } from "./seed-research.mjs";

const arg = (name, d) => { const i = process.argv.indexOf(name); return i > 0 ? Number(process.argv[i + 1]) : d; };
const cpu = arg("--cpu", 4), N = arg("--entities", 5000), NOTES = arg("--notes", 600);
const kit = await createKit("e2e-phase3", { env: { SCRIOBH_SESSION_HOURS: "400", SCRIOBH_SESSION_IDLE_MINUTES: "100000" } });
const { api, waitFor, sleep, MOD } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
const ms = (n) => `${n.toFixed(0)} ms`;
const median = (a) => [...a].sort((x, y) => x - y)[Math.floor(a.length / 2)];

async function serverTime(path, runs = 7) {
  const t = [];
  for (let i = 0; i < runs; i++) {
    const t0 = Date.now();
    const r = await main.request.get(base() + path);
    await r.body();
    if (r.status() !== 200) throw new Error(`${path}: ${r.status()}`);
    t.push(Date.now() - t0);
  }
  return median(t);
}

// ---- the data -------------------------------------------------------------------------------------------
const firsts = ["Máire", "Tadhg", "Ciara", "Oisín", "Niamh", "Fionn", "Aoife", "Cathal", "Sorcha", "Donal", "Eithne", "Ruairí", "Siobhán", "Colm", "Brigid", "Lorcan", "Orla", "Declan", "Gráinne", "Pádraig"];
const lasts = ["Nolan", "Ó Néill", "Bhriain", "Walsh", "Keane", "Doyle", "Byrne", "Quinn", "Lynch", "Burke", "Hayes", "Moran", "Fox", "Tobin", "Kavanagh", "Rafferty", "Sheehan", "Brennan", "Gallagher", "Farrell"];
let book, comp, ids = [], named = [];
await kit.test(`setup: ${N} entities, ${NOTES} research notes, 60 scenes naming 40 of the entities`, async () => {
  const t0 = Date.now();
  book = (await api(main, "POST", "/api/v1/projects", { title: "Scale Book", kind: "novel" })).json.item;
  comp = (await api(main, "POST", "/api/v1/compendiums", { name: "Scale Bible", project_id: book.id })).json.item;
  ids = new Array(N);
  for (let s = 0; s < N; s += 25) {
    await Promise.all(Array.from({ length: Math.min(25, N - s) }, (_, k) => {
      const i = s + k, kind = i % 20 < 12 ? "character" : i % 20 < 16 ? "place" : i % 20 < 18 ? "item" : i % 20 < 19 ? "org" : "concept";
      const name = kind === "character" ? `${firsts[i % 20]} ${lasts[Math.floor(i / 20) % 20]} ${Math.floor(i / 400) + 1}` : `${lasts[i % 20]} ${kind} ${i}`;
      return api(main, "POST", `/api/v1/compendiums/${comp.id}/entities`, { kind, name, summary: `Entry ${i}.`, ...(i % 7 === 0 ? { aliases: [`Known as ${i}`] } : {}) }).then((r) => {
        if (r.status !== 201) throw new Error(`entity ${i}: ${r.status}`);
        ids[i] = r.json.item;
      });
    }));
  }
  console.log(`      ${N} entities in ${((Date.now() - t0) / 1000).toFixed(1)} s`);
  await seedResearch({ api, ctx: main, owner: "projects", id: book.id, n: NOTES });
  await seedResearch({ api, ctx: main, owner: "compendiums", id: comp.id, n: 200 });
  // 40 characters and places (the first 40 of kind character, whose names carry no suffix clash) named in 60 scenes.
  named = ids.filter((e) => e.kind === "character").slice(0, 40);
  const part = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Part", kind: "part" })).json.item.id;
  const chap = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: "Long chapter", kind: "chapter", parent_id: part })).json.item.id;
  for (let i = 0; i < 60; i++) {
    const text = Array.from({ length: 12 }, (_, k) => `${named[(i + k) % 40].name} looked at ${named[(i * 3 + k) % 40].name} across the water and said nothing, as the tide went out for the ${k + 1}th time that day.`).join(" ") + "\n";
    const d = (await api(main, "POST", `/api/v1/projects/${book.id}/docs`, { title: `Scene ${i + 1}`, kind: "scene", parent_id: chap, content: text })).json.item;
    if (i === 0) kit.scene0 = d.id;
    const povRes = await api(main, "PUT", `/api/v1/docs/${d.id}/pov`, { entity: named[i % 40].id, base_revision: d.revision });
    if (povRes.status !== 200) throw new Error("pov " + povRes.status);
  }
  await waitFor(async () => !(await api(main, "GET", `/api/v1/docs/${kit.scene0}/mentions`)).json.indexing, "the mention index to settle", 60000);
});

await kit.test("server: the entity list, a search by alias, a sheet, the research list and board, the sorted outline", async () => {
  const c = `/c/${comp.slug}`;
  const rows = {
    "entity list, first page": [`${c}/entities`, 150],
    "alias search": [`${c}/entities?q=${encodeURIComponent("Known as 700")}`, 150],
    "entity sheet": [`${c}/e/${ids[Math.floor(N / 2)].id}`, 150],
    "research list": [`/p/${book.slug}/research`, 150],
    "research board": [`/p/${book.slug}/research/board`, 150],
    "outline by point of view": [`/p/${book.slug}/outline?sort=pov`, 150],
    "outline by timeline": [`/p/${book.slug}/outline?sort=timeline`, 150],
    "book sheet with Where this appears": [`/p/${book.slug}/e/${named[0].id}`, 150],
    "Compendium sheet with Where this appears": [`${c}/e/${named[0].id}`, 150],
  };
  for (const [name, [path, budget]] of Object.entries(rows)) {
    const t = await serverTime(path);
    console.log(`      ${name}: median ${ms(t)} (budget ${budget})`);
    ok(t < budget, `${name} took ${ms(t)}, budget ${budget} ms`);
  }
  const timed = async (path) => { const t = []; for (let i = 0; i < 7; i++) { const t0 = Date.now(); await api(main, "GET", path); t.push(Date.now() - t0); } return median(t); };
  const ml = await timed(`/api/v1/docs/${kit.scene0}/mentions?spans=true`), se = await timed(`/api/v1/docs/${kit.scene0}/scene-entities`);
  console.log(`      mention.list with spans: median ${ms(ml)}; scene.entities: median ${ms(se)} (budget 50)`);
  ok(ml < 50 && se < 50, `mention.list ${ms(ml)}, scene.entities ${ms(se)}`);
});

// ---- the phone ---------------------------------------------------------------------------------------
const phone = await kit.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true, isMobile: true,
  userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1" });
await phone.addCookies(await main.cookies());
const page = await phone.newPage();
kit.watch(page, "phone");
kit.main = page;
const cdp = await phone.newCDPSession(page);
await cdp.send("Emulation.setCPUThrottlingRate", { rate: cpu });

await kit.test(`phone: the entity list of ${N} shows its first page in 2.5 s, and More adds another within 1.5 s`, async () => {
  const t0 = Date.now();
  await page.goto(`${base()}/c/${comp.slug}/entities`, { waitUntil: "load" });
  await page.waitForSelector(".ent-row");
  const t = Date.now() - t0;
  const nodes = await page.evaluate(() => document.getElementsByTagName("*").length);
  console.log(`      first page on screen in ${ms(t)}, ${nodes} DOM nodes`);
  ok(t < 2500, `entity list took ${ms(t)}`);
  const t1 = Date.now();
  await page.getByRole("link", { name: "More", exact: true }).tap();
  await page.waitForLoadState("load");
  await page.waitForSelector(".ent-row");
  console.log(`      More: ${ms(Date.now() - t1)}`);
  ok(Date.now() - t1 < 1500, "More took too long");
  const t2 = Date.now();
  await page.locator("#ent-q").fill("Known as 1400");
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => /Known as 1400|1401/.test(document.body.innerText));
  console.log(`      alias search typed to results: ${ms(Date.now() - t2)}`);
});

await kit.test(`phone: the research list and the board of ${NOTES} notes`, async () => {
  for (const [name, path, sel] of [["list", `/p/${book.slug}/research`, ".r-row"], ["board", `/p/${book.slug}/research/board`, ".r-card"]]) {
    const t0 = Date.now();
    await page.goto(base() + path, { waitUntil: "load" });
    await page.waitForSelector(sel);
    const t = Date.now() - t0;
    console.log(`      research ${name}: ${ms(t)}`);
    ok(t < 2500, `research ${name} took ${ms(t)}`);
    ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `research ${name} scrolls sideways`);
  }
});

await kit.test("phone: a scene naming many entities is underlined within 3 s of the editor, and typing stays under 16 ms", async () => {
  const t0 = Date.now();
  await page.goto(`${base()}/p/${book.slug}/d/${kit.scene0}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose");
  const mount = Date.now() - t0;
  await page.waitForSelector(".scriobh-prose .mention", { timeout: 15000 });
  const t = Date.now() - t0;
  const n = await page.locator(".scriobh-prose .mention").count();
  console.log(`      editor ${ms(mount)}, ${n} names underlined at ${ms(t)}`);
  ok(t - mount < 3000, `the names came ${ms(t - mount)} after the editor`);
  ok(n >= 20, `${n} names underlined`);
  // Typing: the time from key to the next frame, with all those marks in the document.
  await page.evaluate(() => { const v = window.scriobh.write.editor.view; v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.atEnd(v.state.doc))); v.focus(); });
  const lat = [];
  for (let i = 0; i < 30; i++) {
    const t1 = await page.evaluate(() => { window.__t = performance.now(); return 0; });
    await page.keyboard.type("x");
    lat.push(await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r(performance.now() - window.__t)))));
    void t1;
  }
  const m = median(lat);
  console.log(`      key to next frame: median ${ms(m)} (throttled ${cpu}x; budget 16 ms)`);
  ok(m < 16, `typing took ${ms(m)} a key, the published budget is 16 ms`);
});

await kit.test("phone: the In this scene tab and the point-of-view outline", async () => {
  await page.evaluate(() => window.scriobh.inspector.open("scene", false));
  const t0 = Date.now();
  await page.locator("#ipanel-scene .sc-name").first().waitFor();
  console.log(`      scene tab drawn in ${ms(Date.now() - t0)}`);
  const t1 = Date.now();
  await page.goto(`${base()}/p/${book.slug}/outline?sort=pov`, { waitUntil: "load" });
  await page.waitForSelector(".o-groups");
  console.log(`      outline by point of view, 60 scenes in 40 groups: ${ms(Date.now() - t1)}`);
  ok(Date.now() - t1 < 2500, "the sorted outline took too long");
  eq(await page.locator(".o-grp").count(), 41, "one group per point of view, and the scenes with none");
});

await kit.finish("e2e-phase3");
