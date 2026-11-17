/* Picks the download button for the visitor's system. It reads the page's own links and makes no request.
   Without it the page shows every download as a plain list. */
(function () {
  "use strict";
  var box = document.getElementById("dl-primary");
  if (!box) return;

  function system() {
    var uad = navigator.userAgentData;
    var ua = navigator.userAgent || "";
    if (uad && uad.mobile) return "";
    var p = uad && uad.platform ? uad.platform : "";
    if (/^mac/i.test(p)) p = "mac";
    else if (/^win/i.test(p)) p = "windows";
    else if (/^linux/i.test(p)) p = "linux";
    else if (p) return "";
    if (!p) {
      if (/android|iphone|ipad|ipod|cros/i.test(ua)) return "";
      if (/macintosh|mac os x/i.test(ua)) p = "mac";
      else if (/windows/i.test(ua)) p = "windows";
      else if (/linux|x11|bsd/i.test(ua)) p = "linux";
    }
    // iPadOS reports a Mac; it has touch points, a Mac has none
    if (p === "mac" && navigator.maxTouchPoints > 1) return "";
    return p;
  }

  var os = system();
  var main = os && document.querySelector('a[data-primary="' + os + '"]');
  if (!main) {
    var note = document.getElementById("dl-none");
    if (note) note.hidden = false;
    return;
  }
  var names = { mac: "macOS", windows: "Windows", linux: "Linux" };
  var btn = box.querySelector("[data-main]");
  btn.href = main.href;
  btn.textContent = "Download for " + names[os];
  box.querySelector("[data-detail]").textContent = main.getAttribute("data-detail");

  var other = document.querySelector('a[data-secondary="' + os + '"]');
  var alt = box.querySelector("[data-alt]");
  if (other && alt) {
    alt.href = other.href;
    alt.textContent = other.getAttribute("data-label");
    alt.hidden = false;
  }
  var group = document.querySelector('[data-os="' + os + '"]');
  if (group) group.classList.add("is-yours");
  box.hidden = false;
})();
