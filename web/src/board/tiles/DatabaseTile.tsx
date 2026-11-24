// Database card (docs/API-db.md; mockup 5): one connection of the project on the board, its tables
// on the left and the one open (or an SQL editor) beside them; a Redis connection's keys. Without
// a connection it lists the project's to pick one. What it shows is kept in the card's data, so
// every device sees the same, and a console wired to it may use the connection when that
// connection lets only wired consoles in (Agents › Which agents).

import { memo, useEffect, useState } from 'react';
import type { NodeProps } from '@xyflow/react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import type { MenuEntry } from '../../components/Menu';
import { dbApi } from '../../db/api';
import { DriverMark, EnvTag, useConfirmRunner } from '../../db/Confirm';
import { NewConnection } from '../../db/ConnectionDialog';
import { ObjectView } from '../../db/ObjectView';
import { compactNum } from '../../db/Side';
import { connWhere, useMachineName } from '../../db/where';
import { useConnections, useSchemaRev, useStatus } from '../../db/store';
import { dbArg, newQueryId, parseDbArg, type DbTarget } from '../../db/tabs';
import { isKV, type Connection, type Schema } from '../../db/types';
import type { FlowNode } from '../flow';
import { readDatabase } from '../kinds';
import { useBoardEnv } from '../ui';
import { stopKeys, TileFrame, TileHead } from './common';

function DatabaseTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, project } = useBoardEnv();
  const d = readDatabase(data.node);
  const { list, loaded } = useConnections(project.id);
  const conn = list.find((c) => c.id === d.connectionId);
  const st = useStatus(conn?.id);
  const target = parseDbArg(d.object);
  const shows: DbTarget | null = conn ? (target.kind !== 'home' && target.conn === conn.id ? target : isKV(conn.driver) ? { kind: 'keys', conn: conn.id } : null) : null;
  const show = (t: DbTarget) => ctl.updateData(id, { object: dbArg(t) });
  const pick = (c: Connection) => ctl.updateData(id, { connectionId: c.id, title: c.name, object: isKV(c.driver) ? dbArg({ kind: 'keys', conn: c.id }) : '' });

  const menu: MenuEntry[] = [
    ...(conn && !isKV(conn.driver) ? [{ label: 'New query', icon: 'file' as const, onSelect: () => show({ kind: 'query', conn: conn.id, id: newQueryId(1) }) }] : []),
    ...list.filter((c) => c.id !== conn?.id).map((c): MenuEntry => ({ label: `Show ${c.name}`, icon: 'database', onSelect: () => pick(c) })),
    ...(conn ? [{ label: 'Choose another connection', icon: 'database' as const, onSelect: () => ctl.updateData(id, { connectionId: '', object: '' }) }] : []),
  ];

  const objName = shows?.kind === 'table' ? shows.table : shows?.kind === 'query' ? 'SQL' : conn?.name;
  const meta =
    conn && (
      <span className="db-card-meta">
        {shows?.kind !== 'keys' && <span className="db-card-conn">· {conn.name}</span>}
        <EnvTag env={conn.env} />
        {conn.security.readOnly && (
          <span className="db-card-ro">
            <Icon name="lock" /> read-only
          </span>
        )}
        {st?.error && <span className="dot dot-danger" aria-label={st.error} title={st.error} />}
      </span>
    );

  return (
    <TileFrame id={id} kind="database" selected={!!selected} label={`Database ${conn?.name ?? d.title}`} minW={480} minH={320}>
      <TileHead id={id} icon={conn ? <DriverMark driver={conn.driver} /> : 'database'} title={conn ? (objName ?? conn.name) : d.title} meta={meta} menu={menu} menuLabel="Database card actions" />
      <div className="tile-db-body nodrag nowheel" onKeyDown={stopKeys}>
        {!conn ? (
          <ConnPicker projectId={project.id} list={list} loaded={loaded} gone={!!d.connectionId} onPick={pick} />
        ) : isKV(conn.driver) ? (
          <ObjectView target={shows ?? { kind: 'keys', conn: conn.id }} tabKey={`card:${id}`} shown={false} card onOpen={(t) => show(t)} />
        ) : (
          <div className="db-card">
            <CardTables conn={conn} active={shows} onOpen={show} />
            <div className="db-card-main">
              {shows ? (
                <ObjectView target={shows as Exclude<DbTarget, { kind: 'home' }>} tabKey={`card:${id}`} shown={false} card onOpen={(t) => show(t.kind === 'query' && !t.id ? { ...t, id: newQueryId(1) } : t)} />
              ) : (
                <div className="db-empty">
                  <Icon name="table" />
                  <p>Pick a table, or a new query.</p>
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </TileFrame>
  );
}

function ConnPicker({ projectId, list, loaded, gone, onPick }: { projectId: string; list: Connection[]; loaded: boolean; gone: boolean; onPick: (c: Connection) => void }) {
  const [creating, setCreating] = useState(false);
  const machineName = useMachineName();
  return (
    <div className="db-picker">
      <p className="db-picker-note">
        <Icon name="database" />
        {gone ? 'This card’s connection is gone: pick another.' : 'Database opens the project’s connections: pick one, then a table or a new query.'}
      </p>
      {loaded &&
        list.map((c) => (
          <button key={c.id} type="button" className="db-picker-it" onClick={() => onPick(c)}>
            <DriverMark driver={c.driver} size="lg" />
            <span className="db-conn-text">
              <b>{c.name}</b>
              <span className="db-conn-sub truncate">{connWhere(c, undefined, machineName)}</span>
            </span>
            <EnvTag env={c.env} />
          </button>
        ))}
      <button type="button" className="db-new-conn" onClick={() => setCreating(true)}>
        <Icon name="plus" /> New connection
      </button>
      {creating && <NewConnection projectId={projectId} onClose={() => setCreating(false)} onDone={onPick} />}
    </div>
  );
}

function CardTables({ conn, active, onOpen }: { conn: Connection; active: DbTarget | null; onOpen: (t: DbTarget) => void }) {
  const [sc, setSc] = useState<Schema | null>(null);
  const [error, setError] = useState('');
  const rev = useSchemaRev(conn.id);
  const [run, dialog] = useConfirmRunner(conn);
  useEffect(() => {
    let live = true;
    void run(() => dbApi.schema(conn.id))
      .then((r) => live && r?.schema && setSc(r.schema))
      .catch((e) => live && setError(errorMessage(e)));
    return () => {
      live = false;
    };
  }, [conn.id, rev, run]);
  const tables = sc?.tables ?? [];
  return (
    <nav className="db-card-side" aria-label={`${conn.name} tables`}>
      <div className="db-sec-h is-static">
        <span>Tables</span>
        <span className="db-sec-n">{tables.length}</span>
      </div>
      {error && <div className="db-side-msg is-err">{error}</div>}
      {!sc && !error && (
        <div className="db-side-msg">
          <span className="spinner" /> Connecting…
        </div>
      )}
      <div className="db-card-list">
        {tables.map((t) => (
          <button key={t.name} type="button" className={`db-it ${active?.kind === 'table' && active.table === t.name ? 'is-on' : ''}`} onClick={() => onOpen({ kind: 'table', conn: conn.id, schema: sc!.current, table: t.name })} title={t.rows >= 0 ? `≈${t.rows.toLocaleString()} rows` : undefined}>
            <Icon name="table" />
            <span className="truncate">{t.name}</span>
            {t.rows >= 0 && <span className="db-it-n">{compactNum(t.rows)}</span>}
          </button>
        ))}
        <button type="button" className={`db-it ${active?.kind === 'query' ? 'is-on' : ''}`} onClick={() => onOpen({ kind: 'query', conn: conn.id, id: active?.kind === 'query' ? active.id : newQueryId(1) })}>
          <Icon name="file" />
          <span>New query</span>
        </button>
      </div>
      <Button size="sm" variant="ghost" icon="refresh" className="db-card-refresh" onClick={() => void run(() => dbApi.schema(conn.id)).then((r) => r?.schema && setSc(r.schema))}>
        Refresh
      </Button>
      {dialog}
    </nav>
  );
}

export const DatabaseTile = memo(DatabaseTileImpl);
