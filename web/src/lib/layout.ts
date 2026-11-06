// Which layout this screen gets (DESIGN.md §3.0): phone, tablet or desktop, and whether its main
// pointer is a finger. This is the one place that decides; CSS reads the same answer from
// <html data-layout data-input>, and public/theme-boot.js sets both before the first paint with
// the same rules.
//
//   phone    the window is under 600 px wide, or a touch screen whose short side is (in either
//            orientation, and with the keyboard open: the screen, not the window, decides)
//   tablet   under 1200 px wide, or any other touch screen
//   desktop  1200 px or wider, with a mouse
//
// Settings › Appearance can pin one per browser, for a tablet with a keyboard and a trackpad.

import { useSyncExternalStore } from 'react';

export type Layout = 'phone' | 'tablet' | 'desktop';
export type LayoutPref = 'auto' | Layout;
export type Input = 'touch' | 'mouse';

export const PHONE_MAX = 600;
export const DESKTOP_MIN = 1200;
const KEY = 'nb.layout';
/** The window shrinks this much with the keyboard open (interactive-widget=resizes-content). */
const KEYBOARD_MIN = 120;

export interface LayoutState {
  layout: Layout;
  input: Input;
  /** The on-screen keyboard is open: the bottom bars give it the room. */
  keyboard: boolean;
  pref: LayoutPref;
}

export function pickLayout(width: number, screenShort: number, touch: boolean, pref: LayoutPref): Layout {
  if (pref !== 'auto') return pref;
  if (width < PHONE_MAX || (touch && screenShort < PHONE_MAX)) return 'phone';
  if (width < DESKTOP_MIN || touch) return 'tablet';
  return 'desktop';
}

function readPref(): LayoutPref {
  try {
    const v = localStorage.getItem(KEY);
    if (v === 'phone' || v === 'tablet' || v === 'desktop') return v;
  } catch {
    /* storage blocked */
  }
  return 'auto';
}

const coarse = typeof window !== 'undefined' && window.matchMedia ? window.matchMedia('(pointer: coarse)') : null;
const listeners = new Set<() => void>();
let state: LayoutState = { layout: 'desktop', input: 'mouse', keyboard: false, pref: 'auto' };
// The tallest the window has been at this width: the keyboard is what takes the difference.
let fullHeight = 0;
let fullWidth = 0;

function editableFocused(): boolean {
  const el = document.activeElement as HTMLElement | null;
  if (!el) return false;
  if (el.isContentEditable) return true;
  if (el.tagName === 'TEXTAREA' || el.tagName === 'SELECT') return true;
  if (el.tagName !== 'INPUT') return false;
  const t = (el as HTMLInputElement).type;
  return !['button', 'checkbox', 'radio', 'range', 'color', 'file', 'submit', 'reset', 'image'].includes(t);
}

function compute(pref = state.pref): LayoutState {
  const touch = !!coarse?.matches;
  const w = window.innerWidth;
  // the visual viewport shrinks with the keyboard even where the layout viewport does not
  const h = Math.min(window.innerHeight, Math.round(window.visualViewport?.height ?? window.innerHeight));
  if (w !== fullWidth) {
    fullWidth = w;
    fullHeight = h;
  } else fullHeight = Math.max(fullHeight, h);
  const short = Math.min(window.screen?.width || w, window.screen?.height || h);
  return {
    layout: pickLayout(w, short, touch, pref),
    input: touch ? 'touch' : 'mouse',
    keyboard: touch && editableFocused() && fullHeight - h >= KEYBOARD_MIN,
    pref,
  };
}

function apply(next: LayoutState) {
  const root = document.documentElement;
  root.setAttribute('data-layout', next.layout);
  root.setAttribute('data-input', next.input);
  if (next.keyboard) root.setAttribute('data-keyboard', '');
  else root.removeAttribute('data-keyboard');
  if (next.layout === state.layout && next.input === state.input && next.keyboard === state.keyboard && next.pref === state.pref) return;
  state = next;
  listeners.forEach((fn) => fn());
}

let installed = false;
function install() {
  if (installed || typeof window === 'undefined') return;
  installed = true;
  state = { ...state, pref: readPref() };
  const update = () => apply(compute());
  window.addEventListener('resize', update);
  window.addEventListener('orientationchange', update);
  window.visualViewport?.addEventListener('resize', update);
  // focus moves before the keyboard opens or closes; the resize that follows does the rest
  document.addEventListener('focusin', update);
  document.addEventListener('focusout', () => window.setTimeout(update, 0));
  coarse?.addEventListener('change', update);
  update();
}
install();

export const layoutState = () => state;

export function setLayoutPref(pref: LayoutPref) {
  try {
    if (pref === 'auto') localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, pref);
  } catch {
    /* storage blocked: still applies to this tab */
  }
  apply(compute(pref));
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

export function useLayout(): LayoutState {
  return useSyncExternalStore(subscribe, () => state);
}
