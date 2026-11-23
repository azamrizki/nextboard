// What the hub asks before a write runs (docs/API-db.md, Safety): a 409 "confirm" (the statements
// that change data), a 409 "confirm_name" (DROP, TRUNCATE, changes without WHERE: the connection's
// name typed), or "password_needed" for a connection that does not save its password. One hook
// runs an action, asks what the hub asks, and runs it again with the answer.

import { useCallback, useRef, useState, type ReactNode } from 'react';
import { ApiError } from '../api';
import { Button } from '../components/Button';
import { Modal } from '../components/Modal';
import { dbApi } from './api';
import type { Connection, Stmt } from './types';

export interface Answer {
  confirmed?: boolean;
  confirmName?: string;
}

interface Asking {
  code: 'confirm' | 'confirm_name' | 'password';
  stmts: Stmt[];
  rows: number[];
  go: (a: Answer) => void;
  stop: () => void;
}

/** run(action) calls action({}) and, when the hub asks, asks the owner and calls it again. */
export function useConfirmRunner(conn: Connection | undefined): [<T>(action: (a: Answer) => Promise<T>) => Promise<T | undefined>, ReactNode] {
  const [asking, setAsking] = useState<Asking | null>(null);
  const connRef = useRef(conn);
  connRef.current = conn;

  const run = useCallback(<T,>(action: (a: Answer) => Promise<T>): Promise<T | undefined> => {
    const attempt = async (a: Answer, depth: number): Promise<T | undefined> => {
      try {
        return await action(a);
      } catch (err) {
        if (!(err instanceof ApiError) || err.status !== 409 || depth > 3) throw err;
        const code = err.code === 'confirm' || err.code === 'confirm_name' ? err.code : err.code === 'password_needed' ? 'password' : '';
        if (!code) throw err;
        const answer = await new Promise<Answer | null>((resolve) => {
          setAsking({
            code: code as Asking['code'],
            stmts: (err.body.statements as Stmt[] | undefined) ?? [],
            rows: (err.body.rows as number[] | undefined) ?? [],
            go: (x) => {
              setAsking(null);
              resolve(x);
            },
            stop: () => {
              setAsking(null);
              resolve(null);
            },
          });
        });
        if (!answer) return undefined;
        return attempt({ ...a, ...answer }, depth + 1);
      }
    };
    return attempt({}, 0);
  }, []);

  const dialog = asking && conn ? <ConfirmDialogFor conn={conn} asking={asking} /> : null;
  return [run, dialog];
}

function ConfirmDialogFor({ conn, asking }: { conn: Connection; asking: Asking }) {
  const [typed, setTyped] = useState('');
  const [pw, setPw] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  if (asking.code === 'password') {
    const save = async () => {
      setBusy(true);
      try {
        await dbApi.password(conn.id, pw);
        asking.go({});
      } catch (e) {
        setErr(e instanceof Error ? e.message : String(e));
      } finally {
        setBusy(false);
      }
    };
    return (
      <Modal title={`Password of ${conn.name}`} icon="key" onClose={asking.stop} size="sm"
        footer={<><Button variant="ghost" onClick={asking.stop}>Cancel</Button><Button variant="primary" loading={busy} onClick={() => void save()}>Connect</Button></>}>
        <p className="text-2 fs-sm">This connection does not save its password. The hub keeps it in memory until it restarts.</p>
        <input className="control db-input" type="password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && void save()} aria-label="Password" />
        {err && <p className="text-danger fs-sm">{err}</p>}
      </Modal>
    );
  }

  if (asking.code === 'confirm') {
    return (
      <Modal title="Run what changes data?" icon="alert" onClose={asking.stop} size="md"
        footer={<><Button variant="ghost" onClick={asking.stop}>Cancel</Button><Button variant="primary" autoFocus onClick={() => asking.go({ confirmed: true })}>Run</Button></>}>
        <p className="text-2 fs-sm">
          <b>{conn.name}</b> asks before every change ({conn.env}). These run:
        </p>
        <pre className="db-sql-preview">{asking.stmts.map((s) => s.sql).join(';\n\n')}</pre>
      </Modal>
    );
  }

  const st = asking.stmts[0];
  const rows = asking.rows[0];
  const mysqlDDL = conn.driver !== 'postgres' && st?.kind === 'ddl';
  return (
    <Modal title={dangerTitle(st)} icon="alert" tone="danger" onClose={asking.stop} size="md"
      footer={
        <>
          <Button variant="ghost" onClick={asking.stop} autoFocus>
            Cancel
          </Button>
          <Button variant="danger" icon="play" disabled={typed !== conn.name} onClick={() => asking.go({ confirmName: typed, confirmed: true })}>
            {mysqlDDL ? 'Run it' : 'Run in a transaction'}
          </Button>
        </>
      }>
      <div className="db-conn-line">
        <DriverMark driver={conn.driver} />
        <b>{conn.name}</b>
        <span className="text-3">· {conn.database || conn.host}</span>
        <EnvTag env={conn.env} />
      </div>
      <pre className="db-sql-preview">{asking.stmts.map((s) => s.sql).join(';\n\n')}</pre>
      <div className="db-banner db-banner-danger">
        {st?.danger}
        {rows >= 0 ? ` (about ${rows.toLocaleString()} rows).` : '.'} DELETE, UPDATE without WHERE, DROP and TRUNCATE always ask like this, whatever the connection allows.
      </div>
      <div className="db-banner db-banner-info">
        {mysqlDDL
          ? 'MySQL commits DROP, TRUNCATE and ALTER at once: this cannot be rolled back.'
          : 'It runs inside a transaction: the rows it touched are counted first, and you commit or roll back after seeing them.'}
      </div>
      <label className="db-field">
        <span className="db-label">
          Type <b className="mono">{conn.name}</b> to run it
        </span>
        <input className="control db-input mono" value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={`Type ${conn.name} to run it`} />
      </label>
    </Modal>
  );
}

function dangerTitle(st: Stmt | undefined): string {
  if (!st) return 'Run this?';
  const t = st.table ? ` of ${st.table}` : '';
  switch (st.verb) {
    case 'DELETE':
      return `This deletes every row${t}`;
    case 'UPDATE':
      return `This changes every row${t}`;
    case 'TRUNCATE':
      return `This empties${st.table ? ' ' + st.table : ' the table'}`;
    case 'DROP':
      return 'This drops it, and all it holds';
    case 'FLUSHDB':
    case 'FLUSHALL':
      return 'This removes every key';
  }
  return st.danger ?? 'Run this?';
}

export function DriverMark({ driver, size }: { driver: string; size?: 'lg' }) {
  const mark = { mysql: 'My', mariadb: 'Ma', postgres: 'Pg', redis: 'Rd', valkey: 'Vk' }[driver] ?? 'Db';
  return (
    <span className={`db-drv db-drv-${driver} ${size === 'lg' ? 'db-drv-lg' : ''}`} aria-hidden="true">
      {mark}
    </span>
  );
}

export function EnvTag({ env }: { env: string }) {
  const label = { local: 'Local', development: 'Dev', staging: 'Staging', production: 'Production' }[env] ?? env;
  return <span className={`db-env db-env-${env}`}>{label}</span>;
}
