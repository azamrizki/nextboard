// Board controller: everything the board UI does to a workspace board goes through here.
//
// - edits become ops on the BoardSync (optimistic + batched to the hub)
// - undo/redo (80 steps, per client) stores inverse ops; deleting a terminal is undone by
//   starting a fresh session with the same preset / cwd / machine (the hub creates a new
//   terminal node, whose id is remapped for older history entries)
// - selection lives here too, so keyboard shortcuts, menus and tiles agree on it

import { api, errorMessage } from '../api';
import { toast } from '../components/Toast';
import { newId } from '../lib/ids';
import type { BoardNode, LaunchMode, NodeLayout, NodePatch, Op, Project, Session } from '../lib/types';
import { store } from '../store';
import { guiApi } from '../gui/api';
import { TileStack } from './flow';
import { frameOps, groupNodes } from './groups';
import { readGroup } from './kinds';
import { guessTermSize } from './layout';
import { invertOps, queueOp, remapOps, withoutMissing } from './ops';
import { BoardSync } from './sync';

export const HISTORY_LIMIT = 80;
const MERGE_MS = 1500;

/** What is needed to start a terminal like an existing one. */
export interface TerminalSpec {
  nodeId: string;
  machineId: string;
  preset: string;
  cwd: string;
  name: string;
  rect: NodeLayout;
  /** Phase 5: undefined = hub default (settings.agents.yoloDefault). */
  yolo?: boolean;
  profileId?: string;
  /** undefined = the default in settings. */
  model?: string;
  effort?: string;
  launch?: { mode: LaunchMode; agentSessionId?: string };
}

/** `then` restores what went with these consoles; it refers to them, so it waits until they are back. */
type Step = { t: 'ops'; ops: Op[] } | { t: 'kill'; nodeIds: string[] } | { t: 'closeApps'; nodeIds: string[] } | { t: 'spawn'; specs: TerminalSpec[]; then: Op[] };

interface Entry {
  undo: Step[];
  redo: Step[];
  merge?: string;
  at: number;
}

export interface ApplyOptions {
  /** Consecutive edits with the same key (within 1.5 s) become one undo step. */
  merge?: string;
  /** false = not undoable (remote-like housekeeping). */
  history?: boolean;
}

export const rectOf = (n: BoardNode): NodeLayout => ({ x: n.x, y: n.y, w: n.w, h: n.h });

/** Board rejections the hub reports with a code (Phase 6 rules), in plain words. */
export function boardRejection(msg: string, code = ''): string {
  switch (code) {
    case 'forward_cycle':
      return 'That forward was not added: results would loop back (a group and its consoles count as one). Remove another forward first.';
    case 'bad_edge':
      return 'That connection is not possible: wires go from a card to consoles or groups, forwards between consoles or groups.';
    case 'name_conflict':
      return 'That group name is taken (by another group or a console handle). Pick another name.';
    case 'member_conflict':
      return 'A console can be in only one group (at most 12 per group). The change was undone.';
    default:
      return `The hub rejected a board change: ${msg}`;
  }
}

export function sessionOf(n: BoardNode): Session | undefined {
  const id = typeof n.data?.sessionId === 'string' ? n.data.sessionId : '';
  return id ? store.getState().sessions[id] : undefined;
}

export function isRunning(s: Session | undefined): boolean {
  return !!s && (s.status === 'running' || s.status === 'starting' || s.status === 'reconnecting');
}

type Listener = () => void;

const mergeable = (e: Entry) => e.undo.length === 1 && e.redo.length === 1 && e.undo[0].t === 'ops' && e.redo[0].t === 'ops';

export class BoardController {
  readonly workspaceId: string;
  readonly sync: BoardSync;
  project: Project;

  private undoStack: Entry[] = [];
  private redoStack: Entry[] = [];
  private idMap = new Map<string, string>();
  /** sessionId of a re-created terminal → node id it replaces in history. */
  private respawned = new Map<string, string>();
  /** Undo steps waiting for their consoles, by the node ids those had. */
  private restores: { waiting: Set<string>; ops: Op[] }[] = [];
  private historyListeners = new Set<Listener>();
  private selectionListeners = new Set<Listener>();
  private unsub: () => void;

  selection: ReadonlySet<string> = new Set();
  /** The tiles this client touched last. */
  readonly stack = new TileStack();

  constructor(workspaceId: string, project: Project) {
    this.workspaceId = workspaceId;
    this.project = project;
    this.sync = new BoardSync(workspaceId);
    this.sync.onRejected = (msg, code) => toast.error(boardRejection(msg, code));
    this.unsub = this.sync.subscribe(() => this.onState());
  }

  dispose() {
    this.unsub();
    this.sync.dispose();
    this.historyListeners.clear();
    this.selectionListeners.clear();
  }

  get nodes(): BoardNode[] {
    return this.sync.state.nodes;
  }
  node(id: string): BoardNode | undefined {
    return this.sync.state.nodes.find((n) => n.id === id);
  }

  private onState() {
    // remap re-created terminals; drop vanished nodes from the selection
    if (this.respawned.size) {
      for (const n of this.nodes) {
        if (n.kind !== 'terminal') continue;
        const sid = typeof n.data.sessionId === 'string' ? n.data.sessionId : '';
        const old = this.respawned.get(sid);
        if (old) {
          this.idMap.set(old, n.id);
          this.respawned.delete(sid);
          for (const r of this.restores) r.waiting.delete(old);
        }
      }
      this.flushRestores();
    }
    if (this.selection.size) {
      const ids = new Set(this.nodes.map((n) => n.id));
      const kept = [...this.selection].filter((id) => ids.has(id));
      if (kept.length !== this.selection.size) this.select(kept);
    }
  }

  // ---- selection ----

  subscribeSelection = (fn: Listener) => {
    this.selectionListeners.add(fn);
    return () => this.selectionListeners.delete(fn);
  };
  getSelection = () => this.selection;
  select(ids: Iterable<string>) {
    const next = new Set(ids);
    if (next.size === this.selection.size && [...next].every((id) => this.selection.has(id))) return;
    this.selection = next;
    this.selectionListeners.forEach((l) => l());
  }
  selectedNodes(): BoardNode[] {
    return this.nodes.filter((n) => this.selection.has(n.id));
  }

  // ---- history ----

  subscribeHistory = (fn: Listener) => {
    this.historyListeners.add(fn);
    return () => this.historyListeners.delete(fn);
  };
  private historyVersion = 0;
  getHistoryVersion = () => this.historyVersion;
  get canUndo() {
    return this.undoStack.length > 0;
  }
  get canRedo() {
    return this.redoStack.length > 0;
  }
  private historyChanged() {
    this.historyVersion++;
    this.historyListeners.forEach((l) => l());
  }

  private push(e: Entry) {
    const top = this.undoStack[this.undoStack.length - 1];
    if (e.merge && top?.merge === e.merge && e.at - top.at < MERGE_MS && mergeable(top) && mergeable(e)) {
      // keep the oldest undo; redo = everything since, folded per node
      const ops = (top.redo[0] as Extract<Step, { t: 'ops' }>).ops.slice();
      for (const op of (e.redo[0] as Extract<Step, { t: 'ops' }>).ops) queueOp(ops, op);
      top.redo = [{ t: 'ops', ops }];
      top.at = e.at;
    } else {
      this.undoStack.push(e);
      if (this.undoStack.length > HISTORY_LIMIT) this.undoStack.shift();
    }
    this.redoStack = [];
    this.historyChanged();
  }

  /** Forget the merge window so the next edit starts a new undo step. */
  breakMerge() {
    const top = this.undoStack[this.undoStack.length - 1];
    if (top) top.merge = undefined;
  }

  /** Drop the newest undo step if it carries this merge key (e.g. an empty text box). */
  forgetLast(merge: string) {
    const top = this.undoStack[this.undoStack.length - 1];
    if (top?.merge === merge) {
      this.undoStack.pop();
      this.historyChanged();
    }
  }

  clearHistory() {
    this.undoStack = [];
    this.redoStack = [];
    this.historyChanged();
  }

  private mapId = (id: string): string => {
    let cur = id;
    for (let i = 0; i < 20 && this.idMap.has(cur); i++) cur = this.idMap.get(cur)!;
    return cur;
  };

  private run(steps: Step[]) {
    for (const s of steps) {
      if (s.t === 'ops') this.sync.change(remapOps(s.ops, this.mapId));
      else if (s.t === 'kill') {
        for (const id of s.nodeIds) {
          const n = this.node(this.mapId(id));
          const sid = n && typeof n.data.sessionId === 'string' ? n.data.sessionId : '';
          if (sid) void this.killSession(sid);
        }
      } else if (s.t === 'closeApps') {
        for (const id of s.nodeIds) {
          const sid = this.node(this.mapId(id))?.data.sessionId;
          // already gone is fine: the tile only had a closed app
          if (typeof sid === 'string' && sid) void guiApi.stop(sid).catch(() => undefined);
        }
      } else {
        const restore = { waiting: new Set(s.specs.map((spec) => spec.nodeId)), ops: s.then };
        this.restores.push(restore);
        for (const spec of s.specs) {
          void this.spawn(spec, spec.nodeId).then((made) => {
            if (made) return;
            restore.waiting.delete(spec.nodeId);
            this.flushRestores();
          });
        }
        this.flushRestores();
      }
    }
  }

  private flushRestores() {
    const ready = this.restores.filter((r) => !r.waiting.size);
    if (!ready.length) return;
    this.restores = this.restores.filter((r) => r.waiting.size);
    for (const r of ready) {
      const ops = withoutMissing(remapOps(r.ops, this.mapId), this.nodes);
      if (ops.length) this.sync.change(ops);
    }
  }

  undo() {
    const e = this.undoStack.pop();
    if (!e) return;
    this.run(e.undo);
    this.redoStack.push(e);
    this.historyChanged();
  }

  redo() {
    const e = this.redoStack.pop();
    if (!e) return;
    this.run(e.redo);
    this.undoStack.push(e);
    this.historyChanged();
  }

  // ---- edits ----

  /** Apply ops (undoable unless history:false). */
  apply(ops: Op[], opts: ApplyOptions = {}) {
    if (ops.length === 0) return;
    if (opts.history !== false) {
      const inv = invertOps(this.sync.state, ops);
      this.push({ undo: [{ t: 'ops', ops: inv }], redo: [{ t: 'ops', ops }], merge: opts.merge, at: Date.now() });
    }
    this.sync.change(ops);
  }

  /** Live changes during a gesture (drag, resize); record() the undo step at the end. */
  transient(ops: Op[]) {
    this.sync.change(ops);
  }

  record(undo: Op[], redo: Op[], merge?: string) {
    if (undo.length === 0 && redo.length === 0) return;
    this.push({ undo: [{ t: 'ops', ops: undo }], redo: [{ t: 'ops', ops: redo }], merge, at: Date.now() });
  }

  patch(id: string, set: NodePatch, opts?: ApplyOptions) {
    this.apply([{ op: 'patch', id, set }], opts);
  }

  updateData(id: string, data: Record<string, unknown>, opts?: ApplyOptions) {
    this.apply([{ op: 'patch', id, set: { data } }], opts);
  }

  addNodes(nodes: BoardNode[], select = true) {
    if (nodes.length === 0) return;
    this.apply(nodes.map((node) => ({ op: 'put', node }) as Op));
    if (select) this.select(nodes.map((n) => n.id));
  }

  /** New node object with a fresh id. */
  make(kind: string, rect: NodeLayout, data: Record<string, unknown>): BoardNode {
    return {
      id: newId('n'),
      kind,
      x: Math.round(rect.x),
      y: Math.round(rect.y),
      w: Math.max(1, Math.round(rect.w)),
      h: Math.max(1, Math.round(rect.h)),
      data,
    };
  }

  zOrder(ids: string[], to: 'front' | 'back') {
    // keep the relative order of a multi-selection
    const order = this.nodes.filter((n) => ids.includes(n.id)).map((n) => n.id);
    const seq = to === 'front' ? order : order.reverse();
    // an explicit order outranks "touched last"
    for (const id of seq) {
      if (to === 'front') this.stack.raise(id);
      else this.stack.lower(id);
    }
    this.apply(seq.map((id) => ({ op: 'z', id, to }) as Op));
  }

  /**
   * Delete nodes; terminals end their session (undo starts a fresh one), app tiles close their app (undo offers to reopen it).
   * A group goes with its last members, whether or not it was selected; one that keeps some shrinks around them.
   */
  deleteNodes(ids: string[]) {
    const gone = new Set(ids);
    const kept = new Map<string, string[]>();
    for (const g of groupNodes(this.nodes)) {
      const { members } = readGroup(g);
      const rest = members.filter((m) => !gone.has(m));
      if (gone.has(g.id) || rest.length === members.length) continue;
      if (rest.length) kept.set(g.id, rest);
      else gone.add(g.id);
    }
    const nodes = this.nodes.filter((n) => gone.has(n.id));
    if (nodes.length === 0) return;
    const terms = nodes.filter((n) => n.kind === 'terminal');
    const others = nodes.filter((n) => n.kind !== 'terminal');
    const ops: Op[] = others.map((n) => ({ op: 'del', id: n.id }));
    for (const [id, members] of kept) ops.push({ op: 'patch', id, set: { data: { members } } });
    ops.push(...frameOps(this.nodes, kept.keys(), undefined, kept));
    // The hub removes terminals and their edges when their session ends; undo needs those edges back.
    const termIds = new Set(terms.map((n) => n.id));
    const inverse = invertOps(this.sync.state, [...ops, ...terms.map((n): Op => ({ op: 'del', id: n.id }))]).filter((op) => !(op.op === 'put' && termIds.has(op.node.id)));
    const specs = terms.map((n) => this.specOf(n)).filter((s): s is TerminalSpec => !!s);
    const undo: Step[] = terms.length ? [{ t: 'spawn', specs, then: inverse }] : [{ t: 'ops', ops: inverse }];
    const redo: Step[] = [];
    const apps = others.filter((n) => n.kind === 'app' || (n.kind === 'web' && n.data.sessionId));
    if (apps.length) redo.push({ t: 'closeApps', nodeIds: apps.map((n) => n.id) });
    if (ops.length) redo.push({ t: 'ops', ops });
    if (terms.length) redo.push({ t: 'kill', nodeIds: terms.map((n) => n.id) });
    this.run(redo);
    this.push({ undo, redo, at: Date.now() });
    this.select([...this.selection].filter((id) => !gone.has(id)));
  }

  // ---- terminals ----

  specOf(n: BoardNode): TerminalSpec | null {
    const s = sessionOf(n);
    if (!s) return null;
    return {
      nodeId: n.id,
      machineId: s.machineId,
      preset: s.agent || s.preset,
      cwd: s.cwd,
      // a re-created console gets a fresh handle; keep a custom title only
      name: s.name,
      rect: rectOf(n),
      // yolo is never re-enabled without a fresh confirmation: the hub default applies
      profileId: s.profileId || undefined,
      model: s.model ?? '',
      effort: s.effort ?? '',
    };
  }

  /** Start a terminal on this workspace; the hub adds its node at `rect`. */
  async spawn(spec: Omit<TerminalSpec, 'nodeId'>, replacesNodeId?: string): Promise<Session | null> {
    const { cols, rows } = guessTermSize(spec.rect.w, spec.rect.h);
    try {
      const s = await api.createSession({
        workspaceId: this.workspaceId,
        machineId: spec.machineId || undefined,
        preset: spec.preset,
        agent: spec.preset,
        yolo: spec.yolo,
        profileId: spec.profileId || undefined,
        model: spec.model,
        effort: spec.effort,
        launch: spec.launch,
        name: spec.name || undefined,
        cwd: spec.cwd || undefined,
        cols,
        rows,
        node: {
          x: Math.round(spec.rect.x),
          y: Math.round(spec.rect.y),
          w: Math.round(spec.rect.w),
          h: Math.round(spec.rect.h),
        },
      });
      store.upsertSession(s);
      if (replacesNodeId) {
        this.respawned.set(s.id, replacesNodeId);
        this.onState();
      }
      return s;
    } catch (err) {
      toast.error(`Could not start the terminal: ${errorMessage(err)}`);
      return null;
    }
  }

  private async killSession(sessionId: string) {
    try {
      await api.deleteSession(sessionId);
      store.removeSession(sessionId);
    } catch (err) {
      toast.error(`Could not close the terminal: ${errorMessage(err)}`);
    }
  }
}
