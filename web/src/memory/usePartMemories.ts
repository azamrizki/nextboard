// The parts of a project as its memory shows them, named as their machines are named now.

import { useMemo } from 'react';
import { LOCAL_MACHINE } from '../studio/code/paths';
import { projectParts } from '../lib/parts';
import { useMachines, useProject } from '../store';
import { partMemories, type PartMemory } from './parts';

/** None for a project of one part, or one the store does not know. */
export function usePartMemories(projectId: string | undefined): PartMemory[] {
  const project = useProject(projectId || undefined);
  const machines = useMachines();
  return useMemo(() => {
    if (!project) return [];
    const name = (id: string) => machines.find((m) => m.id === id)?.name ?? (id === LOCAL_MACHINE || !id ? 'This PC' : id);
    return partMemories(projectParts(project), name);
  }, [project, machines]);
}
