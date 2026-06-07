// Browser entry point: discover the signed-in identity from the session cookie
// (via /whoami), then mount a <wave-conversation> for the wave named in the URL,
// pointed at the same-origin WebSocket endpoint.
//
//   /?wave=example.com%2Fw%2Bdemo%2F~%2Fconv%2Broot
//
// The wave id must be percent-encoded — it contains '+' and '/', and a literal
// '+' in a query string decodes to a space (yielding a different, invalid name;
// the server rejects it). Identity comes from the session cookie (set by /login),
// not the URL. On a 401
// the page sends the user to /login and returns. As a convenience for demos and
// tests, a ?user=<address> hint is forwarded to /login (dev mode trusts it); the
// cookie remains authoritative once set. The WebSocket endpoint is /socket on the
// same origin (so it shares host/port and the auth cookie).

import "./wave-conversation.ts";
import type { WaveConversation } from "./wave-conversation.ts";
import { setDebug } from "../wave/debug.ts";
import { WaveDebug } from "./debug-panel.ts";

const params = new URLSearchParams(location.search);
const wave = params.get("wave") ?? "example.com/w+demo/~/conv+root";

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

  const conv = document.createElement("wave-conversation") as WaveConversation;
  conv.url = url;
  conv.wave = wave;
  conv.user = address;
  document.body.appendChild(conv);

  if (debug) {
    const panel = new WaveDebug();
    panel.provider = () => conv.getClient();
    document.body.appendChild(panel);
  }
}

void boot();
