import { describe, expect, it } from 'vitest';
import type { Project } from '../lib/types';
import { consolePaths, dirToList, fileQueryAt, fileSourceOf, insertFile, matchEntries, matchFiles, splitDirQuery } from './fileMentions';

const project = (parts: Project['parts'], over: Partial<Project> = {}) => ({ id: 'p1', name: 'Shop', machineId: 'local', path: parts?.[0]?.path ?? '/', parts, ...over }) as Project;

const one = project([{ id: 'pt_web', name: 'web', machineId: 'local', path: '/home/me/shop' }]);
const two = project(
  [
    { id: 'pt_web', name: 'web', machineId: 'local', path: '/home/me/shop' },
    { id: 'pt_api', name: 'api', machineId: 'local', path: '/srv/api' },
  ],
  { rootDir: '/home/me/.nextboard/roots/p1' },
);

describe('fileSourceOf', () => {
  it('a console in a part: that part’s list, from its folder', () => {
    expect(fileSourceOf({ cwd: '/home/me/shop', partId: 'pt_web', machineId: 'local' }, one)).toEqual({
      kind: 'parts',
      key: 'part:p1~pt_web:',
      parts: [{ ref: 'p1~pt_web', prefix: '', sub: '' }],
    });
  });

  it('a console in a folder inside its part: only what is under it', () => {
    const s = fileSourceOf({ cwd: '/home/me/shop/src/app/', partId: 'pt_web', machineId: 'local' }, one);
    expect(s).toMatchObject({ kind: 'parts', parts: [{ ref: 'p1~pt_web', prefix: '', sub: 'src/app' }] });
  });

  it('a root console: every part, under its name in the root folder', () => {
    const s = fileSourceOf({ cwd: '/home/me/.nextboard/roots/p1', root: true, machineId: 'local' }, two);
    expect(s).toMatchObject({
      kind: 'parts',
      parts: [
        { ref: 'p1~pt_web', prefix: 'web/', sub: '' },
        { ref: 'p1~pt_api', prefix: 'api/', sub: '' },
      ],
    });
  });

  it('a console in no part’s folder, or with no project: its folder, level by level', () => {
    expect(fileSourceOf({ cwd: '/tmp/scratch', machineId: 'local' }, two)).toEqual({ kind: 'dir', key: 'dir:local:/tmp/scratch', machineId: 'local', cwd: '/tmp/scratch' });
    expect(fileSourceOf({ cwd: '/tmp/x/', machineId: '' }, undefined)).toMatchObject({ kind: 'dir', machineId: 'local', cwd: '/tmp/x' });
    // the part says one folder, the console works in another
    expect(fileSourceOf({ cwd: '/elsewhere', partId: 'pt_web', machineId: 'local' }, one)).toMatchObject({ kind: 'dir', cwd: '/elsewhere' });
  });

  it('none without a folder', () => {
    expect(fileSourceOf({ cwd: '', machineId: 'local' }, undefined)).toBeNull();
    expect(fileSourceOf({ cwd: 'relative/dir', machineId: 'local' }, undefined)).toBeNull();
  });
});

describe('consolePaths', () => {
  const items = ['README.md', 'src/app/main.ts', 'src/app/view/x.tsx', 'src/lib.ts'];

  it('names a part’s files from the console’s folder', () => {
    expect(consolePaths(items, { prefix: '', sub: 'src/app' })).toEqual(['main.ts', 'view/x.tsx']);
    expect(consolePaths(items, { prefix: '', sub: '' })).toEqual(items);
  });

  it('puts the part’s name in front for a root console', () => {
    expect(consolePaths(['a.go', 'cmd/main.go'], { prefix: 'api/', sub: '' })).toEqual(['api/a.go', 'api/cmd/main.go']);
  });

  it('does not take a sibling folder that starts the same way', () => {
    expect(consolePaths(['src/app2/z.ts', 'src/app/y.ts'], { prefix: '', sub: 'src/app' })).toEqual(['y.ts']);
  });
});

describe('matchFiles', () => {
  const paths = ['src/components/Button.tsx', 'src/api/users.ts', 'docs/UserGuide.md', 'src/UserController.php', 'package.json'];

  it('finds a file by letters of its name, best first', () => {
    const hits = matchFiles('uc', paths).map((f) => f.path);
    expect(hits[0]).toBe('src/UserController.php');
    expect(matchFiles('button', paths)[0]).toMatchObject({ path: 'src/components/Button.tsx', dir: false });
  });

  it('marks the letters matched', () => {
    const [hit] = matchFiles('pkg', ['package.json']);
    expect(hit.positions.map((i) => 'package.json'[i]).join('')).toBe('pkg');
  });

  it('lists the shortest paths first for a bare "@", and no more than asked', () => {
    expect(matchFiles('', paths, 2).map((f) => f.path)).toEqual(['package.json', 'src/api/users.ts']);
    expect(matchFiles('zzz', paths)).toEqual([]);
  });
});

describe('browsing a folder that is no part', () => {
  it('splits what is typed into the folder and the name', () => {
    expect(splitDirQuery('src/comp')).toEqual({ dir: 'src', name: 'comp' });
    expect(splitDirQuery('src/')).toEqual({ dir: 'src', name: '' });
    expect(splitDirQuery('read')).toEqual({ dir: '', name: 'read' });
  });

  it('lists only inside the console’s folder', () => {
    expect(dirToList('/w', '')).toBe('/w');
    expect(dirToList('/w', 'src/app')).toBe('/w/src/app');
    expect(dirToList('/w', '../etc')).toBeNull();
  });

  it('offers folders (to go on) and files, matching the name', () => {
    const entries = [
      { name: 'src', dir: true },
      { name: 'README.md', dir: false },
      { name: '.git', dir: true },
      { name: 'scripts', dir: true },
    ];
    expect(matchEntries(entries, '', '').map((o) => o.path)).toEqual(['scripts/', 'src/', 'README.md']);
    expect(matchEntries(entries, '', 'sr')[0]).toMatchObject({ path: 'src/', dir: true });
    expect(matchEntries(entries, '', '.g').map((o) => o.path)).toEqual(['.git/']);
    const [inSub] = matchEntries([{ name: 'main.ts', dir: false }], 'src/app', 'main');
    expect(inSub).toMatchObject({ path: 'src/app/main.ts', dir: false });
    expect(inSub.positions.map((i) => inSub.path[i]).join('')).toBe('main');
  });
});

describe('fileQueryAt and insertFile', () => {
  it('finds the "@word" at the caret', () => {
    expect(fileQueryAt('look at @src/ma', 15)).toEqual({ start: 8, query: 'src/ma' });
    expect(fileQueryAt('@', 1)).toEqual({ start: 0, query: '' });
    expect(fileQueryAt('mail me@example.com', 19)).toBeNull();
    expect(fileQueryAt('@done and more', 14)).toBeNull();
  });

  it('inserts a file with a space after it, replacing the whole word', () => {
    expect(insertFile('see @ma and', 7, 4, { path: 'src/main.ts', dir: false })).toEqual({ text: 'see @src/main.ts and', caret: 17 });
    expect(insertFile('@rea', 4, 0, { path: 'README.md', dir: false })).toEqual({ text: '@README.md ', caret: 11 });
  });

  it('inserts a folder with no space, so its files list next', () => {
    expect(insertFile('@sr', 3, 0, { path: 'src/', dir: true })).toEqual({ text: '@src/', caret: 5 });
  });
});
