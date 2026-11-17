/* Scríobh writing view: what the live-update stream does here. It keeps the manuscript tree's counts and order
   current, and tells the writer when this chapter was changed somewhere else. It never touches the text: a bar
   offers Reload (only when nothing is unsaved) or Keep mine, and while there are unsaved words the bar only says
   so, because saving will then ask which version to keep, exactly as it does for any other conflict. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  var notices = doc.getElementById("notices");
  if (!W || !notices) return;
  var seen = 0, tid = 0, bar = null;
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function say(m) { var l = doc.getElementById("live"); if (l) l.textContent = m; }

  // The tree: fresh counts and order a moment after the last event, and never while it is being renamed or dragged.
  function tree() {
    win.clearTimeout(tid);
    tid = win.setTimeout(function () {
      if (doc.querySelector(".is-renaming, .is-dragging") || !S.tree) { tree(); return; }
      var p = S.tree.refresh();
      if (p && p.then) p.then(null, function () { /* the server is away: the next event asks again */ });
    }, 1200);
  }
  function close() { if (bar) { bar.remove(); bar = null; } }
  function show(msg, actions) {
    close();
    bar = el("div", "notice-bar");
    bar.id = "nb-live";
    bar.dataset.kind = "warn";
    bar.setAttribute("role", "group");
    bar.setAttribute("aria-label", "Notice");
    var a = el("div", "notice-actions");
    actions.forEach(function (x) { var b = el("button", "btn" + (x.solid ? " solid" : ""), x.label); b.type = "button"; b.addEventListener("click", x.fn); a.appendChild(b); });
    bar.append(el("p", "notice-msg", msg), a);
    notices.appendChild(bar);
    say(msg);
  }
  // `by_you` cannot tell this tab from another tab of the same person, so the revision decides: a change whose
  // revision this tab does not hold (after its own save has had time to be recorded) came from somewhere else.
  function changed(rev) {
    if (rev && (rev <= seen || rev <= W.rev)) return;
    if (rev) seen = rev;
    if (W.unsaved()) {
      show("This chapter was changed somewhere else. Your unsaved text here is kept; when it saves you will be asked which version to keep.", [{ label: "Dismiss", fn: close }]);
      return;
    }
    show("This chapter was changed somewhere else.", [
      { label: "Reload", solid: true, fn: function () {
        if (W.unsaved()) { say("You typed something since. Let it save first."); return; }
        W.api("GET", "/api/v1/docs/" + W.cfg.id).then(function (r) { if (r.ok && r.json && !W.unsaved()) { W.adopt(r.json.item); close(); } });
      } },
      { label: "Keep mine", fn: close }
    ]);
  }
  doc.addEventListener("scriobh:event", function (e) {
    var d = e.detail || {}, t = d.type || "";
    if (t === "reset") { tree(); W.api("GET", "/api/v1/docs/" + W.cfg.id).then(function (r) { if (r.ok && r.json && r.json.item.revision > W.rev) changed(r.json.item.revision); }); return; }
    if (t.indexOf("doc.") !== 0 && t !== "revision.restored") return;
    tree();
    if (d.document_id !== W.cfg.id) return;
    if (t === "doc.trashed") { show("This chapter was moved to the trash somewhere else. Your text here is kept.", [{ label: "Dismiss", fn: close }]); return; }
    var p = d.payload || {};
    if (t === "doc.updated" && !p.content) return; // a rename or a move: the tree has it, the text did not change
    win.setTimeout(function () { changed(p.revision); }, 1500);
  });
})();
