import { describe, expect, it } from 'vitest';
import type { Beat } from './api';
import { averageMs24h, millis, msAxis, msTick } from './format';

const NOW = Date.parse('2026-11-15T12:00:00Z');
const beat = (minAgo: number, ms: number): Beat => ({ t: NOW - minAgo * 60_000, s: 1, ms });

describe('averageMs24h', () => {
  it('is 0 for checks under a millisecond, however few and however new', () => {
    // a monitor up for two minutes: 8 checks, all 0 ms
    const recent = Array.from({ length: 8 }, (_, i) => beat(2 - i * 0.25, 0));
    const m = { avgMs24h: 0, ms: 0, lastCheck: new Date(NOW - 5_000).toISOString(), recent };
    expect(averageMs24h(m, NOW)).toBe(0);
    expect(millis(averageMs24h(m, NOW))).toBe('<1 ms');
  });

  it('is what the hub says when there is an average', () => {
    expect(averageMs24h({ avgMs24h: 42, ms: 40, recent: [beat(1, 40)] }, NOW)).toBe(42);
  });

  it('is null when nothing of the day was timed', () => {
    // a push monitor without ping=: checks, but no times
    expect(averageMs24h({ avgMs24h: 0, ms: -1, lastCheck: new Date(NOW - 60_000).toISOString(), recent: [beat(1, -1), beat(2, -1)] }, NOW)).toBeNull();
    // never checked
    expect(averageMs24h({ avgMs24h: 0, ms: 0 }, NOW)).toBeNull();
    // timed, but more than a day ago
    expect(averageMs24h({ avgMs24h: 0, ms: 0, lastCheck: new Date(NOW - 25 * 3600_000).toISOString(), recent: [beat(25 * 60, 0)] }, NOW)).toBeNull();
    expect(millis(null)).toBe('—');
  });

  it('counts the last check when the beats are not there', () => {
    expect(averageMs24h({ avgMs24h: 0, ms: 0, lastCheck: new Date(NOW - 60_000).toISOString() }, NOW)).toBe(0);
  });
});

describe('millis', () => {
  it('reads like before above a millisecond', () => {
    expect(millis(0)).toBe('<1 ms');
    expect(millis(1)).toBe('1 ms');
    expect(millis(850)).toBe('850 ms');
    expect(millis(1500)).toBe('1.50 s');
    expect(millis(-1)).toBe('—');
  });
});

describe('msAxis', () => {
  it('gives sub-millisecond times an axis of 0 to 1 ms with three different labels', () => {
    const { max, ticks } = msAxis([0, 0, 0, -1]);
    expect(max).toBe(1);
    expect(ticks.map(msTick)).toEqual(['0 ms', '0.5 ms', '1 ms']);
  });

  it('never prints the same label twice, from no time to minutes', () => {
    for (let top = 0; top <= 200_000; top += top < 100 ? 1 : top < 5000 ? 7 : 997) {
      const labels = msAxis([top]).ticks.map(msTick);
      expect(new Set(labels).size, `labels for a slowest time of ${top} ms: ${labels.join(', ')}`).toBe(3);
    }
  });

  it('labels each tick by its own value, not a rounded one', () => {
    expect(msAxis([3]).ticks.map(msTick)).toEqual(['0 ms', '2.5 ms', '5 ms']);
    expect(msAxis([21]).ticks.map(msTick)).toEqual(['0 ms', '12.5 ms', '25 ms']);
    expect(msAxis([180]).ticks.map(msTick)).toEqual(['0 ms', '100 ms', '200 ms']);
    expect(msAxis([2100]).ticks.map(msTick)).toEqual(['0 ms', '1.25 s', '2.5 s']);
    // the top is above the slowest time
    for (const v of [1, 7, 99, 1234, 98765]) expect(msAxis([v]).max).toBeGreaterThanOrEqual(v);
  });
});
