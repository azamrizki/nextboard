// A Redis or Valkey connection (docs/API-db.md): keys by pattern (SCAN), grouped by their ":"
// prefix; a key's TTL, size and value, editable; and a console as redis-cli.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { dbApi } from './api';
import { useConfirmRunner } from './Confirm';
import { cellText } from './DataGrid';
import type { Connection, KeyItem, KeyValue, KVInfo } from './types';

const TYPES = ['string', 'hash', 'list', 'set', 'zset', 'stream'];
const SHORT: Record<string, string> = { string: 'STR', hash: 'HASH', list: 'LIST', set: 'SET', zset: 'ZSET', stream: 'STREAM' };

export function ttlText(ms: number): string {
  if (ms < 0) return '∞';
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d`;
}

function bytes(n: number) {
  if (n <= 0) return '';
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}

function KeyType({ t }: { t: string }) {
  return <span className={`db-ktype db-ktype-${t}`}>{SHORT[t] ?? t.toUpperCase()}</span>;
}

export function RedisView({ conn, compact, initialMatch }: { conn: Connection; compact?: boolean; initialMatch?: string }) {
  const [info, setInfo] = useState<KVInfo | null>(null);
  const [db, setDb] = useState(conn.redisDb ?? 0);
  const [match, setMatch] = useState(initialMatch ?? '*');
  const [typ, setTyp] = useState('');
  const [keys, setKeys] = useState<KeyItem[]>([]);
  const [cursor, setCursor] = useState(0);
  const [total, setTotal] = useState(0);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [sel, setSel] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    void dbApi
      .schema(conn.id)
      .then((r) => setInfo(r.kv ?? null))
      .catch((e) => setError(errorMessage(e)));
  }, [conn.id]);

  const load = useCallback(
    async (more: boolean) => {
      setLoading(true);
      setError('');
      try {
        const p = await dbApi.keys(conn.id, { db, cursor: more ? cursor : 0, match: match || '*', type: typ, count: 500 });
        setKeys((k) => (more ? [...k, ...p.keys] : p.keys));
        setCursor(p.cursor);
        setTotal(p.total);
      } catch (e) {
        setError(errorMessage(e));
      } finally {
        setLoading(false);
      }
    },
    [conn.id, db, cursor, match, typ],
  );

  useEffect(() => {
    void load(false);
    // a new search, not a page: the cursor starts again
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn.id, db, typ]);

  // keys grouped by the part before their first ":"
  const groups = useMemo(() => {
    const g = new Map<string, KeyItem[]>();
    const loose: KeyItem[] = [];
    for (const k of keys) {
      const i = k.name.indexOf(':');
      if (i > 0) {
        const p = k.name.slice(0, i + 1);
        g.set(p, [...(g.get(p) ?? []), k]);
      } else loose.push(k);
    }
    return { g: [...g.entries()].sort((a, b) => a[0].localeCompare(b[0])), loose };
  }, [keys]);

  const dbs = info ? Object.keys(info.dbs).map(Number).sort((a, b) => a - b) : [db];
  if (!dbs.includes(db)) dbs.unshift(db);

  return (
    <div className={`db-redis ${compact ? 'is-compact' : ''}`}>
      <div className="db-redis-keys">
        <form className="db-keysearch" onSubmit={(e) => (e.preventDefault(), void load(false))}>
          <Icon name="search" />
          <input className="mono" value={match} onChange={(e) => setMatch(e.target.value)} aria-label="Key pattern" placeholder="session:*" />
          {!compact && <span className="text-3 fs-xs">SCAN, 500 at a time</span>}
        </form>
        <div className="db-chips">
          <select value={db} onChange={(e) => setDb(Number(e.target.value))} aria-label="Database" className="db-dbsel">
            {dbs.map((n) => (
              <option key={n} value={n}>
                db{n}
                {info?.dbs[n] !== undefined ? ` · ${info.dbs[n].toLocaleString()}` : ''}
              </option>
            ))}
          </select>
          <button type="button" className={`db-chip ${typ === '' ? 'is-on' : ''}`} onClick={() => setTyp('')}>
            All
          </button>
          {TYPES.map((t) => (
            <button key={t} type="button" className={`db-chip ${typ === t ? 'is-on' : ''}`} onClick={() => setTyp(t)}>
              {t === 'zset' ? 'ZSet' : t[0].toUpperCase() + t.slice(1)}
            </button>
          ))}
        </div>
        <div className="db-keytree" role="tree" aria-label="Keys">
          {error && <div className="db-error">{error}</div>}
          {groups.g.map(([prefix, list]) => (
            <div key={prefix} role="treeitem" aria-expanded={!!open[prefix]}>
              <button type="button" className="db-key-group" onClick={() => setOpen({ ...open, [prefix]: !open[prefix] })}>
                <Icon name={open[prefix] ? 'chevronDown' : 'chevronRight'} />
                <b>{prefix}</b>
                <span className="db-n tabular">{list.length.toLocaleString()}</span>
              </button>
              {open[prefix] &&
                list.map((k) => (
                  <button key={k.name} type="button" className={`db-key ${sel === k.name ? 'is-sel' : ''}`} onClick={() => setSel(k.name)}>
                    <KeyType t={k.type} />
                    <span className="mono truncate">{k.name.slice(prefix.length)}</span>
                    <span className="db-n">{ttlText(k.ttl)}</span>
                  </button>
                ))}
            </div>
          ))}
          {groups.loose.map((k) => (
            <button key={k.name} type="button" className={`db-key is-loose ${sel === k.name ? 'is-sel' : ''}`} onClick={() => setSel(k.name)}>
              <KeyType t={k.type} />
              <span className="mono truncate">{k.name}</span>
              <span className="db-n">{ttlText(k.ttl)}</span>
            </button>
          ))}
          {!loading && keys.length === 0 && !error && <div className="db-empty text-3">No key matches.</div>}
        </div>
        <div className="db-foot">
          <span className="text-3 tabular fs-xs">
            {keys.length.toLocaleString()} of {total.toLocaleString()} keys
          </span>
          <span className="db-spacer" />
          {cursor !== 0 && (
            <Button size="sm" variant="ghost" loading={loading} onClick={() => void load(true)}>
              Load more
            </Button>
          )}
        </div>
      </div>
      <div className="db-redis-main">
        {sel ? <KeyDetail conn={conn} db={db} name={sel} onGone={() => (setSel(''), void load(false))} /> : <div className="db-empty text-3">Choose a key.</div>}
        <Console conn={conn} db={db} onChanged={() => void load(false)} />
      </div>
    </div>
  );
}

function KeyDetail({ conn, db, name, onGone }: { conn: Connection; db: number; name: string; onGone: () => void }) {
  const [kv, setKv] = useState<KeyValue | null>(null);
  const [error, setError] = useState('');
  const [edit, setEdit] = useState<{ op: string; field?: string; value?: string; title: string; score?: string } | null>(null);
  const [run, confirmDialog] = useConfirmRunner(conn);
  const ro = conn.security.readOnly;

  const load = useCallback(async () => {
    setError('');
    try {
      setKv(await dbApi.key(conn.id, db, name));
    } catch (e) {
      setError(errorMessage(e));
      setKv(null);
    }
  }, [conn.id, db, name]);

  useEffect(() => {
    void load();
  }, [load]);

  const write = async (body: Record<string, unknown>, after?: () => void) => {
    try {
      const r = await run((a) => dbApi.writeKey(conn.id, { db, key: name, confirmed: a.confirmed, ...body }));
      if (r === undefined) return;
      setEdit(null);
      if (after) after();
      else void load();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  const more = async () => {
    if (!kv?.next) return;
    try {
      const n = await dbApi.key(conn.id, db, name, kv.next);
      setKv({ ...n, items: [...(kv.items ?? []), ...(n.items ?? [])], entries: [...(kv.entries ?? []), ...(n.entries ?? [])] });
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  if (error) return <div className="db-error">{error}</div>;
  if (!kv) return <div className="db-empty text-3">Loading…</div>;
  const json = kv.type === 'string' ? tryJSON(cellText(kv.value ?? null)) : null;

  return (
    <div className="db-keydetail">
      <div className="db-keyhead">
        <KeyType t={kv.type} />
        <b className="mono db-keyname">{kv.name}</b>
        <span className="db-spacer" />
        <Button size="sm" variant="ghost" icon="pencil" disabled={ro} onClick={() => setEdit({ op: 'rename', value: kv.name, title: 'Rename the key' })}>
          Rename
        </Button>
        <Button size="sm" variant="ghost" icon="clock" disabled={ro} onClick={() => setEdit({ op: 'expire', value: kv.ttl > 0 ? String(Math.round(kv.ttl / 1000)) : '', title: 'Time to live (seconds)' })}>
          TTL
        </Button>
        <Button size="sm" variant="danger-ghost" icon="trash" disabled={ro} onClick={() => void write({ op: 'del' }, onGone)}>
          Delete
        </Button>
      </div>
      <dl className="db-kv">
        <dt>TTL</dt>
        <dd>
          {kv.ttl < 0 ? 'none' : ttlText(kv.ttl)}
          {kv.ttl > 0 && !ro && (
            <button type="button" className="db-link-btn" onClick={() => void write({ op: 'persist' })}>
              keep forever
            </button>
          )}
        </dd>
        <dt>Size</dt>
        <dd>
          {[bytes(kv.size), kv.length ? `${kv.length.toLocaleString()} ${kv.type === 'string' ? 'bytes' : kv.type === 'hash' ? 'fields' : kv.type === 'stream' ? 'entries' : 'items'}` : '', kv.encoding].filter(Boolean).join(' · ')}
        </dd>
      </dl>
      {kv.type === 'string' && (
        <div className="db-strval">
          <pre className="db-json">{json ? JSON.stringify(json, null, 2) : cellText(kv.value ?? null)}</pre>
          {!ro && (
            <Button size="sm" icon="pencil" onClick={() => setEdit({ op: 'set', value: cellText(kv.value ?? null), title: 'Value' })}>
              Edit
            </Button>
          )}
        </div>
      )}
      {(kv.type === 'hash' || kv.type === 'list' || kv.type === 'set' || kv.type === 'zset') && (
        <table className="db-plain db-items">
          <thead>
            <tr>
              <th>#</th>
              <th>{kv.type === 'hash' ? 'field' : kv.type === 'zset' ? 'member' : 'value'}</th>
              {(kv.type === 'hash' || kv.type === 'zset') && <th>{kv.type === 'hash' ? 'value' : 'score'}</th>}
              {!ro && <th />}
            </tr>
          </thead>
          <tbody>
            {(kv.items ?? []).map((it, i) => (
              <tr key={i}>
                <td className="text-3 tabular">{i + 1}</td>
                <td className="mono">{cellText(it[0])}</td>
                {(kv.type === 'hash' || kv.type === 'zset') && <td className="mono">{cellText(it[1])}</td>}
                {!ro && (
                  <td className="db-row-acts">
                    {kv.type === 'hash' && <Button size="sm" variant="ghost" iconOnly icon="pencil" aria-label="Edit the field" onClick={() => setEdit({ op: 'hset', field: cellText(it[0]), value: cellText(it[1]), title: `Field ${cellText(it[0])}` })} />}
                    {kv.type === 'list' && <Button size="sm" variant="ghost" iconOnly icon="pencil" aria-label="Edit the item" onClick={() => setEdit({ op: 'lset', field: String(i), value: cellText(it[0]), title: `Item ${i + 1}` })} />}
                    <Button
                      size="sm"
                      variant="ghost"
                      iconOnly
                      icon="trash"
                      aria-label="Remove"
                      onClick={() => void write(kv.type === 'hash' ? { op: 'hdel', field: cellText(it[0]) } : { op: { list: 'lrem', set: 'srem', zset: 'zrem' }[kv.type]!, value: cellText(it[0]) })}
                    />
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {kv.type === 'stream' && (
        <table className="db-plain db-items">
          <thead>
            <tr>
              <th>id</th>
              <th>fields</th>
            </tr>
          </thead>
          <tbody>
            {(kv.entries ?? []).map((en) => (
              <tr key={en.id}>
                <td className="mono">{en.id}</td>
                <td className="mono">{Object.entries(en.fields).map(([k, v]) => `${k}=${cellText(v)}`).join('  ')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="db-keyfoot">
        {kv.next && (
          <Button size="sm" variant="ghost" onClick={() => void more()}>
            More
          </Button>
        )}
        {!ro && kv.type !== 'string' && (
          <Button
            size="sm"
            icon="plus"
            onClick={() =>
              setEdit({ op: { hash: 'hset', list: 'rpush', set: 'sadd', zset: 'zadd', stream: 'xadd' }[kv.type] ?? 'sadd', field: '', value: '', score: '0', title: 'Add' })
            }
          >
            Add
          </Button>
        )}
      </div>
      {edit && (
        <Modal title={edit.title} icon="pencil" size="sm" onClose={() => setEdit(null)}
          footer={<><Button variant="ghost" onClick={() => setEdit(null)}>Cancel</Button><Button variant="primary" onClick={() => void submitEdit(edit, write, onGone)}>Save</Button></>}>
          {(edit.op === 'hset' || edit.op === 'xadd') && (
            <input className="control db-input mono" value={edit.field} onChange={(e) => setEdit({ ...edit, field: e.target.value })} placeholder="field" aria-label="Field" disabled={edit.op === 'hset' && edit.title !== 'Add'} />
          )}
          {edit.op === 'zadd' && <input className="control db-input mono" value={edit.score} onChange={(e) => setEdit({ ...edit, score: e.target.value })} placeholder="score" aria-label="Score" />}
          {edit.op === 'set' ? (
            <textarea className="control db-input mono db-textarea" value={edit.value} onChange={(e) => setEdit({ ...edit, value: e.target.value })} aria-label="Value" />
          ) : (
            <input className="control db-input mono" autoFocus value={edit.value} onChange={(e) => setEdit({ ...edit, value: e.target.value })} aria-label="Value" />
          )}
        </Modal>
      )}
      {confirmDialog}
    </div>
  );
}

function submitEdit(edit: { op: string; field?: string; value?: string; score?: string }, write: (b: Record<string, unknown>, after?: () => void) => Promise<void>, onGone: () => void) {
  switch (edit.op) {
    case 'rename':
      return write({ op: 'rename', to: edit.value }, onGone);
    case 'expire':
      return Number(edit.value) > 0 ? write({ op: 'expire', ttl: Number(edit.value) }) : write({ op: 'persist' });
    case 'lset':
      return write({ op: 'lset', index: Number(edit.field), value: edit.value });
    case 'zadd':
      return write({ op: 'zadd', value: edit.value, score: Number(edit.score) || 0 });
    default:
      return write({ op: edit.op, field: edit.field, value: edit.value });
  }
}

function tryJSON(s: string): unknown {
  const t = s.trim();
  if (!(t.startsWith('{') || t.startsWith('['))) return null;
  try {
    return JSON.parse(t);
  } catch {
    return null;
  }
}

function Console({ conn, db, onChanged }: { conn: Connection; db: number; onChanged: () => void }) {
  const [lines, setLines] = useState<{ cmd: string; out: string; err?: boolean }[]>([]);
  const [line, setLine] = useState('');
  const [hist, setHist] = useState<string[]>([]);
  const [hi, setHi] = useState(-1);
  const [run, confirmDialog] = useConfirmRunner(conn);
  const end = useRef<HTMLDivElement>(null);

  useEffect(() => {
    end.current?.scrollIntoView({ block: 'end' });
  }, [lines]);

  const send = async () => {
    const cmd = line.trim();
    if (!cmd) return;
    setLine('');
    setHist([cmd, ...hist.filter((h) => h !== cmd)].slice(0, 50));
    setHi(-1);
    try {
      const r = await run((a) => dbApi.command(conn.id, { db, line: cmd, confirmed: a.confirmed, confirmName: a.confirmName }));
      if (r === undefined) return;
      setLines((l) => [...l, { cmd, out: r.output }].slice(-200));
      if (!/^(get|hget|hgetall|scan|type|ttl|pttl|info|ping|lrange|smembers|zrange|xrange|exists|strlen|llen|scard|zcard|hlen|dbsize)\b/i.test(cmd)) onChanged();
    } catch (e) {
      setLines((l) => [...l, { cmd, out: errorMessage(e), err: true }].slice(-200));
    }
  };

  return (
    <div className="db-console">
      <div className="db-console-out">
        {lines.map((l, i) => (
          <div key={i}>
            <div>
              <span className="db-prompt">{conn.name}&gt;</span> {l.cmd}
            </div>
            <div className={l.err ? 'text-danger' : 'text-2'} style={{ whiteSpace: 'pre-wrap' }}>
              {l.out}
            </div>
          </div>
        ))}
        <div ref={end} />
      </div>
      <form className="db-console-in" onSubmit={(e) => (e.preventDefault(), void send())}>
        <span className="db-prompt">{conn.name}&gt;</span>
        <input
          className="mono"
          value={line}
          onChange={(e) => setLine(e.target.value)}
          aria-label="Redis command"
          placeholder="HGETALL session:…"
          onKeyDown={(e) => {
            if (e.key === 'ArrowUp' && hist.length) {
              const n = Math.min(hist.length - 1, hi + 1);
              setHi(n);
              setLine(hist[n]);
              e.preventDefault();
            } else if (e.key === 'ArrowDown') {
              const n = hi - 1;
              setHi(Math.max(-1, n));
              setLine(n >= 0 ? hist[n] : '');
              e.preventDefault();
            }
          }}
        />
      </form>
      {confirmDialog}
    </div>
  );
}
