/* Scríobh: the page a share link opens, for a person with no account. It reads the link from the address, asks
   /s/{token}/api for the contents and each chapter as the chapter is reached, and for a comment link takes a name and
   comments on a selection. No cookie is read or written and nothing goes anywhere but this address: the passphrase's proof
   (a header, since a page that is navigated to cannot send one) is kept in this tab's session storage, and the name for
   the next comment likewise. Chapter text arrives as a fragment from the server's whitelist renderer and is the only
   markup inserted; everything a person wrote is set as text. Strings come from the page (data-msg). */
(function () {
  "use strict";
  var doc = document, win = window, root = doc.getElementById("sh");
  if (!root) return;
  var base = win.location.pathname.replace(/\/+$/, ""), PK = "scriobh.share." + base, NK = "scriobh.share.name";
  var M = {}, man = null, loaded = 0, threads = {}, sec = {}, popCap = null, caret = false, proof = "", justUnlocked = false;
  var rdrafts = {}; // an unsent reply, by thread, kept through a redraw
  var MK = "scriobh.share.mine." + base, DK = "scriobh.share.draft." + base;
  function mineIds() { try { return JSON.parse(mem(MK) || "[]"); } catch (e) { return []; } }
  function iMade(id) { return mineIds().indexOf(id) >= 0; }
  function iMake(id) { var a = mineIds(); a.push(id); mem(MK, JSON.stringify(a.slice(-200))); }
  var BLOCK = ".r-prose p, .r-prose li, .r-prose h2, .r-prose h3, .r-prose h4, .r-prose h5, .r-prose h6";
  var SKIP = BLOCK + ", sup.fnref, .omitted";
  try { M = JSON.parse(doc.getElementById("sh-msg").dataset.msg || "{}"); } catch (e) { M = {}; }
  function T(k, d, v) { var s = M[k] || d; if (v) for (var n in v) s = s.split("{" + n + "}").join(v[n]); return s; }
  function $(s, r) { return (r || doc).querySelector(s); }
  function $$(s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); }
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function say(m) { var l = $("#sh-live"); l.textContent = ""; win.setTimeout(function () { l.textContent = m; }, 40); }
  function mem(k, v) { try { if (v === undefined) return win.sessionStorage.getItem(k) || ""; win.sessionStorage.setItem(k, v); } catch (e) { return ""; } return v; }
  proof = mem(PK);

  function api(method, path, body) {
    var h = { Accept: "application/json" };
    if (proof) h["X-Share-Unlock"] = proof;
    if (body !== undefined) h["Content-Type"] = "application/json";
    return fetch(base + "/api" + path, { method: method, credentials: "omit", referrerPolicy: "no-referrer", headers: h, body: body === undefined ? undefined : JSON.stringify(body) }).then(function (r) {
      return r.text().then(function (t) {
        var j = null;
        try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ }
        return { status: r.status, ok: r.ok, json: j, wait: parseInt(r.headers.get("Retry-After") || "0", 10) || 0 };
      });
    }, function () { return { status: 0, ok: false, json: null, wait: 0 }; });
  }
  function msg(r) { return (r.json && r.json.error && r.json.error.message) || (r.status === 0 ? T("offline", "Could not reach the server. Try again in a moment.") : T("failed", "That did not work. Try again.")); }
  function state(s) {
    root.dataset.state = s;
    ["loading", "unlock", "gone"].forEach(function (k) { var e = $("#sh-" + k); if (e) e.hidden = k !== s; });
    $("#sh-main").hidden = s !== "ready";
  }
  function gone() { $("#sh-title").textContent = T("gone_h", "This link is not available"); $("#sh-sub").textContent = ""; state("gone"); }

  // ---- opening ----------------------------------------------------------------------------------------------------
  function start() {
    api("GET", "/manifest").then(function (r) {
      if (r.status === 401) return unlock();
      if (r.status === 0) return offline();
      if (!r.ok || !r.json) return gone();
      open(r.json);
    });
  }
  // no network is not a link that is gone: say so where it will be heard, with a way to try again
  function offline() {
    var n = $("#sh-loading"), b = el("button", "btn small", T("retry", "Try again"));
    n.textContent = T("opening_failed", "Could not reach the server. Check your connection, then try again.") + " "; n.setAttribute("role", "alert");
    b.type = "button"; b.addEventListener("click", function () { n.setAttribute("role", "status"); n.textContent = T("loading", "Opening the text."); start(); });
    n.appendChild(b); b.focus();
  }
  function unlock() {
    state("unlock");
    var f = $("#sh-unlock-form"), err = $("#sh-unlock-err"), pw = $("#sh-pass");
    pw.focus();
    pw.addEventListener("input", function () { pw.removeAttribute("aria-invalid"); pw.removeAttribute("aria-describedby"); err.hidden = true; });
    f.onsubmit = function (e) {
      e.preventDefault();
      err.hidden = true;
      api("POST", "/unlock", { passphrase: pw.value }).then(function (r) {
        if (r.ok && r.json && r.json.proof) { proof = r.json.proof; mem(PK, proof); pw.value = ""; justUnlocked = true; state("loading"); return start(); }
        if (r.status === 404) return gone();
        err.textContent = r.status === 429 ? T("wait", "Too many tries from here. Wait {n} seconds and try again.", { n: r.wait || 30 }) : r.status === 401 ? T("wrong", "That passphrase is not right.") : msg(r);
        err.hidden = false; pw.setAttribute("aria-invalid", "true"); pw.setAttribute("aria-describedby", "sh-unlock-err"); pw.select();
      });
    };
  }
  function open(m) {
    man = m;
    root.dataset.scope = m.scope;
    var docsList = m.documents || [];
    $("#sh-title").textContent = m.title;
    doc.title = m.title;
    $("#sh-sub").textContent = (m.scope === "comment" ? T("sub_comment", "You can read this and leave comments.") : T("sub_read", "You can read this.")) + " " + T("words", "{n} words", { n: String(m.words).replace(/\B(?=(\d{3})+(?!\d))/g, ",") });
    var ol = $("#sh-toc-list");
    if (win.matchMedia("(max-width: 700px)").matches && docsList.length > 3 || docsList.length > 12) $("#sh-toc-d").open = false;
    docsList.forEach(function (d, i) {
      var li = el("li", "d" + Math.min(d.depth || 0, 3) + (d.kind === "part" ? " is-part" : "")), a = el("a", null, d.title || T("untitled", "Untitled"));
      a.href = "#c-" + d.id;
      a.addEventListener("click", function (e) { e.preventDefault(); loadTo(i).then(function () { goto(d.id); }); });
      li.appendChild(a); if (d.threads) li.appendChild(el("span", "sh-n", " " + d.threads));
      ol.appendChild(li);
    });
    if (m.scope === "comment") { $("#sh-who").hidden = false; $("#sh-comments").hidden = false; $("#sh-name").value = mem(NK); $("#sh-name").addEventListener("input", function () { mem(NK, this.value.trim()); }); }
    if (m.allow_download) { var dl = $("#sh-dl"); dl.hidden = false; dl.href = "#"; dl.addEventListener("click", download); }
    state("ready");
    if (justUnlocked) { justUnlocked = false; var h1 = $("#sh-title"); h1.tabIndex = -1; h1.focus(); } // focus was on the form that is gone
    if (!docsList.length) { $("#sh-book").appendChild(el("p", "sh-note", T("nothing", "There is nothing to read here yet."))); return; }
    loadTo(0).then(function () { var d0 = readDraft(); if (d0) { compose(d0.cap, d0.body); } });
    var more = el("div", "sh-more"); more.id = "sh-more"; $("#sh-book").after(more);
    if (win.IntersectionObserver) new win.IntersectionObserver(function (es) { if (es[0].isIntersecting) loadTo(loaded); }, { rootMargin: "1200px" }).observe(more);
    var all = $("#sh-all"); all.hidden = docsList.length < 2; all.addEventListener("click", function () { loadTo(docsList.length - 1).then(function () { all.hidden = true; say(T("all_loaded", "The whole text is loaded.")); }); });
  }
  function goto(id) { var s = $("#c-" + id); if (s) { s.scrollIntoView(); s.focus({ preventScroll: true }); } }

  // ---- chapters ---------------------------------------------------------------------------------------------------
  var chain = Promise.resolve();
  function loadTo(i) { var p = chain.then(function () { return step(i); }); chain = p.then(null, function () { /* the next call tries again */ }); return p; }
  function step(i) {
    var list = man.documents;
    if (loaded > i || loaded >= list.length) return Promise.resolve();
    var d = list[loaded];
    return api("GET", "/docs/" + d.id).then(function (r) {
      if (!r.ok || !r.json) { if (r.status === 401 || r.status === 404) return gone(); say(msg(r)); return; }
      var s = el("section", "chapter"), h = el("header", "chapter-top"), t = el("h2", "r-title", r.json.title || d.title || T("untitled", "Untitled"));
      s.id = "c-" + d.id; s.tabIndex = -1; s.dataset.doc = d.id; s.setAttribute("aria-labelledby", "h-" + d.id); t.id = "h-" + d.id;
      h.appendChild(t);
      if (man.scope === "comment") { var cb = el("button", "btn quiet small", T("on_chapter", "Comment on this chapter")); cb.type = "button"; cb.addEventListener("click", function () { compose({ doc: d.id, whole: true }); }); h.appendChild(cb); }
      s.appendChild(h);
      var body = el("div", "r-sec"); body.dataset.doc = d.id;
      var prose = el("div", "r-prose"); prose.innerHTML = r.json.html; // the server's whitelist renderer: safe under this page's policy
      body.appendChild(prose); s.appendChild(body);
      $("#sh-book").appendChild(s); sec[d.id] = body; loaded++;
      threads[d.id] = r.json.threads || [];
      threads[d.id].forEach(function (th) { mark(th); });
      drawThreads();
      var all = $("#sh-all"); if (loaded >= list.length) all.hidden = true;
      if (loaded <= i) return step(i);
      var more = $("#sh-more"); if (more && loaded < list.length && more.getBoundingClientRect().top < win.innerHeight * 2) return step(loaded);
    });
  }

  // ---- the text of a block as it is shown (as the reading view does it) ----------------------------------------------
  function index(block) {
    var w = doc.createTreeWalker(block, 1 | 4, { acceptNode: function (n) { if (n.nodeType === 1) return n !== block && n.matches(SKIP) ? 2 : 3; return 1; } });
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
  function blockOf(n) { var e = n.nodeType === 1 ? n : n.parentElement; return e && e.closest ? e.closest(BLOCK) : null; }
  function mark(th) {
    var host = sec[th.document_id];
    if (!host || th.anchor !== "active" || !th.quote) return;
    var cands = [];
    $$(BLOCK, host).forEach(function (b) {
      var ix = index(b), from = 0, i;
      while ((i = ix.text.indexOf(th.quote, from)) >= 0) {
        var pre = ix.text.slice(0, i).replace(/ +$/, ""), post = ix.text.slice(i + th.quote.length).replace(/^ +/, "");
        cands.push({ ix: ix, a: i, e: i + th.quote.length, ok: (!th.before || pre.slice(-th.before.length) === th.before) && (!th.after || post.indexOf(th.after) === 0) });
        from = i + 1;
      }
    });
    var c = cands.filter(function (x) { return x.ok; })[0] || (cands.length === 1 ? cands[0] : null);
    if (!c) return;
    var segs = [], last = null;
    for (var i = c.a; i < c.e; i++) { var p = c.ix.map[i]; if (last && last.n === p[0]) last.e = p[1] + 1; else { last = { n: p[0], s: p[1], e: p[1] + 1 }; segs.push(last); } }
    segs.reverse().forEach(function (g) { var r = doc.createRange(), m = el("mark", "sh-mark"); m.dataset.t = th.id; r.setStart(g.n, g.s); r.setEnd(g.n, g.e); r.surroundContents(m); });
  }

  // ---- comments (a comment link) ----------------------------------------------------------------------------------------
  function drawThreads() {
    var ol = $("#sh-threads"); if (!ol || man.scope !== "comment") return;
    if ($("textarea", ol) && ol.contains(doc.activeElement)) return;
    ol.textContent = "";
    man.documents.slice(0, loaded).forEach(function (d) {
      (threads[d.id] || []).forEach(function (th) {
        var li = el("li", "sh-thread" + (th.status === "resolved" ? " is-resolved" : "")); li.id = "th-" + th.id;
        li.appendChild(el("p", "sh-where", (d.title || "") + (th.status === "resolved" ? " · " + T("resolved", "Resolved") : "")));
        if (th.quote) li.appendChild(el("blockquote", "sh-quote", "“" + th.quote + "”"));
        else if (th.anchor === "orphaned") li.appendChild(el("p", "sh-where", T("lost", "The words this was on have changed.")));
        (th.comments || []).forEach(function (c) { var p = el("p", "sh-c"); p.appendChild(el("strong", null, c.author + (iMade(c.id) ? " " + T("you", "(you)") : ""))); p.appendChild(doc.createTextNode(": " + c.body)); p.dir = "auto"; li.appendChild(p); });
        var rb = el("button", "btn quiet small", T("reply", "Reply")); rb.type = "button"; rb.addEventListener("click", function () { reply(li, th); }); li.appendChild(rb);
        if (th.quote) { var sb = el("button", "btn quiet small", T("show", "Show in the text")); sb.type = "button"; sb.addEventListener("click", function () { var m = $('mark[data-t="' + th.id + '"]'); if (m) { m.tabIndex = -1; m.scrollIntoView({ block: "center" }); m.focus({ preventScroll: true }); } }); li.appendChild(sb); }
        ol.appendChild(li);
        if (rdrafts[th.id] != null) reply(li, th, true);
      });
    });
    if (!ol.firstChild) ol.appendChild(el("li", "fine", T("none", "There are no comments on this link yet.")));
  }
  function iMark(t) { (t && t.comments || []).slice(-1).forEach(function (c) { iMake(c.id); }); } // the comment this tab just made
  function nameOK() {
    var n = $("#sh-name"), v = n.value.trim();
    if (!v) { n.focus(); say(T("need_name", "Write the name to put on your comments first.")); return ""; }
    mem(NK, v); return v;
  }
  function readDraft() { try { var d = JSON.parse(mem(DK) || "null"); return d && d.cap && d.cap.doc ? d : null; } catch (e) { return null; } }
  function form(label, id, go, cancel, draft) {
    var f = el("form", "sh-form"), fd = el("div", "field"), l = el("label", null, label), ta = el("textarea"), err = el("p", "field-error"), row = el("div", "form-actions"), ok = el("button", "btn solid small", T("send", "Send")), no = el("button", "btn quiet small", T("cancel", "Cancel"));
    l.htmlFor = ta.id = id; ta.rows = 4; ta.maxLength = 2000; err.hidden = true; err.setAttribute("role", "alert"); ok.type = "submit"; no.type = "button";
    no.addEventListener("click", cancel); row.appendChild(no); row.appendChild(ok); fd.appendChild(l); fd.appendChild(ta); f.appendChild(fd); f.appendChild(err); f.appendChild(row);
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      var body = ta.value.trim(), nm = nameOK();
      if (!body) { err.textContent = T("empty", "Write something first."); err.hidden = false; return; }
      if (!nm) return;
      ok.disabled = true;
      go(body, nm).then(function (m) { ok.disabled = false; if (m) { err.textContent = m; err.hidden = false; } });
    });
    if (draft) ta.addEventListener("input", function () { mem(DK, JSON.stringify({ cap: draft, body: ta.value })); });
    f.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); cancel(); } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); f.requestSubmit(); } });
    return { f: f, ta: ta };
  }
  function post(payload) {
    return api("POST", "/comments", payload).then(function (r) {
      if (r.status === 429) return { err: T("wait", "Too many tries from here. Wait {n} seconds and try again.", { n: r.wait || 30 }) };
      if (!r.ok || !r.json) return { err: msg(r) };
      return { thread: r.json.thread };
    });
  }
  function compose(cap, body0) {
    var box = $("#sh-compose"); box.hidden = false; box.textContent = "";
    if (!cap.whole) box.appendChild(el("p", "sh-quote", "“" + cap.quote + "”"));
    var fm = form(cap.whole ? T("your_chapter", "Your comment on this chapter") : T("your_comment", "Your comment"), "sh-new", function (body, name) {
      return post({ document_id: cap.doc, selection: cap.whole ? "" : cap.quote, before: cap.before || "", after: cap.after || "", body: body, name: name }).then(function (o) {
        if (o.err) return o.err;
        threads[cap.doc] = (threads[cap.doc] || []).filter(function (x) { return x.id !== o.thread.id; }).concat([o.thread]);
        box.hidden = true; box.textContent = ""; mem(DK, ""); iMark(o.thread); mark(o.thread); drawThreads(); popHide(); win.getSelection().removeAllRanges(); say(T("added", "Your comment was added."));
        var li = $("#th-" + o.thread.id); if (li) { li.tabIndex = -1; li.scrollIntoView({ block: "nearest" }); li.focus(); }
      });
    }, function () { box.hidden = true; box.textContent = ""; mem(DK, ""); popHide(); }, cap);
    if (body0) fm.ta.value = body0;
    box.appendChild(fm.f); $("#sh-comments").scrollIntoView({ block: "nearest" }); fm.ta.focus();
  }
  function reply(li, th, restore) {
    if ($(".sh-form", li)) { $("textarea", li).focus(); return; }
    var fm = form(T("your_reply", "Your reply"), "sh-r-" + th.id, function (body, name) {
      return post({ document_id: th.document_id, parent_id: th.id, body: body, name: name }).then(function (o) {
        if (o.err) return o.err;
        var cur = threads[th.document_id].filter(function (x) { return x.id === th.id; })[0];
        if (cur) cur.comments = (cur.comments || []).concat(o.thread.comments.slice(-1));
        delete rdrafts[th.id]; iMark(o.thread); fm.f.remove(); drawThreads(); say(T("replied", "Your reply was added."));
        var li2 = $("#th-" + th.id); if (li2) { li2.tabIndex = -1; li2.focus(); }
      });
    }, function () { delete rdrafts[th.id]; fm.f.remove(); li.focus(); });
    if (rdrafts[th.id]) fm.ta.value = rdrafts[th.id]; else rdrafts[th.id] = "";
    fm.ta.addEventListener("input", function () { rdrafts[th.id] = fm.ta.value; });
    li.appendChild(fm.f); if (!restore) fm.ta.focus();
  }
  function capture() {
    var s = win.getSelection();
    if (!s || !s.rangeCount || s.isCollapsed) return null;
    var r = s.getRangeAt(0), a = blockOf(r.startContainer), b = blockOf(r.endContainer);
    if (!a || !$("#sh-book").contains(a)) return null;
    if (a !== b) return { err: T("one_block", "Select inside one paragraph.") };
    var ix = index(a), i0 = -1, i1 = -1;
    for (var i = 0; i < ix.map.length; i++) { if (r.comparePoint(ix.map[i][0], ix.map[i][1]) === 0) { if (i0 < 0) i0 = i; i1 = i + 1; } }
    var q = i0 < 0 ? "" : ix.text.slice(i0, i1).trim();
    if (!q) return null;
    return { doc: a.closest(".r-sec").dataset.doc, quote: q, before: ix.text.slice(Math.max(0, i0 - 24), i0).replace(/^ +/, ""), after: ix.text.slice(i1, i1 + 24).replace(/ +$/, "") };
  }
  function popHide() { popCap = null; $("#sh-pop").hidden = true; }
  var st = 0;
  function popUpdate() {
    if (!man || man.scope !== "comment") return;
    var c = capture();
    if (!c || c.err) { popHide(); if (c && c.err) say(c.err); return; }
    popCap = c; $("#sh-pop").hidden = false;
  }
  doc.addEventListener("selectionchange", function () { win.clearTimeout(st); st = win.setTimeout(popUpdate, 120); });
  $("#sh-pop-b").addEventListener("mousedown", function (e) { e.preventDefault(); });
  $("#sh-pop-b").addEventListener("click", function () { if (popCap) compose(popCap); });

  // a keyboard can place a cursor in the text and select from it, as the reading view does
  var GRAN = { ArrowLeft: ["backward", "character", "word"], ArrowRight: ["forward", "character", "word"], ArrowUp: ["backward", "line", "line"], ArrowDown: ["forward", "line", "line"] };
  doc.addEventListener("keydown", function (e) {
    if (!man || man.scope !== "comment" || e.defaultPrevented || e.metaKey) return;
    var t = e.target, ctl = t && t.closest && t.closest("input, textarea, select, button, a");
    var s = win.getSelection(), inBook = s && s.rangeCount && s.anchorNode && $("#sh-book").contains(s.anchorNode.nodeType === 1 ? s.anchorNode : s.anchorNode.parentNode);
    if (GRAN[e.key] && e.shiftKey && inBook && !ctl) { var g = GRAN[e.key]; e.preventDefault(); s.modify("extend", g[0], e.altKey || e.ctrlKey ? g[2] : g[1]); popUpdate(); return; }
    if (ctl || e.ctrlKey || e.altKey || e.shiftKey) return;
    if (e.key === "[" || e.key === "]") {
      var bs = $$(BLOCK, $("#sh-book")).filter(function (b) { return index(b).text; }), at = inBook && blockOf(s.anchorNode), i = bs.indexOf(at);
      var b = bs[i < 0 ? (e.key === "]" ? 0 : bs.length - 1) : Math.max(0, Math.min(bs.length - 1, i + (e.key === "]" ? 1 : -1)))];
      if (!b) return;
      var p = index(b).map[0], r = doc.createRange(); r.setStart(p[0], p[1]); r.collapse(true); s.removeAllRanges(); s.addRange(r);
      b.scrollIntoView({ block: "center" }); e.preventDefault(); caret = true; say(T("caret", "Paragraph chosen. Shift and the arrow keys select words; C comments.")); return;
    }
    if (e.key === "c" && caret) { var cap = capture(); if (!cap && inBook) { var bb = blockOf(s.anchorNode), ix = index(bb); cap = ix.text ? { doc: bb.closest(".r-sec").dataset.doc, quote: ix.text.slice(0, 400).trim(), before: "", after: "" } : null; } if (cap && !cap.err) { e.preventDefault(); compose(cap); } }
  });

  // ---- the download, a print, a name ------------------------------------------------------------------------------------
  function download(e) {
    e.preventDefault();
    var h = {}; if (proof) h["X-Share-Unlock"] = proof;
    fetch(base + "/api/download", { credentials: "omit", referrerPolicy: "no-referrer", headers: h }).then(function (r) {
      if (!r.ok) throw r;
      return r.blob();
    }).then(function (b) {
      var u = URL.createObjectURL(b), a = el("a"); a.href = u; a.download = (man.title || "book").replace(/[^\w.-]+/g, "-") + ".md"; doc.body.appendChild(a); a.click(); a.remove();
      win.setTimeout(function () { URL.revokeObjectURL(u); }, 1000);
    }, function () { say(T("failed", "That did not work. Try again.")); });
  }
  doc.addEventListener("keydown", function (e) { if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key === "p" && man && loaded < man.documents.length) { e.preventDefault(); $("#sh-print").click(); } });
  // The button asks the browser to print. Some windows have no print panel of their own (the desktop application's): there
  // the request does nothing and no print event comes, and the page says where printing is instead of staying silent.
  var asked = false;
  function print() {
    asked = false; win.print();
    win.setTimeout(function () { if (!asked) say(man && man.allow_download ? T("print_none_dl", "Printing did not start from this button. Use the Print command of your browser or application (File, Print), or download the text and print that.") : T("print_none", "Printing did not start from this button. Use the Print command of your browser or application (File, Print).")); }, 600);
  }
  win.addEventListener("beforeprint", function () { asked = true; if (man && loaded < man.documents.length) loadTo(man.documents.length - 1); });
  $("#sh-print").addEventListener("click", function () { if (man && loaded < man.documents.length) { say(T("printing", "Loading the whole text before printing.")); loadTo(man.documents.length - 1).then(function () { win.setTimeout(print, 200); }); } else print(); });

  start();
})();
