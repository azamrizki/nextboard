import { describe, expect, it } from 'vitest';
import { ATTACHES_PER_TICK, HOLD_MS, TICK_MS, budgetAfterLoss, budgetFor, plan, retryDelay, type BudgetEnv, type Slot } from './webglPool';

const NOW = 100_000;
const slot = (id: number, o: Partial<Slot> = {}): Slot => ({ id, area: 0, focused: false, gl: false, since: 0, shown: 0, never: false, retryAt: 0, ...o });
const on = (id: number, area: number, o: Partial<Slot> = {}) => slot(id, { area, shown: NOW, ...o });
const held = (id: number, o: Partial<Slot> = {}) => slot(id, { gl: true, since: NOW - 10 * HOLD_MS, ...o });

describe('which consoles get WebGL', () => {
  it('ranks the focused one first, then the largest on screen, up to the budget', () => {
    const p = plan([on(1, 100), on(2, 900), on(3, 50, { focused: true }), on(4, 400)], 2, NOW);
    expect(p.attach).toEqual([3, 2]);
    expect(p.release).toEqual([]);
  });

  it('gives nothing to consoles off screen, and takes nothing from them while no one needs it', () => {
    const p = plan([slot(1), held(2), held(3, { shown: NOW - 60_000 })], 4, NOW);
    expect(p).toEqual({ attach: [], release: [], again: null });
  });

  it('makes at most a few contexts per tick and comes back for the rest', () => {
    const many = Array.from({ length: 9 }, (_, i) => on(i + 1, 1000 - i));
    const p = plan(many, 12, NOW);
    expect(p.attach).toEqual([1, 2, 3, 4].slice(0, ATTACHES_PER_TICK));
    expect(p.again).toBe(NOW + TICK_MS);
    // consoles just mounted: all at once
    expect(plan(many, 12, NOW, Infinity)).toEqual({ attach: [1, 2, 3, 4, 5, 6, 7, 8, 9], release: [], again: null });
  });

  it('takes a context from off screen first, the one shown longest ago first', () => {
    const p = plan([held(1, { area: 10, shown: NOW }), held(2, { shown: NOW - 5000 }), held(3, { shown: NOW - 9000 }), on(4, 500), on(5, 400)], 3, NOW);
    expect(p.attach).toEqual([4, 5]);
    expect(p.release).toEqual([3, 2]);
  });

  it('takes from a visible one that ranks below the budget only when none is off screen', () => {
    const p = plan([held(1, { area: 10, shown: NOW }), on(2, 500)], 1, NOW);
    expect(p).toMatchObject({ attach: [2], release: [1] });
  });

  it('lets a console keep WebGL it got less than the hold time ago, unless the focused one needs it', () => {
    const fresh = held(1, { since: NOW - 500 });
    const p = plan([fresh, on(2, 500)], 1, NOW);
    expect(p).toEqual({ attach: [], release: [], again: NOW - 500 + HOLD_MS });
    expect(plan([fresh, on(2, 500, { focused: true })], 1, NOW)).toMatchObject({ attach: [2], release: [1] });
  });

  it('does not trade WebGL between two consoles of about the same size', () => {
    expect(plan([held(1, { area: 450, shown: NOW }), on(2, 500)], 1, NOW)).toMatchObject({ attach: [], release: [] });
    expect(plan([held(1, { area: 300, shown: NOW }), on(2, 500)], 1, NOW)).toMatchObject({ attach: [2], release: [1] });
  });

  it('skips consoles that may never have it and waits out a retry after a lost context', () => {
    const p = plan([on(1, 900, { never: true }), on(2, 800, { retryAt: NOW + 4000 }), on(3, 100)], 4, NOW);
    expect(p.attach).toEqual([3]);
    expect(p.again).toBe(NOW + 4000);
    expect(plan([on(2, 800, { retryAt: NOW - 1 })], 4, NOW).attach).toEqual([2]);
  });

  it('gives back what is over a lowered budget, and never what is pinned', () => {
    const p = plan([held(1, { area: 100, shown: NOW }), held(2, { shown: NOW - 1000 }), held(3, { shown: NOW - 2000, pinned: true }), held(4, { shown: NOW - 3000 })], 2, NOW);
    expect(p.release).toEqual([4, 2]);
    expect(plan([held(1, { pinned: true }), on(2, 500)], 1, NOW)).toMatchObject({ attach: [], release: [] });
  });
});

describe('the budget', () => {
  const env = (o: Partial<BudgetEnv> = {}): BudgetEnv => ({ userAgent: 'Mozilla/5.0 (X11; Linux x86_64) Chrome/150', platform: 'Linux x86_64', maxTouchPoints: 0, mode: null, max: null, hardware: () => true, ...o });

  it('stays below each browser’s cap', () => {
    expect(budgetFor(env())).toBe(12);
    expect(budgetFor(env({ userAgent: 'Mozilla/5.0 (Linux; Android 15; 2412DPC0AG) Chrome/150 Mobile' }))).toBe(6);
    expect(budgetFor(env({ userAgent: 'Mozilla/5.0 (Linux; Android 14; wv) Version/4.0 Chrome/150' }))).toBe(6);
    expect(budgetFor(env({ userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) Safari/605' }))).toBe(8);
    expect(budgetFor(env({ userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605', platform: 'MacIntel', maxTouchPoints: 5 }))).toBe(8);
    expect(budgetFor(env({ userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605', platform: 'MacIntel' }))).toBe(12);
  });

  it('is 0 on a software GPU unless forced, and follows the overrides', () => {
    expect(budgetFor(env({ hardware: () => false }))).toBe(0);
    expect(budgetFor(env({ hardware: () => false, mode: 'force' }))).toBe(12);
    expect(budgetFor(env({ mode: 'off' }))).toBe(0);
    expect(budgetFor(env({ max: '3' }))).toBe(3);
    expect(budgetFor(env({ max: '0' }))).toBe(0);
    expect(budgetFor(env({ max: 'lots' }))).toBe(12);
    expect(budgetFor(env({ hardware: () => false, mode: 'force', max: '20' }))).toBe(20);
  });

  it('does not probe the GPU when WebGL is off or forced', () => {
    const probe = () => {
      throw new Error('probed');
    };
    expect(budgetFor(env({ mode: 'off', hardware: probe }))).toBe(0);
    expect(budgetFor(env({ mode: 'force', hardware: probe }))).toBe(12);
  });

  it('drops to what was left when the browser took contexts, not when the GPU reset', () => {
    expect(budgetAfterLoss(12, 9, 1)).toBe(8);
    expect(budgetAfterLoss(12, 12, 2)).toBe(10);
    expect(budgetAfterLoss(6, 4, 1)).toBe(3);
    expect(budgetAfterLoss(12, 9, 9)).toBe(12);
    expect(budgetAfterLoss(12, 9, 0)).toBe(12);
    expect(budgetAfterLoss(3, 9, 1)).toBe(3);
  });

  it('retries a lost console after 2 s, doubling to 64 s', () => {
    expect([1, 2, 3, 4, 5, 6, 7, 12].map(retryDelay)).toEqual([2000, 4000, 8000, 16000, 32000, 64000, 64000, 64000]);
  });
});
