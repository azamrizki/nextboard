import { useSyncExternalStore } from 'react';

/** Chromium only, secure contexts only; not in TypeScript's DOM lib. */
type KeyboardLock = { lock?: (keys: string[]) => Promise<void>; unlock?: () => void };
const keyboardLock = () => (navigator as Navigator & { keyboard?: KeyboardLock }).keyboard;

/**
 * Browser full screen for the whole page: menus and modals portal to document.body, so
 * the root element has to be the one that goes full screen. `supported` is false where
 * the API is missing (iPhone Safari, native webviews).
 */
export function useFullscreen() {
  const active = useSyncExternalStore(
    (fn) => {
      document.addEventListener('fullscreenchange', fn);
      return () => document.removeEventListener('fullscreenchange', fn);
    },
    () => !!document.fullscreenElement,
  );
  const toggle = () => {
    if (document.fullscreenElement) {
      keyboardLock()?.unlock?.();
      void document.exitFullscreen().catch(() => undefined);
      return;
    }
    // Esc belongs to the terminals (agents use it to interrupt): where the browser allows
    // the lock, leaving full screen takes holding Esc instead of a single press.
    document.documentElement
      .requestFullscreen()
      .then(() => keyboardLock()?.lock?.(['Escape']))
      .catch(() => undefined);
  };
  return { supported: document.fullscreenEnabled, active, toggle };
}
