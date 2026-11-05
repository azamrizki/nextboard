// Agent catalog (docs/API-agents.md §1): the global list (GET /api/agents) and the
// per-machine list with availability + default agent (GET /api/machines/{id}/agents).
// Small shared caches so every terminal header reads capabilities without refetching;
// refreshed on the "agents" / "settings" events and when a machine is re-probed.
// Phase-4 hubs (no /api/agents) fall back to /api/presets.

import { useEffect, useMemo, useSyncExternalStore } from 'react';
import { api, isStatus } from '../api';
import type { Agent, Preset } from '../lib/types';
import { store } from '../store';

/** Preset (Phase 4) → Agent with conservative capabilities. */
export function presetToAgent(p: Preset): Agent {
  const shell = p.command === '$SHELL' || p.id === 'shell';
  const cont = p.resumeArgs ?? [];
  return {
    id: p.id,
    name: p.name,
    kind: shell ? 'shell' : 'agent',
    source: 'builtin',
    base: '',
    command: p.command,
    args: p.args,
    env: null,
    color: p.color,
    detect: '',
    yoloArgs: [],
    yoloEnv: [],
    resume: { kind: cont.length ? 'continue' : 'none', launchArgs: [], resumeArgs: [], continueArgs: cont, pickerArgs: [] },
    liveStatus: 'none',
    transcript: 'none',
    profileEnv: '',
    screenRules: '',
    resumeArgs: cont,
    available: p.available,
  };
}

// ---- capabilities ----

export const hasYolo = (a: Agent | undefined) => !!a && a.kind === 'agent' && ((a.yoloArgs?.length ?? 0) > 0 || (a.yoloEnv?.length ?? 0) > 0);
export const canResumeExact = (a: Agent | undefined) => !!a && a.resume?.kind === 'session-id' && (a.resume.resumeArgs?.length ?? 0) > 0;
export const canContinue = (a: Agent | undefined) => !!a && (a.resume?.continueArgs?.length ?? 0) > 0;
export const canPickInCli = (a: Agent | undefined) => !!a && (a.resume?.pickerArgs?.length ?? 0) > 0;
export const hasTranscript = (a: Agent | undefined) => !!a && !!a.transcript && a.transcript !== 'none';
export const supportsProfiles = (a: Agent | undefined) => !!a && !!a.profileEnv;
export const hasLiveStatus = (a: Agent | undefined) => !!a && (a.liveStatus === 'claude-hooks' || a.liveStatus === 'codex-notify');

export const SOURCE_LABEL: Record<string, string> = { builtin: 'Built-in', catalog: 'Catalog', custom: 'Your agent' };

/** Agent display order for pickers: claude, codex, gemini first, then by name. */
const PRIORITY = ['claude', 'codex', 'gemini'];
export function byPriority(a: Agent, b: Agent): number {
  const pa = PRIORITY.indexOf(a.id);
  const pb = PRIORITY.indexOf(b.id);
  if (pa !== pb) return (pa < 0 ? 99 : pa) - (pb < 0 ? 99 : pb);
  return a.name.localeCompare(b.name);
}

// ---- tiny cache with listeners ----

type Status = 'idle' | 'loading' | 'ready' | 'error';
interface Entry<T> {
  data: T | null;
  status: Status;
  error: string;
  key: string;
  /** Hub has no Phase-5 endpoint (fell back to presets). */
  legacy: boolean;
}

const listeners = new Set<() => void>();
let version = 0;
const emit = () => {
  version++;
  listeners.forEach((l) => l());
};
const subscribe = (fn: () => void) => {
  listeners.add(fn);
  return () => listeners.delete(fn);
};

let global: Entry<Agent[]> = { data: null, status: 'idle', error: '', key: '', legacy: false };
const perMachine = new Map<string, Entry<{ defaultAgent: string; agents: Agent[] }>>();
let globalById = new Map<string, Agent>();

function globalKey() {
  return String(store.getState().agentsRev);
}

function loadGlobal() {
  const key = globalKey();
  if (global.key === key && global.status !== 'idle' && global.status !== 'error') return;
  global = { ...global, status: 'loading', key };
  emit();
  api
    .agents()
    .then((list) => ({ list, legacy: false }))
    .catch(async (err) => {
      if (isStatus(err, 404)) return { list: (await api.presets()).map(presetToAgent), legacy: true };
      throw err;
    })
    .then(({ list, legacy }) => {
      if (global.key !== key) return;
      global = { data: list, status: 'ready', error: '', key, legacy };
      globalById = new Map(list.map((a) => [a.id, a]));
      emit();
    })
    .catch((err) => {
      if (global.key !== key) return;
      global = { ...global, status: 'error', error: err instanceof Error ? err.message : String(err) };
      emit();
    });
}

function machineKey(machineId: string) {
  const st = store.getState();
  const m = st.machines[machineId];
  const s = st.settings?.agents;
  return [
    st.agentsRev,
    m?.probe?.probedAt ?? '',
    m?.status ?? '',
    s?.defaults?.[machineId] ?? '',
    JSON.stringify(s?.paths?.[machineId] ?? {}),
    JSON.stringify(s?.disabled ?? {}),
  ].join('|');
}

function loadMachine(machineId: string, force = false) {
  const key = machineKey(machineId);
  const cur = perMachine.get(machineId);
  if (!force && cur && cur.key === key && cur.status !== 'error') return;
  perMachine.set(machineId, { data: cur?.data ?? null, status: 'loading', error: '', key, legacy: cur?.legacy ?? false });
  emit();
  api
    .machineAgents(machineId)
    .then((r) => ({ defaultAgent: r.defaultAgent, agents: r.agents ?? [], legacy: false }))
    .catch(async (err) => {
      if (!isStatus(err, 404)) throw err;
      const agents = (await api.machinePresets(machineId)).map(presetToAgent);
      const def = agents.find((a) => a.available !== false && a.kind === 'agent')?.id ?? 'shell';
      return { defaultAgent: def, agents, legacy: true };
    })
    .then(({ legacy, ...data }) => {
      if (perMachine.get(machineId)?.key !== key) return;
      perMachine.set(machineId, { data, status: 'ready', error: '', key, legacy });
      emit();
    })
    .catch((err) => {
      if (perMachine.get(machineId)?.key !== key) return;
      perMachine.set(machineId, { data: cur?.data ?? null, status: 'error', error: err instanceof Error ? err.message : String(err), key, legacy: false });
      emit();
    });
}

/** Re-validate caches whenever the inputs of their keys change. */
let watching = false;
function watch() {
  if (watching) return;
  watching = true;
  store.subscribe(() => {
    if (global.status !== 'idle' && global.key !== globalKey()) loadGlobal();
    for (const id of perMachine.keys()) if (perMachine.get(id)?.key !== machineKey(id)) loadMachine(id);
  });
}

// ---- hooks ----

export interface CatalogView {
  agents: Agent[];
  ready: boolean;
  error: string;
  legacy: boolean;
}

const EMPTY: Agent[] = [];

/** Global catalog (no availability). */
export function useAgentCatalog(): CatalogView {
  useEffect(() => {
    watch();
    loadGlobal();
  }, []);
  const g = useSyncExternalStore(subscribe, () => global);
  return { agents: g.data ?? EMPTY, ready: g.status === 'ready' || !!g.data, error: g.error, legacy: g.legacy };
}

/** One catalog entry by id (capabilities for a terminal header). */
export function useAgent(id: string | undefined): Agent | undefined {
  useEffect(() => {
    watch();
    loadGlobal();
  }, []);
  return useSyncExternalStore(subscribe, () => (id ? globalById.get(id) : undefined));
}

export function agentById(id: string | undefined): Agent | undefined {
  return id ? globalById.get(id) : undefined;
}

export interface MachineAgentsView {
  agents: Agent[];
  defaultAgent: string;
  ready: boolean;
  loading: boolean;
  error: string;
  legacy: boolean;
  reload: () => void;
}

/** Agents of one machine with availability and its default agent. */
export function useMachineAgents(machineId: string | undefined): MachineAgentsView {
  const remote = !!machineId && machineId !== 'local';
  useEffect(() => {
    watch();
    if (machineId) loadMachine(machineId);
  }, [machineId]);
  const e = useSyncExternalStore(subscribe, () => (machineId ? perMachine.get(machineId) : undefined));
  const own = e?.data?.agents ?? EMPTY;
  // Agents always run on This PC. A server offers plain shells, plus what the
  // hub says can work on it from there.
  // What is turned off is not offered anywhere.
  const agents = useMemo(() => own.filter((a) => !a.disabled && (!remote || a.kind === 'shell' || a.available === true)), [remote, own]);
  return {
    agents,
    defaultAgent: remote ? (agents.find((a) => a.kind === 'agent')?.id ?? 'shell') : (e?.data?.defaultAgent ?? ''),
    ready: !!e?.data,
    loading: e?.status === 'loading',
    error: e?.error ?? '',
    legacy: e?.legacy ?? false,
    reload: () => machineId && loadMachine(machineId, true),
  };
}

/** Agents of several machines (Settings: detection per machine), also those turned off. */
export function useMachinesAgents(machineIds: string[]): Record<string, { defaultAgent: string; agents: Agent[] } | undefined> {
  const key = machineIds.join(',');
  useEffect(() => {
    watch();
    for (const id of key ? key.split(',') : []) loadMachine(id);
  }, [key]);
  const v = useSyncExternalStore(subscribe, () => version);
  return useMemo(() => {
    const out: Record<string, { defaultAgent: string; agents: Agent[] } | undefined> = {};
    for (const id of key ? key.split(',') : []) out[id] = perMachine.get(id)?.data ?? undefined;
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, v]);
}

/** Force a refetch everywhere (after custom agent CRUD). */
export function refreshAgents() {
  store.bumpAgents();
}

// ---- armed agent (per client, §1.2) ----

const ARMED_KEY = 'nb.agent.armed';
const armedListeners = new Set<() => void>();
function readArmed(): string {
  try {
    return window.localStorage.getItem(ARMED_KEY) ?? window.localStorage.getItem('nb.preset') ?? '';
  } catch {
    return '';
  }
}
let armed = readArmed();
export function getArmed(): string {
  return armed;
}
export function setArmed(id: string) {
  armed = id;
  try {
    window.localStorage.setItem(ARMED_KEY, id);
  } catch {
    /* ignore */
  }
  armedListeners.forEach((l) => l());
}
export function useArmed(): string {
  return useSyncExternalStore(
    (fn) => {
      armedListeners.add(fn);
      return () => armedListeners.delete(fn);
    },
    () => armed,
  );
}
