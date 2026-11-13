// The parts of a project (docs/API-board.md §1, ROADMAP #49): the folders it spans on one machine
// or several. A hub from before parts sends none; its project is one part.

import type { Project, ProjectPart } from './types';

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
