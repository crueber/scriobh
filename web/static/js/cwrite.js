/* Scríobh writing view: writing a comment from the Comments tab, for yourself first. A comment goes on the chapter as a whole (a
   box at the top of the tab) or on selected words (Alt+Shift+M, the format bar's button, the palette). "Only me" is the default:
   nobody else can see it, nobody is told, and it is not in the manuscript, its history or any export. The words are named in the
   stored Markdown by the editor's own serializer, once what was typed is saved (the way the alternatives and the split do), and the
   comment is posted with its quote and the words around it, never with offsets. Nothing is dispatched to the editor: the words, the
   caret and the page stay as they were, and focus goes back to the text. The words come from the page (data-msg).
   js/review.js loads this file the first time the tab is shown and gives it its helpers (S.review). */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, R = S && S.review;
  if (!W || !R || !W.editor) return;
  var ed = W.editor, view = ed.view, M = {}, T0 = doc.getElementById("cw-msg"), $ = R.$, el = R.el, drafts = { sel: "", chap: "" }, editing = {}, cw = null, cur = null, busy = false;
  try { M = JSON.parse((T0 && T0.dataset.msg) || "{}"); } catch (e) { M = {}; }
  function T(k, d, v) { var s = M[k] || d; if (v) for (var n in v) s = s.split("{" + n + "}").join(v[n]); return s; }
  var phone = win.matchMedia("(max-width: 639px)"), home = null; // phone: the inspector is a sheet over the page; home: where the page was before this feature moved it

  // ---- words held or flashed in the text: one set each, carried through every edit (nothing is drawn or rebuilt by a key) ----------
  function Mark(name, cls) {
    var set = null, at = null;
    function fn() {
      if (!set) return [];
      if (at !== view.state.doc) { set = ed.alts.mapSet(set, at, view.state.doc); at = view.state.doc; }
      return set;
    }
    return {
      put: function (from, to, doc0) {
        var E = win.scriobhEditor, d = doc0 || view.state.doc;
        set = E.DecorationSet.create(d, [E.Decoration.inline(from, to, { class: cls })]); at = d;
        S.decorate(ed, name, fn);
      },
      clear: function () { if (set) { set = null; S.decorate(ed, name, fn); } },
      range: function () { var f = set ? fn().find() : []; return f.length ? { from: f[0].from, to: f[0].to } : null; }
    };
  }
  var held = Mark("cmt", "cmt-pending"), flash = Mark("cmtf", "cmt-flash");

  // ---- the words, from where they are selected -------------------------------------------------------------------------------
  // A selection inside one paragraph, spaces trimmed off both ends; or a message in words.
  function grab(w) {
    if (!w || w.empty) return null;
    if (w.src) {
      var t = w.doc.slice(w.from, w.to), a = w.from + (t.length - t.trimStart().length), z = w.to - (t.length - t.trimEnd().length);
      return z > a ? { src: 1, from: a, to: z, text: w.doc.slice(a, z), md: w.doc } : null;
    }
    var d = w.doc, $f = d.resolve(w.from), $t = d.resolve(w.to);
    if (!$f.sameParent($t)) return { err: T("one_block", "Select inside one paragraph. A comment goes on a stretch of one paragraph, or on the chapter.") };
    if ($f.parent.type.spec.code) return null;
    var x = d.textBetween(w.from, w.to, "", "￼"), lead = x.length - x.trimStart().length, tail = x.length - x.trimEnd().length;
    return w.to - tail > w.from + lead ? { from: w.from + lead, to: w.to - tail, text: x.trim(), doc: d } : null;
  }

  // ---- the saved text and the places in it (as js/lab.js does it) ------------------------------------------------------------
  function bodyOf(md) { var t = md.replace(/\n+$/, ""), i = t.lastIndexOf("\n"), l = i >= 0 ? t.slice(i + 1) : t; return l.indexOf("<!-- scriobh:alts ") === 0 ? md.slice(0, md.lastIndexOf(l)) : md; }
  // The places rangesOf gives are in the document the stored Markdown parses to, which has lost the spaces a paragraph starts and
  // ends with where the editor's own document keeps them: carried over block by block they are the editor's own.
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
  function place(list) { return toLive(ed.alts.rangesOf(bodyOf(W.serverMd), list.map(function (a) { return { start: a.start, end: a.end }; }))); }

  // ---- the form: words, and who can see them ---------------------------------------------------------------------------------
  // "Only me" unless the person says otherwise; the choice exists only where someone else is on the project. A private comment has
  // no @ picker and mentions no one, so the picker is off while "Only me" is chosen.
  function who(id) {
    var fs = el("fieldset", "cw-vis"), only = el("p", "fine", T("only_you_see", "Only you can see this.")), rs = el("div"), val = "private";
    fs.appendChild(el("legend", null, T("who", "Who can see this")));
    [["private", T("only_me", "Only me")], ["shared", T("people", "People on this project")]].forEach(function (o) {
      var l = el("label", "check"), i = el("input"); i.type = "radio"; i.name = "cw-v-" + id; i.value = o[0]; i.checked = o[0] === "private";
      i.addEventListener("change", function () { val = o[0]; });
      l.appendChild(i); l.appendChild(el("span", null, o[1])); rs.appendChild(l);
    });
    rs.appendChild(el("p", "fine", T("no_undo", "Once shared it cannot be made private again. You can delete it.")));
    rs.hidden = true; fs.appendChild(only); fs.appendChild(rs);
    R.people().then(function (ps) { if (ps.some(function (p) { return p.user_id !== R.me; })) { rs.hidden = false; only.hidden = true; } });
    return { node: fs, value: function () { return val; } };
  }

  // ---- the composer: on the chapter, or on selected words ----------------------------------------------------------------------
  function build(sel, focus) {
    var kind = sel ? "sel" : "chap", old = cw.querySelector("textarea");
    if (old) drafts[cur ? "sel" : "chap"] = old.value;
    cw.replaceChildren();
    cur = sel || null;
    var id = "cw-" + kind, v = who(id), q = sel && el("p", "rv-quote", "“" + sel.text + "”");
    if (q) q.id = id + "-q";
    var fm = R.form(sel ? T("sel_label", "Your comment on these words") : T("chap_label", "Comment on this chapter"), id, T("add", "Add comment"), function (body, ids) {
      return submit(body, v.value(), ids);
    }, function () { drafts[kind] = fm.ta.value; if (sel) held.clear(); build(null); back(); }, function () { return v.value() === "private"; });
    fm.form.insertBefore(v.node, fm.form.querySelector(".field-error"));
    fm.form.appendChild(el("p", "fine", T("note", "Stays beside your text, out of the manuscript and its history. For a note inside the text itself, use a private note (%%like this%%).")));
    fm.ta.value = drafts[kind]; fm.ta.rows = 3;
    if (q) { fm.ta.setAttribute("aria-describedby", q.id); cw.appendChild(q); }
    cw.appendChild(fm.form);
    if (focus) {
      // the composer is the top of the tab: if the tab was scrolled down the list, it is scrolled back (the tab's own, never the page)
      for (var e = cw; e; e = e.parentElement) { if (e.scrollTop) e.scrollTop = 0; if (e.id === "inspector") break; }
      if (sel) reveal();
      fm.ta.focus({ preventScroll: true });
    }
  }
  // Back to the text, the caret and the page where they were; on a phone the sheet is put away first (focus is out of it by then). The page is put
  // back where it was before this feature moved it (the words lifted above the sheet), unless the writer has moved it since.
  function back() {
    var x = win.scrollX, y = win.scrollY;
    S.inspector.toEditor();
    if (win.scrollY !== y || win.scrollX !== x) win.scrollTo(x, y); // putting focus back must not move the page
    if (phone.matches) S.inspector.close(false);
    restore();
  }
  function restore() {
    if (home && Math.abs(win.scrollY - home.at) <= 1 && Math.abs(win.scrollX - home.atx) <= 1) win.scrollTo(home.x, home.y);
    home = null;
  }
  // Words under the bottom sheet on a phone are brought into the free upper part, as the reading view's reveal does: the one scroll the
  // feature ever causes, and none when they are in view already. (Only where there is a sheet: the inspector is a column beside the page
  // above 639 px, and nothing covers the words.)
  function lift(n) {
    var e = n.nodeType === 1 ? n : n.parentElement, b = e.getBoundingClientRect();
    if (phone.matches && (b.bottom > win.innerHeight * 0.4 || b.top < 0)) {
      var x = win.scrollX, y = win.scrollY;
      win.scrollBy(0, b.top - win.innerHeight * 0.1);
      if (!home) home = { x: x, y: y };
      home.at = win.scrollY; home.atx = win.scrollX;
    }
  }
  function reveal() {
    var r = held.range();
    try { if (r) lift(view.domAtPos(r.from).node); } catch (x) { /* the words stay where they are */ }
  }

  // ---- posting -----------------------------------------------------------------------------------------------------------------
  function post(body, vis, ids, src, retry) {
    var o = { document_id: W.cfg.id, body: body, visibility: vis, mentions: vis === "shared" && ids.length ? ids : undefined };
    if (src) { o.quote = src.quote; o.before = src.before; o.after = src.after; if (src.plain) o.plain = true; }
    return R.api("POST", R.base + "/comments", o).then(function (r) {
      // the same words twice: say which, with more of the text around them
      return !r.ok && retry && /occurs \d+ times/.test(R.why(r)) ? retry() : r;
    });
  }
  function finish(r) {
    if (!r.ok || !r.json) return R.why(r);
    drafts[cur ? "sel" : "chap"] = ""; // what was posted; a half-written comment of the other kind is still there
    held.clear(); cur = null; cw.replaceChildren(); build(null);
    R.added(r.json.item); R.say(T("added", "Comment added."));
    back();
  }
  function submit(body, vis, ids) {
    var A = S.alts;
    if (busy) return Promise.resolve();
    if (!cur) return post(body, vis, ids).then(finish);
    if (!A && !cur.src) return Promise.resolve(T("loading", "The comment tool is still loading. Try again in a moment."));
    busy = true;
    var done = function (r) { busy = false; return r && r.err ? r.err : finish(r); }, bad = function (m) { return { err: m }; };
    if (cur.src) { // the Source view: the caret's place is the offset in the Markdown
      var md = cur.md, at = function (c) { return { quote: cur.text, before: md.slice(Math.max(0, cur.from - c), cur.from), after: md.slice(cur.to, cur.to + c) }; };
      return W.flushAll().then(function (ok) {
        var s = $(".scriobh-source:not([hidden])");
        if (!ok) return bad(T("unsaved", "Your text is not saved yet, so the comment was not added. Try again in a moment."));
        if (!s || s.value !== md) return bad(T("gone", "Those words are gone from the chapter. Select them again."));
        return post(body, vis, ids, at(24), function () { return post(body, vis, ids, at(160)); });
      }).then(done);
    }
    return named().then(function (r) {
      if (!r.spec) return done(r);
      var p = r.spec;
      return post(body, vis, ids, p[0], function () { return post(body, vis, ids, p[1]); }).then(function (x) {
        if (!x.ok && p[0].plain && x.status === 422) return done(bad(T("not_exact", "Those words could not be found exactly in the saved text, and the chapter was not rewritten to make them fit. Nothing was changed."))); // the words are as a reader sees them, not as the Markdown spells them
        return done(x);
      });
    });
  }
  // The words, named in the saved text, with the editor held only while that is done (the saved text is not rewritten for it: what is not saved
  // yet is saved, and nothing else is written; where the editor spells the chapter otherwise than the saved text does, the words are named as a
  // reader sees them). Then the editor is let go, and the comment is posted with the quote and the words around it, found at any revision.
  function named() {
    var A = S.alts, bad = function (m) { return { err: m }; };
    if (!A) return Promise.resolve(bad(T("loading", "The comment tool is still loading. Try again in a moment.")));
    return A.exclusive(function () {
      return W.flushAll().then(function (saved) {
        if (!saved) return bad(T("unsaved", "Your text is not saved yet, so the comment was not added. Try again in a moment."));
        var rg = held.range();
        if (!rg) return bad(T("gone", "Those words are gone from the chapter. Select them again."));
        var p = spec(rg);
        return p ? { spec: p } : bad(T("unnamed", "Those words could not be named exactly. Select a little more or less of them."));
      });
    }).then(function (r) { return r && (r.spec || r.err) ? r : { ok: false }; });
  }
  // [the words with 24 characters around them, with 160]: as the stored Markdown spells them when the editor spells it alike, else as read.
  function spec(rg) {
    if (S.alts.placed()) {
      var a = ed.alts.sourceOf(rg.from, rg.to), b = a && ed.alts.sourceOf(rg.from, rg.to, 160);
      return a && [a, b || a];
    }
    var d = view.state.doc, $f = d.resolve(rg.from), t = $f.parent.textBetween(0, $f.parent.content.size, "", "\ufffc"), i = rg.from - $f.start(), j = rg.to - $f.start();
    if (t.slice(i, j).indexOf("\ufffc") >= 0) return null;
    function at(c) { return { plain: true, quote: t.slice(i, j), before: t.slice(Math.max(0, i - c), i).replace(/^ +/, ""), after: t.slice(j, j + c).replace(/ +$/, "") }; }
    return [at(24), at(160)];
  }

  // ---- the tab asks for a composer (S.commentsWant is the editor's selection at the key or the tap) -------------------------------
  function show(panel) {
    if (!cw) {
      cw = el("div", "cw"); cw.id = "cw"; panel.insertBefore(cw, panel.firstChild);
      build(null);
    }
    if (phone.matches) doc.body.classList.add("cw-pad"); // the page can be scrolled until its last line is above the sheet (and stays so: closing the sheet moves nothing)
    var w = S.commentsWant; S.commentsWant = null;
    if (!w) return;
    home = null;
    var sel = W.cfg.readonly === "length" && !w.src ? null : grab(w);
    if (w.empty) { build(null, true); return; }
    if (!sel || sel.err) {
      // the reason is said to a screen reader and shown in the composer, which is what it is about (a comment on the chapter instead)
      var why = sel ? sel.err : W.cfg.readonly === "length" ? T("long", "This chapter is too long to open for editing: comment on it as a whole here, or select the words in the Source view.") : T("no_sel", "Select some words in the chapter first.");
      R.say(why); build(null, true); cw.insertBefore(el("p", "fine", why), cw.firstChild); return;
    }
    if (!sel.src) held.put(sel.from, sel.to, sel.doc); // the words stay marked while focus is in the tab
    build(sel, true);
  }

  // ---- what a card offers beside Reply and Resolve ----------------------------------------------------------------------------
  function edit(li, t, restore) {
    if ($(".cw-edit", li)) { $(".cw-edit textarea", li).focus(); return; }
    var id = t.thread_id, fm = R.form(T("edit_label", "Edit your comment"), "cw-e-" + id, T("save", "Save"), function (body, ids) {
      return R.api("PATCH", R.base + "/comments/" + id, { body: body, mentions: t.visibility === "shared" && ids.length ? ids : undefined }).then(function (r) {
        if (!r.ok || !r.json) return R.why(r);
        delete editing[id]; R.replace(r.json.item); R.draw(true); R.say(T("saved", "Saved.")); R.focusCard(id);
      });
    }, function () { delete editing[id]; fm.form.remove(); li.focus(); }, function () { return t.visibility === "private"; });
    fm.form.classList.add("cw-edit");
    fm.ta.value = editing[id] != null ? editing[id] : t.body; editing[id] = fm.ta.value;
    fm.ta.addEventListener("input", function () { editing[id] = fm.ta.value; });
    li.appendChild(fm.form);
    if (!restore) fm.ta.focus();
  }
  // Show in the text: scrolls to the words and marks them for two seconds. The caret is not moved and the editor is not focused. The
  // place is read again from the server, and used only when the editor holds exactly the text the server holds.
  function showIn(t) {
    var go = W.unsaved() ? W.flushAll() : Promise.resolve(true);
    go.then(function (ok) {
      if (!ok || W.unsaved()) { R.say(T("show_unsaved", "Your text is not saved yet.")); return; }
      return Promise.all([R.api("GET", R.base + "/comments?plain=true&status=all&limit=200&document_id=" + W.cfg.id), R.api("GET", "/api/v1/docs/" + W.cfg.id)]).then(function (a) {
        var f = a[0].ok && a[0].json && a[0].json.items.filter(function (x) { return x.thread_id === t.thread_id; })[0], g = a[1].ok && a[1].json && a[1].json.item;
        if (!f || !g || !f.anchor || f.anchor.state !== "active" || g.content !== W.serverMd || W.unsaved()) { R.say(T("changed", "The chapter changed since this was loaded. Reload it, then try again.")); return; }
        var rg = place([f.anchor])[0];
        if (!rg || view.state.doc.textBetween(rg.from, rg.to, "", "￼") !== (f.anchor.quote_plain || "")) { R.say(T("not_there", "Those words are not in the text as it is now.")); return; }
        flash.put(rg.from, rg.to);
        var n = view.domAtPos(rg.from).node, e = n.nodeType === 1 ? n : n.parentElement;
        var x0 = win.scrollX, y0 = win.scrollY;
        e.scrollIntoView({ block: "center" });
        home = { x: x0, y: y0, at: win.scrollY, atx: win.scrollX };
        lift(e);
        var off = function () { flash.clear(); view.dom.removeEventListener("keydown", off); };
        view.dom.addEventListener("keydown", off); win.setTimeout(off, 2000);
      });
    });
  }
  // A lost comment: attach it to the words selected now, or keep it on the chapter. Nothing is moved by a guess.
  function attach(t) {
    S.alts.sync();
    var s = view.state.selection, sel = grab({ from: s.from, to: s.to, empty: s.empty, doc: view.state.doc });
    if (!sel || sel.err) { R.say(sel ? sel.err : T("no_sel", "Select some words in the chapter first.")); return; }
    held.put(sel.from, sel.to);
    named().then(function (r) {
      held.clear();
      if (!r.spec) return { err: r.err };
      var p = r.spec[0];
      return R.api("POST", R.base + "/comments/" + t.thread_id + "/reanchor", { quote: p.quote, before: p.before, after: p.after, plain: p.plain || undefined });
    }).then(function (r) { moved(r, T("attached", "Attached to the selected words.")); });
  }
  function moved(r, msg) {
    if (r.err || !r.ok || !r.json) { R.say(r.err || R.why(r)); return; }
    R.swap(r.json.item); R.say(msg); R.focusCard(r.json.item.thread_id);
  }
  function actions(t, act, li) {
    var at = act.firstChild, add = function (label, fn) { act.insertBefore(R.btn(label, fn), at); };
    if (t.can_edit) add(T("edit", "Edit"), function () { edit(li, t); });
    if (t.anchor && t.anchor.state === "active") add(T("show", "Show in the text"), function () { showIn(t); });
    if (t.anchor && t.anchor.state !== "active" && t.can_resolve) {
      add(T("attach", "Attach to the selected words"), function () { attach(t); });
      add(T("keep", "Keep on the chapter"), function () { R.api("POST", R.base + "/comments/" + t.thread_id + "/reanchor", { detach: true }).then(function (r) { moved(r, T("kept", "Kept on the chapter.")); }); });
    }
    if (editing[t.thread_id] != null) edit(li, t, true); // an unsent edit is still there after a redraw
  }
  S.cwrite = { show: show, actions: actions, place: place };
})();
