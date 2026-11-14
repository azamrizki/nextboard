// Mission Map (docs/API-orchestration.md §7, D16): who directs whom. Three columns —
// directors (command bar + orchestrator cards) → groups → consoles — joined by lines;
// a dashed line marks "command bar only". Filters, search, an inspector with actions;
// double-click (or Show on board) jumps to the node on the board. X opens it, Esc closes.

import { useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type KeyboardEvent } from 'react';
import { agentById } from '../../agents/catalog';
import { restartConsole } from '../../agents/actions';
import { jumpToSession } from '../../agents/jump';
import { agentPill } from '../../agents/state';
import type { BoardController } from '../../board/controller';
import { directedTerminals } from '../../board/groups';
import { readGroup, readOrchestrator } from '../../board/kinds';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { SegmentedControl } from '../../components/form';
import { Icon } from '../../components/Icon';
import { Modal } from '../../components/Modal';
import { timeAgo } from '../../lib/format';
import { useLayout } from '../../lib/layout';
import { projectParts } from '../../lib/parts';
import type { Session } from '../../lib/types';
import { useMediaQuery } from '../../lib/useMediaQuery';
import { killSessions, pauseSessions, resumeSessions } from '../actions';
import { orchApi } from '../api';
import { useNow, useStoreSessions } from '../hooks';
import { useOrch } from '../store';
import { ROLE_LABEL, type Mission, type Role } from '../types';

type Filter = 'all' | 'working' | 'needs' | 'idle' | 'done';
type Sel = { kind: 'director' | 'group' | 'console'; id: string } | null;

interface MConsole {
  session: Session;
  nodeId: string;
  groupId?: string;
  role?: string;
  directedBy: string[];
  subagents: number;
  currentOrder?: string;
}

function bucket(s: Session): Filter {
  if (s.status === 'exited' || s.agentState === 'exited') return 'done';
  if (s.paused) return 'idle';
  if (s.agentState === 'needsYou' || s.agentState === 'error') return 'needs';
  if (s.agentState === 'working' || s.agentState === 'starting') return 'working';
  return 'idle';
}

export default function MissionMap({ ctl, onClose }: { ctl: BoardController; onClose: () => void }) {
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const sessions = useStoreSessions();
  const orders = useOrch((s) => s.orders);
  const narrow = useMediaQuery('(max-width: 899px)');
  const touch = useLayout().input === 'touch';
  const [filter, setFilter] = useState<Filter>('all');
  const [partFilter, setPartFilter] = useState('');
  const [q, setQ] = useState('');
  const [sel, setSel] = useState<Sel>(null);
  const [hub, setHub] = useState<Mission | null>(null);
  const [kill, setKill] = useState<Session | null>(null);
  const now = useNow(5000);

  // hub-only details (subagents, current orders, last tasks); states come live from the store
  useEffect(() => {
    let alive = true;
    const load = () =>
      !document.hidden &&
      orchApi
        .mission(ctl.workspaceId)
        .then((m) => alive && setHub(m))
        .catch(() => undefined);
    void load();
    const t = window.setInterval(load, 5000);
    return () => {
      alive = false;
      window.clearInterval(t);
    };
  }, [ctl.workspaceId]);

  const model = useMemo(() => {
    const byId = new Map(doc.nodes.map((n) => [n.id, n]));
    const groups = doc.nodes.filter((n) => n.kind === 'group');
    const cards = doc.nodes.filter((n) => n.kind === 'orchestrator');
    const groupOfNode = new Map<string, string>();
    for (const g of groups) for (const m of readGroup(g).members) groupOfNode.set(m, g.id);
    const directed = new Map<string, string[]>();
    for (const c of cards) for (const t of directedTerminals(doc.nodes, doc.edges, c.id)) directed.set(t, [...(directed.get(t) ?? []), c.id]);
    const hubConsoles = new Map((hub?.consoles ?? []).map((c) => [c.sessionId, c]));
    const consoles: MConsole[] = [];
    for (const n of doc.nodes) {
      if (n.kind !== 'terminal') continue;
      const s = typeof n.data.sessionId === 'string' ? sessions[n.data.sessionId] : undefined;
      if (!s) continue;
      const gid = groupOfNode.get(n.id);
      const hc = hubConsoles.get(s.id);
      const cur = hc?.currentOrder?.text ?? Object.values(orders).find((o) => o.targets?.some((t) => t.sessionId === s.id && (t.state === 'working' || t.state === 'delivered' || t.state === 'queued')))?.text;
      consoles.push({
        session: s,
        nodeId: n.id,
        groupId: gid,
        role: gid ? readGroup(byId.get(gid)!).role : undefined,
        directedBy: directed.get(n.id) ?? ['bar'],
        subagents: hc?.subagents ?? 0,
        currentOrder: cur,
      });
    }
    const hubDirectors = new Map((hub?.directors ?? []).map((d) => [d.id, d]));
    const directors = [
      { id: 'bar', kind: 'bar' as const, title: 'Command bar', last: hubDirectors.get('bar')?.lastOrder?.text },
      ...cards.map((c) => ({ id: c.id, kind: 'orchestrator' as const, title: readOrchestrator(c).title, last: hubDirectors.get(c.id)?.lastOrder?.text })),
    ];
    const hubGroups = new Map((hub?.groups ?? []).map((g) => [g.nodeId, g]));
    const gs = groups.map((g) => {
      const d = readGroup(g);
      const wiredBy = doc.edges.filter((e) => e.kind === 'wire' && e.target === g.id && byId.get(e.source)?.kind === 'orchestrator').map((e) => e.source);
      return { id: g.id, name: d.name, role: d.role, color: d.color, members: d.members, lastTask: hubGroups.get(g.id)?.lastTask, directedBy: wiredBy.length ? wiredBy : ['bar'] };
    });
    // console directly wired to a card (not only through its group)
    const direct = new Map<string, string[]>();
    for (const e of doc.edges) if (e.kind === 'wire' && byId.get(e.source)?.kind === 'orchestrator' && byId.get(e.target)?.kind === 'terminal') direct.set(e.target, [...(direct.get(e.target) ?? []), e.source]);
    return { consoles, directors, groups: gs, direct };
  }, [doc, sessions, hub, orders]);

  // a project in several parts: which part each console works in, and a filter by part (ROADMAP #49)
  const parts = projectParts(ctl.project);
  const partOf = (s: Session) => (parts.length > 1 ? parts.find((p) => p.id === (s.partId || parts[0].id)) : undefined);
  const match = (c: MConsole) => {
    if (filter !== 'all' && bucket(c.session) !== filter) return false;
    if (partFilter && partOf(c.session)?.id !== partFilter) return false;
    const n = q.trim().toLowerCase();
    if (!n) return true;
    const agent = agentById(c.session.agent || c.session.preset)?.name ?? c.session.agent ?? '';
    return [c.session.handle, c.session.name, agent, c.role, partOf(c.session)?.name].some((v) => v && v.toLowerCase().includes(n));
  };
  const shown = model.consoles.filter(match);
  const shownIds = new Set(shown.map((c) => c.nodeId));
  const groupsShown = model.groups.filter((g) => g.members.some((m) => shownIds.has(m)) || (q.trim() && g.name.toLowerCase().includes(q.trim().toLowerCase())));
  const counts = { working: 0, needs: 0, idle: 0, done: 0 } as Record<Exclude<Filter, 'all'>, number>;
  for (const c of model.consoles) counts[bucket(c.session) as Exclude<Filter, 'all'>]++;

  // ---- connector lines (desktop) ----
  const boxRef = useRef<HTMLDivElement>(null);
  const [lines, setLines] = useState<{ d: string; dashed: boolean; key: string }[]>([]);
  useLayoutEffect(() => {
    if (narrow) return;
    const box = boxRef.current;
    if (!box) return;
    const measure = () => {
      const b = box.getBoundingClientRect();
      const at = (key: string, side: 'l' | 'r') => {
        const el = box.querySelector<HTMLElement>(`[data-mm="${CSS.escape(key)}"]`);
        if (!el) return null;
        const r = el.getBoundingClientRect();
        return { x: (side === 'l' ? r.left : r.right) - b.left, y: r.top + r.height / 2 - b.top + box.scrollTop };
      };
      const out: { d: string; dashed: boolean; key: string }[] = [];
      const link = (from: string, to: string, dashed: boolean) => {
        const a = at(from, 'r');
        const z = at(to, 'l');
        if (!a || !z) return;
        const mx = (a.x + z.x) / 2;
        out.push({ key: `${from}>${to}`, dashed, d: `M${a.x},${a.y} C${mx},${a.y} ${mx},${z.y} ${z.x},${z.y}` });
      };
      for (const g of groupsShown) for (const d of g.directedBy) link(`d:${d}`, `g:${g.id}`, d === 'bar');
      for (const c of shown) {
        if (c.groupId && groupsShown.some((g) => g.id === c.groupId)) link(`g:${c.groupId}`, `c:${c.nodeId}`, false);
        const direct = model.direct.get(c.nodeId) ?? [];
        for (const d of direct) link(`d:${d}`, `c:${c.nodeId}`, false);
        if (!c.groupId && !direct.length) link('d:bar', `c:${c.nodeId}`, true);
      }
      setLines(out);
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(box);
    return () => ro.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [narrow, model, filter, q]);

  const focusNode = (sid: string) => {
    onClose();
    window.setTimeout(() => jumpToSession(sid), 60);
  };

  const onListKey = (e: KeyboardEvent<HTMLElement>) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const items = Array.from(boxRef.current?.querySelectorAll<HTMLElement>('.mm-item') ?? []);
    const i = items.indexOf(document.activeElement as HTMLElement);
    if (i < 0) return;
    e.preventDefault();
    items[(i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length]?.focus();
  };

  const selConsole = sel?.kind === 'console' ? model.consoles.find((c) => c.nodeId === sel.id) : undefined;
  const selGroup = sel?.kind === 'group' ? model.groups.find((g) => g.id === sel.id) : undefined;
  const selDirector = sel?.kind === 'director' ? model.directors.find((d) => d.id === sel.id) : undefined;

  const filterOpts = [
    { value: 'all' as const, label: `All ${model.consoles.length}` },
    { value: 'working' as const, label: `Working ${counts.working}` },
    { value: 'needs' as const, label: `Needs you ${counts.needs}` },
    { value: 'idle' as const, label: `Idle ${counts.idle}` },
    { value: 'done' as const, label: `Done ${counts.done}` },
  ];

  return (
    <Modal title="Mission Map" icon="map" size="lg" className="mission-modal" onClose={onClose}>
      <div className="mm-toolbar">
        <SegmentedControl<Filter> value={filter} onChange={setFilter} options={filterOpts} label="Filter consoles" size="sm" className="mm-filter" />
        {parts.length > 1 && (
          <select className="control control-sm mm-part" aria-label="Part of the project" value={partFilter} onChange={(e) => setPartFilter(e.target.value)}>
            <option value="">All parts</option>
            {parts.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        )}
        <div className="input-group has-lead mm-search">
          <span className="ig-lead">
            <Icon name="search" />
          </span>
          <input className="control control-sm" placeholder="Search name, agent or role" aria-label="Search the map" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
      </div>
      <div className="mm-body">
        <div className="mm-cols" ref={boxRef} onKeyDown={onListKey}>
          {!narrow && (
            <svg className="mm-lines" aria-hidden="true">
              {lines.map((l) => (
                <path key={l.key} d={l.d} className={l.dashed ? 'is-bar' : ''} />
              ))}
            </svg>
          )}
          <section className="mm-col" aria-label="Directors">
            <h3 className="mm-col-title">Directors</h3>
            {model.directors.map((d) => (
              <button
                key={d.id}
                type="button"
                className={`mm-item mm-director ${sel?.id === d.id ? 'is-selected' : ''}`}
                data-mm={`d:${d.id}`}
                onClick={() => setSel({ kind: 'director', id: d.id })}
              >
                <Icon name={d.kind === 'bar' ? 'keyboard' : 'orchestrator'} />
                <span className="mm-name truncate">{d.title}</span>
                {d.last && <span className="mm-sub truncate">“{d.last}”</span>}
              </button>
            ))}
          </section>
          <section className="mm-col" aria-label="Groups">
            <h3 className="mm-col-title">Groups</h3>
            {groupsShown.length === 0 && <p className="mm-empty">{model.groups.length ? 'No group matches.' : touch ? 'No groups. Long-press a console on the board and pick Group console.' : 'No groups. Select consoles and press Ctrl+G.'}</p>}
            {groupsShown.map((g) => (
              <button key={g.id} type="button" className={`mm-item mm-group ${sel?.id === g.id ? 'is-selected' : ''}`} data-mm={`g:${g.id}`} onClick={() => setSel({ kind: 'group', id: g.id })}>
                <span className="group-dot" style={{ background: `var(--tag-${g.color})` }} aria-hidden="true" />
                <span className="mm-name truncate">{g.name}</span>
                <span className="mm-sub">
                  {ROLE_LABEL[g.role as Role] ?? g.role} · {g.members.length}
                </span>
              </button>
            ))}
          </section>
          <section className="mm-col mm-col-consoles" aria-label="Consoles">
            <h3 className="mm-col-title">Consoles</h3>
            {shown.length === 0 && <p className="mm-empty">{model.consoles.length ? 'No console matches this filter.' : 'No consoles on this board yet.'}</p>}
            {shown.map((c) => {
              const s = c.session;
              const pill = agentPill(s);
              return (
                <button
                  key={c.nodeId}
                  type="button"
                  className={`mm-item mm-console ${sel?.id === c.nodeId ? 'is-selected' : ''}`}
                  data-mm={`c:${c.nodeId}`}
                  onClick={() => setSel({ kind: 'console', id: c.nodeId })}
                  onDoubleClick={() => focusNode(s.id)}
                  title={touch ? undefined : 'Double-click to show it on the board'}
                >
                  <span className={`dot dot-${pill.dot}`} aria-hidden="true" />
                  <span className="mm-name">@{s.handle ?? s.name}</span>
                  <span className="mm-sub truncate">{agentById(s.agent || s.preset)?.name ?? s.agent}</span>
                  {partOf(s) && (
                    <span className="cap-chip" title="The part of the project it works in">
                      {partOf(s)!.name}
                    </span>
                  )}
                  <span className="spacer" />
                  {c.subagents > 0 && <span className="cap-chip" title="Subagents running">{c.subagents} sub</span>}
                  <span className={`agent-pill tone-${pill.tone}`}>{pill.label}</span>
                  {(pill.activity || c.currentOrder) && <span className="mm-activity truncate">{pill.activity || `“${c.currentOrder}”`}</span>}
                </button>
              );
            })}
          </section>
        </div>
        <aside className="mm-inspector" aria-label="Details">
          {!sel && <p className="mm-empty">{touch ? 'Tap a console, group or director to see what it is doing, and Show it on the board from there.' : 'Select a console, group or director to see what it is doing. Double-click a console to jump to it.'}</p>}
          {selConsole && (
            <ConsoleInspector c={selConsole} now={now} onFocus={() => focusNode(selConsole.session.id)} onKill={() => setKill(selConsole.session)} />
          )}
          {selGroup && (
            <div className="mm-insp">
              <h3>{selGroup.name}</h3>
              <p className="text-3 fs-xs">{ROLE_LABEL[selGroup.role as Role] ?? selGroup.role} · {selGroup.members.length} consoles</p>
              <dl className="kv">
                <dt>Last task</dt>
                <dd>{selGroup.lastTask || '—'}</dd>
                <dt>Directed by</dt>
                <dd>{selGroup.directedBy.map((d) => model.directors.find((x) => x.id === d)?.title ?? d).join(', ')}</dd>
              </dl>
            </div>
          )}
          {selDirector && (
            <div className="mm-insp">
              <h3>{selDirector.title}</h3>
              <dl className="kv">
                <dt>Last instruction</dt>
                <dd>{selDirector.last || '—'}</dd>
                <dt>Directs</dt>
                <dd>
                  {selDirector.id === 'bar'
                    ? 'Everything on the board'
                    : `${directedTerminals(doc.nodes, doc.edges, selDirector.id).length} consoles`}
                </dd>
              </dl>
            </div>
          )}
        </aside>
      </div>
      {kill && (
        <ConfirmDialog title={`Kill @${kill.handle ?? kill.name}?`} confirmLabel="Kill console" danger onClose={() => setKill(null)} onConfirm={() => killSessions([kill])}>
          <p>The process stops; the tile stays on the board marked DONE.</p>
        </ConfirmDialog>
      )}
    </Modal>
  );
}

function ConsoleInspector({ c, now, onFocus, onKill }: { c: MConsole; now: number; onFocus: () => void; onKill: () => void }) {
  const s = c.session;
  const pill = agentPill(s);
  const [armed, setArmed] = useState(false);
  const since = s.stateSince ? Date.parse(s.stateSince) : NaN;
  const exited = s.status === 'exited';
  return (
    <div className="mm-insp">
      <h3>@{s.handle ?? s.name}</h3>
      <p className="text-3 fs-xs">
        {agentById(s.agent || s.preset)?.name ?? s.agent} on {s.machineName}
        {c.role ? ` · ${ROLE_LABEL[c.role as Role] ?? c.role}` : ''}
      </p>
      <dl className="kv">
        <dt>Status</dt>
        <dd>
          <span className={`agent-pill tone-${pill.tone}`}>{pill.label}</span>
          {Number.isFinite(since) && <span className="text-3 fs-xs"> for {timeAgo(s.stateSince, now).replace(' ago', '')}</span>}
        </dd>
        <dt>Activity</dt>
        <dd>{pill.activity || s.activity || (s.error && s.status === 'error' ? s.error : '—')}</dd>
        <dt>Task</dt>
        <dd>{c.currentOrder || '—'}</dd>
        <dt>Subagents</dt>
        <dd className="tabular">{c.subagents}</dd>
      </dl>
      <div className="row-gap wrap">
        <Button size="sm" icon="target" onClick={onFocus}>
          Show on board
        </Button>
        {!exited &&
          (s.paused ? (
            <Button size="sm" icon="play" onClick={() => void resumeSessions([s])}>
              Resume
            </Button>
          ) : (
            <Button size="sm" icon="pause" disabled={s.agentState === 'shell'} onClick={() => void pauseSessions([s])}>
              Pause
            </Button>
          ))}
        {exited && (
          <Button size="sm" icon="restart" onClick={() => void restartConsole(s, s.agentSessionId ? { mode: 'resume' } : {}, 'Relaunch')}>
            Relaunch
          </Button>
        )}
        {!exited && (
          <Button
            size="sm"
            variant="danger-ghost"
            icon="power"
            onClick={() => {
              if (armed) {
                setArmed(false);
                onKill();
              } else {
                setArmed(true);
                window.setTimeout(() => setArmed(false), 4000);
              }
            }}
          >
            {armed ? 'Click again to kill' : 'Kill'}
          </Button>
        )}
      </div>
    </div>
  );
}
