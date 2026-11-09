// Git helpers shared by the pill, the panel and the branches card: plain-language
// messages for the hub's error and job codes (docs/API-studio.md §3.2, §3.5).

import { ApiError, errorMessage } from '../api';
import type { Job } from '../orch/types';
import type { GitFile } from '../studio/types';

const CODE_TEXT: Record<string, string> = {
  empty_message: 'Write a commit message first.',
  bad_message: 'That commit message can’t be used. Keep it to plain text.',
  no_paths: 'Tick at least one file to commit.',
  nothing_to_commit: 'Nothing to commit: the ticked files have no changes any more.',
  conflicts: 'This merge still has conflicts. Resolve them in a console, then commit.',
  large_files: 'Some files are over 100 MB. GitHub refuses them: keep them off GitHub first.',
  not_large: 'Only files over 100 MB in commits that aren’t pushed can be removed.',
  head_moved: 'The branch moved since this was checked. Nothing was changed; look again.',
  operation_running: 'A merge, rebase, cherry-pick or bisect is under way. Finish or abort it first.',
  too_many_commits: 'More than 2000 commits aren’t pushed. Do this one in a console.',
  nothing_unpushed: 'Every commit is already on a remote.',
  no_backup: 'That backup doesn’t exist any more.',
  git_identity: 'Git doesn’t know who you are on this machine. Run git config --global user.name and user.email in a console.',
  git_busy: 'Another Git action is still running for this repository. Try again when it finishes.',
  nested_repos: 'This folder has Git repositories in its subfolders. Pick one of them; a new repository here would wrap them.',
  nothing_to_discard: 'Nothing to discard: those files have no changes.',
  exists: 'A file is already there, so nothing was overwritten.',
  bad_ref: 'That isn’t a valid branch name.',
  not_merged: 'That branch has commits that aren’t merged anywhere, so Git kept it.',
  git_refused: 'Git refused.',
  not_found: 'That branch doesn’t exist any more.',
  remote_exists: 'This repository already has a remote.',
  detached: 'You are on a detached HEAD. Switch to a branch first.',
  no_commits: 'There are no commits to publish yet.',
  bad_name: 'Use a repository name like my-app or owner/my-app.',
  gh_missing: 'The GitHub CLI (gh) isn’t installed on this machine.',
  gh_not_logged_in: 'The GitHub CLI isn’t signed in on this machine. Connect your account first.',
  bad_path: 'That path is outside the repository.',
  git_missing: 'Git isn’t installed on this machine.',
  runner_unsupported: 'Suggestions with a CLI need Claude Code or Codex.',
};

/** Message for a failed git request (hub code first, git's own words after). */
export function gitError(err: unknown): string {
  if (err instanceof ApiError && err.code && CODE_TEXT[err.code]) {
    const extra = err.code === 'git_refused' && err.message ? ` ${firstLine(err.message)}` : '';
    return CODE_TEXT[err.code] + extra;
  }
  return errorMessage(err);
}

const JOB_TEXT: Record<string, string> = {
  diverged: 'Branches diverged — merge in a console.',
  rejected: 'The remote has commits you don’t have. Pull first, then push again.',
  auth_failed: 'The remote refused the credentials. Set up an SSH key or a credential helper on that machine, then try again.',
  no_remote: 'This repository has no remote to push to.',
  no_upstream: 'This branch isn’t published yet. Use Publish branch.',
  detached: 'You are on a detached HEAD. Switch to a branch first.',
  network: 'The remote can’t be reached. Check the network on that machine.',
  timeout: 'Git took too long and was stopped.',
  idle_timeout: 'Git stopped printing anything for 5 minutes and was stopped.',
  large_files: 'The remote refused files over 100 MB. Take them out of the commits, then push again.',
  gh_missing: CODE_TEXT.gh_missing,
  gh_not_logged_in: CODE_TEXT.gh_not_logged_in,
  hub_restart: 'The hub restarted while this was running.',
};

const VERB: Record<string, string> = { push: 'Push', pull: 'Pull', fetch: 'Fetch', publish: 'Publish' };

/** One line about a finished push/pull/fetch/publish job. */
export function jobSummary(j: Job): { ok: boolean; text: string } {
  const verb = VERB[j.purpose ?? ''] ?? 'Git';
  if (j.state === 'done') {
    const done: Record<string, string> = { push: 'Pushed.', pull: 'Pulled.', fetch: 'Fetched.', publish: 'Published to GitHub.' };
    return { ok: true, text: done[j.purpose ?? ''] ?? 'Done.' };
  }
  if (j.state === 'cancelled') return { ok: false, text: `${verb} cancelled.` };
  const msg = (j.code && JOB_TEXT[j.code]) || firstLine(j.error || '') || 'it failed';
  return { ok: false, text: `${verb} failed: ${msg}` };
}

export function firstLine(s: string): string {
  return s.split('\n').find((l) => l.trim())?.trim() ?? '';
}

/** Whether a side of a file's status holds a change; the hub leaves an unchanged side empty. */
export const gitSideChanged = (side: string) => side !== '' && side !== ' ' && side !== '.' && side !== '?';

/** The colour family of a status in the editor (the `git-*` classes). */
export const STATUS_TONE: Record<string, string> = { modified: 'modified', typechange: 'modified', added: 'added', untracked: 'added', renamed: 'renamed', deleted: 'deleted', conflict: 'deleted' };

export const STATUS_LETTER: Record<string, string> = {
  modified: 'M',
  added: 'A',
  deleted: 'D',
  renamed: 'R',
  untracked: 'U',
  conflict: '!',
  typechange: 'T',
};

export const STATUS_LABEL: Record<string, string> = {
  modified: 'Modified',
  added: 'Added',
  deleted: 'Deleted',
  renamed: 'Renamed',
  untracked: 'New file',
  conflict: 'Conflict',
  typechange: 'Type changed',
};

export function splitPath(p: string): { dir: string; name: string } {
  const i = p.lastIndexOf('/');
  return i >= 0 ? { dir: p.slice(0, i + 1), name: p.slice(i + 1) } : { dir: '', name: p };
}

export function lineCounts(f: GitFile): string {
  if (f.binary) return 'binary';
  if (!f.added && !f.deleted) return '';
  return `+${f.added} −${f.deleted}`;
}

export function formatBytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(n >= 100 << 20 ? 0 : 1)} MB`;
  if (n >= 1 << 10) return `${Math.round(n / 1024)} KB`;
  return `${n} B`;
}
