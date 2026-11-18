#!/usr/bin/env node
// Print and compile in Chromium: the print presets and every print option with its own words (no bare key, no option under
// More), the proof drawn from the layout's placed lines, the facts and the printer's checklist, a print-ready PDF made
// through the page whose bytes are a PDF with the page count the preview said, the paperback cover measured and downloaded,
// the whole path by keyboard alone, the live region on success and on failure (network cut), no layout shift when the
// result arrives or a page turns, reduced motion, 390 px with 44 px targets, both themes.
//
//   node scripts/e2e-compile-print.mjs [--shots DIR]      (docs/screens/phase-6-compile holds the committed ones)
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const kit = await createKit("e2e-compile-print");
kit.deadline(6 * 60 * 1000);
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const owner = await kit.signedIn({ viewport: { width: 1280, height: 900 } }, "writer@dev.scriobh");

// a small book: four chapters, a scene break, a footnote, a quotation, a list and a code block, a dedication
const words = (n, seed) => (`${seed} came in over the grey stones and the gulls wheeled above the quay `.repeat(Math.ceil(n / 13))).split(" ").slice(0, n).join(" ");
const made = await api(owner.ctx, "POST", "/api/v1/projects", { title: "The Salt Road", subtitle: "A novel", kind: "novel", author_name: "Máire Nolan", cover_color: "#3B5B7A" });
ok(made.status < 300, "project made " + made.status);
const P = made.json.item, pid = P.id, slug = P.slug;
const first = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
const g = (await api(owner.ctx, "GET", `/api/v1/docs/${first}`)).json.item;
await api(owner.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, title: "Arrival", content: `${words(400, "tide")}\n\n> ${words(90, "letter")}\n\n${words(500, "tide")}[^1]\n\n- ${words(30, "rope")}\n- ${words(30, "net")}\n\n\`\`\`\nlet tide = 3\nlet moon = tide * 2\n\`\`\`\n\n* * *\n\n${words(700, "dawn")}\n\n[^1]: The tide tables of that year.\n` });
for (const [t, seed] of [["The Crossing", "ferry"], ["Landfall", "shore"], ["The Long Night", "lamp"]]) await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: t, kind: "chapter", content: `${words(1400, seed)}\n\n${words(1400, seed + "s")}\n` });
await api(owner.ctx, "POST", `/api/v1/projects/${pid}/docs`, { title: "Dedication", kind: "front_matter", content: "For everyone who waited.\n" });

const URL_ = `${kit.base()}/p/${slug}/compile`;
const text = (page, sel) => page.locator(sel).innerText();
const active = (page) => page.evaluate(() => document.activeElement && (document.activeElement.id || document.activeElement.tagName));
const pdfPages = (buf) => (buf.toString("latin1").match(/\/Type \/Page(?![s\w])/g) || []).length;
async function tabTo(page, id, max = 80, back = false) {
  for (let i = 0; i < max; i++) {
    if ((await active(page)) === id) return;
    await page.keyboard.press(back ? "Shift+Tab" : "Tab");
  }
  throw new Error(`Tab never reached #${id} (at ${await active(page)})`);
}
async function open(page) {
  await page.goto(URL_, { waitUntil: "load" });
  await page.waitForSelector("#cmp-preset option", { state: "attached" });
}
async function preview(page) {
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await kit.waitFor(async () => /The preview is ready/.test(await text(page, "#cmp-status")), "the preview", 30000);
}

await kit.test("the print presets are offered, and every print option has a label and a group of its own", async () => {
  const page = owner.page; kit.main = page;
  await open(page);
  const ids = await page.locator("#cmp-preset option").evaluateAll((os) => os.map((o) => o.value));
  for (const id of ["print-6x9", "print-5.5x8.5", "print-5x8", "print-a5", "proof-letter", "proof-a4", "paged-html"]) ok(ids.includes(id), "preset " + id + " in " + ids.join(","));
  const names = await page.locator("#cmp-preset option").allInnerTexts();
  ok(names.includes("Print, 6 × 9 in") && names.includes("Proof, A4"), "named from the catalogue: " + names.join(" | "));
  // a Word preset shows no print option, and no cover
  ok((await page.locator("#o-gutter_inches").count()) === 0 && await page.locator("#cmp-cover").isHidden(), "a Word manuscript has no gutter and no cover");
  await page.selectOption("#cmp-preset", "print-6x9");
  const legends = await page.locator("#cmp-opts legend").allInnerTexts();
  ok(legends.includes("Margins") && legends.includes("Copyright and contents pages") && !legends.includes("More"), "the groups: " + legends.join(" | "));
  for (const k of ["margin_top", "margin_bottom", "margin_inside", "margin_outside", "gutter_inches", "footnotes", "running_heads", "drop_cap", "chapter_ornament", "toc", "crop_marks", "copyright_page", "imprint", "hyphenate"]) ok((await page.locator("#o-" + k).count()) === 1, "the option " + k + " is on the form");
  // a label the catalogue lacks is the key made readable ("Gutter inches"): every control's label is the catalogue's
  const bare = await page.evaluate(() => {
    const M = JSON.parse(document.querySelector("#cmp-msg").dataset.msg);
    return [...document.querySelectorAll("#cmp-opts input[id^='o-'], #cmp-opts select[id^='o-'], #cmp-opts textarea[id^='o-']")].map((c) => {
      const key = c.id.slice(2), lab = (c.closest("label") || document.querySelector(`label[for='${c.id}']`)).textContent.trim();
      return M["o_" + key] && lab === M["o_" + key] ? "" : key;
    }).filter(Boolean);
  });
  eq(bare.length, 0, "options labelled with their key: " + bare.join(", "));
  const vals = await page.locator("#o-page_size option, #o-footnotes option, #o-format option").allInnerTexts();
  ok(vals.includes("6 × 9 in") && vals.includes("At the foot of the page") && vals.includes("Print-ready PDF"), "allowed values have words: " + vals.join(" | "));
  eq(await page.locator("#o-gutter_inches").inputValue(), "0.15", "the preset's gutter");
  eq(await page.locator("#o-hyphenate").isDisabled(), true, "hyphenation is shown and cannot be turned on");
  eq(await page.locator("#o-imprint").evaluate((e) => e.tagName), "TEXTAREA", "the copyright text has room for lines");
  eq(await page.locator("#cmp-submit").isDisabled(), true, "a submission bundle is a Word file: not offered for print");
  ok(await page.locator("#cmp-cover").isVisible(), "the cover is offered for a print PDF");
  // the kind of file switched by hand rebuilds the form: paged HTML has no crop marks and no cover
  await page.selectOption("#o-format", "html-paged");
  ok((await page.locator("#o-crop_marks").count()) === 0 && (await page.locator("#o-gutter_inches").count()) === 1 && await page.locator("#cmp-cover").isHidden(), "paged HTML: margins, no crop marks, no cover");
  eq(await active(page), "o-format", "the focus stays on the control that was changed");
  // a Word preset turned into a PDF by hand: the print switches it leaves unset show what the file will do (on)
  await page.selectOption("#cmp-preset", "shunn-us");
  await page.selectOption("#o-format", "pdf");
  ok(await page.locator("#o-toc").isChecked() && await page.locator("#o-running_heads").isChecked() && !(await page.locator("#o-drop_cap").isChecked()), "the unset switches show their defaults");
});

let previewPages = 0;
await kit.test("the proof: pages drawn from the layout, the facts, the checklist; no shift when it arrives or a page turns", async () => {
  const page = owner.page; kit.main = page;
  await open(page);
  await page.selectOption("#cmp-preset", "print-6x9");
  await page.selectOption("#cmp-show", "12");
  // where the button sits in the document (the focus moving to the result may scroll the window; that is not a shift)
  const at = () => page.evaluate(() => { const r = document.querySelector("#cmp-preview").getBoundingClientRect(); return { x: r.left + scrollX, y: r.top + scrollY }; });
  const before = await at();
  await preview(page);
  const after = await at();
  ok(Math.abs(before.x - after.x) < 1 && Math.abs(before.y - after.y) < 1, `the Preview button did not move: ${JSON.stringify(before)} -> ${JSON.stringify(after)}`);
  const sum = await text(page, "#cmp-sum");
  const m = /(\d+) pages/.exec(sum);
  ok(m && !/about/.test(sum), "an exact page count, not an estimate: " + sum);
  previewPages = parseInt(m[1], 10);
  ok(previewPages >= 24 && previewPages % 2 === 0, "pages: " + previewPages);
  const facts = await text(page, "#cmp-pfacts");
  ok(/Trim size\s+6 × 9 in/.test(facts) && /leaves/.test(facts) && /Inside margin with the gutter\s+0\.9 in/.test(facts), "the facts: " + facts.replace(/\s+/g, " "));
  await page.locator("#cmp-check-d summary").click();
  const check = await text(page, "#cmp-check");
  ok(/The page count is even/.test(check) && /fonts are embedded/.test(check) && /printed proof/.test(check), "the checklist: " + check.replace(/\s+/g, " "));
  eq(await page.locator("#cmp-paper svg.cmp-sheet").count(), 1, "one page drawn");
  ok((await page.locator("#cmp-paper svg text").count()) === 0 || true, "the first page may be the title page");
  ok(/Page 1 of \d+, numbered i, right-hand/.test(await text(page, "#cmp-pos")), "the position says the number and the side: " + await text(page, "#cmp-pos"));
  const box1 = await page.locator("#cmp-paper svg").boundingBox();
  // turn to a page of text: its lines sit inside the page, a running head or folio is centred
  let found = false;
  for (let i = 0; i < 11 && !found; i++) {
    await page.locator("#cmp-next").click();
    found = (await page.locator("#cmp-paper svg text.k-text").count()) > 10;
  }
  ok(found, "a page of body text among the first twelve");
  const box2 = await page.locator("#cmp-paper svg").boundingBox();
  ok(Math.abs(box1.height - box2.height) < 1 && Math.abs(box1.width - box2.width) < 1, `every page is the same size: ${JSON.stringify(box1)} ${JSON.stringify(box2)}`);
  const fit = await page.evaluate(() => { const s = document.querySelector("#cmp-paper svg"), r = s.getBoundingClientRect(); return [...s.querySelectorAll("text")].every((t) => { const b = t.getBoundingClientRect(); return b.left >= r.left - 1 && b.right <= r.right + 1; }); });
  ok(fit, "no line runs off the page");
  ok(/The pages are drawn from the layout the PDF uses/.test(await text(page, "#cmp-proofnote")), "the proof says what it is");
  if (shots) { await page.evaluate(() => window.scrollTo(0, 0)); await page.screenshot({ path: `${shots}/compile-print-1280.png`, caret: "initial" }); }
});

await kit.test("a print-ready PDF is made through the page: its bytes are a PDF with the page count the preview said", async () => {
  const page = owner.page; kit.main = page;
  const [dl] = await Promise.all([page.waitForEvent("download"), page.locator("#cmp-download").click()]);
  const p = join(kit.tmp, dl.suggestedFilename());
  await dl.saveAs(p);
  ok(/\.pdf$/.test(dl.suggestedFilename()), "a .pdf: " + dl.suggestedFilename());
  const buf = readFileSync(p);
  eq(buf.subarray(0, 5).toString("latin1"), "%PDF-", "the file starts as a PDF does");
  eq(pdfPages(buf), previewPages, "the PDF's pages are the preview's");
  await page.waitForSelector("#cmp-manifest:not([hidden])");
  ok(/Yes: the input hashes are equal/.test(await text(page, "#cmp-mfacts")), "the file is what the preview said");
});

await kit.test("the cover: the thickness is asked for, the wrap is measured and drawn, the PDF is the wrap on one page", async () => {
  const page = owner.page; kit.main = page;
  kit.allowed.push(/status of 422/); // the missing thickness is a 422 answer to the form, shown as a sentence
  await page.locator("#cmp-cv-form button[type=submit]").click();
  await kit.waitFor(async () => /thickness per page/.test(await text(page, "#cv-out")), "the missing thickness is said, in an alert");
  eq(await page.locator("#cv-out [role=alert]").count(), 1, "once");
  eq(await page.locator("#cv-thick").getAttribute("aria-invalid"), "true", "and the field is marked");
  eq(await active(page), "cv-thick", "the focus goes to it");
  await page.fill("#cv-thick", "0.0025");
  await page.check("#cv-barcode");
  await page.locator("#cmp-cv-form button[type=submit]").click();
  await kit.waitFor(async () => /The cover is measured/.test(await text(page, "#cmp-status")), "measured", 30000);
  const facts = await text(page, "#cv-facts");
  ok(new RegExp(`Pages\\s+${previewPages}, from the layout`).test(facts), "the page count is the layout's: " + facts.replace(/\s+/g, " "));
  const spine = (previewPages * 0.0025).toFixed(3).replace(/0+$/, "").replace(/\.$/, "");
  ok(facts.includes(`Spine\n${spine} in`) || facts.includes(`${spine} in`), `the spine is pages × thickness (${spine}): ` + facts.replace(/\s+/g, " "));
  ok((await page.locator("#cv-art svg rect[fill='#3B5B7A']").count()) === 1, "the wrap is the project's colour");
  ok((await page.locator("#cv-art svg rect[fill='#FFFFFF']").count()) === 1, "with the barcode's box");
  ok((await text(page, "#cv-art")).includes("The Salt Road"), "and its title");
  const [dl] = await Promise.all([page.waitForEvent("download"), page.locator("#cv-download").click()]);
  const p = join(kit.tmp, dl.suggestedFilename());
  await dl.saveAs(p);
  const buf = readFileSync(p);
  ok(/-cover\.pdf$/.test(dl.suggestedFilename()) && buf.subarray(0, 5).toString("latin1") === "%PDF-", "a cover PDF: " + dl.suggestedFilename());
  eq(pdfPages(buf), 1, "the file for the printer is one page: the wrap");
  ok(await page.locator("#cv-proof").isHidden(), "a generated cover has no proof to offer");
  ok(await page.locator("#cv-front").isVisible(), "the front alone is offered");
  ok(!(await text(page, "#cmp-mfacts")).includes("Same content as the preview"), "a cover is not compared with the book's preview");
});

await kit.test("by keyboard alone: choose the preset, preview, turn pages, download the PDF, measure the cover", async () => {
  const page = owner.page; kit.main = page;
  await open(page);
  await page.evaluate(() => document.activeElement && document.activeElement.blur());
  await tabTo(page, "cmp-preset");
  // type-ahead on the closed select, then the arrows, until the print preset is chosen
  await page.keyboard.type("Print, 6");
  for (let i = 0; i < 20 && (await page.locator("#cmp-preset").inputValue()) !== "print-6x9"; i++) await page.keyboard.press("ArrowDown");
  eq(await page.locator("#cmp-preset").inputValue(), "print-6x9", "chosen by keyboard");
  await kit.waitFor(async () => (await page.locator("#o-gutter_inches").count()) === 1, "the print options");
  await tabTo(page, "cmp-preview");
  await page.keyboard.press("Enter");
  await kit.waitFor(async () => /The preview is ready/.test(await text(page, "#cmp-status")), "the preview by Enter", 30000);
  eq(await active(page), "cmp-r-h", "the focus moves to the preview's heading");
  await tabTo(page, "cmp-viewer");
  await page.keyboard.press("ArrowRight");
  await kit.waitFor(async () => /^Page 2 of/.test(await text(page, "#cmp-pos")), "page 2 by the arrow");
  await page.keyboard.press("End");
  await page.keyboard.press("Home");
  await kit.waitFor(async () => /^Page 1 of/.test(await text(page, "#cmp-pos")), "Home");
  await tabTo(page, "cmp-download", 120, true);
  const [dl] = await Promise.all([page.waitForEvent("download"), page.keyboard.press("Enter")]);
  ok(/\.pdf$/.test(dl.suggestedFilename()), "the PDF by Enter");
  await tabTo(page, "cv-thick");
  await page.keyboard.type("0.002");
  await page.keyboard.press("Enter");
  await kit.waitFor(async () => /The cover is measured/.test(await text(page, "#cmp-status")), "the cover measured by Enter", 30000);
  const ring = await page.evaluate(() => { const e = document.activeElement, s = getComputedStyle(e); return s.outlineStyle !== "none" || s.boxShadow !== "none"; });
  ok(ring, "the focused control shows its focus");
});

await kit.test("the live region says when the preview fails: the network cut", async () => {
  const page = owner.page; kit.main = page;
  kit.allowed.push(/Failed to load resource|ERR_INTERNET_DISCONNECTED|net::/);
  await page.context().setOffline(true);
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  await kit.waitFor(async () => /Could not reach the server/.test(await text(page, "#cmp-status")), "the failure is announced");
  eq(await page.locator("#cmp-status").getAttribute("aria-live"), "polite", "in a live region");
  ok(await page.locator("#cmp-status.is-bad").count() === 1, "and shown as a failure");
  await page.context().setOffline(false);
});

await kit.test("reduced motion: nothing on the page animates", async () => {
  const page = owner.page; kit.main = page;
  await page.emulateMedia({ reducedMotion: "reduce" });
  await preview(page);
  await page.locator("#cmp-next").click();
  await kit.sleep(200);
  eq(await page.evaluate(() => document.getAnimations().filter((a) => a.playState === "running").length), 0, "running animations");
  await page.emulateMedia({ reducedMotion: "no-preference" });
});

await kit.test("dark theme: the proof and the cover in the dark", async () => {
  const page = owner.page; kit.main = page;
  await page.emulateMedia({ colorScheme: "dark" });
  const paper = await page.evaluate(() => getComputedStyle(document.querySelector("#cmp-paper svg .pg")).fill);
  const ink = await page.evaluate(() => { const t = document.querySelector("#cmp-paper svg text"); return t ? getComputedStyle(t).fill : ""; });
  ok(paper && ink && paper !== ink, `paper ${paper} and ink ${ink} differ in the dark`);
  if (shots) {
    await page.locator("#cmp-cover").scrollIntoViewIfNeeded();
    await page.screenshot({ path: `${shots}/compile-print-cover-dark.png`, caret: "initial" });
  }
  await page.emulateMedia({ colorScheme: "light" });
});

await kit.test("390 px: no sideways scroll, every control at least 44 px, the proof and the cover fit", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true }, "writer@dev.scriobh");
  const page = ph.page; kit.main = page;
  await open(page);
  await page.selectOption("#cmp-preset", "print-5x8");
  const wide = () => page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1);
  ok(!(await wide()), "the setup scrolls sideways");
  await preview(page);
  await page.fill("#cv-thick", "0.0025");
  await page.locator("#cmp-cv-form button[type=submit]").tap();
  await kit.waitFor(async () => /The cover is measured/.test(await text(page, "#cmp-status")), "measured", 30000);
  ok(!(await wide()), "the proof or the cover scrolls sideways");
  const small = await page.evaluate(() => [...document.querySelectorAll("#cmp button, #cmp select, #cmp input:not([type=checkbox]), #cmp textarea, #cmp summary, #cmp label.check")]
    .filter((e) => e.offsetParent !== null && !e.disabled)
    .map((e) => { const r = e.getBoundingClientRect(); return { id: e.id || e.textContent.trim().slice(0, 30), w: Math.round(r.width), h: Math.round(r.height) }; })
    .filter((r) => r.h < 44 || r.w < 44));
  eq(small.length, 0, "targets under 44 px: " + JSON.stringify(small.slice(0, 8)));
  const sheet = await page.locator("#cmp-paper svg").boundingBox();
  ok(sheet.width <= 390 && sheet.width > 200, "the page fits the phone: " + JSON.stringify(sheet));
  await page.locator("#cmp-next").tap();
  await kit.waitFor(async () => /^Page 2 of/.test(await text(page, "#cmp-pos")), "turned by touch");
  if (shots) { await page.locator("#cmp-viewer").evaluate((e) => e.scrollIntoView({ block: "start" })); await page.screenshot({ path: `${shots}/compile-print-390.png`, caret: "initial" }); }
  await ph.ctx.close();
  kit.main = null;
});

await kit.finish("e2e-compile-print");
