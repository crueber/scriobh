/* Scríobh writing view: names from the bible, underlined in the text.
   - After a save, or when the page has been still, mention.list?spans=true says where each name and alias is. They
     are never asked for, nor drawn anew, while keys are going down; between asks, an edit only moves the marks that
     it did not touch.
   - A mark is a thin solid underline (a wiki link is dotted, a missing one dashed); a name that could mean more than
     one entity is wavy. Neither rests on colour.
   - Hovering a mark, resting the caret in it, or tapping it shows a small card with the book's own summary and a
     link to the sheet; Ctrl or Command with a click goes straight there. Escape closes the card.
   - Every mark is checked against the text it should stand on: if the server's places and the editor's words do not
     agree, nothing is drawn rather than something wrong.
   - A palette command turns the marks off and on (remembered in this browser).
   The editor bundle is unchanged: this works through view, setDecorations and the shared decoration slot. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, E = win.scriobhEditor;
  if (!W || !E || !W.editor || !W.editor.view) return;
  var ed = W.editor, view = ed.view, api = W.api, slug = W.cfg.slug, T = E.markdown.schema, LET = /[\p{L}\p{N}]/u;
  var el = function (tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  var KIND = { character: "Character", place: "Place", item: "Item", org: "Organisation", concept: "Concept" };
  var on = true, items = {}, base = null, marks = [], set = null, setDoc = null, lastKey = 0, timer = 0, asked = 0, retries = 0;
  try { on = win.localStorage.getItem("scriobh:mentions") !== "off"; } catch (e) { /* the marks stay on */ }
  view.dom.addEventListener("keydown", function () { lastKey = Date.now(); }, true);

  // ---- where the words are in the editor ------------------------------------------------------------------------------
  // The prose as the server reads it: text blocks (not code blocks) in order, with private notes and links held back as
  // characters that match nothing, so they can neither be found nor shift the count.
  function flatten(d) {
    var txt = [], at = [];
    d.descendants(function (n, pos) {
      if (n.type.spec.code) return false;
      if (!n.isTextblock) return true;
      if (txt.length) { txt.push("\n"); at.push(-1); }
      n.forEach(function (c, off) {
        var p = pos + 1 + off, i;
        if (c.isText && !c.marks.some(function (m) { return m.type === T.marks.private_note; })) {
          for (i = 0; i < c.text.length; i++) { txt.push(c.text[i]); at.push(p + i); }
        } else for (i = 0; i < c.nodeSize; i++) { txt.push("\u0001"); at.push(-1); }
      });
      return false;
    });
    return { s: txt.join(""), at: at };
  }
  function place(md, list) {
    var all = [], f = flatten(view.state.doc), out = [], cur = 0, bad = 0;
    list.forEach(function (m, k) { (m.spans || []).forEach(function (sp) { all.push({ k: k, a: sp.start, b: sp.end }); }); });
    all.sort(function (x, y) { return x.a - y.a; });
    all.forEach(function (sp) {
      var t = md.slice(sp.a, sp.b), i;
      if (t.slice(0, 2) === "[[") return; // a wiki link has its own marks
      if (!t || t.length > 120 || t.indexOf("\n") >= 0) { bad++; return; }
      for (i = f.s.indexOf(t, cur); i >= 0; i = f.s.indexOf(t, i + 1)) {
        if (!(i > 0 && LET.test(f.s[i - 1])) && !LET.test(f.s[i + t.length] || " ")) break;
      }
      if (i < 0 || f.at[i] < 0 || f.at[i + t.length - 1] < 0) { bad++; return; }
      out.push({ from: f.at[i], to: f.at[i + t.length - 1] + 1, k: sp.k, t: t });
      cur = i + t.length;
    });
    return bad ? null : out;
  }

  // ---- drawing -----------------------------------------------------------------------------------------------------------
  // Between asks the marks follow the text: the one stretch an edit changed is dropped, the rest move with it.
  function follow(d) {
    if (!base || base === d) { base = d; return; }
    var s = base.content.findDiffStart(d.content), e = base.content.findDiffEnd(d.content);
    if (s != null && e) {
      var over = s - Math.min(e.a, e.b);
      if (over > 0) { e.a += over; e.b += over; }
      marks = marks.map(function (m) {
        if (m.to <= s) return m;
        if (m.from >= e.a) return { from: m.from + e.b - e.a, to: m.to + e.b - e.a, k: m.k, t: m.t };
        return null;
      }).filter(function (m) { return m && d.textBetween(m.from, m.to) === m.t; });
    }
    base = d; set = null;
  }
  function draw(state) {
    if (!on) return [];
    follow(state.doc);
    if (set && setDoc === state.doc) return set;
    setDoc = state.doc;
    set = marks.map(function (m) {
      var it = items[m.k];
      return E.Decoration.inline(m.from, m.to, { class: "mention" + (it && it.ambiguous ? " mention-amb" : ""), "data-mention": String(m.k) });
    });
    return set;
  }
  function redraw() { set = null; S.decorate(ed, "mentions", draw); }
  S.decorate(ed, "mentions", draw);

  // ---- asking ----------------------------------------------------------------------------------------------------------------
  function ask() {
    win.clearTimeout(timer);
    if (!on || doc.hidden) return;
    if (W.unsaved() || Date.now() - lastKey < 1500) { timer = win.setTimeout(ask, 1500); return; }
    var mine = ++asked, md = ed.getMarkdown(), rev = W.rev;
    api("GET", "/api/v1/docs/" + W.cfg.id + "/mentions?spans=true").then(function (r) {
      if (mine !== asked || !r.ok || !r.json) return;
      // Anything typed or saved since the question was put makes the answer stale.
      if (W.unsaved() || rev !== W.rev || md !== ed.getMarkdown()) { timer = win.setTimeout(ask, 1500); return; }
      var d = r.json;
      if (d.indexing && retries++ < 4) timer = win.setTimeout(ask, 3000);
      else if (!d.indexing) retries = 0;
      items = d.items || [];
      var spots = d.spans_complete ? place(md, items) : null;
      base = view.state.doc;
      marks = spots || [];
      redraw();
    }, function () {});
  }
  function soon(ms) { win.clearTimeout(timer); timer = win.setTimeout(ask, ms); }
  doc.addEventListener("scriobh:saved", function () { retries = 0; soon(2500); });
  doc.addEventListener("visibilitychange", function () { if (!doc.hidden) soon(800); });
  win.addEventListener("pageshow", function () { soon(800); });
  soon(1200);

  // ---- the card ----------------------------------------------------------------------------------------------------------------
  var card = el("div", "men-card"), sheet = null, shownFor = null, hideT = 0, showT = 0;
  card.id = "men-card"; card.setAttribute("role", "group"); card.setAttribute("aria-label", "Name from the bible"); card.hidden = true;
  doc.body.appendChild(card);
  if (win.CSSStyleSheet && doc.adoptedStyleSheets) { sheet = new win.CSSStyleSheet(); doc.adoptedStyleSheets = doc.adoptedStyleSheets.concat([sheet]); }
  function hide() { win.clearTimeout(showT); win.clearTimeout(hideT); card.hidden = true; shownFor = null; }
  function say(m) { var l = doc.getElementById("live"); if (l) { l.textContent = ""; win.setTimeout(function () { l.textContent = m; }, 40); } }
  function show(node, announce) {
    var it = items[Number(node.dataset.mention)];
    if (!it) return;
    // The card is a floating note a screen reader does not meet by itself: when it comes up from the caret resting in a
    // name, the same words are spoken.
    if (announce && shownFor !== node) say(it.entity.name + ", " + (KIND[it.entity.kind] || it.entity.kind) + ". " + (it.entity.summary || "No summary yet.") + (it.ambiguous ? " These words could also mean another entity." : ""));
    win.clearTimeout(hideT);
    if (shownFor === node && !card.hidden) return;
    shownFor = node;
    var b = it.entity, a = el("a", "men-name", b.name), kids = [a, el("span", "insp-tag", KIND[b.kind] || b.kind)];
    a.href = "/p/" + slug + "/e/" + b.id;
    if (b.overridden) kids.push(el("span", "insp-tag", "This book's version"));
    var p = el("p", "men-sum", b.summary || "No summary yet.");
    var line = el("p", "men-top"); line.append.apply(line, kids);
    card.replaceChildren(line, p);
    if (it.ambiguous) {
      var also = (it.also || []).map(function (x) { return x.name; });
      card.append(el("p", "men-sum", "These words could also mean" + (also.length ? ": " + also.join(", ") : " another entity") + ". The first is used."));
    }
    card.hidden = false;
    if (sheet) {
      var r = node.getBoundingClientRect(), w = Math.min(320, win.innerWidth - 16), left = Math.max(8, Math.min(r.left, win.innerWidth - w - 8));
      var vv = win.visualViewport, room = (vv ? vv.offsetTop + vv.height : win.innerHeight) - 110, h = card.offsetHeight || 90;
      var top = r.bottom + 6 + h > room && r.top - 6 - h > 60 ? r.top - 6 - h : r.bottom + 6;
      sheet.replaceSync("#men-card{left:" + Math.round(left) + "px;top:" + Math.round(top) + "px;width:" + w + "px}");
    }
  }
  function markOf(t) { return t && t.closest ? t.closest(".mention") : null; }
  view.dom.addEventListener("mouseover", function (e) {
    var m = markOf(e.target);
    win.clearTimeout(showT);
    if (!m) { if (!card.hidden) hideT = win.setTimeout(hide, 350); return; }
    showT = win.setTimeout(function () { show(m); }, 450);
  });
  card.addEventListener("mouseover", function () { win.clearTimeout(hideT); });
  card.addEventListener("mouseleave", function () { hideT = win.setTimeout(hide, 350); });
  view.dom.addEventListener("click", function (e) {
    var m = markOf(e.target);
    if (!m) { hide(); return; }
    if (e.ctrlKey || e.metaKey) {
      var it = items[Number(m.dataset.mention)];
      if (it) { e.preventDefault(); W.flushAll().then(function () { win.location.assign("/p/" + slug + "/e/" + it.entity.id); }); }
      return;
    }
    show(m);
  });
  // The caret at rest inside a name shows the card; typing, or leaving the name, takes it away.
  var restT = 0;
  doc.addEventListener("selectionchange", function () {
    win.clearTimeout(restT);
    restT = win.setTimeout(function () {
      if (!on || Date.now() - lastKey < 900) return;
      var s = win.getSelection(), n = s && s.focusNode, m = n && markOf(n.nodeType === 1 ? n : n.parentNode);
      if (m && view.dom.contains(m)) show(m, true); else if (card.contains(doc.activeElement) === false && shownFor) hide();
    }, 900);
  });
  doc.addEventListener("keydown", function (e) { if (e.key === "Escape" && !card.hidden) { hide(); } }, true);
  view.dom.addEventListener("keydown", function (e) { if (e.key.length === 1 || e.key === "Backspace" || e.key === "Enter") hide(); });

  // ---- on and off -----------------------------------------------------------------------------------------------------
  // The name under the caret, if there is one: what the palette's "Open the entity under the cursor" goes to.
  function underCaret() {
    // The editor's own selection, not the page's: the palette that asks has taken the page's selection away.
    var at = view.domAtPos(view.state.selection.from), n = at.node, m = n.nodeType === 3 ? markOf(n.parentNode)
      : markOf(n.childNodes[at.offset - 1]) || markOf(n.childNodes[at.offset]) || markOf(n); // at the edge of a name, it is the name
    return m && view.dom.contains(m) ? items[Number(m.dataset.mention)] : null;
  }
  S.mentions = {
    get on() { return on; },
    open: function () {
      var it = underCaret();
      if (!it) { say(on ? "The cursor is not in a name from the bible." : "Names from the bible are turned off."); if (S.toast) S.toast("The cursor is not in a name from the bible."); return false; }
      W.flushAll().then(function () { win.location.assign("/p/" + slug + "/e/" + it.entity.id); });
      return true;
    },
    toggle: function () {
      on = !on; hide();
      try { win.localStorage.setItem("scriobh:mentions", on ? "on" : "off"); } catch (e) { /* kept for this page only */ }
      if (on) ask(); else redraw();
      var l = doc.getElementById("live"); if (l) l.textContent = on ? "Names from the bible are underlined." : "Names from the bible are no longer underlined.";
      return on;
    }
  };
})();
