/* Scríobh Writing Studio dashboard: the calendar opens at its newest week, and a day's figure can be read by touch, click or
   the arrow keys (the hover title is not the only way to it). Nothing here counts or changes anything. */
(function () {
  "use strict";
  var t = document.querySelector(".st-cal"), box = t && t.closest(".st-scroll");
  if (!t || !box || !t.tBodies[0]) return;
  var rtl = getComputedStyle(box).direction === "rtl", rows = [].slice.call(t.tBodies[0].rows), cur = null;
  box.scrollLeft = rtl ? -box.scrollWidth : box.scrollWidth; // the newest week, which a narrow screen would not show
  var out = document.createElement("p");
  out.className = "fine st-day"; out.setAttribute("role", "status"); out.textContent = t.dataset.hint || "";
  box.parentNode.insertBefore(out, box.nextSibling);
  function name(td) { var n = td && td.querySelector(".sr-only"); return n ? n.textContent : ""; }
  function pick(td) {
    if (!name(td)) return;
    if (cur) cur.classList.remove("cur");
    cur = td; td.classList.add("cur"); out.textContent = name(td);
    td.scrollIntoView({ block: "nearest", inline: "nearest" });
  }
  t.addEventListener("click", function (e) { pick(e.target.closest("td")); });
  box.addEventListener("keydown", function (e) {
    var d = { ArrowLeft: [rtl ? 1 : -1, 0], ArrowRight: [rtl ? -1 : 1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }[e.key];
    if (!d || e.target !== box) return;
    e.preventDefault();
    if (!cur) { pick(t.querySelector("td.now") || t.querySelector("td.cur")); if (cur) return; }
    var r = rows.indexOf(cur.parentNode) + d[1], c = cur.cellIndex + d[0], n;
    while (rows[r] && (n = rows[r].cells[c])) {
      if (name(n)) { pick(n); return; }
      if (d[1]) return; // a day that has not come yet: stay
      c += d[0];
    }
  });
})();
