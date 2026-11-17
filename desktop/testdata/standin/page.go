package main

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// The same policy as the real app's pages: nothing inline, nothing from elsewhere.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; worker-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// workerJS handles navigations the way the real app's service worker does (web/static/sw/worker.js): it claims
// the page and answers a navigation with its own fetch of the same request. With STANDIN_SW=1 the pages register
// it, so a test can see how the webview describes a navigation that went through a worker.
const workerJS = `self.addEventListener("install", function (e) { self.skipWaiting(); });
self.addEventListener("activate", function (e) { e.waitUntil(self.clients.claim()); });
self.addEventListener("fetch", function (e) {
  if (e.request.mode === "navigate") e.respondWith(fetch(e.request).catch(function () { return new Response("offline", { status: 503 }); }));
});`

const style = `body{font:16px/1.5 system-ui,sans-serif;margin:2rem;max-width:44rem;color:#1c1c1c;background:#fbfaf7}
a,button{display:inline-block;margin:.2rem .4rem .2rem 0}pre{border:1px solid #bbb;padding:.6rem;white-space:pre-wrap}
@media (prefers-color-scheme:dark){body{color:#eee;background:#161616}}`

// probeJS runs on every stand-in page. It tries to reach the shell the way a hostile page script would, tries
// the browser dialogs the real app uses, and tells the stand-in what happened. With ?do=<action> it also
// performs one action without a click, so a script can drive the checks.
const probeJS = `(function () {
  var out = { href: location.pathname, isTauri: !!window.isTauri, internals: typeof window.__TAURI_INTERNALS__, global: typeof window.__TAURI__, calls: {} };
  var pending = 0;
  function send() { fetch("/standin/probe", { method: "POST", body: JSON.stringify(out) }); var p = document.getElementById("probe"); if (p) p.textContent = JSON.stringify(out, null, 1); }
  function attempt(name, args) {
    var T = window.__TAURI_INTERNALS__;
    if (!T || typeof T.invoke !== "function") { out.calls[name] = "no invoke function"; return; }
    pending++;
    var done = function (label) { return function (v) { out.calls[name] = label + ": " + String(v && v.message ? v.message : v).slice(0, 160); if (--pending === 0) send(); }; };
    try { T.invoke(name, args || {}).then(done("ALLOWED"), done("refused")); } catch (e) { done("threw")(e); }
  }
  attempt("plugin:app|version");
  attempt("plugin:app|name");
  attempt("plugin:window|close", { label: "main" });
  attempt("plugin:window|set_title", { label: "main", value: "pwned" });
  attempt("plugin:webview|create_webview_window", { options: { label: "x", url: "https://example.com" } });
  attempt("plugin:event|emit", { event: "x", payload: 1 });
  attempt("plugin:event|listen", { event: "x", target: { kind: "Any" }, handler: 1 });
  attempt("plugin:path|resolve_directory", { directory: 21 });
  attempt("plugin:updater|check");
  attempt("plugin:deep-link|get_current");
  attempt("plugin:opener|open_path", { path: "/etc/passwd" });
  attempt("plugin:opener|open_url", { url: "https://example.com" });
  attempt("plugin:shell|execute", { program: "sh", args: ["-c", "id"] });
  attempt("plugin:fs|read_text_file", { path: "/etc/passwd" });
  attempt("plugin:dialog|message", { message: "x" });
  attempt("restart");
  out.confirmSource = String(window.confirm).indexOf("[native code]") >= 0 ? "native" : "replaced";
  var q = new URLSearchParams(location.search), act = q.get("do");
  if (act === "confirm") { out.confirmResult = String(window.confirm("Stand-in: choose OK or Cancel")); }
  if (pending === 0) send(); else setTimeout(send, 1500);
  var acts = {
    external: function () { location.href = "https://example.com/from-a-script"; },
    open: function () { out.windowOpen = String(window.open("https://example.com/window-open", "_blank")); send(); },
    openlocal: function () { window.open("/settings", "_blank"); },
    localhost: function () { location.href = "http://localhost:" + location.port + "/foreign-localhost"; },
    redirect: function () { location.href = "/redirect-out"; },
    js: function () { location.href = "javascript:document.title='pwned'"; },
    file: function () { location.href = "file:///etc/passwd"; },
    scheme: function () { location.href = "x-apple.systempreferences:com.apple.preference.security"; },
    download: function () { location.href = "/download/book.epub"; },
    pdf: function () { location.href = "/download/book.pdf"; },
    pdfattr: function () { var a = document.createElement("a"); a.href = "/download/book.pdf"; a.download = ""; document.body.appendChild(a); a.click(); },
    evilname: function () { location.href = "/download/evil"; },
    blob: function () { var a = document.createElement("a"); a.href = URL.createObjectURL(new Blob(["# From a blob\n"], { type: "text/markdown" })); a.download = "from-a-blob.md"; document.body.appendChild(a); a.click(); },
    print: function () { window.print(); }
  };
  if (acts[act]) setTimeout(acts[act], 300);
  document.addEventListener("click", function (e) { var b = e.target.closest("[data-do]"); if (b && acts[b.dataset.do]) acts[b.dataset.do](); });
  var drop = document.getElementById("drop");
  if (drop) {
    document.addEventListener("dragover", function (e) { e.preventDefault(); });
    document.addEventListener("drop", function (e) { e.preventDefault(); var n = []; for (var i = 0; i < e.dataTransfer.files.length; i++) n.push(e.dataTransfer.files[i].name + " (" + e.dataTransfer.files[i].size + " bytes)"); drop.textContent = "Dropped: " + n.join(", "); out.dropped = n; send(); });
  }
})();`

func page(w http.ResponseWriter, title, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><link rel="stylesheet" href="/standin.css"><script src="/standin.js" defer></script></head><body><h1>%s</h1>%s<h2>What the page script could reach</h2><pre id="probe">…</pre></body></html>`,
		html.EscapeString(title), html.EscapeString(title), body)
}

func pages(mux *http.ServeMux) {
	mux.HandleFunc("GET /standin.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		io.WriteString(w, style)
	})
	mux.HandleFunc("GET /standin.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		io.WriteString(w, probeJS)
		if os.Getenv("STANDIN_SW") == "1" {
			io.WriteString(w, `;if (navigator.serviceWorker) navigator.serviceWorker.register("/sw.js", { scope: "/" });`)
		}
	})
	mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		io.WriteString(w, workerJS)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		page(w, "Stand-in library", `<p>This is the stand-in server, not Scríobh.</p>
<p><a href="/import">Import</a> <a href="/settings">Settings</a> <a href="/p/demo">A project</a> <a href="/p/demo/d/doc_1">A document</a></p>
<h2>Links a manuscript might hold</h2>
<p><a href="https://example.com/an-outside-link">An outside link</a>
<a href="https://example.com/new-tab" target="_blank" rel="noopener">An outside link in a new tab</a>
<a href="mailto:someone@example.com">A mail link</a>
<a href="file:///etc/passwd">A file link</a>
<a href="javascript:document.title='pwned'">A script link</a>
<a href="x-apple.systempreferences:com.apple.preference.security">A system link</a>
<a href="/redirect-out">A redirect out</a></p>
<h2>Actions</h2>
<p><button type="button" data-do="open">window.open outside</button><button type="button" data-do="openlocal">window.open inside</button>
<a href="/download/book.epub">Download an EPUB</a><a href="/download/book.pdf">Download a PDF</a><a href="/download/evil">Download with a hostile name</a>
<button type="button" data-do="blob">Download from a blob</button><button type="button" data-do="print">window.print()</button>
<a href="/?do=confirm">window.confirm()</a></p>
<h2>Drop a file here</h2><p id="drop">Nothing dropped yet.</p><p><input type="file" aria-label="Choose a file"></p>`)
	})
	for _, p := range []string{"/import", "/settings", "/p/{slug}", "/p/{slug}/compile", "/p/{slug}/d/{id}"} {
		mux.HandleFunc("GET "+p, func(w http.ResponseWriter, r *http.Request) {
			// A deep link can only name a page; /p/do-<action> is how a script asks the library page to act.
			if act, ok := strings.CutPrefix(r.PathValue("slug"), "do-"); ok && r.PathValue("id") == "" {
				http.Redirect(w, r, "/?do="+url.QueryEscape(act), http.StatusSeeOther)
				return
			}
			page(w, "Stand-in page "+r.URL.Path, `<p><a href="/">Back to the library</a></p><p id="drop">Nothing dropped yet.</p><p><input type="file" aria-label="Choose a file"></p>`)
		})
	}
	mux.HandleFunc("GET /redirect-out", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/redirected", http.StatusFound)
	})
	mux.HandleFunc("GET /download/{name}", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		switch r.PathValue("name") {
		case "book.epub":
			h.Set("Content-Type", "application/epub+zip")
			h.Set("Content-Disposition", `attachment; filename="An Leabhar.epub"`)
			io.WriteString(w, "PK\x03\x04 a stand-in book\n")
		case "book.pdf":
			h.Set("Content-Type", "application/pdf")
			h.Set("Content-Disposition", `attachment; filename="An Leabhar.pdf"`)
			io.WriteString(w, "%PDF-1.4\n% a stand-in, not a real PDF\n")
		case "evil":
			h.Set("Content-Type", "application/octet-stream")
			h.Set("Content-Disposition", "attachment; filename=\"../../../../tmp/.evil‮gpj.command\"")
			io.WriteString(w, "#!/bin/sh\necho no\n")
		default:
			http.NotFound(w, r)
		}
	})
}
