// Where a Documents or Diagrams run reads in a project of several parts (docs/API-studio.md §5,
// §6): the project root by default, every part (the CLI on This PC, the parts on This PC through
// the root's links, the others through the hub's read tools), or one part picked with
// <PlacePicker label="Read in">. A project in one part reads its folder, as it always did.

import { useState } from 'react';
import { partRef, projectParts } from '../lib/parts';
import { ROOT_VALUE } from '../lib/placement';
import type { Project, ProjectPart } from '../lib/types';

export interface ReadPlace {
  /** The PlacePicker value: ROOT_VALUE, or the id of the part read. */
  value: string;
  /** The project ref the request goes to: the project id, or "<project>~<part>" for a part. */
  ref: string;
  /** The request's root field: set for a root run. */
  root?: true;
  /** The part read; none at the root. */
  part?: ProjectPart;
  /** The machine the CLI is picked on (and its run times kept for): This PC for the root. */
  machineId: string;
}

const onServer = (p: ProjectPart) => (p.machineId || 'local') !== 'local';

/** A picked value made valid for the project: a part it no longer has is the root again. */
export function readPlace(project: Project, value: string): ReadPlace {
  const parts = projectParts(project);
  if (parts.length < 2) {
    const pt = parts[0];
    return { value: pt.id, ref: project.id, part: pt, machineId: pt.machineId || 'local' };
  }
  const pt = value === ROOT_VALUE ? undefined : parts.find((p) => p.id === value);
  if (!pt) return { value: ROOT_VALUE, ref: project.id, root: true, machineId: 'local' };
  return { value: pt.id, ref: partRef(project, pt), part: pt, machineId: pt.machineId || 'local' };
}

/** Where the run reads, for the line under the generator. */
export const readsWhere = (place: ReadPlace) => (place.root ? 'the project root (every part)' : (place.part?.path ?? ''));

/**
 * Codex picked, but what it would read goes through the hub's tools, which need Claude Code: the
 * hub runs Claude Code instead (a part on a server, or at the root a read-only part). "" otherwise.
 */
export function claudeReadsHint(project: Project, place: ReadPlace, agent: string, hubTools = true): string {
  // with the hub's server tools off, nothing switches: the run reads what it can reach
  if (agent !== 'codex' || !hubTools) return '';
  const parts = place.root ? projectParts(project) : place.part ? [place.part] : [];
  const server = parts.some(onServer);
  const readOnly = !!place.root && parts.some((p) => p.readOnly && !onServer(p));
  if (!server && !readOnly) return '';
  const what = server && readOnly ? 'parts on servers and read-only parts' : server ? 'parts on servers' : 'read-only parts';
  return `Claude Code reads ${what}, so this run uses Claude Code.`;
}

/** The place a document version was read in, to regenerate it there; undefined: the default. */
export function versionPlace(v: { root?: boolean; partId?: string } | undefined): string | undefined {
  if (v?.root) return ROOT_VALUE;
  return v?.partId || undefined;
}

/** The place picked in a generator: the root unless `initial` names a part (or the root). */
export function useReadPlace(project: Project, initial?: string) {
  const [value, setValue] = useState(initial ?? ROOT_VALUE);
  return { ...readPlace(project, value), setValue };
}
