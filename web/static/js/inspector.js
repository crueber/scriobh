/* Scríobh writing view: inspector panel (details, links, lazy tabs).
   Never steals focus or shifts text while typing; saves details via doc.update. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  var box = doc.getElementById("inspector");
  if (!W || !box) return;
  var $ = S.$, $$ = S.$$, el = S.el, api = W.api, id = W.cfg.id, canEdit = W.cfg.canEdit, slug = W.cfg.slug;
  var docUrl = "/api/v1/docs/" + id, TAB_KEY = "scriobh:insp-tab";
  // Keep open/tab state in a cookie so SSR renders it open without layout shift.
  function keep(v) {
    if (v === undefined) { var m = /(?:^|; )scriobh_insp=(\w*)/.exec(doc.cookie); return m ? m[1] : ""; }
    doc.cookie = "scriobh_insp=" + v + "; path=/; max-age=31536000; samesite=lax";
  }

  // ---- open, close, tabs ----
  var openers = $$(".insp-open"), tabs = $$(".insp-tab", box);
  var panels = { details: $("#ipanel-details"), versions: $("#ipanel-versions"), scene: $("#ipanel-scene"), compendium: $("#ipanel-compendium"), alts: $("#ipanel-alts"), stash: $("#ipanel-stash"), lab: $("#ipanel-lab"), ai: $("#ipanel-ai"), comments: $("#ipanel-comments") };
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
    try { win.localStorage.setItem(TAB_KEY, name); } catch (e) {}
    reveal($("#itab-" + name));
    if (LAZY[name]) lazyPanel(name);
    if (name === "details") links();
  }
  function lazyPanel(name) {
    var m = LAZY[name];
    if (S[m]) { S[m].show(panels[name]); return; }
    S.loadScript($("#write").dataset[name + "Src"]).then(function () { if (S[m]) S[m].show(panels[name]); }, function () {
      var b = el("button", "btn small", "Try again"); b.type = "button";
      b.addEventListener("click", function () { lazyPanel(name); });
      var p = el("p", "", "That could not be loaded. Your text is unchanged."); p.setAttribute("role", "alert");
      panels[name].replaceChildren(p, b);
    });
  }
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
    if (had || refocus) { var back = $('.insp-open[data-panel="' + name + '"]') || $(".insp-open:not([data-panel])") || $(".insp-open"); if (back) back.focus({ preventScroll: true }); }
  }
  function last() { var n; try { n = win.localStorage.getItem(TAB_KEY); } catch (e) {} return panels[n] ? n : "details"; }
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
  var was = keep(), pre = box.classList.contains("pre");
  box.classList.remove("pre");
  if (was && wide.matches && panels[was]) open(was, false);
  else if (pre) box.classList.remove("is-open");
  function srcView() { return $(".scriobh-source:not([hidden])"); }
  function selected() {
    var src = srcView(), v = W.editor && W.editor.view, s = v && v.state.selection;
    return (src ? src.value.slice(src.selectionStart, src.selectionEnd) : s ? v.state.doc.textBetween(s.from, s.to, "\n") : "").trim();
  }
  function toEditor() {
    var src = srcView();
    if (src) src.focus({ preventScroll: true }); else W.editor.view.focus();
  }
  function compendium(how) {
    var t = how ? selected() : "";
    S.lookupWant = { sel: /\n/.test(t) || t.length > 200 ? "" : t, isNew: how === "new" };
    open("compendium", false);
  }
  function want(panel, how) {
    if (!panels[panel] || !W.editor) return;
    var src = srcView(), v = W.editor.view, s;
    if (!src && S.alts) S.alts.sync();
    s = v.state.selection;
    S[panel + "Want"] = src ? { src: 1, from: src.selectionStart, to: src.selectionEnd, empty: how === "chapter" || src.selectionStart === src.selectionEnd, doc: src.value }
      : { from: s.from, to: s.to, empty: how === "chapter" || s.empty, doc: v.state.doc };
    open(panel, false);
  }
  S.inspector = { open: open, close: close, get current() { return current; }, flush: function () { return canEdit ? flush() : Promise.resolve(true); }, selected: selected, compendium: compendium, toEditor: toEditor, want: want };
  var chords = (S.chords = S.chords || { clash: [] }), who = "compendium: the panel", whoM = "comments: the panel";
  if (chords.KeyC && chords.KeyC !== who) chords.clash.push("KeyC: " + chords.KeyC + " and " + who);
  if (chords.KeyM && chords.KeyM !== whoM) chords.clash.push("KeyM: " + chords.KeyM + " and " + whoM);
  chords.KeyC = who;
  chords.KeyM = whoM;
  doc.addEventListener("keydown", function (e) {
    var m = e.code === "KeyM" && panels.comments;
    if ((e.code !== "KeyC" && !m) || !e.altKey || !e.shiftKey || e.ctrlKey || e.metaKey || e.isComposing || e.keyCode === 229) return;
    e.preventDefault(); e.stopPropagation();
    if (e.repeat) return;
    if (current === (m ? "comments" : "compendium") && box.contains(doc.activeElement)) { toEditor(); if (!wide.matches) close(false); }
    else if (m) want("comments"); else compendium(1);
  }, true);

  // ---- details ----
  var form = $("#insp-form"), note = $("#insp-saved"), fields = {};
  form.addEventListener("submit", function (e) { e.preventDefault(); });
  $$("[name]", form).forEach(function (f) { fields[f.name] = f; });
  function value(f) { return f.type === "checkbox" ? f.checked : f.name === "target_words" ? (parseInt(f.value, 10) || 0) : f.value; }
  var known = {}, queued = {}, rejected = {}, timer = 0, busy = false, noteTimer = 0, metaRev = 0, waiters = [];
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
      if (rejected[k] !== undefined && rejected[k] === v) delete queued[k];
      else if (v !== known[k]) { queued[k] = v; delete rejected[k]; } else delete queued[k];
    });
    return Object.keys(queued).length > 0;
  }
  function settle(ok) { var w = waiters; waiters = []; w.forEach(function (f) { f(ok); }); }
  function flush() {
    clearTimeout(timer);
    if (!busy && !collect()) return Promise.resolve(true);
    return new Promise(function (res) { waiters.push(res); if (!busy) send(0); });
  }
  function send(tries, baseRev) {
    tries = tries || 0;
    clearTimeout(timer);
    if (busy) return;
    if (!collect()) { settle(true); return; }
    busy = true;
    var synced = baseRev == null && metaRev <= W.rev;
    var body = { base_revision: baseRev != null ? baseRev : Math.max(W.rev, metaRev), tab_id: W.tab }, sent = {};
    Object.keys(queued).forEach(function (k) { body[k] = queued[k]; sent[k] = queued[k]; fieldError(k, ""); });
    function next() { if (collect()) send(0); else settle(true); }
    function retry(msg) { say(msg, true); timer = setTimeout(function () { send(0); }, 6000); settle(false); }
    api("PATCH", docUrl, body).then(function (r) {
      busy = false;
      if (r.ok && r.json && r.json.item) {
        metaRev = r.json.item.revision;
        if (synced) W.rev = metaRev;
        Object.keys(sent).forEach(function (k) { known[k] = sent[k]; if (queued[k] === sent[k]) delete queued[k]; });
        say("Saved");
        next();
        return;
      }
      var err = (r.json && r.json.error) || {};
      if (r.status === 409 && err.data && err.data.reason === "revision") {
        if (typeof err.data.current_revision === "number" && err.data.current_revision > 0 && err.data.current_revision > metaRev) {
          metaRev = Math.max(metaRev, err.data.current_revision);
        }
        if (tries < 2) {
          api("GET", docUrl).then(function (g) {
            var it = g.ok && g.json && g.json.item;
            if (!it) { retry("Could not save. It will be tried again."); return; }
            var sameProse = S.lockHost ? S.lockHost.sameText(it.content, W.serverMd) : it.content === W.serverMd;
            var sameTitle = W.serverTitle === undefined || it.title === W.serverTitle;
            if (sameProse && sameTitle && it.revision > W.rev) W.rev = it.revision;
            metaRev = Math.max(metaRev, it.revision);
            // Only advance W.rev when prose and title match; otherwise retry metadata against latest revision.
            send(tries + 1, sameProse && sameTitle ? null : it.revision);
          }, function () { retry("Could not save. It will be tried again."); });
        } else {
          retry("Could not save. It will be tried again.");
        }
      } else if (r.status === 409) {
        say("This chapter is open for writing in another tab, so this change was not saved. Take over there, or close that tab.", true);
        settle(false);
      } else if (r.status === 422 && err.field && fields[err.field]) {
        fieldError(err.field, err.message || "That value cannot be saved.");
        rejected[err.field] = sent[err.field];
        delete queued[err.field];
        say("Not saved: " + (err.message || "check the highlighted field."), true);
        next();
      } else if (r.status === 401) {
        say("Signed out. Your change is kept here until you sign in again.", true);
        settle(false);
      } else retry("Could not save. It will be tried again.");
    }, function () { busy = false; retry("Offline. It will be saved when the connection returns."); });
  }
  if (canEdit) {
    form.addEventListener("input", function (e) {
      if (e.target.type === "checkbox" || e.target.tagName === "SELECT") return;
      fieldError(e.target.name, "");
      clearTimeout(timer);
      timer = setTimeout(function () { send(0); }, 900);
    });
    form.addEventListener("change", function () { send(0); });
    form.addEventListener("focusout", function () { send(0); });
    win.addEventListener("pagehide", function () {
      if (!collect()) return;
      var body = { base_revision: Math.max(W.rev, metaRev), tab_id: W.tab };
      Object.keys(queued).forEach(function (k) { body[k] = queued[k]; });
      try { win.fetch(docUrl, { method: "PATCH", keepalive: true, credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); } catch (e) {}
    });
    S.onBeforeSignOut(flush);
  }

  // ---- links ----
  var out = $("#il-out"), inn = $("#il-in"), linkTimer = 0, linkAt = 0;
  function anchor(label, docId) {
    var a = el("a", "", label);
    a.href = "/p/" + slug + "/d/" + docId;
    return a;
  }
  function crumbs(ref) { return (ref.breadcrumb || []).map(function (c) { return c.title || "Untitled"; }).join(" › "); }
  function toast(m) { if (S.toast) S.toast(m); }
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
              if (r.ok) { toast("Created the note “" + l.text + "”."); if (S.wiki) S.wiki.refresh(); links(); }
              else { b.disabled = false; toast("Could not create the note."); }
            }, function () { b.disabled = false; toast("Could not reach the server."); });
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
    api("GET", docUrl + "/links").then(function (r) { if (r.ok && r.json) drawOut(r.json.items || []); }, function () {});
    api("GET", docUrl + "/backlinks").then(function (r) { if (r.ok && r.json) drawIn(r.json.items || []); }, function () {});
  }
  doc.addEventListener("scriobh:saved", function () {
    if (current !== "details") return;
    clearTimeout(linkTimer);
    linkTimer = setTimeout(links, Date.now() - linkAt < 4000 ? 4000 : 1500);
  });
  S.inspectorLinks = links;
  doc.addEventListener("scriobh:scene", function (e) {
    var d = e.detail || {};
    ["pov", "location"].forEach(function (k) { if (d[k] != null && fields[k]) { fields[k].value = d[k]; known[k] = d[k]; } });
  });
})();
