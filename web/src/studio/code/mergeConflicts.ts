// Conflict markers in a file: colours, and Accept Current / Incoming / Both above each conflict.

import { monaco } from './monaco';

type Editor = monaco.editor.IStandaloneCodeEditor;
type Model = monaco.editor.ITextModel;

export interface Conflict {
  /** Lines from 1: `<<<<<<<`, the optional `|||||||`, `=======`, `>>>>>>>`. */
  start: number;
  base: number;
  sep: number;
  end: number;
}

export type Choice = 'current' | 'incoming' | 'both';

export function parseConflicts(lines: string[]): Conflict[] {
  const out: Conflict[] = [];
  let cur: Partial<Conflict> | null = null;
  lines.forEach((text, i) => {
    const n = i + 1;
    if (text.startsWith('<<<<<<< ') || text === '<<<<<<<') cur = { start: n, base: 0 };
    else if (cur && !cur.sep && text.startsWith('||||||| ')) cur.base = n;
    else if (cur && !cur.sep && text === '=======') cur.sep = n;
    else if (cur?.sep && (text.startsWith('>>>>>>> ') || text === '>>>>>>>')) {
      out.push({ start: cur.start!, base: cur.base!, sep: cur.sep, end: n });
      cur = null;
    }
  });
  return out;
}

/** The lines that stay when a conflict is settled. */
export function chosenLines(lines: string[], c: Conflict, choice: Choice): string[] {
  const current = lines.slice(c.start, (c.base || c.sep) - 1);
  const incoming = lines.slice(c.sep, c.end - 1);
  if (choice === 'current') return current;
  if (choice === 'incoming') return incoming;
  return [...current, ...incoming];
}

function take(model: Model, start: number, choice: Choice) {
  const lines = model.getLinesContent();
  const c = parseConflicts(lines).find((x) => x.start === start);
  if (!c) return;
  const keep = chosenLines(lines, c, choice).join(model.getEOL());
  const last = model.getLineCount();
  // a conflict that was all of the file leaves no line behind
  const range = keep === '' && c.end < last ? new monaco.Range(c.start, 1, c.end + 1, 1) : new monaco.Range(c.start, 1, c.end, model.getLineMaxColumn(c.end));
  model.pushStackElement();
  model.pushEditOperations([], [{ range, text: keep }], () => null);
  model.pushStackElement();
}

const changed = new monaco.Emitter<monaco.languages.CodeLensProvider>();

const provider: monaco.languages.CodeLensProvider = {
  onDidChange: changed.event,
  provideCodeLenses(model) {
    const lenses = parseConflicts(model.getLinesContent()).flatMap((c) => {
      const range = new monaco.Range(c.start, 1, c.start, 1);
      const lens = (title: string, choice: Choice) => ({ range, command: { id: 'nb.conflict.take', title, arguments: [model.uri.toString(), c.start, choice] } });
      return [lens('Accept Current', 'current'), lens('Accept Incoming', 'incoming'), lens('Accept Both', 'both')];
    });
    return { lenses, dispose() {} };
  },
};

let registered = false;

export function registerConflicts() {
  if (registered) return;
  registered = true;
  monaco.languages.registerCodeLensProvider('*', provider);
  monaco.editor.registerCommand('nb.conflict.take', (_acc, uri: string, start: number, choice: Choice) => {
    const model = monaco.editor.getModels().find((m) => m.uri.toString() === uri);
    if (model) take(model, start, choice);
  });
}

export function watchConflicts(ed: Editor): monaco.IDisposable {
  const coll = ed.createDecorationsCollection();
  const paint = () => {
    const model = ed.getModel();
    if (!model) {
      coll.clear();
      return;
    }
    const line = (from: number, to: number, cls: string): monaco.editor.IModelDeltaDecoration[] =>
      to < from ? [] : [{ range: new monaco.Range(from, 1, to, 1), options: { isWholeLine: true, className: cls } }];
    coll.set(
      parseConflicts(model.getLinesContent()).flatMap((c) => [
        ...line(c.start, c.start, 'wb-conflict-mark'),
        ...line(c.start + 1, (c.base || c.sep) - 1, 'wb-conflict-current'),
        ...(c.base ? line(c.base, c.sep - 1, 'wb-conflict-base') : []),
        ...line(c.sep, c.sep, 'wb-conflict-mark'),
        ...line(c.sep + 1, c.end - 1, 'wb-conflict-incoming'),
        ...line(c.end, c.end, 'wb-conflict-mark'),
      ]),
    );
  };
  paint();
  const subs = [
    ed.onDidChangeModel(paint),
    ed.onDidChangeModelContent(() => {
      paint();
      changed.fire(provider);
    }),
  ];
  return { dispose: () => { subs.forEach((s) => s.dispose()); coll.clear(); } };
}
