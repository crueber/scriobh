/* Scríobh keyboard shortcuts window (F-1250), the part that is fetched when it is first asked for.
   js/palette.js has the keys' words and drawing (S.keys) and the ? key; this opens the page's own #dialog with the fragment from
   /ui/shortcuts, kept for the page. It is fetched here and not with htmx: a sign-in redirect must never replace the page or the text
   in it (the shell would navigate), so anything but a plain answer is a toast. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, K = S && S.keys;
  if (!K) return;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var mac = K.mac, KM = K.map, kh = K.hints, letter = K.letter, shut = K.shut, write = K.write;
  var kbc = {}, kbn = 0;
  // The caps of the window for this computer: the Alt+Shift letters of this layout, the Mac's signs (named in a tooltip) and its own keys.
  function skin(r) {
    Array.prototype.forEach.call(r.querySelectorAll("kbd[data-code]"), function (k) { k.textContent = letter(k.dataset.code, k.dataset.code.slice(3)); });
    if (!mac) return;
    Array.prototype.forEach.call(r.querySelectorAll("kbd[data-k]"), function (k) { k.textContent = KM[k.dataset.k]; k.title = (kh.m || {})[k.dataset.k] || ""; });
    Array.prototype.forEach.call(r.querySelectorAll("[data-os=mac]"), function (a) { a.hidden = false; });
  }
  function open() {
    var dlg = $("#dialog"), body = $("#dialog-body"), here = $("[data-scope]");
    if (!dlg || !body || !here || $("dialog[open]")) return;
    shut();
    // From the account menu the popover has just closed and focus is back on its button (or nowhere, where it is found by hand).
    var back = doc.activeElement, mine = ++kbn;
    if (!back || back === doc.body) back = Array.prototype.filter.call(doc.querySelectorAll('[popovertarget="account"]'), function (b) { return b.offsetParent; })[0] || back;
    // Whether the person can edit comes from the page: the writing view, the outline, the boards and the timeline each say so.
    var ce = $("[data-can-edit],[data-movable]");
    var q = "scope=" + here.dataset.scope + (write ? "&ai=" + (write.dataset.ai === "on" ? 1 : 0) : "") + (ce ? "&edit=" + ((ce.dataset.canEdit || ce.dataset.movable) === "true" ? 1 : 0) : "");
    function show(html) {
      if (mine !== kbn || $("dialog[open]")) return;
      body.replaceChildren.apply(body, new win.DOMParser().parseFromString(html, "text/html").body.childNodes);
      skin(body);
      dlg.setAttribute("aria-labelledby", "dialog-title");
      dlg.showModal();
      var h = $("#dialog-title", body);
      // Tab goes round inside the window in every browser, not only where a modal dialog keeps it.
      function trap(e) {
        if (e.key !== "Tab") return;
        var f = Array.prototype.filter.call(dlg.querySelectorAll("button, a[href]"), function (x) { return !x.disabled && x.offsetParent; }), a = doc.activeElement;
        if (f.length && (e.shiftKey ? a === f[0] || a === h : a === f[f.length - 1])) { e.preventDefault(); f[e.shiftKey ? f.length - 1 : 0].focus(); }
      }
      dlg.addEventListener("keydown", trap);
      h.tabIndex = -1;
      h.focus();
      dlg.addEventListener("close", function () { dlg.removeEventListener("keydown", trap); if (back && back.isConnected && (!doc.activeElement || doc.activeElement === doc.body)) back.focus({ preventScroll: true }); }, { once: true });
    }
    if (kbc[q]) { show(kbc[q]); return; }
    win.fetch("/ui/shortcuts?" + q, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } })
      .then(function (r) { return r.ok && !r.redirected ? r.text() : Promise.reject(); })
      .then(function (h) { show(kbc[q] = h); }, function () { if (S.toast) S.toast(here.dataset.none); });
  }
  S.kbskin = skin;
  S.kbwin = open;
})();
