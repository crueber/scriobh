/* Scríobh note and scratchpad page: saves by itself with the revision it was drawn with, keeps a copy in this
   browser until the server has the text, and never replaces what is in a box without being asked. A save that
   finds newer text on the server shows both and lets the person choose. Without script the page is a plain
   form that saves with its Save button. */
(function () {
  "use strict";
  var doc = document, form = doc.getElementById("r-note");
  if (!form || !form.dataset.pad) return;
  var note = form.dataset.pad === "note", can = form.dataset.canEdit === "true";
  var state = doc.getElementById("pad-state"), bars = doc.getElementById("pad-bars");
  var NAMES = note ? ["title", "status", "tags", "source_url", "retrieved_on", "quote", "body"] : ["content"];
  var csrf = (form.elements._csrf || {}).value || "";
  var rev = parseInt(form.elements.rev.value, 10) || 0;
  var timer = 0, flight = null, again = false, held = false, saved = null;

  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text) e.textContent = text; return e; }
  function field(n) { return form.elements[n] || null; }
  function values() { var v = {}; NAMES.forEach(function (n) { var f = field(n); if (f) v[n] = f.value; }); return v; }
  function same(a, b) { return JSON.stringify(a) === JSON.stringify(b); }
  function show(text, kind) { if (state) { state.textContent = text; state.dataset.state = kind; } }
  function body(v, withTags) {
    var o = { base_revision: rev };
    NAMES.forEach(function (n) {
      if (!(n in v)) return;
      if (n === "tags") { if (withTags) o.tags = v.tags.split(",").map(function (t) { return t.trim(); }).filter(Boolean); } else o[n] = v[n];
    });
    return o;
  }
  function api(method, o) {
    var h = { Accept: "application/json", "Content-Type": "application/json", "X-CSRF-Token": csrf };
    return fetch(form.dataset.url, { method: method, credentials: "same-origin", headers: h, body: o ? JSON.stringify(o) : undefined, keepalive: !!(o && JSON.stringify(o).length < 60000) }).then(function (r) {
      return r.text().then(function (t) { var j = null; try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ } return { status: r.status, ok: r.ok, json: j }; });
    });
  }

  // ---- a copy in this browser ----------------------------------------------------------------------------
  var key = form.dataset.draft;
  function store(v) { try { if (v) localStorage.setItem(key, JSON.stringify({ v: v, rev: rev, at: Date.now() })); else localStorage.removeItem(key); } catch (e) { /* storage may be off */ } }
  function readDraft() { try { return JSON.parse(localStorage.getItem(key)); } catch (e) { return null; } }
  var keepCopy = 0;
  function copyLater() { clearTimeout(keepCopy); keepCopy = setTimeout(function () { store(same(values(), saved) ? null : values()); }, 300); }

  function bar(kind, text, buttons) {
    var b = el("div", "r-bar"), p = el("p", "", text), row = el("div", "row");
    b.dataset.kind = kind; b.appendChild(p);
    buttons.forEach(function (x) { var u = el("button", "btn small" + (x[2] ? " solid" : ""), x[0]); u.type = "button"; u.addEventListener("click", x[1]); row.appendChild(u); });
    b.appendChild(row); bars.appendChild(b); return b;
  }
  function unbar() { while (bars.firstChild) bars.removeChild(bars.firstChild); }

  // ---- saving ----------------------------------------------------------------------------------------------
  function setRev(n) { rev = n; form.elements.rev.value = n; }
  function when() { return new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); }
  function dirty() { return !same(values(), saved); }
  function save(manual) {
    clearTimeout(timer);
    if (!can || held) return Promise.resolve(false);
    if (flight) { again = true; return flight; }
    var v = values(), active = doc.activeElement, tagsBusy = note && active === field("tags");
    if (same(v, saved) && !manual) return Promise.resolve(true);
    show("Saving", "saving");
    flight = api(note ? "PATCH" : "PUT", body(v, !tagsBusy)).then(function (r) {
      flight = null;
      if (r.ok && r.json && r.json.item) {
        setRev(r.json.item.revision);
        saved = tagsBusy ? Object.assign({}, v, { tags: saved.tags }) : v;
        if (same(values(), saved)) store(null); else copyLater();
        show("Saved at " + when(), "saved"); unbar();
        if (again || dirty()) { again = false; schedule(); }
        return true;
      }
      if (r.status === 409) { conflict(v); return false; }
      var m = r.json && r.json.error && r.json.error.message;
      show("Not saved." + (m ? " " + m : "") + " Your text is kept in this browser.", "error"); store(values());
      return false;
    }, function () {
      flight = null; show("Not saved: no connection. Your text is kept in this browser, and saving will be tried again.", "error"); store(values());
      timer = setTimeout(function () { save(); }, 10000); return false;
    });
    return flight;
  }
  // js/gate.js: the project locked (what is typed goes to the copy in this browser, nothing is sent) or opened again (save it, as ever).
  doc.addEventListener("scriobh:gate", function (e) {
    if (e.detail.state === "locked") { clearTimeout(timer); if (can && dirty()) store(values()); }
    else if (e.detail.state === "unlocked" && can && !held && dirty()) save(true);
  });
  function schedule() { clearTimeout(timer); if (can && !held) timer = setTimeout(save, note ? 2500 : 1500); }

  // Someone saved newer text: say so, show it, and let the person keep theirs or take the other.
  function conflict(mine) {
    held = true; clearTimeout(timer); store(mine);
    show("Not saved: this changed somewhere else.", "conflict"); unbar();
    api("GET").then(function (r) {
      var cur = r.json && r.json.item;
      if (!cur) { held = false; show("Not saved: could not read the other version. Your text is still here.", "error"); return; }
      var theirs = note ? [cur.title, (cur.body || "").slice(0, 400)].filter(Boolean).join(": ") : (cur.content || "").slice(0, 400);
      var b = bar("danger", "This changed somewhere else since you opened it. Yours is still in the boxes. The other version: " + (theirs || "empty") + (theirs.length >= 400 ? "…" : ""), [
        ["Keep mine", function () { setRev(cur.revision); held = false; unbar(); save(true); }, true],
        ["Use theirs", function () { fill(cur); setRev(cur.revision); saved = values(); store(null); held = false; unbar(); show("Showing the version saved elsewhere.", "saved"); }]]);
      b.setAttribute("role", "alert"); b.querySelector("button").focus();
    }, function () { held = false; show("Not saved: could not reach the server. Your text is still here.", "error"); });
  }
  function fill(cur) {
    NAMES.forEach(function (n) {
      var f = field(n); if (!f) return;
      var x = cur[n]; if (n === "tags") x = (cur.tags || []).join(", ");
      f.value = x == null ? "" : x;
    });
  }

  // ---- drafts from an earlier visit -------------------------------------------------------------------------
  function offer() {
    var d = readDraft();
    if (!d || !d.v) return;
    if (same(d.v, values())) { store(null); return; }
    if (doc.getElementById("r-conflict")) return;
    var older = d.rev !== rev;
    bar("warn", "A copy of changes you did not save, from " + new Date(d.at).toLocaleString([], { dateStyle: "medium", timeStyle: "short" }) + ", is kept in this browser." + (older ? " The saved text has changed since then, so saving it will ask you to choose." : ""), [
      ["Put it back", function () { fill(d.v); if (older) setRev(d.rev); unbar(); field(NAMES[NAMES.length - 1]).focus(); show("Not saved yet", "error"); if (!older) schedule(); }, true],
      ["Throw it away", function () { store(null); unbar(); }]]);
  }

  // ---- sending text away safely ----------------------------------------------------------------------------
  function flush() { clearTimeout(timer); return dirty() && can && !held ? save() : Promise.resolve(!dirty() || !can); }
  var S = window.scriobh = window.scriobh || {}, prior = S.beforeSignOut;
  S.beforeSignOut = function () { return flush().then(function (ok) { return ok && (prior ? prior() : true); }); };
  function away() { if (can && !held && dirty()) { store(values()); clearTimeout(timer); save().then(function (ok) { if (ok && !dirty()) store(null); }); } }
  addEventListener("pagehide", away);
  doc.addEventListener("visibilitychange", function () { if (doc.visibilityState === "hidden") away(); });
  addEventListener("online", function () { if (dirty()) save(); });

  // ---- wiring ----------------------------------------------------------------------------------------------
  saved = values();
  if (can) {
    form.addEventListener("input", function () { show("Not saved yet", "saving"); copyLater(); schedule(); });
    form.addEventListener("change", function (e) { if (e.target === field("tags")) save(); });
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      save(true).then(function (ok) { if (ok && note) location.assign(location.pathname + "?saved=saved"); });
    });
    offer();
  }

  // ---- preview and the clipping source (note page) -----------------------------------------------------------
  var tg = form.querySelector("[data-toggle-preview]"), pv = doc.getElementById("n-preview"), ta = field("body");
  if (tg && pv && ta) {
    tg.hidden = false;
    tg.addEventListener("click", function () {
      var on = tg.getAttribute("aria-pressed") !== "true";
      if (!on) { pv.hidden = true; ta.hidden = false; tg.setAttribute("aria-pressed", "false"); tg.textContent = "Preview"; ta.focus(); return; }
      tg.disabled = true;
      fetch(form.dataset.preview, { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": csrf, "X-Scriobh-Fetch": "1" }, body: new URLSearchParams({ _csrf: csrf, body: ta.value }) })
        .then(function (r) { return r.ok ? r.text() : Promise.reject(); })
        .then(function (h) { pv.innerHTML = h; ta.hidden = true; pv.hidden = false; tg.setAttribute("aria-pressed", "true"); tg.textContent = "Edit"; pv.focus(); },
          function () { show("The preview could not be made. Your text is unchanged.", "error"); })
        .then(function () { tg.disabled = false; });
    });
  }
  var cp = doc.querySelector("[data-copy]"), op = doc.querySelector("[data-open]"), warn = doc.getElementById("n-warn"), src = doc.getElementById("n-src");
  if (src && cp) {
    var said = el("span", "sr-only"); said.setAttribute("role", "status"); cp.parentNode.insertBefore(said, cp.nextSibling);
    cp.hidden = false;
    cp.addEventListener("click", function () {
      var t = src.textContent.trim(), ok = function () { said.textContent = "Address copied."; cp.textContent = "Copied"; setTimeout(function () { cp.textContent = "Copy the address"; }, 2000); };
      if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(t).then(ok, function () { said.textContent = "Could not copy. Select the address and copy it."; });
      else { var r = doc.createRange(); r.selectNodeContents(src); var s = getSelection(); s.removeAllRanges(); s.addRange(r); said.textContent = "The address is selected. Copy it with your keyboard."; }
    });
  }
  if (src && op && warn) {
    var shut = function () { warn.hidden = true; op.setAttribute("aria-expanded", "false"); op.focus(); };
    op.hidden = false;
    op.addEventListener("click", function () { warn.hidden = false; op.setAttribute("aria-expanded", "true"); warn.querySelector("[data-go]").focus(); });
    warn.querySelector("[data-cancel]").addEventListener("click", shut);
    warn.addEventListener("keydown", function (e) { if (e.key === "Escape") shut(); });
    warn.querySelector("[data-go]").addEventListener("click", function () {
      var u = src.textContent.trim();
      if (/^https?:\/\//i.test(u)) window.open(u, "_blank", "noopener,noreferrer");
      shut();
    });
  }
}());
