/* Scríobh writing view: the project's stash, a tab of the inspector for editors only. Paragraphs, notes and outlines kept
   out of the chapters: searched, made from a selection ("Send to the stash"), edited, put in order, trashed and brought
   back, and put into the open chapter only when the writer asks, at the cursor or at the end, as a paragraph of its own or
   inside one, as a copy or as a move. The stash is not part of any export. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, E = win.scriobhEditor;
  if (!W || !E) return;
  var el = function (tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  var base = "/api/v1/projects/" + W.cfg.project + "/stash", root = null, q = "", trash = false, items = [], total = 0, openFor = "", msg = "";
  var ed = W.editor, view = ed.view, md = E.markdown;
  function say(m) { msg = m; if (S.alts) S.alts.say(m); }
  function errText(r) { return (r.json && r.json.error && r.json.error.message) || "That did not work. Try again."; }
  function btn(label, fn, cls, aria) { var b = el("button", "btn " + (cls || "quiet small"), label); b.type = "button"; if (aria) b.setAttribute("aria-label", aria); b.addEventListener("click", fn); return b; }
  function load() {
    var url = base + "?limit=100" + (q ? "&q=" + encodeURIComponent(q) : "") + (trash ? "&trash=1" : "");
    return W.api("GET", url).then(function (r) { if (r.ok && r.json) { items = r.json.items || []; total = r.json.total || items.length; } draw(); }, function () { say("Could not reach the server."); });
  }
  function again(p, key) {
    return p.then(function (r) { return load().then(function () { var n = key && root.querySelector('[data-k="' + key + '"]'); if (n) n.focus(); return r; }); });
  }
  function call(method, path, body) { return W.api(method, base + path, body).then(function (r) { if (!r.ok) say(errText(r)); return r; }, function () { say("Could not reach the server."); return { ok: false }; }); }

  // ---- making an item -------------------------------------------------------------------------------------------------------
  // The selection as Markdown, whole blocks or inline text, as the serializer writes it.
  function selectionSource() {
    var s = view.state.selection;
    if (s.empty) return "";
    var frag = s.content().content, D = md.schema.nodes;
    try {
      var d = frag.firstChild && frag.firstChild.isInline ? D.doc.create(null, D.paragraph.create(null, frag)) : D.doc.create(null, frag);
      return md.serialize(d).replace(/\n+$/, "");
    } catch (e) { return view.state.doc.textBetween(s.from, s.to, "\n\n"); }
  }
  // The range, the document and the words are taken when the key is pressed. The editor is held until the stash has answered,
  // and what is removed is that range of that document: never whatever is selected when the answer arrives.
  function sendSelection() {
    var A = S.alts;
    if (!A) return null;
    if (!A.can || W.locked) return A.note("This chapter cannot be changed here, so text cannot be sent from it to the stash.");
    var text = selectionSource();
    if (!text.trim()) return A.note("Select the text to send to the stash first.");
    var sel = view.state.selection, cap = { from: sel.from, to: sel.to, doc: view.state.doc };
    var many = /\n\s*\n/.test(text);
    return A.exclusive(function () {
      if (view.state.doc !== cap.doc) { A.note("The chapter changed before that could be sent, so nothing was sent or removed. Select the text again."); return { ok: false }; }
      return call("POST", "", { content: text, kind: "paragraph", title: text.replace(/[*_`#>\[\]]/g, "").slice(0, 60).trim(), source_document_id: W.cfg.id });
    }, function () {
      if (view.state.doc !== cap.doc) { A.note("Sent to the stash, but the chapter changed meanwhile, so the text was left where it is."); return; }
      view.dispatch(view.state.tr.delete(cap.from, cap.to).scrollIntoView()); // an ordinary edit, saved like typing
      view.focus();
      A.note("Sent to the stash" + (many ? " (more than one paragraph)" : "") + ". It is no longer in the chapter; find it in the Stash tab.");
    }).then(function (r) { if (r && r.ok !== false && root && !root.hidden) load(); return r; });
  }

  // ---- putting an item back ---------------------------------------------------------------------------------------------------
  function putBack(it, o) {
    if (!S.alts) return;
    var pos = view.state.selection.from;
    var body = { document_id: W.cfg.id, item_id: it.id, placement: o.placement, mode: o.mode, with_content: true };
    return S.alts.run(function (rev, tab) {
      // Once the editor is held and saved: "at the cursor" is an offset into the saved text, trusted only if the editor and the
      // saved text spell it alike (and it is never turned into "the end" by a failure to find the place).
      var go = function () {
        if (o.where === "cursor") {
          var at = S.alts.offsetOf(pos);
          if (at < 0) { S.alts.note(S.alts.notPlaced); return null; }
          body.at = at;
        }
        body.base_revision = W.rev; body.tab_id = tab; // the revision now: finding the place may have stored the editor's spelling first
        return W.api("POST", base + "/drag-back", body);
      };
      if (o.where !== "cursor") return go();
      return S.alts.ensurePlaced().then(function (ok) { if (!ok) { S.alts.note(S.alts.notPlaced); return null; } return go(); });
    }).then(function (r) {
      if (!r.ok) return;
      openFor = "";
      S.alts.note((o.mode === "move" ? "Moved" : "Copied") + " into the chapter" + (o.where === "cursor" ? " at the cursor." : " at the end."));
      load();
    });
  }
  function putForm(it) {
    var f = el("form", "ap-put"), mk = function (name, opts) {
      var s = el("select"); s.name = name; s.setAttribute("aria-label", name === "where" ? "Where" : name === "placement" ? "How" : "Keep a copy");
      opts.forEach(function (o) { var x = el("option", "", o[1]); x.value = o[0]; s.append(x); }); return s;
    };
    var where = mk("where", [["cursor", "At the cursor"], ["end", "At the end of the chapter"]]);
    var place = mk("placement", [["block", "As a paragraph of its own"], ["inline", "Inside the paragraph, as words"]]);
    var mode = mk("mode", [["copy", "Keep it in the stash too"], ["move", "Take it out of the stash"]]);
    var go = el("button", "btn small solid", "Put it in the chapter"); go.type = "submit";
    f.append(where, place, mode, go, btn("Cancel", function () { openFor = ""; draw(); var n = root.querySelector('[data-k="put' + it.id + '"]'); if (n) n.focus(); }));
    f.addEventListener("submit", function (e) { e.preventDefault(); putBack(it, { where: where.value, placement: place.value, mode: mode.value }); });
    return f;
  }
  function editForm(li, it) {
    var f = el("form", "ap-put"), t = el("input"), c = el("textarea"), go = el("button", "btn small solid", "Save"); go.type = "submit";
    t.type = "text"; t.value = it.title; t.setAttribute("aria-label", "Title"); c.rows = 6; c.value = it.content; c.setAttribute("aria-label", "Text, in Markdown");
    f.append(t, c, go, btn("Cancel", draw));
    f.addEventListener("submit", function (e) { e.preventDefault(); again(call("PATCH", "/item", { item_id: it.id, base_revision: it.revision, title: t.value, content: c.value }), "edit" + it.id); });
    li.replaceChildren(f); t.focus();
  }

  function row(it, i) {
    var li = el("li", "ap-card"), h = el("div", "ap-head");
    h.append(el("span", "insp-tag", it.kind), el("strong", "", it.title || "Untitled"), el("span", "fine", it.word_count + (it.word_count === 1 ? " word" : " words")));
    li.append(h, el("p", "ap-text", (it.snippet || it.content).replace(/[*_`#>]/g, "").slice(0, 160)));
    var acts = el("div", "ap-row"), k = function (b, key) { b.dataset.k = key; return b; };
    if (!trash) {
      acts.append(k(btn("Put in the chapter", function () { openFor = openFor === it.id ? "" : it.id; draw(); }, "small", "Put “" + (it.title || "this item") + "” in the chapter"), "put" + it.id), k(btn("Edit", function () { editForm(li, it); }, "quiet small", "Edit " + (it.title || "this item")), "edit" + it.id));
      if (!q) {
        var up = k(btn("Move up", function () { again(call("POST", "/reorder", { item_id: it.id, after_item_id: i > 1 ? items[i - 2].id : "" }), "up" + it.id); }, "quiet small", "Move " + (it.title || "this item") + " up"), "up" + it.id); up.disabled = i === 0;
        var dn = k(btn("Move down", function () { again(call("POST", "/reorder", { item_id: it.id, after_item_id: items[i + 1].id }), "dn" + it.id); }, "quiet small", "Move " + (it.title || "this item") + " down"), "dn" + it.id); dn.disabled = i === items.length - 1;
        acts.append(up, dn);
      }
      acts.append(btn("Trash", function () { again(call("POST", "/trash", { item_id: it.id })); }, "quiet small", "Trash " + (it.title || "this item")));
    } else {
      acts.append(btn("Restore", function () { again(call("POST", "/restore", { item_id: it.id })); }, "small"), btn("Delete for good", function (e) { win.scriobh.confirm(li, { msg: "Delete this item for good? It cannot be brought back.", ok: "Delete for good", from: e.currentTarget, onOk: function () { again(call("POST", "/delete", { item_id: it.id })); } }); }, "quiet small"));
    }
    li.append(acts);
    if (openFor === it.id) li.append(putForm(it));
    return li;
  }
  function draw() {
    if (!root) return;
    var kids = [el("h2", "ver-h", trash ? "Stash trash" : "Stash")];
    kids.push(el("p", "fine", "Paragraphs, notes and outlines you are not using yet. Only editors of this project see the stash. It is not part of any export, and nothing goes back into a chapter unless you put it there. Select text and choose Send to the stash (Alt+Shift+S) to keep it here."));
    var f = el("form", "ap-add"), s = el("input"), go = el("button", "btn small", "Search"); s.type = "search"; s.value = q; s.setAttribute("aria-label", "Search the stash"); s.placeholder = "Search the stash"; go.type = "submit";
    f.append(s, go); f.addEventListener("submit", function (e) { e.preventDefault(); q = s.value.trim(); load(); });
    kids.push(f);
    var bar = el("div", "ap-row");
    bar.append(btn("Send the selection to the stash", sendSelection, "small"), btn(trash ? "Back to the stash" : "Show the trash", function () { trash = !trash; q = ""; load(); }, "quiet small"));
    kids.push(bar);
    if (msg) { var m = el("p", "fine", msg); m.setAttribute("role", "status"); kids.push(m); }
    kids.push(el("p", "fine", total + (total === 1 ? " item" : " items") + (q ? " match." : ".")));
    if (items.length) { var ul = el("ul", "ap-list"); items.forEach(function (it, i) { ul.append(row(it, i)); }); kids.push(ul); }
    else kids.push(el("p", "fine", q ? "Nothing in the stash matches." : trash ? "The stash trash is empty." : "The stash is empty."));
    var wrap = el("div", "ap-wrap"); wrap.append.apply(wrap, kids);
    root.replaceChildren(wrap);
  }
  S.stash = { show: function (panel) { root = panel; msg = ""; load(); }, sendSelection: sendSelection };
})();
