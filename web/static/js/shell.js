/* Scríobh shell: CSRF for htmx, the dialog, toasts, typed confirmations.
   Vanilla, no dependencies. Nothing here animates or reflows text. */
(function () {
  "use strict";
  var doc = document;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  var csrfMeta = $('meta[name="csrf-token"]');
  var csrf = (csrfMeta && csrfMeta.content) || "";
  function setCsrf(t) { if (t) { csrf = t; if (csrfMeta) csrfMeta.content = t; } }

  // Every htmx request carries the CSRF token.
  doc.addEventListener("htmx:configRequest", function (e) {
    if (csrf) e.detail.headers["X-CSRF-Token"] = csrf;
  });

  function refocus(el) {
    if (el && el.isConnected) el.focus({ preventScroll: true });
    else { var m = $("#main"); if (m) m.focus({ preventScroll: true }); }
  }

  // ---- toasts -----------------------------------------------------------
  var toasts = $("#toasts");
  function toast(message, opts) {
    if (!toasts || !message) return;
    opts = opts || {};
    var t = doc.createElement("div"), s = doc.createElement("span"), timer;
    t.className = "toast";
    s.textContent = message;
    t.appendChild(s);
    var prev = doc.activeElement;
    function remove() {
      clearTimeout(timer);
      var had = t.contains(doc.activeElement);
      t.remove();
      if (!had) return;
      var back = prev && prev.isConnected && prev !== doc.body ? prev : $("#library-title") || $("#main");
      if (back) back.focus({ preventScroll: true });
    }
    function arm() { clearTimeout(timer); timer = setTimeout(remove, 6000); }
    var b = null;
    if (opts.undo) {
      b = doc.createElement("button");
      b.type = "button";
      b.textContent = "Undo";
      b.addEventListener("click", function () {
        remove();
        if (typeof opts.undo === "function") opts.undo();
        else if (window.htmx) {
          // Undo on the library redraws the shelf; elsewhere it only runs the action.
          var shelf = $("#shelf-wrap");
          window.htmx.ajax("POST", String(opts.undo), shelf ? { target: "#shelf-wrap", swap: "outerHTML" } : { swap: "none" });
        }
      });
      t.appendChild(b);
    }
    t.addEventListener("mouseenter", function () { clearTimeout(timer); });
    t.addEventListener("focusin", function () { clearTimeout(timer); });
    t.addEventListener("mouseleave", arm);
    t.addEventListener("focusout", arm);
    toasts.appendChild(t);
    while (toasts.children.length > 3) toasts.firstChild.remove();
    arm();
    // A toast with an Undo that was started from the keyboard takes focus, so Undo can be reached and used.
    if (opts.focus && b) b.focus();
  }
  // Was the last thing the person did a key press? (Decides whether a toast's Undo takes focus.)
  var byKeyboard = false;
  doc.addEventListener("keydown", function () { byKeyboard = true; }, true);
  doc.addEventListener("pointerdown", function () { byKeyboard = false; }, true);
  var scripts = {};
  function loadScript(src) {
    if (!src) return Promise.reject(new Error("no script"));
    if (scripts[src]) return scripts[src];
    return (scripts[src] = new Promise(function (res, rej) {
      var s = doc.createElement("script");
      s.src = src;
      s.onload = function () { res(); };
      s.onerror = function (e) { delete scripts[src]; s.remove(); rej(e); };
      doc.head.appendChild(s);
    }));
  }
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function fmt(n) { return String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }
  var signOutHooks = [];
  window.scriobh = {
    $: $,
    $$: $$,
    el: el,
    fmt: fmt,
    toast: toast,
    loadScript: loadScript,
    getCsrf: function () { return csrf; },
    setCsrf: setCsrf,
    onBeforeSignOut: function (fn) { if (typeof fn === "function") signOutHooks.push(fn); }
  };
  // A question before something that cannot be undone, asked in the page, where the button was (js/arm.js, fetched the first
  // time it is needed). Never the browser's own confirmation panel: a window may have none (the desktop application's has none).
  window.scriobh.confirm = function (host, o) {
    if (window.scriobh.arm) { window.scriobh.arm.ask(host, o); return; }
    var m = $('meta[name="scriobh-arm"]');
    loadScript(m ? m.content : "").then(function () { window.scriobh.arm.ask(host, o); }, function () {
      toast("That could not be asked, so nothing was done. Reload the page and try again.");
    });
  };

  // A form that names a script (data-lazy) is handed to it: the first submit fetches the script, which takes the form
  // over, and the submit is made again. Without script the form posts as it is.
  doc.addEventListener("submit", function (e) {
    var f = e.target, src = f.getAttribute && f.getAttribute("data-lazy");
    if (!src) return;
    e.preventDefault();
    if (f.lazy) return;
    f.lazy = 1;
    var by = e.submitter, s = doc.createElement("script");
    s.src = src;
    s.onload = function () { f.lazy = 0; f.removeAttribute("data-lazy"); f.requestSubmit(by); };
    s.onerror = function () { f.lazy = 0; };
    doc.head.appendChild(s);
  }, true);
  // The editor draws one set of decorations. Focus dimming and wiki-link marks both need it, so each registers a
  // function returning decorations and the slot draws them together; registering again redraws.
  var decorators = {};
  window.scriobh.decorate = function (ed, name, fn) {
    decorators[name] = fn;
    ed.setDecorations(function (state) {
      // A decorator returns an array of decorations, or a DecorationSet it keeps from one document to the next (the
      // alternatives do, so a key does not rebuild thousands of them).
      var all = [], sets = [], D = window.scriobhEditor.DecorationSet;
      Object.keys(decorators).forEach(function (k) {
        var r = decorators[k](state);
        if (!r) return;
        if (Array.isArray(r)) all = all.concat(r); else sets.push(r);
      });
      var out = sets.length ? sets[0] : null;
      for (var i = 1; i < sets.length; i++) out = out.add(state.doc, sets[i].find());
      if (all.length) out = out ? out.add(state.doc, all) : D.create(state.doc, all);
      return out || D.empty;
    });
  };
  // The writing view's lazy parts (wiki links, inspector, versions, goals): fetched once the page has loaded,
  // never in the way of typing. #write lists them as data-ext, a space-separated list of asset URLs.
  // Any element may carry data-ext; the live-update client is one of them (see liveMarker in the templates).
  var exts = $$("[data-ext]");
  if (exts.length) {
    window.addEventListener("load", function () {
      exts.forEach(function (e) { e.dataset.ext.split(" ").forEach(function (src) {
        if (!src) return;
        var s = doc.createElement("script"); s.src = src;
        // A part that did not arrive must not leave dead buttons behind: the formatting bar says so in words, and the text still saves.
        s.onerror = function () {
          var bar = /\/format\./.test(src) && $("#fmtbar");
          if (bar) { var p = doc.createElement("p"); p.className = "fine fmt-gone"; p.textContent = "The formatting buttons could not be loaded. Your text is not affected and still saves. Bold, italic and links work from the keyboard (" + (window.scriobh && window.scriobh.keys ? window.scriobh.keys.text("Mod+B Mod+I Mod+K") : "the format bar's keys") + "). Reload to try again."; bar.replaceChildren(p); bar.removeAttribute("role"); }
        };
        doc.head.appendChild(s);
      }); });
    });
  }
  // HX-Trigger: {"scriobh:toast": {"message": "Project archived", "undo": "/ui/..."}} or a bare string.
  doc.addEventListener("scriobh:toast", function (e) {
    var d = e.detail || {};
    toast(d.message || d.value, { undo: d.undo, focus: byKeyboard });
  });

  // A typed confirmation: the button stays off until the box holds the exact name.
  function syncConfirm(i) {
    var f = i.form, b = f && $("[data-confirm-submit]", f);
    if (b) b.disabled = i.value !== i.dataset.confirm;
  }
  function confirmGate(root) { $$("[data-confirm]", root).forEach(syncConfirm); }
  doc.addEventListener("input", function (e) { if (e.target && e.target.hasAttribute && e.target.hasAttribute("data-confirm")) syncConfirm(e.target); });
  doc.addEventListener("htmx:afterSettle", function (e) { confirmGate((e.detail && e.detail.elt) || doc); });
  confirmGate(doc);
  // Signing out (or signing this device out) leaves no draft of a manuscript in the browser, but only once the server
  // has said the session is over, and never without asking when a draft holds text the server does not have.
  // Drafts used to be keyed by the document alone, so a later person on the browser could be offered an earlier person's
  // unsaved text. They carry the person's id now. The old keys are not deleted: they may hold text the server never had, so the
  // chapter's own page takes over what is kept for it (doc.js), and sign-out asks about them like any other draft.
  function draftKeys() {
    try { return Object.keys(localStorage).filter(function (k) { return /^scriobh:(draft|held|scratch|research|read|fields):/.test(k); }); } catch (e) { return []; }
  }
  // A scratchpad or research note keeps its unsaved text under scriobh:{scratch|research}:{person}:{p|c}:{id}[:{note}]:
  // the text it holds is compared with what the server has, as a chapter's is.
  function padUrl(k) {
    var m = /^scriobh:(scratch|research):[^:]+:([pc]):([^:]+)(?::([^:]+))?$/.exec(k);
    return m && "/api/v1/" + (m[2] === "p" ? "projects/" : "compendiums/") + encodeURIComponent(m[3]) + (m[1] === "scratch" ? "/scratchpad" : "/research/" + encodeURIComponent(m[4] || ""));
  }
  // How many chapters have a draft that differs from the saved text; and whether any could not be checked.
  function checkDrafts() {
    var bad = 0, unknown = false, locked = 0;
    return Promise.all(draftKeys().map(function (k) {
      var v = null;
      try { v = JSON.parse(localStorage.getItem(k) || "null"); } catch (e) { /* unreadable: ignore */ }
      if (/^scriobh:fields:/.test(k)) { if (v && v.f && v.f.length) bad++; return null; } // changes to a field of a form: there is nothing to compare them with
      if (/^scriobh:(scratch|research):/.test(k)) {
        var u = padUrl(k), a = v && v.v;
        if (!u || !a) return null;
        return fetch(u, { credentials: "same-origin", headers: { Accept: "application/json" } }).then(function (r) {
          if (r.status === 404) { bad++; return; }
          if (!r.ok) { unknown = true; return; }
          return r.json().then(function (j) {
            var it = j.item || {};
            if (["content", "body", "title"].some(function (f) { return typeof a[f] === "string" && a[f] !== it[f]; })) bad++;
          });
        }, function () { unknown = true; });
      }
      var list = (Array.isArray(v) ? v : v ? [v] : []).filter(function (x) { return x && typeof x.md === "string"; });
      if (!list.length) return null;
      return fetch("/api/v1/docs/" + encodeURIComponent(k.split(":").pop()), { credentials: "same-origin", headers: { Accept: "application/json" } }).then(function (r) {
        if (r.status === 404) { bad++; return; }
        if (r.status === 403) { locked++; return; } // a project with a passphrase this session has not unlocked: it cannot be compared, and it is not left behind unasked
        if (!r.ok) { unknown = true; return; }
        return r.json().then(function (j) {
          if (list.some(function (x) { return x.md !== j.item.content || (typeof x.title === "string" && x.title !== j.item.title); })) bad++;
        });
      }, function () { unknown = true; });
    })).then(function () { return { bad: bad, unknown: unknown, locked: locked }; });
  }
  function purge() {
    draftKeys().forEach(function (k) { try { localStorage.removeItem(k); } catch (e) { /* ignore */ } });
    try { Object.keys(sessionStorage).filter(function (k) { return /^scriobh:lk:/.test(k); }).forEach(function (k) { sessionStorage.removeItem(k); }); } catch (e) { /* ignore */ } // the Compendium tab's place in this tab
    try { localStorage.removeItem("scriobh:purge"); } catch (e) { /* ignore */ }
  }
  // A sign-out that could not finish its removal (the network went, the tab closed) finishes it before anything is drawn.
  try { if (localStorage.getItem("scriobh:purge")) purge(); } catch (e) { /* ignore */ }
  function signOut(form) {
    var w = form.dataset, from = form.querySelector("button"), menu = form.closest("[popover]");
    // The questions are asked where the Sign out button is; when the menu it is in is closed (the palette's Sign out), it opens.
    // A form that carries no words of its own borrows the account menu's (every signed-in page has it): never an empty panel.
    var base = ($("#signout") || form).dataset;
    function word(k, last) { return w[k] || base[k] || last; }
    function ask(msg, n, next) {
      if (menu && menu.showPopover && !menu.matches(":popover-open")) menu.showPopover();
      window.scriobh.confirm(form, { msg: (msg || "Sign out and discard the text this browser holds?").split("{n}").join(n), ok: word("mOk", "Sign out and discard"), cancel: word("mCancel", "Stay signed in"), from: from, onOk: next });
    }
    function go() {
      // Whether or not the server could be asked, what this browser keeps is removed: the removal is local and needs no permission. If the
      // request fails or the page is closed first, the marker makes the next page of this browser do it before anything is shown.
      try { localStorage.setItem("scriobh:purge", "1"); } catch (e) { /* storage is off: there is nothing kept */ }
      var failed = function () { try { localStorage.removeItem("scriobh:purge"); } catch (e) { /* ignore */ } toast("Could not sign out. You are still signed in, and nothing was changed."); };
      fetch(form.action, { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": csrf, "X-Scriobh-Fetch": "1" }, body: new URLSearchParams(new FormData(form)) }).then(function (r) {
        if (!(r.ok && r.redirected)) return failed();
        purge();
        window.location.assign("/");
      }, failed);
    }
    checkDrafts().then(function (d) {
      var locked = function () { if (d.locked) ask(word("mLocked"), d.locked, go); else go(); };
      if (d.bad) ask(word("mUnsaved"), d.bad, locked); else locked();
    });
  }
  doc.addEventListener("submit", function (e) {
    var f = e.target;
    if (!f.hasAttribute || !f.hasAttribute("data-signs-out")) return;
    e.preventDefault();
    var all = signOutHooks.slice();
    if (window.scriobh.beforeSignOut) all.push(window.scriobh.beforeSignOut);
    var before = all.length ? Promise.all(all.map(function (fn) { return Promise.resolve(fn()); })).then(function (rs) { return rs.every(Boolean); }) : Promise.resolve(true);
    before.then(function (ok) { if (ok) signOut(f); else toast("Not saved yet. Staying signed in so nothing is lost."); });
  });
  // After a shelf redraw focus goes back to the same control (found by its form or link), or to the heading.
  var shelfSig = null;
  function escAttr(v) { return (window.CSS && CSS.escape) ? CSS.escape(v) : String(v).replace(/["\\]/g, "\\$&"); }
  doc.addEventListener("htmx:beforeSwap", function (e) {
    if (!e.detail.target || e.detail.target.id !== "shelf-wrap") return;
    var a = doc.activeElement, f = a && a.closest && a.closest("form"), h = a && a.getAttribute && a.getAttribute("href");
    shelfSig = f ? 'form[action="' + escAttr(f.getAttribute("action") || "") + '"] button' : h ? 'a[href="' + escAttr(h) + '"]' : null;
  });
  doc.addEventListener("htmx:afterSettle", function () {
    if (shelfSig === null) return;
    var sig = shelfSig, a = doc.activeElement;
    shelfSig = null;
    if (a && a.closest && a.closest("#toasts")) return;
    var f = (sig && $("#shelf-wrap " + sig)) || $("#library-title");
    if (f && (!a || a === doc.body || !a.isConnected)) f.focus({ preventScroll: true });
  });
  // Settings: a choice shows at once; saving it is the form's job.
  doc.addEventListener("change", function (e) {
    var t = e.target, k = t.dataset && t.dataset.live;
    if (!k) return;
    var root = doc.documentElement, v = t.value, name = "data-" + (k === "motion" ? "motion" : k);
    var off = (k === "theme" && v === "system") || (k === "density" && v === "comfortable") || (k === "motion" && v === "system");
    if (off) root.toggleAttribute(name, false);
    else root.setAttribute(name, k === "density" && v === "spacious" ? "roomy" : k === "motion" ? (v === "on" ? "reduce" : "full") : v);
  });

  // ---- account menu ------------------------------------------------------
  // A popover does not move focus by itself: the first item takes it, so the menu reads from the top and Sign out is not the first thing Enter does.
  var acct = $("#account");
  if (acct) acct.addEventListener("toggle", function (e) { if (e.newState === "open") { var f = $(".menu-item", acct); if (f) f.focus(); } });

  // ---- dialog -----------------------------------------------------------
  var dlg = $("#dialog"), body = $("#dialog-body"), opener = null, downOnBackdrop = false;
  if (dlg && body) {
    doc.addEventListener("htmx:beforeRequest", function (e) {
      var d = e.detail;
      if (!d.target || d.target.id !== "dialog-body") return;
      var el = d.elt;
      if (opener) return;
      opener = el && el !== body && el.matches("a,button") ? el : doc.activeElement;
    });
    // A request for the dialog that was redirected (an expired session sends
    // the browser to the sign-in page) must not swap that page into the dialog.
    doc.addEventListener("htmx:beforeSwap", function (e) {
      var d = e.detail;
      if (!d.target || d.target.id !== "dialog-body" || !d.xhr || !d.xhr.responseURL || !d.pathInfo) return;
      var want = String(d.pathInfo.finalRequestPath || "").split("?")[0], got = new URL(d.xhr.responseURL, location.href).pathname;
      if (want && got !== want) {
        d.shouldSwap = false;
        opener = null;
        location.assign(want);
      }
    });
    doc.addEventListener("htmx:afterSwap", function (e) {
      if (e.detail.target !== body) return;
      // Named by the heading of whatever was loaded into it; nothing to point at while it is empty.
      if ($("#dialog-title", body)) dlg.setAttribute("aria-labelledby", "dialog-title"); else dlg.removeAttribute("aria-labelledby");
      if (!dlg.open) dlg.showModal();
    });
    // htmx focuses [autofocus] once it settles. A form re-rendered with errors
    // must instead put focus on the first field in error, and never on <body>.
    doc.addEventListener("htmx:afterSettle", function () {
      var scope = $("dialog[open]") || doc, bad = $('[aria-invalid="true"]', scope);
      if (bad) { bad.focus(); return; }
      var a = doc.activeElement;
      if (a && a !== doc.body && a.isConnected) return;
      var f = $("[autofocus]", scope) || $("#dialog-title", scope);
      if (f) { if (!f.hasAttribute("autofocus")) f.tabIndex = -1; f.focus(); }
    });
    // A failed request keeps the form as it is. While the dialog is open a toast
    // would sit outside the modal, so the message goes inside it.
    var failed = function (e) {
      var x = e.detail && e.detail.xhr, st = x ? x.status : 0, msg;
      if (e.type === "htmx:sendError" || !st) msg = "Could not reach the server. Nothing was lost. Try again.";
      else if (st === 403) msg = "This form expired. Reload the page and try again.";
      else if (st === 429) msg = "Too many requests. Try again in a few seconds.";
      else msg = "That did not go through. Your work is safe. Try again in a moment.";
      if (dlg.open) {
        var p = $("[data-dialog-error]", body);
        if (!p) {
          p = doc.createElement("p");
          p.className = "field-error";
          p.setAttribute("role", "alert");
          p.setAttribute("data-dialog-error", "");
          var head = $(".dialog-head", body);
          if (head) head.after(p); else body.prepend(p);
        }
        p.textContent = msg;
      } else {
        opener = null;
        toast(msg);
      }
    };
    doc.addEventListener("htmx:sendError", failed);
    doc.addEventListener("htmx:responseError", failed);
    dlg.addEventListener("close", function () {
      body.replaceChildren();
      dlg.removeAttribute("aria-labelledby");
      var o = opener;
      opener = null;
      // Native focus restore has normally run; step in only when it left focus nowhere (Safari).
      if (!doc.activeElement || doc.activeElement === doc.body) refocus(o);
    });
    doc.addEventListener("scriobh:close", function () { if (dlg.open) dlg.close(); });
    dlg.addEventListener("pointerdown", function (e) { downOnBackdrop = e.target === dlg; });
    dlg.addEventListener("click", function (e) {
      if (e.target === dlg && downOnBackdrop) dlg.close();
      var c = e.target.closest && e.target.closest("[data-dialog-close]");
      if (c) { e.preventDefault(); dlg.close(); }
    });
  }

  // The browser's time zone, sent once per browser (and again only if the zone changes). The server stores it only for a person who
  // has never chosen one, so a zone a person chose is never changed by this. Upkeep, not use: the request does not keep a gate open.
  // It is sent a second after the load event, never while the page is starting. The request in flight while the writing view mounts
  // was most of the ~25 ms that this block added to that mount at 4x CPU (U5 measurement). The phase-2 budget guards the mount; the page's
  // own first requests go first.
  var zoneKey = "scriobh:zone-sent";
  function sendZone() {
    var zone = ""; try { zone = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (e) { /* no zone to send */ }
    var sent = ""; try { sent = window.localStorage.getItem(zoneKey) || ""; } catch (e) { /* not remembered here: sent again next time */ }
    if (zone && zone !== sent) {
      fetch("/api/v1/me/zone-default", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Accept: "application/json", "X-Scriobh-Background": "1" }, body: JSON.stringify({ timezone: zone }) }).then(function (r) {
        if (r.ok) { try { window.localStorage.setItem(zoneKey, zone); } catch (e) { /* the next page sends it again */ } }
      }, function () { /* offline: sent on a later page */ });
    }
  }
  if ($('meta[name="scriobh-user"]')) window.addEventListener("load", function () { window.setTimeout(sendZone, 1000); });
})();
