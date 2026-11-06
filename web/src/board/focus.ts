// Focus mode (DESIGN.md §3.5): one card fills the board view, on this screen only. Nothing is
// written to the board, so a phone that focuses a console does not move anything on the desktop;
// a console does take the size it is shown at (the last screen to resize it wins).
//
// The card is in the URL (?card=<node id>): Back on Android leaves it, a reload keeps it, and a
// link can open one.

import type { BoardNode } from '../lib/types';
import { navigate, useSearchParam } from '../lib/router';

export const FOCUS_PARAM = 'card';

/** Background things are not cards: frames, shapes, lines, drawings, loose text. */
const NOT_CARDS = new Set(['group', 'shape', 'line', 'ink', 'text']);
export const isFocusable = (n: BoardNode) => !NOT_CARDS.has(n.kind);

function urlWith(id: string | null): string {
  const url = new URL(window.location.href);
  if (id) url.searchParams.set(FOCUS_PARAM, id);
  else url.searchParams.delete(FOCUS_PARAM);
  return url.pathname + url.search;
}

/** Fill the view with this card. From another focused card it takes that one's place in history. */
export function focusCard(id: string) {
  const from = new URLSearchParams(window.location.search).get(FOCUS_PARAM);
  if (from === id) return;
  navigate(urlWith(id), !!from, { nbFocus: true });
}

/** Back to the board: the history entry focusing added goes, so Back does not reopen it. */
export function leaveFocus() {
  if (!new URLSearchParams(window.location.search).get(FOCUS_PARAM)) return;
  if ((window.history.state as { nbFocus?: boolean } | null)?.nbFocus) window.history.back();
  else navigate(urlWith(null), true);
}

export function useFocusedCard(): string | null {
  return useSearchParam(FOCUS_PARAM);
}

const ROW = 160;

/** Cards in reading order, for the previous and next buttons: rows from the top, then left to right. */
export function cardOrder(nodes: BoardNode[]): BoardNode[] {
  return nodes
    .filter(isFocusable)
    .slice()
    .sort((a, b) => Math.round(a.y / ROW) - Math.round(b.y / ROW) || a.x - b.x || a.y - b.y);
}
