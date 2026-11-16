// Part memories of a project of several parts (docs/API-orchestration.md §12): the main memory
// holds what is true for the whole project, and each part has one page of its own for what holds
// there only. A project of one part has none of this and looks as it always did.

import type { ProjectPart } from '../lib/types';

/** The page of a part's memory: "part-" and the id without "pt_" (the hub's orch.PartSlug). */
export const partSlug = (partId: string) => `part-${partId.replace(/^pt_/, '')}`;

/** A part's page name, as partSlug makes them: "part-" and 10 of [a-z2-9]. */
export const isPartSlug = (name: string) => /^part-[a-z2-9]{10}$/.test(name);

/**
 * The hub keeps the names a part's page could have ("part-" and 10 of [a-z2-9]) in a project for
 * its parts' memories: a new page there cannot have one (404 part_not_found). Other names starting
 * with "part-" are ordinary pages. The memory of all projects has no parts.
 */
export const partReserved = (owner: string, name: string, partsCount = 2) => !!owner && partsCount > 1 && isPartSlug(name);

/** As ifMtime: write only a page that does not exist yet (the hub's orch.MtimeAbsent). */
export const MTIME_ABSENT = 'absent';

/** A part as the memory shows it: its page's name and where it is. */
export interface PartMemory {
  id: string;
  name: string;
  /** The machine's name now. */
  machine: string;
  path: string;
  slug: string;
}

/** The parts that have a memory each: none for a project of one part. */
export function partMemories(parts: ProjectPart[], machineName: (id: string) => string): PartMemory[] {
  if (parts.length < 2) return [];
  return parts.map((pt) => ({ id: pt.id, name: pt.name, machine: machineName(pt.machineId), path: pt.path, slug: partSlug(pt.id) }));
}

/** What the hub says a part page is for, shown before the page exists. */
export const partDescription = (p: Pick<PartMemory, 'name' | 'machine' | 'path'>) => `What holds for the ${p.name} part only (${p.machine}, ${p.path}).`;

interface DocLike {
  name: string;
  kind: string;
  critical?: boolean;
  part?: string;
}

/** The page of each current part (or none yet), and the pages left over that are not one. */
export function splitPartDocs<D extends DocLike>(docs: D[], parts: PartMemory[]): { parts: { part: PartMemory; doc?: D }[]; pages: D[] } {
  const subs = docs.filter((d) => d.kind === 'sub' && !d.critical);
  const used = new Set<D>();
  const rows = parts.map((part) => {
    const doc = subs.find((d) => d.part === part.id) ?? subs.find((d) => !d.part && d.name === part.slug);
    if (doc) used.add(doc);
    return { part, doc };
  });
  // a page of a part that is gone (or of a hub that has not cleared it yet) is one more page
  return { parts: rows, pages: subs.filter((d) => !used.has(d)) };
}

export interface FactGroup<F> {
  /** "" for the whole project. */
  part: string;
  label: string;
  items: { fact: F; index: number }[];
}

/**
 * Facts under "Whole project" and then each part's name, in the parts' order; a fact for a part
 * that is no longer there goes to the whole project. Groups without facts are left out, and a
 * project of one part has one group.
 */
export function groupFacts<F extends { part?: string }>(facts: F[], parts: Pick<PartMemory, 'id' | 'name'>[]): FactGroup<F>[] {
  const groups: FactGroup<F>[] = [{ part: '', label: 'Whole project', items: [] }, ...parts.map((p) => ({ part: p.id, label: p.name, items: [] }))];
  facts.forEach((fact, index) => {
    const g = (fact.part && groups.find((x) => x.part === fact.part)) || groups[0];
    g.items.push({ fact, index });
  });
  return groups.filter((g) => g.items.length > 0);
}

/**
 * Facts as they are accepted: each keeps its part only when that part is a current part, so a
 * fact of a part removed since the run goes to the whole project. A fact for the whole project
 * has no part at all, as before parts.
 */
export function routedFacts<F extends { part?: string }>(facts: F[], parts: Pick<PartMemory, 'id'>[]): F[] {
  const ids = new Set(parts.map((p) => p.id));
  return facts.map((f) => {
    const { part, ...rest } = f;
    return part && ids.has(part) ? f : (rest as F);
  });
}

/** "2 to the whole project, 3 to Frontend": where accepted facts went. */
export function factsWhere<F extends { part?: string }>(facts: F[], parts: Pick<PartMemory, 'id' | 'name'>[]): string {
  return groupFacts(facts, parts)
    .map((g) => `${g.items.length} to ${g.part ? g.label : 'the whole project'}`)
    .join(', ');
}
