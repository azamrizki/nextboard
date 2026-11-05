// Light and dark (Settings › Appearance, the header button). Per browser, like the text size.
// The colours are all in styles/tokens.css; here is only which block applies: data-theme on
// <html>, set before the first paint by public/theme-boot.js.

import { useSyncExternalStore } from 'react';
import { prefs } from './format';

export type ThemePref = 'system' | 'light' | 'dark';
export type Theme = 'light' | 'dark';
/** Agent CLIs can be set up for a dark terminal, so a console may stay dark in a light app. */
export type ConsolePref = 'match' | 'dark';

const media = typeof window.matchMedia === 'function' ? window.matchMedia('(prefers-color-scheme: light)') : null;

const readPref = (): ThemePref => {
  const v = prefs.get('theme');
  return v === 'light' || v === 'dark' ? v : 'system';
};
const readConsole = (): ConsolePref => (prefs.get('consoleTheme') === 'dark' ? 'dark' : 'match');
const resolve = (p: ThemePref): Theme => (p === 'system' ? (media?.matches ? 'light' : 'dark') : p);

interface State {
  pref: ThemePref;
  theme: Theme;
  consolePref: ConsolePref;
}

let state: State = { pref: readPref(), theme: resolve(readPref()), consolePref: readConsole() };

const listeners = new Set<() => void>();

function apply() {
  const root = document.documentElement;
  if (root.getAttribute('data-theme') !== state.theme) {
    // every hover transition at once would smear the change over the whole page
    root.classList.add('theme-switching');
    root.setAttribute('data-theme', state.theme);
    requestAnimationFrame(() => requestAnimationFrame(() => root.classList.remove('theme-switching')));
  }
  const bg = getComputedStyle(root).getPropertyValue('--bg').trim();
  if (bg) document.querySelector('meta[name="theme-color"]')?.setAttribute('content', bg);
}

function update(next: Partial<State>) {
  state = { ...state, ...next };
  apply();
  listeners.forEach((l) => l());
}

export function setThemePref(pref: ThemePref) {
  prefs.set('theme', pref);
  update({ pref, theme: resolve(pref) });
}

export function setConsolePref(consolePref: ConsolePref) {
  prefs.set('consoleTheme', consolePref);
  update({ consolePref });
}

export const currentAppTheme = (): Theme => state.theme;

/** For code outside React that holds colours of its own (Monaco). Returns the unsubscribe. */
export function onThemeChange(fn: () => void): () => void {
  let last = state.theme;
  const l = () => {
    if (state.theme === last) return;
    last = state.theme;
    fn();
  };
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useTheme(): State {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => state,
  );
}

/** data-theme for the element around a terminal: set only when it differs from the page. */
export function useConsoleThemeAttr(): 'dark' | undefined {
  return useTheme().consolePref === 'dark' ? 'dark' : undefined;
}

// the system's choice changing while the app is open
media?.addEventListener('change', () => {
  if (state.pref === 'system') update({ theme: resolve('system') });
});

// another tab of this browser changed it
window.addEventListener('storage', (e) => {
  if (e.key === 'nb.theme' || e.key === 'nb.consoleTheme') {
    const pref = readPref();
    update({ pref, theme: resolve(pref), consolePref: readConsole() });
  }
});

apply();
