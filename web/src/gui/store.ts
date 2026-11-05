// Desktop app sessions fed by /api/events (docs/API-gui.md §3). Same pattern as the other
// stores: immutable snapshot + useSyncExternalStore.

import { useEffect, useSyncExternalStore } from 'react';
import { ApiError, isCode } from '../api';
import type { HubEvent } from '../lib/types';
import { store } from '../store';
import { guiApi, type GuiEvent, type GuiSession } from './api';

export interface GuiEnded {
  reason: string;
  message: string;
}

interface GuiState {
  sessions: Record<string, GuiSession>;
  loaded: boolean;
  /** Why a session of this page's lifetime ended; a tile shows it instead of "not running". */
  ended: Record<string, GuiEnded>;
}

const INITIAL: GuiState = { sessions: {}, loaded: false, ended: {} };

class GuiStore {
  private state: GuiState = INITIAL;
  private listeners = new Set<() => void>();
  private installed = false;
  private loading: Promise<void> | null = null;

  get = () => this.state;
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  private set(patch: Partial<GuiState>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((l) => l());
  }

  install() {
    if (this.installed) return;
    this.installed = true;
    store.onEvent((ev) => this.apply(ev));
  }

  reset() {
    this.state = INITIAL;
    this.listeners.forEach((l) => l());
  }

  private apply(ev: HubEvent) {
    const e = ev as GuiEvent | HubEvent;
    switch (e.type) {
      case 'snapshot':
        // reconnected: sessions may have ended while we were away
        if (this.state.loaded) void this.load();
        break;
      case 'guiSession':
        this.upsert(e.session);
        break;
      case 'guiSessionRemoved': {
        const sessions = { ...this.state.sessions };
        delete sessions[e.id];
        this.set({ sessions, ended: { ...this.state.ended, [e.id]: { reason: e.reason, message: e.message ?? '' } } });
        break;
      }
    }
  }

  upsert(s: GuiSession) {
    this.set({ sessions: { ...this.state.sessions, [s.id]: s } });
  }

  load(): Promise<void> {
    this.loading ??= guiApi
      .sessions()
      .then((list) => {
        const sessions: Record<string, GuiSession> = {};
        for (const s of list) sessions[s.id] = s;
        this.set({ sessions, loaded: true });
      })
      .catch(() => this.set({ loaded: true }))
      .finally(() => {
        this.loading = null;
      });
    return this.loading;
  }
}

export const guiStore = new GuiStore();

export function useGui<T>(select: (s: GuiState) => T): T {
  return useSyncExternalStore(guiStore.subscribe, () => select(guiStore.get()));
}

/** A session by id (loads the list once). */
export function useGuiSession(id: string | undefined): GuiSession | undefined {
  const s = useGui((st) => (id ? st.sessions[id] : undefined));
  const loaded = useGui((st) => st.loaded);
  useEffect(() => {
    if (!loaded) void guiStore.load();
  }, [loaded]);
  return s;
}

/** Start an app, or get its running session: each app has one, on one profile. */
export async function openApp(body: { appId: string; path?: string; width?: number; height?: number }): Promise<GuiSession> {
  let session: GuiSession;
  try {
    session = await guiApi.launch(body);
  } catch (err) {
    if (!(isCode(err, 'already_running') && err instanceof ApiError && err.body.session)) throw err;
    session = err.body.session as GuiSession;
  }
  // The "guiSession" event may already have brought a newer state.
  if (!guiStore.get().sessions[session.id]) guiStore.upsert(session);
  return session;
}
