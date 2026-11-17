// The hub's own update as the web follows it (docs/API.md Hub update): the check, then the job
// (download, checksum, swap), the restart, and the hub answering with the new version. Kept
// outside any component, so it goes on while About this hub is closed.

import { useSyncExternalStore } from 'react';
import { api, errorMessage, type Health, type HubUpdate } from '../api';
import { bundleScript, sameVersion } from '../lib/build';
import type { Job } from '../orch/types';
import { store } from '../store';
import { toast } from './Toast';

export type UpdateFlow =
  | { step: 'idle' }
  | { step: 'running'; target: string; jobId: string; text: string; pct: number | null }
  | { step: 'restarting'; target: string; slow: boolean; restartError?: string }
  | { step: 'updated'; target: string; reload: boolean }
  | { step: 'failed'; target: string; error: string };

export interface HubUpdateState {
  check: HubUpdate | null;
  checking: boolean;
  /** The hub could not be asked at all. */
  checkError: string;
  flow: UpdateFlow;
}

/** How long a check is reused here; the hub itself asks GitHub at most once an hour. */
const REUSE_MS = 5 * 60_000;
/** After this the restart is "taking long", and the panel says what may be wrong. */
const SLOW_MS = 45_000;
const GIVE_UP_MS = 10 * 60_000;

let state: HubUpdateState = { check: null, checking: false, checkError: '', flow: { step: 'idle' } };
let checkedAt = 0;
const listeners = new Set<() => void>();
const changed = new Set<(h: Health) => void>();

function set(patch: Partial<HubUpdateState>) {
  state = { ...state, ...patch };
  listeners.forEach((l) => l());
}

export const hubUpdateStore = {
  get: () => state,
  subscribe(fn: () => void) {
    listeners.add(fn);
    return () => {
      listeners.delete(fn);
    };
  },
  /** Called once the hub answers with the version it updated to. */
  onUpdated(fn: (h: Health) => void) {
    changed.add(fn);
    return () => {
      changed.delete(fn);
    };
  },
};

export function useHubUpdate(): HubUpdateState {
  return useSyncExternalStore(hubUpdateStore.subscribe, hubUpdateStore.get);
}

/** Asks the hub whether a newer release is out (no network call of its own on a dev build). */
export function loadUpdateCheck(force = false) {
  if (state.checking) return;
  if (!force && state.check && Date.now() - checkedAt < REUSE_MS) return;
  set({ checking: true });
  api.hubUpdate().then(
    (c) => {
      checkedAt = Date.now();
      set({ check: c, checking: false, checkError: '' });
      // an update started elsewhere (another tab, another browser): follow it here too
      if (c.jobId && state.flow.step === 'idle' && c.latest) follow(c.jobId, c.latest);
    },
    (err) => set({ checking: false, checkError: errorMessage(err) }),
  );
}

/** Records the owner's answer to the update popup, here and on the hub (for every browser). */
export async function answerReminder(action: 'skip' | 'later', version: string): Promise<void> {
  const r = await api.remindHubUpdate(action === 'skip' ? { action, version } : { action });
  if (state.check) set({ check: { ...state.check, skipped: r.skipped, remindAfter: r.remindAfter, remind: r.remind } });
}

/** Starts the update to target; throws what the hub refused, for the confirm dialog to show. */
export async function startUpdate(target: string): Promise<void> {
  const job = await api.startHubUpdate(target);
  follow(job.id, target, job);
}

function follow(jobId: string, target: string, first?: Job) {
  let over = false;
  set({ flow: { step: 'running', target, jobId, text: 'Starting…', pct: null } });
  const apply = (j: Job) => {
    if (over || j.id !== jobId) return;
    if (j.state === 'done') {
      stop();
      void waitForRestart(target);
    } else if (j.state === 'failed' || j.state === 'cancelled') {
      stop();
      set({ flow: { step: 'failed', target, error: j.error || 'The update did not finish.' } });
    } else {
      const pct = typeof j.progress?.pct === 'number' ? Math.round(j.progress.pct) : null;
      set({ flow: { step: 'running', target, jobId, text: j.progress?.text || 'Starting…', pct } });
    }
  };
  // its events, and a slow poll in case one is missed
  const off = store.onEvent((ev) => {
    if (ev.type === 'job') apply(ev.job);
  });
  const poll = window.setInterval(() => void api.job(jobId).then(apply, () => undefined), 2000);
  const stop = () => {
    over = true;
    off();
    window.clearInterval(poll);
  };
  if (first) apply(first);
}

const loadedBundle = () => document.querySelector<HTMLScriptElement>('script[type="module"][src]')?.getAttribute('src') ?? '';

/** Whether the restarted hub serves another web bundle than this page runs. */
async function bundleChanged(before: string): Promise<boolean> {
  try {
    const r = await fetch('/', { cache: 'no-store', credentials: 'same-origin' });
    const now = bundleScript(await r.text());
    return !before || !now || now !== before;
  } catch {
    return true;
  }
}

const sleep = (ms: number) => new Promise((done) => window.setTimeout(done, ms));

/** Polls /api/health until the hub answers with target: it stops, then the service starts it. */
async function waitForRestart(target: string) {
  set({ flow: { step: 'restarting', target, slow: false } });
  const before = loadedBundle();
  const started = Date.now();
  let slow = false;
  while (Date.now() - started < GIVE_UP_MS) {
    await sleep(1000);
    const h = await api.health().catch(() => null);
    if (h && sameVersion(h.version, target)) {
      const reload = await bundleChanged(before);
      checkedAt = 0;
      set({ flow: { step: 'updated', target, reload }, check: null });
      changed.forEach((fn) => fn(h));
      toast.success(`Updated to ${target}.`);
      return;
    }
    if (!slow && Date.now() - started > SLOW_MS) {
      slow = true;
      const c = await api.hubUpdate().catch(() => null);
      set({ flow: { step: 'restarting', target, slow: true, restartError: c?.restartError } });
    }
  }
}
