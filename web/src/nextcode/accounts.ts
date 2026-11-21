// NextCode's accounts (docs/API-agents.md §30, One NextCode): what runs NextCode, as the owner adds
// them in Settings › NextCode. A login of its own (pf_…: a Claude login runs Claude Code, a ChatGPT
// login runs Codex) or an API provider (ap_…: NextCode's own engine). The chat's model menu lists
// the models of every account, and picking one of another account moves the chat there. Pure, so
// tests can read it.

import type { Agent, Profile, Settings } from '../lib/types';
import type { ApiProvider } from '../providers/providers';
import { NEXTCODE_AGENTS, type Engine } from './engines';

export interface NcAccount {
  /** The login profile (pf_…) or API provider (ap_…). */
  id: string;
  kind: 'login' | 'provider';
  engine: Engine;
  /** NextCode's agent that runs it. */
  agentId: string;
  /** "Claude", "Codex", "Claude · Work", a provider's name. */
  name: string;
  /** What it is, in a few words. */
  detail: string;
  /** A login whose profile folder is gone, or a removed provider: kept in the list, cannot run. */
  missing?: boolean;
}

/** The engines' names, as the owner knows the account. */
const LOGIN_NAME: Record<string, string> = { claude: 'Claude', codex: 'Codex' };
const LOGIN_DETAIL: Record<string, string> = { claude: 'Claude account, through Claude Code', codex: 'ChatGPT account, through Codex' };

const ENGINE_OF: Record<string, Engine> = Object.fromEntries(Object.entries(NEXTCODE_AGENTS).map(([e, id]) => [id, e as Engine]));

export interface AccountSources {
  settings: Pick<Settings, 'agents' | 'nextcode'>;
  profiles: Profile[];
  providers: Pick<ApiProvider, 'id' | 'name' | 'kind' | 'api'>[];
  /** The catalog, for whether an engine's CLI is installed (an engine with none is left out by default). */
  agents?: Pick<Agent, 'id' | 'available'>[];
}

/** NextCode's login on an engine by default: the one Settings › Profiles makes the default, else its home folder. */
function defaultLogin(src: AccountSources, agentId: string): Profile | undefined {
  const mine = src.profiles.filter((p) => p.agent === agentId && p.machineId === 'local');
  const id = src.settings.agents.profiles?.local?.[agentId];
  return mine.find((p) => p.id === id) ?? mine.find((p) => p.home);
}

/**
 * The account ids NextCode offers: Settings › NextCode's list, else (never chosen) its default
 * Claude login, its default Codex login where Codex is installed, and the default API provider.
 */
export function accountIds(src: AccountSources): string[] {
  const chosen = src.settings.nextcode?.accounts;
  if (chosen) return chosen;
  const ids: string[] = [];
  for (const e of ['claude', 'codex'] as const) {
    const agentId = NEXTCODE_AGENTS[e];
    // an engine whose CLI is not installed is left out (while the catalog loads, none is)
    if (src.agents?.find((a) => a.id === agentId)?.available === false) continue;
    const p = defaultLogin(src, agentId);
    if (p) ids.push(p.id);
  }
  const prov = src.settings.nextcode?.provider;
  if (prov && src.providers.some((p) => p.id === prov)) ids.push(prov);
  return ids;
}

/** One account by its id, as the menu and Settings show it; null when it is not NextCode's. */
export function accountOf(id: string, src: AccountSources, all: string[] = accountIds(src)): NcAccount | null {
  if (id.startsWith('ap_')) {
    const p = src.providers.find((x) => x.id === id);
    const api = p ? (p.kind === 'anthropic' ? 'Anthropic API' : p.api === 'responses' ? 'Responses API' : 'OpenAI-compatible API') : '';
    return { id, kind: 'provider', engine: 'api', agentId: NEXTCODE_AGENTS.api, name: p?.name ?? 'A removed provider', detail: p ? `API provider · ${api}` : 'Removed from Settings › API providers', ...(p ? {} : { missing: true }) };
  }
  const p = src.profiles.find((x) => x.id === id);
  const engine = p ? ENGINE_OF[p.agent] : undefined;
  if (!p || !engine || engine === 'api') return null;
  // two logins of one engine are told apart by their names
  const twin = all.some((other) => other !== id && src.profiles.find((x) => x.id === other)?.agent === p.agent);
  const name = twin || !p.home ? `${LOGIN_NAME[engine]} · ${p.name}` : LOGIN_NAME[engine];
  return { id, kind: 'login', engine, agentId: p.agent, name, detail: LOGIN_DETAIL[engine], ...(p.exists === false ? { missing: true } : {}) };
}

/** NextCode's accounts, in its menu's order. */
export function ncAccounts(src: AccountSources): NcAccount[] {
  const ids = accountIds(src);
  return ids.flatMap((id) => {
    const a = accountOf(id, src, ids);
    return a ? [a] : [];
  });
}

/** The account a console runs on: its provider, else its login, else its agent's default login. */
export function sessionAccount(s: { providerId?: string; profileId?: string; agent?: string; preset?: string } | undefined, src: AccountSources): string {
  if (!s) return '';
  if (s.providerId) return s.providerId;
  if (s.profileId) return s.profileId;
  return defaultLogin(src, s.agent || s.preset || '')?.id ?? '';
}

/**
 * The accounts the chat's menu lists: NextCode's, with the console's own first when it is not
 * among them (an account removed from the list since, or one it was started on otherwise).
 */
export function menuAccounts(current: string, src: AccountSources): NcAccount[] {
  const list = ncAccounts(src);
  if (!current || list.some((a) => a.id === current)) return list;
  const own = accountOf(current, src, [...accountIds(src), current]);
  return own ? [own, ...list] : list;
}

/** Logins of an engine NextCode can add: its profiles not in the list yet. */
export function addableLogins(engine: 'claude' | 'codex', src: AccountSources): Profile[] {
  const ids = new Set(accountIds(src));
  return src.profiles.filter((p) => p.agent === NEXTCODE_AGENTS[engine] && p.machineId === 'local' && !ids.has(p.id));
}

/** The default of a new console as Settings › NextCode has it, read for the pickers: account '' = none set. */
export function defaultStart(settings: Pick<Settings, 'nextcode'>): { account: string; model: string; effort: string } {
  const nc = settings.nextcode;
  return { account: nc?.account ?? '', model: nc?.model ?? '', effort: nc?.effort ?? '' };
}
