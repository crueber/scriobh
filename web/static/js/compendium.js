/* Scríobh Compendium pages: when something else changes the Compendium or the project (a live event from another
   tab or person), the part of the page marked data-live-refresh is fetched again and swapped in. Never while the
   person is typing in the page: then a small "Changed elsewhere" button waits instead, and the swap happens when
   they press it. The page works without this. */
(function () {
  "use strict";
  var doc = document, win = window;
  var timer = 0, waiting = false;
  function box() { return doc.querySelector("[data-live-refresh]"); }
  function typing() {
    var a = doc.activeElement;
    return !!(a && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName) && a.closest("#comp-page, .comp"));
  }
  function note(show) {
    var n = doc.getElementById("comp-stale");
    if (!show) { if (n) n.remove(); return; }
    if (n) return;
    var b = box();
    if (!b) return;
    n = doc.createElement("p");
    n.id = "comp-stale";
    n.className = "stale-note";
    n.setAttribute("role", "status");
    var t = doc.createElement("span");
    t.textContent = "This changed elsewhere. ";
    var btn = doc.createElement("button");
    btn.type = "button";
    btn.className = "btn small";
    btn.textContent = "Refresh the list";
    btn.addEventListener("click", function () { note(false); refresh(true); });
    n.append(t, btn);
    b.parentNode.insertBefore(n, b);
  }
  function refresh(force) {
    var b = box();
    if (!b) return;
    if (!force && typing()) { waiting = true; note(true); return; }
    win.fetch(win.location.pathname + win.location.search, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) { return r.ok ? r.text() : null; }).then(function (html) {
      if (!html) return;
      var fresh = new win.DOMParser().parseFromString(html, "text/html").querySelector("[data-live-refresh]");
      var cur = box();
      if (!fresh || !cur) return;
      if (!force && typing()) { waiting = true; note(true); return; }
      cur.replaceChildren.apply(cur, Array.prototype.slice.call(fresh.childNodes));
      waiting = false;
      note(false);
    }, function () {});
  }
  doc.addEventListener("scriobh:event", function (e) {
    var d = e.detail || {};
    if (d.by_you) return;
    win.clearTimeout(timer);
    timer = win.setTimeout(function () { refresh(false); }, 400);
  });
  doc.addEventListener("focusout", function () { if (waiting) win.setTimeout(function () { if (waiting && !typing()) refresh(false); }, 300); });
})();
