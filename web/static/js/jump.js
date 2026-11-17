/* Scríobh: open a chapter at a passage. A search by meaning links to /p/{slug}/d/{id}?find=the first words of the passage;
   this finds them in the text and scrolls them to the middle of the screen, selected. It only reads the page and moves
   the view: nothing is typed, saved or changed, and the address is cleaned afterwards. */
(function () {
  "use strict";
  var doc = document, win = window, m = /[?&]find=([^&]*)/.exec(win.location.search);
  if (!m) return;
  var want = "";
  try { want = decodeURIComponent(m[1].replace(/\+/g, " ")).replace(/\s+/g, " ").trim(); } catch (e) { return; }
  if (!want) return;
  function walk(root, needle) {
    var w = doc.createTreeWalker(root, 4), n, i;
    while ((n = w.nextNode())) { i = n.nodeValue.indexOf(needle); if (i >= 0) return { node: n, at: i }; }
    return null;
  }
  var tries = 0;
  (function look() {
    var ed = doc.querySelector(".scriobh-prose");
    if (!ed) { if (tries++ < 50) win.setTimeout(look, 100); return; }
    // The whole phrase, then shorter and shorter beginnings of it: a mark in the middle splits a text node.
    var found = null;
    for (var len = want.length; len >= 14 && !found; len -= 10) found = walk(ed, want.slice(0, len));
    if (found && found.at >= 0) {
      var r = doc.createRange(), n = found.node, end = Math.min(n.nodeValue.length, found.at + Math.min(want.length, 40));
      r.setStart(n, found.at); r.setEnd(n, end);
      var s = win.getSelection(); s.removeAllRanges(); s.addRange(r);
      var host = n.parentElement; if (host && host.scrollIntoView) host.scrollIntoView({ block: "center" });
    }
    try { win.history.replaceState(null, "", win.location.pathname); } catch (e) { /* the address stays */ }
  })();
})();
