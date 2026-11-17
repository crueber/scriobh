/* Scríobh outline: the order (manuscript, story timeline, point of view) and which columns show.
   The three orders are real links, so the page works without this file; here they swap the list in place, and what
   the person chose, and which columns they keep, is remembered in this browser. */
(function () {
  "use strict";
  var doc = document, win = window, root = doc.getElementById("outline");
  if (!root) return;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var KEY = "scriobh:outline", saved = { sort: "", cols: null };
  try { var j = JSON.parse(win.localStorage.getItem(KEY) || "null"); if (j) saved = { sort: j.sort || "", cols: Array.isArray(j.cols) ? j.cols : null }; } catch (e) { /* defaults */ }
  function keep() { try { win.localStorage.setItem(KEY, JSON.stringify(saved)); } catch (e) { /* this page only */ } }
  function say(m) { var l = $("#o-live"); if (l) { l.textContent = ""; win.setTimeout(function () { l.textContent = m; }, 40); } }
  var WORDS = { order: "manuscript order", timeline: "story timeline order", pov: "point of view order" };

  // Columns: the person's own choice wins; without one, what the server drew for the order.
  function list() { return $("#o-list"); }
  function cols() { var l = list(); return l ? (saved.cols ? saved.cols.join(" ") : l.dataset.cols || "") : ""; }
  function apply() {
    var l = list(), c = $("#o-colsel"), on = cols().split(" ");
    if (l) l.dataset.cols = cols();
    if (c) {
      c.hidden = false;
      Array.prototype.forEach.call(c.querySelectorAll("input[name=col]"), function (i) { i.checked = on.indexOf(i.value) >= 0; });
    }
  }
  function swapped() { apply(); doc.dispatchEvent(new win.CustomEvent("scriobh:outline-swapped")); }

  var asked = 0;
  function load(a, fromSaved) {
    var sort = a.dataset.sortLink, mine = ++asked;
    return win.fetch(a.dataset.frag, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) {
      if (!r.ok) throw r;
      return r.text();
    }).then(function (t) {
      if (mine !== asked) return;
      if ($(".orow-edit", root)) { say("Finish or cancel the edit first."); return; }
      $("#o-body").innerHTML = t;
      win.history.replaceState(null, "", a.href);
      if (!fromSaved) { saved.sort = sort; keep(); }
      swapped();
      var now = $('[data-sort-link="' + sort + '"]');
      if (now && !fromSaved) now.focus();
      say("Showing the outline in " + WORDS[sort] + ".");
    }, function () { say("Could not change the order. Try again."); });
  }
  root.addEventListener("click", function (e) {
    var a = e.target.closest && e.target.closest("[data-sort-link]");
    if (!a || e.ctrlKey || e.metaKey || e.shiftKey || e.button) return;
    e.preventDefault();
    if (a.getAttribute("aria-current")) return;
    load(a, false);
  });
  root.addEventListener("change", function (e) {
    if (!e.target.matches || !e.target.matches("input[name=col]")) return;
    saved.cols = Array.prototype.map.call(root.querySelectorAll("input[name=col]:checked"), function (i) { return i.value; });
    keep(); apply();
    say((e.target.checked ? "Showing " : "Hiding ") + e.target.parentNode.textContent.trim().toLowerCase() + ".");
  });
  apply();
  // A saved order is applied on arrival unless the address names one.
  if (saved.sort && saved.sort !== "order" && !/[?&]sort=/.test(win.location.search)) {
    var a = $('[data-sort-link="' + saved.sort + '"]');
    if (a && !a.getAttribute("aria-current")) load(a, true);
  }
})();
