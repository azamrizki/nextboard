import { describe, expect, it } from 'vitest';
import type { Machine, Project, Session } from './lib/types';
import { machineNameOf, store } from './store';

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

describe('live values over copies taken earlier', () => {
  it('a session names its machine as it is called now, its own copy only once the machine is gone', () => {
    store.seedMachines([{ ...machine('local'), name: 'This PC' }, { ...machine('m_box'), name: 'devbox' }]);
    const s = { machineId: 'm_box', machineName: 'old-box' };
    expect(machineNameOf(s)).toBe('devbox');
    store.apply({ type: 'machine', machine: { ...machine('m_box'), name: 'buildbox' } });
    expect(machineNameOf(s)).toBe('buildbox');
    store.apply({ type: 'machine', machine: { ...machine('m_box', '2026-11-15T10:00:00Z'), name: 'buildbox' } });
    expect(machineNameOf(s)).toBe('buildbox');
    store.apply({ type: 'machineRemoved', id: 'm_box' });
    expect(machineNameOf(s)).toBe('old-box');
    // This PC keeps the hub's name the hub gave its consoles, until it is renamed
    const here = { machineId: 'local', machineName: 'studio-hub' };
    expect(machineNameOf(here)).toBe('studio-hub');
    store.apply({ type: 'machine', machine: { ...machine('local'), name: 'Workstation' } });
    expect(machineNameOf(here)).toBe('Workstation');
  });

  it('patchProject changes the project as the store has it then, not a copy from before a request', () => {
    const ws = (id: string, name: string) => ({ id, name }) as NonNullable<Project['workspaces']>[number];
    store.seedProjects([{ ...project('p_live'), workspaces: [ws('w_a', 'Main')] }]);
    const clickTime = store.getState().projects.p_live;
    // while the request was out, another tab renamed the project and its workspace
    store.apply({ type: 'project', project: { ...clickTime, name: 'Renamed', workspaces: [ws('w_a', 'API')] } });
    store.patchProject('p_live', (p) => ({ ...p, workspaces: [...(p.workspaces ?? []), ws('w_b', 'New')] }));
    const now = store.getState().projects.p_live;
    expect(now.name).toBe('Renamed');
    expect(now.workspaces?.map((w) => w.name)).toEqual(['API', 'New']);
    store.patchProject('p_gone', (p) => ({ ...p, name: 'x' }));
    expect(store.getState().projects.p_gone).toBeUndefined();
  });

  it('a project event that changes nothing keeps the record and notifies no one', () => {
    store.seedProjects([{ ...project('p_same'), workspaces: [{ id: 'w_a', name: 'Main' }] as Project['workspaces'] }]);
    const before = store.getState();
    let notified = 0;
    const off = store.subscribe(() => notified++);
    store.apply({ type: 'project', project: { ...project('p_same'), workspaces: [{ id: 'w_a', name: 'Main' }] as Project['workspaces'] } });
    expect(store.getState()).toBe(before);
    expect(notified).toBe(0);
    store.apply({ type: 'project', project: { ...project('p_same'), workspaces: [{ id: 'w_a', name: 'API' }] as Project['workspaces'] } });
    expect(store.getState().projects.p_same.workspaces?.[0].name).toBe('API');
    expect(notified).toBe(1);
    off();
  });
});

describe('the newer record of a console stays', () => {
  const session = (rev: number | undefined, agentState: Session['agentState']): Session => ({ id: 's_rev', name: 'NextCode', agentState, ...(rev === undefined ? {} : { rev }) }) as Session;

  it('a reply read before an event that came first does not undo it', () => {
    // a NextCode console is ready before the reply that made it goes out
    store.apply({ type: 'session', session: session(20, 'ready') });
    store.upsertSession(session(10, 'starting'));
    expect(store.getState().sessions.s_rev.agentState).toBe('ready');
    store.upsertSession(session(30, 'working'));
    expect(store.getState().sessions.s_rev.agentState).toBe('working');
  });

  it('a record that differs only in rev notifies no one', () => {
    store.upsertSession(session(40, 'ready'));
    const before = store.getState().sessions.s_rev;
    let calls = 0;
    const off = store.subscribe(() => calls++);
    store.upsertSession(session(41, 'ready'));
    off();
    expect(calls).toBe(0);
    expect(store.getState().sessions.s_rev).toBe(before);
    // and the rev was taken: a reply of 40 is older than what came since
    store.upsertSession(session(40, 'starting'));
    expect(store.getState().sessions.s_rev.agentState).toBe('ready');
  });

  it("a console socket's status frame applies after a record that changed only rev", () => {
    store.upsertSession(session(50, 'ready'));
    store.upsertSession(session(51, 'ready'));
    store.patchSessionStatus('s_rev', { t: 'status', status: 'running', exitCode: 0, busy: true, agentState: 'working' });
    expect(store.getState().sessions.s_rev.agentState).toBe('working');
  });

  it('a snapshot is what the hub has now, and records without rev (an older hub) always apply', () => {
    store.apply({ type: 'snapshot', sessions: [session(5, 'needsYou')], devices: [], machines: [] });
    expect(store.getState().sessions.s_rev.agentState).toBe('needsYou');
    store.upsertSession(session(4, 'ready'));
    expect(store.getState().sessions.s_rev.agentState).toBe('needsYou');
    store.upsertSession(session(undefined, 'ready'));
    expect(store.getState().sessions.s_rev.agentState).toBe('ready');
  });
});
