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
    // Not signed in: go to the login endpoint, returning here afterward. Forward
    // a ?user= hint if present (dev login trusts it) for one-click demo/test links.
    const here = location.pathname + location.search;
    const hint = params.get("user");
    const q = new URLSearchParams({ redirect: here });
    if (hint !== null) q.set("user", hint);
    location.href = `/login?${q.toString()}`;
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

  if (debug) {
    const panel = new WaveDebug();
    panel.provider = () => app.getActiveClient();
    document.body.appendChild(panel);
  }
}

void boot();
