/* Scríobh import page, books and tracked changes: the EPUB and PDF sources, what the preview says of a book (its metadata,
   the cut, the edition label), of a PDF (what could and could not be read) and of a Word file's tracked changes, the
   controls that nest a chapter under another or join it to one, and the answer after an apply that kept an edition or wrote
   suggestions. Words come from the catalogue (imp.js.*) through #imp-msg. Everything the server says is set as text.
   Loaded after js/import.js and js/import-formats.js on the import pages. */
(function () {
  "use strict";
  var doc = document, I = window.scriobhImport, fmt = doc.getElementById("imp-fmt"), mbox = doc.getElementById("imp-msg");
  if (!I || !fmt || !mbox) return;
  var M = {}, BOOK = 32 * 1024 * 1024, layout = "", label = null, notesOpen = false;
  try { M = JSON.parse(mbox.getAttribute("data-msg")) || {}; } catch (e) { M = {}; }
  var all = function (s) { return Array.prototype.slice.call(doc.querySelectorAll(s)); };
  function T(k, d, v) { var s = M[k] || d; if (v) for (var n in v) s = s.split("{" + n + "}").join(v[n]); return s; }
  function h(tag, attrs, kids) {
    var e = doc.createElement(tag);
    for (var k in attrs || {}) { var v = attrs[k]; if (v === false || v == null) continue; if (k === "text") e.textContent = v; else e.setAttribute(k, v === true ? "" : v); }
    (kids || []).forEach(function (c) { if (c) e.appendChild(c); });
    return e;
  }
  function num(n) { return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ","); }
  function radio(name) { var c = doc.querySelector('input[name="' + name + '"]:checked'); return c ? c.value : ""; }
  function pages(a) { a = a || []; return a.slice(0, 12).join(", ") + (a.length > 12 ? T("and_more", " and {n} more", { n: a.length - 12 }) : ""); }
  function fact(k, v) { return v || v === 0 ? h("div", null, [h("dt", { text: k }), h("dd", { text: String(v) })]) : null; }
  function list(items) { return h("ul", { class: "imp-facts" }, items.filter(Boolean).map(function (t) { return typeof t === "string" ? h("li", { text: t }) : t; })); }

  // ---- the sources: a book is sent whole, as base64, and kept by the server only for an EPUB --------------------------
  function bookFile(kind) {
    return function (f) {
      if (f.size > BOOK) return Promise.reject(new Error(T("too_big", "That file is larger than 32 MiB, which is the most that can be sent or kept.")));
      I.say(T("reading", "Reading {name}…", { name: f.name }));
      return I.b64(f).then(function (d) {
        return { files: [], kind: kind, name: f.name.replace(/\.[^.]+$/, ""), opts: function (b) {
          delete b.files; delete b.layout; b[kind] = d;
          if (kind === "epub") { b.epub_sections = radio("imp-es") || "auto"; b.epub_pages = radio("imp-ep") || "skip"; }
        } };
      });
    };
  }
  [["imp-epub", "epub"], ["imp-pdffile", "pdf"]].forEach(function (x) { var el = doc.getElementById(x[0]); if (el) I.pick(el, bookFile(x[1])); });
  all('input[name="imp-es"], input[name="imp-ep"], input[name="imp-dc"]').forEach(function (e) { e.addEventListener("change", I.again); });

  var H = I.hook, wasLabel = H.label;
  H.label = function (p) {
    layout = p.layout;
    return p.layout === "epub" ? T("name_epub", "An EPUB book") : p.layout === "pdf" ? T("name_pdf", "A PDF") : wasLabel && wasLabel(p);
  };
  H.undone = T("undone", "That change was not made. ");
  // What was done, in words for a screen reader: where a chapter went, not only that it moved.
  H.movedTo = function (t, i, n, o, above) { return T(above ? "moved_above" : "moved_below", above ? "Moved {t} above {o}: now {i} of {n}." : "Moved {t} below {o}: now {i} of {n}.", { t: t, o: o, i: i, n: n }); };
  H.movedUnder = function (t, p) { return p ? T("moved_under", "Moved {t} to the end of {p}.", { t: t, p: p }) : T("moved_top", "Moved {t} to the top level of the book.", { t: t }); };
  H.joined = function (t, p) { return T("joined", "Joined {t} into {p}. It is listed under Left out of the import, and can be put back.", { t: t, p: p }); };
  H.stays = function (t, j) { return T("stays", "{t} cannot be left out while {j} is joined into it. Put {j} back first.", { t: t, j: j }); };
  H.noJoin = T("no_join", "Choose a chapter to join this one into.");
  H.level = function (n) { return T("level", "level {n}", { n: n }); };
  H.body = function (b) { if (b.docx) b.docx_changes = radio("imp-dc") || "suggest"; };
  // A name a person types holds no direction override or control character; the text they see is the text sent.
  H.apply = function (b) { var v = label && label.value.replace(/[\u0000-\u001f\u007f-\u009f\u200e\u200f\u2028\u2029\u202a-\u202e\u2066-\u2069]/g, " ").replace(/\s+/g, " ").trim(); if (v) b.edition_label = v; };
  H.error = function (e) {
    var s = I.src(), k = s && s.kind;
    if (e.status === 413 && k) return T("too_big", "That file is larger than 32 MiB, which is the most that can be sent or kept.");
    if (e.status === 429 && k === "pdf") return T("pdf_busy", "A PDF is being read right now, yours or someone else's, and only one at a time can be. Nothing was imported and your file is still chosen. Try again in a moment.");
    if (e.status === 422 && k) { var m = e.message || ""; return m.charAt(0).toUpperCase() + m.slice(1) + (/[.!?]$/.test(m) ? "" : "."); } // the op's own refusal, in a sentence
    if (e.status === 429) return T("busy", "The server is busy with other imports. Nothing was imported and your file is still chosen. Try again in a moment.");
  };

  // ---- the preview ----------------------------------------------------------------------------------------------
  var wasShow = H.show;
  H.show = function (p, ui) {
    if (wasShow) wasShow(p, ui);
    if (!ui.bk) {
      ui.bk = { box: h("div", { class: "imp-book" }), map: h("div", { class: "imp-map" }) };
      ui.notes.parentNode.insertBefore(ui.bk.box, ui.notes.nextSibling);
      ui.tree.parentNode.insertBefore(ui.bk.map, ui.tree);
      label = null;
    }
    var kids = [], mapnote = [];
    if (p.layout === "epub" && p.epub) kids = epub(p, ui.bk);
    else if (p.layout === "pdf") kids = pdf(p);
    else if (p.layout === "docx") kids = word(p);
    if (p.layout === "epub" || p.layout === "pdf" || p.layout === "docx" || p.layout === "pandoc") mapnote = [h("h3", { text: T("map_h", "Chapters") }), h("p", { class: "fine", text: T("map_help", "This is how the book becomes parts, chapters and front and back matter. Open Edit on a line to rename it, change what it is, nest it under a part or a chapter, join it to another, or leave it out. Move up and Move down put it in order. Nothing is created until you press the button at the end.") })];
    if ((p.layout === "pdf" || p.layout === "docx") && ui.notes.children.length > 1) { // the server's own sentences, kept but folded: the panel above says the same in short
      var d = h("details", { class: "imp-files", open: notesOpen }, [h("summary", { text: T("notes_h", "Everything the importer noted ({n})", { n: ui.notes.children.length }) })].concat(Array.prototype.slice.call(ui.notes.children)));
      d.addEventListener("toggle", function () { notesOpen = d.open; });
      ui.notes.replaceChildren(d);
    }
    ui.bk.box.replaceChildren.apply(ui.bk.box, kids.filter(Boolean));
    ui.bk.map.replaceChildren.apply(ui.bk.map, mapnote);
  };

  function epub(p, bk) {
    var e = p.epub, c = e.cover, out = [h("h3", { text: T("book_h", "The book") })];
    var lang = e.language ? e.language + (e.direction === "rtl" ? " (" + T("rtl", "right to left") + ")" : "") : "";
    out.push(h("dl", { class: "imp-dl" }, [fact(T("f_title", "Title"), (e.title || T("not_stated", "not stated")) + (e.subtitle ? ": " + e.subtitle : "")), fact(T("f_author", "Author"), (e.authors || []).join(", ") || T("not_stated", "not stated")),
      fact(T("f_lang", "Language"), lang || T("not_stated", "not stated")), fact(T("f_version", "EPUB version"), e.version), fact(T("f_notes", "Footnotes"), num(e.footnotes || 0)), fact(T("f_publisher", "Publisher"), e.publisher)]));
    var contents = { nav: T("src_nav", "navigation document"), ncx: T("src_ncx", "NCX contents") }[e.contents];
    var cut = { toc: T("cut_toc", "By the book's table of contents{from}", { from: contents ? " (" + contents + ")" : "" }), files: T("cut_files", "One chapter for each file of the book"), headings: T("cut_headings", "At each chapter heading") }[e.strategy] || e.strategy;
    out.push(h("p", { class: "imp-line", text: T("cut_used", "How it was cut: {cut}.", { cut: (radio("imp-es") === "auto" ? T("cut_auto", "chosen for you, ") : "") + cut.charAt(0).toLowerCase() + cut.slice(1) }) }));
    out.push(h("p", { class: "imp-line", text: !c || !c.kept && !c.reason && !c.path ? T("cover_none", "This book has no cover picture.") : c.kept ? T("cover_kept", "The cover picture is kept as an attachment of the project.") : T("cover_not", "The cover picture was not kept: {reason}", { reason: c.reason }) }));
    if (!bk.label) {
      bk.label = h("input", { id: "imp-label", type: "text", maxlength: "200", "aria-describedby": "imp-label-h" });
      bk.field = h("div", { class: "field" }, [h("label", { for: "imp-label", text: T("label", "Name of this edition") }), bk.label, h("span", { class: "hint", id: "imp-label-h", text: T("label_hint", "The title and the year unless you say otherwise. The original file is kept on this server, as it was received, under this name, and you can download it from Editions. It is not in the Markdown copy, it is never pushed to Git, and it is shared with no one.") })]);
      bk.label.value = e.label || "";
      label = bk.label;
    }
    out.push(bk.field);
    // The files of the book and what each became, built when the list is opened: a long book has thousands.
    var d = h("details", { class: "imp-files" }, [h("summary", { text: T("files_h", "The book's files and what each became ({n})", { n: num((e.files || []).length) }) })]);
    d.addEventListener("toggle", function () {
      if (!d.open || d.querySelector("ul")) return;
      var title = {}; I.nodes().forEach(function (n) { title[n.key] = n.title; });
      var fs = e.files || [];
      d.appendChild(h("ul", { class: "imp-filelist" }, fs.slice(0, 300).map(function (f) {
        return h("li", null, [h("span", { class: "imp-path", text: f.path }), h("span", { class: "why", text: f.skipped ? T("file_skipped", "Not imported: {reason}", { reason: f.skipped }) : T("file_becomes", "Becomes {title}", { title: title[f.key] || f.title || f.path }) })]);
      })));
      if (fs.length > 300) d.appendChild(h("p", { class: "fine", text: T("files_more", "And {n} more files, not listed here.", { n: num(fs.length - 300) }) }));
    });
    out.push(d);
    return out;
  }

  function pdf(p) {
    var r = ((p.report || {}).pdf || {}).report || {}, out = [h("h3", { text: T("pdf_h", "What was read from the PDF") })];
    var src = { outline: T("pdf_outline", "taken from the PDF's outline"), headings: T("pdf_headings", "proposed from headings set larger than the text"), none: T("pdf_nochap", "none could be told apart") }[r.chapter_source];
    out.push(h("dl", { class: "imp-dl" }, [fact(T("pdf_pages", "Pages read"), T("pdf_pages_v", "{n} ({t} with text)", { n: num(r.pages || 0), t: num(r.text_pages || 0) })),
      fact(T("pdf_conf", "Confidence"), T("pdf_conf_v", "{c}% on average; pages under 70%: {l}", { c: r.confidence || 0, l: num(r.low_confidence_pages || 0) })), fact(T("pdf_chapters", "Chapters"), src)]));
    var tick = function (ok, t) { return h("li", { "data-ok": ok ? "true" : "false" }, [h("span", { class: "imp-mark", "aria-hidden": "true" }), h("span", { text: t })]); };
    out.push(h("h4", { text: T("pdf_lost_h", "What could and could not be recovered") }));
    out.push(list([
      tick(!r.table_rows_flattened, r.table_rows_flattened ? T("pdf_tables", "Tables: rows flattened to one paragraph each, cells separated by |: {n}, on pages {p}. A PDF's tables are not rebuilt.", { n: num(r.table_rows_flattened), p: pages(r.table_pages) }) : T("pdf_tables0", "Tables: none found.")),
      tick(!r.images, r.images ? T("pdf_images", "Pictures on the pages, not imported: {n}. The text around them was imported.", { n: num(r.images) }) : T("pdf_images0", "Pictures: none found.")),
      tick(false, T("pdf_links", "Links, underline, colour and type size: not kept. The text of a link stays.")),
      tick(!(r.two_column_pages || (r.ambiguous_column_pages || []).length), (r.two_column_pages || (r.ambiguous_column_pages || []).length) ? T("pdf_cols", "Columns: pages read as two columns, left then right: {n}. Pages that may have columns or a table, read as one column: {p}.", { n: num(r.two_column_pages || 0), p: pages(r.ambiguous_column_pages) || T("none", "none") }) : T("pdf_cols0", "Columns: every page was read as one column. Three or more columns are not read as columns.")),
      tick(!r.picture_only_total, r.picture_only_total ? T("pdf_scan", "Scanned pages, left out because they are pictures with no text: {n}. No character recognition is done, and none is promised.", { n: num(r.picture_only_total) }) : T("pdf_scan0", "Scanned pages: none. Every page has a text layer.")),
      (r.right_to_left_pages || []).length ? tick(false, T("pdf_rtl", "Right-to-left text on pages {p} is kept in the order the file stores it, which is not always the reading order.", { p: pages(r.right_to_left_pages) })) : null,
      r.unmapped_chars_left_out ? tick(false, T("pdf_unmapped", "Characters with no known meaning, left out: {n}.", { n: num(r.unmapped_chars_left_out) })) : null,
      (r.truncated_pages || []).length ? tick(false, T("pdf_cut", "A limit was reached on pages {p}: part of them was not read.", { p: pages(r.truncated_pages) })) : null,
      (r.running_head_lines_removed || r.page_number_lines_removed) ? tick(true, T("pdf_heads", "Running head and page number lines removed: {n}. The rule: {rule}", { n: num((r.running_head_lines_removed || 0) + (r.page_number_lines_removed || 0)), rule: r.running_head_rule || "" })) : null,
      r.contents_lines_removed ? tick(true, T("pdf_toc", "A contents page was removed (pages {p}).", { p: pages(r.contents_pages_removed) })) : null
    ]));
    var bad = r.page_detail || [];
    if (bad.length) {
      out.push(h("details", { class: "imp-files" }, [h("summary", { text: T("pdf_pages_h", "Pages worth a look ({n}, least reliable first)", { n: num(bad.length) }) }),
        h("ul", { class: "imp-filelist" }, bad.slice(0, 40).map(function (x) {
          return h("li", null, [h("span", { class: "imp-path", text: T("pdf_page", "Page {n}: {c}%", { n: x.page, c: x.confidence }) }), h("span", { class: "why", text: (x.notes || []).slice(0, 3).join(" ") })]);
        }))]));
    }
    out.push(h("p", { class: "fine", text: T("pdf_nokeep", "The PDF itself is not kept. Only its checksum is recorded with the import.") }));
    return out;
  }

  // The reasons a change cannot be a suggestion, with the words for each count that is not nothing.
  var WHY = [["inserted_paragraphs", function (n) { return T("tr_ins", "In whole paragraphs a reviewer inserted (the text is kept): {n}.", { n: n }); }], ["deleted_paragraphs", function (n) { return T("tr_del", "In whole paragraphs a reviewer deleted (that text is not kept): {n}.", { n: n }); }],
    ["paragraph_marks", function (n) { return T("tr_mark", "In paragraphs whose mark changed: {n}.", { n: n }); }], ["in_headings", function (n) { return T("tr_head", "In headings: {n}.", { n: n }); }], ["in_tables", function (n) { return T("tr_table", "In tables: {n}.", { n: n }); }], ["in_footnotes", function (n) { return T("tr_note", "In footnotes: {n}.", { n: n }); }],
    ["in_code", function (n) { return T("tr_code", "In code: {n}.", { n: n }); }], ["over_cap", function (n) { return T("tr_cap", "Over the limits for suggestions (300 a document, 1,000 an import): {n}.", { n: n }); }], ["not_placed", function (n) { return T("tr_place", "Not found in the text exactly once: {n}.", { n: n }); }],
    ["refused_by_review_checks", function (n) { return T("tr_refused", "That the review checks would not allow: {n}.", { n: n }); }], ["author_conflicts", function (n) { return T("tr_conflict", "Too close to a change by another author: {n}.", { n: n }); }], ["at_formatting_edges", function (n) { return T("tr_edge", "At the edge of bold or italic text: {n}.", { n: n }); }]];

  function word(p) {
    var d = ((p.report || {}).docx || {}), t = d.tracked || {}, mode = d.changes_mode || "suggest";
    if (!t.revisions && !t.comments_read && !p.suggestions && !p.comments) return [];
    var out = [h("h3", { text: T("tr_h", "Tracked changes and comments") })];
    var say = { suggest: T("tr_suggest", "The chapters hold the original text, as its author left it. Each change a reviewer made waits as a suggestion, which you accept or reject in Review under the reviewer's Word name. Accepting them all gives the reviewers' text."), accept: T("tr_accept", "The chapters hold the final text, with every change accepted. No suggestions are written."), reject: T("tr_reject", "The chapters hold the original text, with every change rejected. No suggestions are written.") }[mode];
    out.push(h("p", { class: "imp-line", text: say }));
    if (mode === "suggest") out.push(h("p", { class: "imp-line", text: T("tr_rule", "Where two reviewers changed the same sentence, the later change is already in the text and is not a suggestion. A whole paragraph a reviewer deleted is gone from the text, and a whole paragraph one inserted is in it.") }));
    var items = [];
    if (mode === "suggest") items.push(T("tr_count", "Suggestions to be written: {n}.", { n: num(p.suggestions || 0) }));
    items.push(T("tr_comments", "Word comments to be written as comments: {n}.", { n: num(p.comments || 0) }));
    if (t.comments_not_imported) items.push(T("tr_comments_lost", "Word comments not imported (in a heading, a table or a footnote, or over the limit): {n}.", { n: num(t.comments_not_imported) }));
    if (mode !== "suggest" && t.revisions) items.push(T("tr_revs", "Tracked changes in the file: {n}.", { n: num(t.revisions) }));
    if (mode === "suggest" && t.taken_as_accepted) {
      items.push(T("tr_taken", "Changes that cannot be suggestions and are taken as the reviewer left them: {n}", { n: num(t.taken_as_accepted) }));
      var why = WHY.filter(function (w) { return t[w[0]]; }).map(function (w) { return h("li", { text: w[1](num(t[w[0]])) }); });
      if (why.length) items.push(h("li", { class: "imp-sub" }, [h("ul", { class: "imp-facts" }, why)]));
    }
    var pairs = t.author_conflict_pairs || [];
    if (mode === "suggest" && pairs.length) items.push(T("tr_pairs", "Reviewers whose changes met, the later one's change is in the text: {pairs}.", { pairs: pairs.map(function (q) { return String(q).split(" / ").map(function (a) { return T("word_name", "{name} (from a Word file)", { name: a }); }).join(" / "); }).join("; ") }));
    if (t.moves) items.push(T("tr_moves", "Tracked moves, each now a deletion and an insertion: {n}.", { n: num(t.moves) }));
    if (t.formatting_changes_ignored) items.push(T("tr_fmt", "Formatting changes ignored: {n}.", { n: num(t.formatting_changes_ignored) }));
    out.push(list(items));
    var au = Object.keys(t.authors || {});
    if (au.length) {
      out.push(h("h4", { text: T("tr_authors", "Who made the changes") }));
      out.push(list(au.slice(0, 20).map(function (a) { return T("word_name", "{name} (from a Word file)", { name: a }) + ", " + T("tr_n", "changes: {n}", { n: num(t.authors[a]) }); })));
    }
    return out;
  }

  // ---- nesting a chapter under another, joining it to one ---------------------------------------------------
  var BOOKS = { epub: 1, pdf: 1, docx: 1, pandoc: 1 };
  // What a chapter can be nested or joined into: everything but itself and what is inside it.
  function targets(n) {
    var no = {}, list = [];
    no[n.key] = 1;
    I.nodes().forEach(function (x) { if (no[x.parent_key]) no[x.key] = 1; if (!no[x.key]) list.push(x); });
    return list;
  }
  var nid = 0;
  H.node = function (n, body, info) {
    if (!BOOKS[layout]) return;
    var t = targets(n), why = "", whyNest = "";
    if (n.kind === "part" || info.kids) why = T("join_part", "A part, or a chapter with chapters inside it, cannot be joined. Make it a chapter first, or move its chapters.");
    else if (!t.length) why = T("join_none", "There is no other chapter to join this into.");
    if (!t.length && !n.parent_key) whyNest = T("nest_none", "There is no other chapter to nest this under.");
    var b = function (a, txt, aria, off) {
      var id = "imp-w" + (++nid);
      return [h("button", { class: "btn small quiet", type: "button", "data-bk": a, "data-key": n.key, "aria-label": aria, text: txt, disabled: !!off, "aria-describedby": off ? id : false }), off ? h("span", { class: "imp-warn imp-why", id: id, text: off }) : null];
    };
    body.push(h("button", { class: "btn small quiet", type: "button", "data-key": n.key, "data-act": "kind", value: n.kind === "part" ? "chapter" : "part", "aria-label": (n.kind === "part" ? T("make_chapter_a", "Make {t} a chapter", { t: n.title }) : T("make_part_a", "Make {t} a part", { t: n.title })), text: n.kind === "part" ? T("make_chapter", "Make a chapter") : T("make_part", "Make a part") }));
    b("parent", T("nest", "Nest under…"), T("nest_a", "Nest {t} under another", { t: n.title }), whyNest).forEach(function (e) { if (e) body.push(e); });
    b("merge", T("join", "Join into…"), T("join_a", "Join {t} into another", { t: n.title }), why).forEach(function (e) { if (e) body.push(e); });
  };
  function picker(btn) {
    var k = btn.dataset.key, mode = btn.dataset.bk, row = btn.parentNode, old = row.querySelector(".imp-to");
    if (old) old.remove();
    var nodes = I.nodes(), self = nodes.filter(function (x) { return x.key === k; })[0], title = {}, opts = [];
    nodes.forEach(function (x) { title[x.key] = x.title; });
    if (mode === "parent") opts.push(["", T("top", "The top level of the book")]);
    targets(self || { key: k }).forEach(function (x) { opts.push([x.key, x.parent_key && title[x.parent_key] ? T("opt_in", "{t} (in {p})", { t: x.title, p: title[x.parent_key] }) : x.title]); });
    var sel = h("select", { "aria-label": mode === "parent" ? T("nest_pick", "Nest under") : T("join_pick", "Join into") });
    opts.forEach(function (o) { sel.appendChild(h("option", { value: o[0], text: o[1] })); });
    var span = h("span", { class: "imp-to" }, [sel,
      h("button", { class: "btn small", type: "button", "data-act": mode, "data-key": k, text: mode === "parent" ? T("nest_go", "Move here") : T("join_go", "Join") }),
      h("button", { class: "btn small quiet", type: "button", "data-bk": "cancel", "data-key": k, text: T("cancel", "Cancel") }),
      mode === "merge" ? h("span", { class: "imp-warn", text: T("join_note", "Its text is added to the end of the one you choose, and this one is removed. Footnotes are renumbered so none collide. You can undo this under Left out of the import.") }) : null]);
    span.opener = btn;
    row.appendChild(span);
    span.addEventListener("keydown", function (e) { if (e.key === "Escape") { e.stopPropagation(); span.remove(); btn.focus(); } });
    sel.focus();
  }
  if (I.out) I.out.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("button");
    if (!b) return;
    if (b.dataset.bk === "cancel") { var s = b.closest(".imp-to"), o = s.opener; s.remove(); if (o) o.focus(); } else if (b.dataset.bk) picker(b);
    else if (b.dataset.act === "parent" || b.dataset.act === "merge") { var sel = b.parentNode.querySelector("select"); b.dataset.to = sel ? sel.value : ""; } // read before the page's own handler acts
  }, true);

  // ---- after an apply that kept an edition or wrote suggestions: the answer is a page of its own, with the result first ---------------
  H.done = function (r, slug, pid) {
    if (!(r.edition_id || r.suggestions || r.comments)) return false;
    var home = "/p/" + slug, first = r.first_document_id ? home + "/d/" + r.first_document_id : home, o = I.out, ed = h("p", { class: "imp-line" }), items = [];
    I.say("");
    if (r.edition_id) {
      ed.textContent = T("done_edn_wait", "Keeping the original file…");
      I.api("/api/v1/projects/" + pid + "/editions/" + r.edition_id, undefined, "GET").then(function (e) { ed.textContent = T("done_edn", "Kept the original file as edition “{label}”. You can download it from Editions.", { label: e.label }); },
        function () { ed.textContent = T("done_edn_plain", "Kept the original file as an edition. You can download it from Editions."); });
      items.push(ed);
    }
    if (r.suggestions) items.push(h("p", { class: "imp-line", text: T("done_sug", "Suggestions waiting in Review, under the reviewers' Word names: {n}. The chapters hold the original text until you accept them.", { n: num(r.suggestions) }) }));
    if (r.comments) items.push(h("p", { class: "imp-line", text: T("done_com", "Word comments written as comments: {n}.", { n: num(r.comments) }) }));
    (r.warnings || []).slice(0, 5).forEach(function (w) { items.push(h("p", { class: "imp-note", "data-kind": "warn", text: w })); });
    var pk = doc.getElementById("imp-pick");
    if (pk) pk.hidden = true; // the answer is the page now, at the top
    var links = [h("a", { class: "btn solid", href: first, text: T("done_open", "Open the book") })];
    if (r.edition_id) links.push(h("a", { class: "btn", href: home + "/editions", text: T("done_editions", "Editions") }));
    if (r.suggestions || r.comments) links.push(h("a", { class: "btn", href: home + "/review", text: T("done_review", "Review") }));
    links.push(h("a", { class: "btn quiet", href: doc.location.pathname, text: T("done_again", "Import another") }));
    var hd = h("h2", { id: "imp-h", tabindex: "-1", text: r.documents === 1 ? T("done_h1", "Imported 1 document") : T("done_h", "Imported {n} documents", { n: num(r.documents) }) });
    o.replaceChildren.apply(o, [hd].concat(items, [h("div", { class: "imp-go" }, links)]));
    o.hidden = false;
    hd.focus();
    return true;
  };
})();
