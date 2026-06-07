// Presence client: the transient awareness channel (who is here, who is typing,
// which blip they are focused on). A plain-JSON WebSocket to /presence, entirely
// separate from the OT delta socket — presence is lossy, so a dropped message just
// means a slightly stale indicator, never a convergence problem.
//
// The client sends its own state (throttled) and exposes the remote roster via a
// change callback. It reconnects with backoff; the server re-sends the room snapshot
// on each (re)connect, so state re-synchronizes after a blip.

// RemotePresence is one other participant's current awareness state. anchor/focus
// are the caret's rune offsets in blipId (focus = caret/moving end; anchor==focus is
// a collapsed caret; -1 = no caret). They are RAW offsets (not OT-transformed), so a
// remote caret is briefly stale after a local edit until the peer re-publishes.
export interface RemotePresence {
  participant: string;
  typing: boolean;
  blipId: string;
  anchor: number;
  focus: number;
}

// localState is what we publish about ourselves.
interface LocalState {
  typing: boolean;
  blipId: string;
  anchor: number;
  focus: number;
}

// wireUpdate is one server→client message (mirrors presence.Update).
interface WireUpdate {
  participant: string;
  typing: boolean;
  blipId: string;
  anchor: number;
  focus: number;
  online: boolean;
}

const SEND_THROTTLE_MS = 150; // coalesce rapid typing/focus changes
const RECONNECT_MS = 1500;

export class PresenceClient {
  private ws: WebSocket | null = null;
  private closed = false;
  private readonly remote = new Map<string, RemotePresence>();
  private readonly listeners = new Set<() => void>();

  private local: LocalState = { typing: false, blipId: "", anchor: -1, focus: -1 };
  private sent: LocalState = { typing: false, blipId: "", anchor: -1, focus: -1 };
  private sendTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly url: string;

  constructor(url: string) {
    this.url = url;
  }

  /** Open the presence socket (idempotent). */
  connect(): void {
    if (this.closed || this.ws !== null) return;
    let ws: WebSocket;
    try {
      ws = new WebSocket(this.url);
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.ws = ws;
    ws.onopen = (): void => {
      // Re-publish our state on (re)connect so peers see it after a reconnect.
      this.sent = { typing: false, blipId: "", anchor: -1, focus: -1 };
      this.flush();
    };
    ws.onmessage = (ev: MessageEvent): void => {
      let u: WireUpdate;
      try {
        u = JSON.parse(typeof ev.data === "string" ? ev.data : "") as WireUpdate;
      } catch {
        return;
      }
      if (u.online)
        this.remote.set(u.participant, {
          participant: u.participant,
          typing: u.typing,
          blipId: u.blipId,
          anchor: u.anchor ?? -1,
          focus: u.focus ?? -1,
        });
      else this.remote.delete(u.participant);
      this.notify();
    };
    ws.onclose = (): void => {
      this.ws = null;
      this.remote.clear();
      this.notify();
      this.scheduleReconnect();
    };
    ws.onerror = (): void => ws.close();
  }

  /** Close permanently (no reconnect). */
  close(): void {
    this.closed = true;
    if (this.sendTimer !== null) clearTimeout(this.sendTimer);
    if (this.reconnectTimer !== null) clearTimeout(this.reconnectTimer);
    this.ws?.close();
    this.ws = null;
  }

  /** Subscribe to remote-roster changes; returns an unsubscribe function. */
  onChange(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  /** The online peers (excludes self — the server never echoes our own state). */
  remotes(): RemotePresence[] {
    return [...this.remote.values()];
  }

  /** Set our own state (typing + focused blip + caret offsets); sent throttled.
   *  anchor/focus default to -1 (no caret) for callers that only track the blip. */
  setLocal(typing: boolean, blipId: string, anchor = -1, focus = -1): void {
    this.local = { typing, blipId, anchor, focus };
    if (this.sendTimer === null) {
      this.sendTimer = setTimeout(() => {
        this.sendTimer = null;
        this.flush();
      }, SEND_THROTTLE_MS);
    }
  }

  private flush(): void {
    if (this.ws === null || this.ws.readyState !== WebSocket.OPEN) return;
    const l = this.local;
    const s = this.sent;
    if (l.typing === s.typing && l.blipId === s.blipId && l.anchor === s.anchor && l.focus === s.focus) return;
    this.sent = { ...this.local };
    try {
      this.ws.send(JSON.stringify(this.local));
    } catch {
      /* a failed send is recovered by the next flush / reconnect */
    }
  }

  private scheduleReconnect(): void {
    if (this.closed || this.reconnectTimer !== null) return;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, RECONNECT_MS);
  }

  private notify(): void {
    for (const fn of this.listeners) fn();
  }
}
