// The work the editor does itself for extensions whose code cannot run here
// (docs/API-studio.md §7.6): the hub names it per installed extension, and each piece follows
// this list, on while its extension is installed.

import type { InstalledExtension } from '../api';
import type { monaco } from './monaco';
import type { Workspace } from './workspace';

let active = new Set<string>();
const subs = new Set<() => void>();

export const hasNative = (name: string) => active.has(name);

export function setNativeList(list: InstalledExtension[]) {
  const next = new Set(list.flatMap((x) => (x.native ? [x.native] : [])));
  if (next.size === active.size && [...next].every((n) => active.has(n))) return;
  active = next;
  for (const fn of [...subs]) fn();
}

export function onNativeChange(fn: () => void): () => void {
  subs.add(fn);
  return () => subs.delete(fn);
}

/** Who shows a model: providers are global, the project a file belongs to is not. */
export const modelOwners = new WeakMap<monaco.editor.ITextModel, Workspace>();
