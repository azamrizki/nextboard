// Chats live state: each opened project's agent chats (fed by the hub's "chat" events) and each
// shown thread's lead chats ("leadChats" events). When a thread's active chat changes, the thread
// is loaded again, since GET /api/threads/{tid}/messages returns the active chat's messages.

import { useEffect, useSyncExternalStore } from 'react';
import { ApiError } from '../api';
import type { HubEvent } from '../lib/types';
import { orchStore } from '../orch/store';
import { store } from '../store';
import { chatsApi } from './api';
import { byLast, laterThan } from './helpers';
import type { ThreadMessage } from '../orch/types';
import type { ChatEvent, ChatView, LeadChat, LeadChatList } from './types';

type Status = 'idle' | 'loading' | 'ready' | 'error' | 'unsupported';

export interface ProjectChats {
  items: ChatView[];
  status: Status;
  error: string;
}

export interface LeadChats {
  active: string;
  items: LeadChat[];
  status: Status;
}

interface State {
  projects: Record<string, ProjectChats>;
  lead: Record<string, LeadChats>;
}

const NO_CHATS: ProjectChats = { items: [], status: 'idle', error: '' };
const NO_LEAD: LeadChats = { active: '', items: [], status: 'idle' };

/** No such route: a hub from before chats. */
const unsupported = (err: unknown) => err instanceof ApiError && !err.code && (err.status === 404 || err.status === 405 || err.status === 501);

class ChatsStore {
  private state: State = { projects: {}, lead: {} };
  private listeners = new Set<() => void>();
  private installed = false;
  /** One load at a time per project / thread. */
  private loading = new Map<string, Promise<void>>();
  /** Asked again while a load ran: what it fetched may be from before the change. */
  private again = new Set<string>();
  /** By thread: the messages counted into its chats here (an update of one comes again). */
  private counted = new Map<string, Set<string>>();

  get = () => this.state;
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  private set(patch: Partial<State>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((l) => l());
  }

  install() {
    if (this.installed) return;
    this.installed = true;
    store.onEvent((ev) => this.apply(ev));
  }

  apply(ev: HubEvent | ChatEvent) {
    switch (ev.type) {
      case 'snapshot':
        // reconnected: what is shown may have missed events
        for (const pid of Object.keys(this.state.projects)) if (this.state.projects[pid].status === 'ready') void this.load(pid, true);
        for (const tid of Object.keys(this.state.lead)) if (this.state.lead[tid].status === 'ready') void this.loadLead(tid, true);
        break;
      case 'chat':
        this.upsert(ev.chat);
        break;
      case 'leadChats': {
        const cur = this.state.lead[ev.threadId];
        if (!cur || cur.status === 'idle') break;
        // a thread's first chat, made with its first message, holds what is shown already
        if (ev.active && cur.active && ev.active !== cur.active) this.switchTo(ev.threadId, ev.active);
        else if (ev.active && !cur.active) this.set({ lead: { ...this.state.lead, [ev.threadId]: { ...cur, active: ev.active } } });
        // the true counts and times (the hub sends this when they change too, at most every 3 s)
        void this.loadLead(ev.threadId, true);
        break;
      }
      case 'thread':
        this.countMessage(ev.threadId, ev.message);
        break;
      default:
        break;
    }
  }

  // ---- agent chats ----

  upsert(c: ChatView) {
    const cur = this.state.projects[c.projectId];
    // a list on its way may be from before this: it is asked for once more when it lands
    const key = `p:${c.projectId}`;
    if (this.loading.has(key)) this.again.add(key);
    // a project nobody shows: loaded fresh when it is
    if (!cur || cur.status !== 'ready') return;
    const items = [...cur.items.filter((x) => x.id !== c.id), c].sort(byLast);
    this.set({ projects: { ...this.state.projects, [c.projectId]: { ...cur, items } } });
  }

  load(projectId: string, force = false): Promise<void> {
    const cur = this.state.projects[projectId];
    if (!force && cur && cur.status !== 'idle' && cur.status !== 'error') return this.loading.get(`p:${projectId}`) ?? Promise.resolve();
    const key = `p:${projectId}`;
    const running = this.loading.get(key);
    if (running) {
      if (force) this.again.add(key);
      return running;
    }
    if (!cur || cur.status !== 'ready') this.set({ projects: { ...this.state.projects, [projectId]: { ...(cur ?? NO_CHATS), status: 'loading', error: '' } } });
    const p = chatsApi
      .list(projectId, { limit: 500 })
      .then((items) => {
        this.set({ projects: { ...this.state.projects, [projectId]: { items: items.slice().sort(byLast), status: 'ready', error: '' } } });
      })
      .catch((err) => {
        const prev = this.state.projects[projectId] ?? NO_CHATS;
        this.set({
          projects: { ...this.state.projects, [projectId]: { ...prev, status: unsupported(err) ? 'unsupported' : 'error', error: err instanceof Error ? err.message : String(err) } },
        });
      })
      .finally(() => this.settle(key, () => this.load(projectId, true)));
    this.loading.set(key, p);
    return p;
  }

  /** A load ended: once more when it was asked for again meanwhile. */
  private settle(key: string, reload: () => Promise<void>) {
    this.loading.delete(key);
    if (this.again.delete(key)) void reload();
  }

  // ---- lead chats ----

  /** Another chat became active: its messages replace the thread's. */
  private switchTo(threadId: string, active: string) {
    const cur = this.state.lead[threadId] ?? NO_LEAD;
    this.set({ lead: { ...this.state.lead, [threadId]: { ...cur, active } } });
    orchStore.apply({ type: 'threadCleared', threadId });
    void orchStore.loadThread(threadId, true);
  }

  /**
   * A new message of a thread (its "thread" event): one more in its chat, and its time is the
   * chat's last. The hub announces the list only when it changes otherwise. A message already
   * counted here, or not later than the chat's last (an update, or one the list counted), is not.
   */
  private countMessage(threadId: string, m: ThreadMessage) {
    const cur = this.state.lead[threadId];
    const key = `t:${threadId}`;
    if (this.loading.has(key)) this.again.add(key);
    if (!cur || cur.status !== 'ready' || !m.chatId) return;
    let seen = this.counted.get(threadId);
    if (!seen) this.counted.set(threadId, (seen = new Set()));
    if (seen.has(m.id)) return;
    const chat = cur.items.find((c) => c.id === m.chatId);
    if (!chat) {
      // a chat this list does not have yet
      void this.loadLead(threadId, true);
      return;
    }
    if (!laterThan(m.at, chat.lastAt)) return;
    seen.add(m.id);
    if (seen.size > 2000) seen.delete(seen.values().next().value as string);
    const next = { ...chat, messages: chat.messages + 1, lastAt: m.at };
    const items = [next, ...cur.items.filter((c) => c.id !== chat.id)].sort((a, b) => (laterThan(a.lastAt || a.createdAt, b.lastAt || b.createdAt) ? -1 : laterThan(b.lastAt || b.createdAt, a.lastAt || a.createdAt) ? 1 : 0));
    this.set({ lead: { ...this.state.lead, [threadId]: { ...cur, items } } });
  }

  /** A list from the hub (GET, or the answer to a change). */
  setLead(threadId: string, list: LeadChatList) {
    const cur = this.state.lead[threadId];
    if (cur && cur.status === 'ready' && list.active && cur.active && list.active !== cur.active) this.switchTo(threadId, list.active);
    const now = this.state.lead[threadId] ?? NO_LEAD;
    this.set({ lead: { ...this.state.lead, [threadId]: { ...now, active: list.active ?? '', items: list.items ?? [], status: 'ready' } } });
  }

  loadLead(threadId: string, force = false): Promise<void> {
    const cur = this.state.lead[threadId];
    const key = `t:${threadId}`;
    const running = this.loading.get(key);
    if (running) {
      if (force) this.again.add(key);
      return running;
    }
    if (!force && cur && cur.status !== 'idle' && cur.status !== 'error') return Promise.resolve();
    if (!cur || cur.status === 'idle') this.set({ lead: { ...this.state.lead, [threadId]: { ...NO_LEAD, status: 'loading' } } });
    const p = chatsApi
      .leadChats(threadId)
      .then((list) => this.setLead(threadId, list))
      .catch((err) => {
        const prev = this.state.lead[threadId] ?? NO_LEAD;
        this.set({ lead: { ...this.state.lead, [threadId]: { ...prev, status: unsupported(err) ? 'unsupported' : 'error' } } });
      })
      .finally(() => this.settle(key, () => this.loadLead(threadId, true)));
    this.loading.set(key, p);
    return p;
  }

  /** + New chat: the old one is kept, the thread shows the new (empty) one. */
  async newLeadChat(threadId: string) {
    const r = await chatsApi.newLeadChat(threadId);
    if (r && 'items' in r) return this.setLead(threadId, r);
    const cur = this.state.lead[threadId];
    if (r?.id && cur && cur.status === 'ready' && cur.active !== r.id) {
      this.set({ lead: { ...this.state.lead, [threadId]: { ...cur, items: [r, ...cur.items.filter((c) => c.id !== r.id)] } } });
      this.switchTo(threadId, r.id);
    }
    await this.loadLead(threadId, true);
  }

  /** Resume a chat of the thread. */
  async activate(threadId: string, chatId: string) {
    await chatsApi.activateLeadChat(threadId, chatId);
    if ((this.state.lead[threadId]?.active ?? '') !== chatId) this.switchTo(threadId, chatId);
    await this.loadLead(threadId, true);
  }

  reset() {
    this.state = { projects: {}, lead: {} };
    this.loading.clear();
    this.again.clear();
    this.counted.clear();
    this.listeners.forEach((l) => l());
  }
}

export const chatsStore = new ChatsStore();

function useChatsState<T>(select: (s: State) => T): T {
  return useSyncExternalStore(chatsStore.subscribe, () => select(chatsStore.get()));
}

/** A project's agent chats, loaded on first use and kept fresh by events. */
export function useProjectChats(projectId: string | undefined): ProjectChats {
  useEffect(() => {
    if (!projectId) return;
    chatsStore.install();
    void chatsStore.load(projectId);
  }, [projectId]);
  return useChatsState((s) => (projectId ? (s.projects[projectId] ?? NO_CHATS) : NO_CHATS));
}

/** A thread's lead chats (tid = project id or an orchestrator card's id). */
export function useLeadChats(threadId: string | null | undefined): LeadChats {
  useEffect(() => {
    if (!threadId) return;
    chatsStore.install();
    void chatsStore.loadLead(threadId);
  }, [threadId]);
  return useChatsState((s) => (threadId ? (s.lead[threadId] ?? NO_LEAD) : NO_LEAD));
}

/** Just the active chat id, so a thread re-renders only when it changes. */
export function useLeadActive(threadId: string | null | undefined): string {
  useEffect(() => {
    if (!threadId) return;
    chatsStore.install();
    void chatsStore.loadLead(threadId);
  }, [threadId]);
  return useChatsState((s) => (threadId ? (s.lead[threadId]?.active ?? '') : ''));
}
