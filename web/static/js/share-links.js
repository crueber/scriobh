/* Scríobh: the share links page. Once, when a link has just been made: put this installation's address in front of the
   path the server gave (the server does not guess its public address), and copy it. The link is in this page only until
   it is left; nothing here stores it. */
(function () {
  "use strict";
  var doc = document, win = window, box = doc.getElementById("shl-url"), btn = doc.getElementById("shl-copy");
  if (!box) return;
  box.value = win.location.origin + box.dataset.path;
  var n = doc.querySelector("[data-nojs]"); if (n) n.hidden = true;
  box.addEventListener("focus", function () { box.select(); });
  box.focus(); // the page was just made by the form that asked for this link: focus goes to the link, which is read out with its note
  if (!btn) return;
  btn.hidden = false;
  btn.addEventListener("click", function () {
    var done = function () { var t = btn.textContent; btn.textContent = btn.dataset.done || t; win.setTimeout(function () { btn.textContent = t; }, 1800); };
    if (win.navigator.clipboard && win.navigator.clipboard.writeText) win.navigator.clipboard.writeText(box.value).then(done, function () { box.select(); });
    else { box.select(); try { doc.execCommand("copy"); done(); } catch (e) { /* the text is selected for the person to copy */ } }
  });
  btn.dataset.done = btn.getAttribute("data-copied") || "";
})();
