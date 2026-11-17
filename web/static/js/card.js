/* Scríobh: the progress card page. Once, when a card has just been made: this server's own address in front of the path
   the server gave (it does not guess its public address), a copy button, and the picture as a PNG, drawn here from the
   card's SVG on a canvas (the server has no rasteriser). The address is in this page only until it is left; nothing
   here stores it. Without this script the page shows the path and offers the SVG. */
(function () {
  "use strict";
  var doc = document, win = window, box = doc.getElementById("card-url");
  if (!box) return;
  box.value = win.location.origin + box.dataset.path;
  var n = doc.querySelector("[data-nojs]"); if (n) n.hidden = true;
  box.addEventListener("focus", function () { box.select(); });
  box.focus(); // the form that asked for this card made this page: focus goes to the link, read out with its note
  var btn = doc.querySelector("[data-copy]");
  if (btn) {
    btn.hidden = false;
    btn.addEventListener("click", function () {
      var t = btn.textContent, done = function () { btn.textContent = btn.dataset.copied || t; win.setTimeout(function () { btn.textContent = t; }, 1800); };
      if (win.navigator.clipboard && win.navigator.clipboard.writeText) win.navigator.clipboard.writeText(box.value).then(done, function () { box.select(); });
      else { box.select(); try { doc.execCommand("copy"); done(); } catch (e) { /* the text is selected for the person to copy */ } }
    });
  }
  var png = doc.querySelector("[data-card-png]"), err = doc.getElementById("card-png-err");
  if (!png || !doc.createElement("canvas").getContext) return;
  png.hidden = false;
  png.addEventListener("click", function () {
    var img = new win.Image(), fail = function () { if (err) err.hidden = false; };
    if (err) err.hidden = true;
    img.onerror = fail;
    img.onload = function () {
      try {
        var c = doc.createElement("canvas");
        c.width = 1200; c.height = 630; // twice the card's 600 by 315
        c.getContext("2d").drawImage(img, 0, 0, c.width, c.height);
        c.toBlob(function (b) {
          if (!b) { fail(); return; }
          var a = doc.createElement("a"), u = win.URL.createObjectURL(b);
          a.href = u; a.download = (png.dataset.name || "progress") + ".png";
          doc.body.appendChild(a); a.click(); a.remove();
          win.setTimeout(function () { win.URL.revokeObjectURL(u); }, 5000);
        }, "image/png");
      } catch (e) { fail(); }
    };
    img.src = png.dataset.cardPng;
  });
})();
