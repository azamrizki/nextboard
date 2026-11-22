import { describe, expect, it } from 'vitest';
import { sessionWindow } from './format';

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
