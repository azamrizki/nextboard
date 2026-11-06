// Bottom panel of the Code workbench: terminals of this project (shell sessions with no
// board node, kept when the workbench closes), Problems from the language services and
// Output from commands on save and language servers.

import { memo, useEffect, useMemo, useRef, useState } from 'react';
import { useConsoleThemeAttr } from '../../lib/theme';
import { api, errorMessage } from '../../api';
import { useTerminal } from '../../board/useTerminal';
import { Button } from '../../components/Button';
import { Icon, type IconName } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { useSession, useStoreReady } from '../../store';
import { DebugConsole } from './DebugPanel';
import { monaco } from './monaco';
import { PortsPanel } from './PortsPanel';
import { TasksPanel } from './TasksPanel';
import { useWs } from './useWs';
import { dirOf, nameOf, type PanelView, type Workspace } from './workspace';

export const TerminalView = memo(function TerminalView({ sessionId }: { sessionId: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const consoleTheme = useConsoleThemeAttr();
  useTerminal(sessionId, ref);
  return <div className="wb-terminal" ref={ref} data-keep-escape="" data-theme={consoleTheme} data-session={sessionId} />;
});

/** Start a terminal in the project (or a folder of it). */
export async function newTerminal(ws: Workspace, workspaceId: string, cwd?: string, input?: string) {
  try {
    const s = await api.createSession({ workspaceId, preset: 'shell', panel: true, name: cwd ? nameOf(cwd) : 'Terminal', cwd: cwd ?? ws.root });
    ws.terminals = [...ws.terminals, s.id];
    ws.activeTerminal = s.id;
    ws.panelOpen = true;
    ws.panelView = 'terminal';
    ws.changed();
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
  return (
    <div className={`wb-term-tab ${active ? 'is-active' : ''}`}>
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
        {s?.name || `Terminal ${index + 1}`}
        {s && s.status !== 'running' && s.status !== 'starting' && ' (ended)'}
      </button>
      <button type="button" className="wb-tab-close" aria-label="Close terminal" title="Close terminal" onClick={close}>
        <Icon name="close" />
      </button>
    </div>
  );
}

function Terminals({ ws, workspaceId }: { ws: Workspace; workspaceId: string }) {
  const started = useRef(false);
  useEffect(() => {
    if (started.current || ws.terminals.length) return;
    started.current = true;
    void newTerminal(ws, workspaceId);
  }, [ws, workspaceId]);
  const active = ws.terminals.includes(ws.activeTerminal) ? ws.activeTerminal : ws.terminals[0];
  return (
    <div className="wb-terms">
      <div className="wb-term-body">{active && <TerminalView key={active} sessionId={active} />}</div>
      <div className="wb-term-list">
        <Button size="sm" variant="ghost" icon="plus" onClick={() => void newTerminal(ws, workspaceId)}>
          New terminal
        </Button>
        {ws.terminals.map((id, i) => (
          <TerminalTab key={id} ws={ws} id={id} index={i} />
        ))}
      </div>
    </div>
  );
}

interface Problem {
  path: string;
  line: number;
  col: number;
  message: string;
  severity: number;
  source?: string;
}

export function useProblems(ws: Workspace) {
  const version = ws.version;
  return useMemo(() => {
    const list: Problem[] = [];
    for (const m of monaco.editor.getModelMarkers({})) {
      if (m.resource.scheme !== 'file') continue;
      list.push({ path: m.resource.path, line: m.startLineNumber, col: m.startColumn, message: m.message, severity: m.severity, source: m.source });
    }
    for (const f of ws.lsp.closedDiagnostics()) for (const d of f.items) list.push({ path: f.path, ...d });
    const errors = list.filter((p) => p.severity === monaco.MarkerSeverity.Error).length;
    const warnings = list.filter((p) => p.severity === monaco.MarkerSeverity.Warning).length;
    return { list, errors, warnings };
    // the workspace mutates in place: its version is the dependency
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, version]);
}

const SEVERITY_ICON = (s: number): { icon: IconName; cls: string } =>
  s === monaco.MarkerSeverity.Error ? { icon: 'alertCircle', cls: 'is-error' } : s === monaco.MarkerSeverity.Warning ? { icon: 'alert', cls: 'is-warning' } : { icon: 'info', cls: 'is-info' };

function Problems({ ws }: { ws: Workspace }) {
  const { list } = useProblems(ws);
  const [closed, setClosed] = useState<Set<string>>(new Set());
  const byFile = useMemo(() => {
    const m = new Map<string, Problem[]>();
    for (const p of list) m.set(p.path, [...(m.get(p.path) ?? []), p]);
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [list]);
  if (!list.length) return <p className="wb-panel-msg">No problems have been detected in the open files.</p>;
  return (
    <div className="wb-problems nowheel">
      {byFile.map(([path, items]) => {
        const isClosed = closed.has(path);
        return (
          <div key={path}>
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
              <span className="wb-result-dir truncate">{dirOf(path) === ws.root ? '' : ws.rel(dirOf(path))}</span>
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
  const tab = (v: PanelView, label: string, badge?: number) => (
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
      {label}
      {badge ? <span className="wb-badge tabular">{badge}</span> : null}
    </button>
  );
  return (
    <div className="wb-panel">
      <div className="wb-panel-head">
        <div role="tablist" aria-label="Panel" className="wb-panel-tabs">
          {tab('terminal', 'Terminal')}
          {tab('problems', 'Problems', errors + warnings)}
          {tab('tasks', 'Tasks', [...ws.taskRuns.values()].filter((r) => r.state === 'running').length)}
          {tab('output', 'Output')}
          {tab('ports', 'Ports')}
          {tab('debug', 'Debug Console')}
        </div>
        <span className="spacer" />
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
