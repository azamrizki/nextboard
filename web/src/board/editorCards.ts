// Editor cards by placement (v0.9.0-beta.2, docs/API-studio.md §7.7): a board may hold several
// Code editor cards, each at the project Root (one tree of every part) or in one part (data.part,
// a part id; absent, "" or a part that went = Root). Which card opens a file, a commit or a pane,
// and what a new card is placed in. A single-part project has one placement, so any editor card is
// "the" card, as before. Pure.

import { isRoot, normalPlacement, partHolding, partHoldingOn, placementOf, projectParts, ROOT, samePlacement, type Placement } from '../lib/parts';
import type { BoardNode, Project, ProjectPart } from '../lib/types';

/** A file to open in an editor card: absolute on its machine (none named: found from its part or path). */
export interface CodeFile {
  machineId?: string;
  partId?: string;
  path: string;
  line?: number;
}

export const isEditor = (n: BoardNode) => n.kind === 'editor';

/** The placement of an editor card. */
export const cardPlacement = (n: BoardNode, project: Project): Placement => placementOf(n.data, project);

/** An editor node's data for a placement: data.part in a project of several parts, nothing in one part. */
export function editorData(project: Project, p: Placement): Record<string, unknown> {
  const q = normalPlacement(project, p);
  return projectParts(project).length > 1 && !isRoot(q) ? { part: q.partId } : {};
}

/** The first editor card of the board with this placement; with rootOk, else the first Root card. */
export function cardFor(nodes: BoardNode[], project: Project, p: Placement, rootOk = false): BoardNode | undefined {
  const want = normalPlacement(project, p);
  const cards = nodes.filter(isEditor);
  return cards.find((n) => samePlacement(cardPlacement(n, project), want)) ?? (rootOk ? cards.find((n) => isRoot(cardPlacement(n, project))) : undefined);
}

/**
 * The part a file belongs to: the one named, if the project has it; else the part holding the path
 * on its machine; a sender that names no machine (an older one) gets the part holding the path on
 * any machine, the deepest.
 */
export function partOfFile(project: Project, f: CodeFile): ProjectPart | undefined {
  const parts = projectParts(project);
  if (f.partId) {
    const named = parts.find((p) => p.id === f.partId);
    if (named) return named;
  }
  return f.machineId ? partHoldingOn(project, f.machineId, f.path) : partHolding(project, f.path);
}

/**
 * Where a file opens: the card of its part, else a Root card (it shows every part), else none, and
 * then a new card goes in its part (or the Root for a file outside every part). No question: the
 * file decides.
 */
export function cardForFile(nodes: BoardNode[], project: Project, f: CodeFile): { card?: BoardNode; placement: Placement } {
  const part = partOfFile(project, f);
  const placement = normalPlacement(project, part ? { partId: part.id } : ROOT);
  return { card: cardFor(nodes, project, placement, true), placement };
}

/** "Code editor · admin" / "Code editor · All parts": the card label in a project of several parts. */
export function cardLabel(project: Project, p: Placement): string {
  if (projectParts(project).length < 2) return 'Code editor';
  const q = normalPlacement(project, p);
  return isRoot(q) ? 'Code editor · All parts' : `Code editor · ${projectParts(project).find((pt) => pt.id === q.partId)?.name ?? ''}`;
}
