/* Scríobh writing view: the way in to the writing partner. This file is on the page only when AI is on for the project and the
   person may edit it; it does nothing until an AI control is used (the toolbar button, the palette, Alt+Shift+I), and only then
   fetches the rest (js/ai.js and its stylesheet). With AI off none of this is loaded and no control exists. A part that asks for
   the core before this file has arrived waits for the scriobh:aiboot event; one that is told the core could not be loaded can
   offer a retry. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, box = doc.getElementById("write");
  if (!S || !box || box.dataset.ai !== "on") return;
  var waiting = [], loading = false;
  var chords = (S.chords = S.chords || { clash: [] }); // every Alt+Shift chord of the writing view is claimed in one table (see js/alts.js)
  if (chords.KeyI && chords.KeyI !== "ai: the menu") chords.clash.push("KeyI: " + chords.KeyI + " and ai: the menu");
  chords.KeyI = "ai: the menu";
  // Run `cb` once the core is loaded (the first use loads it); `fail` when it could not be.
  S.aiLoad = function (cb, fail) {
    if (S.ai && S.ai.ready) return cb();
    waiting.push({ cb: cb, fail: fail });
    if (loading) return;
    loading = true;
    if (!doc.querySelector('link[href="' + box.dataset.aiCss + '"]')) { var l = doc.createElement("link"); l.rel = "stylesheet"; l.href = box.dataset.aiCss; doc.head.appendChild(l); }
    var s = doc.createElement("script"); s.src = box.dataset.aicoreSrc;
    function failed() {
      loading = false; s.remove();
      var q = waiting; waiting = [];
      if (S.toast) S.toast("The writing partner could not be loaded. Your text is unchanged.");
      q.forEach(function (w) { if (w.fail) w.fail(); });
    }
    s.onload = function () { if (!(S.ai && S.ai.ready)) return failed(); loading = false; var q = waiting; waiting = []; q.forEach(function (w) { w.cb(); }); };
    s.onerror = failed;
    doc.head.appendChild(s);
  };
  doc.addEventListener("scriobh:ai", function (e) { S.aiLoad(function () { S.ai.cmd(e.detail); }); });
  doc.addEventListener("keydown", function (e) {
    if (e.altKey && e.shiftKey && !e.ctrlKey && !e.metaKey && e.code === "KeyI" && !e.isComposing && doc.activeElement && doc.activeElement.closest && doc.activeElement.closest(".scriobh-prose")) {
      e.preventDefault(); e.stopPropagation();
      doc.dispatchEvent(new win.CustomEvent("scriobh:ai", { detail: "menu" }));
    }
  }, true);
  doc.dispatchEvent(new win.CustomEvent("scriobh:aiboot"));
})();
