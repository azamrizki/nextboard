// Git pill (bottom left of the board, docs/API-studio.md §17): branch, changed files and
// ahead/behind. Opens the Git panel (G). The hub only notices changes when someone asks
// for the status, so the pill asks every 15 s while the tab is visible (read-only,
// GIT_OPTIONAL_LOCKS=0) and right away when the window regains focus. A project of several parts
// gets every part's repositories added up (v0.9.0-beta.2), as Home's card does: "3 repos", the
// branch only when they all share one.

import { useEffect, useState } from 'react';
import { machineReady } from '../board/sessionStatus';
import { Icon } from '../components/Icon';
import { partRef, projectParts } from '../lib/parts';
import type { Project } from '../lib/types';
import { useMachines } from '../store';
import { studioStore, useStudio } from '../studio/store';
import { sumRepos } from './parts';

const POLL_MS = 15_000;

/** Calls load now, every 15 s while the tab is visible, and when the window comes back. */
function usePoll(load: () => void, key: string) {
  useEffect(() => {
    const run = () => {
      if (document.visibilityState === 'visible') load();
    };
    run();
    const t = window.setInterval(run, POLL_MS);
    window.addEventListener('focus', run);
    document.addEventListener('visibilitychange', run);
    return () => {
      window.clearInterval(t);
      window.removeEventListener('focus', run);
      document.removeEventListener('visibilitychange', run);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- load is new each render; key says when it changes
  }, [key]);
}

type PillProps = { projectId: string; open: boolean; onOpen: () => void; shortcut: string; project?: Project };

export function GitPill(props: PillProps) {
  return props.project && projectParts(props.project).length > 1 ? <PartsPill {...props} project={props.project} /> : <OnePill {...props} />;
}

/** A project of several parts: every part's repositories (those on an away machine left out). */
function PartsPill({ project, open, onOpen, shortcut }: PillProps & { project: Project }) {
  const machines = useMachines();
  const parts = projectParts(project).filter((pt) => {
    const m = machines.find((x) => x.id === (pt.machineId || 'local'));
    return !m || machineReady(m);
  });
  const refs = parts.map((pt) => partRef(project, pt));
  const refsKey = refs.join('\n');
  // one string, so the store hook stays stable: "repos:changed:ahead:behind:branch" per part
  const packed = useStudio((s) =>
    refs
      .map((r) => {
        const items = s.gitRepos[r]?.items;
        if (!items) return '';
        const t = sumRepos(items);
        return `${t.repos}:${t.changed}:${t.ahead}:${t.behind}:${t.repos === 1 ? t.branch : '*'}`;
      })
      .join('|'),
  );
  usePoll(() => refsKey.split('\n').filter(Boolean).forEach((r) => void studioStore.loadGitSummary(r)), refsKey);
  const rows = packed.split('|').filter(Boolean).map((x) => x.split(':'));
  if (!rows.length) return null;
  const n = (i: number) => rows.reduce((a, r) => a + (Number(r[i]) || 0), 0);
  const repos = n(0);
  const changed = n(1);
  const ahead = n(2);
  const behind = n(3);
  const branches = new Set(rows.filter((r) => Number(r[0]) > 0).map((r) => r[4]));
  const branch = branches.size === 1 && !branches.has('*') ? [...branches][0] : '';
  const label = !repos
    ? 'No Git repository in any part'
    : `${repos} repositor${repos === 1 ? 'y' : 'ies'} in ${parts.length} parts${branch ? `, branch ${branch}` : ''}, ${changed} changed file${changed === 1 ? '' : 's'}${ahead ? `, ${ahead} to push` : ''}${behind ? `, ${behind} to pull` : ''}`;
  return (
    <button
      type="button"
      className={`git-pill ${open ? 'is-open' : ''} ${repos ? '' : 'is-norepo'}`}
      aria-label={`${label}. Open the Git panel (${shortcut.toUpperCase()})`}
      aria-expanded={open}
      title={`${label} · Git panel (${shortcut.toUpperCase()})`}
      onClick={onOpen}
    >
      <Icon name="branch" />
      {repos ? (
        <>
          <span className="git-pill-branch truncate">{branch || `${repos} repos`}</span>
          {!!changed && <span className="git-pill-count tabular">{changed > 999 ? '999+' : changed}</span>}
          {!!ahead && (
            <span className="git-pill-sync tabular" aria-hidden="true">
              ↑{ahead}
            </span>
          )}
          {!!behind && (
            <span className="git-pill-sync tabular" aria-hidden="true">
              ↓{behind}
            </span>
          )}
        </>
      ) : (
        <span className="git-pill-branch">No repo</span>
      )}
    </button>
  );
}

function OnePill({ projectId: id, project, open, onOpen, shortcut }: PillProps) {
  // its part's git, "<project>~<part>" as every part's (lib/parts.ts refOf)
  const projectId = project ? partRef(project, projectParts(project)[0]) : id;
  const sum = useStudio((s) => s.git[projectId]);
  const selected = useStudio((s) => s.gitRepo[projectId] ?? '');
  const repos = useStudio((s) => s.gitRepos[projectId]?.items);
  const [repo, setRepo] = useState<boolean | null>(null);

  useEffect(() => {
    let stop = false;
    const load = () => {
      if (document.visibilityState !== 'visible') return;
      void studioStore.loadGitSummary(projectId).then((st) => {
        if (!stop && st) setRepo(st.isRepo);
      });
    };
    load();
    const t = window.setInterval(load, POLL_MS);
    window.addEventListener('focus', load);
    document.addEventListener('visibilitychange', load);
    return () => {
      stop = true;
      window.clearInterval(t);
      window.removeEventListener('focus', load);
      document.removeEventListener('visibilitychange', load);
    };
  }, [projectId, selected]);

  if (repo === null && !sum) return null;
  const isRepo = repo !== false || !!sum;
  // with several repositories the pill says which one it is about
  const name = repos && repos.length > 1 ? repos.find((r) => r.path === selected)?.name : '';
  const label = !isRepo
    ? 'No Git repository'
    : `${name ? `Repository ${name}, b` : 'B'}ranch ${sum?.branch || 'detached'}, ${sum?.changed ?? 0} changed file${sum?.changed === 1 ? '' : 's'}${sum?.ahead ? `, ${sum.ahead} to push` : ''}${sum?.behind ? `, ${sum.behind} to pull` : ''}`;
  return (
    <button
      type="button"
      className={`git-pill ${open ? 'is-open' : ''} ${isRepo ? '' : 'is-norepo'}`}
      aria-label={`${label}. Open the Git panel (${shortcut.toUpperCase()})`}
      aria-expanded={open}
      title={`${label} · Git panel (${shortcut.toUpperCase()})`}
      onClick={onOpen}
    >
      <Icon name="branch" />
      {isRepo ? (
        <>
          <span className="git-pill-branch truncate">
            {name ? `${name} · ` : ''}
            {sum?.branch || 'detached'}
          </span>
          {!!sum?.changed && <span className="git-pill-count tabular">{sum.changed > 999 ? '999+' : sum.changed}</span>}
          {!!sum?.ahead && (
            <span className="git-pill-sync tabular" aria-hidden="true">
              ↑{sum.ahead}
            </span>
          )}
          {!!sum?.behind && (
            <span className="git-pill-sync tabular" aria-hidden="true">
              ↓{sum.behind}
            </span>
          )}
        </>
      ) : (
        <span className="git-pill-branch">No repo</span>
      )}
    </button>
  );
}
