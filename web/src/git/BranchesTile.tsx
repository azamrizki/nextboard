// Branches card (node kind "branches", docs/API-studio.md §3.4, B22): current branch,
// ahead/behind, changed files and up to 200 local branches. Click = switch (no
// confirmation: Git itself refuses a switch that would overwrite work), New branch,
// delete with `git branch -d` (refuses unmerged branches), Panel opens the Git panel.

import { memo, useCallback, useEffect, useMemo, useState } from 'react';
import type { NodeProps } from '@xyflow/react';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { timeAgo } from '../lib/format';
import type { FlowNode } from '../board/flow';
import { TileFrame, TileHead, stopKeys } from '../board/tiles/common';
import { useBoardEnv } from '../board/ui';
import { gitApi } from '../studio/api';
import { studioStore, useStudio } from '../studio/store';
import type { GitBranch } from '../studio/types';
import { gitError } from './common';
import { RepoPicker } from './RepoPicker';

function BranchesTileImpl({ id, selected }: NodeProps<FlowNode>) {
  const { ui, project } = useBoardEnv();
  const pid = project.id;
  const sum = useStudio((s) => s.git[pid]);
  const rev = useStudio((s) => s.gitRev[pid] ?? 0);
  const scanned = useStudio((s) => !!s.gitRepos[pid]);
  const repo = useStudio((s) => s.gitRepo[pid] ?? '');
  const target = useMemo(() => ({ pid, repo }), [pid, repo]);
  const [items, setItems] = useState<GitBranch[] | null>(null);
  const [error, setError] = useState('');
  const [name, setName] = useState('');
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState('');

  const load = useCallback(() => {
    if (!scanned) return;
    gitApi
      .branches(target)
      .then((r) => {
        setItems(r.items ?? []);
        setError('');
      })
      .catch((err) => setError(gitError(err)));
  }, [target, scanned]);
  useEffect(load, [load, rev]);
  useEffect(() => {
    void studioStore.loadGitRepos(pid);
  }, [pid]);
  useEffect(() => {
    if (!sum) void studioStore.loadGitSummary(pid);
  }, [pid, sum]);

  const act = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(label);
    try {
      await fn();
      load();
      void studioStore.loadGitSummary(pid);
    } catch (err) {
      toast.error(gitError(err));
    } finally {
      setBusy('');
    }
  };

  const create = () => {
    const n = name.trim();
    if (!n) return;
    void act('new', async () => {
      await gitApi.createBranch(target, n, true);
      toast.success(`Created ${n} and switched to it.`);
      setName('');
      setAdding(false);
    });
  };

  const current = items?.find((b) => b.current);
  return (
    <TileFrame id={id} kind="branches" selected={!!selected} label="Branches" minW={260} minH={220}>
      <TileHead
        id={id}
        icon="branch"
        title="Branches"
        meta={<span className="tile-meta tabular">{items ? `${items.length}` : ''}</span>}
        actions={
          <Button size="sm" variant="ghost" className="nodrag" onClick={() => ui.set({ panel: 'git' })}>
            Panel
          </Button>
        }
        menuLabel="Branches card actions"
      />
      <div className="branches-body nodrag nowheel">
        <RepoPicker pid={pid} disabled={!!busy} />
        <div className="branches-now">
          <span className="badge git-branch">
            <Icon name="branch" />
            <span className="truncate">{current?.name ?? sum?.branch ?? '—'}</span>
          </span>
          <span className="fs-xs text-3 tabular">
            {sum ? `${sum.changed} changed${sum.ahead ? ` · ↑${sum.ahead}` : ''}${sum.behind ? ` · ↓${sum.behind}` : ''}` : ''}
          </span>
        </div>
        {error && <p className="field-error">{error}</p>}
        {!items && !error && <p className="tile-hint">Loading branches…</p>}
        {items && (
          <ul className="plain branch-list" aria-label="Local branches">
            {items.map((b) => (
              <li key={b.name} className={`branch-row ${b.current ? 'is-current' : ''}`}>
                <button
                  type="button"
                  className="branch-switch"
                  disabled={b.current || !!busy}
                  aria-current={b.current || undefined}
                  title={b.current ? 'Current branch' : `Switch to ${b.name}`}
                  onClick={() => void act(b.name, () => gitApi.switchBranch(target, b.name))}
                >
                  {busy === b.name ? <span className="spinner" aria-hidden="true" /> : <Icon name={b.current ? 'check' : 'branch'} />}
                  <span className="truncate">{b.name}</span>
                  <span className="spacer" />
                  <span className="fs-xs text-3 tabular">
                    {b.ahead ? `↑${b.ahead} ` : ''}
                    {b.behind ? `↓${b.behind} ` : ''}
                    {b.lastCommitAt ? timeAgo(b.lastCommitAt) : ''}
                  </span>
                </button>
                {!b.current && (
                  <button
                    type="button"
                    className="branch-del"
                    aria-label={`Delete branch ${b.name}`}
                    title="Delete (git branch -d: unmerged branches are kept)"
                    disabled={!!busy}
                    onClick={() =>
                      void act(`del:${b.name}`, async () => {
                        await gitApi.deleteBranch(target, b.name);
                        toast.success(`Deleted ${b.name}.`);
                      })
                    }
                  >
                    <Icon name="trash" />
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>
      <div className="tile-foot nodrag">
        {adding ? (
          <>
            <input
              className="control control-sm mono grow"
              value={name}
              autoFocus
              placeholder="feature/new-thing"
              aria-label="New branch name"
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                stopKeys(e);
                if (e.key === 'Enter') create();
                if (e.key === 'Escape') setAdding(false);
              }}
            />
            <Button size="sm" variant="primary" loading={busy === 'new'} disabled={!name.trim()} onClick={create}>
              Create
            </Button>
          </>
        ) : (
          <Button size="sm" icon="plus" onClick={() => setAdding(true)}>
            New branch
          </Button>
        )}
      </div>
    </TileFrame>
  );
}

export const BranchesTile = memo(BranchesTileImpl);
