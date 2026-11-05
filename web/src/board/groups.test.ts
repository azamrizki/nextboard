// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import type { BoardNode } from '../lib/types';
import { connectIntent } from './groups';

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
    expect(connectIntent(nodes, [], 'o', 'e').ok).toBe(false);
    expect(connectIntent(nodes, [], 'e', 'o').ok).toBe(true);
  });

  it('a second drag disconnects', () => {
    const edge = { id: 'x', source: 'm', target: 'o', kind: 'wire', data: {} };
    expect(connectIntent(nodes, [edge], 'm', 'o')).toMatchObject({ ok: true, label: 'Disconnected', op: { op: 'delEdge', id: 'x' } });
  });
});
