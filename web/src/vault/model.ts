// What is inside a vault blob, and how it is shown. The types follow Bitwarden's, so that its
// exports come in whole and an export from here goes back.

import type { IconName } from '../components/Icon';

export const ITEM_TYPES = ['login', 'card', 'identity', 'note', 'ssh'] as const;
export type ItemType = (typeof ITEM_TYPES)[number];

export interface CustomField {
  name: string;
  value: string;
  hidden: boolean;
}

export interface PasswordUse {
  password: string;
  at: string;
}

export interface VaultItem {
  k: 'item';
  id: string;
  rev: number;
  type: ItemType;
  name: string;
  notes: string;
  favorite: boolean;
  folderId: string;
  /** The type's own fields, by the keys in TYPE_SCHEMA. */
  values: Record<string, string>;
  /** A login's addresses. */
  uris: string[];
  fields: CustomField[];
  /** Earlier passwords of a login, newest first. */
  history: PasswordUse[];
  createdAt: string;
  updatedAt: string;
  /** In the trash since. */
  deletedAt?: string;
}

export interface VaultFolder {
  k: 'folder';
  id: string;
  rev: number;
  name: string;
}

export type VaultRecord = VaultItem | VaultFolder;

export type FieldKind = 'text' | 'secret' | 'password' | 'totp' | 'multiline';

export interface FieldDef {
  key: string;
  label: string;
  kind?: FieldKind;
  mono?: boolean;
  /** Takes the whole row of the form. */
  wide?: boolean;
}

export interface TypeSchema {
  label: string;
  /** For "2 logins", and for the kind of item a list holds. */
  plural: string;
  icon: IconName;
  fields: FieldDef[];
}

export const TYPE_SCHEMA: Record<ItemType, TypeSchema> = {
  login: {
    label: 'Login',
    plural: 'Logins',
    icon: 'globe',
    fields: [
      { key: 'username', label: 'Username', mono: true, wide: true },
      { key: 'password', label: 'Password', kind: 'password', mono: true },
      { key: 'totp', label: 'Authenticator key', kind: 'totp', mono: true },
    ],
  },
  card: {
    label: 'Card',
    plural: 'Cards',
    icon: 'creditCard',
    fields: [
      { key: 'cardholderName', label: 'Cardholder name' },
      { key: 'brand', label: 'Brand' },
      { key: 'number', label: 'Number', kind: 'secret', mono: true },
      { key: 'code', label: 'Security code', kind: 'secret', mono: true },
      { key: 'expMonth', label: 'Expiry month', mono: true },
      { key: 'expYear', label: 'Expiry year', mono: true },
    ],
  },
  identity: {
    label: 'Identity',
    plural: 'Identities',
    icon: 'user',
    fields: [
      { key: 'title', label: 'Title' },
      { key: 'firstName', label: 'First name' },
      { key: 'middleName', label: 'Middle name' },
      { key: 'lastName', label: 'Last name' },
      { key: 'username', label: 'Username', mono: true },
      { key: 'company', label: 'Company' },
      { key: 'email', label: 'Email', mono: true },
      { key: 'phone', label: 'Phone', mono: true },
      { key: 'ssn', label: 'National ID', kind: 'secret', mono: true },
      { key: 'passportNumber', label: 'Passport number', kind: 'secret', mono: true },
      { key: 'licenseNumber', label: 'License number', kind: 'secret', mono: true },
      { key: 'address1', label: 'Address' },
      { key: 'address2', label: 'Address 2' },
      { key: 'address3', label: 'Address 3' },
      { key: 'city', label: 'City' },
      { key: 'state', label: 'State or province' },
      { key: 'postalCode', label: 'Postal code', mono: true },
      { key: 'country', label: 'Country' },
    ],
  },
  note: { label: 'Secure note', plural: 'Secure notes', icon: 'note', fields: [] },
  ssh: {
    label: 'SSH key',
    plural: 'SSH keys',
    icon: 'terminal',
    fields: [
      { key: 'publicKey', label: 'Public key', kind: 'multiline', mono: true },
      { key: 'fingerprint', label: 'Fingerprint', mono: true, wide: true },
      { key: 'privateKey', label: 'Private key', kind: 'multiline', mono: true },
    ],
  },
};

/** A multiline field holding something secret: masked like any other. */
export const isSecretField = (type: ItemType, f: FieldDef) => f.kind === 'secret' || f.kind === 'password' || f.kind === 'totp' || (type === 'ssh' && f.key === 'privateKey');

/** How long an item stays in the trash, as in Bitwarden. */
export const TRASH_DAYS = 30;
/** Earlier passwords kept per login. */
export const HISTORY_MAX = 5;

export function emptyItem(type: ItemType, id: string, folderId = ''): VaultItem {
  const now = new Date().toISOString();
  return { k: 'item', id, rev: 0, type, name: '', notes: '', favorite: false, folderId, values: {}, uris: type === 'login' ? [''] : [], fields: [], history: [], createdAt: now, updatedAt: now };
}

const str = (v: unknown) => (typeof v === 'string' ? v : '');

/** What a blob holds may come from an older version: never trust its shape. */
export function readRecord(raw: unknown, id: string, rev: number): VaultRecord | null {
  const o = raw && typeof raw === 'object' ? (raw as Record<string, unknown>) : null;
  if (!o) return null;
  if (o.k === 'folder') return { k: 'folder', id, rev, name: str(o.name) };
  if (o.k !== 'item') return null;
  const type = (ITEM_TYPES as readonly unknown[]).includes(o.type) ? (o.type as ItemType) : 'note';
  const values: Record<string, string> = {};
  if (o.values && typeof o.values === 'object') for (const [k, v] of Object.entries(o.values)) if (typeof v === 'string') values[k] = v;
  const list = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.filter((x) => x && typeof x === 'object') : []);
  return {
    k: 'item',
    id,
    rev,
    type,
    name: str(o.name),
    notes: str(o.notes),
    favorite: o.favorite === true,
    folderId: str(o.folderId),
    values,
    uris: Array.isArray(o.uris) ? o.uris.filter((u): u is string => typeof u === 'string') : [],
    fields: list(o.fields).map((f) => ({ name: str(f.name), value: str(f.value), hidden: f.hidden === true })),
    history: list(o.history).map((h) => ({ password: str(h.password), at: str(h.at) })),
    createdAt: str(o.createdAt),
    updatedAt: str(o.updatedAt),
    deletedAt: typeof o.deletedAt === 'string' ? o.deletedAt : undefined,
  };
}

/** Second line of a list row: never anything secret. */
export function itemSubtitle(it: VaultItem): string {
  const v = it.values;
  switch (it.type) {
    case 'login':
      return v.username || hostOf(it.uris[0] ?? '');
    case 'card': {
      const digits = (v.number ?? '').replace(/\D/g, '');
      const last = digits.length >= 4 ? `•••• ${digits.slice(-4)}` : '';
      return [v.brand, last].filter(Boolean).join(' ');
    }
    case 'identity':
      return [v.firstName, v.lastName].filter(Boolean).join(' ') || v.email || '';
    case 'ssh':
      return v.fingerprint ?? '';
    default:
      return '';
  }
}

export function hostOf(uri: string): string {
  const href = safeHref(uri);
  if (!href) return uri;
  try {
    return new URL(href).host;
  } catch {
    return uri;
  }
}

/**
 * A link for an address, or null. Only http and https ever become a link: an imported
 * `javascript:` address must stay text.
 */
export function safeHref(uri: string): string | null {
  const v = uri.trim();
  if (!v) return null;
  try {
    const u = new URL(/^[a-z][a-z0-9+.-]*:\/\//i.test(v) ? v : `https://${v}`);
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : null;
  } catch {
    return null;
  }
}

// ---- which items a card shows ----

/** all | favorites | trash | type:<type> | folder:<id>, and folder: is "no folder". */
export type NavKey = string;

export function inNav(it: VaultItem, nav: NavKey): boolean {
  if (nav === 'trash') return !!it.deletedAt;
  if (it.deletedAt) return false;
  if (nav === 'all') return true;
  if (nav === 'favorites') return it.favorite;
  if (nav.startsWith('type:')) return it.type === nav.slice(5);
  if (nav.startsWith('folder:')) return it.folderId === nav.slice(7);
  return false;
}

/** Search looks at what you would recognise an item by, and never at a secret. */
export function matchesQuery(it: VaultItem, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  const hay = [it.name, it.notes, it.values.username, it.values.email, it.values.brand, it.values.company, it.values.cardholderName, ...it.uris, ...it.fields.map((f) => f.name)];
  return hay.some((s) => s?.toLowerCase().includes(q));
}

export const byName = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' });

// ---- filling (FillDialog) ----

const LOCAL = new Set(['localhost', '127.0.0.1', '[::1]', '::1', '0.0.0.0']);

/** Is this login saved for this site? A subdomain counts, the other way round does not. */
export function matchesSite(it: VaultItem, host: string, port: number): boolean {
  const page = host.toLowerCase();
  return it.uris.some((uri) => {
    const href = safeHref(uri);
    if (!href) return false;
    const u = new URL(href);
    const saved = u.hostname.toLowerCase();
    if (u.port && Number(u.port) !== port) return false;
    if (LOCAL.has(saved) && LOCAL.has(page)) return true;
    return page === saved || page.endsWith(`.${saved}`);
  });
}

/** A hint only, for sorting: a window's title proves nothing about the site in it. */
export function matchesTitle(it: VaultItem, title: string): boolean {
  const t = title.toLowerCase();
  const words = [it.name, ...it.uris.map((u) => hostOf(u).split('.').slice(-2, -1)[0] ?? '')];
  return words.some((w) => w.length >= 3 && t.includes(w.toLowerCase()));
}

export const isFillable = (it: VaultItem) => it.type === 'login' && !it.deletedAt;
