/* Scríobh reading mode: turning pages by key, remembering where you stopped, and an optional continuous scroll.
   Every chapter is already on the page, rendered by the server; nothing here is needed to read it. Nothing is
   sent anywhere: the position is kept in this browser only. */
(function () {
  "use strict";
  var doc = document, win = window;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || doc).querySelectorAll(s)); };
  var reader = $("#reader");
  if (!reader) return;
  var project = reader.dataset.project, slug = reader.dataset.slug, stream = $("#read-stream"), live = $("#read-live");
  var KEY = "scriobh:read:" + (reader.dataset.user || "") + ":" + project, MODE = "scriobh.read.scroll";
  var mode = $("#read-mode"), banner = $("#read-continue"), pager = $("#read-pager");
  var scrolling = false, loading = 0, ticket = 0, touched = false, timer = 0;

  var M = {};
  try { M = JSON.parse(($("#read-msg") || { dataset: {} }).dataset.msg || "{}"); } catch (e) { M = {}; }
  function T(k, d, v) { var s = M[k] || d; if (v) for (var n in v) s = s.split("{" + n + "}").join(v[n]); return s; }
  function get(k) { try { return win.localStorage.getItem(k); } catch (e) { return null; } }
  function put(k, v) { try { win.localStorage.setItem(k, v); } catch (e) { /* private window */ } }
  function say(m) { live.textContent = ""; win.setTimeout(function () { live.textContent = m; }, 40); }
  function sections() { return $$("section.chapter", stream); }
  function clamp(n) { return n < 0 ? 0 : n > 1 ? 1 : n; }

  // The chapter at the top of the window, and how far through it the top of the window is.
  function here() {
    var all = sections(), cur = all[0], line = win.innerHeight * 0.3;
    all.forEach(function (s) { if (s.getBoundingClientRect().top <= line) cur = s; });
    var r = cur.getBoundingClientRect(), h = cur.offsetHeight || 1;
    return { el: cur, f: clamp(-r.top / h) };
  }
  function remember(doc_, f, title) {
    put(KEY, JSON.stringify({ doc: doc_, f: Math.round(f * 1000) / 1000, title: title, at: Date.now() }));
  }
  function save() {
    if (!touched) return;
    var h = here();
    remember(h.el.dataset.doc, h.f, h.el.dataset.title);
  }
  function later() { touched = true; win.clearTimeout(timer); timer = win.setTimeout(save, 300); }

  // ---- where you stopped ---------------------------------------------------------------------------------------
  function restore(f) {
    var s = sections()[0];
    if (!s || !isFinite(f)) return;
    win.scrollTo(0, Math.max(0, s.getBoundingClientRect().top + win.pageYOffset + f * s.offsetHeight - 8));
  }
  var m = /^#at=([0-9.]+)$/.exec(win.location.hash);
  if (m) {
    restore(parseFloat(m[1]));
    try { win.history.replaceState(null, "", win.location.pathname + win.location.search); } catch (e) { /* ignore */ }
    touched = true;
  } else {
    var saved = null;
    try { saved = JSON.parse(get(KEY) || "null"); } catch (e) { /* none */ }
    if (saved && saved.doc && banner) {
      var same = saved.doc === reader.dataset.doc;
      if (!same || saved.f > 0.03) {
        var p = doc.createElement("p"), pct = Math.round(saved.f * 100), a;
        p.textContent = pct > 0 ? T("stopped", "You stopped in {title}, {pct}% of the way through.", { title: saved.title || T("this_project", "this project"), pct: pct }) : T("stopped0", "You stopped in {title}.", { title: saved.title || T("this_project", "this project") });
        banner.appendChild(p);
        if (same) {
          a = doc.createElement("button"); a.type = "button"; a.className = "btn solid small"; a.textContent = T("continue", "Continue where you stopped");
          a.addEventListener("click", function () { restore(saved.f); banner.hidden = true; touched = true; });
        } else {
          a = doc.createElement("a"); a.className = "btn solid small"; a.textContent = T("continue", "Continue where you stopped");
          a.href = "/p/" + slug + "/read/" + saved.doc + "#at=" + saved.f;
        }
        var x = doc.createElement("button"); x.type = "button"; x.className = "btn quiet small"; x.textContent = T("dismiss", "Dismiss");
        x.addEventListener("click", function () { banner.hidden = true; });
        banner.appendChild(a); banner.appendChild(x);
        banner.hidden = false;
      }
    }
  }
  win.addEventListener("scroll", later, { passive: true });
  win.addEventListener("pagehide", save);

  // ---- turning pages -------------------------------------------------------------------------------------------
  function link(rel) { return $('a[rel="' + rel + '"]', pager); }
  function turn(dir) {
    if (scrolling) return step(dir);
    var a = link(dir < 0 ? "prev" : "next");
    if (!a) { say(dir < 0 ? T("first", "This is the first page.") : T("last", "This is the last page.")); return; }
    var t = $(".pg-title", a);
    var id = /\/read\/([^/?#]+)$/.exec(a.getAttribute("href"));
    if (id) remember(id[1], 0, t ? t.textContent : "");
    win.location.assign(a.href);
  }
  function inControl(t) {
    return !!(t && t.closest && t.closest("a, button, input, select, textarea, summary, [contenteditable]"));
  }
  function popoverOpen() { try { return !!$("#read-toc:popover-open"); } catch (e) { return false; } }
  doc.addEventListener("keydown", function (e) {
    if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) return;
    var t = e.target;
    if (inControl(t) || popoverOpen()) return;
    var d = e.key === "ArrowRight" || e.key === "j" ? 1 : e.key === "ArrowLeft" || e.key === "k" ? -1 : 0;
    if (!d) return;
    e.preventDefault();
    turn(d);
  });

  // ---- continuous scroll (the next chapters are fetched as you near the end) --------------------------------------
  function load() {
    var all = sections(), last = all[all.length - 1], next = last && last.dataset.next;
    if (!next || loading) return Promise.resolve(null);
    var mine = ++ticket;
    loading++;
    return fetch("/ui/projects/" + project + "/read/" + next, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) {
      if (!r.ok) throw r;
      return r.text();
    }).then(function (t) {
      loading--;
      if (mine !== ticket || !scrolling || sections()[sections().length - 1] !== last) return null;
      var box = doc.createElement("div");
      box.innerHTML = t;
      var s = $("section.chapter", box);
      if (!s) return null;
      stream.appendChild(s);
      return s;
    }, function () { loading--; say(T("noload", "Could not load the next chapter. Turn the page with the link below.")); return null; });
  }
  function near() {
    if (!scrolling) return;
    var h = doc.documentElement;
    if (h.scrollHeight - (win.pageYOffset + win.innerHeight) < win.innerHeight * 1.5) load().then(function (s) { if (s) near(); });
  }
  function step(dir) {
    var all = sections(), cur = here().el, i = all.indexOf(cur), target = all[i + dir];
    if (!target && dir > 0) {
      return load().then(function (s) { if (s) go(s); else say(T("last", "This is the last page.")); });
    }
    if (!target) { if (win.pageYOffset > 0) win.scrollTo(0, 0); else say(T("first", "This is the first page.")); return; }
    go(target);
  }
  function go(s) {
    win.scrollTo(0, s.getBoundingClientRect().top + win.pageYOffset - 8);
    touched = true;
    say(s.dataset.title);
    later();
  }
  function setMode(on) {
    scrolling = on;
    reader.classList.toggle("is-scroll", on);
    mode.setAttribute("aria-pressed", on ? "true" : "false");
    mode.textContent = on ? T("paged", "Read one chapter at a time") : T("scroll", "Scroll continuously");
    put(MODE, on ? "1" : "0");
    if (on) near();
  }
  if (mode) {
    mode.hidden = false;
    mode.addEventListener("click", function () {
      var h = here();
      if (scrolling) {
        // Back to one chapter at a time: go to the chapter you are in, without a trace of the others.
        var cur = h.el, at = h.f;
        sections().forEach(function (s) { if (s !== cur) s.remove(); });
        setMode(false);
        if (cur.dataset.doc !== reader.dataset.doc) {
          win.location.assign("/p/" + slug + "/read/" + cur.dataset.doc + "#at=" + at);
          return;
        }
        restore(at);
      } else setMode(true);
      say(scrolling ? T("on_scroll", "Reading continuously.") : T("on_paged", "Reading one chapter at a time."));
    });
    win.addEventListener("scroll", near, { passive: true });
    if (get(MODE) === "1") setMode(true);
  }
})();
