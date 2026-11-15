// The memory page loads on demand: its galaxy is not for the first page. So that opening it does
// not wait on its code and then on its data, one after the other, the code is fetched when the app
// is idle or a pointer or focus rests on a Memory link, and the galaxy's data the moment the page
// is pressed or opened, both while the other is on its way. Small: it is in the main bundle.

import { memoryApi } from './api';
import type { GalaxyView } from './types';

type PageModule = typeof import('./MemoryPage');
let page: Promise<PageModule> | null = null;
let loaded: PageModule | null = null;

/** The page's code, fetched once (again after a failure, so that a retry can work). */
export function loadMemoryPage(): Promise<PageModule> {
  page ??= import('./MemoryPage').then(
    (m) => (loaded = m),
    (err: unknown) => {
      page = null;
      throw err;
    },
  );
  return page;
}

/**
 * The page's code if it is here already. Rendered as it is, the page shows at once: React keeps
 * a Suspense fallback up for at least 300 ms once it has shown one, even for code that was
 * fetched long before, as a lazy component's always is on its first render.
 */
export const memoryPageIfLoaded = (): PageModule | null => loaded;

/** An answer fetched early, taken once while it is fresh; later loads ask again. */
export function freshOnce<T>(load: () => Promise<T>, freshMs: number, now: () => number = Date.now) {
  let early: { at: number; p: Promise<T> } | null = null;
  const fresh = () => !!early && now() - early.at < freshMs;
  return {
    start() {
      if (fresh()) return;
      const p = load();
      p.catch(() => undefined); // whoever takes it sees the error
      early = { at: now(), p };
    },
    take(): Promise<T> {
      const p = fresh() ? early!.p : null;
      early = null;
      return p ?? load();
    },
  };
}

const galaxy = freshOnce<GalaxyView>(() => memoryApi.galaxy(), 5_000);
export const prefetchGalaxy = galaxy.start;
/** The galaxy for the page's first look: the early answer if there is one. */
export const takeGalaxy = galaxy.take;

/** Fetches the page's code once the app has nothing else to do. Gives back how to call it off. */
export function preloadMemoryWhenIdle(): () => void {
  const run = () => void loadMemoryPage().catch(() => undefined);
  if (typeof window.requestIdleCallback === 'function') {
    let idle = 0;
    // after the first page has settled, not in the middle of it
    const t = window.setTimeout(() => {
      idle = window.requestIdleCallback(run, { timeout: 10_000 });
    }, 2_000);
    return () => {
      window.clearTimeout(t);
      if (idle) window.cancelIdleCallback(idle);
    };
  }
  const t = window.setTimeout(run, 4_000);
  return () => window.clearTimeout(t);
}

/** For a link to the memory page: the code on hover or focus, the data too on a press. */
export const memoryLinkIntent = {
  onPointerEnter: () => void loadMemoryPage().catch(() => undefined),
  onFocus: () => void loadMemoryPage().catch(() => undefined),
  onPointerDown: () => {
    void loadMemoryPage().catch(() => undefined);
    prefetchGalaxy();
  },
};
