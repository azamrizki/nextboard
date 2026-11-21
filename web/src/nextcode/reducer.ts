// The chat's state from the hub's frames (docs/API-agents.md §30), kept pure so it can be tested:
// a snapshot replaces everything, an item is new or replaces the one with its id, a streamed
// block grows with its deltas until the item whose `stream` is its id takes its place, requests
// come and go, and the status is the latest one.

import type { BlockKind, Frame, Item, Request, Status } from './types';

/** A block being streamed: what the deltas said so far. */
export interface Stream {
  id: string;
  kind: BlockKind;
  /** A tool's name. */
  name?: string;
  parent?: string;
  text: string;
  /** The turn ended before its item came (stopped): drawn without the caret. */
  ended?: boolean;
}

export interface ChatState {
  /** A snapshot came: until then the view shows that it is loading. */
  loaded: boolean;
  items: Item[];
  /** Older items are on the hub (GET ?before=). */
  more: boolean;
  requests: Request[];
  status: Status | null;
  streams: Stream[];
}

/** Older items, from GET ?before= (put in front of what is there). */
export type ChatAction = Frame | { t: 'older'; items: Item[]; more: boolean };

export const emptyChat: ChatState = { loaded: false, items: [], more: false, requests: [], status: null, streams: [] };

function upsert<T extends { id: string }>(list: T[], x: T): T[] {
  const i = list.findIndex((y) => y.id === x.id);
  if (i < 0) return [...list, x];
  const next = list.slice();
  next[i] = x;
  return next;
}

export function reduce(state: ChatState, a: ChatAction): ChatState {
  switch (a.t) {
    case 'snapshot':
      // what was being streamed is not in a snapshot: its item comes later, or it was lost
      return {
        loaded: true,
        items: a.items ?? [],
        more: !!a.more,
        requests: a.requests ?? [],
        status: a.status ?? state.status,
        streams: [],
      };
    case 'item': {
      const it = a.item;
      if (!it?.id) return state;
      const streams = it.stream ? state.streams.filter((s) => s.id !== it.stream) : state.streams;
      return { ...state, items: upsert(state.items, it), streams: streams.length === state.streams.length ? state.streams : streams };
    }
    case 'block': {
      if (!a.id || state.streams.some((s) => s.id === a.id)) return state;
      // an item that came first (a frame order the hub does not promise against) wins
      if (state.items.some((i) => i.stream === a.id)) return state;
      return { ...state, streams: [...state.streams, { id: a.id, kind: a.kind, name: a.name || undefined, parent: a.parent || undefined, text: '' }] };
    }
    case 'delta': {
      const i = state.streams.findIndex((s) => s.id === a.id);
      // a block that started before this view's snapshot: its start is missing, so it waits for its item
      if (i < 0 || !a.text) return state;
      const streams = state.streams.slice();
      streams[i] = { ...streams[i], text: streams[i].text + a.text };
      return { ...state, streams };
    }
    case 'request':
      if (!a.request?.id) return state;
      return { ...state, requests: upsert(state.requests, a.request) };
    case 'resolved': {
      const requests = state.requests.filter((r) => r.id !== a.id);
      return requests.length === state.requests.length ? state : { ...state, requests };
    }
    case 'status': {
      if (!a.status) return state;
      // the turn is over: a block still open will not grow any more
      const over = !a.status.working && state.streams.some((s) => !s.ended);
      return { ...state, status: a.status, streams: over ? state.streams.map((s) => (s.ended ? s : { ...s, ended: true })) : state.streams };
    }
    case 'older': {
      const have = new Set(state.items.map((i) => i.id));
      const older = a.items.filter((i) => !have.has(i.id));
      return { ...state, items: [...older, ...state.items], more: a.more };
    }
    default:
      return state;
  }
}

/** Several frames at once (the view applies what came in one animation frame together). */
export function reduceAll(state: ChatState, list: ChatAction[]): ChatState {
  let s = state;
  for (const a of list) s = reduce(s, a);
  return s;
}

/** A sub-agent's tool call: its items have `parent` set to the call's id. */
export const SUB_AGENT_TOOLS = ['Task', 'Agent'];

/**
 * The items of the main conversation, and each tool call's sub-agent items by the call's id. An
 * item whose parent is not loaded (it is further back) shows in the main conversation.
 */
export function groupItems(items: Item[]): { top: Item[]; children: Map<string, Item[]>; ids: Set<string> } {
  const ids = new Set(items.map((i) => i.id));
  const top: Item[] = [];
  const children = new Map<string, Item[]>();
  for (const it of items) {
    if (it.parent && it.parent !== it.id && ids.has(it.parent)) {
      const list = children.get(it.parent);
      if (list) list.push(it);
      else children.set(it.parent, [it]);
    } else top.push(it);
  }
  return { top, children, ids };
}

/** The open streams the same way: of the main conversation, and by the sub-agent's call. */
export function groupStreams(streams: Stream[], ids: Set<string>): { top: Stream[]; children: Map<string, Stream[]> } {
  const top: Stream[] = [];
  const children = new Map<string, Stream[]>();
  for (const s of streams) {
    if (s.parent && ids.has(s.parent)) {
      const list = children.get(s.parent);
      if (list) list.push(s);
      else children.set(s.parent, [s]);
    } else top.push(s);
  }
  return { top, children };
}
