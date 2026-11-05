// The editor card's side of the link to Claude Code consoles (docs/API-agents.md §24). While
// the card is open the hub can ask it what a console's own IDE would know: the selection, the
// open files, the problems. A console's proposed change opens as a diff to accept or reject.

import { api, errorMessage, wsUrl } from '../../api';
import { toast } from '../../components/Toast';
import { monaco } from './monaco';
import { nameOf, type Workspace } from './workspace';

type Editor = monaco.editor.IStandaloneCodeEditor;
type Json = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

export interface LinkedConsole {
  id: string;
  name: string;
}

/** A change a console proposes: its call stays open until someone decides. */
export interface Proposal {
  tab: string;
  path: string;
  oldPath: string;
  after: string;
  console: string;
  callId: string;
  /** What the diff shows now: it can be edited before accepting. */
  currentText?: () => string;
}

const SELECTION_MS = 120;
const TABS_MS = 300;
const RETRY_MAX_MS = 15_000;
const MAX_SELECTION = 256 << 10;
/** Never sent along, even when not ignored by git. */
const SENSITIVE = /(^|\/)(\.env(\.[^/]*)?|[^/]*\.(pem|key))$/i;

const SEVERITY: Record<number, string> = { 8: 'Error', 4: 'Warning', 2: 'Information', 1: 'Hint' };

const text = (...items: string[]) => ({ content: items.map((t) => ({ type: 'text', text: t })) });
const asJson = (v: unknown) => text(JSON.stringify(v));

const NOTHING = { text: '', filePath: '', fileUrl: '', selection: { start: { line: 0, character: 0 }, end: { line: 0, character: 0 }, isEmpty: true } };

export class IdeLink {
  consoles: LinkedConsole[] = [];
  readonly proposals = new Map<string, Proposal>();
  private sock: WebSocket | null = null;
  private workspaceId = '';
  private retryMs = 1000;
  private retryTimer = 0;
  private selectionTimer = 0;
  private tabsTimer = 0;
  private lastSelection = '';
  private lastTabs = '';
  private offWs: (() => void) | null = null;

  constructor(private readonly ws: Workspace) {}

  /** The card opened on this board. */
  connect(workspaceId: string) {
    this.workspaceId = workspaceId;
    this.retryMs = 1000;
    this.offWs ??= this.ws.subscribe(() => this.tabsSoon());
    this.open();
  }

  /** The card closed: consoles go back to asking in their terminal. */
  disconnect() {
    this.workspaceId = '';
    window.clearTimeout(this.retryTimer);
    window.clearTimeout(this.selectionTimer);
    window.clearTimeout(this.tabsTimer);
    this.offWs?.();
    this.offWs = null;
    const sock = this.sock;
    this.sock = null;
    sock?.close();
    this.consoles = [];
    this.dropProposals();
    this.ws.changed();
  }

  private open() {
    if (!this.workspaceId) return;
    const sock = new WebSocket(wsUrl(`/api/workspaces/${encodeURIComponent(this.workspaceId)}/ide/ws`));
    this.sock = sock;
    sock.onopen = () => {
      this.retryMs = 1000;
      // the hub knows nothing after a restart
      if (this.lastSelection) sock.send(this.lastSelection);
      this.lastTabs = '';
      this.sendTabs();
    };
    sock.onmessage = (ev) => this.onMessage(String(ev.data));
    sock.onclose = () => {
      if (this.sock !== sock) return;
      this.sock = null;
      this.consoles = [];
      // whatever was waiting got its answer from the hub when this socket went
      this.dropProposals();
      this.ws.changed();
      this.retryTimer = window.setTimeout(() => this.open(), this.retryMs);
      this.retryMs = Math.min(this.retryMs * 2, RETRY_MAX_MS);
    };
  }

  private send(m: Json) {
    if (this.sock?.readyState === WebSocket.OPEN) this.sock.send(JSON.stringify(m));
  }

  // ---- what the editor tells ----------------------------------------------------------

  /** The selection of an editor changed; a burst becomes one message. */
  selectionFrom(ed: Editor) {
    window.clearTimeout(this.selectionTimer);
    this.selectionTimer = window.setTimeout(() => this.sendSelection(ed), SELECTION_MS);
  }

  private shareable(path: string) {
    return this.ws.editorPrefs.shareSelection && !SENSITIVE.test(path) && !this.ws.ignored.has(path);
  }

  private sendSelection(ed: Editor) {
    const model = ed.getModel();
    const sel = ed.getSelection();
    let selection: Json = NOTHING;
    if (model && sel && model.uri.scheme === 'file' && this.shareable(model.uri.path)) {
      selection = {
        text: model.getValueInRange(sel).slice(0, MAX_SELECTION),
        filePath: model.uri.path,
        fileUrl: model.uri.toString(),
        selection: {
          start: { line: sel.startLineNumber - 1, character: sel.startColumn - 1 },
          end: { line: sel.endLineNumber - 1, character: sel.endColumn - 1 },
          isEmpty: sel.isEmpty(),
        },
      };
    }
    const m = JSON.stringify({ t: 'selection', selection });
    if (m === this.lastSelection) return;
    this.lastSelection = m;
    if (this.sock?.readyState === WebSocket.OPEN) this.sock.send(m);
  }

  private tabsSoon() {
    window.clearTimeout(this.tabsTimer);
    this.tabsTimer = window.setTimeout(() => this.sendTabs(), TABS_MS);
  }

  private sendTabs() {
    const active = this.ws.activeTab;
    const tabs = this.ws.groups.flatMap((g) =>
      g.tabs
        .filter((t) => !t.diff && t.path)
        .map((t) => {
          const d = this.ws.docs.get(t.path);
          return { uri: monaco.Uri.file(t.path).toString(), isActive: t === active, label: nameOf(t.path), languageId: d?.model?.getLanguageId() ?? 'plaintext', isDirty: !!d?.dirty };
        }),
    );
    const m = JSON.stringify({ t: 'tabs', tabs });
    if (m === this.lastTabs) return;
    this.lastTabs = m;
    if (this.sock?.readyState === WebSocket.OPEN) this.sock.send(m);
  }

  /** Put a reference to the selection in a console's prompt. */
  mention(ed: Editor, consoleId: string) {
    const model = ed.getModel();
    const sel = ed.getSelection();
    if (!model) return;
    const whole = !sel || sel.isEmpty();
    // without a selection the file itself is meant, as in VS Code
    // a selection that stops at the start of a line does not include that line
    const last = !whole && sel.endColumn === 1 && sel.endLineNumber > sel.startLineNumber ? sel.endLineNumber - 1 : sel?.endLineNumber;
    const mention: Json = { filePath: model.uri.path, lineStart: whole ? undefined : sel.startLineNumber - 1, lineEnd: whole || last === undefined ? undefined : last - 1 };
    this.send({ t: 'mention', session: consoleId, mention });
  }

  /** For a CLI that is not linked: the same reference, typed into its prompt as text. */
  typeReference(ed: Editor, consoleId: string) {
    const model = ed.getModel();
    const sel = ed.getSelection();
    if (!model) return;
    let where = this.ws.rel(model.uri.path);
    if (sel && !sel.isEmpty()) {
      const last = sel.endColumn === 1 && sel.endLineNumber > sel.startLineNumber ? sel.endLineNumber - 1 : sel.endLineNumber;
      where += sel.startLineNumber === last ? `:${last}` : `:${sel.startLineNumber}-${last}`;
    }
    void api.sendInput(consoleId, `${where} `).catch(() => undefined);
  }

  // ---- what the hub asks --------------------------------------------------------------

  private onMessage(raw: string) {
    let m: Json;
    try {
      m = JSON.parse(raw);
    } catch {
      return;
    }
    if (m.t === 'consoles') {
      this.consoles = m.items ?? [];
      this.ws.changed();
    } else if (m.t === 'cancel') {
      for (const p of this.proposals.values()) if (p.callId === m.id) this.closeProposal(p);
    } else if (m.t === 'call') {
      void this.onCall(m);
    }
  }

  private reply(id: string, result: unknown) {
    this.send({ t: 'reply', id, result });
  }

  private async onCall(m: Json) {
    const a: Json = m.args ?? {};
    try {
      switch (m.tool) {
        case 'openDiff':
          this.openProposal(m.id, a, m.session?.name ?? 'Claude Code', m.session?.id ?? '');
          return; // answered when someone decides
        case 'close_tab': {
          const p = this.proposals.get(a.tab_name);
          if (p) {
            this.reply(p.callId, text('TAB_CLOSED'));
            this.closeProposal(p);
          }
          return this.reply(m.id, text('TAB_CLOSED'));
        }
        case 'closeAllDiffTabs': {
          const all = [...this.proposals.values()];
          for (const p of all) {
            this.reply(p.callId, text('TAB_CLOSED'));
            this.closeProposal(p);
          }
          return this.reply(m.id, text(`CLOSED_${all.length}_DIFF_TABS`));
        }
        case 'getDiagnostics':
          return this.reply(m.id, asJson(this.diagnostics(a.uri)));
        case 'checkDocumentDirty': {
          const d = this.ws.docs.get(a.filePath);
          return this.reply(m.id, asJson(d ? { success: true, filePath: a.filePath, isDirty: !!d.dirty, isUntitled: false } : { success: false, message: `Document not open: ${a.filePath}` }));
        }
        case 'saveDocument': {
          const d = this.ws.docs.get(a.filePath);
          if (!d) return this.reply(m.id, asJson({ success: false, message: `Document not open: ${a.filePath}` }));
          const saved = await this.ws.save(a.filePath);
          return this.reply(m.id, asJson({ success: saved, filePath: a.filePath, saved, message: saved ? 'Document saved' : 'Could not save the document' }));
        }
        case 'openFile':
          return this.reply(m.id, await this.openFile(a));
        default:
          return this.reply(m.id, { ...text(`Unknown tool: ${m.tool}`), isError: true });
      }
    } catch (err) {
      this.reply(m.id, { ...text(errorMessage(err)), isError: true });
    }
  }

  private diagnostics(uri?: string) {
    const only = uri ? monaco.Uri.parse(uri) : undefined;
    const files = new Map<string, Json[]>();
    for (const d of monaco.editor.getModelMarkers(only ? { resource: only } : {})) {
      if (d.resource.scheme !== 'file' || (!only && !this.ws.inside(d.resource.path))) continue;
      const key = d.resource.toString();
      const code = typeof d.code === 'string' ? d.code : d.code?.value;
      files.set(key, [
        ...(files.get(key) ?? []),
        {
          message: d.message,
          severity: SEVERITY[d.severity] ?? 'Information',
          range: { start: { line: d.startLineNumber - 1, character: d.startColumn - 1 }, end: { line: d.endLineNumber - 1, character: d.endColumn - 1 } },
          source: d.source,
          code,
        },
      ]);
    }
    return [...files].map(([u, diagnostics]) => ({ uri: u, diagnostics }));
  }

  private async openFile(a: Json) {
    const path: string = a.filePath;
    let at: { line?: number; col?: number; endCol?: number } = {};
    if (a.startText) {
      const model = await this.ws.ensureModel(path);
      const hit = model?.findMatches(a.startText, false, false, true, null, false)[0];
      if (hit) {
        const end = hit.range.endLineNumber === hit.range.startLineNumber ? hit.range.endColumn : undefined;
        at = { line: hit.range.startLineNumber, col: hit.range.startColumn, endCol: a.selectToEndOfLine && model ? model.getLineMaxColumn(hit.range.startLineNumber) : end };
      }
    }
    if (a.makeFrontmost === false) {
      const model = await this.ws.ensureModel(path);
      return asJson({ success: !!model, filePath: path, languageId: model?.getLanguageId(), lineCount: model?.getLineCount() });
    }
    this.ws.open(path, { pin: !a.preview, source: true, ...at });
    return text(`Opened file: ${path}`);
  }

  // ---- proposed changes ---------------------------------------------------------------

  private openProposal(callId: string, a: Json, consoleName: string, consoleId: string) {
    const old = this.proposals.get(a.tab_name);
    if (old) {
      // the same tab name again takes the place of the old one
      this.reply(old.callId, text('DIFF_REJECTED', old.tab));
      this.proposals.delete(old.tab);
    }
    const p: Proposal = { tab: a.tab_name, path: a.new_file_path || a.old_file_path, oldPath: a.old_file_path, after: a.new_file_contents ?? '', console: consoleName, callId };
    this.proposals.set(p.tab, p);
    this.ws.openDiff(p.path, `ide:${p.tab}`);
    // a console inside this editor is in view already, and its needs-you says the rest
    if (this.ws.paneConsoles.includes(consoleId)) return;
    toast.notify('info', `${consoleName} proposes a change`, nameOf(p.path), {
      label: 'Review',
      onClick: () => this.ws.openDiff(p.path, `ide:${p.tab}`),
    });
  }

  /** The text before the change; a new file has none. */
  before(p: Proposal): Promise<string> {
    return api
      .fsRead(this.ws.mid, p.oldPath, 32 << 20)
      .then((r) => (r.content.startsWith('﻿') ? r.content.slice(1) : r.content))
      .catch(() => '');
  }

  accept(tab: string) {
    const p = this.proposals.get(tab);
    if (!p) return;
    this.reply(p.callId, text('FILE_SAVED', p.currentText?.() ?? p.after));
    this.closeProposal(p);
  }

  reject(tab: string) {
    const p = this.proposals.get(tab);
    if (!p) return;
    this.reply(p.callId, text('DIFF_REJECTED', p.tab));
    this.closeProposal(p);
  }

  /** Its tab was closed by hand: as in VS Code, that says no. */
  tabClosed(tab: string) {
    const p = this.proposals.get(tab);
    if (!p) return;
    this.proposals.delete(tab);
    this.reply(p.callId, text('DIFF_REJECTED', p.tab));
  }

  private closeProposal(p: Proposal) {
    this.proposals.delete(p.tab);
    this.ws.closeDiffTab(p.path, `ide:${p.tab}`);
  }

  private dropProposals() {
    for (const p of [...this.proposals.values()]) this.closeProposal(p);
  }
}
