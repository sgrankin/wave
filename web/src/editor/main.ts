// Browser entry point: read the wavelet + user from the URL query, mount a
// <wave-conversation> pointed at the same-origin WebSocket endpoint.
//
//   /?user=alice@example.com&wave=example.com/w+demo/~/conv+root
//
// The WebSocket endpoint is /socket on the same origin (so it shares host/port
// with the page, and later the auth cookie). Defaults make it runnable bare.

import "./wave-conversation.ts";
import type { WaveConversation } from "./wave-conversation.ts";
import { setDebug } from "../wave/debug.ts";
import { WaveDebug } from "./debug-panel.ts";

const params = new URLSearchParams(location.search);
const user = params.get("user") ?? "user@example.com";
const wave = params.get("wave") ?? "example.com/w+demo/~/conv+root";
const wsProto = location.protocol === "https:" ? "wss:" : "ws:";
const url = `${wsProto}//${location.host}/socket`;

// `?debug=1` turns on the client's delta-flow console trace and the state overlay.
const debug = params.get("debug") === "1";
if (debug) setDebug(true);

const conv = document.createElement("wave-conversation") as WaveConversation;
conv.url = url;
conv.wave = wave;
conv.user = user;
document.body.appendChild(conv);

if (debug) {
  const panel = new WaveDebug();
  panel.provider = () => conv.getClient();
  document.body.appendChild(panel);
}
