// A WebSocket wrapper that reconnects automatically with exponential-ish backoff.

export type SocketState = 'connecting' | 'open' | 'closed';

export interface ReconnectingSocketOptions {
  /** Path relative to the current origin, e.g. "/api/events". */
  path: string;
  binaryType?: BinaryType;
  onOpen?: () => void;
  onMessage?: (ev: MessageEvent) => void;
  /** Called with the close code whenever an established/attempted connection closes. */
  onClose?: (code: number) => void;
  onStateChange?: (state: SocketState) => void;
}

/** Delays (ms) between reconnect attempts; the last value repeats. */
const BACKOFF = [500, 1000, 2000, 5000, 10000, 15000];
/**
 * The delay while the tab is hidden or the browser is offline: a board of twenty terminals left
 * open overnight with the hub away tried more than once a second. Coming back (the tab shown,
 * the network up) tries at once.
 */
const AWAY_DELAY = 60_000;

/** Sockets waiting to reconnect. */
const waiting = new Set<ReconnectingSocket>();
let watching = false;

function watchReturn() {
  if (watching || typeof window === 'undefined') return;
  watching = true;
  const back = () => {
    if (document.hidden || navigator.onLine === false) return;
    for (const s of [...waiting]) s.reconnectNow();
  };
  window.addEventListener('online', back);
  document.addEventListener('visibilitychange', back);
}

function away(): boolean {
  return (typeof document !== 'undefined' && document.hidden) || (typeof navigator !== 'undefined' && navigator.onLine === false);
}

/** Build an absolute ws(s):// URL for a same-origin path (wss when the page is https). */
export function wsUrl(path: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}${path}`;
}

export class ReconnectingSocket {
  private ws: WebSocket | null = null;
  private attempt = 0;
  private timer: number | undefined;
  private stopped = false;
  private openedAt = 0;
  private opts: ReconnectingSocketOptions;
  state: SocketState = 'connecting';

  constructor(opts: ReconnectingSocketOptions) {
    this.opts = opts;
    this.connect();
  }

  /** True when the underlying socket is open and can send. */
  get isOpen(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  /** Send a text frame (all client → hub frames are JSON text). Returns false if not open. */
  send(data: string): boolean {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return false;
    this.ws.send(data);
    return true;
  }

  sendJSON(value: unknown): boolean {
    return this.send(JSON.stringify(value));
  }

  /** Force an immediate reconnect (e.g. after the network comes back). */
  reconnectNow(): void {
    if (this.stopped) return;
    window.clearTimeout(this.timer);
    waiting.delete(this);
    this.attempt = 0;
    if (this.ws) {
      this.ws.close();
    } else {
      this.connect();
    }
  }

  /** Close permanently; no further reconnects. */
  close(): void {
    this.stopped = true;
    window.clearTimeout(this.timer);
    waiting.delete(this);
    if (this.ws) {
      const ws = this.ws;
      this.ws = null;
      ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
      ws.close();
    }
    this.setState('closed');
  }

  private setState(s: SocketState) {
    if (this.state === s) return;
    this.state = s;
    this.opts.onStateChange?.(s);
  }

  private connect() {
    waiting.delete(this);
    if (this.stopped) return;
    this.setState('connecting');
    let ws: WebSocket;
    try {
      ws = new WebSocket(wsUrl(this.opts.path));
    } catch {
      this.scheduleReconnect();
      return;
    }
    if (this.opts.binaryType) ws.binaryType = this.opts.binaryType;
    this.ws = ws;

    ws.onopen = () => {
      this.openedAt = Date.now();
      this.setState('open');
      this.opts.onOpen?.();
    };
    ws.onmessage = (ev) => this.opts.onMessage?.(ev);
    ws.onerror = () => {
      // onclose always follows; reconnect is handled there.
    };
    ws.onclose = (ev) => {
      if (this.ws !== ws) return;
      this.ws = null;
      // Only reset the backoff if the connection was healthy for a while,
      // so a server that accepts and immediately closes doesn't cause a tight loop.
      if (this.openedAt && Date.now() - this.openedAt > 5000) this.attempt = 0;
      this.openedAt = 0;
      this.opts.onClose?.(ev.code);
      this.scheduleReconnect();
    };
  }

  private scheduleReconnect() {
    if (this.stopped) return;
    this.setState('connecting');
    let base = BACKOFF[Math.min(this.attempt, BACKOFF.length - 1)];
    if (away()) base = Math.max(base, AWAY_DELAY);
    this.attempt++;
    const jitter = Math.random() * 0.25 * base;
    watchReturn();
    waiting.add(this);
    this.timer = window.setTimeout(() => this.connect(), base + jitter);
  }
}
