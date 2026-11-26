/* Scríobh writing view: the caret and the scroll come back after Back, a reload or Back to writing (F-1266). */
(function () {
  "use strict";
  var win = window, doc = document, W = win.scriobh && win.scriobh.write, ed = W && W.editor, view = ed && ed.view;
  if (!view) return;
  var key = "scriobh:pos:" + W.person + ":" + W.cfg.id, store = win.sessionStorage, moved = false, back = win.location.hash === "#resume", p = null;
  ["keydown", "pointerdown", "wheel", "touchstart"].forEach(function (t) { doc.addEventListener(t, function () { moved = true; }, { capture: true, passive: true }); });
  win.addEventListener("pagehide", function () {
    try {
      var s = view.state.selection;
      if (ed.mode() === "rich") { store.setItem(key, JSON.stringify({ r: W.rev, h: s.head, y: win.scrollY|0 })); win.history.scrollRestoration = "manual"; }
      else { store.removeItem(key); win.history.scrollRestoration = "auto"; }
    } catch (e) {}
  });
  try { p = JSON.parse(store.getItem(key)); } catch (e) {}
  if (back) try { win.history.replaceState(null, "", win.location.pathname + win.location.search); } catch (e) {}
  var s = view.state.selection, nav = win.performance.getEntriesByType("navigation")[0];
  if (!p || nav && nav.type === "navigate" && !back || /[?&](find|new)=/.test(win.location.search) || ed.mode() !== "rich" || W.unsaved() || win.scrollY > 4 || doc.activeElement !== doc.body || !s.eq(s.constructor.atStart(view.state.doc))) return;
  var ok = p.r === W.rev, end = view.state.doc.content.size;
  if (ok) try { view.dispatch(view.state.tr.setSelection(s.constructor.near(view.state.doc.resolve(Math.min(p.h, end))))); } catch (e) { ok = false; }
  function put() { if (!moved) win.scrollTo(0, p.y); }
  put();
  win.requestAnimationFrame(function () {
    put();
    if (!ok || moved) return;
    try { var c = view.coordsAtPos(view.state.selection.head); if (c.top < 0 || c.bottom > win.innerHeight) win.scrollBy(0, c.top - win.innerHeight / 3); } catch (e) {}
    if (back && doc.activeElement === doc.body && win.matchMedia("(pointer: fine)").matches) view.focus();
  });
}());
