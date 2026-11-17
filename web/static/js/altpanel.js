/* Scríobh writing view: the Alternatives tab of the inspector. Every alternative of the open chapter with its wordings
   (use, edit, move, remove, add), the original always one button away, the lost anchors (an alternative whose text is
   gone) with a way to attach them to a new selection, and the ghosted text with Revive. It is a list over window.scriobh.alts
   (js/alts.js), which does the work and keeps the text and the server in step. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, root = null, undo = null, shown = false;
  var reduce = win.matchMedia && win.matchMedia("(prefers-reduced-motion: reduce)");
  var el = function (tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  var plain = function (t) { return String(t).replace(/[*_`~]|\[([^\]]*)\]\([^)]*\)/g, "$1"); };
  var cut = function (t, n) { t = plain(t); return t.length > n ? t.slice(0, n) + "…" : t; };
  function A() { return S.alts; }
  function btn(label, fn, cls, aria) {
    var b = el("button", "btn " + (cls || "quiet small"), label); b.type = "button";
    if (aria) b.setAttribute("aria-label", aria);
    b.addEventListener("click", fn); return b;
  }
  // Keep the keyboard where it was across a redraw: the same kind of button of the same alternative.
  function again(fn) {
    var a = doc.activeElement, key = a && root.contains(a) && a.dataset.k;
    return Promise.resolve(fn()).then(function () { draw(); var n = key && root.querySelector('[data-k="' + key + '"]'); if (n) n.focus(); });
  }
  function variant(a, v, i) {
    var li = el("li", "ap-var" + (v.id === a.chosen_variant_id ? " is-chosen" : "")), vs = a.variants, can = A().can;
    li.dataset.v = v.id;
    var txt = el("span", "ap-text", plain(v.text) || "(empty)");
    li.append(txt);
    var tags = el("span", "ap-tags");
    if (v.id === a.chosen_variant_id) tags.append(el("span", "insp-tag", "In the text"));
    if (v.original) tags.append(el("span", "insp-tag", "Original"));
    else if (v.source === "generated") tags.append(el("span", "insp-tag", "Suggested"));
    li.append(tags);
    var acts = el("span", "ap-acts");
    if (v.id !== a.chosen_variant_id && can) { var u = btn("Use", function () { again(function () { return A().choose(a.id, v.id); }); }, "small", "Use “" + cut(v.text, 30) + "” in the text"); u.dataset.k = "use" + v.id; acts.append(u); }
    if (can && !v.original) {
      var e = btn("Edit", function () { edit(li, a, v); }, "quiet small", "Edit “" + cut(v.text, 30) + "”"); e.dataset.k = "edit" + v.id; acts.append(e);
      var up = btn("Move up", function () { again(function () { return A().reorder(a.id, v.id, i > 1 ? vs[i - 2].id : ""); }); }, "quiet small", "Move “" + cut(v.text, 30) + "” up"); up.disabled = i === 0; up.dataset.k = "up" + v.id;
      var dn = btn("Move down", function () { again(function () { return A().reorder(a.id, v.id, vs[i + 1].id); }); }, "quiet small", "Move “" + cut(v.text, 30) + "” down"); dn.disabled = i === vs.length - 1; dn.dataset.k = "dn" + v.id;
      var rm = btn("Remove", function () { remove(li, a, v, i); }, "quiet small", "Remove “" + cut(v.text, 30) + "”"); rm.dataset.k = "rm" + v.id;
      acts.append(up, dn, rm);
    }
    li.append(acts);
    return li;
  }
  function edit(li, a, v) {
    var ta = el("textarea"), ok = btn("Save", function () { var t = ta.value.trim(); if (!t) return; again(function () { return A().editVariant(a.id, v.id, t); }); }, "solid small"), no = btn("Cancel", draw, "quiet small");
    ta.rows = 2; ta.value = v.text; ta.setAttribute("aria-label", "New words for this wording");
    ta.addEventListener("keydown", function (e) { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); ok.click(); } else if (e.key === "Escape") { e.preventDefault(); draw(); } });
    li.replaceChildren(ta, el("span", "ap-acts", "")); li.lastChild.append(ok, no);
    ta.focus(); ta.select();
  }
  // Remove with a short fade (none when motion is reduced) and a way back: adding the wording again where it was.
  function remove(li, a, v, i) {
    var prev = i > 0 ? a.variants[i - 1].id : "", text = v.text;
    li.classList.add("is-leaving");
    win.setTimeout(function () {
      again(function () { return A().removeVariant(a.id, v.id); }).then(function () {
        undo = { text: text, after: prev, aid: a.id };
        draw();
        var u = root.querySelector(".ap-undo button"); if (u) u.focus();
      });
    }, reduce && reduce.matches ? 0 : 180);
  }
  function card(a) {
    var li = el("li", "ap-card"), c = a.variants.filter(function (x) { return x.id === a.chosen_variant_id; })[0];
    li.dataset.a = a.id;
    var h = el("div", "ap-head"), title = el("strong", "", cut(c ? c.text : a.quote, 60));
    h.append(el("span", "insp-tag", a.scope), title, el("span", "fine", "Wording " + a.chosen_position + " of " + a.variants.length));
    li.append(h);
    var list = el("ol", "ap-vars"); a.variants.forEach(function (v, i) { list.append(variant(a, v, i)); }); li.append(list);
    if (A().can) {
      var f = el("form", "ap-add"), t = el("input"), s = el("button", "btn small", "Add a wording");
      t.type = "text"; t.setAttribute("aria-label", "Another wording"); t.placeholder = "Another wording"; t.autocomplete = "off"; s.type = "submit";
      f.addEventListener("submit", function (e) { e.preventDefault(); var x = t.value.trim(); if (x) { t.value = ""; again(function () { return A().addVariant(a.id, x); }); } });
      f.append(t, s); li.append(f);
    }
    var row = el("div", "ap-row");
    if (a.status === "active") row.append(btn("Show in the text", function () { A().show(a.id); }, "quiet small"));
    var orig = a.variants.filter(function (x) { return x.original; })[0];
    if (A().can && orig && orig.id !== a.chosen_variant_id) { var o = btn("Show original", function () { again(function () { return A().choose(a.id, orig.id); }); }, "small"); o.dataset.k = "orig" + a.id; row.append(o); }
    if (A().can) row.append(btn("Resolve", function (e) { win.scriobh.confirm(li, { msg: "Keep the wording now in the text and remove the other wordings?", ok: "Resolve", from: e.currentTarget, onOk: function () { again(function () { return A().del(a.id); }); } }); }, "quiet small", "Resolve: keep the wording in the text, drop the others"));
    li.append(row);
    return li;
  }
  function lost(a) {
    var li = el("li", "ap-card is-lost");
    li.append(el("p", "ap-head", "“" + cut(a.quote, 80) + "”"));
    var list = el("ol", "ap-vars"); a.variants.forEach(function (v) { var x = el("li", "ap-var"); x.append(el("span", "ap-text", plain(v.text))); if (v.original) x.append(el("span", "insp-tag", "Original")); list.append(x); }); li.append(list);
    if (A().can) {
      var row = el("div", "ap-row");
      row.append(btn("Attach to the selection", function () { again(function () { return A().reanchor(a.id); }); }, "small"), btn("Delete", function () { again(function () { return A().del(a.id); }); }, "quiet small"));
      li.append(row);
    }
    return li;
  }
  function draw() {
    if (!root || !S.alts) return;
    var items = A().items, act = items.filter(function (a) { return a.status === "active"; }), orph = items.filter(function (a) { return a.status !== "active"; });
    var kids = [el("h2", "ver-h", "Alternatives")];
    kids.push(el("p", "fine", A().can ? "Select a word, a sentence or a paragraph, then Add alternative (" + A().hint("KeyA") + "). Cycle the wordings in the text with " + A().hint("KeyN") + " and " + A().hint("KeyP") + "; " + A().hint("KeyO") + " brings the original back. The keys are by position: on another keyboard layout they sit under other letters, and every command is in the palette. A change made here (a choice, a ghost, the stash) takes the saved text back whole, so the editor's Undo cannot step back across it; the chapter's history can." : "The wordings kept for this chapter. You can read them but not change them."));
    if (A().can) kids.push(btn("Add alternative for the selection", function () { A().add(); }, "small"));
    if (undo) {
      var u = el("p", "ap-undo", "A wording was removed. "), b = btn("Undo", function () { var x = undo; undo = null; again(function () { return A().addVariant(x.aid, x.text, x.after); }); }, "small");
      b.dataset.k = "undo"; u.append(b); u.setAttribute("role", "status"); kids.push(u);
    }
    if (act.length) { var ul = el("ul", "ap-list"); act.forEach(function (a) { ul.append(card(a)); }); kids.push(ul); }
    else kids.push(el("p", "fine", "No alternatives in this chapter yet."));
    if (orph.length) {
      kids.push(el("h2", "ver-h", "Lost anchors"), el("p", "fine", "The text these were attached to is gone or changed too much. Their wordings are kept. Select new text and attach them."));
      var ol = el("ul", "ap-list"); orph.forEach(function (a) { ol.append(lost(a)); }); kids.push(ol);
    }
    var gs = A().ghosts, gi = A().ghostInfo;
    kids.push(el("h2", "ver-h", "Ghosted text"));
    if (gs.length) {
      kids.push(el("p", "fine", gi.words + (gi.words === 1 ? " word is" : " words are") + " ghosted: still in the chapter, not counted (" + gi.raw + " with them)."));
      var gl = el("ul", "ap-list");
      gs.forEach(function (g) {
        var li = el("li", "ap-card"), p = el("p", "ap-text", "“" + cut(g.preview, 100) + "”");
        li.append(p, el("span", "fine", g.words + (g.words === 1 ? " word" : " words")));
        if (A().can) { var r = btn("Revive", function () { again(function () { return A().revive({ ghost_ids: [g.id] }); }); }, "small", "Revive “" + cut(g.preview, 30) + "”"); r.dataset.k = "rv" + g.id; li.append(r); }
        gl.append(li);
      });
      kids.push(gl);
      if (A().can && gs.length > 1) kids.push(btn("Revive all", function () { again(function () { return A().revive({ all: true }); }); }, "quiet small"));
    } else kids.push(el("p", "fine", "Nothing is ghosted. Ghost text you are not ready to cut: it stays in the chapter, dimmed, and is not counted or exported unless you ask."));
    var wrap = el("div", "ap-wrap"); wrap.append.apply(wrap, kids);
    // What is typed in a card's "Another wording" box, and where the caret was, survives the redraw.
    var typed = {}, here = "", at = [0, 0];
    root.querySelectorAll(".ap-card[data-a] .ap-add input").forEach(function (i) {
      var k = i.closest(".ap-card").dataset.a;
      if (i.value) typed[k] = i.value;
      if (i === doc.activeElement) { here = k; at = [i.selectionStart, i.selectionEnd]; }
    });
    root.replaceChildren(wrap);
    Object.keys(typed).forEach(function (k) { var i = root.querySelector('.ap-card[data-a="' + k + '"] .ap-add input'); if (i) i.value = typed[k]; });
    if (here) { var f = root.querySelector('.ap-card[data-a="' + here + '"] .ap-add input'); if (f) { f.focus(); try { f.setSelectionRange(at[0], at[1]); } catch (e) { /* no caret */ } } }
    stale = false;
  }
  // A change that arrives while a wording is being edited (the textarea) waits for the editing to end: the redraw would take it away.
  var stale = false;
  function editing() { var a = doc.activeElement; return !!(a && root && root.contains(a) && a.tagName === "TEXTAREA"); }
  S.altpanel = {
    show: function (panel) {
      root = panel; shown = true;
      if (!S.alts) { root.textContent = "The alternatives could not be loaded. Your text is unchanged."; return; }
      if (!S.alts.__bound) {
        S.alts.__bound = true;
        S.alts.on(function () { if (root && !root.hidden) { if (editing()) stale = true; else draw(); } });
      }
      // Whichever element the panel is shown in: a redraw that waited for an edit to end happens when it loses focus.
      if (!root.__fo) { root.__fo = true; root.addEventListener("focusout", function () { if (stale) win.setTimeout(function () { if (stale && !editing()) draw(); }, 0); }); }
      draw();
      A().refresh();
    }
  };
})();
