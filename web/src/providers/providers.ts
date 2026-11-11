// API providers (docs/API-agents.md §10.2): an Anthropic- or OpenAI-compatible endpoint a console
// of Claude Code, Codex or OpenCode can run on instead of a subscription login. The key never
// comes back from the hub: the list only says whether one is set.

import { useEffect, useSyncExternalStore } from 'react';
import { apiRequest } from '../api';
import type { Agent } from '../lib/types';
import { store } from '../store';

export interface ProviderPrice {
  input: number;
  output: number;
  cacheRead?: number;
  cacheWrite?: number;
}

export interface ApiProvider {
  id: string;
  name: string;
  kind: 'anthropic' | 'openai';
  baseUrl: string;
  /** openai: Chat Completions or the Responses API (Codex needs the latter). */
  api?: 'chat' | 'responses';
  /** anthropic: Authorization: Bearer (most services) or x-api-key (Anthropic's own). */
  auth?: 'bearer' | 'x-api-key';
  model: string;
  fastModel?: string;
  models?: string[];
  prices?: Record<string, ProviderPrice>;
  /** US dollars per calendar month; 0 or absent = none. */
  budget?: number;
  hasKey: boolean;
  createdAt: string;
  updatedAt: string;
  /** This month so far, in dollars, at its prices. */
  monthCost?: number;
  /** Consoles running on it. */
  inUse?: number;
}

export interface ProviderDraft {
  name?: string;
  kind?: ApiProvider['kind'];
  baseUrl?: string;
  api?: ApiProvider['api'];
  auth?: ApiProvider['auth'];
  model?: string;
  fastModel?: string;
  models?: string[];
  prices?: Record<string, ProviderPrice>;
  budget?: number;
  /** "" keeps the key a provider has. */
  apiKey?: string;
}

export interface TestResult {
  ok: boolean;
  models?: string[];
  checked?: 'models' | 'message';
  status?: number;
  error?: string;
}

export interface Tokens {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

export interface Spend {
  month: string;
  cost: number;
  budget?: number;
  tokens: Tokens;
  byModel: (Tokens & { model: string; cost: number; priced: boolean })[];
  unpriced?: string[];
}

const enc = encodeURIComponent;

export const providersApi = {
  list: () => apiRequest<{ items: ApiProvider[] }>('GET', '/api/providers').then((r) => r.items ?? []),
  create: (d: ProviderDraft) => apiRequest<ApiProvider>('POST', '/api/providers', d),
  update: (id: string, d: ProviderDraft) => apiRequest<ApiProvider>('PATCH', `/api/providers/${enc(id)}`, d),
  remove: (id: string) => apiRequest<{ ok: boolean }>('DELETE', `/api/providers/${enc(id)}`),
  /** A draft as typed, or a saved one by id, with the typed key or else the saved one. */
  test: (d: ProviderDraft & { id?: string }) => apiRequest<TestResult>('POST', '/api/providers/test', d),
  spend: (id: string, month?: string) => apiRequest<Spend>('GET', `/api/providers/${enc(id)}/spend${month ? `?month=${enc(month)}` : ''}`),
  sessionCost: (sessionId: string) => apiRequest<Spend>('GET', `/api/sessions/${enc(sessionId)}/cost`),
};

/** The CLI a console is built on, as the hub counts it. */
export function baseOf(a: Pick<Agent, 'id' | 'base'>): string {
  return a.base || a.id;
}

/** Which agents a provider can run: Claude Code on Anthropic's API, Codex on the Responses API, OpenCode on both. */
export function compatible(a: Pick<Agent, 'id' | 'base'> | undefined, p: ApiProvider): boolean {
  if (!a) return false;
  switch (baseOf(a)) {
    case 'claude':
      return p.kind === 'anthropic';
    case 'codex':
      return p.kind === 'openai' && p.api === 'responses';
    case 'opencode':
      return true;
  }
  return false;
}

/** The models to offer for a provider: the default, the fast one, and those it listed. */
export function providerModels(p: ApiProvider): string[] {
  return [...new Set([p.model, ...(p.fastModel ? [p.fastModel] : []), ...(p.models ?? [])])];
}

export function money(n: number | undefined): string {
  if (n == null) return '—';
  if (n > 0 && n < 0.01) return '<$0.01';
  return `$${n.toFixed(2)}`;
}

export function tokensShort(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${Math.round(n / 1e3)}k`;
  return String(n);
}

// ---- the list, kept current by events

class ProvidersStore {
  private list: ApiProvider[] = [];
  private loaded = false;
  private loading: Promise<void> | null = null;
  private listeners = new Set<() => void>();
  private installed = false;

  get = () => this.list;
  isLoaded = () => this.loaded;
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  private set(list: ApiProvider[]) {
    this.list = [...list].sort((a, b) => a.name.localeCompare(b.name));
    this.loaded = true;
    this.listeners.forEach((l) => l());
  }

  install() {
    if (this.installed) return;
    this.installed = true;
    store.onEvent((ev) => {
      const e = ev as { type: string; provider?: ApiProvider; id?: string };
      if (e.type === 'provider' && e.provider) this.upsert(e.provider);
      else if (e.type === 'providerRemoved' && e.id) this.set(this.list.filter((p) => p.id !== e.id));
      else if (e.type === 'snapshot' && this.loaded) void this.load(true);
    });
  }

  upsert(p: ApiProvider) {
    const i = this.list.findIndex((x) => x.id === p.id);
    const next = [...this.list];
    if (i >= 0) next[i] = { ...next[i], ...p };
    else next.push(p);
    this.set(next);
  }

  remove(id: string) {
    this.set(this.list.filter((p) => p.id !== id));
  }

  load(force = false): Promise<void> {
    if (this.loaded && !force) return Promise.resolve();
    this.loading ??= providersApi
      .list()
      .then((l) => this.set(l))
      .catch(() => this.set([])) // a hub without providers
      .finally(() => {
        this.loading = null;
      });
    return this.loading;
  }
}

export const providersStore = new ProvidersStore();

export function useProviders(): ApiProvider[] {
  useEffect(() => {
    providersStore.install();
    void providersStore.load();
  }, []);
  return useSyncExternalStore(providersStore.subscribe, providersStore.get);
}

export function useProvider(id: string | undefined): ApiProvider | undefined {
  const list = useProviders();
  return id ? list.find((p) => p.id === id) : undefined;
}
