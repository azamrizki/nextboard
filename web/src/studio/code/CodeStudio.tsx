// Code workbench (docs/API-studio.md §7): the project's files in an editor of VS Code's
// class, as a card on the board. A card is placed (v0.9.0-beta.2, node data.part): at the Root of
// a project of several parts, one tree with every part as a top-level folder, or in one part, that
// part's folder as the tree. A board may hold one card per placement; two cards showing the same
// file share its model and unsaved edits. Activity bar, side bar (Explorer, Search, Source Control,
// Run and Debug, Extensions, Ask, Settings), editor groups, bottom panel, status bar, Go to
// file and the command palette. Monaco is the editor; language servers and debug adapters
// on the project's machine add the rest; files stay on the machine, written with ifMtime.
//
// Its width decides its layout (DESIGN.md §3.5): under 1000 px the agents side bar lies over the
// editor, under 640 px (a phone) the side bar does too and closes when it opens a file, and only
// the current editor group shows. On a touch screen a key bar comes with the keyboard.
//
// mode "page" is the Code view (/p/<id>/code, DESIGN.md §3.19): the same workbench full screen,
// on the binding of a board that is not drawn (CodeViewPage.tsx). No card chrome: the page's top
// bar (project switcher, Board | Code, quick open, pills, ⋯) instead of the title bar; the tools
// (Chats, Memory, Studios, Vault, Browser, Loops, Mission Map) in the activity bar after Ask, as
// editor tabs; nothing that adds cards to a board (a web page opens as a Browser tab instead). Its
// nodeId is "": no card, so the Root placement, the editor state the project's Root cards show
// too. On a phone, bottom navigation Files · Editor · Terminal · Lead · Agents (CodePhone.tsx).

import { useReactFlow } from '@xyflow/react';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { errorMessage } from '../../api';
import type { BoardController } from '../../board/controller';
import { rectOf } from '../../board/controller';
import { snapshotOf } from '../../board/files';
import { DEFAULT_SIZE, SNAPSHOT_MAX } from '../../board/kinds';
import { placeNear } from '../../board/layout';
import { pushAside, type Moved } from '../../board/pushAside';
import { TileHead, useSize } from '../../board/tiles/common';
import { KeyBar } from '../../board/KeyBar';
import { prefs } from '../../lib/format';
import { useLayout } from '../../lib/layout';
import { EditorKeyBar } from './EditorKeyBar';
import { Button } from '../../components/Button';
import { Icon, type IconName } from '../../components/Icon';
import { ContextMenu, type MenuEntry } from '../../components/Menu';
import { Modal } from '../../components/Modal';
import { EditorProjectSwitch, SwitchProjectMenu } from '../../components/ProjectBar';
import { useUi, type OpenFileRequest, type UiStore } from '../../board/ui';
import { toast } from '../../components/Toast';
import { isRoot, placementOf, projectParts, type Placement } from '../../lib/parts';
import { placeLabel } from '../../lib/placement';
import type { NodeLayout, Op, Project } from '../../lib/types';
import { docStore, readKey } from './docs';
import { fileKey, isRootKey, parentKey, splitKey, viewKey } from './paths';
import { useFullscreen } from '../../lib/useFullscreen';
import { useRemoteConfirm } from '../confirm';
import { BottomPanel, newTerminal, useProblems } from './BottomPanel';
import { DebugDialogs, DebugToolbar, DebugView, registerDebugHover } from './DebugPanel';
import { EditorArea } from './EditorArea';
import { Explorer } from './Explorer';
import { applyTheme, loadExtensions, useBuiltinTheme } from './extensions';
import { store } from '../../store';
import { registerEditorFocus, takePendingPane } from '../../agents/jump';
import { AgentSideBar, ClosePaneDialog, PaneEnv, ResumeChatsDialog, startAgentPane, usePaneChoices } from './AgentPanes';
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
import { nameOf, ORCHESTRATOR_PANE, Workspace, type SideView } from './workspace';
import { browserArg, CODE_VIEW_TOOLS, TOOL_ITEMS, type CodeViewTool } from './toolsList';
import type { ToolKind } from './toolKinds';
import { PhoneNav, PhoneSheetView, type PhoneSheet, type PhoneTab } from './CodePhone';
import { noSideBar, placePaneSide, registerPhoneSheets, showPane } from './pageSide';
import { orchStore } from '../../orch/store';
import type { UiAction } from '../../orch/types';
import { boardPath, navigate } from '../../lib/router';
import { runPageUiAction } from './pageActions';
import '../../styles/studio.css';
import '../../styles/workbench.css';

/** What the Code view's top bar (CodeViewPage.tsx) gets from the workbench under it. */
export interface PageBar {
  ws: Workspace;
  /** Go to file, or with ">" the command palette. */
  quickOpen: (initial?: string) => void;
  /** The phone layout: the bottom navigation does what the bar's toggles do elsewhere. */
  phone: boolean;
}

interface Props {
  project: Project;
  ctl: BoardController;
  ui: UiStore;
  /** The board node this workbench is the content of; "" in the Code view: no card, the Root placement. */
  nodeId: string;
  viewCenter: () => { x: number; y: number };
  /** The card fills the board view: node data "fill" keeps the rect to go back to. */
  filled: boolean;
  /** "card" on a board (the default), "page" the Code view, full screen with no card chrome. */
  mode?: 'card' | 'page';
  /** Page mode: the bar above the workbench. */
  topBar?: (bar: PageBar) => ReactNode;
}

/** Opens a tool as an editor tab, or focuses the one open (the Code view's activity bar). */
const openTool = (ws: Workspace, kind: ToolKind, arg?: string) => ws.openTool(kind, arg);

/** How much a board's zoom scales this element: pointer distances come in screen pixels. */
const scaleOf = (el: Element | null) => {
  const w = (el as HTMLElement | null)?.offsetWidth;
  return el && w ? el.getBoundingClientRect().width / w : 1;
};

/** An editor card's title: the project, and with several parts the placement it shows. */
const cardTitle = (project: Project, ws: Workspace) => (projectParts(project).length > 1 ? `${project.name} · ${isRoot(ws.placement) ? 'All parts' : placeLabel(project, ws.placement)}` : project.name);

/** Where the switch menu of a narrow card opens: under its title bar. */
const switchSpot = (root: HTMLElement | null) => {
  const r = root?.getBoundingClientRect();
  return { x: (r?.left ?? 0) + 8, y: (r?.top ?? 0) + 40 };
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
  // branch, language server and ESLint of the part of the active file (a Root card shows which)
  const part = ws.activePart;
  const lsp = lang ? part.lsp.stateFor(lang) : null;
  const eslint = eslintStatus(ws, doc?.path);
  const opts = model?.getOptions();
  const st = part.git;
  return (
    <footer className="wb-status" aria-label="Status">
      {ws.paths.multi && (
        <button type="button" onClick={() => ws.revealInTree(part.rootKey)} title={`${part.name}: ${part.root}. Show its folder`}>
          <Icon name="layers" />
          {part.name}
        </button>
      )}
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

function Workbench({
  ws,
  project,
  ctl,
  nodeId,
  viewCenter,
  filled,
  page,
  topBar,
}: {
  ws: Workspace;
  project: Project;
  ctl: BoardController;
  nodeId: string;
  viewCenter: () => { x: number; y: number };
  filled: boolean;
  /** The Code view: full screen, no card (CodeViewPage.tsx). */
  page: boolean;
  topBar?: (bar: PageBar) => ReactNode;
}) {
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
  const shown = useLayout();
  // the Code view on a phone: one view at a time, picked along the bottom (CodePhone.tsx); the
  // view and the sheet are this screen's, not the editor state shared with the project's cards
  const phone = page && (shown.layout === 'phone' || mode === 'narrow');
  const [phoneTab, setPhoneTab] = useState<PhoneTab>('editor');
  const [sheet, setSheet] = useState<PhoneSheet | null>(null);
  const phoneRef = useRef(phone);
  phoneRef.current = phone;
  // a file opened from a side bar that lies over the editor closes it: the file is what was wanted
  const activeTab = ws.activeTab?.id ?? null;
  const lastTab = useRef(activeTab);
  useEffect(() => {
    if (activeTab === lastTab.current) return;
    lastTab.current = activeTab;
    // a phone shows what was opened: a file from Files, a tool, a problem's line
    if (phone) {
      if (activeTab) setPhoneTab('editor');
      return;
    }
    if (sideOver && ws.sideOpen && activeTab) {
      ws.sideOpen = false;
      ws.changed();
    }
  }, [activeTab, sideOver, phone, ws]);
  // a terminal started (Explorer's Open in Terminal, a task) shows the Terminal view; the panel
  // closed from its own bar goes back to the editor
  const panelWasOpen = useRef(ws.panelOpen);
  useEffect(() => {
    if (ws.panelOpen === panelWasOpen.current) return;
    panelWasOpen.current = ws.panelOpen;
    if (!phone) return;
    if (ws.panelOpen) setPhoneTab('terminal');
    else setPhoneTab((t) => (t === 'terminal' ? 'editor' : t));
  });
  const pickPhoneTab = useCallback(
    (t: PhoneTab) => {
      setSheet(null);
      setPhoneTab(t);
      if (t === 'terminal' && !ws.panelOpen) {
        ws.panelOpen = true;
        ws.changed();
      }
    },
    [ws],
  );

  // a touch screen: the keys its keyboard lacks, for the editor or a terminal that has the keyboard
  const coarse = shown.input === 'touch';
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
      // a phone's Files view is the side view: picking one shows it, never hides it
      if (phoneRef.current) {
        ws.side = v;
        ws.sideOpen = true;
        setPhoneTab('files');
        setSheet(null);
      } else if (ws.side === v && ws.sideOpen && v !== 'search' && v !== 'agents') {
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
    if (phoneRef.current) {
      // the Terminal view and back
      setPhoneTab((t) => (t === 'terminal' ? 'editor' : 'terminal'));
      if (!ws.panelOpen) {
        ws.panelOpen = true;
        ws.changed();
      }
      return;
    }
    ws.panelOpen = !ws.panelOpen;
    ws.changed();
    if (!ws.panelOpen) ws.focusTick++;
  }, [ws]);

  const addFile = (path: string) => {
    void (async () => {
      try {
        const s = DEFAULT_SIZE.code;
        const rect = placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), s.w, s.h);
        const key = ws.toKey(path);
        if (!key || isRootKey(key)) return;
        const { machineId, abs } = splitKey(key);
        const model = monaco.editor.getModel(docStore.uriOf(key));
        const d = ws.docs.at(key);
        const r = model && d ? { content: model.getValue(), size: d.size, mtime: d.mtime } : await readKey(key, SNAPSHOT_MAX);
        ctl.addNodes([ctl.make('file', rect, { machineId, path: abs, view: 'code', snapshot: snapshotOf(r.content.slice(0, SNAPSHOT_MAX), r.size, r.mtime, r.content.length > SNAPSHOT_MAX) })]);
        toast.success(`${nameOf(path)} added to the board.`);
      } catch (err) {
        toast.error(errorMessage(err));
      }
    })();
  };

  const addFolder = (dir: string) => {
    const s = DEFAULT_SIZE.folder;
    const rect = placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), s.w, s.h);
    const key = ws.toKey(dir);
    if (!key || isRootKey(key)) return;
    const { machineId, abs } = splitKey(key);
    ctl.addNodes([ctl.make('file', rect, { machineId, path: abs, view: 'folder' })]);
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
        const key = ws.toKey(target);
        const rel = key ? ws.relLabel(key) : target;
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
      if (phoneRef.current) setPhoneTab('files');
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
    // the Code view has no board to add a card to
    addToBoard: page ? undefined : (path) => addFile(path),
    // the Ports panel: the page from the machine its ports were listed on (that section's, else the
    // active part's), not the project's first part's (audit #31). The Code view opens it as a
    // Browser tab, the same preview a board's Browser card shows.
    openWeb: (url: string, machineId?: string) => {
      if (page) {
        // the machine goes with the address: a server part's port is that server's localhost
        openTool(ws, 'browser', browserArg(url, machineId));
        return;
      }
      const rect = placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), DEFAULT_WEB_SIZE.w, DEFAULT_WEB_SIZE.h);
      const part = machineId ? (ws.parts.find((pt) => pt.machineId === machineId) ?? null) : ws.activePart;
      ctl.addNodes([webNode(ctl, rect, url, { machineId: machineId ?? part?.machineId, partId: ws.paths.multi || projectParts(project).length > 1 ? part?.partId : undefined })]);
      toast.success(`${url} added to the board.`);
    },
  };

  const choices = usePaneChoices(ws, ctl.workspaceId);
  const paneEnv = useMemo(() => ({ project, ctl, nodeId }), [project, ctl, nodeId]);

  // a console's needs-you, the live chip and N all come here for an agent inside the editor
  useEffect(() => {
    // a phone's Code view has no side bar: there the pane opens as a tab (pageSide.ts)
    const show = (id: string) => showPane(ws, id) || (placePaneSide(ws, id), true);
    // only a pane of this card's placement (a Root card takes any)
    const pending = takePendingPane(ws.placement);
    if (pending) show(pending);
    return registerEditorFocus(ctl.workspaceId, nodeId, show);
  }, [ws, ctl.workspaceId, nodeId]);

  // typed into a new terminal: in a folder when given (the Laravel part, a part's language server), else at the card's default place
  const installInTerminal = useCallback((command: string, dir?: string) => void newTerminal(ws, ctl.workspaceId, dir, command), [ws, ctl.workspaceId]);

  const commands = useCallback((): Command[] => {
    const doc = ws.activeDoc;
    const ed = ws.activeEditor();
    const prefToggle = (label: string, key: 'minimap' | 'wordWrap' | 'stickyScroll' | 'renderWhitespace' | 'formatOnSave') => ({ id: `pref.${key}`, label, run: () => ws.setPrefs({ [key]: !ws.editorPrefs[key] }) });
    const list: Command[] = [
      { id: 'file.save', label: 'File: Save', keys: 'Ctrl+S', icon: 'save', run: () => doc && void ws.save(doc.path) },
      { id: 'file.saveAll', label: 'File: Save All', keys: 'Ctrl+Alt+S', icon: 'save', run: () => void ws.saveAll() },
      { id: 'file.close', label: 'View: Close Editor', keys: 'Alt+W', icon: 'close', run: () => ws.group.active && ws.closeTab(ws.activeGroup, ws.group.active) },
      // the active tab, whatever it shows (a file, a view, an agent)
      { id: 'view.pin', label: 'View: Pin Editor', keys: 'Ctrl+K Shift+Enter', icon: 'pin', run: () => ws.group.active && ws.setPinned(ws.activeGroup, ws.group.active, true) },
      { id: 'view.unpin', label: 'View: Unpin Editor', keys: 'Ctrl+K Shift+Enter', icon: 'pin', run: () => ws.group.active && ws.setPinned(ws.activeGroup, ws.group.active, false) },
      { id: 'view.lock', label: 'View: Lock Editor', icon: 'lock', run: () => ws.group.active && ws.setLocked(ws.activeGroup, ws.group.active, true) },
      { id: 'view.unlock', label: 'View: Unlock Editor', icon: 'unlock', run: () => ws.group.active && ws.setLocked(ws.activeGroup, ws.group.active, false) },
      // in a Root card without an open file: the active part's folder (no file goes at the virtual root)
      { id: 'file.new', label: 'File: New File', icon: 'file', run: () => ws.startInput('file', (doc && parentKey(doc.path)) || ws.activePart.rootKey) },
      { id: 'file.newFolder', label: 'File: New Folder', icon: 'folder', run: () => ws.startInput('folder', (doc && parentKey(doc.path)) || ws.activePart.rootKey) },
      { id: 'file.reveal', label: 'File: Reveal Active File in Explorer', icon: 'folderOpen', run: () => doc && ws.revealInTree(doc.path) },
      { id: 'file.copyPath', label: 'File: Copy Path of Active File', icon: 'link', run: () => doc && void navigator.clipboard?.writeText(splitKey(doc.path).abs) },
      { id: 'file.copyRel', label: 'File: Copy Relative Path of Active File', icon: 'link', run: () => doc && void navigator.clipboard?.writeText(ws.relLabel(doc.path)) },
      ...(page ? [] : [{ id: 'file.board', label: 'File: Add Active File to Board', icon: 'plus', run: () => doc && addFile(doc.path) } as Command]),
      { id: 'file.revert', label: 'File: Revert File', icon: 'undo', run: () => doc && void ws.reload(doc.path) },
      { id: 'git.compare', label: 'Git: Compare Active File with HEAD', icon: 'branch', run: () => doc && ws.openDiff(doc.path, 'head') },
      {
        id: 'tasks.run',
        label: 'Tasks: Run Task…',
        icon: 'play',
        // every part's tasks; a Root card names the part ("admin › npm: build")
        run: () =>
          void partTasks(ws)
            .then((list) => ws.actions.pick?.('Run a task', list.map(({ part, task: t }) => ({ label: `${ws.paths.multi ? `${part.name} › ` : ''}${t.group}: ${t.name}`, detail: t.command, run: () => void runTask(ws, part, t) }))))
            .catch((err) => toast.error(errorMessage(err))),
      },
      {
        id: 'tasks.test',
        label: 'Tasks: Run Tests',
        icon: 'checklist',
        // the active part's tests
        run: () => {
          const part = ws.activePart;
          void taskApi
            .list(part.gitTarget)
            .then((list) => {
              const t = list.find((x) => x.kind === 'test');
              if (t) void runTask(ws, part, t);
              else toast.info(ws.paths.multi ? `${part.name} names no tests.` : 'This project names no tests.');
            })
            .catch((err) => toast.error(errorMessage(err)));
        },
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
      { id: 'view.splitDown', label: 'View: Split Editor Down', keys: 'Ctrl+K Ctrl+\\', icon: 'splitDown', run: () => ws.splitDown() },
      { id: 'view.sidebar', label: 'View: Toggle Side Bar', keys: 'Ctrl+B', run: () => ((ws.sideOpen = !ws.sideOpen), ws.changed()) },
      { id: 'view.panel', label: 'View: Toggle Panel', keys: 'Ctrl+J', run: togglePanel },
      { id: 'view.explorer', label: 'View: Show Explorer', keys: 'Ctrl+Shift+E', icon: 'file', run: () => showView('files') },
      { id: 'view.search', label: 'View: Show Search', keys: 'Ctrl+Shift+F', icon: 'search', run: () => showView('search') },
      { id: 'view.scm', label: 'View: Show Source Control', keys: 'Ctrl+Shift+G', icon: 'branch', run: () => showView('scm') },
      { id: 'view.agents', label: 'View: Show Ask', keys: 'Ctrl+L', icon: 'sparkle', run: () => askAgent() },
      { id: 'claude.mention', label: 'Agents: Send Selection to Console', keys: 'Alt+K', icon: 'message', run: () => ws.actions.mention?.() },
      { id: 'agents.toggle', label: 'View: Toggle Agents Side Bar', keys: 'Ctrl+Alt+B', icon: 'bot', run: () => ((ws.rightOpen = !ws.rightOpen), ws.changed()) },
      ...choices.agents.flatMap<Command>((a) => [
        // a phone's Code view has no side bar: a tab there (pageSide.ts)
        { id: `agents.side.${a.id}`, label: `Agents: Open ${a.name} in the Side Bar`, icon: 'bot', run: () => void startAgentPane(ws, ctl.workspaceId, a, noSideBar(ws) ? 'tab' : 'side', undefined, nodeId) },
        { id: `agents.tab.${a.id}`, label: `Agents: Open ${a.name} in a New Tab`, icon: 'bot', run: () => void startAgentPane(ws, ctl.workspaceId, a, 'tab', undefined, nodeId) },
      ]),
      { id: 'agents.orch.side', label: 'Agents: Open Orchestrator in the Side Bar', icon: 'orchestrator', run: () => placePaneSide(ws, ORCHESTRATOR_PANE) },
      { id: 'agents.orch.tab', label: 'Agents: Open Orchestrator in a New Tab', icon: 'orchestrator', run: () => ws.placePane(ORCHESTRATOR_PANE, 'tab') },
      { id: 'ai.explain', label: 'AI: Explain This', icon: 'sparkle', run: () => askAgent(undefined, 'Explain this code.') },
      { id: 'ai.bugs', label: 'AI: Find Bugs Here', icon: 'sparkle', run: () => askAgent(undefined, 'Look for bugs in this code. Say what is wrong and why, most serious first.') },
      { id: 'view.debug', label: 'View: Show Run and Debug', keys: 'Ctrl+Shift+D', icon: 'bug', run: () => showView('debug') },
      ...(ws.parts.some((pt) => pt.laravel?.detected) ? [{ id: 'view.laravel', label: 'View: Show Laravel', icon: 'layers' as const, run: () => showView('laravel') }] : []),
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
      { id: 'lsp.restart', label: 'Language Servers: Restart', icon: 'restart', run: () => ws.parts.forEach((pt) => pt.lsp.restart()) },
      { id: 'lang.pick', label: 'Change Language Mode', run: () => pick('language') },
      { id: 'lang.indent', label: 'Change Indentation', run: () => pick('indent') },
      { id: 'lang.eol', label: 'Change End of Line Sequence', run: () => pick('eol') },
      prefToggle('View: Toggle Minimap', 'minimap'),
      prefToggle('View: Toggle Word Wrap', 'wordWrap'),
      prefToggle('View: Toggle Sticky Scroll', 'stickyScroll'),
      prefToggle('View: Toggle Render Whitespace', 'renderWhitespace'),
      prefToggle('Preferences: Toggle Format on Save', 'formatOnSave'),
      // the Code view's tools, each an editor tab
      ...(page ? TOOL_ITEMS.map<Command>((t) => ({ id: `tool.${t.kind}`, label: `View: Open ${t.label}`, icon: t.icon, run: () => openTool(ws, t.kind) })) : []),
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
  }, [ws, ctl.workspaceId, togglePanel, showView, askAgent, pick, fullscreen.active, fullscreen.supported, choices, nodeId, page]);

  // ---- global shortcuts ----
  const pageRef = useRef(page);
  pageRef.current = page;
  const latest = useRef({ showView, togglePanel, askAgent, palette, commands, startDebug, toggleBreakpointHere });
  latest.current = { showView, togglePanel, askAgent, palette, commands, startDebug, toggleBreakpointHere };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // a dialog or the palette above the workbench keeps its keys
      if (document.querySelector('.modal, .wb-palette')) return;
      // a card among others: its shortcuts count only while the user works inside it.
      // A clicked button may be gone by now, which leaves the focus on the page itself.
      const root = rootRef.current;
      // the Code view is the whole page: a key with nothing focused is its own
      const onPage = document.activeElement === document.body && (clickedInside.current || pageRef.current);
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

  // The Code view's right side bar is open or not on its own, apart from the project's Root cards
  // (which share the rest of this editor state): while the page shows, ws.rightOpen is the page's,
  // and the cards' value comes back when it goes. The first time a browser opens it on a screen
  // wide enough to have it beside the editor, it shows.
  useLayoutEffect(() => {
    if (!page) return;
    const key = `codeview.right.${project.id}`;
    const cards = ws.rightOpen;
    const saved = prefs.get(key);
    ws.rightOpen = saved ? saved === '1' : window.innerWidth >= 1000;
    ws.changed();
    let gone = false;
    const restore = () => {
      if (gone) return;
      gone = true;
      prefs.set(key, ws.rightOpen ? '1' : '0');
      ws.rightOpen = cards;
      ws.changed();
      ws.flushSave();
    };
    window.addEventListener('pagehide', restore);
    return () => {
      window.removeEventListener('pagehide', restore);
      restore();
    };
  }, [page, project.id, ws]);

  // the phone's sheets, for what would show in the side bar it does not draw (pageSide.ts)
  useEffect(() => (phone ? registerPhoneSheets(ws, { lead: () => setSheet('lead'), agents: () => setSheet('agents') }) : undefined), [phone, ws]);

  // The lead's UI actions for this client (orch/store.ts): a board runs them (BoardView.tsx); the
  // Code view runs what an editor can, and for what only a board shows offers to open the board.
  useEffect(() => {
    if (!page) return;
    const onBoard = (what: string) =>
      toast.notify('info', `${what} is on the board`, 'The Code view has no board to show it on.', { label: 'Open the board', onClick: () => navigate(boardPath(project.id, ctl.workspaceId)) });
    return orchStore.onUiActions((acts: UiAction[]) => {
      for (const a of acts) runPageUiAction(a, { ws, project, ctl, onBoard, showView });
    });
  }, [page, ws, project, ctl, showView]);

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

  // the Code view's tools in the activity bar; Studios (and a phone's ⋯ Tools) open a menu beside it
  const [toolMenu, setToolMenu] = useState<{ x: number; y: number; label: string; items: MenuEntry[] } | null>(null);
  const toolMenuAt = (e: { currentTarget: HTMLElement }, label: string, items: CodeViewTool['menu']) => {
    const r = e.currentTarget.getBoundingClientRect();
    setToolMenu({ x: r.right + 4, y: r.top, label, items: (items ?? []).map((t) => ({ label: t.label, icon: t.icon, onSelect: () => openTool(ws, t.kind) })) });
  };
  // an editor card's project switcher: in its title bar, or in its ⋯ menu when the card is narrow
  const [switchAt, setSwitchAt] = useState<{ x: number; y: number } | null>(null);

  // a phone shows the side view as its Files view, whatever the editor state says
  const sideShown = phone ? phoneTab === 'files' : ws.sideOpen;
  // a tool's button is lit while the active tab is that tool (Studios: one of its studios)
  const activeTool = ws.activeTab?.tool?.kind;
  const toolShown = (t: CodeViewTool) => !!activeTool && (t.menu ? t.menu.some((m) => m.kind === activeTool) : t.kind === activeTool);
  const closing = ws.closing;
  const dirtyCount = [...ws.docs.values()].filter((d) => d.dirty).length;
  // the activity badges add up the parts the card shows
  const changedFiles = ws.parts.reduce((n, pt) => n + (pt.git?.files?.length ?? 0), 0);
  const reviewCount = ws.parts.reduce((n, pt) => n + pt.reviewCount, 0);

  return (
    <PaneEnv.Provider value={paneEnv}>
    <div
      className={`wb nokey ${mode === 'wide' ? '' : `is-${mode}`} ${page ? 'is-page' : ''} ${phone ? `is-phone is-phone-${phoneTab}` : ''} ${shown.layout === 'phone' || mode === 'narrow' ? 'is-one-group' : ''}`}
      ref={rootRef}
      tabIndex={-1}
      role="application"
      aria-label={`Code: ${project.name}`}
      // the board's own shortcuts listen further up: keys typed in the card stop here
      onKeyDown={(e) => e.stopPropagation()}
      // a double click inside picks a word; on the title bar it zooms the board to the card
      onDoubleClick={(e) => {
        if (!(e.target as HTMLElement).closest('.wb-title')) e.stopPropagation();
      }}
      // the title bar still gives the card's board menu; inside, menus are the editor's own
      onContextMenu={(e) => {
        if (!(e.target as HTMLElement).closest('.wb-title')) e.stopPropagation();
      }}
    >
      {page ? (
        topBar?.({ ws, quickOpen: (initial = '') => setPalette({ initial }), phone })
      ) : (
      <TileHead
        id={nodeId}
        className="wb-title"
        icon="code"
        // several parts: what the card shows ("acme-shop · admin", "acme-shop · All parts"), as the
        // project switcher (its menu opens the picked project's board) when the card has room
        title={mode === 'narrow' ? cardTitle(project, ws) : <EditorProjectSwitch project={project} target="board" label={cardTitle(project, ws)} className="wb-title-switch" />}
        menu={mode === 'narrow' ? [{ label: 'Switch project…', icon: 'folder', onSelect: () => setSwitchAt(switchSpot(rootRef.current)) }] : undefined}
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
      )}
      <div className="wb-body nodrag nowheel nopan" ref={bodyRef}>
        <nav className="wb-activity" aria-label="Views">
          {ACTIVITY.filter((a) => a.view !== 'laravel' || ws.parts.some((pt) => pt.laravel?.detected)).map((a) => (
            <button
              key={a.view}
              type="button"
              className={`wb-activity-btn ${sideShown && ws.side === a.view ? 'is-active' : ''}`}
              aria-label={a.label}
              aria-pressed={sideShown && ws.side === a.view}
              title={a.keys ? `${a.label} · ${a.keys}` : a.label}
              onClick={() => showView(a.view)}
            >
              <Icon name={a.icon} />
              {a.view === 'scm' && changedFiles > 0 && <span className="wb-activity-badge tabular">{changedFiles}</span>}
              {a.view === 'review' && reviewCount > 0 && <span className="wb-activity-badge tabular">{reviewCount}</span>}
            </button>
          ))}
          {page && <span className="wb-activity-sep" role="separator" />}
          {page &&
            !phone &&
            CODE_VIEW_TOOLS.map((t) => (
              <button
                key={t.label}
                type="button"
                className={`wb-activity-btn ${toolShown(t) ? 'is-active' : ''}`}
                aria-label={t.label}
                aria-pressed={toolShown(t)}
                aria-haspopup={t.menu ? 'menu' : undefined}
                title={t.keys ? `${t.label} · ${t.keys}` : t.label}
                data-tool={t.kind}
                onClick={(e) => (t.menu ? toolMenuAt(e, t.label, t.menu) : openTool(ws, t.kind))}
              >
                <Icon name={t.icon} />
              </button>
            ))}
          {page && phone && (
            <button type="button" className="wb-activity-btn" aria-label="Tools" aria-haspopup="menu" title="Tools" onClick={(e) => toolMenuAt(e, 'Tools', TOOL_ITEMS)}>
              <Icon name="more" />
            </button>
          )}
          <span className="spacer" />
          <button type="button" className={`wb-activity-btn ${sideShown && ws.side === 'settings' ? 'is-active' : ''}`} aria-label="Settings" aria-pressed={sideShown && ws.side === 'settings'} title="Settings" onClick={() => showView('settings')}>
            <Icon name="sliders" />
          </button>
        </nav>
        {sideShown && (
          <>
            {sideOver && !phone && <button type="button" className="wb-scrim wb-scrim-side" aria-label="Close the side bar" onClick={() => ((ws.sideOpen = false), ws.changed())} />}
            <aside className="wb-side" style={phone ? undefined : { width: ws.sideW }}>
              {ws.side === 'files' && <Explorer ws={ws} onAddFile={page ? undefined : addFile} onAddFolder={page ? undefined : addFolder} onTerminal={(dir) => void newTerminal(ws, ctl.workspaceId, dir)} onAsk={askAgent} />}
              {ws.side === 'search' && <SearchPanel ws={ws} focusTick={searchTick} />}
              {ws.side === 'scm' && <ScmPanel ws={ws} />}
              {ws.side === 'review' && <ReviewPanel ws={ws} />}
              {ws.side === 'debug' && <DebugView ws={ws} onInstall={installInTerminal} />}
              {ws.side === 'laravel' && <LaravelPanel ws={ws} onTerminal={installInTerminal} />}
              {ws.side === 'extensions' && <ExtensionsPanel ws={ws} onInstall={installInTerminal} />}
              {ws.side === 'agents' && <AgentsPanel ws={ws} ctl={ctl} nodeId={nodeId} context={context} setContext={setContext} request={agentRequest} focusTick={agentsTick} />}
              {ws.side === 'settings' && <SettingsPanel ws={ws} onInstall={installInTerminal} />}
            </aside>
            {!sideOver && !phone && <div className="wb-sash wb-sash-v" role="separator" aria-orientation="vertical" aria-label="Resize side bar" onPointerDown={startSide} />}
          </>
        )}
        <main className="wb-center">
          <DebugToolbar ws={ws} />
          <EditorArea ws={ws} onAddFile={addFile} aiOpen={aiOpen} setAiOpen={setAiOpen} />
          {(phone ? phoneTab === 'terminal' : ws.panelOpen) && (
            <>
              {!phone && <div className="wb-sash wb-sash-h" role="separator" aria-orientation="horizontal" aria-label="Resize panel" onPointerDown={startPanel} />}
              <div className="wb-panel-wrap" style={phone ? undefined : { height: ws.panelH, flex: '0 0 auto' }}>
                <BottomPanel ws={ws} workspaceId={ctl.workspaceId} />
              </div>
            </>
          )}
        </main>
        {ws.rightOpen && !phone && (
          <>
            {rightOver ? (
              <button type="button" className="wb-scrim" aria-label="Close the agents side bar" onClick={() => ((ws.rightOpen = false), ws.changed())} />
            ) : (
              <div className="wb-sash wb-sash-v" role="separator" aria-orientation="vertical" aria-label="Resize agents side bar" onPointerDown={startRight} />
            )}
            <AgentSideBar ws={ws} workspaceId={ctl.workspaceId} page={page} />
          </>
        )}
      </div>
      {!phone && (
        <div className="nodrag">
          <StatusBar ws={ws} onPick={pick} onView={showView} onPanel={togglePanel} />
        </div>
      )}
      {phone && <PhoneNav tab={phoneTab} sheet={sheet} onTab={pickPhoneTab} onSheet={setSheet} />}
      {phone && sheet && <PhoneSheetView kind={sheet} ws={ws} onClose={() => setSheet(null)} />}
      {toolMenu && <ContextMenu x={toolMenu.x} y={toolMenu.y} label={toolMenu.label} items={toolMenu.items} onClose={() => setToolMenu(null)} />}
      {switchAt && <SwitchProjectMenu project={project} target="board" x={switchAt.x} y={switchAt.y} onClose={() => setSwitchAt(null)} />}

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
      <ResumeChatsDialog ws={ws} workspaceId={ctl.workspaceId} />
      {confirmDialog}
      {/* fixed to the screen: out of the card, which the board moves and scales */}
      {typing === 'editor' && createPortal(<EditorKeyBar ws={ws} onGoToFile={() => setPalette({ initial: '' })} />, document.body)}
      {typing && typing !== 'editor' && createPortal(<KeyBar key={typing} sessionId={typing} name="the terminal" />, document.body)}
    </div>
    </PaneEnv.Provider>
  );
}

/** The board's first editor card: the one an older sender's openFile (no nodeId) goes to. */
const firstCard = (ctl: BoardController) => ctl.nodes.find((n) => n.kind === 'editor')?.id;

/**
 * The workbench of the card's placement (v0.9.0-beta.2): data.part names a part, else (absent, "",
 * a part that went) the card is at the Root, one tree of every part. A single-part project has one
 * placement, so every card shows its folder as before.
 */
export default function CodeStudio(props: Props) {
  // only data.part matters here: the rest of the board changing does not re-render the editor
  const part = useSyncExternalStore(props.ctl.sync.subscribe, () => {
    const v = props.ctl.node(props.nodeId)?.data.part;
    return typeof v === 'string' ? v : '';
  });
  const placement = placementOf({ part }, props.project);
  const key = viewKey(props.project.id, projectParts(props.project).length, placement);
  // the per-browser part switch of before (beta.1) is not read any more
  const atRoot = projectParts(props.project).length > 1 && isRoot(placement);
  useEffect(() => {
    if (atRoot) dropPref(`wb.part.${props.project.id}`);
  }, [props.project.id, atRoot]);
  return <CodeStudioView key={key} {...props} placement={placement} />;
}

function dropPref(key: string) {
  try {
    window.localStorage.removeItem(`nb.${key}`);
  } catch {
    /* ignore */
  }
}

function CodeStudioView({ project, ctl, ui, nodeId, viewCenter, filled, placement, mode = 'card', topBar }: Props & { placement: Placement }) {
  setupMonaco();
  // Blade and the Laravel helpers (the editor opener itself is nativeExt's)
  registerLaravel();
  const [ws] = useState(() => Workspace.get(project, placement));
  // the project changed (a part renamed, added, moved): the view follows, its tabs stay
  useEffect(() => {
    Workspace.get(project, placement);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- placement is fixed for this view (keyed)
  }, [project]);

  useEffect(() => {
    ws.ide.connect(ctl.workspaceId);
    return () => ws.ide.disconnect();
  }, [ws, ctl.workspaceId]);

  /** An openFile this card takes: its own (nodeId), or an older sender's when it is the first card. */
  const mine = useCallback((o: OpenFileRequest | null) => !!o && (o.nodeId ? o.nodeId === nodeId : firstCard(ctl) === nodeId), [ctl, nodeId]);

  useEffect(() => {
    ws.mount();
    registerInlineAi(ws);
    registerDebugHover(ws.debug);
    void ws.debug.refreshAdapters();
    void loadExtensions();
    if (!ws.groups.some((g) => g.tabs.length) && !mine(ui.get().openFile)) void openFirst(ws);
    return () => ws.unmount();
    // once per card
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws]);

  // "open this file" from elsewhere on the board (a file card, the lead, a commit): the board picks
  // the card by placement, and the file is (machine, path)
  const openFile = useUi((s) => s.openFile);
  useEffect(() => {
    if (!openFile || !mine(openFile)) return;
    const key = openFile.machineId ? fileKey(openFile.machineId, openFile.path) : ws.toKey(openFile.path);
    if (key) ws.open(key, { pin: true, source: true, line: openFile.line });
    ui.set({ openFile: null });
  }, [ws, ui, openFile, mine]);

  return <Workbench ws={ws} project={project} ctl={ctl} nodeId={nodeId} viewCenter={viewCenter} filled={filled} page={mode === 'page'} topBar={topBar} />;
}

/** Every part's tasks of the card, with their part (the palette's Tasks: Run Task…). */
async function partTasks(ws: Workspace) {
  const lists = await Promise.all(ws.parts.map((part) => taskApi.list(part.gitTarget).then((list) => list.map((task) => ({ part, task })))));
  return lists.flat();
}

/** First visit: the README if there is one, as a preview tab (a card of one part; a Root card opens none). */
async function openFirst(ws: Workspace) {
  if (ws.paths.multi) return;
  const part = ws.parts[0];
  if (!part) return;
  try {
    const list = await part.fileList();
    const rel = (list.items ?? []).find((p) => /^readme(\.|$)/i.test(p));
    if (rel && !ws.groups.some((g) => g.tabs.length)) ws.open(part.keyOf(rel));
  } catch {
    /* the tree still works */
  }
}
