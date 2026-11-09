// Code workbench (docs/API-studio.md §7): the project's files in an editor of VS Code's
// class, as a card on the board. Activity bar, side bar (Explorer, Search, Source Control,
// Run and Debug, Extensions, Ask, Settings), editor groups, bottom panel, status bar, Go to
// file and the command palette. Monaco is the editor; language servers and debug adapters
// on the project's machine add the rest; files stay on the machine, written with ifMtime.
//
// Its width decides its layout (DESIGN.md §3.5): under 1000 px the agents side bar lies over the
// editor, under 640 px (a phone) the side bar does too and closes when it opens a file, and only
// the current editor group shows. On a touch screen a key bar comes with the keyboard.

import { useReactFlow } from '@xyflow/react';
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type PointerEvent as ReactPointerEvent } from 'react';
import { createPortal } from 'react-dom';
import { api, errorMessage } from '../../api';
import type { BoardController } from '../../board/controller';
import { rectOf } from '../../board/controller';
import { snapshotOf } from '../../board/files';
import { DEFAULT_SIZE, SNAPSHOT_MAX } from '../../board/kinds';
import { placeNear } from '../../board/layout';
import { pushAside, type Moved } from '../../board/pushAside';
import { TileHead, useSize } from '../../board/tiles/common';
import { KeyBar } from '../../board/KeyBar';
import { useLayout } from '../../lib/layout';
import { EditorKeyBar } from './EditorKeyBar';
import { Button } from '../../components/Button';
import { Icon, type IconName } from '../../components/Icon';
import { Modal } from '../../components/Modal';
import { useUi, type UiStore } from '../../board/ui';
import { toast } from '../../components/Toast';
import type { NodeLayout, Op, Project } from '../../lib/types';
import { useFullscreen } from '../../lib/useFullscreen';
import { useRemoteConfirm } from '../confirm';
import { BottomPanel, newTerminal, useProblems } from './BottomPanel';
import { DebugDialogs, DebugToolbar, DebugView, registerDebugHover } from './DebugPanel';
import { EditorArea } from './EditorArea';
import { Explorer } from './Explorer';
import { applyTheme, loadExtensions, useBuiltinTheme } from './extensions';
import { store } from '../../store';
import { registerEditorFocus, takePendingPane } from '../../agents/jump';
import { AgentSideBar, ClosePaneDialog, PaneEnv, startAgentPane, usePaneChoices } from './AgentPanes';
import { allowESLint, eslintFixAll, eslintStatus, type EslintState } from './eslintRun';
import { ExtensionsPanel } from './ExtensionsPanel';
import { taskApi } from '../api';
import { showHistory, toggleFileBlame } from './gitLens';
import { DEFAULT_WEB_SIZE, webNode } from '../../web/model';
import { jumpBookmark, listBookmarks, toggleBookmark } from './bookmarks';
import { LaravelPanel } from './LaravelPanel';
import { registerLaravel } from './laravel';
import { ReviewPanel } from './ReviewPanel';
import { runTask } from './TasksPanel';
import { hasNative } from './nativeExt';
import { registerInlineAi } from './inlineAi';
import { BUILTIN_THEME, currentTheme, languageLabel, monaco, setupMonaco } from './monaco';
import { QuickOpen, type Command, type PickItem } from './QuickOpen';
import { ScmPanel } from './ScmPanel';
import { SearchPanel } from './SearchPanel';
import { AgentsPanel, SettingsPanel, type AgentContext, type AgentRequest } from './SidePanels';
import { useWs } from './useWs';
import { dirOf, nameOf, ORCHESTRATOR_PANE, Workspace, type SideView } from './workspace';
import '../../styles/studio.css';
import '../../styles/workbench.css';

interface Props {
  project: Project;
  ctl: BoardController;
  ui: UiStore;
  /** The board node this workbench is the content of. */
  nodeId: string;
  viewCenter: () => { x: number; y: number };
  /** The card fills the board view: node data "fill" keeps the rect to go back to. */
  filled: boolean;
}

/** How much a board's zoom scales this element: pointer distances come in screen pixels. */
const scaleOf = (el: Element | null) => {
  const w = (el as HTMLElement | null)?.offsetWidth;
  return el && w ? el.getBoundingClientRect().width / w : 1;
};

const ACTIVITY: { view: SideView; icon: IconName; label: string; keys?: string }[] = [
  { view: 'files', icon: 'file', label: 'Explorer', keys: 'Ctrl+Shift+E' },
  { view: 'search', icon: 'search', label: 'Search', keys: 'Ctrl+Shift+F' },
  { view: 'scm', icon: 'branch', label: 'Source Control', keys: 'Ctrl+Shift+G' },
  { view: 'review', icon: 'checklist', label: 'Review' },
  { view: 'debug', icon: 'bug', label: 'Run and Debug', keys: 'Ctrl+Shift+D' },
  { view: 'laravel', icon: 'layers', label: 'Laravel' },
  { view: 'extensions', icon: 'puzzle', label: 'Extensions', keys: 'Ctrl+Shift+X' },
  { view: 'agents', icon: 'sparkle', label: 'Ask', keys: 'Ctrl+L' },
];

type PickKind = 'language' | 'indent' | 'eol' | 'encoding' | 'theme';

const ENCODINGS: [string, string][] = [
  ['windows-1252', 'Western'],
  ['iso-8859-1', 'Western (Latin-1)'],
  ['iso-8859-15', 'Western (Latin-9)'],
  ['utf-16le', 'UTF-16 LE'],
  ['utf-16be', 'UTF-16 BE'],
  ['shift_jis', 'Japanese'],
  ['euc-jp', 'Japanese'],
  ['gb18030', 'Chinese'],
  ['big5', 'Traditional Chinese'],
  ['euc-kr', 'Korean'],
  ['windows-1251', 'Cyrillic'],
  ['koi8-r', 'Cyrillic'],
];

let openerWs: Workspace | null = null;
let openerDone = false;

/** Go to definition in another file ends up here. */
function registerOpener() {
  if (openerDone) return;
  openerDone = true;
  registerLaravel();
  monaco.editor.registerEditorOpener({
    openCodeEditor(_source, resource, target) {
      const ws = openerWs;
      if (!ws || resource.scheme !== 'file') return false;
      const pos = target && 'startLineNumber' in target ? { line: target.startLineNumber, col: target.startColumn } : target && 'lineNumber' in target ? { line: target.lineNumber, col: target.column } : {};
      ws.open(resource.path, { ...pos, source: true });
      return true;
    },
  });
}

const ESLINT_TEXT: Record<EslintState, string> = { ask: 'ESLint: allow?', off: 'ESLint: off', running: 'ESLint…', ok: 'ESLint', failed: 'ESLint failed', missing: 'No ESLint' };
const ESLINT_TITLE: Record<EslintState, string> = {
  ask: 'Run this project’s ESLint? Its config is code that runs on the machine. Click to allow.',
  off: 'ESLint does not run in this project. Click to allow it.',
  running: 'ESLint is checking this file.',
  ok: 'ESLint checked this file.',
  failed: 'ESLint could not check this file. Click to open the panel.',
  missing: 'This project and this machine have no eslint.',
};

function StatusBar({ ws, onPick, onView, onPanel }: { ws: Workspace; onPick: (kind: PickKind) => void; onView: (v: SideView) => void; onPanel: () => void }) {
  useWs(ws);
  useSyncExternalStore(ws.subscribeCursor, ws.getCursorVersion);
  const { errors, warnings } = useProblems(ws);
  const doc = ws.activeDoc;
  const model = doc?.model ?? null;
  const c = ws.cursor;
  const lang = model?.getLanguageId();
  const lsp = lang ? ws.lsp.stateFor(lang) : null;
  const eslint = eslintStatus(ws, doc?.path);
  const opts = model?.getOptions();
  const st = ws.git;
  return (
    <footer className="wb-status" aria-label="Status">
      {st?.isRepo && (
        <button type="button" onClick={() => onView('scm')} title="Source control">
          <Icon name="branch" />
          {st.branch || 'detached'}
          {st.ahead ? ` ↑${st.ahead}` : ''}
          {st.behind ? ` ↓${st.behind}` : ''}
        </button>
      )}
      <button
        type="button"
        title="Problems"
        onClick={() => {
          ws.panelOpen = true;
          ws.panelView = 'problems';
          ws.changed();
        }}
      >
        <Icon name="alertCircle" />
        <span className="tabular">{errors}</span>
        <Icon name="alert" />
        <span className="tabular">{warnings}</span>
      </button>
      {[...ws.docs.values()].some((d) => d.saving) && <span className="wb-status-note">Saving…</span>}
      {ws.debug.active && (
        <span className="wb-status-note wb-status-debug">
          <Icon name="bug" />
          {ws.debug.state === 'stopped' ? 'Paused' : ws.debug.state === 'starting' ? 'Starting…' : 'Debugging'}
        </span>
      )}
      {ws.aiBusy > 0 && (
        <span className="wb-status-note">
          <span className="spinner" aria-hidden="true" /> AI suggestion…
        </span>
      )}
      <span className="spacer" />
      {ws.ide.consoles.length > 0 && (
        <span className="wb-status-item" title={`Linked to this editor: ${ws.ide.consoles.map((c) => c.name).join(', ')}`}>
          <Icon name="link" /> {ws.ide.consoles.length === 1 ? ws.ide.consoles[0].name : `${ws.ide.consoles.length} Claude consoles`}
        </span>
      )}
      {eslint && (
        <button
          type="button"
          className={`wb-status-lsp is-${eslint.state === 'ok' ? 'ready' : eslint.state === 'failed' ? 'failed' : 'none'}`}
          title={ESLINT_TITLE[eslint.state] + (eslint.detail ? `\n${eslint.detail}` : '')}
          onClick={() => (eslint.state === 'ask' || eslint.state === 'off' ? allowESLint(ws, true) : eslint.state === 'failed' ? onPanel() : undefined)}
        >
          {eslint.state === 'running' ? <span className="spinner" aria-hidden="true" /> : eslint.state === 'ok' ? <Icon name="check" /> : eslint.state === 'failed' ? <Icon name="alert" /> : <Icon name="info" />}
          <span className="truncate">{ESLINT_TEXT[eslint.state]}</span>
        </button>
      )}
      {lsp && lsp.state !== 'none' && (
        <button type="button" className={`wb-status-lsp is-${lsp.state}`} title={lsp.state === 'missing' || lsp.state === 'builtin' ? `For the whole project, install: ${lsp.install}` : lsp.name} onClick={() => onView('settings')}>
          {lsp.state === 'ready' ? <Icon name="check" /> : lsp.state === 'starting' ? <span className="spinner" aria-hidden="true" /> : lsp.state === 'failed' ? <Icon name="alert" /> : <Icon name="info" />}
          <span className="truncate">{lsp.text}</span>
        </button>
      )}
      {model && (
        <>
          <span className="wb-status-item tabular">
            Ln {c.line}, Col {c.col}
            {c.selected ? ` (${c.selected} selected)` : ''}
          </span>
          <button type="button" className="wb-status-extra" onClick={() => onPick('indent')} title="Indentation">
            {opts?.insertSpaces ? 'Spaces' : 'Tab Size'}: {opts?.indentSize ?? opts?.tabSize}
          </button>
          {doc?.encoding ? (
            <button type="button" className="wb-status-extra" onClick={() => onPick('encoding')} title="Reopen with another encoding">
              {doc.encoding}
            </button>
          ) : (
            <span className="wb-status-item wb-status-extra">{doc?.bom ? 'UTF-8 with BOM' : 'UTF-8'}</span>
          )}
          <button type="button" className="wb-status-extra" onClick={() => onPick('eol')} title={doc?.mixedEol ? 'This file mixes line endings: saving makes them all the same' : 'End of line sequence'}>
            {model.getEOL() === '\r\n' ? 'CRLF' : 'LF'}
            {doc?.mixedEol ? ' (mixed)' : ''}
          </button>
          <button type="button" onClick={() => onPick('language')} title="Language">
            {lang ? languageLabel(lang) : ''}
          </button>
        </>
      )}
    </footer>
  );
}

function Workbench({ ws, project, ctl, nodeId, viewCenter, filled }: { ws: Workspace; project: Project; ctl: BoardController; nodeId: string; viewCenter: () => { x: number; y: number }; filled: boolean }) {
  useWs(ws);
  const rootRef = useRef<HTMLDivElement>(null);
  const clickedInside = useRef(false);
  const rf = useReactFlow();
  /** Where the view was before the card was made to fill it; a reload forgets this. */
  const viewBeforeFill = useRef<{ x: number; y: number; zoom: number } | null>(null);
  const [palette, setPalette] = useState<{ initial: string; pick?: { placeholder: string; items: PickItem[] } } | null>(null);
  const [searchTick, setSearchTick] = useState(0);
  const [agentsTick, setAgentsTick] = useState(0);
  const [aiOpen, setAiOpen] = useState(false);
  const [context, setContext] = useState<AgentContext | null>(null);
  const [agentRequest, setAgentRequest] = useState<AgentRequest | null>(null);
  const [confirmDialog, run] = useRemoteConfirm();
  const fullscreen = useFullscreen();
  ws.run = run;

  // the layout follows the card's width, not the screen's: a small card on a desktop is narrow too
  const bodyRef = useRef<HTMLDivElement>(null);
  const { w: bodyW } = useSize(bodyRef);
  const mode = !bodyW || bodyW >= 1000 ? 'wide' : bodyW < 640 ? 'narrow' : 'medium';
  const sideOver = mode === 'narrow';
  const rightOver = mode !== 'wide';
  // a file opened from a side bar that lies over the editor closes it: the file is what was wanted
  const activeTab = ws.activeTab?.id ?? null;
  const lastTab = useRef(activeTab);
  useEffect(() => {
    if (activeTab === lastTab.current) return;
    lastTab.current = activeTab;
    if (sideOver && ws.sideOpen && activeTab) {
      ws.sideOpen = false;
      ws.changed();
    }
  }, [activeTab, sideOver, ws]);

  // a touch screen: the keys its keyboard lacks, for the editor or a terminal that has the keyboard
  const coarse = useLayout().input === 'touch';
  const [typing, setTyping] = useState<string | null>(null);
  useEffect(() => {
    const root = rootRef.current;
    if (!root || !coarse) {
      setTyping(null);
      return;
    }
    let timer = 0;
    const read = () => {
      const a = document.activeElement as HTMLElement | null;
      let next: string | null = null;
      if (a && root.contains(a)) {
        // the editor's input: a textarea, or an EditContext element where the browser has it
        if (a.closest('.monaco-editor')) next = 'editor';
        else next = a.closest<HTMLElement>('.wb-terminal')?.dataset.session ?? null;
      }
      setTyping(next);
    };
    // focus leaves one element before it reaches the next
    const later = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(read, 0);
    };
    document.addEventListener('focusin', read);
    document.addEventListener('focusout', later);
    read();
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener('focusin', read);
      document.removeEventListener('focusout', later);
    };
  }, [coarse]);

  const showView = useCallback(
    (v: SideView) => {
      if (ws.side === v && ws.sideOpen && v !== 'search' && v !== 'agents') {
        ws.sideOpen = false;
      } else {
        ws.side = v;
        ws.sideOpen = true;
      }
      if (v === 'search') setSearchTick((n) => n + 1);
      if (v === 'agents') setAgentsTick((n) => n + 1);
      ws.changed();
    },
    [ws],
  );

  const togglePanel = useCallback(() => {
    ws.panelOpen = !ws.panelOpen;
    ws.changed();
    if (!ws.panelOpen) ws.focusTick++;
  }, [ws]);

  const addFile = (path: string) => {
    void (async () => {
      try {
        const s = DEFAULT_SIZE.code;
        const rect = placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), s.w, s.h);
        const model = monaco.editor.getModel(monaco.Uri.file(path));
        const d = ws.docs.get(path);
        const r = model && d ? { content: model.getValue(), size: d.size, mtime: d.mtime } : await api.fsRead(ws.mid, path, SNAPSHOT_MAX);
        ctl.addNodes([ctl.make('file', rect, { machineId: ws.mid, path, view: 'code', snapshot: snapshotOf(r.content.slice(0, SNAPSHOT_MAX), r.size, r.mtime, r.content.length > SNAPSHOT_MAX) })]);
        toast.success(`${nameOf(path)} added to the board.`);
      } catch (err) {
        toast.error(errorMessage(err));
      }
    })();
  };

  const addFolder = (dir: string) => {
    const s = DEFAULT_SIZE.folder;
    const rect = placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), s.w, s.h);
    ctl.addNodes([ctl.make('file', rect, { machineId: ws.mid, path: dir, view: 'folder' })]);
    toast.success('Folder card added to the board.');
  };

  /** Ctrl+L and the editor's menu: the selection (or the file) goes along with a question. */
  const askAgent = useCallback(
    (path?: string, prompt?: string) => {
      const ed = ws.activeEditor();
      const doc = ws.activeDoc;
      const target = path ?? doc?.path;
      if (target) {
        const model = !path ? ed?.getModel() : null;
        const sel = model && model === doc?.model ? ed?.getSelection() : null;
        const rel = ws.rel(target);
        const language = model?.getLanguageId() ?? '';
        if (sel && !sel.isEmpty() && model) {
          const end = sel.endColumn === 1 && sel.endLineNumber > sel.startLineNumber ? sel.endLineNumber - 1 : sel.endLineNumber;
          const range = sel.startLineNumber === end ? `line ${end}` : `lines ${sel.startLineNumber}-${end}`;
          setContext({ label: `${nameOf(target)}, ${range}`, rel, language, code: model.getValueInRange(sel), startLine: sel.startLineNumber, endLine: end });
        } else {
          setContext({ label: nameOf(target), rel, language, code: '', startLine: 0, endLine: 0 });
        }
      }
      ws.side = 'agents';
      ws.sideOpen = true;
      setAgentsTick((n) => n + 1);
      if (prompt) setAgentRequest({ prompt, tick: Date.now() });
      ws.changed();
    },
    [ws],
  );

  const pick = useCallback(
    (kind: PickKind) => {
      if (kind === 'theme') {
        void (async () => {
          const list = await loadExtensions();
          const start = currentTheme();
          setPalette({
            initial: '',
            pick: {
              placeholder: `Select a colour theme (now: ${start === BUILTIN_THEME ? 'NextBoard' : start})`,
              items: [
                { label: 'NextBoard', detail: 'built in', run: () => useBuiltinTheme() },
                { label: 'Dark', detail: 'built in', run: () => useBuiltinTheme('vs-dark') },
                { label: 'Light', detail: 'built in', run: () => useBuiltinTheme('vs') },
                { label: 'High Contrast', detail: 'built in', run: () => useBuiltinTheme('hc-black') },
                ...list.flatMap((x) => (x.contributes.themes ?? []).map((t) => ({ label: t.label, detail: x.displayName || x.name, run: () => void applyTheme(x, t.path).catch((err) => toast.error(errorMessage(err))) }))),
              ],
            },
          });
        })();
        return;
      }
      const doc = ws.activeDoc;
      const model = doc?.model;
      if (!model || !doc) return;
      if (kind === 'encoding') {
        setPalette({
          initial: '',
          pick: {
            placeholder: 'Reopen with encoding (the file stays read-only)',
            items: ENCODINGS.map(([id, detail]) => ({ label: id, detail, run: () => void ws.reopenWithEncoding(doc.path, id) })),
          },
        });
      } else if (kind === 'eol') {
        setPalette({
          initial: '',
          pick: {
            placeholder: 'Select the end of line sequence',
            items: [
              { label: 'LF', detail: 'Linux, macOS', run: () => model.pushEOL(monaco.editor.EndOfLineSequence.LF) },
              { label: 'CRLF', detail: 'Windows', run: () => model.pushEOL(monaco.editor.EndOfLineSequence.CRLF) },
            ],
          },
        });
      } else if (kind === 'indent') {
        const sizes = [2, 3, 4, 6, 8];
        setPalette({
          initial: '',
          pick: {
            placeholder: 'Select the indentation of this file',
            items: [
              ...sizes.map((n) => ({ label: `Spaces: ${n}`, run: () => model.updateOptions({ insertSpaces: true, tabSize: n, indentSize: n }) })),
              ...sizes.map((n) => ({ label: `Tabs: ${n}`, run: () => model.updateOptions({ insertSpaces: false, tabSize: n, indentSize: n }) })),
              { label: 'Convert indentation to spaces', run: () => void ws.activeEditor()?.getAction('editor.action.indentationToSpaces')?.run() },
              { label: 'Convert indentation to tabs', run: () => void ws.activeEditor()?.getAction('editor.action.indentationToTabs')?.run() },
            ],
          },
        });
      } else {
        setPalette({
          initial: '',
          pick: {
            placeholder: 'Select the language of this file',
            items: monaco.languages.getLanguages().map((l) => ({
              label: l.aliases?.[0] ?? l.id,
              detail: l.id,
              run: () => monaco.editor.setModelLanguage(model, l.id),
            })),
          },
        });
      }
    },
    [ws],
  );

  /** F5: the chosen configuration, else the first one that fits. */
  const startDebug = useCallback(
    async (noDebug = false) => {
      const list = await ws.debug.configurations();
      const c = list.find((x) => x.name === ws.debug.configName) ?? list[0];
      ws.side = 'debug';
      ws.sideOpen = true;
      if (c) await ws.debug.start(c, noDebug);
      else ws.debug.notice = { text: 'Nothing to debug here. Open a Go, Python or JavaScript file, or add a configuration to launch.json.' };
      ws.changed();
    },
    [ws],
  );

  const toggleBreakpointHere = useCallback(() => {
    const ed = ws.activeEditor();
    const d = ws.activeDoc;
    const line = ed?.getPosition()?.lineNumber;
    if (d && line) ws.debug.toggleBreakpoint(d.path, line);
  }, [ws]);

  // what the editor's own menu calls
  ws.actions = {
    ask: (prompt) => askAgent(undefined, prompt),
    aiEdit: () => setAiOpen(true),
    palette: () => setPalette({ initial: '>' }),
    mention: () => {
      const ed = ws.activeEditor() ?? [...ws.editors.values()][0];
      const linked = ws.ide.consoles;
      // agents here that are not linked get the same reference as plain text in their prompt
      const others = ws.paneConsoles.filter((id) => !linked.some((c) => c.id === id)).flatMap((id) => {
        const s = store.getState().sessions[id];
        return s ? [{ id, name: s.handle ? `@${s.handle}` : s.name }] : [];
      });
      const list = [...linked, ...others];
      const send = (id: string) => {
        if (!ed) return;
        if (linked.some((c) => c.id === id)) ws.ide.mention(ed, id);
        else ws.ide.typeReference(ed, id);
        ws.revealPane(id);
      };
      if (!ed || !list.length) {
        toast.info('Open an agent first: the selection has nowhere to go.');
      } else if (list.length === 1) {
        send(list[0].id);
      } else {
        setPalette({ initial: '', pick: { placeholder: 'Send to which console?', items: list.map((c) => ({ label: c.name, run: () => send(c.id) })) } });
      }
    },
    pick: (placeholder, items) => setPalette({ initial: '', pick: { placeholder, items } }),
    addToBoard: (path) => addFile(path),
    openWeb: (url) => {
      const rect = placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), DEFAULT_WEB_SIZE.w, DEFAULT_WEB_SIZE.h);
      ctl.addNodes([webNode(ctl, rect, url)]);
      toast.success(`${url} added to the board.`);
    },
  };

  const choices = usePaneChoices(ws, ctl.workspaceId);
  const paneEnv = useMemo(() => ({ project, ctl }), [project, ctl]);

  // a console's needs-you, the live chip and N all come here for an agent inside the editor
  useEffect(() => {
    const show = (id: string) => ws.revealPane(id) || (ws.placePane(id, 'side'), true);
    const pending = takePendingPane();
    if (pending) show(pending);
    return registerEditorFocus(ctl.workspaceId, show);
  }, [ws, ctl.workspaceId]);

  const installInTerminal = useCallback((command: string) => void newTerminal(ws, ctl.workspaceId, undefined, command), [ws, ctl.workspaceId]);

  const commands = useCallback((): Command[] => {
    const doc = ws.activeDoc;
    const ed = ws.activeEditor();
    const prefToggle = (label: string, key: 'minimap' | 'wordWrap' | 'stickyScroll' | 'renderWhitespace' | 'formatOnSave') => ({ id: `pref.${key}`, label, run: () => ws.setPrefs({ [key]: !ws.editorPrefs[key] }) });
    const list: Command[] = [
      { id: 'file.save', label: 'File: Save', keys: 'Ctrl+S', icon: 'save', run: () => doc && void ws.save(doc.path) },
      { id: 'file.saveAll', label: 'File: Save All', keys: 'Ctrl+Alt+S', icon: 'save', run: () => void ws.saveAll() },
      { id: 'file.close', label: 'View: Close Editor', keys: 'Alt+W', icon: 'close', run: () => ws.group.active && ws.closeTab(ws.activeGroup, ws.group.active) },
      { id: 'file.new', label: 'File: New File', icon: 'file', run: () => ws.startInput('file', doc ? dirOf(doc.path) : ws.root) },
      { id: 'file.newFolder', label: 'File: New Folder', icon: 'folder', run: () => ws.startInput('folder', doc ? dirOf(doc.path) : ws.root) },
      { id: 'file.reveal', label: 'File: Reveal Active File in Explorer', icon: 'folderOpen', run: () => doc && ws.revealInTree(doc.path) },
      { id: 'file.copyPath', label: 'File: Copy Path of Active File', icon: 'link', run: () => doc && void navigator.clipboard?.writeText(doc.path) },
      { id: 'file.copyRel', label: 'File: Copy Relative Path of Active File', icon: 'link', run: () => doc && void navigator.clipboard?.writeText(ws.rel(doc.path)) },
      { id: 'file.board', label: 'File: Add Active File to Board', icon: 'plus', run: () => doc && addFile(doc.path) },
      { id: 'file.revert', label: 'File: Revert File', icon: 'undo', run: () => doc && void ws.reload(doc.path) },
      { id: 'git.compare', label: 'Git: Compare Active File with HEAD', icon: 'branch', run: () => doc && ws.openDiff(doc.path, 'head') },
      {
        id: 'tasks.run',
        label: 'Tasks: Run Task…',
        icon: 'play',
        run: () =>
          void taskApi
            .list(ws.gitTarget)
            .then((list) => ws.actions.pick?.('Run a task', list.map((t) => ({ label: `${t.group}: ${t.name}`, detail: t.command, run: () => void runTask(ws, t) }))))
            .catch((err) => toast.error(errorMessage(err))),
      },
      {
        id: 'tasks.test',
        label: 'Tasks: Run Tests',
        icon: 'checklist',
        run: () =>
          void taskApi
            .list(ws.gitTarget)
            .then((list) => {
              const t = list.find((x) => x.kind === 'test');
              if (t) void runTask(ws, t);
              else toast.info('This project names no tests.');
            })
            .catch((err) => toast.error(errorMessage(err))),
      },
      { id: 'bookmarks.toggle', label: 'Bookmarks: Toggle', keys: 'Ctrl+Alt+K', icon: 'bookmark', run: () => doc && toggleBookmark(ws, doc.path, ed?.getPosition()?.lineNumber ?? 1) },
      { id: 'bookmarks.next', label: 'Bookmarks: Next', keys: 'Ctrl+Alt+L', icon: 'arrowDown', run: () => jumpBookmark(ws, doc?.path, ed?.getPosition()?.lineNumber ?? 1, 1) },
      { id: 'bookmarks.prev', label: 'Bookmarks: Previous', keys: 'Ctrl+Alt+J', icon: 'arrowUp', run: () => jumpBookmark(ws, doc?.path, ed?.getPosition()?.lineNumber ?? 1, -1) },
      { id: 'bookmarks.list', label: 'Bookmarks: List', icon: 'bookmark', run: () => listBookmarks(ws) },
      { id: 'git.graph', label: 'Git: Open Commit Graph', icon: 'branch', run: () => ws.openView({ kind: 'graph' }, { pin: true }) },
      { id: 'file.localHistory', label: 'File: Local History', icon: 'history', run: () => doc && ws.openView({ kind: 'history', path: doc.path }) },
      ...(hasNative('gitlens')
        ? ([
            { id: 'gitlens.file', label: 'GitLens: Show File History', icon: 'history', run: () => doc && void showHistory(ws, doc.path) },
            { id: 'gitlens.line', label: 'GitLens: Show Line History', icon: 'history', run: () => doc && void showHistory(ws, doc.path, ed?.getPosition()?.lineNumber) },
            { id: 'gitlens.blame', label: 'GitLens: Toggle File Blame', icon: 'history', run: () => ed && void toggleFileBlame(ws, ed) },
          ] as Command[])
        : []),
      ...(hasNative('eslint')
        ? ([
            { id: 'eslint.fix', label: 'ESLint: Fix All Auto-Fixable Problems', icon: 'check', run: () => ed && void eslintFixAll(ws, ed) },
            { id: 'eslint.allow', label: 'ESLint: Allow This Project’s ESLint to Run', icon: 'check', run: () => allowESLint(ws, true) },
            { id: 'eslint.deny', label: 'ESLint: Stop Running in This Project', icon: 'close', run: () => allowESLint(ws, false) },
          ] as Command[])
        : []),
      { id: 'view.split', label: 'View: Split Editor', keys: 'Ctrl+\\', icon: 'layout', run: () => ws.splitRight() },
      { id: 'view.sidebar', label: 'View: Toggle Side Bar', keys: 'Ctrl+B', run: () => ((ws.sideOpen = !ws.sideOpen), ws.changed()) },
      { id: 'view.panel', label: 'View: Toggle Panel', keys: 'Ctrl+J', run: togglePanel },
      { id: 'view.explorer', label: 'View: Show Explorer', keys: 'Ctrl+Shift+E', icon: 'file', run: () => showView('files') },
      { id: 'view.search', label: 'View: Show Search', keys: 'Ctrl+Shift+F', icon: 'search', run: () => showView('search') },
      { id: 'view.scm', label: 'View: Show Source Control', keys: 'Ctrl+Shift+G', icon: 'branch', run: () => showView('scm') },
      { id: 'view.agents', label: 'View: Show Ask', keys: 'Ctrl+L', icon: 'sparkle', run: () => askAgent() },
      { id: 'claude.mention', label: 'Agents: Send Selection to Console', keys: 'Alt+K', icon: 'message', run: () => ws.actions.mention?.() },
      { id: 'agents.toggle', label: 'View: Toggle Agents Side Bar', keys: 'Ctrl+Alt+B', icon: 'bot', run: () => ((ws.rightOpen = !ws.rightOpen), ws.changed()) },
      ...choices.agents.flatMap<Command>((a) => [
        { id: `agents.side.${a.id}`, label: `Agents: Open ${a.name} in the Side Bar`, icon: 'bot', run: () => void startAgentPane(ws, ctl.workspaceId, a, 'side') },
        { id: `agents.tab.${a.id}`, label: `Agents: Open ${a.name} in a New Tab`, icon: 'bot', run: () => void startAgentPane(ws, ctl.workspaceId, a, 'tab') },
      ]),
      { id: 'agents.orch.side', label: 'Agents: Open Orchestrator in the Side Bar', icon: 'orchestrator', run: () => ws.placePane(ORCHESTRATOR_PANE, 'side') },
      { id: 'agents.orch.tab', label: 'Agents: Open Orchestrator in a New Tab', icon: 'orchestrator', run: () => ws.placePane(ORCHESTRATOR_PANE, 'tab') },
      { id: 'ai.explain', label: 'AI: Explain This', icon: 'sparkle', run: () => askAgent(undefined, 'Explain this code.') },
      { id: 'ai.bugs', label: 'AI: Find Bugs Here', icon: 'sparkle', run: () => askAgent(undefined, 'Look for bugs in this code. Say what is wrong and why, most serious first.') },
      { id: 'view.debug', label: 'View: Show Run and Debug', keys: 'Ctrl+Shift+D', icon: 'bug', run: () => showView('debug') },
      ...(ws.laravel?.detected ? [{ id: 'view.laravel', label: 'View: Show Laravel', icon: 'layers' as const, run: () => showView('laravel') }] : []),
      { id: 'view.extensions', label: 'View: Show Extensions', keys: 'Ctrl+Shift+X', icon: 'puzzle', run: () => showView('extensions') },
      { id: 'debug.start', label: 'Debug: Start Debugging', keys: 'F5', icon: 'play', run: () => void startDebug() },
      { id: 'debug.run', label: 'Debug: Run Without Debugging', keys: 'Ctrl+F5', icon: 'play', run: () => void startDebug(true) },
      { id: 'debug.stop', label: 'Debug: Stop', keys: 'Shift+F5', icon: 'stop', run: () => void ws.debug.stop() },
      { id: 'debug.toggle', label: 'Debug: Toggle Breakpoint', keys: 'F9', icon: 'bug', run: () => toggleBreakpointHere() },
      { id: 'debug.launch', label: 'Debug: Open launch.json', icon: 'sliders', run: () => void ws.debug.openLaunchJson() },
      { id: 'pref.theme', label: 'Preferences: Color Theme', icon: 'palette', run: () => pick('theme') },
      { id: 'ai.inline', label: 'AI: Trigger Inline Suggestion', keys: 'Alt+\\', icon: 'sparkle', run: () => void ed?.getAction('editor.action.inlineSuggest.trigger')?.run() },
      { id: 'view.settings', label: 'Preferences: Open Settings', icon: 'sliders', run: () => showView('settings') },
      { id: 'view.problems', label: 'View: Show Problems', icon: 'alertCircle', run: () => ((ws.panelOpen = true), (ws.panelView = 'problems'), ws.changed()) },
      { id: 'view.output', label: 'View: Show Output', run: () => ((ws.panelOpen = true), (ws.panelView = 'output'), ws.changed()) },
      { id: 'term.new', label: 'Terminal: Create New Terminal', icon: 'terminal', run: () => void newTerminal(ws, ctl.workspaceId) },
      { id: 'term.toggle', label: 'Terminal: Toggle Terminal', keys: 'Ctrl+`', icon: 'terminal', run: togglePanel },
      { id: 'ai.edit', label: 'AI: Edit with AI', keys: 'Ctrl+I', icon: 'sparkle', run: () => setAiOpen(true) },
      { id: 'ai.ask', label: 'AI: Ask an Agent About the Selection', keys: 'Ctrl+L', icon: 'sparkle', run: () => askAgent() },
      { id: 'lsp.restart', label: 'Language Servers: Restart', icon: 'restart', run: () => ws.lsp.restart() },
      { id: 'lang.pick', label: 'Change Language Mode', run: () => pick('language') },
      { id: 'lang.indent', label: 'Change Indentation', run: () => pick('indent') },
      { id: 'lang.eol', label: 'Change End of Line Sequence', run: () => pick('eol') },
      prefToggle('View: Toggle Minimap', 'minimap'),
      prefToggle('View: Toggle Word Wrap', 'wordWrap'),
      prefToggle('View: Toggle Sticky Scroll', 'stickyScroll'),
      prefToggle('View: Toggle Render Whitespace', 'renderWhitespace'),
      prefToggle('Preferences: Toggle Format on Save', 'formatOnSave'),
    ];
    if (fullscreen.supported) list.push({ id: 'view.fullscreen', label: fullscreen.active ? 'View: Exit Full Screen' : 'View: Enter Full Screen', icon: fullscreen.active ? 'fullscreenExit' : 'fullscreen', run: fullscreen.toggle });
    const seen = new Set(list.map((c) => c.label));
    for (const a of ed?.getSupportedActions() ?? []) {
      if (!a.label || seen.has(a.label) || !a.isSupported()) continue;
      seen.add(a.label);
      list.push({ id: a.id, label: a.label, run: () => void ed?.getAction(a.id)?.run() });
    }
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, ctl.workspaceId, togglePanel, showView, askAgent, pick, fullscreen.active, fullscreen.supported, choices]);

  // ---- global shortcuts ----
  const latest = useRef({ showView, togglePanel, askAgent, palette, commands, startDebug, toggleBreakpointHere });
  latest.current = { showView, togglePanel, askAgent, palette, commands, startDebug, toggleBreakpointHere };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // a dialog or the palette above the workbench keeps its keys
      if (document.querySelector('.modal, .wb-palette')) return;
      // a card among others: its shortcuts count only while the user works inside it.
      // A clicked button may be gone by now, which leaves the focus on the page itself.
      const root = rootRef.current;
      const onPage = document.activeElement === document.body && clickedInside.current;
      if (!root?.contains(document.activeElement) && !onPage) return;
      if (onPage) {
        // and the board must not read these keys as its tools
        e.stopPropagation();
        if (!e.ctrlKey && !e.metaKey && !e.altKey && e.key.length === 1) root?.focus();
      }
      const t = e.target as HTMLElement | null;
      const inTerminal = !!t?.closest?.('.xterm');
      const mod = e.ctrlKey || e.metaKey;
      const k = e.key.toLowerCase();
      const stop = () => {
        e.preventDefault();
        e.stopPropagation();
      };
      const dbg = ws.debug;
      if (mod && e.altKey && k === 'b') {
        stop();
        ws.rightOpen = !ws.rightOpen;
        ws.changed();
      } else if (e.ctrlKey && e.code === 'Backquote') {
        stop();
        latest.current.togglePanel();
      } else if (e.key === 'F5') {
        // also keeps the browser from reloading the page under the editor
        stop();
        if (mod && e.shiftKey) void dbg.restart();
        else if (e.shiftKey) void dbg.stop();
        else if (dbg.state === 'stopped') dbg.continue();
        else if (!dbg.active) void latest.current.startDebug(mod);
      } else if (e.key === 'F6' && dbg.state === 'running') {
        stop();
        dbg.pause();
      } else if (e.key === 'F9' && !inTerminal) {
        stop();
        latest.current.toggleBreakpointHere();
      } else if (e.key === 'F10' && dbg.state === 'stopped') {
        stop();
        dbg.stepOver();
      } else if (e.key === 'F11' && dbg.state === 'stopped') {
        stop();
        if (e.shiftKey) dbg.stepOut();
        else dbg.stepIn();
      } else if (mod && e.shiftKey && k === 'd') {
        stop();
        ws.side = 'debug';
        ws.sideOpen = true;
        ws.changed();
      } else if (mod && e.shiftKey && k === 'x') {
        stop();
        ws.side = 'extensions';
        ws.sideOpen = true;
        ws.changed();
      } else if (e.key === 'F1') {
        stop();
        setPalette({ initial: '>' });
      } else if (mod && e.shiftKey && k === 'p') {
        stop();
        setPalette({ initial: '>' });
      } else if (mod && e.shiftKey && k === 'f') {
        stop();
        latest.current.showView('search');
      } else if (mod && e.shiftKey && k === 'e') {
        stop();
        ws.side = 'files';
        ws.sideOpen = true;
        ws.changed();
      } else if (mod && e.shiftKey && k === 'g') {
        stop();
        ws.side = 'scm';
        ws.sideOpen = true;
        ws.changed();
      } else if (inTerminal) {
        // everything else belongs to the program in the terminal
      } else if (mod && !e.shiftKey && k === 'p') {
        stop();
        setPalette({ initial: '' });
      } else if (mod && !e.shiftKey && !e.altKey && k === 's') {
        stop();
        const proposal = ws.activeTab?.diff?.startsWith('ide:') ? ws.activeTab.diff.slice('ide:'.length) : null;
        const d = ws.activeDoc;
        // saving a proposed change means taking it, as in VS Code
        if (proposal) ws.ide.accept(proposal);
        else if (d) void ws.save(d.path);
      } else if (mod && e.altKey && k === 's') {
        stop();
        void ws.saveAll();
      } else if (mod && !e.shiftKey && k === 'b') {
        stop();
        ws.sideOpen = !ws.sideOpen;
        ws.changed();
      } else if (mod && !e.shiftKey && k === 'j') {
        stop();
        latest.current.togglePanel();
      } else if (mod && e.key === '\\') {
        stop();
        ws.splitRight();
      } else if (mod && !e.shiftKey && k === 'i') {
        stop();
        setAiOpen(true);
      } else if (mod && !e.shiftKey && k === 'l') {
        stop();
        latest.current.askAgent();
      } else if (e.altKey && !mod && k === 'w') {
        // browsers keep Ctrl+W for themselves
        stop();
        if (ws.group.active) ws.closeTab(ws.activeGroup, ws.group.active);
      } else if (e.altKey && !mod && e.key === 'PageDown') {
        stop();
        ws.cycleTab(1);
      } else if (e.altKey && !mod && e.key === 'PageUp') {
        stop();
        ws.cycleTab(-1);
      }
    };
    document.addEventListener('keydown', onKey, true);
    const onPointer = (e: PointerEvent) => {
      clickedInside.current = !!rootRef.current?.contains(e.target as Node);
    };
    document.addEventListener('pointerdown', onPointer, true);
    const onBlur = () => ws.focusChanged();
    window.addEventListener('blur', onBlur);
    return () => {
      document.removeEventListener('keydown', onKey, true);
      document.removeEventListener('pointerdown', onPointer, true);
      window.removeEventListener('blur', onBlur);
    };
  }, [ws]);

  // A closed tab must not lose edits to a slow draft timer.
  useEffect(() => {
    const flush = () => ws.flushDrafts();
    window.addEventListener('beforeunload', flush);
    return () => window.removeEventListener('beforeunload', flush);
  }, [ws]);

  const startSide = (e: ReactPointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const start = e.clientX;
    const w = ws.sideW;
    const root = rootRef.current;
    const scale = scaleOf(root);
    const max = (root?.offsetWidth ?? window.innerWidth) - 320;
    const move = (ev: PointerEvent) => {
      ws.sideW = Math.min(Math.max(180, w + (ev.clientX - start) / scale), max);
      ws.changed();
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const startRight = (e: ReactPointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const start = e.clientX;
    const w = ws.rightW;
    const root = rootRef.current;
    const scale = scaleOf(root);
    const max = (root?.offsetWidth ?? window.innerWidth) - 320;
    const move = (ev: PointerEvent) => {
      ws.rightW = Math.min(Math.max(260, w - (ev.clientX - start) / scale), max);
      ws.changed();
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const startPanel = (e: ReactPointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const start = e.clientY;
    const h = ws.panelH;
    const root = rootRef.current;
    const scale = scaleOf(root);
    const max = (root?.offsetHeight ?? window.innerHeight) - 200;
    const move = (ev: PointerEvent) => {
      ws.panelH = Math.min(Math.max(100, h + (start - ev.clientY) / scale), max);
      ws.changed();
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  /**
   * Grow the card to the visible board at 100 % zoom, and back: it stays a card. Its
   * neighbours move out of the way and come back with it. The way back is kept in the node,
   * so it survives a reload.
   */
  const toggleFill = () => {
    const n = ctl.node(nodeId);
    if (!n) return;
    const saved = n.data.fill as (NodeLayout & { moved?: Moved[] }) | undefined;
    if (saved) {
      const { moved = [], ...rect } = saved;
      // by the same distance, not to the old spot: one moved by hand meanwhile stays moved
      const back = moved.flatMap((m): Op[] => {
        const o = ctl.node(m.id);
        return o ? [{ op: 'patch', id: m.id, set: { x: o.x - m.dx, y: o.y - m.dy } }] : [];
      });
      ctl.apply([{ op: 'patch', id: nodeId, set: { ...rect, data: { fill: null } } }, ...back], { history: false });
      if (viewBeforeFill.current) void rf.setViewport(viewBeforeFill.current, { duration: 200 });
      else void rf.setCenter(rect.x + rect.w / 2, rect.y + rect.h / 2, { zoom: rf.getZoom(), duration: 200 });
      viewBeforeFill.current = null;
      return;
    }
    const box = document.querySelector('.board-canvas')?.getBoundingClientRect();
    if (!box) return;
    viewBeforeFill.current = rf.getViewport();
    // clear of the header, the rails and the command bar
    const margin = { left: 72, right: 72, top: 72, bottom: 108 };
    const from = rectOf(n);
    const to = { x: n.x, y: n.y, w: Math.max(560, box.width - margin.left - margin.right), h: Math.max(340, box.height - margin.top - margin.bottom) };
    const moved = pushAside(ctl.nodes, nodeId, from, to);
    const aside = moved.flatMap((m): Op[] => {
      const o = ctl.node(m.id);
      return o ? [{ op: 'patch', id: m.id, set: { x: o.x + m.dx, y: o.y + m.dy } }] : [];
    });
    ctl.stack.raise(nodeId);
    ctl.select([nodeId]);
    ctl.apply([{ op: 'patch', id: nodeId, set: { w: to.w, h: to.h, data: { fill: { ...from, moved } } } }, ...aside], { history: false });
    void rf.setViewport({ x: margin.left - n.x, y: margin.top - n.y, zoom: 1 }, { duration: 200 });
  };

  const closeCard = () => {
    ws.flushDrafts();
    ctl.deleteNodes([nodeId]);
  };

  const closing = ws.closing;
  const dirtyCount = [...ws.docs.values()].filter((d) => d.dirty).length;

  return (
    <PaneEnv.Provider value={paneEnv}>
    <div
      className={`wb nokey ${mode === 'wide' ? '' : `is-${mode}`}`}
      ref={rootRef}
      tabIndex={-1}
      role="application"
      aria-label={`Code: ${project.name}`}
      // the board's own shortcuts listen further up: keys typed in the card stop here
      onKeyDown={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      // the title bar still gives the card's board menu; inside, menus are the editor's own
      onContextMenu={(e) => {
        if (!(e.target as HTMLElement).closest('.wb-title')) e.stopPropagation();
      }}
    >
      <TileHead
        id={nodeId}
        className="wb-title"
        icon="code"
        title={project.name}
        meta={
          <button type="button" className="wb-command-center nodrag" onClick={() => setPalette({ initial: '' })} title="Go to file · Ctrl+P">
            <Icon name="search" />
            <span className="truncate">Go to file, or &gt; for commands</span>
            <kbd>Ctrl P</kbd>
          </button>
        }
        actions={
          <span className="wb-title-tools nodrag">
            {dirtyCount > 0 && (
              <Button size="sm" variant="ghost" icon="save" onClick={() => void ws.saveAll()} title="Save all · Ctrl+Alt+S">
                Save {dirtyCount}
              </Button>
            )}
            <Button size="sm" variant={ws.sideOpen ? 'secondary' : 'ghost'} iconOnly icon="layout" aria-label="Toggle side bar (Ctrl+B)" aria-pressed={ws.sideOpen} title="Side bar · Ctrl+B" onClick={() => ((ws.sideOpen = !ws.sideOpen), ws.changed())} />
            <Button size="sm" variant={ws.rightOpen ? 'secondary' : 'ghost'} iconOnly icon="bot" aria-label="Toggle agents side bar (Ctrl+Alt+B)" aria-pressed={ws.rightOpen} title="Agents side bar · Ctrl+Alt+B" onClick={() => ((ws.rightOpen = !ws.rightOpen), ws.changed())} />
            <Button size="sm" variant={ws.panelOpen ? 'secondary' : 'ghost'} iconOnly icon="terminal" aria-label="Toggle panel (Ctrl+`)" aria-pressed={ws.panelOpen} title="Terminal panel · Ctrl+`" onClick={togglePanel} />
            <Button size="sm" variant={filled ? 'secondary' : 'ghost'} iconOnly className="wb-fill" icon={filled ? 'fullscreenExit' : 'fullscreen'} aria-label={filled ? 'Back to the card’s own size' : 'Fill the board view'} aria-pressed={filled} title={filled ? 'Back to the card’s own size' : 'Fill the board view'} onClick={toggleFill} />
          </span>
        }
        menuLabel="Editor card actions"
        deleteItem={{ label: 'Close the card', icon: 'close', title: 'Tabs and unsaved edits are kept', onSelect: closeCard }}
      />
      <div className="wb-body nodrag nowheel nopan" ref={bodyRef}>
        <nav className="wb-activity" aria-label="Views">
          {ACTIVITY.filter((a) => a.view !== 'laravel' || ws.laravel?.detected).map((a) => (
            <button
              key={a.view}
              type="button"
              className={`wb-activity-btn ${ws.sideOpen && ws.side === a.view ? 'is-active' : ''}`}
              aria-label={a.label}
              aria-pressed={ws.sideOpen && ws.side === a.view}
              title={a.keys ? `${a.label} · ${a.keys}` : a.label}
              onClick={() => showView(a.view)}
            >
              <Icon name={a.icon} />
              {a.view === 'scm' && !!ws.git?.files?.length && <span className="wb-activity-badge tabular">{ws.git.files.length}</span>}
              {a.view === 'review' && ws.reviewCount > 0 && <span className="wb-activity-badge tabular">{ws.reviewCount}</span>}
            </button>
          ))}
          <span className="spacer" />
          <button type="button" className={`wb-activity-btn ${ws.sideOpen && ws.side === 'settings' ? 'is-active' : ''}`} aria-label="Settings" aria-pressed={ws.sideOpen && ws.side === 'settings'} title="Settings" onClick={() => showView('settings')}>
            <Icon name="sliders" />
          </button>
        </nav>
        {ws.sideOpen && (
          <>
            {sideOver && <button type="button" className="wb-scrim wb-scrim-side" aria-label="Close the side bar" onClick={() => ((ws.sideOpen = false), ws.changed())} />}
            <aside className="wb-side" style={{ width: ws.sideW }}>
              {ws.side === 'files' && <Explorer ws={ws} onAddFile={addFile} onAddFolder={addFolder} onTerminal={(dir) => void newTerminal(ws, ctl.workspaceId, dir)} onAsk={askAgent} />}
              {ws.side === 'search' && <SearchPanel ws={ws} focusTick={searchTick} />}
              {ws.side === 'scm' && <ScmPanel ws={ws} />}
              {ws.side === 'review' && <ReviewPanel ws={ws} />}
              {ws.side === 'debug' && <DebugView ws={ws} onInstall={installInTerminal} />}
              {ws.side === 'laravel' && <LaravelPanel ws={ws} onTerminal={installInTerminal} />}
              {ws.side === 'extensions' && <ExtensionsPanel ws={ws} onInstall={installInTerminal} />}
              {ws.side === 'agents' && <AgentsPanel ws={ws} ctl={ctl} nodeId={nodeId} context={context} setContext={setContext} request={agentRequest} focusTick={agentsTick} />}
              {ws.side === 'settings' && <SettingsPanel ws={ws} onInstall={installInTerminal} />}
            </aside>
            {!sideOver && <div className="wb-sash wb-sash-v" role="separator" aria-orientation="vertical" aria-label="Resize side bar" onPointerDown={startSide} />}
          </>
        )}
        <main className="wb-center">
          <DebugToolbar ws={ws} />
          <EditorArea ws={ws} onAddFile={addFile} aiOpen={aiOpen} setAiOpen={setAiOpen} />
          {ws.panelOpen && (
            <>
              <div className="wb-sash wb-sash-h" role="separator" aria-orientation="horizontal" aria-label="Resize panel" onPointerDown={startPanel} />
              <div className="wb-panel-wrap" style={{ height: ws.panelH, flex: '0 0 auto' }}>
                <BottomPanel ws={ws} workspaceId={ctl.workspaceId} />
              </div>
            </>
          )}
        </main>
        {ws.rightOpen && (
          <>
            {rightOver ? (
              <button type="button" className="wb-scrim" aria-label="Close the agents side bar" onClick={() => ((ws.rightOpen = false), ws.changed())} />
            ) : (
              <div className="wb-sash wb-sash-v" role="separator" aria-orientation="vertical" aria-label="Resize agents side bar" onPointerDown={startRight} />
            )}
            <AgentSideBar ws={ws} workspaceId={ctl.workspaceId} />
          </>
        )}
      </div>
      <div className="nodrag">
        <StatusBar ws={ws} onPick={pick} onView={showView} onPanel={togglePanel} />
      </div>

      {palette && <QuickOpen key={palette.initial + (palette.pick?.placeholder ?? '')} ws={ws} initial={palette.initial} pick={palette.pick} commands={commands} onClose={() => setPalette(null)} />}
      {closing && (
        <Modal
          title="Unsaved changes"
          size="sm"
          onClose={() => ws.resolveClosing('cancel')}
          footer={
            <>
              <Button onClick={() => ws.resolveClosing('cancel')}>Keep editing</Button>
              <Button variant="danger-ghost" onClick={() => ws.resolveClosing('discard')}>
                Don’t save
              </Button>
              <Button variant="primary" onClick={() => ws.resolveClosing('save')}>
                Save
              </Button>
            </>
          }
        >
          <p className="text-2">Save the changes to {nameOf(closing.tab.path)} before closing it?</p>
        </Modal>
      )}
      <DebugDialogs ws={ws} />
      <ClosePaneDialog ws={ws} />
      {confirmDialog}
      {/* fixed to the screen: out of the card, which the board moves and scales */}
      {typing === 'editor' && createPortal(<EditorKeyBar ws={ws} onGoToFile={() => setPalette({ initial: '' })} />, document.body)}
      {typing && typing !== 'editor' && createPortal(<KeyBar key={typing} sessionId={typing} name="the terminal" />, document.body)}
    </div>
    </PaneEnv.Provider>
  );
}

export default function CodeStudio({ project, ctl, ui, nodeId, viewCenter, filled }: Props) {
  setupMonaco();
  registerOpener();
  const [ws] = useState(() => Workspace.get(project));
  openerWs = ws;

  useEffect(() => {
    ws.ide.connect(ctl.workspaceId);
    return () => ws.ide.disconnect();
  }, [ws, ctl.workspaceId]);

  useEffect(() => {
    ws.mount();
    openerWs = ws;
    registerInlineAi(ws);
    registerDebugHover(ws.debug);
    void ws.debug.refreshAdapters();
    void loadExtensions();
    if (!ws.groups.some((g) => g.tabs.length) && !ui.get().openFile) void openFirst(ws);
    return () => ws.unmount();
    // once per card
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws]);

  // "open this file" from elsewhere on the board: a file card, the planner
  const openFile = useUi((s) => s.openFile);
  useEffect(() => {
    if (!openFile) return;
    ws.open(openFile.path, { pin: true, source: true, line: openFile.line });
    ui.set({ openFile: null });
  }, [ws, ui, openFile]);

  return <Workbench ws={ws} project={project} ctl={ctl} nodeId={nodeId} viewCenter={viewCenter} filled={filled} />;
}

/** First visit: the README if there is one, as a preview tab. */
async function openFirst(ws: Workspace) {
  try {
    const list = await ws.fileList();
    const rel = (list.items ?? []).find((p) => /^readme(\.|$)/i.test(p));
    if (rel && !ws.groups.some((g) => g.tabs.length)) ws.open(ws.abs(rel));
  } catch {
    /* the tree still works */
  }
}
