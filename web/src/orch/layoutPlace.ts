// Where the groups and lanes of a layout template work when it is deployed on a project of
// several parts (docs/API-orchestration.md §13, v0.9.0-beta.2). A template saved from such a
// project remembers each lane's placement: the root, or a part by **name** (lane.root, lane.part),
// so it fits another project with parts of the same names; an editor card remembers its part the
// same way (node data.part, a name; none = the Root). Deploy matches the names (any case); a name
// this project lacks is asked for before deploying (the hub would answer 409 place_unmatched). A
// single-part project ignores all of it. Pure.

import { projectParts } from '../lib/parts';
import { ROOT_VALUE } from '../lib/placement';
import type { Project, ProjectPart } from '../lib/types';
import type { Layout, LayoutLane, Place, UnmatchedPlace } from './types';

/** A part by id, else by name in any case, ignoring surrounding spaces: Go project.PartNamed. */
export function partNamed(project: Project, s: string): ProjectPart | undefined {
  const parts = projectParts(project);
  const t = s.trim();
  if (!t) return undefined;
  return parts.find((p) => p.id === t) ?? parts.find((p) => p.name.toLowerCase() === t.toLowerCase());
}

/** A lane's saved placement on this project: a place value, '' for a part name it lacks, null when none was saved. */
export function savedPlace(lane: Pick<LayoutLane, 'root' | 'part'>, project: Project): string | null {
  if (lane.root) return ROOT_VALUE;
  if (lane.part) return partNamed(project, lane.part)?.id ?? '';
  return null;
}

export interface LayoutPlaces {
  /** By group key: a place value, or '' (a saved part this project lacks: to be picked). */
  groups: Record<string, string>;
  /** By lane key, for lanes outside a group, and grouped lanes saved somewhere else than their group. */
  lanes: Record<string, string>;
  /** By node key, for the template's editor cards: the Root, a part, or '' (a saved part this project lacks). */
  cards: Record<string, string>;
  /** The saved part names this project lacks, by group or lane key. */
  unmatched: UnmatchedPlace[];
}

/**
 * The starting placements of a template's groups and lanes on this project: the saved ones,
 * matched by part name; where nothing was saved, `fallback` (the board's default "Place in").
 */
export function initialPlaces(layout: Layout, project: Project, fallback: string): LayoutPlaces {
  const lanes = layout.lanes ?? [];
  const groupKeys = new Set((layout.groups ?? []).map((g) => g.key));
  const out: LayoutPlaces = { groups: {}, lanes: {}, cards: {}, unmatched: [] };
  const miss = (key: string, part: string | undefined) => {
    if (part && !out.unmatched.some((u) => u.key === key)) out.unmatched.push({ key, part });
  };
  for (const g of layout.groups ?? []) {
    const own = lanes.filter((l) => l.group === g.key);
    const saved = own.map((l) => savedPlace(l, project));
    // the group's: the placement most of its lanes were saved in (the first such on a tie)
    const counts = new Map<string, number>();
    for (const v of saved) if (v !== null) counts.set(v, (counts.get(v) ?? 0) + 1);
    let best: string | null = null;
    for (const [v, n] of counts) if (best === null || n > (counts.get(best) ?? 0)) best = v;
    out.groups[g.key] = best ?? fallback;
    if (best === '') miss(g.key, own.find((l) => savedPlace(l, project) === '')?.part);
    own.forEach((l, i) => {
      const v = saved[i];
      if (v === null || v === best) return;
      out.lanes[l.key] = v;
      if (v === '') miss(l.key, l.part);
    });
  }
  for (const l of lanes) {
    if (l.group && groupKeys.has(l.group)) continue;
    const v = savedPlace(l, project);
    out.lanes[l.key] = v ?? fallback;
    if (v === '') miss(l.key, l.part);
  }
  // an editor card saved in a part goes to that part's card; one saved without goes to the Root card
  for (const n of layout.nodes ?? []) {
    if (n.kind !== 'editor') continue;
    const part = typeof n.data?.part === 'string' ? n.data.part : '';
    const v = part ? (partNamed(project, part)?.id ?? '') : ROOT_VALUE;
    out.cards[n.key] = v;
    if (v === '') miss(n.key, part);
  }
  return out;
}

/** A place value as the deploy request's Place. */
export function placeOfValue(v: string): Place {
  return v === ROOT_VALUE ? { root: true } : { part: v };
}

/**
 * The deploy request's places{} for what is shown: every group, and every lane with its own.
 * Lanes added by a group size (keys the template lacks) follow their group.
 */
export function deployPlaces(groups: Record<string, string>, lanes: Record<string, string>, shown: Pick<Layout, 'lanes' | 'groups'>, cards: Record<string, string> = {}): Record<string, Place> {
  const out: Record<string, Place> = {};
  for (const [key, v] of Object.entries(cards)) if (v) out[key] = placeOfValue(v);
  for (const g of shown.groups ?? []) {
    const v = groups[g.key];
    if (v) out[g.key] = placeOfValue(v);
  }
  for (const l of shown.lanes ?? []) {
    const v = lanes[l.key];
    if (v) out[l.key] = placeOfValue(v);
  }
  return out;
}

/** The groups and lanes still to be placed (a saved part this project lacks, not picked yet). */
export function unplaced(groups: Record<string, string>, lanes: Record<string, string>, shown: Pick<Layout, 'lanes' | 'groups'>, cards: Record<string, string> = {}): string[] {
  const keys = new Set((shown.groups ?? []).map((g) => g.key));
  const out: string[] = Object.entries(cards)
    .filter(([, v]) => v === '')
    .map(([k]) => k);
  for (const g of shown.groups ?? []) if (groups[g.key] === '' && (shown.lanes ?? []).some((l) => l.group === g.key && !lanes[l.key])) out.push(g.key);
  for (const l of shown.lanes ?? []) {
    const own = lanes[l.key];
    if (own === '') out.push(l.key);
    else if (own === undefined && !(l.group && keys.has(l.group))) out.push(l.key);
  }
  return out;
}
