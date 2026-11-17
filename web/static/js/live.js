/* Scríobh live updates: follows /api/v1/events for the one project or Compendium a page names (a hidden
   <span data-live="project:ID"> or "compendium:ID") and hands each event to the page as a `scriobh:event` on
   document, with the frame's data as detail ({type, seq, project_id or compendium_id, document_id, by_you,
   payload}: ids and tokens only). It resumes from the last id it saw, backs off when the connection drops,
   stops while the tab is hidden, and says `reset` when it fell too far behind, so a page refetches. It never
   changes the page itself: what to do with an event is each page's business, and a page never lets one overwrite
   what a person is typing. */
(function () {
  "use strict";
  var doc = document, win = window, mark = doc.querySelector("[data-live]");
  if (!mark || !win.fetch || !win.TextDecoder || !win.AbortController) return;
  var p = mark.dataset.live.split(":"), url = "/api/v1/events?" + (p[0] === "compendium" ? "compendium=" : "project=") + encodeURIComponent(p[1]);
  var last = "", ac = null, wait = 3000, timer = 0, gone = false, since = 0;
  function emit(type, data) {
    var d = {};
    try { d = JSON.parse(data || "{}"); } catch (e) { /* a frame without data */ }
    d.type = d.type || type;
    doc.dispatchEvent(new win.CustomEvent("scriobh:event", { detail: d }));
  }
  function again() {
    ac = null;
    win.clearTimeout(timer);
    if (since && Date.now() - since > 60000) wait = 3000; // a connection that held for a minute was a good one
    since = 0;
    if (gone || doc.hidden) return;
    timer = win.setTimeout(open, wait);
    wait = Math.min(wait * 2, 30000);
  }
  function frame(ev, data, id) {
    if (id) last = id;
    if (ev === "hello") { if (!last) { try { last = String(JSON.parse(data).seq); } catch (e) { /* no cursor */ } } return; }
    if (ev === "bye") return;
    wait = 3000; // the wait grows until the server has really sent something, not merely answered
    if (ev === "reset") last = "";
    emit(ev || "message", data);
  }
  function open() {
    if (ac || gone || doc.hidden) return;
    var mine = (ac = new win.AbortController()), h = { Accept: "text/event-stream" };
    if (last) h["Last-Event-ID"] = last;
    win.fetch(url, { credentials: "same-origin", headers: h, signal: mine.signal }).then(function (r) {
      if (r.status === 401 || r.status === 403 || r.status === 404) { gone = true; if (r.status === 403) doc.dispatchEvent(new win.CustomEvent("scriobh:gate-check")); return; } // a refusal may be the project locking: js/gate.js asks, and covers the page
      if (!r.ok || !r.body) throw r;
      since = Date.now();
      var rd = r.body.getReader(), dec = new win.TextDecoder(), buf = "", ev = "", data = "", id = "";
      return (function pump() {
        return rd.read().then(function (x) {
          if (x.done) return;
          buf += dec.decode(x.value, { stream: true });
          var lines = buf.split("\n");
          buf = lines.pop();
          lines.forEach(function (l) {
            l = l.replace(/\r$/, "");
            if (l === "") { if (ev || data) frame(ev, data, id); ev = data = ""; return; }
            if (l.charAt(0) === ":") return;
            var i = l.indexOf(":"), k = i < 0 ? l : l.slice(0, i), v = i < 0 ? "" : l.slice(i + 1).replace(/^ /, "");
            if (k === "event") ev = v; else if (k === "data") data += (data ? "\n" : "") + v; else if (k === "id") id = v;
          });
          return pump();
        });
      })();
    }).then(function () { if (ac === mine) again(); }, function () { if (ac === mine) again(); });
  }
  doc.addEventListener("visibilitychange", function () {
    if (doc.hidden) { win.clearTimeout(timer); if (ac) { var a = ac; ac = null; a.abort(); } } else { wait = 3000; open(); }
  });
  win.addEventListener("pagehide", function () { gone = true; if (ac) { var a = ac; ac = null; a.abort(); } });
  // The project opened again (js/gate.js): the stream starts over from where it was.
  doc.addEventListener("scriobh:gate", function (e) { if (e.detail.state === "unlocked") { gone = false; wait = 3000; open(); } });
  // A page restored from the back-forward cache is alive again: pagehide must not have ended the stream for good.
  win.addEventListener("pageshow", function (e) { if (e.persisted) { gone = false; wait = 3000; open(); } });
  open();
})();
