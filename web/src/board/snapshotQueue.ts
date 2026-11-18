// Snapshots applied a few at a time. Every (re)connect of a console starts with a snapshot of up to
// 1 MiB (docs/API.md), and xterm parses it on the main thread. A board of twenty consoles opening
// together (first load, the hub back after a blip) parsed twenty of them in one burst and the tab
// froze. Now at most SNAPSHOTS_AT_ONCE consoles parse one, the focused and on-screen ones first;
// the next starts when xterm has parsed the previous. Live output that comes in meanwhile waits
// behind its console's snapshot, in order.

export const SNAPSHOTS_AT_ONCE = 2;
/** A slot not given back by then is freed anyway (the data is already xterm's): a console that never finishes must not hold up the rest. */
export const SLOT_MAX_MS = 3000;

export interface SnapshotJob {
  /** Higher goes first (focused, on screen); asked when a slot frees, so it is current then. */
  rank: () => number;
  /** Apply it; call `done` when xterm has parsed it. */
  start: (done: () => void) => void;
}

export interface SnapshotQueue {
  /** Queues a job (or starts it now when a slot is free). Gives back its cancel: it leaves the line, or frees its slot. */
  add: (job: SnapshotJob) => () => void;
}

export function snapshotQueue(max = SNAPSHOTS_AT_ONCE, slotMaxMs = SLOT_MAX_MS): SnapshotQueue {
  let running = 0;
  let pumping = false;
  const waiting: { job: SnapshotJob; cancel: () => void }[] = [];

  const rankOf = (job: SnapshotJob) => {
    try {
      return job.rank();
    } catch {
      return 0;
    }
  };

  function pump() {
    // a job that finishes inside start() frees its slot here, not in a nested pump
    if (pumping) return;
    pumping = true;
    try {
      while (running < max && waiting.length) {
        let best = 0;
        let bestRank = rankOf(waiting[0].job);
        for (let i = 1; i < waiting.length; i++) {
          const r = rankOf(waiting[i].job);
          if (r > bestRank) {
            best = i;
            bestRank = r;
          }
        }
        const w = waiting.splice(best, 1)[0];
        running++;
        let freed = false;
        const timer = setTimeout(() => free(), slotMaxMs);
        const free = () => {
          if (freed) return;
          freed = true;
          clearTimeout(timer);
          running--;
          pump();
        };
        w.cancel = free;
        try {
          w.job.start(free);
        } catch {
          free();
        }
      }
    } finally {
      pumping = false;
    }
  }

  return {
    add(job) {
      let queued = true;
      const w = {
        job,
        cancel: () => {
          queued = false;
          const i = waiting.indexOf(w);
          if (i >= 0) waiting.splice(i, 1);
        },
      };
      waiting.push(w);
      pump();
      return () => {
        if (queued) w.cancel();
      };
    },
  };
}

/** The page's queue, shared by every console. */
const shared = snapshotQueue();

/** What a feed needs of xterm's Terminal. */
export interface FeedTerm {
  reset(): void;
  write(data: Uint8Array, done?: () => void): void;
}

/**
 * One console's output: its snapshot goes through the queue, live output after it. `rank` as in
 * SnapshotJob.
 */
export class SnapshotFeed {
  private pending: { data: Uint8Array; live: Uint8Array[]; cancel: () => void } | null = null;
  /** Slots of snapshots xterm is still parsing. */
  private parsing = new Set<() => void>();
  private disposed = false;
  private term: FeedTerm;
  private rank: () => number;
  private queue: SnapshotQueue;

  constructor(term: FeedTerm, rank: () => number, queue: SnapshotQueue = shared) {
    this.term = term;
    this.rank = rank;
    this.queue = queue;
  }

  /**
   * A snapshot is being parsed. It is history: the terminal queries in it were answered long ago,
   * and xterm's answers must not reach the program as typed text.
   */
  get replaying(): boolean {
    return this.parsing.size > 0;
  }

  /** The first binary frame after a connect. */
  snapshot(data: Uint8Array): void {
    if (this.disposed) return;
    if (this.pending) {
      // a newer one before the last got its turn: it resets the screen anyway, so the old one and its live output go
      this.pending.data = data;
      this.pending.live = [];
      return;
    }
    const p = { data, live: [] as Uint8Array[], cancel: () => undefined as void };
    this.pending = p;
    p.cancel = this.queue.add({
      rank: this.rank,
      start: (done) => {
        this.pending = null;
        const slot = () => {
          if (this.parsing.delete(slot)) done();
        };
        this.parsing.add(slot);
        this.term.reset();
        this.term.write(p.data, slot);
        // xterm parses writes in order, so these land after the snapshot as they would have
        for (const d of p.live) this.term.write(d);
      },
    });
  }

  /** Any later binary frame. */
  live(data: Uint8Array): void {
    if (this.disposed) return;
    if (this.pending) this.pending.live.push(data);
    else this.term.write(data);
  }

  /** The console is closing: out of the line, and its slots free. */
  dispose(): void {
    this.disposed = true;
    this.pending?.cancel();
    this.pending = null;
    const slots = [...this.parsing];
    for (const s of slots) s();
  }
}

