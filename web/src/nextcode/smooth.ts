// A streamed reply, shown smoothly (docs/API-agents.md §30). A model's text comes in bursts: a few
// words at once, then nothing for a moment, as the provider, the CLI and the network hand it on.
// Drawn as it comes, the reply jumps. Here what came is shown a little at a time, at the speed
// that empties what is waiting within a fraction of a second: a burst is spread over the frames
// after it, a steady stream is shown about as fast as it comes, and nothing is held back for long.
//
// While a reply streams, only its last block of Markdown is rendered again on each frame: the
// blocks before it are done and keep what they were drawn as.

import { useEffect, useRef, useState } from 'react';

/** How long a burst takes to be shown while the reply streams, and once it is complete. */
export const LAG_LIVE_MS = 260;
export const LAG_DONE_MS = 140;
/** The slowest it goes, in characters a second, so the end of a burst does not crawl. */
export const MIN_RATE = 90;

/**
 * How much of the text shows after dtMs more: what is waiting, spread over the lag. `shown` may
 * hold a fraction (carried to the next frame); the caller shows Math.floor of it.
 */
export function advance(shown: number, total: number, dtMs: number, done: boolean): number {
  if (shown >= total) return total;
  const waiting = total - shown;
  const lag = done ? LAG_DONE_MS : LAG_LIVE_MS;
  const rate = Math.max(MIN_RATE, (waiting * 1000) / lag);
  // a long pause (a background tab, a slow frame) does not show a burst all at once
  const step = (rate * Math.min(dtMs, 64)) / 1000;
  return Math.min(total, shown + Math.max(step, 0.5));
}

/** Where to cut text at n so that a character outside the BMP is not cut in half. */
export function cutAt(text: string, n: number): number {
  if (n <= 0) return 0;
  if (n >= text.length) return text.length;
  const c = text.charCodeAt(n - 1);
  return c >= 0xd800 && c <= 0xdbff ? n + 1 : n;
}

/**
 * How far each streamed block was shown, by its id: the item that replaces a block when it is
 * complete goes on from there, instead of showing the rest at once.
 */
const shownSoFar = new Map<string, number>();

/** For tests. */
export function forgetShown() {
  shownSoFar.clear();
}

const hidden = () => typeof document !== 'undefined' && document.hidden;

/**
 * text as it is to be shown now. live: more may come (else what is left drains quickly). id: the
 * streamed block's id, so that its item continues where it was (an item passes its `stream`).
 * fromStart: a block that has just begun streaming, whose first burst came with its start, shows
 * from its first character; otherwise text that was there when the view appeared shows at once
 * (an item loaded with the chat), and only what comes after it animates.
 */
export function useSmoothText(text: string, live: boolean, id?: string, fromStart = false): string {
  const [start] = useState(() => {
    const from = id ? shownSoFar.get(id) : undefined;
    if (from !== undefined && from < text.length) return from;
    return fromStart && !hidden() ? 0 : text.length;
  });
  const shown = useRef(start);
  const [n, setN] = useState(start);
  const frame = useRef(0);
  const last = useRef(0);
  const latest = useRef({ text, live });
  latest.current = { text, live };

  useEffect(() => {
    const total = text.length;
    if (shown.current > total) shown.current = total; // the text was replaced by a shorter one
    if (shown.current >= total) {
      setN(total);
      if (id) shownSoFar.set(id, total);
      return;
    }
    if (hidden()) {
      // no frames come to a hidden tab: the text is there when it shows again
      shown.current = total;
      setN(total);
      if (id) shownSoFar.set(id, total);
      return;
    }
    if (frame.current) return; // already running: it reads the newest text
    last.current = performance.now();
    const step = (now: number) => {
      const { text: t, live: l } = latest.current;
      const dt = now - last.current;
      last.current = now;
      shown.current = hidden() ? t.length : advance(shown.current, t.length, dt, !l);
      const k = cutAt(t, Math.floor(shown.current));
      setN(k);
      if (id) shownSoFar.set(id, k);
      frame.current = shown.current < t.length ? requestAnimationFrame(step) : 0;
    };
    frame.current = requestAnimationFrame(step);
  }, [text, live, id]);

  useEffect(
    () => () => {
      if (frame.current) cancelAnimationFrame(frame.current);
      frame.current = 0;
      // the block's item takes over, or it is gone: kept for a moment, then not needed any more
      if (id) window.setTimeout(() => shownSoFar.delete(id), 2000);
    },
    [id],
  );

  return n >= text.length ? text : text.slice(0, cutAt(text, n));
}

const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const LIST = /^ {0,3}([-*+]|\d{1,9}[.)])(\s|$)/;

/**
 * Markdown cut into the blocks that are done and the one still being written. A block ends at a
 * blank line outside a code fence when what follows starts at the margin and is not the next
 * item of a list: Markdown then draws the two apart the same way whether they are rendered
 * together or one by one. The rest is the tail, the only part that changes as text comes.
 */
export function splitBlocks(text: string): { done: string[]; tail: string } {
  const lines = text.split('\n');
  const done: string[] = [];
  let from = 0;
  let fence = '';
  let blank = false;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const f = FENCE.exec(line);
    if (fence) {
      if (f && f[1][0] === fence[0] && f[1].length >= fence.length && !line.slice(f[0].length).trim()) fence = '';
      blank = false;
      continue;
    }
    if (!line.trim()) {
      blank = true;
      continue;
    }
    // a block starts here: the one before it is done, if Markdown cannot join the two
    if (blank && i > from && !/^\s/.test(line) && !LIST.test(line) && i < lines.length - 1) {
      done.push(lines.slice(from, i).join('\n'));
      from = i;
    }
    blank = false;
    if (f) fence = f[1];
  }
  return { done, tail: lines.slice(from).join('\n') };
}
