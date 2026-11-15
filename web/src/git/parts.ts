// The parts of a project in the board's Git panel (docs/API-board.md §1, ROADMAP #49). Each part
// has its own git: the hub reads it through the project ref "<project>~<part>", and the studio
// store keeps its repositories, summary and events under the same key.

import type { Project, ProjectPart } from '../lib/types';
import { projectParts } from '../lib/parts';
import type { GitRepoInfo, GitSummary } from '../studio/types';

/** What the hub and the studio store call a part's git: the project id for the first part. */
export function partRef(project: Project, part: ProjectPart): string {
  const first = projectParts(project)[0];
  return !first || part.id === first.id ? project.id : `${project.id}~${part.id}`;
}

/** A part's repositories added up, for its header. Repositories that could not be read do not count. */
export function sumRepos(items: GitRepoInfo[] | undefined): GitSummary & { repos: number; unread: number } {
  const out = { branch: '', ahead: 0, behind: 0, changed: 0, repos: items?.length ?? 0, unread: 0 };
  for (const r of items ?? []) {
    if (r.error) {
      out.unread++;
      continue;
    }
    out.ahead += r.ahead;
    out.behind += r.behind;
    out.changed += r.changed;
  }
  if (items?.length === 1 && !items[0].error) out.branch = items[0].branch;
  return out;
}

/** "Branch main, 3 changed files, 1 to push": what a header says to a screen reader. */
export function summaryLabel(s: GitSummary): string {
  return [
    `Branch ${s.branch || 'detached'}`,
    `${s.changed} changed file${s.changed === 1 ? '' : 's'}`,
    s.ahead ? `${s.ahead} to push` : '',
    s.behind ? `${s.behind} to pull` : '',
  ]
    .filter(Boolean)
    .join(', ');
}

/** Part ids that were open in the panel last time, kept per project in the browser; unknown ones drop. */
export function openPartsFrom(saved: string | null, parts: ProjectPart[]): Set<string> {
  let list: unknown = [];
  try {
    list = saved ? JSON.parse(saved) : [];
  } catch {
    list = [];
  }
  const known = new Set(parts.map((p) => p.id));
  return new Set(Array.isArray(list) ? list.filter((id): id is string => typeof id === 'string' && known.has(id)) : []);
}
