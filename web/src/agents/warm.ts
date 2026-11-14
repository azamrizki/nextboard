// Starting a project's asleep agents early (ROADMAP #50, docs/API-agents.md §28). A CLI takes
// a few seconds to come back from sleep, so the hub is told as soon as someone is about to open
// a project (a hover, a press, the page opening), not when its terminals connect.

import { useRef } from 'react';
import { api } from '../api';

const sent = new Map<string, number>();
const AGAIN_MS = 20_000;

/** Wakes the asleep agents of a project, or of one of its boards; at most once per 20 s. A hub
 *  from before sleeping agents answers 404, which changes nothing. */
export function warmProject(projectId: string, workspaceId?: string | null): void {
  const key = `${projectId}/${workspaceId ?? ''}`;
  const now = Date.now();
  if (now - (sent.get(key) ?? 0) < AGAIN_MS) return;
  sent.set(key, now);
  void api.wakeProject(projectId, workspaceId ?? undefined).catch(() => undefined);
}

/** Handlers for what opens a project: a mouse resting on it (150 ms, so sweeping across Home
 *  starts nothing), a press, or keyboard focus that stays (300 ms). */
export function useWarmIntent(projectId: string, workspaceId?: string | null) {
  const timer = useRef(0);
  const later = (ms: number) => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => warmProject(projectId, workspaceId), ms);
  };
  const cancel = () => window.clearTimeout(timer.current);
  return {
    onPointerEnter: (e: React.PointerEvent) => {
      if (e.pointerType === 'mouse') later(150);
    },
    onPointerLeave: cancel,
    onPointerDown: () => {
      cancel();
      warmProject(projectId, workspaceId);
    },
    onFocus: () => later(300),
    onBlur: cancel,
  };
}
