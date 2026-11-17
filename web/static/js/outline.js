/* Scríobh outline: inline editing, reordering and adding on /p/{slug}/outline. Vanilla, no dependencies,
   loaded on that page only and only for people who can edit. The page reads fine without it.

   Every change goes through the same ops as the REST API (doc.update, doc.move, doc.create). After a
   change the server's own list is fetched again and merged into the page, row by row: the open editor
   keeps its text, focus goes back to where the person was. Nothing is saved on a guess: an edit sends
   the revision the row was drawn with, and a 409 shows both versions and asks. */
(function () {
  "use strict";
  var doc = document, win = window;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  var root = $("#outline");
  if (!root || root.dataset.canEdit !== "true") return;
  var project = root.dataset.project, live = $("#o-live"), active = null, drag = null;
  var STATUS = [["idea", "Idea"], ["outline", "Outline"], ["draft", "Draft"], ["revise", "Revise"], ["done", "Done"]];
  var sprite = (($("svg.icon use") || {}).getAttribute ? $("svg.icon use").getAttribute("href") : "").split("#")[0];

  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function icon(name) {
    var ns = "http://www.w3.org/2000/svg", s = doc.createElementNS(ns, "svg"), u = doc.createElementNS(ns, "use");
    s.setAttribute("class", "icon"); s.setAttribute("aria-hidden", "true"); s.setAttribute("focusable", "false");
    u.setAttribute("href", sprite + "#" + name); s.appendChild(u);
    return s;
  }
  function say(m) { live.textContent = ""; setTimeout(function () { live.textContent = m; }, 40); }
  function fail(m) { say(m); if (win.scriobh && win.scriobh.toast) win.scriobh.toast(m); }
  function num(n) { return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }

  function api(method, path, body) {
    var o = { method: method, credentials: "same-origin", headers: { Accept: "application/json" } };
    if (body !== undefined) { o.headers["Content-Type"] = "application/json"; o.body = JSON.stringify(body); }
    return fetch(path, o).then(function (r) {
      return r.text().then(function (t) {
        var j = null;
        try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ }
        return { status: r.status, ok: r.ok, json: j, err: (j && j.error) || {} };
      });
    });
  }
  var list = function () { return $("#o-list"); };
  // The timeline and point-of-view orders are flat lists of scenes: reading them, editing a synopsis, status or target
  // works as ever, but there is nothing to move there, so the move tools are not drawn.
  var sorted = function () { var l = list(); return !!(l && l.dataset.sort && l.dataset.sort !== "order"); };
  var rows = function () { return $$(".orow", list()); };
  var byId = function (id) { return id ? $("#o-" + id) : null; };
  function sibs(li) { var p = li.dataset.parent; return rows().filter(function (r) { return r.dataset.parent === p; }); }
  function subtree(li) {
    var set = {}, all = rows(), i = all.indexOf(li);
    set[li.dataset.id] = 1;
    for (i++; i < all.length && set[all[i].dataset.parent]; i++) set[all[i].dataset.id] = 1;
    return set;
  }
  var ttl = function (li) { return li.dataset.title; };

  // ---- merging the server's list into the page -------------------------------------------------------
  function merge(text) {
    var t = doc.createElement("template");
    t.innerHTML = text;
    var ol = list(), nl = t.content.querySelector("#o-list");
    if (!ol || !nl) { $("#o-body").innerHTML = text; return; }
    if ((ol.dataset.sort || "order") !== (nl.dataset.sort || "order")) return; // the order was changed while this was on its way
    if (sorted()) { // groups and a flat order are redrawn whole; an open editor keeps what is being typed, so wait
      if ($(".orow-edit", ol)) { win.setTimeout(refresh, 2500); return; }
      active = null;
      var keepCols = ol.dataset.cols;
      $("#o-body").innerHTML = text;
      if (list() && keepCols) list().dataset.cols = keepCols;
      return;
    }
    var sum = $("#o-summary"), nsum = t.content.querySelector("#o-summary");
    if (sum && nsum) sum.replaceWith(nsum);
    var old = {}, used = [];
    $$(".orow", ol).forEach(function (r) { old[r.dataset.id] = r; });
    $$(".orow", nl).forEach(function (n) {
      var o = old[n.dataset.id], use = n;
      if (o && $(".orow-edit", o)) { // an open editor keeps its text; only its place in the list changes
        o.dataset.parent = n.dataset.parent; o.dataset.kind = n.dataset.kind;
        o.className = n.className + " is-editing" + (o === active ? " is-active" : "");
        $(".orow-kind", o).textContent = $(".orow-kind", n).textContent;
        use = o;
      } else if (o === active) { n.classList.add("is-active"); }
      used.push(use);
      ol.appendChild(use);
    });
    Object.keys(old).forEach(function (id) { if (used.indexOf(old[id]) < 0) old[id].remove(); });
    if (active && !active.isConnected) active = byId(active.dataset.id);
    rows().forEach(function (r) { rove(r, r === active); });
    if (active && !$(".orow-tools", active)) tools(active);
  }
  // Only the newest answer is drawn: a slow older one must not put the list back the way it was.
  var asked = 0;
  function refresh() {
    var mine = ++asked;
    return fetch("/ui/projects/" + project + "/outline" + (sorted() ? "?sort=" + list().dataset.sort : ""), { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) {
      if (!r.ok) throw r;
      return r.text();
    }).then(function (t) { if (mine === asked) merge(t); }, function () { if (mine === asked) fail("Could not refresh the outline. Reload the page to see the latest."); });
  }
  // Live updates: a change made elsewhere is drawn a moment after the last one. An open editor keeps its text (merge
  // handles that), and nothing is redrawn while a row is being dragged.
  var liveTimer = 0;
  doc.addEventListener("scriobh:event", function (e) {
    var t = (e.detail && e.detail.type) || "";
    if (t !== "reset" && t.indexOf("doc.") !== 0 && t !== "revision.restored") return;
    win.clearTimeout(liveTimer);
    liveTimer = win.setTimeout(function again() { if ($(".is-dragging", list() || doc)) { liveTimer = win.setTimeout(again, 1000); return; } refresh(); }, 700);
  });
  doc.addEventListener("scriobh:outline-swapped", function () { active = null; });
  function settleOn(id, sel) {
    var li = byId(id);
    if (!li) return;
    activate(li);
    var f = sel ? $(sel, li) : li;
    if (f) f.focus();
  }

  // ---- the active row: one tab stop for the list, and the tools of the row you are on ----------------------
  function rove(li, on) {
    li.tabIndex = on ? 0 : -1;
    var a = $(".orow-title", li);
    if (a) a.tabIndex = on ? 0 : -1;
  }
  function activate(li) {
    if (active === li) return;
    if (active) { active.classList.remove("is-active"); var old = $(".orow-tools", active); if (old) old.remove(); rove(active, false); }
    active = li;
    li.classList.add("is-active");
    rove(li, true);
    tools(li);
  }
  function tool(li, act, label, name, text, title) {
    var b = el("button", text ? "btn" : "icon-btn", text || "");
    b.type = "button"; b.dataset.act = act;
    b.setAttribute("aria-label", label + " " + ttl(li));
    if (!text) { b.title = title || label; b.appendChild(icon(name)); }
    return b;
  }
  function tools(li) {
    var t = el("div", "orow-tools"), k = li.dataset.kind, s = sibs(li), i = s.indexOf(li);
    t.setAttribute("role", "group"); t.setAttribute("aria-label", "Actions for " + ttl(li));
    t.appendChild(tool(li, "edit", "Edit", "", "Edit"));
    if (!sorted()) {
      var up = tool(li, "up", "Move up", "arrow-up"), dn = tool(li, "down", "Move down", "arrow-down");
      var out = tool(li, "outdent", "Outdent", "outdent", "", "Move out one level"), inn = tool(li, "indent", "Indent", "indent", "", "Move inside the one above");
      up.disabled = i === 0; dn.disabled = i === s.length - 1; out.disabled = !li.dataset.parent; inn.disabled = i === 0;
      [up, dn, out, inn].forEach(function (b) { t.appendChild(b); });
    }
    if (k === "part") t.appendChild(tool(li, "addin", "Add chapter to", "", "Add chapter"));
    else if (k === "chapter") t.appendChild(tool(li, "addin", "Add scene to", "", "Add scene"));
    if (!sorted()) {
      var g = tool(li, "grip", "Drag", "drag");
      g.classList.add("orow-grip"); g.tabIndex = -1; g.title = "Drag to reorder, or use the arrow buttons";
      t.appendChild(g);
    }
    li.appendChild(t);
  }

  // ---- moving ---------------------------------------------------------------------------------------------
  // Moves run one at a time. Each works out where it goes from the list as the one before left it, so two quick
  // presses (or a press during a drag's drop) never act on a stale picture of the list.
  var queue = Promise.resolve();
  function enqueue(fn) { var next = queue.then(fn, fn); queue = next.catch(function () {}); return next; }
  function sendMove(li, parent, after) { return enqueue(function () { return sendMoveNow(li, parent, after); }); }
  function sendMoveNow(li, parent, after) {
    var body = { parent_id: parent };
    if (after !== undefined) body.after = after;
    return api("POST", "/api/v1/docs/" + li.dataset.id + "/move", body).then(function (r) {
      if (!r.ok) { fail(r.err.message || "Could not move that."); return; }
      var id = li.dataset.id;
      return refresh().then(function () {
        settleOn(id);
        var now = byId(id);
        if (!now) return;
        var s = sibs(now), p = byId(now.dataset.parent);
        say("Moved " + ttl(now) + ". Now " + (s.indexOf(now) + 1) + " of " + s.length + (p ? " in " + ttl(p) : " at the top level") + ".");
      });
    });
  }
  function act(li, a) { return enqueue(function () { return actNow(li, a); }); }
  function actNow(li, a) {
    li = byId(li.dataset.id) || li; // the row may have been redrawn by the move before this one
    var s = sibs(li), i = s.indexOf(li);
    if (a === "up" && i > 0) return sendMoveNow(li, li.dataset.parent, i > 1 ? s[i - 2].dataset.id : "");
    if (a === "down" && i < s.length - 1) return sendMoveNow(li, li.dataset.parent, s[i + 1].dataset.id);
    if (a === "indent" && i > 0) return sendMoveNow(li, s[i - 1].dataset.id, undefined);
    if (a === "outdent" && li.dataset.parent) { var p = byId(li.dataset.parent); return sendMoveNow(li, p.dataset.parent, p.dataset.id); }
    say(a === "up" ? "Already first." : a === "down" ? "Already last." : a === "indent" ? "Nothing above to move into." : "Already at the top level.");
  }

  // ---- adding ------------------------------------------------------------------------------------------------
  function create(body, label) {
    return api("POST", "/api/v1/projects/" + project + "/docs", body).then(function (r) {
      if (!r.ok || !r.json) { fail(r.err.message || "Could not add that."); return null; }
      var id = r.json.item.id;
      return refresh().then(function () { settleOn(id); say("Added " + label + "."); return id; });
    });
  }
  var form = $("#o-add");
  if (form) form.addEventListener("submit", function (e) {
    e.preventDefault();
    var kind = form.elements.kind, title = form.elements.title;
    create({ kind: kind.value, title: title.value.trim() }, kind.options[kind.selectedIndex].text.toLowerCase()).then(function (id) { if (id) title.value = ""; });
  });

  // ---- editing one row -----------------------------------------------------------------------------------
  function field(id, label, control) {
    var f = el("div", "field"), l = el("label", "", label);
    l.htmlFor = id; control.id = id;
    f.appendChild(l); f.appendChild(control);
    return f;
  }
  function openEdit(li) {
    var open = $(".orow-edit", list());
    if (open) { var f0 = $("textarea", open); if (open.parentNode !== li) { fail("Finish or cancel the edit that is open first."); } if (f0) f0.focus(); return; }
    var id = li.dataset.id, f = el("form", "orow-edit");
    f.setAttribute("aria-label", "Edit " + ttl(li)); f.noValidate = true;
    var syn = el("textarea"); syn.name = "synopsis"; syn.maxLength = 300; syn.rows = 3; syn.value = li.dataset.syn;
    var st = el("select"); st.name = "status";
    STATUS.forEach(function (o) { var op = el("option", "", o[1]); op.value = o[0]; if (o[0] === li.dataset.status) op.selected = true; st.appendChild(op); });
    var tg = el("input"); tg.type = "text"; tg.name = "target"; tg.inputMode = "numeric"; tg.autocomplete = "off"; tg.value = li.dataset.target;
    var grid = el("div", "grid");
    grid.appendChild(field("oe-st-" + id, "Status", st));
    var tf = field("oe-tg-" + id, "Target words", tg);
    if ($(".orow-mix", li)) tf.appendChild(el("span", "hint", "Its own target. 0 uses the total of what is inside."));
    grid.appendChild(tf);
    var err = el("p", "field-error"); err.setAttribute("role", "alert"); err.hidden = true;
    var box = el("div", "oe-actions"), ok = el("button", "btn solid", "Save"), no = el("button", "btn", "Cancel");
    ok.type = "submit"; no.type = "button";
    box.appendChild(ok); box.appendChild(no);
    f.appendChild(field("oe-sy-" + id, "Synopsis", syn)); f.appendChild(grid); f.appendChild(err); f.appendChild(box);
    f._rev = parseInt(li.dataset.rev, 10);
    li.classList.add("is-editing");
    var tl = $(".orow-tools", li);
    li.insertBefore(f, tl);
    syn.focus();
    no.addEventListener("click", function () { closeEdit(li, true); });
    f.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { e.stopPropagation(); closeEdit(li, true); }
      else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); save(li, f, f._rev); }
    });
    f.addEventListener("submit", function (e) { e.preventDefault(); save(li, f, f._rev); });
  }
  function closeEdit(li, back) {
    var f = $(".orow-edit", li);
    if (f) f.remove();
    li.classList.remove("is-editing");
    if (back) { var b = $('[data-act="edit"]', li); if (b) b.focus(); }
  }
  function changes(li, f) {
    var c = {}, e = f.elements, n = e.target.value.trim().replace(/,/g, "");
    if (e.synopsis.value !== li.dataset.syn) c.synopsis_short = e.synopsis.value;
    if (e.status.value !== li.dataset.status) c.status = e.status.value;
    if (n !== li.dataset.target) c.target_words = n === "" ? 0 : /^\d+$/.test(n) ? parseInt(n, 10) : NaN;
    return c;
  }
  function problem(f, m) { var p = $(".field-error", f); p.textContent = m; p.hidden = false; say(m); }
  function save(li, f, rev) {
    var c = changes(li, f), id = li.dataset.id;
    $(".field-error", f).hidden = true;
    if (c.target_words !== c.target_words) return problem(f, "The target must be a whole number of words, or 0 for none.");
    if (!Object.keys(c).length) { closeEdit(li, true); return say("Nothing changed."); }
    c.base_revision = rev;
    api("PATCH", "/api/v1/docs/" + id, c).then(function (r) {
      if (r.ok) {
        closeEdit(li, false);
        return refresh().then(function () { settleOn(id, '[data-act="edit"]'); say("Saved " + ttl(byId(id) || li) + "."); });
      }
      if (r.status === 409) return conflict(li, f, c);
      problem(f, r.status === 429 ? "Too many changes at once. Wait a moment and save again." : r.err.message || "Could not save. Your changes are still here.");
    }, function () { problem(f, "Could not reach the server. Your changes are still here."); });
  }
  // Someone changed the document since the row was drawn: show theirs, keep mine, and let the person choose.
  function conflict(li, f, c) {
    api("GET", "/api/v1/docs/" + li.dataset.id).then(function (r) {
      var cur = r.ok && r.json && r.json.item;
      if (!cur || cur.trashed) { problem(f, "This document is no longer here. Reload the outline."); return; }
      var old = $(".conflict", f), box = el("div", "conflict"), p = el("p");
      if (old) old.remove();
      box.setAttribute("role", "alert");
      p.textContent = "This changed somewhere else since you opened it. Theirs: " + (cur.synopsis_short || "no synopsis") + "; " +
        (STATUS.filter(function (s) { return s[0] === cur.status; })[0] || ["", cur.status])[1] + "; " + (cur.target_words ? num(cur.target_words) + " words" : "no target") + ". Yours is still in the form.";
      var keep = el("button", "btn solid", "Keep mine"), use = el("button", "btn", "Use theirs");
      keep.type = use.type = "button";
      keep.addEventListener("click", function () { f._rev = cur.revision; save(li, f, cur.revision); });
      use.addEventListener("click", function () { closeEdit(li, false); refresh().then(function () { settleOn(li.dataset.id, '[data-act="edit"]'); say("Showing the version saved elsewhere."); }); });
      var a = el("div", "oe-actions"); a.appendChild(keep); a.appendChild(use);
      box.appendChild(p); box.appendChild(a);
      f.insertBefore(box, $(".field-error", f));
      keep.focus();
    }, function () { problem(f, "Could not reach the server. Your changes are still here."); });
  }

  // ---- events ---------------------------------------------------------------------------------------------
  doc.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-act]"), li = e.target.closest && e.target.closest(".orow");
    if (!li || !root.contains(li)) return;
    if (b) {
      var a = b.dataset.act;
      if (a === "edit") openEdit(li);
      else if (a === "addin") create({ kind: li.dataset.kind === "part" ? "chapter" : "scene", title: "", parent_id: li.dataset.id }, li.dataset.kind === "part" ? "a chapter" : "a scene");
      else if (a !== "grip") act(li, a);
      return;
    }
    if (!e.target.closest("a, button, input, select, textarea, form")) activate(li);
  });
  doc.addEventListener("focusin", function (e) { var li = e.target.closest && e.target.closest(".orow"); if (li && root.contains(li)) activate(li); });
  doc.addEventListener("keydown", function (e) {
    var li = e.target.closest && e.target.closest(".orow");
    if (drag && e.key === "Escape") { endDrag(true); return; }
    if (!li || !root.contains(li) || e.target.closest("form, select, textarea, input")) return;
    var all = rows(), i = all.indexOf(li);
    if (e.altKey && !e.ctrlKey && !e.metaKey) {
      var a = { ArrowUp: "up", ArrowDown: "down", ArrowRight: "indent", ArrowLeft: "outdent" }[e.key];
      if (a) { e.preventDefault(); if (sorted()) say("Moving is only possible in manuscript order."); else act(li, a); }
      return;
    }
    if (e.target !== li) return;
    var n = e.key === "ArrowDown" ? i + 1 : e.key === "ArrowUp" ? i - 1 : e.key === "Home" ? 0 : e.key === "End" ? all.length - 1 : -1;
    if (n >= 0 && n < all.length) { e.preventDefault(); activate(all[n]); all[n].focus(); all[n].scrollIntoView({ block: "nearest" }); }
    else if (e.key === "Enter") { e.preventDefault(); openEdit(li); }
  });

  // ---- dragging (pointer events, so touch works too) -----------------------------------------------------------
  function target(x, y) {
    var el0 = doc.elementFromPoint(x, y), r = el0 && el0.closest(".orow"), all = rows(), last = all[all.length - 1];
    clearMarks();
    if (!r && last && y > last.getBoundingClientRect().bottom) {
      list().classList.add("drop-end");
      return { parent: "", after: undefined };
    }
    if (!r || drag.set[r.dataset.id]) return null;
    var b = r.getBoundingClientRect(), f = (y - b.top) / b.height, part = r.dataset.kind === "part";
    if (f < (part ? 0.35 : 0.5)) {
      r.classList.add("drop-before");
      var s = sibs(r).filter(function (x) { return x !== drag.li; }), i = s.indexOf(r);
      return { parent: r.dataset.parent, after: i > 0 ? s[i - 1].dataset.id : "" };
    }
    if (part) { r.classList.add("drop-into"); return { parent: r.dataset.id, after: "" }; }
    r.classList.add("drop-after");
    return { parent: r.dataset.parent, after: r.dataset.id };
  }
  function clearMarks() {
    var ol = list();
    if (!ol) return;
    ol.classList.remove("drop-end");
    $$(".drop-before, .drop-after, .drop-into", ol).forEach(function (r) { r.classList.remove("drop-before", "drop-after", "drop-into"); });
  }
  function endDrag(cancel) {
    if (!drag) return;
    var d = drag;
    drag = null;
    clearInterval(d.timer);
    clearMarks();
    $$(".is-dragging", list()).forEach(function (r) { r.classList.remove("is-dragging"); });
    doc.removeEventListener("pointermove", onMove);
    doc.removeEventListener("pointerup", onUp);
    doc.removeEventListener("pointercancel", onCancel);
    if (!cancel && d.spec) sendMove(d.li, d.spec.parent, d.spec.after);
    else if (cancel) say("Move cancelled.");
  }
  function onMove(e) { drag.x = e.clientX; drag.y = e.clientY; drag.spec = target(e.clientX, e.clientY); e.preventDefault(); }
  function onUp() { endDrag(false); }
  function onCancel() { endDrag(true); }
  doc.addEventListener("pointerdown", function (e) {
    var g = e.target.closest && e.target.closest(".orow-grip"), li = g && g.closest(".orow");
    if (!g || !li || (e.pointerType === "mouse" && e.button !== 0)) return;
    e.preventDefault();
    drag = { li: li, set: subtree(li), x: e.clientX, y: e.clientY, spec: null };
    rows().forEach(function (r) { if (drag.set[r.dataset.id]) r.classList.add("is-dragging"); });
    drag.timer = setInterval(function () { // scroll while the pointer rests near an edge
      if (!drag) return;
      var dy = drag.y < 80 ? -16 : drag.y > win.innerHeight - 80 ? 16 : 0;
      if (dy) { win.scrollBy(0, dy); drag.spec = target(drag.x, drag.y); }
    }, 40);
    doc.addEventListener("pointermove", onMove);
    doc.addEventListener("pointerup", onUp);
    doc.addEventListener("pointercancel", onCancel);
    say("Dragging " + ttl(li) + ". Release over another row to drop it there. Escape cancels.");
  });

  // ---- start -----------------------------------------------------------------------------------------------------
  var first = rows();
  first.forEach(function (r) { rove(r, false); });
  if (first.length) rove(first[0], true);
})();
