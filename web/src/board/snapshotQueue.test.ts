import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SLOT_MAX_MS, SnapshotFeed, snapshotQueue, type FeedTerm } from './snapshotQueue';

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

/** A terminal that records what it was given and parses when told to. */
class FakeTerm implements FeedTerm {
  log: string[] = [];
  parsed: (() => void)[] = [];
  constructor(private name: string, private shared: string[] = []) {}
  reset() {
    this.log.push('reset');
    this.shared.push(`${this.name}:reset`);
  }
  write(data: Uint8Array, done?: () => void) {
    const s = new TextDecoder().decode(data);
    this.log.push(s);
    this.shared.push(`${this.name}:${s}`);
    if (done) this.parsed.push(done);
  }
  /** xterm finished the snapshot. */
  finish() {
    this.parsed.shift()?.();
  }
}

const b = (s: string) => new TextEncoder().encode(s);

describe('snapshotQueue', () => {
  it('runs at most max at a time; the next starts when one is done', () => {
    const q = snapshotQueue(2);
    const started: number[] = [];
    const done: (() => void)[] = [];
    for (let i = 0; i < 5; i++) q.add({ rank: () => 0, start: (d) => (started.push(i), done.push(d)) });
    expect(started).toEqual([0, 1]);
    done[0]();
    done[0](); // twice frees one slot only
    expect(started).toEqual([0, 1, 2]);
    done[1]();
    done[2]();
    expect(started).toEqual([0, 1, 2, 3, 4]);
  });

  it('takes the highest rank first, as it is when a slot frees, then the order they came', () => {
    const q = snapshotQueue(1);
    const started: string[] = [];
    let release = () => undefined as void;
    const ranks: Record<string, number> = { a: 0, b: 0, c: 1, d: 0 };
    for (const n of ['a', 'b', 'c', 'd']) q.add({ rank: () => ranks[n], start: (d) => (started.push(n), (release = d)) });
    expect(started).toEqual(['a']);
    ranks.d = 2; // focused meanwhile
    release();
    expect(started).toEqual(['a', 'd']);
    release();
    expect(started).toEqual(['a', 'd', 'c']);
    release();
    expect(started).toEqual(['a', 'd', 'c', 'b']);
  });

  it('cancels a waiting job, and frees the slot of a running one', () => {
    const q = snapshotQueue(1);
    const started: string[] = [];
    const cancelA = q.add({ rank: () => 0, start: () => started.push('a') });
    const cancelB = q.add({ rank: () => 0, start: () => started.push('b') });
    q.add({ rank: () => 0, start: () => started.push('c') });
    cancelB();
    cancelA(); // a was running: c gets its slot
    expect(started).toEqual(['a', 'c']);
  });

  it('frees a slot never given back after a while', () => {
    const q = snapshotQueue(1);
    const started: string[] = [];
    q.add({ rank: () => 0, start: () => started.push('a') });
    q.add({ rank: () => 0, start: () => started.push('b') });
    vi.advanceTimersByTime(SLOT_MAX_MS - 1);
    expect(started).toEqual(['a']);
    vi.advanceTimersByTime(1);
    expect(started).toEqual(['a', 'b']);
  });

  it('copes with jobs done at once, without nesting', () => {
    const q = snapshotQueue(1);
    const started: number[] = [];
    for (let i = 0; i < 1000; i++) q.add({ rank: () => 0, start: (d) => (started.push(i), d()) });
    expect(started).toHaveLength(1000);
  });
});

describe('SnapshotFeed', () => {
  it('resets and writes the snapshot when its turn comes, live output after it in order', () => {
    const q = snapshotQueue(1);
    const all: string[] = [];
    const t1 = new FakeTerm('1', all);
    const t2 = new FakeTerm('2', all);
    const f1 = new SnapshotFeed(t1, () => 0, q);
    const f2 = new SnapshotFeed(t2, () => 0, q);
    f1.snapshot(b('S1'));
    f2.snapshot(b('S2'));
    f2.live(b('x'));
    f1.live(b('a'));
    f2.live(b('y'));
    expect(all).toEqual(['1:reset', '1:S1', '1:a']);
    expect(f1.replaying).toBe(true);
    expect(f2.replaying).toBe(false); // waiting is not parsing: input still goes
    t1.finish();
    expect(f1.replaying).toBe(false);
    expect(t2.log).toEqual(['reset', 'S2', 'x', 'y']);
    f2.live(b('z'));
    expect(t2.log).toEqual(['reset', 'S2', 'x', 'y', 'z']);
  });

  it('a newer snapshot while waiting replaces the old one and its live output', () => {
    const q = snapshotQueue(1);
    const blocker = new SnapshotFeed(new FakeTerm('0'), () => 0, q);
    blocker.snapshot(b('B'));
    const t = new FakeTerm('1');
    const f = new SnapshotFeed(t, () => 0, q);
    f.snapshot(b('old'));
    f.live(b('lost'));
    f.snapshot(b('new'));
    f.live(b('kept'));
    blocker.dispose();
    expect(t.log).toEqual(['reset', 'new', 'kept']);
  });

  it('a console closing while it waits leaves the line; while parsing, frees its slot', () => {
    const q = snapshotQueue(1);
    const t1 = new FakeTerm('1');
    const t2 = new FakeTerm('2');
    const t3 = new FakeTerm('3');
    const f1 = new SnapshotFeed(t1, () => 0, q);
    const f2 = new SnapshotFeed(t2, () => 0, q);
    const f3 = new SnapshotFeed(t3, () => 0, q);
    f1.snapshot(b('S1'));
    f2.snapshot(b('S2'));
    f3.snapshot(b('S3'));
    f2.dispose();
    f2.live(b('after'));
    f1.dispose(); // its parse never reports back
    expect(t2.log).toEqual([]);
    expect(t3.log).toEqual(['reset', 'S3']);
    t1.finish(); // late callback changes nothing
    expect(t3.log).toEqual(['reset', 'S3']);
  });

  it('a reconnect while the last snapshot is parsed queues the new one', () => {
    const q = snapshotQueue(2);
    const t = new FakeTerm('1');
    const f = new SnapshotFeed(t, () => 0, q);
    f.snapshot(b('A'));
    f.snapshot(b('B'));
    f.live(b('b'));
    expect(t.log).toEqual(['reset', 'A', 'reset', 'B', 'b']);
    t.finish();
    expect(f.replaying).toBe(true);
    t.finish();
    expect(f.replaying).toBe(false);
  });
});
