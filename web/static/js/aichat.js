/* Scríobh writing view: chat with the writing partner, in the AI tab. A chat is about this chapter or the whole project; every
   message is previewed before it is sent, like everything else; the answer streams in and can be stopped. A chat is held by the
   browser and gone when the page is, unless the person keeps it: a kept chat is stored on the server, private to them (not the
   owner, not an administrator, not another member), and deleted whenever they like. Only what is said after Keep is turned on
   is kept. Nothing of a chat is inserted into the chapter; a wording a chat proposes for a selection is a proposal like any other. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write, A = S && S.ai;
  if (!W || !A) return;
  var h = A.h, P = W.cfg.project, DOC = W.cfg.id, base = "/api/v1/projects/" + P;
  var st = { turns: [], chatId: "", keep: false, scope: "doc", chats: null, busy: false, cancel: null }, ui = null, title = "";
  function dl(name, text) {
    var a = h("a", { href: win.URL.createObjectURL(new win.Blob([text], { type: "text/markdown" })), download: name }); doc.body.appendChild(a); a.click(); win.setTimeout(function () { win.URL.revokeObjectURL(a.href); a.remove(); }, 500);
  }
  function exportMd() {
    var t = (title || "Chat").trim() || "Chat";
    dl(t.replace(/[^\w -]+/g, "").trim().replace(/\s+/g, "-").toLowerCase() + ".md", "# " + t + "\n\n" + st.turns.map(function (x) { return "**" + (x.role === "user" ? "You" : "Writing partner") + "**\n\n" + x.content + "\n"; }).join("\n"));
  }
  function line(role, text, meta) {
    var li = h("li", { class: "ai-msg", "data-role": role }, h("strong", { text: role === "user" ? "You" : "Writing partner" }), h("div", { class: "ai-msg-text", text: text }), meta ? h("small", { text: meta }) : null);
    ui.log.appendChild(li); li.scrollIntoView({ block: "nearest" });
    return li;
  }
  function redraw() { ui.log.replaceChildren(); st.turns.forEach(function (t) { line(t.role, t.content); }); ui.keep.checked = st.keep; }
  function loadList() {
    return A.call("GET", base + "/ai/chats").then(function (r) {
      st.chats = (r.json && r.json.items) || [];
      var sel = ui.sel; sel.replaceChildren(h("option", { value: "", text: "A new chat" + (st.keep ? " (to be kept)" : " (not kept)") }));
      st.chats.forEach(function (c) { var o = h("option", { value: c.chat_id, text: c.title + " (" + c.turn_count + ")" }); if (c.chat_id === st.chatId) o.selected = true; sel.appendChild(o); });
      ui.del.disabled = !st.chatId; ui.exp.disabled = !st.turns.length;
    });
  }
  function pick(id) {
    st.chatId = id;
    if (!id) { st.turns = []; st.keep = ui.keep.checked; title = ""; redraw(); loadList(); return; }
    A.call("GET", base + "/ai/chat?chat_id=" + encodeURIComponent(id)).then(function (r) {
      if (!r.ok) { A.note(A.why(r)); return; }
      var c = r.json.item; st.turns = (c.turns || []).map(function (t) { return { role: t.role, content: t.content }; }); st.keep = true; title = c.title; redraw(); loadList();
    });
  }
  function send() {
    var msg = ui.msg.value.trim();
    if (!msg || st.busy) return;
    var sel = A.selectionNow();
    if (sel && sel.error) { A.note(sel.error); return; }
    // Busy from the first press: a second press while the page is still being saved must not open a second preview.
    st.busy = true; ui.send.disabled = true;
    function idle() { st.busy = false; ui.send.disabled = false; ui.stop.hidden = true; st.cancel = null; ui.log.setAttribute("aria-busy", "false"); }
    A.saved().then(function (ok) {
      if (!ok) { idle(); return; }
      var spec = { recipe: "chat", params: { message: msg }, turns: st.turns.slice(-20), stream: true, focusBack: ui.msg };
      if (st.scope === "doc") spec.document_id = DOC;
      if (sel) { spec.selection = Object.assign({ base_revision: W.rev }, sel.sel); spec.document_id = DOC; }
      return A.approve(spec).then(function (ap) {
        if (!ap) { idle(); A.say("Cancelled. Nothing was sent."); return; }
        ui.msg.value = "";
        line("user", msg);
        ui.log.setAttribute("aria-busy", "true");
        var out = line("assistant", "", "Waiting for the answer…"), txt = out.querySelector(".ai-msg-text"), meta = out.querySelector("small"), got = "";
        var pr = A.run(ap.req, { onDelta: function (d) { got += d; txt.textContent = got; out.scrollIntoView({ block: "nearest" }); } });
        ui.stop.hidden = false; st.cancel = pr.cancel;
        return pr.then(function (res) {
          idle();
          if (!res.ok) { txt.textContent = got; meta.textContent = (got ? "Incomplete. " : "") + (res.message || "The answer did not arrive."); A.focusIf(ui.msg, doc.getElementById("ipanel-ai"), "The answer did not arrive."); return; }
          var it = res.item; txt.textContent = it.text || got; meta.textContent = A.metaLine(it) + (it.incomplete ? " · incomplete" : "");
          var answer = it.text || got;
          st.turns.push({ role: "user", content: msg }, { role: "assistant", content: answer });
          if (it.data && it.data.suggestions && it.data.suggestions.length && sel) {
            var b = A.btn("Review it as a proposal for the selection", function () { A.propose({ recipe: "chat" }, res, sel); }); out.appendChild(b);
          }
          if (st.keep && it.status === "ok") return keepIt(msg, answer, it.run_id);
        });
      });
    }).catch(idle);
  }
  function keepIt(msg, answer, runId) {
    var go = st.chatId ? Promise.resolve(true) : A.call("POST", base + "/ai/chats", { title: msg.slice(0, 60) }).then(function (r) { if (!r.ok) { A.note(A.why(r)); return false; } st.chatId = r.json.item.chat_id; title = r.json.item.title; return true; });
    return go.then(function (ok) { if (!ok) return; return A.call("POST", base + "/ai/chat/append", { chat_id: st.chatId, message: msg, answer: answer, run_id: runId }).then(function (r) { if (!r.ok) A.note(A.why(r)); loadList(); }); });
  }
  function build() {
    var log = h("ol", { class: "ai-log", role: "log", "aria-live": "polite", "aria-busy": "false", "aria-label": "The chat" });
    var sel = h("select", { id: "aic-sel", "aria-label": "Kept chats" });
    var keep = h("input", { type: "checkbox", id: "aic-keep" }), scope = h("select", { id: "aic-scope", "aria-label": "What the chat is about" }, h("option", { value: "doc", text: "This chapter" }), h("option", { value: "project", text: "The whole project" }));
    var msg = h("textarea", { id: "aic-msg", rows: "3", "aria-label": "Your message", placeholder: "Ask about this chapter, or think aloud." });
    var send1 = h("button", { type: "button", class: "btn solid", text: "Send…" }), stop = h("button", { type: "button", class: "btn", text: "Stop" }); stop.hidden = true;
    var exp = h("button", { type: "button", class: "btn quiet small", text: "Export" }), del = h("button", { type: "button", class: "btn quiet small danger", text: "Delete this chat", disabled: true });
    ui = { log: log, sel: sel, keep: keep, scope: scope, msg: msg, send: send1, stop: stop, exp: exp, del: del };
    sel.addEventListener("change", function () { pick(sel.value); });
    keep.addEventListener("change", function () {
      st.keep = keep.checked; if (!st.chatId) loadList();
      if (keep.checked && st.turns.length && !st.chatId) A.say("Keep is on. What was said before is not kept; only what comes next is.");
    });
    scope.addEventListener("change", function () { st.scope = scope.value; });
    send1.addEventListener("click", send);
    msg.addEventListener("keydown", function (e) { if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); send(); } });
    stop.addEventListener("click", function () { if (st.cancel) st.cancel(); A.focusIf(msg, doc.getElementById("ipanel-ai")); });
    exp.addEventListener("click", exportMd);
    A.guard(del, "Really delete it?", function () {
      A.call("DELETE", base + "/ai/chat?chat_id=" + encodeURIComponent(st.chatId)).then(function (r) { if (!r.ok) { A.note(A.why(r)); return; } A.say("The chat was deleted."); st.chatId = ""; st.turns = []; title = ""; redraw(); loadList(); A.focusIf(msg, doc.getElementById("ipanel-ai")); });
    });
    return h("div", { class: "ai-chat" },
      h("div", { class: "field" }, h("label", { for: "aic-sel", text: "Chat" }), sel),
      h("div", { class: "ai-row" }, h("div", { class: "field" }, h("label", { for: "aic-scope", text: "About" }), scope), exp, del),
      h("div", { class: "ai-checks" }, h("label", {}, keep, h("span", {}, "Keep this chat", h("small", { text: "Stored on the server for you alone, until you delete it. Only what is said after you turn this on is kept. Off: it is gone when you leave the page." })))),
      log,
      h("div", { class: "field" }, h("label", { for: "aic-msg", text: "Message" }), msg, h("span", { class: "hint", text: "A selection in the chapter goes with it. You will see what is sent before it is. " + (S && S.keys ? S.keys.text("Mod+Enter") + " sends." : "Send it with the button.") + "" })),
      h("div", { class: "ai-row" }, send1, stop));
  }
  S.aichat = { show: function (el) { if (!ui) { var node = build(); S.aichat.node = node; loadList(); } el.replaceChildren(S.aichat.node); } };
})();
