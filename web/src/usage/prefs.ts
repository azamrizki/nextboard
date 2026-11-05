// Usage check preferences (docs/API-studio.md §10, owner decision #2 still open).
//   - Which CLIs may be checked is the hub's switch (settings.usage.agents, every device).
//     Codex starts off there: starting Codex 0.165 interactively makes it write a [tui]
//     block (screen-reader detection marker) into its config.toml when it isn't there.
//   - Automatic checks are this browser's choice, off by default: a check then runs only
//     when you click the usage pill or Check again, because every Claude check adds two
//     lines (/usage, /exit) to its history.jsonl on that machine.

import { useSyncExternalStore } from 'react';
import { prefs } from '../lib/format';
import type { Settings } from '../lib/types';

export interface UsagePrefs {
  /** Check by itself when a board opens (max age 3 min). */
  auto: boolean;
}

export const USAGE_AGENTS = [
  { id: 'claude', name: 'Claude Code', short: 'Claude' },
  { id: 'codex', name: 'Codex', short: 'Codex' },
] as const;

const DEFAULTS: UsagePrefs = { auto: false };
const KEY = 'usage.prefs';
/** The hub's default, until its settings arrive. */
const HUB_DEFAULT_AGENTS: Record<string, boolean> = { codex: false };

/** May this CLI be checked? A key missing from the hub's switch means on. */
export function usageAgentOn(s: Settings, agent: string): boolean {
  return !s.agents.disabled?.[agent] && (s.usage?.agents ?? HUB_DEFAULT_AGENTS)[agent] !== false;
}

function read(): UsagePrefs {
  try {
    const v = JSON.parse(prefs.get(KEY) ?? 'null') as Partial<UsagePrefs> | null;
    return { auto: v?.auto === true };
  } catch {
    return DEFAULTS;
  }
}

let cur = read();
const listeners = new Set<() => void>();

export function setUsagePrefs(patch: Partial<UsagePrefs>) {
  cur = { ...cur, ...patch };
  prefs.set(KEY, JSON.stringify(cur));
  listeners.forEach((l) => l());
}

export function useUsagePrefs(): UsagePrefs {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => cur,
  );
}

// ---- popover open state (pill, command bar "open usage") ----

let open = false;
const openListeners = new Set<() => void>();
export const usageUi = {
  set(v: boolean) {
    open = v;
    openListeners.forEach((l) => l());
  },
  get: () => open,
  subscribe(fn: () => void) {
    openListeners.add(fn);
    return () => openListeners.delete(fn);
  },
};
export function useUsageOpen(): boolean {
  return useSyncExternalStore(usageUi.subscribe, usageUi.get);
}

/** Colour band of a usage percentage (amber ≥ 75 %, red ≥ 90 %, §10.1). */
export function usageLevel(pct: number): 'ok' | 'warn' | 'high' {
  return pct >= 90 ? 'high' : pct >= 75 ? 'warn' : 'ok';
}
