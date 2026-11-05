// Phase 7 live state fed by /api/events (git, github, previews, preview logs, documents,
// diagrams, usage, skills, streamed job output) plus REST loads. Same pattern as the
// other stores: immutable snapshot + useSyncExternalStore. Job state itself lives in
// orchStore (event "job").

import { useEffect, useSyncExternalStore } from 'react';
import type { HubEvent } from '../lib/types';
import { orchStore, useOrch } from '../orch/store';
import type { Job } from '../orch/types';
import { prefs } from '../lib/format';
import { store } from '../store';
import { docApi, gitApi, jobApi, previewApi, usageApi } from './api';
import type { GitHubStatus, GitRepoInfo, GitRepoList, GitSummary, GitTarget, Preview, PreviewLogEntry, StudioDocument, StudioEvent, UsageReport } from './types';

export interface StudioState {
  /** projectId → summary of the selected repository. */
  git: Record<string, GitSummary>;
  /** projectId → bumped on every git event (open panels refetch the full status). */
  gitRev: Record<string, number>;
  reviewRev: Record<string, number>;
  /** projectId → the repositories in its folder (absent until scanned). */
  gitRepos: Record<string, GitRepoList>;
  /** projectId → the selected repository (path in gitRepos; "" = the project's own). */
  gitRepo: Record<string, string>;
  github: Record<string, GitHubStatus>;
  previews: Record<string, Preview>;
  previewsLoaded: boolean;
  logs: Record<string, PreviewLogEntry[]>;
  /** jobId → streamed text so far (jobOutput events, in seq order). */
  output: Record<string, string>;
  documents: Record<string, StudioDocument[]>;
  /** projectId → bumped on diagram events. */
  diagramRev: Record<string, number>;
  usage: Record<string, UsageReport>;
  usageLoaded: boolean;
  /** machineId → bumped on skills events. */
  skillsRev: Record<string, number>;
}

const INITIAL: StudioState = {
  git: {},
  gitRev: {},
  reviewRev: {},
  gitRepos: {},
  gitRepo: {},
  github: {},
  previews: {},
  previewsLoaded: false,
  logs: {},
  output: {},
  documents: {},
  diagramRev: {},
  usage: {},
  usageLoaded: false,
  skillsRev: {},
};

/** The summaries with a project's entry taken from its scanned repository, if that was read. */
function withSummary(git: Record<string, GitSummary>, pid: string, items: GitRepoInfo[] | undefined, repo: string) {
  const it = items?.find((r) => r.path === repo);
  if (!it || it.error) return git;
  return { ...git, [pid]: { branch: it.branch, ahead: it.ahead, behind: it.behind, changed: it.changed } };
}

export const usageKey = (r: { machineId: string; agent: string; profileId?: string }) => `${r.machineId}|${r.agent}|${r.profileId ?? ''}`;

type Fn<T> = (v: T) => void;
const LOG_KEEP = 500;

class StudioStore {
  private state: StudioState = INITIAL;
  private listeners = new Set<() => void>();
  private installed = false;
  private outSeq: Record<string, number> = {};
  private fsListeners = new Map<string, Fn<Extract<StudioEvent, { type: 'fsChanged' }>>>();
  readonly onReset = new Set<Fn<Extract<StudioEvent, { type: 'usageReset' }>>>();
  readonly onSuggested = new Set<Fn<Extract<StudioEvent, { type: 'previewSuggested' }>>>();
  readonly onReviewOverlap = new Set<Fn<Extract<StudioEvent, { type: 'reviewOverlap' }>>>();

  get = () => this.state;
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  private set(patch: Partial<StudioState>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((l) => l());
  }

  install() {
    if (this.installed) return;
    this.installed = true;
    store.onEvent((ev) => this.apply(ev));
  }

  reset() {
    this.state = INITIAL;
    this.outSeq = {};
    this.listeners.forEach((l) => l());
  }

  apply(ev: HubEvent) {
    const e = ev as StudioEvent | HubEvent;
    switch (e.type) {
      case 'snapshot':
        // reconnected: previews and usage may have changed while we were away
        if (this.state.previewsLoaded) void this.loadPreviews();
        break;
      case 'git':
      case 'gitRepo': {
        const repo = e.type === 'gitRepo' ? e.repo : '';
        const list = this.state.gitRepos[e.projectId];
        this.set({
          git: (this.state.gitRepo[e.projectId] ?? '') === repo ? { ...this.state.git, [e.projectId]: e.status } : this.state.git,
          gitRev: { ...this.state.gitRev, [e.projectId]: (this.state.gitRev[e.projectId] ?? 0) + 1 },
          gitRepos: list
            ? { ...this.state.gitRepos, [e.projectId]: { ...list, items: list.items.map((it) => (it.path === repo ? { ...it, ...e.status, error: undefined } : it)) } }
            : this.state.gitRepos,
        });
        break;
      }
      case 'review':
        this.set({ reviewRev: { ...this.state.reviewRev, [e.projectId]: (this.state.reviewRev[e.projectId] ?? 0) + 1 } });
        break;
      case 'reviewOverlap':
        this.onReviewOverlap.forEach((fn) => fn(e));
        break;
      case 'github':
        this.set({ github: { ...this.state.github, [e.projectId]: e.github } });
        break;
      case 'preview':
        this.upsertPreview(e.preview);
        break;
      case 'previewRemoved': {
        if (!(e.id in this.state.previews)) break;
        const previews = { ...this.state.previews };
        delete previews[e.id];
        this.set({ previews });
        break;
      }
      case 'previewSuggested':
        this.onSuggested.forEach((fn) => fn(e));
        break;
      case 'previewLog':
        this.addLogs(e.previewId, e.entries ?? []);
        break;
      case 'document':
        this.upsertDocument(e.projectId, e.document);
        break;
      case 'documentRemoved': {
        const cur = this.state.documents[e.projectId];
        if (cur) this.set({ documents: { ...this.state.documents, [e.projectId]: cur.filter((d) => d.id !== e.documentId) } });
        break;
      }
      case 'diagram':
        this.set({ diagramRev: { ...this.state.diagramRev, [e.projectId]: (this.state.diagramRev[e.projectId] ?? 0) + 1 } });
        break;
      case 'usage':
        this.upsertUsage(e.report);
        break;
      case 'usageReset':
        this.onReset.forEach((fn) => fn(e));
        break;
      case 'skills':
        this.set({ skillsRev: { ...this.state.skillsRev, [e.machineId]: (this.state.skillsRev[e.machineId] ?? 0) + 1 } });
        break;
      case 'fsChanged':
        this.fsListeners.get(e.watchId)?.(e);
        break;
      case 'jobOutput': {
        const last = this.outSeq[e.jobId] ?? 0;
        if (e.seq <= last) break;
        this.outSeq[e.jobId] = e.seq;
        this.set({ output: { ...this.state.output, [e.jobId]: (this.state.output[e.jobId] ?? '') + e.text } });
        break;
      }
      default:
        break;
    }
  }

  // ---- mutations ----

  setGitSummary(pid: string, s: GitSummary) {
    const cur = this.state.git[pid];
    if (cur && cur.branch === s.branch && cur.ahead === s.ahead && cur.behind === s.behind && cur.changed === s.changed) return;
    this.set({ git: { ...this.state.git, [pid]: s } });
  }
  /** The repository a project's Git views work on. */
  gitTarget(pid: string): GitTarget {
    return { pid, repo: this.state.gitRepo[pid] ?? '' };
  }
  selectGitRepo(pid: string, repo: string) {
    if ((this.state.gitRepo[pid] ?? '') === repo) return;
    prefs.set(`gitRepo.${pid}`, repo);
    this.set({ gitRepo: { ...this.state.gitRepo, [pid]: repo }, git: withSummary(this.state.git, pid, this.state.gitRepos[pid]?.items, repo) });
  }
  setGitHub(pid: string, g: GitHubStatus) {
    this.set({ github: { ...this.state.github, [pid]: g } });
  }
  upsertPreview(p: Preview) {
    this.set({ previews: { ...this.state.previews, [p.id]: p } });
  }
  addLogs(id: string, entries: PreviewLogEntry[]) {
    if (!entries.length) return;
    const cur = this.state.logs[id] ?? [];
    const last = cur.length ? cur[cur.length - 1].seq : 0;
    const fresh = entries.filter((x) => x.seq > last);
    if (!fresh.length) return;
    this.set({ logs: { ...this.state.logs, [id]: [...cur, ...fresh].slice(-LOG_KEEP) } });
  }
  clearLogs(id: string) {
    this.set({ logs: { ...this.state.logs, [id]: [] } });
  }
  upsertDocument(pid: string, d: StudioDocument) {
    const cur = this.state.documents[pid] ?? [];
    const next = cur.some((x) => x.id === d.id) ? cur.map((x) => (x.id === d.id ? d : x)) : [d, ...cur];
    this.set({ documents: { ...this.state.documents, [pid]: next } });
  }
  setDocuments(pid: string, list: StudioDocument[]) {
    this.set({ documents: { ...this.state.documents, [pid]: list } });
  }
  upsertUsage(r: UsageReport) {
    this.set({ usage: { ...this.state.usage, [usageKey(r)]: r } });
  }
  /** Text streamed for a job so far ("" when none). */
  seedOutput(jobId: string, text: string) {
    if (this.state.output[jobId]) return;
    this.set({ output: { ...this.state.output, [jobId]: text } });
  }
  watchFs(watchId: string, fn: Fn<Extract<StudioEvent, { type: 'fsChanged' }>>) {
    this.fsListeners.set(watchId, fn);
    return () => this.fsListeners.delete(watchId);
  }

  // ---- loads ----

  private previewsLoading: Promise<void> | null = null;
  loadPreviews(): Promise<void> {
    this.previewsLoading ??= previewApi
      .list()
      .then((list) => {
        const previews: Record<string, Preview> = {};
        for (const p of list) previews[p.id] = p;
        this.set({ previews, previewsLoaded: true });
      })
      .catch(() => this.set({ previewsLoaded: true }))
      .finally(() => {
        this.previewsLoading = null;
      });
    return this.previewsLoading;
  }

  async loadUsage() {
    try {
      const items = await usageApi.reports();
      const usage = { ...this.state.usage };
      for (const r of items) usage[usageKey(r)] = r;
      this.set({ usage, usageLoaded: true });
    } catch {
      this.set({ usageLoaded: true });
    }
  }

  async loadDocuments(pid: string) {
    try {
      this.setDocuments(pid, await docApi.list(pid));
    } catch {
      /* the studio shows its own error */
    }
  }

  private reposLoading = new Map<string, Promise<void>>();
  /** Scans a project's folder once (force: again) and keeps the selection on a repository that exists. */
  loadGitRepos(pid: string, force = false): Promise<void> {
    if (this.state.gitRepos[pid] && !force) return Promise.resolve();
    let p = this.reposLoading.get(pid);
    if (!p) {
      p = gitApi
        .repos(pid)
        .catch((): GitRepoList => this.state.gitRepos[pid] ?? { items: [], truncated: false })
        .then((list) => {
          const items = list.items ?? [];
          const wanted = this.state.gitRepo[pid] ?? prefs.get(`gitRepo.${pid}`) ?? '';
          const repo = items.some((r) => r.path === wanted) ? wanted : (items.find((r) => r.path === '') ?? items[0])?.path ?? '';
          this.set({
            gitRepos: { ...this.state.gitRepos, [pid]: { ...list, items } },
            gitRepo: { ...this.state.gitRepo, [pid]: repo },
            git: withSummary(this.state.git, pid, items, repo),
          });
        })
        .finally(() => this.reposLoading.delete(pid));
      this.reposLoading.set(pid, p);
    }
    return p;
  }

  async loadGitSummary(pid: string) {
    try {
      await this.loadGitRepos(pid);
      const target = this.gitTarget(pid);
      const st = await gitApi.status(target);
      // the picker may have moved on while this was loading
      if (target.repo !== this.gitTarget(pid).repo) return null;
      if (st.isRepo) this.setGitSummary(pid, { branch: st.branch, ahead: st.ahead, behind: st.behind, changed: st.changed ?? st.files?.length ?? 0 });
      return st;
    } catch {
      return null;
    }
  }
}

export const studioStore = new StudioStore();

// ---- hooks ----------------------------------------------------------------------------

export function useStudio<T>(select: (s: StudioState) => T): T {
  return useSyncExternalStore(studioStore.subscribe, () => select(studioStore.get()));
}

/** Previews by id (loads the list once). */
export function usePreview(id: string | undefined): Preview | undefined {
  const p = useStudio((s) => (id ? s.previews[id] : undefined));
  const loaded = useStudio((s) => s.previewsLoaded);
  useEffect(() => {
    if (!loaded) void studioStore.loadPreviews();
  }, [loaded]);
  return p;
}

const TERMINAL = new Set(['done', 'failed', 'cancelled']);
export const jobActive = (j: Job | undefined | null) => !!j && !TERMINAL.has(j.state);

/**
 * A job by id: events keep it current; while it runs it is also polled every 2 s (the
 * hub limits job events to 2 per second and a reconnect may miss the last one).
 */
export function useJob(id: string | null | undefined): Job | undefined {
  const job = useOrch((s) => (id ? s.jobs[id] : undefined));
  const active = !job || jobActive(job);
  useEffect(() => {
    if (!id || !active) return;
    let stop = false;
    const tick = () =>
      jobApi
        .get(id)
        .then((j) => !stop && orchStore.upsertJob(j))
        .catch(() => undefined);
    if (!orchStore.get().jobs[id]) void tick();
    const t = window.setInterval(tick, 2000);
    return () => {
      stop = true;
      window.clearInterval(t);
    };
  }, [id, active]);
  return job;
}

export function useJobOutput(id: string | null | undefined): string {
  return useStudio((s) => (id ? (s.output[id] ?? '') : ''));
}

/** Wait for a job to finish (events or polling). */
export function waitJob(id: string, timeoutMs = 15 * 60_000): Promise<Job> {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    let done = false;
    const check = (j: Job | undefined) => {
      if (done || !j || !TERMINAL.has(j.state)) return;
      done = true;
      off();
      window.clearInterval(t);
      resolve(j);
    };
    const off = orchStore.subscribe(() => check(orchStore.get().jobs[id]));
    const t = window.setInterval(() => {
      if (Date.now() - started > timeoutMs) {
        done = true;
        off();
        window.clearInterval(t);
        reject(new Error('The job took too long.'));
        return;
      }
      jobApi
        .get(id)
        .then((j) => {
          orchStore.upsertJob(j);
          check(j);
        })
        .catch(() => undefined);
    }, 2000);
    check(orchStore.get().jobs[id]);
  });
}
