// The work the editor does itself for extensions whose code cannot run here
// (docs/API-studio.md §7.6): the hub names it per installed extension, and each piece follows
// this list, on while its extension is installed.
//
// Also who shows a model: providers and openers are global in Monaco, the card a file is shown
// in is not. These lookups replace "the card opened last" (v0.9.0-beta.2).

import type { InstalledExtension } from '../api';
import { monaco } from './monaco';
import { keyOfUri } from './paths';
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

/** The card that last showed a model in one of its editors. */
export const modelOwners = new WeakMap<monaco.editor.ITextModel, Workspace>();

/** The card each editor belongs to (editorFeatures.ts attachFeatures). */
export const editorViews = new WeakMap<monaco.editor.ICodeEditor, Workspace>();

let lastFocused: WeakRef<Workspace> | null = null;

/** An editor of this card took the focus. */
export function focusedView(ws: Workspace) {
  lastFocused = new WeakRef(ws);
}

/** The card whose editor had the focus last; null before any did. */
export function lastView(): Workspace | null {
  return lastFocused?.deref() ?? null;
}

/** The card a model is worked on in: the focused editor's that shows it, else the last that showed it. */
export function viewOfModel(model: monaco.editor.ITextModel): Workspace | null {
  for (const ed of monaco.editor.getEditors()) {
    if (ed.getModel() !== model || !ed.hasTextFocus()) continue;
    const v = editorViews.get(ed);
    if (v) return v;
  }
  return modelOwners.get(model) ?? null;
}

let openerDone = false;

/**
 * Go to definition (and every other "open this file") lands in the card of the editor it came
 * from, at the file's key: the URI names the machine, so the same path on two machines stays two
 * files. Without a source editor, the card focused last.
 */
export function registerOpener() {
  if (openerDone) return;
  openerDone = true;
  monaco.editor.registerEditorOpener({
    openCodeEditor(source, resource, target) {
      const key = keyOfUri(resource);
      if (!key) return false;
      const model = source?.getModel();
      const view = (source && editorViews.get(source)) ?? (model ? modelOwners.get(model) : undefined) ?? lastView();
      if (!view) return false;
      const pos = target && 'startLineNumber' in target ? { line: target.startLineNumber, col: target.startColumn } : target && 'lineNumber' in target ? { line: target.lineNumber, col: target.column } : {};
      view.open(key, { ...pos, source: true });
      return true;
    },
  });
}
