// Service worker for the Wave PWA. Strategy: NETWORK-FIRST for the app shell, with an
// offline fallback to the cached shell. Network-first (not cache-first) is deliberate
// for a self-hosted app: a deploy is picked up immediately when online — no stale-code
// trap — while an offline launch still loads the shell (then shows "offline" until the
// connection returns; real-time data needs the network).
//
// Data + realtime endpoints (/socket, /presence, /api, /attachments, /login, /whoami)
// are NEVER intercepted — they pass straight through to the network.

const CACHE = "wave-shell-v1";
const SHELL = ["/", "/index.html", "/main.js", "/manifest.webmanifest", "/icon.svg"];

self.addEventListener("install", (e) => {
  e.waitUntil(
    caches
      .open(CACHE)
      .then((c) => c.addAll(SHELL))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

const PASSTHROUGH = /^\/(socket|presence|api|attachments|login|whoami)\b/;

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return; // mutations always hit the network
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return; // cross-origin: let the browser handle it
  if (PASSTHROUGH.test(url.pathname)) return; // realtime/data: never intercept

  // The cache key for any navigation is the shell document.
  const key = req.mode === "navigate" ? "/index.html" : req;
  e.respondWith(
    fetch(req)
      .then((resp) => {
        // Refresh the cached shell copy when online (so offline launches stay current).
        if (resp.ok) {
          const copy = resp.clone();
          caches.open(CACHE).then((c) => c.put(key, copy)).catch(() => {});
        }
        return resp;
      })
      .catch(() => caches.match(key).then((r) => r || caches.match("/index.html"))),
  );
});
