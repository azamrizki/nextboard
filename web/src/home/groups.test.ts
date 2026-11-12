import { describe, expect, it } from 'vitest';
import type { Project, ProjectGroup } from '../lib/types';
import { flatGroups, groupTree } from './groups';

const group = (id: string, name: string, parentId?: string): ProjectGroup => ({ id, name, parentId, createdAt: '2026-01-01T00:00:00Z' });
const project = (name: string, groupId: string | undefined, day: number): Project =>
  ({ id: name, name, machineId: 'local', path: `/srv/${name}`, groupId, createdAt: '2026-01-01T00:00:00Z', lastOpenedAt: `2026-01-${String(day).padStart(2, '0')}T00:00:00Z` }) as Project;

const groups = [group('g_a', 'Acme'), group('g_g', 'Globex'), group('g_u', 'Umbrella'), group('g_gi', 'Initech', 'g_g'), group('g_gz', 'Zeta', 'g_g')];
// as useProjects gives them: the one opened last first
const projects = [
  project('Portal', 'g_gz', 9),
  project('api 10', 'g_a', 8),
  project('Loose b', undefined, 7),
  project('api 9', 'g_a', 6),
  project('Billing', 'g_gi', 5),
  project('Loose a', undefined, 4),
  project('Admin', 'g_g', 3),
];
const names = (r: ReturnType<typeof groupTree>) => flatGroups(r.roots).map((n) => `${n.group.name}: ${n.projects.map((p) => p.name).join(', ')}`);

describe('the tree of project groups, sorted', () => {
  it('keeps groups A to Z and projects as given by default', () => {
    const t = groupTree(groups, projects);
    expect(names(t)).toEqual(['Acme: api 10, api 9', 'Globex: Admin', 'Initech: Billing', 'Zeta: Portal', 'Umbrella: ']);
    expect(t.loose.map((p) => p.name)).toEqual(['Loose b', 'Loose a']);
  });

  it('orders groups by the project in them, or below them, opened last; empty ones last', () => {
    const t = groupTree(groups, projects, { groups: 'recent' });
    expect(names(t)).toEqual(['Globex: Admin', 'Zeta: Portal', 'Initech: Billing', 'Acme: api 10, api 9', 'Umbrella: ']);
    expect(flatGroups(t.roots).find((n) => n.group.name === 'Globex')?.lastOpened).toBe(Date.parse('2026-01-09T00:00:00Z'));
  });

  it('orders projects A to Z with numbers in order, loose ones too', () => {
    const t = groupTree(groups, projects, { groups: 'name', projects: 'name' });
    expect(names(t)[0]).toBe('Acme: api 9, api 10');
    expect(t.loose.map((p) => p.name)).toEqual(['Loose a', 'Loose b']);
  });

  it('orders projects opened last first whatever order they came in', () => {
    const t = groupTree(groups, [...projects].reverse(), { projects: 'recent' });
    expect(names(t)[0]).toBe('Acme: api 10, api 9');
    expect(t.loose.map((p) => p.name)).toEqual(['Loose b', 'Loose a']);
  });
});
