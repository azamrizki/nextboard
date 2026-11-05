// Language Server Protocol client of the Code workbench (docs/API-studio.md §7.3). The hub
// starts the machine's own server in the project folder and bridges its stdio to a
// WebSocket, one JSON-RPC message per text frame. Providers are registered per language
// once and dispatch to the connection of the workspace that owns the model.

import { wsUrl } from '../../api';
import { toast } from '../../components/Toast';
import { filesApi, type LspServer } from '../api';
import { disableBuiltinTypescript, monaco } from './monaco';
import { hasNative, onNativeChange } from './nativeExt';

type Model = monaco.editor.ITextModel;
type Json = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

/** What the client needs from the workspace. */
export interface LspHost {
  projectId: string;
  root: string;
  /** The model of a file (loaded when needed), or null when it cannot be read. */
  ensureModel(path: string): Promise<Model | null>;
  log(line: string): void;
  changed(): void;
}

export interface LspState {
  state: 'none' | 'builtin' | 'missing' | 'starting' | 'ready' | 'failed';
  name: string;
  text: string;
  install: string;
}

const fromLspRange = (r: Json): monaco.IRange => ({
  startLineNumber: r.start.line + 1,
  startColumn: r.start.character + 1,
  endLineNumber: r.end.line + 1,
  endColumn: r.end.character + 1,
});
const toLspPos = (p: monaco.IPosition) => ({ line: p.lineNumber - 1, character: p.column - 1 });
const toLspRange = (r: monaco.IRange) => ({
  start: { line: r.startLineNumber - 1, character: r.startColumn - 1 },
  end: { line: r.endLineNumber - 1, character: r.endColumn - 1 },
});
const isFileUri = (uri: string) => uri.startsWith('file:');
const pathOf = (uri: string) => monaco.Uri.parse(uri).path;
const docId = (model: Model) => ({ uri: model.uri.toString() });

const k = () => monaco.languages.CompletionItemKind;
const completionKind = (n?: number): monaco.languages.CompletionItemKind => {
  const K = k();
  const map = [K.Text, K.Text, K.Method, K.Function, K.Constructor, K.Field, K.Variable, K.Class, K.Interface, K.Module, K.Property, K.Unit, K.Value, K.Enum, K.Keyword, K.Snippet, K.Color, K.File, K.Reference, K.Folder, K.EnumMember, K.Constant, K.Struct, K.Event, K.Operator, K.TypeParameter];
  return map[n ?? 1] ?? K.Text;
};

const SEVERITY = () => [monaco.MarkerSeverity.Error, monaco.MarkerSeverity.Error, monaco.MarkerSeverity.Warning, monaco.MarkerSeverity.Info, monaco.MarkerSeverity.Hint];

function markdownOf(c: unknown): monaco.IMarkdownString[] {
  if (!c) return [];
  if (Array.isArray(c)) return c.flatMap(markdownOf);
  if (typeof c === 'string') return c ? [{ value: c }] : [];
  const o = c as Json;
  if (o.language) return [{ value: `\`\`\`${o.language}\n${o.value}\n\`\`\`` }];
  return o.value ? [{ value: String(o.value) }] : [];
}

const CAPABILITIES = {
  general: { positionEncodings: ['utf-16'] },
  textDocument: {
    synchronization: { didSave: true, willSave: false, willSaveWaitUntil: false, dynamicRegistration: false },
    completion: {
      contextSupport: true,
      completionItem: {
        snippetSupport: true,
        commitCharactersSupport: true,
        documentationFormat: ['markdown', 'plaintext'],
        deprecatedSupport: true,
        preselectSupport: true,
        labelDetailsSupport: true,
        tagSupport: { valueSet: [1] },
        resolveSupport: { properties: ['documentation', 'detail', 'additionalTextEdits'] },
      },
    },
    hover: { contentFormat: ['markdown', 'plaintext'] },
    signatureHelp: { signatureInformation: { documentationFormat: ['markdown', 'plaintext'], parameterInformation: { labelOffsetSupport: true }, activeParameterSupport: true }, contextSupport: true },
    definition: { linkSupport: true },
    declaration: { linkSupport: true },
    typeDefinition: { linkSupport: true },
    implementation: { linkSupport: true },
    references: {},
    documentHighlight: {},
    documentSymbol: { hierarchicalDocumentSymbolSupport: true, symbolKind: { valueSet: Array.from({ length: 26 }, (_, i) => i + 1) } },
    codeAction: {
      isPreferredSupport: true,
      dataSupport: true,
      resolveSupport: { properties: ['edit'] },
      codeActionLiteralSupport: { codeActionKind: { valueSet: ['', 'quickfix', 'refactor', 'refactor.extract', 'refactor.inline', 'refactor.rewrite', 'source', 'source.organizeImports', 'source.fixAll'] } },
    },
    formatting: {},
    rangeFormatting: {},
    rename: { prepareSupport: true },
    publishDiagnostics: { relatedInformation: true, tagSupport: { valueSet: [1, 2] }, codeDescriptionSupport: true, dataSupport: true },
    inlayHint: { resolveSupport: { properties: [] } },
  },
  workspace: {
    applyEdit: true,
    workspaceEdit: { documentChanges: true },
    configuration: true,
    workspaceFolders: true,
    symbol: { symbolKind: { valueSet: Array.from({ length: 26 }, (_, i) => i + 1) } },
    didChangeConfiguration: {},
  },
  window: { workDoneProgress: true, showMessage: {} },
};

const lspLanguageId = (model: Model) => {
  const id = model.getLanguageId();
  const p = model.uri.path.toLowerCase();
  if (id === 'typescript' && p.endsWith('.tsx')) return 'typescriptreact';
  if (id === 'javascript' && p.endsWith('.jsx')) return 'javascriptreact';
  if (id === 'shell') return 'shellscript';
  return id;
};

/** One running language server. */
class Connection {
  state: 'starting' | 'ready' | 'failed' | 'closed' = 'starting';
  reason = '';
  caps: Json = {};
  /** Features the server registered after initialize. */
  dynamic = new Set<string>();
  private ws: WebSocket;
  private next = 1;
  private pending = new Map<number, { resolve: (v: any) => void; reject: (e: Error) => void }>(); // eslint-disable-line @typescript-eslint/no-explicit-any
  private opened = new Map<string, Model>();
  private progress = new Map<string, string>();
  /** Last diagnostics per uri, including files that have no model. */
  diagnostics = new Map<string, Json[]>();

  constructor(
    private hub: LspHub,
    readonly server: LspServer,
    onFail: (c: Connection) => void,
  ) {
    this.ws = new WebSocket(wsUrl(`/api/projects/${encodeURIComponent(hub.host.projectId)}/lsp/${encodeURIComponent(server.id)}/ws`));
    this.ws.onopen = () => void this.initialize();
    this.ws.onmessage = (ev) => this.onMessage(String(ev.data));
    this.ws.onclose = (ev) => {
      if (this.state === 'closed') return;
      this.state = 'failed';
      this.reason = ev.reason || 'The language server stopped.';
      for (const p of this.pending.values()) p.reject(new Error(this.reason));
      this.pending.clear();
      this.hub.host.log(`[${server.name}] ${this.reason}`);
      this.hub.clearDiagnostics(this);
      onFail(this);
      this.hub.host.changed();
    };
  }

  /** Markers per server: two may speak about one file. */
  get markerOwner() {
    return this.server.extra || this.server.id === 'vue' ? `lsp-${this.server.id}` : 'lsp';
  }

  private get initOptions(): Json {
    if (this.server.id === 'vue') return { typescript: { tsdk: `${this.hub.host.root}/node_modules/typescript/lib` }, vue: { hybridMode: false } };
    const plugin = this.server.id === 'typescript' ? this.hub.tsPlugin : '';
    return plugin ? { plugins: [{ name: '@vue/typescript-plugin', location: plugin, languages: ['vue'] }] } : {};
  }

  close() {
    this.state = 'closed';
    for (const p of this.pending.values()) p.reject(new Error('closed'));
    this.pending.clear();
    this.hub.clearDiagnostics(this);
    try {
      this.ws.close();
    } catch {
      /* ignore */
    }
  }

  private send(msg: Json) {
    if (this.ws.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify({ jsonrpc: '2.0', ...msg }));
  }

  request<T = any>(method: string, params: unknown): Promise<T> { // eslint-disable-line @typescript-eslint/no-explicit-any
    const id = this.next++;
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.send({ id, method, params });
    });
  }

  notify(method: string, params: unknown) {
    this.send({ method, params });
  }

  private async initialize() {
    const root = this.hub.host.root;
    const rootUri = monaco.Uri.file(root).toString();
    try {
      const res = await this.request('initialize', {
        processId: null,
        clientInfo: { name: 'NextBoard' },
        rootPath: root,
        rootUri,
        workspaceFolders: [{ uri: rootUri, name: root.split('/').pop() || root }],
        capabilities: CAPABILITIES,
        initializationOptions: this.initOptions,
      });
      this.caps = res?.capabilities ?? {};
      this.notify('initialized', {});
      this.notify('workspace/didChangeConfiguration', { settings: {} });
      this.state = 'ready';
      for (const m of this.opened.values()) this.sendOpen(m);
      this.hub.host.changed();
    } catch (err) {
      if (this.state !== 'closed') this.hub.host.log(`[${this.server.name}] initialize failed: ${(err as Error).message}`);
    }
  }

  private sendOpen(model: Model) {
    this.notify('textDocument/didOpen', { textDocument: { ...docId(model), languageId: lspLanguageId(model), version: model.getVersionId(), text: model.getValue() } });
  }

  open(model: Model) {
    const uri = model.uri.toString();
    if (this.opened.has(uri)) return;
    this.opened.set(uri, model);
    if (this.state === 'ready') this.sendOpen(model);
  }

  closeDoc(model: Model) {
    if (this.opened.delete(model.uri.toString()) && this.state === 'ready') this.notify('textDocument/didClose', { textDocument: docId(model) });
  }

  change(model: Model, e: monaco.editor.IModelContentChangedEvent) {
    if (this.state !== 'ready' || !this.opened.has(model.uri.toString())) return;
    const sync = this.caps.textDocumentSync;
    const kind = typeof sync === 'number' ? sync : (sync?.change ?? 0);
    if (!kind) return;
    const contentChanges =
      kind === 2 ? e.changes.map((c) => ({ range: toLspRange(c.range), rangeLength: c.rangeLength, text: c.text })) : [{ text: model.getValue() }];
    this.notify('textDocument/didChange', { textDocument: { ...docId(model), version: model.getVersionId() }, contentChanges });
  }

  saved(model: Model) {
    if (this.state !== 'ready' || !this.opened.has(model.uri.toString())) return;
    const sync = this.caps.textDocumentSync;
    if (typeof sync === 'object' && sync?.save) {
      this.notify('textDocument/didSave', { textDocument: docId(model), text: sync.save.includeText ? model.getValue() : undefined });
    }
  }

  has(capability: string, method?: string): boolean {
    return !!this.caps[capability] || (!!method && this.dynamic.has(method));
  }

  get statusText(): string {
    if (this.state === 'starting') return 'Starting…';
    if (this.state === 'failed') return this.reason;
    const title = [...this.progress.values()][0];
    return title ? `${title}…` : 'IntelliSense ready';
  }

  private onMessage(raw: string) {
    let msg: Json;
    try {
      msg = JSON.parse(raw);
    } catch {
      return;
    }
    if (msg.method && msg.id !== undefined) {
      void this.onServerRequest(msg);
      return;
    }
    if (msg.method) {
      this.onNotification(msg.method, msg.params);
      return;
    }
    const p = this.pending.get(msg.id);
    if (!p) return;
    this.pending.delete(msg.id);
    if (msg.error) p.reject(new Error(msg.error.message));
    else p.resolve(msg.result);
  }

  private async onServerRequest(msg: Json) {
    const reply = (result: unknown) => this.send({ id: msg.id, result: result ?? null });
    const p = msg.params ?? {};
    switch (msg.method) {
      case 'workspace/configuration':
        return reply((p.items ?? []).map(() => (this.server.extra ? {} : null)));
      case 'workspace/workspaceFolders': {
        const root = this.hub.host.root;
        return reply([{ uri: monaco.Uri.file(root).toString(), name: root.split('/').pop() || root }]);
      }
      case 'client/registerCapability':
        for (const r of p.registrations ?? []) this.dynamic.add(r.method);
        this.hub.host.changed();
        return reply(null);
      case 'client/unregisterCapability':
        for (const r of p.unregisterations ?? p.unregistrations ?? []) this.dynamic.delete(r.method);
        return reply(null);
      case 'window/workDoneProgress/create':
        return reply(null);
      case 'window/showMessageRequest':
        return reply(null);
      case 'workspace/applyEdit':
        return reply({ applied: await this.hub.applyEdit(p.edit) });
      default:
        this.send({ id: msg.id, error: { code: -32601, message: `Method not handled: ${msg.method}` } });
    }
  }

  private onNotification(method: string, p: Json) {
    switch (method) {
      case 'textDocument/publishDiagnostics':
        this.diagnostics.set(p.uri, p.diagnostics ?? []);
        this.hub.applyDiagnostics(p.uri, p.diagnostics ?? [], this.markerOwner);
        break;
      case '$/progress': {
        const token = String(p.token);
        const v = p.value ?? {};
        if (v.kind === 'begin') this.progress.set(token, v.title || 'Indexing project');
        else if (v.kind === 'end') this.progress.delete(token);
        this.hub.host.changed();
        break;
      }
      case 'window/showMessage':
        if (p.type === 1) toast.error(`${this.server.name}: ${p.message}`);
        this.hub.host.log(`[${this.server.name}] ${p.message}`);
        break;
      case 'window/logMessage':
        this.hub.host.log(`[${this.server.name}] ${p.message}`);
        break;
      case 'tsserver/request':
        this.hub.askTypescript(this, p);
        break;
    }
  }
}

let active: LspHub | null = null;
let providersDone = false;

/** Language servers of one workspace. */
export class LspHub {
  servers: LspServer[] = [];
  readonly conns = new Map<string, Connection>();
  private restarts = new Map<string, number>();
  private models = new Map<string, { model: Model; off: () => void }>();
  private disposed = false;
  private offNative: (() => void) | null = null;
  /** Set once a Vue file needs it: TypeScript's server then starts with the plugin. */
  tsPlugin = '';

  constructor(readonly host: LspHost) {}

  private followExtensions() {
    this.offNative?.();
    this.offNative = onNativeChange(() => this.extrasChanged());
  }

  /** Learn what the machine has, then serve every model already known. */
  async start() {
    active = this;
    this.followExtensions();
    registerProviders();
    try {
      const r = await filesApi.lspStatus(this.host.projectId);
      this.servers = r.items ?? [];
    } catch {
      this.servers = [];
    }
    if (this.disposed) return;
    registerProviders(this.servers.flatMap((s) => s.languages));
    if (this.serverFor('typescript')?.available) disableBuiltinTypescript();
    for (const { model } of this.models.values()) this.route(model);
    this.host.changed();
  }

  private serverFor(language: string): LspServer | undefined {
    return this.servers.find((s) => !s.extra && s.languages.includes(language));
  }

  /** The servers that work next to the main one, while their extension is installed. */
  private extrasFor(language: string): LspServer[] {
    return this.servers.filter((s) => s.extra && s.available && hasNative(`lsp:${s.id}`) && s.languages.includes(language));
  }

  connFor(model: Model): Connection | undefined {
    const s = this.serverFor(model.getLanguageId());
    const c = s && this.conns.get(s.id);
    return c?.state === 'ready' ? c : undefined;
  }

  /** TypeScript's server, when Vue's leaves the script of this language to it. */
  private partnerFor(language: string): LspServer | undefined {
    if (language !== 'vue' || !this.serverFor('vue')?.plugin) return undefined;
    const ts = this.serverFor('typescript');
    return ts?.available ? ts : undefined;
  }

  /** Every server ready for this file: the main one first. */
  connsFor(model: Model): Connection[] {
    const main = this.connFor(model);
    const language = model.getLanguageId();
    const partner = this.partnerFor(language);
    const others = [...(partner ? [partner] : []), ...this.extrasFor(language)].flatMap((s) => {
      const c = this.conns.get(s.id);
      return c?.state === 'ready' ? [c] : [];
    });
    return main ? [main, ...others] : others;
  }

  /** Vue's server asks about the script through the client: the answer may be null, never missing. */
  askTypescript(vue: Connection, params: unknown) {
    const list = params as unknown[];
    const [id, command, args] = (Array.isArray(list[0]) ? list[0] : list) as [number, string, unknown];
    const answer = (body: unknown) => vue.notify('tsserver/response', [[id, body ?? null]]);
    const ts = this.conns.get('typescript');
    if (ts?.state !== 'ready') return answer(null);
    ts.request<Json | null>('workspace/executeCommand', { command: 'typescript.tsserverRequest', arguments: [command, args, { isAsync: true, lowPriority: true }] }).then(
      (r) => answer(r?.body),
      () => answer(null),
    );
  }

  private route(model: Model) {
    if (this.disposed) return;
    const language = model.getLanguageId();
    const main = this.serverFor(language);
    const partner = this.partnerFor(language);
    if (partner && !this.tsPlugin) {
      // it may be running without the plugin, which it only reads when it starts
      this.tsPlugin = main?.plugin ?? '';
      this.conns.get(partner.id)?.close();
      this.conns.delete(partner.id);
      for (const { model: m } of this.models.values()) if (m !== model) this.route(m);
    }
    for (const s of [...(main?.available ? [main] : []), ...(partner ? [partner] : []), ...this.extrasFor(language)]) {
      let c = this.conns.get(s.id);
      if (!c || c.state === 'closed') {
        c = this.connect(s);
      }
      c.open(model);
    }
  }

  /** An extension came or went: its server follows. */
  private extrasChanged() {
    if (this.disposed) return;
    for (const s of this.servers) {
      if (!s.extra || hasNative(`lsp:${s.id}`)) continue;
      this.conns.get(s.id)?.close();
      this.conns.delete(s.id);
    }
    for (const { model } of this.models.values()) this.route(model);
    this.host.changed();
  }

  private connect(s: LspServer): Connection {
    const c = new Connection(this, s, () => this.onFail(s));
    this.conns.set(s.id, c);
    this.host.changed();
    return c;
  }

  private onFail(s: LspServer) {
    const tries = this.restarts.get(s.id) ?? 0;
    if (tries >= 2 || this.disposed) return;
    this.restarts.set(s.id, tries + 1);
    window.setTimeout(() => {
      if (this.disposed) return;
      this.conns.delete(s.id);
      for (const { model } of this.models.values()) this.route(model);
    }, 2000 * (tries + 1));
  }

  attach(model: Model) {
    const uri = model.uri.toString();
    if (this.models.has(uri)) return;
    const sub = model.onDidChangeContent((e) => this.connsFor(model).forEach((c) => c.change(model, e)));
    this.models.set(uri, { model, off: () => sub.dispose() });
    this.route(model);
    // diagnostics that arrived before the file was opened
    for (const c of this.conns.values()) {
      const d = c.diagnostics.get(uri);
      if (d) this.applyDiagnostics(uri, d, c.markerOwner);
    }
  }

  detach(model: Model) {
    const uri = model.uri.toString();
    const m = this.models.get(uri);
    if (!m) return;
    m.off();
    this.models.delete(uri);
    for (const c of this.conns.values()) c.closeDoc(model);
  }

  saved(model: Model) {
    this.connsFor(model).forEach((c) => c.saved(model));
  }

  /** Stop every server and reconnect the open files (command "Restart language servers"). */
  restart() {
    for (const c of this.conns.values()) c.close();
    this.conns.clear();
    this.restarts.clear();
    for (const { model } of this.models.values()) this.route(model);
    this.host.changed();
  }

  dispose() {
    this.disposed = true;
    this.offNative?.();
    this.offNative = null;
    if (active === this) active = null;
    for (const c of this.conns.values()) c.close();
    this.conns.clear();
    for (const m of this.models.values()) m.off();
    this.models.clear();
  }

  /** Make this hub the one providers talk to, after a suspend. */
  resume() {
    this.disposed = false;
    active = this;
    this.followExtensions();
    for (const m of monaco.editor.getModels()) if (m.uri.scheme === 'file' && m.uri.path.startsWith(this.host.root)) this.attach(m);
  }

  stateFor(language: string): LspState {
    const s = this.serverFor(language);
    if (!s) return { state: 'none', name: '', text: '', install: '' };
    if (!s.available) {
      // the editor's own TypeScript service covers one file at a time
      if (language === 'typescript' || language === 'javascript') return { state: 'builtin', name: s.name, text: 'Basic IntelliSense', install: s.install };
      return { state: 'missing', name: s.name, text: 'No language server', install: s.install };
    }
    const c = this.conns.get(s.id);
    if (!c || c.state === 'closed') return { state: 'starting', name: s.name, text: 'Starting…', install: s.install };
    return { state: c.state === 'failed' ? 'failed' : c.state === 'ready' ? 'ready' : 'starting', name: s.name, text: c.statusText, install: s.install };
  }

  applyDiagnostics(uri: string, diagnostics: Json[], owner = 'lsp') {
    const model = monaco.editor.getModel(monaco.Uri.parse(uri));
    if (model) {
      const sev = SEVERITY();
      monaco.editor.setModelMarkers(
        model,
        owner,
        diagnostics.map((d) => ({
          ...fromLspRange(d.range),
          message: d.message,
          severity: sev[d.severity ?? 1] ?? monaco.MarkerSeverity.Error,
          source: d.source,
          code: d.code === undefined ? undefined : String(d.code),
          tags: d.tags,
          relatedInformation: d.relatedInformation?.map((r: Json) => ({ resource: monaco.Uri.parse(r.location.uri), ...fromLspRange(r.location.range), message: r.message })),
        })),
      );
    }
    this.host.changed();
  }

  clearDiagnostics(c: Connection) {
    for (const uri of c.diagnostics.keys()) {
      const model = monaco.editor.getModel(monaco.Uri.parse(uri));
      if (model) monaco.editor.setModelMarkers(model, c.markerOwner, []);
    }
    c.diagnostics.clear();
  }

  /** Diagnostics of files without a model, for the Problems panel. */
  closedDiagnostics(): { path: string; items: { line: number; col: number; message: string; severity: number; source?: string }[] }[] {
    const out: { path: string; items: { line: number; col: number; message: string; severity: number; source?: string }[] }[] = [];
    const sev = SEVERITY();
    for (const c of this.conns.values()) {
      for (const [uri, list] of c.diagnostics) {
        if (!list.length || !isFileUri(uri) || monaco.editor.getModel(monaco.Uri.parse(uri))) continue;
        out.push({
          path: pathOf(uri),
          items: list.map((d) => ({ line: d.range.start.line + 1, col: d.range.start.character + 1, message: d.message, severity: sev[d.severity ?? 1] ?? monaco.MarkerSeverity.Error, source: d.source })),
        });
      }
    }
    return out;
  }

  /** Symbols across the project, for "#" in Go to file. */
  async workspaceSymbols(query: string): Promise<{ name: string; detail: string; path: string; line: number; col: number }[]> {
    const lists = await Promise.all(
      [...this.conns.values()]
        .filter((c) => c.state === 'ready' && c.has('workspaceSymbolProvider', 'workspace/symbol'))
        .map((c) => c.request<Json[] | null>('workspace/symbol', { query }).catch(() => null)),
    );
    const out: { name: string; detail: string; path: string; line: number; col: number }[] = [];
    for (const l of lists) {
      for (const s of l ?? []) {
        const uri: string | undefined = s.location?.uri;
        if (!uri || !isFileUri(uri)) continue;
        const range = s.location.range;
        out.push({ name: s.name, detail: s.containerName ?? '', path: pathOf(uri), line: (range?.start?.line ?? 0) + 1, col: (range?.start?.character ?? 0) + 1 });
      }
    }
    return out.slice(0, 200);
  }

  /** LSP WorkspaceEdit → per-file text edits, with models loaded so they can apply. */
  async collectEdit(edit: Json): Promise<{ model: Model; edits: Json[] }[]> {
    const files = new Map<string, Json[]>();
    for (const [uri, edits] of Object.entries(edit?.changes ?? {})) files.set(uri, [...(files.get(uri) ?? []), ...(edits as Json[])]);
    for (const ch of edit?.documentChanges ?? []) {
      if (ch.kind) {
        this.host.log(`A language server asked for a file ${ch.kind}; do it from the file tree.`);
        continue;
      }
      const uri = ch.textDocument?.uri;
      if (uri) files.set(uri, [...(files.get(uri) ?? []), ...(ch.edits ?? [])]);
    }
    const out: { model: Model; edits: Json[] }[] = [];
    for (const [uri, edits] of files) {
      if (!isFileUri(uri)) continue;
      const model = await this.host.ensureModel(pathOf(uri));
      if (model) out.push({ model, edits });
    }
    return out;
  }

  async toMonacoEdit(edit: Json): Promise<monaco.languages.WorkspaceEdit> {
    const files = await this.collectEdit(edit);
    return {
      edits: files.flatMap(({ model, edits }) =>
        edits.map((e) => ({ resource: model.uri, versionId: undefined, textEdit: { range: fromLspRange(e.range), text: e.newText } })),
      ),
    };
  }

  async applyEdit(edit: Json): Promise<boolean> {
    try {
      for (const { model, edits } of await this.collectEdit(edit)) {
        model.pushStackElement();
        model.pushEditOperations([], edits.map((e) => ({ range: fromLspRange(e.range), text: e.newText })), () => null);
        model.pushStackElement();
      }
      return true;
    } catch {
      return false;
    }
  }

  /** Locations for peek and go to definition: their models must exist first. */
  async toLocations(res: unknown): Promise<monaco.languages.LocationLink[]> {
    if (!res) return [];
    const list = (Array.isArray(res) ? res : [res]) as Json[];
    const out: monaco.languages.LocationLink[] = [];
    await Promise.all(
      list.map(async (l) => {
        const uri: string = l.targetUri ?? l.uri;
        if (!uri || !isFileUri(uri)) return;
        const model = await this.host.ensureModel(pathOf(uri));
        if (!model) return;
        const range = fromLspRange(l.targetSelectionRange ?? l.range);
        out.push({
          uri: model.uri,
          range,
          targetSelectionRange: range,
          originSelectionRange: l.originSelectionRange ? fromLspRange(l.originSelectionRange) : undefined,
        });
      }),
    );
    return out;
  }
}

const TRIGGERS = ['.', ':', '>', '"', "'", '/', '@', '<', '$', '-', '\\', '(', '#'];

const providerLanguages = new Set<string>();

/** Register the providers once per language; they ask `active` at call time. */
function registerProviders(languages: string[] = []) {
  if (!providersDone) {
    providersDone = true;
    monaco.editor.registerCommand('nb.lsp.execute', (_acc, serverId: string, command: string, args: unknown[]) => {
      const c = active && [...active.conns.values()].find((x) => x.server.id === serverId);
      void c?.request('workspace/executeCommand', { command, arguments: args }).catch(() => undefined);
    });
  }
  for (const lang of languages) {
    if (providerLanguages.has(lang)) continue;
    providerLanguages.add(lang);
    registerLanguage(lang);
  }
}

function registerLanguage(lang: string) {
  const L = monaco.languages;
  const conn = (model: Model) => active?.connFor(model);
  const quiet = <T>(p: Promise<T>, fallback: T) => p.catch(() => fallback);
  /** The first server with an answer: in a Vue file TypeScript's speaks for the script. */
  const firstAnswer = async <T = any>(model: Model, capability: string, ask: (c: Connection) => Promise<T>): Promise<T | null> => { // eslint-disable-line @typescript-eslint/no-explicit-any
    for (const c of active?.connsFor(model) ?? []) {
      if (!c.caps[capability]) continue;
      const r = await quiet<T | null>(ask(c), null);
      if (r !== null && r !== undefined && !(Array.isArray(r) && !r.length)) return r;
    }
    return null;
  };

  const completeWith = async (c: Connection, model: Model, position: monaco.Position, ctx: monaco.languages.CompletionContext) => {
    if (!c.caps.completionProvider) return undefined;
    const declared: string[] = c.caps.completionProvider.triggerCharacters ?? [];
    if (ctx.triggerKind === L.CompletionTriggerKind.TriggerCharacter && !declared.includes(ctx.triggerCharacter ?? '')) return undefined;
    const res = await quiet(
      c.request('textDocument/completion', {
        textDocument: docId(model),
        position: toLspPos(position),
        context: { triggerKind: ctx.triggerKind === L.CompletionTriggerKind.TriggerCharacter ? 2 : ctx.triggerKind === L.CompletionTriggerKind.TriggerForIncompleteCompletions ? 3 : 1, triggerCharacter: ctx.triggerCharacter },
      }),
      null,
    );
    if (!res) return undefined;
    const items: Json[] = Array.isArray(res) ? res : (res.items ?? []);
    const word = model.getWordUntilPosition(position);
    const fallback: monaco.IRange = { startLineNumber: position.lineNumber, startColumn: word.startColumn, endLineNumber: position.lineNumber, endColumn: word.endColumn };
    const defaults = Array.isArray(res) ? {} : (res.itemDefaults ?? {});
    return {
      incomplete: !Array.isArray(res) && !!res.isIncomplete,
      suggestions: items.map((it) => {
        const te = it.textEdit ?? (defaults.editRange ? { range: defaults.editRange, newText: it.textEditText ?? it.insertText ?? it.label } : null);
        const range = te ? fromLspRange(te.range ?? te.replace ?? te.insert) : fallback;
        const format = it.insertTextFormat ?? defaults.insertTextFormat;
        const label = it.labelDetails ? { label: it.label, detail: it.labelDetails.detail, description: it.labelDetails.description } : it.label;
        return {
          label,
          kind: completionKind(it.kind),
          detail: it.detail,
          documentation: markdownOf(it.documentation)[0],
          sortText: it.sortText,
          filterText: it.filterText,
          preselect: it.preselect,
          insertText: te?.newText ?? it.insertText ?? it.label,
          insertTextRules: format === 2 ? L.CompletionItemInsertTextRule.InsertAsSnippet : undefined,
          range,
          commitCharacters: it.commitCharacters ?? defaults.commitCharacters,
          additionalTextEdits: it.additionalTextEdits?.map((e: Json) => ({ range: fromLspRange(e.range), text: e.newText })),
          tags: it.tags?.includes(1) || it.deprecated ? [L.CompletionItemTag.Deprecated] : undefined,
          lsp: it,
          lspConn: c,
        } as monaco.languages.CompletionItem & { lsp: Json; lspConn: Connection };
      }),
    };
  };

  L.registerCompletionItemProvider(lang, {
    triggerCharacters: TRIGGERS,
    async provideCompletionItems(model, position, ctx) {
      const answers = await Promise.all((active?.connsFor(model) ?? []).map((c) => completeWith(c, model, position, ctx)));
      const lists = answers.filter((x) => !!x);
      if (!lists.length) return undefined;
      return { incomplete: lists.some((x) => x.incomplete), suggestions: lists.flatMap((x) => x.suggestions) };
    },
    async resolveCompletionItem(item) {
      const { lsp: raw, lspConn: c } = item as monaco.languages.CompletionItem & { lsp?: Json; lspConn?: Connection };
      if (!raw || !c?.caps.completionProvider?.resolveProvider) return item;
      const r = await quiet(c.request('completionItem/resolve', raw), null);
      if (!r) return item;
      return {
        ...item,
        detail: r.detail ?? item.detail,
        documentation: markdownOf(r.documentation)[0] ?? item.documentation,
        additionalTextEdits: r.additionalTextEdits?.map((e: Json) => ({ range: fromLspRange(e.range), text: e.newText })) ?? item.additionalTextEdits,
      };
    },
  });

  L.registerHoverProvider(lang, {
    async provideHover(model, position) {
      const conns = (active?.connsFor(model) ?? []).filter((c) => c.caps.hoverProvider);
      const answers = await Promise.all(conns.map((c) => quiet(c.request('textDocument/hover', { textDocument: docId(model), position: toLspPos(position) }), null)));
      const hits = answers.filter((r) => markdownOf(r?.contents).length);
      if (!hits.length) return null;
      return { contents: hits.flatMap((r) => markdownOf(r.contents)), range: hits[0].range ? fromLspRange(hits[0].range) : undefined };
    },
  });

  L.registerSignatureHelpProvider(lang, {
    signatureHelpTriggerCharacters: ['(', ','],
    signatureHelpRetriggerCharacters: [','],
    async provideSignatureHelp(model, position) {
      const r = await firstAnswer(model, 'signatureHelpProvider', (c) => c.request('textDocument/signatureHelp', { textDocument: docId(model), position: toLspPos(position) }));
      if (!r?.signatures?.length) return null;
      return {
        value: {
          activeSignature: r.activeSignature ?? 0,
          activeParameter: r.signatures[r.activeSignature ?? 0]?.activeParameter ?? r.activeParameter ?? 0,
          signatures: r.signatures.map((s: Json) => ({
            label: s.label,
            documentation: markdownOf(s.documentation)[0],
            parameters: (s.parameters ?? []).map((p: Json) => ({ label: p.label, documentation: markdownOf(p.documentation)[0] })),
          })),
        },
        dispose() {},
      };
    },
  });

  const locations = (capability: string, method: string) => async (model: Model, position: monaco.Position) => {
    const r = await firstAnswer(model, capability, (c) => c.request(method, { textDocument: docId(model), position: toLspPos(position) }));
    return r && active ? active.toLocations(r) : null;
  };
  L.registerDefinitionProvider(lang, { provideDefinition: locations('definitionProvider', 'textDocument/definition') });
  L.registerDeclarationProvider(lang, { provideDeclaration: locations('declarationProvider', 'textDocument/declaration') });
  L.registerTypeDefinitionProvider(lang, { provideTypeDefinition: locations('typeDefinitionProvider', 'textDocument/typeDefinition') });
  L.registerImplementationProvider(lang, { provideImplementation: locations('implementationProvider', 'textDocument/implementation') });
  L.registerReferenceProvider(lang, {
    async provideReferences(model, position, ctx) {
      const r = await firstAnswer(model, 'referencesProvider', (c) => c.request('textDocument/references', { textDocument: docId(model), position: toLspPos(position), context: { includeDeclaration: ctx.includeDeclaration } }));
      return r && active ? active.toLocations(r) : null;
    },
  });

  L.registerDocumentHighlightProvider(lang, {
    async provideDocumentHighlights(model, position) {
      const r = await firstAnswer<Json[]>(model, 'documentHighlightProvider', (c) => c.request('textDocument/documentHighlight', { textDocument: docId(model), position: toLspPos(position) }));
      return (r ?? []).map((h: Json) => ({ range: fromLspRange(h.range), kind: (h.kind ?? 1) - 1 }));
    },
  });

  L.registerDocumentSymbolProvider(lang, {
    displayName: 'Language server',
    async provideDocumentSymbols(model) {
      const c = conn(model);
      if (!c?.caps.documentSymbolProvider) return null;
      const r: Json[] | null = await quiet(c.request('textDocument/documentSymbol', { textDocument: docId(model) }), null);
      const conv = (s: Json): monaco.languages.DocumentSymbol => {
        const range = fromLspRange(s.range ?? s.location.range);
        return {
          name: s.name,
          detail: s.detail ?? s.containerName ?? '',
          kind: (s.kind ?? 13) - 1,
          tags: s.tags ?? [],
          range,
          selectionRange: s.selectionRange ? fromLspRange(s.selectionRange) : range,
          children: s.children?.map(conv),
        };
      };
      return (r ?? []).map(conv);
    },
  });

  L.registerRenameProvider(lang, {
    async provideRenameEdits(model, position, newName) {
      if (!active?.connsFor(model).some((c) => c.caps.renameProvider)) return { edits: [], rejectReason: 'This language server cannot rename.' };
      const r = await firstAnswer(model, 'renameProvider', (c) => c.request('textDocument/rename', { textDocument: docId(model), position: toLspPos(position), newName }));
      return r && active ? active.toMonacoEdit(r) : { edits: [] };
    },
    async resolveRenameLocation(model, position) {
      const word = model.getWordAtPosition(position);
      const fallback = word
        ? { range: { startLineNumber: position.lineNumber, startColumn: word.startColumn, endLineNumber: position.lineNumber, endColumn: word.endColumn }, text: word.word }
        : { range: new monaco.Range(position.lineNumber, position.column, position.lineNumber, position.column), text: '', rejectReason: 'Nothing to rename here.' };
      const conns = active?.connsFor(model).filter((c) => c.caps.renameProvider) ?? [];
      if (!conns.length || conns.some((c) => typeof c.caps.renameProvider !== 'object' || !c.caps.renameProvider.prepareProvider)) return fallback;
      const r = await firstAnswer(model, 'renameProvider', (c) => c.request('textDocument/prepareRename', { textDocument: docId(model), position: toLspPos(position) }));
      if (!r) return { ...fallback, rejectReason: 'This cannot be renamed.' };
      if (r.range) return { range: fromLspRange(r.range), text: r.placeholder ?? model.getValueInRange(fromLspRange(r.range)) };
      if (r.start) return { range: fromLspRange(r), text: model.getValueInRange(fromLspRange(r)) };
      return fallback;
    },
  });

  L.registerCodeActionProvider(lang, {
    async provideCodeActions(model, range, ctx) {
      const hub = active;
      if (!hub) return { actions: [], dispose() {} };
      const conns = hub.connsFor(model).filter((c) => c.caps.codeActionProvider && (c === conn(model) || c.server.id === 'typescript'));
      const lists = await Promise.all(
        conns.map(async (c) => {
          const known = c.diagnostics.get(model.uri.toString()) ?? [];
          const overlapping = known.filter((d) => monaco.Range.areIntersectingOrTouching(fromLspRange(d.range), range));
          const r: Json[] | null = await quiet(
            c.request('textDocument/codeAction', { textDocument: docId(model), range: toLspRange(range), context: { diagnostics: overlapping, only: ctx.only ? [ctx.only] : undefined } }),
            null,
          );
          return Promise.all(
            (r ?? []).map(async (a) => {
              const action: monaco.languages.CodeAction & { lsp?: Json; lspConn?: Connection } = { title: a.title, kind: a.kind, isPreferred: a.isPreferred, disabled: a.disabled?.reason, lsp: a, lspConn: c };
              if (a.edit) action.edit = await hub.toMonacoEdit(a.edit);
              const cmd = a.command && typeof a.command === 'object' ? a.command : a.command ? a : null;
              if (cmd) action.command = { id: 'nb.lsp.execute', title: cmd.title, arguments: [c.server.id, cmd.command, cmd.arguments] };
              return action;
            }),
          );
        }),
      );
      return { actions: lists.flat(), dispose() {} };
    },
    async resolveCodeAction(action) {
      const a = action as monaco.languages.CodeAction & { lsp?: Json; lspConn?: Connection };
      const c = a.lspConn;
      if (!c || a.edit || !a.lsp || !active) return action;
      if (typeof c.caps.codeActionProvider !== 'object' || !c.caps.codeActionProvider.resolveProvider) return action;
      const r = await quiet(c.request('codeAction/resolve', a.lsp), null);
      if (r?.edit) a.edit = await active.toMonacoEdit(r.edit);
      return a;
    },
  });

  L.registerDocumentFormattingEditProvider(lang, {
    displayName: 'Language server',
    async provideDocumentFormattingEdits(model, options) {
      const c = conn(model);
      if (!c?.caps.documentFormattingProvider) return null;
      const r: Json[] | null = await quiet(c.request('textDocument/formatting', { textDocument: docId(model), options }), null);
      return (r ?? []).map((e) => ({ range: fromLspRange(e.range), text: e.newText }));
    },
  });
  L.registerDocumentRangeFormattingEditProvider(lang, {
    displayName: 'Language server',
    async provideDocumentRangeFormattingEdits(model, range, options) {
      const c = conn(model);
      if (!c?.caps.documentRangeFormattingProvider) return null;
      const r: Json[] | null = await quiet(c.request('textDocument/rangeFormatting', { textDocument: docId(model), range: toLspRange(range), options }), null);
      return (r ?? []).map((e) => ({ range: fromLspRange(e.range), text: e.newText }));
    },
  });

  L.registerInlayHintsProvider(lang, {
    async provideInlayHints(model, range) {
      const c = conn(model);
      if (!c?.caps.inlayHintProvider) return { hints: [], dispose() {} };
      const r: Json[] | null = await quiet(c.request('textDocument/inlayHint', { textDocument: docId(model), range: toLspRange(range) }), null);
      return {
        hints: (r ?? []).map((h) => ({
          label: typeof h.label === 'string' ? h.label : (h.label as Json[]).map((p) => p.value).join(''),
          position: { lineNumber: h.position.line + 1, column: h.position.character + 1 },
          kind: h.kind,
          paddingLeft: h.paddingLeft,
          paddingRight: h.paddingRight,
        })),
        dispose() {},
      };
    },
  });
}
