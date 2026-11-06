// One workspace board: React Flow canvas fed by the board document (ops synced with the
// hub), tools, rails, inspector, menus, clipboard, drops, keyboard shortcuts.

import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type DragEvent,
  type MouseEvent as ReactMouseEvent,
} from 'react';
import {
  Background,
  BackgroundVariant,
  MiniMap,
  Panel,
  ReactFlow,
  SelectionMode,
  useKeyPress,
  useReactFlow,
  useStoreApi,
  ViewportPortal,
  type EdgeChange,
  type NodeChange,
  type NodeTypes,
  type OnConnectEnd,
  type Viewport,
} from '@xyflow/react';
import { AgentFlyout, AllAgentsDialog } from '../agents/AgentPicker';
import { SpawnPanel } from './SpawnPanel';
import { byPriority } from '../agents/catalog';
import { setArmed } from '../agents/catalog';
import { jumpToNextNeedsYou, registerBoardFocus } from '../agents/jump';
import { currentShortcuts, useShortcuts } from '../agents/settings';
import { ResumeAllBanner } from '../agents/ResumeAll';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { FolderBrowser } from '../components/FolderBrowser';
import { Icon, type IconName } from '../components/Icon';
import { ContextMenu, type MenuEntry } from '../components/Menu';
import { toast } from '../components/Toast';
import { arrangeOps } from './arrange';
import { prefs } from '../lib/format';
import type { BoardNode, Op, Project, Workspace } from '../lib/types';
import { useLayout } from '../lib/layout';
import { useMediaQuery } from '../lib/useMediaQuery';
import { store, useMachine } from '../store';
import { duplicate, makePayload, parsePayload, pastePayload, recall, remember, serialize, type ClipPayload } from './clipboard';
import { BoardController, isRunning, rectOf, sessionOf } from './controller';
import { createTile, type InsertKind } from './create';
import { DrawLayer } from './DrawLayer';
import { DropDialog } from './DropDialog';
import { addPath, imageNodeFromBlob } from './files';
import { FlowCache, type FlowNode } from './flow';
import { cardOrder, focusCard, isFocusable, leaveFocus, useFocusedCard } from './focus';
import { ConnectionWire, EDGE_TYPES, EdgeCache, type FlowEdge } from './edges';
import { connectIntent, CONTENT_SOURCES, createGroupOps, dropMembershipOps, frameOps, groupNodes, groupsTouching, WIRE_SOURCES } from './groups';
import { GroupFrame } from './tiles/GroupFrame';
import { MemoryTile } from './tiles/MemoryTile';
import { OrchestratorTile } from './tiles/OrchestratorTile';
import { invertOps } from './ops';
import { sendTile } from '../orch/actions';
import { CommandBar } from '../orch/CommandBar';
import { orchStore, useOrch } from '../orch/store';
import { boardPath, navigate } from '../lib/router';
import type { UiAction } from '../orch/types';
import { alignmentGuides, boundsOf, type Guide } from './geometry';
import { Inspector } from './Inspector';
import { baseName, DEFAULT_SIZE, dirName, nodeLabel, readGroup, readNote } from './kinds';
import { DEFAULT_H, DEFAULT_W, MINIMAP_H, MINIMAP_W, placeNear } from './layout';
import { useNewTerminal, type TerminalChoice } from './NewTerminal';
import { NOTE_COLORS } from './palette';
import { InsertButtons, InsertRail, ToolRail, ZoomControls, type InsertItem } from './Rails';
import { RingGrid, RingMenu, type RingItem } from './RingMenu';
import { SnapshotsPanel } from './SnapshotsPanel';
import { TerminalNode } from './TerminalNode';
import { CalendarTile } from './tiles/CalendarTile';
import { UptimeTile } from './tiles/UptimeTile';
import { CredentialsTile } from './tiles/CredentialsTile';
import { ChecklistTile } from './tiles/ChecklistTile';
import { FileTile } from './tiles/FileTile';
import { ImageTile } from './tiles/ImageTile';
import { InkTile } from './tiles/InkTile';
import { KanbanTile } from './tiles/KanbanTile';
import { LineTile } from './tiles/LineTile';
import { NoteTile } from './tiles/NoteTile';
import { ShapeTile } from './tiles/ShapeTile';
import { TextTile } from './tiles/TextTile';
import { UnsupportedTile } from './tiles/UnsupportedTile';
import { BranchesTile } from '../git/BranchesTile';
import { EditorTile } from './tiles/EditorTile';
import { GitPill } from '../git/GitPill';
import { DocumentTile } from '../studio/documents/DocumentTile';
import { useDiagramRuns } from '../studio/diagrams/runs';
import { studioStore } from '../studio/store';
import { diagramApi } from '../studio/api';
import { usageUi } from '../usage/prefs';
import { useVoicePrefs } from '../voice/voice';
import { LinkTile } from '../web/LinkTile';
import { logCardFor, webNode } from '../web/model';
import { WebLogTile } from '../web/WebLogTile';
import { WebTile } from '../web/WebTile';
import type { GuiApp, GuiSession } from '../gui/api';
import { AppPicker } from '../gui/AppPicker';
import { AppTile } from '../gui/AppTile';
import { BoardEnvContext, UiStore, type BoardEnv, type Tool } from './ui';
import { deviceEntries, sizesInMenu } from './cardSize';

const MarkupEditor = lazy(() => import('./MarkupEditor'));
// Phase 6 panels: loaded on first use
const MissionMap = lazy(() => import('../orch/panels/MissionMap'));
const LoopsPanel = lazy(() => import('../orch/panels/LoopsPanel'));
const MemoryPanel = lazy(() => import('../orch/panels/MemoryPanel'));
const LayoutsPanel = lazy(() => import('../orch/panels/LayoutsPanel'));
const TeamComposer = lazy(() => import('../orch/panels/TeamComposer'));
const ComposeForm = lazy(() => import('../orch/panels/ComposeForm'));
const HandoffDialog = lazy(() => import('../orch/panels/HandoffDialog'));
// Phase 7 panels and studios: loaded on first use
const GitPanel = lazy(() => import('../git/GitPanel'));
const DiagramsPanel = lazy(() => import('../studio/diagrams/DiagramsPanel'));
const DrawingCards = lazy(() => import('../studio/diagrams/DrawingCards'));
const DocumentsStudio = lazy(() => import('../studio/documents/DocumentsStudio'));
const MarkdownStudio = lazy(() => import('../studio/markdown/MarkdownStudio'));
const SkillsStudio = lazy(() => import('../studio/skills/SkillsStudio'));

const NODE_TYPES: NodeTypes = {
  terminal: TerminalNode,
  note: NoteTile,
  text: TextTile,
  checklist: ChecklistTile,
  kanban: KanbanTile,
  calendar: CalendarTile,
  uptime: UptimeTile,
  credentials: CredentialsTile,
  shape: ShapeTile,
  line: LineTile,
  ink: InkTile,
  image: ImageTile,
  file: FileTile,
  nbgroup: GroupFrame,
  orchestrator: OrchestratorTile,
  memory: MemoryTile,
  web: WebTile,
  weblog: WebLogTile,
  link: LinkTile,
  branches: BranchesTile,
  editor: EditorTile,
  document: DocumentTile,
  app: AppTile,
  unsupported: UnsupportedTile,
};

/** Keep fitted nodes clear of the floating header (top), rails (sides) and dock (bottom). */
const FIT_PAD = { top: 132, bottom: 200, left: 88, right: 88 };
const FIT_OPTIONS = { maxZoom: 1, padding: { top: `${FIT_PAD.top}px`, bottom: `${FIT_PAD.bottom}px`, left: `${FIT_PAD.left}px`, right: `${FIT_PAD.right}px` } } as const;
const EXTENT: [[number, number], [number, number]] = [
  [-20000, -20000],
  [20000, 20000],
];
const EMPTY = new Set<string>();

// ---- per-client viewport (localStorage) ----
function loadViewport(wid: string): Viewport | undefined {
  try {
    const v = JSON.parse(prefs.get(`vp.${wid}`) ?? 'null') as Viewport | null;
    if (v && Number.isFinite(v.x) && Number.isFinite(v.y) && v.zoom >= 0.2 && v.zoom <= 3) return v;
  } catch {
    /* ignore */
  }
  return undefined;
}
function saveViewport(wid: string, v: Viewport) {
  prefs.set(`vp.${wid}`, JSON.stringify({ x: Math.round(v.x), y: Math.round(v.y), zoom: Math.round(v.zoom * 1000) / 1000 }));
}

/** Keys typed here belong to the element, not to board shortcuts. */
function isTyping(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el || !el.closest) return false;
  if (el.isContentEditable) return true;
  if (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return true;
  return !!el.closest('.xterm, [role="menu"], .modal-layer, .popover, .wb');
}
const modalOpen = () => !!document.querySelector('.modal-layer');

interface BoardViewProps {
  project: Project;
  workspace: Workspace;
  /** Session to bring into view once the board is loaded (live chip). */
  focusSession?: string | null;
}

export function BoardView({ project, workspace, focusSession }: BoardViewProps) {
  const [ctl, setCtl] = useState<BoardController | null>(null);
  useEffect(() => {
    const c = new BoardController(workspace.id, project);
    setCtl(c);
    void c.sync.load();
    const off = store.onEvent((ev) => {
      if (ev.type === 'boardOps' && ev.workspaceId === workspace.id) c.sync.receive(ev);
      else if (ev.type === 'snapshot') void c.sync.load(); // reconnected: we may have missed ops
    });
    return () => {
      off();
      c.dispose();
    };
    // the project object changes often (counts); the controller only needs the latest one
  }, [workspace.id]);
  if (ctl) ctl.project = project;
  if (!ctl) return <BoardLoading />;
  return <BoardCanvas ctl={ctl} project={project} focusSession={focusSession ?? null} />;
}

function BoardLoading() {
  return (
    <div className="board-status status" role="status">
      <span className="spinner" aria-hidden="true" />
      Loading board…
    </div>
  );
}

interface MenuState {
  x: number;
  y: number;
  at: { x: number; y: number };
  ids: string[] | null;
  /** Right-click on a wire / forward edge. */
  edge?: string;
  /** The menu starts with the sizes: a card without a header, or several cards. */
  sizes?: boolean;
}

function BoardCanvas({ ctl, project, focusSession }: { ctl: BoardController; project: Project; focusSession: string | null }) {
  const rf = useReactFlow<FlowNode>();
  const [ui] = useState(() => new UiStore());
  const state = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const status = useSyncExternalStore(ctl.sync.subscribe, () => ctl.sync.status);
  const syncError = useSyncExternalStore(ctl.sync.subscribe, () => ctl.sync.error);
  const selection = useSyncExternalStore(ctl.subscribeSelection, ctl.getSelection);
  useSyncExternalStore(ctl.subscribeHistory, ctl.getHistoryVersion);
  const uiState = useSyncExternalStore(ui.subscribe, ui.get);
  const { tool, editing, penColor, penWidth, panel, markup, filePicker, appPicker, mission, composer, handoff, connectFrom, studio } = uiState;
  const diagramRuns = useDiagramRuns(ctl.workspaceId);
  const voice = useVoicePrefs();
  const [edgeSel, setEdgeSel] = useState<ReadonlySet<string>>(EMPTY);
  const edgeCache = useRef(new EdgeCache());
  const edges = useMemo(() => edgeCache.current.build(state.edges, edgeSel), [state.edges, edgeSel]);
  const [connecting, setConnecting] = useState(false);
  const loopsRunning = useOrch((s) => Object.values(s.loops).filter((l) => l.projectId === project.id && l.state?.status === 'running').length);
  useEffect(() => {
    orchStore.install();
    void orchStore.loadLoops(project.id);
  }, [project.id]);

  const [dragging, setDragging] = useState<ReadonlySet<string>>(EMPTY);
  const [guides, setGuides] = useState<Guide[]>([]);
  const [menu, setMenu] = useState<MenuState | null>(null);
  const [studioMenu, setStudioMenu] = useState<{ x: number; y: number } | null>(null);
  const [confirmDel, setConfirmDel] = useState<{ ids: string[]; cut?: ClipPayload } | null>(null);
  const [drop, setDrop] = useState<{ files: File[]; at: { x: number; y: number } } | null>(null);
  const [dropHover, setDropHover] = useState(false);
  const canvasRef = useRef<HTMLDivElement>(null);
  const railRef = useRef<HTMLElement | null>(null);
  const imageInput = useRef<HTMLInputElement>(null);
  const imageAt = useRef<{ x: number; y: number } | null>(null);
  const pointer = useRef<{ x: number; y: number } | null>(null);
  const dragStart = useRef<Map<string, { x: number; y: number }> | null>(null);
  const dragFlow = useRef<Set<string>>(new Set());
  const pendingFocus = useRef<{ sessionId: string; frame: boolean; keyboard?: boolean } | null>(
    focusSession ? { sessionId: focusSession, frame: true, keyboard: true } : null,
  );
  const cache = useRef(new FlowCache());
  const compact = useMediaQuery('(max-width: 899px)');
  // a finger: one-finger drags pan, and every insert is a tap away (no right-click needed)
  const coarse = useLayout().input === 'touch';
  const machine = useMachine(project.machineId);
  const shortcuts = useShortcuts();
  const [spawnOpen, setSpawnOpen] = useState(false);
  const [allAgents, setAllAgents] = useState(false);

  const stackRev = useSyncExternalStore(ctl.stack.subscribe, ctl.stack.getRev);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const nodes = useMemo(() => cache.current.build(state.nodes, selection, dragging, editing, ctl.stack), [state.nodes, selection, dragging, editing, stackRev, ctl.stack]);
  const selectedNodes = useMemo(() => state.nodes.filter((n) => selection.has(n.id)), [state.nodes, selection]);

  // ---- focus mode (focus.ts): one card fills the view on this screen, the board is untouched ----
  const focusId = useFocusedCard();
  const focused = focusId ? state.nodes.find((n) => n.id === focusId && isFocusable(n)) : undefined;
  const focusedRef = useRef(focused);
  focusedRef.current = focused;
  const focusAreaRef = useRef<HTMLDivElement>(null);
  const [focusArea, setFocusArea] = useState<{ left: number; top: number; width: number; height: number } | null>(null);
  const viewBeforeFocus = useRef<Viewport | null>(null);
  const isFocusing = !!focused;
  useLayoutEffect(() => {
    if (!isFocusing) {
      setFocusArea(null);
      return;
    }
    const el = focusAreaRef.current;
    if (!el) return;
    // the area is CSS (.focus-area): the bars and safe areas of this layout, measured here
    const measure = () => {
      const r = el.getBoundingClientRect();
      setFocusArea((a) => (a && a.left === r.left && a.top === r.top && a.width === r.width && a.height === r.height ? a : { left: r.left, top: r.top, width: r.width, height: r.height }));
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    window.addEventListener('resize', measure);
    return () => {
      ro.disconnect();
      window.removeEventListener('resize', measure);
    };
  }, [isFocusing]);
  useEffect(() => {
    if (!focused || !focusArea) return;
    if (!viewBeforeFocus.current) viewBeforeFocus.current = rf.getViewport();
    void rf.setViewport({ x: focusArea.left - focused.x, y: focusArea.top - focused.y, zoom: 1 });
  }, [focused?.id, focused?.x, focused?.y, focusArea, rf]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (focused) {
      if (!ctl.selection.has(focused.id) || ctl.selection.size > 1) ctl.select([focused.id]);
      return;
    }
    const before = viewBeforeFocus.current;
    viewBeforeFocus.current = null;
    if (before) void rf.setViewport(before, { duration: 200 });
  }, [focused?.id, ctl, rf]); // eslint-disable-line react-hooks/exhaustive-deps
  // a card that went away (deleted here or elsewhere) takes focus mode with it
  useEffect(() => {
    if (focusId && status === 'ready' && !focused) leaveFocus();
  }, [focusId, status, focused]);
  useEffect(() => {
    document.documentElement.toggleAttribute('data-card-focus', isFocusing);
    return () => document.documentElement.removeAttribute('data-card-focus');
  }, [isFocusing]);
  const shownNodes = useMemo(() => {
    if (!focused || !focusArea) return nodes;
    return nodes.map((f) =>
      f.id !== focused.id
        ? f
        : { ...f, width: focusArea.width, height: focusArea.height, measured: { width: focusArea.width, height: focusArea.height }, zIndex: 100000, draggable: false, className: 'is-focused' },
    );
  }, [nodes, focused, focusArea]);

  // ---- touch: select several by tapping, as Shift does with a mouse ----
  const flowStore = useStoreApi<FlowNode>();
  const [picking, setPicking] = useState(false);
  useEffect(() => {
    flowStore.setState({ multiSelectionActive: picking });
  }, [picking, flowStore]);

  // ---- geometry helpers ----
  const viewCenter = useCallback(() => {
    const box = canvasRef.current?.getBoundingClientRect();
    if (!box) return { x: 0, y: 0 };
    return rf.screenToFlowPosition({ x: box.left + box.width / 2, y: box.top + box.height / 2 });
  }, [rf]);

  const frame = useCallback(
    (id: string) => {
      railRef.current = document.querySelector<HTMLElement>('.rail-left');
      const n = ctl.node(id);
      const box = canvasRef.current?.getBoundingClientRect();
      if (!n || !box) return;
      // leave room for the rails / header / dock, less on small screens; narrow screens
      // only have the left rail, so centre in the area to its right
      const narrow = box.width < 900;
      const inset = narrow ? (railRef.current?.getBoundingClientRect().right ?? 0) : 0;
      const padX = Math.min(200, box.width * 0.12);
      const padY = Math.min(260, box.height * 0.3);
      const zoom = Math.min(1.5, Math.max(0.2, Math.min((box.width - inset - padX) / n.w, (box.height - padY) / n.h)));
      void rf.setCenter(n.x + n.w / 2 - inset / 2 / zoom, n.y + n.h / 2, { zoom, duration: 250 });
    },
    [ctl, rf],
  );

  const fit = useCallback(() => void rf.fitView({ ...FIT_OPTIONS, duration: 250 }), [rf]);

  const [arranging, setArranging] = useState(false);
  const arrange = useCallback(() => {
    // the shape of what stays free between the header, the rails and the dock, as `fit` sees it
    const box = canvasRef.current?.getBoundingClientRect();
    const freeW = (box?.width ?? 0) - FIT_PAD.left - FIT_PAD.right;
    const freeH = (box?.height ?? 0) - FIT_PAD.top - FIT_PAD.bottom;
    const ops = arrangeOps(ctl.nodes, ctl.sync.state.edges, freeW > 0 && freeH > 0 ? freeW / freeH : 16 / 9);
    if (!ops.length) {
      toast.info('The board is already tidy.');
      return;
    }
    setArranging(true); // cards glide to their place, so that it can be followed
    ctl.apply(ops);
    window.setTimeout(fit, 40);
    window.setTimeout(() => setArranging(false), 400);
    toast.notify('success', 'Board arranged', 'Nothing overlaps now.', { label: 'Undo', onClick: () => ctl.undo() });
  }, [ctl, fit]);

  /** Bring a board rect into view when it isn't fully visible (diagrams that just landed, resized cards). */
  const revealRect = useCallback(
    (r: { x: number; y: number; w: number; h: number }) => {
      const box = canvasRef.current?.getBoundingClientRect();
      if (!box) return;
      const a = rf.screenToFlowPosition({ x: box.left + 60, y: box.top + 80 });
      const b = rf.screenToFlowPosition({ x: box.right - 60, y: box.bottom - 120 });
      if (r.x >= a.x && r.y >= a.y && r.x + r.w <= b.x && r.y + r.h <= b.y) return;
      window.setTimeout(() => void rf.fitBounds({ x: r.x, y: r.y, width: r.w, height: r.h }, { padding: 0.25, duration: 300 }), 60);
    },
    [rf],
  );

  const nodeMenuRef = useRef<(ids: string[], withDelete: boolean) => MenuEntry[]>(() => []);
  const env = useMemo<BoardEnv>(
    () => ({
      ctl,
      ui,
      project,
      viewCenter,
      frame,
      reveal: revealRect,
      edgeMenu: (id, x, y) => {
        setEdgeSel(new Set([id]));
        setMenu({ x, y, at: rf.screenToFlowPosition({ x, y }), ids: null, edge: id });
      },
      cardMenu: (id, withDelete) => nodeMenuRef.current([id], withDelete),
    }),
    [ctl, ui, project, viewCenter, frame, revealRect, rf],
  );

  // ---- terminals ----
  // select (and on narrow screens frame) a terminal once its node shows up — the hub's
  // boardOps may arrive before or after the POST /api/sessions response
  const focusPending = useCallback(() => {
    const p = pendingFocus.current;
    if (!p || ctl.sync.status !== 'ready') return;
    const n = ctl.nodes.find((x) => x.kind === 'terminal' && x.data.sessionId === p.sessionId);
    if (!n) {
      // an agent opened inside the editor: the card shows it (agents/jump.ts)
      if (store.getState().sessions[p.sessionId]?.panel) {
        pendingFocus.current = null;
        openCodeRef.current();
      }
      return;
    }
    pendingFocus.current = null;
    ctl.select([n.id]);
    if (p.frame) window.setTimeout(() => frame(n.id), 60);
    if (p.keyboard) focusTerminal(n.id, 340);
  }, [ctl, frame]);
  useEffect(focusPending, [state.nodes, status, focusPending]);

  // needs-you chip / N / notifications: frame a console of this board and give it the keyboard
  useEffect(
    () =>
      registerBoardFocus(ctl.workspaceId, (sessionId) => {
        const n = ctl.nodes.find((x) => x.kind === 'terminal' && x.data.sessionId === sessionId);
        if (!n) {
          // an agent opened inside the editor has no tile: it lives in the editor card
          if (!store.getState().sessions[sessionId]?.panel) return false;
          openCodeRef.current();
          return true;
        }
        ctl.select([n.id]);
        frame(n.id);
        focusTerminal(n.id, 300);
        return true;
      }),
    [ctl, frame],
  );
  const spawnAt = useCallback(
    async (choice: TerminalChoice, center: { x: number; y: number }) => {
      if (!choice.preset) {
        toast.error('Pick an agent in the New terminal dock first.');
        return false;
      }
      const rect = placeNear(ctl.nodes.map(rectOf), center, DEFAULT_W, DEFAULT_H);
      const s = await ctl.spawn({
        machineId: choice.machineId,
        preset: choice.preset,
        name: choice.name,
        cwd: choice.cwd,
        rect,
        yolo: choice.yolo,
        profileId: choice.profileId,
        model: choice.model,
        effort: choice.effort,
      });
      if (s) {
        pendingFocus.current = { sessionId: s.id, frame: compact };
        focusPending();
      }
      return !!s;
    },
    [ctl, compact, focusPending],
  );
  const nt = useNewTerminal(project, (c) => spawnAt(c, viewCenter()));

  // ---- Spawn tool (T): the armed agent + its flyout ----
  const openSpawn = useCallback(() => {
    if (spawnOpen) {
      setSpawnOpen(false);
      if (ui.get().tool === 'spawn') ui.setTool('select');
      return;
    }
    setSpawnOpen(true);
    // armed (and installed here): the tool is ready right away; otherwise pick first
    if (nt.armed && nt.agentId === nt.armed) ui.setTool('spawn');
    else if (ui.get().tool !== 'select') ui.setTool('select');
  }, [spawnOpen, ui, nt.armed, nt.agentId]);
  const openSpawnRef = useRef(openSpawn);
  openSpawnRef.current = openSpawn;


  // ---- deletion (asks before ending running terminals) ----
  const requestDelete = useCallback(
    (ids: string[], cut?: ClipPayload) => {
      const running = ctl.nodes.filter((n) => ids.includes(n.id) && n.kind === 'terminal' && isRunning(sessionOf(n)));
      const cards = ctl.nodes.some((n) => ids.includes(n.id) && n.kind === 'orchestrator');
      if (running.length || cards) setConfirmDel({ ids, cut });
      else ctl.deleteNodes(ids);
    },
    [ctl],
  );

  // ---- groups (Ctrl+G / Ctrl+Shift+G) ----
  const groupSelection = useCallback(() => {
    const terms = ctl.selectedNodes().filter((n) => n.kind === 'terminal');
    if (!terms.length) {
      toast.info('Select one or more consoles first, then press Ctrl+G to group them.');
      return;
    }
    const r = createGroupOps(ctl.nodes, terms.map((n) => n.id));
    if (!r) return;
    ctl.apply(r.ops);
    ctl.select([r.group.id]);
    toast.success(`Grouped ${terms.length === 1 ? '1 console' : `${terms.length} consoles`} as ${readGroup(r.group).name}. Double-click the name to rename it.`);
  }, [ctl]);
  const ungroupSelection = useCallback(() => {
    const sel = ctl.selectedNodes();
    const ids = new Set(sel.filter((n) => n.kind === 'group').map((n) => n.id));
    for (const n of sel) if (n.kind === 'terminal') for (const g of groupsTouching(ctl.nodes, [n.id])) ids.add(g);
    if (!ids.size) return;
    ctl.apply([...ids].map((id) => ({ op: 'del', id }) as Op));
    toast.success(ids.size === 1 ? 'Group dissolved; its consoles keep running.' : `${ids.size} groups dissolved.`);
  }, [ctl]);

  // ---- wires & forwards ----
  const connectTo = useCallback(
    (fromId: string, toId: string) => {
      const intent = connectIntent(ctl.nodes, ctl.sync.state.edges, fromId, toId);
      if (intent.ok) {
        ctl.apply([intent.op]);
        if (intent.sends) void sendTile(ctl.workspaceId, fromId, [toId]);
        return true;
      }
      if (intent.reason) toast.info(intent.reason);
      return false;
    },
    [ctl],
  );
  const onConnectEnd: OnConnectEnd = useCallback(
    (e, st) => {
      setConnecting(false);
      const fromId = st.fromNode?.id;
      if (!fromId) return;
      let toId = st.toNode?.id && st.toNode.id !== fromId ? st.toNode.id : '';
      const pt = 'changedTouches' in e ? e.changedTouches[0] : e;
      if (!toId && pt) {
        for (const el of document.elementsFromPoint(pt.clientX, pt.clientY)) {
          const id = (el as HTMLElement).closest?.('.react-flow__node')?.getAttribute('data-id');
          if (id && id !== fromId) {
            toId = id;
            break;
          }
        }
        if (!toId) {
          // group frames ignore the pointer: hit-test their geometry
          const p = rf.screenToFlowPosition({ x: pt.clientX, y: pt.clientY });
          toId = [...groupNodes(ctl.nodes)].reverse().find((g) => g.id !== fromId && p.x >= g.x && p.x <= g.x + g.w && p.y >= g.y && p.y <= g.y + g.h)?.id ?? '';
        }
      }
      if (toId) connectTo(fromId, toId);
    },
    [ctl, rf, connectTo],
  );

  // ---- clipboard ----
  const pasteTarget = useCallback(
    (p: ClipPayload) => {
      const box = boundsOf(p.nodes.map(rectOf));
      if (!box) return undefined;
      const ptr = pointer.current;
      if (ptr) return { x: ptr.x - Math.min(box.w, 400) / 2, y: ptr.y - Math.min(box.h, 300) / 2 };
      const ids = new Set(ctl.nodes.map((n) => n.id));
      if (p.nodes.some((n) => ids.has(n.id))) return undefined; // same board: offset a little
      const c = viewCenter();
      return { x: c.x - box.w / 2, y: c.y - box.h / 2 };
    },
    [ctl, viewCenter],
  );

  const copyIds = useCallback(
    (ids: string[]) => {
      const p = makePayload(ctl, ids);
      if (!p) return null;
      remember(p);
      void navigator.clipboard?.writeText(serialize(p)).catch(() => undefined);
      return p;
    },
    [ctl],
  );

  const pasteImages = useCallback(
    async (files: File[], at: { x: number; y: number }) => {
      const made: BoardNode[] = [];
      let x = at.x;
      for (const f of files) {
        const n = await imageNodeFromBlob(ctl, f, { x, y: at.y }, f.name && f.name !== 'image.png' ? f.name : '', made.length === 0);
        if (n) {
          made.push(n);
          x = n.x + n.w + 32 + n.w / 2;
        }
      }
      if (made.length) ctl.addNodes(made);
    },
    [ctl],
  );

  useEffect(() => {
    const onCopy = (e: ClipboardEvent, cut: boolean) => {
      if (isTyping(e.target) || modalOpen()) return;
      const ts = window.getSelection();
      if (ts && !ts.isCollapsed && ts.toString().trim()) return; // copying page text (e.g. a markdown card)
      const ids = [...ctl.selection];
      if (!ids.length) return;
      const p = makePayload(ctl, ids);
      if (!p) return;
      e.preventDefault();
      e.clipboardData?.setData('text/plain', serialize(p));
      remember(p);
      if (cut) requestDelete(ids, p);
    };
    const onPaste = (e: ClipboardEvent) => {
      if (isTyping(e.target) || modalOpen()) return;
      const dt = e.clipboardData;
      if (!dt) return;
      const at = pointer.current ?? viewCenter();
      const images = Array.from(dt.files).filter((f) => f.type.startsWith('image/'));
      if (images.length) {
        e.preventDefault();
        void pasteImages(images, at);
        return;
      }
      const text = dt.getData('text/plain');
      const p = parsePayload(text) ?? (!text ? recall() : null);
      if (p) {
        e.preventDefault();
        pastePayload(ctl, p, pasteTarget(p));
        return;
      }
      if (text.trim()) {
        e.preventDefault();
        const node = createTile(ctl, ui, 'text', at);
        ctl.updateData(node.id, { text: text.slice(0, 20000) }, { merge: `text:${node.id}` });
        ui.set({ editing: null });
      }
    };
    const copy = (e: ClipboardEvent) => onCopy(e, false);
    const cut = (e: ClipboardEvent) => onCopy(e, true);
    document.addEventListener('copy', copy);
    document.addEventListener('cut', cut);
    document.addEventListener('paste', onPaste);
    return () => {
      document.removeEventListener('copy', copy);
      document.removeEventListener('cut', cut);
      document.removeEventListener('paste', onPaste);
    };
  }, [ctl, ui, requestDelete, pasteTarget, pasteImages, viewCenter]);

  // ---- keyboard shortcuts (only when the board has the keyboard) ----
  const nudgeTimer = useRef(0);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || isTyping(e.target) || modalOpen()) return;
      if (e.key === 'Escape' && focusedRef.current) {
        e.preventDefault();
        leaveFocus();
        return;
      }
      const mod = e.ctrlKey || e.metaKey;
      const ids = [...ctl.selection];
      const key = e.key;
      if (mod && !e.altKey) {
        const k = key.toLowerCase();
        if (k === 'z') {
          e.preventDefault();
          if (e.shiftKey) ctl.redo();
          else ctl.undo();
        } else if (k === 'y') {
          e.preventDefault();
          ctl.redo();
        } else if (k === 'a') {
          e.preventDefault();
          ctl.select(ctl.nodes.map((n) => n.id));
        } else if (k === 'd') {
          e.preventDefault();
          if (ids.length) duplicate(ctl, ids);
        } else if (k === '=' || k === '+') {
          e.preventDefault();
          void rf.zoomTo(Math.min(3, rf.getZoom() * 1.2));
        } else if (k === '-' || k === '_') {
          e.preventDefault();
          void rf.zoomTo(Math.max(0.2, rf.getZoom() / 1.2));
        } else if (k === '0') {
          e.preventDefault();
          void rf.zoomTo(1, { duration: 150 });
        } else if (k === 'g') {
          e.preventDefault();
          if (e.shiftKey) ungroupSelection();
          else groupSelection();
        }
        return;
      }
      if (e.altKey) return;
      if (key === '!' || (e.shiftKey && e.code === 'Digit1')) {
        e.preventDefault();
        fit();
        return;
      }
      if (key === 'Escape') {
        if (ui.get().connectFrom) ui.set({ connectFrom: null });
        else if (ui.get().tool !== 'select') ui.setTool('select');
        else if (ui.get().panel) ui.set({ panel: null });
        else {
          ctl.select([]);
          setEdgeSel(EMPTY);
        }
        return;
      }
      if ((key === 'Delete' || key === 'Backspace') && edgeSelRef.current.size && !ids.length) {
        e.preventDefault();
        ctl.apply([...edgeSelRef.current].map((id) => ({ op: 'delEdge', id }) as Op));
        setEdgeSel(EMPTY);
        return;
      }
      if ((key === 'Delete' || key === 'Backspace') && ids.length) {
        e.preventDefault();
        requestDelete(ids);
        return;
      }
      if (key.startsWith('Arrow') && ids.length) {
        e.preventDefault();
        const step = e.shiftKey ? 10 : 1;
        const dx = key === 'ArrowLeft' ? -step : key === 'ArrowRight' ? step : 0;
        const dy = key === 'ArrowUp' ? -step : key === 'ArrowDown' ? step : 0;
        // a selected group moves with its members; frames follow moved members
        const move = new Set<string>();
        for (const n of ctl.nodes) {
          if (!ctl.selection.has(n.id) || n.locked) continue;
          if (n.kind === 'group') readGroup(n).members.forEach((m) => move.add(m));
          else move.add(n.id);
        }
        const rects = new Map<string, { x: number; y: number; w: number; h: number }>();
        const ops: Op[] = ctl.nodes
          .filter((n) => move.has(n.id))
          .map((n) => {
            rects.set(n.id, { x: n.x + dx, y: n.y + dy, w: n.w, h: n.h });
            return { op: 'patch', id: n.id, set: { x: n.x + dx, y: n.y + dy } } as Op;
          });
        ops.push(...frameOps(ctl.nodes, groupsTouching(ctl.nodes, move), rects));
        // consecutive nudges within a moment are one undo step
        const now = Date.now();
        const merge = now - nudgeTimer.current < 1200 ? `nudge:${ids.join(',')}` : `nudge:${ids.join(',')}:${now}`;
        nudgeTimer.current = now;
        ctl.apply(ops, { merge });
        return;
      }
      if (e.shiftKey && key.length === 1 && /[a-z]/i.test(key)) return;
      const sc = currentShortcuts();
      const k = key.length === 1 ? key.toLowerCase() : '';
      if (!k) return;
      if (k === 'x') {
        // Mission Map: fixed key (not rebindable)
        e.preventDefault();
        ui.set({ mission: !ui.get().mission });
        return;
      }
      if (k === sc.loops.toLowerCase() || k === sc.memory.toLowerCase()) {
        e.preventDefault();
        const p = k === sc.loops.toLowerCase() ? 'loops' : 'memory';
        ui.set({ panel: ui.get().panel === p ? null : p });
        return;
      }
      if (k === sc.needsYou.toLowerCase()) {
        e.preventDefault();
        jumpToNextNeedsYou();
        return;
      }
      if (k === sc.git.toLowerCase()) {
        e.preventDefault();
        ui.set({ panel: ui.get().panel === 'git' ? null : 'git' });
        return;
      }
      if (k === sc.markdown.toLowerCase()) {
        e.preventDefault();
        ui.set({ studio: { kind: 'markdown' } });
        return;
      }
      if (k === sc.skills.toLowerCase()) {
        e.preventDefault();
        ui.set({ studio: { kind: 'skills' } });
        return;
      }
      if (k === sc.spawn.toLowerCase()) {
        e.preventDefault();
        openSpawnRef.current();
        return;
      }
      const tools: Record<string, Tool> = { [sc.select.toLowerCase()]: 'select', [sc.hand.toLowerCase()]: 'hand', [sc.pen.toLowerCase()]: 'pen', [sc.eraser.toLowerCase()]: 'eraser' };
      const t = tools[k];
      if (t) {
        e.preventDefault();
        setSpawnOpen(false);
        ui.setTool(t);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [ctl, ui, rf, fit, requestDelete, groupSelection, ungroupSelection]);
  const edgeSelRef = useRef(edgeSel);
  edgeSelRef.current = edgeSel;

  // tool hint
  const agentName = nt.agent?.name ?? 'the terminal';
  useEffect(() => {
    if (tool === 'spawn') toast.info(`Click on the board to place ${agentName}. Esc cancels.`);
    else setSpawnOpen(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tool]);

  // Two fingers move and zoom the board wherever they land, over a console or an editor too: one
  // finger there belongs to the card (scrolling, selecting), so React Flow leaves those touches
  // alone and this takes the pair. On the empty board React Flow does it itself.
  useEffect(() => {
    const el = canvasRef.current;
    if (!el || !coarse) return;
    let g: { d0: number; mx: number; my: number; vp: Viewport } | null = null;
    const onCard = (t: EventTarget | null) => t instanceof Element && !!t.closest('.react-flow__node') && !!t.closest('.nopan, .nowheel, .nodrag');
    const pair = (e: TouchEvent) => {
      const [a, b] = [e.touches[0], e.touches[1]];
      return { x: (a.clientX + b.clientX) / 2, y: (a.clientY + b.clientY) / 2, d: Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY) };
    };
    const start = (e: TouchEvent) => {
      if (e.touches.length !== 2 || focusedRef.current) return;
      if (!Array.from(e.touches).some((t) => onCard(t.target))) return;
      const m = pair(e);
      g = { d0: Math.max(1, m.d), mx: m.x, my: m.y, vp: rf.getViewport() };
      e.preventDefault();
    };
    const move = (e: TouchEvent) => {
      if (!g || e.touches.length !== 2) return;
      e.preventDefault();
      const m = pair(e);
      const zoom = Math.min(3, Math.max(0.2, (g.vp.zoom * m.d) / g.d0));
      // the board point that was under the fingers stays under them
      const fx = (g.mx - g.vp.x) / g.vp.zoom;
      const fy = (g.my - g.vp.y) / g.vp.zoom;
      void rf.setViewport({ x: m.x - fx * zoom, y: m.y - fy * zoom, zoom });
    };
    const end = (e: TouchEvent) => {
      if (!g || e.touches.length >= 2) return;
      g = null;
      saveViewport(ctl.workspaceId, rf.getViewport());
    };
    el.addEventListener('touchstart', start, { capture: true, passive: false });
    el.addEventListener('touchmove', move, { capture: true, passive: false });
    el.addEventListener('touchend', end, true);
    el.addEventListener('touchcancel', end, true);
    return () => {
      el.removeEventListener('touchstart', start, true);
      el.removeEventListener('touchmove', move, true);
      el.removeEventListener('touchend', end, true);
      el.removeEventListener('touchcancel', end, true);
    };
  }, [coarse, rf, ctl]);

  // ---- React Flow events ----
  const onNodesChange = useCallback(
    (changes: NodeChange<FlowNode>[]) => {
      const ops: Op[] = [];
      let sel: Set<string> | null = null;
      const flowMoved = new Set(changes.filter((c) => c.type === 'position').map((c) => c.id));
      const resized = new Map<string, { x: number; y: number; w: number; h: number }>();
      for (const c of changes) {
        if (c.type === 'position' && c.position) {
          const n = ctl.node(c.id);
          const x = Math.round(c.position.x);
          const y = Math.round(c.position.y);
          if (n && (n.x !== x || n.y !== y)) {
            ops.push({ op: 'patch', id: c.id, set: { x, y } });
            // dragging a group's header moves the whole team (one request)
            if (n.kind === 'group') {
              const dx = x - n.x;
              const dy = y - n.y;
              for (const m of readGroup(n).members) {
                if (flowMoved.has(m) || dragFlow.current.has(m)) continue;
                const mn = ctl.node(m);
                if (mn && !mn.locked) ops.push({ op: 'patch', id: m, set: { x: mn.x + dx, y: mn.y + dy } });
              }
            }
          }
        } else if (c.type === 'dimensions' && c.dimensions && (c.resizing || c.setAttributes)) {
          const n = ctl.node(c.id);
          const w = Math.max(1, Math.round(c.dimensions.width));
          const h = Math.max(1, Math.round(c.dimensions.height));
          if (n && (n.w !== w || n.h !== h)) {
            ops.push({ op: 'patch', id: c.id, set: { w, h } });
            if (n.kind === 'terminal') resized.set(n.id, { x: n.x, y: n.y, w, h });
          }
        } else if (c.type === 'select') {
          sel ??= new Set(ctl.selection);
          if (c.selected) sel.add(c.id);
          else sel.delete(c.id);
        }
      }
      // a resized member stretches its frame live
      if (resized.size) ops.push(...frameOps(ctl.nodes, groupsTouching(ctl.nodes, resized.keys()), resized));
      if (ops.length) ctl.transient(ops);
      if (sel) ctl.select(sel);
    },
    [ctl],
  );

  const onDragStart = useCallback(
    (_e: unknown, dragged: FlowNode[]) => {
      const m = new Map<string, { x: number; y: number }>();
      for (const d of dragged) {
        const n = ctl.node(d.id);
        if (!n) continue;
        m.set(n.id, { x: n.x, y: n.y });
        // members of a dragged group move too (undo needs their start)
        if (n.kind === 'group')
          for (const id of readGroup(n).members) {
            const mn = ctl.node(id);
            if (mn) m.set(id, { x: mn.x, y: mn.y });
          }
      }
      dragStart.current = m;
      dragFlow.current = new Set(dragged.map((d) => d.id));
      setDragging(new Set(m.keys()));
      ui.set({ editing: null });
    },
    [ctl, ui],
  );
  const onDrag = useCallback(
    (_e: unknown, dragged: FlowNode[]) => {
      const ids = new Set(dragged.map((d) => d.id));
      const box = boundsOf(dragged.map((d) => ({ x: d.position.x, y: d.position.y, w: d.width ?? 0, h: d.height ?? 0 })));
      if (!box) return;
      const others = ctl.nodes.filter((n) => !ids.has(n.id) && n.kind !== 'ink').slice(-400).map(rectOf);
      setGuides(alignmentGuides(box, others, 6 / rf.getZoom()));
    },
    [ctl, rf],
  );
  const onDragStop = useCallback(() => {
    const start = dragStart.current;
    const flow = dragFlow.current;
    dragStart.current = null;
    dragFlow.current = new Set();
    setDragging(EMPTY);
    setGuides([]);
    if (!start) return;
    const undo: Op[] = [];
    const redo: Op[] = [];
    for (const [id, p] of start) {
      const n = ctl.node(id);
      if (!n || (n.x === p.x && n.y === p.y)) continue;
      undo.push({ op: 'patch', id, set: { x: p.x, y: p.y } });
      redo.push({ op: 'patch', id, set: { x: n.x, y: n.y } });
    }
    // consoles dropped into / out of a group frame change membership (§1)
    const draggedGroups = new Set([...flow].filter((id) => ctl.node(id)?.kind === 'group'));
    const movedTerms = [...flow].filter((id) => {
      const n = ctl.node(id);
      if (n?.kind !== 'terminal') return false;
      const p = start.get(id);
      const own = groupsTouching(ctl.nodes, [id]);
      return !!p && (n.x !== p.x || n.y !== p.y) && !own.some((g) => draggedGroups.has(g));
    });
    if (movedTerms.length) {
      const { ops, full } = dropMembershipOps(ctl.nodes, movedTerms);
      if (full.length) toast.info(`${full[0]} already has 12 consoles.`);
      if (ops.length) {
        undo.unshift(...invertOps(ctl.sync.state, ops));
        ctl.transient(ops);
        redo.push(...ops);
      }
    }
    ctl.record(undo, redo);
  }, [ctl]);

  const onNodeDoubleClick = useCallback(
    (e: ReactMouseEvent, node: FlowNode) => {
      const n = node.data.node;
      if (n.kind === 'note' || n.kind === 'text' || n.kind === 'shape') {
        ui.set({ editing: n.id });
        return;
      }
      // a double click inside a console or an editor picks a word; the board must not move under it
      if ((e.target as HTMLElement).closest('.nodrag')) return;
      frame(n.id);
      if (n.kind === 'terminal' && (e.target as HTMLElement).closest('.nb-node-header')) {
        window.setTimeout(() => {
          document.querySelector<HTMLTextAreaElement>(`.react-flow__node[data-id="${n.id}"] .xterm-helper-textarea`)?.focus();
        }, 280);
      }
    },
    [ui, frame],
  );

  const openMenu = useCallback(
    (clientX: number, clientY: number, ids: string[] | null, sizes = false) => {
      const p = rf.screenToFlowPosition({ x: clientX, y: clientY });
      setMenu({ x: clientX, y: clientY, at: p, ids, sizes });
    },
    [rf],
  );

  // ---- inserts ----
  /** Insert at a point (menus), or in the free spot nearest the view centre (rails). */
  const insertAt = useCallback(
    (kind: InsertKind, at?: { x: number; y: number }) => {
      if (ui.get().tool !== 'select') ui.set({ tool: 'select' });
      if (at) {
        createTile(ctl, ui, kind, at);
        return;
      }
      const size = DEFAULT_SIZE[kind] ?? DEFAULT_SIZE.shape;
      const rect = placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), size.w, size.h);
      createTile(ctl, ui, kind, { x: rect.x + rect.w / 2, y: rect.y + rect.h / 2 }, rect);
    },
    [ctl, ui, viewCenter],
  );
  const pickImage = useCallback((at: { x: number; y: number }) => {
    imageAt.current = at;
    imageInput.current?.click();
  }, []);

  const togglePanel = (p: Exclude<typeof panel, null>) => ui.set({ panel: panel === p ? null : p });
  /** A web tile (empty = port dial) or the branches card, placed like other inserts. */
  const insertNode = useCallback(
    (kind: 'web' | 'branches' | 'app' | 'editor', at?: { x: number; y: number }, url = '', appData?: Record<string, unknown>) => {
      if (ui.get().tool !== 'select') ui.set({ tool: 'select' });
      const size = DEFAULT_SIZE[kind];
      const rect = at
        ? { x: at.x - size.w / 2, y: at.y - size.h / 2, w: size.w, h: size.h }
        : placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), size.w, size.h);
      const node = kind === 'web' ? webNode(ctl, rect, url) : kind === 'app' ? ctl.make('app', rect, appData ?? {}) : kind === 'editor' ? ctl.make('editor', rect, {}) : ctl.make('branches', rect, { projectId: project.id });
      ctl.addNodes([node]);
      // a big tile that landed (partly) outside the view gets framed
      const box = canvasRef.current?.getBoundingClientRect();
      if (box) {
        const a = rf.screenToFlowPosition({ x: box.left + 60, y: box.top + 80 });
        const b = rf.screenToFlowPosition({ x: box.right - 60, y: box.bottom - 120 });
        if (rect.x < a.x || rect.y < a.y || rect.x + rect.w > b.x || rect.y + rect.h > b.y) window.setTimeout(() => frame(node.id), 60);
      }
      return node;
    },
    [ctl, ui, viewCenter, project.id, rf, frame],
  );
  /** Each app has one session: a tile of it on this board takes the session, else a new tile. */
  const placeApp = useCallback(
    (app: GuiApp, session: GuiSession, at?: { x: number; y: number }) => {
      const existing = ctl.nodes.find((n) => (n.kind === 'app' || n.kind === 'web') && n.data.appId === app.id);
      if (!existing) {
        insertNode('app', at, '', { appId: app.id, name: app.name, sessionId: session.id });
        return;
      }
      ctl.apply([{ op: 'patch', id: existing.id, set: { data: { sessionId: session.id, name: app.name } } }], { history: false });
      ctl.select([existing.id]);
      frame(existing.id);
    },
    [ctl, insertNode, frame],
  );
  /** The editor is a card: show the one on this board, or place it, and open a file in it. */
  const openCode = useCallback(
    (path?: string, line?: number, at?: { x: number; y: number }) => {
      const existing = ctl.nodes.find((n) => n.kind === 'editor');
      const node = existing ?? insertNode('editor', at);
      ctl.select([node.id]);
      frame(node.id);
      if (path) ui.set({ openFile: { path, line, at: Date.now() } });
    },
    [ctl, insertNode, frame, ui],
  );
  const openCodeRef = useRef(openCode);
  openCodeRef.current = openCode;
  const openStudio = (kind: 'documents' | 'code' | 'markdown' | 'skills', extra: { path?: string; documentId?: string } = {}) => {
    if (kind === 'code') openCode(extra.path);
    else ui.set({ studio: { kind, ...extra } });
  };
  /** The right rail: what a project is worked with. What goes on the board is in the right-click ring. */
  const railItems: InsertItem[] = [
    { icon: 'code', label: 'Code editor', onClick: () => openStudio('code') },
    {
      icon: 'book',
      label: 'Studios',
      active: !!studio || panel === 'diagrams',
      onClick: (e) => {
        const r = e.currentTarget.getBoundingClientRect();
        setStudioMenu({ x: r.left - 220, y: r.top });
      },
    },
    { icon: 'brain', label: 'Memory', shortcut: shortcuts.memory.toUpperCase(), active: panel === 'memory', onClick: () => togglePanel('memory'), sepBefore: true },
    { icon: 'branch', label: 'Git', shortcut: shortcuts.git.toUpperCase(), active: panel === 'git', onClick: () => togglePanel('git') },
    {
      icon: 'loop',
      label: `Loops${loopsRunning ? ` — ${loopsRunning} running` : ''}`,
      shortcut: shortcuts.loops.toUpperCase(),
      active: panel === 'loops',
      onClick: () => togglePanel('loops'),
      badge: loopsRunning ? <span className="rail-badge tabular" aria-hidden="true">{loopsRunning}</span> : undefined,
    },
    { icon: 'map', label: 'Mission Map', shortcut: 'X', active: mission, onClick: () => ui.set({ mission: !mission }) },
    { icon: 'grid', label: 'Auto arrange', disabled: ctl.nodes.length < 2, onClick: arrange, sepBefore: true },
  ];
  /** A touch screen or a narrow window: everything stays one tap away in the left rail's flyout. */
  const insertItems: InsertItem[] = [
    { icon: 'checklist', label: 'Checklist', onClick: () => insertAt('checklist') },
    { icon: 'kanban', label: 'Kanban', onClick: () => insertAt('kanban') },
    { icon: 'calendar', label: 'Calendar', onClick: () => insertAt('calendar') },
    { icon: 'activity', label: 'Uptime monitor', onClick: () => insertAt('uptime') },
    { icon: 'key', label: 'Credentials', onClick: () => insertAt('credentials') },
    { icon: 'fileCode', label: 'File from project', onClick: () => ui.set({ filePicker: viewCenter() }) },
    { icon: 'orchestrator', label: 'Orchestrator card', onClick: () => insertAt('orchestrator') },
    { icon: 'memoryCard', label: 'Memory tile', onClick: () => insertAt('memory') },
    { icon: 'globe', label: 'Web page', onClick: () => insertNode('web') },
    { icon: 'monitor', label: 'Desktop app', onClick: () => ui.set({ appPicker: {} }) },
    { icon: 'branch', label: 'Branches card', onClick: () => insertNode('branches') },
    { icon: 'layout', label: 'Layouts', active: panel === 'layouts', onClick: () => togglePanel('layouts') },
    { icon: 'history', label: 'Snapshots', active: panel === 'snapshots', onClick: () => togglePanel('snapshots') },
    ...(coarse ? [{ icon: 'selectAll' as IconName, label: 'Select several', active: picking, onClick: () => setPicking((p) => !p) }] : []),
    // the right rail's own items, where there is no right rail
    ...(compact ? railItems : []),
  ];
  const studioItems: MenuEntry[] = [
    { type: 'heading', label: 'Studios' },
    { label: 'Documents', icon: 'book', onSelect: () => openStudio('documents') },
    { label: 'Diagrams', icon: 'diagram', onSelect: () => togglePanel('diagrams') },
    { label: 'Code', icon: 'code', onSelect: () => openStudio('code') },
    { label: 'Markdown', icon: 'markdown', shortcut: shortcuts.markdown.toUpperCase(), onSelect: () => openStudio('markdown') },
    { label: 'Skills', icon: 'puzzle', shortcut: shortcuts.skills.toUpperCase(), onSelect: () => openStudio('skills') },
  ];

  // ---- a focus that had to open this board first ----
  useEffect(() => {
    const t = orchStore.pendingFocus;
    if (!t || !ctl.node(t)) return;
    orchStore.pendingFocus = null;
    ctl.select([t]);
    frame(t);
  }, [state, ctl, frame]);

  // ---- uiActions from the hub (commands this client sent) ----
  useEffect(
    () =>
      orchStore.onUiActions((acts: UiAction[]) => {
        for (const a of acts) {
          switch (a.action) {
            case 'fit':
              fit();
              break;
            case 'openFile':
              if (a.path) openCodeRef.current(a.path, a.line);
              break;
            case 'zoomIn':
              void rf.zoomIn({ duration: 150 });
              break;
            case 'zoomOut':
              void rf.zoomOut({ duration: 150 });
              break;
            case 'zoomReset':
              void rf.zoomTo(1, { duration: 150 });
              break;
            case 'focus': {
              const t = a.target ?? '';
              if (a.workspaceId && a.workspaceId !== ctl.workspaceId) {
                orchStore.pendingFocus = t;
                navigate(boardPath(project.id, a.workspaceId));
                break;
              }
              const n = ctl.node(t) ?? ctl.nodes.find((x) => x.kind === 'terminal' && x.data.sessionId === t);
              if (n) {
                ctl.select([n.id]);
                frame(n.id);
              }
              break;
            }
            case 'openPanel':
              if (a.panel === 'loops' || a.panel === 'memory' || a.panel === 'git') ui.set({ panel: a.panel });
              else if (a.panel === 'mission') ui.set({ mission: true });
              else if (a.panel === 'settings') navigate('/settings');
              else if (a.panel === 'usage') usageUi.set(true);
              else if (a.panel === 'code') openCodeRef.current();
              else if (a.panel === 'documents' || a.panel === 'markdown' || a.panel === 'skills') ui.set({ studio: { kind: a.panel } });
              else if (a.panel === 'diagrams') ui.set({ panel: 'diagrams' });
              else toast.info(`${a.panel ? a.panel[0].toUpperCase() + a.panel.slice(1) : 'That panel'} isn’t available here.`);
              break;
            case 'closePanel':
              if (ui.get().studio) ui.set({ studio: null });
              else if (ui.get().mission) ui.set({ mission: false });
              else ui.set({ panel: null });
              break;
            case 'tool':
              if (a.tool === 'select' || a.tool === 'pen' || a.tool === 'eraser') ui.setTool(a.tool);
              break;
            case 'undo':
              ctl.undo();
              break;
            case 'redo':
              ctl.redo();
              break;
            case 'goWorkspace':
              if (a.workspaceId) navigate(boardPath(project.id, a.workspaceId));
              break;
          }
        }
      }),
    [ctl, ui, rf, fit, frame, project.id],
  );

  // ---- auto previews (B18): the hub adds the web tile; tell and offer to show it ----
  useEffect(() => {
    const fn = (e: { workspaceId?: string; nodeId?: string; url: string; port?: number }) => {
      if (e.workspaceId && e.workspaceId !== ctl.workspaceId) return;
      const nodeId = e.nodeId;
      const where = e.port ? `localhost:${e.port}` : e.url.replace(/^https?:\/\//, '');
      toast.notify('info', 'Preview opened', `${where} answers, so a web tile opened next to its console.`, nodeId ? { label: 'Show', onClick: () => frame(nodeId) } : undefined);
    };
    studioStore.onSuggested.add(fn);
    return () => {
      studioStore.onSuggested.delete(fn);
    };
  }, [ctl, frame]);

  // ---- menus ----
  /** Right-click on the empty board, clockwise from the top. */
  const ringItems = (at: { x: number; y: number }): RingItem[] => {
    const clip = recall();
    const agents = nt.agents.filter((a) => a.available !== false).sort(byPriority);
    const shapes: { kind: InsertKind; icon: IconName; label: string }[] = [
      { kind: 'note', icon: 'note', label: 'Sticky note' },
      { kind: 'text', icon: 'type', label: 'Text' },
      { kind: 'rect', icon: 'square', label: 'Rectangle' },
      { kind: 'ellipse', icon: 'circle', label: 'Ellipse' },
      { kind: 'diamond', icon: 'diamond', label: 'Diamond' },
      { kind: 'line', icon: 'line', label: 'Line' },
      { kind: 'arrow', icon: 'arrowUpRight', label: 'Arrow' },
    ];
    return [
      { icon: 'grid', label: 'Auto arrange', disabled: ctl.nodes.length < 2, note: 'Needs two cards', onSelect: arrange },
      {
        icon: 'sparkle',
        label: 'Spawn agent',
        shortcut: shortcuts.spawn.toUpperCase(),
        panel: (close) => (
          <SpawnPanel
            agents={agents}
            nt={nt}
            onSpawn={(a) => {
              close();
              void spawnAt(a.id === nt.agentId ? nt.choice() : { ...nt.choice(), preset: a.id, yolo: undefined, profileId: undefined, model: undefined, effort: undefined }, at);
            }}
          />
        ),
      },
      { icon: 'image', label: 'Insert image', onSelect: () => pickImage(at) },
      { icon: 'globe', label: 'Web page', onSelect: () => insertNode('web', at) },
      { icon: 'clipboard', label: 'Paste', shortcut: 'Ctrl+V', disabled: !clip, note: 'Nothing copied yet', onSelect: () => clip && pastePayload(ctl, clip, { x: at.x, y: at.y }) },
      {
        icon: 'shapes',
        label: 'Shapes and notes',
        panel: (close) => (
          <RingGrid
            label="Shapes and notes"
            items={shapes.map((s) => ({
              key: s.kind,
              mark: <Icon name={s.icon} />,
              label: s.label,
              onClick: () => {
                close();
                insertAt(s.kind, at);
              },
            }))}
          />
        ),
      },
      {
        icon: 'users',
        label: 'Compose group',
        panel: (close) => (
          <Suspense
            fallback={
              <div className="ring-loading" role="status">
                <span className="spinner" aria-hidden="true" />
              </div>
            }
          >
            <ComposeForm
              ctl={ctl}
              project={project}
              opts={{ slots: 6, group: true }}
              at={at}
              onClose={close}
              onSpawned={(ids) => {
                if (ids[0]) {
                  pendingFocus.current = { sessionId: ids[0], frame: compact };
                  focusPending();
                }
                // the team landed at the pointer, and may reach past the view
                window.setTimeout(() => {
                  const box = boundsOf(ctl.nodes.filter((n) => n.kind === 'terminal' && ids.includes(String(n.data.sessionId))).map(rectOf));
                  if (box) revealRect(box);
                }, 700);
              }}
            />
          </Suspense>
        ),
      },
    ];
  };

  /**
   * The ring's More: all else that goes on the board. What has its own button elsewhere is not
   * here: Code editor (right rail), Fit (left rail), zoom (bottom left).
   */
  const ringMore = (at: { x: number; y: number }): RingItem[] => [
    { icon: 'checklist', label: 'Checklist', onSelect: () => insertAt('checklist', at) },
    { icon: 'kanban', label: 'Kanban', onSelect: () => insertAt('kanban', at) },
    { icon: 'calendar', label: 'Calendar', onSelect: () => insertAt('calendar', at) },
    { icon: 'activity', label: 'Uptime monitor', onSelect: () => insertAt('uptime', at) },
    { icon: 'key', label: 'Credentials', onSelect: () => insertAt('credentials', at) },
    { icon: 'fileCode', label: 'File from project', onSelect: () => ui.set({ filePicker: at }) },
    { icon: 'branch', label: 'Branches card', onSelect: () => insertNode('branches', at) },
    { icon: 'memoryCard', label: 'Memory tile', onSelect: () => insertAt('memory', at) },
    { icon: 'orchestrator', label: 'Orchestrator card', onSelect: () => insertAt('orchestrator', at) },
    { icon: 'monitor', label: 'Desktop app', onSelect: () => ui.set({ appPicker: { at } }) },
    { icon: 'layout', label: 'Layouts', onSelect: () => togglePanel('layouts') },
    { icon: 'history', label: 'Snapshots', onSelect: () => togglePanel('snapshots') },
    { icon: 'selectAll', label: 'Select all', shortcut: 'Ctrl+A', disabled: !ctl.nodes.length, note: 'The board is empty', onSelect: () => ctl.select(ctl.nodes.map((n) => n.id)) },
  ];

  const nodeMenu = (ids: string[], withDelete = true): MenuEntry[] => {
    const list = ctl.nodes.filter((n) => ids.includes(n.id));
    const single = list.length === 1 ? list[0] : null;
    const terms = list.filter((n) => n.kind === 'terminal');
    const locked = list.length > 0 && list.every((n) => n.locked);
    const items: MenuEntry[] = [
      { label: 'Duplicate', icon: 'duplicate', shortcut: 'Ctrl+D', onSelect: () => duplicate(ctl, ids) },
      { label: 'Copy', icon: 'copy', shortcut: 'Ctrl+C', onSelect: () => void copyIds(ids) },
      { label: 'Bring to front', icon: 'front', onSelect: () => ctl.zOrder(ids, 'front') },
      { label: 'Send to back', icon: 'back', onSelect: () => ctl.zOrder(ids, 'back') },
    ];
    if (single && isFocusable(single)) items.unshift({ label: 'Focus', icon: 'fullscreen', onSelect: () => focusCard(single.id) });
    if (coarse && !picking) items.push({ label: 'Select several…', icon: 'selectAll', onSelect: () => setPicking(true) });
    if (single) items.push({ label: 'Zoom to tile', icon: 'fit', onSelect: () => frame(single.id) });
    items.push({
      label: locked ? 'Unlock position' : 'Lock position',
      icon: locked ? 'unlock' : 'lock',
      onSelect: () => ctl.apply(list.map((n) => ({ op: 'patch', id: n.id, set: { locked: !locked } }) as Op)),
    });
    if (terms.length) items.push({ label: terms.length === 1 ? 'Group console' : `Group ${terms.length} consoles`, icon: 'group', shortcut: 'Ctrl+G', onSelect: groupSelection });
    if (list.some((n) => n.kind === 'group') || groupsTouching(ctl.nodes, terms.map((n) => n.id)).length)
      items.push({ label: 'Ungroup', icon: 'ungroup', shortcut: 'Ctrl+Shift+G', onSelect: ungroupSelection });
    if (single && WIRE_SOURCES.has(single.kind))
      items.push({ label: 'Connect consoles…', icon: 'wire', onSelect: () => ui.set({ connectFrom: single.id }) });
    if (single && CONTENT_SOURCES.has(single.kind) && ctl.sync.state.edges.some((e) => e.kind === 'wire' && e.source === single.id))
      items.push({ label: 'Send to wired consoles', icon: 'send', onSelect: () => void sendTile(ctl.workspaceId, single.id) });
    if (single && (single.kind === 'note' || single.kind === 'text' || single.kind === 'shape')) {
      items.push({ label: 'Edit text', icon: 'pencil', onSelect: () => ui.set({ editing: single.id }) });
    }
    if (single?.kind === 'note') {
      items.push({
        type: 'swatches',
        label: 'Paper',
        value: readNote(single).color,
        options: NOTE_COLORS.map((c) => ({ value: c, label: c[0].toUpperCase() + c.slice(1), color: `var(--note-${c})` })),
        onSelect: (v) => ctl.updateData(single.id, { color: v }),
      });
    }
    if (single?.kind === 'image') items.push({ label: 'Markup…', icon: 'pen', onSelect: () => ui.set({ markup: single.id }) });
    if (single?.kind === 'web' && typeof single.data.previewId === 'string' && single.data.previewId) {
      const pid = single.data.previewId;
      items.push({ label: 'Console log card', icon: 'braces', onSelect: () => ctl.select([logCardFor(ctl, single, pid)]) });
    }
    const did = single && typeof single.data.diagramId === 'string' && single.data.diagramId.startsWith('dg_') ? single.data.diagramId : '';
    if (did) {
      const exp = (kind: 'png' | 'excalidraw') =>
        void (async () => {
          try {
            const [{ diagramPng, diagramExcalidraw }, { download, fileSafe }, { landedLook }, rec] = await Promise.all([
              import('../studio/diagrams/export'),
              import('../studio/documents/model'),
              import('../studio/diagrams/land'),
              diagramApi.get(did),
            ]);
            if (!rec.diagram) throw new Error('this diagram has no result');
            const style = rec.style === 'color' ? 'color' : 'ink';
            const name = fileSafe(rec.diagram.title || 'diagram');
            const look = landedLook(ctl.nodes, did);
            if (kind === 'png') {
              const url = URL.createObjectURL(await diagramPng(rec.diagram, style, look));
              const a = document.createElement('a');
              a.href = url;
              a.download = `${name}.png`;
              a.click();
              window.setTimeout(() => URL.revokeObjectURL(url), 1000);
            } else download(`${name}.excalidraw`, diagramExcalidraw(rec.diagram, style, look), 'application/json');
          } catch (err) {
            toast.error(`Export failed: ${err instanceof Error ? err.message : String(err)}`);
          }
        })();
      items.push({ label: 'Export diagram as PNG', icon: 'image', onSelect: () => exp('png') }, { label: 'Export as Excalidraw', icon: 'download', onSelect: () => exp('excalidraw') });
    }
    if (single?.kind === 'file' && typeof single.data.path === 'string' && single.data.path) {
      const path = single.data.path;
      const mid = typeof single.data.machineId === 'string' ? single.data.machineId : '';
      items.push({ label: 'Copy path', icon: 'link', onSelect: () => void navigator.clipboard?.writeText(path).catch(() => undefined) });
      if (mid === project.machineId && single.data.view !== 'folder')
        items.push(
          single.data.view === 'markdown'
            ? { label: 'Open in Markdown studio', icon: 'markdown', onSelect: () => openStudio('markdown', { path }) }
            : { label: 'Open in the editor card', icon: 'code', onSelect: () => openStudio('code', { path }) },
        );
      if (mid && single.data.view !== 'folder')
        items.push({
          label: 'Show folder',
          icon: 'folderOpen',
          onSelect: () => void addPath(ctl, mid, dirName(path), { x: single.x + single.w + 40, y: single.y }, { dir: true, centre: false }),
        });
    }
    if (!withDelete) return items;
    items.push('sep');
    if (terms.length) {
      items.push({
        label: terms.length === list.length ? (terms.length > 1 ? `Close ${terms.length} terminals…` : 'Close terminal…') : 'Delete…',
        icon: 'trash',
        danger: true,
        onSelect: () => requestDelete(ids),
      });
    } else {
      items.push({ label: 'Delete', icon: 'trash', danger: true, shortcut: 'Del', onSelect: () => ctl.deleteNodes(ids) });
    }
    return items;
  };
  nodeMenuRef.current = nodeMenu;

  const edgeMenu = (id: string): MenuEntry[] => {
    const e = state.edges.find((x) => x.id === id);
    if (!e) return [];
    const remove: MenuEntry = {
      label: e.kind === 'wire' ? 'Disconnect wire' : e.kind === 'forward' ? 'Remove forward' : 'Remove line',
      icon: 'trash',
      danger: true,
      shortcut: 'Del',
      onSelect: () => ctl.apply([{ op: 'delEdge', id }]),
    };
    if (e.kind !== 'forward') return [remove];
    const set = (patch: Record<string, unknown>) => ctl.apply([{ op: 'putEdge', edge: { ...e, data: { ...e.data, ...patch } } }]);
    return [
      { type: 'heading', label: 'Send' },
      { label: 'All results as one digest', checked: e.data.mode !== 'split', onSelect: () => set({ mode: 'digest' }) },
      { label: 'Split the work between targets', checked: e.data.mode === 'split', onSelect: () => set({ mode: 'split' }) },
      { type: 'heading', label: 'When' },
      { label: 'After every finished turn', checked: e.data.trigger !== 'order', onSelect: () => set({ trigger: 'turn' }) },
      { label: 'Only after orders', checked: e.data.trigger === 'order', onSelect: () => set({ trigger: 'order' }) },
      'sep',
      remove,
    ];
  };

  // ---- drops from the browser ----
  const onDragOver = (e: DragEvent) => {
    if (!Array.from(e.dataTransfer.types).includes('Files')) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
    setDropHover(true);
  };
  const onDrop = (e: DragEvent) => {
    setDropHover(false);
    if (!Array.from(e.dataTransfer.types).includes('Files')) return;
    e.preventDefault();
    const items = Array.from(e.dataTransfer.items ?? []);
    const dirs = items.filter((it) => it.webkitGetAsEntry?.()?.isDirectory).length;
    const files = Array.from(e.dataTransfer.files).filter((f) => f.size > 0 || f.type);
    if (dirs) toast.info('Folders can’t be dropped from the browser. Use “File from project” to add a folder card.');
    if (!files.length) return;
    const p = rf.screenToFlowPosition({ x: e.clientX, y: e.clientY });
    setDrop({ files: files.slice(0, 20), at: p });
  };

  const minimapColor = useCallback((n: FlowNode) => {
    const b = n.data.node;
    if (b.kind === 'terminal') return sessionOf(b)?.color || 'var(--c-grey)';
    if (b.kind === 'note') return `var(--note-${readNote(b).color})`;
    if (b.kind === 'web' || b.kind === 'weblog') return 'var(--edge-preview)';
    return 'var(--minimap-node)';
  }, []);

  const showDraw = tool !== 'select' && tool !== 'hand';
  const spaceHeld = useKeyPress(voice.enabled && voice.holdSpace ? null : 'Space');
  const hand = tool === 'hand' || (spaceHeld && !showDraw);
  // The wheel belongs to the board. A tile only gets it while it has the focus: it is the one
  // selected tile, or the keyboard is in it. Over any other tile a swipe moves the board, in
  // every direction, as over empty board. A swipe that brings a tile under the resting pointer
  // goes on moving the board, also over the focused one, until the pointer moves or presses.
  useEffect(() => {
    const el = canvasRef.current;
    if (!el) return;
    let at: { x: number; y: number } | null = null;
    const release = () => {
      at = null;
      el.classList.remove('is-swiping');
    };
    // :focus-visible, or a tile that was only clicked and then deselected would keep the wheel
    const hasFocus = (tile: Element) => !!tile.querySelector(':focus-visible') || (tile.classList.contains('selected') && el.querySelectorAll('.react-flow__node.selected').length === 1);
    const onWheel = (e: WheelEvent) => {
      const target = e.target as Element;
      const renderer = target.closest('.react-flow__renderer');
      if (!renderer) return;
      const tile = target.closest('.react-flow__node');
      if (target.closest('.nowheel') && (!tile || (!at && hasFocus(tile)))) return;
      at = { x: e.clientX, y: e.clientY };
      el.classList.add('is-swiping');
      if (!tile) return;
      // Nothing in the tile may see it, not its scrollers and not a terminal's own listener. The
      // board gets a copy from outside the tile, and treats it like any other swipe or pinch.
      e.preventDefault();
      e.stopPropagation();
      const { deltaX, deltaY, deltaZ, deltaMode, clientX, clientY, screenX, screenY, ctrlKey, shiftKey, altKey, metaKey } = e;
      renderer.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaX, deltaY, deltaZ, deltaMode, clientX, clientY, screenX, screenY, ctrlKey, shiftKey, altKey, metaKey }));
    };
    const onMove = (e: PointerEvent) => {
      if (at && Math.hypot(e.clientX - at.x, e.clientY - at.y) > 4) release();
    };
    el.addEventListener('wheel', onWheel, { capture: true, passive: false });
    window.addEventListener('pointermove', onMove, true);
    window.addEventListener('pointerdown', release, true);
    return () => {
      el.removeEventListener('wheel', onWheel, true);
      window.removeEventListener('pointermove', onMove, true);
      window.removeEventListener('pointerdown', release, true);
    };
  }, []);
  const terminalHint = nt.agent ? `${nt.agent.name}${nt.machine && nt.machine.id !== project.machineId ? ` on ${nt.machine.name}` : ''}` : '';
  const initialViewport = useMemo(() => loadViewport(ctl.workspaceId), [ctl.workspaceId]);
  // Decided once, when the board first loads: React Flow (re)fits whenever this prop is
  // true and nodes appear, which must not happen while someone is working.
  const fitRef = useRef<boolean | null>(null);
  if (status === 'ready' && fitRef.current === null) fitRef.current = !initialViewport && state.nodes.length > 0;
  const fitOnInit = fitRef.current ?? false;
  const moveTimer = useRef(0);
  const layerTimer = useRef(0);

  return (
    <BoardEnvContext.Provider value={env}>
      <div
        className={`board-canvas ${selection.size > 1 ? 'is-many-selected' : ''} ${arranging ? 'is-arranging' : ''} ${showDraw ? 'is-drawing' : ''} ${hand ? 'is-hand' : ''} ${dropHover ? 'is-drop' : ''} ${connecting || connectFrom ? 'is-connecting' : ''}`}
        ref={canvasRef}
        tabIndex={-1}
        onMouseMove={(e) => {
          pointer.current = rf.screenToFlowPosition({ x: e.clientX, y: e.clientY });
        }}
        onMouseLeave={() => {
          pointer.current = null;
        }}
        onDragOver={onDragOver}
        onDragLeave={(e) => {
          if (e.currentTarget === e.target) setDropHover(false);
        }}
        onDrop={onDrop}
      >
        {status === 'error' && (
          <div className="board-status card" role="alert">
            <div className="empty-state">
              <span className="empty-icon">
                <Icon name="alertCircle" />
              </span>
              <h3>Could not load the board</h3>
              <p>{syncError}</p>
              <Button variant="primary" icon="refresh" onClick={() => void ctl.sync.load()}>
                Reload board
              </Button>
            </div>
          </div>
        )}
        {status === 'loading' && <BoardLoading />}
        {status === 'ready' && (
          <ReactFlow<FlowNode, FlowEdge>
            nodes={shownNodes}
            nodeTypes={NODE_TYPES}
            edges={edges}
            edgeTypes={EDGE_TYPES}
            onEdgesChange={(changes: EdgeChange<FlowEdge>[]) => {
              let next: Set<string> | null = null;
              for (const c of changes) {
                if (c.type !== 'select') continue;
                next ??= new Set(edgeSel);
                if (c.selected) next.add(c.id);
                else next.delete(c.id);
              }
              if (next) setEdgeSel(next);
            }}
            onEdgeClick={(e, edge) => {
              setEdgeSel(new Set([edge.id]));
              ctl.select([]);
              // a finger has no right-click: a tap on a line opens what right-click does
              if (coarse) setMenu({ x: e.clientX, y: e.clientY, at: rf.screenToFlowPosition({ x: e.clientX, y: e.clientY }), ids: null, edge: edge.id });
            }}
            onEdgeContextMenu={(e, edge) => {
              e.preventDefault();
              setEdgeSel(new Set([edge.id]));
              setMenu({ x: e.clientX, y: e.clientY, at: rf.screenToFlowPosition({ x: e.clientX, y: e.clientY }), ids: null, edge: edge.id });
            }}
            onConnectStart={() => setConnecting(true)}
            onConnectEnd={onConnectEnd}
            connectionRadius={36}
            connectionLineComponent={ConnectionWire}
            onNodeClick={(e, node) => {
              const from = ui.get().connectFrom;
              if (!from || node.id === from) return;
              const fromNode = ctl.node(from);
              if (!fromNode) {
                ui.set({ connectFrom: null });
                return;
              }
              e.stopPropagation();
              const ok = connectTo(from, node.id);
              // a forward from a console is one target at a time
              if (ok && (fromNode.kind === 'terminal' || fromNode.kind === 'group')) {
                ui.set({ connectFrom: null });
                toast.success('Forward added: finished turns are passed on. Right-click the dashed line to change it.');
              }
            }}
            onNodesChange={onNodesChange}
            onNodeDragStart={(e, _n, ns) => onDragStart(e, ns)}
            onNodeDrag={(e, _n, ns) => onDrag(e, ns)}
            onNodeDragStop={onDragStop}
            onSelectionDragStart={(e, ns) => onDragStart(e, ns)}
            onSelectionDrag={(e, ns) => onDrag(e, ns)}
            onSelectionDragStop={onDragStop}
            onNodeDoubleClick={onNodeDoubleClick}
            onNodeContextMenu={(e, node) => {
              e.preventDefault();
              const ids = ctl.selection.has(node.id) ? [...ctl.selection] : [node.id];
              if (!ctl.selection.has(node.id)) ctl.select([node.id]);
              openMenu(e.clientX, e.clientY, ids, sizesInMenu(e.target, ctl.node(node.id)?.kind ?? '', ids.length));
            }}
            onSelectionContextMenu={(e) => {
              e.preventDefault();
              openMenu(e.clientX, e.clientY, [...ctl.selection]);
            }}
            onPaneContextMenu={(e) => {
              e.preventDefault();
              openMenu(e.clientX, e.clientY, null);
            }}
            onPaneClick={() => {
              setEdgeSel(EMPTY);
              if (ui.get().editing) ui.set({ editing: null });
              (document.activeElement as HTMLElement | null)?.blur?.();
              canvasRef.current?.focus({ preventScroll: true });
            }}
            onMoveStart={() => {
              // Own compositor layer while the view moves: panning then just moves pixels
              // instead of repainting every tile each frame (~10× cheaper with 100 tiles).
              // Removed at the end so text is re-rasterised crisply at the new zoom.
              window.clearTimeout(layerTimer.current);
              canvasRef.current?.classList.add('is-moving');
            }}
            onMoveEnd={(_e, vp) => {
              window.clearTimeout(layerTimer.current);
              layerTimer.current = window.setTimeout(() => canvasRef.current?.classList.remove('is-moving'), 400);
              window.clearTimeout(moveTimer.current);
              // the focused view is this card's, not the board's
              if (!focusedRef.current) moveTimer.current = window.setTimeout(() => saveViewport(ctl.workspaceId, vp), 300);
            }}
            defaultViewport={initialViewport}
            fitView={fitOnInit}
            fitViewOptions={FIT_OPTIONS}
            minZoom={0.2}
            maxZoom={3}
            nodeExtent={EXTENT}
            panOnScroll={!coarse && !isFocusing}
            panOnDrag={isFocusing ? false : coarse || hand ? true : [1]}
            zoomOnScroll={!isFocusing}
            zoomOnPinch={!isFocusing}
            nodesDraggable={!isFocusing}
            selectionOnDrag={!coarse && !isFocusing}
            selectionMode={SelectionMode.Partial}
            multiSelectionKeyCode={['Shift', 'Meta', 'Control']}
            selectionKeyCode={null}
            deleteKeyCode={null}
            elevateNodesOnSelect={false}
            disableKeyboardA11y
            zoomOnDoubleClick={false}
            panActivationKeyCode={null}
            nodeDragThreshold={2}
            colorMode="dark"
            proOptions={{ hideAttribution: true }}
          >
            <Background variant={BackgroundVariant.Dots} gap={24} size={1.5} />
            <Panel position="bottom-left" className="bl-panel">
              <ZoomControls />
              <GitPill projectId={project.id} open={panel === 'git'} onOpen={() => togglePanel('git')} shortcut={shortcuts.git} />
            </Panel>
            <MiniMap<FlowNode>
              pannable
              zoomable
              nodeColor={minimapColor}
              nodeBorderRadius={8}
              style={{ width: MINIMAP_W, height: MINIMAP_H }}
              ariaLabel="Board overview"
            />
            {guides.length > 0 && (
              <ViewportPortal>
                <svg className="guides" aria-hidden="true">
                  {guides.map((g, i) =>
                    g.axis === 'v' ? (
                      <line key={i} x1={g.pos} x2={g.pos} y1={g.from - 24} y2={g.to + 24} vectorEffect="non-scaling-stroke" />
                    ) : (
                      <line key={i} y1={g.pos} y2={g.pos} x1={g.from - 24} x2={g.to + 24} vectorEffect="non-scaling-stroke" />
                    ),
                  )}
                </svg>
              </ViewportPortal>
            )}
            {showDraw && (
              <DrawLayer
                tool={tool}
                ctl={ctl}
                ui={ui}
                penColor={penColor}
                penWidth={penWidth}
                onSpawn={(at) => void spawnAt(nt.choice(), at)}
                onImage={pickImage}
                onContextMenu={(x, y) => openMenu(x, y, null)}
              />
            )}
          </ReactFlow>
        )}
        {status === 'ready' && state.nodes.length === 0 && !showDraw && (
          <div className="board-empty">
            <div className="card">
              <span className="empty-icon">
                <Icon name="grid" />
              </span>
              <h2>This board is empty</h2>
              <p>
                Start a terminal in <strong>{project.name}</strong>. Right-click the board to add agents, notes, shapes, web pages and
                everything else.
              </p>
              <span className="hint-down">
                {compact ? 'Type an order below, or tap the agent chip to start a console' : 'Type an order below, use New console, or press T and click the board'}
                <Icon name="arrowDown" />
              </span>
            </div>
          </div>
        )}
      </div>

      {status === 'ready' && (
        <>
          <ToolRail
            tool={tool}
            onTool={(t) => ui.setTool(t)}
            onFit={fit}
            onUndo={() => ctl.undo()}
            onRedo={() => ctl.redo()}
            canUndo={ctl.canUndo}
            canRedo={ctl.canRedo}
            terminalHint={terminalHint}
            insert={compact || coarse ? <InsertButtons items={insertItems} labelled={coarse} /> : undefined}
            shortcuts={shortcuts}
            spawnOpen={spawnOpen}
            onSpawnClick={openSpawn}
            onSpawnClose={() => setSpawnOpen(false)}
            spawnFlyout={
              <AgentFlyout
                machineId={nt.machineId}
                machineName={nt.machine?.name ?? 'This PC'}
                armed={nt.armed && nt.agentId === nt.armed ? nt.armed : ''}
                onPick={(a) => {
                  setArmed(a.id);
                  // stays open to choose its model: a click on the board closes it and places the agent
                  if (!a.models) setSpawnOpen(false);
                  ui.setTool('spawn');
                }}
                choice={nt.modelChoice}
                onChoice={nt.setModelChoice}
                onAll={() => {
                  setSpawnOpen(false);
                  setAllAgents(true);
                }}
                onClose={() => setSpawnOpen(false)}
              />
            }
          />
          <ResumeAllBanner workspaceId={ctl.workspaceId} />
          {focused && (
            <>
              <div className="focus-area" ref={focusAreaRef} aria-hidden="true" />
              <FocusBar ctl={ctl} node={focused} nodes={state.nodes} />
            </>
          )}
          <InsertRail items={railItems} />
          <Inspector ctl={ctl} ui={ui} selection={selectedNodes} tool={tool} penColor={penColor} penWidth={penWidth} />
          <CommandBar ctl={ctl} ui={ui} project={project} nt={nt} loopsRunning={loopsRunning} onArrange={arrange} />
          {connectFrom && <ConnectBanner ctl={ctl} from={connectFrom} onDone={() => ui.set({ connectFrom: null })} />}
          {picking && (
            <div className="connect-banner pick-banner" role="status">
              <Icon name="selectAll" />
              <span className="grow">
                {selection.size === 0 ? 'Tap cards to select them' : `${selection.size} selected · long-press one for what to do with them`}
              </span>
              <Button size="sm" variant="ghost" onClick={() => ctl.select(ctl.nodes.filter((n) => n.kind !== 'group').map((n) => n.id))}>
                All
              </Button>
              <Button size="sm" variant="primary" onClick={() => setPicking(false)}>
                Done
              </Button>
            </div>
          )}
        </>
      )}

      {panel === 'snapshots' && <SnapshotsPanel ctl={ctl} onClose={() => ui.set({ panel: null })} />}
      <Suspense fallback={null}>
        {panel === 'loops' && <LoopsPanel ctl={ctl} project={project} onClose={() => ui.set({ panel: null })} />}
        {panel === 'memory' && (
          <MemoryPanel
            project={project}
            onClose={() => ui.set({ panel: null })}
            onAddCard={() => {
              ui.set({ panel: null });
              insertAt('memory');
            }}
          />
        )}
        {panel === 'layouts' && (
          <LayoutsPanel
            ctl={ctl}
            project={project}
            onClose={() => ui.set({ panel: null })}
            onDeployed={() => {
              ui.set({ panel: null });
              window.setTimeout(fit, 600);
            }}
          />
        )}
        {mission && <MissionMap ctl={ctl} onClose={() => ui.set({ mission: false })} />}
        {composer && (
          <TeamComposer
            ctl={ctl}
            project={project}
            opts={composer}
            onClose={() => ui.set({ composer: null })}
            onSpawned={(ids) => {
              if (ids[0]) {
                pendingFocus.current = { sessionId: ids[0], frame: compact };
                focusPending();
              }
              // show the new team (placed right of the content by the hub)
              if (!compact) window.setTimeout(fit, 700);
            }}
          />
        )}
        {handoff && <HandoffDialog sessionId={handoff} workspaceId={ctl.workspaceId} projectId={project.id} onClose={() => ui.set({ handoff: null })} />}
        {panel === 'git' && <GitPanel project={project} workspaceId={ctl.workspaceId} onClose={() => ui.set({ panel: null })} />}
        {panel === 'diagrams' && <DiagramsPanel ctl={ctl} project={project} viewCenter={viewCenter} reveal={revealRect} onClose={() => ui.set({ panel: null })} />}
        {diagramRuns.length > 0 && <DrawingCards ctl={ctl} viewCenter={viewCenter} reveal={revealRect} />}
        {studio?.kind === 'documents' && (
          <DocumentsStudio
            project={project}
            initialDocument={studio.documentId}
            onClose={() => ui.set({ studio: null })}
            onAddToBoard={(doc, versionId) => {
              const s = DEFAULT_SIZE.document;
              const rect = placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), s.w, s.h);
              ctl.addNodes([ctl.make('document', rect, { documentId: doc.id, versionId, title: doc.title })]);
              ui.set({ studio: null });
              toast.success(`${doc.title || 'Document'} added to the board.`);
            }}
          />
        )}
        {studio?.kind === 'markdown' && <MarkdownStudio project={project} ctl={ctl} initialPath={studio.path} viewCenter={viewCenter} onClose={() => ui.set({ studio: null })} />}
        {studio?.kind === 'skills' && <SkillsStudio project={project} onClose={() => ui.set({ studio: null })} />}
      </Suspense>

      {allAgents && (
        <AllAgentsDialog
          machine={nt.machine}
          machineId={nt.machineId}
          armed={nt.agentId}
          onClose={() => setAllAgents(false)}
          onPick={(a) => {
            setArmed(a.id);
            ui.setTool('spawn');
          }}
        />
      )}

      {menu && !menu.edge && !menu.ids && <RingMenu x={menu.x} y={menu.y} label="Board actions" items={ringItems(menu.at)} more={ringMore(menu.at)} onClose={() => setMenu(null)} />}
      {menu && (menu.edge || menu.ids) && (
        <ContextMenu
          x={menu.x}
          y={menu.y}
          label={menu.edge ? 'Connection actions' : menu.ids && menu.ids.length > 1 ? `Actions for ${menu.ids.length} tiles` : `Actions for ${nodeLabelOf(ctl, menu.ids?.[0] ?? '')}`}
          items={menu.edge ? edgeMenu(menu.edge) : [...(menu.sizes ? [...deviceEntries(ctl, menu.ids ?? [], revealRect), 'sep' as const] : []), ...nodeMenu(menu.ids ?? [])]}
          onClose={() => setMenu(null)}
        />
      )}

      {studioMenu && <ContextMenu x={studioMenu.x} y={studioMenu.y} label="Studios" items={studioItems} onClose={() => setStudioMenu(null)} />}

      {confirmDel && (
        <ConfirmDialog
          title={(() => {
            const n = ctl.nodes.filter((x) => confirmDel.ids.includes(x.id) && x.kind === 'terminal' && isRunning(sessionOf(x))).length;
            if (!n) return 'Delete the orchestrator card?';
            return n === 1 ? 'End a running terminal?' : `End ${n} running terminals?`;
          })()}
          confirmLabel={
            ctl.nodes.some((x) => confirmDel.ids.includes(x.id) && x.kind === 'terminal' && isRunning(sessionOf(x)))
              ? confirmDel.cut
                ? 'Cut and end terminals'
                : 'Delete and end terminals'
              : 'Delete card'
          }
          danger
          onClose={() => setConfirmDel(null)}
          onConfirm={() => ctl.deleteNodes(confirmDel.ids)}
        >
          {ctl.nodes.some((x) => confirmDel.ids.includes(x.id) && x.kind === 'terminal' && isRunning(sessionOf(x))) ? (
            <p>
              Running terminals are killed and their scrollback is discarded. Undo (Ctrl+Z) starts fresh terminals with the same preset,
              machine and folder.
            </p>
          ) : (
            <p>Its wires are removed; the consoles keep running. Undo (Ctrl+Z) brings the card and its wires back.</p>
          )}
        </ConfirmDialog>
      )}

      {drop && <DropDialog files={drop.files} at={drop.at} ctl={ctl} project={project} onClose={() => setDrop(null)} />}

      {filePicker && (
        <FolderBrowser
          machineId={project.machineId}
          machineName={machine?.name ?? project.machineId}
          initialPath={project.path}
          title={`Add a file from ${project.name}`}
          chooseLabel="Add folder card"
          onClose={() => ui.set({ filePicker: null })}
          onChoose={(path) => {
            const at = filePicker;
            ui.set({ filePicker: null });
            void addPath(ctl, project.machineId, path, at, { dir: true });
          }}
          onChooseFile={(path) => {
            const at = filePicker;
            ui.set({ filePicker: null });
            void addPath(ctl, project.machineId, path, at).then((n) => n && toast.success(`Added ${baseName(path)}.`));
          }}
        />
      )}

      {appPicker && (
        <AppPicker
          project={project}
          onClose={() => ui.set({ appPicker: null })}
          onOpened={(app, session) => {
            const at = appPicker.at;
            ui.set({ appPicker: null });
            placeApp(app, session, at);
          }}
        />
      )}

      {markup && (
        <Suspense fallback={null}>
          <MarkupEditor ctl={ctl} nodeId={markup} onClose={() => ui.set({ markup: null })} />
        </Suspense>
      )}

      <input
        ref={imageInput}
        type="file"
        accept="image/png,image/jpeg,image/gif,image/webp"
        multiple
        hidden
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          e.target.value = '';
          const at = imageAt.current ?? viewCenter();
          imageAt.current = null;
          if (files.length) void pasteImages(files, at);
        }}
      />
    </BoardEnvContext.Provider>
  );
}

/**
 * Give a terminal node's xterm the keyboard (after the frame animation). A board that
 * just loaded may not have created the xterm yet, so retry for a moment.
 */
function focusTerminal(nodeId: string, delay: number, tries = 12) {
  window.setTimeout(() => {
    const el = document.querySelector<HTMLTextAreaElement>(`.react-flow__node[data-id="${CSS.escape(nodeId)}"] .xterm-helper-textarea`);
    if (el) el.focus();
    else if (tries > 0) focusTerminal(nodeId, 200, tries - 1);
  }, delay);
}

function nodeLabelOf(ctl: BoardController, id: string): string {
  const n = ctl.node(id);
  return n ? nodeLabel(n) : 'tile';
}

/** Top banner while a card (or a console's forward) is in Connect mode. */
function ConnectBanner({ ctl, from, onDone }: { ctl: BoardController; from: string; onDone: () => void }) {
  const n = ctl.node(from);
  useEffect(() => {
    if (!n) onDone();
  }, [n, onDone]);
  if (!n) return null;
  const forward = n.kind === 'terminal' || n.kind === 'group';
  const fromLead = n.kind === 'orchestrator';
  const s = n.kind === 'terminal' ? sessionOf(n) : undefined;
  const name = n.kind === 'terminal' ? (s?.handle ? `@${s.handle}` : 'this console') : nodeLabel(n);
  return (
    <div className="connect-banner" role="status">
      <Icon name={forward ? 'forward' : 'wire'} />
      <span>
        {forward
          ? `Click the console or group that should receive ${name}’s results.`
          : `Click consoles, groups${fromLead ? ' or task boards' : ''} to wire them to ${name}; click again to disconnect.`}
      </span>
      <Button size="sm" variant={forward ? 'secondary' : 'primary'} onClick={onDone}>
        {forward ? 'Cancel' : 'Done'}
      </Button>
    </div>
  );
}

/** Over a focused card: back to the board, its name, and the cards before and after it. */
function FocusBar({ ctl, node, nodes }: { ctl: BoardController; node: BoardNode; nodes: BoardNode[] }) {
  const order = cardOrder(nodes);
  const i = order.findIndex((n) => n.id === node.id);
  const prev = i > 0 ? order[i - 1] : undefined;
  const next = i >= 0 && i < order.length - 1 ? order[i + 1] : undefined;
  const name = (n: BoardNode) => {
    if (n.kind !== 'terminal') return nodeLabelOf(ctl, n.id);
    const s = sessionOf(n);
    return s?.handle ? `@${s.handle}` : s?.name || 'Console';
  };
  return (
    <div className="focus-bar" role="toolbar" aria-label="Focused card">
      <Button size="sm" variant="ghost" icon="chevronLeft" onClick={leaveFocus}>
        Board
      </Button>
      <span className="focus-title truncate" title={name(node)}>
        {name(node)}
      </span>
      {order.length > 1 && (
        <span className="focus-count tabular" aria-label={`Card ${i + 1} of ${order.length}`}>
          {i + 1}/{order.length}
        </span>
      )}
      <Button size="sm" variant="ghost" iconOnly icon="chevronLeft" disabled={!prev} aria-label={prev ? `Previous card: ${name(prev)}` : 'Previous card'} onClick={() => prev && focusCard(prev.id)} />
      <Button size="sm" variant="ghost" iconOnly icon="chevronRight" disabled={!next} aria-label={next ? `Next card: ${name(next)}` : 'Next card'} onClick={() => next && focusCard(next.id)} />
    </div>
  );
}
