/* Settings: choosing a light or dark theme shows at once (the form's Save button keeps it).
   The server renders the same attributes on <html> from the saved preferences, so a reload
   needs no script: data-lt names the light theme, data-dk the dark one, absent means Paper or Ink. */
(function () {
  "use strict";
  var root = document.documentElement;
  document.addEventListener("change", function (e) {
    var t = e.target, k = t.dataset && t.dataset.themePick;
    if (!k || !t.checked) return;
    var attr = k === "theme_light" ? "data-lt" : "data-dk", dflt = k === "theme_light" ? "paper" : "ink";
    if (t.value === dflt) root.removeAttribute(attr); else root.setAttribute(attr, t.value);
  });
})();
