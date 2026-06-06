// Browser entry point: read the wavelet + user from the URL query, mount a
// <wave-editor> pointed at the same-origin WebSocket endpoint.
//
//   /?user=alice@example.com&wave=example.com/w+demo/~/conv+root
//
// The WebSocket endpoint is /socket on the same origin (so it shares host/port
// with the page, and later the auth cookie). Defaults make it runnable bare.

import "./wave-editor.ts";
import type { WaveEditor } from "./wave-editor.ts";

const params = new URLSearchParams(location.search);
const user = params.get("user") ?? "user@example.com";
const wave = params.get("wave") ?? "example.com/w+demo/~/conv+root";
const wsProto = location.protocol === "https:" ? "wss:" : "ws:";
const url = `${wsProto}//${location.host}/socket`;

const editor = document.createElement("wave-editor") as WaveEditor;
editor.url = url;
editor.wave = wave;
editor.user = user;
document.body.appendChild(editor);
