#!/usr/bin/env node
// Seeds a Compendium with a lot of entities through the REST API, then measures the entity list and the search:
// server time and DOM weight. Deterministic: the same N gives the same names, aliases, fields and relationships.
//
//   node scripts/seed-entities.mjs [--n 5000] [--keep] [--batch 25]
//
// It builds the binary, starts a server on an ephemeral loopback port with the rate limit raised, signs in through
// the stub issuer, and creates a Compendium named "Seeded Bible" with N entities (60% characters, 20% places, 10%
// items, 5% organisations, 5% concepts). About one in seven has aliases, one in five characters has fields, and one
// in eleven has a relationship to the one before it. With --keep the server stays up and the script prints how to
// reach it, so a browser walk can follow; stop it with Ctrl-C.
import { createKit } from "./e2e-kit.mjs";

const arg = (name, dflt) => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : dflt; };
const N = Math.max(1, Number(arg("--n", "5000")));
const BATCH = Math.max(1, Number(arg("--batch", "25")));
const keep = process.argv.includes("--keep");

const firsts = ["Máire", "Tadhg", "Ciara", "Oisín", "Niamh", "Fionn", "Aoife", "Cathal", "Sorcha", "Donal", "Eithne", "Ruairí", "Siobhán", "Colm", "Brigid", "Lorcan", "Orla", "Declan", "Gráinne", "Pádraig"];
const lasts = ["Nolan", "Ó Néill", "Bhriain", "Walsh", "Keane", "Doyle", "Byrne", "Quinn", "Lynch", "Burke", "Hayes", "Moran", "Fox", "Tobin", "Kavanagh", "Rafferty", "Sheehan", "Brennan", "Gallagher", "Farrell"];
const places = ["Quay", "Harbour", "Ford", "Marsh", "Hollow", "Cross", "Strand", "Ridge", "Lough", "Glen"];
const things = ["lantern", "ledger", "compass", "satchel", "flask", "key", "bell", "knife", "ring", "map"];
const orgs = ["Guild", "Order", "Company", "League", "Watch"];
const ideas = ["the tide", "the debt", "the oath", "the crossing", "the winter"];
const epithets = ["Ferrywoman", "Lamplighter", "Red", "Younger", "Quiet", "Wanderer", "Bell-ringer"];

function plan(i) {
  const r = i % 20;
  const kind = r < 12 ? "character" : r < 16 ? "place" : r < 18 ? "item" : r < 19 ? "org" : "concept";
  const a = firsts[i % firsts.length], b = lasts[Math.floor(i / firsts.length) % lasts.length], n = Math.floor(i / (firsts.length * lasts.length)) + 1;
  const tag = n > 1 ? ` ${n}` : "";
  let name;
  if (kind === "character") name = `${a} ${b}${tag}`;
  else if (kind === "place") name = `${b} ${places[i % places.length]}${tag} ${a.slice(0, 3)}`;
  else if (kind === "item") name = `${a}'s ${things[i % things.length]}${tag} ${b.slice(0, 3)}`;
  else if (kind === "org") name = `${b} ${orgs[i % orgs.length]}${tag} of ${a}`;
  else name = `${ideas[i % ideas.length]} of ${a} ${b}${tag}`;
  const body = { kind, name: `${name} #${i + 1}`, summary: `Entry ${i + 1}: ${kind} in the seeded bible.` };
  if (i % 7 === 0) body.aliases = [`${epithets[i % epithets.length]} ${i + 1}`, `Alias ${i + 1}`].slice(0, 1 + (i % 2));
  if (kind === "character" && i % 5 === 0) body.fields = { age: String(18 + (i % 60)), role: epithets[i % epithets.length] };
  return body;
}

const kit = await createKit("seed-entities");
const { api } = kit;
const { ctx, page } = await kit.signedIn();
kit.main = page;
const comp = (await api(ctx, "POST", "/api/v1/compendiums", { name: "Seeded Bible" })).json.item;
const t0 = Date.now();
const ids = new Array(N);
for (let start = 0; start < N; start += BATCH) {
  const batch = [];
  for (let i = start; i < Math.min(N, start + BATCH); i++) {
    batch.push(api(ctx, "POST", `/api/v1/compendiums/${comp.id}/entities`, plan(i)).then((r) => {
      if (r.status !== 201) throw new Error(`entity ${i}: ${r.status} ${JSON.stringify(r.json)}`);
      ids[i] = r.json.item.id;
    }));
  }
  await Promise.all(batch);
  if ((start / BATCH) % 20 === 0) process.stdout.write(`  ${Math.min(N, start + BATCH)} / ${N}\r`);
}
let rel = 0;
for (let start = 11; start < N; start += 11 * BATCH) {
  const batch = [];
  for (let i = start; i < Math.min(N, start + 11 * BATCH); i += 11) {
    batch.push(api(ctx, "POST", `/api/v1/entities/${ids[i]}/relations`, { to_entity_id: ids[i - 1], kind: "ally" }).then((r) => { if (r.status === 201) rel++; }));
  }
  await Promise.all(batch);
}
console.log(`seeded ${N} entities and ${rel} relationships in ${((Date.now() - t0) / 1000).toFixed(1)}s`);

// Measure: server time for the list (first page, a deep page, a search) and what the browser has to hold.
const time = async (path, runs = 7) => {
  const ms = [];
  let size = 0;
  for (let i = 0; i < runs; i++) {
    const t = Date.now();
    const r = await ctx.request.get(kit.base() + path);
    const body = await r.body();
    ms.push(Date.now() - t);
    size = body.length;
    if (r.status() !== 200) throw new Error(`${path}: ${r.status()}`);
  }
  ms.sort((a, b) => a - b);
  return { median: ms[Math.floor(ms.length / 2)], max: ms[ms.length - 1], bytes: size };
};
const cu = (p) => `/c/${comp.slug}${p}`;
const first = await time(cu("/entities"));
const frag = await time(cu("/entities?frag=1"));
const kindPage = await time(cu("/entities?kind=place"));
const search = await time(cu("/entities?q=ferrywoman"));
const miss = await time(cu("/entities?q=zzzqqq"));
const sheet = await time(cu("/e/" + ids[N >> 1]));
await page.goto(kit.base() + cu("/entities"), { waitUntil: "load" });
const dom = await page.evaluate(() => ({ nodes: document.querySelectorAll("*").length, rows: document.querySelectorAll(".ent-row").length, html: document.documentElement.outerHTML.length }));
// Walk to the end by following "More".
let pages = 1;
while (await page.getByRole("link", { name: "More", exact: true }).count()) {
  await page.getByRole("link", { name: "More", exact: true }).click();
  await page.waitForLoadState("load");
  pages++;
  if (pages > Math.ceil(N / 50) + 2) throw new Error("paging does not end");
}
const last = await page.evaluate(() => document.querySelectorAll(".ent-row").length);
console.log(JSON.stringify({ n: N, list_first_page: first, list_fragment: frag, list_kind_filter: kindPage, search_alias: search, search_none: miss, sheet, dom_first_page: dom, pages, rows_on_last_page: last }, null, 2));

if (keep) {
  console.log(`\nSERVER ${kit.base()}  (data ${kit.dataDir})\nCOMPENDIUM /c/${comp.slug}/entities\nSigned in only inside this script's browser; sign in at ${kit.base()}/auth/login with the stub issuer. Ctrl-C to stop.`);
  await new Promise(() => {});
}
await kit.finish("seed-entities");
