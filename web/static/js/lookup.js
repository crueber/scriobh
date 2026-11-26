/* Scríobh writing view: the Compendium tab of the inspector (htmx fragments from /ui/projects/{id}/lookup). This is what htmx cannot do:
   one save at a time, only the fields the writer changed, the conflict, the editor's selection, Esc back to the caret, the last save on the way out.
   It never touches the editor: no dispatch, no scroll, no focus unless the writer pressed something. */
(function () {
  "use strict";
  var doc = document, win = window, S = (win.scriobh = win.scriobh || {}), W = S.write, H = win.htmx;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var box = $("#lk-msg"), M = {};
  try { M = JSON.parse((box && box.dataset.msg) || "{}"); } catch (e) { M = {}; }
  function T(k, d, v) { var s = M[k] || d; if (v) for (var n in v) s = s.split("{" + n + "}").join(v[n]); return s; }
  var wr = $("#write"), root = null, main = null, base = "", key = "", busy = false, owed = false, sec = {}, keepRev = 0, force = false, stale = false, first = null, touched = 0, sent = 0, dirty = false, bad = false, timer = 0, seq = 0, nd = false, sentN = "", press = false, wide = win.matchMedia("(min-width: 1150px)"), waiters = [], D = {}, fly = {};
  var csrf = ($('meta[name="csrf-token"]') || {}).content || "";
  function off() { return T("offline", "Could not reach the server. Your words are kept here."); }
  // The status by the Save button, or the one by the names; with neither, in place of "Loading".
  function say(m, isBad, id) { var s = $("#" + (id || "lk-status")) || $("#lk-status"); if (s) s.textContent = m; else if (!main.firstElementChild) main.textContent = m; if (isBad != null) bad = !!isBad; }
  // The last thing asked is what is shown: an older answer is dropped.
  function ask(url) { H.ajax("GET", url, { target: main, source: main, swap: "innerHTML", headers: { "X-Scriobh-Fetch": "1", "X-Lk-Seq": ++seq } }).catch(function () {}); }
  function form() { return $("#lk-form"); }
  function remember() {
    var e = $("#lk-entry"), q = $("#lk-q"), v = $("#lk-view");
    try { win.sessionStorage.setItem(key, JSON.stringify({ e: e ? e.dataset.entity : "", s: e ? e.dataset.scope : "", q: q ? q.value : "", v: v ? v.dataset.view : "" })); } catch (x) {}
  }
  function css(h) { return new Promise(function (ok) { var l = doc.createElement("link"); l.rel = "stylesheet"; l.href = h; l.onload = l.onerror = ok; doc.head.appendChild(l); }); }
  // A field's words as the server keeps them, and their hash (the same as lkHash in lookupui.go): the words a save started from.
  function norm(k, v) { return k === "body" ? v.replace(/\r\n/g, "\n") : v.trim(); }
  function hash(v) {
    var b = new win.TextEncoder().encode(v.replace(/\r\n/g, "\n")), h1 = 2166136261, h2 = 5381, i;
    for (i = 0; i < b.length; i++) { h1 = Math.imul(h1 ^ b[i], 16777619) >>> 0; h2 = (Math.imul(h2, 33) ^ b[i]) >>> 0; }
    return ("0000000" + h1.toString(16)).slice(-8) + ("0000000" + h2.toString(16)).slice(-8);
  }
  function nv() { var f = $("#lk-nform"); return f ? f.elements.title.value + "\0" + f.elements.body.value : ""; }
  function names() { return Object.keys(D); }
  function val(f, k) { var el = f.elements[k]; return el ? (el.type === "checkbox" ? (el.checked ? el.value : null) : el.value) : null; }

  function save() {
    clearTimeout(timer);
    var f = form();
    if (!f || !dirty || $(".lk-conflict")) return;
    if (busy) { owed = true; return; }
    H.trigger(f, "lk-save");
  }
  function settle(ok) { var w = waiters; waiters = []; w.forEach(function (r) { r(ok); }); }
  function flush() {
    if (!dirty) return Promise.resolve(true);
    if ($(".lk-conflict")) return Promise.resolve(false); // it waits for Keep mine or Use theirs, and nothing waits on it
    return new Promise(function (r) { waiters.push(r); save(); });
  }
  doc.addEventListener("htmx:configRequest", function (e) {
    var el = e.detail.elt, p = e.detail.parameters;
    if (!root || !root.contains(el)) return;
    e.detail.headers["X-Scriobh-Fetch"] = "1";
    if (e.detail.target === main && !e.detail.headers["X-Lk-Seq"]) e.detail.headers["X-Lk-Seq"] = ++seq; // every request into the view is numbered, ask() or a button
    if (/[?&]view=events/.test(e.detail.path)) p.doc = W.cfg.id; // the scene the events are of
    if (csrf) e.detail.headers["X-CSRF-Token"] = csrf;
    if (el !== form()) return;
    // Only what the writer changed goes: a field not touched stays as it is now, which may be what someone else made it.
    Object.keys(p).forEach(function (k) { if (!/^(_csrf|entity_id|base_revision)$/.test(k) && !D[k] && !(/^o\./.test(k) && D[k.slice(2)])) delete p[k]; });
    if (force) p.force = "1";
  });
  doc.addEventListener("htmx:beforeRequest", function (e) {
    var f = form();
    if (first && !first.x && e.detail.elt.className === "lk-search") first.x = e.detail.xhr;
    if ($("#lk-nform") && (e.detail.elt.id === "lk-nform" || e.detail.elt.closest(".lk-conflict"))) sentN = nv(); // the words this save carries
    if (!root || e.detail.elt !== f) return;
    if (busy) { e.preventDefault(); owed = true; return; }
    busy = true; owed = false; sent = touched; fly = {};
    names().forEach(function (k) { var v = val(f, k); if (v != null) fly[k] = hash(norm(k, v)); });
    say(T("saving", "Saving"));
  });
  // Why a request was refused: a lost session, a stale token (the next try asks for a fresh one), a person who may not, or the server.
  function failed(x, id) {
    var j = {};
    try { j = JSON.parse(x.responseText).error || {}; } catch (e) {}
    bad = true;
    if (x.status === 403 && j.data && j.data.reason === "csrf") { stale = true; say(T("stale_csrf", "This panel expired. Your words are kept here. Try again."), true, id); }
    else if (x.status === 401) say(T("signed_out", "You were signed out. Your words are kept here. Sign in again in another tab, then try again."), true, id);
    else if (x.status === 403 || x.status === 404) say(j.message || T("missing", "That entry is not available to you."), true, id);
    else say(T("failed", "Could not save. It will be tried again when you press Save now."), true, id);
  }
  doc.addEventListener("htmx:afterRequest", function (e) {
    var f = form(), x = e.detail.xhr;
    if (!root || !root.contains(e.detail.elt)) return;
    if ($("#lk-status [data-nsaved]") && nv() === sentN) nd = false; // not if words were typed while it was on its way
    if (!e.detail.successful && !(x.status === 404 && /entity=/.test(e.detail.pathInfo.requestPath))) press = false; // a failed open takes nothing
    if (!f || e.detail.elt !== f) return;
    busy = false; force = false;
    var ok = e.detail.successful && x.status < 300;
    if (ok) {
      bad = false;
      names().forEach(function (k) { if (D[k] <= sent) delete D[k]; });
      dirty = names().length > 0;
      var s = $("#lk-status [data-saved]");
      if (s) s.textContent = T("saved", "Saved at {time}.", { time: new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) });
    } else if (x.status === 422) bad = !!$(".lk-conflict");
    else failed(x);
    settle(ok);
    if (keepRev) mine(); else if (ok && owed && dirty) save();
  });
  // Keep mine: the same words, over what is there now.
  function mine() {
    var c = $(".lk-conflict");
    if (c) c.remove();
    $("#lk-rev").value = keepRev; keepRev = 0; force = true; dirty = true; save();
  }
  doc.addEventListener("htmx:sendError", function (e) {
    if (!root || !root.contains(e.detail.elt)) return;
    busy = false; bad = true; press = false; say(off(), true, e.detail.elt.closest("#lk-aka") ? "lk-aka-status" : null); settle(false);
  });
  doc.addEventListener("htmx:responseError", function (e) {
    var x = e.detail.xhr, el = e.detail.elt;
    if (!root || !root.contains(el) || el === form()) return;
    if (x.status === 404 && /entity=/.test(e.detail.pathInfo.requestPath)) ask(base);
    else failed(x, el.closest("#lk-aka") ? "lk-aka-status" : null);
  });
  doc.addEventListener("htmx:confirm", function (e) {
    var el = e.detail.elt;
    // After a refused token the next change asks for a fresh one first, as the editor does.
    if (stale && root && root.contains(el) && el.hasAttribute("hx-post")) {
      e.preventDefault(); stale = false;
      win.fetch("/ui/csrf", { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) { return r.ok ? r.json() : null; }).then(function (j) { if (j && j.token) csrf = j.token; }, function () {}).then(e.detail.issueRequest);
      return;
    }
    var main1 = e.detail.target && e.detail.target.id === "lk-main";
    if (nd && main1 && el.closest(".lk-conflict")) nd = false; // Use theirs is the writer's own choice to let the words go
    if (nd && main1 && el.id !== "lk-nform") { e.preventDefault(); if (!$(".lk-conflict")) say(T("note_unsaved", "Save the note first, or its words are lost."), true); return; }
    if (!dirty || !e.detail.target || e.detail.target.id !== "lk-main") return;
    e.preventDefault();
    flush().then(function (ok) {
      var c = $(".lk-conflict"), p;
      if (ok) e.detail.issueRequest();
      else if (c && !$(".lk-need")) { p = doc.createElement("p"); p.className = "lk-need lk-bad"; p.textContent = T("conflict_open", "This entry changed since you opened it. Choose Keep mine or Use theirs."); c.insertBefore(p, c.firstChild); }
    });
  });
  // A late answer never replaces what has been typed since.
  doc.addEventListener("htmx:beforeSwap", function (e) {
    if (root && e.detail.target === main && (dirty || nd && e.detail.requestConfig.elt.id !== "lk-nform" || e.detail.requestConfig.headers["X-Lk-Seq"] < seq)) e.detail.shouldSwap = false;
  });

  function typed(e) {
    var f = form(), n = e.target.name;
    if (!f || !n || e.target.form !== f && e.target.getAttribute("form") !== "lk-form") return;
    dirty = true; D[n] = ++touched;
    if (/^blank\./.test(n)) D["f." + n.slice(6)] = touched;
    if (e.type === "input") {
      if (e.target.id === "es-body") { var w = $("#es-words"); if (w) w.textContent = w.textContent.replace(/\d+/, e.target.value.trim() ? e.target.value.trim().split(/\s+/).length : 0); }
      clearTimeout(timer); timer = setTimeout(save, 800);
    } else save();
  }
  // Tab or a click into the panel makes the browser scroll the page to the box: the page stays.
  var y0 = 0, t0 = 0;
  function mark() { y0 = win.scrollY; t0 = Date.now(); }
  doc.addEventListener("keydown", mark, true);
  doc.addEventListener("pointerdown", mark, true);
  function wire() {
    root.addEventListener("focusin", function () { if (Date.now() - t0 < 200 && win.scrollY !== y0) win.scrollTo(0, y0); });
    try { sec = JSON.parse(win.sessionStorage.getItem(key + ":o") || "{}"); } catch (x) {}
    root.addEventListener("toggle", function (e) {
      var d = e.target;
      if (d.id && d.closest("#lk-entry")) { sec[d.id] = d.open; try { win.sessionStorage.setItem(key + ":o", JSON.stringify(sec)); } catch (x) {} }
    }, true);
    root.addEventListener("input", function (e) { if (e.target.id === "lk-q") first = null; else if (e.target.form && e.target.form.id === "lk-nform") nd = true; typed(e); });
    root.addEventListener("change", typed);
    root.addEventListener("focusout", function (e) { if (dirty && e.target.closest && e.target.closest("#lk-form,[form=lk-form]")) save(); });
    root.addEventListener("keydown", function (e) {
      if (e.isComposing || e.keyCode === 229) return; // an input method is using the key
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); back(); }
      else if (e.key === "Enter" && e.target.id === "lk-q") { e.preventDefault(); first = {}; H.trigger($(".lk-search"), "submit"); }
    });
    root.addEventListener("click", function (e) {
      var t = e.target.closest("button"), f = form();
      if (!t) return;
      if (t.hasAttribute("data-open")) press = true;
      if (t.dataset.lkKeep && f) { keepRev = t.dataset.lkKeep; if (!busy) mine(); }
      else if (t.hasAttribute("data-lk-theirs")) { dirty = false; D = {}; var en = $("#lk-entry"); press = true; ask(base + "?entity=" + en.dataset.entity + "&scope=" + en.dataset.scope + "&fresh=1"); }
      else if (t.id === "lk-selname") name();
      else if (t.dataset.lkScene) scene(t.dataset.lkScene, t.dataset.entity);
      else if (t.id === "es-preview") preview(t);
    });
    root.addEventListener("htmx:afterSwap", function (e) {
      H.process(e.target);
      // Enter opens the first of the answer to its own search, not of an older one.
      if (first && e.detail.xhr === first.x && e.target.id === "lk-rows") { first = null; var b = $(".lk-pick"); if (b) { press = true; b.click(); } }
      if (e.target.id === "lk-main") {
        nd = false; dirty = false; busy = false; owed = false; bad = false; D = {};
        if (press) { var h = $("#lk-h,#lk-new-h,.lk-state .lk-h") || $("#lk-first") || $("#lk-q"); if (h) h.focus({ preventScroll: true }); press = false; }
        Object.keys(sec).forEach(function (id) { var d = $("#" + id); if (d && d.closest("#lk-entry")) d.open = sec[id]; });
        draft();
        remember();
      }
    });
  }
  doc.addEventListener("lk:names", function () { if (S.mentions && S.mentions.refresh) S.mentions.refresh(); });

  // Words too many for the last request wait in this browser and come back into their entry, to be saved.
  function dk(en) { return "scriobh:lkdraft:" + wr.dataset.user + ":" + en; }
  function draft() {
    var en = $("#lk-entry"), f = form(), d = null;
    if (!en || !f) return;
    try { d = JSON.parse(win.localStorage.getItem(dk(en.dataset.entity))); win.localStorage.removeItem(dk(en.dataset.entity)); } catch (x) { d = null; }
    if (!d) return;
    Object.keys(d).forEach(function (k) { var el = f.elements[k]; if (el) { el.value = d[k]; D[k] = ++touched; dirty = true; } });
    say(T("draft", "Words you typed here before the page closed are back. They are being saved."));
    save();
  }

  function name() {
    var t = S.inspector.selected(), f = $("#lk-addname"), i = $("#lk-aka-in");
    if (!t) { say(T("sel_empty", "Select the words in the text first."), true, "lk-aka-status"); return; }
    if (/\s*\n/.test(t) || t.length > 200) { say(T("sel_long", "That selection is too long or runs over a line."), true, "lk-aka-status"); return; }
    i.value = t; H.trigger(f, "submit");
  }
  function scene(field, id) {
    if (!W || !W.api) return;
    W.api("PUT", "/api/v1/docs/" + W.cfg.id + "/" + field, { entity: id, base_revision: W.rev }).then(function (r) {
      var o = r.json && (r.json.item || r.json);
      if (!r.ok || !o) { say((r.json && r.json.error && r.json.error.message) || T("pov_fail", "That could not be linked."), true); return; }
      if (o.revision > W.rev) W.rev = o.revision;
      doc.dispatchEvent(new win.CustomEvent("scriobh:scene", { detail: { pov: o.pov, location: o.location } }));
      say(field === "pov" ? T("pov_set", "Linked as the point of view.") : T("loc_set", "Linked as the location."));
    }, function () { say(off(), true); });
  }
  function preview(b) {
    var ta = $("#es-body"), out = $("#es-prev"), b2 = new win.URLSearchParams();
    if (!out.hidden) { out.hidden = true; b.textContent = b.dataset.show; return; }
    b2.set("body", ta.value); b2.set("_csrf", csrf);
    win.fetch(b.dataset.src, { method: "POST", credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1", "X-CSRF-Token": csrf, "Content-Type": "application/x-www-form-urlencoded" }, body: b2.toString() })
      .then(function (r) { return r.ok ? r.text() : Promise.reject(); })
      .then(function (h) { out.innerHTML = h; out.hidden = false; b.textContent = b.dataset.hide; }, function () { say(off(), true); });
  }

  function back() {
    S.inspector.toEditor();
    if (!wide.matches) S.inspector.close(false);
  }

  function go(want) {
    var u = base;
    if (want && want.entity) { u += "?entity=" + want.entity; press = true; }
    else if (want && want.isNew) { u += "?new=" + encodeURIComponent(want.sel || ""); press = true; }
    else if (want && want.sel) { u += "?sel=" + encodeURIComponent(want.sel); press = true; }
    else if (want) press = true;
    else {
      var k = {}; try { k = JSON.parse(win.sessionStorage.getItem(key) || "{}"); } catch (e) { k = {}; }
      if (k.v) u += "?view=" + k.v;
      else if (k.e) u += "?entity=" + k.e + "&scope=" + (k.s || "");
      else if (k.q) u += "?q=" + encodeURIComponent(k.q);
    }
    ask(u);
  }
  function show(panel) {
    var want = S.lookupWant; S.lookupWant = null;
    if (root === panel && main) {
      if (want) go(want);
      return;
    }
    root = panel; base = "/ui/projects/" + W.cfg.project + "/lookup"; key = "scriobh:lk:" + wr.dataset.user + ":" + W.cfg.project;
    main = doc.createElement("div"); main.id = "lk-main"; main.className = "lk"; main.textContent = T("loading", "Loading the Compendium");
    panel.replaceChildren(main);
    H.process(main); // htmx drops the requests it queued for an element when it first processes it: before any is asked, not after the first answer
    wire();
    // Signing out saves what is typed here first.
    var before = S.beforeSignOut;
    S.beforeSignOut = function () { return Promise.all([before ? before() : true, flush()]).then(function (r) { return r[0] !== false && r[1] !== false; }); };
    Promise.all([css(wr.dataset.entryCss), css(wr.dataset.compendiumCss)]).then(function () { H.process(panel); go(want); });
  }
  S.lookup = { show: show };
  // The last save goes with the page: the changed fields, with the words they began with and those of a save still on its way (which may
  // or may not land first). Too much for such a request (about 64 KB) is kept in this browser.
  win.addEventListener("pagehide", function () {
    var f = form(), en = $("#lk-entry");
    if (!f || !dirty || !en) return;
    var p = new win.URLSearchParams(), d = {};
    ["_csrf", "entity_id", "base_revision"].forEach(function (k) { if (f.elements[k]) p.set(k, f.elements[k].value); });
    names().forEach(function (k) {
      var v = val(f, k), o = f.elements["o." + k];
      if (v == null) return;
      p.set(k, v); d[k] = v;
      if (o) p.set("o." + k, o.value + (fly[k] ? "," + fly[k] : ""));
    });
    if (p.toString().length > 60000) { try { win.localStorage.setItem(dk(en.dataset.entity), JSON.stringify(d)); } catch (e) {} return; }
    try { win.fetch(f.getAttribute("hx-post"), { method: "POST", keepalive: true, credentials: "same-origin", headers: { "X-CSRF-Token": csrf, "X-Scriobh-Fetch": "1" }, body: p }).catch(function () {}); } catch (e) {}
  });
  win.addEventListener("beforeunload", function (e) { if (dirty && bad || nd) { e.preventDefault(); e.returnValue = ""; } });
  doc.addEventListener("visibilitychange", function () {
    var en = $("#lk-entry");
    if (doc.hidden || !root || root.hidden || !en || dirty || root.contains(doc.activeElement)) return;
    press = false;
    ask(base + "?entity=" + en.dataset.entity + "&scope=" + en.dataset.scope);
  });
})();
