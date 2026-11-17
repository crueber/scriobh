/* Scríobh writing view: the writing partner's core. Loaded the first time an AI control is used on a project that has AI on.
   Three things live here and everything else (chat, reviews, the Lab) is built on them:

     approve(spec)  the context preview: a dialog that shows exactly what would be sent, where it goes and what it costs, lets the
                    person take chips out, and resolves with the request bound to that payload (its hash and nonce) or with null.
                    Nothing is sent by it: the preview op calls no provider. Cancel resolves null and nothing leaves.
     run(req)       sends an approved request (the run route streams; a stop button cancels it at the provider).
     proposals      a selection action's answer is a proposal: a word-by-word diff the writer accepts, keeps as an alternative,
                    rejects or asks again for. Accepting is the explicit op; the text the server holds is what is written.

   Text from a model is only ever put into the page as text. Nothing here writes to the chapter except the accept ops. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, box = doc.getElementById("write");
  if (!W || !box || (S.ai && S.ai.ready)) return;
  var P = W.cfg.project, DOC = W.cfg.id, ed = W.editor, view = ed.view, base = "/api/v1/projects/" + P;
  function h(tag, attrs) {
    var e = doc.createElement(tag);
    for (var k in attrs || {}) { var v = attrs[k]; if (v == null || v === false) continue; if (k === "text") e.textContent = v; else if (k === "class") e.className = v; else e.setAttribute(k, v === true ? "" : v); }
    (function add(list) { for (var i = 0; i < list.length; i++) { var c = list[i]; if (c == null || c === false) continue; if (Array.isArray(c)) add(c); else e.appendChild(typeof c === "string" ? doc.createTextNode(c) : c); } })([].slice.call(arguments, 2));
    return e;
  }
  function note(m) { if (S.alts) S.alts.note(m); else if (S.toast) S.toast(m); }
  function say(m) { if (S.alts) S.alts.say(m); }
  function call(method, path, body, headers) {
    var o = { method: method, credentials: "same-origin", headers: Object.assign({ Accept: "application/json" }, body !== undefined ? { "Content-Type": "application/json" } : {}, headers || {}) };
    if (body !== undefined) o.body = JSON.stringify(body);
    return win.fetch(path, o).then(function (r) { return r.text().then(function (t) { var j = null; try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ } return { ok: r.ok, status: r.status, json: j }; }); }, function () { return { ok: false, status: 0, json: null }; });
  }
  function why(r) { return (r.json && r.json.error && r.json.error.message) || (r.status === 0 ? "Could not reach the server." : "That did not work."); }
  function money(m, cur) { return (m / 1e6).toFixed(m && m < 10000 ? 4 : 2) + " " + (cur || ""); }
  function words(n) { return n + (n === 1 ? " word" : " words"); }

  // ---- recipes -----------------------------------------------------------------------------------------------------
  var recipeCache = null;
  function recipes() {
    if (recipeCache) return Promise.resolve(recipeCache);
    return call("GET", "/api/v1/ai/recipes").then(function (r) { if (!r.ok) return r; recipeCache = {}; ((r.json && r.json.items) || []).forEach(function (x) { recipeCache[x.id] = x; }); return recipeCache; });
  }
  var CHIP_NAMES = { selection: "The selected text", sentence: "The sentence around it", paragraph: "The paragraph around it", document: "This chapter", previous_summary: "The previous chapter's summary", previous_document: "The previous chapter", synopsis: "This chapter's synopsis", brief: "The project brief", style_guide: "The style guide", entities: "Entities named in the text", research: "Research notes", outline: "The outline", summaries: "Summaries of earlier chapters", timeline: "The timeline", instruction: "A one-off instruction" };

  // ---- waiting ------------------------------------------------------------------------------------------------------
  var waitEl = null;
  function waiting(msg, stop) {
    if (!waitEl) { waitEl = h("div", { class: "ai-wait", id: "ai-wait", role: "status" }); doc.body.appendChild(waitEl); }
    waitEl.replaceChildren(h("span", { text: msg }), stop ? h("button", { type: "button", class: "btn small", text: "Stop waiting" }) : null);
    if (stop) waitEl.lastChild.addEventListener("click", stop);
    waitEl.hidden = false;
    return function () { if (waitEl) waitEl.hidden = true; };
  }

  // ---- the selection -------------------------------------------------------------------------------------------------
  function selectionNow() {
    var s = view.state.selection;
    if (s.empty) return null;
    var src = ed.alts.sourceOf(s.from, s.to);
    if (!src) return { error: "That selection cannot be named exactly. Select a little more or less of it." };
    return { from: s.from, to: s.to, doc: view.state.doc, text: view.state.doc.textBetween(s.from, s.to, " "), sel: { quote: src.quote, before: src.before, after: src.after } };
  }
  // What the request names must be what the server holds: the page's pending save goes first, in the editor's own spelling.
  function saved() { return W.flushAll(true).then(function (ok) { if (!ok) note(win.navigator.onLine === false ? "You are offline, so nothing was asked and nothing was sent." : "Your text is not saved yet, so nothing was asked. Try again in a moment."); return ok; }); }
  var chords = (S.chords = S.chords || { clash: [] });
  function claim(code, who) { if (chords[code] && chords[code] !== who) chords.clash.push(code + ": " + chords[code] + " and " + who); chords[code] = who; }
  claim("KeyJ", "ai: go to the proposal");
  var touch = !!(win.matchMedia && win.matchMedia("(pointer: coarse)").matches);
  function keyed(label, key) { return touch ? label : label + " (" + key + ")"; }
  // A guard for a button that cannot be undone: the first press arms it (and says so); the second does it, but only once half a
  // second has passed and the key of the first press (if it was a key) has been let go, so holding Enter confirms nothing. It
  // disarms itself after five seconds, on Escape, and when focus leaves it, and it takes focus back only if focus is still on it
  // or on the page body: a writer who has moved on to type is never handed a button to press with the next space. A double-click
  // does nothing.
  function guard(b, armedText, fn) {
    var label = b.textContent, armed = false, settling = false, released = false, fresh = false, t1 = 0, t2 = 0;
    function up(e) { if (e.key === "Enter" || e.key === " ") released = true; }
    function down(e) { if (released && !e.repeat && (e.key === "Enter" || e.key === " ")) fresh = true; } // a new press, not a repeat of the held one
    function disarm() { armed = false; settling = false; win.clearTimeout(t1); win.clearTimeout(t2); doc.removeEventListener("keyup", up, true); doc.removeEventListener("keydown", down, true); b.textContent = label; b.disabled = false; b.removeAttribute("data-armed"); }
    b.addEventListener("click", function (e) {
      if (!armed) {
        armed = true; settling = true; released = e.detail !== 0; // a click made by the keyboard has no detail: wait for its key to be let go
        fresh = false; doc.addEventListener("keyup", up, true); doc.addEventListener("keydown", down, true);
        b.textContent = armedText; b.setAttribute("data-armed", "true"); b.disabled = true;
        say(armedText + " Press it again to do it.");
        t1 = win.setTimeout(function () {
          settling = false; b.disabled = false;
          var a = doc.activeElement;
          if (!a || a === doc.body || a === b || a.contains(b)) b.focus(); else disarm(); // a panel that took the focus when the button was disabled is where it was
        }, 500);
        t2 = win.setTimeout(disarm, 5000);
        return;
      }
      if (settling || !released || (e.detail === 0 && !fresh)) return; // by key: only a new press after the first was let go
      disarm(); fn();
    });
    b.addEventListener("blur", function () { if (armed && !settling) disarm(); });
    b.addEventListener("keydown", function (e) { if (e.key === "Escape" && armed) { e.preventDefault(); e.stopPropagation(); disarm(); } });
  }
  // Move focus only if it is still where the action began (inside `within`, or on the page body): a writer who has gone back to
  // the text is never pulled out of it. Otherwise the result is only announced.
  function focusIf(el, within, said) {
    var a = doc.activeElement;
    if (!a || a === doc.body || (within && within.contains(a))) { el.focus(); return true; }
    if (said) say(said);
    return false;
  }

  // ---- the context preview ----------------------------------------------------------------------------------------------
  function bodyOf(spec, st) {
    var b = { recipe: spec.recipe, params: st.params, stream: true }; // every request goes over the streaming route, so that is what is previewed
    if (spec.document_id) b.document_id = spec.document_id;
    if (spec.selection) b.selection = spec.selection;
    if (spec.turns) b.turns = spec.turns;
    if (st.kinds) b.chips = st.kinds.map(function (k) { return { kind: k, auto: k === "entities" }; });
    return b;
  }
  function approve(spec) {
    return recipes().then(function (rc) {
      if (typeof rc.ok === "boolean") { note(rc.status === 0 ? "Could not reach the server, so nothing was asked. Nothing was sent." : why(rc) + " Nothing was sent."); return null; }
      var rec = rc[spec.recipe];
      if (!rec) { note("That is not something the writing partner does."); return null; }
      return new Promise(function (resolve) {
        var st = { params: Object.assign({}, spec.params || {}), kinds: spec.chips ? spec.chips.slice() : null, pv: null, gen: 0 };
        rec.params.forEach(function (p) { if (st.params[p.name] == null && p.default) st.params[p.name] = p.default; });
        var all = rec.default_chips.slice();
        var dlg = h("dialog", { class: "ai-dlg", "aria-labelledby": "aid-h" });
        var head = h("h2", { id: "aid-h", text: spec.title || rec.name, tabindex: "-1" });
        var intro = h("p", { class: "fine", text: "This is exactly what would be sent. Nothing is sent until you press Send." });
        var form = h("div", { class: "ai-opts" });
        var statusEl = h("div", { class: "ai-status", role: "status" });
        var out = h("div", { class: "ai-pv" });
        var send = h("button", { type: "button", class: "btn solid", text: "Send", disabled: true });
        var cancel = h("button", { type: "button", class: "btn", text: "Cancel" });
        dlg.appendChild(head); dlg.appendChild(intro); dlg.appendChild(form); dlg.appendChild(statusEl); dlg.appendChild(out);
        dlg.appendChild(h("div", { class: "ai-dlg-foot" }, send, cancel));
        var closed = false;
        function close(v) { if (closed) return;  closed = true; try { dlg.close(); } catch (e) { /* not open */ } dlg.remove(); (spec.focusBack && spec.focusBack.isConnected ? spec.focusBack : view).focus(); resolve(v); }
        cancel.addEventListener("click", function () { close(null); });
        dlg.addEventListener("cancel", function (e) { e.preventDefault(); close(null); }); // Escape is Cancel: nothing is sent
        dlg.addEventListener("close", function () { if (!closed) close(null); });
        // The parameters of the recipe: what the person may say about the task.
        rec.params.forEach(function (p) {
          var id = "aip-" + p.name, el;
          if (p.enum) { el = h("select", { id: id }); p.enum.forEach(function (o) { var op = h("option", { value: o, text: o }); if (st.params[p.name] === o) op.selected = true; el.appendChild(op); }); }
          else if (p.name === "percent" || p.name === "count") { el = h("input", { id: id, type: "number", min: "1", inputmode: "numeric" }); el.value = st.params[p.name] || ""; }
          else { el = h(p.max_len > 200 ? "textarea" : "input", { id: id, type: "text", rows: "2", maxlength: String(p.max_len || 1000), autocomplete: "off" }); el.value = st.params[p.name] || ""; }
          var later = 0;
          el.addEventListener("input", function () { st.params[p.name] = el.value; win.clearTimeout(later); later = win.setTimeout(refresh, 350); });
          el.addEventListener("change", function () { st.params[p.name] = el.value; win.clearTimeout(later); refresh(); });
          form.appendChild(h("div", { class: "field" }, h("label", { for: id, text: (p.desc || p.name) + (p.required ? "" : " (optional)") }), el));
        });
        function chipRow(kind, info, skipped) {
          var on = st.kinds ? st.kinds.indexOf(kind) >= 0 : true;
          var c = h("input", { type: "checkbox", id: "aic-" + kind }); c.checked = on && !skipped;
          c.disabled = !!skipped;
          c.addEventListener("change", function () {
            if (!st.kinds) st.kinds = all.slice();
            var i = st.kinds.indexOf(kind);
            if (c.checked && i < 0) st.kinds.push(kind); else if (!c.checked && i >= 0) st.kinds.splice(i, 1);
            refresh();
          });
          var label = (info && info.label) || CHIP_NAMES[kind] || kind;
          var meta = skipped ? skipped.message : info ? info.tokens + " tokens" + (info.redacted ? ", names replaced" : "") : c.checked ? "nothing of this kind here, so nothing is sent for it" : "left out";
          return h("li", {}, h("label", { for: "aic-" + kind }, c, h("span", {}, label, h("small", { text: meta }))));
        }
        function draw(pv, err) {
          out.replaceChildren();
          st.pv = pv && !err ? pv : null;
          send.disabled = !(pv && !err && pv.decision && pv.decision.allowed && pv.payload_hash);
          statusEl.replaceChildren();
          if (err) { statusEl.appendChild(h("p", { class: "ai-bad", text: err })); return; }
          var d = pv.decision || {};
          if (!d.allowed) {
            statusEl.appendChild(h("p", { class: "ai-bad" }, h("strong", { text: "This cannot be sent. " }), d.message || "The policy does not allow it."));
            statusEl.appendChild(h("p", { class: "fine" }, h("a", { href: "/p/" + box.dataset.slug + "/ai", text: "Open this project's AI settings" }), " to change the policy, the providers or the caps."));
          }
          var at = pv.attempts || [], first = at[0];
          if (first) {
            var leaves = at.some(function (a) { return !a.local; });
            out.appendChild(h("section", { "aria-label": "Where it goes" }, h("p", { class: "ai-dest", "data-leaves": leaves ? "true" : "false" }, h("strong", { text: leaves ? "This leaves your server: " : "This stays on your machine or network: " }), at.map(function (a) { return a.provider_name + " (" + a.model + ")" + (a.local ? ", local" : ", remote"); }).join(", then ")),
              at.length > 1 ? h("p", { class: "fine", text: "If the first fails in a way another could fix, it moves on to the next. You are approving the whole list." }) : null));
          }
          var cw = pv.context_window || (first && first.context_window) || 0;
          out.appendChild(h("section", { "aria-label": "Size and cost" }, h("p", {}, h("strong", { text: "About " + pv.input_tokens.toLocaleString() + " tokens" }), cw ? " of the model's " + cw.toLocaleString() + "." : ".", " It may answer with up to " + pv.max_tokens.toLocaleString() + "."), pv.reserve_tokens && pv.reserve_tokens !== pv.input_tokens + pv.max_tokens ? h("p", { class: "fine", text: "Counted against a monthly cap: up to " + pv.reserve_tokens.toLocaleString() + " tokens (the request once for each try the providers allow, and the longest answer)." }) : null,
            h("p", {}, h("strong", { text: "Cost: " }), pv.cost.local ? "none, it is local." : pv.cost.known ? "about " + money(pv.cost.estimate_micros || 0, pv.cost.currency) + " (at most " + money(pv.cost.max_micros || 0, pv.cost.currency) + ")." : "unknown: no price is set for this model.")));
          var ul = h("ul", { class: "ai-chips" }), byKind = {}; (pv.chips || []).forEach(function (c) { byKind[c.kind] = c; });
          var skipBy = {}; (pv.skipped || []).forEach(function (x) { skipBy[x.kind] = x; });
          all.forEach(function (k) { ul.appendChild(chipRow(k, byKind[k], !byKind[k] && skipBy[k])); });
          out.appendChild(h("section", { "aria-label": "What goes with it" }, h("h3", { text: "What goes with it" }), h("p", { class: "fine", text: "Take a box off to leave that out; the preview is made again." }), ul));
          if ((pv.redaction_mapping || []).length) {
            var tb = h("tbody"); pv.redaction_mapping.forEach(function (m) { tb.appendChild(h("tr", {}, h("td", { text: m.original }), h("td", {}, h("code", { text: m.placeholder })), h("td", { text: m.kind }))); });
            out.appendChild(h("section", { "aria-label": "Names replaced" }, h("h3", { text: "Names replaced before sending" }), h("table", { class: "ai-table" }, h("thead", {}, h("tr", {}, h("th", { scope: "col", text: "Name" }), h("th", { scope: "col", text: "Sent as" }), h("th", { scope: "col", text: "Kind" }))), tb)));
          }
          if ((pv.skipped_providers || []).length) out.appendChild(h("details", {}, h("summary", { text: "Providers that cannot be used (" + pv.skipped_providers.length + ")" }), h("ul", {}, pv.skipped_providers.map(function (x) { return h("li", { text: (x.name || x.provider_id) + ": " + x.message }); }))));
          var pay = h("section", { "aria-label": "The payload" }, h("h3", { text: "The payload" }));
          ((pv.payload && pv.payload.messages) || []).forEach(function (m, i) { pay.appendChild(h("details", i === pv.payload.messages.length - 1 || m.role !== "system" ? { open: true } : {}, h("summary", { text: m.role === "system" ? "Standing instructions (from Scríobh)" : "Message" + (m.role ? " (" + m.role + ")" : "") }), h("pre", { class: "ai-pay", text: m.content }))); });
          if (at.length) pay.appendChild(h("details", {}, h("summary", { text: "The request as each provider receives it" }), at.map(function (a) { return [h("p", { class: "fine", text: a.provider_name + " (" + a.model + ")" }), h("pre", { class: "ai-pay", text: a.body })]; })));
          if (pv.payload_hash) pay.appendChild(h("p", { class: "fine" }, "Request fingerprint (keyed): ", h("code", { text: pv.payload_hash }), ". The server made it for exactly this request with its own key, and sending is bound to it: if anything changes, nothing is sent."));
          out.appendChild(pay);
          send.textContent = first ? (first.local ? "Send (stays local)" : "Send to " + first.provider_name) : "Send";
        }
        function refresh() {
          var key = JSON.stringify(bodyOf(spec, st));
          if (st.pv && key === st.key) return; // nothing has changed since the preview on the screen was made (a field losing focus is not a change)
          var gen = ++st.gen; st.loading = true; st.key = key;
          var miss = rec.params.filter(function (p) { return p.required && !String(st.params[p.name] || "").trim(); });
          if (miss.length) { st.loading = false; st.pv = null; send.disabled = true; out.replaceChildren(); statusEl.replaceChildren(h("p", { class: "fine", text: "Fill in " + miss.map(function (p) { return p.desc || p.name; }).join(" and ").replace(/\.$/, "") + " to see exactly what would be sent." })); return; }
          statusEl.replaceChildren(h("p", { class: "fine", text: "Preparing the preview…" }));
          call("POST", base + "/ai/preview", bodyOf(spec, st)).then(function (r) {
            if (gen !== st.gen || closed) return;
            st.loading = false;
            if (!r.ok) { draw(null, why(r)); return; }
            draw(r.json.item);
          });
        }
        send.addEventListener("click", function () {
          if (!st.pv || !st.pv.payload_hash) return;
          // What is on the screen must be what is sent: a preview that is older than the options is made again and looked at again.
          if (st.loading || JSON.stringify(bodyOf(spec, st)) !== st.key) { if (!st.loading) refresh(); statusEl.replaceChildren(h("p", { class: "fine", text: "The preview is being updated. Check it, then press Send again." })); return; }
          var req = bodyOf(spec, st); req.payload_hash = st.pv.payload_hash; req.nonce = st.pv.nonce;
          close({ req: req, preview: st.pv });
        });
        doc.body.appendChild(dlg);
        dlg.showModal();
        head.focus(); // the heading, not the first field: a number box would raise the keyboard on a phone
        refresh();
      });
    });
  }

  // ---- running ----------------------------------------------------------------------------------------------------------
  // Send an approved request over the streaming route. `o.onDelta(text)` is given pieces as they arrive; the answer is the run's
  // result (the same shape ai.run returns). The promise has a cancel(): it closes the connection and tells the server.
  function run(req, o) {
    o = o || {};
    var ctl = new win.AbortController(), runId = "", stopped = false;
    var p = win.fetch(base + "/ai/run/stream", { method: "POST", credentials: "same-origin", signal: ctl.signal, headers: { "Content-Type": "application/json", Accept: "text/event-stream, application/json" }, body: JSON.stringify(req) }).then(function (r) {
      var type = r.headers.get("Content-Type") || "";
      if (type.indexOf("text/event-stream") < 0) return r.text().then(function (t) { var j = null; try { j = JSON.parse(t); } catch (e) { /* not JSON */ } return r.ok && j && j.item ? { ok: true, item: j.item } : { ok: false, message: (j && j.error && j.error.message) || "The request did not go through.", status: r.status }; });
      var rd = r.body.getReader(), dec = new win.TextDecoder(), buf = "", result = null;
      function frame(f) {
        var ev = "", data = "";
        f.split("\n").forEach(function (l) { if (l.indexOf("event: ") === 0) ev = l.slice(7); else if (l.indexOf("data: ") === 0) data += l.slice(6); });
        if (!ev) return;
        var j = null; try { j = JSON.parse(data); } catch (e) { return; }
        if (ev === "start") { runId = j.run_id; if (o.onStart) o.onStart(j); }
        else if (ev === "delta" && o.onDelta) o.onDelta(j.text || "");
        else if (ev === "done") result = { ok: true, item: j.item };
        else if (ev === "error") result = { ok: false, message: (j.error && j.error.message) || "The request failed." };
      }
      function pump() {
        return rd.read().then(function (x) {
          if (x.done) { if (buf.trim()) frame(buf); return result || { ok: false, message: stopped ? "Stopped." : "The answer was cut off before it finished." }; }
          buf += dec.decode(x.value, { stream: true });
          var i; while ((i = buf.indexOf("\n\n")) >= 0) { frame(buf.slice(0, i)); buf = buf.slice(i + 2); }
          return pump();
        });
      }
      return pump();
    }, function () { return { ok: false, message: stopped ? "Stopped." : "Could not reach the server." }; }).catch(function () { return { ok: false, message: stopped ? "Stopped." : "The answer was cut off before it finished." }; });
    p.cancel = function () { stopped = true; if (runId) call("POST", base + "/ai/run/cancel", { run_id: runId }); ctl.abort(); };
    return p;
  }
  // Ask, run, and wait with a way to stop: the whole of one request from the person's side.
  function ask(spec, opts) {
    opts = opts || {};
    return approve(spec).then(function (ap) {
      if (!ap) return null;
      var pr = run(ap.req, opts), where = ap.preview.attempts && ap.preview.attempts[0], end = waiting("Waiting for " + (where ? where.provider_name : "the provider") + "…", function () { pr.cancel(); });
      return pr.then(function (res) { end(); res.req = ap.req; res.preview = ap.preview; return res; });
    });
  }

  // ---- proposals -----------------------------------------------------------------------------------------------------------
  var target = null, cardEl = null;
  // The text a proposal is about is marked while the card is open, through the page's one decoration slot (nothing else is drawn,
  // and it is gone as soon as the text under it changes).
  function decorFn(state) {
    if (!target || target.doc !== state.doc) return [];
    return [win.scriobhEditor.Decoration.inline(target.from, target.to, { class: "ai-target", title: "The text the writing partner is working on" })];
  }
  function redecorate() { S.decorate(ed, "ai", decorFn); }
  redecorate();
  function closeCard(keepSelection) {
    if (cardEl) { cardEl.remove(); cardEl = null; }
    if (target) { target = null; redecorate(); }
    var s = view.state.selection;
    if (!s.empty && !keepSelection) { try { view.dispatch(view.state.tr.setSelection(s.constructor.create(view.state.doc, Math.min(s.to, view.state.doc.content.size)))); } catch (e) { /* the selection stays */ } }
    view.focus();
  }
  // One slot for the card on the screen: a new card replaces the one there (the summary card in js/aireviews.js takes it too).
  function slot(el) { if (cardEl && cardEl !== el) cardEl.remove(); cardEl = el; doc.body.appendChild(el); return el; }
  // A key that acted on a card is swallowed until it is let go, wherever focus has gone: a held key must not go on typing.
  var swallow = "";
  doc.addEventListener("keydown", function (e) { if (swallow && e.code === swallow) { e.preventDefault(); e.stopPropagation(); } }, true);
  doc.addEventListener("keyup", function (e) { if (e.code === swallow) swallow = ""; }, true);
  function metaLine(it) { return [it.provider_name, it.model, it.local ? "local" : "remote", it.input_tokens + " in, " + it.output_tokens + " out", it.cost_known ? (it.local ? "no charge" : money(it.cost_micros, it.currency)) : "price unknown"].filter(Boolean).join(" · "); }
  function diffNode(segs) {
    var d = h("div", { class: "ai-diff", role: "group", "aria-label": "The proposed change, word by word. Struck words come out, underlined words go in." });
    (segs || []).forEach(function (g) { d.appendChild(g.op === "delete" ? h("del", { text: g.text }) : g.op === "insert" ? h("ins", { text: g.text }) : h("span", { text: g.text })); });
    return d;
  }
  function cardShell(title, it) {
    if (cardEl) cardEl.remove();
    cardEl = h("section", { class: "ai-pop", id: "ai-pop", role: "dialog", "aria-labelledby": "aip-h", tabindex: "-1" });
    cardEl.appendChild(h("h2", { id: "aip-h", text: title }));
    cardEl.appendChild(h("p", { class: "fine", text: metaLine(it) }));
    doc.body.appendChild(cardEl);
    return cardEl;
  }
  // The answer of an edit recipe (rewrite, tighten, expand, tone, grammar, describe, dialogue, continue).
  function propose(spec, res, sel) {
    var it = res.item, data = it.data, s = data && data.suggestions && data.suggestions[0];
    if (it.status !== "ok" || !s) {
      var c0 = cardShell("No proposal", it);
      c0.appendChild(h("p", { text: it.status === "cancelled" ? "It was stopped, and nothing changed." : (it.error_message || "The model did not give anything that could be used.") }));
      (it.warnings || []).forEach(function (w) { c0.appendChild(h("p", { class: "fine", text: w })); });
      if (it.text) c0.appendChild(h("details", {}, h("summary", { text: "What it answered, as text" }), h("pre", { class: "ai-pay", text: it.text })));
      c0.appendChild(h("div", { class: "ai-dlg-foot" }, btn("Close", closeCard)));
      say("There is no proposal. " + (it.error_message || "")); return;
    }
    target = sel ? { from: sel.from, to: sel.to, doc: sel.doc } : null; if (target) redecorate();
    var rec = (recipeCache || {})[spec.recipe] || {}, c = cardShell("Proposed: " + (rec.name || spec.recipe), it);
    c.appendChild(diffNode(s.diff));
    c.appendChild(h("p", { class: "fine", text: "Original " + words(s.words.original) + ", proposal " + words(s.words.text) + ". Nothing in the chapter has changed." }));
    (it.warnings || []).forEach(function (w) { c.appendChild(h("p", { class: "fine", text: w })); });
    if (it.incomplete) c.appendChild(h("p", { class: "ai-bad", text: "The answer was cut off, so this may be incomplete." }));
    var msg = h("p", { class: "ai-bad", role: "alert" }); msg.hidden = true;
    c.appendChild(h("details", {}, h("summary", { text: "The new text on its own" }), h("pre", { class: "ai-pay", text: s.text })));
    var acceptB = btn(keyed("Accept", "A"), function () {
      acceptB.disabled = true;
      S.alts.run(function (rev, tab) { return W.api("POST", base + "/ai/accept/edit", { run_id: it.run_id, suggestion_id: s.id, base_revision: rev, expect_hash: s.text_hash, tab_id: tab }); }).then(function (r) {
        acceptB.disabled = false;
        if (r && r.ok) { closeCard(); note("Accepted. The earlier text is kept in the chapter's versions."); }
        else { msg.textContent = (r && r.message) || "It could not be accepted."; msg.hidden = false; if (doc.activeElement === doc.body) acceptB.focus(); }
      });
    }, "solid");
    var altB = rec.action === "selection" && spec.recipe !== "continue" && sel ? btn(keyed("Keep as an alternative", "L"), function () {
      altB.disabled = true;
      var restore = view.state.selection.constructor.create(view.state.doc, Math.min(sel.from, view.state.doc.content.size), Math.min(sel.to, view.state.doc.content.size));
      if (sel.doc !== view.state.doc) { msg.textContent = "The chapter changed since this was asked for, so it cannot be kept as an alternative of that text. Reject it and ask again."; msg.hidden = false; altB.disabled = false; return; }
      view.dispatch(view.state.tr.setSelection(restore));
      S.alts.makeFromSelection(s.text).then(function (r) { altB.disabled = false; if (r && r.ok) { call("POST", base + "/ai/accept/reject", { run_id: it.run_id }); closeCard(); note("Kept as another wording of that text; the text is as it was and Alt+Shift+N cycles to it. It is an ordinary alternative, and the run is recorded as rejected, because the proposal itself was not applied."); } else { msg.textContent = (r && r.message) || "It could not be kept as an alternative."; msg.hidden = false; } });
    }) : null;
    var rejB = btn(keyed("Reject", "R"), function () { call("POST", base + "/ai/accept/reject", { run_id: it.run_id }); closeCard(); say("Rejected. Nothing changed."); });
    var again = btn(keyed("Try again", "T"), function () { call("POST", base + "/ai/accept/reject", { run_id: it.run_id }); closeCard(true); cmd(spec.recipe, spec); });
    c.appendChild(msg);
    if (!touch) c.appendChild(h("p", { class: "fine", text: "From the text, Alt+Shift+J moves here. Then A accepts, L keeps it as an alternative, R rejects, T tries again." }));
    c.appendChild(h("div", { class: "ai-dlg-foot" }, acceptB, altB, rejB, again));
    c.addEventListener("keydown", function (e) {
      if (e.ctrlKey || e.metaKey || e.altKey || /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName)) return;
      if (e.repeat) { e.preventDefault(); return; } // a held key is one press, and never a second action
      var k = e.key.toLowerCase();
      if (/^(a|l|r|t|escape)$/.test(k)) swallow = e.code;
      if (k === "a") { e.preventDefault(); acceptB.click(); } else if (k === "l" && altB) { e.preventDefault(); altB.click(); } else if (k === "r" || k === "escape") { e.preventDefault(); rejB.click(); } else if (k === "t") { e.preventDefault(); again.click(); }
    });
    // The card does not take focus: the writer may be typing, and a key meant for the text must reach the text. The keys above
    // act only once focus is in the card, which is by a click, by Tab or by the chord that moves it there.
    say("A proposal is ready. " + (touch ? "It is at the bottom of the screen." : "Alt+Shift+J moves to it."));
  }
  function btn(label, fn, cls) { var b = h("button", { type: "button", class: "btn" + (cls ? " " + cls : ""), text: label }); b.addEventListener("click", fn); return b; }
  // The answer of the alternatives recipe: candidates to add to the selection's alternative.
  function candidates(spec, res, sel) {
    var it = res.item, data = it.data;
    if (it.status !== "ok" || !data || !(data.candidates || []).length) {
      var c0 = cardShell("No alternatives", it);
      c0.appendChild(h("p", { text: it.status === "cancelled" ? "It was stopped, and nothing changed." : (it.error_message || "The model did not give anything that could be used.") }));
      c0.appendChild(h("div", { class: "ai-dlg-foot" }, btn("Close", closeCard)));
      say("There are no alternatives. " + (it.error_message || "")); return;
    }
    target = sel ? { from: sel.from, to: sel.to, doc: sel.doc } : null; if (target) redecorate();
    var c = cardShell("Alternative wordings", it);
    c.appendChild(h("p", { class: "fine" }, "Original: ", h("q", { text: data.original })));
    var list = h("ul", { class: "ai-chips" }), boxes = [];
    data.candidates.forEach(function (cd, i) {
      var cb = h("input", { type: "checkbox", id: "aca-" + i }); cb.checked = true; boxes.push([cb, cd]);
      list.appendChild(h("li", {}, h("label", { for: "aca-" + i }, cb, h("span", { text: cd.text }))));
    });
    c.appendChild(list);
    var msg = h("p", { class: "ai-bad", role: "alert" }); msg.hidden = true;
    function add(choose) {
      var ids = boxes.filter(function (b) { return b[0].checked; }).map(function (b) { return b[1].id; });
      if (!ids.length) { msg.textContent = "Tick at least one."; msg.hidden = false; return; }
      S.alts.run(function (rev, tab) { return W.api("POST", base + "/ai/accept/alternative", { run_id: it.run_id, candidate_ids: ids, choose: choose || undefined, base_revision: rev, tab_id: tab }); }).then(function (r) {
        if (r && r.ok) { closeCard(); note(ids.length + (ids.length === 1 ? " wording was" : " wordings were") + " added as alternatives" + (choose ? " and one is in the text now" : "; the text is as it was") + "."); }
        else { msg.textContent = (r && r.message) || "They could not be added."; msg.hidden = false; }
      });
    }
    var addB = btn("Add the ticked wordings as alternatives", function () { add(""); }, "solid");
    var rejB = btn("Reject all", function () { call("POST", base + "/ai/accept/reject", { run_id: it.run_id }); closeCard(); say("Rejected. Nothing changed."); });
    var again = btn("Try again", function () { call("POST", base + "/ai/accept/reject", { run_id: it.run_id }); closeCard(true); cmd("alternatives", spec); });
    c.appendChild(msg); c.appendChild(h("div", { class: "ai-dlg-foot" }, addB, rejB, again));
    c.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.preventDefault(); if (!e.repeat) { swallow = e.code; rejB.click(); } } });
    say("Alternative wordings are ready. " + (touch ? "They are at the bottom of the screen." : "Alt+Shift+J moves to them."));
  }
  function toCard() { var c = doc.getElementById("ai-pop"); if (c) { c.focus(); return true; } note("There is nothing waiting."); return false; }
  doc.addEventListener("keydown", function (e) {
    if (e.altKey && e.shiftKey && !e.ctrlKey && !e.metaKey && e.code === "KeyJ" && !e.isComposing && doc.getElementById("ai-pop")) { e.preventDefault(); toCard(); }
  }, true);

  // ---- the menu and the commands ----------------------------------------------------------------------------------------------
  var ACTIONS = [
    ["rewrite", "Rewrite…", "Your instruction, the passage reworked.", true],
    ["tighten", "Tighten", "Cut about a fifth of the words.", true],
    ["expand", "Expand", "Add concrete detail.", true],
    ["simplify", "Simplify", "Plainer words and shorter sentences.", true],
    ["tone", "Change the tone…", "Warmer, drier, more formal.", true],
    ["grammar", "Fix grammar and punctuation", "Nothing else changes.", true],
    ["alternatives", "Give me alternatives", "Other wordings to keep beside it.", true],
    ["chat", "Chat about this chapter…", "Ask, think aloud, argue.", false],
    ["critique", "Critique this chapter", "Notes, not prose, kept as a review.", false],
    ["summary", "Summarise into the synopsis", "A proposal you accept.", false],
    ["continuity", "Check continuity", "Against the bible, the timeline and earlier chapters.", false],
    ["extract", "Find names not in the bible", "Proposed entries; each accepted on its own.", false],
    ["reviews", "Show the reviews", "Your critiques, checks and proposed entries.", false]
  ];
  var RECIPE_OF = { rewrite: "rewrite", tighten: "tighten", expand: "expand", simplify: "simplify", tone: "tone", grammar: "grammar", alternatives: "alternatives" };
  function menu() {
    var sel = !view.state.selection.empty, dlg = h("dialog", { class: "ai-dlg ai-menu", "aria-labelledby": "aim-h" }), list = h("ul", { class: "ai-chips" });
    dlg.appendChild(h("h2", { id: "aim-h", text: "The writing partner" }));
    dlg.appendChild(h("p", { class: "fine", text: sel ? "Choose what to ask. You will see exactly what is sent before anything is." : "Select some text for the actions on a passage. The ones on the chapter work without a selection." }));
    ACTIONS.forEach(function (a) {
      var b = h("button", { type: "button", class: "btn ai-act", text: a[1] }); b.disabled = a[3] && !sel;
      b.addEventListener("click", function () { dlg.close(); dlg.remove(); cmd(a[0]); });
      list.appendChild(h("li", {}, b, h("small", { text: a[3] && !sel ? "Select some text first. " + a[2] : a[2] })));
    });
    dlg.appendChild(list);
    var x = btn("Close", function () { dlg.close(); dlg.remove(); view.focus(); });
    dlg.appendChild(h("div", { class: "ai-dlg-foot" }, x));
    dlg.addEventListener("close", function () { dlg.remove(); });
    doc.body.appendChild(dlg); dlg.showModal();
    (list.querySelector("button:not([disabled])") || x).focus();
  }
  function part(name, cb, fail) {
    var key = "ai" + name, src = box.dataset[key + "Src"];
    if (S[key]) return cb(S[key]);
    var s = doc.createElement("script"); s.src = src;
    s.onload = function () { if (S[key]) cb(S[key]); else { s.remove(); note("That part of the writing partner did not start. Your text is unchanged."); if (fail) fail(); } };
    s.onerror = function () { s.remove(); note("That part of the writing partner could not be loaded. Your text is unchanged."); if (fail) fail(); };
    doc.head.appendChild(s);
  }
  function cmd(name, again) {
    if (name === "menu") return menu();
    if (name === "proposal") return toCard();
    if (name === "chat" || name === "reviews") { S.aiWant = name; if (S.inspector) S.inspector.open("ai", true); doc.dispatchEvent(new win.CustomEvent("scriobh:aitab", { detail: name })); return; }
    if (name === "critique" || name === "continuity" || name === "extract" || name === "summary") { return part("reviews", function (R) { R.start(name); }); }
    var rid = RECIPE_OF[name];
    if (!rid) return;
    var sel = selectionNow();
    if (!sel) return note("Select the text first.");
    if (sel.error) return note(sel.error);
    var spec = { recipe: rid, document_id: DOC, params: again && again.params ? again.params : {}, title: undefined };
    saved().then(function (ok) {
      if (!ok) return;
      spec.selection = Object.assign({ base_revision: W.rev }, sel.sel);
      ask(spec).then(function (res) {
        if (!res) { say("Cancelled. Nothing was sent."); return; }
        if (!res.ok) { note(res.message || "It did not go through."); return; }
        if (rid === "alternatives") candidates(spec, res, sel); else propose(spec, res, sel);
      });
    });
  }

  S.ai = { ready: true, cmd: cmd, approve: approve, run: run, ask: ask, call: call, h: h, why: why, money: money, note: note, say: say, saved: saved, part: part, waiting: waiting, selectionNow: selectionNow, recipes: recipes, closeCard: closeCard, diffNode: diffNode, metaLine: metaLine, btn: btn, propose: propose, guard: guard, keyed: keyed, touch: touch, slot: slot, closeCard: closeCard, focusIf: focusIf };
})();
