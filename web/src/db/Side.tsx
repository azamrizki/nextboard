// The Databases side view (docs/API-db.md; mockups 1, 2 and 4): the project's connections as
// cards, the one chosen with its schema under it (tables, views, routines), its saved queries and
// history; a Redis connection with its databases and server. Picking something opens it as an
// editor tab. On a phone the same list, as cards (mockup 7).

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Menu, type MenuEntry } from '../components/Menu';
import { toast } from '../components/Toast';
import { dbApi } from './api';
import { DriverMark, EnvTag, useConfirmRunner } from './Confirm';
import { connWhere, useMachineName } from './where';
import { EditConnection, NewConnection } from './ConnectionDialog';
import { useConnections, useQueries, useSchemaRev, useStatus } from './store';
import type { DbTarget } from './tabs';
import { isKV, type Connection, type HistoryItem, type KVInfo, type Schema } from './types';

export function compactNum(n: number): string {
  if (n < 0) return '';
  if (n < 1000) return String(n);
  if (n < 1e6) return `${(n / 1000).toFixed(n < 1e4 ? 1 : 0)}k`;
  return `${(n / 1e6).toFixed(n < 1e7 ? 1 : 0)}M`;
}

const SEL_KEY = (pid: string) => `nb.db.side.${pid}`;
const SHUT_KEY = (pid: string) => `nb.db.side.folded.${pid}`;

export interface DbSideProps {
  projectId: string;
  /** the tab in front: its connection is chosen, its table marked */
  active?: DbTarget;
  onOpen: (t: DbTarget, sql?: string) => void;
  /** a phone: cards, as its mockup */
  phone?: boolean;
}

export function DbSide({ projectId, active, onOpen, phone }: DbSideProps) {
  const { list, loaded } = useConnections(projectId);
  const [sel, setSel] = useState<string>(() => {
    try {
      return localStorage.getItem(SEL_KEY(projectId)) ?? '';
    } catch {
      return '';
    }
  });
  // the chosen connection's schema shown, or folded away (its card clicked again)
  const [shut, setShut] = useState<boolean>(() => {
    try {
      return localStorage.getItem(SHUT_KEY(projectId)) === '1';
    } catch {
      return false;
    }
  });
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState('');
  const machineName = useMachineName();

  // the tab in front chooses its connection, unfolded when it is another one
  const activeConn = active && active.kind !== 'home' ? active.conn : '';
  const lastActive = useRef(activeConn);
  useEffect(() => {
    if (activeConn && activeConn !== lastActive.current) {
      if (activeConn !== sel) setShut(false);
      setSel(activeConn);
    }
    lastActive.current = activeConn;
    // only a new tab in front moves the choice, so sel is read but not a dependency
  }, [activeConn]);
  useEffect(() => {
    try {
      localStorage.setItem(SHUT_KEY(projectId), shut ? '1' : '0');
    } catch {
      /* kept in this page */
    }
  }, [projectId, shut]);
  useEffect(() => {
    try {
      if (sel) localStorage.setItem(SEL_KEY(projectId), sel);
    } catch {
      /* the choice lives in this page */
    }
  }, [projectId, sel]);

  const chosen = list.find((c) => c.id === sel) ?? list[0];
  const open = !!chosen && !shut;
  const choose = (c: Connection) => {
    // the one chosen (by a click or its tab, not only by being first) folds or unfolds; another is
    // chosen, unfolded, and a Redis connection's keys open
    if (c.id === sel && list.some((x) => x.id === sel)) {
      setShut(!shut);
      return;
    }
    setSel(c.id);
    setShut(false);
    if (isKV(c.driver)) onOpen({ kind: 'keys', conn: c.id });
  };

  const menu = (): MenuEntry[] => [
    { label: 'New connection', icon: 'plus', onSelect: () => setCreating(true) },
    ...(chosen && !isKV(chosen.driver) ? [{ label: `New SQL editor on ${chosen.name}`, icon: 'fileCode' as const, onSelect: () => onOpen({ kind: 'query', conn: chosen.id, id: '' }) }] : []),
    ...(chosen ? [{ label: `Edit ${chosen.name}`, icon: 'pencil' as const, onSelect: () => setEditing(chosen.id) }] : []),
  ];

  return (
    <section className={`db-side ${phone ? 'is-phone' : ''}`} aria-label="Databases">
      <header className="db-side-h">
        <b>Databases</b>
        <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="New connection" title="New connection" onClick={() => setCreating(true)} />
        <Menu label="Databases actions" items={menu} />
      </header>
      <div className="db-side-body">
        {phone && <div className="db-cap">Connections</div>}
        {loaded && list.length === 0 && (
          <div className="db-side-empty">
            <Icon name="database" />
            <p>Connect MySQL, MariaDB, PostgreSQL, Redis or Valkey: directly, over a machine of this project or through an SSH tunnel.</p>
            <Button size="sm" variant="primary" icon="plus" onClick={() => setCreating(true)}>
              New connection
            </Button>
          </div>
        )}
        <div className="db-conns" role="list">
          {list.map((c) => (
            <ConnCard key={c.id} c={c} on={chosen?.id === c.id} expanded={open && chosen?.id === c.id} onChoose={() => choose(c)} onEdit={() => setEditing(c.id)} onNewQuery={() => onOpen({ kind: 'query', conn: c.id, id: '' })} machineName={machineName} phone={phone} />
          ))}
          {phone && list.length > 0 && (
            <button type="button" className="db-new-conn" onClick={() => setCreating(true)}>
              <Icon name="plus" /> New connection
            </button>
          )}
        </div>
        {open && !isKV(chosen.driver) && <SqlTree key={chosen.id} c={chosen} active={active} onOpen={onOpen} phone={phone} />}
        {open && isKV(chosen.driver) && <KvTree key={chosen.id} c={chosen} active={active} onOpen={onOpen} />}
        <SavedQueries projectId={projectId} conns={list} active={active} onOpen={onOpen} phone={phone} />
        {open && !isKV(chosen.driver) && !phone && <History c={chosen} onOpen={(sql) => onOpen({ kind: 'query', conn: chosen.id, id: '' }, sql)} />}
      </div>
      {creating && <NewConnection projectId={projectId} onClose={() => setCreating(false)} onDone={(c) => choose(c)} />}
      {editing && list.find((c) => c.id === editing) && <EditConnection conn={list.find((c) => c.id === editing)!} onClose={() => setEditing('')} />}
    </section>
  );
}

function ConnCard(p: { c: Connection; on: boolean; expanded: boolean; onChoose: () => void; onEdit: () => void; onNewQuery: () => void; machineName: (id: string) => string; phone?: boolean }) {
  const { c } = p;
  const st = useStatus(c.id);
  const kv = isKV(c.driver);
  const menu = (): MenuEntry[] => [
    ...(kv ? [] : [{ label: 'New SQL editor', icon: 'fileCode' as const, onSelect: p.onNewQuery }]),
    { label: 'Edit connection', icon: 'pencil', onSelect: p.onEdit, shortcut: 'F4' },
    { label: 'Disconnect', icon: 'power', onSelect: () => void dbApi.disconnect(c.id).catch((e) => toast.error(errorMessage(e))) },
  ];
  const where = connWhere(c, st?.version, p.machineName);
  return (
    <div className={`db-conn ${p.on ? 'is-on' : ''}`} role="listitem">
      <button
        type="button"
        className="db-conn-btn"
        aria-current={p.on ? 'true' : undefined}
        aria-expanded={p.expanded}
        onClick={p.onChoose}
        onKeyDown={(e) => e.key === 'F4' && (e.preventDefault(), p.onEdit())}
        title={st?.error ? `${c.name}: ${st.error}` : `${c.name} · ${where}`}
      >
        <Icon name={p.expanded ? 'chevronDown' : 'chevronRight'} className="db-conn-chev" />
        <DriverMark driver={c.driver} size={p.phone ? 'lg' : undefined} />
        <span className="db-conn-text">
          <span className="db-conn-name">
            <span className="truncate">{c.name}</span>
            {c.security.readOnly && <Icon name="lock" className="db-conn-lock" aria-label="read-only" />}
            {st?.error && <span className="dot dot-danger" aria-label="cannot connect" />}
          </span>
          <span className="db-conn-sub truncate">{where}</span>
        </span>
        <EnvTag env={c.env} />
      </button>
      <Menu label={`Actions for ${c.name}`} items={menu} className="db-conn-menu" />
    </div>
  );
}

function Section({ id, label, count, open, onToggle, children, extra }: { id: string; label: string; count?: string | number; open: boolean; onToggle: () => void; children: ReactNode; extra?: ReactNode }) {
  return (
    <div className="db-sec" data-sec={id}>
      <button type="button" className="db-sec-h" aria-expanded={open} onClick={onToggle}>
        <Icon name={open ? 'chevronDown' : 'chevronRight'} />
        <span>{label}</span>
        {extra}
        {count !== undefined && <span className="db-sec-n">{count}</span>}
      </button>
      {open && <div className="db-sec-body">{children}</div>}
    </div>
  );
}

function useShut(key: string, init: Record<string, boolean>) {
  const [shut, setShut] = useState<Record<string, boolean>>(() => {
    try {
      return { ...init, ...(JSON.parse(localStorage.getItem(key) ?? '{}') as Record<string, boolean>) };
    } catch {
      return init;
    }
  });
  const toggle = (id: string) =>
    setShut((s) => {
      const n = { ...s, [id]: !s[id] };
      try {
        localStorage.setItem(key, JSON.stringify(n));
      } catch {
        /* kept in this page */
      }
      return n;
    });
  return [shut, toggle] as const;
}

function SqlTree({ c, active, onOpen, phone }: { c: Connection; active?: DbTarget; onOpen: DbSideProps['onOpen']; phone?: boolean }) {
  const [cur, setCur] = useState('');
  const [sc, setSc] = useState<Schema | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [q, setQ] = useState('');
  const [shut, toggle] = useShut('nb.db.side.shut', { views: true, routines: true });
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
    // the schema again when the connection, the database or the tables change
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [c.id, cur, rev]);

  const match = (n: string) => !q || n.toLowerCase().includes(q.toLowerCase());
  const isOn = (name: string) => active?.kind === 'table' && active.conn === c.id && active.table === name && !active.filter;

  const items = (key: 'tables' | 'views' | 'routines', icon: 'table' | 'eye' | 'braces') =>
    (sc?.[key] ?? [])
      .filter((t) => match(t.name))
      .map((t) => (
        <button
          key={t.name}
          type="button"
          className={`db-it ${key !== 'routines' && isOn(t.name) ? 'is-on' : ''}`}
          onClick={() => (key === 'routines' ? onOpen({ kind: 'query', conn: c.id, id: '' }, pg ? `SELECT ${t.name}();` : `CALL ${t.name}();`) : onOpen({ kind: 'table', conn: c.id, schema: cur, table: t.name }))}
          title={t.rows >= 0 && key === 'tables' ? `≈${t.rows.toLocaleString()} rows` : t.type}
        >
          <Icon name={icon} />
          <span className="truncate">{t.name}</span>
          {key === 'tables' && t.rows >= 0 && <span className="db-it-n">{compactNum(t.rows)}</span>}
        </button>
      ));

  return (
    <div className="db-tree" role="tree" aria-label={`${c.name} schema`}>
      {(sc?.databases?.length ?? 0) > 1 && (
        <label className="db-side-select">
          <Icon name="database" />
          <select value={cur} onChange={(e) => setCur(e.target.value)} aria-label={pg ? 'Schema' : 'Database'}>
            {sc!.databases!.map((d) => (
              <option key={d}>{d}</option>
            ))}
          </select>
        </label>
      )}
      {!phone && (sc?.tables?.length ?? 0) > 0 && (
        <label className="db-filter-in">
          <Icon name="search" />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter tables" aria-label="Filter tables" />
        </label>
      )}
      {loading && !sc && (
        <div className="db-side-msg">
          <span className="spinner" /> Connecting…
        </div>
      )}
      {error && (
        <div className="db-side-msg is-err">
          <span>{error}</span>
          <Button size="sm" variant="ghost" icon="refresh" onClick={() => void load()}>
            Retry
          </Button>
        </div>
      )}
      {sc && (
        <>
          <Section id="tables" label="Tables" count={sc.tables?.length ?? 0} open={!shut.tables} onToggle={() => toggle('tables')}>
            {items('tables', 'table')}
            {(sc.tables?.length ?? 0) === 0 && <div className="db-side-msg">No table in {sc.current || 'this database'}.</div>}
          </Section>
          {(sc.views?.length ?? 0) > 0 && (
            <Section id="views" label="Views" count={sc.views!.length} open={!shut.views} onToggle={() => toggle('views')}>
              {items('views', 'eye')}
            </Section>
          )}
          {(sc.routines?.length ?? 0) > 0 && (
            <Section id="routines" label={pg ? 'Functions' : 'Routines'} count={sc.routines!.length} open={!shut.routines} onToggle={() => toggle('routines')}>
              {items('routines', 'braces')}
            </Section>
          )}
        </>
      )}
      {dialog}
    </div>
  );
}

function KvTree({ c, active, onOpen }: { c: Connection; active?: DbTarget; onOpen: DbSideProps['onOpen'] }) {
  const [info, setInfo] = useState<KVInfo | null>(null);
  const [error, setError] = useState('');
  const [shut, toggle] = useShut('nb.db.side.shut', {});
  const [run, dialog] = useConfirmRunner(c);
  useEffect(() => {
    let live = true;
    void run(() => dbApi.schema(c.id))
      .then((r) => live && setInfo(r?.kv ?? null))
      .catch((e) => live && setError(errorMessage(e)));
    return () => {
      live = false;
    };
  }, [c.id, run]);
  const dbs = info ? Object.entries(info.dbs).sort((a, b) => Number(a[0]) - Number(b[0])) : [];
  return (
    <div className="db-tree" aria-label={`${c.name} server`}>
      {error && <div className="db-side-msg is-err">{error}</div>}
      {!info && !error && (
        <div className="db-side-msg">
          <span className="spinner" /> Connecting…
        </div>
      )}
      {info && (
        <>
          <Section id="kvdbs" label="Databases" count={`${dbs.length} with keys`} open={!shut.kvdbs} onToggle={() => toggle('kvdbs')}>
            {dbs.map(([n, keys]) => (
              <button key={n} type="button" className={`db-it ${active?.kind === 'keys' && active.conn === c.id && (active.db ?? c.redisDb ?? 0) === Number(n) ? 'is-on' : ''}`} onClick={() => onOpen({ kind: 'keys', conn: c.id, db: Number(n) })}>
                <Icon name="database" />
                <span>db{n}</span>
                <span className="db-it-n">{keys.toLocaleString()} keys</span>
              </button>
            ))}
          </Section>
          <Section id="kvserver" label="Server" open={!shut.kvserver} onToggle={() => toggle('kvserver')}>
            <div className="db-it is-static">
              <Icon name="chartBar" />
              <span>Memory</span>
              <span className="db-it-n">{info.memory}</span>
            </div>
            <div className="db-it is-static">
              <Icon name="server" />
              <span>Clients</span>
              <span className="db-it-n">{info.clients}</span>
            </div>
            <div className="db-it is-static">
              <Icon name="clock" />
              <span>Slow log</span>
              <span className="db-it-n">{info.slowLog}</span>
            </div>
          </Section>
        </>
      )}
      {dialog}
    </div>
  );
}

function SavedQueries({ projectId, conns, active, onOpen, phone }: { projectId: string; conns: Connection[]; active?: DbTarget; onOpen: DbSideProps['onOpen']; phone?: boolean }) {
  const queries = useQueries(projectId);
  const [shut, toggle] = useShut('nb.db.side.shut', {});
  const sqlConns = useMemo(() => conns.filter((c) => !isKV(c.driver)), [conns]);
  if (queries.length === 0) return null;
  const body = queries.map((q) => {
    const c = conns.find((x) => x.id === q.connectionId) ?? sqlConns[0];
    const on = active?.kind === 'query' && active.id === q.id;
    return (
      <div key={q.id} className="db-it-row">
        <button type="button" className={`db-it ${on ? 'is-on' : ''}`} disabled={!c} onClick={() => c && onOpen({ kind: 'query', conn: c.id, id: q.id })} title={q.sql.slice(0, 400)}>
          <Icon name="file" />
          <span className="truncate">{q.name}.sql</span>
          {phone && c && <span className="db-it-n">{c.name}</span>}
        </button>
        {!phone && (
          <button type="button" className="db-it-x" aria-label={`Delete ${q.name}`} onClick={() => void dbApi.deleteQuery(projectId, q.id).catch((e) => toast.error(errorMessage(e)))}>
            <Icon name="trash" />
          </button>
        )}
      </div>
    );
  });
  if (phone)
    return (
      <>
        <div className="db-cap">Saved queries</div>
        <div className="db-conns">{body}</div>
      </>
    );
  return (
    <Section id="saved" label="Saved queries" count={queries.length} open={!shut.saved} onToggle={() => toggle('saved')}>
      {body}
    </Section>
  );
}

function History({ c, onOpen }: { c: Connection; onOpen: (sql: string) => void }) {
  const [shut, toggle] = useShut('nb.db.side.shut', { history: true });
  const [items, setItems] = useState<HistoryItem[] | null>(null);
  const rev = useSchemaRev(c.id);
  useEffect(() => {
    if (shut.history) return;
    let live = true;
    void dbApi
      .history(c.id, 40)
      .then((r) => live && setItems(r.items))
      .catch(() => live && setItems([]));
    return () => {
      live = false;
    };
  }, [c.id, shut.history, rev]);
  const today = new Date().toDateString();
  const day = items?.[0] ? (new Date(items[0].at).toDateString() === today ? 'Today' : '') : '';
  return (
    <Section id="history" label="History" open={!shut.history} onToggle={() => toggle('history')} extra={day ? <span className="db-sec-n">{day}</span> : undefined}>
      {items?.length === 0 && <div className="db-side-msg">Nothing ran yet.</div>}
      {items?.map((h, i) => (
        <button key={i} type="button" className={`db-it db-hist ${h.error ? 'is-err' : ''}`} onClick={() => onOpen(h.sql)} title={`${h.ms} ms${h.who ? ' · ' + h.who : ''}\n${h.sql.slice(0, 600)}`}>
          <Icon name="history" />
          <span className="mono truncate">{h.sql.replace(/\s+/g, ' ')}</span>
          <span className="db-it-n">{new Date(h.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })}</span>
        </button>
      ))}
    </Section>
  );
}
