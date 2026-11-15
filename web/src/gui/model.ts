import { isRoot, projectParts } from '../lib/parts';
import { lastPlacement } from '../lib/placement';
import type { Project } from '../lib/types';

/** Height of the tile header: the display gets the rest of the node. */
const HEAD_H = 40;

/**
 * The folder a desktop app opens (apps run on This PC): the part asked for when it is on This PC,
 * else the part last placed in when it is, else the project's part on This PC (v0.9.0-beta.2: any
 * part there, not only the first); none when no part is on This PC.
 */
export function localFolder(project: Project, partId?: string): string | undefined {
  const local = projectParts(project).filter((p) => (p.machineId || 'local') === 'local');
  const last = lastPlacement(project);
  const wanted = partId || (last && !isRoot(last) ? last.partId : '');
  return (local.find((p) => p.id === wanted) ?? local[0])?.path;
}

/** Launch request of a tile of this size; folder apps open a part of the project that is on This PC. */
export function appLaunchBody(appId: string, project: Project, acceptsFolder: boolean, w: number, h: number, partId?: string) {
  return {
    appId,
    path: acceptsFolder ? localFolder(project, partId) : undefined,
    width: Math.round(w),
    height: Math.round(h - HEAD_H),
  };
}
