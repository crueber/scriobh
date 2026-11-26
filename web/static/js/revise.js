/* Scriobh writing view: the Revise menu in the formatting bar (an alternative, a ghost, the stash and the chapter break).
   The menu is a popover, so it opens without this script. This file adds the rest: a key press puts focus on the first
   item, the arrows and Home and End move between the items, Escape or Tab returns focus to Revise, a press on an item keeps
   the editor's selection and focus, each item's shortcut is drawn from the registry, and an item the chapter cannot take
   (Source, a held chapter, a text too long to edit) is disabled when the menu opens. It runs its commands through the bar
   (window.scriobh.tools.run), so each one does what its button did. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  var btn = doc.querySelector('[data-cmd="revise"]'), menu = doc.getElementById("revise-menu");
  if (!W || !W.editor || !btn || !menu) return;
  var items = [].slice.call(menu.querySelectorAll('[role="menuitem"]')), byKey = false;
  var place = null; // the menu's position on a wide screen, set through an adopted sheet
  try { place = new win.CSSStyleSheet(); doc.adoptedStyleSheets = doc.adoptedStyleSheets.concat([place]); } catch (e) { place = null; }
  var live = function () { return items.filter(function (x) { return !x.disabled; }); };
  items.forEach(function (it) { var k = S.keys ? S.keys.text(it.dataset.keys) : ""; it.querySelector(".menu-keys").textContent = k; });
  // A click with no pointer behind it is a key press (Enter or Space): only then does the first item take focus.
  btn.addEventListener("click", function (e) { byKey = e.detail === 0; });
  // Before the menu shows (so its items are never drawn in the wrong state): an item the chapter cannot take is off.
  menu.addEventListener("beforetoggle", function (e) {
    if (e.newState !== "open") return;
    var dead = W.editor.mode() === "source" || W.locked || !W.cfg.canEdit || W.cfg.readonly === "length";
    items.forEach(function (it) { it.disabled = it.dataset.cmd === "split" ? !!W.locked : dead; });
    if (place && win.matchMedia("(min-width: 1024px)").matches) { // a menu under its button on a wide screen; the phone's sheet stays where the CSS puts it (an adopted sheet, not the style attribute)
      var q = btn.getBoundingClientRect();
      place.replaceSync("#revise-menu{inset:" + (q.bottom + 4) + "px auto auto " + q.left + "px}");
    }
  });
  menu.addEventListener("toggle", function (e) {
    var open = e.newState === "open";
    btn.setAttribute("aria-expanded", String(open));
    if (open) {
      var first = live()[0];
      if (byKey && first) first.focus();
      byKey = false;
    } else if (!btn.disabled && (!doc.activeElement || doc.activeElement === doc.body || menu.contains(doc.activeElement))) {
      btn.focus();
    }
  });
  menu.addEventListener("keydown", function (e) {
    var l = live(), i = l.indexOf(doc.activeElement), n = -1;
    if (e.key === "Tab") { menu.hidePopover(); btn.focus(); return; } // focus goes back to Revise now, and the Tab moves on from it
    if (i < 0) return;
    if (e.key === "ArrowDown") n = (i + 1) % l.length;
    else if (e.key === "ArrowUp") n = (i - 1 + l.length) % l.length;
    else if (e.key === "Home") n = 0;
    else if (e.key === "End") n = l.length - 1;
    if (n < 0) return;
    e.preventDefault();
    l[n].focus();
  });
  // The editor keeps its selection and focus while an item is pressed; the item runs on the release.
  menu.addEventListener("mousedown", function (e) { if (e.target.closest('[role="menuitem"]')) e.preventDefault(); });
  menu.addEventListener("click", function (e) {
    var it = e.target.closest('[role="menuitem"]');
    if (!it || it.disabled) return;
    var key = e.detail === 0;
    menu.hidePopover();
    if (S.tools) S.tools.run(it.dataset.cmd);
    // After a key press the text takes focus back, unless the command has put focus somewhere it means to be.
    if (key && (doc.activeElement === btn || doc.activeElement === doc.body)) W.editor.focus();
  });
})();
