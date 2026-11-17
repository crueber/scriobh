/* Scríobh corkboard and status board: moving cards, editing a synopsis, changing a status. Vanilla, loaded on
   /p/{slug}/cards and /p/{slug}/board for people who can edit; both pages read fine without it.

   Every change goes through the REST ops (doc.move, doc.update) one at a time, in the order they were asked
   for, and carries the revision the card was drawn with. After a change the server's own list is fetched and
   drawn again; an answer that is not the newest is dropped. A change made elsewhere (a live event) redraws
   the page too, but never while something is being edited or moved here: it waits, and says so when it lands. */
(function () {
  "use strict";
  var doc = document, win = window;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  var root = $("#cards") || $("#board");
  if (!root || root.dataset.canEdit !== "true") return;
  var cardsView = root.dataset.view === "cards", project = root.dataset.project, body = $("#b-body"), live = $("#b-live");
  var STATUS = (root.dataset.statuses || "idea:Idea,outline:Outline,draft:Draft,revise:Revise,done:Done").split(",").map(function (p) { return p.split(":"); });
  var sprite = (($("svg.icon use") || { getAttribute: function () { return ""; } }).getAttribute("href") || "").split("#")[0];
  var active = null, chain = Promise.resolve(), asked = 0, pending = false, others = false, moving = 0;

  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function say(m) { live.textContent = ""; win.setTimeout(function () { live.textContent = m; }, 40); }
  function fail(m) { say(m); if (win.scriobh && win.scriobh.toast) win.scriobh.toast(m); }
  function num(n) { return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }
  function api(method, path, data) {
    var o = { method: method, credentials: "same-origin", headers: { Accept: "application/json" } };
    if (data !== undefined) { o.headers["Content-Type"] = "application/json"; o.body = JSON.stringify(data); }
    return fetch(path, o).then(function (r) {
      return r.text().then(function (t) {
        var j = null;
        try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ }
        return { status: r.status, ok: r.ok, json: j, err: (j && j.error) || {} };
      });
    });
  }
  // Changes run one at a time, in order, so two quick presses can never interleave.
  function enqueue(fn) { moving++; chain = chain.then(fn, fn).then(function () { moving--; }, function () { moving--; }); return chain; }

  var cards = function () { return $$(cardsView ? ".ccard" : ".bcard", body); };
  var byId = function (id) { return id ? $("#c-" + id, body) : null; };
  var title = function (c) { return c.dataset.title; };
  var statusName = function (v) { return (STATUS.filter(function (s) { return s[0] === v; })[0] || ["", v])[1]; };

  // ---- redrawing -------------------------------------------------------------------------------------------
  function busy() {
    return moving > 0 || !!$(".cedit, .cmove, .cconf", body) || !!(doc.activeElement && doc.activeElement.closest && doc.activeElement.closest(".cedit, .cmove, .cconf"));
  }
  function swap(text) {
    var a = active && active.dataset.id, focused = doc.activeElement, inCard = focused && body.contains(focused) && focused !== body, which = inCard && focused.dataset && focused.dataset.act;
    var keep = inCard;
    body.innerHTML = text;
    active = null;
    laneTools();
    var c = byId(a);
    cards().forEach(function (x) { rove(x, false); });
    if (c) { activate(c); if (keep) { var t = which && $('[data-act="' + which + '"]', c); (t && !t.disabled ? t : c).focus(); } }
    else if (cards()[0]) rove(cards()[0], true);
  }
  function refresh() {
    var mine = ++asked, q = cardsView ? location.search : "";
    return fetch("/ui/projects/" + project + "/" + (cardsView ? "cards" : "board") + q, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) {
      if (!r.ok) throw r;
      return r.text();
    }).then(function (t) { if (mine === asked) swap(t); }, function () { fail("Could not refresh. Reload the page to see the latest."); });
  }
  function settle(id, act) {
    var c = byId(id);
    if (!c) return;
    activate(c);
    var t = act && $('[data-act="' + act + '"]', c);
    (t && !t.disabled ? t : c).focus();
  }

  // ---- one tab stop, and the tools of the card you are on ------------------------------------------------------
  function rove(c, on) {
    c.tabIndex = on ? 0 : -1;
    var a = $(".ccard-title", c);
    if (a) a.tabIndex = on ? 0 : -1;
  }
  function icon(name) {
    var ns = "http://www.w3.org/2000/svg", s = doc.createElementNS(ns, "svg"), u = doc.createElementNS(ns, "use");
    s.setAttribute("class", "icon"); s.setAttribute("aria-hidden", "true"); s.setAttribute("focusable", "false");
    u.setAttribute("href", sprite + "#" + name); s.appendChild(u);
    return s;
  }
  function tool(c, act, label, text, name) {
    var b = el("button", "btn small", ""), t = el("span", "", text);
    b.type = "button"; b.dataset.act = act;
    b.setAttribute("aria-label", label + " " + title(c));
    if (name) b.appendChild(icon(name));
    b.appendChild(t);
    return b;
  }
  function activate(c) {
    if (active === c) return;
    if (active) { active.classList.remove("is-active"); var o = $(".ctools", active); if (o) o.remove(); rove(active, false); }
    active = c;
    c.classList.add("is-active");
    rove(c, true);
    var t = el("div", "ctools"), filtered = cardsView && root.dataset.filter;
    t.setAttribute("role", "group"); t.setAttribute("aria-label", "Actions for " + title(c));
    if (cardsView) {
      t.appendChild(tool(c, "edit", "Edit the synopsis of", "Edit synopsis"));
      var e = tool(c, "earlier", "Move earlier:", "Earlier", "arrow-up"), l = tool(c, "later", "Move later:", "Later", "arrow-down");
      e.disabled = !c.dataset.prev || !!filtered; l.disabled = !c.dataset.next || !!filtered;
      t.appendChild(e); t.appendChild(l);
      t.appendChild(tool(c, "to", "Move to another part:", "Move to…"));
    } else {
      var i = STATUS.map(function (s) { return s[0]; }).indexOf(c.dataset.status), p = STATUS[i - 1], n = STATUS[i + 1];
      var b1 = tool(c, "col-", "Move to " + (p ? p[1] : "an earlier status") + ":", p ? "← " + p[1] : "←", ""), b2 = tool(c, "col+", "Move to " + (n ? n[1] : "a later status") + ":", n ? n[1] + " →" : "→", "");
      b1.disabled = !p; b2.disabled = !n;
      t.appendChild(b1); t.appendChild(b2);
    }
    c.appendChild(t);
  }

  // ---- reordering (the corkboard) --------------------------------------------------------------------------------
  // What a move does is worked out when its turn comes, from the card as the server last drew it, so two quick
  // presses are two moves, not the same move twice.
  function post(id, parent, after, then) {
    var b = { parent_id: parent };
    if (after !== undefined) b.after = after;
    return api("POST", "/api/v1/docs/" + id + "/move", b).then(function (r) {
      if (!r.ok) { fail(r.err.message || "Could not move that."); return; }
      return refresh().then(then);
    }, function () { fail("Could not reach the server. Nothing was moved."); });
  }
  function nudge(id, dir) {
    return enqueue(function () {
      var c = byId(id);
      if (!c) return;
      var t = title(c);
      if (dir < 0 ? !c.dataset.prev : !c.dataset.next) { say(dir < 0 ? "Already first." : "Already last."); return; }
      return post(id, c.dataset.parent, dir < 0 ? c.dataset.prev2 || "" : c.dataset.next, function () { settle(id, null); say("Moved " + t + (dir < 0 ? " earlier." : " later.")); });
    });
  }
  function earlier(c) { return nudge(c.dataset.id, -1); }
  function later(c) { return nudge(c.dataset.id, 1); }
  function moveTo(c, to, name) {
    var id = c.dataset.id, t = title(c);
    return enqueue(function () { return post(id, to, undefined, function () { settle(id, null); say("Moved " + t + " to " + name + "."); }); });
  }
  function openMove(c) {
    if ($(".cedit, .cmove", body)) { fail("Finish or cancel what is open first."); return; }
    var targets = [];
    try { targets = JSON.parse(root.dataset.targets || "[]"); } catch (e) { /* none */ }
    var kind = c.dataset.kind, f = el("form", "cmove"), s = el("select"), lab = el("label", "", "Move " + title(c) + " to");
    s.id = "mv-" + c.dataset.id; lab.htmlFor = s.id;
    if (kind !== "scene") { var top = el("option", "", "The top level, outside the parts"); top.value = ""; s.appendChild(top); }
    targets.forEach(function (t) {
      if (t.id === c.dataset.id || t.kind !== (kind === "scene" ? "chapter" : "part")) return;
      var o = el("option", "", t.label); o.value = t.id; s.appendChild(o);
    });
    if ($('option[value="' + c.dataset.parent + '"]', s)) s.value = c.dataset.parent;
    var row = el("div", "row"), go = el("button", "btn solid small", "Move"), no = el("button", "btn small", "Cancel");
    go.type = "submit"; no.type = "button";
    row.appendChild(go); row.appendChild(no);
    f.appendChild(lab); f.appendChild(s); f.appendChild(row);
    c.insertBefore(f, $(".ctools", c));
    s.focus();
    function close() { f.remove(); var b = $('[data-act="to"]', c); if (b) b.focus(); }
    no.addEventListener("click", close);
    f.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.stopPropagation(); close(); } });
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      var to = s.value, name = s.options[s.selectedIndex].text;
      f.remove();
      moveTo(c, to, name);
    });
  }

  // A part moves as a whole, with its chapters and scenes: its own pair of buttons under the lane heading.
  function laneTools() {
    if (!cardsView || root.dataset.filter) return;
    $$(".lane[data-part]", body).forEach(function (l) {
      var t = el("div", "lane-tools"), h = $(".lane-head", l), name = l.dataset.title;
      t.setAttribute("role", "group"); t.setAttribute("aria-label", "Actions for " + name);
      [["learlier", "Move part earlier:", "Earlier", "arrow-up", !l.dataset.prev], ["llater", "Move part later:", "Later", "arrow-down", !l.dataset.next]].forEach(function (d) {
        var b = el("button", "btn small", ""); b.type = "button"; b.dataset.act = d[0]; b.disabled = d[4];
        b.setAttribute("aria-label", d[1] + " " + name); b.appendChild(icon(d[3])); b.appendChild(el("span", "", d[2] + " (part)"));
        t.appendChild(b);
      });
      h.parentNode.insertBefore(t, h.nextSibling);
    });
  }
  function movePart(id, dir) {
    return enqueue(function () {
      var l = $('.lane[data-part="' + id + '"]', body);
      if (!l) return;
      var name = l.dataset.title;
      if (dir < 0 ? !l.dataset.prev : !l.dataset.next) { say(dir < 0 ? "Already first." : "Already last."); return; }
      return post(id, "", dir < 0 ? l.dataset.prev2 || "" : l.dataset.next, function () {
        var n = $('.lane[data-part="' + id + '"]', body), x = n && $('[data-act="' + (dir < 0 ? "learlier" : "llater") + '"]', n);
        if (x && x.disabled) x = $('[data-act="' + (dir < 0 ? "llater" : "learlier") + '"]', n);
        if (x) x.focus();
        say("Moved " + name + " " + (dir < 0 ? "earlier" : "later") + ", with everything in it.");
      });
    });
  }

  // ---- editing a synopsis (the corkboard) -------------------------------------------------------------------------
  function openEdit(c) {
    if ($(".cedit, .cmove", body)) { fail("Finish or cancel what is open first."); return; }
    var f = el("form", "cedit"), t = el("textarea"), lab = el("label", "", "Synopsis of " + title(c)), err = el("p", "field-error");
    t.id = "sy-" + c.dataset.id; lab.htmlFor = t.id; t.maxLength = 300; t.rows = 3; t.value = c.dataset.syn;
    err.setAttribute("role", "alert"); err.hidden = true;
    var row = el("div", "row"), ok = el("button", "btn solid small", "Save"), no = el("button", "btn small", "Cancel");
    ok.type = "submit"; no.type = "button";
    row.appendChild(ok); row.appendChild(no);
    f.appendChild(lab); f.appendChild(t); f.appendChild(err); f.appendChild(row);
    f._rev = parseInt(c.dataset.rev, 10);
    c.insertBefore(f, $(".ctools", c));
    t.focus();
    function close(back) { f.remove(); if (back) { var b = $('[data-act="edit"]', c); if (b) b.focus(); } flushLater(); }
    no.addEventListener("click", function () { close(true); });
    f.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { e.stopPropagation(); close(true); }
      else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); save(c, f, t, err); }
    });
    f.addEventListener("submit", function (e) { e.preventDefault(); save(c, f, t, err); });
  }
  function save(c, f, t, err) {
    var id = c.dataset.id;
    err.hidden = true;
    if (t.value === c.dataset.syn) { f.remove(); say("Nothing changed."); flushLater(); return; }
    enqueue(function () {
      return api("PATCH", "/api/v1/docs/" + id, { synopsis_short: t.value, base_revision: f._rev }).then(function (r) {
        if (r.ok) { f.remove(); return refresh().then(function () { settle(id, "edit"); say("Saved the synopsis of " + title(byId(id) || c) + "."); }); }
        if (r.status === 409) return conflict(c, f, t, err);
        err.textContent = r.err.message || "Could not save. Your text is still here."; err.hidden = false; say(err.textContent);
      }, function () { err.textContent = "Could not reach the server. Your text is still here."; err.hidden = false; });
    });
  }
  // The card changed elsewhere since it was drawn: show theirs, keep mine, and let the person choose.
  function conflict(c, f, t, err) {
    return api("GET", "/api/v1/docs/" + c.dataset.id).then(function (r) {
      var cur = r.ok && r.json && r.json.item;
      if (!cur || cur.trashed) { err.textContent = "This document is no longer here. Reload the page."; err.hidden = false; return; }
      var old = $(".cconf", f), box = el("div", "cconf"), p = el("p", "", "This changed somewhere else since you opened it. Theirs: " + (cur.synopsis_short || "no synopsis") + ". Yours is still in the box."), row = el("div", "row");
      if (old) old.remove();
      box.setAttribute("role", "alert");
      var keep = el("button", "btn solid small", "Keep mine"), use = el("button", "btn small", "Use theirs");
      keep.type = use.type = "button";
      keep.addEventListener("click", function () { f._rev = cur.revision; box.remove(); save(c, f, t, err); });
      use.addEventListener("click", function () { f.remove(); refresh().then(function () { settle(c.dataset.id, "edit"); say("Showing the version saved elsewhere."); flushLater(); }); });
      row.appendChild(keep); row.appendChild(use); box.appendChild(p); box.appendChild(row);
      f.insertBefore(box, err);
      keep.focus();
    }, function () { err.textContent = "Could not reach the server. Your text is still here."; err.hidden = false; });
  }

  // ---- changing a status (the board) ------------------------------------------------------------------------------
  // One column to the left or right of where the card is when its turn comes, with the revision it was last drawn at.
  function stepStatus(id, dir) {
    return enqueue(function () {
      var c = byId(id);
      if (!c) return;
      var k = STATUS.map(function (s) { return s[0]; }).indexOf(c.dataset.status), n = STATUS[k + dir];
      if (!n) { say("Already in the " + (dir > 0 ? "last" : "first") + " status."); return; }
      return attempt(id, c.dataset.status, n[0], parseInt(c.dataset.rev, 10), title(c), false);
    });
  }
  function attempt(id, was, to, rev, t, retried) {
    return api("PATCH", "/api/v1/docs/" + id, { status: to, base_revision: rev }).then(function (r) {
      if (r.ok) return refresh().then(function () { settle(id, null); say("Moved " + t + " to " + statusName(to) + "."); });
      if (r.status !== 409) { fail(r.err.message || "Could not change the status."); return; }
      return api("GET", "/api/v1/docs/" + id).then(function (g) {
        var cur = g.ok && g.json && g.json.item;
        if (!cur || cur.trashed) { fail("That document is no longer here."); return refresh(); }
        // Only other details changed: the same status change on the new revision is safe, once.
        if (cur.status === was && !retried) return attempt(id, was, to, cur.revision, t, true);
        return conflictBar(id, t, to, cur);
      });
    }, function () { fail("Could not reach the server. Nothing was changed."); });
  }
  function conflictBar(id, t, to, cur) {
    return refresh().then(function () {
      var c = byId(id);
      if (!c) return;
      activate(c);
      var box = el("div", "cconf"), p = el("p", "", t + " was changed elsewhere and is now " + statusName(cur.status) + ". You wanted " + statusName(to) + "."), row = el("div", "row");
      box.setAttribute("role", "alert");
      var keep = el("button", "btn solid small", "Move to " + statusName(to) + " anyway"), use = el("button", "btn small", "Leave it in " + statusName(cur.status));
      keep.type = use.type = "button";
      keep.addEventListener("click", function () { box.remove(); enqueue(function () { return attempt(id, cur.status, to, cur.revision, t, true); }); });
      use.addEventListener("click", function () { box.remove(); say("Left " + t + " in " + statusName(cur.status) + "."); settle(id, null); });
      row.appendChild(keep); row.appendChild(use); box.appendChild(p); box.appendChild(row);
      c.appendChild(box);
      keep.focus();
      say(p.textContent);
    });
  }

  // ---- events -----------------------------------------------------------------------------------------------------
  doc.addEventListener("click", function (e) {
    var lb = e.target.closest && e.target.closest(".lane-tools [data-act]");
    if (lb && body.contains(lb)) { movePart(lb.closest(".lane").dataset.part, lb.dataset.act === "learlier" ? -1 : 1); return; }
    var b = e.target.closest && e.target.closest("[data-act]"), c = e.target.closest && e.target.closest(cardsView ? ".ccard" : ".bcard");
    if (!c || !body.contains(c)) return;
    if (b) {
      var a = b.dataset.act;
      if (a === "edit") openEdit(c);
      else if (a === "earlier") earlier(c);
      else if (a === "later") later(c);
      else if (a === "to") openMove(c);
      else if (a === "col-" || a === "col+") stepStatus(c.dataset.id, a === "col+" ? 1 : -1);
      return;
    }
    if (!e.target.closest("a, button, input, select, textarea, form")) activate(c);
  });
  doc.addEventListener("focusin", function (e) { var c = e.target.closest && e.target.closest(cardsView ? ".ccard" : ".bcard"); if (c && body.contains(c)) activate(c); });
  doc.addEventListener("keydown", function (e) {
    var c = e.target.closest && e.target.closest(cardsView ? ".ccard" : ".bcard");
    if (!c || !body.contains(c) || e.target.closest("form, select, textarea, input")) return;
    var all = cards(), i = all.indexOf(c);
    if (e.altKey && !e.ctrlKey && !e.metaKey && cardsView) {
      if (e.key === "ArrowUp" || e.key === "ArrowLeft") { e.preventDefault(); if (!root.dataset.filter) earlier(c); }
      else if (e.key === "ArrowDown" || e.key === "ArrowRight") { e.preventDefault(); if (!root.dataset.filter) later(c); }
      return;
    }
    if (e.target !== c || e.ctrlKey || e.metaKey) return;
    if (!cardsView && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
      e.preventDefault();
      stepStatus(c.dataset.id, e.key === "ArrowRight" ? 1 : -1);
      return;
    }
    var m = e.key === "ArrowDown" ? i + 1 : e.key === "ArrowUp" ? i - 1 : e.key === "Home" ? 0 : e.key === "End" ? all.length - 1 : -1;
    if (m >= 0 && m < all.length) { e.preventDefault(); activate(all[m]); all[m].focus(); all[m].scrollIntoView({ block: "nearest" }); }
    else if (e.key === "Enter" && cardsView) { e.preventDefault(); openEdit(c); }
  });

  // ---- changes made elsewhere ---------------------------------------------------------------------------------------
  var timer = 0;
  function flushLater() { if (pending) schedule(); }
  function schedule() { win.clearTimeout(timer); timer = win.setTimeout(attemptLive, busy() ? 1500 : 400); }
  function attemptLive() {
    if (busy()) { pending = true; schedule(); return; }
    var said = others;
    pending = others = false;
    // Our own changes redraw the page quietly: only a change from somewhere else is announced.
    refresh().then(function () { if (said) say("Updated: this page changed somewhere else."); });
  }
  doc.addEventListener("scriobh:event", function (e) {
    var t = (e.detail && e.detail.type) || "";
    if (t !== "reset" && t !== "revision.restored" && t.indexOf("doc.") !== 0) return;
    pending = true;
    if (!(e.detail && e.detail.by_you)) others = true;
    schedule();
  });

  // ---- start -------------------------------------------------------------------------------------------------------
  laneTools();
  var first = cards();
  first.forEach(function (c) { rove(c, false); });
  if (first.length) rove(first[0], true);
  win.__boardsReady = true;
})();
