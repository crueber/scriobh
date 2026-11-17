/* Scríobh entity bible: the Markdown preview on the sheet, and the live refresh of the list. The pages work without
   it. A live change refreshes the list of links, and never replaces a form: on a sheet it only says the entity
   changed elsewhere and offers a reload, so nothing the person is typing is touched. */
(function () {
  "use strict";
  var doc = document, win = window, $ = function (id) { return doc.getElementById(id); };
  var live = $("ent-announce");
  function say(m) {
    if (!live) return;
    live.textContent = "";
    win.setTimeout(function () { live.textContent = m; }, 40);
  }

  // A refusal is shown where the form is; bring it into view and to the reader's attention.
  var er = $("ent-error");
  if (er) er.focus();

  var pv = $("es-preview"), ta = $("es-body"), out = $("es-prev");
  if (pv && ta && out) {
    pv.hidden = false;
    pv.setAttribute("aria-expanded", "false");
    pv.addEventListener("click", function () {
      if (!out.hidden) {
        out.hidden = true;
        pv.setAttribute("aria-expanded", "false");
        pv.textContent = "Preview";
        return;
      }
      var body = new win.URLSearchParams();
      body.set("body", ta.value);
      body.set("_csrf", pv.dataset.csrf || "");
      pv.disabled = true;
      win.fetch(pv.dataset.src, { method: "POST", credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1", "Content-Type": "application/x-www-form-urlencoded" }, body: body.toString() })
        .then(function (r) { return r.ok ? r.text() : Promise.reject(); })
        .then(function (h) {
          out.innerHTML = h;
          out.hidden = false;
          pv.setAttribute("aria-expanded", "true");
          pv.textContent = "Hide the preview";
        }, function () { say("The preview could not be made. Your text is untouched."); })
        .then(function () { pv.disabled = false; });
    });
  }

  var list = $("ent-list"), sheet = $("sheet"), timer = 0, busy = false;
  var relevant = /entity|override|link|compendium/;
  var queued = false;
  function refreshList() {
    if (!list || !list.dataset.src) return;
    if (busy) { queued = true; return; } // a change that came while the list was being fetched asks for one more fetch
    busy = true;
    win.fetch(list.dataset.src, { credentials: "same-origin", cache: "no-cache", headers: { "X-Scriobh-Fetch": "1" } })
      .then(function (r) { return r.ok ? r.text() : Promise.reject(); })
      .then(function (h) {
        var before = ($("ent-count") || {}).textContent;
        if (h.trim() !== list.innerHTML.trim()) {
          // The row that has focus is found again by its id, and the page stays where it was: a keyboard user is not
          // sent back to the top whenever someone else changes an entity.
          var a = doc.activeElement, keep = a && list.contains(a) ? a.id : "", y = win.scrollY;
          list.innerHTML = h;
          var k = keep && $(keep);
          if (k) k.focus({ preventScroll: true });
          win.scrollTo(0, y);
          var after = ($("ent-count") || {}).textContent;
          if (after !== before) say("The list changed. " + (after || "").trim());
        }
      }, function () { /* stay as it was */ })
      .then(function () { busy = false; if (queued) { queued = false; win.clearTimeout(timer); timer = win.setTimeout(refreshList, 400); } });
  }
  function changedNote() {
    var n = $("ent-changed");
    if (!n || !n.hidden) return;
    n.textContent = "This entity changed elsewhere. What you have typed here is untouched. ";
    var b = doc.createElement("button");
    b.type = "button";
    b.className = "btn small";
    b.textContent = "Reload the sheet";
    b.addEventListener("click", function () { win.location.reload(); });
    n.appendChild(b);
    n.hidden = false;
  }
  doc.addEventListener("scriobh:event", function (ev) {
    var d = ev.detail || {};
    if (!relevant.test(String(d.type || ""))) return;
    // The list follows every change, the person's own from another tab included (by_you cannot tell two tabs of one
    // person apart, and a list that did not change is left alone).
    if (list) {
      win.clearTimeout(timer);
      timer = win.setTimeout(refreshList, 400);
    }
    if (sheet && !d.by_you) {
      var id = d.payload && d.payload.entity_id;
      if (!id || id === sheet.dataset.entity) changedNote();
    }
  });

  // "Where this appears" says it is still looking while the mention index catches up: ask again a few times, and
  // swap only that section, so nothing being typed on the page is touched.
  var where = $("where"), tries = 0;
  function again() {
    if (!where || !where.dataset.indexing || tries++ >= 6) return;
    win.setTimeout(function () {
      win.fetch(win.location.pathname + win.location.search, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } })
        .then(function (r) { return r.ok ? r.text() : Promise.reject(); })
        .then(function (h) {
          var t = doc.createElement("template"), n;
          t.innerHTML = h;
          n = t.content.getElementById("where");
          if (n && where.isConnected) {
            if (n.innerHTML.trim() !== where.innerHTML.trim()) { where.replaceWith(n); say("Where this appears has changed."); }
            where = n;
          }
          again();
        }, function () { /* stay as it was */ });
    }, 3000);
  }
  again();
})();
