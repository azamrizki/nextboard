// Orchestration live state (Phase 6): threads, orders, confirmations, deliveries, loops,
// handoffs, forwards, jobs. Fed by the hub's /api/events (via store.onEvent) and by REST
// responses. Immutable snapshot + useSyncExternalStore, like the main LiveStore.

import { useSyncExternalStore } from 'react';
import { ApiError } from '../api';
import { CLIENT_ID } from '../lib/ids';
import type { HubEvent } from '../lib/types';
import { store } from '../store';
import { orchApi } from './api';
import type {
  Confirmation,
  Delivery,
  Forward,
  Handoff,
  Job,
  LoopDef,
  LoopHistoryEntry,
  OrchEvent,
  Order,
  PlannerStatus,
  ThreadMessage,
  UiAction,
} from './types';

export interface ThreadState {
  messages: ThreadMessage[];
  hasMore: boolean;
  status: 'idle' | 'loading' | 'ready' | 'error' | 'unsupported';
  error: string;
}

export interface OrchState {
  threads: Record<string, ThreadState>;
  orders: Record<string, Order>;
  confirmations: Record<string, Confirmation>;
  /** Deliveries by id (from events and per-session lists). */
  deliveries: Record<string, Delivery>;
  loops: Record<string, LoopDef>;
  loopsLoaded: Record<string, 'loading' | 'ready' | 'error' | 'unsupported'>;
  loopHistory: Record<string, LoopHistoryEntry[]>;
  handoffs: Record<string, Handoff>;
  forwards: Record<string, Forward>;
  jobs: Record<string, Job>;
  /** commandId → state for commands this client sent ("Still thinking…"). */
  pending: Record<string, { state: string; threadId: string; at: number }>;
  planner: PlannerStatus | null;
  /** Bumped on "layouts" events. */
  layoutsRev: number;
  /** projectId → bumped on "memory" events. */
  memoryRev: Record<string, number>;
  /** false once a Phase-6 endpoint answered 404 (hub too old). */
  supported: boolean | null;
}

const EMPTY_THREAD: ThreadState = { messages: [], hasMore: false, status: 'idle', error: '' };

const INITIAL: OrchState = {
  threads: {},
  orders: {},
  confirmations: {},
  deliveries: {},
  loops: {},
  loopsLoaded: {},
  loopHistory: {},
  handoffs: {},
  forwards: {},
  jobs: {},
  pending: {},
  planner: null,
  layoutsRev: 0,
  memoryRev: {},
  supported: null,
};

type UiHandler = (actions: UiAction[]) => void;

function sortMessages(list: ThreadMessage[]): ThreadMessage[] {
  return list.sort((a, b) => a.at.localeCompare(b.at) || a.id.localeCompare(b.id));
}

/** Merge messages by id (an update replaces the earlier version, e.g. "Still thinking…"). */
function mergeMessages(cur: ThreadMessage[], add: ThreadMessage[]): ThreadMessage[] {
  const byId = new Map(cur.map((m) => [m.id, m]));
  for (const m of add) byId.set(m.id, m);
  return sortMessages([...byId.values()]);
}

/** The hub has no such endpoint (older hub). Coded 404s (loop_not_found, …) are real answers. */
export const is404 = (err: unknown) => err instanceof ApiError && !err.code && (err.status === 404 || err.status === 405 || err.status === 501);

class OrchStore {
  private state: OrchState = INITIAL;
  private listeners = new Set<() => void>();
  private uiHandlers = new Set<UiHandler>();
  /** A node to focus once its board has opened (focus across boards). */
  pendingFocus: string | null = null;
  private installed = false;

  get = () => this.state;
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  private set(patch: Partial<OrchState>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((l) => l());
  }

  /** Start listening to hub events (idempotent). */
  install() {
    if (this.installed) return;
    this.installed = true;
    store.onEvent((ev) => this.apply(ev));
  }

  reset() {
    this.state = INITIAL;
    this.listeners.forEach((l) => l());
  }

  markUnsupported() {
    if (this.state.supported !== false) this.set({ supported: false });
  }
  markSupported() {
    if (this.state.supported !== true) this.set({ supported: true });
  }

  /** The board runs uiActions sent back for this client. */
  onUiActions(fn: UiHandler): () => void {
    this.uiHandlers.add(fn);
    return () => this.uiHandlers.delete(fn);
  }
  runUiActions(actions: UiAction[] | null | undefined) {
    if (!actions?.length) return;
    this.uiHandlers.forEach((h) => h(actions));
  }

  // ---- events ----

  apply(ev: HubEvent) {
    const e = ev as OrchEvent | HubEvent;
    switch (e.type) {
      case 'snapshot':
        // reconnected: refresh what is open (events may have been missed)
        for (const tid of Object.keys(this.state.threads)) if (this.state.threads[tid].status === 'ready') void this.loadThread(tid, true);
        break;
      case 'thread':
        this.addMessages(e.threadId, [e.message]);
        break;
      case 'threadCleared':
        this.set({ threads: { ...this.state.threads, [e.threadId]: { ...EMPTY_THREAD, status: 'ready' } } });
        break;
      case 'order':
        this.upsertOrder(e.order);
        break;
      case 'confirmation':
        this.upsertConfirmation(e.confirmation);
        break;
      case 'delivery':
        this.upsertDelivery(e.delivery);
        break;
      case 'command': {
        const cur = this.state.pending[e.commandId];
        if (cur || e.clientId === CLIENT_ID) {
          this.set({ pending: { ...this.state.pending, [e.commandId]: { state: e.state, threadId: cur?.threadId ?? '', at: cur?.at ?? Date.now() } } });
        }
        if (e.clientId === CLIENT_ID && e.state === 'done') this.runUiActions(e.uiActions);
        break;
      }
      case 'handoff':
        this.set({ handoffs: { ...this.state.handoffs, [e.handoff.id]: e.handoff } });
        break;
      case 'forward':
        this.set({ forwards: { ...this.state.forwards, [e.forward.id]: e.forward } });
        break;
      case 'loop':
        this.upsertLoop(e.loop);
        break;
      case 'loopRemoved': {
        if (!(e.id in this.state.loops)) break;
        const loops = { ...this.state.loops };
        delete loops[e.id];
        this.set({ loops });
        break;
      }
      case 'loopRun': {
        const cur = this.state.loopHistory[e.loopId];
        if (cur) this.set({ loopHistory: { ...this.state.loopHistory, [e.loopId]: [e.entry, ...cur].slice(0, 200) } });
        break;
      }
      case 'memory':
        this.set({ memoryRev: { ...this.state.memoryRev, [e.projectId]: (this.state.memoryRev[e.projectId] ?? 0) + 1 } });
        break;
      case 'layouts':
        this.set({ layoutsRev: this.state.layoutsRev + 1 });
        break;
      case 'job':
        this.upsertJob(e.job);
        break;
      default:
        break;
    }
  }

  // ---- mutations (also used for REST responses) ----

  addMessages(threadId: string, msgs: ThreadMessage[]) {
    if (!msgs.length) return;
    const cur = this.state.threads[threadId] ?? EMPTY_THREAD;
    this.set({ threads: { ...this.state.threads, [threadId]: { ...cur, messages: mergeMessages(cur.messages, msgs) } } });
  }
  upsertOrder(o: Order) {
    this.set({ orders: { ...this.state.orders, [o.id]: o } });
  }
  upsertConfirmation(c: Confirmation) {
    this.set({ confirmations: { ...this.state.confirmations, [c.id]: c } });
  }
  upsertDelivery(d: Delivery) {
    this.set({ deliveries: { ...this.state.deliveries, [d.id]: d } });
  }
  setDeliveries(list: Delivery[]) {
    const next = { ...this.state.deliveries };
    for (const d of list) next[d.id] = d;
    this.set({ deliveries: next });
  }
  upsertLoop(l: LoopDef) {
    this.set({ loops: { ...this.state.loops, [l.id]: l } });
  }
  removeLoop(id: string) {
    const loops = { ...this.state.loops };
    delete loops[id];
    this.set({ loops });
  }
  upsertHandoff(h: Handoff) {
    this.set({ handoffs: { ...this.state.handoffs, [h.id]: h } });
  }
  upsertJob(j: Job) {
    this.set({ jobs: { ...this.state.jobs, [j.id]: j } });
  }
  setPlanner(p: PlannerStatus | null) {
    this.set({ planner: p });
  }
  trackCommand(commandId: string, threadId: string, state: string) {
    this.set({ pending: { ...this.state.pending, [commandId]: { state, threadId, at: Date.now() } } });
  }

  // ---- loading ----

  async loadThread(threadId: string, force = false) {
    const cur = this.state.threads[threadId];
    if (!force && cur && (cur.status === 'loading' || cur.status === 'ready' || cur.status === 'unsupported')) return;
    this.set({ threads: { ...this.state.threads, [threadId]: { ...(cur ?? EMPTY_THREAD), status: 'loading', error: '' } } });
    try {
      const [res, orders] = await Promise.all([orchApi.messages(threadId), orchApi.orders(threadId).catch(() => [] as Order[])]);
      this.markSupported();
      const prev = this.state.threads[threadId] ?? EMPTY_THREAD;
      // Events only reach a connected client: a reload or a hub restart misses them.
      const confirmations = { ...this.state.confirmations };
      for (const c of res.confirmations ?? []) if (confirmations[c.id]?.state !== 'confirmed' && confirmations[c.id]?.state !== 'cancelled') confirmations[c.id] = c;
      const pending = { ...this.state.pending };
      if (res.planning !== undefined) {
        for (const id in pending) if (pending[id].threadId === threadId && pending[id].state === 'planning' && id !== res.planning) delete pending[id];
        if (res.planning && !pending[res.planning]) pending[res.planning] = { state: 'planning', threadId, at: Date.now() };
      }
      this.set({
        threads: {
          ...this.state.threads,
          [threadId]: { messages: mergeMessages(prev.messages, res.items ?? []), hasMore: !!res.hasMore, status: 'ready', error: '' },
        },
        confirmations,
        pending,
      });
      if (orders.length) {
        const next = { ...this.state.orders };
        for (const o of orders) next[o.id] = o;
        this.set({ orders: next });
      }
    } catch (err) {
      const unsupported = is404(err);
      if (unsupported) this.markUnsupported();
      const prev = this.state.threads[threadId] ?? EMPTY_THREAD;
      this.set({
        threads: {
          ...this.state.threads,
          [threadId]: { ...prev, status: unsupported ? 'unsupported' : 'error', error: err instanceof Error ? err.message : String(err) },
        },
      });
    }
  }

  async loadOlder(threadId: string) {
    const cur = this.state.threads[threadId];
    if (!cur || !cur.hasMore || !cur.messages.length) return;
    const res = await orchApi.messages(threadId, cur.messages[0].id);
    const prev = this.state.threads[threadId] ?? EMPTY_THREAD;
    this.set({
      threads: { ...this.state.threads, [threadId]: { ...prev, messages: mergeMessages(prev.messages, res.items ?? []), hasMore: !!res.hasMore } },
    });
  }

  async loadLoops(projectId: string, force = false) {
    const st = this.state.loopsLoaded[projectId];
    if (!force && (st === 'loading' || st === 'ready' || st === 'unsupported')) return;
    this.set({ loopsLoaded: { ...this.state.loopsLoaded, [projectId]: 'loading' } });
    try {
      const list = await orchApi.loops(projectId);
      const loops = { ...this.state.loops };
      for (const id of Object.keys(loops)) if (loops[id].projectId === projectId) delete loops[id];
      for (const l of list) loops[l.id] = l;
      this.set({ loops, loopsLoaded: { ...this.state.loopsLoaded, [projectId]: 'ready' } });
    } catch (err) {
      this.set({ loopsLoaded: { ...this.state.loopsLoaded, [projectId]: is404(err) ? 'unsupported' : 'error' } });
    }
  }

  async loadLoopHistory(loopId: string) {
    try {
      const list = await orchApi.loopHistory(loopId);
      this.set({ loopHistory: { ...this.state.loopHistory, [loopId]: list } });
    } catch {
      this.set({ loopHistory: { ...this.state.loopHistory, [loopId]: this.state.loopHistory[loopId] ?? [] } });
    }
  }

  async loadPlanner() {
    try {
      this.setPlanner(await orchApi.planner());
    } catch (err) {
      this.setPlanner({ available: false, reason: is404(err) ? 'unsupported' : 'unknown' });
    }
  }
}

export const orchStore = new OrchStore();

// ---- hooks ----------------------------------------------------------------------------

export function useOrch<T>(select: (s: OrchState) => T): T {
  return useSyncExternalStore(orchStore.subscribe, () => select(orchStore.get()));
}

export function useThread(threadId: string | null | undefined): ThreadState {
  return useOrch((s) => (threadId ? (s.threads[threadId] ?? EMPTY_THREAD) : EMPTY_THREAD));
}
