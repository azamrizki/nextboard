import { describe, expect, it } from 'vitest';
import { ROOT } from '../../lib/parts';
import type { Project } from '../../lib/types';
import { absIn, dirOf, markdownPartOfFile, markdownPartOfPlace, relIn, resolvePath } from './notes';

const project = (parts: Project['parts']): Project => ({
  id: 'p_atlas',
  name: 'Atlas',
  machineId: parts?.[0]?.machineId ?? 'local',
  path: parts?.[0]?.path ?? '/home/me/atlas',
  parts,
  createdAt: '',
  lastOpenedAt: '',
  workspaces: null,
  sessionCount: 0,
  runningCount: 0,
});

// two machines with the same folder, and a nested part on This PC
const multi = project([
  { id: 'pt_site', name: 'Site', machineId: 'local', path: '/srv/atlas' },
  { id: 'pt_api', name: 'Api', machineId: 'srv-b', path: '/srv/atlas' },
  { id: 'pt_docs', name: 'Docs', machineId: 'local', path: '/srv/atlas/docs/' },
]);
const single = project([{ id: 'pt_one', name: 'One', machineId: 'local', path: '/home/me/one' }]);
const legacy = project(undefined);

describe('the part a markdown card opens the studio in', () => {
  it('is the part holding the file on its machine, the deepest one', () => {
    expect(markdownPartOfFile(multi, 'local', '/srv/atlas/README.md')?.id).toBe('pt_site');
    expect(markdownPartOfFile(multi, 'srv-b', '/srv/atlas/README.md')?.id).toBe('pt_api');
    expect(markdownPartOfFile(multi, 'local', '/srv/atlas/docs/guide.md')?.id).toBe('pt_docs');
    expect(markdownPartOfFile(multi, '', '/srv/atlas/docs/../plan.md')?.id).toBe('pt_site');
  });
  it('outside every part: the first part on that machine; no part there: none', () => {
    expect(markdownPartOfFile(multi, 'srv-b', '/home/ops/notes.md')?.id).toBe('pt_api');
    expect(markdownPartOfFile(multi, 'local', '/tmp/x.md')?.id).toBe('pt_site');
    expect(markdownPartOfFile(multi, 'srv-c', '/srv/atlas/README.md')).toBeUndefined();
  });
  it('a single-part project: its part on its machine, as before', () => {
    expect(markdownPartOfFile(single, 'local', '/elsewhere/a.md')?.id).toBe('pt_one');
    expect(markdownPartOfFile(single, 'srv-b', '/home/me/one/a.md')).toBeUndefined();
    expect(markdownPartOfFile(legacy, 'local', '/home/me/atlas/a.md')?.id).toBe('main');
  });
});

describe('the part the studio opens in from the rail', () => {
  it('is the focused tile’s part in a multi-part project, else the studio’s own default', () => {
    expect(markdownPartOfPlace(multi, { partId: 'pt_api' })).toBe('pt_api');
    expect(markdownPartOfPlace(multi, { partId: 'pt_gone' })).toBeUndefined();
    expect(markdownPartOfPlace(multi, ROOT)).toBeUndefined();
    expect(markdownPartOfPlace(multi, null)).toBeUndefined();
    expect(markdownPartOfPlace(single, { partId: 'pt_one' })).toBeUndefined();
  });
});

describe('note paths', () => {
  it('resolves links against the note', () => {
    expect(dirOf('/srv/atlas/docs/a.md')).toBe('/srv/atlas/docs/');
    expect(resolvePath('/srv/atlas/docs/a.md', '../README.md')).toBe('/srv/atlas/README.md');
    expect(resolvePath('/srv/atlas/docs/a.md', './b.md')).toBe('/srv/atlas/docs/b.md');
    expect(resolvePath('/srv/atlas/a.md', '/etc/../srv/x.md')).toBe('/srv/x.md');
    expect(resolvePath('/a.md', '../../b.md')).toBe('/b.md');
  });
  it('rel within the part folder, at a slash boundary', () => {
    expect(relIn('/srv/atlas/', '/srv/atlas/docs/a.md')).toBe('docs/a.md');
    expect(relIn('/srv/atlas', '/srv/atlas2/a.md')).toBeNull();
    expect(relIn('/srv/atlas', '/srv/atlas')).toBeNull();
    expect(relIn('/', '/a.md')).toBe('a.md');
    expect(absIn('/srv/atlas/', 'docs/a.md')).toBe('/srv/atlas/docs/a.md');
    expect(absIn('/', 'a.md')).toBe('/a.md');
  });
});
