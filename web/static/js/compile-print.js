/* Scríobh compile page, print. The proof: each page compile.preview placed, drawn as an SVG of the trim size with the text
   block dashed and every line at the layout's own position and size; the breaks and the places are the file's, the
   letterforms are the browser's serif, and a line the browser sets wider than the file's is squeezed to its measure. The
   facts a printer asks about, and a proof checklist read from the warnings. The paperback cover: the form that asks the
   server to measure and draw it (htmx), and the downloads of export.cover through the page's download route.
   compile.js hands its helpers on root.cmp; this file answers on root.cmpPrint. Everything is set as text or as an SVG
   attribute: no style attribute, no markup. */
(function () {
  "use strict";
  var doc = document, win = window, root = doc.getElementById("cmp");
  if (!root || !root.cmp) return;
  var C = root.cmp, T = C.T, el = C.el, NS = "http://www.w3.org/2000/svg", last = null;
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

  // ---- the cover -------------------------------------------------------------------------------------------------------------
  // The server measures it and draws it (htmx swaps the answer into #cv-out, in words of the catalogue); this adds what the
  // server cannot know, the preset and its options, says the answer, and measures again when a setting changes.
  var sec = $("#cmp-cover"), form = $("#cmp-cv-form"), thick = $("#cv-thick"), out = $("#cv-out");
  function onFormat(f) { sec.hidden = f !== "pdf"; $("#cmp-coverpage").hidden = f === "pdf"; }
  root.addEventListener("cmp:format", function (e) { onFormat(e.detail); });
  onFormat(C.format());
  function fields(part) {
    return { page_thickness_in: thick.value.trim(), page_count: $("#cv-pages").value.trim(), bleed_in: $("#cv-bleed").value.trim(), profile: $("#cv-profile").value, guides: $("#cv-guides").checked ? "1" : "", barcode: $("#cv-barcode").checked ? "1" : "", part: part || "" };
  }
  form.addEventListener("htmx:configRequest", function (e) { var b = C.request(); e.detail.parameters.preset = b.preset; e.detail.parameters.options = JSON.stringify(b.options); });
  var auto = false; // the last request was a change somewhere else, not the person's own Measure
  function gone() { var a = out.firstElementChild; if (a && !a.querySelector(".cmp-gone")) a.insertBefore(el("p", "cmp-warn cmp-gone", T("cv_pic_gone", "A picture of your cover can't be loaded. Choose it again on the Cover page.")), a.firstChild).setAttribute("role", "alert"); }
  out.addEventListener("htmx:afterSwap", function () {
    var a = out.firstElementChild || {}, d = a.dataset || {}, f = d.focus && $("#" + d.focus), mine = !auto;
    auto = false; out.hidden = !a.tagName;
    ["cv-thick", "cv-pages", "cv-bleed"].forEach(function (i) { $("#" + i).removeAttribute("aria-invalid"); });
    $("#cv-download").classList.toggle("solid", !!a.tagName && !d.bad); $("#cv-measure").classList.toggle("solid", !a.tagName || !!d.bad); // once measured, the file for the printer is the main action
    if (!d.bad) $("#cv-proof").hidden = $("#cv-proof-h").hidden = !d.proof; // a proof is of the project's own cover
    C.say(d.bad ? "" : d.say || "", false); // a refusal is the alert inside the answer: said once
    // the shell moves focus to a field marked invalid once a swap settles: the mark waits until then after a measure the person did not ask for
    if (f) { if (mine) { f.setAttribute("aria-invalid", "true"); f.focus(); } else out.addEventListener("htmx:afterSettle", function () { win.setTimeout(function () { f.setAttribute("aria-invalid", "true"); }, 0); }, { once: true }); }
    [].forEach.call(out.querySelectorAll("image"), function (i) { i.addEventListener("error", gone); });
  });
  form.addEventListener("htmx:sendError", function () { C.say(T("offline", "Could not reach the server."), true); });
  form.addEventListener("htmx:responseError", function () { C.say(T("failed", "That did not work. Try again."), true); });
  // a change to the cover's own settings, the options or the preset measures again, when it has been measured
  var again = 0;
  root.addEventListener("change", function (e) {
    if (sec.hidden || out.hidden || !thick.value.trim() || !(form.contains(e.target) || e.target.closest("#cmp-opts") || e.target.id === "cmp-preset")) return;
    win.clearTimeout(again); // one measure for the changes of one moment
    again = win.setTimeout(function () { if (!sec.hidden) { auto = true; win.htmx.trigger(form, "submit"); } }, 60);
  });
  [["cv-download", "cover"], ["cv-front", "front"], ["cv-proof", "proof"]].forEach(function (b) {
    $("#" + b[0]).addEventListener("click", function () {
      if (thick.value.trim()) C.file("cover", fields(b[1] === "cover" ? "" : b[1]));
      else win.htmx.trigger(form, "submit"); // the answer says what is missing, in words, and where
    });
  });
})();
