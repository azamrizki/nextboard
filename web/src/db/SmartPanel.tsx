// SmartDB's assistant (docs/API-db.md, SmartDB; mockup 8): the owner asks about a connection in
// any language; Claude looks with the hub's database tools, each of its queries shows here as it
// runs ("dbAgentCall": its SQL, a chart of a series, its first rows), then its answer ("dbChat").

import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { store } from '../store';
import { dbApi } from './api';
import { cellText, isNumType, numText } from './DataGrid';
import { hlSQL } from './hl';
import type { Cell, Connection, ResultColumn } from './types';

interface Call {
  sql: string;
  ms: number;
  error?: string;
  columns?: ResultColumn[];
  rows?: Cell[][];
  total?: number;
  affected?: number;
}

interface Msg {
  role: 'user' | 'assistant';
  text: string;
  run?: string;
  calls?: Call[];
  error?: string;
  done?: boolean;
}

/** The SQL code blocks of an answer, to open as a query. */
export function sqlBlocks(text: string): string[] {
  const out: string[] = [];
  const re = /```(?:sql|mysql|postgres(?:ql)?)?\s*\n([\s\S]*?)```/gi;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) out.push(m[1].trim());
  return out;
}

/** **bold** and `code` in a line of the answer. */
function inline(text: string): ReactNode[] {
  return text.split(/(\*\*[^*]+\*\*|`[^`]+`)/g).map((p, i) =>
    p.startsWith('**') && p.endsWith('**') ? (
      <b key={i}>{p.slice(2, -2)}</b>
    ) : p.startsWith('`') && p.endsWith('`') ? (
      <code key={i}>{p.slice(1, -1)}</code>
    ) : (
      <Fragment key={i}>{p}</Fragment>
    ),
  );
}

function Answer({ text }: { text: string }) {
  const parts = text.split(/(```[\s\S]*?```)/g);
  return (
    <div className="db-answer">
      {parts.map((p, i) =>
        p.startsWith('```') ? (
          <pre key={i} className="db-answer-code">
            {hlSQL(p.replace(/^```[a-z]*\n?/i, '').replace(/```$/, '').trim())}
          </pre>
        ) : p.trim() ? (
          <p key={i}>{inline(p.trim())}</p>
        ) : null,
      )}
    </div>
  );
}

/** A series to draw: a column of days (or times) and a numeric one, 3 to 31 rows; categories stay a table. */
function series(c: Call): number[] | null {
  const cols = c.columns ?? [];
  const rows = c.rows ?? [];
  if (cols.length !== 2 || rows.length < 3 || rows.length > 31) return null;
  const timeLike = /date|time/i.test(cols[0].type ?? '') || rows.every((r) => /^\d{4}-\d{2}(-\d{2})?/.test(cellText(r[0])));
  if (!timeLike) return null;
  if (!isNumType(cols[1].type) && !rows.every((r) => !Number.isNaN(Number(cellText(r[1]))))) return null;
  return rows.map((r) => Number(cellText(r[1])) || 0);
}

function Bars({ values }: { values: number[] }) {
  const max = Math.max(1, ...values);
  const avg = values.reduce((a, b) => a + b, 0) / values.length;
  return (
    <div className="db-bars" aria-hidden="true">
      {values.map((v, i) => (
        <i key={i} className={v < avg * 0.6 ? 'is-low' : ''} style={{ height: `${Math.max(4, (v / max) * 100)}%` }} />
      ))}
    </div>
  );
}

export function SmartPanel({ conn, table, schema, onOpenQuery, onClose }: { conn: Connection; table?: string; schema?: string; onOpenQuery?: (sql: string) => void; onClose?: () => void }) {
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [text, setText] = useState('');
  const end = useRef<HTMLDivElement>(null);
  const busy = msgs.some((m) => m.role === 'assistant' && !m.done);
  const ro = conn.security.readOnly || conn.agents.access === 'read';

  useEffect(
    () =>
      store.onEvent((ev) => {
        if (ev.type === 'dbAgentCall' && ev.connectionId === conn.id) {
          setMsgs((ms) => ms.map((m) => (m.run === ev.run ? { ...m, calls: [...(m.calls ?? []), { sql: ev.sql, ms: ev.ms, error: ev.error, columns: ev.columns, rows: ev.rows, total: ev.total, affected: ev.affected }] } : m)));
        }
        if (ev.type === 'dbChat' && ev.connectionId === conn.id) {
          setMsgs((ms) => ms.map((m) => (m.run === ev.run ? { ...m, text: ev.text ?? '', error: ev.error, done: true } : m)));
        }
      }),
    [conn.id],
  );

  // a block, not an arrow's value: newer browsers' scrollIntoView returns a promise, which React
  // would call as the effect's cleanup ("l is not a function" stopped the tab)
  useEffect(() => {
    end.current?.scrollIntoView({ block: 'end' });
  }, [msgs]);

  const send = async () => {
    const q = text.trim();
    if (!q || busy) return;
    setText('');
    const history = msgs.filter((m) => m.done !== false).map((m) => ({ role: m.role, text: m.text }));
    setMsgs((ms) => [...ms, { role: 'user', text: q, done: true }]);
    try {
      const r = await dbApi.aiChat(conn.id, { message: q, history, table, schema });
      setMsgs((ms) => [...ms, { role: 'assistant', text: '', run: r.run, calls: [] }]);
    } catch (e) {
      setMsgs((ms) => [...ms, { role: 'assistant', text: '', error: errorMessage(e), done: true }]);
    }
  };

  const save = async (sql: string) => {
    try {
      const q = await dbApi.saveQuery(conn.projectId, { name: `SmartDB ${new Date().toLocaleDateString([], { month: 'short', day: 'numeric' })} ${new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })}`, sql, connectionId: conn.id });
      toast.success(`Saved as ${q.name}.sql`);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  return (
    <aside className="db-smart" aria-label="SmartDB">
      <div className="db-smart-head">
        <Icon name="sparkle" className="db-smart-i" />
        <b>SmartDB</b>
        <span className="db-smart-sub truncate">{conn.name} · Claude</span>
        <span className="db-spacer" />
        <span className="db-pill-o">
          <Icon name="shield" /> {ro ? 'read-only' : conn.agents.access === 'ask' ? 'asks to write' : 'may write'}
        </span>
        {onClose && <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close SmartDB" onClick={onClose} />}
      </div>
      <div className="db-smart-msgs">
        {msgs.length === 0 && (
          <div className="db-smart-empty">
            <Icon name="sparkle" />
            <p>
              Ask about <b>{conn.name}</b> in your words: why did revenue drop yesterday, which customers ordered most this month. SmartDB reads the schema and runs read-only queries itself.
            </p>
          </div>
        )}
        {msgs.map((m, i) =>
          m.role === 'user' ? (
            <div key={i} className="db-me">
              {m.text}
            </div>
          ) : (
            <div key={i} className="db-bot">
              {(m.calls ?? []).map((c, j) => {
                const s = series(c);
                return (
                  <div key={j} className="db-call">
                    <div className="db-call-head">
                      <Icon name="database" />
                      <b>db_query</b>
                      <span>
                        · {conn.name} · {c.ms} ms{c.columns ? ` · ${(c.total ?? c.rows?.length ?? 0).toLocaleString()} rows` : c.affected ? ` · ${c.affected} changed` : ''}
                      </span>
                    </div>
                    <pre className="db-call-sql">{hlSQL(c.sql)}</pre>
                    {c.error && <div className="db-call-err">{c.error}</div>}
                    {s && <Bars values={s} />}
                    {!s && c.columns && c.rows && c.rows.length > 0 && (
                      <table className="db-mini">
                        <thead>
                          <tr>
                            {c.columns.map((col) => (
                              <th key={col.name}>{col.name}</th>
                            ))}
                          </tr>
                        </thead>
                        <tbody>
                          {c.rows.slice(0, 6).map((r, k) => (
                            <tr key={k}>
                              {r.map((v, n) => (
                                <td key={n} className={typeof v === 'number' || isNumType(c.columns?.[n]?.type) ? 'is-num' : ''}>
                                  {typeof v === 'number' || isNumType(c.columns?.[n]?.type) ? numText(v) : cellText(v)}
                                </td>
                              ))}
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    )}
                  </div>
                );
              })}
              {!m.done && (
                <div className="db-thinking">
                  <span className="spinner" /> {m.calls?.length ? 'looking further…' : 'reading the schema…'}
                </div>
              )}
              {m.error && <div className="db-error">{m.error}</div>}
              {m.text && <Answer text={m.text} />}
              {m.done && m.text && (
                <div className="db-chips">
                  {sqlBlocks(m.text)
                    .slice(0, 1)
                    .map((s) => (
                      <Fragment key="sql">
                        <button type="button" className="db-chip" onClick={() => onOpenQuery?.(s)}>
                          <Icon name="file" /> Open as query
                        </button>
                        <button type="button" className="db-chip" onClick={() => void save(s)}>
                          <Icon name="star" /> Save
                        </button>
                      </Fragment>
                    ))}
                  <button type="button" className="db-chip" onClick={() => void navigator.clipboard?.writeText(m.text).then(() => toast.success('Copied'))}>
                    <Icon name="copy" /> Copy
                  </button>
                </div>
              )}
            </div>
          ),
        )}
        <div ref={end} />
      </div>
      <form className="db-smart-ask" onSubmit={(e) => (e.preventDefault(), void send())}>
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={`Ask about ${conn.name}… it reads the schema and runs read-only queries`}
          aria-label="Ask SmartDB"
          rows={2}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <div className="db-smart-ask-row">
          {table && (
            <span className="db-smart-ctx">
              <Icon name="table" /> {table} in view
            </span>
          )}
          <span className="db-spacer" />
          <span className="db-smart-model">Claude</span>
          <Button size="sm" variant="primary" iconOnly icon="send" aria-label="Ask" type="submit" disabled={!text.trim() || busy} />
        </div>
      </form>
    </aside>
  );
}
