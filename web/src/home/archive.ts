// Board archive (ArchiveDialog): which project a restore offers first.

import type { ArchiveEntry, Project } from '../lib/types';

/**
 * The project an archived board goes back to unless another is picked: its own, by id, even
 * renamed since; for archives from before they kept the id, one of the same name; else the first.
 */
export function archiveTarget(e: Pick<ArchiveEntry, 'projectId' | 'projectName'>, projects: readonly Pick<Project, 'id' | 'name'>[]): string {
  return (e.projectId && projects.find((p) => p.id === e.projectId)?.id) || projects.find((p) => p.name === e.projectName)?.id || projects[0]?.id || '';
}
