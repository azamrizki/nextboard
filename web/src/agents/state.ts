// Agent state → pill (docs/API-agents.md §6.1, DESIGN.md §3.7). The label is always
// text (colour is never the only signal); the activity goes in the tooltip and, when
// there is room, next to the pill.

import type { DotColor } from '../components/StatusDot';
import { sessionStatus } from '../board/sessionStatus';
import type { SocketState } from '../lib/socket';
import type { AgentState, Session } from '../lib/types';

export type PillTone = 'success' | 'needs' | 'info' | 'grey' | 'warning' | 'danger';

export interface Pill {
  /** Effective state ("legacy" when the hub has no agent states). */
  state: AgentState | 'connecting' | 'legacy' | 'paused';
  /** Short label, rendered uppercase. */
  label: string;
  dot: DotColor;
  tone: PillTone;
  /** Activity text (≤ 120 chars) for working / needs you. */
  activity: string;
  /** Tooltip / aria description. */
  detail: string;
}

const TONE_OF: Record<DotColor, PillTone> = {
  busy: 'success',
  success: 'success',
  warning: 'warning',
  grey: 'grey',
  danger: 'danger',
  info: 'info',
  needs: 'needs',
};

/** True when the hub reports Phase-5 agent states for this session. */
export const hasAgentState = (s: Session) => typeof s.agentState === 'string';

export function agentPill(s: Session, socket: SocketState = 'open'): Pill {
  if (!hasAgentState(s)) {
    const st = sessionStatus(s, socket);
    return { state: 'legacy', label: st.label, dot: st.color, tone: TONE_OF[st.color], activity: '', detail: st.detail ?? st.label };
  }
  const activity = (s.activity ?? '').slice(0, 120);
  const state = s.agentState as AgentState;
  // Phase 6: a paused console keeps its (frozen) state; the pill says PAUSED
  if (s.paused && s.status === 'running')
    return { state: 'paused', label: 'Paused', dot: 'warning', tone: 'warning', activity: '', detail: 'Paused by NextBoard: the process is stopped until you resume it. Orders wait.' };
  switch (state) {
    case 'error':
      return { state, label: 'Error', dot: 'danger', tone: 'danger', activity: '', detail: s.error || activity || 'The console failed to start' };
    case 'reconnecting':
      return { state, label: 'Reconnecting', dot: 'warning', tone: 'warning', activity: '', detail: `Reconnecting to ${s.machineName}` };
    case 'exited': {
      const bad = s.exitCode !== 0;
      return {
        state,
        label: bad ? `Done (${s.exitCode})` : 'Done',
        dot: bad ? 'danger' : 'grey',
        tone: bad ? 'danger' : 'grey',
        activity: '',
        detail: `The console process ended with code ${s.exitCode}. Restart to start it again.`,
      };
    }
    case 'asleep':
      return {
        state,
        label: 'Asleep',
        dot: 'grey',
        tone: 'grey',
        activity: '',
        detail: 'Its process was stopped to free memory. It resumes its conversation when you open it, type into it or send it work.',
      };
    case 'starting':
      return { state, label: 'Starting', dot: 'warning', tone: 'warning', activity: '', detail: 'Launching…' };
    case 'needsYou':
      return { state, label: 'Needs you', dot: 'needs', tone: 'needs', activity, detail: activity ? `Needs you: ${activity}` : 'Waiting for your answer' };
    default:
      break;
  }
  if (socket !== 'open' && s.status === 'running') {
    return { state: 'connecting', label: 'Connecting', dot: 'warning', tone: 'warning', activity: '', detail: 'Connecting to the hub' };
  }
  switch (state) {
    case 'working':
      return { state, label: 'Working', dot: 'busy', tone: 'success', activity, detail: activity ? `Working: ${activity}` : 'Working on a turn' };
    case 'ready':
      return { state, label: 'Idle', dot: 'grey', tone: 'grey', activity: '', detail: 'At its prompt, waiting for an order' };
    case 'shell':
    default:
      if (s.active) {
        return { state: 'shell', label: 'Running', dot: 'busy', tone: 'success', activity: s.job ?? '', detail: s.job ? `Running ${s.job}` : 'Printing output' };
      }
      return {
        state: 'shell',
        label: 'Idle',
        dot: 'grey',
        tone: 'grey',
        activity: '',
        detail: s.agentKind === 'shell' ? 'Shell at its prompt, nothing running' : 'The agent is not running; the console is at the shell prompt',
      };
  }
}

/** Really processing right now. A hub without the field still knows an agent's turn. */
export const isActive = (s: Session) => s.active ?? s.agentState === 'working';

/** Alive and waiting for nothing but a next order: what a monitor can leave out. */
export const isIdle = (s: Session) => !isActive(s) && !s.paused && s.status === 'running' && (s.agentState === 'ready' || s.agentState === 'shell');

export const isAgentConsole = (s: Session) => (s.agentKind ?? 'agent') === 'agent' && s.preset !== 'shell' && s.agent !== 'shell';

/** "See output" shows when a finished reply is long enough and the agent isn't mid-turn (§8). */
export function hasOutput(s: Session): boolean {
  const r = s.reply;
  if (!r) return false;
  if (s.agentState === 'working') return false;
  return r.chars >= 80 || r.paragraphs >= 3;
}

/** Display handle ("@Ash"), falling back to the session name on Phase-4 hubs. */
export function handleOf(s: Session): string {
  return s.handle ? `@${s.handle}` : s.name;
}

export const HANDLE_RE = /^[A-Za-z][A-Za-z0-9-]{0,19}$/;
export const RESERVED_HANDLES = ['everyone', 'all', 'everybody', 'me', 'bar', 'hub', 'orchestrator', 'group', 'loop', 'memory'];

export function handleProblem(h: string): string {
  if (!HANDLE_RE.test(h)) return 'Start with a letter; use up to 20 letters, digits or dashes.';
  if (RESERVED_HANDLES.includes(h.toLowerCase())) return `“${h}” is reserved. Pick another name.`;
  return '';
}

/** "Profile name" → "PN" (header badge). */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return '?';
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[1][0]).toUpperCase();
}
