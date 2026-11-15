import { describe, expect, it } from 'vitest';
import type { ProjectPart } from '../lib/types';
import type { GitRepoInfo } from '../studio/types';
import { fitCount, partsGit, partsGitText, partsSummary } from './partsCard';

const part = (name: string, machineId: string): ProjectPart => ({ id: `pt_${name}`, name, machineId, path: `/srv/acme-shop/${name}` });
const repo = (branch: string, changed = 0, over: Partial<GitRepoInfo> = {}): GitRepoInfo => ({ path: '/x', name: 'x', branch, ahead: 0, behind: 0, changed, ...over }) as GitRepoInfo;

describe('a project in several parts on its card', () => {
  it('says how many parts on how many machines', () => {
    const parts = [part('storefront', 'm_devbox'), part('api', 'm_api'), part('worker', 'm_worker'), part('admin', 'm_devbox')];
    expect(partsSummary(parts)).toBe('4 parts · 3 machines');
    expect(partsSummary([part('storefront', 'local'), part('api', 'local')])).toBe('2 parts · 1 machine');
    expect(partsSummary(parts, false)).toBe('4 parts');
  });

  it('shows every chip that fits, or as many as leave room for +N', () => {
    // 3 chips of 50 with gaps of 4: 158 wide
    expect(fitCount([50, 50, 50], 158, 4, 24)).toBe(3);
    expect(fitCount([50, 50, 50], 200, 4, 24)).toBe(3);
    // not all: two chips (104) + gap + "+1" (24) = 132
    expect(fitCount([50, 50, 50], 140, 4, 24)).toBe(2);
    expect(fitCount([50, 50, 50], 131, 4, 24)).toBe(1);
    expect(fitCount([50, 50, 50], 20, 4, 24)).toBe(0);
    expect(fitCount([], 100, 4, 24)).toBe(0);
  });

  it('adds the parts’ git up: repos, the ones with changes, the branch they share', () => {
    const s = partsGit([{ items: [repo('main')] }, { items: [repo('main', 3), repo('main')] }, { items: [repo('main', 1)] }]);
    expect(s).toMatchObject({ repos: 4, dirty: 2, branch: 'main', offline: 0, pending: 0 });
    expect(partsGitText(s)).toMatchObject({ text: '4 repos', badge: '2 with changes', dirty: true });
    const clean = partsGit([{ items: [repo('main')] }, { items: [repo('develop')] }]);
    expect(clean.branch).toBe('');
    expect(partsGitText(clean)).toMatchObject({ text: '2 repos', badge: 'All clean', dirty: false });
  });

  it('says which parts it could not ask, and does not count them', () => {
    const s = partsGit([{ items: [repo('main')] }, { offline: true }, { error: 'timeout' }, { items: [repo('main', 0, { error: 'bad' })] }]);
    expect(s).toMatchObject({ repos: 1, offline: 1, unread: 2 });
    expect(partsGitText(s)).toMatchObject({ text: '1 repo', badge: 'All clean', note: '1 offline · 2 unreadable' });
    expect(partsGitText(partsGit([{ offline: true }, { offline: true }])).text).toBe('Machines offline');
    expect(partsGitText(partsGit([{ items: [] }, { items: [] }])).text).toBe('No git repositories');
    expect(partsGitText(partsGit([undefined, { items: [] }])).text).toBe('Checking git…');
  });
});
