// Moving credentials in and out. Bitwarden's unencrypted JSON both ways, so the way back stays
// open, and CSV from Bitwarden, Chrome and Firefox one way. Nothing here touches the network.

import { emptyItem, type CustomField, type ItemType, type VaultFolder, type VaultItem } from './model';

export interface ImportedFolder {
  /** Only for finding it from the items in the same file. */
  ref: string;
  name: string;
}

export interface ImportedItem {
  item: VaultItem;
  folderRef: string;
}

export interface Imported {
  source: string;
  folders: ImportedFolder[];
  items: ImportedItem[];
  /** Records of a kind that has nowhere to go here. */
  skipped: number;
}

const BW_TYPE: Record<number, ItemType> = { 1: 'login', 2: 'note', 3: 'card', 4: 'identity', 5: 'ssh' };
const BW_NUMBER: Record<ItemType, number> = { login: 1, note: 2, card: 3, identity: 4, ssh: 5 };
const CARD_KEYS = ['cardholderName', 'brand', 'number', 'expMonth', 'expYear', 'code'];
const IDENTITY_KEYS = ['title', 'firstName', 'middleName', 'lastName', 'username', 'company', 'email', 'phone', 'ssn', 'passportNumber', 'licenseNumber', 'address1', 'address2', 'address3', 'city', 'state', 'postalCode', 'country'];

type Obj = Record<string, unknown>;
const obj = (v: unknown): Obj => (v && typeof v === 'object' ? (v as Obj) : {});
const str = (v: unknown): string => (typeof v === 'string' ? v : typeof v === 'number' ? String(v) : '');
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);

function picked(source: unknown, keys: string[]): Record<string, string> {
  const o = obj(source);
  const out: Record<string, string> = {};
  for (const k of keys) if (str(o[k])) out[k] = str(o[k]);
  return out;
}

function fromBitwardenJson(text: string): Imported {
  let doc: Obj;
  try {
    doc = obj(JSON.parse(text));
  } catch {
    throw new Error('This file is not valid JSON.');
  }
  if (doc.encrypted === true) throw new Error('This is an encrypted export. In Bitwarden, export again as .json, not "encrypted".');
  if (!Array.isArray(doc.items)) throw new Error('This JSON has no items. Export it from Bitwarden as .json.');
  const folders = arr(doc.folders).map((f) => ({ ref: str(obj(f).id), name: str(obj(f).name) }));
  const items: ImportedItem[] = [];
  let skipped = 0;
  for (const raw of doc.items) {
    const o = obj(raw);
    const type = BW_TYPE[Number(o.type)];
    if (!type) {
      skipped++;
      continue;
    }
    const it = emptyItem(type, '');
    it.name = str(o.name) || 'Untitled';
    it.notes = str(o.notes);
    it.favorite = o.favorite === true;
    if (str(o.creationDate)) it.createdAt = str(o.creationDate);
    if (str(o.revisionDate)) it.updatedAt = str(o.revisionDate);
    if (str(o.deletedDate)) it.deletedAt = str(o.deletedDate);
    it.fields = arr(o.fields)
      .map((f): CustomField => ({ name: str(obj(f).name), value: str(obj(f).value), hidden: obj(f).type === 1 }))
      .filter((f) => f.name || f.value);
    it.history = arr(o.passwordHistory)
      .map((h) => ({ password: str(obj(h).password), at: str(obj(h).lastUsedDate) }))
      .filter((h) => h.password);
    // Ours, beside Bitwarden's format: an export without them would quietly lose the keys.
    const keys = arr(o.nextboardPasskeys)
      .map((k) => obj(k))
      .filter((k) => str(k.rpId) && str(k.credentialId) && str(k.secret))
      .map((k) => ({ rpId: str(k.rpId), credentialId: str(k.credentialId), userHandle: str(k.userHandle), userName: str(k.userName), secret: str(k.secret), createdAt: str(k.createdAt) }));
    if (keys.length) it.passkeys = keys;
    if (type === 'login') {
      const login = obj(o.login);
      it.values = picked(login, ['username', 'password', 'totp']);
      it.uris = arr(login.uris)
        .map((u) => str(obj(u).uri))
        .filter(Boolean);
    } else if (type === 'card') it.values = picked(o.card, CARD_KEYS);
    else if (type === 'identity') it.values = picked(o.identity, IDENTITY_KEYS);
    else if (type === 'ssh') {
      const key = obj(o.sshKey);
      it.values = { publicKey: str(key.publicKey), privateKey: str(key.privateKey), fingerprint: str(key.keyFingerprint) };
    }
    items.push({ item: it, folderRef: str(o.folderId) });
  }
  return { source: 'Bitwarden JSON', folders, items, skipped };
}

// ---- CSV ----

/** RFC 4180: quotes, doubled quotes, and line breaks inside quotes. */
export function parseCsv(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = '';
  let quoted = false;
  const s = text.replace(/^﻿/, '');
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (quoted) {
      if (c === '"' && s[i + 1] === '"') {
        cell += '"';
        i++;
      } else if (c === '"') quoted = false;
      else cell += c;
    } else if (c === '"') quoted = true;
    else if (c === ',') {
      row.push(cell);
      cell = '';
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && s[i + 1] === '\n') i++;
      row.push(cell);
      cell = '';
      if (row.some((v) => v !== '')) rows.push(row);
      row = [];
    } else cell += c;
  }
  row.push(cell);
  if (row.some((v) => v !== '')) rows.push(row);
  return rows;
}

function fromCsv(text: string): Imported {
  const rows = parseCsv(text);
  if (rows.length < 2) throw new Error('This CSV has no records.');
  const head = rows[0].map((h) => h.trim().toLowerCase());
  const col = (row: string[], ...names: string[]) => {
    for (const n of names) {
      const i = head.indexOf(n);
      if (i >= 0 && row[i]) return row[i];
    }
    return '';
  };
  const isBitwarden = head.includes('login_uri') || head.includes('login_password');
  if (!isBitwarden && !head.includes('password')) throw new Error('This CSV has no password column. Bitwarden, Chrome and Firefox exports work.');
  const names = new Map<string, string>();
  const items: ImportedItem[] = [];
  let skipped = 0;
  for (const row of rows.slice(1)) {
    const kind = col(row, 'type') || 'login';
    if (kind !== 'login' && kind !== 'note') {
      skipped++;
      continue;
    }
    const type: ItemType = kind === 'note' ? 'note' : 'login';
    const it = emptyItem(type, '');
    const uri = col(row, 'login_uri', 'url');
    it.name = col(row, 'name', 'title') || (uri ? uri.replace(/^https?:\/\//, '').split('/')[0] : 'Untitled');
    it.notes = col(row, 'notes', 'note');
    it.favorite = col(row, 'favorite') === '1';
    if (type === 'login') {
      it.values = { username: col(row, 'login_username', 'username'), password: col(row, 'login_password', 'password'), totp: col(row, 'login_totp') };
      it.uris = uri
        .split(',')
        .map((u) => u.trim())
        .filter(Boolean);
    }
    // Bitwarden writes custom fields as "name: value", one to a line.
    it.fields = col(row, 'fields')
      .split('\n')
      .filter(Boolean)
      .map((line) => {
        const at = line.indexOf(': ');
        return at < 0 ? { name: line, value: '', hidden: false } : { name: line.slice(0, at), value: line.slice(at + 2), hidden: false };
      });
    const folder = col(row, 'folder');
    if (folder) names.set(folder, folder);
    items.push({ item: it, folderRef: folder });
  }
  return { source: isBitwarden ? 'Bitwarden CSV' : 'CSV', folders: [...names.keys()].map((n) => ({ ref: n, name: n })), items, skipped };
}

export function parseImport(fileName: string, text: string): Imported {
  const out = /\.json$/i.test(fileName) || text.trimStart().startsWith('{') ? fromBitwardenJson(text) : fromCsv(text);
  if (!out.items.length) throw new Error('There is nothing to import in this file.');
  return out;
}

// ---- export ----

/** Bitwarden's unencrypted format: its importer takes this back. */
export function toBitwardenJson(items: VaultItem[], folders: VaultFolder[]): string {
  return JSON.stringify(
    {
      encrypted: false,
      folders: folders.map((f) => ({ id: f.id, name: f.name })),
      items: items.map((it) => {
        const base: Obj = {
          id: it.id,
          organizationId: null,
          folderId: it.folderId || null,
          type: BW_NUMBER[it.type],
          reprompt: 0,
          name: it.name,
          notes: it.notes || null,
          favorite: it.favorite,
          fields: it.fields.map((f) => ({ name: f.name, value: f.value, type: f.hidden ? 1 : 0, linkedId: null })),
          collectionIds: null,
          creationDate: it.createdAt,
          revisionDate: it.updatedAt,
          deletedDate: it.deletedAt ?? null,
        };
        if (it.history.length) base.passwordHistory = it.history.map((h) => ({ lastUsedDate: h.at, password: h.password }));
        if (it.passkeys?.length) base.nextboardPasskeys = it.passkeys;
        const v = it.values;
        if (it.type === 'login') base.login = { uris: it.uris.map((uri) => ({ match: null, uri })), username: v.username || null, password: v.password || null, totp: v.totp || null, fido2Credentials: [] };
        else if (it.type === 'note') base.secureNote = { type: 0 };
        else if (it.type === 'card') base.card = Object.fromEntries(CARD_KEYS.map((k) => [k, v[k] || null]));
        else if (it.type === 'identity') base.identity = Object.fromEntries(IDENTITY_KEYS.map((k) => [k, v[k] || null]));
        else if (it.type === 'ssh') base.sshKey = { privateKey: v.privateKey || '', publicKey: v.publicKey || '', keyFingerprint: v.fingerprint || '' };
        return base;
      }),
    },
    null,
    2,
  );
}

export function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'application/json' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
