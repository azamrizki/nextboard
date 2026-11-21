// Words and numbers of the NextCode chat: the permission modes, the usage line, the live line and
// the hint under a failed sign-in (or, on an API provider, a refused key). Pure, so tests can read them.

import type { AgentState } from '../lib/types';
import type { DotColor } from '../components/StatusDot';
import { inputOf } from './tools';
import type { PermissionMode, Status } from './types';

export const MODES: { value: PermissionMode; label: string; short: string; title: string }[] = [
  { value: 'default', label: 'Ask', short: 'Ask', title: 'Asks before it edits files or runs commands outside your rules' },
  { value: 'acceptEdits', label: 'Accept edits', short: 'Edits', title: 'Edits files without asking; still asks before other tools' },
  { value: 'plan', label: 'Plan', short: 'Plan', title: 'Reads and plans only; you approve the plan before it changes anything' },
  { value: 'auto', label: 'Auto', short: 'Auto', title: 'Decides by itself which calls are safe to make without asking' },
  { value: 'bypassPermissions', label: 'Bypass', short: 'Bypass', title: 'Never asks: every tool call goes ahead. Only in a sandbox you trust.' },
];

/** What Auto does on NextCode's API engine: no Claude classifier, a fixed rule. */
const API_AUTO_TITLE = 'Edits in the project go ahead, read-only commands run, and other commands ask';

/** The modes as an engine has them: on the API engine Auto says what it does there. */
export function modesFor(engine: string | undefined): typeof MODES {
  if (engine !== 'api') return MODES;
  return MODES.map((m) => (m.value === 'auto' ? { ...m, title: API_AUTO_TITLE } : m));
}

export function modeOf(status: Status | null): PermissionMode {
  const m = status?.permissionMode;
  return MODES.some((x) => x.value === m) ? (m as PermissionMode) : 'default';
}

export const modeLabel = (m: PermissionMode) => MODES.find((x) => x.value === m)?.label ?? 'Ask';

/** "$0.42"; nothing before the first turn costs anything. */
export function costText(usd: number | undefined): string {
  if (!usd || usd <= 0) return '';
  return usd < 0.01 ? '< $0.01' : `$${usd.toFixed(2)}`;
}

const str = (v: unknown) => (typeof v === 'string' ? v : '');

function clock(d: Date): string {
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/**
 * The CLI's rate limit report as a few words: "limit 23% used", "near the limit", "limit
 * reached · resets 14:00". The report is Claude Code's rate_limit_info, as it gives it.
 */
export function rateLimitText(raw: unknown, engine?: string): string {
  // the API engine has no subscription to be limited by: a provider's limits come as errors
  if (engine === 'api') return '';
  // Codex: {rateLimits: {primary: {usedPercent, resetsAt}, secondary, …}}; the fuller window
  const codex = inputOf(inputOf(raw).rateLimits);
  if (Object.keys(codex).length) {
    const windows = [inputOf(codex.primary), inputOf(codex.secondary)].filter((w) => typeof w.usedPercent === 'number' && Number.isFinite(w.usedPercent));
    if (!windows.length) return '';
    const w = windows.reduce((a, b) => ((b.usedPercent as number) > (a.usedPercent as number) ? b : a));
    const pct = Math.round(w.usedPercent as number);
    const at = typeof w.resetsAt === 'number' ? new Date(w.resetsAt < 1e12 ? w.resetsAt * 1000 : w.resetsAt) : null;
    return pct >= 100 ? `limit reached${at ? ` · resets ${clock(at)}` : ''}` : `limit ${pct}% used`;
  }
  const o = inputOf(raw);
  const status = str(o.status);
  const at = typeof o.resetsAt === 'number' ? new Date(o.resetsAt < 1e12 ? o.resetsAt * 1000 : o.resetsAt) : null;
  if (status === 'rejected') return `limit reached${at ? ` · resets ${clock(at)}` : ''}`;
  if (typeof o.utilization === 'number' && Number.isFinite(o.utilization)) {
    const pct = Math.round(o.utilization <= 1 ? o.utilization * 100 : o.utilization);
    return `limit ${pct}% used`;
  }
  if (status === 'allowed_warning') return 'near the limit';
  return '';
}

/** "0:41", "12:05", "1:02:09". */
export function elapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const pad = (n: number) => String(n).padStart(2, '0');
  return h ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
}

export interface Live {
  dot: DotColor;
  text: string;
  /** Count the time of the turn (working or waiting on you). */
  timed: boolean;
}

/**
 * The line over the composer: the console's state as the board has it (agentState, activity),
 * with what only the chat knows (stopping, a retry, a CLI at rest).
 */
export function liveLine(agentState: AgentState | undefined, activity: string | undefined, status: Status | null, connected: boolean): Live {
  const act = (activity ?? '').trim();
  const note = status?.note ? ` · ${status.note}` : '';
  if (!connected) return { dot: 'warning', text: 'Connecting to the chat…', timed: false };
  if (status?.stopping) return { dot: 'warning', text: 'Stopping…', timed: true };
  switch (agentState) {
    case 'needsYou':
      return { dot: 'needs', text: `Needs you${act ? ` · ${act}` : ''}`, timed: true };
    case 'working':
      return { dot: 'busy', text: `Working · ${act || 'Thinking'}${note}`, timed: true };
    case 'starting':
      return { dot: 'warning', text: 'Starting…', timed: false };
    case 'error':
      return { dot: 'danger', text: 'The console failed', timed: false };
    case 'exited':
      return { dot: 'grey', text: 'This console has ended', timed: false };
    default:
      break;
  }
  if (status?.working) return { dot: 'busy', text: `Working${note}`, timed: true };
  if (status?.process === 'stopped' && status.conversation) return { dot: 'grey', text: 'Ready · resting to free memory; your next message wakes it', timed: false };
  return { dot: 'grey', text: `Ready${note}`, timed: false };
}

/** An error that means Claude Code has no login for this profile (or, on an API provider, that its key was refused). */
export function isLoginError(text: string): boolean {
  return /not logged in|please run \/login|invalid api key|authentication[_ ]error|oauth token|401\b/i.test(text);
}

/** What the agent gives back in a turn that worked: its reply, its thinking or a tool call. */
const REPLY_KINDS = new Set(['text', 'thinking', 'tool']);

/**
 * The main conversation without the sign-in failures that are over: once the agent replied after
 * one (signed in since, or the key fixed), its card has nothing left to say. Others stay.
 */
export function withoutSettledLogins<T extends { kind: string; text?: string }>(top: T[]): T[] {
  let replied = false;
  let drop: Set<T> | null = null;
  for (let i = top.length - 1; i >= 0; i--) {
    const it = top[i];
    if (REPLY_KINDS.has(it.kind)) replied = true;
    else if (replied && it.kind === 'error' && isLoginError(it.text ?? '')) (drop ??= new Set()).add(it);
  }
  return drop ? top.filter((it) => !drop.has(it)) : top;
}

/**
 * The message to send again after signing in: the owner's last one, when the chat ends on a
 * sign-in failure that came after it. '' when there is none (the agent replied since, or the
 * failure was not about the login).
 */
export function retryAfterSignIn<T extends { kind: string; text?: string }>(top: T[]): string {
  let failed = false;
  for (let i = top.length - 1; i >= 0; i--) {
    const it = top[i];
    if (it.kind === 'error' && isLoginError(it.text ?? '')) failed = true;
    else if (it.kind === 'user') return failed ? (it.text ?? '') : '';
    else if (it.kind !== 'notice') return '';
  }
  return '';
}

export interface LoginHelp {
  title: string;
  hint: string;
  action: string;
  to: string;
}

/**
 * What a chat says under a failed sign-in, by what the console runs on. A console on an API
 * provider (always, on the API engine) has no login: the provider's key is what was refused.
 */
export function loginHelp(engine: string | undefined, onProvider: boolean): LoginHelp {
  if (engine === 'api' || onProvider)
    return {
      title: 'The API provider refused the key',
      hint: 'This console runs on an API provider, with no login of its own. Check the provider’s key, or pick another provider, in Settings › API providers.',
      action: 'Open API providers',
      to: '/settings/providers',
    };
  const cli = engine === 'codex' ? 'Codex' : 'Claude Code';
  return {
    title: 'Not logged in',
    hint: `NextCode's login for this console is not signed in. It is its own, apart from ${cli}'s; signing in runs ${cli}'s own login with NextCode's folder, and NextBoard never sees the token.`,
    action: 'Sign in NextCode',
    to: '/settings/nextcode',
  };
}

/** Slash commands shown at once. */
const MAX_COMMANDS = 8;

/** The commands that fit what follows "/": those that start with it first, then those that hold it. */
export function matchCommands(commands: string[], typed: string): string[] {
  const q = typed.toLowerCase();
  const starts = commands.filter((c) => c.toLowerCase().startsWith(q));
  const contains = q ? commands.filter((c) => !c.toLowerCase().startsWith(q) && c.toLowerCase().includes(q)) : [];
  return [...starts, ...contains].slice(0, MAX_COMMANDS);
}
