import { describe, expect, it } from 'vitest';
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
