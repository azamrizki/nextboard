// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { tickWhileVisible } from './visibleTick';

/** An IntersectionObserver the test drives by hand. */
class FakeObserver {
  static last: FakeObserver | null = null;
  constructor(private cb: (entries: { isIntersecting: boolean }[]) => void) {
    FakeObserver.last = this;
  }
  observe() {}
  disconnect() {}
  show(on: boolean) {
    this.cb([{ isIntersecting: on }]);
  }
}

let hidden = false;
beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal('IntersectionObserver', FakeObserver);
  hidden = false;
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('a clock for what is on screen', () => {
  it('ticks each second while in view, stops out of view or in a hidden tab, and catches up on return', () => {
    const tick = vi.fn();
    const stop = tickWhileVisible(document.createElement('div'), 1000, tick);
    vi.advanceTimersByTime(3000);
    expect(tick).toHaveBeenCalledTimes(3);

    FakeObserver.last!.show(false);
    vi.advanceTimersByTime(5000);
    expect(tick).toHaveBeenCalledTimes(3);
    FakeObserver.last!.show(true); // back in view: shows the time right away
    expect(tick).toHaveBeenCalledTimes(4);

    hidden = true;
    document.dispatchEvent(new Event('visibilitychange'));
    vi.advanceTimersByTime(5000);
    expect(tick).toHaveBeenCalledTimes(4);
    hidden = false;
    document.dispatchEvent(new Event('visibilitychange'));
    expect(tick).toHaveBeenCalledTimes(5);
    vi.advanceTimersByTime(1000);
    expect(tick).toHaveBeenCalledTimes(6);

    stop();
    vi.advanceTimersByTime(5000);
    document.dispatchEvent(new Event('visibilitychange'));
    expect(tick).toHaveBeenCalledTimes(6);
  });
});
