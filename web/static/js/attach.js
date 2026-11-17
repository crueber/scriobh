/* Scríobh attachments: adding files, and using them in a chapter.
   One lazy file for three places:
   - the writing view: a button, drag and drop, and paste of an image upload the file to the project; when it is
     stored a small bar asks for alt text and an explicit "Insert". Nothing is put into the text before that, and
     what is inserted goes in at the cursor as one ordinary edit.
   - the attachments page: the same uploader, then the list is asked for again.
   - a file's page: copy the Markdown, and preview a PDF in a frame on request.
   Uploads are raw-body requests to the attachment op; the server decides what a file is. */
(function boot(tries) {
  "use strict";
  // In the writing view the editor may still be starting when this file arrives: wait for it, for up to fifteen seconds.
  var w0 = window.scriobh && window.scriobh.write;
  if (document.getElementById("write") && !(w0 && w0.editor && w0.editor.view) && tries < 150) { window.setTimeout(function () { boot(tries + 1); }, 100); return; }
  run();
  function run() {
  var doc = document, win = window, S = (win.scriobh = win.scriobh || {});
  if (S.attach) return;
  var $ = function (s, r) { return (r || doc).querySelector(s); };
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function mb(n) { return n >= 1048576 ? (Math.round(n / 104857.6) / 10) + " MB" : Math.max(1, Math.round(n / 1024)) + " KB"; }
  var W = S.write, ed = W && W.editor && W.editor.view ? W.editor : null;
  var mgr = $("#attach[data-attach]");
  var API = ed ? "/api/v1/projects/" + W.cfg.project + "/attachments" : mgr ? mgr.dataset.attach : "";
  var live = $("#att-live");
  if (!live && ed) { live = el("p", "sr-only"); live.id = "att-live"; live.setAttribute("role", "status"); live.setAttribute("aria-live", "polite"); $("#write").appendChild(live); }
  function say(m) { if (live) { live.textContent = ""; win.setTimeout(function () { live.textContent = m; }, 30); } }
  function toast(m) { if (S.toast) S.toast(m); else say(m); }

  // ---- copy buttons, and the PDF preview -----------------------------------------------------------------
  function unhide(root) {
    Array.prototype.forEach.call((root || doc).querySelectorAll("[data-copy][hidden]"), function (b) { b.hidden = false; });
  }
  doc.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-copy]");
    if (!b) return;
    var text = b.dataset.copy, done = function (ok) { say(ok ? "Copied." : "Select the text and copy it."); };
    if (win.navigator.clipboard && win.navigator.clipboard.writeText) win.navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(false); });
    else done(false);
  });
  unhide();
  var pv = $("#att-preview");
  if (pv) {
    pv.hidden = false;
    pv.addEventListener("click", function () {
      var box = $("#att-frame"), open = pv.getAttribute("aria-expanded") === "true";
      if (open) { box.replaceChildren(); box.hidden = true; pv.setAttribute("aria-expanded", "false"); pv.textContent = "Preview here"; return; }
      var f = el("iframe");
      f.referrerPolicy = "no-referrer"; // the preview is asked for without telling the file where it was opened from
      f.setAttribute("allow", "");      // and with no browser feature (camera, clipboard, fullscreen) delegated to it
      f.title = "Preview of " + ($("h1") || {}).textContent;
      f.src = pv.dataset.src;
      box.replaceChildren(f);
      box.hidden = false;
      pv.setAttribute("aria-expanded", "true");
      pv.textContent = "Hide the preview";
    });
  }
  if (!API) { S.attach = {}; return; }

  // ---- what may be added ----------------------------------------------------------------------------------
  var EXT = { png: 1, jpg: 1, jpeg: 1, gif: 1, webp: 1, pdf: 1, txt: 1, md: 1, markdown: 1 };
  var max = mgr ? parseInt(mgr.dataset.max, 10) || 0 : 0;
  function limit() {
    if (max) return Promise.resolve(max);
    return win.fetch("/api/v1/files/usage", { credentials: "same-origin", headers: { Accept: "application/json" } }).then(function (r) { return r.ok ? r.json() : null; }).then(function (j) { max = (j && j.max_file_bytes) || 0; return max; }, function () { return 0; });
  }
  function refuse(f, cap) {
    var ext = (f.name.split(".").pop() || "").toLowerCase(), t = f.type || "";
    if (ext === "svg" || t === "image/svg+xml") return "SVG pictures are not accepted, because they can carry scripts. Save the picture as a PNG, or as a PDF, and add that.";
    if (!EXT[ext] && !/^image\/(png|jpeg|gif|webp)$/.test(t) && t !== "application/pdf" && !/^text\/(plain|markdown)$/.test(t)) return f.name + " is not a kind of file that can be added. Pictures (PNG, JPEG, GIF, WebP), PDFs, plain text and Markdown can.";
    if (!f.size) return f.name + " is empty.";
    if (cap && f.size > cap) return f.name + " is " + mb(f.size) + ", more than the " + mb(cap) + " one file may be.";
    return "";
  }
  function explain(status, j, f) {
    var e = (j && j.error) || {}, code = e.code || "";
    if (code === "quota_exceeded") return (e.message || "There is no room left.") + " Delete files you no longer need, then try again.";
    if (status === 401) return "You are signed out. Sign in again, then add " + f.name + ".";
    if (status === 413 || code === "too_large") return f.name + " is larger than " + (e.data && e.data.max_file_bytes ? mb(e.data.max_file_bytes) : "the server accepts") + ", the most one file may be.";
    if (status === 429 && code !== "quota") return "Too many uploads at once. Wait a moment, then add " + f.name + " again.";
    return e.message || "The server could not take " + f.name + ".";
  }

  // ---- the queue ---------------------------------------------------------------------------------------------
  var queue = [], busy = false, n = 0;
  var host = ed ? $("#notices") : $("#att-queue");
  function box(f) {
    var id = "att-r" + (++n), b = ed ? el("div", "notice-bar att-row") : el("li", "att-row");
    b.id = ed ? "nb-" + id : id;
    if (ed) { b.setAttribute("role", "group"); b.setAttribute("aria-label", "Upload of " + f.name); }
    var msg = el("p", "notice-msg att-msg"), acts = el("div", "notice-actions");
    b.append(msg, acts);
    host.appendChild(b);
    if (ed) win.dispatchEvent(new win.Event("resize")); // the writing view keeps the caret clear of its bars
    return { b: b, msg: msg, acts: acts, f: f };
  }
  function drop(r) { r.b.remove(); if (ed) win.dispatchEvent(new win.Event("resize")); }
  function button(label, fn, cls) { var b = el("button", cls || "btn", label); b.type = "button"; b.addEventListener("click", fn); return b; }
  function problem(r, text, again) {
    r.b.dataset.kind = "danger";
    r.msg.textContent = text;
    r.acts.replaceChildren();
    if (again) r.acts.append(button("Try again", function () { drop(r); add([r.f]); }));
    r.acts.append(button("Dismiss", function () { drop(r); if (ed) W.editor.focus(); }, "btn quiet"));
    say(text);
    busy = false; next();
  }
  function send(r) {
    var f = r.f, x = new win.XMLHttpRequest(), prog = el("progress");
    prog.max = 100; prog.value = 0; prog.className = "att-progress";
    prog.setAttribute("aria-label", "Upload of " + f.name);
    var last = -1;
    r.msg.textContent = "Adding " + f.name + "… ";
    r.msg.appendChild(prog);
    r.acts.replaceChildren(button("Cancel", function () { x.abort(); }, "btn quiet"));
    x.open("POST", API + "?filename=" + encodeURIComponent(f.name));
    x.setRequestHeader("Content-Type", f.type || "application/octet-stream");
    x.setRequestHeader("Accept", "application/json");
    x.upload.onprogress = function (e) {
      if (!e.lengthComputable) return;
      var p = Math.round(e.loaded * 100 / e.total);
      prog.value = p;
      if (p - last >= 25 || p === 100) { last = p; say("Adding " + f.name + ": " + p + " percent."); }
    };
    x.onload = function () {
      var j = null;
      try { j = JSON.parse(x.responseText); } catch (e) { /* not JSON */ }
      if (x.status >= 200 && x.status < 300 && j && j.item) stored(r, j.item, !!j.duplicate);
      else problem(r, explain(x.status, j, f), x.status !== 413 && x.status !== 422);
    };
    x.onerror = function () { problem(r, "The upload did not reach the server. Check the connection, then try again.", true); };
    x.onabort = function () { drop(r); say("Cancelled."); busy = false; next(); };
    x.send(f);
  }
  function next() {
    if (busy || !queue.length) return;
    busy = true;
    send(queue.shift());
  }
  function add(files) {
    var list = Array.prototype.slice.call(files);
    if (!list.length) return;
    limit().then(function (cap) {
      list.forEach(function (f) {
        var why = refuse(f, cap), r = box(f);
        if (why) { r.b.dataset.kind = "danger"; r.msg.textContent = why; say(why); r.acts.append(button("Dismiss", function () { drop(r); }, "btn quiet")); return; }
        r.msg.textContent = f.name + ": waiting.";
        queue.push(r);
      });
      next();
    });
  }

  // ---- when a file is stored -----------------------------------------------------------------------------------
  function esc(s) { return s.replace(/\\/g, "\\\\").replace(/\[/g, "\\[").replace(/\]/g, "\\]"); }
  function stored(r, item, dup) {
    busy = false;
    var pre = dup ? item.filename + " is already here, so no second copy was made. " : "";
    r.b.dataset.kind = "ok";
    if (!ed) {
      r.msg.textContent = pre + (dup ? "" : item.filename + " is added.");
      r.acts.replaceChildren(button("Dismiss", function () { drop(r); }, "btn quiet"));
      say(r.msg.textContent);
      refresh();
      next();
      return;
    }
    var img = item.kind === "image", id = r.b.id + "-alt", field = el("div", "att-alt-field");
    var lab = el("label", "", img ? "Alt text (what the picture shows)" : "Link text"), inp = el("input");
    lab.htmlFor = inp.id = id;
    inp.type = "text"; inp.maxLength = 500; inp.autocomplete = "off";
    inp.value = img ? (item.alt || item.filename.replace(/\.[^.]*$/, "").replace(/[_-]+/g, " ")) : item.filename;
    field.append(lab, inp);
    r.msg.textContent = pre + (dup ? "" : item.filename + " is added. ");
    r.msg.appendChild(field);
    function go() {
      var v = inp.value.trim() || item.filename;
      var st = doc.getElementById("save-state");
      if (W.cfg.readonly || (st && st.dataset.state === "locked")) { toast("This chapter cannot be edited here, so nothing was inserted."); return; }
      var md = img ? "![" + esc(v) + "](" + item.url + ")" : "[" + esc(v) + "](" + item.url + ")";
      if (ed.mode() === "source") {
        var ta = $("#write .scriobh-source");
        if (ta) {
          // After the selection, never over it; insertText keeps it on the textarea's own undo stack.
          ta.focus(); ta.setSelectionRange(ta.selectionEnd, ta.selectionEnd);
          if (!doc.execCommand("insertText", false, md)) { ta.setRangeText(md, ta.selectionEnd, ta.selectionEnd, "end"); ta.dispatchEvent(new win.Event("input", { bubbles: true })); }
        }
      } else {
        // An added file never replaces what is selected (a picture, a passage): it goes in after it.
        var st0 = ed.view.state, sel0 = st0.selection;
        if (!sel0.empty && sel0.constructor.near) ed.view.dispatch(st0.tr.setSelection(sel0.constructor.near(st0.doc.resolve(sel0.to), 1)));
        ed.pasteMarkdown(md);
      }
      if (img && !item.alt && v !== item.filename) win.fetch(API + "/" + item.id, { method: "PATCH", credentials: "same-origin", headers: { "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify({ alt: v }) }).catch(function () {});
      drop(r);
      ed.focus();
      say((img ? "Image" : "Link") + " inserted.");
    }
    inp.addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); go(); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); drop(r); ed.focus(); }
    });
    r.acts.replaceChildren(button(img ? "Insert image" : "Insert link", go, "btn solid"), button("Not now", function () { drop(r); ed.focus(); }, "btn quiet"));
    say(r.msg.textContent + "Type its alt text and choose Insert. From the text, Shift and Tab reach the field.");
    // Focus moves to the alt field only if the person is not typing in the chapter or elsewhere.
    var a = doc.activeElement;
    if (!a || a === doc.body || (a.closest && a.closest(".fmtbar"))) inp.focus();
    next();
  }

  // ---- the page's list -----------------------------------------------------------------------------------------------
  var pending = 0;
  function refresh() {
    var cur = $("#att-list");
    if (!cur || !cur.dataset.list) return;
    win.fetch(cur.dataset.list, { credentials: "same-origin", headers: { "X-Scriobh-Fetch": "1" } }).then(function (r) { return r.ok ? r.text() : null; }).then(function (html) {
      if (!html) return;
      var t = doc.createElement("template");
      t.innerHTML = html;
      var nl = t.content.firstElementChild;
      if (!nl || nl.id !== "att-list") return;
      var old = $("#att-list");
      if (old.contains(doc.activeElement)) { say("The list changed. Reload the page to see the new files."); return; }
      old.replaceWith(nl);
      unhide(nl);
    }, function () {});
  }
  doc.addEventListener("scriobh:event", function (e) {
    var d = e.detail || {};
    if (mgr && /^attachment\./.test(d.type || "") && !d.by_you) { win.clearTimeout(pending); pending = win.setTimeout(refresh, 400); }
  });

  // ---- where files come from -----------------------------------------------------------------------------------------
  var input = el("input");
  input.type = "file"; input.multiple = true; input.hidden = true;
  input.accept = "image/png,image/jpeg,image/gif,image/webp,application/pdf,text/plain,text/markdown,.md,.markdown,.txt";
  input.addEventListener("change", function () { add(input.files); input.value = ""; });
  doc.body.appendChild(input);
  function files(dt) { return dt && dt.types && Array.prototype.indexOf.call(dt.types, "Files") >= 0; }
  function over(zone, on) { zone.classList.toggle("att-over", on); }
  function zone(z) {
    ["dragenter", "dragover"].forEach(function (t) { z.addEventListener(t, function (e) { if (files(e.dataTransfer)) { e.preventDefault(); over(z, true); } }, true); });
    z.addEventListener("dragleave", function (e) { if (!z.contains(e.relatedTarget)) over(z, false); }, true);
    z.addEventListener("drop", function (e) { if (!files(e.dataTransfer)) return; e.preventDefault(); e.stopPropagation(); over(z, false); add(e.dataTransfer.files); }, true);
  }
  if (ed) {
    if (!W.cfg.canEdit) { S.attach = {}; return; }
    var btn = $('.tb[data-cmd="image"]');
    if (btn) btn.addEventListener("click", function () { input.click(); });
    zone($("#paper") || ed.view.dom);
    ed.view.dom.addEventListener("paste", function (e) {
      var cd = e.clipboardData, fl = cd ? Array.prototype.filter.call(cd.files || [], function (f) { return /^image\//.test(f.type); }) : [];
      if (!fl.length || cd.getData("text/plain")) return; // text that came with it is the person's paste, not the picture's
      e.preventDefault(); e.stopPropagation();
      add(fl.map(function (f) { return f.name && /\.\w+$/.test(f.name) ? f : new win.File([f], "pasted-image." + (f.type.split("/")[1] || "png").replace("jpeg", "jpg"), { type: f.type }); }));
    }, true);
  } else {
    var pick = $("#att-pick"), dz = $("#att-drop");
    if (pick) { pick.hidden = false; pick.addEventListener("click", function () { input.click(); }); var ns = $(".att-noscript"); if (ns) ns.hidden = true; }
    if (dz) zone(dz);
  }
  S.attach = { add: add };
  }
})(0);
