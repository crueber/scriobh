/* Scríobh: times written by the server in UTC (so the page reads without a script) are shown as the person's own: their time
   zone and language, the same short form the reading view's margin uses, with the full time in the title. */
(function () {
  "use strict";
  var dl = document.documentElement.lang || "", lang = (navigator.language || "").slice(0, 2) === dl.slice(0, 2) ? navigator.language : dl || undefined; // the browser's own form of the interface's language
  Array.prototype.forEach.call(document.querySelectorAll("time[datetime]"), function (t) {
    var d = new Date(t.getAttribute("datetime"));
    if (isNaN(d)) return;
    var f = function (o) { try { return d.toLocaleString(lang, o); } catch (e) { return d.toLocaleString(undefined, o); } }; // a test locale is no tag the browser knows
    try { t.textContent = f({ dateStyle: "medium", timeStyle: "short" }); t.title = f({ dateStyle: "full", timeStyle: "long" }); } catch (e) { /* the UTC text stays */ }
  });
})();
