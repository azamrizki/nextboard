// Small React hooks over the live stores for orchestration UI.

import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import type { Session } from '../lib/types';
import { store } from '../store';

/** All sessions (re-renders on any session change). */
export function useStoreSessions(): Record<string, Session> {
  return useSyncExternalStore(store.subscribe, () => store.getState().sessions);
}

/**
 * A value computed from the sessions, re-rendering only when it changes: a boolean or a string
 * where a component needs no more, as each edge of a board does.
 */
export function useSessionsValue<T extends string | number | boolean>(compute: (sessions: Record<string, Session>) => T): T {
  return useSyncExternalStore(store.subscribe, () => compute(store.getState().sessions));
}

/**
 * The sessions `keep` takes, in the store's order, re-rendering only when one of them changes,
 * comes or goes: a card showing its team need not render again for every console on the hub.
 */
export function useSessionList(keep: (s: Session) => boolean): Session[] {
  const last = useRef<{ all: Record<string, Session> | null; keep: ((s: Session) => boolean) | null; list: Session[] }>({ all: null, keep: null, list: [] });
  return useSyncExternalStore(store.subscribe, () => {
    const all = store.getState().sessions;
    const c = last.current;
    if (all === c.all && keep === c.keep) return c.list;
    const list = Object.values(all).filter(keep);
    const same = list.length === c.list.length && list.every((s, i) => s === c.list[i]);
    last.current = { all, keep, list: same ? c.list : list };
    return last.current.list;
  });
}

/** Sessions of a workspace, creation order. */
export function useWorkspaceSessions(workspaceId: string): Session[] {
  const rec = useStoreSessions();
  return useMemo(
    () =>
      Object.values(rec)
        .filter((s) => s.workspaceId === workspaceId)
        .sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id)),
    [rec, workspaceId],
  );
}

/** Live clock for countdowns (re-render every `ms`). */
export function useNow(ms = 1000, enabled = true): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return;
    const t = window.setInterval(() => setNow(Date.now()), ms);
    return () => window.clearInterval(t);
  }, [ms, enabled]);
  return now;
}
