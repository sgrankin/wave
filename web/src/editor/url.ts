// URL helpers for editor links — pure (no DOM), so they unit-test under node:test.
//
// A link href is user-controlled and can arrive from a REMOTE peer's link annotation,
// so safeHref is a security boundary, not a convenience: it is the only thing standing
// between a malicious `javascript:` annotation and code execution on a viewer's client
// (lit-html does not sanitize hrefs, the server does not inspect annotation values, and
// there is no CSP). Keep it strict.

// normalizeUrl prefixes a scheme-less URL with https:// so an href like "example.com"
// is treated as an absolute link, not a path relative to the app. A value that already
// carries a scheme (http:, https:, mailto:, …) is left as typed.
export function normalizeUrl(url: string): string {
  return /^[a-z][a-z0-9+.-]*:/i.test(url) ? url : `https://${url}`;
}

// safeHref returns url if it is safe to put in an href, else null. Only schemes that
// cannot execute script are allowed (http/https/mailto, or a scheme-less/relative
// value); a javascript:/data:/vbscript:/… URL returns null so the caller renders inert
// text instead of a live link.
//
// The scheme is detected on a SCRUBBED copy that mirrors what a browser's URL parser
// ignores — it removes ALL tab/newline/CR (anywhere) and trims leading C0 controls and
// space — so obfuscations like "java\tscript:" or "\x01javascript:" cannot hide a
// dangerous scheme from the regex while still parsing as javascript: in the browser.
export function safeHref(url: string): string | null {
  // eslint-disable-next-line no-control-regex
  const scrubbed = url.replace(/[\t\n\r]/g, "").replace(/^[\x00-\x20]+/, "");
  const m = /^([a-z][a-z0-9+.-]*):/i.exec(scrubbed);
  if (m === null) return url; // no scheme → relative/normalized; safe
  const scheme = (m[1] ?? "").toLowerCase();
  return scheme === "http" || scheme === "https" || scheme === "mailto" ? url : null;
}
