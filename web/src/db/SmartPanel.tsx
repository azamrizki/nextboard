// SmartDB's assistant (docs/API-db.md, SmartDB): the owner asks about a connection in any
// language; Claude looks with the hub's database tools, each of its queries shows here as it runs
// ("dbAgentCall"), then its answer ("dbChat").

import { useEffect, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { store } from '../store';
import { dbApi } from './api';
import { cellText } from './DataGrid';
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

function Answer({ text }: { text: string }) {
  const parts = text.split(/(```[\s\S]*?```)/g);
  return (
    <div className="db-answer">
      {parts.map((p, i) =>
        p.startsWith('```') ? (
          <pre key={i} className="db-sql-preview">
            {p.replace(/^```[a-z]*\n?/i, '').replace(/```$/, '')}
          </pre>
        ) : (
          <p key={i} style={{ whiteSpace: 'pre-wrap' }}>
            {p.trim()}
          </p>
        ),
      )}
    </div>
  );
}

export function SmartPanel({ conn, table, schema, onOpenQuery, onClose }: { conn: Connection; table?: string; schema?: string; onOpenQuery?: (sql: string) => void; onClose?: () => void }) {
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [text, setText] = useState('');
  const end = useRef<HTMLDivElement>(null);
  const busy = msgs.some((m) => m.role === 'assistant' && !m.done);

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

  return (
    <aside className="db-smart" aria-label="SmartDB">
      <div className="db-smart-head">
        <Icon name="sparkle" />
        <b>SmartDB</b>
        <span className="text-3 fs-xs truncate">{conn.name}</span>
        <span className="db-spacer" />
        <span className="db-pill">
          <Icon name="shield" /> {conn.security.readOnly || conn.agents.access === 'read' ? 'read-only' : conn.agents.access === 'ask' ? 'asks to write' : 'may write'}
        </span>
        {onClose && <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close SmartDB" onClick={onClose} />}
      </div>
      <div className="db-smart-msgs">
        {msgs.length === 0 && (
          <div className="db-empty text-3">
            Ask about {conn.name} in your words: "why did revenue drop yesterday?", "which customers ordered most this month?". SmartDB reads the schema and runs read-only queries itself.
          </div>
        )}
        {msgs.map((m, i) =>
          m.role === 'user' ? (
            <div key={i} className="db-me">
              {m.text}
            </div>
          ) : (
            <div key={i} className="db-bot">
              {(m.calls ?? []).map((c, j) => (
                <div key={j} className="db-call">
                  <div className="db-call-head">
                    <Icon name="database" /> <b>query</b>
                    <span className="text-3">
                      · {c.ms} ms{c.columns ? ` · ${c.total ?? c.rows?.length ?? 0} rows` : c.affected ? ` · ${c.affected} changed` : ''}
                    </span>
                  </div>
                  <pre className="db-call-sql">{c.sql}</pre>
                  {c.error && <div className="text-danger fs-xs">{c.error}</div>}
                  {c.columns && c.rows && c.rows.length > 0 && (
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
                              <td key={n}>{cellText(v)}</td>
                            ))}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </div>
              ))}
              {!m.done && (
                <div className="db-thinking text-3">
                  <span className="spinner" /> {m.calls?.length ? 'looking further…' : 'reading the schema…'}
                </div>
              )}
              {m.error && <div className="db-error">{m.error}</div>}
              {m.text && <Answer text={m.text} />}
              {m.done && m.text && (
                <div className="db-chips">
                  {sqlBlocks(m.text).slice(0, 1).map((s) => (
                    <button key="open" type="button" className="db-chip" onClick={() => onOpenQuery?.(s)}>
                      <Icon name="fileCode" /> Open as query
                    </button>
                  ))}
                  <button type="button" className="db-chip" onClick={() => void navigator.clipboard?.writeText(m.text)}>
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
          placeholder={`Ask about ${conn.name}…`}
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
            <span className="text-3 fs-xs">
              <Icon name="table" /> {table} in view
            </span>
          )}
          <span className="db-spacer" />
          <Button size="sm" variant="primary" iconOnly icon="send" aria-label="Ask" type="submit" disabled={!text.trim() || busy} />
        </div>
      </form>
    </aside>
  );
}
