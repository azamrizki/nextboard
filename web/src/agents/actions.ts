// Agent actions on a console (docs/API-agents.md §3.3): start the agent again from the
// shell, or fully restart the console. Errors become actionable toasts.

import { api, ApiError, errorMessage, isCode, type RestartBody } from '../api';
import { toast } from '../components/Toast';
import type { LaunchMode, Session } from '../lib/types';
import { store } from '../store';

export type LaunchResult = 'ok' | 'wrap_missing' | 'failed';

const who = (s: Session) => (s.handle ? `@${s.handle}` : `“${s.name}”`);

/** Exact resume needs an agent session id; otherwise fall back to "latest in folder". */
export function resumeMode(s: Session): { mode: Exclude<LaunchMode, 'shell'>; agentSessionId?: string } {
  return s.agentSessionId ? { mode: 'resume', agentSessionId: s.agentSessionId } : { mode: 'continue' };
}

/**
 * Start the agent in a console. At the shell prompt this types the wrapper command
 * (POST /agent); while the agent runs it is a full restart.
 */
export async function launchAgent(s: Session, mode: Exclude<LaunchMode, 'shell'>, agentSessionId?: string): Promise<LaunchResult> {
  const atShell = s.agentState === 'shell' || s.foreground === 'shell';
  try {
    if (atShell && s.status === 'running') {
      store.upsertSession(await api.agentLaunch(s.id, mode, agentSessionId));
    } else {
      const body: RestartBody = { mode };
      store.upsertSession(await api.restartSession(s.id, body));
    }
    return 'ok';
  } catch (err) {
    if (isCode(err, 'wrap_missing')) return 'wrap_missing';
    toast.error(launchError(s, err));
    return 'failed';
  }
}

/** Full restart (kill + launch); scrollback stays. */
export async function restartConsole(s: Session, body: RestartBody, label = 'Restart'): Promise<boolean> {
  try {
    store.upsertSession(await api.restartSession(s.id, body));
    return true;
  } catch (err) {
    toast.error(`${label} failed for ${who(s)}: ${errorMessage(err)}`);
    return false;
  }
}

export function launchError(s: Session, err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case 'shell_busy':
        return `A program is running in ${who(s)}’s shell. Quit it first, then start the agent again.`;
      case 'agent_running':
        return `The agent is already running in ${who(s)}.`;
      case 'not_agent':
        return `${who(s)} is a plain shell console. Use “Switch agent” to run an agent in it.`;
      case 'machine_offline':
        return `${s.machineName} is offline. Reconnect it from Machines, then try again.`;
    }
  }
  return `Could not start the agent in ${who(s)}: ${errorMessage(err)}`;
}
