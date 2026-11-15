import { describe, expect, it } from 'vitest';
import type { Machine, Project, Session } from '../lib/types';
import { machineBlock, machineUsers } from '../machines/inUse';
import { fitSize } from './art';
import { bulkOutcome, deleteList, nameList, removeSummary, stopLine, typedOk } from './remove';

const project = (id: string, over: Partial<Project> = {}): Project =>
  ({ id, name: id, machineId: 'local', path: `/srv/${id}`, workspaces: [{ id: `w_${id}` }], sessionCount: 0, runningCount: 0, createdAt: '', lastOpenedAt: '', ...over }) as Project;
const session = (id: string, projectId: string, status: Session['status'], over: Partial<Session> = {}): Session => ({ id, projectId, status, ...over }) as Session;

describe('the remove dialog', () => {
  it('asks for the word delete, however it is typed', () => {
    expect(typedOk('delete')).toBe(true);
    expect(typedOk('  DELETE ')).toBe(true);
    expect(typedOk('del')).toBe(false);
    expect(typedOk('')).toBe(false);
    expect(typedOk('2')).toBe(false);
  });

  it('counts the consoles that stop from the live sessions, not the editor’s own shells', () => {
    const s = removeSummary(
      [project('a'), project('b', { workspaces: [{ id: 'w1' }, { id: 'w2' }] as Project['workspaces'] })],
      [session('s1', 'a', 'running'), session('s2', 'a', 'asleep'), session('s3', 'b', 'starting'), session('s4', 'b', 'running', { panel: true }), session('s5', 'c', 'running')],
    );
    expect(s).toMatchObject({ count: 2, consoles: 3, running: 2, boards: 3 });
    expect(stopLine(s)).toBe('2 running consoles will stop.');
  });

  it('falls back to the project’s own counts when the page has no sessions yet', () => {
    const s = removeSummary([project('a', { sessionCount: 3, runningCount: 1 })], []);
    expect(s).toMatchObject({ consoles: 3, running: 1 });
    expect(stopLine(s)).toBe('1 running console will stop.');
    expect(stopLine(removeSummary([project('b')], []))).toBe('');
  });

  it('keeps every folder of every part, once', () => {
    const p = project('a', { parts: [{ id: 'p1', name: 'Api', machineId: 'local', path: '/srv/a' }, { id: 'p2', name: 'Web', machineId: 'm_x', path: '/var/www/a' }] });
    expect(removeSummary([p, project('a2', { path: '/srv/a' })], []).folders).toEqual(['/srv/a', '/var/www/a']);
  });

  it('lists what a permanent delete removes, with counts where known', () => {
    const lines = deleteList(removeSummary([project('a', { sessionCount: 2 })], []));
    expect(lines[0]).toBe('2 consoles and their scrollback');
    expect(lines[1]).toMatch(/^1 board /);
    expect(lines.join(' ')).toMatch(/memory/);
    expect(lines.join(' ')).toMatch(/history/);
  });

  it('reads each id of a bulk answer on its own', () => {
    const out = bulkOutcome(
      [{ id: 'a', ok: true }, { id: 'b', ok: false, error: 'in_use' }],
      ['a', 'b', 'c'],
      (id) => `Project ${id}`,
    );
    expect(out.done).toEqual(['a']);
    expect(out.failed).toEqual([
      { id: 'b', name: 'Project b', error: 'it is in use' },
      { id: 'c', name: 'Project c', error: 'no answer from the hub' },
    ]);
  });

  it('names a few, then says how many more', () => {
    expect(nameList([])).toBe('');
    expect(nameList(['Alpha'])).toBe('Alpha');
    expect(nameList(['Alpha', 'Beta', 'Gamma'])).toBe('Alpha, Beta and Gamma');
    expect(nameList(['Alpha', 'Beta', 'Gamma', 'Delta'])).toBe('Alpha, Beta and 2 more');
  });
});

describe('machines in use', () => {
  const machine = (id: string, over: Partial<Machine> = {}): Machine => ({ id, name: id, kind: 'ssh', sessionCount: 0, ...over }) as Machine;
  const projects = [
    project('Zulu', { machineId: 'm_a' }),
    project('Alpha', { parts: [{ id: 'p1', name: 'Main', machineId: 'local', path: '/a' }, { id: 'p2', name: 'Db', machineId: 'm_a', path: '/b' }] }),
    project('Other', { machineId: 'm_b' }),
  ];

  it('are those a part of any project is on, by name', () => {
    expect(machineUsers('m_a', projects)).toEqual([
      { id: 'Alpha', name: 'Alpha' },
      { id: 'Zulu', name: 'Zulu' },
    ]);
    expect(machineUsers('m_c', projects)).toEqual([]);
  });

  it('say why they cannot go: This PC, the projects, or open consoles', () => {
    expect(machineBlock(machine('local', { kind: 'local' }), [])).toBe('This PC always stays');
    expect(machineBlock(machine('m_a'), machineUsers('m_a', projects))).toBe('Used by Alpha and Zulu');
    expect(machineBlock(machine('m_c', { sessionCount: 2 }), [])).toBe('Has 2 consoles open');
    expect(machineBlock(machine('m_c'), [])).toBe('');
  });
});

describe('fitting a picture', () => {
  it('leaves a cover that fits as it is, and shrinks a big one keeping its shape', () => {
    expect(fitSize(1200, 800, 'cover')).toMatchObject({ w: 1200, h: 800, same: true });
    expect(fitSize(4000, 2000, 'cover')).toMatchObject({ sx: 0, sy: 0, w: 1600, h: 800, same: false });
    expect(fitSize(1000, 3000, 'cover')).toMatchObject({ w: 400, h: 1200 });
  });

  it('keeps the middle square of an icon, at most 256 px', () => {
    expect(fitSize(1024, 1024, 'icon')).toMatchObject({ sx: 0, sw: 1024, w: 256, h: 256, same: false });
    expect(fitSize(300, 200, 'icon')).toMatchObject({ sx: 50, sy: 0, sw: 200, sh: 200, w: 200, h: 200, same: false });
    expect(fitSize(128, 128, 'icon')).toMatchObject({ w: 128, h: 128, same: true });
  });
});
