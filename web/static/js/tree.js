/* Scríobh writing view: the manuscript tree (keyboard, drag, rename, trash, move),
   the narrow-screen drawer, the chapter jump list. Loaded after doc.js; every change
   goes through the server's UI routes and the tree is re-rendered from its fragment.
   Plain fetch, never htmx: a signed-out session must not navigate away from the text. */
(function () {
  "use strict";
  var doc = document, win = window;
  var S = win.scriobh || (win.scriobh = {});
  var $ = S.$, $$ = S.$$, el = S.el;
  var W = S.write;
  var root = $("#write"), slot = $("#tree-slot");
  if (!root || !slot || !W) return;
  var project = root.dataset.project, slug = root.dataset.slug, current = W.cfg.id, canEdit = W.cfg.canEdit;
  var isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

  function keep(k, v) {
    try { if (v === undefined) return localStorage.getItem(k); if (v === null) localStorage.removeItem(k); else localStorage.setItem(k, v); } catch (e) { /* storage is optional */ }
    return null;
  }
  function fmt(n) { return n ? S.fmt(n) : "–"; }
  function tree() { return $("#tree", slot); }
  function rows() { var t = tree(); return t ? $$(".tree-row", t) : []; }
  function visible() { return rows().filter(function (r) { return !r.hidden; }); }
  function byId(id) { return $("#tr-" + id, slot); }
  function title(r) { return $(".tree-title", r).textContent; }
  function level(r) { return +r.dataset.level; }
  function href(id) { return "/p/" + slug + "/d/" + id; }

  // ---- counts and titles that change without a reload ------------------------------------
  function recount() {
    var rs = rows(), total = 0;
    rs.forEach(function (r, i) {
      var own = +r.dataset.words, sum = own;
      if (r.hasAttribute("aria-expanded")) {
        for (var j = i + 1; j < rs.length && level(rs[j]) > level(r); j++) sum += +rs[j].dataset.words;
      }
      $(".tree-count", r).textContent = fmt(sum);
      relabel(r, sum);
      total += own;
    });
    var t = $("#tree-total", slot);
    if (t) t.textContent = fmt(total) === "–" ? "0" : fmt(total);
  }
  function relabel(r, n) {
    var num = $(".tree-num", r);
    r.setAttribute("aria-label", (num ? num.textContent + ", " : "") + title(r) + ", " + (n || 0).toLocaleString("en-US") + (n === 1 ? " word" : " words"));
  }
  function setWords(id, n) { var r = byId(id); if (r) { r.dataset.words = n; recount(); } }
  function setTitle(id, t) {
    var r = byId(id);
    if (r) { $(".tree-title", r).textContent = t || "Untitled"; relabel(r, +$(".tree-count", r).textContent.replace(/[^0-9]/g, "") || 0); }
    var j = $("#j-" + id);
    if (j) { j.dataset.label = t || "Untitled"; $(".j-title", j).textContent = t || "Untitled"; }
  }

  // ---- collapse state, filter -------------------------------------------------------------------
  var ckey = "scriobh:collapsed:" + project, filterText = "";
  function collapsedSet() { try { return JSON.parse(keep(ckey) || "[]"); } catch (e) { return []; } }
  function layout() {
    var rs = rows(), cs = collapsedSet(), below = 0, q = filterText.trim().toLowerCase();
    if (q) {
      var show = {};
      rs.forEach(function (r, i) {
        if (title(r).toLowerCase().indexOf(q) < 0) return;
        show[r.dataset.id] = 1;
        for (var j = i - 1, l = level(r); j >= 0; j--) if (level(rs[j]) < l) { show[rs[j].dataset.id] = 1; l = level(rs[j]); }
      });
      rs.forEach(function (r) { r.hidden = !show[r.dataset.id]; });
      return;
    }
    rs.forEach(function (r) {
      var l = level(r);
      if (below && l > below) { r.hidden = true; return; }
      below = 0;
      r.hidden = false;
      if (r.hasAttribute("aria-expanded")) {
        var open = cs.indexOf(r.dataset.id) < 0;
        r.setAttribute("aria-expanded", String(open));
        if (!open) below = l;
      }
    });
  }
  // A collapsed folder must not hide the page you are on, or leave the tree without a tab stop.
  function reveal() {
    var cs = collapsedSet(), r = byId(current), changed = false;
    while (r && r.dataset.parent && byId(r.dataset.parent)) {
      r = byId(r.dataset.parent);
      var at = cs.indexOf(r.dataset.id);
      if (at >= 0) { cs.splice(at, 1); changed = true; }
    }
    if (changed) keep(ckey, JSON.stringify(cs));
  }
  function stop() {
    var vis = visible(), cur = byId(current);
    if (vis.some(function (x) { return x.tabIndex === 0; })) return;
    rows().forEach(function (x) { x.tabIndex = -1; });
    var f = cur && !cur.hidden ? cur : vis[0];
    if (f) f.tabIndex = 0;
  }
  function toggle(r, open) {
    if (!r.hasAttribute("aria-expanded")) return;
    var cs = collapsedSet().filter(function (x) { return x !== r.dataset.id; });
    if (!open) cs.push(r.dataset.id);
    keep(ckey, JSON.stringify(cs));
    layout();
  }
  function focusRow(r) {
    if (!r) return;
    rows().forEach(function (x) { x.tabIndex = -1; });
    r.tabIndex = 0;
    r.focus({ preventScroll: false });
  }

  // ---- talking to the server ------------------------------------------------------------------------
  function say(m, undo) { if (S.toast) S.toast(m, undo ? { undo: undo } : undefined); }
  function problem(r) {
    var m = "That did not go through. Your work is safe.";
    try { m = JSON.parse(r.text).error.message || m; } catch (e) { /* keep the default */ }
    if (r.status === 401) m = "Signed out. Sign in in another tab, then try again.";
    else if (r.status === 0) m = "Could not reach the server. Nothing was changed.";
    say(m);
  }
  var later = null; // a redraw that arrived while a title was being typed waits for it
  function swap(html, focusId) {
    if ($(".is-renaming", slot)) { later = [html, focusId]; return; }
    // A slow answer must not pull the caret out of the editor: focus moves only if it was in the tree or a menu.
    var a = doc.activeElement, mine = slot.contains(a) || (menu && menu.open) || a === doc.body || !a;
    slot.innerHTML = html;
    reveal();
    layout();
    recount();
    var t = tree();
    if (t) {
      var f = focusId && byId(focusId) && !byId(focusId).hidden ? byId(focusId) : byId(current) || rows()[0];
      if (f) { rows().forEach(function (x) { x.tabIndex = -1; }); f.tabIndex = 0; if (focusId && mine) f.focus(); }
      stop();
    }
    navAids();
  }
  // The chapter jump list, the previous and next links and the position all come from the tree,
  // so they are rebuilt from it after every change.
  function navAids() {
    var rs = rows(), i = -1;
    rs.forEach(function (r, n) { if (r.dataset.id === current) i = n; });
    if (i < 0) return;
    var pos = $("#st-pos");
    if (pos) pos.textContent = "Document " + (i + 1) + " of " + rs.length;
    var prev = rs[i - 1], next = rs[i + 1];
    ["prev", "next"].forEach(function (k, n) {
      var r = n ? next : prev;
      if (r) root.dataset[k] = href(r.dataset.id); else delete root.dataset[k];
    });
    var nav = $(".doc-nav"), paper = $("#paper");
    if (nav) nav.remove();
    if (paper && (prev || next)) {
      nav = el("nav", "doc-nav");
      nav.setAttribute("aria-label", "Neighbouring documents");
      [[prev, "Previous", "prev", ""], [next, "Next", "next", " doc-nav-next"]].forEach(function (x) {
        if (!x[0]) { if (x[2] === "prev") nav.appendChild(el("span")); return; }
        var a = el("a", "doc-nav-link" + x[3]);
        a.rel = x[2]; a.href = href(x[0].dataset.id);
        a.append(el("span", "doc-nav-dir", x[1]), el("span", "doc-nav-title", title(x[0])));
        nav.appendChild(a);
      });
      paper.appendChild(nav);
    }
    if (jlist) {
      var path = [];
      jlist.replaceChildren.apply(jlist, rs.map(function (r) {
        var l = level(r);
        path.length = l - 1;
        var li = el("li"), num = $(".tree-num", r);
        li.id = "j-" + r.dataset.id; li.setAttribute("role", "option");
        li.dataset.href = href(r.dataset.id); li.dataset.label = title(r);
        if (r.dataset.id === current) li.dataset.current = "";
        li.append(el("span", "j-num", num ? num.textContent : ""), el("span", "j-title", title(r)));
        if (path.length) li.appendChild(el("span", "j-group", path.join(" \u203a ")));
        path[l - 1] = title(r);
        return li;
      }));
      jopts = $$("li", jlist);
    }
  }
  var queue = Promise.resolve();
  function mutate(url, fields, focusId) {
    fields = fields || {};
    var run = function () {
      fields.current = current;
      return W.ui(url, fields).then(function (r) {
        if (!r.ok) { problem(r); return null; }
        swap(r.text, focusId || r.id);
        return r;
      }, function () { problem({ status: 0 }); return null; });
    };
    queue = queue.then(run, run); // one change at a time, in the order they were made
    return queue;
  }
  function refresh() {
    return fetch("/ui/projects/" + project + "/tree?current=" + encodeURIComponent(current), { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } })
      .then(function (r) { return r.ok ? r.text() : null; }).then(function (t) { if (t) swap(t); });
  }

  // ---- moves -----------------------------------------------------------------------------------------------
  function siblings(r) { return rows().filter(function (x) { return x.dataset.parent === r.dataset.parent; }); }
  function move(r, parent, after) {
    var f = { parent_id: parent };
    if (after !== undefined) f.after = after;
    return mutate("/ui/docs/" + r.dataset.id + "/move", f, r.dataset.id);
  }
  function plan(r, how) {
    var sib = siblings(r), i = sib.indexOf(r), par = r.dataset.parent, p = par ? byId(par) : null;
    if (how === "up" && i > 0) return { parent: par, after: i > 1 ? sib[i - 2].dataset.id : "" };
    if (how === "down" && i < sib.length - 1) return { parent: par, after: sib[i + 1].dataset.id };
    if (how === "in" && i > 0) return { parent: sib[i - 1].dataset.id };
    if (how === "out" && p) return { parent: p.dataset.parent, after: p.dataset.id };
    return null;
  }
  function shift(r, how) {
    var p = plan(r, how);
    if (!p) return;
    if (how === "in") { var t = byId(p.parent); if (t && t.getAttribute("aria-expanded") === "false") toggle(t, true); }
    move(r, p.parent, p.after);
  }
  function inside(r, t) {
    if (t === r) return true;
    var rs = rows(), i = rs.indexOf(r);
    for (var j = i + 1; j < rs.length && level(rs[j]) > level(r); j++) if (rs[j] === t) return true;
    return false;
  }

  // ---- rename, trash ------------------------------------------------------------------------------------------
  function rename(r) {
    if (!canEdit || r.classList.contains("is-renaming")) return;
    var link = $(".tree-link", r), old = r.dataset.title != null ? r.dataset.title : title(r);
    if (old === "Untitled") old = "";
    var input = el("input", "tree-rename");
    input.type = "text"; input.value = old; input.maxLength = 300; input.setAttribute("aria-label", "Title");
    r.classList.add("is-renaming");
    link.hidden = true;
    r.insertBefore(input, link);
    input.focus(); input.select();
    var done = false;
    function finish(save) {
      if (done) return;
      done = true;
      var v = input.value.trim();
      input.remove(); link.hidden = false; r.classList.remove("is-renaming");
      r.focus();
      if (later) { var l = later; later = null; setTimeout(function () { swap(l[0], l[1]); }, 0); }
      if (!save || v === old) return;
      if (r.dataset.id === current) {
        var t = $("#doc-title");
        W.setTitle(v);
        t.dispatchEvent(new win.Event("input", { bubbles: true }));
        W.flushAll();
      } else {
        setTitle(r.dataset.id, v);
        mutate("/ui/docs/" + r.dataset.id + "/rename", { title: v }, r.dataset.id);
      }
    }
    input.addEventListener("keydown", function (e) {
      e.stopPropagation();
      if (e.key === "Enter") { e.preventDefault(); finish(true); }
      else if (e.key === "Escape") { e.preventDefault(); finish(false); }
    });
    input.addEventListener("blur", function () { finish(true); });
  }
  function trash(r) {
    if (!canEdit) return;
    var id = r.dataset.id, name = title(r), here = r.dataset.id === current || inside(r, byId(current));
    var rs = visible(), i = rs.indexOf(r);
    var next = rs.filter(function (x) { return !inside(r, x); });
    var around = next[Math.min(i, next.length - 1)] || null;
    var rs0 = rows(), at = rs0.indexOf(r), kids = 0;
    for (var k = at + 1; k < rs0.length && level(rs0[k]) > level(r); k++) kids++;
    var what = "\u201c" + name + "\u201d" + (kids ? " and the " + kids + (kids === 1 ? " document" : " documents") + " inside it" : "");
    function go() {
      mutate("/ui/docs/" + id + "/trash", {}, around ? around.dataset.id : "").then(function (res) {
        if (!res) return;
        if (here) {
          var to = around ? href(around.dataset.id) : "/p/" + slug;
          win.location.assign(to + (to.indexOf("?") < 0 ? "?" : "&") + "trashed=" + id + "&tt=" + encodeURIComponent(name));
          return;
        }
        say("Moved " + what + " to the trash.", function () { restoreDoc(id); });
      });
    }
    if (here) W.flushAll().then(function (ok) { if (ok) go(); else say("Not saved yet. Staying here so nothing is lost."); });
    else go();
  }
  function restoreDoc(id, open) {
    mutate("/ui/docs/" + encodeURIComponent(id) + "/restore", {}, id).then(function (r) { if (r && open) win.location.assign(href(id)); });
  }
  (function undoAfterReload() {
    var q = new win.URLSearchParams(win.location.search), id = q.get("trashed") || "";
    if (!id) return;
    win.history.replaceState(null, "", win.location.pathname);
    if (!/^doc_[0-9A-Za-z]+$/.test(id)) return;
    say("Moved \u201c" + (q.get("tt") || "the document").slice(0, 200) + "\u201d to the trash.", function () { restoreDoc(id, true); });
  })();

  // ---- adding ---------------------------------------------------------------------------------------------------------
  function selected() { return doc.activeElement && doc.activeElement.closest && doc.activeElement.closest(".tree-row") || byId(current); }
  function place(kind, sel, inner) {
    if (!sel) return {};
    var par = sel.dataset.parent ? byId(sel.dataset.parent) : null, k = sel.dataset.kind;
    if (inner) return { parent: sel.dataset.id };
    if (kind === "part") {
      var top = sel; while (top.dataset.parent && byId(top.dataset.parent)) top = byId(top.dataset.parent);
      return { parent: "", after: top.dataset.id };
    }
    if (kind === "chapter") {
      if (k === "part") return { parent: sel.dataset.id };
      if (k === "scene" && par && par.dataset.kind === "chapter") return { parent: par.dataset.parent, after: par.dataset.id };
    }
    if (kind === "scene" && (k === "chapter" || k === "part")) return { parent: sel.dataset.id };
    return { parent: sel.dataset.parent, after: sel.dataset.id };
  }
  function add(kind, sel, inner) {
    var p = place(kind, sel, inner), f = { kind: kind, parent_id: p.parent || "" };
    if (p.after !== undefined) f.after = p.after;
    W.flushAll().then(function (ok) {
      mutate("/ui/projects/" + project + "/docs", f).then(function (r) {
        if (!r) return;
        if (ok) win.location.assign(href(r.id) + "?new=1");
        else say("Added. Open it from the manuscript when your text is saved.");
      });
    });
  }
  $$("[data-add]").forEach(function (b) {
    b.addEventListener("click", function () {
      var m = $("#add-menu");
      if (m && m.hidePopover) m.hidePopover();
      add(b.dataset.add, selected());
    });
  });

  // ---- the row menu: the same actions for touch and for the keyboard --------------------------------------------------
  // Its code is a separate file, fetched the first time a menu is asked for (touch users and the keyboard).
  var menu = $("#row-menu");
  function openMenu(r) {
    if (!menu || !canEdit) return;
    var show = function () { S.rowMenu(r); };
    if (S.rowMenu) return show();
    var t = el("script");
    t.src = root.dataset.rowmenuSrc;
    t.onload = show;
    t.onerror = function () { say("Could not load the menu. Nothing was changed."); };
    doc.head.appendChild(t);
  }
  S.treeApi = { rename: rename, add: add, shift: shift, plan: plan, move: move, trash: trash, title: title, rows: rows, byId: byId, inside: inside };

  // ---- keyboard ---------------------------------------------------------------------------------------------------------------
  slot.addEventListener("keydown", function (e) {
    var r = e.target.closest && e.target.closest(".tree-row");
    if (!r || r.classList.contains("is-renaming") || e.target.tagName === "INPUT" || e.target.closest(".tree-more")) return;
    var vis = visible(), i = vis.indexOf(r), k = e.key, alt = e.altKey, handled = true;
    if (alt && !e.ctrlKey && !e.metaKey && canEdit) {
      if (k === "ArrowUp") shift(r, "up"); else if (k === "ArrowDown") shift(r, "down");
      else if (k === "ArrowRight") shift(r, "in"); else if (k === "ArrowLeft") shift(r, "out");
      else handled = false;
    } else if (e.ctrlKey || e.metaKey) {
      handled = false;
    } else if (k === "ArrowDown") focusRow(vis[i + 1]);
    else if (k === "ArrowUp") focusRow(vis[i - 1]);
    else if (k === "Home") focusRow(vis[0]);
    else if (k === "End") focusRow(vis[vis.length - 1]);
    else if (k === "ArrowRight") {
      if (r.getAttribute("aria-expanded") === "false") toggle(r, true);
      else if (r.hasAttribute("aria-expanded")) focusRow(vis[i + 1]);
    } else if (k === "ArrowLeft") {
      if (r.getAttribute("aria-expanded") === "true") toggle(r, false);
      else if (r.dataset.parent && byId(r.dataset.parent)) focusRow(byId(r.dataset.parent));
    } else if (k === "Enter" || k === " ") W.go(href(r.dataset.id));
    else if (k === "F2" && canEdit) rename(r);
    else if ((k === "Delete" || k === "Backspace" && isMac) && canEdit) trash(r);
    else if ((k === "ContextMenu" || k === "F10" && e.shiftKey) && canEdit) openMenu(r);
    else if (k.length === 1 && !alt) {
      var q = k.toLowerCase(), n = vis.slice(i + 1).concat(vis.slice(0, i + 1)).filter(function (x) { return title(x).toLowerCase().indexOf(q) === 0; })[0];
      if (n) focusRow(n); else handled = false;
    } else handled = false;
    if (handled) e.preventDefault();
  });
  slot.addEventListener("focusin", function (e) {
    var r = e.target.closest && e.target.closest(".tree-row");
    if (r && r !== doc.activeElement) return;
    if (r) { rows().forEach(function (x) { x.tabIndex = -1; }); r.tabIndex = 0; }
  });
  slot.addEventListener("click", function (e) {
    if (dragged) { dragged = false; e.preventDefault(); return; }
    var cv = e.target.closest(".tree-cover"), r = e.target.closest(".tree-row");
    if (cv && !(e.metaKey || e.ctrlKey || e.shiftKey || e.button)) { e.preventDefault(); W.go(cv.getAttribute("href")); return; } // the fixed Cover row: leave only when the text is saved
    if (!r) return;
    if (e.target.closest(".tree-more")) { e.preventDefault(); openMenu(r); return; }
    if (e.target.closest(".twist")) { toggle(r, r.getAttribute("aria-expanded") === "false"); return; }
    if (e.target.closest(".tree-rename")) return;
    var a = e.target.closest(".tree-link");
    if (a && !(e.metaKey || e.ctrlKey || e.shiftKey || e.button)) { e.preventDefault(); W.go(a.getAttribute("href")); }
  });
  slot.addEventListener("dblclick", function (e) {
    var r = e.target.closest(".tree-row");
    if (r && canEdit && e.target.closest(".tree-title")) { e.preventDefault(); rename(r); }
  });
  slot.addEventListener("contextmenu", function (e) {
    var r = e.target.closest(".tree-row");
    if (r && canEdit) { e.preventDefault(); openMenu(r); }
  });

  // ---- drag and drop with pointer events (touch included) ----------------------------------------------------------------------------
  var drag = null, dragged = false;
  function zone(t, y) {
    var b = t.getBoundingClientRect(), f = (y - b.top) / b.height;
    return f < 0.28 ? "before" : f > 0.72 ? "after" : "into";
  }
  function clearDrop() { $$(".drop-before,.drop-after,.drop-into").forEach(function (x) { x.classList.remove("drop-before", "drop-after", "drop-into"); }); }
  function beginDrag(d) {
    d.active = true;
    d.row.classList.add("is-dragging");
    doc.body.classList.add("is-dragging-row");
    try { d.row.setPointerCapture(d.id); } catch (e) { /* not capturable */ }
  }
  function endDrag() {
    if (!drag) return;
    clearTimeout(drag.timer);
    if (drag.active) { drag.row.classList.remove("is-dragging"); doc.body.classList.remove("is-dragging-row"); clearDrop(); }
    drag = null;
    doc.removeEventListener("touchmove", stopScroll);
  }
  function stopScroll(e) { if (drag && drag.active && e.cancelable) e.preventDefault(); }
  slot.addEventListener("pointerdown", function (e) {
    var r = e.target.closest && e.target.closest(".tree-row");
    if (!canEdit || !r || e.button || e.target.closest(".tree-more,.tree-rename,.twist") || r.classList.contains("is-renaming")) return;
    drag = { row: r, id: e.pointerId, x: e.clientX, y: e.clientY, active: false, touch: e.pointerType === "touch", target: null };
    if (drag.touch) { drag.timer = setTimeout(function () { if (drag) beginDrag(drag); }, 380); doc.addEventListener("touchmove", stopScroll, { passive: false }); }
  });
  slot.addEventListener("pointermove", function (e) {
    if (!drag || e.pointerId !== drag.id) return;
    if (!drag.active) {
      if (Math.abs(e.clientX - drag.x) + Math.abs(e.clientY - drag.y) < 6) return;
      if (drag.touch) { endDrag(); return; } // a scroll, not a drag
      beginDrag(drag);
    }
    clearDrop();
    var under = doc.elementFromPoint(e.clientX, e.clientY), t = under && under.closest && under.closest(".tree-row");
    drag.target = null;
    if (t && !t.hidden && !inside(drag.row, t)) { var z = zone(t, e.clientY); t.classList.add("drop-" + z); drag.target = { row: t, zone: z }; }
    var box = tree().getBoundingClientRect();
    if (e.clientY < box.top + 24) tree().scrollTop -= 10; else if (e.clientY > box.bottom - 24) tree().scrollTop += 10;
  });
  function drop(e) {
    if (!drag) return;
    var d = drag, was = d.active;
    endDrag();
    if (!was) return;
    dragged = true;
    setTimeout(function () { dragged = false; }, 0);
    if (e.type === "pointercancel" || !d.target) return;
    var t = d.target.row, z = d.target.zone, r = d.row;
    if (z === "into") { if (t.getAttribute("aria-expanded") === "false") toggle(t, true); move(r, t.dataset.id); return; }
    var sib = siblings(t).filter(function (x) { return x !== r; }), i = sib.indexOf(t);
    move(r, t.dataset.parent, z === "after" ? t.dataset.id : i > 0 ? sib[i - 1].dataset.id : "");
  }
  slot.addEventListener("pointerup", drop);
  slot.addEventListener("pointercancel", drop);

  // ---- the narrow-screen drawer -----------------------------------------------------------------------------------------------------------
  var rail = $("#rail"), tog = $("#tree-toggle"), scrim = $("#scrim"), filter = $("#tree-filter"), shut = $("#rail-close");
  var wide = win.matchMedia("(min-width: 1024px)");
  function inert(on) { ["#main", ".topbar", ".tabbar"].forEach(function (s) { var n = $(s); if (n) n.inert = on; }); }
  function openRail() {
    rail.classList.add("is-open"); scrim.hidden = false; tog.setAttribute("aria-expanded", "true"); inert(true);
    focusRow(byId(current) && !byId(current).hidden ? byId(current) : visible()[0]);
  }
  function closeRail(back) {
    if (!rail.classList.contains("is-open")) return;
    rail.classList.remove("is-open"); scrim.hidden = true; tog.setAttribute("aria-expanded", "false"); inert(false);
    if (back !== false) tog.focus();
  }
  tog.addEventListener("click", function () { if (rail.classList.contains("is-open")) closeRail(); else openRail(); });
  scrim.addEventListener("click", function () { closeRail(); });
  shut.addEventListener("click", function () { closeRail(); });
  rail.addEventListener("keydown", function (e) { if (e.key === "Escape" && rail.classList.contains("is-open") && !e.target.closest(".is-renaming")) { e.preventDefault(); closeRail(); } });
  wide.addEventListener("change", function () { if (wide.matches) closeRail(false); });
  filter.addEventListener("input", function () { filterText = filter.value; layout(); });

  // Mod+\ hides the tree for focus on a wide screen.
  var hidden = keep("scriobh:tree") === "hidden";
  doc.body.classList.toggle("tree-hidden", hidden);
  doc.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key === "\\" && wide.matches) {
      e.preventDefault();
      hidden = !hidden;
      doc.body.classList.toggle("tree-hidden", hidden);
      keep("scriobh:tree", hidden ? "hidden" : "shown");
    }
  });

  // ---- the chapter jump list (breadcrumb on a wide screen, bottom sheet on a narrow one) ------------------------------------------------------
  var jump = $("#jump"), jin = $("#jump-input"), jlist = $("#jump-list"), jempty = $("#jump-empty"), jopen = $("#jump-open");
  var jopts = $$("li", jlist), jshown = [], jactive = -1, jreturn = null;
  function jselect(n) {
    jactive = n;
    jopts.forEach(function (o) { o.removeAttribute("aria-selected"); });
    var o = jshown[n];
    if (o) { o.setAttribute("aria-selected", "true"); jin.setAttribute("aria-activedescendant", o.id); o.scrollIntoView({ block: "nearest" }); }
    else jin.removeAttribute("aria-activedescendant");
  }
  function jfilter() {
    var q = jin.value.trim().toLowerCase();
    jshown = jopts.filter(function (o) {
      var hit = !q || (o.dataset.label + " " + ($(".j-group", o) || {}).textContent).toLowerCase().indexOf(q) >= 0;
      o.hidden = !hit;
      return hit;
    });
    jempty.hidden = jshown.length > 0;
    var cur = jshown.filter(function (o) { return o.hasAttribute("data-current"); })[0];
    jselect(jshown.length ? (q || !cur ? 0 : jshown.indexOf(cur)) : -1);
  }
  function jgo(o) { if (!o) return; jump.close(); W.go(o.dataset.href); }
  if (jump) {
    jopen.addEventListener("click", function () { jreturn = jopen; jin.value = ""; jfilter(); jump.showModal(); jin.focus(); });
    jin.addEventListener("input", jfilter);
    jin.addEventListener("keydown", function (e) {
      var n = jshown.length;
      if (e.key === "ArrowDown") { e.preventDefault(); if (n) jselect((jactive + 1) % n); }
      else if (e.key === "ArrowUp") { e.preventDefault(); if (n) jselect((jactive - 1 + n) % n); }
      else if (e.key === "Home") { e.preventDefault(); jselect(0); }
      else if (e.key === "End") { e.preventDefault(); jselect(n - 1); }
      else if (e.key === "Enter") { e.preventDefault(); jgo(jshown[jactive]); }
    });
    jlist.addEventListener("click", function (e) { jgo(e.target.closest("li")); });
    jump.addEventListener("click", function (e) { if (e.target === jump) jump.close(); });
    jump.addEventListener("close", function () { if (jreturn && jreturn.isConnected) jreturn.focus({ preventScroll: true }); });
  }

  S.tree = { setWords: setWords, setTitle: setTitle, refresh: refresh };
  reveal();
  layout();
  recount();
  stop();
  var cur = byId(current);
  if (cur && !cur.hidden) { var t0 = tree(); if (t0 && cur.offsetTop > t0.clientHeight - 60) t0.scrollTop = cur.offsetTop - t0.clientHeight / 2; }
})();
