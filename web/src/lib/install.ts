// NextBoard as an app on this device (DESIGN.md §3.17). A browser installs a web app only from
// a secure address (HTTPS, or this computer itself); a hub opened over plain HTTP still works
// in full, and Add to Home screen gives a shortcut that opens in a browser tab.
//
// Chrome offers the install by an event, early after the page loads, so the event is watched
// from the start (main.tsx) and kept for the Install button. Chrome's own install entries stay.

import { useSyncExternalStore } from 'react';

interface InstallPromptEvent extends Event {
  prompt(): Promise<void>;
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>;
}

/**
 * installed  running as the installed app
 * ready      the browser offers the install: the Install button shows its dialog
 * manual     a secure address, but no offer: from the browser's menu (Safari, Firefox, or a
 *            Chrome that has it installed already or wants more use of the page first)
 * insecure   plain HTTP: the browser only adds a shortcut
 */
export type InstallState = 'installed' | 'ready' | 'manual' | 'insecure';

let offer: InstallPromptEvent | null = null;
let installed = false;
const listeners = new Set<() => void>();
const changed = () => listeners.forEach((l) => l());

const APP_MODES = ['standalone', 'fullscreen', 'minimal-ui', 'window-controls-overlay'];

/** Whether this page runs as the installed app, not in a browser tab. */
export function runsAsApp(): boolean {
  if (typeof window === 'undefined') return false;
  if ((navigator as { standalone?: boolean }).standalone === true) return true;
  return APP_MODES.some((m) => window.matchMedia?.(`(display-mode: ${m})`).matches);
}

function state(): InstallState {
  if (installed || runsAsApp()) return 'installed';
  if (offer) return 'ready';
  return window.isSecureContext ? 'manual' : 'insecure';
}

export function watchInstall() {
  window.addEventListener('beforeinstallprompt', (e) => {
    offer = e as InstallPromptEvent;
    changed();
  });
  window.addEventListener('appinstalled', () => {
    offer = null;
    installed = true;
    changed();
  });
}

/** Shows the browser's install dialog; true when it was accepted. */
async function install(): Promise<boolean> {
  const e = offer;
  if (!e) return false;
  try {
    await e.prompt();
    const { outcome } = await e.userChoice;
    // an offer is good for one dialog; the browser makes a new one if it may ask again
    offer = null;
    changed();
    return outcome === 'accepted';
  } catch {
    offer = null;
    changed();
    return false;
  }
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

export function useInstall(): { state: InstallState; install: () => Promise<boolean> } {
  return { state: useSyncExternalStore(subscribe, state, () => 'manual' as InstallState), install };
}
