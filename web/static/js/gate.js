/* Scríobh: a page of a project that has a passphrase, once it is open (F-252).
   When the project locks (the idle limit, another tab, a changed passphrase, Lock now, or this page's own idle timer) the page
   puts what is typed into this browser's drafts (the writing view, the notes and held.js do that when they hear `scriobh:gate`),
   then takes the manuscript out of the page and goes to the unlock page; after the unlock it comes back to the chapter, where
   the drafts are offered back. Until the unlock page has arrived (and for as long as it cannot, with the network cut) the
   page shows a cover with the unlock box, and the page behind it has nothing of the text left in it: it is removed, not just
   hidden, so that the developer tools, Save page as and a reader mode find none of it. The cover shows the project's title.
   How it finds out, with no request of its own that counts as use:
   - any fetch answered 403 project_locked (this file wraps fetch), and an htmx request answered the same way (the redirect
     to the unlock page is cancelled so that nothing is thrown away before the drafts are written);
   - the live stream ending in a refusal (live.js says so), the tab coming back, the window focused, and a quiet check
     every 20 seconds while the tab is shown (project.passphrase.get, which is not use);
   - a timer of its own: the idle limit is known to the page, and when nothing has been typed, clicked, touched or scrolled
     for that long the page locks itself even if the network is cut. The server stays the authority.
   Strings come from the page (data-msg). The passphrase is sent once, in the body of the unlock call, and the box is emptied
   at once; it is not kept anywhere. */
(function () {
  "use strict";
  var doc = document, win = window, id = doc.body.dataset.gate, cover = doc.getElementById("gate-cover");
  if (!id || !cover || !cover.showModal) return;
  var M = {};
  try { M = JSON.parse(cover.dataset.msg || "{}"); } catch (e) { M = {}; }
  function T(k, d, v) { var s = M[k] || d; if (v) for (var n in v) s = s.split("{" + n + "}").join(v[n]); return s; }
  var $ = function (s) { return doc.getElementById(s); };
  var form = $("gate-cover-form"), pass = $("gate-cover-pass"), err = $("gate-cover-err"), held = $("gate-cover-held"), go = $("gate-cover-go"), signin = $("gate-cover-signin");
  var csrf = (doc.querySelector('meta[name="csrf-token"]') || {}).content || "";
  var idle = (parseInt(doc.body.dataset.gateIdle, 10) || 0) * 60000, slug = doc.body.dataset.gateSlug || "";
  var base = "/api/v1/projects/" + encodeURIComponent(id);
  var S = (win.scriobh = win.scriobh || {});
  var locked = false, busy = false, hiddenAt = 0, waitTimer = 0, leaveTimer = 0, noop = function () {};
  var here = win.location.pathname, unlockURL = "/p/" + slug + "/unlock?next=" + encodeURIComponent(here);
  // The clock the idle timer reads: a test may put its own in S.gate.clock.
  function now() { return S.gate && S.gate.clock ? S.gate.clock() : Date.now(); }
  var lastAct = now();

  function say(msg) { err.textContent = msg || ""; err.hidden = !msg; if (msg) pass.setAttribute("aria-invalid", "true"); else pass.removeAttribute("aria-invalid"); }
  function note(state) { doc.dispatchEvent(new win.CustomEvent("scriobh:gate", { detail: { state: state } })); }
  // The palette, the dialog and any open menu are closed before the cover goes up: a modal dialog over an open popover is a trap.
  function close() {
    ["palette", "dialog"].forEach(function (n) { var d = $(n); if (d && d.open) d.close(); });
    Array.prototype.forEach.call(doc.querySelectorAll(":popover-open"), function (p) { try { p.hidePopover(); } catch (e) { /* already closed */ } });
  }
  // Everything of the page but the cover leaves the document. What was typed is already in the drafts (note("locked") came first).
  function detach() {
    Array.prototype.slice.call(doc.body.children).forEach(function (e) { if (e !== cover && e.tagName !== "SCRIPT") e.remove(); });
  }

  function lock() {
    if (locked) return;
    locked = true;
    note("locked"); // first: the page and held.js write their drafts, synchronously
    close();
    held.hidden = !((S.write && S.write.unsaved && S.write.unsaved()) || (S.held && S.held.count()));
    doc.documentElement.classList.add("gate-locked");
    doc.title = T("tab", "Locked") + " · Scríobh";
    say("");
    pass.value = "";
    if (!cover.open) cover.showModal();
    pass.focus();
    detach();
    leave();
  }
  // To the unlock page, when it can be reached; the cover stays where it cannot (and is tried again when the network returns).
  function leave() {
    win.clearTimeout(leaveTimer);
    nativeFetch.call(win, unlockURL, { credentials: "same-origin", redirect: "manual", headers: { Accept: "text/html", "X-Scriobh-Background": "1" } }).then(function (r) {
      if (r.type === "opaqueredirect" || r.status === 0) win.location.reload(); // not locked after all, or signed out: the server says which
      else if (r.ok) win.location.assign(unlockURL);
      else leaveTimer = win.setTimeout(leave, 10000);
    }, function () { leaveTimer = win.setTimeout(leave, 10000); });
  }
  win.addEventListener("online", function () { if (locked) leave(); });
  // Escape must not dismiss it, and neither may anything that closes a dialog: while locked it opens again.
  cover.addEventListener("cancel", function (e) { e.preventDefault(); });
  cover.addEventListener("close", function () { if (locked) win.setTimeout(function () { if (locked && !cover.open) cover.showModal(); }, 0); });
  // Tab goes round the cover and out through nothing else: the way out is Unlock, or the link back to the library.
  cover.addEventListener("keydown", function (e) {
    if (e.key !== "Tab") return;
    var f = Array.prototype.filter.call(cover.querySelectorAll("a[href], button, input:not([type=hidden])"), function (x) { return !x.disabled && !x.hidden; });
    if (!f.length) return;
    var a = f[0], z = f[f.length - 1];
    if (e.shiftKey && doc.activeElement === a) { e.preventDefault(); z.focus(); } else if (!e.shiftKey && doc.activeElement === z) { e.preventDefault(); a.focus(); }
  });

  // ---- finding out ---------------------------------------------------------------------------------------------
  var nativeFetch = win.fetch;
  if (nativeFetch) win.fetch = function () {
    var p = nativeFetch.apply(this, arguments);
    p.then(function (r) {
      if (r.status !== 403 || locked) return;
      r.clone().json().then(function (j) {
        var d = j && j.error && j.error.data;
        if (d && d.reason === "project_locked" && (!d.project_id || d.project_id === id)) { lock(); win.setTimeout(check, 1500); } // and asks again: a refusal already on its way when the project was unlocked clears at once
      }, noop);
    }, noop);
    return p;
  };
  doc.addEventListener("htmx:beforeOnLoad", function (e) {
    var x = e.detail && e.detail.xhr;
    if (x && x.status === 403 && /\/unlock(\?|$)/.test(x.getResponseHeader("HX-Redirect") || "")) { e.preventDefault(); lock(); }
  });
  function check() {
    nativeFetch.call(win, base + "/passphrase", { credentials: "same-origin", headers: { Accept: "application/json", "X-Scriobh-Background": "1" } }).then(function (r) { return r.ok ? r.json() : null; }).then(function (j) {
      var s = j && j.item;
      if (!s) return;
      if (s.protected && !s.unlocked) lock(); else if (locked && !busy) win.location.reload(); // open again (another tab, or never locked): the page comes back with its drafts offered
    }, noop);
  }
  win.setInterval(function () { if (!doc.hidden) check(); }, 20000);
  doc.addEventListener("scriobh:gate-check", check);
  win.addEventListener("focus", check);
  win.addEventListener("pageshow", function (e) { if (e.persisted) check(); });

  // ---- this page's own idle timer ------------------------------------------------------------------------------
  function idleCheck() { if (!locked && idle && now() - lastAct > idle) lock(); }
  ["keydown", "pointerdown", "input", "scroll", "touchstart", "wheel"].forEach(function (ev) {
    doc.addEventListener(ev, function () { lastAct = now(); }, { capture: true, passive: true });
  });
  win.setInterval(idleCheck, 5000);
  doc.addEventListener("visibilitychange", function () {
    if (doc.hidden) { hiddenAt = Date.now(); return; }
    // A tab left for longer than the idle limit is covered before the answer comes: nothing of the text is on the screen for the
    // moment it takes to ask. If the project turns out to be open (another tab was in use), the page comes back by itself.
    if (idle && hiddenAt && Date.now() - hiddenAt > idle) lock();
    idleCheck();
    check();
  });

  // ---- Lock now ------------------------------------------------------------------------------------------------
  function lockNow() {
    var saved = S.write && S.write.flushAll ? S.write.flushAll() : Promise.resolve(true);
    Promise.race([saved, new Promise(function (r) { win.setTimeout(r, 3000); })]).then(function () {
      return nativeFetch.call(win, base + "/lock", { method: "POST", credentials: "same-origin", headers: { Accept: "application/json", "X-CSRF-Token": csrf } });
    }).then(function (r) {
      if (r.ok) lock(); else if (S.toast) S.toast(T("lock_failed", "The project could not be locked. Nothing was changed."));
    }, function () { if (S.toast) S.toast(T("lock_failed", "The project could not be locked. Nothing was changed.")); });
  }
  doc.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-gate-lock]");
    if (b) { e.preventDefault(); lockNow(); }
  });
  S.gate = { lock: lock, lockNow: lockNow, idleCheck: idleCheck, clock: null, get locked() { return locked; } };

  // ---- unlocking from the cover (the network was down when the page locked, or the unlock page could not be reached) -----------------
  function wait(secs) {
    var n = Math.max(1, Math.min(secs || 30, 3600));
    say(n < 90 ? T("wait_s", "Too many tries. You can try again in {n} seconds.", { n: n }) : T("wait_m", "Too many tries. You can try again in about {n} minutes.", { n: Math.ceil(n / 60) }));
    go.disabled = true;
    win.clearTimeout(waitTimer);
    waitTimer = win.setTimeout(function () { go.disabled = false; say(""); }, n * 1000);
  }
  form.addEventListener("submit", function (e) {
    e.preventDefault();
    if (busy || go.disabled) return;
    var v = pass.value;
    if (!v) { say(T("empty", "Type the passphrase.")); pass.focus(); return; }
    busy = true;
    say("");
    nativeFetch.call(win, base + "/unlock", { method: "POST", credentials: "same-origin", headers: { Accept: "application/json", "Content-Type": "application/json", "X-CSRF-Token": csrf }, body: JSON.stringify({ passphrase: v }) }).then(function (r) {
      pass.value = ""; // the box is emptied whatever the answer was
      return r.text().then(function (t) {
        var j = null;
        try { j = t ? JSON.parse(t) : null; } catch (x) { /* not JSON */ }
        busy = false;
        if (r.ok) { win.location.assign(here); return; } // the chapter again, with what was typed offered back
        var er = (j && j.error) || {}, d = er.data || {};
        if (r.status === 401) { say(T("signed_out", "You were signed out. Sign in again to carry on; what you typed is kept in this browser.")); signin.href = "/auth/login?next=" + encodeURIComponent(here); signin.hidden = false; go.disabled = true; signin.focus(); return; }
        if (r.status === 429) wait(er.retry_after || parseInt(r.headers.get("Retry-After") || "0", 10));
        else if (r.status === 409 && d.reason === "passphrase_changed") say(T("changed", "The passphrase changed while you were typing it. Try again."));
        else if (r.status === 422 && er.field === "passphrase") say(T("wrong", "That passphrase is not right."));
        else if (r.status === 422 && er.message) say(er.message);
        else say(T("failed", "The project could not be unlocked. Try again."));
        if (!go.disabled) pass.focus();
      });
    }, function () { busy = false; pass.value = ""; say(T("offline", "Could not reach the server. Your text is kept here. Try again.")); pass.focus(); });
  });
})();
