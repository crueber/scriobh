/* Scríobh Git sync page: the copy button for the deploy key, and a quiet check every few seconds while a push is
   running, which stops while the tab is hidden and ends when the push does. The page works without it. */
(function () {
  "use strict";
  var doc = document, win = window;
  var announce = doc.getElementById("git-announce");
  function say(m) {
    if (!announce) return;
    announce.textContent = "";
    win.setTimeout(function () { announce.textContent = m; }, 30);
  }
  Array.prototype.forEach.call(doc.querySelectorAll("[data-copy]"), function (b) {
    var t = doc.querySelector(b.dataset.copy);
    if (!t) return;
    b.hidden = false;
    b.addEventListener("click", function () {
      var text = t.textContent.trim();
      function fallback() {
        var r = doc.createRange(), ok = false, s = win.getSelection();
        r.selectNodeContents(t);
        s.removeAllRanges();
        s.addRange(r);
        try { ok = doc.execCommand("copy"); } catch (e) { /* the key stays selected */ }
        say(ok ? b.dataset.copied : b.dataset.selected);
      }
      if (win.navigator.clipboard && win.navigator.clipboard.writeText) win.navigator.clipboard.writeText(text).then(function () { say(b.dataset.copied); }, fallback);
      else fallback();
    });
  });

  // After a form is answered, the result is what the person needs to see: bring the first one into view and give it focus.
  var res = doc.querySelector("[data-result]");
  if (res) res.focus();

  var box = doc.getElementById("git-live");
  if (!box) return;
  var timer = 0, headline = (doc.getElementById("git-headline") || {}).textContent || "";
  function schedule() {
    win.clearTimeout(timer);
    if (!box || box.dataset.poll !== "true" || doc.hidden) return;
    timer = win.setTimeout(poll, 3000);
  }
  // After the area is redrawn, focus goes back to the same control, found by its id or by the form it belongs to.
  function marker(a) {
    if (!a || !box.contains(a)) return null;
    if (a.id) return { id: a.id };
    var f = a.form;
    return f ? { action: f.getAttribute("action"), text: a.textContent } : null;
  }
  function refocus(m, root) {
    var el = null;
    if (m && m.id) el = doc.getElementById(m.id);
    else if (m) Array.prototype.forEach.call(root.querySelectorAll("form"), function (f) { if (f.getAttribute("action") === m.action) { var b = f.querySelector("button, input, select"); if (b && !el) el = b; } });
    if (!el) el = root.querySelector("#h-status");
    if (el) { if (!el.hasAttribute("tabindex") && !/^(A|BUTTON|INPUT|SELECT|TEXTAREA)$/.test(el.tagName)) el.tabIndex = -1; el.focus({ preventScroll: true }); }
  }
  function poll() {
    win.fetch(box.dataset.src, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1", "X-Scriobh-Background": "1" } }).then(function (r) { return r.ok ? r.text() : (r.status === 401 || r.status === 403 || r.status === 404 ? false : null); }).then(function (html) {
      if (html === false) return; // signed out, no access, or the remote is gone: stop polling
      if (html === null) { schedule(); return; }
      var t = doc.createElement("template");
      t.innerHTML = html;
      var n = t.content.firstElementChild;
      if (!n || n.id !== "git-live") { schedule(); return; }
      var m = marker(doc.activeElement);
      box.replaceWith(n);
      box = n;
      if (m) refocus(m, box);
      var h = (doc.getElementById("git-headline") || {}).textContent || "";
      if (h && h !== headline) say(h);
      headline = h;
      schedule();
    }, schedule);
  }
  doc.addEventListener("visibilitychange", function () {
    if (doc.hidden) win.clearTimeout(timer); else if (box.dataset.poll === "true") poll();
  });
  schedule();
})();
