/* Scríobh: the AI settings pages. /settings/ai (the providers an account may use, the instance's switch and caps, usage) and
   /p/{slug}/ai (a project's policy, what is never sent, what it costs). Both are drawn here from the ai.* ops and every change
   goes through them; nothing is kept in the browser. Keys are write-only: a field to replace one, never a value. */
(function () {
  "use strict";
  var doc = document, win = window, root = doc.getElementById("ai-root");
  if (!root) return;
  var page = doc.getElementById("ai-project") || doc.getElementById("ai-settings"), isProject = page.id === "ai-project";
  var P = isProject ? page.dataset.project : "", owner = page.dataset.owner === "true", editor = page.dataset.edit === "true", admin = page.dataset.admin === "true", writer = admin || page.dataset.writer === "true";
  var live = doc.getElementById("ai-announce");
  function h(tag, attrs) {
    var e = doc.createElement(tag);
    for (var k in attrs || {}) { var v = attrs[k]; if (v == null || v === false) continue; if (k === "text") e.textContent = v; else if (k === "class") e.className = v; else e.setAttribute(k, v === true ? "" : v); }
    (function add(list) { for (var i = 0; i < list.length; i++) { var c = list[i]; if (c == null || c === false) continue; if (Array.isArray(c)) add(c); else e.appendChild(typeof c === "string" ? doc.createTextNode(c) : c); } })([].slice.call(arguments, 2));
    return e;
  }
  function say(m) { if (live) { live.textContent = ""; win.setTimeout(function () { live.textContent = m; }, 30); } }
  function api(method, path, body) {
    return win.fetch(path, { method: method, credentials: "same-origin", headers: body !== undefined ? { Accept: "application/json", "Content-Type": "application/json" } : { Accept: "application/json" }, body: body !== undefined ? JSON.stringify(body) : undefined }).then(function (r) {
      return r.text().then(function (t) { var j = null; try { j = t ? JSON.parse(t) : null; } catch (e) { /* not JSON */ } return { ok: r.ok, status: r.status, json: j }; });
    }, function () { return { ok: false, status: 0, json: null }; });
  }
  function why(r) { return (r.json && r.json.error && r.json.error.message) || (r.status === 0 ? "Could not reach the server." : "That did not work."); }
  function money(micros, cur) { return (micros / 1e6).toFixed(micros && micros < 10000 ? 4 : 2) + " " + (cur || ""); }
  function n(v) { return Number(v || 0).toLocaleString(); }
  var KIND = { openrouter: "OpenRouter", ollama: "Ollama (local)", opencode: "OpenCode", openai_compatible: "OpenAI-compatible" };
  var DEFAULT_URL = { openrouter: "https://openrouter.ai/api/v1", ollama: "http://127.0.0.1:11434", opencode: "http://127.0.0.1:4096", openai_compatible: "" };
  var ACTIONS = [
    ["selection", "Rewrite, tighten, expand, change tone", "The selection actions: a proposal you review, never inserted by itself."],
    ["alternatives", "Alternative wordings", "Candidates you can add to an alternative."],
    ["chat", "Chat", "Talk about your writing with the context you choose."],
    ["critique", "Critique", "Notes on a chapter or a passage, kept as a review."],
    ["summaries", "Summaries", "A chapter summary for the synopsis, as a proposal."],
    ["continuity", "Continuity checks", "Findings against the bible, the timeline and earlier chapters."],
    ["extraction", "Entity extraction", "Proposed bible entries, created only when you accept each."],
    ["lab", "Lab checks that use a model", "Marks on the text; the local checks never use a model."]
  ];
  // A table from a caption (for screen readers), headings [[text, numeric]] and rows of cells (text or nodes).
  function table(cap, heads, rows) {
    var tr = h("tr"), tb = h("tbody");
    heads.forEach(function (x) { tr.appendChild(h("th", { scope: "col", class: x[1] ? "n" : "" }, x[0])); });
    rows.forEach(function (cells) { var r = h("tr"); cells.forEach(function (c, i) { r.appendChild(h("td", { class: heads[i] && heads[i][1] ? "n" : "" }, c)); }); tb.appendChild(r); });
    return h("div", { class: "ai-scroll", role: "region", tabindex: "0", "aria-label": cap }, h("table", { class: "ai-table" }, h("caption", { class: "sr-only", text: cap }), h("thead", {}, tr), tb));
  }
  function field(id, label, input, hint) { return h("div", { class: "field" }, h("label", { for: id, text: label }), input, hint ? h("span", { class: "hint", text: hint }) : null); }
  function button(label, fn, cls) { var b = h("button", { type: "button", class: "btn" + (cls ? " " + cls : ""), text: label }); b.addEventListener("click", fn); return b; }
  // The first press arms the button and says so; the second, not sooner than half a second later, does it. It disarms after five
  // seconds, on Escape and when focus leaves it, and a double-click does nothing.
  function guard(b, armedText, fn) {
    var label = b.textContent, armed = false, settling = false, released = false, fresh = false, t1 = 0, t2 = 0;
    function up(e) { if (e.key === "Enter" || e.key === " ") released = true; }
    function down(e) { if (released && !e.repeat && (e.key === "Enter" || e.key === " ")) fresh = true; } // a new press, not a repeat of the held one
    function disarm() { armed = false; settling = false; win.clearTimeout(t1); win.clearTimeout(t2); doc.removeEventListener("keyup", up, true); doc.removeEventListener("keydown", down, true); b.textContent = label; b.disabled = false; b.removeAttribute("data-armed"); }
    b.addEventListener("click", function (e) {
      if (!armed) {
        armed = true; settling = true; released = e.detail !== 0; fresh = false; doc.addEventListener("keyup", up, true); doc.addEventListener("keydown", down, true); // a click made by a key waits for the key to be let go
        b.textContent = armedText; b.setAttribute("data-armed", "true"); b.disabled = true; say(armedText + " Press it again to do it.");
        t1 = win.setTimeout(function () { settling = false; b.disabled = false; var a = doc.activeElement; if (!a || a === doc.body || a === b || a.contains(b)) b.focus(); else disarm(); }, 500); t2 = win.setTimeout(disarm, 5000);
        return;
      }
      if (settling || !released || (e.detail === 0 && !fresh)) return; // by key: only a new press after the first was let go
      disarm(); fn();
    });
    b.addEventListener("blur", function () { if (armed && !settling) disarm(); });
    b.addEventListener("keydown", function (e) { if (e.key === "Escape" && armed) { e.preventDefault(); e.stopPropagation(); disarm(); } });
  }
  function plainWarn(m) { return String(m || "").replace(/ \(ai\.[a-z.]+\)/g, "").replace(/micro-units/g, "small units"); }
  function banner(tone, head, detail) { return h("div", { class: "ai-banner", "data-tone": tone, role: "group" }, h("p", { text: head }), detail ? h("p", { text: detail }) : null); }
  function section(id, title, sub) { var s = h("section", { id: id, "aria-labelledby": id + "-h" }, h("h2", { id: id + "-h", text: title })); if (sub) s.appendChild(h("p", { class: "sub", text: sub })); return s; }

  // ---- providers ---------------------------------------------------------------------------------------------------
  function providerCard(p, reload) {
    var card = h("li", { class: "ai-card" });
    var tags = h("span", {}, h("span", { class: "ai-tag", text: p.scope === "instance" ? "Instance" : "Personal" }), " ", p.local ? h("span", { class: "ai-tag", "data-k": "local", text: "Stays on this machine or network" }) : h("span", { class: "ai-tag", "data-k": "leaves", text: "Leaves your server" }), p.enabled ? null : h("span", { class: "ai-tag", text: " Off" }));
    card.appendChild(h("h3", {}, p.name, " ", tags));
    var facts = h("dl", { class: "ai-facts" });
    function fact(k, v) { facts.appendChild(h("div", {}, h("dt", { text: k }), h("dd", { text: v }))); }
    fact("Kind", KIND[p.kind] || p.kind);
    if (p.base_url) fact("Address", p.base_url);
    fact("Key", p.has_key ? "Stored" + (p.key_hint ? ", ends in " + p.key_hint : "") + ". It is never shown." : (p.kind === "ollama" || p.local ? "None needed" : "Not set"));
    fact("Default model", p.default_model || "None chosen");
    fact("Models known", String((p.models || []).length));
    card.appendChild(facts);
    var out = h("p", { class: "ai-result", role: "status" });
    var acts = h("div", { class: "ai-actions" });
    acts.appendChild(button("Test", function () {
      out.textContent = "Testing…"; out.removeAttribute("data-ok");
      api("POST", "/api/v1/ai/providers/" + p.id + "/test", {}).then(function (r) {
        if (!r.ok) { out.textContent = why(r); out.setAttribute("data-ok", "false"); return; }
        var j = r.json; out.textContent = j.message + (j.latency_ms ? " (" + j.latency_ms + " ms)" : ""); out.setAttribute("data-ok", j.ok ? "true" : "false"); say(j.message);
      });
    }));
    if (p.editable) {
      acts.appendChild(button("Edit", function () { editForm(card, p, reload); }));
      var del = button("Remove", function () {}, "danger");
      guard(del, "Really remove it and its key?", function () {
        api("DELETE", "/api/v1/ai/providers/" + p.id).then(function (r) { if (!r.ok) { out.textContent = why(r); out.setAttribute("data-ok", "false"); return; } say("The provider was removed."); reload(); });
      });
      acts.appendChild(del);
    }
    card.appendChild(acts); card.appendChild(out);
    return card;
  }
  function providerFields(p, isAdmin, mode) {
    var kind = h("select", { id: "pf-kind", name: "kind" });
    Object.keys(KIND).forEach(function (k) { var o = h("option", { value: k, text: KIND[k] }); if ((p && p.kind === k) || (!p && k === "ollama")) o.selected = true; kind.appendChild(o); });
    if (p) kind.disabled = true;
    var name = h("input", { id: "pf-name", type: "text", maxlength: "80", autocomplete: "off", required: "" }); name.value = p ? p.name : "";
    var url = h("input", { id: "pf-url", type: "url", autocomplete: "off", spellcheck: "false" }); url.value = p ? (p.base_url || "") : DEFAULT_URL.ollama;
    var key = h("input", { id: "pf-key", type: "password", autocomplete: "new-password", placeholder: p && p.has_key ? "Stored. Type to replace it." : "" });
    var model = h("input", { id: "pf-model", type: "text", autocomplete: "off", spellcheck: "false" }); model.value = p ? (p.default_model || "") : "";
    var local = h("input", { id: "pf-local", type: "checkbox" }); if (p && p.local) local.checked = true;
    var scope = h("select", { id: "pf-scope" }, h("option", { value: "user", text: "Personal: only I can use it" }), isAdmin ? h("option", { value: "instance", text: "Instance: projects may allow it; nobody else holds the key" }) : null);
    if (isAdmin && !p) scope.value = "instance";
    var on = h("input", { id: "pf-on", type: "checkbox" }); on.checked = p ? p.enabled : true;
    if (!p) kind.addEventListener("change", function () { if (DEFAULT_URL[kind.value] !== undefined && (!url.value || Object.keys(DEFAULT_URL).some(function (k) { return DEFAULT_URL[k] === url.value; }))) url.value = DEFAULT_URL[kind.value]; local.checked = kind.value === "ollama" || kind.value === "opencode" ? local.checked : false; });
    var els = { kind: kind, name: name, url: url, key: key, model: model, local: local, scope: scope, on: on };
    var rows = [
      h("div", { class: "field-row" }, field("pf-kind", "Kind", kind), field("pf-name", "Name", name)),
      field("pf-url", "Address", url, "OpenRouter's is fixed. A server on this machine or your network needs the administrator's \"stays on this network\" setting below."),
      field("pf-key", "API key", key, "Write-only: it is sealed on the server and never shown again. Leave it empty to keep the stored one."),
      field("pf-model", "Default model", model, "The model name as the provider spells it. You can type one in; the list the provider gives is shown by Test."),
    ];
    if (!p && (isAdmin || mode === "settings")) rows.push(field("pf-scope", "Who may use it", scope));
    if (isAdmin) rows.push(h("div", { class: "ai-checks" }, h("label", {}, local, h("span", {}, "Stays on this machine or network", h("small", { text: "Allows a loopback or private address and plain http. Only an administrator can say so, and a project set to local only uses only these." })))));
    rows.push(h("div", { class: "ai-checks" }, h("label", {}, on, h("span", { text: "In use" }))));
    return { els: els, rows: rows };
  }
  function editForm(card, p, reload) {
    var f = providerFields(p, admin, "settings"), form = h("form", { class: "ai-form", novalidate: "" }), msg = h("p", { class: "ai-result", role: "alert" });
    f.rows.forEach(function (r) { form.appendChild(r); });
    var save = h("button", { type: "submit", class: "btn solid", text: "Save" });
    form.appendChild(h("div", { class: "ai-actions" }, save, button("Cancel", reload, "quiet")));
    form.appendChild(msg);
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      var e2 = f.els, body = { name: e2.name.value.trim(), default_model: e2.model.value.trim(), enabled: e2.on.checked };
      if (e2.url.value.trim() !== (p.base_url || "")) body.base_url = e2.url.value.trim();
      if (e2.key.value) body.api_key = e2.key.value;
      if (admin) body.local = e2.local.checked;
      save.disabled = true;
      api("PATCH", "/api/v1/ai/providers/" + p.id, body).then(function (r) { save.disabled = false; if (!r.ok) { msg.textContent = why(r); msg.setAttribute("data-ok", "false"); return; } say("Saved."); reload(); });
    });
    card.replaceChildren(h("h3", { text: "Edit " + p.name }), form);
    f.els.name.focus();
  }
  function addForm(reload) {
    var f = providerFields(null, admin, "settings"), form = h("form", { class: "ai-form", novalidate: "" }), msg = h("p", { class: "ai-result", role: "alert" });
    f.rows.forEach(function (r) { form.appendChild(r); });
    var add = h("button", { type: "submit", class: "btn solid", text: "Add the provider" });
    form.appendChild(h("div", { class: "ai-actions" }, add)); form.appendChild(msg);
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      var e2 = f.els, body = { kind: e2.kind.value, name: e2.name.value.trim(), base_url: e2.url.value.trim(), default_model: e2.model.value.trim(), enabled: e2.on.checked, scope: admin ? e2.scope.value : "user" };
      if (e2.key.value) body.api_key = e2.key.value;
      if (admin) body.local = e2.local.checked;
      if (!body.name || !body.base_url) { msg.textContent = "Give it a name and an address."; msg.setAttribute("data-ok", "false"); return; }
      add.disabled = true;
      api("POST", "/api/v1/ai/providers", body).then(function (r) { add.disabled = false; if (!r.ok) { msg.textContent = why(r); msg.setAttribute("data-ok", "false"); return; } say("The provider was added. Test it before you rely on it."); reload(); });
    });
    var d = h("details", { class: "ai-card" }, h("summary", { text: "Add a provider" }), form);
    return d;
  }

  // ---- the settings page --------------------------------------------------------------------------------------------
  function usageBlock(u) {
    var s = section("usage", "Your use this month", "What you ran, from the run log. Prompts and answers are not stored, only counts.");
    var t = u.totals || {};
    s.appendChild(h("dl", { class: "ai-facts" },
      h("div", {}, h("dt", { text: "Requests" }), h("dd", { text: n(t.runs) })),
      h("div", {}, h("dt", { text: "Tokens in and out" }), h("dd", { text: n(t.input_tokens) + " and " + n(t.output_tokens) })),
      h("div", {}, h("dt", { text: "Cost" }), h("dd", { text: money(t.cost_micros, u.currency) }))));
    (u.budget && u.budget.lines || []).forEach(function (l) { s.appendChild(meter(l, u.currency)); });
    if ((u.days || []).length) s.appendChild(table("Use by day", [["Day"], ["Requests", 1], ["Tokens", 1], ["Cost", 1]], u.days.map(function (d) { return [d.day, n(d.runs), n(d.input_tokens + d.output_tokens), money(d.cost_micros, u.currency)]; })));
    return s;
  }
  var SCOPE_LABEL = { instance: "This server", user: "You", project: "This project", project_owner: "The project's owner" };
  var CAP_LABEL = { instance: "The server's cap", user: "Your cap", project: "This project's cap", project_owner: "The project owner's cap" };
  function meter(l, cur) {
    var wrap = h("div", { class: "ai-meter" });
    var capT = l.max_tokens, capC = l.max_cost_micros;
    if (!capT && !capC) { wrap.appendChild(h("span", { text: CAP_LABEL[l.scope] + ": no cap. Used " + n(l.spent_tokens) + " tokens, " + money(l.spent_cost_micros, cur) + "." })); return wrap; }
    var used = capC ? l.spent_cost_micros + l.reserved_cost_micros : l.spent_tokens + l.reserved_tokens, max = capC || capT;
    var p = h("progress", { max: String(max), value: String(Math.min(used, max)), "aria-label": CAP_LABEL[l.scope] + " (monthly)" });
    var pct = Math.round(100 * used / max);
    wrap.appendChild(p);
    wrap.appendChild(h("span", { text: CAP_LABEL[l.scope] + ": " + pct + "% of the monthly cap" + (capC ? " (" + money(used, cur) + " of " + money(capC, cur) + ")" : " (" + n(used) + " of " + n(capT) + " tokens)") + (pct >= 100 ? ". Reached: requests are refused until next month." : pct >= 80 ? ". Nearly reached." : ".") }));
    return wrap;
  }
  function drawSettings() {
    var secs = [];
    if (!writer) { root.replaceChildren(banner("info", "AI providers are for people who can write.", "Readers and reviewers do not set up providers or use the writing partner.")); root.removeAttribute("data-loading"); return; }
    Promise.all([api("GET", "/api/v1/ai/status"), api("GET", "/api/v1/ai/providers"), api("GET", "/api/v1/ai/usage"), admin ? api("GET", "/api/v1/ai/settings") : null, admin ? api("GET", "/api/v1/ai/budget") : null, api("GET", "/api/v1/projects")]).then(function (rs) {
      var st = rs[0].json, pv = rs[1].json, us = rs[2].json, se = rs[3] && rs[3].json, bu = rs[4] && rs[4].json, pj = rs[5].json;
      var top = [];
      if (!rs[0].ok) top.push(banner("danger", "AI could not be checked.", why(rs[0])));
      else if (!st.enabled) top.push(banner("warn", "AI is off for the whole server.", st.message || "An administrator switched it off, or the server was started with it off."));
      else if (!(pv && (pv.items || []).length)) top.push(banner("info", "No provider is set up, so AI is not available.", admin ? "Add one below: OpenRouter, Ollama on this machine, or an OpenCode instance." : "An administrator can add one" + (pv && pv.personal_providers_allowed ? ", or you can add a personal one below." : ".")));
      else top.push(banner("on", "AI can run, project by project.", "Each project is off until its owner turns it on, and nothing is sent without a preview you approve."));
      ((st && st.warnings) || []).forEach(function (w) { top.push(banner("warn", plainWarn(w.message))); });
      var sp = section("providers", "Providers", "The model servers you can use: the instance's, and your own.");
      var list = h("ul", { class: "ai-list" });
      ((pv && pv.items) || []).forEach(function (p) { list.appendChild(providerCard(p, drawSettings)); });
      if (!list.children.length) sp.appendChild(h("p", { class: "fine", text: "Nothing is configured." }));
      sp.appendChild(list);
      if (admin || (writer && pv && pv.personal_providers_allowed)) sp.appendChild(addForm(drawSettings)); else sp.appendChild(h("p", { class: "fine", text: "Personal providers are switched off on this server." }));
      secs.push(sp);
      if (admin && se) secs.push(adminBlock(se, bu));
      var items = (pj && pj.items || []).filter(function (x) { return x.role === "owner" || x.role === "editor"; }).slice(0, 40);
      secs.push(projectsBlock(items));
      if (us) secs.push(usageBlock(us));
      root.replaceChildren.apply(root, top.concat(secs));
      root.removeAttribute("data-loading");
    });
  }
  function adminBlock(se, bu) {
    var s = section("instance", "This server", "For administrators.");
    var on = h("input", { id: "as-on", type: "checkbox" }); on.checked = se.enabled;
    var cur = h("input", { id: "as-cur", type: "text", maxlength: "8" }); cur.value = se.currency || "";
    var msg = h("p", { class: "ai-result", role: "status" });
    var form = h("form", { class: "ai-form", novalidate: "" }, h("div", { class: "ai-checks" }, h("label", {}, on, h("span", {}, "AI is allowed on this server", h("small", { text: se.env_off ? "The server was started with AI off (SCRIOBH_AI=off); this switch cannot override that." : "Off stops every AI request at once, whatever a project says." })))), field("as-cur", "Currency label", cur, "A label only: it says what prices and caps are counted in."), h("div", { class: "ai-actions" }, h("button", { type: "submit", class: "btn", text: "Save" })), msg);
    if (se.env_off) on.disabled = true;
    form.addEventListener("submit", function (e) { e.preventDefault(); api("PUT", "/api/v1/ai/settings", { enabled: on.checked, currency: cur.value.trim() }).then(function (r) { msg.textContent = r.ok ? "Saved." : why(r); msg.setAttribute("data-ok", r.ok ? "true" : "false"); say(msg.textContent); }); });
    s.appendChild(form);
    s.appendChild(h("h3", { text: "Monthly caps" }));
    s.appendChild(h("p", { class: "sub", text: "A request that could take the month over a cap is refused before it is sent." }));
    var rows = (bu && bu.rows) || [];
    if (rows.length) s.appendChild(table("Caps that are set", [["Applies to"], ["Tokens", 1], ["Cost", 1], [""]], rows.map(function (r) {
      var rm = button("Remove", function () { api("PUT", "/api/v1/ai/budget", { scope: r.scope, subject: r.subject || "", max_tokens: 0, max_cost_micros: 0 }).then(drawSettings); }, "quiet");
      return [SCOPE_LABEL[r.scope] + (r.subject ? " " + r.subject : ""), r.max_tokens ? n(r.max_tokens) : "none", r.max_cost_micros ? money(r.max_cost_micros, se.currency) : "none", rm];
    })));
    var tok = h("input", { id: "bg-tok", type: "number", min: "0", inputmode: "numeric" }), cost = h("input", { id: "bg-cost", type: "number", min: "0", step: "0.01", inputmode: "decimal" }), bm = h("p", { class: "ai-result", role: "status" });
    var bf = h("form", { class: "ai-form", novalidate: "" }, h("div", { class: "field-row" }, field("bg-tok", "Tokens a month", tok, "0 for none"), field("bg-cost", "Cost a month, in " + (se.currency || "units"), cost, "0 for none")), h("div", { class: "ai-actions" }, h("button", { type: "submit", class: "btn", text: "Set the cap for the whole server" })), bm);
    bf.addEventListener("submit", function (e) { e.preventDefault(); api("PUT", "/api/v1/ai/budget", { scope: "instance", max_tokens: parseInt(tok.value || "0", 10), max_cost_micros: Math.round(parseFloat(cost.value || "0") * 1e6) }).then(function (r) { if (!r.ok) { bm.textContent = why(r); bm.setAttribute("data-ok", "false"); return; } say("Cap saved."); drawSettings(); }); });
    s.appendChild(bf);
    return s;
  }
  function projectsBlock(items) {
    var s = section("projects", "Which providers a project may use", "A project uses a provider only if its policy names it. These are the projects you can edit.");
    if (!items.length) { s.appendChild(h("p", { class: "fine", text: "You have no project to edit." })); return s; }
    var list = h("ul", { class: "ai-list" });
    items.forEach(function (x) {
      var li = h("li", { class: "ai-card" }, h("h3", {}, h("a", { href: "/p/" + (x.address || x.slug) + "/ai", text: x.title })), h("p", { class: "fine", text: "Loading…" }));
      list.appendChild(li);
      api("GET", "/api/v1/projects/" + x.id + "/ai/policy").then(function (r) {
        var p = li.lastChild;
        if (!r.ok) { p.textContent = why(r); return; }
        var it = r.json.item, names = (r.json.usable_providers || []).filter(function (u) { return (it.providers || []).indexOf(u.id) >= 0; }).map(function (u) { return u.name + (u.local ? " (local)" : ""); });
        p.textContent = it.mode === "off" ? "AI is off." : (it.mode === "local_only" ? "Local providers only. " : "Asks before every request. ") + (names.length ? "May use: " + names.join(", ") + "." : "No provider is named yet, so nothing can run.");
      });
    });
    s.appendChild(list);
    return s;
  }

  // ---- the project page ---------------------------------------------------------------------------------------------
  function drawProject() {
    if (!editor) { root.replaceChildren(banner("info", "AI is for people who can edit this project.", "Readers and reviewers cannot see its AI settings or use the writing partner.")); root.removeAttribute("data-loading"); return; }
    // With the server's switch off nothing can be sent, but what sends nothing still works: the never-send flags, the use and
    // the run log, and deleting kept chats. The policy waits until it is switched on (its ops refuse).
    api("GET", "/api/v1/ai/status").then(function (sr) { load(!!(sr.json && sr.json.enabled === false)); });
  }
  function load(off) {
    var base = "/api/v1/projects/" + P;
    Promise.all([api("GET", "/api/v1/ai/status"), off ? null : api("GET", base + "/ai/policy"), off ? null : api("GET", base + "/ai/style"), api("GET", base + "/ai/exclude"), api("GET", base + "/docs"), owner ? api("GET", base + "/ai/usage") : api("GET", "/api/v1/ai/usage"), api("GET", "/api/v1/ai/budget?project_id=" + P), api("GET", base + "/ai/runs?limit=15"), api("GET", base + "/ai/chats")]).then(function (rs) {
      var st = rs[0].json, po = rs[1], sty = rs[2] && rs[2].json, ex = rs[3].json, docs = rs[4].json, us = rs[5].json, bu = rs[6].json, runs = rs[7].json, chats = rs[8].json;
      var out = [];
      if (off) out.push(banner("warn", "AI is turned off for this server.", ((st && st.message) || "").replace(/^AI is switched off on this server\. /, "") + " Nothing can be sent. You can still keep things out of every request, see what was used and delete your kept chats."), section("policy", "Policy", "The policy and the style guide are not shown or changed here while AI is switched off for this server. None of it is lost."));
      else if (!po.ok) { root.replaceChildren(banner("warn", "You cannot see this project's AI settings.", why(po))); root.removeAttribute("data-loading"); return; }
      else {
        var it = po.json.item, usable = po.json.usable_providers || [];
        if (st && !st.enabled) out.push(banner("warn", "AI is turned off for this server.", st.message || "Nothing here can run until an administrator switches it on."));
        else if (it.mode === "off") out.push(banner("info", "AI is off for this project.", owner ? "Turn it on below. Until you do, no request can be made and the writing view shows no AI controls." : "The project's owner can turn it on."));
        else out.push(banner("on", it.mode === "local_only" ? "AI is on, local providers only." : "AI is on: every request is previewed and asked for first.", usable.length ? null : "No provider is available yet. An administrator adds providers in your account's AI settings."));
        out.push(policyBlock(it, usable, sty, st));
      }
      out.push(excludeBlock(ex, docs));
      out.push(usageProject(us, bu, runs));
      out.push(chatsBlock(chats));
      root.replaceChildren.apply(root, out);
      root.removeAttribute("data-loading");
    });
  }
  function policyBlock(it, usable, sty, st) {
    var s = section("policy", "Policy", owner ? "Only the project's owner changes this, from a browser: an API key cannot." : "Set by the project's owner. You can read it.");
    var form = h("form", { class: "ai-form", novalidate: "" }), msg = h("p", { class: "ai-result", role: "status" });
    var dis = !owner;
    var modes = h("fieldset", { class: "ai-checks" }, h("legend", { text: "Mode" }));
    [["off", "Off", "Nothing is sent to any provider."], ["ask", "Ask first", "AI is available; every request shows exactly what will be sent and waits for you."], ["local_only", "Local only", "As Ask first, but only providers that stay on this machine or network."]].forEach(function (m) {
      var r = h("input", { type: "radio", name: "mode", value: m[0] }); if (it.mode === m[0]) r.checked = true; r.disabled = dis;
      modes.appendChild(h("label", {}, r, h("span", {}, m[1], h("small", { text: m[2] }))));
    });
    form.appendChild(modes);
    var pf = h("fieldset", { class: "ai-checks" }, h("legend", { text: "Providers this project may use" }));
    if (!usable.length) pf.appendChild(h("p", { class: "fine", text: "No provider is available." }));
    usable.forEach(function (u) { var c = h("input", { type: "checkbox", name: "provider", value: u.id }); c.checked = (it.providers || []).indexOf(u.id) >= 0; c.disabled = dis; pf.appendChild(h("label", {}, c, h("span", {}, u.name + (u.scope === "user" ? " (yours)" : ""), h("small", { text: (KIND[u.kind] || u.kind) + ". " + (u.local ? "Stays on this machine or network." : "Leaves your server.") })))); });
    form.appendChild(pf);
    var af = h("fieldset", { class: "ai-checks" }, h("legend", { text: "What it may do" }));
    ACTIONS.forEach(function (a) { var c = h("input", { type: "checkbox", name: "action", value: a[0] }); c.checked = (it.actions || []).indexOf(a[0]) >= 0; c.disabled = dis; af.appendChild(h("label", {}, c, h("span", {}, a[1], h("small", { text: a[2] })))); });
    form.appendChild(af);
    var red = h("select", { id: "po-red", disabled: dis }); [["off", "Off: names go as they are"], ["entities", "Entities: names and aliases from the bible become placeholders"], ["names", "Names: bible names and any capitalised names found in the text"]].forEach(function (o) { var op = h("option", { value: o[0], text: o[1] }); if (it.redaction === o[0]) op.selected = true; red.appendChild(op); });
    form.appendChild(field("po-red", "Redaction", red, "Placeholders such as {{CHAR_1}} replace the names before sending and are restored in the answer. The preview shows the mapping."));
    var ag = h("input", { type: "checkbox", id: "po-agents" }); ag.checked = !!it.agents_may_use_ai; ag.disabled = dis;
    var pe = h("input", { type: "checkbox", id: "po-personal" }); pe.checked = !!it.allow_personal_providers; pe.disabled = dis;
    form.appendChild(h("div", { class: "ai-checks" }, h("label", {}, ag, h("span", {}, "Agents (API keys) may run AI here", h("small", { text: "Off by default. A key can never change this policy." }))), h("label", {}, pe, h("span", {}, "Collaborators may use their own providers for this project", h("small", { text: "Off by default: only the providers above are used." })))));
    var tok = h("input", { id: "po-tok", type: "number", min: "0", inputmode: "numeric" }), cost = h("input", { id: "po-cost", type: "number", min: "0", step: "0.01", inputmode: "decimal" });
    tok.value = it.budget_tokens || 0; cost.value = ((it.budget_cost_micros || 0) / 1e6).toString(); tok.disabled = dis; cost.disabled = dis;
    form.appendChild(h("div", { class: "field-row" }, field("po-tok", "Tokens a month for this project", tok, "0 for no cap of your own. The server's and each person's caps still apply."), field("po-cost", "Cost a month for this project", cost, "In the server's currency. 0 for none.")));
    var style = h("textarea", { id: "po-style", rows: "6", maxlength: String(sty && sty.max_chars || 8000) }); style.value = (sty && sty.style_guide) || ""; style.disabled = dis;
    form.appendChild(field("po-style", "Style guide", style, "Your voice rules (point of view, tense, words never to use). It is sent only when a request includes the style guide, and every member's requests include yours."));
    if (owner) {
      form.appendChild(h("div", { class: "ai-actions" }, h("button", { type: "submit", class: "btn solid", text: "Save the policy" })));
      form.appendChild(msg);
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var q = function (sel) { return [].slice.call(form.querySelectorAll(sel)); };
        var mode = (q('input[name="mode"]:checked')[0] || {}).value || "off";
        var body = { mode: mode, providers: q('input[name="provider"]:checked').map(function (c) { return c.value; }), actions: q('input[name="action"]:checked').map(function (c) { return c.value; }), redaction: red.value, agents_may_use_ai: ag.checked, allow_personal_providers: pe.checked, budget_tokens: parseInt(tok.value || "0", 10), budget_cost_micros: Math.round(parseFloat(cost.value || "0") * 1e6) };
        api("PUT", "/api/v1/projects/" + P + "/ai/policy", body).then(function (r) {
          if (!r.ok) { msg.textContent = why(r); msg.setAttribute("data-ok", "false"); return; }
          api("PUT", "/api/v1/projects/" + P + "/ai/style", { style_guide: style.value }).then(function (r2) {
            msg.textContent = r2.ok ? "Saved." : "The policy was saved, but the style guide was not: " + why(r2); msg.setAttribute("data-ok", r2.ok ? "true" : "false"); say(msg.textContent);
            if (r2.ok) win.setTimeout(drawProject, 400);
          });
        });
      });
    }
    s.appendChild(form);
    return s;
  }
  function excludeBlock(ex, docs) {
    var s = section("never", "Never send", "A flagged chapter, note or entity is left out of every AI request, and a request that names one is refused. Lifting a flag needs a browser session too.");
    var flagged = {}; ((ex && ex.items) || []).forEach(function (x) { flagged[x.kind + ":" + x.id] = x; });
    var msg = h("p", { class: "ai-result", role: "status" });
    function set(kind, id, on, after) { api("PUT", "/api/v1/projects/" + P + "/ai/exclude", { kind: kind, id: id, exclude: on }).then(function (r) { if (!r.ok) { msg.textContent = why(r); msg.setAttribute("data-ok", "false"); return; } say(on ? "Never sent." : "May be sent again."); if (after) after(); }); }
    var list = h("fieldset", { class: "ai-checks" }, h("legend", { text: "Chapters and notes" }));
    var items = (docs && docs.items) || []; var order = (docs && docs.order) || items.map(function (d) { return d.id; }), by = {}; items.forEach(function (d) { by[d.id] = d; });
    order.forEach(function (id) { var d = by[id]; if (!d) return; var c = h("input", { type: "checkbox" }); c.checked = !!flagged["document:" + id]; c.disabled = !editor; c.addEventListener("change", function () { set("document", id, c.checked); }); list.appendChild(h("label", {}, c, h("span", { text: (d.title || "Untitled") + (d.kind && d.kind !== "chapter" ? " (" + d.kind + ")" : "") }))); });
    s.appendChild(list);
    var others = ((ex && ex.items) || []).filter(function (x) { return x.kind !== "document"; });
    if (others.length) {
      var ul = h("ul", { class: "ai-list" });
      others.forEach(function (x) { ul.appendChild(h("li", { class: "ai-card" }, h("span", { text: x.title + " (" + x.kind + ")" }), " ", editor ? button("Allow again", function () { set(x.kind, x.id, false, drawProject); }, "quiet") : null)); });
      s.appendChild(h("h3", { text: "Entities and research notes" })); s.appendChild(ul);
    }
    if (editor) {
      var q = h("input", { id: "nv-q", type: "text", autocomplete: "off", placeholder: "Name of an entity" }), res = h("ul", { class: "ai-list" });
      var go = button("Find", function () {
        res.replaceChildren();
        api("GET", "/api/v1/projects/" + P + "/entities?q=" + encodeURIComponent(q.value.trim())).then(function (r) {
          var its = (r.json && r.json.items) || []; if (!its.length) res.appendChild(h("li", { class: "fine", text: "No entity by that name." }));
          its.slice(0, 8).forEach(function (en) { res.appendChild(h("li", { class: "ai-card" }, h("span", { text: en.name + " (" + (en.kind || "entity") + ") " }), flagged["entity:" + en.id] ? h("span", { class: "ai-tag", text: "Never sent" }) : button("Never send", function () { set("entity", en.id, true, drawProject); }, "quiet"))); });
        });
      });
      s.appendChild(h("h3", { text: "Add an entity" })); s.appendChild(h("div", { class: "ai-actions" }, h("div", { class: "field" }, h("label", { for: "nv-q", text: "Find an entity to keep out" }), q), go)); s.appendChild(res);
    }
    s.appendChild(msg);
    return s;
  }
  function usageProject(us, bu, runs) {
    var s = section("usage", "Use and caps this month", owner ? "Everyone's use of AI on this project." : "Your own use of AI. The owner sees the project as a whole.");
    var cur = (us && us.currency) || (bu && bu.item && bu.item.currency) || "";
    var lines = (bu && bu.item && bu.item.lines) || [];
    if (!lines.length && !(us && us.totals)) s.appendChild(h("p", { class: "fine", text: "Nothing has been used." }));
    lines.forEach(function (l) { s.appendChild(meter(l, cur)); });
    var t = us && us.totals;
    if (t) s.appendChild(h("dl", { class: "ai-facts" }, h("div", {}, h("dt", { text: "Requests" }), h("dd", { text: n(t.runs) })), h("div", {}, h("dt", { text: "Tokens in and out" }), h("dd", { text: n(t.input_tokens) + " and " + n(t.output_tokens) })), h("div", {}, h("dt", { text: "Cost" }), h("dd", { text: money(t.cost_micros, cur) }))));
    if (us && us.by_user && us.by_user.length) s.appendChild(table("Use by person", [["Person"], ["Requests", 1], ["Cost", 1]], us.by_user.map(function (u) { return [u.display_name, n(u.runs), money(u.cost_micros, cur)]; })));
    var items = (runs && runs.items) || [];
    s.appendChild(h("h3", { text: "Recent requests" }));
    if (!items.length) s.appendChild(h("p", { class: "fine", text: "None yet. The log keeps counts and hashes, never the text." }));
    else s.appendChild(table("Recent requests", [["When"], ["What"], ["Where"], ["Tokens", 1], ["Result"]], items.map(function (r) { return [(r.created_at || "").replace("T", " ").slice(0, 16), r.recipe, (r.model || "") + (r.local ? " (local)" : ""), n(r.input_tokens + r.output_tokens), r.status + (r.error_code ? ": " + r.error_code : "")]; })));
    return s;
  }
  function chatsBlock(chats) {
    var s = section("chats", "Kept chats", "A chat is kept only if you ask for that in the chat. A kept chat is yours alone: not the owner, not an administrator and not another member can see it. It stays until you delete it, and it is removed with the project.");
    var items = (chats && chats.items) || [];
    var count = h("p", { tabindex: "-1" });
    function tell() { count.textContent = items.length ? "You have " + items.length + (items.length === 1 ? " kept chat." : " kept chats.") : "You have no kept chats."; }
    tell(); s.appendChild(count);
    if (items.length) {
      var ul = h("ul", { class: "ai-list" });
      items.forEach(function (c) {
        var id = "ch-" + c.chat_id, del = button("Delete", function () {}, "danger"), li = h("li", { class: "ai-card" }, h("h3", { id: id, text: c.title || "Untitled chat" }), h("p", { class: "hint", text: n(c.turn_count) + (c.turn_count === 1 ? " message" : " messages") + ", last on " + (c.updated_at || "").slice(0, 10) + "." }), h("div", { class: "ai-actions" }, del));
        del.setAttribute("aria-describedby", id);
        guard(del, "Really delete it?", function () {
          api("DELETE", "/api/v1/projects/" + P + "/ai/chat?chat_id=" + encodeURIComponent(c.chat_id)).then(function (r) {
            if (!r.ok) { say(why(r)); return; }
            li.remove(); items.splice(items.indexOf(c), 1); tell(); count.focus(); say("The kept chat was deleted.");
            if (!items.length) ul.remove(); delAll.hidden = items.length < 2;
          });
        });
        ul.appendChild(li);
      });
      var delAll = button("Delete all my kept chats", function () {}, "danger");
      guard(delAll, "Really delete them all?", function () {
        Promise.all(items.map(function (c) { return api("DELETE", "/api/v1/projects/" + P + "/ai/chat?chat_id=" + encodeURIComponent(c.chat_id)); })).then(function () { say("Deleted."); drawProject(); });
      });
      delAll.hidden = items.length < 2; s.appendChild(ul); s.appendChild(delAll);
    }
    return s;
  }

  if (isProject) drawProject(); else drawSettings();
})();
