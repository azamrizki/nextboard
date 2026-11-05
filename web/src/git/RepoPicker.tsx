// Picks which of a project's repositories the Git views work on (docs/API-studio.md §3.6).
// Nothing shows when the project has one repository or none.

import { studioStore, useStudio } from '../studio/store';
import type { GitRepoInfo } from '../studio/types';

function label(r: GitRepoInfo): string {
  const name = r.path || `${r.name} (project folder)`;
  if (r.error) return name;
  return `${name} · ${r.branch || 'detached'}${r.changed ? ` · ${r.changed > 999 ? '999+' : r.changed} changed` : ''}`;
}

export function RepoPicker({ pid, className = '', disabled }: { pid: string; className?: string; disabled?: boolean }) {
  const list = useStudio((s) => s.gitRepos[pid]);
  const repo = useStudio((s) => s.gitRepo[pid] ?? '');
  if (!list || list.items.length < 2) return null;
  return (
    <select
      className={`control control-sm git-repo-picker nodrag ${className}`}
      aria-label="Repository"
      title="Repository"
      value={repo}
      disabled={disabled}
      onChange={(e) => studioStore.selectGitRepo(pid, e.target.value)}
    >
      {list.items.map((r) => (
        <option key={r.path} value={r.path}>
          {label(r)}
        </option>
      ))}
      {list.truncated && <option disabled>Not every repository is listed</option>}
    </select>
  );
}
