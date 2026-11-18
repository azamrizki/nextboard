// Wire routes per edge, kept between frames. At rest every change of the tiles routes every
// wire afresh (a moved tile can open or block any path). During a drag that is O(edges ×
// tiles) per frame, so only wires with a moving end are routed; the others keep their path
// until the drag ends, when the snapshot loses `moving` and everything is routed again.

import { routeWire, type Box, type Route } from './wireRoute';

export interface TileBox extends Box {
  id: string;
  solid: boolean;
}

/** The tiles as wires see them; a new object whenever a box or `moving` changes. */
export interface BoxSnapshot {
  boxes: TileBox[];
  byId: Map<string, TileBox>;
  /** Some tile is being dragged. */
  moving: boolean;
}

export function snapshotOf(boxes: TileBox[], moving: boolean): BoxSnapshot {
  return { boxes, byId: new Map(boxes.map((b) => [b.id, b])), moving };
}

const sameBox = (a: Box, b: Box) => a.x === b.x && a.y === b.y && a.w === b.w && a.h === b.h;

export function sameBoxes(a: readonly TileBox[], b: readonly TileBox[]): boolean {
  return a.length === b.length && a.every((o, k) => o.id === b[k].id && o.solid === b[k].solid && sameBox(o, b[k]));
}

export interface CachedRoute {
  snap: BoxSnapshot;
  source: string;
  target: string;
  s?: TileBox;
  t?: TileBox;
  route: Route | null;
}

/** The route of one wire in `snap`, reusing `prev` (the same wire's last answer) when allowed. */
export function routeIn(prev: CachedRoute | undefined, snap: BoxSnapshot, source: string, target: string): CachedRoute {
  if (prev && (prev.source !== source || prev.target !== target)) prev = undefined;
  if (prev?.snap === snap) return prev;
  const s = snap.byId.get(source);
  const t = snap.byId.get(target);
  if (!s || !t) return { snap, source, target, route: null };
  // the same Route object: the edge's selector result is unchanged and it does not re-render
  if (snap.moving && prev?.route && prev.s && prev.t && sameBox(prev.s, s) && sameBox(prev.t, t)) return { snap, source, target, s, t, route: prev.route };
  return { snap, source, target, s, t, route: routeWire(s, t, snap.boxes.filter((b) => b.solid && b.id !== source && b.id !== target)) };
}
