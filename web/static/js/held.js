/* Scríobh: a local draft for every field of a page of a project that has a passphrase (F-252).
   A project can lock under a page at any moment, and the page then leaves for the unlock page: whatever was typed into a field and
   not yet saved must survive that, and a closed tab, in this browser. This watches every field a person edits (the inspector's, an
   outline's inline edit, a comment or a suggestion, a form) except the ones that keep drafts of their own (the chapter's text and
   title, the notes and the scratchpad). It writes what is not saved yet to this browser, a moment after the last key and never in the
   way of typing, and takes a field off the list when a request that carried its text was answered well (a fetch, an htmx request,
   a submitted form). After the unlock the page that comes back puts each draft back into its field through the field's own
   handlers, so it is saved by the page's ordinary path, and says so; a draft with no field left to go into is shown, to copy or
   discard. The drafts are plain text in this browser's storage, not encrypted, until they are saved; signing out removes them.
   Strings come from the page (data-msg of the cover). */
(function () {
  "use strict";
  var doc = document, win = window, S = (win.scriobh = win.scriobh || {});
  var um = doc.querySelector('meta[name="scriobh-user"]'), cover = doc.getElementById("gate-cover");
  if (!um || !cover) return;
  var M = {};
  try { M = JSON.parse(cover.dataset.msg || "{}"); } catch (e) { M = {}; }
  function T(k, d) { return M[k] || d; }
  var KEY = "scriobh:fields:" + um.content + ":" + win.location.pathname;
  var SKIP = ".scriobh-prose, #doc-title, #palette, #gate-cover, #dialog, form[data-pad], form[method=get], [data-nodraft]";
  var pending = {}, timer = 0;

  function keep(v) { try { if (v === undefined) return win.localStorage.getItem(KEY); if (v === null) win.localStorage.removeItem(KEY); else win.localStorage.setItem(KEY, v); } catch (e) { /* storage is off: the page behind still has the text */ } }
  // A field is found again by its id, or by the nearest named ancestor and its own name.
  function sel(el) {
    if (el.id) return "#" + (win.CSS && win.CSS.escape ? win.CSS.escape(el.id) : el.id);
    var a = el.closest("[data-id],[data-doc],[data-card],[id]"), p = "";
    if (a && a !== el) p = a.tagName.toLowerCase() + (a.id ? "#" + a.id : "[" + (a.dataset.id ? "data-id" : a.dataset.doc ? "data-doc" : "data-card") + '="' + (a.dataset.id || a.dataset.doc || a.dataset.card) + '"]') + " ";
    var f = el.form && el.form.getAttribute("action");
    return (f ? 'form[action="' + f + '"] ' : p) + el.tagName.toLowerCase() + (el.name ? '[name="' + el.name + '"]' : "");
  }
  function label(el) {
    var l = el.labels && el.labels[0];
    return (l && l.textContent.trim()) || el.getAttribute("aria-label") || el.getAttribute("placeholder") || el.name || "";
  }
  function watched(el) {
    if (!el || !el.matches || !el.matches("input, textarea, select") || el.matches(SKIP) || el.closest(SKIP)) return false;
    return !/^(password|hidden|file|submit|button|reset|search|image)$/.test(el.type || "") && !el.readOnly && !el.disabled;
  }
  function value(el) { return el.type === "checkbox" || el.type === "radio" ? (el.checked ? "1" : "0") : el.value; }
  function write() {
    timer = 0;
    var f = Object.keys(pending).map(function (k) { return pending[k]; });
    keep(f.length ? JSON.stringify({ f: f, t: Date.now() }) : null);
  }
  function later() { win.clearTimeout(timer); timer = win.setTimeout(write, 500); }
  function edited(e) {
    var el = e.target;
    if (!watched(el)) return;
    var k = sel(el);
    // Back to what the page drew with is no change at all.
    if (el.type !== "checkbox" && el.type !== "radio" && el.value === el.defaultValue && !pending[k]) return;
    pending[k] = { s: k, v: value(el), l: label(el), y: el.type };
    later();
  }
  doc.addEventListener("input", edited, true);
  doc.addEventListener("change", edited, true);

  // A request that carried a field's text and came back well has saved it.
  function carried(body) {
    var s = "";
    try { s = typeof body === "string" ? body : body && body.constructor && /FormData|URLSearchParams/.test(body.constructor.name) ? Array.from(body.entries ? body.entries() : []).map(function (x) { return x[1]; }).join("\u0000") : ""; } catch (e) { return; }
    if (!s) return;
    Object.keys(pending).forEach(function (k) {
      var v = pending[k].v;
      if (!v || s.indexOf(v) >= 0 || s.indexOf(JSON.stringify(v).slice(1, -1)) >= 0 || s.indexOf(encodeURIComponent(v).replace(/%20/g, "+")) >= 0 || s.indexOf(encodeURIComponent(v)) >= 0) delete pending[k];
    });
    later();
  }
  var nf = win.fetch;
  if (nf) win.fetch = function (input, init) {
    var body = init && init.body, p = nf.apply(this, arguments);
    if (body) p.then(function (r) { if (r.ok) carried(body); }, function () { /* not sent: it stays */ });
    return p;
  };
  doc.addEventListener("htmx:afterRequest", function (e) {
    var d = e.detail;
    if (d && d.successful && d.requestConfig && d.requestConfig.parameters) carried(JSON.stringify(d.requestConfig.parameters));
  });
  doc.addEventListener("submit", function (e) {
    var f = e.target;
    if (!f || !f.elements) return;
    Object.keys(pending).forEach(function (k) { var el = doc.querySelector(k); if (el && el.form === f) delete pending[k]; });
    later();
  }, true);
  // Locking: the page is about to leave. Write now.
  doc.addEventListener("scriobh:gate", function (e) { if (e.detail.state === "locked") { win.clearTimeout(timer); write(); } });
  win.addEventListener("pagehide", write);
  // The chapter was taken over from a tab that had held it: what the lease had kept the fields from saving goes again, through their own handlers.
  doc.addEventListener("scriobh:lease", function () {
    Object.keys(pending).forEach(function (k) {
      var el = null;
      try { el = doc.querySelector(k); } catch (e) { el = null; }
      if (el && watched(el)) { el.dispatchEvent(new win.Event("input", { bubbles: true })); el.dispatchEvent(new win.Event("change", { bubbles: true })); }
    });
  });
  S.held = { count: function () { return Object.keys(pending).length; } };

  // ---- coming back -----------------------------------------------------------------------------------------------
  function notice(text, left) {
    var main = doc.getElementById("main");
    if (!main) return;
    var b = doc.createElement("div"), p = doc.createElement("p");
    b.className = "held-note"; b.setAttribute("role", "status");
    p.textContent = text; b.appendChild(p);
    if (left.length) {
      var d = doc.createElement("details"), s = doc.createElement("summary");
      d.open = true; s.textContent = T("held_left", "Kept text that has no field to go back to:"); d.appendChild(s);
      left.forEach(function (x) {
        var t = doc.createElement("textarea"), l = doc.createElement("label");
        t.readOnly = true; t.value = x.v; t.rows = 3; t.className = "held-copy";
        l.textContent = x.l || ""; l.appendChild(t); d.appendChild(l);
      });
      var n = doc.createElement("button");
      n.type = "button"; n.className = "btn small"; n.textContent = T("held_discard", "Discard this text");
      n.addEventListener("click", function () { b.remove(); });
      d.appendChild(n); b.appendChild(d);
    }
    var x = doc.createElement("button");
    x.type = "button"; x.className = "btn quiet small"; x.textContent = T("held_ok", "Dismiss");
    x.addEventListener("click", function () { b.remove(); });
    b.appendChild(x);
    main.insertBefore(b, main.firstChild);
  }
  function restore() {
    var raw = keep(), saved = null;
    try { saved = raw && JSON.parse(raw); } catch (e) { saved = null; }
    if (!saved || !saved.f || !saved.f.length) { keep(null); return; }
    var back = 0, left = [];
    pending = {};
    keep(null);
    saved.f.forEach(function (x) {
      var el = null;
      try { el = doc.querySelector(x.s); } catch (e) { el = null; }
      if (!el || !watched(el) || el.type !== x.y) { left.push(x); return; }
      if (value(el) !== x.v) {
        if (el.type === "checkbox" || el.type === "radio") el.checked = x.v === "1"; else el.value = x.v;
        el.dispatchEvent(new win.Event("input", { bubbles: true }));
        el.dispatchEvent(new win.Event("change", { bubbles: true }));
      }
      back++;
    });
    if (back) notice(T("held_back", "Your unsaved changes were kept in this browser and are back."), left);
    else if (left.length) notice(T("held_none", "Unsaved changes were kept in this browser."), left);
    // What could not go back stays kept until it is discarded or the person signs out.
    if (left.length) { left.forEach(function (x) { pending[x.s] = x; }); write(); }
  }
  // After the page's own scripts are up: the inspector adds its handlers a moment after the page has loaded.
  function when() {
    var tries = 0;
    (function look() {
      var insp = doc.getElementById("insp-form");
      if (insp && !S.inspector && tries++ < 60) { win.setTimeout(look, 100); return; }
      restore();
    })();
  }
  if (doc.readyState === "complete") when(); else win.addEventListener("load", when);
})();
