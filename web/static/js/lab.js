/* Scríobh writing view: the Lab, a tab of the inspector. It marks the text and never rewrites it. The local checks (long
   sentences, hedge words, double spaces and the like) need no model, no policy and no provider, and work with AI off. The checks
   that use a model (tangled sentences, tone, words to trim) exist only when AI is on, and go through the same preview as every
   other request. Marks are drawn in the text with a different line and a small letter for each kind (never colour alone) and
   listed here; each is accepted or dismissed on its own, or a chosen set is accepted as one change after a preview of the whole.
   Marks are private to the person who made them and are only ever drawn; accepting a mark that has a fix is the one thing that
   writes. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, box = doc.getElementById("write");
  if (!W || !box || S.lab) return;
  var P = W.cfg.project, DOC = W.cfg.id, ed = W.editor, view = ed.view, base = "/api/v1/projects/" + P, aiOn = box.dataset.ai === "on";
  var MAXSET = 300; // the most marks one accept-all takes (the server's limit)
  var KIND = { long_sentence: "Long sentence", hedge_words: "Hedge word", punctuation_fix: "Punctuation", convoluted_sentence: "Tangled sentence", tone_mismatch: "Tone", trim_filler: "Filler", trim_adverbs: "Adverb", trim_repetition: "Repetition", trim_dialogue_tags: "Dialogue tag" };
  var PLURAL = { long_sentence: ["long sentence", "long sentences"], hedge_words: ["hedge word", "hedge words"], punctuation_fix: ["punctuation fix", "punctuation fixes"], convoluted_sentence: ["tangled sentence", "tangled sentences"], tone_mismatch: ["tone mark", "tone marks"], trim_filler: ["filler", "fillers"], trim_adverbs: ["adverb", "adverbs"], trim_repetition: ["repetition", "repetitions"], trim_dialogue_tags: ["dialogue tag", "dialogue tags"] };
  var GLYPH = { long_sentence: "L", hedge_words: "H", punctuation_fix: "P", convoluted_sentence: "T", tone_mismatch: "N", trim_filler: "−", trim_adverbs: "−", trim_repetition: "−", trim_dialogue_tags: "−" };
  var HOW = { long_sentence: "A sentence over the length you would likely want", hedge_words: "A word that weakens what is said", punctuation_fix: "A small fix to spacing, quotes or repeated marks", convoluted_sentence: "A reader may have to read it twice", tone_mismatch: "It does not fit the project's tone", trim_filler: "Words that can go without changing the meaning", trim_adverbs: "An adverb that may be doing a verb's job", trim_repetition: "A repeat of something already said", trim_dialogue_tags: "A tag that says what the dialogue already shows" };
  function h(tag, attrs) { var e = doc.createElement(tag); for (var k in attrs || {}) { var v = attrs[k]; if (v == null || v === false) continue; if (k === "text") e.textContent = v; else if (k === "class") e.className = v; else e.setAttribute(k, v === true ? "" : v); } (function add(l) { for (var i = 0; i < l.length; i++) { var c = l[i]; if (c == null || c === false) continue; if (Array.isArray(c)) add(c); else e.appendChild(typeof c === "string" ? doc.createTextNode(c) : c); } })([].slice.call(arguments, 2)); return e; }
  function say(m) { if (S.alts) S.alts.say(m); }
  function note(m) { if (S.alts) S.alts.note(m); else if (S.toast) S.toast(m); }
  function call(method, path, body) {
    var o = { method: method, credentials: "same-origin", headers: Object.assign({ Accept: "application/json" }, body !== undefined ? { "Content-Type": "application/json" } : {}) };
    if (body !== undefined) o.body = JSON.stringify(body);
    return win.fetch(path, o).then(function (r) { return r.text().then(function (t) { var j = null; try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ } return { ok: r.ok, status: r.status, json: j }; }); }, function () { return { ok: false, status: 0, json: null }; });
  }
  function why(r) { return (r.json && r.json.error && r.json.error.message) || (r.status === 0 ? "Could not reach the server." : "That did not work."); }
  [box.dataset.labCss, box.dataset.aiCss].forEach(function (href) { if (href && !doc.querySelector('link[href="' + href + '"]')) { var l = doc.createElement("link"); l.rel = "stylesheet"; l.href = href; doc.head.appendChild(l); } });
  // The writing partner's core, whichever of the boot file and this one came first.
  function core(cb, fail) {
    if (S.aiLoad) return S.aiLoad(cb, fail);
    var done = false, t = 0;
    function go() { if (done) return; done = true; win.clearTimeout(t); doc.removeEventListener("scriobh:aiboot", go); if (S.aiLoad) S.aiLoad(cb, fail); else fail(); }
    doc.addEventListener("scriobh:aiboot", go);
    t = win.setTimeout(go, 8000);
  }
  function plain(s) { return /^\s*$/.test(s); }
  function gap(s) { return s.length === 1 ? "a space" : s.length + " spaces"; }
  function quoteText(m) { return plain(m.quote) ? "(" + gap(m.quote) + ")" : m.quote; }
  function fixText(m) {
    if (m.action === "none") return "An observation: there is nothing to change, only to look at.";
    if (m.action === "delete") return plain(m.quote) ? "The fix: take out the extra space." : "The fix: take these words out.";
    if (plain(m.fix)) return "The fix: put " + gap(m.fix) + " in its place.";
    return "The fix: put “" + m.fix + "” in its place.";
  }

  // ---- marks in the text --------------------------------------------------------------------------------------------------
  var marks = [], truncated = false, set = null, setDoc = null, root = null, shown = false, filter = "", visible = true, timer = 0, ctl = null;
  // The set, carried through every edit since it was drawn: positions are always those of the text as it is now.
  function current() {
    if (!set) return null;
    if (setDoc !== view.state.doc) { set = ed.alts.mapSet(set, setDoc, view.state.doc); setDoc = view.state.doc; }
    return set;
  }
  function decorFn() { return visible ? (current() || []) : []; }
  S.decorate(ed, "lab", decorFn);
  function live() { // the marks that are still in the text, where they are now, in order
    var s = current();
    return s ? s.find().map(function (d) { return { id: d.spec.id, from: d.from, to: d.to }; }).sort(function (a, b) { return a.from - b.from; }) : [];
  }
  function bodyOf(md) { var t = md.replace(/\n+$/, ""), i = t.lastIndexOf("\n"), l = i >= 0 ? t.slice(i + 1) : t; return l.indexOf("<!-- scriobh:alts ") === 0 ? md.slice(0, md.lastIndexOf(l)) : md; }
  // The places rangesOf gives are in the document the stored Markdown parses to, which has lost the spaces a paragraph starts and
  // ends with (CommonMark drops them) where the editor's own document keeps them: every later place would be short by one for each
  // such block. This carries the places over block by block, so they are the editor's own.
  function toLive(rs) {
    var blocks = [], cum = 0;
    view.state.doc.descendants(function (n, pos) {
      if (!n.isTextblock) return true;
      var l = 0, tr = 0;
      if (!n.type.spec.code) {
        var tx = n.textBetween(0, n.content.size, "", "\ufffc");
        l = (/^[ \t]+/.exec(tx) || [""])[0].length; tr = l === tx.length ? 0 : (/[ \t]+$/.exec(tx) || [""])[0].length;
      }
      blocks.push({ pcs: pos + 1 - cum, add: cum + l }); cum += l + tr;
      return false;
    });
    function at(p) {
      var lo = 0, hi = blocks.length - 1, k = -1;
      while (lo <= hi) { var mid = (lo + hi) >> 1; if (blocks[mid].pcs <= p) { k = mid; lo = mid + 1; } else hi = mid - 1; }
      return k < 0 ? p : p + blocks[k].add;
    }
    return rs.map(function (r) { return r && { from: at(r.from), to: at(r.to) }; });
  }
  // Where the marks are in the editor's document, from where the server found them in the stored text. Only while the editor
  // holds exactly what is stored (nothing unsaved, the text as it was when the question went out).
  function draw(list, doc0, rev0) {
    if (view.state.doc !== doc0 || W.rev !== rev0 || W.unsaved()) { later(900); return false; }
    marks = list;
    var open = list.filter(function (m) { return m.status === "open" && m.anchor && m.anchor.state === "placed"; });
    var rs = open.length ? toLive(ed.alts.rangesOf(bodyOf(W.serverMd), open.map(function (m) { return { start: m.anchor.start, end: m.anchor.end }; }))) : [];
    var decos = [];
    open.forEach(function (m, i) {
      if (!rs[i]) return;
      decos.push(win.scriobhEditor.Decoration.inline(rs[i].from, rs[i].to, { class: "lab-mark lab-" + m.kind, "data-lab": m.id, title: (KIND[m.kind] || m.kind) + ": " + (m.reason || HOW[m.kind] || "") }, { id: m.id }));
    });
    set = win.scriobhEditor.DecorationSet.create(view.state.doc, decos); setDoc = view.state.doc;
    S.decorate(ed, "lab", decorFn);
    return true;
  }
  function later(ms) { win.clearTimeout(timer); timer = win.setTimeout(refresh, ms); }
  function refresh() {
    win.clearTimeout(timer);
    var d0 = view.state.doc, r0 = W.rev;
    if (W.unsaved()) { later(1500); return Promise.resolve(); }
    return call("GET", base + "/ai/lab/marks?document_id=" + encodeURIComponent(DOC) + "&status=open").then(function (r) {
      if (!r.ok) { if (root) root.querySelector(".lab-list").replaceChildren(h("p", { class: "ai-bad", text: why(r) })); return; }
      truncated = !!r.json.truncated;
      if (draw(r.json.marks || [], d0, r0) && root) list();
    });
  }
  doc.addEventListener("scriobh:saved", function () { if (marks.length || shown) later(1200); });
  view.dom.addEventListener("click", function (e) {
    var el = e.target.closest && e.target.closest(".lab-mark");
    if (!el || !root || !(S.inspector && S.inspector.current === "lab")) return; // a tap in the text never opens a panel over it
    var item = root.querySelector('[data-mark="' + el.dataset.lab + '"]'); if (item) { item.scrollIntoView({ block: "nearest" }); item.classList.add("is-current"); win.setTimeout(function () { item.classList.remove("is-current"); }, 1200); }
  });
  function select(m) {
    var p = live().filter(function (x) { return x.id === m.id; })[0];
    if (!p) { note("That mark is not in the text as it is now. Check the chapter again."); return; }
    view.dispatch(view.state.tr.setSelection(view.state.selection.constructor.create(view.state.doc, p.from, p.to)).scrollIntoView()); view.focus();
    say((KIND[m.kind] || m.kind) + ". " + (m.reason || HOW[m.kind] || ""));
  }

  // ---- doing things ------------------------------------------------------------------------------------------------------------
  function analyse(btn, out) {
    btn.disabled = true; out.textContent = "Checking…"; out.removeAttribute("data-ok");
    W.flushAll(true).then(function (ok) {
      if (!ok) { btn.disabled = false; out.textContent = "Your text is not saved yet. Try again in a moment."; return; }
      return call("POST", base + "/ai/lab/analyse", { document_id: DOC }).then(function (r) {
        btn.disabled = false;
        if (!r.ok) { out.textContent = why(r); out.setAttribute("data-ok", "false"); return; }
        var c = r.json.counts || {}, ks = Object.keys(c).filter(function (k) { return c[k]; }), total = ks.reduce(function (a, k) { return a + c[k]; }, 0);
        out.textContent = total ? total + (total === 1 ? " mark: " : " marks: ") + ks.map(function (k) { var pl = PLURAL[k] || [k, k]; return c[k] + " " + (c[k] === 1 ? pl[0] : pl[1]); }).join(", ") + "." : "Nothing to mark.";
        if (ks.some(function (k) { return c[k] >= 300; })) out.textContent += " The check stops at 300 marks of one kind: settle some, then check again for the rest.";
        out.setAttribute("data-ok", "true"); say(out.textContent);
        return refresh();
      });
    });
  }
  function model(recipe, params, out, back) {
    if (!aiOn) return;
    core(function () {
      S.ai.saved().then(function (ok) {
        if (!ok) return;
        S.ai.ask({ recipe: recipe, document_id: DOC, params: params || {}, focusBack: back }).then(function (res) {
          if (!res) { say("Cancelled. Nothing was sent."); return; }
          if (!res.ok) { out.textContent = res.message || "It did not go through."; out.setAttribute("data-ok", "false"); return; }
          out.textContent = res.item.status === "ok" ? "The marks are in the text and the list." : (res.item.error_message || "The model gave nothing that could be used.");
          out.setAttribute("data-ok", res.item.status === "ok" ? "true" : "false");
          refresh();
        });
      });
    }, function () { out.textContent = "The writing partner could not be loaded. Your text is unchanged."; out.setAttribute("data-ok", "false"); });
  }
  function accept(m, out) {
    if (!S.alts) return;
    S.alts.run(function (rev, tab) { return W.api("POST", base + "/ai/lab/accept", { document_id: DOC, mark_id: m.id, base_revision: rev, tab_id: tab }); }).then(function (r) {
      if (r && r.ok) { say(m.action === "none" ? "Marked as dealt with." : "Done. The change is one step in the chapter's versions."); refresh(); } else out.textContent = (r && r.message) || "It could not be done.";
    });
  }
  function dismiss(m, out) { call("POST", base + "/ai/lab/dismiss", { document_id: DOC, mark_ids: [m.id] }).then(function (r) { if (r.ok) refresh(); else out.textContent = why(r); }); }
  // A chosen set of marks as one change, after a preview of the whole result. The server takes at most 300 in one change: more
  // than that are taken in document order, the rest stay open, and the dialog says so.
  function acceptAll(ids0) {
    var order = live().map(function (x) { return x.id; }), ids = ids0.slice().sort(function (a, b) { var x = order.indexOf(a), y = order.indexOf(b); return (x < 0 ? 1e9 : x) - (y < 0 ? 1e9 : y); }), left = 0;
    if (ids.length > MAXSET) { left = ids.length - MAXSET; ids = ids.slice(0, MAXSET); }
    W.flushAll(true).then(function (ok) {
      if (!ok) { note("Your text is not saved yet. Try again in a moment."); return; }
      call("POST", base + "/ai/lab/preview", { document_id: DOC, mark_ids: ids, base_revision: W.rev }).then(function (r) {
        if (!r.ok) { note(why(r)); return; }
        var p = r.json, dlg = h("dialog", { class: "ai-dlg", "aria-labelledby": "lab-h" });
        dlg.appendChild(h("h2", { id: "lab-h", tabindex: "-1", text: "Accept " + p.applied.length + (p.applied.length === 1 ? " mark" : " marks") + " as one change" }));
        dlg.appendChild(h("p", { class: "fine", text: p.added_words + " words in, " + p.removed_words + " out. It is one step in the chapter's versions and earns no word credit. Nothing has changed yet." }));
        if (left) dlg.appendChild(h("p", { class: "ai-bad", text: "One change takes at most " + MAXSET + " marks. This takes the first " + MAXSET + " in the chapter; the other " + left + " stay open for another round." }));
        var diff = h("div", { class: "ai-diff", role: "group", "aria-label": "The whole change, word by word. Struck words come out, underlined words go in." });
        (p.diff || []).forEach(function (g) { diff.appendChild(g.op === "delete" ? h("del", { text: g.text }) : g.op === "insert" ? h("ins", { text: g.text }) : h("span", { text: g.text })); });
        dlg.appendChild(diff);
        if (p.diff_note) dlg.appendChild(h("p", { class: "fine", text: p.diff_note }));
        if ((p.skipped || []).length) dlg.appendChild(h("p", { class: "fine", text: p.skipped.length + " left out: " + p.skipped.map(function (s) { return ({ stale: "its text changed", overlap: "it overlaps another", hidden: "it is in a private note or ghosted text", not_open: "it is already settled" })[s.reason] || s.reason; }).join("; ") + "." }));
        var go = h("button", { type: "button", class: "btn solid", text: "Apply as one change" }), no = h("button", { type: "button", class: "btn", text: "Cancel" });
        function close() { try { dlg.close(); } catch (e) { /* closed */ } dlg.remove(); (ctl && ctl.all && ctl.all.isConnected ? ctl.all : view).focus(); }
        no.addEventListener("click", close);
        dlg.addEventListener("cancel", function (e) { e.preventDefault(); close(); });
        go.addEventListener("click", function () {
          go.disabled = true;
          S.alts.run(function (rev, tab) { return W.api("POST", base + "/ai/lab/accept_all", { document_id: DOC, mark_ids: p.applied.concat(p.resolved || []), base_revision: rev, tab_id: tab }); }).then(function (r2) { close(); if (r2 && r2.ok) { say("Done: one change." + (left ? " " + left + " marks are still open." : "")); refresh(); } else note((r2 && r2.message) || "It could not be applied."); });
        });
        dlg.appendChild(h("div", { class: "ai-dlg-foot" }, go, no));
        doc.body.appendChild(dlg); dlg.showModal(); dlg.querySelector("h2").focus();
        if (!p.applied.length) go.disabled = true;
      });
    });
  }

  // ---- the panel -------------------------------------------------------------------------------------------------------------------
  function list() {
    var lst = root.querySelector(".lab-list"), open = marks.filter(function (m) { return m.status === "open" && (!filter || m.kind === filter); });
    var counts = {}; marks.forEach(function (m) { if (m.status === "open") counts[m.kind] = (counts[m.kind] || 0) + 1; });
    var sel = root.querySelector(".lab-filter"); sel.replaceChildren(h("option", { value: "", text: "All kinds (" + marks.filter(function (m) { return m.status === "open"; }).length + ")" })); Object.keys(counts).forEach(function (k) { var o = h("option", { value: k, text: (KIND[k] || k) + " (" + counts[k] + ")" }); if (filter === k) o.selected = true; sel.appendChild(o); });
    lst.replaceChildren();
    if (truncated) lst.appendChild(h("p", { class: "ai-bad", text: "The list stops at the first 1,000 marks. Settle some, then check the chapter again for the rest." }));
    if (!open.length) { lst.appendChild(h("p", { class: "fine", text: marks.length ? "No open marks of that kind." : "No marks yet. Check the chapter." })); root.querySelector(".lab-all").hidden = true; return; }
    var withFix = open.filter(function (m) { return m.action !== "none" && m.anchor && m.anchor.state === "placed"; });
    var all = root.querySelector(".lab-all"); all.hidden = !withFix.length; all.textContent = "Accept all " + withFix.length + " that have a fix…"; all.onclick = function () { acceptAll(withFix.map(function (m) { return m.id; })); };
    var ul = h("ul", { class: "ai-list" });
    open.forEach(function (m) {
      var out = ctl.live, acts = h("div", { class: "ai-row" }), ok = m.anchor && m.anchor.state === "placed";
      var show = h("button", { type: "button", class: "btn small", text: "Show" });
      if (ok) { show.addEventListener("click", function () { select(m); }); acts.appendChild(show); }
      var li = h("li", { class: "ai-rv", "data-mark": m.id, "data-kind": m.kind },
        h("div", { class: "ai-tags" }, h("span", { class: "insp-tag lab-tag lab-" + m.kind }, h("span", { class: "lab-g", "aria-hidden": "true", text: GLYPH[m.kind] || "" }), KIND[m.kind] || m.kind), m.source === "model" ? h("span", { class: "insp-tag", text: "Model" }) : null, ok ? null : h("span", { class: "insp-tag", text: m.anchor && m.anchor.state === "stale" ? "text changed" : "not found" })),
        h("blockquote", { class: "ai-quote" }, plain(m.quote) ? h("span", { class: "ai-gap", text: quoteText(m) }) : h("q", { text: m.quote })), m.reason ? h("p", { class: "fine", text: m.reason }) : null,
        h("p", { class: "fine", text: fixText(m) }),
        acts);
      if (ok && m.action !== "none") { var a = h("button", { type: "button", class: "btn small", text: "Accept" }); a.addEventListener("click", function () { accept(m, out); }); acts.appendChild(a); }
      if (ok && m.action === "none") { var r = h("button", { type: "button", class: "btn small", text: "Resolve" }); r.addEventListener("click", function () { accept(m, out); }); acts.appendChild(r); }
      var d = h("button", { type: "button", class: "btn small quiet", text: "Dismiss" }); d.addEventListener("click", function () { dismiss(m, out); }); acts.appendChild(d);
      ul.appendChild(li);
    });
    lst.appendChild(ul);
  }
  function build() {
    var out = h("p", { class: "ai-result", role: "status" });
    var check = h("button", { type: "button", class: "btn", text: "Check this chapter" });
    check.addEventListener("click", function () { analyse(check, out); });
    ctl = { check: check, out: out };
    var r = h("div", { class: "lab" });
    r.appendChild(h("h2", { class: "ver-h", text: "Lab" }));
    r.appendChild(h("p", { class: "fine", text: "The Lab marks the text and never rewrites it. These checks run here, with no model and no cost: long sentences, words that hedge, double spaces, repeated marks, spacing before punctuation, straight against curly quotes." }));
    r.appendChild(h("div", { class: "ai-row" }, check)); r.appendChild(out);
    if (aiOn) {
      var mo = h("p", { class: "ai-result", role: "status" }), trim = h("select", { id: "lab-trim", "aria-label": "What to trim" }, [["filler", "Filler words"], ["adverbs", "Adverbs"], ["repetition", "Repetition"], ["dialogue_tags", "Dialogue tags"]].map(function (x) { return h("option", { value: x[0], text: x[1] }); }));
      r.appendChild(h("h2", { class: "ver-h", text: "Checks that use a model" }));
      r.appendChild(h("p", { class: "fine", text: "You see exactly what is sent before it is. The marks come back as marks: nothing is rewritten." }));
      var b1 = h("button", { type: "button", class: "btn small", text: "Mark tangled sentences" }), b2 = h("button", { type: "button", class: "btn small", text: "Mark tone that does not fit" }), b3 = h("button", { type: "button", class: "btn small", text: "Mark words to trim" });
      b1.addEventListener("click", function () { model("lab.convoluted", {}, mo, b1); }); b2.addEventListener("click", function () { model("lab.tone", {}, mo, b2); }); b3.addEventListener("click", function () { model("lab.trim", { mode: trim.value }, mo, b3); });
      r.appendChild(h("div", { class: "ai-row" }, b1, b2)); r.appendChild(h("div", { class: "ai-row" }, trim, b3)); r.appendChild(mo);
    }
    var vis = h("input", { type: "checkbox", id: "lab-vis" }); vis.checked = true;
    vis.addEventListener("change", function () { visible = vis.checked; S.decorate(ed, "lab", decorFn); });
    r.appendChild(h("h2", { class: "ver-h", text: "Marks" }));
    r.appendChild(h("div", { class: "ai-checks" }, h("label", {}, vis, h("span", { text: "Show the marks in the text" }))));
    var f = h("select", { class: "lab-filter", id: "lab-filter", "aria-label": "Which kind of mark" }); f.addEventListener("change", function () { filter = f.value; list(); });
    var all = h("button", { type: "button", class: "btn small solid lab-all", text: "Accept all" }); all.hidden = true; ctl.all = all;
    r.appendChild(h("div", { class: "ai-row" }, f, all));
    ctl.live = h("p", { class: "ai-result lab-live", role: "status" });
    r.appendChild(ctl.live);
    r.appendChild(h("div", { class: "lab-list" }));
    return r;
  }
  S.lab = {
    show: function (el) {
      if (!root) root = build(); el.replaceChildren(root); shown = true; list(); refresh();
      if (S.labWant === "check") { S.labWant = ""; analyse(ctl.check, ctl.out); }
    },
    // The next (1) or previous (-1) mark from the caret, selected and read out.
    step: function (d) {
      var placed = live();
      if (!placed.length) { if (S.inspector) S.inspector.open("lab", true); return; }
      var at = view.state.selection.from, pick = null, i;
      if (d > 0) { for (i = 0; i < placed.length; i++) if (placed[i].from > at) { pick = placed[i]; break; } pick = pick || placed[0]; }
      else { for (i = placed.length - 1; i >= 0; i--) if (placed[i].to < at) { pick = placed[i]; break; } pick = pick || placed[placed.length - 1]; }
      var m = marks.filter(function (x) { return x.id === pick.id; })[0]; if (m) select(m);
    }
  };
})();
