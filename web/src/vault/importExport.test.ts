import { describe, expect, it } from 'vitest';
import { emptyItem } from './model';
import { parseCsv, parseImport, toBitwardenJson } from './importExport';

describe('parseCsv', () => {
  it('reads RFC 4180', () => {
    expect(parseCsv('a,b\n1,2\n')).toEqual([['a', 'b'], ['1', '2']]);
    expect(parseCsv('"x,y","say ""hi""","two\nlines"\r\nlast,,')).toEqual([['x,y', 'say "hi"', 'two\nlines'], ['last', '', '']]);
    expect(parseCsv('﻿a,b')).toEqual([['a', 'b']]);
  });
});

describe('parseImport', () => {
  it('refuses what it cannot read, and says so', () => {
    expect(() => parseImport('x.json', '{nope')).toThrow('not valid JSON');
    expect(() => parseImport('x.json', '{"encrypted":true,"items":[]}')).toThrow('encrypted export');
    expect(() => parseImport('x.json', '{"folders":[]}')).toThrow('no items');
    expect(() => parseImport('x.json', '{"items":[]}')).toThrow('nothing to import');
  });

  it('counts the kinds it has no place for', () => {
    const out = parseImport('x.json', JSON.stringify({ items: [{ type: 1, name: 'a', login: {} }, { type: 99, name: 'b' }] }));
    expect(out.items).toHaveLength(1);
    expect(out.skipped).toBe(1);
  });

  it('reads a Bitwarden CSV', () => {
    const csv = 'folder,favorite,type,name,notes,fields,reprompt,login_uri,login_username,login_password,login_totp\nWork,1,login,Mail,hi,,0,https://example.com,alice,hunter2,\n';
    const out = parseImport('bw.csv', csv);
    expect(out.source).toBe('Bitwarden CSV');
    expect(out.folders.map((f) => f.name)).toEqual(['Work']);
    expect(JSON.stringify(out.items[0])).toContain('hunter2');
    expect(JSON.stringify(out.items[0])).toContain('https://example.com');
  });
});

describe('Bitwarden JSON', () => {
  it('comes back the way it went out', () => {
    const login = {
      ...emptyItem('login', 'id1', 'f1'),
      name: 'Mail',
      notes: 'n',
      favorite: true,
      uris: ['https://example.com'],
      values: { username: 'alice', password: 'hunter2', totp: 'GEZDGNBV' },
      fields: [{ name: 'pin', value: '42', hidden: true }],
      history: [{ password: 'old', at: '2026-01-01T00:00:00.000Z' }],
    };
    const card = { ...emptyItem('card', 'id2'), name: 'Visa', values: { number: '4111', code: '999' } };
    const note = { ...emptyItem('note', 'id3'), name: 'Note', notes: 'text' };
    const text = toBitwardenJson([login, card, note], [{ k: 'folder', id: 'f1', rev: 0, name: 'Work' }]);
    const doc = JSON.parse(text);
    expect(doc.encrypted).toBe(false);
    expect(doc.items.map((i: { type: number }) => i.type)).toEqual([1, 3, 2]);

    const back = parseImport('x.json', text);
    expect(back.skipped).toBe(0);
    expect(back.folders).toEqual([{ ref: 'f1', name: 'Work' }]);
    const raw = JSON.stringify(back.items);
    for (const want of ['hunter2', 'alice', 'GEZDGNBV', 'https://example.com', '"pin"', '4111', '999', 'old']) expect(raw).toContain(want);
  });
});
