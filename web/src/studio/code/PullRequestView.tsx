// One pull request in a tab: what it says, its checks and reviews, its diff, and what can be done
// with it, through the gh CLI of the project's machine (docs/API-studio.md §3.3).

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { gitError } from '../../git/common';
import { formatDateTime, timeAgo } from '../../lib/format';
import { gitApi } from '../api';
import { MarkdownView } from '../MarkdownView';
import type { GitPullCheck, GitPullComment, GitPullDetail, GitTarget } from '../types';
import type { Workspace } from './workspace';

const MAX_LINES = 6000;

interface PatchFile {
  path: string;
  lines: string[];
}

function splitPatch(diff: string): PatchFile[] {
  const out: PatchFile[] = [];
  let cur: PatchFile | null = null;
  for (const line of diff.split('\n')) {
    const head = /^diff --git a\/(.+?) b\/(.+)$/.exec(line);
    if (head) {
      cur = { path: head[2], lines: [] };
      out.push(cur);
    } else if (cur && !/^(index |--- |\+\+\+ |new file mode |deleted file mode |similarity index |rename )/.test(line)) {
      cur.lines.push(line);
    }
  }
  return out;
}

const checkName = (c: GitPullCheck) => c.name || c.context || 'check';
const checkResult = (c: GitPullCheck) => (c.conclusion || c.state || c.status || '').toUpperCase();

function checkTone(c: GitPullCheck): 'ok' | 'bad' | 'wait' {
  const r = checkResult(c);
  if (r === 'SUCCESS' || r === 'NEUTRAL' || r === 'SKIPPED') return 'ok';
  if (r === 'FAILURE' || r === 'ERROR' || r === 'CANCELLED' || r === 'TIMED_OUT' || r === 'ACTION_REQUIRED') return 'bad';
  return 'wait';
}

function CommentRow({ c }: { c: GitPullComment }) {
  const at = c.submittedAt && !c.submittedAt.startsWith('0001') ? c.submittedAt : c.createdAt;
  return (
    <div className="wb-pr-comment">
      <div className="wb-pr-comment-head">
        <strong>{c.author.login}</strong>
        {c.state && <span className={`wb-pr-pill is-${c.state.toLowerCase()}`}>{c.state.replace('_', ' ').toLowerCase()}</span>}
        {at && !at.startsWith('0001') && <span className="text-3">{timeAgo(at)}</span>}
      </div>
      {c.body && <MarkdownView text={c.body} className="md-note" />}
    </div>
  );
}

export function PullRequestView({ ws, number, repo }: { ws: Workspace; number: number; repo: string }) {
  const target = useMemo<GitTarget>(() => ({ pid: ws.pid, repo }), [ws.pid, repo]);
  const [pr, setPr] = useState<GitPullDetail | null>(null);
  const [diff, setDiff] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');
  const [note, setNote] = useState('');
  const [merging, setMerging] = useState(false);
  const [method, setMethod] = useState<'merge' | 'squash' | 'rebase'>('squash');
  const [dropBranch, setDropBranch] = useState(true);
  const [open, setOpen] = useState<Set<string>>(new Set());

  const load = useCallback(() => {
    setError('');
    gitApi
      .pullRequest(target, number)
      .then(setPr)
      .catch((err) => setError(gitError(err)));
    gitApi
      .pullDiff(target, number)
      .then((r) => setDiff(r.diff))
      .catch(() => setDiff(''));
  }, [target, number]);
  useEffect(load, [load]);

  const files = useMemo(() => splitPatch(diff), [diff]);

  const act = async (name: string, fn: () => Promise<unknown>, done: string) => {
    setBusy(name);
    try {
      await fn();
      toast.success(done);
      load();
      ws.pullsTick++;
      void ws.refreshGit();
      return true;
    } catch (err) {
      toast.error(gitError(err));
      return false;
    } finally {
      setBusy('');
    }
  };

  const review = (event: 'approve' | 'comment' | 'request-changes', done: string) =>
    void act(event, () => gitApi.pullReview(target, number, event, note.trim()), done).then((ok) => {
      if (ok) setNote('');
    });

  if (error) {
    return (
      <div className="wb-view-empty" role="alert">
        <Icon name="alertCircle" />
        <p>{error}</p>
      </div>
    );
  }
  if (!pr) return <div className="wb-row-msg">Loading the pull request…</div>;

  const checks = pr.statusCheckRollup ?? [];
  const isOpen = pr.state === 'OPEN';
  let lines = 0;
  return (
    <div className="wb-pr nowheel">
      <header className="wb-cv-head wb-pr-head">
        <div className="wb-cv-title">
          <h2>
            {pr.title} <span className="text-3">#{pr.number}</span>
          </h2>
          <span className={`wb-pr-pill is-${pr.isDraft ? 'draft' : pr.state.toLowerCase()}`}>{pr.isDraft ? 'draft' : pr.state.toLowerCase()}</span>
        </div>
        <div className="wb-cv-meta">
          <strong>{pr.author.login}</strong>
          <span className="mono" title="From, into">
            {pr.headRefName} → {pr.baseRefName}
          </span>
          <span className="text-3" title={formatDateTime(pr.updatedAt)}>
            updated {timeAgo(pr.updatedAt)}
          </span>
          <span>
            {pr.changedFiles} {pr.changedFiles === 1 ? 'file' : 'files'} <span className="wb-cv-add">+{pr.additions}</span> <span className="wb-cv-del">−{pr.deletions}</span>
          </span>
        </div>
        {checks.length > 0 && (
          <div className="wb-pr-checks" aria-label="Checks">
            {checks.map((c, i) => (
              <span key={`${checkName(c)}:${i}`} className={`wb-pr-check is-${checkTone(c)}`} title={checkResult(c).toLowerCase()}>
                <Icon name={checkTone(c) === 'ok' ? 'check' : checkTone(c) === 'bad' ? 'close' : 'clock'} />
                {checkName(c)}
              </span>
            ))}
          </div>
        )}
        <div className="wb-pr-actions">
          <Button size="sm" loading={busy === 'checkout'} disabled={!!busy} onClick={() => void act('checkout', () => gitApi.pullCheckout(target, number), `On ${pr.headRefName}.`)}>
            Check out
          </Button>
          <a className="btn btn-ghost btn-sm" href={pr.url} target="_blank" rel="noreferrer noopener">
            Open on GitHub
          </a>
          <span className="spacer" />
          {isOpen && (
            <Button size="sm" variant="primary" disabled={!!busy || pr.mergeable === 'CONFLICTING'} title={pr.mergeable === 'CONFLICTING' ? 'This branch conflicts with its base.' : undefined} onClick={() => setMerging(true)}>
              Merge…
            </Button>
          )}
        </div>
      </header>
      <div className="wb-pr-body">
        {pr.body ? <MarkdownView text={pr.body} className="md-note" /> : <p className="text-3">No description.</p>}
        {[...(pr.reviews ?? []), ...(pr.comments ?? [])].map((c, i) => (
          <CommentRow key={i} c={c} />
        ))}
        {isOpen && (
          <div className="wb-pr-review">
            <textarea className="control prose" rows={3} aria-label="Your review" placeholder="Write a comment or a review" value={note} onChange={(e) => setNote(e.target.value)} />
            <div className="wb-commit-actions">
              <Button size="sm" disabled={!!busy || !note.trim()} loading={busy === 'comment'} onClick={() => review('comment', 'Commented.')}>
                Comment
              </Button>
              <Button size="sm" disabled={!!busy || !note.trim()} loading={busy === 'request-changes'} onClick={() => review('request-changes', 'Asked for changes.')}>
                Request changes
              </Button>
              <Button size="sm" variant="primary" icon="check" disabled={!!busy} loading={busy === 'approve'} onClick={() => review('approve', 'Approved.')}>
                Approve
              </Button>
            </div>
          </div>
        )}
        <h3 className="wb-pr-files-title">Files</h3>
        {!files.length && <p className="text-3">{diff ? '' : 'No diff to show.'}</p>}
        {files.map((f) => {
          const shown = open.has(f.path) || files.length <= 5;
          const stat = pr.files?.find((x) => x.path === f.path);
          return (
            <section key={f.path} className="wb-pr-file">
              <button
                type="button"
                className="wb-section-title wb-section-toggle"
                aria-expanded={shown}
                onClick={() =>
                  setOpen((cur) => {
                    const n = new Set(cur);
                    if (n.has(f.path)) n.delete(f.path);
                    else n.add(f.path);
                    return n;
                  })
                }
              >
                <Icon name={shown ? 'chevronDown' : 'chevronRight'} className="wb-chev" />
                <span className="mono truncate">{f.path}</span>
                {stat && (
                  <span className="tabular">
                    <span className="wb-cv-add">+{stat.additions}</span> <span className="wb-cv-del">−{stat.deletions}</span>
                  </span>
                )}
              </button>
              {shown && (
                <pre className="wb-pr-patch mono">
                  {f.lines.map((l, i) => {
                    lines++;
                    if (lines > MAX_LINES) return null;
                    const tone = l.startsWith('@@') ? 'hunk' : l.startsWith('+') ? 'add' : l.startsWith('-') ? 'del' : '';
                    return (
                      <div key={i} className={tone && `is-${tone}`}>
                        {l || ' '}
                      </div>
                    );
                  })}
                </pre>
              )}
            </section>
          );
        })}
        {lines > MAX_LINES && <p className="text-3">The rest is too long to show here. It is on GitHub.</p>}
      </div>
      {merging && (
        <ConfirmDialog
          title={`Merge #${pr.number} into ${pr.baseRefName}?`}
          confirmLabel="Merge"
          onClose={() => setMerging(false)}
          onConfirm={async () => {
            await gitApi.pullMerge(target, number, method, dropBranch);
            toast.success('Merged.');
            load();
            ws.pullsTick++;
            void ws.refreshGit();
          }}
        >
          <label className="field">
            <span className="field-label">How</span>
            <select className="control" aria-label="Merge method" value={method} onChange={(e) => setMethod(e.target.value as typeof method)}>
              <option value="squash">Squash into one commit</option>
              <option value="merge">Merge commit</option>
              <option value="rebase">Rebase</option>
            </select>
          </label>
          <label className="wb-check">
            <input type="checkbox" checked={dropBranch} onChange={(e) => setDropBranch(e.target.checked)} />
            <span>Delete the branch afterwards</span>
          </label>
        </ConfirmDialog>
      )}
    </div>
  );
}
