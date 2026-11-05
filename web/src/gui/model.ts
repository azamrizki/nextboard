import type { Project } from '../lib/types';

/** Height of the tile header: the display gets the rest of the node. */
const HEAD_H = 40;

/** Launch request of a tile of this size; folder apps open the project when it is on This PC. */
export function appLaunchBody(appId: string, project: Project, acceptsFolder: boolean, w: number, h: number) {
  return {
    appId,
    path: acceptsFolder && project.machineId === 'local' ? project.path : undefined,
    width: Math.round(w),
    height: Math.round(h - HEAD_H),
  };
}
