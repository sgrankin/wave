// Browser entry point: discover the signed-in identity from the session cookie
// (via /whoami), then mount the <wave-app> shell (inbox/search on the left, the
// active wave on the right). The active wave is read from the ?wave= query param
// by the shell and updated as the user navigates.
//
//   /                        → inbox, no wave open
//   /?wave=example.com%2Fw%2Bxyz%2F~%2Fconv%2Broot   → that wave open
//
// The wave id must be percent-encoded — it contains '+' and '/', and a literal
// '+' in a query string decodes to a space. Identity comes from the session
// cookie (set by /login), not the URL. On a 401 the page sends the user to /login.

import "./wave-app.ts";
import type { WaveApp } from "./wave-app.ts";
import { ensureSelectionToolbar } from "./selection-toolbar.ts";
import { setDebug } from "../wave/debug.ts";
import { WaveDebug } from "./debug-panel.ts";

const params = new URLSearchParams(location.search);

// `?debug=1` turns on the client's delta-flow console trace and the state overlay.
const debug = params.get("debug") === "1";
if (debug) setDebug(true);

async function boot(): Promise<void> {
  let resp: Response;
  try {
    resp = await fetch("/whoami", { credentials: "same-origin" });
  } catch (e) {
    document.body.textContent = `auth error: ${String(e)}`;
    return;
  }
  if (resp.status === 401) {
    // Not signed in: render the root as a stable LANDING page with a sign-in button,
    // rather than auto-redirecting to /login. This keeps "/" a real page in every
    // state, so it can be bookmarked / added to the home screen while logged out (the
    // PWA icon then targets "/", not a transient login redirect). The button does a
    // full-page navigation to login — mirroring how OIDC redirects to an IdP (browser
    // chrome on the credential page is correct; the root just hosts the entry points).
    showLanding(params.get("user"));
    return;
  }
  if (!resp.ok) {
    document.body.textContent = `auth error: ${resp.status}`;
    return;
  }
  let address: string;
  try {
    const body = (await resp.json()) as { address?: string };
    if (typeof body.address !== "string" || body.address === "") {
      throw new Error("no address in /whoami response");
    }
    address = body.address;
  } catch (e) {
    document.body.textContent = `auth error: ${String(e)}`;
    return;
  }

  const wsProto = location.protocol === "https:" ? "wss:" : "ws:";
  const url = `${wsProto}//${location.host}/socket`;

  const app = document.createElement("wave-app") as WaveApp;
  app.wsUrl = url;
  app.user = address;
  document.body.appendChild(app);

  // Mount the floating selection toolbar once, at the document root (so its fixed
  // positioning is anchored to the viewport, not the app subtree).
  ensureSelectionToolbar();

  if (debug) {
    const panel = new WaveDebug();
    panel.provider = () => app.getActiveClient();
    document.body.appendChild(panel);
  }
}

// showLanding renders the stable logged-out root: a sign-in entry point. The button
// NAVIGATES to /login (the dev name form now; per-method IdP buttons under OIDC
// later) — credentials are entered on that page, with the address bar visible. `hint`
// forwards a ?user= for one-click dev/test links. Returning to "/" after login mounts
// the app. Plain DOM (this is the pre-app bootstrap, before the Lit shell loads).
function showLanding(hint: string | null): void {
  const q = new URLSearchParams({ redirect: location.pathname + location.search });
  if (hint !== null) q.set("user", hint);
  const loginHref = `/login?${q.toString()}`;

  document.body.replaceChildren();
  const style = document.createElement("style");
  style.textContent = `
    .landing { min-height: 100dvh; display: flex; flex-direction: column; align-items: center;
      justify-content: center; gap: 14px; padding: 24px; text-align: center; }
    .landing h1 { font: 600 28px system-ui, sans-serif; margin: 0; color: #4060c0; }
    .landing p { font: 15px system-ui, sans-serif; color: #555; margin: 0; max-width: 22em; }
    .landing a.signin { font: 600 16px system-ui, sans-serif; text-decoration: none; margin-top: 6px;
      padding: 11px 22px; border-radius: 8px; background: #4060c0; color: #fff; }
    .landing a.signin:hover { background: #36509c; }`;
  const wrap = document.createElement("div");
  wrap.className = "landing";
  const h1 = document.createElement("h1");
  h1.textContent = "Wave";
  const tag = document.createElement("p");
  tag.textContent = "Real-time collaborative editor";
  const signin = document.createElement("a");
  signin.className = "signin";
  signin.href = loginHref;
  signin.textContent = "Sign in";
  wrap.append(h1, tag, signin);
  document.body.append(style, wrap);
}

void boot();
