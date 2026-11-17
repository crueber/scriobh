/* Scríobh writing view: the Versions tab of the inspector. Days with versions, the versions of a day,
   a read-only view of one, a word-level comparison with the current text or the previous version, naming
   a version, deleting a named one, and restoring (three clicks: Versions, a day, Restore).

   Restoring saves what is in the editor first, locks the editor for the length of the request, and keeps
   the text it replaces in history, so nothing typed is ever lost. History is append-only. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  if (!W) return;
  var el = function (tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  var api = W.api, id = W.cfg.id, canEdit = W.cfg.canEdit, base = "/api/v1/docs/" + id;
  var root = null, state = { day: "", sub: null, days: null, items: null, head: null }, busy = false, refreshTimer = 0;
  var offset = -new Date().getTimezoneOffset(), writable = canEdit && !W.cfg.readonly;
  function toast(m, undo) { if (S.toast) S.toast(m, undo ? { undo: undo } : undefined); }
  function fmt(n) { return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }
  function words(n) { return fmt(n) + (n === 1 ? " word" : " words"); }
  function time(iso) { return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); }
  function ymd(iso) { var d = new Date(iso), p = function (n) { return (n < 10 ? "0" : "") + n; }; return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate()); }
  function dayName(d) {
    var p = d.split("-"), dt = new Date(+p[0], +p[1] - 1, +p[2]), t = new Date(); t.setHours(0, 0, 0, 0);
    var n = Math.round((t - dt) / 864e5);
    return n === 0 ? "Today" : n === 1 ? "Yesterday" : dt.toLocaleDateString(undefined, { weekday: "long", day: "numeric", month: "long" });
  }
  var REASON = { autosave: "Autosaved", manual: "Named version", snapshot: "Snapshot", restore: "Restored", "import": "Imported", alternative: "Alternative", "ai-accepted": "Accepted suggestion" };
  function button(label, fn, cls, aria) {
    var b = el("button", cls || "btn quiet small", label);
    b.type = "button";
    if (aria) b.setAttribute("aria-label", aria);
    b.addEventListener("click", fn);
    return b;
  }
  function failure(r) {
    var e = (r && r.json && r.json.error) || {};
    return r && r.status === 409 ? "This chapter changed somewhere else. Reload the page, then try again." : e.message || "That did not work. Nothing was changed.";
  }

  // ---- the list ---------------------------------------------------------------------------------------------
  function load() {
    return api("GET", base + "/revisions/days?utc_offset=" + offset + "&limit=60").then(function (r) {
      if (!r.ok || !r.json) throw r;
      state.days = r.json.days || [];
      if (!state.day && state.days.length) state.day = state.days[0].day;
      return loadDay();
    });
  }
  function loadDay() {
    if (!state.day) { state.items = []; return api("GET", base + "/revisions?limit=1").then(function (r) { state.head = r.json && r.json.head; draw(); }); }
    var p = state.day.split("-"), from = new Date(+p[0], +p[1] - 1, +p[2]), to = new Date(+p[0], +p[1] - 1, +p[2] + 1);
    return api("GET", base + "/revisions?limit=100&from=" + encodeURIComponent(from.toISOString()) + "&to=" + encodeURIComponent(to.toISOString())).then(function (r) {
      if (!r.ok || !r.json) throw r;
      state.items = r.json.items || [];
      state.head = r.json.head;
      draw();
    });
  }
  function refresh() { if (state.sub) return; clearTimeout(refreshTimer); refreshTimer = setTimeout(function () { load().catch(function () {}); }, 600); }
  function problem(msg) {
    var p = el("p", "field-error", msg);
    p.setAttribute("role", "alert");
    root.replaceChildren(p, button("Try again", function () { show(root); }));
  }

  function draw() {
    if (state.sub) return;
    var kids = [], h = el("h2", "ver-h", "Versions");
    h.id = "ver-h"; h.tabIndex = -1;
    kids.push(h);
    if (canEdit) kids.push(nameBox());
    var days = el("ul", "ver-days");
    days.setAttribute("aria-label", "Days with versions");
    (state.days || []).forEach(function (d) {
      var li = el("li"), b = button(dayName(d.day) + ", " + d.count + (d.count === 1 ? " version" : " versions"), function () { state.day = d.day; loadDay().then(function () { var n = root.querySelector('[data-day="' + d.day + '"]'); if (n) n.focus(); }); }, "ver-day");
    b.dataset.day = d.day;
      b.setAttribute("aria-pressed", String(d.day === state.day));
      b.append(el("span", "ver-day-w", words(d.word_count)));
      li.append(b);
      days.append(li);
    });
    if (!(state.days || []).length) kids.push(el("p", "fine", "No versions yet. One is kept automatically as you write, at most every five minutes."));
    else kids.push(days);
    if (state.head && state.head.saved === false) kids.push(el("p", "fine", "What is on the page now is newer than the last version."));
    var list = el("ol", "ver-list");
    list.setAttribute("aria-label", state.day ? "Versions on " + dayName(state.day) : "Versions");
    (state.items || []).forEach(function (v) { list.append(row(v)); });
    if (state.day) kids.push(list);
    root.replaceChildren.apply(root, kids);
  }
  function row(v) {
    var li = el("li", "ver-item"), top = el("div", "ver-top"), t = time(v.created_at);
    top.append(el("span", "ver-time", t), el("span", "ver-reason", v.label || REASON[v.reason] || v.reason));
    if (v.current) top.append(el("span", "insp-tag", "Current"));
    if (v.pinned && !v.label) top.append(el("span", "insp-tag", "Kept"));
    var meta = el("p", "ver-meta", words(v.word_count) + (v.delta ? " (" + (v.delta > 0 ? "+" : "−") + fmt(Math.abs(v.delta)) + ")" : ""));
    li.append(top, meta);
    var acts = el("div", "ver-acts");
    if (canEdit && !v.current) acts.append(button("Restore", function () { restore(v); }, "btn small", "Restore the version from " + t));
    acts.append(button("View", function () { view(v, false); }, "btn quiet small", "View the version from " + t));
    if (!v.current) acts.append(button("Compare", function () { view(v, true); }, "btn quiet small", "Compare the version from " + t + " with the text now"));
    if (canEdit) acts.append(button(v.label ? "Rename" : "Name", function () { rename(v, li); }, "btn quiet small", (v.label ? "Rename" : "Name") + " the version from " + t));
    if (canEdit && v.reason === "manual") acts.append(del(v, t));
    li.append(acts);
    return li;
  }
  // Delete is armed by one press and done by a second after half a second and a key let go: a double-click or a held Enter deletes nothing.
  function del(v, t) {
    var armed = false, settling = false, released = false, fresh = false, t1 = 0, t2 = 0;
    function up(e) { if (e.key === "Enter" || e.key === " ") released = true; }
    function down(e) { if (released && !e.repeat && (e.key === "Enter" || e.key === " ")) fresh = true; }
    function disarm() { armed = settling = false; clearTimeout(t1); clearTimeout(t2); doc.removeEventListener("keyup", up, true); doc.removeEventListener("keydown", down, true); b.disabled = false; b.textContent = "Delete"; b.setAttribute("aria-label", "Delete the named version from " + t); }
    var b = button("Delete", function (e) {
      if (!armed) {
        armed = settling = true; released = e.detail !== 0; fresh = false; doc.addEventListener("keyup", up, true); doc.addEventListener("keydown", down, true);
        b.textContent = "Confirm delete"; b.setAttribute("aria-label", "Confirm deleting the named version from " + t); b.disabled = true;
        t1 = setTimeout(function () { settling = false; b.disabled = false; var a = doc.activeElement; if (!a || a === doc.body || a === b || a.contains(b)) b.focus(); else disarm(); }, 500);
        t2 = setTimeout(disarm, 4000);
        return;
      }
      if (settling || !released || (e.detail === 0 && !fresh)) return;
      disarm();
      api("DELETE", base + "/revision?revision_id=" + encodeURIComponent(v.id)).then(function (r) { if (r.ok) { toast("Deleted the named version."); load(); } else toast(failure(r)); });
    }, "btn quiet small", "Delete the named version from " + t);
    b.addEventListener("blur", function () { if (armed && !settling) disarm(); });
    return b;
  }

  // ---- naming ------------------------------------------------------------------------------------------------
  function nameBox() {
    var f = el("form", "ver-name"), i = el("input"), l = el("label", "sr-only", "Name for this version"), b = el("button", "btn small", "Keep a named version");
    i.type = "text"; i.maxLength = 100; i.placeholder = "A name, for example Draft 2"; i.autocomplete = "off"; i.id = "ver-name-in"; l.htmlFor = i.id;
    b.type = "submit";
    f.append(l, i, b);
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      if (busy) return;
      busy = true;
      var stop = function () { busy = false; toast("Could not reach the server. No version was kept."); };
      W.flushAll().then(function (ok) {
        if (!ok) { busy = false; return toast("Your text is not saved yet, so no version was kept."); }
        return api("POST", base + "/revisions", { label: i.value.trim() }).then(function (r) {
          busy = false;
          if (r.ok) { i.value = ""; toast("Version kept. It stays until you delete it."); state.day = ""; load().then(function () { var n = root.querySelector("#ver-name-in"); if (n) n.focus(); }); } else toast(failure(r));
        }, stop);
      }, stop);
    });
    return f;
  }
  function rename(v, li) {
    var f = el("form", "ver-name"), i = el("input"), l = el("label", "sr-only", "Name"), ok = el("button", "btn small", "Save name"), no = button("Cancel", function () { draw(); });
    i.type = "text"; i.maxLength = 100; i.value = v.label || ""; i.id = "ver-rn"; l.htmlFor = i.id;
    ok.type = "submit";
    f.append(l, i, ok, no);
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      api("PATCH", base + "/revision", { revision_id: v.id, label: i.value.trim() }).then(function (r) { if (r.ok) load(); else toast(failure(r)); });
    });
    var old = li.querySelector(".ver-acts");
    li.replaceChild(f, old);
    i.focus();
    i.select();
  }

  // ---- view and compare ---------------------------------------------------------------------------------------
  function page(title, v) {
    state.sub = { v: v };
    var h = el("h2", "ver-h", title);
    h.tabIndex = -1;
    var back = button("‹ All versions", function () { state.sub = null; load().then(function () { var n = root.querySelector("#ver-h"); if (n) n.focus(); }); }, "btn quiet small");
    return { h: h, back: back };
  }
  function view(v, compare) {
    var t = time(v.created_at), p = page((compare ? "Changes since " : "Version from ") + dayName(ymd(v.created_at)).toLowerCase() + " " + t, v);
    var body = el("div", "ver-body");
    body.setAttribute("aria-busy", "true");
    root.replaceChildren(p.back, p.h, body);
    p.h.focus();
    if (compare) {
      var unit = "word";
      var go = function () {
        api("GET", base + "/revisions/diff?from=" + encodeURIComponent(v.id) + "&to=current&unit=" + unit).then(function (r) {
          body.setAttribute("aria-busy", "false");
          if (!r.ok || !r.json) { body.textContent = failure(r); return; }
          body.replaceChildren(diffView(r.json, function () { unit = unit === "word" ? "paragraph" : "word"; go(); }, unit), actions(v));
        });
      };
      go();
    } else {
      api("GET", base + "/revision?revision_id=" + encodeURIComponent(v.id)).then(function (r) {
        body.setAttribute("aria-busy", "false");
        if (!r.ok || !r.json) { body.textContent = failure(r); return; }
        var pre = el("pre", "ver-text", r.json.content);
        pre.tabIndex = 0;
        pre.setAttribute("aria-label", "Text of this version, read only");
        body.replaceChildren(el("p", "fine", words(r.json.item.word_count) + ". Read only."), pre, actions(v));
      });
    }
  }
  function actions(v) {
    var d = el("div", "ver-acts");
    if (canEdit && !v.current) d.append(button("Restore this version", function () { restore(v); }, "btn"));
    return d;
  }
  function diffView(d, toggle, unit) {
    var wrap = el("div"), s = d.stats || {};
    wrap.append(el("p", "fine", d.changed ? fmt(s.added_words || 0) + " added, " + fmt(s.removed_words || 0) + " removed. Added text is underlined; removed text is struck through." : "No difference from the text now."));
    if (d.coarse) wrap.append(el("p", "fine", "This comparison is approximate: a long passage changed too much to compare word by word, so it is shown as one removal and one addition."));
    var pre = el("pre", "ver-text ver-diff");
    pre.tabIndex = 0;
    pre.setAttribute("aria-label", "Changes, read only");
    (d.segments || []).forEach(function (g) {
      if (g.op === "equal") pre.append(doc.createTextNode(g.text));
      else {
        var n = el(g.op === "insert" ? "ins" : "del", "", g.text);
        n.prepend(el("span", "sr-only", g.op === "insert" ? "[added: " : "[removed: "));
        n.append(el("span", "sr-only", "]"));
        pre.append(n);
      }
    });
    wrap.append(pre, button(unit === "word" ? "Compare by paragraph" : "Compare by word", toggle, "btn quiet small"));
    return wrap;
  }

  // ---- restore --------------------------------------------------------------------------------------------------
  function restore(v) {
    if (busy) return;
    busy = true;
    var ed = W.editor, bar = doc.getElementById("save-state");
    function done() { busy = false; ed.setReadOnly(!writable); }
    W.flushAll().then(function (ok) {
      if (!ok) { busy = false; return toast("Your text is not saved yet, so nothing was restored. It is safe in the editor."); }
      // Only a chapter this tab may write, with nothing waiting or in conflict, is replaced.
      if (bar && bar.dataset.state !== "saved") { busy = false; return toast("This chapter is locked, offline or in conflict, so nothing was restored. Sort that out first."); }
      ed.setReadOnly(true); // nothing is typed into the page while the old text is on its way
      return api("POST", base + "/revisions/restore", { revision_id: v.id, base_revision: W.rev, tab_id: W.tab }).then(function (r) {
        done();
        if (!r.ok || !r.json) return toast(failure(r));
        if (r.json.unchanged) { toast("That is already the text on the page."); return; }
        W.adopt(r.json.item);
        state.sub = null;
        toast("Restored the version from " + dayName(ymd(v.created_at)).toLowerCase() + " " + time(v.created_at) + ". The text it replaced is kept in the history.", undo);
        state.day = ""; load().then(function () { var n = root && root.querySelector("#ver-h"); if (n) n.focus(); });
        ed.focus();
      }, function () { done(); toast("Could not reach the server. Nothing was changed."); });
    }, function () { done(); toast("Could not reach the server. Nothing was changed."); });
  }
  // Undo of a restore: the text that stood before it is the version just below the one the restore wrote.
  function undo() {
    api("GET", base + "/revisions?limit=5").then(function (r) {
      var items = (r.json && r.json.items) || [], i;
      for (i = 0; i < items.length; i++) if (items[i].reason === "restore") break;
      var prev = items[i + 1];
      if (!prev) return toast("The earlier text is in the Versions tab.");
      restore(prev);
    });
  }

  S.versions = {
    show: function (panel) { root = panel; state.sub = null; load().catch(function (r) { problem(r && r.status === 404 ? "This chapter is not here any more." : "Versions could not be loaded."); }); },
    refresh: refresh
  };
  doc.addEventListener("scriobh:saved", function () { if (root && !root.hidden && S.inspector && S.inspector.current === "versions") refresh(); });
  var panel = doc.getElementById("ipanel-versions");
  if (panel && S.inspector && S.inspector.current === "versions") S.versions.show(panel);
})();
