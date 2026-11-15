// Per-board UI state that many components read (tool, which tile is being edited, pen
// settings, open panels). A tiny external store so a tile only re-renders when its own
// slice changes.

import { createContext, useContext, useSyncExternalStore } from 'react';
import type { MenuEntry } from '../components/Menu';
import { prefs } from '../lib/format';
import type { Project } from '../lib/types';
import type { BoardController } from './controller';
import { INK_COLORS, INK_WIDTHS } from './palette';

export type Tool =
  | 'select'
  | 'hand'
  | 'spawn'
  | 'note'
  | 'text'
  | 'rect'
  | 'ellipse'
  | 'diamond'
  | 'line'
  | 'arrow'
  | 'pen'
  | 'eraser'
  | 'image'
  | 'checklist'
  | 'kanban'
  | 'calendar';

/** Tools that stay active after use (the others revert to Select after one placement). */
export const STICKY_TOOLS: ReadonlySet<Tool> = new Set(['select', 'hand', 'pen', 'eraser']);

export type Panel = 'snapshots' | 'loops' | 'memory' | 'layouts' | 'git' | 'diagrams' | null;

/** Full-screen studios (Phase 7), opened over the board. */
export type StudioKind = 'documents' | 'code' | 'markdown' | 'skills';
export interface StudioOpen {
  kind: StudioKind;
  /** Optional target: a path to open (code / markdown), a document id. */
  path?: string;
  documentId?: string;
  /** Markdown: the part whose notes to read (a card's file part, v0.9.0-beta.2); absent: the studio picks. */
  partId?: string;
}

/** Team composer (Spawn 4 / Group / add to a group / spawn for a card). */
export interface ComposerOpts {
  slots: number;
  /** Put the new consoles in a new group (name/role editable). */
  group: boolean;
  /** Add to this existing group node. */
  groupId?: string;
  /** Wire the new consoles to this orchestrator card. */
  orchestratorId?: string;
}

/**
 * A file to open in an editor card (v0.9.0-beta.2). nodeId: the card that takes it; without one
 * (an older sender) the first editor card does. The file is (machineId, path): path is absolute
 * on that machine; without machineId it is found from the part (partId) or, failing that, from
 * the part holding the path (lib/parts.ts partHoldingOn). partId: the part the sender meant
 * (the lead's UIAction.part); BoardView.openCode picks the card by it.
 */
export interface OpenFileRequest {
  nodeId?: string;
  machineId?: string;
  partId?: string;
  path: string;
  line?: number;
  at: number;
}

export interface UiState {
  tool: Tool;
  /** Node whose text is being edited (note, text, shape label). */
  editing: string | null;
  penColor: string;
  penWidth: number;
  panel: Panel;
  /** Image node open in the markup editor. */
  markup: string | null;
  /** Open the "file from project" picker. */
  filePicker: { x: number; y: number } | null;
  /** Open the desktop app picker; `at` is where the tile goes (free spot when absent). */
  appPicker: { at?: { x: number; y: number } } | null;
  /** Phase 6 overlays. */
  mission: boolean;
  composer: ComposerOpts | null;
  /** Session being handed off (dialog open). */
  handoff: string | null;
  /** Card in Connect mode: clicks on consoles / groups toggle a wire from it. */
  connectFrom: string | null;
  /** Phase 7: the open studio overlay. */
  studio: StudioOpen | null;
  /** A file asked for from elsewhere on the board, for an editor card. */
  openFile: OpenFileRequest | null;
}

function initial(): UiState {
  const w = Number(prefs.get('penWidth'));
  return {
    tool: 'select',
    editing: null,
    penColor: prefs.get('penColor') ?? INK_COLORS[0].value,
    penWidth: (INK_WIDTHS as readonly number[]).includes(w) ? w : INK_WIDTHS[1],
    panel: null,
    markup: null,
    filePicker: null,
    appPicker: null,
    mission: false,
    composer: null,
    handoff: null,
    connectFrom: null,
    studio: null,
    openFile: null,
  };
}

export class UiStore {
  private state: UiState = initial();
  private listeners = new Set<() => void>();
  get = () => this.state;
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  set(patch: Partial<UiState>) {
    this.state = { ...this.state, ...patch };
    if (patch.penColor) prefs.set('penColor', patch.penColor);
    if (patch.penWidth) prefs.set('penWidth', String(patch.penWidth));
    this.listeners.forEach((l) => l());
  }
  setTool(tool: Tool) {
    this.set({ tool, editing: null });
  }
}

export interface BoardEnv {
  ctl: BoardController;
  ui: UiStore;
  project: Project;
  /** Board coordinates of the visible centre (for inserts without a click position). */
  viewCenter: () => { x: number; y: number };
  /** Frame a node (double-click). */
  frame: (id: string) => void;
  /** Bring a board rect into view when it isn't fully visible. */
  reveal: (r: { x: number; y: number; w: number; h: number }) => void;
  /** Open the menu of a wire / forward edge at a screen point (edge labels). */
  edgeMenu?: (id: string, x: number, y: number) => void;
  /** What every card's "…" menu has, the same as its right-click menu. */
  cardMenu: (id: string, withDelete: boolean) => MenuEntry[];
}

export const BoardEnvContext = createContext<BoardEnv | null>(null);

export function useBoardEnv(): BoardEnv {
  const env = useContext(BoardEnvContext);
  if (!env) throw new Error('useBoardEnv outside the board');
  return env;
}

export function useUi<T>(select: (s: UiState) => T): T {
  const { ui } = useBoardEnv();
  return useSyncExternalStore(ui.subscribe, () => select(ui.get()));
}

export function useIsEditing(id: string): boolean {
  return useUi((s) => s.editing === id);
}
