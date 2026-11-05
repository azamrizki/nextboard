// Orchestration actions shared by the command bar, node menus, group frames, cards and
// the Mission Map. Errors become toasts that say what to do next (DESIGN.md §5).

import { api, ApiError, errorMessage } from '../api';
import { toast } from '../components/Toast';
import { CLIENT_ID } from '../lib/ids';
import type { Session } from '../lib/types';
import { store } from '../store';
import { orchApi } from './api';
import { is404, orchStore } from './store';
import type { CommandResponse, Confirmation } from './types';

const who = (s: Session) => (s.handle ? `@${s.handle}` : `“${s.name}”`);
const list = (ss: Session[]) => (ss.length === 1 ? who(ss[0]) : `${ss.length} consoles`);

const CODE_TEXT: Record<string, string> = {
  empty_text: 'Type an order first.',
  text_too_long: 'That is too long: orders take up to 4000 characters.',
  thread_busy: 'Earlier orders in this thread are still being planned. Wait a moment, then send again.',
  queue_full: 'That console already has 20 messages waiting. Cancel some from its queue badge.',
  not_agent: 'That console is not running an agent, so nothing was sent.',
  agent_this_pc_only: 'Agents run on This PC. A server only gets a plain shell.',
  target_busy: 'The agent is busy right now.',
  already_sent: 'That text is already in the agent’s input.',
  nothing_to_resend: 'That order was already sent again.',
  already_decided: 'Someone already answered this.',
  expired: 'That question expired.',
  machine_offline: 'The machine is offline. Reconnect it from Machines, then try again.',
  handoff_running: 'A handoff from this console is already running.',
  bad_loop: 'Check the loop: every 1 minute to 24 hours, a five-field cron line, output text of 2+ characters, 1–500 runs, a time limit of 15 minutes to 24 hours.',
  too_many_loops: 'This hub already runs 50 loops. Delete some first.',
  too_many: 'Too many consoles: up to 12 at once and 64 per board.',
  name_conflict: 'That name is taken by another group or a console handle.',
  member_conflict: 'A console can be in only one group, and a group holds at most 12.',
  forward_cycle: 'That forward would make results loop back.',
  layout_builtin: 'Built-in layouts can’t be renamed or deleted.',
  layout_not_found: 'That layout no longer exists.',
  memory_too_big: 'The memory is limited to 64 KB.',
  memory_empty: 'The memory is empty: write or scan some facts first.',
  runner_unsupported: 'Scans run with Claude Code or Codex only.',
  unknown_agent: 'That agent is not installed there.',
  group_not_found: 'That group no longer exists.',
  loop_not_found: 'That loop no longer exists.',
  pid_unknown: 'NextBoard cannot find the agent’s process to pause it. Restart the console to pause it later.',
  signal_refused: 'The process could not be paused safely.',
};

/** Plain-English message for an orchestration error (hub codes first). */
export function orchError(err: unknown): string {
  if (is404(err)) return UNSUPPORTED;
  if (err instanceof ApiError && err.code && CODE_TEXT[err.code]) return CODE_TEXT[err.code];
  return errorMessage(err);
}

export const UNSUPPORTED = 'This hub does not support orchestration yet. Update NextBoard on the hub to use orders, groups and loops.';

function explain(err: unknown, s?: Session): string {
  if (is404(err)) return UNSUPPORTED;
  if (err instanceof ApiError && err.code === 'not_agent' && !s) return orchError(err);
  if (err instanceof ApiError) {
    switch (err.code) {
      case 'not_agent':
        return s ? `${who(s)} is not running an agent right now, so there is nothing to pause.` : 'That console is not running an agent.';
      case 'machine_offline':
        return s ? `${s.machineName} is offline. Reconnect it from Machines, then try again.` : 'The machine is offline.';
      case 'pid_unknown':
        return s ? `NextBoard cannot find ${who(s)}’s process on ${s.machineName}. Restart the console to pause it later.` : 'The process is unknown.';
    }
  }
  return orchError(err);
}

/** Agents only: plain shells are never paused or sent orders. */
/**
 * Send a content tile to consoles and say what happened: to `targets` when a wire was
 * just made, otherwise to everything it is wired to.
 */
export async function sendTile(workspaceId: string, nodeId: string, targets?: string[]) {
  try {
    const r = await orchApi.pushTile(workspaceId, nodeId, targets);
    const sent = r.delivered + r.busy;
    if (r.empty) toast.info(targets ? 'Wired. It has nothing to send yet.' : 'Nothing new to send.');
    else if (sent) toast.success(`Sent to ${sent} console${sent === 1 ? '' : 's'}.${r.busy ? ' A busy agent gets it when it finishes.' : ''}`);
    else toast.info('Not sent: no agent can take it right now.');
  } catch (err) {
    toast.error(`Could not send it: ${errorMessage(err)}`);
  }
}

export const isAgentSession = (s: Session) => (s.agentKind ?? 'agent') === 'agent' && s.agent !== 'shell' && s.preset !== 'shell';

export async function pauseSessions(ss: Session[]) {
  const targets = ss.filter((s) => isAgentSession(s) && !s.paused && s.status === 'running');
  if (!targets.length) {
    toast.info('Nothing to pause: no running agent here.');
    return;
  }
  let ok = 0;
  for (const s of targets) {
    try {
      store.upsertSession(await orchApi.pause(s.id));
      ok++;
    } catch (err) {
      toast.error(`Could not pause ${who(s)}: ${explain(err, s)}`);
      if (is404(err)) return;
    }
  }
  if (ok) toast.success(`Paused ${ok === targets.length ? list(targets) : `${ok} of ${targets.length} consoles`}. Their state is kept until you resume.`);
}

export async function resumeSessions(ss: Session[]) {
  const targets = ss.filter((s) => isAgentSession(s) && s.status === 'running');
  if (!targets.length) return;
  let ok = 0;
  for (const s of targets) {
    try {
      store.upsertSession(await orchApi.resume(s.id));
      ok++;
    } catch (err) {
      toast.error(`Could not resume ${who(s)}: ${explain(err, s)}`);
      if (is404(err)) return;
    }
  }
  if (ok) toast.success(`Resumed ${ok === targets.length ? list(targets) : `${ok} of ${targets.length} consoles`}.`);
}

/** Every board of the project, as one action of the command bar. */
const totalSessions = (rs: { sessions: string[] | null }[]) => rs.reduce((t, r) => t + (r.sessions?.length ?? 0), 0);

export async function pauseAll(workspaceIds: string[]) {
  try {
    const n = totalSessions(await Promise.all(workspaceIds.map((id) => orchApi.pauseAll(id))));
    toast.success(n ? `Paused ${n === 1 ? '1 agent' : `${n} agents`}. Resume all to continue.` : 'No running agent to pause.');
  } catch (err) {
    toast.error(`Could not pause the agents: ${explain(err)}`);
  }
}

export async function resumeAll(workspaceIds: string[]) {
  try {
    const n = totalSessions(await Promise.all(workspaceIds.map((id) => orchApi.resumeAll(id))));
    toast.success(n ? `Resumed ${n === 1 ? '1 agent' : `${n} agents`}.` : 'Nothing was paused.');
  } catch (err) {
    toast.error(`Could not resume the agents: ${explain(err)}`);
  }
}

/** Stop the processes; the tiles stay on the board (DONE). */
export async function killSessions(ss: Session[]) {
  // in parallel: the hub may take a few seconds per console to stop it
  await Promise.all(
    ss
      .filter((s) => s.status !== 'exited')
      .map((s) =>
        api.killSession(s.id).then(
          (x) => store.upsertSession(x),
          (err) => toast.error(`Could not kill ${who(s)}: ${errorMessage(err)}`),
        ),
      ),
  );
}

export interface SendOptions {
  /** Id of the thread message this answers. */
  replyTo?: string;
  workspaceId: string;
  cardId?: string | null;
  selection?: string[];
  source?: 'typed' | 'voice';
}

/** POST a command; applies messages / confirmation / uiActions. Throws on error. */
export async function sendCommand(projectId: string, text: string, opts: SendOptions): Promise<CommandResponse> {
  const planner = orchStore.get().planner;
  try {
    const res = await orchApi.command(projectId, {
      text,
      source: opts.source ?? 'typed',
      workspaceId: opts.workspaceId,
      cardId: opts.cardId ?? null,
      clientId: CLIENT_ID,
      selection: opts.selection ?? [],
      planner: planner && !planner.available && planner.reason === 'disabled' ? 'off' : 'auto',
      replyTo: opts.replyTo,
    });
    orchStore.markSupported();
    if (res.messages?.length) orchStore.addMessages(res.threadId, res.messages);
    if (res.confirmation) orchStore.upsertConfirmation(res.confirmation);
    if (res.handledBy === 'pending') orchStore.trackCommand(res.commandId, res.threadId, 'planning');
    orchStore.runUiActions(res.uiActions);
    return res;
  } catch (err) {
    if (is404(err)) orchStore.markUnsupported();
    throw err;
  }
}

export async function decide(c: Confirmation, decision: 'confirm' | 'cancel', choice?: string) {
  try {
    orchStore.upsertConfirmation(await orchApi.decide(c.id, decision, choice));
  } catch (err) {
    if (err instanceof ApiError && (err.code === 'already_decided' || err.status === 409)) {
      toast.info('Someone already answered this on another device.');
      orchStore.upsertConfirmation({ ...c, state: c.state === 'pending' ? 'confirmed' : c.state });
    } else if (err instanceof ApiError && (err.code === 'expired' || err.code === 'confirmation_not_found' || err.status === 410)) {
      toast.info('That question expired. Send the order again if you still want it.');
      orchStore.upsertConfirmation({ ...c, state: 'expired' });
    } else toast.error(`Could not answer: ${orchError(err)}`);
  }
}

/** Pending confirmations of a thread, newest first. */
export function pendingConfirmations(threadId: string): Confirmation[] {
  return Object.values(orchStore.get().confirmations)
    .filter((c) => c.threadId === threadId && c.state === 'pending' && Date.parse(c.expiresAt) > Date.now())
    .sort((a, b) => b.expiresAt.localeCompare(a.expiresAt));
}
