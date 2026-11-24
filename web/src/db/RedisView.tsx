// A Redis or Valkey connection (docs/API-db.md; mockup 4): keys by pattern (SCAN), grouped by their
// ":" prefix, typed and with their TTL; a key's TTL, size and value, a field's JSON shown pretty,
// editable unless read-only; and a console as redis-cli.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { dbApi } from './api';
import { useConfirmRunner } from './Confirm';
import { cellText } from './DataGrid';
import type { Cell, Connection, KeyItem, KeyValue } from './types';

const TYPES = ['string', 'hash', 'list', 'set', 'zset', 'stream'];
const SHORT: Record<string, string> = { string: 'STR', hash: 'HASH', list: 'LIST', set: 'SET', zset: 'ZSET', stream: 'STREAM' };
const CHIP: Record<string, string> = { string: 'String', hash: 'Hash', list: 'List', set: 'Set', zset: 'ZSet', stream: 'Stream' };

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

/** A TTL in full, as a key's head shows it: "1h 12m 04s". */
function ttlLong(ms: number): string {
  if (ms < 0) return 'none';
  let s = Math.round(ms / 1000);
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  return [d && `${d}d`, (d || h) && `${h}h`, (d || h || m) && `${m}m`, `${String(s).padStart(d || h || m ? 2 : 1, '0')}s`].filter(Boolean).join(' ');
}

function bytes(n: number) {
  if (n <= 0) return '';
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}

export function KeyType({ t }: { t: string }) {
  return <span className={`db-ktype db-ktype-${t}`}>{SHORT[t] ?? t.toUpperCase()}</span>;
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

export interface RedisViewProps {
  conn: Connection;
  db?: number;
  compact?: boolean;
  initialMatch?: string;
  onSmart?: () => void;
  smartOn?: boolean;
}

export function RedisView({ conn, db: dbProp, compact, initialMatch, onSmart, smartOn }: RedisViewProps) {
  const db = dbProp ?? conn.redisDb ?? 0;
  const [match, setMatch] = useState(initialMatch ?? '*');
  const [typ, setTyp] = useState('');
  const [keys, setKeys] = useState<KeyItem[]>([]);
  const [cursor, setCursor] = useState(0);
  const [total, setTotal] = useState(0);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [sel, setSel] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const ro = conn.security.readOnly;

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

  return (
    <div className={`db-redis ${compact ? 'is-compact' : ''}`}>
      <div className="db-redis-keys">
        <form className="db-keysearch" onSubmit={(e) => (e.preventDefault(), void load(false))}>
          <Icon name="search" />
          <input value={match} onChange={(e) => setMatch(e.target.value)} aria-label="Key pattern" placeholder="session:*" />
          {!compact && <span className="db-keysearch-k">SCAN, 500 at a time</span>}
        </form>
        <div className="db-kchips" role="group" aria-label="Key types">
          <button type="button" className={typ === '' ? 'is-on' : ''} aria-pressed={typ === ''} onClick={() => setTyp('')}>
            All
          </button>
          {TYPES.map((t) => (
            <button key={t} type="button" className={typ === t ? 'is-on' : ''} aria-pressed={typ === t} onClick={() => setTyp(t)}>
              {CHIP[t]}
            </button>
          ))}
        </div>
        <div className="db-keytree" role="tree" aria-label="Keys">
          {error && <div className="db-error">{error}</div>}
          {groups.g.map(([prefix, list]) => (
            <div key={prefix} role="treeitem" aria-expanded={!!open[prefix]}>
              <button type="button" className="db-key-group" onClick={() => setOpen({ ...open, [prefix]: !open[prefix] })}>
                <Icon name={open[prefix] ? 'chevronDown' : 'chevronRight'} />
                <span>{prefix}</span>
                <span className="db-key-n">{list.length.toLocaleString()}</span>
              </button>
              {open[prefix] &&
                list.map((k) => (
                  <button key={k.name} type="button" className={`db-key ${sel === k.name ? 'is-on' : ''}`} onClick={() => setSel(k.name)}>
                    <KeyType t={k.type} />
                    <span className="truncate">{k.name.slice(prefix.length)}</span>
                    <span className="db-key-n">{ttlText(k.ttl)}</span>
                  </button>
                ))}
            </div>
          ))}
          {groups.loose.map((k) => (
            <button key={k.name} type="button" className={`db-key is-loose ${sel === k.name ? 'is-on' : ''}`} onClick={() => setSel(k.name)}>
              <KeyType t={k.type} />
              <span className="truncate">{k.name}</span>
              <span className="db-key-n">{ttlText(k.ttl)}</span>
            </button>
          ))}
          {!loading && keys.length === 0 && !error && <div className="db-empty">No key matches.</div>}
        </div>
        <div className="db-redis-foot">
          <span className="tabular">
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
        <div className="db-redis-top">
          {ro && (
            <span className="db-ro-badge">
              <Icon name="lock" /> {conn.env === 'production' ? 'Production · ' : ''}read-only
            </span>
          )}
          <span className="db-spacer" />
          {onSmart && (
            <Button size="sm" icon="sparkle" className={`db-smart-btn ${smartOn ? 'is-on' : ''}`} onClick={onSmart} aria-pressed={!!smartOn}>
              SmartDB
            </Button>
          )}
        </div>
        {sel ? <KeyDetail key={sel} conn={conn} db={db} name={sel} onGone={() => (setSel(''), void load(false))} /> : <div className="db-empty db-redis-pick">Choose a key.</div>}
        <Console conn={conn} db={db} onChanged={() => void load(false)} />
      </div>
    </div>
  );
}

function KeyDetail({ conn, db, name, onGone }: { conn: Connection; db: number; name: string; onGone: () => void }) {
  const [kv, setKv] = useState<KeyValue | null>(null);
  const [error, setError] = useState('');
  const [row, setRow] = useState<number | null>(null);
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
  if (!kv) return <div className="db-empty">Loading…</div>;
  const two = kv.type === 'hash' || kv.type === 'zset';
  const items = kv.items ?? [];
  // a value that is JSON reads pretty under the table (the selected row's, else the string's)
  const shown: Cell | undefined = kv.type === 'string' ? (kv.value ?? null) : row !== null ? items[row]?.[two && kv.type === 'hash' ? 1 : 0] : undefined;
  const json = shown !== undefined ? tryJSON(cellText(shown)) : null;
  const what = kv.type === 'string' ? 'bytes' : kv.type === 'hash' ? 'fields' : kv.type === 'stream' ? 'entries' : 'items';

  return (
    <div className="db-keydetail">
      <div className="db-keyhead">
        <KeyType t={kv.type} />
        <b className="db-keyname">{kv.name}</b>
        <span className="db-spacer" />
        {!ro && (
          <>
            <Button size="sm" icon="pencil" onClick={() => setEdit({ op: 'rename', value: kv.name, title: 'Rename the key' })}>
              Rename
            </Button>
            <Button size="sm" icon="clock" onClick={() => setEdit({ op: 'expire', value: kv.ttl > 0 ? String(Math.round(kv.ttl / 1000)) : '', title: 'Time to live (seconds)' })}>
              TTL
            </Button>
            <Button size="sm" variant="danger-ghost" icon="trash" className="db-del-btn" onClick={() => void write({ op: 'del' }, onGone)}>
              Delete
            </Button>
          </>
        )}
      </div>
      <dl className="db-kv">
        <dt>TTL</dt>
        <dd>
          {ttlLong(kv.ttl)}
          {kv.ttl > 0 && !ro && (
            <button type="button" className="db-link-btn" onClick={() => void write({ op: 'persist' })}>
              keep forever
            </button>
          )}
        </dd>
        <dt>Size</dt>
        <dd>{[bytes(kv.size), kv.length ? `${kv.length.toLocaleString()} ${what}` : '', kv.encoding].filter(Boolean).join(' · ')}</dd>
      </dl>
      {kv.type === 'string' && (
        <div className="db-strval">
          <pre className="db-json-box">{json ? JSON.stringify(json, null, 2) : cellText(kv.value ?? null)}</pre>
          {!ro && (
            <Button size="sm" icon="pencil" onClick={() => setEdit({ op: 'set', value: cellText(kv.value ?? null), title: 'Value' })}>
              Edit
            </Button>
          )}
        </div>
      )}
      {(two || kv.type === 'list' || kv.type === 'set') && (
        <table className="db-ktable">
          <thead>
            <tr>
              <th className="db-ktable-n" />
              <th>{kv.type === 'hash' ? 'field' : kv.type === 'zset' ? 'member' : 'value'}</th>
              {two && <th>{kv.type === 'hash' ? 'value' : 'score'}</th>}
              {!ro && <th className="db-ktable-acts" />}
            </tr>
          </thead>
          <tbody>
            {items.map((it, i) => (
              <tr key={i} className={row === i ? 'is-on' : ''} onClick={() => setRow(i)}>
                <td className="db-ktable-n">{i + 1}</td>
                <td className={two ? 'db-kfield' : ''}>{cellText(it[0])}</td>
                {two && <td>{cellText(it[1])}</td>}
                {!ro && (
                  <td className="db-ktable-acts">
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
        <table className="db-ktable">
          <thead>
            <tr>
              <th>id</th>
              <th>fields</th>
            </tr>
          </thead>
          <tbody>
            {(kv.entries ?? []).map((en) => (
              <tr key={en.id}>
                <td className="db-kfield">{en.id}</td>
                <td>{Object.entries(en.fields).map(([k, v]) => `${k}=${cellText(v)}`).join('  ')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {kv.type !== 'string' && json !== null && row !== null && (
        <div className="db-kjson">
          <span className="db-kjson-l">
            {cellText(items[row]?.[0] ?? '')} · JSON
          </span>
          <pre className="db-json-box">{JSON.stringify(json, null, 2)}</pre>
        </div>
      )}
      <div className="db-keyfoot">
        {kv.next && (
          <Button size="sm" variant="ghost" onClick={() => void more()}>
            More
          </Button>
        )}
        {!ro && kv.type !== 'string' && (
          <Button size="sm" icon="plus" onClick={() => setEdit({ op: { hash: 'hset', list: 'rpush', set: 'sadd', zset: 'zadd', stream: 'xadd' }[kv.type] ?? 'sadd', field: '', value: '', score: '0', title: 'Add' })}>
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

function Console({ conn, db, onChanged }: { conn: Connection; db: number; onChanged: () => void }) {
  const [lines, setLines] = useState<{ cmd: string; out: string; err?: boolean }[]>([]);
  const [line, setLine] = useState('');
  const [hist, setHist] = useState<string[]>([]);
  const [hi, setHi] = useState(-1);
  const [run, confirmDialog] = useConfirmRunner(conn);
  const end = useRef<HTMLDivElement>(null);

  // a block: scrollIntoView may return a promise, which React would take for a cleanup
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
      setLines((l) => [...l, { cmd, out: `Refused: ${errorMessage(e)}`, err: true }].slice(-200));
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
            <div className={l.err ? 'db-console-err' : 'db-console-res'}>{l.out}</div>
          </div>
        ))}
        <div ref={end} />
      </div>
      <form className="db-console-in" onSubmit={(e) => (e.preventDefault(), void send())}>
        <span className="db-prompt">{conn.name}&gt;</span>
        <input
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
