// The parts of a multi-part project on its Home card (DESIGN.md §3.11): "6 parts · 3 machines"
// and one row of part chips, as many as fit and "+N", in the line a one-part card gives its
// folder, so both cards are as tall. The row opens the full list: each part with its machine,
// folder and git. The git row adds the parts up.

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Popover } from '../components/Popover';
import { machineReady } from '../board/sessionStatus';
import { partRef, projectParts } from '../lib/parts';
import type { Machine, Project } from '../lib/types';
import { useMachines } from '../store';
import { gitApi } from '../studio/api';
import { FolderPath } from './FolderPath';
import { fitCount, partsGit, partsGitText, partsSummary, type PartGit } from './partsCard';

// briefly kept, as the one-part cards keep theirs: going back to Home is instant
const cache = new Map<string, { at: number; git: PartGit }>();
const TTL = 30_000;

/** Each part's git, in the parts' order; a part on a machine that is away is not asked. */
export function usePartsGit(p: Project, enabled = true): (PartGit | undefined)[] {
  const machines = useMachines();
  const parts = projectParts(p);
  const key = parts.map((pt) => `${pt.id}@${pt.machineId}`).join(',');
  const down = parts.map((pt) => {
    const m = machines.find((x) => x.id === pt.machineId);
    return !!m && !machineReady(m);
  });
  const downKey = down.join(',');
  // keyed by "<project>~<part>" (lib/parts.ts partRef): a part keeps its entry when the parts are reordered
  const read = () => parts.map((pt, i): PartGit | undefined => (down[i] ? { offline: true } : cache.get(partRef(p, pt))?.git));
  const [list, setList] = useState(read);

  useEffect(() => {
    setList(read());
    if (!enabled) return;
    let on = true;
    parts.forEach((pt, i) => {
      if (down[i]) return;
      const ref = partRef(p, pt);
      const c = cache.get(ref);
      if (c && Date.now() - c.at < TTL) return;
      gitApi
        .repos(ref)
        .then((r) => ({ items: r.items ?? [] }) as PartGit, (err: unknown) => ({ error: errorMessage(err) }) as PartGit)
        .then((git) => {
          cache.set(ref, { at: Date.now(), git });
          if (on) setList((prev) => prev.map((x, k) => (k === i ? git : x)));
        });
    });
    return () => {
      on = false;
    };
    // the parts and which machines are away are what change the answer
  }, [p.id, key, downKey, enabled]);

  return list;
}

function machineOf(machines: Machine[], id: string) {
  return machines.find((m) => m.id === id);
}

/** The line under the name: the count, then the chips that fit. */
export function PartsLine({ project: p, git, onEdit, machinesShown = false }: { project: Project; git: (PartGit | undefined)[]; onEdit?: () => void; machinesShown?: boolean }) {
  const machines = useMachines();
  const parts = projectParts(p);
  const [open, setOpen] = useState(false);
  const [shown, setShown] = useState(parts.length);
  const btn = useRef<HTMLButtonElement>(null);
  const row = useRef<HTMLSpanElement>(null);
  // where the card has a chip for its machines, the line does not count them again
  const summary = partsSummary(parts, !machinesShown);

  useLayoutEffect(() => {
    const el = row.current;
    if (!el) return;
    const measure = () => {
      const chips = [...el.querySelectorAll<HTMLElement>('.partsline-chip')];
      const plus = el.querySelector<HTMLElement>('.partsline-more')?.offsetWidth ?? 0;
      const gap = parseFloat(getComputedStyle(el).columnGap) || 0;
      setShown(fitCount(chips.map((c) => c.offsetWidth), el.clientWidth, gap, plus));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [parts.map((pt) => pt.name).join('\n')]);

  const more = parts.length - shown;
  return (
    <>
      <button
        ref={btn}
        type="button"
        className="project-partsline"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`${summary}: ${parts.map((pt) => pt.name).join(', ')}. Show the parts`}
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
      >
        <span className="partsline-sum">{summary}</span>
        <span className="partsline-chips" ref={row} aria-hidden="true">
          {parts.map((pt, i) => {
            const m = machineOf(machines, pt.machineId);
            const down = !!m && !machineReady(m);
            return (
              <span key={pt.id} className={`partsline-chip ${i >= shown ? 'is-out' : ''} ${down ? 'is-down' : ''}`} title={`${pt.name} · ${m?.name ?? pt.machineId}${down ? ' (not connected)' : ''}\n${pt.path}`}>
                {pt.name}
              </span>
            );
          })}
          <span className={`partsline-more tabular ${more > 0 ? '' : 'is-out'}`}>+{more}</span>
        </span>
      </button>
      {open && (
        <Popover anchor={btn} onClose={() => setOpen(false)} label={`Parts of ${p.name}`} align="start" className="parts-pop" width={380}>
          <PartsList project={p} git={git} />
          {onEdit && (
            <div className="parts-pop-foot">
              <Button
                size="sm"
                icon="pencil"
                onClick={() => {
                  setOpen(false);
                  onEdit();
                }}
              >
                Edit parts…
              </Button>
            </div>
          )}
        </Popover>
      )}
    </>
  );
}

/** Every part, equal: its name, machine, folder, and git. */
function PartsList({ project: p, git }: { project: Project; git: (PartGit | undefined)[] }) {
  const machines = useMachines();
  const parts = projectParts(p);
  return (
    <ul className="parts-pop-list" aria-label={`Parts of ${p.name}`}>
      {parts.map((pt, i) => {
        const m = machineOf(machines, pt.machineId);
        const down = !!m && !machineReady(m);
        const g = git[i];
        const s = g?.items ? partsGit([g]) : null;
        const t = s ? partsGitText(s) : null;
        return (
          <li key={pt.id} className="parts-pop-row">
            <div className="parts-pop-head">
              <strong className="truncate">{pt.name}</strong>
              <span className={`parts-pop-machine truncate ${down ? 'is-down' : ''}`}>
                <Icon name={pt.machineId === 'local' ? 'monitor' : 'server'} />
                {m?.name ?? pt.machineId}
                {down && ' · not connected'}
              </span>
              {pt.readOnly && (
                <span className="parts-pop-ro" title="Agents only read here">
                  <Icon name="lock" />
                  <span className="sr-only">Read-only for agents</span>
                </span>
              )}
            </div>
            <FolderPath path={pt.path} />
            <div className="project-git">
              {down || g?.offline ? (
                <span className="project-note">Machine offline</span>
              ) : !g ? (
                <span className="project-note">Checking git…</span>
              ) : g.error ? (
                <span className="project-warn">Git unavailable</span>
              ) : !s?.repos ? (
                <span className="project-note">No git repository</span>
              ) : (
                <>
                  <span className="project-branch mono">
                    <Icon name="branch" />
                    <span className="project-branch-name">{s.branch || (s.repos > 1 ? `${s.repos} repositories` : 'detached')}</span>
                  </span>
                  <span className={`badge ${t?.dirty ? 'badge-warning' : 'badge-success'}`}>{t?.dirty ? (s.repos > 1 ? t.badge : `${g.items?.[0]?.changed ?? 0} changed`) : 'Clean'}</span>
                </>
              )}
            </div>
          </li>
        );
      })}
    </ul>
  );
}

/** The git row of a multi-part card: the shared branch if there is one, the repos, and their state. */
export function PartsGitLine({ git }: { git: (PartGit | undefined)[] }) {
  const s = partsGit(git);
  const t = partsGitText(s);
  return (
    <div className="project-git">
      {s.branch && s.repos > 0 && (
        <span className="project-branch mono">
          <Icon name="branch" />
          <span className="project-branch-name">{s.branch}</span>
        </span>
      )}
      <span className={s.repos ? 'project-repos' : 'project-note'}>{t.text}</span>
      {t.note && <span className="project-note truncate">· {t.note}</span>}
      {s.ahead > 0 && (
        <span className="project-sync tabular">
          <Icon name="arrowUp" />
          {s.ahead}
          <span className="sr-only"> ahead</span>
        </span>
      )}
      {s.behind > 0 && (
        <span className="project-sync is-behind tabular">
          <Icon name="arrowDown" />
          {s.behind}
          <span className="sr-only"> behind</span>
        </span>
      )}
      {t.badge && <span className={`badge ${t.dirty ? 'badge-warning' : 'badge-success'}`}>{t.badge}</span>}
    </div>
  );
}
