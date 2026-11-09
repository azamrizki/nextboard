// Run and Debug: the side view (configurations, variables, watch, call stack, breakpoints),
// the floating toolbar, the debug console, and what the debugger adds to an editor:
// breakpoint dots in the glyph margin, the stopped line, values on hover.

import { useEffect, useRef, useState, type KeyboardEvent, type RefObject } from 'react';
import { Button } from '../../components/Button';
import { Checkbox } from '../../components/form';
import { Icon } from '../../components/Icon';
import { Modal } from '../../components/Modal';
import { monaco } from './monaco';
import type { Breakpoint, DapVariable, Debugger, LaunchConfig } from './debug';
import { useWs } from './useWs';
import { nameOf, type Workspace } from './workspace';

function Section({ title, count, children, defaultOpen = true, actions }: { title: string; count?: number; children: React.ReactNode; defaultOpen?: boolean; actions?: React.ReactNode }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <section className="wb-dbg-section">
      <div className="wb-dbg-head">
        <button type="button" className="wb-section-title wb-section-toggle" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          <Icon name={open ? 'chevronDown' : 'chevronRight'} className="wb-chev" />
          <span>{title}</span>
          {count ? <span className="wb-result-count tabular">{count}</span> : null}
        </button>
        {actions}
      </div>
      {open && children}
    </section>
  );
}

function VariableRow({ dbg, v, depth }: { dbg: Debugger; v: DapVariable; depth: number }) {
  return (
    <>
      <button type="button" className="wb-var" style={{ paddingLeft: 8 + depth * 12 }} aria-expanded={v.ref ? v.open : undefined} title={v.type ? `${v.name}: ${v.type}` : v.name} onClick={() => v.ref && void dbg.toggleVar(v)}>
        {v.ref ? <Icon name={v.open ? 'chevronDown' : 'chevronRight'} className="wb-chev" /> : <span className="wb-chev" aria-hidden="true" />}
        <span className="wb-var-name">{v.name}</span>
        <span className="wb-var-value mono truncate">{v.value}</span>
      </button>
      {v.open && v.children?.map((c) => <VariableRow key={c.key} dbg={dbg} v={c} depth={depth + 1} />)}
    </>
  );
}

function BreakpointDialog({ dbg, b }: { dbg: Debugger; b: Breakpoint }) {
  const [condition, setCondition] = useState(b.condition ?? '');
  const [hit, setHit] = useState(b.hitCondition ?? '');
  const [log, setLog] = useState(b.logMessage ?? '');
  const close = () => {
    dbg.editing = null;
    dbg.updateBreakpoint(b, {});
  };
  return (
    <Modal
      title={`Breakpoint at ${nameOf(b.path)}:${b.line}`}
      size="sm"
      onClose={close}
      footer={
        <>
          <Button
            variant="danger-ghost"
            onClick={() => {
              dbg.editing = null;
              dbg.removeBreakpoint(b);
            }}
          >
            Remove
          </Button>
          <Button
            variant="primary"
            onClick={() => {
              dbg.editing = null;
              dbg.updateBreakpoint(b, { condition: condition.trim() || undefined, hitCondition: hit.trim() || undefined, logMessage: log.trim() || undefined });
            }}
          >
            Save
          </Button>
        </>
      }
    >
      <label className="field-label" htmlFor="bp-condition">
        Stop only when
      </label>
      <input id="bp-condition" className="control mono" placeholder="i > 10" value={condition} onChange={(e) => setCondition(e.target.value)} />
      <label className="field-label" htmlFor="bp-hit">
        Hit count
      </label>
      <input id="bp-hit" className="control mono" placeholder="5" value={hit} onChange={(e) => setHit(e.target.value)} />
      <label className="field-label" htmlFor="bp-log">
        Log a message instead of stopping
      </label>
      <input id="bp-log" className="control mono" placeholder="value is {x}" value={log} onChange={(e) => setLog(e.target.value)} />
    </Modal>
  );
}

// Choosing the process to attach to. It says what opening an inspector means: on a server
// other people log in to, that is a way into the program for as long as it stays open.
function AttachPicker({ dbg }: { dbg: Debugger }) {
  const p = dbg.picker;
  if (!p) return null;
  return (
    <Modal title="Attach to a Node.js process" size="md" onClose={() => dbg.closePicker()}>
      <p className="text-2 fs-sm">If the process has no inspector yet, NextBoard opens one with a signal and closes it when you stop. Until then, anyone with a login on that machine could run code in that process.</p>
      {p.error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{p.error}</span>
        </div>
      )}
      {!p.items && (
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Looking for node processes…
        </div>
      )}
      {p.items && !p.items.length && !p.error && <div className="list-msg">No node process of yours is running on this machine.</div>}
      <ul className="plain wb-attach-list">
        {p.items?.map((x) => (
          <li key={x.pid}>
            <button type="button" className="wb-attach-row" disabled={!!p.opening} aria-busy={p.opening === x.pid} onClick={() => void dbg.attachTo(x.pid)}>
              <span className="mono fs-xs truncate">{x.command || 'node'}</span>
              <span className="text-3 fs-xs truncate">
                pid {x.pid}
                {x.cwd ? ` · ${x.cwd}` : ''}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </Modal>
  );
}

export function DebugView({ ws, onInstall }: { ws: Workspace; onInstall: (command: string) => void }) {
  useWs(ws);
  const dbg = ws.debug;
  const [configs, setConfigs] = useState<LaunchConfig[]>([]);
  const [watch, setWatch] = useState('');
  const activePath = ws.activeDoc?.path;

  useEffect(() => {
    void dbg.refreshAdapters();
  }, [dbg]);

  // launch.json may have just been edited, and the automatic entries follow the file
  const launchMtime = ws.docs.get(ws.abs('.vscode/launch.json'))?.mtime;
  useEffect(() => {
    let stop = false;
    void dbg.configurations().then((c) => !stop && setConfigs(c));
    return () => {
      stop = true;
    };
  }, [dbg, activePath, launchMtime]);

  const selected = configs.find((c) => c.name === dbg.configName) ?? configs[0];

  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2>Run and debug</h2>
        <Button size="sm" variant="ghost" iconOnly icon="sliders" aria-label="Open launch.json" title="Open launch.json" onClick={() => void dbg.openLaunchJson()} />
      </header>
      <div className="wb-dbg-start">
        <select
          className="control control-sm grow"
          aria-label="Debug configuration"
          value={selected?.name ?? ''}
          disabled={dbg.active}
          onChange={(e) => {
            dbg.configName = e.target.value;
            ws.changed();
          }}
        >
          {!configs.length && <option value="">Open a Go, Python or JavaScript file, or add launch.json</option>}
          {configs.map((c) => (
            <option key={c.name} value={c.name}>
              {c.name}
            </option>
          ))}
        </select>
        <Button size="sm" variant="primary" iconOnly icon="play" aria-label="Start debugging (F5)" title="Start debugging · F5" disabled={!selected || dbg.active} onClick={() => selected && void dbg.start(selected)} />
      </div>
      {dbg.notice && (
        <div className="wb-dbg-notice" role="alert">
          <p>{dbg.notice.text}</p>
          {dbg.notice.install && (
            <Button size="sm" onClick={() => onInstall(dbg.notice!.install!)} title={dbg.notice.install}>
              Install…
            </Button>
          )}
        </div>
      )}
      <AttachPicker dbg={dbg} />
      <div className="wb-dbg-body nowheel">
        <Section title="Variables">
          {dbg.state !== 'stopped' && <div className="wb-row-msg">{dbg.state === 'running' ? 'Running…' : 'Not paused.'}</div>}
          {dbg.state === 'stopped' &&
            dbg.scopes.map((s) => (
              <div key={s.name}>
                <button type="button" className="wb-var is-scope" aria-expanded={s.open} onClick={() => void dbg.toggleScope(s)}>
                  <Icon name={s.open ? 'chevronDown' : 'chevronRight'} className="wb-chev" />
                  <span>{s.name}</span>
                </button>
                {s.open && s.vars?.map((v) => <VariableRow key={v.key} dbg={dbg} v={v} depth={1} />)}
              </div>
            ))}
        </Section>
        <Section title="Watch" count={dbg.watches.length}>
          {dbg.watches.map((w) => (
            <div key={w.expr} className="wb-watch">
              {w.variable ? (
                <VariableRow dbg={dbg} v={w.variable} depth={0} />
              ) : (
                <div className="wb-var is-static">
                  <span className="wb-chev" aria-hidden="true" />
                  <span className="wb-var-name">{w.expr}</span>
                  <span className={`wb-var-value mono truncate ${w.error ? 'is-error' : ''}`}>{w.value ?? ''}</span>
                </div>
              )}
              <button type="button" className="wb-watch-remove" aria-label={`Remove the watch ${w.expr}`} onClick={() => dbg.removeWatch(w.expr)}>
                <Icon name="close" />
              </button>
            </div>
          ))}
          <form
            className="wb-watch-add"
            onSubmit={(e) => {
              e.preventDefault();
              dbg.addWatch(watch);
              setWatch('');
            }}
          >
            <input className="control control-sm mono" aria-label="Add a watch expression" placeholder="Add an expression" value={watch} onChange={(e) => setWatch(e.target.value)} />
          </form>
        </Section>
        <Section title="Call stack" count={dbg.frames.length}>
          {dbg.threads.length > 1 && (
            <select className="control control-sm wb-dbg-thread" aria-label="Thread" value={dbg.threadId} onChange={(e) => void dbg.selectThread(Number(e.target.value))}>
              {dbg.threads.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          )}
          {dbg.frames.map((f) => (
            <button key={f.id} type="button" className={`wb-frame ${dbg.frameId === f.id ? 'is-active' : ''}`} disabled={!f.path} title={f.path ?? f.label} onClick={() => void dbg.selectFrame(f)}>
              <span className="wb-frame-name truncate">{f.name}</span>
              <span className="wb-result-dir tabular truncate">
                {f.path ? nameOf(f.path) : (f.label ?? '')}
                {f.line ? `:${f.line}` : ''}
              </span>
            </button>
          ))}
        </Section>
        <Section
          title="Breakpoints"
          count={dbg.breakpoints.length}
          actions={dbg.breakpoints.length ? <Button size="sm" variant="ghost" iconOnly icon="trash" aria-label="Remove all breakpoints" title="Remove all breakpoints" onClick={() => dbg.removeAllBreakpoints()} /> : undefined}
        >
          {dbg.filters.map((f) => (
            <div key={f.filter} className="wb-bp-filter">
              <Checkbox checked={dbg.enabledFilters.has(f.filter)} label={f.label} onChange={(on) => dbg.setFilter(f.filter, on)} />
            </div>
          ))}
          {dbg.breakpoints.map((b) => (
            <div key={b.id} className="wb-bp-row">
              <input type="checkbox" aria-label={`Enable the breakpoint at ${nameOf(b.path)}:${b.line}`} checked={b.enabled} onChange={(e) => dbg.updateBreakpoint(b, { enabled: e.target.checked })} />
              <button type="button" className="wb-bp-loc truncate" title={b.message ?? b.path} onClick={() => ws.open(b.path, { line: b.line, source: true })}>
                <span className={`wb-bp-dot ${b.logMessage ? 'is-log' : ''} ${b.condition || b.hitCondition ? 'is-cond' : ''} ${b.verified === false ? 'is-unverified' : ''}`} aria-hidden="true" />
                {nameOf(b.path)}
                <span className="wb-result-dir tabular"> {b.line}</span>
                {b.condition && <span className="wb-result-dir mono"> {b.condition}</span>}
              </button>
              <button type="button" className="wb-watch-remove" aria-label={`Edit the breakpoint at ${nameOf(b.path)}:${b.line}`} title="Condition, hit count, log message" onClick={() => dbg.editBreakpoint(b.path, b.line)}>
                <Icon name="pencil" />
              </button>
            </div>
          ))}
          {!dbg.breakpoints.length && <div className="wb-row-msg">Click left of a line number, or press F9.</div>}
        </Section>
      </div>
    </div>
  );
}

/** Continue, step and stop, over the editors while a session runs. */
export function DebugToolbar({ ws }: { ws: Workspace }) {
  useWs(ws);
  const dbg = ws.debug;
  if (!dbg.active) return null;
  const stopped = dbg.state === 'stopped';
  return (
    <div className="wb-dbg-bar" role="toolbar" aria-label="Debug">
      {stopped ? (
        <Button size="sm" variant="ghost" iconOnly icon="play" aria-label="Continue (F5)" title="Continue · F5" onClick={() => dbg.continue()} />
      ) : (
        <Button size="sm" variant="ghost" iconOnly icon="pause" aria-label="Pause (F6)" title="Pause · F6" disabled={dbg.state !== 'running'} onClick={() => dbg.pause()} />
      )}
      <Button size="sm" variant="ghost" iconOnly icon="arrowRight" aria-label="Step over (F10)" title="Step over · F10" disabled={!stopped} onClick={() => dbg.stepOver()} />
      <Button size="sm" variant="ghost" iconOnly icon="arrowDown" aria-label="Step into (F11)" title="Step into · F11" disabled={!stopped} onClick={() => dbg.stepIn()} />
      <Button size="sm" variant="ghost" iconOnly icon="arrowUp" aria-label="Step out (Shift+F11)" title="Step out · Shift+F11" disabled={!stopped} onClick={() => dbg.stepOut()} />
      <Button size="sm" variant="ghost" iconOnly icon="restart" aria-label="Restart (Ctrl+Shift+F5)" title="Restart · Ctrl+Shift+F5" onClick={() => void dbg.restart()} />
      <Button
        size="sm"
        variant="ghost"
        iconOnly
        icon="stop"
        aria-label={dbg.attached ? 'Detach (Shift+F5)' : 'Stop (Shift+F5)'}
        title={dbg.attached ? 'Detach, the program keeps running · Shift+F5' : 'Stop · Shift+F5'}
        onClick={() => void dbg.stop()}
      />
    </div>
  );
}

export function DebugDialogs({ ws }: { ws: Workspace }) {
  useWs(ws);
  const b = ws.debug.editing;
  return b ? <BreakpointDialog key={b.id} dbg={ws.debug} b={b} /> : null;
}

export function DebugConsole({ ws }: { ws: Workspace }) {
  const dbg = ws.debug;
  const ref = useRef<HTMLDivElement>(null);
  const [text, setText] = useState('');
  const history = useRef<string[]>([]);
  const at = useRef(-1);
  useEffect(() => {
    const el = ref.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [dbg.console.length]);
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
      e.preventDefault();
      const h = history.current;
      at.current = Math.max(-1, Math.min(h.length - 1, at.current + (e.key === 'ArrowUp' ? 1 : -1)));
      setText(at.current < 0 ? '' : (h[h.length - 1 - at.current] ?? ''));
    }
  };
  return (
    <div className="wb-dbg-console">
      <div className="wb-dbg-output nowheel" ref={ref} role="log" aria-label="Debug console">
        {!dbg.console.length && <p className="wb-panel-msg">{dbg.active ? 'Waiting for output…' : 'Output of the program you debug appears here. Start with F5.'}</p>}
        {dbg.console.map((l) => (
          <pre key={l.id} className={`wb-dbg-line is-${l.category}`}>
            {l.category === 'input' ? `› ${l.text}` : l.text}
          </pre>
        ))}
      </div>
      <form
        className="wb-dbg-input"
        onSubmit={(e) => {
          e.preventDefault();
          const t = text.trim();
          if (!t) return;
          history.current = [...history.current.filter((h) => h !== t), t];
          at.current = -1;
          setText('');
          void dbg.repl(t);
        }}
      >
        <span aria-hidden="true">›</span>
        <input aria-label="Evaluate in the debugger" spellCheck={false} placeholder={dbg.state === 'stopped' ? 'Evaluate an expression' : 'Available while paused'} disabled={!dbg.active} value={text} onChange={(e) => setText(e.target.value)} onKeyDown={onKey} />
      </form>
    </div>
  );
}

type Editor = monaco.editor.IStandaloneCodeEditor;

/** Dots, the stopped line, clicks in the glyph margin. */
export function useDebugEditor(edRef: RefObject<Editor | null>, ws: Workspace, path: string, model: monaco.editor.ITextModel | null) {
  const pathRef = useRef(path);
  pathRef.current = path;
  const shown = useRef<number[]>([]);
  const coll = useRef<monaco.editor.IEditorDecorationsCollection | null>(null);
  const dbg = ws.debug;

  useEffect(() => {
    const ed = edRef.current;
    if (!ed) return;
    coll.current = ed.createDecorationsCollection();
    const hint = ed.createDecorationsCollection();
    const subs = [
      ed.onMouseDown((e) => {
        if (e.target.type !== monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN) return;
        const line = e.target.position?.lineNumber;
        if (!line) return;
        if (e.event.rightButton) dbg.editBreakpoint(pathRef.current, line);
        else dbg.toggleBreakpoint(pathRef.current, line);
      }),
      ed.onMouseMove((e) => {
        const line = e.target.type === monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN ? e.target.position?.lineNumber : undefined;
        if (line && !dbg.breakpoints.some((b) => b.path === pathRef.current && b.line === line)) {
          hint.set([{ range: new monaco.Range(line, 1, line, 1), options: { glyphMarginClassName: 'wb-bp-hint' } }]);
        } else {
          hint.clear();
        }
      }),
      ed.onMouseLeave(() => hint.clear()),
      // a breakpoint stays on its code when lines are added above it
      ed.onDidChangeModelContent(() => {
        const ranges = coll.current?.getRanges() ?? [];
        const moves = shown.current.map((id, i) => (ranges[i] ? { id, line: ranges[i].startLineNumber } : null)).filter((m): m is { id: number; line: number } => !!m && m.id > 0);
        if (moves.length) dbg.moveBreakpoints(pathRef.current, moves);
      }),
    ];
    return () => {
      subs.forEach((s) => s.dispose());
      coll.current?.clear();
      hint.clear();
      coll.current = null;
    };
    // the editor is created once for the group
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [edRef, ws]);

  const list = dbg.breakpointsFor(path);
  const key = list.map((b) => `${b.id}:${b.line}:${b.enabled}:${b.verified}:${b.condition ?? ''}:${b.hitCondition ?? ''}:${b.logMessage ?? ''}`).join('|');
  const here = dbg.current?.path === path ? dbg.current.line : 0;
  useEffect(() => {
    const c = coll.current;
    if (!c || !model) return;
    const decos: monaco.editor.IModelDeltaDecoration[] = [];
    const ids: number[] = [];
    const max = model.getLineCount();
    for (const b of list) {
      if (b.line > max) continue;
      const cls = !b.enabled ? 'wb-bp is-disabled' : b.verified === false ? 'wb-bp is-unverified' : b.logMessage ? 'wb-bp is-log' : b.condition || b.hitCondition ? 'wb-bp is-cond' : 'wb-bp';
      ids.push(b.id);
      decos.push({
        range: new monaco.Range(b.line, 1, b.line, 1),
        options: {
          glyphMarginClassName: cls,
          glyphMarginHoverMessage: { value: b.message ?? (b.logMessage ? `Log: ${b.logMessage}` : b.condition ? `Stop when: ${b.condition}` : 'Breakpoint') },
          stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
        },
      });
    }
    if (here && here <= max) {
      ids.push(-1);
      decos.push({
        range: new monaco.Range(here, 1, here, 1),
        options: {
          isWholeLine: true,
          className: 'wb-debug-line',
          glyphMarginClassName: 'wb-debug-arrow',
          overviewRuler: { color: '#e89558', position: monaco.editor.OverviewRulerLane.Full },
        },
      });
    }
    shown.current = ids;
    c.set(decos);
    // list is summed up by key
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, here, model]);
}

let hoverDone = false;
let hoverDebugger: Debugger | null = null;

/** Values under the mouse while paused, in any language. */
export function registerDebugHover(dbg: Debugger) {
  hoverDebugger = dbg;
  if (hoverDone) return;
  hoverDone = true;
  monaco.languages.registerHoverProvider('*', {
    async provideHover(model, position) {
      const d = hoverDebugger;
      if (!d || d.state !== 'stopped') return null;
      const word = model.getWordAtPosition(position);
      if (!word) return null;
      // "a.b.c" as far as the mouse is
      const line = model.getLineContent(position.lineNumber);
      let start = word.startColumn - 1;
      while (start > 1 && line[start - 1] === '.' && /[\w$\]]/.test(line[start - 2] ?? '')) {
        let s = start - 2;
        while (s > 0 && /[\w$]/.test(line[s - 1])) s--;
        start = s;
      }
      const expr = line.slice(start, word.endColumn - 1);
      const value = await d.hover(expr);
      if (value === null) return null;
      return {
        range: new monaco.Range(position.lineNumber, start + 1, position.lineNumber, word.endColumn),
        contents: [{ value: `\`${expr}\` = \`${value.replace(/`/g, "'")}\`` }],
      };
    },
  });
}
