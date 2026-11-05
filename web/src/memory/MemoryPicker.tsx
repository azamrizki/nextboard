// Pick memories: this project's pages, those of all projects, or another project's. Critical
// notes are not offered: a project's own always go along, and another project's bind that
// project. The list has sizes and no content, so opening it costs nothing.

import { useEffect, useMemo, useState } from 'react';
import { errorMessage } from '../api';
import { TextField } from '../components/form';
import type { MemoryBlock } from '../orch/types';
import { memoryApi } from './api';
import { blockKey, tokensLabel } from './blocks';
import type { GalaxyOwner, GalaxyView } from './types';
import '../styles/orch.css';

function matches(q: string, ...texts: (string | undefined)[]) {
  return !q || texts.some((t) => t?.toLowerCase().includes(q));
}

export function MemoryPicker({ projectId, picked, hidden, onToggle }: { projectId: string; picked: Set<string>; hidden?: Set<string>; onToggle: (b: MemoryBlock, on: boolean) => void }) {
  const [view, setView] = useState<GalaxyView | null>(null);
  const [error, setError] = useState('');
  const [query, setQuery] = useState('');
  const q = query.trim().toLowerCase();

  useEffect(() => {
    let alive = true;
    memoryApi
      .galaxy()
      .then((v) => alive && setView(v))
      .catch((err) => alive && setError(errorMessage(err)));
    return () => {
      alive = false;
    };
  }, []);

  const groups = useMemo(() => {
    if (!view) return [];
    const own = view.projects.find((p) => p.projectId === projectId);
    const list: { owner: GalaxyOwner; label: string; other: boolean }[] = [];
    if (own) list.push({ owner: own, label: `${own.name} (this project)`, other: false });
    list.push({ owner: view.global, label: 'All projects', other: false });
    for (const p of view.projects) if (p !== own) list.push({ owner: p, label: p.name, other: true });
    return list
      .map((g) => ({
        ...g,
        docs: g.owner.docs.filter((d) => !d.critical && d.chars > 0 && !hidden?.has(blockKey({ projectId: g.owner.projectId, name: d.name })) && matches(q, d.title, d.description, g.label)),
      }))
      .filter((g) => g.docs.length > 0);
  }, [view, projectId, hidden, q]);

  if (error) return <p className="field-error" role="alert">Could not list the memories: {error}</p>;
  if (!view) return <div className="list-msg" role="status">Loading memories…</div>;

  return (
    <div className="memory-picker">
      <TextField label="Find a memory" value={query} placeholder="Title or project" onChange={(e) => setQuery(e.target.value)} />
      <div className="memory-picker-list">
        {groups.length === 0 && <p className="tile-hint">{q ? 'No memory matches that.' : 'There is no other memory to add yet.'}</p>}
        {groups.map((g) => {
          const rows = g.docs.map((d) => {
            const block: MemoryBlock = g.owner.projectId ? { projectId: g.owner.projectId, name: d.name } : { name: d.name };
            const on = picked.has(blockKey(block));
            return (
              <label key={d.name} className="memory-pick-row">
                <input type="checkbox" checked={on} onChange={() => onToggle(block, !on)} />
                <span className="memory-pick-text">
                  <span className="truncate">{d.title}</span>
                  {d.description && <span className="memory-pick-desc truncate">{d.description}</span>}
                </span>
                <span className="memory-size tabular" title={`${d.chars} characters`}>
                  {tokensLabel(d.chars)}
                </span>
              </label>
            );
          });
          if (!g.other) {
            return (
              <section key={g.label} aria-label={g.label}>
                <h4 className="memory-picker-head">{g.label}</h4>
                {rows}
              </section>
            );
          }
          const hasPicked = g.docs.some((d) => picked.has(blockKey({ projectId: g.owner.projectId, name: d.name })));
          return (
            <details key={g.label} open={!!q || hasPicked}>
              <summary className="memory-picker-head">{g.label}</summary>
              {rows}
            </details>
          );
        })}
      </div>
    </div>
  );
}
