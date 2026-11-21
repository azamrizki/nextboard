// The setup wizard's logic (design §5), kept apart from the page so it can be tested: which steps
// an instance goes through, what the copy step sends, and what the summary shows.
//
// A default instance with no other instance on the machine keeps today's first run: the admin,
// then the agent CLIs found. Every other instance also chooses how its agents sign in, may copy
// from a sibling, and ends on a summary of where it keeps everything.

import type { Hub } from '../lib/types';

export type StepId = 'claim' | 'signin' | 'copy' | 'agents' | 'summary';

export const STEP_LABELS: Record<StepId, string> = {
  claim: 'Admin',
  signin: 'Sign-in',
  copy: 'Copy',
  agents: 'Agents',
  summary: 'Summary',
};

export interface StepContext {
  /** The instance is the default one (~/.nextboard). */
  isDefault: boolean;
  /** Other instances a copy can come from; null until known (they are listed after the login). */
  sources: number | null;
  /** The setup link named an instance to copy from (?from=), so there is one. */
  fromHint?: boolean;
}

/** The steps, in order. */
export function wizardSteps(c: StepContext): StepId[] {
  const sources = c.sources ?? (c.fromHint ? 1 : 0);
  if (c.isDefault && sources === 0) return ['claim', 'agents'];
  const out: StepId[] = ['claim', 'signin'];
  if (sources > 0) out.push('copy');
  out.push('agents', 'summary');
  return out;
}

/** The step after cur, or null when cur is the last one (or not a step any more). */
export function nextStep(steps: StepId[], cur: StepId): StepId | null {
  const i = steps.indexOf(cur);
  return i >= 0 && i + 1 < steps.length ? steps[i + 1] : null;
}

// ---- step 2: how this instance's agents sign in ------------------------------------------------

export type SignInMode = 'separate' | 'own' | 'provider';

/** The agents that have a login folder of their own (CLAUDE_CONFIG_DIR, CODEX_HOME). */
export const LOGIN_AGENTS = ['claude', 'codex'] as const;

export interface HomeProfile {
  agent: string;
  id: string;
}

/**
 * Agents whose login is always NextBoard's own: NextCode (docs/API-agents.md §30) has no CLI you
 * run yourself, and with no profile it would use Claude Code's ~/.claude (its Codex engine
 * Codex's ~/.codex) and its memory.
 */
export const NEXTBOARD_ONLY_LOGINS: readonly string[] = ['nextcode', 'nextcode-codex'];

/**
 * The settings patch a choice makes. Separate logins: each agent's NextBoard login profile in
 * this instance's data folder is its default again. Own logins: no profile, so the CLI uses
 * ~/.claude or ~/.codex as in a terminal; NextCode keeps its own. A provider keeps the separate
 * logins for consoles and, when asked, runs the lead and the other hidden runs on it.
 */
export function signInPatch(mode: SignInMode, homes: HomeProfile[], providerForRuns?: string): Record<string, unknown> | null {
  const local: Record<string, string> = {};
  if (mode === 'own') {
    const agents = new Set<string>([...LOGIN_AGENTS, ...homes.map((h) => h.agent)]);
    for (const a of agents) local[a] = (NEXTBOARD_ONLY_LOGINS.includes(a) && homes.find((h) => h.agent === a)?.id) || '';
  } else {
    for (const h of homes) local[h.agent] = h.id;
  }
  const patch: Record<string, unknown> = {};
  if (Object.keys(local).length > 0) patch.agents = { profiles: { local } };
  if (mode === 'provider' && providerForRuns) patch.runner = { provider: providerForRuns };
  return Object.keys(patch).length > 0 ? patch : null;
}

// ---- step 3: copy from another instance -----------------------------------------------------------

/** What POST /api/instances/import copies. A secret goes only with what it belongs to. */
export interface CopyChoices {
  settings: boolean;
  /** Custom agents (presets.json) and saved layouts. */
  presets: boolean;
  mcp: boolean;
  mcpSecrets: boolean;
  machines: boolean;
  machineCredentials: boolean;
  providers: boolean;
  providerKeys: boolean;
  vault: boolean;
}

export type CopyKey = keyof CopyChoices;

/** Settings, presets and MCP definitions on; everything that holds a secret off. */
export const DEFAULT_COPY: CopyChoices = {
  settings: true,
  presets: true,
  mcp: true,
  mcpSecrets: false,
  machines: false,
  machineCredentials: false,
  providers: false,
  providerKeys: false,
  vault: false,
};

/** Each secret and what it belongs to. */
export const SECRET_OF: Partial<Record<CopyKey, CopyKey>> = { mcpSecrets: 'mcp', machineCredentials: 'machines', providerKeys: 'providers' };

/** Ticks or unticks one box: a secret ticks what it belongs to, and unticking that unticks it. */
export function setChoice(c: CopyChoices, key: CopyKey, on: boolean): CopyChoices {
  const next = { ...c, [key]: on };
  const parent = SECRET_OF[key];
  if (on && parent) next[parent] = true;
  if (!on) {
    for (const [secret, of] of Object.entries(SECRET_OF) as [CopyKey, CopyKey][]) {
      if (of === key) next[secret] = false;
    }
  }
  return next;
}

export function anythingChosen(c: CopyChoices): boolean {
  return c.settings || c.presets || c.mcp || c.machines || c.providers || c.vault;
}

/** The secrets that would leave the other instance, by name, for the line under the boxes. */
export function secretsChosen(c: CopyChoices): string[] {
  const out: string[] = [];
  if (c.providers && c.providerKeys) out.push('API keys');
  if (c.machines && c.machineCredentials) out.push('SSH credentials');
  if (c.mcp && c.mcpSecrets) out.push('MCP header and environment values');
  if (c.vault) out.push('the vault (as ciphertext)');
  return out;
}

export interface ImportBody extends CopyChoices {
  from: string;
  password: string;
}

/** The body of POST /api/instances/import: secrets without what they belong to are dropped. */
export function importBody(from: string, password: string, c: CopyChoices): ImportBody {
  return {
    from,
    password,
    ...c,
    mcpSecrets: c.mcp && c.mcpSecrets,
    machineCredentials: c.machines && c.machineCredentials,
    providerKeys: c.providers && c.providerKeys,
  };
}

export interface ImportCopied {
  settings: boolean;
  agents: number;
  layouts: number;
  mcp: number;
  machines: number;
  providers: number;
  vault: boolean;
  vaultItems: number;
  secretsTaken: boolean;
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** One line for what came over: "settings, 2 custom agents and the vault (3 items)". */
export function copiedSummary(c: ImportCopied): string {
  const parts: string[] = [];
  if (c.settings) parts.push('settings');
  if (c.agents) parts.push(plural(c.agents, 'custom agent'));
  if (c.layouts) parts.push(plural(c.layouts, 'layout'));
  if (c.mcp) parts.push(plural(c.mcp, 'MCP server'));
  if (c.machines) parts.push(plural(c.machines, 'SSH machine'));
  if (c.providers) parts.push(plural(c.providers, 'API provider'));
  if (c.vault) parts.push(`the vault (${plural(c.vaultItems, 'item')})`);
  if (parts.length === 0) return 'nothing new';
  return parts.length === 1 ? parts[0] : `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}`;
}

/** The pick of the copy step: the ?from= of the link when it is a source, else the only one. */
export function preselectSource(sources: { name: string; setUp: boolean }[], from: string): string {
  const ready = sources.filter((s) => s.setUp);
  if (from && ready.some((s) => s.name === from)) return from;
  return ready.length === 1 ? ready[0].name : '';
}

// ---- step 5: the summary ---------------------------------------------------------------------------

/** /api/hub with the instance fields (design §3); every one may be missing on an older hub. */
export interface WizardHub extends Hub {
  instance?: { name: string; default: boolean };
  hubId?: string;
  dataDir?: string;
  configFile?: string;
  service?: { kind: 'systemd' | 'launchd' | 'none' | string; name?: string; unitPath?: string; managed?: boolean };
  statusListen?: string;
}

export interface Here {
  protocol: string;
  host: string;
  hostname: string;
}

export interface Address {
  url: string;
  /** The one this browser came in through, or one on the same host name. */
  here: boolean;
}

/** The hub's addresses, the one this browser uses first and marked. */
export function markAddresses(urls: string[], here: Here): Address[] {
  const parsed = urls.map((url) => {
    try {
      return { url, u: new URL(url) };
    } catch {
      return { url, u: null };
    }
  });
  const exact = parsed.findIndex((p) => p.u && p.u.host === here.host && p.u.protocol === here.protocol);
  const near = exact >= 0 ? exact : parsed.findIndex((p) => p.u && p.u.hostname === here.hostname);
  const out = parsed.map((p, i) => ({ url: p.url, here: i === near }));
  if (near > 0) out.unshift(...out.splice(near, 1));
  return out;
}

/** The port of a listen address ("127.0.0.1:12412" → "12412"). */
export function portOf(listen: string): string {
  const m = /:(\d+)$/.exec(listen.trim());
  return m ? m[1] : '';
}

/** Whether a listen address is reachable from other machines (anything but loopback). */
export function beyondLoopback(listen: string): boolean {
  const host = listen.trim().replace(/:\d+$/, '').replace(/^\[|\]$/g, '');
  if (host === '' || host === '0.0.0.0' || host === '::') return true;
  return !(host === 'localhost' || host === '::1' || /^127\./.test(host));
}

/** What the Service row says. */
export function serviceText(s: WizardHub['service']): string {
  if (!s) return 'Unknown';
  if (s.kind === 'none' || !s.name) return 'Not a service: started by hand or by make dev';
  const kind = s.kind === 'launchd' ? 'LaunchAgent' : 'systemd user service';
  return `${s.name} (${kind}${s.managed === false ? ', not the one running this hub' : ''})`;
}

/** Said plainly in the summary (design §6.1). */
export const ISOLATION_NOTE =
  'Instances of one OS user keep their data, logins, projects and sessions apart, but they are not a confidentiality boundary: an agent of one instance can read the data folder of another. For real secrecy run the instance under a separate OS user.';
