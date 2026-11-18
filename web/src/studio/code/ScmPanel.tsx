// Source control view: changed files with checkboxes, commit, push, pull, fetch, a
// suggested subject and the history graph. Clicking a file opens its diff against HEAD.

import { useEffect, useMemo, useState } from 'react';
import { useEffectiveSettings } from '../../agents/settings';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { gitError, gitSideChanged, jobSummary, splitPath, STATUS_LABEL, STATUS_LETTER, STATUS_TONE } from '../../git/common';
import { LargeInCommits } from '../../git/LargeInCommits';
import { RepoRows } from '../../git/RepoRows';
import { sumRepos } from '../../git/parts';
import { SUGGEST_LABEL, SUGGEST_LABEL_OFFLINE, suggestCommitMessage } from '../../git/suggest';
import { gitApi } from '../api';
import { studioStore, useStudio, waitJob } from '../store';
import type { GitFile } from '../types';
import { HistorySection } from './CommitGraph';
import { FileIcon } from './Explorer';
import { gitHostOf, repoKey } from './partGit';
import type { PartService } from './partsvc';
import { PullSection } from './PullSection';
import { StashSection } from './StashSection';
import { useWs } from './useWs';
import type { Workspace } from './workspace';

const OPERATION_LABEL: Record<string, string> = { merge: 'Merge', 'cherry-pick': 'Cherry-pick', revert: 'Revert' };

/** Source control of a card: its part's, or in a Root card one group per part, the active one open. */
export function ScmPanel({ ws }: { ws: Workspace }) {
  useWs(ws);
  return ws.paths.multi ? <RootScm ws={ws} /> : <PartScm ws={ws} part={ws.parts[0]} />;
}

/** A Root card: every part's repository, each a group of its own; the part in use opens first. */
function RootScm({ ws }: { ws: Workspace }) {
  const [open, setOpen] = useState<Set<string>>(() => new Set([ws.activePart.partId]));
  const toggle = (id: string) =>
    setOpen((cur) => {
      const n = new Set(cur);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  const refreshAll = () => void Promise.all(ws.parts.map((p) => studioStore.loadGitRepos(p.ref, true).then(() => p.refreshGit())));
  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2 className="truncate">Source control</h2>
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Refresh every part" title="Refresh every part" onClick={refreshAll} />
      </header>
      <div className="wb-tree nowheel wb-scm-parts">
        {ws.parts.map((p) => (
          <ScmPartGroup key={p.partId} ws={ws} part={p} open={open.has(p.partId)} onToggle={() => toggle(p.partId)} />
        ))}
      </div>
    </div>
  );
}

/** One part's group in a Root card: its head (the repositories' state) and, open, its source control. */
function ScmPartGroup({ ws, part: p, open: isOpen, onToggle }: { ws: Workspace; part: PartService; open: boolean; onToggle: () => void }) {
  useWs(ws);
  const repos = useStudio((s) => s.gitRepos[p.ref]?.items);
  const many = (repos?.length ?? 0) > 1;
  const sum = sumRepos(repos);
  const changed = many ? sum.changed : (p.git?.files?.length ?? 0);
  return (
    <section className={`wb-scm-part ${isOpen ? 'is-open' : ''}`} aria-label={`Source control of ${p.name}`}>
      <button type="button" className="wb-section-title wb-section-toggle wb-part-head" aria-expanded={isOpen} title={`${p.name}: ${p.root}`} onClick={onToggle}>
        <Icon name={isOpen ? 'chevronDown' : 'chevronRight'} className="wb-chev" />
        <span className="truncate">{p.name}</span>
        {many ? <span className="wb-result-dir truncate">{repos!.length} repositories</span> : p.git?.branch && <span className="wb-result-dir truncate">{p.git.branch}</span>}
        <span className="spacer" />
        {!many && p.git && !p.git.isRepo ? <span className="text-3">No repository</span> : <span className="wb-result-count tabular">{changed}</span>}
      </button>
      {isOpen && <PartScm ws={ws} part={p} grouped />}
    </section>
  );
}

/** One part's source control: today's panel, or (grouped) its body inside a Root card's group. */
function PartScm({ ws, part, grouped = false }: { ws: Workspace; part: PartService; grouped?: boolean }) {
  useWs(ws);
  const st = part.git;
  const target = part.gitTarget;
  const host = gitHostOf(ws, part);
  const repos = useStudio((s) => s.gitRepos[part.ref]?.items);
  const useCli = useEffectiveSettings().git?.commitWithRunner !== false;
  const [message, setMessage] = useState('');
  const [excluded, setExcluded] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState('');
  const [discard, setDiscard] = useState<GitFile | null>(null);
  const [largeRescan, setLargeRescan] = useState(0);

  const files = useMemo(() => st?.files ?? [], [st]);
  const conflicts = files.filter((f) => f.status === 'conflict');
  const staged = files.filter((f) => f.status !== 'conflict' && gitSideChanged(f.index));
  const changes = files.filter((f) => f.status !== 'conflict' && (f.status === 'untracked' || gitSideChanged(f.worktree)));
  const hasStaged = staged.length > 0;
  const chosen = changes.filter((f) => !excluded.has(f.path)).map((f) => f.path);
  const canCommit = hasStaged || chosen.length > 0;
  const keyOf = (rel: string) => repoKey(part, rel);

  useEffect(() => {
    void studioStore.loadGitRepos(part.ref, true);
    void part.refreshGit();
  }, [part]);

  // the ticks and the draft belong to one repository
  useEffect(() => {
    setExcluded(new Set());
    setMessage('');
  }, [target.repo]);

  const act = async (name: string, fn: () => Promise<void>) => {
    setBusy(name);
    try {
      await fn();
    } catch (err) {
      toast.error(gitError(err));
    } finally {
      setBusy('');
      void part.refreshGit();
    }
  };

  const job = (name: string, start: () => ReturnType<typeof gitApi.push>) =>
    act(name, async () => {
      const j = await waitJob((await start()).id);
      const { ok, text } = jobSummary(j);
      if (ok) toast.success(text);
      else toast.error(text);
      if (j.code === 'large_files') setLargeRescan((n) => n + 1);
    });

  const commit = (thenPush: boolean) =>
    act(thenPush ? 'commitPush' : 'commit', async () => {
      await ws.saveAll();
      await gitApi.commit(target, hasStaged ? { message: message.trim(), staged: true } : { message: message.trim(), paths: chosen });
      setMessage('');
      setExcluded(new Set());
      if (thenPush) {
        const j = await waitJob((await gitApi.push(target, !st?.upstream)).id);
        const { ok, text } = jobSummary(j);
        if (ok) toast.success(text);
        else toast.error(text);
        if (j.code === 'large_files') setLargeRescan((n) => n + 1);
      } else {
        toast.success('Committed.');
      }
    });

  const shellClass = grouped ? 'wb-scm-body' : 'wb-view';

  if (st && !st.isRepo) {
    return (
      <div className={shellClass}>
        {!grouped && (
          <header className="wb-view-head">
            <h2>Source control</h2>
          </header>
        )}
        <RepoRows pid={part.ref} label={`Repositories of ${grouped ? part.name : ws.name}`} className="wb-repo-rows" />
        {repos?.length ? (
          <div className="wb-view-empty">
            <p>{target.repo || (grouped ? part.name : ws.name)} is no longer a Git repository.</p>
            <Button size="sm" icon="refresh" onClick={() => void studioStore.loadGitRepos(part.ref, true)}>
              Scan again
            </Button>
          </div>
        ) : (
          <div className="wb-view-empty">
            <p>{grouped ? `${part.name} is not a Git repository yet.` : 'This project is not a Git repository yet.'}</p>
            <Button
              variant="primary"
              size="sm"
              icon="branch"
              loading={busy === 'init'}
              onClick={() =>
                void act('init', async () => {
                  await gitApi.init(target);
                  await studioStore.loadGitRepos(part.ref, true);
                })
              }
            >
              Create repository
            </Button>
          </div>
        )}
      </div>
    );
  }

  return (
    <div className={shellClass}>
      <header className={grouped ? 'wb-scm-tools' : 'wb-view-head'}>
        {grouped ? <span className="spacer" /> : <h2 className="truncate">Source control</h2>}
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Refresh" title="Refresh" onClick={() => void studioStore.loadGitRepos(part.ref, true).then(() => part.refreshGit())} />
        <Button size="sm" variant="ghost" iconOnly icon="arrowDown" aria-label="Pull" title="Pull" loading={busy === 'pull'} disabled={!!busy || !st?.hasRemote} onClick={() => void job('pull', () => gitApi.pull(target))} />
        <Button size="sm" variant="ghost" iconOnly icon="arrowUp" aria-label="Push" title={st?.ahead ? `Push ${st.ahead} commit${st.ahead === 1 ? '' : 's'}` : 'Push'} loading={busy === 'push'} disabled={!!busy || !st?.hasRemote} onClick={() => void job('push', () => gitApi.push(target, !st?.upstream))} />
        <Button size="sm" variant="ghost" iconOnly icon="download" aria-label="Fetch" title="Fetch" loading={busy === 'fetch'} disabled={!!busy || !st?.hasRemote} onClick={() => void job('fetch', () => gitApi.fetch(target))} />
      </header>
      <RepoRows pid={part.ref} label={`Repositories of ${grouped ? part.name : ws.name}`} className="wb-repo-rows" disabled={!!busy} />
      <LargeInCommits key={target.repo ?? ''} target={target} unpushed={st?.ahead ?? 0} rescan={largeRescan} disabled={!!busy} onStatus={() => void part.refreshGit()} />
      <div className="wb-commit">
        <textarea
          className="control prose"
          rows={Math.max(3, Math.min(12, message.split('\n').length))}
          aria-label="Commit message"
          placeholder={busy === 'suggest' ? 'Reading the changes…' : `Message (Ctrl+Enter commits${st?.branch ? ` on ${st.branch}` : ''})`}
          value={message}
          readOnly={busy === 'suggest'}
          onChange={(e) => setMessage(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && message.trim() && canCommit) {
              e.preventDefault();
              void commit(false);
            }
          }}
        />
        <div className="wb-commit-actions">
          <Button size="sm" variant="primary" icon="check" className="grow" loading={busy === 'commit'} disabled={!message.trim() || !canCommit || !!busy} onClick={() => void commit(false)}>
            {hasStaged ? `Commit staged (${staged.length})` : `Commit ${chosen.length ? `(${chosen.length})` : ''}`}
          </Button>
          <Button size="sm" icon="arrowUp" loading={busy === 'commitPush'} disabled={!message.trim() || !canCommit || !st?.hasRemote || !!busy} onClick={() => void commit(true)}>
            & Push
          </Button>
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon="sparkle"
            aria-label={SUGGEST_LABEL}
            title={useCli ? SUGGEST_LABEL : SUGGEST_LABEL_OFFLINE}
            loading={busy === 'suggest'}
            disabled={!canCommit || !!busy}
            onClick={() =>
              void act('suggest', async () => {
                setMessage(await suggestCommitMessage(target, hasStaged ? staged.map((f) => f.path) : chosen, { staged: hasStaged, useCli }));
              })
            }
          />
        </div>
      </div>
      {st?.operation && (
        <div className="studio-banner wb-banner is-warning wb-op" role="status">
          <Icon name="alert" />
          <span>
            {OPERATION_LABEL[st.operation] ?? st.operation} under way{conflicts.length ? `, ${conflicts.length} ${conflicts.length === 1 ? 'conflict' : 'conflicts'} to settle` : ''}.
          </span>
          <span className="spacer" />
          <Button size="sm" loading={busy === 'abort'} disabled={!!busy} onClick={() => void act('abort', async () => void (await gitApi.operation(target, 'abort')))}>
            Abort
          </Button>
          <Button size="sm" variant="primary" loading={busy === 'continue'} disabled={!!busy || conflicts.length > 0} onClick={() => void act('continue', async () => void (await gitApi.operation(target, 'continue')))}>
            Continue
          </Button>
        </div>
      )}
      <div className={grouped ? 'wb-scm-list' : 'wb-tree nowheel'}>
        {conflicts.length > 0 && (
          <>
            <div className="wb-section-title">
              <span>Merge conflicts</span>
              <span className="wb-result-count tabular">{conflicts.length}</span>
            </div>
            {conflicts.map((f) => {
              const { dir, name } = splitPath(f.path);
              return (
                <div key={f.path} className="wb-row wb-change git-deleted" title={`${f.path} · Accept Current, Incoming or Both above each conflict, then mark it resolved.`} onClick={() => ws.open(keyOf(f.path), { pin: true, source: true })}>
                  <FileIcon name={name} />
                  <span className="wb-row-name truncate">{name}</span>
                  {dir && <span className="wb-result-dir truncate">{dir}</span>}
                  <button
                    type="button"
                    className="wb-result-act is-shown"
                    aria-label={`Mark ${name} as resolved`}
                    title="Mark as resolved"
                    onClick={(e) => {
                      e.stopPropagation();
                      void act('resolve', async () => {
                        await ws.saveAll();
                        await gitApi.stage(target, [f.path]);
                      });
                    }}
                  >
                    <Icon name="check" />
                  </button>
                  <span className="wb-row-mark">!</span>
                </div>
              );
            })}
          </>
        )}
        {hasStaged && (
          <>
            <div className="wb-section-title">
              <span>Staged</span>
              <button type="button" className="wb-section-act" title="Unstage everything" disabled={!!busy} onClick={() => void act('unstage', async () => void (await gitApi.unstage(target, staged.map((f) => f.path))))}>
                Unstage all
              </button>
              <span className="wb-result-count tabular">{staged.length}</span>
            </div>
            {staged.map((f) => {
              const { dir, name } = splitPath(f.path);
              const key = keyOf(f.path);
              return (
                <div key={f.path} className={`wb-row wb-change git-${STATUS_TONE[f.status] ?? 'modified'}`} title={`${f.path} · staged`} onClick={() => (f.status === 'deleted' ? undefined : ws.openDiff(key, 'staged'))}>
                  <FileIcon name={name} />
                  <span className="wb-row-name truncate">{name}</span>
                  {dir && <span className="wb-result-dir truncate">{dir}</span>}
                  <button
                    type="button"
                    className="wb-result-act"
                    aria-label={`Unstage ${name}`}
                    title="Unstage"
                    onClick={(e) => {
                      e.stopPropagation();
                      void act('unstage', async () => void (await gitApi.unstage(target, [f.path])));
                    }}
                  >
                    <Icon name="minus" />
                  </button>
                  <span className="wb-row-mark">{f.index}</span>
                </div>
              );
            })}
          </>
        )}
        <div className="wb-section-title">
          <span>Changes</span>
          {changes.length > 0 && (
            <button type="button" className="wb-section-act" title="Stage everything" disabled={!!busy} onClick={() => void act('stage', async () => void (await gitApi.stage(target, changes.map((f) => f.path))))}>
              Stage all
            </button>
          )}
          <span className="wb-result-count tabular">{changes.length}</span>
        </div>
        {!st && <div className="wb-row-msg">Loading…</div>}
        {st && !changes.length && <div className="wb-row-msg">No changes in the working tree.</div>}
        {changes.map((f) => {
          const { dir, name } = splitPath(f.path);
          const key = keyOf(f.path);
          const tone = STATUS_TONE[f.status] ?? 'modified';
          return (
            <div key={f.path} className={`wb-row wb-change git-${tone}`} title={`${f.path} · ${STATUS_LABEL[f.status] ?? f.status}`} onClick={() => (f.status === 'deleted' ? undefined : ws.openDiff(key, 'index'))}>
              {!hasStaged && (
                <input
                  type="checkbox"
                  aria-label={`Include ${f.path} in the commit`}
                  checked={!excluded.has(f.path)}
                  onClick={(e) => e.stopPropagation()}
                  onChange={() =>
                    setExcluded((cur) => {
                      const n = new Set(cur);
                      if (n.has(f.path)) n.delete(f.path);
                      else n.add(f.path);
                      return n;
                    })
                  }
                />
              )}
              <FileIcon name={name} />
              <span className="wb-row-name truncate">{name}</span>
              {dir && <span className="wb-result-dir truncate">{dir}</span>}
              {f.status !== 'deleted' && (
                <button
                  type="button"
                  className="wb-result-act"
                  aria-label={`Open ${name}`}
                  title="Open file"
                  onClick={(e) => {
                    e.stopPropagation();
                    ws.open(key, { pin: true, source: true });
                  }}
                >
                  <Icon name="file" />
                </button>
              )}
              <button
                type="button"
                className="wb-result-act"
                aria-label={`Stage ${name}`}
                title="Stage"
                onClick={(e) => {
                  e.stopPropagation();
                  void act('stage', async () => {
                    await ws.saveAll();
                    await gitApi.stage(target, [f.path]);
                  });
                }}
              >
                <Icon name="plus" />
              </button>
              <button
                type="button"
                className="wb-result-act"
                aria-label={`Discard changes in ${name}`}
                title="Discard changes"
                onClick={(e) => {
                  e.stopPropagation();
                  setDiscard(f);
                }}
              >
                <Icon name="undo" />
              </button>
              <span className="wb-row-mark">{STATUS_LETTER[f.status] ?? '•'}</span>
            </div>
          );
        })}
        {/* each asks git for its list: only once the status says this is a repository */}
        {st?.isRepo && (
          <>
            <PullSection ws={ws} part={part} />
            <StashSection ws={host} />
            <HistorySection ws={host} />
          </>
        )}
      </div>
      {discard && (
        <ConfirmDialog
          title={`Discard changes in ${splitPath(discard.path).name}?`}
          confirmLabel="Discard"
          danger
          onClose={() => setDiscard(null)}
          onConfirm={async () => {
            await act('discard', async () => {
              const r = await gitApi.discard(target, [discard.path]);
              toast.success(`A copy of the edits is in ${r.trashedTo}.`);
              void ws.reload(keyOf(discard.path));
            });
          }}
        >
          <p>The file goes back to its last commit. A copy of your edits stays in the project’s trash.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
