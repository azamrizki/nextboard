// What removing projects does, as the remove dialog says it (DESIGN.md §3.11, API-board §1):
// Archive keeps everything and can be undone; Delete permanently removes NextBoard's data of
// them. Folders and files on disk are never touched by either.

import type { BulkResult, Project, Session } from '../lib/types';

/** What has to be typed before a permanent delete. */
export const CONFIRM_WORD = 'delete';

export type RemoveChoice = 'archive' | 'delete';

/** Typed as asked: case and spaces around it do not matter. */
export function typedOk(typed: string): boolean {
  return typed.trim().toLowerCase() === CONFIRM_WORD;
}

/** A console still doing something: it stops when its project is archived or deleted. */
const live = (s: Session) => s.status === 'running' || s.status === 'starting' || s.status === 'reconnecting';

export interface RemoveSummary {
  count: number;
  /** Consoles (any state) of these projects: deleted with a permanent delete. */
  consoles: number;
  /** Of those, the ones running now: they stop either way. */
  running: number;
  boards: number;
  /** Every folder of every part: kept on disk. */
  folders: string[];
}

/**
 * Counted from the live sessions when the page has them, from the project record otherwise
 * (a page opened straight onto the list may not have sessions yet).
 */
export function removeSummary(projects: Project[], sessions: Session[]): RemoveSummary {
  let consoles = 0;
  let running = 0;
  let boards = 0;
  const folders: string[] = [];
  for (const p of projects) {
    const own = sessions.filter((s) => s.projectId === p.id && !s.panel);
    consoles += own.length || p.sessionCount || 0;
    running += own.length ? own.filter(live).length : p.runningCount || 0;
    boards += p.workspaces?.length ?? 0;
    const parts = p.parts?.length ? p.parts : [{ path: p.path }];
    for (const pt of parts) if (!folders.includes(pt.path)) folders.push(pt.path);
  }
  return { count: projects.length, consoles, running, boards, folders };
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** "2 running consoles stop." or '' when none runs. */
export function stopLine(s: RemoveSummary): string {
  if (!s.running) return '';
  return `${plural(s.running, 'running console')} will stop.`;
}

/** What a permanent delete removes, one line each, the counts where they are known. */
export function deleteList(s: RemoveSummary): string[] {
  return [
    s.consoles ? `${plural(s.consoles, 'console')} and their scrollback` : 'Its consoles and their scrollback',
    s.boards ? `${plural(s.boards, 'board')} with their cards, archives and snapshots` : 'Its boards, their archives and snapshots',
    'Project memory and its sub-memories',
    'Review, history, threads, loops and uptime monitors',
    'Files pasted into its orders',
  ];
}

export interface BulkOutcome {
  done: string[];
  failed: { id: string; name: string; error: string }[];
}

/** Each id came back on its own: which went through, and why the others did not. */
export function bulkOutcome(results: BulkResult[], ids: string[], nameOf: (id: string) => string): BulkOutcome {
  const byId = new Map(results.map((r) => [r.id, r]));
  const out: BulkOutcome = { done: [], failed: [] };
  for (const id of ids) {
    const r = byId.get(id);
    if (r?.ok) out.done.push(id);
    else out.failed.push({ id, name: nameOf(id), error: friendlyError(r?.error ?? 'no answer from the hub') });
  }
  return out;
}

/** The hub's codes, said as a person would. */
export function friendlyError(code: string): string {
  switch (code) {
    case 'in_use':
      return 'it is in use';
    case 'not_archived':
      return 'it was not archived';
    case 'not_found':
      return 'it is gone already';
    default:
      return code;
  }
}

/** "Alpha and Beta", "Alpha, Beta and 3 more". */
export function nameList(names: string[], shown = 2): string {
  if (names.length <= 1) return names[0] ?? '';
  if (names.length <= shown + 1) return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
  return `${names.slice(0, shown).join(', ')} and ${names.length - shown} more`;
}
