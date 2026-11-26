/* Scríobh writing view: the words that comments are on, marked in the text. Each open comment the person can read has a faint fill and a hairline
   under the words it is on (the server's place for them, checked against the text the editor holds). The marks are decorations only: they are
   never in the Markdown, the saved text, an export or the mirror, and a key carries them with the words it is typed among (mapSet), without
   drawing them again. They are drawn only from saved text, while the editor holds exactly what is saved. A click on a mark opens its comment in
   the Comments tab: the card is marked current and scrolled to, and focus and the caret stay where the click put them. A switch in the tab hides
   the marks, and the choice is kept in this browser. Loaded after the page (data-ext) when the chapter has a comment the person can read, else by
   js/review.js the first time the tab has one. Its own copy of the place code (bodyOf, toLive), because js/cwrite.js needs the reading view's script
   and is loaded only with the tab. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, box = doc.getElementById("write");
  if (!W || !box || S.cmarks) return;
  var ed = W.editor, view = ed.view, base = "/api/v1/projects/" + W.cfg.project + "/comments", KEY = "scriobh-marks";
  var set = null, setDoc = null, on = true, timer = 0, pending = "", sw = doc.getElementById("cm-on"), wrap = doc.getElementById("cm-off"), panel = doc.getElementById("ipanel-comments");
  try { on = win.localStorage.getItem(KEY) !== "off"; } catch (e) { on = true; } // not kept: the marks are on
  // The set, carried through every edit since it was drawn: its places are those of the text as it is now.
  function current() {
    if (!set) return null;
    if (setDoc !== view.state.doc) { set = ed.alts.mapSet(set, setDoc, view.state.doc); setDoc = view.state.doc; }
    return set;
  }
  function decorFn() { return on ? current() : null; }
  S.decorate(ed, "cmarks", decorFn);
  // The saved text and the places in it (as js/lab.js and js/cwrite.js place them).
  function bodyOf(md) { var t = md.replace(/\n+$/, ""), i = t.lastIndexOf("\n"), l = i >= 0 ? t.slice(i + 1) : t; return l.indexOf("<!-- scriobh:alts ") === 0 ? md.slice(0, md.lastIndexOf(l)) : md; }
  // The places rangesOf gives are in the document the stored Markdown parses to, which has lost the spaces a paragraph starts and ends with
  // where the editor's own document keeps them: carried over block by block, they are the editor's own.
  function toLive(rs) {
    var blocks = [], cum = 0;
    view.state.doc.descendants(function (n, pos) {
      if (!n.isTextblock) return true;
      var l = 0, tr = 0;
      if (!n.type.spec.code) {
        var tx = n.textBetween(0, n.content.size, "", "￼");
        l = (/^[ \t]+/.exec(tx) || [""])[0].length; tr = l === tx.length ? 0 : (/[ \t]+$/.exec(tx) || [""])[0].length;
      }
      blocks.push({ pcs: pos + 1 - cum, add: cum + l }); cum += l + tr;
      return false;
    });
    function at(p) {
      var lo = 0, hi = blocks.length - 1, k = -1;
      while (lo <= hi) { var mid = (lo + hi) >> 1; if (blocks[mid].pcs <= p) { k = mid; lo = mid + 1; } else hi = mid - 1; }
      return k < 0 ? p : p + blocks[k].add;
    }
    return rs.map(function (r) { return r && { from: at(r.from), to: at(r.to) }; });
  }
  // The open threads of this chapter that this person can read, every page of them (the server answers each person with what that person may see).
  function fetchOpen() {
    var out = [];
    function page(off) {
      return win.fetch(base + "?document_id=" + encodeURIComponent(W.cfg.id) + "&plain=true&status=open&limit=200&offset=" + off, { credentials: "same-origin", headers: { Accept: "application/json" } })
        .then(function (r) { return r.ok ? r.json() : null; }, function () { return null; })
        .then(function (j) {
          if (!j || !j.items) return null;
          out = out.concat(j.items);
          return j.items.length === 200 && off < 800 ? page(off + 200) : out;
        });
    }
    return page(0);
  }
  // Only saved text is drawn from: nothing while a key is going down, and the answer is used only if the editor and the text are the same as when it was asked for.
  function refresh() {
    win.clearTimeout(timer);
    if (!ed.alts) { later(300); return; } // the alternatives' script arrives with this one, in no fixed order
    if (W.unsaved()) { later(1500); return; }
    var d0 = view.state.doc, r0 = W.rev, md0 = W.serverMd;
    fetchOpen().then(function (list) {
      if (!list) { later(4000); return; }
      if (view.state.doc !== d0 || W.rev !== r0 || W.unsaved() || W.serverMd !== md0) { later(900); return; }
      draw(list);
    });
  }
  function later(ms) { win.clearTimeout(timer); timer = win.setTimeout(refresh, ms); }
  function draw(list) {
    var open = list.filter(function (t) { return t.status === "open" && !t.deleted && t.anchor && t.anchor.state === "active"; });
    var rs = open.length ? toLive(ed.alts.rangesOf(bodyOf(W.serverMd), open.map(function (t) { return { start: t.anchor.start, end: t.anchor.end }; }))) : [];
    var E = win.scriobhEditor, decos = [];
    open.forEach(function (t, i) {
      var r = rs[i];
      // a mark whose words are not the words the server found is not drawn at all
      if (!r || r.to <= r.from || view.state.doc.textBetween(r.from, r.to, "", "￼") !== (t.anchor.quote_plain || "")) return;
      decos.push(E.Decoration.inline(r.from, r.to, { class: "cm-mark", "data-cm": t.thread_id }, { id: t.thread_id }));
    });
    // the same marks as those already drawn: nothing is replaced, so nothing is redrawn
    var have = current() ? current().find().map(function (d) { return d.spec.id + ":" + d.from + "-" + d.to; }).join(" ") : "";
    if (have !== decos.map(function (d) { return d.spec.id + ":" + d.from + "-" + d.to; }).join(" ")) {
      set = E.DecorationSet.create(view.state.doc, decos); setDoc = view.state.doc;
      S.decorate(ed, "cmarks", decorFn);
    }
    if (wrap) wrap.hidden = !open.length;
  }
  function show(v) {
    on = !!v;
    try { win.localStorage.setItem(KEY, on ? "on" : "off"); } catch (e) { /* not kept */ }
    if (sw) sw.checked = on;
    S.decorate(ed, "cmarks", decorFn);
    if (on) refresh();
  }
  if (sw) { sw.checked = on; sw.addEventListener("change", function () { show(sw.checked); }); }
  // A click on a mark: its comment is opened in the Comments tab. The card is shown when it exists, and marked current for a moment.
  function reveal() {
    var c = pending && doc.getElementById("rv-t-" + pending);
    if (!c) return;
    pending = "";
    c.scrollIntoView({ block: "nearest" });
    c.classList.add("is-current");
    win.setTimeout(function () { c.classList.remove("is-current"); }, 1500);
  }
  view.dom.addEventListener("click", function (e) {
    var m = e.target.closest && e.target.closest(".cm-mark");
    if (!m) return;
    pending = m.dataset.cm || "";
    if (S.inspector) S.inspector.open("comments");
    reveal();
  });
  if (panel) new win.MutationObserver(reveal).observe(panel, { childList: true, subtree: true }); // the card is there once the tab has loaded it
  doc.addEventListener("scriobh:saved", function () { if (set) later(1200); });
  doc.addEventListener("scriobh:event", function (e) { if (/^comment\./.test((e.detail && e.detail.type) || "")) later(400); }); // a shared comment of someone else
  S.cmarks = { refresh: refresh, show: show };
  refresh();
})();
