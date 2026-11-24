// What a database tab of the Code view shows (docs/API-db.md): a table, an SQL editor or a Redis
// connection's keys, as the argument of a "db" tool tab. Pure, so the tab list and tests can read
// it: "table:<conn>:<schema>:<table>[:<filter>]", "query:<conn>:<id>", "keys:<conn>[:<db>]". The SQL of a
// new editor waits in this browser under its id (SqlEditor), never in the tab's argument.

import type { IconName } from '../components/Icon';
import type { Filter } from './types';

export type DbTarget =
  | { kind: 'table'; conn: string; schema: string; table: string; filter?: Filter }
  | { kind: 'query'; conn: string; id: string }
  | { kind: 'keys'; conn: string; db?: number }
  | { kind: 'home' };

const enc = encodeURIComponent;
const dec = (s: string | undefined) => {
  try {
    return decodeURIComponent(s ?? '');
  } catch {
    return '';
  }
};

export function dbArg(t: DbTarget): string {
  switch (t.kind) {
    case 'table':
      return `table:${enc(t.conn)}:${enc(t.schema)}:${enc(t.table)}${t.filter ? `:${enc(JSON.stringify(t.filter))}` : ''}`;
    case 'query':
      return `query:${enc(t.conn)}:${enc(t.id)}`;
    case 'keys':
      return `keys:${enc(t.conn)}${t.db !== undefined ? `:${t.db}` : ''}`;
    default:
      return '';
  }
}

export function parseDbArg(arg: string | undefined): DbTarget {
  const p = (arg ?? '').split(':');
  if (p[0] === 'table' && p[1] && p[3]) {
    let filter: Filter | undefined;
    if (p[4]) {
      try {
        const f = JSON.parse(dec(p[4])) as Filter;
        if (f && typeof f.column === 'string' && typeof f.op === 'string') filter = f;
      } catch {
        /* a filter that does not read: the table without it */
      }
    }
    return { kind: 'table', conn: dec(p[1]), schema: dec(p[2]), table: dec(p[3]), ...(filter ? { filter } : {}) };
  }
  if (p[0] === 'query' && p[1] && p[2]) return { kind: 'query', conn: dec(p[1]), id: dec(p[2]) };
  if (p[0] === 'keys' && p[1]) {
    const db = p[2] === undefined || p[2] === '' ? NaN : Number(p[2]);
    return { kind: 'keys', conn: dec(p[1]), ...(Number.isInteger(db) && db >= 0 ? { db } : {}) };
  }
  return { kind: 'home' };
}

/** A new SQL editor's id ("n3-k2x8a1"): its number names the tab, its text is kept in this browser under it. */
export const newQueryId = (n: number) => `n${n}-${Math.random().toString(36).slice(2, 8)}`;

const TEXT = (id: string) => `nb.db.q.${id}`;

/** An editor's text, as this browser keeps it. */
export function queryText(id: string): string {
  try {
    return localStorage.getItem(TEXT(id)) ?? '';
  } catch {
    return '';
  }
}

/** Keep an editor's text in this browser (a new editor's first SQL is put here before it opens). */
export function putQueryText(id: string, text: string) {
  try {
    localStorage.setItem(TEXT(id), text);
  } catch {
    /* storage blocked: the text lives in this page */
  }
}

// titles the tabs learn from what they show (a saved query's name, a connection's), as toolMeta
// is pure and knows no store
const titles = new Map<string, string>();

/** Keep a tab's title; true when it changed. */
export function rememberDbTitle(arg: string, title: string): boolean {
  if (!title || titles.get(arg) === title) return false;
  titles.set(arg, title);
  return true;
}

/** A database tab's title and icon: the table, the query's name, "<connection> · keys". */
export function dbTabMeta(arg: string | undefined): { title: string; icon: IconName; tip: string } {
  const t = parseDbArg(arg);
  const known = arg ? titles.get(arg) : undefined;
  switch (t.kind) {
    case 'table': {
      const f = t.filter ? ` · ${t.filter.column} ${t.filter.op} ${t.filter.value ?? ''}`.trimEnd() : '';
      return { title: known ?? `${t.table}${f}`, icon: 'table', tip: `${t.schema ? `${t.schema}.` : ''}${t.table}${f}` };
    }
    case 'query': {
      const n = /^n(\d+)-/.exec(t.id)?.[1];
      const title = known ?? (n ? `SQL ${n}` : 'SQL');
      return { title, icon: 'file', tip: title };
    }
    case 'keys':
      return { title: known ?? 'keys', icon: 'database', tip: known ?? 'Redis keys' };
    default:
      return { title: 'Databases', icon: 'database', tip: 'Databases' };
  }
}
