// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import type { BoardNode, Project, Session } from '../lib/types';
import { connectIntent, directedSessions, directorsOf, editorAgents } from './groups';

const node = (id: string, kind: string, data: Record<string, unknown> = {}): BoardNode => ({ id, kind, x: 0, y: 0, w: 100, h: 100, z: 0, data }) as BoardNode;

describe('what a memory tile may connect to', () => {
  const memory = node('m', 'memory');
  const note = node('n', 'note');
  const orchestrator = node('o', 'orchestrator');
  const editor = node('e', 'editor');
  const nodes = [memory, note, orchestrator, editor];

  it('reaches orchestrator cards and the editor, and sends when wired', () => {
    for (const to of ['o', 'e']) {
      const got = connectIntent(nodes, [], 'm', to);
      expect(got).toMatchObject({ ok: true, sends: true, op: { op: 'putEdge', edge: { kind: 'wire', source: 'm', target: to } } });
    }
  });

  it('does not type when the tile is kept by hand', () => {
    const manual = node('m', 'memory', { manual: true });
    expect(connectIntent([manual, orchestrator], [], 'm', 'o')).toMatchObject({ ok: true, sends: false });
  });

  it('says what it can be wired to', () => {
    const got = connectIntent(nodes, [], 'm', 'n');
    expect(got.ok).toBe(false);
    if (!got.ok) expect(got.reason).toContain('orchestrator card or the editor');
  });

  it('stays out of what other cards may do', () => {
    expect(connectIntent(nodes, [], 'n', 'o').ok).toBe(false);
    expect(connectIntent(nodes, [], 'n', 'e').ok).toBe(false);
    expect(connectIntent(nodes, [], 'o', 'e').ok).toBe(true);
    expect(connectIntent(nodes, [], 'e', 'o').ok).toBe(true);
  });

  it('a second drag disconnects', () => {
    const edge = { id: 'x', source: 'm', target: 'o', kind: 'wire', data: {} };
    expect(connectIntent(nodes, [edge], 'm', 'o')).toMatchObject({ ok: true, label: 'Disconnected', op: { op: 'delEdge', id: 'x' } });
  });
});

describe('an orchestrator card wired to an editor card', () => {
  const project = { id: 'p_shop', name: 'shop', machineId: 'local', path: '/work/shop', parts: [
    { id: 'pt_main', name: 'shop', machineId: 'local', path: '/work/shop' },
    { id: 'pt_web', name: 'web', machineId: 'local', path: '/work/web' },
    { id: 'pt_api', name: 'api', machineId: 'm_box', path: '/srv/api' },
  ] } as unknown as Project;
  const at = { workspaceId: 'w_main', project };
  const lead = node('o', 'orchestrator');
  const root = node('er', 'editor');
  const web = node('ew', 'editor', { part: 'pt_web' });
  const kanban = node('k', 'kanban');
  const nodes = [lead, root, web, kanban];
  const agent = (id: string, more: Partial<Session> = {}) => ({ id, handle: id, panel: true, agentKind: 'agent', workspaceId: 'w_main', status: 'running', ...more }) as Session;

  it('is a wire next to the editor’s own, and says when no agent is inside yet', () => {
    const got = connectIntent(nodes, [], 'o', 'ew', at);
    expect(got).toMatchObject({ ok: true, op: { op: 'putEdge', edge: { kind: 'wire', source: 'o', target: 'ew' } } });
    if (got.ok) expect(got.note).toContain('No agent is open in this editor yet');
    const back = { id: 'x', source: 'ew', target: 'o', kind: 'wire', data: {} };
    expect(connectIntent(nodes, [back], 'o', 'ew', at)).toMatchObject({ ok: true, op: { op: 'putEdge' } });
    expect(connectIntent(nodes, [back], 'ew', 'o', at)).toMatchObject({ ok: true, label: 'Disconnected' });
    expect(connectIntent(nodes, [], 'k', 'ew', at).ok).toBe(false);
    const wire = { id: 'y', source: 'o', target: 'ew', kind: 'wire', data: {} };
    expect(directorsOf(nodes, [wire, back], 'ew')).toEqual([lead]);
  });

  it('holds the agents opened in it, and the older ones of its placement', () => {
    const sessions = [
      agent('ada', { card: 'ew', partId: 'pt_web' }),
      agent('bo', { partId: 'pt_web' }), // before cards were recorded
      agent('cy', { card: 'n_gone', partId: 'pt_web' }),
      agent('di', { root: true }),
      agent('ed', { partId: 'pt_api' }), // no card of api: the Root card shows it
      agent('fay', { card: 'er', partId: 'pt_web' }),
      agent('gil', { card: 'ew', partId: 'pt_web', agentKind: 'shell' }), // the user's own terminal
      agent('hal', { partId: 'pt_web', workspaceId: 'w_other' }),
      agent('ivy', { panel: false, partId: 'pt_web' }),
    ];
    const ids = (l: Session[]) => l.map((s) => s.id).sort();
    expect(ids(editorAgents(nodes, web, sessions, at))).toEqual(['ada', 'bo', 'cy']);
    expect(ids(editorAgents(nodes, root, sessions, at))).toEqual(['di', 'ed', 'fay']);
    const byId = Object.fromEntries(sessions.map((s) => [s.id, s]));
    const t = node('t', 'terminal', { sessionId: 'ivy' });
    const edges = [
      { id: 'y', source: 'o', target: 'ew', kind: 'wire', data: {} },
      { id: 'z', source: 'o', target: 't', kind: 'wire', data: {} },
    ];
    expect(directedSessions([...nodes, t], edges, 'o', byId, at).sort()).toEqual(['ada', 'bo', 'cy', 'ivy']);
  });
});
