// The speed test (docs/API.md, Connection to the hub): about 10 MB down from the hub and 4 MB up,
// timed here, only when asked: a phone pays for the data. The result stays for this page, so the
// details show it again when they open.

import { useSyncExternalStore } from 'react';

export const DOWN_BYTES = 10 << 20;
export const UP_BYTES = 4 << 20;

export interface SpeedResult {
  mbps: number;
  bytes: number;
  ms: number;
  /** ms since the epoch */
  at: number;
}

export interface SpeedState {
  phase: 'idle' | 'down' | 'up' | 'done' | 'error';
  down: SpeedResult | null;
  up: SpeedResult | null;
  /** Of the part running now. */
  progress: { loaded: number; total: number; mbps: number } | null;
  error: string;
}

const EMPTY: SpeedState = { phase: 'idle', down: null, up: null, progress: null, error: '' };
let state: SpeedState = EMPTY;
const listeners = new Set<() => void>();
let abort: AbortController | null = null;

function set(patch: Partial<SpeedState>) {
  state = { ...state, ...patch };
  listeners.forEach((l) => l());
}

export function useSpeedTest(): SpeedState {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => state,
  );
}

/** Megabits per second of bytes in ms. */
export function mbpsOf(bytes: number, ms: number): number {
  if (ms <= 0) return 0;
  return Math.round(((bytes * 8) / (ms / 1000) / 1e6) * 10) / 10;
}

/** Bytes nothing on the way can compress. */
function noise(n: number): Blob {
  const chunk = new Uint8Array(64 << 10);
  const parts: Uint8Array[] = [];
  for (let left = n; left > 0; left -= chunk.length) {
    const c = left >= chunk.length ? new Uint8Array(chunk.length) : new Uint8Array(left);
    crypto.getRandomValues(c);
    parts.push(c);
  }
  return new Blob(parts as BlobPart[], { type: 'application/octet-stream' });
}

async function download(signal: AbortSignal): Promise<SpeedResult> {
  const res = await fetch(`/api/net/download?bytes=${DOWN_BYTES}`, { cache: 'no-store', credentials: 'same-origin', signal });
  if (!res.ok || !res.body) throw new Error(`The hub answered ${res.status} to the download.`);
  const reader = res.body.getReader();
  // timed from the first bytes: the wait before them is the round trip, shown apart
  let first = 0;
  let firstBytes = 0;
  let loaded = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    const now = performance.now();
    if (!first) {
      first = now;
      firstBytes = value.length;
    }
    loaded += value.length;
    set({ progress: { loaded, total: DOWN_BYTES, mbps: mbpsOf(loaded - firstBytes, now - first) } });
  }
  const ms = performance.now() - first;
  if (loaded < DOWN_BYTES) throw new Error('The download broke off before its end.');
  return { mbps: mbpsOf(loaded - firstBytes, ms), bytes: loaded, ms: Math.round(ms), at: Date.now() };
}

function upload(signal: AbortSignal): Promise<SpeedResult> {
  const body = noise(UP_BYTES);
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    let t0 = 0;
    let sent = 0;
    xhr.open('POST', '/api/net/upload');
    xhr.setRequestHeader('X-NextBoard', '1');
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');
    xhr.upload.onprogress = (e) => {
      sent = performance.now();
      set({ progress: { loaded: e.loaded, total: UP_BYTES, mbps: mbpsOf(e.loaded, sent - t0) } });
    };
    xhr.upload.onload = () => {
      sent = performance.now();
    };
    xhr.onload = () => {
      if (xhr.status === 200) {
        const ms = (sent || performance.now()) - t0;
        resolve({ mbps: mbpsOf(UP_BYTES, ms), bytes: UP_BYTES, ms: Math.round(ms), at: Date.now() });
      } else if (xhr.status === 413) {
        reject(new Error('A proxy in front of the hub refused 4 MB (413): it takes smaller uploads only. In nginx that is client_max_body_size.'));
      } else reject(new Error(`The hub answered ${xhr.status} to the upload.`));
    };
    xhr.onerror = () => reject(new Error('The upload broke off: the hub could not be reached.'));
    xhr.onabort = () => reject(new DOMException('stopped', 'AbortError'));
    signal.addEventListener('abort', () => xhr.abort());
    t0 = performance.now();
    xhr.send(body);
  });
}

/** Download, then upload. Does nothing while a test runs. */
export async function runSpeedTest(): Promise<void> {
  if (abort) return;
  const ctl = new AbortController();
  abort = ctl;
  set({ phase: 'down', down: null, up: null, progress: { loaded: 0, total: DOWN_BYTES, mbps: 0 }, error: '' });
  try {
    const down = await download(ctl.signal);
    set({ down, phase: 'up', progress: { loaded: 0, total: UP_BYTES, mbps: 0 } });
    const up = await upload(ctl.signal);
    set({ up, phase: 'done', progress: null });
  } catch (err) {
    if (ctl.signal.aborted) set({ phase: state.down || state.up ? 'done' : 'idle', progress: null });
    else set({ phase: 'error', progress: null, error: err instanceof Error ? err.message : String(err) });
  } finally {
    abort = null;
  }
}

export function stopSpeedTest(): void {
  abort?.abort();
}

/** For tests. */
export function resetSpeedTest(): void {
  abort?.abort();
  abort = null;
  state = EMPTY;
  listeners.forEach((l) => l());
}
