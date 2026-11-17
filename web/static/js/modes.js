/* Scríobh writing modes: typewriter scrolling and dimming the other paragraphs.
   Loaded on document routes only when the person turned one of them on in Settings;
   it works through the editor's public API and never touches the save loop. */
(function () {
  "use strict";
  var win = window, doc = document;
  var W = win.scriobh && win.scriobh.write, E = win.scriobhEditor;
  var root = doc.getElementById("write"), paper = doc.getElementById("paper");
  if (!W || !E || !root || !paper) return;
  var ed = W.editor;

  // Focus: one decoration, on the paragraph (top-level block) the caret is in.
  // The stylesheet fades every other block; nothing else is rewritten.
  if (root.dataset.focusdim === "true") {
    paper.classList.add("focus-dim");
    win.scriobh.decorate(ed, "focus", function (state) {
      var $f = state.selection.$from;
      if ($f.depth < 1) return [];
      return [E.Decoration.node($f.before(1), $f.after(1), { class: "is-active" })];
    });
  }

  // Typewriter: while text is being typed, keep the caret line near the middle of the window.
  // The scroll is instant, taken once per frame, only when the line has drifted, and never
  // for a click or a drag (those move the selection, not the text).
  if (root.dataset.typewriter === "true") {
    var pending = 0, dispatch = ed.view.dispatch;
    var settle = function () {
      pending = 0;
      if (ed.mode() !== "rich" || !ed.view.hasFocus()) return;
      var c = ed.view.coordsAtPos(ed.view.state.selection.head), dy = c.top - win.innerHeight * 0.45;
      if (Math.abs(dy) > 28) win.scrollBy(0, dy);
    };
    ed.view.dispatch = function (tr) {
      dispatch(tr);
      if (tr.docChanged && !pending) pending = win.requestAnimationFrame(settle);
    };
  }
})();
