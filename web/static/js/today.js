/* Scríobh writing view: the small "today" figure in the status bar. It reads stats.today a moment after a
   save lands and writes the answer into a place of fixed width, so nothing around it moves. Words added
   only: a day of cutting shows no number against the writer, and no goal means no goal wording. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  var out = doc.getElementById("st-today");
  if (!W || !out) return;
  var timer = 0, last = "";
  function fmt(n) { return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }
  function draw(t) {
    var s;
    if (!t.words_added && !t.has_goal) s = "";
    else if (t.has_goal) s = "Today " + fmt(t.words_added) + " of " + fmt(t.target) + (t.hit ? ", goal met" : "");
    else s = "Today " + fmt(t.words_added) + (t.words_added === 1 ? " word" : " words");
    if (s === last) return;
    last = s;
    out.textContent = s;
    out.hidden = !s;
    out.dataset.hit = t.hit ? "1" : "";
    if (t.has_goal) { out.title = "Words added today toward your daily goal"; }
  }
  function load() {
    W.api("GET", "/api/v1/projects/" + W.cfg.project + "/stats/today").then(function (r) { if (r.ok && r.json) draw(r.json.item || r.json); }, function () {});
  }
  doc.addEventListener("scriobh:saved", function () { clearTimeout(timer); timer = setTimeout(load, 2500); });
  load();
})();
