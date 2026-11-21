// NextCode's engines (docs/API-agents.md §30): one NextCode, run by Claude Code (agent "nextcode"),
// by Codex (agent "nextcode-codex"), each with logins of its own, or by its own engine on an API
// provider (agent "nextcode-api": no CLI and no login). The pickers show NextCode once, with an
// Engine control that picks between the agents; everywhere else the catalog's `engine` says which
// consoles are chats. Pure, so tests can read it.

import type { Agent } from '../lib/types';

export type Engine = 'claude' | 'codex' | 'api';

/** The built-in NextCode agent of each engine. */
export const NEXTCODE_AGENTS: Record<Engine, string> = { claude: 'nextcode', codex: 'nextcode-codex', api: 'nextcode-api' };

export const ENGINES: { value: Engine; label: string }[] = [
  { value: 'claude', label: 'Claude' },
  { value: 'codex', label: 'Codex' },
  { value: 'api', label: 'API' },
];

/** What the API engine takes as an effort (the hub's list wins when it gives one). */
export const API_EFFORTS: readonly string[] = ['low', 'medium', 'high', 'xhigh', 'max'];

/** Where an API provider is added, and why the API engine cannot start without one. */
export const PROVIDERS_PATH = '/settings/providers';
export const NEEDS_PROVIDER = 'NextCode’s API engine runs on an API provider. Add one in Settings › API providers, then start the console again.';

type AgentLike = Pick<Agent, 'id'> & { engine?: string; available?: boolean };

const BUILTINS = new Set(Object.values(NEXTCODE_AGENTS));

/** One of the built-in NextCode agents (not a custom agent with an engine). */
export const isNextCodeBuiltin = (id: string | undefined) => !!id && BUILTINS.has(id);

/** "Claude", "Codex", "API", or the engine as the hub names it; '' for none. */
export function engineLabel(engine: string | undefined): string {
  if (!engine) return '';
  return ENGINES.find((e) => e.value === engine)?.label ?? engine[0].toUpperCase() + engine.slice(1);
}

/** The engine of a NextCode agent: its catalog engine, else what the built-in's id says; '' for a terminal agent. */
export function engineOf(a: AgentLike | undefined): string {
  if (!a) return '';
  if (a.engine) return a.engine;
  return ENGINES.find((e) => NEXTCODE_AGENTS[e.value] === a.id)?.value ?? '';
}

/** NextCode's own engine: no CLI and no login profiles, it runs on an API provider. */
export const isApiEngine = (a: AgentLike | undefined) => engineOf(a) === 'api';

/** The built-in NextCode agent of an engine in a list, if the hub has it there. */
export function agentOfEngine<T extends AgentLike>(agents: T[], engine: Engine): T | undefined {
  return agents.find((a) => a.id === NEXTCODE_AGENTS[engine]);
}

export interface EngineChoice {
  value: Engine;
  label: string;
  agentId: string;
  disabled: boolean;
  title: string;
}

/** What runs each engine, for the titles. */
const RUNS_ON: Record<Engine, { cli: string; needs: string }> = {
  claude: { cli: 'Claude Code', needs: 'Claude Code' },
  codex: { cli: 'Codex', needs: 'the codex CLI' },
  api: { cli: '', needs: '' },
};

export interface EngineChoiceOptions {
  /** The machine's name, for why an engine is not offered. */
  where?: string;
  /** How many API providers there are: with none, the API engine cannot start. Unknown when absent. */
  providers?: number;
}

/**
 * The Engine control's options for the agents of a list: the engines the hub has there, none
 * unless at least two (an older hub, or the others turned off in Settings). An engine whose CLI is
 * not installed is disabled, and the API engine with no API provider to run on.
 */
export function engineChoices(agents: AgentLike[], opts: EngineChoiceOptions | string = {}): EngineChoice[] {
  const { where = 'this machine', providers } = typeof opts === 'string' ? { where: opts } : opts;
  const rows = ENGINES.flatMap((e) => {
    const a = agentOfEngine(agents, e.value);
    return a ? [{ e, a }] : [];
  });
  if (rows.length < 2) return [];
  return rows.map(({ e, a }) => {
    const missing = a.available === false;
    const noProvider = e.value === 'api' && providers === 0;
    const own = RUNS_ON[e.value];
    let title: string;
    if (e.value === 'api') title = missing ? `The API engine does not run on ${where}` : noProvider ? 'Add an API provider in Settings › API providers first' : 'NextCode’s own engine: no CLI, it runs on an API provider';
    else title = missing ? `The ${e.label} engine needs ${own.needs}, which is not installed on ${where}` : `NextCode runs on ${own.cli}, with its own logins`;
    return { value: e.value, label: e.label, agentId: a.id, disabled: missing || noProvider, title };
  });
}

/**
 * The list with NextCode once: of the built-ins, the one chosen (`selected`) takes the place of
 * the first, else the preferred engine's when it can run here, else one that can.
 */
export function foldEngines<T extends AgentLike>(agents: T[], selected?: string, prefer: Engine = preferredEngine()): T[] {
  const builtins = agents.filter((a) => isNextCodeBuiltin(a.id));
  if (builtins.length < 2) return agents;
  const usable = (a: T) => a.available !== false;
  const keep = builtins.find((a) => a.id === selected) ?? builtins.find((a) => a.id === NEXTCODE_AGENTS[prefer] && usable(a)) ?? builtins.find(usable) ?? builtins[0];
  const first = builtins[0];
  return agents.flatMap((a) => (!isNextCodeBuiltin(a.id) ? [a] : a === first ? [keep] : []));
}

/** The other engine's agent for a NextCode agent and the engine picked: what the control switches to. */
export function engineSwitch<T extends AgentLike>(agents: T[], engine: Engine): string {
  return agentOfEngine(agents, engine)?.id ?? '';
}

/**
 * An agent's name in a list where several NextCode engines may stand side by side (menus with no
 * Engine control): "NextCode · Claude", "NextCode · Codex" and "NextCode · API" when the list has
 * two or more of them, else the name.
 */
export function agentChoiceName(a: Pick<Agent, 'id' | 'name'> & { engine?: string }, list: Pick<Agent, 'id'>[]): string {
  if (!isNextCodeBuiltin(a.id)) return a.name;
  const several = ENGINES.filter((e) => list.some((x) => x.id === NEXTCODE_AGENTS[e.value])).length > 1;
  return several ? `${a.name} · ${engineLabel(engineOf(a))}` : a.name;
}

/**
 * The API provider a console of the API engine starts on: the one chosen when it is offered, else
 * the default of Settings › NextCode (settings.nextcode.provider) when offered, else the first.
 * '' when there is none to run on.
 */
export function apiProviderChoice(chosen: string | undefined, providers: { id: string }[], defaultProvider?: string): string {
  const has = (id: string | undefined) => !!id && providers.some((p) => p.id === id);
  if (has(chosen)) return chosen!;
  if (has(defaultProvider)) return defaultProvider!;
  return providers[0]?.id ?? '';
}

// ---- the engine picked last, per browser ----

const ENGINE_KEY = 'nb.nextcode.engine';

let preferred: Engine | null = null;

/** The engine NextCode was last started with in this browser: a NextCode tile opens on it. */
export function preferredEngine(): Engine {
  if (preferred) return preferred;
  try {
    const v = window.localStorage.getItem(ENGINE_KEY);
    preferred = v === 'codex' || v === 'api' ? v : 'claude';
  } catch {
    preferred = 'claude';
  }
  return preferred;
}

export function rememberEngine(e: Engine) {
  preferred = e;
  try {
    window.localStorage.setItem(ENGINE_KEY, e);
  } catch {
    /* ignore */
  }
}
