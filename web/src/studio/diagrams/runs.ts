// Diagram runs started from this client (E3): kept outside the panel so the "drawing"
// card keeps going — and the result still lands — after the panel closes.

import { useSyncExternalStore } from 'react';
import type { DiagramStyle } from '../types';
import type { DiagramLook } from './layout';

export interface DiagramRun {
  diagramId: string;
  jobId: string;
  projectId: string;
  workspaceId: string;
  style: DiagramStyle;
  look: DiagramLook;
  label: string;
  /** Generate request, for Try again. */
  request: { subject: string; ask?: string; effort: string; style: DiagramStyle; agent?: string };
}

let runs: DiagramRun[] = [];
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

export const diagramRuns = {
  add(r: DiagramRun) {
    runs = [...runs.filter((x) => x.diagramId !== r.diagramId), r];
    emit();
  },
  remove(diagramId: string) {
    runs = runs.filter((x) => x.diagramId !== diagramId);
    emit();
  },
  subscribe(fn: () => void) {
    listeners.add(fn);
    return () => listeners.delete(fn);
  },
  get: () => runs,
};

export function useDiagramRuns(workspaceId: string): DiagramRun[] {
  const all = useSyncExternalStore(diagramRuns.subscribe, diagramRuns.get);
  return all.filter((r) => r.workspaceId === workspaceId);
}
