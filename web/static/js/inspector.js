/* Scríobh writing view: the inspector. A panel beside the page (a bottom sheet on a phone) with the
   document's details (synopsis, status, target, point of view, place, story dates, compile flag), the
   links out of it and into it, and the Versions tab (versions.js, fetched the first time it is opened).

   It never moves focus unless the person opens it, and nothing in it changes size while they type in the
   editor: results are written into fixed places. Details are saved through doc.update with the revision
   the writing view holds, and the revision it gets back is handed to the save loop. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  var box = doc.getElementById("inspector");
  if (!W || !box) return;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  var api = W.api, id = W.cfg.id, canEdit = W.cfg.canEdit, slug = W.cfg.slug;
  // Whether the panel is open, and on which tab, is kept in a cookie so the server draws it open on the next page:
  // opening it after the editor is already typeable would move the page.
  function keep(v) {
    if (v === undefined) { var m = /(?:^|; )scriobh_insp=(\w*)/.exec(doc.cookie); return m ? m[1] : ""; }
    doc.cookie = "scriobh_insp=" + v + "; path=/; max-age=31536000; samesite=lax";
  }

  // ---- open, close, tabs -----------------------------------------------------------------------------
  var openers = $$(".insp-open"), tabs = $$(".insp-tab", box), panels = { details: $("#ipanel-details"), versions: $("#ipanel-versions"), scene: $("#ipanel-scene"), compendium: $("#ipanel-compendium"), alts: $("#ipanel-alts"), stash: $("#ipanel-stash"), lab: $("#ipanel-lab"), ai: $("#ipanel-ai"), comments: $("#ipanel-comments") };
  // The tabs that are their own files, and the object each one defines (the Alternatives tab is js/altpanel.js).
  var LAZY = { versions: "versions", scene: "scene", compendium: "lookup", alts: "altpanel", stash: "stash", lab: "lab", ai: "aipanel", comments: "review" };
  var wide = win.matchMedia("(min-width: 1150px)"), current = "";
  function show(name) {
    tabs.forEach(function (t) {
      var on = t.id === "itab-" + name;
      t.setAttribute("aria-selected", String(on));
      t.tabIndex = on ? 0 : -1;
    });
    Object.keys(panels).forEach(function (k) { if (panels[k]) panels[k].hidden = k !== name; });
    current = name;
    try { win.localStorage.setItem("scriobh:insp-tab", name); } catch (e) { /* not remembered here */ }
    reveal($("#itab-" + name));
    if (LAZY[name]) lazyPanel(name);
    if (name === "details") links();
  }
  // These tabs are their own files, fetched the first time the tab is shown.
  function lazyPanel(name) {
    var m = LAZY[name];
    if (S[m]) { S[m].show(panels[name]); return; }
    var t = el("script");
    t.src = $("#write").dataset[name + "Src"];
    t.onload = function () { if (S[m]) S[m].show(panels[name]); };
    t.onerror = function () {
      t.remove();
      var b = el("button"); b.type = "button"; b.className = "btn small"; b.textContent = "Try again";
      b.addEventListener("click", function () { lazyPanel(name); });
      var p = el("p"); p.setAttribute("role", "alert"); p.textContent = "That could not be loaded. Your text is unchanged.";
      panels[name].replaceChildren(p, b);
    };
    doc.head.appendChild(t);
  }
  // One row of tabs that scrolls; a button at an end says there are more. The tab that is shown is scrolled into the row, by the row alone.
  var strip = $(".insp-tabs", box), tl = $("#insp-tl"), tr = $("#insp-tr");
  function cue() { var m = strip.scrollWidth - strip.clientWidth; tl.hidden = strip.scrollLeft < 2; tr.hidden = strip.scrollLeft > m - 2; }
  function reveal(t) {
    if (t && t.offsetLeft < strip.scrollLeft + 44) strip.scrollLeft = t.offsetLeft - 44;
    else if (t && t.offsetLeft + t.offsetWidth > strip.scrollLeft + strip.clientWidth - 44) strip.scrollLeft = t.offsetLeft + t.offsetWidth - strip.clientWidth + 44;
    cue();
  }
  strip.addEventListener("scroll", cue);
  win.addEventListener("resize", cue);
  tl.addEventListener("click", function () { strip.scrollBy({ left: -strip.clientWidth * 0.7 }); });
  tr.addEventListener("click", function () { strip.scrollBy({ left: strip.clientWidth * 0.7 }); });
  function open(name, focus) {
    box.classList.add("is-open");
    doc.body.classList.add("insp-on");
    openers.forEach(function (b) { b.setAttribute("aria-expanded", String(b.dataset.panel ? b.dataset.panel === name : true)); });
    show(name);
    keep(name);
    if (focus) { var t = $("#itab-" + name); if (t) t.focus({ preventScroll: true }); }
  }
  function close(refocus) {
    var had = box.contains(doc.activeElement), name = current;
    box.classList.remove("is-open");
    doc.body.classList.remove("insp-on");
    openers.forEach(function (b) { b.setAttribute("aria-expanded", "false"); });
    current = "";
    keep("");
    if (had || refocus) { var back = $('.insp-open[data-panel="' + name + '"]') || $(".insp-open:not([data-panel])") || $(".insp-open"); if (back) back.focus({ preventScroll: true }); } // focus goes back to the button that opened it, and the page stays where it is
  }
  // The toggle with no tab of its own (the Sidebar button) opens on the tab used last, in this browser.
  function last() { var n; try { n = win.localStorage.getItem("scriobh:insp-tab"); } catch (e) { /* none */ } return panels[n] ? n : "details"; }
  openers.forEach(function (b) {
    b.hidden = false;
    b.addEventListener("click", function () {
      var name = b.dataset.panel || last();
      if (b.dataset.panel ? current === name : current) { close(true); return; }
      open(name, true);
    });
  });
  $("#insp-close").addEventListener("click", function () { close(true); });
  box.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !e.defaultPrevented) { e.preventDefault(); close(true); }
    var t = e.target.closest && e.target.closest(".insp-tab");
    if (!t) return;
    var i = tabs.indexOf(t), n = -1;
    if (e.key === "ArrowRight") n = (i + 1) % tabs.length; else if (e.key === "ArrowLeft") n = (i - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") n = 0; else if (e.key === "End") n = tabs.length - 1;
    if (n < 0) return;
    e.preventDefault();
    open(tabs[n].id.slice(5), true);
  });
  tabs.forEach(function (t) { t.addEventListener("click", function () { open(t.id.slice(5), false); }); });
  // Left open on a wide screen, it stays open from chapter to chapter. A phone always starts with it closed.
  var was = keep(), pre = box.classList.contains("pre");
  box.classList.remove("pre");
  if (was && wide.matches && panels[was]) open(was, false);
  else if (pre) box.classList.remove("is-open"); // drawn open for a wide screen, and this one is not
  // The selected words, for the Compendium tab.
  function selected() {
    var src = $(".scriobh-source:not([hidden])"), v = W.editor && W.editor.view, s = v && v.state.selection;
    return (src ? src.value.slice(src.selectionStart, src.selectionEnd) : s ? v.state.doc.textBetween(s.from, s.to, "\n") : "").trim();
  }
  // Back to the writing, the caret where it was and the page where it was, in the Source view too.
  function toEditor() {
    var src = $(".scriobh-source:not([hidden])");
    if (src) src.focus({ preventScroll: true }); else W.editor.view.focus();
  }
  // The Compendium tab: its search, the selected words (how 1), or a new entry from them ("new").
  function compendium(how) {
    var t = how ? selected() : "";
    S.lookupWant = { sel: /\n/.test(t) || t.length > 200 ? "" : t, isNew: how === "new" };
    open("compendium", false);
  }
  // The Comments tab: the editor's selection as it is now (taken at the key or the tap, after the browser's own is read), kept for
  // the tab's file, then the tab. Nothing is dispatched to the editor, so the words and the caret are as they were.
  function want(panel, how) {
    if (!panels[panel] || !W.editor) return;
    var src = $(".scriobh-source:not([hidden])"), v = W.editor.view, s;
    if (!src && S.alts) S.alts.sync();
    s = v.state.selection;
    S[panel + "Want"] = src ? { src: 1, from: src.selectionStart, to: src.selectionEnd, empty: how === "chapter" || src.selectionStart === src.selectionEnd, doc: src.value }
      : { from: s.from, to: s.to, empty: how === "chapter" || s.empty, doc: v.state.doc };
    open(panel, false);
  }
  S.inspector = { open: open, close: close, get current() { return current; }, flush: function () { return canEdit ? flush() : Promise.resolve(true); }, selected: selected, compendium: compendium, toEditor: toEditor, want: want };
  // Alt+Shift+C opens the Compendium tab with the selected words, Alt+Shift+M the Comments tab (comment on them, or on the chapter);
  // from inside the same tab, back to the editor.
  var chords = (S.chords = S.chords || { clash: [] }), who = "compendium: the panel", whoM = "comments: the panel";
  if (chords.KeyC && chords.KeyC !== who) chords.clash.push("KeyC: " + chords.KeyC + " and " + who);
  if (chords.KeyM && chords.KeyM !== whoM) chords.clash.push("KeyM: " + chords.KeyM + " and " + whoM);
  chords.KeyC = who;
  chords.KeyM = whoM;
  doc.addEventListener("keydown", function (e) {
    var m = e.code === "KeyM" && panels.comments; // a reader has no Comments tab: the key is not ours
    if ((e.code !== "KeyC" && !m) || !e.altKey || !e.shiftKey || e.ctrlKey || e.metaKey || e.isComposing || e.keyCode === 229) return;
    e.preventDefault(); e.stopPropagation();
    if (e.repeat) return;
    if (current === (m ? "comments" : "compendium") && box.contains(doc.activeElement)) { toEditor(); if (!wide.matches) close(false); }
    else if (m) want("comments"); else compendium(1);
  }, true);

  // ---- details ---------------------------------------------------------------------------------------
  var form = $("#insp-form"), note = $("#insp-saved");
  form.addEventListener("submit", function (e) { e.preventDefault(); });
  var fields = {};
  $$("[name]", form).forEach(function (f) { fields[f.name] = f; });
  function value(f) { return f.type === "checkbox" ? f.checked : f.name === "target_words" ? (parseInt(f.value, 10) || 0) : f.value; }
  var known = {}, queued = {}, rejected = {}, timer = 0, busy = false, noteTimer = 0;
  Object.keys(fields).forEach(function (k) { known[k] = value(fields[k]); });
  function say(msg, bad) {
    clearTimeout(noteTimer);
    note.textContent = msg;
    note.dataset.bad = bad ? "1" : "";
    if (msg && !bad) noteTimer = setTimeout(function () { note.textContent = ""; }, 3000);
  }
  function fieldError(name, msg) {
    var e = $('[data-err="' + name + '"]', form), f = fields[name];
    if (e) { e.id = "ierr-" + name; e.textContent = msg || ""; e.hidden = !msg; }
    if (f) {
      if (msg) { f.setAttribute("aria-invalid", "true"); f.setAttribute("aria-describedby", "ierr-" + name); }
      else { f.removeAttribute("aria-invalid"); f.removeAttribute("aria-describedby"); }
    }
  }
  function collect() {
    Object.keys(fields).forEach(function (k) {
      var v = value(fields[k]);
      if (rejected[k] !== undefined && rejected[k] === v) delete queued[k]; // refused as it is; held back until it changes
      else if (v !== known[k]) { queued[k] = v; delete rejected[k]; } else delete queued[k];
    });
    return Object.keys(queued).length > 0;
  }
  // flush() resolves true once nothing is waiting to be saved, false if it could not be: signing out and closing the
  // tab both go through it, so a field typed a moment ago is never left behind.
  var waiters = [];
  function settle(ok) { var w = waiters; waiters = []; w.forEach(function (f) { f(ok); }); }
  function flush() {
    clearTimeout(timer);
    if (!busy && !collect()) return Promise.resolve(true);
    return new Promise(function (res) { waiters.push(res); if (!busy) send(false); });
  }
  function send(retried) {
    clearTimeout(timer);
    if (busy) return;
    if (!collect()) { settle(true); return; }
    busy = true;
    var body = { base_revision: W.rev, tab_id: W.tab }, sent = {};
    Object.keys(queued).forEach(function (k) { body[k] = queued[k]; sent[k] = queued[k]; fieldError(k, ""); });
    api("PATCH", "/api/v1/docs/" + id, body).then(function (r) {
      busy = false;
      if (r.ok && r.json && r.json.item) {
        W.rev = r.json.item.revision;
        Object.keys(sent).forEach(function (k) { known[k] = sent[k]; if (queued[k] === sent[k]) delete queued[k]; });
        say("Saved");
        if (collect()) send(false); else settle(true);
        return;
      }
      var err = (r.json && r.json.error) || {};
      if (r.status === 409 && !retried && err.data && err.data.reason === "revision") {
        // Something else changed the chapter's details or text since: take its revision and try once more.
        api("GET", "/api/v1/docs/" + id).then(function (g) { if (g.ok && g.json) { if (g.json.item.revision > W.rev) W.rev = g.json.item.revision; send(true); } else { say("Could not save. Try again.", true); settle(false); } }, function () { settle(false); });
      } else if (r.status === 409) {
        say("This chapter is open for writing in another tab, so this change was not saved. Take over there, or close that tab.", true);
        settle(false);
      } else if (r.status === 422 && err.field && fields[err.field]) {
        fieldError(err.field, err.message || "That value cannot be saved.");
        rejected[err.field] = sent[err.field];
        delete queued[err.field];
        say("Not saved: " + (err.message || "check the highlighted field."), true);
        if (collect()) send(false); else settle(true); // the other fields of that request still go
      } else if (r.status === 401) {
        say("Signed out. Your change is kept here until you sign in again.", true);
        settle(false);
      } else {
        say("Could not save. It will be tried again.", true);
        timer = setTimeout(function () { send(false); }, 6000);
        settle(false);
      }
    }, function () { busy = false; say("Offline. It will be saved when the connection returns.", true); timer = setTimeout(function () { send(false); }, 6000); settle(false); });
  }
  if (canEdit) {
    form.addEventListener("input", function (e) {
      if (e.target.type === "checkbox" || e.target.tagName === "SELECT") return;
      fieldError(e.target.name, "");
      clearTimeout(timer);
      timer = setTimeout(function () { send(false); }, 900);
    });
    form.addEventListener("change", function () { send(false); });
    form.addEventListener("focusout", function () { send(false); });
    // Closing the tab: an ordinary request would be cut off, a keepalive one is finished by the browser.
    win.addEventListener("pagehide", function () {
      if (!collect()) return;
      var body = { base_revision: W.rev, tab_id: W.tab };
      Object.keys(queued).forEach(function (k) { body[k] = queued[k]; });
      try { win.fetch("/api/v1/docs/" + id, { method: "PATCH", keepalive: true, credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); } catch (e) { /* the field stays in the form */ }
    });
    // Signing out saves them first, and stays signed in if it cannot.
    var before = S.beforeSignOut;
    S.beforeSignOut = function () {
      return Promise.all([before ? before() : true, flush()]).then(function (r) { return r[0] !== false && r[1] !== false; });
    };
  }

  // ---- links ------------------------------------------------------------------------------------------
  var out = $("#il-out"), inn = $("#il-in"), linkTimer = 0, linkAt = 0;
  function href(docId) { return "/p/" + slug + "/d/" + docId; }
  function anchor(label, docId) {
    var a = el("a", "", label);
    a.href = href(docId);
    return a;
  }
  function crumbs(ref) { return (ref.breadcrumb || []).map(function (c) { return c.title || "Untitled"; }).join(" › "); }
  function drawOut(items) {
    out.replaceChildren();
    items.forEach(function (l) {
      var li = el("li", "insp-item"), name = l.alias ? l.alias + " (" + l.text + ")" : l.text;
      if (l.resolved && l.target) {
        li.append(anchor(l.target.title || l.text, l.target.id));
        if (l.ambiguous) li.append(el("span", "insp-tag", "Several notes share this title"));
      } else {
        li.append(el("span", "insp-missing", name), el("span", "insp-tag", "No note yet"));
        if (canEdit) {
          var b = el("button", "btn quiet small", "Create note “" + (l.text.length > 24 ? l.text.slice(0, 24) + "…" : l.text) + "”");
          b.type = "button";
          b.addEventListener("click", function () {
            b.disabled = true;
            api("POST", "/api/v1/projects/" + W.cfg.project + "/links/resolve", { title: l.text, create: true }).then(function (r) {
              if (r.ok) { if (S.toast) S.toast("Created the note “" + l.text + "”."); if (S.wiki) S.wiki.refresh(); links(); }
              else { b.disabled = false; if (S.toast) S.toast("Could not create the note."); }
            }, function () { b.disabled = false; if (S.toast) S.toast("Could not reach the server."); });
          });
          li.append(b);
        }
      }
      if (l.count > 1) li.append(el("span", "insp-tag", l.count + " times"));
      out.append(li);
    });
    $("#il-out-empty").hidden = items.length > 0;
  }
  function drawIn(items) {
    inn.replaceChildren();
    items.forEach(function (b) {
      var li = el("li", "insp-item");
      li.append(anchor(b.from.title || "Untitled", b.from.id));
      var c = crumbs(b.from);
      if (c) li.append(el("span", "insp-crumb", c));
      if (b.snippet) li.append(el("span", "insp-snip", b.snippet));
      inn.append(li);
    });
    $("#il-in-empty").hidden = items.length > 0;
  }
  function links() {
    clearTimeout(linkTimer);
    if (current !== "details") return;
    linkAt = Date.now();
    api("GET", "/api/v1/docs/" + id + "/links").then(function (r) { if (r.ok && r.json) drawOut(r.json.items || []); }, function () {});
    api("GET", "/api/v1/docs/" + id + "/backlinks").then(function (r) { if (r.ok && r.json) drawIn(r.json.items || []); }, function () {});
  }
  // Links change as the text is saved; look again a few seconds later, and only while the panel is showing them.
  doc.addEventListener("scriobh:saved", function () {
    if (current !== "details") return;
    clearTimeout(linkTimer);
    linkTimer = setTimeout(links, Date.now() - linkAt < 4000 ? 4000 : 1500);
  });
  S.inspectorLinks = links;
  // The scene tab changed the point of view or place: the Details fields show the text it wrote.
  doc.addEventListener("scriobh:scene", function (e) {
    var d = e.detail || {};
    ["pov", "location"].forEach(function (k) { if (d[k] != null && fields[k]) { fields[k].value = d[k]; known[k] = d[k]; } });
  });
})();
