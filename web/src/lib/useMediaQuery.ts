import { useSyncExternalStore } from 'react';

/** Live boolean for a CSS media query. */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (fn) => {
      const mql = window.matchMedia(query);
      mql.addEventListener('change', fn);
      return () => mql.removeEventListener('change', fn);
    },
    () => window.matchMedia(query).matches,
  );
}
