#!/usr/bin/env node
// Search by meaning in Chromium (F-170, F-205), against an embedding endpoint this script starts on loopback (it hashes
// words into vectors: it proves the screens and what is sent, and says nothing of a real model's results). The owner
// previews before anything is sent, switches the index on, and searches by meaning; a hit opens the chapter at its
// passage; a reader sees scenes like this one and nothing else of the feature; pausing stops the questions; a private
// note never reaches the endpoint; an endpoint that is down is said calmly; 390 px, keyboard and mirrored.
//
//   node scripts/e2e-semantic.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { createServer } from "node:http";
import { createHash } from "node:crypto";
import { mkdirSync } from "node:fs";

// ---- the stand-in embedding endpoint (Ollama's /api/embed shape)
const seen = [];
let down = false;
const vec = (text) => {
  const v = new Array(32).fill(0);
  for (const w of text.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean)) v[createHash("md5").update(w).digest().readUInt32BE(0) % 32] += 1;
  const n = Math.sqrt(v.reduce((a, b) => a + b * b, 0)) || 1;
  return v.map((x) => x / n || 0).map((x, i) => (n === 1 && !x && i === 0 ? 1 : x));
};
const embed = createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    if (down) { res.statusCode = 500; res.end("down"); return; }
    let body = {};
    try { body = JSON.parse(Buffer.concat(chunks).toString("utf8")); } catch { /* not JSON */ }
    if (req.url !== "/api/embed" || !Array.isArray(body.input)) { res.statusCode = 404; res.end(); return; }
    seen.push(...body.input);
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ embeddings: body.input.map(vec) }));
  });
});
await new Promise((r) => embed.listen(0, "127.0.0.1", r));
const embedBase = `http://127.0.0.1:${embed.address().port}`;

const kit = await createKit("e2e-semantic", { env: { SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000" } });
kit.deadline(6 * 60 * 1000);
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api, waitFor } = kit;
const base = kit.base();
const owner = await kit.signedIn({}, "writer@dev.scriobh");
const admin = await kit.signedIn({}, "admin@dev.scriobh");
const reader = await kit.signedIn({}, "reader@dev.scriobh");
const mk = async (ctx, path, data) => { const r = await api(ctx, "POST", path, data); ok(r.status < 300, `${path}: ${r.status} ${JSON.stringify(r.json)}`); return r.json.item || r.json; };
const prov = await mk(admin.ctx, "/api/v1/ai/providers", { name: "Local embeddings", kind: "ollama", scope: "instance", base_url: embedBase, local: true, default_model: "embed-a" });
const book = await mk(owner.ctx, "/api/v1/projects", { title: "The Salt Road", kind: "novel" });
await mk(owner.ctx, `/api/v1/projects/${book.id}/members`, { email: "reader@dev.scriobh", role: "reader" });
await api(owner.ctx, "PUT", `/api/v1/projects/${book.id}/ai/policy`, { mode: "ask" });
const docs = {};
for (const [title, body] of [
  ["The Forge", "The smith lifted the hammer and the anvil rang. Sparks fell on the iron while the forge roared.\n\n%%PRIVATE-CANARY-ONE keep this at home%%\n"],
  ["The Mill", "Grain poured, flour rose, the wheel turned in the water beside the mill stones."],
  ["The Harbour", "Boats creaked at the quay, gulls wheeled over the tide, and the rope strained at the harbour wall."],
  ["The Orchard", "Apples hung from the branches and the ladder leaned on the orchard grass beside the basket."],
  ["The Quay", "At the quay the boats rocked, the gulls cried, the tide pulled the rope along the harbour stones."],
]) docs[title] = (await mk(owner.ctx, `/api/v1/projects/${book.id}/docs`, { title, kind: "scene", content: body })).id;
const text = (page) => page.evaluate(() => document.body.innerText.replace(/\s+/g, " "));
const hscroll = (page) => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
const status = async () => (await api(owner.ctx, "GET", `/api/v1/projects/${book.id}/search/semantic/status`)).json;

await kit.test("off: the page says nothing was sent and offers a preview that sends nothing", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${base}/p/${book.slug}/semantic`, { waitUntil: "load" });
  const t = await text(page);
  ok(t.includes("Search by meaning is off for this project.") && t.includes("Nothing has been sent anywhere."), "off, and nothing sent: " + t.slice(0, 200));
  eq(await page.locator("h1").count(), 1, "one h1");
  await page.fill("#sem-model", "embed-a");
  await page.getByRole("button", { name: "See what would be sent" }).click();
  await page.waitForSelector("#sem-preview");
  const p = await text(page);
  for (const w of ["What would be sent", "Pieces of text", "On this machine or its network", "Private notes, ghosted text", "None: the model is on this machine"]) ok(p.includes(w), `the preview says "${w}"`);
  eq(seen.length, 0, "the preview sent nothing to the endpoint");
  eq((await status()).state, "off", "and it is still off");
  if (shots) await page.screenshot({ path: `${shots}/semantic-preview-1280.png`, fullPage: true });
  kit.main = null;
});

await kit.test("switch it on: indexing runs, the private note never reaches the endpoint, the status says where it stands", async () => {
  const page = owner.page; kit.main = page;
  await page.getByRole("button", { name: "Switch on" }).click();
  await page.waitForSelector(".saved");
  ok((await text(page)).includes("Switched on."), "said");
  // the worker waits for a document to be left alone for 20 seconds
  await waitFor(async () => (await status()).state === "ready", "the index to catch up", 90000);
  await page.goto(`${base}/p/${book.slug}/semantic`, { waitUntil: "load" });
  const t = await text(page);
  ok(t.includes("Up to date") && t.includes("Local embeddings") && t.includes("embed-a"), "the status card: " + t.slice(0, 300));
  ok(seen.length > 0, "the endpoint was asked");
  ok(!seen.some((s) => s.includes("PRIVATE-CANARY")), "a private note was sent to the endpoint");
  ok(!seen.some((s) => s.includes("keep this at home")), "the words of a private note were sent");
  if (shots) await page.screenshot({ path: `${shots}/semantic-status-1280.png`, fullPage: true });
  kit.main = null;
});

await kit.test("By meaning: owners and editors are offered it, a hit opens the chapter at its passage, the question is said to be sent", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${base}/p/${book.slug}/search`, { waitUntil: "load" });
  await page.getByRole("link", { name: "By meaning" }).click();
  await page.waitForSelector("#search-q");
  ok((await text(page)).includes("Nothing is sent until you search."), "nothing is sent before a search");
  const before = seen.length;
  // an address with a question in it sends nothing: only the form does
  await page.goto(`${base}/p/${book.slug}/search?mode=meaning&q=the+smith+struck+the+anvil`, { waitUntil: "load" });
  eq(seen.length, before, "a GET with a question sent nothing");
  await page.fill("#search-q", "the smith struck the anvil with a hammer near the forge");
  await page.keyboard.press("Enter");
  await page.waitForSelector(".sem-hit");
  const t = await text(page);
  ok(t.includes("Your question is sent to Local embeddings, which is on this machine or its network."), "where the question went");
  ok(seen.length > before, "the question reached the endpoint");
  const first = page.locator(".sem-hit a").first();
  eq(await first.innerText(), "The Forge", "the closest scene first");
  if (shots) await page.screenshot({ path: `${shots}/semantic-results-1280.png`, fullPage: true });
  await first.click();
  await page.waitForSelector(".scriobh-prose");
  await waitFor(() => page.evaluate(() => window.getSelection().toString().length > 5), "the passage selected", 5000);
  const sel = await page.evaluate(() => window.getSelection().toString());
  ok(/smith|hammer|forge/i.test(sel), "the passage is selected: " + sel);
  ok(!new URL(page.url()).search, "the address is clean afterwards: " + page.url());
  kit.main = null;
});

await kit.test("a reader sees scenes like this one and nothing else of the feature; it sends nothing", async () => {
  const page = reader.page; kit.main = page;
  kit.allowed.push(/403/);
  await page.goto(`${base}/p/${book.slug}/search`, { waitUntil: "load" });
  ok(!(await text(page)).includes("By meaning"), "a reader is not told about the mode");
  const r = await page.goto(`${base}/p/${book.slug}/semantic`, { waitUntil: "load" });
  eq(r.status(), 403, "the settings page refuses a reader");
  await page.goto(`${base}/p/${book.slug}/d/${docs["The Harbour"]}`, { waitUntil: "load" });
  await page.waitForSelector(".scriobh-prose");
  const before = seen.length;
  await page.click("#insp-open");
  await page.waitForSelector("#sem-like");
  await page.locator("#sem-like summary").click();
  await page.waitForSelector("#sem-like-body .sem-hit", { timeout: 8000 });
  const t = await page.locator("#sem-like-body").innerText();
  ok(/The Quay/.test(t), "the scene that reads alike: " + t.slice(0, 120));
  eq(seen.length, before, "asking for scenes like this one sent nothing");
  // by keyboard: the summary opens with Enter
  kit.main = null;
});

await kit.test("an endpoint that is down is said calmly, not as raw errors and not as 'nothing matches'", async () => {
  const page = owner.page; kit.main = page;
  kit.allowed.push(/50\d/, /Failed to load resource/);
  down = true;
  await page.goto(`${base}/p/${book.slug}/search?mode=meaning`, { waitUntil: "load" });
  await page.fill("#search-q", "the smith and the anvil");
  await page.keyboard.press("Enter");
  await page.waitForSelector("#search-sent");
  const t = await text(page);
  ok(t.includes("could not be done just now"), "said: " + t.slice(0, 300));
  ok(!/\b(500|502|503|ECONN|timeout)\b/i.test(t), "no raw error");
  ok(!t.includes("Nothing close enough"), "and not claimed to be an empty result");
  down = false;
  kit.main = null;
});

await kit.test("pause: the questions stop, and the page says why in words", async () => {
  const page = owner.page; kit.main = page;
  // a search page that was open before the pause
  const other = await owner.ctx.newPage(); kit.watch(other, "tab2");
  await other.goto(`${base}/p/${book.slug}/search?mode=meaning`, { waitUntil: "load" });
  await page.goto(`${base}/p/${book.slug}/semantic`, { waitUntil: "load" });
  await page.getByRole("button", { name: "Pause" }).click();
  await page.waitForSelector(".saved");
  ok((await text(page)).includes("Paused. Nothing more is sent."), "said");
  const before = seen.length;
  await other.fill("#search-q", "anvil");
  await other.keyboard.press("Enter");
  await other.waitForSelector(".notice-line");
  ok((await text(other)).includes("question was not sent"), "said in words");
  eq(await other.locator(".sem-hit").count(), 0, "no result while paused");
  eq(seen.length, before, "no question sent while paused");
  await other.close();
  kit.main = null;
});

await kit.test("delete the index: the typed title enables the button; the index and the choice are gone", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${base}/p/${book.slug}/semantic`, { waitUntil: "load" });
  const btn = page.locator('form[action$="/semantic/purge"] button[type=submit]');
  ok(await btn.isDisabled(), "disabled until the title is typed");
  await page.fill("#sem-purge", "The Salt Roa");
  ok(await btn.isDisabled(), "a wrong title does not enable it");
  await page.fill("#sem-purge", "The Salt Road");
  ok(!(await btn.isDisabled()), "the title enables it");
  await btn.click();
  await page.waitForSelector(".saved");
  ok((await text(page)).includes("The index is deleted"), "said");
  const st = await status();
  eq(st.state, "off", "off"); eq(st.chunks, 0, "no chunks");
  kit.main = null;
});

await kit.test("an endpoint that is not local needs its host typed, and nothing is sent without it", async () => {
  const rp = await api(admin.ctx, "POST", "/api/v1/ai/providers", { name: "Hosted embeddings", kind: "openai_compatible", scope: "instance", base_url: "https://embed.example.test/v1", default_model: "embed-z" });
  ok(rp.status < 300, "a remote provider: " + rp.status);
  const page = owner.page; kit.main = page;
  const before = seen.length;
  await page.goto(`${base}/p/${book.slug}/semantic`, { waitUntil: "load" });
  const hosted = await page.locator("#sem-prov option", { hasText: "Hosted embeddings" }).getAttribute("value");
  await page.selectOption("#sem-prov", hosted);
  await page.fill("#sem-model", "embed-z");
  await page.getByRole("button", { name: "See what would be sent" }).click();
  await page.waitForSelector("#sem-preview");
  const t = await text(page);
  ok(t.includes("This leaves your network.") && t.includes("embed.example.test"), "the remote warning: " + t.slice(t.indexOf("What would"), t.indexOf("What would") + 300));
  const sw = page.getByRole("button", { name: "Switch on" });
  ok(await sw.isDisabled(), "Switch on waits for the host to be typed");
  await page.fill("#sem-host", "somewhere.else");
  ok(await sw.isDisabled(), "and a wrong host does not enable it");
  await page.evaluate(() => { document.querySelector("[data-confirm-submit]").disabled = false; }); // the server refuses it as well, whatever the page does
  await sw.click();
  await page.waitForSelector("#sem-error");
  eq((await status()).state, "off", "a wrong host did not switch it on");
  eq(seen.length, before, "nothing was sent");
  if (shots) await page.screenshot({ path: `${shots}/semantic-remote-1280.png`, fullPage: true });
  kit.main = null;
});

await kit.test("at 390 px and mirrored: the settings and results pages have no sideways scroll, controls are 44 px", async () => {
  const ctx = await kit.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, storageState: await owner.ctx.storageState() });
  const page = await ctx.newPage(); kit.watch(page, "phone"); kit.main = page;
  await page.goto(`${base}/p/${book.slug}/semantic`, { waitUntil: "load" });
  eq(await hscroll(page) <= 0, true, "no sideways scroll on the settings page");
  const small = await page.evaluate(() => [...document.querySelectorAll("main a.btn, main button, main select, main input:not([type=hidden])")].filter((e) => e.offsetParent && e.getBoundingClientRect().height < 43.5).map((e) => e.outerHTML.slice(0, 70)));
  eq(small.length, 0, "controls under 44 px: " + small.join(" | "));
  if (shots) await page.screenshot({ path: `${shots}/semantic-settings-390.png`, fullPage: true });
  await ctx.close();
  await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "xx-rtl" } });
  const p = owner.page; kit.main = p;
  await p.goto(`${base}/p/${book.slug}/semantic`, { waitUntil: "load" });
  eq(await p.evaluate(() => document.documentElement.dir), "rtl", "dir=rtl");
  eq(await hscroll(p) <= 0, true, "no sideways scroll mirrored");
  if (shots) await p.screenshot({ path: `${shots}/semantic-rtl.png`, fullPage: true });
  await api(owner.ctx, "PUT", "/api/v1/me/prefs", { items: { language: "en" } });
  kit.main = null;
});

embed.close();
await kit.finish("e2e-semantic");
