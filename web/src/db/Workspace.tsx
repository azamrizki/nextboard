// The database browser of a project (docs/API-db.md): its connections on the left, each with its
// schema (tables, views, routines), the saved queries and the history; tables, SQL editors and
// Redis keys in tabs; SmartDB on the right. The Code view's Databases tab and every Database card
// of the board show it; a card may keep to one connection. A narrow one (a small card, a phone)
// puts the connections behind a button.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Menu, type MenuEntry } from '../components/Menu';
import { toast } from '../components/Toast';
import { formatDateTime } from '../lib/format';
import { dbApi } from './api';
import { DriverMark, EnvTag, useConfirmRunner } from './Confirm';
import { EditConnection, NewConnection } from './ConnectionDialog';
import { RedisView } from './RedisView';
import { SmartPanel } from './SmartPanel';
import { SqlEditor } from './SqlEditor';
import { useConnections, useQueries, useSchemaRev, useStatus } from './store';
import { TableView } from './TableView';
import { isKV, type Connection, type Filter, type HistoryItem, type Schema } from './types';

export interface DbTab {
  id: string;
  kind: 'table' | 'query' | 'redis';
  conn: string;
  title: string;
  schema?: string;
  table?: string;
  filter?: Filter;
  queryId?: string;
  sql?: string;
}

interface Saved {
  tabs: DbTab[];
  active: string;
  smart: boolean;
}

const MAX_TABS = 16;
/** Narrower than this, the connections go behind a button: a phone, a small card, a Code view with its panels open. */
const NARROW = 640;

function loadSaved(key: string): Saved {
  try {
    const raw = localStorage.getItem(`nb.db.ws.${key}`);
    if (raw) {
      const s = JSON.parse(raw) as Saved;
      if (Array.isArray(s.tabs)) return { tabs: s.tabs.slice(0, MAX_TABS), active: s.active ?? '', smart: !!s.smart };
    }
  } catch {
    /* nothing kept: a fresh workspace */
  }
  return { tabs: [], active: '', smart: false };
}

function keep(key: string, s: Saved) {
  try {
    localStorage.setItem(`nb.db.ws.${key}`, JSON.stringify(s));
  } catch {
    /* storage blocked: the tabs live in this page */
  }
}

const newId = () => Math.random().toString(36).slice(2, 10);

export interface WorkspaceProps {
  projectId: string;
  /** where its tabs are kept in this browser: "code:<project>", "card:<node>" */
  storeKey: string;
  /** a card kept to one connection */
  only?: string;
  /** what a card shows when it has no room: no SmartDB side by side */
  compact?: boolean;
}

export function DbWorkspace({ projectId, storeKey, only, compact }: WorkspaceProps) {
  const { list: all, loaded } = useConnections(projectId);
  const list = useMemo(() => (only ? all.filter((c) => c.id === only) : all), [all, only]);
  const [saved, setSaved] = useState<Saved>(() => loadSaved(storeKey));
  const [sel, setSel] = useState<string>('');
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<string>('');
  const [sideOpen, setSideOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(1200);

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);
  const narrow = width < NARROW;
  const medium = !narrow && width < 1000;

  useEffect(() => keep(storeKey, saved), [storeKey, saved]);

  // tabs of a connection that is gone close
  useEffect(() => {
    if (!loaded) return;
    const ids = new Set(all.map((c) => c.id));
    setSaved((s) => {
      const tabs = s.tabs.filter((t) => ids.has(t.conn) && (!only || t.conn === only));
      return tabs.length === s.tabs.length ? s : { ...s, tabs, active: tabs.some((t) => t.id === s.active) ? s.active : (tabs[0]?.id ?? '') };
    });
  }, [all, loaded, only]);

  const active = saved.tabs.find((t) => t.id === saved.active);
  const selected = list.find((c) => c.id === (sel || active?.conn)) ?? list[0];
  const activeConn = list.find((c) => c.id === active?.conn) ?? selected;

  const openTab = useCallback((t: Omit<DbTab, 'id'>, reuse = true) => {
    setSaved((s) => {
      const same = reuse ? s.tabs.find((x) => x.kind === t.kind && x.conn === t.conn && x.schema === t.schema && x.table === t.table && x.queryId === t.queryId && JSON.stringify(x.filter ?? null) === JSON.stringify(t.filter ?? null) && t.kind !== 'query') : undefined;
      if (same) return { ...s, active: same.id };
      if (reuse && t.kind === 'query' && t.queryId) {
        const q = s.tabs.find((x) => x.kind === 'query' && x.queryId === t.queryId);
        if (q) return { ...s, active: q.id };
      }
      const tab = { ...t, id: newId() };
      let tabs = [...s.tabs, tab];
      if (tabs.length > MAX_TABS) tabs = tabs.slice(tabs.length - MAX_TABS);
      return { ...s, tabs, active: tab.id };
    });
    setSideOpen(false);
  }, []);

  const closeTab = (id: string) =>
    setSaved((s) => {
      const i = s.tabs.findIndex((t) => t.id === id);
      const tabs = s.tabs.filter((t) => t.id !== id);
      try {
        localStorage.removeItem(`nb.db.q.${id}`);
      } catch {
        /* nothing kept */
      }
      return { ...s, tabs, active: s.active === id ? (tabs[Math.max(0, i - 1)]?.id ?? '') : s.active };
    });

  const newQuery = (c: Connection, sql?: string) => {
    const n = saved.tabs.filter((t) => t.kind === 'query' && !t.queryId).length + 1;
    openTab({ kind: 'query', conn: c.id, title: `SQL ${n}`, sql }, false);
  };

  const openConn = (c: Connection) => {
    setSel(c.id);
    if (isKV(c.driver)) openTab({ kind: 'redis', conn: c.id, title: c.name });
  };

  if (loaded && list.length === 0) {
    return (
      <div className="db-ws db-ws-empty" ref={box}>
        <div className="db-empty-cta">
          <span className="db-empty-art" aria-hidden="true">
            <Icon name="database" />
          </span>
          <h3>{only ? 'This card’s connection is gone' : 'No database yet'}</h3>
          <p className="text-2">Connect MySQL, MariaDB, PostgreSQL, Redis or Valkey: directly, over a machine of this project or through an SSH tunnel. NextBoard finds the ones in this project’s .env files.</p>
          <Button variant="primary" icon="plus" onClick={() => setCreating(true)}>
            New connection
          </Button>
        </div>
        {creating && <NewConnection projectId={projectId} onClose={() => setCreating(false)} onDone={openConn} />}
      </div>
    );
  }

  const side = (
    <aside className={`db-side ${narrow ? 'is-overlay' : ''} ${narrow && !sideOpen ? 'is-hidden' : ''}`} aria-label="Connections">
      <div className="db-side-head">
        <b>{only ? 'Connection' : 'Connections'}</b>
        <span className="db-spacer" />
        {!only && <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="New connection" title="New connection" onClick={() => setCreating(true)} />}
        {narrow && <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close" onClick={() => setSideOpen(false)} />}
      </div>
      <div className="db-side-body">
        {groupsOf(list).map(([group, conns]) => (
          <div key={group || '-'} className="db-side-group">
            {group && <div className="db-side-gh">{group}</div>}
            {conns.map((c) => (
              <ConnNode
                key={c.id}
                c={c}
                open={selected?.id === c.id}
                onSelect={() => openConn(c)}
                onEdit={() => setEditing(c.id)}
                onNewQuery={() => newQuery(c)}
                onTable={(schema, table) => openTab({ kind: 'table', conn: c.id, schema, table, title: table })}
                onSQL={(sql) => newQuery(c, sql)}
              />
            ))}
          </div>
        ))}
        <SavedQueries projectId={projectId} conns={list} onOpen={(q, c) => openTab({ kind: 'query', conn: c.id, title: q.name, queryId: q.id })} only={only} />
        {selected && !isKV(selected.driver) && <History conn={selected} onOpen={(sql) => newQuery(selected, sql)} />}
      </div>
    </aside>
  );

  const smartOn = saved.smart && !!activeConn && activeConn.agents.access !== 'none';
  const smartOverlay = narrow || compact;

  return (
    <div className={`db-ws ${narrow ? 'is-narrow' : medium ? 'is-medium' : ''}`} ref={box}>
      {side}
      {narrow && sideOpen && <div className="db-scrim" onClick={() => setSideOpen(false)} />}
      <section className="db-main">
        <div className="db-tabs" role="tablist" aria-label="Database tabs">
          {narrow && (
            <Button size="sm" variant="ghost" iconOnly icon="database" aria-label="Connections" title="Connections" onClick={() => setSideOpen(true)} />
          )}
          <div className="db-tabs-scroll">
            {saved.tabs.map((t) => {
              const c = all.find((x) => x.id === t.conn);
              return (
                <div key={t.id} role="tab" aria-selected={t.id === saved.active} className={`db-tab ${t.id === saved.active ? 'is-on' : ''} db-tab-${c?.env ?? 'local'}`}>
                  <button type="button" className="db-tab-btn" onClick={() => setSaved({ ...saved, active: t.id })} onAuxClick={(e) => e.button === 1 && closeTab(t.id)} title={`${c?.name ?? ''} · ${t.schema ? t.schema + '.' : ''}${t.title}`}>
                    <Icon name={t.kind === 'table' ? 'table' : t.kind === 'redis' ? 'database' : 'fileCode'} />
                    <span className="truncate">{t.title}</span>
                  </button>
                  <button type="button" className="db-tab-x" aria-label={`Close ${t.title}`} onClick={() => closeTab(t.id)}>
                    <Icon name="close" />
                  </button>
                </div>
              );
            })}
          </div>
          {selected && !isKV(selected.driver) && (
            <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="New SQL editor" title={`New SQL editor on ${selected.name}`} onClick={() => newQuery(selected)} />
          )}
          <span className="db-spacer" />
          {activeConn && activeConn.agents.access !== 'none' && (
            <Button size="sm" variant={saved.smart ? 'secondary' : 'ghost'} icon="sparkle" onClick={() => setSaved({ ...saved, smart: !saved.smart })} aria-pressed={saved.smart}>
              {narrow ? '' : 'SmartDB'}
            </Button>
          )}
        </div>
        <div className="db-main-body">
          {saved.tabs.length === 0 && (
            <div className="db-empty db-start">
              {selected ? (
                isKV(selected.driver) ? (
                  <Button icon="database" onClick={() => openConn(selected)}>
                    Open {selected.name}
                  </Button>
                ) : (
                  <>
                    <p className="text-2">Open a table from {narrow ? 'Connections' : 'the left'}, or write SQL.</p>
                    <Button icon="fileCode" onClick={() => newQuery(selected)}>
                      New SQL editor
                    </Button>
                  </>
                )
              ) : (
                <span className="spinner" />
              )}
            </div>
          )}
          {saved.tabs.map((t) => {
            const c = all.find((x) => x.id === t.conn);
            if (!c) return null;
            return (
              <div key={t.id} className="db-tab-body" hidden={t.id !== saved.active}>
                {t.kind === 'table' && (
                  <TableView conn={c} schema={t.schema ?? ''} table={t.table ?? ''} compact={narrow} initialFilter={t.filter} onOpenTable={(schema, table, filter) => openTab({ kind: 'table', conn: c.id, schema, table, filter, title: table })} />
                )}
                {t.kind === 'query' && <SqlEditor conn={c} tabId={t.id} queryId={t.queryId} initialSQL={t.sql} compact={narrow} />}
                {t.kind === 'redis' && <RedisView conn={c} compact={narrow} />}
              </div>
            );
          })}
        </div>
        {activeConn && <StatusBar conn={activeConn} onEdit={() => setEditing(activeConn.id)} />}
      </section>
      {smartOn && activeConn && (
        <div className={`db-smart-wrap ${smartOverlay ? 'is-overlay' : ''}`}>
          <SmartPanel
            key={activeConn.id}
            conn={activeConn}
            table={active?.kind === 'table' ? active.table : undefined}
            schema={active?.schema}
            onOpenQuery={(sql) => newQuery(activeConn, sql)}
            onClose={() => setSaved({ ...saved, smart: false })}
          />
        </div>
      )}
      {creating && <NewConnection projectId={projectId} onClose={() => setCreating(false)} onDone={openConn} />}
      {editing && list.find((c) => c.id === editing) && <EditConnection conn={list.find((c) => c.id === editing)!} onClose={() => setEditing('')} />}
    </div>
  );
}

function groupsOf(list: Connection[]): [string, Connection[]][] {
  const m = new Map<string, Connection[]>();
  for (const c of list) m.set(c.group ?? '', [...(m.get(c.group ?? '') ?? []), c]);
  return [...m.entries()].sort((a, b) => (a[0] === '' ? -1 : b[0] === '' ? 1 : a[0].localeCompare(b[0])));
}

function StatusDot({ id }: { id: string }) {
  const st = useStatus(id);
  const cls = st?.error ? 'dot-danger' : st?.connected ? 'dot-success' : 'dot-grey';
  return <span className={`dot ${cls}`} aria-label={st?.error ? 'error' : st?.connected ? 'connected' : 'not connected'} title={st?.error || (st?.connected ? `Connected${st.version ? ' · ' + st.version : ''}` : 'Not connected')} />;
}

function ConnNode(p: { c: Connection; open: boolean; onSelect: () => void; onEdit: () => void; onNewQuery: () => void; onTable: (schema: string, table: string) => void; onSQL: (sql: string) => void }) {
  const { c } = p;
  const kv = isKV(c.driver);
  const menu = (): MenuEntry[] => [
    ...(kv ? [] : [{ label: 'New SQL editor', icon: 'fileCode' as const, onSelect: p.onNewQuery }]),
    { label: 'Edit connection', icon: 'pencil', onSelect: p.onEdit, shortcut: 'F4' },
    { label: 'Disconnect', icon: 'power', onSelect: () => void dbApi.disconnect(c.id).catch((e) => toast.error(errorMessage(e))) },
  ];
  return (
    <div className={`db-conn ${p.open ? 'is-open' : ''}`}>
      <div className="db-conn-row">
        <button type="button" className="db-conn-btn" onClick={p.onSelect} onKeyDown={(e) => e.key === 'F4' && (e.preventDefault(), p.onEdit())} aria-expanded={kv ? undefined : p.open}>
          {!kv && <Icon name={p.open ? 'chevronDown' : 'chevronRight'} />}
          <DriverMark driver={c.driver} />
          <span className="db-conn-name truncate">{c.name}</span>
          <EnvTag env={c.env} />
          {c.security.readOnly && <Icon name="lock" className="db-ro" aria-label="read-only" />}
          {c.reach.kind !== 'direct' && <Icon name="link" className="db-ro" aria-label="through SSH" />}
          <StatusDot id={c.id} />
        </button>
        <Menu label={`Actions for ${c.name}`} items={menu} className="db-conn-menu" />
      </div>
      {p.open && !kv && <SchemaTree c={c} onTable={p.onTable} onSQL={p.onSQL} />}
    </div>
  );
}

function SchemaTree({ c, onTable, onSQL }: { c: Connection; onTable: (schema: string, table: string) => void; onSQL: (sql: string) => void }) {
  const [cur, setCur] = useState('');
  const [sc, setSc] = useState<Schema | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [q, setQ] = useState('');
  const [shut, setShut] = useState<Record<string, boolean>>({ routines: true });
  const rev = useSchemaRev(c.id);
  const [run, dialog] = useConfirmRunner(c);
  const pg = c.driver === 'postgres';

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const r = await run(() => dbApi.schema(c.id, cur));
      if (r?.schema) {
        setSc(r.schema);
        if (!cur) setCur(r.schema.current);
      } else if (!r) setError('The password is needed to connect.');
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [c.id, cur, run]);

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [c.id, cur, rev]);

  const match = (n: string) => !q || n.toLowerCase().includes(q.toLowerCase());
  const section = (key: string, label: string, items: Schema['tables'], icon: 'table' | 'eye' | 'braces') => {
    const xs = (items ?? []).filter((t) => match(t.name));
    if (!items?.length) return null;
    return (
      <div className="db-tree-sec">
        <button type="button" className="db-tree-h" onClick={() => setShut({ ...shut, [key]: !shut[key] })} aria-expanded={!shut[key]}>
          <Icon name={shut[key] ? 'chevronRight' : 'chevronDown'} />
          {label}
          <span className="db-n">{xs.length}</span>
        </button>
        {!shut[key] &&
          xs.map((t) => (
            <button
              key={t.name}
              type="button"
              className="db-tree-item"
              onClick={() => (key === 'routines' ? onSQL(pg ? `SELECT ${t.name}();` : `CALL ${t.name}();`) : onTable(cur, t.name))}
              title={t.rows >= 0 && key === 'tables' ? `≈${t.rows.toLocaleString()} rows` : t.type}
            >
              <Icon name={icon} />
              <span className="truncate">{t.name}</span>
              {key === 'tables' && t.rows >= 0 && <span className="db-n tabular">{compactNum(t.rows)}</span>}
            </button>
          ))}
      </div>
    );
  };

  return (
    <div className="db-tree" role="tree" aria-label={`${c.name} schema`}>
      {(sc?.databases?.length ?? 0) > 1 && (
        <select className="db-dbsel" value={cur} onChange={(e) => setCur(e.target.value)} aria-label={pg ? 'Schema' : 'Database'}>
          {sc!.databases!.map((d) => (
            <option key={d}>{d}</option>
          ))}
        </select>
      )}
      {(sc?.tables?.length ?? 0) > 8 && <input className="db-tree-q" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter tables" aria-label="Filter tables" />}
      {loading && !sc && (
        <div className="db-tree-msg text-3">
          <span className="spinner" /> Connecting…
        </div>
      )}
      {error && (
        <div className="db-tree-msg db-tree-err">
          {error}
          <Button size="sm" variant="ghost" icon="refresh" onClick={() => void load()}>
            Retry
          </Button>
        </div>
      )}
      {sc && (
        <>
          {section('tables', 'Tables', sc.tables, 'table')}
          {section('views', 'Views', sc.views, 'eye')}
          {section('routines', pg ? 'Functions' : 'Routines', sc.routines, 'braces')}
          {!sc.tables?.length && !sc.views?.length && <div className="db-tree-msg text-3">No table in {sc.current || 'this database'}.</div>}
        </>
      )}
      {dialog}
    </div>
  );
}

function compactNum(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1e6) return `${(n / 1000).toFixed(n < 1e4 ? 1 : 0)}k`;
  return `${(n / 1e6).toFixed(n < 1e7 ? 1 : 0)}M`;
}

function SavedQueries({ projectId, conns, onOpen, only }: { projectId: string; conns: Connection[]; onOpen: (q: { id: string; name: string }, c: Connection) => void; only?: string }) {
  const queries = useQueries(projectId);
  const [shut, setShut] = useState(false);
  const list = queries.filter((q) => !only || !q.connectionId || q.connectionId === only);
  if (list.length === 0) return null;
  return (
    <div className="db-side-sec">
      <button type="button" className="db-tree-h" onClick={() => setShut(!shut)} aria-expanded={!shut}>
        <Icon name={shut ? 'chevronRight' : 'chevronDown'} />
        <Icon name="star" /> Saved queries
        <span className="db-n">{list.length}</span>
      </button>
      {!shut &&
        list.map((q) => {
          const c = conns.find((x) => x.id === q.connectionId) ?? conns.find((x) => !isKV(x.driver));
          return (
            <div key={q.id} className="db-tree-row">
              <button type="button" className="db-tree-item" disabled={!c} onClick={() => c && onOpen(q, c)} title={q.sql.slice(0, 400)}>
                <Icon name="fileCode" />
                <span className="truncate">{q.name}</span>
              </button>
              <button
                type="button"
                className="db-tree-x"
                aria-label={`Delete ${q.name}`}
                onClick={() => void dbApi.deleteQuery(projectId, q.id).catch((e) => toast.error(errorMessage(e)))}
              >
                <Icon name="trash" />
              </button>
            </div>
          );
        })}
    </div>
  );
}

function History({ conn, onOpen }: { conn: Connection; onOpen: (sql: string) => void }) {
  const [shut, setShut] = useState(true);
  const [items, setItems] = useState<HistoryItem[] | null>(null);
  const rev = useSchemaRev(conn.id);
  useEffect(() => {
    if (shut) return;
    void dbApi
      .history(conn.id, 60)
      .then((r) => setItems(r.items))
      .catch(() => setItems([]));
  }, [conn.id, shut, rev]);
  return (
    <div className="db-side-sec">
      <button type="button" className="db-tree-h" onClick={() => setShut(!shut)} aria-expanded={!shut}>
        <Icon name={shut ? 'chevronRight' : 'chevronDown'} />
        <Icon name="history" /> History
      </button>
      {!shut && items?.length === 0 && <div className="db-tree-msg text-3">Nothing ran yet.</div>}
      {!shut &&
        items?.map((h, i) => (
          <button key={i} type="button" className={`db-tree-item db-hist ${h.error ? 'is-err' : ''}`} onClick={() => onOpen(h.sql)} title={`${formatDateTime(h.at)} · ${h.ms} ms${h.who ? ' · ' + h.who : ''}\n${h.sql.slice(0, 600)}`}>
            <span className="mono truncate">{h.sql.replace(/\s+/g, ' ')}</span>
            <span className="db-n">{h.who ? h.who.slice(0, 10) : `${h.ms}ms`}</span>
          </button>
        ))}
    </div>
  );
}

function StatusBar({ conn, onEdit }: { conn: Connection; onEdit: () => void }) {
  const st = useStatus(conn.id);
  return (
    <div className="db-status">
      <DriverMark driver={conn.driver} />
      <b className="truncate">{conn.name}</b>
      <EnvTag env={conn.env} />
      {st?.connected ? (
        <span className="text-2">
          <span className="dot dot-success" /> {st.version || 'connected'}
        </span>
      ) : st?.error ? (
        <span className="text-danger truncate" title={st.error}>
          <span className="dot dot-danger" /> {st.error}
        </span>
      ) : (
        <span className="text-3">not connected</span>
      )}
      {st?.tunnel && (
        <span className="text-2" title="Through an SSH tunnel">
          <Icon name="link" /> tunnel{st.localPort ? ` · 127.0.0.1:${st.localPort}` : ''}
        </span>
      )}
      <span className="text-3">
        {conn.security.readOnly ? (
          <>
            <Icon name="lock" /> read-only
          </>
        ) : conn.security.confirmWrites ? (
          'writes ask first'
        ) : (
          'full access'
        )}
      </span>
      <span className="db-spacer" />
      <Button size="sm" variant="ghost" icon="pencil" onClick={onEdit}>
        Edit
      </Button>
    </div>
  );
}
