/* Scríobh writing view: the locked page (another tab holds the chapter's lease). doc.js fetches this when the page locks and
   keeps the state; this file draws the banner, takes the lease on request, and asks again while the page is in front. */
(function () {
  "use strict";
  var win = window, doc = document, S = (win.scriobh = win.scriobh || {});
  if (S.lock) return; // fetched twice before it ran
  var M = {};
  try { M = JSON.parse(doc.getElementById("write").dataset.lockMsgs || "{}"); } catch (e) { M = {}; }
  function T(k, d) { return M[k] || d; }
  var on = false, timer = 0, busy = false;

  // A locked page has nothing typed, so the server's copy is taken as it is when it moved on.
  function reconcile(cur) {
    var h = S.lockHost, W = S.write;
    W.editor.flush();
    if ((cur.revision !== W.rev || !h.sameText(cur.content, W.serverMd)) && !h.needs()) W.adopt(cur, 1);
    else { W.rev = cur.revision; W.editor.alts.setComment(h.altLine(cur.content)); }
  }
  // Only Take over moves the caret: a page that gets its lease back alone never takes focus.
  function unlock(focus) {
    var h = S.lockHost, ed = S.write.editor;
    stop();
    h.setLocked(false);
    h.unbar("lock");
    var stale = doc.getElementById("nb-live"); if (stale) stale.remove(); // "changed somewhere else" (livewrite.js): the text is the server's now
    doc.getElementById("doc-title").readOnly = false;
    ed.setReadOnly(false);
    h.setState("saved", T("again", "You can write here again"));
    if (S.tools) S.tools.update();
    if (focus) ed.focus();
    h.startRenewal();
    doc.dispatchEvent(new win.CustomEvent("scriobh:lease")); // held.js sends again the fields the lease kept
    h.offerDraft();
  }
  function takeOver() {
    var h = S.lockHost;
    h.takeLease(true).then(function (held) {
      if (!held) return h.toast(T("took_fail", "Could not take over. Try again."));
      return h.fetchDoc().then(function (cur) { reconcile(cur); unlock(true); });
    }, function () { h.toast(T("no_reach", "Could not reach the server. Try again.")); });
  }
  // take is false, so a lease a live tab holds is never taken; the request is upkeep, not use. Any failure leaves the page locked.
  function check() {
    if (busy) return;
    busy = true;
    S.lockHost.takeLease(false, true).then(function (held) {
      if (held) return S.lockHost.fetchDoc().then(function (cur) { reconcile(cur); unlock(false); });
    }).catch(function () { /* still locked */ }).then(function () { busy = false; });
  }
  // Only a page in front asks: a hidden tab that took the lease would hold it from the tab being looked at.
  function tick() { if (on && doc.visibilityState === "visible") check(); }
  function start() {
    if (on) return;
    on = true;
    timer = win.setInterval(tick, 30000);
    doc.addEventListener("visibilitychange", tick);
    win.addEventListener("focus", tick);
  }
  function stop() {
    on = false;
    win.clearInterval(timer);
    doc.removeEventListener("visibilitychange", tick);
    win.removeEventListener("focus", tick);
  }

  S.lock = {
    show: function () {
      S.lockHost.bar("lock", "warn", T("bar", "Open in another tab. This tab can read, but not edit. Take over to write here."), [{ label: T("take", "Take over"), fn: takeOver }]);
      if (S.tools) S.tools.update();
      start();
    }
  };
}());
