#!/usr/bin/env node
// Makes the README's screenshots and demo video from a seeded throwaway instance (scripts/readme-seed.mjs): real Chromium
// against the development build on an ephemeral loopback port, the stub issuer, no network. Everything shown is invented.
//
//   node scripts/readme-media.mjs [--shots] [--video] [--out docs/media] [--only NAME]
//
// With neither --shots nor --video it does both. The stills are 1440x900 in light and dark and one phone-width pair;
// the video is 1280x720, recorded with Playwright's recordVideo and encoded to WebM (VP8), an animated WebP poster and an
// MP4 (H.264) with ffmpeg and img2webp (Homebrew: `brew install ffmpeg webp`). Metadata is stripped. Build-time only.
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, renameSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createKit } from "./e2e-kit.mjs";
import { repo } from "./harness.mjs";
import { seedDemo } from "./readme-seed.mjs";

const argv = process.argv.slice(2);
const arg = (n, d = "") => (argv.includes(n) ? argv[argv.indexOf(n) + 1] : d);
const OUT = resolve(repo, arg("--out", "docs/media"));
const ONLY = arg("--only");
const wantShots = argv.includes("--shots") || !argv.includes("--video");
const wantVideo = argv.includes("--video") || !argv.includes("--shots");
mkdirSync(OUT, { recursive: true });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
// A failed run stops its own browser and server (by handle) before it exits.
const onFail = (kit) => process.on("uncaughtException", async (e) => {
  console.error(e);
  try { await kit.browser.close(); } catch { /* gone */ }
  try { await kit.srv.stop(); } catch { /* gone */ }
  process.exit(1);
});

// ------------------------------------------------------------------------------------------------ stills
async function stills() {
  const kit = await createKit("readme-shots");
  kit.deadline(10 * 60 * 1000);
  onFail(kit);
  const { ctx: boot, page: bp } = await kit.signedIn();
  const demo = await seedDemo(kit, boot);
  const cookies = await boot.cookies();
  await bp.close();
  const slug = demo.book.slug, gauge = demo.ids["The Gauge"];
  const base = kit.base();
  const clean = (f) => { rmSync(join(OUT, f), { force: true }); };

  const sizes = { desktop: { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 }, phone: { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true } };
  const go = async (page, path) => {
    await page.goto(base + path, { waitUntil: "load" });
    await page.evaluate(() => document.fonts.ready);
    await sleep(700);
  };
  const shot = async (page, name, scheme, o = {}) => {
    if (ONLY && !name.includes(ONLY)) return;
    await sleep(300);
    await page.screenshot({ path: join(OUT, `${name}-${scheme}.png`), ...o });
    console.log("shot", `${name}-${scheme}`);
  };

  for (const scheme of ["light", "dark"]) {
    const ctx = await kit.newContext({ ...sizes.desktop, colorScheme: scheme });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    kit.watch(page, scheme);

    await go(page, "/");
    await shot(page, "library", scheme);

    await go(page, `/p/${slug}/d/${gauge}`);
    await page.waitForSelector(".scriobh-prose .scriobh-alt");
    await page.evaluate(() => window.scriobh.inspector.close());
    await shot(page, "write", scheme);

    // an alternative being cycled: the mark is clicked, the cycler opens, and the next wording is put in place
    await page.locator(".scriobh-prose .scriobh-alt").first().click();
    await page.locator("#alt-cyc").waitFor({ timeout: 5000 });
    await page.getByRole("button", { name: "Next wording" }).click();
    await sleep(900);
    await shot(page, "alternatives", scheme);
    await page.getByRole("button", { name: "Show the original" }).click(); // leave the text as it was, so the next theme starts from the same words
    await sleep(500);

    await go(page, `/p/${slug}/outline`);
    await shot(page, "outline", scheme);

    await go(page, `/c/${demo.bible.slug}/entities`);
    await shot(page, "compendium", scheme);

    await go(page, `/p/${slug}/read/${demo.ids["Isak"]}`);
    await page.waitForSelector("mark.rv-mark");
    await shot(page, "review", scheme);

    await go(page, "/studio");
    await shot(page, "studio", scheme);

    await go(page, `/p/${slug}/compile`);
    await page.waitForSelector("#cmp-preset option", { state: "attached" });
    await page.selectOption("#cmp-preset", "epub3");
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    await page.waitForSelector("#cmp-paper .cmp-page");
    await page.evaluate(() => window.scrollTo(0, 0));
    await sleep(500);
    await shot(page, "compile", scheme);
    await ctx.close();

    const pc = await kit.newContext({ ...sizes.phone, colorScheme: scheme });
    await pc.addCookies(cookies);
    const pp = await pc.newPage();
    kit.watch(pp, "phone-" + scheme);
    await go(pp, `/p/${slug}/d/${gauge}`);
    await pp.waitForSelector(".scriobh-prose");
    await shot(pp, "phone-write", scheme);
    await pc.close();
  }
  optimise();
  await kit.finish("readme-shots");
}

// Lossless PNG recompression, and every ancillary chunk (text, time, colour profile) stripped. Skipped, with a note, when oxipng
// is not installed (`brew install oxipng`).
function optimise() {
  const files = readdirSync(OUT).filter((f) => f.endsWith(".png")).map((f) => join(OUT, f));
  const r = spawnSync("oxipng", ["--opt", "4", "--strip", "all", "--quiet", ...files], { encoding: "utf8" });
  if (r.error || r.status !== 0) { console.log("oxipng not available or failed; the PNGs are as Chromium wrote them", r.error ? r.error.message : r.stderr); return; }
  console.log("optimised", files.length, "PNGs:", Math.round(files.reduce((n, f) => n + statSync(f).size, 0) / 1024), "KB");
}

// ------------------------------------------------------------------------------------------------ video
// A synthetic pointer and a caption bar, drawn into the page for the recording only (a headless recording has no pointer).
// The app's CSP forbids style attributes written as markup, so the styles are set through the CSSOM.
const overlay = () => {
  const set = (el, o) => { for (const k of Object.keys(o)) el.style.setProperty(k, o[k]); };
  const mount = () => {
    if (document.getElementById("rm-cursor")) return;
    const c = document.createElement("div"); c.id = "rm-cursor";
    set(c, { position: "fixed", left: "0", top: "0", width: "22px", height: "22px", "margin-left": "-11px", "margin-top": "-11px", "border-radius": "50%", background: "rgba(20,20,20,0.55)", border: "2px solid #fff", "box-shadow": "0 0 0 1px rgba(0,0,0,0.35)", "pointer-events": "none", "z-index": "2147483647", transition: "transform 90ms ease-out" });
    const cap = document.createElement("div"); cap.id = "rm-caption";
    set(cap, { position: "fixed", left: "50%", bottom: "26px", transform: "translateX(-50%)", padding: "9px 20px", "border-radius": "999px", background: "rgba(24,24,22,0.88)", color: "#fff", font: "500 17px/1.2 system-ui, sans-serif", "pointer-events": "none", "z-index": "2147483646", opacity: "0", transition: "opacity 250ms ease", "white-space": "nowrap" });
    document.body.append(c, cap);
    addEventListener("mousemove", (e) => set(c, { left: e.clientX + "px", top: e.clientY + "px" }), true);
    addEventListener("mousedown", () => set(c, { transform: "scale(0.7)" }), true);
    addEventListener("mouseup", () => set(c, { transform: "scale(1)" }), true);
  };
  if (document.body) mount(); else document.addEventListener("DOMContentLoaded", mount);
  window.__caption = (t) => { const e = document.getElementById("rm-caption"); if (!e) return; if (t) { e.textContent = t; e.style.setProperty("opacity", "1"); } else e.style.setProperty("opacity", "0"); };
};

const ff = (args) => { const r = spawnSync("ffmpeg", ["-v", "error", "-y", ...args], { encoding: "utf8" }); if (r.status !== 0) throw new Error("ffmpeg " + args.join(" ") + "\n" + r.stderr); };

async function video() {
  const kit = await createKit("readme-video");
  kit.deadline(10 * 60 * 1000);
  onFail(kit);
  const { ctx: boot, page: bp } = await kit.signedIn();
  const demo = await seedDemo(kit, boot);
  const cookies = await boot.cookies();
  await bp.close();
  const slug = demo.book.slug, base = kit.base();
  const raw = join(kit.tmp, "raw");
  const W = 1280, H = 720;
  const ctx = await kit.newContext({ viewport: { width: W, height: H }, deviceScaleFactor: 1, colorScheme: "light", recordVideo: { dir: raw, size: { width: W, height: H } } });
  const t0 = Date.now();
  await ctx.addCookies(cookies);
  await ctx.addInitScript(overlay);
  const page = await ctx.newPage();
  kit.watch(page, "video");
  const log = (...a) => console.log(((Date.now() - t0) / 1000).toFixed(1).padStart(5) + "s", ...a);
  const caption = async (t) => { await page.evaluate((x) => window.__caption && window.__caption(x), t).catch(() => {}); };
  let mx = W / 2, my = H / 2;
  const moveTo = async (loc, o = {}) => {
    await loc.scrollIntoViewIfNeeded();
    const b = await loc.boundingBox();
    const x = b.x + (o.dx ?? b.width / 2), y = b.y + (o.dy ?? b.height / 2);
    const steps = Math.max(12, Math.min(40, Math.round(Math.hypot(x - mx, y - my) / 18)));
    await page.mouse.move(x, y, { steps }); mx = x; my = y;
    await sleep(o.pause ?? 250);
  };
  const click = async (loc, o = {}) => { await moveTo(loc, o); await page.mouse.down(); await sleep(70); await page.mouse.up(); await sleep(o.after ?? 400); };
  const scroll = async (dy, ms = 2200) => { const n = Math.round(ms / 40); for (let i = 0; i < n; i++) { await page.mouse.wheel(0, dy / n); await sleep(40); } };

  // The page is up and settled: the recording is cut to start here.
  await page.goto(base + "/", { waitUntil: "load" });
  await page.evaluate(() => document.fonts.ready);
  await page.mouse.move(mx, my);
  await sleep(1200);
  const start = (Date.now() - t0) / 1000;
  log("start");

  await caption("Your library: every project, one shelf");
  await sleep(2200);
  await click(page.locator("a", { hasText: "The Long Slack" }).first(), { after: 1500 });
  await page.waitForSelector(".scriobh-prose");
  await page.evaluate(() => document.fonts.ready);
  await caption("The writing view: calm, with the manuscript tree");
  await sleep(1500);
  await click(page.locator("#tree a, nav a, .tree a", { hasText: "The Gauge" }).first(), { after: 1300 });
  await page.waitForSelector(".scriobh-prose .scriobh-alt");
  await page.evaluate(() => window.scriobh.inspector.close());
  await sleep(900);

  log("type");
  await caption("Type; nothing moves while you write");
  const last = page.locator(".scriobh-prose p", { hasText: "She let it." }).first();
  await click(last, { dx: 30, after: 300 });
  await page.keyboard.press("End");
  await sleep(300);
  await page.keyboard.type(" The water went on without her.", { delay: 85 });
  await sleep(1400);

  log("alternatives");
  await caption("Keep other wordings beside the text and cycle them in place");
  await click(page.locator(".scriobh-prose .scriobh-alt").first(), { after: 1100 });
  await page.locator("#alt-cyc").waitFor({ timeout: 5000 });
  await click(page.getByRole("button", { name: "Next wording" }), { after: 1500 });
  await click(page.getByRole("button", { name: "Next wording" }), { after: 1500 });
  await click(page.getByRole("button", { name: "Show the original" }), { after: 1400 });
  await page.keyboard.press("Escape");
  await sleep(300);

  log("outline");
  await caption("The outline: synopses, status and word targets");
  await click(page.getByRole("link", { name: "Outline", exact: true }).first(), { after: 1200 });
  await page.waitForSelector(".orow-syn, [id^=o-]");
  await sleep(1400);
  await scroll(520, 2400);
  await sleep(1000);

  log("studio");
  await caption("The Writing Studio: your own numbers, never anyone else\u2019s");
  await click(page.getByRole("link", { name: "Library" }).first(), { after: 900 });
  await click(page.getByRole("link", { name: "Writing studio" }).first(), { after: 1200 });
  await page.waitForSelector(".st-card");
  await sleep(1800);
  await scroll(380, 2000);
  await sleep(1200);

  log("compile");
  await caption("Compile: choose a preset, preview it, download an EPUB");
  await page.goto(`${base}/p/${slug}/compile`, { waitUntil: "load" });
  await page.evaluate(() => document.fonts.ready);
  await sleep(1400);
  await moveTo(page.locator("#cmp-preset"));
  await page.selectOption("#cmp-preset", "epub3");
  await sleep(900);
  await click(page.getByRole("button", { name: "Preview", exact: true }), { after: 400 });
  await page.waitForSelector("#cmp-paper .cmp-page");
  await page.evaluate(() => window.scrollTo(0, 0));
  await sleep(2000);
  const [dl] = await Promise.all([page.waitForEvent("download"), click(page.locator("#cmp-download"), { after: 200 })]);
  await dl.saveAs(join(kit.tmp, dl.suggestedFilename()));
  log("downloaded", dl.suggestedFilename());
  await page.waitForSelector("#cmp-manifest:not([hidden])");
  await page.locator("#cmp-manifest").scrollIntoViewIfNeeded();
  await caption("An EPUB, with a manifest of what went in");
  await sleep(3200);
  await caption("");
  await sleep(500);
  const dur = (Date.now() - t0) / 1000 - start;
  const vpath = await page.video().path();
  await ctx.close();
  log("recorded", dur.toFixed(1) + "s");
  encode(vpath, start, dur);
  optimise();
  await kit.finish("readme-video");
}

// WebM (VP8) and an animated WebP poster, as the owner's other projects ship them, and an MP4 (H.264) for anything that wants one.
function encode(src, start, dur) {
  const meta = ["-an", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:v", "+bitexact"];
  const webm = join(OUT, "demo.webm"), mp4 = join(OUT, "demo.mp4"), webp = join(OUT, "demo.webp");
  ff(["-ss", String(start), "-i", src, "-t", String(dur), "-c:v", "libvpx", "-b:v", "0", "-crf", process.env.CRF || "26", "-qmin", "4", "-qmax", "42", "-pix_fmt", "yuv420p", "-auto-alt-ref", "0", ...meta, webm]);
  ff(["-i", webm, "-c:v", "libx264", "-preset", "slow", "-crf", process.env.MP4_CRF || "27", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-r", "25", ...meta, "-map_metadata", "-1", mp4]);
  // x264 writes its option string into an SEI NAL unit; drop it (and the container's encoder tag), keeping the video as it is
  const slim = mp4 + ".slim.mp4";
  ff(["-i", mp4, "-c", "copy", "-bsf:v", "filter_units=remove_types=6", "-movflags", "+faststart", "-map_metadata", "-1", "-metadata:s:v:0", "encoder=", "-fflags", "+bitexact", slim]);
  renameSync(slim, mp4);
  const fr = mkdtempSync(join(tmpdir(), "scriobh-frames-"));
  try {
    ff(["-i", webm, "-vf", `fps=${process.env.WEBP_FPS || 6},scale=${process.env.WEBP_W || 800}:-1:flags=lanczos`, join(fr, "%04d.png")]);
    const frames = readdirSync(fr).filter((f) => f.endsWith(".png")).sort();
    const d = Math.round(1000 / Number(process.env.WEBP_FPS || 6));
    const list = join(fr, "args.txt");
    writeFileSync(list, ["-loop", "0", "-lossy", "-q", process.env.WEBP_Q || "50", "-m", "6", "-d", String(d), ...frames.map((f) => join(fr, f)), "-o", webp].join("\n"));
    const r = spawnSync("img2webp", [list], { encoding: "utf8" });
    if (r.status !== 0) throw new Error("img2webp: " + r.stderr + r.stdout);
  } finally { rmSync(fr, { recursive: true, force: true }); }
  ff(["-ss", "6", "-i", webm, "-frames:v", "1", join(OUT, "demo-poster.png")]); // the writing view, as a still for a video's poster
  for (const f of [webm, mp4, webp]) console.log(f, Math.round(statSync(f).size / 1024), "KB");
}

// With neither flag both stages run, each in a process of its own (a stage ends by exiting).
if (wantShots && wantVideo) {
  for (const stage of ["--shots", "--video"]) {
    const r = spawnSync(process.execPath, [new URL(import.meta.url).pathname, stage, "--out", OUT, ...(ONLY ? ["--only", ONLY] : [])], { stdio: "inherit" });
    if (r.status !== 0) process.exit(r.status || 1);
  }
} else if (wantShots) await stills();
else await video();
