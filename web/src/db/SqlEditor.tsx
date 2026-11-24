// The SQL editor of a connection (docs/API-db.md; mockup 2): CodeMirror with the connection's
// dialect and its schema's tables and columns (with their types) completed; the statement under
// the cursor marked, and run with Ctrl Enter (all of them with Ctrl Shift Enter); Explain, Stop;
// a result tab per statement and Messages; Run on a database or schema, in auto-commit or a
// transaction kept open until Commit or Roll back. SmartDB writes, explains, fixes and optimises.

import { autocompletion, completionKeymap, type Completion } from '@codemirror/autocomplete';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { MySQL, PostgreSQL, sql as sqlLang, type SQLNamespace } from '@codemirror/lang-sql';
import { bracketMatching, syntaxHighlighting } from '@codemirror/language';
import { Compartment, EditorState, Prec, RangeSetBuilder } from '@codemirror/state';
import { Decoration, drawSelection, EditorView, keymap, lineNumbers, placeholder, ViewPlugin, type DecorationSet, type ViewUpdate } from '@codemirror/view';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { highlight, theme } from '../studio/code/cm';
import { dbApi, downloadExport } from './api';
import { DriverMark, EnvTag, useConfirmRunner } from './Confirm';
import { cellText, DataGrid, isNumType, numText } from './DataGrid';
import { bumpSchema, useQueries } from './store';
import { putQueryText, queryText as loadText } from './tabs';
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

/** The statement the cursor is in (or right before), as Run statement takes it. */
export function statementAt(list: { from: number; to: number; sql: string }[], pos: number) {
  return list.find((s) => pos >= s.from && pos <= s.to + 1) ?? list.find((s) => s.from >= pos) ?? list[list.length - 1];
}

// the statement under the cursor, marked as the mockup's (a bar and a tint)
const currentStatement = (pg: boolean) =>
  ViewPlugin.fromClass(
    class {
      deco: DecorationSet;
      constructor(v: EditorView) {
        this.deco = this.build(v);
      }
      update(u: ViewUpdate) {
        if (u.docChanged || u.selectionSet || u.focusChanged) this.deco = this.build(u.view);
      }
      build(v: EditorView): DecorationSet {
        const b = new RangeSetBuilder<Decoration>();
        const doc = v.state.doc;
        if (doc.length === 0) return b.finish();
        const st = statementAt(statementsOf(doc.toString(), pg), v.state.selection.main.head);
        if (!st) return b.finish();
        const first = doc.lineAt(st.from).number;
        const last = doc.lineAt(Math.min(st.to, doc.length)).number;
        if (last - first > 400) return b.finish();
        for (let n = first; n <= last; n++) b.add(doc.line(n).from, doc.line(n).from, Decoration.line({ class: 'cm-db-stmt' }));
        return b.finish();
      }
    },
    { decorations: (x) => x.deco },
  );

export interface SqlEditorProps {
  conn: Connection;
  /** the editor's own id: its session on the hub and its text in this browser */
  tabId: string;
  /** a saved query this editor edits */
  queryId?: string;
  compact?: boolean;
  onSmart?: () => void;
  smartOn?: boolean;
}

type Busy = '' | 'run' | 'ai';

export function SqlEditor({ conn, tabId, queryId, compact, onSmart, smartOn }: SqlEditorProps) {
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
  const [txMode, setTxMode] = useState<'auto' | 'manual'>('auto');
  const [aiOpen, setAiOpen] = useState(false);
  const [aiPrompt, setAiPrompt] = useState('');
  const [aiText, setAiText] = useState<{ title: string; text: string; sql?: string } | null>(null);
  const [lastErr, setLastErr] = useState<{ sql: string; error: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  const [name, setName] = useState('');
  const queries = useQueries(conn.projectId);
  const saved = useMemo(() => queries.find((q) => q.id === queryId), [queries, queryId]);
  const [run, confirmDialog] = useConfirmRunner(conn);
  const runRef = useRef<(all: boolean) => void>(() => undefined);
  const aiOn = conn.agents.editorAi !== false && conn.agents.access !== 'none';

  // the editor
  useEffect(() => {
    if (!host.current) return;
    const v = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: loadText(tabId),
        extensions: [
          lineNumbers(),
          history(),
          drawSelection(),
          bracketMatching(),
          syntaxHighlighting(highlight),
          theme,
          autocompletion(),
          currentStatement(pg),
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
            if (u.docChanged) putQueryText(tabId, u.state.sliceDoc());
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

  // a saved query's text, once it is known, in an editor that has none of its own yet
  useEffect(() => {
    const v = view.current;
    if (!v || !saved || v.state.doc.length > 0 || loadText(tabId)) return;
    v.dispatch({ changes: { from: 0, insert: saved.sql } });
  }, [saved, tabId]);

  // completion from the schema: tables, then their columns with their types (the first 40 tables)
  useEffect(() => {
    let live = true;
    void dbApi
      .schema(conn.id, schema)
      .then(async (r) => {
        if (!live || !r.schema) return;
        const sc = r.schema;
        setSchemas(sc.databases ?? []);
        if (!schema) setSchema(sc.current);
        const ns: Record<string, Completion[]> = {};
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
                  ns[n] = t.columns.map((c) => ({ label: c.name, type: 'property', detail: c.values?.length ? `enum(${c.values.map((x) => `'${x}'`).join(',')})` : `${c.type}${c.nullable ? ' · null' : ''}` }));
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
    return statementAt(statementsOf(text, pg), sel.head)?.sql ?? '';
  };

  const execute = useCallback(
    async (sql: string) => {
      if (!sql.trim()) return;
      setBusy('run');
      setLastErr(null);
      // a manual transaction begins with the first statement, and stays open until Commit
      const text = txMode === 'manual' && !txOpen ? `BEGIN;\n${sql}` : sql;
      try {
        const r = await run((a) => dbApi.run(conn.id, { session: tabId, sql: text, schema, confirmed: a.confirmed, confirmName: a.confirmName }));
        if (!r) return;
        const res = txMode === 'manual' && !txOpen ? r.results.slice(1) : r.results;
        setResults(res);
        const firstRows = res.findIndex((x) => x.columns?.length);
        setTab(Math.max(0, firstRows >= 0 ? firstRows : res.length - 1));
        const bad = res.find((x) => x.error);
        if (bad) setLastErr({ sql: bad.sql, error: bad.error! });
        const last = r.results[r.results.length - 1];
        if (txMode === 'manual' || last?.notice?.startsWith('transaction open') || last?.pending) setTxOpen(true);
        if (res.some((x) => /^(COMMIT|ROLLBACK|END|ABORT)/i.test(x.sql.trim()))) setTxOpen(false);
        if (res.some((x) => x.kind === 'ddl' && !x.error)) bumpSchema(conn.id);
      } catch (e) {
        if (e instanceof ApiError && e.code === 'pending') setTxOpen(true);
        toast.error(errorMessage(e));
      } finally {
        setBusy('');
      }
    },
    [conn.id, tabId, schema, run, txMode, txOpen],
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

  const ai = async (m: 'write' | 'explain' | 'fix' | 'optimize') => {
    const cur = statementAtCursor();
    setAiOpen(false);
    setBusy('ai');
    try {
      const r = await dbApi.aiSQL(conn.id, { mode: m, sql: m === 'fix' ? (lastErr?.sql ?? cur) : cur, prompt: m === 'write' ? aiPrompt : '', error: lastErr?.error ?? '', schema });
      if (m === 'write') {
        const v = view.current;
        if (v) {
          const end = v.state.doc.length;
          const sep = end && !v.state.sliceDoc().endsWith('\n') ? '\n\n' : '';
          v.dispatch({ changes: { from: end, insert: sep + (r.sql ?? '').trim() + '\n' }, selection: { anchor: end + sep.length } });
          v.focus();
        }
        setAiPrompt('');
        toast.info(r.text || 'SmartDB wrote the SQL below');
      } else {
        setAiText({ title: { explain: 'What it does', fix: 'The fix', optimize: 'Faster' }[m], text: r.text ?? '', sql: m === 'explain' ? undefined : r.sql });
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
      const q = await dbApi.saveQuery(conn.projectId, { id: saved?.id, name: saved?.name ?? name.replace(/\.sql$/i, ''), sql: text, connectionId: conn.id });
      toast.success(`Saved as ${q.name}.sql`);
      setSaving(false);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  const cur = results[tab === results.length ? -1 : tab];
  const messages = tab === results.length;
  const exportRows = async (format: 'csv' | 'json' | 'sql') => {
    setExportOpen(false);
    if (!cur) return;
    try {
      await downloadExport(conn.id, { sql: cur.sql, schema, format }, `result.${format}`);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  const failed = results.findIndex((r) => r.error);
  const rowsOf = (r: Result) => (r.columns?.length ? `${(r.rows?.length ?? 0).toLocaleString()} rows` : `${r.affected.toLocaleString()} row${r.affected === 1 ? '' : 's'}`);
  const ms = results.reduce((a, r) => a + r.ms, 0);

  return (
    <div className={`db-sql ${compact ? 'is-compact' : ''}`}>
      <div className="db-tbar">
        {busy === 'run' ? (
          <Button size="sm" variant="danger" icon="stop" onClick={stop}>
            Stop
          </Button>
        ) : (
          <Button size="sm" variant="primary" icon="play" onClick={() => runRef.current(false)} title="Run the statement under the cursor (Ctrl Enter)">
            {compact ? 'Run' : 'Run statement'} {!compact && <kbd className="db-kbd">Ctrl ↵</kbd>}
          </Button>
        )}
        <Button size="sm" onClick={() => runRef.current(true)} disabled={busy !== ''} title="Run every statement (Ctrl Shift Enter)">
          Run all {!compact && <kbd className="db-kbd">Ctrl ⇧ ↵</kbd>}
        </Button>
        {!compact && (
          <Button size="sm" icon="chartBar" disabled={busy !== ''} onClick={() => void execute('EXPLAIN ' + statementAtCursor())}>
            Explain
          </Button>
        )}
        <Button size="sm" variant="ghost" icon="star" onClick={() => (saved ? void saveQuery() : setSaving(true))}>
          {saved ? 'Save' : 'Save…'}
        </Button>
        {(aiOn || onSmart) && (
          <div className="db-menu-wrap">
            <Button size="sm" variant="ghost" icon="sparkle" loading={busy === 'ai'} className={`db-smart-btn ${smartOn ? 'is-on' : ''}`} onClick={() => setAiOpen((v) => !v)} aria-expanded={aiOpen}>
              SmartDB
            </Button>
            {aiOpen && (
              <div className="db-menu db-ai-menu" role="menu">
                {aiOn && (
                  <>
                    <form className="db-ai-write" onSubmit={(e) => (e.preventDefault(), void ai('write'))}>
                      <Icon name="sparkle" />
                      <input autoFocus value={aiPrompt} onChange={(e) => setAiPrompt(e.target.value)} placeholder="Write SQL that… (any language)" aria-label="What the SQL should do" />
                    </form>
                    <button type="button" role="menuitem" onClick={() => void ai('explain')}>
                      <b>Explain</b>
                      <span>what the statement does</span>
                    </button>
                    <button type="button" role="menuitem" disabled={!lastErr} onClick={() => void ai('fix')}>
                      <b>Fix</b>
                      <span>the last error</span>
                    </button>
                    <button type="button" role="menuitem" onClick={() => void ai('optimize')}>
                      <b>Optimise</b>
                      <span>make it faster</span>
                    </button>
                  </>
                )}
                {onSmart && (
                  <button type="button" role="menuitem" onClick={() => (setAiOpen(false), onSmart())}>
                    <b>{smartOn ? 'Close the assistant' : 'Ask SmartDB'}</b>
                    <span>about the data, in your words</span>
                  </button>
                )}
              </div>
            )}
          </div>
        )}
        <span className="db-spacer" />
        {!compact && (
          <>
            <label className="db-runon">
              <span>Run on</span>
              <span className="db-runon-box">
                <DriverMark driver={conn.driver} />
                <b>{conn.name}</b>
                <EnvTag env={conn.env} />
                {schemas.length > 1 && (
                  <select value={schema} onChange={(e) => setSchema(e.target.value)} aria-label={pg ? 'Schema' : 'Database'}>
                    {schemas.map((s) => (
                      <option key={s}>{s}</option>
                    ))}
                  </select>
                )}
              </span>
            </label>
            <label className="db-txmode">
              <span className="db-sr">Transaction</span>
              <select value={txMode} onChange={(e) => setTxMode(e.target.value as 'auto' | 'manual')} aria-label="Transaction" disabled={txOpen}>
                <option value="auto">Transaction: auto-commit</option>
                <option value="manual">Transaction: manual</option>
              </select>
            </label>
          </>
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
              {r.error ? `Result ${i + 1} · Error` : r.columns?.length ? `Result ${i + 1} · ${rowsOf(r)}` : `Result ${i + 1} · ${r.kind === 'tx' ? 'Transaction' : (r.sql.trim().split(/\s+/)[0] ?? '').toUpperCase()} · ${rowsOf(r)}`}
              <span className="db-rtab-ms"> · {r.ms} ms</span>
            </button>
          ))}
          {results.length > 0 && (
            <button type="button" role="tab" aria-selected={messages} className={`db-rtab ${messages ? 'is-on' : ''}`} onClick={() => setTab(results.length)}>
              Messages
            </button>
          )}
        </div>
        {cur?.columns?.length && !messages ? (
          <div className="db-rbar">
            <div className="db-seg" role="tablist" aria-label="Show as">
              {(
                [
                  ['table', 'Table', 'table'],
                  ['chart', 'Chart', 'chartBar'],
                  ['json', 'JSON', 'braces'],
                ] as const
              ).map(([m, label, icon]) => (
                <button key={m} type="button" role="tab" aria-selected={mode === m} className={mode === m ? 'is-on' : ''} onClick={() => setMode(m)}>
                  <Icon name={icon} />
                  {label}
                </button>
              ))}
            </div>
            <span className="db-spacer" />
            <Button size="sm" icon="copy" onClick={() => void navigator.clipboard?.writeText(toTSV(cur)).then(() => toast.success('Copied as TSV'))}>
              Copy
            </Button>
            <div className="db-menu-wrap">
              <Button size="sm" icon="upload" onClick={() => setExportOpen((v) => !v)} aria-expanded={exportOpen}>
                CSV · JSON · SQL
              </Button>
              {exportOpen && (
                <div className="db-menu db-menu-right" role="menu">
                  {(['csv', 'json', 'sql'] as const).map((f) => (
                    <button key={f} type="button" role="menuitem" onClick={() => void exportRows(f)}>
                      <b>{f.toUpperCase()}</b>
                      <span>{f === 'sql' ? 'INSERT statements' : f === 'csv' ? 'a spreadsheet' : 'an array of rows'}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>
          </div>
        ) : null}
        <div className="db-result-body">
          {results.length === 0 && (
            <div className="db-empty db-sql-hint">
              <Icon name="play" />
              <p>
                <kbd className="db-kbd">Ctrl ↵</kbd> runs the statement under the cursor, <kbd className="db-kbd">Ctrl ⇧ ↵</kbd> all of them.
              </p>
            </div>
          )}
          {messages && (
            <div className="db-messages">
              {results.map((r, i) => (
                <div key={i} className={`db-msg-line ${r.error ? 'is-err' : ''}`}>
                  <Icon name={r.error ? 'alertCircle' : 'check'} />
                  <span>
                    <b>Statement {i + 1}</b> · {r.error ? r.error : r.notice ? r.notice : `${rowsOf(r)} · ${r.ms} ms`}
                  </span>
                </div>
              ))}
            </div>
          )}
          {!messages && cur?.error && (
            <div className="db-error" role="alert">
              <span>{cur.error}</span>
              {aiOn && (
                <Button size="sm" icon="sparkle" onClick={() => void ai('fix')}>
                  Fix with SmartDB
                </Button>
              )}
            </div>
          )}
          {!messages && cur && !cur.error && !cur.columns?.length && (
            <div className="db-msg">
              <Icon name="check" /> {cur.kind === 'tx' ? (cur.notice ?? 'done') : `${rowsOf(cur)} changed`} · {cur.ms} ms
              {cur.notice && cur.kind !== 'tx' && <div className="db-msg-note">{cur.notice}</div>}
            </div>
          )}
          {!messages && cur?.columns?.length && mode === 'table' ? <DataGrid label="Result" columns={cur.columns.map((c) => ({ name: c.name, type: c.type, sub: c.type?.toLowerCase() }))} rows={cur.rows ?? []} /> : null}
          {!messages && cur?.columns?.length && mode === 'json' ? <pre className="db-json">{JSON.stringify(toObjects(cur), null, 2)}</pre> : null}
          {!messages && cur?.columns?.length && mode === 'chart' ? <Chart r={cur} /> : null}
        </div>
        {results.length > 0 && (
          <div className={`db-runline ${failed >= 0 ? 'is-err' : ''}`}>
            <Icon name={failed >= 0 ? 'alertCircle' : 'check'} />
            <span className="truncate">
              {failed >= 0
                ? `Statement ${failed + 1} of ${results.length} failed`
                : `${results.length === 1 ? 'Statement' : `${results.length} statements`} ran${cur && !messages ? ` · ${rowsOf(cur)}` : ''} · ${ms} ms`}
              {cur?.truncated ? ' · only the first rows: export for all of them' : ''}
            </span>
            <span className="db-spacer" />
            {!compact && <span className="db-runline-r">Stops after {conn.limits.timeoutSec} s · Results capped at 1,000 rows</span>}
          </div>
        )}
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

/** The rows with a bar of their last numeric column, as the mockup's result (the first other column labels it). */
function Chart({ r }: { r: Result }) {
  const cols = r.columns ?? [];
  const rows = (r.rows ?? []).slice(0, 200);
  let num = -1;
  for (let i = cols.length - 1; i >= 0; i--) {
    if (isNumType(cols[i].type) || (rows.length > 0 && rows.every((row) => row[i] === null || !Number.isNaN(Number(cellText(row[i])))))) {
      num = i;
      break;
    }
  }
  if (num < 0) return <div className="db-empty">No column of numbers to chart.</div>;
  const label = cols.findIndex((_, i) => i !== num);
  const vals = rows.map((row) => Number(cellText(row[num])) || 0);
  const max = Math.max(1, ...vals.map(Math.abs));
  return (
    <div className="db-chart" role="img" aria-label={`${cols[num].name} by ${label >= 0 ? cols[label].name : 'row'}`}>
      <div className="db-chart-head">
        <span>{label >= 0 ? cols[label].name : '#'}</span>
        <span>{cols[num].name}</span>
        <span />
      </div>
      {rows.map((row, i) => (
        <div key={i} className="db-chart-row">
          <span className="db-chart-label truncate">{label >= 0 ? cellText(row[label]) : i + 1}</span>
          <span className="db-chart-val">{numText(row[num])}</span>
          <span className="db-chart-track">
            <span className="db-chart-bar" style={{ width: `${(Math.abs(vals[i]) / max) * 100}%` }} />
          </span>
        </div>
      ))}
    </div>
  );
}
