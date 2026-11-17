/* Scríobh research list and board: puts focus back after a change, moves a board card with Left and Right,
   and refreshes the list or board when someone else changes it, without touching anything being typed.
   Every action also works as a plain form with no script. */
(function () {
  "use strict";
  var doc = document, root = doc.getElementById("research");
  if (!root) return;
  var live = doc.getElementById("r-live");
  function say(t) { if (live) { live.textContent = ""; setTimeout(function () { live.textContent = t; }, 30); } }
  function csrf() { var i = root.querySelector("input[name=_csrf]"); return i ? i.value : ""; }
  // The ETag of the last copy drawn: a list nobody changed answers 304 and is left alone.
  var tag = "";
  function fetchText(url) {
    return fetch(url, { credentials: "same-origin", cache: "no-cache", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) {
      if (!r.ok) return null;
      var t = r.headers.get("ETag") || "";
      if (t && t === tag) return null;
      tag = t;
      return r.text();
    }, function () { return null; });
  }
  function errText(r) {
    return r.json().then(function (j) { return (j && ((j.error && j.error.message) || j.message)) || "That did not work. Try again."; }, function () { return "That did not work. Try again."; });
  }

  // Focus: after a move or a board change the server sends us back with #n-ID or #c-ID.
  var id = location.hash.slice(1), el = id && doc.getElementById(decodeURIComponent(id));
  if (el) { var f = el.querySelector(".r-name, .r-move button:not([disabled])"); (f && el.classList.contains("r-card") ? f : el).focus(); } else {
    var h = doc.getElementById("r-title"); if (h && /[?&](saved|e)=/.test(location.search)) h.focus();
  }

  // Replace a list or board with a fresh copy, unless a field inside it holds a value the person is editing.
  // Changes arrive in bursts (a merge, a tag rename): they are gathered for 400 ms, a change that arrives while the
  // list is being fetched asks for one more fetch after it, and the place the person was at (the focused row, the scroll)
  // is put back.
  var busy = false, again = false, timer = 0;
  function dirty(box) {
    var a = doc.activeElement;
    return !!(a && box.contains(a) && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName) && a.matches(":focus"));
  }
  function refresh(box) {
    if (!box || !box.dataset.src || dirty(box)) return;
    if (busy) { again = true; return; }
    busy = true;
    var a = doc.activeElement, host = a && box.contains(a) && a.closest("[id^='n-'],[id^='c-']"), keep = host ? host.id : "";
    var kind = host && a !== host ? a.className.split(" ")[0] : "", y = window.scrollY;
    fetchText(box.dataset.src).then(function (html) {
      busy = false;
      var cur = doc.getElementById(box.id);
      if (html && cur && !dirty(cur)) {
        var t = doc.createElement("template"); t.innerHTML = html;
        var next = t.content.getElementById(box.id);
        if (next) {
          cur.replaceWith(next);
          var k = keep && doc.getElementById(keep); k = k && ((kind && k.querySelector("." + kind)) || k);
          if (k) k.focus({ preventScroll: true });
          window.scrollTo(0, y);
        }
      }
      if (again) { again = false; schedule(); }
    });
  }
  function box() { return doc.getElementById("r-tree") || doc.getElementById("r-board"); }
  function schedule() { clearTimeout(timer); timer = setTimeout(function () { refresh(box()); }, 400); }
  doc.addEventListener("scriobh:event", function (e) {
    var d = e.detail || {}, k = d.type || d.kind || "";
    if (k && !/research|compendium|project/.test(k)) return;
    schedule();
  });

  // Board: Left and Right move the card whose title has focus, with a spoken result.
  function post(form) {
    return fetch(form.action, { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": csrf(), "X-Scriobh-Fetch": "1" }, body: new URLSearchParams(new FormData(form)) });
  }
  function moveCard(form, to) {
    var card = form.closest(".r-card"), cardID = card.id, name = card.querySelector(".r-name").textContent.trim();
    form.elements.to.value = to;
    var label = form.elements.to.selectedOptions[0].textContent;
    var undo = function () { form.elements.to.value = form.elements.from.value; }; // a move that did not happen leaves the box where the card is
    post(form).then(function (r) {
      if (!r.ok) { undo(); return errText(r).then(function (m) { say(m); }); }
      var stale = /[?&]e=stale/.test(r.url);
      return r.text().then(function () {
        var b = doc.getElementById("r-board");
        return fetchText(b.dataset.src).then(function (html) {
          if (html) {
            var t = doc.createElement("template"); t.innerHTML = html;
            var next = t.content.getElementById("r-board"); if (next) b.replaceWith(next);
          }
          var c = doc.getElementById(cardID), a = c && c.querySelector(".r-name"); if (a) a.focus();
          say(stale ? "Not moved: " + name + " was changed elsewhere, so the board has been refreshed." : name + " moved to " + label + ".");
        });
      });
    }, function () { undo(); say("Not moved: the server could not be reached. Nothing was changed."); });
  }
  doc.addEventListener("keydown", function (e) {
    if ((e.key !== "ArrowLeft" && e.key !== "ArrowRight") || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    var a = e.target.closest && e.target.closest(".r-card .r-name"); if (!a) return;
    var form = a.closest(".r-card").querySelector(".r-moveto"); if (!form) return;
    var cols = [].map.call(doc.querySelectorAll(".r-col"), function (c) { return c.dataset.key; });
    var i = cols.indexOf(form.elements.from.value) + (e.key === "ArrowRight" ? 1 : -1);
    e.preventDefault();
    if (i < 0 || i >= cols.length) { say(i < 0 ? "This is the first column." : "This is the last column."); return; }
    moveCard(form, cols[i]);
  });
  doc.addEventListener("submit", function (e) {
    var form = e.target; if (!form.matches || !form.matches(".r-moveto")) return;
    e.preventDefault();
    if (form.elements.to.value !== form.elements.from.value) moveCard(form, form.elements.to.value);
  });
}());
