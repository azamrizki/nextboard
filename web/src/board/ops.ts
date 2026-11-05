// Pure board-op semantics (docs/API-board.md §2): apply a batch to a board state and
// compute the batch that undoes it. Untouched nodes keep their object identity, so
// React only re-renders what changed.

import type { BoardEdge, BoardNode, NodePatch, Op } from '../lib/types';

export interface DocState {
  /** z order: last = top. */
  nodes: BoardNode[];
  edges: BoardEdge[];
}

export const EMPTY_STATE: DocState = { nodes: [], edges: [] };

export function patchNode(n: BoardNode, set: NodePatch): BoardNode {
  const next: BoardNode = { ...n };
  if (set.x !== undefined) next.x = set.x;
  if (set.y !== undefined) next.y = set.y;
  if (set.w !== undefined) next.w = set.w;
  if (set.h !== undefined) next.h = set.h;
  if (set.rotation !== undefined) next.rotation = set.rotation;
  if (set.locked !== undefined) next.locked = set.locked;
  if (set.data) next.data = { ...(n.data ?? {}), ...set.data };
  return next;
}

/** Apply ops in order (last-writer-wins per node, like the hub). */
export function applyOps(state: DocState, ops: Op[]): DocState {
  if (ops.length === 0) return state;
  let nodes = state.nodes;
  let edges = state.edges;
  let nodesCopied = false;
  let edgesCopied = false;
  let index: Map<string, number> | null = null;

  const ensureNodes = () => {
    if (!nodesCopied) {
      nodes = nodes.slice();
      nodesCopied = true;
    }
  };
  const ensureEdges = () => {
    if (!edgesCopied) {
      edges = edges.slice();
      edgesCopied = true;
    }
  };
  const idx = (): Map<string, number> => {
    if (!index) {
      index = new Map();
      nodes.forEach((n, i) => index!.set(n.id, i));
    }
    return index;
  };

  for (const op of ops) {
    switch (op.op) {
      case 'put': {
        if (!op.node?.id) break;
        ensureNodes();
        const i = idx().get(op.node.id);
        if (i === undefined) {
          idx().set(op.node.id, nodes.length);
          nodes.push(op.node);
        } else {
          nodes[i] = op.node;
        }
        break;
      }
      case 'patch': {
        const i = idx().get(op.id);
        if (i === undefined) break;
        ensureNodes();
        nodes[i] = patchNode(nodes[i], op.set ?? {});
        break;
      }
      case 'del': {
        const i = idx().get(op.id);
        if (i === undefined) break;
        ensureNodes();
        nodes.splice(i, 1);
        index = null;
        if (edges.some((e) => e.source === op.id || e.target === op.id)) {
          edges = edges.filter((e) => e.source !== op.id && e.target !== op.id);
          edgesCopied = true;
        }
        break;
      }
      case 'z': {
        const i = idx().get(op.id);
        if (i === undefined) break;
        if (op.to === 'front' && i === nodes.length - 1) break;
        if (op.to === 'back' && i === 0) break;
        ensureNodes();
        const [n] = nodes.splice(i, 1);
        if (op.to === 'front') nodes.push(n);
        else nodes.unshift(n);
        index = null;
        break;
      }
      case 'putEdge': {
        if (!op.edge?.id) break;
        ensureEdges();
        const i = edges.findIndex((e) => e.id === op.edge.id);
        if (i < 0) edges.push(op.edge);
        else edges[i] = op.edge;
        break;
      }
      case 'delEdge': {
        const i = edges.findIndex((e) => e.id === op.id);
        if (i < 0) break;
        ensureEdges();
        edges.splice(i, 1);
        break;
      }
    }
  }
  if (!nodesCopied && !edgesCopied) return state;
  return { nodes, edges };
}

function sameOrder(a: BoardNode[], b: BoardNode[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i].id !== b[i].id) return false;
  return true;
}

/** Ops that move nodes in `cur` back into the z order of `want` (same id set). */
function zFix(cur: DocState, want: BoardNode[], candidates: Iterable<string>): Op[] {
  if (sameOrder(cur.nodes, want)) return [];
  const wantIds = want.map((n) => n.id);
  // A single front/back move of one of the touched nodes is enough in the common case.
  for (const id of candidates) {
    for (const to of ['front', 'back'] as const) {
      const tried = applyOps(cur, [{ op: 'z', id, to }]);
      if (sameOrder(tried.nodes, want)) return [{ op: 'z', id, to }];
    }
  }
  // General case: bring everything from the first mismatch to the front, in order.
  let k = 0;
  while (k < want.length && cur.nodes[k]?.id === wantIds[k]) k++;
  return wantIds.slice(k).map((id) => ({ op: 'z', id, to: 'front' }) as Op);
}

/**
 * Ops that restore `before` for everything `ops` touches. Single patches invert to a
 * patch of the old values (so unrelated concurrent edits survive); anything else
 * restores the whole node. z order is restored exactly.
 */
export function invertOps(before: DocState, ops: Op[]): Op[] {
  const touches = new Map<string, Op[]>();
  let edgeTouched = false;
  for (const op of ops) {
    if (op.op === 'putEdge' || op.op === 'delEdge') {
      edgeTouched = true;
      continue;
    }
    const id = op.op === 'put' ? op.node.id : op.id;
    if (op.op === 'del') edgeTouched = true;
    const list = touches.get(id);
    if (list) list.push(op);
    else touches.set(id, [op]);
  }
  const prev = new Map(before.nodes.map((n) => [n.id, n]));
  const inv: Op[] = [];

  for (const [id, list] of touches) {
    const old = prev.get(id);
    if (!old) {
      if (list.some((o) => o.op === 'put')) inv.push({ op: 'del', id });
      continue;
    }
    if (list.every((o) => o.op === 'patch')) {
      const set: NodePatch = {};
      const keys = new Set<string>();
      const dataKeys = new Set<string>();
      for (const o of list) {
        if (o.op !== 'patch') continue;
        for (const k of Object.keys(o.set ?? {})) {
          if (k === 'data') Object.keys(o.set.data ?? {}).forEach((d) => dataKeys.add(d));
          else keys.add(k);
        }
      }
      const s = set as Record<string, unknown>;
      for (const k of keys) {
        const v = (old as unknown as Record<string, unknown>)[k];
        s[k] = v === undefined ? (k === 'locked' ? false : k === 'rotation' ? 0 : v) : v;
      }
      if (dataKeys.size) {
        set.data = {};
        for (const d of dataKeys) set.data[d] = old.data?.[d] ?? null;
      }
      inv.push({ op: 'patch', id, set });
    } else if (list.every((o) => o.op === 'z')) {
      // handled by the z fix below
    } else {
      inv.push({ op: 'put', node: old });
    }
  }

  if (edgeTouched) {
    const after = applyOps(before, ops);
    const afterEdges = new Map(after.edges.map((e) => [e.id, e]));
    const beforeEdges = new Map(before.edges.map((e) => [e.id, e]));
    for (const e of before.edges) if (afterEdges.get(e.id) !== e) inv.push({ op: 'putEdge', edge: e });
    for (const e of after.edges) if (!beforeEdges.has(e.id)) inv.push({ op: 'delEdge', id: e.id });
  }

  const restored = applyOps(applyOps(before, ops), inv);
  return inv.concat(zFix(restored, before.nodes, touches.keys()));
}

/**
 * Queue an op, folding it into an earlier op on the same node when nothing in between
 * touched that node (keeps drags and typing down to one op per flush).
 */
export function queueOp(queue: Op[], op: Op): void {
  if (op.op === 'patch') {
    for (let i = queue.length - 1; i >= 0; i--) {
      const q = queue[i];
      const qid = q.op === 'put' ? q.node.id : q.op === 'putEdge' ? '' : q.id;
      if (qid !== op.id) continue;
      if (q.op === 'patch') {
        const data = q.set.data || op.set.data ? { ...(q.set.data ?? {}), ...(op.set.data ?? {}) } : undefined;
        queue[i] = { op: 'patch', id: op.id, set: { ...q.set, ...op.set, ...(data ? { data } : {}) } };
        return;
      }
      if (q.op === 'put') {
        queue[i] = { op: 'put', node: patchNode(q.node, op.set) };
        return;
      }
      break; // del / z in between: keep order
    }
  }
  queue.push(op);
}

/** A group's `members` are node ids; other data is left as it is. */
function mapMembers(data: Record<string, unknown>, map: (ids: string[]) => string[]): Record<string, unknown> {
  return Array.isArray(data?.members) ? { ...data, members: map(data.members as string[]) } : data;
}

/** Remap node ids inside ops (terminal nodes get new ids when re-created by undo). */
export function remapOps(ops: Op[], map: (id: string) => string): Op[] {
  const members = (ids: string[]) => ids.map(map);
  return ops.map((op) => {
    switch (op.op) {
      case 'put':
        return { op: 'put', node: { ...op.node, id: map(op.node.id), data: mapMembers(op.node.data, members) } };
      case 'patch':
        return { ...op, id: map(op.id), set: op.set.data ? { ...op.set, data: mapMembers(op.set.data, members) } : op.set };
      case 'del':
      case 'z':
        return map(op.id) === op.id ? op : { ...op, id: map(op.id) };
      case 'putEdge':
        return { op: 'putEdge', edge: { ...op.edge, source: map(op.edge.source), target: map(op.edge.target) } };
      default:
        return op;
    }
  });
}

/** Ops without what points at nodes that are not there; the hub rejects a whole request over one. */
export function withoutMissing(ops: Op[], nodes: BoardNode[]): Op[] {
  const here = new Set(nodes.map((n) => n.id));
  for (const op of ops) if (op.op === 'put') here.add(op.node.id);
  const members = (ids: string[]) => ids.filter((id) => here.has(id));
  return ops.flatMap((op): Op[] => {
    switch (op.op) {
      case 'put':
        return [{ op: 'put', node: { ...op.node, data: mapMembers(op.node.data, members) } }];
      case 'patch':
        if (!here.has(op.id)) return [];
        return [{ ...op, set: op.set.data ? { ...op.set, data: mapMembers(op.set.data, members) } : op.set }];
      case 'z':
        return here.has(op.id) ? [op] : [];
      case 'putEdge':
        return here.has(op.edge.source) && here.has(op.edge.target) ? [op] : [];
      default:
        return [op];
    }
  });
}
