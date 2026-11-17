#!/usr/bin/env node
// End-to-end test of a project's custom typography (F-085) in a real Chromium: that the settings reach the writing and
// reading views, that the measure is the same number of characters at every size (the writing view sizes its page in the
// page's own unscaled font), that the scene break and the plain-text stand-in follow the setting, that a reader's own editor
// settings and typography override win over the project's, and that a project with none is drawn as it was.
//
//   node scripts/e2e-typography.mjs
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-typography");
kit.deadline(300000);
const { api } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;
await pg.setViewportSize({ width: 2000, height: 1000 }); // wide enough that the viewport never decides the measure

const made = await api(main, "POST", "/api/v1/projects", { title: "Typeset book", kind: "novel" });
const proj = made.json.item;
const para = (n) => Array.from({ length: 60 }, (_, i) => `word${n}x${i}`).join(" ");
const chapter = (await api(main, "GET", `/api/v1/projects/${proj.id}/docs`)).json.order[0];
await api(main, "PATCH", `/api/v1/docs/${chapter}`, { base_revision: 1, content: `${para(1)}\n\n${para(2)}\n\n---\n\n${para(3)}\n\n${para(4)}\n` });
// a scene under the chapter: the reading view puts a separator before a scene
const scene = await api(main, "POST", `/api/v1/projects/${proj.id}/docs`, { title: "A scene", kind: "scene", parent_id: chapter, content: `${para(5)}\n\n${para(6)}\n` });
ok(scene.status === 201, "the scene was not made: " + scene.status);
const writeUrl = `${base()}/p/${proj.slug}/d/${chapter}`;
const readUrl = `${base()}/p/${proj.slug}/read/${chapter}`;
const set = (body) => api(main, "PUT", `/api/v1/projects/${proj.id}/typography`, body);
const reset = () => api(main, "POST", `/api/v1/projects/${proj.id}/typography/reset`);

// characters per line of an element: its width over the width of 1ch in its own font
const chars = (page, sel) => page.evaluate((s) => {
  const el = document.querySelector(s);
  const probe = document.createElement("span");
  probe.style.cssText = "display:inline-block;width:1ch;visibility:hidden";
  el.appendChild(probe);
  const ch = probe.getBoundingClientRect().width;
  probe.remove();
  const cs = getComputedStyle(el);
  const inner = el.getBoundingClientRect().width - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
  return { chars: inner / ch, px: parseFloat(cs.fontSize), ch };
}, sel);
const editorReady = async (page) => { await page.locator(".scriobh-prose").waitFor(); await page.waitForFunction(() => !!document.querySelector(".scriobh-prose p")); };
const lines = [];

await kit.test("typography: a project with none is drawn as before", async () => {
  await pg.goto(writeUrl);
  await editorReady(pg);
  const tag = await pg.evaluate(() => document.getElementById("paper").outerHTML.split(">")[0]);
  ok(!/ data-t[a-z]+=/.test(tag), "an attribute on the paper: " + tag);
  const links = await pg.evaluate(() => [...document.querySelectorAll('link[rel=stylesheet]')].map((l) => l.href).filter((h) => /typography/.test(h)));
  eq(links.length, 0, "typography.css is linked though nothing is set");
  const m = await chars(pg, ".scriobh-prose");
  lines.push(`default: ${m.chars.toFixed(1)} characters a line at ${m.px}px`);
});

for (const size of [100, 140]) {
  for (const measure of [40, 80]) {
    await kit.test(`typography: measure ${measure} at ${size} percent is ${measure} characters in both views`, async () => {
      const r = await set({ size, measure });
      ok(r.status === 200, "set: " + r.status + JSON.stringify(r.json));
      await pg.goto(writeUrl);
      await editorReady(pg);
      const w = await chars(pg, ".scriobh-prose");
      const base0 = 19;
      ok(Math.abs(w.px - base0 * size / 100) < 0.6, `the editor's text is ${w.px}px, want about ${base0 * size / 100}`);
      // within 4 percent: the unscaled default measures 64.6 characters for 66 in this browser (the probe's font against the page's), so the check is that scaling does not move it
      const tol = measure * 0.04 + 0.5;
      ok(Math.abs(w.chars - measure) <= tol, `writing view: ${w.chars.toFixed(1)} characters a line, want ${measure} (within ${tol.toFixed(1)})`);
      // the plain-text stand-in is drawn at the same size, so the text does not jump when the editor mounts
      const noScript = await kit.browser.newContext({ storageState: await main.storageState(), javaScriptEnabled: false, serviceWorkers: "block", viewport: { width: 2000, height: 1000 } });
      const np = await noScript.newPage();
      await np.goto(writeUrl);
      const a = await np.evaluate(() => getComputedStyle(document.getElementById("doc-plain")).fontSize);
      await noScript.close();
      const b = w.px + "px";
      ok(Math.abs(parseFloat(a) - w.px) < 0.05, `doc-plain (${a}) and the editor (${b}) differ in size`);
      await pg.goto(readUrl);
      await pg.locator(".chapter").first().waitFor();
      const rd = await chars(pg, ".chapter");
      // the reading view's page also fills at most the column the layout leaves it (beside the review margin): at 140 percent 80
      // characters are wider than that column on a 2000 px screen, and the line is then as wide as the column
      const room = await pg.evaluate(() => { const p = document.getElementById("read-stream"); const cs = getComputedStyle(p); return p.getBoundingClientRect().width - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight); });
      const limited = rd.chars < measure - tol && Math.abs(room - rd.chars * rd.ch) < 2 && room < measure * rd.ch;
      ok(Math.abs(rd.chars - measure) <= tol || limited, `reading view: ${rd.chars.toFixed(1)} characters a line, want ${measure} (within ${tol.toFixed(1)}); room ${room.toFixed(0)}px`);
      if (limited) lines.push(`  (the reading column left ${room.toFixed(0)}px, which is ${(room / rd.ch).toFixed(1)} characters at this size)`);
      lines.push(`size ${size}%, measure ${measure}: writing ${w.chars.toFixed(1)} chars at ${w.px}px, plain ${a}, reading ${rd.chars.toFixed(1)} chars at ${rd.px}px`);
    });
  }
}

await kit.test("typography: a size alone keeps the default measure, and a reader's full width is left as it was", async () => {
  await reset();
  await set({ size: 140 });
  await pg.goto(writeUrl);
  await editorReady(pg);
  const w = await chars(pg, ".scriobh-prose");
  ok(Math.abs(w.chars - 64.6) <= 2, `size 140 alone, writing view: ${w.chars.toFixed(1)} characters a line, want about the default 64.6`);
  await pg.goto(readUrl);
  await pg.locator(".chapter").first().waitFor();
  const r = await chars(pg, ".chapter");
  // 66 characters at 140 percent need about 950 px and the reading column leaves 864 px here, so the line is the column's width
  const room = await pg.evaluate(() => { const p = document.getElementById("read-stream"); const cs = getComputedStyle(p); return p.getBoundingClientRect().width - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight); });
  ok((r.chars >= 64 && r.chars <= 68) || Math.abs(room - r.chars * r.ch) < 2, `size 140 alone, reading view: ${r.chars.toFixed(1)} characters a line (the column leaves ${room.toFixed(0)}px), want the default 66 or the column`);
  lines.push(`size 140 alone: writing ${w.chars.toFixed(1)} chars, reading ${r.chars.toFixed(1)} chars`);
  // a reader whose own measure is full width: the project's size does not change the width of either page
  await api(main, "PUT", "/api/v1/me/prefs", { items: { editor_measure: "full" } });
  const width = async (url, sel) => { await pg.goto(url); await pg.locator(sel).first().waitFor(); return pg.evaluate((q) => document.querySelector(q).getBoundingClientRect().width, sel); };
  const withSize = [await width(writeUrl, ".scriobh-prose"), await width(readUrl, ".chapter")];
  await reset();
  const without = [await width(writeUrl, ".scriobh-prose"), await width(readUrl, ".chapter")];
  ok(Math.abs(withSize[0] - without[0]) < 1.5, `a reader's full width in the writing view changed with the project's size: ${withSize[0]} against ${without[0]}`);
  ok(Math.abs(withSize[1] - without[1]) < 1.5, `a reader's full width in the reading view changed with the project's size: ${withSize[1]} against ${without[1]}`);
  lines.push(`reader's full width, with and without a project size: writing ${withSize[0].toFixed(0)} / ${without[0].toFixed(0)} px, reading ${withSize[1].toFixed(0)} / ${without[1].toFixed(0)} px`);
  await api(main, "PUT", "/api/v1/me/prefs", { items: { editor_measure: "66" } });
});

await kit.test("typography: a rule for scene breaks, in the reading view's separator and the editor's", async () => {
  await reset();
  await pg.goto(readUrl);
  await pg.locator(".r-sec > hr.scene-break").first().waitFor();
  const before = await pg.evaluate(() => { const e = document.querySelector(".r-sec > hr.scene-break"); return getComputedStyle(e, "::before").content; });
  ok(/\*/.test(before), "the default separator is not three asterisks: " + before);
  await set({ scene_break: "rule" });
  await pg.reload();
  await pg.locator(".r-sec > hr.scene-break").first().waitFor();
  const rule = await pg.evaluate(() => { const c = getComputedStyle(document.querySelector(".r-sec > hr.scene-break"), "::before"); return { content: c.content, border: c.borderTopWidth, style: c.borderTopStyle }; });
  ok(rule.content === '""' || rule.content === "none" || rule.content === "normal", "the reading view's scene separator still shows its asterisks: " + rule.content);
  eq(rule.style, "solid", "the separator has no rule");
  eq(rule.border, "1px", "the rule's width");
  await pg.goto(writeUrl);
  await editorReady(pg);
  const w = await pg.evaluate(() => { const e = document.querySelector(".scriobh-prose .scene-break"); if (!e) return null; const c = getComputedStyle(e, "::before"); return { content: c.content, style: c.borderTopStyle }; });
  ok(w && w.style === "solid" && (w.content === '""' || w.content === "none"), "the editor's scene break: " + JSON.stringify(w));
  lines.push(`scene break rule: reading ${JSON.stringify(rule)}, writing ${JSON.stringify(w)}`);
});

await kit.test("typography: indent, face and line height", async () => {
  await set({ paragraphs: "indent", font: "system-mono", line_height: "1.8", size: 120 });
  await pg.goto(writeUrl);
  await editorReady(pg);
  const m = await pg.evaluate(() => {
    const ps = [...document.querySelectorAll(".scriobh-prose > p")];
    const cs = (e) => getComputedStyle(e);
    return { first: cs(ps[0]).textIndent, second: cs(ps[1]).textIndent, size: parseFloat(cs(ps[1]).fontSize), gap: cs(ps[1]).marginBottom, face: cs(document.querySelector(".scriobh-prose")).fontFamily, lh: parseFloat(cs(document.querySelector(".scriobh-prose")).lineHeight) / parseFloat(cs(document.querySelector(".scriobh-prose")).fontSize), afterBreak: (() => { const br = document.querySelector(".scriobh-prose .scene-break"); return br && br.nextElementSibling ? cs(br.nextElementSibling).textIndent : null; })() };
  });
  eq(m.first, "0px", "the first paragraph is indented");
  eq(parseFloat(m.second).toFixed(1), (1.5 * m.size).toFixed(1), "the second paragraph's indent");
  eq(m.gap, "0px", "paragraphs keep a gap");
  ok(/ui-monospace/.test(m.face), "the face: " + m.face);
  ok(Math.abs(m.lh - 1.8) < 0.02, "the line height ratio: " + m.lh);
  if (m.afterBreak !== null) eq(m.afterBreak, "0px", "the paragraph after a scene break is indented");
  lines.push(`indent ${m.second} at ${m.size}px, face ${m.face.slice(0, 40)}, line height ${m.lh.toFixed(2)}`);
});

await kit.test("typography: a reader's own editor settings and typography override win over the project", async () => {
  await set({ line_height: "1.3", measure: 80, font: "mono", size: 100, paragraphs: "spacing", scene_break: "ornament" });
  await api(main, "PUT", "/api/v1/me/prefs", { items: { editor_line_height: "1.9", editor_measure: "full", editor_font: "sans" } });
  await pg.goto(writeUrl);
  await editorReady(pg);
  const m = await pg.evaluate(() => { const e = document.querySelector(".scriobh-prose"); const cs = getComputedStyle(e); return { lh: parseFloat(cs.lineHeight) / parseFloat(cs.fontSize), face: cs.fontFamily, paper: document.getElementById("paper").outerHTML.split(">")[0] }; });
  ok(Math.abs(m.lh - 1.9) < 0.02, "the reader's own 1.9 line height was replaced: " + m.lh);
  ok(/Instrument Sans/.test(m.face), "the reader's own typeface was replaced: " + m.face);
  ok(!/data-tlh|data-tm=/.test(m.paper), "the project's line height or measure reached the page: " + m.paper);
  await api(main, "PUT", "/api/v1/me/typography", { line_height: "1.3" });
  await pg.reload();
  await editorReady(pg);
  const o = await pg.evaluate(() => { const cs = getComputedStyle(document.querySelector(".scriobh-prose")); return parseFloat(cs.lineHeight) / parseFloat(cs.fontSize); });
  ok(Math.abs(o - 1.3) < 0.02, "the personal override does not win: " + o);
  lines.push(`reader's 1.9 kept under a project 1.3 (${m.lh.toFixed(2)}); override 1.3 wins (${o.toFixed(2)})`);
  await api(main, "POST", "/api/v1/me/typography/reset");
});

console.log("\nmeasurements:\n  " + lines.join("\n  "));
await kit.finish("e2e-typography");
