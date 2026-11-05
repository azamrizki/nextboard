// GitLens: who last changed a line at its end, the file's authors above it, blame for the whole
// file, and the history of a file or a line, all from the machine's git.

import { errorMessage } from '../../api';
import { timeAgo } from '../../lib/format';
import { toast } from '../../components/Toast';
import { gitApi, type GitBlame, type GitBlameCommit } from '../api';
import { monaco } from './monaco';
import { hasNative, modelOwners, onNativeChange } from './nativeExt';
import { nameOf, type Workspace } from './workspace';

type Editor = monaco.editor.IStandaloneCodeEditor;
type Model = monaco.editor.ITextModel;

const CURSOR_MS = 150;
const TYPING_MS = 800;

interface Cached {
  key: string;
  blame: Promise<GitBlame | null>;
  /** The last answer, kept for the file's summary while a newer one is on its way. */
  latest: GitBlame | null;
}
const cache = new WeakMap<Model, Cached>();

const lensChanged = new monaco.Emitter<monaco.languages.CodeLensProvider>();

export const isUncommitted = (c: GitBlameCommit) => /^0+$/.test(c.hash);

const blameKey = (ws: Workspace, model: Model) => `${ws.gitVersion}:${model.getVersionId()}`;

function blameOf(ws: Workspace, model: Model): Promise<GitBlame | null> {
  const key = blameKey(ws, model);
  const old = cache.get(model);
  if (old?.key === key) return old.blame;
  const rel = ws.repoRel(model.uri.path);
  if (!rel) return Promise.resolve(null);
  const dirty = ws.docs.get(model.uri.path)?.dirty;
  const entry: Cached = { key, latest: old?.latest ?? null, blame: Promise.resolve(null) };
  entry.blame = gitApi
    .blame(ws.gitTarget, rel, dirty ? model.getValue() : undefined)
    .then((b) => {
      entry.latest = b;
      lensChanged.fire(lensProvider);
      return b;
    })
    .catch(() => null);
  cache.set(model, entry);
  return entry.blame;
}

/** Commit text comes from whoever wrote the commit: as words, never as markdown. */
export const plainMarkdown = (s: string) => s.replace(/[\\`*_{}[\]()#+\-.!<>|~]/g, '\\$&');

export function whoAndWhen(c: GitBlameCommit, me: string): string {
  if (isUncommitted(c)) return 'You, uncommitted changes';
  return `${c.email && c.email === me ? 'You' : c.author}, ${timeAgo(c.at)}`;
}

function hoverOf(c: GitBlameCommit, me: string, path: string): monaco.IMarkdownString {
  if (isUncommitted(c)) return { value: 'Not committed yet' };
  const args = encodeURIComponent(JSON.stringify([path, c.hash]));
  return {
    value: `**${plainMarkdown(c.author)}**${c.email && c.email === me ? ' (you)' : ''}, ${plainMarkdown(new Date(c.at).toLocaleString())}\n\n${plainMarkdown(c.summary)}\n\n\`${c.hash.slice(0, 8)}\` · [Open changes](command:nb.gitlens.openDiff?${args})`,
    isTrusted: { enabledCommands: ['nb.gitlens.openDiff'] },
  };
}

// ---- blame of the current line ------------------------------------------------------

export function watchGitLens(ed: Editor, ws: Workspace): monaco.IDisposable {
  const coll = ed.createDecorationsCollection();
  let timer = 0;
  let turn = 0;
  const show = async () => {
    const me = ++turn;
    const model = ed.getModel();
    const line = ed.getPosition()?.lineNumber;
    if (!model || !line || !hasNative('gitlens') || model.uri.scheme !== 'file') {
      coll.clear();
      return;
    }
    const blame = await blameOf(ws, model);
    if (me !== turn || ed.getModel() !== model) return;
    const c = blame?.commits[blame.lines[line - 1]];
    if (!blame || !c || line > model.getLineCount()) {
      coll.clear();
      return;
    }
    const end = model.getLineMaxColumn(line);
    const text = `      ${whoAndWhen(c, blame.me)}${isUncommitted(c) ? '' : ` • ${c.summary}`}`;
    coll.set([
      {
        range: new monaco.Range(line, end, line, end),
        options: {
          showIfCollapsed: true,
          hoverMessage: hoverOf(c, blame.me, model.uri.path),
          after: { content: text.length > 160 ? `${text.slice(0, 160)}…` : text, inlineClassName: 'wb-blame-text', cursorStops: monaco.editor.InjectedTextCursorStops.None },
        },
      },
    ]);
  };
  const later = (ms: number) => {
    window.clearTimeout(timer);
    timer = window.setTimeout(() => void show(), ms);
  };
  const subs = [
    ed.onDidChangeCursorPosition(() => later(CURSOR_MS)),
    ed.onDidChangeModel(() => later(0)),
    ed.onDidChangeModelContent(() => {
      // quiet while typing, as the words would jump along
      turn++;
      coll.clear();
      later(TYPING_MS);
    }),
  ];
  let gitVersion = ws.gitVersion;
  const offWs = ws.subscribe(() => {
    if (gitVersion === ws.gitVersion) return;
    gitVersion = ws.gitVersion;
    later(CURSOR_MS);
  });
  const off = onNativeChange(() => later(0));
  later(0);
  return {
    dispose() {
      turn++;
      window.clearTimeout(timer);
      subs.forEach((s) => s.dispose());
      offWs();
      off();
      coll.clear();
    },
  };
}

// ---- blame of the whole file --------------------------------------------------------

const fileBlame = new WeakMap<Editor, () => void>();

export function blameGutter(blame: GitBlame): (string | null)[] {
  const out: (string | null)[] = [];
  let prev = -1;
  for (const i of blame.lines) {
    if (i === prev) {
      out.push(null);
      continue;
    }
    prev = i;
    const c = blame.commits[i];
    out.push(isUncommitted(c) ? 'Uncommitted' : `${c.hash.slice(0, 7)} ${c.author} · ${timeAgo(c.at)}`);
  }
  return out;
}

const GUTTER_WIDTH = 32;

export async function toggleFileBlame(ws: Workspace, ed: Editor) {
  const stop = fileBlame.get(ed);
  if (stop) {
    stop();
    return;
  }
  const model = ed.getModel();
  if (!model) return;
  const blame = await blameOf(ws, model);
  if (!blame?.lines.length) {
    toast.info('Git knows nothing about this file yet.');
    return;
  }
  const coll = ed.createDecorationsCollection();
  const lines = blameGutter(blame);
  coll.set(
    lines.flatMap((text, i) => {
      const line = i + 1;
      if (line > model.getLineCount()) return [];
      const shown = (text ?? '').slice(0, GUTTER_WIDTH - 1).padEnd(GUTTER_WIDTH, ' ');
      return [
        {
          range: new monaco.Range(line, 1, line, 1),
          options: { showIfCollapsed: true, before: { content: shown, inlineClassName: `wb-blame-gutter ${text ? 'is-first' : ''}`, cursorStops: monaco.editor.InjectedTextCursorStops.None } },
        },
      ];
    }),
  );
  // it says who wrote these lines, so it ends once they change or another file shows
  const subs = [ed.onDidChangeModel(() => fileBlame.get(ed)?.()), ed.onDidChangeModelContent(() => fileBlame.get(ed)?.())];
  fileBlame.set(ed, () => {
    subs.forEach((s) => s.dispose());
    coll.clear();
    fileBlame.delete(ed);
  });
}

// ---- history ------------------------------------------------------------------------

export async function showHistory(ws: Workspace, path: string, line?: number) {
  const rel = ws.repoRel(path);
  if (!rel) {
    toast.info('This file is not in the selected Git repository.');
    return;
  }
  try {
    const items = await gitApi.fileLog(ws.gitTarget, rel, line);
    if (!items.length) {
      toast.info('Git has no history for this yet.');
      return;
    }
    ws.actions.pick?.(
      line ? `History of ${nameOf(path)}:${line}` : `History of ${nameOf(path)}`,
      items.map((c) => ({
        label: c.subject,
        detail: `${c.short} · ${c.author} · ${timeAgo(c.at)}`,
        run: () => ws.openDiff(path, `commit:${c.hash}`),
      })),
    );
  } catch (err) {
    toast.error(`History: ${errorMessage(err)}`);
  }
}

// ---- what lives once for all editors ------------------------------------------------

function fileSummary(blame: GitBlame): { last: string; authors: number } | null {
  const known = blame.lines.map((i) => blame.commits[i]).filter((c) => c && !isUncommitted(c));
  if (!known.length) return null;
  const newest = known.reduce((a, b) => (b.at > a.at ? b : a));
  return { last: whoAndWhen(newest, blame.me), authors: new Set(known.map((c) => c.email || c.author)).size };
}

const lensProvider: monaco.languages.CodeLensProvider = {
  onDidChange: lensChanged.event,
  provideCodeLenses(model) {
    const none = { lenses: [], dispose() {} };
    const ws = modelOwners.get(model);
    if (!ws || !hasNative('gitlens') || model.uri.scheme !== 'file') return none;
    // while unsaved, the last answer will do: this runs on every pause in typing
    if (!ws.docs.get(model.uri.path)?.dirty) void blameOf(ws, model);
    const blame = cache.get(model)?.latest;
    const sum = blame && fileSummary(blame);
    if (!sum) return none;
    const range = new monaco.Range(1, 1, 1, 1);
    const open = (title: string, id: string) => ({ range, id, command: { id: 'nb.gitlens.history', title, arguments: [model.uri.toString()] } });
    return { lenses: [open(sum.last, 'nb-last'), open(`${sum.authors} author${sum.authors === 1 ? '' : 's'}`, 'nb-authors')], dispose() {} };
  },
};

let done = false;

export function registerGitLens() {
  if (done) return;
  done = true;
  monaco.languages.registerCodeLensProvider('*', lensProvider);
  monaco.editor.registerCommand('nb.gitlens.history', (_acc, uri: string) => {
    const model = monaco.editor.getModel(monaco.Uri.parse(uri));
    const ws = model && modelOwners.get(model);
    if (ws) void showHistory(ws, model.uri.path);
  });
  monaco.editor.registerCommand('nb.gitlens.openDiff', (_acc, path: string, hash: string) => {
    const model = monaco.editor.getModels().find((m) => m.uri.path === path);
    const ws = model && modelOwners.get(model);
    if (ws && /^[0-9a-f]{7,64}$/.test(hash)) ws.openDiff(path, `commit:${hash}`);
  });
  onNativeChange(() => lensChanged.fire(lensProvider));
}
