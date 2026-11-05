// ESLint: the hub runs the project's own eslint over a file as it is in the editor, and the
// answers become markers and quick fixes. Its config is code from the project, so nothing runs
// until the user has allowed it for that project (docs/API-studio.md §7.6).

import { errorMessage } from '../../api';
import { prefs } from '../../lib/format';
import { toast } from '../../components/Toast';
import { filesApi, type EslintMessage } from '../api';
import { monaco } from './monaco';
import { hasNative, onNativeChange } from './nativeExt';
import type { Workspace } from './workspace';

type Editor = monaco.editor.IStandaloneCodeEditor;
type Model = monaco.editor.ITextModel;

export type EslintState = 'ask' | 'off' | 'running' | 'ok' | 'failed' | 'missing';

interface WsState {
  state: EslintState;
  detail: string;
  asked: boolean;
  missingUntil: number;
}

const OWNER = 'eslint';
const DEBOUNCE_MS = 1000;
const MISSING_RETRY_MS = 60_000;
const LINTABLE = /\.(?:[cm]?[jt]sx?|vue)$/i;

const states = new WeakMap<Workspace, WsState>();
const stateOf = (ws: Workspace): WsState => {
  let s = states.get(ws);
  if (!s) {
    s = { state: 'ask', detail: '', asked: false, missingUntil: 0 };
    states.set(ws, s);
  }
  return s;
};

const prefKey = (ws: Workspace) => `wb.eslint.${ws.pid}`;
const allowed = (ws: Workspace) => prefs.get(prefKey(ws)) === '1';

const setState = (ws: Workspace, state: EslintState, detail = '') => {
  const s = stateOf(ws);
  if (s.state === state && s.detail === detail) return;
  s.state = state;
  s.detail = detail;
  ws.changed();
};

/** What the status bar shows for a file, or null when ESLint has no say in it. */
export function eslintStatus(ws: Workspace, path: string | undefined): { state: EslintState; detail: string } | null {
  if (!path || !hasNative('eslint') || !LINTABLE.test(path)) return null;
  const s = stateOf(ws);
  if (!allowed(ws)) return { state: prefs.get(prefKey(ws)) === '0' ? 'off' : 'ask', detail: '' };
  return { state: s.state === 'ask' || s.state === 'off' ? 'running' : s.state, detail: s.detail };
}

interface ModelState {
  version: number;
  messages: EslintMessage[];
  timer: number;
  running: boolean;
  again: boolean;
}
const models = new WeakMap<Model, ModelState>();
const modelState = (m: Model): ModelState => {
  let s = models.get(m);
  if (!s) {
    s = { version: -1, messages: [], timer: 0, running: false, again: false };
    models.set(m, s);
  }
  return s;
};

/** ESLint's answers as markers; lines and columns count from 1 in both. */
export function toMarkers(messages: EslintMessage[]): monaco.editor.IMarkerData[] {
  return messages
    .filter((m) => !/^File ignored/.test(m.message))
    .map((m) => {
      const line = m.line ?? 1;
      const col = m.column ?? 1;
      return {
        startLineNumber: line,
        startColumn: col,
        endLineNumber: m.endLine ?? line,
        endColumn: m.endColumn ?? col + 1,
        severity: m.severity === 2 ? monaco.MarkerSeverity.Error : monaco.MarkerSeverity.Warning,
        message: m.message,
        source: 'eslint',
        code: m.ruleId ?? undefined,
      };
    });
}

async function lint(ws: Workspace, model: Model) {
  if (model.isDisposed() || !hasNative('eslint') || !allowed(ws) || !LINTABLE.test(model.uri.path) || !ws.inside(model.uri.path)) return;
  const wst = stateOf(ws);
  if (Date.now() < wst.missingUntil) return;
  const st = modelState(model);
  if (st.running) {
    st.again = true;
    return;
  }
  st.running = true;
  setState(ws, 'running');
  const version = model.getVersionId();
  try {
    const r = await filesApi.eslint(ws.pid, ws.rel(model.uri.path), model.getValue());
    if (model.isDisposed()) return;
    if (!r.available) {
      wst.missingUntil = Date.now() + MISSING_RETRY_MS;
      monaco.editor.setModelMarkers(model, OWNER, []);
      setState(ws, 'missing');
    } else if (r.error) {
      monaco.editor.setModelMarkers(model, OWNER, []);
      ws.log(`[ESLint] ${r.error}`);
      setState(ws, 'failed', r.error);
    } else if (model.getVersionId() === version) {
      // answers to older text would land on the wrong lines, and the run below follows
      st.version = version;
      st.messages = r.messages ?? [];
      monaco.editor.setModelMarkers(model, OWNER, toMarkers(st.messages));
      setState(ws, 'ok');
    }
  } catch (err) {
    setState(ws, 'failed', errorMessage(err));
  } finally {
    st.running = false;
    if (st.again || (!model.isDisposed() && model.getVersionId() !== version)) {
      st.again = false;
      schedule(ws, model);
    }
  }
}

function schedule(ws: Workspace, model: Model) {
  const st = modelState(model);
  window.clearTimeout(st.timer);
  st.timer = window.setTimeout(() => void lint(ws, model), DEBOUNCE_MS);
}

/** Answer the question "may this project's eslint run". */
export function allowESLint(ws: Workspace, on: boolean) {
  prefs.set(prefKey(ws), on ? '1' : '0');
  stateOf(ws).missingUntil = 0;
  for (const ed of ws.editors.values()) {
    const m = ed.getModel();
    if (!m) continue;
    if (on) schedule(ws, m);
    else monaco.editor.setModelMarkers(m, OWNER, []);
  }
  ws.changed();
}

function askOnce(ws: Workspace) {
  const s = stateOf(ws);
  if (s.asked || prefs.get(prefKey(ws)) !== null) return;
  s.asked = true;
  toast.notify('info', 'Run this project’s ESLint?', 'It loads the project’s ESLint config, and that config is code that runs on the machine.', { label: 'Allow', onClick: () => allowESLint(ws, true) });
}

export function watchESLint(ed: Editor, ws: Workspace): monaco.IDisposable {
  const start = () => {
    const m = ed.getModel();
    if (!m || !hasNative('eslint') || !LINTABLE.test(m.uri.path)) return;
    if (allowed(ws)) void lint(ws, m);
    else askOnce(ws);
  };
  const subs = [
    ed.onDidChangeModel(start),
    ed.onDidChangeModelContent(() => {
      const m = ed.getModel();
      if (m && hasNative('eslint') && allowed(ws)) schedule(ws, m);
    }),
  ];
  const off = onNativeChange(() => {
    const m = ed.getModel();
    if (!m) return;
    if (hasNative('eslint')) start();
    else monaco.editor.setModelMarkers(m, OWNER, []);
  });
  start();
  return {
    dispose() {
      subs.forEach((s) => s.dispose());
      off();
    },
  };
}

/** Fix everything ESLint can, through its own --fix. */
export async function eslintFixAll(ws: Workspace, ed: Editor) {
  const model = ed.getModel();
  if (!model || !allowed(ws)) return;
  const version = model.getVersionId();
  try {
    const r = await filesApi.eslint(ws.pid, ws.rel(model.uri.path), model.getValue(), true);
    if (r.error) throw new Error(r.error);
    if (typeof r.output !== 'string' || model.getVersionId() !== version) return;
    ed.pushUndoStop();
    ed.executeEdits(OWNER, [{ range: model.getFullModelRange(), text: r.output }], ed.getSelections() ?? undefined);
    ed.pushUndoStop();
  } catch (err) {
    toast.error(`ESLint: ${errorMessage(err).split('\n')[0]}`);
  }
}

let providerDone = false;

function fixAction(model: Model, m: EslintMessage, marker: monaco.editor.IMarkerData): monaco.languages.CodeAction | null {
  if (!m.fix) return null;
  const a = model.getPositionAt(m.fix.range[0]);
  const b = model.getPositionAt(m.fix.range[1]);
  return {
    title: `Fix this ${m.ruleId ?? 'problem'}`,
    kind: 'quickfix',
    diagnostics: [marker],
    isPreferred: true,
    edit: { edits: [{ resource: model.uri, versionId: undefined, textEdit: { range: new monaco.Range(a.lineNumber, a.column, b.lineNumber, b.column), text: m.fix.text } }] },
  };
}

function disableAction(model: Model, m: EslintMessage, marker: monaco.editor.IMarkerData): monaco.languages.CodeAction | null {
  if (!m.ruleId) return null;
  const line = marker.startLineNumber;
  const indent = model.getLineContent(line).match(/^\s*/)?.[0] ?? '';
  return {
    title: `Disable ${m.ruleId} for this line`,
    kind: 'quickfix',
    diagnostics: [marker],
    edit: { edits: [{ resource: model.uri, versionId: undefined, textEdit: { range: new monaco.Range(line, 1, line, 1), text: `${indent}// eslint-disable-next-line ${m.ruleId}\n` } }] },
  };
}

export function registerESLintActions() {
  if (providerDone) return;
  providerDone = true;
  for (const lang of ['javascript', 'typescript', 'vue', 'html']) {
    monaco.languages.registerCodeActionProvider(lang, {
      provideCodeActions(model, range) {
        const st = models.get(model);
        const actions: monaco.languages.CodeAction[] = [];
        if (hasNative('eslint') && st && st.version === model.getVersionId()) {
          for (const m of st.messages) {
            const marker = toMarkers([m])[0];
            if (!marker || !monaco.Range.areIntersectingOrTouching(marker as monaco.IRange, range)) continue;
            const fix = fixAction(model, m, marker);
            if (fix) actions.push(fix);
            const off = disableAction(model, m, marker);
            if (off) actions.push(off);
          }
        }
        return { actions, dispose() {} };
      },
    });
  }
}
