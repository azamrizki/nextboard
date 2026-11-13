// The parts of a project (docs/API-board.md §1, ROADMAP #49): the folders it spans on one machine
// or several. A hub from before parts sends none; its project is one part.

import type { Project, ProjectPart } from './types';

/** The most parts a project has (the hub's project.MaxParts). */
export const MAX_PARTS = 8;

export function projectParts(p: Project): ProjectPart[] {
  return p.parts?.length ? p.parts : [{ id: 'main', name: 'Main', machineId: p.machineId, path: p.path }];
}

/** The first part on a machine: where a terminal there opens by default. */
export function partOn(p: Project, machineId: string): ProjectPart | undefined {
  return projectParts(p).find((pt) => pt.machineId === machineId);
}

/** "Frontend, Docs": the parts on a machine, for a project with more than one part. */
export function partNamesOn(p: Project, machineId: string): string {
  const parts = projectParts(p);
  return parts.length > 1
    ? parts
        .filter((pt) => pt.machineId === machineId)
        .map((pt) => pt.name)
        .join(', ')
    : '';
}

/** The part whose folder holds an absolute path: the deepest one. */
export function partHolding(p: Project, path: string): ProjectPart | undefined {
  let best: ProjectPart | undefined;
  for (const pt of projectParts(p)) {
    const root = pt.path.replace(/\/+$/, '');
    if ((path === root || path.startsWith(`${root}/`) || root === '') && (!best || root.length > best.path.replace(/\/+$/, '').length)) best = pt;
  }
  return best;
}
