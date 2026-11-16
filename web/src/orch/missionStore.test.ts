import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api';
import { orchApi } from './api';
import { orchStore } from './store';
import type { MissionRun } from './types';

const run = (over: Partial<MissionRun> = {}): MissionRun => ({
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
  rounds: 2,
  maxRounds: 30,
  createdAt: '2026-11-16T10:00:00.000Z',
  updatedAt: '2026-11-16T10:00:00.000Z',
  ...over,
});

afterEach(() => {
  vi.restoreAllMocks();
  orchStore.reset();
});

describe('missions a tab keeps', () => {
  it('asks again for a mission that ended while the tab was away, though a thread asked for it before', async () => {
    const get = vi.spyOn(orchApi, 'missionRun').mockResolvedValueOnce(run());
    // the thread's message asked for it once: it was running then
    await orchStore.loadMission('ms_one');
    expect(orchStore.get().missions.ms_one.state).toBe('running');
    // back from away: the project's active list no longer has it, it was stopped meanwhile
    vi.spyOn(orchApi, 'missionRuns').mockResolvedValue([]);
    get.mockResolvedValueOnce(run({ state: 'stopped', updatedAt: '2026-11-16T11:00:00.000Z' }));
    await orchStore.loadMissions('p_demo', true);
    await vi.waitFor(() => expect(orchStore.get().missions.ms_one.state).toBe('stopped'));
    expect(get).toHaveBeenCalledTimes(2);
  });

  it('asks again after a fetch that failed, and drops a mission the hub no longer has', async () => {
    const get = vi.spyOn(orchApi, 'missionRun').mockRejectedValueOnce(new TypeError('Failed to fetch'));
    await orchStore.loadMission('ms_one');
    get.mockResolvedValueOnce(run());
    await orchStore.loadMission('ms_one');
    expect(get).toHaveBeenCalledTimes(2);
    expect(orchStore.get().missions.ms_one).toBeDefined();
    // once it is there, a thread does not ask for it again
    await orchStore.loadMission('ms_one');
    expect(get).toHaveBeenCalledTimes(2);

    get.mockRejectedValueOnce(new ApiError(404, 'mission not found', 'mission_not_found'));
    await orchStore.loadMission('ms_one', true);
    expect(orchStore.get().missions.ms_one).toBeUndefined();
    // gone for good: not asked again and again
    await orchStore.loadMission('ms_one');
    expect(get).toHaveBeenCalledTimes(3);
  });

  it('keeps the newer of an event and an answer that crossed it', () => {
    orchStore.upsertMission(run({ state: 'paused', updatedAt: '2026-11-16T10:05:00.000Z' }));
    orchStore.upsertMission(run({ state: 'running', updatedAt: '2026-11-16T10:01:00.000Z' }));
    expect(orchStore.get().missions.ms_one.state).toBe('paused');
    orchStore.apply({ type: 'mission', mission: run({ state: 'stopped', updatedAt: '2026-11-16T10:06:00.000Z' }) } as never);
    expect(orchStore.get().missions.ms_one.state).toBe('stopped');
  });
});
