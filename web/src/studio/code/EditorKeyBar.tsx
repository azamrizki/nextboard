// Keys a phone's keyboard does not have, for the code editor that has the keyboard on a touch
// screen (DESIGN.md §3.9): Esc, Tab, Shift+Tab and the arrows go to the editor as key presses, so
// they do what they do there (accept a suggestion, leave a widget, vim's normal mode); Select makes
// the arrows select. Then undo, redo, find, comment, save and Go to file. Pressing a key does not
// take the focus, so the on-screen keyboard stays up.

import { useEffect, useState } from 'react';
import { Key } from '../../board/KeyBar';
import { monaco } from './monaco';
import type { Workspace } from './workspace';

interface Press {
  key: string;
  code: string;
  keyCode: number;
  shift?: boolean;
}

const KEYS: Record<'esc' | 'tab' | 'backtab' | 'up' | 'down' | 'left' | 'right', Press> = {
  esc: { key: 'Escape', code: 'Escape', keyCode: 27 },
  tab: { key: 'Tab', code: 'Tab', keyCode: 9 },
  backtab: { key: 'Tab', code: 'Tab', keyCode: 9, shift: true },
  up: { key: 'ArrowUp', code: 'ArrowUp', keyCode: 38 },
  down: { key: 'ArrowDown', code: 'ArrowDown', keyCode: 40 },
  left: { key: 'ArrowLeft', code: 'ArrowLeft', keyCode: 37 },
  right: { key: 'ArrowRight', code: 'ArrowRight', keyCode: 39 },
};

/** A key press on the editor's input, which is where a keyboard's go. */
function press(p: Press, shift = false) {
  const target = document.activeElement;
  if (!target?.closest('.monaco-editor')) return;
  for (const type of ['keydown', 'keyup'] as const) {
    const ev = new KeyboardEvent(type, { key: p.key, code: p.code, shiftKey: p.shift || shift, bubbles: true, cancelable: true });
    // the editor reads the old key codes, which a constructed event leaves at 0
    Object.defineProperty(ev, 'keyCode', { get: () => p.keyCode });
    Object.defineProperty(ev, 'which', { get: () => p.keyCode });
    target.dispatchEvent(ev);
  }
}

/** The editor that has the keyboard. */
const focusedEditor = () => monaco.editor.getEditors().find((e) => e.hasTextFocus()) ?? null;

export function EditorKeyBar({ ws, onGoToFile }: { ws: Workspace; onGoToFile: () => void }) {
  const [select, setSelect] = useState(false);

  // the focused card's area leaves room for this
  useEffect(() => {
    document.documentElement.setAttribute('data-keybar', '');
    return () => document.documentElement.removeAttribute('data-keybar');
  }, []);

  const run = (id: string) => {
    const ed = focusedEditor();
    if (!ed) return;
    try {
      ed.trigger('keybar', id, null);
    } catch {
      /* not in this editor */
    }
  };
  const arrow = (k: 'up' | 'down' | 'left' | 'right') => press(KEYS[k], select);
  const save = () => {
    const path = ws.activeDoc?.path;
    if (path) void ws.save(path);
  };

  return (
    <div className="key-bar" role="toolbar" aria-label="Keys for the editor">
      <div className="key-bar-keys">
        <Key label="Esc" name="Esc" onPress={() => press(KEYS.esc)} />
        <Key label="Tab" name="Tab" onPress={() => press(KEYS.tab)} />
        <Key label="⇧Tab" name="Shift+Tab" onPress={() => press(KEYS.backtab)} />
        <Key icon="arrowLeft" name="Left" onPress={() => arrow('left')} />
        <Key icon="arrowUp" name="Up" onPress={() => arrow('up')} />
        <Key icon="arrowDown" name="Down" onPress={() => arrow('down')} />
        <Key icon="arrowRight" name="Right" onPress={() => arrow('right')} />
        <Key label="Sel" name="Select: the arrows select" pressed={select} onPress={() => setSelect((s) => !s)} />
        <Key icon="undo" name="Undo" onPress={() => run('undo')} />
        <Key icon="redo" name="Redo" onPress={() => run('redo')} />
        <Key icon="search" name="Find" onPress={() => run('actions.find')} />
        <Key label="//" name="Comment the line" onPress={() => run('editor.action.commentLine')} />
      </div>
      <div className="key-bar-tools">
        <Key icon="save" name="Save" onPress={save} />
        <Key icon="file" name="Go to file" onPress={onGoToFile} />
        <Key icon="keyboard" name="Hide the keyboard" onPress={() => (document.activeElement as HTMLElement | null)?.blur()} />
      </div>
    </div>
  );
}
