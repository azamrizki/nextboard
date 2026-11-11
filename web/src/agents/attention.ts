// Attention events (docs/API-agents.md §12) → in-app toast with "Show", plus a browser
// notification while the tab is hidden (permission is only requested from a button in
// Settings). Per-type filters live on the hub (settings.notifications); whether this
// browser shows toasts / system notifications is per client (localStorage).

import { useSyncExternalStore } from 'react';
import { api } from '../api';
import { toast } from '../components/Toast';
import { prefs } from '../lib/format';
import type { Attention, Session } from '../lib/types';
import { store } from '../store';
import { revealMonitor } from '../uptime/store';
import { openCredentialRequest } from '../vault/agents';
import { navigate } from '../lib/router';
import { jumpToSession } from './jump';

// ---- per-client preferences ----

export interface ClientNotifyPrefs {
  toasts: boolean;
  browser: boolean;
}

const prefListeners = new Set<() => void>();
function readPrefs(): ClientNotifyPrefs {
  return { toasts: prefs.get('notify.toasts') !== '0', browser: prefs.get('notify.browser') === '1' };
}
let clientPrefs = readPrefs();

export function setClientNotifyPrefs(patch: Partial<ClientNotifyPrefs>) {
  clientPrefs = { ...clientPrefs, ...patch };
  prefs.set('notify.toasts', clientPrefs.toasts ? '1' : '0');
  prefs.set('notify.browser', clientPrefs.browser ? '1' : '0');
  prefListeners.forEach((l) => l());
}

export function useClientNotifyPrefs(): ClientNotifyPrefs {
  return useSyncExternalStore(
    (fn) => {
      prefListeners.add(fn);
      return () => prefListeners.delete(fn);
    },
    () => clientPrefs,
  );
}

// ---- browser permission ----

export type Permission = NotificationPermission | 'unsupported';

export function browserPermission(): Permission {
  return typeof window !== 'undefined' && 'Notification' in window ? Notification.permission : 'unsupported';
}

/** Ask for permission (call from a click handler only). */
export async function requestBrowserPermission(): Promise<Permission> {
  if (browserPermission() === 'unsupported') return 'unsupported';
  try {
    const p = await Notification.requestPermission();
    prefListeners.forEach((l) => l());
    return p;
  } catch {
    return Notification.permission;
  }
}

// ---- delivery ----

const KIND_TOAST = {
  needsYou: 'warning',
  credential: 'warning',
  finished: 'success',
  error: 'error',
  uptimeDown: 'error',
  uptimeUp: 'success',
  certExpiring: 'warning',
  providerBudget: 'warning',
  providerSpent: 'error',
} as const;

/** Show what the attention is about. */
const showTarget = (a: Attention) =>
  a.kind === 'credential'
    ? openCredentialRequest()
    : a.kind.startsWith('provider')
      ? navigate('/settings/providers')
      : a.monitorId
        ? revealMonitor(a.projectId, a.monitorId)
        : void jumpToSession(a.sessionId);
const open = new Map<string, Notification>();

function showBrowser(a: Attention) {
  if (!clientPrefs.browser || browserPermission() !== 'granted' || !document.hidden) return;
  try {
    const key = a.monitorId || a.sessionId || a.kind;
    open.get(key)?.close();
    const n = new Notification(a.title, { body: a.body || undefined, tag: `nb-${key}` });
    n.onclick = () => {
      window.focus();
      showTarget(a);
      n.close();
    };
    n.onclose = () => {
      if (open.get(key) === n) open.delete(key);
    };
    open.set(key, n);
  } catch {
    /* some browsers only allow notifications from a service worker */
  }
}

/** The console the attention is about currently has the keyboard: no toast needed. */
function isFocused(sessionId: string): boolean {
  if (document.hidden) return false;
  return !!document.activeElement?.closest?.(`.nb-node[data-session="${CSS.escape(sessionId)}"]`);
}

function deliver(a: Attention) {
  showBrowser(a);
  if (!clientPrefs.toasts || isFocused(a.sessionId)) return;
  const kind = KIND_TOAST[a.kind as keyof typeof KIND_TOAST] ?? 'info';
  toast.notify(kind, a.title || 'Agent update', a.body || '', { label: 'Show', onClick: () => showTarget(a) });
}

/** Close a system notification once its console no longer needs you. */
function onSession(s: Session) {
  const n = open.get(s.id);
  if (n && s.agentState !== 'needsYou') {
    n.close();
    open.delete(s.id);
  }
}

/** Test notification from Settings (shows even while the tab is visible). */
export function sendTestNotification(): boolean {
  if (browserPermission() !== 'granted') return false;
  try {
    const n = new Notification('NextBoard notifications are on', { body: 'You will see agents that need you here while this tab is in the background.', tag: 'nb-test' });
    n.onclick = () => {
      window.focus();
      n.close();
    };
    return true;
  } catch {
    return false;
  }
}

/** Listen for attention events; returns the cleanup. Started once after login. */
export function startAttention(): () => void {
  let bootId = '';
  let seq = -1;
  const seen = new Set<string>();
  let stopped = false;

  const handle = (a: Attention) => {
    if (seen.has(a.id)) return;
    seen.add(a.id);
    if (seen.size > 400) seen.delete(seen.values().next().value as string);
    if (a.seq > seq) seq = a.seq;
    deliver(a);
  };

  // baseline: don't replay history on load
  api
    .attention(undefined, 1)
    .then((r) => {
      if (stopped) return;
      bootId = r.bootId;
      seq = Math.max(seq, r.seq);
    })
    .catch(() => undefined);

  const catchUp = () => {
    if (seq < 0) return;
    api
      .attention(seq)
      .then((r) => {
        if (stopped) return;
        if (r.bootId !== bootId) {
          // hub restarted: old sequence numbers mean nothing
          bootId = r.bootId;
          seq = r.seq;
          return;
        }
        (r.items ?? []).slice(-3).forEach(handle);
      })
      .catch(() => undefined);
  };

  const off = store.onEvent((ev) => {
    if (ev.type === 'attention') handle(ev.attention);
    else if (ev.type === 'snapshot') catchUp();
    else if (ev.type === 'session') onSession(ev.session);
  });
  const onVisible = () => {
    if (!document.hidden) {
      // back on the page: system notifications for visible consoles are noise
      for (const n of open.values()) n.close();
      open.clear();
    }
  };
  document.addEventListener('visibilitychange', onVisible);

  return () => {
    stopped = true;
    off();
    document.removeEventListener('visibilitychange', onVisible);
  };
}
