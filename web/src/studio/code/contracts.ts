// The layers of the Code workbench across parts (v0.9.0-beta.2, docs/API-studio.md §7): types
// only, so that the core model and the panels build against the same names in parallel.
//
//   DocStore (docs.ts, one per browser tab)  by FileKey            the shared Doc and its Monaco model
//   PartService (partsvc.ts)                  by "<project>~<part>"  ref, machine, LSP hub, git, review, Laravel, file list
//   View (workspace.ts, one per card place)   by viewKey             tabs, tree, search, debug, terminals, persistence
//
// Two cards that show one file share its Doc and model (and dirty state). LSP hubs belong to parts,
// not cards. Nothing here falls back to "the first part": a key names its machine, a part is named
// by id. Paths are paths.ts FileKeys; fs calls take splitKey(key), never a key.

import type { Placement } from '../../lib/parts';
import type { Project, ProjectPart } from '../../lib/types';
import type { FileListing, LaravelIndex } from '../api';
import type { ProjectTask, ReviewSummary } from '../types';
import type { GitHost } from './CommitGraph';
import type { LspHub } from './lsp';
import type { monaco } from './monaco';
import type { FileKey, Mount, ViewPaths } from './paths';
import type { CommitTab, DiffSource, Doc, GitMark, RemoteRun, Tab } from './workspace';

// ---- DocStore -----------------------------------------------------------------------------

/**
 * Today's Doc, keyed by FileKey and shared by every card that shows the file. readOnly is the
 * file's own reason ("Not UTF-8", too large); "Outside the project" is the view's (readOnlyOf).
 */
export type SharedDoc = Omit<Doc, 'path'> & { path: FileKey };

/** A view holding docs. */
export interface DocHolder {
  /** Something about a doc it holds changed: render again. */
  changed(): void;
  /** A doc it holds got an edit: its own tabs of that doc stop being previews. */
  edited(key: FileKey): void;
}

export interface DocSaveOptions {
  /** The ref of the part holding the file (fs/write projectId): the outside check and the review snapshot go by it. */
  ref: string;
  /** The saving view's remote-confirm dialog. */
  run: RemoteRun;
  force?: boolean;
  outside?: boolean;
  /** The saving view's format-on-save (its editor), run before the write. */
  format?: (doc: SharedDoc) => Promise<unknown>;
}

/** docs.ts: every open file of the browser tab, whatever card shows it. */
export interface DocStoreApi {
  get(key: FileKey): SharedDoc | undefined;
  /** Hold a file for a view (loads it the first time). via: the part the view reached it through, whose LSP hub attaches the model when the holding part's hub does not run. */
  acquire(key: FileKey, holder: DocHolder, via: PartServiceApi | null): SharedDoc;
  /** Let go of it; the model is disposed once no view holds it and no lend is left. */
  release(key: FileKey, holder: DocHolder): void;
  /** A model for a file no tab shows (LSP edits, peek): lent, let go after LENT_MS unless held. */
  ensureModel(key: FileKey, via: PartServiceApi | null): Promise<monaco.editor.ITextModel | null>;
  /** monaco.Uri.from(uriPartsOf(key)). */
  uriOf(key: FileKey): monaco.Uri;
  keyOfModel(m: monaco.editor.ITextModel): FileKey | null;
  holders(key: FileKey): ReadonlySet<DocHolder>;
  /** Writes the file; then tells the holding part (LSP saved, git soon, Laravel refresh, on-save commands). */
  save(key: FileKey, o: DocSaveOptions): Promise<boolean>;
  reload(key: FileKey): Promise<void>;
  restoreDraft(key: FileKey): void;
  dropDraft(key: FileKey): void;
  reopenWithEncoding(key: FileKey, encoding: string): Promise<void>;
  /** A watch saw the file change. Idempotent per mtime, so every card's watch may feed it. */
  onDiskChange(key: FileKey, exists: boolean, mtime: string | undefined): void;
}

// ---- PartService --------------------------------------------------------------------------

/**
 * partsvc.ts: one part of a project, whatever card mounts it. It is the GitHost data side; a
 * view adds openCommit, activeTab, openView and saveAll around it (build that adapter with
 * getters: spreading a class instance drops its getters).
 */
export interface PartServiceApi extends Pick<GitHost, 'gitTarget' | 'gitVersion' | 'git' | 'refreshGit'> {
  readonly projectId: string;
  readonly partId: string;
  readonly name: string;
  readonly machineId: string;
  /** The part's folder on its machine, clean. */
  readonly root: string;
  readonly rootKey: FileKey;
  /** refOf(projectId, partId): every API call about the part, and its studio-store state (reviewRev, git). */
  readonly ref: string;
  readonly readOnly: boolean;
  /** One hub per part, shared by the cards that mount it. */
  readonly lsp: LspHub;

  gitSoon(): void;
  gitMark(key: FileKey): GitMark | undefined;
  gitDirTone(key: FileKey): string | undefined;
  /** The path inside the part's git repository; null outside it. */
  repoRel(key: FileKey): string | null;

  readonly review: ReviewSummary | null;
  readonly reviewCount: number;
  reviewMark(key: FileKey): string[] | undefined;
  refreshReview(): Promise<void>;

  readonly laravel: LaravelIndex | null;
  refreshLaravel(): Promise<void>;

  /** Go to file's list of the part (cached 30 s). */
  fileList(force?: boolean): Promise<FileListing>;
  readonly ignored: ReadonlySet<FileKey>;
  loadIgnored(dirKey: FileKey, names: string[]): Promise<void>;
  /** The part's tasks, as the Tasks panel last read them. */
  tasks: ProjectTask[];

  /** Counted by the views that mount the part: starts and resumes its LSP, stops it after LSP_IDLE_MS. */
  mount(): void;
  unmount(): void;
  subscribe(fn: () => void): () => void;
}

/** PartService.get(project, part): by refOf(project.id, part.id); a part whose machine or folder changed gets a new one. */
export interface PartServiceRegistry {
  get(project: Project, part: ProjectPart): PartServiceApi;
}

// ---- View (the card) ----------------------------------------------------------------------

/** A tab's own view instead of a file, now naming its part (id). */
export type PartSpecialView =
  | { kind: 'review'; part: string; session: string; /** relative to the part's folder, as the hub reports it */ path: string; agent: string }
  | { kind: 'history'; part: string; path: FileKey }
  | { kind: 'graph'; part: string }
  | { kind: 'pr'; part: string; number: number; title: string; repo: string };

/** A commit tab, now naming its part (id). */
export type PartCommitTab = CommitTab & { part: string };

/** A tab of a view: path is a key ("" for a pane, a commit or a special view). */
export type KeyedTab = Omit<Tab, 'path' | 'view' | 'commit'> & { path: FileKey | ''; view?: PartSpecialView; commit?: PartCommitTab };

/** View.open's options (today's). */
export interface OpenOptions {
  pin?: boolean;
  keepFocus?: boolean;
  side?: boolean;
  group?: number;
  line?: number;
  col?: number;
  endCol?: number;
  source?: boolean;
}

/** One file's problems in a view's Problems panel. */
export interface ViewProblems {
  key: FileKey;
  /** view.label(key). */
  label: string;
  items: { line: number; col: number; message: string; severity: number; source?: string }[];
}

/** What the panels use of a card's view (workspace.ts Workspace). */
export interface ViewApi {
  readonly projectId: string;
  readonly placement: Placement;
  /** paths.ts viewKey: persistence (wb.<viewKey>) and the registry. */
  readonly viewKey: string;
  readonly paths: ViewPaths;
  /** paths.rootKey: ROOT_KEY in a Root view, else the part's folder. */
  readonly rootKey: FileKey;
  /** The mounted parts, in part order. */
  readonly parts: readonly PartServiceApi[];
  /** The part of the project holding a key, mounted here or not; null outside every part. */
  partOf(key: FileKey): PartServiceApi | null;
  /** The part of the active doc, else of the selected tree row, else the first mount. */
  readonly activePart: PartServiceApi;

  label(key: FileKey): string;
  relLabel(key: FileKey): string;
  partRel(key: FileKey): { mount: Mount; rel: string } | null;
  resolve(input: string, machineId?: string): FileKey | null;
  /** Why a doc cannot be edited here ("" = it can): its own reason, or outside every part of the project. */
  readOnlyOf(doc: SharedDoc): string;

  /** The docs this view holds (the shared objects). */
  readonly docs: ReadonlyMap<FileKey, SharedDoc>;
  readonly activeTab: KeyedTab | null;
  readonly activeDoc: SharedDoc | null;
  open(key: FileKey, o?: OpenOptions): void;
  /** fileKey(machineId, abs), then open. */
  openReal(machineId: string, abs: string, o?: OpenOptions): void;
  openDiff(key: FileKey, source: DiffSource, group?: number): void;
  openCommit(part: PartServiceApi, hash: string, o?: { label?: string; pin?: boolean }): void;
  openView(view: PartSpecialView, o?: { pin?: boolean }): void;
  saveAll(): Promise<unknown>;
  /** Problems of the docs this view mounts or holds, from every mounted part's hub. */
  problems(): ViewProblems[];

  changed(): void;
  subscribe(fn: () => void): () => void;
}

/** Workspace.get(project, placement): by viewKey; the same place on two boards is one view. */
export interface ViewRegistry {
  get(project: Project, placement: Placement): ViewApi;
}
