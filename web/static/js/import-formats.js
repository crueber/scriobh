/* Scríobh import page, the formats beside plain Markdown: the chooser that shows the words and options of each,
   reading a Scrivener project (a zip or the .scriv folder), a WordPress or Ghost export (the file or a zip), and, in the
   preview, the research notes and the converter's report of what it read and did not keep. Everything the server says
   is set as text. Loaded after js/import.js on the import pages. */
(function () {
  "use strict";
  var doc = document, I = window.scriobhImport, fmt = doc.getElementById("imp-fmt");
  if (!I || !fmt) return;
  var all = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  var MAX = 34 * 1024 * 1024, PIC = /\.(png|jpe?g|gif|webp)$/i;
  var NAMES = { scrivener: "A Scrivener project", wordpress: "A WordPress export", ghost: "A Ghost export", docx: "A Word document", pandoc: "A document converted by Pandoc", obsidian: "An Obsidian vault", scriobh: "A Scríobh project folder", "markdown-folder": "A folder of Markdown files, one document each" };
  var SHEET = { character: "character sheet", place: "place sheet" };
  var opened = {};

  function h(tag, attrs, kids) {
    var e = doc.createElement(tag);
    for (var k in attrs || {}) { var v = attrs[k]; if (v === false || v == null) continue; if (k === "text") e.textContent = v; else e.setAttribute(k, v === true ? "" : v); }
    (kids || []).forEach(function (c) { if (c) e.appendChild(c); });
    return e;
  }
  function num(n) { return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }

  // ---- the chooser: one panel of words and options per format --------------------------------------------------------
  function shown() {
    var v = (fmt.querySelector("input:checked") || {}).value || "md";
    all("[data-fmt]").forEach(function (b) { b.hidden = b.getAttribute("data-fmt") !== v; });
  }
  fmt.addEventListener("change", shown);
  shown();

  // The options of a format go with every preview and the apply, read when they are sent.
  function opts(layout) {
    return function (b) {
      b.layout = layout;
      all('[data-fmt="' + layout + '"] [data-opt]').forEach(function (e) { b[e.getAttribute("data-opt")] = e.type === "checkbox" ? (e.checked ? e.value : e.getAttribute("data-off")) : e.value; });
    };
  }
  all("[data-opt]").forEach(function (e) { e.addEventListener("change", I.again); });

  function bare(name) { return name.replace(/\.zip$/i, "").replace(/\.scriv$/i, "").replace(/\.(xml|json)$/i, "").replace(/\.ghost\.[\d-]+$/i, ""); }
  // A zip, or one export file sent as a listing of one.
  function file(layout) {
    return function (f) {
      var go = /\.zip$/i.test(f.name) ? I.fromZip(f) : f.size > MAX ? Promise.reject(new Error("That file is larger than 34 MiB, which is more than can be sent at once.")) :
        (I.say("Reading " + f.name + "…"), I.b64(f).then(function (d) { return { files: [{ path: f.name, data: d, size: f.size }] }; }));
      return go.then(function (s) { s.name = bare(f.name); s.opts = opts(layout); return s; });
    };
  }
  // A .scriv folder: the binder, the text files and the pictures, nothing else, and nothing hidden.
  function folder(list) {
    var name = "", want = [], skip = [], total = 0;
    Array.prototype.forEach.call(list, function (f) {
      var p = (f.webkitRelativePath || f.name).split("/");
      if (!name) name = p[0];
      p = p.slice(1);
      var path = p.join("/");
      if (!p.length || p.some(function (s) { return s[0] === "."; })) return;
      if (/\.(scrivx|rtf|txt)$/i.test(path) || PIC.test(path)) { total += f.size; want.push({ f: f, path: path }); } else skip.push({ path: path, size: f.size });
    });
    if (!want.some(function (w) { return /\.scrivx$/i.test(w.path); })) return Promise.reject(new Error("That folder has no .scrivx file, so it is not a Scrivener project. Choose the folder whose name ends in .scriv."));
    if (total > MAX) return Promise.reject(new Error("This project is larger than 34 MiB, which is more than can be sent at once. Zip the .scriv folder and choose the zip; pictures can be left out."));
    var files = skip, i = 0;
    return new Promise(function (res, rej) {
      (function next() {
        if (i >= want.length) return res({ files: files, name: bare(name), opts: opts("scrivener") });
        var w = want[i++];
        if (i % 20 === 0) I.say("Reading file " + i + " of " + want.length + "…");
        I.b64(w.f).then(function (d) { files.push({ path: w.path, data: d, size: w.f.size }); next(); }, rej);
      })();
    });
  }
  // A document Pandoc reads: sent whole as base64 with its format, which the name of the file suggests (the person can change it).
  var PDFMT = { odt: "odt", rtf: "rtf", tex: "latex", latex: "latex", wiki: "mediawiki", mediawiki: "mediawiki", org: "org", textile: "textile", rst: "rst", fb2: "fb2", typ: "typst", dbk: "docbook", docbook: "docbook" };
  function pandocFile(f) {
    if (f.size > MAX) return Promise.reject(new Error("That file is larger than 34 MiB, which is more than can be sent at once."));
    var sel = doc.getElementById("imp-pdf"), m = /\.([a-z0-9]+)$/i.exec(f.name), g = m && PDFMT[m[1].toLowerCase()];
    if (sel && g) sel.value = g;
    I.say("Reading " + f.name + "…");
    return I.b64(f).then(function (d) {
      return { files: [], name: f.name.replace(/\.[^.]+$/, ""), opts: function (b) {
        delete b.files; b.pandoc = d;
        all('[data-fmt="pandoc"] [data-opt]').forEach(function (e) { b[e.getAttribute("data-opt")] = e.value; });
      } };
    });
  }
  [["imp-pd", pandocFile], ["imp-scriv", file("scrivener")], ["imp-scrivdir", folder], ["imp-wp", file("wordpress")], ["imp-gh", file("ghost")]].forEach(function (x) {
    var el = doc.getElementById(x[0]);
    if (el) I.pick(el, x[1]);
  });

  // ---- the preview: what kind of source it was, its research notes and its report ---------------------------------
  I.hook.label = function (p) { return NAMES[p.layout]; };
  function label(k) { k = String(k).replace(/_/g, " "); return k.charAt(0).toUpperCase() + k.slice(1); }
  function empty(v) { return v == null || v === 0 || v === false || v === "" || (typeof v === "object" && !Object.keys(v).length); }
  function val(v) {
    if (v == null) return "";
    if (Array.isArray(v)) return v.slice(0, 20).map(val).join(", ") + (v.length > 20 ? ", …" : "");
    if (typeof v === "object") return Object.keys(v).slice(0, 30).map(function (k) { return k + " " + val(v[k]); }).join(", ");
    return typeof v === "number" ? num(v) : String(v);
  }
  function details(id, summary, body) {
    var d = h("details", { "data-id": id, open: !!opened[id] }, [h("summary", { text: summary }), body]);
    d.addEventListener("toggle", function () { opened[id] = d.open; });
    return d;
  }
  I.hook.show = function (p, ui) {
    if (!ui.more) { ui.more = h("div", { class: "imp-more" }); ui.left.parentNode.insertBefore(ui.more, ui.left); }
    var parts = [], rs = p.research || [], kids = {};
    if (rs.length) {
      rs.forEach(function (n) { (kids[n.parent_key] = kids[n.parent_key] || []).push(n); });
      var tree = function (key) {
        return (kids[key] || []).map(function (n) {
          var meta = n.kind === "folder" ? "Folder" : num(n.words) + (n.words === 1 ? " word" : " words");
          if (SHEET[n.entity_candidate]) meta += " · " + SHEET[n.entity_candidate];
          var li = h("li", { class: "imp-node" }, [h("div", { class: "imp-row" }, [
            h("span", { class: "imp-title", text: n.title }), h("span", { class: "imp-meta", text: meta }),
            h("button", { class: "btn small quiet", type: "button", "data-key": n.key, "data-act": "skip", "aria-label": "Leave out " + n.title, text: "Leave out" })])]);
          if (kids[n.key]) li.appendChild(h("ul", null, tree(n.key)));
          return li;
        });
      };
      parts.push(details("r", num(p.research_total) + (p.research_total === 1 ? " research note" : " research notes and folders") + " would be written",
        h("ul", { class: "imp-tree", "aria-label": "Research notes that would be written" }, tree(""))));
    }
    // The report is counts by name, often one map per converter: a line for each count that is not nothing.
    var rows = [], add = function (k, v) { if (!empty(v)) rows.push(h("li", null, [h("span", { text: label(k) }), h("span", { class: "why", text: val(v) })])); };
    Object.keys(p.report || {}).forEach(function (k) {
      var v = p.report[k];
      if (v && typeof v === "object" && !Array.isArray(v)) Object.keys(v).forEach(function (j) { add(j, v[j]); }); else add(k, v);
    });
    if (rows.length) parts.push(details("c", "What the converter read and did not keep", h("ul", { class: "imp-rep" }, rows.slice(0, 80))));
    ui.more.replaceChildren.apply(ui.more, parts);
  };
})();
