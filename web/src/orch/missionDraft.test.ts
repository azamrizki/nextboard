import { describe, expect, it } from 'vitest';
import { dependencyChoices, draftFrom, draftProblem, emptyDraft, missionInput, newTask, removeTask, titleOf, type MissionDraft } from './missionDraft';
import type { MissionRun } from './types';

const draft = (over: Partial<MissionDraft> = {}): MissionDraft => ({
  goal: 'Sign-up flow',
  doneWhen: '',
  doc: '',
  review: false,
  tasks: [
    { id: 't1', handle: 'Arkan', title: 'API', text: 'Build the endpoint', dependsOn: [] },
    { id: 't3', handle: 'Bintang', title: '', text: 'Build the form\nwith validation', dependsOn: ['t1'] },
  ],
  ...over,
});

describe('mission draft', () => {
  it('numbers new tasks after the highest id', () => {
    expect(newTask(draft().tasks, 'Arkan').id).toBe('t4');
    expect(emptyDraft(['Citra']).tasks[0]).toMatchObject({ id: 't1', handle: 'Citra' });
  });

  it('drops dependencies on a removed task', () => {
    expect(removeTask(draft().tasks, 't1')).toEqual([{ id: 't3', handle: 'Bintang', title: '', text: 'Build the form\nwith validation', dependsOn: [] }]);
  });

  it('never offers a dependency that closes a circle', () => {
    const tasks = draft().tasks;
    expect(dependencyChoices(tasks, 't3')).toEqual(['t1']);
    expect(dependencyChoices(tasks, 't1')).toEqual([]);
  });

  it('says the first problem', () => {
    const team = ['Arkan', 'Bintang'];
    expect(draftProblem(draft(), team)).toBe('');
    expect(draftProblem(draft({ goal: ' ' }), team)).toBe('Write the goal.');
    expect(draftProblem(draft(), ['Arkan'])).toBe('@Bintang (t3) is not wired to this tile.');
    expect(draftProblem(draft({ tasks: [{ id: 't1', handle: '', title: '', text: 'x', dependsOn: [] }] }), team)).toBe('Pick a console for t1.');
    expect(draftProblem(draft({ tasks: [] }), team)).toBe('Add at least one task.');
  });

  it('renumbers tasks and fills titles in the request', () => {
    expect(missionInput(draft({ doneWhen: ' tests pass ' }), 'n_1', 'w_1')).toEqual({
      goal: 'Sign-up flow',
      doneWhen: 'tests pass',
      tasks: [
        { id: 't1', handle: 'Arkan', title: 'API', text: 'Build the endpoint' },
        { id: 't2', handle: 'Bintang', title: 'Build the form', text: 'Build the form\nwith validation', dependsOn: ['t1'] },
      ],
      threadId: 'n_1',
      cardId: 'n_1',
      workspaceId: 'w_1',
    });
  });

  it('cuts a long title at a word', () => {
    expect(titleOf('Write the migration for the new accounts table and backfill every existing row')).toBe('Write the migration for the new accounts table and backfill…');
  });

  it('turns a finished mission into a draft', () => {
    const m = { goal: 'G', doneWhen: 'D', doc: '', tasks: [{ id: 't1', handle: 'Arkan', title: 'A', text: 'T', dependsOn: null, state: 'done' }] } as unknown as MissionRun;
    expect(draftFrom(m)).toEqual({ goal: 'G', doneWhen: 'D', doc: '', review: false, tasks: [{ id: 't1', handle: 'Arkan', title: 'A', text: 'T', dependsOn: [] }] });
  });
});
