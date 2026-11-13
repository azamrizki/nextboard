// Hub settings document (docs/API-agents.md §11): loaded once after login, kept live by
// the "settings" event, changed with JSON Merge Patch. Hubs without /api/settings
// (Phase 4) get the defaults and read-only UI.

import { api, isStatus } from '../api';
import type { Settings, ShortcutMap } from '../lib/types';
import { store, useSettings } from '../store';

export const DEFAULT_SHORTCUTS: ShortcutMap = {
  select: 'v',
  hand: 'h',
  spawn: 't',
  pen: 'p',
  eraser: 'e',
  git: 'g',
  memory: 'm',
  skills: 's',
  loops: 'l',
  markdown: 'd',
  needsYou: 'n',
};

export const DEFAULT_SETTINGS: Settings = {
  schemaVersion: 1,
  agents: {
    defaults: {},
    paths: {},
    profiles: {},
    roleAgents: {},
    yoloDefault: false,
    liveStatus: true, ideLink: true,
    hookChannel: 'auto',
    resumeOnRestart: 'auto',
    sleepAfterMin: 30,
    lazyStart: true,
  },
  notifications: { needsYou: true, finished: true, error: true, finishedMinSec: 30 },
  shortcuts: DEFAULT_SHORTCUTS,
};

let supported: boolean | null = null;

/** Fetch the settings document (idempotent per login). */
export async function loadSettings(): Promise<void> {
  try {
    store.setSettings(await api.settings());
    supported = true;
  } catch (err) {
    if (isStatus(err, 404)) supported = false;
  }
}

export function settingsSupported(): boolean | null {
  return supported;
}

/** Merge-patch the hub settings; the response (full document) is applied right away. */
export async function patchSettings(patch: Record<string, unknown>): Promise<Settings> {
  const next = await api.patchSettings(patch);
  store.setSettings(next);
  return next;
}

/** Settings with defaults filled in (never null). */
/** The account a console of this agent gets when none is chosen ("" = the CLI's own login). */
export const defaultProfileOf = (s: Settings, machineId: string, agentId: string) => s.agents.profiles?.[machineId]?.[agentId] ?? '';

/** The empty choice of an account list says which account that is. */
export function defaultLoginLabel(profiles: { id: string; name: string }[], defaultId: string): string {
  const p = profiles.find((x) => x.id === defaultId);
  return p ? `${p.name} (default)` : 'CLI’s own login (default)';
}

export function useEffectiveSettings(): Settings {
  const s = useSettings();
  return s ?? DEFAULT_SETTINGS;
}

export function shortcutsOf(s: Settings | null): ShortcutMap {
  return { ...DEFAULT_SHORTCUTS, ...(s?.shortcuts ?? {}) };
}

export function currentShortcuts(): ShortcutMap {
  return shortcutsOf(store.getState().settings);
}

export function useShortcuts(): ShortcutMap {
  return shortcutsOf(useSettings());
}

/** Validation per §11: one printable character, unique, not x, not space. */
export function shortcutProblem(key: string, action: keyof ShortcutMap, map: ShortcutMap): string {
  if ([...key].length !== 1) return 'Use a single character.';
  if (key === ' ') return 'Space is reserved for push-to-talk.';
  if (key.toLowerCase() === 'x') return 'X always opens the Mission Map.';
  if (/[\p{C}]/u.test(key)) return 'Use a printable character.';
  const clash = (Object.keys(map) as (keyof ShortcutMap)[]).find((k) => k !== action && map[k].toLowerCase() === key.toLowerCase());
  if (clash) return `Already used by “${SHORTCUT_LABELS[clash]}”.`;
  return '';
}

export const SHORTCUT_LABELS: Record<keyof ShortcutMap, string> = {
  select: 'Select',
  hand: 'Hand (move the board)',
  spawn: 'Spawn agent',
  pen: 'Pen',
  eraser: 'Eraser',
  needsYou: 'Jump to the console that needs you',
  git: 'Git panel',
  memory: 'Project memory',
  skills: 'Skills',
  loops: 'Loops',
  markdown: 'Markdown studio',
};

/** Actions that exist in this version (the rest are reserved for later phases). */
export const ACTIVE_SHORTCUTS: (keyof ShortcutMap)[] = ['select', 'hand', 'spawn', 'pen', 'eraser', 'needsYou', 'git', 'markdown', 'skills', 'loops', 'memory'];
