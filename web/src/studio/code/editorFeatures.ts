// Everything that follows an editor because an extension asked for it (nativeExt.ts).

import { watchBookmarks } from './bookmarks';
import { watchErrorLens } from './errorLens';
import { registerESLintActions, watchESLint } from './eslintRun';
import { registerGitLens, watchGitLens } from './gitLens';
import { registerConflicts, watchConflicts } from './mergeConflicts';
import type { monaco } from './monaco';
import { modelOwners } from './nativeExt';
import './prettierFormat';
import { watchVim } from './vimMode';
import type { Workspace } from './workspace';

type Editor = monaco.editor.IStandaloneCodeEditor;

export function attachFeatures(ed: Editor, ws: Workspace, status: HTMLElement, gi: () => number): monaco.IDisposable {
  registerESLintActions();
  registerGitLens();
  registerConflicts();
  const own = () => {
    const m = ed.getModel();
    if (m) modelOwners.set(m, ws);
  };
  own();
  const subs = [
    ed.onDidChangeModel(own),
    ed.onDidChangeCursorSelection(() => ws.ide.selectionFrom(ed)),
    ed.onDidFocusEditorText(() => ws.ide.selectionFrom(ed)),
    ed.onDidChangeModel(() => ws.ide.selectionFrom(ed)),
    watchErrorLens(ed),
    watchESLint(ed, ws),
    watchGitLens(ed, ws),
    watchConflicts(ed),
    watchBookmarks(ed, ws),
    watchVim(ed, status, {
      save: () => {
        const m = ed.getModel();
        if (m) void ws.save(m.uri.path);
      },
      close: () => {
        const g = gi();
        const id = ws.groups[g]?.active;
        if (id) ws.closeTab(g, id);
      },
    }),
  ];
  return { dispose: () => subs.forEach((s) => s.dispose()) };
}
