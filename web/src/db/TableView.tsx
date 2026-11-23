// A table of an SQL connection (docs/API-db.md): its rows (filter chips, a raw WHERE, sort, pages,
// SmartDB's Ask bar), its structure, indexes and relations. Edits in the grid stay pending until
// Commit runs them in one transaction; Review SQL shows the statements first.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { dbApi, downloadExport, type RowsRequest } from './api';
import { useConfirmRunner } from './Confirm';
import { cellText, DataGrid, type GridColumn } from './DataGrid';
import { useSchemaRev } from './store';
import type { Cell, Change, Connection, Filter, RowsPage, Sort, TableInfo } from './types';

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
  { op: 'like', label: 'LIKE' },
  { op: 'in', label: 'in', values: true },
  { op: 'not in', label: 'not in', values: true },
  { op: 'between', label: 'between', values: true },
  { op: 'null', label: 'is NULL', none: true },
  { op: 'not null', label: 'is not NULL', none: true },
];

type View = 'data' | 'structure' | 'indexes' | 'relations';

export interface TableViewProps {
  conn: Connection;
  schema: string;
  table: string;
  compact?: boolean;
  /** opens another table (a foreign key followed), with a filter */
  onOpenTable?: (schema: string, table: string, filter?: Filter) => void;
  initialFilter?: Filter;
}

function opLabel(op: string) {
  return OPS.find((o) => o.op === op)?.label ?? op;
}

export function TableView({ conn, schema, table, compact, onOpenTable, initialFilter }: TableViewProps) {
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
  // the row's detail: opened from its number, then it follows the selected row
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn.id, schema, table, filters, where, sort, offset, rev]);

  const pk = info?.primaryKey ?? [];
  const fkCols = useMemo(() => new Set((info?.foreignKeys ?? []).flatMap((f) => f.columns)), [info]);
  const columns: GridColumn[] = useMemo(() => {
    const byName = new Map((info?.columns ?? []).map((c) => [c.name, c]));
    return (page?.columns ?? []).map((c) => {
      const col = byName.get(c.name);
      const sub = col ? `${col.type}${col.key === 'PRI' ? ' · PK' : ''}${fkCols.has(c.name) ? ' → ' + (info?.foreignKeys ?? []).find((f) => f.columns.includes(c.name))?.refTable : ''}${col.nullable ? ' · null' : ''}` : c.type;
      return { name: c.name, type: col?.type ?? c.type, sub: compact ? undefined : sub, fk: fkCols.has(c.name), key: col?.key };
    });
  }, [page, info, fkCols, compact]);

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
  const commit = async (reviewed = false) => {
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
  };

  const onEdit = (row: number, col: string, value: string) => {
    const orig = page?.rows[row][page.columns.findIndex((c) => c.name === col)];
    const next = new Map(edits);
    const e = { ...(next.get(row) ?? {}) };
    if (cellText(orig) === value && !(orig === null && value === '')) delete e[col];
    else e[col] = value;
    if (Object.keys(e).length) next.set(row, e);
    else next.delete(row);
    setEdits(next);
  };

  const setNull = (row: number, col: string) => {
    const next = new Map(edits);
    next.set(row, { ...(next.get(row) ?? {}), [col]: null });
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
      setFilters(r.filters);
      setSort(r.sort);
      setWhere(r.where);
      setWhereOpen(!!r.where);
      setOffset(0);
      setAskNote(r.note);
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setAsking(false);
    }
  };

  const followFk = (col: string, v: Cell) => {
    const fk = (info?.foreignKeys ?? []).find((f) => f.columns.includes(col));
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

  return (
    <div className={`db-table ${compact ? 'is-compact' : ''}`}>
      <div className="db-tbar">
        <div className="segmented sm" role="tablist" aria-label="Table view">
          {(['data', 'structure', 'indexes', 'relations'] as View[]).map((v) => (
            <button key={v} type="button" role="tab" aria-selected={view === v} className={`segmented-item ${view === v ? 'is-active' : ''}`} onClick={() => setView(v)}>
              {v[0].toUpperCase() + v.slice(1)}
            </button>
          ))}
        </div>
        {!compact && (
          <span className="db-tbar-meta text-3">
            {schema}.{table}
            {info && info.rows >= 0 ? ` · ≈${info.rows.toLocaleString()} rows` : ''}
          </span>
        )}
        <span className="db-spacer" />
        <Button size="sm" variant="ghost" icon="refresh" onClick={() => void load()} loading={loading}>
          {compact ? '' : 'Refresh'}
        </Button>
        <div className="db-menu-wrap">
          <Button size="sm" variant="ghost" icon="download" onClick={() => setExportOpen((v) => !v)} aria-expanded={exportOpen}>
            {compact ? '' : 'Export'}
          </Button>
          {exportOpen && (
            <div className="db-menu" role="menu">
              {(['csv', 'json', 'sql'] as const).map((f) => (
                <button key={f} type="button" role="menuitem" onClick={() => void exportAs(f)}>
                  {f.toUpperCase()}
                  <span className="text-3">{f === 'sql' ? 'INSERT statements' : f === 'csv' ? 'a spreadsheet' : 'an array of rows'}</span>
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
      </div>

      {view === 'data' && (
        <>
          {conn.agents.askBar !== false && conn.agents.access !== 'none' && !compact && (
            <form className="db-askbar" onSubmit={(e) => (e.preventDefault(), void askAI())}>
              <Icon name="sparkle" />
              <input value={ask} onChange={(e) => setAsk(e.target.value)} placeholder={`Ask for rows in your words: "paid orders over 500 thousand, newest first"`} aria-label="Ask SmartDB for filters" />
              {asking ? <span className="spinner" aria-label="SmartDB is thinking" /> : <kbd>Enter</kbd>}
            </form>
          )}
          <div className="db-fbar">
            <Icon name="sliders" />
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
            <button type="button" className={`db-where-toggle mono ${whereOpen || where ? 'is-on' : ''}`} onClick={() => setWhereOpen(!whereOpen)}>
              WHERE…
            </button>
            {askNote && <span className="db-ask-note text-3">✦ {askNote}</span>}
          </div>
          {(whereOpen || where) && (
            <form
              className="db-where"
              onSubmit={(e) => {
                e.preventDefault();
                const v = (new FormData(e.currentTarget).get('where') as string) ?? '';
                setWhere(v);
                setOffset(0);
              }}
            >
              <span className="mono text-3">WHERE</span>
              <input name="where" className="mono" defaultValue={where} key={where} aria-label="A WHERE condition" placeholder="user_id IN (SELECT id FROM users WHERE city = 'Jakarta')" />
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
                onEdit={onEdit}
                onEditAdded={(i, col, v) => setAdded(added.map((r, j) => (j === i ? { ...r, [col]: v } : r)))}
                onFk={onOpenTable ? followFk : undefined}
                sort={sort}
                onSort={toggleSort}
              />
            )}
            {detail && selectedRow && page && (
              <RowDetail
                info={info}
                columns={page.columns.map((c) => c.name)}
                row={selectedRow}
                edits={edits.get(selected!) ?? {}}
                editable={canEdit}
                deleted={deleted.has(selected!)}
                onEdit={(col, v) => onEdit(selected!, col, v)}
                onNull={(col) => setNull(selected!, col)}
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
            )}
          </div>
          <div className="db-foot">
            {pending > 0 ? (
              <>
                <span className="db-changes">
                  <Icon name="pencil" />
                  {pending} change{pending === 1 ? '' : 's'}
                  <span className="text-3">
                    : {edits.size} edited · {added.length} new · {deleted.size} deleted
                  </span>
                </span>
                <Button size="sm" variant="ghost" icon="eye" onClick={() => void showSQL()}>
                  Review SQL
                </Button>
                <Button size="sm" variant="ghost" onClick={discard}>
                  Discard
                </Button>
                <Button size="sm" variant="primary" icon="check" onClick={() => void commit()}>
                  Commit
                </Button>
              </>
            ) : (
              !canEdit && (
                <span className="text-3 fs-xs">{conn.security.readOnly ? 'Read-only connection' : pk.length === 0 && info ? 'No primary key: change rows with SQL' : ''}</span>
              )
            )}
            <span className="db-spacer" />
            <span className="tabular text-2">
              {page ? `${page.rows.length ? offset + 1 : 0}–${shownTo}${total >= 0 ? ` of ${total.toLocaleString()}` : ''}` : ''}
            </span>
            <Button size="sm" variant="ghost" iconOnly icon="chevronLeft" aria-label="Previous page" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - limit))} />
            <Button size="sm" variant="ghost" iconOnly icon="chevronRight" aria-label="Next page" disabled={!page || page.rows.length < limit || (total >= 0 && shownTo >= total)} onClick={() => setOffset(offset + limit)} />
            {page && <span className="text-3 tabular fs-xs">{page.ms} ms</span>}
          </div>
        </>
      )}

      {view === 'structure' && info && <Structure info={info} />}
      {view === 'indexes' && info && <Indexes info={info} />}
      {view === 'relations' && info && <Relations info={info} onOpen={onOpenTable} />}

      {review && (
        <Modal title="What Commit runs" icon="eye" size="md" onClose={() => setReview(null)}
          footer={<><Button variant="ghost" onClick={() => setReview(null)}>Close</Button><Button variant="primary" icon="check" onClick={() => void commit(true)}>Commit</Button></>}>
          <p className="text-2 fs-sm">In one transaction; an UPDATE or DELETE that would touch other than one row rolls all of it back.</p>
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

function RowDetail(p: {
  info: TableInfo | null;
  columns: string[];
  row: Cell[];
  edits: Record<string, unknown>;
  editable: boolean;
  deleted: boolean;
  onEdit: (col: string, v: string) => void;
  onNull: (col: string) => void;
  onDelete: () => void;
  onFollow: (col: string) => void;
  onReferenced: (fk: NonNullable<TableInfo['referencedBy']>[number]) => void;
  onClose: () => void;
}) {
  const cols = new Map((p.info?.columns ?? []).map((c) => [c.name, c]));
  const fks = p.info?.foreignKeys ?? [];
  return (
    <aside className="db-detail" aria-label="The row">
      <div className="db-detail-head">
        <Icon name="table" />
        <b>Row</b>
        <span className="db-spacer" />
        {p.editable && (
          <Button size="sm" variant={p.deleted ? 'secondary' : 'danger-ghost'} icon={p.deleted ? 'undo' : 'trash'} onClick={p.onDelete}>
            {p.deleted ? 'Keep' : 'Delete'}
          </Button>
        )}
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close the row" onClick={p.onClose} />
      </div>
      <div className="db-detail-body">
        {p.columns.map((name, i) => {
          const c = cols.get(name);
          const edited = name in p.edits;
          const v = edited ? p.edits[name] : p.row[i];
          const fk = fks.find((f) => f.columns.includes(name));
          return (
            <label key={name} className="db-detail-field">
              <span className="db-detail-label">
                {name} <small className="mono">{c?.type}</small>
                {edited && <span className="db-edited">edited</span>}
                {p.editable && c?.nullable && v !== null && (
                  <button type="button" className="db-link-btn" onClick={(e) => (e.preventDefault(), p.onNull(name))}>
                    NULL
                  </button>
                )}
              </span>
              {c?.values?.length && p.editable ? (
                <select className={`control db-input ${edited ? 'is-edited' : ''}`} value={v === null ? '' : cellText(v)} onChange={(e) => p.onEdit(name, e.target.value)}>
                  {c.nullable && <option value="">NULL</option>}
                  {c.values.map((x) => (
                    <option key={x}>{x}</option>
                  ))}
                </select>
              ) : (
                <input
                  className={`control db-input mono ${edited ? 'is-edited' : ''} ${v === null ? 'is-null' : ''}`}
                  value={v === null ? '' : cellText(v)}
                  placeholder={v === null ? 'NULL' : ''}
                  readOnly={!p.editable}
                  onChange={(e) => p.onEdit(name, e.target.value)}
                />
              )}
              {fk && v !== null && (
                <button type="button" className="db-link-btn" onClick={(e) => (e.preventDefault(), p.onFollow(name))}>
                  → {fk.refTable} #{cellText(v)}
                </button>
              )}
            </label>
          );
        })}
        {(p.info?.referencedBy ?? []).length > 0 && (
          <div className="db-detail-field">
            <span className="db-detail-label">Referenced by</span>
            {(p.info?.referencedBy ?? []).map((fk) => (
              <button key={fk.name + fk.refTable} type="button" className="db-link-btn" onClick={() => p.onReferenced(fk)}>
                {fk.refTable} ({fk.columns.join(', ')})
              </button>
            ))}
          </div>
        )}
      </div>
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
              <td className="mono">{c.name}</td>
              <td className="mono text-2">{c.type}</td>
              <td>{c.nullable ? 'yes' : ''}</td>
              <td className="mono text-2">{c.default ?? ''}</td>
              <td>{c.key}</td>
              <td className="text-2">{c.extra}</td>
              <td className="text-2">{c.comment}</td>
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
              <td className="mono">
                {ix.name}
                {ix.primary && <span className="db-pk">PK</span>}
              </td>
              <td className="mono text-2">{ix.columns.join(', ')}</td>
              <td>{ix.unique ? 'yes' : ''}</td>
              <td className="text-2">{ix.kind}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {(info.indexes ?? []).length === 0 && <p className="text-3 fs-sm">No index.</p>}
    </div>
  );
}

function Relations({ info, onOpen }: { info: TableInfo; onOpen?: (schema: string, table: string) => void }) {
  const row = (fk: NonNullable<TableInfo['foreignKeys']>[number], out: boolean) => (
    <tr key={fk.name + fk.refTable + out}>
      <td className="mono">{fk.name}</td>
      <td className="mono">{out ? `${info.name}(${fk.columns.join(', ')})` : `${fk.refTable}(${fk.refColumns.join(', ')})`}</td>
      <td>→</td>
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
      <h4 className="section-title">Points to</h4>
      <table className="db-plain">
        <tbody>{(info.foreignKeys ?? []).map((f) => row(f, true))}</tbody>
      </table>
      {(info.foreignKeys ?? []).length === 0 && <p className="text-3 fs-sm">No foreign key.</p>}
      <h4 className="section-title">Pointed to by</h4>
      <table className="db-plain">
        <tbody>{(info.referencedBy ?? []).map((f) => row(f, false))}</tbody>
      </table>
      {(info.referencedBy ?? []).length === 0 && <p className="text-3 fs-sm">No table points here.</p>}
    </div>
  );
}
