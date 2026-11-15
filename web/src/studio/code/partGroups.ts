// How the Code workbench's panels group and route by part (v0.9.0-beta.2, docs/API-studio.md §7):
// a Root card shows every part, so its lists get one section per part and its terminals and agents
// ask where to start; a card of one part shows that part only, as before. Pure: paths.ts keys and
// mounts in, plain values out (no Monaco, no API calls), so the rules are tested on their own.

import { isRoot, ROOT, type Placement } from '../../lib/parts';
import { cleanAbs, fileKey, isRootKey, LOCAL_MACHINE, splitKey, type FileKey, type Mount, type ViewPaths } from './paths';

/** One part's section of a panel list; mount null = the files outside every part of the card. */
export interface PartSection<T> {
  mount: Mount | null;
  items: T[];
}

/**
 * Items by the part holding their key, in the card's part order, each part once, empty parts left
 * out. Items outside every part (or with no key) come last in a section of their own.
 */
export function byPart<T>(paths: ViewPaths, items: readonly T[], keyOf: (item: T) => FileKey | null): PartSection<T>[] {
  const by = new Map<string, T[]>();
  const outside: T[] = [];
  for (const it of items) {
    const k = keyOf(it);
    const m = k && !isRootKey(k) ? paths.mountOf(k) : null;
    if (!m) outside.push(it);
    else by.set(m.partId, [...(by.get(m.partId) ?? []), it]);
  }
  const out: PartSection<T>[] = paths.mounts.flatMap((m) => (by.has(m.partId) ? [{ mount: m, items: by.get(m.partId)! }] : []));
  if (outside.length) out.push({ mount: null, items: outside });
  return out;
}

/** A part's folder itself (a Root card's top row, or a card's tree root): no rename, move, trash or cut. */
export const isPartRoot = (paths: ViewPaths, key: string): boolean => isRootKey(key) || paths.mounts.some((m) => m.rootKey === key);

/** The machines a card's parts are on, each with its parts, in part order: one Ports section each. */
export function machinesOf(mounts: readonly Mount[]): { machineId: string; parts: Mount[] }[] {
  const out: { machineId: string; parts: Mount[] }[] = [];
  for (const m of mounts) {
    const id = m.machineId || LOCAL_MACHINE;
    const at = out.find((x) => x.machineId === id);
    if (at) at.parts.push(m);
    else out.push({ machineId: id, parts: [m] });
  }
  return out;
}

// ---- where a terminal or an agent of the card starts ------------------------------------------

/** What a card's "New terminal" and "Open an agent" offer: a Root card Root and every part, a card of one part only that part. */
export function placeChoices(paths: ViewPaths): Placement[] {
  const parts = paths.mounts.map((m): Placement => ({ partId: m.partId }));
  return paths.multi ? [ROOT, ...parts] : parts.slice(0, 1);
}

/** Whether a placement is one of the card's choices (a remembered one may name a part since removed). */
export function offered(paths: ViewPaths, p: Placement | null | undefined): p is Placement {
  if (!p) return false;
  return isRoot(p) ? paths.multi : paths.mounts.some((m) => m.partId === p.partId) && (paths.multi || paths.mounts[0].partId === p.partId);
}

/**
 * The choice a card starts with: a card of one part its part; a Root card the part of the file or
 * row in use, else the one chosen last (when still offered), else Root.
 */
export function defaultPlace(paths: ViewPaths, active: FileKey | null, last: Placement | null): Placement {
  if (!paths.multi) return { partId: paths.mounts[0].partId };
  const m = active && !isRootKey(active) ? paths.mountOf(active) : null;
  if (m) return { partId: m.partId };
  return offered(paths, last) ? last : ROOT;
}

/** The folder a placement starts in (a key, for the card's placeOf): a part's folder; Root none. */
export function placeDir(paths: ViewPaths, p: Placement): FileKey | undefined {
  return isRoot(p) ? undefined : paths.mounts.find((m) => m.partId === p.partId)?.rootKey;
}

/**
 * The panel agents an editor card offers to show: a Root card every one, a card of one part the
 * ones in that part (none at the root, none outside it). A single-part project's card takes all.
 */
export function fitsCard(s: { root?: boolean; partId?: string }, paths: ViewPaths, multiPartProject: boolean): boolean {
  if (paths.multi || !multiPartProject) return true;
  return !s.root && !!s.partId && s.partId === paths.mounts[0].partId;
}

// ---- lists the card shows by path -----------------------------------------------------------

/** What Go to file and @ mentions list a part's file as: "admin/src/x.ts" in a Root card, "src/x.ts" otherwise (ViewPaths.resolve reads both back). */
export const fileLabel = (multi: boolean, part: string, rel: string) => (multi ? `${part}/${rel}` : rel);

/** One step of a file's breadcrumbs: its name, and the folder to reveal (null: nothing to reveal, outside the card's parts). */
export interface Crumb {
  label: string;
  key: FileKey | null;
}

/**
 * The breadcrumbs of a file: in a Root card its part first ("admin › src › x.ts"), in a card of one
 * part the path inside it ("src › x.ts"), outside every part the absolute path, folder by folder.
 */
export function crumbsOf(paths: ViewPaths, key: FileKey): Crumb[] {
  const pr = paths.partRel(key);
  if (!pr) {
    const { machineId, abs } = splitKey(key);
    const segs = abs.split('/').filter(Boolean);
    return segs.map((s, i) => ({ label: s, key: i < segs.length - 1 ? fileKey(machineId, `/${segs.slice(0, i + 1).join('/')}`) : key }));
  }
  const segs = pr.rel ? pr.rel.split('/') : [];
  const out: Crumb[] = paths.multi ? [{ label: pr.mount.name, key: pr.mount.rootKey }] : [];
  segs.forEach((s, i) => out.push({ label: s, key: paths.keyOf(pr.mount, segs.slice(0, i + 1).join('/')) }));
  return out;
}

/**
 * What a file tab says beside its name: in a Root card the part ("admin"), and when another tab of
 * the group has the same name, the folder too ("admin › src").
 */
export function tabDir(paths: ViewPaths, key: FileKey, twin: boolean): string {
  const pr = paths.partRel(key);
  const parent = (p: string) => {
    const d = p.slice(0, Math.max(0, p.lastIndexOf('/')));
    return d.slice(d.lastIndexOf('/') + 1);
  };
  const dir = twin ? parent(pr ? pr.rel : splitKey(key).abs) : '';
  const part = paths.multi && pr ? pr.mount.name : '';
  return [part, dir].filter(Boolean).join(' › ');
}

// ---- tasks ----------------------------------------------------------------------------------

/** A task run's key (Workspace.taskRuns, taskSelected): its part and its id, so "npm: build" in two parts stays two. */
export const taskKey = (partId: string, taskId: string) => `${partId}\u0001${taskId}`;

/**
 * The file a task's output names, as a key on its part's machine: an absolute path as it is, a
 * relative one against the task's folder (cwd inside base, base being the part's git root or folder).
 */
export function taskFile(machineId: string, base: string, cwd: string, file: string): FileKey | null {
  const p = cleanAbs(file.startsWith('/') ? file : `${base}/${[cwd, file.replace(/^\.\//, '')].filter(Boolean).join('/')}`);
  return p ? fileKey(machineId, p) : null;
}
