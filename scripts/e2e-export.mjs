#!/usr/bin/env node
// Opens the HTML export in a real Chromium: the page's own policy lets its one
// stylesheet apply (the hash matches), blocks everything else, runs nothing a
// hostile manuscript tried, makes no request, and follows light, dark and print.
//
//   node scripts/e2e-export.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { mkdirSync } from "node:fs";

const kit = await createKit("e2e-export");
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const { ctx } = await kit.signedIn();

const hostile = "<script>window.__xss = 1</script>\n\n<img src=x onerror=\"window.__xss = 2\">\n\n[click](javascript:window.__xss=3)\n\n" +
  "![beacon](https://tracker.invalid/p.gif)\n\n<style>body{background:red}</style>\n\n<iframe src=\"https://tracker.invalid/frame\"></iframe>";
const made = await api(ctx, "POST", "/api/v1/projects", { title: "The <b>Salt</b> Road", kind: "novel", subtitle: "A novel of the coast", author_name: "Máire Ní Bhriain" });
const p = made.json.item;
const docs = (await api(ctx, "GET", `/api/v1/projects/${p.id}/docs`)).json;
const first = docs.order[0];
await api(ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: 1, title: "Arrival", content: "She came *late* on the **last** ferry, and the harbour was already dark.\n\n* * *\n\nA second scene, with a [real link](https://example.org/a), a note.[^1] %%A private aside.%%\n\n[^1]: The note.\n\n" + hostile + "\n" });
await api(ctx, "POST", `/api/v1/projects/${p.id}/docs`, { title: "<img src=x onerror=alert(1)> Departure", kind: "chapter", content: "See [[Arrival]]. The tide turned.\n" });

const exportHTML = async (mode, body = {}) => {
  const r = await ctx.request.fetch(`${kit.base()}/api/v1/projects/${p.id}/export/html`, { method: "POST", data: { mode, ...body }, headers: { "Content-Type": "application/json" } });
  eq(r.status(), 200, "the export");
  return { body: await r.body(), type: r.headers()["content-type"], cd: r.headers()["content-disposition"] };
};

await kit.test("the exported page applies its own style, runs nothing and loads nothing", async () => {
  const exp = await exportHTML("single");
  ok(/^text\/html/.test(exp.type) && /attachment/.test(exp.cd), "the response is a downloaded HTML file");
  const html = exp.body.toString("utf8");
  ok(!html.includes("A private aside"), "a private note is in the export");
  const requests = [];
  const page = await kit.browser.newPage({ viewport: { width: 900, height: 900 } });
  page.on("request", (r) => requests.push(r.url()));
  const violations = [];
  await page.addInitScript(() => { document.addEventListener("securitypolicyviolation", (e) => (window.__v = (window.__v || []).concat(e.violatedDirective + " " + e.blockedURI))); });
  // Served from an origin of its own, so nothing here is the application's.
  await page.route("http://export.test/**", (route) => route.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: exp.body }));
  await page.goto("http://export.test/the-salt-road.html", { waitUntil: "load" });
  const probe = await page.evaluate(() => ({
    xss: window.__xss, v: window.__v || [],
    bg: getComputedStyle(document.body).backgroundColor,
    font: getComputedStyle(document.body).fontFamily,
    h1: getComputedStyle(document.querySelector("h1.title")).fontSize,
    scripts: document.querySelectorAll("script").length, iframes: document.querySelectorAll("iframe").length, imgs: document.querySelectorAll("img").length,
    redBody: [...document.querySelectorAll("style")].length,
    cover: (() => { const r = document.querySelector(".cover svg").getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; })(),
    toc: document.querySelectorAll(".toc a").length,
    anchors: [...document.querySelectorAll(".toc a")].every((a) => document.getElementById(a.getAttribute("href").slice(1))),
    text: document.body.innerText,
  }));
  eq(probe.xss, undefined, "a hostile script ran");
  eq(probe.bg, "rgb(251, 250, 247)", "the page's stylesheet did not apply (its hash is not in the policy?)");
  ok(/Newsreader|Iowan|Palatino|Georgia|serif/.test(probe.font), "the serif stack: " + probe.font);
  eq(probe.scripts + probe.iframes + probe.imgs, 0, "script, iframe or img elements");
  eq(probe.redBody, 1, "a second style element");
  ok(probe.cover[0] > 100 && probe.cover[1] > 100, "the cover is drawn: " + probe.cover);
  ok(probe.toc >= 2 && probe.anchors, "the contents links land on headings");
  ok(probe.text.includes("<script>window.__xss = 1</script>") && probe.text.includes("tracker.invalid"), "hostile text is shown as text");
  ok(probe.text.includes("The <b>Salt</b> Road") && probe.text.includes("<img src=x onerror=alert(1)> Departure"), "hostile titles are shown as text");
  ok(!probe.text.includes("A private aside"), "private note shown");
  ok(probe.v.length === 0, "policy violations: " + probe.v.join(", "));
  const outside = requests.filter((u) => !u.startsWith("http://export.test/") && !u.startsWith("data:"));
  eq(outside.length, 0, "requests outside the file: " + outside.join(", "));
  // The wiki link resolves in the page.
  const wiki = await page.evaluate(() => { const a = document.querySelector("a.wiki"); return a ? a.getAttribute("href") : ""; });
  ok(/^#d\d+-arrival$/.test(wiki), "wiki link " + wiki);
  await page.click("a.wiki");
  ok((await page.evaluate(() => location.hash)).startsWith("#d"), "following the link");

  // Dark follows the system; print drops the screen background.
  await page.emulateMedia({ colorScheme: "dark" });
  eq(await page.evaluate(() => getComputedStyle(document.body).backgroundColor), "rgb(19, 19, 17)", "dark scheme");
  if (shots) await page.screenshot({ path: `${shots}/export-dark.png`, fullPage: false });
  await page.emulateMedia({ colorScheme: "light" });
  if (shots) await page.screenshot({ path: `${shots}/export-light.png`, fullPage: true });
  await page.setViewportSize({ width: 390, height: 800 });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
  ok(!overflow, "the page scrolls sideways at 390 px");
  if (shots) await page.screenshot({ path: `${shots}/export-390.png`, fullPage: false });
  await page.emulateMedia({ media: "print" });
  eq(await page.evaluate(() => getComputedStyle(document.body).backgroundColor), "rgb(255, 255, 255)", "print background");
  await page.close();
});

await kit.finish("e2e-export");
