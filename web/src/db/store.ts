// What the database browser knows of each project's connections, live: the list and each one's
// status, its schema's version (a statement that changes tables bumps it), the saved queries,
// and the agents' requests waiting for the owner.

import { useEffect, useSyncExternalStore } from 'react';
import { store } from '../store';
import { dbApi } from './api';
import type { Connection, DbRequest, SavedQuery, Status } from './types';

interface State {
  byProject: Record<string, Connection[]>;
  status: Record<string, Status>;
  schemaRev: Record<string, number>;
  queries: Record<string, SavedQuery[]>;
  requests: DbRequest[];
  loaded: Record<string, boolean>;
}

let state: State = { byProject: {}, status: {}, schemaRev: {}, queries: {}, requests: [], loaded: {} };
const listeners = new Set<() => void>();

function set(patch: Partial<State>) {
  state = { ...state, ...patch };
  listeners.forEach((l) => l());
}

function use<T>(pick: (s: State) => T): T {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => pick(state),
  );
}

export async function loadConnections(pid: string): Promise<void> {
  const r = await dbApi.list(pid);
  set({ byProject: { ...state.byProject, [pid]: r.connections }, status: { ...state.status, ...r.status }, loaded: { ...state.loaded, [pid]: true } });
}

export async function loadQueries(pid: string): Promise<void> {
  const r = await dbApi.queries(pid);
  set({ queries: { ...state.queries, [pid]: r.items } });
}

export async function loadRequests(): Promise<void> {
  const r = await dbApi.requests();
  set({ requests: r.items });
}

function upsert(c: Connection) {
  const list = state.byProject[c.projectId] ?? [];
  const i = list.findIndex((x) => x.id === c.id);
  const next = i >= 0 ? list.map((x) => (x.id === c.id ? c : x)) : [...list, c];
  next.sort((a, b) => (a.group ?? '').localeCompare(b.group ?? '') || a.name.localeCompare(b.name));
  set({ byProject: { ...state.byProject, [c.projectId]: next } });
}

/** A connection saved here (the reply of create or replace), shown before its event comes. */
export function rememberConnection(c: Connection) {
  upsert(c);
}

let watching = false;
function watch() {
  if (watching) return;
  watching = true;
  store.onEvent((ev) => {
    switch (ev.type) {
      case 'db':
        upsert(ev.connection);
        set({ status: { ...state.status, [ev.connection.id]: ev.status }, schemaRev: { ...state.schemaRev, [ev.connection.id]: (state.schemaRev[ev.connection.id] ?? 0) + 1 } });
        break;
      case 'dbRemoved': {
        const list = (state.byProject[ev.projectId] ?? []).filter((c) => c.id !== ev.id);
        set({ byProject: { ...state.byProject, [ev.projectId]: list } });
        break;
      }
      case 'dbStatus':
        set({ status: { ...state.status, [ev.id]: ev.status } });
        break;
      case 'dbSchema':
        set({ schemaRev: { ...state.schemaRev, [ev.id]: (state.schemaRev[ev.id] ?? 0) + 1 } });
        break;
      case 'dbQueries':
        void loadQueries(ev.projectId).catch(() => undefined);
        break;
      case 'dbRequest':
        void loadRequests().catch(() => undefined);
        break;
      case 'snapshot':
        void loadRequests().catch(() => undefined);
        for (const pid of Object.keys(state.loaded)) void loadConnections(pid).catch(() => undefined);
        break;
    }
  });
}

export function useConnections(pid: string | undefined): { list: Connection[]; loaded: boolean } {
  useEffect(() => {
    watch();
    if (pid && !state.loaded[pid]) void loadConnections(pid).catch(() => undefined);
  }, [pid]);
  const list = use((s) => (pid ? s.byProject[pid] : undefined));
  const loaded = use((s) => (pid ? !!s.loaded[pid] : false));
  return { list: list ?? EMPTY, loaded };
}

const EMPTY: Connection[] = [];
const EMPTY_Q: SavedQuery[] = [];

export function useConnection(id: string | undefined): Connection | undefined {
  return use((s) => {
    if (!id) return undefined;
    for (const list of Object.values(s.byProject)) {
      const c = list.find((x) => x.id === id);
      if (c) return c;
    }
    return undefined;
  });
}

/** A connection by id, loading it when this page has not seen its project yet. */
export function useConnectionById(id: string | undefined): Connection | undefined {
  const c = useConnection(id);
  useEffect(() => {
    watch();
    if (id && !c) {
      void dbApi
        .get(id)
        .then((r) => {
          upsert(r.connection);
          set({ status: { ...state.status, [id]: r.status } });
        })
        .catch(() => undefined);
    }
  }, [id, c]);
  return c;
}

export function useStatus(id: string | undefined): Status | undefined {
  return use((s) => (id ? s.status[id] : undefined));
}

export function setStatus(id: string, st: Status) {
  set({ status: { ...state.status, [id]: st } });
}

export function useSchemaRev(id: string | undefined): number {
  return use((s) => (id ? (s.schemaRev[id] ?? 0) : 0));
}

export function bumpSchema(id: string) {
  set({ schemaRev: { ...state.schemaRev, [id]: (state.schemaRev[id] ?? 0) + 1 } });
}

export function useQueries(pid: string | undefined): SavedQuery[] {
  useEffect(() => {
    watch();
    if (pid && !state.queries[pid]) void loadQueries(pid).catch(() => undefined);
  }, [pid]);
  return use((s) => (pid ? (s.queries[pid] ?? EMPTY_Q) : EMPTY_Q));
}

export function useDbRequests(): DbRequest[] {
  useEffect(() => {
    watch();
    void loadRequests().catch(() => undefined);
  }, []);
  return use((s) => s.requests);
}

export function startWatching() {
  watch();
}
