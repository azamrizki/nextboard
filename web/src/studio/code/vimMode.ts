// Vim keys, from monaco-vim, loaded only when the Vim extension is installed. Its status line
// (mode, ":" commands, search) goes in a strip below the editor.

import type { monaco } from './monaco';
import { hasNative, onNativeChange } from './nativeExt';

type Editor = monaco.editor.IStandaloneCodeEditor;

interface VimHost {
  save(): void;
  close(): void;
}

/** ":w" and friends find their editor here. */
const hosts = new WeakMap<Editor, VimHost>();
let exDefined = false;

function defineEx(Vim: { defineEx(name: string, prefix: string, fn: (cm: { editor: Editor }) => void): void }) {
  if (exDefined) return;
  exDefined = true;
  const host = (cm: { editor: Editor }) => hosts.get(cm.editor);
  Vim.defineEx('write', 'w', (cm) => host(cm)?.save());
  Vim.defineEx('quit', 'q', (cm) => host(cm)?.close());
  const both = (cm: { editor: Editor }) => {
    host(cm)?.save();
    host(cm)?.close();
  };
  Vim.defineEx('wq', 'wq', both);
  Vim.defineEx('xit', 'x', both);
}

export function watchVim(ed: Editor, status: HTMLElement, host: VimHost): monaco.IDisposable {
  hosts.set(ed, host);
  let stop: (() => void) | null = null;
  let wanted = false;
  let gone = false;
  const sync = async () => {
    wanted = hasNative('vim') && !gone;
    if (!wanted) {
      stop?.();
      stop = null;
      return;
    }
    if (stop) return;
    const { initVimMode, VimMode } = await import('monaco-vim');
    if (!wanted || stop) return;
    defineEx((VimMode as unknown as { Vim: Parameters<typeof defineEx>[0] }).Vim);
    const vim = initVimMode(ed, status);
    stop = () => {
      vim.dispose();
      status.replaceChildren();
    };
  };
  const off = onNativeChange(() => void sync());
  void sync();
  return {
    dispose() {
      gone = true;
      off();
      void sync();
    },
  };
}
