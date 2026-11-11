// Layout templates (docs/API-orchestration.md §13, B8), a popup over the board: a library of
// team layouts (built-in first), a preview, how many consoles each group gets, a lane inspector
// (agent, machine, profile per lane, or fill every lane with one agent) and Deploy at the centre
// of the view, which closes the popup. "Save current layout" keeps the board's structure (or the
// selection) as a new template.

import { orchError } from '../actions';
import { useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import { byPriority, useMachineAgents } from '../../agents/catalog';
import { defaultProfileOf, useEffectiveSettings } from '../../agents/settings';
import type { BoardController } from '../../board/controller';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import type { Agent, Project } from '../../lib/types';
import { useMachines, useProfiles } from '../../store';
import { AccountOptions, splitAccount } from '../../providers/AccountOptions';
import { compatible, useProviders } from '../../providers/providers';
import { orchApi } from '../api';
import { is404, useOrch } from '../store';
import { ROLE_LABEL, type LaneOverride, type Layout, type Role } from '../types';

export default function LayoutsPanel({
  ctl,
  project,
  onDeployed,
  onClose,
}: {
  ctl: BoardController;
  project: Project;
  onDeployed: (nodeIds: string[]) => void;
  onClose: () => void;
}) {
  const rev = useOrch((s) => s.layoutsRev);
  const [list, setList] = useState<Layout[] | null>(null);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error' | 'unsupported'>('loading');
  const [error, setError] = useState('');
  const [selId, setSelId] = useState('');
  const [saveName, setSaveName] = useState('');
  const [saving, setSaving] = useState(false);
  const [nonce, setNonce] = useState(0);
  const [deploying, setDeploying] = useState(false);
  const [query, setQuery] = useState('');
  // Deploy sits in the popup's footer; the lane choices it sends live in the inspector
  const deployRef = useRef<(() => Promise<void>) | null>(null);

  useEffect(() => {
    let alive = true;
    orchApi
      .layouts()
      .then((l) => {
        if (!alive) return;
        setList(l);
        setStatus('ready');
        setSelId((cur) => cur || l[0]?.id || '');
      })
      .catch((err) => {
        if (!alive) return;
        setStatus(is404(err) ? 'unsupported' : 'error');
        setError(orchError(err));
      });
    return () => {
      alive = false;
    };
  }, [rev, nonce]);

  const sel = list?.find((l) => l.id === selId) ?? null;
  // by name, team or role ("security", "testers")
  const q = query.trim().toLowerCase();
  const shown = (list ?? []).filter((l) => !q || [l.name, l.description ?? '', ...(l.groups ?? []).flatMap((g) => [g.name, g.role])].join(' ').toLowerCase().includes(q));
  const selection = [...ctl.selection];

  const save = async () => {
    setSaving(true);
    try {
      const l = await orchApi.saveLayout({ workspaceId: ctl.workspaceId, name: saveName.trim() || undefined, nodeIds: selection.length ? selection : undefined });
      toast.success(`Saved “${l.name}”.`);
      setSaveName('');
      setSelId(l.id);
      setNonce((n) => n + 1);
    } catch (err) {
      toast.error(`Could not save the layout: ${orchError(err)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title="Layouts"
      icon="layout"
      size="lg"
      className="layouts-modal"
      onClose={onClose}
      help="Deploy a ready-made team on this board, or keep this board’s setup as a template."
      footer={
        <>
          <Button onClick={onClose}>Close</Button>
          <Button variant="primary" icon="layout" disabled={!sel} loading={deploying} onClick={() => void deployRef.current?.()}>
            {sel ? `Deploy ${sel.name}` : 'Deploy'}
          </Button>
        </>
      }
    >
      {status === 'unsupported' && <div className="list-msg">This hub has no layout templates yet. Update NextBoard on the hub.</div>}
      {status === 'error' && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      {status === 'loading' && (
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Loading layouts…
        </div>
      )}
      {list && (
        <div className="layouts-split">
          <div className="layouts-lib">
            <div className="app-search">
              <Icon name="search" />
              <input className="control control-sm" placeholder="Search layouts" aria-label="Search layouts" autoFocus value={query} onChange={(e) => setQuery(e.target.value)} />
            </div>
            {shown.length === 0 && <p className="app-note">No layouts match.</p>}
            <div className="layout-grid" role="listbox" aria-label="Layouts">
              {shown.map((l) => (
                <button
                  key={l.id}
                  type="button"
                  role="option"
                  aria-selected={l.id === selId}
                  className={`layout-card ${l.id === selId ? 'is-selected' : ''}`}
                  onClick={() => setSelId(l.id)}
                  onDoubleClick={() => setSelId(l.id)}
                >
                  <LayoutPreview layout={l} />
                  <span className="layout-name truncate" title={l.description}>
                    {l.name}
                  </span>
                  <span className="layout-meta">
                    {(l.lanes?.length ?? 0) === 1 ? '1 console' : `${l.lanes?.length ?? 0} consoles`}
                    {l.builtin ? '' : ' · yours'}
                  </span>
                </button>
              ))}
            </div>
          </div>
          <div className="layouts-side">
            {sel && <LaneInspector key={sel.id} layout={sel} ctl={ctl} project={project} deployRef={deployRef} onDeploying={setDeploying} onDeployed={onDeployed} onChanged={() => setNonce((n) => n + 1)} />}
            <section className="memory-section" aria-label="Save current layout">
              <h3 className="section-title">Save current layout</h3>
              <p className="fs-xs text-3">
                {selection.length ? `Saves the ${selection.length === 1 ? 'selected tile' : `${selection.length} selected tiles`}` : 'Saves this board'}: consoles (agent, size), groups, cards, wires and kanban columns. Drawings, images and card contents are left out.
              </p>
              <div className="row-gap">
                <input className="control control-sm grow" placeholder="Set 1" aria-label="Layout name (optional)" value={saveName} onChange={(e) => setSaveName(e.target.value)} maxLength={40} />
                <Button size="sm" icon="download" loading={saving} onClick={() => void save()}>
                  Save layout
                </Button>
              </div>
            </section>
          </div>
        </div>
      )}
    </Modal>
  );
}

const lanePalette = ['sky', 'violet', 'teal', 'amber', 'rose', 'sage'];

/** The hub's limits (internal/orch): it refuses more with too_many, whatever this says. */
const MAX_PER_GROUP = 12;
const MAX_CONSOLES = 12;

export function LayoutPreview({ layout }: { layout: Layout }) {
  const items = [...(layout.lanes ?? []).map((l) => ({ ...l, lane: true as const })), ...(layout.nodes ?? []).map((n) => ({ ...n, lane: false as const }))];
  if (!items.length) return <svg className="layout-preview" viewBox="0 0 100 60" aria-hidden="true" />;
  const minX = Math.min(...items.map((i) => i.x));
  const minY = Math.min(...items.map((i) => i.y));
  const maxX = Math.max(...items.map((i) => i.x + i.w));
  const maxY = Math.max(...items.map((i) => i.y + i.h));
  const w = maxX - minX || 1;
  const h = maxY - minY || 1;
  const s = Math.min(92 / w, 52 / h);
  const ox = (100 - w * s) / 2;
  const oy = (60 - h * s) / 2;
  const groupColor = new Map((layout.groups ?? []).map((g, i) => [g.key, g.color || lanePalette[i % lanePalette.length]]));
  const pos = new Map(items.map((i) => [i.key, { x: ox + (i.x - minX) * s + (i.w * s) / 2, y: oy + (i.y - minY) * s + (i.h * s) / 2 }]));
  return (
    <svg className="layout-preview" viewBox="0 0 100 60" aria-hidden="true">
      {(layout.wires ?? []).map((wi, k) => {
        const a = pos.get(wi.from);
        const groupLanes = (layout.lanes ?? []).filter((l) => l.group === wi.to);
        const b = pos.get(wi.to) ?? (groupLanes[0] ? pos.get(groupLanes[0].key) : undefined);
        return a && b ? <line key={k} x1={a.x} y1={a.y} x2={b.x} y2={b.y} className={wi.kind === 'forward' ? 'pv-forward' : 'pv-wire'} /> : null;
      })}
      {items.map((i) => (
        <rect
          key={i.key}
          x={ox + (i.x - minX) * s}
          y={oy + (i.y - minY) * s}
          width={Math.max(2, i.w * s)}
          height={Math.max(2, i.h * s)}
          rx={1.5}
          className={i.lane ? 'pv-lane' : `pv-node pv-${i.kind}`}
          style={i.lane && i.group ? { fill: `var(--tag-${groupColor.get(i.group) ?? 'sky'})` } : undefined}
        />
      ))}
    </svg>
  );
}

function LaneInspector({
  layout,
  ctl,
  project,
  deployRef,
  onDeploying,
  onDeployed,
  onChanged,
}: {
  layout: Layout;
  ctl: BoardController;
  project: Project;
  deployRef: RefObject<(() => Promise<void>) | null>;
  onDeploying: (busy: boolean) => void;
  onDeployed: (ids: string[]) => void;
  onChanged: () => void;
}) {
  const machines = useMachines();
  const profiles = useProfiles();
  const providers = useProviders();
  const settings = useEffectiveSettings();
  const ma = useMachineAgents(project.machineId);
  const agents = useMemo(() => ma.agents.filter((a) => a.available !== false).sort(byPriority), [ma.agents]);
  const [over, setOver] = useState<Record<string, LaneOverride>>({});
  const [fill, setFill] = useState('');
  const [rename, setRename] = useState<string | null>(null);
  /** Group sizes that differ from the template, and the layout the hub made of them. */
  const [sizes, setSizes] = useState<Record<string, number>>({});
  const [sized, setSized] = useState<Layout | null>(null);
  const shown = sized ?? layout;
  const lanes = shown.lanes ?? [];
  const groups = layout.groups ?? [];
  const groupName = new Map(groups.map((g) => [g.key, g.name]));
  const baseCount = (key: string) => (layout.lanes ?? []).filter((l) => l.group === key).length;
  const countOf = (key: string) => sizes[key] ?? baseCount(key);
  const total = groups.reduce((n, g) => n + countOf(g.key), 0) + (layout.lanes ?? []).filter((l) => !l.group || !groupName.has(l.group)).length;
  const sizesKey = JSON.stringify(sizes);

  useEffect(() => {
    if (sizesKey === '{}') {
      setSized(null);
      return;
    }
    let alive = true;
    orchApi
      .sizedLayout(layout.id, JSON.parse(sizesKey) as Record<string, number>)
      .then((l) => {
        if (alive) setSized(l);
      })
      .catch((err) => {
        if (!alive) return;
        toast.error(orchError(err));
        setSizes({});
      });
    return () => {
      alive = false;
    };
  }, [layout.id, sizesKey]);

  const setSize = (key: string, n: number) =>
    setSizes((cur) => {
      const next = { ...cur };
      if (n === baseCount(key)) delete next[key];
      else next[key] = n;
      return next;
    });

  const nameOf = (id: string) => agents.find((a) => a.id === id)?.name ?? ma.agents.find((a) => a.id === id)?.name ?? id;
  const defaultFor = (role?: string, laneAgent?: string) => {
    if (fill) return fill;
    if (laneAgent) return laneAgent;
    const ra = role ? settings.agents.roleAgents?.[role] : '';
    return ra || ma.defaultAgent || 'shell';
  };
  // another agent may not run on the provider picked for the old one
  const setLane = (key: string, patch: LaneOverride) => setOver((o) => ({ ...o, [key]: { ...o[key], ...(patch.agent !== undefined ? { providerId: undefined } : {}), ...patch } }));
  /** Click cycles through the installed agents (right-click / select picks any). */
  const cycle = (key: string, current: string) => {
    const list = agents.filter((a) => a.kind === 'agent');
    if (!list.length) return;
    const i = list.findIndex((a) => a.id === current);
    setLane(key, { agent: list[(i + 1) % list.length].id });
  };

  deployRef.current = async () => {
    onDeploying(true);
    try {
      const overrides: Record<string, LaneOverride> = {};
      for (const [k, v] of Object.entries(over)) {
        const clean: LaneOverride = {};
        if (v.agent) clean.agent = v.agent;
        if (v.machineId) clean.machineId = v.machineId;
        if (v.profileId) clean.profileId = v.profileId;
        if (v.providerId) clean.providerId = v.providerId;
        if (Object.keys(clean).length) overrides[k] = clean;
      }
      // no "at": the hub lays the team out right of the board's content
      const r = await orchApi.deploy(ctl.workspaceId, {
        layoutId: layout.id,
        ...(fill ? { fill: { agent: fill } } : {}),
        ...(Object.keys(overrides).length ? { laneOverrides: overrides } : {}),
        ...(Object.keys(sizes).length ? { groupSizes: sizes } : {}),
      });
      const n = r.sessions?.length ?? 0;
      toast.success(`Deployed ${layout.name}: ${n === 1 ? '1 console' : `${n} consoles`}.`);
      onDeployed(r.nodeIds ?? []);
    } catch (err) {
      toast.error(`Could not deploy ${layout.name}: ${orchError(err)}`);
    } finally {
      onDeploying(false);
    }
  };

  const agentOptions = (a: Agent[]) =>
    a.map((x) => (
      <option key={x.id} value={x.id}>
        {x.name}
      </option>
    ));

  return (
    <section className="lane-insp" aria-label={`${layout.name} lanes`}>
      <div className="lane-head">
        {rename !== null ? (
          <input
            className="control control-sm grow"
            value={rename}
            autoFocus
            data-keep-escape
            aria-label="Layout name"
            maxLength={40}
            onChange={(e) => setRename(e.target.value)}
            onKeyDown={(e) => {
              e.stopPropagation();
              if (e.key === 'Escape') setRename(null);
              if (e.key === 'Enter' && rename.trim())
                void orchApi
                  .renameLayout(layout.id, rename.trim())
                  .then(() => {
                    setRename(null);
                    onChanged();
                  })
                  .catch((err) => toast.error(orchError(err)));
            }}
          />
        ) : (
          <h3 className="truncate">{layout.name}</h3>
        )}
        <span className="spacer" />
        {!layout.builtin && rename === null && (
          <>
            <Button size="sm" variant="ghost" iconOnly icon="pencil" aria-label={`Rename ${layout.name}`} onClick={() => setRename(layout.name)} />
            <Button
              size="sm"
              variant="danger-ghost"
              iconOnly
              icon="trash"
              aria-label={`Delete ${layout.name}`}
              onClick={() =>
                void orchApi
                  .deleteLayout(layout.id)
                  .then(() => {
                    toast.success(`Deleted “${layout.name}”.`);
                    onChanged();
                  })
                  .catch((err) => toast.error(orchError(err)))
              }
            />
          </>
        )}
      </div>
      {layout.description && <p className="fs-xs text-3 lane-desc">{layout.description}</p>}
      <div className="lane-preview" data-testid="layout-preview">
        <LayoutPreview layout={shown} />
      </div>
      {groups.length > 0 && (
        <div className="lane-sizes" role="group" aria-label="Consoles per group" data-testid="layout-sizes">
          <div className="lane-sizes-head">
            <span className="field-label">Consoles per group</span>
            <span className="text-3 fs-xs tabular">{total === 1 ? '1 console' : `${total} consoles`}</span>
          </div>
          {groups.map((g) => {
            const n = countOf(g.key);
            // a layout with one group keeps it; with more, one can be left out
            const least = groups.length > 1 && total > 1 ? 0 : 1;
            return (
              <div key={g.key} className={`lane-size ${n === 0 ? 'is-off' : ''}`}>
                <span className="grow truncate" title={g.role && g.role !== 'general' ? (ROLE_LABEL[g.role as Role] ?? g.role) : undefined}>
                  {g.name}
                </span>
                <Button size="sm" variant="ghost" iconOnly icon="minus" aria-label={`Fewer consoles in ${g.name}`} disabled={n <= least} onClick={() => setSize(g.key, n - 1)} />
                <span className="lane-size-n tabular" aria-label={`${g.name}: ${n}`}>
                  {n}
                </span>
                <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label={`More consoles in ${g.name}`} disabled={n >= MAX_PER_GROUP || total >= MAX_CONSOLES} onClick={() => setSize(g.key, n + 1)} />
              </div>
            );
          })}
          {Object.keys(sizes).length > 0 && (
            <div>
              <Button size="sm" variant="ghost" onClick={() => setSizes({})}>
                Back to the template’s sizes
              </Button>
            </div>
          )}
        </div>
      )}
      <div className="lane-fill">
        <label className="field-label" htmlFor={`fill-${layout.id}`}>
          Fill every lane with
        </label>
        <select id={`fill-${layout.id}`} className="control control-sm" value={fill} onChange={(e) => setFill(e.target.value)}>
          <option value="">Each lane’s own agent</option>
          {agentOptions(agents.filter((a) => a.kind === 'agent'))}
        </select>
      </div>
      <ul className="lane-list">
        {lanes.map((l) => {
          const o = over[l.key] ?? {};
          const eff = o.agent || defaultFor(l.role, l.agent);
          const mid = o.machineId || project.machineId;
          const profs = profiles.filter((p) => p.machineId === mid && p.agent === eff);
          const effAgent = ma.agents.find((a) => a.id === eff);
          const provs = providers.filter((p) => compatible(effAgent, p));
          const defaultId = defaultProfileOf(settings, mid, eff);
          return (
            <li key={l.key} className="lane-row">
              <div className="lane-label">
                <span className="lane-key">{l.role && l.role !== 'general' ? (ROLE_LABEL[l.role as Role] ?? l.role) : `Lane ${l.key}`}</span>
                {l.group && <span className="text-3 fs-xs truncate">{groupName.get(l.group) ?? l.group}</span>}
              </div>
              <button
                type="button"
                className="lane-agent"
                title="Click to cycle the agent; use the list for any agent"
                onClick={() => cycle(l.key, eff)}
                onContextMenu={(e) => {
                  e.preventDefault();
                  (e.currentTarget.nextElementSibling as HTMLSelectElement | null)?.focus();
                }}
              >
                {nameOf(eff)}
              </button>
              <select className="control control-sm lane-select" aria-label={`Agent for lane ${l.key}`} value={o.agent ?? ''} onChange={(e) => setLane(l.key, { agent: e.target.value || undefined })}>
                <option value="">Default ({nameOf(defaultFor(l.role, l.agent))})</option>
                {agentOptions(ma.agents.filter((a) => a.available !== false))}
              </select>
              <select className="control control-sm lane-select" aria-label={`Machine for lane ${l.key}`} value={mid} onChange={(e) => setLane(l.key, { machineId: e.target.value === project.machineId ? undefined : e.target.value, profileId: undefined, providerId: undefined })}>
                {machines.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </select>
              {profs.length + provs.length > 0 && (
                <select
                  className="control control-sm lane-select"
                  aria-label={`Login profile for lane ${l.key}`}
                  value={o.providerId ?? o.profileId ?? ''}
                  onChange={(e) => setLane(l.key, { profileId: undefined, providerId: undefined, ...splitAccount(e.target.value) })}
                >
                  <AccountOptions profiles={profs} defaultProfile={defaultId} providers={provs} />
                </select>
              )}
            </li>
          );
        })}
        {lanes.length === 0 && <li className="fs-xs text-3">No consoles in this layout; it adds cards only.</li>}
      </ul>
    </section>
  );
}
