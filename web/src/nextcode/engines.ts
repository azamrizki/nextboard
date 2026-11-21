// NextCode's engines (docs/API-agents.md §30): one NextCode, run by Claude Code (agent "nextcode"),
// by Codex (agent "nextcode-codex"), each with logins of its own, or by its own engine on an API
// provider (agent "nextcode-api": no CLI and no login). The pickers show NextCode once, with no
// engine to choose: NextCode runs on the account of the model picked (accounts.ts); everywhere
// else the catalog's `engine` says which consoles are chats. Pure, so tests can read it.

import type { Agent } from '../lib/types';

export type Engine = 'claude' | 'codex' | 'api';

/** The built-in NextCode agent of each engine. */
export const NEXTCODE_AGENTS: Record<Engine, string> = { claude: 'nextcode', codex: 'nextcode-codex', api: 'nextcode-api' };

export const ENGINES: { value: Engine; label: string }[] = [
  { value: 'claude', label: 'Claude' },
  { value: 'codex', label: 'Codex' },
  { value: 'api', label: 'API' },
];

/**
 * NextCode's efforts, the same on every account (One NextCode): the hub fits one to what the
 * model takes when it starts it (a Codex model with no max runs at its highest).
 */
export const NEXTCODE_EFFORTS: readonly string[] = ['low', 'medium', 'high', 'xhigh', 'max'];
/** What the API engine takes as an effort: NextCode's own. */
export const API_EFFORTS = NEXTCODE_EFFORTS;

/** NextCode's own commands, carried out by NextCode on every engine (never the CLI's). */
export const NEXTCODE_COMMANDS: readonly string[] = ['model', 'effort', 'mode'];

/** The composer's commands: NextCode's own first, then the CLI's other ones. */
export function withNextCodeCommands(cli: readonly string[]): string[] {
  return [...NEXTCODE_COMMANDS, ...cli.filter((c) => !NEXTCODE_COMMANDS.includes(c))];
}

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

/**
 * The list with NextCode once, in the place of the first of its agents: the one chosen
 * (`selected`), else the one of `prefer`'s engine when it can run here, else one that can. Which
 * of them it is matters little: NextCode starts on Settings › NextCode's account, whichever engine
 * that is (docs/API-agents.md §30).
 */
export function foldEngines<T extends AgentLike>(agents: T[], selected?: string, prefer: Engine = 'claude'): T[] {
  const builtins = agents.filter((a) => isNextCodeBuiltin(a.id));
  if (builtins.length < 2) return agents;
  const usable = (a: T) => a.available !== false;
  const keep = builtins.find((a) => a.id === selected) ?? builtins.find((a) => a.id === NEXTCODE_AGENTS[prefer] && usable(a)) ?? builtins.find(usable) ?? builtins[0];
  const first = builtins[0];
  return agents.flatMap((a) => (!isNextCodeBuiltin(a.id) ? [a] : a === first ? [keep] : []));
}

/**
 * NextCode picks what runs it (One NextCode, docs/API-agents.md §30): a form that starts one names
 * no login, provider, model or effort, and the hub starts it on Settings › NextCode's account.
 * Its chat picks another model, of any account. A custom agent built on an engine is not NextCode.
 */
export const picksOwnAccount = (a: Pick<Agent, 'id'> | undefined) => isNextCodeBuiltin(a?.id);

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
