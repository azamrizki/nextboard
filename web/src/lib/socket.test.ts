// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ReconnectingSocket, wsUrl, type SocketState } from './socket';

class FakeWebSocket {
  static OPEN = 1;
  static all: FakeWebSocket[] = [];
  readyState = 0;
  binaryType = '';
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: unknown) => void) | null = null;
  onclose: ((e: { code: number }) => void) | null = null;
  onerror: (() => void) | null = null;
  url: string;
  constructor(url: string) {
    this.url = url;
    FakeWebSocket.all.push(this);
  }
  send(d: string) {
    this.sent.push(d);
  }
  close() {
    this.drop(1000);
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  drop(code: number) {
    this.readyState = 3;
    this.onclose?.({ code });
  }
}

const sockets = () => FakeWebSocket.all;
const last = () => sockets()[sockets().length - 1];

beforeEach(() => {
  vi.useFakeTimers();
  FakeWebSocket.all = [];
  vi.stubGlobal('WebSocket', FakeWebSocket);
  vi.spyOn(Math, 'random').mockReturnValue(0); // no jitter
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('ReconnectingSocket', () => {
  it('connects to the same host', () => {
    const s = new ReconnectingSocket({ path: '/api/events' });
    expect(last().url).toBe(wsUrl('/api/events'));
    expect(last().url).toMatch(/^ws:\/\/.*\/api\/events$/);
    s.close();
  });

  it('reports its state and hands over messages', () => {
    const states: SocketState[] = [];
    const got: unknown[] = [];
    const s = new ReconnectingSocket({ path: '/x', binaryType: 'arraybuffer', onStateChange: (v) => states.push(v), onMessage: (e) => got.push(e) });
    expect(last().binaryType).toBe('arraybuffer');
    last().open();
    expect(s.state).toBe('open');
    last().onmessage?.('hello');
    expect(got).toEqual(['hello']);
    expect(states).toContain('open');
    s.close();
  });

  it('only sends while open', () => {
    const s = new ReconnectingSocket({ path: '/x' });
    expect(s.send('early')).toBe(false);
    last().open();
    expect(s.isOpen).toBe(true);
    expect(s.sendJSON({ a: 1 })).toBe(true);
    expect(last().sent).toEqual(['{"a":1}']);
    s.close();
    expect(s.send('late')).toBe(false);
  });

  it('passes the close code on, the one a revoked login is told by', () => {
    const codes: number[] = [];
    const s = new ReconnectingSocket({ path: '/x', onClose: (c) => codes.push(c) });
    last().open();
    last().drop(4401);
    expect(codes).toEqual([4401]);
    s.close();
  });

  it('backs off while the hub is away, up to a limit', () => {
    const s = new ReconnectingSocket({ path: '/x' });
    for (const delay of [500, 1000, 2000, 5000, 10000, 15000, 15000]) {
      const n = sockets().length;
      last().drop(1006);
      vi.advanceTimersByTime(delay - 1);
      expect(sockets()).toHaveLength(n);
      vi.advanceTimersByTime(1);
      expect(sockets()).toHaveLength(n + 1);
    }
    s.close();
  });

  it('starts over after a connection that held, not after one that dropped at once', () => {
    const s = new ReconnectingSocket({ path: '/x' });
    last().drop(1006);
    vi.advanceTimersByTime(500);
    // accepted and closed at once: no tight loop
    last().open();
    last().drop(1006);
    let n = sockets().length;
    vi.advanceTimersByTime(999);
    expect(sockets()).toHaveLength(n);
    vi.advanceTimersByTime(1);
    expect(sockets()).toHaveLength(n + 1);
    // held for a while
    last().open();
    vi.advanceTimersByTime(6000);
    last().drop(1006);
    n = sockets().length;
    vi.advanceTimersByTime(500);
    expect(sockets()).toHaveLength(n + 1);
    s.close();
  });

  it('stays down once closed', () => {
    const closes: number[] = [];
    const s = new ReconnectingSocket({ path: '/x', onClose: (c) => closes.push(c) });
    last().open();
    const first = last();
    s.close();
    expect(s.state).toBe('closed');
    first.drop(1006);
    vi.advanceTimersByTime(60000);
    expect(sockets()).toHaveLength(1);
    expect(closes).toEqual([]);
    s.reconnectNow();
    expect(sockets()).toHaveLength(1);
  });

  it('stops a reconnect already waiting when closed', () => {
    const s = new ReconnectingSocket({ path: '/x' });
    last().drop(1006);
    s.close();
    vi.advanceTimersByTime(60000);
    expect(sockets()).toHaveLength(1);
  });

  it('reconnects now when told, with a fresh backoff', () => {
    const s = new ReconnectingSocket({ path: '/x' });
    last().open();
    s.reconnectNow();
    vi.advanceTimersByTime(500);
    expect(sockets()).toHaveLength(2);
    s.close();
  });

  it('survives a constructor that throws', () => {
    let fail = true;
    class Throwing extends FakeWebSocket {
      constructor(url: string) {
        super(url);
        if (fail) throw new Error('bad url');
      }
    }
    vi.stubGlobal('WebSocket', Throwing);
    const s = new ReconnectingSocket({ path: '/x' });
    fail = false;
    const n = sockets().length;
    vi.advanceTimersByTime(500);
    expect(sockets()).toHaveLength(n + 1);
    s.close();
  });
});
