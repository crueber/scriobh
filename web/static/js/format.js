/* Scríobh writing view: the formatting bar and the selected-words figure. They were part of doc.js; the writing view's
   own script is kept small (it has a budget of its own), and the bar is wired a moment after the page has loaded.
   It reads the editor and the view's state through window.scriobh.write and never changes the save loop. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  if (!W || !W.editor) return;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  var ed = W.editor, markdown = win.scriobhEditor.markdown, fmtbar = $("#fmtbar"), cs = $("#st-sel");
  var isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
  var wr = W.cfg.canEdit && W.cfg.readonly !== "length";
  var tools = fmtbar ? $$(".tb[data-cmd]", fmtbar) : [], selTimer = 0;
  function hasMark(state, type) {
    var s = state.selection;
    return s.empty ? !!type.isInSet(state.storedMarks || s.$from.marks()) : state.doc.rangeHasMark(s.from, s.to, type);
  }
  function inNode(state, name, test) {
    var $f = state.selection.$from;
    for (var d = $f.depth; d >= 0; d--) { var n = $f.node(d); if (n.type.name === name && (!test || test(n))) return true; }
    return false;
  }
  function update() {
    if (!fmtbar) return;
    var state = ed.view.state, M = markdown.schema.marks, src = ed.mode() === "source";
    var on = {
      bold: hasMark(state, M.strong), italic: hasMark(state, M.em), strike: hasMark(state, M.strike), code: hasMark(state, M.code), link: hasMark(state, M.link),
      h1: inNode(state, "heading", function (n) { return n.attrs.level === 1; }), h2: inNode(state, "heading", function (n) { return n.attrs.level === 2; }),
      h3: inNode(state, "heading", function (n) { return n.attrs.level === 3; }), quote: inNode(state, "blockquote"),
      bullet: inNode(state, "bullet_list") && !inNode(state, "list_item", function (n) { return n.attrs.checked != null; }),
      ordered: inNode(state, "ordered_list"), task: inNode(state, "list_item", function (n) { return n.attrs.checked != null; }), source: src
    };
    var dead = src || W.locked || !wr;
    tools.forEach(function (b) {
      var c = b.dataset.cmd;
      if (b.hasAttribute("aria-pressed")) { var v = String(!!on[c]); if (b.getAttribute("aria-pressed") !== v) b.setAttribute("aria-pressed", v); }
      b.disabled = c !== "source" && dead;
    });
    var rov = tools.filter(function (b) { return b.tabIndex === 0; })[0];
    if (!rov || rov.disabled) { tools.forEach(function (b) { b.tabIndex = -1; }); $('[data-cmd="source"]', fmtbar).tabIndex = 0; }
    var ss = $("#st-source");
    if (ss) ss.setAttribute("aria-pressed", String(src));
  }
  function selection() {
    clearTimeout(selTimer);
    selTimer = setTimeout(function () {
      if (ed.mode() !== "rich") { if (cs) cs.textContent = ""; update(); return; }
      update();
      var s = ed.view.state.selection;
      if (!cs) return;
      if (s.empty || !W.showCounts) { cs.textContent = ""; return; }
      var n = ed.view.state.doc.textBetween(s.from, s.to, " ").split(/\s+/).filter(function (t) { return /[\p{L}\p{N}]/u.test(t); }).length;
      cs.textContent = n ? W.words(n) + " selected" : "";
    }, 60);
  }
  function run(name) {
    if (S.alts && S.alts.cmd(name)) return; // alternatives, ghosts and the stash are js/alts.js
    if (name === "source") { ed.toggleSource(); update(); W.counts(); return; }
    if (name === "link") {
      // Mod+K is the editor's own link command (set, or remove when already linked).
      ed.view.focus();
      var fire = function () { ed.view.dom.dispatchEvent(new win.KeyboardEvent("keydown", { key: "k", code: "KeyK", ctrlKey: !isMac, metaKey: isMac, bubbles: true, cancelable: true })); };
      var sel = ed.view.state.selection;
      if (sel.empty && hasMark(ed.view.state, markdown.schema.marks.link)) {
        // A caret inside a link: select the whole link first, so the command removes it.
        var n = ed.view.domAtPos(sel.from).node, a = (n.nodeType === 1 ? n : n.parentNode).closest("a");
        if (a) { win.getSelection().selectAllChildren(a); setTimeout(fire, 40); return; }
      }
      fire();
      return;
    }
    if (name === "ai") { doc.dispatchEvent(new win.CustomEvent("scriobh:ai", { detail: "menu" })); return; } // the writing partner's menu: js/aiboot.js
    ed.exec(name);
  }
  if (fmtbar) {
    tools.forEach(function (b) {
      var keys = (b.dataset.keys || "").replace("Mod", isMac ? "⌘" : "Ctrl");
      b.title = b.dataset.tip + (keys ? " (" + keys + ")" : "");
    });
    // The editor keeps focus (and the on-screen keyboard) while a button is pressed.
    fmtbar.addEventListener("pointerdown", function (e) { if (e.target.closest(".tb")) e.preventDefault(); });
    fmtbar.addEventListener("mousedown", function (e) { if (e.target.closest(".tb")) e.preventDefault(); });
    fmtbar.addEventListener("click", function (e) {
      var b = e.target.closest(".tb");
      if (b && !b.disabled && b.dataset.cmd) run(b.dataset.cmd);
    });
    fmtbar.addEventListener("keydown", function (e) {
      var live = tools.filter(function (b) { return !b.disabled; }), i = live.indexOf(doc.activeElement), n = -1;
      if (i < 0) return;
      if (e.key === "ArrowRight") n = (i + 1) % live.length;
      else if (e.key === "ArrowLeft") n = (i - 1 + live.length) % live.length;
      else if (e.key === "Home") n = 0;
      else if (e.key === "End") n = live.length - 1;
      else if (e.key === "Escape") { ed.focus(); return; }
      if (n < 0) return;
      e.preventDefault();
      tools.forEach(function (b) { b.tabIndex = -1; });
      live[n].tabIndex = 0;
      live[n].focus();
    });
  }
  // More buttons than the bar is wide (a phone, or a narrow column with the inspector open): a button at its end says so, and
  // scrolls the bar a page at a time (and back).
  if (fmtbar) {
    var more = doc.createElement("button"), still = win.matchMedia && win.matchMedia("(prefers-reduced-motion: reduce)");
    more.type = "button"; more.className = "fmt-more"; more.hidden = true; more.tabIndex = -1;
    more.addEventListener("pointerdown", function (e) { e.preventDefault(); });
    var atEnd = function () { return fmtbar.scrollLeft + fmtbar.clientWidth >= fmtbar.scrollWidth - 2; };
    var fit = function () {
      var over = fmtbar.scrollWidth - (more.hidden ? 0 : more.offsetWidth) > fmtbar.clientWidth + 2;
      more.hidden = !over;
      if (over) { var e = atEnd(); more.textContent = e ? "\u2039" : "\u203a"; more.setAttribute("aria-label", e ? "Earlier formatting buttons" : "More formatting buttons"); }
    };
    more.addEventListener("click", function () { fmtbar.scrollBy({ left: atEnd() ? -fmtbar.scrollWidth : Math.round(fmtbar.clientWidth * 0.7), behavior: still && still.matches ? "auto" : "smooth" }); });
    fmtbar.append(more);
    fmtbar.addEventListener("scroll", fit, { passive: true });
    win.addEventListener("resize", fit);
    if (win.ResizeObserver) new win.ResizeObserver(fit).observe(fmtbar);
    win.requestAnimationFrame(fit);
  }
  doc.addEventListener("selectionchange", selection);
  var ss = $("#st-source");
  if (ss) { ss.hidden = false; ss.addEventListener("click", function () { run("source"); }); var k = $("kbd", ss); if (k) k.textContent = (isMac ? "⌘" : "Ctrl") + " E"; }
  S.tools = { update: update, selection: selection, run: run };
  update();
})();
