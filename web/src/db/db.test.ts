import { describe, expect, it } from 'vitest';
import { outgoing, tunnelProblem } from './ConnectionDialog';
import { cellKind, numText, pillTone } from './DataGrid';
import { dbArg, dbTabMeta, newQueryId, parseDbArg, rememberDbTitle } from './tabs';
import { cellText } from './DataGrid';
import { ttlText } from './RedisView';
import { sqlBlocks } from './SmartPanel';
import { statementsOf } from './SqlEditor';
import { blankConnection, defaultSecurity, driverOf, isKV, isSQL } from './types';

describe('statementsOf', () => {
  it('splits on semicolons outside quotes and comments, with offsets', () => {
    const text = "SELECT 1;\nSELECT ';' AS a; -- x;\nSELECT 3";
    const got = statementsOf(text, false);
    expect(got.map((s) => s.sql)).toEqual(['SELECT 1', "SELECT ';' AS a", '-- x;\nSELECT 3']);
    expect(text.slice(got[1].from, got[1].to)).toBe("SELECT ';' AS a");
  });

  it('keeps a PostgreSQL function body in dollar quotes whole', () => {
    const text = 'CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql; SELECT f()';
    expect(statementsOf(text, true).map((s) => s.sql)).toEqual(['CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql', 'SELECT f()']);
  });

  it('treats backticks as quotes in MySQL only, and drops empty statements', () => {
    expect(statementsOf('SELECT `a;b` FROM t;;  ;', false).map((s) => s.sql)).toEqual(['SELECT `a;b` FROM t']);
    expect(statementsOf('/* only a comment */', false)).toEqual([]);
  });
});

describe('cellText', () => {
  it('shows NULL, cut text, binary and JSON as the grid does', () => {
    expect(cellText(null)).toBe('NULL');
    expect(cellText(42)).toBe('42');
    expect(cellText({ $cut: 'abc', len: 100000 })).toBe('abc');
    expect(cellText({ $bin: '0a0b', len: 2 })).toBe('0x0a0b');
    expect(cellText({ $bin: '0a0b', len: 900 })).toBe('0x0a0b…');
    expect(cellText({ a: 1 })).toBe('{"a":1}');
  });
});

describe('ttlText', () => {
  it('reads a key’s time to live in the largest unit that fits', () => {
    expect(ttlText(-1)).toBe('∞');
    expect(ttlText(42_000)).toBe('42s');
    expect(ttlText(5 * 60_000)).toBe('5m');
    expect(ttlText((2 * 60 + 5) * 60_000)).toBe('2h 5m');
    expect(ttlText(3 * 24 * 3600_000)).toBe('3d');
  });
});

describe('sqlBlocks', () => {
  it('finds the SQL of an answer, to open as a query', () => {
    const text = 'Revenue fell because…\n```sql\nSELECT 1;\n```\nand\n```\nSELECT 2\n```';
    expect(sqlBlocks(text)).toEqual(['SELECT 1;', 'SELECT 2']);
  });
});

describe('connections', () => {
  it('defaults Production to read-only, the others to full access', () => {
    expect(defaultSecurity('production').readOnly).toBe(true);
    expect(defaultSecurity('local').readOnly).toBe(false);
    expect(defaultSecurity('staging').confirmWrites).toBe(true);
  });

  it('starts a new connection as the driver’s defaults', () => {
    const c = blankConnection('p1', 'postgres');
    expect(c.projectId).toBe('p1');
    expect(c.driver).toBe('postgres');
    expect(c.port).toBe(driverOf('postgres').port);
    expect(c.reach.kind).toBe('direct');
    expect(isSQL('postgres') && isKV('valkey') && !isKV('mysql')).toBe(true);
  });
});

describe('the tunnel a connection dialog sends', () => {
  const base = () => blankConnection('p1', 'mysql');
  const form = (c: ReturnType<typeof blankConnection>) => ({ c, hopSecrets: [null], hopKeys: [null] });

  it('sends no SSH server when a Host of ~/.ssh/config or a machine is the way', () => {
    const c = base();
    c.reach = { ...c.reach, kind: 'ssh', configHost: 'db-bastion', hops: [{ host: '', port: 22, user: '', auth: 'key' }] };
    expect(outgoing(form(c)).c.reach.hops).toEqual([]);
    expect(tunnelProblem(c)).toBe('');
    const m = base();
    m.reach = { ...m.reach, kind: 'machine', machineId: 'm1', hops: [{ host: '', port: 22, user: '', auth: 'key' }] };
    expect(outgoing(form(m)).c.reach.hops).toEqual([]);
  });

  it('says which server is missing its host or user, before anything is sent', () => {
    const c = base();
    c.reach = { ...c.reach, kind: 'ssh', hops: [{ host: '', port: 22, user: 'deploy', auth: 'agent' }, { host: 'db-1', port: 22, user: '', auth: 'key' }] };
    expect(tunnelProblem(c)).toBe('SSH tunnel: fill in the host and user name of jump server 1, or untick Use SSH tunnel.');
    c.reach.hops![0].host = 'bastion';
    expect(tunnelProblem(c)).toBe('SSH tunnel: fill in the host and user name of the SSH server, or untick Use SSH tunnel.');
    c.reach.hops![1].user = 'deploy';
    expect(tunnelProblem(c)).toBe('');
    expect(outgoing(form(c)).c.reach.hops).toHaveLength(2);
  });
});

describe('database tabs of the Code view', () => {
  it('reads back what it writes: a table with its filter, a query, a Redis database', () => {
    const t = { kind: 'table' as const, conn: 'db_1', schema: 'public', table: 'or:ders', filter: { column: 'id', op: '=', value: '18' } };
    expect(parseDbArg(dbArg(t))).toEqual(t);
    expect(parseDbArg(dbArg({ kind: 'query', conn: 'db_1', id: 'n2-abc' }))).toEqual({ kind: 'query', conn: 'db_1', id: 'n2-abc' });
    expect(parseDbArg(dbArg({ kind: 'keys', conn: 'db_r', db: 0 }))).toEqual({ kind: 'keys', conn: 'db_r', db: 0 });
    expect(parseDbArg(dbArg({ kind: 'keys', conn: 'db_r' }))).toEqual({ kind: 'keys', conn: 'db_r' });
    expect(parseDbArg('')).toEqual({ kind: 'home' });
    expect(parseDbArg('table:x')).toEqual({ kind: 'home' });
  });

  it('names a tab after its table, its query, else what the tab learned', () => {
    expect(dbTabMeta(dbArg({ kind: 'table', conn: 'c', schema: 's', table: 'orders' }))).toMatchObject({ title: 'orders', icon: 'table' });
    const q = dbArg({ kind: 'query', conn: 'c', id: newQueryId(3) });
    expect(dbTabMeta(q).title).toBe('SQL 3');
    expect(rememberDbTitle(q, 'paid per day.sql')).toBe(true);
    expect(rememberDbTitle(q, 'paid per day.sql')).toBe(false);
    expect(dbTabMeta(q).title).toBe('paid per day.sql');
    expect(dbTabMeta(undefined).title).toBe('Databases');
  });
});

describe('the grid reads values by their kind', () => {
  it('knows numbers, text, keys, enums and NULL', () => {
    expect(cellKind(null, {})).toBe('null');
    expect(cellKind('12.50', { type: 'decimal(12,2)' })).toBe('num');
    expect(cellKind('IDR', { type: 'char(3)' })).toBe('str');
    expect(cellKind('77', { type: 'bigint', fk: true })).toBe('fk');
    expect(cellKind('paid', { type: "enum('paid','pending')", values: ['paid', 'pending'] })).toBe('enum');
    expect(cellKind('2026-11-23 09:12:44', { type: 'timestamp' })).toBe('plain');
  });

  it('groups a number and keeps its decimals, big ones too', () => {
    const g = numText('1120000.50');
    expect(g.replace(/\D/g, '')).toBe('112000050');
    expect(g.length).toBeGreaterThan('1120000.50'.length);
    expect(numText('-5')).toBe('-5');
    expect(numText('12345678901234567890').replace(/\D/g, '')).toBe('12345678901234567890');
    expect(numText('abc')).toBe('abc');
  });

  it('colours a status by what it means', () => {
    expect(pillTone('paid')).toBe('ok');
    expect(pillTone('Pending')).toBe('warn');
    expect(pillTone('refunded')).toBe('violet');
    expect(pillTone('failed')).toBe('err');
    expect(pillTone('cancelled')).toBe('off');
    expect(pillTone('whatever')).toBe(pillTone('whatever'));
  });
});
