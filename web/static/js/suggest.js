/* Scríobh review: suggestions on the reading view, loaded by review.js when the chapter has open ones or when a
   reviewer or editor turns Suggesting on. A suggestion is a proposal beside the text: the old words stay and are struck
   through, the new words follow them, and nothing is applied except by an editor's Accept (suggestion.accept and
   accept_all, which check the words are still where they were and say plainly when they are not). The text here is
   never changed by this script: after a decision it fetches the chapter again. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, R, sugg = [], creating = false, panel, tabs, tabNow = "comments", plan = null;
  function T(k, d, v) { return R.T(k, d, v); }
  function plainish(s) { return String(s || "").replace(/[*_~`]/g, ""); }

  // ---- the marks -----------------------------------------------------------------------------------------------------
  function unmarkS(sec) {
    R.$$("ins.rv-ins", sec).forEach(function (n) { n.remove(); });
    R.$$("del.rv-del", sec).forEach(function (m) { var p = m.parentNode; while (m.firstChild) p.insertBefore(m.firstChild, m); p.removeChild(m); p.normalize(); });
  }
  function insAt(sec, before, after, text, id) {
    // an insertion is a point: where the words before it end and the words after it begin
    var best = null;
    R.$$(".r-prose p, .r-prose li, .r-prose h3, .r-prose h4, .r-prose h5, .r-prose h6", sec).forEach(function (b) {
      if (best) return;
      var ix = R.index(b), from = 0, i;
      var tryAt = function (p) {
        var pre = ix.text.slice(0, p).replace(/ +$/, ""), post = ix.text.slice(p).replace(/^ +/, "");
        return (!before || pre.slice(-before.length) === before) && (!after || post.indexOf(after) === 0);
      };
      if (!before) { if (tryAt(0) && after) best = { ix: ix, p: 0 }; return; }
      while ((i = ix.text.indexOf(before, from)) >= 0) { if (tryAt(i + before.length)) { best = { ix: ix, p: i + before.length }; return; } from = i + 1; }
    });
    if (!best) return null;
    var m = best.ix.map, p = best.p, r = doc.createRange(), n = m[p] ? m[p] : m[p - 1];
    if (!n) return null;
    var off = m[p] ? n[1] : n[1] + 1;
    // a gap between words is a space in the page: put the insertion where the words before it end
    if (m[p] && p > 0 && best.ix.text.charAt(p) === " ") { var q = m[p - 1]; r.setStart(q[0], q[1] + 1); } else r.setStart(n[0], off);
    r.collapse(true);
    var ins = R.el("ins", "rv-ins", text); ins.dataset.s = id;
    r.insertNode(ins);
    return ins;
  }
  function markOne(s) {
    var sec = R.secOf(s.document_id);
    s.placed = false;
    if (!sec || s.stale || s.status !== "open") return;
    var ok = true, made = [];
    s.changes.forEach(function (c) {
      if (c.kind === "replace" && c.original_plain) {
        var hit = R.locate(sec, c.original_plain, c.before_plain, c.after_plain);
        if (!hit) { ok = false; return; }
        var ms = R.wrap(hit.ix, hit.a, hit.b, function () { var d = R.el("del", "rv-del"); d.dataset.s = s.id; return d; });
        if (c.text) { var last = ms[0], ins = R.el("ins", "rv-ins", c.text); ins.dataset.s = s.id; last.parentNode.insertBefore(ins, last.nextSibling); made.push(ins); }
        made = made.concat(ms);
      } else if (c.kind === "insert") {
        var i = insAt(sec, c.before_plain, c.after_plain, c.text, s.id);
        if (i) made.push(i); else ok = false;
      } else ok = false;
    });
    s.placed = ok && made.length > 0;
  }
  function markAll() {
    R.sections().forEach(function (sec) { unmarkS(sec); });
    sugg.forEach(markOne);
    // comment marks were laid over the same words: they are drawn again by the next reload when text moved
  }

  // ---- loading -----------------------------------------------------------------------------------------------------
  function load() {
    var ids = R.sections().map(function (s) { return s.dataset.doc; });
    return Promise.all(ids.map(function (id) {
      return R.api("GET", R.base + "/suggestions?document_id=" + id + "&plain=true&status=open&limit=200").then(function (r) { return r.ok && r.json ? r.json.items : []; });
    })).then(function (all) { sugg = [].concat.apply([], all); markAll(); draw(); count(); });
  }
  function count() { var b = R.$("#rv-suggest-n"); if (b) b.textContent = sugg.length ? String(sugg.length) : ""; }
  function titleOf(docId) { var sec = R.secOf(docId), ch = sec && sec.closest("section.chapter"); return ch ? ch.dataset.title : ""; }
  function refreshChapter(docId) {
    var sec = R.secOf(docId), ch = sec && sec.closest("section.chapter");
    if (!ch) return Promise.resolve();
    return fetch("/ui/projects/" + R.project + "/read/" + ch.dataset.doc, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) { return r.ok ? r.text() : ""; }).then(function (t) {
      var box = doc.createElement("div"); box.innerHTML = t;
      var n = box.querySelector("section.chapter");
      if (n && ch.parentNode) ch.replaceWith(n);
      return R.reload().then(load);
    });
  }

  // ---- the Suggestions tab ---------------------------------------------------------------------------------------------
  function tab(name) {
    tabNow = name;
    var c = name === "comments";
    ["#rv-list", "#rv-tools", "#rv-empty"].forEach(function (s) { var e = R.$(s); if (e) e.hidden = !c; });
    panel.hidden = c;
    R.$$(".rv-tabs .btn", R.root).forEach(function (b) { b.setAttribute("aria-pressed", String(b.dataset.tab === name)); });
    R.$("#rv-h").textContent = c ? T("comments", "Comments") : T("suggestions", "Suggestions");
    if (!c) draw();
  }
  function change(c) {
    var p = R.el("p", "rv-chg"), old = c.original_plain || plainish(c.original);
    if (c.kind === "insert") { p.appendChild(R.el("ins", "rv-ins", c.text)); return p; }
    p.appendChild(R.el("del", "rv-del", old));
    if (c.text) { p.appendChild(doc.createTextNode(" ")); p.appendChild(R.el("ins", "rv-ins", c.text)); }
    return p;
  }
  function card(s) {
    var li = R.el("li", "rv-card"); li.id = "rv-s-" + s.id; li.tabIndex = -1; li.dataset.id = s.id;
    var meta = R.el("p", "rv-meta"); meta.appendChild(R.el("strong", null, R.who(s.author))); meta.appendChild(R.stamp(s.created_at));
    li.appendChild(meta);
    s.changes.forEach(function (c) { li.appendChild(change(c)); });
    if (s.note) li.appendChild(R.el("p", "rv-body", s.note));
    if (s.stale) li.appendChild(R.el("p", "rv-stale", T("stale", "The chapter has changed since this was suggested. It can still be accepted where the same words are found again exactly once, and is refused if they are not.")));
    else if (!s.placed && R.secOf(s.document_id)) li.appendChild(R.el("p", "rv-moved", T("s_unplaced", "This cannot be drawn in the text as it is shown, but it can still be decided here.")));
    var act = R.el("div", "rv-actions");
    if (s.placed) act.appendChild(R.btn(T("show", "Show in the text"), function () { show(s.id); }));
    if (s.can_decide) {
      act.appendChild(R.btn(T("accept", "Accept"), function () { decideOne(s, "accept"); }, "solid small"));
      act.appendChild(R.btn(T("reject", "Reject"), function () { decideOne(s, "reject"); }));
    }
    if (s.can_withdraw) act.appendChild(R.btn(T("withdraw", "Withdraw"), function (e) {
      R.armed(li, { msg: T("withdraw_ask", "Withdraw this suggestion? It is taken away and cannot be brought back; you would make a new one."), ok: T("withdraw", "Withdraw"), cancel: T("cancel", "Cancel"), from: e.currentTarget, onOk: function () { decideOne(s, "withdraw"); } });
    }, "quiet small danger-text"));
    li.appendChild(act);
    return li;
  }
  function draw() {
    if (!panel) return;
    if (panel.querySelector(".arm")) return;
    var keep = doc.activeElement && panel.contains(doc.activeElement) ? doc.activeElement.id : ""; // a redraw (a live update) keeps focus where it was
    panel.textContent = "";
    if (plan) { panel.appendChild(plan); }
    if (!sugg.length) { panel.appendChild(R.el("p", "rv-empty fine", T("none", "No open suggestions."))); return; }
    function again() { var k = keep && doc.getElementById(keep); if (k) k.focus({ preventScroll: true }); }
    var groups = {};
    sugg.forEach(function (s) { (groups[s.document_id] = groups[s.document_id] || []).push(s); });
    R.sections().forEach(function (sec) {
      var g = groups[sec.dataset.doc];
      if (!g) return;
      var head = R.el("div", "rv-meta"), h = R.el("strong", null, titleOf(sec.dataset.doc) + " (" + g.length + ")");
      head.appendChild(h);
      if (R.decide) { var all = R.btn(T("accept_all", "Accept all…"), function () { previewAll(sec.dataset.doc, g); }, "quiet small"); all.dataset.all = sec.dataset.doc; head.appendChild(all); }
      panel.appendChild(head);
      var ul = R.el("ul", "rv-sugg-list");
      g.forEach(function (s) { ul.appendChild(card(s)); });
      panel.appendChild(ul);
    });
    again();
  }
  function show(id) {
    var m = R.$('[data-s="' + id + '"]', R.reader);
    if (!m) { R.say(T("s_not_on_page", "That suggestion is not drawn on this page.")); return; }
    R.$$(".is-current", R.reader).forEach(function (x) { x.classList.remove("is-current"); });
    R.$$('[data-s="' + id + '"]', R.reader).forEach(function (x) { x.classList.add("is-current"); });
    m.tabIndex = -1; m.scrollIntoView({ block: "center" }); m.focus({ preventScroll: true });
  }

  // ---- deciding --------------------------------------------------------------------------------------------------------
  function afterDecision(id) {
    var li = doc.getElementById("rv-s-" + id), nxt = li && (li.nextElementSibling || li.previousElementSibling);
    return nxt ? nxt.id : "";
  }
  function landFocus(id) {
    var n = (id && doc.getElementById(id)) || R.$(".rv-card", panel) || R.$('.rv-tabs .btn[data-tab="suggestions"]');
    if (n) n.focus({ preventScroll: false });
  }
  function decideOne(s, op) {
    var path = "/suggestions/" + s.id + "/" + op, nxt = afterDecision(s.id);
    R.api("POST", R.base + path, op === "accept" ? { base_revision: undefined } : undefined).then(function (r) {
      var li = doc.getElementById("rv-s-" + s.id);
      if (!r.ok) {
        var msg = R.why(r), e = r.json && r.json.error;
        if (e && e.details && e.details.reason === "stale" || r.status === 409) msg = msg + " " + T("stale_hint", "Reject it, or ask for a new one.");
        if (li) { var p = R.el("p", "field-error", msg); p.setAttribute("role", "alert"); li.insertBefore(p, li.querySelector(".rv-actions")); }
        R.say(msg); return;
      }
      R.say(op === "accept" ? T("accepted", "Accepted. The chapter was updated.") : op === "reject" ? T("rejected", "Rejected.") : T("withdrawn", "Withdrawn."));
      refreshChapter(s.document_id).then(function () { landFocus(nxt); });
    });
  }
  function previewAll(docId, group) {
    var ids = group.map(function (s) { return s.id; });
    R.api("POST", R.base + "/suggestions/accept_all", { document_id: docId, suggestion_ids: ids, preview: true }).then(function (r) {
      if (!r.ok || !r.json) { R.say(R.why(r)); return; }
      var o = r.json, box = R.el("div", "rv-compose"), n = o.applied.length;
      box.appendChild(R.el("p", null, T("plan", "{n} of {total} would be applied, in one step you can undo from the chapter's history.", { n: n, total: ids.length })));
      if (o.skipped && o.skipped.length) {
        box.appendChild(R.el("p", "rv-stale", T("skipped", "{n} would be left out:", { n: o.skipped.length })));
        var ul = R.el("ul", "rv-plan");
        o.skipped.forEach(function (k) { ul.appendChild(R.el("li", null, k.message || (REASON[k.reason] ? T("why_" + k.reason, REASON[k.reason]) : k.reason))); });
        box.appendChild(ul);
      }
      var row = R.el("div", "form-actions");
      var no = R.btn(T("cancel", "Cancel"), cancel), go = null;
      function cancel() { plan = null; draw(); var h = R.$('[data-all="' + docId + '"]'); if (h) h.focus(); }
      row.appendChild(no);
      if (n) {
        go = R.btn(T("accept_n", "Accept {n}", { n: n }), function () {
          if (go.disabled) return;
          R.api("POST", R.base + "/suggestions/accept_all", { document_id: docId, suggestion_ids: ids }).then(function (r2) {
            plan = null;
            if (!r2.ok) { R.say(R.why(r2)); draw(); return; }
            R.say(T("accepted_n", "{n} suggestions accepted. The chapter was updated.", { n: n })); refreshChapter(docId).then(function () { landFocus(""); });
          });
        }, "solid small");
        go.disabled = true; win.setTimeout(function () { go.disabled = false; }, 500); // a held Enter or a double tap cannot confirm it
        row.appendChild(go);
      }
      box.appendChild(row);
      box.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); cancel(); } });
      plan = box; draw();
      no.focus();
    });
  }
  var REASON = { stale: "Its words have changed since it was made.", overlap: "It overlaps one that comes before it.", invalid: "It can no longer be applied.", decided: "It was already decided." };

  // ---- making one -----------------------------------------------------------------------------------------------------
  function compose(cap) {
    var box = R.$("#rv-compose");
    box.textContent = ""; box.hidden = false;
    R.hold(cap.range); // the words stay lit while the box is open
    box.appendChild(R.el("p", "rv-quote", "“" + cap.quote + "”"));
    var f = R.el("form", "rv-form"), what = R.el("select"), wl = R.el("label", null, T("what", "What to suggest")), nl = R.el("label", null, T("new_text", "New words")), ta = R.el("textarea"), nt = R.el("input"), tl = R.el("label", null, T("why_note", "Why (optional)"));
    wl.htmlFor = what.id = "rv-sw"; nl.htmlFor = ta.id = "rv-st"; tl.htmlFor = nt.id = "rv-sn"; nt.type = "text"; nt.maxLength = 2000;
    [["replace", T("a_replace", "Replace these words")], ["delete", T("a_delete", "Delete these words")], ["before", T("a_before", "Insert before them")], ["after", T("a_after", "Insert after them")]].forEach(function (o) { var op = R.el("option", null, o[1]); op.value = o[0]; what.appendChild(op); });
    ta.rows = 3; ta.value = cap.quote;
    var err = R.el("p", "field-error"); err.hidden = true; err.setAttribute("role", "alert");
    var row = R.el("div", "form-actions"), go = R.el("button", "btn solid small", T("send", "Send suggestion")); go.type = "submit";
    row.appendChild(R.btn(T("cancel", "Cancel"), close)); row.appendChild(go);
    var hint = R.el("p", "fine", T("md_hint", "The new words are Markdown: *italics* and **bold** work as they do in the chapter."));
    [wl, what, nl, ta, hint, tl, nt, err, row].forEach(function (n) { f.appendChild(n); });
    what.addEventListener("change", function () { ta.hidden = nl.hidden = what.value === "delete"; what.focus(); if (what.value === "before" || what.value === "after") { if (ta.value === cap.quote) ta.value = ""; } else if (what.value === "replace" && !ta.value) ta.value = cap.quote; });
    function close() { box.hidden = true; box.textContent = ""; R.hold(null); R.popHide(); }
    f.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(); } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); f.requestSubmit(); } });
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      var kind = what.value, text = kind === "delete" ? "" : ta.value;
      if (kind !== "delete" && !text.trim()) { err.textContent = T("need_text", "Write the new words, or choose Delete."); err.hidden = false; ta.focus(); return; }
      if (kind === "replace" && text === cap.quote) { err.textContent = T("same", "That is the same as the words already there."); err.hidden = false; return; }
      go.disabled = true;
      R.api("POST", R.base + "/suggestions", { document_id: cap.doc, base_revision: cap.rev, plain: true, note: nt.value.trim() || undefined,
        changes: [{ quote: cap.quote, before: cap.before, after: cap.after, where: kind === "before" || kind === "after" ? kind : "replace", text: text }] }).then(function (r) {
        go.disabled = false;
        if (r.ok) {
          var made = r.json && r.json.item && r.json.item.id;
          close(); win.getSelection().removeAllRanges(); R.say(T("sent", "Suggestion sent."));
          load().then(function () { tab("suggestions"); var c = made && doc.getElementById("rv-s-" + made); (c || R.$('.rv-tabs .btn[data-tab="suggestions"]')).focus(); });
          return;
        }
        var e2 = r.json && r.json.error, d = e2 && e2.details;
        if (r.status === 409 && d && d.current_revision) { err.textContent = T("changed", "The chapter changed while you were reading. It has been reloaded: select the words again."); err.hidden = false; refreshChapter(cap.doc); return; }
        err.textContent = R.why(r); err.hidden = false;
      });
    });
    box.appendChild(f);
    R.openSheet(true);
    ta.focus(); ta.select();
  }

  S.suggest = {
    init: function (rv) {
      R = rv; panel = R.$("#rv-sugg");
      tabs = R.el("div", "rv-tabs");
      [["comments", T("comments", "Comments")], ["suggestions", T("suggestions", "Suggestions")]].forEach(function (t) {
        var b = R.btn(t[1], function () { tab(t[0]); }); b.dataset.tab = t[0]; b.setAttribute("aria-pressed", String(t[0] === "comments"));
        if (t[0] === "suggestions") { var n = R.el("span", "rv-count"); n.id = "rv-suggest-n"; b.appendChild(doc.createTextNode(" ")); b.appendChild(n); }
        tabs.appendChild(b);
      });
      // the tabs say which list this is, so the heading above them stays for the dialog's name but is not shown twice
      R.$(".rv-top", R.root).after(tabs); R.$("#rv-h").classList.add("sr-only");
      load();
      doc.addEventListener("scriobh:event", function (e) { var t = (e.detail && e.detail.type) || ""; if (/^suggestion\./.test(t) || t === "reset") win.setTimeout(load, 500); });
    },
    mode: function (on) { creating = on; S.suggestOn = on; R.popUpdate(); R.say(on ? T("mode_on", "Suggesting is on: select words, then press S or the button.") : T("mode_off", "Suggesting is off.")); },
    on: function (cap) { if (creating) compose(cap); },
    load: load
  };
})();
