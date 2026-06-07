# QA & Styling Backlog

Status: **in progress** (2026-06-07). Captured from a user demo session, now being
worked as the QA & styling pass. The functional stack is complete and tested (see
[02-porting-plan](architecture/02-porting-plan.md)); these are correctness/polish
defects found by eye, not regressions in the OT/convergence core.

Progress:
- **B3 (caret) — DONE.** Root-caused and fixed; see the "blip-view caret mapping"
  notes inline below. Regression guards: `web/src/editor/blip-caret.test.ts`
  (component) + `web/test/caret.browser.test.ts` (real keyboard/mouse e2e).
- **B1 (leading gap) — DONE.** Same root cause as the B3 click-trigger (a leading
  whitespace text node from template indentation rendered as visible space under
  `white-space:pre-wrap` on the container); fixed alongside B3.
- **B2 (width) — open.** Next.

Verify with two browser clients against a real `waved` (`make release && ./waved
-ws 127.0.0.1:8140 -auth dev`), or extend `web/test/*.browser.test.ts`.

## Reported issues (from the demo)

### B1 — blip editor: first line of text starts partway down the box (alignment) — RESOLVED 2026-06-07
**Resolution:** confirmed cause was a leading whitespace text node (`"\n        "`,
from the template indentation around the paragraph map) rendered as a visible ~one-line
gap because `white-space:pre-wrap` was on the `.blip-doc` container. Fixed by moving
`pre-wrap` onto `.blip-doc .para` and removing the template indentation. Commit
`qzymrxvm`. (NOT a spurious projection paragraph — `project()` of `<body><line/>…` is
clean; original hypothesis below.)
- **Symptom:** in a blip editor the first line of text renders well below the top
  of the box, as if there is leading vertical space / an empty leading line.
- **Suspect:** the projection of `<body><line/>text</body>` may emit a leading
  *empty* paragraph (text-before-first-`<line>`) in addition to the real line, or
  the `EMPTY_PARAGRAPH` fallback / `.para { min-height: 1.6em }` stacks. Check
  `web/src/editor/blipdoc.ts` (how `<line>` markers split paragraphs — is there a
  spurious paragraph for the content *before* the first `<line>`?) and the
  `.blip-doc`/`.para` CSS in `web/src/editor/blip-view.ts`.
- **Triage first:** log `proj.paragraphs` for a fresh single-line blip; expect
  exactly one paragraph with `textStart` just after the `<line>` marker, no leading
  empty para.

### B2 — blip box does not grow with width (stays narrow)
- **Symptom:** the blip editor card stays a fixed narrow width and does not fill the
  conversation pane as the window widens.
- **Suspect (most likely):** the custom elements `<wave-thread>` / `<wave-blip>`
  have no `display: block`, so they default to `display: inline` and shrink-wrap —
  the `display:block` `<blip-view>` inside an inline parent never gets full width.
  `<blip-view>` / `.blip-doc` also set no `width: 100%`. Check
  `web/src/editor/wave-conversation.ts` STYLES (the `wave-thread`/`wave-blip`/
  `blip-view` rules) — give the container elements `display:block; width:100%`.
- **Triage first:** inspect computed `display` on `wave-thread`/`wave-blip` in the
  browser; confirm they're `inline`.

### B3 — multi-line caret mapping: typing on line 2 inserts at the start of line 1 (CORRECTNESS) — RESOLVED 2026-06-07
**Resolution:** the actual trigger was the B1 leading-whitespace gap above line 1:
clicking it parked the caret in a stray text node that `domToOffset` mapped (via the
old `nearestParagraphIndex`) to line 1's start. A second latent bug: the `node===root`
branch treated a child-node index as a paragraph index (the `Math.min` clamp masked it
for ≤2 lines, mis-mapped with 3+). Fixed by resolving any out-of-paragraph caret to the
nearest paragraph boundary in document order, and by removing the leading gap (B1).
Commit `qzymrxvm`. (Original hypothesis below kept for the record.)
- **Repro:** type a line → Enter → click into the (now empty) second line → type →
  the text appears at the **beginning of the first line** instead of on line 2.
- **Suspect:** `domToOffset` in `web/src/editor/blip-view.ts` (~L327-355). Clicking
  into the empty second line likely parks the browser caret either on the editable
  root (`node === root` branch, L333 — `domOffset` may not index the intended
  paragraph) or in a stray node outside any `.para` (`el === null` branch, L347 —
  `nearestParagraphIndex` may return paragraph 0, mapping to `textStart` 0 = doc
  start). Either path maps the line-2 caret to offset 0 → insert lands at line 1.
  Cross-check the line-marker offset accounting in `blipdoc.ts` (a paragraph's
  `textStart` must account for the preceding `<line>` element item).
- **This is the priority fix.** Reproduce with a Playwright test first (type/Enter/
  click line 2 by coordinate or `.para:nth-child(2)`/type, assert the model has two
  distinct lines and the second blip text is "line2text", not prepended to line 1),
  then fix the mapping, then keep the test as the regression guard. It touches the
  caret-mapping invariant the whole editor rests on — review the fix from clean context.

## Proposed workflow shape (when we run it)

A multi-phase QA + styling pass (the functional core is done, so this is hardening):
1. **Editor caret/correctness suite** — reproduce B3 + adversarially probe the
   contenteditable: multi-line edits, click-to-caret on every line/empty line, caret
   after Enter / Backspace-merge / mid-paragraph, selection across lines, IME/paste,
   around embedded elements (reply anchors, images). Failing browser tests first,
   then fix the DOM↔offset mapping, then lock in.
2. **Responsive layout & styling audit** — B1, B2, and a sweep: the editor fills its
   pane and grows with width; long content/words wrap; the two-pane shell at narrow
   widths; roster/inbox/presence overflow; consistent spacing/typography. Screenshot
   diffs at a few widths.
3. **Cross-cutting polish** — focus/hover states, empty states, the History scrubber
   and presence bar visual fit, dark-text contrast/a11y basics, console-error sweep.
4. **Synthesis** — fix list ranked by severity (B3 = correctness, first), each
   browser-verified; update this doc as items close.

Keep the modern CSS direction (the user likes it) — this is refinement, not a redesign.
