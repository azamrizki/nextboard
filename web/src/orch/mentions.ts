// @mention candidates (docs/API-orchestration.md §3.3): the hub's list when it has one,
// otherwise computed here from the board and the live sessions (same order: everyone,
// consoles, groups, roles, agents; ≤ 30).

import type { BoardEdge, BoardNode, Session } from '../lib/types';
import { readGroup } from '../board/kinds';
import { directedTerminals } from '../board/groups';
import { ROLE_LABEL, type Mention, type Role } from './types';

export const normName = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, '');

export function localMentions(
  nodes: BoardNode[],
  edges: BoardEdge[],
  sessions: Record<string, Session>,
  agentName: (id: string) => string,
  cardId?: string | null,
): Mention[] {
  const terms = nodes.filter((n) => n.kind === 'terminal');
  const allowed = cardId ? new Set(directedTerminals(nodes, edges, cardId)) : null;
  const out: Mention[] = [{ kind: 'everyone', id: 'everyone', label: 'everyone' }];
  const consoleSessions: Session[] = [];
  for (const n of terms) {
    if (allowed && !allowed.has(n.id)) continue;
    const s = typeof n.data.sessionId === 'string' ? sessions[n.data.sessionId] : undefined;
    if (!s?.handle) continue;
    consoleSessions.push(s);
    out.push({ kind: 'console', id: s.id, label: s.handle, handle: s.handle, agentState: s.agentState });
  }
  const groups = nodes.filter((n) => n.kind === 'group');
  const roles = new Set<string>();
  for (const g of groups) {
    const d = readGroup(g);
    if (allowed && !d.members.some((m) => allowed.has(m))) continue;
    out.push({ kind: 'group', id: g.id, label: d.name });
    roles.add(d.role);
  }
  for (const r of roles) if (r !== 'general') out.push({ kind: 'role', id: r, label: ROLE_LABEL[r as Role] ?? r });
  const agents = new Set(consoleSessions.map((s) => s.agent || s.preset).filter((a) => a && a !== 'shell'));
  for (const a of agents) out.push({ kind: 'agent', id: a, label: agentName(a) || a });
  return out.slice(0, 30);
}

/** Text to insert for a mention (handles as-is, names with spaces made one token). */
export function mentionToken(m: Mention): string {
  if (m.kind === 'console') return `@${m.handle ?? m.label}`;
  if (m.kind === 'agent') return `@${m.id}`;
  return `@${m.label.replace(/\s+/g, '')}`;
}

export function filterMentions(list: Mention[], q: string): Mention[] {
  const nq = normName(q);
  if (!nq) return list;
  const starts = list.filter((m) => normName(m.label).startsWith(nq) || (m.handle && normName(m.handle).startsWith(nq)) || normName(m.id).startsWith(nq));
  const contains = list.filter((m) => !starts.includes(m) && normName(m.label).includes(nq));
  return [...starts, ...contains];
}

/** @tokens in the text, resolved against the candidates (for target chips). */
export function mentionedTargets(text: string, list: Mention[]): { token: string; mention: Mention | null }[] {
  const out: { token: string; mention: Mention | null }[] = [];
  const seen = new Set<string>();
  for (const m of text.matchAll(/(^|[\s,(])@([A-Za-z][\w-]*)/g)) {
    const token = m[2];
    const key = normName(token);
    if (seen.has(key)) continue;
    seen.add(key);
    const hit =
      list.find((x) => x.kind === 'console' && normName(x.handle ?? x.label) === key) ??
      list.find((x) => normName(x.label) === key) ??
      list.find((x) => normName(x.id) === key) ??
      (['all', 'everybody', 'everyone'].includes(key) ? (list.find((x) => x.kind === 'everyone') ?? null) : null);
    out.push({ token, mention: hit ?? null });
  }
  return out;
}

/** Remove one @token from the text (chip ✕). */
export function removeMention(text: string, token: string): string {
  const re = new RegExp(`(^|[\\s,(])@${token.replace(/[-]/g, '\\-')}\\b[,]?\\s?`, 'i');
  return text.replace(re, '$1').replace(/^\s+/, '');
}
