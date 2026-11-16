// Missions (orchestrated teamwork): what the thread shows of one, in words and numbers. Pure, so
// the card, the thread and the orchestrator card header say the same thing.

import type { IconName } from '../components/Icon';
import type { MissionRun, MissionTask, ThreadMessage } from './types';

/** A mission in one of these still holds its thread: at most one per thread. */
export const MISSION_ACTIVE = new Set(['awaiting_approval', 'running', 'waiting_owner', 'paused']);
export const isMissionActive = (m: Pick<MissionRun, 'state'> | null | undefined) => !!m && MISSION_ACTIVE.has(m.state);

const STATE_LABEL: Record<string, string> = {
  awaiting_approval: 'Awaiting approval',
  running: 'Running',
  waiting_owner: 'Waiting for you',
  paused: 'Paused',
  done: 'Done',
  stopped: 'Stopped',
  failed: 'Failed',
};
export const missionStateLabel = (state: string) => STATE_LABEL[state] ?? state.replace(/_/g, ' ');

export type Tone = 'grey' | 'info' | 'success' | 'warning' | 'danger' | 'needs';
const STATE_TONE: Record<string, Tone> = {
  awaiting_approval: 'needs',
  running: 'success',
  waiting_owner: 'needs',
  paused: 'warning',
  done: 'info',
  stopped: 'grey',
  failed: 'danger',
};
export const missionTone = (state: string): Tone => STATE_TONE[state] ?? 'grey';

const TASK_LABEL: Record<string, string> = {
  waiting: 'Waiting',
  sent: 'Sent',
  working: 'Working',
  done: 'Done',
  failed: 'Failed',
  cancelled: 'Cancelled',
};
export const taskStateLabel = (state: string) => TASK_LABEL[state] ?? state;
const TASK_TONE: Record<string, Tone> = { waiting: 'grey', sent: 'info', working: 'success', done: 'info', failed: 'danger', cancelled: 'grey' };
export const taskTone = (state: string): Tone => TASK_TONE[state] ?? 'grey';

export interface MissionProgress {
  done: number;
  /** Tasks that count: a cancelled one is no longer part of the plan. */
  total: number;
  failed: number;
  working: number;
}

export function missionProgress(m: Pick<MissionRun, 'tasks'>): MissionProgress {
  const tasks = (m.tasks ?? []).filter((t) => t.state !== 'cancelled');
  return {
    done: tasks.filter((t) => t.state === 'done').length,
    total: tasks.length,
    failed: tasks.filter((t) => t.state === 'failed').length,
    working: tasks.filter((t) => t.state === 'working' || t.state === 'sent').length,
  };
}

/** "Mission · 3/5": the orchestrator card header while one runs. */
export function missionChip(m: Pick<MissionRun, 'tasks'>): string {
  const p = missionProgress(m);
  return p.total ? `Mission · ${p.done}/${p.total}` : 'Mission';
}

/** A time the hub sends as its zero value (not set yet) is no time. */
export function timeOf(iso: string | null | undefined): number | null {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) || t < Date.UTC(2000, 0, 1) ? null : t;
}

/** "Round 4/30": lead turns since the owner approved, of the limit. */
export function roundsLabel(m: Pick<MissionRun, 'rounds' | 'maxRounds'>): string {
  return m.maxRounds > 0 ? `Round ${m.rounds}/${m.maxRounds}` : `Round ${m.rounds}`;
}

/** What is left of the time limit, in words; empty before approval or once it ended. */
export function timeLeftLabel(m: Pick<MissionRun, 'deadline' | 'state'>, now: number): string {
  const end = timeOf(m.deadline);
  if (end == null || !MISSION_ACTIVE.has(m.state) || m.state === 'awaiting_approval') return '';
  const left = end - now;
  if (left <= 0) return 'Time is up';
  const min = Math.ceil(left / 60_000);
  if (min < 60) return `${min}m left`;
  const h = Math.floor(min / 60);
  return min % 60 ? `${h}h ${min % 60}m left` : `${h}h left`;
}

/**
 * A mission order as the thread shows it: the task alone. The hub frames it for the agent after a
 * "---" line (goal, team, tools), the same for every task and in the mission card already.
 */
export function missionTaskText(text: string): string {
  const i = text.indexOf('\n---\n');
  return i > 0 ? text.slice(0, i).trimEnd() : text;
}

/** Tasks per agent, each agent where its first task is, tasks in plan order. */
export function tasksByAgent(tasks: MissionTask[] | null | undefined): { handle: string; tasks: MissionTask[] }[] {
  const out: { handle: string; tasks: MissionTask[] }[] = [];
  const at = new Map<string, number>();
  for (const t of tasks ?? []) {
    let i = at.get(t.handle);
    if (i === undefined) {
      i = out.length;
      at.set(t.handle, i);
      out.push({ handle: t.handle, tasks: [] });
    }
    out[i].tasks.push(t);
  }
  return out;
}

/** The mission that holds a thread now, if any (the newest, should a stale one linger). */
export function activeMissionOf(missions: Record<string, MissionRun>, threadId: string): MissionRun | undefined {
  let best: MissionRun | undefined;
  for (const id in missions) {
    const m = missions[id];
    if (m.threadId !== threadId || !MISSION_ACTIVE.has(m.state)) continue;
    if (!best || m.createdAt > best.createdAt) best = m;
  }
  return best;
}

// ---- team messages -----------------------------------------------------------------

const TEAM_ICON: Record<string, IconName> = { ask: 'message', reply: 'reply', update: 'send', doc: 'fileText', report: 'flag' };
export const teamIcon = (kind: string): IconName => TEAM_ICON[kind] ?? 'message';
const TEAM_VERB: Record<string, string> = { ask: 'asks', reply: 'answers', update: 'tells', doc: 'updated the brief', report: 'reports to' };
export const teamVerb = (kind: string) => TEAM_VERB[kind] ?? kind;

const isLead = (who: string, lead: string) => /^lead$/i.test(who) || (!!lead && who.toLowerCase() === lead.toLowerCase());

/** How a member is named: the lead by its name, a console as @handle. */
export function teamName(who: string | undefined, lead: string): string {
  if (!who) return '';
  if (isLead(who, lead)) return lead || 'Lead';
  return who.startsWith('@') ? who : `@${who}`;
}

/** "@ana → @budi": who wrote to whom; a note for the whole team, or a brief edit, names no one. */
export function teamRoute(m: Pick<ThreadMessage, 'team' | 'handle'>, lead: string): { from: string; to: string } {
  const t = m.team;
  const from = teamName(t?.from || m.handle, lead);
  if (!t || t.kind === 'doc') return { from, to: '' };
  if (t.to) return { from, to: teamName(t.to, lead) };
  return { from, to: t.kind === 'report' ? lead || 'Lead' : t.kind === 'update' ? 'team' : '' };
}

/** What the thread lists: messages, with runs of team messages folded into one item. */
export type ThreadItem = { kind: 'msg'; m: ThreadMessage } | { kind: 'team'; key: string; msgs: ThreadMessage[] };

/** From this many team messages in a row, the thread folds them (the rest of the thread stays readable). */
export const TEAM_FOLD = 3;

export function threadItems(messages: ThreadMessage[], fold = TEAM_FOLD): ThreadItem[] {
  const out: ThreadItem[] = [];
  let run: ThreadMessage[] = [];
  const flush = () => {
    if (run.length >= fold) out.push({ kind: 'team', key: run[0].id, msgs: run });
    else for (const m of run) out.push({ kind: 'msg', m });
    run = [];
  };
  for (const m of messages) {
    if (m.kind === 'team' && (!run.length || run[0].missionId === m.missionId)) {
      run.push(m);
      continue;
    }
    flush();
    if (m.kind === 'team') run.push(m);
    else out.push({ kind: 'msg', m });
  }
  flush();
  return out;
}

/**
 * The team conversation of a mission for its card: every team message in time order, an answer
 * right under the question it answers (one level deep: an answer is never a question).
 */
export function teamConversation(messages: ThreadMessage[], missionId: string): { m: ThreadMessage; answer: boolean }[] {
  const team = messages.filter((m) => m.kind === 'team' && m.missionId === missionId);
  const asks = new Map<string, number>();
  const out: { m: ThreadMessage; answer: boolean }[] = [];
  const answers = new Map<number, ThreadMessage[]>();
  for (const m of team) {
    const q = m.team?.questionId;
    if (m.team?.kind === 'reply' && q && asks.has(q)) {
      const i = asks.get(q)!;
      answers.set(i, [...(answers.get(i) ?? []), m]);
      continue;
    }
    if (m.team?.kind === 'ask' && q) asks.set(q, out.length);
    out.push({ m, answer: false });
  }
  if (!answers.size) return out;
  const nested: { m: ThreadMessage; answer: boolean }[] = [];
  out.forEach((row, i) => {
    nested.push(row);
    for (const a of answers.get(i) ?? []) nested.push({ m: a, answer: true });
  });
  return nested;
}

/** Questions asked in the mission nobody has answered yet. */
export function openQuestions(messages: ThreadMessage[], missionId: string): number {
  const asked = new Set<string>();
  const answered = new Set<string>();
  for (const m of messages) {
    if (m.kind !== 'team' || m.missionId !== missionId || !m.team?.questionId) continue;
    if (m.team.kind === 'ask') asked.add(m.team.questionId);
    else if (m.team.kind === 'reply') answered.add(m.team.questionId);
  }
  let n = 0;
  for (const q of asked) if (!answered.has(q)) n++;
  return n;
}

/**
 * Which state message of a mission is its final summary: the last one of a mission that is done,
 * or one whose text carries the mission's result.
 */
export function isMissionSummary(m: ThreadMessage, mission: MissionRun | undefined, lastOfMission: boolean): boolean {
  if (!mission || mission.state !== 'done') return false;
  const result = (mission.result ?? '').trim();
  if (result && m.text.includes(result.slice(0, 160))) return true;
  return lastOfMission && !!timeOf(mission.endedAt) && Date.parse(m.at) >= (timeOf(mission.endedAt) ?? 0) - 5000;
}

/** The first and the last state message of each mission in a thread (its card, its summary). */
export function missionMarks(messages: ThreadMessage[]): { first: Set<string>; last: Set<string> } {
  const first = new Map<string, string>();
  const last = new Map<string, string>();
  for (const m of messages) {
    if (m.kind !== 'mission' || !m.missionId) continue;
    if (!first.has(m.missionId)) first.set(m.missionId, m.id);
    last.set(m.missionId, m.id);
  }
  return { first: new Set(first.values()), last: new Set(last.values()) };
}
