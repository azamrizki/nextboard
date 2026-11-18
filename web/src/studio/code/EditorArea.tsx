// Editor groups of the Code workbench: tab strips (preview tabs in italics, dirty dots, pinned
// tabs first, a lock for locked ones), breadcrumbs, one Monaco editor per group, diffs against
// HEAD or the disk, Markdown as Reader/Split/Source, image and media preview, and the bars that
// ask what to do when a file changed on disk. Inline AI edits (Ctrl+I) end in a diff to accept
// or reject.
//
// The groups lie on a grid of up to four, two by two (grid.ts, docs/API-studio.md §7.15): a tab
// dragged to a group's edge splits it there, Split Right / Split Down (Ctrl+\, Ctrl+K Ctrl+\)
// do it from the tab, and the sashes between them are dragged. Under 640 px only the active
// group shows. A tab can also be a console (an agent, a terminal, a board's agent) or a tool.

import { useContext, useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type DragEvent, type PointerEvent as ReactPointerEvent, type ReactNode, type RefObject } from 'react';
import '../../styles/editor-grid.css';
import { errorMessage } from '../../api';
import { clipboardFiles } from '../../board/files';
import { watchFileDrop } from '../../lib/fileDrop';
import { Button } from '../../components/Button';
import { SegmentedControl } from '../../components/form';
import { Icon } from '../../components/Icon';
import { ContextMenu, Menu, type MenuEntry } from '../../components/Menu';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import { filesApi, gitApi } from '../api';
import { MarkdownView } from '../MarkdownView';
import { waitJob } from '../store';
import { useDebugEditor } from './DebugPanel';
import { agentMenu, PaneBody as AgentPane, PaneEnv, PaneTitle, usePaneChoices } from './AgentPanes';
import { attachFeatures } from './editorFeatures';
import { ProposalPane } from './IdeProposal';
import { FileIcon } from './Explorer';
import { showHistory, toggleFileBlame } from './gitLens';
import { hasNative } from './nativeExt';
import { gitError, gitSideChanged } from '../../git/common';
import { jumpBookmark, toggleBookmark } from './bookmarks';
import { CommitGraphView } from './CommitGraph';
import { CommitView } from './CommitView';
import { LocalHistoryView } from './LocalHistoryView';
import { PullRequestView } from './PullRequestView';
import { baseWithChange, currentWithoutChange, type LineChange } from './reviewHunks';
import { ReviewView } from './ReviewView';
import { currentTheme, FONT, languageFor, monaco } from './monaco';
import { editorKeys } from './editorKeys';
import { useWs } from './useWs';
import { rawUrlOf, readKey } from './docs';
import { crumbsOf, tabDir } from './partGroups';
import type { PartSpecialView } from './contracts';
import { mountedPart } from './partGit';
import type { PartService } from './partsvc';
import { asFileKey, cleanAbs, fileKey, splitKey, type FileKey } from './paths';
import { plan, type Edge, type GridBox, type GridPlan } from './grid';
import ToolTab from './ToolTab';
import { toolMeta } from './toolsList';
import { dirOf, extOf, isTerminalPane, MARKDOWN_EXT, nameOf, type Doc, type Group, type MdView, type Tab, type Workspace } from './workspace';

type Editor = monaco.editor.IStandaloneCodeEditor;


function editorOptions(ws: Workspace, readOnly: boolean): monaco.editor.IStandaloneEditorConstructionOptions {
  const p = ws.editorPrefs;
  return {
    theme: currentTheme(),
    automaticLayout: true,
    fontFamily: FONT,
    fontSize: p.fontSize,
    fontLigatures: true,
    tabSize: p.tabSize,
    detectIndentation: true,
    minimap: { enabled: p.minimap },
    wordWrap: p.wordWrap ? 'on' : 'off',
    stickyScroll: { enabled: p.stickyScroll },
    bracketPairColorization: { enabled: p.bracketColors },
    guides: { bracketPairs: p.bracketColors, indentation: true },
    renderWhitespace: p.renderWhitespace ? 'all' : 'selection',
    readOnly,
    domReadOnly: readOnly,
    padding: { top: 8 },
    smoothScrolling: true,
    cursorSmoothCaretAnimation: 'on',
    cursorBlinking: 'smooth',
    mouseWheelZoom: true,
    fixedOverflowWidgets: false,
    linkedEditing: true,
    folding: true,
    glyphMargin: true,
    lightbulb: { enabled: monaco.editor.ShowLightbulbIconMode.On },
    suggest: { preview: true, showStatusBar: true },
    inlayHints: { enabled: 'on' },
    scrollBeyondLastLine: false,
    contextmenu: false,
  };
}

/** Line ranges changed against HEAD, from a unified diff. */
function parseHunks(diff: string) {
  const added: [number, number][] = [];
  const modified: [number, number][] = [];
  const deleted: number[] = [];
  let line = 0;
  let plus = 0;
  let minus = 0;
  const flush = () => {
    if (plus && minus) modified.push([line - plus, line - 1]);
    else if (plus) added.push([line - plus, line - 1]);
    else if (minus) deleted.push(Math.max(line - 1, 1));
    plus = minus = 0;
  };
  for (const l of diff.split('\n')) {
    if (l.startsWith('@@')) {
      flush();
      const m = /\+(\d+)/.exec(l);
      line = m ? Number(m[1]) : line;
    } else if (!line) {
      continue;
    } else if (l.startsWith('+')) {
      plus++;
      line++;
    } else if (l.startsWith('-')) {
      minus++;
    } else if (l.startsWith(' ')) {
      flush();
      line++;
    }
  }
  flush();
  return { added, modified, deleted };
}

const viewStates = new WeakMap<Group, Map<string, monaco.editor.ICodeEditorViewState | null>>();

function MonacoView({ ws, group, gi, doc, tab }: { ws: Workspace; group: Group; gi: number; doc: Doc; tab: Tab }) {
  const host = useRef<HTMLDivElement>(null);
  const vimStatus = useRef<HTMLDivElement>(null);
  const edRef = useRef<Editor | null>(null);
  const shown = useRef<string | null>(null);
  const pathRef = useRef(doc.path);
  pathRef.current = doc.path;
  // its own reason (not UTF-8, too large), or outside every part of the project
  const readOnly = !!ws.readOnlyOf(doc);
  useDebugEditor(edRef, ws, doc.path, doc.model);

  // a layout effect, so that the editor exists when the model effect below runs
  useLayoutEffect(() => {
    if (!host.current) return;
    const ed = monaco.editor.create(host.current, { ...editorOptions(ws, readOnly), model: null });
    edRef.current = ed;
    ws.editors.set(group.id, ed);
    const keys = editorKeys(ed, {
      save: () => void ws.save(pathRef.current),
      inlineSuggest: () => void ed.getAction('editor.action.inlineSuggest.trigger')?.run(),
      mention: () => ws.actions.mention?.(),
      bookmark: () => toggleBookmark(ws, pathRef.current, ed.getPosition()?.lineNumber ?? 1),
      nextBookmark: () => jumpBookmark(ws, pathRef.current, ed.getPosition()?.lineNumber ?? 1, 1),
      prevBookmark: () => jumpBookmark(ws, pathRef.current, ed.getPosition()?.lineNumber ?? 1, -1),
      format: () => void ed.getAction('editor.action.formatDocument')?.run(),
      pin: () => group.active && ws.setPinned(ws.groups.indexOf(group), group.active),
    });
    const subs = [
      keys,
      ed.onDidFocusEditorText(() => ws.focusGroup(ws.groups.indexOf(group))),
      ed.onDidBlurEditorText(() => ws.focusChanged()),
      ed.onDidChangeCursorSelection(() => {
        const s = ed.getSelection();
        const m = ed.getModel();
        if (s && m && ws.groups[ws.activeGroup] === group) ws.setCursor(s.positionLineNumber, s.positionColumn, m.getValueLengthInRange(s));
      }),
      attachFeatures(ed, ws, vimStatus.current!, () => ws.groups.indexOf(group)),
    ];
    const el = host.current;
    const onPaste = (e: ClipboardEvent) => {
      if (ed.getOption(monaco.editor.EditorOption.readOnly)) return;
      const files = clipboardFiles(e.clipboardData);
      if (!files.length) return;
      e.preventDefault();
      e.stopPropagation();
      void ws.pasteIntoEditor(ed, pathRef.current, files);
    };
    el.addEventListener('paste', onPaste, true);
    // a file dropped on the editor is a pasted one, at the place it was dropped
    const undrop = watchFileDrop(
      el,
      (files, e) => {
        if (ed.getOption(monaco.editor.EditorOption.readOnly)) {
          toast.info('This file is read-only here.');
          return;
        }
        const at = ed.getTargetAtClientPoint(e.clientX, e.clientY)?.position;
        if (at) ed.setPosition(at);
        void ws.pasteIntoEditor(ed, pathRef.current, files);
      },
      { label: 'Drop to add here' },
    );
    return () => {
      el.removeEventListener('paste', onPaste, true);
      undrop();
      subs.forEach((s) => s.dispose());
      const states = viewStates.get(group);
      if (shown.current && states) states.set(shown.current, ed.saveViewState());
      if (ws.editors.get(group.id) === ed) ws.editors.delete(group.id);
      ed.dispose();
      edRef.current = null;
    };
    // options follow below; the editor lives as long as the view
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, group]);

  // the file shown changed: keep each tab's scroll and cursor
  useLayoutEffect(() => {
    const ed = edRef.current;
    if (!ed) return;
    let states = viewStates.get(group);
    if (!states) {
      states = new Map();
      viewStates.set(group, states);
    }
    if (shown.current && shown.current !== doc.path) states.set(shown.current, ed.saveViewState());
    if (ed.getModel() !== doc.model) ed.setModel(doc.model);
    if (shown.current !== doc.path) {
      const vs = states.get(doc.path);
      if (vs) ed.restoreViewState(vs);
    }
    shown.current = doc.path;
  }, [group, doc.path, doc.model]);

  useEffect(() => {
    edRef.current?.updateOptions(editorOptions(ws, readOnly));
  }, [ws, ws.editorPrefs, readOnly]);

  // a line was asked for: from search, go to file or a definition
  const req = ws.reveal;
  useEffect(() => {
    const ed = edRef.current;
    if (!ed || !req || req.group !== group.id || req.tabId !== tab.id || ed.getModel() !== doc.model) return;
    ws.reveal = null;
    const col = req.col ?? 1;
    const range = new monaco.Range(req.line, col, req.line, req.endCol ?? col);
    ed.setSelection(range);
    ed.revealRangeInCenterIfOutsideViewport(range);
  }, [ws, req, group, tab.id, doc.model]);

  const focusTick = ws.focusTick;
  useEffect(() => {
    if (ws.focusDone === focusTick || ws.groups[ws.activeGroup] !== group || document.querySelector('.wb-palette, .modal')) return;
    ws.focusDone = focusTick;
    edRef.current?.focus();
    // only when focus was asked for, not on every change of the group, and not by appearing
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusTick]);

  // gutter marks against HEAD of the file's part, refreshed when git or the saved file changes
  const part = ws.partOf(doc.path);
  const repoRel = part?.repoRel(doc.path) ?? null;
  const mark = part?.gitMark(doc.path);
  const tracked = !!repoRel && mark && mark.letter !== 'U';
  useEffect(() => {
    const ed = edRef.current;
    if (!ed || !doc.model) return;
    const coll = ed.createDecorationsCollection();
    if (!tracked || !repoRel || !part) return () => coll.clear();
    let stop = false;
    gitApi
      .diff(part.gitTarget, repoRel)
      .then((d) => {
        if (stop || d.binary || d.truncated || ed.getModel() !== doc.model) return;
        const h = parseHunks(d.diff);
        const lane = monaco.editor.OverviewRulerLane.Left;
        const deco = (start: number, end: number, cls: string, color: string): monaco.editor.IModelDeltaDecoration => ({
          range: new monaco.Range(start, 1, end, 1),
          options: { isWholeLine: true, linesDecorationsClassName: cls, overviewRuler: { color, position: lane }, minimap: { color, position: monaco.editor.MinimapPosition.Gutter } },
        });
        coll.set([
          ...h.added.map(([a, b]) => deco(a, b, 'wb-gutter-added', '#2fdc8a')),
          ...h.modified.map(([a, b]) => deco(a, b, 'wb-gutter-modified', '#6e95ff')),
          ...h.deleted.map((l) => deco(l, l, 'wb-gutter-deleted', '#ff5c77')),
        ]);
      })
      .catch(() => undefined);
    return () => {
      stop = true;
      coll.clear();
    };
  }, [ws, part, part?.gitVersion, doc.model, doc.mtime, repoRel, tracked]);

  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  useEffect(() => {
    const ed = edRef.current;
    if (!ed) return;
    const sub = ed.onContextMenu((e) => {
      if (e.target.type === monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN) return;
      e.event.preventDefault();
      if (e.target.position) {
        // a click outside the selection moves the cursor there, as in VS Code
        const sel = ed.getSelection();
        if (!sel?.containsPosition(e.target.position)) ed.setPosition(e.target.position);
      }
      ed.focus();
      setMenu({ x: e.event.browserEvent.clientX, y: e.event.browserEvent.clientY });
    });
    return () => sub.dispose();
  }, [ws, group]);

  const run = (id: string) => () => {
    const ed = edRef.current;
    ed?.focus();
    void ed?.getAction(id)?.run();
  };
  const hasSelection = !!edRef.current?.getSelection() && !edRef.current?.getSelection()?.isEmpty();
  const noun = hasSelection ? 'this code' : 'this file';
  const menuItems: MenuEntry[] = [
    { label: 'Ask About This…', icon: 'sparkle', shortcut: 'Ctrl+L', onSelect: () => ws.actions.ask?.() },
    { label: 'Explain', icon: 'message', onSelect: () => ws.actions.ask?.(`Explain ${noun}.`) },
    { label: 'Find Bugs', icon: 'alertCircle', onSelect: () => ws.actions.ask?.(`Look for bugs in ${noun}. Say what is wrong and why, most serious first.`) },
    { label: 'Write Tests', icon: 'checklist', onSelect: () => ws.actions.ask?.(`Write tests for ${noun}.`) },
    { label: 'Edit with AI…', icon: 'pencil', shortcut: 'Ctrl+I', disabled: readOnly, onSelect: () => ws.actions.aiEdit?.() },
    ...(ws.ide.consoles.length ? ([{ label: 'Send to Claude Code', icon: 'message', shortcut: 'Alt+K', onSelect: () => ws.actions.mention?.() }] as MenuEntry[]) : []),
    'sep',
    { label: 'Go to Definition', shortcut: 'F12', onSelect: run('editor.action.revealDefinition') },
    { label: 'Go to References', shortcut: 'Shift+F12', onSelect: run('editor.action.goToReferences') },
    { label: 'Peek Definition', shortcut: 'Alt+F12', onSelect: run('editor.action.peekDefinition') },
    { label: 'Rename Symbol', shortcut: 'F2', disabled: readOnly, onSelect: run('editor.action.rename') },
    { label: 'Change All Occurrences', shortcut: 'Ctrl+F2', disabled: readOnly, onSelect: run('editor.action.changeAll') },
    { label: 'Format Document', shortcut: 'Shift+Alt+F', disabled: readOnly, onSelect: run('editor.action.formatDocument') },
    'sep',
    { label: 'Cut', shortcut: 'Ctrl+X', disabled: readOnly, onSelect: run('editor.action.clipboardCutAction') },
    { label: 'Copy', shortcut: 'Ctrl+C', onSelect: run('editor.action.clipboardCopyAction') },
    { label: 'Paste', shortcut: 'Ctrl+V', disabled: readOnly, onSelect: run('editor.action.clipboardPasteAction') },
    'sep',
    { label: 'Toggle Breakpoint', icon: 'bug', shortcut: 'F9', onSelect: () => { const l = edRef.current?.getPosition()?.lineNumber; if (l) ws.debug.toggleBreakpoint(doc.path, l); } },
    // not in the Code view, which has no board (DESIGN.md §3.19)
    ...(ws.actions.addToBoard ? ([{ label: 'Add to Board', icon: 'plus', onSelect: () => ws.actions.addToBoard?.(doc.path) }] as MenuEntry[]) : []),
    { label: 'Local History', icon: 'history', onSelect: () => ws.openView({ kind: 'history', path: doc.path }) },
    { label: 'Toggle Bookmark', icon: 'bookmark', shortcut: 'Ctrl+Alt+K', onSelect: () => toggleBookmark(ws, doc.path, edRef.current?.getPosition()?.lineNumber ?? 1) },
    ...(hasNative('gitlens')
      ? ([
          'sep',
          { label: 'Show File History', icon: 'history', onSelect: () => void showHistory(ws, doc.path) },
          { label: 'Show Line History', icon: 'history', onSelect: () => void showHistory(ws, doc.path, edRef.current?.getPosition()?.lineNumber) },
          { label: 'Toggle File Blame', icon: 'history', onSelect: () => edRef.current && void toggleFileBlame(ws, edRef.current) },
          'sep',
        ] as MenuEntry[])
      : []),
    { label: 'Command Palette…', icon: 'search', shortcut: 'Ctrl+Shift+P', onSelect: () => ws.actions.palette?.() },
  ];

  return (
    <div className="wb-monaco-col">
      <div className="wb-monaco" ref={host} data-keep-escape="" onMouseDown={() => ws.focusGroup(gi)} />
      <div className="wb-vim-status" ref={vimStatus} data-keep-escape="" />
      {menu && <ContextMenu x={menu.x} y={menu.y} label="Editor actions" items={menuItems} onClose={() => setMenu(null)} />}
    </div>
  );
}

function useModelText(model: monaco.editor.ITextModel | null): string {
  const [text, setText] = useState(() => model?.getValue() ?? '');
  useEffect(() => {
    if (!model) return;
    setText(model.getValue());
    let t = 0;
    const sub = model.onDidChangeContent(() => {
      window.clearTimeout(t);
      t = window.setTimeout(() => setText(model.getValue()), 120);
    });
    return () => {
      window.clearTimeout(t);
      sub.dispose();
    };
  }, [model]);
  return text;
}

const headingId = (text: string) =>
  text
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s-]/gu, '')
    .replace(/\s+/g, '-');

function MarkdownReader({ ws, doc, scrollRef }: { ws: Workspace; doc: Doc; scrollRef?: RefObject<HTMLDivElement | null> }) {
  const ref = useRef<HTMLDivElement | null>(null);
  const text = useModelText(doc.model);
  // URL math on the real path, on the file's own machine, then a key again
  const { machineId, abs: docAbs } = splitKey(doc.path);
  const dir = dirOf(docAbs);
  const keyAt = (rel: string): FileKey | null => {
    const p = cleanAbs(decodeURI(new URL(rel, `file://${dir === '/' ? '' : dir}/`).pathname));
    return p ? fileKey(machineId, p) : null;
  };
  const resolve = (src: string) => {
    if (/^[a-z]+:/i.test(src) || src.startsWith('/')) return null;
    const k = keyAt(src);
    return k && ws.partOf(k) ? rawUrlOf(k) : null;
  };
  const onLink = (href: string) => {
    if (/^[a-z]+:/i.test(href)) return false;
    const [target] = href.split('#');
    if (!target) {
      // same page: only this reader may scroll
      ref.current?.querySelector(`[id="${CSS.escape(href.slice(1))}"]`)?.scrollIntoView({ block: 'start' });
      return true;
    }
    const k = keyAt(target);
    if (!k || !MARKDOWN_EXT.has(extOf(k))) return false;
    if (!ws.partOf(k)) {
      toast.error('That link points outside the project.');
      return true;
    }
    ws.open(k, { pin: true });
    return true;
  };
  // code blocks in the file's own language, coloured by the editor; copy buttons, heading links
  useEffect(() => {
    const t = window.setTimeout(() => {
      const root = ref.current;
      if (!root) return;
      const seen = new Map<string, number>();
      for (const h of root.querySelectorAll<HTMLElement>('h1, h2, h3, h4, h5, h6')) {
        const base = headingId(h.textContent?.replace(/^#/, '') ?? '');
        const n = seen.get(base) ?? 0;
        seen.set(base, n + 1);
        h.id = n ? `${base}-${n}` : base;
        if (h.querySelector('.wb-md-anchor')) continue;
        const a = document.createElement('a');
        a.className = 'wb-md-anchor';
        a.href = `#${h.id}`;
        a.setAttribute('aria-label', 'Link to this heading');
        a.textContent = '#';
        h.prepend(a);
      }
      for (const pre of root.querySelectorAll<HTMLElement>('pre')) {
        if (pre.querySelector('.wb-md-copy')) continue;
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'wb-md-copy';
        b.textContent = 'Copy';
        pre.append(b);
      }
      for (const el of ref.current?.querySelectorAll<HTMLElement>('pre code[class*="language-"]') ?? []) {
        const lang = /language-([\w-]+)/.exec(el.className)?.[1];
        if (!lang || el.dataset.lang) continue;
        el.dataset.lang = monaco.languages.getLanguages().find((l) => l.id === lang || l.aliases?.some((a) => a.toLowerCase() === lang.toLowerCase()) || l.extensions?.includes(`.${lang}`))?.id ?? 'plaintext';
        void monaco.editor.colorizeElement(el, { theme: currentTheme() });
      }
    }, 150);
    return () => window.clearTimeout(t);
  }, [text]);
  return (
    <div
      className="wb-md nowheel"
      ref={(el) => {
        ref.current = el;
        if (scrollRef) scrollRef.current = el;
      }}
      onClick={(e) => {
        const btn = (e.target as HTMLElement).closest('.wb-md-copy');
        if (!btn) return;
        // the colouriser writes non-breaking spaces
        const code = (btn.parentElement?.querySelector('code')?.textContent ?? '').replace(/\u00a0/g, ' ');
        void navigator.clipboard?.writeText(code).then(() => {
          btn.textContent = 'Copied';
          window.setTimeout(() => (btn.textContent = 'Copy'), 1200);
        });
      }}
    >
      <MarkdownView text={text} className="md-note" resolveImage={resolve} onLink={onLink} anchors />
    </div>
  );
}

function DiffPane({ ws, tab, doc }: { ws: Workspace; tab: Tab; doc: Doc }) {
  const host = useRef<HTMLDivElement>(null);
  const diffRef = useRef<monaco.editor.IStandaloneDiffEditor | null>(null);
  const baseRef = useRef<monaco.editor.ITextModel | null>(null);
  const stagedRef = useRef<monaco.editor.ITextModel | null>(null);
  const [sideBySide, setSideBySide] = useState(true);
  const [note, setNote] = useState('');
  const [changes, setChanges] = useState<LineChange[]>([]);
  const [at, setAt] = useState(0);
  const [busy, setBusy] = useState(false);
  // git of the part holding the file (a commit's files are in its part's repository)
  const part = ws.partOf(doc.path);
  const rel = part?.repoRel(doc.path) ?? null;
  const file = part?.git?.files?.find((f) => f.path === rel);
  const partlyStaged = !!file && gitSideChanged(file.index);
  // against HEAD, staging a part only works while the index still is HEAD
  const canStage = tab.diff === 'index' || (tab.diff === 'head' && !partlyStaged);
  const canUnstage = tab.diff === 'staged';

  useEffect(() => {
    if (!host.current || !doc.model) return;
    let stop = false;
    let original: monaco.editor.ITextModel | null = null;
    let modified: monaco.editor.ITextModel | null = null;
    const readIndex = async (path: string) => {
      if (!part) throw new Error('This file is in no part of the project.');
      const f = await gitApi.index(part.gitTarget, path);
      if (f.binary) throw new Error('Binary file: nothing to compare.');
      return f.text;
    };
    const diff = monaco.editor.createDiffEditor(host.current, {
      theme: currentTheme(),
      automaticLayout: true,
      fontFamily: FONT,
      fontSize: ws.editorPrefs.fontSize,
      renderSideBySide: sideBySide,
      originalEditable: false,
      ignoreTrimWhitespace: false,
      readOnly: !!ws.readOnlyOf(doc),
      fixedOverflowWidgets: false,
    });
    diffRef.current = diff;
    // closing the last tab of a file disposes its model before this view goes
    const leaving = doc.model.onWillDispose(() => diff.setModel(null));
    const updated = diff.onDidUpdateDiff(() => {
      const list = diff.getLineChanges() ?? [];
      setChanges(list);
      setAt((i) => Math.min(i, Math.max(list.length - 1, 0)));
    });
    const keys = editorKeys(diff.getModifiedEditor(), { save: () => void ws.save(doc.path) });
    const commit = tab.diff?.startsWith('commit:') ? tab.diff.slice('commit:'.length) : null;
    const load = async (ref = 'HEAD'): Promise<string> => {
      if (tab.diff === 'disk') {
        const r = await readKey(doc.path, 10 << 20);
        return r.content.startsWith('﻿') ? r.content.slice(1) : r.content;
      }
      const rel = part?.repoRel(doc.path);
      if (!part || !rel) throw new Error('This file is not in the selected Git repository.');
      if (tab.diff === 'index') {
        const f = await gitApi.index(part.gitTarget, rel);
        if (f.binary) throw new Error('Binary file: nothing to compare.');
        if (!f.exists) setNote('New file: not in Git yet.');
        return f.text;
      }
      const f = await gitApi.show(part.gitTarget, rel, ref);
      if (f.binary) throw new Error('Binary file: nothing to compare.');
      if (!f.exists && !commit) setNote('New file: not in HEAD yet.');
      return f.content;
    };
    const language = languageFor(doc.path) ?? doc.model.getLanguageId();
    if (commit || tab.diff === 'staged') diff.updateOptions({ readOnly: true });
    const rel0 = part?.repoRel(doc.path);
    (commit ? Promise.all([load(`${commit}^`), load(commit)]) : tab.diff === 'staged' && rel0 ? Promise.all([load(), readIndex(rel0)]) : load().then((t) => [t, null] as const))
      .then(([before, after]) => {
        if (stop || !doc.model) return;
        original = monaco.editor.createModel(before, language);
        if (after !== null) modified = monaco.editor.createModel(after, language);
        baseRef.current = original;
        stagedRef.current = modified;
        diff.setModel({ original, modified: modified ?? doc.model });
      })
      .catch((err) => !stop && setNote(errorMessage(err)));
    return () => {
      stop = true;
      updated.dispose();
      leaving.dispose();
      keys.dispose();
      diff.setModel(null);
      diff.dispose();
      original?.dispose();
      modified?.dispose();
      diffRef.current = null;
      baseRef.current = null;
      stagedRef.current = null;
    };
    // sideBySide updates in place; the file's text is read when the tab opens
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, part, doc.model, tab.diff, doc.path]);

  useEffect(() => {
    diffRef.current?.updateOptions({ renderSideBySide: sideBySide });
  }, [sideBySide]);

  const current = changes[at];
  useEffect(() => {
    if (current) diffRef.current?.getModifiedEditor().revealLineInCenterIfOutsideViewport(Math.max(current.modifiedStartLineNumber, 1));
  }, [current]);

  /** Put one change into the index, or take it out. */
  const moveChange = async (stage: boolean) => {
    const base = baseRef.current?.getValue();
    const after = (stagedRef.current ?? doc.model)?.getValue();
    if (!part || !rel || !current || base === undefined || after === undefined) return;
    setBusy(true);
    try {
      const text = stage ? baseWithChange(base, after, current) : currentWithoutChange(base, after, current);
      await gitApi.stage(part.gitTarget, [rel], text);
      if (stage) baseRef.current?.setValue(text);
      else stagedRef.current?.setValue(text);
      void part.refreshGit();
    } catch (err) {
      toast.error(gitError(err));
    } finally {
      setBusy(false);
    }
  };

  /** Take one change out of the file, in the editor, where Ctrl+Z brings it back. */
  const revertChange = () => {
    const base = baseRef.current?.getValue();
    const model = doc.model;
    if (!current || base === undefined || !model) return;
    model.pushStackElement();
    model.pushEditOperations([], [{ range: model.getFullModelRange(), text: currentWithoutChange(base, model.getValue(), current) }], () => null);
    model.pushStackElement();
  };

  return (
    <div className="wb-diff">
      <div className="wb-diff-bar wb-rv-bar">
        <span className="truncate">
          {tab.diff?.startsWith('commit:')
            ? `Changes in ${tab.diff.slice('commit:'.length, 'commit:'.length + 8)}`
            : tab.diff === 'staged'
              ? 'HEAD ↔ staged'
              : `${tab.diff === 'disk' ? 'On disk' : tab.diff === 'index' ? 'Staged' : 'HEAD'} ↔ ${doc.dirty ? 'your edits' : 'working tree'}`}
        </span>
        {note && <span className="text-3 truncate">{note}</span>}
        <span className="spacer" />
        {(canStage || canUnstage) && changes.length > 0 && (
          <span className="wb-rv-step">
            <Button size="sm" variant="ghost" iconOnly icon="arrowUp" aria-label="Previous change" disabled={at === 0} onClick={() => setAt((i) => Math.max(i - 1, 0))} />
            <span className="tabular">
              Change {at + 1} of {changes.length}
            </span>
            <Button size="sm" variant="ghost" iconOnly icon="arrowDown" aria-label="Next change" disabled={at >= changes.length - 1} onClick={() => setAt((i) => Math.min(i + 1, changes.length - 1))} />
            {canStage && (
              <Button size="sm" variant="ghost" onClick={revertChange}>
                Revert change
              </Button>
            )}
            {canStage && (
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => void moveChange(true)}>
                Stage change
              </Button>
            )}
            {canUnstage && (
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => void moveChange(false)}>
                Unstage change
              </Button>
            )}
          </span>
        )}
        <Button size="sm" variant="ghost" onClick={() => setSideBySide((v) => !v)}>
          {sideBySide ? 'Inline' : 'Side by side'}
        </Button>
        <Button size="sm" variant="ghost" icon="file" onClick={() => ws.open(doc.path, { pin: true, source: true })}>
          Open file
        </Button>
      </div>
      <div className="wb-monaco" ref={host} data-keep-escape="" />
    </div>
  );
}

function Banners({ ws, doc }: { ws: Workspace; doc: Doc }) {
  const path = doc.path;
  return (
    <>
      {doc.recovered && (
        <div className="studio-banner wb-banner" role="status">
          <Icon name="history" />
          <span>Unsaved changes from {new Date(doc.recovered.at).toLocaleString()} were recovered, but the file changed since then.</span>
          <span className="spacer" />
          <Button size="sm" variant="primary" onClick={() => ws.restoreDraft(path)}>
            Restore them
          </Button>
          <Button size="sm" variant="ghost" onClick={() => ws.dropDraft(path)}>
            Discard
          </Button>
        </div>
      )}
      {doc.conflict && (
        <div className="studio-banner wb-banner is-warning" role="alert">
          <Icon name="alert" />
          <span>Changed on disk while you were editing (an agent may have written to it).</span>
          <span className="spacer" />
          <Button size="sm" onClick={() => ws.openDiff(path, 'disk')}>
            Compare
          </Button>
          <Button size="sm" onClick={() => void ws.reload(path)}>
            Reload
          </Button>
          <Button size="sm" variant="primary" onClick={() => void ws.save(path, { force: true })}>
            Overwrite
          </Button>
        </div>
      )}
      {doc.deleted && (
        <div className="studio-banner wb-banner is-warning" role="alert">
          <Icon name="alert" />
          <span>This file was deleted on disk.{doc.dirty ? ' Saving brings it back with your edits.' : ''}</span>
          <span className="spacer" />
          <Button size="sm" variant="primary" onClick={() => void ws.save(path, { force: true })}>
            Save it again
          </Button>
        </div>
      )}
      {doc.outside && (
        <div className="studio-banner wb-banner is-warning" role="alert">
          <Icon name="link" />
          <span>This file’s real path is outside the project (a link). Save there anyway?</span>
          <span className="spacer" />
          <Button size="sm" variant="primary" onClick={() => void ws.save(path, { outside: true })}>
            Allow saving there
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              doc.outside = false;
              doc.readOnly = 'Real path is outside the project: read-only.';
              ws.changed();
            }}
          >
            Keep read-only
          </Button>
        </div>
      )}
      {doc.saveError && (
        <div className="studio-banner wb-banner is-error" role="alert">
          <Icon name="alertCircle" />
          <span>Not saved: {doc.saveError}</span>
          <span className="spacer" />
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon="close"
            aria-label="Dismiss"
            onClick={() => {
              doc.saveError = '';
              ws.changed();
            }}
          />
        </div>
      )}
    </>
  );
}

function Crumbs({ ws, tab, doc }: { ws: Workspace; tab: Tab; doc: Doc | undefined }) {
  const key = tab.path as FileKey;
  // a Root card starts with the part; a file outside the card's parts shows its absolute path
  const crumbs = crumbsOf(ws.paths, key);
  const inside = !!ws.paths.mountOf(key);
  const isMd = doc?.kind === 'text' && !tab.diff && MARKDOWN_EXT.has(extOf(tab.path));
  const part = ws.partOf(key);
  const repoRel = part?.repoRel(key);
  const readOnly = doc ? ws.readOnlyOf(doc) : '';
  return (
    <nav className="wb-crumbs" aria-label="Path">
      {crumbs.map((c, i) => (
        <span key={i} className="wb-crumb">
          {i > 0 && <Icon name="chevronRight" />}
          <button type="button" className="truncate" onClick={() => ws.revealInTree(inside && c.key ? c.key : key)} title={c.key ? ws.relLabel(c.key) : c.label}>
            {c.label}
          </button>
        </span>
      ))}
      <span className="spacer" />
      {readOnly && doc?.kind === 'text' && <span className="wb-crumb-note truncate">{readOnly}</span>}
      {isMd && doc && (
        <SegmentedControl<MdView>
          size="sm"
          label="Markdown view"
          value={doc.md}
          onChange={(v) => ws.setMdView(key, v)}
          options={[
            { value: 'reader', label: 'Reader' },
            { value: 'split', label: 'Split' },
            { value: 'source', label: 'Source' },
          ]}
        />
      )}
      {!tab.diff && repoRel && part?.gitMark(key) && (
        <Button size="sm" variant="ghost" iconOnly icon="branch" aria-label="Compare with HEAD" title="Compare with HEAD" onClick={() => ws.openDiff(key, 'head')} />
      )}
    </nav>
  );
}

/** The reader beside the source: both scroll together, by position in the document. */
function SplitReader({ ws, group, doc }: { ws: Workspace; group: Group; doc: Doc }) {
  const reader = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const ed = ws.editors.get(group.id);
    const el = reader.current;
    if (!ed || !el) return;
    let from: 'editor' | 'reader' | null = null;
    let release = 0;
    const lock = (who: 'editor' | 'reader') => {
      from = who;
      window.clearTimeout(release);
      release = window.setTimeout(() => (from = null), 120);
    };
    const sub = ed.onDidScrollChange(() => {
      if (from === 'reader') return;
      lock('editor');
      const range = ed.getScrollHeight() - ed.getLayoutInfo().height;
      el.scrollTop = range > 0 ? (ed.getScrollTop() / range) * (el.scrollHeight - el.clientHeight) : 0;
    });
    const onScroll = () => {
      if (from === 'editor') return;
      lock('reader');
      const range = el.scrollHeight - el.clientHeight;
      ed.setScrollTop(range > 0 ? (el.scrollTop / range) * (ed.getScrollHeight() - ed.getLayoutInfo().height) : 0);
    };
    el.addEventListener('scroll', onScroll, { passive: true });
    return () => {
      window.clearTimeout(release);
      sub.dispose();
      el.removeEventListener('scroll', onScroll);
    };
  }, [ws, group.id, doc.path]);
  return <MarkdownReader ws={ws} doc={doc} scrollRef={reader} />;
}

function PaneBody({ ws, group, gi, tab, doc }: { ws: Workspace; group: Group; gi: number; tab: Tab; doc: Doc | undefined }) {
  // a proposal can be for a file that does not exist yet, so it needs no document
  if (tab.diff?.startsWith('ide:')) return <ProposalPane ws={ws} tab={tab} font={FONT} />;
  if (!doc || doc.status === 'loading') {
    return (
      <div className="wb-empty" role="status">
        <span className="spinner" aria-hidden="true" /> Opening…
      </div>
    );
  }
  if (doc.status === 'error') {
    return (
      <div className="wb-empty">
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{doc.error}</span>
        </div>
      </div>
    );
  }
  if (doc.kind === 'image') {
    return (
      <div className="wb-preview nowheel">
        <img src={rawUrlOf(doc.path)} alt={nameOf(doc.path)} />
        <p className="wb-preview-note tabular">{doc.size >= 1024 ? `${Math.round(doc.size / 1024)} KB` : `${doc.size} B`}</p>
      </div>
    );
  }
  if (doc.kind === 'media') {
    const audio = ['mp3', 'wav', 'ogg', 'flac'].includes(extOf(doc.path));
    return <div className="wb-preview">{audio ? <audio controls src={rawUrlOf(doc.path)} /> : <video controls src={rawUrlOf(doc.path)} />}</div>;
  }
  if (doc.kind !== 'text' || !doc.model) {
    return (
      <div className="wb-empty">
        <Icon name="file" className="wb-empty-icon" />
        <p>{doc.readOnly}</p>
        <Button size="sm" onClick={() => window.open(rawUrlOf(doc.path), '_blank', 'noopener')}>
          Download
        </Button>
      </div>
    );
  }
  if (tab.diff) return <DiffPane ws={ws} tab={tab} doc={doc} />;
  const md = MARKDOWN_EXT.has(extOf(doc.path)) ? doc.md : 'source';
  if (md === 'reader') return <MarkdownReader ws={ws} doc={doc} />;
  return (
    <div className={`wb-editor-host ${md === 'split' ? 'is-split' : ''}`}>
      <MonacoView ws={ws} group={group} gi={gi} doc={doc} tab={tab} />
      {md === 'split' && <SplitReader ws={ws} group={group} doc={doc} />}
    </div>
  );
}

function tabLabel(ws: Workspace, tab: Tab, group: Group) {
  if (tab.tool) return { name: toolMeta(tab.tool.kind, tab.tool.arg).title, dir: '' };
  // a Root card says which part a commit, a review, a history or a graph is of
  const part = ws.paths.multi ? (mountedPart(ws, tab.commit?.part ?? tab.view?.part)?.name ?? '') : '';
  if (tab.commit) return { name: tab.commit.label, dir: part };
  if (tab.view?.kind === 'review') return { name: `${nameOf(tab.view.path)} (${tab.view.agent})`, dir: part };
  if (tab.view?.kind === 'history') return { name: `${nameOf(tab.view.path)} (history)`, dir: part };
  if (tab.view?.kind === 'graph') return { name: 'Commit graph', dir: part };
  if (tab.view?.kind === 'pr') return { name: `#${tab.view.number} ${tab.view.title}`, dir: part };
  const name = nameOf(tab.path);
  const twin = group.tabs.some((t) => t !== tab && nameOf(t.path) === name && t.path !== tab.path);
  const suffix = tab.diff === 'head' ? ' (HEAD)' : tab.diff === 'index' ? ' (changes)' : tab.diff === 'staged' ? ' (staged)' : tab.diff === 'disk' ? ' (on disk)' : tab.diff?.startsWith('ide:') ? ' (proposed)' : '';
  // a Root card names the part, and the folder when two tabs share a name
  return { name: name + suffix, dir: tab.path ? tabDir(ws.paths, tab.path, twin) : '' };
}

/** What a review or history tab's tooltip names: the file as the card shows it ("admin/src/x.ts" in a Root card). */
function viewTitle(ws: Workspace, v: PartSpecialView): string {
  if (v.kind === 'history') return ws.relLabel(v.path);
  if (v.kind !== 'review') return '';
  // the hub reports a review's path inside its part
  const key = mountedPart(ws, v.part)?.keyOf(v.path);
  return key ? ws.relLabel(key) : v.path;
}

/** A tab of a part's own (a commit, the graph, a review, a pull request): shown while the card still mounts that part. */
function PartTab({ ws, partId, children }: { ws: Workspace; partId: string; children: (part: PartService) => ReactNode }) {
  const part = mountedPart(ws, partId);
  if (!part) {
    return (
      <div className="wb-view-empty" role="status">
        <p>This tab belongs to a part this card no longer shows.</p>
      </div>
    );
  }
  return <>{children(part)}</>;
}

const LOCKED_TIP = 'Locked: unlock it to close';

/** A console tab's close button: an agent of this editor stops, a terminal is killed, a board's agent only leaves the editor. */
const paneCloseLabel = (ws: Workspace, id: string) => (ws.isBoardPane(id) ? 'Close this tab' : isTerminalPane(id) ? 'Kill this terminal' : 'Close this agent');

/** The tab being dragged now: a target cannot read the drag's data before the drop. */
let dragging: { ws: Workspace; group: Group; id: string } | null = null;

function TabStrip({ ws, group, gi, onAddFile, onAi }: { ws: Workspace; group: Group; gi: number; onAddFile: (p: string) => void; onAi: () => void }) {
  const [menu, setMenu] = useState<{ x: number; y: number; tab: Tab } | null>(null);
  const ref = useRef<HTMLDivElement>(null);

  // the active tab in view: scrolled within the strip only (scrollIntoView would move the page on
  // a phone), and again when a strip that was hidden shows (a phone's Editor after Files)
  useLayoutEffect(() => {
    const strip = ref.current;
    if (!strip) return;
    const reveal = () => {
      const t = strip.querySelector<HTMLElement>('.wb-tab.is-active');
      if (!t || !strip.clientWidth) return;
      const s = strip.getBoundingClientRect();
      const r = t.getBoundingClientRect();
      // the tools at the strip's end cover its last part
      const tools = strip.querySelector<HTMLElement>('.wb-tabs-tools')?.getBoundingClientRect().width ?? 0;
      if (r.left < s.left) strip.scrollLeft -= s.left - r.left;
      else if (r.right > s.right - tools) strip.scrollLeft += Math.min(r.right - (s.right - tools), r.left - s.left);
    };
    reveal();
    const ro = new ResizeObserver(reveal);
    ro.observe(strip);
    return () => ro.disconnect();
  }, [group.active, group.tabs.length]);

  const workspaceId = useContext(PaneEnv)?.ctl.workspaceId ?? '';
  const choices = usePaneChoices(ws, workspaceId);
  const tab = group.tabs.find((t) => t.id === group.active) ?? null;

  // every kind of tab: VS Code's pin, and a lock that nothing closes
  const flagItems = (t: Tab): MenuEntry[] => [
    { label: t.pinned ? 'Unpin' : 'Pin', icon: 'pin', shortcut: 'Ctrl+K Shift+Enter', onSelect: () => ws.setPinned(gi, t.id, !t.pinned) },
    t.locked ? { label: 'Unlock', icon: 'unlock', onSelect: () => ws.setLocked(gi, t.id, false) } : { label: 'Lock', icon: 'lock', title: 'Nothing closes it until it is unlocked', onSelect: () => ws.setLocked(gi, t.id, true) },
  ];

  const closeItems = (t: Tab): MenuEntry[] => [
    { label: 'Close', icon: 'close', shortcut: 'Alt+W', title: t.locked ? LOCKED_TIP : undefined, onSelect: () => ws.closeTab(gi, t.id) },
    { label: 'Close Others', title: 'Pinned and locked tabs stay', onSelect: () => ws.closeOthers(gi, t.id) },
    { label: 'Close to the Right', title: 'Pinned and locked tabs stay', onSelect: () => ws.closeRight(gi, t.id) },
    { label: 'Close All', title: 'Pinned and locked tabs stay', onSelect: () => ws.closeAll(gi) },
  ];

  // a console, a tool, a commit or a view has one place: alone in its group, it has nowhere to split off from
  const alone = (t: Tab) => !t.path && group.tabs.length === 1;
  const splitItems = (t: Tab): MenuEntry[] => [
    { label: 'Split Right', icon: 'splitRight', shortcut: 'Ctrl+\\', disabled: alone(t), title: alone(t) ? 'The only tab of its group' : undefined, onSelect: () => ws.splitTab(gi, t.id, 'right') },
    { label: 'Split Down', icon: 'splitDown', shortcut: 'Ctrl+K Ctrl+\\', disabled: alone(t), title: alone(t) ? 'The only tab of its group' : undefined, onSelect: () => ws.splitTab(gi, t.id, 'bottom') },
  ];

  const paneItems = (t: Tab): MenuEntry[] => {
    const id = t.pane!;
    const locked = t.locked ? 'Locked: unlock it to move it' : undefined;
    // a terminal goes back to the panel; a board's console is only shown here; the editor's own agents have the side bar
    const move: MenuEntry = isTerminalPane(id)
      ? { label: 'Move to Panel', icon: 'terminal', disabled: !!t.locked, title: locked ?? 'Its shell keeps running', onSelect: () => ws.movePaneToPanel(id) }
      : ws.isBoardPane(id)
        ? { label: 'Close Tab', icon: 'close', disabled: !!t.locked, title: t.locked ? LOCKED_TIP : 'The agent keeps running on its board', onSelect: () => ws.closeTab(gi, t.id) }
        : { label: 'Move to Side Bar', icon: 'external', disabled: !!t.locked, title: locked, onSelect: () => ws.placePane(id, 'side') };
    const close: MenuEntry[] = ws.isBoardPane(id)
      ? []
      : [{ label: isTerminalPane(id) ? 'Kill Terminal' : 'Close', icon: 'close', title: t.locked ? LOCKED_TIP : undefined, onSelect: () => ws.closeTab(gi, t.id) }];
    return [move, ...splitItems(t), 'sep', ...flagItems(t), ...(close.length ? (['sep', ...close] as MenuEntry[]) : [])];
  };

  const specialItems = (t: Tab): MenuEntry[] => [
    ...closeItems(t),
    'sep',
    ...flagItems(t),
    'sep',
    ...splitItems(t),
    ...(t.commit ? ([{ label: 'Copy Commit Hash', icon: 'copy', onSelect: () => void navigator.clipboard?.writeText(t.commit!.hash).then(() => toast.success('Hash copied.')) }] as MenuEntry[]) : []),
  ];

  const items = (t: Tab): MenuEntry[] => t.pane ? paneItems(t) : !t.path ? specialItems(t) : [
    ...closeItems(t),
    'sep',
    ...flagItems(t),
    'sep',
    ...splitItems(t),
    { label: 'Compare with HEAD', icon: 'branch', disabled: !(t.path && ws.partOf(t.path)?.gitMark(t.path)), onSelect: () => t.path && ws.openDiff(t.path, 'head', gi) },
    { label: 'Local History', icon: 'history', onSelect: () => ws.openView({ kind: 'history', path: t.path }) },
    { label: 'Reveal in Explorer', icon: 'folderOpen', onSelect: () => t.path && ws.revealInTree(t.path) },
    { label: 'Copy Path', icon: 'link', onSelect: () => t.path && void navigator.clipboard?.writeText(splitKey(t.path).abs).then(() => toast.success('Path copied.')) },
    { label: 'Copy Relative Path', icon: 'link', onSelect: () => t.path && void navigator.clipboard?.writeText(ws.relLabel(t.path)).then(() => toast.success('Path copied.')) },
    ...(ws.actions.addToBoard ? ([{ label: 'Add to Board', icon: 'plus', onSelect: () => onAddFile(t.path) }] as MenuEntry[]) : []),
  ];

  return (
    // a long press is the tab's right-click on any touch screen (lib/longpress.ts)
    <div className="wb-tabs" role="tablist" aria-label="Open files" ref={ref} data-longpress="">
      {group.tabs.map((t) => {
        const d = t.path ? ws.docs.at(t.path) : undefined;
        const active = group.active === t.id;
        const { name, dir } = tabLabel(ws, t, group);
        const mark = !t.diff && t.path ? ws.partOf(t.path)?.gitMark(t.path) : undefined;
        return (
          <div
            key={t.id}
            role="tab"
            tabIndex={active ? 0 : -1}
            aria-selected={active}
            className={`wb-tab ${active ? 'is-active' : ''} ${t.preview ? 'is-preview' : ''} ${t.pinned ? 'is-pinned' : ''} ${t.locked ? 'is-locked' : ''} ${mark ? `git-${mark.tone}` : ''} ${d?.dirty && !t.diff ? 'is-dirty' : ''}`}
            title={t.pane ? undefined : t.tool ? toolMeta(t.tool.kind, t.tool.arg).tip : t.commit ? t.commit.label : t.view?.kind === 'graph' ? 'Commit graph' : t.view?.kind === 'pr' ? t.view.title : t.view ? viewTitle(ws, t.view) : t.path ? ws.label(t.path) : ''}
            draggable
            onDragStart={(e) => {
              e.dataTransfer.setData('application/x-nextboard-tab', JSON.stringify({ gi, id: t.id }));
              e.dataTransfer.effectAllowed = 'move';
              dragging = { ws, group, id: t.id };
            }}
            onDragEnd={() => {
              dragging = null;
            }}
            onClick={() => ws.activate(gi, t.id)}
            onDoubleClick={() => ws.pin(gi, t.id)}
            onAuxClick={(e) => {
              if (e.button === 1) {
                e.preventDefault();
                ws.closeTab(gi, t.id);
              }
            }}
            onContextMenu={(e) => {
              e.preventDefault();
              setMenu({ x: e.clientX, y: e.clientY, tab: t });
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                ws.activate(gi, t.id);
              }
            }}
          >
            {t.pane ? (
              <PaneTitle ws={ws} id={t.pane} />
            ) : (
              <>
                {t.tool ? <Icon name={toolMeta(t.tool.kind, t.tool.arg).icon} className="wb-icon" /> : t.commit || t.view?.kind === 'history' ? <Icon name="history" className="wb-icon" /> : t.view?.kind === 'graph' || t.view?.kind === 'pr' ? <Icon name="branch" className="wb-icon" /> : t.view ? <Icon name="checklist" className="wb-icon" /> : t.diff ? <Icon name="branch" className="wb-icon" /> : <FileIcon name={nameOf(t.path)} />}
                <span className="wb-tab-name truncate">{name}</span>
                {dir && <span className="wb-tab-dir truncate">{dir}</span>}
              </>
            )}
            {t.locked ? (
              // no close button: unlocking is in the tab's menu, so a stray click closes nothing
              <span className="wb-tab-lock" role="img" aria-label={t.pinned ? `Pinned. ${LOCKED_TIP}` : LOCKED_TIP} title={LOCKED_TIP}>
                {t.pinned && <Icon name="pin" />}
                {d?.dirty && !t.diff && <span className="wb-tab-dot" aria-hidden="true" />}
                <Icon name="lock" />
              </span>
            ) : t.pinned ? (
              <button
                type="button"
                className="wb-tab-close wb-tab-pin"
                aria-label={`Unpin ${name}${d?.dirty ? ' (unsaved)' : ''}`}
                title={d?.dirty ? 'Unsaved changes · Unpin' : 'Pinned · Unpin'}
                onClick={(e) => {
                  e.stopPropagation();
                  ws.setPinned(gi, t.id, false);
                }}
              >
                <span className="wb-tab-dot" aria-hidden="true" />
                <Icon name="pin" />
              </button>
            ) : (
              <button
                type="button"
                className="wb-tab-close"
                aria-label={t.pane ? paneCloseLabel(ws, t.pane) : `Close ${name}${d?.dirty ? ' (unsaved)' : ''}`}
                title={d?.dirty ? 'Unsaved changes' : 'Close'}
                onClick={(e) => {
                  e.stopPropagation();
                  ws.closeTab(gi, t.id);
                }}
              >
                <span className="wb-tab-dot" aria-hidden="true" />
                <Icon name="close" />
              </button>
            )}
          </div>
        );
      })}
      <span className="spacer" />
      <div className="wb-tabs-tools">
        <Menu label="Open an agent in a new tab" icon="bot" items={agentMenu(ws, workspaceId, choices, 'tab')} />
        <Button size="sm" variant="ghost" iconOnly icon="sparkle" aria-label="Edit with AI (Ctrl+I)" title="Edit with AI · Ctrl+I" disabled={!group.active} onClick={onAi} />
        <Menu
          label="Split editor"
          icon="splitRight"
          items={[
            { label: 'Split Right', icon: 'splitRight', shortcut: 'Ctrl+\\', disabled: !tab || alone(tab), onSelect: () => tab && ws.splitTab(gi, tab.id, 'right') },
            { label: 'Split Down', icon: 'splitDown', shortcut: 'Ctrl+K Ctrl+\\', disabled: !tab || alone(tab), onSelect: () => tab && ws.splitTab(gi, tab.id, 'bottom') },
          ]}
        />
      </div>
      {menu && <ContextMenu x={menu.x} y={menu.y} label="Tab actions" items={items(menu.tab)} onClose={() => setMenu(null)} />}
    </div>
  );
}

// ---- inline AI edit ------------------------------------------------------------------

function ReviewDiff({ original, modified, language }: { original: string; modified: string; language: string }) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!host.current) return;
    const a = monaco.editor.createModel(original, language);
    const b = monaco.editor.createModel(modified, language);
    const diff = monaco.editor.createDiffEditor(host.current, {
      theme: currentTheme(),
      automaticLayout: true,
      fontFamily: FONT,
      readOnly: true,
      renderSideBySide: false,
      minimap: { enabled: false },
      scrollBeyondLastLine: false,
      fixedOverflowWidgets: false,
    });
    diff.setModel({ original: a, modified: b });
    return () => {
      diff.setModel(null);
      diff.dispose();
      a.dispose();
      b.dispose();
    };
  }, [original, modified, language]);
  return <div className="wb-review-diff" ref={host} data-keep-escape="" />;
}

function replacementOf(result: unknown): string | null {
  const r = result as { structured?: { replacement?: unknown }; text?: unknown } | null;
  if (typeof r?.structured?.replacement === 'string') return r.structured.replacement;
  if (typeof r?.text !== 'string') return null;
  const m = /\{[\s\S]*\}/.exec(r.text);
  if (!m) return null;
  try {
    const v = (JSON.parse(m[0]) as { replacement?: unknown }).replacement;
    return typeof v === 'string' ? v : null;
  } catch {
    return null;
  }
}

function AiEditBar({ ws, editor, doc, onClose }: { ws: Workspace; editor: Editor; doc: Doc; onClose: () => void }) {
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [proposal, setProposal] = useState<{ original: string; replacement: string } | null>(null);
  const deco = useRef<monaco.editor.IEditorDecorationsCollection | null>(null);
  const cancelled = useRef(false);
  const model = doc.model;
  const sel = editor.getSelection();
  const lines = sel && !sel.isEmpty() ? sel.endLineNumber - sel.startLineNumber + (sel.endColumn > 1 ? 1 : 0) : 0;

  useEffect(
    () => () => {
      cancelled.current = true;
      deco.current?.clear();
    },
    [],
  );

  const submit = async () => {
    const s = editor.getSelection();
    if (!model || !s || !text.trim()) return;
    setBusy(true);
    const before = model.getValueInRange(new monaco.Range(Math.max(1, s.startLineNumber - 200), 1, s.startLineNumber, s.startColumn));
    const endLine = Math.min(model.getLineCount(), s.endLineNumber + 100);
    const after = model.getValueInRange(new monaco.Range(s.endLineNumber, s.endColumn, endLine, model.getLineMaxColumn(endLine)));
    const selection = model.getValueInRange(s);
    // follows the range if the file is edited while the agent works
    deco.current = editor.createDecorationsCollection([{ range: s, options: { className: 'wb-ai-range', stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges } }]);
    try {
      // asked of the file's part: its folder is where the agent reads
      const part = ws.partOf(doc.path);
      const rel = part?.relOf(doc.path);
      if (!part || !rel) throw new Error('This file is in no part of the project.');
      const j = await filesApi.aiEdit(part.ref, { path: rel, language: model.getLanguageId(), instruction: text.trim(), before, selection, after });
      const done = await waitJob(j.id, 4 * 60_000);
      if (cancelled.current) return;
      if (done.state !== 'done') throw new Error(done.error || 'The agent did not finish.');
      const replacement = replacementOf(done.result);
      if (replacement === null) throw new Error('The agent did not return any code.');
      setProposal({ original: selection, replacement });
    } catch (err) {
      if (!cancelled.current) toast.error(`Edit with AI: ${errorMessage(err)}`);
      deco.current?.clear();
    } finally {
      setBusy(false);
    }
  };

  const accept = () => {
    const range = deco.current?.getRange(0);
    if (range && proposal) {
      editor.pushUndoStop();
      editor.executeEdits('ai-edit', [{ range, text: proposal.replacement, forceMoveMarkers: true }]);
      editor.pushUndoStop();
      editor.focus();
    }
    onClose();
  };

  return (
    <>
      <form
        className="wb-ai-bar"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <Icon name="sparkle" className="wb-icon" />
        <input
          // eslint-disable-next-line jsx-a11y/no-autofocus
          autoFocus
          aria-label="What should change"
          placeholder={lines ? `Change ${lines} selected ${lines === 1 ? 'line' : 'lines'}: describe the edit` : 'Describe the code to write at the cursor'}
          value={text}
          disabled={busy}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              e.preventDefault();
              e.stopPropagation();
              onClose();
              editor.focus();
            }
          }}
        />
        <Button type="submit" size="sm" variant="primary" loading={busy} disabled={!text.trim() || !!ws.readOnlyOf(doc)}>
          {busy ? 'Working…' : 'Generate'}
        </Button>
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close" onClick={onClose} />
      </form>
      {proposal && model && (
        <Modal
          title="Review the AI edit"
          icon="sparkle"
          size="lg"
          dismissable={false}
          onClose={onClose}
          footer={
            <>
              <Button onClick={onClose}>Reject</Button>
              <Button
                onClick={() => {
                  setProposal(null);
                  void submit();
                }}
              >
                Try again
              </Button>
              <Button variant="primary" icon="check" onClick={accept}>
                Accept
              </Button>
            </>
          }
        >
          <ReviewDiff original={proposal.original} modified={proposal.replacement} language={model.getLanguageId()} />
        </Modal>
      )}
    </>
  );
}

// ---- groups --------------------------------------------------------------------------

/**
 * Tool tabs a group keeps mounted while another tab shows, for what they hold that is theirs (a
 * draft, filters, a page). Live views (Mission Map polls the hub, Loops) and the vault (its state
 * is the store's) are made again instead when they show.
 */
const KEEP_TOOLS = 3;
const KEPT_KINDS = new Set(['chats', 'memory', 'documents', 'diagrams', 'markdown', 'skills', 'browser']);

/** Where a drop lands in a group: near an edge it splits there, else it joins the group. */
type DropZone = Edge | 'center';

const EDGE_SHARE = 0.25;
const ZONE_LABEL: Record<DropZone, string> = { left: 'Drop to split left', right: 'Drop to split right', top: 'Drop to split up', bottom: 'Drop to split down', center: 'Drop to move here' };

/** The zone under the pointer, in the group's body (under its tab strip): the nearest edge within a quarter of it. */
function zoneAt(box: { left: number; top: number; width: number; height: number }, x: number, y: number): DropZone {
  const fx = (x - box.left) / Math.max(box.width, 1);
  const fy = (y - box.top) / Math.max(box.height, 1);
  const near: [Edge, number][] = [
    ['left', fx],
    ['right', 1 - fx],
    ['top', fy],
    ['bottom', 1 - fy],
  ];
  near.sort((a, b) => a[1] - b[1]);
  return near[0][1] < EDGE_SHARE ? near[0][0] : 'center';
}

function GroupView({ ws, group, gi, onAddFile, aiOpen, setAiOpen }: { ws: Workspace; group: Group; gi: number; onAddFile: (p: string) => void; aiOpen: boolean; setAiOpen: (open: boolean) => void }) {
  const tab = group.tabs.find((t) => t.id === group.active) ?? null;
  const doc = tab?.path ? ws.docs.at(tab.path) : undefined;
  const editor = ws.editors.get(group.id);
  const [drop, setDrop] = useState<{ zone: DropZone; top: number; file: boolean } | null>(null);
  const sectionRef = useRef<HTMLElement>(null);
  // tool tabs shown lately stay mounted, hidden, so that switching tabs keeps what they hold (a
  // draft, filters, a browser's page): the active one and the last few, most recent first
  const keptTools = useRef<string[]>([]);
  if (tab?.tool) keptTools.current = [tab.id, ...keptTools.current.filter((id) => id !== tab.id)];
  keptTools.current = keptTools.current.filter((id) => group.tabs.some((t) => t.id === id && t.tool && (t === tab || KEPT_KINDS.has(t.tool.kind)))).slice(0, KEEP_TOOLS);
  const tools = keptTools.current.map((id) => group.tabs.find((t) => t.id === id)!);
  // a drag given up (Esc) or dropped elsewhere leaves no zone behind
  useEffect(() => {
    if (!drop) return;
    const clear = () => setDrop(null);
    window.addEventListener('dragend', clear, true);
    window.addEventListener('drop', clear, true);
    return () => {
      window.removeEventListener('dragend', clear, true);
      window.removeEventListener('drop', clear, true);
    };
  }, [drop]);

  /** What a drag over this group would do here, or null for nothing (a tab alone in this group, a tab of another card). */
  const zoneOf = (e: DragEvent): { zone: DropZone; top: number; file: boolean } | null => {
    const types = [...e.dataTransfer.types];
    const isTab = types.includes('application/x-nextboard-tab');
    const file = !isTab && types.includes('application/x-nextboard-path');
    if (!isTab && !file) return null;
    if (isTab && dragging?.ws !== ws) return null;
    const own = isTab && dragging?.group === group;
    if (own && group.tabs.length === 1) return null;
    const el = sectionRef.current;
    if (!el) return null;
    const box = el.getBoundingClientRect();
    const strip = el.querySelector('.wb-tabs')?.getBoundingClientRect();
    const top = strip ? strip.bottom - box.top : 0;
    const body = { left: box.left, top: box.top + top, width: box.width, height: box.height - top };
    // only the active group shows on a phone or a narrow card: no edges to split at; over the strip it joins the strip
    const narrow = !!el.closest('.wb.is-narrow, .wb.is-one-group');
    let zone: DropZone = narrow || e.clientY < body.top ? 'center' : zoneAt(body, e.clientX, e.clientY);
    if (zone !== 'center' && !ws.canSplit(gi, zone)) zone = 'center';
    if (zone === 'center' && own) return null;
    return { zone, top, file };
  };

  const onDrop = (e: DragEvent) => {
    const at = zoneOf(e);
    setDrop(null);
    const moved = e.dataTransfer.getData('application/x-nextboard-tab');
    // a tree row's key (Explorer.tsx); a folder has items, a file not
    const path = asFileKey(e.dataTransfer.getData('application/x-nextboard-path'));
    if (moved) {
      e.preventDefault();
      const from = dragging;
      dragging = null;
      if (!at || !from || from.ws !== ws) return;
      const fromGi = ws.groups.indexOf(from.group);
      if (at.zone === 'center') ws.moveTabToGroup(fromGi, from.id, gi);
      else ws.moveTabToEdge(fromGi, from.id, gi, at.zone);
    } else if (path && ws.items.get(path) === undefined) {
      e.preventDefault();
      if (at && at.zone !== 'center') ws.openAtEdge(path, gi, at.zone);
      else ws.open(path, { pin: true, group: gi });
    }
  };

  return (
    <section
      ref={sectionRef}
      className={`wb-group ${ws.activeGroup === gi && ws.groups.length > 1 ? 'is-active' : ''} ${drop ? 'is-drop' : ''}`}
      aria-label={`Editor group ${gi + 1}`}
      onMouseDownCapture={() => ws.focusGroup(gi)}
      onDragOver={(e) => {
        const at = zoneOf(e);
        if (!at) {
          if (drop) setDrop(null);
          return;
        }
        e.preventDefault();
        e.dataTransfer.dropEffect = at.file ? 'copy' : 'move';
        if (drop?.zone !== at.zone || drop.top !== at.top) setDrop(at);
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDrop(null);
      }}
      onDrop={onDrop}
    >
      <TabStrip ws={ws} group={group} gi={gi} onAddFile={onAddFile} onAi={() => setAiOpen(true)} />
      {tab?.pane ? (
        <div className="wb-pane-tab">
          <AgentPane key={tab.pane} ws={ws} id={tab.pane} />
        </div>
      ) : tab?.tool ? null : tab?.commit ? (
        <div className="wb-pane-tab">
          <PartTab ws={ws} partId={tab.commit.part}>{(part) => <CommitView key={tab.id} ws={ws} part={part} commit={tab.commit!} />}</PartTab>
        </div>
      ) : tab?.view ? (
        <div className="wb-pane-tab">
          {tab.view.kind === 'history' ? (
            <LocalHistoryView key={tab.id} ws={ws} path={tab.view.path} />
          ) : (
            <PartTab ws={ws} partId={tab.view.part}>
              {(part) => {
                const v = tab.view!;
                return v.kind === 'review' ? <ReviewView key={tab.id} ws={ws} part={part} tab={tab} session={v.session} path={v.path} agent={v.agent} /> : v.kind === 'graph' ? <CommitGraphView key={tab.id} ws={ws} part={part} /> : v.kind === 'pr' ? <PullRequestView key={tab.id} ws={ws} part={part} number={v.number} repo={v.repo} /> : null;
              }}
            </PartTab>
          )}
        </div>
      ) : tab ? (
        <>
          <Crumbs ws={ws} tab={tab} doc={doc} />
          {doc && !tab.diff && <Banners ws={ws} doc={doc} />}
          {aiOpen && ws.activeGroup === gi && editor && doc?.model && <AiEditBar ws={ws} editor={editor} doc={doc} onClose={() => setAiOpen(false)} />}
          <PaneBody ws={ws} group={group} gi={gi} tab={tab} doc={doc} />
        </>
      ) : (
        <div className="wb-watermark">
          <Icon name="code" className="wb-empty-icon" />
          <dl>
            <div>
              <dt>Go to file</dt>
              <dd>
                <kbd>Ctrl</kbd> <kbd>P</kbd>
              </dd>
            </div>
            <div>
              <dt>Commands</dt>
              <dd>
                <kbd>Ctrl</kbd> <kbd>Shift</kbd> <kbd>P</kbd>
              </dd>
            </div>
            <div>
              <dt>Search in files</dt>
              <dd>
                <kbd>Ctrl</kbd> <kbd>Shift</kbd> <kbd>F</kbd>
              </dd>
            </div>
            <div>
              <dt>Terminal</dt>
              <dd>
                <kbd>Ctrl</kbd> <kbd>`</kbd>
              </dd>
            </div>
          </dl>
        </div>
      )}
      {tools.map((t) => (
        <div key={t.id} className="wb-pane-tab wb-tool-tab" hidden={t !== tab}>
          <ToolTab ws={ws} tab={t} />
        </div>
      ))}
      {drop && (
        // the half a split takes, or the whole group the tab joins
        <div className="wb-drop-area" style={{ top: drop.top }} aria-hidden="true">
          <div className={`wb-drop-zone is-${drop.zone}`}>
            <span>{drop.zone === 'center' && drop.file ? 'Drop to open here' : ZONE_LABEL[drop.zone]}</span>
          </div>
        </div>
      )}
    </section>
  );
}

const pct = (v: number) => `${+(v * 100).toFixed(4)}%`;

/** A group's place: the share it has, less the 1 px of the sash on its left or top. */
function cellStyle(c: GridBox): CSSProperties {
  const left = c.x > 1e-6;
  const top = c.y > 1e-6;
  return {
    left: left ? `calc(${pct(c.x)} + 1px)` : 0,
    top: top ? `calc(${pct(c.y)} + 1px)` : 0,
    width: left ? `calc(${pct(c.w)} - 1px)` : pct(c.w),
    height: top ? `calc(${pct(c.h)} - 1px)` : pct(c.h),
  };
}

/** A split's sash: 1 px between its two sides, across the split's whole box. */
function sashStyle(s: GridPlan['sashes'][number]): CSSProperties {
  return s.dir === 'row' ? { left: pct(s.x + s.w * s.at), top: pct(s.y), height: pct(s.h) } : { top: pct(s.y + s.h * s.at), left: pct(s.x), width: pct(s.w) };
}

/**
 * Ctrl+K Ctrl+\ splits down, as in VS Code. The card's Ctrl+\ (split right) must not take its
 * second key; in the editor, Monaco must still see it, to end the chord Ctrl+K began there.
 */
function useSplitDownChord(ws: Workspace, area: RefObject<HTMLDivElement | null>) {
  useEffect(() => {
    let armed = 0;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Control' || e.key === 'Shift' || e.key === 'Alt' || e.key === 'Meta') return;
      const t = e.target as HTMLElement | null;
      const root = area.current?.closest('.wb');
      // a terminal's keys are its program's (Ctrl+K kills a line in a shell)
      if (!t || !root?.contains(t) || t.closest('.xterm')) {
        armed = 0;
        return;
      }
      const mod = e.ctrlKey || e.metaKey;
      if (mod && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'k') {
        armed = Date.now();
        return;
      }
      const was = armed;
      armed = 0;
      if (!was || Date.now() - was > 5000 || !mod || e.shiftKey || e.altKey || e.key !== '\\') return;
      e.preventDefault();
      ws.splitDown();
      if (t.closest('.monaco-editor')) {
        Object.defineProperty(e, 'key', { value: 'Unidentified' });
        // ending the chord, Monaco gives the keyboard back to the editor it began in (which makes its
        // group the active one again): the new group takes both back once the key is done
        const g = ws.group;
        window.setTimeout(() => {
          const gi = ws.groups.indexOf(g);
          if (gi < 0) return;
          ws.focusGroup(gi);
          ws.editors.get(g.id)?.focus();
        }, 0);
      } else {
        e.stopImmediatePropagation();
      }
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [ws, area]);
}

export function EditorArea({ ws, onAddFile, aiOpen, setAiOpen }: { ws: Workspace; onAddFile: (p: string) => void; aiOpen: boolean; setAiOpen: (open: boolean) => void }) {
  useWs(ws);
  const area = useRef<HTMLDivElement>(null);
  useSplitDownChord(ws, area);
  const layout = plan(ws.grid);
  // in the order they were made, wherever they lie: moving a group in the page would reload what it shows (a browser tab)
  const slots = [...ws.groups].sort((a, b) => Number(a.id.slice(1)) - Number(b.id.slice(1)));

  const startSash = (s: GridPlan['sashes'][number], e: ReactPointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const box = area.current?.getBoundingClientRect();
    if (!box) return;
    // a page in a tab would take the pointer while it passes over it
    document.body.classList.add('wb-sash-dragging');
    const move = (ev: PointerEvent) => {
      const at = s.dir === 'row' ? ((ev.clientX - box.left) / box.width - s.x) / s.w : ((ev.clientY - box.top) / box.height - s.y) / s.h;
      ws.resizeSplit(s.path, at);
    };
    const up = () => {
      document.body.classList.remove('wb-sash-dragging');
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  return (
    <div className="wb-editors wb-grid" ref={area}>
      {slots.map((g) => {
        const gi = ws.groups.indexOf(g);
        const cell = layout.cells.find((c) => c.id === g.id) ?? { x: 0, y: 0, w: 1, h: 1 };
        return (
          <div key={g.id} className={`wb-group-slot ${ws.activeGroup === gi ? 'is-current' : ''}`} style={cellStyle(cell)}>
            <GroupView ws={ws} group={g} gi={gi} onAddFile={onAddFile} aiOpen={aiOpen} setAiOpen={setAiOpen} />
          </div>
        );
      })}
      {layout.sashes.map((s) => (
        <div
          key={s.path || 'root'}
          className={`wb-sash wb-grid-sash ${s.dir === 'row' ? 'wb-sash-v' : 'wb-sash-h'}`}
          style={sashStyle(s)}
          role="separator"
          aria-orientation={s.dir === 'row' ? 'vertical' : 'horizontal'}
          aria-label={s.dir === 'row' ? 'Resize the groups side by side' : 'Resize the groups above and below'}
          aria-valuenow={Math.round(s.at * 100)}
          aria-valuemin={10}
          aria-valuemax={90}
          title="Drag to resize · double-click to even out"
          onPointerDown={(e) => startSash(s, e)}
          onDoubleClick={() => ws.resizeSplit(s.path, 0.5)}
        />
      ))}
    </div>
  );
}
