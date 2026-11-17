/* Scríobh command palette (Mod+K) and quick switcher (Mod+O).
   Loaded on every signed-in page, with its own size budget. It builds its commands from
   the page: the same everywhere (library, settings, sign out), plus the chapters of the
   open project and the writing view's actions when there is one. */
(function () {
  "use strict";
  var doc = document, win = window;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var pal = $("#palette"), input = $("#palette-input"), list = $("#palette-list"), empty = $("#palette-empty");
  if (!pal || !input || !list) return;
  var S = (win.scriobh = win.scriobh || {});
  var mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent), MOD = mac ? "⌘" : "Ctrl";
  var write = $("#write[data-doc]"), canEdit = write && write.dataset.canEdit === "true";

  // ---- the commands ------------------------------------------------------------
  var items = [], shown = [], active = -1, returnTo = null, caret = null, mode = "", uid = 0, fetched = 0;
  function go(href) { if (S.write && S.write.go) S.write.go(href); else win.location.assign(href); }
  function click(sel) { var e = $(sel); if (e) e.click(); }
  function add(o) { o.id = "cmd-" + uid++; items.push(o); } // ids never repeat, however often the list is rebuilt
  add({ label: "Go to library", run: function () { go("/"); } });
  // A dialog is loaded with htmx on pages without a manuscript. On a writing page the same forms open as pages through
  // go(), which saves first and stays put if it cannot: htmx would follow a sign-in redirect and drop the buffer.
  function dialog(href) { if (write) go(href); else if (win.htmx) win.htmx.ajax("GET", href, { target: "#dialog-body", swap: "innerHTML" }); }
  if (pal.dataset.writer === "true") add({ label: "New project", run: function () { dialog("/ui/projects/new"); } });
  if (write) {
    add({ label: "Go to chapter…", key: MOD + " O", run: function () { click("#jump-open"); } });
    if (canEdit) {
      add({ label: "Add chapter", run: function () { click('[data-add="chapter"]'); } });
      add({ label: "Add scene", run: function () { click('[data-add="scene"]'); } });
      add({ label: "Add part", run: function () { click('[data-add="part"]'); } });
    }
    if (canEdit) {
      add({ label: "Split at cursor", run: function () { S.split(); } });
      add({ label: "Export as Markdown\u2026", run: function () { dialog("/ui/projects/" + write.dataset.project + "/export"); } });
    }
    add({ label: "Show or hide the details", run: function () { click("#insp-open"); } });
    add({ label: "Show or hide the versions", run: function () { click("#ver-open"); } });
    add({ label: "Show or hide the underlined names from the bible", run: function () { if (S.mentions) S.mentions.toggle(); } });
    add({ label: "Open the entity under the cursor", run: function () { if (S.mentions) S.mentions.open(); } });
    // Alternatives, ghosts and the stash: each is the same command as its key (js/alts.js).
    [["Add an alternative to the selection", "alt"], ["Next wording of the alternative here", "next"], ["Previous wording of the alternative here", "previous"], ["Show the original wording", "original"],
     ["Ghost the selection", "ghost"], ["Revive the ghosted text here", "revive"], ["Send the selection to the stash", "stash"], ["Play a sound when wordings change, on or off", "sound"]].forEach(function (c) {
      add({ label: c[0], run: function () { if (S.alts) S.alts.cmd(c[1]); } });
    });
    // The Lab's local checks work with AI off; the writing partner's commands exist only when AI is on for the project.
    if (canEdit) {
      add({ label: "Lab: check this chapter (no AI needed)", run: function () { S.labWant = "check"; if (S.inspector) S.inspector.open("lab", true); } });
      add({ label: "Show the Lab", run: function () { if (S.inspector) S.inspector.open("lab", true); } });
      add({ label: "Lab: next mark", run: function () { if (S.lab) S.lab.step(1); else if (S.inspector) S.inspector.open("lab", true); } });
      add({ label: "Lab: previous mark", run: function () { if (S.lab) S.lab.step(-1); else if (S.inspector) S.inspector.open("lab", true); } });
    }
    if (canEdit && write.dataset.ai === "on") {
      [["Ask the writing partner about the selection\u2026", "menu", "Alt Shift I"], ["Go to the proposal that is waiting", "proposal", "Alt Shift J"], ["Rewrite the selection\u2026", "rewrite"], ["Tighten the selection", "tighten"], ["Expand the selection", "expand"], ["Change the tone of the selection\u2026", "tone"],
       ["Give me alternatives for the selection", "alternatives"], ["Chat about this chapter\u2026", "chat"], ["Critique this chapter", "critique"], ["Summarise this chapter into its synopsis", "summary"],
       ["Check this chapter for continuity", "continuity"], ["Look for names that are not in the bible yet", "extract"], ["Show the reviews", "reviews"]].forEach(function (c) {
        add({ label: c[0], key: c[2], run: function () { doc.dispatchEvent(new win.CustomEvent("scriobh:ai", { detail: c[1] })); } });
      });
    }
    add({ label: "Show the alternatives of this chapter", run: function () { if (S.inspector) S.inspector.open("alts", true); } });
    if (canEdit) add({ label: "Show the stash", run: function () { if (S.inspector) S.inspector.open("stash", true); } });
    add({ label: "Show who is in this scene", run: function () { if (S.inspector) S.inspector.open("scene", true); } });
    if (canEdit) add({ label: "Add files to this project\u2026", run: function () { go("/p/" + write.dataset.slug + "/import"); } });
    add({ label: "Search this project", run: function () { go("/p/" + write.dataset.slug + "/search"); } });
    add({ label: "Show or hide Markdown source", key: MOD + " E", run: function () { if (S.write) S.write.editor.toggleSource(); } });
    if (doc.getElementById("sprint-open")) add({ label: "Start or stop a sprint\u2026", run: function () { click("#sprint-open"); } });
    if (write.dataset.next) add({ label: "Next chapter", key: "Alt PgDn", run: function () { go(write.dataset.next); } });
    if (write.dataset.prev) add({ label: "Previous chapter", key: "Alt PgUp", run: function () { go(write.dataset.prev); } });
  }
  // Push now, from anywhere in the writing view: the same route the Git page's button uses, answered as JSON.
  function pushNow() {
    var csrf = ($('meta[name="csrf-token"]') || {}).content || "";
    var say = function (m) { if (S.toast) S.toast(m); };
    win.fetch("/ui/projects/" + write.dataset.project + "/git/push", { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": csrf, "X-Scriobh-Fetch": "1", "Content-Type": "application/x-www-form-urlencoded" }, body: "_csrf=" + encodeURIComponent(csrf) }).then(function (r) {
      return r.json().then(function (j) { say(r.ok ? j.message + " Open Git sync to follow it." : ((j.error && j.error.message) || "The push could not be started. Open Git sync to see why.")); }, function () { say("The push could not be started. Open Git sync to see why."); });
    }, function () { say("Could not reach the server."); });
  }
  // The project's own pages, from any page of the project.
  var pm = /^\/p\/([^\/]+)/.exec(win.location.pathname), slug = pm && pm[1];
  if (slug) {
    add({ label: "Go to the outline", run: function () { go("/p/" + slug + "/outline"); } });
    add({ label: "Go to the goals", run: function () { go("/p/" + slug + "/goals"); } });
    // Each of a project's own pages: the label, and where it is.
    [["Go to the corkboard", "cards"], ["Go to the status board", "board"], ["Go to the entities of this project", "entities"],
     ["Go to the Compendium of this project", "compendium"], ["Go to the people of this project", "people"],
     ["Go to the research of this project", "research"], ["Go to the research board", "research/board"], ["Go to the scratchpad of this project", "scratch"], ["Go to the attachments of this project", "files"], ["Go to the timeline of this project", "timeline"], ["Go to the AI page of this project", "ai"]].forEach(function (p) {
      add({ label: p[0], run: function () { go("/p/" + slug + "/" + p[1]); } });
    });
    add({ label: "Reading mode", run: function () { go("/p/" + slug + "/read" + (write ? "/" + write.dataset.doc : "")); } });
    add({ label: "Go to the editions (imported books) of this project", run: function () { go("/p/" + slug + "/editions"); } });
    if (!write || canEdit) add({ label: "Git sync", run: function () { go("/p/" + slug + "/git"); } });
    if (write && canEdit) add({ label: "Push to Git now", run: function () { pushNow(); } });
    if (!write) add({ label: "Search this project", run: function () { go("/p/" + slug + "/search"); } });
    // A project with a passphrase: lock it now (js/gate.js covers the page and holds what is typed). The words come from the page.
    if (doc.body.dataset.gate) add({ label: doc.body.dataset.gateLockLabel || "", run: function () { click("[data-gate-lock]"); } });
  }
  if (pal.dataset.writer === "true") {
    add({ label: "Import a folder or zip\u2026", run: function () { go("/import"); } });
    add({ label: "Import from a Git repository\u2026", run: function () { go("/import/git"); } });
  }
  add({ label: "AI providers and settings", run: function () { go("/settings/ai"); } });
  add({ label: "Compendiums", run: function () { go("/compendiums"); } });
  if (pal.dataset.writer === "true") add({ label: "New Compendium\u2026", run: function () { dialog("/ui/compendiums/new"); } });
  var cm = /^\/c\/([^\/]+)/.exec(win.location.pathname);
  if (cm) [["Entities of this Compendium", "entities"], ["Custom fields of this Compendium", "fields"], ["Research of this Compendium", "research"],
    ["Research board of this Compendium", "research/board"], ["Scratchpad of this Compendium", "scratch"], ["Attachments of this Compendium", "files"], ["Timeline of this Compendium", "timeline"]].forEach(function (p) {
    add({ label: p[0], run: function () { go("/c/" + cm[1] + "/" + p[1]); } });
  });
  add({ label: "Search every project", run: function () { go("/search"); } });
  add({ label: "Go to the writing studio", run: function () { go("/studio"); } });
  add({ label: "Writing goals", run: function () { go("/studio/goals"); } });
  add({ label: "Settings", run: function () { go("/settings"); } });
  add({ label: "Archived projects", run: function () { go("/?state=archived"); } });
  add({ label: "Trash", run: function () { go("/?state=trashed"); } });
  add({ label: "Sign out", run: function () { var f = $("#signout"); if (f) f.requestSubmit(); } });
  // Chapters of the open project: found by typing, or listed by the switcher.
  // They are read from the page each time the palette opens, so a renamed or moved chapter is never stale.
  function chapters() {
    items = items.filter(function (o) { return o.grp !== "chapter"; });
    if (!write) return;
    Array.prototype.forEach.call(doc.querySelectorAll("#jump-list li"), function (li) {
      add({ grp: "chapter", label: li.dataset.label, sub: (li.querySelector(".j-group") || {}).textContent || "", run: function () { go(li.dataset.href); } });
    });
  }
  chapters();
  // The projects are asked for each time the palette opens (archived ones too, marked), so none is stale or missing.
  function loadProjects() {
    var mine = ++fetched;
    fetch("/api/v1/projects?state=all", { credentials: "same-origin", headers: { Accept: "application/json" } }).then(function (r) { return r.ok ? r.json() : null; }).then(function (j) {
      if (!j || mine !== fetched) return;
      items = items.filter(function (o) { return o.grp !== "project"; });
      j.items.forEach(function (p) {
        if (p.trashed) return;
        add({ grp: "project", label: p.title, sub: p.archived ? "Archived project" : "Project", run: function () { go("/p/" + (p.address || p.slug) + (p.last_document_id ? "/d/" + p.last_document_id : "")); } });
      });
      render();
    }, function () { /* the commands still work without the list */ });
  }

  // ---- matching and painting ----------------------------------------------------------
  // The query is found whole inside the label (best at the start of a word), or each of its words begins a word of
  // the label. Letters scattered across a long title are not a match; short labels allow it, ranked last.
  function match(label, q) {
    var l = label.toLowerCase(), at = l.indexOf(q), idx = [], i;
    if (at >= 0) {
      for (i = 0; i < q.length; i++) idx.push(at + i);
      return { idx: idx, score: (at === 0 || /[^a-z0-9]/.test(l[at - 1]) ? 0 : 50) + at };
    }
    var starts = [], used = {}, ok = true;
    l.replace(/[a-z0-9\u00c0-\uffff]+/g, function (w, p) { starts.push(p); return w; });
    q.split(/\s+/).filter(Boolean).forEach(function (w) {
      var s = starts.filter(function (p) { return !used[p] && l.substr(p, w.length) === w; })[0];
      if (s === undefined) { ok = false; return; }
      used[s] = 1;
      for (i = 0; i < w.length; i++) idx.push(s + i);
    });
    if (ok && idx.length) return { idx: idx.sort(function (a, b) { return a - b; }), score: 100 + idx[0] };
    // Last and least: letters in order, for short labels only (nw finds New project). A long title never bolds scattered letters.
    if (l.length > 18) return null;
    idx = [];
    for (i = 0; idx.length < q.length && i < l.length; i++) if (l[i] === q.charAt(idx.length)) idx.push(i);
    return idx.length === q.length ? { idx: idx, score: 300 + idx[idx.length - 1] - idx[0] } : null;
  }
  function row(o, idx) {
    var li = doc.createElement("li"), lab = doc.createElement("span"), set = {}, run = "", bold = false;
    li.id = o.id; li.setAttribute("role", "option");
    lab.className = "cmd-label";
    (idx || []).forEach(function (n) { set[n] = 1; });
    function flush() {
      if (!run) return;
      if (bold) { var b = doc.createElement("b"); b.textContent = run; lab.appendChild(b); } else lab.appendChild(doc.createTextNode(run));
      run = "";
    }
    for (var i = 0; i < o.label.length; i++) { if (!!set[i] !== bold) { flush(); bold = !!set[i]; } run += o.label[i]; }
    flush();
    li.appendChild(lab);
    if (o.sub || o.key) { var k = doc.createElement("span"); k.className = "cmd-key"; k.textContent = o.sub || o.key; li.appendChild(k); }
    return li;
  }
  function render() {
    var q = input.value.trim().toLowerCase(), hits = [];
    items.forEach(function (o, n) {
      // Chapters and projects show when typing, or all of them in the switcher; commands show in the palette.
      if (mode === "switch" ? !o.grp : (o.grp && !q)) return;
      var m = q ? match(o.label, q) : { idx: [], score: n };
      if (m) hits.push({ o: o, m: m });
    });
    if (q) hits.sort(function (a, b) { return a.m.score - b.m.score; });
    hits = hits.slice(0, 60);
    shown = hits.map(function (h) { return h.o; });
    list.replaceChildren.apply(list, hits.map(function (h) { return row(h.o, h.m.idx); }));
    empty.hidden = shown.length > 0;
    select(shown.length ? 0 : -1);
  }
  function select(n) {
    active = n;
    Array.prototype.forEach.call(list.children, function (li) { li.removeAttribute("aria-selected"); });
    var li = list.children[n];
    if (li) { li.setAttribute("aria-selected", "true"); input.setAttribute("aria-activedescendant", li.id); li.scrollIntoView({ block: "nearest" }); }
    else input.removeAttribute("aria-activedescendant");
  }

  // ---- opening, running ------------------------------------------------------------------
  function open(m) {
    if ($("dialog[open]:not(#palette)")) return;
    if (pal.open) { pal.close(); return; }
    doc.querySelectorAll("[popover]:popover-open").forEach(function (p) { p.hidePopover(); });
    mode = m || "";
    chapters();
    returnTo = doc.activeElement;
    // A text box loses its caret when the dialog takes the page inert; it gets it back on close.
    caret = returnTo && returnTo.selectionStart != null ? [returnTo.selectionStart, returnTo.selectionEnd] : null;
    input.value = "";
    input.setAttribute("aria-expanded", "true");
    input.placeholder = mode === "switch" ? "Go to a chapter or project" : "Type a command";
    input.setAttribute("aria-label", mode === "switch" ? "Go to" : "Command");
    loadProjects();
    render();
    pal.showModal();
    input.focus();
  }
  function run(o) {
    if (!o) return;
    var back = returnTo;
    pal.close();
    if (back && back.isConnected) { back.focus({ preventScroll: true }); if (caret) back.setSelectionRange(caret[0], caret[1]); }
    o.run();
  }
  input.addEventListener("input", render);
  input.addEventListener("keydown", function (e) {
    var n = shown.length;
    if (e.key === "ArrowDown") { e.preventDefault(); if (n) select((active + 1) % n); }
    else if (e.key === "ArrowUp") { e.preventDefault(); if (n) select((active - 1 + n) % n); }
    else if (e.key === "Home") { e.preventDefault(); if (n) select(0); }
    else if (e.key === "End") { e.preventDefault(); if (n) select(n - 1); }
    else if (e.key === "Enter") { e.preventDefault(); run(shown[active]); }
  });
  list.addEventListener("click", function (e) { var li = e.target.closest("[role=option]"); if (li) run(shown[Array.prototype.indexOf.call(list.children, li)]); });
  list.addEventListener("pointermove", function (e) {
    var li = e.target.closest("[role=option]"), n = li ? Array.prototype.indexOf.call(list.children, li) : -1;
    if (n >= 0 && n !== active) select(n);
  });
  var down = false;
  pal.addEventListener("pointerdown", function (e) { down = e.target === pal; });
  pal.addEventListener("click", function (e) { if (e.target === pal && down) pal.close(); });
  pal.addEventListener("close", function () {
    input.setAttribute("aria-expanded", "false");
    if (returnTo && returnTo.isConnected && (!doc.activeElement || doc.activeElement === doc.body)) returnTo.focus({ preventScroll: true });
  });
  doc.addEventListener("keydown", function (e) {
    // The editor keeps Mod+K for links (F-063); Mod+Shift+P opens the palette from inside the page.
    if (e.defaultPrevented || !(e.ctrlKey || e.metaKey) || e.altKey) return;
    var k = e.key.toLowerCase();
    // Mod+/ also opens it: Firefox keeps Mod+Shift+P for private windows.
    if (e.shiftKey) { if (k === "p") { e.preventDefault(); open(""); } return; }
    if (k === "/") { e.preventDefault(); open(""); return; }
    if (k === "k") { e.preventDefault(); open(""); }
    else if (k === "o") { e.preventDefault(); open("switch"); }
  });
  doc.addEventListener("click", function (e) { if (e.target.closest("[data-palette-open]")) open(""); });
  // ---- split at cursor (doc.split) ------------------------------------------------------------------
  // From Source mode, where the caret's place in the Markdown text is exact. It also works in a
  // read-only chapter (past 25,000 words), which is how such a chapter gets cut down to size.
  // The tab id doc.js keeps in sessionStorage, so the server's lease also guards a split.
  function tabId() { try { return win.sessionStorage.getItem("scriobh:tab") || undefined; } catch (e) { return undefined; } }
  S.split = function () {
    var W = S.write;
    if (!W || !W.cfg.canEdit) return;
    var ed = W.editor, say = function (m) { if (S.toast) S.toast(m); }, st = $("#save-state");
    if (st && st.dataset.state === "locked") return say("Another tab is writing this chapter. Take over there first.");
    if (ed.mode() !== "source") {
      ed.toggleSource(true);
      say("Put the cursor where the new document should begin, then choose Split at cursor again.");
      return;
    }
    var at = $(".scriobh-source").selectionStart;
    W.flushAll().then(function (ok) {
      if (!ok) return say("Not saved yet. Staying here so nothing is lost.");
      var md = ed.getMarkdown();
      fetch("/api/v1/docs/" + W.cfg.id + "/split", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ base_revision: W.rev, offset: Math.min(at, md.length), tab_id: tabId() }) })
        .then(function (r) { return r.json().then(function (j) { return { ok: r.ok, j: j }; }, function () { return { ok: false }; }); })
        .then(function (x) {
          if (x.ok) win.location.assign("/p/" + W.cfg.slug + "/d/" + x.j.tail.id + "?new=1"); // the new document takes a title first
          else say((x.j && x.j.error && x.j.error.message) || "The split did not go through. Nothing was changed.");
        }, function () { say("Could not reach the server. Nothing was changed."); });
    });
  };

  // ---- the export dialog: fetch the file, so the page can say what was recorded -----------------------------
  doc.addEventListener("submit", function (e) {
    var f = e.target;
    if (!f.matches || !f.matches("form[data-download]")) return;
    e.preventDefault();
    var status = $("#export-status", f), btn = $("button[type=submit]", f), csrf = ($('meta[name="csrf-token"]') || {}).content || "";
    var fail = function (m) { status.textContent = m; status.hidden = false; };
    btn.disabled = true;
    status.hidden = true;
    fetch(f.action, { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": csrf, "X-Scriobh-Fetch": "1", "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams(new FormData(f)) })
      .then(function (r) {
        if (!r.ok) {
          return r.json().then(function (j) { throw new Error(r.status === 401 ? "Signed out. Sign in in another tab, then try again." : (j && j.error && j.error.message) || "The export did not go through."); },
            function () { throw new Error("The export did not go through."); });
        }
        var n = r.headers.get("X-Export-Documents"), id = r.headers.get("X-Export-Id");
        var name = (/filename="?([^";]+)"?/i.exec(r.headers.get("Content-Disposition") || "") || [])[1] || "export";
        return r.blob().then(function (b) {
          var a = doc.createElement("a");
          a.href = URL.createObjectURL(b);
          a.download = name;
          doc.body.appendChild(a);
          a.click();
          a.remove();
          setTimeout(function () { URL.revokeObjectURL(a.href); }, 5000);
          var left = r.headers.get("X-Export-Skipped"), note = left && f.dataset.skipped ? " " + f.dataset.skipped.split("{n}").join(left) : "";
          if (S.toast) S.toast("Exported " + n + (n === "1" ? " document" : " documents") + ". Recorded with a manifest (" + id + ")." + note);
          var d = $("#dialog");
          if (d && d.open) d.close();
        });
      })
      .catch(function (err) { fail(err.message || "Could not reach the server. Nothing was changed."); })
      .then(function () { btn.disabled = false; });
  });
  var hint = $("[data-mod]");
  if (hint && mac) hint.textContent = "⌘";
  S.palette = { open: open };
})();
