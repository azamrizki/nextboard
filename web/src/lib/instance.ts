// This hub among the instances of its machine, as the web sees it (multi-instance design §1
// #19–#20, §3, §4.2): page titles that say which instance a tab is, the storage guard for an
// origin another hub had before, and the links and checks of New instance….

// ---- titles ------------------------------------------------------------------------------

let named = '';
let hubName = '';

/**
 * Set at boot from /api/setup/status: the pages of a named instance end in its hub name
 * ("Settings — devbox · work"), so two tabs of two instances tell themselves apart; the default
 * instance keeps "— NextBoard". The hub's manifest names the app the same way.
 */
export function setInstance(name: string | undefined, isDefault?: boolean, hub?: string): void {
  named = name && name !== 'default' && isDefault !== true ? name : '';
  hubName = (hub ?? '').trim();
}

function suffix(): string {
  if (!named) return 'NextBoard';
  return hubName || `NextBoard · ${named}`;
}

/** A page's title, "Settings — NextBoard" or "Settings — devbox · work"; without a page, the app's. */
export function pageTitle(page?: string): string {
  if (page) return `${page} — ${suffix()}`;
  return named && hubName ? `NextBoard — ${hubName}` : suffix();
}

// ---- the hubId storage guard -------------------------------------------------------------

export const HUB_ID_KEY = 'nb.hubId';
/** The studio's drafts (studio/drafts.ts). */
const DRAFTS_DB = 'nextboard';

function localStore(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

function dropDatabase(name: string): Promise<void> {
  return new Promise((resolve) => {
    try {
      const req = indexedDB.deleteDatabase(name);
      const done = () => resolve();
      req.onsuccess = done;
      req.onerror = done;
      req.onblocked = done;
      setTimeout(resolve, 1500);
    } catch {
      resolve();
    }
  });
}

/**
 * What this browser keeps for an origin (localStorage nb.*, the drafts database) belongs to the
 * hub it was kept for. A port is reused when an instance is removed and another is made, and the
 * new hub would find the old one's drafts, preferences and vault pin record: so when
 * /api/setup/status names another hub than the one seen here before, they go. A browser that has
 * seen no hubId yet keeps what it has (an upgrade: it is this hub's).
 *
 * True when something was cleared (the page should then reload, so nothing already read keeps an
 * old value), and only once the new hubId is stored, so a reload cannot loop.
 */
export async function guardHubStorage(
  hubId: string | undefined,
  storage: Storage | null = localStore(),
  drop: (db: string) => Promise<void> = dropDatabase,
): Promise<boolean> {
  if (!hubId || !storage) return false;
  let seen: string | null;
  try {
    seen = storage.getItem(HUB_ID_KEY);
  } catch {
    return false;
  }
  if (seen === hubId) return false;
  const stale = seen !== null;
  if (stale) {
    const keys: string[] = [];
    for (let i = 0; i < storage.length; i++) {
      const k = storage.key(i);
      if (k?.startsWith('nb.')) keys.push(k);
    }
    for (const k of keys) {
      try {
        storage.removeItem(k);
      } catch {
        // blocked: nothing more to do
      }
    }
    await drop(DRAFTS_DB);
  }
  let saved = false;
  try {
    storage.setItem(HUB_ID_KEY, hubId);
    saved = storage.getItem(HUB_ID_KEY) === hubId;
  } catch {
    saved = false;
  }
  return stale && saved;
}

// ---- New instance… -----------------------------------------------------------------------

export const INSTANCE_NAME = /^[a-z][a-z0-9-]{0,23}$/;

/** What is wrong with a new instance's name, "" when nothing. */
export function instanceNameError(name: string, taken: string[]): string {
  if (!name) return 'Give it a name, for example work or personal.';
  if (!INSTANCE_NAME.test(name)) return 'Lowercase letters, digits and dashes, starting with a letter; at most 24.';
  if (name === 'default') return 'default is the instance in ~/.nextboard: set it up with nextboard install.';
  if (taken.includes(name)) return `An instance called ${name} is already on this machine.`;
  return '';
}

/** What is wrong with a port typed in, "" when nothing. */
export function portError(port: string): string {
  const n = Number(port.trim());
  if (!/^\d+$/.test(port.trim()) || n < 1024 || n > 65535) return 'A port from 1024 to 65535.';
  return '';
}

export interface Here {
  protocol: string;
  hostname: string;
}

function hostOf(listen: string): string {
  return listen
    .trim()
    .replace(/:\d+$/, '')
    .replace(/^\[|\]$/g, '');
}

const loopback = (h: string) => h === 'localhost' || h === '::1' || h === '[::1]' || /^127\./.test(h);
const everyAddress = (h: string) => h === '' || h === '0.0.0.0' || h === '::';

/** How the address radio names a host: "every address, 0.0.0.0" or the host itself. */
export function hostLabel(host: string): string {
  return everyAddress(host) ? `every address, ${host || '0.0.0.0'}` : host;
}

/**
 * The link Open setup opens: the setup URL on this browser's host name, else one made on it when
 * the new hub listens there too (every address, this host, or loopback seen from loopback). null
 * when this browser cannot reach the new hub (it is on loopback, the browser on another machine).
 * from: the instance whose settings the wizard offers to copy (?from=).
 */
export function setupLink(setupUrls: string[], listen: string, here: Here, from?: string): string | null {
  const parsed = setupUrls.flatMap((u) => {
    try {
      return [new URL(u)];
    } catch {
      return [];
    }
  });
  if (parsed.length === 0) return null;
  let pick = parsed.find((u) => u.hostname === here.hostname) ?? null;
  if (!pick) {
    const host = hostOf(listen);
    if (everyAddress(host) || host === here.hostname.replace(/^\[|\]$/g, '') || (loopback(host) && loopback(here.hostname))) {
      pick = new URL(parsed[0].toString());
      pick.hostname = here.hostname;
    }
  }
  if (!pick) return null;
  if (from) pick.searchParams.set('from', from);
  return pick.toString();
}

/** The tunnel that reaches a new hub on loopback from another machine. */
export function tunnelCommand(listen: string, sshHost: string): string {
  const port = /:(\d+)$/.exec(listen.trim())?.[1] ?? '';
  return `ssh -L ${port}:127.0.0.1:${port} ${sshHost}`;
}
