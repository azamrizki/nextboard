// Pure helpers for memory blocks and what a send costs (docs/API-orchestration.md §12).

import type { MemoryBlock, MemoryPlanTarget } from '../orch/types';

export const MAIN_DOC = 'main';

/** The hub's name for a memory: the project, or "global" for all projects. */
export const blockKey = (b: { projectId?: string; name: string }) => `${b.projectId || 'global'}/${b.name}`;

/** Rough: about four characters a token. It is for weighing, not for billing. */
export function tokensLabel(chars: number): string {
  const t = Math.ceil(chars / 4);
  return t < 1000 ? `≈${t}` : `≈${(t / 1000).toFixed(1)}k`;
}

/** Add blocks a stack does not have yet, and never the project's own memory, which is always on. */
export function addBlocks(current: MemoryBlock[], added: MemoryBlock[], projectId: string): MemoryBlock[] {
  const have = new Set([...current.map(blockKey), blockKey({ projectId, name: MAIN_DOC })]);
  const out = [...current];
  for (const b of added) {
    if (have.has(blockKey(b))) continue;
    have.add(blockKey(b));
    out.push(b);
  }
  return out;
}

export interface SendSummary {
  /** Agents a send would type to now. */
  send: MemoryPlanTarget[];
  /** They already have all of it: no turn is used. */
  upToDate: number;
  /** Paused, waiting for you, or stopped. */
  unavailable: number;
  chars: number;
}

export function summarizeSend(targets: MemoryPlanTarget[]): SendSummary {
  const send = targets.filter((t) => t.canSend && t.chars > 0);
  return {
    send,
    upToDate: targets.filter((t) => t.canSend && t.chars === 0).length,
    unavailable: targets.filter((t) => !t.canSend).length,
    chars: send.reduce((n, t) => n + t.chars, 0),
  };
}

export const agentsLabel = (n: number) => (n === 1 ? '1 agent' : `${n} agents`);

/** What a send did, in one line. */
export function sentMessage(r: { delivered: number; busy: number; upToDate?: number }): string {
  const parts: string[] = [];
  if (r.delivered) parts.push(`Memory sent to ${agentsLabel(r.delivered)}`);
  if (r.busy) parts.push(`${agentsLabel(r.busy)} ${r.busy === 1 ? 'gets' : 'get'} it when free`);
  if (r.upToDate) parts.push(`${r.upToDate} had it already`);
  return parts.length ? `${parts.join('; ')}.` : 'Nothing was sent.';
}

/** A brick dragged onto another takes its place. */
export function moveBlock(blocks: MemoryBlock[], fromKey: string, toKey: string): MemoryBlock[] {
  const from = blocks.findIndex((b) => blockKey(b) === fromKey);
  const to = blocks.findIndex((b) => blockKey(b) === toKey);
  if (from < 0 || to < 0 || from === to) return blocks;
  const out = [...blocks];
  const [moved] = out.splice(from, 1);
  out.splice(to, 0, moved);
  return out;
}

/** One line per command; blanks do not count. */
export const commandLines = (text: string) =>
  text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean);
