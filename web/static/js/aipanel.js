/* Scríobh writing view: the AI tab of the inspector, present only when AI is on for the project. Two views, each its own file
   fetched when first shown: Chat (js/aichat.js) and Reviews (js/aireviews.js). The core (js/ai.js) is loaded first; this file
   waits for js/aiboot.js (which may arrive after it) and says plainly, with a way to try again, when a part cannot be loaded. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh;
  var views = { chat: { label: "Chat", mod: "chat" }, reviews: { label: "Reviews", mod: "reviews" } }, panel = null, body = null, tabs = {}, current = "";
  function h(tag, attrs) { var e = doc.createElement(tag); for (var k in attrs || {}) { var v = attrs[k]; if (v == null || v === false) continue; if (k === "text") e.textContent = v; else if (k === "class") e.className = v; else e.setAttribute(k, v === true ? "" : v); } for (var i = 2; i < arguments.length; i++) if (arguments[i] != null) e.appendChild(typeof arguments[i] === "string" ? doc.createTextNode(arguments[i]) : arguments[i]); return e; }
  // The core, whichever of the boot file and this one came first: wait for the boot file for a few seconds, then give up plainly.
  function core(cb, fail) {
    if (S.aiLoad) return S.aiLoad(cb, fail);
    var done = false, t = 0;
    function go() { if (done) return; done = true; win.clearTimeout(t); doc.removeEventListener("scriobh:aiboot", go); if (S.aiLoad) S.aiLoad(cb, fail); else fail(); }
    doc.addEventListener("scriobh:aiboot", go);
    t = win.setTimeout(go, 8000);
  }
  function failed(name) {
    var retry = h("button", { type: "button", class: "btn small", text: "Try again" });
    retry.addEventListener("click", function () { open(name); });
    body.replaceChildren(h("p", { class: "ai-bad", role: "alert", text: "The writing partner could not be loaded. Your text is unchanged." }), h("div", { class: "ai-row" }, retry));
    // Focus goes to the button only if it is still in the panel (or nowhere): a writer typing in the chapter is never pulled out.
    var a = doc.activeElement; if (!a || a === doc.body || (panel && panel.contains(a))) retry.focus();
  }
  function open(name) {
    if (!views[name]) name = "chat";
    current = name;
    Object.keys(tabs).forEach(function (k) { tabs[k].setAttribute("aria-selected", String(k === name)); tabs[k].tabIndex = k === name ? 0 : -1; });
    body.replaceChildren(h("p", { class: "fine", text: "Loading…" }));
    core(function () {
      S.ai.part(views[name].mod, function (m) { if (current === name) m.show(body); }, function () { if (current === name) failed(name); });
    }, function () { if (current === name) failed(name); });
  }
  S.aipanel = {
    show: function (el) {
      if (panel !== el) {
        panel = el;
        var bar = h("div", { class: "ai-subtabs", role: "tablist", "aria-label": "Writing partner" });
        Object.keys(views).forEach(function (k) {
          var b = h("button", { type: "button", role: "tab", class: "btn quiet small", id: "aitab-" + k, text: views[k].label, "aria-selected": "false", tabindex: "-1" });
          b.addEventListener("click", function () { open(k); });
          b.addEventListener("keydown", function (e) { if (e.key === "ArrowRight" || e.key === "ArrowLeft") { var ks = Object.keys(views), i = ks.indexOf(k), n = ks[(i + (e.key === "ArrowRight" ? 1 : ks.length - 1)) % ks.length]; e.preventDefault(); open(n); tabs[n].focus(); } });
          tabs[k] = b; bar.appendChild(b);
        });
        body = h("div", { class: "ai-body", role: "tabpanel" });
        el.replaceChildren(h("h2", { class: "ver-h", text: "Writing partner" }), h("p", { class: "fine", text: "Nothing is sent until you have seen exactly what would be, and nothing it writes enters the chapter without your accept." }), bar, body);
        var want = S.aiWant; S.aiWant = "";
        open(want || current || "chat");
      }
    }
  };
  doc.addEventListener("scriobh:aitab", function (e) { if (panel && views[e.detail]) open(e.detail); else if (views[e.detail]) current = e.detail; });
})();
