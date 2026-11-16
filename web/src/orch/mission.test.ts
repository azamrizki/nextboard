import { describe, expect, it } from 'vitest';
import {
  activeMissionOf,
  isMissionSummary,
  missionChip,
  missionMarks,
  missionProgress,
  missionStateLabel,
  missionTaskText,
  missionTone,
  openQuestions,
  roundsLabel,
  tasksByAgent,
  teamConversation,
  teamRoute,
  threadItems,
  timeLeftLabel,
} from './mission';
import type { MissionRun, MissionTask, ThreadMessage } from './types';

const task = (id: string, handle: string, state: string, extra: Partial<MissionTask> = {}): MissionTask => ({ id, handle, title: `Task ${id}`, text: `Do ${id}`, state, ...extra });

const mission = (over: Partial<MissionRun> = {}): MissionRun => ({
  id: 'ms_one',
  projectId: 'p_demo',
  threadId: 'p_demo',
  workspaceId: 'w_demo',
  lead: 'Nara',
  goal: 'Ship the sign-up page',
  summary: '',
  doneWhen: '',
  doc: '',
  docRev: 0,
  tasks: [],
  state: 'running',
  rounds: 0,
  maxRounds: 30,
  createdAt: '2026-11-16T10:00:00.000Z',
  updatedAt: '2026-11-16T10:00:00.000Z',
  ...over,
});

let n = 0;
const msg = (over: Partial<ThreadMessage>): ThreadMessage => {
  n++;
  return { id: `m${String(n).padStart(4, '0')}`, threadId: 'p_demo', at: `2026-11-16T10:${String(n % 60).padStart(2, '0')}:00.000Z`, role: 'hub', kind: 'note', text: '', ...over };
};
const team = (from: string, to: string | undefined, kind: string, questionId?: string, missionId = 'ms_one') =>
  msg({ kind: 'team', role: 'agent', missionId, text: `${kind} from ${from}`, team: { from, to, kind, questionId } });

describe('mission states in words', () => {
  it('names every state and gives the ones that wait for the owner the attention tone', () => {
    expect(missionStateLabel('awaiting_approval')).toBe('Awaiting approval');
    expect(missionStateLabel('waiting_owner')).toBe('Waiting for you');
    expect(missionStateLabel('paused')).toBe('Paused');
    expect(missionStateLabel('done')).toBe('Done');
    expect(missionStateLabel('something_new')).toBe('something new');
    expect(missionTone('waiting_owner')).toBe('needs');
    expect(missionTone('awaiting_approval')).toBe('needs');
    expect(missionTone('running')).toBe('success');
    expect(missionTone('failed')).toBe('danger');
    expect(missionTone('stopped')).toBe('grey');
  });

  it('counts progress without cancelled tasks, and says it in the card header chip', () => {
    const m = mission({ tasks: [task('t1', 'ana', 'done'), task('t2', 'budi', 'working'), task('t3', 'budi', 'cancelled'), task('t4', 'ana', 'failed'), task('t5', 'citra', 'sent')] });
    expect(missionProgress(m)).toEqual({ done: 1, total: 4, failed: 1, working: 2 });
    expect(missionChip(m)).toBe('Mission · 1/4');
    expect(missionChip(mission({ tasks: null }))).toBe('Mission');
  });

  it('says the rounds used and the time left, and nothing for a time not set', () => {
    expect(roundsLabel(mission({ rounds: 4 }))).toBe('Round 4/30');
    const now = Date.parse('2026-11-16T10:00:00.000Z');
    expect(timeLeftLabel(mission({ deadline: '2026-11-16T13:12:00.000Z' }), now)).toBe('3h 12m left');
    expect(timeLeftLabel(mission({ deadline: '2026-11-16T14:00:00.000Z' }), now)).toBe('4h left');
    expect(timeLeftLabel(mission({ deadline: '2026-11-16T10:44:30.000Z' }), now)).toBe('45m left');
    expect(timeLeftLabel(mission({ deadline: '2026-11-16T09:00:00.000Z' }), now)).toBe('Time is up');
    // the hub's zero time before approval, an approval still open, a mission that ended
    expect(timeLeftLabel(mission({ deadline: '0001-01-01T00:00:00Z' }), now)).toBe('');
    expect(timeLeftLabel(mission({ state: 'awaiting_approval', deadline: '2026-11-16T14:00:00.000Z' }), now)).toBe('');
    expect(timeLeftLabel(mission({ state: 'done', deadline: '2026-11-16T14:00:00.000Z' }), now)).toBe('');
  });

  it('lists tasks per agent in plan order', () => {
    const groups = tasksByAgent([task('t1', 'ana', 'done'), task('t2', 'budi', 'waiting'), task('t3', 'ana', 'waiting')]);
    expect(groups.map((g) => [g.handle, g.tasks.map((t) => t.id)])).toEqual([
      ['ana', ['t1', 't3']],
      ['budi', ['t2']],
    ]);
    expect(tasksByAgent(null)).toEqual([]);
  });

  it('pins the active mission of a thread only', () => {
    const all = {
      a: mission({ id: 'a', state: 'done', createdAt: '2026-11-16T09:00:00Z' }),
      b: mission({ id: 'b', state: 'paused', createdAt: '2026-11-16T10:00:00Z' }),
      c: mission({ id: 'c', state: 'running', threadId: 'n_card', createdAt: '2026-11-16T11:00:00Z' }),
    };
    expect(activeMissionOf(all, 'p_demo')?.id).toBe('b');
    expect(activeMissionOf(all, 'n_card')?.id).toBe('c');
    expect(activeMissionOf(all, 'n_other')).toBeUndefined();
  });
});

describe('mission orders in the thread', () => {
  it('shows the task, not the framing the hub adds for the agent', () => {
    expect(missionTaskText('Write POST /signup.\n\n---\nThis is task t1 of a mission led by Nara.')).toBe('Write POST /signup.');
    expect(missionTaskText('No framing here')).toBe('No framing here');
    expect(missionTaskText('---\nonly a rule')).toBe('---\nonly a rule');
  });
});

describe('team messages', () => {
  it('says who wrote to whom, the lead by its name', () => {
    expect(teamRoute(team('ana', 'budi', 'ask'), 'Nara')).toEqual({ from: '@ana', to: '@budi' });
    expect(teamRoute(team('budi', 'lead', 'ask'), 'Nara')).toEqual({ from: '@budi', to: 'Nara' });
    expect(teamRoute(team('ana', undefined, 'report'), 'Nara')).toEqual({ from: '@ana', to: 'Nara' });
    expect(teamRoute(team('ana', undefined, 'update'), 'Nara')).toEqual({ from: '@ana', to: 'team' });
    expect(teamRoute(team('ana', 'budi', 'doc'), 'Nara')).toEqual({ from: '@ana', to: '' });
  });

  it('folds runs of team messages in the thread, and only runs', () => {
    const order = msg({ kind: 'order', role: 'user', text: 'go' });
    const a = team('ana', 'budi', 'ask', 'q_1');
    const b = team('budi', 'ana', 'reply', 'q_1');
    const c = team('ana', undefined, 'update');
    const report = msg({ kind: 'report', role: 'agent' });
    const d = team('budi', 'ana', 'ask', 'q_2');
    const items = threadItems([order, a, b, c, report, d]);
    expect(items.map((i) => (i.kind === 'team' ? `team:${i.msgs.length}` : i.m.kind))).toEqual(['order', 'team:3', 'report', 'team']);
    // two in a row stay rows
    expect(threadItems([a, b]).map((i) => i.kind)).toEqual(['msg', 'msg']);
    // a run never mixes two missions
    const other = team('citra', 'dewi', 'ask', 'q_9', 'ms_two');
    expect(threadItems([a, b, c, other]).map((i) => (i.kind === 'team' ? `team:${i.msgs.length}` : i.m.missionId))).toEqual(['team:3', 'ms_two']);
  });

  it('puts an answer under its question in the card, and counts the open ones', () => {
    const q1 = team('ana', 'budi', 'ask', 'q_1');
    const q2 = team('budi', 'lead', 'ask', 'q_2');
    const note = team('citra', undefined, 'update');
    const a1 = team('budi', 'ana', 'reply', 'q_1');
    const elsewhere = team('x', 'y', 'ask', 'q_3', 'ms_two');
    const thread = [q1, q2, note, a1, elsewhere];
    expect(teamConversation(thread, 'ms_one').map((r) => [r.m.id, r.answer])).toEqual([
      [q1.id, false],
      [a1.id, true],
      [q2.id, false],
      [note.id, false],
    ]);
    expect(openQuestions(thread, 'ms_one')).toBe(1);
    expect(openQuestions(thread, 'ms_two')).toBe(1);
  });
});

describe('mission state messages', () => {
  it('knows the first one (the card) and the last one of each mission', () => {
    const start = msg({ kind: 'mission', missionId: 'ms_one', text: 'Mission proposed' });
    const other = msg({ kind: 'mission', missionId: 'ms_two', text: 'Mission proposed' });
    const end = msg({ kind: 'mission', missionId: 'ms_one', text: 'Done' });
    const marks = missionMarks([start, msg({ kind: 'note' }), other, end]);
    expect([...marks.first].sort()).toEqual([start.id, other.id].sort());
    expect([...marks.last].sort()).toEqual([other.id, end.id].sort());
  });

  it('shows the summary of a done mission prominently, and only that', () => {
    const done = mission({ state: 'done', result: '## Built\nThe sign-up page and its API.', endedAt: '2026-11-16T11:00:00.000Z' });
    const summary = msg({ kind: 'mission', missionId: 'ms_one', at: '2026-11-16T11:00:01.000Z', text: '## Built\nThe sign-up page and its API.\n\nOpen points: none.' });
    expect(isMissionSummary(summary, done, false)).toBe(true);
    const last = msg({ kind: 'mission', missionId: 'ms_one', at: '2026-11-16T11:00:02.000Z', text: 'Mission done.' });
    expect(isMissionSummary(last, done, true)).toBe(true);
    const start = msg({ kind: 'mission', missionId: 'ms_one', at: '2026-11-16T10:00:00.000Z', text: 'Mission approved' });
    expect(isMissionSummary(start, done, false)).toBe(false);
    expect(isMissionSummary(summary, mission({ state: 'stopped', result: 'x' }), true)).toBe(false);
    expect(isMissionSummary(summary, undefined, true)).toBe(false);
  });
});
