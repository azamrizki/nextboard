// Making room when a card grows (DESIGN.md §3.5): like inserting rows and columns, what lies
// right of the card moves right together and what lies below it moves down together. Tiles
// keep their arrangement among each other, and no two that were apart can end up overlapping.
// Only as far as the nearest one needs. What lies left of it or above it is out of the way.

import type { BoardNode, NodeLayout } from '../lib/types';
import { readGroup } from './kinds';
import { placeNear } from './layout';

const GAP = 40;
/** The hub refuses coordinates beyond this (docs/API-board.md §2). */
const BOARD_MAX = 20000;
/** Drawn behind the work: one around the card stays where it is. */
const BACKDROPS = new Set(['shape', 'line']);

export interface Moved {
  id: string;
  dx: number;
  dy: number;
}

interface Block {
  ids: string[];
  rect: NodeLayout;
  /** Pen strokes follow their neighbours but never push anything. */
  soft: boolean;
  backdrop: boolean;
}

const hits = (a: NodeLayout, b: NodeLayout, margin = 0) =>
  a.x < b.x + b.w + margin && a.x + a.w + margin > b.x && a.y < b.y + b.h + margin && a.y + a.h + margin > b.y;

/** A group frame moves with its members. A locked tile, and a group holding one, stays. */
function blocksOf(nodes: BoardNode[], cardId: string): Block[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const inGroup = new Set<string>();
  const blocks: Block[] = [];
  for (const g of nodes) {
    if (g.kind !== 'group') continue;
    const members = readGroup(g).members.flatMap((id) => byId.get(id) ?? []);
    members.forEach((m) => inGroup.add(m.id));
    if (g.locked || members.some((m) => m.locked)) continue;
    const all = [g, ...members];
    const x = Math.min(...all.map((n) => n.x));
    const y = Math.min(...all.map((n) => n.y));
    const rect = { x, y, w: Math.max(...all.map((n) => n.x + n.w)) - x, h: Math.max(...all.map((n) => n.y + n.h)) - y };
    blocks.push({ ids: all.map((n) => n.id), rect, soft: false, backdrop: false });
  }
  for (const n of nodes) {
    if (n.id === cardId || n.kind === 'group' || n.locked || inGroup.has(n.id)) continue;
    blocks.push({ ids: [n.id], rect: { x: n.x, y: n.y, w: n.w, h: n.h }, soft: n.kind === 'ink', backdrop: BACKDROPS.has(n.kind) });
  }
  return blocks;
}

/** What has to move for the card at `from` to grow to `to`, which keeps its top-left corner. */
export function pushAside(nodes: BoardNode[], cardId: string, from: NodeLayout, to: NodeLayout): Moved[] {
  const blocks = blocksOf(nodes, cardId);
  const oldRight = from.x + from.w;
  const oldBottom = from.y + from.h;
  const isRight = (b: Block) => b.rect.x >= oldRight;
  const isBelow = (b: Block) => b.rect.y >= oldBottom;
  const pushers = blocks.filter((b) => !b.soft && hits(b.rect, to, GAP));

  // one that lies both right and below clears the card by moving right
  const dx = Math.max(0, ...pushers.filter(isRight).map((b) => to.x + to.w + GAP - b.rect.x));
  const dy = Math.max(0, ...pushers.filter((b) => !isRight(b) && isBelow(b)).map((b) => to.y + to.h + GAP - b.rect.y));

  const placed = new Map<Block, { dx: number; dy: number }>();
  for (const b of blocks) {
    const mx = isRight(b) ? Math.min(dx, BOARD_MAX - b.rect.x - b.rect.w) : 0;
    const my = isBelow(b) ? Math.min(dy, BOARD_MAX - b.rect.y - b.rect.h) : 0;
    placed.set(b, { dx: mx, dy: my });
  }

  // already overlapping the card, or in its way without lying on either side: the nearest free spot
  const taken: NodeLayout[] = [to];
  const stuck: Block[] = [];
  for (const b of blocks) {
    if (b.soft) continue;
    const inTheWay = !isRight(b) && !isBelow(b) && hits(b.rect, to);
    if (inTheWay && !b.backdrop) stuck.push(b);
    else if (!b.backdrop) {
      const p = placed.get(b)!;
      taken.push({ ...b.rect, x: b.rect.x + p.dx, y: b.rect.y + p.dy });
    }
  }
  for (const b of stuck) {
    const spot = placeNear(taken, { x: to.x + to.w + GAP + b.rect.w / 2, y: b.rect.y + b.rect.h / 2 }, b.rect.w, b.rect.h);
    placed.set(b, { dx: spot.x - b.rect.x, dy: spot.y - b.rect.y });
    taken.push(spot);
  }

  const out: Moved[] = [];
  for (const b of blocks) {
    const p = placed.get(b)!;
    if (p.dx || p.dy) for (const id of b.ids) out.push({ id, dx: p.dx, dy: p.dy });
  }
  return out;
}
