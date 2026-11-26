/* Scríobh compile page. The page does not know what a preset is made of: the presets with every option resolved, and each
   option's type and allowed values, are handed over by the server from the ops (compile.preset.list and the schema of
   compile.preview), and the form is built from them, so an option or a format an op adds appears without a change here.
   It asks compile.preview, draws the answer (title page, running header, outline, warnings, what was left out, the pages
   as blocks, and placed lines when the preview has them: the shape is detected, not assumed), keeps custom presets
   (compile.preset.*), and downloads through UI routes that return the file with its manifest summary in headers.
   The print proof and the cover are compile-print.js, which this file hands what it needs on root.cmp and asks through
   root.cmpPrint when a preview has placed pages. An option names the formats it is for; the others are not shown.
   Strings come from the page (data-msg); the English here is the fallback. Everything is set as text. */
(function () {
  "use strict";
  var doc = document, win = window, root = doc.getElementById("cmp");
  if (!root) return;
  var project = root.dataset.project, csrf = root.dataset.csrf, M = {}, presets = [], descs = [], cur = null, vals = {}, last = null, page = 0, pages = [], printed = false, pdc = root.dataset.pandoc === "true", PFMT = ["odt", "rtf", "latex", "rst", "mediawiki"], chap = +root.dataset.chapters || 0;
  try { M = JSON.parse(doc.getElementById("cmp-msg").dataset.msg || "{}"); presets = (JSON.parse(root.dataset.presets).presets) || []; descs = JSON.parse(root.dataset.options) || []; } catch (e) { M = M || {}; }
  function T(k, d, v) { var s = M[k] || d; if (v) for (var n in v) s = s.split("{" + n + "}").join(v[n]); return s; }
  function $(s, r) { return (r || doc).querySelector(s); }
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function human(k) { return k.replace(/_/g, " ").replace(/^./, function (c) { return c.toUpperCase(); }); }
  var status = $("#cmp-status");
  function say(m, bad) { status.textContent = m; status.className = "cmp-status" + (bad ? " is-bad" : ""); }
  function api(method, path, body) {
    var o = { method: method, credentials: "same-origin", headers: { Accept: "application/json" } };
    if (body !== undefined) { o.headers["Content-Type"] = "application/json"; o.body = JSON.stringify(body); }
    return fetch("/api/v1/projects/" + project + path, o).then(function (r) { return r.text().then(function (t) { var j = null; try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ } return { ok: r.ok, status: r.status, json: j }; }); }, function () { return { ok: false, status: 0, json: null }; });
  }
  function why(r) { return (r.json && r.json.error && r.json.error.message) || (r.status === 0 ? T("offline", "Could not reach the server.") : T("failed", "That did not work. Try again.")); }
  // a built-in preset is named in the person's language where the catalogue has its name; their own keep the name they gave
  function pname(p) { return p && p.builtin ? T("preset_" + p.id, p.name) : p ? p.name : ""; }
  // a count with its noun: "1 document", "2 documents" (the message for one has the key and a 1)
  function NN(k, d1, d, n, shown) { return n === 1 ? T(k + "1", d1, { n: shown }) : T(k, d, { n: shown }); }
  function byId(id) { return presets.filter(function (p) { return p.id === id; })[0]; }

  // ---- presets -------------------------------------------------------------------------------------------------------
  var sel = $("#cmp-preset");
  function drawPresets(pick) {
    sel.textContent = "";
    [[true, T("builtin", "Built in")], [false, T("custom", "Your own")]].forEach(function (g) {
      var list = presets.filter(function (p) { return !!p.builtin === g[0]; });
      if (!list.length) return;
      var og = el("optgroup"); og.label = g[1];
      list.forEach(function (p) { var o = el("option", null, pname(p)); o.value = p.id; og.appendChild(o); });
      sel.appendChild(og);
    });
    choose(pick || (byId("shunn-us") ? "shunn-us" : presets[0] && presets[0].id));
  }
  function choose(id) {
    cur = byId(id); if (!cur) return;
    sel.value = id; vals = JSON.parse(JSON.stringify(cur.options || {}));
    $("#cmp-desc").textContent = cur.description || "";
    $("#cmp-update").hidden = $("#cmp-delete").hidden = !!cur.builtin;
    buildForm(); formatChanged();
  }
  function fmtNow() { return vals.format || (cur && cur.format) || ""; }
  // the summary line above the fold: the kind of file, the preset, the chapters (the outline's count once a preview has one)
  function what() { var f = fmtNow(); $("#cmp-what").textContent = [f && T("v_format_" + f, f), cur && pname(cur), NN("n_chapters", "{n} chapter", "{n} chapters", chap, chap)].filter(Boolean).join(" · "); }
  // the buttons and notes that depend on the kind of file: a bundle holds a Word file; print has its proof and its cover
  function formatChanged() {
    var f = fmtNow();
    what();
    $("#cmp-submit").disabled = f !== "docx";
    $("#cmp-subnote").hidden = f !== "docx"; $("#cmp-printnote").hidden = f !== "pdf";
    root.dispatchEvent(new CustomEvent("cmp:format", { detail: f }));
  }
  sel.addEventListener("change", function () { choose(sel.value); });

  // ---- the options form, from what the ops say ----------------------------------------------------------------------------
  var GROUPS = ["output", "page", "margins", "title", "front", "header", "chapters", "scenes", "include", "more"];
  function buildForm() {
    var box = $("#cmp-opts"); box.textContent = "";
    var seen = {}, f = fmtNow();
    descs.forEach(function (d) { if (!d.formats || d.formats.indexOf(f) >= 0) (seen[d.group] = seen[d.group] || []).push(d); });
    GROUPS.forEach(function (g) {
      if (!seen[g]) return;
      var fs = el("fieldset", "cmp-group"), lg = el("legend", null, T("g_" + g, human(g)));
      fs.appendChild(lg);
      seen[g].forEach(function (d) { fs.appendChild(control(d)); });
      box.appendChild(fs);
    });
  }
  function control(d) {
    var id = "o-" + d.key, w = el("div", "field"), v = vals[d.key], lab = T("o_" + d.key, human(d.key)), hint = M["h_" + d.key], c;
    if (d.type === "boolean") {
      var l = el("label", "check"); c = el("input"); c.type = "checkbox"; c.id = id; c.checked = (v === true || v == null && !!d.on) && !d.off; c.disabled = !!d.off;
      c.addEventListener("change", function () { vals[d.key] = c.checked; });
      l.appendChild(c); l.appendChild(el("span", null, lab)); w.appendChild(l);
    } else {
      var lb = el("label", null, lab); lb.htmlFor = id; w.appendChild(lb);
      if (d.enum && d.enum.length) {
        // The Pandoc formats are offered only while the bridge is on (the server says so); a preset that already uses one keeps it, marked.
        var en = d.enum.filter(function (e) { return d.key !== "format" || pdc || PFMT.indexOf(e) < 0 || e === v; });
        c = el("select"); en.forEach(function (e) { var nm = T("v_" + d.key + "_" + e, human(e)), o = el("option", null, d.key === "format" && !pdc && PFMT.indexOf(e) >= 0 ? T("v_format_needs", "{name} (Pandoc is switched off)", { name: nm }) : nm); o.value = e; c.appendChild(o); });
        c.value = v == null ? en[0] : v;
        if (d.key === "format" && PFMT.indexOf(c.value) >= 0) hint = T("pandoc_note", "This file is made by Pandoc on this server from the compile Markdown. The same text gives the same file only with the same Pandoc version, which is recorded with the export.");
        c.addEventListener("change", function () { vals[d.key] = c.value; if (d.key === "format") { buildForm(); formatChanged(); $("#o-format").focus(); } });
      } else if (d.type === "number" || d.type === "integer") {
        c = el("input"); c.type = "number"; c.step = "any"; c.inputMode = "decimal"; c.value = v == null ? "" : v;
        c.addEventListener("input", function () { vals[d.key] = c.value === "" ? 0 : parseFloat(c.value); });
      } else {
        c = el(d.long ? "textarea" : "input"); if (d.long) c.rows = 4; else c.type = "text";
        c.maxLength = d.long ? 600 : 200; c.value = v == null ? "" : v; c.addEventListener("input", function () { vals[d.key] = c.value; });
      }
      c.id = id; w.appendChild(c);
    }
    if (hint) { var h = el("span", "hint", hint); h.id = id + "-h"; w.appendChild(h); c.setAttribute("aria-describedby", id + "-h"); }
    return w;
  }
  // What differs from the preset, as an op takes it: changed options, and a text option emptied goes in clear.
  function diff(base) {
    var o = {}, clear = [], b = (base && base.options) || {};
    descs.forEach(function (d) {
      var v = vals[d.key], bv = b[d.key];
      if (v === undefined || v === bv) return;
      if (d.type === "string" && !d.enum && v === "") { if (bv) clear.push(d.key); return; }
      if (d.type === "string" && !d.enum && !v && !bv) return;
      o[d.key] = v;
    });
    if (clear.length) o.clear = clear;
    return o;
  }

  // ---- custom presets ----------------------------------------------------------------------------------------------------
  var nb = $("#cmp-namebox");
  $("#cmp-save-new").addEventListener("click", function () { nb.hidden = false; $("#cmp-name").value = ""; $("#cmp-name").focus(); });
  $("#cmp-name-cancel").addEventListener("click", function () { nb.hidden = true; $("#cmp-save-new").focus(); });
  nb.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); $("#cmp-name-cancel").click(); } });
  nb.addEventListener("submit", function (e) {
    e.preventDefault();
    var name = $("#cmp-name").value.trim();
    if (!name) { say(T("need_name", "Give the preset a name."), true); return; }
    var base = cur.builtin ? cur : byId(cur.base) || cur;
    api("POST", "/compile/presets", { name: name, base: base.id, options: diff(base) }).then(function (r) {
      if (!r.ok) { say(why(r), true); return; }
      presets.push(r.json.item); nb.hidden = true; drawPresets(r.json.item.id); say(T("saved", "Preset saved: {name}.", { name: name })); sel.focus();
    });
  });
  $("#cmp-update").addEventListener("click", function () {
    api("PATCH", "/compile/presets/" + cur.id, { options: diffAll() }).then(function (r) {
      if (!r.ok) { say(why(r), true); return; }
      presets[presets.indexOf(cur)] = r.json.item; drawPresets(r.json.item.id); say(T("updated", "Preset updated."));
    });
  });
  function diffAll() { var o = {}; descs.forEach(function (d) { var v = vals[d.key]; if (v !== undefined) o[d.key] = v; }); return o; }
  $("#cmp-delete").addEventListener("click", function (e) {
    var which = cur, from = e.currentTarget;
    win.scriobh.confirm(from.parentNode, { msg: T("confirm_delete", "Delete the preset “{name}”? Files you already made from it are not affected.", { name: pname(which) }), ok: T("confirm_delete_ok", "Delete the preset"), cancel: T("confirm_cancel", "Cancel"), from: from, onOk: function () {
      api("DELETE", "/compile/presets/" + which.id).then(function (r) {
        if (!r.ok) { say(why(r), true); return; }
        presets = presets.filter(function (p) { return p !== which; }); drawPresets(); say(T("deleted", "Preset deleted.")); sel.focus();
      });
    } });
  });

  // ---- the preview ----------------------------------------------------------------------------------------------------------
  function request() { return { preset: cur.id, options: diff(cur) }; }
  function tag(text, cls) { return el("span", "cmp-tag" + (cls ? " " + cls : ""), text); }
  $("#cmp-preview").addEventListener("click", function () {
    var b = request(); b.pages = parseInt($("#cmp-show").value, 10) || 3;
    say(T("working", "Working out the preview…"));
    api("POST", "/compile/preview", b).then(function (r) {
      if (!r.ok) { say(why(r), true); return; }
      last = r.json; show(last);
      var nw = (last.warnings || []).length;
      say(nw ? NN("ready_w", "The preview is ready, with {n} warning.", "The preview is ready, with {n} warnings.", nw, nw) : T("ready", "The preview is ready."));
    });
  });
  function show(p) {
    $("#cmp-empty").hidden = true; $("#cmp-out").hidden = false;
    chap = (p.outline || []).filter(function (o) { return o.kind === "chapter"; }).length; what();
    var pr = !!(p.print && root.cmpPrint);
    $("#cmp-pfacts").hidden = $("#cmp-proofnote").hidden = $("#cmp-check-d").hidden = !pr;
    if (pr) root.cmpPrint.show(p);
    var pc = p.page_count || (p.pages || []).length, est = p.page_estimated !== false && !p.print;
    var sum = [pname(byId(p.preset)) || p.preset_name || p.preset, (p.format || "").toUpperCase(), NN("n_words", "{n} word", "{n} words", p.words, fmt(p.words)), NN("n_docs", "{n} document", "{n} documents", p.documents, p.documents)];
    if (pc) sum.push(est ? T("about_pages", "about {n} pages (an estimate: the program that opens the file decides)", { n: pc }) : NN("n_pages", "{n} page", "{n} pages", pc, pc));
    $("#cmp-sum").textContent = sum.join(" · ");
    var nt = $("#cmp-notices"); nt.textContent = "";
    var tp = p.title_page;
    if (tp && tp.layout && tp.layout !== "none" && !pr) { // a print proof shows its title page as a page
      var t = el("div", "cmp-titlepage"); t.appendChild(el("p", "cmp-tp-t", tp.title));
      if (tp.subtitle) t.appendChild(el("p", null, tp.subtitle)); if (tp.byline) t.appendChild(el("p", null, tp.byline));
      if (tp.word_count) t.appendChild(el("p", "fine", tp.word_count));
      (tp.contact || []).forEach(function (l) { t.appendChild(el("p", "fine", l)); });
      if (p.running_header) t.appendChild(el("p", "fine", T("running", "Running header: {h}", { h: p.running_header })));
      nt.appendChild(t);
    }
    (p.warnings || []).forEach(function (w) { nt.appendChild(el("p", "cmp-warn", (M["w_" + w.code] ? M["w_" + w.code] + " " : "") + w.message + (w.title ? " (" + w.title + ")" : ""))); });
    if (p.switched_off) nt.appendChild(el("p", "cmp-warn", T("switched_off", "{n} documents are left out because this preset switches front or back matter off.", { n: p.switched_off })));
    if (p.notes_excluded && p.notes_excluded.documents) nt.appendChild(el("p", "cmp-warn", T("notes_out", "{n} notes ({w} words) are not in the file.", { n: p.notes_excluded.documents, w: fmt(p.notes_excluded.words) })));
    var ol = $("#cmp-outline"); ol.textContent = "";
    (p.outline || []).forEach(function (o) {
      var li = el("li", "cmp-o" + (o.level ? " l" + Math.min(o.level, 3) : "")); li.appendChild(el("span", null, o.heading || o.title || ""));
      li.appendChild(el("span", "cmp-n", (o.words ? T("n_words", "{n} words", { n: fmt(o.words) }) : "") + (o.page ? " · " + T("page_n", "page {n}", { n: o.page }) : "")));
      ol.appendChild(li);
    });
    var ex = $("#cmp-excl"); ex.textContent = ""; var n = p.excluded_total || (p.excluded || []).length;
    $("#cmp-excl-s").textContent = T("excluded", "Left out of the file: {n}", { n: n });
    $("#cmp-excl-d").hidden = !n;
    (p.excluded || []).forEach(function (x) { ex.appendChild(el("li", null, (x.title || T("untitled", "Untitled")) + " — " + (M["r_" + x.reason] || x.reason))); });
    // the pages: placed lines when the preview has them, else blocks
    printed = !!(p.print && p.print.pages && p.print.pages.length);
    pages = printed ? p.print.pages : p.pages || [];
    page = 0; draw();
    $("#cmp-r-h").focus();
  }
  function fmt(n) { return String(n || 0).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }
  function draw() {
    var paper = $("#cmp-paper"); paper.textContent = "";
    $("#cmp-prev").disabled = page <= 0; $("#cmp-next").disabled = page >= pages.length - 1;
    if (!pages.length) { paper.appendChild(el("p", "fine", T("no_pages", "There are no pages to show for this format."))); $("#cmp-pos").textContent = ""; return; }
    var pg = pages[page], art = el("article", "cmp-page" + (pg.title_page ? " is-title" : "")); art.setAttribute("aria-label", T("page_of", "Page {n} of {m}", { n: pg.number || page + 1, m: pages.length }));
    var cp = printed && root.cmpPrint;
    paper.appendChild(art);
    if (cp) { art.className = "cmp-page is-sheet"; cp.page(pg, art); $("#cmp-pos").textContent = cp.pos(pg); return; }
    if (pg.header) art.appendChild(el("p", "cmp-ph", pg.header));
    (pg.blocks || []).forEach(function (b) { art.appendChild(el("p", "cmp-b k-" + (b.kind || "paragraph") + (b.indent ? " is-indent" : "") + (b.continues ? " is-cont" : ""), b.text || "")); });
    (pg.lines || []).forEach(function (l) { art.appendChild(el("p", "cmp-b k-line", typeof l === "string" ? l : l.text || "")); });
    if (pg.footer) art.appendChild(el("p", "cmp-pf", pg.footer));
    $("#cmp-pos").textContent = T("page_of", "Page {n} of {m}", { n: pg.number || page + 1, m: pages.length }) + (printed ? " · " + T("exact", "as placed") : "");
  }
  function go(d) { var n = Math.max(0, Math.min(pages.length - 1, page + d)); if (n !== page) { page = n; draw(); } }
  $("#cmp-prev").addEventListener("click", function () { go(-1); });
  $("#cmp-next").addEventListener("click", function () { go(1); });
  $("#cmp-viewer").addEventListener("keydown", function (e) {
    if (e.target.tagName === "BUTTON" || e.ctrlKey || e.metaKey || e.altKey) return;
    var rtl = root.dataset.dir === "rtl", k = e.key;
    var d = k === "PageDown" || k === " " || k === "ArrowDown" ? 1 : k === "PageUp" ? -1 : k === "ArrowRight" ? (rtl ? -1 : 1) : k === "ArrowLeft" ? (rtl ? 1 : -1) : 0;
    if (k === "Home") { e.preventDefault(); page = 0; draw(); return; }
    if (k === "End") { e.preventDefault(); page = Math.max(0, pages.length - 1); draw(); return; }
    if (d) { e.preventDefault(); go(d); }
  });

  // ---- download and the bundle, with the manifest summary ---------------------------------------------------------------------------
  function file(action, extra) {
    var b = request(), body = new URLSearchParams({ preset: b.preset, options: JSON.stringify(b.options) });
    for (var k in extra || {}) body.set(k, extra[k]);
    say(T("making", "Making the file…"));
    fetch("/ui/projects/" + project + "/compile/" + action, { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": csrf, "X-Scriobh-Fetch": "1", "Content-Type": "application/x-www-form-urlencoded" }, body: body }).then(function (r) {
      if (!r.ok) return r.json().then(function (j) { throw new Error((j && j.error && j.error.message) || T("failed", "That did not work. Try again.")); }, function () { throw new Error(T("failed", "That did not work. Try again.")); });
      var h = function (n) { return r.headers.get(n) || ""; }, name = (/filename="?([^";]+)"?/i.exec(h("Content-Disposition")) || [])[1] || "compile";
      return r.blob().then(function (blob) {
        var a = el("a"); a.href = URL.createObjectURL(blob); a.download = name; doc.body.appendChild(a); a.click(); a.remove();
        win.setTimeout(function () { URL.revokeObjectURL(a.href); }, 5000);
        manifest({ name: name, docs: h("X-Export-Documents"), words: h("X-Export-Words"), skipped: h("X-Export-Skipped"), sha: h("X-Export-Sha256"), input: h("X-Export-Input-Hash"), id: h("X-Export-Id"), warns: h("X-Export-Warnings"), notes: h("X-Export-Notes"), cover: action === "cover" });
        say(T("made", "Made {name}. It is recorded with a manifest.", { name: name }));
      });
    }).catch(function (e) { say(e.message || T("offline", "Could not reach the server."), true); });
  }
  function manifest(m) {
    var box = $("#cmp-manifest"), dl = $("#cmp-mfacts"); dl.textContent = ""; box.hidden = false;
    function row(k, v, cls) { var d = el("div"); d.appendChild(el("dt", null, k)); var dd = el("dd", cls, v); d.appendChild(dd); dl.appendChild(d); }
    row(T("m_file", "File"), m.name); row(T("m_docs", "Documents"), m.docs); row(T("m_words", "Words"), fmt(parseInt(m.words, 10)));
    if (parseInt(m.warns, 10) > 0) row(T("m_warns", "Warnings in the file"), m.warns);
    if (m.notes) decodeURIComponent(m.notes).split("\n").forEach(function (n) { row(T("m_note", "Note"), n); });
    if (parseInt(m.skipped, 10) > 0) row(T("m_skipped", "Left out (do not compile)"), m.skipped);
    row(T("m_sha", "SHA-256 of the file"), m.sha, "cmp-hash"); row(T("m_input", "Input hash"), m.input, "cmp-hash");
    var same = last && last.input_hash && last.input_hash === m.input;
    if (!m.cover) row(T("m_match", "Same content as the preview"), last ? (same ? T("yes_same", "Yes: the input hashes are equal") : T("no_diff", "No: the preview was made before something changed. Preview again.")) : T("no_preview", "There was no preview first"), same ? "is-ok" : "");
    row(T("m_id", "Record"), m.id);
  }
  $("#cmp-download").addEventListener("click", function () { file("download"); });
  $("#cmp-submit").addEventListener("click", function () { file("submission"); });
  root.cmp = { T: T, NN: NN, el: el, api: api, say: say, why: why, fmt: fmt, request: request, file: file, format: fmtNow };
  drawPresets(root.dataset.pick);
})();
