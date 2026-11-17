/* Scríobh import page. Reads a folder or a zip in the browser, asks import.preview what it would create,
   lets the person rename, retype, reorder, split and leave out, then sends import.apply with the preview's
   token. Everything the server shows is set as text, never as markup. Loaded on the import page only. */
(function () {
  "use strict";
  var doc = document, win = window;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var root = $("#import");
  if (!root) return;
  var into = root.dataset.into || "", slug = root.dataset.slug || "";
  var csrf = ($('meta[name="csrf-token"]') || {}).content || "";
  var statusEl = $("#imp-status"), out = $("#imp-result"), splitEl = $("#imp-split");
  var CHUNK = 3 * 1024 * 1024, FILE = 2 * 1024 * 1024, TOTAL = 32 * 1024 * 1024, ZIP = 34 * 1024 * 1024, LISTED = 10000;
  var KINDS = [["chapter", "Chapter"], ["part", "Part"], ["scene", "Scene"], ["note", "Note"], ["front_matter", "Front matter"], ["back_matter", "Back matter"]];
  var PKINDS = [["novel", "Novel"], ["novella", "Novella"], ["short", "Short story"], ["collection", "Collection"], ["nonfiction", "Non-fiction book"], ["essay", "Essay"], ["blog", "Blog"], ["report", "Report"], ["blank", "Blank"]];
  var src, edits, order, gone, open, last, good = "", seq = 0, timer = 0, ui, aim = "", hidden = 0, driver = null; // driver: a repository job (js/import-repo.js) instead of files
  var hook = {}; // js/import-formats.js and js/import-books.js: label(p), show(p, ui), node(n, body, i, count), body(b), apply(b), done(r), error(e)
  var KIND = {}; KINDS.forEach(function (k) { KIND[k[0]] = k[1]; });

  function h(tag, attrs, kids) {
    var e = doc.createElement(tag);
    for (var k in attrs || {}) { var v = attrs[k]; if (v === false || v == null) continue; if (k === "text") e.textContent = v; else e.setAttribute(k, v === true ? "" : v); }
    (kids || []).forEach(function (c) { if (c) e.appendChild(c); });
    return e;
  }
  var retry = $("#imp-retry") || h("button", { hidden: true });
  var live = $("#imp-live"), liveT = 0;
  // An announcement that stays long enough to be heard: its own polite region, which the status line never touches.
  function tell(m) { if (!live || !m) return; clearTimeout(liveT); live.textContent = m; liveT = setTimeout(function () { live.textContent = ""; }, 8000); }
  function say(m, bad) { statusEl.textContent = m; if (bad) statusEl.setAttribute("data-bad", ""); else statusEl.removeAttribute("data-bad"); }
  function num(n) { return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }
  var EMPTY = "[{},{},{}]"; // the state of a mapping with nothing changed
  function reset() { src = null; edits = {}; order = {}; gone = {}; open = {}; last = null; ui = null; aim = ""; good = EMPTY; retry.hidden = true; out.hidden = true; out.replaceChildren(); clearTimeout(timer); seq++; }

  // ---- reading files without freezing the page: slices of 3 MiB, base64 by the browser, a breath between slices ----
  function b64(blob) {
    var parts = [], at = 0;
    return new Promise(function (res, rej) {
      (function next() {
        if (at >= blob.size) return res(parts.join(""));
        var r = new win.FileReader();
        r.onload = function () { parts.push(String(r.result).split(",")[1] || ""); at += CHUNK; setTimeout(next, 0); };
        r.onerror = function () { rej(new Error("The browser could not read that file.")); };
        r.readAsDataURL(blob.slice(at, at + CHUNK));
      })();
    });
  }
  function fromZip(f) {
    if (f.size > ZIP) return Promise.reject(new Error("That zip is larger than 34 MiB, which is more than can be sent at once. Take out images and other large files, or choose the folder instead: only Markdown is sent from a folder."));
    say("Reading " + f.name + "…");
    return b64(f).then(function (z) { return { zip: z, name: f.name.replace(/\.zip$/i, "") }; });
  }
  // A Word document goes the same way as a zip: read in slices, sent as base64, converted by the server to a folder of Markdown.
  function fromDocx(f) {
    if (f.size > ZIP) return Promise.reject(new Error("That document is larger than 34 MiB, which is more than can be sent at once."));
    say("Reading " + f.name + "…");
    return b64(f).then(function (z) { return { docx: z, name: f.name.replace(/\.docx$/i, "") }; });
  }
  function fromFolder(list) {
    var name = "", items = [], total = 0;
    hidden = 0;
    Array.prototype.forEach.call(list, function (f) {
      var p = (f.webkitRelativePath || f.name).split("/");
      if (!name) name = p[0];
      p = p.slice(1);
      if (!p.length) return;
      if (p.some(function (s) { return s[0] === "." || s === "node_modules"; })) { hidden++; return; }
      var path = p.join("/"), text = /\.(md|markdown)$/i.test(path) || path === "scriobh.json" || path === "project.json";
      if (text && f.size <= FILE) total += f.size;
      items.push({ f: f, path: path, text: text && f.size <= FILE });
    });
    if (items.length > LISTED) return Promise.reject(new Error("This folder holds " + num(items.length) + " files, and the limit is " + num(LISTED) + ". Choose a smaller folder."));
    if (total > TOTAL) return Promise.reject(new Error("This folder holds more than 32 MiB of Markdown, which is the most one import can take. Import it in parts."));
    var files = [], i = 0;
    return new Promise(function (res, rej) {
      (function next() {
        if (i >= items.length) return res({ files: files, name: name });
        var it = items[i++];
        if (i % 20 === 0) say("Reading file " + i + " of " + items.length + "…");
        if (!it.text) { files.push({ path: it.path, size: it.f.size }); return next(); }
        b64(it.f).then(function (d) { files.push({ path: it.path, data: d, size: it.f.size }); next(); }, rej);
      })();
    });
  }

  // ---- the server ----------------------------------------------------------------------------------------------
  function api(path, body, method) {
    return fetch(path, { method: method || "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Accept: "application/json", "X-CSRF-Token": csrf }, body: body === undefined ? undefined : JSON.stringify(body) }).then(function (r) {
      return r.text().then(function (t) {
        var j = null;
        try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ }
        if (r.ok && j) return j.item || j;
        var m = j && j.error && j.error.message;
        if (r.status === 413) m = "That is more than can be sent at once. The limit is 32 MiB of Markdown in all.";
        else if (r.status === 401) m = "You are signed out. Sign in again in another tab, then try again here.";
        else if (r.status === 429) m = "That was too many requests in a row. Wait a moment and try again.";
        var er = new Error(m || "The server could not do that (" + r.status + ")."), d = j && j.error;
        er.code = (d && d.data && d.data.git_code) || (d && d.code) || "";
        er.status = r.status;
        throw er;
      });
    }, function () { throw new Error("Could not reach the server. Nothing was imported."); });
  }
  function body(extra) {
    var b = { split_scenes: splitEl.checked ? "h2" : "none" };
    if (driver) b.job_id = driver.job;
    else {
      b.name = src.name;
      if (src.docx) { b.docx = src.docx; b.docx_headings = $("#imp-dh").value; } else if (src.zip) b.zip = src.zip; else b.files = src.files;
      if ($("#imp-obs").checked && !src.docx) b.layout = "obsidian";
      if (src.opts) src.opts(b);
      if (hook.body) hook.body(b);
    }
    for (var k in extra) b[k] = extra[k];
    return b;
  }
  function editList() { return Object.keys(edits).map(function (k) { var e = { key: k }; for (var f in edits[k]) e[f] = edits[k][f]; return e; }); }
  function orderList() { return Object.keys(order).map(function (k) { return { parent: k, keys: order[k] }; }); }
  function preview(first) {
    var mine = ++seq, sent = JSON.stringify([edits, order, gone]);
    say(first ? "Working out what would be created…" : "");
    return api(driver ? "/api/v1/imports/git/preview" : into ? "/api/v1/projects/" + into + "/import/preview" : "/api/v1/imports/preview", body({ edits: editList(), reorder: orderList() })).then(function (p) {
      if (mine !== seq) return;
      if (driver) p = p.preview;
      last = p; good = sent; retry.hidden = true; say(""); show(p);
      if (first) { var hd = $("#imp-h"); if (hd) hd.focus(); }
    }, function (e) {
      if (mine !== seq) return;
      // A change the server refuses (a join into itself, a move into what is inside it) is taken back, so the page is never stuck on it.
      // (back to the last state the server accepted: more than one change can be waiting when the refusal comes).
      if (!first && sent !== good && (e.status === 422 || e.status === 409)) {
        var s = JSON.parse(good); edits = s[0]; order = s[1]; gone = s[2];
        return preview(false).then(function () { say((hook.undone || "That change was not made. ") + e.message, true); });
      }
      fail(e, function () { preview(first); });
    });
  }
  // An error in words, and a way to try again when the server was only busy.
  var again1 = null;
  function fail(e, again) {
    say(hook.error && hook.error(e) || e.message, true);
    again1 = again; retry.hidden = e.status !== 429 || !again;
    if (!retry.hidden) retry.focus();
  }
  retry.addEventListener("click", function () { retry.hidden = true; if (again1) again1(); });
  function later() { clearTimeout(timer); timer = setTimeout(function () { preview(false); }, 350); }

  // ---- the preview -----------------------------------------------------------------------------------------------
  function select(opts, cur, attrs) {
    var s = h("select", attrs);
    opts.forEach(function (o) { s.appendChild(h("option", { value: o[0], selected: o[0] === cur, text: o[1] })); });
    return s;
  }
  function build(p) {
    var kind = PKINDS.some(function (k) { return k[0] === p.project.kind; }) ? p.project.kind : "blank";
    var t = h("input", { id: "imp-t", type: "text", maxlength: "200", value: p.project.title }), k = select(PKINDS, kind, { id: "imp-k" }), a = h("input", { id: "imp-a", type: "text", maxlength: "200", value: p.project.author || "" });
    var field = function (label, ctl, id) { return h("div", { class: "field" }, [h("label", { for: id, text: label }), ctl]); };
    ui = { sum: h("p", { class: "imp-sum" }), notes: h("div"), tree: h("ul", { class: "imp-tree", "aria-label": "Documents that would be created" }), left: h("div", { class: "imp-left" }), skipped: h("div", { class: "imp-skipped" }), t: t, k: k, a: a,
      go: h("button", { class: "btn solid", type: "button", "data-go": true }) };
    out.replaceChildren(h("h2", { id: "imp-h", tabindex: "-1", text: into ? "What would be added" : "What would be created" }), ui.sum, ui.notes,
      into ? null : h("div", { class: "imp-fields" }, [field("Project title", t, "imp-t"), field("Kind", k, "imp-k"), field("Author", a, "imp-a")]),
      ui.tree, ui.left, ui.skipped, h("div", { class: "imp-go" }, [ui.go]));
    out.hidden = false;
  }
  function show(p) {
    if (!ui) build(p);
    var sum = [num(p.documents) + (p.documents === 1 ? " document" : " documents"), num(p.words) + (p.words === 1 ? " word" : " words"), hook.label && hook.label(p) || (p.layout === "scriobh" ? "A Scríobh project folder" : src && src.docx ? "A Word document" : $("#imp-obs") && $("#imp-obs").checked ? "An Obsidian vault" : "A folder of Markdown files, one document each")];
    ui.sum.replaceChildren.apply(ui.sum, sum.map(function (s) { return h("span", { text: s }); }));
    var notes = (p.warnings || []).map(function (w) { return h("p", { class: "imp-note", "data-kind": "warn", text: "Note: " + w }); });
    if (hidden) notes.push(h("p", { class: "imp-note", text: "Note: " + num(hidden) + (hidden === 1 ? " hidden file was" : " hidden files were") + " not read (names that start with a dot, such as .git)." }));
    ui.notes.replaceChildren.apply(ui.notes, notes);
    ui.go.textContent = (into ? "Add " : "Import ") + num(p.documents) + (p.documents === 1 ? " document" : " documents");
    ui.go.disabled = !p.documents;
    var kids = {};
    p.nodes.forEach(function (n) { (kids[n.parent_key] = kids[n.parent_key] || []).push(n); });
    function list(parent, depth) {
      var arr = kids[parent] || [];
      return arr.map(function (n, i) { return node(n, i, arr.length, kids[n.key], depth); });
    }
    function node(n, i, count, below, depth) {
      var q = function (act, extra) { var a = { "data-key": n.key, "data-act": act }; for (var x in extra) a[x] = extra[x]; return a; };
      var body = [h("input", q("title", { type: "text", maxlength: "300", value: n.title, "aria-label": "Title of " + n.title }))];
      body.push(select(KINDS, n.kind, q("kind", { "aria-label": "Kind of " + n.title })));
      if (n.headings) body.push(h("label", { class: "check" }, [h("input", q("split", { type: "checkbox", checked: !!n.split })), h("span", { text: "Split at ## headings (" + n.headings + ")" })]));
      body.push(h("button", q("up", { class: "btn small", type: "button", disabled: i === 0, "aria-label": "Move " + n.title + " up", text: "Move up" })));
      body.push(h("button", q("down", { class: "btn small", type: "button", disabled: i === count - 1, "aria-label": "Move " + n.title + " down", text: "Move down" })));
      if (count > 3) { // a long list: the two ends are one press away
        body.push(h("button", q("top", { class: "btn small quiet", type: "button", disabled: i === 0, "aria-label": "Move " + n.title + " to the top", text: "Move to top" })));
        body.push(h("button", q("bottom", { class: "btn small quiet", type: "button", disabled: i === count - 1, "aria-label": "Move " + n.title + " to the bottom", text: "Move to bottom" })));
      }
      body.push(h("button", q("skip", { class: "btn small quiet", type: "button", "aria-label": "Leave out " + n.title, text: "Leave out" })));
      if (hook.node) hook.node(n, body, { depth: depth, kids: !!below });
      var li = h("li", { class: "imp-node", "data-kind": n.kind, "data-key": n.key }, [
        h("div", { class: "imp-row" }, [
          h("span", { class: "imp-title", text: n.title }),
          h("span", { class: "imp-meta", text: (KIND[n.kind] || n.kind) + " · " + num(n.words) + (n.words === 1 ? " word" : " words") + (depth > 3 ? " · " + (hook.level ? hook.level(depth + 1) : "level " + (depth + 1)) : "") }),
          h("details", { class: "imp-edit", "data-key": n.key, open: !!open[n.key] }, [h("summary", { "aria-label": "Edit " + n.title, text: "Edit" }), h("div", { class: "imp-edit-body" }, body)]),
          (n.warnings || []).length ? h("p", { class: "imp-warn", text: "Note: " + n.warnings.join(" ") }) : null
        ])
      ]);
      if (below) li.appendChild(h("ul", null, list(n.key, depth + 1)));
      return li;
    }
    ui.tree.replaceChildren.apply(ui.tree, list("", 0));
    // What was left out can be brought back; the server no longer lists it, so the page remembers it.
    var gk = Object.keys(gone);
    ui.left.replaceChildren();
    if (gk.length) ui.left.appendChild(h("details", { open: true }, [h("summary", { text: "Left out of the import (" + gk.length + ")" }), h("ul", null, gk.map(function (k) {
      var g = gone[k];
      return h("li", null, [h("span", { text: g.title + (g.into ? " (joined into " + g.into + ")" : g.inside ? " and " + g.inside + " inside it" : "") }), h("button", { class: "btn small quiet", type: "button", "data-key": k, "data-act": "back", "aria-label": "Put " + g.title + " back", text: "Put back" })]);
    }))]));
    var why = Object.keys(p.skipped_by_reason || {});
    ui.skipped.replaceChildren();
    if (p.skipped_total) {
      var more = p.skipped_total > p.skipped.length ? h("li", { text: "and " + num(p.skipped_total - p.skipped.length) + " more" }) : null;
      ui.skipped.appendChild(h("details", null, [h("summary", { text: num(p.skipped_total) + (p.skipped_total === 1 ? " file" : " files") + " will not be imported" }),
        h("ul", null, why.map(function (r) { return h("li", { text: r + ": " + num(p.skipped_by_reason[r]) }); }).concat(p.skipped.map(function (s) {
          return h("li", null, [h("span", { text: s.path }), h("span", { class: "why", text: s.reason })]);
        }), [more]))]));
    }
    if (hook.show) hook.show(p, ui);
    if (aim) {
      var want = aim.split("|"), el = Array.prototype.filter.call(out.querySelectorAll("[data-act]"), function (e) { return e.dataset.key === want[0] && e.dataset.act === want[1] && !e.disabled; })[0];
      if (!el) el = Array.prototype.filter.call(out.querySelectorAll("details.imp-edit"), function (e) { return e.dataset.key === want[0]; }).map(function (d) { return d.firstChild; })[0];
      if (el) el.focus();
      aim = "";
    }
  }

  // ---- edits -----------------------------------------------------------------------------------------------------
  function find(k) { return (last ? last.nodes.concat(last.research || []) : []).filter(function (n) { return n.key === k; })[0]; }
  function inside(k) {
    var n = 0, seen = {}; seen[k] = 1;
    (last ? last.nodes : []).forEach(function (x) { if (seen[x.parent_key]) { seen[x.key] = 1; n++; } });
    return n;
  }
  // The title of a chapter that was joined into k or into something inside it, when there is one.
  function joinedInto(k) {
    var in2 = {}; in2[k] = 1;
    (last ? last.nodes : []).forEach(function (x) { if (in2[x.parent_key]) in2[x.key] = 1; });
    for (var g in gone) if (gone[g].to && in2[gone[g].to]) return gone[g].title;
    return "";
  }
  function tidy(k) { if (edits[k] && !Object.keys(edits[k]).length) delete edits[k]; }
  function act(el) {
    var k = el.dataset.key, a = el.dataset.act, n = find(k), e = edits[k] = edits[k] || {};
    aim = k + "|" + a;
    if (a === "title") {
      if (!el.value.trim()) { say("A title cannot be empty.", true); el.value = n ? n.title : ""; aim = ""; tidy(k); return; }
      e.title = el.value.trim();
    } else if (a === "kind") e.kind = el.value;
    else if (a === "split") {
      e.split = el.checked;
      Object.keys(edits).concat(Object.keys(order)).forEach(function (x) { if (x.indexOf(k + "#") === 0) { delete edits[x]; delete order[x]; } });
      delete order[k];
    } else if (a === "skip") {
      var tied = joinedInto(k); // a chapter that others are joined into stays until they are put back (the server refuses the mapping, in either order)
      if (tied) { tidy(k); say(hook.stays ? hook.stays(n ? n.title : "", tied) : "Put " + tied + " back first.", true); aim = k + "|edit"; return; }
      gone[k] = { title: n ? n.title : k, inside: inside(k) };
      e.skip = true;
      aim = k + "|back";
      Object.keys(order).forEach(function (p) { order[p] = order[p].filter(function (x) { return x !== k; }); });
    } else if (a === "back") { delete e.skip; delete e.merge_into; delete gone[k]; aim = k + "|edit"; }
    else if (a === "parent" || a === "merge") {
      // Joined or moved: out of every order list (the server refuses an order that names a child the parent no longer has), and a join stands alone.
      var to = el.dataset.to || "", into = find(to);
      if (a === "merge" && (!to || !into || to === k)) { tidy(k); say(hook.noJoin || "That chapter cannot be joined to nothing.", true); aim = k + "|edit"; return; } // the server would ignore it: the page must not say it was done
      Object.keys(order).forEach(function (p) { order[p] = order[p].filter(function (x) { return x !== k; }); });
      if (a === "parent") { e.parent = to; aim = k + "|edit"; tell(hook.movedUnder ? hook.movedUnder(n ? n.title : "", to ? into.title : "") : "Moved."); }
      else {
        gone[k] = { title: n ? n.title : k, into: into.title, to: to };
        tell(hook.joined ? hook.joined(n ? n.title : "", into.title) : "Joined.");
        edits[k] = e = { merge_into: to };
        aim = k + "|back";
      }
    }
    else if (a === "up" || a === "down" || a === "top" || a === "bottom") {
      tidy(k);
      var par = n.parent_key, keys = order[par] || last.nodes.filter(function (x) { return x.parent_key === par; }).map(function (x) { return x.key; }), at = keys.indexOf(k),
        to = a === "up" ? at - 1 : a === "down" ? at + 1 : a === "top" ? 0 : keys.length - 1;
      if (at < 0 || to < 0 || to >= keys.length || to === at) return;
      keys = keys.slice(); keys.splice(at, 1); keys.splice(to, 0, k);
      order[par] = keys;
      aim = k + "|" + ((a === "up" || a === "top") && to === 0 || (a === "down" || a === "bottom") && to === keys.length - 1 ? "edit" : a);
      var nb = find(keys[to < keys.length - 1 ? to + 1 : to - 1]); // where it went, said in words: its place and its neighbour
      tell(hook.movedTo ? hook.movedTo(n.title, to + 1, keys.length, nb ? nb.title : "", to < keys.length - 1) : "Moved.");
    }
    tidy(k);
    later();
  }
  out.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("button");
    if (!b) return;
    if (b.hasAttribute("data-go")) return apply();
    if (b.dataset.act) act(b);
  });
  out.addEventListener("change", function (e) { var t = e.target; if (t.dataset && (t.dataset.act === "title" || t.dataset.act === "kind" || t.dataset.act === "split")) act(t); });
  out.addEventListener("toggle", function (e) { var d = e.target; if (d.dataset && d.classList.contains("imp-edit")) { if (d.open) open[d.dataset.key] = 1; else delete open[d.dataset.key]; } }, true);

  function apply() {
    var b = body({ token: last.token, edits: editList(), reorder: orderList() });
    if (!into) { b.title = ui.t.value.trim(); b.kind = ui.k.value; b.author = ui.a.value.trim(); }
    ui.go.disabled = true;
    say("Importing. This can take a moment for a large folder…");
    if (hook.apply) hook.apply(b);
    api(driver ? "/api/v1/imports/git" : into ? "/api/v1/projects/" + into + "/import" : "/api/v1/imports", b).then(function (r) {
      if (hook.done && hook.done(r, into ? slug : r.slug, into || r.project_id)) return;
      say("Imported " + num(r.documents) + (r.documents === 1 ? " document." : " documents."));
      win.location.assign("/p/" + (into ? slug : r.slug) + (r.first_document_id ? "/d/" + r.first_document_id : ""));
    }, function (e) { ui.go.disabled = false; fail(e, apply); if (retry.hidden) ui.go.focus(); });
  }

  // ---- choosing ------------------------------------------------------------------------------------------------------
  function start(job, input) {
    reset();
    say("Reading your files…");
    job.then(function (s) { src = s; return preview(true); }).catch(function (e) { say(e.message || "Could not read those files.", true); input.value = ""; });
  }
  var fold = $("#imp-folder"), zip = $("#imp-zip"), dx = $("#imp-docx"), picks = [fold, zip, dx];
  function pick(el, make) { if (el) el.addEventListener("change", function (e) { if (e.target.files.length) { picks.forEach(function (o) { if (o && o !== el) o.value = ""; }); start(make(el.webkitdirectory ? e.target.files : e.target.files[0]), e.target); } }); }
  pick(fold, fromFolder); pick(zip, fromZip); pick(dx, fromDocx);
  function again() { if (src) { edits = {}; order = {}; gone = {}; good = EMPTY; later(); } }
  [$("#imp-obs"), $("#imp-dh")].forEach(function (c) { if (c) c.addEventListener("change", again); });
  splitEl.addEventListener("change", function () { if (src || driver) { edits = {}; order = {}; gone = {}; good = EMPTY; later(); } });
  reset();
  // The repository page hands over a ready job: the preview, its edits and the apply step are this file's.
  win.scriobhImport = { hook: hook, out: out, nodes: function () { return last ? last.nodes : []; }, src: function () { return src; }, again: again, b64: b64, fromZip: fromZip, pick: function (el, make) { picks.push(el); pick(el, make); }, api: api, say: say, root: root, clear: function () { driver = null; reset(); }, begin: function (job) { reset(); driver = { job: job }; return preview(true); } };
})();
