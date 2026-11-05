import { describe, expect, it } from 'vitest';
import { emptyItem, hostOf, inNav, isFillable, itemSubtitle, matchesQuery, matchesSite, readRecord, safeHref, type VaultItem } from './model';

function login(uris: string[], extra: Partial<VaultItem> = {}): VaultItem {
  return { ...emptyItem('login', 'id1'), name: 'Mail', uris, ...extra };
}

describe('safeHref', () => {
  it.each(['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'javascript://example.com/%0aalert(1)', 'data:text/html,<script>alert(1)</script>', 'file:///etc/passwd', 'vbscript:msgbox(1)', 'ftp://example.com/x', '', '   '])(
    'keeps %s from becoming a link',
    (uri) => {
      expect(safeHref(uri)).toBeNull();
    },
  );

  it('makes http and https links, and takes a bare host for https', () => {
    expect(safeHref('https://example.com/a')).toBe('https://example.com/a');
    expect(safeHref('http://example.com')).toBe('http://example.com/');
    expect(safeHref(' example.com ')).toBe('https://example.com/');
    expect(hostOf('https://example.com:8443/a')).toBe('example.com:8443');
    expect(hostOf('javascript:alert(1)')).toBe('javascript:alert(1)');
  });
});

// A password filled into the wrong site is what the vault exists to prevent.
describe('matchesSite', () => {
  const m = (uri: string, host: string, port = 443) => matchesSite(login([uri]), host, port);

  it('takes the saved site and its subdomains', () => {
    expect(m('https://example.com', 'example.com')).toBe(true);
    expect(m('https://example.com', 'EXAMPLE.com')).toBe(true);
    expect(m('example.com', 'login.example.com')).toBe(true);
  });

  it.each([
    ['a name that ends the same', 'https://example.com', 'evilexample.com'],
    ['the saved name inside a longer one', 'https://example.com', 'example.com.evil.net'],
    ['the parent of a saved subdomain', 'https://login.example.com', 'example.com'],
    ['a sibling of a saved subdomain', 'https://login.example.com', 'other.example.com'],
    ['another site', 'https://example.com', 'example.org'],
    ['a saved address that is no site', 'javascript:alert(1)', 'example.com'],
    ['a local name for a real site', 'https://localhost', 'example.com'],
  ])('refuses %s', (_name, uri, host) => {
    expect(m(uri, host)).toBe(false);
  });

  it('respects a saved port', () => {
    expect(m('https://example.com:8443', 'example.com', 8443)).toBe(true);
    expect(m('https://example.com:8443', 'example.com', 443)).toBe(false);
  });

  it('treats the names of this machine as one', () => {
    expect(m('http://localhost:3000', '127.0.0.1', 3000)).toBe(true);
    expect(m('http://127.0.0.1:3000', 'localhost', 3000)).toBe(true);
    expect(m('http://localhost:3000', 'localhost', 4000)).toBe(false);
  });

  it('looks at every address of a login', () => {
    expect(matchesSite(login(['https://a.example', 'https://b.example']), 'b.example', 443)).toBe(true);
    expect(matchesSite(login([]), 'b.example', 443)).toBe(false);
  });
});

describe('what the lists show', () => {
  const secretLogin = login(['https://example.com'], {
    values: { username: 'alice', password: 'hunter2', totp: 'GEZDGNBV' },
    history: [{ password: 'oldpass', at: '' }],
    fields: [{ name: 'recovery', value: 'seedwords', hidden: true }],
  });

  it('search never looks inside a secret', () => {
    for (const q of ['hunter2', 'GEZDGNBV', 'oldpass', 'seedwords']) expect(matchesQuery(secretLogin, q)).toBe(false);
    for (const q of ['alice', 'mail', 'EXAMPLE', 'recovery', '']) expect(matchesQuery(secretLogin, q)).toBe(true);
  });

  it('the second line of a row holds nothing secret', () => {
    expect(itemSubtitle(secretLogin)).toBe('alice');
    const card = { ...emptyItem('card', 'c'), values: { brand: 'Visa', number: '4111 1111 1111 1234', code: '999' } };
    expect(itemSubtitle(card)).toBe('Visa •••• 1234');
    expect(itemSubtitle(card)).not.toContain('4111');
    expect(itemSubtitle({ ...emptyItem('note', 'n'), notes: 'private text' })).toBe('');
  });

  it('the trash keeps to itself', () => {
    const gone = login([], { deletedAt: '2026-01-01', favorite: true });
    expect(inNav(gone, 'trash')).toBe(true);
    for (const nav of ['all', 'favorites', 'type:login', 'folder:']) expect(inNav(gone, nav)).toBe(false);
    expect(isFillable(gone)).toBe(false);
    expect(isFillable(login([]))).toBe(true);
    expect(isFillable(emptyItem('note', 'n'))).toBe(false);
  });
});

describe('readRecord', () => {
  it('trusts nothing about a stored shape', () => {
    for (const raw of [null, 1, 'x', [], {}, { k: 'other' }]) expect(readRecord(raw, 'id', 1)).toBeNull();
    const r = readRecord({ k: 'item', type: 'nonsense', name: 5, values: { a: 'ok', b: 7 }, uris: ['u', 3], fields: [null, { name: 'n', hidden: 'yes' }], history: 'no' }, 'id', 2) as VaultItem;
    expect(r).toMatchObject({ id: 'id', rev: 2, type: 'note', name: '', values: { a: 'ok' }, uris: ['u'], history: [] });
    expect(r.fields).toEqual([{ name: 'n', value: '', hidden: false }]);
  });

  it('takes the record id from the hub, not from the blob', () => {
    expect(readRecord({ k: 'item', id: 'other', type: 'note' }, 'id', 1)?.id).toBe('id');
    expect(readRecord({ k: 'folder', id: 'other', name: 'F' }, 'id', 1)).toEqual({ k: 'folder', id: 'id', rev: 1, name: 'F' });
  });
});
