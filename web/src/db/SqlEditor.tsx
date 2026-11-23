// The SQL editor of a connection (docs/API-db.md): CodeMirror with the connection's dialect and
// completion from its schema; Run statement (Ctrl Enter, the one under the cursor), Run all,
// Explain, Stop; results per statement; a transaction the owner opened, or a dangerous statement
// waiting, shows Commit and Roll back. SmartDB writes, explains, fixes and optimises SQL.

import { autocompletion, completionKeymap } from '@codemirror/autocomplete';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { MySQL, PostgreSQL, sql as sqlLang, type SQLNamespace } from '@codemirror/lang-sql';
import { bracketMatching, syntaxHighlighting } from '@codemirror/language';
import { Compartment, EditorState, Prec } from '@codemirror/state';
import { drawSelection, EditorView, highlightActiveLine, keymap, lineNumbers, placeholder } from '@codemirror/view';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { highlight, theme } from '../studio/code/cm';
import { dbApi, downloadExport } from './api';
import { useConfirmRunner } from './Confirm';
import { cellText, DataGrid } from './DataGrid';
import { bumpSchema, useQueries } from './store';
import type { Connection, Result } from './types';

/** The statements of a script with their offsets (quotes, comments and $$ kept), for the one under the cursor. */
export function statementsOf(text: string, pg: boolean): { from: number; to: number; sql: string }[] {
  const out: { from: number; to: number; sql: string }[] = [];
  let start = 0;
  let i = 0;
  const n = text.length;
  const push = (end: number) => {
    const raw = text.slice(start, end);
    const t = raw.trim();
    if (t && !/^(--[^\n]*\n?|\s|\/\*[\s\S]*?\*\/)*$/.test(t)) {
      const lead = raw.length - raw.trimStart().length;
      out.push({ from: start + lead, to: start + lead + t.length, sql: t });
    }
  };
  while (i < n) {
    const c = text[i];
    if (c === "'" || c === '"' || (c === '`' && !pg)) {
      i++;
      while (i < n && text[i] !== c) {
        if (text[i] === '\\' && !pg) i++;
        i++;
      }
      i++;
    } else if (c === '-' && text[i + 1] === '-') {
      const j = text.indexOf('\n', i);
      i = j < 0 ? n : j + 1;
    } else if (c === '/' && text[i + 1] === '*') {
      const j = text.indexOf('*/', i + 2);
      i = j < 0 ? n : j + 2;
    } else if (c === '$' && pg) {
      const m = /^\$[A-Za-z_]*\$/.exec(text.slice(i));
      if (m) {
        const j = text.indexOf(m[0], i + m[0].length);
        i = j < 0 ? n : j + m[0].length;
      } else i++;
    } else if (c === ';') {
      push(i);
      i++;
      start = i;
    } else i++;
  }
  push(n);
  return out;
}

const KEY = (id: string) => `nb.db.q.${id}`;

function loadText(id: string): string {
  try {
    return localStorage.getItem(KEY(id)) ?? '';
  } catch {
    return '';
  }
}

function saveText(id: string, text: string) {
  try {
    localStorage.setItem(KEY(id), text);
  } catch {
    /* storage blocked: the text lives in this page */
  }
}

export interface SqlEditorProps {
  conn: Connection;
  /** the editor's own id: its session on the hub and its text in this browser */
  tabId: string;
  /** a saved query this editor edits */
  queryId?: string;
  initialSQL?: string;
  compact?: boolean;
}

type Busy = '' | 'run' | 'ai';

export function SqlEditor({ conn, tabId, queryId, initialSQL, compact }: SqlEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const langC = useRef(new Compartment());
  const pg = conn.driver === 'postgres';
  const [schemas, setSchemas] = useState<string[]>([]);
  const [schema, setSchema] = useState('');
  const [results, setResults] = useState<Result[]>([]);
  const [tab, setTab] = useState(0);
  const [mode, setMode] = useState<'table' | 'json' | 'chart'>('table');
  const [busy, setBusy] = useState<Busy>('');
  const [txOpen, setTxOpen] = useState(false);
  const [aiOpen, setAiOpen] = useState(false);
  const [aiPrompt, setAiPrompt] = useState('');
  const [aiText, setAiText] = useState<{ title: string; text: string; sql?: string } | null>(null);
  const [lastErr, setLastErr] = useState<{ sql: string; error: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [name, setName] = useState('');
  const queries = useQueries(conn.projectId);
  const saved = useMemo(() => queries.find((q) => q.id === queryId), [queries, queryId]);
  const [run, confirmDialog] = useConfirmRunner(conn);
  const runRef = useRef<(all: boolean) => void>(() => undefined);

  // the editor
  useEffect(() => {
    if (!host.current) return;
    const text = loadText(tabId) || saved?.sql || initialSQL || '';
    const v = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: text,
        extensions: [
          lineNumbers(),
          history(),
          drawSelection(),
          highlightActiveLine(),
          bracketMatching(),
          syntaxHighlighting(highlight),
          theme,
          autocompletion(),
          placeholder(pg ? 'SELECT * FROM public.orders LIMIT 100;' : 'SELECT * FROM orders LIMIT 100;'),
          langC.current.of(sqlLang({ dialect: pg ? PostgreSQL : MySQL, upperCaseKeywords: true })),
          Prec.highest(
            keymap.of([
              { key: 'Mod-Enter', run: () => (runRef.current(false), true) },
              { key: 'Mod-Shift-Enter', run: () => (runRef.current(true), true) },
            ]),
          ),
          keymap.of([...completionKeymap, ...historyKeymap, indentWithTab, ...defaultKeymap]),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) saveText(tabId, u.state.sliceDoc());
          }),
        ],
      }),
    });
    view.current = v;
    return () => {
      v.destroy();
      view.current = null;
    };
    // the editor is made once per tab
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabId]);

  // schema completion: tables, then their columns (the first 40)
  useEffect(() => {
    let live = true;
    void dbApi
      .schema(conn.id, schema)
      .then(async (r) => {
        if (!live || !r.schema) return;
        const sc = r.schema;
        setSchemas(sc.databases ?? []);
        if (!schema) setSchema(sc.current);
        const ns: Record<string, string[]> = {};
        for (const t of [...(sc.tables ?? []), ...(sc.views ?? [])]) ns[t.name] = [];
        const apply = () => {
          view.current?.dispatch({ effects: langC.current.reconfigure(sqlLang({ dialect: pg ? PostgreSQL : MySQL, upperCaseKeywords: true, schema: ns as SQLNamespace, defaultSchema: sc.current })) });
        };
        apply();
        const names = (sc.tables ?? []).slice(0, 40).map((t) => t.name);
        for (let i = 0; i < names.length; i += 6) {
          await Promise.all(
            names.slice(i, i + 6).map((n) =>
              dbApi
                .table(conn.id, sc.current, n)
                .then((t) => {
                  ns[n] = t.columns.map((c) => c.name);
                })
                .catch(() => undefined),
            ),
          );
          if (!live) return;
        }
        apply();
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [conn.id, schema, pg]);

  const statementAtCursor = (): string => {
    const v = view.current;
    if (!v) return '';
    const sel = v.state.selection.main;
    const text = v.state.sliceDoc();
    if (!sel.empty) return text.slice(sel.from, sel.to);
    const list = statementsOf(text, pg);
    return (list.find((s) => sel.head >= s.from && sel.head <= s.to + 1) ?? list.find((s) => s.from >= sel.head) ?? list[list.length - 1])?.sql ?? '';
  };

  const execute = useCallback(
    async (sql: string) => {
      if (!sql.trim()) return;
      setBusy('run');
      setLastErr(null);
      try {
        const r = await run((a) => dbApi.run(conn.id, { session: tabId, sql, schema, confirmed: a.confirmed, confirmName: a.confirmName }));
        if (!r) return;
        setResults(r.results);
        setTab(Math.max(0, r.results.findIndex((x) => x.columns?.length) >= 0 ? r.results.findIndex((x) => x.columns?.length) : r.results.length - 1));
        const bad = r.results.find((x) => x.error);
        if (bad) setLastErr({ sql: bad.sql, error: bad.error! });
        const last = r.results[r.results.length - 1];
        if (last?.notice?.startsWith('transaction open') || last?.pending) setTxOpen(true);
        if (r.results.some((x) => /^(COMMIT|ROLLBACK|END|ABORT)/i.test(x.sql.trim()))) setTxOpen(false);
        if (r.results.some((x) => x.kind === 'ddl' && !x.error)) bumpSchema(conn.id);
      } catch (e) {
        if (e instanceof ApiError && e.code === 'pending') setTxOpen(true);
        toast.error(errorMessage(e));
      } finally {
        setBusy('');
      }
    },
    [conn.id, tabId, schema, run],
  );

  runRef.current = (all: boolean) => void execute(all ? (view.current?.state.sliceDoc() ?? '') : statementAtCursor());

  const finish = async (commit: boolean) => {
    try {
      await dbApi.finish(conn.id, tabId, commit);
      setTxOpen(false);
      toast.success(commit ? 'Committed' : 'Rolled back');
      setResults((rs) => rs.map((r) => (r.pending ? { ...r, pending: false, notice: commit ? 'committed' : 'rolled back' } : r)));
      bumpSchema(conn.id);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  const stop = () => void dbApi.cancel(conn.id, tabId).catch(() => undefined);

  const ai = async (mode: 'write' | 'explain' | 'fix' | 'optimize') => {
    const cur = statementAtCursor();
    setAiOpen(false);
    setBusy('ai');
    try {
      const r = await dbApi.aiSQL(conn.id, { mode, sql: mode === 'fix' ? (lastErr?.sql ?? cur) : cur, prompt: mode === 'write' ? aiPrompt : '', error: lastErr?.error ?? '', schema });
      if (mode === 'write') {
        const v = view.current;
        if (v) {
          const end = v.state.doc.length;
          const sep = end && !v.state.sliceDoc().endsWith('\n') ? '\n\n' : '';
          v.dispatch({ changes: { from: end, insert: sep + r.sql.trim() + '\n' }, selection: { anchor: end + sep.length } });
          v.focus();
        }
        setAiPrompt('');
        toast.info(r.text || 'SmartDB wrote the SQL below');
      } else {
        setAiText({ title: { explain: 'What it does', fix: 'The fix', optimize: 'Faster' }[mode], text: r.text, sql: mode === 'explain' ? undefined : r.sql });
      }
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setBusy('');
    }
  };

  const applySQL = (sql: string) => {
    const v = view.current;
    if (!v) return;
    const cur = statementAtCursor();
    const text = v.state.sliceDoc();
    const i = text.indexOf(cur);
    if (cur && i >= 0) v.dispatch({ changes: { from: i, to: i + cur.length, insert: sql.trim().replace(/;$/, '') } });
    else v.dispatch({ changes: { from: text.length, insert: '\n' + sql } });
    setAiText(null);
  };

  const saveQuery = async () => {
    const text = view.current?.state.sliceDoc() ?? '';
    try {
      const q = await dbApi.saveQuery(conn.projectId, { id: saved?.id, name: saved?.name ?? name, sql: text, connectionId: conn.id });
      toast.success(`Saved as ${q.name}`);
      setSaving(false);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  const cur = results[tab];
  const exportRows = async (format: 'csv' | 'json' | 'sql') => {
    if (!cur) return;
    try {
      await downloadExport(conn.id, { sql: cur.sql, schema, format }, `result.${format}`);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  return (
    <div className={`db-sql ${compact ? 'is-compact' : ''}`}>
      <div className="db-tbar">
        {busy === 'run' ? (
          <Button size="sm" variant="danger" icon="stop" onClick={stop}>
            Stop
          </Button>
        ) : (
          <Button size="sm" variant="primary" icon="play" onClick={() => runRef.current(false)} title="Run the statement under the cursor (Ctrl Enter)">
            Run{compact ? '' : ' statement'}
          </Button>
        )}
        <Button size="sm" onClick={() => runRef.current(true)} disabled={busy !== ''} title="Run every statement (Ctrl Shift Enter)">
          Run all
        </Button>
        {!compact && (
          <Button size="sm" variant="ghost" icon="chartBar" disabled={busy !== ''} onClick={() => void execute('EXPLAIN ' + statementAtCursor())}>
            Explain
          </Button>
        )}
        {conn.agents.editorAi !== false && conn.agents.access !== 'none' && (
          <div className="db-menu-wrap">
            <Button size="sm" variant="ghost" icon="sparkle" loading={busy === 'ai'} onClick={() => setAiOpen((v) => !v)} aria-expanded={aiOpen}>
              SmartDB
            </Button>
            {aiOpen && (
              <div className="db-menu db-ai-menu" role="menu">
                <form className="db-ai-write" onSubmit={(e) => (e.preventDefault(), void ai('write'))}>
                  <input autoFocus value={aiPrompt} onChange={(e) => setAiPrompt(e.target.value)} placeholder="Write SQL that… (in any language)" aria-label="What the SQL should do" />
                </form>
                <button type="button" role="menuitem" onClick={() => void ai('explain')}>
                  Explain the statement
                </button>
                <button type="button" role="menuitem" disabled={!lastErr} onClick={() => void ai('fix')}>
                  Fix the error
                </button>
                <button type="button" role="menuitem" onClick={() => void ai('optimize')}>
                  Make it faster
                </button>
              </div>
            )}
          </div>
        )}
        <Button size="sm" variant="ghost" icon="star" onClick={() => (saved ? void saveQuery() : setSaving(true))}>
          {saved ? 'Save' : 'Save as…'}
        </Button>
        <span className="db-spacer" />
        {!compact && (
          <label className="db-runon">
            <span className="text-3 fs-xs">Run on</span>
            <select value={schema} onChange={(e) => setSchema(e.target.value)} aria-label={pg ? 'Schema' : 'Database'}>
              {schemas.map((s) => (
                <option key={s}>{s}</option>
              ))}
            </select>
          </label>
        )}
      </div>
      {txOpen && (
        <div className="db-txbar" role="status">
          <Icon name="alert" />
          <span>{results.some((r) => r.pending) ? (results.find((r) => r.pending)?.notice ?? 'A statement waits for Commit or Roll back') : 'A transaction is open: Commit or Roll back when done'}</span>
          <span className="db-spacer" />
          <Button size="sm" variant="ghost" onClick={() => void finish(false)}>
            Roll back
          </Button>
          <Button size="sm" variant="primary" onClick={() => void finish(true)}>
            Commit
          </Button>
        </div>
      )}
      <div className="db-sql-editor" ref={host} />
      <div className="db-results">
        <div className="db-rtabs" role="tablist" aria-label="Results">
          {results.map((r, i) => (
            <button key={i} type="button" role="tab" aria-selected={i === tab} className={`db-rtab ${i === tab ? 'is-on' : ''} ${r.error ? 'is-err' : ''}`} onClick={() => setTab(i)}>
              {r.error ? 'Error' : r.columns?.length ? `Result ${i + 1} · ${r.rows?.length ?? 0} rows` : `${r.kind === 'tx' ? 'Transaction' : r.kind.toUpperCase()} · ${r.affected} rows`}
              <span className="text-3"> · {r.ms} ms</span>
            </button>
          ))}
          <span className="db-spacer" />
          {cur?.columns?.length ? (
            <>
              <div className="segmented sm">
                {(['table', 'chart', 'json'] as const).map((m) => (
                  <button key={m} type="button" className={`segmented-item ${mode === m ? 'is-active' : ''}`} onClick={() => setMode(m)}>
                    {m[0].toUpperCase() + m.slice(1)}
                  </button>
                ))}
              </div>
              <Button size="sm" variant="ghost" icon="copy" aria-label="Copy as TSV" iconOnly onClick={() => void navigator.clipboard?.writeText(toTSV(cur))} />
              <Button size="sm" variant="ghost" icon="download" onClick={() => void exportRows('csv')}>
                CSV
              </Button>
            </>
          ) : null}
        </div>
        <div className="db-result-body">
          {!cur && <div className="db-empty text-3">Run a statement: Ctrl Enter runs the one under the cursor.</div>}
          {cur?.error && (
            <div className="db-error" role="alert">
              {cur.error}
              {conn.agents.editorAi !== false && conn.agents.access !== 'none' && (
                <Button size="sm" variant="ghost" icon="sparkle" onClick={() => void ai('fix')}>
                  Fix with SmartDB
                </Button>
              )}
            </div>
          )}
          {cur && !cur.error && !cur.columns?.length && (
            <div className="db-msg">
              <Icon name="check" /> {cur.kind === 'tx' ? (cur.notice ?? 'done') : `${cur.affected} row${cur.affected === 1 ? '' : 's'} changed`} · {cur.ms} ms
              {cur.notice && cur.kind !== 'tx' && <div className="text-2 fs-sm">{cur.notice}</div>}
            </div>
          )}
          {cur?.columns?.length && mode === 'table' ? <DataGrid label="Result" columns={cur.columns.map((c) => ({ name: c.name, type: c.type, sub: c.type }))} rows={cur.rows ?? []} /> : null}
          {cur?.columns?.length && mode === 'json' ? <pre className="db-json">{JSON.stringify(toObjects(cur), null, 2)}</pre> : null}
          {cur?.columns?.length && mode === 'chart' ? <Chart r={cur} /> : null}
          {cur?.truncated && <div className="db-msg text-3">Only the first {cur.rows?.length} rows are shown; export for all of them.</div>}
        </div>
      </div>
      {saving && (
        <Modal title="Save the query" icon="star" size="sm" onClose={() => setSaving(false)}
          footer={<><Button variant="ghost" onClick={() => setSaving(false)}>Cancel</Button><Button variant="primary" disabled={!name.trim()} onClick={() => void saveQuery()}>Save</Button></>}>
          <input className="control db-input" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="paid per day" aria-label="Name" onKeyDown={(e) => e.key === 'Enter' && name.trim() && void saveQuery()} />
        </Modal>
      )}
      {aiText && (
        <Modal title={aiText.title} icon="sparkle" size="md" onClose={() => setAiText(null)}
          footer={<><Button variant="ghost" onClick={() => setAiText(null)}>Close</Button>{aiText.sql && <Button variant="primary" onClick={() => applySQL(aiText.sql!)}>Use this SQL</Button>}</>}>
          <div className="db-ai-text">{aiText.text}</div>
          {aiText.sql && <pre className="db-sql-preview">{aiText.sql}</pre>}
        </Modal>
      )}
      {confirmDialog}
    </div>
  );
}

function toTSV(r: Result): string {
  const head = (r.columns ?? []).map((c) => c.name).join('\t');
  return [head, ...(r.rows ?? []).map((row) => row.map((v) => cellText(v)).join('\t'))].join('\n');
}

function toObjects(r: Result) {
  return (r.rows ?? []).slice(0, 500).map((row) => Object.fromEntries((r.columns ?? []).map((c, i) => [c.name, row[i]])));
}

/** A bar chart of the first numeric column by the first other one. */
function Chart({ r }: { r: Result }) {
  const cols = r.columns ?? [];
  const rows = (r.rows ?? []).slice(0, 60);
  const num = cols.findIndex((_, i) => rows.length > 0 && rows.every((row) => row[i] === null || !Number.isNaN(Number(cellText(row[i])))));
  if (num < 0) return <div className="db-empty text-3">No column of numbers to chart.</div>;
  const label = cols.findIndex((_, i) => i !== num);
  const vals = rows.map((row) => Number(cellText(row[num])) || 0);
  const max = Math.max(1, ...vals.map(Math.abs));
  return (
    <div className="db-chart" role="img" aria-label={`${cols[num].name} by ${label >= 0 ? cols[label].name : 'row'}`}>
      {rows.map((row, i) => (
        <div key={i} className="db-chart-row">
          <span className="db-chart-label truncate">{label >= 0 ? cellText(row[label]) : i + 1}</span>
          <span className="db-chart-bar" style={{ width: `${(Math.abs(vals[i]) / max) * 100}%` }} />
          <span className="db-chart-val tabular">{vals[i].toLocaleString()}</span>
        </div>
      ))}
    </div>
  );
}
