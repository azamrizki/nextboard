// The Archived view of Home (DESIGN.md §3.11): archived projects in one glass panel, each with
// Restore and Delete permanently, and the same select mode as the active ones (§3.18).

import { useState, type MouseEvent as ReactMouseEvent } from 'react';
import { Button } from '../components/Button';
import { SelectBox, type SelectMode } from '../components/SelectBar';
import { toast } from '../components/Toast';
import { formatDateTime, timeAgo } from '../lib/format';
import type { Project } from '../lib/types';
import { useMachines } from '../store';
import { projectParts } from '../lib/parts';
import { FolderPath } from './FolderPath';
import { partsSummary } from './partsCard';
import { ProjectIcon } from './ProjectArt';
import { restoreProjects } from './RemoveDialog';

interface ArchivedListProps {
  projects: Project[];
  /** A filter hides the others. */
  filtering?: boolean;
  select: SelectMode;
  onDelete: (list: Project[]) => void;
}

function ArchivedRow({ p, select, onDelete }: { p: Project; select: SelectMode; onDelete: (list: Project[]) => void }) {
  const machines = useMachines();
  const [busy, setBusy] = useState(false);
  const selecting = select.on;
  const selected = select.has(p.id);
  const machine = machines.find((m) => m.id === p.machineId)?.name ?? p.machineId;

  const restore = async () => {
    setBusy(true);
    const out = await restoreProjects([p]).catch((err: unknown) => ({ done: [], failed: [{ id: p.id, name: p.name, error: String(err) }] }));
    if (out.failed.length) {
      toast.error(`Could not restore ${p.name}: ${out.failed[0].error}`);
      setBusy(false);
    } else toast.success(`${p.name} is back. Its agents wake when you open it.`);
  };

  const onPick = (e: ReactMouseEvent) => {
    if ((e.target as HTMLElement).closest('.archived-actions')) return;
    if (selecting || e.shiftKey || e.ctrlKey || e.metaKey) {
      e.preventDefault();
      select.click(p.id, e);
    }
  };

  return (
    <article
      className={`archived-row ${selecting ? 'is-selecting' : ''} ${selected ? 'is-selected' : ''}`}
      aria-label={p.name}
      data-project={p.id}
      onClick={onPick}
      onMouseDown={(e) => {
        if (e.shiftKey) e.preventDefault();
      }}
      {...select.pressProps(p.id)}
    >
      {selecting && (
        <button type="button" className="archived-pick" role="checkbox" aria-checked={selected} aria-label={`Select ${p.name}`}>
          <SelectBox checked={selected} />
        </button>
      )}
      <ProjectIcon project={p} />
      <div className="archived-main">
        <h3 className="truncate">{p.name}</h3>
        {projectParts(p).length > 1 ? (
          <span className="archived-when" title={projectParts(p).map((pt) => `${pt.name}: ${pt.path}`).join('\n')}>
            {partsSummary(projectParts(p))} · {projectParts(p).map((pt) => pt.name).join(', ')}
          </span>
        ) : (
          <FolderPath path={p.path} />
        )}
        <span className="archived-when tabular" title={p.archivedAt ? `Archived ${formatDateTime(p.archivedAt)} · on ${machine}` : undefined}>
          Archived {p.archivedAt ? timeAgo(p.archivedAt) : ''} · {machine}
        </span>
      </div>
      <div className="archived-actions">
        <Button size="sm" icon="restart" loading={busy} disabled={busy || selecting} onClick={() => void restore()}>
          Restore
        </Button>
        <Button size="sm" variant="danger-ghost" icon="trash" disabled={busy || selecting} onClick={() => onDelete([p])} aria-label={`Delete ${p.name} permanently`} title="Delete permanently">
          <span className="archived-delete-label">Delete permanently</span>
        </Button>
      </div>
    </article>
  );
}

export function ArchivedList({ projects, select, onDelete, filtering }: ArchivedListProps) {
  if (!projects.length && filtering) {
    return (
      <div className="center-msg" role="status">
        <p>No archived project matches the filter.</p>
      </div>
    );
  }
  if (!projects.length) {
    return (
      <div className="card home-empty">
        <div className="empty-state">
          <h2>Nothing archived</h2>
          <p>An archived project keeps its boards, memory and history, and comes back with Restore.</p>
        </div>
      </div>
    );
  }
  return (
    <div className="card archived-list" role="list" aria-label="Archived projects">
      {projects.map((p) => (
        <div role="listitem" key={p.id}>
          <ArchivedRow p={p} select={select} onDelete={onDelete} />
        </div>
      ))}
    </div>
  );
}
