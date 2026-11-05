// Editor groups of the Code workbench: tab strips (preview tabs in italics, dirty dots),
// breadcrumbs, one Monaco editor per group, diffs against HEAD or the disk, Markdown as
// Reader/Split/Source, image and media preview, and the bars that ask what to do when a
// file changed on disk. Inline AI edits (Ctrl+I) end in a diff to accept or reject.

import { useContext, useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type DragEvent, type PointerEvent as ReactPointerEvent, type RefObject } from 'react';
import { api, errorMessage, fsRawUrl } from '../../api';
import { clipboardFiles } from '../../board/files';
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
import { useWs } from './useWs';
import { dirOf, extOf, MARKDOWN_EXT, nameOf, type Doc, type Group, type MdView, type Tab, type Workspace } from './workspace';

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
  const readOnly = !!doc.readOnly;
  useDebugEditor(edRef, ws, doc.path, doc.model);

  // a layout effect, so that the editor exists when the model effect below runs
  useLayoutEffect(() => {
    if (!host.current) return;
    const ed = monaco.editor.create(host.current, { ...editorOptions(ws, readOnly), model: null });
    edRef.current = ed;
    ws.editors.set(group.id, ed);
    ed.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => void ws.save(pathRef.current));
    ed.addCommand(monaco.KeyMod.Alt | monaco.KeyCode.Backslash, () => void ed.getAction('editor.action.inlineSuggest.trigger')?.run());
    ed.addCommand(monaco.KeyMod.Alt | monaco.KeyCode.KeyK, () => ws.actions.mention?.());
    ed.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.KeyK, () => toggleBookmark(ws, pathRef.current, ed.getPosition()?.lineNumber ?? 1));
    ed.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.KeyL, () => jumpBookmark(ws, pathRef.current, ed.getPosition()?.lineNumber ?? 1, 1));
    ed.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.KeyJ, () => jumpBookmark(ws, pathRef.current, ed.getPosition()?.lineNumber ?? 1, -1));
    // the key the menu shows: on Linux Monaco only binds Ctrl+Shift+I to it
    ed.addCommand(monaco.KeyMod.Shift | monaco.KeyMod.Alt | monaco.KeyCode.KeyF, () => void ed.getAction('editor.action.formatDocument')?.run());
    const subs = [
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
    return () => {
      el.removeEventListener('paste', onPaste, true);
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

  // gutter marks against HEAD, refreshed when git or the saved file changes
  const repoRel = ws.repoRel(doc.path);
  const mark = ws.gitMark(doc.path);
  const tracked = !!repoRel && mark && mark.letter !== 'U';
  useEffect(() => {
    const ed = edRef.current;
    if (!ed || !doc.model) return;
    const coll = ed.createDecorationsCollection();
    if (!tracked || !repoRel) return () => coll.clear();
    let stop = false;
    gitApi
      .diff(ws.gitTarget, repoRel)
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
  }, [ws, ws.gitVersion, doc.model, doc.mtime, repoRel, tracked]);

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
    { label: 'Add to Board', icon: 'plus', onSelect: () => ws.actions.addToBoard?.(doc.path) },
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
  const dir = dirOf(doc.path);
  const resolve = (src: string) => {
    if (/^[a-z]+:/i.test(src) || src.startsWith('/')) return null;
    const abs = new URL(src, `file://${dir}/`).pathname;
    return ws.inside(abs) ? fsRawUrl(ws.mid, decodeURI(abs)) : null;
  };
  const onLink = (href: string) => {
    if (/^[a-z]+:/i.test(href)) return false;
    const [target] = href.split('#');
    if (!target) {
      // same page: only this reader may scroll
      ref.current?.querySelector(`[id="${CSS.escape(href.slice(1))}"]`)?.scrollIntoView({ block: 'start' });
      return true;
    }
    const abs = decodeURI(new URL(target, `file://${dir}/`).pathname);
    if (!MARKDOWN_EXT.has(extOf(abs))) return false;
    if (!ws.inside(abs)) {
      toast.error('That link points outside the project.');
      return true;
    }
    ws.open(abs, { pin: true });
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
  const rel = ws.repoRel(doc.path);
  const file = ws.git?.files?.find((f) => f.path === rel);
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
      const f = await gitApi.index(ws.gitTarget, path);
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
      readOnly: !!doc.readOnly,
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
    diff.getModifiedEditor().addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => void ws.save(doc.path));
    const commit = tab.diff?.startsWith('commit:') ? tab.diff.slice('commit:'.length) : null;
    const load = async (ref = 'HEAD'): Promise<string> => {
      if (tab.diff === 'disk') {
        const r = await api.fsRead(ws.mid, doc.path, 10 << 20);
        return r.content.startsWith('﻿') ? r.content.slice(1) : r.content;
      }
      const rel = ws.repoRel(doc.path);
      if (!rel) throw new Error('This file is not in the selected Git repository.');
      if (tab.diff === 'index') {
        const f = await gitApi.index(ws.gitTarget, rel);
        if (f.binary) throw new Error('Binary file: nothing to compare.');
        if (!f.exists) setNote('New file: not in Git yet.');
        return f.text;
      }
      const f = await gitApi.show(ws.gitTarget, rel, ref);
      if (f.binary) throw new Error('Binary file: nothing to compare.');
      if (!f.exists && !commit) setNote('New file: not in HEAD yet.');
      return f.content;
    };
    const language = languageFor(doc.path) ?? doc.model.getLanguageId();
    if (commit || tab.diff === 'staged') diff.updateOptions({ readOnly: true });
    const rel0 = ws.repoRel(doc.path);
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
  }, [ws, doc.model, tab.diff, doc.path]);

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
    if (!rel || !current || base === undefined || after === undefined) return;
    setBusy(true);
    try {
      const text = stage ? baseWithChange(base, after, current) : currentWithoutChange(base, after, current);
      await gitApi.stage(ws.gitTarget, [rel], text);
      if (stage) baseRef.current?.setValue(text);
      else stagedRef.current?.setValue(text);
      void ws.refreshGit();
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
  const rel = ws.rel(tab.path);
  const parts = rel.split('/');
  const isMd = doc?.kind === 'text' && !tab.diff && MARKDOWN_EXT.has(extOf(tab.path));
  const repoRel = ws.repoRel(tab.path);
  return (
    <nav className="wb-crumbs" aria-label="Path">
      {parts.map((p, i) => (
        <span key={i} className="wb-crumb">
          {i > 0 && <Icon name="chevronRight" />}
          <button type="button" className="truncate" onClick={() => ws.revealInTree(ws.inside(tab.path) ? ws.abs(parts.slice(0, i + 1).join('/')) : tab.path)} title={parts.slice(0, i + 1).join('/')}>
            {p}
          </button>
        </span>
      ))}
      <span className="spacer" />
      {doc?.readOnly && doc.kind === 'text' && <span className="wb-crumb-note truncate">{doc.readOnly}</span>}
      {isMd && doc && (
        <SegmentedControl<MdView>
          size="sm"
          label="Markdown view"
          value={doc.md}
          onChange={(v) => ws.setMdView(tab.path, v)}
          options={[
            { value: 'reader', label: 'Reader' },
            { value: 'split', label: 'Split' },
            { value: 'source', label: 'Source' },
          ]}
        />
      )}
      {!tab.diff && repoRel && ws.gitMark(tab.path) && (
        <Button size="sm" variant="ghost" iconOnly icon="branch" aria-label="Compare with HEAD" title="Compare with HEAD" onClick={() => ws.openDiff(tab.path, 'head')} />
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
        <img src={fsRawUrl(ws.mid, doc.path)} alt={nameOf(doc.path)} />
        <p className="wb-preview-note tabular">{doc.size >= 1024 ? `${Math.round(doc.size / 1024)} KB` : `${doc.size} B`}</p>
      </div>
    );
  }
  if (doc.kind === 'media') {
    const audio = ['mp3', 'wav', 'ogg', 'flac'].includes(extOf(doc.path));
    return <div className="wb-preview">{audio ? <audio controls src={fsRawUrl(ws.mid, doc.path)} /> : <video controls src={fsRawUrl(ws.mid, doc.path)} />}</div>;
  }
  if (doc.kind !== 'text' || !doc.model) {
    return (
      <div className="wb-empty">
        <Icon name="file" className="wb-empty-icon" />
        <p>{doc.readOnly}</p>
        <Button size="sm" onClick={() => window.open(fsRawUrl(ws.mid, doc.path), '_blank', 'noopener')}>
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
  if (tab.commit) return { name: tab.commit.label, dir: '' };
  if (tab.view?.kind === 'review') return { name: `${nameOf(tab.view.path)} (${tab.view.agent})`, dir: '' };
  if (tab.view?.kind === 'history') return { name: `${nameOf(tab.view.path)} (history)`, dir: '' };
  if (tab.view?.kind === 'graph') return { name: 'Commit graph', dir: '' };
  if (tab.view?.kind === 'pr') return { name: `#${tab.view.number} ${tab.view.title}`, dir: '' };
  const name = nameOf(tab.path);
  const twin = group.tabs.some((t) => t !== tab && nameOf(t.path) === name && t.path !== tab.path);
  const suffix = tab.diff === 'head' ? ' (HEAD)' : tab.diff === 'index' ? ' (changes)' : tab.diff === 'staged' ? ' (staged)' : tab.diff === 'disk' ? ' (on disk)' : tab.diff?.startsWith('ide:') ? ' (proposed)' : '';
  return { name: name + suffix, dir: twin ? nameOf(dirOf(ws.rel(tab.path))) : '' };
}

function TabStrip({ ws, group, gi, onAddFile, onAi }: { ws: Workspace; group: Group; gi: number; onAddFile: (p: string) => void; onAi: () => void }) {
  const [menu, setMenu] = useState<{ x: number; y: number; tab: Tab } | null>(null);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    ref.current?.querySelector('.wb-tab.is-active')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  }, [group.active]);

  const workspaceId = useContext(PaneEnv)?.ctl.workspaceId ?? '';
  const choices = usePaneChoices(ws, workspaceId);

  const paneItems = (t: Tab): MenuEntry[] => [
    { label: 'Move to Side Bar', icon: 'external', onSelect: () => ws.placePane(t.pane!, 'side') },
    { label: 'Split Right', icon: 'layout', disabled: ws.groups.length >= 3 && gi === ws.groups.length - 1, onSelect: () => (ws.activate(gi, t.id), ws.splitRight()) },
    'sep',
    { label: 'Close', icon: 'close', onSelect: () => ws.closeTab(gi, t.id) },
  ];

  const specialItems = (t: Tab): MenuEntry[] => [
    { label: 'Close', icon: 'close', shortcut: 'Alt+W', onSelect: () => ws.closeTab(gi, t.id) },
    { label: 'Close Others', onSelect: () => ws.closeOthers(gi, t.id) },
    { label: 'Close All', onSelect: () => ws.closeAll(gi) },
    'sep',
    { label: 'Split Right', icon: 'layout', disabled: ws.groups.length >= 3 && gi === ws.groups.length - 1, onSelect: () => (ws.activate(gi, t.id), ws.splitRight()) },
    ...(t.commit ? ([{ label: 'Copy Commit Hash', icon: 'copy', onSelect: () => void navigator.clipboard?.writeText(t.commit!.hash).then(() => toast.success('Hash copied.')) }] as MenuEntry[]) : []),
  ];

  const items = (t: Tab): MenuEntry[] => t.pane ? paneItems(t) : !t.path ? specialItems(t) : [
    { label: 'Close', icon: 'close', shortcut: 'Alt+W', onSelect: () => ws.closeTab(gi, t.id) },
    { label: 'Close Others', onSelect: () => ws.closeOthers(gi, t.id) },
    { label: 'Close All', onSelect: () => ws.closeAll(gi) },
    'sep',
    { label: 'Split Right', icon: 'layout', onSelect: () => ws.open(t.path, { pin: true, side: true }) },
    { label: 'Compare with HEAD', icon: 'branch', disabled: !ws.gitMark(t.path), onSelect: () => ws.openDiff(t.path, 'head', gi) },
    { label: 'Local History', icon: 'history', onSelect: () => ws.openView({ kind: 'history', path: t.path }) },
    { label: 'Reveal in Explorer', icon: 'folderOpen', onSelect: () => ws.revealInTree(t.path) },
    { label: 'Copy Path', icon: 'link', onSelect: () => void navigator.clipboard?.writeText(t.path).then(() => toast.success('Path copied.')) },
    { label: 'Copy Relative Path', icon: 'link', onSelect: () => void navigator.clipboard?.writeText(ws.rel(t.path)).then(() => toast.success('Path copied.')) },
    { label: 'Add to Board', icon: 'plus', onSelect: () => onAddFile(t.path) },
  ];

  return (
    <div className="wb-tabs" role="tablist" aria-label="Open files" ref={ref}>
      {group.tabs.map((t) => {
        const d = ws.docs.get(t.path);
        const active = group.active === t.id;
        const { name, dir } = tabLabel(ws, t, group);
        const mark = !t.diff ? ws.gitMark(t.path) : undefined;
        return (
          <div
            key={t.id}
            role="tab"
            tabIndex={active ? 0 : -1}
            aria-selected={active}
            className={`wb-tab ${active ? 'is-active' : ''} ${t.preview ? 'is-preview' : ''} ${mark ? `git-${mark.tone}` : ''} ${d?.dirty && !t.diff ? 'is-dirty' : ''}`}
            title={t.pane ? undefined : t.commit ? t.commit.label : t.view?.kind === 'graph' ? 'Commit graph' : t.view?.kind === 'pr' ? t.view.title : t.view ? ws.rel(ws.abs(t.view.path)) : t.path}
            draggable
            onDragStart={(e) => {
              e.dataTransfer.setData('application/x-nextboard-tab', JSON.stringify({ gi, id: t.id }));
              e.dataTransfer.effectAllowed = 'move';
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
                {t.commit || t.view?.kind === 'history' ? <Icon name="history" className="wb-icon" /> : t.view?.kind === 'graph' || t.view?.kind === 'pr' ? <Icon name="branch" className="wb-icon" /> : t.view ? <Icon name="checklist" className="wb-icon" /> : t.diff ? <Icon name="branch" className="wb-icon" /> : <FileIcon name={nameOf(t.path)} />}
                <span className="wb-tab-name truncate">{name}</span>
                {dir && <span className="wb-tab-dir truncate">{dir}</span>}
              </>
            )}
            <button
              type="button"
              className="wb-tab-close"
              aria-label={t.pane ? 'Close this agent' : `Close ${name}${d?.dirty ? ' (unsaved)' : ''}`}
              title={d?.dirty ? 'Unsaved changes' : 'Close'}
              onClick={(e) => {
                e.stopPropagation();
                ws.closeTab(gi, t.id);
              }}
            >
              <span className="wb-tab-dot" aria-hidden="true" />
              <Icon name="close" />
            </button>
          </div>
        );
      })}
      <span className="spacer" />
      <div className="wb-tabs-tools">
        <Menu label="Open an agent in a new tab" icon="bot" items={agentMenu(ws, workspaceId, choices, 'tab')} />
        <Button size="sm" variant="ghost" iconOnly icon="sparkle" aria-label="Edit with AI (Ctrl+I)" title="Edit with AI · Ctrl+I" disabled={!group.active} onClick={onAi} />
        <Button size="sm" variant="ghost" iconOnly icon="layout" aria-label="Split editor (Ctrl+\)" title="Split editor · Ctrl+\" disabled={!group.active || ws.groups.length >= 3} onClick={() => ws.splitRight()} />
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
      const j = await filesApi.aiEdit(ws.pid, { path: ws.rel(doc.path), language: model.getLanguageId(), instruction: text.trim(), before, selection, after });
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
        <Button type="submit" size="sm" variant="primary" loading={busy} disabled={!text.trim() || !!doc.readOnly}>
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

function GroupView({ ws, group, gi, onAddFile, aiOpen, setAiOpen }: { ws: Workspace; group: Group; gi: number; onAddFile: (p: string) => void; aiOpen: boolean; setAiOpen: (open: boolean) => void }) {
  const tab = group.tabs.find((t) => t.id === group.active) ?? null;
  const doc = tab ? ws.docs.get(tab.path) : undefined;
  const editor = ws.editors.get(group.id);
  const [over, setOver] = useState(false);

  const onDrop = (e: DragEvent) => {
    setOver(false);
    const moved = e.dataTransfer.getData('application/x-nextboard-tab');
    const path = e.dataTransfer.getData('application/x-nextboard-path');
    if (moved) {
      e.preventDefault();
      const { gi: from, id } = JSON.parse(moved) as { gi: number; id: string };
      ws.moveTabToGroup(from, id, gi);
    } else if (path && ws.items.get(path) === undefined) {
      e.preventDefault();
      ws.open(path, { pin: true, group: gi });
    }
  };

  return (
    <section
      className={`wb-group ${ws.activeGroup === gi && ws.groups.length > 1 ? 'is-active' : ''} ${over ? 'is-drop' : ''}`}
      aria-label={`Editor group ${gi + 1}`}
      onMouseDownCapture={() => ws.focusGroup(gi)}
      onDragOver={(e) => {
        if ([...e.dataTransfer.types].some((t) => t.startsWith('application/x-nextboard'))) {
          e.preventDefault();
          setOver(true);
        }
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOver(false);
      }}
      onDrop={onDrop}
    >
      <TabStrip ws={ws} group={group} gi={gi} onAddFile={onAddFile} onAi={() => setAiOpen(true)} />
      {tab?.pane ? (
        <div className="wb-pane-tab">
          <AgentPane key={tab.pane} ws={ws} id={tab.pane} />
        </div>
      ) : tab?.commit ? (
        <div className="wb-pane-tab">
          <CommitView key={tab.id} ws={ws} commit={tab.commit} />
        </div>
      ) : tab?.view ? (
        <div className="wb-pane-tab">
          {tab.view.kind === 'review' ? <ReviewView key={tab.id} ws={ws} tab={tab} session={tab.view.session} path={tab.view.path} agent={tab.view.agent} /> : tab.view.kind === 'graph' ? <CommitGraphView key={tab.id} ws={ws} /> : tab.view.kind === 'pr' ? <PullRequestView key={tab.id} ws={ws} number={tab.view.number} repo={tab.view.repo} /> : <LocalHistoryView key={tab.id} ws={ws} path={tab.view.path} />}
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
    </section>
  );
}

export function EditorArea({ ws, onAddFile, aiOpen, setAiOpen }: { ws: Workspace; onAddFile: (p: string) => void; aiOpen: boolean; setAiOpen: (open: boolean) => void }) {
  useWs(ws);
  const n = ws.groups.length;
  const [sizes, setSizes] = useState<number[]>([]);
  const area = useRef<HTMLDivElement>(null);
  const weights = sizes.length === n ? sizes : Array.from({ length: n }, () => 1);

  const startSash = (i: number, e: ReactPointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const box = area.current?.getBoundingClientRect();
    if (!box) return;
    const total = weights.reduce((a, b) => a + b, 0);
    const pair = weights[i] + weights[i + 1];
    const startX = e.clientX;
    const startA = weights[i];
    const move = (ev: PointerEvent) => {
      const delta = ((ev.clientX - startX) / box.width) * total;
      const a = Math.min(pair * 0.9, Math.max(pair * 0.1, startA + delta));
      const next = [...weights];
      next[i] = a;
      next[i + 1] = pair - a;
      setSizes(next);
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  return (
    <div className="wb-editors" ref={area}>
      {ws.groups.map((g, i) => (
        <div key={g.id} className="wb-group-slot" style={{ flexGrow: weights[i] } as CSSProperties}>
          {i > 0 && <div className="wb-sash wb-sash-v" role="separator" aria-orientation="vertical" onPointerDown={(e) => startSash(i - 1, e)} />}
          <GroupView ws={ws} group={g} gi={i} onAddFile={onAddFile} aiOpen={aiOpen} setAiOpen={setAiOpen} />
        </div>
      ))}
    </div>
  );
}
