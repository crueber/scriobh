/* Scríobh writing view: the sprint line. Fetched only when a sprint is running as the page loads, or when the person
   starts one from the Sprint menu. It writes one short text into the status bar (and into the menu, for a screen with no
   status bar), at most once a minute, and says
   the end in the toasts (a polite live region): nothing here touches the text, moves it, or takes the caret. The words
   are counted by the saves the editor already makes; this file counts nothing. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh || {};
  var btn = doc.getElementById("st-sprint"), menu = doc.getElementById("sprint-menu"), write = doc.getElementById("write");
  if (!btn || !menu || !write) return;
  var M = {}, sp = null, timer = 0, last = "", told = false, lineIn = menu.querySelector("[data-sprint-line]"), account = doc.getElementById("account");
  try { M = JSON.parse(menu.dataset.msg || "{}"); } catch (e) { /* the words stay as the server wrote them */ }
  var csrf = (doc.querySelector('meta[name="csrf-token"]') || {}).content || "";
  function msg(k, n) { return (M[k] || "").replace("{n}", String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ",")); }
  function count(k, n) { return msg(k + (n === 1 ? ".one" : ".other"), n); }
  function say(t) { if (t && S.toast) S.toast(t); }
  function api(method, path, body) {
    return win.fetch(path, { method: method, credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf }, body: body ? JSON.stringify(body) : undefined })
      .then(function (r) { return r.json().catch(function () { return null; }).then(function (j) { return { ok: r.ok, status: r.status, json: j }; }); });
  }
  function show(running) {
    var a = menu.querySelector("[data-sprint-start]"), b = menu.querySelector("[data-sprint-end]");
    if (a) a.hidden = running;
    if (b) b.hidden = !running;
  }
  function text() {
    if (!sp) return M.idle || "";
    var now = Date.now();
    if (!sp.ends) return count("open", Math.max(0, Math.floor((now - sp.started) / 60000)));
    var left = Math.ceil((sp.ends - now) / 60000);
    return left > 0 ? count("left", left) : M.over || "";
  }
  function draw() { var t = text(); if (t !== last) { last = t; (btn.querySelector("[data-sprint-now]") || btn).textContent = t; if (lineIn) lineIn.textContent = t; } btn.hidden = !sp; }
  function finish() {
    // The server closed it by its own rule (its time ran out, or it was ended in another tab). It is looked for by its own id
    // among the finished sprints, and told by its own state: a sprint that is not there (it was set aside) is not told at all.
    var id = sp && sp.id;
    sp = null; told = false; show(false); draw();
    if (!id) return;
    api("GET", "/api/v1/me/studio/sprints?range=30d").then(function (r) {
      var list = (r.ok && r.json && r.json.recent) || [], x = null;
      for (var i = 0; i < list.length; i++) if (list[i].id === id) { x = list[i]; break; }
      if (x) say(msg(x.state === "stopped" ? "stopped" : "done", x.words_added));
    }, function () {});
  }
  function tick() {
    if (!sp) return;
    var now = Date.now();
    if (now >= sp.closes) { finish(); return; }
    if (sp.ends && now >= sp.ends && !told) { told = true; say(M.over); }
    draw();
  }
  function take(o) {
    if (!o || o.state !== "running" || o.project_id !== write.dataset.project) { sp = null; show(false); draw(); return; }
    // The server's seconds, laid on this clock: a clock that is wrong here does not move the end.
    var now = Date.now(), grace = Date.parse(o.closes_at) - (o.ends_at ? Date.parse(o.ends_at) : Date.parse(o.started_at));
    sp = { id: o.id, started: now - (o.elapsed_seconds || 0) * 1000, ends: o.ends_at ? now + (o.remaining_seconds || 0) * 1000 : 0 };
    sp.closes = (sp.ends || sp.started) + grace;
    told = !!o.over;
    show(true); draw();
    win.clearInterval(timer);
    timer = win.setInterval(tick, 20000); // looks three times a minute, writes only when the minute changes
  }
  function close() { [menu, account].forEach(function (m) { try { if (m) m.hidePopover(); } catch (e) { /* not open */ } }); }
  // Closing the menu from the account menu would leave focus on the page itself. After a start or a stop it goes to the text, where
  // a pointer is fine enough that the editor takes focus by itself on load (a touch screen is not given a keyboard it did not ask
  // for), and otherwise to the sprint line, or the account button.
  function back() {
    var fine = win.matchMedia && win.matchMedia("(pointer: fine)").matches, ed = fine && doc.querySelector("#editor [contenteditable='true']");
    var plain = doc.getElementById("doc-plain"), t = ed || (fine && plain && !plain.hidden && !plain.readOnly ? plain : null) || (!btn.hidden && btn.offsetParent ? btn : null) || [].filter.call(doc.querySelectorAll('[popovertarget="account"]'), function (e) { return e.offsetParent; })[0];
    if (t) { try { t.focus({ preventScroll: true }); } catch (e) { /* the page has no such place */ } }
  }
  menu.addEventListener("submit", function (e) {
    var f = e.target, act = f.dataset.sprint;
    if (!act) return;
    e.preventDefault();
    var b = e.submitter, min = b && b.name === "minutes" ? b.value : "";
    if (act === "start") {
      if (min === "custom") min = (f.querySelector('[name="custom"]') || {}).value || "";
      var n = parseInt(min, 10);
      if (!(n >= 0 && n <= 240)) { say(M.bad); return; }
      api("POST", "/api/v1/projects/" + write.dataset.project + "/studio/sprints", { minutes: n }).then(function (r) {
        if (r.ok && r.json) { take(r.json.item); close(); back(); say(n ? count("started", n) : M.started_open); }
        else if (r.status === 409) { show(true); say(M.running); } // one runs on another project: the menu now offers to stop it
        else say(M.failed);
      }, function () { say(M.offline); });
    } else {
      api("POST", "/api/v1/me/studio/sprint/" + act).then(function (r) {
        var x = r.ok && r.json && r.json.item;
        win.clearInterval(timer); sp = null; show(false); draw(); close(); back();
        if (x) say(x.state === "abandoned" ? M.abandoned : msg(x.state === "completed" ? "done" : "stopped", x.words_added));
      }, function () { say(M.offline); });
    }
  });
  [].forEach.call(menu.querySelectorAll("form[data-lazy]"), function (f) { f.removeAttribute("data-lazy"); });
  // What the server drew is a minute old at most; the clock here takes over from the times it gave.
  if (menu.dataset.sprint) { try { take(JSON.parse(menu.dataset.sprint)); } catch (e) { /* nothing running */ } }
  doc.addEventListener("visibilitychange", function () {
    if (doc.visibilityState !== "visible" || !sp) return;
    api("GET", "/api/v1/me/studio/sprint").then(function (r) { if (r.ok && r.json) { if (r.json.sprint) take(r.json.sprint); else finish(); } }, function () {});
  });
})();
