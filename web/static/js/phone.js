/* Scríobh writing view, phone (under 640 px): the sheet gives the caret back when it closes (Escape, its close button, or a tap on
   the text), keeps the caret line above it and the keyboard, and the Aa button opens the formatting strip. U11.3. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  var box = doc.getElementById("inspector"), bar = doc.getElementById("fmtbar"), aa = doc.getElementById("fmt-aa"), track = doc.getElementById("fmt-track");
  if (!W || !W.editor || !box || !S.inspector) return;
  var view = W.editor.view, sheet = win.matchMedia("(max-width: 639px)"), strip = win.matchMedia("(max-width: 639px) and (pointer: coarse)");
  var tapped = false, wasOpen = false, lastKey = false, byKey = false, isOpen = function () { return box.classList.contains("is-open"); };
  // The caret line stays between the header and the sheet (its layout top: the sheet may still be sliding in).
  function keep() {
    if (!sheet.matches || !isOpen()) return;
    var c = view.coordsAtPos(view.state.selection.head), head = doc.querySelector(".topbar").getBoundingClientRect().bottom + 8;
    var foot = win.innerHeight - parseFloat(getComputedStyle(box).bottom) - box.offsetHeight - 24;
    if (c.bottom > foot) win.scrollBy(0, c.bottom - foot);
    else if (c.top < head) win.scrollBy(0, c.top - head);
  }
  // Closed by other than a tap: the caret returns, unless focus went somewhere on purpose.
  function back() {
    var ae = doc.activeElement;
    if (!ae || ae === doc.body || box.contains(ae) || ae.classList.contains("insp-open")) { var y = win.scrollY; view.focus(); win.scrollTo(0, y); } // the browser scrolls to the caret on focus; the page stays put
  }
  new win.MutationObserver(function () {
    var open = isOpen();
    if (open === wasOpen) return;
    wasOpen = open;
    // The Comments tab lifts its own words and keeps the page to go back to (js/cwrite.js), so the caret is kept only for the other tabs.
    if (open) { byKey = lastKey; var cp = doc.getElementById("ipanel-comments"); if (cp && !cp.hidden) return; win.requestAnimationFrame(keep); } // a reader has no Comments tab
    else if (tapped) tapped = false;
    else if (sheet.matches && !byKey) back(); // opened by a finger: the caret comes back; opened by a key: inspector.js returns to where it was
  }).observe(box, { attributes: true, attributeFilter: ["class"] });
  if (win.visualViewport) win.visualViewport.addEventListener("resize", keep);
  doc.addEventListener("pointerdown", function () { lastKey = false; }, true);
  doc.addEventListener("keydown", function () { lastKey = true; }, true);
  // A click, not a touch: a finger that scrolls the text does not close it.
  doc.getElementById("editor").addEventListener("click", function () {
    if (sheet.matches && isOpen()) { tapped = true; S.inspector.close(false); }
  });
  // Escape closes it from anywhere, but not over a menu or a dialog.
  doc.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !e.defaultPrevented && sheet.matches && isOpen() && !doc.querySelector(":popover-open, dialog[open]")) S.inspector.close(false);
  });
  // js/format.js works out the "more" button from the track's scroll, which new contents do not raise. If the bar did not load, there is no Aa.
  if (aa && track) {
    var applyStrip = function () {
      if (!strip.matches) bar.removeAttribute("data-aa");
      else if (bar.dataset.aa !== "open") bar.dataset.aa = "closed";
      aa.setAttribute("aria-expanded", String(bar.dataset.aa === "open" && strip.matches));
      track.dispatchEvent(new Event("scroll"));
    };
    aa.addEventListener("click", function () { bar.dataset.aa = bar.dataset.aa === "open" ? "closed" : "open"; applyStrip(); });
    // An arrow on the closed strip opens it, so the arrows reach every button.
    bar.addEventListener("keydown", function (e) { if (bar.dataset.aa === "closed" && /^Arrow(Left|Right)$/.test(e.key)) { bar.dataset.aa = "open"; applyStrip(); } }, true);
    strip.addEventListener("change", applyStrip);
    applyStrip();
  }
}());
