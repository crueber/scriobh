/* Scríobh: the link dialog. Fetched by doc.js the first time a link is added. */
(function () {
  "use strict";
  var doc = document;
  var S = (window.scriobh = window.scriobh || {});
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  S.linkDialog = function (info, ed) {
    var d = $("#link-dialog");
    if (!d) {
      d = el("dialog", "sheet"); d.id = "link-dialog"; d.setAttribute("aria-label", "Add a link");
      var f = el("form", "sheet-field"), i = el("input"); i.type = "text"; i.id = "link-url"; i.placeholder = "Link address"; i.setAttribute("aria-label", "Link address"); i.autocomplete = "off"; i.spellcheck = false;
      var b = el("button", "btn solid", "Add link"); b.type = "submit";
      f.append(i, b); d.appendChild(f);
      f.addEventListener("submit", function (e) {
        e.preventDefault();
        var url = i.value.trim();
        d.close();
        if (!url || /^\s*(javascript|data|vbscript):/i.test(url)) return;
        var text = d.dataset.text || url;
        ed.pasteMarkdown("[" + text.replace(/[\[\]\\]/g, "\\$&") + "](" + url.replace(/[()\s]/g, encodeURIComponent) + ")");
      });
      d.addEventListener("close", function () { ed.focus(); });
      d.addEventListener("click", function (e) { if (e.target === d) d.close(); });
      doc.body.appendChild(d);
    }
    d.dataset.text = info.text || "";
    $("#link-url").value = "";
    d.showModal();
    $("#link-url").focus();
  };
})();
