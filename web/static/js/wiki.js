/* Scríobh writing view: wiki links in the editor.
   - Typing [[ offers the titles of the project's documents in a keyboard-driven list (Up, Down, Enter or Tab,
     Escape). Nothing is inserted until it is accepted.
   - A link whose note exists reads as found (dotted underline); one without a note reads as missing (dashed,
     with a question mark), so the difference never rests on colour.
   - Ctrl or Command with a click, or Ctrl or Command with Enter while a link is selected, follows it:
     doc.resolve_link finds the note; for a missing one a small card offers "Create note", then goes there.
     Mod+Enter keeps inserting a scene break everywhere else.
   Everything here works through the editor's public handles (view, setDecorations); the editor bundle is unchanged. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, E = win.scriobhEditor;
  if (!W || !E || !W.editor || !W.editor.view) return;
  var ed = W.editor, view = ed.view, T = E.markdown.schema.nodes.wiki_link, api = W.api, canEdit = W.cfg.canEdit;
  var isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
  var el = function (tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  function norm(t) { return String(t || "").toLowerCase().replace(/\s+/g, " ").trim(); }
  function href(docId) { return "/p/" + W.cfg.slug + "/d/" + docId; }

  // ---- the project's titles ----------------------------------------------------------------------------------------
  var list = [], known = {}, loadedAt = 0, set = null, setDoc = null, setKnown = null;
  function refresh() {
    return api("GET", "/api/v1/projects/" + W.cfg.project + "/docs").then(function (r) {
      if (!r.ok || !r.json) return;
      var byId = {};
      (r.json.items || []).forEach(function (m) { byId[m.id] = m; });
      list = []; known = {};
      (r.json.order || []).forEach(function (id) {
        var m = byId[id];
        if (!m || !m.title || /[\[\]|]/.test(m.title)) return;
        list.push({ id: id, title: m.title, kind: m.kind });
        if (!known[norm(m.title)]) known[norm(m.title)] = id;
      });
      loadedAt = Date.now();
      S.decorate(ed, "wiki", decorate); // registering again makes the editor draw them again
    }, function () {});
  }
  S.wiki = { refresh: refresh };

  // ---- found and missing ---------------------------------------------------------------------------------------------
  function decorate(state) {
    if (set && setDoc === state.doc && setKnown === known) return set;
    var out = [];
    state.doc.descendants(function (n, pos) {
      if (n.type === T) {
        var ok = known[norm(n.attrs.target)];
        out.push(E.Decoration.node(pos, pos + n.nodeSize, ok ? { class: "wikilink-found" } : { class: "wikilink-missing", title: "No note with this title yet" }));
        return false;
      }
      return !n.isLeaf;
    });
    setDoc = state.doc; setKnown = known;
    set = out;
    return set;
  }

  // ---- the completion list -----------------------------------------------------------------------------------------
  var pop = el("ul", "wiki-pop"), live = el("p", "sr-only"), card = el("div", "wiki-card"), sheet = null;
  pop.id = "wiki-list"; pop.setAttribute("role", "listbox"); pop.setAttribute("aria-label", "Notes to link to"); pop.hidden = true;
  live.setAttribute("role", "status"); live.setAttribute("aria-live", "polite");
  card.id = "wiki-card"; card.setAttribute("role", "group"); card.setAttribute("aria-label", "Link"); card.hidden = true;
  doc.body.append(pop, live, card);
  if (win.CSSStyleSheet && doc.adoptedStyleSheets) { sheet = new win.CSSStyleSheet(); doc.adoptedStyleSheets = doc.adoptedStyleSheets.concat([sheet]); }
  // Below the caret, or above it when the space below is taken by the keyboard, the formatting bar and the tab bar.
  function place(target, pos, w) {
    if (!sheet) return;
    var c = view.coordsAtPos(pos), vw = win.innerWidth, vv = win.visualViewport, left = Math.max(8, Math.min(c.left, vw - w - 8));
    var room = (vv ? vv.offsetTop + vv.height : win.innerHeight) - 110, h = doc.getElementById(target).offsetHeight || 120;
    var top = c.bottom + 6 + h > room && c.top - 6 - h > 60 ? c.top - 6 - h : c.bottom + 6;
    sheet.replaceSync("#" + target + "{left:" + Math.round(left) + "px;top:" + Math.round(top) + "px;max-width:" + (vw - 16) + "px}");
    shown[target] = pos;
  }
  var shown = {};
  var ctx = null, items = [], active = 0;
  function context(state) {
    var s = state.selection;
    if (!s.empty || ed.mode() !== "rich") return null;
    var $f = s.$from;
    if (!$f.parent.isTextblock || $f.parent.type.spec.code) return null;
    var before = $f.parent.textBetween(Math.max(0, $f.parentOffset - 90), $f.parentOffset, null, "￼");
    var m = /\[\[([^\[\]|\n￼]{0,80})$/.exec(before);
    return m ? { q: m[1], from: $f.pos - m[1].length - 2, to: $f.pos } : null;
  }
  function choices(q) {
    var n = norm(q), first = [], rest = [];
    list.forEach(function (d) {
      var t = norm(d.title), at = n ? t.indexOf(n) : 0;
      if (at === 0) first.push(d); else if (at > 0) rest.push(d);
    });
    var out = first.concat(rest).slice(0, 8).map(function (d) { return { title: d.title, label: d.title, sub: d.kind.replace("_", " ") }; });
    if (n && !known[n] && !/[\[\]|]/.test(q)) out.push({ title: q.trim(), label: "Link to “" + q.trim() + "”", sub: "no note yet" });
    return out;
  }
  function draw() {
    pop.replaceChildren();
    items.forEach(function (c, i) {
      var li = el("li", "wiki-opt");
      li.id = "wiki-o" + i;
      li.setAttribute("role", "option");
      li.setAttribute("aria-selected", String(i === active));
      li.append(el("span", "wiki-t", c.label), el("span", "wiki-k", c.sub));
      li.addEventListener("pointerdown", function (e) { e.preventDefault(); });
      li.addEventListener("click", function () { active = i; accept(); });
      pop.append(li);
    });
    view.dom.setAttribute("aria-activedescendant", "wiki-o" + active);
  }
  function openList(c) {
    if (Date.now() - loadedAt > 30000) refresh();
    ctx = c;
    items = choices(c.q);
    if (!items.length) { closeList(); return; }
    if (active >= items.length) active = 0;
    var was = pop.hidden;
    pop.hidden = false;
    view.dom.setAttribute("aria-controls", "wiki-list");
    view.dom.setAttribute("aria-autocomplete", "list");
    draw();
    place("wiki-list", c.to, 260);
    if (was) live.textContent = items.length + (items.length === 1 ? " note" : " notes") + " to link to. Up and Down choose, Enter links, Escape closes.";
    else live.textContent = items[active].label;
  }
  function closeList() {
    if (pop.hidden) return;
    pop.hidden = true; ctx = null; active = 0;
    ["aria-controls", "aria-autocomplete", "aria-activedescendant"].forEach(function (a) { view.dom.removeAttribute(a); });
  }
  function accept() {
    var c = items[active], at = ctx;
    if (!c || !at) return;
    var tr = view.state.tr.replaceWith(at.from, at.to, T.create({ target: c.title, alias: null }));
    view.dispatch(tr.scrollIntoView());
    closeList();
    view.focus();
  }

  // ---- following a link ---------------------------------------------------------------------------------------------
  function selectedLink(state) {
    var s = state.selection, n = s.node || (s.to - s.from === 1 ? state.doc.nodeAt(s.from) : null);
    return n && n.type === T ? { node: n, pos: s.from } : null;
  }
  function showCard(l) {
    var title = l.node.attrs.target, found = known[norm(title)];
    card.replaceChildren(el("span", "wiki-name", "“" + (title.length > 40 ? title.slice(0, 40) + "…" : title) + "”"));
    var b;
    if (found) b = mkBtn("Open note", function () { go(title, false); });
    else if (canEdit) b = mkBtn("Create note", function () { go(title, true); });
    else card.append(el("span", "wiki-k", "no note yet"));
    if (b) card.append(b);
    card.hidden = false;
    place("wiki-card", l.pos, 240);
    return b;
  }
  function mkBtn(label, fn) { var b = el("button", "btn small", label); b.type = "button"; b.addEventListener("click", fn); b.addEventListener("pointerdown", function (e) { e.preventDefault(); }); return b; }
  function hideCard() { card.hidden = true; }
  function go(title, create) {
    var cached = known[norm(title)];
    var done = function (id) { W.go(href(id)); };
    if (!canEdit) { if (cached) done(cached); else if (S.toast) S.toast("There is no note called “" + title + "” yet."); return; }
    api("POST", "/api/v1/projects/" + W.cfg.project + "/links/resolve", { title: title, create: !!create }).then(function (r) {
      if (!r.ok || !r.json) { if (S.toast) S.toast("Could not follow that link."); return; }
      if (r.json.found && r.json.item) { if (r.json.created) refresh(); done(r.json.item.id); return; }
      var b = showCard({ node: { attrs: { target: title } }, pos: view.state.selection.from });
      if (b) b.focus();
    }, function () { if (S.toast) S.toast("Could not reach the server."); });
  }
  function follow(l) {
    var title = l.node.attrs.target;
    if (known[norm(title)] || !canEdit) go(title, false);
    else { var b = showCard(l); if (b) b.focus(); }
  }

  // ---- wiring ----------------------------------------------------------------------------------------------------------
  var dispatch = view.dispatch;
  view.dispatch = function (tr) {
    dispatch(tr);
    if (!tr.docChanged && !tr.selectionSet) return;
    var c = context(view.state);
    if (c) { hideCard(); openList(c); } else { closeList(); var l = selectedLink(view.state); if (l && view.hasFocus()) showCard(l); else hideCard(); }
  };
  view.dom.addEventListener("keydown", function (e) {
    if (e.isComposing) return;
    if (!pop.hidden) {
      var k = e.key;
      if (k === "ArrowDown" || k === "ArrowUp") { e.preventDefault(); e.stopPropagation(); active = (active + (k === "ArrowDown" ? 1 : items.length - 1)) % items.length; draw(); live.textContent = items[active].label; return; }
      if (k === "Enter" || k === "Tab") { e.preventDefault(); e.stopPropagation(); accept(); return; }
      if (k === "Escape") { e.preventDefault(); e.stopPropagation(); closeList(); return; }
    }
    if (e.key === "Enter" && (isMac ? e.metaKey : e.ctrlKey)) {
      var l = selectedLink(view.state);
      if (l) { e.preventDefault(); e.stopPropagation(); follow(l); }
    } else if (e.key === "Escape" && !card.hidden) { hideCard(); }
  }, true);
  card.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); hideCard(); view.focus(); } });
  view.dom.addEventListener("click", function (e) {
    var a = e.target.closest && e.target.closest(".wikilink");
    if (!a) return;
    if (isMac ? e.metaKey : e.ctrlKey) { e.preventDefault(); go(a.getAttribute("data-target"), false); }
  });
  view.dom.addEventListener("blur", function () { setTimeout(function () { if (!card.contains(doc.activeElement)) { hideCard(); closeList(); } }, 120); });
  // A scroll or a resize moves the page under the list: it follows the caret instead of closing.
  function follow2() {
    if (!pop.hidden && ctx) place("wiki-list", ctx.to, 260);
    else if (!card.hidden) place("wiki-card", shown["wiki-card"], 240);
  }
  win.addEventListener("resize", follow2);
  win.addEventListener("scroll", follow2, true);
  S.decorate(ed, "wiki", decorate);
  refresh();
})();
