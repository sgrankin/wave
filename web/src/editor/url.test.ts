import { test } from "node:test";
import assert from "node:assert/strict";

import { normalizeUrl, safeHref } from "./url.ts";

// Control characters built via fromCharCode so the source stays pure ASCII (no
// literal control bytes in string literals to mangle a newline into a syntax error).
const TAB = String.fromCharCode(9);
const NL = String.fromCharCode(10);
const CR = String.fromCharCode(13);
const NUL = String.fromCharCode(0);
const C1 = String.fromCharCode(1);

test("normalizeUrl prefixes https:// only when there is no scheme", () => {
  assert.equal(normalizeUrl("example.com"), "https://example.com");
  assert.equal(normalizeUrl("example.com/a/b"), "https://example.com/a/b");
  assert.equal(normalizeUrl("https://example.com"), "https://example.com");
  assert.equal(normalizeUrl("http://example.com"), "http://example.com");
  assert.equal(normalizeUrl("mailto:a@b.com"), "mailto:a@b.com");
});

test("safeHref allows http/https/mailto and scheme-less/relative", () => {
  for (const ok of [
    "https://example.com/x",
    "http://example.com",
    "mailto:a@b.com",
    "example.com/x",
    "/relative/path",
    "#anchor",
    "?q=1",
  ]) {
    assert.equal(safeHref(ok), ok, `expected ${JSON.stringify(ok)} allowed`);
  }
});

test("safeHref rejects script-capable schemes, including obfuscated ones", () => {
  // The XSS gate: a malicious href can arrive in a remote peer's annotation, and the
  // render side is the only defense. The tab/newline/CR/C0-control variants are
  // stripped by the browser's URL parser to recover the dangerous scheme, so we must
  // detect the scheme on the same scrubbed basis.
  const bad = [
    "javascript:alert(1)",
    "JaVaScript:alert(1)",
    "java" + TAB + "script:alert(1)", // tab inside the scheme
    "javascript" + TAB + ":alert(1)",
    "java" + NL + "script:alert(1)", // newline inside the scheme
    "java" + CR + "script:alert(1)", // CR inside the scheme
    C1 + "javascript:alert(1)", // leading C0 control
    " javascript:alert(1)", // leading space
    NL + "javascript:alert(1)", // leading newline
    NUL + "javascript:alert(1)", // leading NUL
    "  " + TAB + " javascript:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "vbscript:msgbox(1)",
  ];
  for (const b of bad) {
    assert.equal(safeHref(b), null, `expected ${JSON.stringify(b)} rejected`);
  }
});
