/* Scríobh timeline pages: live hints for partial story dates, the entity picker, one more participant row, and
   keyboard moves of events in a book's own order. Vanilla, no dependencies, loaded on the timeline pages only.
   The pages work without it: the date fields are plain text the server checks, names are resolved by the server,
   and every move is a form. Nothing here changes data except the same order form the page already posts. */
(function () {
  "use strict";
  var doc = document, win = window;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  var seq = 0, status = $("#tl-live");
  if (!status) {
    status = el("div", "sr-only");
    status.setAttribute("role", "status"); status.setAttribute("aria-live", "polite"); status.setAttribute("aria-atomic", "true");
    doc.body.appendChild(status);
  }
  function say(m) { status.textContent = ""; win.setTimeout(function () { status.textContent = m; }, 40); }

  // ---- story dates: the same grammar as the server, so a mistake is explained while it is typed -------------------
  var MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
  var EX = "Use 1998, 1998-04, 1998-04-12 or 1998-04-12T19:30. A year can be negative, such as -0044.";
  function dim(y, m) { return m === 2 ? ((y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0)) ? 29 : 28) : (m === 4 || m === 6 || m === 9 || m === 11 ? 30 : 31); }
  function pad(n, w) { n = String(n); while (n.length < w) n = "0" + n; return n; }
  // parse returns null for empty, a string for a mistake, or the date.
  function parse(s) {
    s = s.trim();
    if (!s) return null;
    var m = /^(-?)(\d{1,4})(?:-(\d{2})(?:-(\d{2})(?:[T ](\d{2}):(\d{2}))?)?)?$/.exec(s);
    if (!m) {
      if (/^-?\d{1,4}-\d(-|$)/.test(s) || /^-?\d{1,4}-\d{2}-\d(\D|$)/.test(s)) return "Write the month and the day with two digits, such as 1998-04-02. " + EX;
      if (/^\d{5,}/.test(s)) return "A year has at most four digits. " + EX;
      return "That is not a date the timeline can read. " + EX;
    }
    var d = { y: (m[1] ? -1 : 1) * +m[2], mo: m[3] ? +m[3] : 0, d: m[4] ? +m[4] : 0, h: m[5] ? +m[5] : -1, mi: m[6] ? +m[6] : 0 };
    if (m[3] && (d.mo < 1 || d.mo > 12)) return "There is no month " + m[3] + ". Months run from 01 to 12.";
    if (m[4] && (d.d < 1 || d.d > dim(d.y, d.mo))) return MONTHS[d.mo - 1] + " has " + dim(d.y, d.mo) + " days that year, so " + m[4] + " is not a day of it.";
    if (m[5] && (d.h > 23 || d.mi > 59)) return "A time runs from 00:00 to 23:59.";
    return d;
  }
  function key(y, mo, d, min) { return pad(y + 10000, 5) + pad(mo, 2) + pad(d, 2) + pad(min, 4); }
  function startKey(d) { return key(d.y, d.mo, d.d, d.h >= 0 ? d.h * 60 + d.mi + 1 : 0); }
  function endKey(d) {
    if (d.h >= 0) return key(d.y, d.mo, d.d, d.h * 60 + d.mi + 1);
    if (d.d) return key(d.y, d.mo, d.d, 1441);
    if (d.mo) return key(d.y, d.mo, dim(d.y, d.mo), 1441);
    return key(d.y, 12, 31, 1441);
  }
  function reads(d) {
    var y = d.y < 0 ? "year " + d.y : String(d.y);
    if (d.h >= 0) return "At " + pad(d.h, 2) + ":" + pad(d.mi, 2) + " on " + d.d + " " + MONTHS[d.mo - 1] + " " + y + ".";
    if (d.d) return "The day " + d.d + " " + MONTHS[d.mo - 1] + " " + y + ".";
    if (d.mo) return "The month of " + MONTHS[d.mo - 1] + " " + y + ".";
    return "The whole of " + y + ".";
  }
  function dateHint(inp) {
    var hint = doc.getElementById(inp.getAttribute("aria-describedby").split(" ")[0]), v = parse(inp.value), msg = "", bad = false;
    if (!hint.dataset.def) hint.dataset.def = hint.textContent;
    if (typeof v === "string") { msg = v; bad = true; } else if (v) {
      msg = reads(v);
      var other = inp.dataset.pair && doc.getElementById(inp.dataset.pair), o = other && parse(other.value);
      if (o && typeof o !== "string") {
        var a = inp.name === "story_date_end" ? o : v, b = inp.name === "story_date_end" ? v : o;
        if (endKey(b) < startKey(a)) { msg = "The end is before the start."; bad = true; }
      }
    } else msg = hint.dataset.def;
    if (msg !== hint.dataset.def && inp.form && inp.form.classList.contains("tl-filter")) msg = $("label[for=" + inp.id + "]").textContent + ": " + msg; // the hints sit below the buttons, so say whose it is
    hint.textContent = msg;
    hint.classList.toggle("is-bad", bad);
    if (bad) inp.setAttribute("aria-invalid", "true"); else inp.removeAttribute("aria-invalid");
  }
  $$("[data-date]").forEach(function (inp) {
    var t = 0, run = function () { dateHint(inp); var p = inp.dataset.pair && doc.getElementById(inp.dataset.pair); if (p && p.value) dateHint(p); };
    inp.addEventListener("input", function () { win.clearTimeout(t); t = win.setTimeout(run, 250); });
    inp.addEventListener("blur", function () { win.clearTimeout(t); run(); });
    if (inp.value) dateHint(inp);
  });

  // ---- the entity picker: a combobox over the Compendium's own entity list ---------------------------------------
  function picker(inp) {
    var wrap = el("div", "tl-cb"), lb = el("ul", "tl-lb"), id = "tl-lb-" + (++seq), items = [], sel = -1, timer = 0, asked = 0;
    inp.parentNode.insertBefore(wrap, inp); wrap.appendChild(inp); wrap.appendChild(lb);
    lb.id = id; lb.hidden = true; lb.setAttribute("role", "listbox"); lb.setAttribute("aria-label", "Matches in this Compendium");
    inp.setAttribute("role", "combobox"); inp.setAttribute("aria-autocomplete", "list"); inp.setAttribute("aria-expanded", "false"); inp.setAttribute("aria-controls", id);
    function mark(i) {
      sel = i;
      items.forEach(function (li, k) { li.setAttribute("aria-selected", k === i ? "true" : "false"); });
      if (i >= 0) { inp.setAttribute("aria-activedescendant", items[i].id); items[i].scrollIntoView({ block: "nearest" }); } else inp.removeAttribute("aria-activedescendant");
    }
    function close() { lb.hidden = true; inp.setAttribute("aria-expanded", "false"); mark(-1); }
    function pick(i) { if (items[i] && items[i].dataset.name) { inp.value = items[i].dataset.name; say(items[i].dataset.name + " chosen."); } close(); }
    function draw(list) {
      lb.textContent = ""; items = [];
      list.forEach(function (e, i) {
        var li = el("li", "", ""); li.id = id + "-" + i; li.setAttribute("role", "option"); li.setAttribute("aria-selected", "false"); li.dataset.name = e.name;
        li.appendChild(el("span", "", e.name)); li.appendChild(el("span", "fine", e.kind));
        li.addEventListener("mousedown", function (ev) { ev.preventDefault(); pick(i); });
        lb.appendChild(li); items.push(li);
      });
      if (!list.length) { var n = el("li", "none", "Nothing here has that name. The server will say so if you save it as typed."); n.setAttribute("role", "option"); n.setAttribute("aria-disabled", "true"); lb.appendChild(n); }
      lb.hidden = false; inp.setAttribute("aria-expanded", "true"); mark(-1);
      say(list.length ? list.length + (list.length === 1 ? " match." : " matches.") : "No matches.");
    }
    function ask() {
      var q = inp.value.trim(), mine = ++asked;
      if (!q) { close(); return; }
      var url = "/api/v1/compendiums/" + encodeURIComponent(inp.dataset.compendium) + "/entities?limit=8&q=" + encodeURIComponent(q) + (inp.dataset.kind ? "&kind=" + encodeURIComponent(inp.dataset.kind) : "");
      fetch(url, { credentials: "same-origin", headers: { Accept: "application/json" } }).then(function (r) { return r.ok ? r.json() : { items: [] }; })
        .then(function (j) { if (mine === asked) draw(j.items || []); }, function () { if (mine === asked) close(); });
    }
    inp.addEventListener("input", function () { win.clearTimeout(timer); timer = win.setTimeout(ask, 150); });
    inp.addEventListener("keydown", function (e) {
      var open = !lb.hidden && items.length;
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        if (!open) { if (e.key === "ArrowDown") ask(); return; }
        e.preventDefault(); mark(e.key === "ArrowDown" ? (sel + 1) % items.length : (sel <= 0 ? items.length - 1 : sel - 1));
      } else if (e.key === "Enter" && open && sel >= 0) { e.preventDefault(); pick(sel); }
      else if (e.key === "Escape" && !lb.hidden) { e.preventDefault(); e.stopPropagation(); close(); }
      else if (e.key === "Tab") close();
    });
    inp.addEventListener("blur", function () { win.setTimeout(close, 120); });
  }
  $$("[data-picker]").forEach(picker);

  var add = $("#tl-addp");
  if (add) {
    add.hidden = false;
    add.addEventListener("click", function () {
      var list = $("#tl-prows"), rows = $$(".tl-prow", list);
      if (rows.length >= 100) return;
      var n = rows.length, c = rows[rows.length - 1].cloneNode(true), a = $("[name=person_name]", c), r = $("[name=person_role]", c), w = $(".tl-cb", c);
      if (w) { w.parentNode.insertBefore(a, w); w.remove(); }
      a.value = r.value = ""; a.removeAttribute("aria-expanded"); a.removeAttribute("aria-controls"); a.removeAttribute("aria-activedescendant");
      a.id = "ev-pn-" + n; r.id = "ev-pr-" + n;
      $$("label", c)[0].setAttribute("for", a.id); $$("label", c)[1].setAttribute("for", r.id);
      list.appendChild(c); picker(a); a.focus(); say("Another row added.");
    });
  }

  // ---- a book's own order: Earlier and Later, or Alt with the arrow keys, queued so quick moves cannot cross ------
  var root = $("#tl[data-kind=project]");
  if (!root || root.dataset.movable !== "true") return;
  var queue = Promise.resolve(), pid = root.dataset.project;
  var table = function () { return $("#tl-ptable"); }, body = function () { return $("#tl-body"); };
  function resync(focusId) {
    return fetch("/ui/projects/" + pid + "/timeline/rows?order=narrative&offset=" + (root.dataset.offset || 0), { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } })
      .then(function (r) { if (!r.ok) throw r; return r.text(); })
      .then(function (t) { table().outerHTML = t; var b = focusId && $("#tl-row-" + focusId + " button:not([disabled])"); if (b) b.focus(); },
        function () { say("Could not refresh the list. Reload the page to see the order the server holds."); });
  }
  function send(f, dir, id, title) {
    var data = new URLSearchParams(new FormData(f)); data.set("dir", dir);
    queue = queue.then(function () {
      return fetch(f.action, { method: "POST", credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" }, body: data }).then(function (r) {
        if (r.ok) return null;
        return r.text().then(function (t) {
          var m = ""; try { m = JSON.parse(t).error.message; } catch (e) { /* not JSON */ }
          say("Could not move " + title + ". " + (m || "Try again in a moment.") + " The list is shown as the server holds it.");
          return resync(id);
        });
      }, function () { say("Could not reach the server, so " + title + " was not moved."); return resync(id); });
    });
  }
  function states(rows, before, after) {
    rows.forEach(function (tr, k) {
      var up = $("[value=earlier]", tr), dn = $("[value=later]", tr);
      if (up) up.disabled = k === 0 && !before;
      if (dn) dn.disabled = k === rows.length - 1 && !after;
    });
  }
  function move(tr, dir, btn) {
    var rows = $$("tr[data-event]", body()), i = rows.indexOf(tr), j = dir === "earlier" ? i - 1 : i + 1, f = $("form", tr), id = tr.dataset.event, title = tr.dataset.title;
    var before = !$("[value=earlier]", rows[0]).disabled, after = !$("[value=later]", rows[rows.length - 1]).disabled;
    if (j < 0 || j >= rows.length) { // the neighbour is on another page: the server decides and the page is redrawn
      if ((dir === "earlier" && !before) || (dir === "later" && !after)) return;
      send(f, dir, id, title); queue = queue.then(function () { return resync(id); }); say("Moved " + title + " " + dir + ".");
      return;
    }
    var other = rows[j], b = body(), ph = el("tr");
    var p1 = $(".tl-pos", tr), p2 = $(".tl-pos", other), t = p1.textContent; p1.textContent = p2.textContent; p2.textContent = t;
    b.insertBefore(ph, tr); b.insertBefore(tr, other); b.insertBefore(other, ph); b.removeChild(ph);
    $$(".tr-moved", b).forEach(function (r) { r.classList.remove("tr-moved"); });
    tr.classList.add("tr-moved");
    states($$("tr[data-event]", b), before, after);
    var keep = btn && !btn.disabled ? btn : $("button:not([disabled])", tr) || $(".tl-title", tr);
    keep.focus();
    say("Moved " + title + " " + dir + ". It is now number " + p1.textContent + ".");
    send(f, dir, id, title);
  }
  doc.addEventListener("submit", function (e) {
    var f = e.target;
    if (!f.classList || !f.classList.contains("tl-mf") || !e.submitter || !e.submitter.value) return;
    e.preventDefault(); move(f.closest("tr"), e.submitter.value, e.submitter);
  });
  doc.addEventListener("keydown", function (e) {
    if (!e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
    var tr = e.target.closest && e.target.closest("tr[data-event]");
    if (!tr || !body() || !body().contains(tr)) return;
    var dir = e.key === "ArrowUp" ? "earlier" : "later", b = $("[value=" + dir + "]", tr);
    e.preventDefault();
    if (b && !b.disabled) move(tr, dir, b); else say(tr.dataset.title + " is already " + (dir === "earlier" ? "first" : "last") + ".");
  });
})();
