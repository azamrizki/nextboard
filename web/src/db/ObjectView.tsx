// One database object at full size (docs/API-db.md): a table, an SQL editor or a Redis
// connection's keys, as the Code view's tab and the board card show it, with SmartDB beside it
// (mockup 8) when asked. It tells the Code view's status bar which connection it is on.

import { useEffect, useRef, useState } from 'react';
import { Icon } from '../components/Icon';
import { RedisView } from './RedisView';
import { connWhere, useMachineName } from './where';
import { SmartPanel } from './SmartPanel';
import { SqlEditor } from './SqlEditor';
import { clearDbStatus, setDbStatus } from './status';
import { useConnectionById, useQueries, useStatus } from './store';
import { TableView } from './TableView';
import type { DbTarget } from './tabs';
import type { Connection } from './types';

const ENV_LABEL: Record<string, string> = { local: 'Local', development: 'Development', staging: 'Staging', production: 'Production' };

export function modeText(c: Connection): string {
  const what = c.security.readOnly ? 'read-only' : c.security.confirmWrites ? 'writes ask first' : 'read and write';
  return `${ENV_LABEL[c.env] ?? c.env} · ${what}`;
}

export interface ObjectViewProps {
  target: Exclude<DbTarget, { kind: 'home' }>;
  /** the tab (or card) it is in: its status line, its editor's text */
  tabKey: string;
  shown: boolean;
  /** open another object: a foreign key followed, SmartDB's SQL */
  onOpen: (t: DbTarget, sql?: string) => void;
  /** the title the tab should carry, once known */
  onTitle?: (title: string) => void;
  /** a board card: no SmartDB beside, a narrower toolbar */
  card?: boolean;
}

export function ObjectView({ target, tabKey, shown, onOpen, onTitle, card }: ObjectViewProps) {
  const conn = useConnectionById(target.conn);
  const st = useStatus(target.conn);
  const queries = useQueries(conn?.projectId);
  const machineName = useMachineName();
  const [smart, setSmart] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(1400);

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  // the tab's title
  const saved = target.kind === 'query' ? queries.find((q) => q.id === target.id) : undefined;
  const title = !conn ? '' : target.kind === 'keys' ? `${conn.name} · keys` : target.kind === 'query' ? (saved ? `${saved.name}.sql` : '') : '';
  useEffect(() => {
    if (title) onTitle?.(title);
  }, [title, onTitle]);

  // the status bar, while this one is in front
  useEffect(() => {
    if (!shown || !conn) return;
    const dialect = conn.driver === 'postgres' ? 'PostgreSQL' : conn.driver === 'mariadb' ? 'MariaDB' : 'MySQL';
    setDbStatus({
      tab: tabKey,
      dot: st?.error ? 'err' : st?.connected ? 'ok' : 'off',
      conn: conn.name,
      where: connWhere(conn, st?.version, machineName, true),
      mode: modeText(conn),
      right: target.kind === 'query' ? `SQL · ${dialect} dialect` : target.kind === 'keys' ? (conn.security.readOnly ? `${ENV_LABEL[conn.env]} · read-only` : 'Redis') : conn.security.readOnly ? 'Read-only' : 'Edits commit together',
    });
  }, [shown, conn, st, target.kind, tabKey, machineName]);
  useEffect(() => () => clearDbStatus(tabKey), [tabKey]);

  if (!conn)
    return (
      <div className="db-obj db-obj-gone" ref={box}>
        <div className="db-empty">
          <Icon name="database" />
          <p>This connection is gone, or still loading.</p>
        </div>
      </div>
    );

  const canSmart = conn.agents.access !== 'none' && !card;
  const smartOn = smart && canSmart;
  const overlay = width < 1000;
  const toggleSmart = canSmart ? () => setSmart((v) => !v) : undefined;

  return (
    <div className={`db-obj ${smartOn ? 'has-smart' : ''} ${card ? 'is-card' : ''}`} ref={box}>
      <div className="db-obj-main">
        {target.kind === 'table' && (
          <TableView
            key={`${target.schema}.${target.table}.${JSON.stringify(target.filter ?? null)}`}
            conn={conn}
            schema={target.schema}
            table={target.table}
            initialFilter={target.filter}
            compact={width < 640}
            card={card}
            onOpenTable={(schema, table, filter) => onOpen({ kind: 'table', conn: conn.id, schema, table, filter })}
            onSmart={toggleSmart}
            smartOn={smartOn}
          />
        )}
        {target.kind === 'query' && <SqlEditor conn={conn} tabId={target.id} queryId={saved?.id} compact={width < 640} onSmart={toggleSmart} smartOn={smartOn} />}
        {target.kind === 'keys' && <RedisView conn={conn} db={target.db} compact={width < 760} onSmart={toggleSmart} smartOn={smartOn} />}
      </div>
      {smartOn && (
        <div className={`db-smart-wrap ${overlay ? 'is-overlay' : ''}`}>
          <SmartPanel
            conn={conn}
            table={target.kind === 'table' ? target.table : undefined}
            schema={target.kind === 'table' ? target.schema : undefined}
            onOpenQuery={(sql) => onOpen({ kind: 'query', conn: conn.id, id: '' }, sql)}
            onClose={() => setSmart(false)}
          />
        </div>
      )}
    </div>
  );
}
