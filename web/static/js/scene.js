/* Scríobh writing view: the "In this scene" tab of the inspector. The scene's point of view and place (a character
   and a place of the book's bible, with the free text kept as it was), and every entity its text mentions, with
   the book's own name and summary for each. Entities the person may not read never appear: the panel only says
   that some were left out. It never takes focus by itself, and a changed list is written in place. */
(function () {
  "use strict";
  var doc = document, win = window, S = win.scriobh, W = S && S.write;
  if (!W) return;
  var el = function (tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  var api = W.api, id = W.cfg.id, canEdit = W.cfg.canEdit, slug = W.cfg.slug, root = null, retry = 0, timer = 0, picking = null;
  var KIND = { character: "Character", place: "Place", item: "Item", org: "Organisation", concept: "Concept" };
  function say(m) { var l = doc.getElementById("live"); if (l) { l.textContent = ""; win.setTimeout(function () { l.textContent = m; }, 40); } }
  function sheet(e) { return "/p/" + slug + "/e/" + e.id; }
  function kindWord(k) { return KIND[k] || k; }

  function load() {
    return api("GET", "/api/v1/docs/" + id + "/scene-entities").then(function (r) {
      if (!r.ok || !r.json) { root.replaceChildren(el("p", "field-error", "The list for this scene could not be loaded.")); return; }
      draw(r.json);
      win.clearTimeout(timer);
      if (r.json.indexing && retry++ < 6) timer = win.setTimeout(function () { if (!root.hidden && !picking) load(); }, 3000);
      else if (!r.json.indexing) retry = 0;
    }, function () { root.replaceChildren(el("p", "field-error", "Could not reach the server.")); });
  }

  // ---- the two references ----------------------------------------------------------------------------------
  function ref(label, field, brief, text) {
    var row = el("div", "sc-ref"), name = el("div", "sc-refname");
    name.append(el("span", "insp-label", label));
    if (brief) {
      var a = el("a", "", brief.name); a.href = sheet(brief);
      name.append(a, el("span", "insp-tag", kindWord(brief.kind)));
      if (brief.overridden) name.append(el("span", "insp-tag", "This book's version"));
    } else if (text) name.append(el("span", "", text), el("span", "insp-tag", "Not linked to the bible"));
    else name.append(el("span", "fine", "Not set"));
    row.append(name);
    if (canEdit) {
      var acts = el("div", "ver-acts");
      var change = el("button", "btn quiet small", brief || text ? "Change" : "Choose");
      change.type = "button";
      change.setAttribute("aria-label", (brief || text ? "Change the " : "Choose the ") + label.toLowerCase());
      change.addEventListener("click", function () { pick(field, label, row); });
      acts.append(change);
      if (brief) {
        var off = el("button", "btn quiet small", "Unlink");
        off.type = "button";
        off.setAttribute("aria-label", "Unlink the " + label.toLowerCase() + " from the bible (the text stays)");
        off.addEventListener("click", function () { set(field, ""); });
        acts.append(off);
      }
      row.append(acts);
    }
    return row;
  }
  function set(field, entity) {
    api("PUT", "/api/v1/docs/" + id + "/" + field, { entity: entity, base_revision: W.rev }).then(function (r) {
      var o = r.json && (r.json.item || r.json);
      if (!r.ok || !o) { var e = (r.json && r.json.error) || {}; say(e.message || "That could not be set."); if (S.toast) S.toast(e.message || "That could not be set."); return; }
      if (o.revision > W.rev) W.rev = o.revision;
      doc.dispatchEvent(new win.CustomEvent("scriobh:scene", { detail: { pov: o.pov, location: o.location } }));
      picking = null;
      load().then(function () { var b = root.querySelector(".sc-ref .btn"); if (b) b.focus(); });
      say(entity ? "Linked." : "Unlinked. The text stays.");
    }, function () { say("Could not reach the server."); });
  }
  // A small search box over the book's characters or places, as a listbox.
  function pick(field, label, row) {
    if (picking) { picking.remove(); picking = null; }
    var kind = field === "pov" ? "character" : "place", box = el("div", "sc-pick"), input = el("input"), list = el("ul", "sc-opts"), lab = el("label", "sr-only", "Find a " + kind), cancel = el("button", "btn quiet small", "Cancel");
    var lid = "sc-pick-" + field;
    input.type = "text"; input.id = lid + "-in"; input.autocomplete = "off"; input.spellcheck = false; input.placeholder = "Type a name"; lab.htmlFor = input.id;
    input.setAttribute("role", "combobox"); input.setAttribute("aria-expanded", "false"); input.setAttribute("aria-controls", lid); input.setAttribute("aria-autocomplete", "list");
    list.id = lid; list.setAttribute("role", "listbox"); list.setAttribute("aria-label", kind === "character" ? "Characters" : "Places"); list.hidden = true;
    cancel.type = "button";
    var items = [], act = 0, seq = 0, tm = 0;
    function draw() {
      list.replaceChildren();
      items.forEach(function (it, i) {
        var li = el("li", "sc-opt", it.name); li.id = lid + "-" + i; li.setAttribute("role", "option"); li.setAttribute("aria-selected", String(i === act));
        li.addEventListener("pointerdown", function (e) { e.preventDefault(); });
        li.addEventListener("click", function () { set(field, it.id); });
        list.append(li);
      });
      list.hidden = !items.length;
      input.setAttribute("aria-expanded", String(items.length > 0));
      if (items.length) input.setAttribute("aria-activedescendant", lid + "-" + act); else input.removeAttribute("aria-activedescendant");
    }
    function search() {
      var mine = ++seq;
      api("GET", "/api/v1/projects/" + W.cfg.project + "/entities?kind=" + kind + "&limit=8&q=" + encodeURIComponent(input.value.trim())).then(function (r) {
        if (mine !== seq) return;
        items = r.ok && r.json ? (r.json.items || []) : []; act = 0; draw();
        if (!items.length) say(input.value.trim() ? "No " + kind + " matches." : "No " + kind + "s in this book's bible yet.");
        else say(items.length + (items.length === 1 ? " match." : " matches."));
      }, function () { if (mine === seq) { items = []; draw(); say("Could not reach the server."); } });
    }
    input.addEventListener("input", function () { win.clearTimeout(tm); tm = win.setTimeout(search, 200); });
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") { if (!items.length) return; e.preventDefault(); act = (act + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length; draw(); }
      else if (e.key === "Enter") { e.preventDefault(); if (items[act]) set(field, items[act].id); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); box.remove(); picking = null; var b = row.querySelector(".btn"); if (b) b.focus(); }
    });
    cancel.addEventListener("click", function () { box.remove(); picking = null; var b = row.querySelector(".btn"); if (b) b.focus(); });
    box.append(lab, input, list, cancel);
    row.append(box);
    picking = box;
    input.focus();
    search();
  }

  // ---- the mentions -------------------------------------------------------------------------------------------
  function mentioned(it) {
    var li = el("li", "insp-item sc-item"), b = it.entity, roles = it.roles || [];
    var btn = el("button", "sc-name", b.name);
    btn.type = "button"; btn.setAttribute("aria-expanded", "false");
    var card = el("div", "sc-card"); card.hidden = true; card.id = "sc-card-" + b.id;
    btn.setAttribute("aria-controls", card.id);
    btn.addEventListener("click", function () { var open = card.hidden; card.hidden = !open; btn.setAttribute("aria-expanded", String(open)); });
    var bits = [];
    if (roles.indexOf("pov") >= 0) bits.push("point of view");
    if (roles.indexOf("location") >= 0) bits.push("location");
    if (it.count) bits.push("named " + it.count + (it.count === 1 ? " time" : " times"));
    if (it.explicit) bits.push(it.explicit + (it.explicit === 1 ? " link" : " links"));
    li.append(btn, el("span", "insp-tag", kindWord(b.kind)));
    if (b.overridden) li.append(el("span", "insp-tag", "This book's version"));
    if (it.ambiguous) li.append(el("span", "insp-tag", "Could also mean another entity"));
    if (bits.length) li.append(el("span", "insp-crumb", bits.join(", ")));
    if (b.summary) card.append(el("p", "sc-sum", b.summary)); else card.append(el("p", "fine", "No summary yet."));
    var open = el("a", "", "Open the sheet"); open.href = sheet(b);
    card.append(open);
    li.append(card);
    return li;
  }
  function draw(d) {
    var kids = [], h = el("h2", "ver-h sr-only", "In this scene");
    h.id = "sc-h"; h.tabIndex = -1;
    kids.push(h, ref("Point of view", "pov", d.pov, d.pov_text), ref("Location", "location", d.location, d.location_text));
    // Choosing from the bible writes the name into the scene's own text field, so it is as visible as the text is.
    if (canEdit) kids.push(el("p", "fine", "The name is saved in the scene\u2019s text, which everyone on this book can read."));
    var list = el("ul", "insp-list"), items = (d.items || []).filter(function (x) { return x.roles.indexOf("mentioned") >= 0 || x.count || x.explicit; });
    items.forEach(function (it) { list.append(mentioned(it)); });
    var h2 = el("h2", "ver-h", "Named in the text");
    kids.push(h2);
    if (items.length) kids.push(list);
    else kids.push(el("p", "fine", d.indexing ? "Still looking through the book for names. This fills in within a moment." : "No characters, places or things from the bible are named in this text yet."));
    if (d.indexing && items.length) kids.push(el("p", "fine", "Still looking through the book for names; more may appear."));
    if (d.hidden) kids.push(el("p", "fine", "Some names in this scene are left out because they belong to a Compendium you are not a member of."));
    var keepPick = picking && root.contains(picking);
    var wrap = el("div", "sc-wrap");
    wrap.append.apply(wrap, kids);
    root.replaceChildren(wrap);
    if (keepPick) picking = null;
  }

  S.scene = { show: function (panel) { root = panel; load(); } };
  // The list follows saves: the index is updated in the same transaction as the text.
  var after = 0;
  doc.addEventListener("scriobh:saved", function () {
    win.clearTimeout(after);
    after = win.setTimeout(function () { if (root && !root.hidden && S.inspector && S.inspector.current === "scene" && !picking) load(); }, 2500);
  });
  var panel = doc.getElementById("ipanel-scene");
  if (panel && S.inspector && S.inspector.current === "scene") S.scene.show(panel);
})();
