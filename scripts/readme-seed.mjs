// The demo library for the README media (scripts/readme-media.mjs): a small, believable novel with its outline, its
// Compendium, a comment, alternative wordings and a writing history, and four other projects so the library looks lived in.
// Everything here is invented: the people, the book, the place. Nothing is read from a real account or a real manuscript.
// It writes only through the REST ops of a throwaway development server. Build-time only; nothing here ships.
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { repo } from "./harness.mjs";

export const AUTHOR = "Ione Calderwick";
export const TITLE = "The Long Slack";

const p = (...s) => s.join("\n\n") + "\n";

const CH1 = p(
  "The gauge at Skerrow Quay was a post of black oak with brass numbers screwed to it, and Wenna Harrow read it every morning before she read anything else. Six feet two, falling. The water had the flat, tired look it wore in January, as if the whole estuary had been up all night and had nothing left to say about it.",
  "She wrote the number in the book with the stub of pencil she kept on a string. Her father had kept the book for thirty-one years, and for the nine years since he died she had kept it in his hand, the sevens crossed, the nines closed like small fists. Nobody asked her to. The harbour board had a machine now that sent the heights to a screen in an office forty miles away. But the machine could not tell you which way the wind was leaning on the water, and the book could.",
  "That morning there was a boat at the foot of the steps. It was a small clinker dinghy, painted a green that had once been a better green, tied off with a bowline that she knew. She stood with the pencil in her hand and the tide going out beneath her, and did not go down to look at it. There are things you can afford to know slowly.",
  "Behind her the kettle in the gatehouse began to sing. She let it.",
);

const CH2 = p(
  "He came up the quay road at noon with a canvas bag over one shoulder and the walk of someone who had been told the ground would be level and had not believed it. Eleven years had made him thinner and, somehow, taller. He stopped at the gate and looked at the gatehouse, at the washing line, at the gauge, and finally at her, in that order, as if he were checking a list.",
  "“You still cross your sevens,” he said.",
  "“Dad did,” said Wenna. “Somebody had to.”",
  "They did not embrace. They stood on either side of the gate while the gulls went over, complaining, and the tide made its small noises against the stones. Then Isak lifted the latch himself, which was a thing a guest would not have done and a stranger would not have known how to do, and came in.",
  "She made tea. He drank it standing, with his coat on, looking out through the window over the sink as though the water might do something. It did not. It lay there being mud, and then being less mud, and a heron walked the edge of it with the patience of a clerk.",
  "“I came by the road,” he said at last, “because I wanted to see how long it was.”",
  "“Eleven years,” said Wenna, “and a bit.”",
);

const CH3 = p(
  "The tables for the year came in a grey pamphlet from the harbour board, and her father had never trusted them. He trusted the book. When the two disagreed he wrote the difference in the margin, in pencil, and left it for the next person to puzzle over, who was always, in the end, Wenna.",
  "That night, with Isak asleep in the room that still had his school atlas on the shelf, she took down the oldest volume and turned to the March of the year he left. The tables promised low water at a quarter past three. The book said twenty to, and beside it, in her father’s careful hand: *Ran early. Ask Isak.*",
  "She read it a second time and then closed the book very gently, as though it were something that might wake.",
  "Thirty-five minutes. A tide does not run thirty-five minutes early. Water is the most obedient thing there is; it has been told what to do by the moon for four billion years and has never once been late. But the boat had gone out that night, and the water had been somewhere it should not have been, and her father had known, and had written it down, and had asked no one but a brother who was not there to be asked.",
);

const CH4 = p(
  "The causeway to Gannet Holm is open for two hours either side of low water, and not a minute longer, and the people of Skerrow tell this to visitors the way other towns tell them about the weather. On the second morning Isak asked to walk it.",
  "They went out at ten, in boots, with the wet sand ribbed beneath them and the whole sky laid open overhead. He walked a little ahead of her, not from impatience but from some old habit of going first into places that might not hold. Halfway across he stopped and pointed, and she saw what he had been looking for: the post, black and leaning, where the lantern had hung.",
  "“I wasn’t running away,” he said. “I was going to fetch him back.”",
  "She waited. Behind them the first thin line of the flood was coming up the channel, quiet as a thought.",
);

// [title, kind, parent key, synopsis, status, target words, content]
const BOOK = [
  ["Dedication", "front_matter", null, "For the keepers of small records.", "done", 0, "For the keepers of small records.\n"],
  ["Part One: Slack Water", "part", null, "Wenna keeps her father’s tide book; a green boat appears; the brother she has not seen in eleven years comes home.", "draft", 2400, ""],
  ["The Gauge", "chapter", "Part One: Slack Water", "Wenna reads the gauge at dawn and finds a dinghy at the steps with a knot she knows.", "revise", 800, CH1],
  ["Isak", "chapter", "Part One: Slack Water", "Her brother walks up the quay road after eleven years and neither of them says what they mean.", "draft", 800, CH2],
  ["What the Tables Say", "chapter", "Part One: Slack Water", "The book and the tables disagree on the night he left. Her father’s note: ask Isak.", "draft", 800, CH3],
  ["Part Two: Ebb", "part", null, "The causeway, the lantern post, and what was fetched and what was not.", "outline", 7000, ""],
  ["The Causeway", "chapter", "Part Two: Ebb", "Two hours either side of low water: Isak walks to the post where the lantern hung.", "draft", 1200, CH4],
  ["The Lamplighters", "chapter", "Part Two: Ebb", "Hollis, the harbour clerk, produces a ledger nobody was meant to find.", "outline", 2500, ""],
  ["Spring Tide", "chapter", "Part Two: Ebb", "The highest water in nineteen years, and everyone who came to watch it.", "idea", 3000, ""],
  ["Acknowledgements", "back_matter", null, "", "idea", 0, "To the people who answered a stranger’s questions about tides.\n"],
];

const OTHERS = [
  {
    title: "Notes from the Tidal Flats", kind: "essay", author: AUTHOR, goal: 6000, color: "#3B5B7A",
    text: p("Every estuary keeps two calendars. One is the moon’s, which is printed in tables and sold in harbour shops. The other is kept by the birds, who have never read the tables and are never wrong.", "I began to keep the birds’ calendar the winter I came to Skerrow, writing down in a school exercise book which species arrived on which tide, and what they did when they got there. The curlews came first and stayed latest. The redshanks came as a committee."),
  },
  {
    title: "Hedgerow", kind: "short", author: AUTHOR, goal: 4000, color: "#4B6B3A",
    text: p("Nobody in the village could say who had planted the hedge, only that it had always been there, and that it had always been somebody else’s job to cut it.", "In June it filled with wrens. In August it filled with blackberries and the children of people who should have known better."),
  },
  {
    title: "The Winter Ledger", kind: "novella", author: AUTHOR, goal: 30000, color: "#6B3E4F",
    text: p("The first entry in the ledger is dated the fourth of November and reads, in full: *Snow on the hill. Stove lit. No visitors.*", "The second entry is longer."),
  },
  {
    title: "Field Notes", kind: "collection", author: "", goal: 0, color: "#8A6420",
    text: p("A page for things seen from a train: a heron on a flooded field, a washing line with nine blue shirts, a man waving at nothing."),
  },
];

export async function seedDemo(kit, ctx, who) {
  const { api, dataDir } = kit;
  const ok = (r, what) => { if (r.status >= 300) throw new Error(`${what}: ${r.status} ${JSON.stringify(r.json)}`); return r.json; };
  const A = async (m, path, data) => ok(await api(ctx, m, path, data), `${m} ${path}`);
  const me = (await A("GET", "/api/v1/me")).item;

  // The other projects.
  const made = {};
  for (const o of OTHERS) {
    const q = (await A("POST", "/api/v1/projects", { title: o.title, kind: o.kind, word_goal: o.goal, cover_color: o.color, author_name: o.author })).item;
    const d = (await A("GET", `/api/v1/projects/${q.id}/docs`)).order[0];
    const cur = (await A("GET", `/api/v1/docs/${d}`)).item;
    await A("PATCH", `/api/v1/docs/${d}`, { base_revision: cur.revision, content: o.text });
    made[o.title] = q;
  }
  // The library: four other projects first, then the novel, so the novel is the one last written in.
  const book = (await A("POST", "/api/v1/projects", { title: TITLE, kind: "novel", word_goal: 10000, cover_color: "#2F5D50", author_name: AUTHOR, subtitle: "A novel" })).item;
  const seeded = (await A("GET", `/api/v1/projects/${book.id}/docs`)).order[0];
  const placeholder = (await A("POST", `/api/v1/projects/${book.id}/docs`, { title: "placeholder", kind: "note" })).item.id;
  await A("POST", `/api/v1/docs/${seeded}/trash`);
  const ids = {};
  for (const [title, kind, parent, synopsis, status, target, content] of BOOK) {
    const d = (await A("POST", `/api/v1/projects/${book.id}/docs`, { title, kind, parent_id: parent ? ids[parent] : undefined, content: content || undefined, synopsis_short: synopsis || undefined })).item;
    ids[title] = d.id;
    const cur = (await A("GET", `/api/v1/docs/${d.id}`)).item;
    await A("PATCH", `/api/v1/docs/${d.id}`, { base_revision: cur.revision, status, ...(target ? { target_words: target } : {}) });
  }
  await A("POST", `/api/v1/docs/${placeholder}/trash`);

  // Alternatives in the first chapter. The text always holds the chosen wording; the others sit beside it.
  const gauge = ids["The Gauge"];
  let rev = (await A("GET", `/api/v1/docs/${gauge}`)).item.revision;
  const alt = async (body) => { const r = await A("POST", `/api/v1/docs/${gauge}/alternatives`, { base_revision: rev, ...body }); rev = r.revision; return r; };
  await alt({ scope: "word", quote: "tired", before: "flat, ", after: " look", variants: ["spent", "worn"] });
  await alt({ scope: "word", quote: "leaning", before: "wind was ", after: " on the water", variants: ["pressing", "resting"] });
  await alt({ scope: "sentence", quote: "There are things you can afford to know slowly.", variants: ["Some things you can afford to know slowly.", "A person can afford to know certain things slowly."] });

  // A comment, anchored in the second chapter.
  await A("POST", `/api/v1/projects/${book.id}/comments`, {
    document_id: ids["Isak"], quote: "at the gauge, and finally at her", kind: "comment",
    body: "He checks the gauge before he looks at her. Keep that, and let her notice it on the page.",
  });

  // The Compendium: the bible for the book.
  const bible = (await A("POST", "/api/v1/compendiums", { name: "The Long Slack: Bible", kind: "fiction", description: "People, places and things in the Skerrow books." })).item;
  const mk = async (kind, name, extra = {}) => (await A("POST", `/api/v1/compendiums/${bible.id}/entities`, { kind, name, ...extra })).item;
  const wenna = await mk("character", "Wenna Harrow", {
    sort_name: "Harrow, Wenna", summary: "Keeper of the tide book at Skerrow Quay. Precise, wry, slow to forgive.", color: "#2F5D50", aliases: ["The tide-keeper"],
    body: "She has kept her father's book for nine years, in his hand.\n\n## What she knows\n\n- Every tide of the estuary, by the colour of the water.\n- The night her brother left, and what the book says about it.\n\n> Water is the most obedient thing there is.\n",
  });
  const isak = await mk("character", "Isak Harrow", { sort_name: "Harrow, Isak", summary: "Her younger brother. Went out in a green dinghy eleven years ago and did not write.", color: "#7A4B2A", aliases: ["Isy"] });
  const quay = await mk("place", "Skerrow Quay", { summary: "A stone quay, a gatehouse and a gauge at the head of the estuary.", color: "#3B5B7A", body: "The gauge is black oak with brass numbers. The steps below it dry out at half ebb." });
  await mk("place", "Gannet Holm", { summary: "A sandbank island reached by a causeway, open two hours either side of low water." });
  await mk("item", "The tide book", { summary: "Thirty-one volumes of readings in two hands, with margin notes in pencil." });
  await mk("character", "Hollis Teague", { summary: "Harbour clerk, keeper of the other ledger." });
  await mk("org", "The Estuary Board", { summary: "Sends the heights to a screen forty miles away." });
  await A("POST", `/api/v1/entities/${wenna.id}/relations`, { to_entity_id: isak.id, kind: "family", symmetric: true });
  await A("POST", `/api/v1/entities/${wenna.id}/relations`, { to_entity_id: quay.id, kind: "custom", label: "keeps the gauge at", inverse_label: "gauge kept by" });
  await A("PUT", `/api/v1/projects/${book.id}/compendium`, { compendium_id: bible.id });
  await A("PUT", `/api/v1/projects/${book.id}/goals`, { kind: "daily", target_words: 300 });

  await A("POST", `/api/v1/projects/${book.id}/pin`, { pinned: true });
  await A("POST", `/api/v1/projects/${made["Hedgerow"].id}/pin`, { pinned: true });
  await A("POST", `/api/v1/projects/${book.id}/open`, {});

  // A writing history, back-dated by the Go helper the studio scripts use (no op writes into the past): about four months,
  // most days written, some quiet, ending on a run of goal days that today can continue.
  seedHistory(dataDir, me.id, book.id);
  return { book, ids, bible, wenna, others: made, me, rev: () => rev };
}

const dayAgo = (n) => new Date(Date.now() - n * 864e5).toISOString().slice(0, 10);
export function seedHistory(dataDir, userId, projectId) {
  const rows = [];
  let x = 4242;
  const rnd = () => (x = (x * 1103515245 + 12345) % 2147483648) / 2147483648;
  for (let ago = 1; ago <= 130; ago++) {
    const recent = ago <= 6; // a run of goal days up to yesterday
    if (!recent && (rnd() < 0.27 || (ago > 52 && ago < 60))) continue;
    const words = recent ? 340 + Math.floor(rnd() * 420) : 80 + Math.floor(rnd() * rnd() * 1300);
    rows.push({ day: dayAgo(ago), words, net: words - Math.floor(rnd() * 60), minutes: Math.max(8, Math.round(words / 22)),
      hour: [6, 7, 7, 8, 12, 20, 21, 21][Math.floor(rnd() * 8)], target: 300, hit: words >= 300 });
  }
  const dir = mkdtempSync(join(tmpdir(), "scriobh-readme-"));
  const file = join(dir, "rows.json");
  writeFileSync(file, JSON.stringify({ user: userId, project: projectId, rows }));
  try {
    execFileSync("go", ["test", "./internal/server", "-run", "^TestSeedStudioHistory$", "-count=1"], {
      cwd: repo, stdio: ["ignore", "ignore", "inherit"], env: { ...process.env, GOMAXPROCS: "2", SCRIOBH_SEED_DIR: dataDir, SCRIOBH_SEED_ROWS: file },
    });
  } finally { rmSync(dir, { recursive: true, force: true }); }
}
