// The grid of the database browser (mockup 1): a table's rows or a query's result. Rows are
// windowed, so a page of thousands scrolls smoothly. Values read by their kind: numbers right and
// grouped, text in the string colour, a foreign key in blue that opens its row, an enum as a pill,
// NULL quiet. In a table view cells edit in place (double-click or Enter); edits stay pending,
// coloured, until the view commits them.

import { useEffect, useMemo, useRef, useState, type KeyboardEvent as RKeyboardEvent, type ReactNode } from 'react';
import type { Cell } from './types';

export interface GridColumn {
  name: string;
  type?: string;
  /** under the name: "bigint · PK", "bigint → users", "varchar(255) · null" */
  sub?: string;
  /** a foreign key: its values link to the row they point to */
  fk?: boolean;
  key?: string;
  /** an enum's values: its cells are pills */
  values?: string[];
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

const ROW_H = 29;

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

export type CellKind = 'null' | 'num' | 'str' | 'enum' | 'fk' | 'plain' | 'special';

const NUM_TYPE = /\b(tiny|small|medium|big)?int(eger)?\d*\b|serial|decimal|numeric|float|double|real|money/i;
const STR_TYPE = /char|text|citext|uuid|inet|cidr|macaddr|name\b|enum|set\(/i;

/** A column type that holds numbers. */
export const isNumType = (t: string | undefined) => !!t && NUM_TYPE.test(t) && !/point|interval/i.test(t);

/** How a cell reads: by its column's type, else by its value. */
export function cellKind(v: unknown, col: Pick<GridColumn, 'type' | 'fk' | 'values'>): CellKind {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'object') return 'special';
  if (col.fk) return 'fk';
  if (col.values?.length || /^enum\b/i.test(col.type ?? '')) return 'enum';
  if (typeof v === 'number' || (col.type && NUM_TYPE.test(col.type) && !/point|interval/i.test(col.type))) return 'num';
  if (typeof v === 'boolean') return 'plain';
  if (col.type && STR_TYPE.test(col.type)) return 'str';
  return 'plain';
}

const GROUP = new Intl.NumberFormat(undefined, { useGrouping: true, maximumFractionDigits: 0 });
const DECIMAL = (1.5).toLocaleString(undefined).charAt(1);

/** A number as it reads: its integer part grouped (big ones too), its decimals as the database gives them. */
export function numText(v: Cell | unknown): string {
  const s = cellText(v);
  const m = /^(-?)(\d+)(?:\.(\d+))?$/.exec(s);
  if (!m) return s;
  let int: string;
  try {
    int = GROUP.format(BigInt(m[2]));
  } catch {
    int = m[2];
  }
  return `${m[1]}${int}${m[3] !== undefined ? DECIMAL + m[3] : ''}`;
}

// the words statuses use, by what they mean: done, waiting, back, failed, off
const TONES: [RegExp, string][] = [
  [/^(paid|active|success|succeeded|done|completed?|delivered|approved|enabled|published|verified|ok|open|online|yes|true|settled|confirmed)$/i, 'ok'],
  [/^(pending|processing|waiting|queued|draft|review|in[_ -]?progress|unpaid|hold|on[_ -]?hold|partial|running|scheduled)$/i, 'warn'],
  [/^(refunded|returned|archived|reversed|chargeback)$/i, 'violet'],
  [/^(failed|error|rejected|declined|expired|blocked|banned|overdue|deleted|fraud)$/i, 'err'],
  [/^(cancell?ed|inactive|disabled|closed|no|false|unknown|void|offline)$/i, 'off'],
];
const HUES = ['info', 'cyan', 'violet', 'warn', 'ok'];

export function pillTone(v: string): string {
  for (const [re, tone] of TONES) if (re.test(v)) return tone;
  let h = 0;
  for (let i = 0; i < v.length; i++) h = (h * 31 + v.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

function widthOf(col: GridColumn, rows: Cell[][], i: number): number {
  let chars = Math.max(col.name.length * 1.05, (col.sub ?? '').length * 0.86);
  for (let r = 0; r < Math.min(rows.length, 60); r++) chars = Math.max(chars, Math.min(44, cellText(rows[r][i]).length * 1.04));
  return Math.round(Math.min(360, Math.max(64, chars * 7.4 + 22)));
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
    if (editing || !cur) return;
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

  const tableW = 46 + widths.reduce((a, b) => a + b, 0);
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
              <div key={r} role="row" className={`db-grid-row ${isAdded ? 'is-new' : ''} ${del ? 'is-del' : ''} ${sel ? 'is-sel' : ''}`} style={{ top: r * ROW_H }} onClick={() => !isAdded && p.onSelect?.(row)}>
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
                  const kind = isAdded && v === null ? 'null' : cellKind(v, c);
                  const text = cellText(v);
                  // amounts grouped; a key (an id) as it is
                  let content: ReactNode = kind === 'num' && c.key !== 'PRI' && !/^(id|.*_id)$/i.test(c.name) ? numText(v) : text;
                  if (kind === 'enum') content = <span className={`db-pill tone-${pillTone(text)}`}>{text}</span>;
                  if (kind === 'fk' && p.onFk)
                    content = (
                      <button type="button" className="db-fk-btn" onClick={(e) => (e.stopPropagation(), p.onFk?.(c.name, v as Cell))} title={`Open the row ${text} points to`}>
                        {text}
                      </button>
                    );
                  return (
                    <div
                      key={i}
                      role="gridcell"
                      className={`db-grid-td k-${kind} ${isEdited ? 'is-edited' : ''} ${isCur ? 'is-cur' : ''}`}
                      style={{ width: widths[i] }}
                      title={text.length > 40 ? text.slice(0, 2000) : undefined}
                      onMouseDown={() => setCur({ r, c: i })}
                      onDoubleClick={() => startEdit(r, i)}
                    >
                      {isAdded && v === null && c.key === 'PRI' ? <span className="db-auto">auto</span> : content}
                      {typeof v === 'object' && v !== null && '$cut' in (v as object) && <span className="db-cut">…</span>}
                    </div>
                  );
                })}
              </div>
            );
          })}
        </div>
      </div>
      {total === 0 && <div className="db-empty db-grid-empty">No rows</div>}
    </div>
  );
}
