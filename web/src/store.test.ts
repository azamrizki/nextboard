import { describe, expect, it } from 'vitest';
import type { Machine, Project } from './lib/types';
import { store } from './store';

const project = (id: string, archivedAt?: string): Project => ({ id, name: id, machineId: 'local', path: `/srv/${id}`, workspaces: [], sessionCount: 0, runningCount: 0, createdAt: '', lastOpenedAt: '', archivedAt }) as Project;
const machine = (id: string, archivedAt?: string): Machine => ({ id, name: id, kind: 'ssh', sessionCount: 0, archivedAt }) as Machine;

describe('archived projects and machines in the live store', () => {
  it('a project event with archivedAt takes it off the active list, a restore brings it back', () => {
    store.seedProjects([project('p_a'), project('p_b')]);
    store.apply({ type: 'project', project: project('p_a', '2026-11-15T10:00:00Z') });
    expect(Object.keys(store.getState().projects)).toEqual(['p_b']);
    expect(Object.keys(store.getState().archivedProjects)).toEqual(['p_a']);
    store.apply({ type: 'project', project: project('p_a') });
    expect(Object.keys(store.getState().projects).sort()).toEqual(['p_a', 'p_b']);
    expect(store.getState().archivedProjects).toEqual({});
  });

  it('projectRemoved (a permanent delete) takes it off both lists', () => {
    store.seedArchivedProjects([project('p_c', '2026-11-15T10:00:00Z')]);
    store.apply({ type: 'projectRemoved', id: 'p_c' });
    store.apply({ type: 'projectRemoved', id: 'p_b' });
    expect(store.getState().archivedProjects).toEqual({});
    expect(Object.keys(store.getState().projects)).toEqual(['p_a']);
  });

  it('never lists an archived one among the active, whichever way it came', () => {
    store.seedProjects([project('p_x', '2026-11-15T10:00:00Z'), project('p_y')]);
    expect(Object.keys(store.getState().projects)).toEqual(['p_y']);
    store.apply({ type: 'snapshot', sessions: [], devices: [], machines: [machine('local'), machine('m_old', '2026-11-15T10:00:00Z')], projects: [project('p_z', '2026-11-15T10:00:00Z')] });
    expect(Object.keys(store.getState().machines)).toEqual(['local']);
    expect(store.getState().projects).toEqual({});
  });

  it('machines move between the lists as projects do', () => {
    store.seedMachines([machine('local'), machine('m_a')]);
    store.apply({ type: 'machine', machine: machine('m_a', '2026-11-15T10:00:00Z') });
    expect(Object.keys(store.getState().machines)).toEqual(['local']);
    expect(Object.keys(store.getState().archivedMachines)).toEqual(['m_a']);
    store.apply({ type: 'machineRemoved', id: 'm_a' });
    expect(store.getState().archivedMachines).toEqual({});
  });
});
