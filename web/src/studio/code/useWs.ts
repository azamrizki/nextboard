import { useSyncExternalStore } from 'react';
import type { Workspace } from './workspace';

/** Re-render when the workspace changes. */
export function useWs(ws: Workspace) {
  useSyncExternalStore(ws.subscribe, ws.getVersion);
}

/** Re-render when any model's markers change (they do not bump the workspace's version). */
export function useMarkers(ws: Workspace): number {
  return useSyncExternalStore(ws.subscribeMarkers, ws.getMarkerVersion);
}
