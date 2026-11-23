// The grid of the database browser: a table's rows or a query's result. Rows are windowed, so a
// page of thousands scrolls smoothly. In a table view cells edit in place (double-click or Enter);
// the edits stay pending, coloured, until the view commits them.

import { useEffect, useMemo, useRef, useState, type KeyboardEvent as RKeyboardEvent } from 'react';
import type { Cell } from './types';

export interface GridColumn {
  name: string;
  type?: string;
  sub?: string;
  /** a foreign key: its values link to the row they point to */
  fk?: boolean;
  key?: string;
}

export interface GridProps {
  columns: GridColumn[];
  rows: Cell[][];
  /** the number shown before the first row (a page's offset + 1) */
  first?: number;
  editable?: boolean;
  /** edited cells: row → column → the new value */
  edits?: Map<number, Record<string, unknown>>;
  deleted?: Set<number>;
  /** rows added, shown first */
  added?: Record<string, unknown>[];
  selected?: number | null;
  onSelect?: (row: number) => void;
  /** the row's number clicked (or Space): its detail */
  onOpenRow?: (row: number) => void;
  onEdit?: (row: number, column: string, value: string) => void;
  onEditAdded?: (i: number, column: string, value: string) => void;
  onFk?: (column: string, value: Cell) => void;
  sort?: { column: string; desc?: boolean }[];
  onSort?: (column: string) => void;
  label: string;
}

const ROW_H = 30;

export function cellText(v: Cell | unknown): string {
  if (v === null || v === undefined) return 'NULL';
  if (typeof v === 'object') {
    const o = v as Record<string, unknown>;
    if (typeof o.$cut === 'string') return o.$cut;
    if (typeof o.$bin === 'string') return `0x${o.$bin}${(o.len as number) > o.$bin.length / 2 ? '…' : ''}`;
    return JSON.stringify(v);
  }
  return String(v);
}

function cellClass(v: unknown, type?: string): string {
  if (v === null || v === undefined) return 'db-null';
  if (typeof v === 'number' || /int|decimal|numeric|float|double|real|serial|money/i.test(type ?? '')) return 'db-num';
  if (typeof v === 'object') return 'db-special';
  return '';
}

function widthOf(col: GridColumn, rows: Cell[][], i: number): number {
  let chars = Math.max(col.name.length, (col.sub ?? '').length * 0.85);
  for (let r = 0; r < Math.min(rows.length, 60); r++) chars = Math.max(chars, Math.min(48, cellText(rows[r][i]).length));
  return Math.round(Math.min(380, Math.max(72, chars * 7.6 + 24)));
}

export function DataGrid(p: GridProps) {
  const box = useRef<HTMLDivElement>(null);
  const [top, setTop] = useState(0);
  const [height, setHeight] = useState(400);
  const [cur, setCur] = useState<{ r: number; c: number } | null>(null);
  const [editing, setEditing] = useState<{ r: number; c: number; v: string } | null>(null);
  const added = p.added ?? [];
  const widths = useMemo(() => p.columns.map((c, i) => widthOf(c, p.rows, i)), [p.columns, p.rows]);
  const total = added.length + p.rows.length;

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setHeight(el.clientHeight));
    ro.observe(el);
    setHeight(el.clientHeight);
    return () => ro.disconnect();
  }, []);

  const from = Math.max(0, Math.floor(top / ROW_H) - 8);
  const to = Math.min(total, Math.ceil((top + height) / ROW_H) + 8);

  const valueAt = (r: number, c: number): unknown => {
    if (r < added.length) return added[r][p.columns[c].name] ?? null;
    const row = r - added.length;
    const e = p.edits?.get(row);
    if (e && p.columns[c].name in e) return e[p.columns[c].name];
    return p.rows[row]?.[c];
  };

  const startEdit = (r: number, c: number) => {
    if (!p.editable) return;
    if (r >= added.length && p.deleted?.has(r - added.length)) return;
    const v = valueAt(r, c);
    setEditing({ r, c, v: v === null || v === undefined ? '' : cellText(v) });
  };

  const commit = () => {
    if (!editing) return;
    const col = p.columns[editing.c].name;
    if (editing.r < added.length) p.onEditAdded?.(editing.r, col, editing.v);
    else p.onEdit?.(editing.r - added.length, col, editing.v);
    setEditing(null);
    box.current?.focus();
  };

  const onKey = (e: RKeyboardEvent) => {
    if (editing) return;
    if (!cur) return;
    const move = (dr: number, dc: number) => {
      const r = Math.max(0, Math.min(total - 1, cur.r + dr));
      const c = Math.max(0, Math.min(p.columns.length - 1, cur.c + dc));
      setCur({ r, c });
      if (r >= added.length) p.onSelect?.(r - added.length);
      const el = box.current;
      if (el) {
        if (r * ROW_H < el.scrollTop) el.scrollTop = r * ROW_H;
        if ((r + 2) * ROW_H > el.scrollTop + el.clientHeight) el.scrollTop = (r + 2) * ROW_H - el.clientHeight;
      }
    };
    switch (e.key) {
      case 'ArrowDown':
        move(1, 0);
        break;
      case 'ArrowUp':
        move(-1, 0);
        break;
      case 'ArrowLeft':
        move(0, -1);
        break;
      case 'ArrowRight':
        move(0, 1);
        break;
      case 'Enter':
      case 'F2':
        startEdit(cur.r, cur.c);
        break;
      case ' ':
        if (cur.r >= added.length) p.onOpenRow?.(cur.r - added.length);
        break;
      case 'c':
        if (e.ctrlKey || e.metaKey) void navigator.clipboard?.writeText(cellText(valueAt(cur.r, cur.c))).catch(() => undefined);
        return;
      default:
        return;
    }
    e.preventDefault();
  };

  const tableW = 52 + widths.reduce((a, b) => a + b, 0);
  const sortOf = (name: string) => p.sort?.find((s) => s.column === name);

  return (
    <div className="db-grid" ref={box} tabIndex={0} role="grid" aria-label={p.label} aria-rowcount={total} onScroll={(e) => setTop(e.currentTarget.scrollTop)} onKeyDown={onKey}>
      <div className="db-grid-inner" style={{ width: tableW }}>
        <div className="db-grid-head" role="row">
          <div className="db-grid-rn" role="columnheader" aria-label="Row" />
          {p.columns.map((c, i) => {
            const s = sortOf(c.name);
            return (
              <div key={c.name + i} className="db-grid-th" role="columnheader" style={{ width: widths[i] }} aria-sort={s ? (s.desc ? 'descending' : 'ascending') : undefined}>
                <button type="button" className="db-grid-th-btn" onClick={() => p.onSort?.(c.name)} disabled={!p.onSort} title={c.type}>
                  <span className="db-grid-th-name">
                    {c.key === 'PRI' && <span className="db-pk" aria-label="primary key">PK</span>}
                    {c.name}
                    {s && <span className="db-sort-mark">{s.desc ? '↓' : '↑'}</span>}
                  </span>
                  {c.sub && <span className="db-grid-th-sub">{c.sub}</span>}
                </button>
              </div>
            );
          })}
        </div>
        <div style={{ height: total * ROW_H, position: 'relative' }}>
          {Array.from({ length: Math.max(0, to - from) }, (_, k) => {
            const r = from + k;
            const isAdded = r < added.length;
            const row = r - added.length;
            const del = !isAdded && p.deleted?.has(row);
            const edits = isAdded ? undefined : p.edits?.get(row);
            const sel = !isAdded && p.selected === row;
            return (
              <div
                key={r}
                role="row"
                className={`db-grid-row ${isAdded ? 'is-new' : ''} ${del ? 'is-del' : ''} ${sel ? 'is-sel' : ''}`}
                style={{ top: r * ROW_H }}
                onClick={() => !isAdded && p.onSelect?.(row)}
              >
                <div className="db-grid-rn" role="rowheader">
                  {!isAdded && p.onOpenRow ? (
                    <button type="button" className="db-rn-btn" aria-label={`Open row ${(p.first ?? 1) + row}`} title="Open the row" onClick={(e) => (e.stopPropagation(), p.onSelect?.(row), p.onOpenRow?.(row))}>
                      {(p.first ?? 1) + row}
                    </button>
                  ) : isAdded ? (
                    '+'
                  ) : (
                    (p.first ?? 1) + row
                  )}
                </div>
                {p.columns.map((c, i) => {
                  const v = valueAt(r, i);
                  const isEdited = !!edits && c.name in edits;
                  const isCur = cur?.r === r && cur?.c === i;
                  if (editing && editing.r === r && editing.c === i) {
                    return (
                      <div key={i} className="db-grid-td is-editing" role="gridcell" style={{ width: widths[i] }}>
                        <input
                          className="db-cell-input"
                          autoFocus
                          value={editing.v}
                          aria-label={`${c.name}, row ${r + 1}`}
                          onChange={(e) => setEditing({ ...editing, v: e.target.value })}
                          onBlur={commit}
                          onKeyDown={(e) => {
                            e.stopPropagation();
                            if (e.key === 'Enter') commit();
                            if (e.key === 'Escape') {
                              setEditing(null);
                              box.current?.focus();
                            }
                          }}
                        />
                      </div>
                    );
                  }
                  const text = cellText(v);
                  return (
                    <div
                      key={i}
                      role="gridcell"
                      className={`db-grid-td ${cellClass(v, c.type)} ${isEdited ? 'is-edited' : ''} ${isCur ? 'is-cur' : ''} ${c.fk && v !== null ? 'db-fk' : ''}`}
                      style={{ width: widths[i] }}
                      title={text.length > 40 ? text.slice(0, 2000) : undefined}
                      onMouseDown={() => setCur({ r, c: i })}
                      onDoubleClick={() => startEdit(r, i)}
                    >
                      {c.fk && v !== null && p.onFk ? (
                        <button type="button" className="db-fk-btn" onClick={(e) => (e.stopPropagation(), p.onFk?.(c.name, v as Cell))} title={`Open the row ${text} points to`}>
                          {text}
                        </button>
                      ) : (
                        text
                      )}
                      {typeof v === 'object' && v !== null && '$cut' in (v as object) && <span className="db-cut">…</span>}
                    </div>
                  );
                })}
              </div>
            );
          })}
        </div>
      </div>
      {total === 0 && <div className="db-empty">No rows</div>}
    </div>
  );
}
