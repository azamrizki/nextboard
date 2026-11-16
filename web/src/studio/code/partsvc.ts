// One part of a project as the Code workbench works with it (v0.9.0-beta.2, docs/API-studio.md
// §7): its ref, machine and folder, its language servers (one LspHub per part), git status and
// marks, the review of what agents changed, the Laravel index, the file list of Go to file, the
// ignored set and the tasks list. Shared by every editor card that mounts the part; the board's
// Git panel may use one without an editor (it is a GitHost's data side).
//
// Kept by refOf(project, part) for every part: nothing here means "the first part".

import { toast } from '../../components/Toast';
import { prefs } from '../../lib/format';
import { mountOfPart, projectParts } from '../../lib/parts';
import type { Project, ProjectPart } from '../../lib/types';
import { filesApi, gitApi, laravelApi, reviewApi, type FileListing, type LaravelIndex } from '../api';
import { studioStore } from '../store';
import type { GitStatus, GitTarget, ProjectTask, ReviewSummary, StudioEvent } from '../types';
import { bindParts, docStore, type DocPart, type Holder } from './docs';
import { LspHub } from './lsp';
import type { monaco } from './monaco';
import { authorityOf, cleanAbs, fileKey, joinKey, keyOfUri, keyUnder, parentKey, splitKey, under, type FileKey } from './paths';

/** A part no card mounts stops its language servers after this long. */
const LSP_IDLE_MS = 5 * 60_000;
const FILES_MS = 30_000;

export interface GitMark {
  letter: string;
  tone: string;
}

const STATUS_TONE: Record<string, string> = { modified: 'modified', typechange: 'modified', added: 'added', untracked: 'added', renamed: 'renamed', deleted: 'deleted', conflict: 'deleted' };
const STATUS_LETTERS: Record<string, string> = { modified: 'M', typechange: 'T', added: 'A', untracked: 'U', renamed: 'R', deleted: 'D', conflict: '!' };

const services = new Map<string, PartService>();

/** What a card gives a part it mounts. */
export interface PartView extends Holder {
  log(line: string): void;
}

export class PartService implements DocPart {
  readonly projectId: string;
  readonly partId: string;
  name: string;
  readonly machineId: string;
  readonly root: string;
  readonly rootKey: FileKey;
  readonly ref: string;
  readOnly: boolean;
  readonly lsp: LspHub;

  git: GitStatus | null = null;
  gitVersion = 0;
  review: ReviewSummary | null = null;
  laravel: LaravelIndex | null = null;
  /** The part's tasks, as the Tasks panel last read them. */
  tasks: ProjectTask[] = [];
  readonly ignored = new Set<FileKey>();

  private gitFiles = new Map<FileKey, GitMark>();
  private gitDirs = new Map<FileKey, string>();
  private gitTimer = 0;
  private reviewMarks = new Map<FileKey, string[]>();
  private filesCache: { at: number; list: FileListing } | null = null;
  private mounts = 0;
  private lspTimer = 0;
  private listeners = new Set<() => void>();
  private views = new Set<PartView>();
  /** Cards that keep it (mounted or not): at none it lets its subscriptions go. */
  private holds = new Set<object>();
  private offs: (() => void)[] = [];
  private disposed = false;

  /** The part's service: one per "<project>~<part>". A part whose machine or folder changed gets a new one. */
  static get(project: Project, part: ProjectPart): PartService {
    const m = mountOfPart(project, part);
    let s = services.get(m.ref);
    if (s && (s.machineId !== m.machineId || s.root !== m.root)) {
      services.delete(m.ref);
      if (!s.holds.size) s.dispose();
      s = undefined;
    }
    if (!s) {
      s = new PartService(project, part);
      services.set(m.ref, s);
    }
    // renamed or made read-only in another tab or by the hub: every card that mounts it shows it
    if (s.name !== part.name || s.readOnly !== !!part.readOnly) {
      s.name = part.name;
      s.readOnly = !!part.readOnly;
      s.changed();
    }
    return s;
  }

  /** The service of a ref, when one exists in this tab. */
  static byRef(ref: string): PartService | null {
    return services.get(ref) ?? null;
  }

  /** The part of any project known to this tab whose folder holds a key: the deepest, mounted first. */
  static holding(key: FileKey): PartService | null {
    let best: PartService | null = null;
    for (const s of services.values()) {
      if (!keyUnder(key, s.rootKey)) continue;
      if (!best || (s.mounted && !best.mounted) || (s.mounted === best.mounted && s.root.length > best.root.length)) best = s;
    }
    return best;
  }

  private constructor(project: Project, part: ProjectPart) {
    const m = mountOfPart(project, part);
    this.projectId = project.id;
    this.partId = part.id;
    this.name = part.name;
    this.machineId = m.machineId;
    this.root = m.root;
    this.rootKey = m.rootKey;
    this.ref = m.ref;
    this.readOnly = !!part.readOnly;
    const authority = authorityOf(this.machineId);
    this.lsp = new LspHub({
      id: this.ref,
      ref: this.ref,
      root: this.root,
      machineId: this.machineId,
      authority,
      ensureModel: (abs) => docStore.ensureModel(fileKey(this.machineId, abs), this),
      reachedVia: (model) => this.reachedVia(model),
      log: (l) => this.log(l),
      changed: () => this.changed(),
    });
    // compat: the repository picked in the Git views was kept by the bare project id for the
    // first part (before v0.9.0-beta.2); it carries over to that part's ref once
    if (projectParts(project)[0]?.id === part.id && prefs.get(`gitRepo.${this.ref}`) === null) {
      const old = prefs.get(`gitRepo.${project.id}`);
      if (old !== null) prefs.set(`gitRepo.${this.ref}`, old);
    }
    let reviewRev = studioStore.get().reviewRev[this.ref] ?? 0;
    this.offs.push(
      studioStore.subscribe(() => {
        const rev = studioStore.get().reviewRev[this.ref] ?? 0;
        if (rev === reviewRev) return;
        reviewRev = rev;
        void this.refreshReview();
      }),
    );
    const overlap = (e: Extract<StudioEvent, { type: 'reviewOverlap' }>) => {
      if (e.projectId === this.ref) toast.info(`${e.agents.join(' and ')} both changed ${e.path}. Check it in Review.`);
    };
    studioStore.onReviewOverlap.add(overlap);
    this.offs.push(() => studioStore.onReviewOverlap.delete(overlap));
    let gitRepo = this.gitTarget.repo;
    this.offs.push(
      studioStore.subscribe(() => {
        if (gitRepo === this.gitTarget.repo) return;
        gitRepo = this.gitTarget.repo;
        this.git = null;
        void this.refreshGit();
      }),
    );
    void this.refreshReview();
    void this.refreshLaravel();
  }

  // ---- views ---------------------------------------------------------------------------

  /** A card keeps the part (its tree, marks): until it lets go, the subscriptions stay. */
  hold(by: object) {
    this.holds.add(by);
  }

  /** A card let go; the last one ends the part (its language servers stop). */
  drop(by: object) {
    this.holds.delete(by);
    if (this.holds.size) return;
    if (services.get(this.ref) === this) services.delete(this.ref);
    this.dispose();
  }

  private dispose() {
    if (this.disposed) return;
    this.disposed = true;
    window.clearTimeout(this.lspTimer);
    window.clearTimeout(this.gitTimer);
    this.lsp.dispose();
    for (const off of this.offs.splice(0)) off();
    this.listeners.clear();
    this.views.clear();
  }

  get mounted(): boolean {
    return this.mounts > 0;
  }

  /** A card shows the part: its language servers start (or resume) and git is read. */
  mount(view?: PartView) {
    if (view) this.views.add(view);
    this.mounts++;
    window.clearTimeout(this.lspTimer);
    if (this.mounts > 1) return;
    if (!this.lsp.servers.length) void this.lsp.start();
    else this.lsp.resume();
    void this.refreshGit();
  }

  /** A card closed; with none left the language servers stop after LSP_IDLE_MS. */
  unmount(view?: PartView) {
    if (view) this.views.delete(view);
    this.mounts = Math.max(0, this.mounts - 1);
    if (this.mounts > 0) return;
    window.clearTimeout(this.lspTimer);
    this.lspTimer = window.setTimeout(() => {
      if (!this.mounted) this.lsp.dispose();
    }, LSP_IDLE_MS);
  }

  /** The cards that mount it. */
  holders(): Iterable<PartView> {
    return this.views;
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  }

  /** Something about the part changed: every card that mounts it renders again. */
  changed() {
    for (const fn of [...this.listeners]) fn();
  }

  /** A line for the Output panel of every card that mounts the part. */
  log(line: string) {
    for (const v of this.views) v.log(line);
  }

  private reachedVia(model: monaco.editor.ITextModel): boolean {
    const k = keyOfUri(model.uri);
    return !!k && docStore.viaOf(k) === this;
  }

  // ---- paths ---------------------------------------------------------------------------

  /** The path of a key inside the part ("" = its folder); null outside it. */
  relOf(key: FileKey): string | null {
    if (!keyUnder(key, this.rootKey)) return null;
    const { abs } = splitKey(key);
    return abs === this.root ? '' : this.root === '/' ? abs.slice(1) : abs.slice(this.root.length + 1);
  }

  /** The key of a path inside the part ("" = its folder). */
  keyOf(rel: string): FileKey {
    const r = rel.replace(/^(?:\.\/|\/)+/, '');
    return r ? joinKey(this.rootKey, r) : this.rootKey;
  }

  // ---- git -----------------------------------------------------------------------------

  /** The repository the Git views work on (the picker of the Git panel and of source control). */
  get gitTarget(): GitTarget {
    return studioStore.gitTarget(this.ref);
  }

  /** Refresh soon, once: saves and file events come in bursts. */
  gitSoon() {
    window.clearTimeout(this.gitTimer);
    this.gitTimer = window.setTimeout(() => void this.refreshGit(), 700);
  }

  async refreshGit() {
    try {
      await studioStore.loadGitRepos(this.ref);
      const target = this.gitTarget;
      const st = await gitApi.status(target);
      // the picker moved on: its own refresh is under way
      if (target.repo !== this.gitTarget.repo) return;
      this.git = st;
      this.gitFiles.clear();
      this.gitDirs.clear();
      const base = cleanAbs(st.root ?? '') ?? this.root;
      for (const f of st.files ?? []) {
        const tone = STATUS_TONE[f.status] ?? 'modified';
        const key = fileKey(this.machineId, `${base}/${f.path}`);
        this.gitFiles.set(key, { letter: STATUS_LETTERS[f.status] ?? '•', tone });
        for (let d = parentKey(key); d && splitKey(d).abs !== '/' && keyUnder(d, this.rootKey); d = parentKey(d)) {
          // a conflict or deletion outranks a modification in the folder's colour
          if (!this.gitDirs.has(d) || tone === 'deleted') this.gitDirs.set(d, tone);
          if (d === this.rootKey) break;
        }
      }
    } catch {
      this.git = null;
      this.gitFiles.clear();
      this.gitDirs.clear();
    }
    this.gitVersion++;
    this.changed();
  }

  gitMark(key: FileKey): GitMark | undefined {
    return this.gitFiles.get(key);
  }

  gitDirTone(key: FileKey): string | undefined {
    return this.gitDirs.get(key);
  }

  /** The path inside the part's git repository (git API); null outside it. */
  repoRel(key: FileKey): string | null {
    const base = cleanAbs(this.git?.root ?? '');
    if (!this.git?.isRepo || !base) return null;
    const { machineId, abs } = splitKey(key);
    if (machineId !== splitKey(this.rootKey).machineId || abs === base || !under(abs, base)) return null;
    return base === '/' ? abs.slice(1) : abs.slice(base.length + 1);
  }

  // ---- review, Laravel -------------------------------------------------------------------

  async refreshReview() {
    try {
      this.review = await reviewApi.list(this.ref);
    } catch {
      this.review = { supported: false, agents: [] };
    }
    this.reviewMarks.clear();
    for (const a of this.review.agents) {
      for (const f of a.files) {
        const k = this.keyOf(f.path);
        this.reviewMarks.set(k, [...(this.reviewMarks.get(k) ?? []), a.name]);
      }
    }
    this.changed();
  }

  /** The agents with a change to this file nobody has looked at yet. */
  reviewMark(key: FileKey): string[] | undefined {
    return this.reviewMarks.get(key);
  }

  get reviewCount(): number {
    return this.reviewMarks.size;
  }

  async refreshLaravel() {
    try {
      this.laravel = await laravelApi.index(this.ref);
    } catch {
      /* no answer now: what is known stays */
    }
    this.changed();
  }

  // ---- files -----------------------------------------------------------------------------

  /** Every file of the part (ignored ones left out), relative to its folder, cached briefly. */
  async fileList(force = false): Promise<FileListing> {
    if (!force && this.filesCache && Date.now() - this.filesCache.at < FILES_MS) return this.filesCache.list;
    const list = await filesApi.list(this.ref);
    this.filesCache = { at: Date.now(), list };
    return list;
  }

  /** Files came or went: the next Go to file reads the list again. */
  filesChanged() {
    this.filesCache = null;
  }

  /** Which of a folder's entries git ignores (dimmed in the tree). */
  async loadIgnored(dirKey: FileKey, names: string[]) {
    const rel = this.relOf(dirKey);
    if (rel === null || !names.length) return;
    const paths = names.map((n) => (rel ? `${rel}/${n}` : n));
    try {
      const r = await filesApi.ignored(this.ref, paths);
      for (const p of paths) this.ignored.delete(this.keyOf(p));
      for (const p of r.items ?? []) this.ignored.add(this.keyOf(p.replace(/\/$/, '')));
      this.changed();
    } catch {
      /* an older hub: nothing is dimmed */
    }
  }
}

bindParts({ byRef: (ref) => PartService.byRef(ref), holding: (key) => PartService.holding(key) });
