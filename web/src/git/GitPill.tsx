// Git pill (bottom left of the board, docs/API-studio.md §17): branch, changed files and
// ahead/behind. Opens the Git panel (G). The hub only notices changes when someone asks
// for the status, so the pill asks every 15 s while the tab is visible (read-only,
// GIT_OPTIONAL_LOCKS=0) and right away when the window regains focus.

import { useEffect, useState } from 'react';
import { Icon } from '../components/Icon';
import { studioStore, useStudio } from '../studio/store';

const POLL_MS = 15_000;

export function GitPill({ projectId, open, onOpen, shortcut }: { projectId: string; open: boolean; onOpen: () => void; shortcut: string }) {
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
