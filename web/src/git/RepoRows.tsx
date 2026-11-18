// The repositories of one part as rows (docs/API-studio.md §3.6, DESIGN.md §3.5 Git): name,
// branch, ahead/behind and changed files, the one the Git views work on marked. The same rows in
// the board's Git panel (a part's section, or a project of one part), the editor's Source control
// and the Branches card, so a part with several repositories looks alike everywhere. A part of
// one repository or none shows no rows.

import { Icon } from '../components/Icon';
import { studioStore, useStudio } from '../studio/store';
import type { GitSummary } from '../studio/types';
import { summaryLabel } from './parts';
import '../styles/git-rows.css';

/** A repository's state in one line: its branch, ↑ ↓, changed files; "Could not read it" on an error. */
export function RepoStat({ s, error }: { s: GitSummary; error?: string }) {
  if (error)
    return (
      <span className="git-part-stat text-3 truncate" title={error}>
        Could not read it
      </span>
    );
  return (
    <span className="git-part-stat tabular">
      <span className="sr-only">{summaryLabel(s)}</span>
      <span className="badge git-branch" aria-hidden="true">
        <Icon name="branch" />
        <span className="truncate">{s.branch || 'detached'}</span>
      </span>
      {!!s.ahead && <span aria-hidden="true">↑{s.ahead}</span>}
      {!!s.behind && <span aria-hidden="true">↓{s.behind}</span>}
      <span className={s.changed ? 'git-part-changed' : 'text-3'} aria-hidden="true">
        {s.changed > 999 ? '999+' : s.changed} changed
      </span>
    </span>
  );
}

/**
 * The part's repositories as rows; picking one makes it the one its Git views work on. marked
 * says whether the current one is shown as current (a closed section marks none). label names
 * the list for screen readers ("Repositories of api").
 */
export function RepoRows({
  pid,
  label,
  disabled,
  marked = true,
  className = '',
  onPick,
}: {
  pid: string;
  label: string;
  disabled?: boolean;
  marked?: boolean;
  className?: string;
  onPick?: (path: string) => void;
}) {
  const list = useStudio((s) => s.gitRepos[pid]);
  const repo = useStudio((s) => s.gitRepo[pid] ?? '');
  if (!list || list.items.length < 2) return null;
  return (
    <ul className={`plain git-part-repos ${className}`} aria-label={label}>
      {list.items.map((r) => {
        const current = marked && r.path === repo;
        return (
          <li key={r.path}>
            <button
              type="button"
              className={`git-part-repo nodrag ${current ? 'is-current' : ''}`}
              aria-current={current ? 'true' : undefined}
              disabled={disabled}
              title={r.root || r.path || r.name}
              onClick={() => {
                studioStore.selectGitRepo(pid, r.path);
                onPick?.(r.path);
              }}
            >
              <Icon name="branch" className="git-part-repo-icon" />
              <span className="git-part-repo-name truncate">{r.path || `${r.name} (part folder)`}</span>
              <RepoStat s={r} error={r.error} />
            </button>
          </li>
        );
      })}
      {list.truncated && <li className="fs-xs text-3">Not every repository is listed</li>}
    </ul>
  );
}
