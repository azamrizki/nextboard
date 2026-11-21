// Bottom panel of the Code workbench: terminals of this project (shell sessions with no
// board node, kept when the workbench closes), Problems from the language services and
// Output from commands on save and language servers. A terminal starts in the card's part, on
// that part's machine; a Root card asks: at the project root (every part) or in one part.
// A terminal can also live in the editor area, as a tab (docs/API-studio.md §7.15): started
// there, or moved there and back; its shell keeps running through every move.

import { memo, useEffect, useMemo, useRef, useState } from 'react';
import { useConsoleThemeAttr } from '../../lib/theme';
import { api, errorMessage } from '../../api';
import { useTerminal } from '../../board/useTerminal';
import { Button } from '../../components/Button';
import { Icon, type IconName } from '../../components/Icon';
import { ContextMenu, Menu, type MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import { isRoot, type Placement } from '../../lib/parts';
import { lastPlacement, placeValue, rememberPlacement } from '../../lib/placement';
import { store, useSession, useStoreReady } from '../../store';
import { DebugConsole } from './DebugPanel';
import { monaco } from './monaco';
import { byPart, defaultPlace, placeChoices, placeDir } from './partGroups';
import { mountedPart } from './partGit';
import { asFileKey, isRootKey, LOCAL_MACHINE, splitKey, type FileKey } from './paths';
import { PortsPanel } from './PortsPanel';
import { TasksPanel } from './TasksPanel';
import { useMarkers, useWs } from './useWs';
import { useIsNextCode } from '../../nextcode/detect';
import { NextCodeChat } from '../../nextcode/NextCodeChat';
import { dirOf, isTerminalPane, nameOf, type PanelView, type Workspace } from './workspace';

/** A console's screen: its terminal, or the chat of a NextCode console (docs/API-agents.md §30), which has none. */
export const TerminalView = memo(function TerminalView({ sessionId }: { sessionId: string }) {
  const nextcode = useIsNextCode(sessionId);
  if (nextcode) return <NextCodeChat sessionId={sessionId} variant="card" />;
  return <XtermView sessionId={sessionId} />;
});

const XtermView = memo(function XtermView({ sessionId }: { sessionId: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const consoleTheme = useConsoleThemeAttr();
  useTerminal(sessionId, ref);
  return <div className="wb-terminal" ref={ref} data-keep-escape="" data-theme={consoleTheme} data-session={sessionId} />;
});

/** The file or row the card works on now, for the default place of a terminal or an agent. */
export function activeKey(ws: Workspace): FileKey | null {
  const t = ws.activeTab;
  if (t?.path) return t.path;
  const part = mountedPart(ws, t?.commit?.part ?? t?.view?.part);
  if (part) return part.rootKey;
  return ws.selected ? asFileKey(ws.selected) : null;
}

/** Where a new terminal or agent of the card starts unless told: its part, or in a Root card the part in use, else the last choice, else the root. */
export function defaultPlaceOf(ws: Workspace): Placement {
  const project = store.getState().projects[ws.projectId];
  return defaultPlace(ws.paths, activeKey(ws), project ? lastPlacement(project) : null);
}

/** A choice made in a Root card's picker: the next default when nothing else says. */
export function rememberPlace(ws: Workspace, p: Placement) {
  const project = store.getState().projects[ws.projectId];
  if (project) rememberPlacement(project, p);
}

/** A machine's name for a tooltip: This PC, else its name in the hub's list. */
export const machineLabel = (id: string) => (id === LOCAL_MACHINE || !id ? 'This PC' : (store.getState().machines[id]?.name ?? id));

/** "Root (all parts)" and the card's parts, the default checked: what a Root card's "New terminal" and agent menus offer. */
export function placeItems(ws: Workspace, start: (p: Placement) => void): MenuEntry[] {
  const def = placeValue(defaultPlaceOf(ws));
  return [
    { type: 'heading', label: 'Start in' },
    ...placeChoices(ws.paths).map<MenuEntry>((p) => {
      const part = isRoot(p) ? null : mountedPart(ws, p.partId);
      return {
        label: part ? part.name : 'Root (all parts)',
        icon: part ? 'folder' : 'layers',
        checked: placeValue(p) === def,
        title: part ? `${part.root} on ${machineLabel(part.machineId)}` : 'On This PC, in a folder with one entry per part: it reaches every part.',
        onSelect: () => {
          rememberPlace(ws, p);
          start(p);
        },
      };
    }),
  ];
}

/** Where a new terminal shows: the panel (as before), or a tab of the editor area. */
export type TerminalWhere = 'panel' | 'editor';

/**
 * Start a terminal of the card: in a folder (its part, on that part's machine), else at a place
 * chosen in a Root card's picker, else at the card's default (defaultPlaceOf). The project root
 * (ROOT_KEY, or Root chosen) is a root terminal on This PC that reaches every part. `where`
 * "editor" opens it as a tab of the active editor group instead of in the panel.
 */
export async function newTerminal(ws: Workspace, workspaceId: string, cwd?: string, input?: string, place?: Placement, where: TerminalWhere = 'panel') {
  const dir = cwd ? (ws.toKey(cwd) ?? undefined) : undefined;
  const show = where;
  const at = place ?? (dir ? null : defaultPlaceOf(ws));
  const at2 = dir ? ws.placeOf(dir) : ws.placeOf(at ? placeDir(ws.paths, at) : undefined);
  const part = at && !isRoot(at) ? mountedPart(ws, at.partId) : null;
  const name = dir ? (isRootKey(dir) ? 'Root' : nameOf(splitKey(dir).abs) || 'Terminal') : ws.paths.multi ? (part?.name ?? 'Root') : 'Terminal';
  try {
    const s = await api.createSession({ workspaceId, preset: 'shell', panel: true, name, ...at2 });
    ws.hadTerminal = true;
    if (show === 'editor') {
      // its tab must find it in the store, or the tab takes it for gone
      store.upsertSession(s);
      ws.openPane(s.id);
    } else {
      ws.terminals = [...ws.terminals, s.id];
      ws.activeTerminal = s.id;
      ws.panelOpen = true;
      ws.panelView = 'terminal';
      ws.changed();
    }
    // typed, not run: the user decides whether to press Enter
    if (input) window.setTimeout(() => void api.sendInput(s.id, input).catch(() => undefined), 800);
  } catch (err) {
    toast.error(`Could not start a terminal: ${errorMessage(err)}`);
  }
}

function TerminalTab({ ws, id, index }: { ws: Workspace; id: string; index: number }) {
  const s = useSession(id);
  const ready = useStoreReady();
  // the hub no longer has it (it was closed elsewhere)
  useEffect(() => {
    if (!ready || s) return;
    ws.terminals = ws.terminals.filter((t) => t !== id);
    if (ws.activeTerminal === id) ws.activeTerminal = ws.terminals[0] ?? '';
    ws.changed();
  }, [ready, s, ws, id]);
  const close = () => {
    ws.terminals = ws.terminals.filter((t) => t !== id);
    if (ws.activeTerminal === id) ws.activeTerminal = ws.terminals[0] ?? '';
    ws.changed();
    void api.deleteSession(id).catch(() => undefined);
  };
  const active = ws.activeTerminal === id;
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const label = s?.name || `Terminal ${index + 1}`;
  return (
    // a long press is the row's right-click on a touch screen (lib/longpress.ts)
    <div
      className={`wb-term-tab ${active ? 'is-active' : ''}`}
      data-longpress=""
      onContextMenu={(e) => {
        e.preventDefault();
        setMenu({ x: e.clientX, y: e.clientY });
      }}
    >
      <button
        type="button"
        className="truncate"
        aria-pressed={active}
        onClick={() => {
          ws.activeTerminal = id;
          ws.changed();
        }}
      >
        <Icon name="terminal" />
        {label}
        {s && s.status !== 'running' && s.status !== 'starting' && ' (ended)'}
      </button>
      <button type="button" className="wb-tab-close wb-term-move" aria-label={`Move ${label} to the editor area`} title="Move terminal to editor area" onClick={() => ws.movePanelTerminalToEditor(id)}>
        <Icon name="external" />
      </button>
      <button type="button" className="wb-tab-close" aria-label="Close terminal" title="Close terminal" onClick={close}>
        <Icon name="close" />
      </button>
      {menu && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          label="Terminal actions"
          items={[
            { label: 'Move Terminal to Editor Area', icon: 'external', title: 'Its shell keeps running', onSelect: () => ws.movePanelTerminalToEditor(id) },
            'sep',
            { label: 'Kill Terminal', icon: 'close', danger: true, onSelect: close },
          ]}
          onClose={() => setMenu(null)}
        />
      )}
    </div>
  );
}

/**
 * The panel's + menu: a new terminal in the panel or in the editor area (a Root card: at the root
 * or in a part, for each), and the active terminal moved to the editor area.
 */
export function terminalMenu(ws: Workspace, workspaceId: string, active?: string): MenuEntry[] {
  const start = (where: TerminalWhere) => (p?: Placement) => void newTerminal(ws, workspaceId, undefined, undefined, p, where);
  const items: MenuEntry[] = ws.paths.multi
    ? [
        // a Root card asks where: the root (every part) or one part
        ...placeItems(ws, start('panel')).map((x) => (typeof x === 'object' && 'type' in x && x.type === 'heading' ? { ...x, label: 'New terminal in' } : x)),
        ...placeItems(ws, start('editor')).map((x) => (typeof x === 'object' && 'type' in x && x.type === 'heading' ? { ...x, label: 'New terminal in the editor area, in' } : x)),
      ]
    : [
        { label: 'New Terminal', icon: 'terminal', onSelect: () => start('panel')() },
        { label: 'New Terminal in Editor Area', icon: 'splitRight', title: 'A terminal as an editor tab', onSelect: () => start('editor')() },
      ];
  if (active) items.push('sep', { label: 'Move Terminal to Editor Area', icon: 'external', title: 'The terminal shown here; its shell keeps running', onSelect: () => ws.movePanelTerminalToEditor(active) });
  return items;
}

function Terminals({ ws, workspaceId }: { ws: Workspace; workspaceId: string }) {
  const started = useRef(false);
  // the first time only: a view whose terminals were all moved out, closed or never wanted one
  // since then gets none by itself each time the panel shows (a phone leaves and comes back often)
  useEffect(() => {
    if (started.current || ws.terminals.length || ws.hadTerminal) return;
    if (ws.groups.some((g) => g.tabs.some((t) => t.pane && isTerminalPane(t.pane)))) return;
    started.current = true;
    void newTerminal(ws, workspaceId);
  }, [ws, workspaceId]);
  const active = ws.terminals.includes(ws.activeTerminal) ? ws.activeTerminal : ws.terminals[0];
  return (
    <div className="wb-terms">
      <div className="wb-term-body">
        {active ? <TerminalView key={active} sessionId={active} /> : <p className="wb-panel-msg">No terminal here: start one with New terminal, or bring one back from the editor area (its tab’s Move to Panel).</p>}
      </div>
      <div className="wb-term-list">
        {ws.terminals.map((id, i) => (
          <TerminalTab key={id} ws={ws} id={id} index={i} />
        ))}
      </div>
    </div>
  );
}

/** The problems of the files this card shows or holds (every mounted part's servers), by file, and how many. */
export function useProblems(ws: Workspace) {
  const version = ws.version;
  const markers = useMarkers(ws);
  return useMemo(() => {
    const files = ws.problems();
    let errors = 0;
    let warnings = 0;
    for (const f of files) {
      for (const p of f.items) {
        if (p.severity === monaco.MarkerSeverity.Error) errors++;
        else if (p.severity === monaco.MarkerSeverity.Warning) warnings++;
      }
    }
    return { files, errors, warnings };
    // the workspace mutates in place: its version is the dependency
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, version, markers]);
}

const SEVERITY_ICON = (s: number): { icon: IconName; cls: string } =>
  s === monaco.MarkerSeverity.Error ? { icon: 'alertCircle', cls: 'is-error' } : s === monaco.MarkerSeverity.Warning ? { icon: 'alert', cls: 'is-warning' } : { icon: 'info', cls: 'is-info' };

function Problems({ ws }: { ws: Workspace }) {
  const { files } = useProblems(ws);
  const [closed, setClosed] = useState<Set<string>>(new Set());
  // by part in a Root card (in part order, files outside the parts last), by path inside each
  const sections = useMemo(() => byPart(ws.paths, [...files].sort((a, b) => a.label.localeCompare(b.label)), (f) => f.key), [files, ws.paths]);
  /** The folder a file is in, as the card shows it: inside its part, else on its machine. */
  const folderOf = (key: FileKey) => {
    const pr = ws.partRel(key);
    const d = dirOf(pr ? pr.rel : splitKey(key).abs);
    return pr ? (d === '/' ? '' : d) : d;
  };
  if (!files.length) return <p className="wb-panel-msg">No problems have been detected in the open files.</p>;
  return (
    <div className="wb-problems nowheel">
      {sections.map((sec) => (
        <div key={sec.mount?.partId ?? '@outside'}>
          {ws.paths.multi && (
            <div className="wb-section-title wb-part-head">
              <Icon name="folder" className="wb-icon" />
              <span className="truncate">{sec.mount ? sec.mount.name : 'Outside the parts'}</span>
              <span className="spacer" />
              <span className="wb-result-count tabular">{sec.items.reduce((n, f) => n + f.items.length, 0)}</span>
            </div>
          )}
          {sec.items.map(({ key: path, label, items }) => {
            const isClosed = closed.has(path);
            return (
              <div key={path} title={label}>
                <button
                  type="button"
                  className="wb-row wb-problem-file"
                  aria-expanded={!isClosed}
                  onClick={() =>
                    setClosed((cur) => {
                      const n = new Set(cur);
                      if (n.has(path)) n.delete(path);
                      else n.add(path);
                      return n;
                    })
                  }
                >
                  <Icon name={isClosed ? 'chevronRight' : 'chevronDown'} className="wb-chev" />
                  <Icon name="file" className="wb-icon" />
                  <span className="wb-row-name truncate">{nameOf(path)}</span>
                  <span className="wb-result-dir truncate">{folderOf(path)}</span>
                  <span className="wb-result-count tabular">{items.length}</span>
                </button>
                {!isClosed &&
                  items.map((p, i) => {
                    const sev = SEVERITY_ICON(p.severity);
                    return (
                      <button key={i} type="button" className="wb-problem" onClick={() => ws.open(path, { pin: true, line: p.line, col: p.col, source: true })}>
                        <Icon name={sev.icon} className={`wb-problem-icon ${sev.cls}`} />
                        <span className="wb-problem-msg">{p.message}</span>
                        {p.source && <span className="wb-result-dir">{p.source}</span>}
                        <span className="wb-result-dir tabular">
                          {p.line}:{p.col}
                        </span>
                      </button>
                    );
                  })}
              </div>
            );
          })}
        </div>
      ))}
    </div>
  );
}

function Output({ ws }: { ws: Workspace }) {
  const ref = useRef<HTMLPreElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [ws.output.length]);
  if (!ws.output.length) return <p className="wb-panel-msg">Output of commands on save and language servers appears here.</p>;
  return (
    <pre className="wb-output nowheel" ref={ref}>
      {ws.output.join('\n')}
    </pre>
  );
}

export function BottomPanel({ ws, workspaceId }: { ws: Workspace; workspaceId: string }) {
  useWs(ws);
  const { errors, warnings } = useProblems(ws);
  const tab = (v: PanelView, label: string, badge?: number, icon?: IconName) => (
    <button
      type="button"
      role="tab"
      aria-selected={ws.panelView === v}
      className={`wb-panel-tab ${ws.panelView === v ? 'is-active' : ''}`}
      onClick={() => {
        ws.panelView = v;
        ws.changed();
      }}
    >
      {icon && <Icon name={icon} />}
      {label}
      {badge ? <span className="wb-badge tabular">{badge}</span> : null}
    </button>
  );
  return (
    <div className="wb-panel">
      <div className="wb-panel-head">
        <div role="tablist" aria-label="Panel" className="wb-panel-tabs">
          {tab('terminal', 'Terminal', undefined, 'terminal')}
          {tab('problems', 'Problems', errors + warnings, 'alertCircle')}
          {tab('tasks', 'Tasks', [...ws.taskRuns.values()].filter((r) => r.state === 'running').length)}
          {tab('output', 'Output')}
          {tab('ports', 'Ports')}
          {tab('debug', 'Debug Console')}
        </div>
        <span className="spacer" />
        {ws.panelView === 'terminal' &&
          (ws.paths.multi ? (
            // a Root card asks where: the root (every part) or one part, in the panel or the editor area
            <Menu label="New terminal" icon="plus" items={() => terminalMenu(ws, workspaceId, ws.activeTerminal)} />
          ) : (
            <>
              <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="New terminal" title="New terminal" onClick={() => void newTerminal(ws, workspaceId)} />
              <Menu label="More terminal actions" icon="splitRight" items={() => terminalMenu(ws, workspaceId, ws.activeTerminal)} />
            </>
          ))}
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          icon="close"
          aria-label="Close panel"
          title="Close panel · Ctrl+`"
          onClick={() => {
            ws.panelOpen = false;
            ws.changed();
          }}
        />
      </div>
      <div className="wb-panel-body">
        {ws.panelView === 'terminal' && <Terminals ws={ws} workspaceId={workspaceId} />}
        {ws.panelView === 'problems' && <Problems ws={ws} />}
        {ws.panelView === 'tasks' && <TasksPanel ws={ws} />}
        {ws.panelView === 'output' && <Output ws={ws} />}
        {ws.panelView === 'ports' && <PortsPanel ws={ws} />}
        {ws.panelView === 'debug' && <DebugConsole ws={ws} />}
      </div>
    </div>
  );
}
