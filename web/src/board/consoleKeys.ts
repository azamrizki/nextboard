// What the touch key bar (KeyBar.tsx) needs from a live console: send keys it has no key for on a
// phone's keyboard, read its text for copying, and hand it files. useTerminal registers each
// console here while it is open.
//
// Ctrl and Alt are one-shot: armed from the bar, they change the next key typed on the on-screen
// keyboard (Ctrl then c is ^C), then let go.

import { useSyncExternalStore } from 'react';

export interface LiveConsole {
  /** Raw input, as if typed. */
  send(data: string): void;
  /** The cursor keys' sequences depend on the program's mode (vim, less, agent CLIs). */
  appCursor(): boolean;
  /** The screen and the scrollback above it, as plain text. */
  text(maxLines: number): string;
  attach(files: File[]): Promise<void>;
  focus(): void;
}

const live = new Map<string, LiveConsole>();

export function registerConsole(sessionId: string, c: LiveConsole): () => void {
  live.set(sessionId, c);
  return () => {
    if (live.get(sessionId) === c) live.delete(sessionId);
  };
}

export const liveConsole = (sessionId: string) => live.get(sessionId);

// ---- one-shot modifiers ----

let mods = { ctrl: false, alt: false };
const listeners = new Set<() => void>();
const publish = () => listeners.forEach((fn) => fn());

export function toggleMod(m: 'ctrl' | 'alt') {
  mods = { ...mods, [m]: !mods[m] };
  publish();
}

export function useMods() {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => mods,
  );
}

/** The armed modifiers applied to one piece of typed input, which then disarms them. */
export function applyMods(d: string): string {
  if (!mods.ctrl && !mods.alt) return d;
  let out = d;
  if (mods.ctrl && d.length === 1) {
    const c = d.toLowerCase().charCodeAt(0);
    // a–z, and @ [ \ ] ^ _ as a terminal maps them
    if (c >= 97 && c <= 122) out = String.fromCharCode(c - 96);
    else if (d === ' ' || d === '@' || d === '2') out = '\x00';
    else if ('[\\]^_'.includes(d)) out = String.fromCharCode(d.charCodeAt(0) - 64);
  }
  if (mods.alt) out = `\x1b${out}`;
  mods = { ctrl: false, alt: false };
  publish();
  return out;
}

export type KeyName = 'esc' | 'tab' | 'backtab' | 'enter' | 'up' | 'down' | 'left' | 'right' | 'ctrl-c';

export function keySequence(k: KeyName, appCursor: boolean): string {
  const arrow = (c: string) => (appCursor ? `\x1bO${c}` : `\x1b[${c}`);
  switch (k) {
    case 'esc':
      return '\x1b';
    case 'tab':
      return '\t';
    case 'backtab':
      return '\x1b[Z';
    case 'enter':
      return '\r';
    case 'up':
      return arrow('A');
    case 'down':
      return arrow('B');
    case 'right':
      return arrow('C');
    case 'left':
      return arrow('D');
    case 'ctrl-c':
      return '\x03';
  }
}
