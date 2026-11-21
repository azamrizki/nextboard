// Where a tile is drawn (DESIGN.md §3.19, Code view = Board view): on the board's canvas, or as a
// tab of the Code view, where the same card opens with nothing of the canvas (no resize handles,
// no wire knobs: links are made from the Code view's side bar there).

import { createContext, useContext, type ReactNode } from 'react';

export type TileHostKind = 'canvas' | 'tab';

export const TileHost = createContext<TileHostKind>('canvas');

export const useTileHost = () => useContext(TileHost);

/** What only the canvas has: a tile's wire knobs. */
export function CanvasOnly({ children }: { children: ReactNode }) {
  return useTileHost() === 'canvas' ? <>{children}</> : null;
}
