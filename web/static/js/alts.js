/* Scríobh writing view: alternatives and ghosts in the text. A word, sentence or paragraph can carry other wordings; the
   writer cycles them in place, and the text always holds the chosen one. This file draws the marks (wavy underline, end dots),
   makes and cycles alternatives (keys, menu, toolbar, palette), ghosts and revives text, and keeps the document and the
   server in step by the save contract in docs/EDITOR-SPIKE.md. The side panel is js/altpanel.js.
   Everything is drawn as decorations through scriobh.decorate; nothing enters the schema, and nothing is drawn anew
   while keys go down. The server's history is the record of a choice: the editor's undo is not used for it. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, E = win.scriobhEditor;
  if (!W || !E || !W.editor || !W.editor.alts) return;
  var ed = W.editor, view = ed.view, A = ed.alts, id = W.cfg.id, can = W.cfg.canEdit && W.cfg.readonly !== "length";
  var el = function (tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  var OPEN = "<scriobh-ghost>", CLOSE = "</scriobh-ghost>", ALT = "<!-- scriobh:alts ";
  var items = [], idx = new Map(), marks = [], ghosts = [], ghostInfo = { words: 0, raw: 0 }, base = null, expect = null, set = null, setDoc = null;
  var hasGhost = false, queue = Promise.resolve(), lastKey = 0, timer = 0, listeners = [], dirtySet = false, touched = false;
  function setItems(l) { items = l; idx = new Map(); l.forEach(function (a) { idx.set(a.id, a); }); }
  var reduce = win.matchMedia && win.matchMedia("(prefers-reduced-motion: reduce)");
  // Where the keys are: the chords are by position (the A, N, P, O, G and S keys of a US keyboard), so on another layout they sit
  // under other letters. The browser can say which (Chromium does); where it cannot, the US letter is shown and the palette has
  // every command.
  var layout = null;
  if (win.navigator.keyboard && win.navigator.keyboard.getLayoutMap) win.navigator.keyboard.getLayoutMap().then(function (m) { layout = m; tell(); }, function () { /* the US letters */ });
  function hint(code) { var k = layout && layout.get(code); return "Alt+Shift+" + String(k || code.replace("Key", "")).toUpperCase(); }
  function say(m) { var l = doc.getElementById("live"); if (l) { l.textContent = ""; win.setTimeout(function () { l.textContent = m; }, 40); } }
  // One announcement: the toast is a live region of its own, so the page's status line is used only when there is no toast.
  function note(m) { if (S.toast) S.toast(m); else say(m); }
  function altLine(m) { var t = m.replace(/\n+$/, ""), l = t.slice(t.lastIndexOf("\n") + 1); return l.indexOf(ALT) === 0 ? l : ""; }
  function byId(x) { return idx.get(x) || null; }
  function bodyOf(md) { var ln = altLine(md); return ln ? md.slice(0, md.lastIndexOf(ln)) : md; }
  function trimEnd(s) { return s.replace(/\n+$/, ""); }
  function chosen(a) { for (var i = 0; i < a.variants.length; i++) if (a.variants[i].id === a.chosen_variant_id) return a.variants[i]; return null; }
  function changed(a) { var c = chosen(a); return !!c && !c.original; }
  function tell() { listeners.forEach(function (f) { try { f(); } catch (e) { /* a panel gone */ } }); }
  var lastNav = 0; // the last key that can move the browser's selection (an arrow, Home, End, a page key, select all)
  view.dom.addEventListener("keydown", function (e) { lastKey = Date.now(); if (/^(Arrow|Home$|End$|Page)/.test(e.key) || ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "a")) lastNav = lastKey; }, true);

  // ---- ghosts: pairs of tags, found the way count.js and the server find them ------------------------------------------
  function ghostPairs(d) {
    var out = [];
    d.descendants(function (n, pos) {
      if (!n.isTextblock) return true;
      var open = -1, openEnd = 0;
      n.forEach(function (c, off) {
        if (c.type.name !== "source_inline") return;
        var p = pos + 1 + off;
        if (c.attrs.raw === OPEN) { if (open < 0) { open = p; openEnd = p + c.nodeSize; } }
        else if (c.attrs.raw === CLOSE && open >= 0) { out.push({ a: open, b: openEnd, c: p, d: p + c.nodeSize }); open = -1; }
      });
      return false;
    });
    return out;
  }
  function ghostDecos(d) {
    var out = [];
    ghostPairs(d).forEach(function (g) {
      out.push(E.Decoration.node(g.a, g.b, { class: "scriobh-ghost-tag" }), E.Decoration.node(g.c, g.d, { class: "scriobh-ghost-tag" }));
      if (g.c > g.b) out.push(E.Decoration.inline(g.b, g.c, { class: "scriobh-ghost" }, { inclusiveStart: true, inclusiveEnd: true }));
    });
    return out;
  }
  function scanGhosts() {
    hasGhost = false;
    view.state.doc.descendants(function (n) { if (hasGhost) return false; if (n.type.name === "source_inline" && n.attrs.raw === OPEN) hasGhost = true; return !n.isTextblock || true; });
    set = null;
  }

  // ---- marks: where each alternative is, followed through edits between asks -------------------------------------------
  // Returns the one edit between the document drawn last and `d` as {s, a, b} (the old span [s, a) became [s, b)), or null.
  function follow(d) {
    var diff = null;
    if (!base || base === d) { base = d; return null; }
    var s = base.content.findDiffStart(d.content), e = base.content.findDiffEnd(d.content);
    if (s != null && e) {
      var over = s - Math.min(e.a, e.b);
      if (over > 0) { e.a += over; e.b += over; }
      diff = { s: s, a: e.a, b: e.b };
      var n = marks.length;
      marks = marks.map(function (m) {
        if (m.to < s) return m;
        if (m.to === s) { touched = true; return m; } // typed at its end: the server may take that into the wording
        if (m.from > e.a) return { id: m.id, from: m.from + e.b - e.a, to: m.to + e.b - e.a };
        if (m.from === e.a) { touched = true; return { id: m.id, from: m.from + e.b - e.a, to: m.to + e.b - e.a }; }
        return null; // an edit inside it: its place is asked for again after the next save
      }).filter(Boolean);
      if (marks.length < n) { dirtySet = true; if (!expect) later(2500); }
    }
    if (expect) { // a wording this file put in place: its range is known
      marks = marks.filter(function (m) { return m.id !== expect.id; });
      marks.push({ id: expect.id, from: expect.from, to: expect.to });
      expect = null; dirtySet = true;
    }
    base = d;
    return diff;
  }
  function dot(kind, on, label) {
    var d = el("span", "alt-dot alt-" + kind + (on ? " is-changed" : ""));
    d.setAttribute("aria-hidden", "true");
    d.contentEditable = "false";
    if (label) d.title = label;
    return d;
  }
  // Every decoration of the marks and the ghosts, made afresh. Only after an ask, an op or an edit that took a mark or a
  // ghost tag in or out: between those the set is carried over each edit (mapSet), so a key costs nothing in proportion to the
  // number of alternatives.
  function build(d) {
    var out = [];
    marks.forEach(function (m) {
      var a = byId(m.id), on = !!a && changed(a);
      out.push(E.Decoration.inline(m.from, m.to, { class: "scriobh-alt" + (on ? " is-changed" : ""), "data-alt": m.id, title: a ? "Alternative: wording " + a.chosen_position + " of " + a.variants.length : "Alternative" }));
      out.push(E.Decoration.widget(m.from, function () { return dot("start", on); }, { side: -1, key: "s" + m.id + on }));
      out.push(E.Decoration.widget(m.to, function () { return dot("end", on, a ? "Wording " + a.chosen_position + " of " + a.variants.length : ""); }, { side: 1, key: "e" + m.id + on + (a ? a.chosen_position : "") }));
    });
    if (hasGhost) out = out.concat(ghostDecos(d));
    return E.DecorationSet.create(d, out);
  }
  // Is a ghost tag in the span the edit took out of `old` or put into `now`?
  function tagIn(d, from, to) {
    var hit = false;
    try { d.nodesBetween(Math.max(0, from), Math.min(d.content.size, to), function (n) { if (n.type.name === "source_inline" && (n.attrs.raw === OPEN || n.attrs.raw === CLOSE)) hit = true; return !hit; }); } catch (e) { hit = true; }
    return hit;
  }
  function draw(state) {
    var prev = base, diff = follow(state.doc);
    if (set && setDoc === state.doc) return set;
    if (set && prev && !dirtySet && diff && !tagIn(prev, diff.s, diff.a) && !tagIn(state.doc, diff.s, diff.b)) set = A.mapSet(set, prev, state.doc);
    else { if (diff && (hasGhost || tagIn(state.doc, diff.s, diff.b))) scanGhosts(); set = build(state.doc); }
    dirtySet = false; setDoc = state.doc;
    return set;
  }
  function redraw() { set = null; S.decorate(ed, "alts", draw); }
  S.decorate(ed, "alts", draw);

  function later(ms) { win.clearTimeout(timer); timer = win.setTimeout(refresh, ms); }
  // Ask where everything is: after load, and after an edit that touched a mark. Never while keys are going down, and never
  // while anything is unsaved (the places are those of the stored text). The answer is about the text as it was when the
  // question went out: if the editor or the stored text has moved on since, the answer is thrown away and the question asked
  // again, since drawing it would put the marks on the wrong words.
  function refresh(force) {
    win.clearTimeout(timer);
    if (!force && (W.unsaved() || Date.now() - lastKey < 1500)) { later(1500); return Promise.resolve(); }
    var d0 = view.state.doc, rev0 = W.rev;
    return W.api("GET", "/api/v1/docs/" + id + "/alternatives").then(function (r) {
      if (!r.ok || !r.json) return;
      if (view.state.doc !== d0 || W.rev !== rev0 || W.unsaved()) { later(600); return; }
      setItems(r.json.items || []);
      var act = items.filter(function (a) { return a.status === "active"; });
      var md = W.serverMd, ln = altLine(md), body = ln ? md.slice(0, md.lastIndexOf(ln)) : md;
      var rs = act.length ? A.rangesOf(body, act.map(function (a) { return { start: a.start, end: a.end }; })) : [];
      marks = [];
      act.forEach(function (a, i) { if (rs[i]) marks.push({ id: a.id, from: rs[i].from, to: rs[i].to }); });
      base = view.state.doc; touched = false; redraw(); tell();
    }, function () { /* offline: the marks stay as they were */ });
  }
  function refreshGhosts() {
    return W.api("GET", "/api/v1/docs/" + id + "/ghosts").then(function (r) {
      if (!r.ok || !r.json) return;
      ghosts = r.json.items || []; ghostInfo = { words: r.json.ghosted_words || 0, raw: r.json.raw_word_count || 0 };
      tell();
    }, function () { /* offline */ });
  }

  // ---- ops: the editor is held, then the pending save is flushed, one op at a time ---------------------------------------
  // The editor is held from the moment an op is asked for until it has answered (and every op queued behind it): no edit of
  // the text goes through, from a key, the toolbar, undo, a paste or a drop, because the text that is about to be stored and
  // compared must not move. It stays editable and keeps focus (switching it to read-only would take the caret out of the text
  // and the next key would go nowhere); the editor itself refuses the edits (ed.alts.lock).
  var busy = false, pending = 0, told = false;
  // The hold takes effect once no composition is in progress (an input method's own steps cannot be refused without breaking
  // it), but is owed from the moment the op is asked for: `pending` counts it, and `engage` takes hold as soon as it can.
  function hold() { pending++; engage(); }
  function engage() {
    if (busy || !pending) return;
    if (view.composing) { win.setTimeout(engage, 50); return; }
    busy = true; told = false; view.dom.setAttribute("aria-busy", "true"); A.lock(true);
  }
  function release() { pending = Math.max(0, pending - 1); if (!pending) { busy = false; view.dom.removeAttribute("aria-busy"); A.lock(false); } }
  function refused() { if (!told) { told = true; note("Hold on: the chapter is being saved for that change. Your key was not taken."); } }
  view.dom.addEventListener("beforeinput", function (e) { if (busy) { e.preventDefault(); refused(); } }, true);
  // Keys that change the text, or move to the Source view, are refused; Tab and the arrows are left alone, so the keyboard is
  // never trapped in the text.
  view.dom.addEventListener("keydown", function (e) {
    if (!busy) return;
    if ((!e.ctrlKey && !e.metaKey && !e.altKey && (e.key.length === 1 || /^(Backspace|Delete|Enter)$/.test(e.key))) || ((e.ctrlKey || e.metaKey) && /^[zyxvbiuke]$/i.test(e.key))) { e.preventDefault(); refused(); }
  }, true);
  function fail(r) {
    var m = (r && r.json && r.json.error && r.json.error.message) || "That could not be done. Your text is unchanged.";
    if (r && r.status === 409 && r.json && r.json.error && r.json.error.data && r.json.error.data.current_revision) m = "This chapter changed somewhere else, so nothing was done. Reload it to see the latest.";
    if (/not in the document at this revision/i.test(m)) m = "That passage cannot be named: it runs across a line break in the saved text, or the text changed. Select inside one line, or use the Source view.";
    note(m);
    return { ok: false, message: m };
  }
  // A composition (an input method) cannot be refused, so an op waits for it to end before it reads the text.
  function composed() { return new Promise(function (res) { var n = 0; (function chk() { if (!view.composing || n++ > 100) res(); else win.setTimeout(chk, 100); })(); }); }
  // Run `fn` with the editor held and after the ops asked for before it. The answer of `fn` is the answer.
  // `edit(answer)`, when given, runs once `fn` has answered with ok, while nothing else can run: the one place the held editor
  // is let take an edit of its own (the text a stash took out of the chapter).
  function exclusive(fn, edit) {
    if (ed.mode() === "source") { note("Switch back to the text view (Ctrl or Command with E) first: the Source view is a plain text box, and this change is made in the text."); return Promise.resolve({ ok: false }); }
    hold();
    var go = function () {
      return composed().then(function () { engage(); return fn(); }).then(function (r) {
        try { if (edit && r && r.ok) { A.lock(false); try { edit(r); } finally { A.lock(true); } } } finally { release(); }
        return r;
      }, function () { release(); note("That could not be done. Your text is unchanged."); return { ok: false }; });
    };
    queue = queue.then(go, go).catch(function () { return { ok: false }; });
    return queue;
  }
  // The marks and the ghosts are asked for once an op has landed and the hold is over, so a slow answer cannot keep keys refused.
  function finish(p) {
    return p.then(function (r) {
      if (r && r.refresh) { r.refresh = false; return Promise.all([refresh(true), refreshGhosts()]).then(function () { return r; }); }
      return r;
    });
  }
  // Does the editor's serialization match the stored text? Offsets into the source mean the same in both only if it does.
  function placed() { return trimEnd(bodyOf(A.plain())) === trimEnd(bodyOf(W.serverMd)); } // the comment line is the server's, so it is left out of both
  // Offsets are only used on text both sides spell alike: if they do not yet (a chapter written elsewhere and opened without a
  // change), the editor's own spelling is stored first, and the places are checked again.
  function ensurePlaced() {
    if (placed()) return Promise.resolve(true);
    return W.flushAll(true).then(function (ok) { return ok && placed(); });
  }
  var NOT_PLACED = "That place could not be matched exactly with the saved text. Reload the chapter and try again.";
  // Run one op. `apply(res)` puts its answer in the document and returns the stored text (or null to ask for it). `body` may be a
  // function, asked once the editor is held and saved (it returns null, having said why, to stop). A passage the stored text does
  // not spell as the editor does (a chapter written elsewhere) is asked for once more after the editor's spelling has been stored.
  function op(method, path, body, apply, edit) {
    return finish(exclusive(function () {
      var again = false;
      var attempt = function () {
        return W.flushAll(again).then(function (saved) {
          if (!saved) { note("Your text is not saved yet, so nothing was done. Try again in a moment."); return { ok: false }; }
          var ver = A.version(); // held since before the flush: the text just stored is the text at this version
          var bb = typeof body === "function" ? body() : body;
          if (bb === null || bb === undefined) return { ok: false };
          return Promise.resolve(bb).then(function (bb2) {
            if (bb2 === null) return { ok: false };
            var b = Object.assign({ base_revision: W.rev, tab_id: W.tab }, bb2);
            return W.api(method, "/api/v1/docs/" + id + path, b).then(function (r) {
              if (!(r.ok && r.json)) {
                if (!again && r.json && r.json.error && /not in the document at this revision/i.test(r.json.error.message || "")) { again = true; return attempt(); }
                return fail(r);
              }
              var res = r.json, md = null;
              try { md = apply(res); } catch (e) { md = null; }
              if (md == null) return reload(res, ver).then(function () { return { ok: true, res: res, refresh: true }; });
              W.afterAlt(res, md, ver);
              return { ok: true, res: res, inplace: true };
            }, function () { note("Could not reach the server. Nothing was changed."); return { ok: false }; });
          });
        });
      };
      return attempt();
    }, edit && function (r) { if (r.inplace) edit(r); }));
  }
  // Run any request that edits this document (one from the stash panel, say) the way an alternatives op runs: the editor
  // held, the pending save flushed first, one at a time, the stored text taken whole afterwards. `call(rev, tab)` returns the
  // api answer (or null, having said why, to stop); its body must carry revision, comment and content.
  function run(call) {
    return finish(exclusive(function () {
      return W.flushAll().then(function (saved) {
        if (!saved) { note("Your text is not saved yet, so nothing was done. Try again in a moment."); return { ok: false }; }
        var ver = A.version();
        return Promise.resolve(call(W.rev, W.tab)).then(function (r) {
          if (r === null || r === undefined) return { ok: false };
          if (!(r.ok && r.json)) return fail(r);
          return reload(r.json, ver).then(function () { return { ok: true, res: r.json, refresh: true }; });
        }, function () { note("Could not reach the server. Nothing was changed."); return { ok: false }; });
      });
    }));
  }
  // Take the stored text whole, as the editor's: after an op that made or removed the comment or ghost tags, or when a
  // wording could not be put in place by itself. Never over text the editor holds that the server does not: that stays, the
  // next save meets the changed revision, and the conflict bar offers the choice.
  function reload(res, ver) {
    var at = view.state.selection.from;
    var done = function (c) {
      if (A.version() !== ver) { note("The chapter changed on the server at the same moment you typed. Your typing is kept; reload to see the change."); return null; }
      ed.setMarkdown(c, { force: true });
      try { var n = view.state.doc.content.size; A.select(Math.max(1, Math.min(at, n - 1)), Math.max(1, Math.min(at, n - 1))); } catch (e) { /* the caret stays at the start */ }
      W.afterAlt(res, c, ver);
      scanGhosts(); redraw();
      return null;
    };
    if (typeof res.content === "string") return Promise.resolve(done(res.content));
    // The change is made; only the new text could not be fetched.
    return W.api("GET", "/api/v1/docs/" + id).then(function (r) { return r.ok && r.json ? done(r.json.item.content) : gone(); }, gone);
    function gone() { note("The change was made, but the page could not fetch the new text. Reload the chapter to see it; a save from this page will meet the changed revision and ask what to do."); return null; }
  }
  function merged(res) { // the stored text after a choice, from the text before it: no download
    var md = W.serverMd, r = res.replaced;
    if (r) md = md.slice(0, r.start) + r.text + md.slice(r.end);
    var old = altLine(md);
    if (!old || !res.comment) return null;
    var i = md.lastIndexOf(old);
    return md.slice(0, i) + res.comment + md.slice(i + old.length);
  }

  // ---- reading the selection --------------------------------------------------------------------------------------------
  function blockOf(pos) {
    var $p = view.state.doc.resolve(pos);
    for (var d = $p.depth; d >= 0; d--) if ($p.node(d).isTextblock) return { node: $p.node(d), start: $p.start(d), depth: d };
    return null;
  }
  function textOf(b) { return b.node.textBetween(0, b.node.content.size, "", "￼"); }
  var seg = win.Intl && Intl.Segmenter ? { w: new Intl.Segmenter(undefined, { granularity: "word" }), s: new Intl.Segmenter(undefined, { granularity: "sentence" }) } : null;
  function edges(t, a, b, scope) {
    var lo = a, hi = b;
    if (scope === "word") {
      if (seg) {
        for (var x of seg.w.segment(t)) { var e = x.index + x.segment.length; if (x.isWordLike && e > a && x.index < b) { lo = Math.min(lo, x.index); hi = Math.max(hi, e); } }
      } else { while (lo > 0 && /\S/.test(t[lo - 1])) lo--; while (hi < t.length && /\S/.test(t[hi])) hi++; }
    } else if (scope === "sentence") {
      if (seg) { for (var y of seg.s.segment(t)) { var f = y.index + y.segment.length; if (f > a && y.index < b) { lo = Math.min(lo, y.index); hi = Math.max(hi, f); } } }
      else { var m, re = /[^.!?]+[.!?]*["')\]]*\s*/g; while ((m = re.exec(t))) { var g = m.index + m[0].length; if (g > a && m.index < b) { lo = Math.min(lo, m.index); hi = Math.max(hi, g); } } }
    }
    while (lo < hi && /\s/.test(t[lo])) lo++;
    while (hi > lo && /\s/.test(t[hi - 1])) hi--;
    return [lo, hi];
  }
  // The range an alternative of `scope` would take for the selection: inside one paragraph or heading, to word or sentence
  // edges, or the whole paragraph. A string is returned for a reason it cannot be made.
  function range(scope) {
    var s = view.state.selection, b1 = blockOf(s.from), b2 = blockOf(s.to);
    if (!b1 || !b2 || b1.start !== b2.start) return "Select inside one paragraph. An alternative cannot cross paragraphs.";
    if (b1.node.type.spec.code) return "An alternative cannot be made in code.";
    var t = textOf(b1), r = edges(t, s.from - b1.start, s.to - b1.start, scope);
    if (scope === "paragraph") {
      if (b1.depth !== 1) return "A paragraph alternative is a whole paragraph at the top level of the chapter, not one in a list or a quote.";
      r = edges(t, 0, t.length, "paragraph");
    }
    if (r[0] >= r[1]) return "Select some words first.";
    return { from: b1.start + r[0], to: b1.start + r[1], text: t.slice(r[0], r[1]) };
  }
  function defaultScope() {
    var s = view.state.selection;
    if (s.empty) return "word";
    var b = blockOf(s.from), t = b ? textOf(b) : "", sel = view.state.doc.textBetween(s.from, s.to, " ").trim();
    if (b && b.depth === 1 && sel === t.trim()) return "paragraph";
    return /\s/.test(sel) ? "sentence" : "word";
  }
  function markOf(aid) { for (var i = 0; i < marks.length; i++) if (marks[i].id === aid) return marks[i]; return null; }
  function markAt(pos) { for (var i = 0; i < marks.length; i++) if (pos >= marks[i].from && pos <= marks[i].to) return marks[i]; return null; }
  function current() { var s = view.state.selection, m = markAt(s.from); return m && markAt(s.to) === m ? m : null; }

  // ---- making an alternative -----------------------------------------------------------------------------------------------
  function create(scope, rg, wording, ctx) {
    var src = A.sourceOf(rg.from, rg.to, ctx || 24);
    if (!src) { var why = "That text could not be named exactly. Select a little more or less of it, or use the Source view."; note(why); return Promise.resolve({ ok: false, message: why }); }
    if (!/^\S[\s\S]*\S$|^\S$/.test(src.quote) || unbalanced(src.quote)) return Promise.resolve({ ok: false, message: "Select whole words and complete formatting: the selection cuts through emphasis or a link." });
    var body = { scope: scope, quote: src.quote, before: src.before, after: src.after, variants: [wording], with_content: true };
    return op("POST", "/alternatives", body, function () { return null; }).then(function (r) {
      // The same words twice: say which, with more of the text around them.
      return !r.ok && /occurs \d+ times/.test(r.message || "") && !ctx ? create(scope, rg, wording, 160) : r;
    });
  }
  function unbalanced(q) {
    var n = function (re) { return (q.replace(/\\./g, "").match(re) || []).length; };
    return n(/\*/g) % 2 || n(/`/g) % 2 || n(/~~/g) % 2 || n(/\[/g) !== n(/\]/g);
  }
  var pop = null, popFor = null;
  function closePop(focus) { if (pop) { pop.remove(); pop = null; popFor = null; if (focus) view.focus(); } }
  // Floating things (the add form, the cycler, the menu) are placed by rules in one adopted style sheet, not by style
  // attributes, which the page's policy forbids: one rule per element id.
  var sheet = null, rules = {};
  if (win.CSSStyleSheet && doc.adoptedStyleSheets) { sheet = new win.CSSStyleSheet(); doc.adoptedStyleSheets = doc.adoptedStyleSheets.concat([sheet]); }
  // `up`: put it above the place when there is room, so it covers the lines already read and not the one being read.
  function place(node, c, up) {
    if (!sheet) return;
    var w = Math.min(node.id === "alt-menu" ? 240 : 340, win.innerWidth - 16), left = Math.max(8, Math.min(c.left, win.innerWidth - w - 8)), vv = win.visualViewport;
    var room = (vv ? vv.offsetTop + vv.height : win.innerHeight) - 110, h = node.offsetHeight || 140;
    var top = (up || c.bottom + 6 + h > room) && c.top - 6 - h > 60 ? c.top - 6 - h : c.bottom + 6;
    rules[node.id] = "#" + node.id + "{left:" + Math.round(left) + "px;top:" + Math.round(top) + "px;width:" + w + "px}";
    sheet.replaceSync(Object.keys(rules).map(function (k) { return rules[k]; }).join("\n"));
  }
  function addPrompt() {
    if (!can) return note("You can read this chapter but not change it.");
    var sc = defaultScope(), rg = range(sc);
    if (typeof rg === "string") return note(rg);
    closePop();
    pop = el("div", "alt-pop"); pop.id = "alt-pop"; pop.setAttribute("role", "dialog"); pop.setAttribute("aria-label", "Add an alternative");
    var q = el("p", "alt-quote", "“" + (rg.text.length > 80 ? rg.text.slice(0, 80) + "…" : rg.text) + "”"), f = el("fieldset", "alt-scopes"), lg = el("legend", "sr-only", "What it covers");
    f.append(lg);
    ["word", "sentence", "paragraph"].forEach(function (k) {
      var l = el("label", "alt-scope"), r = el("input"); r.type = "radio"; r.name = "alt-scope"; r.value = k; r.checked = k === sc;
      l.append(r, el("span", "", k.charAt(0).toUpperCase() + k.slice(1))); f.append(l);
    });
    var lab = el("label", "", "Another wording"), ta = el("textarea"), err = el("p", "field-error"), row = el("div", "alt-row"), go = el("button", "btn solid small", "Add"), no = el("button", "btn quiet small", "Cancel");
    ta.rows = 2; ta.id = "alt-new"; lab.htmlFor = ta.id; ta.value = ""; err.setAttribute("role", "alert"); err.hidden = true;
    go.type = "button"; no.type = "button"; row.append(go, no);
    pop.append(q, f, lab, ta, err, row);
    doc.body.appendChild(pop);
    place(pop, view.coordsAtPos(view.state.selection.to));
    ta.focus();
    function submit() {
      var scope = pop.querySelector("input:checked").value, r2 = range(scope), text = ta.value.trim();
      if (typeof r2 === "string") { err.textContent = r2; err.hidden = false; return; }
      if (!text) { err.textContent = "Type the other wording first."; err.hidden = false; return; }
      go.disabled = true;
      create(scope, r2, text).then(function (r) {
        go.disabled = false;
        if (!r.ok) { err.textContent = r.message || "That could not be done."; err.hidden = false; return; }
        closePop(true); say("Alternative added. It has two wordings; the text still holds the original.");
        var made = r.res && r.res.alternative; if (made) api.show(made.id); // the caret goes to it, so the next key cycles it
        tell();
      });
    }
    go.addEventListener("click", submit); no.addEventListener("click", function () { closePop(true); });
    ta.addEventListener("keydown", function (e) { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(); } else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closePop(true); } });
    pop.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); closePop(true); } });
  }

  // ---- cycling ----------------------------------------------------------------------------------------------------------------
  var AN = /\b(an?)(\s+)$/i;
  function article(next) { // "a" or "an" for the word that now follows
    var w = (next.match(/[A-Za-zÀ-ɏ]+/) || [""])[0].toLowerCase();
    if (!w) return null;
    if (/^(hour|honest|honou?r|heir)/.test(w)) return "an";
    if (/^(uni|use|usu|uti|eu|one|once|ubi)/.test(w)) return "a";
    return /^[aeiou]/.test(w) ? "an" : "a";
  }
  function beep() {
    var on = false; try { on = win.localStorage.getItem("scriobh:altsound") === "on"; } catch (e) { /* off */ }
    if (!on || !(win.AudioContext || win.webkitAudioContext)) return;
    try {
      var c = beep.c || (beep.c = new (win.AudioContext || win.webkitAudioContext)());
      if (c.state !== "running") return; // a muted or suspended device stays silent
      var o = c.createOscillator(), g = c.createGain();
      o.frequency.value = 660; g.gain.value = 0.03; o.connect(g); g.connect(c.destination); o.start(); o.stop(c.currentTime + 0.05);
    } catch (e) { /* no sound */ }
  }
  // Put the answer to a choice into the text, as one quiet step that keeps the selection, and fix "a"/"an" before it. The
  // server says which source text it replaced (`replaced`); the editor puts the new wording over the words it holds at the
  // mark only if they are those words. When they are not (the editor and the server have drifted) nothing is replaced and
  // null sends the caller to take the stored text whole.
  function applyChoice(aid, res) {
    var m = markOf(aid), r = res.replaced; // the range as it is now: an earlier press in the queue may have changed it
    if (!m) return r ? null : merged(res);
    if (!r) return merged(res);
    var size = A.measure(r.text);
    if (size < 0) return null;
    var was = W.serverMd.slice(r.start, r.end);
    // Where the server's range falls in the editor's document must be where the mark is, not only hold the same words.
    var at = A.rangesOf(bodyOf(W.serverMd), [{ start: r.start, end: r.end }])[0];
    if (!at || at.from !== m.from || at.to !== m.to) return null;
    expect = { id: m.id, from: m.from, to: m.from + size }; // the draw that follows the edit takes the new range from here
    if (A.replace(m.from, m.to, r.text, was) < 0) { expect = null; return null; }
    var md = merged(res);
    var upd = res.alternative;
    if (upd) setItems(items.map(function (x) { return x.id === upd.id ? upd : x; }));
    return md;
  }
  // The "a" or "an" before a word that has just changed, fixed while the editor is still held (an edit made after the hold ends
  // would be refused if another op were queued behind this one).
  function articleFor(aid) { var mm = markOf(aid), u = byId(aid); if (mm && u && u.scope === "word") fixArticle(mm); }
  function fixArticle(m) {
    var b = blockOf(m.from);
    if (!b) return;
    var before = b.node.textBetween(0, m.from - b.start, "", "￼"), hit = AN.exec(before);
    if (!hit) return;
    var want = article(view.state.doc.textBetween(m.from, m.to, " "));
    if (!want || want === hit[1].toLowerCase()) return;
    var cap = hit[1][0] === "A", rep = (cap ? want[0].toUpperCase() : want[0]) + want.slice(1);
    var from = m.from - hit[0].length, to = from + hit[1].length;
    view.dispatch(view.state.tr.insertText(rep, from, to)); // an ordinary edit: it is saved like typing
  }
  function cycle(step, variantId) {
    if (!can) return note("You can read this chapter but not change it.");
    var m = current();
    if (!m) return note("The cursor is not in an alternative. Select a word, a sentence or a paragraph and add one.");
    var a = byId(m.id), body = { alternative_id: m.id };
    if (variantId) body.variant_id = variantId; else body.step = step;
    return op("POST", "/alternatives/choose", body, function (res) { return applyChoice(m.id, res); }, function () { articleFor(m.id); }).then(function (r) {
      if (!r.ok) return r;
      var u = byId(m.id) || a, c = u && chosen(u);
      if (u && c) { say("Wording " + u.chosen_position + " of " + u.variants.length + (c.original ? ", the original" : "") + ": " + c.text.replace(/[*_`\[\]]/g, "")); beep(); showCycler(m.id); }
      redraw(); tell();
      return r;
    });
  }
  var cyc = null;
  function showCycler(aid) {
    var a = byId(aid), m = marks.filter(function (x) { return x.id === aid; })[0];
    if (!a || !m) return;
    if (!cyc) { cyc = el("div", "alt-cycler"); cyc.id = "alt-cyc"; cyc.setAttribute("role", "group"); cyc.setAttribute("aria-label", "Wordings of this alternative"); doc.body.appendChild(cyc); }
    var c = chosen(a);
    cyc.replaceChildren();
    [["Previous wording", "◀", function () { cycle("previous"); }], null, ["Next wording", "▶", function () { cycle("next"); }], ["Show the original", "Original", function () { cycle("original"); }]].forEach(function (x) {
      if (!x) { cyc.append(el("span", "alt-pos", a.chosen_position + " of " + a.variants.length + (c && c.original ? " (original)" : ""))); return; }
      var b = el("button", "btn quiet small", x[1]); b.type = "button"; b.setAttribute("aria-label", x[0]); b.addEventListener("pointerdown", function (e) { e.preventDefault(); }); b.addEventListener("click", x[2]); cyc.append(b);
    });
    cyc.hidden = false;
    place(cyc, view.coordsAtPos(m.from), true);
  }
  function hideCycler() { if (cyc) cyc.hidden = true; }

  // ---- changing wordings, and the whole alternative -------------------------------------------------------------------------
  function after(res, aid) { var u = res.alternative; if (u) setItems(items.map(function (x) { return x.id === u.id ? u : x; })); tell(); redraw(); return res; }
  function viaMark(aid, res) { return applyChoice(aid, res); } // an op whose answer rewrote the text of the alternative (editing or removing the chosen wording)
  var api = {
    get items() { return items; }, get ghosts() { return ghosts; }, get ghostInfo() { return ghostInfo; }, get can() { return can; },
    on: function (f) { listeners.push(f); }, refresh: function () { return Promise.all([refresh(), refreshGhosts()]); },
    hint: hint, placed: placed, ensurePlaced: ensurePlaced, notPlaced: NOT_PLACED, cycle: cycle, add: addPrompt, run: run, exclusive: exclusive, get busy() { return busy; }, offsetOf: function (pos) { return A.offsetOf(pos); }, say: say, note: note,
    marksOf: function () { return marks; },
    show: function (aid) { var m = marks.filter(function (x) { return x.id === aid; })[0]; if (m) { A.select(m.from, m.to); view.focus(); } },
    choose: function (aid, vid) {
      if (!markOf(aid)) return Promise.resolve({ ok: false });
      return op("POST", "/alternatives/choose", { alternative_id: aid, variant_id: vid }, function (res) { return applyChoice(aid, res); }, function () { articleFor(aid); }).then(function (r) { if (r.ok) after(r.res, aid); return r; });
    },
    addVariant: function (aid, text, afterId) {
      return op("POST", "/alternatives/variants", { alternative_id: aid, text: text, after_variant_id: afterId || undefined }, function (res) { return merged(res) || null; }).then(function (r) { return r.ok ? (after(r.res, aid), r) : r; });
    },
    editVariant: function (aid, vid, text) {
      return op("PATCH", "/alternatives/variants", { alternative_id: aid, variant_id: vid, text: text }, function (res) { return viaMark(aid, res); }).then(function (r) { return r.ok ? (after(r.res, aid), r) : r; });
    },
    removeVariant: function (aid, vid) {
      return op("POST", "/alternatives/variants/remove", { alternative_id: aid, variant_id: vid }, function (res) { return viaMark(aid, res); }).then(function (r) { return r.ok ? (after(r.res, aid), r) : r; });
    },
    reorder: function (aid, vid, afterId) {
      return op("POST", "/alternatives/variants/reorder", { alternative_id: aid, variant_id: vid, after_variant_id: afterId || "" }, function (res) { return merged(res) || null; }).then(function (r) { return r.ok ? (after(r.res, aid), r) : r; });
    },
    del: function (aid) { return op("POST", "/alternatives/delete", { alternative_id: aid, with_content: true }, function () { return null; }); },
    reanchor: function (aid) {
      var r = range(items.filter(function (x) { return x.id === aid; })[0].scope);
      if (typeof r === "string") { note(r); return Promise.resolve({ ok: false }); }
      var src = A.sourceOf(r.from, r.to);
      if (!src) { note("That text could not be named exactly. Select a little more or less of it."); return Promise.resolve({ ok: false }); }
      return op("POST", "/alternatives/reanchor", { alternative_id: aid, quote: src.quote, before: src.before, after: src.after, with_content: true }, function () { return null; });
    },
    // The selection becomes an alternative whose other wording is `wording` (the writing partner's "keep as an alternative": the text
    // is not changed). The scope is the one the selection suggests.
    makeFromSelection: function (wording) {
      if (!can) { note("You can read this chapter but not change it."); return Promise.resolve({ ok: false }); }
      var sc = defaultScope(), rg = range(sc);
      if (typeof rg === "string") { note(rg); return Promise.resolve({ ok: false, message: rg }); }
      return create(sc, rg, wording).then(function (r) { if (r.ok) tell(); return r; });
    },
    ghostSelection: function () {
      if (!can) return note("You can read this chapter but not change it.");
      var s = view.state.selection;
      if (s.empty) return note("Select the text to ghost first.");
      var rs = [], d = view.state.doc, bad = false;
      d.nodesBetween(s.from, s.to, function (n, pos) {
        if (!n.isTextblock) return true;
        if (n.type.spec.code) return false;
        var a = Math.max(s.from, pos + 1), b = Math.min(s.to, pos + 1 + n.content.size);
        if (b > a) { var t = d.textBetween(a, b, "", "￼"); if (t.trim()) { var src = A.sourceOf(a, b); if (src) rs.push({ quote: src.quote, before: src.before, after: src.after }); else bad = true; } }
        return false;
      });
      if (bad) return note("Part of that selection could not be named exactly, so nothing was ghosted. Select a little more or less of it.");
      if (!rs.length) return note("There is no text in that selection to ghost.");
      return op("POST", "/ghosts", { ranges: rs.slice(0, 50), with_content: true }, function () { return null; }).then(function (r) { if (r.ok) say("Ghosted. The words stay in the chapter and are not counted."); return r; });
    },
    revive: function (spec) { return op("POST", "/ghosts/revive", typeof spec === "function" ? function () { return Promise.resolve(spec()).then(function (x) { return x && Object.assign({ with_content: true }, x); }); } : Object.assign({ with_content: true }, spec), function () { return null; }).then(function (r) { if (r.ok) say("Revived."); return r; }); },
    // The ghosts the selection touches, named by where they are in the text (the server finds them by range), not by their
    // place in a list that may be older than the text. The range is worked out once the editor is held and saved, from the
    // editor's text, and checked against it: it must start at an opening tag and end after a closing one.
    reviveSelection: function () {
      var s = view.state.selection, lo = Infinity, hi = -Infinity;
      ghostPairs(view.state.doc).forEach(function (g) { if ((g.c > s.from && g.a < s.to) || (s.empty && s.from >= g.a && s.from <= g.d)) { lo = Math.min(lo, g.a); hi = Math.max(hi, g.d); } });
      if (lo === Infinity) return note("The cursor is not in ghosted text.");
      return api.revive(function () { return ensurePlaced().then(function (ok) { if (!ok) { note(NOT_PLACED); return null; } return rangeOf(lo, hi); }); });
    },
    cmd: function (name) {
      if (name === "alt") { addPrompt(); return true; }
      if (name === "ghost") { var s = view.state.selection; if (touchesGhost(s)) api.reviveSelection(); else api.ghostSelection(); return true; }
      if (name === "next") { cycle("next"); return true; }
      if (name === "previous") { cycle("previous"); return true; }
      if (name === "original") { cycle("original"); return true; }
      if (name === "revive") { api.reviveSelection(); return true; }
      if (name === "stash") { if (S.stash) S.stash.sendSelection(); else W.lazy(W.cfg && doc.getElementById("write").dataset.stashSrc, "stash", function () { S.stash.sendSelection(); }); return true; }
      if (name === "sound") { try { var on = win.localStorage.getItem("scriobh:altsound") === "on"; win.localStorage.setItem("scriobh:altsound", on ? "off" : "on"); say(on ? "The cycling sound is off." : "The cycling sound is on, if this device is not muted."); } catch (e) { /* kept for this page */ } return true; }
      return false;
    }
  };
  // The one key ghosts plain text and revives ghosted text: a caret in a ghost, or a selection that takes in any of one, revives.
  function touchesGhost(s) {
    var ps = ghostPairs(view.state.doc);
    for (var i = 0; i < ps.length; i++) if (s.empty ? s.from >= ps[i].a && s.from <= ps[i].d : s.from < ps[i].d && s.to > ps[i].a) return true;
    return false;
  }
  // The source offsets of the ghost pair(s) from position `lo` to `hi`, once checked: null (having said why) when the saved text and
  // the editor do not agree on where they are, or the range is not a ghost's tags.
  function rangeOf(lo, hi) {
    var plain = A.plain(), start = A.offsetOf(lo), end = A.offsetOf(hi);
    if (!placed() || start < 0 || end < 0 || plain.slice(start, start + OPEN.length) !== OPEN || plain.slice(end - CLOSE.length, end) !== CLOSE) { note(NOT_PLACED); return null; }
    return { start: start, end: end };
  }
  function ghostAt(pos) { var ps = ghostPairs(view.state.doc); for (var i = 0; i < ps.length; i++) if (pos >= ps[i].a && pos <= ps[i].d) return i; return -1; }
  S.alts = api;

  // ---- keys, the menu, clicks ---------------------------------------------------------------------------------------------------
  // The editor learns of a selection the keyboard made when the browser reports it, a moment after the key, and it does not
  // look again at a key it has no use for. A chord asked for in that moment (just after a key that moves the selection) must act on
  // the selection the person sees, so the browser's selection is read first. A selection made by the page itself is the editor's own.
  function syncSelection() {
    try {
      var d = win.getSelection(), cur = view.state.selection;
      if (Date.now() - lastNav > 600 || !d || !d.rangeCount || !view.hasFocus() || !view.dom.contains(d.anchorNode) || !view.dom.contains(d.focusNode) || cur.node) return;
      var a = view.posAtDOM(d.anchorNode, d.anchorOffset), h = view.posAtDOM(d.focusNode, d.focusOffset);
      if (a !== cur.anchor || h !== cur.head) view.dispatch(view.state.tr.setSelection(cur.constructor.create(view.state.doc, a, h)));
    } catch (e) { /* the state is as it was */ }
  }
  var KEYS = { KeyA: "alt", KeyN: "next", KeyP: "previous", KeyO: "original", KeyG: "ghost", KeyS: "stash" };
  var chords = (S.chords = S.chords || { clash: [] }); // one table of every Alt+Shift chord: a chord claimed twice is listed in `clash`
  Object.keys(KEYS).forEach(function (c) { var who = "alternatives: " + KEYS[c]; if (chords[c] && chords[c] !== who) chords.clash.push(c + ": " + chords[c] + " and " + who); chords[c] = who; });
  doc.addEventListener("keydown", function (e) {
    if (!(e.altKey && e.shiftKey) || e.ctrlKey || e.metaKey || !KEYS[e.code]) return;
    if (e.isComposing || e.keyCode === 229 || view.composing) return; // an input method is using the key
    if (!view.dom.contains(e.target) && !view.dom.contains(doc.activeElement)) return;
    e.preventDefault(); e.stopPropagation();
    syncSelection();
    api.cmd(KEYS[e.code]);
  }, true);
  view.dom.addEventListener("keydown", function (e) { if (e.key.length === 1 || e.key === "Backspace" || e.key === "Enter") hideCycler(); });
  doc.addEventListener("keydown", function (e) { if (e.key === "Escape") { if (cyc && !cyc.hidden) hideCycler(); } }, true);
  view.dom.addEventListener("click", function (e) {
    var a = e.target.closest && e.target.closest(".scriobh-alt"), g = e.target.closest && e.target.closest(".scriobh-ghost");
    if (a && !g) showCycler(a.dataset.alt); else if (g && can) reviveCard(g); else hideCycler();
  });
  function reviveCard(g) {
    closePop();
    pop = el("div", "alt-pop alt-mini"); pop.id = "alt-pop"; pop.setAttribute("role", "group"); pop.setAttribute("aria-label", "Ghosted text");
    var b = el("button", "btn small", "Revive this text"); b.type = "button";
    pop.append(el("p", "fine", "Ghosted: still in the chapter, not counted."), b);
    doc.body.appendChild(pop);
    place(pop, g.getBoundingClientRect());
    b.addEventListener("click", function () {
      var at = view.posAtDOM(g, 0), k = ghostAt(at), ps = ghostPairs(view.state.doc); closePop();
      if (k >= 0) { var pr = ps[k]; api.revive(function () { return ensurePlaced().then(function (ok) { if (!ok) { note(NOT_PLACED); return null; } return rangeOf(pr.a, pr.d); }); }); } else api.reviveSelection();
    });
    b.focus();
  }
  doc.addEventListener("pointerdown", function (e) { if (pop && !pop.contains(e.target)) closePop(); if (cyc && !cyc.hidden && !cyc.contains(e.target) && !(e.target.closest && e.target.closest(".scriobh-alt"))) hideCycler(); }, true);
  // The context menu: only where it has something to offer (a selection, an alternative, ghosted text); elsewhere the browser's own.
  var menu = null;
  function closeMenu() { if (menu) { menu.remove(); menu = null; } }
  // A right click inside a selection keeps it, as a native menu does. Some browsers collapse the page's selection between the
  // press and the menu, so the selection at the press is remembered and put back when the click was inside it.
  var pressed = null;
  view.dom.addEventListener("mousedown", function (e) {
    pressed = null;
    if (e.button !== 2) return;
    var s = view.state.selection;
    if (!s.empty && !s.node) pressed = { anchor: s.anchor, head: s.head, doc: view.state.doc };
  }, true);
  view.dom.addEventListener("contextmenu", function (e) {
    var p = pressed; pressed = null;
    if (p && view.state.doc === p.doc && view.state.selection.empty) {
      var at = view.posAtCoords({ left: e.clientX, top: e.clientY });
      if (at && at.pos >= Math.min(p.anchor, p.head) && at.pos <= Math.max(p.anchor, p.head)) {
        try { var c = view.state.selection.constructor; view.dispatch(view.state.tr.setSelection(c.create(p.doc, p.anchor, p.head))); } catch (x) { /* the menu is for the caret */ }
      }
    }
    var sel = !view.state.selection.empty, onAlt = e.target.closest && e.target.closest(".scriobh-alt"), onGhost = e.target.closest && e.target.closest(".scriobh-ghost");
    if (!can || !(sel || onAlt || onGhost)) return;
    e.preventDefault(); closeMenu();
    menu = el("ul", "alt-menu"); menu.id = "alt-menu"; menu.setAttribute("role", "menu"); menu.setAttribute("aria-label", "Alternatives");
    var list = [];
    if (sel) list.push(["Add an alternative…", "alt"], ["Ghost this text", "ghost"], ["Send to the stash", "stash"]);
    if (onAlt) list.push(["Next wording", "next"], ["Previous wording", "previous"], ["Show the original", "original"]);
    if (onGhost) list.push(["Revive this text", "revive"]);
    list.forEach(function (x) {
      var li = el("li"), b = el("button", "alt-mi", x[0]); b.type = "button"; b.setAttribute("role", "menuitem"); b.tabIndex = -1;
      b.addEventListener("click", function () { closeMenu(); view.focus(); api.cmd(x[1]); }); li.append(b); menu.append(li);
    });
    doc.body.appendChild(menu);
    place(menu, { left: e.clientX, top: e.clientY - 6, bottom: e.clientY - 6 });
    menu.querySelector("button").focus();
    menu.addEventListener("keydown", function (k) {
      var bs = [].slice.call(menu.querySelectorAll("button")), i = bs.indexOf(doc.activeElement);
      if (k.key === "ArrowDown") { k.preventDefault(); bs[(i + 1) % bs.length].focus(); }
      else if (k.key === "ArrowUp") { k.preventDefault(); bs[(i - 1 + bs.length) % bs.length].focus(); }
      else if (k.key === "Escape") { k.preventDefault(); closeMenu(); view.focus(); }
    });
  });
  doc.addEventListener("pointerdown", function (e) { if (menu && !menu.contains(e.target)) closeMenu(); }, true);

  // A non-visual cue: when the caret comes into an alternative or into ghosted text (by an arrow key or a click, not while typing),
  // it is said, with how to cycle or revive it.
  var inAlt = "", inGhost = false, said = 0;
  doc.addEventListener("selectionchange", function () {
    win.clearTimeout(said);
    said = win.setTimeout(function () {
      if (busy || !view.hasFocus()) return;
      var typing = Date.now() - lastKey < 400 && lastKey !== lastNav;
      var m = current(), g = view.state.selection.empty && ghostAt(view.state.selection.from) >= 0;
      if (!typing) {
        if (m && m.id !== inAlt) { var a = byId(m.id); if (a) say("Alternative, wording " + a.chosen_position + " of " + a.variants.length + ". " + hint("KeyN") + " for the next, " + hint("KeyO") + " for the original."); }
        else if (g && !inGhost) say("Ghosted text, not counted. " + hint("KeyG") + " brings it back.");
      }
      inAlt = m ? m.id : ""; inGhost = !!g;
    }, 250);
  });

  // ---- starting ----------------------------------------------------------------------------------------------------------------
  // After a save the server may have taken an edit at the edge of a mark into its wording, or lost a mark to an edit inside it:
  // the places are asked for again (once nothing is being typed).
  doc.addEventListener("scriobh:saved", function () { if (touched || marks.length < items.filter(function (a) { return a.status === "active"; }).length) later(touched ? 300 : 2500); });
  scanGhosts(); redraw();
  win.setTimeout(function () { api.refresh(); }, 300);
})();
