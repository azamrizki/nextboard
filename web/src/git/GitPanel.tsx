// Git panel (G; docs/API-studio.md §3, B20–B21): status and sync of the project's repo,
// the changes list with checkboxes (synced to the hub so the command bar's "commit and
// push" uses the same files), per-file diff, a suggested subject, Commit & Push /
// Commit, Push / Publish branch, Pull, Fetch (only when clicked), Discard to the
// project trash (with restore), history, files over 100 MB, Create repository and
// Publish to GitHub through the gh CLI of the project's machine. A project folder may hold
// several repositories (§3.6): the panel scans it and works on the one picked.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { isCode, type ApiError } from '../api';
import { useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { SegmentedControl } from '../components/form';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { timeAgo } from '../lib/format';
import type { Project } from '../lib/types';
import { orchStore } from '../orch/store';
import type { Job } from '../orch/types';
import { gitApi, jobApi } from '../studio/api';
import { jobActive, studioStore, useJob, useStudio } from '../studio/store';
import type { GitCommit, GitDiff, GitFile, GitRepoInfo, GitStatus, GitTarget, GitTrashItem } from '../studio/types';
import { formatBytes, gitError, jobSummary, lineCounts, splitPath, STATUS_LABEL, STATUS_LETTER } from './common';
import { LargeInCommits } from './LargeInCommits';
import { RepoPicker } from './RepoPicker';
import { SUGGEST_LABEL, SUGGEST_LABEL_OFFLINE, suggestCommitMessage } from './suggest';

interface GitPanelProps {
  project: Project;
  workspaceId: string;
  onClose: () => void;
}

export default function GitPanel(props: GitPanelProps) {
  const pid = props.project.id;
  const repos = useStudio((s) => s.gitRepos[pid]?.items);
  const repo = useStudio((s) => s.gitRepo[pid] ?? '');

  // every open looks again: a repository may have been cloned in a console since
  useEffect(() => {
    void studioStore.loadGitRepos(pid, true);
  }, [pid]);

  if (!repos) {
    return (
      <aside className="side-panel git-panel" aria-label="Git">
        <div className="side-panel-head">
          <Icon name="branch" />
          <h2>Git</h2>
          <span className="spacer" />
          <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close Git" onClick={props.onClose} />
        </div>
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Looking for repositories…
        </div>
      </aside>
    );
  }
  // ticks, draft and open diffs belong to one repository
  return <RepoPanel key={repo} {...props} repo={repo} repos={repos} />;
}

function RepoPanel({ project, workspaceId, onClose, repo, repos }: GitPanelProps & { repo: string; repos: GitRepoInfo[] }) {
  const pid = project.id;
  const target = useMemo<GitTarget>(() => ({ pid, repo }), [pid, repo]);
  const rev = useStudio((s) => s.gitRev[pid] ?? 0);
  const [st, setSt] = useState<GitStatus | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [excluded, setExcluded] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState<string | null>(null);
  const [message, setMessage] = useState('');
  const [suggested, setSuggested] = useState('');
  const [suggesting, setSuggesting] = useState(false);
  const [committing, setCommitting] = useState<'' | 'commit' | 'push'>('');
  const [large, setLarge] = useState<string[] | null>(null);
  const [discard, setDiscard] = useState<string[] | null>(null);
  const [largeRescan, setLargeRescan] = useState(0);
  const [jobId, setJobId] = useState<string | null>(null);
  const job = useJob(jobId);
  const [section, setSection] = useState<'' | 'history' | 'trash'>('');
  const settings = useEffectiveSettings();
  const useCli = settings.git?.commitWithRunner !== false;

  const apply = useCallback(
    (next: GitStatus) => {
      setSt(next);
      setError('');
      if (next.isRepo && studioStore.gitTarget(pid).repo === repo) studioStore.setGitSummary(pid, { branch: next.branch, ahead: next.ahead, behind: next.behind, changed: next.changed ?? next.files?.length ?? 0 });
    },
    [pid, repo],
  );

  const load = useCallback(async () => {
    try {
      apply(await gitApi.status(target));
    } catch (err) {
      setError(gitError(err));
    } finally {
      setLoading(false);
    }
  }, [target, apply]);

  // first load, then whenever the hub reports a change (debounced)
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      void load();
      return;
    }
    const t = window.setTimeout(() => void load(), 300);
    return () => window.clearTimeout(t);
  }, [rev, load]);

  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.modal-layer') && !(e.target as HTMLElement)?.closest?.('input, textarea, select')) onClose();
    };
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  }, [onClose]);

  const files = useMemo(() => st?.files ?? [], [st]);
  const checked = useMemo(() => files.filter((f) => !excluded.has(f.path)).map((f) => f.path), [files, excluded]);
  const checkedKey = checked.join('\n');

  // the command bar commits what is ticked here (PUT …/git/selection)
  useEffect(() => {
    if (!st?.isRepo) return;
    const t = window.setTimeout(() => void gitApi.selection(target, checked).catch(() => undefined), 400);
    return () => window.clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [checkedKey, st?.isRepo, target]);

  // finished push/pull/fetch/publish: tell, then refresh
  const reported = useRef<string | null>(null);
  useEffect(() => {
    if (!job || jobActive(job) || reported.current === job.id) return;
    reported.current = job.id;
    const s = jobSummary(job);
    if (job.purpose === 'publish' && job.state === 'done') {
      const url = (job.result as { repoUrl?: string } | null)?.repoUrl;
      toast.success(url ? `Published to ${url}.` : 'Published to GitHub.');
    } else if (s.ok) toast.success(s.text);
    else toast.error(s.text);
    if (job.code === 'large_files') setLargeRescan((n) => n + 1);
    void load();
  }, [job, load]);

  const toggle = (path: string) =>
    setExcluded((cur) => {
      const n = new Set(cur);
      if (n.has(path)) n.delete(path);
      else n.add(path);
      return n;
    });

  const suggest = async () => {
    setSuggesting(true);
    try {
      const m = await suggestCommitMessage(target, checked, { useCli });
      setMessage(m);
      setSuggested(m);
    } catch (err) {
      toast.error(`No suggestion: ${gitError(err)}`);
    } finally {
      setSuggesting(false);
    }
  };

  const startJob = (j: Job) => {
    orchStore.upsertJob(j);
    reported.current = null;
    setJobId(j.id);
  };

  const push = async () => {
    if (!st) return;
    try {
      startJob(await gitApi.push(target, !st.upstream));
    } catch (err) {
      toast.error(`Push failed: ${gitError(err)}`);
    }
  };

  const commit = async (andPush: boolean, allowLarge = false) => {
    if (!st) return;
    setCommitting(andPush ? 'push' : 'commit');
    try {
      const msg = message.trim();
      const r = await gitApi.commit(target, { message: msg, paths: checked, allowLarge });
      apply(r.status);
      setMessage('');
      setSuggested('');
      setLarge(null);
      toast.success(`Committed ${r.commit.short} “${r.commit.subject}”.`);
      if (andPush) await push();
    } catch (err) {
      if (isCode(err, 'large_files')) {
        const paths = (err as ApiError).body.paths;
        setLarge(Array.isArray(paths) ? (paths as string[]) : (st.largeFiles ?? []).map((f) => f.path));
      } else toast.error(`Commit failed: ${gitError(err)}`);
    } finally {
      setCommitting('');
    }
  };

  const keepOff = async (paths: string[]) => {
    try {
      apply(await gitApi.untrackLarge(target, paths));
      setLarge(null);
      toast.success(`${paths.length === 1 ? '1 file stays' : `${paths.length} files stay`} on this machine only (added to .gitignore).`);
    } catch (err) {
      toast.error(gitError(err));
    }
  };

  const doDiscard = async (paths: string[]) => {
    const r = await gitApi.discard(target, paths);
    apply(r.status);
    setExcluded(new Set());
    setOpen(null);
    toast.success(`Discarded ${paths.length === 1 ? '1 file' : `${paths.length} files`}. Your versions are in ${r.trashedTo}.`);
  };

  const simple = async (fn: () => Promise<Job | GitStatus>, label: string) => {
    try {
      const r = await fn();
      if ('isRepo' in r) apply(r);
      else startJob(r);
    } catch (err) {
      toast.error(`${label}: ${gitError(err)}`);
    }
  };

  const running = jobActive(job);
  const conflicts = !!st && st.merging && st.conflicts > 0;
  const canPush = !!st && st.hasRemote && !st.detached && !!st.hasCommits && (st.ahead > 0 || !st.upstream);
  const commitPushLabel = st?.detached ? 'Commit' : 'Commit & Push';

  return (
    <aside className="side-panel git-panel" aria-label="Git">
      <div className="side-panel-head">
        <Icon name="branch" />
        <h2>Git</h2>
        <span className="text-3 fs-xs truncate" title={st?.root || project.path}>
          {(st?.root || project.path).split('/').filter(Boolean).pop()}
        </span>
        <span className="spacer" />
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Refresh status" title="Refresh status" onClick={() => void studioStore.loadGitRepos(pid, true).then(load)} />
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close Git" onClick={onClose} />
      </div>

      <RepoPicker pid={pid} className="git-repo-row" disabled={jobActive(job) || !!committing} />

      {loading && !st && (
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Reading the repository…
        </div>
      )}
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}

      {st && !st.isRepo && repos.length > 0 && (
        <div className="git-empty">
          <p>
            <strong>{repo || project.name}</strong> is no longer a Git repository.
          </p>
          <Button size="sm" icon="refresh" onClick={() => void studioStore.loadGitRepos(pid, true)}>
            Scan again
          </Button>
        </div>
      )}
      {st && !st.isRepo && repos.length === 0 && (
        <div className="git-empty">
          <p>
            <strong>{project.name}</strong> isn’t a Git repository yet.
          </p>
          <p className="fs-xs text-3">Create one on the main branch to track changes, commit and publish them. Repositories in subfolders show up here by themselves.</p>
          <Button
            variant="primary"
            size="sm"
            icon="plus"
            onClick={() =>
              void simple(async () => {
                const created = await gitApi.init(target);
                await studioStore.loadGitRepos(pid, true);
                return created;
              }, 'Could not create the repository')
            }
          >
            Create repository
          </Button>
        </div>
      )}

      {st?.isRepo && (
        <>
          <div className="git-sync">
            <span className={`badge ${st.detached ? 'badge-warning' : ''} git-branch`} title={st.detached ? 'Detached HEAD' : `Branch ${st.branch}`}>
              <Icon name="branch" />
              <span className="truncate">{st.detached ? `detached at ${st.branch || 'HEAD'}` : st.branch || 'main'}</span>
            </span>
            <span className="git-sync-text">
              {!st.hasRemote
                ? 'Local only — no remote yet'
                : !st.upstream
                  ? 'Not published yet'
                  : st.ahead || st.behind
                    ? `${st.ahead ? `${st.ahead} to push` : ''}${st.ahead && st.behind ? ' · ' : ''}${st.behind ? `${st.behind} to pull` : ''} vs ${st.upstream}`
                    : `In sync with ${st.upstream}`}
            </span>
          </div>
          {st.hasRemote && (
            <div className="git-fetched fs-xs text-3">
              <span>{st.fetchedAt ? `As of the last fetch, ${timeAgo(st.fetchedAt)}` : 'Never fetched — counts may be old'}</span>
              <span className="spacer" />
              <Button size="sm" variant="ghost" icon="download" disabled={running} onClick={() => void simple(() => gitApi.fetch(target), 'Fetch failed')}>
                Fetch
              </Button>
              {st.webUrl && (
                <a className="btn btn-ghost btn-sm" href={st.webUrl} target="_blank" rel="noopener noreferrer">
                  <Icon name="external" />
                  Open on web
                </a>
              )}
            </div>
          )}

          {(running || (job && !jobActive(job) && job.state === 'failed')) && job && (
            <div className={`job-row ${running ? '' : 'is-failed'}`} role="status">
              {running ? <span className="spinner" aria-hidden="true" /> : <Icon name="alertCircle" />}
              <span className="truncate">{running ? job.progress?.text || `${job.purpose ?? 'Git'}…` : jobSummary(job).text}</span>
              <span className="spacer" />
              {running ? (
                <Button size="sm" variant="ghost" onClick={() => void jobApi.cancel(job.id).then((j) => orchStore.upsertJob(j)).catch(() => undefined)}>
                  Stop
                </Button>
              ) : (
                <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Dismiss" onClick={() => setJobId(null)} />
              )}
            </div>
          )}

          {conflicts && (
            <div className="alert" role="alert">
              <Icon name="alert" />
              <span>
                A merge is in progress with {st.conflicts} conflict{st.conflicts === 1 ? '' : 's'}. Resolve {st.conflicts === 1 ? 'it' : 'them'} in a console; committing is blocked
                until then.
              </span>
            </div>
          )}

          {!!st.largeFiles?.length && <LargeFiles files={st.largeFiles} onKeepOff={(p) => void keepOff(p)} />}
          <LargeInCommits target={target} unpushed={st.ahead} rescan={largeRescan} disabled={running} onStatus={apply} />

          {!st.hasRemote && <PublishCard project={project} target={target} workspaceId={workspaceId} status={st} running={running} onJob={startJob} />}

          {(st.behind > 0 || canPush) && (
            <div className="row-gap wrap">
              {st.behind > 0 && (
                <Button size="sm" icon="download" disabled={running} onClick={() => void simple(() => gitApi.pull(target), 'Pull failed')}>
                  Pull {st.behind}
                </Button>
              )}
              {canPush && (
                <Button size="sm" icon="upload" disabled={running} onClick={() => void push()}>
                  {st.upstream ? `Push ${st.ahead}` : 'Publish branch'}
                </Button>
              )}
            </div>
          )}

          <section className="git-changes" aria-label="Changes">
            <div className="git-changes-head">
              <h3 className="section-title">Changes · {st.changed ?? files.length}</h3>
              <span className="spacer" />
              <Button size="sm" variant="ghost" disabled={!files.length} onClick={() => setExcluded(new Set())}>
                All
              </Button>
              <Button size="sm" variant="ghost" disabled={!files.length} onClick={() => setExcluded(new Set(files.map((f) => f.path)))}>
                None
              </Button>
            </div>
            {files.length === 0 ? (
              <p className="list-msg">Nothing to commit — the working tree is clean.</p>
            ) : (
              <ul className="plain git-files" aria-label="Changed files">
                {files.map((f) => (
                  <FileRow key={f.path} target={target} f={f} checked={!excluded.has(f.path)} onToggle={() => toggle(f.path)} open={open === f.path} onOpen={() => setOpen(open === f.path ? null : f.path)} />
                ))}
              </ul>
            )}
            {st.truncated && <p className="fs-xs text-3">Showing the first {files.length} of {st.changed} changed files.</p>}
          </section>

          {files.length > 0 && (
          <div className="git-commit">
            <div className="git-message">
              <label className="sr-only" htmlFor="git-msg">
                Commit message
              </label>
              <textarea
                id="git-msg"
                className="control prose"
                rows={Math.min(10, message.split('\n').length)}
                value={message}
                readOnly={suggesting}
                placeholder={suggesting ? 'Reading the changes…' : suggested ? '' : 'Commit message'}
                onChange={(e) => setMessage(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && message.trim() && checked.length) void commit(st.hasRemote && !st.detached);
                }}
              />
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                icon="sparkle"
                loading={suggesting}
                disabled={!checked.length}
                aria-label={SUGGEST_LABEL}
                title={useCli ? SUGGEST_LABEL : SUGGEST_LABEL_OFFLINE}
                onClick={() => void suggest()}
              />
            </div>
            <div className="row-gap wrap">
              {st.hasRemote && !st.detached && (
                <Button size="sm" variant="primary" icon="upload" loading={committing === 'push'} disabled={!checked.length || !message.trim() || conflicts || !!committing || running} onClick={() => void commit(true)}>
                  {commitPushLabel}
                </Button>
              )}
              <Button
                size="sm"
                variant={st.hasRemote && !st.detached ? 'secondary' : 'primary'}
                icon="check"
                loading={committing === 'commit'}
                disabled={!checked.length || !message.trim() || conflicts || !!committing}
                onClick={() => void commit(false)}
              >
                {st.hasRemote && !st.detached ? 'Commit only' : 'Commit'}
              </Button>
              <span className="spacer" />
              <Button size="sm" variant="danger-ghost" icon="trash" disabled={!checked.length} onClick={() => setDiscard(checked)}>
                Discard…
              </Button>
            </div>
            <p className="fs-xs text-3">{checked.length === files.length ? `All ${files.length} files` : `${checked.length} of ${files.length} files`} ticked. Your commit hooks run as usual.</p>
          </div>
          )}

          {large && (
            <div className="alert warning" role="alert">
              <Icon name="alert" />
              <div className="stack-sm">
                <span>
                  {large.length === 1 ? 'A file is' : `${large.length} files are`} over 100 MB. GitHub refuses such files; keep {large.length === 1 ? 'it' : 'them'} off GitHub or
                  commit anyway.
                </span>
                <span className="mono fs-xs truncate">{large.slice(0, 3).join(', ')}</span>
                <div className="row-gap wrap">
                  <Button size="sm" variant="primary" onClick={() => void keepOff(large)}>
                    Keep off GitHub
                  </Button>
                  <Button size="sm" onClick={() => void commit(false, true)}>
                    Commit anyway
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setLarge(null)}>
                    Cancel
                  </Button>
                </div>
              </div>
            </div>
          )}

          <div className="git-more" role="tablist" aria-label="More">
            <button type="button" role="tab" aria-selected={section === 'history'} className={`git-more-tab ${section === 'history' ? 'is-active' : ''}`} onClick={() => setSection(section === 'history' ? '' : 'history')}>
              <Icon name="history" /> History
            </button>
            <button type="button" role="tab" aria-selected={section === 'trash'} className={`git-more-tab ${section === 'trash' ? 'is-active' : ''}`} onClick={() => setSection(section === 'trash' ? '' : 'trash')}>
              <Icon name="trash" /> Discarded
            </button>
          </div>
          {section === 'history' && <History target={target} rev={rev} />}
          {section === 'trash' && <Trash target={target} rev={rev} onRestored={() => void load()} />}
        </>
      )}

      {discard && (
        <ConfirmDialog
          title={discard.length === 1 ? 'Discard changes to 1 file?' : `Discard changes to ${discard.length} files?`}
          confirmLabel={discard.length === 1 ? 'Discard file' : `Discard ${discard.length} files`}
          danger
          onClose={() => setDiscard(null)}
          onConfirm={() => doDiscard(discard)}
        >
          <p>Tracked files go back to the last commit. A copy of your version, and every new file, moves to the project’s trash (.git/nextboard-trash) — restore them from Discarded.</p>
          <ul className="plain mono fs-xs discard-list">
            {discard.slice(0, 8).map((p) => (
              <li key={p} className="truncate">
                {p}
              </li>
            ))}
            {discard.length > 8 && <li>+ {discard.length - 8} more</li>}
          </ul>
        </ConfirmDialog>
      )}
    </aside>
  );
}

function FileRow({ target, f, checked, onToggle, open, onOpen }: { target: GitTarget; f: GitFile; checked: boolean; onToggle: () => void; open: boolean; onOpen: () => void }) {
  const { dir, name } = splitPath(f.path);
  const counts = lineCounts(f);
  return (
    <li className={`git-file ${open ? 'is-open' : ''}`}>
      <div className="git-file-row">
        <span className="check">
          <input type="checkbox" checked={checked} onChange={onToggle} aria-label={`Include ${f.path}`} />
        </span>
        <span className={`git-status st-${f.status}`} title={STATUS_LABEL[f.status] ?? f.status} aria-label={STATUS_LABEL[f.status] ?? f.status}>
          {STATUS_LETTER[f.status] ?? '?'}
        </span>
        <button type="button" className="git-file-name" aria-expanded={open} title={f.oldPath ? `${f.oldPath} → ${f.path}` : f.path} onClick={onOpen}>
          <span className="truncate">
            {dir && <span className="text-3">{dir}</span>}
            {name}
          </span>
        </button>
        {counts && (
          <span className="git-counts tabular" title={f.binary ? 'Binary file' : `${f.added} lines added, ${f.deleted} removed`}>
            {f.binary ? 'bin' : (
              <>
                <span className="add">+{f.added}</span> <span className="del">−{f.deleted}</span>
              </>
            )}
          </span>
        )}
      </div>
      {open && <DiffView target={target} path={f.path} />}
    </li>
  );
}

function DiffView({ target, path }: { target: GitTarget; path: string }) {
  const [d, setD] = useState<GitDiff | null>(null);
  const [err, setErr] = useState('');
  useEffect(() => {
    let stop = false;
    gitApi
      .diff(target, path)
      .then((x) => !stop && setD(x))
      .catch((e) => !stop && setErr(gitError(e)));
    return () => {
      stop = true;
    };
  }, [target, path]);
  if (err) return <p className="field-error">{err}</p>;
  if (!d) return <p className="fs-xs text-3">Loading the diff…</p>;
  if (d.binary) return <p className="fs-xs text-3">Binary file — no text diff.</p>;
  const lines = d.diff.split('\n');
  return (
    <div className="git-diff nowheel" role="region" aria-label={`Diff of ${path}`} tabIndex={0}>
      {lines.map((l, i) => (
        <div key={i} className={`dl ${l.startsWith('@@') ? 'hunk' : l.startsWith('+') && !l.startsWith('+++') ? 'add' : l.startsWith('-') && !l.startsWith('---') ? 'del' : /^(diff|index|---|\+\+\+|new file|deleted file|similarity|rename)/.test(l) ? 'meta' : ''}`}>
          {l || ' '}
        </div>
      ))}
      {d.truncated && <div className="dl meta">… diff cut at 512 KB</div>}
    </div>
  );
}

function LargeFiles({ files, onKeepOff }: { files: NonNullable<GitStatus['largeFiles']>; onKeepOff: (paths: string[]) => void }) {
  const untracked = files.filter((f) => !f.tracked).map((f) => f.path);
  return (
    <div className="alert warning" role="status">
      <Icon name="alert" />
      <div className="stack-sm">
        <span>
          {files.length === 1 ? '1 file is' : `${files.length} files are`} over 100 MB — GitHub refuses files that big.
        </span>
        <ul className="plain mono fs-xs">
          {files.slice(0, 4).map((f) => (
            <li key={f.path} className="truncate">
              {f.path} · {formatBytes(f.sizeBytes)}
            </li>
          ))}
        </ul>
        {untracked.length > 0 && (
          <div>
            <Button size="sm" onClick={() => onKeepOff(files.map((f) => f.path))}>
              Keep off GitHub
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

function History({ target, rev }: { target: GitTarget; rev: number }) {
  const [items, setItems] = useState<GitCommit[] | null>(null);
  useEffect(() => {
    gitApi
      .log(target, 100)
      .then(setItems)
      .catch(() => setItems([]));
  }, [target, rev]);
  if (!items) return <p className="list-msg">Loading history…</p>;
  if (!items.length) return <p className="list-msg">No commits yet.</p>;
  return (
    <ul className="plain git-log" aria-label="Last 100 commits">
      {items.map((c) => (
        <li key={c.hash} className="git-commit-row">
          <span className={`git-pushed ${c.pushed ? 'is-pushed' : ''}`} title={c.pushed ? 'Pushed' : 'Not pushed yet'} aria-label={c.pushed ? 'Pushed' : 'Not pushed yet'}>
            <Icon name={c.pushed ? 'check' : 'upload'} />
          </span>
          <span className="mono fs-xs text-3">{c.short}</span>
          <span className="truncate" title={c.subject}>
            {c.subject}
          </span>
          <span className="spacer" />
          <span className="fs-xs text-3 nowrap" title={`${c.author} · ${new Date(c.at).toLocaleString()}`}>
            {timeAgo(c.at)}
          </span>
        </li>
      ))}
    </ul>
  );
}

function Trash({ target, rev, onRestored }: { target: GitTarget; rev: number; onRestored: () => void }) {
  const [items, setItems] = useState<GitTrashItem[] | null>(null);
  const reload = useCallback(() => {
    gitApi
      .trash(target)
      .then(setItems)
      .catch(() => setItems([]));
  }, [target]);
  useEffect(reload, [reload, rev]);
  const restore = async (it: GitTrashItem) => {
    try {
      const r = await gitApi.restore(target, it.id);
      toast.success(`Restored ${r.restored?.length ?? 0} file${r.restored?.length === 1 ? '' : 's'}.`);
      onRestored();
      reload();
    } catch (err) {
      const paths = isCode(err, 'exists') ? ((err as ApiError).body.paths as string[] | undefined) : undefined;
      toast.error(paths?.length ? `Not restored: ${paths.slice(0, 3).join(', ')} already exist${paths.length === 1 ? 's' : ''}. Move or discard them first.` : gitError(err));
    }
  };
  if (!items) return <p className="list-msg">Loading…</p>;
  if (!items.length) return <p className="list-msg">Nothing discarded yet. Discarded versions land here so you can bring them back.</p>;
  return (
    <ul className="plain git-trash">
      {items.map((it) => (
        <li key={it.id} className="git-trash-row">
          <div className="grow">
            <div className="fs-sm">
              {it.paths?.length ?? 0} file{it.paths?.length === 1 ? '' : 's'} · {timeAgo(it.at)}
            </div>
            <div className="mono fs-xs text-3 truncate">{(it.paths ?? []).slice(0, 3).join(', ')}</div>
          </div>
          <Button size="sm" icon="undo" onClick={() => void restore(it)}>
            Restore
          </Button>
        </li>
      ))}
    </ul>
  );
}

/** No remote yet: publish to GitHub with the gh CLI of the project's machine (B21). */
function PublishCard({ project, target, workspaceId, status, running, onJob }: { project: Project; target: GitTarget; workspaceId: string; status: GitStatus; running: boolean; onJob: (j: Job) => void }) {
  const pid = project.id;
  const gh = useStudio((s) => s.github[pid]);
  const [loading, setLoading] = useState(!gh);
  const [name, setName] = useState(() => (status.root || project.path).split('/').filter(Boolean).pop() ?? project.name);
  const [visibility, setVisibility] = useState<'private' | 'public'>('private');
  const [commitAll, setCommitAll] = useState(true);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    gitApi
      .github(target)
      .then((g) => studioStore.setGitHub(pid, g))
      .catch(() => undefined)
      .finally(() => setLoading(false));
  }, [pid, target]);
  useEffect(load, [load]);

  const connect = async () => {
    try {
      await gitApi.githubLogin(pid, workspaceId);
      toast.info('A console on the board runs gh auth login. Finish the sign-in there; this panel updates by itself.');
    } catch (err) {
      toast.error(`Could not start the sign-in: ${gitError(err)}`);
    }
  };

  const publish = async () => {
    setBusy(true);
    try {
      onJob(await gitApi.publish(target, { name: name.trim(), visibility, commitAll }));
    } catch (err) {
      toast.error(`Publish failed: ${gitError(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="git-publish" aria-label="Publish to GitHub">
      <h3 className="section-title">Publish to GitHub</h3>
      {loading ? (
        <p className="fs-xs text-3">Checking the GitHub CLI…</p>
      ) : !gh?.ghInstalled ? (
        <div className="stack-sm">
          <p className="fs-sm">Publishing uses the GitHub CLI (gh) on this project’s machine, and it isn’t installed there.</p>
          {gh?.installHint && (
            <div className="git-hint">
              <code className="mono">{gh.installHint}</code>
              <Button size="sm" variant="ghost" iconOnly icon="copy" aria-label="Copy the install command" onClick={() => void navigator.clipboard?.writeText(gh.installHint).then(() => toast.success('Copied.'))} />
            </div>
          )}
          <Button size="sm" variant="ghost" icon="refresh" onClick={load}>
            Check again
          </Button>
        </div>
      ) : !gh.authenticated ? (
        <div className="stack-sm">
          <p className="fs-sm">The GitHub CLI isn’t signed in. Sign in once in a console; the token stays in gh’s own keyring on that machine.</p>
          <div>
            <Button size="sm" variant="primary" icon="user" onClick={() => void connect()}>
              Connect GitHub account
            </Button>
          </div>
        </div>
      ) : (
        <div className="stack-sm">
          <p className="fs-xs text-3">Signed in as {gh.user || 'your account'}. Creates the repository, adds it as origin and pushes.</p>
          <label className="field-label" htmlFor="gh-name">
            Repository name
          </label>
          <input id="gh-name" className="control control-sm mono" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} />
          <SegmentedControl<'private' | 'public'>
            value={visibility}
            onChange={setVisibility}
            size="sm"
            label="Visibility"
            options={[
              { value: 'private', label: 'Private' },
              { value: 'public', label: 'Public' },
            ]}
          />
          <label className="check">
            <input type="checkbox" checked={commitAll} onChange={(e) => setCommitAll(e.target.checked)} />
            Commit every change first
          </label>
          <div>
            <Button size="sm" variant="primary" icon="upload" loading={busy} disabled={!name.trim() || running} onClick={() => void publish()}>
              {commitAll ? 'Create repo & push everything' : 'Create repo & push'}
            </Button>
          </div>
        </div>
      )}
    </section>
  );
}
