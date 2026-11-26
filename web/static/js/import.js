/* Scríobh import pages: the three things a server-rendered review cannot do alone. The chooser sends a chosen file at once and
   says how much of it has gone; a chosen folder sends only the files an import reads and lists the rest by name and size, so
   no picture is uploaded; and after a fragment is swapped, the control the server marked (data-focus) takes focus. No state is
   kept here: the review is the draft the server holds. */
(function () {
  "use strict";
  var d = document, st = function () { return d.getElementById("imp-status"); };
  var MD = /\.(md|markdown)$/i, MF = /(^|\/)(scriobh|project)\.json$/, SC = /\.(scrivx|rtf|txt|png|jpe?g|gif|webp)$/i;
  var PF = { odt: "odt", rtf: "rtf", tex: "latex", latex: "latex", wiki: "mediawiki", mediawiki: "mediawiki", org: "org", textile: "textile", rst: "rst", fb2: "fb2", typ: "typst", dbk: "docbook", docbook: "docbook" };
  d.addEventListener("htmx:beforeRequest", function (e) { if (d.hidden && e.detail.requestConfig.verb === "get" && e.target.id === "imp-review") e.preventDefault(); }); // a hidden tab does not poll
  // A change to the draft is checked against the revision it was made on. While a change with a revision is in flight, or was accepted (the
  // draft moved on), a second send with that revision is not sent. A change the server refused or that failed did not move the draft, so
  // its revision is released. Project fields are saved without a new revision (see saveProject), so they are not held back.
  var sent = {};
  var sendKey = function (rq) {
    var fd = rq.formData, rev = fd && fd.get("rev");
    return rev && fd.get("act") !== "project" && /\/ui\/import\/d\/[^/]+\/(update|apply)$/.test(rq.path || "") ? rq.path + "|" + rev : "";
  };
  d.addEventListener("htmx:beforeRequest", function (e) {
    var k = sendKey(e.detail.requestConfig);
    if (!k) return;
    if (sent[k]) { e.preventDefault(); return; }
    sent[k] = true;
  });
  d.addEventListener("htmx:afterRequest", function (e) {
    var k = sendKey(e.detail.requestConfig || {}), x = e.detail.xhr;
    if (k && (!x || x.status < 200 || x.status >= 300)) delete sent[k]; // refused, or never answered: the revision still stands
  });
  // Enter in a field of a change form commits it once, as leaving the field does. The browser's own submit would send the same form again,
  // with the same revision. Other forms (the cut's pattern, the search fields) keep Enter as their submit.
  d.addEventListener("keydown", function (e) {
    var t = e.target;
    if (e.key !== "Enter" || e.isComposing || t.tagName !== "INPUT" || t.type !== "text" || !t.form || !t.closest("#imp-review")) return;
    if (t.form.getAttribute("hx-trigger") !== "change" && !t.hasAttribute("hx-post")) return;
    e.preventDefault();
    if (t.value !== t.defaultValue) t.dispatchEvent(new Event("change", { bubbles: true }));
  });
  d.addEventListener("DOMContentLoaded", function () {
    var f = d.querySelector("[data-focus]") || (d.querySelector("[data-review]") && d.getElementById("imp-h"));
    if (f) f.focus({ preventScroll: true });
  });
  d.addEventListener("change", function (e) {
    var t = e.target, f = t.form;
    if (t.type !== "file" || !t.files.length || !f || !f.classList.contains("imp-form")) return;
    var pf = f.querySelector("select[name='opt.pandoc_format']"), m = pf && PF[(t.files[0].name.split(".").pop() || "").toLowerCase()];
    if (m) pf.value = m; // a Pandoc file's format follows its name
    f.querySelectorAll("input[type=file]").forEach(function (i) { if (i !== t) i.value = ""; }); // one source at a time
    f.requestSubmit();
  });
  d.addEventListener("htmx:configRequest", function (e) {
    var f = e.target.closest && e.target.closest("form[data-keep]");
    if (!f) return;
    var fd = e.detail.formData, scriv = f.dataset.keep === "scriv", keep = [], gone = [], root = "";
    fd.delete("f");
    f.querySelectorAll("input[name=f]").forEach(function (i) {
      Array.prototype.forEach.call(i.files, function (x) {
        var p = x.webkitRelativePath || x.name, q = p.split("/");
        root = root || q[0];
        (scriv ? SC.test(p) : (MD.test(p) && x.size <= 2097152) || MF.test(p) ? 1 : 0) ? keep.push([x, p]) : gone.push(q.slice(1).join("/") + "\t" + x.size);
      });
    });
    if (root) fd.append("root", root);
    if (gone.length && !scriv) fd.append("unsent", gone.join("\n"));
    keep.forEach(function (k) { fd.append("f", k[0], k[1]); });
  });
  d.addEventListener("htmx:xhr:progress", function (e) {
    var s = st(), l = e.detail;
    if (s && s.dataset.up && l.lengthComputable) s.textContent = s.dataset.up.replace("{a}", Math.round(l.loaded / 1048576)).replace("{b}", Math.max(1, Math.round(l.total / 1048576)));
  });
  d.addEventListener("htmx:afterSettle", function (e) {
    var s = st(), f = (e.target.querySelector && e.target.querySelector("[data-focus]")) || (e.target.matches && e.target.matches("[data-focus]") && e.target);
    if (s && e.target.id !== "imp-status") s.textContent = "";
    if (!f) return;
    f.focus({ preventScroll: true });
    var r = f.getBoundingClientRect();
    if (r.top < 0 || r.bottom > innerHeight) f.scrollIntoView({ block: "nearest" });
  });
})();
