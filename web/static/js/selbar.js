/* Scríobh writing view: the selection bar. Over a finished selection in the text, three buttons: Add to Compendium (the
   Compendium's New entry with the words), Look up (the Compendium search with them) and Comment (the Comments tab on them). They
   are the Alt+Shift+C and Alt+Shift+M paths of js/inspector.js, called as they are. The bar appears only when a pointer or a
   Shift+arrow has finished a selection, and hides on a key that is pressed, a press in the page, a scroll, the editor's blur and a
   selection that collapses. It never takes focus (its buttons are not in the Tab order, and a press on one keeps the selection),
   and nothing in the text moves: one adopted sheet places it, it is drawn at once and not animated. A phone (a coarse pointer)
   gets none, so the native selection handles are never covered. js/alts.js fetches this file at the first finished selection, and
   its right-click menu runs these same three items through S.selbar.run. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  if (!W || !W.editor || S.selbar) return;
  var view = W.editor.view, M = {}, bar = null, sheet = null, arrow = false;
  try { M = JSON.parse(doc.getElementById("write").dataset.selbarMsgs || "{}"); } catch (e) { M = {}; }
  function eligible() {
    var s = win.getSelection();
    return !!s && !s.isCollapsed && W.editor.mode() === "rich" && view.dom.contains(s.anchorNode) && view.dom.contains(s.focusNode) && !win.matchMedia("(pointer: coarse)").matches;
  }
  function hide() { if (bar) bar.hidden = true; }
  function keep(e) { e.preventDefault(); } // a press keeps the editor's focus and its selection
  function build() {
    bar = doc.createElement("div");
    bar.id = "selbar"; bar.setAttribute("role", "group"); bar.setAttribute("aria-label", M.group || ""); bar.hidden = true;
    ["add", "look", "comment"].forEach(function (k) {
      var b = doc.createElement("button");
      b.type = "button"; b.tabIndex = -1; b.textContent = M[k] || "";
      b.addEventListener("pointerdown", keep); b.addEventListener("mousedown", keep);
      b.addEventListener("click", function () { run(k); });
      bar.append(b);
    });
    doc.body.append(bar);
    sheet = new win.CSSStyleSheet(); doc.adoptedStyleSheets = doc.adoptedStyleSheets.concat([sheet]);
  }
  // The same paths as the chords: Alt+Shift+C is the Compendium with the words (the New entry for "add"), Alt+Shift+M the Comments tab.
  function run(k) {
    hide();
    var I = S.inspector;
    if (k === "add") I.compendium("new"); else if (k === "look") I.compendium(1); else I.want("comments");
  }
  function place() {
    var r = win.getSelection().getRangeAt(0).getBoundingClientRect(), w = bar.offsetWidth, h = bar.offsetHeight;
    var left = Math.max(8, Math.min((r.left + r.right) / 2 - w / 2, win.innerWidth - w - 8)), top = r.top - h - 8;
    if (top < 8) top = r.bottom + 8;
    sheet.replaceSync("#selbar{left:" + Math.round(left) + "px;top:" + Math.round(top) + "px}");
  }
  function finish() {
    if (!eligible()) return hide();
    if (!bar) build();
    bar.hidden = false; // measured and placed before the frame is drawn
    place();
  }
  doc.addEventListener("pointerdown", function (e) { if (!bar || !bar.contains(e.target)) hide(); }, true);
  doc.addEventListener("pointerup", function (e) { if (view.dom.contains(e.target)) win.setTimeout(finish, 0); }, true);
  doc.addEventListener("keydown", function (e) {
    if (e.key === "Escape" || /^Arrow/.test(e.key) || (e.key.length === 1 && !e.ctrlKey && !e.metaKey)) hide(); // a key that types, Escape, an arrow: the selection is about to move (beforeinput covers the rest)
    arrow = e.shiftKey && /^Arrow/.test(e.key);
  }, true);
  doc.addEventListener("keyup", function (e) {
    if (arrow && (e.key === "Shift" || /^Arrow/.test(e.key))) { arrow = false; win.setTimeout(finish, 0); }
  }, true);
  doc.addEventListener("beforeinput", hide, true);
  doc.addEventListener("scroll", hide, true);
  doc.addEventListener("selectionchange", function () { if (bar && !bar.hidden && !eligible()) hide(); });
  view.dom.addEventListener("blur", hide);
  win.addEventListener("resize", hide);
  S.selbar = { show: finish, run: run, hide: hide };
})();
