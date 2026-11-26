/* Scríobh writing view: reviews, in the AI tab. A review is what a critique, a continuity check or an extraction left for the
   person to work through: notes that point at the text (quote, kind, how much it matters), each of which can be shown in the
   text, resolved or dismissed, none of which changes the chapter. An extraction's proposed entries are accepted into the
   bible one at a time, after a dry run that says what would be made. A chapter summary is a proposal for the synopsis. Reviews
   are private to the person who ran them. Everything here is read and changed through the ai.* ops. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, A = S && S.ai;
  if (!W || !A) return;
  var h = A.h, P = W.cfg.project, DOC = W.cfg.id, base = "/api/v1/projects/" + P, root = null, openId = "", focusNext = false;
  var RECIPE = { critique: "critique.chapter", continuity: "continuity.chapter", extract: "extract.chapter", summary: "summary.chapter" };
  var KIND = { critique: "Critique", continuity: "Continuity check", extraction: "Proposed entries" };
  function when(t) { return (t || "").replace("T", " ").slice(0, 16); }

  // ---- running a review ----------------------------------------------------------------------------------------------------
  function panelEl() { return doc.getElementById("ipanel-ai"); }
  // `back` is the button the person pressed (inside the panel): focus returns to it, and the finished review may take focus only
  // if focus is still in the panel (or on the page body) when it is done. A run started from the text or the palette never moves it.
  function start(name, back) {
    var rid = RECIPE[name];
    A.saved().then(function (ok) {
      if (!ok) return;
      A.ask({ recipe: rid, document_id: DOC, focusBack: back }).then(function (res) {
        if (!res) { A.say("Cancelled. Nothing was sent."); return; }
        if (!res.ok) { A.note(res.message || "It did not go through."); return; }
        var it = res.item;
        if (it.status !== "ok") { A.note(it.error_message || "The model gave nothing that could be used."); return; }
        if (name === "summary") { summaryCard(res); return; }
        var a = doc.activeElement, mine = !a || a === doc.body || (panelEl() && panelEl().contains(a));
        S.aiWant = "reviews"; if (S.inspector) S.inspector.open("ai", false);
        doc.dispatchEvent(new win.CustomEvent("scriobh:aitab", { detail: "reviews" }));
        focusNext = mine;
        win.setTimeout(function () { if (root) { openId = ""; list(true); } }, 150);
        A.say((name === "critique" ? "The critique" : name === "continuity" ? "The continuity check" : "The proposed entries") + " is in the Reviews list. Nothing in the chapter changed.");
      });
    });
  }
  function summaryCard(res) {
    var it = res.item, d = it.data, s = d && d.suggestion;
    if (!s) { A.note("The model gave nothing that could be used as a summary."); return; }
    var pop = h("section", { class: "ai-pop", id: "ai-pop", role: "dialog", "aria-labelledby": "ais-h", tabindex: "-1" });
    var apply = h("select", { id: "ais-apply", "aria-label": "Which synopsis to write" }, h("option", { value: "both", text: "Both the short and the long synopsis" }), h("option", { value: "short", text: "The short synopsis only" }), h("option", { value: "long", text: "The long synopsis only" }));
    if (!s.short) apply.value = "long"; else if (!s.long) apply.value = "short";
    var msg = h("p", { class: "ai-bad", role: "alert" }); msg.hidden = true;
    var cur = d.current || {};
    pop.appendChild(h("h2", { id: "ais-h", text: "Proposed synopsis" }));
    pop.appendChild(h("p", { class: "fine", text: A.metaLine(it) }));
    function pair(label, now, next) { return next ? h("div", { class: "ai-pair" }, h("h3", { text: label + " synopsis" }), h("p", { class: "ai-k", text: "Now" }), h("p", { class: "ai-prose", text: now || "(empty)" }), h("p", { class: "ai-k", text: "Proposed" }), h("p", { class: "ai-prose ai-new", text: next })) : null; }
    [pair("Short", cur.short, s.short), pair("Long", cur.long, s.long)].forEach(function (n) { if (n) pop.appendChild(n); });
    pop.appendChild(h("div", { class: "field" }, h("label", { for: "ais-apply", text: "Write" }), apply));
    pop.appendChild(msg);
    var okB = A.btn("Accept", function () {
      okB.disabled = true;
      // Held and flushed like every other write to the chapter, but the synopsis is not in the body: the page only takes the new
      // revision (as after a save), and the editor's text and its undo history are left alone. Only if the stored body is not what
      // the page holds is the stored text taken whole.
      S.alts.exclusive(function () {
        return W.flushAll().then(function (saved) {
          if (!saved) { A.note("Your text is not saved yet, so nothing was done. Try again in a moment."); return { ok: false, message: "Your text is not saved yet." }; }
          var ver = W.editor.alts.version();
          return A.call("POST", base + "/ai/summary/accept", { run_id: it.run_id, apply: apply.value, base_revision: W.rev, expect_hash: s.text_hash, tab_id: W.tab }).then(function (r) {
            if (!r.ok) return { ok: false, message: A.why(r) };
            return W.api("GET", "/api/v1/docs/" + DOC).then(function (g) {
              var d0 = g.ok && g.json && g.json.item, words = parseInt(doc.getElementById("write").dataset.words, 10) || 0;
              if (!d0) W.afterAlt({ revision: r.json.revision, word_count: words }, W.serverMd, ver);
              else if (d0.content === W.serverMd) W.afterAlt({ revision: d0.revision, word_count: d0.word_count }, W.serverMd, ver);
              else { W.editor.setMarkdown(d0.content, { force: true }); W.afterAlt({ revision: d0.revision, word_count: d0.word_count }, d0.content, ver); }
              return { ok: true, res: r.json };
            });
          });
        });
      }).then(function (r) {
        okB.disabled = false;
        if (!r || !r.ok) { msg.textContent = (r && r.message) || "It could not be accepted."; msg.hidden = false; if (doc.activeElement === doc.body) okB.focus(); return; }
        var wrote = (r.res && r.res.wrote) || {};
        A.closeCard(); A.note("The synopsis was written."); doc.dispatchEvent(new win.CustomEvent("scriobh:synopsis"));
        var f = doc.querySelector('#insp-form [name="synopsis_short"]'); if (f && wrote.short) f.value = s.short || f.value;
        var g2 = doc.querySelector('#insp-form [name="synopsis_long"]'); if (g2 && wrote.long) g2.value = s.long || g2.value;
      });
    }, "solid");
    var no = A.btn("Reject", function () { A.call("POST", base + "/ai/accept/reject", { run_id: it.run_id }); A.closeCard(); A.say("Rejected. Nothing changed."); });
    if (!A.touch) pop.appendChild(h("p", { class: "fine", text: "From the text, Alt+Shift+J moves here." }));
    pop.appendChild(h("div", { class: "ai-dlg-foot" }, okB, no));
    pop.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); if (!e.repeat) no.click(); } });
    A.slot(pop); // it does not take focus: the writer may be typing; Alt+Shift+J moves to it
    A.say("A proposed synopsis is ready. " + (A.touch ? "It is at the bottom of the screen." : "Alt+Shift+J moves to it."));
  }

  // ---- the list --------------------------------------------------------------------------------------------------------------
  function list(openNewest) {
    var box = root.querySelector(".ai-rv-body");
    A.call("GET", base + "/ai/reviews?document_id=" + encodeURIComponent(DOC)).then(function (r) {
      if (!r.ok) { box.replaceChildren(h("p", { class: "ai-bad", text: A.why(r) })); return; }
      var items = (r.json && r.json.items) || [];
      if (openNewest && items.length) { open(items[0].review_id); return; }
      box.replaceChildren();
      box.appendChild(h("div", { class: "ai-row" }, [["Critique this chapter", "critique"], ["Check continuity", "continuity"], ["Find names not in the Compendium", "extract"], ["Summarise", "summary"]].map(function (x) { return A.btn(x[0], function (e) { start(x[1], e.currentTarget); }, "small"); })));
      box.appendChild(h("p", { class: "fine", text: "Your reviews of this chapter. They are private to you and change nothing in the text." }));
      if (!items.length) { box.appendChild(h("p", { class: "fine", text: "No reviews yet." })); return; }
      var ul = h("ul", { class: "ai-list" });
      items.forEach(function (it) {
        var c = it.counts || {}, b = h("button", { type: "button", class: "btn small", text: "Open" });
        b.addEventListener("click", function () { open(it.review_id); });
        ul.appendChild(h("li", { class: "ai-rv" }, h("strong", { text: KIND[it.review_kind] || it.review_kind }), " ", h("span", { class: "fine", text: when(it.created_at) + " · " + c.open + " open, " + c.accepted + " dealt with, " + c.dismissed + " dismissed" }), " ", b));
      });
      box.appendChild(ul);
    });
  }

  // ---- one review ---------------------------------------------------------------------------------------------------------------
  function find(quote) {
    var v = W.editor.view, hit = null;
    v.state.doc.descendants(function (n, p) { if (hit || !n.isText) return; var i = n.text.indexOf(quote); if (i >= 0) hit = [p + i, p + i + quote.length]; });
    return hit;
  }
  function show(note) {
    var v = W.editor.view, at = null;
    if (note.quote) at = find(note.quote) || find(note.quote.replace(/[*_`]/g, ""));
    if (!at) { A.note("That passage could not be found in the text as it is now."); return; }
    v.dispatch(v.state.tr.setSelection(v.state.selection.constructor.create(v.state.doc, at[0], at[1])).scrollIntoView()); v.focus();
  }
  function open(id) {
    openId = id;
    var box = root.querySelector(".ai-rv-body");
    box.replaceChildren(h("p", { class: "fine", text: "Loading…" }));
    Promise.all([A.call("GET", base + "/ai/review?review_id=" + encodeURIComponent(id)), A.call("GET", base + "/compendium")]).then(function (rs) {
      var r = rs[0];
      if (!r.ok) { box.replaceChildren(h("p", { class: "ai-bad", text: A.why(r) }), A.btn("Back to the list", function () { list(); })); return; }
      var rv = r.json.item, kind = rv.review_kind, comp = rs[1].json && rs[1].json.item && rs[1].json.item.primary;
      box.replaceChildren();
      var del = A.btn("Delete this review", function () {}, "danger small");
      A.guard(del, "Really delete it?", function () { A.call("DELETE", base + "/ai/review?review_id=" + encodeURIComponent(id)).then(function () { A.say("The review was deleted."); list(); }); });
      var top = h("div", { class: "ai-row ai-top" }, A.btn("Back", function () { list(); }, "small"), del);
      box.appendChild(top);
      var head = h("h3", { class: "ver-h", tabindex: "-1", text: (KIND[kind] || "Review") + " · " + when(rv.created_at) });
      box.appendChild(head);
      if (focusNext) { focusNext = false; A.focusIf(head, panelEl()); }
      var openN = (rv.notes || []).filter(function (n) { return n.status === "open"; });
      box.appendChild(h("p", { class: "fine", text: openN.length + " open of " + rv.notes.length + ". " + (rv.dropped ? rv.dropped + " the model gave could not be used. " : "") + "None of this changes the chapter." }));
      if (openN.length > 1) {
        var all = A.btn("Dismiss all " + openN.length + " open", function () {}, "small");
        A.guard(all, "Really dismiss all " + openN.length + "?", function () { Promise.all(openN.map(function (n) { return A.call("POST", base + (kind === "extraction" ? "/ai/extract/dismiss" : "/ai/review/note/dismiss"), { note_id: n.id }); })).then(function () { open(id); }); });
        box.appendChild(all);
      }
      var ul = h("ul", { class: "ai-list" });
      rv.notes.forEach(function (n) { ul.appendChild(noteItem(rv, n, kind, comp)); });
      box.appendChild(ul);
    });
  }
  function noteItem(rv, n, kind, comp) {
    var li = h("li", { class: "ai-rv", "data-status": n.status });
    var tags = h("div", { class: "ai-tags" }, h("span", { class: "insp-tag", text: n.category }), h("span", { class: "insp-tag", text: n.severity }), n.status !== "open" ? h("span", { class: "insp-tag", text: n.status === "accepted" ? "dealt with" : "dismissed" }) : null, n.anchor && n.anchor.state !== "placed" && kind !== "extraction" ? h("span", { class: "insp-tag", text: n.anchor.state === "stale" ? "text changed" : "not found" }) : null);
    li.appendChild(tags);
    if (kind === "extraction") {
      var s = (n.sheet && (n.sheet.Sheet || n.sheet)) || {};
      li.appendChild(h("p", {}, h("strong", { text: s.name || "(unnamed)" }), " (" + (s.kind || "entity") + ")"));
      if (s.summary) li.appendChild(h("p", { text: s.summary }));
      if (n.sheet && n.sheet.exists_as) li.appendChild(h("p", { class: "ai-bad", text: "The Compendium already has " + n.sheet.exists_as.name + ": accepting this would clash with it." }));
    } else {
      if (n.quote) li.appendChild(h("blockquote", { class: "ai-quote" }, h("q", { text: n.quote })));
      li.appendChild(h("p", { text: n.note }));
      if (n.example) li.appendChild(h("details", {}, h("summary", { text: "An example of the kind of fix" }), h("p", { text: n.example })));
      if ((n.sources || []).length) li.appendChild(h("p", { class: "fine", text: "Cites " + n.sources.map(function (x) { return x.kind + (x.field ? " " + x.field : ""); }).join(", ") }));
    }
    var out = h("p", { class: "ai-result", role: "status" });
    var acts = h("div", { class: "ai-row" });
    if (kind !== "extraction" && n.quote) acts.appendChild(A.btn("Show in the text", function () { show(n); }, "small"));
    if (n.status === "open") {
      if (kind === "extraction") {
        acts.appendChild(A.btn("Add to the Compendium…", function () { accept(n, comp, li, out); }, "small"));
        acts.appendChild(A.btn("Dismiss", function () { A.call("POST", base + "/ai/extract/dismiss", { note_id: n.id }).then(function () { open(openId); }); }, "small"));
      } else {
        acts.appendChild(A.btn("Resolve", function () { A.call("POST", base + "/ai/review/note/resolve", { note_id: n.id }).then(function (r) { if (r.ok) open(openId); else out.textContent = A.why(r); }); }, "small"));
        acts.appendChild(A.btn("Dismiss", function () { A.call("POST", base + "/ai/review/note/dismiss", { note_id: n.id }).then(function (r) { if (r.ok) open(openId); else out.textContent = A.why(r); }); }, "small"));
      }
    }
    li.appendChild(acts); li.appendChild(out);
    return li;
  }
  // An extraction's entry goes into the bible only after a dry run has said what would be made.
  function accept(n, comp, li, out) {
    if (!comp || !comp.compendium_id) { out.textContent = "This project has no Compendium of its own to add to. Add one on the project's Compendium page."; out.setAttribute("data-ok", "false"); return; }
    A.call("POST", base + "/ai/extract/accept", { note_id: n.id, compendium_id: comp.compendium_id }, { "X-Scriobh-Dry-Run": "1" }).then(function (r) {
      if (!r.ok) { out.textContent = A.why(r); out.setAttribute("data-ok", "false"); return; }
      var dropped = (r.json && r.json.dropped_fields) || [];
      out.setAttribute("data-ok", "true");
      out.textContent = "Dry run: this would create it in " + comp.name + (dropped.length ? ", leaving out the fields " + dropped.join(", ") + " that the Compendium does not have" : "") + ". Nothing has been made yet.";
      var go = A.btn("Create it now", function () {
        go.disabled = true;
        A.call("POST", base + "/ai/extract/accept", { note_id: n.id, compendium_id: comp.compendium_id }).then(function (r2) { go.disabled = false; if (!r2.ok) { out.textContent = A.why(r2); out.setAttribute("data-ok", "false"); return; } A.say("The entry was created in the Compendium."); open(openId); });
      }, "small solid");
      out.appendChild(doc.createTextNode(" ")); out.appendChild(go);
    });
  }

  S.aireviews = {
    start: start,
    show: function (el) {
      root = h("div", { class: "ai-reviews" }, h("div", { class: "ai-rv-body" }));
      el.replaceChildren(root);
      if (openId) open(openId); else list();
    }
  };
})();
