// Unsaved studio edits kept in IndexedDB per (machine, path), so a crash or a closed tab
// doesn't lose them (docs/API-studio.md §7, §8). Best effort: no IndexedDB → no drafts.

export interface Draft {
  content: string;
  /** mtime of the file the draft started from. */
  baseMtime: string;
  at: number;
}

const DB = 'nextboard';
const STORE = 'drafts';
let dbp: Promise<IDBDatabase | null> | null = null;

function db(): Promise<IDBDatabase | null> {
  dbp ??= new Promise((resolve) => {
    try {
      const req = indexedDB.open(DB, 1);
      req.onupgradeneeded = () => {
        if (!req.result.objectStoreNames.contains(STORE)) req.result.createObjectStore(STORE);
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => resolve(null);
    } catch {
      resolve(null);
    }
  });
  return dbp;
}

const key = (machineId: string, path: string) => `${machineId}:${path}`;

function tx<T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T>): Promise<T | undefined> {
  return db().then(
    (d) =>
      new Promise((resolve) => {
        if (!d) return resolve(undefined);
        try {
          const r = fn(d.transaction(STORE, mode).objectStore(STORE));
          r.onsuccess = () => resolve(r.result);
          r.onerror = () => resolve(undefined);
        } catch {
          resolve(undefined);
        }
      }),
  );
}

export const drafts = {
  get: (machineId: string, path: string) => tx<Draft>('readonly', (s) => s.get(key(machineId, path)) as IDBRequest<Draft>),
  put: (machineId: string, path: string, d: Draft) => tx('readwrite', (s) => s.put(d, key(machineId, path))),
  remove: (machineId: string, path: string) => tx('readwrite', (s) => s.delete(key(machineId, path))),
};
