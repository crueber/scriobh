/* Scríobh cover page (lazy: this page only). The page is one ordinary form that works without this file; this adds
   three things. A picture chosen or dropped on a drop zone is sent at once (the form is submitted with its "add the
   picture" button, so the server answers as it would to the form: the picture added, nothing saved). The nine-point
   position grid also answers ArrowUp and ArrowDown. And while a front is a page, the preview is redrawn as the controls
   change, by the arithmetic of the server's own placing (coverlayout.PlacePage, in js: the same rounding), as an SVG
   whose attributes carry the numbers: the CSP and the audit allow no style attribute, and CSS object-fit cannot say a
   scale, an actual size or a margin. */
(function () {
  "use strict";
  var doc = document, win = window;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  var root = $("#cover"), f = $("#cv-form");
  if (!root) return;
  var M = {};
  try { M = JSON.parse(root.dataset.msg || "{}"); } catch (e) { /* the words are optional: the page still works */ }
  var live = $("#cv-live");
  function say(t) { if (live) { live.textContent = ""; win.setTimeout(function () { live.textContent = t; }, 30); } }
  var msg = $("#cv-msg");
  if (msg) msg.focus();
  if (!f) return;

  // ---- a picture chosen or dropped is sent at once -------------------------------------------------------
  var up = $("#cv-upload");
  function send(inp, pre) {
    var file = inp.files && inp.files[0];
    if (!file) return;
    say(((pre ? pre + " " : "") + (M.sending || "")).replace(/\{name\}/g, file.name));
    if (f.requestSubmit) f.requestSubmit(up); else { up.hidden = false; up.click(); }
  }
  $$("input[type=file]", f).forEach(function (inp) { inp.addEventListener("change", function () { send(inp); }); });
  function files(e) { return e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types || [], "Files") >= 0; }
  // a file dropped anywhere else on the page must not make the browser leave the form for the picture
  doc.addEventListener("dragover", function (e) {
    if (!files(e)) return;
    e.preventDefault();
    if (!(e.target.closest && e.target.closest("[data-drop]"))) e.dataTransfer.dropEffect = "none";
  });
  doc.addEventListener("drop", function (e) { if (files(e)) e.preventDefault(); });
  $$("[data-drop]", f).forEach(function (z) {
    var inp = $("input[type=file]", z), note = $(".cv-dropnote", z);
    if (note) note.hidden = false;
    ["dragenter", "dragover"].forEach(function (ev) { z.addEventListener(ev, function (e) { if (files(e)) { e.preventDefault(); z.classList.add("is-over"); } }); });
    z.addEventListener("dragleave", function (e) { if (!z.contains(e.relatedTarget)) z.classList.remove("is-over"); });
    z.addEventListener("drop", function (e) {
      z.classList.remove("is-over");
      if (!files(e)) return;
      e.preventDefault();
      var fl = e.dataTransfer.files;
      if (!fl.length) return;
      // a browser that cannot build a file list says so, and the file button still works
      try {
        var dt = new win.DataTransfer();
        dt.items.add(fl[0]);
        inp.files = dt.files;
      } catch (x) { say(M.no_drop || ""); return; }
      send(inp, fl.length > 1 ? M.one_only : M.dropped); // one announcement: the file taken, and that it is being sent
    });
  });

  // ---- the position grid: rows as well as columns ----------------------------------------------------------
  f.addEventListener("keydown", function (e) {
    var r = e.target;
    if (r.type !== "radio" || !/_pos$/.test(r.name) || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
    e.preventDefault();
    var rs = $$("input[name=" + r.name + "]", f), n = rs[rs.indexOf(r) + (e.key === "ArrowUp" ? -3 : 3)];
    if (n) { n.focus(); n.checked = true; n.dispatchEvent(new win.Event("input", { bubbles: true })); }
  });

  // ---- the front, drawn as it will be --------------------------------------------------------------------------
  var NS = "http://www.w3.org/2000/svg", front = $("#cv-front"), box = $("#cv-pv-page .cv-frame"), cap = $("#cv-pv-cap");
  function mk(n, a) { var e = doc.createElementNS(NS, n); for (var k in a) e.setAttribute(k, a[k]); return e; }
  function rp(v) { return Math.floor(v + 0.5 + 1e-9); }
  function num(id, int) { var v = $(id).value.trim(); return v === "" || (int && !/^\d+$/.test(v)) ? NaN : +v; }
  function cls(m) { return m ? m[0] : ""; }
  function val(n) { var c = $("input[name=" + n + "]:checked", f); return c ? c.value : ""; }
  function place(pw, ph, tw, th, o) {
    var cw = th >= tw ? rp(2560 * tw / th) : 2560, ch = th >= tw ? 2560 : rp(2560 * th / tw), ppi = cw / tw, m = rp(o.margin * ppi), bw = cw - 2 * m, bh = ch - 2 * m;
    if (bw <= 0 || bh <= 0) return null;
    var k = o.fit === "cover" ? Math.max(bw / pw, bh / ph) : o.fit === "actual" ? ppi / 300 : Math.min(bw / pw, bh / ph);
    if (k > 1 && o.fit !== "actual") k = 1; // never enlarged: a picture pixel is at most a page pixel
    k *= o.scale / 100;
    var dw = pw * k, dh = ph * k, p = o.pos;
    if (dw > 8 * cw || dh > 8 * ch) return null; // cover_picture_extreme: too far from the page's proportions
    var fx = /right/.test(p) ? 1 : /left/.test(p) ? 0 : 0.5, fy = /^top/.test(p) ? 0 : /^bottom/.test(p) ? 1 : 0.5;
    var x0 = m + (bw - dw) * fx, y0 = m + (bh - dh) * fy;
    var r = { cw: cw, ch: ch, m: m, bw: bw, bh: bh, x: rp(x0), y: rp(y0), w: rp(x0 + dw) - rp(x0), h: rp(y0 + dh) - rp(y0) };
    return r.w < 1 || r.h < 1 ? null : r; // under a pixel: the same refusal
  }
  function draw() {
    if (!front || !box || !front.dataset.url || !$("#cv-shape-sides").checked) return;
    var mode = val("front_mode"), gen = $(".cv-generated", box), sel = $("#cv-trim").selectedOptions[0];
    $$(".cv-svg, img", box).forEach(function (n) { n.remove(); });
    box.dataset.trim = $("#cv-trim").value;
    if (mode !== "image" && mode !== "page") { gen.hidden = false; return; }
    var pw = +front.dataset.pw, ph = +front.dataset.ph, tw = +sel.dataset.w, th = +sel.dataset.h, a = { href: front.dataset.url }, svg, bg = cls(box.className.match(/cover-\d/));
    if (mode === "image") {
      var c = th >= tw ? [rp(2560 * tw / th), 2560] : [2560, rp(2560 * th / tw)];
      svg = mk("svg", { "class": "cv-svg " + bg, viewBox: "0 0 " + c[0] + " " + c[1] });
      svg.append(mk("rect", { "class": "cv-bg", width: c[0], height: c[1] }), mk("image", { href: a.href, width: c[0], height: c[1], preserveAspectRatio: "xMidYMid slice" }));
    } else {
      var sc = num("#cv-front-scale", 1), mg = num("#cv-front-margin");
      // what Save refuses is not drawn: a scale outside 10 to 100 (never read as 100), a margin outside 0 to 1.5
      var p = sc >= 10 && sc <= 100 && mg >= 0 && mg <= 1.5 ? place(pw, ph, tw, th, { fit: val("front_fit"), scale: sc, margin: mg, pos: val("front_pos") || "center" }) : null;
      if (!p) { gen.hidden = true; if (cap && M.refused) cap.textContent = M.refused; return; }
      var chip = $("input[name=front_bg]:checked + .chip", f), clip = mk("clipPath", { id: "cv-clip" });
      clip.append(mk("rect", { x: p.m, y: p.m, width: p.bw, height: p.bh }));
      svg = mk("svg", { "class": "cv-svg " + cls(chip ? chip.className.match(/cover-\d/) : bg), viewBox: "0 0 " + p.cw + " " + p.ch });
      svg.append(mk("rect", { "class": "cv-bg", width: p.cw, height: p.ch }), clip, mk("image", { href: a.href, x: p.x, y: p.y, width: p.w, height: p.h, preserveAspectRatio: "none", "clip-path": "url(#cv-clip)" }));
    }
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", $("#cv-alt").value);
    gen.hidden = true;
    box.insertBefore(svg, gen);
    if (cap && M.draft) cap.textContent = M.draft;
  }
  f.addEventListener("input", draw);
  f.addEventListener("change", draw);
})();
