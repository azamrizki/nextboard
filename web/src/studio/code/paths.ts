// The Code workbench's path model across the parts of a project (docs/API-studio.md §7,
// v0.9.0-beta.2). Pure: no Monaco, no DOM, no API calls.
//
// A file is (machine, absolute path), kept as a FileKey: "/abs/path" on This PC and
// "//m-<hex of the machine id>/abs/path" on any other machine. Every internal map (tabs, docs,
// tree rows, git and review marks, breakpoints, bookmarks, LSP models) is keyed by it, so a key
// depends on neither the card, the part's name nor the part order. On This PC a key is the path
// itself, so a single-part local project keeps today's keys, URIs and saved state.
//
// The "/<part>/…" path is display and input only (tree labels, crumbs, tab descriptions, Go to
// file, search globs, the lead's editor.open); ViewPaths makes and reads it. Fs calls never take
// a key: split it first (splitKey).

import type { Placement } from '../../lib/parts';

declare const fileKeyBrand: unique symbol;
/** A file's identity: "/abs" on This PC, "//m-<hex>/abs" elsewhere. Only made by fileKey / asFileKey. */
export type FileKey = string & { readonly [fileKeyBrand]: true };

/** The hub's id of This PC (machine.LocalID). "" is read as This PC too. */
export const LOCAL_MACHINE = 'local';

/** The virtual root of a Root view (all parts as top-level folders). Never sent to the hub. */
export const ROOT_KEY = 'nb:root' as FileKey;
export const isRootKey = (k: string): boolean => k === ROOT_KEY;

/** The version of saved workbench state whose paths are keys (storedPaths). */
export const PERSIST_VERSION = 2;

const local = (machineId: string) => machineId === '' || machineId === LOCAL_MACHINE;

/** An absolute path made clean as Go's path.Clean does ("//a/./b/../c/" → "/a/c"); null when not absolute. */
export function cleanAbs(p: string): string | null {
  if (!p.startsWith('/')) return null;
  const out: string[] = [];
  for (const seg of p.split('/')) {
    if (seg === '' || seg === '.') continue;
    if (seg === '..') out.pop();
    else out.push(seg);
  }
  return `/${out.join('/')}`;
}

const enc = new TextEncoder();

/** A machine's URI authority: "" for This PC, else "m-" + lowercase hex of its id's UTF-8 (URIs lowercase authorities). */
export function authorityOf(machineId: string): string {
  if (local(machineId)) return '';
  let h = 'm-';
  for (const b of enc.encode(machineId)) h += b.toString(16).padStart(2, '0');
  return h;
}

/** The machine id of an authority: "" → This PC; null when it is not one authorityOf makes. */
export function machineOfAuthority(a: string): string | null {
  if (a === '') return LOCAL_MACHINE;
  if (!/^m-(?:[0-9a-f]{2})+$/i.test(a)) return null;
  const hex = a.slice(2);
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < bytes.length; i++) bytes[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  try {
    const id = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
    return local(id) ? null : id;
  } catch {
    return null;
  }
}

/** The key of a file. abs must be absolute (it is cleaned); a relative path throws, it is a bug. */
export function fileKey(machineId: string, abs: string): FileKey {
  const p = cleanAbs(abs);
  if (p === null) throw new Error(`not an absolute path: ${abs}`);
  const a = authorityOf(machineId);
  return (a ? `//${a}${p}` : p) as FileKey;
}

/** A raw string (saved state, an event) as a key, made canonical; null when it is none. ROOT_KEY stays itself. */
export function asFileKey(raw: string): FileKey | null {
  if (raw === ROOT_KEY) return ROOT_KEY;
  if (raw.startsWith('//m-')) {
    const i = raw.indexOf('/', 2);
    if (i < 0) return null;
    const m = machineOfAuthority(raw.slice(2, i));
    const p = cleanAbs(raw.slice(i));
    return m === null || p === null ? null : fileKey(m, p);
  }
  const p = cleanAbs(raw);
  return p === null ? null : (p as FileKey);
}

/** The machine and the real path of a key, for fs calls. ROOT_KEY throws: it is no place on a machine. */
export function splitKey(k: FileKey): { machineId: string; abs: string } {
  if (k.startsWith('//')) {
    const i = k.indexOf('/', 2);
    const m = i < 0 ? null : machineOfAuthority(k.slice(2, i));
    if (m !== null && i > 0) return { machineId: m, abs: k.slice(i) };
  } else if (k.startsWith('/')) {
    return { machineId: LOCAL_MACHINE, abs: k };
  }
  throw new Error(`not a file key: ${k}`);
}

/** The key of a Monaco model's URI (anything with scheme, authority and path); null unless a file URI of ours. */
export function keyOfUri(u: { scheme: string; authority: string; path: string }): FileKey | null {
  if (u.scheme !== 'file') return null;
  const m = machineOfAuthority(u.authority);
  const p = cleanAbs(u.path);
  return m === null || p === null ? null : fileKey(m, p);
}

/** The parts of a key's Monaco URI: monaco.Uri.from(uriPartsOf(k)). On This PC the same as Uri.file(abs). */
export function uriPartsOf(k: FileKey): { scheme: 'file'; authority: string; path: string } {
  const { machineId, abs } = splitKey(k);
  return { scheme: 'file', authority: authorityOf(machineId), path: abs };
}

/** p is root or inside it, at a "/" boundary: "/srv/app" holds "/srv/app/x", not "/srv/app2/x"; "/" holds every absolute path. */
export function under(p: string, root: string): boolean {
  const r = root.length > 1 ? root.replace(/\/+$/, '') : root;
  if (r === '/') return p.startsWith('/');
  return p === r || p.startsWith(`${r}/`);
}

/** under() for keys: the same machine, and k's path is root's or inside it. ROOT_KEY is only under itself. */
export function keyUnder(k: FileKey, root: FileKey): boolean {
  if (isRootKey(k) || isRootKey(root)) return k === root;
  const a = splitKey(k);
  const b = splitKey(root);
  return a.machineId === b.machineId && under(a.abs, b.abs);
}

/** The key of rel ("src/x.ts", or a name) inside a folder's key, on the same machine. */
export function joinKey(dir: FileKey, rel: string): FileKey {
  const { machineId, abs } = splitKey(dir);
  return fileKey(machineId, `${abs}/${rel}`);
}

/** The folder holding a key; null for "/" and ROOT_KEY. */
export function parentKey(k: FileKey): FileKey | null {
  if (isRootKey(k)) return null;
  const { machineId, abs } = splitKey(k);
  if (abs === '/') return null;
  return fileKey(machineId, abs.slice(0, abs.lastIndexOf('/')) || '/');
}

/** One part as a view mounts it. */
export interface Mount {
  partId: string;
  name: string;
  machineId: string;
  /** The part's folder on its machine, clean, no trailing "/". */
  root: string;
  rootKey: FileKey;
  /** The hub ref of the part (lib/parts.ts partRef): "<project>~<part>". */
  ref: string;
  readOnly?: boolean;
}

const relIn = (abs: string, root: string) => (abs === root ? '' : root === '/' ? abs.slice(1) : abs.slice(root.length + 1));
const noDotDot = (s: string) => !s.split('/').includes('..');
const trimLead = (s: string) => s.replace(/^(?:\.\/|\/)+/, '').replace(/^\.$/, '');

/**
 * The paths of one editor card's view. Identity mode (a single-part project, or a card placed in
 * one part): one mount, keys are real paths, labels are today's. Prefixed mode (multi: the Root
 * view of a project with two or more parts): the tree root is ROOT_KEY, its children are the
 * parts in part order, and "/<part name>/rel" ↔ that part's key.
 */
export class ViewPaths {
  constructor(
    readonly mounts: readonly Mount[],
    readonly multi: boolean,
  ) {
    if (!mounts.length) throw new Error('a view mounts at least one part');
  }

  /** The tree root: ROOT_KEY in the Root view, else the part's folder. */
  get rootKey(): FileKey {
    return this.multi ? ROOT_KEY : this.mounts[0].rootKey;
  }

  /** The mount holding a key: same machine, the longest root, at a "/" boundary. */
  mountOf(k: FileKey): Mount | null {
    if (isRootKey(k)) return null;
    const { machineId, abs } = splitKey(k);
    let best: Mount | null = null;
    for (const m of this.mounts) {
      if ((local(m.machineId) ? LOCAL_MACHINE : m.machineId) === machineId && under(abs, m.root) && (!best || m.root.length > best.root.length)) best = m;
    }
    return best;
  }

  /** The mount by part name: exact, else in any case (names are unique in any case). */
  mountNamed(name: string): Mount | null {
    return this.mounts.find((m) => m.name === name) ?? this.mounts.find((m) => m.name.toLowerCase() === name.toLowerCase()) ?? null;
  }

  /** The part and the path inside it, for API calls on the part's ref (rel "" = its folder). */
  partRel(k: FileKey): { mount: Mount; rel: string } | null {
    const mount = this.mountOf(k);
    return mount ? { mount, rel: relIn(splitKey(k).abs, mount.root) } : null;
  }

  /** The key of a path inside a mount (rel "" = its folder). */
  keyOf(m: Mount, rel: string): FileKey {
    const r = trimLead(rel);
    return r ? joinKey(m.rootKey, r) : m.rootKey;
  }

  /**
   * What a key shows as. Root view: "/admin/src/x.ts" inside a part, "/admin" for its folder, "/"
   * for ROOT_KEY, and "admin: /usr/lib/x.d.ts" outside every part (the first part on that
   * machine, else the machine id). Identity mode: the absolute path, as today.
   */
  label(k: FileKey): string {
    if (isRootKey(k)) return '/';
    const { machineId, abs } = splitKey(k);
    if (!this.multi) return abs;
    const pr = this.partRel(k);
    if (pr) return `/${pr.mount.name}${pr.rel ? `/${pr.rel}` : ''}`;
    const on = this.mounts.find((m) => (local(m.machineId) ? LOCAL_MACHINE : m.machineId) === machineId);
    return `${on ? on.name : machineId}: ${abs}`;
  }

  /**
   * The path to copy as relative. Root view: "admin/src/x.ts" ("admin" for its folder). Identity
   * mode: "src/x.ts" as today ("" for the part's folder). Outside every part: the absolute path.
   */
  relLabel(k: FileKey): string {
    if (isRootKey(k)) return '';
    const pr = this.partRel(k);
    if (!pr) return splitKey(k).abs;
    return this.multi ? `${pr.mount.name}${pr.rel ? `/${pr.rel}` : ''}` : pr.rel;
  }

  /**
   * A typed or named path as a key; null when it names nothing. Never takes "..". With machineId,
   * an absolute input is a real path on that machine. Root view: "/admin/x", "admin/x" and
   * "./admin/x" name part admin (in any case), "/" is ROOT_KEY, and another absolute path is the
   * real path in the part holding it (first in part order). Identity mode: "src/x" is inside the
   * part, an absolute path is a real path on the part's machine.
   */
  resolve(input: string, machineId?: string): FileKey | null {
    const s = input.trim();
    if (!s || !noDotDot(s)) return null;
    if (machineId !== undefined && s.startsWith('/')) return fileKey(machineId, s);
    if (!this.multi) {
      const m = this.mounts[0];
      return s.startsWith('/') ? fileKey(m.machineId, s) : this.keyOf(m, s);
    }
    const rest = trimLead(s);
    if (!rest) return s.startsWith('/') ? ROOT_KEY : null;
    const i = rest.indexOf('/');
    const named = this.mountNamed(i < 0 ? rest : rest.slice(0, i));
    if (named) return this.keyOf(named, i < 0 ? '' : rest.slice(i + 1));
    if (!s.startsWith('/')) return null;
    let best: Mount | null = null;
    for (const m of this.mounts) if (under(cleanAbs(s) ?? s, m.root) && (!best || m.root.length > best.root.length)) best = m;
    return best ? fileKey(best.machineId, s) : null;
  }

  /**
   * Search include or exclude globs ("admin/**, *.ts") by part: a glob whose first segment is a
   * part's name goes to that part without the segment ("admin" alone is "**"), every other glob
   * to "*" (all parts). Identity mode: everything is "*". Values are comma-joined. For an include,
   * search only the parts in the map unless "*" is in it.
   */
  routeGlobs(globs: string): Map<string, string> {
    const by = new Map<string, string[]>();
    for (const raw of globs.split(',')) {
      const g = raw.trim();
      if (!g) continue;
      let key = '*';
      let rest = g;
      if (this.multi) {
        const s = trimLead(g);
        const i = s.indexOf('/');
        const m = this.mountNamed(i < 0 ? s : s.slice(0, i));
        if (m) {
          key = m.partId;
          rest = (i < 0 ? '' : s.slice(i + 1)) || '**';
        }
      }
      by.set(key, [...(by.get(key) ?? []), rest]);
    }
    return new Map([...by].map(([k, v]) => [k, v.join(',')]));
  }
}

/**
 * The key a card's view is kept and found by (wb.<viewKey> in the browser, the view registry):
 * the project id for a single-part project (as today), "<project>~<part>" for a card in one part
 * of a multi-part project, "<project>@root" for its Root card.
 */
export function viewKey(projectId: string, partsCount: number, placement: Placement): string {
  if (partsCount < 2) return projectId;
  return 'root' in placement ? `${projectId}@root` : `${projectId}~${placement.partId}`;
}

/**
 * How to read the paths of saved workbench state: with v: 2 they are keys (asFileKey), without
 * a version they are absolute paths on the machine of the part the state was saved for. The
 * reader gives null for what is neither (drop it). Pane tabs have no path: do not pass "".
 */
export function storedPaths(saved: unknown, legacyMachineId: string): (raw: string) => FileKey | null {
  const v2 = !!saved && typeof saved === 'object' && (saved as { v?: unknown }).v === PERSIST_VERSION;
  if (v2) return (raw) => asFileKey(raw);
  return (raw) => {
    const p = cleanAbs(raw);
    return p === null ? null : fileKey(legacyMachineId, p);
  };
}
