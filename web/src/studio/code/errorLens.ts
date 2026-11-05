// Error Lens: the worst problem of a line, in words at its end, and the line tinted.

import { monaco } from './monaco';
import { hasNative, onNativeChange } from './nativeExt';

type Editor = monaco.editor.IStandaloneCodeEditor;

const MAX_TEXT = 200;
const TONES: [monaco.MarkerSeverity, string][] = [
  [monaco.MarkerSeverity.Error, 'error'],
  [monaco.MarkerSeverity.Warning, 'warning'],
  [monaco.MarkerSeverity.Info, 'info'],
];

/** The decorations for a file's markers, at most one message per line. */
export function lensDecorations(model: monaco.editor.ITextModel, markers: monaco.editor.IMarker[]): monaco.editor.IModelDeltaDecoration[] {
  const lines = new Map<number, monaco.editor.IMarker[]>();
  const last = model.getLineCount();
  for (const m of markers) {
    if (m.severity < monaco.MarkerSeverity.Info || m.startLineNumber > last) continue;
    lines.set(m.startLineNumber, [...(lines.get(m.startLineNumber) ?? []), m]);
  }
  const out: monaco.editor.IModelDeltaDecoration[] = [];
  for (const [line, list] of lines) {
    const worst = list.reduce((a, b) => (b.severity > a.severity ? b : a));
    const tone = TONES.find(([s]) => s === worst.severity)?.[1] ?? 'info';
    const first = worst.message.split('\n')[0].trim();
    const more = list.length > 1 ? ` (+${list.length - 1})` : '';
    const text = first.length > MAX_TEXT ? `${first.slice(0, MAX_TEXT)}…` : first;
    const end = model.getLineMaxColumn(line);
    out.push(
      { range: new monaco.Range(line, 1, line, 1), options: { isWholeLine: true, className: `wb-lens-line is-${tone}` } },
      {
        range: new monaco.Range(line, end, line, end),
        options: {
          showIfCollapsed: true,
          after: { content: `    ${text}${more}`, inlineClassName: `wb-lens-text is-${tone}`, cursorStops: monaco.editor.InjectedTextCursorStops.None },
        },
      },
    );
  }
  return out;
}

export function watchErrorLens(ed: Editor): monaco.IDisposable {
  const coll = ed.createDecorationsCollection();
  let timer = 0;
  const refresh = () => {
    window.clearTimeout(timer);
    const model = ed.getModel();
    if (!model || !hasNative('errorlens')) {
      coll.clear();
      return;
    }
    coll.set(lensDecorations(model, monaco.editor.getModelMarkers({ resource: model.uri })));
  };
  const soon = () => {
    window.clearTimeout(timer);
    timer = window.setTimeout(refresh, 80);
  };
  const subs = [
    monaco.editor.onDidChangeMarkers((uris) => {
      const uri = ed.getModel()?.uri.toString();
      if (uri && uris.some((u) => u.toString() === uri)) soon();
    }),
    ed.onDidChangeModel(refresh),
    // a line's end moves as it is typed in, and the message with it
    ed.onDidChangeModelContent(soon),
  ];
  const off = onNativeChange(refresh);
  refresh();
  return {
    dispose() {
      window.clearTimeout(timer);
      subs.forEach((s) => s.dispose());
      off();
      coll.clear();
    },
  };
}
