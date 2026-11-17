/* Scríobh: the publish and connect pages (site, webhooks, connect an agent). Three small things, and the pages work
   without them: the address the server guessed from the request is replaced by this page's own origin wherever a
   snippet names it (nothing knows a hard-coded origin); copy buttons appear and copy what they point at, saying so in
   the page's live region; and a secret shown once gets the focus, so it is read out with its note. Nothing is stored. */
(function () {
  "use strict";
  var doc = document, win = window, root = doc.getElementById("cx");
  if (!root) return;
  var live = doc.getElementById("cx-live"), from = root.dataset.origin, here = win.location.origin;
  function say(m) { if (!live) return; live.textContent = ""; win.setTimeout(function () { live.textContent = m; }, 30); }
  if (from && here && from !== here) {
    Array.prototype.forEach.call(root.querySelectorAll("[data-snippet]"), function (e) {
      if ("value" in e && e.tagName === "INPUT") e.value = e.value.split(from).join(here);
      else e.textContent = e.textContent.split(from).join(here);
    });
  }
  Array.prototype.forEach.call(root.querySelectorAll("[data-copy]"), function (b) {
    var t = doc.querySelector(b.dataset.copy);
    if (!t) return;
    b.hidden = false;
    b.addEventListener("click", function () {
      var text = t.tagName === "INPUT" ? t.value : t.textContent;
      function fallback() {
        var ok = false;
        if (t.tagName === "INPUT") t.select();
        else { var r = doc.createRange(), s = win.getSelection(); r.selectNodeContents(t); s.removeAllRanges(); s.addRange(r); }
        try { ok = doc.execCommand("copy"); } catch (e) { /* the text stays selected */ }
        say(ok ? b.dataset.done : (root.dataset.selected || ""));
      }
      if (win.navigator.clipboard && win.navigator.clipboard.writeText) win.navigator.clipboard.writeText(text).then(function () { say(b.dataset.done); }, fallback);
      else fallback();
    });
  });
  var once = root.querySelector("[data-once]");
  if (once) { once.addEventListener("focus", function () { once.select(); }); once.focus(); }
})();
