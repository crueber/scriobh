/* Scríobh: register the service worker and offer an update. Nothing here
   runs in the writing path, and a browser without service workers, or one that
   refuses (a private window, a blocked context), simply goes without. */
(function () {
  "use strict";
  if (!("serviceWorker" in navigator)) return;
  var asked = false;

  // A new version waits until the person agrees; the page then reloads onto it.
  function offer(worker) {
    var box = document.getElementById("toasts");
    if (!box || document.getElementById("pwa-update")) return;
    var t = document.createElement("div"), s = document.createElement("span");
    t.className = "toast";
    t.id = "pwa-update";
    s.textContent = "A new version of Scríobh is ready.";
    t.appendChild(s);
    var go = document.createElement("button"), later = document.createElement("button");
    go.type = later.type = "button";
    go.textContent = "Reload";
    later.textContent = "Later";
    go.addEventListener("click", function () { asked = true; t.remove(); worker.postMessage({ type: "SKIP_WAITING" }); });
    later.addEventListener("click", function () { t.remove(); });
    t.appendChild(go);
    t.appendChild(later);
    box.appendChild(t);
    document.dispatchEvent(new CustomEvent("scriobh:update-ready", { detail: { apply: function () { asked = true; worker.postMessage({ type: "SKIP_WAITING" }); } } }));
  }

  navigator.serviceWorker.addEventListener("controllerchange", function () { if (asked) window.location.reload(); });
  navigator.serviceWorker.register("/sw.js", { scope: "/" }).then(function (reg) {
    if (reg.waiting && navigator.serviceWorker.controller) offer(reg.waiting);
    reg.addEventListener("updatefound", function () {
      var w = reg.installing;
      if (!w) return;
      w.addEventListener("statechange", function () {
        if (w.state === "installed" && navigator.serviceWorker.controller) offer(w);
      });
    });
  }).catch(function () { /* no worker: the app works without one */ });
})();
