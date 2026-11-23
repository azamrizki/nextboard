import { describe, expect, it } from 'vitest';
import { sessionWindow, untilShort } from './format';

const w = (id: string, label: string, usedPct: number) => ({ id, label, usedPct });

describe("the usage pill's window", () => {
  it("is the current session's, not the busiest", () => {
    // as Claude's /usage reads on this hub: a 24 h line at 96% was what the pill showed
    const claude = [w('session', 'Current session', 19), w('week', 'Current week (all models)', 3), w('week-fable', 'Current week (Fable)', 0), w('last-24h-shares-overlap', 'Last 24h · shares overlap', 96)];
    expect(sessionWindow(claude)?.usedPct).toBe(19);
    expect(sessionWindow([w('weekly', 'Weekly limit', 5), w('5h', '5h limit', 0)])?.id).toBe('5h');
  });

  it('finds it by its label, else the first that is not a week', () => {
    expect(sessionWindow([w('week', 'Current week', 40), w('x', '5 hours window', 12)])?.usedPct).toBe(12);
    expect(sessionWindow([w('week', 'Current week', 40), w('day', 'Daily', 7)])?.id).toBe('day');
    expect(sessionWindow([w('week', 'Current week', 40)])?.id).toBe('week');
    expect(sessionWindow(null)).toBeUndefined();
  });
});

describe('untilShort', () => {
  const now = Date.parse('2026-11-23T12:00:00Z');
  const at = (min: number) => new Date(now + min * 60_000).toISOString();
  it('says the time left as the header does: minutes, hours, days', () => {
    expect(untilShort(at(45), now)).toBe('45m');
    expect(untilShort(at(125), now)).toBe('2h');
    expect(untilShort(at(47 * 60 + 59), now)).toBe('47h');
    expect(untilShort(at(3 * 24 * 60 + 10), now)).toBe('3d');
  });
  it('says nothing when the time is unknown or past', () => {
    expect(untilShort(null, now)).toBe('');
    expect(untilShort('not a date', now)).toBe('');
    expect(untilShort(at(-5), now)).toBe('');
  });
});
