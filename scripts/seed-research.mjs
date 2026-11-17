#!/usr/bin/env node
// Seed research for timing and screenshots: folders, then N notes (default 600, about 2 KB of Markdown each)
// spread over the folders, with tags from the eight colours, every status, and a clipping every seventh note.
// Everything goes through the REST API, a few requests at a time.
//
// As a module:  import { seedResearch } from "./seed-research.mjs"; await seedResearch({ api, ctx, owner: "projects", id, n })
// As a script:  node scripts/seed-research.mjs [N]   (starts a server, seeds a project and a Compendium, and prints the
//               research page's server time and DOM weight for each at N notes)
import { pathToFileURL } from "node:url";

const WORDS = "harbour tide ledger rope hemp tar lantern gull keel mast slack ebb fog salt chart anchor quay weir shingle samphire".split(" ");
function paragraph(seed, words) {
  let s = seed, out = [];
  for (let i = 0; i < words; i++) { s = (s * 1103515245 + 12345) & 0x7fffffff; out.push(WORDS[s % WORDS.length]); }
  const t = out.join(" ");
  return t[0].toUpperCase() + t.slice(1) + ".";
}
const COLORS = ["slate", "clay", "ochre", "moss", "teal", "blue", "plum", "rose"];
const TAGS = ["harbour", "weather", "people", "boats", "ledgers", "maps", "songs", "law"];
const STATUS = ["inbox", "reading", "useful", "archived"];

export async function seedResearch({ api, ctx, owner = "projects", id, n = 600, folders = 12, log = () => {} }) {
  const base = `/api/v1/${owner}/${id}`;
  for (let i = 0; i < TAGS.length; i++) await api(ctx, "POST", `${base}/tags`, { name: TAGS[i], color: COLORS[i] });
  const fids = [];
  for (let i = 0; i < folders; i++) {
    const r = await api(ctx, "POST", `${base}/research`, { kind: "folder", title: `Folder ${String(i + 1).padStart(2, "0")}: ${WORDS[i % WORDS.length]}` });
    if (!r.json || !r.json.item) throw new Error(`folder: ${r.status} ${JSON.stringify(r.json)}`);
    fids.push(r.json.item.id);
  }
  const t0 = Date.now();
  const make = async (i) => {
    const clip = i % 7 === 6;
    const body = Array.from({ length: 3 }, (_, k) => paragraph(i * 7 + k, 52)).join("\n\n"); // about 2 KB
    const tags = [TAGS[i % 8], ...(i % 3 === 0 ? [TAGS[(i + 3) % 8]] : [])];
    const r = await api(ctx, "POST", `${base}/research`, {
      kind: clip ? "clipping" : "note", title: `${clip ? "Clipping" : "Note"} ${String(i + 1).padStart(3, "0")} on ${WORDS[i % WORDS.length]}`,
      parent_id: i % 5 === 4 ? undefined : fids[i % folders], body, tags, status: STATUS[i % 4],
      ...(clip ? { source_url: `https://example.org/source/${i}`, retrieved_on: "2026-03-14", quote: paragraph(i, 20) } : {}),
    });
    if (!r.json || !r.json.item) throw new Error(`note ${i}: ${r.status} ${JSON.stringify(r.json)}`);
  };
  for (let i = 0; i < n; i += 8) {
    await Promise.all(Array.from({ length: Math.min(8, n - i) }, (_, k) => make(i + k)));
    if (i % 200 === 192) log(`seeded ${i + 8} of ${n}`);
  }
  log(`seeded ${n} notes in ${folders} folders in ${((Date.now() - t0) / 1000).toFixed(1)} s`);
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const { createKit } = await import("./e2e-kit.mjs");
  const n = Number(process.argv[2]) || 600;
  const kit = await createKit("seed-research");
  const { ctx, page } = await kit.signedIn();
  const p = (await kit.api(ctx, "POST", "/api/v1/projects", { title: "Research at scale", kind: "novel" })).json.item;
  const c = (await kit.api(ctx, "POST", "/api/v1/compendiums", { name: "Research at scale bible" })).json.item;
  for (const [owner, o, path] of [["projects", p, `/p/${p.slug}/research`], ["compendiums", c, `/c/${c.slug}/research`]]) {
    await seedResearch({ api: kit.api, ctx, owner, id: o.id, n, log: (m) => console.log(`${owner}: ${m}`) });
    for (const [label, url] of [["list", path], ["board", path + "/board"], ["tags", path + "/tags"]]) {
      const times = [];
      let bytes = 0;
      for (let i = 0; i < 7; i++) {
        const t0 = Date.now();
        const r = await ctx.request.get(kit.base() + url);
        const b = await r.body();
        times.push(Date.now() - t0);
        bytes = b.length;
      }
      times.sort((a, b) => a - b);
      await page.goto(kit.base() + url, { waitUntil: "load" });
      const dom = await page.evaluate(() => ({ nodes: document.getElementsByTagName("*").length, rows: document.querySelectorAll(".r-row, .r-card").length }));
      console.log(`${owner} ${label}: ${n} notes, server render median ${times[3]} ms, max ${times[6]} ms, ${(bytes / 1024).toFixed(1)} KB HTML, ${dom.nodes} DOM nodes, ${dom.rows} rows/cards`);
    }
  }
  await kit.finish("seed-research");
}
