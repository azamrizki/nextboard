// "Place in: Root (all parts) / <part>…" (v0.9.0-beta.2, docs/API-board.md §1): where a new
// console, group, editor card or tile works in a project of several parts. Every place that makes
// one shows it; a single-part project never does (the component renders nothing). Up to four
// choices it is a segmented control (DESIGN.md §3.2), more a select. The machine and folder of each
// part are in its tooltip; a read-only part says so.

import { useId, useState } from 'react';
import { SegmentedControl, type SegmentOption } from '../components/form';
import { Icon } from '../components/Icon';
import { isRoot, partRef, projectParts } from '../lib/parts';
import { isMultiPart, lastPlacement, ROOT_VALUE } from '../lib/placement';
import type { Machine, Project, ProjectPart } from '../lib/types';
import { useMachines } from '../store';

/** "Another folder…": a console that names its own machine and folder (only where offered). */
export const OTHER_VALUE = '@other';

const ROOT_TITLE = 'Root (all parts): on This PC, in a folder with one entry per part. It reaches every part.';

function machineName(machines: Machine[], id: string) {
  return id === 'local' || !id ? 'This PC' : (machines.find((m) => m.id === id)?.name ?? id);
}

export function partTitle(part: ProjectPart, machines: Machine[]) {
  return `${part.name}: ${part.path} on ${machineName(machines, part.machineId)}${part.readOnly ? ' (read only: agents there only read)' : ''}`;
}

/** One line under the picker on what the choice means. */
export function placeHint(project: Project, value: string, machines: Machine[]): string {
  if (value === ROOT_VALUE) return 'On This PC, in a folder with one entry per part: it reaches every part.';
  if (value === OTHER_VALUE) return 'Any machine and folder, outside the parts.';
  const pt = projectParts(project).find((p) => p.id === value);
  return pt ? `${pt.path} on ${machineName(machines, pt.machineId)}${pt.readOnly ? ' · read only' : ''}` : '';
}

interface Props {
  project: Project;
  /** placeValue(placement), or OTHER_VALUE. */
  value: string;
  onChange: (value: string) => void;
  /** Offer "Another folder…" last (the new console form). */
  other?: boolean;
  /** Parts only, no Root (a folder browser: the root is no folder of its own). */
  noRoot?: boolean;
  /** A visible label, default "Place in"; null: none (aria-label only). */
  label?: string | null;
  size?: 'sm' | 'md';
  /** Show the hint line under the control. */
  hint?: boolean;
  /** Ask the segmented control to fill the width. */
  block?: boolean;
  className?: string;
  disabled?: boolean;
}

/** Renders nothing in a single-part project. */
export function PlacePicker({ project, value, onChange, other, noRoot, label = 'Place in', size = 'sm', hint = true, block = true, className, disabled }: Props) {
  const machines = useMachines();
  const id = useId();
  if (!isMultiPart(project)) return null;
  const parts = projectParts(project);
  const options: SegmentOption<string>[] = [
    ...(noRoot ? [] : [{ value: ROOT_VALUE, label: 'Root', title: ROOT_TITLE }]),
    ...parts.map((p) => ({
      value: p.id,
      label: (
        <>
          {p.readOnly && <Icon name="lock" />}
          <span className="truncate">{p.name}</span>
        </>
      ),
      title: partTitle(p, machines),
    })),
    ...(other ? [{ value: OTHER_VALUE, label: 'Other…', title: 'Another machine or folder, outside the parts' }] : []),
  ];
  const text = (o: string) => (o === ROOT_VALUE ? 'Root (all parts)' : o === OTHER_VALUE ? 'Another folder…' : '');
  const chars = parts.reduce((n, p) => n + p.name.length, 0);
  // DESIGN.md §3.2: a choice of 2–4 is a segmented control; longer names or more parts a select
  const segmented = options.length <= 4 && chars <= 28;
  const hintText = hint ? placeHint(project, value, machines) : '';
  const labelId = `${id}-label`;
  return (
    <div className={`field place-picker ${className ?? ''}`}>
      {label !== null && (
        <span className="field-label" id={labelId}>
          {label}
        </span>
      )}
      {segmented ? (
        <SegmentedControl
          value={value}
          onChange={onChange}
          options={options}
          size={size}
          block={block}
          disabled={disabled}
          className="place-seg"
          {...(label !== null ? { labelledBy: labelId } : { label: 'Place in' })}
        />
      ) : (
        <select
          className={`control ${size === 'sm' ? 'control-sm' : ''}`}
          value={value}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
          {...(label !== null ? { 'aria-labelledby': labelId } : { 'aria-label': 'Place in' })}
        >
          {!noRoot && <option value={ROOT_VALUE}>{text(ROOT_VALUE)}</option>}
          {parts.map((p) => (
            <option key={p.id} value={p.id} title={partTitle(p, machines)}>
              {p.name} · {machineName(machines, p.machineId)}
              {p.readOnly ? ' (read only)' : ''}
            </option>
          ))}
          {other && <option value={OTHER_VALUE}>{text(OTHER_VALUE)}</option>}
        </select>
      )}
      {hintText && <span className="field-hint truncate" title={hintText}>{hintText}</span>}
    </div>
  );
}

/**
 * One part of the project to work in, for what works in a part's folder (the Markdown studio's
 * vault; Documents and Diagrams read at the root by default, studio/readPlace.ts): the part last
 * placed in, else one on This PC, else the first shown; picked with
 * <PlacePicker noRoot>. `ref` is the project ref the hub reads that part through: the project id
 * in a single-part project (as before), "<project>~<part>" otherwise. `initial`: a part id to start
 * in (the part of a card's file), when the project has it.
 */
export function usePartChoice(project: Project, initial?: string) {
  const parts = projectParts(project);
  const [picked, setPicked] = useState(() => {
    const last = lastPlacement(project);
    return (
      parts.find((p) => !!initial && p.id === initial) ??
      parts.find((p) => last && !isRoot(last) && p.id === last.partId) ??
      parts.find((p) => (p.machineId || 'local') === 'local') ??
      parts[0]
    ).id;
  });
  const part = parts.find((p) => p.id === picked) ?? parts[0];
  return { part, setPartId: setPicked, ref: parts.length > 1 ? partRef(project, part) : project.id };
}
