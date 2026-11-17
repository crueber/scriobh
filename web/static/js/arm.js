/* Scríobh: a confirmation that a double tap or a held Enter cannot get through. An action that cannot be undone (resolving
   everything, accepting everything, removing a person, revoking a link, deleting a comment, withdrawing a suggestion) asks
   first, in the place where the button was: focus lands on Cancel, the confirming button is off for half a second, Escape
   or leaving the panel (or waiting) puts everything back. Three ways in:
     form[data-arm="the question"] (data-arm-ok, data-arm-cancel): the form's submit is held until the person confirms;
     [data-hold]: a server-rendered plan whose confirming button is off for half a second, with [data-first] focused
       and Escape pressing [data-cancel];
     scriobh.arm.ask(host, {msg, ok, cancel, from, onOk}): the same panel for a script (the margin's comment delete). */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh = win.scriobh || {}, WAIT = 500, LIFE = 20000;
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function ask(host, o) {
    var old = host.querySelector(":scope > .arm");
    if (old) { var b0 = old.querySelector("button"); if (b0) b0.focus(); return; }
    var box = el("div", "arm"), p = el("p", "arm-msg", o.msg), no = el("button", "btn small", o.cancel || "Cancel"), yes = el("button", "btn small solid arm-ok", o.ok || "OK"), t1, t2;
    p.id = "arm-" + Math.random().toString(36).slice(2, 8);
    box.setAttribute("role", "group"); box.setAttribute("aria-labelledby", p.id);
    no.type = yes.type = "button"; yes.disabled = true;
    box.appendChild(p); box.appendChild(no); box.appendChild(yes);
    function close(back) {
      win.clearTimeout(t1); win.clearTimeout(t2); box.remove();
      if (back && o.from && o.from.isConnected) o.from.focus();
    }
    no.addEventListener("click", function () { close(true); if (o.onCancel) o.onCancel(); });
    yes.addEventListener("click", function () { if (yes.disabled) return; close(false); o.onOk(); });
    box.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(true); if (o.onCancel) o.onCancel(); } });
    box.addEventListener("focusout", function () { win.setTimeout(function () { if (box.isConnected && !box.contains(doc.activeElement)) close(false); }, 0); });
    host.appendChild(box);
    no.focus();
    t1 = win.setTimeout(function () { yes.disabled = false; }, WAIT);
    t2 = win.setTimeout(function () { close(box.contains(doc.activeElement)); }, LIFE);
    return box;
  }
  S.arm = { ask: ask };
  doc.addEventListener("submit", function (e) {
    var f = e.target;
    if (!f || !f.matches || !f.matches("form[data-arm]") || f.dataset.armed) return;
    e.preventDefault();
    var from = e.submitter || f.querySelector("button");
    ask(f, { msg: f.dataset.arm, ok: f.dataset.armOk, cancel: f.dataset.armCancel, from: from, onOk: function () { f.dataset.armed = "1"; f.requestSubmit(from); } });
  }, true);
  function hold() {
    Array.prototype.forEach.call(doc.querySelectorAll("[data-hold]"), function (b) {
      b.disabled = true;
      win.setTimeout(function () { b.disabled = false; }, WAIT);
      var box = b.closest("section") || b.form;
      if (box) box.addEventListener("keydown", function (e) { var c = box.querySelector("[data-cancel]"); if (e.key === "Escape" && c) { e.preventDefault(); c.click(); } });
    });
    var f = doc.querySelector("[data-first]"); if (f) f.focus();
  }
  if (doc.readyState === "loading") doc.addEventListener("DOMContentLoaded", hold); else hold();
})();
