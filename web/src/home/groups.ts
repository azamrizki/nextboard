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
/** How projects can go: as a group can, or by their badge's name (those without one last). */
export type ProjectSort = TreeSort | 'badge';
/** The name of a project's badge, for sorting by it. */
export type BadgeName = (p: Project) => string | undefined;

const nameOrder = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' });
const byName = (a: ProjectGroup, b: ProjectGroup) => nameOrder(a.name, b.name);
const openedAt = (p: Project) => Date.parse(p.lastOpenedAt) || 0;

/**
 * Projects in the given order: A to Z, the one opened last first, or by badge (badged ones A to Z
 * by the badge, then those without); A to Z on a tie.
 */
export function sortProjects(list: Project[], sort: ProjectSort, badgeName?: BadgeName): Project[] {
  const byBadge = (a: Project, b: Project) => {
    const x = badgeName?.(a);
    const y = badgeName?.(b);
    if (x === y) return 0;
    if (x === undefined) return 1;
    if (y === undefined) return -1;
    return nameOrder(x, y);
  };
  return [...list].sort((a, b) => (sort === 'recent' ? openedAt(b) - openedAt(a) : sort === 'badge' ? byBadge(a, b) : 0) || nameOrder(a.name, b.name));
}

/**
 * The groups as a tree. Groups go A to Z and projects stay in the order they were given, unless
 * `sort` says otherwise; a group with no project goes after those that have one when by recent.
 */
export function groupTree(
  groups: ProjectGroup[],
  projects: Project[],
  sort: { groups?: TreeSort; projects?: ProjectSort; badgeName?: BadgeName } = {},
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
    if (sort.projects) n.projects = sortProjects(n.projects, sort.projects, sort.badgeName);
  };
  roots.forEach((n) => finish(n, 0));
  if (sort.groups === 'recent') roots.sort(byRecent);
  return { roots, loose: sort.projects ? sortProjects(loose, sort.projects, sort.badgeName) : loose };
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
const MENU_BY = 'projects.menu.by';

/** What the project menu puts its projects under: their groups, or their badges. */
export type MenuBy = 'group' | 'badge';

/** How the project menu orders its groups and its projects, per browser: groups A to Z and
 * projects opened last first until chosen. */
export function useMenuSort() {
  const [sort, setSort] = useState(() => {
    const g = prefs.get(MENU_SORT.groups);
    const p = prefs.get(MENU_SORT.projects);
    return {
      groups: (g === 'name' || g === 'recent' ? g : 'name') as TreeSort,
      projects: (p === 'name' || p === 'recent' || p === 'badge' ? p : 'recent') as ProjectSort,
    };
  });
  const choose = (k: keyof typeof MENU_SORT, v: ProjectSort) => {
    prefs.set(MENU_SORT[k], v);
    setSort((s) => ({ ...s, [k]: v }));
  };
  return [sort, choose] as const;
}

/** Groups or badges in the project menu, per browser; groups until chosen. */
export function useMenuBy() {
  const [by, setBy] = useState<MenuBy>(() => (prefs.get(MENU_BY) === 'badge' ? 'badge' : 'group'));
  const choose = (v: MenuBy) => {
    prefs.set(MENU_BY, v);
    setBy(v);
  };
  return [by, choose] as const;
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
