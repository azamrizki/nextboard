// Board nodes → React Flow nodes. The board document is the source of truth; React Flow
// gets derived nodes (identity kept for unchanged ones so memoised tiles skip renders).

import type { Node } from '@xyflow/react';
import type { BoardNode } from '../lib/types';
import { isKnownKind } from './kinds';

export interface TileData extends Record<string, unknown> {
  node: BoardNode;
}
export type FlowNode = Node<TileData>;

/** Kinds that are dragged by their header only (their body is interactive). */
const HEADER_DRAG: Record<string, string> = {
  terminal: '.nb-node-header',
  kanban: '.tile-head',
  calendar: '.tile-head',
  uptime: '.tile-head',
  credentials: '.tile-head',
  file: '.tile-head',
  group: '.group-head',
  orchestrator: '.tile-head',
  memory: '.tile-head',
  web: '.tile-head',
  weblog: '.tile-head',
  branches: '.tile-head',
  editor: '.wb-title, .editor-tile-dup, .editor-tile-loading',
  document: '.tile-head',
  app: '.tile-head',
};

/** Board kind → React Flow node type ("group" is a built-in React Flow type with its own styles). */
const FLOW_TYPE: Record<string, string> = { group: 'nbgroup' };

/** Drawn behind the work: selecting one must not cover the tiles standing on it. */
const BACKDROP = new Set(['group', 'shape', 'line', 'ink']);

/**
 * The tile this client touched last goes on top, like a window. This is only a zIndex on
 * top of the saved order: nothing is sent to the hub, and the DOM stays as it is, because
 * moving a node would reload its iframes and take a terminal's focus.
 */
export class TileStack {
  private order = new Map<string, number>();
  private top = 1;
  private rev = 0;
  private listeners = new Set<() => void>();

  raise(id: string, tell = true) {
    this.order.set(id, ++this.top);
    if (tell) this.changed();
  }
  lower(id: string) {
    this.order.delete(id);
    this.changed();
  }
  z(id: string): number {
    return this.order.get(id) ?? 0;
  }
  private changed() {
    this.rev++;
    this.listeners.forEach((l) => l());
  }
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  getRev = () => this.rev;
}

interface Cached {
  node: BoardNode;
  selected: boolean;
  dragging: boolean;
  editing: boolean;
  z: number;
  flow: FlowNode;
}

export class FlowCache {
  private cache = new Map<string, Cached>();

  build(nodes: BoardNode[], selection: ReadonlySet<string>, dragging: ReadonlySet<string>, editing: string | null, stack: TileStack): FlowNode[] {
    const next = new Map<string, Cached>();
    const out: FlowNode[] = new Array(nodes.length);
    nodes.forEach((n, i) => {
      const selected = selection.has(n.id);
      const drag = dragging.has(n.id);
      const edit = editing === n.id;
      const prev = this.cache.get(n.id);
      const isGroup = n.kind === 'group';
      if (!BACKDROP.has(n.kind) && ((selected && !prev?.selected) || (drag && !prev?.dragging))) stack.raise(n.id, false);
      // group frames sit behind everything; a card filling the view stays over the unraised
      // tiles even right after a reload, when this client hasn't touched anything yet
      // a selected wire too: its tips lie on the outlines of the shapes it joins, and have to be reachable
      const reachable = selected && n.kind === 'line' && Array.isArray(n.data.route);
      const z = isGroup ? -1 : stack.z(n.id) || (reachable ? 2 : n.data.fill ? 1 : 0);
      let c: Cached;
      if (prev && prev.node === n && prev.selected === selected && prev.dragging === drag && prev.editing === edit && prev.z === z) {
        c = prev;
      } else {
        const type = isKnownKind(n.kind) ? (FLOW_TYPE[n.kind] ?? n.kind) : 'unsupported';
        const flow: FlowNode = {
          id: n.id,
          type,
          position: { x: n.x, y: n.y },
          width: n.w,
          height: n.h,
          // sizes are always explicit, so React Flow need not measure before showing a node
          measured: { width: n.w, height: n.h },
          data: prev && prev.node === n ? prev.flow.data : { node: n },
          selected,
          dragging: drag,
          draggable: !n.locked && !edit,
          dragHandle: HEADER_DRAG[n.kind],
          ariaLabel: undefined,
          zIndex: z,
          // group frames are selected through their header (BoardView), so a marquee over
          // members never grabs the frame too
          ...(isGroup ? { selectable: false } : {}),
        };
        c = { node: n, selected, dragging: drag, editing: edit, z, flow };
      }
      next.set(n.id, c);
      out[i] = c.flow;
    });
    this.cache = next;
    return out;
  }
}
