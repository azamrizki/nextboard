// A file over 100 MB that already went into a commit (docs/API-studio.md §3.7). Keeping
// it off GitHub is not enough then: the commit still holds it, and the push still fails.
// This looks in the commits no remote has yet and offers to rewrite them without it.

import { useCallback, useEffect, useState } from 'react';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { gitApi } from '../studio/api';
import type { GitLargeHistory, GitStatus, GitTarget } from '../studio/types';
import { formatBytes, gitError } from './common';

interface Props {
  target: GitTarget;
  /** Commits ahead of the upstream. Above the cap the scan only runs when a push failed. */
  unpushed: number;
  /** Bumped when a push failed on file size, which also covers a branch without upstream. */
  rescan: number;
  disabled?: boolean;
  onStatus: (st: GitStatus) => void;
}

const SCAN_CAP = 200;

export function LargeInCommits({ target, unpushed, rescan, disabled, onStatus }: Props) {
  const [history, setHistory] = useState<GitLargeHistory | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [removed, setRemoved] = useState<{ backup: string; head: string; paths: string[] } | null>(null);

  const scan = useCallback(async () => {
    try {
      setHistory(await gitApi.largeHistory(target));
    } catch {
      setHistory(null);
    }
  }, [target]);

  useEffect(() => {
    if (rescan > 0 || (unpushed > 0 && unpushed <= SCAN_CAP)) void scan();
    else setHistory(null);
  }, [unpushed, rescan, scan]);

  const remove = async () => {
    if (!history) return;
    const paths = history.files.map((f) => f.path);
    const r = await gitApi.removeLarge(target, history.head, paths);
    onStatus(r.status);
    setRemoved({ backup: r.backup, head: r.head, paths });
    toast.success(`${paths.length === 1 ? '1 file' : `${paths.length} files`} out of the commits. They stay on this machine, in .gitignore.`);
    await scan();
  };

  const undo = async () => {
    if (!removed) return;
    try {
      onStatus(await gitApi.undoRemoveLarge(target, removed.backup, removed.head, removed.paths));
      setRemoved(null);
      toast.success('The old commits are back.');
      void scan();
    } catch (err) {
      toast.error(gitError(err));
    }
  };

  const files = history?.files ?? [];
  const pushedToo = files.filter((f) => f.inPushed);
  return (
    <>
      {history && files.length > 0 && (
        <div className="alert warning" role="status" data-testid="git-large-history">
          <Icon name="alert" />
          <div className="stack-sm">
            <span>
              {files.length === 1 ? '1 file' : `${files.length} files`} over 100 MB {files.length === 1 ? 'is' : 'are'} inside commits that aren’t pushed. GitHub refuses the push until {files.length === 1 ? 'it is' : 'they are'} out of
              those commits.
            </span>
            <ul className="plain mono fs-xs">
              {files.slice(0, 4).map((f) => (
                <li key={f.path} className="truncate">
                  {f.path} · {formatBytes(f.sizeBytes)} · {f.commits === 1 ? '1 commit' : `${f.commits} commits`}
                </li>
              ))}
              {files.length > 4 && <li>+ {files.length - 4} more</li>}
            </ul>
            {pushedToo.length > 0 && (
              <span>The remote already has {pushedToo.length === 1 ? pushedToo[0].path : `${pushedToo.length} of these paths`} at a smaller size. Removing means your next push deletes it there.</span>
            )}
            {history.detached ? (
              <span>Switch to a branch first.</span>
            ) : history.tooMany ? (
              <span>More than 2000 commits aren’t pushed. Do this one in a console.</span>
            ) : (
              <div>
                <Button size="sm" disabled={disabled} onClick={() => setConfirming(true)}>
                  Remove from commits
                </Button>
              </div>
            )}
          </div>
        </div>
      )}

      {removed && (
        <div className="alert" role="status" data-testid="git-large-removed">
          <Icon name="check" />
          <div className="stack-sm">
            <span>The old commits are kept in refs/nextboard/backup/{removed.backup} until you delete that ref. A push never sends it.</span>
            <div>
              <Button size="sm" disabled={disabled} onClick={() => void undo()}>
                Undo
              </Button>
            </div>
          </div>
        </div>
      )}

      {confirming && history && (
        <ConfirmDialog
          title={`Rewrite ${history.unpushed === 1 ? '1 commit' : `${history.unpushed} commits`} that aren’t pushed?`}
          confirmLabel="Remove from commits"
          danger
          onClose={() => setConfirming(false)}
          onConfirm={remove}
        >
          <p>
            New commits replace them, without {files.length === 1 ? 'this file' : 'these files'}. Author, date and message stay. A commit that held nothing else goes away. Pushed commits don’t change, and your open edits aren’t touched.
          </p>
          <ul className="plain mono fs-xs discard-list">
            {files.slice(0, 8).map((f) => (
              <li key={f.path} className="truncate">
                {f.path}
              </li>
            ))}
            {files.length > 8 && <li>+ {files.length - 8} more</li>}
          </ul>
          <p>The files stay on this machine and go into .gitignore. The old commits are kept as a backup, and you can undo this until you commit again.</p>
        </ConfirmDialog>
      )}
    </>
  );
}
