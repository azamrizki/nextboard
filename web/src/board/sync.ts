// One workspace board kept in sync with the hub (docs/API-board.md §2–3).
//
//   local state = confirmed (hub state at `rev`) + in-flight batches + pending ops
//
// Local ops apply immediately and are POSTed in batches (~150 ms). Remote `boardOps`
// events advance `confirmed` in rev order; our own ops are rebased on top, so a
// concurrent remote edit never hides an edit we have not sent yet. A rev gap (missed
// broadcast, reconnect) reloads the board.

import { api, ApiError, errorMessage } from '../api';
import { CLIENT_ID } from '../lib/ids';
import type { Board, HubEvent, Op } from '../lib/types';
import { applyOps, EMPTY_STATE, queueOp, type DocState } from './ops';

const FLUSH_MS = 150;
const MAX_OPS = 400; // hub limit 500 per request
const MAX_BYTES = 1_500_000; // hub limit 2 MB per request
const RETRY_MS = 2000;

type BoardOpsEvent = Extract<HubEvent, { type: 'boardOps' }>;

interface Batch {
  ops: Op[];
  /** The hub answered (rev known). */
  acked: boolean;
  rev: number;
}

export type SyncStatus = 'loading' | 'ready' | 'error';

export class BoardSync {
  readonly workspaceId: string;
  status: SyncStatus = 'loading';
  error = '';
  rev = 0;
  state: DocState = EMPTY_STATE;
  /** Called with a message (and the hub's error code) when the hub rejects our ops (they are rolled back). */
  onRejected?: (message: string, code?: string) => void;
  /** Called after a (re)load replaced the state from the hub. */
  onLoaded?: () => void;

  private confirmed: DocState = EMPTY_STATE;
  private pending: Op[] = [];
  private inflight: Batch[] = [];
  private sending = false;
  private timer: number | undefined;
  private listeners = new Set<() => void>();
  private disposed = false;
  private loadSeq = 0;
  /** boardOps that arrived while a load was in progress. */
  private buffered: BoardOpsEvent[] = [];

  constructor(workspaceId: string) {
    this.workspaceId = workspaceId;
  }

  // ---- subscription (useSyncExternalStore) ----

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  getState = (): DocState => this.state;
  private emit() {
    this.listeners.forEach((l) => l());
  }

  /** True while local edits have not been confirmed by the hub. */
  get dirty(): boolean {
    return this.pending.length > 0 || this.inflight.length > 0;
  }

  // ---- loading ----

  async load(): Promise<void> {
    const seq = ++this.loadSeq;
    if (this.status !== 'ready') {
      this.status = 'loading';
      this.emit();
    }
    this.buffered = [];
    try {
      const b = await api.board(this.workspaceId);
      if (this.disposed || seq !== this.loadSeq) return;
      this.adopt(b);
      // acknowledged batches are part of what we just loaded
      this.inflight = this.inflight.filter((x) => !x.acked);
      const buffered = this.buffered;
      this.buffered = [];
      this.status = 'ready';
      this.error = '';
      this.rebase();
      for (const ev of buffered) this.receive(ev);
      this.onLoaded?.();
      this.schedule();
    } catch (err) {
      if (this.disposed || seq !== this.loadSeq) return;
      this.error = errorMessage(err);
      if (this.status !== 'ready') this.status = 'error';
      this.emit();
    }
  }

  private adopt(b: Board) {
    this.confirmed = { nodes: b.nodes ?? [], edges: b.edges ?? [] };
    this.rev = b.rev ?? 0;
  }

  /** Replace everything with a board returned by the hub (snapshot restore). */
  reset(b: Board) {
    this.loadSeq++;
    this.adopt(b);
    this.pending = [];
    this.inflight = [];
    this.buffered = [];
    this.status = 'ready';
    this.rebase();
  }

  // ---- local changes ----

  /** Apply ops locally right away and queue them for the hub. */
  change(ops: Op[]) {
    if (ops.length === 0 || this.disposed) return;
    this.state = applyOps(this.state, ops);
    for (const op of ops) queueOp(this.pending, op);
    this.schedule();
    this.emit();
  }

  private schedule(delay = FLUSH_MS) {
    if (this.timer !== undefined || this.sending || this.pending.length === 0 || this.disposed) return;
    this.timer = window.setTimeout(() => {
      this.timer = undefined;
      void this.flush();
    }, delay);
  }

  /** Take up to MAX_OPS / MAX_BYTES from the pending queue. */
  private takeChunk(): Op[] {
    let bytes = 0;
    let n = 0;
    while (n < this.pending.length && n < MAX_OPS) {
      const size = JSON.stringify(this.pending[n]).length;
      if (n > 0 && bytes + size > MAX_BYTES) break;
      bytes += size;
      n++;
    }
    return this.pending.splice(0, n);
  }

  async flush(): Promise<void> {
    window.clearTimeout(this.timer);
    this.timer = undefined;
    if (this.sending || this.pending.length === 0 || this.status !== 'ready' || this.disposed) return;
    const batch: Batch = { ops: this.takeChunk(), acked: false, rev: 0 };
    this.inflight.push(batch);
    this.sending = true;
    let retry = false;
    try {
      const res = await api.postOps(this.workspaceId, { clientId: CLIENT_ID, baseRev: this.rev, ops: batch.ops });
      batch.acked = true;
      batch.rev = res?.rev ?? 0;
      this.onAck(batch);
    } catch (err) {
      this.drop(batch);
      if (err instanceof ApiError && (err.status === 0 || err.status >= 500 || err.status === 429)) {
        // hub unreachable / busy: keep the ops and try again
        this.pending.unshift(...batch.ops);
        retry = true;
      } else {
        this.onRejected?.(errorMessage(err), err instanceof ApiError ? err.code : '');
        this.rebase();
      }
    } finally {
      this.sending = false;
      if (retry) this.schedule(RETRY_MS);
      else this.schedule();
    }
  }

  private drop(b: Batch) {
    const i = this.inflight.indexOf(b);
    if (i >= 0) this.inflight.splice(i, 1);
  }

  private onAck(b: Batch) {
    if (!this.inflight.includes(b)) return; // the echo was faster
    if (!b.rev || b.rev <= this.rev) {
      // already included (reload) or the hub did not report a rev
      if (!b.rev) return; // wait for the echo
      this.drop(b);
      return;
    }
    if (b.rev === this.rev + 1) {
      this.confirmed = applyOps(this.confirmed, b.ops);
      this.rev = b.rev;
      this.drop(b);
      this.rebase();
    }
    // else: other clients' ops come first; they arrive on the events socket
  }

  // ---- remote changes ----

  /** A `boardOps` event for this workspace. */
  receive(ev: BoardOpsEvent) {
    if (this.disposed) return;
    if (this.status !== 'ready') {
      this.buffered.push(ev);
      return;
    }
    if (ev.rev <= this.rev) return; // duplicate / already loaded
    while (ev.rev > this.rev + 1) {
      const b = this.inflight.find((x) => x.acked && x.rev === this.rev + 1);
      if (!b) {
        void this.load(); // missed something
        return;
      }
      this.confirmed = applyOps(this.confirmed, b.ops);
      this.rev = b.rev;
      this.drop(b);
    }
    if (ev.clientId === CLIENT_ID) {
      const own = this.inflight.find((x) => x.acked && x.rev === ev.rev) ?? this.inflight.find((x) => !x.acked);
      if (own) this.drop(own);
    }
    this.confirmed = applyOps(this.confirmed, ev.ops ?? []);
    this.rev = ev.rev;
    this.rebase();
  }

  private rebase() {
    let s = this.confirmed;
    for (const b of this.inflight) s = applyOps(s, b.ops);
    if (this.pending.length) s = applyOps(s, this.pending);
    this.state = s;
    this.emit();
  }

  /** Stop syncing; unsent ops are sent one last time (fire and forget). */
  dispose() {
    if (this.disposed) return;
    window.clearTimeout(this.timer);
    if (this.pending.length && this.status === 'ready') {
      const ops = this.pending.splice(0, MAX_OPS);
      void api.postOps(this.workspaceId, { clientId: CLIENT_ID, baseRev: this.rev, ops }).catch(() => undefined);
    }
    this.disposed = true;
    this.listeners.clear();
  }
}
