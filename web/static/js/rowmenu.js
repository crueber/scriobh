/* Scríobh: the manuscript row menu (rename, add, move up and down, indent, move to, trash).
   Fetched by tree.js the first time a menu is opened. It uses tree.js through S.treeApi. */
(function () {
  "use strict";
  var doc = document, S = window.scriobh, T = S && S.treeApi;
  var menu = doc.getElementById("row-menu"), menuBody = doc.getElementById("row-menu-body"), menuRow = null;
  if (!T || !menu) return;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  var title = T.title, plan = T.plan, shift = T.shift, rename = T.rename, add = T.add, trash = T.trash, move = T.move, byId = T.byId, rows = T.rows, inside = T.inside;
  function item(label, fn, opts) {
    var b = el("button", "sheet-item", label);
    b.type = "button";
    if (opts && opts.off) b.disabled = true;
    if (opts && opts.danger) b.dataset.danger = "";
    b.addEventListener("click", fn);
    return b;
  }
  function showActions(r) {
    var k = r.dataset.kind, head = el("div", "sheet-head"), list = el("div", "sheet-actions");
    head.append(el("p", "", k.replace("_", " ")), el("h2", "", title(r)));
    var close = function (fn) { return function () { menu.close(); fn(); }; };
    list.append(
      item("Rename", close(function () { rename(r); })),
      item(k === "chapter" || k === "part" ? "Add scene inside" : "Add scene after", close(function () { add("scene", r, k === "chapter" || k === "part"); })),
      item(k === "part" ? "Add chapter inside" : "Add chapter after", close(function () { add("chapter", r, k === "part"); })),
      item("Move up", close(function () { shift(r, "up"); }), { off: !plan(r, "up") }),
      item("Move down", close(function () { shift(r, "down"); }), { off: !plan(r, "down") }),
      item("Indent", close(function () { shift(r, "in"); }), { off: !plan(r, "in") }),
      item("Outdent", close(function () { shift(r, "out"); }), { off: !plan(r, "out") }),
      item("Move to…", function () { showTargets(r); }),
      item("Move to trash", close(function () { trash(r); }), { danger: true }));
    menuBody.replaceChildren(head, list);
  }
  function showTargets(r) {
    var head = el("div", "sheet-head"), list = el("div", "sheet-actions");
    head.append(el("p", "", "Move"), el("h2", "", title(r)));
    list.append(item("Top level", function () { menu.close(); move(r, ""); }));
    rows().forEach(function (t) {
      if (inside(r, t)) return;
      var b = item(title(t), function () { menu.close(); move(r, t.dataset.id); });
      b.prepend(el("span", "j-num", t.dataset.kind === "part" ? "Part" : ""));
      list.append(b);
    });
    var back = item("Back", function () { showActions(r); var f = $(".sheet-item", menuBody); if (f) f.focus(); });
    menuBody.replaceChildren(head, list, back);
    var f = $(".sheet-item", list);
    if (f) f.focus();
  }
  menu.addEventListener("close", function () {
    var r = menuRow && byId(menuRow.dataset.id);
    menuRow = null;
    if (r && !doc.querySelector(".is-renaming")) r.focus();
  });
  menu.addEventListener("click", function (e) { if (e.target === menu) menu.close(); });
  S.rowMenu = function (r) {
    menuRow = r;
    showActions(r);
    menu.showModal();
    var f = $(".sheet-item:not([disabled])", menuBody);
    if (f) f.focus();
  };
})();
