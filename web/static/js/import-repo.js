/* Scríobh: import a Git repository. Starts the fetch (import.git_preview with an address or a project's remote),
   follows the job while the tab is showing, then hands the ready job to js/import.js, which draws the preview,
   takes the edits and applies. Loaded on the repository import page only, after import.js. Everything the server
   says is set as text. A token is read from its field, sent once, and the field is emptied at once. */
(function () {
  "use strict";
  var doc = document, win = window, I = win.scriobhImport;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  var form = $("#rg-form");
  if (!I || !form) return;
  var jobEl = $("#rg-job"), list = $("#rg-jobs"), sec = $("#rg-jobs-sec"), go = $("#rg-start");
  var cur = "", timer = 0, state = "";
  function h(tag, attrs, kids) {
    var e = doc.createElement(tag);
    for (var k in attrs || {}) { var v = attrs[k]; if (v === false || v == null) continue; if (k === "text") e.textContent = v; else e.setAttribute(k, v === true ? "" : v); }
    (kids || []).forEach(function (c) { if (c) e.appendChild(c); });
    return e;
  }
  function num(n) { return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }
  function noun(n, one, many) { return num(n) + " " + (n === 1 ? one : many); }

  // ---- what went wrong, in words -------------------------------------------------------------------------------------
  var HINT = {
    address_blocked: "Scríobh will not connect to a machine on a private or internal network unless the administrator allows it. If this repository is on your own network, ask whoever runs this server to set SCRIOBH_GIT_ALLOW_PRIVATE=1. A public host such as github.com needs nothing.",
    auth_failed: "The host refused the sign-in. A private repository needs a token that can read it, and a user name if the host asks for one. A wrong or expired token looks exactly like this.",
    repo_not_found: "The host cannot find that repository, or says you may not read it. Check the spelling of the address; a private repository also needs a token.",
    unreachable: "This server could not reach the host. Check the address, then try again in a moment.",
    timeout: "The host did not answer in time. Try again in a moment.",
    too_large: "Import only a folder of the repository (the Folder field), or ask an administrator about SCRIOBH_GIT_MAX_REPO_MB.",
    host_key_unpinned: "The remote's host key has not been accepted yet. Do that on the project's Git page, then come back.",
    host_key_changed: "The remote's host key changed, so nothing was fetched. Look at it on the project's Git page.",
    credentials_missing: "That remote has no credential stored. Set it on the project's Git page.",
    credentials_unreadable: "The stored credential of that remote cannot be read by this server. Enter it again on the project's Git page.",
    empty: "Push something to the repository first, or import another one.",
    layout: "Scríobh looks for Markdown files (.md) and for a Scríobh project file (scriobh.json). If your text is in a sub-folder, name it in the Folder field.",
    invalid: "Use the https address of the repository, for example https://github.com/owner/book.git. An ssh address needs a deploy key: import through one of your projects' remotes instead.",
    forbidden: "Only a project's owner can import through its remote.",
    not_found: "That remote is not there, or it is not one of yours."
  };
  var BY_TEXT = [[/not one this server may connect to/, "address_blocked"], [/refused the (credentials|deploy key)/, "auth_failed"], [/does not exist, or that/, "repo_not_found"],
    [/could not be reached/, "unreachable"], [/did not answer in time/, "timeout"], [/larger than the|sent more data/, "too_large"], [/repository is empty/, "empty"],
    [/no Markdown text|no branch to import|head commit|files could not be read|is not in the repository|lists more than|totals more than/, "layout"]];
  function hintFor(code, msg) {
    if (HINT[code]) return HINT[code];
    for (var i = 0; i < BY_TEXT.length; i++) if (BY_TEXT[i][0].test(msg || "")) return HINT[BY_TEXT[i][1]];
    return "";
  }

  // ---- the job panel -----------------------------------------------------------------------------------------------------
  function panel(job, msg, hint) {
    var head = h("h2", { id: "rg-job-h", tabindex: "-1", text: "The fetch" });
    var meta = h("p", { class: "rg-meta" }, [h("span", { text: job.source_url || "" }), job.branch ? h("span", { text: " · " + job.branch }) : null, job.path ? h("span", { text: " · folder " + job.path }) : null, job.commit_sha ? h("span", { text: " · commit " + job.commit_sha.slice(0, 8) }) : null]);
    var kids = [head, meta, h("p", { class: "rg-line", text: msg })];
    if (hint) kids.push(h("p", { class: "rg-hint", text: hint }));
    if (job.status === "running") {
      kids.push(h("p", { class: "fine", id: "rg-wait", text: "A large repository can take a few minutes. You can leave this page: the fetch carries on and waits in your list below for a day. A fetch in progress cannot be stopped; when it ends you can throw it away." }));
    }
    jobEl.replaceChildren.apply(jobEl, kids);
    jobEl.hidden = false;
    return head;
  }
  function failed(job, code, focus) {
    var msg = job.error || "The fetch failed.";
    var hd = panel(job, msg, hintFor(code, msg));
    jobEl.setAttribute("data-bad", "");
    I.say("The fetch failed. The reason is in the box below.", true);
    if (focus) hd.focus();
  }

  // ---- following a job ------------------------------------------------------------------------------------------------------
  function follow(job, focus) {
    clearTimeout(timer);
    cur = job.id;
    jobEl.removeAttribute("data-bad");
    upsert(job);
    if (job.status === "running") {
      var first = state !== "running:" + job.id;
      state = "running:" + job.id;
      var hd = panel(job, "Fetching the repository…");
      if (first) { I.say("Fetching the repository…"); if (focus) hd.focus(); }
      later(job.id);
    } else if (job.status === "preview") {
      state = "preview:" + job.id;
      var s = job.summary || {};
      var h2 = panel(job, "Fetched. " + (s.documents != null ? noun(s.documents, "document", "documents") + ", " + noun(s.words, "word", "words") + "." : "") + " Below is what would be created; nothing is created until you press the button at the end.");
      I.say("The repository is ready to preview.");
      I.begin(job.id).then(function () { if (focus) h2.focus(); });
    } else if (job.status === "failed") {
      state = "failed:" + job.id;
      failed(job, "", true);
    } else {
      state = "";
      panel(job, "This import already created a project.");
    }
  }
  function later(id) {
    clearTimeout(timer);
    if (cur !== id || doc.hidden) return; // a hidden tab asks nothing; it resumes when shown
    timer = setTimeout(function () {
      I.api("/api/v1/imports/git?job_id=" + encodeURIComponent(id), undefined, "GET").then(function (j) { if (cur === id) follow(j.item || j); },
        function (e) { if (cur === id) { I.say(e.status === 404 ? "That import is gone." : "Could not reach the server. Trying again.", true); if (e.status !== 404) later(id); } });
    }, 2500);
  }
  doc.addEventListener("visibilitychange", function () {
    if (doc.hidden) clearTimeout(timer);
    else if (cur && state === "running:" + cur) { clearTimeout(timer); timer = setTimeout(function () { later(cur); }, 0); }
  });

  // ---- starting ------------------------------------------------------------------------------------------------------------------
  function mode() { return $("#rg-src-remote").checked ? "remote" : "url"; }
  function syncMode() {
    var r = mode() === "remote";
    $("#rg-b-url").hidden = r; $("#rg-b-remote").hidden = !r;
    if (r) { var o = $("#rg-remote").selectedOptions[0]; $("#rg-branch").placeholder = o && o.dataset.branch ? o.dataset.branch : "The remote's branch"; }
    else $("#rg-branch").placeholder = "The repository's own default";
  }
  Array.prototype.forEach.call(form.querySelectorAll('input[name="rg-src"]'), function (r) { r.addEventListener("change", syncMode); });
  $("#rg-remote").addEventListener("change", syncMode);
  syncMode();
  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var b = {}, branch = $("#rg-branch").value.trim(), path = $("#rg-path").value.trim();
    if (mode() === "remote") b.remote_id = $("#rg-remote").value;
    else {
      b.url = $("#rg-url").value.trim();
      if (!b.url) { I.say("Enter the repository's address first.", true); $("#rg-url").focus(); return; }
      var tok = $("#rg-token").value, user = $("#rg-user").value.trim();
      if (tok) { b.token = tok; if (user) b.username = user; }
    }
    if (branch) b.branch = branch;
    if (path) b.path = path;
    $("#rg-token").value = ""; // used once, never kept in the page
    clearTimeout(timer);
    cur = ""; state = "";
    I.clear();
    jobEl.hidden = true; jobEl.removeAttribute("data-bad");
    go.disabled = true;
    I.say("Starting the fetch…");
    I.api("/api/v1/imports/git/preview", b).then(function (r) {
      go.disabled = false;
      follow(r.job, true);
    }, function (er) {
      go.disabled = false;
      var m = er.message || "The fetch could not be started.";
      var hd = panel({ source_url: b.url || "", branch: branch, path: path }, m, hintFor(er.code || (er.status === 403 ? "forbidden" : er.status === 404 ? "not_found" : er.status === 422 ? "invalid" : ""), m));
      jobEl.setAttribute("data-bad", "");
      I.say("The fetch could not be started. The reason is in the box below.", true);
      hd.focus();
    });
  });

  // ---- the list --------------------------------------------------------------------------------------------------------------------
  function label(j) {
    if (j.status === "running") return "Being fetched";
    if (j.status === "preview") return "Ready" + (j.summary ? ": " + noun(j.summary.documents, "document", "documents") + ", " + noun(j.summary.words, "word", "words") : "");
    return j.status === "done" ? "Imported" : "Failed";
  }
  function btn(text, act, id, quiet, aria) { return h("button", { type: "button", class: "btn small" + (quiet ? " quiet" : ""), "data-act": act, "data-job": id, "aria-label": aria, text: text }); }
  function upsert(j) {
    if (!list) return;
    var li = h("li", { class: "rg-item", "data-id": j.id, "data-status": j.status }, [
      h("div", { class: "rg-item-main" }, [h("span", { class: "rg-url", text: j.source_url }),
        h("span", { class: "rg-meta", text: (j.branch ? j.branch + " · " : "") + (j.path ? "folder " + j.path + " · " : "") + new Date(j.created_at).toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" }) }),
        h("span", { class: "rg-state", "data-s": j.status, text: label(j) }), j.error ? h("span", { class: "rg-err", text: j.error }) : null]),
      h("div", { class: "rg-acts" }, [
        j.status === "preview" ? btn("Continue", "open", j.id) : null,
        j.status === "running" ? btn("Check progress", "check", j.id) : null,
        j.status === "preview" || j.status === "failed" ? btn("Throw away", "discard", j.id, true, "Throw away the import of " + j.source_url) : null])]);
    var old = list.querySelector('[data-id="' + j.id + '"]');
    if (old) list.replaceChild(li, old); else list.insertBefore(li, list.firstChild);
    sec.hidden = false;
  }
  if (list) list.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("button[data-act]");
    if (!b) return;
    var id = b.dataset.job, act = b.dataset.act;
    if (act === "discard") {
      b.disabled = true;
      I.api("/api/v1/imports/git?job_id=" + encodeURIComponent(id), undefined, "DELETE").then(function () {
        var li = list.querySelector('[data-id="' + id + '"]');
        if (li) li.remove();
        if (cur === id) { cur = ""; state = ""; clearTimeout(timer); I.clear(); jobEl.hidden = true; }
        if (!list.children.length) sec.hidden = true;
        I.say("Thrown away.");
        $("#imp-title").focus();
      }, function (er) { b.disabled = false; I.say(er.message, true); });
      return;
    }
    I.api("/api/v1/imports/git?job_id=" + encodeURIComponent(id), undefined, "GET").then(function (j) { follow(j.item || j, true); }, function (er) { I.say(er.status === 404 ? "That import is gone." : er.message, true); });
  });
})();
