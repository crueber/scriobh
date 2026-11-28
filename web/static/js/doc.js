/* Scríobh writing view: the editor host, the save loop, the tab lease, local
   drafts, notices, the formatting bar and the status bar. Vanilla, no dependencies.

   Saves use plain fetch on purpose. htmx follows an HX-Redirect to the sign-in page,
   which would throw the buffer away the moment a session expires. Here a failed save
   never navigates: the text stays in the editor, in a local draft, and is retried.

   The rules this file keeps (docs/ARCHITECTURE.md, "Autosave and conflicts"):
   - the editor buffer is the truth until the server confirms a save;
   - one save in flight per document, always the newest text;
   - a local draft is written before every network save and cleared only on confirmation;
   - a conflict never discards text: a copy is stored before either side is chosen. */
(function () {
  "use strict";
  var doc = document, win = window;
  var S = (win.scriobh = win.scriobh || {});
  var $ = S.$, $$ = S.$$, el = S.el, fmt = S.fmt;
  var root = $("#write");
  if (!root || !root.dataset.doc || !win.scriobhEditor) return;

  var D = root.dataset;
  var cfg = { id: D.doc, project: D.project, slug: D.slug, canEdit: D.canEdit === "true", readonly: D.readonly || "", guard: D.guard || "", updated: D.updated || "" };
  var csrf = S.getCsrf();
  var isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
  var markdown = win.scriobhEditor.markdown;

  // ---- small helpers --------------------------------------------------------
  function keep(kind, k, v) {
    try {
      var s = kind === "session" ? sessionStorage : localStorage;
      if (v === undefined) return s.getItem(k);
      if (v === null) s.removeItem(k); else s.setItem(k, v);
      return true;
    } catch (e) { return v === undefined ? null : false; }
  }
  function rand() {
    var a = new Uint8Array(12);
    win.crypto.getRandomValues(a);
    return Array.prototype.map.call(a, function (b) { return (b < 16 ? "0" : "") + b.toString(16); }).join("");
  }
  function words(n) { return fmt(n) + (n === 1 ? " word" : " words"); }

  // One id per tab, kept across a reload so the lease is still ours. A duplicated tab
  // copies sessionStorage, so another live tab with the same id makes this one pick a new one.
  var tab = keep("session", "scriobh:tab") || rand();
  keep("session", "scriobh:tab", tab);
  var inst = rand(), tabReady = !win.BroadcastChannel, clashed = Promise.resolve();
  if (win.BroadcastChannel) {
    clashed = new Promise(function (res) {
      var done = function () { tabReady = true; res(); };
      try {
        var bc = new win.BroadcastChannel("scriobh-tab");
        bc.onmessage = function (e) {
          var m = e.data || {};
          if (m.q === tab && m.inst !== inst) bc.postMessage({ a: tab, inst: inst });
          else if (m.a === tab && m.inst !== inst) { tab = rand(); keep("session", "scriobh:tab", tab); done(); }
        };
        bc.postMessage({ q: tab, inst: inst });
        setTimeout(done, 120);
      } catch (e) { done(); /* no channel: the revision check still protects the text */ }
    });
  }

  // ---- the server ------------------------------------------------------------
  function api(method, path, body, timeout, background) {
    var ac = new AbortController(), t = setTimeout(function () { ac.abort(); }, timeout || 30000);
    var o = { method: method, credentials: "same-origin", signal: ac.signal, headers: { Accept: "application/json" } };
    if (background) o.headers["X-Scriobh-Background"] = "1"; // upkeep, not use: a page left open must not keep a project's unlock alive
    if (body !== undefined) { o.headers["Content-Type"] = "application/json"; o.body = JSON.stringify(body); }
    return fetch(path, o).then(function (r) {
      return r.text().then(function (txt) {
        var j = null;
        try { j = txt ? JSON.parse(txt) : null; } catch (e) { /* not JSON */ }
        clearTimeout(t);
        return { status: r.status, ok: r.ok, json: j, retry: parseInt(r.headers.get("Retry-After") || "0", 10) || 0 };
      });
    }, function (e) { clearTimeout(t); throw e; });
  }
  var docPath = "/api/v1/docs/" + cfg.id;
  function fetchDoc() { return api("GET", docPath).then(function (r) { if (r.ok && r.json) return r.json.item; throw r; }); }

  // A UI route (tree changes): form-encoded, CSRF header, errors as JSON. Never follows a redirect.
  function ui(url, fields) {
    function go() {
      return fetch(url, { method: "POST", credentials: "same-origin", redirect: "manual",
        headers: { "X-CSRF-Token": csrf, "X-Scriobh-Fetch": "1", "Content-Type": "application/x-www-form-urlencoded" },
        body: new URLSearchParams(fields || {}) }).then(function (r) {
        return r.text().then(function (t) { return { status: r.status, ok: r.ok, text: t, id: r.headers.get("X-Doc-Id") || "" }; });
      });
    }
    return go().then(function (r) {
      if (r.status !== 403) return r;
      return fetch("/ui/csrf", { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (x) { return x.ok ? x.json() : null; }).then(function (j) {
        if (j && j.token) { csrf = j.token; S.setCsrf(csrf); return go(); }
        return r;
      });
    });
  }

  // ---- elements -----------------------------------------------------------------
  var plain = $("#doc-plain"), editorEl = $("#editor"), titleEl = $("#doc-title"), titleGrow = $("#title-grow");
  var titleStatic = $("#title-static"), titleError = $("#title-error"), paper = $("#paper");
  var stateEl = $("#save-state"), wordEl = $("#save-word"), live = $("#live"), notices = $("#notices");
  var fmtbar = $("#fmtbar");
  var initialMd = plain ? plain.textContent : "";

  // ---- state ------------------------------------------------------------------------
  var st = { rev: parseInt(D.rev, 10) || 1, serverMd: initialMd, serverTitle: titleEl.value.trim() };
  var want = null;          // the newest text the editor handed over: {md, version}
  var inflight = false, halted = false, locked = false, gated = false, skipTitle = false, tooLargeMd = null;
  var backoff = 0, retryTimer = 0, savingTimer = 0, copiedMd = null, conflictInfo = null, waiters = [], echoes = 0;
  var unconfirmed = []; // saves sent whose answer never came: they may have landed
  var saveDelay = Math.min(5000, Math.max(300, parseInt(D.autosave, 10) || 700)); // Settings: save after
  var ed = null;
  var writable = cfg.canEdit && cfg.readonly !== "length";

  function titleNow() { return titleEl.value.replace(/\s*\n\s*/g, " ").trim(); }
  // The alternatives comment is the document's last line. The server may rewrite it, so two texts are compared without it.
  var ALT = "<!-- scriobh:alts ";
  function altLine(m) { var t = m.replace(/\n+$/, ""), l = t.slice(t.lastIndexOf("\n") + 1); return l.indexOf(ALT) === 0 ? l : ""; }
  function sameText(a, b) { if (a === b) return true; var x = altLine(a), y = altLine(b); return a.slice(0, a.length - x.length) === b.slice(0, b.length - y.length); }
  function textPending() { return !!want && !sameText(want.md, st.serverMd) && want.md !== tooLargeMd; }
  function titlePending() { return titleNow() !== st.serverTitle && !skipTitle; }
  function needs() { return textPending() || titlePending(); }
  // Text the server refused as too large: it stays in the editor and the draft, and nothing may call it saved.
  function blocked() { return !!want && tooLargeMd !== null && want.md === tooLargeMd; }
  function unsaved() { return needs() || inflight || blocked() || (ed && ed.isDirty()); }

  // ---- save state, notices, announcements ------------------------------------------
  var LABEL = { saved: "Saved", saving: "Saving…", offline: "Offline", signedout: "Signed out", conflict: "Conflict", locked: "Locked", toolarge: "Too large", error: "Not saved", paused: "Paused" };
  var state = "saved", announced = "saved", annTimer = 0, wordsTimer = 0, lastWords = 0;
  function announce(text) {
    clearTimeout(annTimer);
    annTimer = setTimeout(function () { live.textContent = text; }, 1200);
  }
  function setState(s, message) {
    if (s === state && !message) return;
    state = s;
    stateEl.dataset.state = s;
    wordEl.textContent = LABEL[s] || s;
    if (s !== "saving" && s !== announced) {
      var prev = announced;
      announced = s;
      if (s !== "saved" || prev !== "saved") announce(message || (s === "saved" ? "Saved" : LABEL[s]));
    }
  }
  function bar(id, kind, message, actions) {
    var b = $("#nb-" + id, notices);
    if (!b) { b = el("div", "notice-bar"); b.id = "nb-" + id; b.setAttribute("role", "group"); b.setAttribute("aria-label", "Notice"); notices.appendChild(b); }
    b.dataset.kind = kind;
    var p = el("p", "notice-msg", message), a = el("div", "notice-actions");
    (actions || []).forEach(function (x, i) {
      var c;
      if (x.href) { c = el("a", "btn" + (i === 0 && !x.quiet ? " solid" : ""), x.label); c.href = x.href; c.target = "_blank"; c.rel = "noopener"; }
      else { c = el("button", "btn" + (i === 0 && !x.quiet ? " solid" : ""), x.label); c.type = "button"; c.addEventListener("click", x.fn); }
      a.appendChild(c);
    });
    b.replaceChildren(p, a);
    margins();
    return b;
  }
  function unbar(id) { var b = $("#nb-" + id, notices); if (b) { b.remove(); margins(); } }
  // The caret must stay clear of the fixed bars: header and toolbar above; status, formatting bar and notices below.
  function margins() {
    if (!ed) return;
    var mobile = win.matchMedia("(max-width: 1023px)").matches, nb = notices.getBoundingClientRect().height;
    ed.view.setProps({ scrollMargin: { top: mobile ? 72 : 110, bottom: (mobile ? 150 : 90) + Math.round(nb), left: 0, right: 0 }, scrollThreshold: { top: 0, bottom: 0, left: 0, right: 0 } });
  }
  function clearNet() { ["net", "signedout", "toolarge"].forEach(unbar); }

  // ---- the local draft ---------------------------------------------------------------
  var draftKey = "scriobh:draft:" + (D.user || "") + ":" + cfg.id; // the person's id is in the key: the next person on this browser never reads it
  var heldKey = "scriobh:held:" + (D.user || "") + ":" + cfg.id;
  var heldMoved = false;
  // How many earlier copies of a chapter's unsaved text are kept (the bar offers the newest and says how many are older).
  var MAXHELD = 8;
  function parseJSON(v) { try { return JSON.parse(v || "null"); } catch (e) { return null; } }
  // Over the room, the oldest ordinary copy goes. The newest copy and any taken over from the old keys stay (they may be someone's only one), even if that is more than the room.
  function trimHeld(list) {
    while (list.length > MAXHELD) {
      var i = list.length - 1;
      while (i > 0 && list[i].legacy) i--;
      if (i <= 0) break; // the newest copy and the adopted ones are not dropped: over the room is better than losing text
      list.splice(i, 1);
    }
    return list;
  }
  // Drafts used to be kept under the document's id alone, with nobody's name on them. They cannot be told apart by person, so
  // they are taken over only by someone who can write to the chapter (a reader's page leaves them where they are), as held
  // copies marked as from before drafts were tied to a person: nothing unsaved is thrown away. An entry moves only if there is
  // room for it; the old key is removed only when every entry it held has been kept. (The keys of chapters not opened stay
  // where they are until they are.)
  (function adoptLegacy() {
    if (!D.user || !writable) return;
    var ld = "scriobh:draft:" + cfg.id, lh = "scriobh:held:" + cfg.id;
    var a = parseJSON(keep("local", ld)), b = parseJSON(keep("local", lh));
    if (!a && !b) return;
    var held = parseJSON(keep("local", heldKey)) || [];
    if (!Array.isArray(held)) held = [held];
    function take(list) { // the entries of `list` that could not be kept
      var rest = [];
      (Array.isArray(list) ? list : list ? [list] : []).forEach(function (x) {
        if (!x || typeof x.md !== "string") return; // not text: nothing to keep
        if (held.some(function (h) { return h.md === x.md; })) return; // already kept
        if (held.length >= MAXHELD) { rest.push(x); return; }
        x.legacy = true; held.push(x);
      });
      return rest;
    }
    var restA = take(a), restB = take(b);
    if (!keep("local", heldKey, JSON.stringify(held))) return; // could not be written: nothing is removed
    if (restA.length) keep("local", ld, JSON.stringify(restA[0])); else keep("local", ld, null);
    if (restB.length) keep("local", lh, JSON.stringify(restB)); else keep("local", lh, null);
  })();
  // A title-only change is a draft too.
  function writeDraft() {
    if (locked || !(want || titlePending())) return;
    if (!want) want = { md: localMd(), version: null };
    if (!heldMoved) { heldMoved = true; holdOnLoad(); } // an earlier session's draft goes aside before this one's first write
    if (keep("local", draftKey, JSON.stringify({ md: want.md, title: titleNow(), rev: st.rev, t: Date.now() }))) { unbar("quota"); return; }
    // The store is full or blocked: never leave an older draft that looks current, and say so.
    keep("local", draftKey, null);
    bar("quota", "warn", "This browser could not keep a local copy of your text (its storage is full or blocked). Your text is still here: keep this tab open until it says Saved.", []);
  }
  function readJSON(k, d) { try { return JSON.parse(keep("local", k) || "null") || d; } catch (e) { return d; } }
  function readDraft() { return readJSON(draftKey, null); }
  function clearDraft() { keep("local", draftKey, null); }
  // At load, before a key is pressed, whatever an earlier session left moves to a held list,
  // so the first keystroke of this session cannot overwrite what the bar is about to offer.
  function holdOnLoad() {
    var d = readDraft();
    if (!d || typeof d.md !== "string") return;
    var held = readJSON(heldKey, []).filter(function (h) { return h.md !== d.md; });
    held.unshift(d);
    if (keep("local", heldKey, JSON.stringify(trimHeld(held)))) clearDraft();
  }
  function clearHeld() { keep("local", heldKey, null); }


  // ---- the save loop ----------------------------------------------------------------------
  function pump() {
    if (inflight || halted || locked || gated) return;
    if (!needs()) { settle(true); return; }
    if (!tabReady) { clashed.then(pump); return; }
    var sendMd = textPending();
    var snap = { md: sendMd ? want.md : null, ver: sendMd && want ? want.version : null, title: titlePending() ? titleNow() : null };
    var body = { base_revision: st.rev, tab_id: tab };
    if (snap.md !== null) body.content = snap.md;
    if (snap.title !== null) body.title = snap.title;
    inflight = true;
    clearTimeout(retryTimer);
    clearTimeout(savingTimer);
    if (state === "saved") savingTimer = setTimeout(function () { if (inflight && state === "saved") setState("saving"); }, 300);
    api("PATCH", docPath, body).then(function (r) {
      inflight = false;
      clearTimeout(savingTimer);
      if (r.ok && r.json && r.json.item) saved(r.json.item, snap); else failed(r, snap);
    }, function () {
      inflight = false;
      clearTimeout(savingTimer);
      unconfirmed.push(snap);
      if (unconfirmed.length > 4) unconfirmed.shift();
      netFail();
    });
  }
  function settle(ok) { var w = waiters; waiters = []; w.forEach(function (f) { f(ok); }); }
  function saved(item, snap) {
    st.rev = item.revision;
    // What the server stored is the base, not what was sent: it may have rewritten the comment line.
    if (snap.md !== null) {
      st.serverMd = typeof item.content === "string" ? item.content : snap.md;
      if (st.serverMd !== snap.md) ed.alts.setComment(altLine(st.serverMd));
      // The server may store something other than what was sent (it repairs text it cannot keep). If the editor has not moved on,
      // what it holds is now what is stored, and sending it again would only be answered the same way: it is taken as the text to
      // save. A save answered differently four times running stops here and says so.
      if (!sameText(st.serverMd, snap.md)) {
        echoes++;
        if (want && want.md === snap.md) want = { md: st.serverMd, version: want.version };
        else if (echoes >= 4 && want) want = { md: st.serverMd, version: want.version };
        if (echoes >= 4) bar("echo", "warn", "The server keeps changing your text when it saves it, so this page has stopped sending it again. Your text is kept here. Look for characters that cannot be saved.", [{ label: "Dismiss", quiet: true, fn: function () { unbar("echo"); } }]);
      } else echoes = 0;
    }
    if (item.notices && item.notices.length) toast(item.notices.join(" "));
    // What was sent is what the server now holds; a normalised echo (trimmed, repaired) must not make the field look unsaved.
    st.serverTitle = snap.title !== null ? snap.title : item.title;
    backoff = 0;
    unconfirmed = [];
    if (snap.md !== null && snap.ver !== null) ed.markSaved(snap.ver);
    syncAfterSave(item);
    if (blocked()) { // a title-only save does not make the oversize text saved
      setState("toolarge");
      settle(false);
      return;
    }
    clearNet();
    if (needs()) { writeDraft(); pump(); return; }
    var d = readDraft();
    if (d && (!want || d.md === want.md || d.md === snap.md)) clearDraft();
    if (!halted) setState("saved");
    settle(true);
  }
  // An alternatives op answered (it saved the document itself). `md` is the stored text, which alts.js worked out from the
  // answer; the editor already holds the body change, so only its comment line and its saved state follow.
  function afterAlt(res, md, ver) {
    st.rev = res.revision; st.serverMd = md; want = null; clearDraft();
    ed.alts.setComment(altLine(md));
    ed.alts.settle(ver); // the version that was stored; anything typed after it is still to be saved
    syncAfterSave(res);
  }
  function syncAfterSave(item) {
    root.dataset.words = item.word_count;
    var mw = $("#meta-words");
    if (mw) mw.textContent = words(item.word_count);
    if (S.tree) S.tree.setWords(cfg.id, item.word_count);
    showGuard(item.word_count);
    doc.dispatchEvent(new win.CustomEvent("scriobh:saved", { detail: item }));
  }
  // Take a document the server holds as the editor's text: after a restore, a conflict, a take-over.
  function adopt(cur, keep) {
    ed.setMarkdown(cur.content, { force: true });
    setTitle(cur.title);
    st.rev = cur.revision; st.serverMd = cur.content; st.serverTitle = cur.title;
    want = null; if (!keep) clearDraft();
    syncAfterSave(cur);
    counts(cur.content);
  }
  function retryIn(ms) { clearTimeout(retryTimer); retryTimer = setTimeout(pump, ms); }
  function netFail() {
    var ms = Math.min(30000, 2000 * Math.pow(2, backoff++));
    setState("offline");
    bar("net", "warn", "Offline. Your text is kept here and will save when the connection returns.", [{ label: "Retry now", fn: function () { pump(); } }]);
    retryIn(ms);
    settle(false);
  }
  function failed(r, snap) {
    var err = (r.json && r.json.error) || {}, ms;
    if (r.status === 401) {
      var msg = "Signed out — your text is kept here. Sign in in another tab; it will save when you return.";
      setState("signedout", msg);
      bar("signedout", "warn", msg, [{ label: "Sign in", href: "/auth/login" }, { label: "Retry now", fn: function () { pump(); } }]);
      ms = Math.min(60000, 20000 * Math.pow(1.5, backoff++));
      retryIn(ms);
      settle(false);
    } else if (r.status === 409) {
      conflict(err, snap);
    } else if (r.status === 413) {
      tooLargeMd = snap.md;
      var m = "This chapter is over the 2 MiB limit, so it cannot be saved. Your text is kept in this browser. Split the chapter in two.";
      setState("toolarge", m);
      bar("toolarge", "danger", m, []);
      settle(false);
    } else if (r.status === 422 && (err.field === "title" || err.field === "synopsis_short")) {
      skipTitle = true;
      titleError.textContent = err.message || "That title cannot be saved.";
      titleError.hidden = false;
      setState("error", "The title was not saved. " + titleError.textContent);
      if (textPending()) pump(); else settle(false);
    } else if (r.status === 429) {
      ms = Math.max(2, r.retry || 5) * 1000;
      setState("paused");
      bar("net", "warn", "Too many saves at once. Your text is kept here. Retrying in " + Math.round(ms / 1000) + " seconds.", []);
      retryIn(ms);
      settle(false);
    } else if (r.status === 403 && err.data && err.data.reason === "project_locked") {
      // The project locked (idle, another tab, a new passphrase). The text stays: in the editor, in the draft, and it is saved when js/gate.js says the project is open again.
      gated = true;
      if (ed) ed.flush();
      writeDraft();
      setState("locked");
      settle(false);
    } else if (r.status === 403 || r.status === 404) {
      var m2 = r.status === 404 ? "This chapter no longer exists. Your text is kept in this browser." : "You are not allowed to edit this chapter. Your text is kept in this browser.";
      setState("error", m2);
      bar("net", "danger", m2, []);
      halted = true;
      settle(false);
    } else {
      ms = Math.min(30000, 2000 * Math.pow(2, backoff++));
      setState("error");
      bar("net", "warn", "Could not save. Your text is still here. Retrying in " + Math.round(ms / 1000) + " seconds.", [{ label: "Retry now", fn: function () { pump(); } }]);
      retryIn(ms);
      settle(false);
    }
  }
  // `force`: hand over the editor's own spelling of the text even if nothing was typed (an operation that names places by offset
  // needs the stored text and the editor's to agree on where they are).
  function flushAll(force) {
    if (ed) ed.flush(force === true);
    return new Promise(function (res) {
      if (blocked()) return res(false);
      if (!needs() && !inflight) return res(true);
      if (halted || locked || gated) return res(false);
      waiters.push(res);
      clearTimeout(retryTimer);
      pump();
    });
  }
  // js/gate.js: the project locked (hold what is typed) or opened again (save it, through pump, as ever).
  doc.addEventListener("scriobh:gate", function (e) {
    if (e.detail.state === "locked") { gated = true; if (ed) { ed.flush(); writeDraft(); } return; }
    if (e.detail.state !== "unlocked") return;
    gated = false;
    if (state === "locked" && !locked) setState(needs() ? "saving" : "saved");
    var p = e.detail.prev;
    if (p && editorEl.contains(p)) { ed.focus(); e.preventDefault(); }
    pump();
  });
  // Try again at once when the page comes back to the foreground or the network returns.
  function wake() { if (state !== "saved" && state !== "locked" && state !== "conflict" && !inflight) { clearTimeout(retryTimer); pump(); } }
  win.addEventListener("online", wake);
  win.addEventListener("focus", function () { wake(); renew(); });
  doc.addEventListener("visibilitychange", function () { if (doc.visibilityState === "visible") { wake(); renew(); } else flushAll(); }); // hidden: what is typed goes out now

  // ---- conflicts -------------------------------------------------------------------------
  function ensureCopy(md) {
    if (copiedMd === md) return Promise.resolve(true);
    return api("POST", docPath + "/conflict", { content: md, base_revision: st.rev }).then(function (r) {
      if (r.ok) copiedMd = md;
      return r.ok;
    }, function () { return false; });
  }
  function localMd() { return ed.getMarkdown(); }
  function setTitle(t) { titleEl.value = t; titleGrow.dataset.value = t; mirrorTitle(t); }

  // 409: find out which kind before showing anything.
  function conflict(err, snap) {
    var reason = err.data && err.data.reason;
    if (reason === "project_trashed") { // the whole project went to the trash since the page opened: nothing here can be saved until it is restored
      halted = true;
      setState("conflict", "This project is in the trash. Your text is kept here.");
      bar("conflict", "danger", "This project is in the trash. Your text is kept here. Restore the project from the library to keep writing.", [{ label: "Open the library", href: "/?state=trashed" }]);
      settle(false);
      return;
    }
    fetchDoc().then(function (cur) {
      if (cur.trashed || !reason) {
        halted = true;
        setState("conflict", "This chapter is in the trash. Your text is kept here.");
        bar("conflict", "danger", "This chapter is in the trash. Your text is kept here.", [{ label: "Restore it", fn: function () { restoreDoc(cur); } }]);
        settle(false);
        return;
      }
      // A save whose answer was lost may have landed, even if more was typed after it.
      var landed = [snap].concat(unconfirmed).some(function (u) {
        return (sameText(cur.content, u.md === null ? st.serverMd : u.md)) && (u.title === null || cur.title === u.title);
      });
      if (landed && reason === "revision") {
        var same = want && want.md === cur.content;
        saved(cur, { md: cur.content, ver: same ? want.version : null, title: null });
        return;
      }
      if (reason === "revision" && sameText(cur.content, st.serverMd)) {
        // Only details changed elsewhere (a rename, a move): take the new revision and go on.
        var mine = titleNow() !== st.serverTitle;
        st.rev = cur.revision;
        if (!mine && cur.title !== st.serverTitle) { setTitle(cur.title); if (S.tree) S.tree.setTitle(cur.id, cur.title); }
        st.serverTitle = mine ? st.serverTitle : cur.title;
        if (needs()) pump(); else settle(true);
        return;
      }
      halted = true;
      conflictInfo = { reason: reason, cur: cur };
      var md = want ? want.md : localMd();
      ensureCopy(md);
      var msg = reason === "lease"
        ? "This chapter is open for writing in another tab. Your text is kept, and a copy is stored on the server. Choose which version to keep."
        : "This chapter changed somewhere else since you opened it. Your text is kept, and a copy is stored on the server. Choose which version to keep.";
      setState("conflict", msg);
      bar("conflict", "danger", msg, [
        { label: "Keep mine", fn: keepMine }, { label: "Keep theirs", fn: keepTheirs }, { label: "Compare", fn: compare }]);
      settle(false);
    }, function (r) { if (r && r.status === 401) failed(r, snap); else netFail(); });
  }
  function resolved() {
    halted = false; conflictInfo = null; unbar("conflict"); unbar("compare"); clearNet();
    setState("saved");
  }
  function takeLease(take, background) {
    return api("POST", docPath + "/lease", { tab_id: tab, take: !!take }, 0, background).then(function (r) { return r.ok && r.json ? !!r.json.held : null; }, function () { return null; });
  }
  var resolving = false;
  function resolveWith(fn) {
    if (resolving) return;
    resolving = true;
    var end = function () { resolving = false; };
    Promise.resolve().then(function () {
      ed.flush();
      var md = want ? want.md : localMd();
      return ensureCopy(md).then(function (ok) {
        if (!ok) return toast("Could not store a copy of your text. Nothing was changed.");
        return Promise.resolve().then(function () { return fn(md); }).catch(function () { toast("Could not reach the server. Nothing was changed."); });
      });
    }).then(end, end);
  }
  function keepMine() {
    resolveWith(function (md) {
      return takeLease(true).then(fetchDoc).then(function (cur) {
        var mine = titleNow() !== st.serverTitle;
        st.rev = cur.revision; st.serverMd = cur.content; ed.alts.setComment(altLine(cur.content));
        if (!mine) { st.serverTitle = cur.title; setTitle(cur.title); }
        if (!want) want = { md: md, version: null };
        resolved(); pump();
      });
    });
  }
  function keepTheirs() {
    resolveWith(function () {
      return fetchDoc().then(function (cur) {
        adopt(cur); resolved();
        return takeLease(false).then(function (held) { if (held === false) lockUI(); });
      });
    });
  }
  function restoreDoc(cur) {
    api("POST", docPath + "/restore", {}).then(function (r) {
      if (!r.ok) return toast("Could not restore the chapter.");
      st.rev = r.json.item.revision;
      if (S.tree) S.tree.refresh();
      resolved(); pump();
    }, function () { toast("Could not reach the server."); });
  }

  // The comparison panel (a word-level diff) is its own small file, fetched the first time it is wanted.
  function compare() {
    var open = $("#nb-compare", notices);
    if (open) { open.remove(); margins(); return; }
    var cur = conflictInfo && conflictInfo.cur;
    if (!cur) return;
    lazy(D.compareSrc, "compare", function () { S.compare(cur.content, localMd(), notices); margins(); });
  }

  // ---- the tab lease ---------------------------------------------------------------------------
  var leaseTimer = 0;
  // js/lock.js (the locked page) is fetched when the page locks.
  function lockUI() {
    // Text typed before the lease answer arrived is never stranded behind a lock: the save
    // meets the lease conflict and the three choices appear.
    if (ed) ed.flush();
    if (needs() || inflight) { pump(); return; }
    locked = true;
    if (ed) ed.setReadOnly(true);
    titleEl.readOnly = true; // at once: lock.js arrives later
    setState("locked", "Open in another tab. This tab can read but not edit.");
    lazy(D.lockSrc, "lock", function () { S.lock.show(); });
  }
  // What js/lock.js needs (the rest is S.write).
  S.lockHost = { st: st, takeLease: takeLease, fetchDoc: fetchDoc, bar: bar, unbar: unbar, toast: toast, setState: setState,
    needs: needs, sameText: sameText, altLine: altLine, startRenewal: startRenewal, offerDraft: offerDraft, setLocked: function (v) { locked = v; } };
  function renew() {
    if (!writable || locked || halted || !ed || doc.visibilityState !== "visible") return;
    takeLease(false, true).then(function (held) {
      if (held !== false || locked) return;
      ed.flush();
      if (needs() || inflight) { want = want || { md: localMd(), version: null }; pump(); } else lockUI();
    });
  }
  function startRenewal() { clearInterval(leaseTimer); leaseTimer = setInterval(renew, 45000); }

  // ---- editor change, counts ---------------------------------------------------------------------
  function onChange(md, info) {
    want = { md: md, version: info.version };
    writeDraft();
    pump();
    counts(md);
  }
  function chars(md) { return md.length - (md.match(/[\uD800-\uDBFF]/g) || []).length; }
  var showCounts = D.counts !== "false"; // the person's setting; the status bar switch writes it back
  function counts(md) {
    var n = ed.mode() === "source" ? (md || ed.getMarkdown()).split(/\s+/).filter(function (t) { return /[\p{L}\p{N}]/u.test(t); }).length : ed.wordCount();
    lastWords = n;
    renderCounts(n, md);
    clearTimeout(wordsTimer);
    wordsTimer = setTimeout(function () { announce(words(n)); }, 4000);
  }
  var cw = $("#st-words"), cc = $("#st-chars"), cr = $("#st-read"), cs = $("#st-sel"), cbtn = $("#st-counts");
  function renderCounts(n, md) {
    if (!cbtn) return;
    if (!showCounts) { cw.textContent = "Counts hidden"; cc.textContent = cr.textContent = ""; return; }
    cw.textContent = words(n);
    cc.textContent = md == null ? cc.textContent : fmt(chars(md)) + " characters";
    cr.textContent = n ? Math.max(1, Math.round(n / 238)) + " min read" : "";
  }

  // ---- guardrails (F-275) ---------------------------------------------------------------------------
  var guardDismissed = false;
  function showGuard(n) {
    if (cfg.readonly === "role" || n < 15000) { unbar("guard"); return; }
    if (guardDismissed && n < 25000) return;
    var ro = n >= 25000;
    var msg = ro
      ? "This chapter is " + fmt(n) + " words, over the 25,000-word limit for one document. It opens read-only. Split it at a scene break into shorter chapters."
      : "This chapter is " + fmt(n) + " words. Long chapters slow editing down. Consider splitting it at a scene break.";
    var acts = [];
    var sb = $('[data-cmd="split"]', fmtbar); // js/split.js, labelled as the toolbar button is
    if (cfg.canEdit) acts.push({ label: (sb && sb.dataset.tip) || "Split chapter here", fn: function () { if (S.split) S.split(); else toast("Still loading. Try again in a moment."); } });
    if (!ro) acts.push({ label: "Dismiss", quiet: true, fn: function () { guardDismissed = true; unbar("guard"); } });
    bar("guard", "warn", msg, acts);
  }

  // ---- toolbar ------------------------------------------------------------------------------------------
  // The formatting bar's own code is js/format.js (it has no budget to share with the save loop); the counts switch stays.
  var tl = function (f) { if (S.tools) S.tools[f](); };
  function wireCounts() {
    if (fmtbar) fmtbar.hidden = false;
    if (cbtn) {
      cbtn.hidden = false;
      cbtn.setAttribute("aria-pressed", String(showCounts));
      cbtn.addEventListener("click", function () {
        showCounts = !showCounts;
        api("PUT", "/api/v1/me/prefs", { items: { show_word_count: String(showCounts) } }).catch(function () {});
        cbtn.setAttribute("aria-pressed", String(showCounts));
        renderCounts(lastWords, ed.getMarkdown());
      });
    }
  }

  // ---- the title field ------------------------------------------------------------------------------------
  function mirrorTitle(t) {
    var c = $("#crumb-title"); if (c) c.textContent = t || "Untitled";
    doc.title = (t || "Untitled") + doc.title.replace(/^[^·]*/, "");
    if (S.tree) S.tree.setTitle(cfg.id, t);
  }
  var titleTimer = 0;
  function wireTitle() {
    titleStatic.hidden = true;
    titleGrow.hidden = false;
    titleEl.readOnly = !writable;
    titleEl.addEventListener("input", function () {
      titleGrow.dataset.value = titleEl.value;
      titleError.hidden = true;
      skipTitle = false;
      mirrorTitle(titleNow());
      writeDraft(); // kept in this browser at once, as the text is
      clearTimeout(titleTimer);
      titleTimer = setTimeout(function () { if (titlePending()) pump(); }, saveDelay);
    });
    titleEl.addEventListener("blur", function () { clearTimeout(titleTimer); if (titlePending()) pump(); });
    titleEl.addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); ed.focus(); }
      else if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "s") { e.preventDefault(); flushAll(); }
    });
  }

  // ---- navigation, shortcuts, unload ---------------------------------------------------------------------------
  function toast(m) { if (S.toast) S.toast(m); }
  function go(href) {
    flushAll().then(function (ok) {
      if (ok) win.location.assign(href);
      else toast("Not saved yet. Staying here so nothing is lost.");
    });
  }
  doc.addEventListener("click", function (e) {
    var a = e.target.closest && e.target.closest("a[href]");
    if (!a || e.defaultPrevented || e.button || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || a.target) return;
    var u = new URL(a.href, win.location.href);
    if (u.origin !== win.location.origin || u.pathname === win.location.pathname && u.search === win.location.search) return;
    if (gated || !unsaved()) return; // locked: what was typed is in the draft already, and the cover's own link must leave
    e.preventDefault();
    go(a.href);
  });
  doc.addEventListener("keydown", function (e) {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "s") { e.preventDefault(); flushAll(); }
    else if (e.altKey && !e.ctrlKey && !e.metaKey && (e.key === "PageUp" || e.key === "PageDown")) {
      var to = root.dataset[e.key === "PageUp" ? "prev" : "next"];
      if (to) { e.preventDefault(); go(to); }
    }
  });
  win.addEventListener("beforeunload", function (e) {
    if (gated || !unsaved()) return; // a locked page's text is in the draft: leaving it must not ask
    e.preventDefault();
    e.returnValue = "";
  });
  win.addEventListener("pagehide", function () {
    if (needs()) writeDraft();
    // A closing tab frees the chapter at once instead of holding it for 90 seconds.
    if (!locked && writable) {
      try { fetch(docPath + "/lease", { method: "POST", keepalive: true, credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf }, body: JSON.stringify({ tab_id: tab, release: true }) }); } catch (e) { /* the lease expires by itself */ }
    }
  });

  // ---- the visual viewport: the formatting bar rides above the on-screen keyboard ------------------------------
  function followKeyboard() {
    var vv = win.visualViewport;
    if (!vv || !win.CSSStyleSheet || !doc.adoptedStyleSheets) return;
    var sheet = new win.CSSStyleSheet(), last = -1;
    doc.adoptedStyleSheets = doc.adoptedStyleSheets.concat([sheet]);
    var update = function () {
      var kb = Math.max(0, Math.round(win.innerHeight - vv.height - vv.offsetTop));
      if (kb < 80) kb = 0;
      if (kb === last) return;
      var opening = kb > 0 && last <= 0;
      last = kb;
      doc.documentElement.classList.toggle("kb", kb > 0);
      sheet.replaceSync(":root{--kb:" + kb + "px}");
      if (opening && ed && ed.mode() === "rich" && doc.activeElement && ed.view.dom.contains(doc.activeElement)) {
        ed.view.dispatch(ed.view.state.tr.scrollIntoView());
      }
    };
    vv.addEventListener("resize", update);
    vv.addEventListener("scroll", update);
    update();
  }

  // ---- start ------------------------------------------------------------------------------------------------------
  // Small, rarely used parts are their own files, fetched the first time they are needed.
  function lazy(src, ready, fn) {
    if (S[ready]) return fn();
    S.loadScript(src).then(fn, function () { toast("Could not load that. Your text is unchanged."); });
  }
  function onLink(info) { lazy(D.linkSrc, "linkDialog", function () { S.linkDialog(info, ed); }); }
  function limit(info) { if (info.level) showGuard(info.words); }

  function start() {
    var ro = !writable;
    ed = win.scriobhEditor.mount(editorEl, {
      markdown: initialMd, readOnly: ro, label: "Document text", sourceLabel: "Markdown source",
      limits: { debounceMs: saveDelay },
      onChange: onChange, onLimit: limit, onLink: onLink,
      onWarning: function () { bar("parse", "warn", "Part of this chapter could not be shown as rich text. It is kept as source, and nothing is lost.", [{ label: "Dismiss", quiet: true, fn: function () { unbar("parse"); } }]); }
    });
    margins();
    win.addEventListener("resize", margins);
    // Toolbar state follows every transaction (a stored mark raises no selectionchange).
    var dispatch = ed.view.dispatch;
    ed.view.dispatch = function (tr) { dispatch(tr); if (tr.selectionSet || tr.storedMarksSet || tr.docChanged) tl("selection"); };
    // Source mode flips `hidden` on the surfaces (Mod+E is handled inside the editor).
    new win.MutationObserver(function () { tl("selection"); counts(); }).observe(editorEl, { attributes: true, subtree: true, attributeFilter: ["hidden"] });
    plain.remove();
    editorEl.hidden = false;
    stateEl.hidden = false;
    wireTitle();
    wireCounts();
    counts(initialMd);
    tl("update");
    followKeyboard();
    syncAfterSave({ word_count: parseInt(D.words, 10) || 0 });
    if (cfg.readonly === "role") { bar("role", "info", "You can read this project but not change it.", []); setState("locked", "Read only"); locked = true; }
    else if (cfg.readonly === "length") { bar("guard", "warn", "This chapter is over the 25,000-word limit for one document, so it opens read-only. Split it at a scene break into shorter chapters.", []); showGuard(parseInt(D.words, 10) || 25000); }
    S.write = { cfg: cfg, flushAll: flushAll, unsaved: unsaved, ui: ui, editor: ed, go: go, setTitle: setTitle, get rev() { return st.rev; }, set rev(v) { st.rev = v; }, get tab() { return tab; }, adopt: adopt, api: api, markdown: markdown,
      get locked() { return locked; }, get serverMd() { return st.serverMd; }, get serverTitle() { return st.serverTitle; }, get showCounts() { return showCounts; }, words: words, counts: counts, afterAlt: afterAlt, lazy: lazy, person: D.user };
    if (/[?&]new=1\b/.test(win.location.search)) {
      titleEl.focus();
      win.history.replaceState(null, "", win.location.pathname);
    } else if (win.matchMedia("(pointer: fine)").matches && writable && !initialMd.trim()) ed.focus();
    // Remember where this person is, for Continue where I left off (a REST call, never a GET side effect).
    api("POST", "/api/v1/projects/" + cfg.project + "/open", { document_id: cfg.id }).catch(function () {});
    if (!writable) return;
    clashed.then(function () {
      return takeLease(false);
    }).then(function (held) {
      if (held === false) { lockUI(); return; }
      startRenewal();
      offerDraft();
    });
  }

  function offerDraft() {
    heldMoved = true;
    holdOnLoad();
    showHeld();
  }
  // The bar for the newest held copy. Drawn from the list as it is: a Discard shows the next one without touching what the page
  // itself has written since it loaded.
  function showHeld() {
    var held = readJSON(heldKey, []).filter(function (h) {
      return typeof h.md === "string" && !(h.md === st.serverMd && (typeof h.title !== "string" || h.title === st.serverTitle));
    });
    if (!held.length) { clearHeld(); return; }
    var d = held[0], dt = typeof d.title === "string" ? d.title : st.serverTitle;
    var older = d.rev === st.rev ? "" : " It is older than the saved chapter, which changed elsewhere.";
    var more = held.length > 1 ? " " + (held.length - 1) + (held.length === 2 ? " older copy is" : " older copies are") + " kept after it." : "";
    var who = d.legacy ? " It was kept before drafts were tied to a person, so it may not have been yours." : "";
    bar("draft", "warn", "Unsaved text from " + new Date(d.t).toLocaleString() + " is still in this browser." + older + who + more, [
      { label: "Restore your unsaved text", fn: function () {
        // Whatever is in the editor now (or the saved text, if the chapter moved on) is copied before it is replaced.
        var now = localMd(), go = now === st.serverMd && d.rev === st.rev ? Promise.resolve(true) : ensureCopy(now);
        go.then(function (ok) {
          if (!ok) return toast("Could not store a copy of the saved text. Nothing was changed.");
          ed.setMarkdown(d.md, { force: true });
          setTitle(dt);
          want = { md: d.md, version: null };
          writeDraft();
          keep("local", heldKey, held.length > 1 ? JSON.stringify(held.slice(1)) : null); // older drafts stay
          unbar("draft");
          counts(d.md);
          pump();
        });
      } },
      { label: "Discard", fn: function () { keep("local", heldKey, held.length > 1 ? JSON.stringify(held.slice(1)) : null); unbar("draft"); if (held.length > 1) showHeld(); } }]);
  }

  S.beforeSignOut = flushAll; // signing out (shell.js) saves first, and stays if it cannot

  // The desktop app is about to restart for an update and has asked the server, which tells every writing page over the live stream
  // (js/live.js, data-flush; a plain server never sends it): save now, then say whether anything is left. It is the page's own save and
  // changes nothing on the screen, takes no focus and asks nothing of the person; what cannot be saved stays in the local draft.
  doc.addEventListener("scriobh:event", function (e) {
    var d = e.detail || {};
    if (d.type !== "desktop.save" || !d.flusher) return;
    flushAll().then(function (ok) {
      var saved = ok && !unsaved();
      fetch("/desktop/saved", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Scriobh-Fetch": "1" }, body: JSON.stringify({ n: d.n, flusher: d.flusher, saved: saved }) }).catch(function () { /* the app waits ten seconds and says so */ });
    });
  });

  start();
})();
