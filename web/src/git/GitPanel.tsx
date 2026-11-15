// Git panel (G; docs/API-studio.md §3, B20–B21): status and sync of the project's repo,
// the changes list with checkboxes (synced to the hub so the command bar's "commit and
// push" uses the same files), per-file diff, a suggested subject, Commit & Push /
// Commit, Push / Publish branch, Pull, Fetch (only when clicked), Discard to the
// project trash (with restore), branches, the stash, the history graph, files over 100 MB,
// Create repository and Publish to GitHub through the gh CLI of the project's machine. A
// project folder may hold several repositories (§3.6): the panel scans it and works on the one
// picked. A project in several parts (ROADMAP #49) shows every part at once, each with its own
// repositories, and each opens on its own.

import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { isCode, type ApiError } from '../api';
import { useEffectiveSettings } from '../agents/settings';
import type { UiStore } from '../board/ui';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { SegmentedControl } from '../components/form';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { formatDateTime, prefs, timeAgo } from '../lib/format';
import { projectParts } from '../lib/parts';
import type { Project, ProjectPart } from '../lib/types';
import { orchStore } from '../orch/store';
import type { Job } from '../orch/types';
import { useMachines } from '../store';
import { gitApi, jobApi } from '../studio/api';
import { HistorySection, RefPills, type GitHost } from '../studio/code/CommitGraph';
import { StashSection } from '../studio/code/StashSection';
import { jobActive, studioStore, useJob, useStudio } from '../studio/store';
import type { GitBranch, GitCommitDetail, GitDiff, GitFile, GitRepoInfo, GitStatus, GitSummary, GitTarget, GitTrashItem } from '../studio/types';
import { formatBytes, gitError, jobSummary, lineCounts, splitPath, STATUS_LABEL, STATUS_LETTER } from './common';
import { LargeInCommits } from './LargeInCommits';
import { showCommitInEditor } from './openInEditor';
import { openPartsFrom, partRef, sumRepos, summaryLabel } from './parts';
import { RepoPicker } from './RepoPicker';
import { SUGGEST_LABEL, SUGGEST_LABEL_OFFLINE, suggestCommitMessage } from './suggest';
import '../styles/workbench.css';
import '../styles/git-panel.css';

interface GitPanelProps {
  project: Project;
  workspaceId: string;
  onClose: () => void;
  /** Shows the editor card, with this file when given: "Open in editor" of a commit. */
  openEditor?: (path?: string) => void;
  /** The board's UI state, which the editor card reads files from. */
  ui?: UiStore;
}

const OPEN_PREF = (projectId: string) => `git.parts.${projectId}`;

export default function GitPanel(props: GitPanelProps) {
  const parts = projectParts(props.project);
  const { onClose } = props;

  // one Escape for the whole panel, whatever is open in it
  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.modal-layer') && !(e.target as HTMLElement)?.closest?.('input, textarea, select')) onClose();
    };
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  }, [onClose]);

  return parts.length < 2 ? <OnePart {...props} part={parts[0]} /> : <AllParts {...props} parts={parts} />;
}

/** A project in one folder: the panel as it always was. */
function OnePart(props: GitPanelProps & { part: ProjectPart }) {
  const pref = props.project.id;
  const repos = useStudio((s) => s.gitRepos[pref]?.items);
  const repo = useStudio((s) => s.gitRepo[pref] ?? '');

  // every open looks again: a repository may have been cloned in a console since
  useEffect(() => {
    void studioStore.loadGitRepos(pref, true);
  }, [pref]);

  return (
    <aside className="side-panel git-panel" aria-label="Git">
      {!repos ? (
        <>
          <div className="side-panel-head">
            <Icon name="branch" />
            <h2>Git</h2>
            <span className="spacer" />
            <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close Git" onClick={props.onClose} />
          </div>
          <div className="list-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Looking for repositories…
          </div>
        </>
      ) : (
        // ticks, draft and open diffs belong to one repository
        <RepoPanel key={repo} {...props} pref={pref} label={props.project.name} repo={repo} repos={repos} picker head refresh={0} />
      )}
    </aside>
  );
}

/** A project in several parts: every part's git at once, each part opens on its own. */
function AllParts(props: GitPanelProps & { parts: ProjectPart[] }) {
  const { project, parts, onClose } = props;
  const machines = useMachines();
  const machineName = (id: string) => machines.find((m) => m.id === id)?.name ?? id;
  const [open, setOpen] = useState(() => openPartsFrom(prefs.get(OPEN_PREF(project.id)), parts));
  const [refresh, setRefresh] = useState(0);
  const refs = parts.map((pt) => partRef(project, pt));
  const refsKey = refs.join('\n');

  const scan = useCallback(() => Promise.all(refsKey.split('\n').map((r) => studioStore.loadGitRepos(r, true))), [refsKey]);
  // every open looks again, in every part
  useEffect(() => {
    void scan();
  }, [scan]);

  const toggle = (id: string, to?: boolean) =>
    setOpen((cur) => {
      const next = new Set(cur);
      if (to ?? !next.has(id)) next.add(id);
      else next.delete(id);
      prefs.set(OPEN_PREF(project.id), JSON.stringify([...next]));
      return next;
    });

  return (
    <aside className="side-panel git-panel git-panel-parts" aria-label="Git">
      <div className="side-panel-head">
        <Icon name="branch" />
        <h2>Git</h2>
        <span className="text-3 fs-xs">{parts.length} parts</span>
        <span className="spacer" />
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Refresh every part" title="Refresh every part" onClick={() => void scan().then(() => setRefresh((n) => n + 1))} />
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close Git" onClick={onClose} />
      </div>
      {parts.map((pt, i) => (
        <PartSection key={pt.id} {...props} part={pt} pref={refs[i]} machine={machineName(pt.machineId)} open={open.has(pt.id)} onToggle={(to) => toggle(pt.id, to)} refresh={refresh} />
      ))}
    </aside>
  );
}

function RepoStat({ s, error }: { s: GitSummary; error?: string }) {
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

/** One part: a header with its repositories' state, and the whole panel for it when open. */
function PartSection(props: GitPanelProps & { part: ProjectPart; pref: string; machine: string; open: boolean; onToggle: (to?: boolean) => void; refresh: number }) {
  const { part, pref, machine, open, onToggle } = props;
  const list = useStudio((s) => s.gitRepos[pref]);
  const repo = useStudio((s) => s.gitRepo[pref] ?? '');
  const [busy, setBusy] = useState(false);
  const bodyId = useId();
  const items = list?.items;
  const sum = sumRepos(items);

  return (
    <section className={`git-part ${open ? 'is-open' : ''}`} aria-label={`Part ${part.name}`}>
      <h3 className="git-part-head">
        <button type="button" className="git-part-toggle" aria-expanded={open} aria-controls={open ? bodyId : undefined} onClick={() => onToggle()}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} className="git-part-chev" />
          <span className="git-part-title">
            <span className="git-part-name truncate">{part.name}</span>
            <span className="git-part-machine truncate" title={part.path}>
              {machine}
            </span>
          </span>
          {!items ? (
            <span className="spinner" role="status" aria-label="Reading" />
          ) : items.length === 0 ? (
            <span className="git-part-stat text-3">No Git repository</span>
          ) : items.length === 1 ? (
            <RepoStat s={items[0]} error={items[0].error} />
          ) : (
            <span className="git-part-stat tabular">
              <span>{items.length} repositories</span>
              {!!sum.ahead && <span>↑{sum.ahead}</span>}
              {!!sum.behind && <span>↓{sum.behind}</span>}
              <span className={sum.changed ? 'git-part-changed' : 'text-3'}>{sum.changed} changed</span>
            </span>
          )}
        </button>
      </h3>
      {items && items.length > 1 && (
        <ul className="plain git-part-repos" aria-label={`Repositories of ${part.name}`}>
          {items.map((r) => (
            <li key={r.path}>
              <button
                type="button"
                className={`git-part-repo ${open && r.path === repo ? 'is-current' : ''}`}
                aria-current={open && r.path === repo ? 'true' : undefined}
                disabled={busy}
                title={r.root || r.path}
                onClick={() => {
                  studioStore.selectGitRepo(pref, r.path);
                  onToggle(true);
                }}
              >
                <span className="git-part-repo-name truncate">{r.path || `${r.name} (part folder)`}</span>
                <RepoStat s={r} error={r.error} />
              </button>
            </li>
          ))}
          {list?.truncated && <li className="fs-xs text-3">Not every repository is listed</li>}
        </ul>
      )}
      {open && (
        <div className="git-part-body" id={bodyId}>
          {!items ? (
            <div className="list-msg" role="status">
              <span className="spinner" aria-hidden="true" /> Looking for repositories…
            </div>
          ) : (
            <RepoPanel key={repo} {...props} label={part.name} repo={repo} repos={items} onBusy={setBusy} />
          )}
        </div>
      )}
    </section>
  );
}

interface RepoPanelProps extends GitPanelProps {
  part: ProjectPart;
  /** The part's git, as the hub names it: the project, or "<project>~<part>". */
  pref: string;
  /** What the messages call it: the project, or the part in a project of several. */
  label: string;
  repo: string;
  repos: GitRepoInfo[];
  /** The repository picker and the panel's own title: a project of one part. */
  picker?: boolean;
  head?: boolean;
  /** Moves when the whole panel was refreshed. */
  refresh: number;
  onBusy?: (busy: boolean) => void;
}

function RepoPanel({ project, part, pref, label, workspaceId, onClose, openEditor, ui, repo, repos, picker, head, refresh, onBusy }: RepoPanelProps) {
  const target = useMemo<GitTarget>(() => ({ pid: pref, repo }), [pref, repo]);
  const rev = useStudio((s) => s.gitRev[pref] ?? 0);
  const msgId = useId();
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
  const [peek, setPeek] = useState<{ hash: string; label: string } | null>(null);
  const settings = useEffectiveSettings();
  const useCli = settings.git?.commitWithRunner !== false;

  const apply = useCallback(
    (next: GitStatus) => {
      setSt(next);
      setError('');
      if (next.isRepo && studioStore.gitTarget(pref).repo === repo) studioStore.setGitSummary(pref, { branch: next.branch, ahead: next.ahead, behind: next.behind, changed: next.changed ?? next.files?.length ?? 0 });
    },
    [pref, repo],
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

  // first load, then whenever the hub reports a change (debounced), or the panel is refreshed
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      void load();
      return;
    }
    const t = window.setTimeout(() => void load(), 300);
    return () => window.clearTimeout(t);
  }, [rev, refresh, load]);

  // the history graph and the stash of the editor's Source Control, working on this repository
  const live = useRef({ load, rev, st, peek });
  live.current = { load, rev, st, peek };
  const host = useMemo<GitHost>(
    () => ({
      gitTarget: target,
      get gitVersion() {
        return live.current.rev;
      },
      get git() {
        return live.current.st;
      },
      refreshGit: () => live.current.load(),
      openCommit: (hash, o) => setPeek({ hash, label: o?.label ?? hash.slice(0, 8) }),
      get activeTab() {
        const p = live.current.peek;
        return p ? { commit: { hash: p.hash } } : null;
      },
    }),
    [target],
  );

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
    const where = head ? '' : `${label}: `;
    if (job.purpose === 'publish' && job.state === 'done') {
      const url = (job.result as { repoUrl?: string } | null)?.repoUrl;
      toast.success(`${where}${url ? `Published to ${url}.` : 'Published to GitHub.'}`);
    } else if (s.ok) toast.success(`${where}${s.text}`);
    else toast.error(`${where}${s.text}`);
    if (job.code === 'large_files') setLargeRescan((n) => n + 1);
    void load();
  }, [job, load, head, label]);

  const running = jobActive(job);
  // switching the repository would lose the job and the draft
  useEffect(() => {
    onBusy?.(running || !!committing);
  }, [running, committing, onBusy]);

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
      toast.success(`Committed ${r.commit.short} “${r.commit.subject}”${head ? '' : ` in ${label}`}.`);
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

  const simple = async (fn: () => Promise<Job | GitStatus>, what: string) => {
    try {
      const r = await fn();
      if ('isRepo' in r) apply(r);
      else startJob(r);
    } catch (err) {
      toast.error(`${what}: ${gitError(err)}`);
    }
  };

  const conflicts = !!st && st.merging && st.conflicts > 0;
  const canPush = !!st && st.hasRemote && !st.detached && !!st.hasCommits && (st.ahead > 0 || !st.upstream);
  const commitPushLabel = st?.detached ? 'Commit' : 'Commit & Push';
  const folder = part.path || project.path;

  return (
    <>
      {head && (
        <div className="side-panel-head">
          <Icon name="branch" />
          <h2>Git</h2>
          <span className="text-3 fs-xs truncate" title={st?.root || folder}>
            {(st?.root || folder).split('/').filter(Boolean).pop()}
          </span>
          <span className="spacer" />
          <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Refresh status" title="Refresh status" onClick={() => void studioStore.loadGitRepos(pref, true).then(load)} />
          <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close Git" onClick={onClose} />
        </div>
      )}

      {picker && <RepoPicker pid={pref} className="git-repo-row" disabled={running || !!committing} />}

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
            <strong>{repo || label}</strong> is no longer a Git repository.
          </p>
          <Button size="sm" icon="refresh" onClick={() => void studioStore.loadGitRepos(pref, true)}>
            Scan again
          </Button>
        </div>
      )}
      {st && !st.isRepo && repos.length === 0 && (
        <div className="git-empty">
          <p>
            <strong>{label}</strong> isn’t a Git repository yet.
          </p>
          <p className="fs-xs text-3">Create one on the main branch to track changes, commit and publish them. Repositories in subfolders show up here by themselves.</p>
          <Button
            variant="primary"
            size="sm"
            icon="plus"
            onClick={() =>
              void simple(async () => {
                const created = await gitApi.init(target);
                await studioStore.loadGitRepos(pref, true);
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

          {!st.hasRemote && <PublishCard project={project} pref={pref} target={target} folder={folder} label={label} workspaceId={workspaceId} status={st} running={running} onJob={startJob} />}

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
                <label className="sr-only" htmlFor={msgId}>
                  Commit message
                </label>
                <textarea
                  id={msgId}
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
              <p className="fs-xs text-3">
                {checked.length === files.length ? `All ${files.length} files` : `${checked.length} of ${files.length} files`} ticked. Your commit hooks run as usual.
              </p>
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

          <div className="git-scm">
            <BranchesSection target={target} rev={rev} disabled={running || !!committing} onChanged={() => void load()} />
            <StashSection ws={host} defaultOpen={false} />
            {peek && (
              <CommitPeek
                key={peek.hash}
                target={target}
                hash={peek.hash}
                onClose={() => setPeek(null)}
                onOpen={
                  openEditor
                    ? (file) => {
                        const root = st.root?.replace(/\/+$/, '');
                        void showCommitInEditor({ project, part, hash: peek.hash, label: peek.label, file: root && file ? `${root}/${file}` : undefined, openEditor, ui }).catch((err) => toast.error(gitError(err)));
                      }
                    : undefined
                }
              />
            )}
            <HistorySection ws={host} defaultOpen={false} />
            <TrashSection target={target} rev={rev} onRestored={() => void load()} />
          </div>
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
    </>
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
            {f.binary ? (
              'bin'
            ) : (
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
        <span>{files.length === 1 ? '1 file is' : `${files.length} files are`} over 100 MB — GitHub refuses files that big.</span>
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

/** A section that opens and closes, as Source Control's in the editor. */
function Fold({ title, count, act, open, onOpen, children }: { title: string; count?: number; act?: ReactNode; open: boolean; onOpen: () => void; children: ReactNode }) {
  return (
    <div className="git-fold">
      <div className="wb-section-title wb-history-head">
        <button type="button" className="wb-history-toggle" aria-expanded={open} onClick={onOpen}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} className="wb-chev" />
          <span>{title}</span>
        </button>
        {act}
        {count !== undefined && <span className="wb-result-count tabular">{count}</span>}
      </div>
      {open && children}
    </div>
  );
}

/** The local branches: switch (git refuses when it would overwrite work), create, delete. */
function BranchesSection({ target, rev, disabled, onChanged }: { target: GitTarget; rev: number; disabled: boolean; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<GitBranch[] | null>(null);
  const [error, setError] = useState('');
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState('');

  const load = useCallback(() => {
    gitApi
      .branches(target)
      .then((r) => {
        setItems(r.items ?? []);
        setError('');
      })
      .catch((err) => setError(gitError(err)));
  }, [target]);
  useEffect(() => {
    if (open) load();
  }, [open, load, rev]);

  const act = async (key: string, fn: () => Promise<unknown>, done: string): Promise<boolean> => {
    setBusy(key);
    try {
      await fn();
      toast.success(done);
      load();
      onChanged();
      return true;
    } catch (err) {
      toast.error(gitError(err));
      return false;
    } finally {
      setBusy('');
    }
  };
  const create = () => {
    const n = name.trim();
    if (!n) return;
    void act('new', () => gitApi.createBranch(target, n, true), `Created ${n} and switched to it.`).then((ok) => {
      if (!ok) return;
      setName('');
      setAdding(false);
    });
  };

  return (
    <Fold
      title="Branches"
      count={items?.length}
      open={open}
      onOpen={() => setOpen((v) => !v)}
      act={
        <button
          type="button"
          className="wb-section-act"
          disabled={disabled}
          onClick={() => {
            setOpen(true);
            setAdding(true);
          }}
        >
          New branch
        </button>
      }
    >
      {adding && (
        <div className="git-branch-new">
          <input
            className="control control-sm mono"
            value={name}
            autoFocus
            placeholder="feature/new-thing"
            aria-label="New branch name"
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') create();
              if (e.key === 'Escape') {
                e.stopPropagation();
                setAdding(false);
              }
            }}
          />
          <Button size="sm" variant="primary" loading={busy === 'new'} disabled={!name.trim() || disabled} onClick={create}>
            Create
          </Button>
        </div>
      )}
      {error && (
        <div className="wb-row-msg is-error" role="alert">
          {error}
        </div>
      )}
      {!items && !error && <div className="wb-row-msg">Loading…</div>}
      {items?.map((b) => (
        <div key={b.name} className={`wb-row git-branch-row ${b.current ? 'is-selected' : ''}`}>
          <button
            type="button"
            className="git-branch-switch"
            disabled={b.current || !!busy || disabled}
            aria-current={b.current || undefined}
            title={b.current ? 'Current branch' : `Switch to ${b.name}`}
            onClick={() => void act(b.name, () => gitApi.switchBranch(target, b.name), `Switched to ${b.name}.`)}
          >
            {busy === b.name ? <span className="spinner" aria-hidden="true" /> : <Icon name={b.current ? 'check' : 'branch'} className="wb-icon" />}
            <span className="wb-row-name truncate">{b.name}</span>
            <span className="wb-result-dir tabular">
              {b.ahead ? `↑${b.ahead} ` : ''}
              {b.behind ? `↓${b.behind} ` : ''}
              {b.lastCommitAt ? timeAgo(b.lastCommitAt) : ''}
            </span>
          </button>
          {!b.current && (
            <button
              type="button"
              className="wb-result-act"
              aria-label={`Delete branch ${b.name}`}
              title="Delete (git branch -d: unmerged branches are kept)"
              disabled={!!busy || disabled}
              onClick={() => void act(`del:${b.name}`, () => gitApi.deleteBranch(target, b.name), `Deleted ${b.name}.`)}
            >
              <Icon name="trash" />
            </button>
          )}
        </div>
      ))}
    </Fold>
  );
}

/** A commit picked in the graph or the stash: what it changed; the diffs open in the editor. */
function CommitPeek({ target, hash, onClose, onOpen }: { target: GitTarget; hash: string; onClose: () => void; onOpen?: (file?: string) => void }) {
  const [d, setD] = useState<GitCommitDetail | null>(null);
  const [err, setErr] = useState('');
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let stop = false;
    gitApi
      .commitDetail(target, hash)
      .then((x) => !stop && setD(x))
      .catch((e) => !stop && setErr(gitError(e)));
    return () => {
      stop = true;
    };
  }, [target, hash]);
  useEffect(() => {
    box.current?.scrollIntoView?.({ block: 'nearest' });
  }, [d]);
  const kept = d?.files.find((f) => f.status !== 'deleted');
  const add = d?.files.reduce((n, f) => n + f.add, 0) ?? 0;
  const del = d?.files.reduce((n, f) => n + f.del, 0) ?? 0;
  return (
    <div className="git-peek" ref={box} role="region" aria-label={d ? `Commit ${d.short}` : 'Commit'}>
      <div className="git-peek-head">
        <span className="mono fs-xs text-3">{d?.short ?? hash.slice(0, 7)}</span>
        <strong className="git-peek-subject truncate" title={d?.subject}>
          {d?.subject ?? ''}
        </strong>
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close the commit" onClick={onClose} />
      </div>
      {err && <p className="field-error">{err}</p>}
      {!d && !err && <p className="fs-xs text-3">Loading the commit…</p>}
      {d && (
        <>
          <div className="git-peek-meta fs-xs text-3">
            <span className="truncate" title={`${d.author}${d.email ? ` <${d.email}>` : ''}\n${formatDateTime(d.at)}`}>
              {d.author} · {timeAgo(d.at)}
            </span>
            <RefPills refs={d.refs} color={0} max={3} />
          </div>
          {d.body.trim() && <p className="git-peek-body fs-xs">{d.body.trim()}</p>}
          <ul className="plain git-peek-files" aria-label={`Files of ${d.short}`}>
            {d.files.map((f) => {
              const { dir, name } = splitPath(f.path);
              return (
                <li key={`${f.oldPath ?? ''}:${f.path}`} className="git-file-row">
                  <span className={`git-status st-${f.status}`} title={STATUS_LABEL[f.status] ?? f.status} aria-label={STATUS_LABEL[f.status] ?? f.status}>
                    {STATUS_LETTER[f.status] ?? '?'}
                  </span>
                  <span className="git-peek-path truncate" title={f.oldPath ? `${f.oldPath} → ${f.path}` : f.path}>
                    {dir && <span className="text-3">{dir}</span>}
                    {name}
                  </span>
                  <span className="git-counts tabular" title={f.binary ? 'Binary file' : `${f.add} lines added, ${f.del} removed`}>
                    {f.binary ? (
                      'bin'
                    ) : (
                      <>
                        <span className="add">+{f.add}</span> <span className="del">−{f.del}</span>
                      </>
                    )}
                  </span>
                </li>
              );
            })}
          </ul>
          <div className="git-peek-foot">
            <span className="fs-xs text-3 tabular">
              {d.files.length} file{d.files.length === 1 ? '' : 's'}
              {d.truncated ? '+' : ''} ·{' '}
              <span className="git-counts">
                <span className="add">+{add}</span> <span className="del">−{del}</span>
              </span>
            </span>
            <span className="spacer" />
            {onOpen && (
              <Button size="sm" icon="code" onClick={() => onOpen(kept?.path)}>
                Open in editor
              </Button>
            )}
          </div>
        </>
      )}
    </div>
  );
}

function TrashSection({ target, rev, onRestored }: { target: GitTarget; rev: number; onRestored: () => void }) {
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<GitTrashItem[] | null>(null);
  const reload = useCallback(() => {
    gitApi
      .trash(target)
      .then(setItems)
      .catch(() => setItems([]));
  }, [target]);
  useEffect(() => {
    if (open) reload();
  }, [open, reload, rev]);
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
  return (
    <Fold title="Discarded" count={open ? items?.length : undefined} open={open} onOpen={() => setOpen((v) => !v)}>
      {!items ? (
        <div className="wb-row-msg">Loading…</div>
      ) : !items.length ? (
        <div className="wb-row-msg">Nothing discarded yet. Discarded versions land here so you can bring them back.</div>
      ) : (
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
      )}
    </Fold>
  );
}

/** No remote yet: publish to GitHub with the gh CLI of the part's machine (B21). */
function PublishCard({
  project,
  pref,
  target,
  folder,
  label,
  workspaceId,
  status,
  running,
  onJob,
}: {
  project: Project;
  pref: string;
  target: GitTarget;
  folder: string;
  label: string;
  workspaceId: string;
  status: GitStatus;
  running: boolean;
  onJob: (j: Job) => void;
}) {
  const gh = useStudio((s) => s.github[pref]);
  // the hub's "github" event names only the project: each part asks its own machine again
  const signal = useStudio((s) => JSON.stringify(s.github[project.id] ?? null));
  const nameId = useId();
  const [loading, setLoading] = useState(!gh);
  const [name, setName] = useState(() => (status.root || folder).split('/').filter(Boolean).pop() ?? label);
  const [visibility, setVisibility] = useState<'private' | 'public'>('private');
  const [commitAll, setCommitAll] = useState(true);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    gitApi
      .github(target)
      .then((g) => studioStore.setGitHub(pref, g))
      .catch(() => undefined)
      .finally(() => setLoading(false));
  }, [pref, target]);
  useEffect(load, [load]);
  const seen = useRef(signal);
  useEffect(() => {
    if (seen.current === signal) return;
    seen.current = signal;
    load();
  }, [signal, load]);

  const connect = async () => {
    try {
      await gitApi.githubLogin(pref, workspaceId);
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
          <p className="fs-sm">Publishing uses the GitHub CLI (gh) on this machine, and it isn’t installed there.</p>
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
          <label className="field-label" htmlFor={nameId}>
            Repository name
          </label>
          <input id={nameId} className="control control-sm mono" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} />
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
