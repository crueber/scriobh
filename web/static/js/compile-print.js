/* Scríobh compile page, print. The proof: each page compile.preview placed, drawn as an SVG of the trim size with the text
   block dashed and every line at the layout's own position and size; the breaks and the places are the file's, the
   letterforms are the browser's serif, and a line the browser sets wider than the file's is squeezed to its measure. The
   facts a printer asks about, and a proof checklist read from the warnings. The paperback cover: its measurements from
   compile.cover_spec, a flat drawing of the wrap, and the PDF from export.cover through the page's download route.
   compile.js hands its helpers on root.cmp; this file answers on root.cmpPrint. Everything is set as text or as an SVG
   attribute: no style attribute, no markup. */
(function () {
  "use strict";
  var doc = document, root = doc.getElementById("cmp");
  if (!root || !root.cmp) return;
  var C = root.cmp, T = C.T, el = C.el, NS = "http://www.w3.org/2000/svg", look = {}, last = null;
  try { look = JSON.parse(root.dataset.cover || "{}"); } catch (e) { look = {}; }
  function $(s) { return doc.querySelector(s); }
  function svg(tag, at, text) { var e = doc.createElementNS(NS, tag); for (var k in at) e.setAttribute(k, at[k]); if (text != null) e.textContent = text; return e; }
  function r(n, d) { var f = Math.pow(10, d || 2); return Math.round(n * f) / f; }
  function row(dl, k, v) { var d = el("div"); d.appendChild(el("dt", null, k)); d.appendChild(el("dd", null, String(v))); dl.appendChild(d); }
  function size(w, h) { return T("p_in", "{w} × {h} in", { w: w, h: h }); }

  // ---- the proof --------------------------------------------------------------------------------------------------------
  function show(p) {
    last = p;
    var g = p.print, dl = $("#cmp-pfacts"), has = {};
    dl.textContent = "";
    row(dl, T("p_trim", "Trim size"), size(r(g.page_width_pt / 72), r(g.page_height_pt / 72)));
    row(dl, T("p_pages", "Pages"), T("p_leaves", "{n}, on {l} leaves", { n: p.page_count, l: g.leaves }));
    row(dl, T("p_roman", "Front matter, numbered in roman"), g.front_pages);
    row(dl, T("p_block", "Text block"), size(r(g.text_width_pt / 72), r(g.text_height_pt / 72)));
    row(dl, T("p_inside", "Inside margin with the gutter"), T("p_inch", "{n} in", { n: r(g.inside_margin_pt / 72, 3) }));
    row(dl, T("p_type", "Type"), T("p_type_v", "{s} pt on {l} pt, {n} lines a page", { s: r(g.font_size_pt, 1), l: r(g.leading_pt, 1), n: g.lines_per_page }));
    (p.warnings || []).forEach(function (w) { has[w.code] = 1; });
    var ul = $("#cmp-check");
    ul.textContent = "";
    [[p.page_count % 2 === 0, T("c_even", "The page count is even."), T("c_odd", "The page count is odd: printers print leaves of two pages.")],
      [p.page_count >= 24, T("c_many", "24 pages or more, as print-on-demand printers commonly ask."), T("c_few", "Under 24 pages: many printers ask for more.")],
      [!has.gutter_small, T("c_gutter", "The inside margin suits the page count."), T("c_gutter_bad", "The inside margin is small for this many pages: widen the gutter.")],
      [!has.image_low_resolution, T("c_pics", "No picture prints under 300 dpi."), T("c_pics_bad", "A picture prints under 300 dpi: see the warnings.")],
      [!has.missing_glyph && !has.script_unsupported, T("c_glyphs", "Every character is in the embedded fonts."), T("c_glyphs_bad", "Some characters are not in the embedded fonts: see the warnings.")],
      [true, T("c_fonts", "The fonts are embedded in the file.")],
      [null, T("c_embed", "Whether every picture could be put in the PDF is known only when the file is made: after Download, the warnings in the file are counted.")],
      [null, T("c_proof", "Order a printed proof before the print run, and check the cover against your printer's template.")]
    ].forEach(function (c) {
      var li = el("li", c[0] === null ? "" : c[0] ? "is-ok" : "is-bad");
      li.appendChild(el("span", "cmp-mark", c[0] === null ? "○" : c[0] ? "✓" : "!")).setAttribute("aria-hidden", "true");
      li.appendChild(el("span", null, c[0] === false ? c[2] : c[1]));
      ul.appendChild(li);
    });
  }
  function pos(pg) {
    return T("p_pos", "Page {n} of {m}, numbered {label}, {side}", { n: pg.number, m: last.page_count, label: pg.label || "–", side: pg.recto ? T("recto", "right-hand") : T("verso", "left-hand") });
  }
  function page(pg, art) {
    var g = last.print, W = g.page_width_pt, H = g.page_height_pt, tw = g.text_width_pt, x0 = pg.recto ? g.inside_margin_pt : g.outside_margin_pt, end = x0 + tw;
    var s = svg("svg", { viewBox: "0 0 " + W + " " + H, width: r(W * 4 / 3), height: r(H * 4 / 3), "class": "cmp-sheet", focusable: "false" });
    art.setAttribute("aria-label", pos(pg));
    art.appendChild(s);
    s.appendChild(svg("rect", { "class": "pg", x: 0, y: 0, width: W, height: H }));
    s.appendChild(svg("rect", { "class": "tb", x: x0, y: g.top_margin_pt, width: tw, height: g.text_height_pt, "vector-effect": "non-scaling-stroke" }));
    if (pg.blank) s.appendChild(svg("text", { "class": "blank", x: W / 2, y: H / 2, "text-anchor": "middle", "font-size": g.font_size_pt }, T("blank", "Blank page")));
    var o = last.options || {}, ls = pg.lines || [], just = o.justify, indent = (o.indent_inches || 0) * 72;
    ls.forEach(function (l, i) {
      if (l.kind === "image") { var iw = Math.max(12, 2 * (x0 + tw / 2 - l.x)); s.appendChild(svg("rect", { "class": "im", x: l.x, y: l.y - l.size, width: iw, height: l.size })); return; }
      var parts = String(l.text).split("\t"), mid = l.kind === "head" || l.kind === "folio";
      var t = svg("text", { "class": "k-" + l.kind, x: l.x, y: l.y, "font-size": l.size, "text-anchor": mid ? "middle" : "start" }, parts[0]);
      s.appendChild(t);
      var room = end - l.x;
      if (parts.length > 1) { s.appendChild(svg("text", { "class": "k-text", x: end, y: l.y, "font-size": l.size, "text-anchor": "end" }, parts[1])); room -= l.size * 2.5; }
      // a justified line of body text that is not a paragraph's last fills the measure, as in the file: it starts at the
      // block's edge or the first-line indent, the next line follows one leading below at the edge, neither is a contents
      // entry, and the browser already sets it near the measure (a quotation, a list or code is inset, short or both).
      // A line the browser sets wider than the measure is squeezed into it.
      var n = ls[i + 1], edge = function (x, d) { return x >= x0 - 0.5 && x <= x0 + d + 0.5; };
      var full = just && parts.length === 1 && l.kind === "text" && n && n.kind === "text" && String(n.text).indexOf("\t") < 0 && Math.abs(n.y - l.y - g.leading_pt) < 1 && edge(l.x, indent) && edge(n.x, 0);
      if (!mid && room > 0) { try { var len = t.getComputedTextLength(), over = len > room; if (over || full && len > room * 0.85) { t.setAttribute("textLength", r(room)); t.setAttribute("lengthAdjust", over ? "spacingAndGlyphs" : "spacing"); } } catch (e) { /* not laid out */ } }
    });
  }
  root.cmpPrint = { show: show, page: page, pos: pos };

  // ---- the cover ------------------------------------------------------------------------------------------------------------
  var sec = $("#cmp-cover"), thick = $("#cv-thick");
  function onFormat(f) { sec.hidden = f !== "pdf"; }
  root.addEventListener("cmp:format", function (e) { onFormat(e.detail); });
  onFormat(C.format());
  function fields() {
    return { page_thickness_in: thick.value.trim(), page_count: $("#cv-pages").value.trim(), bleed_in: $("#cv-bleed").value.trim(), guides: $("#cv-guides").checked ? "1" : "", barcode: $("#cv-barcode").checked ? "1" : "" };
  }
  function ready() {
    if (thick.value.trim()) return true;
    thick.setAttribute("aria-invalid", "true"); thick.focus();
    C.say(T("cv_need", "Give the paper's thickness per page: it is in your printer's cover guide."), true);
    return false;
  }
  thick.addEventListener("input", function () { thick.removeAttribute("aria-invalid"); });
  $("#cmp-cv-form").addEventListener("submit", function (e) {
    e.preventDefault();
    if (!ready()) return;
    var f = fields(), b = C.request();
    b.page_thickness_in = parseFloat(f.page_thickness_in);
    if (f.page_count) b.page_count = parseInt(f.page_count, 10);
    if (f.bleed_in) b.bleed_in = parseFloat(f.bleed_in);
    C.say(T("cv_working", "Measuring the cover…"));
    C.api("POST", "/compile/cover-spec", b).then(function (res) {
      if (!res.ok) { C.say(C.why(res), true); return; }
      draw(res.json, f.barcode === "1");
      C.say(T("cv_ready", "The cover is measured: the spine is {s} in wide.", { s: r(res.json.spine_in, 3) }));
    });
  });
  $("#cv-download").addEventListener("click", function () { if (ready()) C.file("cover", fields()); });

  // words broken into lines of about n characters
  function wrap(text, n) {
    var out = [], line = "";
    text.split(/\s+/).forEach(function (w) { if (line && (line + " " + w).length > n) { out.push(line); line = w; } else line = line ? line + " " + w : w; });
    if (line) out.push(line);
    return out;
  }
  function lines(s, words, x, y, sz, cls) {
    words.forEach(function (w, i) { s.appendChild(svg("text", { "class": cls, x: x, y: y + i * sz * 1.2, "font-size": sz, "text-anchor": "middle" }, w)); });
    return y + words.length * sz * 1.2;
  }
  function draw(c, barcode) {
    var W = c.wrap_width_in * 72, H = c.wrap_height_in * 72, b = c.bleed_in * 72, tw = c.trim_width_in * 72, th = c.trim_height_in * 72, m = tw * 0.12;
    var s = svg("svg", { viewBox: "0 0 " + W + " " + H, "class": "cmp-wrapart", role: "img", "aria-label": T("cv_alt", "The cover laid flat, back, spine and front, {w} × {h} in with the bleed", { w: r(c.wrap_width_in, 3), h: r(c.wrap_height_in, 3) }) });
    s.appendChild(svg("rect", { x: 0, y: 0, width: W, height: H, fill: look.color || "#2F5D50" }));
    var k = s.appendChild(svg("g", { fill: "#FFFFFF" })); // white type on the colour in either theme, as in the file
    // the front: the title in a third of the panel, the subtitle, the author near the foot
    var fx = c.front.x0 + tw / 2, sz = tw * 0.08, title = look.title || T("untitled", "Untitled"), tl;
    for (;;) { tl = wrap(title, Math.max(6, Math.floor((tw - 2 * m) / (sz * 0.52)))); if (tl.length * sz * 1.2 <= th * 0.38 || sz <= tw * 0.035) break; sz *= 0.9; }
    var y = lines(k, tl, fx, b + th * 0.16 + sz, sz, "cv-t");
    if (look.subtitle) lines(k, wrap(look.subtitle, Math.floor((tw - 2 * m) / (tw * 0.02))), fx, y + tw * 0.04, tw * 0.0375, "cv-s");
    if (look.author) k.appendChild(svg("text", { "class": "cv-a", x: fx, y: b + th * 0.88, "font-size": tw * 0.045, "text-anchor": "middle" }, look.author));
    // the back: the kind of book, and the barcode's box
    if (look.kind) k.appendChild(svg("text", { "class": "cv-k", x: c.back.x0 + tw / 2, y: b + th * 0.18, "font-size": tw * 0.023, "text-anchor": "middle" }, look.kind.toUpperCase()));
    if (barcode) s.appendChild(svg("rect", { x: c.back.x1 - m / 2 - 144, y: b + th - m / 2 - 86.4, width: 144, height: 86.4, fill: "#FFFFFF" }));
    // the spine, read top to bottom, when it is wide enough for type
    var sw = c.spine.x1 - c.spine.x0, sx = c.spine.x0 + sw / 2;
    if (c.spine_has_text) k.appendChild(svg("text", { "class": "cv-p", x: sx, y: H / 2, "font-size": Math.min(sw * 0.5, 11), "text-anchor": "middle", "dominant-baseline": "central", transform: "rotate(90 " + sx + " " + H / 2 + ")" }, title + (look.author ? "   " + look.author : "")));
    // the trim and the folds, dashed
    s.appendChild(svg("rect", { "class": "cv-g", stroke: "#FFFFFF", x: b, y: b, width: W - 2 * b, height: th, "vector-effect": "non-scaling-stroke" }));
    [c.spine.x0, c.spine.x1].forEach(function (x) { if (sw > 0) s.appendChild(svg("line", { "class": "cv-g", stroke: "#FFFFFF", x1: x, y1: 0, x2: x, y2: H, "vector-effect": "non-scaling-stroke" })); });
    var art = $("#cv-art"), dl = $("#cv-facts"), wn = $("#cv-warn");
    art.textContent = ""; art.appendChild(s); dl.textContent = ""; wn.textContent = "";
    row(dl, T("cv_pagecount", "Pages"), c.page_count_source === "given" ? T("cv_given", "{n}, as you gave it", { n: c.page_count }) : T("cv_layout", "{n}, from the layout", { n: c.page_count }));
    row(dl, T("cv_spine", "Spine"), T("p_inch", "{n} in", { n: r(c.spine_in, 3) }) + (c.spine_has_text ? "" : " · " + T("cv_notype", "too narrow for type")));
    row(dl, T("cv_wrap", "Full wrap with the bleed"), size(r(c.wrap_width_in, 3), r(c.wrap_height_in, 3)));
    row(dl, T("p_trim", "Trim size"), size(r(c.trim_width_in, 3), r(c.trim_height_in, 3)));
    row(dl, T("cv_bleed", "Bleed"), T("p_inch", "{n} in", { n: r(c.bleed_in, 3) }));
    (c.warnings || []).forEach(function (w) { wn.appendChild(el("p", "cmp-warn", w.message)); });
    $("#cv-out").hidden = false;
  }
})();
