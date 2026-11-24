// A table of an SQL connection (docs/API-db.md; mockups 1, 7 and 8): its rows with filter chips,
// a raw WHERE, a sort and pages, SmartDB's Ask bar when SmartDB is on; its structure, indexes and
// relations. Edits in the grid stay pending until Commit (Ctrl S) runs them in one transaction;
// Review SQL shows the statements first. On a phone the rows are a list and a row a sheet.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon, type IconName } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { dbApi, downloadExport, type RowsRequest } from './api';
import { useConfirmRunner } from './Confirm';
import { cellText, DataGrid, isNumType, numText, pillTone, type GridColumn } from './DataGrid';
import { compactNum } from './Side';
import { useSchemaRev } from './store';
import type { Cell, Change, Column, Connection, Filter, ForeignKey, RowsPage, Sort, TableInfo } from './types';

const OPS: { op: string; label: string; values?: boolean; none?: boolean }[] = [
  { op: '=', label: '=' },
  { op: '!=', label: '≠' },
  { op: '>', label: '>' },
  { op: '>=', label: '≥' },
  { op: '<', label: '<' },
  { op: '<=', label: '≤' },
  { op: 'contains', label: 'contains' },
  { op: 'starts', label: 'starts with' },
  { op: 'ends', label: 'ends with' },
  { op: 'like', label: 'like' },
  { op: 'in', label: 'in', values: true },
  { op: 'not in', label: 'not in', values: true },
  { op: 'between', label: 'between', values: true },
  { op: 'null', label: 'is null', none: true },
  { op: 'not null', label: 'is not null', none: true },
];

type View = 'data' | 'structure' | 'indexes' | 'relations';
const VIEWS: { id: View; label: string; icon: IconName }[] = [
  { id: 'data', label: 'Data', icon: 'table' },
  { id: 'structure', label: 'Structure', icon: 'layout' },
  { id: 'indexes', label: 'Indexes', icon: 'key' },
  { id: 'relations', label: 'Relations', icon: 'link' },
];

export interface TableViewProps {
  conn: Connection;
  schema: string;
  table: string;
  /** a phone or a narrow card: rows as a list, a row as a sheet */
  compact?: boolean;
  /** a board card: the toolbar is its head's */
  card?: boolean;
  /** opens another table (a foreign key followed), with a filter */
  onOpenTable?: (schema: string, table: string, filter?: Filter) => void;
  initialFilter?: Filter;
  onSmart?: () => void;
  smartOn?: boolean;
}

const opLabel = (op: string) => OPS.find((o) => o.op === op)?.label ?? op;

/** A type as a header says it: enum('a','b') is "enum", set(…) "set". */
export const shortType = (t: string) => t.replace(/^(enum|set)\s*\(.*\)$/i, '$1');

/** "bigint · PK", "bigint → users", "varchar(255) · null": a column under its name. */
function subOf(col: Column | undefined, fk: ForeignKey | undefined, fallback?: string): string {
  if (!col) return fallback ?? '';
  const parts = [fk ? `${shortType(col.type)} → ${fk.refTable}` : shortType(col.type)];
  if (col.key === 'PRI') parts.push('PK');
  else if (col.nullable) parts.push('null');
  return parts.join(' · ');
}

export function TableView({ conn, schema, table, compact, card, onOpenTable, initialFilter, onSmart, smartOn }: TableViewProps) {
  const [view, setView] = useState<View>('data');
  const [info, setInfo] = useState<TableInfo | null>(null);
  const [page, setPage] = useState<RowsPage | null>(null);
  const [filters, setFilters] = useState<Filter[]>(initialFilter ? [initialFilter] : []);
  const [where, setWhere] = useState('');
  const [whereOpen, setWhereOpen] = useState(false);
  const [sort, setSort] = useState<Sort[]>([]);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState<number | null>(null);
  // the row's detail: opened from its number (or a phone's tap), then it follows the selected row
  const [detail, setDetail] = useState(false);
  const [edits, setEdits] = useState<Map<number, Record<string, unknown>>>(new Map());
  const [deleted, setDeleted] = useState<Set<number>>(new Set());
  const [added, setAdded] = useState<Record<string, unknown>[]>([]);
  const [review, setReview] = useState<string[] | null>(null);
  const [ask, setAsk] = useState('');
  const [asking, setAsking] = useState(false);
  const [askNote, setAskNote] = useState('');
  const [exportOpen, setExportOpen] = useState(false);
  const rev = useSchemaRev(conn.id);
  const [run, confirmDialog] = useConfirmRunner(conn);
  const limit = conn.limits.pageSize || 100;

  useEffect(() => {
    let live = true;
    dbApi
      .table(conn.id, schema, table)
      .then((t) => live && setInfo(t))
      .catch((e) => live && setError(errorMessage(e)));
    return () => {
      live = false;
    };
  }, [conn.id, schema, table, rev]);

  const request = useCallback((): RowsRequest => ({ schema, table, filters, where, sort, offset, limit }), [schema, table, filters, where, sort, offset, limit]);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const p = await run(() => dbApi.rows(conn.id, request()));
      if (p) setPage(p);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [conn.id, request, run]);

  useEffect(() => {
    void load();
    // a new page when what it shows changes
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn.id, schema, table, filters, where, sort, offset, rev]);

  const pk = info?.primaryKey ?? [];
  const fks = useMemo(() => info?.foreignKeys ?? [], [info]);
  const columns: GridColumn[] = useMemo(() => {
    const byName = new Map((info?.columns ?? []).map((c) => [c.name, c]));
    return (page?.columns ?? []).map((c) => {
      const col = byName.get(c.name);
      const fk = fks.find((f) => f.columns.includes(c.name));
      return { name: c.name, type: col?.type ?? c.type, sub: subOf(col, fk, c.type), fk: !!fk, key: col?.key, values: col?.values };
    });
  }, [page, info, fks]);

  const pending = edits.size + deleted.size + added.length;
  const canEdit = !conn.security.readOnly && pk.length > 0;

  const keyOf = (row: number): Record<string, unknown> => {
    const out: Record<string, unknown> = {};
    for (const k of pk) {
      const i = page?.columns.findIndex((c) => c.name === k) ?? -1;
      out[k] = i >= 0 ? page?.rows[row][i] : null;
    }
    return out;
  };

  const changes = (): Change[] => {
    const out: Change[] = [];
    edits.forEach((set, row) => {
      if (!deleted.has(row)) out.push({ op: 'update', key: keyOf(row), set });
    });
    added.forEach((set) => out.push({ op: 'insert', set }));
    deleted.forEach((row) => out.push({ op: 'delete', key: keyOf(row) }));
    return out;
  };

  const discard = () => {
    setEdits(new Map());
    setDeleted(new Set());
    setAdded([]);
  };

  const showSQL = async () => {
    try {
      const r = await dbApi.apply(conn.id, { schema, table, changes: changes(), preview: true });
      setReview(r.sql);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  // from Review SQL the owner has seen the statements: no second question
  const commit = useCallback(
    async (reviewed = false) => {
      try {
        const r = await run((a) => dbApi.apply(conn.id, { schema, table, changes: changes(), confirmed: reviewed || a.confirmed }));
        if (!r) return;
        toast.success(`${r.sql.length} change${r.sql.length === 1 ? '' : 's'} committed`);
        discard();
        setReview(null);
        void load();
      } catch (e) {
        toast.error(errorMessage(e));
      }
    },
    // the changes are read when it runs
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [conn.id, schema, table, edits, deleted, added, run, load],
  );

  // Ctrl S commits, as the mockup's button says
  useEffect(() => {
    if (!pending) return;
    const key = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's' && !e.shiftKey) {
        e.preventDefault();
        void commit();
      }
    };
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  }, [pending, commit]);

  const onEdit = (row: number, col: string, value: string | null) => {
    const orig = page?.rows[row][page.columns.findIndex((c) => c.name === col)];
    const next = new Map(edits);
    const e = { ...(next.get(row) ?? {}) };
    if (value !== null && cellText(orig) === value && orig !== null) delete e[col];
    else if (value === null && orig === null) delete e[col];
    else e[col] = value;
    if (Object.keys(e).length) next.set(row, e);
    else next.delete(row);
    setEdits(next);
  };

  const toggleSort = (col: string) => {
    const s = sort.find((x) => x.column === col);
    setOffset(0);
    if (!s) setSort([{ column: col }]);
    else if (!s.desc) setSort([{ column: col, desc: true }]);
    else setSort([]);
  };

  const askAI = async () => {
    if (!ask.trim()) return;
    setAsking(true);
    setAskNote('');
    try {
      const r = await dbApi.aiFilters(conn.id, { schema, table, text: ask });
      setFilters(r.filters ?? []);
      setSort(r.sort ?? []);
      setWhere(r.where ?? '');
      setWhereOpen(!!r.where);
      setOffset(0);
      setAskNote(r.note ?? '');
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setAsking(false);
    }
  };

  const followFk = (col: string, v: Cell) => {
    const fk = fks.find((f) => f.columns.includes(col));
    if (!fk || !onOpenTable) return;
    const refCol = fk.refColumns[fk.columns.indexOf(col)] ?? fk.refColumns[0];
    onOpenTable(fk.refSchema || schema, fk.refTable, { column: refCol, op: '=', value: cellText(v) });
  };

  const exportAs = async (format: 'csv' | 'json' | 'sql') => {
    setExportOpen(false);
    try {
      await downloadExport(conn.id, { rows: { schema, table, filters, where, sort }, format }, `${table}.${format}`);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  const total = page?.total ?? -1;
  const shownTo = (page?.rows.length ?? 0) + offset;
  const selectedRow = selected !== null && page ? page.rows[selected] : null;
  const views = compact ? VIEWS.filter((v) => v.id !== 'indexes') : VIEWS;
  const smartAsk = smartOn && conn.agents.askBar !== false;

  const detailEl =
    detail && selectedRow && page ? (
      <RowDetail
        table={table}
        info={info}
        columns={page.columns.map((c) => c.name)}
        row={selectedRow}
        pkValue={pk.length ? cellText(selectedRow[page.columns.findIndex((c) => c.name === pk[0])]) : String((selected ?? 0) + offset + 1)}
        edits={edits.get(selected!) ?? {}}
        editable={canEdit}
        deleted={deleted.has(selected!)}
        sheet={compact}
        onEdit={(col, v) => onEdit(selected!, col, v)}
        onDelete={() => {
          const next = new Set(deleted);
          if (next.has(selected!)) next.delete(selected!);
          else next.add(selected!);
          setDeleted(next);
        }}
        onFollow={(col) => followFk(col, selectedRow[page.columns.findIndex((c) => c.name === col)])}
        onReferenced={(fk) => {
          // a table pointing here: its column (refColumns) holds our column's (columns) value
          const key = selectedRow[page.columns.findIndex((c) => c.name === fk.columns[0])];
          onOpenTable?.(fk.refSchema, fk.refTable, { column: fk.refColumns[0], op: '=', value: cellText(key) });
        }}
        onClose={() => setDetail(false)}
      />
    ) : null;

  return (
    <div className={`db-table ${compact ? 'is-compact' : ''} ${card ? 'is-card' : ''}`}>
      <div className="db-tbar">
        <div className="db-seg" role="tablist" aria-label="Table view">
          {views.map((v) => (
            <button key={v.id} type="button" role="tab" aria-selected={view === v.id} className={view === v.id ? 'is-on' : ''} onClick={() => setView(v.id)}>
              {!compact && <Icon name={v.icon} />}
              {compact && v.id === 'structure' ? 'Columns' : v.label}
            </button>
          ))}
        </div>
        {!compact && !card && (
          <span className="db-tbar-meta truncate">
            {schema ? `${schema}.` : ''}
            {table}
            {info && info.rows >= 0 ? ` · ≈${info.rows.toLocaleString()} rows` : ''}
          </span>
        )}
        <span className="db-spacer" />
        <Button size="sm" icon="refresh" onClick={() => void load()} loading={loading} aria-label="Refresh" title="Refresh">
          {compact ? '' : 'Refresh'}
        </Button>
        <div className="db-menu-wrap">
          <Button size="sm" icon="upload" onClick={() => setExportOpen((v) => !v)} aria-expanded={exportOpen} aria-label="Export" title="Export">
            {compact ? '' : 'Export'}
          </Button>
          {exportOpen && (
            <div className="db-menu" role="menu">
              {(['csv', 'json', 'sql'] as const).map((f) => (
                <button key={f} type="button" role="menuitem" onClick={() => void exportAs(f)}>
                  <b>{f.toUpperCase()}</b>
                  <span>{f === 'sql' ? 'INSERT statements' : f === 'csv' ? 'a spreadsheet' : 'an array of rows'}</span>
                </button>
              ))}
            </div>
          )}
        </div>
        {canEdit && view === 'data' && (
          <Button size="sm" icon="plus" onClick={() => setAdded([...added, {}])}>
            Row
          </Button>
        )}
        {onSmart && (
          <Button size="sm" icon="sparkle" className={`db-smart-btn ${smartOn ? 'is-on' : ''}`} onClick={onSmart} aria-pressed={!!smartOn} title="SmartDB: ask in your words">
            {compact ? '' : 'SmartDB'}
          </Button>
        )}
      </div>

      {view === 'data' && (
        <>
          {smartAsk && (
            <form className="db-askbar" onSubmit={(e) => (e.preventDefault(), void askAI())}>
              <Icon name="sparkle" />
              <input value={ask} onChange={(e) => setAsk(e.target.value)} placeholder="Ask for rows in your words: paid orders over 500 thousand, newest first" aria-label="Ask SmartDB for filters" />
              {asking ? <span className="spinner" aria-label="SmartDB is thinking" /> : <span className="db-askbar-k">Enter makes the filters</span>}
            </form>
          )}
          <div className="db-fbar">
            <Icon name="sliders" className="db-fbar-i" />
            {filters.map((f, i) => (
              <span key={i} className="db-fchip">
                {f.column} <span className="db-op">{opLabel(f.op)}</span>
                {f.values?.length ? <span className="db-val">{f.values.join(', ')}</span> : f.value !== undefined && f.value !== '' ? <span className="db-val">{f.value}</span> : null}
                <button type="button" aria-label={`Remove the filter on ${f.column}`} onClick={() => (setFilters(filters.filter((_, j) => j !== i)), setOffset(0))}>
                  <Icon name="close" />
                </button>
              </span>
            ))}
            <FilterAdder columns={info?.columns.map((c) => c.name) ?? []} onAdd={(f) => (setFilters([...filters, f]), setOffset(0))} />
            {!compact && (
              <button type="button" className={`db-where-toggle ${whereOpen || where ? 'is-on' : ''}`} onClick={() => setWhereOpen(!whereOpen)}>
                WHERE …
              </button>
            )}
            {askNote && <span className="db-ask-note">✦ {askNote}</span>}
            <span className="db-spacer" />
            {sort[0] && (
              <button type="button" className="db-sortchip" onClick={() => toggleSort(sort[0].column)} title="Change the order">
                <Icon name="sort" />
                {sort[0].column} <span className="db-op">{sort[0].desc ? 'desc' : 'asc'}</span>
              </button>
            )}
          </div>
          {(whereOpen || where) && (
            <form
              className="db-where"
              onSubmit={(e) => {
                e.preventDefault();
                setWhere(((new FormData(e.currentTarget).get('where') as string) ?? '').trim());
                setOffset(0);
              }}
            >
              <span className="db-where-k">WHERE</span>
              <input name="where" defaultValue={where} key={where} aria-label="A WHERE condition" placeholder="user_id IN (SELECT id FROM users WHERE city = 'Jakarta')" />
              <Button size="sm" type="submit">
                Apply
              </Button>
              {where && (
                <Button size="sm" variant="ghost" onClick={() => (setWhere(''), setWhereOpen(false))}>
                  Clear
                </Button>
              )}
            </form>
          )}
          <div className="db-table-body">
            {error ? (
              <div className="db-error" role="alert">
                {error}
              </div>
            ) : compact && page ? (
              <RowList info={info} page={page} pk={pk} deleted={deleted} onOpen={(i) => (setSelected(i), setDetail(true))} />
            ) : (
              <DataGrid
                label={`${table} rows`}
                columns={columns}
                rows={page?.rows ?? []}
                first={offset + 1}
                editable={canEdit}
                edits={edits}
                deleted={deleted}
                added={added}
                selected={selected}
                onSelect={setSelected}
                onOpenRow={() => setDetail(true)}
                onEdit={(r, c, v) => onEdit(r, c, v)}
                onEditAdded={(i, col, v) => setAdded(added.map((r, j) => (j === i ? { ...r, [col]: v } : r)))}
                onFk={onOpenTable ? followFk : undefined}
                sort={sort}
                onSort={toggleSort}
              />
            )}
            {!compact && detailEl}
          </div>
          {compact && detailEl}
          <div className="db-foot">
            {pending > 0 ? (
              <>
                <span className="db-changes">
                  <Icon name="pencil" />
                  {pending} change{pending === 1 ? '' : 's'}
                  {!compact && `: ${[edits.size && `${edits.size} edited`, added.length && `${added.length} new`, deleted.size && `${deleted.size} deleted`].filter(Boolean).join(' · ')}`}
                </span>
                <Button size="sm" icon="eye" onClick={() => void showSQL()}>
                  Review SQL
                </Button>
                <Button size="sm" variant="ghost" onClick={discard}>
                  Discard
                </Button>
                <Button size="sm" variant="primary" icon="check" onClick={() => void commit()}>
                  Commit {!compact && <kbd className="db-kbd">Ctrl S</kbd>}
                </Button>
              </>
            ) : (
              !canEdit &&
              info && <span className="db-foot-note">{conn.security.readOnly ? 'Read-only connection' : pk.length === 0 ? 'No primary key: change rows with SQL' : ''}</span>
            )}
            <span className="db-spacer" />
            <span className="db-foot-range tabular">{page ? `${page.rows.length ? offset + 1 : 0}–${shownTo}${total >= 0 ? ` of ${total.toLocaleString()}` : ''}` : ''}</span>
            <Button size="sm" variant="ghost" iconOnly icon="chevronLeft" aria-label="Previous page" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - limit))} />
            <Button size="sm" variant="ghost" iconOnly icon="chevronRight" aria-label="Next page" disabled={!page || page.rows.length < limit || (total >= 0 && shownTo >= total)} onClick={() => setOffset(offset + limit)} />
            {page && !compact && <span className="db-foot-ms tabular">{page.ms} ms</span>}
          </div>
        </>
      )}

      {view === 'structure' && info && <Structure info={info} />}
      {view === 'indexes' && info && <Indexes info={info} />}
      {view === 'relations' && info && <Relations info={info} onOpen={onOpenTable} />}

      {review && (
        <Modal title="What Commit runs" icon="eye" size="md" onClose={() => setReview(null)}
          footer={<><Button variant="ghost" onClick={() => setReview(null)}>Close</Button><Button variant="primary" icon="check" onClick={() => void commit(true)}>Commit</Button></>}>
          <p className="db-dlg-note">In one transaction; an UPDATE or DELETE that would touch other than one row rolls all of it back.</p>
          <pre className="db-sql-preview">{review.join(';\n') + ';'}</pre>
        </Modal>
      )}
      {confirmDialog}
    </div>
  );
}

function FilterAdder({ columns, onAdd }: { columns: string[]; onAdd: (f: Filter) => void }) {
  const [open, setOpen] = useState(false);
  const [col, setCol] = useState('');
  const [op, setOp] = useState('=');
  const [val, setVal] = useState('');
  const spec = OPS.find((o) => o.op === op)!;
  if (!open) {
    return (
      <button type="button" className="db-add-filter" onClick={() => (setOpen(true), setCol(columns[0] ?? ''))}>
        <Icon name="plus" /> Filter
      </button>
    );
  }
  const add = () => {
    if (!col) return;
    const f: Filter = { column: col, op };
    if (spec.values) f.values = val.split(',').map((x) => x.trim()).filter(Boolean);
    else if (!spec.none) f.value = val;
    onAdd(f);
    setOpen(false);
    setVal('');
  };
  return (
    <form className="db-filter-form" onSubmit={(e) => (e.preventDefault(), add())}>
      <select value={col} onChange={(e) => setCol(e.target.value)} aria-label="Column">
        {columns.map((c) => (
          <option key={c}>{c}</option>
        ))}
      </select>
      <select value={op} onChange={(e) => setOp(e.target.value)} aria-label="Operator">
        {OPS.map((o) => (
          <option key={o.op} value={o.op}>
            {o.label}
          </option>
        ))}
      </select>
      {!spec.none && <input value={val} onChange={(e) => setVal(e.target.value)} placeholder={spec.values ? 'a, b, c' : 'value'} aria-label="Value" autoFocus />}
      <Button size="sm" type="submit">
        Add
      </Button>
      <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Cancel" onClick={() => setOpen(false)} />
    </form>
  );
}

/** A phone's rows (mockup 7): the key and its status, an amount, and who and when under them. */
function RowList({ info, page, pk, deleted, onOpen }: { info: TableInfo | null; page: RowsPage; pk: string[]; deleted: Set<number>; onOpen: (i: number) => void }) {
  const cols = page.columns.map((c) => c.name);
  const byName = new Map((info?.columns ?? []).map((c) => [c.name, c]));
  const fkCols = new Set((info?.foreignKeys ?? []).flatMap((f) => f.columns));
  const idI = cols.indexOf(pk[0] ?? cols[0]);
  const pillI = cols.findIndex((n) => byName.get(n)?.values?.length);
  const typeOf = (n: string, i: number) => byName.get(n)?.type ?? page.columns[i]?.type ?? '';
  const amountI = cols.findIndex((n, i) => i !== idI && !fkCols.has(n) && isNumType(typeOf(n, i)));
  const fkI = cols.findIndex((n) => fkCols.has(n));
  const timeI = cols.findIndex((n, i) => /time|date/i.test(typeOf(n, i)));
  const textI = cols.findIndex((n, i) => i !== idI && i !== pillI && /char|text/i.test(typeOf(n, i)));
  const when = (v: Cell) => {
    const s = cellText(v);
    const m = /(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2})/.exec(s);
    if (!m) return s;
    return m[1] === new Date().toISOString().slice(0, 10) ? m[2] : m[1];
  };
  if (page.rows.length === 0) return <div className="db-empty">No rows</div>;
  return (
    <ul className="db-rowlist" aria-label="Rows">
      {page.rows.map((r, i) => {
        const sub = [fkI >= 0 && r[fkI] !== null ? `${cols[fkI].replace(/_id$/, '')} ${cellText(r[fkI])}` : '', timeI >= 0 && r[timeI] !== null ? when(r[timeI]) : '', fkI < 0 && textI >= 0 && r[textI] !== null ? cellText(r[textI]) : '']
          .filter(Boolean)
          .join(' · ');
        return (
          <li key={i}>
            <button type="button" className={`db-rowcard ${deleted.has(i) ? 'is-del' : ''}`} onClick={() => onOpen(i)}>
              <span className="db-rowcard-top">
                <b className="mono">#{cellText(r[idI])}</b>
                {pillI >= 0 && r[pillI] !== null && <span className={`db-pill tone-${pillTone(cellText(r[pillI]))}`}>{cellText(r[pillI])}</span>}
                <span className="db-spacer" />
                {amountI >= 0 && <span className="mono">{numText(r[amountI])}</span>}
              </span>
              {sub && <span className="db-rowcard-sub truncate">{sub}</span>}
            </button>
          </li>
        );
      })}
    </ul>
  );
}

function RowDetail(p: {
  table: string;
  info: TableInfo | null;
  columns: string[];
  row: Cell[];
  pkValue: string;
  edits: Record<string, unknown>;
  editable: boolean;
  deleted: boolean;
  sheet?: boolean;
  onEdit: (col: string, v: string | null) => void;
  onDelete: () => void;
  onFollow: (col: string) => void;
  onReferenced: (fk: ForeignKey) => void;
  onClose: () => void;
}) {
  const [editing, setEditing] = useState(!p.sheet);
  const cols = new Map((p.info?.columns ?? []).map((c) => [c.name, c]));
  const fks = p.info?.foreignKeys ?? [];
  const canEdit = p.editable && editing;
  const fields = (
    <div className="db-detail-body">
      {p.columns.map((name, i) => {
        const c = cols.get(name);
        const edited = name in p.edits;
        const v = edited ? p.edits[name] : p.row[i];
        const fk = fks.find((f) => f.columns.includes(name));
        return (
          <div key={name} className="db-field">
            <label className="db-field-l" htmlFor={`dbf-${name}`}>
              {name} {!p.sheet && <small>{c ? shortType(c.type) : ''}</small>}
              {edited && <span className="db-edited">edited</span>}
              {canEdit && c?.nullable && v !== null && (
                <button type="button" className="db-link-btn" onClick={() => p.onEdit(name, null)}>
                  NULL
                </button>
              )}
            </label>
            {c?.values?.length && canEdit ? (
              <select id={`dbf-${name}`} className={`db-field-in ${edited ? 'is-edited' : ''}`} value={v === null ? '' : cellText(v)} onChange={(e) => p.onEdit(name, e.target.value)}>
                {c.nullable && <option value="">NULL</option>}
                {c.values.map((x) => (
                  <option key={x}>{x}</option>
                ))}
              </select>
            ) : (
              <input
                id={`dbf-${name}`}
                className={`db-field-in ${edited ? 'is-edited' : ''} ${v === null ? 'is-null' : ''}`}
                value={v === null ? '' : cellText(v)}
                placeholder={v === null ? 'NULL' : ''}
                readOnly={!canEdit}
                onChange={(e) => p.onEdit(name, e.target.value)}
              />
            )}
            {edited && <span className="db-was">was {cellText(p.row[i])}</span>}
            {fk && v !== null && (
              <button type="button" className="db-link-btn" onClick={() => p.onFollow(name)}>
                → {fk.refTable} #{cellText(v)}
              </button>
            )}
          </div>
        );
      })}
      {(p.info?.referencedBy ?? []).length > 0 && (
        <div className="db-field db-refby">
          <span className="db-field-l">Referenced by</span>
          {(p.info?.referencedBy ?? []).map((fk) => (
            <button key={fk.name + fk.refTable} type="button" className="db-link-btn" onClick={() => p.onReferenced(fk)}>
              {fk.refTable} · {fk.refColumns.join(', ')}
            </button>
          ))}
        </div>
      )}
      {p.editable && !p.sheet && (
        <button type="button" className={`db-link-btn ${p.deleted ? '' : 'is-danger'}`} onClick={p.onDelete}>
          {p.deleted ? 'Keep this row' : 'Delete this row'}
        </button>
      )}
    </div>
  );
  if (p.sheet) {
    const json = Object.fromEntries(p.columns.map((n, i) => [n, p.row[i]]));
    return (
      <Modal
        title={`Row ${p.pkValue}`}
        icon="table"
        size="md"
        onClose={p.onClose}
        className="db-row-sheet"
        actions={<span className="db-detail-sub">{p.table}</span>}
        footer={
          <>
            <Button icon="copy" onClick={() => void navigator.clipboard?.writeText(JSON.stringify(json, null, 2)).then(() => toast.success('Copied'))}>
              Copy JSON
            </Button>
            {p.editable && !editing ? (
              <Button variant="primary" icon="pencil" onClick={() => setEditing(true)}>
                Edit
              </Button>
            ) : p.editable ? (
              <Button variant={p.deleted ? 'secondary' : 'danger-ghost'} icon={p.deleted ? 'undo' : 'trash'} onClick={p.onDelete}>
                {p.deleted ? 'Keep' : 'Delete'}
              </Button>
            ) : null}
          </>
        }
      >
        {fields}
      </Modal>
    );
  }
  return (
    <aside className="db-detail" aria-label="The row">
      <div className="db-detail-h">
        <Icon name="table" />
        <b>Row {p.pkValue}</b>
        <span className="db-detail-sub">{p.table}</span>
        <span className="db-spacer" />
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close the row" onClick={p.onClose} />
      </div>
      {fields}
    </aside>
  );
}

function Structure({ info }: { info: TableInfo }) {
  return (
    <div className="db-struct">
      <table className="db-plain">
        <thead>
          <tr>
            <th>Column</th>
            <th>Type</th>
            <th>Null</th>
            <th>Default</th>
            <th>Key</th>
            <th>Extra</th>
            <th>Comment</th>
          </tr>
        </thead>
        <tbody>
          {info.columns.map((c) => (
            <tr key={c.name}>
              <td className="mono db-strong">{c.name}</td>
              <td className="mono db-dim">{c.type}</td>
              <td>{c.nullable ? 'yes' : ''}</td>
              <td className="mono db-dim">{c.default ?? ''}</td>
              <td>{c.key === 'PRI' ? <span className="db-pill tone-warn">PK</span> : c.key}</td>
              <td className="db-dim">{c.extra}</td>
              <td className="db-dim">{c.comment}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="db-ddl-head">
        <b>DDL</b>
        <Button size="sm" variant="ghost" icon="copy" onClick={() => void navigator.clipboard?.writeText(info.ddl)}>
          Copy
        </Button>
      </div>
      <pre className="db-sql-preview">{info.ddl}</pre>
    </div>
  );
}

function Indexes({ info }: { info: TableInfo }) {
  return (
    <div className="db-struct">
      <table className="db-plain">
        <thead>
          <tr>
            <th>Index</th>
            <th>Columns</th>
            <th>Unique</th>
            <th>Kind</th>
          </tr>
        </thead>
        <tbody>
          {(info.indexes ?? []).map((ix) => (
            <tr key={ix.name}>
              <td className="mono db-strong">
                {ix.name} {ix.primary && <span className="db-pill tone-warn">PK</span>}
              </td>
              <td className="mono db-dim">{ix.columns.join(', ')}</td>
              <td>{ix.unique ? 'yes' : ''}</td>
              <td className="db-dim">{ix.kind}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {(info.indexes ?? []).length === 0 && <p className="db-dlg-note">No index.</p>}
    </div>
  );
}

function Relations({ info, onOpen }: { info: TableInfo; onOpen?: (schema: string, table: string) => void }) {
  const row = (fk: ForeignKey, out: boolean) => (
    <tr key={fk.name + fk.refTable + out}>
      <td className="mono db-dim">{fk.name}</td>
      <td className="mono">{out ? `${info.name}(${fk.columns.join(', ')})` : `${fk.refTable}(${fk.refColumns.join(', ')})`}</td>
      <td className="db-dim">→</td>
      <td className="mono">
        {onOpen ? (
          <button type="button" className="db-link-btn" onClick={() => onOpen(fk.refSchema, fk.refTable)}>
            {out ? `${fk.refTable}(${fk.refColumns.join(', ')})` : `${info.name}(${fk.columns.join(', ')})`}
          </button>
        ) : out ? (
          `${fk.refTable}(${fk.refColumns.join(', ')})`
        ) : (
          `${info.name}(${fk.columns.join(', ')})`
        )}
      </td>
    </tr>
  );
  return (
    <div className="db-struct">
      <h4 className="db-cap">Points to</h4>
      <table className="db-plain">
        <tbody>{(info.foreignKeys ?? []).map((f) => row(f, true))}</tbody>
      </table>
      {(info.foreignKeys ?? []).length === 0 && <p className="db-dlg-note">No foreign key.</p>}
      <h4 className="db-cap">Pointed to by</h4>
      <table className="db-plain">
        <tbody>{(info.referencedBy ?? []).map((f) => row(f, false))}</tbody>
      </table>
      {(info.referencedBy ?? []).length === 0 && <p className="db-dlg-note">No table points here.</p>}
      <p className="db-dlg-note">{compactNum(info.rows) ? `≈${info.rows.toLocaleString()} rows` : ''}</p>
    </div>
  );
}
