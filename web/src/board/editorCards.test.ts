import { describe, expect, it } from 'vitest';
import { ROOT } from '../lib/parts';
import type { BoardNode, Project } from '../lib/types';
import { cardFor, cardForFile, cardLabel, editorData, partOfFile } from './editorCards';

const project = (parts: Project['parts']): Project => ({
  id: 'p_shop',
  name: 'acme-shop',
  machineId: parts?.[0]?.machineId ?? 'local',
  path: parts?.[0]?.path ?? '/srv/app',
  parts,
  createdAt: '',
  lastOpenedAt: '',
  workspaces: null,
  sessionCount: 0,
  runningCount: 0,
});
// the same folder on two machines: only the machine tells them apart
const two = project([
  { id: 'pt_admin', name: 'admin', machineId: 'srv-a', path: '/srv/app' },
  { id: 'pt_web', name: 'frontend', machineId: 'local', path: '/srv/app' },
]);
const one = project([{ id: 'pt_shop', name: 'shop', machineId: 'local', path: '/srv/app' }]);

const editor = (id: string, part?: string): BoardNode => ({ id, kind: 'editor', x: 0, y: 0, w: 10, h: 10, data: part === undefined ? {} : { part } });

describe('editor cards by placement', () => {
  it('finds the card of a placement, a Root card when allowed', () => {
    const nodes = [editor('root'), editor('admin', 'pt_admin')];
    expect(cardFor(nodes, two, { partId: 'pt_admin' })?.id).toBe('admin');
    expect(cardFor(nodes, two, { partId: 'pt_web' })).toBeUndefined();
    expect(cardFor(nodes, two, { partId: 'pt_web' }, true)?.id).toBe('root');
    expect(cardFor(nodes, two, ROOT)?.id).toBe('root');
    // a card naming a part that went is a Root card
    expect(cardFor([editor('old', 'pt_gone')], two, ROOT)?.id).toBe('old');
  });

  it('treats every card of a single-part project as the one card', () => {
    expect(cardFor([editor('a'), editor('b', 'pt_shop')], one, ROOT)?.id).toBe('a');
    expect(editorData(one, ROOT)).toEqual({});
    expect(cardLabel(one, ROOT)).toBe('Code editor');
  });

  it('opens a file in its part’s card, else a Root card, else places one in its part', () => {
    const file = { machineId: 'srv-a', path: '/srv/app/x.php' };
    expect(partOfFile(two, file)?.id).toBe('pt_admin');
    expect(partOfFile(two, { machineId: 'local', path: '/srv/app/x.php' })?.id).toBe('pt_web');
    expect(partOfFile(two, { partId: 'pt_web', path: '/srv/app/x.php' })?.id).toBe('pt_web');
    expect(cardForFile([editor('admin', 'pt_admin'), editor('root')], two, file).card?.id).toBe('admin');
    expect(cardForFile([editor('web', 'pt_web'), editor('root')], two, file).card?.id).toBe('root');
    const none = cardForFile([editor('web', 'pt_web')], two, file);
    expect(none.card).toBeUndefined();
    expect(none.placement).toEqual({ partId: 'pt_admin' });
    expect(editorData(two, none.placement)).toEqual({ part: 'pt_admin' });
    // outside every part: the Root
    expect(cardForFile([], two, { machineId: 'local', path: '/etc/hosts' }).placement).toEqual(ROOT);
  });

  it('labels a card with its part, or all parts', () => {
    expect(cardLabel(two, ROOT)).toBe('Code editor · All parts');
    expect(cardLabel(two, { partId: 'pt_web' })).toBe('Code editor · frontend');
  });
});
