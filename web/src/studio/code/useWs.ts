import { useSyncExternalStore } from 'react';
import type { Workspace } from './workspace';

/** Re-render when the workspace changes. */
export function useWs(ws: Workspace) {
  useSyncExternalStore(ws.subscribe, ws.getVersion);
}
