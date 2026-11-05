// Groups (node kind "group", docs/API-orchestration.md §1): frame geometry, membership
// changes and wires/forwards (edges). Everything here returns board ops; callers apply
// them through the controller (one undo step, one request).

import { newId } from '../lib/ids';
import type { BoardEdge, BoardNode, NodeLayout, Op, Session } from '../lib/types';
import { isAgentSession } from '../orch/actions';
import { ROLE_COLOR, type ForwardMode, type ForwardTrigger, type GroupData, type Role } from '../orch/types';
import { store } from '../store';
import { readGroup } from './kinds';

export const GROUP_PAD = 24;
export const GROUP_HEAD = 32;
export const GROUP_MAX = 12;

const rect = (n: BoardNode): NodeLayout => ({ x: n.x, y: n.y, w: n.w, h: n.h });

/** Frame around member rects: bounding box + padding 24 + header 32 (§1). */
export function frameOf(members: NodeLayout[]): NodeLayout | null {
  if (!members.length) return null;
  const minX = Math.min(...members.map((r) => r.x));
  const minY = Math.min(...members.map((r) => r.y));
  const maxX = Math.max(...members.map((r) => r.x + r.w));
  const maxY = Math.max(...members.map((r) => r.y + r.h));
  return {
    x: Math.round(minX - GROUP_PAD),
    y: Math.round(minY - GROUP_PAD - GROUP_HEAD),
    w: Math.round(maxX - minX + GROUP_PAD * 2),
    h: Math.round(maxY - minY + GROUP_PAD * 2 + GROUP_HEAD),
  };
}

export const isGroup = (n: BoardNode | undefined): n is BoardNode => n?.kind === 'group';

export function groupNodes(nodes: BoardNode[]): BoardNode[] {
  return nodes.filter((n) => n.kind === 'group');
}

/** The group a terminal node belongs to (≤ 1). */
export function groupOf(nodes: BoardNode[], nodeId: string): BoardNode | undefined {
  return nodes.find((n) => n.kind === 'group' && readGroup(n).members.includes(nodeId));
}

/** Group frame patch for these groups, using `rects` overrides for moved members. */
export function frameOps(nodes: BoardNode[], groupIds: Iterable<string>, rects?: Map<string, NodeLayout>, members?: Map<string, string[]>): Op[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const ops: Op[] = [];
  for (const gid of new Set(groupIds)) {
    const g = byId.get(gid);
    if (!g || g.kind !== 'group') continue;
    const ids = members?.get(gid) ?? readGroup(g).members;
    const rs = ids.map((id) => rects?.get(id) ?? (byId.get(id) ? rect(byId.get(id)!) : null)).filter((r): r is NodeLayout => !!r);
    const f = frameOf(rs);
    if (!f) continue;
    if (f.x !== g.x || f.y !== g.y || f.w !== g.w || f.h !== g.h) ops.push({ op: 'patch', id: gid, set: f });
  }
  return ops;
}

/** Groups whose members include any of these node ids. */
export function groupsTouching(nodes: BoardNode[], ids: Iterable<string>): string[] {
  const set = new Set(ids);
  return groupNodes(nodes)
    .filter((g) => readGroup(g).members.some((m) => set.has(m)))
    .map((g) => g.id);
}

const norm = (s: string) => s.trim().toLowerCase();

/** "Group N" with the smallest free N. */
export function nextGroupName(nodes: BoardNode[]): string {
  const used = new Set(groupNodes(nodes).map((g) => norm(readGroup(g).name)));
  for (let i = 1; i < 1000; i++) if (!used.has(`group ${i}`)) return `Group ${i}`;
  return `Group ${Date.now() % 1000}`;
}

/** Client-side check of the hub rules (1–24 chars, unique per workspace, not a handle). */
export function groupNameProblem(name: string, nodes: BoardNode[], selfId?: string): string {
  const v = name.trim();
  if (!v) return 'Give the group a name.';
  if (v.length > 24) return 'Use up to 24 characters.';
  if (groupNodes(nodes).some((g) => g.id !== selfId && norm(readGroup(g).name) === norm(v))) return `There is already a group called “${v}”.`;
  const clash = Object.values(store.getState().sessions).find((s) => s.handle && norm(s.handle) === norm(v));
  if (clash) return `@${clash.handle} is a console handle. Pick another group name.`;
  return '';
}

/**
 * Ops that create a group of these terminal nodes (taken out of their old groups).
 * Returns the new group node too.
 */
export function createGroupOps(nodes: BoardNode[], terminalIds: string[], opts: { name?: string; role?: Role } = {}): { ops: Op[]; group: BoardNode } | null {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const members = terminalIds.filter((id) => byId.get(id)?.kind === 'terminal').slice(0, GROUP_MAX);
  const f = frameOf(members.map((id) => rect(byId.get(id)!)));
  if (!f) return null;
  const role = opts.role ?? 'general';
  const data: GroupData = { name: opts.name ?? nextGroupName(nodes), role, color: ROLE_COLOR[role], members };
  const group: BoardNode = { id: newId('n'), kind: 'group', ...f, data: { ...data } };
  const ops: Op[] = [];
  const moved = new Set(members);
  for (const g of groupNodes(nodes)) {
    const gm = readGroup(g).members;
    if (gm.some((m) => moved.has(m))) {
      const rest = gm.filter((m) => !moved.has(m));
      ops.push({ op: 'patch', id: g.id, set: { data: { members: rest } } });
      ops.push(...frameOps(nodes, [g.id], undefined, new Map([[g.id, rest]])));
    }
  }
  ops.push({ op: 'put', node: group }, { op: 'z', id: group.id, to: 'back' });
  return { ops, group };
}

/**
 * Membership after terminals were dropped (drag in/out of a frame, §1). Frames are the
 * ones from before the drag (they do not follow a member while it is dragged).
 */
export function dropMembershipOps(nodes: BoardNode[], movedIds: string[]): { ops: Op[]; full: string[] } {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const groups = groupNodes(nodes);
  const members = new Map(groups.map((g) => [g.id, readGroup(g).members.slice()]));
  const touched = new Set<string>();
  const full: string[] = [];
  for (const id of movedIds) {
    const n = byId.get(id);
    if (!n || n.kind !== 'terminal') continue;
    const cx = n.x + n.w / 2;
    const cy = n.y + n.h / 2;
    const cur = groups.find((g) => members.get(g.id)!.includes(id));
    const inside = (g: BoardNode) => cx >= g.x && cx <= g.x + g.w && cy >= g.y && cy <= g.y + g.h;
    if (cur && inside(cur)) {
      touched.add(cur.id);
      continue;
    }
    // topmost other frame under the centre (array order = z order)
    const target = [...groups].reverse().find((g) => g.id !== cur?.id && inside(g));
    if (cur) {
      members.set(cur.id, members.get(cur.id)!.filter((m) => m !== id));
      touched.add(cur.id);
    }
    if (target) {
      const list = members.get(target.id)!;
      if (list.length >= GROUP_MAX) {
        full.push(readGroup(target).name);
        if (cur) members.set(cur.id, [...members.get(cur.id)!, id]); // stays where it was
        continue;
      }
      list.push(id);
      touched.add(target.id);
    }
  }
  const ops: Op[] = [];
  for (const gid of touched) {
    const before = readGroup(byId.get(gid)!).members;
    const after = members.get(gid)!;
    if (before.length !== after.length || before.some((m, i) => after[i] !== m)) ops.push({ op: 'patch', id: gid, set: { data: { members: after } } });
  }
  ops.push(...frameOps(nodes, touched, undefined, members));
  return { ops, full };
}

/** Sessions of a group's member nodes. */
export function groupSessions(nodes: BoardNode[], g: BoardNode): Session[] {
  const st = store.getState().sessions;
  const byId = new Map(nodes.map((n) => [n.id, n]));
  return readGroup(g)
    .members.map((id) => byId.get(id))
    .map((n) => (n && typeof n.data.sessionId === 'string' ? st[n.data.sessionId] : undefined))
    .filter((s): s is Session => !!s);
}

// ---- wires & forwards (edges) ---------------------------------------------------------

/** Tiles whose content can go to an agent: sent once when wired, again when asked. */
export const CONTENT_SOURCES = new Set(['note', 'checklist', 'kanban', 'calendar', 'file', 'image', 'link', 'document', 'branches', 'web', 'weblog']);
export const WIRE_SOURCES = new Set(['orchestrator', 'editor', 'memory', ...CONTENT_SOURCES]);
export const WIRE_TARGETS = new Set(['terminal', 'group']);
/** A lead keeps these up to date for its team (docs/API-orchestration.md §3.10). */
export const TASK_BOARDS = new Set(['kanban', 'checklist']);

export function edgeBetween(edges: BoardEdge[], source: string, target: string, kind: string): BoardEdge | undefined {
  return edges.find((e) => e.kind === kind && e.source === source && e.target === target);
}

/**
 * Would a forward edge source → target close a cycle? Like the hub, a group and its
 * members count as one vertex (forwarding inside a group loops back to itself).
 */
export function forwardCycle(edges: BoardEdge[], source: string, target: string, nodes: BoardNode[] = []): boolean {
  const rep = new Map<string, string>();
  for (const g of groupNodes(nodes)) for (const m of readGroup(g).members) rep.set(m, g.id);
  const v = (id: string) => rep.get(id) ?? id;
  const a = v(source);
  const b = v(target);
  if (a === b) return true;
  const next = new Map<string, string[]>();
  for (const e of edges) if (e.kind === 'forward') next.set(v(e.source), [...(next.get(v(e.source)) ?? []), v(e.target)]);
  const seen = new Set<string>();
  const stack = [b];
  while (stack.length) {
    const cur = stack.pop()!;
    if (cur === a) return true;
    if (seen.has(cur)) continue;
    seen.add(cur);
    stack.push(...(next.get(cur) ?? []));
  }
  return false;
}

/**
 * Why a console or group has no part in a connection: only an agent has turns to forward
 * and takes text. '' when it can. One agent in a group is enough; an empty group may get
 * members later, and a console still loading gets the benefit of the doubt.
 */
export function noAgentReason(nodes: BoardNode[], n: BoardNode): string {
  if (n.kind === 'terminal') {
    const s = store.getState().sessions[String(n.data.sessionId)];
    return s && !isAgentSession(s) ? `@${s.handle} has no agent running` : '';
  }
  if (n.kind === 'group') {
    const list = groupSessions(nodes, n);
    return list.length && !list.some(isAgentSession) ? `${readGroup(n).name} has no agent running` : '';
  }
  return '';
}

/** `sends`: the tile's content goes to the new target now that it is wired. */
export type EdgeIntent = { ok: true; op: Op; label: string; sends?: boolean } | { ok: false; reason: string };

/** What dropping a connection from `from` on `to` means (wire, forward, or why not). */
export function connectIntent(nodes: BoardNode[], edges: BoardEdge[], fromId: string, toId: string): EdgeIntent {
  const from = nodes.find((n) => n.id === fromId);
  const to = nodes.find((n) => n.id === toId);
  if (!from || !to || from.id === to.id) return { ok: false, reason: '' };
  const line = (kind: string, label: string): EdgeIntent => {
    const existing = edgeBetween(edges, from.id, to.id, kind);
    if (existing) return { ok: true, op: { op: 'delEdge', id: existing.id }, label: 'Disconnected' };
    return { ok: true, op: { op: 'putEdge', edge: { id: newId('e'), source: from.id, target: to.id, kind, data: {} } }, label };
  };
  // a page and its log card: the card follows the page it is linked to, and only one
  if (from.kind === 'web' && to.kind === 'weblog') {
    if (edges.some((e) => e.kind === 'log' && e.target === to.id && e.source !== from.id)) return { ok: false, reason: 'This log card already follows a page. Remove that line first.' };
    return line('log', 'Linked');
  }
  if (from.kind === 'terminal' && to.kind === 'web') return line('preview', 'Linked');
  // a task board and its lead, whichever end the drag began at: the wire starts at the lead
  if (from.kind === 'orchestrator' && TASK_BOARDS.has(to.kind)) return line('wire', 'Linked');
  if (TASK_BOARDS.has(from.kind) && to.kind === 'orchestrator') return connectIntent(nodes, edges, to.id, from.id);
  if (WIRE_SOURCES.has(from.kind)) {
    // the editor also asks orchestrator cards; only it may be wired to one
    const reachable = WIRE_TARGETS.has(to.kind) || (from.kind === 'editor' && to.kind === 'orchestrator');
    if (!reachable) return { ok: false, reason: from.kind === 'editor' ? 'Wire the editor to a console, a group or an orchestrator card.' : 'Wire a card to a console or a group.' };
    const intent = line('wire', 'Wired');
    if (intent.ok && intent.label === 'Disconnected') return intent;
    const noAgent = noAgentReason(nodes, to);
    if (noAgent) return { ok: false, reason: `${noAgent}, and NextBoard only types to agents. Start one there first.` };
    return intent.ok ? { ...intent, sends: CONTENT_SOURCES.has(from.kind) } : intent;
  }
  if (WIRE_TARGETS.has(from.kind)) {
    if (!WIRE_TARGETS.has(to.kind)) return { ok: false, reason: 'Forward results to a console or a group.' };
    if (edgeBetween(edges, from.id, to.id, 'forward')) return { ok: false, reason: 'These two are already connected by a forward.' };
    const fromShell = noAgentReason(nodes, from);
    if (fromShell) return { ok: false, reason: `${fromShell}, so it has no finished turns to forward. Start one there first.` };
    const toShell = noAgentReason(nodes, to);
    if (toShell) return { ok: false, reason: `${toShell}, and NextBoard only types to agents. Start one there first.` };
    const gf = groupOf(nodes, from.id);
    if (gf && (gf.id === to.id || gf.id === groupOf(nodes, to.id)?.id || readGroup(gf).members.includes(to.id)))
      return { ok: false, reason: `Both are in ${readGroup(gf).name}: a forward inside a group would come back to itself. Forward to a console or group outside it.` };
    if (forwardCycle(edges, from.id, to.id, nodes)) return { ok: false, reason: 'That forward would make a loop of results. Remove another forward first.' };
    const data: { mode: ForwardMode; trigger: ForwardTrigger } = { mode: 'digest', trigger: 'turn' };
    return { ok: true, op: { op: 'putEdge', edge: { id: newId('e'), source: from.id, target: to.id, kind: 'forward', data } }, label: 'Forward added' };
  }
  return { ok: false, reason: '' };
}

/** Node ids a card directs: wired terminals + members of wired groups. */
export function directedTerminals(nodes: BoardNode[], edges: BoardEdge[], cardId: string): string[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const out: string[] = [];
  for (const e of edges) {
    if (e.kind !== 'wire' || e.source !== cardId) continue;
    const t = byId.get(e.target);
    if (!t) continue;
    if (t.kind === 'terminal') out.push(t.id);
    else if (t.kind === 'group') out.push(...readGroup(t).members);
  }
  return [...new Set(out)];
}
