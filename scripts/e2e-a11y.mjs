#!/usr/bin/env node
// Accessibility audit in Chromium. Walks every signed-in and public page family (each project role, the writing view with
// the writing partner against scripts/fake-ai.mjs, the review, share, compile, site, webhooks, connect, import and admin
// pages, the sign-in, error and 404 pages) and checks, from the browser's accessibility tree and the DOM:
//
//   structure    one main landmark, labelled navigation, a working skip link, one h1, no skipped heading level, a unique
//                descriptive title, lang, no duplicate id, ARIA references that resolve, no positive tabindex, alt on images,
//                names on every control and field (a placeholder alone is not a name), radio groups named, a viewport that
//                allows zoom, links in running text that are not marked by colour alone
//   keyboard     Tab through the whole page and back (every control reached, no trap, focus never hidden behind a sticky bar),
//                a visible focus indicator on every stop with at least 3:1 against what is behind it, dialogs and panels:
//                focus goes in, Escape closes, focus comes back
//   size         text contrast (4.5:1, 3:1 for large text, both themes), targets at least 24x24 CSS px and primary controls
//                44x44 at 390 px on a touch device, no horizontal page scroll at 320 px (400% of 1280) and at 640 px (200%),
//                200% text, the WCAG text-spacing override with no newly clipped text
//   preference   prefers-reduced-motion: nothing animates or transitions; forced-colors: focus and state stay visible
//                by shape rather than by colour; live regions for save and status messages
//
// It finds elements by role and structure, never by class name where it can help it. Violations are listed by rule; the
// script exits 1 while any remain that the ALLOW list below does not excuse (each entry says why).
//
//   node scripts/e2e-a11y.mjs [--only substring] [--dump DIR] [--json FILE] [--report-only] [--quick] [--max N]
//     --only         run the pages whose id contains the text
//     --dump DIR     write each page's accessibility tree (ariaSnapshot), landmarks and heading outline to DIR
//     --json FILE    write every de-duplicated violation to FILE
//     --report-only  print the list and exit 0
//     --quick        the structure scan at 1280 light only (no keyboard walk, no media or zoom passes); for iterating on a fix
//     --max N        examples printed per rule (default 12)
import { createKit } from "./e2e-kit.mjs";
import { startFake } from "./fake-ai.mjs";
import { installFakePandoc } from "./fake-pandoc.mjs";
import { desktopEnv, launchToken, rm, startServer, tempDir } from "./harness.mjs";
import { seedDays } from "./seed-studio.mjs";
import { startFakeHost } from "./fakehost.mjs";
import { makeBigEpub, makeDocx, makeEpub, makeLongDocx, writePdf } from "./fixtures/books.mjs";
import { harbourPng, wrapPng } from "./covers-pictures.mjs";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { join } from "node:path";

const argv = process.argv.slice(2);
const opt = (n) => (argv.includes(n) ? argv[argv.indexOf(n) + 1] : "");
const ONLY = opt("--only"), DUMP = opt("--dump"), JSONOUT = opt("--json");
const REPORT_ONLY = argv.includes("--report-only"), QUICK = argv.includes("--quick");
const MAXEX = +opt("--max") || 12;
if (DUMP) mkdirSync(DUMP, { recursive: true });

// ---------------------------------------------------------------------------------------------- the allow list
// A violation is excused only by an entry here: { rule, sel (RegExp on the element description), page (RegExp on "page viewport tag",
// optional), why }. Keep it short; every line is a thing we could not fix, and docs/ACCESSIBILITY.md says so too.
const ALLOW = [
  { rule: "skip-link", sel: /^input#gate-cover-pass/, page: /^gate-cover /, why: "while a project is locked the cover is the whole page (everything behind it is hidden and inert): it holds one passphrase box, one link and one button, so there is nothing to skip" },
  { rule: "field-contrast", sel: /^textarea#doc-title/, why: "the title is a heading edited in place: it has no box on purpose, so that the page title is not a form field; it is found by its text and shows the focus indicator when edited" },
  { rule: "field-contrast", sel: /^input#(palette-input|jump-input)\[/, why: "the command palette's and the chapter jump's search rows are a bare input beside a magnifier, set apart by the dialog's own rule: the icon and the placeholder identify them and the focus ring is drawn on the input itself; a box inside the dialog's box would be decoration" },
  { rule: "target-44", sel: /^textarea#doc-title/, why: "the same in-place heading; its height is the heading's own line, and a taller box would push the chapter text down (the title is also reachable at 44 px through the chapter jump button beside it)" },
  { rule: "text-spacing", sel: /^(span#crumb-title|\.crumb-project > span) "/, page: /^(write|editor-write|reviewer-write|reader-write|write-note|write-project) /, why: "with the text-spacing override the header's breadcrumb ellipsises before the page tabs do; the same title is the page's h1 directly below and the control's accessible name, so no information is lost (WCAG 1.4.12 loss of content)" },
  { rule: "text-200", sel: /^(span#crumb-title|\.crumb-project > span|\.crumb-part > span|span\.crumb-here|button#jump-open) "/, why: "at 200% text size the header's breadcrumb ellipsises (the tabs scroll instead of pushing the page sideways); the name of the current page and of the project is also the page's h1 and the control's accessible name, so nothing is lost" },
];

// ---------------------------------------------------------------------------------------------- in-page helpers
// Installed into every document before its own scripts run. Nothing here touches the page's markup.
function helpers() {
  const A = (window.__a11y = {});
  let n = 0;
  const keys = new WeakMap();
  A.key = (el) => { if (!keys.has(el)) keys.set(el, ++n); return keys.get(el); };
  A.sel = (el) => {
    if (!el || el.nodeType !== 1) return String(el);
    let s = el.tagName.toLowerCase();
    if (el.id) s += "#" + el.id;
    else if (el.classList.length) s += "." + [...el.classList].slice(0, 2).join(".");
    else if (el.parentElement && el.parentElement.classList.length) s = "." + el.parentElement.classList[0] + " > " + s;
    const r = el.getAttribute("role"); if (r) s += `[role=${r}]`;
    const t = el.getAttribute("type"); if (t && s.startsWith("input")) s += `[type=${t}]`;
    const txt = (el.getAttribute("aria-label") || el.textContent || el.getAttribute("name") || el.getAttribute("href") || "").trim().replace(/\s+/g, " ").slice(0, 28);
    return txt ? `${s} "${txt}"` : s;
  };
  const cv = document.createElement("canvas"); cv.width = cv.height = 1;
  const cx = cv.getContext("2d", { willReadFrequently: true });
  const cache = new Map();
  A.rgba = (c) => {
    if (cache.has(c)) return cache.get(c);
    let out = [0, 0, 0, 0];
    try { cx.clearRect(0, 0, 1, 1); cx.fillStyle = "#000"; cx.fillStyle = c; cx.fillRect(0, 0, 1, 1); const d = cx.getImageData(0, 0, 1, 1).data; out = [d[0], d[1], d[2], d[3] / 255]; } catch { /* unparsable */ }
    cache.set(c, out); return out;
  };
  A.over = (f, b) => { const a = f[3]; return [f[0] * a + b[0] * (1 - a), f[1] * a + b[1] * (1 - a), f[2] * a + b[2] * (1 - a), 1]; };
  const lin = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
  A.lum = (c) => 0.2126 * lin(c[0]) + 0.7152 * lin(c[1]) + 0.0722 * lin(c[2]);
  A.ratio = (a, b) => { const x = A.lum(a), y = A.lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
  A.canvasColor = () => (matchMedia("(prefers-color-scheme: dark)").matches ? [18, 18, 18, 1] : [255, 255, 255, 1]);
  // The colour behind an element: its own and its ancestors' backgrounds composited down to an opaque one. null when an image is behind.
  A.backdrop = (el, fromParent) => {
    const stack = [];
    let e = fromParent ? el.parentElement : el;
    for (; e && e.nodeType === 1; e = e.parentElement) {
      const cs = getComputedStyle(e);
      if (cs.backgroundImage !== "none") return null;
      const c = A.rgba(cs.backgroundColor);
      if (c[3] > 0) { stack.push(c); if (c[3] >= 1) break; }
    }
    let base = A.canvasColor();
    if (stack.length && stack[stack.length - 1][3] >= 1) base = stack.pop();
    for (let i = stack.length - 1; i >= 0; i--) base = A.over(stack[i], base);
    return base;
  };
  A.srOnly = (el) => { const r = el.getBoundingClientRect(); return r.width <= 1 || r.height <= 1; };
  A.visible = (el) => { try { return el.checkVisibility({ checkVisibilityCSS: true }) && !el.closest("[inert]"); } catch { return false; } };
  A.shown = (el) => A.visible(el) && !A.srOnly(el);
  A.tabbable = (rootSel) => {
    const q = 'a[href], button, input:not([type=hidden]), select, textarea, summary, iframe, [contenteditable=""], [contenteditable="true"], [tabindex]';
    const out = [];
    const scope = rootSel ? document.querySelector(rootSel) : document;
    for (const el of scope ? scope.querySelectorAll(q) : []) {
      if (el.disabled || el.tabIndex < 0 || !A.visible(el)) continue;
      const d = el.closest("details:not([open])");
      if (d && !(el.tagName === "SUMMARY" && el.parentElement === d)) continue;
      out.push(el);
    }
    return out;
  };
  A.sig = (el) => {
    const cs = getComputedStyle(el);
    return { border: [cs.borderTopColor, cs.borderRightColor, cs.borderBottomColor, cs.borderLeftColor].join("|"), bg: cs.backgroundColor, td: cs.textDecorationLine, outline: `${cs.outlineStyle} ${cs.outlineWidth} ${cs.outlineColor} ${cs.outlineOffset}`, shadow: cs.boxShadow };
  };
  A.toTop = () => { if (document.activeElement && document.activeElement !== document.body) document.activeElement.blur(); window.scrollTo(0, 0); };
  A.before = {};
  A.snapshot = () => { A.before = {}; for (const el of A.tabbable()) A.before[A.key(el)] = A.sig(el); };
  // What a person sees when this element has focus (call right after Tab).
  A.focusInfo = (rootSel) => {
    const el = document.activeElement;
    if (!el || el === document.body || el === document.documentElement) return { gone: true };
    const cs = getComputedStyle(el), r = el.getBoundingClientRect();
    const k = A.key(el);
    const info = { key: k, sel: A.sel(el), fv: el.matches(":focus-visible"), skip: el.classList.contains("skip"), inRoot: rootSel ? !!(document.querySelector(rootSel) && document.querySelector(rootSel).contains(el)) : true };
    const ow = parseFloat(cs.outlineWidth) || 0;
    info.outline = cs.outlineStyle !== "none" && ow > 0 ? { w: ow, off: parseFloat(cs.outlineOffset) || 0, c: A.rgba(cs.outlineColor) } : null;
    info.shadow = null;
    if (cs.boxShadow && cs.boxShadow !== "none") {
      const m = cs.boxShadow.match(/(rgba?\([^)]*\)|color\([^)]*\)|#[0-9a-f]+)\s+(-?[\d.]+)px\s+(-?[\d.]+)px\s+([\d.]+)px(?:\s+(-?[\d.]+)px)?(\s+inset)?/i);
      if (m && !m[6]) info.shadow = { c: A.rgba(m[1]), spread: parseFloat(m[5] || "0"), blur: parseFloat(m[4]) };
    }
    info.opaqueBg = A.backdrop(el, info.outline ? info.outline.off > 0 : true) || A.backdrop(el, true);
    info.parentBg = A.backdrop(el, true);
    const b = A.before[k], s = A.sig(el);
    info.changed = b ? { outline: b.outline !== s.outline, shadow: b.shadow !== s.shadow, border: b.border !== s.border, bg: b.bg !== s.bg, bColor: [b.border.split("|")[0], s.border.split("|")[0]], bgColors: [b.bg, s.bg] } : null;
    const cxp = Math.min(Math.max(r.x + Math.min(r.width / 2, 40), 0), innerWidth - 1), cyp = Math.min(Math.max(r.y + r.height / 2, 0), innerHeight - 1);
    const top = document.elementFromPoint(cxp, cyp);
    const lab = (a, z) => !!(a.closest("label") && a.closest("label").contains(z));
    info.obscured = !!top && top !== el && !el.contains(top) && !top.contains(el) && !lab(top, el) && !lab(el, top);
    info.obscuredBy = info.obscured ? A.sel(top) : "";
    info.offscreen = r.width > 0 && (r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth);
    info.editor = !!el.closest(".scriobh-prose, [contenteditable=true]");
    return info;
  };
  // Elements whose text is cut off by overflow, keyed by a description.
  A.clipped = () => {
    const out = {};
    for (const el of document.body.querySelectorAll("*")) {
      if (!el.childNodes.length || !A.visible(el) || A.srOnly(el)) continue;
      const cs = getComputedStyle(el);
      const cl = (v) => v === "hidden" || v === "clip";
      if (!cl(cs.overflowX) && !cl(cs.overflowY)) continue;
      if (!/\S/.test(el.textContent)) continue;
      const hy = cl(cs.overflowY) && el.scrollHeight > el.clientHeight + 1, hx = cl(cs.overflowX) && el.scrollWidth > el.clientWidth + 1;
      if (hx || hy) out[A.sel(el) + (hx ? " (x)" : "") + (hy ? " (y)" : "")] = [el.scrollWidth, el.clientWidth, el.scrollHeight, el.clientHeight].join("/");
    }
    return out;
  };
}

// ---------------------------------------------------------------------------------------------- the structure scan
// Everything that needs only the DOM and computed style. Returns [{rule, sel, msg}].
function scan(o) {
  const A = window.__a11y, V = [];
  const add = (rule, el, msg) => V.push({ rule, sel: el && el.nodeType === 1 ? A.sel(el) : String(el || ""), msg });
  const doc = document, shown = (e) => A.visible(e);

  // ---- document
  if (!doc.documentElement.getAttribute("lang")) add("lang", doc.documentElement, "no lang attribute");
  const title = (doc.title || "").trim();
  if (!title) add("title", null, "empty title");
  else if (/^Scr[ií]obh$/.test(title) && !o.titleOk) add("title", null, `the title is only "${title}"`);
  const vp = doc.querySelector('meta[name="viewport"]');
  if (!vp) add("zoom", null, "no viewport meta");
  else if (/user-scalable\s*=\s*(no|0)|maximum-scale\s*=\s*(0?\.\d|1(\.0*)?)(?![\d])/i.test(vp.content)) add("zoom", vp, "the viewport blocks zoom: " + vp.content);

  // ---- landmarks and headings
  const mains = [...doc.querySelectorAll('main, [role="main"]')].filter(shown);
  if (mains.length !== 1) add("main", mains[1] || null, `${mains.length} main landmarks, want 1`);
  const lab = (e) => (e.getAttribute("aria-label") || "").trim() || ((id) => { const t = id && doc.getElementById(id.split(/\s+/)[0]); return t ? t.textContent.trim() : ""; })(e.getAttribute("aria-labelledby"));
  const navs = [...doc.querySelectorAll('nav, [role="navigation"]')].filter(shown);
  for (const n of navs) if (!lab(n)) add("nav-label", n, "navigation landmark without a name");
  const navNames = navs.map(lab).filter(Boolean);
  navNames.forEach((x, i) => { if (navNames.indexOf(x) !== i) add("nav-label", navs[i], `two navigation landmarks are both named "${x}"`); });
  for (const a of doc.querySelectorAll('aside, [role="complementary"], section[aria-label], section[aria-labelledby], form[aria-label], form[aria-labelledby], [role="region"], [role="search"]')) if (shown(a) && !lab(a)) add("landmark-label", a, "landmark without a name");
  if ([...doc.querySelectorAll('body > header, [role="banner"]')].filter(shown).length > 1) add("landmark", null, "more than one banner");
  const h = [...doc.querySelectorAll("h1,h2,h3,h4,h5,h6,[role=heading]")].filter((e) => shown(e) && !e.closest(".scriobh-prose, .doc-plain, .md-preview, .r-prose, .prose"));
  const level = (e) => (e.getAttribute("aria-level") ? +e.getAttribute("aria-level") : +e.tagName[1] || 2);
  const h1 = h.filter((e) => level(e) === 1);
  if (h1.length !== 1) add("h1", h1[1] || null, `${h1.length} h1 elements, want 1`);
  let prev = 0;
  for (const e of h) {
    const lv = level(e);
    if (!(e.textContent || "").trim() && !e.querySelector("img[alt]")) add("heading-empty", e, "empty heading");
    if (prev && lv > prev + 1) add("heading-order", e, `h${lv} follows h${prev}`);
    if (!prev && lv > 1) add("heading-order", e, `the first heading is h${lv}`);
    prev = lv;
  }

  // ---- ids and references
  const seen = {};
  for (const e of doc.querySelectorAll("[id]")) (seen[e.id] = seen[e.id] || []).push(e);
  for (const [id, els] of Object.entries(seen)) if (els.length > 1) add("dup-id", els[1], `id "${id}" used ${els.length} times`);
  const refs = ["aria-labelledby", "aria-describedby", "aria-controls", "aria-owns", "aria-activedescendant", "aria-errormessage", "aria-details", "aria-flowto"];
  for (const e of doc.querySelectorAll(refs.map((r) => `[${r}]`).join(","))) {
    for (const r of refs) {
      const v = e.getAttribute(r); if (!v) continue;
      for (const id of v.split(/\s+/)) if (id && !doc.getElementById(id)) add("aria-ref", e, `${r}="${id}" matches nothing`);
    }
  }
  for (const l of doc.querySelectorAll("label[for]")) if (!doc.getElementById(l.htmlFor)) add("aria-ref", l, `label for="${l.htmlFor}" matches nothing`);
  for (const e of doc.querySelectorAll("[tabindex]")) if (+e.getAttribute("tabindex") > 0) add("tabindex", e, `tabindex=${e.getAttribute("tabindex")}`);
  for (const e of doc.querySelectorAll('[aria-hidden="true"]')) {
    const f = e.matches("a[href],button,input,select,textarea,[tabindex]") ? e : e.querySelector("a[href],button:not([tabindex='-1']),input:not([type=hidden]),select,textarea,[tabindex]:not([tabindex='-1'])");
    if (f && f.tabIndex >= 0 && !f.disabled && A.visible(f)) add("aria-hidden-focusable", f, "focusable inside aria-hidden");
  }

  // ---- images, svg, frames, tables, radios
  for (const i of doc.querySelectorAll("img")) if (!i.hasAttribute("alt")) add("img-alt", i, "img without alt");
  for (const s of doc.querySelectorAll("svg")) {
    if (s.parentElement.closest("svg") || s.getAttribute("aria-hidden") === "true" || !shown(s)) continue;
    // named by itself, a <title>, or the labelled figure or group it is drawn in (the proof's pages are labelled "Page 3 of 90" and keep their text readable)
    if (!(lab(s) || s.querySelector("title") || (s.parentElement && s.parentElement.closest("[aria-label], [aria-labelledby]")))) add("svg-name", s, "svg neither hidden nor named");
  }
  for (const f of doc.querySelectorAll("iframe")) if (shown(f) && !lab(f) && !f.title) add("frame-title", f, "iframe without a title");
  for (const t of doc.querySelectorAll("table")) if (shown(t) && t.getAttribute("role") !== "presentation" && !t.querySelector("th") && t.rows.length > 1) add("table-header", t, "data table without header cells");
  const groups = {};
  const gk = (r) => r.name + "|" + (r.form ? [...doc.forms].indexOf(r.form) : "");
  for (const r of doc.querySelectorAll('input[type="radio"]')) (groups[gk(r)] = groups[gk(r)] || []).push(r);
  for (const [g, rs] of Object.entries(groups)) {
    if (rs.length < 2 || !rs.some(shown) && !rs.some((r) => r.closest("label") && shown(r.closest("label")))) continue;
    const fs = rs[0].closest("fieldset, [role=radiogroup], [role=group]");
    if (!fs || !(fs.tagName === "FIELDSET" ? (fs.querySelector(":scope > legend") || lab(fs)) : lab(fs))) add("radio-group", rs[0], `radio group "${g.split("|")[0]}" without a group name`);
  }

  // ---- required parents for composite roles
  const need = { option: ["listbox", "combobox"], tab: ["tablist"], treeitem: ["tree", "group"], menuitem: ["menu", "menubar", "group"], menuitemcheckbox: ["menu", "menubar"], menuitemradio: ["menu", "menubar"], row: ["table", "grid", "treegrid", "rowgroup"], gridcell: ["row"] };
  for (const e of doc.querySelectorAll("[role]")) {
    const r = e.getAttribute("role").split(/\s+/)[0];
    if (!need[r] || !shown(e)) continue;
    if (!e.parentElement.closest(need[r].map((x) => `[role="${x}"]`).join(","))) add("aria-parent", e, `role=${r} outside ${need[r].join("/")}`);
  }
  for (const e of doc.querySelectorAll('[role="tab"]')) if (!e.hasAttribute("aria-selected")) add("aria-state", e, "tab without aria-selected");

  // ---- links in running text are not marked by colour alone
  for (const a of doc.querySelectorAll("p a[href], li a[href], dd a[href], td a[href], .fine a[href], .sub a[href]")) {
    if (!shown(a)) continue;
    const par = a.parentElement, own = (par.textContent || "").replace(a.textContent, "").trim();
    if (own.length < 8 || getComputedStyle(a).display !== "inline") continue; // the link is the whole item, not a link in a sentence
    const cs = getComputedStyle(a), pcs = getComputedStyle(par);
    const ul = cs.textDecorationLine.includes("underline") || (parseFloat(cs.borderBottomWidth) > 0 && cs.borderBottomStyle !== "none");
    if (!ul && !(+cs.fontWeight >= +pcs.fontWeight + 200)) add("link-colour-only", a, "link in running text with no underline");
  }

  // ---- text contrast
  const done = new Set();
  const w = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT);
  for (let t; (t = w.nextNode());) {
    if (!/\S/.test(t.nodeValue)) continue;
    const e = t.parentElement;
    if (!e || done.has(e) || e.closest("svg, [disabled], [aria-disabled=true], script, style, noscript, canvas") || !shown(e)) continue;
    done.add(e);
    const cs = getComputedStyle(e);
    const rr = e.getBoundingClientRect(); if (rr.width < 2 || rr.height < 2) continue;
    const bg = A.backdrop(e, false);
    if (!bg) continue; // an image is behind it; judged by eye
    let fg = A.rgba(cs.color), op = 1;
    for (let p = e; p && p.nodeType === 1; p = p.parentElement) { const x = parseFloat(getComputedStyle(p).opacity); if (!isNaN(x)) op *= x; }
    fg = [fg[0], fg[1], fg[2], fg[3] * op];
    const f = A.over(fg, bg), px = parseFloat(cs.fontSize), bold = +cs.fontWeight >= 700;
    const want = px >= 24 || (px >= 18.66 && bold) ? 3 : 4.5, r = A.ratio(f, bg);
    if (r < want - 0.005) add("contrast", e, `${r.toFixed(2)}:1, want ${want}:1 (${cs.color} on rgb(${bg.slice(0, 3).map(Math.round)}), ${px}px)`);
  }
  for (const e of doc.querySelectorAll("input[placeholder], textarea[placeholder]")) {
    if (!shown(e)) continue;
    const ph = A.rgba(getComputedStyle(e, "::placeholder").color), bg = A.backdrop(e, false);
    if (!bg) continue;
    const r = A.ratio(A.over(ph, bg), bg);
    if (r < 4.5) add("contrast", e, `placeholder ${r.toFixed(2)}:1, want 4.5:1`);
  }
  // the edge or fill of a field is 3:1 against what is behind it (WCAG 1.4.11)
  for (const e of doc.querySelectorAll("input:not([type=hidden]):not([type=checkbox]):not([type=radio]):not([type=submit]):not([type=button]):not([type=file]), select, textarea")) {
    if (!A.shown(e)) continue;
    const cs = getComputedStyle(e), bg = A.backdrop(e.parentElement, false);
    if (!bg) continue;
    const own = A.rgba(cs.backgroundColor);
    const edge = parseFloat(cs.borderBottomWidth) > 0 && cs.borderBottomStyle !== "none" ? A.over(A.rgba(cs.borderBottomColor), bg) : null;
    const fill = own[3] > 0 ? A.over(own, bg) : null;
    const best = Math.max(edge ? A.ratio(edge, bg) : 1, fill ? A.ratio(fill, bg) : 1);
    if (best < 3) add("field-contrast", e, `edge or fill is ${best.toFixed(2)}:1 against its surroundings, want 3:1`);
  }

  // ---- target size
  const q = "a[href], button, input:not([type=hidden]), select, textarea, summary, [role=button], [role=tab], [role=menuitem], [role=switch], [role=checkbox], [role=radio], [role=link], [role=treeitem], [role=option]";
  const T = [];
  const textual = "p, li, dd, td, .fine, .sub, .lede, label";
  for (const e of doc.querySelectorAll(q)) {
    if (!A.shown(e) || e.disabled || e.closest(".scriobh-prose, [contenteditable=true]")) continue;
    if (e.matches("a[href]") && e.closest(textual)) {
      const par = e.closest(textual), own = (par.textContent || "").replace(e.textContent, "").trim();
      if (own.length >= 8 && getComputedStyle(e).display === "inline") continue; // an inline link in a sentence
    }
    let r = e.getBoundingClientRect();
    if (e.matches("input[type=checkbox], input[type=radio]")) { const l = e.closest("label") || (e.id && doc.querySelector(`label[for="${CSS.escape(e.id)}"]`)); if (l) { const lr = l.getBoundingClientRect(); if (lr.width * lr.height > r.width * r.height) r = lr; } }
    if (e.matches("input[type=file]") && getComputedStyle(e).opacity === "0") continue;
    T.push({ e, r });
  }
  const primary = "button, a.btn, a.tab, .menu-item, .icon-btn, [role=tab], [role=switch], input[type=checkbox], input[type=radio], select, summary, input[type=text], input[type=search], input[type=email], input[type=url], input[type=number], input[type=password], input[type=date], input[type=time], textarea";
  for (const { e, r } of T) {
    const wd = Math.round(r.width * 10) / 10, ht = Math.round(r.height * 10) / 10;
    if (wd < 24 || ht < 24) {
      // WCAG 2.5.8 spacing exception: no other target within a 24 px square centred on this one
      const c = { x: r.x + r.width / 2, y: r.y + r.height / 2 };
      const near = T.find((x) => x.e !== e && !e.contains(x.e) && !x.e.contains(e) && x.r.left < c.x + 12 && x.r.right > c.x - 12 && x.r.top < c.y + 12 && x.r.bottom > c.y - 12);
      if (near) add("target-24", e, `${wd}x${ht} px, under 24x24, with another target close (${near.e.tagName.toLowerCase()}${near.e.id ? "#" + near.e.id : ""}${near.e.className && typeof near.e.className === "string" ? "." + near.e.className.split(" ")[0] : ""} "${(near.e.textContent || near.e.getAttribute("aria-label") || "").trim().slice(0, 24)}" in ${near.e.parentElement ? near.e.parentElement.className || near.e.parentElement.id || near.e.parentElement.tagName : ""} at ${Math.round(near.r.left)},${Math.round(near.r.top)} ${Math.round(near.r.width)}x${Math.round(near.r.height)}; this one at ${Math.round(r.left)},${Math.round(r.top)})`);
    }
    if (o.touch && e.matches(primary) && (wd < 43.5 || ht < 43.5)) add("target-44", e, `${wd}x${ht} px on a touch screen, want 44x44`);
  }
  return V;
}

// ---------------------------------------------------------------------------------------------- the run
const gitHost = await startFakeHost({ dir: tempDir() }); // the code host the publish pages talk to
const kit = await createKit("e2e-a11y", { env: { SCRIOBH_GIT_ALLOW_PRIVATE: "1", SCRIOBH_DEV_TRUST_PEM: gitHost.pemPath, SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", SCRIOBH_AI_RATE_PER_MIN: "1000", SCRIOBH_WEBHOOK_ALLOW_PRIVATE: "1", SCRIOBH_SHARE_RATE_LIMIT: "0", SCRIOBH_SHARE_LINK_RATE: "0" } });
kit.deadline(40 * 60 * 1000);
process.on("unhandledRejection", (e) => { console.error("e2e-a11y: unhandled", e.message.split("\n")[0]); console.error((kit.srv.log || "").split("\n").slice(-6).join("\n")); try { kit.browser.process()?.kill("SIGKILL"); } catch { /* gone */ } try { kit.srv.child.kill("SIGKILL"); } catch { /* gone */ } process.exit(2); });
const { api } = kit;
const base = () => kit.base();
const fake = await startFake();
const VIOL = new Map(); // key -> {rule, sel, msg, pages:Set}
const NOTES = []; // things worth saying
const HARD = []; // the audit itself could not do part of its job: fails the run (a check that silently stops is worse than none)
const hard = (m) => { NOTES.push(m); HARD.push(m); };
let cur = { page: "", vp: "" };
const record = (rule, sel, msg, tag = "") => {
  const k = `${rule}|${sel}|${msg}`;
  if (!VIOL.has(k)) VIOL.set(k, { rule, sel, msg, pages: new Set() });
  VIOL.get(k).pages.add(`${cur.page} ${tag || cur.vp}`);
};
const addAll = (list, tag) => { for (const v of list) record(v.rule, v.sel, v.msg, tag); };

// ---- who is who
const people = { owner: "writer@dev.scriobh", editor: "admin@dev.scriobh", reviewer: "reader@dev.scriobh" };
const ctxs = {}; // `${who}/${kind}` -> {ctx, page}
async function pageFor(who, kind = "desk") {
  const k = `${who}/${kind}`;
  if (ctxs[k]) return ctxs[k];
  const opts = kind === "touch" ? { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 } : { viewport: { width: 1280, height: 800 } };
  let ctx, page;
  if (who === "anon") { ctx = await kit.newContext(opts); page = await ctx.newPage(); kit.watch(page, k); }
  else if (who === "desktop-first" || who === "desktop") {
    // the shell's window on the desktop-mode server: before a profile exists, and signed in as the local profile
    ctx = await kit.newContext({ ...opts, storageState: who === "desktop" ? await desktopSigned() : (await desktopWorld()).first });
    page = await ctx.newPage(); kit.watch(page, k);
  }
  else if (kind === "touch") {
    // the same session in a touch browser: the server limits sign-ins, and the audit is about pages, not about the sign-in
    ctx = await kit.newContext({ ...opts, storageState: await (await pageFor(who, "desk")).ctx.storageState() });
    page = await ctx.newPage(); kit.watch(page, k);
  } else ({ ctx, page } = await kit.signedIn(opts, people[who]));
  await ctx.addInitScript(helpers);
  await page.evaluate(helpers).catch(() => {});
  ctxs[k] = { ctx, page };
  return ctxs[k];
}

// ---- desktop mode (SCRIOBH_DESKTOP=1): a second server, started the way the desktop shell starts its sidecar (a launch
// token, the embedded issuer, no development issuer). The first-run profile form is audited before a profile exists; the
// pages about this computer as the local profile, who is the administrator.
let DW = null;
async function desktopWorld() {
  if (DW) return DW;
  const token = launchToken();
  const srv = await startServer(kit.bin, join(kit.tmp, "desktop-data"), { ...desktopEnv(token), SCRIOBH_SECRET_KEY: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" });
  kit.origins = [...(kit.origins || []), new URL(srv.base).origin];
  const ctx = await kit.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  await page.goto(`${srv.base}/desktop/start?t=${token}`, { waitUntil: "load" });
  if (new URL(page.url()).pathname !== "/desktop/profile") hard(`desktop: the first run ended at ${page.url()}, not at the profile form`);
  DW = { srv, base: srv.base, ctx, page, first: await ctx.storageState(), signed: null };
  return DW;
}
async function desktopSigned() {
  const d = await desktopWorld();
  if (!d.signed) {
    await d.page.goto(d.base + "/desktop/profile", { waitUntil: "load" });
    await d.page.fill("#dp-name", "Wren Writer");
    await d.page.click('button[type="submit"]');
    await d.page.locator("body.signed-in").waitFor({ timeout: 15000 }).catch(() => hard("desktop: the local profile was not signed in after the form"));
    d.signed = await d.ctx.storageState();
  }
  return d.signed;
}
const deskBase = () => DW.base;

// ---- seed: a book with every role, a Compendium, research, a comment, alternatives, share links, a webhook, an AI provider
const S = {};
async function seed() {
  const wren = (await pageFor("owner")).ctx, ada = (await pageFor("editor")).ctx;
  await pageFor("reviewer");
  const mk = async (path, data) => { const r = await api(wren, "POST", path, data); if (r.status >= 300) throw new Error(`seed ${path}: ${r.status} ${JSON.stringify(r.json)}`); return r.json; };
  const book = (await mk("/api/v1/projects", { title: "The Salt Road", subtitle: "A novel", kind: "novel", author_name: "Wren Writer" })).item;
  S.slug = book.slug; S.pid = book.id;
  const para = (c, s) => `Sentence ${c}.${s} says the *tide* came in over the stones, and nobody on the quay counted the boats twice.`;
  const body = (c) => Array.from({ length: 4 }, (_, p) => Array.from({ length: 3 }, (_, s) => para(c, p * 3 + s + 1)).join(" ")).join("\n\n") + "\n";
  const first = (await api(wren, "GET", `/api/v1/projects/${book.id}/docs`)).json.order[0];
  const g = (await api(wren, "GET", `/api/v1/docs/${first}`)).json.item;
  await api(wren, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, title: "Chapter 1", content: "# The harbour\n\n" + body(1) });
  S.doc = first;
  for (let c = 2; c <= 3; c++) await mk(`/api/v1/projects/${book.id}/docs`, { title: `Chapter ${c}`, kind: "chapter", content: body(c) });
  S.note = (await mk(`/api/v1/projects/${book.id}/docs`, { title: "Plot notes", kind: "note", content: "The twist.\n" })).item.id;
  const cur1 = (await api(wren, "GET", `/api/v1/docs/${first}`)).json.item;
  const alt = await api(wren, "POST", `/api/v1/docs/${first}/alternatives`, { base_revision: cur1.revision, scope: "word", quote: "1.5", variants: ["shingle", "rocks"] });
  if (alt.status >= 300) hard("seed alternatives: " + alt.status + " " + JSON.stringify(alt.json).slice(0, 160));
  await mk(`/api/v1/projects/${book.id}/members`, { email: people.editor, role: "editor" });
  await mk(`/api/v1/projects/${book.id}/members`, { email: people.reviewer, role: "reviewer" });
  // mail on (nothing connects until something is sent) and a Kindle address, so the Send to Kindle pages have their real states
  const mc = await api(ada, "PUT", "/api/v1/admin/mail", { host: "127.0.0.1", port: 2525, security: "none", allow_private: true, from_address: "scriobh@example.org", enabled: true });
  if (mc.status >= 300) hard("seed mail: " + mc.status + " " + JSON.stringify(mc.json).slice(0, 160));
  await api(wren, "PUT", "/api/v1/me/kindle", { address: "wren_k@kindle.com" });
  for (const [q, b] of [["Sentence 1.2 says the", "Is the tide the right word here?"], ["Sentence 1.7 says the", "This one repeats the last."], ["Sentence 1.9 says the", "Cut this?"]]) {
    const cm = await api((await pageFor("reviewer")).ctx, "POST", `/api/v1/projects/${book.id}/comments`, { document_id: first, quote: q, plain: true, body: b });
    if (cm.status >= 300) hard("seed comment: " + cm.status + " " + JSON.stringify(cm.json).slice(0, 160));
  }
  const comp = (await mk("/api/v1/compendiums", { name: "Salt Bible", project_id: book.id })).item;
  S.cslug = comp.slug;
  S.ents = [];
  for (const [kind, name, summary] of [["character", "Mara Voss", "A harbour pilot."], ["place", "The quay", "Where the boats come in."], ["character", "Tom Hale", "The ferryman."]]) S.ents.push((await mk(`/api/v1/compendiums/${comp.id}/entities`, { kind, name, summary })).item.id);
  S.events = [];
  for (const [t, d] of [["The tide turns", "1901-03"], ["The ferry sinks", "1903-11"]]) S.events.push(((await api(wren, "POST", `/api/v1/compendiums/${comp.id}/timeline/events`, { title: t, story_date: d, description: "At the quay." })).json.item || {}).id);
  S.users = ((await api(ada, "GET", "/api/v1/admin/users")).json.items || []);
  S.rnote = (await mk(`/api/v1/projects/${book.id}/research`, { kind: "note", title: "Tide tables", body: "High water at six.\n" })).item.id;
  await mk(`/api/v1/projects/${book.id}/research`, { kind: "note", title: "Ferry timetable", body: "Every hour.\n" });
  S.crnote = (await mk(`/api/v1/compendiums/${comp.id}/research`, { kind: "note", title: "Harbour map", body: "North quay.\n" })).item.id;
  await mk("/api/v1/compendiums", { name: "Another world" });
  const pv = await api(ada, "POST", "/api/v1/ai/providers", { kind: "openai_compatible", name: "Fake", base_url: fake.base, local: true, default_model: "fake-model", scope: "instance" });
  if (pv.status >= 300) hard("seed provider: " + pv.status);
  else await api(wren, "PUT", `/api/v1/projects/${book.id}/ai/policy`, { mode: "ask", providers: [pv.json.item.id], actions: ["selection", "alternatives", "chat", "critique", "summaries", "continuity", "extraction", "lab"] });
  // the Pandoc bridge on, against the stand-in: the Compile and import pages then offer its formats
  const pd = installFakePandoc(join(kit.tmp, "pandoc-a11y"));
  const pdon = await api(ada, "PUT", "/api/v1/admin/pandoc", { enabled: true, path: pd.bin });
  if (pdon.status >= 300) hard("seed pandoc: " + pdon.status + " " + JSON.stringify(pdon.json).slice(0, 160));
  S.pandoc = pd.bin;
  // covers: the book has a page-mode front and an image back (the cover page, the tree row, the library tile); a second book has a wrap
  {
    const up = async (pid, part, png, q = "") => {
      const r = await wren.request.fetch(`${kit.base()}/api/v1/projects/${pid}/cover-layout/images?part=${part}${q}`, { method: "POST", data: png, headers: { "Content-Type": "image/png" } });
      if (r.status() >= 300) hard(`seed cover picture ${part}: ${r.status()} ${(await r.text()).slice(0, 160)}`);
      return (await r.json()).attachment_id;
    };
    const front = await up(book.id, "front", harbourPng(900, 1350)), back = await up(book.id, "back", harbourPng(900, 1350));
    const set = await api(wren, "PUT", `/api/v1/projects/${book.id}/cover-layout`, { revision: 0, shape: "sides", trim: "6x9", alt: "A boat at a quay at dusk",
      front: { mode: "page", attachment: front, fit: "contain", scale: 80, position: "top", background: "#3B5B7A", margin_in: 0.5 }, back: { mode: "image", attachment: back } });
    if (set.status >= 300) hard("seed cover layout: " + set.status + " " + JSON.stringify(set.json).slice(0, 160));
    const wb = (await mk("/api/v1/projects", { title: "Wrap Book", kind: "novel" })).item;
    S.wslug = wb.slug;
    const w = wrapPng();
    const wa = await up(wb.id, "wrap", w.buffer, "&trim=6x9&bleed_in=0.125");
    const ws = await api(wren, "PUT", `/api/v1/projects/${wb.id}/cover-layout`, { revision: 0, shape: "wrap", trim: "6x9", alt: "A wrap in dusk colours", wrap: { attachment: wa, bleed_in: 0.125 } });
    if (ws.status >= 300) hard("seed wrap layout: " + ws.status + " " + JSON.stringify(ws.json).slice(0, 160));
  }
  S.share = (await mk(`/api/v1/projects/${book.id}/shares`, { label: "Beta reader Sam", scope: "comment" })).path;
  S.shareLocked = (await mk(`/api/v1/projects/${book.id}/shares`, { label: "Locked", scope: "read", passphrase: "correct horse battery" })).path;
  // the Writing Studio: a few weeks of days (written into the database by a Go helper: no op writes into the past), goals of two
  // kinds, and a progress card
  {
    const meId = (await api(wren, "GET", "/api/v1/me")).json.item.id;
    seedDays(kit.dataDir, meId, book.id, [1, 2, 3, 5, 6, 8, 9, 12, 15, 16, 20, 23, 30, 31, 44, 60, 61, 90].map((ago, i) => ({ ago, words: 90 + ((i * 137) % 900), hour: [7, 13, 21][i % 3], target: 300, hit: 90 + ((i * 137) % 900) >= 300 })));
  }
  await mk("/api/v1/me/studio/goals", { period: "day", measure: "words", target: 300 });
  S.sgoal = (await mk("/api/v1/me/studio/goals", { period: "total", measure: "words", target: 80000, project_id: book.id, deadline: new Date(Date.now() + 200 * 864e5).toISOString().slice(0, 10) })).item.id;
  S.card = (await mk("/api/v1/me/studio/cards", { label: "For Sam", expires_days: 30 })).item.path;
  const wh = await api(wren, "POST", `/api/v1/projects/${book.id}/webhooks`, { name: "Receiver", url: "http://127.0.0.1:9/hook", events: ["document.updated", "comment.created"] });
  if (wh.status >= 300) hard("seed webhook: " + wh.status + " " + JSON.stringify(wh.json).slice(0, 120));
  // the publish pages: a connected account at the fake code host, and a remote made with its token
  gitHost.addRepo("mairead", "backup", { private: true });
  gitHost.addRepo("mairead", "empty-one", { private: true });
  const acct = await mk("/api/v1/git/accounts", { provider: "forgejo", host: gitHost.host, token: gitHost.token, label: "Test host" });
  S.account = acct.id || (acct.item || {}).id;
  const rem = await api(wren, "POST", `/api/v1/projects/${book.id}/git/remotes`, { name: "Backup", url: `https://${gitHost.host}/mairead/backup.git`, username: "mairead", token: gitHost.token, audience: "private", include_notes: true });
  if (rem.status >= 300) hard("seed remote: " + rem.status + " " + JSON.stringify(rem.json).slice(0, 160));
  else S.remote = rem.json.item.id;
  // the import screens: an EPUB, a PDF and a Word file with tracked changes, and a project made from the book (its edition, for the Editions pages)
  S.epub = makeEpub(join(kit.tmp, "marsh.epub"), "ok", kit.tmp);
  S.pdf = writePdf(join(kit.tmp, "harbour.pdf"));
  S.docx = makeDocx(join(kit.tmp, "harbour.docx"), kit.tmp);
  S.longDocx = makeLongDocx(join(kit.tmp, "long.docx"), kit.tmp);
  S.text = join(kit.tmp, "harbour.txt");
  writeFileSync(S.text, ["Chapter 1", "Chapter 2", "Chapter 3"].map((l) => `${l}\n\nThe boat came in at dawn and the gulls wheeled over it, and nobody said a word.\n\nShe waited by the window until the light was gone.`).join("\n\n") + "\n");
  {
    const b64 = readFileSync(S.epub).toString("base64");
    const pv = await mk("/api/v1/imports/preview", { name: "marsh", epub: b64 });
    const ap = await mk("/api/v1/imports", { name: "marsh", epub: b64, token: pv.token, title: "The Marsh Almanac", kind: "nonfiction", edition_label: "First printing" });
    S.edSlug = ap.slug; S.edPid = ap.project_id; S.edId = ap.edition_id;
    // the diff screens: a chapter reworded by hand (one difference to review), a revised edition of the book in a project of its own to compare with
    const docsOf = (await api(wren, "GET", `/api/v1/projects/${ap.project_id}/docs`)).json.items;
    const road = docsOf.find((d) => d.title === "The Road");
    const rd = (await api(wren, "GET", `/api/v1/docs/${road.id}`)).json.item;
    const up = await api(wren, "PATCH", `/api/v1/docs/${road.id}`, { content: rd.content.replace(" marsh ", " marsh EDITED words here "), base_revision: rd.revision });
    if (up.status >= 300) hard("seed edit for the diff: " + up.status);
    const dl = await api(wren, "GET", `/api/v1/projects/${ap.project_id}/editions/${ap.edition_id}/diff`);
    S.diffKey = ((dl.json.steps || []).find((st) => st.status === "edited") || {}).key;
    if (!S.diffKey) hard("seed diff: no edited step");
    const e2 = makeEpub(join(kit.tmp, "marsh-v2.epub"), "v2", kit.tmp), b2 = readFileSync(e2).toString("base64");
    const pv2 = await mk("/api/v1/imports/preview", { name: "marsh2", epub: b2 });
    const ap2 = await mk("/api/v1/imports", { name: "marsh2", epub: b2, token: pv2.token, title: "The Marsh Almanac Second Edition", kind: "nonfiction", edition_label: "Second printing" });
    S.ed2Pid = ap2.project_id; S.ed2Id = ap2.edition_id;
    const cmp = await api(wren, "GET", `/api/v1/projects/${ap.project_id}/editions/${ap.edition_id}/compare?with_project=${ap2.project_id}&with_edition=${ap2.edition_id}`);
    S.cmpKey = ((cmp.json.steps || []).find((st) => st.status === "edited") || {}).key;
    if (!S.cmpKey) hard("seed compare: no edited step");
    // a chapter of 200 differences, for the pager (four pages of 50)
    {
      const bb = readFileSync(makeBigEpub(join(kit.tmp, "long.epub"), kit.tmp)).toString("base64");
      const pvb = await mk("/api/v1/imports/preview", { name: "long", epub: bb });
      const apb = await mk("/api/v1/imports", { name: "long", epub: bb, token: pvb.token, title: "The Long Marsh Book", kind: "nonfiction", edition_label: "Long printing" });
      const ch1 = (await api(wren, "GET", `/api/v1/projects/${apb.project_id}/docs`)).json.items.find((d) => /^Chapter 1:/.test(d.title));
      const cd = (await api(wren, "GET", `/api/v1/docs/${ch1.id}`)).json.item;
      await api(wren, "PATCH", `/api/v1/docs/${ch1.id}`, { content: cd.content.split("\n\n").map((q, i) => (i % 2 === 1 ? q.replace("Paragraph", "Reworded paragraph") : q)).join("\n\n"), base_revision: cd.revision });
      const dlb = await api(wren, "GET", `/api/v1/projects/${apb.project_id}/editions/${apb.edition_id}/diff`);
      S.longSlug = apb.slug; S.longEd = apb.edition_id;
      S.longKey = ((dlb.json.steps || []).find((st) => st.status === "edited") || {}).key;
      if (!S.longKey) hard("seed long diff: no edited step");
    }
    await mk(`/api/v1/projects/${ap.project_id}/members`, { email: "admin@dev.scriobh", role: "editor" });
    await mk(`/api/v1/projects/${ap.project_id}/members`, { email: "reader@dev.scriobh", role: "reader" });
  }

  // Phase 7 privacy: a book with a passphrase (Wren has it open; Ada, an editor, has not), a Compendium merge on record, an Ollama-style provider for the index
  const locked = (await mk("/api/v1/projects", { title: "The Vault", kind: "novel" })).item;
  S.lslug = locked.slug; S.lpid = locked.id;
  await mk(`/api/v1/projects/${locked.id}/members`, { email: people.editor, role: "editor" });
  const ps = await api(wren, "PUT", `/api/v1/projects/${locked.id}/passphrase`, { passphrase: "correct horse battery", idle_minutes: 30 });
  if (ps.status >= 300) hard("seed passphrase: " + ps.status + " " + JSON.stringify(ps.json).slice(0, 160));
  const spare = (await mk(`/api/v1/compendiums/${comp.id}/entities`, { kind: "item", name: "Spare lantern", summary: "Moved by a split." })).item.id;
  const sp = await api(wren, "POST", `/api/v1/compendiums/${comp.id}/split/preview`, { name: "Lanterns", entity_ids: [spare] });
  if (sp.status >= 300) hard("seed split preview: " + sp.status);
  else {
    const sd = await api(wren, "POST", `/api/v1/compendiums/${comp.id}/split`, { name: "Lanterns", entity_ids: [spare], fingerprint: sp.json.plan.fingerprint });
    if (sd.status >= 300) hard("seed split: " + sd.status + " " + JSON.stringify(sd.json).slice(0, 160)); else S.change = sd.json.change_id;
  }
  const ol = await api(ada, "POST", "/api/v1/ai/providers", { kind: "ollama", name: "Local embeddings", base_url: fake.base, local: true, default_model: "embed-a", scope: "instance" });
  if (ol.status >= 300) hard("seed embedding provider: " + ol.status);
  await mk("/api/v1/projects", { title: "Harbour Notes", kind: "blog" });
  await mk("/api/v1/projects", { title: "Short stories", kind: "collection" });
}

// ---- the page registry. path() -> url; wait: a selector to wait for; titleOk: the title may repeat another page's (same page, other role);
// status: the expected HTTP status; dialogs: scenarios from DIALOGS below.
const clearOpenDrafts = (pg) => pg.evaluate(async () => { // five open imports are the most a person may hold: the walk of the import pages makes more
  const t = document.querySelector('meta[name="csrf-token"]').content;
  const l = await (await fetch("/api/v1/imports/drafts")).json();
  for (const d of l.items) if (d.status === "open") await fetch("/api/v1/imports/drafts/" + d.id, { method: "DELETE", headers: { "X-CSRF-Token": t } });
});
const P = [
  { id: "signin", who: "anon", path: () => "/" },
  { id: "notfound-anon", who: "anon", path: () => "/nope", status: 404, titleOk: true },
  { id: "share-locked", who: "anon", path: () => S.shareLocked, wait: "#sh-pass", expect: [/401/] },
  { id: "share-gone", who: "anon", path: () => "/s/shl_gone.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", status: 404 },
  { id: "share-open", who: "anon", path: () => S.share, wait: "#sh-main:not([hidden])" },
  { id: "progress-card", who: "anon", path: () => S.card },
  { id: "offline", who: "anon", path: () => "/offline" },
  { id: "library", who: "owner", path: () => "/", dialogs: ["account", "palette", "newproject"] },
  { id: "library-archived", who: "owner", path: () => "/?state=archived" },
  { id: "library-trash", who: "owner", path: () => "/?state=trashed" },
  { id: "project-new", who: "owner", path: () => "/ui/projects/new" },
  { id: "project-export", who: "owner", path: () => `/ui/projects/${S.pid}/export` },
  { id: "project-purge", who: "owner", path: () => `/ui/projects/${S.pid}/purge` },
  { id: "compendium-new", who: "owner", path: () => "/ui/compendiums/new" },
  { id: "settings", who: "owner", path: () => "/settings" },
  { id: "settings-ai", who: "owner", path: () => "/settings/ai", wait: ".ai-page" },
  { id: "settings-mcp", who: "owner", path: () => "/settings/mcp" },
  { id: "import", who: "owner", path: () => "/import" },
  { id: "import-pandoc", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => { await pg.locator("#imp-f-pandoc").check(); } },
  { id: "import-git", who: "owner", path: () => "/import/git" },
  { id: "import-epub", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => { await pg.locator("#imp-f-epub").check(); } },
  { id: "import-epub-preview", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => {
    // the review of a book: its cut and edition label, and the mapping editor with a row's panel open (nest and join are in it)
    await pg.locator("#imp-f-epub").check();
    await pg.locator("#imp-epub").setInputFiles(S.epub);
    await pg.locator("#imp-h").waitFor({ timeout: 20000 });
    await pg.waitForLoadState("load"); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
    await pg.locator('#imp-review summary[aria-label="Edit The Road"]').click();
    await pg.getByRole("button", { name: "Nest The Road under another" }).waitFor();
    await pg.waitForLoadState("load"); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
  } },
  { id: "import-pdf", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => { await pg.locator("#imp-f-pdf").check(); } },
  { id: "import-pdf-preview", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => {
    await pg.locator("#imp-f-pdf").check();
    await pg.locator("#imp-pdffile").setInputFiles(S.pdf);
    await pg.locator("#imp-h").waitFor({ timeout: 25000 });
    await pg.waitForLoadState("load"); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
  } },
  { id: "import-docx-choice", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => { await pg.locator("#imp-f-docx").check(); } },
  { id: "import-docx-preview", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => {
    await pg.locator("#imp-f-docx").check();
    await pg.locator("#imp-docx").setInputFiles(S.docx);
    await pg.locator("#imp-h").waitFor({ timeout: 25000 });
    await pg.waitForLoadState("load"); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
  } },
  // The writer's own breaks: a chapter's Edit panel with the list of paragraphs to split it at, and the pattern section open.
  { id: "import-docx-own-breaks", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => {
    await pg.locator("#imp-f-docx").check();
    await pg.locator("#imp-docx").setInputFiles(S.docx);
    await pg.locator("#imp-h").waitFor({ timeout: 25000 });
    await pg.waitForLoadState("load"); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
    await pg.locator("#imp-pat-sum").click();
    await pg.locator(".imp-edit summary").first().click();
    await pg.getByRole("button", { name: /^Split this chapter/ }).waitFor();
    await pg.getByRole("button", { name: /^Split this chapter/ }).click();
    await pg.locator(".imp-paras").waitFor();
    await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
  } },
  // What is inside a long chapter: what was found, a button for each kind, and the places one by one, with a pattern that was refused.
  { id: "import-docx-inside", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => {
    await pg.locator("#imp-f-docx").check();
    await pg.locator("#imp-docx").setInputFiles(S.longDocx);
    await pg.locator("#imp-h").waitFor({ timeout: 25000 });
    await pg.waitForLoadState("load"); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
    await pg.getByRole("button", { name: /^Look for breaks inside it: The Long One/ }).click();
    await pg.locator(".imp-inside").waitFor();
    await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
  } },
  // Phase 4: a plain text file (its panel, and its review with the places a chapter could start), a project's earlier imports with the undo
  // open, and the notice on the review of a book the project has had before.
  { id: "import-text-choice", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => { await pg.locator("#imp-f-text").check(); } },
  { id: "import-text-preview", who: "owner", path: () => "/import", titleOk: true, pre: async (pg) => {
    await clearOpenDrafts(pg);
    await pg.locator("#imp-f-text").check();
    await pg.locator("#imp-textfile").setInputFiles(S.text);
    await pg.locator("#imp-h").waitFor({ timeout: 25000 });
    await pg.waitForLoadState("load"); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
  } },
  { id: "import-history", who: "owner", path: () => `/p/${S.edSlug}/import`, titleOk: true },
  { id: "import-history-undo-open", who: "owner", path: () => `/p/${S.edSlug}/import`, titleOk: true, pre: async (pg) => {
    await pg.locator("#imp-sources .imp-undo-d > summary").first().click();
  } },
  { id: "import-already", who: "owner", path: () => `/p/${S.edSlug}/import`, titleOk: true, pre: async (pg) => {
    await clearOpenDrafts(pg);
    await pg.locator("#imp-f-epub").check();
    await pg.locator("#imp-epub").setInputFiles(S.epub);
    await pg.locator("#imp-already").waitFor({ timeout: 25000 });
    await pg.waitForLoadState("load"); await pg.waitForFunction(() => !document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added"));
  } },
  { id: "edition-diff", who: "owner", path: () => `/p/${S.edSlug}/editions/${S.edId}/diff`, titleOk: true },
  { id: "edition-diff-step", who: "owner", path: () => `/p/${S.edSlug}/editions/${S.edId}/diff?chapter=${encodeURIComponent(S.diffKey)}`, titleOk: true },
  { id: "edition-diff-paged", who: "owner", path: () => `/p/${S.longSlug}/editions/${S.longEd}/diff?chapter=${encodeURIComponent(S.longKey)}`, titleOk: true },
  { id: "edition-diff-paged-2", who: "owner", path: () => `/p/${S.longSlug}/editions/${S.longEd}/diff?chapter=${encodeURIComponent(S.longKey)}&page=2`, titleOk: true },
  { id: "edition-diff-reader", who: "reviewer", path: () => `/p/${S.edSlug}/editions/${S.edId}/diff`, titleOk: true },
  { id: "edition-compare-pick", who: "owner", path: () => `/p/${S.edSlug}/editions/compare?edition=${S.edId}`, titleOk: true },
  { id: "edition-compare", who: "owner", path: () => `/p/${S.edSlug}/editions/compare?edition=${S.edId}&with_project=${S.ed2Pid}&with_edition=${S.ed2Id}`, titleOk: true },
  { id: "edition-compare-step", who: "owner", path: () => `/p/${S.edSlug}/editions/compare?edition=${S.edId}&with_project=${S.ed2Pid}&with_edition=${S.ed2Id}&chapter=${encodeURIComponent(S.cmpKey)}`, titleOk: true },
  { id: "editions", who: "owner", path: () => `/p/${S.edSlug}/editions` },
  { id: "edition", who: "owner", path: () => `/p/${S.edSlug}/editions/${S.edId}`, titleOk: true },
  { id: "editor-editions", who: "editor", path: () => `/p/${S.edSlug}/editions`, titleOk: true },
  { id: "reader-editions", who: "reviewer", path: () => `/p/${S.edSlug}/editions`, titleOk: true },
  { id: "editions-empty", who: "owner", path: () => `/p/${S.slug}/editions`, titleOk: true },
  { id: "search-all", who: "owner", path: () => "/search?q=tide" },
  { id: "compendiums", who: "owner", path: () => "/compendiums" },
  { id: "notfound", who: "owner", path: () => "/p/no-such-book", status: 404, titleOk: true },
  { id: "write-project", who: "owner", path: () => `/p/${S.slug}`, wait: ".scriobh-prose", titleOk: true },
  { id: "write", who: "owner", expect: [/ERR_INTERNET_DISCONNECTED/], path: () => `/p/${S.slug}/d/${S.doc}`, wait: ".scriobh-prose", titleOk: true, dialogs: ["account", "palette", "inspector", "jump", "aimenu"], write: true, inspectorTabs: true },
  { id: "write-note", who: "owner", path: () => `/p/${S.slug}/d/${S.note}`, wait: ".scriobh-prose" },
  { id: "outline", who: "owner", path: () => `/p/${S.slug}/outline`, wait: "#o-live" },
  { id: "cards", who: "owner", path: () => `/p/${S.slug}/cards`, wait: "#b-live" },
  { id: "board", who: "owner", path: () => `/p/${S.slug}/board`, wait: "#b-live" },
  { id: "read-landing", who: "owner", path: () => `/p/${S.slug}/read`, titleOk: true },
  { id: "read", who: "owner", path: () => `/p/${S.slug}/read/${S.doc}`, wait: "#rv-margin", titleOk: true },
  { id: "research", who: "owner", path: () => `/p/${S.slug}/research` },
  { id: "research-board", who: "owner", path: () => `/p/${S.slug}/research/board` },
  { id: "research-tags", who: "owner", path: () => `/p/${S.slug}/research/tags` },
  { id: "research-note", who: "owner", path: () => `/p/${S.slug}/research/${S.rnote}` },
  { id: "scratch", who: "owner", path: () => `/p/${S.slug}/scratch` },
  { id: "compendium", who: "owner", path: () => `/p/${S.slug}/compendium` },
  { id: "entities", who: "owner", path: () => `/p/${S.slug}/entities` },
  { id: "entity", who: "owner", path: () => `/p/${S.slug}/e/${S.ents[0]}` },
  { id: "timeline", who: "owner", path: () => `/p/${S.slug}/timeline` },
  { id: "goals", who: "owner", path: () => `/p/${S.slug}/goals` },
  { id: "studio", who: "owner", path: () => "/studio" },
  { id: "studio-all", who: "owner", path: () => "/studio?range=all&cal=all", titleOk: true },
  { id: "studio-new", who: "reviewer", path: () => "/studio", titleOk: true },
  { id: "studio-range-refused", who: "owner", path: () => "/studio?from=2026-02-01&to=2026-01-01", titleOk: true },
  { id: "studio-goals", who: "owner", path: () => "/studio/goals" },
  { id: "studio-goal", who: "owner", path: () => `/studio/goals/${S.sgoal}` },
  { id: "studio-sprints", who: "owner", path: () => "/studio/sprints" },
  { id: "studio-data", who: "owner", path: () => "/studio/data" },
  { id: "studio-card", who: "owner", path: () => "/studio/card" },
  { id: "search", who: "owner", path: () => `/p/${S.slug}/search?q=tide` },
  { id: "files", who: "owner", path: () => `/p/${S.slug}/files` },
  { id: "cover", who: "owner", path: () => `/p/${S.slug}/cover` },
  { id: "cover-wrap", who: "owner", path: () => `/p/${S.wslug}/cover`, titleOk: true },
  { id: "cover-generated", who: "owner", path: () => "/p/harbour-notes/cover", titleOk: true },
  { id: "cover-reader", who: "reviewer", path: () => `/p/${S.slug}/cover`, titleOk: true },
  { id: "cover-draft", who: "owner", path: () => "/p/harbour-notes/cover", titleOk: true, pre: async (pg) => {
    // a picture chosen under "front and back": the page answers with the draft, the picture drawn on its page, the note, the page mode's controls
    await pg.locator("#cv-shape-sides").check();
    await pg.locator("#cv-front-mode input[value=page]").check();
    const nav = pg.waitForNavigation();
    await pg.locator("#cv-front-file").setInputFiles({ name: "front.png", mimeType: "image/png", buffer: harbourPng(600, 900) });
    await nav;
    await pg.waitForSelector(".cv-note");
  } },
  { id: "cover-error", who: "owner", path: () => "/p/harbour-notes/cover", titleOk: true, expect: [/status of 422/], pre: async (pg) => {
    // the same, saved with no description: the refusal in words, the field marked, focus on the sentence
    await pg.locator("#cv-shape-sides").check();
    await pg.locator("#cv-front-mode input[value=image]").check();
    const nav = pg.waitForNavigation();
    await pg.locator("#cv-front-file").setInputFiles({ name: "front.png", mimeType: "image/png", buffer: harbourPng(600, 900) });
    await nav;
    const nav2 = pg.waitForNavigation();
    await pg.getByRole("button", { name: "Save the cover" }).click();
    await nav2;
    await pg.waitForSelector("#cv-msg");
  } },
  { id: "review", who: "owner", path: () => `/p/${S.slug}/review` },
  { id: "share", who: "owner", path: () => `/p/${S.slug}/share` },
  { id: "compile", who: "owner", path: () => `/p/${S.slug}/compile`, wait: "#cmp-preset option" },
  { id: "compile-print", who: "owner", path: () => `/p/${S.slug}/compile`, wait: "#cmp-preset option", titleOk: true, pre: async (pg) => {
    // the print options open, and the proof drawn on the page
    await pg.selectOption("#cmp-preset", "print-6x9");
    await pg.getByRole("button", { name: "Preview", exact: true }).click();
    await kit.waitFor(async () => /The preview is ready/.test(await pg.locator("#cmp-status").innerText()), "the proof", 30000);
  } },
  // the paperback cover on the Compile page: measured and drawn from a layout (KDP, with the barcode box), a wrap whose spine does not fit
  // the page count, a layout for another page size (the refusal, as an alert), and the generated cover
  ...[["compile-cover", () => S.slug, "print-6x9", "200", "kdp", true], ["compile-cover-stale", () => S.wslug, "print-6x9", "300", "kdp", false],
    ["compile-cover-refused", () => S.slug, "print-5x8", "200", "kdp", false], ["compile-cover-generated", () => "harbour-notes", "print-6x9", "200", "ingram", false]].map(([id, slug, preset, pages, profile, barcode]) => (
    { id, who: "owner", path: () => `/p/${slug()}/compile`, wait: "#cmp-preset option", titleOk: true, expect: id.endsWith("refused") ? [/status of 422/] : undefined, pre: async (pg) => {
      await pg.selectOption("#cmp-preset", preset);
      await pg.fill("#cv-thick", "0.0025"); await pg.fill("#cv-pages", pages); await pg.selectOption("#cv-profile", profile);
      if (barcode) await pg.check("#cv-barcode");
      await pg.locator("#cmp-cv-form button[type=submit]").click();
      await kit.waitFor(async () => (await pg.locator("#cv-out").innerText()).length > 20, "the cover answer", 20000);
      await pg.waitForFunction(() => [...document.querySelectorAll("#cv-art image")].every((i) => { const im = new Image(); im.src = i.getAttribute("href"); return im.complete; }));
    } })),
  { id: "site", who: "owner", path: () => `/p/${S.slug}/site` },
  { id: "webhooks", who: "owner", path: () => `/p/${S.slug}/webhooks` },
  { id: "people", who: "owner", path: () => `/p/${S.slug}/people` },
  { id: "git", who: "owner", path: () => `/p/${S.slug}/git` },
  { id: "git-empty", who: "owner", path: () => "/p/harbour-notes/git", titleOk: true },
  { id: "git-remote", who: "owner", path: () => `/p/${S.slug}/git?remote=${S.remote}`, titleOk: true },
  { id: "git-publish", who: "owner", path: () => `/p/${S.slug}/git/publish?account_id=${S.account}` },
  { id: "git-publish-existing", who: "owner", path: () => `/p/${S.slug}/git/publish?account_id=${S.account}&mode=existing`, titleOk: true },
  { id: "git-publish-plan", who: "owner", path: () => `/p/${S.slug}/git/publish?account_id=${S.account}`, titleOk: true, pre: async (pg) => {
    // the plan, drawn by the server from the fake host: what will be created, what is left out, the button
    await pg.fill("#pf-name", "salt-road-audit");
    await pg.check('#fields input[name="audience"][value="public"]');
    await pg.click('button:has-text("Review the plan")');
    await pg.waitForSelector("#plan");
  } },
  { id: "git-release", who: "owner", path: () => `/p/${S.slug}/git/release?remote=${S.remote}`, titleOk: true },
  { id: "editor-git", who: "editor", path: () => `/p/${S.slug}/git?remote=${S.remote}`, titleOk: true },
  { id: "kindle", who: "owner", path: () => `/p/${S.slug}/kindle` },
  { id: "kindle-preview", who: "owner", path: () => `/p/${S.slug}/kindle`, titleOk: true, pre: async (pg) => {
    // the preview is the answer to a form: what will be sent, the statement that the manuscript leaves this server, the confirm button
    await pg.locator("#start button[type=submit]").click();
    await pg.waitForSelector("#preview");
  } },
  { id: "project-import", who: "owner", path: () => `/p/${S.slug}/import` },
  { id: "project-ai", who: "owner", path: () => `/p/${S.slug}/ai`, wait: ".ai-page" },
  { id: "passphrase", who: "owner", path: () => `/p/${S.slug}/passphrase` },
  { id: "passphrase-set", who: "owner", path: () => `/p/${S.lslug}/passphrase`, titleOk: true },
  { id: "passphrase-member", who: "editor", path: () => `/p/${S.lslug}/passphrase`, titleOk: true },
  { id: "unlock", who: "editor", path: () => `/p/${S.lslug}`, titleOk: true },
  { id: "gate-cover", who: "owner", path: () => `/p/${S.lslug}`, titleOk: true, expect: [/403/, /ERR_FAILED/], pre: async (pg) => {
    // the project locks while the page is open and the unlock page cannot be reached (it is held back here): the cover stays, with nothing of the page behind it
    await pg.route("**/unlock?next=*", (r) => r.abort());
    await pg.evaluate((id) => fetch(`/api/v1/projects/${id}/lock`, { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": document.querySelector('meta[name="csrf-token"]').content } }), S.lpid);
    await kit.waitFor(() => pg.evaluate(() => !!(document.getElementById("gate-cover") && document.getElementById("gate-cover").open)), "the cover", 25000);
  }, before: async () => { const o = await pageFor("owner"); await api(o.ctx, "POST", `/api/v1/projects/${S.lpid}/unlock`, { passphrase: "correct horse battery" }); } },
  { id: "semantic", who: "owner", path: () => `/p/${S.slug}/semantic` },
  { id: "semantic-preview", who: "owner", path: () => `/p/${S.slug}/semantic`, titleOk: true, pre: async (pg) => {
    await pg.fill("#sem-model", "embed-a");
    await pg.getByRole("button", { name: "See what would be sent" }).click();
    await pg.waitForSelector("#sem-preview");
  } },
  { id: "c-manage", who: "owner", path: () => `/c/${S.cslug}/manage` },
  { id: "c-manage-preview", who: "owner", path: () => `/c/${S.cslug}/manage`, titleOk: true, pre: async (pg) => {
    await pg.locator('form[action$="/manage/merge-preview"] button[type="submit"]').click();
    await pg.waitForSelector("#preview");
  } },
  { id: "c-changes", who: "owner", path: () => `/c/${S.cslug}/changes` },
  { id: "c-change", who: "owner", path: () => `/c/${S.cslug}/changes/${S.change}` },
  { id: "c-home", who: "owner", path: () => `/c/${S.cslug}` },
  { id: "c-entities", who: "owner", path: () => `/c/${S.cslug}/entities` },
  { id: "c-entity", who: "owner", path: () => `/c/${S.cslug}/e/${S.ents[1]}` },
  { id: "c-fields", who: "owner", path: () => `/c/${S.cslug}/fields` },
  { id: "c-timeline", who: "owner", path: () => `/c/${S.cslug}/timeline` },
  { id: "c-timeline-new", who: "owner", path: () => `/c/${S.cslug}/timeline/events/new` },
  { id: "c-timeline-event", who: "owner", path: () => `/c/${S.cslug}/timeline/events/${S.events[0]}` },
  { id: "c-entity-merge", who: "owner", path: () => `/c/${S.cslug}/e/${S.ents[0]}/merge?into=${S.ents[2]}` },
  { id: "git-force", who: "owner", path: () => `/p/${S.slug}/git/force?remote=${S.remote}`, titleOk: true },
  { id: "git-force-409", who: "owner", path: () => "/p/harbour-notes/git/force", status: 409, titleOk: true },
  { id: "c-research", who: "owner", path: () => `/c/${S.cslug}/research` },
  { id: "c-research-note", who: "owner", path: () => `/c/${S.cslug}/research/${S.crnote}` },
  { id: "c-files", who: "owner", path: () => `/c/${S.cslug}/files` },
  { id: "admin-users", who: "editor", path: () => "/admin/users" },
  { id: "admin-quotas", who: "editor", path: () => "/admin/quotas" },
  { id: "admin-diagnostics", who: "editor", path: () => "/admin/diagnostics" },
  // the Pandoc page: switched on (seeded), after a test, then off with its path kept (the entries after it see the bridge off)
  { id: "admin-pandoc", who: "editor", path: () => "/admin/pandoc" },
  { id: "admin-pandoc-tested", who: "editor", path: () => "/admin/pandoc", titleOk: true, pre: async (pg) => { await pg.getByRole("button", { name: "Test", exact: true }).click(); await pg.waitForSelector(".pd-result"); } },
  { id: "admin-pandoc-off", who: "editor", path: () => "/admin/pandoc", titleOk: true, pre: async (pg) => {
    await api((await pageFor("editor")).ctx, "PUT", "/api/v1/admin/pandoc", { enabled: false, path: S.pandoc });
    await pg.reload({ waitUntil: "load" });
  } },
  // after a restore: the mark the restore step writes, so the banner is walked (without a sqlite3 program this is the plain off page)
  { id: "admin-pandoc-restored", who: "editor", path: () => "/admin/pandoc", titleOk: true, pre: async (pg) => {
    spawnSync("sqlite3", [join(kit.dataDir, "scriobh.db"), "PRAGMA busy_timeout=5000; INSERT INTO settings(key, value, updated_at) VALUES ('pandoc.restore_paused', '1', '2026-01-01T00:00:00Z') ON CONFLICT(key) DO UPDATE SET value = '1';"]);
    await pg.reload({ waitUntil: "load" });
  } },
  { id: "admin-pandoc-refused", who: "editor", path: () => "/admin/pandoc", titleOk: true, expect: [/422/], pre: async (pg) => {
    // the answer to a refused save, written into the page (a reload of a posted page would ask to send the form again)
    await pg.evaluate(async () => {
      const f = new URLSearchParams({ _csrf: document.querySelector('input[name="_csrf"]').value, enabled: "1", path: "relative/pandoc", memory_mb: "512" });
      const r = await fetch("/ui/admin/pandoc/save", { method: "POST", body: f, credentials: "same-origin" });
      document.open(); document.write(await r.text()); document.close();
    });
    await pg.waitForSelector("#pd-error");
  } },
  { id: "admin-mail", who: "editor", path: () => "/admin/mail" },
  { id: "admin-disable", who: "editor", path: () => `/admin/users/${(S.users.find((u) => u.email === people.reviewer) || {}).id}/disable` },
  { id: "editor-write", who: "editor", path: () => `/p/${S.slug}/d/${S.doc}`, wait: ".scriobh-prose", titleOk: true },
  { id: "editor-outline", who: "editor", path: () => `/p/${S.slug}/outline`, wait: "#o-live", titleOk: true },
  { id: "editor-review", who: "editor", path: () => `/p/${S.slug}/review`, titleOk: true },
  { id: "editor-compile", who: "editor", path: () => `/p/${S.slug}/compile`, wait: "#cmp-preset option", titleOk: true },
  { id: "editor-people", who: "editor", path: () => `/p/${S.slug}/people`, titleOk: true },
  { id: "editor-share-403", who: "editor", path: () => `/p/${S.slug}/share`, status: 403, titleOk: true },
  { id: "reviewer-write", who: "reviewer", path: () => `/p/${S.slug}/d/${S.doc}`, wait: ".scriobh-prose", titleOk: true },
  { id: "reviewer-read", who: "reviewer", path: () => `/p/${S.slug}/read/${S.doc}`, wait: "#rv-margin", titleOk: true },
  { id: "reviewer-review", who: "reviewer", path: () => `/p/${S.slug}/review`, titleOk: true },
  { id: "reviewer-cards", who: "reviewer", path: () => `/p/${S.slug}/cards`, wait: "#b-live", titleOk: true },
  { id: "reviewer-people", who: "reviewer", path: () => `/p/${S.slug}/people`, titleOk: true },
  { id: "reviewer-ai", who: "reviewer", path: () => `/p/${S.slug}/ai`, wait: ".ai-page", titleOk: true },
  { id: "reviewer-compile-404", who: "reviewer", path: () => `/p/${S.slug}/compile`, status: 404, titleOk: true },
  // the same person as a reader of the book (no comments, no editing)
  { id: "reader-write", who: "reviewer", reader: true, path: () => `/p/${S.slug}/d/${S.doc}`, wait: ".scriobh-prose", titleOk: true },
  { id: "reader-outline", who: "reviewer", reader: true, path: () => `/p/${S.slug}/outline`, wait: "#o-live", titleOk: true },
  { id: "reader-read", who: "reviewer", reader: true, path: () => `/p/${S.slug}/read/${S.doc}`, titleOk: true },
  { id: "reader-cards", who: "reviewer", reader: true, path: () => `/p/${S.slug}/cards`, wait: "#b-live", titleOk: true },
  { id: "reader-compendium", who: "reviewer", reader: true, path: () => `/p/${S.slug}/compendium`, titleOk: true },
  { id: "reader-entities", who: "reviewer", reader: true, path: () => `/p/${S.slug}/entities`, titleOk: true },
  { id: "reader-people", who: "reviewer", reader: true, path: () => `/p/${S.slug}/people`, titleOk: true },
  // desktop mode: its own server (see desktopWorld). The profile form first, while there is no profile; then the pages about
  // this computer as the local profile.
  { id: "desktop-profile", who: "desktop-first", base: deskBase, before: desktopWorld, path: () => "/desktop/profile" },
  { id: "desktop-profile-refused", who: "desktop-first", base: deskBase, before: desktopWorld, path: () => "/desktop/profile", titleOk: true, expect: [/422/], pre: async (pg) => {
    // the answer to a refused form, written into the page (as for the other refused forms above)
    await pg.evaluate(async () => {
      const f = new URLSearchParams({ _csrf: document.querySelector('input[name="_csrf"]').value, name: "Wren Writer", email: "not an address" });
      const r = await fetch("/desktop/profile", { method: "POST", body: f, credentials: "same-origin" });
      document.open(); document.write(await r.text()); document.close();
    });
    await pg.waitForSelector("#dp-error");
  } },
  { id: "desktop-identity", who: "desktop", base: deskBase, before: desktopSigned, path: () => "/desktop/identity" },
  { id: "desktop-identity-provider", who: "desktop", base: deskBase, path: () => "/desktop/identity", titleOk: true, before: async () => {
    await desktopSigned();
    const r = await DW.ctx.request.fetch(DW.base + "/api/v1/desktop/idp", { method: "PUT", headers: { "Content-Type": "application/json" },
      data: { issuer: "https://idp.example.org/realms/writers", client_id: "scriobh", client_secret: "a-client-secret-for-the-audit", admin_group: "admins", port: 45123 } });
    if (r.status() !== 200) hard("desktop: the provider's settings were not saved: " + r.status());
  } },
  { id: "desktop-local-model", who: "desktop", base: deskBase, before: desktopSigned, path: () => "/desktop/local-model" },
  { id: "desktop-local-model-looked", who: "desktop", base: deskBase, before: desktopSigned, path: () => "/desktop/local-model", titleOk: true, pre: async (pg) => {
    await pg.getByRole("button", { name: "Look for Ollama on this computer" }).click();
    await pg.waitForSelector("#lm-result");
  } },
];

// ---------------------------------------------------------------------------------------------- per-page checks
async function load(entry, pg, vp, mq = {}) {
  await pg.emulateMedia({ colorScheme: mq.scheme || "light", reducedMotion: mq.reduced ? "reduce" : "no-preference", forcedColors: mq.forced ? "active" : "none" });
  await pg.setViewportSize(vp);
  // A document is open for editing in one tab at a time: the other browser of the same person must let go first, or this one is read-only.
  if (entry.path && /\/d\//.test(entry.path())) for (const k of ["desk", "touch"]) { const o = ctxs[`${entry.who}/${k}`]; if (o && o.page !== pg && o.page.url() !== "about:blank") { await o.page.goto("about:blank").catch(() => {}); await pg.waitForTimeout(150); } }
  if (entry.html) { await pg.setContent(entry.html); await pg.evaluate(helpers); await pg.waitForTimeout(100); return; }
  if (entry.before) await entry.before(); // set the scene (a project the last pass locked is opened again)
  const url = (entry.base ? entry.base() : base()) + entry.path();
  const resp = await pg.goto(url, { waitUntil: "load" });
  const want = entry.status || 200;
  if (resp && resp.status() !== want) throw new Error(`${entry.id}: ${url} answered ${resp.status()}, want ${want}`);
  if (entry.wait) await pg.waitForSelector(entry.wait, { state: "attached", timeout: 15000 }).catch(() => hard(`${entry.id}: never saw ${entry.wait}`));
  await pg.waitForSelector("main", { timeout: 10000 });
  if (entry.write) await kit.waitFor(() => pg.evaluate(() => !!(window.scriobh && window.scriobh.write && window.scriobh.alts)), "the writing view", 15000).catch(() => {});
  await pg.waitForTimeout(250);
  if (entry.pre) await entry.pre(pg);
}

const settle = (pg) => pg.evaluate(() => new Promise((res) => { const t0 = performance.now(); const tick = () => (document.getAnimations().every((a) => a.playState !== "running") || performance.now() - t0 > 1500 ? res() : requestAnimationFrame(tick)); tick(); }));
const TITLES = new Map(); // title -> page id
async function structure(entry, pg, tag, touch, modal = false) {
  await settle(pg);
  addAll(await pg.evaluate(scan, { touch, titleOk: !!entry.titleOk }), tag);
  const cdp = await pg.context().newCDPSession(pg);
  try {
    await cdp.send("Accessibility.enable");
    await cdp.send("DOM.enable");
    const { nodes } = await cdp.send("Accessibility.getFullAXTree");
    const needName = new Set(["button", "link", "textbox", "searchbox", "checkbox", "radio", "switch", "combobox", "listbox", "menuitem", "menuitemcheckbox", "menuitemradio", "option", "tab", "slider", "spinbutton", "treeitem", "tree", "dialog", "alertdialog", "toolbar", "radiogroup"]);
    const mains = nodes.filter((n) => !n.ignored && n.role && n.role.value === "main").length;
    if (mains !== 1 && !modal && !/AI /.test(tag)) record("main", "AX tree", `${mains} main landmarks in the accessibility tree`, tag); // behind an open modal dialog the page is inert, so none is expected
    const bad = [];
    for (const n of nodes) {
      if (n.ignored || !n.role || !needName.has(n.role.value)) continue;
      const name = ((n.name && n.name.value) || "").trim();
      if (!name) bad.push(n);
      else if (/^(textbox|searchbox|combobox)$/.test(n.role.value) && n.name.sources) {
        const used = n.name.sources.find((s) => s.value && s.value.value && !s.superseded);
        if (used && used.type === "placeholder") bad.push({ ...n, placeholderOnly: true });
      }
    }
    // the same name three or more times, with nothing (a description, or a named group around it) to tell them apart
    const byId = new Map(nodes.map((n) => [n.nodeId, n])), groupsNamed = new Set(["group", "region", "form", "dialog", "article", "tabpanel", "listitem", "row", "cell"]);
    const same = {};
    for (const n of nodes) { if (n.ignored || !n.role || !/^(button|link)$/.test(n.role.value)) continue; const name = ((n.name && n.name.value) || "").trim(); if (name) (same[n.role.value + "|" + name] = same[n.role.value + "|" + name] || []).push(n); }
    for (const [k, list] of Object.entries(same)) {
      if (list.length < 3) continue;
      const loose = list.filter((n) => {
        if (n.description && n.description.value) return false;
        for (let p = byId.get(n.parentId); p; p = byId.get(p.parentId)) if (p.role && groupsNamed.has(p.role.value) && p.name && (p.name.value || "").trim()) return false;
        return true;
      });
      if (loose.length >= 3) record("duplicate-name", k.replace("|", " "), `${loose.length} ${k.split("|")[0]}s share this name with no description or named group to tell them apart`, tag);
    }
    for (const n of bad) {
      let desc = n.role.value;
      try {
        const d = await cdp.send("DOM.describeNode", { backendNodeId: n.backendDOMNodeId });
        const at = {}; const a = d.node.attributes || []; for (let i = 0; i < a.length; i += 2) at[a[i]] = a[i + 1];
        desc = `${d.node.nodeName.toLowerCase()}${at.id ? "#" + at.id : at.class ? "." + at.class.split(" ")[0] : ""}${at.type ? `[type=${at.type}]` : ""}${at["aria-label"] ? ` "${at["aria-label"]}"` : at.href ? ` href=${at.href.slice(0, 30)}` : at.name ? ` name=${at.name}` : ""}`;
      } catch { /* gone */ }
      record(n.placeholderOnly ? "label-placeholder" : "name", desc, n.placeholderOnly ? `${n.role.value} named only by its placeholder` : `${n.role.value} without an accessible name`, tag);
    }
    if (DUMP && tag === "1280 light") {
      const lm = nodes.filter((n) => !n.ignored && n.role && ["banner", "navigation", "main", "complementary", "region", "contentinfo", "search", "form"].includes(n.role.value)).map((n) => `${n.role.value}: ${(n.name && n.name.value) || "(no name)"}`);
      const hd = await pg.evaluate(() => [...document.querySelectorAll("h1,h2,h3,h4,h5,h6,[role=heading]")].filter((e) => window.__a11y.visible(e)).map((e) => `${"  ".repeat((+e.tagName[1] || +e.getAttribute("aria-level") || 2) - 1)}${e.tagName.toLowerCase()} ${e.textContent.trim().replace(/\s+/g, " ").slice(0, 70)}`));
      let snap = ""; try { snap = await pg.locator("body").ariaSnapshot(); } catch (e) { snap = "(ariaSnapshot failed: " + e.message + ")"; }
      writeFileSync(join(DUMP, entry.id + ".txt"), `# ${entry.id}  ${entry.path()}\ntitle: ${await pg.title()}\nlang: ${await pg.evaluate(() => document.documentElement.lang)}\n\n## landmarks\n${lm.join("\n")}\n\n## headings\n${hd.join("\n")}\n\n## accessibility tree (what a reader moves through, in order)\n${snap}\n`);
    }
  } finally { await cdp.detach().catch(() => {}); }
}

// Tab starts from the last focused element, so a page with an autofocused field is walked from there round to the start again.
async function toTop(pg) { await pg.evaluate(() => window.__a11y.toTop()); }
const ratio = (a, b) => { const lin = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; const L = (c) => 0.2126 * lin(c[0]) + 0.7152 * lin(c[1]) + 0.0722 * lin(c[2]); const x = L(a), y = L(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
const over = (c, b) => [c[0] * c[3] + b[0] * (1 - c[3]), c[1] * c[3] + b[1] * (1 - c[3]), c[2] * c[3] + b[2] * (1 - c[3]), 1];
const pc = (s) => { const m = String(s).match(/rgba?\(([^)]*)\)/); if (!m) return null; const p = m[1].split(/[ ,\/]+/).map(Number); return [p[0], p[1], p[2], p[3] == null || isNaN(p[3]) ? 1 : p[3]]; };

// Tab through the page, or through one part of it (o.root). Records focus-indicator, obscured and trap findings; returns the stops.
async function walk(entry, pg, tag, o = {}) {
  const root = o.root || null;
  if (!root) await toTop(pg);
  await pg.evaluate(() => window.__a11y.snapshot());
  let F = await pg.evaluate((r) => window.__a11y.tabbable(r).map((e) => ({ k: window.__a11y.key(e), sel: window.__a11y.sel(e), radio: e.matches("input[type=radio]") ? e.name + "|" + (e.form ? [...document.forms].indexOf(e.form) : "") : "" })), root);
  const stops = [], seenK = new Set();
  let consecutive = 0, last = null;
  const max = Math.min(o.max || 260, F.length + (root ? 3 : 25));
  const judge = (f0, noDiff) => {
    // The indicator has to be something focus did: an outline or ring that was already there, unchanged, before focus is not one.
    const f = { ...f0 };
    if (!noDiff && f.changed) { if (!f.changed.outline) f.outline = null; if (!f.changed.shadow) f.shadow = null; }
    if (o.forced) {
      if (!f.outline && !f.skip) record("forced-focus", f.sel, "with forced-colors active the focused control shows no outline of its own for focus (a shadow, a colour change, or an outline it always has, is not one)", tag);
      return;
    }
    if (!f.fv) record("focus-visible", f.sel, "focused by keyboard but :focus-visible does not match", tag);
    if (f.obscured) record("focus-obscured", f.sel, `focused control is covered by ${f.obscuredBy}`, tag);
    if (f.offscreen) record("focus-offscreen", f.sel, "focused control is not in the viewport", tag);
    if (f.editor && !f.outline && !f.shadow) return; // the writing surface shows a caret, not a ring; the editor's own tests cover it
    const bgs = [f.opaqueBg, f.parentBg].filter(Boolean);
    if (!bgs.length) return; // an image is behind it; judged by eye
    let best = 0, how = "";
    const ind = [];
    if (f.outline && f.outline.c[3] > 0) ind.push(["outline", f.outline.c, f.outline.w]);
    if (f.shadow && f.shadow.c[3] > 0 && (f.shadow.spread >= 1 || f.shadow.blur === 0)) ind.push(["shadow", f.shadow.c, f.shadow.spread || 1]);
    for (const [name, c] of ind) for (const bg of bgs) { const r = ratio(over(c, bg), bg); if (r > best) { best = r; how = name; } }
    if (!ind.length && f.changed) {
      if (f.changed.border) { const a = pc(f.changed.bColor[0]), b = pc(f.changed.bColor[1]); if (a && b) { const r = ratio(over(b, bgs[0]), over(a, bgs[0])); if (r >= 3) { best = r; how = "border change"; } } }
      if (!how && f.changed.bg) { const a = pc(f.changed.bgColors[0]), b = pc(f.changed.bgColors[1]); if (a && b) { const r = ratio(over(b, bgs[0]), over(a, bgs[0])); if (r >= 3) { best = r; how = "background change"; } } }
    }
    if (!how) record("focus-indicator", f.sel, "no visible focus indicator (no outline or ring, and no 3:1 change of border or background)", tag);
    else if (best < 3) record("focus-indicator", f.sel, `the focus indicator (${how}) is ${best.toFixed(2)}:1 against what is behind it, want 3:1`, tag);
    else if (how === "outline" && ind[0][2] < 2) record("focus-indicator", f.sel, `the outline is ${ind[0][2]}px thick, want 2px`, tag);
  };
  if (root) { // the control that already has focus when a dialog opens is a stop too
    const f0 = await pg.evaluate((r) => window.__a11y.focusInfo(r), root);
    if (!f0.gone && f0.inRoot) { seenK.add(f0.key); last = f0.key; stops.push(f0); if (f0.fv) judge(f0, true); }
    const at = F.findIndex((x) => x.k === f0.key);
    if (at > 0) F = F.slice(at); // walking on from a control in the middle of the part: what comes before it is another walk's
  }
  for (let i = 0; i < max; i++) {
    await pg.keyboard.press("Tab");
    const f = await pg.evaluate((r) => window.__a11y.focusInfo(r), root);
    if (f.gone) { stops.push(f); if (stops.filter((x) => x.gone).length > 1 || root) break; last = null; continue; } // out into the browser's own controls; the next Tab comes back in
    if (root && !f.inRoot) break; // a popover lets Tab go on into the page: the part we were walking is done
    // a date or time field has several parts, each a Tab stop inside one element
    if (f.key === last) { if (++consecutive >= (/\[type=(date|time|datetime-local|month|week)\]/.test(f.sel) ? 6 : 2)) { record("focus-trap", f.sel, "Tab does not move on from here", tag); break; } continue; }
    consecutive = 0;
    last = f.key;
    if (seenK.has(f.key)) { if (!root && stops.length && f.key !== stops[0].key) record("focus-trap", f.sel, "Tab cycles inside a part of the page and never leaves it", tag); stops.push({ ...f, repeat: true }); break; }
    seenK.add(f.key); stops.push(f);
    judge(f);
  }
  if (!o.forced) {
    const got = new Set(stops.map((x) => x.key));
    const radios = new Set(await pg.evaluate((ks) => [...document.querySelectorAll("input[type=radio]")].filter((r) => ks.includes(window.__a11y.key(r))).map((r) => r.name + "|" + (r.form ? [...document.forms].indexOf(r.form) : "")), [...got]));
    const missed = F.filter((x) => !got.has(x.k) && !(x.radio && radios.has(x.radio)));
    if (!root && !stops.some((x) => x.gone || x.repeat) && stops.length >= max - 1) record("focus-trap", "page", `Tab did not leave or wrap after ${stops.length} stops`, tag);
    else for (const m of missed) record("unreachable", m.sel, "a focusable control that Tab never reached", tag);
  }
  return stops;
}

async function skipLink(entry, pg, tag) {
  await load(entry, pg, pg.viewportSize());
  // The first control in the page is the skip link (a page that autofocuses a field is entered past it; Shift+Tab reaches it from there).
  const first = await pg.evaluate(() => { const t = window.__a11y.tabbable()[0]; return t ? { skip: t.matches("a.skip, a[href^='#']"), href: t.getAttribute("href"), sel: window.__a11y.sel(t) } : null; });
  if (!first || !first.skip) return record("skip-link", first ? first.sel : "page", "the first control in the page is not a skip link", tag);
  if (first.href !== "#main") record("skip-link", first.sel, `the skip link points to ${first.href}, want #main`, tag);
  await pg.evaluate(() => { document.activeElement && document.activeElement.blur(); });
  await pg.keyboard.press("Shift+Tab"); // into the document's last stop or out of it; either way the next Tab walks forward
  await pg.evaluate(() => window.__a11y.tabbable()[0].focus());
  const a = await pg.evaluate(() => { const e = document.activeElement, r = e.getBoundingClientRect(); return { w: r.width, h: r.height, vis: r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth, sel: window.__a11y.sel(e) }; });
  if (a.w < 4 || a.h < 4 || !a.vis) record("skip-link", a.sel, "the skip link has focus but cannot be seen", tag);
  await pg.keyboard.press("Enter");
  await pg.waitForTimeout(80);
  const r = await pg.evaluate(() => ({ inMain: !!(document.activeElement && document.activeElement.closest("main")) }));
  if (!r.inMain) record("skip-link", a.sel, "activating the skip link does not move focus into main", tag);
  await pg.keyboard.press("Tab");
  const n = await pg.evaluate(() => ({ inMain: !!(document.activeElement && document.activeElement.closest("main")), sel: window.__a11y.sel(document.activeElement), any: window.__a11y.tabbable().some((e) => e.closest("main")) }));
  if (!n.inMain && n.any) record("skip-link", n.sel, "after the skip link, Tab goes to something outside main", tag);
}

// Shift+Tab from the end: no stop may repeat.
async function reverseWalk(entry, pg, tag, forward) {
  if (forward.length < 3) return;
  let last = null, consecutive = 0;
  for (let i = 0; i < forward.length + 3; i++) {
    await pg.keyboard.press("Shift+Tab");
    const k = await pg.evaluate(() => { const e = document.activeElement; return e && e !== document.body ? [window.__a11y.key(e), window.__a11y.sel(e)] : null; });
    if (!k) break;
    // a date or time field has several parts, each a stop inside one element, backwards as forwards (the forward walk allows the same)
    if (k[0] === last) { if (++consecutive >= (/\[type=(date|time|datetime-local|month|week)\]/.test(k[1]) ? 6 : 1)) { record("focus-trap", k[1], "Shift+Tab does not move on from here", tag); break; } continue; }
    last = k[0]; consecutive = 0;
  }
}

async function motion(entry, pg, tag) {
  const r = await pg.evaluate(() => {
    const out = [], A = window.__a11y;
    for (const a of document.getAnimations()) {
      const t = a.effect && a.effect.getComputedTiming ? a.effect.getComputedTiming() : null;
      if (a.playState !== "running" || !t) continue;
      if (t.iterations === Infinity || t.duration > 50) out.push([A.sel(a.effect.target), `${a.animationName || a.transitionProperty || "animation"} running (${Math.round(t.duration)}ms${t.iterations === Infinity ? ", infinite" : ""})`]);
    }
    const ms = (v) => v.split(",").map((x) => parseFloat(x) * (/ms/.test(x) ? 1 : 1000));
    for (const e of document.querySelectorAll("*")) {
      const cs = getComputedStyle(e);
      if (cs.animationName !== "none" && ms(cs.animationDuration).some((d) => d > 50) && A.visible(e)) out.push([A.sel(e), `animation ${cs.animationName} ${cs.animationDuration}`]);
      if (cs.transitionProperty !== "none" && ms(cs.transitionDuration).some((d) => d > 50) && A.visible(e)) out.push([A.sel(e), `transition ${cs.transitionProperty} ${cs.transitionDuration}`]);
    }
    return out;
  });
  for (const [s, m] of r) record("reduced-motion", s, m, tag);
}

// forced-colors: a selected, current, pressed or checked item differs from its siblings by more than colour
async function forcedState(entry, pg, tag) {
  const r = await pg.evaluate(() => {
    const out = [], A = window.__a11y;
    const sig = (e) => { const cs = getComputedStyle(e), b = (s) => getComputedStyle(e, s); return [cs.borderTopWidth, cs.borderBottomWidth, cs.borderLeftWidth, cs.borderRightWidth, cs.borderTopStyle, cs.borderBottomStyle, cs.outlineStyle, cs.outlineWidth, cs.textDecorationLine, cs.fontWeight, cs.fontStyle, b("::before").content, b("::after").content, b("::before").display, b("::after").display, e.querySelector("svg:not([aria-hidden=true]), img") ? "g" : ""].join("|"); };
    for (const attr of ["aria-current", "aria-selected", "aria-pressed", "aria-checked"]) {
      for (const e of document.querySelectorAll(`[${attr}]:not([${attr}=false])`)) {
        if (!A.shown(e) || (e.tagName === "INPUT" && e.type === "checkbox")) continue;
        const sibs = [...e.parentElement.children].filter((s) => s !== e && s.tagName === e.tagName && (!s.hasAttribute(attr) || s.getAttribute(attr) === "false") && A.shown(s));
        if (sibs.length && sig(e) === sig(sibs[0])) out.push([A.sel(e), attr]);
      }
    }
    // checked boxes and radios drawn by CSS: appearance none with no forced-color-adjust
    for (const e of document.querySelectorAll("input[type=checkbox], input[type=radio]")) {
      if (!A.visible(e) && !A.srOnly(e)) continue;
      const cs = getComputedStyle(e);
      if (cs.appearance === "none" && A.srOnly(e)) { // a visually hidden input with a drawn control: the drawn part must survive forced colors
        const lab = e.closest("label"), drawn = lab && [...lab.querySelectorAll("*")].find((x) => x !== e && A.shown(x) && getComputedStyle(x).backgroundColor !== "rgba(0, 0, 0, 0)" && x.getBoundingClientRect().width < 40);
        if (drawn && getComputedStyle(drawn).forcedColorAdjust !== "none" && parseFloat(getComputedStyle(drawn).borderTopWidth) === 0) out.push([A.sel(drawn), "drawn checkbox or radio: no border, so forced colours would hide it"]);
      }
    }
    return out;
  });
  for (const [s, attr] of r) record("forced-state", s, /drawn/.test(attr) ? attr : `${attr}: the state is not shown by shape (border, underline, weight or a mark), only by colour, so forced-colors mode loses it`, tag);
}

async function reflow(entry, pg, tag, label) {
  const o = await pg.evaluate(() => {
    const de = document.documentElement, w = de.clientWidth;
    const out = { scroll: de.scrollWidth - w, over: [] };
    const inScroller = (e) => { for (let p = e.parentElement; p && p !== document.body && p !== de; p = p.parentElement) { const ox = getComputedStyle(p).overflowX; if (ox === "auto" || ox === "scroll" || ox === "hidden" || ox === "clip") return true; } return false; };
    if (out.scroll > 1) for (const e of document.body.querySelectorAll("*")) {
      if (!window.__a11y.shown(e)) continue;
      const r = e.getBoundingClientRect();
      if (r.right > w + 1 && !inScroller(e) && getComputedStyle(e).position !== "fixed") out.over.push(`${window.__a11y.sel(e)} (right edge ${Math.round(r.right)} of ${w})`);
    }
    out.over = out.over.slice(0, 4); return out;
  });
  if (o.scroll > 1) record("reflow", o.over[0] ? o.over[0].split(" (")[0] : "page", `${label}: the page scrolls sideways by ${o.scroll}px; ${o.over.join("; ") || "no single element found"}`, tag);
}

// adoptedStyleSheets are not subject to the page's style-src, so the override works under the real policy
async function injectCss(pg, css) {
  await pg.evaluate((css) => { const s = new CSSStyleSheet(); s.replaceSync(css); document.adoptedStyleSheets = [...document.adoptedStyleSheets, s]; }, css);
}

async function textSpacing(entry, pg, tag) {
  const before = await pg.evaluate(() => window.__a11y.clipped());
  await injectCss(pg, "*, *::before, *::after { line-height: 1.5 !important; letter-spacing: 0.12em !important; word-spacing: 0.16em !important; } p { margin-bottom: 2em !important; }");
  const applied = await pg.evaluate(() => getComputedStyle(document.querySelector("main h1, main p, main li") || document.body).letterSpacing);
  if (!/px$/.test(applied) || parseFloat(applied) < 1) { hard(`${entry.id}: the text-spacing override did not apply (${applied})`); return; }
  await pg.waitForTimeout(120);
  const after = await pg.evaluate(() => window.__a11y.clipped());
  for (const k of Object.keys(after)) if (!(k in before)) record("text-spacing", k.replace(/ \((x|y)\)$/, ""), `text is cut off after the WCAG text-spacing override (${k.match(/\((x|y)\)$/)[1]}, ${after[k]})`, tag);
}

async function text200(entry, pg, tag) {
  const before = await pg.evaluate(() => window.__a11y.clipped());
  // A browser's text-only zoom is emulated by doubling the computed font size (and a fixed line height) of every element in place.
  const fs = await pg.evaluate(() => {
    const els = [...document.body.querySelectorAll("*")], cs = els.map((e) => { const c = getComputedStyle(e); return [parseFloat(c.fontSize), c.lineHeight]; });
    els.forEach((e, i) => { e.style.setProperty("font-size", cs[i][0] * 2 + "px", "important"); if (/px$/.test(cs[i][1])) e.style.setProperty("line-height", parseFloat(cs[i][1]) * 2 + "px", "important"); });
    const m = document.querySelector("main h1, main p, main li"); return m ? [cs[els.indexOf(m)][0], parseFloat(getComputedStyle(m).fontSize)] : [0, 0];
  });
  if (!(fs[1] >= fs[0] * 1.9)) { hard(`${entry.id}: the 200% text override did not apply (${fs[0]}px -> ${fs[1]}px)`); return; }
  await pg.waitForTimeout(150);
  const after = await pg.evaluate(() => window.__a11y.clipped());
  for (const k of Object.keys(after)) if (!(k in before)) record("text-200", k.replace(/ \((x|y)\)$/, ""), `text is cut off at 200% text size (${k.match(/\((x|y)\)$/)[1]}, ${after[k]})`, tag);
  await reflow(entry, pg, tag, "at 200% text size, 1280 px wide");
  const bars = await pg.evaluate(() => { let h = 0; for (const e of document.querySelectorAll("header, nav, div, aside")) { const cs = getComputedStyle(e); if ((cs.position === "fixed" || cs.position === "sticky") && window.__a11y.shown(e)) { const r = e.getBoundingClientRect(); if (r.width > innerWidth * 0.6 && r.height < innerHeight) h += r.height; } } return h / innerHeight; });
  if (bars > 0.5) record("text-200", "sticky bars", `fixed or sticky bars take ${Math.round(bars * 100)}% of the window height at 200% text`, tag);
}

// ---- dialogs and panels: focus goes in, Tab stays in a modal, Escape closes, focus comes back to where it was; what is inside is scanned and
// walked in light, dark and forced colours
const DIALOGS = {
  account: { prep: (pg) => pg.locator("button.avatar-btn").focus(), open: (pg) => pg.keyboard.press("Enter"), root: "#account", kind: "popover" },
  palette: { prep: (pg) => pg.locator("main").focus(), open: (pg) => pg.keyboard.press("Control+k"), root: "#palette", kind: "dialog" },
  newproject: { prep: (pg) => pg.locator('a[href="/ui/projects/new"], button:has-text("New project")').first().focus(), open: (pg) => pg.keyboard.press("Enter"), root: "#dialog", kind: "dialog" },
  // the inspector remembers whether it was open, so start from shut
  inspector: { prep: async (pg) => { if (await pg.locator("#itab-details").isVisible()) { await pg.locator("#insp-close").click(); await pg.waitForTimeout(300); } await pg.locator("#ver-open:visible, .insp-open:visible").first().focus(); }, open: (pg) => pg.keyboard.press("Enter"), root: "#inspector", kind: "panel" },
  jump: { prep: (pg) => pg.locator("#jump-open").focus(), open: (pg) => pg.keyboard.press("Enter"), root: "dialog[open]", kind: "dialog" },
  aimenu: { prep: async (pg) => { await pg.locator(".scriobh-prose").first().click(); }, open: (pg) => pg.keyboard.press("Alt+Shift+KeyI"), root: "dialog.ai-menu", kind: "dialog" },
};
async function dialogs(entry, pg, tag, names, scenarios = DIALOGS, inside = true) {
  for (const name of names) {
    const d = scenarios[name];
    try {
      await load(entry, pg, pg.viewportSize());
      await d.prep(pg);
      const before = await pg.evaluate(() => { const e = document.activeElement; return e ? window.__a11y.key(e) : 0; });
      await d.open(pg);
      await pg.waitForTimeout(350);
      const root = pg.locator(d.root).first();
      if (!(await root.isVisible().catch(() => false))) { record("dialog", name, `could not open by keyboard (${d.root} not visible)`, tag); continue; }
      const within = () => pg.evaluate((sel) => { const r = document.querySelector(sel); return !!(r && document.activeElement && r.contains(document.activeElement)); }, d.root);
      if (!(await kit.waitFor(within, "focus", 1200).catch(() => false))) record("dialog-focus", name, "focus does not move into the dialog or panel when it opens", tag);
      const nm = await pg.evaluate((sel) => { const r = document.querySelector(sel); if (!r) return "?"; const l = r.getAttribute("aria-label") || (r.getAttribute("aria-labelledby") && (document.getElementById(r.getAttribute("aria-labelledby").split(" ")[0]) || {}).textContent) || ""; return l.trim(); }, d.root);
      if (d.kind === "dialog" && !nm) record("dialog-name", name, "the dialog has no accessible name when open", tag);
      if (d.kind === "dialog") {
        // A modal dialog may hand focus to the browser's own controls (the page then reports no focused element); it may not hand it to the inert page.
        const outside = () => pg.evaluate((sel) => { const r = document.querySelector(sel), a = document.activeElement; return !!(a && a !== document.body && r && !r.contains(a)); }, d.root);
        let escaped = false;
        for (let i = 0; i < 30 && !escaped; i++) { await pg.keyboard.press("Tab"); if (await outside()) escaped = true; }
        if (escaped) record("dialog-trap", name, "Tab leaves the open modal dialog for the page behind it", tag);
        for (let i = 0; i < 30 && !(await within()); i++) await pg.keyboard.press("Shift+Tab");
      }
      await pg.keyboard.press("Escape");
      await pg.waitForTimeout(350);
      if (await root.isVisible().catch(() => false)) { record("dialog-escape", name, "Escape does not close it", tag); continue; }
      const back = await pg.evaluate(() => { const e = document.activeElement; return e && e !== document.body ? { key: window.__a11y.key(e), sel: window.__a11y.sel(e) } : null; });
      if (!back || back.key !== before) record("dialog-return", name, `focus is not returned to where it was (now ${back ? back.sel : "the page body"})`, tag);
      // what is inside it: names, contrast and targets once, and a walk by Tab with its focus rings in each colouring
      if (inside) for (const mq of [{ scheme: "light" }, { scheme: "dark" }, { forced: true }]) {
        await load(entry, pg, pg.viewportSize(), mq);
        await d.prep(pg); await d.open(pg); await pg.waitForTimeout(350);
        if (await root.isVisible().catch(() => false)) {
          if (!mq.forced && mq.scheme === "light") await structure(entry, pg, `${name} dialog`, false, d.kind === "dialog");
          await walk(entry, pg, `${name} dialog ${mq.forced ? "forced colors" : mq.scheme}`, { root: d.root, forced: !!mq.forced, max: 40 });
        } else hard(`${entry.id}: ${name} did not open again for its ${mq.forced ? "forced-colours" : mq.scheme} walk`);
        await pg.keyboard.press("Escape");
      }
    } catch (e) { hard(`${entry.id} dialog ${name}: ${e.message.split("\n")[0]}`); await pg.keyboard.press("Escape").catch(() => {}); }
  }
}

// The inspector's tabs (details, versions, in this scene, alternatives, stash, comments, lab, AI): each panel is loaded, scanned and walked
async function inspectorTabs(entry, pg, tag) {
  for (const mq of [{ scheme: "light" }, { scheme: "dark" }, { forced: true }]) {
    await load(entry, pg, pg.viewportSize(), mq);
    // the inspector is a column on a wide window and a sheet on a narrow one, and it remembers whether it was open
    if (!(await pg.locator("#itab-details").isVisible())) { await pg.locator("#ver-open, .insp-open").first().click(); await pg.locator("#itab-details").waitFor({ state: "visible", timeout: 5000 }); }
    await pg.waitForTimeout(400);
    const tabs = await pg.locator('#inspector [role="tab"]').evaluateAll((l) => l.map((e) => e.id));
    if (tabs.length < 4) hard(`${entry.id}: the inspector has only ${tabs.length} tabs`);
    for (const id of tabs) {
      await pg.locator("#" + id).click();
      await pg.waitForTimeout(500);
      if (!mq.forced && mq.scheme === "light") await structure(entry, pg, `inspector ${id.replace("itab-", "")}`, false);
      // start the walk at the tab, reached by keyboard (Shift+Tab out and Tab back), so it is judged as a keyboard focus and what follows it is in order
      await pg.locator("#" + id).focus(); await pg.keyboard.press("Shift+Tab"); await pg.keyboard.press("Tab");
      await walk(entry, pg, `inspector ${id.replace("itab-", "")} ${mq.forced ? "forced colors" : mq.scheme}`, { root: "#inspector", forced: !!mq.forced, max: 40 });
      // The Comments tab with the composer for selected words open (a person who can edit; the quote, the box, the choice of who can see it).
      if (id === "itab-comments" && (await pg.locator(".scriobh-prose").count())) {
        const opened = await pg.evaluate(() => { const S = window.scriobh, ed = S && S.write && S.write.editor; if (!ed || !S.inspector || !S.inspector.want) return false; let p = -1; ed.view.state.doc.descendants((n, pos) => { if (p < 0 && n.isText && n.text.trim().length > 6) p = pos; }); if (p < 0) return false; ed.alts.select(p, p + 5); S.inspector.want("comments"); return true; });
        await pg.waitForTimeout(700);
        if (opened && (await pg.locator("#cw-sel").count())) {
          if (!mq.forced && mq.scheme === "light") await structure(entry, pg, "inspector comments composer", false);
          await pg.locator("#" + id).focus(); await pg.keyboard.press("Shift+Tab"); await pg.keyboard.press("Tab");
          await walk(entry, pg, `inspector comments composer ${mq.forced ? "forced colors" : mq.scheme}`, { root: "#inspector", forced: !!mq.forced, max: 40 });
        }
      }
    }
  }
}

// ---- the writing view: states are announced and are not colour alone
const judgeLive = (st) => [!st.live && ["live-region", "#live", "the writing view has no live region for save, offline and conflict announcements"], !st.toasts && ["live-region", "#toasts", "no live region for toasts"], (!st.state || !st.state.trim()) && ["live-region", "#save-state", "the save state has no text"]].filter(Boolean);
async function liveRegions(entry, pg, tag, ctx) {
  await load(entry, pg, pg.viewportSize());
  const st = await pg.evaluate(() => {
    const l = document.querySelector("#live"), t = document.querySelector("#toasts");
    const ok = (e) => !!e && (e.getAttribute("role") === "status" || e.getAttribute("role") === "alert" || (e.getAttribute("aria-live") || "off") !== "off");
    return { live: ok(l), toasts: ok(t), state: (document.querySelector("#save-state") || {}).textContent };
  });
  for (const [rule, sel, msg] of judgeLive(st)) record(rule, sel, msg, tag);
  if (!st.state || !st.state.trim()) return;
  // Drive a real state change: cut the network, type, and see the state change in words and be announced.
  await pg.evaluate(() => { window.__said = []; const r = document.querySelector("#live"); new MutationObserver(() => window.__said.push(r.textContent)).observe(r, { childList: true, characterData: true, subtree: true }); });
  await ctx.setOffline(true);
  try {
    await pg.locator(".scriobh-prose").click();
    await pg.keyboard.press("Control+End");
    await pg.keyboard.type(" x");
    const changed = await kit.waitFor(async () => (await pg.locator("#save-state").innerText()).trim() !== st.state.trim(), "the state to change", 12000).catch(() => false);
    if (!changed) record("live-region", "#save-state", "cutting the network and typing does not change the visible save state", tag);
    const said = await kit.waitFor(async () => (await pg.evaluate(() => window.__said.filter((x) => x && x.trim()))).length > 0, "an announcement", 8000).catch(() => false);
    if (!said) record("live-region", "#live", "going offline is not announced in the live region", tag);
  } finally {
    await ctx.setOffline(false);
    await kit.waitFor(async () => /saved/i.test(await pg.locator("#save-state").innerText()), "saved again", 25000).catch(() => hard("write view: did not return to Saved after the network came back"));
  }
}

// ---- self-test: pages that break the rules on purpose; the audit is only worth anything if each rule can fire. A rule listed
// as having no negative control here is: focus-visible (a keyboard Tab always matches it in Chromium, so it cannot be made to fail on a page).
const SELFTEST_HTML = `<!doctype html><html><head><title>Scríobh</title><meta name="viewport" content="width=device-width, user-scalable=no">
<style>body{background:#fff;color:#000;font:16px sans-serif} a{color:#06c;text-decoration:none} .low{color:#bbb} input,textarea{border:1px solid #eee;background:#fff}
button,a,input,div{outline:none} .tiny{width:10px;height:10px;display:inline-block;padding:0;border:0;background:#ccc;margin:2px}</style></head>
<body><header></header><header></header><div role="navigation"></div><section aria-label=""></section>
<img src="data:image/gif;base64,R0lGODlhAQABAAAAACw="><h3>Skipped</h3><h1>One</h1><h1>Two</h1><h4>jump</h4><h2></h2><iframe src="about:blank"></iframe>
<div id="d"></div><div id="d"></div><button aria-controls="nowhere"></button><input type="text" placeholder="only a placeholder">
<p class="low">low contrast text lives here</p><p>See <a href="#x">this link here</a> for the details about the thing in a sentence.</p>
<button tabindex="3">positive tabindex</button><svg width="20" height="20"><circle r="5"/></svg>
<table><tr><td>a</td></tr><tr><td>b</td></tr></table><input type="radio" name="r"><input type="radio" name="r"><div role="tab">tab</div>
<div aria-hidden="true"><a href="#y">hidden but focusable</a></div><button class="tiny" aria-label="a"></button><button class="tiny" aria-label="b"></button>
<button>Save</button><button>Save</button><button>Save</button><div tabindex="0" id="trap">a focusable div with no ring</div></body></html>`;
// motion, forced colours, reflow, clipped text
const SELFTEST_B = `<!doctype html><html lang="en"><head><title>B</title><meta name="viewport" content="width=device-width"><style>
@keyframes spin{to{transform:rotate(360deg)}} .spin{width:20px;height:20px;background:#000;animation:spin 2s linear infinite} button{outline:none}
#clip{overflow:hidden;width:40px;white-space:nowrap;font:16px monospace} .wide{width:900px;height:10px;background:#888}</style></head>
<body><main><h1>B</h1><div class="spin"></div><nav aria-label="n"><a href="#a">A</a><a href="#b" aria-current="page">B</a></nav><div id="clip">abcd</div><div class="wide"></div><button>one</button></main></body></html>`;
// focus mechanics: a bar over a link, a link off the screen, Tab held back, a control focus bounces off
const SELFTEST_C = `<!doctype html><html lang="en"><head><title>C</title><meta name="viewport" content="width=device-width"><style>
.bar{position:fixed;top:0;left:0;right:0;height:40px;background:#ddd;z-index:5} a,button,input{outline:2px solid #000}</style></head>
<body><div class="bar"></div><a href="#" style="position:absolute;top:8px;left:8px">under the bar</a><a href="#" style="position:absolute;left:-5000px;top:100px">off the screen</a>
<input id="t" aria-label="held" onkeydown="if(event.key==='Tab')event.preventDefault()"><button onfocus="document.getElementById('t').focus()">bounced</button><button>after</button></body></html>`;
// dialogs: one that does not take focus, has no name, and does not close; one that closes and drops focus
const SELFTEST_D = `<!doctype html><html lang="en"><head><title>D</title><meta name="viewport" content="width=device-width"></head><body><main>
<button id="open1">open1</button><button id="open2">open2</button><button id="open3">open3</button>
<div id="d1" role="dialog" hidden><p>no name, nothing to focus</p></div><div id="d2" hidden><button id="d2b">inside</button></div><button id="after">after</button></main>
<script>open1.onclick=()=>{d1.hidden=false};open2.onclick=()=>{d2.hidden=false;d2b.focus()};document.addEventListener("keydown",e=>{if(e.key==="Escape"){e.preventDefault();d2.hidden=true;document.activeElement.blur()}});</script></body></html>`;
const NEGATIVE = {
  structure: ["lang", "title", "zoom", "main", "nav-label", "landmark-label", "landmark", "h1", "heading-empty", "heading-order", "dup-id", "aria-ref", "tabindex", "aria-hidden-focusable", "img-alt", "svg-name", "frame-title", "table-header", "radio-group", "aria-parent", "aria-state", "link-colour-only", "contrast", "field-contrast", "target-24", "target-44", "name", "label-placeholder", "duplicate-name", "focus-indicator"],
  other: ["reduced-motion", "forced-state", "forced-focus", "reflow", "text-spacing", "text-200", "focus-trap", "focus-obscured", "focus-offscreen", "unreachable", "skip-link", "dialog", "dialog-focus", "dialog-name", "dialog-trap", "dialog-escape", "dialog-return", "editor", "ai-proposal", "live-region"],
};
async function selftest() {
  const pg = (await pageFor("anon")).page;
  const here = async (html, vp = { width: 1280, height: 800 }, mq = {}) => { await pg.emulateMedia({ colorScheme: "light", reducedMotion: "no-preference", forcedColors: mq.forced ? "active" : "none" }); await pg.setViewportSize(vp); await pg.goto("about:blank"); await pg.setContent(html); await pg.evaluate(helpers); await pg.waitForTimeout(80); };
  cur = { page: "selftest", vp: "" };
  const E = { id: "selftest", html: SELFTEST_HTML };
  await here(SELFTEST_HTML);
  await structure(E, pg, "selftest", false);
  await structure(E, pg, "selftest", true);
  await walk(E, pg, "selftest");
  await skipLink(E, pg, "selftest");
  const EB = { id: "selftest-b", html: SELFTEST_B };
  await here(SELFTEST_B); await motion(EB, pg, "selftest"); await forcedState(EB, pg, "selftest");
  await here(SELFTEST_B, { width: 1280, height: 800 }, { forced: true }); await walk(EB, pg, "selftest", { forced: true });
  await here(SELFTEST_B, { width: 320, height: 640 }); await reflow(EB, pg, "selftest", "x");
  await here(SELFTEST_B); await textSpacing(EB, pg, "selftest");
  await here(SELFTEST_B); await text200(EB, pg, "selftest");
  await here(SELFTEST_C); await walk({ id: "selftest-c", html: SELFTEST_C }, pg, "selftest");
  const ED = { id: "selftest-d", html: SELFTEST_D };
  await dialogs(ED, pg, "selftest", ["one", "two", "three"], {
    one: { prep: (q) => q.locator("#open1").focus(), open: (q) => q.keyboard.press("Enter"), root: "#d1", kind: "dialog" },
    two: { prep: (q) => q.locator("#open2").focus(), open: (q) => q.keyboard.press("Enter"), root: "#d2", kind: "panel" },
    three: { prep: (q) => q.locator("#open3").focus(), open: async () => {}, root: "#nowhere", kind: "panel" },
  }, false);
  for (const [rule, sel, msg] of [...judgeEditor({ role: "div", multi: null, label: "", lang: "" }), ...judgeProposal({ marks: [["DEL", "none"], ["INS", "none"]], btn: [], label: "", role: null }), ...judgeLive({ live: false, toasts: false, state: "" })]) record(rule, sel, msg, "selftest");
  const got = new Set([...VIOL.values()].map((v) => v.rule));
  const missing = [...NEGATIVE.structure, ...NEGATIVE.other].filter((r) => !got.has(r));
  VIOL.clear();
  if (missing.length) throw new Error("the audit has rules that cannot fire (self-test): " + missing.join(", "));
}

// ---- the writing view: the editor's own semantics, and the writing partner's proposal (against the fake provider)
async function editorSemantics(entry, pg, tag) {
  await load(entry, pg, pg.viewportSize());
  const e = await pg.evaluate(() => {
    const ed = document.querySelector(".scriobh-prose");
    if (!ed) return null;
    return { role: ed.getAttribute("role"), multi: ed.getAttribute("aria-multiline"), label: ed.getAttribute("aria-label") || "", lang: ed.getAttribute("lang") || (ed.closest("[lang]") || {}).lang || "" };
  });
  if (!e) return hard("write view: no editor to check");
  for (const [rule, sel, msg] of judgeEditor(e)) record(rule, sel, msg, tag);
}
const judgeEditor = (e) => [e.role !== "textbox" && ["editor", ".scriobh-prose", `the editing surface has role ${e.role}, want textbox`], e.multi !== "true" && ["editor", ".scriobh-prose", "the editing surface lacks aria-multiline=true"], !e.label.trim() && ["editor", ".scriobh-prose", "the editing surface has no accessible name"], !e.lang && ["editor", ".scriobh-prose", "the editing surface does not carry the document's language"]].filter(Boolean);
const judgeProposal = (pop) => {
  const out = [];
  for (const [t, d] of pop.marks) { if (t === "DEL" && !/line-through/.test(d)) out.push(["ai-proposal", "del", "a removed word in the diff is not struck through, so it differs only by colour"]); if (t === "INS" && !/underline/.test(d)) out.push(["ai-proposal", "ins", "an added word in the diff is not underlined, so it differs only by colour"]); }
  if (!pop.btn.some((b) => /^Accept/.test(b)) || !pop.btn.some((b) => /^(Reject|Dismiss|Discard|Cancel|Close)/i.test(b))) out.push(["ai-proposal", "#ai-pop", `the proposal offers ${JSON.stringify(pop.btn)}: it needs an Accept and a Reject by name`]);
  if (!pop.label && !pop.role) out.push(["ai-proposal", "#ai-pop", "the proposal is not a named region or dialog"]);
  return out;
};

async function aiFlow(entry, pg, tag, touch) {
  await load(entry, pg, pg.viewportSize());
  try {
    await pg.evaluate(() => { const v = window.scriobh.write.editor.view; let hit = null; v.state.doc.descendants((n, pos) => { if (hit || !n.isText) return; const i = n.text.indexOf("Sentence 1.4 says the"); if (i >= 0) hit = [pos + i, pos + i + 21]; }); v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, hit[0], hit[1]))); v.focus(); });
    const menu = pg.locator("dialog.ai-menu");
    await pg.keyboard.press("Alt+Shift+KeyI");
    if (!(await menu.waitFor({ timeout: 2500 }).then(() => true, () => false))) { await pg.getByRole("button", { name: "Ask the writing partner about the selection" }).click(); await menu.waitFor({ timeout: 5000 }); }
    await menu.getByRole("button", { name: "Tighten" }).click();
    const dlg = pg.locator("dialog.ai-dlg:not(.ai-menu)");
    await dlg.waitFor({ timeout: 5000 });
    await kit.waitFor(async () => (await dlg.locator("pre.ai-pay").count()) > 0, "the payload", 8000);
    await structure(entry, pg, tag + " AI preview", touch);
    await dlg.getByRole("button", { name: /^Send/ }).click();
    await pg.locator("#ai-pop").waitFor({ timeout: 10000 });
    await pg.waitForTimeout(300);
    await structure(entry, pg, tag + " AI proposal", touch);
    const pop = await pg.evaluate(() => {
      const p = document.querySelector("#ai-pop");
      const marks = [...p.querySelectorAll(".ai-diff del, .ai-diff ins")].map((e) => [e.tagName, getComputedStyle(e).textDecorationLine]);
      const btn = [...p.querySelectorAll("button, a[href]")].map((b) => (b.getAttribute("aria-label") || b.textContent).trim());
      const live = !!p.closest("[role=dialog], [role=status], [role=alert], [aria-live]") || p.matches("[role=dialog], [role=status], [role=alert], [aria-live]") || !!document.querySelector("#live[role=status]");
      return { marks, btn, live, role: p.getAttribute("role"), label: p.getAttribute("aria-label") || p.getAttribute("aria-labelledby") || "", focusIn: p.contains(document.activeElement) };
    });
    if (!pop.marks.length) hard("AI proposal: no ins/del in the diff to check");
    for (const [rule, sel, msg] of judgeProposal(pop)) record(rule, sel, msg, tag);
    await pg.keyboard.press("Escape");
  } catch (e) { hard(`${entry.id} AI flow (${tag}): ${e.message.split("\n")[0]}`); if (DUMP) await pg.screenshot({ path: join(DUMP, `ai-flow-${tag}-failed.png`) }).catch(() => {}); await pg.keyboard.press("Escape").catch(() => {}); }
}

// ---------------------------------------------------------------------------------------------- run
const VP = { desk: { width: 1280, height: 800 }, touch: { width: 390, height: 844 } };
await kit.test("self-test: every rule fires on a page built to break it", selftest);
await kit.test("seed", async () => { await seed(); });
if (NOTES.length) console.log("SEED NOTES:\n  " + NOTES.join("\n  "));

for (const entry of P) {
  if (ONLY && !entry.id.includes(ONLY)) continue;
  await kit.test(entry.id, async () => {
    if (entry.status) kit.allowed.push(/Failed to load resource/);
    for (const re of entry.expect || []) kit.allowed.push(re);
    const desk = (await pageFor(entry.who, "desk")).page, touch = (await pageFor(entry.who, "touch")).page;
    kit.main = null;
    if (entry.reader && !S.readerDone) {
      S.readerDone = true;
      const owner = (await pageFor("owner")).ctx;
      const list = (await api(owner, "GET", `/api/v1/projects/${S.pid}/members`)).json;
      const rory = (list.items || list.members || []).find((m) => m.email === people.reviewer || (m.user && m.user.email === people.reviewer));
      const r = rory && (await api(owner, "PATCH", `/api/v1/projects/${S.pid}/members`, { user_id: rory.user_id || rory.id || (rory.user && rory.user.id), role: "reader" }));
      if (!r || r.status >= 300) hard("could not make the reviewer a reader: " + (r ? r.status + " " + JSON.stringify(r.json).slice(0, 120) : "not in the member list: " + JSON.stringify(list).slice(0, 160)));
    }
    cur = { page: entry.id, vp: "1280" };
    await load(entry, desk, VP.desk, { scheme: "light" });
    const title = await desk.title();
    if (TITLES.has(title) && TITLES.get(title) !== entry.id && !entry.titleOk) record("title", title, `the title "${title}" is also the title of page ${TITLES.get(title)}`, "");
    if (!TITLES.has(title)) TITLES.set(title, entry.id);
    await structure(entry, desk, "1280 light", false);
    if (QUICK) return;
    await load(entry, desk, VP.desk, { scheme: "dark" });
    await structure(entry, desk, "1280 dark", false);
    cur.vp = "390";
    await load(entry, touch, VP.touch, { scheme: "light" });
    await structure(entry, touch, "390 light", true);
    await load(entry, touch, VP.touch, { scheme: "dark" });
    await structure(entry, touch, "390 dark", true);
    cur.vp = "1280";
    await skipLink(entry, desk, "1280");
    await load(entry, desk, VP.desk, { scheme: "light" });
    const fw = await walk(entry, desk, "1280 light");
    await reverseWalk(entry, desk, "1280 light", fw);
    await load(entry, desk, VP.desk, { scheme: "dark" });
    await walk(entry, desk, "1280 dark", { max: 120 });
    cur.vp = "390";
    await load(entry, touch, VP.touch, { scheme: "dark" });
    await walk(entry, touch, "390 dark");
    cur.vp = "1280";
    if (entry.dialogs) await dialogs(entry, desk, "1280", entry.dialogs);
    if (entry.inspectorTabs) await inspectorTabs(entry, desk, "1280");
    if (entry.write) { await dialogs(entry, touch, "390", ["inspector"]); await editorSemantics(entry, desk, "1280"); await aiFlow(entry, desk, "1280", false); await aiFlow(entry, touch, "390", true); await liveRegions(entry, desk, "1280", (await pageFor(entry.who, "desk")).ctx); }
    await load(entry, desk, VP.desk, { reduced: true });
    await desk.waitForTimeout(500);
    await motion(entry, desk, "reduced motion");
    await load(entry, desk, VP.desk, { forced: true });
    await forcedState(entry, desk, "forced colors");
    await walk(entry, desk, "forced colors", { forced: true, max: 70 });
    cur.vp = "320";
    await load(entry, desk, { width: 320, height: 640 });
    await reflow(entry, desk, "320 px", "at 320 px wide (400% of 1280)");
    cur.vp = "640";
    await load(entry, desk, { width: 640, height: 800 });
    await reflow(entry, desk, "640 px", "at 640 px wide (200% of 1280)");
    cur.vp = "1280";
    await load(entry, desk, VP.desk);
    await text200(entry, desk, "text 200%");
    await load(entry, desk, VP.desk);
    await textSpacing(entry, desk, "text spacing");
  });
}

// ---------------------------------------------------------------------------------------------- report
const allowed = (v) => ALLOW.find((a) => a.rule === v.rule && a.sel.test(v.sel) && (!a.page || [...v.pages].some((p) => a.page.test(p))));
const list = [...VIOL.values()].map((v) => ({ ...v, pages: [...v.pages], allow: allowed(v) }));
const open = list.filter((v) => !v.allow), excused = list.filter((v) => v.allow);
const byRule = {};
for (const v of open) (byRule[v.rule] = byRule[v.rule] || []).push(v);
console.log("\n==== accessibility violations (one line per element and message; `on` lists where it was seen) ====");
for (const [rule, vs] of Object.entries(byRule).sort((a, b) => b[1].length - a[1].length)) {
  console.log(`\n[${rule}] ${vs.length}`);
  for (const v of vs.slice(0, MAXEX)) console.log(`  ${v.sel}\n      ${v.msg}\n      on: ${v.pages.slice(0, 4).join(", ")}${v.pages.length > 4 ? ` and ${v.pages.length - 4} more` : ""}`);
  if (vs.length > MAXEX) console.log(`  ... ${vs.length - MAXEX} more (--max N, or --json FILE for all)`);
}
if (excused.length) { console.log(`\n${excused.length} violation(s) excused by the allow list:`); for (const v of excused) console.log(`  [${v.rule}] ${v.sel}: ${v.allow.why}`); }
if (NOTES.length) console.log("\nscript notes:\n  " + NOTES.join("\n  "));
if (JSONOUT) writeFileSync(JSONOUT, JSON.stringify({ open, excused, notes: NOTES }, null, 1));
const stepFail = kit.results.filter((r) => !r.ok);
for (const r of stepFail) console.log(`STEP FAILED ${r.name}: ${r.err}`);
for (const f of kit.findings) console.log("FINDING " + f);
if (HARD.length) console.log(`\n${HARD.length} part(s) of the audit could not run (these fail the run):\n  ` + HARD.join("\n  "));
console.log(`\ne2e-a11y: ${open.length} violation(s) in ${Object.keys(byRule).length} rule(s), ${excused.length} excused, ${kit.results.length - 2} pages walked`);
await kit.browser.close(); await kit.srv.stop(); if (DW) await DW.srv.stop(); await fake.close(); await gitHost.stop().catch(() => {}); rm(kit.tmp);
process.exit(REPORT_ONLY ? 0 : open.length || stepFail.length || kit.findings.length || HARD.length ? 1 : 0);
