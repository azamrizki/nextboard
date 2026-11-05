// The History section of the source control view: commits on their lanes, as in GitLens' commit
// graph, with the branches and tags on them. A click opens the commit in a tab (CommitView.tsx).

import { useEffect, useMemo, useRef, useState, type MouseEvent } from 'react';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { ContextMenu, type MenuEntry } from '../../components/Menu';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import { errorMessage } from '../../api';
import { gitError } from '../../git/common';
import { formatDateTime } from '../../lib/format';
import { gitApi } from '../api';
import type { GitCommit, GitRef, GitTarget } from '../types';
import { layoutGraph, refPills, type GraphRow } from './commitGraph';
import { useWs } from './useWs';
import type { Workspace } from './workspace';

const PAGE = 100;
const MAX_COMMITS = 1000;
const RELOAD_MS = 400;
const ROW_H = 28;
const LEFT = 9;
const STEP = 12;
/** Many lanes at once squeeze together rather than push the words out of a narrow panel. */
const GUTTER = 84;
const TAB_GUTTER = 240;
/** Room for the word above the lanes when there are few. */
const GRAPH_TITLE = 56;
const MIN_STEP = 5;

/** "5m", "3h", "12d", then "Sep 7": a narrow column, the full date is in the tooltip. */
function shortWhen(iso: string): string {
  const t = new Date(iso);
  const s = (Date.now() - t.getTime()) / 1000;
  if (Number.isNaN(s)) return '';
  if (s < 60) return 'now';
  if (s < 3600) return `${Math.round(s / 60)}m`;
  if (s < 86400) return `${Math.round(s / 3600)}h`;
  if (s < 30 * 86400) return `${Math.round(s / 86400)}d`;
  const opts: Intl.DateTimeFormatOptions = t.getFullYear() === new Date().getFullYear() ? { month: 'short', day: 'numeric' } : { month: 'short', year: '2-digit' };
  return t.toLocaleDateString(undefined, opts);
}

const laneVar = (color: number) => `var(--wb-lane-${color})`;

export function RefPills({ refs, color, max }: { refs?: GitRef[]; color: number; max?: number }) {
  const pills = refPills(refs);
  if (!pills.length) return null;
  const shown = max ? pills.slice(0, max) : pills;
  return (
    <span className="wb-refs" style={{ '--lane': laneVar(color) } as React.CSSProperties}>
      {shown.map((p) => (
        <span key={`${p.kind}:${p.name}`} className={`wb-ref is-${p.kind}`} title={`${p.kind === 'tag' ? 'Tag' : p.kind === 'remote' ? 'Remote branch' : p.kind === 'head' ? 'Checked out' : 'Branch'}: ${p.name}${p.synced ? ', on the remote too' : ''}`}>
          <Icon name={p.kind === 'tag' ? 'tag' : p.kind === 'remote' ? 'cloud' : 'branch'} />
          <span className="truncate">{p.name}</span>
          {p.synced && <Icon name="cloud" />}
        </span>
      ))}
      {pills.length > shown.length && <span className="wb-ref is-more">+{pills.length - shown.length}</span>}
    </span>
  );
}

function GraphCell({ row, step, width, pushed, head, merge }: { row: GraphRow; step: number; width: number; pushed: boolean; head: boolean; merge: boolean }) {
  const x = (lane: number) => LEFT + lane * step;
  const at = x(row.lane);
  const mid = ROW_H / 2;
  const line = (x1: number, y1: number, x2: number, y2: number) => {
    if (x1 === x2) return `M${x1} ${y1}V${y2}`;
    const m = (y1 + y2) / 2;
    return `M${x1} ${y1}C${x1} ${m} ${x2} ${m} ${x2} ${y2}`;
  };
  return (
    <svg className="wb-graph-cell" width={width} height={ROW_H} aria-hidden="true">
      {row.through.map((l) => (
        <path key={`t${l.lane}`} d={line(x(l.lane), 0, x(l.lane), ROW_H)} stroke={laneVar(l.color)} />
      ))}
      {row.above.map((l) => (
        <path key={`a${l.lane}`} d={line(x(l.lane), 0, at, mid)} stroke={laneVar(l.color)} />
      ))}
      {row.below.map((l) => (
        <path key={`b${l.lane}`} d={line(at, mid, x(l.lane), ROW_H)} stroke={laneVar(l.color)} />
      ))}
      {head && <circle className="wb-graph-head" cx={at} cy={mid} r={7} stroke={laneVar(row.color)} />}
      {/* hollow: not on any remote yet */}
      <circle className="wb-graph-dot" cx={at} cy={mid} r={merge ? 3 : 4} fill={pushed ? laneVar(row.color) : 'var(--surface-solid)'} stroke={laneVar(row.color)} />
    </svg>
  );
}

async function loadPages(target: GitTarget, all: boolean, count: number): Promise<GitCommit[]> {
  const out: GitCommit[] = [];
  for (let skip = 0; skip < count; skip += PAGE) {
    const page = await gitApi.graph(target, { all, skip, limit: PAGE });
    out.push(...page);
    if (page.length < PAGE) break;
  }
  return out;
}

const fingerprint = (list: GitCommit[]) => list.map((c) => `${c.hash}:${c.pushed ? 1 : 0}:${(c.refs ?? []).map((r) => `${r.kind}/${r.name}`).join()}`).join('\n');

/** Asks for the name of a branch or a tag at a commit. */
function NameDialog({ title, label, cta, onSave, onClose }: { title: string; label: string; cta: string; onSave: (name: string) => Promise<void>; onClose: () => void }) {
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const save = async () => {
    setBusy(true);
    setError('');
    try {
      await onSave(name.trim());
      onClose();
    } catch (err) {
      setError(gitError(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title={title}
      size="sm"
      onClose={() => !busy && onClose()}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={() => void save()}>
            {cta}
          </Button>
        </>
      }
    >
      <label className="field">
        <span className="field-label">{label}</span>
        <input
          className="control mono"
          aria-label={label}
          value={name}
          autoFocus
          spellCheck={false}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && name.trim()) void save();
          }}
        />
      </label>
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
    </Modal>
  );
}

type Ask = { kind: 'branch' | 'tag'; commit: GitCommit } | { kind: 'hard'; commit: GitCommit };

/** The commits of the graph, loaded a page at a time and kept fresh. */
function useHistory(ws: Workspace, open: boolean) {
  const target = ws.gitTarget;
  const [all, setAllState] = useState(true);
  const [items, setItems] = useState<GitCommit[] | null>(null);
  const [wanted, setWanted] = useState(PAGE);
  const [error, setError] = useState('');
  const turn = useRef(0);
  const current = useRef('');
  const scope = `${target.repo}|${all}`;
  const lastScope = useRef(scope);
  const firstLoad = items === null;

  useEffect(() => {
    if (!open) return;
    const me = ++turn.current;
    const count = lastScope.current === scope ? wanted : PAGE;
    if (lastScope.current !== scope) {
      lastScope.current = scope;
      setWanted(PAGE);
    }
    // git refreshes often while an agent works: only the last of a burst reads the log
    const timer = window.setTimeout(
      () => {
        loadPages(target, all, count)
          .then((list) => {
            if (me !== turn.current) return;
            setError('');
            const print = fingerprint(list);
            if (print === current.current && !firstLoad) return;
            current.current = print;
            setItems(list);
          })
          .catch((err) => {
            if (me !== turn.current) return;
            setError(errorMessage(err));
            setItems((cur) => cur ?? []);
          });
      },
      firstLoad ? 0 : RELOAD_MS,
    );
    return () => window.clearTimeout(timer);
    // the list reloads after a commit, a pull or a switch of branch
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, ws.gitVersion, scope, open, wanted]);

  const graph = useMemo(() => layoutGraph(items ?? []), [items]);
  return {
    all,
    toggleAll: () => {
      setItems(null);
      current.current = '';
      setAllState((v) => !v);
    },
    items,
    error,
    firstLoad,
    graph,
    more: !!items && items.length >= wanted && items.length % PAGE === 0 && items.length < MAX_COMMITS,
    showMore: () => setWanted((n) => Math.min(MAX_COMMITS, n + PAGE)),
  };
}

function ScopeButton({ all, toggle }: { all: boolean; toggle: () => void }) {
  return (
    <button type="button" className="wb-history-scope" title={all ? 'Showing every branch. Click for the current branch only.' : 'Showing the current branch. Click for every branch.'} onClick={toggle}>
      {all ? 'All branches' : 'Current branch'}
    </button>
  );
}

/** The right-click menu of a commit, and the dialogs it asks with. */
function useCommitMenu(ws: Workspace) {
  const target = ws.gitTarget;
  const [menu, setMenu] = useState<{ x: number; y: number; commit: GitCommit } | null>(null);
  const [ask, setAsk] = useState<Ask | null>(null);

  const run = async (fn: () => Promise<unknown>, done: string) => {
    try {
      await fn();
      toast.success(done);
    } catch (err) {
      toast.error(gitError(err));
    } finally {
      void ws.refreshGit();
    }
  };

  const menuItems = (c: GitCommit): MenuEntry[] => {
    const branches = (c.refs ?? []).filter((r) => r.kind === 'branch' || r.kind === 'remote');
    return [
      { label: 'Open', icon: 'history', onSelect: () => ws.openCommit(c.hash, { label: `${c.short} ${c.subject}` }) },
      { label: 'Copy Hash', icon: 'copy', onSelect: () => void navigator.clipboard?.writeText(c.hash).then(() => toast.success('Hash copied.')) },
      { label: 'Copy Message', icon: 'copy', onSelect: () => void navigator.clipboard?.writeText(c.subject).then(() => toast.success('Message copied.')) },
      'sep',
      { label: 'Checkout This Commit', icon: 'branch', onSelect: () => void run(() => gitApi.commitOp(target, { op: 'checkout', ref: c.hash }), `On ${c.short}, without a branch.`) },
      { label: 'Create Branch Here…', icon: 'plus', onSelect: () => setAsk({ kind: 'branch', commit: c }) },
      { label: 'Create Tag Here…', icon: 'tag', onSelect: () => setAsk({ kind: 'tag', commit: c }) },
      'sep',
      ...branches.map((r): MenuEntry => ({ label: `Merge ${r.name} Into Current`, icon: 'branch', onSelect: () => void run(() => gitApi.commitOp(target, { op: 'merge', ref: r.name }), `Merged ${r.name}.`) })),
      { label: 'Cherry-pick', onSelect: () => void run(() => gitApi.commitOp(target, { op: 'cherry-pick', ref: c.hash }), 'Cherry-picked.') },
      { label: 'Revert', onSelect: () => void run(() => gitApi.commitOp(target, { op: 'revert', ref: c.hash }), 'Reverted.') },
      'sep',
      { label: 'Reset to Here: Soft', title: 'The branch moves here; the changes stay staged.', onSelect: () => void run(() => gitApi.commitOp(target, { op: 'reset', ref: c.hash, mode: 'soft' }), 'Reset, changes staged.') },
      { label: 'Reset to Here: Mixed', title: 'The branch moves here; the changes stay in the files.', onSelect: () => void run(() => gitApi.commitOp(target, { op: 'reset', ref: c.hash, mode: 'mixed' }), 'Reset, changes kept in the files.') },
      { label: 'Reset to Here: Hard…', icon: 'alert', title: 'The branch moves here; the files go back too.', onSelect: () => setAsk({ kind: 'hard', commit: c }) },
    ];
  };

  const overlay = (
    <>
      {menu && <ContextMenu x={menu.x} y={menu.y} label="Commit actions" items={menuItems(menu.commit)} onClose={() => setMenu(null)} />}
      {ask?.kind === 'branch' && (
        <NameDialog
          title={`New branch at ${ask.commit.short}`}
          label="Branch name"
          cta="Create and switch"
          onClose={() => setAsk(null)}
          onSave={async (name) => {
            await gitApi.createBranch(target, name, true, ask.commit.hash);
            void ws.refreshGit();
          }}
        />
      )}
      {ask?.kind === 'tag' && (
        <NameDialog
          title={`New tag at ${ask.commit.short}`}
          label="Tag name"
          cta="Create tag"
          onClose={() => setAsk(null)}
          onSave={async (name) => {
            await gitApi.tag(target, { name, ref: ask.commit.hash });
            void ws.refreshGit();
          }}
        />
      )}
      {ask?.kind === 'hard' && (
        <ConfirmDialog
          title={`Reset the branch to ${ask.commit.short}?`}
          confirmLabel="Reset hard"
          danger
          onClose={() => setAsk(null)}
          onConfirm={async () => {
            await gitApi.commitOp(target, { op: 'reset', ref: ask.commit.hash, mode: 'hard' });
            toast.success('Reset. What was not committed stays in Local History.');
            void ws.refreshGit();
          }}
        >
          <p>Commits after it leave the branch, and the files go back to it. Changes that are not committed stay in Local History.</p>
        </ConfirmDialog>
      )}
    </>
  );
  return { open: (e: MouseEvent, commit: GitCommit) => setMenu({ x: e.clientX, y: e.clientY, commit }), overlay };
}

function lanesFit(lanes: number, gutter: number) {
  const step = Math.max(MIN_STEP, Math.min(STEP, (gutter - LEFT) / Math.max(lanes - 1, 1)));
  return { step, width: Math.min(gutter + LEFT, LEFT * 2 + step * Math.max(lanes - 1, 0)) };
}

export function HistorySection({ ws }: { ws: Workspace }) {
  const [open, setOpen] = useState(true);
  const h = useHistory(ws, open);
  const commitMenu = useCommitMenu(ws);
  const { step, width } = lanesFit(h.graph.lanes, GUTTER);
  const activeHash = ws.activeTab?.commit?.hash;

  return (
    <div className="wb-history">
      <div className="wb-section-title wb-history-head">
        <button type="button" className="wb-history-toggle" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} className="wb-chev" />
          <span>History</span>
        </button>
        {open && (
          <>
            <button type="button" className="wb-section-act wb-graph-open" title="See the graph in a tab of its own" onClick={() => ws.openView({ kind: 'graph' }, { pin: true })}>
              Open graph
            </button>
            <ScopeButton all={h.all} toggle={h.toggleAll} />
          </>
        )}
      </div>
      {open && h.firstLoad && !h.error && <div className="wb-row-msg">Loading…</div>}
      {open && h.error && <div className="wb-row-msg" role="alert">{h.error}</div>}
      {open && h.items && !h.items.length && !h.error && <div className="wb-row-msg">No commits yet.</div>}
      {open &&
        h.items?.map((c, i) => {
          const row = h.graph.rows[i];
          const head = !!c.refs?.some((r) => r.kind === 'head');
          const when = formatDateTime(c.at);
          return (
            <button
              key={c.hash}
              type="button"
              className={`wb-graph-row ${activeHash === c.hash ? 'is-active' : ''}`}
              style={{ height: ROW_H }}
              title={`${c.subject}\n${c.short} · ${c.author} · ${when}${c.pushed ? '' : '\nNot on any remote yet'}`}
              aria-label={`${c.subject}, ${c.short}, ${c.author}, ${when}`}
              aria-current={activeHash === c.hash ? 'true' : undefined}
              onClick={() => ws.openCommit(c.hash, { label: `${c.short} ${c.subject}` })}
              onDoubleClick={() => ws.openCommit(c.hash, { label: `${c.short} ${c.subject}`, pin: true })}
              onContextMenu={(e) => {
                e.preventDefault();
                commitMenu.open(e, c);
              }}
            >
              <GraphCell row={row} step={step} width={width} pushed={c.pushed} head={head} merge={(c.parents?.length ?? 0) > 1} />
              <RefPills refs={c.refs} color={row.color} max={2} />
              <span className="wb-commit-subject truncate">{c.subject}</span>
              <time className="wb-commit-time tabular" dateTime={c.at}>
                {shortWhen(c.at)}
              </time>
            </button>
          );
        })}
      {open && h.more && (
        <button type="button" className="wb-history-more" onClick={h.showMore}>
          Show more
        </button>
      )}
      {commitMenu.overlay}
    </div>
  );
}

/** The graph in a tab of its own: room for every lane, the author and the date, and a filter. */
export function CommitGraphView({ ws }: { ws: Workspace }) {
  useWs(ws);
  const h = useHistory(ws, true);
  const commitMenu = useCommitMenu(ws);
  const [filter, setFilter] = useState('');
  const { step, width } = lanesFit(h.graph.lanes, TAB_GUTTER);
  const columns = `${Math.max(width, GRAPH_TITLE)}px minmax(0, 1fr) 150px 80px 110px`;
  const words = filter.trim().toLowerCase();
  const matches = (c: GitCommit) => !words || `${c.subject} ${c.author} ${c.hash} ${(c.refs ?? []).map((r) => r.name).join(' ')}`.toLowerCase().includes(words);
  const found = words ? (h.items ?? []).filter(matches).length : 0;

  return (
    <div className="wb-gt nowheel">
      <div className="wb-gt-bar">
        <input className="control control-sm" aria-label="Filter commits" placeholder="Filter by message, author, hash or branch" value={filter} onChange={(e) => setFilter(e.target.value)} />
        {!!words && <span className="text-3 tabular">{found} found</span>}
        <span className="spacer" />
        <ScopeButton all={h.all} toggle={h.toggleAll} />
      </div>
      <div className="wb-gt-rows" role="table" aria-label="Commit graph">
        <div className="wb-gt-row wb-gt-head" role="row" style={{ gridTemplateColumns: columns }}>
          <span role="columnheader">Graph</span>
          <span role="columnheader">Message</span>
          <span role="columnheader">Author</span>
          <span role="columnheader">Commit</span>
          <span role="columnheader">Date</span>
        </div>
        {h.firstLoad && !h.error && <div className="wb-row-msg">Loading…</div>}
        {h.error && <div className="wb-row-msg" role="alert">{h.error}</div>}
        {h.items && !h.items.length && !h.error && <div className="wb-row-msg">No commits yet.</div>}
        {h.items?.map((c, i) => {
          const row = h.graph.rows[i];
          const head = !!c.refs?.some((r) => r.kind === 'head');
          return (
            <div
              key={c.hash}
              role="row"
              tabIndex={0}
              className={`wb-gt-row ${matches(c) ? '' : 'is-dim'}`}
              style={{ gridTemplateColumns: columns, height: ROW_H }}
              onClick={() => ws.openCommit(c.hash, { label: `${c.short} ${c.subject}` })}
              onKeyDown={(e) => {
                if (e.key === 'Enter') ws.openCommit(c.hash, { label: `${c.short} ${c.subject}` });
              }}
              onContextMenu={(e) => {
                e.preventDefault();
                commitMenu.open(e, c);
              }}
            >
              <span role="cell">
                <GraphCell row={row} step={step} width={width} pushed={c.pushed} head={head} merge={(c.parents?.length ?? 0) > 1} />
              </span>
              <span role="cell" className="wb-gt-msg">
                <RefPills refs={c.refs} color={row.color} />
                <span className="truncate" title={c.subject}>
                  {c.subject}
                </span>
              </span>
              <span role="cell" className="truncate text-2" title={c.author}>
                {c.author}
              </span>
              <span role="cell" className="mono text-3">
                {c.short}
              </span>
              <time role="cell" className="tabular text-3" dateTime={c.at} title={formatDateTime(c.at)}>
                {shortWhen(c.at)}
              </time>
            </div>
          );
        })}
        {h.more && (
          <button type="button" className="wb-history-more" onClick={h.showMore}>
            Show more
          </button>
        )}
      </div>
      {commitMenu.overlay}
    </div>
  );
}
