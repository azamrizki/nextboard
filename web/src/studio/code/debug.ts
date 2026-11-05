// Debugger of the Code workbench (docs/API-studio.md §7.5): a Debug Adapter Protocol client
// over the hub's WebSocket bridge, plus breakpoints, launch configurations from
// .vscode/launch.json, the call stack, variables, watches and the debug console.

import { api, errorMessage, wsUrl } from '../../api';
import { toast } from '../../components/Toast';
import { prefs } from '../../lib/format';
import { filesApi, type DebugAdapterInfo } from '../api';
import { parseJsonc } from './jsonc';
import { dirOf, extOf, nameOf, type Workspace } from './workspace';

type Json = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

export interface Breakpoint {
  id: number;
  path: string;
  line: number;
  enabled: boolean;
  condition?: string;
  hitCondition?: string;
  logMessage?: string;
  verified?: boolean;
  message?: string;
  adapterId?: number;
}

export interface LaunchConfig {
  name: string;
  type: string;
  request: string;
  [key: string]: unknown;
}

export interface DapVariable {
  key: string;
  name: string;
  value: string;
  type?: string;
  ref: number;
  open: boolean;
  children: DapVariable[] | null;
}

export interface DapScope {
  name: string;
  ref: number;
  open: boolean;
  vars: DapVariable[] | null;
}

export interface DapFrame {
  id: number;
  name: string;
  line: number;
  column: number;
  path?: string;
  label?: string;
}

export interface Watch {
  expr: string;
  value?: string;
  error?: boolean;
  variable?: DapVariable;
}

export interface ConsoleLine {
  id: number;
  category: string;
  text: string;
}

export type DebugState = 'inactive' | 'starting' | 'running' | 'stopped';

const MAX_CONSOLE = 2000;

/** One connection to one adapter. */
class DapClient {
  private ws: WebSocket;
  private seq = 1;
  private pending = new Map<number, { resolve: (v: Json) => void; reject: (e: Error) => void }>();

  constructor(
    url: string,
    private on: { open: () => void; event: (e: Json) => void; close: (reason: string) => void },
  ) {
    this.ws = new WebSocket(url);
    this.ws.onopen = () => this.on.open();
    this.ws.onmessage = (ev) => this.onMessage(String(ev.data));
    this.ws.onclose = (ev) => {
      for (const p of this.pending.values()) p.reject(new Error('The debug session ended.'));
      this.pending.clear();
      this.on.close(ev.reason);
    };
  }

  request(command: string, args?: unknown): Promise<Json> {
    const seq = this.seq++;
    return new Promise((resolve, reject) => {
      if (this.ws.readyState !== WebSocket.OPEN) {
        reject(new Error('The debug session ended.'));
        return;
      }
      this.pending.set(seq, { resolve, reject });
      this.ws.send(JSON.stringify({ seq, type: 'request', command, arguments: args }));
    });
  }

  close() {
    try {
      this.ws.close();
    } catch {
      /* ignore */
    }
  }

  private onMessage(raw: string) {
    let msg: Json;
    try {
      msg = JSON.parse(raw);
    } catch {
      return;
    }
    if (msg.type === 'event') {
      this.on.event(msg);
    } else if (msg.type === 'response') {
      const p = this.pending.get(msg.request_seq);
      if (!p) return;
      this.pending.delete(msg.request_seq);
      if (msg.success) p.resolve(msg.body ?? {});
      else p.reject(new Error(msg.body?.error?.format ?? msg.message ?? 'The debugger refused.'));
    } else if (msg.type === 'request') {
      // reverse requests (a terminal for the program, child sessions) are not offered
      this.ws.send(JSON.stringify({ seq: this.seq++, type: 'response', request_seq: msg.seq, command: msg.command, success: false, message: 'Not supported' }));
    }
  }
}

interface Saved {
  breakpoints: Breakpoint[];
  watches: string[];
  config: string;
  filters: string[];
}

export class Debugger {
  adapters: DebugAdapterInfo[] = [];
  breakpoints: Breakpoint[] = [];
  state: DebugState = 'inactive';
  configName = '';
  /** Why the last session could not start, with what would fix it. */
  notice: { text: string; install?: string } | null = null;
  capabilities: Json = {};
  filters: { filter: string; label: string; default?: boolean }[] = [];
  enabledFilters = new Set<string>();
  threads: { id: number; name: string }[] = [];
  threadId = 0;
  frames: DapFrame[] = [];
  frameId = 0;
  scopes: DapScope[] = [];
  watches: Watch[] = [];
  console: ConsoleLine[] = [];
  /** Where execution is stopped, for the editor's highlight. */
  current: { path: string; line: number } | null = null;
  /** The breakpoint being edited in the dialog. */
  editing: Breakpoint | null = null;

  private client: DapClient | null = null;
  private lastConfig: LaunchConfig | null = null;
  private lastNoDebug = false;
  private nextId = 1;
  private lineId = 1;
  private configured = false;
  private restarting = false;
  /** The session was told to end, or said it would: its closing is no failure. */
  private ending = false;

  constructor(private ws: Workspace) {
    try {
      const raw = prefs.get(`wb.debug.${ws.pid}`);
      const s = raw ? (JSON.parse(raw) as Partial<Saved>) : {};
      this.breakpoints = (s.breakpoints ?? []).map((b) => ({ ...b, verified: undefined, adapterId: undefined }));
      this.nextId = this.breakpoints.reduce((m, b) => Math.max(m, b.id + 1), 1);
      this.watches = (s.watches ?? []).map((expr) => ({ expr }));
      this.configName = s.config ?? '';
      this.enabledFilters = new Set(s.filters ?? []);
    } catch {
      /* start empty */
    }
  }

  private changed() {
    const s: Saved = { breakpoints: this.breakpoints, watches: this.watches.map((w) => w.expr), config: this.configName, filters: [...this.enabledFilters] };
    prefs.set(`wb.debug.${this.ws.pid}`, JSON.stringify(s));
    this.ws.changed();
  }

  get active() {
    return this.state !== 'inactive';
  }

  async refreshAdapters() {
    try {
      this.adapters = (await filesApi.dapStatus(this.ws.pid)).items ?? [];
    } catch {
      this.adapters = [];
    }
    this.ws.changed();
  }

  // ---- configurations ----------------------------------------------------------------

  /** .vscode/launch.json first, then what fits the active file. */
  async configurations(): Promise<LaunchConfig[]> {
    const out: LaunchConfig[] = [];
    try {
      const file = this.ws.abs('.vscode/launch.json');
      const st = await api.fsStat(this.ws.mid, file);
      if (st.exists === false) return this.autoConfigs([]);
      const r = await api.fsRead(this.ws.mid, file);
      const list = (parseJsonc(r.content) as { configurations?: LaunchConfig[] }).configurations ?? [];
      for (const c of list) if (c?.name && c.type && c.request) out.push(c);
    } catch {
      /* no launch.json: the automatic entries below are enough */
    }
    return [...out, ...this.autoConfigs(out)];
  }

  private autoConfigs(existing: LaunchConfig[]): LaunchConfig[] {
    const path = this.ws.activeDoc?.path;
    if (!path) return [];
    const ext = extOf(path);
    const add: LaunchConfig[] = [];
    if (ext === 'go') {
      const test = path.endsWith('_test.go');
      add.push({ name: test ? 'Debug tests in this package' : 'Debug this package', type: 'go', request: 'launch', mode: test ? 'test' : 'debug', program: '${fileDirname}', cwd: '${workspaceFolder}' });
    } else if (ext === 'py') {
      add.push({ name: 'Debug this Python file', type: 'python', request: 'launch', program: '${file}', cwd: '${workspaceFolder}', console: 'internalConsole', justMyCode: true });
    }
    if (['js', 'mjs', 'cjs', 'ts', 'mts', 'cts'].includes(ext)) {
      add.push({ name: 'Debug this file with Node.js', type: 'node', request: 'launch', program: '${file}', cwd: '${workspaceFolder}' });
    }
    return add.filter((a) => !existing.some((e) => e.name === a.name));
  }

  private adapterFor(type: string): DebugAdapterInfo | undefined {
    return this.adapters.find((a) => a.types.includes(type));
  }

  private substitute(v: unknown): unknown {
    if (typeof v === 'string') {
      const path = this.ws.activeDoc?.path ?? '';
      const base = nameOf(path);
      const noExt = base.includes('.') ? base.slice(0, base.lastIndexOf('.')) : base;
      return v
        .replaceAll('${workspaceFolder}', this.ws.root)
        .replaceAll('${workspaceFolderBasename}', nameOf(this.ws.root))
        .replaceAll('${file}', path)
        .replaceAll('${relativeFile}', path ? this.ws.rel(path) : '')
        .replaceAll('${fileDirname}', path ? dirOf(path) : '')
        .replaceAll('${fileBasename}', base)
        .replaceAll('${fileBasenameNoExtension}', noExt);
    }
    if (Array.isArray(v)) return v.map((x) => this.substitute(x));
    if (v && typeof v === 'object') return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, this.substitute(x)]));
    return v;
  }

  /** Open .vscode/launch.json, writing a starting point when there is none. */
  async openLaunchJson() {
    const path = this.ws.abs('.vscode/launch.json');
    try {
      const st = await api.fsStat(this.ws.mid, path);
      if (st.exists === false) {
        await this.ws.run((c) => this.ws.writeNew(path, LAUNCH_TEMPLATE, c));
        this.ws.refreshTree();
      }
      this.ws.open(path, { pin: true });
    } catch (err) {
      toast.error(`Could not open launch.json: ${errorMessage(err)}`);
    }
  }

  // ---- session -----------------------------------------------------------------------

  log(category: string, text: string) {
    if (!text) return;
    const line = { id: this.lineId++, category, text };
    this.console = [...this.console.slice(-(MAX_CONSOLE - 1)), line];
    this.ws.changed();
  }

  async start(config: LaunchConfig, noDebug = false) {
    if (this.active) return;
    this.notice = null;
    if (!this.adapters.length) await this.refreshAdapters();
    const adapter = this.adapterFor(config.type);
    if (!adapter) {
      this.notice = { text: `No debugger here handles the type “${config.type}”. This hub knows: ${this.adapters.flatMap((a) => a.types).join(', ')}.` };
      this.ws.changed();
      return;
    }
    if (!adapter.available) {
      this.notice = { text: `${adapter.name} is not installed on this machine.`, install: adapter.install };
      this.ws.changed();
      return;
    }
    await this.ws.saveAll();
    this.lastConfig = config;
    this.lastNoDebug = noDebug;
    this.configName = config.name;
    this.state = 'starting';
    this.console = [];
    this.configured = false;
    this.threads = [];
    this.frames = [];
    this.scopes = [];
    this.current = null;
    this.ws.panelOpen = true;
    this.ws.panelView = 'debug';
    this.changed();
    const url = wsUrl(`/api/projects/${encodeURIComponent(this.ws.pid)}/dap/${encodeURIComponent(adapter.id)}/ws`);
    this.client = new DapClient(url, {
      open: () => void this.handshake(config, adapter, noDebug),
      event: (e) => this.onEvent(e),
      close: (reason) => this.onClose(reason),
    });
  }

  private async handshake(config: LaunchConfig, adapter: DebugAdapterInfo, noDebug: boolean) {
    const c = this.client;
    if (!c) return;
    try {
      this.capabilities = await c.request('initialize', {
        clientID: 'nextboard',
        clientName: 'NextBoard',
        adapterID: adapter.id,
        pathFormat: 'path',
        linesStartAt1: true,
        columnsStartAt1: true,
        supportsVariableType: true,
        supportsVariablePaging: false,
        supportsRunInTerminalRequest: false,
        supportsStartDebuggingRequest: false,
        locale: 'en',
      });
      this.filters = this.capabilities.exceptionBreakpointFilters ?? [];
      for (const f of this.filters) if (f.default && !this.enabledFilters.has(f.filter)) this.enabledFilters.add(f.filter);
      const args = this.substitute({ ...config }) as Json;
      if (noDebug) args.noDebug = true;
      // Delve would otherwise print to its own stdout, out of the console's reach.
      if (config.type === 'go' && args.outputMode === undefined) args.outputMode = 'remote';
      // Not awaited: some adapters answer only after configurationDone.
      c.request(config.request === 'attach' ? 'attach' : 'launch', args).catch((err) => {
        this.log('stderr', `${errorMessage(err)}\n`);
        void this.stop();
      });
      this.state = 'running';
      this.ws.changed();
    } catch (err) {
      this.log('stderr', `${errorMessage(err)}\n`);
      void this.stop();
    }
  }

  private async configure() {
    const c = this.client;
    if (!c || this.configured) return;
    this.configured = true;
    const files = new Set(this.breakpoints.map((b) => b.path));
    await Promise.all([...files].map((p) => this.sendBreakpoints(p)));
    if (this.filters.length) {
      await c.request('setExceptionBreakpoints', { filters: this.filters.filter((f) => this.enabledFilters.has(f.filter)).map((f) => f.filter) }).catch(() => undefined);
    }
    if (this.capabilities.supportsConfigurationDoneRequest) await c.request('configurationDone').catch(() => undefined);
    this.ws.changed();
  }

  private onEvent(e: Json) {
    const b = e.body ?? {};
    switch (e.event) {
      case 'initialized':
        void this.configure();
        break;
      case 'output':
        if (b.category !== 'telemetry') this.log(b.category ?? 'console', b.output ?? '');
        break;
      case 'stopped':
        void this.onStopped(b);
        break;
      case 'continued':
        this.resumed();
        break;
      case 'breakpoint':
        this.onBreakpointEvent(b.breakpoint);
        break;
      case 'terminated':
        this.ending = true;
        if (b.restart === undefined) void this.stop();
        break;
      case 'exited':
        this.log('console', `The program exited with code ${b.exitCode}.\n`);
        break;
    }
  }

  private onClose(reason: string) {
    if (reason && this.state !== 'inactive' && !this.ending) this.log('stderr', `${reason}\n`);
    this.ending = false;
    this.client = null;
    this.state = 'inactive';
    this.threads = [];
    this.frames = [];
    this.scopes = [];
    this.current = null;
    for (const b of this.breakpoints) {
      b.verified = undefined;
      b.adapterId = undefined;
      b.message = undefined;
    }
    for (const w of this.watches) {
      w.value = undefined;
      w.variable = undefined;
      w.error = undefined;
    }
    this.ws.changed();
    if (this.restarting) {
      this.restarting = false;
      if (this.lastConfig) void this.start(this.lastConfig, this.lastNoDebug);
    }
  }

  async stop() {
    const c = this.client;
    if (!c) return;
    this.ending = true;
    try {
      await Promise.race([c.request('disconnect', { terminateDebuggee: true, restart: false }), new Promise((r) => window.setTimeout(r, 1500))]);
    } catch {
      /* it is ending anyway */
    }
    c.close();
  }

  async restart() {
    if (!this.client) return;
    this.restarting = true;
    await this.stop();
  }

  // ---- stepping ----------------------------------------------------------------------

  private resumed() {
    this.state = 'running';
    this.frames = [];
    this.scopes = [];
    this.current = null;
    this.ws.changed();
  }

  private step(command: string) {
    const c = this.client;
    if (!c || this.state !== 'stopped') return;
    const threadId = this.threadId;
    this.resumed();
    void c.request(command, { threadId }).catch((err) => this.log('stderr', `${errorMessage(err)}\n`));
  }
  continue() {
    this.step('continue');
  }
  stepOver() {
    this.step('next');
  }
  stepIn() {
    this.step('stepIn');
  }
  stepOut() {
    this.step('stepOut');
  }
  pause() {
    if (this.state !== 'running') return;
    void this.client?.request('pause', { threadId: this.threadId || this.threads[0]?.id || 1 }).catch(() => undefined);
  }

  private async onStopped(b: Json) {
    const c = this.client;
    if (!c) return;
    this.state = 'stopped';
    const t = await c.request('threads').catch(() => ({ threads: [] as { id: number; name: string }[] }));
    this.threads = t.threads ?? [];
    this.threadId = b.threadId ?? this.threadId ?? this.threads[0]?.id ?? 0;
    if (b.reason === 'exception' && (b.description || b.text)) this.log('stderr', `Exception: ${b.description ?? ''} ${b.text ?? ''}\n`);
    await this.loadStack(this.threadId);
  }

  async selectThread(id: number) {
    this.threadId = id;
    await this.loadStack(id);
  }

  private async loadStack(threadId: number) {
    const c = this.client;
    if (!c) return;
    try {
      const s = await c.request('stackTrace', { threadId, startFrame: 0, levels: 100 });
      this.frames = (s.stackFrames ?? []).map((f: Json) => {
        const raw: string | undefined = f.source?.path;
        const path = raw && !raw.startsWith('/') ? `${this.ws.root}/${raw}` : raw;
        return { id: f.id, name: f.name, line: f.line, column: f.column, path, label: f.source?.name };
      });
      const first = this.frames.find((f) => f.path) ?? this.frames[0];
      if (first) await this.selectFrame(first);
      else this.ws.changed();
    } catch (err) {
      this.log('stderr', `${errorMessage(err)}\n`);
    }
  }

  /** Show a frame: its line in the editor, its variables, the watches in its context. */
  async selectFrame(f: DapFrame) {
    const c = this.client;
    if (!c) return;
    this.frameId = f.id;
    if (f.path) {
      this.current = { path: f.path, line: f.line };
      this.ws.open(f.path, { line: f.line, col: f.column || 1, source: true });
    } else {
      this.current = null;
    }
    this.ws.changed();
    try {
      const r = await c.request('scopes', { frameId: f.id });
      this.scopes = (r.scopes ?? []).map((s: Json, i: number) => ({ name: s.name, ref: s.variablesReference, open: !s.expensive && i === 0, vars: null }));
      await Promise.all(this.scopes.filter((s) => s.open).map((s) => this.loadScope(s)));
    } catch {
      this.scopes = [];
    }
    await this.refreshWatches();
    this.ws.changed();
  }

  private toVar(v: Json, parent: string): DapVariable {
    return { key: `${parent}/${v.name}`, name: v.name, value: v.value ?? '', type: v.type, ref: v.variablesReference ?? 0, open: false, children: null };
  }

  private async loadScope(s: DapScope) {
    const r = await this.client?.request('variables', { variablesReference: s.ref }).catch(() => null);
    s.vars = (r?.variables ?? []).map((v: Json) => this.toVar(v, s.name));
  }

  async toggleScope(s: DapScope) {
    s.open = !s.open;
    if (s.open && !s.vars) await this.loadScope(s);
    this.ws.changed();
  }

  async toggleVar(v: DapVariable) {
    v.open = !v.open;
    if (v.open && !v.children && v.ref) {
      const r = await this.client?.request('variables', { variablesReference: v.ref }).catch(() => null);
      v.children = (r?.variables ?? []).map((c: Json) => this.toVar(c, v.key));
    }
    this.ws.changed();
  }

  // ---- watches, console, hover -------------------------------------------------------

  private async evaluate(expression: string, context: string): Promise<Json> {
    if (!this.client) throw new Error('No debug session.');
    return this.client.request('evaluate', { expression, context, frameId: this.frameId || undefined });
  }

  addWatch(expr: string) {
    const e = expr.trim();
    if (!e || this.watches.some((w) => w.expr === e)) return;
    this.watches = [...this.watches, { expr: e }];
    this.changed();
    void this.refreshWatches();
  }

  removeWatch(expr: string) {
    this.watches = this.watches.filter((w) => w.expr !== expr);
    this.changed();
  }

  private async refreshWatches() {
    if (this.state !== 'stopped') return;
    await Promise.all(
      this.watches.map(async (w) => {
        try {
          const r = await this.evaluate(w.expr, 'watch');
          w.value = r.result;
          w.error = false;
          w.variable = r.variablesReference ? { key: `watch/${w.expr}`, name: w.expr, value: r.result, type: r.type, ref: r.variablesReference, open: false, children: null } : undefined;
        } catch (err) {
          w.value = errorMessage(err);
          w.error = true;
          w.variable = undefined;
        }
      }),
    );
    this.ws.changed();
  }

  async repl(expr: string) {
    this.log('input', `${expr}\n`);
    try {
      const r = await this.evaluate(expr, 'repl');
      this.log('result', `${r.result}\n`);
      await this.refreshWatches();
    } catch (err) {
      this.log('stderr', `${errorMessage(err)}\n`);
    }
  }

  /** The value of an expression under the mouse, when stopped. */
  async hover(expr: string): Promise<string | null> {
    if (this.state !== 'stopped') return null;
    try {
      const r = await this.evaluate(expr, 'hover');
      return r.result ?? null;
    } catch {
      return null;
    }
  }

  // ---- breakpoints -------------------------------------------------------------------

  breakpointsFor(path: string) {
    return this.breakpoints.filter((b) => b.path === path);
  }

  toggleBreakpoint(path: string, line: number) {
    const existing = this.breakpoints.find((b) => b.path === path && b.line === line);
    if (existing) this.breakpoints = this.breakpoints.filter((b) => b !== existing);
    else this.breakpoints = [...this.breakpoints, { id: this.nextId++, path, line, enabled: true }];
    this.changed();
    void this.sendBreakpoints(path);
  }

  /** The breakpoint on a line, made when there is none, for the dialog. */
  editBreakpoint(path: string, line: number) {
    let b = this.breakpoints.find((x) => x.path === path && x.line === line);
    if (!b) {
      b = { id: this.nextId++, path, line, enabled: true };
      this.breakpoints = [...this.breakpoints, b];
    }
    this.editing = b;
    this.changed();
  }

  updateBreakpoint(b: Breakpoint, patch: Partial<Breakpoint>) {
    Object.assign(b, patch);
    this.changed();
    void this.sendBreakpoints(b.path);
  }

  removeBreakpoint(b: Breakpoint) {
    this.breakpoints = this.breakpoints.filter((x) => x !== b);
    this.changed();
    void this.sendBreakpoints(b.path);
  }

  removeAllBreakpoints() {
    const files = new Set(this.breakpoints.map((b) => b.path));
    this.breakpoints = [];
    this.changed();
    for (const p of files) void this.sendBreakpoints(p);
  }

  /** Lines moved because of edits above them. */
  moveBreakpoints(path: string, moves: { id: number; line: number }[]) {
    let any = false;
    for (const m of moves) {
      const b = this.breakpoints.find((x) => x.id === m.id && x.path === path);
      if (b && b.line !== m.line) {
        b.line = m.line;
        any = true;
      }
    }
    if (any) this.changed();
  }

  fileRenamed(from: string, to: string) {
    let any = false;
    for (const b of this.breakpoints) {
      if (b.path === from || b.path.startsWith(`${from}/`)) {
        b.path = to + b.path.slice(from.length);
        any = true;
      }
    }
    if (any) this.changed();
  }

  private async sendBreakpoints(path: string) {
    const c = this.client;
    if (!c || !this.configured) return;
    const list = this.breakpointsFor(path).filter((b) => b.enabled);
    try {
      const r = await c.request('setBreakpoints', {
        source: { path, name: nameOf(path) },
        breakpoints: list.map((b) => ({ line: b.line, condition: b.condition || undefined, hitCondition: b.hitCondition || undefined, logMessage: b.logMessage || undefined })),
        sourceModified: false,
      });
      (r.breakpoints ?? []).forEach((res: Json, i: number) => {
        const b = list[i];
        if (!b) return;
        b.verified = !!res.verified;
        b.adapterId = res.id;
        b.message = res.message;
        if (res.line) b.line = res.line;
      });
      this.ws.changed();
    } catch (err) {
      this.log('stderr', `Breakpoints in ${nameOf(path)}: ${errorMessage(err)}\n`);
    }
  }

  private onBreakpointEvent(res: Json | undefined) {
    if (!res?.id) return;
    const b = this.breakpoints.find((x) => x.adapterId === res.id);
    if (!b) return;
    b.verified = !!res.verified;
    if (res.line) b.line = res.line;
    b.message = res.message;
    this.ws.changed();
  }

  setFilter(id: string, on: boolean) {
    if (on) this.enabledFilters.add(id);
    else this.enabledFilters.delete(id);
    this.changed();
    if (this.client && this.configured) {
      void this.client.request('setExceptionBreakpoints', { filters: this.filters.filter((f) => this.enabledFilters.has(f.filter)).map((f) => f.filter) }).catch(() => undefined);
    }
  }
}

const LAUNCH_TEMPLATE = `{
  // Debug configurations, in the format of VS Code.
  // Types here: go (Delve), python (debugpy), node, lldb or gdb (C, C++, Rust), php.
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Debug the project",
      "type": "go",
      "request": "launch",
      "mode": "debug",
      "program": "\${workspaceFolder}",
      "args": []
    }
  ]
}
`;
