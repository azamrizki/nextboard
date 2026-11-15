// A project in several parts on its Home card (DESIGN.md §3.11, ROADMAP #49): every part equal,
// none of them "main". One line says how many parts on how many machines, then as many part
// chips as fit and "+N"; the git row adds the parts up instead of showing the first one's.

import type { ProjectPart } from '../lib/types';
import type { GitRepoInfo } from '../studio/types';

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** "6 parts · 3 machines", or "6 parts" where the card names the machines elsewhere. */
export function partsSummary(parts: ProjectPart[], machines = true): string {
  const head = plural(parts.length, 'part');
  return machines ? `${head} · ${plural(machineCount(parts), 'machine')}` : head;
}

export const machineCount = (parts: ProjectPart[]) => new Set(parts.map((p) => p.machineId)).size;

/**
 * How many chips of these widths fit in `width`, in order, with `gap` between them: all of them,
 * or as many as leave room for the "+N" chip after them (possibly none).
 */
export function fitCount(widths: number[], width: number, gap: number, plus: number): number {
  const run = (k: number) => widths.slice(0, k).reduce((a, w) => a + w, 0) + gap * Math.max(0, k - 1);
  if (run(widths.length) <= width + 0.5) return widths.length;
  let k = 0;
  while (k < widths.length && run(k + 1) + gap + plus <= width + 0.5) k++;
  return k;
}

/** What is known of one part's git: its repositories, or why there is nothing to say. */
export interface PartGit {
  /** Its machine is not connected: nothing was asked. */
  offline?: boolean;
  error?: string;
  items?: GitRepoInfo[];
}

export interface PartsGit {
  /** Repositories that could be read, over every part. */
  repos: number;
  /** Of those, the ones with changes. */
  dirty: number;
  /** The branch every one of them is on, or '' when they differ. */
  branch: string;
  ahead: number;
  behind: number;
  /** Parts or repositories that could not be read. */
  unread: number;
  /** Parts whose machine is away. */
  offline: number;
  /** Parts still being asked. */
  pending: number;
}

/** The parts' git added up; `undefined` for a part not answered yet. */
export function partsGit(list: (PartGit | undefined)[]): PartsGit {
  const out: PartsGit = { repos: 0, dirty: 0, branch: '', ahead: 0, behind: 0, unread: 0, offline: 0, pending: 0 };
  const branches = new Set<string>();
  for (const g of list) {
    if (!g) out.pending++;
    else if (g.offline) out.offline++;
    else if (g.error) out.unread++;
    else
      for (const r of g.items ?? []) {
        if (r.error) {
          out.unread++;
          continue;
        }
        out.repos++;
        if (r.changed) out.dirty++;
        out.ahead += r.ahead;
        out.behind += r.behind;
        branches.add(r.branch || 'detached');
      }
  }
  if (branches.size === 1) out.branch = [...branches][0];
  return out;
}

/** What the git row of the card says: "6 repos" and "All clean" or "2 with changes". */
export function partsGitText(s: PartsGit): { text: string; badge: string; dirty: boolean; note: string } {
  const note = [s.offline ? `${s.offline} offline` : '', s.unread ? `${s.unread} unreadable` : ''].filter(Boolean).join(' · ');
  if (s.pending && !s.repos) return { text: 'Checking git…', badge: '', dirty: false, note: '' };
  if (!s.repos) return { text: s.offline && !s.unread ? 'Machines offline' : 'No git repositories', badge: '', dirty: false, note: s.offline && !s.unread ? '' : note };
  return { text: plural(s.repos, 'repo'), badge: s.dirty ? `${s.dirty} with changes` : 'All clean', dirty: s.dirty > 0, note };
}
