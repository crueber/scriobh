/* Scríobh writing view: split the chapter at the cursor (doc.split), in one step from the rich editor. The cursor is mapped to
   the Markdown offset the op needs by the editor's own serializer (ed.splitAt, which also moves a cut out of a list, a quote, a
   heading, formatting or a word, where cutting the stored text would break it), once the editor is held and saved, the way the
   stash puts text back at the cursor (js/stash.js). A chapter too long to edit is never written again: the place is found in
   its saved text as it is. From the Source view the textarea's caret is the offset. When the cut is not where the cursor was
   the writer is told, on the new chapter, and asked first when it moves far. The words come from the page (data-msg). One
   chord, Alt+Shift+B, claimed in the table js/alts.js keeps. */
(function () {
  "use strict";
  var doc = document, win = window, S = (win.scriobh = win.scriobh || {}), W = S.write, KEY = "scriobh:split-note";
  var box = doc.getElementById("split-msg"), M = {};
  try { M = JSON.parse((box && box.dataset.msg) || "{}"); } catch (e) { M = {}; }
  function T(k, d, v) { var s = M[k] || d; if (v) for (var n in v) s = s.split("{" + n + "}").join(v[n]); return s; }
  function note(m) { if (S.toast) S.toast(m); return { ok: false }; }
  // What the page that made a split has to say is said once, on the chapter it opened.
  try { var kept = win.sessionStorage.getItem(KEY); if (kept) { win.sessionStorage.removeItem(KEY); win.setTimeout(function () { note(kept); }, 700); } } catch (e) { /* nothing kept */ }
  if (!W || !W.editor || !W.cfg.canEdit) return;
  var ed = W.editor, view = ed.view, busy = false, done = false;
  function unsaved() { return note(T("unsaved", "Not saved yet. Staying here so nothing is lost.")); }
  function stale() { return note(T("stale", "The chapter changed before it could be split. Nothing was changed. Put the cursor where the new chapter should begin and try again.")); }
  function cannot(why) {
    return note(why === "start" ? T("start", "There is no text before the cursor. Put the cursor where the new chapter should begin.")
      : why === "end" ? T("end", "There is no text after the cursor. Put the cursor before the text that should move to the new chapter.")
      : why === "inside" ? T("inside", "The cursor is in the middle of a word or of formatting, and the nearest places to cut have no text on one side. Move the cursor and try again.")
      : why === "stored" ? T("stored", "This chapter is too long to open for editing, and its saved text is not written the way the editor writes it, so the cursor's place cannot be found without rewriting the chapter. Switch to the Source view (the Source button), put the cursor there, and split from it.")
      : T("unplaced", "That place could not be matched exactly with the saved text. Reload the chapter and try again."));
  }
  // Where the cut went, when it is not where the cursor was.
  function movedNote(p) {
    var v = { text: p.text || "…" };
    if (p.moved === "block") {
      if (p.at === "after") return p.what === "quote" ? T("moved_after_quote", "Split after the quote, which cannot be cut through. This chapter begins with “{text}”.", v) : T("moved_after_list", "Split after the list, which cannot be cut through. This chapter begins with “{text}”.", v);
      return p.what === "list" ? T("moved_list", "Split before the list, which cannot be cut through. This chapter begins with “{text}”.", v)
        : p.what === "quote" ? T("moved_quote", "Split before the quote, which cannot be cut through. This chapter begins with “{text}”.", v)
        : p.what === "heading" ? T("moved_heading", "Split before the heading. This chapter begins with “{text}”.", v)
        : p.what === "code" ? T("moved_code", "Split before the code block, which cannot be cut through. This chapter begins with “{text}”.", v)
        : T("moved_block", "Split before the block the cursor was in, which cannot be cut through. This chapter begins with “{text}”.", v);
    }
    if (p.moved === "word") return T("moved_word", "Split moved to the nearest edge of the word the cursor was in that has text on both sides. This chapter begins with “{text}”.", v);
    return p.moved ? T("moved_inline", "Split moved out of the formatting the cursor was in, so that nothing is cut in half. This chapter begins with “{text}”.", v) : "";
  }

  // The cut, sent. A split that went through takes the writer to the new chapter, to name it (the page it leaves holds the whole
  // old text at a stale revision, so it is not kept: the editor stays held until the page is gone, and if the page does not go
  // within ten seconds it is read again).
  function post(p) {
    return W.api("POST", "/api/v1/docs/" + W.cfg.id + "/split", { base_revision: W.rev, offset: p.offset, tab_id: W.tab }).then(function (r) {
      if (r.ok && r.json && r.json.tail) {
        done = true;
        if (r.json.head && r.json.head.revision) W.rev = r.json.head.revision; // the page's own live updates must not call this a change made elsewhere
        try { var m = movedNote(p); if (m) win.sessionStorage.setItem(KEY, m); } catch (e) { /* the notice is lost, the split is not */ }
        win.location.assign("/p/" + W.cfg.slug + "/d/" + r.json.tail.id + "?new=1");
        return { ok: true };
      }
      return note((r.json && r.json.error && r.json.error.message) || T("failed", "The split did not go through. Nothing was changed."));
    }, function () { return note(T("offline", "Could not reach the server, so the chapter may not have been split. Look at the manuscript before trying again.")); });
  }

  // The question the arm panels ask, in the notices above the format bar (nothing is held while it is open).
  function ask(p, onOk) {
    var host = doc.getElementById("notices"), old = doc.getElementById("nb-split");
    if (!host) return onOk();
    if (old) old.remove();
    var b = doc.createElement("div"), m = doc.createElement("p"), a = doc.createElement("div"), no = doc.createElement("button"), yes = doc.createElement("button"), v = { text: p.text || "…" };
    b.className = "notice-bar"; b.id = "nb-split"; b.dataset.kind = "warn"; b.setAttribute("role", "group"); m.className = "notice-msg"; m.id = "nb-split-msg"; b.setAttribute("aria-labelledby", m.id); a.className = "notice-actions";
    m.textContent = p.at === "after"
      ? T("ask_after", "The cursor is inside something that cannot be cut through (a list, a quote or a block). The cut would be made just after it, and the new chapter would begin with “{text}”. Split there?", v)
      : T("ask_before", "The cursor is inside something that cannot be cut through (a list, a quote or a block). The cut would be made just before it, and the new chapter would begin with “{text}”. Split there?", v);
    no.type = yes.type = "button"; no.className = "btn"; yes.className = "btn solid"; no.textContent = T("ask_no", "Cancel"); yes.textContent = T("ask_ok", "Split there");
    a.append(no, yes); b.append(m, a); host.appendChild(b);
    win.dispatchEvent(new win.Event("resize")); // the writing view makes room for what stands above the bar
    var close = function (back) { b.remove(); win.dispatchEvent(new win.Event("resize")); if (back) view.focus(); };
    no.addEventListener("click", function () { close(true); });
    yes.addEventListener("click", function () { close(false); onOk(); });
    b.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(true); } });
    no.focus();
  }

  // Held, saved, and the place found once the editor and the saved text agree; or, in a chapter that cannot be edited, in the saved
  // text as it is.
  function run(sel, cap) {
    var A = S.alts, ro = W.cfg.readonly === "length";
    busy = true;
    A.exclusive(function () {
      return W.flushAll().then(function (saved) {
        if (!saved) return unsaved();
        // Never write a chapter too long to edit again just to name a place in it.
        return (ro ? Promise.resolve(true) : A.ensurePlaced()).then(function (placed) {
          if (!placed) return cannot("unplaced");
          if (view.state.doc !== cap) return stale();
          var p = ed.splitAt(sel, ro && !A.placed() ? W.serverMd : undefined);
          return p.ok ? post(p) : cannot(p.why);
        });
      }).then(function (r) { return r.ok ? new Promise(function (res) { win.setTimeout(res, 10000); }) : r; }); // the page is on its way out
    }).then(function () {
      busy = false;
      if (!done) return;
      // Ten seconds, and still here: the chapter was split, and this page is out of date.
      try { win.sessionStorage.setItem(KEY, T("late", "The chapter was split. The new chapter follows this one in the manuscript.")); } catch (e) { /* said nowhere */ }
      win.location.reload();
    });
  }

  S.split = function () {
    var A = S.alts, st = doc.getElementById("save-state");
    if (busy) return;
    if (!A) return note(T("loading", "The split tool is still loading. Try again in a moment."));
    if (W.locked || (st && st.dataset.state === "locked")) return note(T("lease", "Another tab is writing this chapter. Take over there first."));
    if (ed.mode() === "source") { // the caret's place in the text is exact
      var at = doc.querySelector(".scriobh-source").selectionStart, md = ed.getMarkdown();
      busy = true;
      return W.flushAll().then(function (ok) {
        if (!ok) return unsaved();
        if (ed.getMarkdown() !== md) return stale(); // typed while it was being saved: the place is not the place
        return post({ offset: Math.min(at, md.length) });
      }).then(function (r) { if (!r.ok) busy = false; });
    }
    A.sync();
    var sel = view.state.selection, cap = view.state.doc, p = ed.splitAt(sel, W.cfg.readonly === "length" && !A.placed() ? W.serverMd : undefined);
    if (!p.ok) return cannot(p.why); // said at once, and nothing is held
    // Moved across a whole list or quote, or far: the writer says yes first.
    if (p.moved === "block" && (p.what === "list" || p.what === "quote" || p.dist > 200)) return ask(p, function () { if (view.state.doc !== cap) stale(); else run(sel, cap); });
    run(sel, cap);
  };

  // Back to this page from the new chapter (the browser may keep it as it was): it is out of date, so it is read again.
  win.addEventListener("pageshow", function (e) { if (e.persisted && done) win.location.reload(); });

  // Alt+Shift+B (by position, as the alternatives' keys are), from the text or the Source view; a held key is one press.
  var chords = (S.chords = S.chords || { clash: [] }), who = "split: the chapter break";
  if (chords.KeyB && chords.KeyB !== who) chords.clash.push("KeyB: " + chords.KeyB + " and " + who);
  chords.KeyB = who;
  doc.addEventListener("keydown", function (e) {
    if (e.code !== "KeyB" || !e.altKey || !e.shiftKey || e.ctrlKey || e.metaKey) return;
    if (e.isComposing || e.keyCode === 229 || view.composing) return; // an input method is using the key
    if (!(e.target.closest && e.target.closest(".scriobh-editor")) && !view.dom.contains(doc.activeElement)) return;
    e.preventDefault(); e.stopPropagation();
    if (!e.repeat) S.split();
  }, true);
})();
