/* Scríobh service worker. The server serves it at /sw.js with a line of
   configuration in front (CONFIG: version, the static files to keep, the
   offline page), so its scope is the whole origin and its cache is named for
   the build.

   What it keeps: the application shell, that is every file under /static (the
   styles, scripts, fonts and icons, all public, none of it per person) and one
   fixed offline page that is the same for everybody.

   What it never keeps: a page, a fragment or an API response. Those carry the
   signed-in person's name, token and manuscript, and a cache is shared by
   whoever uses this browser next. A page the network cannot reach is answered
   with the offline page; unsaved text is held by the writing view itself, in
   this browser's own storage, and is retried when the network is back.

   Updating: a new worker installs beside the old one and waits. It takes over
   only when the page asks (the person said yes to the "new version" notice),
   and the page then reloads, so old markup never meets new files. */
"use strict";
var CACHE = "scriobh-static-" + CONFIG.version;

self.addEventListener("install", function (e) {
  e.waitUntil(caches.open(CACHE).then(function (cache) {
    // addAll is all or nothing: one missing file fails the install, so a half
    // shell is never activated. The requests carry no cookies.
    var reqs = CONFIG.static.map(function (u) { return new Request(u, { credentials: "omit", cache: "reload" }); });
    reqs.push(new Request(CONFIG.offline, { credentials: "omit", cache: "reload", redirect: "error" }));
    return cache.addAll(reqs);
  }));
});

self.addEventListener("activate", function (e) {
  e.waitUntil(caches.keys().then(function (names) {
    return Promise.all(names.filter(function (n) { return n.indexOf("scriobh-") === 0 && n !== CACHE; }).map(function (n) { return caches.delete(n); }));
  }).then(function () { return self.clients.claim(); }));
});

self.addEventListener("message", function (e) {
  if (e.data && e.data.type === "SKIP_WAITING") self.skipWaiting();
});

self.addEventListener("fetch", function (e) {
  var req = e.request;
  if (req.method !== "GET") return;
  var url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname.indexOf("/s/") === 0) return; // a share link's page is a stranger's: never answered, kept or replaced from here
  if (url.pathname.indexOf("/static/") === 0) {
    // Public files, named by content: the cache first, the network to fill a gap.
    e.respondWith(caches.open(CACHE).then(function (cache) {
      return cache.match(req).then(function (hit) {
        if (hit) return hit;
        return fetch(req).then(function (res) {
          if (res.ok && res.status === 200) cache.put(req, res.clone());
          return res;
        });
      });
    }));
    return;
  }
  // Sign-in navigations are never re-sent from here: a re-sent one is a fetch, and the issuer answers the real thing.
  if (/^\/(auth|desktop|desktop-idp)\//.test(url.pathname)) return;
  if (req.mode === "navigate") {
    // Always the network. Only when it cannot be reached is anything else shown.
    e.respondWith(fetch(req).catch(function () {
      return caches.open(CACHE).then(function (cache) { return cache.match(CONFIG.offline); }).then(function (hit) { return hit || Response.error(); });
    }));
  }
  // Everything else (the API, fragments, the manifest, icons) goes to the network untouched.
});
