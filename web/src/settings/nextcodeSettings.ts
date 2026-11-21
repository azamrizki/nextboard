// Settings › NextCode (docs/API-agents.md §30): what a new NextCode console starts with, as the hub
// keeps it in settings.nextcode: the accounts it runs on and the one a new console starts on
// (nextcode/accounts.ts), the permission mode and the idle stop.

import type { NextCodePermissionMode, Settings } from '../lib/types';

/** The built-in NextCode agent. */
export const NEXTCODE_ID = 'nextcode';

/** What the hub takes for nextcode.idleExitMin. */
export const IDLE_LIMITS = { min: 1, max: 1440 } as const;

export const NEXTCODE_DEFAULTS: { permissionMode: NextCodePermissionMode; idleExitMin: number } = { permissionMode: 'default', idleExitMin: 30 };

/** What Auto means on NextCode's API engine, where there is no Claude classifier. */
export const API_AUTO = 'Auto on the API engine: edits in the project go ahead, read-only commands run, and other commands ask.';

export const PERMISSION_MODES: { value: NextCodePermissionMode; label: string; title: string }[] = [
  { value: 'default', label: 'Ask', title: 'Asks before it edits a file or runs a command' },
  { value: 'acceptEdits', label: 'Accept edits', title: 'Edits files without asking, and asks before commands' },
  { value: 'plan', label: 'Plan', title: 'Reads and plans only, and asks you to approve the plan before it changes anything' },
  {
    value: 'auto',
    label: 'Auto',
    title:
      'Claude checks each action and runs the safe ones without asking. Needs a Claude login: through an API provider every action stops. On the API engine: edits in the project go ahead, read-only commands run, other commands ask',
  },
  { value: 'bypassPermissions', label: 'Bypass', title: 'Never asks: every edit and command runs right away' },
];

/** settings.nextcode with the hub's defaults for what is missing or not valid (an older hub has none). */
export function nextcodeOf(s: Pick<Settings, 'nextcode'>): { permissionMode: NextCodePermissionMode; idleExitMin: number } {
  const n = s.nextcode;
  const permissionMode = n && PERMISSION_MODES.some((m) => m.value === n.permissionMode) ? n.permissionMode : NEXTCODE_DEFAULTS.permissionMode;
  const idleExitMin = n && Number.isInteger(n.idleExitMin) && n.idleExitMin >= IDLE_LIMITS.min && n.idleExitMin <= IDLE_LIMITS.max ? n.idleExitMin : NEXTCODE_DEFAULTS.idleExitMin;
  return { permissionMode, idleExitMin };
}

/** The API provider a console of NextCode's API engine runs on when none is picked (settings.nextcode.provider); '' for none. */
export function nextcodeProvider(s: Pick<Settings, 'nextcode'> | null | undefined): string {
  const p = s?.nextcode?.provider;
  return typeof p === 'string' ? p : '';
}

/** The first lines of Settings › NextCode. */
export function nextcodeIntro(): string {
  return 'NextCode is one agent: add the accounts it may run on (your Claude or ChatGPT login, or an API provider), and pick a model of any of them in its chat. An account is only its way to the model: nothing of its CLI or the account comes along (settings, connectors, skills, plugins, instructions files). NextCode’s prompt, skills, memory and MCP servers are its own, below, the same on every account.';
}

/** The idle minutes typed, as the hub takes them: whole, 1–1440. null when it is not a number. */
export function idleMinutes(draft: string): number | null {
  const t = draft.trim();
  if (!t) return null;
  const n = Math.round(Number(t));
  if (!Number.isFinite(n)) return null;
  return Math.max(IDLE_LIMITS.min, Math.min(IDLE_LIMITS.max, n));
}
