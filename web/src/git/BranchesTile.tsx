// Branches card (node kind "branches", docs/API-studio.md §3.4, B22): current branch,
// ahead/behind, changed files and up to 200 local branches. Click = switch (no
// confirmation: Git itself refuses a switch that would overwrite work), New branch,
// delete with `git branch -d` (refuses unmerged branches), Panel opens the Git panel. In a project
// of several parts the card is about one part (data.partId, v0.9.0-beta.2), picked in the card.

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
import { partRef, projectParts } from '../lib/parts';
import { gitError } from './common';
import { RepoPicker } from './RepoPicker';

function BranchesTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ui, project, ctl } = useBoardEnv();
  const parts = projectParts(project);
  const want = typeof data.node.data.partId === 'string' ? data.node.data.partId : '';
  const part = parts.length > 1 ? parts.find((p) => p.id === want) : undefined;
  // its part's git ("<project>~<part>", as every part's); several parts: the card's part, none until picked
  const pid = parts.length > 1 ? (part ? partRef(project, part) : '') : partRef(project, parts[0]);
  const sum = useStudio((s) => (pid ? s.git[pid] : undefined));
  const rev = useStudio((s) => (pid ? (s.gitRev[pid] ?? 0) : 0));
  const scanned = useStudio((s) => !!pid && !!s.gitRepos[pid]);
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
    if (pid) void studioStore.loadGitRepos(pid);
  }, [pid]);
  useEffect(() => {
    if (pid && !sum) void studioStore.loadGitSummary(pid);
  }, [pid, sum]);
  const pickPart = (partId: string) => {
    setItems(null);
    ctl.apply([{ op: 'patch', id, set: { data: { partId } } }]);
  };

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
        {parts.length > 1 && (
          <select className="control control-sm" aria-label="Part" title="Which part’s repository" value={part?.id ?? ''} onChange={(e) => pickPart(e.target.value)} disabled={!!busy}>
            {!part && <option value="">Pick a part</option>}
            {parts.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        )}
        {pid && <RepoPicker pid={pid} disabled={!!busy} />}
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
        {!pid && <p className="tile-hint">Pick the part whose branches this card shows.</p>}
        {pid && !items && !error && <p className="tile-hint">Loading branches…</p>}
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
          <Button size="sm" icon="plus" disabled={!pid} onClick={() => setAdding(true)}>
            New branch
          </Button>
        )}
      </div>
    </TileFrame>
  );
}

export const BranchesTile = memo(BranchesTileImpl);
