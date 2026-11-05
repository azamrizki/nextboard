// Bookmarks: lines to come back to, per project. A bookmark follows its line while the file is edited.

import { prefs } from '../../lib/format';
import { monaco } from './monaco';
import { nameOf, type Workspace } from './workspace';

type Editor = monaco.editor.IStandaloneCodeEditor;

export interface Bookmark {
  path: string;
  line: number;
}

export const bookmarksKey = (pid: string) => `wb.bookmarks.${pid}`;

const save = (ws: Workspace) => prefs.set(bookmarksKey(ws.pid), JSON.stringify(ws.bookmarks));

export function toggleBookmark(ws: Workspace, path: string, line: number) {
  const at = ws.bookmarks.findIndex((b) => b.path === path && b.line === line);
  ws.bookmarks = at >= 0 ? ws.bookmarks.filter((_, i) => i !== at) : [...ws.bookmarks, { path, line }];
  save(ws);
  ws.changed();
}

const inOrder = (list: Bookmark[]) => [...list].sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line);

/** Go to the next or the previous bookmark, around the end. */
export function jumpBookmark(ws: Workspace, path: string | undefined, line: number, step: 1 | -1) {
  const list = inOrder(ws.bookmarks);
  if (!list.length) return;
  const here = (b: Bookmark) => (path ? b.path.localeCompare(path) || b.line - line : 1);
  const next = step > 0 ? (list.find((b) => here(b) > 0) ?? list[0]) : ([...list].reverse().find((b) => here(b) < 0) ?? list[list.length - 1]);
  ws.open(next.path, { pin: true, source: true, line: next.line });
}

export function listBookmarks(ws: Workspace) {
  ws.actions.pick?.(
    'Bookmarks',
    inOrder(ws.bookmarks).map((b) => ({
      label: `${nameOf(b.path)}:${b.line}`,
      detail: ws.rel(b.path),
      run: () => ws.open(b.path, { pin: true, source: true, line: b.line }),
    })),
  );
}

export function watchBookmarks(ed: Editor, ws: Workspace): monaco.IDisposable {
  const coll = ed.createDecorationsCollection();
  let shown = '';
  const paint = () => {
    const path = ed.getModel()?.uri.path;
    const lines = path ? ws.bookmarks.filter((b) => b.path === path).map((b) => b.line) : [];
    const print = `${path}:${lines.join()}`;
    if (print === shown) return;
    shown = print;
    coll.set(lines.map((line) => ({ range: new monaco.Range(line, 1, line, 1), options: { isWholeLine: true, stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges, glyphMarginClassName: 'wb-bookmark-glyph', glyphMarginHoverMessage: { value: 'Bookmark' }, overviewRuler: { color: '#6e95ff', position: monaco.editor.OverviewRulerLane.Left } } })));
  };
  const subs = [
    ed.onDidChangeModel(paint),
    // the marks moved with the text: the bookmarks go along
    ed.onDidChangeModelContent(() => {
      const path = ed.getModel()?.uri.path;
      if (!path) return;
      const lines = coll.getRanges().map((r) => r.startLineNumber);
      const mine = ws.bookmarks.filter((b) => b.path === path);
      if (lines.length !== mine.length || lines.every((l, i) => l === mine[i].line)) return;
      ws.bookmarks = [...ws.bookmarks.filter((b) => b.path !== path), ...lines.map((line) => ({ path, line }))];
      save(ws);
      shown = `${path}:${lines.join()}`;
    }),
  ];
  const off = ws.subscribe(paint);
  paint();
  return { dispose: () => { off(); subs.forEach((s) => s.dispose()); coll.clear(); } };
}
