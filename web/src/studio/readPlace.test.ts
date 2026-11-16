import { describe, expect, it } from 'vitest';
import { ROOT_VALUE } from '../lib/placement';
import type { Project } from '../lib/types';
import { claudeReadsHint, readPlace, readsWhere, versionPlace } from './readPlace';

const project = (parts: Project['parts']): Project => ({
  id: 'p_shop',
  name: 'acme-shop',
  machineId: parts?.[0]?.machineId ?? 'local',
  path: parts?.[0]?.path ?? '/home/me/shop',
  parts,
  createdAt: '',
  lastOpenedAt: '',
  workspaces: null,
  sessionCount: 0,
  runningCount: 0,
});

const two = project([
  { id: 'pt_admin', name: 'admin', machineId: 'local', path: '/home/me/admin' },
  { id: 'pt_web', name: 'frontend', machineId: 'local', path: '/home/me/web' },
]);
const far = project([
  { id: 'pt_admin', name: 'admin', machineId: 'srv-a', path: '/srv/admin' },
  { id: 'pt_web', name: 'frontend', machineId: 'local', path: '/home/me/web' },
]);
const locked = project([
  { id: 'pt_admin', name: 'admin', machineId: 'local', path: '/home/me/admin', readOnly: true },
  { id: 'pt_web', name: 'frontend', machineId: 'local', path: '/home/me/web' },
]);
const one = project([{ id: 'pt_shop', name: 'shop', machineId: 'srv-a', path: '/srv/shop' }]);

describe('readPlace', () => {
  it('reads a project of several parts at its root, on This PC', () => {
    expect(readPlace(two, ROOT_VALUE)).toEqual({ value: ROOT_VALUE, ref: 'p_shop', root: true, machineId: 'local' });
    expect(readsWhere(readPlace(two, ROOT_VALUE))).toBe('the project root (every part)');
  });

  it('reads a picked part through its ref, on its machine', () => {
    const p = readPlace(far, 'pt_admin');
    expect(p).toMatchObject({ value: 'pt_admin', ref: 'p_shop~pt_admin', machineId: 'srv-a' });
    expect(p.root).toBeUndefined();
    expect(readsWhere(p)).toBe('/srv/admin');
  });

  it('falls back to the root for a part that went', () => {
    expect(readPlace(two, 'pt_gone').root).toBe(true);
  });

  it('keeps a project in one part as it was: its folder, the project id, never the root', () => {
    const p = readPlace(one, ROOT_VALUE);
    expect(p).toMatchObject({ value: 'pt_shop', ref: 'p_shop', machineId: 'srv-a' });
    expect(p.root).toBeUndefined();
    expect(readsWhere(p)).toBe('/srv/shop');
  });
});

describe('claudeReadsHint', () => {
  it('says Claude Code reads parts on servers when Codex is picked', () => {
    expect(claudeReadsHint(far, readPlace(far, ROOT_VALUE), 'codex')).toBe('Claude Code reads parts on servers, so this run uses Claude Code.');
    // the hub's server tools off: Codex runs as picked, nothing to say
    expect(claudeReadsHint(far, readPlace(far, ROOT_VALUE), 'codex', false)).toBe('');
    expect(claudeReadsHint(far, readPlace(far, 'pt_admin'), 'codex')).toContain('parts on servers');
    expect(claudeReadsHint(locked, readPlace(locked, ROOT_VALUE), 'codex')).toContain('read-only parts');
  });

  it('says nothing for Claude Code, parts on This PC, or a part on This PC picked', () => {
    expect(claudeReadsHint(far, readPlace(far, ROOT_VALUE), 'claude')).toBe('');
    expect(claudeReadsHint(two, readPlace(two, ROOT_VALUE), 'codex')).toBe('');
    expect(claudeReadsHint(far, readPlace(far, 'pt_web'), 'codex')).toBe('');
    expect(claudeReadsHint(locked, readPlace(locked, 'pt_admin'), 'codex')).toBe('');
  });
});

describe('versionPlace', () => {
  it('regenerates where the version was read', () => {
    expect(versionPlace({ root: true })).toBe(ROOT_VALUE);
    expect(versionPlace({ partId: 'pt_web' })).toBe('pt_web');
    expect(versionPlace({})).toBeUndefined();
    expect(versionPlace(undefined)).toBeUndefined();
  });
});
