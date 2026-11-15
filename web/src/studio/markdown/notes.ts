// The Markdown studio's part and note paths (docs/API-studio.md §8, v0.9.0-beta.2): the studio
// reads the notes of one part of a project — its folder on its machine, wiki links, backlinks and
// graph within it, and Claude's memory folder for it. Pure.

import { cleanAbs, under } from '../code/paths';
import { isRoot, partHoldingOn, projectParts, type Placement } from '../../lib/parts';
import type { Project, ProjectPart } from '../../lib/types';

const machineOf = (id: string | undefined) => id || 'local';

/**
 * The part a markdown file card opens the studio in: the part whose folder holds the file on its
 * machine (the deepest), else the first part shown on that machine (a note outside every part's
 * folder, as before parts). Undefined when no part is on that machine: no studio for it.
 */
export function markdownPartOfFile(project: Project, machineId: string | undefined, path: string): ProjectPart | undefined {
  const mid = machineOf(machineId);
  const abs = cleanAbs(path);
  return (abs !== null ? partHoldingOn(project, mid, abs) : undefined) ?? projectParts(project).find((pt) => machineOf(pt.machineId) === mid);
}

/**
 * The part the studio opens in when nothing names one (the rail, the shortcut, the lead): the
 * focused tile's part in a project of several parts; undefined lets the studio choose (the part
 * placed in last, else one on This PC, else the first shown: board/PlacePicker usePartChoice).
 */
export function markdownPartOfPlace(project: Project, focused: Placement | null): string | undefined {
  if (projectParts(project).length < 2 || !focused || isRoot(focused)) return undefined;
  return projectParts(project).some((pt) => pt.id === focused.partId) ? focused.partId : undefined;
}

/** The folder a note's absolute path is in, with its trailing slash. */
export const dirOf = (p: string) => p.slice(0, p.lastIndexOf('/') + 1);

/** A link of a note resolved against the note's own path: an absolute path (".." never leaves "/"). */
export function resolvePath(base: string, href: string): string {
  const parts = (href.startsWith('/') ? href : dirOf(base) + href).split('/');
  const out: string[] = [];
  for (const p of parts) {
    if (p === '..') out.pop();
    else if (p !== '.' && p !== '') out.push(p);
  }
  return '/' + out.join('/');
}

/** A note's path relative to the part's folder (the hub's vault rel), or null outside it. */
export function relIn(root: string, p: string): string | null {
  const r = cleanAbs(root);
  const abs = cleanAbs(p);
  if (r === null || abs === null || abs === r || !under(abs, r)) return null;
  return abs.slice(r === '/' ? 1 : r.length + 1);
}

/** A vault rel joined to the part's folder. */
export function absIn(root: string, rel: string): string {
  const r = cleanAbs(root) ?? '/';
  return r === '/' ? `/${rel}` : `${r}/${rel}`;
}
