/* Scríobh review: comments in the reading view (a margin beside the page, a sheet on a phone) and the Comments tab of
   the writing view's inspector. A selection on the rendered page becomes the anchor of a thread through the same op
   everything else uses (comment.create with plain: true: the words are found in the text a reader can see, so nothing
   hidden can be found or confirmed). Marks on the page are drawn from comment.list's plain hints and are only marks:
   the text is never changed here. Everything a person wrote is put on the page as text, never as markup.
   Strings come from the page (data-msg); the English here is the fallback. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh = win.scriobh || {};
  var reader = doc.getElementById("reader"), W = S.write, panelMode = !reader;
  if (panelMode && !W) return;
  var project = panelMode ? W.cfg.project : reader.dataset.project, me = (doc.querySelector('meta[name="scriobh-user"]') || {}).content || "";
  var decide = panelMode ? W.cfg.canEdit === true : reader.dataset.decide === "1";
  var slug = panelMode ? W.cfg.slug : reader.dataset.slug;
  var base = "/api/v1/projects/" + project;
  var M = {}, root, list, live, composer, pop, popCap = null, threads = [], loaded = {}, filter = "open", people = null, current = "", caret = false, tick = 0;
  var drafts = {}, dirty = false, down = false, sheetFrom = null, scrim = null, LANG = (win.navigator.language || "").slice(0, 2) === (doc.documentElement.lang || "").slice(0, 2) ? win.navigator.language : doc.documentElement.lang || undefined; // the browser's own form of the interface's language
  var BLOCK = ".r-prose p, .r-prose li, .r-prose h3, .r-prose h4, .r-prose h5, .r-prose h6";
  var SKIP = BLOCK + ", sup.fnref, .omitted, ins.rv-ins";

  function T(k, d, v) { var s = M[k] || d; if (v) for (var n in v) s = s.split("{" + n + "}").join(v[n]); return s; }
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function $(s, r) { return (r || doc).querySelector(s); }
  function $$(s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); }
  function say(m) { if (!live) return; live.textContent = ""; win.setTimeout(function () { live.textContent = m; }, 40); }
  function api(method, path, body) {
    var o = { method: method, credentials: "same-origin", headers: { Accept: "application/json" } };
    if (body !== undefined) { o.headers["Content-Type"] = "application/json"; o.body = JSON.stringify(body); }
    return fetch(path, o).then(function (r) {
      return r.text().then(function (t) {
        var j = null;
        try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ }
        return { status: r.status, ok: r.ok, json: j };
      });
    }, function () { return { status: 0, ok: false, json: null }; });
  }
  function why(r) { return (r.json && r.json.error && r.json.error.message) || (r.status === 0 ? T("offline", "Could not reach the server.") : T("failed", "That did not work. Try again.")); }
  function plainish(s) { return String(s || "").replace(/[*_~`]/g, ""); }
  // the person's language where the browser knows its tag (a test locale is not one: the browser's own then)
  function fmt(t, o) { var d = new Date(t); try { return d.toLocaleString(LANG, o); } catch (e) { try { return d.toLocaleString(undefined, o); } catch (x) { return t; } } }
  function when(t) { return fmt(t, { dateStyle: "medium", timeStyle: "short" }); }
  function stamp(t) { var e = el("time", "rv-when", when(t)); e.dateTime = t; e.title = fmt(t, { dateStyle: "full", timeStyle: "long" }); return e; }
  // a confirmation a double tap cannot get through (arm.js; fetched on demand in the writing view's tab)
  function armed(host, o) {
    if (S.arm) { S.arm.ask(host, o); return; }
    var src = root && root.dataset.arm, sc;
    if (!src) { say(T("failed", "That did not work. Try again.")); return; }
    sc = el("script"); sc.src = src; sc.onload = function () { S.arm.ask(host, o); }; sc.onerror = function () { say(T("failed", "That did not work. Try again.")); };
    doc.head.appendChild(sc);
  }

  // ---- the text of a block as a reader sees it ---------------------------------------------------------------------
  // The same whitespace rule the server uses (runs collapse to one space, none at either end), with a map from each
  // character back to its place in the page, so a quote can be found and marked.
  function index(block) {
    var w = doc.createTreeWalker(block, 1 | 4, { acceptNode: function (n) {
      if (n.nodeType === 1) return n !== block && n.matches(SKIP) ? 2 : 3;
      return 1;
    } });
    var text = "", map = [], gap = null, n;
    while ((n = w.nextNode())) {
      var s = n.nodeValue;
      for (var k = 0; k < s.length; k++) {
        if (/\s/.test(s.charAt(k))) { if (text && !gap) gap = [n, k]; continue; }
        if (gap) { text += " "; map.push(gap); gap = null; }
        text += s.charAt(k); map.push([n, k]);
      }
    }
    return { text: text, map: map };
  }
  function blocks(sec) { return $$(BLOCK, sec); }
  function blockOf(n) { var e = n.nodeType === 1 ? n : n.parentElement; return e && e.closest ? e.closest(BLOCK) : null; }
  function ends(ctx, quote, i) { // the words before and after, as the server compares them
    var pre = ctx.text.slice(0, i).replace(/ +$/, ""), post = ctx.text.slice(i + quote.length).replace(/^ +/, "");
    return [pre, post];
  }
  // Where a quote is in a document's stretch of the page: the one occurrence its context fits, else the only one.
  function locate(sec, quote, before, after) {
    var cands = [], exact = [];
    blocks(sec).forEach(function (b) {
      var ix = index(b), from = 0, i;
      while (quote && (i = ix.text.indexOf(quote, from)) >= 0) {
        var e = ends(ix, quote, i), c = { ix: ix, a: i, b: i + quote.length };
        cands.push(c);
        if ((!before || e[0].slice(-before.length) === before || before.slice(-e[0].length) === e[0]) && (!after || e[1].indexOf(after) === 0 || after.indexOf(e[1]) === 0)) exact.push(c);
        from = i + 1;
      }
    });
    return exact.length === 1 ? exact[0] : !exact.length && cands.length === 1 ? cands[0] : exact[0] || null;
  }
  // Wrap characters [a, b) of an indexed block in marks (one per text node), the later nodes first so earlier offsets hold.
  function wrap(ix, a, b, make) {
    var segs = [], last = null;
    for (var i = a; i < b; i++) {
      var p = ix.map[i];
      if (last && last.n === p[0]) last.e = p[1] + 1; else { last = { n: p[0], s: p[1], e: p[1] + 1 }; segs.push(last); }
    }
    var out = [];
    segs.reverse().forEach(function (g) {
      var r = doc.createRange(), m = make();
      r.setStart(g.n, g.s); r.setEnd(g.n, g.e);
      r.surroundContents(m);
      out.push(m);
    });
    return out;
  }
  function unmark(sec) {
    $$("mark.rv-mark", sec).forEach(function (m) { var p = m.parentNode; while (m.firstChild) p.insertBefore(m.firstChild, m); p.removeChild(m); p.normalize(); });
  }
  function sections() { return $$(".r-sec[data-doc]", reader); }
  function secOf(id) { return reader && $('.r-sec[data-doc="' + id + '"]', reader); }

  function mark(t) {
    var a = t.anchor, sec = secOf(t.document_id);
    t.placed = false;
    if (!sec || !a || a.state !== "active" || !a.quote_plain) return;
    var c = locate(sec, a.quote_plain, a.before_plain, a.after_plain);
    if (!c) return;
    var ms = wrap(c.ix, c.a, c.b, function () { var m = el("mark", "rv-mark"); m.dataset.t = t.thread_id; m.tabIndex = -1; m.setAttribute("aria-details", "rv-t-" + t.thread_id); return m; });
    t.placed = ms.length > 0;
    paint(t);
  }
  function paint(t) {
    $$('mark.rv-mark[data-t="' + t.thread_id + '"]').forEach(function (m) { m.classList.toggle("is-resolved", t.status === "resolved"); m.classList.toggle("is-current", t.thread_id === current); });
  }
  function markAll(docId) {
    var sec = secOf(docId);
    if (!sec) return;
    unmark(sec);
    threads.filter(function (t) { return t.document_id === docId; }).forEach(mark);
  }

  // ---- loading ----------------------------------------------------------------------------------------------------
  function loadDoc(id) {
    return api("GET", base + "/comments?document_id=" + id + "&plain=true&status=all&limit=200").then(function (r) {
      if (!r.ok || !r.json) { say(why(r)); return; }
      threads = threads.filter(function (t) { return t.document_id !== id; }).concat(r.json.items.filter(function (t) { return !t.deleted; }));
      loaded[id] = true;
      if (!panelMode) markAll(id);
    });
  }
  var counts = {};
  function loadCounts() {
    var n = 0, got = {};
    (function page(off) {
      api("GET", base + "/comments?status=open&limit=200&offset=" + off).then(function (r) {
        if (!r.ok || !r.json) return;
        r.json.items.forEach(function (t) { got[t.document_id] = (got[t.document_id] || 0) + 1; n++; });
        if (r.json.items.length === 200 && off < 2000) return page(off + 200);
        counts = got;
        var c = $("#rv-count"); if (c) c.textContent = n ? String(n) : "";
        $$("#read-toc a[data-docs]").forEach(function (a) {
          var k = 0; a.dataset.docs.split(" ").forEach(function (d) { k += got[d] || 0; });
          var b = $(".rv-n", a); if (!b) { b = el("span", "rv-n"); a.appendChild(b); }
          b.textContent = k ? T("n_open", "{n} open", { n: k }) : "";
        });
      });
    })(0);
  }
  function reload() {
    var ids = panelMode ? [W.cfg.id] : sections().map(function (s) { return s.dataset.doc; });
    return Promise.all(ids.map(loadDoc)).then(function () { draw(); loadCounts(); });
  }

  // ---- the margin -----------------------------------------------------------------------------------------------------
  function mine(t) { return t.author && t.author.user_id === me; }
  function mentioned(t) { return (t.mentions || []).some(function (m) { return m.user_id === me; }) || (t.replies || []).some(function (r) { return (r.mentions || []).some(function (m) { return m.user_id === me; }); }); }
  function shown(t) {
    if (t.deleted) return false;
    if (filter === "resolved") return t.status === "resolved";
    if (t.status !== "open") return false;
    return filter === "mine" ? mine(t) : filter === "mentions" ? mentioned(t) : true;
  }
  function order() {
    var rank = {};
    sections().forEach(function (s, i) { rank[s.dataset.doc] = i; });
    return threads.filter(shown).sort(function (a, b) {
      var ra = rank[a.document_id] || 0, rb = rank[b.document_id] || 0;
      if (ra !== rb) return ra - rb;
      var oa = a.anchor && a.anchor.state === "active" ? 0 : 1, ob = b.anchor && b.anchor.state === "active" ? 0 : 1;
      return oa - ob || (a.anchor ? a.anchor.start : 0) - (b.anchor ? b.anchor.start : 0) || (a.created_at < b.created_at ? -1 : 1);
    });
  }
  function btn(label, fn, cls, aria) {
    var b = el("button", "btn " + (cls || "quiet small"), label); b.type = "button";
    if (aria) b.setAttribute("aria-label", aria);
    b.addEventListener("click", fn);
    return b;
  }
  function note(t) {
    var a = t.anchor;
    if (!a) return el("p", "rv-where", t.document_id ? T("on_chapter", "On the chapter as a whole") : T("on_book", "On the book as a whole"));
    if (a.state !== "active") {
      var d = el("div", "rv-where is-lost");
      d.appendChild(el("p", null, T("lost", "The words this was on are gone from the text.")));
      var q = el("blockquote", "rv-quote", "“" + plainish(a.original || a.quote) + "”"); d.appendChild(q);
      return d;
    }
    var d2 = el("div", "rv-where"), shownQ = a.quote_plain || plainish(a.quote);
    d2.appendChild(el("blockquote", "rv-quote", "“" + shownQ + "”"));
    if (a.original && plainish(a.original) !== shownQ) d2.appendChild(el("p", "rv-moved", T("moved", "The words were changed after this was written. It was on “{q}”.", { q: plainish(a.original) })));
    if (!panelMode && secOf(t.document_id) && t.placed === false) d2.appendChild(el("p", "rv-moved", T("unplaced", "These words are not on this page as drawn: the chapter may have changed. Reload to see it as it is now.")));
    return d2;
  }
  // A Word author is a name from an imported file, not a person of this server: it is never shown as a bare name.
  function who(a) { return a && a.name ? (a.kind === "label" ? T("word_name", "{name} (from a Word file)", { name: a.name }) : a.name) : T("someone", "Someone"); }
  function card(t) {
    var li = el("li", "rv-card" + (t.status === "resolved" ? " is-resolved" : "") + (t.anchor && t.anchor.state !== "active" ? " is-lost" : ""));
    li.id = "rv-t-" + t.thread_id; li.dataset.id = t.thread_id; li.tabIndex = -1;
    var meta = el("p", "rv-meta"), nm = el("strong", null, who(t.author));
    meta.id = "rv-m-" + t.thread_id; li.setAttribute("aria-labelledby", meta.id); // named by its author and time, its words stay its content
    meta.appendChild(nm); meta.appendChild(stamp(t.created_at));
    if (t.visibility === "private") meta.appendChild(el("span", "rv-kind", T("only_you", "Only you")));
    if (t.kind && t.kind !== "comment") meta.appendChild(el("span", "rv-kind", t.kind === "question" ? T("question", "Question") : T("suggestion_note", "Suggestion note")));
    if (t.status === "resolved") meta.appendChild(el("span", "rv-kind", T("resolved_by", "Resolved by {name}", { name: who(t.resolved_by) })));
    li.appendChild(meta);
    li.appendChild(note(t));
    li.appendChild(el("p", "rv-body", t.body));
    if (t.replies && t.replies.length) {
      var ul = el("ul", "rv-replies");
      t.replies.filter(function (r) { return !r.deleted; }).forEach(function (r) {
        var ri = el("li"), m = el("p", "rv-meta"); m.appendChild(el("strong", null, who(r.author))); m.appendChild(stamp(r.created_at));
        ri.appendChild(m); ri.appendChild(el("p", "rv-body", r.body)); ul.appendChild(ri);
      });
      li.appendChild(ul);
    }
    var act = el("div", "rv-actions");
    if (panelMode) { var a = el("a", "btn quiet small", T("read_it", "In the reading view")); a.href = "/p/" + slug + "/read/" + t.document_id + "#t=" + t.thread_id; act.appendChild(a); }
    else if (t.placed) act.appendChild(btn(T("show", "Show in the text"), function () { show(t.thread_id); }));
    act.appendChild(btn(T("reply", "Reply"), function () { replyBox(li, t); }));
    if (t.status === "open" && t.can_resolve) act.appendChild(btn(T("resolve", "Resolve"), function () { act1(t, "resolve", T("resolved", "Resolved."), li); }));
    if (t.status === "resolved" && t.can_resolve) act.appendChild(btn(T("reopen", "Reopen"), function () { act1(t, "reopen", T("reopened", "Reopened."), li); }));
    if (t.can_delete) act.appendChild(btn(T("delete", "Delete"), function (e) { remove(t, li, e.currentTarget); }, "quiet small danger-text"));
    if (panelMode && S.cwrite) S.cwrite.actions(t, act, li); // Edit, Show in the text, and what a lost place offers
    Array.prototype.forEach.call(act.children, function (c) { c.setAttribute("aria-describedby", meta.id); }); // "Resolve", then whose comment it is
    li.appendChild(act);
    if (drafts[t.thread_id] != null) replyBox(li, t, true); // an unsent reply is still there after a redraw
    return li;
  }
  function typingIn() { return !!(list && doc.activeElement && doc.activeElement.tagName === "TEXTAREA" && list.contains(doc.activeElement)); }
  function draw(force) {
    if (!list) return;
    if (list.querySelector(".arm")) { dirty = true; return; } // nor under a question being asked
    var keep = doc.activeElement && list.contains(doc.activeElement) ? (doc.activeElement.closest(".rv-card") || {}).id : "", ta = typingIn() && doc.activeElement, at = ta ? [ta.id, ta.selectionStart, ta.selectionEnd] : null;
    if (ta && !force) { dirty = true; return; } // never redraw under a reply being written; it is drawn when the person leaves it
    dirty = false;
    list.textContent = "";
    var ts = order();
    ts.forEach(function (t) { list.appendChild(card(t)); });
    var e = $("#rv-empty"); if (e) { e.hidden = ts.length > 0; e.textContent = filter === "resolved" ? T("none_resolved", "Nothing has been resolved yet.") : filter === "mine" ? T("none_mine", "You have no open comments.") : filter === "mentions" ? T("none_mentions", "Nobody has mentioned you.") : T("none_open", "No open comments."); }
    var first = list.firstElementChild; if (first) first.tabIndex = 0;
    threads.forEach(function (t) { if (!panelMode) paint(t); });
    if (panelMode) { tabN(); marks(); }
    if (at) { var back = doc.getElementById(at[0]); if (back) { back.focus({ preventScroll: true }); back.setSelectionRange(at[1], at[2]); } }
    else if (keep) { var k = doc.getElementById(keep); if (k) k.focus({ preventScroll: true }); }
  }
  // "Comments (3)": the open threads of this chapter, as text on the tab; nothing moves (the tab is a fixed place in the row)
  function tabN() {
    var n = threads.filter(function (t) { return t.status === "open" && !t.deleted; }).length, tab = $("#itab-comments"), c = $("#itab-comments-n");
    if (!tab) return;
    if (!n) { if (c) c.remove(); return; }
    if (!c) { c = el("span"); c.id = "itab-comments-n"; tab.appendChild(c); }
    c.textContent = " (" + n + ")";
  }
  // The words of open comments are marked in the text (js/cmarks.js), fetched the first time this chapter has one the person can read.
  function marks() {
    if (S.cmarks) return S.cmarks.refresh();
    var c = $("#ipanel-comments");
    if (c && c.dataset.cmarks && !c.dataset.cm && threads.some(function (t) { return t.status === "open" && !t.deleted; })) { c.dataset.cm = "1"; var sc = el("script"); sc.src = c.dataset.cmarks; doc.head.appendChild(sc); }
  }
  function replace(t) {
    var old = threads.filter(function (x) { return x.thread_id === t.thread_id; })[0];
    if (old) { t.anchor = old.anchor; t.placed = old.placed; threads[threads.indexOf(old)] = t; } else threads.push(t);
  }
  function act1(t, op, msg, li) {
    var nxt = li && li.nextElementSibling ? li.nextElementSibling.id : "";
    api("POST", base + "/comments/" + t.thread_id + "/" + op).then(function (r) {
      if (!r.ok || !r.json) { say(why(r)); return; }
      replace(r.json.item); current = t.thread_id; draw(); paint(r.json.item); loadCounts(); say(msg);
      var n = nxt && doc.getElementById(nxt); if (n) n.focus(); else if (list.firstElementChild) list.firstElementChild.focus();
    });
  }
  function remove(t, li, from) {
    armed(li, { msg: T("confirm_delete", "Delete this comment? A note that it was deleted stays if there are replies."), ok: T("delete", "Delete"), cancel: T("cancel", "Cancel"), from: from, onOk: function () {
      var nxt = li.nextElementSibling ? li.nextElementSibling.id : "";
      api("DELETE", base + "/comments/" + t.thread_id).then(function (r) {
        if (!r.ok) { say(why(r)); if (from && from.isConnected) from.focus(); return; }
        threads = threads.filter(function (x) { return x.thread_id !== t.thread_id; });
        delete drafts[t.thread_id];
        if (!panelMode) markAll(t.document_id);
        draw(true); loadCounts(); say(T("deleted", "Deleted."));
        var n = nxt && doc.getElementById(nxt) || list.firstElementChild; if (n) n.focus();
      });
    } });
  }
  function show(id) {
    var m = $('mark.rv-mark[data-t="' + id + '"]');
    if (!m) { say(T("not_on_page", "That comment's words are not on this page.")); return; }
    current = id; threads.forEach(paint);
    m.tabIndex = -1; reveal(m);
    if (!sheetOpen()) m.focus({ preventScroll: true });
  }
  function sheetOpen() { return !!root && mobile() && root.classList.contains("is-open"); }
  // the mark in the free part of the window: above the sheet on a phone, the middle of it otherwise
  function reveal(m) {
    m.scrollIntoView({ block: "center" });
    if (sheetOpen()) { var r = m.getBoundingClientRect(); if (r.bottom > win.innerHeight * 0.4 || r.top < 0) win.scrollBy(0, r.top - win.innerHeight * 0.1); }
  }
  function focusCard(id) {
    var c = doc.getElementById("rv-t-" + id);
    if (!c) return false;
    $$(".rv-card", list).forEach(function (x) { x.tabIndex = -1; });
    c.tabIndex = 0; c.scrollIntoView({ block: "nearest" }); c.focus(); current = id; threads.forEach(paint);
    return true;
  }

  // ---- writing: a new thread, a reply, with @ to mention someone --------------------------------------------------
  function loadPeople() {
    if (people) return Promise.resolve(people);
    return api("GET", base + "/comment-people").then(function (r) { people = r.ok && r.json ? r.json.items : []; return people; });
  }
  function area(label, id, off) { // off(): a comment only its author can see mentions nobody, so there is then no @ picker
    var wrap_ = el("div", "field"), l = el("label", null, label), ta = el("textarea"), picked = {};
    l.htmlFor = id; ta.id = id; ta.rows = 3; ta.maxLength = 10000; ta.setAttribute("aria-autocomplete", "list");
    wrap_.appendChild(l); wrap_.appendChild(ta);
    var menu = el("ul", "rv-pick"); menu.setAttribute("role", "listbox"); menu.hidden = true; wrap_.appendChild(menu);
    var sel = -1, hits = [];
    function close() { menu.hidden = true; ta.removeAttribute("aria-activedescendant"); sel = -1; }
    function choose(p) {
      var v = ta.value, at = ta.selectionStart, m = /@([^\s@]*)$/.exec(v.slice(0, at));
      if (!m) return close();
      var ins = "@" + p.name + " ";
      ta.value = v.slice(0, at - m[0].length) + ins + v.slice(at); picked[p.user_id] = true;
      ta.selectionStart = ta.selectionEnd = at - m[0].length + ins.length; close(); ta.focus();
    }
    ta.addEventListener("input", function () {
      var m = /@([^\s@]*)$/.exec(ta.value.slice(0, ta.selectionStart));
      if (!m || (off && off())) return close();
      loadPeople().then(function (ps) {
        var q = m[1].toLowerCase();
        hits = ps.filter(function (p) { return p.user_id !== me && p.name.toLowerCase().indexOf(q) >= 0; }).slice(0, 6);
        menu.textContent = ""; sel = -1;
        hits.forEach(function (p, i) { var o = el("li", null, p.name); o.id = id + "-p" + i; o.setAttribute("role", "option"); o.addEventListener("mousedown", function (e) { e.preventDefault(); choose(p); }); menu.appendChild(o); });
        menu.hidden = !hits.length;
      });
    });
    ta.addEventListener("keydown", function (e) {
      if (menu.hidden) return;
      if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); sel = (sel + (e.key === "ArrowDown" ? 1 : hits.length - 1)) % hits.length; $$("li", menu).forEach(function (o, i) { o.setAttribute("aria-selected", String(i === sel)); }); ta.setAttribute("aria-activedescendant", id + "-p" + sel); }
      else if ((e.key === "Enter" || e.key === "Tab") && sel >= 0) { e.preventDefault(); choose(hits[sel]); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); }
    });
    return { box: wrap_, ta: ta, ids: function () { return Object.keys(picked).filter(function (k) { return ta.value.indexOf("@") >= 0; }); } };
  }
  function form(label, id, submit, onSubmit, onCancel, off) {
    var f = el("form", "rv-form"), a = area(label, id, off), err = el("p", "field-error"), row = el("div", "form-actions");
    err.setAttribute("role", "alert"); err.hidden = true;
    var go = el("button", "btn solid small", submit); go.type = "submit";
    row.appendChild(btn(T("cancel", "Cancel"), onCancel)); row.appendChild(go);
    f.appendChild(a.box); f.appendChild(err); f.appendChild(row);
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      var body = a.ta.value.trim();
      if (!body) { err.textContent = T("empty", "Write something first."); err.hidden = false; a.ta.focus(); return; }
      go.disabled = true;
      onSubmit(body, a.ids()).then(function (msg) { go.disabled = false; if (msg) { err.textContent = msg; err.hidden = false; } });
    });
    f.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); f.requestSubmit(); }
      else if (e.key === "Escape" && !e.defaultPrevented) { e.preventDefault(); e.stopPropagation(); onCancel(); }
    });
    return { form: f, ta: a.ta };
  }
  function replyBox(li, t, restore) {
    var old = $(".rv-form", li), id = t.thread_id;
    if (old) { old.querySelector("textarea").focus(); return; }
    var fm = form(T("reply_label", "Your reply"), "rv-r-" + id, T("send_reply", "Send reply"), function (body, ids) {
      return api("POST", base + "/comments", { parent_id: id, body: body, mentions: ids.length ? ids : undefined }).then(function (r) {
        if (!r.ok || !r.json) return why(r);
        delete drafts[id]; replace(r.json.item); fm.form.remove(); draw(true); loadCounts(); say(T("replied", "Reply sent.")); focusCard(id);
      });
    }, function () { delete drafts[id]; fm.form.remove(); li.focus(); if (dirty) draw(true); }, function () { return t.visibility === "private"; });
    // what is typed is kept by thread, so a live update, a filter or a redraw never takes an unsent reply away
    if (drafts[id]) fm.ta.value = drafts[id]; else drafts[id] = "";
    fm.ta.addEventListener("input", function () { drafts[id] = fm.ta.value; });
    li.appendChild(fm.form);
    if (!restore) fm.ta.focus();
  }
  // The selection, as the page shows it: one block, its words and the few before and after.
  function capture() {
    var s = win.getSelection();
    if (!s || !s.rangeCount || s.isCollapsed || !reader) return null;
    var r = s.getRangeAt(0), a = blockOf(r.startContainer), b = blockOf(r.endContainer);
    if (!a || !b || !reader.contains(a)) return null;
    var sec = a.closest(".r-sec");
    if (!sec || a !== b) return { err: T("one_block", "Select inside one paragraph. A comment goes on a stretch of one paragraph, or on the chapter.") };
    var ix = index(a), i0 = -1, i1 = -1;
    for (var i = 0; i < ix.map.length; i++) {
      var p = ix.map[i];
      if (r.comparePoint(p[0], p[1]) === 0) { if (i0 < 0) i0 = i; i1 = i + 1; }
    }
    if (i0 < 0) return null;
    var q = ix.text.slice(i0, i1).trim();
    if (!q) return null;
    return { doc: sec.dataset.doc, rev: parseInt(sec.dataset.rev || "0", 10), quote: q, before: ix.text.slice(Math.max(0, i0 - 24), i0).replace(/^ +/, ""), after: ix.text.slice(i1, i1 + 24).replace(/ +$/, ""), sec: sec, range: r.cloneRange() };
  }
  // The words being commented on stay highlighted while the box is open, though the selection has moved to it (no markup is added).
  function hold(range) {
    try { if (win.CSS && win.CSS.highlights && win.Highlight) { if (range) win.CSS.highlights.set("rv-sel", new win.Highlight(range)); else win.CSS.highlights.delete("rv-sel"); } } catch (e) { /* the box still shows the words */ }
  }
  function composeOn(cap) {
    if (!cap) return;
    if (cap.err) { say(cap.err); return; }
    composer.textContent = ""; composer.hidden = false;
    hold(cap.range);
    composer.appendChild(el("p", "rv-quote", "“" + cap.quote + "”"));
    var kind = el("select"), kl = el("label", "sr-only", T("kind", "Kind"));
    kl.htmlFor = "rv-kind"; kind.id = "rv-kind";
    [["comment", T("kind_comment", "Comment")], ["question", T("kind_question", "Question")]].forEach(function (o) { var op = el("option", null, o[1]); op.value = o[0]; kind.appendChild(op); });
    var close = function () { composer.hidden = true; composer.textContent = ""; hold(null); };
    var fm = form(T("your_comment", "Your comment"), "rv-new", T("add_comment", "Add comment"), function (body, ids) {
      return api("POST", base + "/comments", { document_id: cap.doc, body: body, quote: cap.quote, before: cap.before, after: cap.after, plain: true, kind: kind.value, mentions: ids.length ? ids : undefined }).then(function (r) {
        if (!r.ok || !r.json) return r.status === 422 || r.status === 409 ? why(r) : why(r);
        var t = r.json.item; t.anchor = t.anchor || {}; threads.push(t); close();
        // the anchor's plain hints come with the next list; the words are what the person selected, so mark them now
        t.anchor.quote_plain = t.anchor.quote_plain || cap.quote; t.anchor.before_plain = cap.before; t.anchor.after_plain = cap.after;
        filter = "open"; syncFilters(); mark(t); draw(); loadCounts(); say(T("added", "Comment added.")); focusCard(t.thread_id);
        win.getSelection().removeAllRanges(); popHide();
      });
    }, function () { close(); popHide(); });
    fm.form.insertBefore(kl, fm.form.firstChild); fm.form.insertBefore(kind, fm.form.children[1]);
    composer.appendChild(fm.form);
    if (mobile()) openSheet(true);
    fm.ta.focus();
  }

  // ---- the button that appears with a selection --------------------------------------------------------------------
  // On a wide page the button comes up beside the words that are selected (a constructable style sheet places it: the page's
  // policy allows no style attribute); on a phone it stays at the bottom, clear of the system's own selection handles.
  var popSheet = null;
  function placePop() {
    if (!pop || pop.hidden) return;
    if (!popSheet && win.CSSStyleSheet && doc.adoptedStyleSheets) { popSheet = new win.CSSStyleSheet(); doc.adoptedStyleSheets = doc.adoptedStyleSheets.concat([popSheet]); }
    if (!popSheet) return;
    var s = win.getSelection();
    if (mobile() || !s || !s.rangeCount) { popSheet.replaceSync(""); return; }
    var rs = s.getRangeAt(0).getClientRects(), r = rs.length ? rs[rs.length - 1] : s.getRangeAt(0).getBoundingClientRect();
    var left = Math.max(8, Math.min(r.left, win.innerWidth - 300)), top = r.bottom + 56 > win.innerHeight ? Math.max(8, r.top - 52) : r.bottom + 8;
    popSheet.replaceSync(".rv-pop{left:" + Math.round(left) + "px;right:auto;top:" + Math.round(top) + "px;bottom:auto;justify-content:flex-start}");
  }
  function popHide() { popCap = null; if (pop) pop.hidden = true; }
  function popUpdate() {
    if (!pop) return;
    var c = capture();
    if (!c || sheetOpen()) { popHide(); return; }
    popCap = c; pop.hidden = false;
    $(".rv-pop-s", pop).hidden = !c.quote || !S.suggestOn;
    placePop();
  }
  function selecting() { var s = win.getSelection(); return s && s.rangeCount && s.anchorNode && blockOf(s.anchorNode) && reader.contains(s.anchorNode.nodeType === 1 ? s.anchorNode : s.anchorNode.parentNode); }

  // ---- keys: the page's own caret, the margin's list ------------------------------------------------------------
  var GRAN = { ArrowLeft: ["backward", "character", "word"], ArrowRight: ["forward", "character", "word"], ArrowUp: ["backward", "line", "line"], ArrowDown: ["forward", "line", "line"] };
  function placeCaret(dir) {
    var bs = $$(BLOCK, reader).filter(function (b) { return !b.closest(".rv-margin") && index(b).text; }), s = win.getSelection(), at = s.rangeCount && blockOf(s.anchorNode), i = bs.indexOf(at);
    var b = bs[i < 0 ? (dir > 0 ? 0 : bs.length - 1) : Math.max(0, Math.min(bs.length - 1, i + dir))];
    if (!b) return;
    var ix = index(b), p = ix.map[0], r = doc.createRange();
    r.setStart(p[0], p[1]); r.collapse(true); s.removeAllRanges(); s.addRange(r);
    caret = true; b.scrollIntoView({ block: "center" });
    $$(".is-caret", reader).forEach(function (x) { x.classList.remove("is-caret"); }); b.classList.add("is-caret");
    say(T("caret", "Paragraph selected for reading. Shift and the arrow keys select words; C comments."));
  }
  function inControl(t) { return !!(t && t.closest && t.closest("input, textarea, select, button, a, [contenteditable]")); }
  doc.addEventListener("keydown", function (e) {
    if (!reader || e.defaultPrevented || e.ctrlKey && !GRAN[e.key] || e.metaKey) return;
    var t = e.target, inMargin = root && root.contains(t);
    if (inMargin) return;
    if (GRAN[e.key] && e.shiftKey && selecting() && !inControl(t)) {
      var g = GRAN[e.key], s = win.getSelection();
      e.preventDefault(); e.stopPropagation();
      s.modify("extend", g[0], e.altKey || e.ctrlKey ? g[2] : g[1]); popUpdate(); return;
    }
    if (inControl(t) || e.altKey || e.shiftKey) return;
    if (e.key === "[" || e.key === "]") { e.preventDefault(); placeCaret(e.key === "]" ? 1 : -1); return; }
    if (e.key === "Escape" && caret) { caret = false; win.getSelection().removeAllRanges(); $$(".is-caret", reader).forEach(function (x) { x.classList.remove("is-caret"); }); popHide(); return; }
    if (e.key === "c" || e.key === "s" && S.suggestOn) {
      var cap = capture();
      if (!cap && selecting()) { var b = blockOf(win.getSelection().anchorNode), ix = index(b); // a caret and no selection: the whole paragraph
        var sec = b.closest(".r-sec"); cap = ix.text ? { doc: sec.dataset.doc, rev: parseInt(sec.dataset.rev || "0", 10), quote: ix.text.slice(0, 400).trim(), before: "", after: "", sec: sec } : null; }
      if (!cap) { say(T("select_first", "Select some words first (press ] to place the cursor in a paragraph, then Shift and the arrow keys).")); return; }
      e.preventDefault();
      if (e.key === "c") composeOn(cap); else if (S.suggest) S.suggest.on(cap);
      return;
    }
    if (e.key === "m") { e.preventDefault(); openSheet(true); var c0 = list.querySelector(".rv-card[tabindex='0']") || list.firstElementChild; if (c0) { c0.tabIndex = 0; c0.focus(); } else say(T("none_open", "No open comments.")); return; }
    if (e.key === "n") { e.preventDefault(); next(); }
  });
  function cardKeys(e) {
    var c = e.target.closest && e.target.closest(".rv-card");
    if (!c || inControl(e.target) && e.target !== c || e.ctrlKey || e.metaKey || e.altKey) return;
    var cs = $$(".rv-card", list), i = cs.indexOf(c), to = -1;
    if (e.key === "j" || e.key === "ArrowDown") to = Math.min(cs.length - 1, i + 1);
    else if (e.key === "k" || e.key === "ArrowUp") to = Math.max(0, i - 1);
    else if (e.key === "Enter" && e.target === c) { e.preventDefault(); e.stopPropagation(); var m = $('mark.rv-mark[data-t="' + c.dataset.id + '"]'); if (m) show(c.dataset.id); return; }
    else if (e.key === "r" && e.target === c) { e.preventDefault(); e.stopPropagation(); var t = threads.filter(function (x) { return x.thread_id === c.dataset.id; })[0]; if (t) replyBox(c, t); return; }
    else if (e.key === "x" && e.target === c) { e.preventDefault(); e.stopPropagation(); var rb = $$(".rv-actions .btn", c).filter(function (x) { return x.textContent === T("resolve", "Resolve") || x.textContent === T("reopen", "Reopen"); })[0]; if (rb) rb.click(); return; }
    else if (e.key === "n" && e.target === c) { e.preventDefault(); e.stopPropagation(); next(); return; }
    else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); var mk = $('mark.rv-mark[data-t="' + c.dataset.id + '"]'); openSheet(false, true); if (mk) mk.focus({ preventScroll: true }); else if (reader) reader.focus({ preventScroll: true }); return; }
    else return;
    e.preventDefault(); e.stopPropagation();
    if (to >= 0 && cs[to]) { cs.forEach(function (x) { x.tabIndex = -1; }); cs[to].tabIndex = 0; cs[to].focus(); current = cs[to].dataset.id; threads.forEach(paint); }
  }

  // ---- the next unresolved comment ------------------------------------------------------------------------------
  function next() {
    var card_ = doc.activeElement && doc.activeElement.closest && doc.activeElement.closest(".rv-card"), after = current || (card_ ? card_.dataset.id : "");
    if (decide) {
      api("GET", base + "/review/queue?limit=1&visibility=all" + (after ? "&after=" + after : "")).then(function (r) {
        var it = r.ok && r.json && r.json.items && r.json.items[0];
        if (!it) { say(T("no_more", "No more open comments.")); return; }
        if (focusCard(it.thread.thread_id) && secOf(it.document.id)) { filter = "open"; syncFilters(); draw(); focusCard(it.thread.thread_id); show(it.thread.thread_id); return; }
        win.location.assign("/p/" + slug + "/read/" + it.document.id + "#t=" + it.thread.thread_id);
      });
      return;
    }
    var all = order(), i = all.map(function (t) { return t.thread_id; }).indexOf(after), t = all[i + 1];
    if (!t) { say(T("no_more", "No more open comments.")); return; }
    focusCard(t.thread_id); show(t.thread_id);
  }

  // ---- the shell of the margin ------------------------------------------------------------------------------------
  var FILTERS = [["open", "Open"], ["mine", "Mine"], ["mentions", "Mentions me"], ["resolved", "Resolved"]];
  function syncFilters() { $$(".rv-filters button", root).forEach(function (b) { b.setAttribute("aria-pressed", String(b.dataset.f === filter)); }); }
  function mobile() { return win.matchMedia("(max-width: 1023px)").matches; }
  // On a phone the margin is a dialog: the page behind it is inert, Tab stays inside, Escape, Close and a tap outside shut it, and
  // focus goes back to where it was.
  function openSheet(on, keepFocus) {
    if (!root) return;
    var was = root.classList.contains("is-open"), sheet = mobile(), t = $("#rv-toggle");
    root.classList.toggle("is-open", on);
    if (t && sheet) t.setAttribute("aria-pressed", String(on));
    if (!sheet || panelMode) return;
    if (on && !was) sheetFrom = doc.activeElement && !root.contains(doc.activeElement) ? doc.activeElement : null;
    if (on) { root.setAttribute("role", "dialog"); root.setAttribute("aria-modal", "true"); } else { root.removeAttribute("role"); root.removeAttribute("aria-modal"); }
    if (scrim) scrim.hidden = !on;
    reader.classList.toggle("rv-sheet-open", on); // room under the page, so the words a card is about can be brought above the sheet
    $$(".read-bar, #read-stream, #read-pager, #read-continue", reader).forEach(function (n) { n.inert = on; });
    popHide();
    if (!on && was && !keepFocus) { var f = sheetFrom && sheetFrom.isConnected ? sheetFrom : $("#rv-toggle"); sheetFrom = null; if (f) f.focus({ preventScroll: true }); }
  }
  function trap(e) {
    if (e.key !== "Tab" || !sheetOpen()) return;
    var f = $$("button, a[href], input, select, textarea, [tabindex]:not([tabindex='-1'])", root).filter(function (n) { return !n.disabled && !n.hidden && n.offsetParent !== null; });
    if (!f.length) return;
    var a = f[0], z = f[f.length - 1];
    if (e.shiftKey && (doc.activeElement === a || !root.contains(doc.activeElement))) { e.preventDefault(); z.focus(); }
    else if (!e.shiftKey && (doc.activeElement === z || !root.contains(doc.activeElement))) { e.preventDefault(); a.focus(); }
  }
  function buildFilters(host) {
    var g = el("div", "rv-filters"); g.setAttribute("role", "group"); g.setAttribute("aria-label", T("show_label", "Show"));
    FILTERS.forEach(function (f) {
      var b = el("button", "btn quiet small", T("f_" + f[0], f[1])); b.type = "button"; b.dataset.f = f[0]; b.setAttribute("aria-pressed", String(f[0] === filter));
      b.addEventListener("click", function () { filter = f[0]; syncFilters(); draw(true); });
      g.appendChild(b);
    });
    var nx = btn(T("next", "Next unresolved"), next, "quiet small"); nx.id = "rv-next"; nx.setAttribute("aria-keyshortcuts", "n");
    host.appendChild(g); host.appendChild(nx);
  }
  function refreshSoon() { win.clearTimeout(tick); tick = win.setTimeout(function () { if (!doc.hidden) reload(); }, 400); }

  function start() {
    try { M = JSON.parse(($("#rv-msg") || { dataset: {} }).dataset.msg || "{}"); } catch (e) { M = {}; }
    root = $("#rv-margin"); live = $("#rv-live");
    if (panelMode) return;
    list = $("#rv-list"); composer = $("#rv-compose"); pop = $("#rv-pop");
    buildFilters($("#rv-tools"));
    list.addEventListener("keydown", cardKeys);
    scrim = el("div", "rv-scrim"); scrim.hidden = true; reader.appendChild(scrim);
    scrim.addEventListener("click", function () { openSheet(false); });
    var cl = $("#rv-close"); if (cl) cl.addEventListener("click", function () { openSheet(false); });
    root.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !e.defaultPrevented && sheetOpen()) { e.preventDefault(); openSheet(false); return; }
      trap(e);
    });
    // what is waiting for the person to leave a reply they were writing is drawn once they have (not while a finger is down on a button)
    doc.addEventListener("pointerdown", function () { down = true; }); doc.addEventListener("pointerup", function () { down = false; });
    list.addEventListener("focusout", function () { win.setTimeout(function () { if (dirty && !down && !typingIn()) draw(); }, 350); });
    var tg = $("#rv-toggle");
    if (tg) {
      // On a wide page the margin is beside the text and the button hides or shows it; on a phone it is a sheet that starts closed.
      tg.setAttribute("aria-pressed", String(!mobile()));
      tg.addEventListener("click", function () {
        var on = tg.getAttribute("aria-pressed") !== "true";
        if (mobile()) openSheet(on);
        else { tg.setAttribute("aria-pressed", String(on)); reader.classList.toggle("rv-hidden", !on); reader.focus({ preventScroll: true }); }
      });
    }
    $(".rv-pop-c", pop).addEventListener("mousedown", function (e) { e.preventDefault(); });
    $(".rv-pop-c", pop).addEventListener("click", function () { composeOn(popCap); });
    $(".rv-pop-s", pop).addEventListener("mousedown", function (e) { e.preventDefault(); });
    $(".rv-pop-s", pop).addEventListener("click", function () { if (S.suggest && popCap) S.suggest.on(popCap); });
    var st = 0, raf = 0;
    win.addEventListener("scroll", function () { if (pop && !pop.hidden && !raf) raf = win.requestAnimationFrame(function () { raf = 0; placePop(); }); }, { passive: true });
    doc.addEventListener("selectionchange", function () { win.clearTimeout(st); st = win.setTimeout(popUpdate, 120); });
    doc.addEventListener("click", function (e) { var m = e.target.closest && e.target.closest("mark.rv-mark"); if (m && list) { openSheet(true); if (focusCard(m.dataset.t) && sheetOpen()) reveal(m); } });
    doc.addEventListener("visibilitychange", function () { if (!doc.hidden) refreshSoon(); });
    doc.addEventListener("scriobh:event", function (e) { var t = (e.detail && e.detail.type) || ""; if (/^(comment|suggestion)\./.test(t) || t === "reset") refreshSoon(); });
    // chapters that arrive as the person scrolls get their marks
    new win.MutationObserver(function () { sections().forEach(function (s) { if (!loaded[s.dataset.doc]) { loaded[s.dataset.doc] = "pending"; loadDoc(s.dataset.doc).then(function () { draw(); if (S.suggest && S.suggest.ready) S.suggest.load(); }); } }); }).observe($("#read-stream"), { childList: true });
    var sug = $("#rv-suggest");
    function withSuggest(cb) {
      if (S.suggest && S.suggest.ready) return cb();
      if (doc.querySelector("script[data-sg]")) return;
      var sc = doc.createElement("script"); sc.src = reader.dataset.suggestSrc; sc.dataset.sg = "1";
      sc.onload = function () { S.suggest.init(S.review); S.suggest.ready = true; cb(); };
      sc.onerror = function () { say(T("failed", "That did not work. Try again.")); };
      doc.head.appendChild(sc);
    }
    if (sug) sug.addEventListener("click", function () {
      var on = sug.getAttribute("aria-pressed") !== "true";
      withSuggest(function () { S.suggest.mode(on); sug.setAttribute("aria-pressed", String(on)); reader.focus({ preventScroll: true }); });
    });
    S.review = { who: who, api: api, T: T, say: say, stamp: stamp, armed: armed, hold: hold, sheetOpen: sheetOpen, reveal: reveal, el: el, $: $, $$: $$, btn: btn, why: why, index: index, locate: locate, wrap: wrap, unmark: unmark, secOf: secOf, sections: sections, base: base, me: me, decide: decide, slug: slug, project: project, form: form, reload: reload, popUpdate: popUpdate, popHide: popHide, root: root, blockOf: blockOf, capture: capture, openSheet: openSheet, reader: reader, get threads() { return threads; } };
    api("GET", base + "/suggestions?status=open&limit=1").then(function (r) { if (r.ok && r.json && r.json.total > 0) withSuggest(function () { /* the marks and the tab are enough */ }); });
    reload().then(function () {
      var m = /^#t=(.+)$/.exec(win.location.hash);
      if (m) { filter = "open"; var t = threads.filter(function (x) { return x.thread_id === m[1]; })[0]; if (t && t.status !== "open") { filter = "resolved"; } syncFilters(); draw(); openSheet(true); if (focusCard(m[1])) show(m[1]); }
    });
  }

  // ---- the writing view's Comments tab --------------------------------------------------------------------------------
  if (panelMode) {
    // The tab's own file (js/cwrite.js: start a comment, edit, show it in the text) is fetched the first time the tab is shown.
    function more(panel) {
      if (S.cwrite) { S.cwrite.show(panel); return; }
      if (doc.querySelector("script[data-cw]") || !panel.dataset.cwrite) return;
      var sc = el("script"); sc.src = panel.dataset.cwrite; sc.dataset.cw = "1";
      sc.onload = function () { if (S.cwrite) S.cwrite.show(panel); };
      sc.onerror = function () { sc.remove(); say(T("failed", "That did not work. Try again.")); };
      doc.head.appendChild(sc);
    }
    S.review = { T: T, el: el, $: $, btn: btn, api: api, say: say, why: why, form: form, replace: replace, draw: draw, focusCard: focusCard, base: base, me: me, people: loadPeople,
      get threads() { return threads; },
      swap: function (t) { var i = threads.map(function (x) { return x.thread_id; }).indexOf(t.thread_id); if (i >= 0) threads[i] = t; else threads.push(t); draw(true); },
      added: function (t) { threads.push(t); filter = "open"; syncFilters(); draw(true); },
      show: function (panel) {
      try { M = JSON.parse((($("#rv-msg") || { dataset: {} }).dataset.msg) || "{}"); } catch (e) { M = {}; }
      live = $("#rv-live") || el("div", "sr-only"); live.setAttribute("role", "status");
      if (!$("link[data-rv]") && panel.dataset.css) { var l = el("link"); l.rel = "stylesheet"; l.href = panel.dataset.css; l.dataset.rv = "1"; doc.head.appendChild(l); }
      if (!panel.dataset.ready) {
        panel.dataset.ready = "1"; root = panel;
        var h = el("div", "rv-tools"); buildFilters(h); panel.appendChild(h);
        list = el("ol", "rv-list"); panel.appendChild(list);
        panel.appendChild(el("p", "rv-empty", "")).id = "rv-empty";
        panel.appendChild(el("p", "fine", T("panel_note", "Comments are shown here and in the reading view. Their words can be marked in the text.")));
        list.addEventListener("keydown", cardKeys);
        list.addEventListener("focusout", function () { win.setTimeout(function () { if (dirty && !down && !typingIn()) draw(); }, 350); });
        doc.addEventListener("pointerdown", function () { down = true; }); doc.addEventListener("pointerup", function () { down = false; });
        // What is private sends no event, so another tab of the same person learns of it when it is looked at again.
        doc.addEventListener("scriobh:event", function (e) { var t = (e.detail && e.detail.type) || ""; if (/^comment\./.test(t)) refreshSoon(); });
        doc.addEventListener("visibilitychange", function () { if (!doc.hidden && current_()) refreshSoon(); });
      }
      root = panel; reload(); more(panel);
    } };
    function current_() { return S.inspector && S.inspector.current === "comments"; }
    return;
  }
  if (doc.readyState === "loading") doc.addEventListener("DOMContentLoaded", start); else start();
})();
