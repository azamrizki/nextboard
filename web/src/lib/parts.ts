// The parts of a project (docs/API-board.md §1, ROADMAP #49): the folders it spans on one machine
// or several. A hub from before parts sends none; its project is one part.

import { cleanAbs, fileKey, LOCAL_MACHINE, under, type Mount } from '../studio/code/paths';
import type { Project, ProjectPart, Session } from './types';

/** The most parts a project has (the hub's project.MaxParts). */
export const MAX_PARTS = 8;

export function projectParts(p: Project): ProjectPart[] {
  return p.parts?.length ? p.parts : [{ id: 'main', name: 'Main', machineId: p.machineId, path: p.path }];
}

/** The first part on a machine: where a terminal there opens by default. */
export function partOn(p: Project, machineId: string): ProjectPart | undefined {
  return projectParts(p).find((pt) => pt.machineId === machineId);
}

/** "Frontend, Docs": the parts on a machine, for a project with more than one part. */
export function partNamesOn(p: Project, machineId: string): string {
  const parts = projectParts(p);
  return parts.length > 1
    ? parts
        .filter((pt) => pt.machineId === machineId)
        .map((pt) => pt.name)
        .join(', ')
    : '';
}

/** The part whose folder holds an absolute path: the deepest one. */
export function partHolding(p: Project, path: string): ProjectPart | undefined {
  let best: ProjectPart | undefined;
  for (const pt of projectParts(p)) {
    const root = pt.path.replace(/\/+$/, '');
    if ((path === root || path.startsWith(`${root}/`) || root === '') && (!best || root.length > best.path.replace(/\/+$/, '').length)) best = pt;
  }
  return best;
}

// ---- placement and part refs (v0.9.0-beta.2) ---------------------------------------------------
// All parts are equal: nothing is keyed or placed by "the first part". A console or an editor card
// works at the project root (every part) or in one part.

/**
 * Where a console or an editor card works in a project (docs/API-board.md §1): at the root,
 * reaching every part, or in one part (its id). On the wire a session says root: true or partId;
 * an editor node's data.part is a part id, "" or absent for the root.
 */
export type Placement = { root: true } | { partId: string };

/** The root placement. */
export const ROOT = Object.freeze({ root: true }) as Placement;

export const isRoot = (p: Placement): p is { root: true } => 'root' in p;

export const samePlacement = (a: Placement, b: Placement) => (isRoot(a) ? isRoot(b) : !isRoot(b) && a.partId === b.partId);

/**
 * The hub's ref and key of a part: "<project>~<part>" for every part, the first one included
 * (Go project.PartKey). Workbench API calls, review and Local History, git state and gitPart
 * events, and the studio store's per-part state (reviewRev, git) all use it.
 */
export const refOf = (projectId: string, partId: string) => `${projectId}~${partId}`;

/** refOf for a project and one of its parts. */
export const partRef = (project: Pick<Project, 'id'>, part: Pick<ProjectPart, 'id'>) => refOf(project.id, part.id);

/** A ref split: "<project>~<part>", or a bare project id (partId ""; the hub reads it as its first part, for old clients). */
export function splitRef(ref: string): { projectId: string; partId: string } {
  const i = ref.indexOf('~');
  return i < 0 ? { projectId: ref, partId: '' } : { projectId: ref.slice(0, i), partId: ref.slice(i + 1) };
}

/**
 * A placement made valid for the project, as the hub's ResolvePlace does: a part the project no
 * longer has is the root, and a single-part project's root is its one part.
 */
export function normalPlacement(project: Project, p: Placement): Placement {
  const parts = projectParts(project);
  if (parts.length === 1) return { partId: parts[0].id };
  return !isRoot(p) && parts.some((pt) => pt.id === p.partId) ? p : ROOT;
}

/** The placement of an editor node from its data.part: a part id, else (absent, "", unknown) the root. */
export function placementOf(data: { part?: unknown } | null | undefined, project: Project): Placement {
  const part = typeof data?.part === 'string' ? data.part : '';
  return normalPlacement(project, part ? { partId: part } : ROOT);
}

/** Where a session works: the root, its part, or null for none (a console outside every part's folder). */
export function placementOfSession(s: Pick<Session, 'root' | 'partId'>, project: Project): Placement | null {
  if (s.root) return normalPlacement(project, ROOT);
  return s.partId && projectParts(project).some((pt) => pt.id === s.partId) ? { partId: s.partId } : null;
}

const machineOf = (id: string) => id || LOCAL_MACHINE;

/** The part on machineId whose folder holds abs, the deepest one: partHolding that also checks the machine. */
export function partHoldingOn(project: Project, machineId: string, abs: string): ProjectPart | undefined {
  let best: ProjectPart | undefined;
  let bestLen = -1;
  for (const pt of projectParts(project)) {
    const root = cleanAbs(pt.path);
    if (root === null || machineOf(pt.machineId) !== machineOf(machineId) || !under(abs, root)) continue;
    if (root.length > bestLen) [best, bestLen] = [pt, root.length];
  }
  return best;
}

/** One part as an editor view mounts it. */
export function mountOfPart(project: Pick<Project, 'id'>, part: ProjectPart): Mount {
  const root = cleanAbs(part.path) ?? '/';
  return { partId: part.id, name: part.name, machineId: machineOf(part.machineId), root, rootKey: fileKey(part.machineId, root), ref: partRef(project, part), readOnly: part.readOnly };
}

/**
 * The mounts of an editor card's view and its mode: the Root card of a multi-part project mounts
 * every part in part order (multi, the "/<part>/…" tree); any other card mounts its one part.
 * new ViewPaths(mounts, multi) makes its paths.
 */
export function mountsOf(project: Project, placement: Placement): { mounts: Mount[]; multi: boolean } {
  const parts = projectParts(project);
  const p = normalPlacement(project, placement);
  if (isRoot(p)) return { mounts: parts.map((pt) => mountOfPart(project, pt)), multi: true };
  return { mounts: [mountOfPart(project, parts.find((pt) => pt.id === p.partId) ?? parts[0])], multi: false };
}
