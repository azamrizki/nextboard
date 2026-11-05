// Board clipboard: copy / cut / paste / duplicate of nodes, within and across workspaces
// (and browser tabs, through the system clipboard as JSON text). A copied terminal is
// pasted as a NEW session of the same preset / machine / cwd — the conversation is not
// copied.

import { newId } from '../lib/ids';
import type { BoardEdge, BoardNode, NodeLayout, Op } from '../lib/types';
import { rectOf, type BoardController, type TerminalSpec } from './controller';
import { boundsOf } from './geometry';
import { createGroupOps, groupNameProblem, nextGroupName } from './groups';
import { readGroup } from './kinds';

const MARK = 'nextboard/nodes';
/** A preview line stays with the console that opened the page: a copy did not. */
const COPIED_EDGES = new Set(['wire', 'forward', 'log']);

export interface ClipPayload {
  type: typeof MARK;
  v: 1;
  nodes: BoardNode[];
  /** Terminal node id → how to start a terminal like it. */
  terminals: Record<string, Omit<TerminalSpec, 'nodeId'>>;
  /**
   * Connections between the copied tiles. One that leaves the selection stays behind: a
   * copy of a console does not start taking orders or forwarding by itself.
   */
  edges?: BoardEdge[];
}

/** Last copied payload (works even when the system clipboard is unavailable). */
let memory: ClipPayload | null = null;

export function makePayload(ctl: BoardController, ids: Iterable<string>): ClipPayload | null {
  const set = new Set(ids);
  const nodes = ctl.nodes.filter((n) => set.has(n.id));
  if (nodes.length === 0) return null;
  const terminals: ClipPayload['terminals'] = {};
  const plain: BoardNode[] = [];
  for (const n of nodes) {
    if (n.kind === 'terminal') {
      const spec = ctl.specOf(n);
      if (!spec) continue;
      terminals[n.id] = { machineId: spec.machineId, preset: spec.preset, cwd: spec.cwd, name: spec.name, rect: spec.rect };
    }
    plain.push(n);
  }
  const kept = new Set(plain.map((n) => n.id));
  const edges = ctl.sync.state.edges.filter((e) => COPIED_EDGES.has(e.kind) && kept.has(e.source) && kept.has(e.target));
  return { type: MARK, v: 1, nodes: plain, terminals, edges };
}

export function remember(p: ClipPayload) {
  memory = p;
}
export function recall(): ClipPayload | null {
  return memory;
}

export function serialize(p: ClipPayload): string {
  return JSON.stringify(p);
}

export function parsePayload(text: string): ClipPayload | null {
  if (!text || text[0] !== '{' || !text.includes(MARK)) return null;
  try {
    const p = JSON.parse(text) as ClipPayload;
    if (p?.type !== MARK || !Array.isArray(p.nodes)) return null;
    p.terminals = p.terminals && typeof p.terminals === 'object' ? p.terminals : {};
    p.edges = Array.isArray(p.edges) ? p.edges : [];
    return p;
  } catch {
    return null;
  }
}

/**
 * Insert a payload. `at` = where the top-left of the copied group goes (board coords);
 * without it the group is offset by 32 px from its original position.
 */
export function pastePayload(ctl: BoardController, p: ClipPayload, at?: { x: number; y: number }) {
  const box = boundsOf(p.nodes.map(rectOf));
  if (!box) return;
  const dx = at ? Math.round(at.x - box.x) : 32;
  const dy = at ? Math.round(at.y - box.y) : 32;
  const fresh: BoardNode[] = [];
  const spawns: { from: string; spec: Omit<TerminalSpec, 'nodeId'>; rect: NodeLayout }[] = [];
  const ids = new Map<string, string>();
  for (const n of p.nodes) {
    if (n.kind === 'terminal') {
      const spec = p.terminals[n.id];
      if (spec) spawns.push({ from: n.id, spec, rect: { x: n.x + dx, y: n.y + dy, w: n.w, h: n.h } });
      continue;
    }
    // a group frame belongs to its members: it comes back around their copies, below
    if (n.kind === 'group') continue;
    const data = structuredClone(n.data ?? {});
    if (n.kind === 'memory') delete data.lastPush;
    if (n.kind === 'editor') delete data.fill;
    // each app has one session: the copy offers to open it instead of sharing the display
    if (n.kind === 'app' || n.kind === 'web') delete data.sessionId;
    const id = newId('n');
    ids.set(n.id, id);
    fresh.push({ ...n, id, x: n.x + dx, y: n.y + dy, data });
  }
  if (!spawns.length) {
    // all in one undo step
    if (fresh.length) {
      ctl.apply([...fresh.map((node) => ({ op: 'put', node }) as Op), ...linkOps(p, ids)]);
      ctl.select(fresh.map((n) => n.id));
    }
    return;
  }
  if (fresh.length) ctl.addNodes(fresh);
  void Promise.all(
    spawns.map(async (s) => {
      const made = await ctl.spawn({ ...s.spec, rect: s.rect });
      const node = made && (await nodeOfSession(ctl, made.id));
      if (node) ids.set(s.from, node.id);
    }),
  ).then(() => {
    const ops = [...groupOps(ctl, p, ids), ...linkOps(p, ids)];
    if (ops.length) ctl.apply(ops);
  });
}

/** The hub adds a console's node with the session; it reaches this client a moment later. */
async function nodeOfSession(ctl: BoardController, sessionId: string): Promise<BoardNode | undefined> {
  for (let i = 0; i < 50; i++) {
    const n = ctl.nodes.find((x) => x.data.sessionId === sessionId);
    if (n) return n;
    await new Promise((r) => setTimeout(r, 100));
  }
  return undefined;
}

/** Copied groups, around the copies of their members. */
function groupOps(ctl: BoardController, p: ClipPayload, ids: Map<string, string>): Op[] {
  const ops: Op[] = [];
  let nodes = ctl.nodes;
  for (const g of p.nodes) {
    if (g.kind !== 'group') continue;
    const old = readGroup(g);
    const members = old.members.flatMap((m) => ids.get(m) ?? []);
    if (!members.length) continue;
    const base = old.name.slice(0, 21);
    let name = nextGroupName(nodes);
    for (let i = 2; i < 100; i++) {
      if (!groupNameProblem(`${base} ${i}`, nodes)) {
        name = `${base} ${i}`;
        break;
      }
    }
    const made = createGroupOps(nodes, members, { name, role: old.role });
    if (!made) continue;
    made.group.data.color = old.color;
    ids.set(g.id, made.group.id);
    ops.push(...made.ops);
    nodes = [...nodes, made.group];
  }
  return ops;
}

/** Copied connections, between the copies of their ends. */
function linkOps(p: ClipPayload, ids: Map<string, string>): Op[] {
  const ops: Op[] = [];
  for (const e of p.edges ?? []) {
    const source = ids.get(e.source);
    const target = ids.get(e.target);
    if (source && target) ops.push({ op: 'putEdge', edge: { ...e, id: newId('e'), source, target } });
  }
  return ops;
}

/** Ctrl/⌘+D: copy-and-paste in place (+32 px) without touching the clipboard. */
export function duplicate(ctl: BoardController, ids: Iterable<string>) {
  const p = makePayload(ctl, ids);
  if (p) pastePayload(ctl, p);
}
