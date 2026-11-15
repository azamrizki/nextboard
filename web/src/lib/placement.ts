// Where a new console, group, editor card or tile goes in a project of several parts
// (v0.9.0-beta.2, docs/API-board.md §1): "Place in: Root (all parts) / <part>…". A single-part
// project never asks: its root is its one part (lib/parts.ts normalPlacement).
//
// The default is the placement of the focused tile (focus mode, else the selection), else the
// one used last in this browser for the project, else the root. Pure, apart from the last-used
// pref; the picker is board/PlacePicker.tsx.

import { prefs } from './format';
import { isRoot, normalPlacement, partHoldingOn, placementOf, placementOfSession, projectParts, ROOT, type Placement } from './parts';
import type { BoardNode, CreateSession, Project, ProjectPart, Session } from './types';

/** More than one part: the pickers show, and placement matters. */
export const isMultiPart = (p: Project) => projectParts(p).length > 1;

/** A placement as one string, for a <select>, a radio group or a pref: "@root" or the part id. */
export const ROOT_VALUE = '@root';
export const placeValue = (p: Placement): string => (isRoot(p) ? ROOT_VALUE : p.partId);
export const placeFromValue = (v: string): Placement => (v === ROOT_VALUE || !v ? ROOT : { partId: v });

/** The part a placement names, if the project has it. */
export function partOfPlace(project: Project, p: Placement | null | undefined): ProjectPart | undefined {
  if (!p || isRoot(p)) return undefined;
  return projectParts(project).find((pt) => pt.id === p.partId);
}

/** "Root (all parts)" or the part's name. */
export function placeLabel(project: Project, p: Placement): string {
  return partOfPlace(project, p)?.name ?? 'Root (all parts)';
}

/** "Root" or the part's name: chips and short labels. */
export function placeShort(project: Project, p: Placement): string {
  return partOfPlace(project, p)?.name ?? 'Root';
}

/**
 * The machine a placement works on: the root is on This PC, a part on its machine. Agents of a
 * server part still run on This PC (the hub decides); this is where the work is.
 */
export function machineOfPlace(project: Project, p: Placement): string {
  return partOfPlace(project, p)?.machineId || 'local';
}

// ---- the wire forms (contracts.md §1.3) ----

/** POST /api/sessions: root: true, or partId. */
export function sessionPlace(p: Placement): Pick<CreateSession, 'root' | 'partId'> {
  return isRoot(p) ? { root: true } : { partId: p.partId };
}

/** Orchestration (spawn and slots, handoff, the command's place, deploy places): root: true, or part. */
export function orchPlace(p: Placement): { root?: true; part?: string } {
  return isRoot(p) ? { root: true } : { part: p.partId };
}

// ---- last used (this browser, per project) ----

const PREF = (projectId: string) => `place.${projectId}`;

/** The placement used last for this project here, if it still fits the project. */
export function lastPlacement(project: Project): Placement | null {
  if (!isMultiPart(project)) return null;
  const v = prefs.get(PREF(project.id));
  if (!v) return null;
  const p = placeFromValue(v);
  // a part that went: nothing remembered (the default applies), not silently the root
  return isRoot(p) || partOfPlace(project, p) ? p : null;
}

export function rememberPlacement(project: Project, p: Placement): void {
  if (isMultiPart(project)) prefs.set(PREF(project.id), placeValue(p));
}

// ---- the placement of a board tile ----

type Sessions = Record<string, Pick<Session, 'root' | 'partId'> | undefined>;

/**
 * Where a tile works, or null when it says nothing (a note, a shape): a console by its session,
 * an editor card by data.part, a file or folder card by the part holding its path on its machine,
 * a web tile or a branches card by data.partId, a group when all its members agree.
 */
export function placementOfNode(node: BoardNode | null | undefined, project: Project, sessions: Sessions, nodes: BoardNode[] = []): Placement | null {
  if (!node) return null;
  const d = node.data ?? {};
  switch (node.kind) {
    case 'terminal': {
      const s = typeof d.sessionId === 'string' ? sessions[d.sessionId] : undefined;
      return s ? placementOfSession(s, project) : null;
    }
    case 'editor':
      return placementOf(d, project);
    case 'file': {
      const path = typeof d.path === 'string' ? d.path : '';
      const machineId = typeof d.machineId === 'string' && d.machineId ? d.machineId : 'local';
      const pt = path ? partHoldingOn(project, machineId, path) : undefined;
      return pt ? { partId: pt.id } : null;
    }
    case 'web':
    case 'branches':
    case 'app': {
      const id = typeof d.partId === 'string' ? d.partId : '';
      return id && projectParts(project).some((pt) => pt.id === id) ? { partId: id } : null;
    }
    case 'group': {
      const members = Array.isArray(d.members) ? d.members.filter((m): m is string => typeof m === 'string') : [];
      return commonPlacement(
        members.map((id) => nodes.find((n) => n.id === id)),
        project,
        sessions,
      );
    }
    default:
      return null;
  }
}

/** The placement every one of these tiles shares, or null when they differ or say nothing. */
export function commonPlacement(list: (BoardNode | undefined)[], project: Project, sessions: Sessions): Placement | null {
  let out: Placement | null = null;
  for (const n of list) {
    if (!n || n.kind === 'group') continue;
    const p = placementOfNode(n, project, sessions);
    if (!p) continue;
    if (!out) out = p;
    else if (placeValue(out) !== placeValue(p)) return null;
  }
  return out;
}

/** The focused tile's placement: the focus-mode card, else the first selected tile that has one. */
export function focusedPlacement(project: Project, nodes: BoardNode[], selection: Iterable<string>, focusId: string | null, sessions: Sessions): Placement | null {
  if (focusId) {
    const p = placementOfNode(
      nodes.find((n) => n.id === focusId),
      project,
      sessions,
      nodes,
    );
    if (p) return p;
  }
  for (const id of selection) {
    const p = placementOfNode(
      nodes.find((n) => n.id === id),
      project,
      sessions,
      nodes,
    );
    if (p) return p;
  }
  return null;
}

/**
 * The default "Place in": the focused tile's placement, else the last used, else the root, made
 * valid for the project (a single-part project gets its one part, a gone part the root).
 */
export function defaultPlacement(project: Project, focused: Placement | null, last: Placement | null): Placement {
  return normalPlacement(project, focused ?? last ?? ROOT);
}
