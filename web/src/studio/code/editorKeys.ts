// Keys of the Code workbench's editors (Save, bookmarks, mention…). Registered once for all
// editors, and each key goes to the editor that has focus.
//
// An editor's own addCommand would not do: Monaco keeps such a command and its key for good,
// with no way to remove them. Each mount of an editor added seven, and each kept its editor and
// workspace alive after both were gone.

import { monaco } from './monaco';

export type EditorKey = 'save' | 'inlineSuggest' | 'mention' | 'bookmark' | 'nextBookmark' | 'prevBookmark' | 'format';

const KEYS: Record<EditorKey, number> = {
  save: monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS,
  inlineSuggest: monaco.KeyMod.Alt | monaco.KeyCode.Backslash,
  mention: monaco.KeyMod.Alt | monaco.KeyCode.KeyK,
  bookmark: monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.KeyK,
  nextBookmark: monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.KeyL,
  prevBookmark: monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.KeyJ,
  // the key the menu shows: on Linux Monaco only binds Ctrl+Shift+I to it
  format: monaco.KeyMod.Shift | monaco.KeyMod.Alt | monaco.KeyCode.KeyF,
};

type Handlers = Partial<Record<EditorKey, () => void>>;

const editors = new Map<monaco.editor.ICodeEditor, Handlers>();
let installed = false;

function install() {
  if (installed) return;
  installed = true;
  for (const [name, keybinding] of Object.entries(KEYS) as [EditorKey, number][]) {
    const command = `nextboard.editor.${name}`;
    monaco.editor.addCommand({ id: command, run: () => press(name) });
    monaco.editor.addKeybindingRule({ keybinding, command, when: 'editorFocus' });
  }
}

function press(name: EditorKey) {
  for (const [ed, keys] of editors) {
    if (ed.hasWidgetFocus()) {
      keys[name]?.();
      return;
    }
  }
}

/** Gives an editor its keys until the returned value is disposed. */
export function editorKeys(ed: monaco.editor.ICodeEditor, keys: Handlers): monaco.IDisposable {
  install();
  editors.set(ed, keys);
  return { dispose: () => void editors.delete(ed) };
}

/** Editors with keys (tests). */
export function editorsWithKeys(): number {
  return editors.size;
}
