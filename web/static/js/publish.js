/* Scríobh release page: while an edition is being built and uploaded, ask for the page's own fragment every two seconds and
   put it in place, keeping focus where it was. It stops when the job ends, when the tab is hidden, and on any answer that is
   not the fragment. The page works without it (reload to see the job). */
(function () {
  "use strict";
  var doc = document, win = window, box = doc.getElementById("rel-live"), timer = 0;
  // After a form is answered, the result is what the person needs to see: bring the first one into view and give it focus.
  var res = doc.querySelector("[data-result]");
  if (res) res.focus();
  if (!box) return;
  var announce = doc.getElementById("rel-announce"), last = (doc.getElementById("rel-headline") || {}).textContent || "";
  function schedule() {
    win.clearTimeout(timer);
    if (!box || box.dataset.poll !== "true" || doc.hidden || !box.dataset.src) return;
    timer = win.setTimeout(poll, 2000);
  }
  function poll() {
    win.fetch(box.dataset.src, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) { return r.ok ? r.text() : (r.status === 401 || r.status === 403 || r.status === 404 ? false : null); }).then(function (html) {
      if (html === false) return;
      if (html === null) { schedule(); return; }
      var t = doc.createElement("template");
      t.innerHTML = html;
      var n = t.content.firstElementChild;
      if (!n || n.id !== "rel-live") { schedule(); return; }
      var a = doc.activeElement, id = a && box.contains(a) ? a.id : "";
      box.replaceWith(n);
      box = n;
      if (id && doc.getElementById(id)) doc.getElementById(id).focus({ preventScroll: true });
      var h = (doc.getElementById("rel-headline") || {}).textContent || "";
      if (h && h !== last && announce) { announce.textContent = ""; win.setTimeout(function () { announce.textContent = h; }, 30); }
      last = h;
      schedule();
    }, schedule);
  }
  doc.addEventListener("visibilitychange", function () { if (doc.hidden) win.clearTimeout(timer); else if (box.dataset.poll === "true") poll(); });
  schedule();
})();
