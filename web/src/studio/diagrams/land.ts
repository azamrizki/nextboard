// Landing a diagram on the board: one undoable step, in the free spot nearest a point.

import type { BoardController } from '../../board/controller';
import { rectOf } from '../../board/controller';
import { placeNear } from '../../board/layout';
import type { BoardNode } from '../../lib/types';
import type { Diagram, DiagramStyle } from '../types';
import { diagramNodes, diagramSize, INK, type DiagramLook } from './layout';

export type Reveal = (rect: { x: number; y: number; w: number; h: number }) => void;

export function landDiagram(ctl: BoardController, d: Diagram, style: DiagramStyle, diagramId: string, center: { x: number; y: number }, reveal?: Reveal, look: DiagramLook = {}): string[] {
  const { w, h } = diagramSize(d, style, look);
  const rect = placeNear(
    ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf),
    center,
    w,
    h,
  );
  const nodes = diagramNodes(d, style, diagramId, rect.x, rect.y, look);
  // not selected: the style inspector would cover the result; one undo step removes it
  ctl.addNodes(nodes, false);
  reveal?.(rect);
  return nodes.map((n) => n.id);
}

/** How a diagram on the board was landed, for an export that should look the same. */
export function landedLook(nodes: BoardNode[], diagramId: string): DiagramLook {
  const lines = nodes.filter((n) => n.kind === 'line' && n.data.diagramId === diagramId);
  return {
    arrows: lines.some((n) => Array.isArray(n.data.route)) ? 'wiring' : 'curved',
    arrowColor: lines.some((n) => n.data.stroke !== INK) ? 'color' : 'mono',
  };
}
