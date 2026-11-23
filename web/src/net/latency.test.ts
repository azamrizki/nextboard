import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { heldFor } from './ConnChip';
import { getNet, netBars, netClosed, netJitter, netLevel, netOpened, netPong, PING_EVERY, PING_TIMEOUT, resetNet } from './latency';
import { mbpsOf } from './speedTest';

describe('what a round trip means', () => {
  it('is good under 100 ms, fair to 300 ms, poor above or with pings lost', () => {
    expect(netLevel(null, [])).toBe('none');
    expect(netLevel(38, [38])).toBe('good');
    expect(netLevel(160, [160])).toBe('fair');
    expect(netLevel(640, [640])).toBe('poor');
    expect(netLevel(38, [38, null, 38])).toBe('fair');
    expect(netLevel(38, [null, 38, null, 38])).toBe('poor');
    // a loss long ago no longer counts
    expect(netLevel(38, [null, 38, 38, 38, 38])).toBe('good');
  });

  it('shows four bars under 60 ms, three to 100, two when fair, one when poor', () => {
    expect(netBars(20, 'good')).toBe(4);
    expect(netBars(80, 'good')).toBe(3);
    expect(netBars(160, 'fair')).toBe(2);
    expect(netBars(640, 'poor')).toBe(1);
    expect(netBars(null, 'none')).toBe(0);
  });

  it('takes the jitter from one answered ping to the next', () => {
    expect(netJitter([40])).toBeNull();
    expect(netJitter([40, 44, 40, null, 48])).toBe(5);
  });

  it('says how long the connection held and how fast a test went', () => {
    expect(heldFor(30_000)).toBe('30 s');
    expect(heldFor(41 * 60_000)).toBe('41 m');
    expect(heldFor((2 * 60 + 14) * 60_000)).toBe('2 h 14 m');
    expect(mbpsOf(10 << 20, 1000)).toBe(83.9);
    expect(mbpsOf(1, 0)).toBe(0);
  });
});

describe('the pings', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    resetNet();
  });
  afterEach(() => {
    resetNet();
    vi.useRealTimers();
  });

  it('times a pong, pings every 15 s, counts a ping with no answer as lost', () => {
    const sent: { type: string; id: number }[] = [];
    netOpened({ sendJSON: (v) => (sent.push(v as { type: string; id: number }), true) });
    vi.advanceTimersByTime(0);
    expect(sent).toEqual([{ type: 'ping', id: 1 }]);
    vi.advanceTimersByTime(30);
    netPong(1);
    expect(getNet().rtt).toBeGreaterThanOrEqual(1);
    expect(getNet().samples).toHaveLength(1);
    // an unknown or repeated pong changes nothing
    netPong(1);
    netPong(99);
    expect(getNet().samples).toHaveLength(1);

    vi.advanceTimersByTime(PING_EVERY);
    expect(sent.map((s) => s.id)).toEqual([1, 2]);
    vi.advanceTimersByTime(PING_TIMEOUT);
    expect(getNet().samples.at(-1)).toBeNull();
  });

  it('forgets the round trip while the connection is down and counts the reconnects', () => {
    const s = { sendJSON: () => true };
    netOpened(s);
    vi.advanceTimersByTime(0);
    netPong(1);
    expect(getNet().rtt).not.toBeNull();
    netClosed();
    expect(getNet()).toMatchObject({ rtt: null, since: 0 });
    netOpened(s);
    expect(getNet().reconnects).toBe(1);
  });
});
