import { describe, expect, it } from 'vitest';
import type { Project } from '../lib/types';
import type { GitRepoInfo } from '../studio/types';
import { openPartsFrom, partRef, sumRepos, summaryLabel } from './parts';

const project = {
  id: 'p_shop',
  name: 'Shop',
  machineId: 'local',
  path: '/w/shop',
  parts: [
    { id: 'pt_api', name: 'Api', machineId: 'local', path: '/w/shop' },
    { id: 'pt_web', name: 'Web', machineId: 'm_far', path: '/srv/web' },
  ],
} as unknown as Project;

const repo = (path: string, o: Partial<GitRepoInfo> = {}): GitRepoInfo => ({ path, name: path || 'shop', branch: 'main', ahead: 0, behind: 0, changed: 0, ...o });

describe('the git of each part', () => {
  it('is project~part for every part, the first one too, as the hub and its gitPart events name them', () => {
    expect(partRef(project, project.parts![0])).toBe('p_shop~pt_api');
    expect(partRef(project, project.parts![1])).toBe('p_shop~pt_web');
  });

  it('keeps its key when the parts are reordered', () => {
    const moved = { ...project, parts: [project.parts![1], project.parts![0]] } as Project;
    expect(partRef(moved, moved.parts![1])).toBe('p_shop~pt_api');
  });

  it('an old part keeps its id main', () => {
    const old = { ...project, parts: undefined } as Project;
    expect(partRef(old, { id: 'main', name: 'Main', machineId: 'local', path: '/w/shop' })).toBe('p_shop~main');
  });
});

describe('a part header', () => {
  it('adds up its repositories and names the branch only when there is one', () => {
    expect(sumRepos([repo('', { branch: 'dev', ahead: 2, changed: 3 })])).toEqual({ branch: 'dev', ahead: 2, behind: 0, changed: 3, repos: 1, unread: 0 });
    const two = sumRepos([repo('a', { ahead: 1, behind: 4, changed: 2 }), repo('b', { changed: 5 })]);
    expect(two).toEqual({ branch: '', ahead: 1, behind: 4, changed: 7, repos: 2, unread: 0 });
  });

  it('leaves out a repository that could not be read, and counts it apart', () => {
    expect(sumRepos([repo('a', { changed: 2 }), repo('b', { changed: 9, error: 'timeout' })])).toMatchObject({ changed: 2, repos: 2, unread: 1 });
    expect(sumRepos([repo('', { error: 'gone' })]).branch).toBe('');
    expect(sumRepos(undefined)).toMatchObject({ repos: 0, changed: 0 });
  });

  it('reads out the branch, the changes and what waits to sync', () => {
    expect(summaryLabel({ branch: 'main', ahead: 0, behind: 0, changed: 1 })).toBe('Branch main, 1 changed file');
    expect(summaryLabel({ branch: '', ahead: 2, behind: 3, changed: 0 })).toBe('Branch detached, 0 changed files, 2 to push, 3 to pull');
  });
});

describe('the parts left open', () => {
  it('come back for the parts that still exist', () => {
    expect([...openPartsFrom('["pt_web","pt_gone"]', project.parts!)]).toEqual(['pt_web']);
  });

  it('start empty when nothing or something else was kept', () => {
    expect(openPartsFrom(null, project.parts!).size).toBe(0);
    expect(openPartsFrom('{bad', project.parts!).size).toBe(0);
    expect(openPartsFrom('{"a":1}', project.parts!).size).toBe(0);
  });
});
