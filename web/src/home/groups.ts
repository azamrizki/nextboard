// The tree of project groups (docs/API-board.md §2), as Home and the project menu show it.
// A project with no group, or one the tree no longer has, is loose: it lists under All projects.

import { useState } from 'react';
import { prefs } from '../lib/format';
import type { Project, ProjectGroup } from '../lib/types';

export interface GroupNode {
  group: ProjectGroup;
  depth: number;
  children: GroupNode[];
  /** In the order they were given, unless the tree sorts them. */
  projects: Project[];
  /** This group and everything below it. */
  total: number;
  /** When a project in it or below it was last opened (ms); 0 when it has none. */
  lastOpened: number;
}

/** "name": A to Z. "recent": the one opened last first; a group by the project in it opened last. */
export type TreeSort = 'name' | 'recent';

const nameOrder = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' });
const byName = (a: ProjectGroup, b: ProjectGroup) => nameOrder(a.name, b.name);
const openedAt = (p: Project) => Date.parse(p.lastOpenedAt) || 0;

/** Projects in the given order: A to Z, or the one opened last first (A to Z on a tie). */
export function sortProjects(list: Project[], sort: TreeSort): Project[] {
  return [...list].sort((a, b) => (sort === 'recent' ? openedAt(b) - openedAt(a) : 0) || nameOrder(a.name, b.name));
}

/**
 * The groups as a tree. Groups go A to Z and projects stay in the order they were given, unless
 * `sort` says otherwise; a group with no project goes after those that have one when by recent.
 */
export function groupTree(
  groups: ProjectGroup[],
  projects: Project[],
  sort: { groups?: TreeSort; projects?: TreeSort } = {},
): { roots: GroupNode[]; loose: Project[] } {
  const nodes = new Map<string, GroupNode>(groups.map((group) => [group.id, { group, depth: 0, children: [], projects: [], total: 0, lastOpened: 0 }]));
  const roots: GroupNode[] = [];
  for (const g of [...groups].sort(byName)) {
    const node = nodes.get(g.id)!;
    const parent = g.parentId ? nodes.get(g.parentId) : undefined;
    if (parent) parent.children.push(node);
    else roots.push(node);
  }
  const loose: Project[] = [];
  for (const p of projects) {
    const node = p.groupId ? nodes.get(p.groupId) : undefined;
    if (node) node.projects.push(p);
    else loose.push(p);
  }
  const byRecent = (a: GroupNode, b: GroupNode) => b.lastOpened - a.lastOpened || byName(a.group, b.group);
  const finish = (n: GroupNode, depth: number) => {
    n.depth = depth;
    n.total = n.projects.length;
    n.lastOpened = Math.max(0, ...n.projects.map(openedAt));
    for (const c of n.children) {
      finish(c, depth + 1);
      n.total += c.total;
      n.lastOpened = Math.max(n.lastOpened, c.lastOpened);
    }
    if (sort.groups === 'recent') n.children.sort(byRecent);
    if (sort.projects) n.projects = sortProjects(n.projects, sort.projects);
  };
  roots.forEach((n) => finish(n, 0));
  if (sort.groups === 'recent') roots.sort(byRecent);
  return { roots, loose: sort.projects ? sortProjects(loose, sort.projects) : loose };
}

/** The tree from top to bottom, a group before what is inside it. */
export function flatGroups(nodes: GroupNode[]): GroupNode[] {
  return nodes.flatMap((n) => [n, ...flatGroups(n.children)]);
}

/** Only the groups that hold a project somewhere, for a filtered list. */
export function withProjects(nodes: GroupNode[]): GroupNode[] {
  return nodes.filter((n) => n.total > 0).map((n) => ({ ...n, children: withProjects(n.children) }));
}

/** The group and the ones above it, inner first. */
export function groupChain(groups: ProjectGroup[], id: string | undefined): ProjectGroup[] {
  const chain: ProjectGroup[] = [];
  let at = id ? groups.find((g) => g.id === id) : undefined;
  while (at && chain.length <= groups.length) {
    chain.push(at);
    const parent: string | undefined = at.parentId;
    at = parent ? groups.find((g) => g.id === parent) : undefined;
  }
  return chain;
}

/** "Clients / Globex" */
export function groupPath(groups: ProjectGroup[], id: string | undefined): string {
  return groupChain(groups, id)
    .reverse()
    .map((g) => g.name)
    .join(' / ');
}

const COLLAPSED = 'projects.collapsed';
const MENU_SORT = { groups: 'projects.menu.groups', projects: 'projects.menu.projects' } as const;

/** How the project menu orders its groups and its projects, per browser: groups A to Z and
 * projects opened last first until chosen. */
export function useMenuSort() {
  const saved = (k: keyof typeof MENU_SORT, fallback: TreeSort): TreeSort => {
    const v = prefs.get(MENU_SORT[k]);
    return v === 'name' || v === 'recent' ? v : fallback;
  };
  const [sort, setSort] = useState(() => ({ groups: saved('groups', 'name'), projects: saved('projects', 'recent') }));
  const choose = (k: keyof typeof MENU_SORT, v: TreeSort) => {
    prefs.set(MENU_SORT[k], v);
    setSort((s) => ({ ...s, [k]: v }));
  };
  return [sort, choose] as const;
}

function savedCollapsed(): Set<string> {
  try {
    const list: unknown = JSON.parse(prefs.get(COLLAPSED) ?? '[]');
    return new Set(Array.isArray(list) ? list.filter((x): x is string => typeof x === 'string') : []);
  } catch {
    return new Set();
  }
}

/** Which groups are closed, per browser. `keepOpen` starts open whatever was saved. */
export function useCollapsedGroups(keepOpen: string[] = []) {
  const [collapsed, setCollapsed] = useState(() => {
    const saved = savedCollapsed();
    for (const id of keepOpen) saved.delete(id);
    return saved;
  });
  const toggle = (id: string) => {
    const next = new Set(collapsed);
    if (!next.delete(id)) next.add(id);
    prefs.set(COLLAPSED, JSON.stringify([...next]));
    setCollapsed(next);
  };
  return [collapsed, toggle] as const;
}

/** Every tag in use with how many projects have it. Spelling differences count as one. */
export function allTags(projects: Project[]): { tag: string; count: number }[] {
  const seen = new Map<string, { tag: string; count: number }>();
  for (const p of projects) {
    for (const tag of p.tags ?? []) {
      const key = tag.toLowerCase();
      const row = seen.get(key);
      if (row) row.count++;
      else seen.set(key, { tag, count: 1 });
    }
  }
  return [...seen.values()].sort((a, b) => a.tag.localeCompare(b.tag, undefined, { sensitivity: 'base' }));
}

export const hasTag = (p: Project, tag: string) => (p.tags ?? []).some((t) => t.toLowerCase() === tag.toLowerCase());
